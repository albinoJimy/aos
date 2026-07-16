package messaging

import "errors"

// Sentinelas de erro do módulo. Comparáveis com errors.Is, inclusive quando
// embrulhados com fmt.Errorf("%w: …", …). TODAS as rejeições resolvem-se pelo lado
// seguro (fail-closed): a ausência de prova de origem/autoridade/referência é
// sempre recusa, nunca acção silenciosa.
var (
	// ErrInvalidMessage — mensagem malformada para assinar/verificar (origem,
	// acção, referência, assinatura, nonce ou timestamp em falta/malformados).
	// Fail-closed.
	ErrInvalidMessage = errors.New("messaging: mensagem invalida (origem/accao/referencia/assinatura/nonce/timestamp em falta)")

	// ErrUnknownOrigin — a NHI clamada como origem NÃO consta do directório de
	// identidade: a origem não é sequer autenticável (chave pública pinada
	// ausente). Distinto de [ErrForgedOrigin]: aqui não há âncora contra a qual
	// verificar. Fail-closed.
	ErrUnknownOrigin = errors.New("messaging: origem desconhecida (NHI sem chave publica pinada)")

	// ErrForgedOrigin — a assinatura NÃO valida contra a chave pública pinada da
	// NHI clamada como origem: a mensagem foi forjada (o emissor real não é quem
	// clama ser) ou adulterada. É o coração da anti-forja e da elevação do
	// hallucination gate: mesmo com um ID que existe, uma assinatura ausente/forjada
	// é rejeitada.
	ErrForgedOrigin = errors.New("messaging: origem forjada (assinatura nao valida contra a chave da NHI clamada)")

	// ErrAuthorityNotCovered — a autoridade AUTORITATIVA do emissor não cobre a
	// acção pedida (ou a autoridade CLAMADA excede a autoritativa). A mensagem é
	// recusada mesmo com assinatura válida. Fail-closed.
	ErrAuthorityNotCovered = errors.New("messaging: autoridade do emissor nao cobre a accao pedida")

	// ErrReferenceNotFound — o item referenciado NÃO existe (referência fabricada).
	// Fail-closed.
	ErrReferenceNotFound = errors.New("messaging: referencia inexistente (item fabricado)")

	// ErrReferenceInauthentic — o item referenciado existe mas o seu hash de
	// conteúdo autêntico DIVERGE do hash coberto pela assinatura (referência
	// adulterada). Fail-closed.
	ErrReferenceInauthentic = errors.New("messaging: referencia inautentica (hash de conteudo divergente)")

	// ErrStaleMessage — a mensagem está FORA da janela de frescura: demasiado antiga
	// (replay de uma captura antiga) ou com timestamp futuro além do skew tolerado.
	// A assinatura autentica a ORIGEM mas não a FRESCURA; este gate fecha o vetor de
	// captura-e-reenvio. Fail-closed.
	ErrStaleMessage = errors.New("messaging: mensagem fora da janela de frescura (antiga ou futura)")

	// ErrReplayedNonce — o par (Origin, Nonce) já foi consumido: a mensagem é um
	// REPLAY de uma anterior já verificada. Rejeitada para não re-autorizar a mesma
	// acção/referência. Fail-closed.
	ErrReplayedNonce = errors.New("messaging: nonce reutilizado (replay de mensagem ja verificada)")

	// ErrSealFailed — a rejeição não pôde ser SELADA no audit tamper-evident. A
	// mensagem permanece rejeitada (audit-before-effect: uma rejeição não-auditável
	// não vira aceitação), mas o erro é sinalizado para diagnóstico.
	ErrSealFailed = errors.New("messaging: falha ao selar a rejeicao no audit")

	// ErrNilDeps — dependências obrigatórias do Verifier/Signer em falta.
	ErrNilDeps = errors.New("messaging: dependencias obrigatorias em falta")
)
