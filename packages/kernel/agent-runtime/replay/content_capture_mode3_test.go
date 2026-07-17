package replay

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// mode3Run é o material de uma trajectória original capturada em CONTENT-CAPTURE
// MODE 3 (AOS-079): os payloads completos vivem no PayloadStore externo e o Event
// Store só guarda referências.
type mode3Run struct {
	store        *eventstore.Store
	payloadStore *InMemoryPayloadStore
	goal         agentruntime.Goal
	spec         TrajectorySpec
	toolHits     *int
}

// runOriginalMode3 corre a MESMA trajectória de 3 turnos de runOriginal, mas com o
// capturer ligado ao PayloadStore externo (mode 3). O escritor do Event Store usa o
// escopo de ESCRITA; o replay usará o de LEITURA (separação de IAM).
func runOriginalMode3(t testing.TB, runID string, capOpts ...CapturerOption) mode3Run {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	payloadStore := NewInMemoryPayloadStore()

	hits := 0
	sink := referencemonitor.NewEventStoreSink(store)
	rm := referencemonitor.New(referencemonitor.WithEventSink(sink))
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		hits++
		return append([]byte("echoed:"), in...), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	recorder := agentruntime.NewTurnRecorder(store)
	opts := append([]CapturerOption{
		WithClock(fixedClock()),
		WithPayloadStore(payloadStore, writerAccessor),
	}, capOpts...)
	capturer, err := NewCapturer(store, opts...)
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}

	callN := 0
	model := agentruntime.ModelClientFunc(func(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		callN++
		switch callN {
		case 1:
			return agentruntime.ModelResponse{
				Text:         "primeiro: chamo echo",
				ToolCalls:    []agentruntime.ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("um")}},
				Usage:        agentruntime.Usage{InputTokens: 10, OutputTokens: 5},
				CostMicroUSD: 1200,
			}, nil
		case 2:
			return agentruntime.ModelResponse{
				Text:         "segundo: chamo echo outra vez",
				ToolCalls:    []agentruntime.ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("dois")}},
				Usage:        agentruntime.Usage{InputTokens: 8, OutputTokens: 4},
				CostMicroUSD: 1000,
			}, nil
		default:
			return agentruntime.ModelResponse{
				Text:         "concluído",
				Final:        true,
				Usage:        agentruntime.Usage{InputTokens: 6, OutputTokens: 2},
				CostMicroUSD: 800,
			}, nil
		}
	})

	rt := agentruntime.New(model, rm, recorder, agentruntime.WithCapturer(capturer))
	goal := sampleGoal(runID)
	res, err := rt.Run(context.Background(), goal)
	if err != nil {
		t.Fatalf("Run original mode 3: %v", err)
	}
	if !res.Terminated || res.Turns != 3 {
		t.Fatalf("original mode 3 inesperado: %+v", res)
	}
	return mode3Run{store: store, payloadStore: payloadStore, goal: goal, spec: specFromGoal(goal), toolHits: &hits}
}

// ---------------------------------------------------------------------------
// Teste — Fidelidade 100% (golden run) com payloads em STORAGE EXTERNO (mode 3).
// ---------------------------------------------------------------------------

func TestReplayMode3Fidelity100(t *testing.T) {
	or := runOriginalMode3(t, "run_mode3_100")
	e, err := NewEngine(or.store, WithPayloadResolver(or.payloadStore, readerAccessor))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	res, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if res.Divergence != nil {
		t.Fatalf("replay mode 3 fiel não devia divergir: %+v", res.Divergence)
	}
	if res.Fidelity != 1.0 {
		t.Fatalf("fidelidade = %v, esperava 1.0 (100%% dos passos reproduzíveis)", res.Fidelity)
	}
	if len(res.Steps) != 3 || !res.Terminated || res.FinalText != "concluído" {
		t.Fatalf("desfecho reconstruído errado: steps=%d term=%v text=%q", len(res.Steps), res.Terminated, res.FinalText)
	}
	// A resposta e as tool calls vieram do PAYLOAD EXTERNO, não do evento do ES.
	if res.Steps[0].Response.Text != "primeiro: chamo echo" {
		t.Fatalf("resposta do turno 1 não reconstruída do payload externo: %q", res.Steps[0].Response.Text)
	}
	if len(res.Steps[0].Response.ToolCalls) != 1 || res.Steps[0].Response.ToolCalls[0].ToolID != "echo" {
		t.Fatalf("tool calls do turno 1 não reconstruídas do payload externo: %+v", res.Steps[0].Response.ToolCalls)
	}
	for _, st := range res.Steps {
		if !st.Matched || st.PromptHash != st.RecordedPromptHash {
			t.Fatalf("turno %d não coincidiu em mode 3: %+v", st.Turn, st)
		}
	}
}

// ---------------------------------------------------------------------------
// Teste — o evento do Event Store é REFERÊNCIA-SÓ; os payloads vivem no store externo.
// ---------------------------------------------------------------------------

func TestReplayMode3EventIsReferenceOnly(t *testing.T) {
	or := runOriginalMode3(t, "run_mode3_refonly")
	events, err := or.store.Read(context.Background(), or.goal.RunID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	captured := 0
	for _, ev := range events {
		if ev.Type != EventTypeCaptured {
			continue
		}
		captured++
		var p capturePayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		// O evento do ES carrega SÓ a referência — o payload pesado não está inline.
		if p.PayloadRef == "" {
			t.Fatalf("evento mode 3 do turno %d devia carregar PayloadRef", p.Turn)
		}
		if p.Response.Text != "" || len(p.Response.ToolCalls) != 0 || len(p.ToolResults) != 0 {
			t.Fatalf("evento mode 3 do turno %d NÃO devia ter payload inline: %+v", p.Turn, p)
		}
		// O texto do modelo (conteúdo pesado) NÃO aparece nos bytes do evento do ES.
		if bytesContains(ev.Payload, "chamo echo") || bytesContains(ev.Payload, "concluído") {
			t.Fatalf("payload pesado vazou para o evento do ES: %s", ev.Payload)
		}
	}
	if captured != 3 {
		t.Fatalf("esperava 3 eventos de captura, obtive %d", captured)
	}
	// Os payloads completos residem no store externo (um por turno).
	if or.payloadStore.count() != 3 {
		t.Fatalf("esperava 3 payloads no store externo, obtive %d", or.payloadStore.count())
	}
}

// ---------------------------------------------------------------------------
// Teste — trace-diffing em mode 3: original vs reexecução com spec evoluído detecta
// a divergência (reusa detectDivergence sobre payloads externos).
// ---------------------------------------------------------------------------

func TestReplayMode3TraceDiffing(t *testing.T) {
	or := runOriginalMode3(t, "run_mode3_diff")
	e, err := NewEngine(or.store, WithPayloadResolver(or.payloadStore, readerAccessor))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	spec := or.spec
	spec.System = "SYSTEM PROMPT EVOLUÍDO" // simula evolução de código

	res, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: spec})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if res.Divergence == nil || res.Divergence.Reason != "prompt_hash" {
		t.Fatalf("esperava divergência de prompt_hash em mode 3, obtive %+v", res.Divergence)
	}
	if res.Divergence.Turn != 1 || res.Fidelity == 1.0 {
		t.Fatalf("divergência mal localizada em mode 3: turno=%d fid=%v", res.Divergence.Turn, res.Fidelity)
	}
}

// ---------------------------------------------------------------------------
// Teste IAM — um accessor NÃO autorizado é NEGADO pelo PayloadStore no replay.
// ---------------------------------------------------------------------------

func TestReplayMode3IAMDeny(t *testing.T) {
	or := runOriginalMode3(t, "run_mode3_iam")

	// (a) O motor com o accessor do ESCRITOR (só escopo de escrita) é negado — prova
	// de que o payload está atrás do seu IAM próprio, separado do escritor do ES.
	eDenied, err := NewEngine(or.store, WithPayloadResolver(or.payloadStore, writerAccessor))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := eDenied.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec}); !errors.Is(err, ErrPayloadAccessDenied) {
		t.Fatalf("replay com accessor não autorizado devia ser NEGADO, obtive %v", err)
	}

	// (b) Um accessor sem escopos nenhuns também é negado.
	eAnon, _ := NewEngine(or.store, WithPayloadResolver(or.payloadStore, Accessor{Principal: "anon"}))
	if _, err := eAnon.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec}); !errors.Is(err, ErrPayloadAccessDenied) {
		t.Fatalf("replay com accessor anónimo devia ser NEGADO, obtive %v", err)
	}

	// (c) O accessor AUTORIZADO (escopo de leitura) resolve e reproduz 100%.
	eOK, _ := NewEngine(or.store, WithPayloadResolver(or.payloadStore, readerAccessor))
	res, err := eOK.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec})
	if err != nil {
		t.Fatalf("replay autorizado: %v", err)
	}
	if res.Fidelity != 1.0 {
		t.Fatalf("replay autorizado devia reproduzir 100%%, fid=%v", res.Fidelity)
	}
}

// ---------------------------------------------------------------------------
// Teste — mode 3 sem resolver ligado ⇒ fail-closed (ErrPayloadStoreRequired).
// ---------------------------------------------------------------------------

func TestReplayMode3WithoutResolverFailsClosed(t *testing.T) {
	or := runOriginalMode3(t, "run_mode3_noresolver")
	e, err := NewEngine(or.store) // sem WithPayloadResolver
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec}); !errors.Is(err, ErrPayloadStoreRequired) {
		t.Fatalf("mode 3 sem resolver devia falhar com ErrPayloadStoreRequired, obtive %v", err)
	}
}

// ---------------------------------------------------------------------------
// Teste NEGATIVO de admissão — payload perdido no store (TTL/crypto-shredding) ⇒
// captura incompleta ⇒ replay INADMISSÍVEL.
// ---------------------------------------------------------------------------

func TestReplayMode3EvictedPayloadIsInadmissible(t *testing.T) {
	or := runOriginalMode3(t, "run_mode3_evicted")
	// Descobre a ref do turno 2 e remove-a do store (simula retenção expirada).
	events, _ := or.store.Read(context.Background(), or.goal.RunID, 1)
	var evictedTurn2 bool
	for _, ev := range events {
		if ev.Type != EventTypeCaptured {
			continue
		}
		var p capturePayload
		_ = json.Unmarshal(ev.Payload, &p)
		if p.Turn == 2 && p.PayloadRef != "" {
			or.payloadStore.evict(PayloadRef{Digest: p.PayloadRef})
			evictedTurn2 = true
		}
	}
	if !evictedTurn2 {
		t.Fatalf("pré-condição falhou: não evicção da ref do turno 2")
	}

	e, _ := NewEngine(or.store, WithPayloadResolver(or.payloadStore, readerAccessor))
	if _, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec}); !errors.Is(err, ErrIncompleteCapture) {
		t.Fatalf("payload perdido devia tornar o replay INADMISSÍVEL (ErrIncompleteCapture), obtive %v", err)
	}
}

// ---------------------------------------------------------------------------
// Teste — mode 3 + modo sensível: a PII NÃO aparece em claro NEM no ES NEM no store
// externo (redação na fronteira, ADR-011).
// ---------------------------------------------------------------------------

func TestReplayMode3SensitiveNoPIIAnywhere(t *testing.T) {
	// A resposta do modelo do harness ecoa "chamo echo"; para exercitar a redação,
	// injectamos um turno sensível directamente no capturer.
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ps := NewInMemoryPayloadStore()
	c, err := NewCapturer(store,
		WithClock(fixedClock()),
		WithSensitiveResults(),
		WithPayloadStore(ps, writerAccessor),
	)
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	const pii = "numero-cartao-4111-1111"
	tc := agentruntime.TurnCapture{
		RunID:  "run_mode3_sens",
		StepID: "step-000001",
		Turn:   1,
		Response: agentruntime.ModelResponse{
			Text:      pii,
			ToolCalls: []agentruntime.ToolInvocation{{ToolID: "echo", Input: []byte(pii)}},
		},
		ToolResults: []agentruntime.CapturedToolResult{
			{Invocation: agentruntime.ToolInvocation{ToolID: "echo"}, Result: agentruntime.Untrusted([]byte(pii))},
		},
	}
	if err := c.Capture(context.Background(), tc); err != nil {
		t.Fatalf("Capture: %v", err)
	}

	// (1) O evento do ES é referência-só e NÃO contém a PII.
	events, _ := store.Read(context.Background(), tc.RunID, 1)
	var ref string
	for _, ev := range events {
		if ev.Type != EventTypeCaptured {
			continue
		}
		if bytesContains(ev.Payload, pii) {
			t.Fatalf("PII em claro no evento do ES: %s", ev.Payload)
		}
		var p capturePayload
		_ = json.Unmarshal(ev.Payload, &p)
		ref = p.PayloadRef
	}
	if ref == "" {
		t.Fatalf("evento mode 3 sem PayloadRef")
	}
	// (2) O payload EXTERNO também NÃO contém a PII em claro (redigida na fronteira).
	blob, err := ps.Get(context.Background(), PayloadRef{Digest: ref}, readerAccessor)
	if err != nil {
		t.Fatalf("Get payload externo: %v", err)
	}
	if strings.Contains(string(blob), pii) {
		t.Fatalf("PII em claro no payload externo (mode 3 + sensível): %s", blob)
	}
	if !strings.Contains(string(blob), "sha256:") {
		t.Fatalf("payload externo sensível devia conter referências sha256: %s", blob)
	}
}

// payloadRefRewritingReader reescreve a PayloadRef do evento de captura de um turno
// para apontar a outro digest — usa-se para simular um payload externo corrompido.
type payloadRefRewritingReader struct {
	inner  EventReader
	turn   int
	newRef string
}

func (r *payloadRefRewritingReader) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	events, err := r.inner.Read(ctx, streamID, fromSeq)
	if err != nil {
		return nil, err
	}
	out := make([]eventstore.Event, len(events))
	copy(out, events)
	for i := range out {
		if out[i].Type != EventTypeCaptured {
			continue
		}
		var p capturePayload
		if err := json.Unmarshal(out[i].Payload, &p); err != nil {
			return nil, err
		}
		if p.Turn == r.turn && p.PayloadRef != "" {
			p.PayloadRef = r.newRef
			raw, err := json.Marshal(p)
			if err != nil {
				return nil, err
			}
			out[i].Payload = raw
		}
	}
	return out, nil
}

// Um payload externo que não descodifica (conteúdo íntegro face à ref, mas não é um
// capturePayload válido) ⇒ ErrCorruptCapture — fail-closed.
func TestReplayMode3CorruptExternalPayload(t *testing.T) {
	or := runOriginalMode3(t, "run_mode3_corrupt")
	// Grava um blob NÃO-JSON no store (content-addressable: a ref bate com o garbage).
	ref, err := or.payloadStore.Put(context.Background(), PayloadPutRequest{Payload: []byte("{{nao-json"), Writer: writerAccessor})
	if err != nil {
		t.Fatalf("Put garbage: %v", err)
	}
	reader := &payloadRefRewritingReader{inner: or.store, turn: 1, newRef: ref.Digest}
	e, err := NewEngine(reader, WithPayloadResolver(or.payloadStore, readerAccessor))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec}); !errors.Is(err, ErrCorruptCapture) {
		t.Fatalf("payload externo corrompido devia dar ErrCorruptCapture, obtive %v", err)
	}
}

// Um payload externo BEM-FORMADO mas com SchemaVersion não suportada ⇒
// ErrCorruptCapture (defesa-em-profundidade: o schema drift falha fail-closed, não
// é silenciosamente mal-parseado).
func TestReplayMode3UnsupportedSchemaFailsClosed(t *testing.T) {
	or := runOriginalMode3(t, "run_mode3_badschema")
	blob, err := json.Marshal(capturePayload{SchemaVersion: "9.9", Turn: 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ref, err := or.payloadStore.Put(context.Background(), PayloadPutRequest{Payload: blob, Writer: writerAccessor})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	reader := &payloadRefRewritingReader{inner: or.store, turn: 1, newRef: ref.Digest}
	e, err := NewEngine(reader, WithPayloadResolver(or.payloadStore, readerAccessor))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec}); !errors.Is(err, ErrCorruptCapture) {
		t.Fatalf("schema não suportado devia dar ErrCorruptCapture, obtive %v", err)
	}
}

// Um payload externo cujo Turn INTERNO diverge do turno do envelope do ES ⇒
// ErrCorruptCapture — não confiamos no conteúdo do store para indexar o turno.
func TestReplayMode3TurnMismatchFailsClosed(t *testing.T) {
	or := runOriginalMode3(t, "run_mode3_turnmismatch")
	blob, err := json.Marshal(capturePayload{SchemaVersion: captureSchemaVersion, Turn: 99})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ref, err := or.payloadStore.Put(context.Background(), PayloadPutRequest{Payload: blob, Writer: writerAccessor})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	reader := &payloadRefRewritingReader{inner: or.store, turn: 1, newRef: ref.Digest}
	e, err := NewEngine(reader, WithPayloadResolver(or.payloadStore, readerAccessor))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec}); !errors.Is(err, ErrCorruptCapture) {
		t.Fatalf("Turn divergente devia dar ErrCorruptCapture, obtive %v", err)
	}
}

// failingPayloadStore falha o Put — prova que o capturer mode 3 é fail-closed (não
// grava um evento com referência pendurada se o payload não chegou ao store).
type failingPayloadStore struct{ err error }

func (f failingPayloadStore) Put(context.Context, PayloadPutRequest) (PayloadRef, error) {
	return PayloadRef{}, f.err
}
func (f failingPayloadStore) Get(context.Context, PayloadRef, Accessor) ([]byte, error) {
	return nil, f.err
}

func TestCaptureMode3PutErrorIsFailClosed(t *testing.T) {
	sentinel := errors.New("store externo indisponível")
	fa := &fakeAppender{}
	c, err := NewCapturer(fa, WithPayloadStore(failingPayloadStore{err: sentinel}, writerAccessor))
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	if err := c.Capture(context.Background(), sampleTurnCapture()); !errors.Is(err, sentinel) {
		t.Fatalf("Put falhado devia propagar o erro, obtive %v", err)
	}
	// Fail-closed: NENHUM evento foi escrito no Event Store (sem ref pendurada).
	if len(fa.got) != 0 {
		t.Fatalf("nenhum evento devia ser escrito quando o Put falha, obtive %d", len(fa.got))
	}
}
