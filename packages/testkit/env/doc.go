// Package env é o AMBIENTE EFÉMERO de teste do AOS (AOS-110, EPIC-11). Fornece um
// harness — [EphemeralEnv] — que PROVISIONA dependências frescas por execução de
// teste (Event Store, transporte push/[Bus], PDP, [FakeVault]), com um lifecycle
// DETERMINISTA (Provision → Seed → uso → Teardown) e teardown GARANTIDO mesmo em
// falha, via t.Cleanup. É a fundação reutilizável pelas suites de domínio de
// integração AOS-111..118.
//
// # Modelo de referência in-process (ZERO deps externas)
//
// A spec do EPIC-11 pede "Testcontainers ou equivalente" com dependências REAIS.
// No AOS os componentes canónicos SÃO modelos in-process, zero-dep e
// deterministas; logo o EQUIVALENTE coerente com o repo é este harness
// IN-PROCESS. Cada [EphemeralEnv] tem o SEU [eventstore.Store] fresco, a SUA
// subscrição push, o SEU FakePDP e o SEU [FakeVault] — sem estado partilhado
// entre Envs (isolamento estrutural). A variante de PRODUÇÃO (Testcontainers com
// imagens pinadas por hash, atrás da mesma API) está documentada em README.md.
//
// # Composição de AOS-109
//
// O harness COMPÕE os mocks/fixtures do [testkit] pai (AOS-109): reutiliza
// testkit.NewEventStore (o *eventstore.Store REAL), testkit.NewFakePDP,
// testkit.NewFakeBroker e as fixtures de run_id/step_id — não os reimplementa.
//
// # Critérios de aceitação (AOS-110)
//
//   - AC1 declaratividade: o teste pede as deps por options ([WithEventStore],
//     [WithBus], [WithPDP], [WithVault], [WithBroker]) e recebe-as provisionadas +
//     destruídas automaticamente.
//   - AC2 isolamento: cada [New] parte de estado LIMPO; nenhum Env observa efeitos
//     de outro (provado por um teste que corre a mesma suite duas vezes).
//   - AC3 teardown garantido: [EphemeralEnv.Teardown] corre via t.Cleanup mesmo em
//     falha/panic-recuperado, fecha o Store e cancela as subscrições push (sem
//     goroutines órfãs, provado com -race) e é idempotente.
//   - AC4 local == CI: in-process Go, sem configuração manual.
//   - AC5 seed: [EphemeralEnv.SeedTrajectory] popula o Event Store com uma
//     trajectória CONHECIDA para os testes de replay/idempotência.
package env
