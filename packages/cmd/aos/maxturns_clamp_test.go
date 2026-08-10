package main

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// AOS-203 (achado F2 do desafio A5) — o TECTO DE TURNOS na fronteira de INGRESSO.
//
// O defeito: `max_turns` vinha cru do corpo de POST /runs para o Goal; o loop só aplicava o
// default quando o campo era <= 0, pelo que um valor grande passava tal e qual. Composto com o
// orçamento RPM POR-PROCESSO do keypool do gateway, um único `POST /runs {max_turns: 200}`
// esgotava-o e deixava o nó sem poder chamar o modelo até reiniciar. Estes testes provam que o
// clamp fecha o buraco: um `max_turns` gigante é ACEITE (201) mas corre no MÁXIMO o tecto, o
// default (sem `max_turns`) continua a ser [agentruntime.DefaultMaxTurns], e um valor legítimo
// ABAIXO do tecto passa INTACTO (é um clamp, não uma constante).

// loopingToolModel emite UMA tool call em CADA turno e NUNCA conclui (Final=false com ToolCalls
// não-vazio ⇒ o loop medeia a call, ela é NEGADA por default-deny, e o loop continua). Assim o
// run só pára ao esgotar MaxTurns — e o nº de invocações do modelo É o MaxTurns efectivo, que é
// exactamente o que o clamp de ingresso governa. Conta as invocações de forma atómica (o run
// corre numa goroutine do loop de serviço).
type loopingToolModel struct{ calls int64 }

func (m *loopingToolModel) Call(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	// Cada turno usa um ResourceValue distinto — o run "progride" do ponto de vista do detector
	// de repetição, mas o breaker está de qualquer modo DESLIGADO nestes testes (ver
	// newClampNode); a variação é só higiene, para o veredicto de paragem ser inequivocamente
	// MaxTurns e não "sem progresso".
	n := atomic.AddInt64(&m.calls, 1)
	return agentruntime.ModelResponse{
		Text: "", // sem Final: força a mediação da call e a continuação do loop.
		ToolCalls: []agentruntime.ToolInvocation{{
			ToolID:        "probe",
			Capability:    "cap:fs.read",
			ResourceType:  "fs",
			ResourceValue: "probe-" + strconvItoa(int(n)),
		}},
		Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

// strconvItoa evita importar strconv só para um int→string no modelo de teste.
func strconvItoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// newClampNode compõe um nó de referência com o modelo em loop e o DISJUNTOR DESLIGADO
// (AOS_BREAKER_MAX_* a 0 ⇒ breakerThresholdsFromEnv devolve nil ⇒ nenhum breaker composto).
// Sem isto, o disjuntor de "sem progresso"/wall-clock poderia parar o run ANTES de MaxTurns e
// mascarar o que o teste mede. Regista a tool `probe` para a mediação alcançar a cadeia e NEGAR
// por política (default-deny), em vez de tropeçar numa tool desconhecida.
func newClampNode(t *testing.T, model agentruntime.ModelClient) *Node {
	t.Helper()
	t.Setenv("AOS_BREAKER_MAX_STALE_ITERATIONS", "0")
	t.Setenv("AOS_BREAKER_MAX_WALL_CLOCK", "0")
	node, _ := newAPINode(t, model, false)
	if err := node.Runtime.Register("probe", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register(probe): %v", err)
	}
	return node
}

// runEffectiveTurns submete um run pelo handler (com o `max_turns` dado no corpo, ou nenhum se
// requestMaxTurns == nil), espera a conclusão e devolve o nº de turnos EFECTIVAMENTE executados
// (= invocações do modelo).
func runEffectiveTurns(t *testing.T, h http.Handler, model *loopingToolModel, svc *NodeService, runID string, requestMaxTurns *int) int64 {
	t.Helper()
	body := map[string]any{
		"run_id":        runID,
		"objective":     "prova do tecto de turnos",
		"principal_nhi": "nhi:" + runID,
	}
	if requestMaxTurns != nil {
		body["max_turns"] = *requestMaxTurns
	}
	rec := postJSON(h, "POST", "/runs", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, ok, err := svc.Wait(waitCtx, runID); err != nil || !ok {
		t.Fatalf("run %q devia ter concluido: ok=%v err=%v", runID, ok, err)
	}
	return atomic.LoadInt64(&model.calls)
}

// TestAPIMaxTurnsClampedToDefaultCeiling — sem opção de tecto, o default é
// [agentruntime.DefaultMaxTurns]. Um `max_turns` gigante é ACEITE mas corre no máximo o tecto;
// o default (sem `max_turns`) corre exactamente o tecto. É o critério de aceitação nº4 do ticket.
func TestAPIMaxTurnsClampedToDefaultCeiling(t *testing.T) {
	ceiling := int64(agentruntime.DefaultMaxTurns)

	// (a) max_turns gigante ⇒ CLAMPADO ao default (não corre 100000 turnos).
	t.Run("max_turns gigante e clampado", func(t *testing.T) {
		model := &loopingToolModel{}
		node := newClampNode(t, model)
		defer func() { _ = node.Close() }()
		svc, h := newAPI(t, node) // sem WithMaxTurnsCeiling ⇒ default
		huge := 100000
		got := runEffectiveTurns(t, h, model, svc, "run-clamp-huge", &huge)
		if got != ceiling {
			t.Fatalf("max_turns=100000 devia correr no maximo o tecto (%d turnos), correu %d", ceiling, got)
		}
	})

	// (b) sem max_turns ⇒ default aplicado no ingresso, corre exactamente o tecto.
	t.Run("sem max_turns fica no default", func(t *testing.T) {
		model := &loopingToolModel{}
		node := newClampNode(t, model)
		defer func() { _ = node.Close() }()
		svc, h := newAPI(t, node)
		got := runEffectiveTurns(t, h, model, svc, "run-clamp-default", nil)
		if got != ceiling {
			t.Fatalf("sem max_turns o run devia correr o default (%d turnos), correu %d", ceiling, got)
		}
	})
}

// TestAPIMaxTurnsCustomCeiling — com um tecto EXPLÍCITO (a via de AOS_MAX_TURNS/[WithMaxTurnsCeiling]):
// um `max_turns` acima é clampado ao tecto, e um valor legítimo ABAIXO passa INTACTO. A segunda
// asserção é o que distingue um CLAMP de uma constante: sem ela, "corre sempre o tecto" passaria
// com um bug que ignorasse o corpo por completo.
func TestAPIMaxTurnsCustomCeiling(t *testing.T) {
	const ceiling = 3

	t.Run("acima do tecto e clampado", func(t *testing.T) {
		model := &loopingToolModel{}
		node := newClampNode(t, model)
		defer func() { _ = node.Close() }()
		svc, h := newAPI(t, node, WithMaxTurnsCeiling(ceiling))
		huge := 100000
		got := runEffectiveTurns(t, h, model, svc, "run-clamp-custom-huge", &huge)
		if got != ceiling {
			t.Fatalf("com tecto %d, max_turns=100000 devia correr %d turnos, correu %d", ceiling, ceiling, got)
		}
	})

	t.Run("abaixo do tecto passa intacto", func(t *testing.T) {
		model := &loopingToolModel{}
		node := newClampNode(t, model)
		defer func() { _ = node.Close() }()
		svc, h := newAPI(t, node, WithMaxTurnsCeiling(ceiling))
		below := 2 // < ceiling
		got := runEffectiveTurns(t, h, model, svc, "run-clamp-custom-below", &below)
		if got != int64(below) {
			t.Fatalf("com tecto %d, um max_turns=%d legitimo devia passar INTACTO (%d turnos), correu %d", ceiling, below, below, got)
		}
	})
}

// TestWithMaxTurnsCeilingIgnoresNonPositive — o tecto NUNCA se desliga: [WithMaxTurnsCeiling]
// com n <= 0 mantém o default. Um clamp "desligável" reabriria o próprio buraco que fecha.
func TestWithMaxTurnsCeilingIgnoresNonPositive(t *testing.T) {
	for _, n := range []int{0, -1, -100} {
		cfg := apiConfig{maxTurnsCeiling: agentruntime.DefaultMaxTurns}
		WithMaxTurnsCeiling(n)(&cfg)
		if cfg.maxTurnsCeiling != agentruntime.DefaultMaxTurns {
			t.Fatalf("WithMaxTurnsCeiling(%d) devia ser IGNORADO (tecto fica %d), ficou %d", n, agentruntime.DefaultMaxTurns, cfg.maxTurnsCeiling)
		}
	}
}

// TestAPIMaxTurnsOptionFromEnv cobre a resolução de AOS_MAX_TURNS: vazio ⇒ sem opção (default no
// handler); positivo ⇒ [WithMaxTurnsCeiling]; inválido (<=0 / não-inteiro) ⇒ [ErrBadMaxTurns]
// (fail-closed de config, não degrada para o default em silêncio).
func TestAPIMaxTurnsOptionFromEnv(t *testing.T) {
	t.Run("vazio nao produz opcao", func(t *testing.T) {
		t.Setenv("AOS_MAX_TURNS", "")
		opt, err := apiMaxTurnsOptionFromEnv()
		if err != nil {
			t.Fatalf("vazio nao devia dar erro, veio %v", err)
		}
		if opt != nil {
			t.Fatalf("vazio nao devia produzir opcao (o default vive no handler), veio nao-nil")
		}
	})

	t.Run("positivo aplica o tecto", func(t *testing.T) {
		t.Setenv("AOS_MAX_TURNS", "42")
		opt, err := apiMaxTurnsOptionFromEnv()
		if err != nil || opt == nil {
			t.Fatalf("valor positivo devia produzir opcao sem erro, veio opt=%v err=%v", opt, err)
		}
		cfg := apiConfig{maxTurnsCeiling: agentruntime.DefaultMaxTurns}
		opt(&cfg)
		if cfg.maxTurnsCeiling != 42 {
			t.Fatalf("AOS_MAX_TURNS=42 devia pôr o tecto a 42, ficou %d", cfg.maxTurnsCeiling)
		}
	})

	for _, bad := range []string{"0", "-1", "abc", "1.5", "16 turnos"} {
		t.Run("invalido aborta: "+bad, func(t *testing.T) {
			t.Setenv("AOS_MAX_TURNS", bad)
			if _, err := apiMaxTurnsOptionFromEnv(); err == nil {
				t.Fatalf("AOS_MAX_TURNS=%q devia ABORTAR (ErrBadMaxTurns), nao deu erro", bad)
			}
		})
	}
}
