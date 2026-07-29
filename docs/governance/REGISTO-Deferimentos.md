# Registo único de deferimentos — onde cada dívida declarada tem um eixo verificável

| Campo | Valor |
|---|---|
| Documento | **Registo único** dos deferimentos declarados no código e no corpus: um por linha, com **eixo**, **dono**, **gatilho de reavaliação** e **estado** |
| Autoridade | **Subordinado**. Este ficheiro **não decide nada** e **não cria tickets** — regista o que já está deferido e torna o eixo verificável por comando |
| Origem | AOS-196 (EPIC-18), achados **DEF-01**, **DEF-03** e **DEF-06** da auditoria multiagente v4 |
| Gate que o impõe | `scripts/ci/deferrals.sh` (bloqueante; `run.sh` → `ALL_GATES`, job `deferrals` em `ci.yml`, `needs:` do agregador `gates`) |
| Última actualização | 2026-07-29 |

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
| DEF-012 | DEFERIDO | packages/cmd/aos/api.go | **mTLS do plano de controlo e autenticação forte da perna OTLP — MECANISMO ENTREGUE OPT-IN.** O mTLS do plano de controlo (CA de cliente por AOS_CONTROL_MTLS_CA_PATH; escopado a /steer,/pause,/approve; ADITIVO à assinatura ed25519, nunca bypass) e a autenticação forte da perna OTLP (mTLS de cliente ou bearer, por ficheiro; fail-open de AOS-173 preservado) estão ENTREGUES e fail-closed. Fica deferido apenas o que é infra-org, não código do nó: a PROVISÃO de PKI/emissão de certificados de cliente aos operadores e o bearer/mTLS do lado do colector | POR ATRIBUIR | Responsável de Segurança | Provisão de infra (PKI de cliente / configuração de autenticação do colector) numa organização real — o mecanismo do nó já não é o gargalo | MITIGADO |
| DEF-101 | DEMO-GRADE | packages/integration/issuer_authority.go | `AllowlistDirectory` é a impl de referência de `HumanDirectory`: regista humanos, não prova autenticação | AOS-174, AOS-156 | Responsável de Segurança | O nó compor `OIDCDirectory` por configuração em vez da allowlist | MITIGADO |
| DEF-102 | NUNCA-EM-PRODUCAO | packages/integration/issuer_authority.go | `AuthenticateAssertion` aceita a asserção sem prova criptográfica (não há IdP na allowlist) | AOS-174 | Responsável de Segurança | Idem DEF-101 | MITIGADO |
| DEF-103 | DEFERIDO | packages/integration/issuer_authority.go | Endurecimento posterior da autoridade: HSM/sign-in-place e chave fora do processo | AOS-175 | Responsável de Segurança | Custódia de chave num KMS/HSM real de ambiente | MITIGADO |
| DEF-104 | DEMO-GRADE | packages/integration/oidc/oidc.go | O verificador OIDC real existe; o marcador nomeia a allowlist que ele substitui | AOS-174 | Responsável de Segurança | Seam de configuração do directório humano no nó | MITIGADO |
| DEF-105 | DEMO-GRADE | packages/integration/oidc_directory.go | Idem: `OIDCDirectory` substitui a allowlist, mas o nó não o compõe por configuração | AOS-174 | Responsável de Segurança | Idem DEF-104 | MITIGADO |
| DEF-106 | STUB | packages/integration/device_attestation.go | Porta de attestation de dispositivo: no nó zero-dep vive só o contrato (bytes e erros) | AOS-177 | Responsável de Segurança | Componente externo de attestation configurado no ambiente | MITIGADO |
| DEF-107 | STUB | packages/integration/foureyes.go | Sem a porta ligada o 4-eyes é ESTRUTURAL (igualdade de strings), não duas credenciais WebAuthn atestadas distintas como o ADR-016 §4 exige | AOS-177, AOS-162 | Responsável de Segurança | Idem DEF-106 | ABERTO |
| DEF-108 | DEMO-GRADE | packages/integration/steer_authenticator.go | Marcador de contraste: o `HMACAuthenticator` replayável que o `Ed25519Authenticator` substituiu | AOS-160 | Arquitecto de Plataforma | Remoção do HMAC de referência do canal de controlo | FECHADO-RESIDUAL |
| DEF-109 | DEMO-GRADE | packages/kernel/agent-runtime/control/steer_channel.go | O `HMACAuthenticator` de referência ignora o nonce (comportamento demo mantido por compatibilidade do seam) | AOS-160 | Arquitecto de Plataforma | Idem DEF-108 | FECHADO-RESIDUAL |
| DEF-110 | DEMO-GRADE | packages/cmd/aos/bootstrap.go | `Config.Humans` alimenta a allowlist DEMO-GRADE-AUTH da autoridade; não há ramo de configuração para OIDC | AOS-174, AOS-163 | Responsável de Segurança | Seam de `HumanDirectory` exposto na `Config` do nó | MITIGADO |
| DEF-111 | STUB | packages/cmd/aos/bootstrap.go | Doc de pacote: contraste com os STUBS NEUTROS do `cmd/aos-demo`; o nó `aos` compõe a cadeia real | AOS-163 | Arquitecto de Plataforma | Remoção do ápice mínimo demo | FECHADO-RESIDUAL |
| DEF-112 | DEMO-GRADE | packages/cmd/aos-demo/main.go | Ápice mínimo: HMAC efémero gerado em runtime + registo demo; é o binário de demonstração, não o nó | AOS-163 | Arquitecto de Plataforma | Idem DEF-111 | FECHADO-RESIDUAL |
| DEF-113 | STUB | packages/cmd/aos-demo/main.go | O demo compõe o RM com STUBS NEUTROS (sem enforcement) e declara-o no passo 7 e na limitação (b) | AOS-163 | Arquitecto de Plataforma | Idem DEF-111 | FECHADO-RESIDUAL |
| DEF-201 | DEFERIDO | packages/cmd/aos/bootstrap.go | AOS-205 entregou a FONTE DE AUTORIDADE board→região (SovereignRegionAuthority, rotação+auditoria de alterações): o registo deixou de ser o mapa de env tratado como verdade. Fica o provisionamento do TENANT concreto (o serviço de config/IdP de soberania real da organização que empurra as alterações autoritativas), que é infra-org | AOS-205 | Arquitecto de Plataforma + Responsável de Segurança | Existir organização com boards/regiões reais (Carta §4.2, D7 CONDICIONAL) | MITIGADO |
| DEF-202 | DEFERIDO | packages/cmd/aos/bootstrap.go | Verificação de coincidência `leitor.região == run.região` no selo D6: o selo grava a região do BOARD DO LEITOR, não a residência por-run | AOS-182 | Responsável de Segurança | Execução de AOS-182 (read-path soberano fail-closed D6/D7) | ABERTO |
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
| DEF-301 | DEFERIDO | packages/cmd/aos/bootstrap.go | **Cifra por-titular do substrato — NÚCLEO ENTREGUE (AOS-093).** O conteúdo não-determinístico dos runs (resposta do modelo + resultados de tools do capturer de replay) é agora CIFRADO por chave POR-TITULAR (envelope DEK/KEK, `audit.SealContent`) ANTES de tocar o WAL; a erasure DSAR destrói a MESMA KEK ⇒ o conteúdo fica IRRECUPERÁVEL (decifragem falha, provado ao nível do nó em `aos093_substrate_erase_test.go`) e a hash-chain do WORM continua a validar. O capturer regista subject→stream no DSARIndex (shred/hold alcançam o substrato). RESIDUAIS nomeados: (a) `turn.recorded` persiste só hashes (prompt_hash/system_hash), nunca o prompt cru — nada a cifrar; (b) o REPLAY de um run selado exige acesso do leitor ao vault do titular (fora do núcleo); (c) o step-ledger só sela com `Producer.NHIID` (o run injecta o principal) | AOS-093 (CA #1 — ver arbitragem A-DEF-301) | Responsável de Segurança | Replay soberano de conteúdo selado / KMS real (DEF-302) | FECHADO-RESIDUAL |
| DEF-302 | DEMO-GRADE | packages/cmd/aos/bootstrap.go | `DSARVault` é um `audit.InMemoryKeyVault`: as KEK por-titular vivem em memória; produção liga um KMS/HSM pela mesma porta | AOS-093, AOS-070 | Responsável de Segurança | Ambiente com KMS/HSM disponível | ABERTO |
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
| DEF-701 | NUNCA-EM-PRODUCAO | packages/substrate/sandbox/driver.go | O driver de referência NÃO cria jail nem impõe as invariantes de isolamento | AOS-064, AOS-068 | Responsável de Segurança | Catálogo de tools não-vazio a executar código não-confiável | ABERTO |
| DEF-702 | NUNCA-EM-PRODUCAO | packages/substrate/sandbox/driver_fake.go | Driver fake dos testes: corre no host | AOS-064 | Responsável de Segurança | Idem DEF-701 | ABERTO |
| DEF-703 | NUNCA-EM-PRODUCAO | packages/platform/broker/internal/vault/vault.go | Vault em memória do Credential Broker | AOS-070 | Responsável de Segurança | Ambiente com Vault/KMS real disponível | ABERTO |
| DEF-704 | NUNCA-EM-PRODUCAO | packages/platform/broker/vault_client.go | Reexporta o vault em memória como `vault.Memory` | AOS-070 | Responsável de Segurança | Idem DEF-703 | ABERTO |
| DEF-705 | NUNCA-EM-PRODUCAO | packages/platform/model-gateway/internal/adapters/adapter.go | Fonte de segredos por mapa `(provider, região)` para testes | AOS-070, AOS-057 | Responsável de Segurança | Idem DEF-703 | ABERTO |
| DEF-706 | NUNCA-EM-PRODUCAO | packages/platform/model-gateway/internal/credentials/broker.go | `ReferenceBroker` não contém segredos e recusa emitir (`ErrNotWired`); o wiring concreto é de infra | AOS-070 | Responsável de Segurança | Idem DEF-703 | MITIGADO |
| DEF-801 | DEFERIDO | packages/kernel/agent-runtime/activity/doc.go | O loop medeia cada tool call directamente e ainda NÃO despacha via `Dispatcher`; a idempotência pelo step-ledger não cobre o efeito externo real | AOS-022 | Arquitecto de Plataforma | Adopção do dispatcher ledger-backed no loop | ABERTO |
| DEF-802 | DEFERIDO | packages/platform/messaging/doc.go | Adaptadores para NHI real, broker/Vault e ponto real de troca de mensagens ficam para o composition root | AOS-073, AOS-070 | Arquitecto de Plataforma | Existir canal real de troca inter-agente no nó | ABERTO |
| DEF-803 | STUB | packages/control-plane/orchestrator/orchestrator.go | Decomposição goal→DAG é um stub: o grafo tem um nó único | AOS-025 | Arquitecto de Plataforma | Necessidade de grafos multi-tarefa com detecção de deadlock | ABERTO |
| DEF-804 | DEFERIDO | packages/cmd/aos/service.go | Persistência durável do shutdown e substrato durável tratados noutros tickets; aqui o substrato é o Event Store de referência | AOS-164, AOS-170 | Arquitecto de Plataforma | Idem: já entregues; revisitar se o `NodeService` mudar de substrato | FECHADO-RESIDUAL |
| DEF-805 | DIFERIDO | packages/kernel/agent-runtime/activity/doc.go | «Adopção pelo loop (AOS-013): DIFERIDA» — a mesma dívida de DEF-801 vista do lado do contrato de activity; AOS-157 entregou a porta `ActivityDispatcher`, o default continua a ser o despacho directo | AOS-022, AOS-157 | Arquitecto de Plataforma | Idem DEF-801 (adopção do dispatcher ledger-backed no loop) | ABERTO |
| DEF-806 | DIFERIDO | packages/kernel/agent-runtime/loop.go | Separação de planos (dual-LLM/CaMeL): o primitivo `SeparatePlanes` existe, mas o conteúdo untrusted continua a ser acrescentado INLINE ao tail que monta o prompt do turno seguinte | AOS-069 | Responsável de Segurança | Execução de AOS-069 (CA «separação efectiva entre o plano que planeia e o plano que manipula dados») | ABERTO |
| DEF-807 | DIFERIDO | packages/kernel/agent-runtime/model.go | `AuthorizationTaint` é uma string convencionada em vez de uma autorização estruturalmente infalsificável mintada no runtime | AOS-069 | Responsável de Segurança | Idem DEF-806 | ABERTO |
| DEF-808 | DIFERIDO | packages/kernel/reference-monitor/taint_gate.go | Sem `DefaultHooksWithTaint` e um `PrivilegedAuthorizer` real ligados no ápice, a metade do ADR-005 fica inactiva; o conjunto `Privileged` composto hoje é vazio | AOS-157, AOS-183 | Responsável de Segurança | Idem DEF-604 (conjunto `Privileged` real no ápice) | MITIGADO |
| DEF-809 | DIFERIDO | packages/kernel/reference-monitor/scope_gate.go | Wiring de produção do par escopo+taint no ápice, «a par de AOS-021/037/043» — as portas RT/RM foram entregues por AOS-157; falta o autorizador privilegiado real | AOS-157, AOS-183 | Responsável de Segurança | Idem DEF-808 | MITIGADO |
| DEF-901 | NUNCA-EM-PRODUCAO | packages/substrate/otel-genai/idgen.go | `SequentialIDGenerator` produz ids deterministas para testes de topologia de árvore | AOS-076 | Arquitecto de Plataforma | Uso do gerador determinista fora de testes | FECHADO-RESIDUAL |
| DEF-902 | NUNCA-EM-PRODUCAO | packages/testkit/env/vault.go | Vault efémero por `Env` do testkit | AOS-109 | Arquitecto de Plataforma | Importação do testkit por código de produção | FECHADO-RESIDUAL |
| DEF-903 | DEFERIDO | packages/cmd/aos/bootstrap.go | **CON-02 — legal hold e job de expiração sem superfície de administração no nó.** O `audit.LegalHold` está composto (`Node.DSARHolds`) mas sem rota de administração; o `audit.ExpirationJob` (AOS-092) não é composto (0 chamadores de produção). Decisão do dono (Opção C, 2026-07-29, dossiê `DOSSIE-CON-02-legal-hold.md`): a utilidade acopla-se ao apagamento real, hoje adiado (a cifra por-titular do substrato é AOS-093); a superfície de administração constrói-se DEPOIS. Princípio registado: obrigação de produto, concretizado na execução | AOS-093 | Dono do produto | AOS-093 entregue (a cifra por-titular torna o shred/expiração reais) | ABERTO |


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
| packages/cmd/aos/bootstrap.go | DEFERIDO | 7 |
| packages/cmd/aos/bootstrap.go | DEMO-GRADE | 9 |
| packages/cmd/aos/bootstrap.go | STUB | 1 |
| packages/cmd/aos/dsar.go | DEFERIDO | 1 |
| packages/cmd/aos/dsar.go | DEMO-GRADE | 1 |
| packages/cmd/aos/main.go | DEFERIDO | 2 |
| packages/cmd/aos/main.go | DEMO-GRADE | 3 |
| packages/cmd/aos/otlpexporter.go | DIFERIDO | 2 |
| packages/cmd/aos/promotion.go | DEFERIDO | 1 |
| packages/cmd/aos/service.go | DEFERIDO | 1 |
| packages/cmd/aos/sovereign_authority.go | DEFERIDO | 1 |
| packages/cmd/aos/sovereign_authority.go | DEMO-GRADE | 1 |
| packages/cmd/aos/sovereignty.go | CONDICIONAL | 1 |
| packages/cmd/aos/sovereignty.go | DEFERIDO | 3 |
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
| packages/kernel/reference-monitor/scope_gate.go | DIFERIDO | 2 |
| packages/kernel/reference-monitor/taint_gate.go | DIFERIDO | 1 |
| packages/platform/broker/internal/vault/vault.go | NUNCA-EM-PRODUCAO | 1 |
| packages/platform/broker/vault_client.go | NUNCA-EM-PRODUCAO | 1 |
| packages/platform/eval/doc.go | DIFERIDO | 1 |
| packages/platform/eval/runner.go | DIFERIDO | 1 |
| packages/platform/messaging/doc.go | DEFERIDO | 2 |
| packages/platform/model-gateway/internal/adapters/adapter.go | NUNCA-EM-PRODUCAO | 1 |
| packages/platform/model-gateway/internal/credentials/broker.go | NUNCA-EM-PRODUCAO | 1 |
| packages/substrate/otel-genai/doc.go | DIFERIDO | 2 |
| packages/substrate/otel-genai/evaluation.go | DIFERIDO | 4 |
| packages/substrate/otel-genai/exporter.go | DIFERIDO | 1 |
| packages/substrate/otel-genai/idgen.go | NUNCA-EM-PRODUCAO | 1 |
| packages/substrate/otel-genai/otlp.go | DIFERIDO | 1 |
| packages/substrate/otel-genai/semconv.go | DIFERIDO | 1 |
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
  (`/steer`,`/pause`,`/approve`), **escopado** (não impõe cert às rotas de dados/sondas), **ADITIVO**
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
