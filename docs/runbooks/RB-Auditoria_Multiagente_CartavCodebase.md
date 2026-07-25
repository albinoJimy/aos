# Runbook — Auditoria Multiagente "Carta ↔ Codebase"

| Campo | Valor |
|---|---|
| Runbook | `docs/runbooks/RB-Auditoria_Multiagente_CartavCodebase.md` |
| Versão | 1.0 |
| Data | 2026-07-24 |
| Idioma | PT-PT |

## 1. Propósito

Este runbook descreve como correr uma auditoria adversarial multi-perspectiva ao alinhamento entre a **documentação congelada** (Carta, System Spec, ADRs, Standards) e o **código/infra real** do AOS.

Diferente das auditorias puramente documentais (ex.: `analises/00_Relatorio_Auditoria.md`), este workflow cruza afirmações da doc com evidências no codebase — linha a linha, gate a gate — usando agentes especializados que se desafiam mutuamente.

## 2. Quando usar

- Antes de declarar uma milestone (ex.: "v1 feita" contra o DoD da Carta §5).
- Após grandes refactorings que toquem fronteiras canónicas, ADRs ou composition-root.
- Quando surgirem divergências entre `specs/`, `tecnica/` e o estado real de `packages/`/`infra/`.
- Como gate de governação antes de merge de EPICs que alterem a forma do produto.

## 3. Pré-requisitos

- Estado do repo pinado (commit hash + nota sobre alterações por commitar).
- Documentos de âncora identificados: Carta, System Spec §13, ADRs FIXOS, `_BRIEF`, `AGENTS.md`.
- Infraestrutura de subagentes disponível (capacidade de lançar N agentes read-only em paralelo).

## 4. Fases do workflow

### Fase 0 — Fixar âncoras e escopo

1. Definir o commit/árvore a auditar.
2. Compilar a lista curta de **factos inegociáveis**:
   - três fundações (RM mandatório, NHI scoped/time-bound, execução durável ao nível do passo);
   - 10 princípios não-negociáveis (`AGENTS.md` §7);
   - decisões FIXAS da Carta §4.1/§4.2;
   - entrypoints canónicos (`integration.NewSecuredRuntime`, `referencemonitor.NewProductionSecure`, `cmd/aos`, `cmd/aos-demo`).
3. Listar os documentos e directorias em scope.

### Fase 1 — Avaliação paralela por agentes especializados

Lançar 12 agentes read-only simultaneamente, cada um com um mandato estreito.

| Agente | Perspectiva | Foco |
|---|---|---|
| ARQ | Arquitecto de Plataforma | Invariantes de camada, sentido de dependências, entrypoints canónicos |
| SEC | Responsável de Segurança | Os 10 princípios não-negociáveis no código vs na doc |
| DUR | Engenheiro de Execução Durável | Idempotência `f(run_id,step_id)`, replay, checkpoints, activities |
| COM | Revisor de Completude | Tickets "Done" e capacidades do System Spec com código+testes |
| COE | Revisor de Coerência Interna | Contradições entre Carta, System Spec, ADRs, `tecnica/`, `AGENTS.md` |
| RAS | Revisor de Rastreabilidade | Cadeia ticket→ADR→código→teste→gate; matrizes de rastreio |
| VIA | Engenheiro de Viabilidade | `Makefile`, `scripts/ci/`, Dockerfile, IaC dev offline |
| SUP | Especialista Supply-Chain | ADR-017: zero-dep do nó, SBOM, govulncheck baseline |
| OBS | Observabilidade & Evals | Spans GenAI, `otel-genai`, eval-gate, golden-sets |
| GOV | Governação & Conformidade | Cedar PDP, bundle assinado, soberania regional, D6/D7 |
| ADV | Agente Adversarial (red team) | Tentar falsificar as afirmações mais fortes da doc |
| UXD | UX/DX | Um agente novo consegue executar um ticket só com a doc? |

**Contrato de saída obrigatório** para cada achado:

```
ACHADO <LETRA>-NN: <título>
  Severidade: CRÍTICO|ALTO|MÉDIO|BAIXO
  Afirmação na doc: <citação + ficheiro:linha>
  Realidade no código: <evidência + ficheiro:linha (ou comando)>
  Veredito: CONFIRMADO | SOBRE-REIVINDICADO | AUSENTE | CONTRADITÓRIO
```

Terminar com:

```
VEREDITO <dimensão>: VERDE|AMARELO|VERMELHO — <justificação>
```

### Fase 2 — Contra-exame

4 agentes fiscais adversariais re-avaliam os achados ALTO/CRÍTICO de grupos cruzados:

- **CROSS-ARQ:** ARQ + SUP + VIA (fronteiras, supply-chain, executabilidade)
- **CROSS-SEC:** SEC + GOV + ADV (modelo de ameaça, fail-closed)
- **CROSS-DUR:** DUR + OBS (durabilidade e observabilidade)
- **CROSS-DOC:** COE + RAS + UXD + COM (coerência, rastreabilidade, completude)

Cada fiscal decide **CONFIRMADO / REFUTADO / RECLASSIFICADO**, com base em:
- emendas datadas da Carta §7;
- excepções documentadas (ex.: ADR-018, emenda 1.3 do D4);
- distinção componente entregue vs wiring no nó;
- verificação directa das citações.

### Fase 3 — Síntese e relatório final

A cadeira (operador humano ou agente síntese) produz:

1. `analises/phase1_findings_normalized.md` — achados normalizados numa tabela por dimensão.
2. `analises/07_Relatorio_Auditoria_Multiagente.md` (ou próximo número disponível) — relatório com:
   - enquadramento/método;
   - sumário executivo e veredicto global;
   - veredictos por dimensão;
   - achados críticos/altos consolidados;
   - refutações do contra-exame;
   - implicações para o DoD da v1;
   - recomendações priorizadas.

## 5. Critérios de veredicto

| Veredicto | Significado |
|---|---|
| **VERDE** | A doc casa com o código; os critérios da dimensão estão satisfeitos. |
| **AMARELO** | Existem gaps, mas são dívida declarada, inofensivos ou não bloqueiam o DoD. |
| **VERMELHO** | Há contradições reais ou ausências que impedem declarar o DoD/versão satisfeitos. |

A regra global é **fail-closed**: se qualquer dimensão crítica (ARQ, SEC, GOV, DUR, RAS) estiver VERMELHA, ou se um critério do DoD da Carta §5 não for cumprido, o veredicto global é **VERMELHO**.

## 6. Integração com o processo de emendas da Carta

- Descobrir dívida escondida → registar como **emenda** na Carta §7 (se tocar decisão FIXA) ou abrir ticket `AOS-NNN`.
- Reabrir decisão FIXA sem emenda → recusar; usar o mecanismo do árbitro da Carta §6.5.
- Contradição entre documentos subordinados → corrigir o documento de menor hierarquia; se contrariar a Carta, é emenda.

## 7. Exemplo de aplicação

A execução de 2026-07-24 auditou o working tree do AOS e concluiu:

- **Veredicto global: VERMELHO.**
- Bibliotecas nucleares fortes, mas o nó `aos` não monta durabilidade, PDP/policy real, identidade real, redacção de PII nem eval-gate de admissão.
- Documentação primária (`_BRIEF.md`, `AGENTS.md`, `specs/INDICE.md`, `packages/README.md`) desactualizada face aos 16 epics e AOS-177 reais.

Relatórios geridos:

- `analises/phase1_findings_normalized.md` (62 achados)
- `analises/07_Relatorio_Auditoria_Multiagente.md`

## 8. Notas operacionais

- Os agentes de Fase 1 e 2 devem ser **read-only**; nenhum altera código.
- Toda a evidência deve vir acompanhada de `caminho:linha` ou comando reproduzível. Sem evidência, o achado é descartado.
- Não confiar em contagens ou nomes de documentos sem verificar a árvore real.
- Quando um achado depende de interpretação de "Done" num epic, aplicar a distinção **componente entregue vs wiring no nó**.

---

*Runbook gerado a partir da execução real do workflow em 2026-07-24. Manter actualizado quando as decisões canónicas ou a estrutura do repo mudarem.*