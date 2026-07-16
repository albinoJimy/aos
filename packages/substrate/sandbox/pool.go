package sandbox

import (
	"context"
	"sync"
	"time"
)

// DefaultWaitDeadline é o deadline default (não-nulo) da política [PolicyWait]: a
// espera por uma VM limpa é SEMPRE limitada e observável (o DoD exige que o WAIT
// tenha deadline). Um construtor de PolicyWait sem [WithWaitDeadline] herda este
// tecto — a espera nunca bloqueia indefinidamente à custa apenas do cancelamento do
// ctx. Sobreponível com [WithWaitDeadline].
const DefaultWaitDeadline = 30 * time.Second

// ExhaustionPolicy é a política de degradação EXPLÍCITA quando o pool não tem uma
// VM pré-aquecida disponível (AOS-065). É uma ESCOLHA declarada e observável —
// NUNCA se serve uma VM suja/reutilizada, seja qual for a política.
type ExhaustionPolicy int

const (
	// PolicyReject recusa fail-closed ([ErrPoolExhausted]) — a degradação mais
	// conservadora: sob esgotamento, o pedido é rejeitado em vez de esperar ou
	// crescer. É o default (fail-closed).
	PolicyReject ExhaustionPolicy = iota
	// PolicyWait bloqueia até uma VM limpa ser reposta (com deadline configurável).
	// Se o deadline expira, degrada para rejeição fail-closed.
	PolicyWait
	// PolicyExpand cria uma VM nova (restore fresco do snapshot) até um tecto
	// (maxSize). Atingido o tecto, degrada para rejeição fail-closed.
	PolicyExpand
)

// String devolve o nome estável da política (métrica/observabilidade).
func (p ExhaustionPolicy) String() string {
	switch p {
	case PolicyWait:
		return "wait"
	case PolicyExpand:
		return "expand"
	default:
		return "reject"
	}
}

// ProvisionOutcome é o resultado de uma reserva bem-sucedida — o caminho pelo qual
// a VM foi disponibilizada (observável no SLI).
type ProvisionOutcome int

const (
	// OutcomeWarmHit — a reserva foi servida por uma VM pré-aquecida (caminho quente,
	// cold-start ≈ handoff).
	OutcomeWarmHit ProvisionOutcome = iota
	// OutcomeExpanded — o pool estava esgotado e a política EXPAND criou uma VM nova
	// (cold-start = restore, 5–30 ms).
	OutcomeExpanded
	// OutcomeWaited — o pool estava esgotado e a política WAIT bloqueou até uma VM
	// limpa ser reposta (cold-start ≈ restore da reposição).
	OutcomeWaited
)

// String devolve o nome estável do resultado (métrica/observabilidade).
func (o ProvisionOutcome) String() string {
	switch o {
	case OutcomeExpanded:
		return "expanded"
	case OutcomeWaited:
		return "waited"
	default:
		return "warm_hit"
	}
}

// warmVM é uma microVM restaurada (pré-aquecida ou expandida): o overlay efémero
// limpo e a duração de restore modelada com que foi construída.
type warmVM struct {
	overlay *Overlay
	restore time.Duration
}

// Lease é a atribuição de uma VM limpa a UMA execução. Dá acesso ao [Overlay]
// (cópia-em-escrita sobre o snapshot base) e mede o cold-start desta reserva. A
// EXECUÇÃO do efeito NÃO corre pelo Lease — continua mediada pelo Reference Monitor
// ([MediatedLauncher]); o Lease só disponibiliza a sandbox pronta. [Lease.Release]
// descarta o overlay sujo (nunca reciclado) e dispara a reposição warm.
type Lease struct {
	pool      *Pool
	vm        *warmVM
	outcome   ProvisionOutcome
	coldStart time.Duration
	released  bool
	mu        sync.Mutex
}

// Overlay devolve o overlay efémero (limpo) desta atribuição.
func (l *Lease) Overlay() *Overlay { return l.vm.overlay }

// Outcome devolve o caminho pelo qual a VM foi disponibilizada.
func (l *Lease) Outcome() ProvisionOutcome { return l.outcome }

// ColdStart devolve o tempo de disponibilização modelado desta reserva (o SLI).
func (l *Lease) ColdStart() time.Duration { return l.coldStart }

// RestoreDuration devolve a duração de restore modelada da VM entregue (5–30 ms).
func (l *Lease) RestoreDuration() time.Duration { return l.vm.restore }

// Release devolve a VM ao ciclo: DESCARTA o overlay sujo (nunca reciclado para
// outra execução) e dispara a reposição warm até N. Idempotente.
func (l *Lease) Release() {
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return
	}
	l.released = true
	l.mu.Unlock()
	l.pool.release(l)
}

// Pool é o pool de microVMs pré-aquecidas com snapshot/restore (AOS-065). Mantém N
// VMs restauradas do snapshot base imutável prontas a atribuir; a atribuição RESERVA
// uma atomicamente (canal buffered — sem corrida no contador) e, sob esgotamento,
// aplica a política de degradação declarada. Após consumo, REPÕE (reposição warm
// assíncrona) até N. NUNCA serve estado sujo: cada VM é um restore fresco do base e
// o overlay de uma execução é descartado no fim.
//
// A EXECUÇÃO do efeito permanece mediada pelo Reference Monitor (compõe o
// [MediatedLauncher] de AOS-064) — o pool disponibiliza a sandbox, não abre um
// caminho que salte o RM (não expõe Exec).
type Pool struct {
	snap     *Snapshot
	driver   DriverKind
	warmN    int
	maxN     int
	policy   ExhaustionPolicy
	deadline time.Duration
	handoff  time.Duration
	metrics  *ColdStartRecorder
	tracer   Tracer

	// ready é a fila de VMs pré-aquecidas. A recepção do canal é a RESERVA ATÓMICA
	// (sem corrida no contador): N reservas concorrentes recebem N VMs distintas.
	ready chan *warmVM

	mu     sync.Mutex // protege warm/inUse/closed
	warm   int        // VMs limpas na fila ready (contabilidade)
	inUse  int        // VMs reservadas, ainda não libertadas
	closed bool

	replenishMu   sync.Mutex     // serializa a reposição (evita sobre-aprovisionamento)
	wg            sync.WaitGroup // reposições assíncronas em curso (quiescência determinista)
	syncReplenish bool
}

// PoolOption configura o [Pool].
type PoolOption func(*Pool)

// WithMaxSize define o tecto de VMs vivas (warm + em uso) para a política EXPAND.
// Default: igual a warmN (sem expansão). Valores < warmN são elevados a warmN.
func WithMaxSize(n int) PoolOption {
	return func(p *Pool) { p.maxN = n }
}

// WithPolicy define a política de esgotamento. Default [PolicyReject] (fail-closed).
func WithPolicy(pol ExhaustionPolicy) PoolOption {
	return func(p *Pool) { p.policy = pol }
}

// WithWaitDeadline define o deadline da política WAIT. Um valor > 0 fixa o tecto da
// espera. O valor 0 NÃO significa espera indefinida: a construção repõe-o para
// [DefaultWaitDeadline] (ver [NewPool]) — a política WAIT tem SEMPRE um deadline
// observável (requisito do DoD), a espera nunca bloqueia só à mercê do cancelamento
// do ctx. Default (sem esta opção): [DefaultWaitDeadline].
func WithWaitDeadline(d time.Duration) PoolOption {
	return func(p *Pool) {
		if d >= 0 {
			p.deadline = d
		}
	}
}

// WithPoolTracer liga o tracer que abre o span de PROVISÃO ([OpProvisionSandbox]) no
// caminho de reserva, no qual o SLI de cold-start anota cold_start_ms/p95_ms (o
// "custo por span" do DoD). Default [NoopTracer] (spans descartados). O span de
// provisão é distinto do span execute_tool de AOS-064 (que cobre a execução mediada),
// e o nome distingue-se de [WithTracer] (a opção do [Launcher]).
func WithPoolTracer(tr Tracer) PoolOption {
	return func(p *Pool) {
		if tr != nil {
			p.tracer = tr
		}
	}
}

// WithHandoff define o custo modelado do handoff de uma VM pré-aquecida (o
// cold-start de um warm hit). Default: 0 (a VM já está restaurada — o handoff é
// negligenciável). Valores < 0 são ignorados.
func WithHandoff(d time.Duration) PoolOption {
	return func(p *Pool) {
		if d >= 0 {
			p.handoff = d
		}
	}
}

// WithColdStartRecorder liga o SLI de cold-start (cada reserva é observada). Default:
// nenhum (sem observação; o pool funciona na mesma).
func WithColdStartRecorder(rec *ColdStartRecorder) PoolOption {
	return func(p *Pool) { p.metrics = rec }
}

// WithDriverKind regista o driver associado ao pool (eixo do SLI). Default
// [DriverFake]. Metadado apenas — o pool não invoca o driver (a execução é mediada).
func WithDriverKind(k DriverKind) PoolOption {
	return func(p *Pool) {
		if k != "" {
			p.driver = k
		}
	}
}

// WithSynchronousReplenish força a reposição a correr SÍNCRONA (na mesma goroutine
// que liberta/consome). Default: assíncrona (a reposição warm é um trabalho de
// fundo). Útil para testes deterministas; em produção a reposição é assíncrona.
func WithSynchronousReplenish() PoolOption {
	return func(p *Pool) { p.syncReplenish = true }
}

// NewPool constrói e PRÉ-AQUECE o pool: restaura warmN VMs do snapshot base e
// coloca-as na fila. maxN >= warmN (o tecto da expansão). Fail-closed se o snapshot
// for nil ou warmN < 0.
func NewPool(snap *Snapshot, warmN int, opts ...PoolOption) (*Pool, error) {
	if snap == nil {
		return nil, ErrNilSnapshot
	}
	if warmN < 0 {
		return nil, ErrInvalidPoolSize
	}
	p := &Pool{
		snap:   snap,
		driver: DriverFake,
		warmN:  warmN,
		maxN:   warmN,
		policy: PolicyReject,
		tracer: NoopTracer{},
	}
	for _, o := range opts {
		o(p)
	}
	if p.maxN < warmN {
		p.maxN = warmN
	}
	// A política WAIT tem SEMPRE um deadline (DoD): sem um valor explícito, herda o
	// tecto default não-nulo — a espera é limitada e observável, nunca bloqueia só à
	// mercê do cancelamento do ctx (ver [TestPool_WaitDefaultDeadlineBounded]).
	if p.policy == PolicyWait && p.deadline <= 0 {
		p.deadline = DefaultWaitDeadline
	}
	// A fila comporta até maxN VMs (warm + as expandidas devolvidas por Release).
	capacity := p.maxN
	if capacity < 1 {
		capacity = 1
	}
	p.ready = make(chan *warmVM, capacity)
	// PRÉ-AQUECIMENTO: restaura warmN VMs limpas (off-path — antes de qualquer pedido).
	for i := 0; i < warmN; i++ {
		vm, _ := p.restoreVM()
		p.ready <- vm
		p.warm++
	}
	return p, nil
}

// key devolve a chave de SLI do pool (versão de imagem, driver).
func (p *Pool) key() PoolKey {
	return PoolKey{ImageVersion: p.snap.version, Driver: p.driver}
}

// Reserve atribui uma VM LIMPA a uma execução. Caminho quente: recebe uma VM
// pré-aquecida (reserva atómica pelo canal). Esgotado, aplica a política declarada
// (reject/wait/expand). NUNCA devolve uma VM suja. Observa o cold-start no SLI.
func (p *Pool) Reserve(ctx context.Context) (*Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Caminho quente: reserva atómica de uma VM pré-aquecida (não-bloqueante).
	select {
	case vm := <-p.ready:
		p.onConsumed()
		return p.lease(ctx, vm, OutcomeWarmHit, p.handoff), nil
	default:
	}

	// Esgotado: política de degradação EXPLÍCITA.
	switch p.policy {
	case PolicyExpand:
		return p.expand(ctx)
	case PolicyWait:
		return p.wait(ctx)
	default:
		return p.reject(ctx)
	}
}

// reject regista o esgotamento (observável) e recusa fail-closed.
func (p *Pool) reject(ctx context.Context) (*Lease, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil, ErrPoolClosed
	}
	p.observeExhaustion(ctx, PolicyReject)
	return nil, ErrPoolExhausted
}

// expand cria uma VM nova (restore fresco) até ao tecto maxN; atingido o tecto,
// degrada para rejeição fail-closed. O cold-start = duração de restore (5–30 ms).
func (p *Pool) expand(ctx context.Context) (*Lease, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrPoolClosed
	}
	// Antes de expandir, tenta uma última reserva atómica (uma reposição pode ter
	// chegado entre o select e aqui) — evita crescer desnecessariamente.
	select {
	case vm := <-p.ready:
		p.warm--
		p.inUse++
		p.mu.Unlock()
		p.triggerReplenish()
		return p.lease(ctx, vm, OutcomeWarmHit, p.handoff), nil
	default:
	}
	if p.warm+p.inUse >= p.maxN {
		p.mu.Unlock()
		// Tecto atingido: degrada para rejeição fail-closed (observável).
		p.observeExhaustion(ctx, PolicyExpand)
		return nil, ErrPoolExhausted
	}
	p.inUse++
	p.mu.Unlock()
	p.observeExhaustion(ctx, PolicyExpand)
	vm, d := p.restoreVM()
	return p.lease(ctx, vm, OutcomeExpanded, d), nil
}

// wait bloqueia até uma VM limpa ser reposta (ou o deadline/ctx expirar). O
// cold-start ≈ handoff + restore da VM que desbloqueou (modelo determinista).
func (p *Pool) wait(ctx context.Context) (*Lease, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil, ErrPoolClosed
	}
	p.observeExhaustion(ctx, PolicyWait)

	var timeout <-chan time.Time
	if p.deadline > 0 {
		t := time.NewTimer(p.deadline)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case vm := <-p.ready:
		p.onConsumed()
		return p.lease(ctx, vm, OutcomeWaited, p.handoff+vm.restore), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timeout:
		return nil, ErrPoolExhausted
	}
}

// onConsumed contabiliza a saída de uma VM pré-aquecida (warm-- , inUse++) e dispara
// a reposição warm até N.
func (p *Pool) onConsumed() {
	p.mu.Lock()
	p.warm--
	p.inUse++
	p.mu.Unlock()
	p.triggerReplenish()
}

// release descarta o overlay sujo e repõe warm.
func (p *Pool) release(l *Lease) {
	l.vm.overlay.Discard() // estado sujo DEITADO FORA — nunca reciclado
	p.mu.Lock()
	p.inUse--
	p.mu.Unlock()
	p.triggerReplenish()
}

// triggerReplenish agenda a reposição (assíncrona por default; síncrona sob
// [WithSynchronousReplenish]).
//
// O wg.Add(1) da via assíncrona é feito SOB mu e só se o pool não estiver fechado.
// Como [Close] marca closed sob mu ANTES de p.wg.Wait(), a serialização por mu
// garante uma de duas ordens: ou o Add corre antes de Close marcar closed (e o
// Wait espera essa reposição), ou vê closed==true e não agenda nada. Nunca há um
// Add concorrente com o Wait a contador zero — elimina o panic "sync: WaitGroup
// misuse: Add called concurrently with Wait" quando um Release de um lease ainda
// vivo interleava com o shutdown.
func (p *Pool) triggerReplenish() {
	if p.syncReplenish {
		p.doReplenish()
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.wg.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.wg.Done()
		p.doReplenish()
	}()
}

// doReplenish repõe VMs limpas até warm==N (sem exceder o tecto maxN de VMs vivas).
// Serializado por replenishMu — reposições concorrentes não sobre-aprovisionam.
func (p *Pool) doReplenish() {
	p.replenishMu.Lock()
	defer p.replenishMu.Unlock()
	for {
		p.mu.Lock()
		if p.closed || p.warm >= p.warmN || p.warm+p.inUse >= p.maxN {
			p.mu.Unlock()
			return
		}
		// Reserva o lugar (warm++) ANTES do restore/envio: outra reposição vê o lugar
		// ocupado e não duplica.
		p.warm++
		p.mu.Unlock()
		vm, d := p.restoreVM()
		// SLI de depleção do warm pool: este restore é o custo pago OFF-PATH (pré-
		// aquecimento) por um consumo anterior. Torna honesto o cold-start — sob carga
		// warm o p95 do caminho crítico é ≈0 mas o restore real acontece aqui.
		if p.metrics != nil {
			p.metrics.ObserveWarmRestore(context.Background(), p.key(), d)
		}
		p.ready <- vm
	}
}

// restoreVM restaura uma VM limpa do snapshot base (overlay efémero + duração
// modelada). É a ÚNICA fonte de VMs do pool — logo toda a VM entregue é um restore
// fresco do base imutável (invariante de isolamento).
func (p *Pool) restoreVM() (*warmVM, time.Duration) {
	ov, d := p.snap.Restore()
	return &warmVM{overlay: ov, restore: d}, d
}

// lease constrói o Lease e observa o cold-start no SLI (se configurado). Abre o span
// de PROVISÃO ([OpProvisionSandbox]) — por default [NoopTracer], sem custo — e
// passa-o ao recorder para que cold_start_ms/p95_ms sejam anotados num span REAL
// quando um tracer está ligado ([WithTracer]). É o que torna o "custo por span" do
// cold-start vivo no fluxo composto (não só um caminho exercitado isoladamente). O
// span carrega apenas eixos NÃO-SECRETOS (operação, driver, resultado).
func (p *Pool) lease(ctx context.Context, vm *warmVM, outcome ProvisionOutcome, coldStart time.Duration) *Lease {
	_, span := p.tracer.StartSpan(ctx, OpProvisionSandbox)
	span.SetAttribute(AttrOperationName, OpProvisionSandbox)
	span.SetAttribute(AttrDriver, string(p.driver))
	span.SetAttribute(AttrProvisionOutcome, outcome.String())
	if p.metrics != nil {
		p.metrics.Observe(ctx, span, ColdStartSample{
			ImageVersion: p.snap.version,
			Driver:       p.driver,
			Outcome:      outcome,
			ColdStart:    coldStart,
			Restore:      vm.restore,
		})
	}
	span.End()
	return &Lease{pool: p, vm: vm, outcome: outcome, coldStart: coldStart}
}

// observeExhaustion regista o esgotamento no SLI (resultado observável da política).
func (p *Pool) observeExhaustion(ctx context.Context, policy ExhaustionPolicy) {
	if p.metrics != nil {
		p.metrics.ObserveExhaustion(ctx, p.key(), policy)
	}
}

// Stats é um instantâneo da contabilidade do pool (introspecção/testes).
type Stats struct {
	Warm  int // VMs pré-aquecidas na fila
	InUse int // VMs reservadas, ainda não libertadas
	WarmN int // alvo de pré-aquecimento
	MaxN  int // tecto de VMs vivas
}

// Stats devolve o instantâneo corrente.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Stats{Warm: p.warm, InUse: p.inUse, WarmN: p.warmN, MaxN: p.maxN}
}

// waitReplenish espera a quiescência das reposições assíncronas em curso (uso de
// teste: torna a reposição warm observável de forma determinista).
func (p *Pool) waitReplenish() { p.wg.Wait() }

// Close fecha o pool: espera as reposições em curso, marca fechado e drena a fila,
// descartando os overlays pré-aquecidos remanescentes (sem VMs órfãs). Idempotente.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()

	p.wg.Wait()
	for {
		select {
		case vm := <-p.ready:
			vm.overlay.Discard()
			p.mu.Lock()
			p.warm--
			p.mu.Unlock()
		default:
			return
		}
	}
}
