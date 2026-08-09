package main

// AOS-252 — O DEADLINE QUE DISPARA TEM DE PARAR O RUN (achado F-A5 da auditoria da W0).
//
// O varrimento de deadlines materializava `running→timed_out` no log durável e deixava o run
// A CORRER. É a pior divergência estado↔efeito que este subsistema pode ter: o operador lê um
// estado TERMINAL, deixa de olhar, e o agente continua a emitir tool calls — com o disjuntor
// cego (Observe é no-op fora de `running`) e o selo terminal já a no-op. Um timeout que não
// interrompe é PIOR do que timeout nenhum, precisamente porque é credível.
//
// Este teste é a prova de NÓ dos dois efeitos, no caso que só o varrimento alcança: um run
// PRESO A MEIO DE UM TURNO (o modelo nunca devolve), onde o breaker — que avalia o MESMO tecto
// mas só na fronteira de fim-de-turno — nunca chega a ser consultado. Contra o código antes da
// correcção, o `Wait` estoirava por timeout: o estado ficava `timed_out` e o run vivo.

import (
	"context"
	"io"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// dlPresoModel é o run PATOLÓGICO que motiva o backstop: a chamada ao modelo nunca devolve
// por si — só sai quando o CONTEXTO DO RUN é cancelado. Enquanto lá está, o loop não atinge
// nenhuma fronteira de fim-de-turno, pelo que nenhum sinal do breaker é avaliado.
type dlPresoModel struct{ entrou chan struct{} }

func (m *dlPresoModel) Call(ctx context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	select {
	case m.entrou <- struct{}{}:
	default: // o sinal é só do PRIMEIRO turno; não bloqueia se ninguém estiver a ler
	}
	<-ctx.Done()
	return agentruntime.ModelResponse{}, ctx.Err()
}

// TestAOS252_DeadlineInterrompeORunPreso: com o tecto de wall-clock no mínimo, o varrimento
// periódico materializa `timed_out` E cancela o contexto do run — o run SAI. As duas metades
// são asseridas: sem o cancelamento o Wait nunca devolveria (falha por timeout do teste);
// sem a transição durável o estado lido de uma máquina fresca não seria `timed_out`.
func TestAOS252_DeadlineInterrompeORunPreso(t *testing.T) {
	// O tecto de wall-clock do run é o MESMO valor do sinal do breaker (um conceito
	// operador, dois pontos de enforcement) — no mínimo, para o deadline já ter expirado
	// quando o primeiro tick do varrimento chega.
	t.Setenv("AOS_BREAKER_MAX_WALL_CLOCK", "1ms")
	t.Setenv("AOS_BREAKER_MAX_STALE_ITERATIONS", "0")
	t.Setenv("AOS_BREAKER_MAX_COST_MICRO_USD_PER_SEC", "0")
	t.Setenv("AOS_BREAKER_MAX_TOKENS_PER_SEC", "0")

	cfg := tnBaseConfig()
	model := &dlPresoModel{entrou: make(chan struct{}, 1)}
	cfg.Model = model
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })

	// Período curto: o teste conduz o varrimento pelo caminho de PRODUÇÃO (o ticker do loop
	// de serviço), não por uma chamada directa — é a composição que F-A5 põe em causa.
	svc, err := NewNodeService(node, WithDeadlineSweepInterval(20*time.Millisecond))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	// Shutdown com DEADLINE, não com context.Background(): o drain gracioso espera pelo
	// WaitGroup, e num mundo em que a correcção de F-A5 regrida o run preso nunca sai —
	// a limpeza ficaria pendurada até o go test estoirar aos 10 minutos, escondendo a
	// falha real atrás de um timeout do binário. Com deadline, o shutdown cancela os em
	// curso ([NodeService.cancelInFlight]) e a regressão falha depressa e no sítio certo.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Shutdown(ctx)
	})

	const runID = "run-deadline-preso"
	if err := svc.Submit(context.Background(), agentruntime.Goal{
		RunID:      runID,
		Principal:  referencemonitor.Principal{NHIID: tnAgent},
		Credential: bktToken(t, node),
		Objective:  "run que fica preso a meio do turno",
		MaxTurns:   64,
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// O run entrou MESMO no turno (não estamos a medir uma submissão que falhou cedo).
	select {
	case <-model.entrou:
	case <-time.After(10 * time.Second):
		t.Fatal("o modelo nunca foi interrogado — o run nao chegou a entrar no turno")
	}

	// A METADE QUE FALTAVA: o run SAI. Sem o cancelamento no varrimento, este Wait esgota o
	// contexto e o teste falha aqui — que é exactamente o estado do código antes da correcção.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, ok, werr := svc.Wait(ctx, runID); werr != nil || !ok {
		t.Fatalf("o run preso NAO foi interrompido pelo deadline (F-A5: o estado seria marcado e o agente continuaria a agir): err=%v ok=%v", werr, ok)
	}

	// A metade que já existia: o estado terminal está no log durável, relido de uma máquina
	// fresca. `timed_out` e NÃO `failed` — o selo terminal de AOS-252 é no-op fora de
	// `running`, pelo que o desfecho do deadline não é reescrito por quem sai a seguir.
	assertMachineState(t, node.EventStore, runID, state.TimedOut)
}
