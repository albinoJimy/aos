package state

import (
	"context"
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

// EventTypeTransition é o tipo canónico do evento append-only que materializa uma
// transição de estado do run no Event Store. Cada transição confirmada é UM evento;
// o estado corrente é reconstruível relendo-os por ordem de seq ([Machine.Rebuild]).
const EventTypeTransition = "run.state.transition"

// transitionStepPrefix namespaceia o step_id do evento de transição no envelope do
// Event Store, para que a sua idempotency_key (run_id + ":state-N") seja DISTINTA da
// do turno (run_id:step_id, AOS-013), do ledger (run_id:ledger-…, AOS-014) e do
// checkpoint (run_id:ckpt-…, AOS-015) — a dedup do ES é global por idempotency_key.
const transitionStepPrefix = "state-"

// Razões canónicas gravadas no evento de transição (campo Reason). São rótulos
// legíveis, não segredos; ajudam a auditar POR QUE a transição ocorreu.
const (
	// ReasonHumanTimeout — waiting_on_human → killed por timeout fail-closed (ADR-013).
	ReasonHumanTimeout = "human_approval_timeout_fail_closed"
	// ReasonWallClockTimeout — running → timed_out por exceder o wall-clock.
	ReasonWallClockTimeout = "wall_clock_exceeded"
)

// FencingToken é o CONTRATO do token de fencing monotónico partilhado com AOS-018
// (lease/heartbeat) e o Escalonador do EPIC-03. AOS-017 só define o contrato e
// VERIFICA a sua presença/validade na entrada em running (o claim ready → running);
// a atribuição monotónica, a renovação por heartbeat e a rejeição de escritas de
// token inferior no Event Store são AOS-018 — NÃO implementadas aqui.
//
// Value é o contador monotónico (para o fencing durável de AOS-018); Valid indica
// se o token é utilizável AGORA para reclamar o run.
type FencingToken interface {
	// Valid reporta se o token é utilizável para entrar em running.
	Valid() bool
	// Value devolve o valor monotónico do token (0 é reservado a "sem token").
	Value() uint64
}

// Uint64Token é a realização de referência (mínima) de um [FencingToken]: um
// contador monotónico onde qualquer valor > 0 é válido (0 = ausência de token). A
// ORIGEM monotónica durável do contador é AOS-018 — aqui é só o tipo do contrato.
type Uint64Token uint64

// Valid implementa [FencingToken]: qualquer token > 0 é válido.
func (t Uint64Token) Valid() bool { return t > 0 }

// Value implementa [FencingToken].
func (t Uint64Token) Value() uint64 { return uint64(t) }

// FencingAuthority reporta o token de fencing CORRENTE de um run, permitindo à
// [Machine] recusar um claim (ready → running) OBSOLETO — não apenas um claim com
// token ausente/inválido. É OPCIONAL: sem ela (default) o claim impõe SÓ a PRESENÇA/
// validade do token (o contrato mínimo de AOS-017), delegando a staleness ao
// [durable.FencedAppender] de AOS-018 (o único caminho que hoje a fecha). Com ela
// ([WithFencingAuthority]) a máquina TAMBÉM fail-closed um claim de worker superado
// (token < corrente), fechando a lacuna de "presença ≠ staleness" no PRÓPRIO caminho
// da transição.
//
// A durabilidade e a monotonicidade do "corrente" são responsabilidade da autoridade
// (no AOS-018, o LeaseManager lê-o do stream de lease). O tipo de retorno é uint64
// (não [FencingToken]) DE PROPÓSITO — para que a autoridade de AOS-018 seja adaptável
// com um wrapper de 3 linhas SEM o pacote state importar durable (ligação por
// interface implícita, como o resto do contrato de fencing).
type FencingAuthority interface {
	// CurrentTokenValue devolve o valor do token corrente (o maior alguma vez mintado)
	// do run, ou 0 se o run nunca foi reclamado.
	CurrentTokenValue(ctx context.Context, runID string) (uint64, error)
}

// TransitionEvent transporta os metadados de uma transição: o motivo (rótulo de
// auditoria) e, quando a transição é o claim ready → running, o fencing token que a
// máquina VERIFICA. Os restantes campos das transições (from/to) são posicionais
// (argumentos de [Machine.Transition]).
type TransitionEvent struct {
	// Reason é o rótulo legível da causa (ex.: "risk_gate", "approval"). Opcional.
	Reason string
	// Token é o fencing token. OBRIGATÓRIO e verificado apenas quando a transição
	// exige fencing ([RequiresFencingToken] — o claim ready → running). Ignorado nas
	// demais transições (as retomas reentram sob o lease já detido).
	Token FencingToken
}

// Clock é o relógio INJECTÁVEL da máquina — a fonte do wall-clock que decide os
// timeouts (fail-closed do gate humano e timed_out do running). Injectá-lo torna os
// testes de timeout DETERMINÍSTICOS, sem sleeps frágeis. Default: [systemClock].
type Clock interface {
	Now() time.Time
}

// ClockFunc adapta uma função a [Clock].
type ClockFunc func() time.Time

// Now implementa [Clock].
func (f ClockFunc) Now() time.Time { return f() }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// TransitionObserver é o gancho de observabilidade das transições (contadores/
// estado). Recebe from/to/reason — rótulos, nunca segredos nem o valor do token.
// Default: [NopTransitionObserver].
type TransitionObserver interface {
	// Transitioned é chamado APÓS uma transição confirmada e persistida.
	Transitioned(from, to State, reason string)
	// Rejected é chamado quando uma transição é recusada (par inválido, token em
	// falta ou falha do Event Store) — o estado NÃO mudou.
	Rejected(from, to State, err error)
}

// NopTransitionObserver descarta os eventos de observabilidade. É o default.
type NopTransitionObserver struct{}

// Transitioned implementa [TransitionObserver].
func (NopTransitionObserver) Transitioned(State, State, string) {}

// Rejected implementa [TransitionObserver].
func (NopTransitionObserver) Rejected(State, State, error) {}

// EventStore é o subconjunto do Event Store (AOS-002) de que a [Machine] depende:
// Append (persistência durável append-only com dedup por idempotency_key) e Read
// (reconstrução do estado corrente a partir do log). *eventstore.Store satisfaz-o.
type EventStore interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// transitionRecord é o payload JSON persistido em cada evento de transição. O
// estado corrente reconstrói-se lendo o From/To do ÚLTIMO destes eventos (o de seq
// mais alto). Não guarda segredos: o token entra apenas como o seu valor monotónico
// (não é segredo — é o que AOS-018 usa para fencing) e um flag de presença.
type transitionRecord struct {
	From       State  `json:"from"`
	To         State  `json:"to"`
	Reason     string `json:"reason,omitempty"`
	TokenValue uint64 `json:"token_value,omitempty"`
	At         string `json:"at"` // RFC3339Nano, wall-clock da transição (relógio injectável)
}

// Machine é a MÁQUINA DE ESTADOS DURÁVEL do run (AOS-017). Cada transição válida é
// um evento append-only no Event Store; o estado corrente é sempre reconstruível
// por replay ([Rebuild]) — sobrevive a crash do worker. Transições inválidas são
// rejeitadas SEM tocar no estado persistido.
//
// # Ordem de confirmação (não-corrupção)
//
// O estado in-memory só avança DEPOIS de o Append durável ter sucesso. Logo, uma
// transição inválida (par fora da tabela ou token em falta) ou uma falha do Event
// Store (p.ex. perda de quórum — fail-closed) deixa o estado — persistido E
// in-memory — EXACTAMENTE como estava.
//
// Segura para uso concorrente (um mutex serializa as transições, garantindo também
// step_ids "state-N" monotónicos e sem colisão).
type Machine struct {
	// mu SERIALIZA as mutações: [Machine.Transition], [Machine.CheckDeadlines] e
	// [Machine.Rebuild] detêm-no do princípio ao fim. É ele que torna a validação
	// (IsValidTransition contra o estado corrente) ATÓMICA face à escrita — a
	// propriedade de que AOS-291 passou a depender ao largar o lock do disjuntor,
	// e que este desdobramento NÃO pode enfraquecer.
	//
	// NÃO protege leituras. Ver estadoMu.
	mu sync.Mutex
	// estadoMu protege APENAS `current`, `enteredAt` e `nStates` — e nunca é detido
	// durante I/O (AOS-301).
	//
	// PORQUÊ DOIS LOCKS. Até AOS-301 havia um só, e `Transition` detinha-o durante a
	// consulta à [FencingAuthority] (rede), o Append (rede/disco), o `span.End()`
	// (Exporter.Export, síncrono) e o callback do [TransitionObserver] (código de
	// terceiros). Como `Current()` e `EnteredAt()` tomavam o MESMO mutex, um Append
	// lento prendia quem só queria LER o estado — e é isso que AOS-291 mediu e
	// declarou por fechar: o `Abort`/`EscalateToHuman` do disjuntor lêem `Current()`,
	// e o `Snapshot` com wall-clock lê `EnteredAt()`. O instante em que se quer
	// abortar era o instante em que a leitura ficava trancada.
	//
	// O QUE UM LEITOR PODE OBSERVAR e é correcto: durante a janela de I/O de uma
	// transição, o estado ainda é o ANTERIOR. Já era assim — o código só avança o
	// estado in-memory DEPOIS do commit durável (passo 4 de doTransition) — pelo que
	// o desdobramento não introduz visibilidade nova: troca «o leitor espera pelo
	// resultado» por «o leitor vê o estado que ainda está em vigor».
	estadoMu sync.RWMutex
	store    EventStore
	runID    string
	producer eventstore.Producer
	clock    Clock
	tracer   agentruntime.Tracer
	obs      TransitionObserver

	humanTTL  time.Duration    // fail-closed de waiting_on_human → killed (0 = desligado)
	wallClock time.Duration    // running → timed_out (0 = desligado)
	fencing   FencingAuthority // opcional: se ligada, o claim recusa tokens obsoletos (nil = só presença)

	current   State     // estado corrente (Ready por omissão / após reconstrução)
	enteredAt time.Time // quando o estado corrente foi entrado (base dos timeouts)
	nStates   uint64    // nº de eventos de transição já persistidos (para o step_id)
}

// Option configura a [Machine] na construção.
type Option func(*Machine)

// WithClock injecta o relógio (default [systemClock]). Usar nos testes de timeout.
func WithClock(c Clock) Option { return func(m *Machine) { m.clock = c } }

// WithProducer define a identidade emissora (NHI + cadeia de delegação) gravada nos
// eventos de transição. Default: Producer zero (aceitável em teste).
func WithProducer(p eventstore.Producer) Option { return func(m *Machine) { m.producer = p } }

// WithTracer reusa a porta de observabilidade do Agent Runtime (AOS-013): abre um
// span por transição confirmada. Default: [agentruntime.NoopTracer].
func WithTracer(t agentruntime.Tracer) Option { return func(m *Machine) { m.tracer = t } }

// WithObserver injecta o gancho de contadores/estado (default [NopTransitionObserver]).
func WithObserver(o TransitionObserver) Option { return func(m *Machine) { m.obs = o } }

// WithHumanApprovalTTL define o TTL do gate humano: passado o TTL sem aprovação,
// [Machine.CheckDeadlines] transita waiting_on_human → killed (fail-closed, ADR-013).
// 0 (default) desliga o fail-closed automático.
func WithHumanApprovalTTL(d time.Duration) Option {
	return func(m *Machine) { m.humanTTL = d }
}

// WithRunWallClock define o wall-clock máximo em running: excedido, [Machine.CheckDeadlines]
// transita running → timed_out. Conta a partir da ENTRADA no running corrente
// ("a partir de running", AOS-017 critério 5). 0 (default) desliga.
func WithRunWallClock(d time.Duration) Option {
	return func(m *Machine) { m.wallClock = d }
}

// WithFencingAuthority liga uma [FencingAuthority] que torna o claim ready → running
// sensível à STALENESS: além de exigir um token válido, a máquina consulta o token
// CORRENTE do run e recusa ([ErrStaleFencingToken]) um claim cujo token seja inferior
// — um worker superado por um novo claim NÃO materializa a transição. Default: nil (a
// máquina impõe só a PRESENÇA do token, delegando a staleness ao FencedAppender de
// AOS-018). Passar nil mantém o comportamento default.
func WithFencingAuthority(a FencingAuthority) Option {
	return func(m *Machine) { m.fencing = a }
}

// NewMachine constrói uma máquina para o run dado, começando em [Ready] (o estado
// inicial implícito — não gera evento; a primeira transição confirmada é que o faz).
// store e runID são obrigatórios.
//
// Para reconstruir uma máquina após crash a partir do log, construa com NewMachine
// e chame [Machine.Rebuild] — que relê o stream e adopta o estado corrente do último
// evento de transição.
func NewMachine(store EventStore, runID string, opts ...Option) (*Machine, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if runID == "" {
		return nil, ErrEmptyRunID
	}
	m := &Machine{
		store:  store,
		runID:  runID,
		clock:  systemClock{},
		tracer: agentruntime.NoopTracer{},
		obs:    NopTransitionObserver{},
	}
	for _, o := range opts {
		o(m)
	}
	if m.clock == nil {
		m.clock = systemClock{}
	}
	if m.tracer == nil {
		m.tracer = agentruntime.NoopTracer{}
	}
	if m.obs == nil {
		m.obs = NopTransitionObserver{}
	}
	m.current = Ready
	m.enteredAt = m.clock.Now()
	return m, nil
}

// RunID devolve o identificador do run (stream_id no Event Store).
func (m *Machine) RunID() string { return m.runID }

// instalarEstado publica os três campos mutáveis sob estadoMu. Chama-se sempre com `mu`
// já detido (é uma mutação), e a secção é curta por construção: nunca há I/O aqui.
func (m *Machine) instalarEstado(current State, enteredAt time.Time, nStates uint64) {
	m.estadoMu.Lock()
	defer m.estadoMu.Unlock()
	m.current = current
	m.enteredAt = enteredAt
	m.nStates = nStates
}

// lerEstado devolve um instantâneo COERENTE dos três campos. Ler cada um por sua vez
// deixaria uma janela em que o `current` é o novo e o `enteredAt` o antigo — e é sobre
// esse par que [Machine.CheckDeadlines] decide um kill fail-closed.
func (m *Machine) lerEstado() (State, time.Time, uint64) {
	m.estadoMu.RLock()
	defer m.estadoMu.RUnlock()
	return m.current, m.enteredAt, m.nStates
}

// Current devolve o estado corrente da máquina.
func (m *Machine) Current() State {
	m.estadoMu.RLock()
	defer m.estadoMu.RUnlock()
	return m.current
}

// EnteredAt devolve o instante (wall-clock do relógio injectado) em que o estado
// corrente foi entrado — a base a partir da qual os timeouts são medidos.
//
// NOTA (wall-clock, AOS-019): o instante é gravado com a componente MONOTÓNICA
// removida (doTransition faz .UTC() para o serializar em RFC3339Nano). Logo os
// deadlines derivados dele — tanto [Machine.CheckDeadlines] como o WaitingGate de
// AOS-019 que reusa este enteredAt — são comparações de WALL-CLOCK e assumem um
// relógio SEM SALTOS: um ajuste NTP para trás ADIA o kill fail-closed, um para a
// frente antecipa-o. Os dois executores degradam da MESMA forma (concordam entre si),
// mas o par não é robusto a saltos de relógio. Se essa robustez for requisito, é
// necessário preservar a leitura monotónica (fora do âmbito de AOS-019).
func (m *Machine) EnteredAt() time.Time {
	m.estadoMu.RLock()
	defer m.estadoMu.RUnlock()
	return m.enteredAt
}

// HumanApprovalTTL devolve o TTL do gate humano configurado ([WithHumanApprovalTTL]);
// 0 significa que o fail-closed automático de waiting_on_human → killed está DESLIGADO.
// É write-once (fixado na construção), logo seguro para leitura sem lock. Exposto para
// que o WaitingGate de AOS-019 seja DERIVADO da MESMA fonte de TTL (ver
// liveness.NewWaitingGateFrom), eliminando por construção o drift entre o veredicto
// GateExpired do classificador e o kill real de [Machine.CheckDeadlines].
func (m *Machine) HumanApprovalTTL() time.Duration { return m.humanTTL }

// Clock devolve o relógio injectado da máquina (write-once na construção, seguro sem
// lock). Exposto para que consumidores — em particular o WaitingGate de AOS-019 — usem
// o MESMO relógio que decide os deadlines duráveis, garantindo que a decisão do gate e
// a transição fail-closed concordam no tempo (sem duas fontes de "agora" a divergir).
func (m *Machine) Clock() Clock { return m.clock }

// parseStateStepID extrai o N de um step_id "state-N" (o namespacing posicional das
// transições). Devolve (0, false) para step_ids sem o prefixo ou com sufixo não
// numérico — permitindo que o Rebuild recaia numa contagem posicional em logs legados.
func parseStateStepID(stepID string) (uint64, bool) {
	if !strings.HasPrefix(stepID, transitionStepPrefix) {
		return 0, false
	}
	n, err := strconv.ParseUint(stepID[len(transitionStepPrefix):], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// Rebuild reconstrói o estado corrente RELENDO os eventos de transição do run do
// Event Store: o estado é o To do evento de transição com seq mais alto (o log é
// append-only e ordenado). Um stream inexistente ou sem transições ⇒ [Ready] (o
// estado inicial). É a materialização de "o estado corrente é reconstruível por
// replay" e o que faz a máquina SOBREVIVER A CRASH: um worker novo constrói uma
// Machine sobre o mesmo cluster, chama Rebuild e continua de onde o anterior parou.
//
// Fail-closed: um evento de transição cujo To não seja um dos dez estados canónicos
// (log corrompido/schema incompatível) aborta com [ErrUnknownState] em vez de
// adoptar um estado desconhecido.
func (m *Machine) Rebuild(ctx context.Context) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	events, err := m.store.Read(ctx, m.runID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			m.instalarEstado(Ready, m.clock.Now(), 0)
			return Ready, nil
		}
		return "", err
	}

	var (
		count uint64
		maxN  uint64
		last  *transitionRecord
		prev  = Ready // estado inicial implícito; a 1ª transição parte de ready.
	)
	for i := range events {
		if events[i].Type != EventTypeTransition {
			continue
		}
		var rec transitionRecord
		if err := json.Unmarshal(events[i].Payload, &rec); err != nil {
			return "", fmt.Errorf("state: rebuild descodifica transição seq=%d: %w", events[i].Seq, err)
		}
		if !IsKnown(rec.To) {
			return "", fmt.Errorf("%w: %q (seq=%d)", ErrUnknownState, rec.To, events[i].Seq)
		}
		// Continuidade da cadeia: o From de cada transição tem de bater o To da
		// anterior (ou ready na primeira). Uma quebra (log bifurcado/com furos/dois
		// escritores) aborta fail-closed em vez de adoptar o último To — coerente com
		// o fail-closed já existente contra estados desconhecidos.
		if rec.From != prev {
			return "", fmt.Errorf("%w: seq=%d from=%q, estado anterior=%q", ErrCorruptChain, events[i].Seq, rec.From, prev)
		}
		prev = rec.To
		count++
		// Deriva o próximo step_id do MAIOR N observado nos step_ids "state-N", não de
		// uma contagem posicional: um furo (state-1, state-3) faria a contagem colidir
		// com um step_id existente e disparar o dedup silencioso do ES.
		if n, ok := parseStateStepID(events[i].StepID); ok && n > maxN {
			maxN = n
		}
		r := rec // cópia estável para o ponteiro
		last = &r
	}

	if last == nil {
		// Stream existe mas sem transições de estado (p.ex. só turn.recorded).
		m.instalarEstado(Ready, m.clock.Now(), 0)
		return Ready, nil
	}

	// nStates é o piso para o próximo step_id (state-{nStates+1}). Usa o maior N dos
	// step_ids quando disponível; recai na contagem posicional para logs legados sem
	// o prefixo "state-".
	nStates := count
	if maxN > nStates {
		nStates = maxN
	}
	enteredAt, perr := time.Parse(time.RFC3339Nano, last.At)
	if perr != nil {
		enteredAt = m.clock.Now()
	}
	// UMA instalação, no fim: a leitura do stream acima é I/O e corre FORA de estadoMu.
	// Instalar campo a campo durante a reconstrução exporia estados intermédios a quem
	// lê — e a máquina só deve mudar de estado de uma vez.
	m.instalarEstado(last.To, enteredAt, nStates)
	return last.To, nil
}

// Transition tenta transitar para to com os metadados de event. Valida (from → to)
// contra a tabela declarativa, impõe o fencing token quando exigido, e só então
// GRAVA a transição como evento append-only. O estado corrente avança apenas se o
// Append tiver sucesso — uma transição inválida ou uma falha do Event Store não
// corrompe o estado (persistido ou in-memory).
//
// Devolve [ErrInvalidTransition] (par fora da tabela), [ErrMissingFencingToken]
// (claim sem token válido) ou o erro do Event Store (p.ex. [eventstore.ErrNoQuorum]).
func (m *Machine) Transition(ctx context.Context, to State, event TransitionEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.doTransition(ctx, to, event)
}

// doTransition assume o lock detido. Contém a lógica de validação → fencing →
// persistência → avanço, partilhada por [Transition] e [CheckDeadlines].
func (m *Machine) doTransition(ctx context.Context, to State, event TransitionEvent) error {
	// Instantâneo sob estadoMu. `mu` está detido pelo chamador, pelo que estes valores
	// não podem mudar debaixo dos pés: nenhuma outra mutação corre em paralelo.
	from, _, nStates := m.lerEstado()

	// 1) Validação contra a TABELA declarativa. Par inválido ⇒ rejeita sem efeitos.
	if !IsValidTransition(from, to) {
		m.obs.Rejected(from, to, ErrInvalidTransition)
		return ErrInvalidTransition
	}

	// 2) Pré-condição de fencing token (só no claim ready → running).
	var tokenValue uint64
	if RequiresFencingToken(from, to) {
		if event.Token == nil || !event.Token.Valid() {
			m.obs.Rejected(from, to, ErrMissingFencingToken)
			return ErrMissingFencingToken
		}
		tokenValue = event.Token.Value()
		// 2b) Staleness OPCIONAL: se uma [FencingAuthority] estiver ligada, a
		// PRESENÇA do token não basta — um token inferior ao corrente é de um worker
		// superado por um novo claim e é recusado fail-closed ANTES de tocar no log
		// (fecha, no próprio caminho da transição, a lacuna "presença ≠ staleness";
		// sem a autoridade a staleness fica delegada ao FencedAppender de AOS-018).
		if m.fencing != nil {
			current, ferr := m.fencing.CurrentTokenValue(ctx, m.runID)
			if ferr != nil {
				m.obs.Rejected(from, to, ferr)
				return ferr
			}
			if tokenValue < current {
				m.obs.Rejected(from, to, ErrStaleFencingToken)
				return fmt.Errorf("%w: token=%d inferior ao corrente=%d (run %s)",
					ErrStaleFencingToken, tokenValue, current, m.runID)
			}
		}
	} else if event.Token != nil && event.Token.Valid() {
		// Token presente numa transição que não o exige: regista-o na mesma (útil
		// para AOS-018 correlacionar o writer), mas não é condição.
		tokenValue = event.Token.Value()
	}

	// 3) Persistência append-only. O step_id "state-N" é único por run (namespaced),
	// evitando que a dedup global do ES colida com turno/ledger/checkpoint.
	now := m.clock.Now().UTC()
	rec := transitionRecord{
		From:       from,
		To:         to,
		Reason:     event.Reason,
		TokenValue: tokenValue,
		At:         now.Format(time.RFC3339Nano),
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		m.obs.Rejected(from, to, err)
		return err
	}
	stepID := fmt.Sprintf("%s%d", transitionStepPrefix, nStates+1)
	res, err := m.store.Append(ctx, m.runID, eventstore.EventInput{
		Type:     EventTypeTransition,
		Payload:  payload,
		RunID:    m.runID,
		StepID:   stepID,
		Producer: m.producer,
	})
	if err != nil {
		// Fail-closed: o Event Store recusou (perda de quórum, closed, ...). O estado
		// NÃO avança — nem persistido nem in-memory. Sem corrupção.
		m.obs.Rejected(from, to, err)
		return err
	}

	// 3b) Reconciliação fail-closed do dedup. O Event Store devolve erro NIL com
	// StatusDuplicate quando a idempotency_key "state-N" já existia — e IGNORA o
	// payload novo (o duplicado original vence). Se o evento já persistido sob esta
	// chave NÃO for exactamente a transição que pedimos (from→to), avançar o estado
	// in-memory para `to` divergiria silenciosamente do log. Só um StatusCommitted —
	// ou um StatusDuplicate cujo payload bate EXACTAMENTE o pedido (retry benigno) —
	// autoriza mutar o estado; caso contrário recusa-se sem tocar no estado.
	if res.Status != eventstore.StatusCommitted {
		var persisted transitionRecord
		if uerr := json.Unmarshal(res.Event.Payload, &persisted); uerr != nil {
			m.obs.Rejected(from, to, uerr)
			return fmt.Errorf("state: reconcilia duplicado %s (seq=%d): %w", stepID, res.Seq, uerr)
		}
		if persisted.From != from || persisted.To != to {
			m.obs.Rejected(from, to, ErrStateDivergence)
			return fmt.Errorf("%w: %s persistido=%s→%s, pedido=%s→%s (seq=%d)",
				ErrStateDivergence, stepID, persisted.From, persisted.To, from, to, res.Seq)
		}
	}

	// 4) Só APÓS o commit durável (ou o duplicado idêntico reconciliado) é que o
	// estado in-memory avança. É a ÚNICA secção de estadoMu deste caminho, e não faz
	// I/O: todo o I/O — autoridade de fencing, Append — já aconteceu acima, com `mu`
	// detido mas estadoMu livre (AOS-301).
	m.instalarEstado(to, now, nStates+1)

	m.emitSpan(ctx, from, to, rec)
	m.obs.Transitioned(from, to, event.Reason)
	return nil
}

// emitSpan abre e fecha um span de transição na porta [agentruntime.Tracer] reusada
// de AOS-013 (observabilidade das transições — DoD). Sem segredos: só estados e
// rótulos entram nos atributos.
func (m *Machine) emitSpan(ctx context.Context, from, to State, rec transitionRecord) {
	_, span := m.tracer.StartSpan(ctx, agentruntime.OpInvokeAgent)
	span.SetAttribute(agentruntime.AttrRunID, m.runID)
	span.SetAttribute("aos.state.from", string(from))
	span.SetAttribute("aos.state.to", string(to))
	if rec.Reason != "" {
		span.SetAttribute("aos.state.reason", rec.Reason)
	}
	span.End()
}

// CheckDeadlines aplica os TIMEOUTS FAIL-CLOSED com base no relógio injectado, SEM
// sleeps: o Escalonador/lease (AOS-018) chama-a periodicamente. Avalia o estado
// corrente e o tempo desde que foi entrado:
//
//   - waiting_on_human há >= humanTTL  → killed  (fail-closed, ADR-013 — NUNCA running);
//   - running        há >= wallClock  → timed_out.
//
// Devolve o estado resultante, se disparou uma transição, e um erro (só do Event
// Store — a transição de timeout é sempre válida na tabela). Idempotente: se nenhum
// deadline foi excedido, ou se o TTL respectivo está desligado (0), não faz nada.
//
// Relógio: as comparações são WALL-CLOCK (o enteredAt tem a componente monotónica
// removida — ver [Machine.EnteredAt]) e assumem um relógio sem saltos; um ajuste NTP
// desloca o instante do kill fail-closed. É o MESMO critério (fronteira inclusiva) e
// a MESMA degradação do WaitingGate de AOS-019, pelo que gate e Machine concordam.
func (m *Machine) CheckDeadlines(ctx context.Context) (State, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.clock.Now()
	// Instantâneo COERENTE do par (estado, instante de entrada): é sobre ele que se
	// decide um kill fail-closed, e lê-los em dois momentos deixaria uma janela em que
	// o estado é o novo e o instante o antigo.
	current, enteredAt, _ := m.lerEstado()
	switch current {
	case WaitingOnHuman:
		if m.humanTTL > 0 && !now.Before(enteredAt.Add(m.humanTTL)) {
			if err := m.doTransition(ctx, Killed, TransitionEvent{Reason: ReasonHumanTimeout}); err != nil {
				return current, false, err
			}
			return Killed, true, nil
		}
	case Running:
		if m.wallClock > 0 && !now.Before(enteredAt.Add(m.wallClock)) {
			if err := m.doTransition(ctx, TimedOut, TransitionEvent{Reason: ReasonWallClockTimeout}); err != nil {
				return current, false, err
			}
			return TimedOut, true, nil
		}
	}
	return current, false, nil
}

// ---------------------------------------------------------------------------
// Eventos pause/resume/kill EXPOSTOS (accionados por AOS-023 steer / AOS-018 lease).
// São conveniências finas sobre [Transition] com o destino fixo; a validade continua
// governada pela tabela declarativa. AOS-017 apenas os EXPÕE — a lógica de steer e
// de lease é dos respectivos tickets.
// ---------------------------------------------------------------------------

// Pause transita running → paused (steer/interrupt aceite). Recusa fora de running
// com [ErrInvalidTransition].
func (m *Machine) Pause(ctx context.Context, event TransitionEvent) error {
	return m.Transition(ctx, Paused, event)
}

// Resume transita paused → running (retoma sob o lease já detido — NÃO re-exige
// fencing token, ao contrário do claim inicial). Recusa fora de paused.
func (m *Machine) Resume(ctx context.Context, event TransitionEvent) error {
	return m.Transition(ctx, Running, event)
}

// Kill transita waiting_on_human → killed (a única aresta para killed na tabela —
// terminação por política ou timeout fail-closed). Recusa a partir de outros estados
// com [ErrInvalidTransition].
func (m *Machine) Kill(ctx context.Context, event TransitionEvent) error {
	return m.Transition(ctx, Killed, event)
}
