package domain

// TrustState é o estado de confiança TOFU (Trust On First Use) de um artefacto,
// gravado na proveniência (tecnica/05 §5). AOS-045 RESERVA os estados e o campo; a
// máquina de detecção de mudança de schema (first_seen → pinned → changed) e o seu
// bloqueio são de AOS-049. Aqui o default de uma publicação é FirstSeen: nada é
// tratado como já-confiado sem uma ratificação explícita futura.
type TrustState string

const (
	// TrustFirstSeen — identidade/digest registados na primeira observação; ainda
	// não ratificado. É o estado inicial de qualquer publicação.
	TrustFirstSeen TrustState = "first_seen"
	// TrustPinned — o operador ratificou; o digest fica a referência de confiança.
	TrustPinned TrustState = "pinned"
	// TrustChanged — divergência posterior do digest = incidente (bloqueia até
	// re-aprovação com nova versão SemVer). Detecção/bloqueio são AOS-049.
	TrustChanged TrustState = "changed"
)

// Valid indica se t é um estado de confiança canónico (fail-closed).
func (t TrustState) Valid() bool {
	switch t {
	case TrustFirstSeen, TrustPinned, TrustChanged:
		return true
	default:
		return false
	}
}

// Provenance é a ORIGEM auditável de um artefacto (tecnica/05 §3): de onde veio,
// quem o publicou, quando, e o seu estado de confiança TOFU. Não contém segredos.
type Provenance struct {
	// Origin é a origem do artefacto (ex.: "git+https://…", "mcp://host", "self").
	Origin string `json:"origin"`
	// Publisher é o identificador do publicador (a chave de confiança que assina o
	// tuplo (id,version,digest) é verificada em AOS-048; aqui é só o identificador).
	Publisher string `json:"publisher"`
	// Timestamp é o instante de admissão em RFC3339 UTC, atribuído pelo REG a partir
	// de um relógio INJECTÁVEL (determinismo; nunca time.Now numa decisão).
	Timestamp string `json:"timestamp"`
	// Trust é o estado de confiança TOFU.
	Trust TrustState `json:"trust"`
}
