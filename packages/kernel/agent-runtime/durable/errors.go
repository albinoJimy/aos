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

	// ErrStepIdentityMismatch — o [StepIdentity] injectado no [Resumer] NÃO reproduz
	// o step_id que o loop gravou no checkpoint (formato de step_id divergente:
	// prefixo/largura diferentes). A correcção do resume-from-step depende de o
	// Resumer e o loop derivarem step_ids IDÊNTICOS para a mesma posição (senão o
	// NextStepID apontaria para um passo que o loop nunca produz, ou o turno retomado
	// gravaria sob uma chave diferente, escapando à dedup). Em vez de só o avisar por
	// comentário, o Resumer VERIFICA-O contra o step_id persistido no cursor e
	// fail-closed aqui — nunca devolve um cursor sob identidade divergente.
	ErrStepIdentityMismatch = errors.New("durable: StepIdentity do Resumer diverge do step_id gravado no checkpoint (formato incompatível)")

	// ErrClearResultInSensitiveMode — em modo sensível ([WithSensitiveResults]) o
	// effect devolveu um Result com Payload não-vazio SEM o marcar como Reference.
	// O Payload é persistido em CLARO no evento durável do Event Store (o cifrado
	// por-titular do ES é dívida de EPIC-13); em modo sensível o ledger recusa
	// memorizar bytes de resultado em claro — o chamador tem de passar uma
	// referência (hash/URI) e marcar Result.Reference.
	ErrClearResultInSensitiveMode = errors.New("durable: modo sensível recusa Payload de resultado em claro (marque Result.Reference com uma referência)")

	// --- AOS-018: liveness por lease/heartbeat + fencing tokens ---

	// ErrInvalidTTL — o TTL passado a [NewLeaseManager] não é > 0. Um lease sem TTL
	// positivo não teria liveness (nunca expiraria ou expiraria de imediato).
	ErrInvalidTTL = errors.New("durable: TTL do lease tem de ser > 0")

	// ErrNilTokenSource — [NewFencedAppender] foi construído sem [TokenSource] (nil):
	// o enforcement de fencing não tem autoridade para consultar o token corrente.
	ErrNilTokenSource = errors.New("durable: token source (autoridade de fencing) em falta")

	// ErrLeaseHeld — [LeaseManager.Claim] recusou porque um lease AINDA VÁLIDO (não
	// expirado) está detido. Não se rouba um lease vivo; só um run livre (nunca
	// reclamado ou com o lease expirado por ausência de heartbeat) é reclamável.
	ErrLeaseHeld = errors.New("durable: run já tem um lease válido detido (não expirado)")

	// ErrLeaseExpired — [LeaseManager.Heartbeat] recusou renovar porque o TTL do lease
	// já se esgotou; é tarde demais — o run já é reclamável por outro worker. O
	// detentor obsoleto deve abortar (as suas escritas serão fenced-out).
	ErrLeaseExpired = errors.New("durable: lease expirado (TTL esgotado); tarde demais para heartbeat")

	// ErrLeaseSuperseded — [LeaseManager.Heartbeat] recusou porque o lease corrente já
	// é de um token SUPERIOR: um novo claim superou este lease. O worker deste lease
	// está obsoleto (fenced-out) e deve abortar.
	ErrLeaseSuperseded = errors.New("durable: lease superado por um claim posterior (token corrente é superior)")

	// ErrClaimContention — [LeaseManager.Claim]/[Heartbeat] esgotou as re-tentativas
	// sob contenção de concorrência optimista sem convergir. Sinal raro (contenção
	// patológica); o chamador pode tentar de novo mais tarde.
	ErrClaimContention = errors.New("durable: contenção de claim esgotou as re-tentativas")

	// ErrStaleFencingToken — o [FencedAppender] REJEITOU uma escrita cujo fencing token
	// é INFERIOR ao corrente do run (worker obsoleto, cujo lease foi superado por um
	// novo claim), ou cujo token está ausente/0. É o enforcement que garante NO MÁXIMO
	// UM ESCRITOR EFECTIVO por run e, com ele, ZERO execução dupla sob reatribuição.
	ErrStaleFencingToken = errors.New("durable: fencing token obsoleto (inferior ao corrente); escrita rejeitada")
)
