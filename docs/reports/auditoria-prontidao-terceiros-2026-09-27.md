# Auditoria — o que falta para o sistema entrar em produção e ser usado por outras pessoas

| Campo | Valor |
|---|---|
| Data | 2026-09-27 |
| Âmbito | Estado do repositório face à pergunta do dono, **medido em `39dead8`**. A base avançou duas vezes durante a vida deste relatório — `c595782` → `483e35e` (AOS-439/440) → `39dead8` (AOS-454) — e cada avanço foi **re-medido, não assumido**: as afirmações da §3 mantiveram-se nas três bases; as linhas de `api.go` deslocaram-se 425/629 → 426/634 na primeira e ficaram na segunda; as contagens de tickets mantiveram-se na primeira e mexeram na segunda (441/211/453 → 442/212/454). Ver a nota da §1.4 |
| Método | Leitura de fontes canónicas (Carta, System Spec, INDICE, registo de deferimentos), leitura do código do nó, e **execução** de dois gates |
| Gates executados | `build` (49 módulos), `lint`, `secrets`, `ref-lint` — todos exit 0. **4 de 29.** Os outros 25 não foram corridos nesta sessão |
| Não verificado | Nada foi verificado **contra o servidor de produção**. As afirmações sobre `37.60.241.150` são leitura de `deploy/server/README.md`, não medição independente |

---

## 0. A pergunta tem um pressuposto falso, e convém dizê-lo primeiro

A pergunta junta duas coisas que não são a mesma: *«entrar em produção»* e *«ser usado por
outras pessoas»*.

**A primeira já aconteceu.** Existe um servidor real (`37.60.241.150`), com `AOS_MODE=production`
ligado, TLS de Let's Encrypt no edge, Keycloak como IdP, Vault com KEK por-titular, gVisor a
executar tool calls, litellm a falar com um modelo real, WORM selado diariamente com checkpoints
ancorados (provados contra 234 partições reais), backups cifrados com ensaio de restauro repetível,
e uma cadeia de entrega `tag → 29 gates → SBOM/proveniência/DSSE → GHCR → servidor` com reversão
por um comando. Runs reais passaram pelo WORM com uma raiz humana `oidc-bound` — o `sub` do IdP, não
um nome escrito à mão.

Não é um protótipo. Está em produção.

**A segunda é um problema diferente e maior**, e a auditoria abaixo separa-as, porque misturá-las é
o que faz parecer que falta «mais rigor». Não falta. Falta outra coisa.

---

## 1. Aceitação formal da v1 — a lacuna é de governança, não de engenharia

### 1.1 O DoD da Carta tem seis critérios e **zero** marcados

`specs/00_AOS_Carta.md` §5 é, pelo §1 da própria Carta, **a fonte única** do que é *feito*. Os seis
critérios estão literalmente `- [ ]`. Pela medida que o projecto escolheu para si, **a v1 não está
aceite** — independentemente do que o código faça.

Cinco dos seis têm substância entregue e verificável hoje: o nó corre e compõe o `integration`; a
interface externa mínima existe (27 rotas em `packages/cmd/aos/planos.go`, SSE incluída em
`handleTrajectory`); a cadeia de governança medeia cada tool call; `specs/00_System_Spec.md` §13 está
6/7 verde com evidência não-vacuosa (`docs/reports/AOS-169-aceitacao-sistemica.md`); os gates são
fail-closed e os dois que corri estão verdes.

O que falta é um **acto de ratificação**, não um commit.

### 1.2 A Carta está desactualizada face ao código, e isso não é cosmético

§4.2 declara **D4 «EM PROVISIONAMENTO»**. Mas a Camada B que a emenda 1.3 mandou construir está, na
substância, provisionada: IdP OIDC real (Keycloak, dois clientes com audiências separadas),
custódia externa da chave (`issuer.key` vive na máquina do operador, o nó corre
*trust-anchor-only*), binding humano↔NHI com o `nonce` a transportar o digest da delegação
(`auth_method: oidc-bound:<iss>`, verificado em produção), e enrolamento de dispositivo
(`aos266_device_enrollment_test.go`).

A última emenda da Carta é a **1.4, de 2026-08-31**. Desde então entraram, no mínimo, os tickets
AOS-383..454. **A fonte única de «o que está decidido» está quase um mês atrás do que existe.** Uma
Carta desactualizada tem exactamente o mesmo modo de falha que a Carta existe para fechar: cada
leitor tira conclusões diferentes sobre o que é «done».

### 1.3 Uma contradição por resolver dentro da mesma linha

Nas emendas 1.2 e 1.3, o **corpo** diz «Sign-off de Segurança/Arquitectura continua **PENDENTE** —
pré-condição da v1 (§5)». A **coluna de aprovação da mesma linha** diz «Segurança/Arquitectura
**assinado** (2026-07-29 — arbitragem §6.5, N-011)».

Lido o N-011 em `docs/governance/REGISTO-Decisoes-Reabertas-e-Arbitragens.md`: os dois papéis
pronunciaram-se sobre **quatro pendências específicas** (REG-005/006/008/010) ao abrigo do §6.5 —
classificar «dívida-escondida vs re-litígio». Isso **não é** o sign-off de aceitação da v1 que o §5
exige.

**Não afirmo qual das duas está certa** — é uma decisão do dono e dos dois papéis, não minha. Afirmo
que estão a dizer coisas incompatíveis no mesmo sítio, e que essa é precisamente a classe de
ambiguidade que o §0 da Carta diz existir para eliminar.

### 1.4 Metade do backlog vive em epics não-ratificadas

**442 cabeçalhos de ticket** (`AOS-NNN`, ids únicos) em `specs/EPIC-*.md`, dos quais **212 em
EPIC-01..17** e **230 em EPIC-18..25** — medido em `39dead8`. O `INDICE.md` declara **189
ratificados** (EPIC-01..17) e tudo a partir da EPIC-18 como *«proposta por ratificar»*.

São, portanto, **pelo menos 230 tickets — mais de metade do backlog — em epics que a governança do
próprio projecto não aceitou**; e mesmo dentro das epics ratificadas há 212 cabeçalhos contra 189
declarados, ou seja 23 tickets cuja ratificação o índice não cobre (AOS-226..229 são nomeados como
«fora das epics listadas», os restantes não).

Três contagens diferentes do mesmo backlog, e nenhuma bate: **442** (cabeçalhos na árvore), **454**
(o que o gate `ref-lint` reporta como «tickets no backlog»), **189** (o que o `INDICE.md` declara
ratificado). O `INDICE.md` pára em AOS-382 enquanto a árvore tem AOS-454. Não tento arbitrar qual
está certa — o ponto é que **o projecto não sabe dizer, por comando, quantos tickets tem e quantos
estão aceites**, e isso é a mesma classe de defeito que o registo de deferimentos foi criado para
fechar noutro eixo.

> **E este parágrafo mede-se a si próprio.** Foi escrito com 441/211/453 sobre `c595782`,
> reverificado sem mudança em `483e35e`, e reescrito para 442/212/454 em `39dead8` — **três bases
> em pouco mais de uma hora**, com o número a mexer na terceira (AOS-454 entrou na EPIC-02). Isto
> não enfraquece o achado: é a sua demonstração. Um backlog cujo tamanho muda debaixo de quem o
> conta, sem que nenhuma das três fontes concorde, não é contável por inspecção — precisa de UMA
> fonte e de um gate que a imponha, como o `deferrals` faz no seu eixo. Enquanto não existir,
> qualquer número escrito aqui está certo à data e errado depois.

### 1.5 O hipercare nunca encerrou

`docs/hipercare/transicao.md` é um **molde com `{{placeholders}}` não preenchidos**. O AOS-108 é o
fecho da EPIC-10 e o gate `CanExit()` exige S1–S4 (SLOs sustentados, runbooks com MTTR, alertas
calibrados, DR revalidado). Nada disso está registado. **Não existe declaração de operação em
regime.**

---

## 2. Infra — o bloqueio técnico real, e não é código

### 2.1 O nó corre sobre um host doente, e isso está medido no próprio README

`deploy/server/README.md` §«O servidor real» é honesto ao ponto de ser desconfortável:

- **Nó único, uma máquina, sem réplica.** O DR da EPIC-10 (Event Store replicado, failover) não está
  lá. A emenda 1.4 nomeou o distribuído **v1.1**, com AOS-100 como único bloqueador.
- **O host partilha 8 vCPU com um control-plane Kubernetes saturado e parcialmente morto:**
  5 dos 6 nós `NotReady`, `kube-apiserver` a ~95% de um core, *load average* observada 17–36 numa
  máquina de 8, o nó com `mem_limit` de 1 GB. O README declara a consequência: **«sob contenção,
  espera latência de mediação acima dos alvos de `tecnica/10`»**.
- **Sem firewall.** O host expõe publicamente e sem filtro `6443` (kube-apiserver), `10250`
  (kubelet), `2379/2380` (etcd) e `8472/udp`. Os scripts de deploy não lhe põem regras — e com
  razão, um `ufw` default-deny cortaria o cluster. Mas o facto fica.

**Este não é um sítio onde se ponha tráfego de outras pessoas.** Não por falta de rigor no AOS —
por causa do vizinho.

### 2.2 Duas garantias penduradas num PC de secretária

A **selagem diária do WORM** (`AOS-SelarWORM`, 03:30) e a **recolha off-host dos backups**
(`AOS-RecolherBackups`, 04:30) correm na máquina Windows do operador. O próprio README declara o
residual: *«a máquina desligada não alerta enquanto está desligada — nenhum processo local pode»*.

A integridade ancorada do log de auditoria e a cópia off-host dependem de um desktop estar ligado.
Para uso próprio é uma escolha defensável. Para servir terceiros, as duas propriedades que mais
interessam num incidente são as que têm a dependência mais frágil.

### 2.3 ~~A entrega vai declaradamente não-assinada~~ — CORRIGIDO: vai assinada

> ⚠️ **Este ponto estava ERRADO, e a correcção interessa mais do que o ponto.** A versão original
> dizia que `deploy/node/release-pubkeys.json` tem `keys: []` e que a entrega segue <!-- roster:historico -->
> **não-assinada**. **Medido no ficheiro a 2026-09-27: tem 1 chave**, do Arquitecto de Plataforma,
> e o próprio JSON declara que *«o roster deixou de estar vazio em 2026-08-14, no primeiro release
> distribuído fora do repositório (v0.1.0)»*. Mais: `release.yml` faz **`exit 1`** se
> `secrets.AOS_RELEASE_KEY` estiver vazia, pelo que nenhum dos releases publicados (v0.1.29,
> v0.1.35) poderia ter saído sem ela — inferência do gate, não observação directa do secret, que
> daqui não se vê.
>
> **Como errei:** citei `deploy/server/README.md` §«O que esta configuração ainda não fecha»
> ponto 2, em vez de abrir o JSON, que estava a um `cat` de distância. Num relatório que acusa o
> corpus de estar desactualizado, repeti uma linha desactualizada em vez de medir a fonte
> primária — e o erro fez a postura de segurança parecer **mais fraca** do que é.
>
> **E não era uma cópia, eram quatro.** À data da correcção, afirmavam `keys: []`: <!-- roster:historico -->
> `deploy/server/README.md:2376`, `deploy/node/CUSTODIA-CHAVE-RELEASE.md:83` e `:174`, e
> **`docs/adr/ADR-017-supply-chain-node.md:174`** — um ADR canónico. Todas corrigidas no mesmo
> commit que esta nota, porque corrigir só a minha teria deixado as outras três a mentir, que é
> exactamente o modo de falha que este relatório persegue e que já me apanhou três vezes nesta
> sessão.

**O que continua verdadeiro**, e não depende do roster: a atestação DSSE é um artefacto separado
(vai para a GitHub Release), um `docker pull` não a traz, e **o servidor verifica o `digest`, não a
assinatura** — residual declarado em ADR-017 ponto 1. Quem assina é a CI; quem corre o nó confia no
digest que o release fixou.

---

## 3. Multi-utilizador — aqui está a lacuna própria da pergunta

Esta secção é a que responde literalmente a *«usado por outras pessoas»*, e é onde o repositório
está mais fraco. Três defeitos estruturais, todos pequenos em código:

### 3.1 O rate-limit é global, não por-chamador

`packages/cmd/aos/api.go:426` — há **dois** token-buckets: um do plano de dados (`POST /runs`) e um
dedicado ao plano de controlo. Ambos **por-nó**, não por-principal. Verificado em `handleSubmit`
(api.go:634): `if !h.bucket.allow()`.

**Consequência:** um chamador esgota o ingresso de todos os outros. Com um utilizador (o dono) é
proteção anti-exaustão correcta; com N utilizadores é um vector de negação de serviço entre pares,
sem malícia necessária.

### 3.2 O orçamento é por-RUN, com um tecto único de ambiente

`packages/cmd/aos/budget_env.go` — `AOS_BUDGET_MAX_TOKENS` é *«o tecto que **cada** run recebe»*, e
`AOS_BUDGET_MAX_COST_MICRO_USD` idem. **Não existe orçamento por-principal nem por-tenant.**

**Consequência:** N runs × tecto = despesa ilimitada. Não há contenção de custo para um utilizador
que não seja o dono. Num sistema que chama um modelo pago, isto é o risco financeiro mais directo
de abrir a terceiros — e é menos trabalho do que quase tudo o que já foi feito.

### 3.3 Um único board, e o onboarding passa pelas chaves offline do operador

`AOS_BOARD_REGIONS=board:prod=eu-west` — **um board só**. O read-path soberano por-leitor está
construído e a recusa cross-region está provada, mas o README declara que *«com um board só não há
como voltar a exercê-la com leitores humanos»*. Na prática, single-tenant.

E admitir uma pessoa nova exige: criar o utilizador no Keycloak com o atributo `board`, **e** cunhar
uma NHI com a `issuer.key` — que vive, por desenho correcto, em suporte offline na máquina do
operador (`get-id-token.ps1 -Cunhar`). **Não há self-service.** O desenho da custódia está certo; o
que não existe é um caminho de admissão que não passe por uma pessoa com uma pen na mão.

### 3.4 O four-eyes prova duas assinaturas, não duas pessoas

O próprio README declara-o (§Custódia, ponto 3): as duas seeds de aprovador vivem na **mesma**
`secrets-local/`. *«São duas chaves e **um** custodiante.»* Adjacente ao DEF-107, que está **ABERTO**
no registo de deferimentos.

Com um operador, é uma formalidade sem dano. Com terceiros no sistema, o controlo dual é o mecanismo
que os protege do operador — e nesse papel não está armado.

### 3.5 O mTLS do plano de controlo não está composto em produção

`AOS_CONTROL_MTLS_CA_PATH` tem default vazio no `docker-compose.prod.yml:298` e nenhum script de
provisionamento o define. O comentário de `packages/cmd/aos/planos.go` di-lo em voz alta:
*«`admitControlMTLS` devolve `true` quando o mTLS não está composto, que é o caso em dev, em CI e **na
produção actual**»*.

Para ser justo: o que protege o plano de controlo de facto é a **assinatura ed25519 do corpo** com
*nonce* de uso único, e essa está lá e está provada. A barreira de transporte é defesa-em-profundidade
ausente, não a única linha. Mas o comentário do ficheiro é a única coisa que o diz — e o teste de
planos fica verde de qualquer maneira, que é o modo de falha que o próprio ficheiro descreve.

---

## 4. Superfície de produto — o que uma pessoa toca

### 4.1 Não existe interface gráfica. Nenhuma.

`find` por `*.tsx`, `*.jsx`, `package.json` em toda a árvore: **zero resultados** (fora
`node_modules`, que também não existe). A **EPIC-13 Frontend são 15 tickets** (AOS-129..143) e não
tem uma linha implementada.

A D1(b) — «superfície web SPA bespoke» — está **CONDICIONAL**, e o gatilho nomeado é
*«utilizadores reais + TCO de ingress + dono de 2.ª supply-chain»*. **O gatilho é exactamente a
pergunta que está a ser feita.** Se a resposta for «sim, outras pessoas», a D1(b) abre por regra,
não por preferência.

O que existe hoje para um humano usar: `curl` com um Bearer do Keycloak, e um script PowerShell na
máquina do operador. Isso serve o dono. Não serve mais ninguém.

### 4.2 Não há documentação de utilizador — só de operador

`deploy/server/README.md` tem mais de 3000 linhas e é excelente. É um **runbook de operador**. Não
existe um *getting started* para quem quer submeter um goal e ver o que aconteceu, sem saber o que é
uma KEK ou um checkpoint de WORM.

### 4.3 Não há LICENSE

Não existe ficheiro de licença na raiz. Para distribuir ou deixar terceiros correr/usar isto, é um
bloqueio legal — e é o item mais barato desta auditoria toda.

### 4.4 Uma superfície viva e inerte

`POST /plans` aceita `201`, mas `aos-orq` está atrás de `profiles: ["orq"]` com `restart: "no"`
(`docker-compose.prod.yml:541-545`): **nada drena a fila em produção**. O CHANGELOG declara-o —
a rota reporta `pending` até isso mudar. A EPIC-19 (planeador, meta-orquestração) está construída e
desligada.

---

## 5. O que está sólido, e é muito

Não seria uma auditoria honesta se só listasse falhas.

- `build` verde nos 49 módulos e `lint` verde — **executados nesta sessão**, não citados.
- A cadeia de entrega é real e reprodutível, com os mesmos 29 gates de um PR. Não existe uma
  definição relaxada para releases.
- A separação de *trust-domains* está certa onde importa: o nó nunca assina, só verifica. A
  `issuer.key`, a CA interna, o selador do WORM e as seeds humanas vivem fora do servidor. *«O
  servidor não guarda nenhuma credencial que conceda autoridade sobre o sistema.»*
- O binding delegação↔autenticação (`oidc-bound`) resolve um problema que a correcção óbvia não
  resolvia, e tem o controlo negativo que o torna não-vacuoso.
- A documentação nomeia as suas próprias lacunas em voz alta. **Esta auditoria foi largamente
  possível por isso** — e é uma propriedade rara que vale mais do que a maioria dos gates.

---

## 6. A crítica que interessa

Há um padrão nesta árvore que vale nomear, porque é ele que explica a forma da lista acima.

O repositório tem **393 tickets, 25 epics, 13 auditorias adversariais, um registo de deferimentos com
**38 entradas ABERTAS** (medido: `scripts/ci/deferrals.sh`), um gate que o impõe, uma ferramenta para detectar colisões
entre sessões concorrentes** — e, ao mesmo tempo, **zero linhas de frontend, nenhum ficheiro de
licença, um rate-limit global e nenhum orçamento por-utilizador.**

O rigor está concentrado nos invariantes arquitecturalmente interessantes, e ausente das coisas
enfadonhas que decidem a adopção. Isso não é um acidente de execução: é uma consequência previsível
de medir o progresso contra uma visão de invariantes em vez de contra um utilizador. Os invariantes
foram bem escolhidos e estão bem construídos. Mas «untrusted não comanda» não é o que impede a
segunda pessoa de usar isto — o que a impede é não ter onde clicar, não ter como entrar sem a pen do
dono, e poder gastar a factura do modelo toda sem tecto.

---

## 7. Resposta directa, por ordem de custo

**Para «entrar em produção»: já entrou.** O que falta é declará-lo — e isso é governança:

1. Reconciliar a Carta com o código (emenda que actualize D4 e o §4.2 para o que existe).
2. Resolver a contradição do sign-off (§1.3): ou N-011 cobre o §5, ou não cobre e falta obtê-lo.
3. Marcar os seis `[x]` do §5 com evidência, ou dizer por escrito qual não está.
4. Ratificar (ou fechar) as EPIC-18..25 e actualizar o `INDICE.md` até ao ticket mais alto da árvore
   (AOS-454 em `39dead8`, e a subir — ver a nota da §1.4).
5. Preencher `docs/hipercare/transicao.md` e fechar o AOS-108, ou declarar que o hipercare não corre.

**Para «ser usado por outras pessoas», por ordem de retorno sobre esforço:**

| # | O que | Natureza | Esforço |
|---|---|---|---|
| 1 | `LICENSE` na raiz | decisão + ficheiro | minutos |
| 2 | Orçamento **por-principal** (não só por-run) | código, 1–2 tickets | pequeno |
| 3 | Rate-limit **por-chamador** (balde por `sub`) | código, 1 ticket | pequeno |
| 4 | Host próprio, fora do k8s moribundo | dinheiro + horas | pequeno–médio |
| 5 | *Getting started* de utilizador (submeter, observar, aprovar) | documentação | pequeno |
| 6 | Onboarding sem as chaves offline do dono (auto-registo + cunhagem mandatada) | código + desenho de custódia | médio |
| 7 | Superfície web (abrir a D1(b) — o gatilho ocorreu) | EPIC-13, 15 tickets | grande |
| 8 | Segundo custodiante para o four-eyes (DEF-107) | organizacional | depende de haver 2.ª pessoa |
| 9 | Ligar `aos-orq` em produção, ou retirar a rota `/plans` | operação ou código | pequeno |
| 10 | Réplica / sair do SPOF (AOS-100, v1.1) | EPIC-10 | grande |

Os itens 1 a 5 são, somados, menos trabalho do que qualquer uma das últimas três epics de
remediação. São também o que decide se existe uma segunda pessoa a usar isto.

---

## 8. Limites desta auditoria

- **2 de 29 gates corridos.** `test`, `-race`, `replay`, `security`, `dr-e2e`, `scale`, `sast`,
  `sca`, `policy-test` e os restantes não foram executados. Não afirmo que estão verdes; afirmo que
  não os medi.
- **Nada foi verificado contra o servidor.** Tudo o que esta auditoria diz sobre produção é leitura
  de `deploy/server/README.md` — um documento que se mostrou rigoroso e auto-crítico, mas que é
  testemunho, não medição.
- **Não li os 442 tickets.** Contei-os e li os epics-índice, a Carta, o System Spec §13/§14, o
  registo de deferimentos e o código do nó nas superfícies relevantes à pergunta.
- O ponto §3.5 (mTLS do plano de controlo) baseia-se no default vazio da variável e na ausência de
  qualquer script que a defina. Se estiver definida à mão no `.env` do servidor, o ponto cai — e não
  tenho como saber daqui.
