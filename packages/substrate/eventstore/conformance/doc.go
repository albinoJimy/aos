// Package conformance mede, contra um substrato candidato a Event Store, a ÚNICA
// propriedade que decide se o AOS-100 foi cumprido:
//
//	o expected_seq (concorrência optimista) é ATÓMICO ENTRE ESCRITORES INDEPENDENTES.
//
// # Porque existe um pacote só para isto
//
// O AOS-100 troca o substrato por baixo de toda a disciplina de posse do AOS — o
// LeaseManager (AOS-018), o FencedAppender, a composição ORQ/SCH↔posse (ADR-023) e a
// re-hidratação do grafo. Essas peças estão CORRECTAS, e a sua correcção é CONDICIONAL
// a esta propriedade. Hoje passam nos testes porque estes correm in-process, onde o log
// é de facto partilhado; esse verde NÃO é prova de que funcionam entre processos.
//
// A propriedade tem, por isso, de ser MEDIDA contra o substrato — nunca inferida da
// documentação dele. Um backend replicado dá ordem total por stream, o que é uma
// propriedade DIFERENTE: se o expected_seq for implementado por leitura-depois-escrita
// sem CAS nativo, continua a não haver arbitragem — e agora com a aparência de a haver.
// É essa a armadilha que este pacote existe para desarmar. Ver a regra de método em
// docs/reports/auditoria-das-minhas-proprias-afirmacoes-2026-08-31.md §6.
//
// # O que é um "handle independente"
//
// [Substrato] devolve N handles que, do ponto de vista do substrato, são ESCRITORES
// DISTINTOS sobre o MESMO log — o que dois processos são um para o outro. Para o Event
// Store de referência isso é N chamadas a eventstore.Open sobre o mesmo WAL; para um
// backend remoto, N ligações. A medição é deliberadamente indiferente a essa diferença:
// é a propriedade que está a ser medida, não a implementação.
//
// A escolha de handles in-process em vez de processos reais é a mesma que
// doisOpensReclamamOMesmoToken (packages/cmd/aos-orq) fez e pela mesma razão: uma
// corrida entre processos mede o mesmo mas com desfecho dependente do escalonador do
// SO, e um sensor intermitente é pior do que nenhum.
//
// # Como se usa
//
//   - Contra um backend CANDIDATO: [RunArbitragem], que FALHA se qualquer sonda
//     acusar ausência. É o gate do AOS-100.
//   - Contra o backend ACTUAL: [Sondar], que devolve o relatório sem falhar, para que
//     um sensor possa assertar a ausência hoje e ACUSAR o dia em que ela desaparecer.
//
// As quatro sondas não são independentes por acaso: cada uma falha sozinha num
// substrato plausível. A visibilidade cai em qualquer cache local; o CAS cai num
// backend sem compare-and-set nativo; a dedup cai num backend cuja deduplicação é uma
// JANELA temporal e não um índice permanente; a corrida cai quando o CAS existe mas
// não é serializável sob contenção.
package conformance
