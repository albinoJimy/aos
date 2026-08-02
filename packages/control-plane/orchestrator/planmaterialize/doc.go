// Package planmaterialize materializa um PlanDocument APROVADO no grafo de tarefas
// (AOS-237, EPIC-19, tecnica/18 §4.1/§6.1): o passo `plan.approved` → DAG + spawns
// delegados. É a fronteira onde o organigrama declarativo vira execução — e SÓ ele.
//
// FRONTEIRA DE CONFIANÇA (ADR-005, tecnica/18 §3.1). O materializador consome o
// documento APROVADO gravado no log (o input do restante run), NUNCA a saída crua
// do LLM. O plano é dados: aqui não se re-chama o modelo, não se re-valida grafo
// (AOS-231) nem se re-deriva risco/orçamento (AOS-232) — assume-se já aprovado. O
// que este pacote faz é DETERMINÍSTICO a partir do documento gravado (§3.6): o mesmo
// documento produz a mesma sequência de efeitos (spawns/nós) e o mesmo
// `plan.materialized`.
//
// TRÊS EFEITOS, POR NÓ (tecnica/18 §6.1, linha da tabela `plan.materialized`):
//
//  1. um node_id FOLHA  → um nó-tarefa `task.node.created` (AOS-025), via a porta
//     [LeafAdmitter] (o composition root liga-a a *orchestrator.GraphBuilder);
//  2. um PAPEL-QUE-EXPANDE → um `Delegator.Spawn` (AOS-026), via a porta [Spawner]
//     (ligada a *orchestrator.Delegator), com `tools[]` a VINCULAR o `Authority[]`
//     da NHI filha (issuer_child) — a autoridade da filha é LIMITADA às tools do
//     papel (ver [Materializer.authorityForNode] e a política de mapeamento em
//     [CapabilityMapper]);
//  3. o resultado projecta-se em `plan.materialized` (constante EXISTENTE
//     [plannerevents.EventMaterialized], via [MaterializeRecorder]).
//
// ADMISSÃO GLOBAL POR NÓ (AOS-027/028). Nenhum nó materializa sem admissão global
// (escalonador). Como o escalonador PODE não estar no módulo, a admissão é uma
// PORTA ([Admission]) que o wiring liga; a materialização é DUAS FASES: admite-se
// TODOS os nós primeiro (fail-closed — uma única negação aborta antes de qualquer
// spawn/nó, zero efeitos parciais) e só então se materializa.
//
// FRONTEIRAS HONESTAS (§5). Nenhuma é fail-open, escalada de privilégio nem
// over-claim de AC/DoD — são limites de escopo declarados:
//
//   - Marcador folha-vs-papel: o schema do PlanDocument é CONGELADO (AOS-230) e NÃO
//     o carrega; a classificação é uma POLÍTICA declarada ([DefaultClassifier]) que
//     o wiring pode substituir (ex.: metadados de papel do REG).
//   - Folha multi-tool: contract.TaskSpec (AOS-025, irmão congelado) é single-tool,
//     pelo que o nó-folha do DAG leva só a PRIMEIRA tool (ordem canónica). Sem perda
//     de autoridade: o conjunto coarse COMPLETO fica em plan.materialized.Nodes[].
//     Tools (o registo autoritativo). Ver [graphLeafAdmitter] em adapters.go.
//   - Orçamento achatado: todo o spawn de papel pende do nó de orçamento RAIZ do run;
//     o aninhamento papel-sob-papel e a consolidação da reserva de cada spawn (Finish
//     do [Delegator]) pertencem ao ciclo-de-vida/despacho (AOS-238), a JUSANTE. O
//     clamp de AUTORIDADE por-nó não é afectado.
//   - Despacho dos nós prontos (AOS-027/028 a jusante) é do ciclo-de-vida do run —
//     fora de AOS-237. Ver os adaptadores em adapters.go.
package planmaterialize
