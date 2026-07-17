package replay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// EventTypeCaptured é o tipo canónico do evento que persiste os inputs
// não-determinísticos de um turno para o replay (AOS-016).
const EventTypeCaptured = "replay.captured"

// captureSchemaVersion versiona o payload de captura (evolução independente do
// envelope do Event Store).
const captureSchemaVersion = "1.0"

// captureStepPrefix namespaceia o step_id do evento de captura no envelope do
// Event Store, para que a sua idempotency_key (run_id + ":cap-" + step_id) seja
// DISTINTA da do turno (run_id:step_id, AOS-013), da do ledger
// (run_id:ledger-step_id, AOS-014) e da do checkpoint (run_id:ckpt-…, AOS-015).
// Sem este namespace o ES deduplicaria a captura contra o evento de turno
// homónimo (a dedup do ES é global por idempotency_key). É o QUARTO domínio de
// dedup por passo.
const captureStepPrefix = "cap-"

// EventStore é o subconjunto do Event Store de que o capturer depende: apenas
// Append. *eventstore.Store satisfaz-o.
type EventStore interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
}

// toolCallCapture serializa a tool call PRETENDIDA pelo modelo. Preserva todos os
// campos que o replay precisa para reconstruir a intenção sem re-chamar o modelo.
type toolCallCapture struct {
	ToolID         string `json:"tool_id"`
	Capability     string `json:"capability,omitempty"`
	ResourceType   string `json:"resource_type,omitempty"`
	ResourceValue  string `json:"resource_value,omitempty"`
	ResourceRegion string `json:"resource_region,omitempty"`
	Input          []byte `json:"input,omitempty"`
}

// responseCapture serializa a resposta do modelo COMPLETA de um turno.
type responseCapture struct {
	Text         string            `json:"text,omitempty"`
	ToolCalls    []toolCallCapture `json:"tool_calls,omitempty"`
	Final        bool              `json:"final"`
	InputTokens  int64             `json:"input_tokens"`
	OutputTokens int64             `json:"output_tokens"`
	CostMicroUSD int64             `json:"cost_micro_usd"`
}

// toolResultCapture serializa o resultado observado de UMA tool call.
//
// # Segredos (ver [WithSensitiveResults])
//
// Output é persistido EM CLARO no evento durável (o cifrado por-titular do ES é
// dívida de EPIC-13). Em modo sensível o capturer substitui Output por uma
// REFERÊNCIA não reversível (Reference=true, PayloadRef=sha256(output)) — o
// replay reconstrói então um marcador de referência, NUNCA a PII em claro.
type toolResultCapture struct {
	Taint      string `json:"taint"`
	Output     []byte `json:"output,omitempty"`
	ToolError  string `json:"tool_error,omitempty"`
	Reference  bool   `json:"reference,omitempty"`
	PayloadRef string `json:"payload_ref,omitempty"`
}

// capturePayload é o corpo JSON do evento "replay.captured". A serialização é
// canónica e estável: structs (ordem de campos fixa), sem mapas — os mesmos
// inputs produzem sempre os mesmos bytes.
//
// # Content-capture mode 3 (AOS-079)
//
// Em mode 3 ([WithPayloadStore]) o payload COMPLETO (Response + ToolResults) migra
// para o [PayloadStore] externo e o evento do Event Store carrega APENAS a
// [PayloadRef] (PayloadRef != "", Response/ToolResults zero) — fica pequeno e fora
// do caminho quente. O replay resolve a referência via o PayloadStore (com o
// accessor autorizado) para reconstruir o payload. Sem a opção, o comportamento de
// AOS-016 (inline: PayloadRef == "", omitido) é byte-idêntico.
type capturePayload struct {
	SchemaVersion      string              `json:"schema_version"`
	Turn               int                 `json:"turn"`
	Response           responseCapture     `json:"response"`
	ToolResults        []toolResultCapture `json:"tool_results,omitempty"`
	ObservedAtUnixNano int64               `json:"observed_at_unix_nano"`
	// PayloadRef, se != "", indica MODE 3: o payload completo reside no PayloadStore
	// externo sob o seu próprio IAM; este evento é referência-só. Omitido (omitempty)
	// em mode inline ⇒ os bytes do evento AOS-016 mantêm-se inalterados.
	PayloadRef string `json:"payload_ref,omitempty"`
}

// EventStoreCapturer implementa [agentruntime.Capturer]: persiste os inputs
// não-determinísticos de cada turno como um evento append-only "replay.captured"
// no Event Store. Liga-se ao loop de AOS-013 via [agentruntime.WithCapturer], de
// forma ADITIVA (sem WithCapturer, o loop é byte-idêntico).
//
// # Relógio de captura
//
// O carimbo observed_at é do PRÓPRIO capturer (clock injectável, default
// time.Now) — é um concern de captura, não do loop. O replay LÊ este carimbo do
// log e EXPÕE-O para observabilidade (ReplayedTurn.ObservedAtUnixNano); NÃO o
// re-injecta na re-materialização do prompt nem em qualquer consumidor. Não há
// relógio ao vivo no caminho de replay porque não há consumidor de relógio — o
// tempo é apenas reportado, nunca usado para reconstruir estado.
//
// Sem estado mutável ⇒ seguro para uso concorrente e -race limpo.
type EventStoreCapturer struct {
	store     EventStore
	now       func() time.Time
	sensitive bool
	// payloadStore/writer activam o content-capture mode 3 (AOS-079): quando != nil,
	// o payload completo do turno é escrito no store externo e o evento do ES carrega
	// só a PayloadRef. writer é o principal de ESCRITA (escopo separado do de leitura
	// exigido no replay). nil ⇒ inline (AOS-016), byte-idêntico.
	payloadStore PayloadStore
	writer       Accessor
}

// CapturerOption configura o [EventStoreCapturer].
type CapturerOption func(*EventStoreCapturer)

// WithClock injecta o relógio de captura (default time.Now). Determinístico em
// teste; carimba observed_at, que o replay LÊ e expõe para observabilidade (nunca
// um relógio ao vivo — o carimbo é reportado, não re-injectado).
func WithClock(now func() time.Time) CapturerOption {
	return func(c *EventStoreCapturer) { c.now = now }
}

// WithSensitiveResults activa a guarda de segredos: os campos PII-portadores são
// persistidos apenas como REFERÊNCIA (sha256), nunca em claro. Cobre TODO o
// não-determinismo em que PII/segredos vivem — não só os outputs das tools:
//
//   - o OUTPUT de cada tool call (toolResultCapture.Output → PayloadRef);
//   - o TEXTO da resposta do modelo (que ecoa frequentemente dados);
//   - o INPUT e o ResourceValue de cada tool call (ex.: corpo/destinatário de um
//     send_email).
//
// É o análogo de [durable.WithSensitiveResults] (AOS-014) para a captura: evita
// gravar PII em claro no Event Store (o cifrado por-titular é dívida de EPIC-13). O
// replay de um turno sensível reconstrói marcadores de referência, NÃO a PII — o
// modo sensível troca fidelidade byte-a-byte por confidencialidade, por desenho.
func WithSensitiveResults() CapturerOption {
	return func(c *EventStoreCapturer) { c.sensitive = true }
}

// WithPayloadStore activa o content-capture MODE 3 (AOS-079): o payload completo do
// turno (resposta do modelo + resultados de tools, já minimizados/redigidos pela
// guarda de segredos quando activa) é escrito no [PayloadStore] EXTERNO sob o seu
// IAM próprio, e o evento "replay.captured" do Event Store passa a carregar APENAS a
// [PayloadRef] — pequeno e fora do caminho quente. writer é o principal de ESCRITA
// (o seu escopo é SEPARADO do escopo de leitura que o replay tem de deter, provando
// o IAM próprio do store).
//
// É ADITIVO e opt-in: sem esta opção o capturer mantém o comportamento inline de
// AOS-016 (payload no próprio evento do ES), byte-idêntico. A minimização/redação
// (ADR-011) é aplicada ANTES da fronteira do store — a PII nunca sai em claro nem
// para o Event Store nem para o payload store.
func WithPayloadStore(store PayloadStore, writer Accessor) CapturerOption {
	return func(c *EventStoreCapturer) {
		c.payloadStore = store
		c.writer = writer
	}
}

// NewCapturer constrói um capturer sobre o Event Store dado. store é obrigatório.
func NewCapturer(store EventStore, opts ...CapturerOption) (*EventStoreCapturer, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	c := &EventStoreCapturer{store: store, now: time.Now}
	for _, o := range opts {
		o(c)
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c, nil
}

// Capture implementa [agentruntime.Capturer]: serializa o pacote de inputs
// não-determinísticos do turno e persiste-o como um evento "replay.captured". O
// envelope usa o step_id namespaced "cap-<step_id>" para não colidir com a dedup
// do turn.recorded / ledger / checkpoint. Uma re-captura do mesmo turno dá
// StatusDuplicate no Event Store (a escrita é idempotente) — não corrompe o log.
func (c *EventStoreCapturer) Capture(ctx context.Context, tc agentruntime.TurnCapture) error {
	observedAt := c.now().UTC().UnixNano()
	payload := capturePayload{
		SchemaVersion:      captureSchemaVersion,
		Turn:               tc.Turn,
		Response:           c.encodeResponse(tc.Response),
		ToolResults:        c.encodeResults(tc.ToolResults),
		ObservedAtUnixNano: observedAt,
	}

	// MODE 3 (AOS-079): o payload completo migra para o PayloadStore externo e o
	// evento do ES fica referência-só. O payload externo já leva a redação aplicada
	// (encodeResponse/encodeResults acima) — a PII nunca sai em claro para o store.
	if c.payloadStore != nil {
		full, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		ref, err := c.payloadStore.Put(ctx, PayloadPutRequest{
			RunID:   tc.RunID,
			StepID:  tc.StepID,
			Turn:    tc.Turn,
			Payload: full,
			Writer:  c.writer,
		})
		if err != nil {
			// Fail-closed: sem o payload escrito no store não se grava um evento com uma
			// referência pendurada (o replay ficaria inadmissível).
			return err
		}
		// O evento do ES fica PEQUENO: só schema/turn/relógio + a referência opaca.
		payload = capturePayload{
			SchemaVersion:      captureSchemaVersion,
			Turn:               tc.Turn,
			ObservedAtUnixNano: observedAt,
			PayloadRef:         ref.Digest,
		}
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = c.store.Append(ctx, tc.RunID, eventstore.EventInput{
		Type:         EventTypeCaptured,
		Payload:      raw,
		RunID:        tc.RunID,
		StepID:       captureStepPrefix + tc.StepID,
		ParentStepID: tc.StepID,
		Producer:     tc.Producer,
	})
	return err
}

// encodeResponse projecta a [agentruntime.ModelResponse] no seu registo canónico,
// aplicando a guarda de segredos ([WithSensitiveResults]) quando activa: em modo
// sensível, o texto do modelo, o Input de cada tool call e o ResourceValue são
// substituídos por uma REFERÊNCIA não reversível (sha256), tal como os outputs de
// tools — PII/segredos vivem frequentemente no texto que o modelo ecoa e nos
// argumentos da tool call (ex.: o corpo de um send_email), não só no output.
func (c *EventStoreCapturer) encodeResponse(r agentruntime.ModelResponse) responseCapture {
	rc := responseCapture{
		Text:         r.Text,
		Final:        r.Final,
		InputTokens:  r.Usage.InputTokens,
		OutputTokens: r.Usage.OutputTokens,
		CostMicroUSD: r.CostMicroUSD,
	}
	if c.sensitive && rc.Text != "" {
		// NUNCA persistir o texto do modelo em claro em modo sensível — pode ecoar PII.
		rc.Text = redactRef([]byte(r.Text))
	}
	for _, tc := range r.ToolCalls {
		call := toolCallCapture{
			ToolID:         tc.ToolID,
			Capability:     tc.Capability,
			ResourceType:   tc.ResourceType,
			ResourceValue:  tc.ResourceValue,
			ResourceRegion: tc.ResourceRegion,
			Input:          tc.Input,
		}
		if c.sensitive {
			// Input e ResourceValue são os campos PII-portadores dos argumentos da tool
			// call (ex.: destinatário/corpo). Type/Region são estruturais e mantêm-se.
			if len(call.Input) > 0 {
				call.Input = []byte(redactRef(call.Input))
			}
			if call.ResourceValue != "" {
				call.ResourceValue = redactRef([]byte(call.ResourceValue))
			}
		}
		rc.ToolCalls = append(rc.ToolCalls, call)
	}
	return rc
}

// redactRef devolve a referência não reversível (sha256:<hex>) de b — a mesma
// redacção-por-referência aplicada aos outputs de tools em modo sensível.
func redactRef(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// encodeResults projecta os resultados de tool no seu registo canónico, aplicando
// a guarda de segredos (referência em vez de output em claro) quando activa.
func (c *EventStoreCapturer) encodeResults(results []agentruntime.CapturedToolResult) []toolResultCapture {
	if len(results) == 0 {
		return nil
	}
	out := make([]toolResultCapture, 0, len(results))
	for _, r := range results {
		trc := toolResultCapture{Taint: r.Result.Taint}
		if r.ToolError != nil {
			trc.ToolError = r.ToolError.Error()
		}
		if c.sensitive && len(r.Result.Value) > 0 {
			// Modo sensível: NUNCA persistir o output em claro. Guarda uma referência
			// não reversível (hash) — o replay devolve um marcador de referência.
			sum := sha256.Sum256(r.Result.Value)
			trc.Reference = true
			trc.PayloadRef = "sha256:" + hex.EncodeToString(sum[:])
		} else {
			trc.Output = r.Result.Value
		}
		out = append(out, trc)
	}
	return out
}

// decode reconstrói o [agentruntime.ModelResponse] a partir do registo canónico —
// o cliente de modelo de replay devolve exactamente esta resposta (nunca ao vivo).
func (r responseCapture) decode() agentruntime.ModelResponse {
	resp := agentruntime.ModelResponse{
		Text:         r.Text,
		Final:        r.Final,
		Usage:        agentruntime.Usage{InputTokens: r.InputTokens, OutputTokens: r.OutputTokens},
		CostMicroUSD: r.CostMicroUSD,
	}
	for _, tc := range r.ToolCalls {
		resp.ToolCalls = append(resp.ToolCalls, agentruntime.ToolInvocation{
			ToolID:         tc.ToolID,
			Capability:     tc.Capability,
			ResourceType:   tc.ResourceType,
			ResourceValue:  tc.ResourceValue,
			ResourceRegion: tc.ResourceRegion,
			Input:          tc.Input,
		})
	}
	return resp
}

// decode reconstrói o resultado da tool (untrusted) e o eventual erro a partir do
// registo canónico. Em modo referência, o valor devolvido é o marcador de
// referência (não a PII em claro).
func (t toolResultCapture) decode() (agentruntime.Tainted, error) {
	taint := t.Taint
	if taint == "" {
		taint = agentruntime.TaintUntrusted
	}
	value := t.Output
	if t.Reference {
		value = []byte(t.PayloadRef)
	}
	var toolErr error
	if t.ToolError != "" {
		toolErr = &capturedToolError{msg: t.ToolError}
	}
	return agentruntime.Tainted{Value: value, Taint: taint}, toolErr
}

// capturedToolError re-hidrata o erro de execução da tool registado no log (só a
// mensagem sobrevive à serialização — é o suficiente para o tail do replay bater).
type capturedToolError struct{ msg string }

func (e *capturedToolError) Error() string { return e.msg }
