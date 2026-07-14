package tofu

// TofuError é o erro sentinela da máquina TOFU (AOS-049). Código estável,
// comparável com errors.Is. FAIL-CLOSED em toda a superfície: qualquer condição
// que não seja uma transição de confiança inequivocamente válida resolve-se por
// RECUSA — nunca por um silêncio que deixe passar um artefacto não-confiado.
type TofuError struct {
	Code string
	msg  string
}

func (e *TofuError) Error() string { return e.Code + ": " + e.msg }

var (
	// ErrNoAuditStore — construiu-se um Monitor sem audit store. A auditabilidade de
	// cada transição de confiança é uma PRÉ-CONDIÇÃO (ADR-010): uma máquina TOFU cujas
	// transições não podem ser seladas na cadeia WORM não é admissível (fail-closed).
	ErrNoAuditStore = &TofuError{Code: "E_TOFU_NO_AUDIT", msg: "audit store obrigatorio (auditabilidade e pre-condicao)"}

	// ErrEmptyIdentity — a observação/ratificação chegou sem identidade de servidor.
	// A identidade é a chave TOFU (o que se regista na primeira ligação); vazia é recusada.
	ErrEmptyIdentity = &TofuError{Code: "E_TOFU_EMPTY_IDENTITY", msg: "identidade de servidor MCP vazia"}

	// ErrUnpinnedVersion — a versão da observação é a NÃO-ESPECIFICADA (0.0.0). O TOFU
	// ancora sempre a uma versão SemVer pinada exacta (nunca uma referência flutuante).
	ErrUnpinnedVersion = &TofuError{Code: "E_TOFU_UNPINNED_VERSION", msg: "versao nao pinada (0.0.0) recusada"}

	// ErrEmptyDigest — a observação chegou sem digest do manifesto de capabilities. O
	// digest (AOS-047) é a referência de estabilidade do schema; ausente é recusado.
	ErrEmptyDigest = &TofuError{Code: "E_TOFU_EMPTY_DIGEST", msg: "digest do manifesto vazio"}

	// ErrAuditFailed — a selagem de uma transição no audit WORM falhou. Uma transição
	// de confiança que não pode ser registada na cadeia é uma acção sem rasto:
	// fail-closed (ADR-002/010) — a transição NÃO toma efeito e é recusada.
	ErrAuditFailed = &TofuError{Code: "E_TOFU_AUDIT_FAILED", msg: "selagem da transicao no audit WORM falhou (transicao recusada)"}

	// ErrSchemaDrift — em estado pinned, o digest do manifesto observado DIVERGE do
	// digest pinado (schema mutou / tools diferentes). É um INCIDENTE de segurança (o
	// rug-pull do "Dia 7"), não uma actualização de rotina: o artefacto transita para
	// changed e fica BLOQUEADO até re-aprovação explícita com uma NOVA versão SemVer.
	ErrSchemaDrift = &TofuError{Code: "E_TOFU_SCHEMA_DRIFT", msg: "digest do manifesto divergiu do pinado (schema drift) = incidente; bloqueado ate re-aprovacao"}

	// ErrNotFirstSeen — tentou-se ratificar (RatifyPin) uma identidade que NÃO está em
	// first_seen. Só o first_seen (a primeira observação, ainda não confiada) é
	// promovível a pinned pelo operador.
	ErrNotFirstSeen = &TofuError{Code: "E_TOFU_NOT_FIRST_SEEN", msg: "ratificacao exige estado first_seen"}

	// ErrRatifyMismatch — a (versão, digest) que o operador ratifica NÃO coincide com a
	// observada em first_seen. O operador ratifica EXACTAMENTE o que foi observado
	// (elimina o TOCTOU entre o que se viu e o que se pina); qualquer divergência recusa.
	ErrRatifyMismatch = &TofuError{Code: "E_TOFU_RATIFY_MISMATCH", msg: "(versao,digest) ratificado nao coincide com o observado em first_seen"}

	// ErrNotChanged — tentou-se re-aprovar (Reapprove) uma identidade que NÃO está em
	// changed. A re-aprovação é o caminho de recuperação de um incidente de drift; uma
	// identidade que não sofreu drift não tem nada a re-aprovar.
	ErrNotChanged = &TofuError{Code: "E_TOFU_NOT_CHANGED", msg: "re-aprovacao exige estado changed"}

	// ErrInBandReapproval — tentou-se re-aprovar com a MESMA versão SemVer da referência
	// pinada (ainda que com um digest diferente). Uma mudança de schema é, por
	// definição, uma NOVA versão (ADR-012): NUNCA é aceite in-band na mesma versão.
	// Re-pinar a mesma versão com digest diferente é o próprio vector do rug-pull — recusado.
	ErrInBandReapproval = &TofuError{Code: "E_TOFU_INBAND_REAPPROVAL", msg: "re-aprovacao in-band recusada: a mudanca de schema exige uma NOVA versao SemVer (nunca a mesma versao)"}

	// ErrVersionRegression — tentou-se re-aprovar com uma versão INFERIOR à pinada. O
	// TOFU só avança em SemVer; um downgrade re-introduziria um schema anterior sob
	// aparência de re-aprovação (fail-closed).
	ErrVersionRegression = &TofuError{Code: "E_TOFU_VERSION_REGRESSION", msg: "re-aprovacao com versao inferior a pinada recusada"}
)
