// Package engine é a PORTA DE EXECUÇÃO DURÁVEL do Agent Runtime (AOS-022, fase
// feature), agnóstica ao backend — o RT programa contra ela sem saber qual substrato
// a implementa (Princípio 8 / anti lock-in; ADR-015 ratificado).
//
// # O que este pacote entrega
//
//   - [Engine] — a INTERFACE (porta) que expõe as operações do contrato de durable
//     execution independentemente do backend: Dispatch (efeito idempotente, mediado e
//     registado), Checkpoint/Resume (cursor intra-iteração), Replay (replay
//     determinístico) e Mode. As assinaturas seguem EXACTAMENTE as APIs de
//     AOS-014/015/016/021 — a porta é uma composição, não uma API nova.
//   - [OwnContractEngine] — o ADAPTADOR DE REFERÊNCIA que satisfaz [Engine]
//     COMPONDO as peças já Done do contrato próprio (activity.Dispatcher, step-ledger,
//     checkpoint/resume e replay), todas sobre o MESMO Event Store replicado
//     (ADR-007). Não reimplementa nenhuma garantia.
//
// # A decisão que este pacote materializa (ADR-015)
//
// AOS-022 não pergunta SE há durable execution (ADR-001 fixa-a como primitivo) — mas
// QUE substrato a implementa. O spike concluiu, e o ADR-015 ratificou, CONSOLIDAR O
// CONTRATO PRÓPRIO: ele já satisfaz ADR-001 com evidência testada (replay 100 %,
// 0 efeitos duplicados), é o único que honra ADR-007 (uma só fonte de verdade — os
// engines externos trariam um segundo log de durabilidade), tem zero lock-in e zero
// infra adicional. Um engine externo (Temporal/Restate/DBOS) fica como BACKEND
// PLUGÁVEL opcional, não um rewrite — a porta [Engine] preserva essa reversibilidade.
//
// # Porque um subpacote próprio (e não `durable`)
//
// A porta [Engine] refere tipos de activity (AOS-021) e replay (AOS-016), e ambos os
// pacotes importam `durable` (AOS-014/015). Colocar a porta em `durable` criaria um
// ciclo de importação. O subpacote `engine` importa `durable`, `activity` e `replay`
// (nenhum deles importa `engine`) — a composição é acíclica por construção.
//
// # Isolamento por contrato — a prova (Princípio 8)
//
// O teste de contrato (ver engine_contract_test.go) corre o CENÁRIO DE REFERÊNCIA
// (run multi-passo com crash e retoma) sobre a porta e prova:
//
//   - IDEMPOTÊNCIA (AOS-014): re-despachar os passos aplicados após um crash — com um
//     WORKER NOVO que reconstrói o ledger do log ([durable.StepLedger.Rebuild] +
//     [WithLedger]) — produz 0 efeitos externos duplicados;
//   - REPLAY (AOS-016): [Engine.Replay] reconstrói a trajectória com fidelidade 100 %
//     e ZERO efeitos externos, e localiza a divergência quando o código evolui;
//   - ISOLAMENTO: o MESMO driver de RT, escrito só contra [Engine], corre com
//     asserções idênticas sobre o [OwnContractEngine] E sobre um stub/fake — trocar o
//     backend não altera a API nem o uso do RT.
//
// # Como um backend EXTERNO implementaria a MESMA porta (mapeamento; não implementado)
//
//	Operação    | Temporal                   | Restate                 | DBOS
//	------------|----------------------------|-------------------------|----------------------
//	Dispatch    | Activity idempotente        | Handler c/ idem. key    | @step transaccional
//	            | (activity-id = run:step)   | (journal + dedup)       | (registo em Postgres)
//	Checkpoint  | Event history (implícito)   | Journal da invocation   | Estado do workflow
//	Resume      | Replay do workflow          | Recuperação do journal  | Recovery do workflow
//	Replay      | Replayer do SDK             | Re-execução do journal   | Time-travel do estado
//
// Em TODOS os casos o RT continuaria a chamar apenas os métodos de [Engine]; o
// adaptador externo subordinaria o seu log ao Event Store (ADR-007) — a fronteira que
// o ADR-015 documenta como custo de adoptar um engine externo.
//
// # Fronteiras (abertas; herdadas do ADR-015)
//
//   - enforcement de fencing POR-ESCRITA exige o ES fencing-aware (AOS-018, aberto);
//   - adopção do Dispatcher/Engine PELO LOOP é wiring diferido (como em AOS-021);
//   - HA de produção depende do ES replicado real (NATS/JetStream), validado em staging.
//
// Não quebra AOS-013..021: o pacote é ADITIVO (nenhuma alteração de API a montante).
package engine
