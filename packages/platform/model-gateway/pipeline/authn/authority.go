package authn

import (
	"context"
	"sort"
)

// AUTORIDADE EFECTIVA = utilizador ∩ classe de agente (AOS-057, tecnica/06 §4).
// É o menor privilégio: a intersecção das capabilities que o UTILIZADOR possui
// com as que a CLASSE do agente concede. Nenhum dos eixos pode alargar o outro —
// o resultado é sempre subconjunto de AMBOS.

// AuthorityResolver resolve as capabilities do utilizador e da classe de agente
// (as duas fontes da intersecção). A implementação real liga-se ao directório de
// identidade/RBAC (GOV); aqui há uma implementação estática determinista.
type AuthorityResolver interface {
	// UserAuthority devolve as capabilities do utilizador (humano responsável).
	UserAuthority(ctx context.Context, userID string) ([]string, error)
	// ClassAuthority devolve as capabilities-máximas da classe de agente.
	ClassAuthority(ctx context.Context, class string) ([]string, error)
}

// EffectiveAuthority calcula utilizador ∩ classe: a intersecção ORDENADA e sem
// duplicados das duas listas de capabilities. Determinista (ordenada) para que o
// registo de atribuição e a serialização sejam estáveis. O resultado é
// subconjunto de ambos os operandos (menor privilégio).
func EffectiveAuthority(userAuthority, classAuthority []string) []string {
	if len(userAuthority) == 0 || len(classAuthority) == 0 {
		return nil
	}
	inClass := make(map[string]struct{}, len(classAuthority))
	for _, c := range classAuthority {
		inClass[c] = struct{}{}
	}
	seen := make(map[string]struct{}, len(userAuthority))
	var out []string
	for _, u := range userAuthority {
		if _, ok := inClass[u]; !ok {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

// contains indica se v está em list.
func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// subset indica se todos os elementos de a estão em b (a ⊆ b). O conjunto vazio é
// subconjunto de qualquer conjunto.
func subset(a, b []string) bool {
	if len(a) == 0 {
		return true
	}
	in := make(map[string]struct{}, len(b))
	for _, x := range b {
		in[x] = struct{}{}
	}
	for _, x := range a {
		if _, ok := in[x]; !ok {
			return false
		}
	}
	return true
}

// StaticAuthority é uma [AuthorityResolver] in-memory determinista (testes/wiring
// de referência): mapas utilizador→capabilities e classe→capabilities. NUNCA usar
// em produção (a fonte real é o directório de identidade/RBAC).
type StaticAuthority struct {
	users   map[string][]string
	classes map[string][]string
}

// NewStaticAuthority constrói um resolver estático vazio.
func NewStaticAuthority() *StaticAuthority {
	return &StaticAuthority{
		users:   map[string][]string{},
		classes: map[string][]string{},
	}
}

// SetUser regista as capabilities de um utilizador.
func (s *StaticAuthority) SetUser(userID string, caps ...string) *StaticAuthority {
	s.users[userID] = append([]string(nil), caps...)
	return s
}

// SetClass regista as capabilities-máximas de uma classe de agente.
func (s *StaticAuthority) SetClass(class string, caps ...string) *StaticAuthority {
	s.classes[class] = append([]string(nil), caps...)
	return s
}

// UserAuthority implementa [AuthorityResolver]. Um utilizador desconhecido tem
// autoridade vazia (a intersecção será vazia ⇒ deny fail-closed a jusante).
func (s *StaticAuthority) UserAuthority(_ context.Context, userID string) ([]string, error) {
	return append([]string(nil), s.users[userID]...), nil
}

// ClassAuthority implementa [AuthorityResolver]. Uma classe desconhecida tem
// autoridade vazia (deny fail-closed a jusante).
func (s *StaticAuthority) ClassAuthority(_ context.Context, class string) ([]string, error) {
	return append([]string(nil), s.classes[class]...), nil
}
