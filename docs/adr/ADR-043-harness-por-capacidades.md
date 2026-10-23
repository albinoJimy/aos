# ADR-043 — Harness por capacidades: conteúdo genérico, artefactos, modelos escolhidos pela tarefa

- **Estado:** Aceite — **ratificado pelo dono a 2026-10-22** («ratifico, prossiga com o AOS-546»).
  A fase R1a (AOS-546 e AOS-547) está em produção desde a v0.1.68 (2026-10-22): o modelo do run
  viu uma planta e listou os cômodos, com replay de fidelidade 1. A fase R1b (AOS-548 e AOS-549)
  está em produção desde a v0.1.69 (2026-10-23): o run viu a planta e gravou `comodos.json` e
  `planta.svg` no armazém do run, e o dono descarregou os dois. O resto do roteiro (§7) por fazer.
- **Data:** 2026-10-22
- **Deciders:** Dono do produto. A análise de base é
  `docs/reports/analise-critica-harness-flexivel-2026-10-22.md` (v2): foi sujeita a duas revisões
  adversariais independentes (factos e proposta), e a sua §8 regista o que caiu.
- **Tickets:** AOS-546 (fase R1a, o modelo do run vê media). Cada fase seguinte do roteiro (§7)
  abre o seu ticket na epic do componente que muda, e cada ticket cita este ADR.
- **Relacionados:**
  - ADR-002 (Reference Monitor mandatório), ADR-003 (NHI), ADR-005/ADR-034 (taint e autoridade do
    contexto), ADR-010 (replay e WORM), ADR-011 (policy-as-code e soberania), ADR-013 (SA-ROC);
  - ADR-017 (supply-chain), ADR-018 (fronteira nó↔ORQ), ADR-021 (scoring determinista no GW),
    ADR-036 (o tail e o perfil da rota), ADR-038 (saída por referência), ADR-041 (titular e
    candidato), ADR-042 (tool cujo efeito é uma chamada a um modelo).

> **Resumo.** O harness passa a crescer por **capacidades declaradas** e não por **casos especiais
> em código**. Hoje, cada modalidade nova é um campo novo na porta, cada uso de um modelo como
> ferramenta é uma tool com nome fixo em Go, cada modelo é um alias aprovado à mão, e a resposta
> do modelo é só texto. Este ADR fixa a direcção em seis decisões:
>
> 1. uma **parte de conteúdo genérica**, nos dois sentidos;
> 2. um **armazém de artefactos** do run;
> 3. **uma** tool genérica de invocação de modelo, configurável;
> 4. o modelo **escolhido pela tarefa**, a partir de requisitos confiáveis;
> 5. um **catálogo qualificado** em vez de uma lista de nomes;
> 6. **tools declarativas** (MCP, HTTP declarado, execução de código).
>
> Tudo isto vive **dentro** das fundações, e não por excepção a elas. O RM, a NHI, a execução
> durável, o SA-ROC (ADR-013) e a Carta **não mudam**. Uma chamada a um modelo com conteúdo do run
> **continua a ser egress externo**, classificado pela sensibilidade dos dados. A protecção contra
> a injecção concretiza a **opção A do ADR-034** (dados por handle), que já era o destino
> declarado.

## 1. Contexto

### 1.1 O pedido

O dono quer um harness versátil e agnóstico de modelos, que use a OpenRouter para escolher e
combinar modelos conforme a tarefa e que **produza artefactos**: imagens, vídeo, ficheiros. O
exemplo dele: «dada a foto de um ambiente, gerar os cômodos». A solução tem de ser genérica, não
feita para esse caso.

### 1.2 O que o código tem hoje (medido a 2026-10-22)

| Hoje | Onde | Razão registada |
|---|---|---|
| Um modelo do run por nó (`AOS_MODEL_NAME`) e no máximo um candidato | `cmd/aos/modelo_candidato.go:44` | ADR-041, D6 |
| Um modelo novo exige o banco de ensaio, duas assinaturas (allowlist e perfil) e cerca de seis ficheiros, sem release | `PROC-BANCO-DE-ENSAIO.md`; AOS-519/522 | ADR-041/042 |
| O nó não conhece o catálogo da OpenRouter | — | — |
| O router (scoring) está composto no GW mas é inerte no nó: sem escada de tiers e com o task-fit sem fonte | `cmd/aos/modelgatewaywiring.go:246` | DEF-280-NO, DEF-280-PORTAS |
| A porta conhece três capacidades (`tools`, `imagem`, `video`) e uma só wire class; a resposta é só texto | `platform/model-gateway/route.go:86-103`; `port/port.go:182` | AOS-509: recusar, não perder |
| O preço só se conta por tokens | `pricing/table.go:83` | — |
| Só `ocr_read`/`video_read` chamam um modelo, e o efeito está em Go | `cmd/aos/ocr_tool.go:217` | ADR-042 §2.1 |
| O guest gVisor só sabe `read`; o MCP está construído mas não é composto no nó | `deploy/server/gvisor/guest/main.go:71`; `platform/registry/mcp/` | DEF-812 |
| Não há armazém de artefactos; `PayloadArtifact` é uma referência opaca | `control-plane/orchestrator/plan/payload.go:114` | — |
| Uma tool privilegiada depois de contexto untrusted é negada ou escalada a uma pessoa | `kernel/reference-monitor/tool_destination.go`; AOS-541 | ADR-034 (destino: opção A) |
| O titular não vê media | — | ADR-042 §3, gatilho D1 |

O padrão é este: **cada capacidade entrou como caso especial.** O custo de acrescentar a próxima
cresce de forma linear, e o desenho da A5/A6 de 2026-10-22 (uma tool por modalidade) repetia-o.

## 2. Decisão

### 2.1 Invariantes que este ADR NÃO toca

- Toda a tool call passa pelo RM (ADR-002), com o token NHI do run (ADR-003).
- A execução é durável, e o replay **usa decisões e resultados gravados, nunca os recalcula**
  (ADR-010/015).
- O **SA-ROC do ADR-013**, FIXO na Carta, mantém-se. O egress externo de dados sensíveis é DANGER.
  Nenhuma classe de egress nova o contorna.
- **O untrusted não comanda** (AGENTS §7, princípio 4). Conteúdo untrusted não escolhe o modelo,
  nem o fornecedor, nem o destino, nem o custo.
- O dono assina a política e a admissão dos modelos (ADR-011). Nada auto-escrito chega à produção
  sem eval-gate (ADR-012).

### 2.2 Conteúdo genérico, nos dois sentidos

A porta do Model Gateway passa a ter uma só forma de parte não-texto:
`Part{media_type, bytes | ref(digest)}`. Os tipos de media que uma rota aceita, **na entrada e
na saída**, são declarados no perfil da rota (assinado) e deixam de ser constantes no código.
`ImagePart`/`VideoPart` passam a ser casos desta forma.

A garantia do AOS-509 mantém-se: **uma parte que a rota não declara é recusada, nunca
descartada.** A resposta «só texto» passa a «as partes que a rota declara». A porta ganha, além do
`chat`, as operações `generate` (síncrona) e `job` (assíncrona, para o vídeo), com um adaptador
por classe de API.

### 2.3 O armazém de artefactos

Os bytes produzidos ou recebidos por um run vivem num **armazém endereçado por conteúdo**: a chave
é (run, `sha256`), sem deduplicação entre runs (correcção de 2026-10-22, AOS-548). Regras:

- **Cifra por titular** — a hierarquia de chaves das capturas, a que a DSAR e a retenção cobrem
  (correcção de 2026-10-22, AOS-548: o texto original dizia «por board», mas a hierarquia que existe
  e que o crypto-shredding alcança é por titular). O crypto-shredding da DSAR e a retenção tornam
  também os artefactos ilegíveis (ADR-011).
- O Event Store guarda **só metadados**. Cada artefacto tem um evento de proveniência selado:
  run, passo, tool, modelo e fornecedor efectivos, custo, e os digests das entradas e da
  instrução.
- **Todo o artefacto é untrusted**, seja qual for o produtor.
- **Entrega ao nó seguinte**: pela saída por referência (ADR-038).
- **Entrega ao cliente**: por uma rota soberana com `Content-Disposition: attachment`, `nosniff`
  e `CSP: sandbox`. O SVG é sanitizado. O HTML fica fora até haver um caso.
- **Tectos** por artefacto, por run e por contagem. Um volume cheio falha fechado. Os valores
  fixam-se no ticket.
- **Replay**: a geração não é reprodutível, por isso o artefacto é **capturado** e o replay usa a
  captura.

### 2.4 Uma tool genérica de invocação de modelo

O efeito «chamar um modelo» deixa de exigir uma tool em Go por uso. Há **uma** implementação no
`cmd/aos` (o molde do ADR-042: `CostingToolFunc`, registo de uso `tool`, comparação do modelo
servido, custo no run, selo) e **várias configurações** no `tools.json`. Cada configuração
declara:

- os requisitos do modelo (modalidades de entrada e saída, classe de tarefa);
- a instrução, que é fixa ou um template sobre argumentos tipados;
- os tectos;
- a sensibilidade.

`ocr_read` e `video_read` passam a ser configurações desta tool.

Regras que não se negoceiam:

1. **As entradas passam por handle** (digest de artefacto do run), nunca por URL nem por texto que
   o modelo copie. É a opção A do ADR-034 (§2.6 deste ADR).
2. **O egress é externo** e o SA-ROC classifica-o pela sensibilidade efectiva dos dados, com o
   taint a elevar (ADR-013). Dados públicos ou internos seguem. Dados sensíveis são DANGER e
   pedem aprovação.
3. **O fornecedor é fixado por pedido** (ordem de fornecedores, sem fallback, ZDR quando a
   política o exige), e o fornecedor efectivo fica selado.
4. **O resultado vai para o armazém** e o modelo recebe a referência e um resumo.

### 2.5 O modelo escolhido pela tarefa, a partir de requisitos confiáveis

Cada passo (nó do plano, ou configuração de tool) declara **requisitos**: modalidades, classe de
tarefa, tecto de preço. O router do ADR-021 é estendido com **correspondência por modalidade e
classe** sobre o catálogo qualificado (§2.7), e o task-fit ganha fonte. A escolha é determinista e
fica selada no evento (modelo, fornecedor, preço, razão). **O replay usa a escolha selada.**

**Os requisitos vêm só do plano aprovado ou do objectivo confiável.** Nunca de conteúdo do
contexto untrusted.

O titular e o candidato do ADR-041 continuam a valer para o modelo do run enquanto esta fase não
entrar. Quando entrar, o ADR-041 recebe uma emenda (§6).

### 2.6 A protecção contra a injecção é a opção A do ADR-034

O gatilho do ADR-034 §5.4 («uma tool de efeito parametrizada por dados untrusted») dispara-se com
a §2.4. Por isso, a opção A deixa de ser destino e passa a ser **pré-condição** de qualquer tool
que envie conteúdo do run para fora:

- o modelo que decide vê só trusted e **handles**;
- os dados viajam por handle até à tool;
- o artefacto produzido herda o taint.

Enquanto a opção A não entrar, a `model_invoke` **só existe em configurações cuja entrada é
media semeada e cuja instrução é fixa**. É exactamente o que a `ocr_read` e a `video_read` são
hoje.

### 2.7 Catálogo qualificado em vez de uma lista de nomes

1. **Política de elegibilidade**, assinada pelo dono: fornecedores permitidos, modalidades, tecto
   de preço, exigências de retenção e ZDR.
2. Um **snapshot** do catálogo da OpenRouter (`/api/v1/models` para chat; os catálogos próprios
   de imagem e vídeo) é filtrado pela política.
3. **Só entra no catálogo servível o que tem um veredicto do banco de ensaio** para a sua classe
   de tarefa (ADR-041/042). O banco corre em lote e o dono assina **o lote**, não cada modelo.

A allowlist por nomes e os perfis por alias passam a ser **gerados** a partir do lote assinado.
O formato assinado e a âncora pinada mantêm-se.

O nó continua a não ler a OpenRouter em tempo de execução: o catálogo é um artefacto assinado,
como a allowlist é hoje.

### 2.8 O preço tem unidades

A tabela de preços passa a conhecer várias unidades: tokens (as quatro tarifas actuais), imagem,
segundo de vídeo, pedido. A reserva é o pior caso declarado. O saldo faz-se pela tabela, e o
custo reportado pelo fornecedor fica gravado como observação. Um par sem preço continua a recusar
o arranque.

### 2.9 Tools declarativas

Três fontes, todas mediadas pelo RM:

| Fonte | Regras |
|---|---|
| **MCP** | Compõe o pacote existente no nó (fecha DEF-812). **Cada servidor é um destino na allowlist de egress.** Os schemas são untrusted. Os servidores entram na cadeia de supply-chain (ADR-017). |
| **HTTP declarado** | O host fica na allowlist e a capability é parametrizada por atributos no Cedar. |
| **`code_exec`** | Corre na gVisor **sem rede**, com imagem fixada por digest. A saída é untrusted e os ficheiros vão para o armazém (é o canal do §2.3, com as mesmas defesas). |

Uma tool nova destas fontes é **configuração**: uma entrada no `tools.json` ou um servidor MCP
registado. Uma **capability** nova continua a ser uma alteração ao bundle Cedar assinado.

## 3. Alternativas consideradas

- **(A) Continuar caso a caso** (`image_generate`, `artifact_convert`, uma tool por modalidade).
  Rejeitada: é o padrão que este ADR existe para parar.
- **(B) Uma classe de egress «gateway» não privilegiada** («chamar o nosso gateway não é
  exfiltração»). Rejeitada na validação adversarial:
  - cada modelo do catálogo pode ser **outro subprocessador**;
  - o **artefacto** é um canal de fuga (SVG/PDF com links);
  - mudaria o eixo de egress do **ADR-013, que é FIXO**, e isso exige emenda da Carta (§6.5).
- **(C) Política por atributos assinada uma vez, sem banco.** Rejeitada: troca a qualificação
  medida por metadados declarados pelo vendedor. Fica a elegibilidade, mais o banco em lote.
- **(D) O nó lê o catálogo da OpenRouter em tempo de execução.** Rejeitada: é egress novo do nó,
  e o catálogo deixaria de estar assinado e de ser reproduzível.
- **(E) Um adaptador directo à OpenRouter, sem o LiteLLM.** Fica em aberto. É um destino de egress
  novo (o guard-test AOS-069 recusa-o hoje). Decide-se no ticket do §2.2, pelo resultado de um
  spike que meça se o LiteLLM fixado encaminha as APIs de imagem e de vídeo.

## 4. Consequências

**Positivas**

- Uma modalidade nova é uma entrada no perfil da rota, e não uma versão nova da porta.
- Um uso novo de um modelo como ferramenta é configuração.
- Os modelos novos entram por lote qualificado.
- O sistema produz e entrega ficheiros com proveniência, custo e apagamento.
- As peças construídas mas paradas (router, MCP) passam a ter consumidor.

**Negativas e custos**

- A superfície de ataque cresce (MCP, `code_exec`, geração). As defesas do §2.3, §2.4, §2.6 e
  §2.9 são condição de entrada de cada fase, não melhorias posteriores.
- A opção A do ADR-034 é trabalho grande (L) e fica no caminho crítico da geração.
- Os artefactos capturados custam disco, retenção e DSAR.
- O roteiro é longo, e cada fase precisa da sua prova em produção.

## 5. Resíduos declarados

1. **Troca de modelo pelo fornecedor por trás do mesmo nome.** A comparação da rota não a detecta
   (`platform/model-gateway/route.go:18-30`), e o snapshot do catálogo não versiona pesos. Fixar
   o fornecedor e a quantização por pedido reduz o risco mas não o fecha.
2. **Moderação do conteúdo gerado.** Fica com o fornecedor; o harness não modera.
3. **Jobs assíncronos** (vídeo) são pagos mesmo que o run falhe.
4. **Os `overrides` de preço da OpenRouter** tornam a reserva uma estimativa. O tecto é o pior caso
   declarado.
5. **A cadência real de modelos novos** não está medida. A urgência da fase do catálogo depende
   dela.

## 6. Relação com outros ADR

**Cada emenda escreve-se no ticket da fase que a torna verdadeira**, não agora, e responde à
objecção original:

| ADR | O que muda | Objecção original a responder |
|---|---|---|
| ADR-013, Carta | **Nada** | — |
| ADR-034 | A opção A passa de destino a pré-condição da geração (§2.6) | O custo de dois modelos e do interpretador de handles: passa a comprar a geração a partir do que se viu |
| ADR-041 | O modelo do run escolhido por requisitos (§2.5); o titular passa a omissão | D6 (um candidato, canary por plano): a escolha continua determinista e selada, e o canary mantém-se como forma de introduzir um modelo |
| ADR-042 | De «tool com nome fixo» a «tool genérica configurável» (§2.4) | §2.1 («o efeito é código do nó, não configuração»): o efeito continua a ser código (uma implementação), e só o uso passa a configuração |
| ADR-042 §3 | O titular passa a ver media (fase R1a) | D1: custo por turno, `prompt_hash`, replay. O digest entra no `prompt_hash`, os bytes resolvem-se no armazém e o custo conta pelos tokens de imagem |
| ADR-021 | Correspondência por modalidade e classe, e fonte do task-fit (DEF-280-PORTAS/NO) | Determinismo: mantém-se, com inteiros e pesos assinados |
| ADR-036 §2.8 | O perfil declara as partes aceites na entrada e na saída | AOS-509: o que não for declarado continua a ser recusado |
| ADR-018 | **Nada.** Os sub-agentes continuam no `aos-orq` | — |

## 7. Roteiro (aceite pelo dono a 2026-10-22)

| Fase | Conteúdo | Pré-condição | Tamanho |
|---|---|---|---|
| R0 | Este ADR | — | S |
| R1a | Parte genérica **de entrada**; o titular vê media com o perfil `imagem` qualificado no banco e com replay pelo digest | Ratificação deste ADR | M |
| R1b | Armazém de artefactos; `artifact_write`/`artifact_read`; rota soberana de entrega | R1a | M |
| R2 | Opção A do ADR-034: entradas por handle e taint do artefacto | R1b | L |
| R3 | `model_invoke` genérica; partes de **saída**; preço por imagem; fornecedor fixado e selado; `ocr_read`/`video_read` como configuração | R2; spike do LiteLLM (§3 E) | L |
| R4 | Catálogo qualificado (snapshot + política + banco em lote) e router por requisitos | R3 | L |
| R5 | `code_exec` na gVisor; MCP composto no nó | R2 | L |
| R6 | Vídeo gerado (API assíncrona) | R3; um caso real | L |

O caso «foto → cômodos» fica entregue em R1a + R1b (lista e planta). As imagens geradas dos
cômodos chegam em R3.

## 8. Conformidade / enforcement

Cada fase prova-se com testes e em produção, como as anteriores. Os guard-tests que este ADR exige
nascem com a fase que os torna verdadeiros:

- uma parte não declarada é recusada (R1a, R3);
- o artefacto é sempre untrusted (R1b);
- a entrega leva `attachment` e `CSP: sandbox` (R1b);
- a `model_invoke` não aceita URLs nem texto livre como entrada de media (R3);
- os requisitos do modelo nunca vêm de um segmento untrusted (R4);
- cada servidor MCP está na allowlist de egress (R5);
- o `code_exec` não tem rede (R5).

## 9. Referências

- `docs/reports/analise-critica-harness-flexivel-2026-10-22.md` (v2, com a validação adversarial)
- `docs/reports/desenho-a5-a6-multimodal-2026-10-14.md`
- `specs/00_AOS_Carta.md` §4.1 e §6.5
- OpenRouter: `GET /api/v1/models`; geração de imagem; geração de vídeo (documentação lida a
  2026-10-22, não medida)
