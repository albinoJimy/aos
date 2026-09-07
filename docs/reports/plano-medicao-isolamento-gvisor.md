# Plano de medição — isolamento do componente gVisor (validação do AOS-103)

| Campo | Valor |
|---|---|
| Documento | Plano de medição — contenção do sandbox gVisor no componente host-side |
| Data | 2026-09-07 |
| Origem | `analises/13` §6.2 — «nada a jusante do POST ao executor foi medido»; e §2.4, que mediu o despacho **até** à fronteira e declarou tudo o que vem depois por medir |
| Alvo | `deploy/server/gvisor/` (componente + guest + seed), serviço `gvisor` de `deploy/server/docker-compose.prod.yml:449-466` |
| Estado | **Primeira execução feita (2026-09-07, §9)** — blocos A/B/C/E contidos num Docker descartável; falta o bloco D e o gVisor sobre virtualização real |

---

## 1. O que este plano mede, e o que não mede

**Mede:** se a fronteira que o componente gVisor afirma ter é a fronteira que ele tem. Concretamente,
se um payload hostil entregue por uma tool call sandboxed consegue (a) ler fora da raiz semeada,
(b) escrever seja onde for, (c) falar para a rede, (d) elevar privilégios, ou (e) observar artefactos
de uma execução anterior.

**Não mede:** o caminho de governação até ao executor. Isso já está medido em `analises/13` §2.4 e
não se repete aqui. Também não mede o Firecracker, que tem fronteira diferente (virtualização de
hardware, ADR-004) e merece plano próprio.

## 2. A armadilha central deste plano

Há **três camadas** entre a tool call e o kernel do host, e elas falham de formas diferentes:

| Camada | Onde vive | O que impõe |
|---|---|---|
| **L1 — verificação do guest** | `guest/main.go:71-84` | `read` é o único verbo aceite; o caminho é limpo e tem de ficar sob `seedRoot`, senão «path foge da raiz semeada» |
| **L2 — configuração OCI** | `component/main.go:199-221` | uid/gid 65532, zero capabilities, `noNewPrivileges`, namespaces próprios com rede vazia, seed montada `rbind,ro,nosuid,nodev,noexec` |
| **L3 — interposição de syscalls** | `runsc`, plataforma `systrap` | A contenção a sério |

O próprio guest declara, em `guest/main.go:11-13`, que **L1 é defesa em profundidade e não é ela que
segura a fronteira**. E é exactamente aí que este plano pode falhar por construção:

> **Uma tentativa de fuga enviada como tool call normal morre em L1 e nunca chega a L3. Se a
> registarmos como «contido», teremos provado que o guest tem um `if`, e concluído que o runsc
> funciona. É o defeito que a auditoria 13 documenta em quatro sítios — um teste que passa pela
> razão errada.**

Por isso **todo o caso deste plano declara a camada que exercita**, e os casos de L3 exigem
**contornar L1 deliberadamente**. Sem isso, o plano é teatro.

### 2.1 Como se chega a L3

Três vias, por ordem de preferência:

1. **Guest de diagnóstico** — substituir, só no ambiente de medição, o binário do guest por um que
   aceite verbos adicionais (`write`, `connect`, `exec`, `stat`, `listdir`). Passa a ser possível
   pedir ao processo dentro da sandbox que tente o que o guest de produção nunca tentaria. É a via
   mais fiel: mesma configuração OCI, mesmo runsc, código hostil por dentro.
2. **Invocação directa do `runsc`** com o mesmo bundle que `component/main.go` monta, trocando o
   `process.args` por um payload de teste. Prova L2+L3 sem passar pelo componente.
3. **Payload no `path`** — só serve para L1. Útil como controlo, inútil como prova de L3.

**Sem a via 1 ou a 2, este plano não deve ser executado**: produziria um relatório verde sobre a
camada errada.

## 3. Pré-condições

- Host **Linux** com `docker` e capacidade de correr um contentor `privileged: true`
  (`docker-compose.prod.yml:455`). O `runsc` com plataforma `systrap` **não exige `/dev/kvm`**
  (`cmd/aos/gvisorexecutor.go:14-17`) — é essa a razão de o gVisor ser a opção num convidado sem
  virtualização aninhada.
- **Ambiente descartável**, não o que serve tráfego real. Um contentor privilegiado ao lado dos
  outros serviços é o vizinho mais poderoso que se pode ter, e vamos correr código hostil lá dentro.
- **Autorização escrita** do dono da infraestrutura para (a) o deployment e (b) os testes de
  contenção. São coisas distintas.
- Um instantâneo do estado do host antes (processos, portas, montagens) para comparação posterior.

## 4. Casos de medição

Cada caso: **hipótese falsificável · camada · como executar · resultado que a refuta · resultado que
a confirma**. «Contido» nunca é a ausência de erro — é a presença do erro certo, na camada certa.

### Bloco A — confinamento do sistema de ficheiros

| # | Hipótese | Camada | Execução | Contido significa |
|---|---|---|---|---|
| **A1** | O caminho `../../etc/passwd` sai da raiz semeada | **L1** | tool call normal com `path: "../../etc/passwd"` | erro «path foge da raiz semeada» **do guest**. *Não prova L3.* É o controlo que mostra que L1 está viva |
| **A2** | Um caminho absoluto `/etc/passwd` sai da raiz | **L1** | idem com `path: "/etc/passwd"` | idem A1. `filepath.Clean("/"+path)` neutraliza-o |
| **A3** | Um `open("/etc/passwd")` **de dentro do processo** lê o host | **L3** | guest de diagnóstico: verbo `read_abs` que chama `os.ReadFile` sem verificação | `ENOENT` ou `EACCES`. Se **ler** o `/etc/passwd` do host, a fronteira não existe — **defeito crítico** |
| **A4** | Um symlink dentro da seed aponta para fora e é seguido | **L3** | semear `seed/fuga -> /etc/passwd` antes de arrancar; ler `fuga` | conteúdo inacessível. Se devolver o ficheiro do host, o `rbind,ro` não contém o alvo do link |
| **A5** | A raiz semeada é escrevível | **L2** | guest de diagnóstico: `write` em `/seed/x` | `EROFS`. A montagem declara `ro` (`component/main.go:221`) |
| **A6** | O rootfs fora da seed é escrevível | **L2/L3** | `write` em `/tmp/x` e em `/x` | ou `EROFS`, ou escrita **confinada ao bundle efémero** — o que A7 verifica |
| **A7** | Uma execução observa artefactos da anterior | **L2** | escrever ficheiro na execução N; procurá-lo em N+1 | ausente. `component/main.go:7` afirma bundle novo por chamada, apagado no fim — **é uma afirmação por verificar** |

### Bloco B — rede

| # | Hipótese | Camada | Execução | Contido significa |
|---|---|---|---|---|
| **B1** | O processo alcança a rede interna do compose | **L2** | guest de diagnóstico: `connect` a `litellm:4000` e ao IP do gateway docker | falha de resolução ou `ENETUNREACH`. O namespace de rede é declarado vazio (`component/main.go:199`) |
| **B2** | O processo alcança a Internet | **L2** | `connect` a um IP público, sem DNS | `ENETUNREACH` |
| **B3** | O processo alcança o próprio componente em `:9101` | **L2** | `connect` a `127.0.0.1:9101` de dentro | recusa. Um guest que fale com o seu executor fecha um ciclo indesejável |
| **B4** | Há loopback utilizável dentro do sandbox | **L2** | `listen` + `connect` em `127.0.0.1` | irrelevante para a fronteira, mas registar: informa o modelo de ameaça |

### Bloco C — privilégios

| # | Hipótese | Camada | Execução | Contido significa |
|---|---|---|---|---|
| **C1** | O processo corre como root | **L2** | `getuid`/`getgid` | 65532/65532 (`component/main.go:205`) |
| **C2** | O processo tem capabilities | **L2** | ler `/proc/self/status` (`CapEff`) | vazio |
| **C3** | `setuid` eleva privilégio | **L2** | binário setuid na seed + execução | falha. A montagem tem `nosuid` **e** `noexec` (`:221`) — este caso valida os dois |
| **C4** | `noNewPrivileges` está activo | **L2** | `/proc/self/status` → `NoNewPrivs: 1` | 1 |
| **C5** | `/proc` e `/sys` do host são visíveis | **L3** | listar `/proc`, ler `/proc/1/cmdline`, `/sys/class` | vista do sandbox, não do host. Ver o `cmdline` do init do host é **defeito crítico** |
| **C6** | Há acesso a dispositivos | **L2/L3** | listar `/dev`; tentar `/dev/kvm`, `/dev/mem` | conjunto mínimo; `/dev/mem` ausente |

### Bloco D — recursos e denial-of-service

| # | Hipótese | Camada | Execução | Contido significa |
|---|---|---|---|---|
| **D1** | Uma bomba de memória derruba o componente | fora do runsc | alocar acima de `GVISOR_MEM_LIMIT` (1 GiB por omissão, `:456`) | o **contentor** limita; o componente sobrevive e devolve erro. Se morrer, uma tool call derruba a sandbox de toda a gente |
| **D2** | Uma fork bomb esgota os pids | fora do runsc | fork em ciclo | `pids_limit: 512` (`:457`) trava. Mesmo critério de sobrevivência |
| **D3** | Uma execução infinita bloqueia o componente | componente | ciclo sem fim | o `context` do `exec.CommandContext` (`component/main.go:154`) mata. **Verificar se há timeout** — o cliente do nó tem 60 s (`gvisorexecutor.go:37`), o componente pode não ter |
| **D4** | Um payload gigante no `write` esgota o disco | componente | `write` de vários GiB | recusa ou limite. O campo `Write []byte` viaja em JSON, sem limite declarado |

### Bloco E — o contrato e a atribuição

| # | Hipótese | Camada | Execução | Contido significa |
|---|---|---|---|---|
| **E1** | Um `command` desconhecido é interpretado | L1 | `command: "exec"`, `"delete"`, `""` | «comando desconhecido» — o `default` recusa (`guest/main.go:83`) |
| **E2** | O componente aceita pedidos não autenticados | componente | POST directo a `:9101` de outro contentor | **nenhuma autenticação está declarada** no compose (`:465-466` só faz `expose`). Registar como facto, não como surpresa: a fronteira é a rede interna |
| **E3** | O `run_id`/`step_id` chegam ao componente | componente | inspeccionar o pedido recebido | **já medido**: chegam errados — AOS-383. Incluído para o plano ficar completo, não para remedir |

## 5. Controlos negativos — obrigatórios

Sem estes, um verde não distingue «contido» de «não chegou lá».

- **N1 — o caminho está vivo.** Uma leitura **legítima** de `seed/notes` devolve o conteúdo. Se
  falhar, todos os «contidos» deste plano são vacuosos: nada estava a ser executado.
- **N2 — a instrumentação detecta.** Com o guest de diagnóstico, um `read_abs` de um ficheiro
  **dentro** da seed tem de suceder. Prova que o verbo funciona e que um `EACCES` em A3 é contenção
  e não um verbo partido.
- **N3 — o sandbox é mesmo o runsc.** Ler `/proc/version` ou `dmesg` de dentro: o gVisor identifica-se.
  Se o processo estiver a correr no host sem `runsc`, tudo o resto deste plano é inválido — e é
  precisamente o cenário do driver `fake`, que `DEF-701`/`DEF-702` marcam «nunca em produção».
- **N4 — mutação do próprio plano.** Desligar deliberadamente uma restrição (por exemplo montar a
  seed sem `ro`) e confirmar que o caso correspondente **avermelha**. Um plano cujos casos não
  falham quando a defesa é removida não está a medir a defesa.

## 6. Como registar a evidência

- Para cada caso: comando exacto, output verbatim, e a camada exercitada. Sem paráfrase.
- **`errno` importa.** `EACCES`, `EROFS`, `ENOENT` e `ENETUNREACH` significam coisas diferentes; um
  «falhou» sem `errno` não distingue contenção de bug.
- Registar os casos que **não** foi possível executar, e porquê. Um `NÃO MEDIDO` honesto é
  informação; um verde inferido não é.
- No fim, confirmar que o host ficou como estava: processos, portas, montagens, e o contentor
  removido.

## 7. Critério de aceitação do AOS-103

O ticket só se declara fechado se:

1. Todos os casos de **L3** (A3, A4, C5) e de **L2** (A5, A6, A7, B1–B3, C1–C4, C6) estiverem
   **contidos**, com o `errno` registado;
2. Os quatro controlos negativos passarem — **em especial o N3 e o N4**;
3. Os casos do bloco **D** provarem que o componente **sobrevive** ao abuso, e não apenas que o
   abuso falha;
4. Qualquer caso não executado estiver nomeado, com o motivo e o que faltaria para o executar.

Fechar o ticket sem o N3 seria declarar isolamento com base num sandbox que pode não estar lá — a
forma mais cara de errar deste plano inteiro.

## 8. Riscos e travões

- **Contentor privilegiado.** É o requisito do `runsc` e não se contorna. Corre-se em ambiente
  descartável, e o instantâneo do §3 existe para provar que se voltou ao estado anterior.
- **Código hostil por desenho.** Os blocos A–D correm payloads que tentam fugir, falar para fora e
  esgotar recursos. É teste de contenção autorizado, em infraestrutura própria — nunca contra
  terceiros, e nunca com egress real como objectivo.
- **Travão.** Se A3, A4 ou C5 falharem — ou seja, se houver fuga real — **parar o plano** e tratar
  como incidente: a conclusão já está tirada e continuar a bater no mesmo sítio só acrescenta risco.

---

## 9. Resultado da primeira execução (2026-09-07)

Executado num Docker descartável local (Docker Desktop sobre WSL2), com o componente construído do
`Dockerfile` deste repositório e um **guest de diagnóstico** (verbos que exercem as syscalls
directamente, para chegar à camada 3 em vez de morrer na verificação de caminho do guest de produção).
Detalhe completo em `analises/13` §2.5.

- **N3 — o sandbox é mesmo o runsc:** `/proc/version` = `Linux version 4.19.0-gvisor`. Não é o driver
  `fake` nem o host nu.
- **Blocos A, B, C, E: todos contidos, nenhuma fuga.** `read_abs /etc/passwd` → ENOENT; symlink para
  fora da seed → ENOENT; `/proc/1/cmdline` → pid 1 é `/guest`, procfs do gVisor; `write /seed/x` →
  EROFS; rede → ENETUNREACH; uid 65532; CapEff a zero; `/dev/kvm` e `/dev/mem` ausentes.
- **N4 — mutação:** removida a opção `ro` da montagem OCI, o `write /seed/x` passou a escrever. Prova
  que o caso mede a montagem e não um `if`.

**Desvios forçados por correr aninhado, registados:** `--cgroupns=host` (conflito de cgroup v2, sem
efeito na fronteira); o bloco de rede mede o netns interno do runsc; `NoNewPrivileges` não observável
pelo procfs sintético do gVisor (declarado na config; CapEff=0 + nosuid/noexec cobrem a superfície).

**Por correr:** o bloco D (esgotamento de recursos, excluído por decisão) e gVisor sobre
virtualização de hardware real (aqui correu sobre o kernel do WSL2).
