// Package budget implementa o orçamento hierárquico com reserva atómica
// (compare-and-swap) do AOS (AOS-008, ADR-008): o admission control que
// substitui o cap de delegação fixo e o contador partilhado com corrida por um
// orçamento por ÁRVORE de execução, denominado em tokens E custo ($), com débito
// reservado antes de cada spawn/tool call.
//
// # Modelo
//
// O orçamento é uma árvore de nós ([node]): um nó raiz (tree_id) e sub-árvores
// (parent_id). Cada nó tem um Limite e dois contadores — Reserved e Committed. O
// headroom de um nó é Available = Limit − Reserved − Committed, avaliado nas DUAS
// dimensões (tokens E custo): só há headroom se a reserva couber em ambas. Uma
// reserva numa sub-árvore consome headroom em TODOS os ancestrais até à raiz
// (admissão hierárquica) — a invariante soma-dos-filhos ≤ pai é estrutural.
//
// # Denominação inteira
//
// [Amount] é {Tokens, CostMicroUSD} em INTEIROS (micro-dólares), nunca float: a
// comparação é exacta e livre de corrida/arredondamento. Orçamento em iterações
// é um proxy péssimo (uma iteração arrasta 200 k tokens) e não é usado.
//
// # Reserva atómica (CAS) e rollback
//
//   - [Budget.Reserve] sobe a cadeia de ancestrais debitando Reserved em cada
//     nível SÓ se houver headroom nas duas dimensões. O check-and-débito de cada
//     nó é atómico (mutex por nó com verificação-e-débito indivisível); dois
//     Reserve concorrentes NUNCA ultrapassam o limite de nenhum nível (0
//     overshoot). Se QUALQUER nível falhar, os níveis já reservados são
//     revertidos (rollback parcial) e devolve-se [ErrNoHeadroom] (deny).
//   - [Budget.Commit] converte Reserved→Committed em todos os níveis (débito
//     final); idempotente por reservation.
//   - [Budget.Release] devolve Reserved a Available (rollback) em todos os
//     níveis; idempotente. Uma reserva é commit OU release exactamente uma vez;
//     commit-após-release e release-após-commit devolvem erro. Reserva não
//     consumida em falha/cancelamento é libertada sem LEAK.
//
// # In-memory vs distribuído
//
// Esta é a implementação de REFERÊNCIA in-memory com CAS real, que torna o
// 0-overshoot determinístico e testável sob -race. Em produção o backend é um
// token-bucket distribuído (Redis/consenso) sobre o TPM/RPM real do provider
// (ADR-008); o seam [Reserver] é o ponto de troca. O estado in-memory é o
// caminho rápido; os eventos do Event Store (budget.reserved/committed/released)
// são o log durável e autoritativo — [Rebuild] reconstrói os contadores a partir
// deles (consistência com AOS-002).
//
// # Integração
//
// O adaptador [BudgetCheck] implementa o hook "budget" do Reference Monitor
// (AOS-003): estima o custo do Call e RESERVA headroom; sem headroom → HookDeny
// (fail-closed) e o RM audita a negação. O consumidor confirma (commit) em
// permit ou liberta (release) em deny/falha. Um circuit breaker leve nega em
// trip de custo/token (o detalhe completo é EPIC-08).
//
// Âmbito (AOS-008). Orçamento hierárquico + reserve/commit/release CAS +
// BudgetCheck do RM + reconstrução por eventos. Fora de âmbito: scheduler e
// backpressure completos (AOS-025+), token-bucket distribuído real, e o detalhe
// do circuit breaker (EPIC-08).
package budget
