# Registo único de deferimentos — onde cada dívida declarada tem um eixo verificável

| Campo | Valor |
|---|---|
| Documento | **Registo único** dos deferimentos declarados no código e no corpus: um por linha, com **eixo**, **dono**, **gatilho de reavaliação** e **estado** |
| Autoridade | **Subordinado**. Este ficheiro **não decide nada** e **não cria tickets** — regista o que já está deferido e torna o eixo verificável por comando |
| Origem | AOS-196 (EPIC-18), achados **DEF-01**, **DEF-03** e **DEF-06** da auditoria multiagente v4 |
| Gate que o impõe | `scripts/ci/deferrals.sh` (bloqueante; `run.sh` → `ALL_GATES`, job `deferrals` em `ci.yml`, `needs:` do agregador `gates`) |
| Última actualização | 2026-08-13 |

---

## 1. Porque este ficheiro existe

A auditoria v4 procurou **dívida escondida** e não a encontrou. O que encontrou foi pior de
diagnosticar e mais barato de corrigir: **eixo errado**.

Os deferimentos deste programa estão declarados em voz alta, no sítio certo, muitas vezes no
próprio banner de arranque do nó. O que está errado é o **destino** que lhes é atribuído:

| Deferimento | Eixo que estava escrito | Facto |
|---|---|---|
| Cifra por-titular do substrato (Event Store) | `EPIC-06/09/10` em `packages/cmd/aos/bootstrap.go`; **`EPIC-13`** em `tecnica/02` | O **EPIC-13 é o epic de *Frontend***. Nenhum dos quatro epics é o destino certo: o ticket que cobre a propriedade é **AOS-093** (EPIC-09), cujo CA #1 é literalmente «toda a PII persistida é cifrada com uma chave por titular» — ver a arbitragem A-DEF-301 |
| Anti-replay da ratificação (ADR-012 §7) | `EPIC-13` | Idem — Frontend. O mecanismo existe (AOS-159); o **wiring** não tem ticket |
| Assinatura/atestação de imagem (ADR-017) | `EPIC-10` | O EPIC-10 não tem ticket para assinatura de imagem (AOS-098…AOS-108 são IaC, réplicas, backup, DR, dashboards, alertas, runbooks, escala, hipercare) |

Um eixo que aponta para um epic **sem ticket** é operacionalmente indistinguível de não ter
eixo nenhum: ninguém o executa, nada o reporta, e a dívida atravessa auditorias inteira
porque *parece* endereçada. A recomendação 7 da auditoria pede exactamente a propriedade que
este ficheiro + o gate impõem:

> «Criar um registo único de deferimentos (id, eixo, dono, gatilho, estado) e corrigir os
> eixos errados. Todo o marcador `DEFERIDO`/`demo-grade` em `packages/**/*.go` (não-teste)
> deve ter entrada no registo com eixo que **contenha um ticket** para ele. Verificável por
> script.»

**Este ficheiro não cria tickets.** Onde não existe ticket, o eixo é o literal
**`POR ATRIBUIR`** e o ticket necessário é **descrito** na nota do §6 — para que o dono do
backlog o crie centralmente. Inventar um número de ticket inexistente partiria o gate
`ref-lint` (bloqueante desde AOS-190) e seria trocar um defeito de rastreabilidade por outro.

---

## 2. Como se usa

### 2.1 Quando se escreve uma linha

Uma linha nova é obrigatória sempre que se introduz, em `packages/**/*.go` de produção, um dos
marcadores do §2.2 — **no mesmo commit**. O gate recusa o merge se faltar (verificação 1).

Uma linha é **removida** — nunca esbatida — quando o marcador desaparece do código. Uma linha
cujo marcador já não existe é **OBSOLETA** e faz o gate ficar vermelho (verificação 5). O
registo **só encolhe**.

### 2.2 Vocabulário controlado (o gate valida-o e falha se for violado)

**`Marcador`** — como o deferimento está declarado. Os seis primeiros são detectados no
código; `DOCUMENTAL` é para deferimentos que vivem num documento (ADR/tecnica/spec) ou numa
forma textual que não é um marcador de código.

| Valor | Forma detectada no código |
|---|---|
| `DEFERIDO` | `DEFERIDO`/`DEFERIDA`/`deferido`/`deferida` (qualquer caixa) |
| `DIFERIDO` | `DIFERIDO`/`DIFERIDA`/`DIFERIDOS` — **só em maiúsculas** |
| `DEMO-GRADE` | `demo-grade` / `DEMO-GRADE` / `DEMO GRADE` (qualquer caixa) |
| `NUNCA-EM-PRODUCAO` | «NUNCA usar em produção» (qualquer caixa) |
| `CONDICIONAL` | `CONDICIONAL` — **só em maiúsculas** |
| `STUB` | `STUB`/`STUBS` — **só em maiúsculas** |
| `DOCUMENTAL` | *(não detectado no código; a linha é validada mas não satisfaz cobertura)* |

> **Porquê `STUB`, `CONDICIONAL` e `DIFERIDO` só em maiúsculas.** Medido, não escolhido por
> gosto: em minúsculas, `stub` ocorre ~30× neste código em **negações** («nunca um stub»,
> «recusa stubs», «no lugar do stub») e em identificadores Go (`BudgetStub`, `IdentityStub`),
> e `condicional` ocorre em prosa corrente. Registá-los produzia dezenas de linhas sem
> significado e um gate que seria desligado — e um gate desligado é pior do que gate nenhum.
> A forma MAIÚSCULA é a que este corpus usa deliberadamente para **declarar** uma fronteira
> («STUBS NEUTROS», «é o STUB histórico», «a TOPOLOGIA é CONDICIONAL ao provisioning»).

> **`DEFERIDO` (com E) e `DIFERIDO` (com I) são o MESMO conceito em duas grafias.** A primeira
> versão deste registo só reconhecia a grafia com **E** — e essa omissão era o defeito DEF-01
> a repetir-se dentro do próprio instrumento criado para o fechar: 25 declarações em 16
> ficheiros de produção, escritas com **I**, não tinham linha nenhuma aqui, e uma delas
> (`reference-monitor/scope_gate.go:69`, «DIFERIDA para EPIC-08») era a **forma textual exacta
> do achado** a passar o gate. Medição de 2026-07-27, que é o que justifica a assimetria das
> duas linhas da tabela:
>
> | Forma | Ocorrências em `packages/**/*.go` de produção | Natureza |
> |---|---|---|
> | `deferid*` minúsculas | 5 | 5/5 são declarações de fronteira ⇒ **é marcador** |
> | `DIFERID*` maiúsculas | 25 (16 ficheiros) | 25/25 são declarações deliberadas ⇒ **é marcador** |
> | `diferid*` minúsculas | 23 | palavra portuguesa corrente: «a libertação do `sealMu` é diferida», «a materialização é diferida», «a compactação é diferida para o checkpoint», e uma **negação** («deixa de ser diferida») ⇒ **NÃO é marcador** |
>
> A forma minúscula com **I** fica fora da cobertura (verificação 1) por ser ruído, mas **não**
> fica fora do gate: a verificação 6 reconhece-a, porque aí a exigência adicional de um
> `EPIC-NN` na mesma linha elimina o ruído — das 23 ocorrências, só 1 traz um `EPIC-NN` sem
> ticket, e essa está na baseline com dono.

**`Estado`** — em que ponto está o deferimento **hoje**:

| Valor | Significado |
|---|---|
| `ABERTO` | O deferimento está em vigor e o eixo não foi entregue. É dívida a sério. |
| `MITIGADO` | O mecanismo real **existe e é fail-closed**, mas o *wiring*/provisionamento por omissão não está ligado. O risco está contido; a capacidade não está entregue. |
| `FECHADO-RESIDUAL` | A fronteira **já fechou** noutro caminho e o marcador permanece como **contraste** (ex.: o `HMACAuthenticator` demo que o ed25519 de AOS-160 substituiu; um helper determinista de teste). Não é dívida — é documentação da fronteira. |

**`Eixo`** — quem fecha isto. **Regra dura:** ou cita ≥1 `AOS-NNN` que **existe** no backlog
(`specs/EPIC-*.md`), ou traz o literal **`POR ATRIBUIR`** (podendo trazer ambos, quando parte
do deferimento tem ticket e parte não). Uma célula que só cite `EPIC-NN` falha as duas
condições e **faz o gate ficar vermelho** — é literalmente o defeito DEF-01 codificado como
regra.

**`POR ATRIBUIR` não é escotilha:** toda a linha que o use tem de ser **nomeada no cabeçalho**
de uma nota do §6, na forma `### N-DEF-NNN — cobre DEF-201, DEF-203, …`, onde o ticket em falta
é **descrito** (objectivo, critérios falsificáveis, epic sugerido). Sem a nota, o gate falha. E
o número de linhas `POR ATRIBUIR` é **impresso em cada execução** — é o contador da dívida sem
dono de execução.

> **Porquê a lista no cabeçalho e não na prosa.** Um deferimento sem eixo replicado por oito
> âncoras é **um** ticket em falta, não oito; escrever a mesma nota oito vezes convidava-as a
> divergir. A lista de cobertura vive no cabeçalho para a leitura por máquina ser inequívoca
> (o gate lê o cabeçalho, não interpreta prosa) e a leitura humana ser óbvia.

### 2.3 Invariantes de formato

- exactamente **8 colunas**, pela ordem da tabela do §3;
- **sem o carácter `|` dentro das células** (a fundamentação longa vai para as notas do §6);
- ID `DEF-NNN`, **nunca reutilizado**; as centenas agrupam por família (§3);
- `Dono`, `Gatilho de reavaliação` e `Deferimento` não podem estar vazios nem ser `—`;
- **várias linhas podem partilhar a mesma âncora e o mesmo marcador** — é assim que
  `bootstrap.go` + `DEFERIDO` carrega três eixos distintos (soberania, coincidência de região,
  cifra do substrato). O gate exige *pelo menos uma*.

---

## 3. O registo

Famílias: **0xx** adapters de deployment na fronteira zero-dep · **1xx** identidade/autoridade
· **2xx** soberania de leitura · **3xx** cifra do substrato · **4xx** anti-replay da
ratificação · **5xx** supply-chain · **6xx** enforcement do Reference Monitor · **7xx**
isolamento e credenciais · **8xx** wiring diferido · **9xx** helpers deterministas de teste.

| ID | Marcador | Âncora | Deferimento | Eixo | Dono | Gatilho de reavaliação | Estado |
|---|---|---|---|---|---|---|---|
| DEF-001 | DIFERIDO | packages/cmd/aos/otlpexporter.go | Marcador de contraste: este ficheiro É o adapter OTLP/HTTP que a folha `otel-genai` declarava DIFERIDO; fecha-o com `net/http` + `MarshalOTLP`, sem SDK externo | AOS-173 | Arquitecto de Plataforma | Remoção do texto DIFERIDO da folha `otel-genai` (o gap está fechado no nó) | FECHADO-RESIDUAL |
| DEF-002 | DIFERIDO | packages/substrate/otel-genai/doc.go | O exportador OTLP sobre o SDK `go.opentelemetry.io` fica DIFERIDO por decisão de arquitectura (regra zero-dep/offline do monorepo), não por falta de trabalho | AOS-173, AOS-076 | Arquitecto de Plataforma | Deployment que exija o SDK OTel oficial (ex.: OTLP-gRPC), o que quebraria a regra zero-dep | FECHADO-RESIDUAL |
| DEF-003 | DIFERIDO | packages/substrate/otel-genai/exporter.go | Idem DEF-002 na porta `Exporter` | AOS-173 | Arquitecto de Plataforma | Idem DEF-002 | FECHADO-RESIDUAL |
| DEF-004 | DIFERIDO | packages/substrate/otel-genai/otlp.go | Idem DEF-002 na serialização OTLP/JSON | AOS-173 | Arquitecto de Plataforma | Idem DEF-002 | FECHADO-RESIDUAL |
| DEF-005 | DIFERIDO | packages/substrate/otel-genai/semconv.go | Idem DEF-002 no mapa de constantes semconv | AOS-173 | Arquitecto de Plataforma | Idem DEF-002 | FECHADO-RESIDUAL |
| DEF-006 | DIFERIDO | packages/substrate/otel-genai/spantracer.go | Idem DEF-002 no tracer concreto | AOS-173 | Arquitecto de Plataforma | Idem DEF-002 | FECHADO-RESIDUAL |
| DEF-007 | DIFERIDO | packages/substrate/otel-genai/wide_event.go | Idem DEF-002 no wide-event e na agregação de cardinalidade | AOS-173 | Arquitecto de Plataforma | Idem DEF-002 | FECHADO-RESIDUAL |
| DEF-008 | DIFERIDO | packages/kernel/agent-runtime/replay/payload_store.go | Content-capture mode 3: a porta `PayloadStore` com IAM próprio existe e é fail-closed; o adapter real (S3 + política IAM + KMS por titular) é adapter de deployment | AOS-079 | Arquitecto de Plataforma | Deployment com storage externo e IAM próprio disponível | MITIGADO |
| DEF-009 | DIFERIDO | packages/substrate/otel-genai/evaluation.go | A folha declara em 4 sítios que o runner/gate CONCRETO «pertence a EPIC-11 e fica DIFERIDO» — mas o harness concreto JÁ existe em `packages/platform/eval` (AOS-114); o que falta é o gate de eval composto num caminho de promoção | AOS-114, AOS-115 | Arquitecto de Plataforma | Existir caminho de promoção composto no nó (o mesmo gatilho de DEF-401) | MITIGADO |
| DEF-010 | DIFERIDO | packages/platform/eval/doc.go | Marcador de contraste: este pacote É o harness que a folha `otel-genai` declarava DIFERIDO para EPIC-11 | AOS-114 | Arquitecto de Plataforma | Idem DEF-009 | FECHADO-RESIDUAL |
| DEF-011 | DIFERIDO | packages/platform/eval/runner.go | Idem DEF-010 no `Runner` concreto que satisfaz a porta `otelgenai.EvalRunner` | AOS-114 | Arquitecto de Plataforma | Idem DEF-009 | FECHADO-RESIDUAL |
| DEF-012 | DEFERIDO | packages/cmd/aos/api.go | **mTLS do plano de controlo e autenticação forte da perna OTLP — MECANISMO ENTREGUE OPT-IN.** O mTLS do plano de controlo (CA de cliente por AOS_CONTROL_MTLS_CA_PATH; escopado ao PLANO DE CONTROLO (as 11 rotas classificadas `planoControlo` na tabela de `packages/cmd/aos/planos.go`: steer, pause, approve, challenge, resume, exhaustion, promote, as tres de autonomia e nhi/revoke) mas NAO ao plano de GOVERNACAO (`/dsar/erase`, `/dsar/hold`, `/dsar/release`, `/dsar/expire`), que se autentica pela credencial forte do gate soberano e NAO exige certificado de cliente â decisao em aberto, escrita no comentario de `barreirasDe` em planos.go; ADITIVO à assinatura ed25519, nunca bypass) e a autenticação forte da perna OTLP (mTLS de cliente ou bearer, por ficheiro; fail-open de AOS-173 preservado) estão ENTREGUES e fail-closed. Fica deferido apenas o que é infra-org, não código do nó: a PROVISÃO de PKI/emissão de certificados de cliente aos operadores e o bearer/mTLS do lado do colector | POR ATRIBUIR | Responsável de Segurança | Provisão de infra (PKI de cliente / configuração de autenticação do colector) numa organização real — o mecanismo do nó já não é o gargalo | MITIGADO |
| DEF-101 | DEMO-GRADE | packages/integration/issuer_authority.go | `AllowlistDirectory` é a impl de referência de `HumanDirectory`: regista humanos, não prova autenticação | AOS-174, AOS-156 | Responsável de Segurança | O nó compor `OIDCDirectory` por configuração em vez da allowlist | MITIGADO |
| DEF-102 | NUNCA-EM-PRODUCAO | packages/integration/issuer_authority.go | `AuthenticateAssertion` aceita a asserção sem prova criptográfica (não há IdP na allowlist) | AOS-174 | Responsável de Segurança | Idem DEF-101 | MITIGADO |
| DEF-103 | DEFERIDO | packages/integration/issuer_authority.go | Endurecimento posterior da autoridade: HSM/sign-in-place e chave fora do processo | AOS-175 | Responsável de Segurança | Custódia de chave num KMS/HSM real de ambiente | MITIGADO |
| DEF-104 | DEMO-GRADE | packages/integration/oidc/oidc.go | O verificador OIDC real existe; o marcador nomeia a allowlist que ele substitui. **FECHADO por AOS-228**: o nó compõe o `OIDCDirectory` por configuração (`AOS_HUMAN_OIDC_*`) | AOS-174, AOS-228 | Responsável de Segurança | Seam de configuração do directório humano no nó — **SATISFEITO por AOS-228** | FECHADO-RESIDUAL |
| DEF-105 | DEMO-GRADE | packages/integration/oidc_directory.go | `OIDCDirectory` substitui a allowlist. **FECHADO por AOS-228**: o nó compõe-o por configuração (`Config.HumanDirectory` ← `AOS_HUMAN_OIDC_*`) | AOS-174, AOS-228 | Responsável de Segurança | Idem DEF-104 | FECHADO-RESIDUAL |
| DEF-106 | STUB | packages/integration/device_attestation.go | Porta de attestation de dispositivo: no nó zero-dep vive só o contrato (bytes e erros) | AOS-177 | Responsável de Segurança | Componente externo de attestation configurado no ambiente | MITIGADO |
| DEF-107 | STUB | packages/integration/foureyes.go | Sem a porta ligada o 4-eyes é ESTRUTURAL (igualdade de strings), não duas credenciais WebAuthn atestadas distintas como o ADR-016 §4 exige | AOS-177, AOS-162 | Responsável de Segurança | Idem DEF-106 | ABERTO |
| DEF-108 | DEMO-GRADE | packages/integration/steer_authenticator.go | Marcador de contraste: o `HMACAuthenticator` replayável que o `Ed25519Authenticator` substituiu | AOS-160 | Arquitecto de Plataforma | Remoção do HMAC de referência do canal de controlo | FECHADO-RESIDUAL |
| DEF-109 | DEMO-GRADE | packages/kernel/agent-runtime/control/steer_channel.go | O `HMACAuthenticator` de referência ignora o nonce (comportamento demo mantido por compatibilidade do seam) | AOS-160 | Arquitecto de Plataforma | Idem DEF-108 | FECHADO-RESIDUAL |
| DEF-110 | DEMO-GRADE | packages/cmd/aos/bootstrap.go | `Config.Humans` alimenta a allowlist da autoridade. **FECHADO por AOS-228**: `Config.HumanDirectory` + `AOS_HUMAN_OIDC_*` compõem o `OIDCDirectory` no nó (a via sem-prova é recusada; o humano-raiz vem do `sub` verificado). A allowlist fica como default de referência quando o OIDC não é configurado (contraste, não dívida) | AOS-174, AOS-163, AOS-228 | Responsável de Segurança | Seam de `HumanDirectory` exposto na `Config` do nó — **SATISFEITO por AOS-228** | FECHADO-RESIDUAL |
| DEF-111 | STUB | packages/cmd/aos/bootstrap.go | Doc de pacote: contraste com os STUBS NEUTROS do `cmd/aos-demo`; o nó `aos` compõe a cadeia real | AOS-163 | Arquitecto de Plataforma | Remoção do ápice mínimo demo | FECHADO-RESIDUAL |
| DEF-112 | DEMO-GRADE | packages/cmd/aos-demo/main.go | Ápice mínimo: HMAC efémero gerado em runtime + registo demo; é o binário de demonstração, não o nó | AOS-163 | Arquitecto de Plataforma | Idem DEF-111 | FECHADO-RESIDUAL |
| DEF-113 | STUB | packages/cmd/aos-demo/main.go | O demo compõe o RM com STUBS NEUTROS (sem enforcement) e declara-o no passo 7 e na limitação (b) | AOS-163 | Arquitecto de Plataforma | Idem DEF-111 | FECHADO-RESIDUAL |
| DEF-201 | DEFERIDO | packages/cmd/aos/bootstrap.go | AOS-205 entregou a FONTE DE AUTORIDADE board→região (SovereignRegionAuthority, rotação+auditoria de alterações): o registo deixou de ser o mapa de env tratado como verdade. Fica o provisionamento do TENANT concreto (o serviço de config/IdP de soberania real da organização que empurra as alterações autoritativas), que é infra-org | AOS-205 | Arquitecto de Plataforma + Responsável de Segurança | Existir organização com boards/regiões reais (Carta §4.2, D7 CONDICIONAL) | MITIGADO |
| DEF-202 | DEFERIDO | packages/cmd/aos/bootstrap.go | **FRONTEIRA POR-RUN — ENTREGUE por AOS-182.** A residência do run é agora SELADA na criação (`POST /runs` → `readGovernance.sealResidency`) a partir da resolução board→região do SUBMISSOR (a MESMA autoridade `RegionFor`/AOS-094, não auto-declarada), durável e tamper-evidente numa partição WORM POR-RunID (`gov.residency/<run>`). `readGovernance.authorizeRead` resolve essa residência e EXIGE `leitor.região == run.região` em GET, trajectória (SSE) E reconstrução — cross-region ⇒ 404 uniforme não-enumerável, nunca servido; e o selo D6 grava a residência do run (obrigação `gov.read.residency`) além da região do leitor. Fail-closed no submit soberano (sem região resolvível ⇒ 403; WORM não sela ⇒ 503). Provado nos dois sentidos por `aos182_residency_test.go`. RESIDUAL nomeado (retro-compat): um run SEM residência selada (criado in-process/legado, ou pré-existente antes do deploy da soberania) é servido SEM check cross-region — o enforcement liga SÓ quando há residência selada (em modo soberano todo o run ingressado via `POST /runs` tem-na) | AOS-182 | Responsável de Segurança | KMS/IdP de soberania real que provisione a topologia board→região (DEF-201/DEF-302) — o mecanismo por-run está fechado | FECHADO-RESIDUAL |
| DEF-203 | DEFERIDO | packages/cmd/aos/sovereignty.go | Idem DEF-201 no doc de ficheiro do read-path soberano: a fonte board→região é a autoridade com rotação+auditoria (AOS-205); resta o tenant concreto | AOS-205 | Arquitecto de Plataforma + Responsável de Segurança | Idem DEF-201 | MITIGADO |
| DEF-204 | DEMO-GRADE | packages/cmd/aos/sovereignty.go | AOS-205 entregou a CREDENCIAL FORTE OIDC verificada: com ela composta o board/reader vêm das CLAIMS verificadas e o header auto-declarado deixa de autorizar. A via por headers `X-Aos-Reader`/`X-Aos-Board` fica como fallback demo-grade só FORA de produção | AOS-205 | Responsável de Segurança | Idem DEF-201 (a credencial do leitor vem do mesmo IdP de soberania) | MITIGADO |
| DEF-205 | CONDICIONAL | packages/cmd/aos/sovereignty.go | A REGRA fail-closed é FIXA (Carta §4) e AOS-205 entregou a fonte de autoridade e a credencial forte; a TOPOLOGIA continua condicional ao provisionamento do tenant concreto | AOS-205 | Arquitecto de Plataforma | Idem DEF-201 | MITIGADO |
| DEF-206 | DEFERIDO | packages/cmd/aos/api.go | Sem soberania composta os handlers mantêm o read-path LEGADO; com ela composta AOS-205 endurece o caminho (autoridade + credencial forte verificada). O fallback legado é aceite só fora de produção | AOS-205 | Arquitecto de Plataforma | Idem DEF-201 | MITIGADO |
| DEF-207 | DEFERIDO | packages/cmd/aos/dsar.go | AOS-205 entregou: o endpoint `POST /dsar/erase` passa a exigir a CREDENCIAL FORTE verificada (reutiliza o gate soberano endurecido). Resta o tenant concreto do IdP de soberania | AOS-205 | Responsável de Segurança | Idem DEF-201 | MITIGADO |
| DEF-208 | DEMO-GRADE | packages/cmd/aos/dsar.go | O endpoint reutiliza o gate soberano de leitura, que AOS-205 passou a exigir com credencial forte OIDC; a via por headers fica só fora de produção | AOS-205 | Responsável de Segurança | Idem DEF-201 | MITIGADO |
| DEF-209 | DEFERIDO | packages/cmd/aos/main.go | AOS-205: `AOS_BOARD_REGIONS` passou a SEMEAR a fonte de autoridade (rotação+auditoria) e a credencial forte vem de `AOS_SOVEREIGN_OIDC_*`; resta o provisionamento do tenant concreto | AOS-205 | Arquitecto de Plataforma | Idem DEF-201 | MITIGADO |
| DEF-210 | DEMO-GRADE | packages/cmd/aos/main.go | `parseBoardRegions` interpreta a config demo-grade que SEMEIA a autoridade board→região (AOS-205); o tenant concreto é config | AOS-203; provisionamento real AOS-205 | Arquitecto de Plataforma | Execução de AOS-203 (variáveis de ambiente + kill-switch de soberania) | MITIGADO |
| DEF-211 | DEMO-GRADE | packages/cmd/aos/bootstrap.go | `Config.BoardRegions` é a SEMENTE da fonte de autoridade board→região (AOS-205); resta o tenant concreto | AOS-203; provisionamento real AOS-205 | Arquitecto de Plataforma | Idem DEF-210 | MITIGADO |
| DEF-212 | DEFERIDO | packages/cmd/aos/sovereign_authority.go | A FONTE DE AUTORIDADE board→região (rotação+auditoria de alterações) existe e é fail-closed; o provisionamento do TENANT concreto (serviço de config/IdP de soberania real que empurra as alterações autoritativas) é infra-org, não código do nó | AOS-205 | Arquitecto de Plataforma + Responsável de Segurança | Existir organização com boards/regiões reais (Carta §4.2, D7 CONDICIONAL) | MITIGADO |
| DEF-213 | DEMO-GRADE | packages/cmd/aos/sovereign_authority.go | A semente e as rotações entram por config/API do operador (não de um tenant real); é demo-grade nesse sentido preciso, com a regra fail-closed e a auditoria de alterações reais | AOS-205 | Arquitecto de Plataforma | Idem DEF-212 | MITIGADO |
| DEF-214 | DEFERIDO | packages/platform/broker/inprocess.go | Consumo v1 IN-PROCESS (D8, decisão do dono 2026-08-10). A injecção no executor REMOTO (microVM) NÃO resolve o valor no processo do nó: entrega o `Handle` OPACO ao orquestrador, que o resolve server-side. Escrito, não construído | AOS-265; desenho-alvo D8-B | Arquitecto de Plataforma | Executor remoto a precisar de credencial (hoje o broker não está live-ligado ao GW) | ABERTO |
| DEF-215 | DEFERIDO | packages/platform/broker/inprocess.go | `Handle()` expõe o handle opaco só para CORRELAÇÃO; é o que se entregaria ao orquestrador no caminho remoto | AOS-265; idem D8-B | Arquitecto de Plataforma | Idem DEF-214 | ABERTO |
| DEF-216 | DEFERIDO | packages/platform/broker/internal/vault/kvv2.go | KV v2 (D7): o corte downstream REAL no provedor — que só dynamic secrets dão — é o desenho-alvo de quando a injecção remota ligar. A v1 corta por lease TTL+revogável | AOS-264; D8-B | Responsável de Segurança | Idem DEF-214 | ABERTO |
| DEF-217 | DEFERIDO | packages/cmd/aos/model_audit_env.go | O audit de governação do GW usa uma cadeia WORM DEDICADA. PARTILHAR o WORM único do nó exigiria construir o gateway DENTRO do Bootstrap (re-ordenação da composição do modelo); o ganho de AOS-265 é a DURABILIDADE, que este store entrega | AOS-265 | Arquitecto de Plataforma | Re-ordenação da composição do modelo no Bootstrap | ABERTO |
| DEF-218 | DEFERIDO | packages/cmd/aos/broker_vault_env.go | O cliente Vault do broker está CONFIGURADO mas a troca fica PENDENTE: ligar ao vivo exige bundle PDP assinado + identidade infra com `cap:http.post`, senão o default-deny NEGA a troca e nenhum turno de modelo executa (o próprio risco do desafio A3). **PRÉ-CONDIÇÃO ACRESCENTADA (AOS-324):** o wiring TEM de declarar a política de provedores (`broker.WithClassProviders`) e assertar que o campo `provider_policy` selado em cada `credential.exchange.issued` diz `enforced`. Sem isso o eixo *Provider* fica sem imposição por conjunto — um principal autorizado a trocar para um provedor obtém material de qualquer outro presente no Vault — e ligar o broker nesse estado é reabrir a confusão de deputado que o AOS-324 fechou por configuração | AOS-264/AOS-265; eixo D4/AOS-156 | Responsável de Segurança | Autoridade de identidade real provisionada (D4) | ABERTO |
| DEF-219 | DEFERIDO | packages/cmd/aos/promotion_api.go | Quarentena do artefacto promovido: a rota `POST /promote` ratifica e sela, mas a quarentena/rollback do artefacto em si é do controlador de promoção, não da rota | AOS-275; AOS-096 | Arquitecto de Plataforma | Pipeline de promoção com artefactos reais | ABERTO |
| DEF-220 | DEFERIDO | packages/cmd/aos/exhaustion_decision.go | **`extend` (levantar o tecto de orçamento) SAI de AOS-263 por decisão do dono (iii), 2026-08-12.** O prompt de exaustão apresenta só decisões com executor (`continue`/`abort`); `extend` não tem nenhum e não pode ter sem quebrar uma decisão de desenho: `budget.Budget` não expõe mutador de tecto e `packages/control-plane/budget/events.go` declara que os LIMITES são «configuração declarativa FORA do log de eventos — por design não reconstruíveis por `Rebuild`». Levantar o tecto em runtime exige evento próprio de orçamento, reconstrução no `Rebuild` e ADR que reabra essa decisão — trabalho de `budget`, não do prompt. Enquanto não existir, a via para um run que precisa de mais orçamento é `abort` + re-submissão com tecto maior, ou `continue` (que NÃO levanta o tecto: deixa o run correr até ele) | POR ATRIBUIR — origem AOS-263 (EPIC-20); ticket de `budget` descrito na nota §6 N-DEF-220 | Arquitecto de Plataforma | Existir necessidade operacional de levantar tecto SEM re-submeter o run (hoje a re-submissão cobre-a) | ABERTO |
| DEF-268 | DEFERIDO | packages/cmd/aos/bootstrap.go | **PRODUÇÃO DA ÂNCORA — ENTREGUE em 2026-08-20; fica deferida só a CADÊNCIA.** O nó CONSUMIA uma âncora assinada e nada no repositório a PRODUZIA: ninguém instanciava o `audit.Signer` fora de testes, pelo que as três envs nunca podiam ser preenchidas — a verificação ancorada era uma parede descrita como opção (o inventário dizia «ancoraria 1 em 108»; ancorava **zero**). Entregue `aos-issuer worm-seal`: sela OUT-OF-PROCESS, com a chave privada na máquina do operador (molde AOS-156), contra a cópia que o backup traz off-host, e recusa selar sobre um WORM que RECUOU face à selagem anterior (`--anterior`) — sem essa guarda, uma cópia truncada produzia uma âncora válida para uma cadeia truncada, porque uma cadeia truncada re-encadeia como íntegra. A ancoragem passou também a cobrir MUITAS partições (`Checkpoints`/`ExpectedHeads` por partição) e o banner declara a COBERTURA (`N de M`). O QUE FICA DEFERIDO: a cadência — quem corre a selagem, e com que frequência. Não é código: a cobertura NUNCA é completa por desenho (as partições nascem por run), pelo que selar mais vezes encolhe a janela não-ancorada sem a fechar; escolher essa frequência é decisão operacional | AOS-268; custódia de chave AOS-156 (D4) | Responsável de Segurança | Definir e automatizar a CADÊNCIA de selagem (o mecanismo está entregue; falta a periodicidade e onde corre) | ABERTO |
| DEF-269 | DEFERIDO | packages/cmd/aos/posture_banner.go | Idem DEF-268 na linha de banner `wormAnchorPostureBanner`. A dobra ANCORADA passou a declarar a **COBERTURA** (`ANCORADA em N de M partição(ões)`) e não só o estado: dizer apenas «ANCORADA» seria verdadeiro sobre o mecanismo e enganador sobre o efeito, que é a forma de falha que este sistema já pagou noutro eixo. A dobra NÃO ANCORADA nomeia as três envs e aponta o comando que produz as âncoras. A postura anunciada = postura ligada (AOS-203/AOS-248) | AOS-268; custódia de chave AOS-156 (D4) | Responsável de Segurança | Idem DEF-268 | ABERTO |
| DEF-270 | DEFERIDO | packages/cmd/aos/saga_compensation.go | **COMPENSAÇÃO REAL (failed→compensating→ready) — DEFERIDA; a CONDUÇÃO atribuível está ENTREGUE (AOS-254, parcial).** O nó compõe o `saga.SagaCoordinator` na cadeia de produção (`Node.Compensations` + `WithCompensationRegistry` no dispatcher durável) e `sealTerminalState` conduz o desfecho `failed` — a única origem da saga — para `driveSagaCompensation`. O que está ENTREGUE e provado pela cadeia real (`aos254_saga_compensation_test.go`) é o **único caminho ALCANÇÁVEL hoje**: com o registo VAZIO (a postura de produção), a ausência de compensação é **DECLARADA** (span + selo WORM na partição da saga, `reason=saga_no_compensation_registered`, `Decision=Deny`, titular atribuído) e o run **permanece `failed`** — nunca em silêncio. O que fica DEFERIDO são as duas peças sem as quais a compensação real é inalcançável: (a) um **PRODUTOR** de compensações — o loop base nunca popula `activity.Activity.Compensation`, pelo que o registo está sempre vazio e `failed→compensating` é código morto; (b) o registo **RUN-SCOPED** — `saga.Compensation` (kernel) carrega só `StepID`, não `run_id`, e o registo é nó-scoped: com compensações de vários runs, o gate `Len()==0` e o `Reversed()` global fariam um run compensar efeitos de OUTRO run (landmine documentado em «LIMITE conhecido», hoje INALCANÇÁVEL por (a)). Fechar (b) exige mudar o kernel (`Compensation` com `RunID`, chave `run_id:step_id`) — escopo próprio, subsistema sensível. Enquanto (a) não existir, nenhum efeito reversível é registado e o caminho declarado é o correcto | AOS-254 (parcial); kernel `agent-runtime/saga` | Arquitecto de Plataforma | Existir um PRODUTOR de compensações (activities reversíveis) **e** o registo run-scoped no kernel — antes disso, habilitar a compensação seria compensar o run errado | ABERTO |
| DEF-271 | DOCUMENTAL | specs/EPIC-20_Prontidao_Agentica_Remediacao.md | **[FECHADO 2026-08-13 por AOS-280 — o registo histórico abaixo mantém-se para leitura; a composição EXISTE.]** **COMPOSIÇÃO do estágio de roteamento no pipeline do GW — era DEFERIDA; a MÁQUINA de scoring está ENTREGUE (AOS-269).** O ADR-021 está implementado por inteiro sobre o `router.Router`: portas de factores (health, headroom, custo, latência, task-fit, estabilidade) em aritmética INTEIRA, tabela de pesos EMBEBIDA e ASSINADA (ed25519 + trust anchor pinado, molde de `policy/allowlist`) com carregamento fail-closed, guard-test AST que proíbe float/rand/relógio no caminho de decisão, e o cenário de soberania que prova que NENHUM peso elege candidato cross-border. O que fica deferido é o **wiring**: o `router.Router` (AOS-059) **nunca esteve composto em produção** — `NewProduction` compõe `failover.NewStage` como estágio de roteamento e `router.New` não tem chamador fora de testes em todo o repositório. Logo o scoring, construído SOBRE esse router, **não tem efeito em produção** enquanto o estágio não for composto. A dívida é **PRÉ-EXISTENTE a AOS-269** (o router já estava por ligar antes deste ticket) e fechá-la é substituir/complementar o `failover` no pipeline — mudança arquitectural do caminho quente de TODAS as chamadas de modelo, com verificação própria. Alcance declarado na **emenda 1.1 do ADR-021** (§5-bis, 2026-08-13): na v1 o scoring é composto por opção e a regra 3 aplica-se quando está composto | AOS-269 (máquina); **AOS-280 (wiring) — ENTREGUE 2026-08-13** | Arquitecto de Plataforma | **FECHADO por AOS-280:** `NewProduction` compõe o slot de roteamento como a CADEIA `failover` → `routingstage(router.New(…, WithScoring(…)))` (`production_routing.go`, `pipeline.Chain`), com classificador de produção (candidatos = inventário ∩ regiões legais ∩ saudáveis; perfil por classe validado contra a tabela assinada no ARRANQUE) e as portas de carga/custo/latência/saúde ligadas a fontes reais do módulo. Prova pela CADEIA REAL ao nível do gateway composto (`routing_chain_aos280_test.go`, 9 testes com positivo e negativo, na lista REQUIRED do gate `routing.sh`). RESIDUAIS com eixo próprio: DEF-280-PORTAS (budget/task-fit ficam seams — fonte noutro módulo), DEF-280-TOKENS (o Exchange não transporta o prompt) e DEF-280-NO (o nó de referência ainda não declara escada de tiers, logo o binário do nó continua a rotear só pelo failover) | FECHADO-RESIDUAL |
| DEF-272 | DOCUMENTAL | specs/EPIC-20_Prontidao_Agentica_Remediacao.md | **[FECHADO 2026-08-30 por AOS-281 — o registo histórico abaixo mantém-se para leitura; o EMISSOR EXISTE.]** O chamador de produção de `Recorder.RecordVerdict` é `runlifecycle.PlanRecorder.RecordVerdict` (`packages/control-plane/runlifecycle/emitters.go`), e o CONSUMO fecha o circuito: `runlifecycle.ResultReader` implementa `plandispatch.ResultView` sobre o log, projectando o facto por `ResultFromVerdict`. Ambos vivem no módulo que o ADR-023 §2.6 identifica como o PRIMEIRO sítio do repositório que os pode legalmente conter — o eixo anterior (AOS-238) estava proibido por guard-test de o fazer. A emissão é FENCED pelo lease do run: um emissor cuja posse foi superada é recusado com `ErrStaleFencingToken` sem tocar no log (`TestEmissor_DonoSuperado_NaoEscreveNoStreamDoPlano`). Circuito provado ponta-a-ponta em `TestDEF272_VeredictoEmitidoEConsumido`. **REGISTO HISTÓRICO:** **EMISSÃO do veredicto do verificador — SEM CHAMADOR DE PRODUÇÃO (AOS-271).** O contrato tipado (`plannerevents.NewVerdictRecorded`: pass/fail + códigos + inteiros, sujeitos amarrados às arestas de entrada do verificador no documento aprovado), a atribuição na admissão (`planvalidate/verifier.go`) e a projecção para o observável do ramo (`plandispatch.ResultFromVerdict`) estão fechados; o que NÃO existe é quem chame `Recorder.RecordVerdict` por um verificador em execução — é wiring do ciclo-de-vida do run. O AC «veredicto registado como evento» foi DESMARCADO no EPIC-20 por esta razão. EIXO DE SEGURANÇA (o que a auditoria adversarial da wave falsificou na afirmação anterior de que não havia nenhum): com a admissão a aceitar ramos sobre `verdict` e nenhum emissor ligado, um ramo de qualidade admitido era DECIDIDO como `branch_not_taken` e o facto ficava REGISTADO — poda silenciosa de metade do organigrama aprovado. MITIGADO nesta passagem do lado do despacho: a ausência de um observável no resultado registado é agora INDECIDA (o nó espera, visível em `waiting_condition`), nunca falsa, pelo que nenhuma decisão de ramo é registada sobre um observável que ninguém produz | AOS-281 | Arquitecto de Plataforma | Existir a composição ORQ/SCH<->nó sob disciplina de lease (AOS-281, EPIC-10). CORRIGIDO a 2026-08-30: o eixo era AOS-238, que está FECHADO e cujo guard-test `TestBoundary_ProductionImportsAreAllowlisted` o PROÍBE de importar o módulo de ciclo-de-vida — apontava para um sítio onde esta dívida nunca poderia ser paga. O gate `deferrals` não o apanha porque verifica que o eixo EXISTE, não que ele cobre | FECHADO-RESIDUAL |
| DEF-273 | DOCUMENTAL | specs/EPIC-20_Prontidao_Agentica_Remediacao.md | **[FECHADO 2026-08-30 por AOS-281 — as DUAS metades; o registo histórico abaixo mantém-se para leitura.]** (1) TRANSPORTE: `runlifecycle.PayloadReader` implementa `plandispatch.PayloadView` sobre o log (projecção por `RefFromPublished`) e `runlifecycle.PlanRecorder.RecordPayloadPublished` é o chamador de produção — ambos fenced pelo lease do run. Provado pelo CONSUMIDOR REAL e não pela porta em cru: `TestDEF273_PayloadPublicadoEResolvido` passa pelo `plandispatch.PayloadResolver`, que re-verifica tipo, taint efectivo e `contract_digest` contra o documento aprovado; `TestDEF273_SemPublicacao_ConsumidorNaoLe` prova que o verde vem da publicação e não de permissividade. (2) ORÁCULO DE EFEITO: `runlifecycle.Tenure.Materializer` compõe o `planmaterialize.Materializer` e DERIVA o oráculo de `planvalidate.Snapshot.EffectOracle()`. Não é uma opção que alguém tenha de se lembrar de ligar — é acrescentada DEPOIS das opções do chamador (a última vence), pelo que um `WithEffectOracle` permissivo vindo de fora não pode baixar o clamp; e um snapshot vazio é RECUSADO (`ErrSemSnapshot`), porque um snapshot em que nada resolve é o `DefaultEffectOracle` por outro nome. A propriedade medida é a CONSEQUÊNCIA, não a atribuição: um nó `verifier` materializa com a tool read-only INTACTA e a de efeito RETIRADA (`TestDEF273_OraculoReal_VerificadorMantemAutoridadeReadOnly`), com não-vacuidade a reproduzir o estado antigo (autoridade VAZIA com o oráculo por omissão) e prova através de um PROCESSO REAL (`TestDEF273_OraculoRealAtravesDeProcessoReal`, binário `aos-orq --plan-doc --snapshot`). Guarda de source: `TestGuard_OraculoDeEfeitoNaoEOpcional`. A admissão global do materializador é `runlifecycle.BudgetAdmission` sobre `budget.Reserver` (ADR-008), com Reserve→Commit/Release para não vazar reservas num plano abortado. **REGISTO HISTÓRICO:** **TRANSPORTE de payload tipado — SEM CHAMADOR DE PRODUÇÃO (AOS-272).** Não existe implementação de `plandispatch.PayloadView` nem chamador de `Recorder.RecordPayloadPublished` fora de testes: nenhum consumidor recebe referência nenhuma num run real (`ErrPayloadNotPublished` em qualquer passagem). O AC «o consumidor recebe referência/resumo» foi DESMARCADO no EPIC-20 por esta razão. EIXO DE SEGURANÇA (a afirmação anterior de que não havia nenhum era FALSA): sem `planmaterialize.WithEffectOracle` ligado pelo mesmo wiring, o materializador usa o `DefaultEffectOracle` — tudo conta como efeito — e emite verificadores com `Authority[]` VAZIA e sem tool call, isto é, o AC1 de AOS-271 cumpre-se por o verificador não conseguir fazer nada | AOS-281 | Arquitecto de Plataforma | Existir a composição ORQ/SCH<->nó sob disciplina de lease (AOS-281, EPIC-10) que implemente a `PayloadView` e ligue o oráculo de efeito real. CORRIGIDO a 2026-08-30 pela mesma razão do DEF-272: o eixo AOS-238 está fechado e é impedido por guard-test de fazer esta ligação | FECHADO-RESIDUAL |
| DEF-274 | DOCUMENTAL | tecnica/18_Planner_Meta_Orquestracao.md | **O GATE VÊ AS EXTENSÕES DE ADR-022 — PORTA E CARTÃO ENTREGUES (invariante §2.4(5)/§5).** O que estava registado («`planapproval.PlanNode`/`PlanCard` não projectam `role`, `conditional_on`, `outputs` nem `consumes`») deixou de ser verdade: a porta local ganhou `Role`/`ConditionalOn`/`Outputs`/`Consumes` e o `PlanCard` projecta-os em `node_extensions[]` por nó, na ordem topológica — papel (`verifier` distingue quem julga de quem produz), condições de entrada em forma canónica (`revisao{verdict=eq=fail,metric(fontes)=lt=3}`) e contratos de dados (`nome:tipo:taint`, `origem:output:tipo`), com `PlanCard.VerificationView()` a dar «quem verifica quem, sob que condição». Três propriedades sustentam-no e são falsificáveis (`def274_extensions_test.go`): (a) **content-free por estrutura** — só símbolos de charset ASCII fechado, inteiros decimais canónicos e referências a nós do próprio plano atravessam a porta; um valor de payload/excerto de output/locator RECUSA o plano (`ErrNonCanonicalExtension`), provado com sentinela de segredo em CADA campo — incluindo o TIPO do consumo, que a wave anterior afirmava cobrir sem ter o caso — e nas DUAS portas: a de construção (`Plan.Validate`/`BuildPlanCard`) e a do WIRE (`PlanCard.UnmarshalJSON` re-parseia a mesma gramática; antes da remediação, `role`/`conditions[].canonical`/`outputs`/`consumes` eram texto livre na desserialização, e o wire é a porta por onde os adaptadores entram); (b) **a ordem apresentada é a que vai correr, e o grafo apresentado também** — a aresta guardada por condição conta como precedência na ordenação do cartão e, com ela, na detecção de ciclo (invariante 1 do ADR, sem travessia nova), E é SERIALIZADA em `conditional_edges[]`, distinta de `edges[]` (que continua a ser o canal declarado, porque é o que a edição humana devolve em `RevisedEdges`): sem o campo, o único campo do cartão com forma de grafo omitia a ligação guardada e uma superfície que desenhasse o organigrama a partir de `edges` mostrava o nó guardado como RAIZ SEM ENTRADA. A projecção por-nó é POSICIONALMENTE ALINHADA com a ordem (não só do mesmo comprimento) e a `Consumes.From` tem de ser uma aresta de entrada declarada do nó, espelhando a invariante de montante de `plan/plandocument.go`; (c) **desacoplamento preservado** — o módulo NÃO importa `orchestrator/plan` (go.mod intocado): a forma canónica é declarada localmente e a isomorfia com `plan.CanonicalConditional`/`CanonicalOutput` é PINADA POR LITERAL no teste (um módulo desacoplado não pode cruzá-la de outra maneira — e é o preço declarado do desacoplamento, não um descuido). Contrato do cartão sobe a `aos.plan.card.v1` **1.1.0** (MINOR: campo aditivo `omitempty`, nil quando não há extensões — um plano pré-ADR-022 produz o cartão de sempre, provado pelo caso negativo). O diff de edição ganhou `changed_extensions` (uma edição que troca um verificador por um produtor deixou de se registar como «sem mudança estrutural»). **RESIDUAL — o WIRING:** o mapeamento `PlanDocument → planapproval.Plan` (ler `plan.Node.Role`/`ConditionalOn`/`Outputs`/`Consumes` e o taint EFECTIVO de `EffectiveOutputTaint`, e povoar a porta) vive a jusante e NÃO existe em produção — nenhum caminho compõe hoje o planeador com o gate, e o `aos-demo` constrói o `Plan` à mão. Até esse mapeador existir, a visibilidade é uma CAPACIDADE DO CONTRATO, não um facto de fim-a-fim | AOS-238 | Arquitecto de Plataforma | Wiring PlanDocument→gate (AOS-238) que povoe a porta a partir do plano real — a projecção e o cartão já não bloqueiam | FECHADO-RESIDUAL |
| DEF-275 | DOCUMENTAL | tecnica/18_Planner_Meta_Orquestracao.md | **EIXO DE MUTACAO em falta no criterio de tool de efeito (ADR-022 §2.2).** `planvalidate.IsEffectTool` decide read-only por dois eixos pinados — egress e reversibilidade — e a ponte «escrita em MEM e um efeito» apoia-se numa INVARIANTE DE CLASSIFICACAO do REG (toda a capability mutadora e classificada `Irreversible`) que o codigo NAO impoe: uma escrita local com undo classificada `EgressNone`+`Reversible` conta como leitura, e entao um verificador poderia pinar a tool que mexe no que revê, e um consumidor com autoridade de escrita nao contaria como privilegiado para a regra de taint. Acrescentar um quarto eixo fail-closed (desconhecido implica mutador) e a direccao certa, mas hoje NAO existe nenhuma construcao de `planvalidate.Capability` fora de testes — o snapshot pinado chega pelo wiring do REG —, pelo que o eixo novo classificaria so fixtures e daria a ilusao de uma garantia que ninguem alimenta. A invariante esta escrita onde e lida (o doc de `IsEffectTool`) e o eixo entra junto com o construtor real do snapshot, com teste sobre o CATALOGO e nao sobre um literal de teste | AOS-238 | Responsável de Segurança | Existir construtor de produção do snapshot de capabilities (o wiring do REG, AOS-238) — antes disso o eixo novo classificaria só fixtures | ABERTO |
| DEF-276 | DOCUMENTAL | specs/EPIC-20_Prontidao_Agentica_Remediacao.md | **FECHADO por AOS-279 (2026-08-13): o golden-set do planeador PASSA a correr no gate `evalgate.sh`.** O gate corre o modulo do planeador SEPARADAMENTE e consome a linha marcada `AOS_PLANNER_EVAL_REPORT`, impondo cobertura nao-vazia + seguranca 100% + o veredicto da politica do proprio planeador; a qualidade fica como sinal reportado (8/10 por desenho). NAO se acoplou `platform/eval` a `control-plane/orchestrator`. Texto original: **GOLDEN-SET DO PLANEADOR NAO CORRIA NO GATE `evalgate.sh` (AOS-273, AC2).** Os cinco casos de ADR-022 (condicional, verificador, payload, o negativo da auto-certificacao e o negativo do carimbo obsoleto) correm no eval-gate do planeador — `plannerprompt.Evaluate`, AOS-241 — como testes do pacote `control-plane/orchestrator/plannerprompt`, que a CI executa. O gate de CI chamado `evalgate.sh` corre OUTRO harness (`packages/platform/eval`, ADR-012/AOS-114) e nao conhece este conjunto: o pass-rate do planeador nao entra no relatorio `AOS_EVAL_REPORT` nem no piso de 0.90. SEM EIXO DE SEGURANCA, e a distincao e deliberada: a cobertura EXISTE e BLOQUEIA (um plano canonico de ADR-022 que o validador recusasse, ou um caso dificil removido sem aprovacao, avermelha o modulo); o que falta e a SINALIZACAO no gate com esse nome e a agregacao do pass-rate. A fronteira «biblioteca pura de eval offline, handoff para um job de CI de staging» ja estava DECLARADA por AOS-241 (`plannerprompt/doc.go`) e nao foi re-clamada por AOS-273 — o AC2 foi PARTIDO em vez de marcado inteiro | AOS-241 (fronteira declarada); wiring do harness AOS-279 | Arquitecto de Plataforma | Ticket que ligue o golden-set do planeador ao harness de `packages/platform/eval` (ou que estenda `evalgate.sh` a este conjunto), com o pass-rate a entrar no `AOS_EVAL_REPORT` | FECHADO-RESIDUAL |
| DEF-277 | DOCUMENTAL | specs/EPIC-20_Prontidao_Agentica_Remediacao.md | **FONTE DE PRECO PARA O MODELO DE UM NO REAL — POR CURAR (AOS-259, canal FECHADO).** O canal de custo esta ligado ponta a ponta e provado em composicao real: o Model Gateway deriva o custo dos quatro contadores de token pela tabela versionada e tamper-evident de `model-gateway/pricing`, escreve-o em `port.Usage.CostMicroUSD`, o adaptador RT->GW projecta-o em `ModelResponse.CostMicroUSD` e dai flui para o span `chat`, para `Result.TotalCostMicroUSD` e para o campo `cost_micro_usd` do evento duravel `turn.recorded` que o burn-down le. O que NAO existe e uma tabela de precos de MERCADO: a embebida cobre so os pares de referencia do repositorio (`claude-sonnet`/`claude-haiku`/`gpt-4o` x `eu-west`/`us-east`) e o default `AOS_MODEL_REGION=eu` nem sequer casa com ela, pelo que um no configurado com o modelo do operador nao tem preco e o canal transporta ZERO. ESCOLHA DELIBERADA, nao esquecimento: inventar rates para o modelo do operador seria pior do que nao haver custo, porque o burn-down em dolares passaria a mentir com autoridade — um canal que transporta zeros honestos e melhor do que um que transporta numeros fabricados. O que ESTE ticket entregou para o fechar do lado do operador: `AOS_MODEL_PRICING_PATH` carrega a tabela do operador pelo mesmo `pricing.LoadTable` (mesmo formato, mesmo digest, fail-closed se nao carregar), a cobertura do par e verificada UMA VEZ no arranque (para o fail-closed por chamada nao virar brownout total) e o estado — ARMADO ou FONTE AUSENTE — e DECLARADO no banner de postura, com o zero explicitamente nomeado como AUSENCIA DE DADOS e nao custo nulo. SEM EIXO DE SEGURANCA e sem cegueira do burn-down: a dimensao que DECIDE continua a ser TOKENS (`AOS_BUDGET_MAX_TOKENS`), lida do ledger de turnos e fail-closed (`ErrBurndownNoUsage`); o que fica por curar e a CURADORIA da tabela. NOTA POS-AOS-260: o eixo de DOLARES passou a ter tecto proprio (`AOS_BUDGET_MAX_COST_MICRO_USD`), e o no RECUSA aceita-lo sem fonte de preco para o par (modelo, regiao) — `ErrBudgetCostNoPriceSource`, fail-closed no arranque no molde de `ErrProgressBudgetUnwired` —, precisamente para que um tecto em $ nunca seja uma capacidade-fantasma comparada contra zero | AOS-259 (canal entregue); curadoria da tabela e decisao do dono / trabalho de operacao | Arquitecto de Plataforma | Existir uma tabela de precos de mercado curada e versionada para os modelos que o no serve — montada por `AOS_MODEL_PRICING_PATH` num deployment, ou promovida a documento versionado do repositorio se o dono decidir que o AOS a distribui | FECHADO-RESIDUAL |
| DEF-278 | DOCUMENTAL | specs/EPIC-20_Prontidao_Agentica_Remediacao.md | **RESIDUAIS DA ADMISSAO DO TURNO DE MODELO (AOS-260, eixo FECHADO).** O turno de modelo passou a ser ADMITIDO: a porta `agentruntime.ModelAdmission` RESERVA no no de orcamento por-run imediatamente antes de `rt.model.Call` e SALDA com o consumo MEDIDO da resposta (`usage` + `cost_micro_usd` de AOS-259); o esgotamento produz degradacao declarada (suspensao HITL de AOS-263, ou paragem propria selada como `timed_out`/`budget_exhausted`) e o replay NAO re-reserva (dedup por `run_id:step_id` + detector do plano de retoma). Ficam TRES limitacoes, todas DECLARADAS no banner de postura e em `deploy/node/README.md`, nenhuma com eixo de seguranca: (a) o TECTO EM DOLARES e admitido UM TURNO TARDE no primeiro turno de cada incarnacao — a projecao de custo sai da TARIFA MEDIDA do proprio run e no primeiro turno ainda nao ha medicao, pelo que esse turno decide so por TOKENS; importar a tabela de precos do Model Gateway para o runtime duplicaria a fonte de verdade do preco que AOS-259 fixou; (b) um turno cujo consumo REAL exceda o que restava do tecto e cobrado ATE AO TOPO (run a 100%) e ARMA a negacao seguinte — o excedente exacto vive no ledger duravel de turnos, que e a fonte do burn-down, nunca se perde; nao ha forma de nao pagar um turno que ja correu; (c) o tecto continua POR-INCARNACAO (a arvore de orcamento vive em memoria e o no do run e recriado em cada hospedagem), enquanto o burn-down/aviso le o ledger duravel e E cumulativo — a assimetria ja estava declarada desde AOS-256 e AOS-260 nao a alterou, so a estendeu ao turno de modelo. ACRESCENTADO na remediacao adversarial da wave: (d) a reclamacao de PROVISAO ORFA esta ARMADA mas INALCANCAVEL hoje — `SettleTurn` repoe a reserva no mapa de pendentes em cada caminho de erro para que `forgetRun` continue a ser a rede de seguranca que promete ser, mas `NewRunBudget` compoe `budget.New` SEM emitter, pelo que `Release`/`Commit` nao falham. A defesa so passa a ter efeito observavel quando o orcamento por-run for DURAVEL (o mesmo trabalho que fecha (c)): a partir dai uma indisponibilidade transitoria do substrato faria a RAIZ da arvore encolher monotonamente a cada turno afectado, e o no acabaria a negar todos os runs com uma razao que parece falta de orcamento | AOS-260 (eixo entregue); orcamento DURAVEL por-run: POR ATRIBUIR | Arquitecto de Plataforma | Existir estado de orcamento DURAVEL por `run_id` (que fecharia (c) e tornaria o tecto verdadeiramente por-run entre incarnacoes); e, para (a), um produtor de tarifa por (modelo, regiao) consultavel do lado do runtime SEM duplicar a tabela de precos do Model Gateway | FECHADO-RESIDUAL |
| DEF-279 | DEFERIDO | packages/cmd/aos/model_pricing_env.go | **COBERTURA DE PRECO VERIFICADA PARA O PAR PEDIDO, NAO PARA TODOS OS PARES ALCANCAVEIS (AOS-259).** `resolveModelPricing` verifica no arranque `table.RateFor(model, region)` com o par de config (`AOS_MODEL_NAME`/`AOS_MODEL_REGION`), mas `Gateway.recordCost` calcula com o par RESOLVIDO pelo roteamento (`ex.ResolvedModel`/`ex.ResolvedRegion`, que `failover.Stage.Process` preenche com a regiao do endpoint ESCOLHIDO). Hoje as duas coincidem sempre e a garantia e REAL, mas e uma garantia do INVENTARIO e nao da verificacao: `newGatewayModelClient` compoe UM unico `modelgateway.InfraAccount` na regiao pedida e nao ha outra por onde falhar. Acrescentar uma segunda conta/regiao ao keypool — a razao de o router de failover existir — torna alcancavel um par sem preco: `pricing.ErrNoPrice` -> `costError` -> chamada RECUSADA, e o mecanismo de resiliencia passa a ser a causa de uma interrupcao total. SEM EIXO DE SEGURANCA hoje (inalcancavel com um so inventario); o limite esta DECLARADO no banner do canal de custo, no doc de `resolveModelPricing` e no ponto de composicao do inventario, em vez de calado | AOS-259 (verificacao de arranque entregue); extensao a todos os pares: POR ATRIBUIR | Arquitecto de Plataforma | Existir mais do que uma conta/regiao no inventario do keypool do no — nessa altura a verificacao de arranque tem de iterar o inventario, nao so o par pedido | ABERTO |
| DEF-280-PORTAS | DEFERIDO | packages/platform/model-gateway/production_routing.go | **Duas portas de factor do refino ficam SEAMS sem fonte ligada (AOS-280).** A composição de produção liga às fontes REAIS a carga (`keypool.Registry.Headroom`, leitura pura nova), o custo (escada de tiers), a latência (p95/bit `Fast`) e a saúde (a mesma `Health` do failover). Ficam por ligar o **orçamento** (`RoutingConfig.Budget`) e o **task-fit** (`RoutingConfig.TaskFit`): a fonte de verdade do primeiro é o burn-down do control-plane (EPIC-03) e a do segundo é o eval harness (EPIC-08), e importá-los do caminho quente do GW violaria o layering que `metering/cost/budgetbridge` e `routing/tieradapter` isolam de propósito. **Consequência declarada:** sem `Budget` ligado NÃO há oferta de degradação graciosa (o router mantém o tier capaz escolhido) e sem `TaskFit` o factor de qualidade vale 0 para todos os candidatos — não discrimina, nunca dá crédito não-ganho. Nenhuma das duas ausências é fail-open | AOS-280 | Arquitecto de Plataforma | Ticket de composição no composition root (`packages/integration`/nó) que ligue o `BudgetProvider` ao burn-down real e o task-fit ao relatório do eval harness | ABERTO |
| DEF-281 | DEFERIDO | packages/substrate/otel-genai/slo.go | **O SLI `mediation_overhead_p95` NAO MEDE OVERHEAD DE MEDIACAO.** Os DOIS produtores de `execute_tool` incluem o despacho da tool: o do worker envolve gate fenced + `ledger.Apply(mediacao + despacho)`, e o do Reference Monitor parece ser so a mediacao mas `Monitor.evaluate` chama `m.dispatch` ANTES de devolver a decisao, pelo que o span so fecha depois de a tool correr. O filtro `Decision != ""` (AOS-085) escolhe a JANELA MAIS ESTREITA disponivel — a do monitor, que ao menos exclui o gate fenced e o ledger de idempotencia — mas o numero publicado continua a ser um TECTO SUPERIOR do custo de uma tool call mediada, e nao o custo que a mediacao acrescenta. Consequencia operacional: o alerta `mediation_overhead_high` (critical, RB-04) dispara em qualquer no com trafego real, porque o SLO de 15ms esta calibrado para avaliacao de politica. MEDIDO em producao a 2026-08-27 com o filtro activo: 1 amostra, 3,047s; antes do filtro, 2 amostras e 4,017s. O limite esta DECLARADO no doc de `overheadP95SLI`, no cabecalho de `slo.go` e no teste que o acompanha, em vez de calado | POR ATRIBUIR — cronometrar a cadeia de politica EXCLUINDO a janela do `dispatch`; e o mesmo follow-up que `packages/cmd/aos/api.go` ja declarava ao dizer que as latencias de request exigem histogramas instrumentados no kernel | Arquitecto de Plataforma | Existir instrumentacao que separe a cadeia de politica do despacho — nessa altura o SLI passa a poder medir o que o nome promete e o SLO de 15ms torna-se alcancavel | ABERTO |
| DEF-280-TOKENS | DEFERIDO | packages/platform/model-gateway/routing/routingstage/classifier.go | **`Task.EstimatedTokens` fica a ZERO (AOS-280).** O `pipeline.Exchange` não transporta o prompt (a fachada do GW passa-o directamente ao adaptador), pelo que o classificador não tem por onde estimar tokens sem inventar. **Consequência declarada:** a reserva de admissão global (ADR-008) coordena na dimensão de PEDIDOS (custo mínimo 1) e não na de TOKENS — um pedido muito grande consome tecto de tokens que não reservou. Não é fail-open (o defer por pedidos continua a valer), é uma reserva SUB-estimada | AOS-280 | Arquitecto de Plataforma | Transportar no `Exchange` uma estimativa de tokens (ou o contador de prompt) e passá-la ao classificador — mudança do contrato do pipeline, com o seu próprio ticket | ABERTO |
| DEF-280-NO | DEFERIDO | packages/cmd/aos/modelgatewaywiring.go | **O NÓ DE REFERÊNCIA não declara escada de tiers (AOS-280).** A cadeia `failover` → refino arma-se quando o deployment declara `RoutingConfig.Tiers`; o nó (`packages/cmd/aos`, OUTRO módulo, fora do âmbito do ticket) não a declara, pelo que o binário do nó continua a rotear **só pelo failover** — o refino existe, está composto e provado no módulo do GW, mas não corre naquele deployment. A escada é decisão de DEPLOYMENT e não se adivinha: cada modelo declarado tem de estar coberto pela allowlist regional do board E pela tabela de preços da região (senão `pricing.ErrNoPrice` recusaria a chamada — o mesmo aviso que a nota da conta única já faz no wiring do nó) | AOS-280 | Arquitecto de Plataforma | Ticket no nó que declare a escada (env `AOS_MODEL_TIERS` ou equivalente assinado), verifique a cobertura de preço/credencial de TODOS os pares alcançáveis e ligue `ProductionConfig.Routing` | ABERTO |
| DEF-280-REGIAO | DEFERIDO | packages/platform/model-gateway/production_routing.go | **A selagem WORM da decisão do refino cobre a troca de MODELO, não uma mudança só de REGIÃO (AOS-280, remediação 2026-08-13).** O terceiro elo da cadeia (`modelSwapRecorder`) sela um `allowlist.GovRecord` para o par EFECTIVO (modelo + região) quando `ex.ResolvedModel != ex.RequestedModel` — o facto NOVO que a composição introduziu. Quando o refino mantém o modelo e só muda a região DENTRO da fronteira já validada, mantém-se a postura de AOS-058, inalterada por este ticket: é a guarda de soberania que a decide (o deny cross-border continua selado) e o registo de atribuição (`Gateway.attribute`) que sela a região efectiva. **Consequência declarada:** o trilho de GOVERNAÇÃO não tem um allow próprio para (board, modelo, região-efectiva) quando só a região muda; a região efectiva é reconstruível por correlação com a atribuição, selada na mesma chamada. Não é fail-open (nenhuma região fora da fronteira é alcançável) | AOS-280 | Arquitecto de Plataforma | Decidir se a resolução de região do failover passa a selar um allow próprio por chamada (custo: +1 registo WORM em CADA chamada com failover) ou se a correlação com o registo de atribuição basta ao auditor | ABERTO |
| DEF-280-ADR021 | DOCUMENTAL | docs/adr/ADR-021-scoring-deterministico-gw.md | **FECHADO por RATIFICACAO DA EMENDA 1.2 (dono, 2026-08-14).** O ADR-021 ganhou o §5-ter: declara que DEF-271 fechou, que o scoring TEM efeito no caminho de producao do gateway quando o deployment declara a escada, e que a regra 3 passou de postura a RECUSA DE ARRANQUE. O §5-bis fica por baixo, marcado SUPERADO e nao reescrito, para a revisao ver QUANDO deixou de valer. A divergencia ADR-vs-codigo que esta linha registava deixou de existir. Registo historico: **O ADR-021 §5-bis (AUTORIDADE CONGELADA) diz que o scoring «não tem efeito em produção» e que DEF-271 é dívida aberta — e desde AOS-280 nenhuma das duas coisas é verdade.** O código composto e o doc do pacote `router` afirmam o contrário do ADR, e a Carta §6 manda ler o ADR como autoridade: uma revisão futura concluiria que o comentário do implementador é uma leitura não-ratificada — precisamente o erro que a emenda 1.1 tinha corrigido. O ADR **não foi editado** por esta remediação, de propósito. **Texto proposto para a emenda 1.2 (decisão de dono):** «estado em 2026-08-13: DEF-271 FECHADO por AOS-280; o scoring está composto no caminho de produção quando o deployment declara a escada de tiers (`RoutingConfig.Tiers`), e a regra 3 é então uma recusa de ARRANQUE, não uma postura opt-in» | AOS-280 (remediação) | Dono (autoridade de ratificação) | Emenda 1.2 do ADR-021 datada e ratificada pelo dono, actualizando o §5-bis; até lá, esta linha é a fonte da divergência declarada | FECHADO-RESIDUAL |
| DEF-301 | DEFERIDO | packages/cmd/aos/bootstrap.go | **Cifra por-titular do substrato — NÚCLEO ENTREGUE (AOS-093).** O conteúdo não-determinístico dos runs (resposta do modelo + resultados de tools do capturer de replay) é agora CIFRADO por chave POR-TITULAR (envelope DEK/KEK, `audit.SealContent`) ANTES de tocar o WAL; a erasure DSAR destrói a MESMA KEK ⇒ o conteúdo fica IRRECUPERÁVEL (decifragem falha, provado ao nível do nó em `aos093_substrate_erase_test.go`) e a hash-chain do WORM continua a validar. O capturer regista subject→stream no DSARIndex (shred/hold alcançam o substrato). RESIDUAIS nomeados: (a) `turn.recorded` persiste só hashes (prompt_hash/system_hash), nunca o prompt cru — nada a cifrar; (b) o REPLAY do lado do LEITOR (reconstrução/inspecção de um run selado por um terceiro) — **ENTREGUE por AOS-214**: `ContentOpener` gated por soberania na reconstrução (`GET /runs/{id}/reconstruct` atrás de D7+D6), o leitor autorizado decifra, o não-autorizado nunca vê claro (`ErrPayloadAccessDenied`), o shred aguenta o replay (`ErrDecrypt`) e o legal hold preserva a reconstruibilidade; o resume durável in-process já ficava fail-closed em AOS-093 — este residual passa a nomear só o resume, **já resolvido**; (c) o step-ledger só sela com `Producer.NHIID` (o run injecta o principal) | AOS-093 (CA #1 — ver arbitragem A-DEF-301) | Responsável de Segurança | KMS real (DEF-302) | FECHADO-RESIDUAL |
| DEF-302 | DEMO-GRADE | packages/cmd/aos/bootstrap.go | **COSTURA DE CUSTÓDIA EXTERNA — ENTREGUE por AOS-215.** `Config.DSARVault`/`Node.DSARVault` passam a ser a **porta** `audit.KeyVault` (não mais o tipo concreto `*audit.InMemoryKeyVault`), injectável no **mesmo molde de precedência** do Event Store/WORM: um vault INJECTADO (key-service/software-KMS de **custódia externa**, as KEK vivem FORA do processo e sobrevivem ao restart) é usado TAL-QUAL; sem injecção cai no `audit.InMemoryKeyVault` de referência (DEMO-GRADE, KEK em memória, não-durável — declarado no banner). A MESMA instância serve o cifrador de conteúdo (AOS-093), o shredder DSAR e o sink de expiração (AOS-213): o `/dsar/erase`/`/dsar/expire` destroem a KEK ONDE ela realmente vive. **FAIL-CLOSED:** um vault injectado que falha propaga o erro pela cifra/shred (aborta a escrita) — NUNCA há fallback silencioso para o in-memory. Provado ao nível do nó (`-race`) por `aos215_kek_custody_test.go` (vault-spy injectado é o que EnsureKey/Key/Delete usam; o erase destrói a KEK NELE e o conteúdo fica irrecuperável; vault que erra ⇒ captura falha). Custódia documentada em `deploy/node/README.md` (§Custódia da KEK). **RESIDUAL HSM *key-never-leaves* — FECHADO por AOS-216:** a porta de **envelope** `audit.KeyWrapper` (`WrapDEK(subjectID, dek) → (wrapped, keyRef)`, `UnwrapDEK(keyRef, wrapped) → (dek, ok)`) faz o embrulho/desembrulho da DEK correr DENTRO do módulo — a **KEK crua NUNCA entra no processo do nó**. `audit.SealContent`/`OpenContent` tomam a via de envelope QUANDO o vault injectado implementa `KeyWrapper` (type assertion), com **fallback** à via KEK-crua de AOS-093/215 quando só implementa `KeyVault` (serialização byte-a-byte idêntica; o formato de envelope é versionado retro-compativelmente por um campo `key_ref` só presente nesse caminho). Impl de referência `audit.InMemoryKeyWrapper` (stdlib AES-256-GCM, in-process) prova o contrato: a KEK nunca é devolvida; `Delete` destrói-a ⇒ `UnwrapDEK` falha (crypto-shred aguenta). Falsificável (`-race`): `aos216_hsm_envelope_test.go` (ao nível do audit — gate que PANICA em `Key()`/`EnsureKey()` ainda cifra/decifra; shred ⇒ `ErrDecrypt`; hash do blob estável) e `packages/cmd/aos/aos216_hsm_envelope_test.go` (ao nível do nó — wrapper injectado; o blob do substrato está em formato de envelope `key_ref`; `Key`/`EnsureKey` a ZERO; `/dsar/erase` torna-o irrecuperável). O **HSM concreto** (AWS KMS, Vault Transit, PKCS#11) permanece INFRA-ORG, não entregue no binário (zero-dep), análogo a AOS-175/DEF-201-212 | AOS-093, AOS-070 | Responsável de Segurança | Porta de envelope `WrapDEK`/`UnwrapDEK` ENTREGUE por AOS-216; só o HSM concreto fica infra-org | FECHADO-RESIDUAL |
| DEF-303 | DOCUMENTAL | tecnica/02_Agent_Runtime_Execucao_Duravel.md | `Result.Payload` do step-ledger: com `durable.WithContentSealer` (composto no nó) é CIFRADO por-titular antes do Event Store; `WithSensitiveResults()` permanece como via de referência opt-in quando não há sealer | AOS-093 (ver A-DEF-301) | Responsável de Segurança | Idem DEF-301 (mesma cifra do substrato) | FECHADO-RESIDUAL |
| DEF-306 | DOCUMENTAL | tecnica/14_Matriz_Conformidade.md | «Eixo declarado» da cifra por titular do substrato aponta EPIC-06/09/10 (ficheiro de outro pipeline — ver pendência P-2) | AOS-093 (ver A-DEF-301) | Responsável de Segurança | Idem DEF-301 | ABERTO |
| DEF-307 | DOCUMENTAL | tecnica/17_Analise_STRIDE.md | Alcance do crypto-shredding: o conteúdo dos runs fica fora, com eixo `EPIC-09/10` | AOS-093 (ver A-DEF-301) | Responsável de Segurança | Idem DEF-301 | ABERTO |
| DEF-401 | DOCUMENTAL | docs/adr/ADR-012-semver-eval-gate.md | **Anti-replay da ratificação.** Freshness+nonce eram portas opcionais desligadas por omissão; `NewProductionRatificationGate` não tinha chamador de produção porque o nó não compunha promotion controller. **FECHADO por AOS-206:** o nó compõe agora `PromotionController` (`packages/cmd/aos/promotion.go`) pela via sancionada `hitl.NewProductionRatificationGate` (freshness+nonce durável FORÇADOS); `TestNodePromotionController_ReplayBlockedThroughNode` prova `ratification_replayed` pelo caminho do nó | AOS-159 (mecanismo); wiring de produção AOS-206 | Responsável de Segurança | Existir um caminho de promoção/auto-modificação composto no nó — **SATISFEITO por AOS-206** | FECHADO-RESIDUAL |
| DEF-402 | DOCUMENTAL | specs/EPIC-14_Integracao_Composition_Root.md | CA de AOS-159 «ligados no promotion controller» estava `[x]` e era falso; corrigido para `[ ]` por AOS-196. **FECHADO por AOS-206:** o CA voltou a `[x]` com chamador de produção real e prova de ápice+negativa (guarda de fonte `TestNode_UsesSanctionedRatificationPathOnly`) | AOS-159; wiring AOS-206 | Responsável de Segurança | Idem DEF-401 — **SATISFEITO por AOS-206** | FECHADO-RESIDUAL |
| DEF-403 | DEFERIDO | packages/cmd/aos/promotion.go | **Eval-gate CONCRETO da política de promoção.** O `PromotionController` (AOS-206) interpõe a via sancionada mas usa a referência fail-closed `otelgenai.FailClosedGate` como pré-condição eval-gate+canary; o gate de eval CONCRETO da política de promoção fica deferido (mesmo subject de DEF-009, agora visível no chamador do nó) | AOS-114, AOS-115 | Arquitecto de Plataforma | Existir eval-gate concreto de política de promoção composto no caminho do nó (idem DEF-009) | MITIGADO |
| DEF-404 | DEFERIDO | packages/cmd/aos/bootstrap.go | Idem DEF-403 no seam `Config.PromotionEval` (default fail-closed `otelgenai.FailClosedGate` quando não injectado) — o eval-gate concreto da política de promoção não é composto no ápice | AOS-114, AOS-115 | Arquitecto de Plataforma | Idem DEF-403 | MITIGADO |
| DEF-405 | DEFERIDO | packages/cmd/aos/bootstrap.go | **Superfície de INVOCAÇÃO da promoção pelo operador.** A ratificação de promoção (AOS-206) é alcançável PELO CAMINHO DO NÓ (`node.Promotion.Promote`, provada em teste), mas o binário não expõe I/O (endpoint HTTP nem subcomando CLI) que a submeta — ao contrário do FourEyesGate (`POST /runs/{id}/approve`) | AOS-096 | Responsável de Segurança | Existir pipeline de promoção/canary (AOS-096) que submeta ratificações ao controller | ABERTO |
| DEF-501 | DOCUMENTAL | docs/adr/ADR-017-supply-chain-node.md | **Assinatura/atestação de imagem.** SBOM é gerado; a atestação fica por assinar e não há registry de imagens assinado | AOS-207 | Arquitecto de Plataforma | Primeiro release distribuído do nó fora do repositório | ABERTO |
| DEF-502 | DOCUMENTAL | docs/adr/ADR-018-fronteira-no-orq-sch.md | Secção «O que muda no distribuído» deferida a EPIC-10 sem nomear ticket na linha | AOS-098, AOS-099, AOS-100 | Arquitecto de Plataforma | Passagem do nó a multi-processo/multi-nó | ABERTO |
| DEF-601 | STUB | packages/kernel/reference-monitor/doc.go | O pacote entrega o RM com STUBS NEUTROS; identidade, política, orçamento, egress e audit reais chegam noutros tickets | AOS-004, AOS-005, AOS-011, AOS-087 | Arquitecto de Plataforma | Substituição dos hooks neutros no ápice de produção | MITIGADO |
| DEF-602 | STUB | packages/kernel/reference-monitor/hooks.go | `IdentityStub`/`PolicyStub`/`BudgetStub`/`EgressStub` e o audit no-op documentam o ponto de injecção sem codificar regra | AOS-004, AOS-005, AOS-011, AOS-087 | Arquitecto de Plataforma | Idem DEF-601 | MITIGADO |
| DEF-603 | STUB | packages/kernel/reference-monitor/production.go | Marcador de contraste: `NewProductionSecure` REJEITA os stubs neutros que `NewProduction` tolerava | AOS-153 | Arquitecto de Plataforma | Remoção de `NewProduction` do contrato público | FECHADO-RESIDUAL |
| DEF-604 | DEMO-GRADE | packages/integration/secured.go | Colaboradores nil caem para defaults demo-grade que são hooks REAIS fail-closed (nunca stubs permissivos); o PDP não carrega bundle e o conjunto `Privileged` é vazio | AOS-181, AOS-183 | Responsável de Segurança | Execução de AOS-181 (bundle PDP) e AOS-183 (conjunto Privileged real) | MITIGADO |
| DEF-605 | DIFERIDO | packages/kernel/reference-monitor/scope_gate.go | A metade SPAN/OTel do critério de escopo efectivo (ADR-002/010) está DIFERIDA: hoje só o canal AUDIT regista a autoridade em vigor. O texto do ficheiro difere para `EPIC-08` sem nomear ticket — o eixo real é AOS-076 | AOS-076 | Arquitecto de Plataforma | Execução de AOS-076 (span `execute_tool` por tool call mediada) | ABERTO |
| DEF-606 | DIFERIDO | packages/kernel/reference-monitor/production.go | Guarda de eficácia do taint (AOS-219): `hasActiveTaintGate` exige conjunto `Privileged` não-vazio e o ápice arranca com um gate wired-mas-inerte, declarando a postura via `Monitor.HasActiveTaintGate` (sem alegar endurecimento que não tem). A costura fail-closed `NewProductionHardenedTaint`/`ErrTaintGateInert` está pronta, mas o conjunto `Privileged` REAL fica diferido a AOS-183 (idem DEF-808/DEF-604) | AOS-157, AOS-183 | Responsável de Segurança | Idem DEF-808 (conjunto `Privileged` real no ápice) | MITIGADO |
| DEF-701 | NUNCA-EM-PRODUCAO | packages/substrate/sandbox/driver.go | O driver de referência NÃO cria jail nem impõe as invariantes de isolamento | AOS-064, AOS-068 | Responsável de Segurança | Catálogo de tools não-vazio a executar código não-confiável | ABERTO |
| DEF-702 | NUNCA-EM-PRODUCAO | packages/substrate/sandbox/driver_fake.go | Driver fake dos testes: corre no host | AOS-064 | Responsável de Segurança | Idem DEF-701 | ABERTO |
| DEF-703 | NUNCA-EM-PRODUCAO | packages/platform/broker/internal/vault/vault.go | Vault em memória do Credential Broker | AOS-070 | Responsável de Segurança | Ambiente com Vault/KMS real disponível | ABERTO |
| DEF-704 | NUNCA-EM-PRODUCAO | packages/platform/broker/vault_client.go | Reexporta o vault em memória como `vault.Memory` | AOS-070 | Responsável de Segurança | Idem DEF-703 | ABERTO |
| DEF-705 | NUNCA-EM-PRODUCAO | packages/platform/model-gateway/internal/adapters/adapter.go | Fonte de segredos por mapa `(provider, região)` para testes | AOS-070, AOS-057 | Responsável de Segurança | Idem DEF-703 | ABERTO |
| DEF-706 | NUNCA-EM-PRODUCAO | packages/platform/model-gateway/internal/credentials/broker.go | `ReferenceBroker` não contém segredos e recusa emitir (`ErrNotWired`); o wiring concreto é de infra | AOS-070 | Responsável de Segurança | Idem DEF-703 | MITIGADO |
| DEF-801 | DEFERIDO | packages/kernel/agent-runtime/activity/doc.go | O loop medeia cada tool call directamente e ainda NÃO despacha via `Dispatcher`; a idempotência pelo step-ledger não cobre o efeito externo real | AOS-022 | Arquitecto de Plataforma | Adopção do dispatcher ledger-backed no loop | ABERTO |
| DEF-802 | DEFERIDO | packages/platform/messaging/doc.go | Adaptadores para NHI real, broker/Vault e ponto real de troca de mensagens ficam para o composition root | AOS-073, AOS-070 | Arquitecto de Plataforma | Existir canal real de troca inter-agente no nó | ABERTO |
| DEF-803 | STUB | packages/control-plane/orchestrator/orchestrator.go | Decomposição goal→DAG é um stub: o grafo tem um nó único. **REAVALIADO a 2026-08-30 por AOS-281 (DoD) e MANTIDO ABERTO.** A reavaliação mudou o que falta, não o estado: até aqui não havia sequer um ESCRITOR LEGÍTIMO do grafo — o `GraphBuilder` não detinha fencing token e, sobre um run existente, construía cego (`ErrLogAhead`). O ADR-023 dá-lhe dono (posse por lease) e via de construção re-hidratada (`orchestrator.NewGraphBuilderFromLog`), pelo que a condição NECESSÁRIA para uma decomposição multi-tarefa real passa a existir. Não é a condição SUFICIENTE: continua a faltar a decomposição em si, que é de AOS-025 e não deste ticket | AOS-025 | Arquitecto de Plataforma | Necessidade de grafos multi-tarefa com detecção de deadlock | ABERTO |
| DEF-804 | DEFERIDO | packages/cmd/aos/service.go | Persistência durável do shutdown e substrato durável tratados noutros tickets; aqui o substrato é o Event Store de referência | AOS-164, AOS-170 | Arquitecto de Plataforma | Idem: já entregues; revisitar se o `NodeService` mudar de substrato | FECHADO-RESIDUAL |
| DEF-805 | DIFERIDO | packages/kernel/agent-runtime/activity/doc.go | «Adopção pelo loop (AOS-013): DIFERIDA» — a mesma dívida de DEF-801 vista do lado do contrato de activity; AOS-157 entregou a porta `ActivityDispatcher`, o default continua a ser o despacho directo | AOS-022, AOS-157 | Arquitecto de Plataforma | Idem DEF-801 (adopção do dispatcher ledger-backed no loop) | ABERTO |
| DEF-806 | DIFERIDO | packages/kernel/agent-runtime/loop.go | Separação de planos (dual-LLM/CaMeL): o primitivo `SeparatePlanes` existe, mas o conteúdo untrusted continua a ser acrescentado INLINE ao tail que monta o prompt do turno seguinte | AOS-069 | Responsável de Segurança | Execução de AOS-069 (CA «separação efectiva entre o plano que planeia e o plano que manipula dados») | ABERTO |
| DEF-807 | DIFERIDO | packages/kernel/agent-runtime/model.go | `AuthorizationTaint` é uma string convencionada em vez de uma autorização estruturalmente infalsificável mintada no runtime | AOS-069 | Responsável de Segurança | Idem DEF-806 | ABERTO |
| DEF-808 | DIFERIDO | packages/kernel/reference-monitor/taint_gate.go | Sem `DefaultHooksWithTaint` e um `PrivilegedAuthorizer` real ligados no ápice, a metade do ADR-005 fica inactiva; o conjunto `Privileged` composto hoje é vazio | AOS-157, AOS-183 | Responsável de Segurança | Idem DEF-604 (conjunto `Privileged` real no ápice) | MITIGADO |
| DEF-809 | DIFERIDO | packages/kernel/reference-monitor/scope_gate.go | Wiring de produção do par escopo+taint no ápice, «a par de AOS-021/037/043» — as portas RT/RM foram entregues por AOS-157; falta o autorizador privilegiado real | AOS-157, AOS-183 | Responsável de Segurança | Idem DEF-808 | MITIGADO |
| DEF-811 | DEFERIDO | packages/cmd/aos/posture_banner.go | **MEM nao esta composto como servico — so como uma escrita.** O no constroi `memory.NewService` sobre o mesmo Event Store (`bootstrap.go`), mas o unico caminho de producao que o usa e a escrita episodica da ingestao (`packages/integration/ingestion.go`). Nenhum caminho de producao invoca recall/query/compactacao/curadoria, `Goal.MemoryContext` nao e preenchido por ninguem (declarado em `packages/kernel/agent-runtime/loop.go`), e `memory/episodic`, `semantic`, `procedural`, `compression` e `migrations` tem ZERO importadores externos nao-teste. As tres classes do `_BRIEF` §2 existem como biblioteca testada, com gate proprio (`scripts/ci/memory.sh`), e nao como comportamento do no. Ao contrario do ORQ/SCH, NAO ha ADR que o declare deliberado — ver N-DEF-811 | POR ATRIBUIR | Arquitecto de Plataforma | Decisao do dono sobre a forma: compor o MEM no no, ou emitir ADR que o declare fora do grafo de build no molde do ADR-023 | ABERTO |
| DEF-812 | DEFERIDO | packages/cmd/aos/posture_banner.go | **REG: catalogo, host MCP e TOFU nao sao construidos pelo no.** `bootstrap.go` compoe `emptyCatalog{}` e o `referenceRevalidator()` com trust store VAZIO. `registry.New` tem um unico chamador nao-teste (`promotion/pipeline.go`), que por sua vez nao tem chamador nenhum; `mcp.NewHost` e `tofu.NewMonitor` tem zero. O que CORRE e o congelamento por run (`toolset`) e a revalidacao por chamada, ligados na cadeia do Reference Monitor (`packages/integration/secured.go`) — o catalogo vazio e default-deny, pelo que nenhuma tool executa. Ao contrario do ORQ/SCH, NAO ha ADR que o declare deliberado — ver N-DEF-812 | POR ATRIBUIR | Arquitecto de Plataforma | Decisao do dono sobre a forma: compor o REG no no, ou emitir ADR que o declare fora do grafo de build no molde do ADR-023 | ABERTO |
| DEF-813 | DOCUMENTAL | analises/11_Auditoria_MEM_REG_GW_BRK_Adversarial.md | **A confirmacao do crypto-shred e OPCIONAL, e a omissao e fail-open para uma custodia futura.** `dsar.ShredConfirmer` so e consultada se `WithShredConfirmer` receber algo nao-nil, e `confirmadorDeShredDe` devolve nil para uma custodia que nao implemente a porta interna. Hoje isso e CORRECTO e esta testado (AOS-322): as duas custodias existentes fazem a escolha certa — o `InMemoryKeyVault` nao a implementa porque o seu `Delete` nao pode falhar, o `vaultKeyVault` implementa-a. O risco e a TERCEIRA: um KMS de terceiros que POSSA falhar a destruir e nao implemente a porta faz a cadeia selar `dsar.key_destroyed` sobre uma irrecuperabilidade que ninguem verificou — o defeito que a porta foi criada para fechar, reaberto pela via da omissao. Nada obriga essa escolha a ser consciente. O banner de arranque passou a declarar qual dos dois casos esta em vigor (AOS-322) | POR ATRIBUIR | Responsavel de Seguranca | Composicao de uma custodia de KEK que nao seja o vault de referencia nem o Vault Transit — ai a escolha deixa de ser teorica e tem de ser imposta, nao declarada | ABERTO |
| DEF-901 | NUNCA-EM-PRODUCAO | packages/substrate/otel-genai/idgen.go | `SequentialIDGenerator` produz ids deterministas para testes de topologia de árvore | AOS-076 | Arquitecto de Plataforma | Uso do gerador determinista fora de testes | FECHADO-RESIDUAL |
| DEF-902 | NUNCA-EM-PRODUCAO | packages/testkit/env/vault.go | Vault efémero por `Env` do testkit | AOS-109 | Arquitecto de Plataforma | Importação do testkit por código de produção | FECHADO-RESIDUAL |
| DEF-903 | DEFERIDO | packages/cmd/aos/bootstrap.go | **CON-02 — superfície de administração de legal hold e expiração.** ENTREGUE por AOS-213 (o marcador DEFERIDA em bootstrap.go passa a CONTRASTE histórico): o `audit.LegalHold` (`Node.DSARHolds`) ganha rotas `POST /dsar/hold`//`/dsar/release` e o `audit.ExpirationJob` (AOS-092) é composto no nó (`Node.ExpirationJob`, conduzido por `POST /dsar/expire`) — deixa de ter 0 chamadores de produção. A expiração materializa por crypto-shred da KEK POR-TITULAR (AOS-093, apagamento real, provado ao nível do nó: `OpenContent`→`ErrDecrypt`+hash-chain valida) e respeita o legal hold. Decisão do dono Opção C (2026-07-29, `DOSSIE-CON-02-legal-hold.md`) cumprida (o gatilho — apagamento real — deu-se com AOS-093). RESIDUAL nomeado (eixo AOS-093/envelope): a granularidade é POR-TITULAR (a KEK embrulha todas as DEKs do titular), não por-registo | AOS-213, AOS-093 | Dono do produto | FECHADO por AOS-213; residual de granularidade por-titular vs por-registo exigiria custódia de chave por-registo ou tombstones no ES (re-arquitectura do envelope AOS-093), não previsto | FECHADO-RESIDUAL |
| DEF-904 | DOCUMENTAL | analises/09_Auditoria_RT_RM_Adversarial.md | **Metade do wiring de AOS-019 (`liveness/`) por ligar.** O `liveness/doc.go:56` exige DUAS coisas ao consumidor para que a garantia «gate excedido ⇒ killed» valha: `(a)` construir o gate com `NewWaitingGateFrom`, derivando-o do MESMO TTL e do MESMO relógio da `state.Machine` — o que elimina o drift por construção — E `(b)` chamar `CheckDeadlines` periodicamente. `(b)` está ENTREGUE desde AOS-252 (`packages/cmd/aos/deadline_sweeper.go:94`, arrancado em `service.go:472`). `(a)` não: `NewWaitingGateFrom` não tem chamador de produção, pelo que o `WaitingGate` do classificador de zumbis não é derivado da Machine e o pacote `liveness` fica com um só consumidor real (`CountsAsActiveWork`, em `breaker/breaker.go:213`). Risco contido — o kill fail-closed de `waiting_on_human` é da Machine e corre; o que falta é o veredicto do classificador partilhar a fronteira com ele. Auditoria §3.3 | AOS-019 | Arquitecto de Plataforma | Ligar `NewWaitingGateFrom` no wiring do nó, a par do varredor de deadlines já composto | MITIGADO |
| DEF-905 | DOCUMENTAL | analises/09_Auditoria_RT_RM_Adversarial.md | **SAROC-04 sem enforcement no PEP.** O `RiskGate` implementa o tiering safe/gray/danger, a preview obrigatória e o timeout fail-closed não-desactivável para irreversíveis (`packages/kernel/reference-monitor/risk_gate.go:323-334`), mas NÃO é montado no ápice: `packages/integration/secured.go:371-374` compõe `NewRiskClassifier(nil)`, que ANOTA a classe e devolve `HookAllow` incondicional. A omissão é deliberada e está justificada no próprio ficheiro — sem um `risk.Gate` composto o hook negaria tudo o que não fosse `safe` e pararia o nó. A cobertura residual é REAL e vem por outra via: a `RiskClass` alimenta o overlay de autonomia no PDP (`control-plane/pdp/autonomy.go`), que devolve `Escalate` quando o modo exige gate humano — logo o gate humano de ADR-013 dispara, com `DeniedBy == "policy"`. O que fica por impor é o específico do hook: uma acção **danger com egress e sem destino concreto** deveria ser negada fail-closed, e nenhuma decisão do nó pode ter `DeniedBy == "risk"`. Auditoria §3.3 | AOS-074 | Responsável de Segurança | Composição de um `risk.Gate` real no ápice (canal de confirmação + lote gray), com a escada de tiers declarada pelo nó | MITIGADO |
| DEF-906 | DOCUMENTAL | analises/09_Auditoria_RT_RM_Adversarial.md | **Sem backstop de wall-clock para `paused` e `waiting_on_tool`.** MEDIDO: com `humanTTL` e `wallClock` ligados e o relógio injectado avançado 87 600 h (dez anos), `state.Machine.CheckDeadlines` não transita nenhum dos dois estados — o `switch` de `state/machine.go:550-570` só tem ramo para `waiting_on_human` e `running`, ambos verificados a disparar como controlo. A segunda via também não cobre: `tecnica/08_Observabilidade_Evals.md:144` designa o disjuntor de EPIC-08 como a rede de segurança, com um sinal wall-clock ABSOLUTO, mas a guarda de entrada de `breaker/breaker.go:213` (`liveness.CountsAsActiveWork`, que só admite `running`) devolve cedo — cablado com 1 h contra um limiar de 1 ms dá `Trip=false` e zero alertas nos dois estados, e `Trip=true` em `running`. O sinal absoluto é recolhido e nunca chega a ser avaliado onde a spec o exige. RESSALVA DE ÂMBITO: isto mede o RT; não foi verificado se o escalonador ou o `runlifecycle` impõem um tecto acima. Auditoria §3.2 | AOS-080, AOS-017 | Arquitecto de Plataforma | Ou o disjuntor avalia o sinal absoluto fora de `running` (fechando o que `tecnica/08` §6 pede), ou a spec é emendada para declarar que estes dois estados não têm backstop | ABERTO |
| DEF-907 | DOCUMENTAL | analises/09_Auditoria_RT_RM_Adversarial.md | **Orçamento por árvore (ADR-008) sem enforcement entre processos.** `control-plane/budget/budget.go:33-41` decide a reserva num contador em memória sob mutex do processo, sem CAS e sem `WithExpectedSeq`; o emitter por omissão é `nopEmitter` e `budget.Rebuild` (`budget/events.go:118`) não tem chamador de produção, pelo que um restart repõe os contadores. N réplicas a hospedar runs da mesma árvore têm N raízes independentes. A raiz `math.MaxInt64` de `packages/integration/budget.go:136` NÃO é o defeito e está comentada no sítio: o tecto da v1 é POR-RUN, e uma raiz finita faria o run B ser negado porque o run A gastou. Contraste que mostra o que falta: `control-plane/scheduler/admission.go` faz o token-bucket de provider com CAS durável — é o único admission control com arbitragem entre processos. Auditoria §3.3 | AOS-027 | Arquitecto de Plataforma | Enforcement do orçamento por árvore com arbitragem durável (CAS sobre o log), no molde do token-bucket de provider | ABERTO |
| DEF-908 | DOCUMENTAL | analises/10_Auditoria_ORQ_SCH_PDP_Adversarial.md | **Despromoção automática por anomalia (AOS-090) sem chamador.** `autonomy.NewController` — promoção só por fiabilidade sustentada e **despromoção imediata em anomalia** (override-rate spike, trip do disjuntor, drift) — tem ZERO chamadores em todo o repositório; o nó compõe apenas o `LevelRegistry`. É a metade de SEGURANÇA da autonomia: quando ela é ligada, é ligada sem ela, e um par promovido a L5 fica a L5 até alguém reparar. Agrava-se com AOS-305, que permite L0→L5 num salto (com duas assinaturas). Medido em `analises/10` §3.3 | AOS-090 | Arquitecto de Plataforma | Compor o `autonomy.Controller` no nó, ligando `Evaluate` E `OnAnomaly` — ligar só o primeiro reproduz o «preso a L5» que este deferimento descreve | ABERTO |
| DEF-909 | DOCUMENTAL | analises/10_Auditoria_ORQ_SCH_PDP_Adversarial.md | **Soberania por board inerte no caminho de EFEITO (AC#2 de AOS-094).** `pdp.WithBoardRegions` não tem chamador de produção, pelo que `applySovereignty` retorna imediato, a obrigação `region` nunca é emitida e o `enforceRegion` do Reference Monitor é inalcançável. Os outros quatro CA de AOS-094 ESTÃO cobertos por outra camada composta e assinada (a allowlist `(board, modelo, região)` do model-gateway, AOS-058) e o read-path por AOS-182/205; o que fica inerte é a cadeia PDP-emite→PEP-impõe para tool calls. Residual adjacente: `Resource.Region` é um campo de metadados declarado pelo operador, sem atestação | AOS-094 | Arquitecto de Plataforma | Compor `WithBoardRegions` no nó E dar ao `Resource.Region` uma fonte de verdade — sem a segunda, a primeira compara a região autorizada do board contra um campo auto-declarado | ABERTO |
| DEF-910 | DOCUMENTAL | analises/10_Auditoria_ORQ_SCH_PDP_Adversarial.md | **O lock do dispatcher do SCH é mantido através do CAS durável da admissão.** `priority.go:Dispatch` mantém `d.mu` durante o laço de candidatos, que chama `Admit` — e este faz `Read` do stream inteiro mais `Append` com `WithExpectedSeq`, em laço de retry. Medido sobre o substrato durável real: `Submit` bloqueado 25,4 ms a N=30 e 83,7 ms a N=100, LINEAR em N. O sinal está invertido — quanto mais saturado, menos trabalho novo consegue ENTRAR. LATENTE: o módulo não está no grafo de build de binário nenhum (ADR-018/023) | AOS-032 | Arquitecto de Plataforma | Tirar o `Admit` da secção crítica do `Dispatch` (reservar fora do lock, ou dividir o lock entre a fila e a decisão) antes de o escalonador ser composto em algum processo | ABERTO |
| DEF-911 | DOCUMENTAL | analises/10_Auditoria_ORQ_SCH_PDP_Adversarial.md | **Cada admissão relê o stream do bucket desde a seq 1.** `admission.go` faz `log.Read(ctx, bucketID, 1)` DENTRO do laço de CAS, sem `fromSeq` avançado, snapshot ou compactação: O(N) por decisão e O(N²) acumulado, no stream mais quente do sistema (partilhado por todos os tenants de um `provider:model:region`). Medido: 99,5 eventos lidos por admissão nas primeiras 200 e 699,5 nas 601-800. A `Window` limita quais as reservas que CONTAM, não quais os eventos que são LIDOS. LATENTE pela mesma razão que DEF-910 | AOS-027 | Arquitecto de Plataforma | Compactação ou snapshot por bucket, ou uma `fromSeq` derivada da fronteira da janela | ABERTO |


### 3.1 Contagens declaradas por par (a verificação 1b lê-as)

A cobertura do §3 tem a chave `(ficheiro, marcador)`. Sem mais nada, um deferimento **novo**
num ficheiro **já registado** passava em silêncio — e esse é o caminho mais comum de
acumulação de dívida, não o ficheiro novo. Esta tabela declara **quantas** ocorrências de cada
par existem. O gate compara-a com a árvore e falha nas duas direcções: **a mais** é dívida
nova por registar, **a menos** é contagem por actualizar. Não é uma escotilha — é a diferença
entre exibir um número e o número ser verificado.

*(Os totais não são repetidos aqui em prosa de propósito: um número escrito à mão ao lado de
um número verificado por máquina diverge, e foi assim que a versão anterior deste registo
escreveu «17 linhas POR ATRIBUIR» quando o gate imprimia 19. O gate imprime os dois totais em
cada execução.)*

<!-- CONTAGENS:INICIO -->

| Âncora | Marcador | Ocorrências |
|---|---|---|
| packages/cmd/aos-demo/main.go | DEMO-GRADE | 6 |
| packages/cmd/aos-demo/main.go | STUB | 3 |
| packages/cmd/aos/api.go | DEFERIDO | 3 |
| packages/cmd/aos/bootstrap.go | DEFERIDO | 8 |
| packages/cmd/aos/bootstrap.go | DEMO-GRADE | 15 |
| packages/cmd/aos/bootstrap.go | STUB | 1 |
| packages/cmd/aos/broker_vault_env.go | DEFERIDO | 1 |
| packages/cmd/aos/dsar.go | DEFERIDO | 1 |
| packages/cmd/aos/dsar.go | DEMO-GRADE | 2 |
| packages/cmd/aos/exhaustion_decision.go | DEFERIDO | 1 |
| packages/cmd/aos/main.go | DEFERIDO | 2 |
| packages/cmd/aos/main.go | DEMO-GRADE | 8 |
| packages/cmd/aos/model_audit_env.go | DEFERIDO | 2 |
| packages/cmd/aos/model_pricing_env.go | DEFERIDO | 1 |
| packages/cmd/aos/modelgatewaywiring.go | DEFERIDO | 1 |
| packages/cmd/aos/otlpexporter.go | DIFERIDO | 2 |
| packages/cmd/aos/posture_banner.go | DEFERIDO | 4 |
| packages/cmd/aos/promotion.go | DEFERIDO | 1 |
| packages/cmd/aos/promotion_api.go | DEFERIDO | 1 |
| packages/cmd/aos/saga_compensation.go | DEFERIDO | 1 |
| packages/cmd/aos/service.go | DEFERIDO | 1 |
| packages/cmd/aos/sovereign_authority.go | DEFERIDO | 1 |
| packages/cmd/aos/sovereign_authority.go | DEMO-GRADE | 1 |
| packages/cmd/aos/sovereignty.go | CONDICIONAL | 1 |
| packages/cmd/aos/sovereignty.go | DEFERIDO | 1 |
| packages/cmd/aos/sovereignty.go | DEMO-GRADE | 7 |
| packages/control-plane/orchestrator/orchestrator.go | STUB | 1 |
| packages/integration/device_attestation.go | STUB | 1 |
| packages/integration/foureyes.go | STUB | 2 |
| packages/integration/issuer_authority.go | DEFERIDO | 1 |
| packages/integration/issuer_authority.go | DEMO-GRADE | 7 |
| packages/integration/issuer_authority.go | NUNCA-EM-PRODUCAO | 2 |
| packages/integration/oidc/oidc.go | DEMO-GRADE | 1 |
| packages/integration/oidc_directory.go | DEMO-GRADE | 1 |
| packages/integration/secured.go | DEMO-GRADE | 3 |
| packages/integration/steer_authenticator.go | DEMO-GRADE | 1 |
| packages/kernel/agent-runtime/activity/doc.go | DEFERIDO | 1 |
| packages/kernel/agent-runtime/activity/doc.go | DIFERIDO | 1 |
| packages/kernel/agent-runtime/control/steer_channel.go | DEMO-GRADE | 1 |
| packages/kernel/agent-runtime/loop.go | DIFERIDO | 2 |
| packages/kernel/agent-runtime/model.go | DIFERIDO | 2 |
| packages/kernel/agent-runtime/replay/payload_store.go | DIFERIDO | 1 |
| packages/kernel/reference-monitor/doc.go | STUB | 1 |
| packages/kernel/reference-monitor/hooks.go | STUB | 1 |
| packages/kernel/reference-monitor/production.go | STUB | 1 |
| packages/kernel/reference-monitor/production.go | DIFERIDO | 2 |
| packages/kernel/reference-monitor/scope_gate.go | DIFERIDO | 2 |
| packages/kernel/reference-monitor/taint_gate.go | DIFERIDO | 2 |
| packages/platform/broker/inprocess.go | DEFERIDO | 2 |
| packages/platform/broker/internal/vault/kvv2.go | DEFERIDO | 1 |
| packages/platform/broker/internal/vault/vault.go | NUNCA-EM-PRODUCAO | 1 |
| packages/platform/broker/vault_client.go | NUNCA-EM-PRODUCAO | 1 |
| packages/platform/eval/doc.go | DIFERIDO | 1 |
| packages/platform/eval/runner.go | DIFERIDO | 1 |
| packages/platform/messaging/doc.go | DEFERIDO | 2 |
| packages/platform/model-gateway/production_routing.go | DEFERIDO | 2 |
| packages/platform/model-gateway/routing/routingstage/classifier.go | DEFERIDO | 1 |
| packages/platform/model-gateway/internal/adapters/adapter.go | NUNCA-EM-PRODUCAO | 1 |
| packages/platform/model-gateway/internal/credentials/broker.go | NUNCA-EM-PRODUCAO | 1 |
| packages/substrate/otel-genai/doc.go | DIFERIDO | 1 |
| packages/substrate/otel-genai/evaluation.go | DIFERIDO | 4 |
| packages/substrate/otel-genai/exporter.go | DIFERIDO | 1 |
| packages/substrate/otel-genai/idgen.go | NUNCA-EM-PRODUCAO | 1 |
| packages/substrate/otel-genai/otlp.go | DIFERIDO | 1 |
| packages/substrate/otel-genai/semconv.go | DIFERIDO | 1 |
| packages/substrate/otel-genai/slo.go | DEFERIDO | 1 |
| packages/substrate/otel-genai/spantracer.go | DIFERIDO | 1 |
| packages/substrate/otel-genai/wide_event.go | DIFERIDO | 2 |
| packages/substrate/sandbox/driver.go | NUNCA-EM-PRODUCAO | 1 |
| packages/substrate/sandbox/driver_fake.go | NUNCA-EM-PRODUCAO | 1 |
| packages/testkit/env/vault.go | NUNCA-EM-PRODUCAO | 1 |

<!-- CONTAGENS:FIM -->

---

## 4. O que este registo mede (e o que não mede)

**Mede:** que cada deferimento declarado no código tem um destino **executável** — um ticket
que existe no backlog, ou um ticket **descrito** e assumidamente por criar.

**Não mede** — e a distinção é a diferença entre um gate honesto e um selo de qualidade:

- **não julga o mérito do deferimento.** Uma linha `ABERTO` com eixo válido continua a ser
  dívida; o registo torna-a contável, não benigna;
- **não verifica que o ticket citado cobre aquele deferimento.** Verifica que **existe**. Um
  `AOS-NNN` existente mas irrelevante passa. O que estava errado em DEF-01 era citar um
  destino **sem ticket nenhum** — é isso que fica fechado. A adequação é revisão humana, e é
  para isso que existe a coluna `Gatilho`;
- **não ancora por linha — ancora por par + contagem.** A chave do §3 é
  `(ficheiro, marcador)`; a chave por LINHA ficava vermelha a cada edição de comentário e
  acabaria desligada. Isso deixava um buraco real: um sétimo `DEMO-GRADE` acrescentado a um
  ficheiro que já declara `DEMO-GRADE` passava em silêncio — e é esse, não o ficheiro novo, o
  caminho comum de acumulação de dívida. O buraco está **fechado pela verificação 1b**: o §3.1
  declara **quantas** ocorrências cada par tem, e o gate falha nas duas direcções. O que
  continua fora é a *identidade da linha*: trocar uma ocorrência por outra dentro do mesmo
  ficheiro, sem mexer no total, não é distinguível;
- **não reconhece `diferid*` em minúsculas como marcador.** É a palavra portuguesa corrente e
  ocorre 23× em prosa técnica sem dívida nenhuma (§2.2). Continua a ser apanhada pela
  verificação 6, onde a exigência de um `EPIC-NN` na mesma linha elimina o ruído. Este limite
  está declarado porque **não** o estar tornava o gate mais forte do que é — foi exactamente
  assim que a grafia `DIFERIDO` (maiúsculas, 25 ocorrências em 16 ficheiros) escapou à
  primeira versão deste registo;
- **não cobre `_test.go` nem `testdata/`.** Um marcador em teste não é uma fronteira de
  produção. `packages/testkit/**` é a excepção deliberada: não é `_test.go`, é código
  importável, e por isso está registado (DEF-902).

---

## 5. Verificação (comando canónico)

```bash
python3 scripts/ci/deferrals.py      # ou: bash scripts/ci/deferrals.sh
make ci GATES=deferrals              # equivalente pelo runner
```

O gate é **bloqueante** e está nos **três** sítios que AOS-190 exige (sem os três, não
bloqueia nada): `ALL_GATES` em `scripts/ci/run.sh`, o job `deferrals` em
`.github/workflows/ci.yml`, e o `needs:` do agregador `gates`. Fail-closed: sem `|| true`,
sem `continue-on-error`, e sem caminho que devolva 0 quando não consegue correr.

**As sete verificações:**

1. **Cobertura** — cada par (ficheiro `.go` de produção sob `packages/`, marcador) tem ≥1
   linha no §3.
1b. **Contagem declarada** — cada par tem uma contagem no §3.1 e ela **bate** com a árvore.
   Observada > declarada é dívida nova por registar; observada < declarada é contagem por
   actualizar; declarada sem par no código é linha de contagem por remover. É esta verificação
   que fecha o buraco de granularidade descrito no §4.
2. **Eixo verificável** — a célula `Eixo` cita ≥1 `AOS-NNN` **existente no backlog** ou traz
   `POR ATRIBUIR`. Citar só um `EPIC-NN` falha. *(fecha DEF-01)*
3. **`POR ATRIBUIR` com nota** — toda a linha sem eixo tem `### N-DEF-NNN` no §6.
4. **Vocabulário e campos** — `Marcador`/`Estado` do §2.2; `Dono`/`Gatilho`/`Deferimento`
   não vazios; IDs únicos.
5. **Só encolhe** — linha cuja âncora não existe é **ÓRFÃ**; linha de marcador de código cuja
   âncora já não contém o marcador é **OBSOLETA**. Uma linha `DOCUMENTAL` **ancorada num
   `.go`** está sujeita à mesma regra: se o ficheiro deixar de declarar deferimento nenhum
   (nem marcador, nem a palavra), a linha é OBSOLETA. Sem esta última cláusula, as linhas
   `DOCUMENTAL` sobre código — DEF-304 e DEF-305 são-no — tornavam-se permanentes por inércia
   no dia em que a pendência P-2 corrigisse o texto. Todas fazem o gate falhar; é a lição das
   baselines de AOS-198.
6. **Anti-eixo-fantasma** — nenhuma linha de `packages/**/*.go` (não-teste), `docs/adr/**.md`
   ou `tecnica/**.md` pode deferir (`DEFERID*`/`DIFERID*`/`deferid*`/`diferid*`/`deferimento`/
   `dívida`, **as duas grafias**) para um `EPIC-NN` **sem nomear um `AOS-NNN` na mesma
   linha**, salvo se constar de `scripts/ci/baseline/deferrals.txt` **com `owner=`**. É a
   forma textual exacta do defeito DEF-01. `specs/` está fora: no backlog, escopar trabalho a
   um epic é a operação normal do documento.

### 5.1 Prova negativa — o gate tem de conseguir ficar vermelho

Um gate que nunca fica vermelho é um selo, não um gate. As injecções seguintes foram corridas
sobre uma **cópia** do repositório (via `AOS_DEFERRALS_ROOT`, a variável que existe só para
isto); todas produziram `exit 1` e a mensagem indicada, e a cópia voltou a `exit 0` depois de
cada reversão. N1–N7 em 2026-07-26; N8–N11 em 2026-07-27, com o gate já endurecido.

| # | Injecção | Saída |
|---|---|---|
| N1 | Eixo de `DEF-801` trocado de `AOS-022` para `EPIC-13` — **o defeito DEF-01 reproduzido** | `DEF-801: eixo 'EPIC-13' não cita nenhum AOS-NNN nem declara 'POR ATRIBUIR' (citar só um EPIC é exactamente o defeito DEF-01)` |
| N2 | Comentário `// DEMO-GRADE: …` acrescentado a `substrate/bus/bus.go` (ficheiro sem linha no registo) | `1 marcador(es) de código sem entrada no registo: packages/substrate/bus/bus.go [DEMO-GRADE]` |
| N3 | Eixo de `DEF-803` trocado para `AOS-777` (número inexistente) | `DEF-803: eixo cita ticket inexistente no backlog: AOS-777` |
| N4 | «NUNCA usar em produção» removido de `otel-genai/idgen.go` | `DEF-901: OBSOLETA — … já não contém o marcador NUNCA-EM-PRODUCAO; remover a linha` |
| N5 | `// A cifra fica DEFERIDA para EPIC-11.` acrescentado a `substrate/bus/metrics.go` | `1 deferimento(s) para EPIC sem ticket na linha (fora da baseline)` |
| N6 | `owner=` removido de uma entrada da baseline | `1 entrada(s) da baseline sem 'owner='` |
| N7 | Cabeçalhos do §6 sem a lista `— cobre DEF-…` (estado real antes da correcção) | `15 eixo(s) inválido(s)`: cada linha `POR ATRIBUIR` sem nota nomeada |
| **N8** | `// … adapter de bus DIFERIDO …` acrescentado a `otel-genai/otlp.go` — ficheiro **já registado**, marcador **já registado** | `packages/substrate/otel-genai/otlp.go [DIFERIDO]: 2 ocorrência(s) > 1 declarada(s) no §3.1 — DÍVIDA NOVA nas linhas [15, 147]` |
| **N9** | `// DEFERIDO: sonda de auditoria — nova dívida sem eixo nenhum.` acrescentado a `cmd/aos/api.go` — **a injecção exacta com que a revisão demonstrou que o gate anterior ficava VERDE** | `packages/cmd/aos/api.go [DEFERIDO]: 3 ocorrência(s) > 2 declarada(s) no §3.1 — DÍVIDA NOVA nas linhas [135, 297, 1049]` |
| **N10** | `// A cifra do bus fica DIFERIDA para EPIC-12.` acrescentado a `substrate/bus/bus.go` — **a grafia com I**, que a primeira versão do gate não via | três erros de uma vez: `1 marcador(es) de código sem entrada no registo`, `1 divergência(s) de contagem`, `1 deferimento(s) para EPIC sem ticket na linha` |
| **N11** | Todo o vocabulário de deferimento apagado de `replay/nondeterminism_capture.go` (âncora **DOCUMENTAL sobre `.go`**, o caso que apodrecia em silêncio) | `DEF-305: OBSOLETA — … já não declara deferimento nenhum (linha DOCUMENTAL sobre código); remover a linha` |

**O que as injecções NÃO provam:** que um eixo *semanticamente errado* mas sintacticamente
válido fica vermelho. Não fica — ver §4. N1 é a prova de que a forma exacta do DEF-01 (apontar
para um epic) fica vermelha; a forma «apontar para o ticket errado» continua a depender de
revisão humana, e é isso que a coluna `Gatilho` serve.

**Limite conhecido da verificação 6 (medido, não presumido):** é por LINHA, e **isenta
qualquer linha que traga um `AOS-NNN`, seja por que razão for**. Duas consequências que
convém não disfarçar:

- um deferimento cuja frase cite o epic numa linha e um ticket noutra escapa;
- uma linha que cite um `AOS-NNN` a propósito de outra coisa escapa — foi o caso do ADR-012,
  que citava `AOS-096` na mesma frase em que deferia para a EPIC-13, e é hoje o caso das
  próprias correcções de AOS-196 a `tecnica/02` e aos ADR, que passam por citarem `AOS-196`.

Exigir a ausência total de `AOS-NNN` foi o único critério sem adivinhação: sem ele o
varrimento produzia dezenas de falsos positivos sobre referências legítimas a epics. As duas
ocorrências que escaparam por esta razão (ADR-012, ADR-017) foram corrigidas **à mão** por
AOS-196 e estão cobertas por `DEF-401`/`DEF-501`. Fica registado aqui em vez de ser fingido
como apanhado. A cobertura desta verificação é, portanto, **menor do que o nome sugere**: ela
apanha a forma canónica do DEF-01 (deferir para um epic e mais nada na linha), não toda a
forma possível de o cometer.

---

## 6. Notas dos deferimentos sem eixo (§2.2 — obrigatórias, o gate valida-as)

Cada nota `N-DEF-NNN` descreve o **ticket que falta**. Não o cria: `specs/EPIC-*.md` é do dono
do backlog, e citar um `AOS-NNN` inexistente partiria o gate `ref-lint`. **O gate exige uma
nota `N-DEF-NNN` por cada linha `POR ATRIBUIR`** e lê a lista de cobertura no cabeçalho.

Uma secção `A-DEF-NNN` é outra coisa: uma **arbitragem** — um eixo que foi discutido, mudou, e
cuja decisão fica escrita com o que a falsificaria. Não é lida pelo gate (as linhas que ela
cobre já têm ticket); está aqui para a decisão não ter de ser redescoberta.

### N-DEF-012 — cobre DEF-012

Introduzido por **AOS-209** (terminação TLS do nó), que decidiu **entregar** a terminação TLS do
ingresso (servidor) e **deferir** a autenticação MÚTUA de transporte. **O mecanismo do nó foi
entregue OPT-IN pelo próprio eixo DEF-012** (sem `AOS-NNN` novo — o eixo é o registo). A decisão de
o fazer defesa-em-profundidade está justificada em `specs/EPIC-18` §8-bis: o plano de controlo já é
autenticado na camada de **aplicação** por assinatura ed25519 no corpo (non-signing, AOS-160),
independente do transporte — o mTLS é uma **segunda** barreira, não a primeira.

**O que foi entregue (código do nó, fail-closed, ADITIVO):**

- O servidor da API exige **certificado de cliente** verificado (`tls.VerifyClientCertIfGiven` +
  `ClientCAs` do bundle montado em `AOS_CONTROL_MTLS_CA_PATH`) nas rotas do plano de controlo
  (as 11 rotas `planoControlo` da tabela de `packages/cmd/aos/planos.go`), **escopado** (não impõe cert às rotas de dados/sondas), **ADITIVO**
  à assinatura ed25519 (um cert válido com assinatura ausente/má continua recusado — provado em
  `control_mtls_test.go`). Exige terminação TLS no nó (`ErrControlMTLSNeedsNodeTLS`).
- O exporter OTLP apresenta **certificado de cliente** (mTLS, `AOS_OTLP_CLIENT_CERT_PATH`+`KEY`) OU um
  **bearer token** (`AOS_OTLP_BEARER_TOKEN_PATH`) perante o colector, mantendo o **fail-open** de
  AOS-173 intacto (provado em `otlp_auth_test.go`). O bearer é um segredo — nunca logado.
- Todo o material privado por **ficheiro montado** (nunca por variável de ambiente, no padrão de
  `AOS_TLS_KEY_PATH`/`AOS_ISSUER_KEY_PATH`); a CA de cliente é material público.

**O que fica deferido (POR ATRIBUIR — é infra-org, não código do nó):** a **provisão** da PKI de
cliente (emissão/rotação de certificados aos operadores) e a configuração de autenticação do lado do
**colector**. Não tem `AOS-NNN` porque é provisionamento de infra da organização, no mesmo padrão de
N-DEF-201; o nó já não é o gargalo. Estado do registo passou de `ABERTO` a `MITIGADO`.

### N-DEF-201 — cobre DEF-201, DEF-203…DEF-211 — **TICKET CRIADO: AOS-205** (`specs/EPIC-18` §8-bis)

É **um só** ticket em falta, replicado por dez linhas em seis ficheiros. DEF-210 e DEF-211 têm
eixo parcial (AOS-203 cobre a documentação e o kill-switch das variáveis de ambiente); a parte
do **provisionamento** é esta.

**Ticket necessário — «Provisionamento do IdP de soberania: registo board→região e credencial
forte do leitor/operador de governação».** Epic sugerido: EPIC-09 (Governação/Conformidade),
com dependência de AOS-094 e AOS-174.

- O registo `board→região` deixa de ser lido de `AOS_BOARD_REGIONS` e passa a vir de uma fonte
  de autoridade da organização, com rotação e auditoria de alterações.
- Os headers `X-Aos-Reader`/`X-Aos-Board` deixam de ser auto-declarados: o leitor de governação
  e o operador DSAR apresentam credencial forte (OIDC/mTLS) verificada contra esse IdP.
- Em `AOS_MODE=production`, arrancar sem essa fonte **recusa** (hoje já recusa sem soberania
  configurada, mas aceita a configuração self-hosted como se fosse autoridade).

**Porque não tem ticket hoje:** o eixo estava escrito como «EPIC-09/10». O EPIC-09 entrega a
*regra* de soberania (AOS-094) e o EPIC-10 entrega topologia/DR (AOS-098…108); nenhum dos onze
tickets entrega o **provisionamento de identidade regional**. A Carta §4.2 marca D7 como
CONDICIONAL a esse provisionamento — a decisão está registada, o ticket é que nunca existiu.

### N-DEF-220 — cobre DEF-220

**Ticket necessário — «Tecto de orçamento mutável em runtime: evento próprio + reconstrução no
`Rebuild` + ADR».** Epic sugerido: EPIC-20 (ou o epic de `budget` que o dono do backlog
escolher), com dependência de AOS-008 e AOS-263.

Não é um número que falte pôr num campo. Hoje o tecto por-run é **configuração declarativa** e
`packages/control-plane/budget/events.go` diz explicitamente que os limites ficam **fora** do log
de eventos, «por design não reconstruíveis por `Rebuild`». Um `extend` que mudasse o tecto sem
tocar nisso produziria um nó cujo estado de orçamento **não se reconstrói** — o tecto vivo em
memória divergiria do que o log diz, e a divergência só apareceria no próximo restart ou failover.
O ticket que falta tem, por isso, três partes e não uma:

- **evento de orçamento** (`budget.limit_raised` ou equivalente) com o principal que o autorizou,
  o tecto anterior, o novo e a razão — no log, não em memória;
- **`Rebuild` a consumi-lo**, para o tecto reconstruído ser o tecto efectivo (é isto que a decisão
  de desenho actual exclui, e é isto que o ADR tem de reabrir);
- **autoridade**, com o piso já entregue por AOS-263: assinatura ed25519 de operador registado,
  nonce durável e selo WORM próprio — a mesma cerimónia do `continue`/`abort`, porque levantar o
  tecto é a decisão de maior consequência das três.

**Enquanto não existir**, o operador não fica sem via: `abort` + re-submissão do run com um tecto
maior faz o mesmo trabalho com um run novo, e o `continue` deixa o run correr até ao tecto que já
tem. É por isso que este deferimento é `ABERTO` e não bloqueante.

### N-DEF-271 — cobre DEF-271

**FECHADO em 2026-08-13 por AOS-280.** O ticket que esta nota pedia foi escalonado e
entregue: `modelgateway.NewProduction` compõe o slot de roteamento como a CADEIA
`failover` → `routingstage(router.New(…, router.WithScoring(…)))`
(`packages/platform/model-gateway/production_routing.go`), com o classificador de
produção a povoar candidatos e perfil, e a prova é pela CADEIA REAL ao nível do
gateway composto (`routing_chain_aos280_test.go`, na lista REQUIRED de
`scripts/ci/routing.sh`). As três partes que a nota nomeava foram todas cumpridas — a
relação com o `failover` foi DECIDIDA pelo dono (encadear, não substituir nem fundir,
2026-08-13), a composição existe, e a verificação é ao nível do gateway.

O que **não** fechou, e ficou com eixo próprio em vez de ser dado por feito:
`DEF-280-PORTAS` (orçamento e task-fit continuam seams — as suas fontes vivem noutros
módulos), `DEF-280-TOKENS` (o `Exchange` não transporta o prompt, logo a reserva de
admissão coordena por pedidos e não por tokens) e `DEF-280-NO` (o nó de referência
ainda não declara a escada de tiers, pelo que naquele binário o refino não corre).

A **remediação adversarial** de 2026-08-13 (pós-entrega) fechou no código o que a
composição tinha deixado em aberto — cobertura de preço e `RoutingConfig` parcial como
recusa de ARRANQUE, troca de modelo SELADA no audit WORM, atribuibilidade do erro de
perfil e guard AST alargado a quem decide — e acrescentou dois residuais com eixo:
`DEF-280-REGIAO` (a selagem cobre a troca de MODELO, não uma mudança só de região) e
`DEF-280-ADR021` (o §5-bis do ADR-021 continua a dizer que o scoring não tem efeito em
produção: é autoridade congelada e exige emenda 1.2 de dono, não uma edição do
implementador).

O registo histórico segue abaixo, por leitura.

**Ticket necessário — «Compor o estágio de roteamento (`routingstage` + `router`, com o scoring
armado) no pipeline do Model Gateway».** Epic sugerido: EPIC-06 (Model Gateway) ou EPIC-20, com
dependência de AOS-269 (a máquina, já entregue) e do ADR-021 emenda 1.1.

Não é ligar uma opção que faltou. O facto que este deferimento regista é mais fundo: o
`router.Router` de **AOS-059** — o router cost/load-aware com soberania, tiering, degradação e
admissão — **nunca esteve composto em produção**. O `NewProduction` do gateway compõe
`failover.NewStage` como estágio de roteamento, e `router.New` não tem, em todo o repositório, um
único chamador fora de testes: o router é exercitado pela suite de cenários AOS-063
(`routingtests`) e por mais nada. AOS-269 construiu o scoring **sobre** esse router, cumprindo o
ADR-021 na máquina; herdou, com ele, o facto de não estar ligado.

O ticket que falta tem, por isso, três partes e não uma:

- **decidir a relação com o `failover`** — o estágio de roteamento composto hoje resolve failover
  intra-fronteira; o `router` resolve soberania+carga+tier+orçamento. Substituir, encadear ou
  fundir é uma decisão de desenho do pipeline, não uma linha de wiring;
- **compor `routingstage.NewStage(router.New(…, router.WithScoring(…)))`** com a tabela de pesos
  embebida e as portas de factores ligadas às fontes reais (o `LoadProvider` do keypool, o
  `BudgetProvider` da degradação, o sinal de *task-fit* do eval harness);
- **prova pela cadeia real** — o caminho quente de **todas** as chamadas de modelo passa a mudar
  de decisor, pelo que a verificação tem de ser ao nível do gateway composto (não do router
  isolado), incluindo o comportamento sob saturação, degradação por orçamento e deny de soberania.

**Enquanto não existir**, nada regride: o gateway continua a rotear exactamente como antes (pelo
`failover`), e o scoring entregue fica inerte — declarado no doc do pacote `router`, na §5-bis do
ADR-021 e no Estado de AOS-269. É por isso que este deferimento é `ABERTO` e não bloqueante: não
há promessa por cumprir em produção, há uma capacidade construída à espera de um ticket de
composição que o dono do backlog tem de escalonar.

### N-DEF-276 — cobre DEF-276

**Ticket CRIADO — `AOS-279`** (`specs/EPIC-20_Prontidao_Agentica_Remediacao.md`, tabela de
tickets): «Ligar o golden-set de decomposição do planeador ao harness de eval e ao gate
`evalgate.sh`», com dependência de AOS-241 (o golden-set e o `Evaluate`, já entregues) e de AOS-114
(o harness de `packages/platform/eval` e o gate). O eixo deixou de ser `POR ATRIBUIR`: a nota
descrevia o ticket mas ninguém o escalonava, e o único sinal de que o golden-set do planeador não
entra no `AOS_EVAL_REPORT` era esta linha de registo.

Não é um teste em falta. O que este deferimento regista é uma **descontinuidade de nome**: existem
dois eval-gates com fronteiras diferentes, e só um deles é o que a CI chama por esse nome.

- `plannerprompt.Evaluate` (AOS-241) é o eval-gate **do prompt de decomposição**: amostragem K× por
  objectivo sobre fixtures, segurança a 100% de K, qualidade por limiar M/K, e trace-diffing
  distribucional contra a versão anterior. AOS-273 acrescentou-lhe cinco casos difíceis para as
  extensões de ADR-022 (o quinto, `adr022-must-reject-stale-version`, entrou na remediação
  adversarial e cobre o CARIMBO: a mesma fixture byte-a-byte, na linha anterior à que usa, tem de
  ser recusada com `plan_version_below_features`). Corre como teste do pacote — cobertura real e
  bloqueante para o módulo, e o corpus publicado está PINADO como literal fora da árvore
  (`adr022PinnedCorpus`), para que apagar um caso difícil dos dois lados deixe de passar despercebido.
- `scripts/ci/evalgate.sh` (ADR-012/AOS-114) corre o harness de `packages/platform/eval`, que marca
  artefactos comportamentais **candidatos** (skills, memória procedural) contra golden-sets curados,
  emite `gen_ai.evaluation.result` ligado ao trace e produz o relatório `AOS_EVAL_REPORT` com piso
  de 0.90. Não conhece o golden-set do planeador.

O ticket que falta tem duas partes, e a primeira é uma decisão e não wiring:

- **decidir qual é a superfície comum** — o harness de `platform/eval` marca um candidato contra um
  dataset; o `plannerprompt` avalia K amostras de um objectivo contra asserções que delegam no
  validador puro. Reduzir o segundo ao primeiro (ou expor o `Report` do planeador como um dataset do
  primeiro) é desenho de contrato entre dois módulos, não uma chamada;
- **fazer o pass-rate do planeador entrar no `AOS_EVAL_REPORT`** e no piso, para que uma queda de
  cobertura do planeador avermelhe o gate que a promoção (AOS-242) lê, e não apenas a suite do
  módulo.

**Enquanto não existir**, nada regride e nada está por cumprir em produção: os casos correm, e um
plano canónico de ADR-022 que o validador recusasse — ou um caso difícil removido/esvaziado sem
aprovação — avermelha o módulo `control-plane/orchestrator`. O que falta é a **sinalização** no gate
com esse nome e a agregação do sinal de promoção. O AC2 de AOS-273 foi **partido** por isto, em vez
de marcado inteiro.

### N-DEF-278 — cobre DEF-278

**Ticket por CRIAR: «orçamento DURÁVEL por `run_id`».** É a peça que fecha a alínea (c) do registo
— o tecto ser **por-incarnação** — e a única das três que é dívida de mecanismo e não uma limitação
inerente. A descrição, para que o dono do backlog o possa escalonar sem reconstruir o contexto:

- **O quê.** Estado de orçamento (reservado/confirmado por `run_id`) que sobreviva à re-hospedagem e
  ao restart do processo, na mesma linha de durabilidade do ledger de turnos. O `budget.Budget`
  **já** tem eventos `budget.reserved/committed/released` e um `Rebuild` que reconstrói contadores a
  partir deles (`packages/control-plane/budget/events.go`); o que não existe é o **emissor ligado**
  no nó e a reconstrução no arranque de cada hospedagem — hoje o `integration.RunBudget` compõe a
  árvore **sem** `WithEmitter` e o nó do run é criado vazio em cada `SecuredRuntime.Run`.
- **Porque não foi feito em AOS-260.** Seria um ticket de **durabilidade**, não de admissão: exige
  decidir a retenção desses eventos, a interacção com o shred DSAR (o `run_id` é dado de run) e o
  que acontece a um `Rebuild` parcial — e um remendo em memória trocaria a assimetria por nós vivos
  para sempre, na mesma zerados no primeiro restart. O que AOS-260 fez foi **não agravar** o eixo: a
  admissão do turno de modelo usa o MESMO nó por-run do hook de tool calls, pelo que não há uma
  segunda contabilidade a divergir.
- **Enquanto não existir**, nada está por cumprir em produção que o banner não declare: a assimetria
  «o AVISO vê o total (ledger durável, cumulativo), o ENFORCEMENT recomeça (árvore em memória,
  por-incarnação)» está no banner de arranque, em `deploy/node/README.md` e no `doc` de
  `RunBudget.acquire`. Um run em ciclo de escalada/retoma pode consumir até N × tecto.

As alíneas (a) — tecto em dólares admitido um turno tarde — e (b) — o excedente de um turno já
corrido — **não têm ticket a criar** e não são dívida de código: (a) fecha-se com uma fonte de tarifa
consultável do lado do runtime, que hoje só existiria duplicando a tabela de preços do Model Gateway
(a fonte de verdade que AOS-259 fixou), e (b) é uma verdade sobre o mundo — um turno que já correu
custou o que custou, e a única alternativa a cobrá-lo até ao topo seria não o cobrar.

### N-DEF-279 — cobre DEF-279

**Ticket necessário — «Verificação de cobertura de preço sobre TODOS os pares alcançáveis do inventário (não só o par de configuração)».** Epic sugerido: EPIC-06 (Model Gateway), com dependência de AOS-259.

Hoje não há divergência possível: o nó compõe **uma** conta de infra (`InfraAccount`), logo o par resolvido pelo roteamento é sempre o par configurado, e a verificação de arranque é exaustiva sobre o inventário real. O ticket que falta só passa a ter conteúdo **quando essa premissa cair** — quando o nó compuser mais do que uma conta/região, o roteamento poderá resolver um par que a verificação de arranque não viu, e o cálculo fail-closed por chamada (`ErrNoPrice`) passaria a recusar chamadas que o banner declarou cobertas.

O trabalho, nesse momento, é: enumerar os pares alcançáveis a partir do inventário composto, verificar a cobertura de **todos** no arranque, e fazer o banner declarar a garantia pelo inventário (não pelo par único). **Enquanto a premissa se mantiver**, abrir este ticket seria escrever código sobre um inventário de uma entrada — a razão pela qual fica deferido e não por fazer.

### N-DEF-810 — cobre DEF-810

> **RESOLVIDO por AOS-212 (EPIC-18 §8-bis).** A porta existe: a tool reporta o custo medido do
> efeito ao Reference Monitor (`referencemonitor.Decision.CostMicroUSD`, alimentado por
> `Monitor.RegisterCosting`/`m.dispatch`), o `activity.Dispatcher` capta-o por **canal lateral** na
> closure de `Apply` (nunca no `durable.Result` gravado no ledger) e anota o `aos.activity` a partir
> do **desfecho do efeito** — só em `applied`, nunca em `dedup`/`replay`. A linha de código do §3
> foi removida (o marcador `[DEFERIDO]` já não existe em `runtime_ports.go`; o registo só encolhe).
> O **produtor real** por-tool (Model Gateway / tools pagas) fica explicitamente deferido em EPIC-06.
> A nota permanece como registo histórico do eixo e da sua resolução. O texto abaixo é o de origem.

Nomeado por **AOS-211** ao pôr o `gen_ai.operation.name` no `aos.activity` (EIXO 1) e ao encarar
o EIXO 2 — o **custo por efeito real** — do mesmo span. O CA #3 de AOS-211 abençoa explicitamente
o deferimento: «propaga um custo por efeito real … **a partir de uma fonte declarada** (ou o eixo
fica **explicitamente deferido** com a razão escrita: a porta que o forneceria não existe)».

A razão é estrutural, não falta de trabalho: na via durável do nó, `integration.DurableDispatcher`
traduz um `referencemonitor.Call` numa `activity.Activity`, e **nenhum dos dois lados carrega
custo**. O `Call`/`CallContext` (`packages/kernel/reference-monitor/call.go`) tem `Taint`,
`BudgetTokensRemaining`, `Reversibility`, `Sensitivity`, `RiskClass`, `RiskApprover`,
`RiskDecisionMode` — não um campo de custo do efeito. A `referencemonitor.Decision` devolvida pelo
dispatcher também não. Sem fonte, deixar `CostMicroUSD` a zero é o comportamento **correcto** do
span (zero não emite: custo gratuito e custo desconhecido são indistintos por desenho), não uma
perda silenciosa. Isto **não** duplica AOS-078 (custo do span DO MODELO, tokens/custo do turno):
aqui é o custo do **efeito** de uma tool, outro eixo.

**Ticket — «Fonte declarada do custo por efeito real da tool» — ENCAMINHADO a AOS-212** (EPIC-18
§8-bis, execução **EPIC-08**), com dependência de AOS-021 (o span de escopo durável), AOS-210 (que o
pôs na árvore exportada) e AOS-211 (`operation.name` + a disciplina «custo opcional, uma vez por
efeito real»). O eixo do registo deixou de ser `POR ATRIBUIR`. Decisão de desenho fixada no ticket:
custo **medido** do desfecho do efeito por **canal lateral** do `Apply` (não estado durável no ledger,
para o replay não o re-incorrer), com o **produtor real** (Model Gateway / tools pagas) deferido em
EPIC-06.

- O `referencemonitor.Call`/`Decision` (ou o `Result` do `activity.Dispatcher`) passa a poder
  transportar o **custo do efeito** apurado quando a tool o reporta, sem o confundir com o custo do
  turno de modelo (AOS-078).
- O `DurableDispatcher` propaga esse custo para `Activity.CostMicroUSD`, e o span `aos.activity`
  emite `gen_ai.usage.cost_usd` **uma só vez por efeito real** — nunca em `dedup`/`replay` (a guarda
  de `dispatch.go` já o garante: só no ramo `applied`).

**Porque não tem ticket hoje:** procurar «custo do efeito» / campo de custo no `Call` no backlog
devolve zero — o eixo do custo por efeito real nunca teve `AOS-NNN` próprio. AOS-211 entrega o
EIXO 1 (operation.name sob contrato) e **nomeia** este EIXO 2 em vez de o deixar mudo, que é
precisamente a deriva que o ticket veio terminar.

### N-DEF-811 — cobre DEF-811

> **O ticket que falta é uma DECISÃO, não código.** O eixo fica `POR ATRIBUIR` porque compor o
> MEM no nó e declará-lo fora do grafo de build são resoluções opostas, e a escolha é do dono da
> forma do produto (Carta §2), não de quem regista. O que este registo fixa é o **estado**: a
> biblioteca existe, é testada, tem gate próprio, e o nó usa uma escrita.
>
> **Porque isto não é o caso do ORQ/SCH.** Ali, «não composto» é doutrina: o ADR-018 §4 e o
> ADR-023 declaram-no, o `EPIC-10`/AOS-281 escreve-o em palavras («não é wiring esquecido: é o
> ADR-018 a impedi-lo por desenho»), e `packages/cmd/aos/boundary_orq_sch_test.go` impõe-no por
> guard-test. Para o MEM não existe nada equivalente — procurado em todo o `docs/adr/`. Enquanto
> a decisão não for tomada, «não composto» significa **inacabado**, e é essa a diferença que
> DEF-811 existe para não deixar apagar.
>
> **Ticket necessário:** um que ou (a) ligue o read-path da memória ao loop do agente com o
> `Goal.MemoryContext` preenchido e a barreira de taint do ADR-005 a mediar, ou (b) emita um ADR
> no molde do ADR-023 que declare o MEM fora do grafo de build da v1, com guard-test. Origem:
> `analises/11` §2 e §6 (item 6); AOS-326 registou a postura, não a resolveu.

### N-DEF-812 — cobre DEF-812

> **Idem DEF-811, no eixo do REG, e com um agravante próprio.** O `posture_banner.go` não dizia
> nada sobre o REG até AOS-326 — ao contrário do credential broker, cuja ausência é declarada em
> cada arranque desde AOS-070. Um operador lia «Skill/Tool Registry» no `_BRIEF` §2 e não tinha
> como saber que o nó arranca com catálogo vazio e trust store vazio.
>
> **O que corre é real e não deve ser confundido com o que falta:** o congelamento por run e a
> revalidação por chamada estão compostos na cadeia do Reference Monitor, e o catálogo vazio é
> default-deny — o nó não executa tools, não as executa mal. O que não existe é o catálogo
> event-sourced, o host MCP e o TOFU.
>
> **Ticket necessário:** um que ou (a) componha `registry.Registry` + `mcp.Host` + `tofu.Monitor`
> no nó, com trust anchors reais por config, ou (b) emita o ADR que os declare fora do grafo de
> build da v1. Nota de dependência: o AOS-320 (digest de `mcp_server`) deve estar fechado antes
> de (a) — ligar o host MCP com o digest constante da classe de egress poria em produção um pino
> que não distingue servidores. Origem: `analises/11` §3.1 e §6 (item 6).

### N-DEF-813 — cobre DEF-813

> **Não há ticket porque não há, hoje, defeito.** As duas custódias que existem fazem a escolha
> certa, e o AOS-322 fixou-a por teste (`aos322_confirmacao_shred_test.go` assere as três ausências
> E a premissa que as justifica — que o `Delete` do vault de referência é infalível). O que fica
> registado é a FORMA do risco: a porta é opcional, e a opcionalidade não distingue «esta custódia
> não precisa de confirmar» de «esta custódia devia confirmar e não confirma».
>
> **Ticket necessário, quando o gatilho ocorrer:** um que torne a escolha imposta em vez de
> declarada — por exemplo, exigindo que uma custódia composta sob `AOS_MODE=production` implemente
> a porta, ou que declare explicitamente por que não precisa. Enquanto só existirem as duas
> custódias actuais, criar esse ticket seria trabalho sobre um risco que nenhuma composição real
> corre. Origem: `analises/11` §8.2 (N-01, cujo enunciado original foi falsificado — ver AOS-322).

### A-DEF-301 — ARBITRAGEM do eixo da cifra do substrato (DEF-301, DEF-303…DEF-307)

**Isto não é uma nota de ticket em falta — é uma arbitragem, e o resultado dela foi corrigir
uma primeira leitura deste próprio registo.** Está aqui, e não em `N-DEF-…`, porque as seis
linhas deixaram de ser `POR ATRIBUIR`.

**A tensão.** A primeira versão deste registo pôs as seis linhas em `POR ATRIBUIR` com o
argumento «AOS-093 entrega o crypto-shredding do *audit*, não do substrato», e descreveu um
ticket novo a criar. A revisão contestou-o lendo o ticket: o **primeiro critério de aceitação
de AOS-093** é

> «Toda a PII persistida é **cifrada com uma chave por titular** (chave no vault; o agente
> nunca a vê).»

— sem restrição ao audit —, e os Detalhes Técnicos listam **`OBS/ES (audit hash-chain
intacto)`**, em que `ES` é o Event Store, que é exactamente o substrato em causa.

**A decisão (2026-07-27): o eixo é `AOS-093`, estado mantido `ABERTO`.** Razões:

1. A leitura ampla é a **literal**. «Toda a PII persistida» inclui o conteúdo dos runs; o
   conteúdo dos runs é PII persistida. Restringir AOS-093 ao audit exigia uma emenda ao texto
   do ticket, não uma leitura dele.
2. AOS-093 **não está entregue** (todos os seus CA e o DoD estão por marcar), pelo que apontar
   para ele não é declarar a dívida fechada — é dar-lhe o executor que já existe.
3. Criar um ticket novo com este âmbito **duplicaria** AOS-093 e produziria dois donos para a
   mesma propriedade. **Trocar um eixo errado por um eixo inflacionado é o mesmo defeito ao
   contrário** — e era isso que a primeira versão fazia.

**O que esta arbitragem NÃO decide.** Não decide que AOS-093, tal como está escrito, é
*suficiente*: os Detalhes Técnicos falam de «cifra envelope … antes de persistir» sem nomear
as **partições/streams** do Event Store nem o registo delas no `DSARIndex`. Isso é um
**refinamento do CA de AOS-093**, em `specs/EPIC-09` — ficheiro do dono do backlog, proibido a
AOS-196. Fica na pendência **P-3b**, com o texto proposto:

- o conteúdo dos runs (`objective`/`system`/`prompt` dos turnos, `Result.Payload` do
  step-ledger, capturas de não-determinismo) é cifrado com a **mesma chave por titular** que o
  crypto-shredding destrói;
- o `DSARIndex` regista as **partições/streams** do titular no Event Store, para o shred
  alcançar o substrato e não só o vault de audit;
- critério falsificável: após `POST /dsar/erase`, um `grep` pelo texto do prompt no ficheiro do
  Event Store não devolve nada e a hash-chain continua a validar.

**Se a arbitragem for revertida** (ou seja, se o dono do backlog confirmar que AOS-093 é
audit-only e reescrever o CA #1 para «toda a PII persistida **no audit**»), as seis linhas
voltam a `POR ATRIBUIR` e esta secção volta a ser `### N-DEF-301 — cobre DEF-301, DEF-303,
DEF-304, DEF-305, DEF-306, DEF-307` com o ticket descrito acima. O gate aceita as duas formas;
o que ele **não** aceita é o estado anterior a esta discussão — `EPIC-06/09/10` em
`bootstrap.go`, `EPIC-13` em `tecnica/02` e no `agent-runtime`, `EPIC-09/10` em `tecnica/17`:
**quatro** sítios, **três** destinos mutuamente inconsistentes, e o EPIC-13 é o epic de
*Frontend*. Esse era o achado **DEF-01**.

**Registo irmão.** Esta arbitragem pertence também a
`docs/governance/REGISTO-Decisoes-Reabertas-e-Arbitragens.md` (criado por AOS-200 para as
decisões reabertas). Esse ficheiro está **fora do âmbito de escrita de AOS-196** — a
transcrição fica na pendência **P-6**.

### N-DEF-401 — cobre DEF-401, DEF-402 — **TICKET CRIADO: AOS-206** (`specs/EPIC-18` §8-bis)

É o achado **DEF-03**.

**Ticket necessário — «Compor o caminho de promoção/auto-modificação no nó com
`NewProductionRatificationGate`».** Epic sugerido: EPIC-14 (Integração e Composition-Root) ou
EPIC-18, com dependência de AOS-159 e AOS-096.

- O nó `aos` compõe um *promotion controller* real (hoje `grep "promotion\|Promote"` em
  `packages/cmd/aos/*.go` não-teste devolve **zero**).
- Esse controller usa a via sancionada `hitl.NewProductionRatificationGate`, que **força**
  `WithRatifyFreshness` + `WithRatifyNonceStore` e recusa a construção sem eles — não
  `NewRatificationGate` cru.
- Critério falsificável: um teste de ápice em que a mesma ratificação, re-submetida após
  consumo, devolve `ReasonRatificationReplayed` **através do caminho do nó**, não do gate
  isolado.

**Porque não tem ticket hoje:** AOS-159 entregou o mecanismo e o CA do wiring foi marcado
`[x]` sem chamador de produção existir (corrigido por AOS-196). O ADR-012 apontava o
endurecimento à EPIC-13 — Frontend.

### N-DEF-501 — cobre DEF-501 — **TICKET CRIADO: AOS-207** (`specs/EPIC-18` §8-bis)

É o achado **DEF-06**.

**Ticket necessário — «Assinatura e atestação da imagem do nó: chave de release, atestação
in-toto/SLSA assinada e verificação na entrega».** Epic sugerido: EPIC-10 (Topologia/Operação)
ou EPIC-05 (Registry/Supply-chain), com dependência de AOS-168 e AOS-187.

- Custódia da chave de assinatura de release documentada (quem assina, onde vive a chave,
  como se roda) — o ADR-017 ponto 5 já exige custódia própria para a autoridade de identidade;
  a imagem do nó não tem equivalente.
- A atestação de proveniência passa de **gerada** a **assinada e verificável**, e a entrega
  recusa uma imagem cuja assinatura não valide.
- Critério falsificável: substituir o digest da imagem no manifesto de entrega faz o gate
  ficar vermelho.

**Porque não tem ticket hoje:** o eixo estava escrito como «parte do endurecimento de
EPIC-10». Nenhum dos onze tickets do EPIC-10 assina imagens: AOS-098 é IaC, AOS-099 workers,
AOS-100 replicação, AOS-101 backup, AOS-102 DR, AOS-103 microVMs, AOS-104/105/106 dashboards,
alertas e runbooks, AOS-107 escala, AOS-108 hipercare. AOS-168 entregou o **empacotamento**
(distroless/non-root/read-only, binário zero-dep, SBOM gerado) e AOS-187 ligou os gates
`package`/`sbom` à CI — nenhum dos dois assina a imagem.

---

## 7. Pendências fora do âmbito de AOS-196

Nomeadas em vez de absorvidas, e por ordem. As primeiras são **propriedade de ficheiros**:
AOS-196 correu em paralelo com outros pipelines e o seu âmbito de escrita foi delimitado —
`CONTRIBUTING.md`, `AGENTS.md`, `specs/**` e `packages/**` (excepto o comentário do eixo em
`bootstrap.go`) estavam fora dele.

- **P-1 — as três strings do banner de arranque de `bootstrap.go`.** Os comentários do eixo
  foram corrigidos; as linhas `log(...)` que imprimem «DEFERIDO (EPIC-09/10)» e «cifra
  por-titular do substrato DEFERIDA (EPIC-06/09/10)» **ao operador** não foram tocadas: o
  âmbito de AOS-196 sobre `bootstrap.go` é «só o comentário do eixo», e uma string de log é
  **comportamento observável**, não comentário — além de `packages/cmd/aos/observability_test.go`
  estar a ser editado por outro pipeline e poder asseverar sobre o texto do banner. É a
  superfície mais **pública** do achado e continua errada. Estão na baseline com dono; é um
  `sed` de três linhas quando o ficheiro estiver livre.
- **P-2 — o eixo antigo em ficheiros de outros pipelines (uma entrada de baseline cada).**
  `durable/errors.go`, `durable/step_ledger.go`, `replay/nondeterminism_capture.go`,
  `cmd/aos/dsar.go`, `cmd/aos/main.go`, `reference-monitor/scope_gate.go`,
  `otel-genai/evaluation.go`, `tecnica/14`, `tecnica/17` e `docs/adr/ADR-018` continuam a
  citar o eixo antigo. **Achado desta segunda passagem:** a atribuição errada à EPIC-13
  (Frontend) ocorre em **seis** sítios, não nos dois que a auditoria v4 nomeou, e a grafia
  `DIFERIDO` acrescentou mais duas ocorrências da mesma forma textual (`scope_gate.go`→EPIC-08,
  `evaluation.go`→EPIC-11). O registo já traz o eixo correcto de cada uma; falta propagar o
  texto.
- **P-3 — criar os três tickets descritos no §6.** É trabalho do dono do backlog em
  `specs/EPIC-18` (proibido a AOS-196). Enquanto não existirem, as linhas `POR ATRIBUIR` são
  o contador que o gate imprime em cada execução, sob `DÍVIDA SEM EIXO` — o número não é
  repetido aqui à mão precisamente para não divergir dele. Baixou nesta passagem: seis linhas
  da família 3xx passaram a `AOS-093` pela arbitragem A-DEF-301.
- **P-3b — refinar o CA de AOS-093 em `specs/EPIC-09`. ✅ FEITO** (2026-07-27, pelo dono do backlog):
  o CA #1 passa a nomear explicitamente o conteúdo dos runs no Event Store; foi acrescentado um CA para
  o `DSARIndex` registar as partições/streams do titular; foi acrescentado o critério falsificável do
  *grep* pós-`/dsar/erase`; os Detalhes Técnicos deixam de fundir as duas obrigações de `OBS/ES`; e os
  Testes Requeridos ganham o teste de alcance ao substrato e o do índice. *(descrição original:)* Consequência directa da arbitragem
  A-DEF-301: o eixo da cifra do substrato passou a apontar para AOS-093, mas o texto do ticket
  não nomeia as **partições/streams** do Event Store nem o seu registo no `DSARIndex`. O texto
  proposto está no §6. Sem ele, o eixo é correcto mas subespecificado — e um eixo
  subespecificado é a semente do próximo DEF-01.
- **P-4 — sincronizar o catálogo de gates (documental, não bloqueante).** Duas linhas em dois
  ficheiros fora do âmbito: `CONTRIBUTING.md` (linha `REQUIRED-CHECKS:` e a tabela de gates) e
  `specs/01_Engineering_Standards_e_Handoff.md` (a tabela canónica de gates que o cabeçalho de
  `scripts/ci/run.sh` cita como fonte da ordem). **A primeira é BLOQUEANTE:** o self-test §M
  compara a lista de required checks em três sítios por sequência exacta, e `deferrals` foi
  acrescentado a dois deles (`needs:` e o comentário `REQUIRED-CHECKS:` de `ci.yml`), não ao
  terceiro. Até alguém inserir `deferrals · ` a seguir a `ref-lint · ` em `CONTRIBUTING.md`, o
  gate `selftest` fica **vermelho** — e, por estar no `needs:` do agregador, arrasta a CI toda.
  É deliberadamente barulhento: o §M imprime as duas listas lado a lado. Saída verificada em
  2026-07-27, com `deferrals` já no `needs:`:

  ```text
  SELFTEST FAIL M: REQUIRED-CHECKS de CONTRIBUTING.md diverge do needs: do agregador
         needs:        … ref-lint deferrals rtm …
         CONTRIBUTING: … ref-lint rtm …
  ```

  A correcção é uma substituição textual na linha `REQUIRED-CHECKS:` de `CONTRIBUTING.md`:
  `· ref-lint · rtm ·` → `· ref-lint · deferrals · rtm ·`. Convém acrescentar também a linha
  `2b'` à tabela de gates do mesmo ficheiro, para o documento **descrever** o gate e não só o
  listar. `specs/01` não é comparado por gate nenhum (nada compara essa tabela com
  `ALL_GATES`), mas deixa um gate de merge fora do catálogo de standards que o cabeçalho de
  `scripts/ci/run.sh` cita como fonte da ordem canónica. **Coordenar com o pipeline paralelo
  que já tem `CONTRIBUTING.md` modificado**, para a edição não se perder no merge.
- **P-5 — reconciliação dos códigos de erro de porta C3/C4/C5.** As 10 entradas de
  `scripts/ci/baseline/contract-codes.txt` criadas por AOS-198 trazem `owner=AOS-196`.
  AOS-196 é o **registo** de deferimentos, não o executor da reconciliação: renomear códigos
  em `packages/platform/{broker,model-gateway,registry}` está fora do seu âmbito de escrita.
  A dívida fica onde AOS-198 a pôs — visível, com dono, e a encolher só por remoção.
- **P-6 — transcrever a arbitragem A-DEF-301 para o registo irmão.**
  `docs/governance/REGISTO-Decisoes-Reabertas-e-Arbitragens.md` é o ficheiro que AOS-200 criou
  para as decisões reabertas e arbitradas, e está fora do âmbito de escrita de AOS-196. A
  arbitragem vive hoje só no §6 deste registo.

---

## 8. Referências

- `analises/08_Relatorio_Auditoria_Multiagente_v4.md` — achados DEF-01, DEF-03, DEF-06;
  recomendação 7.
- `specs/EPIC-18_Remediacao_Auditoria_Multiagente_v4.md` — AOS-196.
- `docs/governance/REGISTO-Decisoes-Reabertas-e-Arbitragens.md` — registo irmão (AOS-200):
  mesmo espírito (tabela estável, vocabulário controlado, comando de verificação), objecto
  diferente (decisões reabertas, não deferimentos).
- `scripts/ci/deferrals.py` · `scripts/ci/baseline/deferrals.txt` · `scripts/ci/run.sh` ·
  `.github/workflows/ci.yml`.
### N-DEF-281 — cobre DEF-281 — **TICKET POR CRIAR**

O achado é de **2026-08-27**, e nasceu de uma correcção que não corrigiu o que dizia corrigir.

**O que se sabe, medido.** O SLI `mediation_overhead_p95` deriva dos spans `execute_tool`. Há
DOIS produtores desse nome, e **ambos incluem o despacho da tool**: o do worker envolve gate
fenced + `ledger.Apply(mediação + despacho)`, e o do Reference Monitor parece ser só a mediação
mas `Monitor.evaluate` chama `m.dispatch` ANTES de devolver a decisão. O filtro introduzido em
AOS-085 escolhe a janela mais estreita (a do monitor), o que é uma melhoria de selecção de
amostras — mas o número continua a ser um **tecto superior do custo de uma tool call mediada**.

Medido em produção com o filtro activo: **1 amostra, `3,047s`** contra um SLO de `15ms`. Antes
do filtro: 2 amostras, `4,017s`. A ordem de grandeza não mudou, e não podia mudar.

**Ticket necessário — «Instrumentar a cadeia de política separadamente do despacho, para que o
`mediation_overhead_p95` meça o que o nome promete».** Epic sugerido: o mesmo que receber o
follow-up já declarado em `packages/cmd/aos/api.go` («as latências de request exigem histogramas
instrumentados no kernel»).

- Cronometrar em `Monitor.evaluate` o intervalo da cadeia de política EXCLUINDO a janela de
  `m.dispatch` — por span filho dedicado, ou por atributo de duração no span existente.
- Fazer `overheadP95SLI` consumir essa medida em vez da latência do span inteiro.
- Manter o filtro da decisão: continua a ser o que impede o span do worker de entrar na amostra.
- Rever se o SLO de `15ms` é o alvo certo depois de a medida passar a ser a correcta — hoje é
  incomparável com o que se mede.

**Consequência operacional enquanto não for feito:** o alerta `mediation_overhead_high`
(`critical`, RB-04) dispara em qualquer nó com tráfego real. Quem opera o RB-04 deve saber que,
até este ticket existir, o disparo **não** significa mediação degradada — significa que houve
uma tool call.
