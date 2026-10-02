# ADR-027 — Cada nó despachado de um plano do `aos-orq` executa como um run do nó `aos`

| Campo | Valor |
|---|---|
| **ADR** | 027 |
| **Título** | O trabalho de um nó do plano é um run do nó `aos`, submetido pelo `aos-orq` por `POST /runs` e acompanhado por `GET /runs/{id}`; a conclusão e o veredicto voltam ao log do plano escritos pelo `aos-orq` sob a posse do run |
| **Estado** | **Aceite** — decidido pelo dono do produto a 2026-09-19 (AOS-413) |
| **Data** | 2026-09-19 |
| **Deciders** | Executor de AOS-413 (proposta) · **Dono do produto (decisão, 2026-09-19)** |
| **Contexto-fonte** | `specs/EPIC-19_Planeador_Meta_Orquestracao.md` §AOS-413; `packages/cmd/aos-orq/dispatch_wiring.go`; `packages/cmd/aos/api.go` (`POST /runs`, `GET /runs/{id}`); `packages/control-plane/orchestrator/graph.go`; `packages/control-plane/runlifecycle/emitters.go` |
| **ADRs relacionados** | **ADR-018** (fronteira nó↔ORQ/SCH — mantém-se intacta), **ADR-023** (escritor único por run — o `aos-orq` escreve o estado dos nós do SEU run; o nó escreve o ciclo de vida dos runs FILHOS), **ADR-024** (o despacho é o efeito governado — este ADR diz o que o efeito É), ADR-006 (domínios de confiança da identidade) |
| **Supersede** | — (completa o ADR-024) |

---

## 1. Contexto

O ADR-024 pôs o efeito no despacho, mas o efeito ficou por definir: despachar um nó é cunhar a NHI
(se for papel) e `MarkRunning`. Nada executa o trabalho do nó nem o conclui. Em produção (v0.1.23,
run `run-aos412-vivo-1`) o organigrama aprovado parou no primeiro nó — `running` para sempre, sem
veredicto do verificador, com o nó de risco à espera de uma condição que nunca se avalia.

O lado de leitura já existe: o despacho lê o estado dos nós (`task.node.state_changed`), os
veredictos (`plan.verdict_recorded`) e os payloads (`plan.payload_published`). Faltam os produtores e
o trabalho do nó.

## 2. Decisão

### 2.1 Onde corre o trabalho

**Cada nó despachado — folha ou papel — é um run do nó `aos`.** O `aos-orq` submete-o por
`POST /runs` e acompanha-o por `GET /runs/{id}`. As tool calls do nó passam pelo mesmo Reference
Monitor, pela mesma PDP e pela mesma sandbox que qualquer run de produção.

- O **id do run filho** é `<run>~<node_id>`. Não `/` (o `/runs/{id}` casa um só segmento) nem `.`
  ou `:`, que a gramática de `node_id` admite — com eles, o run `a` + nó `b.c` e o run `a.b` + nó
  `c` davam o mesmo id. O `~` não é carácter de `node_id` e o `serve` recusa um run que o contenha.
- Um **409** na submissão é um run que ESTE plano não criou (o executor só submete nós pendentes):
  recusa-se, em vez de aceitar o desfecho — e o veredicto — de um run alheio.
- Um run só **conclui** com `completed`, `terminated` e sem erro: um run que parou por orçamento
  ou turnos responde `completed` com `terminated=false`, e é trabalho a meio.
- Um **papel** também trabalha. No organigrama, papel é só o nó de que outros dependem
  (`DefaultClassifier`); o spawn da NHI fica como o registo da delegação (ADR-024), e o trabalho é um
  run como o de uma folha.

### 2.2 Credencial (decisão do dono)

O nó continua a confiar num **só** emissor de NHI.

- **O NHI do run é cunhado pelo operador** com o `aos-issuer`, na cerimónia do plano: tools do plano,
  `model:invoke` e o board. O `aos-orq` lê-o de um **ficheiro montado** e envia-o no campo
  `credential` de cada submissão.
- **O Bearer** vem do `client_credentials` do IdP, com o segredo do cliente num ficheiro montado,
  pedido de novo em cada chamada (um token com `jti` só se aceita uma vez).
- **Rejeitado:** o nó confiar na chave do `aos-orq`. Daria ao orquestrador o poder de cunhar
  qualquer autoridade para o nó e desfazia a separação de domínios de confiança (ADR-006).
- **Tecto:** a validade do NHI (45 minutos) limita a duração de um plano. Um plano que não acabe a
  tempo sai com código próprio e retoma-se com um NHI novo; a renovação fica como resíduo declarado.

### 2.3 Restrição às tools do nó (decisão do dono)

**O `POST /runs` ganha um campo `tools`**: a lista-branca dos nomes de tool que o run pode chamar.
O ciclo do runtime impõe-na em cada tool call **antes** da mediação — uma tool fora dela é negada
sem chegar ao Reference Monitor, que continua a decidir tudo o resto. O `aos-orq` envia as tools
pinadas do nó no `plan.materialized` — as mesmas que o clamp da materialização decidiu. Sem isto, o
clamp seria decorativo: o run teria as tools do run inteiro.

**Ausente e vazia não são o mesmo.** Ausente (`null`) ⇒ sem restrição além do token — os clientes
actuais não mudam. Presente e vazia (`[]`) ⇒ nenhuma tool: é o nó do plano sem tools pinadas, que de
outro modo herdaria as do NHI do run inteiro, incluindo as de risco de outros nós. A distinção
sobrevive à retoma do run no nó.

O `scope` existente fica como está: os clientes actuais já o enviam com capabilities.

### 2.4 Resultado, veredicto e payloads

- **Conclusão:** um run filho em estado terminal leva o nó a `complete` (run `completed`,
  `terminated` e sem erro) ou a `failed` (tudo o resto), por uma transição durável `running→complete|failed` escrita pelo
  `aos-orq` sob o lease do run (ADR-023).
- **Veredicto de um `verifier`:** lê-se da saída final do run por uma gramática **fechada**
  (`{"outcome":"pass|fail","reasons":[<identificador>...]}`). Tudo o que não se ler — saída livre,
  JSON inválido, run falhado — é **`fail`** com a razão `verdict_unparseable`. Os `subjects` vêm do
  plano (as arestas de entrada do verificador), nunca do modelo.
- **Payloads (EMENDADO a 2026-09-20 pelo AOS-414, opção (A) do dono).** A versão original deste
  ADR não publicava nem transportava payloads, e a validação em produção mediu o custo: o
  verificador reprovou com `documento_nao_fornecido`. Agora:
  - o `POST /runs` tem um canal de ENTRADA próprio (`inputs`), distinto do objectivo trusted: o
    conteúdo entra no tail como segmento `plan_input`, marcado `taint=untrusted`, com a
    proveniência (nó de origem, output, digest) nos RÓTULOS da linha de delimitação, que é
    inforjável — nunca no corpo;
  - o nó VERIFICA o digest do que recebe (integridade do que o plano publicou; não é confiança);
  - o `aos-orq` publica `plan.payload_published` por contrato cumprido — referência (run filho +
    digest) nas formas abertas, forma fechada validada no veredicto — e entrega a cada nó só o
    que o `consumes` DELE declara;
  - **o conteúdo vive na MEMÓRIA do `serve`** (decisão (A)): no log fica a referência. As
    alternativas rejeitadas eram guardar saída de modelo em claro no WAL do orquestrador — fora da
    cifra por-titular do nó — ou não transportar nada, que só serve para produtos endereçáveis;
  - **um contrato que não pode ser cumprido fecha o CONSUMIDOR, não o `serve`.** O nó consumidor
    vai a `failed`, com a razão visível, e o plano termina; os seus dependentes são podados pelas
    regras normais. Abortar o `serve` — a primeira versão — deixava os irmãos em voo por recolher
    e repetia-se em todas as retomas, num plano que nunca acabava. Os casos são três: o produtor
    concluiu noutro `serve` (conteúdo perdido), o contrato não é publicável (`metrics`, que
    exigiria números que ninguém mediu), ou a saída passou o tecto;
  - **tectos**: 128 KiB por payload e 512 KiB no conjunto, abaixo do tecto do corpo do
    `POST /runs` — um payload maior não se transporta, e o contrato fica por cumprir;
  - **um nó com mais do que um contrato de forma ABERTA não publica nenhum**: um run devolve UMA
    saída final, e atribuí-la a dois nomes publicaria bytes iguais sob tipos diferentes — o tipo
    que o validador impõe na admissão deixaria de significar o que diz;
  - **o digest é um controlo de INTEGRIDADE do transporte**, não uma prova de origem: quem o
    calcula e quem o envia são o mesmo processo. A proveniência viaja nos rótulos, fora do digest.
  - **Não fecha a separação de planos (DEF-806/AOS-069):** o conteúdo untrusted passa a ter canal
    próprio e marcado, mas continua a ser lido pelo MESMO plano que planeia.
  - **O `plandispatch.PayloadResolver` continua sem chamador de produção:** a entrega lê o mapa em
    memória deste processo, e não a projecção das referências publicadas. A re-verificação de
    tipo/taint/`contract_digest` que o resolver faria fica como defesa-em-profundidade por ligar.

### 2.5 O `serve` espera

O laço de despacho deixa de parar no primeiro ponto fixo. Enquanto houver nós em voo, espera pela
conclusão (por sondagem), escreve-a, liberta o headroom e volta a passar. Termina quando o plano
chega a estado terminal. Se o tempo acabar com nós em voo, sai com um código próprio, e uma nova
invocação retoma os nós `running`.

## 3. Alternativas rejeitadas

- **(B) O `aos-orq` corre o ciclo de modelo em processo.** Um segundo sítio a executar tools, fora
  da sandbox e com um RM sem PDP.
- **(C) Emendar o ADR-018 para o nó `aos` hospedar o multi-nó.** Reabre a decisão de uma só
  autoridade sobre o ciclo de vida de um run.

## 4. Consequências

- **O nó `aos` muda**, mas só em identidade e em restrição de tools: o campo `tools` e a sua
  imposição no RM. Nenhum import do orquestrador ou do scheduler — o `boundary_orq_sch_test.go`
  continua a valer.
- O `aos-orq` ganha um cliente HTTP autenticado, dois ficheiros montados (NHI e segredo do cliente)
  e um laço de espera com prazo.
- O trabalho de um nó continua limitado às tools registadas: sem skills, o objectivo do nó é o
  prompt e as tools pinadas são o que ele pode fazer (lacuna declarada no `tecnica/18`).
- **Resíduos:** a separação de planos (DEF-806/AOS-069), que o canal de entrada do AOS-414 NÃO
  fecha; o conteúdo dos payloads preso à vida do `serve` (decisão (A)); contratos `metrics` e
  segundos contratos de forma aberta que ficam por cumprir; o `PayloadResolver` por ligar; a
  renovação do NHI para planos longos; a colisão do id do run filho num nó SEM gate soberano, que responde 201 em vez de 409 e não
  é detectável (fora de produção); o texto final de um run filho vive na
  memória do nó, pelo que um reinício do nó entre a conclusão e a leitura perde-o (o estado durável
  sobrevive e o nó conta como `failed` se não houver saída legível).
