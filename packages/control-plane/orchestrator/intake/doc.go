// Package intake classifica um Goal de meta-nível como rota `meta` (planeamento
// + gate) ou `simple` (tarefa de 1 nó), de forma DETERMINÍSTICA e SEM LLM, e
// modela a invariante de NÃO-BYPASS (tecnica/18 §3.5; AOS-233).
//
// Três propriedades sustentam este pacote:
//
//  1. Classificação é ROUTING, não autoridade. [Classify] é uma função PURA sobre
//     os campos DECLARATIVOS do Goal ([Signals]) — `intake_mode` explícito do
//     principal, orçamento raiz face ao limiar do tenant, tectos estruturais
//     pedidos, cardinalidade de papéis. O TEXTO do `objective` NUNCA é input:
//     [Signals] nem sequer tem um campo para ele, pelo que injecção no objectivo
//     não pode trocar a rota (imunidade por construção, não por vigilância).
//
//  2. Ambiguidade ⇒ META (fail-safe para supervisão). Na ausência de um sinal
//     POSITIVO de simplicidade (modo explícito `simple` ou exactamente um papel),
//     e sem nenhum sinal de meta, a rota é `meta`: prefere-se sempre a supervisão
//     humana à optimização de custo. Um sinal de meta (orçamento acima do limiar,
//     tecto pedido, multi-papel) força `meta` mesmo perante um `intake_mode`
//     explícito `simple` — é essa a defesa contra o *gaming* do classificador.
//
//  3. Invariante de NÃO-BYPASS (ADR-013). Um run classificado `simple` é 1-nó por
//     construção e NÃO traz plano pré-aprovado. Qualquer tentativa de delegação
//     desse run REENTRA no gate por-spawn ao nível L0–L5 do chamador — um "plano
//     just-in-time" do subgrafo. [DelegationGuard] modela esse ponto de reentrada:
//     não existe caminho de código de uma classificação `simple` para um spawn que
//     salte a consulta ao [SpawnGate]. Manipular o classificador para `simple`
//     portanto NÃO escapa ao gate.
//
// O determinismo torna a classificação replayable: [Classify] não faz I/O, não lê
// relógio nem estado vivo — o limiar do tenant entra como ARGUMENTO ([TenantPolicy],
// um snapshot), nunca por lookup. Mesmo input ⇒ mesmo veredicto. O evento
// `plan.intake_classified` é emitido reutilizando a constante e o payload de
// orchestrator/plannerevents — este pacote NÃO declara um tipo de evento novo.
package intake
