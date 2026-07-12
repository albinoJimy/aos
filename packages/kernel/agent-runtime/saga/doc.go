// Package saga implementa a SAGA DE COMPENSAÇÃO do Agent Runtime do AOS (AOS-020) —
// o mecanismo que DESFAZ efeitos parciais quando um passo falha depois de já ter
// tocado o mundo externo. Fecha o gap que os gates deixavam aberto: os gates
// PREVINEM efeitos indesejados, mas nada faziam quando um efeito legítimo ficava a
// meio; a saga adiciona RECUPERAÇÃO onde antes só havia prevenção (tecnica/02 §7,
// ADR-001).
//
// # Composição, não reimplementação
//
// A saga NÃO reinventa durabilidade. REUTILIZA duas fundações já Done:
//   - o step-ledger de AOS-014 ([durable.StepLedger]) para a IDEMPOTÊNCIA das
//     compensações — cada reversão corre dentro de [durable.StepLedger.Apply] com
//     uma chave de compensação DISTINTA (f(run_id, comp-step_id)); reexecutar não
//     duplica a reversão (0 efeitos de compensação duplicados);
//   - a máquina de estados de AOS-017 ([state.Machine]) para as transições duráveis
//     failed → compensating → ready, válidas e reconstruíveis por replay.
//
// # Registo de compensações
//
// Cada activity com efeito externo REVERSÍVEL regista, no momento em que aplica o
// efeito, a acção inversa associada ao seu step_id ([CompensationRegistry.Register]).
// O registo preserva a ORDEM de aplicação (a ordem de registo == ordem de aplicação),
// que é o que permite compensar por ORDEM INVERSA.
//
// # Execução: LIFO idempotente
//
// [SagaCoordinator.Compensate] executa, na entrada em compensating, as compensações
// dos passos aplicados por ORDEM INVERSA (LIFO — o último aplicado é o primeiro
// compensado). Cada compensação é envolvida no [durable.StepLedger.Apply] com a sua
// chave de compensação: a verificação already-applied PRECEDE o efeito, logo uma
// compensação já COMMITADA é DEDUPLICADA (não corre outra vez) e uma pendente corre. O
// evento step.ledger.applied de cada compensação é o seu REGISTO append-only no Event
// Store.
//
// A cardinalidade honesta é AT-LEAST-ONCE + idempotência downstream = 0 reversões
// duplicadas OBSERVÁVEIS (o mesmo contrato de AOS-014). A verificação already-applied
// assenta no COMMIT durável: um crash-before-commit (efeito aplicado, registo não
// commitado) re-corre a acção inversa na retoma, pelo que "0 duplicados observáveis" só
// se sustenta se a acção inversa for IDEMPOTENTE sobre a sua chave — pré-condição do
// chamador ([Compensation.Action]), não imposta pelo coordinator.
//
// # Crash-resume
//
// Um crash DURANTE a compensação retoma sem repetir as compensações já aplicadas nem
// saltar as pendentes: um worker novo reconstrói o estado durável — [state.Machine.Rebuild]
// dá o estado (compensating) e [durable.StepLedger.Rebuild] dá o conjunto de
// compensações já commitadas (already-applied). O coordenador reitera a mesma sequência
// LIFO; o ledger deduplica as já feitas e corre só as que faltam.
//
// # Compensação que falha (semântica honesta)
//
// Se uma compensação falha, [SagaCoordinator] faz RETRY idempotente (a chave garante
// que uma tentativa falhada — que nada commita no ledger — pode repetir sem duplicar).
// Esgotada a política de retry, a saga NÃO finge sucesso: NÃO transita para ready,
// deixa o run PRESO em compensating e ESCALA por alerta ([Observer.Escalated],
// [ErrCompensationExhausted]). A tabela de AOS-017 não tem aresta compensating → killed,
// pelo que a escalada é por alerta + paragem, nunca por uma transição forjada.
//
// # Política pós-compensação
//
// Concluída a compensação com sucesso, o run transita compensating → ready para RETRY
// LIMPO. É a única aresta de saída de compensating na tabela de AOS-017 (compensating
// → ready), pelo que "retry limpo" é a política modelada; uma desistência permanente
// exprime-se como a escalada acima (preso + alerta), não como um terminal fictício.
//
// # Observabilidade e segredos
//
// Os eventos de compensação são observáveis via [Observer]; as chaves entram sempre na
// forma OPACA (hash SHA-256, [durable.HashKey]) — nunca a chave em claro nem o payload
// da compensação (que é vazio por convenção) — honrando "sem segredos em logs".
//
// # Fora de âmbito (delegado)
//
// A durabilidade do ledger e a dedup do Event Store (AOS-014/AOS-002); a tabela de
// transições e o Rebuild da máquina (AOS-017); o fencing/lease do claim (AOS-018). A
// saga COMPÕE estas peças — não as reimplementa.
package saga
