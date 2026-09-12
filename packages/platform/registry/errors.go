package registry

import "github.com/aos-ref/platform/registry/digest"

// ErrDigestMismatch é re-exportado do pacote digest (AOS-047): a resolução e a
// consulta de digest recalculam o digest sobre o conteúdo canonicalizado e
// comparam-no com o esperado no REG; a divergência BLOQUEIA (fail-closed). É a
// MESMA identidade que a revalidação por chamada (AOS-051) reutilizará, pelo que
// errors.Is(err, registry.ErrDigestMismatch) e errors.Is(err,
// digest.ErrDigestMismatch) são equivalentes.
var ErrDigestMismatch = digest.ErrDigestMismatch

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

	// ErrManifestDigestRequired — publicou-se um `kind=mcp_server` sem `ManifestDigest`
	// (AOS-334). É devolvido EMBRULHADO em [ErrInvalidRequest], pelo que quem só distingue
	// «pedido inválido» continua a funcionar e quem quiser a causa exacta tem-na.
	//
	// PORQUE É FAIL-CLOSED. O contrato de um `mcp_server` é `{Egress, ManifestDigest}`: sem o
	// digest, sobra a CLASSE DE EGRESS — um valor de baixíssima cardinalidade que colide entre
	// servidores completamente diferentes. Foi esse o defeito que o AOS-320 fechou, e fechou-o
	// por CONVENÇÃO: só o `mcp.Host.stage` publica `mcp_server`, e ele grava sempre o digest.
	// Convenção não é gate — qualquer outro caminho de publicação reabria o buraco em silêncio.
	//
	// «`mcp_server` EXIGE», e nunca «só `mcp_server` PODE TER»: as entradas `kind=tool`
	// derivadas de um servidor MCP transportam a âncora de propósito (a correcção A1 do
	// AOS-320), e a formulação exclusiva partiria isso.
	ErrManifestDigestRequired = &RegistryError{Code: "E_REG_MANIFEST_DIGEST_REQUIRED", msg: "kind=mcp_server exige manifest_digest nao-vazio"}

	// ErrAdmissionDenied — o gate de verificação de admissão (staging→active) negou a
	// promoção. É o ponto onde AOS-047/048/053 impõem hash+assinatura+eval-gate; um
	// verificador que recuse mantém o artefacto fora de active (fail-closed).
	ErrAdmissionDenied = &RegistryError{Code: "E_REG_ADMISSION_DENIED", msg: "verificacao de admissao recusou a promocao a active"}

	// ErrNoAdmissionVerifier — tentou-se PROMOVER a active um Registry construído SEM
	// AdmissionVerifier explícito (WithAdmissionVerifier). O default é FAIL-CLOSED: na
	// ausência de um verificador de assinatura real (AOS-048), nenhuma promoção a
	// active é admitida — em vez de admitir tudo (o antigo placeholder fail-open). É
	// devolvido embrulhado em ErrAdmissionDenied, pelo que errors.Is(err,
	// ErrAdmissionDenied) continua verdadeiro no caminho de promoção.
	ErrNoAdmissionVerifier = &RegistryError{Code: "E_REG_NO_ADMISSION_VERIFIER", msg: "nenhum verificador de admissao injectado (fail-closed: promocao a active recusada)"}

	// ErrConcurrentWrite — o catálogo mudou concorrentemente durante a escrita e o
	// número máximo de retentativas optimistas esgotou-se. O chamador deve reler e
	// reavaliar.
	ErrConcurrentWrite = &RegistryError{Code: "E_REG_CONCURRENT_WRITE", msg: "escrita concorrente no catalogo; reavaliar"}
)
