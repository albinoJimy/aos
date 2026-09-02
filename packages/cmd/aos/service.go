// AOS-164a — o LOOP DE SERVIÇO do nó `aos`. O bootstrap de AOS-163 compõe a cadeia
// REAL mas corre UM run síncrono (one-shot). Este ficheiro gradua o nó para
// LONG-RUNNING: um [NodeService] que aceita submissões de run, hospeda MÚLTIPLOS runs
// concorrentemente, isola a falha de cada run (panic-recovery por-run) e encerra
// graciosamente (drena os em curso, nunca os mata cegamente — AOS-023, e liberta os
// leases).
//
// FRONTEIRA de escopo (AOS-164a vs 164b/170):
//
//   - A POSSE de um run é por LEASE DURÁVEL, REUTILIZANDO [worker.Assigner] sobre um
//     [durable.LeaseManager] construído sobre o Event Store do nó — NÃO se reinventa o
//     escalonador nem o mecanismo de lease/fencing. Um run cujo lease é detido por outra
//     réplica NÃO é roubado (sem coordenação intra-processo).
//   - A PERSISTÊNCIA durável do shutdown e o substrato durável são AOS-164b/AOS-170
//     (deferidos): aqui o substrato é o Event Store de REFERÊNCIA do nó. O drain é
//     cooperativo (cancelamento de contexto na fronteira de fim-de-turno), nunca um kill
//     cego.
//   - NÃO há rede aqui (a API HTTP é AOS-166); NÃO há fronteira nó↔ORQ/SCH (AOS-164b).
//     O NodeService é a superfície in-process que a CLI/API conduzirão.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/kernel/agent-runtime/worker"
)

// DefaultLeaseTTL é o TTL do lease de posse de um run quando não configurado. Um run é
// possuído enquanto o lease durar; a posse é RENOVADA por heartbeat enquanto o run corre
// (ver [NodeService.heartbeat]), e só expira por TTL quando o detentor deixa de renovar
// (a liveness é por lease/TTL, nunca por PID — AOS-018).
const DefaultLeaseTTL = 2 * time.Minute

// DefaultCompletedRetention limita quantos desfechos de runs TERMINADOS o serviço retém
// em memória (para [NodeService.Wait]/[NodeService.Outcome]/inspecção). Num loop de
// serviço long-running o registo de terminados cresceria sem limite; ao exceder o limite
// os desfechos MAIS ANTIGOS são podados (FIFO). <= 0 desliga a poda (retenção ilimitada).
const DefaultCompletedRetention = 1024

// Erros do loop de serviço (todos fail-closed).
var (
	// ErrServiceShuttingDown — [NodeService.Submit] recusou porque o shutdown gracioso
	// já começou: o nó PARA de aceitar novos runs (drena os em curso). Fail-closed.
	ErrServiceShuttingDown = errors.New("aos: no em shutdown — nao aceita novos runs")

	// ErrRunAlreadyInProgress — submeteu-se um RunID JÁ hospedado (em curso) por esta
	// réplica. O registo por RunID recusa a duplicação (não se hospeda o mesmo run duas
	// vezes).
	ErrRunAlreadyInProgress = errors.New("aos: run ja em curso nesta replica (RunID duplicado)")

	// ErrRunSuspended — o run PAROU à espera de aval humano (AOS-021) e não é
	// re-submissível: re-submeter perderia o estado suspenso e a aprovação pendente.
	// É RETOMÁVEL por POST /runs/{id}/resume com uma credencial NHI FRESCA.
	ErrRunSuspended = errors.New("aos: run suspenso a espera de aval humano — retome com POST /runs/{id}/resume (credencial fresca), nao re-submeta")

	// ErrRunAlreadyCompleted — submeteu-se um RunID cujo desfecho ESTA réplica já retém
	// como TERMINADO. A re-submissão de um run terminado é recusada explicitamente
	// (fail-closed) enquanto o desfecho estiver retido — em vez de re-executar e
	// SOBRESCREVER silenciosamente o desfecho anterior, ou de a mapear ao enganador
	// [ErrRunLeaseHeldElsewhere] (o lease residual é DESTA réplica, não de outra). Após
	// a poda do desfecho (ver [DefaultCompletedRetention]) o RunID volta a ser submetível.
	ErrRunAlreadyCompleted = errors.New("aos: run ja terminado nesta replica (desfecho retido) — re-submissao recusada")

	// ErrRunLeaseHeldElsewhere — o lease do run é detido por OUTRA réplica (lease vivo):
	// o run NÃO é hospedado aqui (sem roubo de partição — AOS-018/AC3). É a resposta
	// legítima a uma submissão de um run que já corre noutro lado.
	ErrRunLeaseHeldElsewhere = errors.New("aos: lease do run detido por outra replica — run nao hospedado (sem roubo)")

	// ErrEmptyRunID — o goal submetido não tem RunID (é a chave de registo e de lease).
	ErrEmptyRunID = errors.New("aos: goal sem RunID")

	// ErrRunPanicked — sentinela que embrulha o valor de um panic recuperado num run. O
	// panic de um run é CAPTURADO (isolamento por-run): não derruba o nó nem os outros
	// runs; o run é marcado falhado com este erro.
	ErrRunPanicked = errors.New("aos: run entrou em panic (isolado — no continua)")
)

// runState é o estado de UM run hospedado, protegido pelo mutex do [NodeService]. É
// criado no Submit (reserva o RunID), preenchido com o lease e o cancel quando o run é
// hospedado, e move-se de `runs` para `completed` quando termina.
type runState struct {
	runID  string
	lease  durable.Lease
	cancel context.CancelFunc
	done   chan struct{} // fechado quando o run termina e sai do registo de em-curso

	// outcome — escrito UMA vez sob o mutex do serviço, antes de close(done).
	result   agentruntime.Result
	err      error
	panicked bool
	// suspended marca um run que PAROU à espera de aval humano (AOS-021). NÃO terminou:
	// é arquivado no balde `suspended` e continua RETOMÁVEL por POST /runs/{id}/resume.
	suspended bool
	// suspendedAtUnix é QUANDO o run entrou no balde de suspensos, para a idade do mais antigo
	// (ver [NodeService.suspensosAgora]). Escrito sob o mutex do serviço, como o resto.
	//
	// Unix e não time.Time por simetria com `ultimoVarrimentoUnix`: a mesma unidade, a mesma
	// forma de calcular a idade, e um zero que se distingue de «agora».
	suspendedAtUnix int64
}

// RunOutcome é a fotografia imutável do desfecho de um run terminado (inspecção/testes).
type RunOutcome struct {
	RunID    string
	Result   agentruntime.Result
	Err      error
	Panicked bool
}

// NodeService é o LOOP DE SERVIÇO long-running do nó `aos` (AOS-164a). Envolve um [Node]
// (a cadeia REAL de AOS-163) e:
//
//   - ACEITA submissões de run ([Submit]) e HOSPEDA vários concorrentemente;
//   - REGISTA os runs em curso por RunID (mapa protegido por mutex, concorrente-seguro);
//   - possui cada run por LEASE DURÁVEL via [worker.Assigner] (sem roubo entre réplicas);
//   - ISOLA a falha por-run (cada run corre numa goroutine com panic-recovery);
//   - encerra graciosamente ([Shutdown]): para de aceitar, drena os em curso (ou
//     cancela-os limpo no deadline do ctx — nunca mata cego), liberta os leases.
//
// Seguro para uso concorrente: Submit, Shutdown e a conclusão de um run correm em
// goroutines diferentes e coordenam-se pelo mutex + WaitGroup.
type NodeService struct {
	node     *Node
	assigner *worker.Assigner
	leases   *durable.LeaseManager
	logw     io.Writer
	logMu    sync.Mutex // serializa as escritas em logw (ver [NodeService.log])

	hbInterval   time.Duration // período de renovação (heartbeat) da posse; <= 0 desliga
	completedCap int           // teto de desfechos retidos (FIFO); <= 0 = ilimitado

	// AOS-021 — varrimento de aprovações expiradas (decisão do dono: no loop de serviço).
	sweepInterval time.Duration // período do varrimento; <= 0 desliga
	approvalTTL   time.Duration // janela de validade de uma aprovação pendente
	// seloWORM observa os `Append` das vias de governacao — ver [saudeDeSelagem]. NAO e ponteiro:
	// o valor-zero e valido (nunca falhou, nunca teve sucesso) e evita um nil a esquecer.
	seloWORM  saudeDeSelagem
	sweepStop chan struct{} // fechado no Shutdown para parar o varrimento

	// AOS-252 — varrimento de deadlines duráveis (CheckDeadlines). Partilha o sweepStop:
	// o Shutdown pára os dois varrimentos com UM close.
	deadlineSweepInterval time.Duration // período do varrimento; <= 0 desliga

	// AOS-249 — manutenção do token da custódia da KEK (Vault). Partilha o MESMO sweepStop:
	// o Shutdown pára os três laços com UM close.
	vaultTokenRenewInterval time.Duration // período da manutenção; <= 0 desliga

	// AOS-267 — scheduler interno de RETENÇÃO (expiração por TTL sem cron externo). Partilha o
	// MESMO sweepStop: o Shutdown pára os quatro laços com UM close.
	retentionSweepInterval time.Duration // cadência do varrimento; <= 0 desliga
	// AOS-283 — POSSE do laço de retenção por lease durável (`lease:svc:retention`). É o
	// laço que EXIGE exclusão entre réplicas: medido, duas réplicas sobre o mesmo
	// substrato selavam DOIS `retention.expired` para o mesmo facto, porque o guard
	// [NodeService.expireInFlight] é um `atomic.Bool` POR PROCESSO. Ver posse_de_laco.go.
	posseRetencao *posseDeLaco
	// ---- OBSERVABILIDADE DO VARREDOR DE RETENÇÃO (AOS-267) ----------------------------------
	//
	// «A expiração por TTL está a correr?» era uma pergunta sem resposta em runtime. O
	// escalonador declara-se no banner de ARRANQUE e, a partir daí, é invisível: se nunca for
	// armado (política ausente, intervalo <= 0), ou se PARAR — e ele pára para sempre depois de
	// um incidente de integridade da hash-chain, deliberadamente —, o único sinal é uma linha de
	// log que ninguém está a ver. E o que deixa de acontecer é o apagamento de dados fora do TTL,
	// que é uma obrigação com prazo.
	//
	// Estes três campos alimentam `/metrics`. Ver [apiHandler.handleMetrics].
	varrimentosTotal atomic.Int64
	// ultimoVarrimentoUnix é o instante da última passagem CONCLUÍDA (0 = nenhuma ainda). Unix
	// e não time.Time por ser lido sem lock a partir do handler das métricas.
	ultimoVarrimentoUnix atomic.Int64
	// --- OS LAÇOS QUE NÃO TINHAM SINAL NENHUM -----------------------------------------
	//
	// Achado da validação longitudinal de 2026-08-25. Dos oito laços periódicos do nó, três
	// varredores de SERVIÇO não expunham NADA: `approval_sweeper` (expira aprovações
	// pendentes), `deadline_sweeper` (impõe os prazos dos runs) e `orphan_sweeper` (retoma
	// runs interrompidos). Se qualquer um deles parasse, o efeito seria SILENCIOSO — as
	// aprovações nunca expirariam, os runs nunca esgotariam prazo, os órfãos nunca seriam
	// retomados — e nenhuma série do `/metrics` se moveria.
	//
	// O `aos_runs_suspended 0` que o nó publica hoje tem DUAS leituras que ninguém consegue
	// separar: «não há ninguém à espera» e «o varredor de aprovações morreu há três dias».
	//
	// PORQUE A IDADE E NÃO SÓ O CONTADOR. Um contador só denuncia paragem por COMPARAÇÃO de
	// duas leituras — foi precisamente o que a validação teve de fazer à mão, com uma espera
	// de horas. A IDADE denuncia numa leitura só, que é o que um operador tem. O contador
	// fica para a taxa; a idade é o detector.
	//
	// Seguem o molde do varredor de retenção, que é o único que já estava completo — mesma
	// unidade (Unix), mesma leitura sem lock a partir do handler, e a MESMA regra de que a
	// idade só sai DEPOIS da primeira passagem (antes disso, um `0` diria «acabou de varrer»
	// sobre um nó que ainda não varreu nada).
	//
	// O `heartbeat` fica DE FORA de propósito e a razão é que ele é de outra natureza: é
	// por-run e não de serviço, e a sua falha JÁ tem consequência observável — o lease expira
	// e o run é cancelado. Instrumentá-lo seria contar uma coisa por cada run em voo.
	passagensAprovacao  atomic.Int64
	ultimaAprovacaoUnix atomic.Int64
	passagensPrazo      atomic.Int64
	ultimoPrazoUnix     atomic.Int64
	passagensOrfaos     atomic.Int64
	ultimoOrfaoUnix     atomic.Int64
	// varredorParado marca a paragem DEFINITIVA por incidente de integridade — distinta de
	// «ainda não armado» e de «armado, à espera do primeiro tick». As três leem-se de maneira
	// diferente e exigem acções diferentes.
	varredorParado atomic.Bool

	// AOS-274 — AVALIADOR DE SLOs/ALERTAS. Partilha o MESMO sweepStop: o Shutdown pára os cinco
	// laços com UM close. nil ⇒ desligado (AOS_SLO_EVAL_INTERVAL=0 ou nó não composto). É o ÚNICO
	// laço deste loop declaradamente FAIL-OPEN — ver slo_evaluator.go.
	slo *sloEvaluator

	// AOS-101 — AGENDADOR DE EXPORTAÇÃO DE BACKUP. Partilha o MESMO sweepStop: o Shutdown pára os
	// seis laços com UM close. NÃO tem cadência própria — lê [backup.Exporter.Periodicity] do
	// exportador composto no nó, para que o RPO anunciado e o RPO ligado sejam o mesmo por
	// construção. Ver backup_scheduler.go.
	//
	// AS SÉRIES QUE O TORNAM OBSERVÁVEL. «O log está a ser exportado?» tem, como a expiração por
	// TTL de AOS-267, três leituras que exigem acções diferentes: nunca armado (não há destino),
	// armado e PARADO (soberania ou colisão de referência — não volta sozinho), armado e a
	// correr. O contador dá a taxa; a IDADE é o detector, porque denuncia numa leitura só.
	ciclosDeBackup   atomic.Int64
	ultimoBackupUnix atomic.Int64
	backupFalhas     atomic.Int64
	backupPanicos    atomic.Int64
	// backupParado marca a paragem DEFINITIVA por erro permanente — distinta de «não armado» e de
	// «armado, à espera do primeiro tick», exactamente como [varredorParado] no de retenção.
	backupParado atomic.Bool

	// expireInFlight serializa as passagens do [audit.ExpirationJob] — as conduzidas por
	// POST /dsar/expire (AOS-213) E as do scheduler interno (AOS-267). O Run do job é pensado
	// para uma execução de cada vez (o check-then-Add da idempotency key não é atómico ao nível
	// do registo), pelo que duas passagens concorrentes poderiam selar DOIS eventos
	// retention.expired para o mesmo facto. O guard vive AQUI, e não no [apiHandler], porque
	// tem de ser UM SÓ: um guard por-via seriam dois guards sem exclusão entre eles, e a rota
	// voltaria a poder sobrepor-se ao tick. CAS não-bloqueante — quem perde não espera.
	expireInFlight atomic.Bool

	// resumeInFlight serializa as retomas POR RUN. Ver a nota (0) em [NodeService.Resume]: sem
	// ela, a via DURAVEL escapa a reclamacao do balde de suspensos e duas retomas do mesmo run
	// chegam ambas ao submit. Chave = runID, porque retomas de runs diferentes nao competem.
	resumeInFlight map[string]struct{}

	mu             sync.Mutex
	runs           map[string]*runState // em curso (por RunID)
	completed      map[string]*runState // terminados (inspecção/observabilidade)
	suspended      map[string]*runState // à espera de aval humano (AOS-021) — RETOMÁVEIS
	completedOrder []string             // ordem de término (FIFO) para poda do `completed`
	closed         bool                 // shutdown iniciado — recusa novos runs (governa admissão, sob mu)
	wg             sync.WaitGroup       // conta as goroutines de run hospedadas

	// draining ESPELHA `closed` para a sonda de prontidão: é armado sob `mu` no MESMO
	// ponto onde `closed=true` (fonte de verdade única e monotónica — o drain nunca
	// reverte), mas é LIDO lock-free por [Draining] (/readyz) sem tocar em `mu`. Assim a
	// sonda pode ser sondada à frequência de um probe sem contender com o mutex que
	// serializa a admissão/conclusão de runs — simétrico a [eventstore.Store.Healthy],
	// que foi feito atómico pela mesma razão. `closed` continua a governar a admissão
	// sob `mu` (a secção crítica closed+registo que impede um run escapar ao Shutdown
	// fica intacta); só a LEITURA da sonda deixa de adquirir o lock.
	draining atomic.Bool
}

// NodeServiceOption configura o [NodeService].
type NodeServiceOption func(*nodeServiceConfig)

type nodeServiceConfig struct {
	ttl             time.Duration
	hbInterval      time.Duration // 0 ⇒ derivado de ttl/3 em NewNodeService
	completedCap    int           // 0 ⇒ DefaultCompletedRetention; < 0 ⇒ ilimitado
	completedCapSet bool
	workerID        string
	leaseClock      durable.Clock
	logw            io.Writer
	// AOS-021 — varrimento de aprovações. sweepIntervalSet distingue "não configurado"
	// (⇒ default) de "explicitamente 0" (⇒ DESLIGADO, usado em testes).
	sweepInterval    time.Duration
	sweepIntervalSet bool
	approvalTTL      time.Duration
	// AOS-252 — idem para o varrimento de deadlines (CheckDeadlines).
	deadlineSweepInterval    time.Duration
	deadlineSweepIntervalSet bool
	// AOS-249 — idem para a manutenção do token da custódia da KEK (Vault).
	vaultTokenRenewInterval    time.Duration
	vaultTokenRenewIntervalSet bool
	// AOS-267 — idem para o scheduler interno de retenção (expiração por TTL).
	retentionSweepInterval    time.Duration
	retentionSweepIntervalSet bool
	// AOS-274 — idem para o avaliador de SLOs/alertas; sloWindow é a janela de dados agregada
	// (0 ⇒ [DefaultSLOWindow]).
	sloEvalInterval    time.Duration
	sloEvalIntervalSet bool
	sloWindow          time.Duration
}

// WithLeaseTTL define o TTL do lease de posse de run (default [DefaultLeaseTTL]).
// Valores <= 0 são ignorados (mantém o default).
func WithLeaseTTL(ttl time.Duration) NodeServiceOption {
	return func(c *nodeServiceConfig) {
		if ttl > 0 {
			c.ttl = ttl
		}
	}
}

// WithLeaseHeartbeat define o período de renovação (heartbeat) da posse do lease durante
// um run. Default: TTL/3. Um valor <= 0 é ignorado (mantém o default). Deve ser
// confortavelmente inferior ao TTL para a posse nunca expirar a meio de um run vivo.
func WithLeaseHeartbeat(interval time.Duration) NodeServiceOption {
	return func(c *nodeServiceConfig) {
		if interval > 0 {
			c.hbInterval = interval
		}
	}
}

// WithCompletedRetention limita quantos desfechos de runs terminados o serviço retém em
// memória (poda FIFO dos mais antigos ao exceder). Default [DefaultCompletedRetention];
// um valor <= 0 desliga a poda (retenção ilimitada — usar só quando o desfecho é colhido
// e libertado por outra via).
func WithCompletedRetention(max int) NodeServiceOption {
	return func(c *nodeServiceConfig) {
		c.completedCap = max
		c.completedCapSet = true
	}
}

// WithServiceWorkerID rotula os leases desta réplica (só observabilidade — a liveness é
// por lease/TTL, nunca por identidade nem PID).
func WithServiceWorkerID(id string) NodeServiceOption {
	return func(c *nodeServiceConfig) { c.workerID = id }
}

// WithLeaseClock injecta o relógio do [durable.LeaseManager] (determinismo em teste de
// expiração de lease, sem sleeps). Ignora nil.
func WithLeaseClock(clk durable.Clock) NodeServiceOption {
	return func(c *nodeServiceConfig) {
		if clk != nil {
			c.leaseClock = clk
		}
	}
}

// WithServiceLog injecta o destino dos logs do serviço (arranque/panic/drain). nil ⇒
// sem logs.
func WithServiceLog(w io.Writer) NodeServiceOption {
	return func(c *nodeServiceConfig) { c.logw = w }
}

// NewNodeService constrói o loop de serviço sobre um [Node] já composto (AOS-163). O
// [durable.LeaseManager] é construído sobre o Event Store do nó (a posse durável partilha
// o substrato do nó) e embrulhado num [worker.Assigner] REUTILIZADO — não se reinventa o
// escalonador. node e node.EventStore são obrigatórios (fail-closed).
func NewNodeService(node *Node, opts ...NodeServiceOption) (*NodeService, error) {
	if node == nil {
		return nil, errors.New("aos: NodeService exige um Node (nil)")
	}
	if node.EventStore == nil {
		return nil, errors.New("aos: NodeService exige o Event Store do no (nil)")
	}
	cfg := nodeServiceConfig{ttl: DefaultLeaseTTL}
	for _, o := range opts {
		o(&cfg)
	}
	// Período de heartbeat: explícito, ou derivado de TTL/3 (renova ~3x por TTL, dando
	// margem folgada para a posse nunca expirar a meio de um run vivo).
	hbInterval := cfg.hbInterval
	if hbInterval <= 0 {
		hbInterval = cfg.ttl / 3
	}
	// Retenção de desfechos: explícita (incl. <= 0 = ilimitada), ou o default.
	completedCap := DefaultCompletedRetention
	if cfg.completedCapSet {
		completedCap = cfg.completedCap
	}
	// AOS-021 — varrimento de aprovações. Explícito (incl. 0 = DESLIGADO) ou o default.
	sweepInterval := DefaultApprovalSweepInterval
	if cfg.sweepIntervalSet {
		sweepInterval = cfg.sweepInterval
	}
	approvalTTL := cfg.approvalTTL
	if approvalTTL <= 0 {
		approvalTTL = integration.DefaultApprovalTTL
	}
	// AOS-252 — varrimento de deadlines. Explícito (incl. 0 = DESLIGADO) ou o default.
	deadlineSweepInterval := DefaultDeadlineSweepInterval
	if cfg.deadlineSweepIntervalSet {
		deadlineSweepInterval = cfg.deadlineSweepInterval
	}
	// AOS-249 — manutenção do token da custódia. Explícita (incl. 0 = DESLIGADA) ou o default.
	vaultTokenRenewInterval := DefaultVaultTokenRenewInterval
	if cfg.vaultTokenRenewIntervalSet {
		vaultTokenRenewInterval = cfg.vaultTokenRenewInterval
	}
	// AOS-267 — scheduler de retenção. Explícito (incl. 0 = DESLIGADO em processo) ou o default.
	retentionSweepInterval := DefaultRetentionSweepInterval
	if cfg.retentionSweepIntervalSet {
		retentionSweepInterval = cfg.retentionSweepInterval
	}
	// AOS-274 — avaliador de SLOs/alertas. Explícito (incl. 0 = DESLIGADO) ou o default.
	sloEvalInterval := DefaultSLOEvalInterval
	if cfg.sloEvalIntervalSet {
		sloEvalInterval = cfg.sloEvalInterval
	}
	sloWindow := cfg.sloWindow
	if sloWindow <= 0 {
		sloWindow = DefaultSLOWindow
	}

	var leaseOpts []durable.LeaseOption
	if cfg.leaseClock != nil {
		leaseOpts = append(leaseOpts, durable.WithLeaseClock(cfg.leaseClock))
	}
	if cfg.workerID != "" {
		leaseOpts = append(leaseOpts, durable.WithWorkerID(cfg.workerID))
	}
	leases, err := durable.NewLeaseManager(node.EventStore, cfg.ttl, leaseOpts...)
	if err != nil {
		return nil, fmt.Errorf("aos: lease manager do loop de servico: %w", err)
	}
	assigner, err := worker.NewAssigner(leases)
	if err != nil {
		return nil, fmt.Errorf("aos: assigner (posse por lease) do loop de servico: %w", err)
	}

	s := &NodeService{
		node:          node,
		assigner:      assigner,
		leases:        leases,
		logw:          cfg.logw,
		hbInterval:    hbInterval,
		completedCap:  completedCap,
		runs:          make(map[string]*runState),
		completed:     make(map[string]*runState),
		suspended:     make(map[string]*runState),
		sweepInterval: sweepInterval,
		approvalTTL:   approvalTTL,
		sweepStop:     make(chan struct{}),

		deadlineSweepInterval:   deadlineSweepInterval,
		vaultTokenRenewInterval: vaultTokenRenewInterval,
		retentionSweepInterval:  retentionSweepInterval,
	}
	// AOS-283 — a posse do laço de retenção usa a MESMA autoridade de lease que possui os
	// runs (nenhum segundo mecanismo) e o MESMO período de renovação (TTL/3 por omissão),
	// que é folgado o suficiente para a posse nunca expirar entre duas renovações.
	s.posseRetencao = novaPosseDeLaco(lacoRetencao, leases, hbInterval, s.log)
	s.log("loop de servico do no `aos` pronto (AOS-164a): TTL de lease=%s, heartbeat=%s, worker=%q, retencao=%d",
		cfg.ttl, hbInterval, cfg.workerID, completedCap)
	// AOS-021 — varrimento de aprovações expiradas no LOOP DE SERVIÇO. Só arranca quando
	// há four-eyes composto (sem aprovadores não há nada a expirar) e o período é > 0.
	if sweepInterval > 0 && node.PendingApprovals != nil {
		go s.sweepApprovals(s.sweepStop)
		s.log("varrimento de aprovacoes (AOS-021): LIGADO — periodo=%s, TTL de aprovacao=%s; um pendente sem decisao expira e o run fica RETOMAVEL",
			sweepInterval, approvalTTL)
	}
	// AOS-263 — POSTURA DO PROMPT DE EXAUSTÃO. Declarada AQUI, e não no Bootstrap, porque é
	// aqui que o TTL REAL dos pendentes é conhecido (o Bootstrap não o vê) e é este varrimento
	// que expira a pergunta. O argumento é o estado do prompt REALMENTE composto no
	// observador de burn-down — nunca um literal, nunca a intenção da config.
	for _, line := range exhaustionPromptPostureBanner(node.progress.promptArmed(), approvalTTL) {
		s.log("%s", line)
	}
	// AOS-252 — varrimento de DEADLINES duráveis (CheckDeadlines) no loop de serviço. Só
	// arranca quando há máquinas de estado compostas e o período é > 0.
	if deadlineSweepInterval > 0 && node.stateGates != nil {
		go s.sweepDeadlines(s.sweepStop)
		s.log("varrimento de deadlines (AOS-252): LIGADO — periodo=%s; um run preso a meio de um turno materializa running->timed_out E e INTERROMPIDO (contexto do run cancelado)", deadlineSweepInterval)
	}
	// AOS-249 — MANUTENÇÃO DO TOKEN da custódia da KEK (Vault) no loop de serviço. Só arranca
	// quando a custódia composta TEM token para manter (o vault in-memory de referência não
	// implementa a porta) e o período é > 0.
	if _, temToken := node.DSARVault.(vaultTokenRefresher); vaultTokenRenewInterval > 0 && temToken {
		go s.renewVaultToken(s.sweepStop)
		s.log("manutencao do token da custodia da KEK (AOS-249): LIGADA — periodo=%s; o token e re-lido do ficheiro montado e renovado antes de expirar, e o /readyz fica VERMELHO ANTES da expiracao se a manutencao falhar (nunca morte silenciosa da custodia)", vaultTokenRenewInterval)
	}
	// AOS-267 — SCHEDULER INTERNO DE RETENÇÃO no loop de serviço. Só arranca com a conjunção de
	// [retentionSchedulerArmed] satisfeita (política armada + legal hold + WORM + job compostos);
	// o banner DECLARA sempre a postura, ligada ou dormente, e a RAZÃO de estar dormente — sem
	// isso o operador que definiu a política não distingue "corre" de "não corre".
	if retentionSchedulerArmed(node, retentionSweepInterval) {
		go s.sweepRetention(s.sweepStop)
	}
	s.log("%s", retentionSchedulerBanner(node, retentionSweepInterval))
	// AOS-274 — AVALIADOR DE SLOs/ALERTAS no loop de serviço. O avaliador é COMPOSTO sempre que a
	// cadência é > 0 (não exige a torneira de spans: as fontes de maior severidade — prontidão do
	// plano de controlo e integridade do WORM — não dependem dela), e o banner declara quais SLIs
	// têm produtor neste deployment e quais ficam SEM AMOSTRAS. É o único laço FAIL-OPEN daqui.
	if sloEvaluatorArmed(node, sloEvalInterval) {
		s.slo = newSLOEvaluator(node, sloEvalInterval, sloWindow)
		go s.evaluateSLOs(s.sweepStop)
	}
	s.log("%s", sloEvaluatorBanner(node, sloEvalInterval, sloWindow))
	// AOS-101 — AGENDADOR DE EXPORTAÇÃO DE BACKUP no loop de serviço. Só arranca quando o
	// [Bootstrap] compôs o exportador (o que exige um destino imutável EXPLÍCITO — desligado por
	// omissão) e a periodicidade dele é > 0. A cadência NÃO é uma opção deste serviço de
	// propósito: é a do exportador, para que o RPO anunciado seja o RPO ligado. O banner DECLARA
	// sempre a postura — ligada, dormente ou desligada — e, quando ligada, se a cadeia foi
	// RETOMADA do destino ou começada aqui. Ver backup_scheduler.go.
	if backupSchedulerArmed(node) {
		go s.exportarBackups(s.sweepStop)
	}
	s.log("%s", backupSchedulerBanner(node))
	return s, nil
}

// Submit aceita um run e hospeda-o. Passos (fail-closed):
//
//  1. valida o RunID e recusa se o nó está em shutdown;
//  2. RESERVA o RunID no registo de em-curso (recusa um RunID já em curso —
//     [ErrRunAlreadyInProgress] — ou já terminado e ainda retido — [ErrRunAlreadyCompleted]);
//     a reserva bloqueia uma segunda submissão concorrente do mesmo RunID;
//  3. adquire a POSSE por lease via [worker.Assigner.TryAcquire]; se o lease é detido por
//     outra réplica ((_, false, nil)) o run NÃO é hospedado (sem roubo) e a reserva é
//     desfeita;
//  4. re-verifica o shutdown (uma corrida com Shutdown durante a aquisição do lease) e,
//     livre, arranca a goroutine do run com panic-recovery.
//
// O ctx dado é usado SÓ para a aquisição do lease (I/O no Event Store); a EXECUÇÃO do run
// corre sob um contexto próprio do serviço (o run sobrevive ao retorno de Submit e só é
// cancelado por [Shutdown]).
func (s *NodeService) Submit(ctx context.Context, goal agentruntime.Goal) error {
	return s.submit(ctx, goal, false)
}

// submit é o Submit com o interruptor da RETOMA. resuming=true vem exclusivamente de
// [NodeService.Resume] e dispensa a recusa por suspensão — é precisamente o run suspenso
// que se está a re-hospedar, e o log continua a dizer `waiting_on_human` até o arranque o
// repor em `running`. Nenhuma outra via o liga.
func (s *NodeService) submit(ctx context.Context, goal agentruntime.Goal, resuming bool) error {
	if goal.RunID == "" {
		return ErrEmptyRunID
	}
	runID := goal.RunID

	// (1)+(2) Reserva sob o mutex: recusa shutdown e duplicação de RunID.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrServiceShuttingDown
	}
	if _, dup := s.runs[runID]; dup {
		s.mu.Unlock()
		return ErrRunAlreadyInProgress
	}
	if _, susp := s.suspended[runID]; susp {
		// Suspenso à espera de aval humano: NÃO é re-submissível (perderia o estado), mas
		// é RETOMÁVEL — por POST /runs/{id}/resume com credencial fresca.
		s.mu.Unlock()
		return ErrRunSuspended
	}
	if _, done := s.completed[runID]; done {
		// Desfecho ainda retido: re-submissão recusada explicitamente (não re-executa nem
		// sobrescreve o desfecho, nem finge que o lease é de outra réplica). Após a poda
		// do desfecho o RunID volta a ser submetível.
		s.mu.Unlock()
		return ErrRunAlreadyCompleted
	}
	rs := &runState{runID: runID, done: make(chan struct{})}
	s.runs[runID] = rs // RESERVA — bloqueia duplicados enquanto adquirimos o lease
	s.mu.Unlock()

	// (2-bis) SUSPENSÃO DURÁVEL (AOS-021). O balde acima é um cache que um restart esvazia;
	// o log não. Sem esta consulta, re-submeter depois de um restart RECOMEÇARIA do zero um
	// run que está à espera de um humano — perdendo a trajectória e deixando o pendente e o
	// grant órfãos. FAIL-CLOSED: uma leitura que falha recusa a admissão, em vez de admitir
	// sobre um estado que não se conseguiu ler.
	if !resuming {
		susp, serr := s.suspendedDurably(ctx, runID)
		if serr != nil {
			s.unreserve(rs)
			return fmt.Errorf("aos: ler o estado duravel do run %q: %w", runID, serr)
		}
		if susp {
			s.unreserve(rs)
			return ErrRunSuspended
		}
	}

	// (3) POSSE por lease FORA do mutex (I/O no Event Store). Sem roubo: um lease vivo
	// detido por outra réplica ⇒ (_, false, nil) ⇒ não hospeda.
	lease, acquired, err := s.assigner.TryAcquire(ctx, runID)
	if err != nil {
		s.unreserve(rs)
		return fmt.Errorf("aos: aquisicao de lease do run %q: %w", runID, err)
	}
	if !acquired {
		s.unreserve(rs)
		return ErrRunLeaseHeldElsewhere
	}

	// (4) Arranque sob o mutex: a re-verificação de `closed` e o wg.Add ficam no MESMO
	// secção crítica que o set de `closed` do Shutdown — garantindo que um run contado no
	// WaitGroup NUNCA escapa a um Shutdown que já começou (e vice-versa).
	runCtx, cancel := context.WithCancel(context.Background())
	// O run corre sob um contexto PRÓPRIO (sobrevive ao retorno de Submit e só é cancelado
	// por Shutdown), o que o desliga do ctx de entrada — e com ele de todos os seus valores.
	//
	// O PLANO DE REPLAY DA RETOMA (AOS-021) é um desses valores, e perdê-lo esvaziava a
	// retoma em silêncio: os turnos já vividos voltavam a interrogar o modelo em vez de
	// serem reproduzidos das capturas, e a acção re-mediada só coincidia com a APROVADA se
	// o modelo fosse determinista. Re-anexa-se explicitamente — só este valor, para não
	// arrastar o resto do ctx do pedido para a vida do run.
	if plan := replayPlanFrom(ctx); plan != nil {
		runCtx = withReplayPlan(runCtx, plan)
	}
	// IDENTIDADE POR-RUN DO MODELO (AOS-278, CUTOVER DURO). O token NHI do RUN
	// (goal.Credential) — o MESMO que cada tool call mediada verifica — anexa-se ao runCtx
	// PRÓPRIO do run para o caminho do modelo o apresentar ao estágio authn REAL do GW.
	// Anexa-se aqui, no runCtx que sobrevive ao retorno de Submit (o ctx de entrada e os seus
	// valores morrem com o pedido), a par do plano de replay. Vazio ⇒ não anexa: o estágio
	// authn nega atribuívelmente (sem principal forjado). Cobre a RETOMA — o Resume repovoa
	// goal.Credential com a credencial fresca antes de re-submeter.
	runCtx = withModelCredential(runCtx, goal.Credential)
	s.mu.Lock()
	if s.closed {
		// Shutdown começou durante a aquisição do lease: aborta limpo (larga a posse
		// em-processo; o lease durável expira por TTL — sem revogação).
		delete(s.runs, runID)
		s.mu.Unlock()
		cancel()
		s.assigner.Release(runID)
		close(rs.done)
		return ErrServiceShuttingDown
	}
	rs.lease = lease
	rs.cancel = cancel
	s.wg.Add(1)
	s.mu.Unlock()

	// INGESTÃO/REDACÇÃO (AOS-091/AOS-208). O objectivo é INPUT DO UTILIZADOR: redige-se
	// na FRONTEIRA de ingestão — ANTES de o run arrancar — e o valor REDIGIDO é o que
	// segue para o loop E o que alcança o Event Store/memory/spans/audit (fan-out pela
	// MESMA porta). Só quando há objectivo E principal a quem atribuir (um objectivo
	// sem PII é byte-idêntico; sem objectivo/principal nada há a minimizar — no-op
	// retro-compatível). Fail-closed: uma falha de ingestão desfaz a posse e recusa o
	// run (não se hospeda sobre uma minimização parcial). Corre APÓS o wg.Add/rs.lease
	// para o desenrolar reutilizar o mesmo caminho de finish em caso de erro.
	if s.node.Ingestion != nil && goal.Objective != "" && goal.Principal.NHIID != "" {
		// AOS-208: `subject` é o PRINCIPAL DO RUN (a NHI do agente), não o titular dos
		// dados (GDPR data subject). Sob RemoveAllPolicy (minimização, sem tokenização) só
		// alimenta o SubjectID do registo de audit; ver o aviso de crypto-shredding em
		// integration.IngestObjective antes de habilitar tokenização por-titular.
		subject := goal.Principal.NHIID
		ing, ierr := s.node.Ingestion.IngestObjective(ctx, subject, runID, goal.Principal.NHIID, goal.Objective)
		if ierr != nil {
			// Desenrola a posse contabilizada: decrementa o wg, larga o lease e move o
			// run para terminado com o erro (mesmo caminho de finish, sem goroutine).
			rs.err = fmt.Errorf("aos: ingestao/redaccao do objectivo do run %q (AOS-208): %w", runID, ierr)
			s.wg.Done()
			s.finish(rs)
			return rs.err
		}
		goal.Objective = ing.Redacted // o run vê o objectivo MINIMIZADO
	}

	go s.hostRun(runCtx, rs, goal)
	return nil
}

// unreserve desfaz a reserva de um RunID que não chegou a ser hospedado (falha de
// aquisição de lease). Larga a posse em-processo por segurança (idempotente) e fecha o
// done da reserva.
func (s *NodeService) unreserve(rs *runState) {
	s.mu.Lock()
	delete(s.runs, rs.runID)
	s.mu.Unlock()
	s.assigner.Release(rs.runID)
	close(rs.done)
}

// hostRun corre UM run numa goroutine ISOLADA. O defer garante que:
//
//   - um PANIC do run é CAPTURADO (recover) — não derruba o nó nem os outros runs; o run
//     é marcado falhado com [ErrRunPanicked];
//   - o heartbeat de posse é PARADO e AGUARDADO (sem goroutine órfã) antes de largar o
//     lease;
//   - a posse em-processo do lease é largada e o run migra de `runs` para `completed`;
//   - o WaitGroup é decrementado (o Shutdown drena por ele).
//
// A ordem dos defers (LIFO) é: recover() primeiro (captura o panic de Run), depois parar
// o heartbeat (join), depois finish() (liberta lease + move para completed + fecha done),
// e por fim wg.Done().
func (s *NodeService) hostRun(ctx context.Context, rs *runState, goal agentruntime.Goal) {
	defer s.wg.Done()
	defer s.finish(rs)

	// Renovador de posse por-run: mantém o lease vivo enquanto o run corre (fecha a janela
	// em que um run mais longo que o TTL veria a posse EXPIRAR — liveness classificá-lo-ia
	// zombie e outra réplica reclamaria o lease, arrancando uma 2ª execução concorrente do
	// mesmo RunID). A goroutine é PARADA e AGUARDADA aqui (join) antes de finish largar o
	// lease — nem heartbeat órfão nem corrida com a libertação. rs.lease e rs.cancel foram
	// escritos em Submit sob o mutex ANTES deste `go hostRun` (happens-before): lê-los sem
	// lock é seguro.
	hbStop := make(chan struct{})
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		s.heartbeat(ctx, rs.runID, rs.lease, rs.cancel, hbStop)
	}()
	defer func() {
		close(hbStop)
		<-hbDone
	}()

	defer func() {
		if r := recover(); r != nil {
			s.mu.Lock()
			rs.panicked = true
			rs.err = fmt.Errorf("%w: %v", ErrRunPanicked, r)
			s.mu.Unlock()
			s.log("run %q PANIC recuperado (isolamento por-run) — no continua: %v", rs.runID, r)
		}
	}()

	// AOS-180: antes de correr um run, repõe o step-ledger a partir do Event Store.
	// Num run novo o stream não existe e Rebuild é um no-op; numa retoma após crash/
	// failover reconstrói as entradas already-applied para que o ledger deduplique
	// efeitos já executados.
	if err := s.node.Runtime.RebuildLedger(ctx, rs.runID); err != nil {
		s.mu.Lock()
		rs.err = fmt.Errorf("aos: rebuild do ledger durável do run %q: %w", rs.runID, err)
		s.mu.Unlock()
		return
	}

	// AOS-218: ABRE a máquina de estados durável do run (AOS-017) e regista o
	// [control.StateGate] que o canal de steer usa para materializar running↔paused. É
	// reconstruída do log (retoma após crash) e reclamada (ready→running) com o fencing
	// token do lease JÁ detido (rs.lease foi escrito em Submit sob o mutex, happens-before
	// deste `go hostRun`). O gate é LIBERTADO no fim (defer), simétrico ao registo de
	// em-curso — e com ele o estado em memória do disjuntor do run (AOS-251).
	var res agentruntime.Result
	var err error
	if s.node.stateGates != nil {
		if err := s.node.stateGates.Open(ctx, rs.runID, rs.lease.Token); err != nil {
			s.mu.Lock()
			rs.err = fmt.Errorf("aos: abertura da maquina de estados do run %q (AOS-218): %w", rs.runID, err)
			s.mu.Unlock()
			return
		}
		defer s.node.stateGates.Close(rs.runID)
		defer s.node.breakers.forget(rs.runID) // nil-safe: sem disjuntor composto é no-op
		// AOS-262: e com ele a superfície de burn-down do run — o latch do aviso e o cursor
		// do ledger. nil-safe: sem burn-down composto é no-op.
		defer s.node.progress.forget(rs.runID)
		// SELO DO ESTADO TERMINAL (AOS-252). Registado DEPOIS dos defers de libertação para
		// correr ANTES deles (LIFO: o gate ainda está aberto) e ANTES do recover de
		// isolamento — que fica a jusante na pilha de defers e trata o panic em definitivo.
		// Aqui o panic é apenas DETECTADO (recover) para escolher a razão do selo e
		// IMEDIATAMENTE re-lançado. Corre em todos os caminhos de saída: sucesso, erro,
		// MaxTurns, trip do breaker (no-op — já materializado) e panic.
		defer func() {
			r := recover()
			// AOS-254: o titular (goal.Principal.NHIID) acompanha o selo — a saga de rollback que
			// um desfecho `failed` aciona corre no step-ledger cifrado por-titular, que o exige.
			s.sealTerminalState(rs, goal.Principal.NHIID, res, err, r != nil)
			if r != nil {
				panic(r)
			}
		}()
		if gate := s.node.stateGates.resolveGate(rs.runID); gate != nil {
			// CLAIM NO ARRANQUE (AOS-251): a aresta ready→running é reclamada QUANDO o run
			// começa a executar, não no primeiro pause/escalada. Sem isto um run comum
			// ficava em `ready` do princípio ao fim e o disjuntor do agente vivo — no-op
			// fora de `running` — nunca disparava, inclusive no deny-loop. Numa retoma o
			// claim é no-op (a máquina não está em ready) e a reposição de
			// waiting_on_human a seguir é que conduz. Fail-closed: sem o claim durável o
			// run NÃO arranca — arrancar seria correr com o disjuntor cego.
			if err := gate.claimRunning(ctx); err != nil {
				s.mu.Lock()
				rs.err = fmt.Errorf("aos: claim ready->running do run %q no arranque (AOS-251): %w", rs.runID, err)
				s.mu.Unlock()
				return
			}
			// RETOMA (AOS-021): a suspensão é DURÁVEL, pelo que a reconstrução acima devolve
			// `waiting_on_human` num run que está a ser retomado. Hospedá-lo é, por definição,
			// voltar a corrê-lo — repõe-se `running`. Sem isto, uma segunda escalada (o caso
			// normal: o agente pede outra acção de risco no turno seguinte) tentaria
			// waiting_on_human→waiting_on_human e o run morreria como FALHADO.
			if err := gate.resumeIfWaiting(ctx); err != nil {
				s.mu.Lock()
				rs.err = fmt.Errorf("aos: repor o run %q em execucao na retoma (AOS-021): %w", rs.runID, err)
				s.mu.Unlock()
				return
			}
		}
	}

	// CRASH-RESUME (AOS-253): torna ESTE run reconstituível por uma varredura de arranque se o
	// processo morrer a meio. Escrito ANTES do primeiro turno porque um crash NÃO escala — pára
	// sem deixar, por si, quem o retome — e o Goal (objectivo, contexto de memória, tool set) não
	// é reconstituível de mais lado nenhum do log (o turn.recorded guarda hashes, a ingestão
	// redige, e a credencial nunca se persiste). É a MESMA escrita que a suspensão de AOS-021 já
	// fazia, mas antecipada do momento da escalada para o arranque.
	s.persistCrashResumeRecord(ctx, goal)

	res, _, err = s.node.Runtime.Run(ctx, goal, nil)

	// DEGRADAÇÃO DECLARADA POR ORÇAMENTO (AOS-260): o run parou porque a admissão do turno de
	// modelo negou headroom, SEM erro e SEM resposta final. Sem esta linha o desfecho seria
	// legível apenas no log durável (`timed_out`/`budget_exhausted`, ver [runGate.sealTerminal])
	// e o operador que olha para o log do processo veria um run a acabar em silêncio.
	//
	// Este caminho é o do prompt de exaustão DESARMADO: com ele armado o run não chega aqui
	// assim — foi suspenso e o sinal viaja no `err`, absorvido mais abaixo.
	if res.BudgetExhausted {
		s.log("run %q PAROU por ORCAMENTO ESGOTADO (AOS-260) ao fim de %d turnos completos — %s. Estado duravel: timed_out (%s), NAO failed: um tecto defensivo atingido nao e uma falha recuperavel por compensacao. Para o run prosseguir e preciso levantar o tecto (AOS_BUDGET_MAX_TOKENS / AOS_BUDGET_MAX_COST_MICRO_USD) e re-submeter",
			rs.runID, res.Turns, res.BudgetExhaustionReason, reasonBudgetExhausted)
	}

	// SUSPENSÃO À ESPERA DE HUMANO (AOS-021): o run NÃO terminou — uma tool call foi
	// escalada e nenhum efeito ocorreu. Persiste-se o registo de RETOMA (o Goal SEM a
	// credencial) para que qualquer réplica o possa retomar depois, e larga-se tudo o
	// resto (lease, heartbeat) — não se seguram recursos durante minutos de latência
	// humana. FAIL-CLOSED: se o registo não persistir, o run é tratado como FALHADO em
	// vez de suspenso — um "suspenso" que não se consegue retomar seria pior (ficaria à
	// espera de uma aprovação que nunca teria efeito).
	suspenso := false
	if res.Escalated && s.node.ResumeRecords != nil {
		if perr := s.node.ResumeRecords.Put(ctx, resumeRecordFromGoal(goal)); perr != nil {
			err = fmt.Errorf("aos: persistir registo de retoma do run %q: %w", rs.runID, perr)
		} else {
			suspenso = true
		}
	}
	// SUSPENSÃO POR EXAUSTÃO DE ORÇAMENTO (AOS-263). O run também parou à espera de um humano
	// — a transição durável para `waiting_on_human` já aconteceu —, mas o sinal chegou pela
	// porta do observador de burn-down, que só sabe devolver `error`. Sem esta absorção o run
	// seria arquivado como FALHADO e o operador veria "completed" com erro num run que o log
	// diz estar à espera dele. É a MESMA contabilidade de AOS-021 (registo de retoma + balde
	// de suspensos), aplicada ao segundo tipo de decisão humana — ver exhaustion_prompt.go.
	if !suspenso {
		suspenso, err = s.absorveSuspensaoPorExaustao(ctx, goal, err)
	}

	s.mu.Lock()
	rs.result = res
	rs.err = err
	rs.suspended = suspenso
	s.mu.Unlock()
}

// persistCrashResumeRecord grava o registo de RETOMA do run (o Goal SEM credencial) no
// ARRANQUE de cada hospedagem, para que [NodeService.ResumeInterruptedRuns] (AOS-253)
// reconstitua um run interrompido a meio por um crash. É o MESMO registo que a suspensão de
// AOS-021 escreve na escalada — mas antecipado para antes do primeiro turno, porque um crash
// não escala e não deixa, por si, quem o retome.
//
// IDEMPOTENTE: a idempotency_key deriva do RunID, pelo que uma escalada posterior (AOS-021) ou
// uma re-hospedagem na retoma re-escreve o MESMO registo e o Event Store deduplica — o Goal é o
// mesmo objecto (já redigido) nos dois pontos. Só se grava COM titular (Principal.NHIID): o corpo
// é cifrado por-titular e sem titular não há KEK sob que o selar — o MESMO guard da ingestão, e um
// run sem principal não é retomável no modelo selado (o replayPlanFor precisa do titular para abrir
// as capturas).
//
// FAIL-SOFT DECLARADO: uma falha é registada em VOZ ALTA mas NÃO aborta o run — só a
// crash-resumibilidade se perde, não a correcção do run em curso. Abortar todo o run por um soluço
// do registo de retoma trocaria uma degradação recuperável por uma paragem dura; a suspensão de
// AOS-021 aborta porque aí o "suspenso" seria irretomável, o que é pior — aqui o run corre na mesma.
func (s *NodeService) persistCrashResumeRecord(ctx context.Context, goal agentruntime.Goal) {
	if s.node == nil || s.node.ResumeRecords == nil || goal.Principal.NHIID == "" {
		return
	}
	if err := s.node.ResumeRecords.Put(ctx, resumeRecordFromGoal(goal)); err != nil {
		s.log("registo de retoma de arranque do run %q FALHOU (AOS-253) — o run corre, mas um crash a meio NAO sera retomavel pela varredura de arranque: %v", goal.RunID, err)
	}
}

// resumeRecordFromGoal projecta o Goal no registo de retoma. A Credential é
// DELIBERADAMENTE omitida — ver [integration.ResumeRecord].
func resumeRecordFromGoal(goal agentruntime.Goal) integration.ResumeRecord {
	return integration.ResumeRecord{
		RunID:             goal.RunID,
		Principal:         goal.Principal,
		Scope:             goal.Scope,
		Model:             goal.Model,
		System:            goal.System,
		Tools:             goal.Tools,
		Skills:            goal.Skills,
		Objective:         goal.Objective,
		MemoryContext:     goal.MemoryContext,
		MaxTurns:          goal.MaxTurns,
		ParentTraceParent: goal.ParentTraceParent,
	}
}

// heartbeat renova periodicamente a posse do lease do run enquanto ele corre. Pára quando
// hbStop fecha (run terminou) ou o ctx do run é cancelado. Se perder a partição —
// [durable.ErrLeaseSuperseded] (outro claim de token maior superou-nos) ou
// [durable.ErrLeaseExpired] (TTL esgotado apesar da renovação, ex.: pausa de GC/relógio) —
// CANCELA o run COOPERATIVAMENTE (cancel): já não somos o dono. O anti-duplo-efeito NESTE
// caminho NÃO é um fencing de escritas — o nó NÃO compõe o [durable.FencedAppender] (eixo
// nomeado, ver ADR-018 §5-bis); a barreira REAL é a soma de (1) a posse arbitrada por CAS
// atómico do lease ([worker.Assigner]), (2) o cancel cooperativo, que pára o run na fronteira
// de fim-de-turno, e (3) a idempotência do step-ledger (chave = f(RunID,StepID)), que
// deduplica no replay qualquer efeito já aplicado caso uma escrita tardia escape. Erros
// transitórios do Event Store são registados e re-tentados no tick seguinte. O lease durável
// é o mesmo (o heartbeat não minta token), pelo que se reutiliza a cópia imutável capturada
// no arranque.
func (s *NodeService) heartbeat(ctx context.Context, runID string, lease durable.Lease, cancel context.CancelFunc, hbStop <-chan struct{}) {
	if s.hbInterval <= 0 {
		return // heartbeat desligado (ex.: TTL demasiado curto para derivar período)
	}
	t := time.NewTicker(s.hbInterval)
	defer t.Stop()
	for {
		select {
		case <-hbStop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.leases.Heartbeat(ctx, lease); err != nil {
				if errors.Is(err, durable.ErrLeaseSuperseded) || errors.Is(err, durable.ErrLeaseExpired) {
					s.log("run %q PERDEU a posse do lease (%v) — a cancelar COOPERATIVAMENTE (anti-duplo-efeito por cancel + idempotencia do step-ledger f(RunID,StepID); este caminho NAO compoe fencing de escritas)", runID, err)
					if cancel != nil {
						cancel()
					}
					return
				}
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return // ctx do run terminou durante o I/O do heartbeat
				}
				// Transitório (contenção/ES): loga e re-tenta no próximo tick.
				s.log("run %q heartbeat de posse falhou (transitorio, re-tenta): %v", runID, err)
			}
		}
	}
}

// finish liberta a posse em-processo do lease (o lease durável expira por TTL — não há
// revogação, AOS-018), cancela o contexto do run e move-o de em-curso para terminado,
// fechando o seu done. É o ponto único de saída de um run do registo de em-curso.
func (s *NodeService) finish(rs *runState) {
	if rs.suspended {
		// SUSPENSO (AOS-021): a paragem é deliberada e o run VAI ser re-hospedado pela
		// retoma. Anuncia-se o largar no log durável — senão o lease continua vivo e o
		// POST /resume bate em ErrRunLeaseHeldElsewhere durante todo o TTL, na própria
		// réplica que suspendeu o run. Uma falha do anúncio degrada para a semântica de
		// sempre (esperar o TTL), pelo que não é fatal: regista-se e segue.
		if err := s.assigner.Relinquish(context.Background(), rs.runID); err != nil {
			s.log("largar o lease do run suspenso %q falhou (a retoma tera de esperar o TTL): %v", rs.runID, err)
		}
	} else {
		s.assigner.Release(rs.runID)
	}
	if rs.cancel != nil {
		rs.cancel()
	}
	s.mu.Lock()
	delete(s.runs, rs.runID)
	if rs.suspended {
		// À ESPERA DE HUMANO: não é um desfecho. Fica no balde de suspensos — fora da
		// retenção FIFO de terminados (que o podaria e o tornaria irretomável) — e
		// continua a bloquear uma re-submissão do mesmo RunID, mas por POST /resume.
		if rs.suspendedAtUnix == 0 {
			// NÃO se re-carimba: a idade que interessa é a de quando este estado começou a
			// esperar por um humano, e uma re-suspensão do MESMO runState (a reposição do
			// ramo de erro da retoma) não é uma espera nova.
			rs.suspendedAtUnix = time.Now().Unix()
		}
		s.suspended[rs.runID] = rs
	} else {
		if _, seen := s.completed[rs.runID]; !seen {
			s.completedOrder = append(s.completedOrder, rs.runID)
		}
		s.completed[rs.runID] = rs
		s.pruneCompletedLocked()
	}
	s.mu.Unlock()
	close(rs.done)
}

// pruneCompletedLocked poda os desfechos MAIS ANTIGOS (FIFO) enquanto o registo de
// terminados exceder o teto de retenção. Chamado sob o mutex. completedCap <= 0 desliga a
// poda. A compactação é in-place (append sobre o próprio prefixo) — sem crescimento do
// array de suporte nem retenção fantasma das strings podadas.
func (s *NodeService) pruneCompletedLocked() {
	if s.completedCap <= 0 {
		return
	}
	for len(s.completedOrder) > s.completedCap {
		oldest := s.completedOrder[0]
		delete(s.completed, oldest)
		s.completedOrder = append(s.completedOrder[:0], s.completedOrder[1:]...)
	}
}

// Shutdown encerra o nó GRACIOSAMENTE: para de aceitar novos runs e espera os em curso
// drenarem. Se o ctx expirar antes do dreno, CANCELA os runs em curso de forma LIMPA
// (cancelamento de contexto — o loop pára na fronteira de fim-de-turno, nunca a meio nem
// por kill cego, AOS-023) e espera-os desenrolar (libertando os leases). Devolve nil se
// drenou a tempo, ou o erro do ctx se teve de cancelar. Idempotente.
//
// SHUTDOWN DURÁVEL (AOS-164b sobre AOS-170) — o encerramento NÃO perde nem duplica
// trabalho DURÁVEL, sem necessidade de flush adicional NESTE caminho. A garantia é
// composicional, não uma acção do Shutdown:
//
//   - Trabalho COMMITTED já está no WAL. Cada evento que o nó considerou committed
//     (turnos gravados via TurnRecorder, sinais de controlo, e os próprios registos de
//     lease) foi persistido e FSYNC'D pelo Event Store durável ANTES de o Append devolver
//     (write-ahead, AOS-170). Um crash — ou uma saída limpa — após o Shutdown reencontra
//     esse trabalho no reinício via replay do WAL (eventstore.Open), byte-a-byte, com a
//     dedup/CAS reconstruída (idempotência) — sem perda nem duplicação. O Shutdown não
//     precisa de "descarregar" nada committed: já está durável no momento do commit.
//   - Trabalho EM-CURSO é abortado na FRONTEIRA de fim-de-turno. O cancelamento é
//     COOPERATIVO (o run pára ao observar o ctx, nunca a meio de uma escrita durável nem
//     por kill), pelo que não deixa um commit parcial: ou o turno já commitou (durável) ou
//     ainda não escreveu (nada a recuperar). O tail parcial de um crash a meio de um write
//     é detectado e ignorado no replay (crash-safety do WAL).
//   - DUPLA-EXECUÇÃO no reinício é barrada por LEASE DURÁVEL + IDEMPOTÊNCIA. Cada run é
//     possuído por um lease durável de token monotónico (AOS-018); o Shutdown liberta a posse
//     em-processo (finish → Release) e o registo durável do lease expira por TTL. No reinício,
//     um novo Claim minta um token ESTRITAMENTE MAIOR — um token residual da execução anterior
//     é superado (ErrLeaseSuperseded) e pára cooperativamente; e a idempotência do step-ledger
//     (chave = f(RunID,StepID)) deduplica no replay qualquer efeito já aplicado. Juntos — e NÃO
//     um fencing de escritas (este caminho também não compõe o FencedAppender, ver heartbeat +
//     ADR-018 §5-bis) — garantem que uma 2ª execução do mesmo RunID não produz efeitos duplicados. A durabilidade final do WAL do Event Store é selada por
//     [Node.Close] (chamado a JUSANTE deste Shutdown na sequência de paragem do nó); mas,
//     como cada Append já fez fsync, essa Close é o descarregamento de cortesia, não a
//     condição de durabilidade do trabalho committed.
func (s *NodeService) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.draining.Store(true) // espelha o drain (armado SOB mu, no mesmo ponto que closed) para leitura lock-free da sonda
	inflight := len(s.runs)
	s.mu.Unlock()
	// Para os laços periódicos (AOS-021/252/249/267/274/101) com UM close — idempotente: o Shutdown já
	// retornou cedo se `closed` estava armado, pelo que este close acontece uma só vez.
	close(s.sweepStop)
	// AOS-283 — a posse dos laços de serviço é largada por ANÚNCIO, não deixada a expirar:
	// a réplica seguinte assume o laço de imediato em vez de esperar o TTL inteiro (ADR-023
	// §2.5). Depois do `close` acima, porque o anúncio é o ÚLTIMO acto da posse — largar
	// antes de o laço parar deixaria uma passagem a correr sobre uma posse já anunciada
	// como largada.
	//
	// JANELA RESIDUAL, declarada em vez de calada: o `close` pára o laço no PRÓXIMO tick,
	// não interrompe uma passagem já a meio. Um shutdown que apanhe uma passagem em voo
	// anuncia a largada com ela ainda a selar. A consequência é limitada pelo que a réplica
	// seguinte faz a seguir — reclama e corre a SUA passagem, que salta por idempotência os
	// factos já selados (o seen-set é re-hidratado da cadeia no arranque) — e é a mesma
	// janela que o desenho por-run tem. Fechá-la exigiria a passagem participar no
	// WaitGroup do drain, que é trabalho de outro ticket.
	s.posseRetencao.largar(ctx)
	s.log("shutdown gracioso iniciado — %d run(s) em curso a drenar (nao aceita novos)", inflight)

	drained := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(drained)
	}()

	select {
	case <-drained:
		s.log("shutdown: todos os runs drenaram graciosamente")
		return nil
	case <-ctx.Done():
		// Deadline: cancela os em curso LIMPO (cooperativo, nunca kill cego) e espera-os
		// desenrolar — cada um liberta o seu lease no finish().
		s.log("shutdown: deadline atingido — a cancelar (limpo) os runs em curso")
		s.cancelInFlight()
		s.wg.Wait()
		s.log("shutdown: runs cancelados desenrolaram e libertaram os leases")
		return ctx.Err()
	}
}

// cancelInFlight cancela o contexto de cada run em curso (cancelamento cooperativo). NÃO
// mata cegamente: o run pára ao verificar o ctx (fronteira de fim-de-turno / checkpoint)
// e desenrola pelo finish(), libertando o lease.
func (s *NodeService) cancelInFlight() {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.runs))
	for _, rs := range s.runs {
		if rs.cancel != nil {
			cancels = append(cancels, rs.cancel)
		}
	}
	s.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// InProgress devolve os RunIDs em curso, por ordem estável (observabilidade). É uma
// cópia — mutá-la não afecta o registo.
func (s *NodeService) InProgress() []string {
	s.mu.Lock()
	out := make([]string, 0, len(s.runs))
	for id := range s.runs {
		out = append(out, id)
	}
	s.mu.Unlock()
	sort.Strings(out)
	return out
}

// Draining indica se o shutdown gracioso já começou (ou seja, se [Submit] já recusa
// novos runs com [ErrServiceShuttingDown]). Lê o espelho atómico `draining` — armado
// SOB o mutex no MESMO ponto que o `closed` que governa a admissão (ver [Shutdown]),
// e monotónico (o drain nunca reverte), pelo que NÃO pode divergir do estado que
// recusa runs. A leitura é lock-free de propósito: a sonda de prontidão (/readyz) pode
// ser sondada à frequência de um probe sem contender com o `s.mu` que serializa a
// admissão/conclusão de runs — simétrico a [eventstore.Store.Healthy], que foi feito
// atómico pela mesma razão. É esta a fonte que /readyz consulta para virar 503 durante
// o drain (o orquestrador deixa de encaminhar tráfego novo), enquanto a liveness
// (/healthz) permanece 200 até o processo sair.
func (s *NodeService) Draining() bool {
	return s.draining.Load()
}

// InProgressCount devolve o número de runs em curso.
func (s *NodeService) InProgressCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}

// Outcome devolve o desfecho de um run TERMINADO (result/erro/panic) e se está registado
// como terminado. Um run ainda em curso devolve (_, false).
func (s *NodeService) Outcome(runID string) (RunOutcome, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.completed[runID]
	if !ok {
		return RunOutcome{}, false
	}
	return RunOutcome{RunID: rs.runID, Result: rs.result, Err: rs.err, Panicked: rs.panicked}, true
}

// Suspended indica se o run está SUSPENSO à espera de aval humano (AOS-021) — não
// terminou, e é retomável por POST /runs/{id}/resume. Devolve também o desfecho parcial
// (o Result com Escalated=true e a preview da acção que aguarda decisão).
//
// O balde em memória é um CACHE, não a verdade: um run desconhecido desta réplica é
// procurado no log (a máquina de estados durável do run). Sem isso, um restart do nó
// tornaria irretomável um run cujo registo de retoma, pendente e grant estão todos
// persistidos — o operador aprovaria e nada consumiria a aprovação.
func (s *NodeService) Suspended(ctx context.Context, runID string) (RunOutcome, bool) {
	s.mu.Lock()
	rs, ok := s.suspended[runID]
	if ok {
		defer s.mu.Unlock()
		return RunOutcome{RunID: rs.runID, Result: rs.result, Err: rs.err, Panicked: rs.panicked}, true
	}
	_, done := s.completed[runID]
	_, running := s.runs[runID]
	s.mu.Unlock()
	// Esta réplica CONHECE o run (em curso ou com desfecho retido): a contabilidade em
	// memória é autoritativa e não há nada a reconstruir. Consultar o log aqui reportaria
	// como suspenso um run que já foi arquivado como falhado — por exemplo o fail-closed
	// de hostRun, que marca FALHADO um run cuja transição durável já ocorreu.
	if done || running {
		return RunOutcome{}, false
	}
	return s.suspendedFromLog(ctx, runID)
}

// suspendedFromLog reconstitui a suspensão de um run que esta réplica não conhece: o
// estado durável diz `waiting_on_human`. O desfecho parcial não sobrevive em memória, mas
// o TURNO em que o run parou está no pendente durável — reporta-se esse, em vez de zero
// (que seria uma segunda mentira operacional, depois da que a suspensão já corrigiu).
//
// Uma falha de leitura resolve-se como "não suspenso": é um caminho de CONSULTA, e o 404
// uniforme e não-enumerável é preferível a inventar um estado. A admissão (Submit) e a
// retoma tratam o erro de leitura como fatal — ver [NodeService.suspendedDurably].
func (s *NodeService) suspendedFromLog(ctx context.Context, runID string) (RunOutcome, bool) {
	st, susp, err := s.suspensaoDuravel(ctx, runID)
	if err != nil || !susp {
		return RunOutcome{}, false
	}
	// UMA PAUSA NÃO É UMA ESCALADA. Marcar `Escalated` aqui era a segunda metade do achado
	// C: o read-path passava a descrever um run que parou sozinho — por interrupt gracioso
	// ou por disparo do disjuntor — como um run à espera de decisão humana. Ninguém pediu
	// decisão a ninguém, e não há pendentes para listar: procurá-los devolveria sempre uma
	// lista vazia, que na resposta parece um erro de leitura em vez do que é.
	if st == state.Paused {
		return RunOutcome{RunID: runID, Result: agentruntime.Result{Paused: true}}, true
	}
	oc := RunOutcome{RunID: runID, Result: agentruntime.Result{Escalated: true}}
	if s.node != nil && s.node.PendingApprovals != nil {
		if recs, perr := s.node.PendingApprovals.ListForRun(ctx, runID); perr == nil {
			for _, r := range recs {
				if r.Turn > oc.Result.Turns {
					oc.Result.Turns = r.Turn
				}
			}
		}
	}
	return oc, true
}

// suspendedDurably lê do log se o run está À ESPERA DE HUMANO. É a fonte de verdade da
// suspensão; o balde em memória apenas a espelha enquanto o processo viver.
//
// Sem registo de gates de estado composto, devolve false sem erro: um deployment que não
// tem a máquina de estados também não tem suspensão para recuperar.
func (s *NodeService) suspendedDurably(ctx context.Context, runID string) (bool, error) {
	// A PAUSA CONTA, e é o que fecha o achado da verificação de completude de 2026-08-23:
	// `paused` era ABSORVENTE de facto. Nada no binário conduzia `paused → running` — o
	// condutor existe ([runGate.Resume], `steer_gates.go`) e tinha ZERO chamadores de
	// produção, mantido vivo pelo compilador por um `var _ control.StateGate`.
	//
	// E o nó ANUNCIAVA o contrário: a recusa de `POST /exhaustion` e o banner de arranque
	// dizem ambos ao operador que a pausa graciosa «deixa o run RETOMÁVEL». O disjuntor
	// chega lá SOZINHO com os defaults (3 iterações estéreis, 30 min de wall-clock).
	//
	// DOIS EFEITOS, e o segundo é tão importante como o primeiro:
	//   (a) `POST /runs/{id}/resume` passa a aceitar um run pausado;
	//   (b) `Submit` passa a RECUSAR a re-submissão do mesmo run id — que era a única
	//       saída existente e a pior possível: `claimRunning` é no-op fora de `Ready`, pelo
	//       que a segunda execução corria com o disjuntor CEGO
	//       (`CountsAsActiveWork` exige `Running`), sem selo terminal e sem deadline.
	//
	// O PREDICADO É O CERTO PARA AS DUAS VIAS FAIL-CLOSED que o consomem (a retoma e a
	// admissão): as duas só precisam de saber que o run está suspenso e é retomável. Quem
	// precisa de saber QUAL das duas suspensões é o read-path — e para esse existe
	// [NodeService.suspensaoDuravel], que devolve o estado em vez de o achatar. Ver o achado
	// C da verificação de funcionamento de 2026-08-23: achatar aqui fez o `GET /runs/{id}`
	// reportar `waiting_on_human` sobre runs PAUSADOS.
	_, susp, err := s.suspensaoDuravel(ctx, runID)
	return susp, err
}

// suspensaoDuravel devolve o ESTADO de suspensão do run e se ele está suspenso. É o
// [NodeService.suspendedDurably] SEM o achatamento — as duas suspensões do nó não são a
// mesma coisa para quem LÊ:
//
//   - [state.WaitingOnHuman] — alguém PEDIU uma decisão humana (aprovação de tool call
//     escalada, ou o prompt de exaustão de orçamento). Há pendentes a listar.
//   - [state.Paused] — o run parou GRACIOSAMENTE (interrupt out-of-band, ou disparo do
//     disjuntor). Ninguém pediu decisão a ninguém e NÃO há pendentes.
//
// Reportar a segunda como a primeira manda o operador esperar por uma aprovação que nunca
// foi pedida, com a lista de pendentes vazia a parecer um erro de leitura.
func (s *NodeService) suspensaoDuravel(ctx context.Context, runID string) (state.State, bool, error) {
	if s.node == nil || s.node.stateGates == nil {
		return "", false, nil
	}
	st, err := s.node.stateGates.currentState(ctx, runID)
	if err != nil {
		return "", false, err
	}
	return st, st == state.WaitingOnHuman || st == state.Paused, nil
}

// DurableState devolve o estado durável do run reconstruído do log (AOS-252). É uma
// LEITURA — nada transita. Um run sem transições (ou sem stream) é [state.Ready]. É o que
// permite ao GET /runs/{id} reflectir o desfecho de um run que esta réplica já não conhece
// em memória (restart, poda FIFO): o mapa `completed` é um cache, a máquina de estados é a
// verdade.
func (s *NodeService) DurableState(ctx context.Context, runID string) (state.State, error) {
	if s.node == nil || s.node.stateGates == nil {
		return "", nil
	}
	return s.node.stateGates.currentState(ctx, runID)
}

// Wait bloqueia até o run runID terminar (sair do registo de em-curso) e devolve o seu
// desfecho, ou (_, false, ctx.Err()) se o ctx expirar antes. Um run desconhecido (nunca
// submetido ou já colhido) devolve imediatamente o que houver em `completed`. Útil para
// testes determinísticos e para conduzir o run a partir da CLI.
func (s *NodeService) Wait(ctx context.Context, runID string) (RunOutcome, bool, error) {
	s.mu.Lock()
	if rs, ok := s.runs[runID]; ok {
		done := rs.done
		s.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return RunOutcome{}, false, ctx.Err()
		}
	} else {
		s.mu.Unlock()
	}
	oc, ok := s.Outcome(runID)
	return oc, ok, nil
}

// log escreve uma linha no destino de logs do serviço (se configurado).
//
// SERIALIZADO: [io.Writer] não promete nada sobre uso concorrente, e este método é chamado
// de várias goroutines ao mesmo tempo — a de cada run hospedado (panic recuperado, selo do
// desfecho), a do heartbeat de posse, as dos CINCO laços periódicos (aprovações, deadlines,
// manutenção do token da custódia, retenção e avaliação de SLOs) e a de quem invoca o Shutdown. Sem o lock, dois Fprintf concorrentes sobre o
// mesmo destino são uma corrida de dados a sério: o detector apanha-a num destino de teste
// e num ficheiro dá linhas entrelaçadas — precisamente no log que serve para explicar o que
// correu mal. O mutex é DEDICADO e nunca é adquirido com `s.mu` detido, pelo que não pode
// participar numa inversão de ordem com o lock que serializa a admissão de runs.
func (s *NodeService) log(format string, args ...any) {
	if s.logw == nil {
		return
	}
	s.logMu.Lock()
	defer s.logMu.Unlock()
	fmt.Fprintf(s.logw, "[aos-service] "+format+"\n", args...)
}

// suspensosAgora devolve quantos runs esperam por um humano NESTA réplica e o instante em que o
// mais antigo começou a esperar (0 se nenhum tem carimbo).
//
// PORQUE EXISTE. A serialização das retomas (achado 1.12) entrou em produção sem observabilidade
// nenhuma: um run preso em `waiting_on_human` que não conseguisse retomar não aparecia em série
// nenhuma — só se dava por ele quando alguém tentasse retomá-lo e falhasse. Uma guarda sem sinal
// é uma guarda que ninguém vê falhar.
//
// POR RÉPLICA, e isso é uma limitação e não um detalhe: a suspensão é DURÁVEL e decidível de
// qualquer réplica, mas este balde é o cache em-memória desta. Um run suspenso que esta réplica
// nunca hospedou não é contado aqui. A série mede «quantos esperam por mim», não «quantos esperam».
func (s *NodeService) suspensosAgora() (int, int64) {
	if s == nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var maisAntigo int64
	for _, rs := range s.suspended {
		if rs == nil || rs.suspendedAtUnix == 0 {
			continue
		}
		if maisAntigo == 0 || rs.suspendedAtUnix < maisAntigo {
			maisAntigo = rs.suspendedAtUnix
		}
	}
	return len(s.suspended), maisAntigo
}
