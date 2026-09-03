package replay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// onceCorrection é um [agentruntime.SteerSource] de teste: NUNCA pausa e entrega uma
// correcção TRUSTED UMA vez (na 1ª fronteira de fim-de-turno). Modela um steer submetido
// durante o turno 1 — a correcção entra no prompt do turno 2.
type onceCorrection struct {
	corr  []byte
	given bool
}

func (s *onceCorrection) GracefulPause(context.Context, string) (bool, error) { return false, nil }

func (s *onceCorrection) PendingCorrection(context.Context, string) ([]byte, bool) {
	if !s.given && s.corr != nil {
		s.given = true
		return s.corr, true
	}
	return nil, false
}

// steeredRun corre uma trajectória de 3 turnos (2 com tool call, 1 final) com o
// EventStoreCapturer ligado. Se correction != nil, injecta-a como steer TRUSTED no fim do
// turno 1 (entra no prompt do turno 2). Devolve o [originalRun] (store + goal + spec) para
// o replay reconstruir. É o gémeo de runOriginal deste ficheiro, com o eixo do steer.
func steeredRun(t *testing.T, runID string, correction []byte, capOpts ...CapturerOption) originalRun {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	hits := 0
	rm := referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		hits++
		return append([]byte("echoed:"), in...), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	recorder := agentruntime.NewTurnRecorder(store)
	// O relógio fixo é o default; capOpts liga o eixo de confidencialidade (sealer/sensitive)
	// quando o teste o exige — a mesma trajectória, com o conteúdo cifrado por-titular.
	capturer, err := NewCapturer(store, append([]CapturerOption{WithClock(fixedClock())}, capOpts...)...)
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}

	callN := 0
	model := agentruntime.ModelClientFunc(func(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		callN++
		switch callN {
		case 1:
			return agentruntime.ModelResponse{
				Text:      "turno 1: chamo echo",
				ToolCalls: []agentruntime.ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("um")}},
				Usage:     agentruntime.Usage{InputTokens: 10, OutputTokens: 5},
			}, nil
		case 2:
			return agentruntime.ModelResponse{
				Text:      "turno 2: chamo echo outra vez",
				ToolCalls: []agentruntime.ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("dois")}},
				Usage:     agentruntime.Usage{InputTokens: 8, OutputTokens: 4},
			}, nil
		default:
			return agentruntime.ModelResponse{Text: "concluído", Final: true, Usage: agentruntime.Usage{InputTokens: 6, OutputTokens: 2}}, nil
		}
	})

	opts := []agentruntime.Option{agentruntime.WithCapturer(capturer)}
	if correction != nil {
		opts = append(opts, agentruntime.WithSteerSource(&onceCorrection{corr: correction}))
	}
	rt := agentruntime.New(model, rm, recorder, opts...)
	goal := sampleGoal(runID)
	res, err := rt.Run(context.Background(), goal)
	if err != nil {
		t.Fatalf("Run steerado: %v", err)
	}
	if !res.Terminated || res.Turns != 3 {
		t.Fatalf("trajectória steerada inesperada: %+v", res)
	}
	return originalRun{store: store, goal: goal, spec: specFromGoal(goal), toolHits: &hits}
}

// recordedPromptHashTurn2 lê do log o prompt_hash GRAVADO do turno 2 (o turno cujo prompt
// carrega a correcção quando há steer). Serve para provar que a correcção é LOAD-BEARING:
// o hash do turno 2 muda entre o run steerado e o não-steerado.
func recordedPromptHashTurn2(t *testing.T, or originalRun, opts ...EngineOption) string {
	t.Helper()
	e, err := NewEngine(or.store, opts...)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	tr, err := e.load(context.Background(), or.goal.RunID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return tr.manifest[2].PromptHash
}

// TestReplaySteeredRunIsFaithful (AOS-218, ACHADO-1) — FALSIFICÁVEL, não-vacuo:
//
//  1. NÃO-VÁCUO: a correcção é LOAD-BEARING — o prompt_hash gravado do turno 2 DIFERE
//     entre o run steerado e o não-steerado. Se assim não fosse, o teste não provaria nada.
//  2. FALHA-ANTES / PASSA-DEPOIS: o replay do run STEERADO reproduz SEM divergência
//     (fidelidade 1.0). Antes da captura/reconstrução da correcção (AOS-218), o tail
//     reconstruído do turno 2 OMITIRIA o segmento taint=trusted correction=… e o
//     prompt_hash re-materializado divergiria do gravado — divergência ESPÚRIA no turno 2.
func TestReplaySteeredRunIsFaithful(t *testing.T) {
	correction := []byte("prioriza a superficie desktop")

	steered := steeredRun(t, "run_steer_faithful", correction)
	baseline := steeredRun(t, "run_no_steer_faithful", nil)

	// (1) NÃO-VÁCUO: a correcção muda de facto o prompt do turno 2.
	if hs, hb := recordedPromptHashTurn2(t, steered), recordedPromptHashTurn2(t, baseline); hs == hb {
		t.Fatalf("correcção não afectou o prompt do turno 2 (steerado=%q == baseline=%q) — teste vácuo", hs, hb)
	}

	// (2) FALHA-ANTES/PASSA-DEPOIS: o run steerado replaya sem divergência.
	e, err := NewEngine(steered.store)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	res, err := e.Replay(context.Background(), steered.goal.RunID, Options{Spec: steered.spec})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if res.Divergence != nil {
		t.Fatalf("run steerado divergiu no replay (%+v) — a correcção não foi reconstruída no tail", res.Divergence)
	}
	if res.Fidelity != 1.0 {
		t.Fatalf("fidelidade = %v, esperava 1.0 (replay fiel do run steerado)", res.Fidelity)
	}
	if len(res.Steps) != 3 {
		t.Fatalf("esperava 3 turnos replayados, obtive %d", len(res.Steps))
	}
}

// TestReplaySteeredResumeEqualsFullReplay (AOS-218, CA4) — RESUME-FROM-STEP com a
// correcção PRÉ-SEGMENTO: o estado reconstruído dobra a correcção do turno 2 (pré-segmento)
// tal como o replay completo, pelo que o FinalStateHash do resume a partir do turno 3
// IGUALA o do replay completo. Fecha a janela de retoma silenciosamente-errada (um resume
// que omitisse a correcção pré-segmento produziria um estado divergente sem o assinalar).
func TestReplaySteeredResumeEqualsFullReplay(t *testing.T) {
	or := steeredRun(t, "run_steer_resume", []byte("aperta o âmbito ao ticket"))
	e, err := NewEngine(or.store)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	full, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec})
	if err != nil {
		t.Fatalf("Replay completo: %v", err)
	}
	if full.Divergence != nil {
		t.Fatalf("replay completo divergiu: %+v", full.Divergence)
	}

	// step_id do turno 3 (o segmento de retoma). Deriva-se do log.
	tr, err := e.load(context.Background(), or.goal.RunID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	fromStep := tr.stepByTurn[3]
	if fromStep == "" {
		t.Fatal("sem step_id para o turno 3")
	}

	resume, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec, FromStepID: fromStep})
	if err != nil {
		t.Fatalf("Replay resume: %v", err)
	}
	if resume.FinalStateHash != full.FinalStateHash {
		t.Fatalf("FinalStateHash resume=%q != completo=%q — a correcção pré-segmento não foi dobrada no estado do resume",
			resume.FinalStateHash, full.FinalStateHash)
	}
	if resume.FinalStateHash == "" {
		t.Fatal("FinalStateHash vazio")
	}
}

// TestReplayNoSteerByteIdenticalCapture (AOS-218, retro-compat) — um run SEM steer produz
// eventos de captura SEM o campo leading_correction (omitempty): o payload AOS-016 fica
// byte-idêntico. Prova que o campo novo é estritamente aditivo.
func TestReplayNoSteerByteIdenticalCapture(t *testing.T) {
	or := steeredRun(t, "run_no_steer_bytes", nil)
	events, err := or.store.Read(context.Background(), or.goal.RunID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	seen := 0
	for _, ev := range events {
		if ev.Type != EventTypeCaptured {
			continue
		}
		seen++
		if bts := string(ev.Payload); strings.Contains(bts, "leading_correction") {
			t.Fatalf("captura de run sem steer não devia conter leading_correction: %s", bts)
		}
	}
	if seen == 0 {
		t.Fatal("esperava eventos de captura")
	}
}

// assertNoClearInCaptures relê os eventos de captura de um run e falha se needle aparecer em
// claro em algum deles (usado para provar que a correcção de steer NÃO toca o WAL em claro sob
// cifra por-titular).
func assertNoClearInCaptures(t *testing.T, or originalRun, needle string) {
	t.Helper()
	events, err := or.store.Read(context.Background(), or.goal.RunID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	seen := 0
	for _, ev := range events {
		if ev.Type != EventTypeCaptured {
			continue
		}
		seen++
		if strings.Contains(string(ev.Payload), needle) {
			t.Fatalf("captura selada continha %q em CLARO — confidencialidade violada: %s", needle, ev.Payload)
		}
	}
	if seen == 0 {
		t.Fatal("esperava eventos de captura")
	}
}

// TestReplaySteeredSealedRunIsFaithful (AOS-218, achado MÉDIO — caminho de PRODUÇÃO) — o análogo
// SELADO de TestReplaySteeredRunIsFaithful. O caminho de replay de produção de um run steerado é
// SEMPRE selado (bootstrap compõe o capturer com WithContentSealer, AOS-093): a LeadingCorrection
// migra para dentro do envelope cifrado por-titular e é re-hidratada por resolveSealed no read-path.
// Prova, sob cifra e com -race:
//
//  1. CONFIDENCIALIDADE: a correcção NÃO aparece em claro em nenhum evento de captura (vai selada);
//  2. NÃO-VÁCUO: o prompt_hash GRAVADO (turn.recorded, não selado) do turno 2 difere do baseline —
//     a correcção é load-bearing mesmo sob cifra;
//  3. FIDELIDADE: com o opener por-titular AUTORIZADO, o run steerado selado replaya SEM divergência
//     (só possível se a correcção selada foi decifrada e re-dobrada no tail);
//  4. FAIL-CLOSED PÓS-SHRED: destruída a KEK do titular, o replay falha na decifração (nunca cai
//     para claro), MESMO com o opener autorizado — o crypto-shredding aguenta o replay steerado.
func TestReplaySteeredSealedRunIsFaithful(t *testing.T) {
	cipher := newFakeSubjectCipher()
	correction := []byte("prioriza a superficie desktop")

	// A mesma trajectória de steeredRun, agora com o capturer SELADO (WithContentSealer). O
	// titular vem de goal.Principal.NHIID (sampleGoal → "nhi:agent-1"); é sob essa KEK que o
	// conteúdo — incluindo a correcção — é cifrado.
	const subject = "nhi:agent-1"
	steered := steeredRun(t, "run_steer_sealed_faithful", correction, WithContentSealer(cipher))
	baseline := steeredRun(t, "run_no_steer_sealed", nil, WithContentSealer(cipher))

	// (1) CONFIDENCIALIDADE: a correcção nunca toca o WAL em claro.
	assertNoClearInCaptures(t, steered, string(correction))

	// (2) NÃO-VÁCUO sob cifra: o prompt_hash gravado do turno 2 difere (a correcção muda o prompt).
	// O manifesto vem do turn.recorded (não selado); o opener é preciso porque load() resolve o
	// evento de captura selado do mesmo run antes de devolver a trajectória.
	auth := WithContentOpener(cipher, authorizedAccessor())
	if hs, hb := recordedPromptHashTurn2(t, steered, auth), recordedPromptHashTurn2(t, baseline, auth); hs == hb {
		t.Fatalf("correcção não afectou o prompt do turno 2 sob cifra (steerado=%q == baseline=%q) — teste vácuo", hs, hb)
	}

	// (3) FIDELIDADE: com o opener autorizado, o run steerado SELADO replaya sem divergência.
	e, err := NewEngine(steered.store, WithContentOpener(cipher, authorizedAccessor()))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	res, err := e.Replay(context.Background(), steered.goal.RunID, Options{Spec: steered.spec})
	if err != nil {
		t.Fatalf("Replay selado: %v", err)
	}
	if res.Divergence != nil {
		t.Fatalf("run steerado SELADO divergiu no replay (%+v) — a correcção selada não foi decifrada/reconstruída", res.Divergence)
	}
	if res.Fidelity != 1.0 {
		t.Fatalf("fidelidade = %v, esperava 1.0 (replay fiel do run steerado selado)", res.Fidelity)
	}
	if len(res.Steps) != 3 {
		t.Fatalf("esperava 3 turnos replayados, obtive %d", len(res.Steps))
	}

	// (4) FAIL-CLOSED PÓS-SHRED: destruir a KEK do titular ⇒ o replay steerado falha na decifração.
	cipher.shred(subject)
	e2, err := NewEngine(steered.store, WithContentOpener(cipher, authorizedAccessor()))
	if err != nil {
		t.Fatalf("NewEngine (pós-shred): %v", err)
	}
	if _, err := e2.Replay(context.Background(), steered.goal.RunID, Options{Spec: steered.spec}); !errors.Is(err, errFakeDecrypt) {
		t.Fatalf("depois do shred o replay steerado devia falhar na decifração (fail-closed), deu: %v", err)
	}
}

// TestCapturerSensitiveRedactsLeadingCorrection (AOS-218, achado de confidencialidade) — no modo
// referência-só ([WithSensitiveResults], SEM sealer), cujo desenho é manter TODO o texto-livre fora
// do WAL em claro, a LeadingCorrection (texto-livre de um operador, pode carregar segredos) é
// redigida por referência sha256 irreversível, tal como o texto do modelo e os argumentos de tool.
// Dois sentidos: sem o modo sensível a correcção é inline em claro (retro-compat AOS-016).
func TestCapturerSensitiveRedactsLeadingCorrection(t *testing.T) {
	const secret = "SEGREDO-STEER-OPERADOR-42"
	tc := sampleTurnCapture()
	tc.LeadingCorrection = []byte(secret)
	// LeadingCorrection é um campo []byte ⇒ serializa em base64 no JSON. A guarda de segredos
	// só é real se o PLAINTEXT (mesmo na sua forma base64, trivialmente reversível) não estiver
	// presente — por isso a fuga é aferida contra o base64 do segredo, não contra o ASCII cru.
	secretB64 := base64.StdEncoding.EncodeToString([]byte(secret))

	// Modo sensível SEM sealer: a correcção migra para uma referência irreversível.
	fa := &fakeAppender{}
	c, err := NewCapturer(fa, WithSensitiveResults())
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	if err := c.Capture(context.Background(), tc); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	raw := fa.got[0].Payload
	if strings.Contains(string(raw), secretB64) {
		t.Fatalf("correcção de steer em CLARO (base64) no WAL sob modo sensível — fuga: %s", raw)
	}
	var p capturePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if want := redactRef([]byte(secret)); string(p.LeadingCorrection) != want {
		t.Fatalf("LeadingCorrection = %q, esperava a referência %q", p.LeadingCorrection, want)
	}

	// Retro-compat: sem modo sensível, a correcção é inline em claro (comportamento AOS-016/218) —
	// os bytes originais estão presentes (na forma base64) e descodificam de volta ao segredo.
	fa2 := &fakeAppender{}
	c2, err := NewCapturer(fa2)
	if err != nil {
		t.Fatalf("NewCapturer (2): %v", err)
	}
	if err := c2.Capture(context.Background(), tc); err != nil {
		t.Fatalf("Capture (2): %v", err)
	}
	if !strings.Contains(string(fa2.got[0].Payload), secretB64) {
		t.Fatal("sem modo sensível a correcção devia ser inline em claro (retro-compat)")
	}
	var p2 capturePayload
	if err := json.Unmarshal(fa2.got[0].Payload, &p2); err != nil {
		t.Fatalf("unmarshal (2): %v", err)
	}
	if string(p2.LeadingCorrection) != secret {
		t.Fatalf("sem modo sensível LeadingCorrection = %q, esperava o segredo inline %q", p2.LeadingCorrection, secret)
	}
}
