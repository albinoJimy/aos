package digest

import "errors"

// ErrInvalidJSON — o conteúdo apresentado como JSON (schema da tool / manifesto
// de capabilities) não é JSON válido. Fail-closed: não se pina nem se hasheia
// conteúdo malformado como se fosse canónico.
var ErrInvalidJSON = errors.New("E_DIGEST_INVALID_JSON: conteudo nao e JSON valido")

// ErrDigestMismatch — o digest calculado sobre o conteúdo NÃO coincide com o
// digest esperado no REG. É o sinal de conteúdo adulterado (rug-pull / schema
// drift) que BLOQUEIA a admissão do artefacto no run (fail-closed, tecnica/05
// §5). É comparável com errors.Is, inclusive quando embrulhado por [MismatchError].
var ErrDigestMismatch = errors.New("E_DIGEST_MISMATCH: digest calculado difere do esperado (conteudo adulterado)")

// MismatchError carrega os digests esperado e calculado para diagnóstico
// auditável (ambos são públicos — NÃO são segredos). Satisfaz errors.Is para
// [ErrDigestMismatch], permitindo tanto a comparação por sentinela como a
// inspecção dos valores concretos.
type MismatchError struct {
	Expected string
	Computed string
}

func (e *MismatchError) Error() string {
	return ErrDigestMismatch.Error() + " (esperado=" + redact(e.Expected) + " calculado=" + redact(e.Computed) + ")"
}

// Is torna *MismatchError comparável com o sentinela [ErrDigestMismatch].
func (e *MismatchError) Is(target error) bool { return target == ErrDigestMismatch }

// redact devolve "<vazio>" para um digest ausente e o próprio digest caso
// contrário (um digest é público; não há segredo a ocultar aqui — a função
// existe só para tornar o diagnóstico de ausência legível).
func redact(s string) string {
	if s == "" {
		return "<vazio>"
	}
	return s
}
