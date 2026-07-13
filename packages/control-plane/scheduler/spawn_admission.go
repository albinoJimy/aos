package scheduler

// spawn_admission.go — AOS-028: max_spawn derivado do headroom, reserva no admit.
//
// O plano-base fazia spawn com um max_spawn CONSTANTE, cego ao estado agregado
// do provider: 15 árvores, cada uma dentro do seu limite local, saturavam
// colectivamente o rate limit partilhado (cascata de 429s). Este ficheiro liga a
// DELEGAÇÃO lógica (AOS-026, orchestrator.Delegator: sub-orçamento hierárquico
// por árvore) ao ADMISSION CONTROL global (AOS-027, Admission: token-bucket
// distribuído sobre o TPM/RPM real). NÃO reimplementa nenhum dos dois — COMPÕE-os
// num [SpawnCoordinator] que impõe a regra da fonte (ADR-008):
//
//	"o escalonador não faz spawn sem débito reservado no token-bucket global;
//	 max_spawn passa a ser derivado dinamicamente do headroom, não uma constante".
//
// Invariantes centrais:
//
//  1. DERIVAÇÃO DINÂMICA. max_spawn = f(headroom_disponível, custo_por_subagente),
//     reavaliada a CADA pedido (nunca uma constante hard-coded). Monótona: mais
//     headroom ⇒ ≥ spawns permitidos ([deriveMaxSpawn]).
//  2. RESERVA NO ADMIT ANTES DO SPAWN. Antes de criar o sub-agente, reserva-se
//     headroom no token-bucket global (Admission.Admit). Sem headroom ⇒ ADIA
//     (spawn_deferred_no_headroom, com retry_after), NÃO cria o sub-agente, NÃO
//     força oversubscription.
//  3. AMBOS OS LIMITES. Só após a reserva de headroom se delega ao Delegator, que
//     reserva o SUB-ORÇAMENTO da árvore (AOS-026). Ambos têm de permitir: se o
//     global concede mas o sub-orçamento nega (ou o inverso), o spawn é RECUSADO e
//     a reserva bem-sucedida é LIBERTADA — sem fuga de duas-fases (o risco central).
//  4. LIBERTAÇÃO IDEMPOTENTE. Ao terminar (sucesso, falha OU timeout) liberta-se o
//     headroom (Admission.Release) e consolida-se o sub-orçamento (Delegator.Finish),
//     de forma idempotente: um segundo Finish é no-op, sem fuga de reservas.
//
// EVENTOS append-only (headroom_reserved / headroom_released /
// spawn_deferred_no_headroom) tornam cada decisão auditável e reconstruível por
// replay. DETERMINISMO: relógio/IDs injectáveis, serialização estável (structs),
// sem time.Now/rand no caminho de decisão. OTel: span com o headroom reservado por
// spawn, reutilizando a porta agentruntime.Tracer zero-dep.
//
// FRONTEIRA DE RECUPERAÇÃO DE CRASH (fora do âmbito do AOS-028, deliberada). O
// ticket ([SpawnTicket]: headroom reservado + Handle do sub-orçamento) vive apenas
// em memória entre RequestSpawn e Finish. Um crash do coordenador nessa janela é
// recuperado de forma ASSIMÉTRICA: a reserva de headroom auto-expira pelo refill
// temporizado do token-bucket (<= Window, AOS-027, foldBucket ignora reservas mais
// velhas do que a janela), mas a reserva de SUB-ORÇAMENTO da árvore (AOS-026) não
// tem expiração temporizada e fica presa até um Commit/Release explícito — sem o
// Handle (perdido no crash) fica órfã. A reconciliação no arranque (reler os streams
// e libertar reservas reserved-sem-released) ou a persistência do ticket para um
// Finish de recuperação pertencem a um ticket posterior (AOS-029+); aqui a fronteira
// é DOCUMENTADA, não resolvida.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aos-ref/control-plane/orchestrator"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// Tipos de evento append-only do coordenador de spawn (AOS-028). São o contrato
// auditável exigido pelo ticket; o replay reconstrói a sequência de reservas de
// headroom por spawn.
const (
	// EventHeadroomReserved — headroom reservado no admit, antes de criar o
	// sub-agente (com o headroom reservado por spawn e o max_spawn derivado).
	EventHeadroomReserved = "spawn.headroom_reserved"
	// EventHeadroomReleased — headroom libertado ao terminar/falhar/timeout, ou no
	// rollback de duas-fases (sub-orçamento negou depois de o global conceder).
	EventHeadroomReleased = "spawn.headroom_released"
	// EventSpawnDeferredNoHeadroom — spawn adiado por falta de headroom global
	// (backpressure, com retry_after); NÃO se cria o sub-agente.
	EventSpawnDeferredNoHeadroom = "spawn.spawn_deferred_no_headroom"
)

// DefaultSpawnCoordinatorNHI é a NHI por omissão do coordenador nos eventos que
// emite.
const DefaultSpawnCoordinatorNHI = "nhi:control-plane/scheduler/spawn-admission"

// spawnAdmissionStreamPrefix é o prefixo do stream (por run) onde o coordenador
// projecta os seus eventos append-only.
const spawnAdmissionStreamPrefix = "spawn-admission/"

// opSpawnAdmission é o nome de operação do span do coordenador de spawn.
const opSpawnAdmission = "spawn_admission"

// Atributos de span (OTel GenAI-style; reutilizam a convenção zero-dep de AOS-027).
const (
	attrSpawnKey            = "aos.spawn.key"
	attrSpawnTenant         = "aos.spawn.tenant"
	attrSpawnCostTokens     = "aos.spawn.cost_tokens"
	attrSpawnHeadroomT      = "aos.spawn.headroom_reserved_tokens"
	attrSpawnHeadroomR      = "aos.spawn.headroom_reserved_requests"
	attrSpawnMaxSpawn       = "aos.spawn.max_spawn"
	attrSpawnAdmitted       = "aos.spawn.admitted"
	attrSpawnDeferred       = "aos.spawn.deferred"
	attrSpawnRetryAfterMs   = "aos.spawn.retry_after_ms"
	attrSpawnTwoPhaseFailed = "aos.spawn.two_phase_rollback"
	attrSpawnChildTaskID    = "aos.spawn.child_task_id"
)

// Sentinelas de erro do coordenador (comparáveis por errors.Is — fail-closed).
var (
	// ErrSpawnDeferredNoHeadroom — não há headroom global para reservar o custo do
	// sub-agente: o spawn é ADIADO (backpressure), NÃO recusado permanentemente. O
	// chamador deve voltar a tentar após [SpawnOutcome.RetryAfter]. NÃO se cria o
	// sub-agente nem se toca no sub-orçamento da árvore.
	ErrSpawnDeferredNoHeadroom = errors.New("scheduler: spawn adiado — sem headroom global (backpressure)")
	// ErrSpawnUnsatisfiable — o custo estimado do sub-agente excede o próprio tecto
	// TPM/RPM: nenhum refill futuro o torna admissível (rejeição permanente do
	// admit). Distingue-se do defer para o chamador não fazer poll eterno.
	ErrSpawnUnsatisfiable = errors.New("scheduler: spawn recusado — custo excede o tecto TPM/RPM (permanente)")
	// ErrSubtreeBudgetDenied — o headroom global concedeu mas o sub-orçamento da
	// árvore (AOS-026) negou o spawn: recusa fail-closed, com o headroom já reservado
	// LIBERTADO (sem fuga de duas-fases). Envolve o erro concreto do Delegator.
	ErrSubtreeBudgetDenied = errors.New("scheduler: spawn recusado — sub-orçamento da árvore negou (headroom libertado)")
	// ErrInvalidSpawnAdmit — pedido de coordenação malformado.
	ErrInvalidSpawnAdmit = errors.New("scheduler: pedido de spawn-admission inválido")
	// ErrSpawnCoordinatorDeps — dependências obrigatórias do coordenador em falta.
	ErrSpawnCoordinatorDeps = errors.New("scheduler: dependências do SpawnCoordinator em falta (headroom/delegator)")
	// ErrNilSpawnTicket — Finish chamado com ticket nil.
	ErrNilSpawnTicket = errors.New("scheduler: SpawnTicket nil")
)

// HeadroomController é o seam do admission control global (AOS-027) de que o
// coordenador precisa: reservar headroom (Admit), libertá-lo (Release) e observar
// o headroom disponível (Headroom, para derivar max_spawn sem reservar).
// *[Admission] satisfá-lo. NÃO é reimplementado aqui — a reserva atómica sem SPOF
// e o refill temporizado são os do AOS-027.
type HeadroomController interface {
	Admit(ctx context.Context, req AdmitRequest) (AdmitResult, error)
	Release(ctx context.Context, key ProviderKey, reservationID string, costTokens, costRequests int64) error
	Headroom(ctx context.Context, key ProviderKey, tenant string) (HeadroomSnapshot, error)
}

// SubtreeSpawner é o seam da delegação hierárquica (AOS-026): reserva o
// sub-orçamento da árvore e cria a identidade NHI filha mediada pelo RM.
// *orchestrator.Delegator satisfá-lo (ver a asserção de compatibilidade abaixo).
// NÃO é reimplementado aqui.
type SubtreeSpawner interface {
	Spawn(ctx context.Context, req orchestrator.SpawnRequest) (*orchestrator.SpawnHandle, error)
	Finish(ctx context.Context, h *orchestrator.SpawnHandle, success bool) error
}

// Asserção de compatibilidade: o Delegator real (AOS-026) satisfaz o seam sem
// qualquer adaptador — a composição do AOS-028 é sobre o contrato público do
// AOS-026, não sobre uma reimplementação.
var _ SubtreeSpawner = (*orchestrator.Delegator)(nil)

// Asserção de compatibilidade: a Admission real (AOS-027) satisfaz o seam do
// controlador de headroom.
var _ HeadroomController = (*Admission)(nil)

// HeadroomSnapshot é o headroom disponível de uma chave num instante (para
// derivar max_spawn). Tokens/Requests é o que RESTA (limite − reservas activas);
// LimitTokens/LimitRequests é o tecto efectivo (global ∩ tenant).
type HeadroomSnapshot struct {
	Tokens        int64
	Requests      int64
	LimitTokens   int64
	LimitRequests int64
}

// Headroom devolve o headroom disponível da chave (opcionalmente restrito ao
// tecto do tenant), reutilizando a projecção determinística [Admission.foldBucket]
// do AOS-027 — a MESMA que decide a admissão. É uma leitura pura (não reserva nem
// muta estado): serve a derivação de max_spawn e a observabilidade, coerente com o
// estado que o Admit veria no mesmo instante. O tecto global DOMINA sempre: mesmo
// com folga na partição do tenant, o headroom reportado nunca excede o global.
func (a *Admission) Headroom(ctx context.Context, key ProviderKey, tenant string) (HeadroomSnapshot, error) {
	glob, err := a.qp.Limits(ctx, key)
	if err != nil {
		return HeadroomSnapshot{}, fmt.Errorf("scheduler: limites globais: %w", err)
	}
	if glob.Window <= 0 {
		return HeadroomSnapshot{}, fmt.Errorf("scheduler: janela de refill inválida (<=0) para %s", key.String())
	}

	var tenantLim ProviderLimits
	var hasTenantCap bool
	if tqp, ok := a.qp.(TenantQuotaProvider); ok && tenant != "" {
		tl, present, terr := tqp.TenantLimits(ctx, key, tenant)
		if terr != nil {
			return HeadroomSnapshot{}, fmt.Errorf("scheduler: limites do tenant: %w", terr)
		}
		tenantLim, hasTenantCap = tl, present
	}

	nowNano := a.now().UnixNano()
	bucketID := bucketStreamPrefix + key.String()
	st, _, err := a.foldBucket(ctx, bucketID, nowNano, glob.Window.Nanoseconds())
	if err != nil {
		return HeadroomSnapshot{}, err
	}

	snap := HeadroomSnapshot{
		Tokens:        clampNonNeg(glob.TPM - st.tokens),
		Requests:      clampNonNeg(glob.RPM - st.requests),
		LimitTokens:   glob.TPM,
		LimitRequests: glob.RPM,
	}
	// O global domina: o headroom efectivo é o mínimo entre o global e o do tenant.
	if hasTenantCap {
		tTokens := clampNonNeg(tenantLim.TPM - st.tenantTokens[tenant])
		tReq := clampNonNeg(tenantLim.RPM - st.tenantRequests[tenant])
		if tTokens < snap.Tokens {
			snap.Tokens = tTokens
		}
		if tReq < snap.Requests {
			snap.Requests = tReq
		}
		if tenantLim.TPM < snap.LimitTokens {
			snap.LimitTokens = tenantLim.TPM
		}
		if tenantLim.RPM < snap.LimitRequests {
			snap.LimitRequests = tenantLim.RPM
		}
	}
	return snap, nil
}

// clampNonNeg limita um valor a >= 0 (headroom nunca é negativo).
func clampNonNeg(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// deriveMaxSpawn é a FÓRMULA de derivação de max_spawn a partir do headroom e do
// custo estimado por sub-agente. É PURA e DETERMINÍSTICA (sem relógio nem estado):
//
//	max_spawn = min( headroom_tokens / custo_tokens , headroom_requests / 1 )
//
// Cada sub-agente consome custo_tokens do TPM e 1 request do RPM (coerente com o
// reqCost=1 do Admit em AOS-027). Propriedades verificadas em teste:
//   - NÃO é constante: varia com o headroom (é 0 sob headroom nulo);
//   - MONÓTONA não-decrescente no headroom: h1 <= h2 ⇒ maxSpawn(h1) <= maxSpawn(h2);
//   - custo <= 0 é normalizado para 1 (evita divisão por zero / spawn ilimitado).
func deriveMaxSpawn(headroomTokens, headroomRequests, costTokens int64) int {
	if costTokens < 1 {
		costTokens = 1
	}
	headroomTokens = clampNonNeg(headroomTokens)
	headroomRequests = clampNonNeg(headroomRequests)

	byTokens := headroomTokens / costTokens
	byRequests := headroomRequests // custo em requests por sub-agente = 1
	m := byTokens
	if byRequests < m {
		m = byRequests
	}
	if m < 0 {
		m = 0
	}
	return int(m)
}

// SpawnCoordinator compõe o admission control global (AOS-027) com a delegação
// hierárquica (AOS-026) para impor a reserva de headroom no admit antes do spawn e
// a derivação dinâmica de max_spawn. Construir com [NewSpawnCoordinator]. É seguro
// para uso concorrente na medida em que os colaboradores o são (a Admission é
// stateless sobre o Event Store; o Delegator é thread-safe); o estado do
// coordenador é imutável após a construção.
type SpawnCoordinator struct {
	headroom  HeadroomController
	delegator SubtreeSpawner

	log      EventLog // opcional: projecção durável dos eventos de spawn-admission
	producer eventstore.Producer
	tracer   agentruntime.Tracer
	now      func() time.Time
	idGen    func() string
	// metrics é a fachada OPCIONAL de métricas (AOS-034). Nil por omissão: sem
	// WithSpawnMeter o coordenador é bit-a-bit o do AOS-028 (aditivo, nil-safe).
	// Conta os spawns adiados por falta de headroom.
	metrics *SchedulerMetrics
}

// SpawnCoordinatorOption configura o [SpawnCoordinator].
type SpawnCoordinatorOption func(*SpawnCoordinator)

// WithSpawnEventLog liga a projecção durável dos eventos de spawn-admission ao
// Event Store, com a identidade emissora (NHI).
func WithSpawnEventLog(log EventLog, producer eventstore.Producer) SpawnCoordinatorOption {
	return func(c *SpawnCoordinator) {
		c.log = log
		if producer.NHIID != "" {
			c.producer = producer
		}
	}
}

// WithSpawnClock injecta o relógio de decisão (determinismo/replay: sem time.Now
// no caminho de decisão).
func WithSpawnClock(now func() time.Time) SpawnCoordinatorOption {
	return func(c *SpawnCoordinator) {
		if now != nil {
			c.now = now
		}
	}
}

// WithSpawnIDGen injecta o gerador de request IDs da reserva de headroom
// (determinismo/replay). Sem ele, herda o RequestID do pedido ou o do Admit.
func WithSpawnIDGen(gen func() string) SpawnCoordinatorOption {
	return func(c *SpawnCoordinator) {
		if gen != nil {
			c.idGen = gen
		}
	}
}

// WithSpawnTracer injecta a porta OTel (span com o headroom reservado por spawn).
// Zero-dep (reutiliza agentruntime.Tracer).
func WithSpawnTracer(t agentruntime.Tracer) SpawnCoordinatorOption {
	return func(c *SpawnCoordinator) {
		if t != nil {
			c.tracer = t
		}
	}
}

// WithSpawnMeter ACOPLA o coordenador a uma [Meter] (AOS-034). É ADITIVO: conta os
// spawns ADIADOS por falta de headroom ([MetricSpawnDeferred]). Sem esta opção, é
// bit-a-bit o do AOS-028 (nenhum teste do AOS-028 muda). Meter nil é ignorado.
func WithSpawnMeter(m Meter) SpawnCoordinatorOption {
	return func(c *SpawnCoordinator) {
		if m != nil {
			c.metrics = NewSchedulerMetrics(m)
		}
	}
}

// NewSpawnCoordinator constrói o coordenador. headroom (admission control global,
// AOS-027) e delegator (delegação hierárquica, AOS-026) são OBRIGATÓRIOS — a sua
// ausência é fail-closed.
func NewSpawnCoordinator(headroom HeadroomController, delegator SubtreeSpawner, opts ...SpawnCoordinatorOption) (*SpawnCoordinator, error) {
	if headroom == nil || delegator == nil {
		return nil, ErrSpawnCoordinatorDeps
	}
	c := &SpawnCoordinator{
		headroom:  headroom,
		delegator: delegator,
		producer:  eventstore.Producer{NHIID: DefaultSpawnCoordinatorNHI},
		tracer:    agentruntime.NoopTracer{},
		now:       time.Now,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// SpawnAdmitRequest é o pedido de spawn coordenado: reservar headroom no admit
// global e, se concedido, delegar o sub-agente com o seu sub-orçamento de árvore.
type SpawnAdmitRequest struct {
	// Key identifica o bucket global (provider:model:region) onde se reserva
	// headroom para o sub-agente.
	Key ProviderKey
	// Tenant é a dimensão de partição da quota (opcional; o global domina sempre).
	Tenant string
	// EstimatedTokens é o custo estimado por sub-agente (>0). Deriva max_spawn e é o
	// débito reservado no token-bucket global. Se <=0, cai para 1.
	EstimatedTokens int64
	// RequestID torna a reserva de headroom idempotente ENTRE chamadas: dois
	// RequestSpawn com o MESMO RequestID (retry at-least-once do mesmo spawn lógico)
	// convergem na MESMA reserva, sem orfanar a anterior. É o input ROBUSTO para essa
	// idempotência — um idGen injectado gera um id NOVO por chamada e NÃO a garante
	// entre chamadas; nem o id gerado pelo próprio Admit (único por invocação). Se
	// vazio: usa o idGen injectado se houver; senão o reqID é DERIVADO
	// determinísticamente de RunID+ChildTaskID (rede de segurança para retries
	// convergirem por omissão). Fornecer um RequestID estável por spawn lógico
	// continua a ser o modo recomendado quando o par (RunID, ChildTaskID) não é
	// estável entre retries.
	RequestID string
	// Spawn é o pedido de delegação (AOS-026) executado APÓS a reserva de headroom.
	Spawn orchestrator.SpawnRequest
}

// SpawnTicket é o comprovativo de um spawn admitido (headroom reservado + reserva
// do sub-orçamento). Consumido por [SpawnCoordinator.Finish] para libertar o
// headroom e consolidar o sub-orçamento. É seguro para Finish concorrente: a
// primeira consolidação vence e as restantes são no-op (sem fuga de reservas).
type SpawnTicket struct {
	Key                   ProviderKey
	Tenant                string
	RunID                 string
	ChildTaskID           string
	HeadroomReservationID string
	CostTokens            int64
	CostRequests          int64
	// MaxSpawnAtAdmit é o max_spawn derivado do headroom observado no admit.
	MaxSpawnAtAdmit int
	// Handle é o comprovativo do sub-orçamento (AOS-026) a consolidar no Finish.
	Handle *orchestrator.SpawnHandle

	// mu serializa Finish do MESMO ticket, tornando a consolidação (Delegator.Finish
	// + libertação de headroom) ATÓMICA por ticket. finished (lido/escrito sob mu)
	// garante a idempotência: a primeira consolidação COMPLETA vence e marca feito;
	// as restantes são no-op. Serializar (em vez de um guard atómico revertível)
	// elimina o TOCTOU em que um Finish concorrente observaria finished==true na
	// janela antes de um revert e devolveria falso-sucesso sem efeitos consolidados.
	mu       sync.Mutex
	finished bool
}

// SpawnOutcome é o veredicto de [SpawnCoordinator.RequestSpawn].
type SpawnOutcome struct {
	// Admitted indica que ambos os limites concederam e o sub-agente foi criado;
	// Ticket está preenchido.
	Admitted bool
	// Deferred indica um adiamento por falta de headroom global (backpressure): o
	// sub-agente NÃO foi criado, RetryAfter aconselha quando voltar a tentar. Não é
	// um erro — é a resposta correcta a headroom nulo (nunca oversubscription).
	Deferred bool
	// RetryAfter, quando Deferred, é o tempo aconselhado até haver headroom.
	RetryAfter time.Duration
	// MaxSpawn é o max_spawn derivado do headroom observado neste pedido.
	MaxSpawn int
	// HeadroomTokens/Requests é o headroom global observado na decisão.
	HeadroomTokens   int64
	HeadroomRequests int64
	// Ticket, quando Admitted, é o comprovativo a consolidar com Finish.
	Ticket *SpawnTicket
}

// MaxSpawn devolve o max_spawn efectivo derivado do headroom CORRENTE da chave
// (reavaliado a cada chamada — nunca uma constante), para o custo estimado por
// sub-agente. É uma leitura pura (não reserva): serve a observabilidade e o
// planeamento. costPerSubagentTokens <=0 cai para 1.
func (c *SpawnCoordinator) MaxSpawn(ctx context.Context, key ProviderKey, tenant string, costPerSubagentTokens int64) (int, error) {
	snap, err := c.headroom.Headroom(ctx, key, tenant)
	if err != nil {
		return 0, err
	}
	return deriveMaxSpawn(snap.Tokens, snap.Requests, costPerSubagentTokens), nil
}

// RequestSpawn coordena um spawn com reserva de headroom no admit ANTES de criar o
// sub-agente. Fluxo (fail-closed em cada degrau):
//
//  1. RESERVA DE HEADROOM (AOS-027): Admit(cost) no token-bucket global. Sem
//     headroom ⇒ ADIA (spawn_deferred_no_headroom, retry_after), devolve
//     Deferred=true SEM criar o sub-agente nem tocar no sub-orçamento (nunca
//     oversubscription). Custo > tecto ⇒ rejeição permanente (ErrSpawnUnsatisfiable).
//  2. headroom_reserved (evento + span com o headroom reservado por spawn).
//  3. DELEGAÇÃO (AOS-026): Delegator.Spawn reserva o SUB-ORÇAMENTO da árvore. Se
//     NEGAR (ou erro), o headroom já reservado é LIBERTADO (headroom_released) e o
//     spawn é recusado (ErrSubtreeBudgetDenied) — sem fuga de duas-fases.
//  4. Devolve um [SpawnTicket] a consolidar com [SpawnCoordinator.Finish].
//
// max_spawn é derivado do headroom OBSERVADO neste pedido ([deriveMaxSpawn]),
// reavaliado a cada chamada — nunca uma constante.
func (c *SpawnCoordinator) RequestSpawn(ctx context.Context, req SpawnAdmitRequest) (SpawnOutcome, error) {
	if err := ctx.Err(); err != nil {
		return SpawnOutcome{}, err
	}
	if req.Spawn.RunID == "" || req.Spawn.ChildBudgetNode == "" || req.Spawn.Child.AgentID == "" {
		return SpawnOutcome{}, ErrInvalidSpawnAdmit
	}
	cost := req.EstimatedTokens
	if cost < 1 {
		cost = 1
	}
	childTask := req.Spawn.ChildTaskID
	if childTask == "" {
		childTask = req.Spawn.Child.AgentID
	}

	ctx, span := c.tracer.StartSpan(ctx, opSpawnAdmission)
	defer span.End()
	span.SetAttribute(attrSpawnKey, req.Key.String())
	span.SetAttribute(attrSpawnTenant, req.Tenant)
	span.SetAttribute(attrSpawnCostTokens, cost)
	span.SetAttribute(attrSpawnChildTaskID, childTask)

	// 1) Reserva de headroom no token-bucket global (AOS-027), ANTES de qualquer
	// efeito de spawn. O RequestID torna-a idempotente.
	reqID := req.RequestID
	if reqID == "" && c.idGen != nil {
		reqID = c.idGen()
	}
	if reqID == "" {
		// Sem RequestID explícito nem idGen injectado: deriva-se determinísticamente
		// de campos ESTÁVEIS por spawn (RunID+ChildTaskID) para que um retry
		// at-least-once de RequestSpawn convirja na MESMA reserva de headroom (a
		// Admission é idempotente por RequestID sobre a reserva ACTIVA) em vez de
		// criar uma segunda e orfanar a primeira. Retry-idempotência robusta continua
		// a exigir um req.RequestID estável quando (RunID, ChildTaskID) não o é.
		reqID = "spawn:" + req.Spawn.RunID + ":" + childTask
	}
	admit, err := c.headroom.Admit(ctx, AdmitRequest{
		Key:             req.Key,
		Tenant:          req.Tenant,
		Board:           req.Spawn.RunID,
		EstimatedTokens: cost,
		RequestID:       reqID,
	})
	if err != nil {
		return SpawnOutcome{}, fmt.Errorf("scheduler: reservar headroom: %w", err)
	}

	// max_spawn derivado do headroom OBSERVADO no admit (reavaliado a cada pedido).
	maxSpawn := deriveMaxSpawn(admit.HeadroomTokens, admit.HeadroomRequests, cost)
	span.SetAttribute(attrSpawnHeadroomT, admit.HeadroomTokens)
	span.SetAttribute(attrSpawnHeadroomR, admit.HeadroomRequests)
	span.SetAttribute(attrSpawnMaxSpawn, maxSpawn)

	if !admit.Granted {
		// Sem headroom: ADIA (backpressure), NÃO cria o sub-agente. Rejeição
		// permanente (custo > tecto) distingue-se do defer transitório.
		span.SetAttribute(attrSpawnAdmitted, false)
		span.SetAttribute(attrSpawnDeferred, true)
		span.SetAttribute(attrSpawnRetryAfterMs, admit.RetryAfter.Milliseconds())
		c.emit(ctx, req.Spawn.RunID, EventSpawnDeferredNoHeadroom, stepSpawnDeferred(childTask, admit.ReservationID), spawnAdmissionPayload{
			Type:             EventSpawnDeferredNoHeadroom,
			RunID:            req.Spawn.RunID,
			ChildTaskID:      childTask,
			Key:              req.Key.String(),
			Tenant:           req.Tenant,
			ReservationID:    admit.ReservationID,
			CostTokens:       cost,
			CostRequests:     1,
			HeadroomTokens:   admit.HeadroomTokens,
			HeadroomRequests: admit.HeadroomRequests,
			MaxSpawn:         maxSpawn,
			RetryAfterMs:     admit.RetryAfter.Milliseconds(),
			TSUnixNano:       c.now().UnixNano(),
		})
		// Custo em micro-USD do sub-orçamento reservado — a dimensão $ é conhecida
		// AQUI (ao contrário do admission em tokens), pelo que a emitimos como
		// atributo estável (ADR-010, critério "custo em USD onde aplicável").
		c.metrics.RecordSpawnDeferred(ctx, req.Key, req.Tenant,
			Attr{Key: AttrMetricCostMicroUSD, Value: req.Spawn.SpawnReserve.CostMicroUSD})
		out := SpawnOutcome{
			Deferred:         true,
			RetryAfter:       admit.RetryAfter,
			MaxSpawn:         maxSpawn,
			HeadroomTokens:   admit.HeadroomTokens,
			HeadroomRequests: admit.HeadroomRequests,
		}
		if admit.Rejected {
			return out, fmt.Errorf("%w: %s custo=%d", ErrSpawnUnsatisfiable, req.Key.String(), cost)
		}
		return out, ErrSpawnDeferredNoHeadroom
	}

	// 2) Headroom reservado: evento append-only com o headroom reservado por spawn.
	span.SetAttribute(attrSpawnAdmitted, true)
	c.emit(ctx, req.Spawn.RunID, EventHeadroomReserved, stepHeadroomReserved(childTask, admit.ReservationID), spawnAdmissionPayload{
		Type:             EventHeadroomReserved,
		RunID:            req.Spawn.RunID,
		ChildTaskID:      childTask,
		Key:              req.Key.String(),
		Tenant:           req.Tenant,
		ReservationID:    admit.ReservationID,
		CostTokens:       cost,
		CostRequests:     1,
		HeadroomTokens:   admit.HeadroomTokens,
		HeadroomRequests: admit.HeadroomRequests,
		MaxSpawn:         maxSpawn,
		TSUnixNano:       c.now().UnixNano(),
	})

	// 3) Delegação: reserva o SUB-ORÇAMENTO da árvore (AOS-026). AMBOS os limites
	// têm de conceder. Se o sub-orçamento negar, LIBERTA o headroom já reservado —
	// sem fuga de duas-fases.
	handle, spErr := c.delegator.Spawn(ctx, req.Spawn)
	if spErr != nil {
		span.SetAttribute(attrSpawnTwoPhaseFailed, true)
		// Liberta o headroom já reservado. Best-effort NESTE caminho: a causa primária
		// da recusa é o sub-orçamento (o retorno domina); nenhum guard fica consumido
		// (o chamador não recebe ticket), pelo que uma libertação falhada apenas deixa
		// a reserva expirar pela Window — sem o retry-hole do caminho de Finish.
		_ = c.releaseHeadroom(ctx, req.Key, req.Spawn.RunID, childTask, admit.ReservationID, cost, "subtree_denied")
		return SpawnOutcome{
			MaxSpawn:         maxSpawn,
			HeadroomTokens:   admit.HeadroomTokens,
			HeadroomRequests: admit.HeadroomRequests,
		}, fmt.Errorf("%w: %v", ErrSubtreeBudgetDenied, spErr)
	}

	return SpawnOutcome{
		Admitted:         true,
		MaxSpawn:         maxSpawn,
		HeadroomTokens:   admit.HeadroomTokens,
		HeadroomRequests: admit.HeadroomRequests,
		Ticket: &SpawnTicket{
			Key:                   req.Key,
			Tenant:                req.Tenant,
			RunID:                 req.Spawn.RunID,
			ChildTaskID:           childTask,
			HeadroomReservationID: admit.ReservationID,
			CostTokens:            cost,
			CostRequests:          1,
			MaxSpawnAtAdmit:       maxSpawn,
			Handle:                handle,
		},
	}, nil
}

// Finish consolida um spawn admitido ao terminar (sucesso, falha OU timeout), de
// forma IDEMPOTENTE:
//
//   - consolida o sub-orçamento da árvore (Delegator.Finish: Commit em sucesso,
//     Release em falha/timeout — AOS-026, idempotente por reservation.ID);
//   - LIBERTA o headroom global reservado (Admission.Release — AOS-027, idempotente
//     por step_id no Event Store);
//   - emite headroom_released.
//
// Um segundo Finish sobre o MESMO ticket é no-op (Finish serializa por ticket e a
// primeira consolidação completa marca feito), pelo que terminar/falhar/timeout com
// retries — mesmo CONCORRENTES — NÃO causa fuga de reservas, dupla contagem nem
// falso-sucesso. success=false cobre falha e timeout (liberta sem consolidar
// consumo).
func (c *SpawnCoordinator) Finish(ctx context.Context, t *SpawnTicket, success bool) error {
	if t == nil {
		return ErrNilSpawnTicket
	}
	// Serializa Finish do MESMO ticket: a consolidação (sub-orçamento + headroom) é
	// atómica por ticket. Um Finish concorrente BLOQUEIA até o anterior terminar e só
	// então observa o estado final — feito ⇒ no-op; incompleto (erro) ⇒ retry — nunca
	// um estado meio-feito nem um falso-sucesso (elimina o TOCTOU de reverter um guard
	// atómico partilhado).
	t.mu.Lock()
	defer t.mu.Unlock()
	// Idempotência: só a primeira consolidação COMPLETA marca feito; as restantes são
	// no-op (sem re-libertar headroom nem re-consolidar o sub-orçamento).
	if t.finished {
		return nil
	}
	// Consolida o sub-orçamento da árvore (AOS-026) — Commit/Release idempotente. Em
	// erro, mantém finished=false para um retry voltar a tentar, sem deixar o headroom
	// por libertar (estado meio-feito).
	if t.Handle != nil {
		if err := c.delegator.Finish(ctx, t.Handle, success); err != nil {
			return fmt.Errorf("scheduler: consolidar sub-orçamento: %w", err)
		}
	}
	// Liberta o headroom global (AOS-027) — idempotente por step_id. Só marca o ticket
	// como consolidado se a libertação teve SUCESSO; se o Release falhar, PROPAGA o
	// erro e mantém finished=false para um retry reconciliar a reserva, em vez de a
	// perder silenciosamente sob um guard já consumido.
	if err := c.releaseHeadroom(ctx, t.Key, t.RunID, t.ChildTaskID, t.HeadroomReservationID, t.CostTokens, spawnFinishReason(success)); err != nil {
		return fmt.Errorf("scheduler: libertar headroom: %w", err)
	}
	t.finished = true
	return nil
}

// releaseHeadroom liberta a reserva de headroom no token-bucket global e projecta
// headroom_released. É idempotente (Admission.Release deduplica por step_id; o
// evento deduplica por (run_id, step_id) que codifica o motivo).
func (c *SpawnCoordinator) releaseHeadroom(ctx context.Context, key ProviderKey, runID, childTask, reservationID string, costTokens int64, reason string) error {
	// Devolve o custo total reservado (libertação total da reserva de headroom). O
	// erro é PROPAGADO (não descartado): uma falha de Release deixa a reserva por
	// reconciliar e o chamador (Finish) mantém o ticket por consolidar para um retry.
	// Só se projecta headroom_released depois de o Release ter tido sucesso, para o
	// evento não afirmar uma libertação que não aconteceu.
	if err := c.headroom.Release(ctx, key, reservationID, costTokens, 1); err != nil {
		return err
	}
	c.emit(ctx, runID, EventHeadroomReleased, stepHeadroomReleased(childTask, reservationID, reason), spawnAdmissionPayload{
		Type:          EventHeadroomReleased,
		RunID:         runID,
		ChildTaskID:   childTask,
		Key:           key.String(),
		ReservationID: reservationID,
		CostTokens:    costTokens,
		CostRequests:  1,
		Reason:        reason,
		TSUnixNano:    c.now().UnixNano(),
	})
	return nil
}

// spawnFinishReason mapeia o resultado do Finish para um motivo estável e legível.
func spawnFinishReason(success bool) string {
	if success {
		return "completed"
	}
	return "failed_or_timeout"
}

// spawnAdmissionPayload é o corpo serializado (estável, sem mapas) de cada evento
// do coordenador — determinismo/replay.
type spawnAdmissionPayload struct {
	Type             string `json:"type"`
	RunID            string `json:"run_id"`
	ChildTaskID      string `json:"child_task_id"`
	Key              string `json:"key"`
	Tenant           string `json:"tenant,omitempty"`
	ReservationID    string `json:"reservation_id"`
	CostTokens       int64  `json:"cost_tokens"`
	CostRequests     int64  `json:"cost_requests"`
	HeadroomTokens   int64  `json:"headroom_tokens,omitempty"`
	HeadroomRequests int64  `json:"headroom_requests,omitempty"`
	MaxSpawn         int    `json:"max_spawn,omitempty"`
	RetryAfterMs     int64  `json:"retry_after_ms,omitempty"`
	Reason           string `json:"reason,omitempty"`
	TSUnixNano       int64  `json:"ts_unix_nano"`
}

// Step* derivam step_ids DETERMINÍSTICOS e DISTINTOS por facto, para que a
// idempotency_key (run_id:step_id) seja única e o replay deduplique retries. O
// reservation_id (por spawn) e o motivo (na libertação) distinguem factos
// distintos do mesmo sub-agente.
func stepHeadroomReserved(childTask, reservationID string) string {
	return "hr-res:" + childTask + ":" + reservationID
}
func stepHeadroomReleased(childTask, reservationID, reason string) string {
	return "hr-rel:" + childTask + ":" + reservationID + ":" + reason
}
func stepSpawnDeferred(childTask, reservationID string) string {
	return "hr-defer:" + childTask + ":" + reservationID
}

// emit projecta um evento do coordenador no Event Store (stream por run). É
// best-effort e no-op sem log (caminho puramente in-memory): a contabilidade
// AUTORITATIVA vive no admission control (reservas no bucket) e no budget da
// árvore; estes eventos são a projecção auditável das decisões de spawn-admission.
func (c *SpawnCoordinator) emit(ctx context.Context, runID, evType, stepID string, payload spawnAdmissionPayload) {
	if c.log == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	streamID := spawnAdmissionStreamPrefix + runID
	_, _ = c.log.Append(ctx, streamID, eventstore.EventInput{
		Type:     evType,
		Payload:  raw,
		RunID:    streamID,
		StepID:   stepID,
		Producer: c.producer,
	})
}

// SpawnAdmissionRecord é uma decisão de spawn-admission reconstruída do log.
type SpawnAdmissionRecord struct {
	Type          string
	RunID         string
	ChildTaskID   string
	ReservationID string
	CostTokens    int64
	MaxSpawn      int
	RetryAfterMs  int64
	Reason        string
	Seq           uint64
}

// ReplaySpawnAdmission reconstrói fielmente a sequência de decisões de
// spawn-admission de um run (headroom_reserved / headroom_released /
// spawn_deferred_no_headroom), por ordem de seq. Prova de que a sequência se
// reconstrói do Event Store (determinismo/ADR-001).
func (c *SpawnCoordinator) ReplaySpawnAdmission(ctx context.Context, runID string) ([]SpawnAdmissionRecord, error) {
	if c.log == nil {
		return nil, nil
	}
	streamID := spawnAdmissionStreamPrefix + runID
	evs, err := c.log.Read(ctx, streamID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]SpawnAdmissionRecord, 0, len(evs))
	for _, ev := range evs {
		var pl spawnAdmissionPayload
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			return nil, fmt.Errorf("scheduler: payload de spawn-admission corrompido no seq %d: %w", ev.Seq, err)
		}
		out = append(out, SpawnAdmissionRecord{
			Type:          pl.Type,
			RunID:         pl.RunID,
			ChildTaskID:   pl.ChildTaskID,
			ReservationID: pl.ReservationID,
			CostTokens:    pl.CostTokens,
			MaxSpawn:      pl.MaxSpawn,
			RetryAfterMs:  pl.RetryAfterMs,
			Reason:        pl.Reason,
			Seq:           ev.Seq,
		})
	}
	return out, nil
}
