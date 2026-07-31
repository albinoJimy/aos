package episodic

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/platform/memory/domain"
)

// inputFor constrói um EpisodeInput com um AgentID (principal) ARBITRÁRIO — ao
// contrário de baseInput (que fixa "agent-1"), permite dois principais distintos
// escreverem memória no MESMO store.
func inputFor(principal, episodeID, subject, run, goal string, tags []string) EpisodeInput {
	in := baseInput(episodeID, subject, run, goal, tags, domain.TTLPermanent, 2)
	in.AgentID = principal
	return in
}

// TestRecallScopedByPrincipalNoCrossLeak é o teste FALSIFICÁVEL do achado #8
// (AOS-224): dois principais (agent-A e agent-B) escrevem memória no mesmo store;
// o recall de A NÃO devolve a memória de B, e vice-versa.
//
// Como FALHA-ANTES: sem o escopo por principal em Recall (o filtro env.AgentID ==
// PrincipalID), a consulta por objectivo/tags devolveria TODOS os episódios que
// casam — incluindo os do outro principal — e as asserções de isolamento abaixo
// falhariam (recall de A veria ep de B). Prova a não-vacuidade: os dois principais
// partilham objectivo e tags, logo sem o escopo o resultado NÃO seria disjunto.
func TestRecallScopedByPrincipalNoCrossLeak(t *testing.T) {
	es := newES(t)
	s, _, _ := newStore(t, es)
	ctx := context.Background()

	// Mesmo Goal e mesmas Tags nos dois principais: a ÚNICA dimensão que separa os
	// resultados é o principal. Se o escopo não existir, ambos casariam a consulta.
	mustEnqueue(t, s,
		inputFor("agent-A", "ep-a1", "subj-a", "run-a1", "shared-goal", []string{"t"}),
		inputFor("agent-A", "ep-a2", "subj-a", "run-a2", "shared-goal", []string{"t"}),
		inputFor("agent-B", "ep-b1", "subj-b", "run-b1", "shared-goal", []string{"t"}),
	)
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Recall de A: só vê os episódios de A (ep-a1, ep-a2) — NUNCA ep-b1.
	got, err := s.Recall(ctx, Query{PrincipalID: "agent-A", Goal: "shared-goal", Tags: []string{"t"}})
	if err != nil {
		t.Fatalf("Recall(agent-A): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("recall(agent-A)=%d episódios, esperado 2 (só os de A)", len(got))
	}
	for _, ep := range got {
		if ep.AgentID != "agent-A" {
			t.Fatalf("VAZAMENTO cross-principal: recall de agent-A devolveu ep %q de %q", ep.EpisodeID, ep.AgentID)
		}
		if ep.EpisodeID == "ep-b1" {
			t.Fatalf("VAZAMENTO cross-principal: recall de agent-A devolveu ep-b1 (memória de agent-B)")
		}
	}

	// Recall de B: só vê ep-b1 — NUNCA os de A.
	gotB, err := s.Recall(ctx, Query{PrincipalID: "agent-B", Goal: "shared-goal", Tags: []string{"t"}})
	if err != nil {
		t.Fatalf("Recall(agent-B): %v", err)
	}
	if len(gotB) != 1 || gotB[0].EpisodeID != "ep-b1" {
		t.Fatalf("recall(agent-B)=%+v, esperado só ep-b1", gotB)
	}
}

// TestRecallEmptyPrincipalFailClosed prova a guarda FAIL-CLOSED: um recall SEM
// principal verificado (PrincipalID vazio) é RECUSADO com ErrMissingPrincipal —
// nunca devolve memória alheia por omissão do escopo.
//
// Falha-antes: sem a guarda no topo de Recall, um PrincipalID vazio percorreria o
// log e (sem filtro de principal correspondente) devolveria vazio silenciosamente
// OU, pior, todos os episódios — em vez de recusar. A asserção de que o erro é
// ErrMissingPrincipal falharia.
func TestRecallEmptyPrincipalFailClosed(t *testing.T) {
	es := newES(t)
	s, _, _ := newStore(t, es)
	ctx := context.Background()

	mustEnqueue(t, s, inputFor("agent-A", "ep-a1", "subj-a", "run-a1", "g", []string{"t"}))
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	got, err := s.Recall(ctx, Query{Goal: "g", Tags: []string{"t"}}) // PrincipalID vazio
	if !errors.Is(err, ErrMissingPrincipal) {
		t.Fatalf("Recall(sem principal)=%v, esperado ErrMissingPrincipal (fail-closed)", err)
	}
	if len(got) != 0 {
		t.Fatalf("Recall(sem principal) devolveu %d episódios — devia recusar sem devolver nada", len(got))
	}
}

// TestRecallUnknownPrincipalEmpty prova que um recuo cross-principal (um principal
// SEM episódios seus, a pedir sob um objectivo que existe para OUTRO principal)
// resolve para VAZIO — não para o conteúdo alheio.
//
// Falha-antes: sem o escopo, a consulta por "g" devolveria o episódio de agent-A
// (o único que casa), e o comprimento seria 1 em vez de 0.
func TestRecallUnknownPrincipalEmpty(t *testing.T) {
	es := newES(t)
	s, _, _ := newStore(t, es)
	ctx := context.Background()

	mustEnqueue(t, s, inputFor("agent-A", "ep-a1", "subj-a", "run-a1", "g", []string{"t"}))
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	got, err := s.Recall(ctx, Query{PrincipalID: "agent-Z", Goal: "g", Tags: []string{"t"}})
	if err != nil {
		t.Fatalf("Recall(agent-Z): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("recall(agent-Z)=%d, esperado 0 (recuo cross-principal ⇒ vazio, não alheio)", len(got))
	}
}
