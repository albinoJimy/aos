package control

import "errors"

var (
	// ErrNilStore — o [SteerChannel] foi construído sem um Event Store (nil). Sem log
	// append-only os sinais pause/steer/resume não são duráveis nem reconstruíveis por
	// replay — o contrato inteiro de AOS-023 assenta na durabilidade do canal.
	ErrNilStore = errors.New("control: event store em falta")

	// ErrNilAuthenticator — o canal foi construído sem um [Authenticator]. Sem ele NÃO
	// há fronteira de autenticação: qualquer sinal seria aceite e conteúdo untrusted
	// poderia dirigir o agente (escalada de privilégio, violação de ADR-005/013). O
	// canal recusa-se a existir sem autenticador — fail-closed na construção.
	ErrNilAuthenticator = errors.New("control: authenticator em falta (o canal exige autenticação de emissor)")

	// ErrEmptyRunID — o run_id fornecido a um sinal é vazio. É o stream_id no Event
	// Store e a âncora que liga o sinal out-of-band ao run correcto.
	ErrEmptyRunID = errors.New("control: run_id vazio")

	// ErrEmptyEmitterID — o [Emitter] de um sinal não tem ID. Sem identidade de emissor
	// não há NÃO-REPÚDIO (ADR-013): o log tem de provar QUEM emitiu cada pause/steer/
	// resume. Um sinal sem emissor identificado é rejeitado antes de tocar no log.
	ErrEmptyEmitterID = errors.New("control: emitter sem ID (não-repúdio exige identidade do emissor)")

	// ErrUnauthenticated — a assinatura do [Emitter] NÃO valida contra a sua chave
	// registada (ou o emissor é desconhecido). É a FRONTEIRA DE SEGURANÇA de AOS-023:
	// um steer sem autenticação válida é REJEITADO. Conteúdo untrusted (resultado de
	// tool / web, ADR-005) não carrega uma credencial de emissor válida, logo NUNCA se
	// pode tornar um sinal de controlo nem autorizar acções — a escalada de privilégio
	// é impedida por construção. Distinto de dados untrusted: só um sinal AUTENTICADO
	// do canal de controlo dirige o agente.
	ErrUnauthenticated = errors.New("control: sinal não autenticado — assinatura do emissor inválida ou emissor desconhecido")

	// ErrEmptyCorrection — um [SteerChannel.Steer] foi chamado com uma correcção vazia.
	// Uma correcção sem conteúdo não é uma instrução de controlo — é rejeitada.
	ErrEmptyCorrection = errors.New("control: correcção de steer vazia")

	// ErrNilGate — uma operação que transita a máquina de estados (graceful pause /
	// resume) recebeu um [StateGate] nil. Sem gate não há como materializar as
	// transições running→paused / paused→running de AOS-017.
	ErrNilGate = errors.New("control: state gate em falta")

	// ErrCorruptControlLog — o [SteerChannel.Rebuild] leu do log um evento de controlo
	// cujo payload não descodifica ou cujo kind é desconhecido (log corrompido / schema
	// incompatível). Fail-closed: recusa reconstruir a projecção sob um evento que não
	// entende, em vez de perder silenciosamente um sinal pause/steer/resume.
	ErrCorruptControlLog = errors.New("control: evento de controlo corrompido ou desconhecido no log")

	// ErrControlLogDivergence — o Event Store devolveu StatusDuplicate para a
	// idempotency_key ctrl-N (a chave já existia) e IGNOROU o payload novo, mas o
	// evento de controlo JÁ PERSISTIDO sob essa chave NÃO é o mesmo sinal que se pediu
	// (kind/emissor/correcção diferentes). Dobrar o registo novo na projecção divergiria
	// silenciosamente do log durável. Fail-closed (espelha [state.ErrStateDivergence] da
	// máquina de AOS-017): recusa-se a mutar a projecção a partir de um payload
	// descartado, em vez de aceitar um duplicado que não corresponde ao pedido.
	ErrControlLogDivergence = errors.New("control: divergência de dedup no log de controlo (duplicado ctrl-N não corresponde ao sinal pedido)")
)
