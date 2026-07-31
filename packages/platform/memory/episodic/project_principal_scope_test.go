package episodic

import (
	"context"
	"errors"
	"testing"
)

// TestProjectScopedByPrincipalNoCrossLeak é o teste FALSIFICÁVEL do escopo por
// principal no caminho de PROVA POR-ID (Project) — a lacuna de fronteira que o achado
// #8 (AOS-224) deixara aberta: sem escopo, Project devolvia a projecção DECIFRADA de
// QUALQUER episódio a quem soubesse o id. Dois principais (agent-A e agent-B) escrevem
// memória no mesmo store; o Project de A pelo id de B NÃO devolve o conteúdo de B —
// devolve ErrEpisodeNotFound (indistinguível de inexistente: não-oráculo de existência).
//
// Como FALHA-ANTES: sem o filtro env.AgentID == principalID em Project, a chamada
// s.Project(ctx, "agent-A", "ep-b1") decifraria e devolveria a projecção de ep-b1 (com
// TraceID "trace-run-b1") — a asserção de que devolve ErrEpisodeNotFound e NÃO o
// conteúdo de B falharia. Prova a não-vacuidade: o próprio principal continua a provar
// o SEU episódio (ep-a1), logo o escopo separa por identidade, não bloqueia tudo.
func TestProjectScopedByPrincipalNoCrossLeak(t *testing.T) {
	es := newES(t)
	s, _, _ := newStore(t, es)
	ctx := context.Background()

	mustEnqueue(t, s,
		inputFor("agent-A", "ep-a1", "subj-a", "run-a1", "shared-goal", []string{"t"}),
		inputFor("agent-B", "ep-b1", "subj-b", "run-b1", "shared-goal", []string{"t"}),
	)
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// O próprio principal PROVA o seu episódio (projecção decifrada correcta).
	iv, err := s.Project(ctx, "agent-A", "ep-a1")
	if err != nil {
		t.Fatalf("Project(agent-A, ep-a1): %v", err)
	}
	if iv.TraceID != "trace-run-a1" {
		t.Fatalf("projecção de ep-a1 inesperada: %q", iv.TraceID)
	}

	// VAZAMENTO cross-principal: agent-A prova pelo id de agent-B. Tem de RECUSAR com
	// ErrEpisodeNotFound (não-oráculo) e NUNCA devolver o conteúdo de B.
	leaked, err := s.Project(ctx, "agent-A", "ep-b1")
	if !errors.Is(err, ErrEpisodeNotFound) {
		t.Fatalf("Project(agent-A, ep-b1)=%v, esperado ErrEpisodeNotFound (escopo por principal)", err)
	}
	if leaked.TraceID == "trace-run-b1" {
		t.Fatalf("VAZAMENTO cross-principal: Project de agent-A devolveu a projecção de ep-b1 (%q)", leaked.TraceID)
	}

	// Simétrico: agent-B prova o seu, mas não o de A.
	if _, err := s.Project(ctx, "agent-B", "ep-b1"); err != nil {
		t.Fatalf("Project(agent-B, ep-b1): %v", err)
	}
	if _, err := s.Project(ctx, "agent-B", "ep-a1"); !errors.Is(err, ErrEpisodeNotFound) {
		t.Fatalf("Project(agent-B, ep-a1)=%v, esperado ErrEpisodeNotFound", err)
	}
}

// TestProjectEmptyPrincipalFailClosed prova a guarda FAIL-CLOSED do caminho de prova
// por-id: um Project SEM principal verificado (principalID vazio) é RECUSADO com
// ErrMissingPrincipal — nunca devolve conteúdo por omissão do escopo.
//
// Falha-antes: sem a guarda no topo de Project, um principalID vazio percorreria o log
// e devolveria a projecção do episódio que casa o id (conteúdo alheio) em vez de recusar.
func TestProjectEmptyPrincipalFailClosed(t *testing.T) {
	es := newES(t)
	s, _, _ := newStore(t, es)
	ctx := context.Background()

	mustEnqueue(t, s, inputFor("agent-A", "ep-a1", "subj-a", "run-a1", "g", []string{"t"}))
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	got, err := s.Project(ctx, "", "ep-a1") // principal vazio
	if !errors.Is(err, ErrMissingPrincipal) {
		t.Fatalf("Project(sem principal)=%v, esperado ErrMissingPrincipal (fail-closed)", err)
	}
	if got.TraceID != "" {
		t.Fatalf("Project(sem principal) devolveu conteúdo (%q) — devia recusar sem nada", got.TraceID)
	}
}
