package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/toolset"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-486 — o que o run OFERECE ao modelo segue a lista-branca; o que o run CONGELA não.
//
// Medido em produção a 2026-10-02 (run `plan-e2e-pegadas-1790956072~n2_summarize`, lista vazia):
// o manifesto dos dois turnos listava `doc_read`, o modelo pediu-a, e a chamada foi negada. O
// bloco TOOLSET do prefixo e o `tools` do manifesto saem de `Goal.Tools`, que [ApplyFrozenToGoal]
// preenchia com o snapshot inteiro sem olhar para a lista.

// aos486Nomes projecta os nomes de uma lista de specs, pela ordem.
func aos486Nomes(specs []agentruntime.ToolSpec) []string {
	out := []string{}
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

// TestAOS486_ApplyFrozenToGoal_OfertaSegueALista fixa a regra na sua única implementação: sem
// lista (nil) o snapshot inteiro; com lista — MESMO VAZIA — a subsequência que ela admite, na
// ordem congelada e não na ordem da lista.
func TestAOS486_ApplyFrozenToGoal_OfertaSegueALista(t *testing.T) {
	ctx := context.Background()
	signer := testSigner(t)
	// Registadas fora de ordem: a ordem congelada é (id, version), e é ela que tem de sair.
	cat := &fakeCatalog{entries: []domain.Entry{
		signedEntry(t, signer, "doc_write", "1.0.0", domain.Contract{Egress: domain.EgressNone}),
		signedEntry(t, signer, "arquivo", "1.0.0", domain.Contract{Egress: domain.EgressNone}),
		signedEntry(t, signer, "doc_read", "1.0.0", domain.Contract{Egress: domain.EgressNone}),
	}}
	frozen, err := toolset.FreezeToolSet(ctx, cat, "run-486", nil, toolset.WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("FreezeToolSet: %v", err)
	}
	inteiro := []string{"arquivo", "doc_read", "doc_write"}
	if got := aos486Nomes(frozen.Specs()); !reflect.DeepEqual(got, inteiro) {
		t.Fatalf("preparação: o snapshot devia congelar %v, congelou %v", inteiro, got)
	}

	casos := []struct {
		nome  string
		lista []string
		quer  []string
	}{
		{"sem lista (nil): o snapshot inteiro", nil, inteiro},
		{"lista vazia: nenhuma", []string{}, []string{}},
		{"uma tool", []string{"doc_read"}, []string{"doc_read"}},
		{"a ordem é a congelada, não a da lista", []string{"doc_write", "arquivo"}, []string{"arquivo", "doc_write"}},
		{"um nome que o nó não tem não aparece", []string{"inexistente", "doc_read"}, []string{"doc_read"}},
		{"só nomes que o nó não tem: nenhuma", []string{"inexistente"}, []string{}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			goal := ApplyFrozenToGoal(agentruntime.Goal{RunID: "outro", AllowedTools: c.lista}, frozen)
			if got := aos486Nomes(goal.Tools); !reflect.DeepEqual(got, c.quer) {
				t.Fatalf("Goal.Tools = %v, quero %v", got, c.quer)
			}
			// A spec que passa é a do snapshot, com a versão e o digest pinados.
			for _, s := range goal.Tools {
				if s.Version != "1.0.0" || s.Digest == "" {
					t.Fatalf("a spec oferecida perdeu o pin: %+v", s)
				}
			}
			// A lista do goal não é tocada: nil fica nil, vazia fica vazia.
			if !reflect.DeepEqual(goal.AllowedTools, c.lista) {
				t.Fatalf("AllowedTools %#v saiu como %#v", c.lista, goal.AllowedTools)
			}
			// O snapshot — o que a revalidação consulta — continua inteiro.
			if got := aos486Nomes(frozen.Specs()); !reflect.DeepEqual(got, inteiro) {
				t.Fatalf("o snapshot congelado foi estreitado para %v", got)
			}
		})
	}
}

// aos486Run corre, pelo composition-root, o goal do AOS-379 (o turno 1 pede `doc_read`, o 2
// conclui) num nó com TRÊS tools no catálogo assinado e a lista-branca dada.
type aos486Run struct {
	execucoes  int
	congelado  []string   // o snapshot que sec.Run devolveu
	noEvento   []string   // as entradas do `run.toolset.frozen` durável
	manifestos [][]string // `manifest.tools` de cada turn.recorded (nil ⇒ campo ausente)
	prefixos   [][]byte   // o prefixo que o modelo viu em cada turno
	permits    uint64
	denials    uint64
}

func aos486Correr(t *testing.T, permitidas []string) aos486Run {
	t.Helper()
	ctx := context.Background()
	cfg, signer, _, goal, cleanup := aos379PermitConfig(t)
	defer cleanup()

	cat := cfg.Catalog.(*fakeCatalog)
	cat.entries = append(cat.entries,
		signedEntry(t, signer, "doc_write", "1.0.0", domain.Contract{Egress: domain.EgressNone}),
		signedEntry(t, signer, "arquivo", "1.0.0", domain.Contract{Egress: domain.EgressNone}),
	)
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()
	cfg.Recorder = agentruntime.NewTurnRecorder(store)
	cfg.ToolSetStore = store // o `run.toolset.frozen` fica no stream do run, como no nó

	sec, err := NewSecuredRuntime(cfg)
	if err != nil {
		t.Fatalf("NewSecuredRuntime: %v", err)
	}
	var r aos486Run
	if err := sec.Register("doc_read", func(_ context.Context, in []byte) ([]byte, error) {
		r.execucoes++
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	goal.AllowedTools = permitidas
	_, frozen, err := sec.Run(ctx, goal, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	r.congelado = aos486Nomes(frozen.Specs())
	r.prefixos = cfg.Model.(*scriptedModel).prefixes
	r.permits, r.denials, _ = sec.Metrics().Snapshot()

	eventos, err := store.Read(ctx, goal.RunID, 1)
	if err != nil {
		t.Fatalf("Read(%q): %v", goal.RunID, err)
	}
	for _, ev := range eventos {
		switch ev.Type {
		case eventTypeFrozenToolSet:
			var dto frozenSnapshotDTO
			if err := json.Unmarshal(ev.Payload, &dto); err != nil {
				t.Fatalf("payload do run.toolset.frozen: %v", err)
			}
			r.noEvento = []string{}
			for _, e := range dto.Entries {
				r.noEvento = append(r.noEvento, e.ID)
			}
		case agentruntime.EventTypeTurnRecorded:
			var p struct {
				Manifest struct {
					Tools []struct {
						Name string `json:"name"`
					} `json:"tools"`
				} `json:"manifest"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("payload do turn.recorded: %v", err)
			}
			var nomes []string
			for _, d := range p.Manifest.Tools {
				nomes = append(nomes, d.Name)
			}
			r.manifestos = append(r.manifestos, nomes)
		}
	}
	if len(r.manifestos) != 2 || len(r.prefixos) != 2 {
		t.Fatalf("o run tinha de ter 2 turnos: %d turn.recorded, %d chamadas ao modelo", len(r.manifestos), len(r.prefixos))
	}
	return r
}

// aos486LinhaDeTool é a linha do bloco TOOLSET do prefixo (prompt.go, buildPrefix).
func aos486LinhaDeTool(nome string) []byte { return []byte("tool\t" + nome + "\t") }

// TestAOS486_RunRestrito_OfertaEstreitaECongeladoInteiro: num run com lista, o prefixo e o
// manifesto levam só as tools da lista, e o tool set congelado, o evento durável e a revalidação
// continuam com o conjunto inteiro — a tool da lista revalida e EXECUTA.
func TestAOS486_RunRestrito_OfertaEstreitaECongeladoInteiro(t *testing.T) {
	inteiro := []string{"arquivo", "doc_read", "doc_write"}
	// A lista vem fora da ordem congelada, de propósito.
	r := aos486Correr(t, []string{"doc_write", "doc_read"})

	if !reflect.DeepEqual(r.congelado, inteiro) {
		t.Fatalf("o tool set congelado do run foi estreitado: %v, quero %v", r.congelado, inteiro)
	}
	if !reflect.DeepEqual(r.noEvento, inteiro) {
		t.Fatalf("o run.toolset.frozen foi estreitado: %v, quero %v", r.noEvento, inteiro)
	}
	// A revalidação consulta o snapshot inteiro e a chamada da lista passa por ela e executa.
	if r.execucoes != 1 || r.permits != 1 || r.denials != 0 {
		t.Fatalf("a tool da lista tinha de revalidar e executar: execs=%d permits=%d denials=%d", r.execucoes, r.permits, r.denials)
	}
	for i, m := range r.manifestos {
		if !reflect.DeepEqual(m, []string{"doc_read", "doc_write"}) {
			t.Fatalf("turno %d: manifest.tools = %v, quero [doc_read doc_write] (ordem congelada)", i+1, m)
		}
	}
	for i, p := range r.prefixos {
		if bytes.Contains(p, aos486LinhaDeTool("arquivo")) {
			t.Fatalf("turno %d: o prefixo oferece `arquivo`, que a lista não admite:\n%s", i+1, p)
		}
		ler, escrever := bytes.Index(p, aos486LinhaDeTool("doc_read")), bytes.Index(p, aos486LinhaDeTool("doc_write"))
		if ler < 0 || escrever < 0 || ler > escrever {
			t.Fatalf("turno %d: o prefixo tinha de listar doc_read e depois doc_write:\n%s", i+1, p)
		}
	}
	if !bytes.Equal(r.prefixos[0], r.prefixos[1]) {
		t.Fatal("o prefixo mudou entre turnos do mesmo run: deixou de ser cache-estável (ADR-009)")
	}
}

// TestAOS486_RunComListaVazia_NadaOferecidoERecusaContinua: a lista vazia não oferece nenhuma
// tool — o manifesto OMITE `tools` e o bloco TOOLSET fica vazio — e a recusa na chamada
// (AOS-413/AOS-485) continua lá: o guião pede `doc_read` na mesma, e o RM nega.
func TestAOS486_RunComListaVazia_NadaOferecidoERecusaContinua(t *testing.T) {
	r := aos486Correr(t, []string{})

	if got := []string{"arquivo", "doc_read", "doc_write"}; !reflect.DeepEqual(r.congelado, got) || !reflect.DeepEqual(r.noEvento, got) {
		t.Fatalf("o congelado tinha de ficar inteiro: snapshot=%v evento=%v", r.congelado, r.noEvento)
	}
	for i, m := range r.manifestos {
		if m != nil {
			t.Fatalf("turno %d: o manifesto de um run com lista vazia lista %v", i+1, m)
		}
	}
	for i, p := range r.prefixos {
		if bytes.Contains(p, []byte("tool\t")) {
			t.Fatalf("turno %d: o prefixo de um run com lista vazia oferece tools:\n%s", i+1, p)
		}
	}
	// O que é oferecido não substitui a recusa.
	if r.execucoes != 0 || r.denials != 1 || r.permits != 0 {
		t.Fatalf("a chamada fora da lista tinha de ser negada pelo RM: execs=%d denials=%d permits=%d", r.execucoes, r.denials, r.permits)
	}
}

// TestAOS486_RunSemLista_OfertaInteira é o controlo: sem lista, o prefixo e o manifesto são o
// snapshot inteiro, e os bytes do prefixo são os que [toolset.FrozenToolSet.Assembler] dá para o
// mesmo snapshot — o run sem lista não mudou.
func TestAOS486_RunSemLista_OfertaInteira(t *testing.T) {
	inteiro := []string{"arquivo", "doc_read", "doc_write"}
	r := aos486Correr(t, nil)

	if !reflect.DeepEqual(r.congelado, inteiro) || !reflect.DeepEqual(r.noEvento, inteiro) {
		t.Fatalf("snapshot=%v evento=%v, quero %v", r.congelado, r.noEvento, inteiro)
	}
	for i, m := range r.manifestos {
		if !reflect.DeepEqual(m, inteiro) {
			t.Fatalf("turno %d: manifest.tools = %v, quero %v", i+1, m, inteiro)
		}
	}
	// Os bytes FIXOS do prefixo de um run sem lista: o system do goal do AOS-379 e as três
	// linhas de tool. Uma alteração que mexesse neles para o run sem lista falhava aqui.
	for i, p := range r.prefixos {
		for _, nome := range inteiro {
			if !bytes.Contains(p, aos486LinhaDeTool(nome)) {
				t.Fatalf("turno %d: o prefixo de um run sem lista perdeu `%s`:\n%s", i+1, nome, p)
			}
		}
		if n := bytes.Count(p, []byte("tool\t")); n != len(inteiro) {
			t.Fatalf("turno %d: %d linhas de tool no prefixo, quero %d", i+1, n, len(inteiro))
		}
	}
	if r.execucoes != 1 || r.permits != 1 {
		t.Fatalf("sem lista a chamada tinha de executar: execs=%d permits=%d", r.execucoes, r.permits)
	}
}
