package authz

import "sort"

// Normalize devolve a forma canónica de um conjunto de capabilities: ordenada
// (ordem lexicográfica estável) e sem duplicados nem vazias. É a base do
// determinismo — o mesmo conjunto produz sempre a mesma serialização, para que o
// escopo efectivo gravado no span/audit seja estável e comparável.
func Normalize(caps []string) []string {
	if len(caps) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(caps))
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		if c == "" {
			continue
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// Intersect calcula a ⊆ intersecção de dois conjuntos de capabilities: a ∩ b,
// ORDENADA e sem duplicados. É a operação-núcleo de "utilizador ∩ classe"
// (ADR-003, espelha AOS-057). O resultado é subconjunto de AMBOS os operandos —
// nunca alarga nenhum. Determinista: ordem lexicográfica estável.
func Intersect(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	inB := make(map[string]struct{}, len(b))
	for _, x := range b {
		if x == "" {
			continue
		}
		inB[x] = struct{}{}
	}
	seen := make(map[string]struct{}, len(a))
	var out []string
	for _, x := range a {
		if x == "" {
			continue
		}
		if _, ok := inB[x]; !ok {
			continue
		}
		if _, dup := seen[x]; dup {
			continue
		}
		seen[x] = struct{}{}
		out = append(out, x)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// Contains indica se a capability cap pertence ao conjunto set.
func Contains(set []string, cap string) bool {
	for _, x := range set {
		if x == cap {
			return true
		}
	}
	return false
}

// Subset indica se a ⊆ b (todos os elementos de a estão em b). O conjunto vazio é
// subconjunto de qualquer conjunto. É o predicado que fundamenta a detecção de
// escalada: uma autoridade reclamada que NÃO é subconjunto do escopo permitido é
// uma tentativa de alargamento.
func Subset(a, b []string) bool {
	if len(a) == 0 {
		return true
	}
	in := make(map[string]struct{}, len(b))
	for _, x := range b {
		in[x] = struct{}{}
	}
	for _, x := range a {
		if x == "" {
			continue
		}
		if _, ok := in[x]; !ok {
			return false
		}
	}
	return true
}

// ScopeDecision é o veredicto policy-as-code da autorização de escopo. É um
// allow/deny explícito com default-deny (ADR-011): a AUSÊNCIA de correspondência
// (a capability não está no escopo efectivo) resolve DENY.
type ScopeDecision struct {
	// Allow é true só se a capability pertence ao escopo efectivo.
	Allow bool
	// Effective é o escopo efectivo computado (utilizador ∩ classe ∩ cadeia),
	// em forma canónica — observável no span/audit.
	Effective []string
	// Reason descreve a decisão (motivo de negação, quando Allow é false).
	Reason string
}

// Authorize é a POLÍTICA de escopo como código (ADR-011), default-deny: dada a
// autoridade efectiva já computada e a capability pedida, PERMITE se e só se a
// capability pertence ao escopo efectivo; caso contrário NEGA. Uma capability
// vazia é sempre negada (não há acção sem capability nomeada). É pura e
// determinista.
func Authorize(effective []string, capability string) ScopeDecision {
	if capability == "" {
		return ScopeDecision{Allow: false, Effective: effective, Reason: "capability vazia (default-deny)"}
	}
	if !Contains(effective, capability) {
		return ScopeDecision{
			Allow:     false,
			Effective: effective,
			Reason:    "capability fora do escopo efectivo utilizador ∩ classe (default-deny)",
		}
	}
	return ScopeDecision{Allow: true, Effective: effective}
}

// AuthoritySource resolve a autoridade-FONTE (ground truth) de um sujeito da
// cadeia de delegação: para o humano-raiz, as capabilities do UTILIZADOR; para um
// agente, as capabilities-máximas da sua CLASSE. É a fronteira estável entre o
// gate de escopo e a governação de identidade/RBAC (GOV): a implementação real
// liga-se ao directório; aqui há uma implementação estática determinista.
//
// A resolução é PURA (sem ctx, sem relógio): a decisão de escopo tem de ser
// determinista e reproduzível a partir do registo de audit.
type AuthoritySource interface {
	// Authority devolve as capabilities-fonte do sujeito e ok=true se o sujeito é
	// conhecido. Um sujeito DESCONHECIDO devolve (nil, false): o gate trata-o como
	// autoridade VAZIA (fail-closed — a intersecção colapsa para ∅, tudo negado).
	Authority(subject string) (caps []string, ok bool)
}

// StaticAuthoritySource é uma [AuthoritySource] in-memory determinista para
// testes e wiring de referência: mapeia sujeito → capabilities-fonte. NUNCA usar
// em produção (a fonte real é o directório de identidade/RBAC do GOV). É imutável
// após construção via encadeamento de [StaticAuthoritySource.Set]; segura para
// leitura concorrente.
type StaticAuthoritySource struct {
	m map[string][]string
}

// NewStaticAuthoritySource constrói uma fonte estática vazia.
func NewStaticAuthoritySource() *StaticAuthoritySource {
	return &StaticAuthoritySource{m: map[string][]string{}}
}

// Set regista as capabilities-fonte de um sujeito (utilizador humano ou agente) e
// devolve a própria fonte para encadeamento. As capabilities são copiadas
// (defesa contra mutação partilhada) e normalizadas.
func (s *StaticAuthoritySource) Set(subject string, caps ...string) *StaticAuthoritySource {
	s.m[subject] = Normalize(caps)
	return s
}

// Authority implementa [AuthoritySource].
func (s *StaticAuthoritySource) Authority(subject string) ([]string, bool) {
	caps, ok := s.m[subject]
	if !ok {
		return nil, false
	}
	return append([]string(nil), caps...), true
}
