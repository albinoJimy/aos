package durable

import "errors"

var (
	// ErrEmptyRunID — o run_id fornecido à derivação da chave é vazio. A chave de
	// idempotência exige um run_id não-vazio (é metade da identidade do passo).
	ErrEmptyRunID = errors.New("durable: run_id vazio")

	// ErrEmptyStepID — o step_id fornecido à derivação da chave é vazio.
	ErrEmptyStepID = errors.New("durable: step_id vazio")

	// ErrDelimiterInInput — run_id ou step_id contém o delimitador ':'. A forma
	// canónica da chave é run_id + ":" + step_id; permitir ':' nos inputs abriria
	// uma colisão (ex.: ("a","bc") e ("ab","c") produziriam ambos "a:bc"/"ab:c" com
	// o mesmo total "a:bc"). Proibir o delimitador nos inputs torna a função
	// INJECTIVA: existe uma e uma só decomposição de cada chave (split no único ':').
	ErrDelimiterInInput = errors.New("durable: run_id/step_id não pode conter ':'")

	// ErrMalformedKey — a chave passada ao ledger não tem a forma canónica
	// run_id:step_id (exactamente um ':', ambos os lados não-vazios). Uma chave
	// bem-formada é sempre o produto de [IdempotencyKey]; recusá-la de outra forma
	// protege a inversão [SplitKey] usada para derivar o envelope no Event Store.
	ErrMalformedKey = errors.New("durable: chave mal-formada (esperado run_id:step_id)")

	// ErrNilStore — o [StepLedger] foi construído sem um Event Store (nil).
	ErrNilStore = errors.New("durable: event store em falta")

	// ErrNilEffect — Apply foi chamado com um effect nil.
	ErrNilEffect = errors.New("durable: effect nil")

	// ErrReservedStepID — o step_id da chave passada a [StepLedger.Apply] começa
	// pelo prefixo reservado "ledger-", o namespace que o próprio ledger usa no
	// envelope do Event Store (run_id:ledger-<step_id>). Aceitá-lo faria o registo
	// de negócio colidir GLOBALMENTE (a dedup do ES é por idempotency_key sem
	// partição) com o registo do ledger homónimo — um dos dois seria silenciosamente
	// StatusDuplicate (write perdido/phantom). Fecha estruturalmente essa colisão o
	// namespace que o ledger reivindica.
	ErrReservedStepID = errors.New("durable: step_id não pode começar por 'ledger-' (namespace reservado do ledger)")

	// ErrClearResultInSensitiveMode — em modo sensível ([WithSensitiveResults]) o
	// effect devolveu um Result com Payload não-vazio SEM o marcar como Reference.
	// O Payload é persistido em CLARO no evento durável do Event Store (o cifrado
	// por-titular do ES é dívida de EPIC-13); em modo sensível o ledger recusa
	// memorizar bytes de resultado em claro — o chamador tem de passar uma
	// referência (hash/URI) e marcar Result.Reference.
	ErrClearResultInSensitiveMode = errors.New("durable: modo sensível recusa Payload de resultado em claro (marque Result.Reference com uma referência)")
)
