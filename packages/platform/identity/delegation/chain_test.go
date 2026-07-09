package delegation

import (
	"errors"
	"testing"
)

// buildChain constrói uma cadeia raiz→folha válida com Extend, falhando o teste
// se algum passo escalar. Cada nível estreita (ou mantém) a autoridade.
func buildChain(t *testing.T, human string, levels []struct {
	agent     string
	authority []string
}) Chain {
	t.Helper()
	if len(levels) == 0 {
		t.Fatal("levels vazio")
	}
	c, err := NewRoot(human, levels[0].agent, levels[0].authority)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	for _, lv := range levels[1:] {
		c, err = c.Extend(lv.agent, lv.authority)
		if err != nil {
			t.Fatalf("Extend(%s): %v", lv.agent, err)
		}
	}
	return c
}

// ---------------------------------------------------------------------------
// Propagação pai→filho→neto até humano: a cadeia resolve à raiz human:*.
// ---------------------------------------------------------------------------

func TestChain_PropagationToHuman(t *testing.T) {
	t.Parallel()
	c := buildChain(t, "human:alice", []struct {
		agent     string
		authority []string
	}{
		{"orchestrator", []string{"cap:http.get", "cap:fs.read", "cap:db.read"}},
		{"planner", []string{"cap:http.get", "cap:fs.read"}},
		{"worker", []string{"cap:http.get"}},
	})

	if err := c.Verify(); err != nil {
		t.Fatalf("cadeia valida devia verificar, obtive %v", err)
	}
	if len(c) != 3 {
		t.Fatalf("esperava 3 elos, obtive %d", len(c))
	}
	human, err := c.HumanPrincipal()
	if err != nil {
		t.Fatalf("HumanPrincipal: %v", err)
	}
	if human != "human:alice" {
		t.Fatalf("human_principal=%q, esperava human:alice", human)
	}
	// Encadeamento: Sub de cada elo == ActAs do anterior; Depth monotónico.
	if c[1].Sub != "orchestrator" || c[2].Sub != "planner" {
		t.Fatalf("encadeamento de sujeitos errado: %+v", c)
	}
	if c[0].Depth != 0 || c[1].Depth != 1 || c[2].Depth != 2 {
		t.Fatalf("profundidades erradas: %d,%d,%d", c[0].Depth, c[1].Depth, c[2].Depth)
	}
	leaf, _ := c.Leaf()
	if leaf.ActAs != "worker" {
		t.Fatalf("folha errada: %+v", leaf)
	}
}

// ---------------------------------------------------------------------------
// Recusa de escopo alargado no filho (escalada).
// ---------------------------------------------------------------------------

func TestChain_EscalationRejected(t *testing.T) {
	t.Parallel()
	c, err := NewRoot("human:alice", "orchestrator", []string{"cap:http.get"})
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	// Extend pede uma capability que o pai não tem ⇒ escalada.
	if _, err := c.Extend("planner", []string{"cap:http.get", "cap:db.write"}); !errors.Is(err, ErrScopeEscalation) {
		t.Fatalf("Extend com escalada devia dar ErrScopeEscalation, obtive %v", err)
	}

	// Uma cadeia forjada (autoridade do filho alarga) tem de falhar Verify.
	forged := Chain{
		{Sub: "human:alice", ActAs: "orchestrator", Authority: []string{"cap:http.get"}, Depth: 0},
	}
	next := Link{
		Sub: "orchestrator", ActAs: "planner",
		Authority: []string{"cap:http.get", "cap:db.write"}, // alarga
		Depth:     1, PrevHash: LinkHash(forged[0]),
	}
	forged = append(forged, next)
	if err := forged.Verify(); !errors.Is(err, ErrScopeEscalation) {
		t.Fatalf("cadeia com escalada devia falhar Verify com ErrScopeEscalation, obtive %v", err)
	}
}

// ---------------------------------------------------------------------------
// Detecção de cadeia órfã (raiz não-humana).
// ---------------------------------------------------------------------------

func TestChain_OrphanRejected(t *testing.T) {
	t.Parallel()
	// NewRoot recusa uma raiz não-humana à cabeça.
	if _, err := NewRoot("orchestrator", "planner", []string{"cap:x"}); !errors.Is(err, ErrOrphanChain) {
		t.Fatalf("NewRoot nao-humano devia dar ErrOrphanChain, obtive %v", err)
	}

	// Uma cadeia cuja raiz não é humana é órfã em Verify e HumanPrincipal.
	orphan := Chain{
		{Sub: "agt-root", ActAs: "planner", Authority: []string{"cap:x"}, Depth: 0},
	}
	if err := orphan.Verify(); !errors.Is(err, ErrOrphanChain) {
		t.Fatalf("cadeia orfa devia falhar Verify com ErrOrphanChain, obtive %v", err)
	}
	if _, err := orphan.HumanPrincipal(); !errors.Is(err, ErrOrphanChain) {
		t.Fatalf("HumanPrincipal de orfa devia dar ErrOrphanChain, obtive %v", err)
	}
}

// ---------------------------------------------------------------------------
// Adulteração da ordem: encadeamento de hash quebrado.
// ---------------------------------------------------------------------------

func TestChain_TamperedOrderRejected(t *testing.T) {
	t.Parallel()
	c := buildChain(t, "human:alice", []struct {
		agent     string
		authority []string
	}{
		{"orchestrator", []string{"cap:a", "cap:b"}},
		{"planner", []string{"cap:a"}},
	})
	if err := c.Verify(); err != nil {
		t.Fatalf("cadeia base devia verificar: %v", err)
	}

	// Adulterar o Sub da raiz (mantendo o PrevHash do filho) quebra o hash.
	tampered := c.Clone()
	tampered[0].Sub = "human:mallory"
	if err := tampered.Verify(); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("adulteracao devia dar ErrHashMismatch, obtive %v", err)
	}

	// Profundidade não-monotónica.
	badDepth := c.Clone()
	badDepth[1].Depth = 5
	if err := badDepth.Verify(); !errors.Is(err, ErrDepthNonMonotonic) {
		t.Fatalf("profundidade errada devia dar ErrDepthNonMonotonic, obtive %v", err)
	}
}

// ---------------------------------------------------------------------------
// Casos degenerados.
// ---------------------------------------------------------------------------

func TestChain_Degenerate(t *testing.T) {
	t.Parallel()
	var empty Chain
	if err := empty.Verify(); !errors.Is(err, ErrEmptyChain) {
		t.Fatalf("cadeia vazia devia dar ErrEmptyChain, obtive %v", err)
	}
	if _, err := empty.HumanPrincipal(); !errors.Is(err, ErrEmptyChain) {
		t.Fatalf("HumanPrincipal vazia devia dar ErrEmptyChain, obtive %v", err)
	}
	if _, ok := empty.Leaf(); ok {
		t.Fatal("Leaf de cadeia vazia devia dar ok=false")
	}
	if _, err := empty.Extend("x", nil); !errors.Is(err, ErrEmptyChain) {
		t.Fatalf("Extend de vazia devia dar ErrEmptyChain, obtive %v", err)
	}

	// Elo raiz sem ActAs.
	if _, err := NewRoot("human:a", "", nil); !errors.Is(err, ErrInvalidLink) {
		t.Fatalf("NewRoot sem agente devia dar ErrInvalidLink, obtive %v", err)
	}

	// Autoridade vazia é subconjunto de qualquer coisa (estreitar até nada).
	c, _ := NewRoot("human:a", "root", []string{"cap:x"})
	narrowed, err := c.Extend("leaf", nil)
	if err != nil {
		t.Fatalf("estreitar ate vazio devia ser legitimo: %v", err)
	}
	if err := narrowed.Verify(); err != nil {
		t.Fatalf("cadeia estreitada devia verificar: %v", err)
	}
}

// LinkHash é determinístico e sensível a cada campo.
func TestLinkHash_Deterministic(t *testing.T) {
	t.Parallel()
	l := Link{Sub: "human:a", ActAs: "b", Authority: []string{"cap:x"}, Depth: 0}
	if LinkHash(l) != LinkHash(l) {
		t.Fatal("LinkHash nao deterministico")
	}
	l2 := l
	l2.ActAs = "c"
	if LinkHash(l) == LinkHash(l2) {
		t.Fatal("LinkHash devia mudar com o ActAs")
	}
	// nil vs [] de autoridade produzem o mesmo hash (normalização).
	a := Link{Sub: "human:a", ActAs: "b", Authority: nil, Depth: 0}
	b := Link{Sub: "human:a", ActAs: "b", Authority: []string{}, Depth: 0}
	if LinkHash(a) != LinkHash(b) {
		t.Fatal("nil e [] de autoridade deviam ter o mesmo hash")
	}
}
