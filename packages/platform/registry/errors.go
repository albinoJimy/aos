package registry

// RegistryError é o erro sentinela do serviço REG (a camada de orquestração sobre
// o domínio + Event Store). Código estável, comparável com errors.Is. Fail-closed
// em toda a superfície: qualquer condição que não seja um sucesso inequívoco
// resolve-se por rejeição.
type RegistryError struct {
	Code string
	msg  string
}

func (e *RegistryError) Error() string { return e.Code + ": " + e.msg }

var (
	// ErrNoStore — o REG foi construído sem Event Store. A persistência append-only
	// é a fonte de verdade (ADR-007); sem ela o serviço é inutilizável (fail-closed).
	ErrNoStore = &RegistryError{Code: "E_REG_NO_STORE", msg: "event store nao configurado"}

	// ErrVersionExists — tentativa de PUBLICAR uma (id, version) que já existe. O
	// catálogo é append-only: uma versão nunca é editada in-place. Editar produz uma
	// NOVA versão; sobrescrever é recusado.
	ErrVersionExists = &RegistryError{Code: "E_REG_VERSION_EXISTS", msg: "versao ja existe no catalogo (append-only: use uma nova versao)"}

	// ErrNotFound — a (id, version) pinada não está no catálogo. É a base do
	// default-deny: uma capacidade fora do catálogo é recusada (ADR-002).
	ErrNotFound = &RegistryError{Code: "E_REG_NOT_FOUND", msg: "artefacto nao encontrado no catalogo (default-deny)"}

	// ErrFloatingResolution — pediu-se resolução por uma referência FLUTUANTE
	// ("latest", "main", vazio, intervalo…). Proibido: a resolução é sempre por
	// versão SemVer pinada exacta (tecnica/05 §5).
	ErrFloatingResolution = &RegistryError{Code: "E_REG_FLOATING_RESOLUTION", msg: "resolucao por referencia flutuante proibida (exige versao pinada exacta)"}

	// ErrUnpinnedResolution — pediu-se Resolve com a versão NÃO-ESPECIFICADA (0.0.0).
	// A resolução exige uma versão pinada; a ausência de versão é recusada.
	ErrUnpinnedResolution = &RegistryError{Code: "E_REG_UNPINNED_RESOLUTION", msg: "resolucao sem versao pinada recusada"}

	// ErrInvalidRequest — o pedido de publicação é mal-formado (id/version/kind
	// inválidos) antes mesmo de tocar no catálogo.
	ErrInvalidRequest = &RegistryError{Code: "E_REG_INVALID_REQUEST", msg: "pedido de publicacao invalido"}

	// ErrAdmissionDenied — o gate de verificação de admissão (staging→active) negou a
	// promoção. É o ponto onde AOS-047/048/053 impõem hash+assinatura+eval-gate; um
	// verificador que recuse mantém o artefacto fora de active (fail-closed).
	ErrAdmissionDenied = &RegistryError{Code: "E_REG_ADMISSION_DENIED", msg: "verificacao de admissao recusou a promocao a active"}

	// ErrConcurrentWrite — o catálogo mudou concorrentemente durante a escrita e o
	// número máximo de retentativas optimistas esgotou-se. O chamador deve reler e
	// reavaliar.
	ErrConcurrentWrite = &RegistryError{Code: "E_REG_CONCURRENT_WRITE", msg: "escrita concorrente no catalogo; reavaliar"}
)
