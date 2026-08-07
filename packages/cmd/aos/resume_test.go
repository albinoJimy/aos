package main

import (
	"context"
	"errors"
	"io"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// spyModel conta as chamadas AO VIVO e devolve uma resposta fixa.
type spyModel struct {
	calls int
	resp  agentruntime.ModelResponse
}

func (m *spyModel) Call(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	m.calls++
	return m.resp, nil
}

// TestResumeModel_ReproduzTurnosCapturados é o coração do item 3: num turno coberto pelo
// plano, a resposta vem da CAPTURA e o modelo NÃO é chamado. Sem isto a retoma
// reinterrogaria o modelo, que emitiria outra tool call — outra preview — e a aprovação
// nunca se aplicaria.
func TestResumeModel_ReproduzTurnosCapturados(t *testing.T) {
	vivo := &spyModel{resp: agentruntime.ModelResponse{Text: "AO VIVO"}}
	client := newResumeAwareModelClient(vivo)

	plan := replayPlan{
		1: {Text: "turno 1 REGISTADO"},
		2: {Text: "turno 2 REGISTADO"},
	}
	ctx := withReplayPlan(context.Background(), plan)

	// Turnos cobertos: vêm da captura, sem tocar no modelo.
	for turno := 1; turno <= 2; turno++ {
		resp, err := client.Call(ctx, agentruntime.PromptView{Turn: turno})
		if err != nil {
			t.Fatalf("turno %d: %v", turno, err)
		}
		if resp.Text != plan[turno].Text {
			t.Fatalf("turno %d devia vir da captura, veio %q", turno, resp.Text)
		}
	}
	if vivo.calls != 0 {
		t.Fatalf("o modelo NÃO podia ser chamado nos turnos reproduzidos; chamadas=%d", vivo.calls)
	}

	// Turno seguinte: já não há captura ⇒ modelo AO VIVO.
	resp, err := client.Call(ctx, agentruntime.PromptView{Turn: 3})
	if err != nil {
		t.Fatalf("turno 3: %v", err)
	}
	if resp.Text != "AO VIVO" || vivo.calls != 1 {
		t.Fatalf("o turno 3 devia ir ao vivo; texto=%q chamadas=%d", resp.Text, vivo.calls)
	}
}

// TestResumeModel_SemPlanoEhTransparente: um run NORMAL nunca tem plano no contexto — o
// decorador não pode alterar nada.
func TestResumeModel_SemPlanoEhTransparente(t *testing.T) {
	vivo := &spyModel{resp: agentruntime.ModelResponse{Text: "AO VIVO"}}
	client := newResumeAwareModelClient(vivo)
	for turno := 1; turno <= 3; turno++ {
		resp, err := client.Call(context.Background(), agentruntime.PromptView{Turn: turno})
		if err != nil || resp.Text != "AO VIVO" {
			t.Fatalf("sem plano tudo devia ir ao vivo; texto=%q err=%v", resp.Text, err)
		}
	}
	if vivo.calls != 3 {
		t.Fatalf("sem plano o modelo devia ser chamado 3x, foi %d", vivo.calls)
	}
}

// TestResumeModel_PlanoNaoAtravessaContextos sela o isolamento: o plano viaja no ctx da
// retoma e não contamina outro run. É a razão de ser desta abordagem — o PromptView só
// transporta o Turn, pelo que um decorador global não saberia que run está a servir.
func TestResumeModel_PlanoNaoAtravessaContextos(t *testing.T) {
	vivo := &spyModel{resp: agentruntime.ModelResponse{Text: "AO VIVO"}}
	client := newResumeAwareModelClient(vivo)

	ctxRetoma := withReplayPlan(context.Background(), replayPlan{1: {Text: "REGISTADO"}})
	ctxNormal := context.Background()

	if resp, _ := client.Call(ctxRetoma, agentruntime.PromptView{Turn: 1}); resp.Text != "REGISTADO" {
		t.Fatalf("no ctx da retoma o turno 1 devia vir da captura, veio %q", resp.Text)
	}
	if resp, _ := client.Call(ctxNormal, agentruntime.PromptView{Turn: 1}); resp.Text != "AO VIVO" {
		t.Fatalf("noutro ctx o MESMO turno tem de ir ao vivo, veio %q", resp.Text)
	}
}

// TestResumeModel_PlanoVazioNaoEntraNoContexto: um plano vazio não altera o ctx (evita
// carregar valores inúteis em todos os runs).
func TestResumeModel_PlanoVazioNaoEntraNoContexto(t *testing.T) {
	base := context.Background()
	if withReplayPlan(base, nil) != base {
		t.Fatal("plano nil não devia alterar o ctx")
	}
	if withReplayPlan(base, replayPlan{}) != base {
		t.Fatal("plano vazio não devia alterar o ctx")
	}
}

// TestResume_RunNaoSuspenso: retomar o que não está à espera é recusado (e a API traduz
// isto para o 404 uniforme, que não enumera runs alheios).
func TestResume_RunNaoSuspenso(t *testing.T) {
	svc := &NodeService{
		node:      &Node{},
		runs:      make(map[string]*runState),
		completed: make(map[string]*runState),
		suspended: make(map[string]*runState),
		logw:      io.Discard,
	}
	// Sem ResumeRecords composto ⇒ indisponível (four-eyes desligado).
	if err := svc.Resume(context.Background(), "run-x", "cred"); !errors.Is(err, ErrResumeUnavailable) {
		t.Fatalf("sem registo de retoma composto devia dar ErrResumeUnavailable; err=%v", err)
	}
}

// TestResume_RunIDVazio: fail-closed no argumento.
func TestResume_RunIDVazio(t *testing.T) {
	svc := &NodeService{node: &Node{}, logw: io.Discard}
	if err := svc.Resume(context.Background(), "", "cred"); !errors.Is(err, ErrEmptyRunID) {
		t.Fatalf("runID vazio devia dar ErrEmptyRunID; err=%v", err)
	}
}
