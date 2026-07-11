// Package contract define o CONTRATO PARTILHADO do plano de controlo do AOS
// (AOS-012): os tipos de evento canónicos, os identificadores de correlação
// (run_id/task_id/step_id), a máquina de estados mínima (ready → running →
// complete|failed) e as portas estáveis Orchestrator/Scheduler.
//
// É importado tanto pelo Orquestrador (produtor de eventos) como pelo
// Escalonador (consumidor), que vivem em módulos distintos. O pacote tem ZERO
// dependências fora da stdlib — é o núcleo estável sobre o qual EPIC-03 estende
// o plano de controlo sem quebras.
//
// NÃO-PRODUTIVO (esqueleto AOS-012): a máquina de estados é o subconjunto
// mínimo da máquina durável completa (tecnica/02 §5); o grafo é trivial (1 nó,
// ver graph.go); não há leases/fencing/prioridade/backpressure (EPIC-03). Os
// pontos de extensão estão marcados em cada ficheiro.
package contract
