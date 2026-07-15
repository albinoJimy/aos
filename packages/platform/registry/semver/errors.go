// Package semver concretiza a semântica de SemVer ANCORADA A CONTRATO do REG
// (AOS-052, tecnica/05 §7, ADR-012). Não reimplementa o value-type de versão
// (domain.Version, AOS-045) nem o classificador de delta de versão
// (domain.Classify) — constrói POR CIMA deles o núcleo do ticket: dado o contrato
// público ANTIGO e o NOVO, classifica a MUDANÇA que o contrato exige
// (MAJOR/MINOR/PATCH) e VALIDA o bump declarado contra ela.
//
// A regra central é FAIL-CLOSED: uma mudança de contrato QUEBRADA (schema de I/O
// incompatível, scopes de credencial acrescentados, classe de egress elevada,
// semântica declarada quebrada) que NÃO incremente o suficiente é REJEITADA
// (ErrIncompatibleBump). A classificação é DETERMINISTA (mesmos contratos +
// versões → mesmo veredicto), sem relógio nem aleatoriedade na decisão. É MAJOR a
// ÚNICA classe que justifica um novo estado de confiança TOFU (cruza AOS-049).
package semver

// SemverError é o erro sentinela do gate de versionamento (AOS-052). Código
// estável comparável com errors.Is por identidade. Fail-closed: qualquer bump que
// não seja inequivocamente compatível é rejeitado.
type SemverError struct {
	Code string
	msg  string
}

func (e *SemverError) Error() string { return e.Code + ": " + e.msg }

var (
	// ErrIncompatibleBump — a mudança de contrato detectada EXIGE um bump maior do
	// que o declarado pelo delta de versão (ex.: um schema de I/O incompatível ou
	// scopes acrescentados publicados como MINOR/PATCH em vez de MAJOR). É a
	// rejeição central do ticket: uma quebra de contrato tem SEMPRE de incrementar o
	// nível exigido, nunca de forma silenciosa.
	ErrIncompatibleBump = &SemverError{Code: "E_REG_INCOMPATIBLE_BUMP", msg: "bump declarado insuficiente para a mudanca de contrato (quebra de contrato exige bump maior)"}

	// ErrNonMonotonicBump — a versão de destino não é ESTRITAMENTE superior à de
	// origem. Um bump é sempre um avanço; um downgrade ou uma re-publicação da mesma
	// versão não é um bump válido (o rollback para uma versão anterior é uma
	// operação distinta e atómica — ver registry.Lifecycle, não este gate).
	ErrNonMonotonicBump = &SemverError{Code: "E_REG_NON_MONOTONIC_BUMP", msg: "versao de destino nao e estritamente superior a de origem"}
)
