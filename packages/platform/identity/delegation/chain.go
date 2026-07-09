package delegation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// HumanPrefix é o prefixo obrigatório do sujeito da raiz da cadeia. A raiz é
// SEMPRE um humano responsável ("human:<user_id>"); qualquer outra coisa torna a
// cadeia órfã (ErrOrphanChain).
const HumanPrefix = "human:"

// Link é um elo da cadeia de delegação on-behalf-of: o sujeito Sub delega para
// ActAs a autoridade Authority, na profundidade Depth, encadeado ao elo anterior
// por PrevHash.
type Link struct {
	// Sub é quem delega neste elo. Na raiz é "human:<user_id>"; nos elos
	// seguintes é o agente-pai.
	Sub string `json:"sub"`
	// ActAs é para quem se delega (o agente que passa a agir on-behalf-of Sub).
	ActAs string `json:"act_as"`
	// Authority é o escopo (capabilities) concedido NESTE elo. Estreita
	// monotonicamente ao descer a cadeia (nunca alarga).
	Authority []string `json:"authority,omitempty"`
	// Depth é a profundidade do elo (0 na raiz, +1 por delegação).
	Depth int `json:"depth"`
	// PrevHash é o hash do elo anterior (vazio na raiz — âncora de génese). É o
	// que torna a ORDEM da cadeia tamper-evident.
	PrevHash string `json:"prev_hash,omitempty"`
}

// linkDigest é a projecção canónica de um elo para hashing. Usa uma cópia com a
// autoridade normalizada (nil→[]) para que a serialização seja estável.
type linkDigest struct {
	Sub       string   `json:"sub"`
	ActAs     string   `json:"act_as"`
	Authority []string `json:"authority"`
	Depth     int      `json:"depth"`
	PrevHash  string   `json:"prev_hash"`
}

// LinkHash devolve o hash canónico (sha256 hex) de um elo. É determinístico: a
// serialização JSON de [linkDigest] tem ordem de campos fixa e autoridade
// normalizada. Um elo seguinte referencia este valor no seu PrevHash.
func LinkHash(l Link) string {
	auth := l.Authority
	if auth == nil {
		auth = []string{}
	}
	b, _ := json.Marshal(linkDigest{
		Sub:       l.Sub,
		ActAs:     l.ActAs,
		Authority: auth,
		Depth:     l.Depth,
		PrevHash:  l.PrevHash,
	})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Chain é a cadeia de delegação ordenada da RAIZ (índice 0, humano) até à FOLHA
// (o agente actual).
type Chain []Link

// IsHuman indica se um sujeito é um humano responsável (prefixo "human:").
func IsHuman(sub string) bool {
	return strings.HasPrefix(sub, HumanPrefix) && len(sub) > len(HumanPrefix)
}

// Clone devolve uma cópia profunda da cadeia (elos e slices de autoridade), para
// que o embutimento no token nunca partilhe estado com o chamador.
func (c Chain) Clone() Chain {
	if c == nil {
		return nil
	}
	out := make(Chain, len(c))
	for i, l := range c {
		cp := l
		if l.Authority != nil {
			cp.Authority = append([]string(nil), l.Authority...)
		}
		out[i] = cp
	}
	return out
}

// Leaf devolve o último elo (o agente actual) e true; false se a cadeia é vazia.
func (c Chain) Leaf() (Link, bool) {
	if len(c) == 0 {
		return Link{}, false
	}
	return c[len(c)-1], true
}

// Root devolve o primeiro elo (a raiz humana) e true; false se a cadeia é vazia.
func (c Chain) Root() (Link, bool) {
	if len(c) == 0 {
		return Link{}, false
	}
	return c[0], true
}

// NewRoot constrói a cadeia inicial de uma NHI criada on-behalf-of um humano. O
// human tem de ter o prefixo "human:" (senão ErrOrphanChain); agent é o agente
// raiz; authority é o escopo concedido. Profundidade 0, PrevHash vazio.
func NewRoot(human, agent string, authority []string) (Chain, error) {
	if agent == "" {
		return nil, ErrInvalidLink
	}
	if !IsHuman(human) {
		return nil, ErrOrphanChain
	}
	return Chain{{
		Sub:       human,
		ActAs:     agent,
		Authority: append([]string(nil), authority...),
		Depth:     0,
		PrevHash:  "",
	}}, nil
}

// Extend acrescenta um elo à cadeia, delegando para agent a autoridade dada. O
// Sub do novo elo é o ActAs da folha corrente (o agente-pai passa a delegante);
// Depth = folha.Depth+1; PrevHash = hash(folha). REJEITA fail-closed com
// [ErrScopeEscalation] se authority não for subconjunto da autoridade da folha
// (a autoridade só pode estreitar ao descer). A cadeia original não é mutada.
func (c Chain) Extend(agent string, authority []string) (Chain, error) {
	leaf, ok := c.Leaf()
	if !ok {
		return nil, ErrEmptyChain
	}
	if agent == "" {
		return nil, ErrInvalidLink
	}
	if !subset(authority, leaf.Authority) {
		return nil, ErrScopeEscalation
	}
	next := Link{
		Sub:       leaf.ActAs,
		ActAs:     agent,
		Authority: append([]string(nil), authority...),
		Depth:     leaf.Depth + 1,
		PrevHash:  LinkHash(leaf),
	}
	out := make(Chain, 0, len(c)+1)
	out = append(out, c.Clone()...)
	out = append(out, next)
	return out, nil
}

// Verify percorre a cadeia da raiz à folha e impõe todas as invariantes de
// AOS-006 (ver doc do pacote). Devolve o primeiro erro sentinela encontrado
// (comparável com errors.Is) — fail-closed. Uma cadeia vazia é ErrEmptyChain.
func (c Chain) Verify() error {
	if len(c) == 0 {
		return ErrEmptyChain
	}
	// Raiz: tem de ser um humano responsável, profundidade 0, sem PrevHash.
	root := c[0]
	if root.Sub == "" || root.ActAs == "" {
		return ErrInvalidLink
	}
	if !IsHuman(root.Sub) {
		return ErrOrphanChain
	}
	if root.Depth != 0 || root.PrevHash != "" {
		return ErrDepthNonMonotonic
	}
	prev := root
	for i := 1; i < len(c); i++ {
		cur := c[i]
		if cur.Sub == "" || cur.ActAs == "" {
			return ErrInvalidLink
		}
		// Profundidade monotónica +1.
		if cur.Depth != prev.Depth+1 {
			return ErrDepthNonMonotonic
		}
		// Continuidade: encadeamento de hash intacto (ordem tamper-evident).
		if cur.PrevHash != LinkHash(prev) {
			return ErrHashMismatch
		}
		// Não-escalada: autoridade(i) ⊆ autoridade(i-1).
		if !subset(cur.Authority, prev.Authority) {
			return ErrScopeEscalation
		}
		prev = cur
	}
	return nil
}

// HumanPrincipal resolve o humano responsável único na raiz da cadeia. Falha com
// ErrEmptyChain se vazia ou ErrOrphanChain se a raiz não for um humano — NUNCA
// devolve um principal não-humano (base da reconstrução de autoria).
func (c Chain) HumanPrincipal() (string, error) {
	root, ok := c.Root()
	if !ok {
		return "", ErrEmptyChain
	}
	if !IsHuman(root.Sub) {
		return "", ErrOrphanChain
	}
	return root.Sub, nil
}

// subset indica se todos os elementos de a estão em b (a ⊆ b). O conjunto vazio
// é subconjunto de qualquer conjunto (estreitar até nada é sempre legítimo).
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
