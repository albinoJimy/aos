package state

import "errors"

var (
	// ErrInvalidTransition — o par (from → to) pedido a [Machine.Transition] NÃO
	// consta da tabela declarativa ([validTransitions]). A transição é rejeitada SEM
	// escrever no Event Store e SEM mutar o estado corrente — nem o estado persistido
	// nem o in-memory são corrompidos (critério de aceitação AOS-017).
	ErrInvalidTransition = errors.New("state: transição inválida (par não consta da tabela)")

	// ErrMissingFencingToken — a transição EXIGE um fencing token válido (o claim
	// ready → running) mas o [TransitionEvent] não trouxe token, ou o token presente
	// falha [FencingToken.Valid]. A entrada em running é recusada fail-closed — sem
	// token válido nenhum worker se torna o escritor efectivo do run (contrato
	// partilhado com AOS-018).
	ErrMissingFencingToken = errors.New("state: entrada em running exige fencing token válido")

	// ErrStaleFencingToken — o claim ready → running trouxe um token VÁLIDO mas
	// OBSOLETO (inferior ao token corrente do run reportado pela [FencingAuthority]
	// injectada via [WithFencingAuthority]). Um worker superado por um novo claim é
	// recusado fail-closed ANTES de materializar a transição — a PRESENÇA do token não
	// basta quando há autoridade de staleness ligada. Sem [FencingAuthority] a
	// máquina impõe só a presença/validade (contrato mínimo de AOS-017), delegando a
	// staleness ao [durable.FencedAppender] de AOS-018.
	ErrStaleFencingToken = errors.New("state: fencing token obsoleto (inferior ao corrente) — claim de worker superado recusado")

	// ErrNilStore — a [Machine] foi construída sem um Event Store (nil). Sem log
	// append-only não há durabilidade nem reconstrução por replay.
	ErrNilStore = errors.New("state: event store em falta")

	// ErrEmptyRunID — o run_id fornecido à [Machine] é vazio. É o stream_id no Event
	// Store e metade da idempotency_key de cada evento de transição.
	ErrEmptyRunID = errors.New("state: run_id vazio")

	// ErrUnknownState — o Rebuild leu do log um estado que não é um dos dez canónicos
	// (log corrompido ou de um schema futuro/incompatível). Fail-closed: recusa
	// reconstruir sob um estado desconhecido em vez de o adoptar silenciosamente.
	ErrUnknownState = errors.New("state: estado desconhecido no log (não é um dos dez canónicos)")

	// ErrStateDivergence — o Append devolveu StatusDuplicate (a idempotency_key
	// "state-N" já existia) mas o evento persistido sob essa chave NÃO é a transição
	// que a máquina pediu (o duplicado original vence; o payload novo é ignorado pelo
	// Event Store). Avançar o estado in-memory para o alvo pedido divergiria
	// silenciosamente do log; a transição é recusada fail-closed sem mutar o estado.
	// Latente com um único escritor in-memory — defesa-em-profundidade contra
	// split-brain / retries sob a mesma chave (o fencing durável é AOS-018).
	ErrStateDivergence = errors.New("state: divergência in-memory vs log — duplicado persistido difere da transição pedida")

	// ErrCorruptChain — o Rebuild encontrou uma quebra de continuidade na cadeia de
	// transições: o From de um evento não bate o To do evento anterior (log
	// bifurcado, com furos ou de dois escritores). Fail-closed: recusa reconstruir
	// sob uma cadeia partida em vez de adoptar o último To silenciosamente.
	ErrCorruptChain = errors.New("state: cadeia de transições descontínua no log (from não bate o to anterior)")
)
