package autonomy

import "strings"

// DomainUnknown é o domínio devolvido por [DomainOf] quando não é possível derivar
// um domínio da capability nem do recurso. Um par (agente, DomainUnknown) sem nível
// registado resolve para [L0] no [LevelRegistry] (fail-closed).
const DomainUnknown = "unknown"

// DomainOf deriva, de forma DETERMINISTA, o DOMÍNIO de autonomia (ex.: "fs",
// "mail", "http", "db") a partir da capability escopada da tool call e, em
// alternativa, do recurso. O domínio é a granularidade do par (agente, domínio):
// o mesmo agente pode operar a níveis distintos em domínios distintos.
//
// Regra: o domínio é o PRIMEIRO SEGMENTO da capability, ignorando o prefixo "cap:"
// e cortando no primeiro separador ('.', ':' ou '/'). Ex.: "cap:http.post" → "http",
// "fs:write:/reports/*" → "fs", "mail:send" → "mail". Se a capability for vazia,
// recorre ao [Resource.Value]/tipo pelo mesmo critério. Sem nenhum sinal utilizável
// devolve [DomainUnknown] (fail-closed — nunca um domínio arbitrário).
func DomainOf(capability, resource string) string {
	if d := firstSegment(strings.TrimPrefix(capability, "cap:")); d != "" {
		return d
	}
	if d := firstSegment(resource); d != "" {
		return d
	}
	return DomainUnknown
}

// firstSegment devolve o prefixo de s até ao primeiro separador ('.', ':' ou '/'),
// ou s inteiro se não houver separador. Uma string vazia devolve "".
func firstSegment(s string) string {
	if i := strings.IndexAny(s, ".:/"); i >= 0 {
		return s[:i]
	}
	return s
}
