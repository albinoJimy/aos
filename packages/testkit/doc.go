// Package testkit é o framework de testes unit/integração de referência do AOS
// (AOS-109, EPIC-11). Consolida num único módulo OPT-IN as fixtures deterministas
// e os mocks/stubs de referência dos cinco componentes canónicos que a maioria
// dos tickets toca — Reference Monitor (RM), Policy Decision Point (PDP), Event
// Store (ES), Model Gateway (GW) e Credential Broker (BRK) — com os contratos
// alinhados ao catálogo do _BRIEF §2.
//
// # Porquê um testkit
//
// A Definition of Done do backlog (specs/01 §3) exige testes verdes e cobertura
// que não regride, e o gate 3 do pipeline (specs/01 §4) corre a suite unit por
// módulo. Antes deste módulo, cada suite reinventava os seus fakes (fakeSink,
// spyHook, stubDecider, FakeAdapter, FakeBroker...) e as suas fixtures de
// run_id/step_id. O testkit PROMOVE esses padrões dispersos a tipos EXPORTADOS
// partilhados, deterministas e testados (canário -race), para que escrever um
// teste de domínio (idempotência, replay, política) seja compor fixtures em vez
// de as improvisar.
//
// # Determinismo (sem flakiness)
//
// Todas as fixtures isolam as três fontes de não-determinismo: TEMPO ([FixedClock],
// [ManualClock]), ALEATORIEDADE (IDs sequenciais via [SeqIDGen], nunca UUID/rand) e
// I/O (o Event Store é in-memory, [NewEventStore]). Os mocks são funções puras da
// sua configuração — a mesma entrada produz sempre a mesma decisão.
//
// # Layering LEVE
//
// O testkit importa apenas os contratos LEVES/FOLHA reais (eventstore=folha;
// reference-monitor e agent-runtime, ambos zero-dep). Para PDP/GW/BRK — cujos
// contratos reais arrastam cadeias pesadas ou dependências externas (cedar-go) —
// o testkit define INTERFACES ALINHADAS ao _BRIEF §2 e fakes deterministas,
// evitando essa cadeia. Ver pdp.go, gateway.go e broker.go.
//
// # Convenções de teste
//
// A separação unit/ vs integração/, a convenção de nomes e a localização das
// fixtures estão documentadas em docs/testing/ e em README.md deste módulo.
package testkit
