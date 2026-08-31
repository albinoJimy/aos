// Package natsjs é o cliente mínimo do protocolo NATS/JetStream que o Event Store do
// AOS usa como substrato replicado (AOS-100, ADR-007). Só stdlib.
//
// # Porque é escrito aqui em vez de importado
//
// O ADR-017 §1 sela que o binário do nó é ZERO-DEP — stdlib + cedar-go, build
// reprodutível offline. O Event Store vive DENTRO do nó, pelo que um cliente NATS de
// terceiros seria uma dependência externa no artefacto que o ADR protege.
//
// Perante o mesmo dilema, o próprio ADR-017 já decidiu no mesmo sentido: escolheu
// `crypto/ed25519` da stdlib em vez de cosign/sigstore, «decisão declarada, com o custo
// em §Consequências». Este pacote é a aplicação desse padrão ao substrato — decisão do
// dono, 2026-08-31, depois da medição em
// docs/reports/medicao-jetstream-arbitragem-2026-08-31.md.
//
// O custo é simétrico e fica declarado: passamos a ser donos de um cliente de protocolo
// em caminho crítico. Mitiga-se por âmbito — este pacote implementa o SUBCONJUNTO de
// que o Event Store precisa, não o protocolo todo. Não há reconexão automática, não há
// clustering do lado do cliente, não há consumidores durável-push (ver §Limites).
//
// # O que o Event Store precisa, e porquê só isto
//
// A medição de 2026-08-31 contra um cluster R3 real mostrou que as garantias do AOS-100
// são todas do SERVIDOR, não do cliente:
//
//   - a atomicidade do expected_seq entre escritores é imposta pelo cabeçalho
//     Nats-Expected-Last-Subject-Sequence;
//   - o append-only é imposto por deny_delete/deny_purge no stream;
//   - o quórum e o failover são Raft do lado do servidor.
//
// Ao cliente resta publicar com cabeçalhos e ler respostas. É por isso que este pacote
// é pequeno: a parte difícil não é nossa.
//
// # Limites (declarados, não descobertos)
//
//   - SEM reconexão automática. Uma ligação partida devolve erro ao chamador; a
//     política de retry é de quem chama, porque só ele sabe se o retry é seguro — e no
//     Event Store é, precisamente por causa do CAS.
//   - SEM consumidores push duráveis. O transporte push do AC2 do AOS-100 não foi
//     medido (ver §5 do relatório) e não se constrói aqui às cegas.
//   - SEM autenticação. O cluster de referência corre em rede fechada; credenciais são
//     do Broker/Vault (ADR-006) e entram quando houver um deployment que as exija.
package natsjs
