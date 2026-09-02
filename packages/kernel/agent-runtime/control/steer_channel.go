package control

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// SignalKind é o tipo de um sinal do canal de controlo OUT-OF-BAND. Os três sinais
// são o vocabulário mínimo do controlo bidireccional de ADR-013: pausar no fim do
// turno, injectar uma correcção, retomar com ela.
type SignalKind string

const (
	// SignalPause pede a pausa graciosa do run no FIM do turno corrente.
	SignalPause SignalKind = "pause"
	// SignalSteer injecta uma correcção (instrução de controlo confiável) a aplicar na
	// retoma.
	SignalSteer SignalKind = "steer"
	// SignalResume retoma o run paused, aplicando a correcção gravada (se houver).
	SignalResume SignalKind = "resume"

	// SignalAutonomy é uma mudança de NÍVEL DE AUTONOMIA de um par (agente, domínio).
	// Não pertence a um run: o `runID` do tuplo assinado é o âmbito fixo "autonomy", e o
	// alvo concreto vai no PAYLOAD — é o que faz uma assinatura capturada não servir para
	// outro par nem para outro nível.
	SignalAutonomy SignalKind = "autonomy"

	// SignalRevoke é a REVOGAÇÃO de um token NHI pelo seu `jti` (AOS-288). Como o
	// [SignalAutonomy], não pertence a um run — o `runID` do tuplo assinado é o âmbito fixo
	// "nhi.revoke" e o `jti` alvo vai no PAYLOAD, para que uma assinatura capturada não
	// sirva para revogar outro token.
	//
	// NÃO atravessa o [SteerChannel]: a revogação é um facto de IDENTIDADE, não de controlo
	// de um run, e vive no registo consultado por `identity.Verifier.Verify`. O que traz
	// deste pacote é só o vocabulário do tuplo assinado, que é o que o
	// `integration.Ed25519Authenticator` exige e o que impede reutilizar a assinatura de um
	// pause como se fosse de uma revogação.
	SignalRevoke SignalKind = "nhi.revoke"
)

// Tipos canónicos dos eventos append-only do canal de controlo no Event Store. Cada
// sinal aceite é UM evento; a projecção corrente (pausa pendente + correcção
// pendente) é reconstruível relendo-os por ordem de seq ([SteerChannel.Rebuild]) —
// é isto que faz o ciclo pause/steer/resume SOBREVIVER A CRASH.
//
// FIDELIDADE DE REPLAY (AOS-218): o ciclo de controlo sobreviver a crash (via Rebuild) é
// distinto de o RUN steerado reproduzir-se sem divergência. A correcção que o loop injecta
// no tail (ver [agentruntime.SteerSource]/tailFromCorrection) altera o prompt do turno
// seguinte; para o replay (AOS-016) o reproduzir fielmente, essa correcção é CAPTURADA no
// evento de não-determinismo do turno ([agentruntime.TurnCapture.LeadingCorrection]) e
// re-dobrada no tail pelo motor. Antes de AOS-218 a correcção não era captada e um run
// steerado divergia espuriamente por prompt_hash — o wiring de produção (ACHADO-2) e a
// captura (ACHADO-1) fecham as duas metades: a correcção chega ao loop E reproduz-se.
const (
	// EventTypeControlPause — sinal pause aceite (running→paused no fim do turno).
	EventTypeControlPause = "control.pause"
	// EventTypeControlSteer — correcção injectada (gravada para NÃO-REPÚDIO).
	EventTypeControlSteer = "control.steer"
	// EventTypeControlResume — sinal resume aceite (paused→running com a correcção).
	EventTypeControlResume = "control.resume"
)

// controlStepPrefix namespaceia o step_id dos eventos de controlo no envelope do
// Event Store, para que a sua idempotency_key (run_id + ":ctrl-N") seja DISTINTA da
// do turno (run_id:step_id, AOS-013), do ledger (run_id:ledger-…, AOS-014), do
// checkpoint (run_id:ckpt-…, AOS-015) e da transição de estado (run_id:state-N,
// AOS-017) — a dedup do ES é global por idempotency_key.
const controlStepPrefix = "ctrl-"

// Emitter é a IDENTIDADE AUTENTICADA de quem emite um sinal de controlo. É o que dá
// NÃO-REPÚDIO (ADR-013): o ID identifica o emissor e a Signature prova que foi ELE a
// emitir ESTE sinal para ESTE run. Ambos são gravados no evento append-only — o log
// prova quem pausou, corrigiu ou retomou o run.
//
// O Emitter é DELIBERADAMENTE um contrato mínimo local (não importa o módulo
// platform/identity de AOS-005): isso manteria o agent-runtime com ZERO dependências
// externas e evita um ciclo de módulos. Um adaptador de 3 linhas liga a identidade
// ed25519 real de AOS-005 a este contrato quando o wiring de superfície (EPIC-12) o
// exigir — aqui expõe-se só a API do canal.
type Emitter struct {
	// ID é o identificador do emissor (p.ex. a NHI do operador humano ou do serviço de
	// superfície). Obrigatório — sem ID não há não-repúdio.
	ID string
	// Signature é a assinatura sobre (run_id ‖ kind ‖ payload [‖ nonce ‖ issued_at])
	// verificável pelo [Authenticator]. Uma assinatura ausente/inválida ⇒ sinal
	// REJEITADO.
	Signature []byte
	// Nonce é o material anti-replay de USO-ÚNICO deste sinal. Campo ADITIVO (AOS-160):
	// o [HMACAuthenticator] de referência IGNORA-o (mantém o comportamento demo-grade
	// intacto); um [Authenticator] de produção (ed25519) FAZ a assinatura cobri-lo e
	// consome-o num nonce-store durável, pelo que o MESMO sinal capturado não se
	// re-verifica. Vazio nos emissores que não fazem anti-replay por nonce.
	Nonce []byte
	// IssuedAt é o carimbo temporal de EMISSÃO deste sinal. Campo ADITIVO (AOS-160):
	// ignorado pelo [HMACAuthenticator]; um [Authenticator] de produção cobre-o na
	// assinatura e rejeita carimbos fora da janela de frescura (expiração). Zero nos
	// emissores sem frescura.
	IssuedAt time.Time
}

// Authenticator é a FRONTEIRA DE SEGURANÇA do canal (ADR-013 + ADR-005). Verifica que
// um [Emitter] é um emissor legítimo do plano de controlo para ESTE sinal sobre ESTE
// run. É o que impede a escalada de privilégio: conteúdo untrusted (resultado de tool
// / web) não carrega uma credencial de emissor válida, logo Authenticate rejeita-o e
// ele NUNCA se torna um sinal de controlo. Só um sinal cuja assinatura valida dirige
// o agente.
type Authenticator interface {
	// Authenticate devolve nil sse o emissor está autorizado a emitir kind sobre runID
	// com este payload. Qualquer falha (emissor desconhecido, assinatura inválida,
	// sinal adulterado) devolve um erro NÃO-nil — o sinal é rejeitado fail-closed.
	Authenticate(ctx context.Context, runID string, kind SignalKind, payload []byte, emitter Emitter) error
}

// Clock é o relógio INJECTÁVEL do canal — a fonte do carimbo temporal gravado em cada
// evento de controlo. Injectá-lo torna os testes determinísticos (sem sleeps). Default:
// [systemClock].
type Clock interface {
	Now() time.Time
}

// ClockFunc adapta uma função a [Clock].
type ClockFunc func() time.Time

// Now implementa [Clock].
func (f ClockFunc) Now() time.Time { return f() }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// EventStore é o subconjunto do Event Store (AOS-002) de que o [SteerChannel] depende:
// Append (persistência durável append-only com dedup por idempotency_key) e Read
// (reconstrução da projecção corrente a partir do log). *eventstore.Store satisfá-lo.
type EventStore interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// controlRecord é o payload JSON persistido em cada evento de controlo. Guarda a
// identidade e a assinatura do emissor (NÃO-REPÚDIO) e, no steer, a correcção. A
// assinatura é gravada em base64 — não é segredo, é a PROVA de autoria auditável.
type controlRecord struct {
	Kind       SignalKind `json:"kind"`
	EmitterID  string     `json:"emitter_id"`
	Signature  string     `json:"signature,omitempty"`  // base64(assinatura) — prova de autoria
	Correction string     `json:"correction,omitempty"` // só no steer; a instrução confiável
	At         string     `json:"at"`                   // RFC3339Nano, relógio injectável
}

// runControl é a PROJECÇÃO in-memory do estado de controlo de um run — uma dobra dos
// eventos de controlo do log. É reconstruível a qualquer momento por [Rebuild], pelo
// que sobrevive a crash.
type runControl struct {
	nControls      uint64 // nº de eventos de controlo já persistidos (piso do step_id ctrl-N)
	pauseRequested bool   // há um pause pendente ainda não retomado (count(pause) > count(resume))
	correction     []byte // correcção da última steer desde a última resume
	correctionBy   string // emissor autenticado da correcção pendente (rasto de não-repúdio)
	hasCorrection  bool
}

// SteerChannel é o CANAL DE CONTROLO OUT-OF-BAND de AOS-023 (ADR-013). É SEPARADO do
// canal de DADOS (o prompt): os sinais pause/steer/resume entram por aqui, por run_id,
// nunca pelo prompt. Cada sinal aceite é autenticado ([Authenticator]) e gravado como
// evento append-only no Event Store — durável e reconstruível por replay. A projecção
// corrente (pausa/correcção pendentes) é uma dobra desses eventos.
//
// Seguro para uso concorrente (um mutex serializa a projecção e a atribuição de
// step_ids ctrl-N monotónicos).
type SteerChannel struct {
	mu    sync.Mutex
	store EventStore
	auth  Authenticator

	producer eventstore.Producer
	clock    Clock
	tracer   agentruntime.Tracer

	runs map[string]*runControl
}

// ChannelOption configura o [SteerChannel] na construção.
type ChannelOption func(*SteerChannel)

// WithProducer define a identidade emissora (NHI + cadeia de delegação) gravada no
// envelope dos eventos de controlo. É a identidade do CANAL/serviço; a identidade do
// EMISSOR do sinal individual vai no payload ([Emitter]). Default: Producer zero.
func WithProducer(p eventstore.Producer) ChannelOption {
	return func(c *SteerChannel) { c.producer = p }
}

// WithClock injecta o relógio (default [systemClock]). Usar nos testes determinísticos.
func WithClock(c Clock) ChannelOption { return func(sc *SteerChannel) { sc.clock = c } }

// WithTracer reusa a porta de observabilidade do Agent Runtime (AOS-013): abre um span
// por sinal aceite. Default [agentruntime.NoopTracer].
func WithTracer(t agentruntime.Tracer) ChannelOption {
	return func(c *SteerChannel) { c.tracer = t }
}

// NewChannel constrói o canal sobre um Event Store e um [Authenticator]. Ambos são
// OBRIGATÓRIOS: sem store não há durabilidade; sem autenticador não há fronteira de
// segurança (o canal recusa-se a existir — fail-closed na construção).
func NewChannel(store EventStore, auth Authenticator, opts ...ChannelOption) (*SteerChannel, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if auth == nil {
		return nil, ErrNilAuthenticator
	}
	c := &SteerChannel{
		store:  store,
		auth:   auth,
		clock:  systemClock{},
		tracer: agentruntime.NoopTracer{},
		runs:   make(map[string]*runControl),
	}
	for _, o := range opts {
		o(c)
	}
	if c.clock == nil {
		c.clock = systemClock{}
	}
	if c.tracer == nil {
		c.tracer = agentruntime.NoopTracer{}
	}
	return c, nil
}

// runState devolve (criando se necessário) a projecção do run. Assume o lock detido.
func (c *SteerChannel) runState(runID string) *runControl {
	rc := c.runs[runID]
	if rc == nil {
		rc = &runControl{}
		c.runs[runID] = rc
	}
	return rc
}

// Pause emite um sinal PAUSE out-of-band para runID. O sinal é autenticado e gravado
// como evento append-only (com a identidade do emissor, para não-repúdio) e marca uma
// pausa PENDENTE. NÃO transita a máquina de estados de imediato: a pausa é GRACIOSA —
// só se materializa (running→paused) no FIM do turno corrente, quando o loop chama
// [SteerChannel.GracefulPause]. Assim um pause emitido a MEIO de um turno nunca
// interrompe uma activity a meio (não deixa efeitos parciais).
//
// Rejeita [ErrUnauthenticated] se a assinatura do emissor não validar (fronteira de
// segurança — conteúdo untrusted não pode pausar o run).
func (c *SteerChannel) Pause(ctx context.Context, runID string, emitter Emitter) error {
	return c.record(ctx, runID, SignalPause, nil, emitter)
}

// Steer injecta uma CORRECÇÃO no run via o canal de controlo. A correcção é AUTENTICADA
// (assinatura do emissor) e gravada como evento append-only `control.steer` COM a
// identidade do emissor — o registo de NÃO-REPÚDIO exigido por ADR-013. É uma INSTRUÇÃO
// do canal de controlo, DISTINTA de dados untrusted (ADR-005): só entra porque a sua
// assinatura valida. A correcção fica PENDENTE e é aplicada na retoma
// ([SteerChannel.Resume]), injectada no loop como instrução CONFIÁVEL (taint trusted),
// nunca como dado untrusted.
//
// Rejeita [ErrUnauthenticated] se a assinatura não validar — é AQUI que a escalada de
// privilégio é impedida: conteúdo untrusted não tem credencial de emissor válida, logo
// NUNCA se torna um steer.
func (c *SteerChannel) Steer(ctx context.Context, runID string, correction []byte, emitter Emitter) error {
	if len(correction) == 0 {
		return ErrEmptyCorrection
	}
	return c.record(ctx, runID, SignalSteer, correction, emitter)
}

// record é o caminho comum de aceitação de um sinal: valida entradas → AUTENTICA →
// grava o evento append-only (não-repúdio) → actualiza a projecção. A autenticação
// acontece ANTES de qualquer escrita — um sinal não autenticado nunca toca no log nem
// na projecção (fail-closed).
func (c *SteerChannel) record(ctx context.Context, runID string, kind SignalKind, payload []byte, emitter Emitter) error {
	if runID == "" {
		return ErrEmptyRunID
	}
	if emitter.ID == "" {
		return ErrEmptyEmitterID
	}
	// FRONTEIRA DE SEGURANÇA (ADR-013/005): sem assinatura válida, o sinal é recusado
	// ANTES de tocar no log. É isto que garante que conteúdo untrusted não se torna um
	// sinal de controlo.
	if err := c.auth.Authenticate(ctx, runID, kind, payload, emitter); err != nil {
		return fmt.Errorf("%w: emissor %q, kind %q", ErrUnauthenticated, emitter.ID, kind)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	rc := c.runState(runID)

	rec := c.newRecord(kind, emitter, payload)
	if err := c.appendControl(ctx, runID, rc, rec); err != nil {
		return err
	}

	// Actualiza a projecção APÓS o commit durável.
	c.apply(rc, rec)
	c.emitSpan(ctx, runID, kind, emitter.ID)
	return nil
}

// newRecord constrói o payload de um evento de controlo, carimbando o relógio injectado
// e a assinatura do emissor (base64) para não-repúdio. A correcção só é preenchida no
// steer.
func (c *SteerChannel) newRecord(kind SignalKind, emitter Emitter, correction []byte) controlRecord {
	rec := controlRecord{
		Kind:      kind,
		EmitterID: emitter.ID,
		Signature: base64.StdEncoding.EncodeToString(emitter.Signature),
		At:        c.clock.Now().UTC().Format(time.RFC3339Nano),
	}
	if kind == SignalSteer {
		rec.Correction = string(correction)
	}
	return rec
}

// appendControl grava um evento de controlo com o step_id ctrl-N namespaced e único
// por run. Assume o lock detido. Só incrementa o contador de controlos APÓS o append
// ter sucesso (sem furos na sequência ctrl-N em caso de falha do Event Store).
//
// Reconcilia o dedup fail-closed (espelha [state.Machine] em AOS-017): o Event Store
// devolve erro NIL com [eventstore.StatusDuplicate] quando a idempotency_key ctrl-N já
// existia — e IGNORA o payload novo (o registo original vence, devolvido em res.Event).
// Se o evento já persistido sob esta chave NÃO for o MESMO sinal que se pediu, dobrar o
// registo NOVO na projecção divergiria silenciosamente do log durável; devolve então
// [ErrControlLogDivergence] sem mutar a projecção. Um duplicado cujo sinal bate
// exactamente (retry benigno após um commit-mas-erro) é aceite — a dobra é idempotente.
func (c *SteerChannel) appendControl(ctx context.Context, runID string, rc *runControl, rec controlRecord) error {
	payload, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	stepID := controlStepPrefix + strconv.FormatUint(rc.nControls+1, 10)
	res, err := c.store.Append(ctx, runID, eventstore.EventInput{
		Type:     eventTypeFor(rec.Kind),
		Payload:  payload,
		RunID:    runID,
		StepID:   stepID,
		Producer: c.producer,
	})
	if err != nil {
		return err
	}
	// Reconciliação fail-closed do dedup por idempotency_key.
	if res.Status != eventstore.StatusCommitted {
		var persisted controlRecord
		if uerr := json.Unmarshal(res.Event.Payload, &persisted); uerr != nil {
			return fmt.Errorf("%w: %s (seq=%d): %v", ErrControlLogDivergence, stepID, res.Seq, uerr)
		}
		// O carimbo temporal (At) e nada mais define a IDENTIDADE do sinal: um retry
		// benigno relê o relógio, logo At pode diferir legitimamente. A identidade
		// semântica é (kind ‖ emissor ‖ correcção) — é isso que tem de bater.
		if persisted.Kind != rec.Kind || persisted.EmitterID != rec.EmitterID || persisted.Correction != rec.Correction {
			return fmt.Errorf("%w: %s persistido=(kind=%s,emitter=%s) pedido=(kind=%s,emitter=%s) (seq=%d)",
				ErrControlLogDivergence, stepID, persisted.Kind, persisted.EmitterID, rec.Kind, rec.EmitterID, res.Seq)
		}
	}
	rc.nControls++
	return nil
}

// apply dobra um registo de controlo na projecção in-memory. É a MESMA dobra usada por
// [Rebuild] a partir do log — garantindo que o estado in-memory e o reconstruído por
// replay coincidem sempre (durabilidade + replay fiel).
func (c *SteerChannel) apply(rc *runControl, rec controlRecord) {
	switch rec.Kind {
	case SignalPause:
		rc.pauseRequested = true
	case SignalSteer:
		rc.correction = []byte(rec.Correction)
		rc.correctionBy = rec.EmitterID
		rc.hasCorrection = true
	case SignalResume:
		rc.pauseRequested = false
		rc.correction = nil
		rc.correctionBy = ""
		rc.hasCorrection = false
	}
}

// eventTypeFor mapeia um kind ao tipo de evento canónico.
func eventTypeFor(kind SignalKind) string {
	switch kind {
	case SignalPause:
		return EventTypeControlPause
	case SignalSteer:
		return EventTypeControlSteer
	case SignalResume:
		return EventTypeControlResume
	default:
		return "control." + string(kind)
	}
}

// PendingPause indica se há uma pausa pendente para runID (emitida e ainda não
// retomada). O loop consulta-o no FIM do turno para decidir se faz a pausa graciosa.
func (c *SteerChannel) PendingPause(runID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	rc := c.runs[runID]
	return rc != nil && rc.pauseRequested
}

// PendingCorrection devolve a correcção pendente de runID (a última steer desde a
// última resume) e se existe. É a correcção que a retoma aplicará como instrução
// confiável.
func (c *SteerChannel) PendingCorrection(runID string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rc := c.runs[runID]
	if rc == nil || !rc.hasCorrection {
		return nil, false
	}
	out := make([]byte, len(rc.correction))
	copy(out, rc.correction)
	return out, true
}

// Rebuild reconstrói a projecção de controlo de runID RELENDO os eventos de controlo do
// Event Store e dobrando-os por ordem de seq. É a materialização de "o ciclo pause/
// steer/resume é reconstruível por replay" e o que o faz SOBREVIVER A CRASH: um worker
// novo constrói um [SteerChannel] sobre o mesmo cluster, chama Rebuild e recupera a
// pausa e a correcção pendentes INTACTAS. Um stream inexistente ⇒ projecção vazia.
//
// Fail-closed: um evento de controlo cujo payload não descodifica ou cujo kind é
// desconhecido aborta com [ErrCorruptControlLog] em vez de perder o sinal.
func (c *SteerChannel) Rebuild(ctx context.Context, runID string) error {
	if runID == "" {
		return ErrEmptyRunID
	}
	events, err := c.store.Read(ctx, runID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			c.mu.Lock()
			c.runs[runID] = &runControl{}
			c.mu.Unlock()
			return nil
		}
		return err
	}

	rc := &runControl{}
	for i := range events {
		if !isControlEvent(events[i].Type) {
			continue
		}
		var rec controlRecord
		if uerr := json.Unmarshal(events[i].Payload, &rec); uerr != nil {
			return fmt.Errorf("%w: seq=%d: %v", ErrCorruptControlLog, events[i].Seq, uerr)
		}
		if !isKnownKind(rec.Kind) {
			return fmt.Errorf("%w: kind %q (seq=%d)", ErrCorruptControlLog, rec.Kind, events[i].Seq)
		}
		if n, ok := parseControlStepID(events[i].StepID); ok && n > rc.nControls {
			rc.nControls = n
		} else if !ok {
			rc.nControls++
		}
		c.apply(rc, rec)
	}

	c.mu.Lock()
	c.runs[runID] = rc
	c.mu.Unlock()
	return nil
}

// emitSpan abre e fecha um span de sinal de controlo na porta [agentruntime.Tracer]
// reusada de AOS-013. Sem segredos: só run_id, kind e emitter_id (rótulos) entram.
func (c *SteerChannel) emitSpan(ctx context.Context, runID string, kind SignalKind, emitterID string) {
	_, span := c.tracer.StartSpan(ctx, OpControlSignal)
	span.SetAttribute(agentruntime.AttrOperationName, OpControlSignal)
	span.SetAttribute(agentruntime.AttrRunID, runID)
	span.SetAttribute(AttrControlSignal, string(kind))
	span.SetAttribute(AttrControlEmitter, emitterID)
	span.End()
}

// Atributos e operação de span do canal de controlo (observabilidade dos sinais).
const (
	// OpControlSignal — nome de operação do span de um sinal de controlo.
	OpControlSignal = "control_signal"
	// AttrControlSignal — aos.control.signal (pause|steer|resume).
	AttrControlSignal = "aos.control.signal"
	// AttrControlEmitter — aos.control.emitter (ID do emissor, para correlação de audit).
	AttrControlEmitter = "aos.control.emitter"
)

func isControlEvent(t string) bool {
	return t == EventTypeControlPause || t == EventTypeControlSteer || t == EventTypeControlResume
}

func isKnownKind(k SignalKind) bool {
	return k == SignalPause || k == SignalSteer || k == SignalResume
}

// parseControlStepID extrai o N de um step_id "ctrl-N". Devolve (0, false) para
// step_ids sem o prefixo ou com sufixo não numérico.
func parseControlStepID(stepID string) (uint64, bool) {
	if !strings.HasPrefix(stepID, controlStepPrefix) {
		return 0, false
	}
	n, err := strconv.ParseUint(stepID[len(controlStepPrefix):], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// ---------------------------------------------------------------------------
// HMACAuthenticator — realização de REFERÊNCIA de [Authenticator].
// ---------------------------------------------------------------------------

// HMACAuthenticator é uma realização de referência de [Authenticator] baseada em HMAC-
// SHA256 com um segredo partilhado por emissor registado. Usa APENAS a stdlib (crypto/
// hmac, crypto/sha256) — ZERO dependências externas. A assinatura cobre
// (run_id ‖ kind ‖ payload), pelo que uma assinatura de um sinal NÃO pode ser reusada
// para outro run/kind/payload (defesa contra replay).
//
// É deliberadamente mínima: a identidade ed25519 real com cadeia de delegação é
// platform/identity (AOS-005), ligável por um adaptador quando o wiring de superfície
// (EPIC-12) o exigir. O que importa em AOS-023 é a FRONTEIRA — um emissor sem chave
// registada (p.ex. um caminho que tentasse promover conteúdo untrusted a steer) falha
// sempre a verificação.
//
// LIMITE DE REPLAY (não usar em produção como está). A assinatura cobre apenas
// (run_id ‖ kind ‖ payload) — SEM nonce, sequência nem expiração. Impede o replay
// CROSS-run/kind/payload (uma assinatura de um sinal não vale para outro), mas NÃO
// defende contra o replay do MESMO sinal: um (run_id,kind,payload,emitter,assinatura)
// capturado re-verifica sempre — p.ex. um control.resume legítimo capturado poderia ser
// reenviado para retomar um run que um operador quisesse manter em pausa. O adaptador
// que ligar a identidade real (AOS-005) DEVE incluir uma sequência monotónica ou um
// nonce/expiração por run no material assinado e rejeitar identificadores de sinal já
// vistos. Esta realização de referência destina-se a testes determinísticos e à prova
// da fronteira, não a implantação em produção.
type HMACAuthenticator struct {
	mu   sync.RWMutex
	keys map[string][]byte
}

// NewHMACAuthenticator constrói um autenticador sem emissores registados (default-deny:
// qualquer sinal é rejeitado até um emissor ser registado com [Register]).
func NewHMACAuthenticator() *HMACAuthenticator {
	return &HMACAuthenticator{keys: make(map[string][]byte)}
}

// Register regista (ou substitui) o segredo de um emissor. Um emissor sem segredo
// registado NUNCA autentica — é o default-deny que sustenta a fronteira de segurança.
func (a *HMACAuthenticator) Register(emitterID string, secret []byte) {
	key := make([]byte, len(secret))
	copy(key, secret)
	a.mu.Lock()
	a.keys[emitterID] = key
	a.mu.Unlock()
}

// Sign calcula a assinatura HMAC de um emissor sobre (run_id ‖ kind ‖ payload) e
// devolve o [Emitter] pronto a passar a um sinal. É um HELPER para emissores legítimos
// e testes; um adversário sem o segredo registado não a consegue produzir. Devolve
// [ErrUnauthenticated] se o emissor não estiver registado.
func (a *HMACAuthenticator) Sign(runID string, kind SignalKind, payload []byte, emitterID string) (Emitter, error) {
	a.mu.RLock()
	key, ok := a.keys[emitterID]
	a.mu.RUnlock()
	if !ok {
		return Emitter{}, ErrUnauthenticated
	}
	return Emitter{ID: emitterID, Signature: mac(key, runID, kind, payload)}, nil
}

// Authenticate implementa [Authenticator]: recalcula o HMAC esperado com o segredo do
// emissor e compara em tempo constante. Emissor desconhecido ou assinatura divergente
// ⇒ [ErrUnauthenticated].
func (a *HMACAuthenticator) Authenticate(_ context.Context, runID string, kind SignalKind, payload []byte, emitter Emitter) error {
	a.mu.RLock()
	key, ok := a.keys[emitter.ID]
	a.mu.RUnlock()
	if !ok {
		return ErrUnauthenticated
	}
	expected := mac(key, runID, kind, payload)
	if !hmac.Equal(expected, emitter.Signature) {
		return ErrUnauthenticated
	}
	return nil
}

// mac calcula HMAC-SHA256 sobre run_id ‖ kind ‖ payload com separadores de domínio
// (0x00) para que fronteiras distintas de campos não colidam.
func mac(key []byte, runID string, kind SignalKind, payload []byte) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(runID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(kind))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(payload)
	return h.Sum(nil)
}
