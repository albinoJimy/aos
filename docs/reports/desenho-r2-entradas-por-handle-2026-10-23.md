# Desenho R2 — entradas por handle e taint do artefacto (ADR-043 §2.6), 2026-10-23

> Fase R2 do roteiro do ADR-043 §7, a concretização da **opção A do ADR-034** (dual-LLM/CaMeL) e
> a forma forte do ADR-005, que o catálogo nunca materializou. **Decisões do dono de 2026-10-23:**
> (1) o handle é **híbrido** — tipo opaco ao modelo, armazém de artefactos por baixo; (2) o modelo
> que decide vê **só factos de nível-esquema** de um handle, nunca um resumo do conteúdo.
> Base: três levantamentos do código (o tail e o contexto, o armazém da R1b, o mecanismo de taint).

## 1. O que o dono ganha

Hoje, um plano em que um passo usa uma tool protegida **com base no que outro passo leu** falha
sempre — é o limite de produto registado em
`docs/reports/acompanhamento-arquitectura-alvo-fronteira-modelo.md:178`. A mitigação da v0.1.64
(AOS-540) é o nó consumidor pedir as tools de envio no primeiro turno, **antes de ver os dados**:
contorno, não solução, e que custa dois turnos a cada nó consumidor.

Com a R2, **ler deixa de contaminar as chamadas que não consomem o que foi lido**. Gerar a partir
do que se viu deixa de exigir uma pessoa em cada chamada (AOS-541), e a `model_invoke` da R3 nasce
legal em vez de nascer negada.

## 2. O estado de partida (medido a 2026-10-23)

**Os primitivos da opção A já estão escritos e testados, e não têm chamador de produção.**
`packages/kernel/agent-runtime/taint_plane.go`: `PlaneSegment` (`:21`), `Handle` (`:64`),
`Quarantine` (`:78`), `PlannerView` (`:125`), `SeparatePlanes` (`:139`), `ControlPlanner` (`:176`).
O próprio ficheiro o declara (`:172-175`), e só dois comentários o referenciam — `loop.go:772` e
`prompt.go:622`, ambos a apontar ao DEF-806. A R2 é **cablagem**, não invenção.

| Peça | Onde está hoje |
|---|---|
| A regra do rótulo | `context_authority.go:56-65` `SegmentAuthority`: trusted só para `objective`, `correction` e `notice`; `producedUnder` para `history` e `tool_call`; **o resto untrusted** (`default`, fail-closed). `ContextAuthority` (`:70-76`) é dobra monótona |
| Onde o conteúdo entra inline | `plan_input` leva o digest no rótulo e **o conteúdo no corpo** (`prompt.go:521-527`); o `tool_result` é o valor cru (`prompt.go:470`). É o defeito exacto que o DEF-806 nomeia |
| O precedente completo do handle | `media_view` (R1a): os bytes **nunca entram no tail** (`media.go:19-21`), e **o nome da tool é a fronteira** — só o resultado da `media_view` é reconhecido como referência (`media.go:26-32`, `mediaDoResultado` `:190`) |
| A mecânica «corpo omitido, digest no rótulo» | já existe para os argumentos: acima de 4 KiB o corpo fica vazio e a linha ganha `args_digest` (`layout.go:344-367`, `MaxToolCallArgBytes` `:247`) |
| O armazém (R1b) | chave `<dir>/<sha256(run_id)>/<sha256(conteúdo)>` (`artifact_store.go:191`, `:197`), cifra por titular pelo `contentSealer` (`dsar.go:52-60`), evento `artifact.created` (`:284`), tectos 1 MiB por artefacto e 16 artefactos / 16 MiB por run (`:77-79`) |
| O que não existe | `artifact_read`; nenhum esquema de referência é **parseado** como entrada; o `artefactoCriado` não tem campo de linhagem; os contadores do armazém não são publicados em `/metrics` |

## 3. A decisão central: o que é o handle

| | Decidido |
|---|---|
| O que o modelo vê | o `Handle` **opaco** do `PlannerView` mais metadados que **o nó afirma**: tipo de media, tamanho, validade, nome. Nunca conteúdo, nunca resumo. A barreira é **do tipo** — `PlannerView` não tem campo por onde bytes cheguem (`taint_plane.go:11-16`) |
| Quem resolve os bytes | `Quarantine.Resolve` deixa de ler um mapa em memória e passa a ler o armazém por `(subject, run, digest)`. Ganha durabilidade, cifra por titular, DSAR/retenção e proveniência selada |
| Determinismo | os ids continuam sequenciais pela ordem dos segmentos (`Put`, `taint_plane.go:92` — sem relógio, sem rand). É o que o replay por dobra pura exige (`replay/engine.go:601`) |
| **Quando se resolve** | **a jusante da decisão do RM.** O modelo põe o handle no argumento; o RM decide; só depois o nó troca o handle pelos bytes. Os bytes nunca passam pelo tail, pelo `prompt_hash`, pelos spans nem pelos selos |
| Handle forjado | duas condições, ambas necessárias: (a) só uma referência produzida por uma tool de código do nó é reconhecida — a regra do `media.go:26-32`; (b) o par `(run, digest)` tem de constar de um `artifact.created` do stream que o run pode ler — a verificação que `artefactosDoRun` (`artifact_api.go:40-54`) já faz no plano HTTP, levantada para o runtime |
| Entre runs | **só pela declaração selada do plano** (ADR-038), nunca por um digest que o modelo forneça. É a resposta directa ao *confused deputy* que o ADR-038 §3 nomeou ao rejeitar «o nó consumidor resolve a referência» |

**Correcção ao âmbito declarado.** `specs/EPIC-07_Seguranca_Isolamento.md:1627` põe «o `artifact_read`
entre runs» na R2. Com estas duas decisões **não nasce tool nenhuma de leitura**: a resolução é
interna ao nó e a autoridade da travessia é a declaração selada. Uma tool `artifact_read` daria ao
modelo um pedido de leitura por digest — exactamente o vector que a condição (b) existe para fechar.

## 4. A autoridade passa a ser por argumento

É o ponto em que a separação sozinha **não** chega, e tem de entrar com ela.

Se o contexto passar a trusted por construção (porque o `PlannerView` só tem trusted e handles), o
`TaintGate` deixa de negar (`taint_gate.go:164`) e um `web_post` cujo corpo seja um handle de um
documento com injecção passaria. A CaMeL fecha a integridade do **fluxo de controlo** — o atacante
não escolhe qual tool é chamada nem se é chamada — e não a do **fluxo de dados**.

| | Hoje | Com a R2 |
|---|---|---|
| `CallContext.Taint` | o rótulo do **turno**, lido antes de a resposta existir (`loop.go:654-657`, cunhado em `:1193`) | `join(trusted, taint de cada handle resolvido nos argumentos desta call)` |
| Uma call sem handles nos argumentos, depois de o run ter lido | untrusted ⇒ negada se privilegiada | **trusted** — é o limite que se levanta |
| Uma call que consome um handle untrusted | untrusted | untrusted: decide-se como hoje (nega, ou escala com o AOS-541) |

**Não é autorização em branco.** O que desaparece é a contaminação de calls que não consomem o que
foi lido. É a alternativa E do ADR-034 §3 («taint por argumento»), que o próprio ADR diz só ter
sentido com a separação por handle.

Invariantes a preservar, não a substituir:

- **um só rótulo para todos os leitores** (ADR-034 §2.3): o classificador de risco lê o mesmo valor
  (`risk_gate.go:373`, `:511`) e untrusted continua a elevar a sensibilidade um nível;
- **paridade das duas vias de despacho** (ADR-034 §2.8): `Activity.AuthorizationTaint`
  (`activity/contract.go:184`) e `DurableDispatcher` (`integration/runtime_ports.go:240`). Qualquer
  campo novo — o handle incluído — atravessa as duas ou a produção decide diferente do teste;
- **a autoridade é função pura da sequência reconstruída**, ou o replay e a retoma partem
  (`replay/engine.go:601`, `OpenRunTail`/`TailSequence` com uma só definição);
- **a classificação pelo destino da tool** (`tool_destination.go`) é config trusted do operador e é
  ortogonal à origem do rótulo: fica intacta.

## 5. As peças, por ticket

| Ticket | Conteúdo | Epic | Depende de |
|---|---|---|---|
| **AOS-550** | O handle resolve-se a jusante do RM, dentro de um run: `Quarantine` sobre o armazém, metadados de nível-esquema no `Handle`, um argumento de tool pode ser um handle, as duas condições do handle forjado, o tecto do corpo de um `tool_result`, métricas do armazém | EPIC-07 | R1b |
| **AOS-551** | A separação de planos ligada: layout **1.8.0**, `plan_input` e `tool_result` por handle, `SeparatePlanes` com chamador de produção, **a autoridade por argumento** (§4), paridade nas duas vias, replay por dobra. **Fecha o DEF-806** | EPIC-02 | AOS-550 |
| **AOS-552** | O taint e a linhagem do artefacto: o artefacto herda o taint das suas entradas, `artefactoCriado` ganha os digests das entradas, e nasce o guard-test que o ADR-043 §8 exige e que **não existe** («o artefacto é sempre untrusted») | EPIC-07 | AOS-550 |
| **AOS-553** | A travessia da aresta do plano: o handle atravessa pela declaração selada do ADR-038, o nó consumidor resolve do armazém do run produtor, com as defesas do *confused deputy* | EPIC-19 | AOS-551, AOS-552 |

**Restrição de ordem que não se negoceia:** o interruptor do layout 1.8.0 (AOS-551) **não fica
accionável** antes de a autoridade por argumento estar dentro. Ligar a separação sem ela troca
«negar a mais» por «permitir a mais», que é o pior dos dois.

## 6. O que é do dono

Menos do que na R1b, e a confirmar no AOS-550:

1. **Nenhuma capability nova**, se a resolução for interna (§3). Por confirmar contra o Cedar
   assinado: as cláusulas `context.taint != "untrusted"` (`aos_authz.cedar:33`) passam a avaliar um
   valor calculado de outra maneira, mas a **regra** não muda — logo, em princípio, sem re-assinatura.
2. O **critério 6 do AOS-363** (a cláusula de taint em `allow_fs_read`, adiada para a próxima
   cerimónia de chave — ADR-034 §2.6) volta a ser relevante: com a autoridade por argumento, armá-la
   deixa de proibir toda a leitura, que foi a objecção F2 que a adiou.
3. A prova em produção: um plano em que o nó 2 usa uma tool protegida sobre o que o nó 1 leu, **sem
   aprovação humana e sem o custo dos dois turnos**.

## 7. Resíduos e o que fica fora

- **R7 do ADR-034 permanece aberto:** conteúdo colado no `objective` continua a comandar no turno 1
  (`api.go:1178-1188`, guardado por `TestAOS069_InjeccaoNoObjectivoPassaAClausulaDeTaint_R7`). A
  opção A **não** fecha isto — a fronteira do rótulo é o principal autenticado, não o conteúdo.
- **O resumo de conteúdo fica fora** (decisão do dono, §3). Quem precisar de decidir sobre *o que os
  dados dizem* espera a R3: um `model_invoke` em quarentena, sem tools, cujo resultado é outro handle.
- **O tecto do corpo de um `tool_result` não está declarado em sítio nenhum.** `NÃO VERIFICADO`
  pelas três varreduras e pela minha própria busca: há tecto para os argumentos (4 KiB) e para os
  rótulos (256 B), e o `dec.Output` entra inteiro. Se o conteúdo passa a ir para um armazém com 1 MiB
  por artefacto, o tecto tem de nascer — entrada obrigatória do AOS-550.
- **AOS-540 e AOS-542 perdem a razão de ser** para runs em 1.8.0: a fronteira deixa de ser «o
  primeiro turno». Ficam em vigor para os outros layouts; a simplificação é trabalho de depois da
  prova, não deste roteiro.
- O **crypto-shred torna os blobs ilegíveis, não os apaga** (`artifact_store.go:25-26`).
- O **`ParentStepID` não é propagado na via durável** (`replay/engine.go:127`), pelo que a âncora de
  autoridade do replay é cega em produção. Resíduo 9 do ADR-034, sem ticket atribuído.
- A decisão «sem deduplicação entre runs» (ADR-043 §2.3) foi tomada com o artefacto a ser **só
  saída**. O AOS-553 é o primeiro caso em que o mesmo artefacto tem de ser legível por mais de um
  run: ou a travessia reescreve na pasta do consumidor, ou o consumidor abre a pasta do produtor com
  a declaração selada por autoridade. Decide-se no ticket.

## 8. O ADR

O ADR-038 §3, ao rejeitar «o nó consumidor resolve a referência», chamou-lhe **a direcção certa a
prazo**, nomeou o *confused deputy* e escreveu os gatilhos para a reabrir — entre eles, literalmente,
**a reabertura do DEF-806**. A R2 é essa reabertura, e o mesmo parágrafo diz que **merece ADR
próprio**.

Logo: **um ADR próprio** — o número atribui-se na emissão, porque há sessões em paralelo a reclamar numeração —, que materializa a forma forte do ADR-005, e emenda o ADR-034 (a opção A deixa de
ser destino), o ADR-043 §2.4 (o «resumo» passa a factos de nível-esquema) e o ADR-038 §3 (o gatilho
disparou). Nasce com o AOS-551, que é o ticket que torna a decisão verdadeira.

## 9. Ordem

AOS-550 (desligado, provado com testes e smoke) → AOS-552 (a linhagem e o guard-test em falta) →
AOS-551 (a separação, o ADR próprio e o interruptor de 1.8.0) → prova em produção → AOS-553 (a aresta).
