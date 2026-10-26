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

	// ErrLeaseHeartbeatNotBelowTTL — [WithLeaseHeartbeat] recebeu um intervalo >= ao TTL do
	// lease (AOS-297). Não é uma afinação apertada: com o heartbeat a chegar depois de o
	// lease expirar, a posse cai em TODOS os runs, sempre, ao fim de um TTL. Recusa-se na
	// construção porque o par é conhecido lá e porque um nó que arranca assim parece são —
	// o sintoma só aparece um TTL mais tarde, longe da causa.
	ErrLeaseHeartbeatNotBelowTTL = errors.New("aos: intervalo de heartbeat do lease >= TTL do lease — a posse expiraria antes da primeira renovacao em todos os runs (use um intervalo estritamente inferior; o default e TTL/3)")

	// ErrRunAlreadyInProgress — submeteu-se um RunID JÁ hospedado (em curso) por esta
	// réplica. O registo por RunID recusa a duplicação (não se hospeda o mesmo run duas
	// vezes).
	ErrRunAlreadyInProgress = errors.New("aos: run ja em curso nesta replica (RunID duplicado)")

	// ErrCallerInFlightCeiling — o SUBMISSOR já tem o seu tecto de SUBMISSÕES NOVAS simultâneas
	// (AOS-456). NÃO é um tecto de ocupação: ver [WithInFlightPerCaller].
	//
	// É distinto de [ErrTooManyInFlight]/do tecto global de propósito: um 429 que não distingue
	// «o nó está cheio» de «TU estás cheio» manda o operador diagnosticar o nó quando o problema
	// é a quota de um chamador. O corpo da resposta continua uniforme; o que muda é o erro que o
	// log do operador nomeia.
	ErrCallerInFlightCeiling = errors.New("aos: o submissor ja tem o seu tecto de SUBMISSOES NOVAS simultaneas (AOS_INGRESS_MAX_INFLIGHT_PER_CALLER) — a rajada de um chamador nao ocupa os lugares dos outros; nao e um tecto de OCUPACAO (um run suspenso sai da contagem e a retoma nao a consulta)")

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

	// principal é o SUBMISSOR a que este run é imputado, para o tecto de submissões novas
	// simultâneas POR-CHAMADOR (AOS-456). Vem de [imputadoA] — que em modo SOBERANO devolve sempre um valor
	// DERIVADO PELO NÓ (o submissor do plano lido do Event Store, ou o principal que o
	// `readGovernance.authorize` resolveu da credencial verificada, achado A7 do AOS-217), nunca
	// um campo de corpo auto-declarado. Ver a nota de [imputadoA] para o porquê de um run-filho
	// ser imputado a quem pediu o plano e não ao drenador que o submete.
	//
	// Fica AQUI e não se recalcula: o `goal` não sobrevive ao registo, e contar por um valor que
	// se fosse buscar noutro sítio seria contar por um proxy.
	principal string

	// titular é o titular dos dados do run ([agentruntime.Goal.Titular]): a chave sob que a captura
	// cifrou o conteúdo dele. É por ele que o apagamento do titular retira o desfecho do registo em
	// memória (AOS-496).
	titular string

	// existente diz que esta hospedagem é a de um run que JÁ EXISTE no estado durável do nó, e não a
	// de um run novo (AOS-520): a retoma pela API e a varredura de crash-resume (`resuming`), ou um
	// `POST /runs` de um `run_id` cujo estado durável, lido por [NodeService.submitObservando] antes
	// de hospedar, já não é o inicial. Escrito uma vez, antes de a goroutine do run arrancar. Um run
	// que já existe não é escolhido: o seu Goal foi conferido com o que está selado.
	existente bool
	// plano é o pedido de plano de que o run é trabalho, VERIFICADO pelo nó no `POST /runs`
	// (AOS-520, [origemDoPlano]). O valor-zero é um run que não é trabalho de um pedido verificado.
	plano origemDoPlano
	// runsProdutores é o run produtor de cada entrada do plano (`inputs[].from_run`), pela MESMA
	// ordem dos `Goal.Inputs`, tal como o `POST /runs` o leu do corpo (AOS-553). SÓ TEM USO na
	// travessia da aresta do plano, e lá é conferido contra o `plano` acima
	// ([runDoNoProdutor]): não dá autoridade nenhuma por si.
	//
	// Fica AQUI, como o `plano`, e não no Goal: não viaja no Goal nem no contexto, e um run
	// RETOMADO não precisa dele — a travessia corre uma vez, na primeira hospedagem, e o registo
	// de retoma já leva os bytes resolvidos e o handle que os sela.
	runsProdutores []string

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
	// inFlightPerCaller é o tecto de SUBMISSÕES NOVAS simultâneas por SUBMISSOR (AOS-456). <= 0 ⇒ NÃO COMPOSTO,
	// e o banner declara-o — não se finge uma barreira que não existe.
	inFlightPerCaller int

	// AOS-021 — varrimento de aprovações expiradas (decisão do dono: no loop de serviço).
	sweepInterval time.Duration // período do varrimento; <= 0 desliga
	approvalTTL   time.Duration // janela de validade de uma aprovação pendente
	// approvalClock é o «agora» contra o qual o varrimento mede a idade dos pendentes; nil ⇒
	// time.Now. Só os testes o fixam (AOS-488): uma expiração provocada encolhendo o TTL e
	// medida no relógio de parede caía, no Windows, no mesmo tick do `created_at` e não
	// expirava nada.
	approvalClock func() time.Time
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
	// AOS-422 — OS RUNS VIVOS QUE A VARREDURA SALTOU.
	//
	// O AOS-411 fez um run vivo deixar de contar como órfão, e com isso calou a passagem
	// periódica que só encontra runs a correr — que era o ruído a corrigir. Mas calou
	// TAMBÉM a única prova de que a guarda funciona: o log da passagem só sai com órfãos
	// verdadeiros, e os contadores eram variáveis locais.
	//
	// `aos_orphan_sweeps_total` já diz que o varredor correu. O que faltava era dizer o que
	// ele SALTOU — e é a diferença entre «correu e não havia nada» e «correu e protegeu um
	// run a trabalhar».
	vivosSaltadosAqui   atomic.Int64
	vivosSaltadosNoutra atomic.Int64
	// layouts conta os runs hospedados por layout de montagem do prompt (AOS-489). Um run que
	// continua em 1.3.0 — registo de retoma antigo — deixa de ser visível só no WAL.
	layouts *runsPorLayout
	// desfechos conta os runs selados por estado terminal e razão do veredicto do kernel
	// (AOS-493), e os concluídos sem nenhuma tool call com tools na oferta.
	desfechos *desfechosDeRuns
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
	// armado e PARADO (soberania, outro dono da cadeia, registo que não verifica, colisão de conteúdo
	// ou log atrás do cursor — não volta sozinho), armado e a
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
	// inFlightPerCaller — tecto de SUBMISSÕES NOVAS simultâneas por submissor (AOS-456). <= 0 ⇒ não composto.
	inFlightPerCaller int
	workerID          string
	leaseClock        durable.Clock
	logw              io.Writer
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
// um run. Default: TTL/3. Um valor <= 0 é ignorado (mantém o default).
//
// Tem de ser ESTRITAMENTE INFERIOR ao TTL, e isso é IMPOSTO — não sugerido. Um intervalo
// >= TTL produz perda de posse determinística em TODOS os runs, ao fim de um TTL, e antes
// de AOS-297 o nó arrancava sem aviso: a frase «deve ser confortavelmente inferior ao TTL»
// estava aqui e nada a verificava. A recusa vive em [NewNodeService] e não neste closure,
// porque as opções são aplicadas por ordem e o TTL pode ainda não ter sido definido quando
// esta correr — validar aqui recusaria um par legítimo só por causa da ordem das opções.
func WithLeaseHeartbeat(interval time.Duration) NodeServiceOption {
	return func(c *nodeServiceConfig) {
		if interval > 0 {
			c.hbInterval = interval
		}
	}
}

// WithInFlightPerCaller define o tecto de SUBMISSÕES NOVAS simultâneas por SUBMISSOR (AOS-456) —
// e NÃO de ocupação: as isenções (suspenso, retoma) compõem-se. Ver [imputadoA] para o que conta como
// submissor e [NodeService.submit] para onde o tecto morde.
//
// `max <= 0` deixa o tecto NÃO COMPOSTO, que é o default. NÃO é um valor que "desliga" em silêncio:
// a superfície do operador ([ingressLimitsFromEnv]) RECUSA `0` e negativos, e a ausência do tecto é
// declarada no banner de arranque. Esta opção é composição in-process e não tem por onde recusar
// (uma [NodeServiceOption] não devolve erro), pelo que a validação vive na fronteira que o operador
// toca.
func WithInFlightPerCaller(max int) NodeServiceOption {
	return func(c *nodeServiceConfig) { c.inFlightPerCaller = max }
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
	//
	// CABLAGEM FAIL-CLOSED (AOS-297). Um intervalo explícito >= TTL não é uma afinação
	// agressiva: é perda de posse GARANTIDA em todos os runs, ao fim de um TTL, porque a
	// primeira renovação chega sempre depois de o lease já ter expirado. Recusa-se aqui —
	// depois de TODAS as opções aplicadas, que é o único ponto onde os dois valores são
	// ambos conhecidos — no mesmo molde de [breaker.ErrProgressSourceInert]: uma cablagem
	// que não pode funcionar não arranca em silêncio.
	if cfg.hbInterval > 0 && cfg.hbInterval >= cfg.ttl {
		return nil, fmt.Errorf("%w: heartbeat=%s, ttl=%s", ErrLeaseHeartbeatNotBelowTTL, cfg.hbInterval, cfg.ttl)
	}
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
	// AOS-299: liga a autoridade de token às escritas fenceadas do ledger/checkpointer.
	//
	// A RECUSA É DE COMPOSIÇÃO, e é o que impede o fencing de ficar inerte em silêncio: se há
	// ledger durável mas não há autoridade, o nó hospedaria runs a escrever sem fencing sem que
	// nada o denunciasse. Recusa-se aqui, no arranque, em vez de o descobrir em produção.
	if node.Ledger != nil && node.fencingAuth == nil {
		return nil, ErrFencingAuthorityMissing
	}
	if node.fencingAuth != nil {
		node.fencingAuth.ligar(leases)
	}

	s := &NodeService{
		layouts:      novoRunsPorLayout(node.estadoDoProvider != nil, node.pedirAntesDeLer, node.media != nil),
		desfechos:    novoDesfechosDeRuns(),
		node:         node,
		assigner:     assigner,
		leases:       leases,
		logw:         cfg.logw,
		hbInterval:   hbInterval,
		completedCap: completedCap,
		// AOS-456 — tecto por-chamador. Copiado tal como veio: <= 0 mantém-se <= 0 e o banner
		// declara NÃO COMPOSTO.
		inFlightPerCaller: cfg.inFlightPerCaller,
		runs:              make(map[string]*runState),
		completed:         make(map[string]*runState),
		suspended:         make(map[string]*runState),
		sweepInterval:     sweepInterval,
		approvalTTL:       approvalTTL,
		sweepStop:         make(chan struct{}),

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
	// AOS-090/DEF-908 — DEMOÇÃO AUTOMÁTICA POR ANOMALIA no loop de serviço. Partilha o MESMO
	// sweepStop: o Shutdown pára-a com o mesmo close, e o encaminhador DRENA os trips já
	// enfileirados antes de sair (ver autonomiaAnomalias.correr). Só arranca quando o
	// controlador está composto (oráculo de autonomia ligado).
	if node.anomaliaAutonomia != nil {
		go node.anomaliaAutonomia.correr(s.sweepStop)
		s.log("democao automatica por anomalia (AOS-090/DEF-908): LIGADA — cada TRIP do disjuntor multi-sinal demove a CLASSE dos pares (classe,dominio) que o run tocou, para um nivel mais supervisionado, sem gate humano (ADR-014).")
	}
	// AOS-090/ADR-025 — PROMOÇÃO automática por fiabilidade medida no loop de serviço. Subscreve
	// os desfechos pos-efeito (taxa de erro) e as escaladas (override, proxy) e corre o Evaluate
	// periodico. Partilha o sweepStop: o Shutdown para-a e a subscrição e libertada pelo ctx.
	if node.fiabilidade != nil {
		go node.fiabilidade.correr(s.sweepStop)
		s.log("promocao automatica por fiabilidade (AOS-090/ADR-025): LIGADA — o controlador promove uma CLASSE (abaixo de L4; L4/L5 exigem dual-control assinado) quando a taxa de erro e o override-rate (fraccao das CALLS que precisaram de humano; recusas fora do denominador) ficam sob os limiares SUSTENTADOS na janela. So julga as classes provisionadas em AOS_AUTONOMY_LEVELS; a janela recomeca em qualquer mudanca de nivel (promocao, democao ou operador: um degrau por janela inteira no nivel novo); um permit sem desfecho ao fim de min(1h, janela/2) invalida a janela da classe (emenda de 2026-10-18 ao ADR-025). Aplica em-memoria e NAO sobrevive a reinicio: no arranque o par volta a base assinada (a chave do no partilha o disco do WORM, logo uma elevacao duravel sem assinatura seria forjavel).")
	}
	// AOS-496: o apagamento de um titular (`/dsar/erase` ou expiração por TTL) retira do registo em
	// memória os desfechos dos runs dele — ver desfecho_do_titular.go.
	node.titularApagado.subscrever(func(titular string) {
		if n := s.esquecerDesfechosDoTitular(titular); n > 0 {
			s.log("apagamento de titular (AOS-496): %d desfecho(s) retirado(s) do registo em memoria — o GET /runs/{id} desses runs passa a responder pelo log", n)
		}
	})

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

// imputadoA resolve A QUEM se imputa um run para efeitos do tecto por-chamador (AOS-456a): o
// `RequestedBy` quando existe — que é o principal que o gate soberano resolveu — e o `NHIID` em
// último recurso.
//
// A ORDEM É A FRONTEIRA DE SEGURANÇA, não uma preferência: sem gate soberano composto o `RequestedBy`
// vem vazio e cai-se no `NHIID`, que vem do CORPO do pedido e o chamador escolhe — um tecto sobre um
// valor auto-declarado contorna-se mudando-o. É por isso que o banner de arranque distingue TRÊS
// posturas em vez de duas; ver [ingressPostureBanner].
func imputadoA(goal agentruntime.Goal) string {
	if rb := goal.Principal.RequestedBy; rb != "" {
		return rb
	}
	return goal.Principal.NHIID
}

// submit é o Submit com o interruptor da RETOMA. resuming=true vem exclusivamente de
// [NodeService.Resume] e dispensa a recusa por suspensão — é precisamente o run suspenso
// que se está a re-hospedar, e o log continua a dizer `waiting_on_human` até o arranque o
// repor em `running`. Nenhuma outra via o liga.
// imputadoA devolve o SUBMISSOR a que um run é imputado para efeitos do tecto de concorrência
// por-chamador (AOS-456) — e a escolha não é cosmética.
//
// UM RUN-FILHO DE PLANO É IMPUTADO A QUEM PEDIU O PLANO, não a quem chama o nó. O `aos-orq` drena
// a fila de pedidos e submete TODOS os runs-filho sob o SEU principal: imputar ao chamador
// colapsaria os planos de todos os humanos num único tecto — o defeito deste ticket reaparecido
// noutra porta, e foi um dos achados medidos na revisão da tentativa 1 (dois humanos com planos
// distintos partilhariam o tecto do drenador).
//
// O `RequestedBy` serve para isto porque é DERIVADO PELO NÓ e não declarado: vem do
// `planrequest.submitted` lido do Event Store, sob reclamação viva do chamador e com a região a
// coincidir (AOS-439, `submissorDoPedido` → `pedido.Principal`). Nunca é um campo do corpo do
// `POST /runs`. E é o MESMO valor que o [agentruntime.Goal.Subject] já usa como titular dos dados
// de um run-filho, pela mesma razão (AOS-440): o dono do trabalho é quem o pediu.
//
// Vazio ⇒ o run não é trabalho de um plano, e o submissor é quem chama. Em modo LEGADO é sempre
// vazio (a derivação só corre com o gate soberano composto) — e é também aí que o tecto não está
// em vigor, pelo que as duas condições coincidem por construção.
func (s *NodeService) submit(ctx context.Context, goal agentruntime.Goal, resuming bool) error {
	return s.submitObservando(ctx, goal, resuming, origemDoPlano{}, nil, nil)
}

// SubmitObservando é o [NodeService.Submit] com um observador do Goal HOSPEDADO (AOS-506).
//
// `aoHospedar` é chamado UMA vez, em síncrono e antes de o run arrancar, com o Goal DEPOIS da
// ingestão, que minimiza o objectivo (AOS-208).
//
// NÃO É, DESDE O AOS-551, «o Goal tal como o serviço o entrega ao run»: o layout do run é fixado
// dentro do [NodeService.hostRun] (AOS-489) e com ele o selo das entradas do plano num layout por
// handle, que corre LÁ e não aqui. Num run da 1.8.0 com entradas, o observador vê os `Inputs` sem
// o `Handle` que o run vai ver. A consequência, seguida até ao consumidor, é a medição do AOS-506
// ([promptDaTentativaDifere] → [hashDaSementeCom] → `SeedTail`, que recusa entradas sem handle):
// ela lê «não consegui recalcular» como DIFERE, nunca como igual, pelo que o efeito é um
// `prompt_diferente` espúrio num run da 1.8.0 com entradas E aviso de nova tentativa — nunca uma
// diferença escondida.
//
// O AOS-556 FECHOU-O ONDE A MEDIÇÃO SE LÊ, e não aqui: a contagem continua a ser a conservadora
// (o que não se consegue comparar conta como diferente), e o LOG passa a dizer que não se
// conseguiu comparar, em vez de acusar a tentativa de ter mudado o pedido —
// [sementeSemReferencias]. O que NÃO se fez, e porquê: mover o selo para cá obrigava a fixar o
// layout antes da escolha do modelo, e chamar `aoHospedar` de dentro do `hostRun` corre com quem
// lê a semente depois do retorno. Não
// é chamado quando a submissão é recusada. Quem precisa de saber o que o run semeou (a medição do
// prompt de uma nova tentativa) lê-o daqui, e não do Goal que submeteu: os dois diferem sempre
// que o objectivo tem dados que a ingestão redige.
//
// O observador só lê: recebe uma cópia do Goal e nada do que faça muda o run.
func (s *NodeService) SubmitObservando(ctx context.Context, goal agentruntime.Goal, aoHospedar func(agentruntime.Goal)) error {
	return s.submitObservando(ctx, goal, false, origemDoPlano{}, nil, aoHospedar)
}

// submitDoPlano é o [NodeService.SubmitObservando] de um run NOVO que o `POST /runs` provou ser
// trabalho do pedido de plano `plano` (AOS-520). É a ÚNICA via por que a origem do plano chega à
// hospedagem: quem a compõe é o handler, do que o nó verificou no seu log, e ela fica no estado do
// run — não viaja no Goal nem no contexto. `aoHospedar` nil ⇒ sem observador.
func (s *NodeService) submitDoPlano(ctx context.Context, goal agentruntime.Goal, plano origemDoPlano, runsProdutores []string, aoHospedar func(agentruntime.Goal)) error {
	return s.submitObservando(ctx, goal, false, plano, runsProdutores, aoHospedar)
}

// submitObservando é o corpo do [NodeService.submit]; `aoHospedar` nil ⇒ sem observador. `plano` é
// a origem verificada do run num pedido de plano; o valor-zero em todas as vias menos a do
// `POST /runs` com o vínculo verificado.
func (s *NodeService) submitObservando(ctx context.Context, goal agentruntime.Goal, resuming bool, plano origemDoPlano, runsProdutores []string, aoHospedar func(agentruntime.Goal)) error {
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
		// sobrescreve o desfecho, nem finge que o lease é de outra réplica). Após a poda do
		// desfecho a recusa vem do log durável — ver (2-ter-bis) abaixo (AOS-457).
		s.mu.Unlock()
		return ErrRunAlreadyCompleted
	}
	// (2-ter) TECTO DE SUBMISSÕES NOVAS SIMULTÂNEAS POR-CHAMADOR (AOS-456). É a ÚLTIMA guarda de
	// propósito: ver [TestAOS456AAOrdemDasGuardasEidempotenciaDaReSUBMISSAO] para o que se quebra ao
	// movê-la para antes das guardas de estado do run.
	//
	// PORQUE AQUI, e não no handler. A verificação e a RESERVA têm de ser ATÓMICAS: se o handler
	// contasse e só depois submetesse, dois pedidos concorrentes do mesmo submissor veriam ambos
	// `count < tecto` e passariam ambos — um TOCTOU que torna o tecto uma sugestão. Sob este
	// mutex, contar e reservar são a mesma secção crítica, e o tecto é um tecto.
	//
	// PORQUE ISTO NÃO EXIGE REORDENAR NADA — a assimetria que faz o AOS-456 funcionar em
	// concorrência onde falhou em taxa (ver
	// `docs/reports/AOS-456-desenho-do-trade-off-de-ordem.md`):
	//
	//	um pedido RECUSADO não ocupa lugar nenhum;
	//	um pedido RECUSADO **gasta** um token de um balde.
	//
	// Por isso o tecto pode ser verificado TARDE, depois de a identidade estar resolvida, sem que
	// a recusa tenha custado nada partilhado a quem a sofreu ou a quem vem a seguir. A tentativa
	// de fazer o mesmo com um token-bucket falhou exactamente por não ter esta propriedade: o
	// token global era gasto antes de a etapa por-chamador decidir.
	//
	// RETOMA ISENTA (`resuming`): o `POST /runs/{id}/resume` re-hospeda um run que JÁ foi admitido
	// e que está suspenso à espera de um humano. Contá-lo aqui tornaria a retoma recusável pelo
	// tecto — e um run que não se pode retomar por causa de uma quota é um run preso, não um run
	// limitado. É a mesma isenção que o tecto GLOBAL já faz (a retoma não consulta `maxInFlight`).
	//
	// UM SUSPENSO NÃO OCUPA LUGAR, por coerência com o tecto global: `s.runs` só tem os em curso,
	// e um run que entra em `s.suspended` sai desta contagem. O residual está declarado no ticket:
	// um chamador pode acumular suspensos sem bater no tecto.
	if !resuming && s.inFlightPerCaller > 0 {
		if p := imputadoA(goal); p != "" {
			n := 0
			for _, r := range s.runs {
				if r.principal == p {
					n++
				}
			}
			if n >= s.inFlightPerCaller {
				s.mu.Unlock()
				return ErrCallerInFlightCeiling
			}
		}
	}
	rs := &runState{runID: runID, done: make(chan struct{}), principal: imputadoA(goal), titular: goal.Titular(), plano: plano, runsProdutores: runsProdutores}
	s.runs[runID] = rs // RESERVA — bloqueia duplicados enquanto adquirimos o lease
	s.mu.Unlock()

	// (2-bis) SUSPENSÃO DURÁVEL (AOS-021). O balde acima é um cache que um restart esvazia;
	// o log não. Sem esta consulta, re-submeter depois de um restart RECOMEÇARIA do zero um
	// run que está à espera de um humano — perdendo a trajectória e deixando o pendente e o
	// grant órfãos. FAIL-CLOSED: uma leitura que falha recusa a admissão, em vez de admitir
	// sobre um estado que não se conseguiu ler.
	//
	// (2-ter-bis) DESFECHO DURÁVEL (AOS-457). O mesmo raciocínio, para o fim do run: o
	// `completed` acima também é um cache — com poda FIFO, vazio depois de um restart, e só
	// desta réplica. Sem esta consulta, re-submeter um `run_id` cujo desfecho está no log
	// RE-EXECUTAVA-O: o `claimRunning` é no-op fora de `ready`, o `Runtime.Run` voltava a chamar
	// o modelo, e os `turn.recorded` da segunda execução eram deduplicados por `(run_id, step)` —
	// o ledger não os via, pelo que nem o tecto por-run nem a quota por principal os contavam.
	// Medido pela revisão do AOS-457: 20 re-submissões aceites, 20 chamadas ao modelo, gasto
	// contado 0. É o defeito que o (2-bis) fechou para os runs pausados, agora para os terminados.
	// A resposta é a do cache: [ErrRunAlreadyCompleted], que a API trata como re-submissão
	// idempotente.
	if !resuming {
		st, susp, serr := s.suspensaoDuravel(ctx, runID)
		if serr != nil {
			s.unreserve(rs)
			return fmt.Errorf("aos: ler o estado duravel do run %q: %w", runID, serr)
		}
		if susp {
			s.unreserve(rs)
			return ErrRunSuspended
		}
		if desfechoDuravelRegistado(st) {
			s.unreserve(rs)
			return &desfechoDuravelError{runID: runID, estado: st}
		}
		// (2-ter-ter) UM `run_id` QUE JÁ EXISTE (AOS-520). O estado durável que se acabou de ler não é
		// o inicial, não é suspenso e não é terminal: o run está EM CURSO — deu turnos, ou foi
		// reclamado, e o nó caiu. Hospedá-lo por esta via é uma retoma por outro nome, e tem o
		// contrato da retoma: o Goal é RECONSTITUÍDO do registo de retoma e, se este nó não o pode
		// continuar com o que está selado — ou se o corpo não é o que está selado —, a submissão é
		// recusada AQUI, antes de hospedar: sem lease, sem quota, sem uma escrita. O run fica como
		// estava. É neste caminho, e só neste, que se lê a história do run; um run novo (estado
		// inicial) segue sem uma leitura a mais.
		//
		// O QUE A CONFERÊNCIA COBRE, CAMPO A CAMPO, está em `aos559_rehospedagem_confere_o_selo.go`
		// — e é lá que fica, porque é longo e tem uma razão medida por campo. Em resumo: o Goal sai
		// do selo; a `Credential` é a fresca do corpo; o `Objective`, os `Inputs` e o aviso da
		// tentativa são sobrepostos sem comparação (a ingestão redige, o selo substitui as
		// referências, o nó decide o aviso); tudo o resto que o corpo forneça e divirja RECUSA.
		// Até ao AOS-559 conferiam-se três campos — o alias, a marca e o digest — e o Goal era o
		// DO CORPO com esses três sobrepostos.
		//
		// Sem máquina de estados composta o estado vem vazio e o run é tratado como novo: esse nó
		// não tem como saber que o run existe (nem o sabia antes).
		if st != "" && st != state.Ready && s.node != nil {
			existente, gerr := s.node.goalDoRunExistente(ctx, goal)
			if gerr != nil {
				s.unreserve(rs)
				// O REMÉDIO É POR CAUSA, e não uma lista de três que só serve a um terço dos casos
				// (achado da revisão): as recusas do AOS-559 não se resolvem repondo um perfil, e
				// as do selo em falta não se resolvem de modo nenhum.
				s.log("POST de um run que ja existe RECUSADO antes de hospedar (AOS-520/AOS-559): run %q, estado duravel %q, causa=%s — nada foi escrito e o run fica como estava: %s: %v",
					runID, st, causaDoRunExistenteRecusado(gerr), remedioDoRunExistenteRecusado(gerr), gerr)
				return gerr
			}
			// AOS-559: `rs.principal` e `rs.titular` foram calculados do Goal DO CORPO, acima, e não
			// são recalculados aqui — nem precisam: o `Principal` e o `Subject` estão na classe
			// «recusado se divergir», pelo que um corpo admitido traz os do selo. Quem mexer nessa
			// classificação tem de mexer nisto, que é o que imputa a quota (2-quater) e o que
			// indexa o desfecho por titular.
			//
			// OS RUNS PRODUTORES SÃO DO CORPO E DESCREVEM AS ENTRADAS DO CORPO, que acabam de ser
			// descartadas — e [atravessarArestasDoPlano] indexa `goal.Inputs` POR eles. Com as
			// entradas a vir do selo as duas listas deixam de ser do mesmo ciclo, e o resultado não
			// é uma recusa de admissão: a travessia corre dentro do [NodeService.hostRun], cujo
			// defer SELA O RUN `failed` — um comprimento diferente sai como
			// `travessia_arestas_incoerentes` ([tCausaComposicao]), e um comprimento igual
			// re-atravessa entradas cujo conteúdo a primeira travessia já
			// RESOLVEU (deixou de ser uma referência) e recusa. Uma re-submissão LEGÍTIMA E FIEL
			// matava um run recuperável, que é o oposto do contrato desta via. A aresta foi
			// atravessada na PRIMEIRA hospedagem e o seu resultado está no selo: uma re-hospedagem
			// não a atravessa. É também a forma que as outras duas vias já têm (chegam com `nil`).
			// E AS ARESTAS QUE O CORPO DECLAROU FICAM ATRIBUÍDAS, pela mesma razão: a porta aceitou
			// e VALIDOU cada `inputs[].from_run`, e a re-hospedagem anula-os. Descartá-los em
			// silêncio era o aceitar-e-ignorar que esta conferência condena (achado da 2.ª
			// passagem da revisão).
			if declaradas := arestasDeclaradas(rs.runsProdutores); declaradas > 0 {
				s.log("POST de um run que ja existe (AOS-559): run %q — %d aresta(s) `inputs[].from_run` declarada(s) no corpo foram DESCARTADAS: a aresta foi atravessada na PRIMEIRA hospedagem e o resultado dela esta no selo, pelo que uma re-hospedagem nao a atravessa",
					runID, declaradas)
			}
			rs.runsProdutores = nil
			// A DIVERGÊNCIA DO OBJECTIVO FICA ATRIBUÍDA, porque é o único campo que o corpo fornece,
			// que alarga autoridade (entra no tail como CONFIÁVEL) e que esta conferência não pode
			// comparar — o do selo está redigido pela ingestão e o do corpo é cru. Sem esta linha a
			// única coisa silenciosa era exactamente a que mais interessa ver. Nem um nem outro
			// texto vai ao log: o objectivo é input do utilizador.
			//
			// «SELADO» E NÃO «O DO SELO TAL E QUAL»: a ingestão corre mais abaixo, sobre o Goal já
			// substituído, pelo que o run continua com o objectivo selado RE-INGERIDO — como nas
			// outras duas vias, que também o re-ingerem. Sob a política de remoção isso é
			// byte-idêntico (o texto já está minimizado); com tokenização por-titular deixaria de o
			// ser, e aí o turno 1 remontaria outros bytes. Fica nomeado onde alguém o vai ler.
			// AS ENTRADAS DE MEDIA DO CORPO FICAM ATRIBUÍDAS, pela MESMA razão das arestas acima, e é
			// achado da revisão adversarial do AOS-561: a porta aceitou-as e CONFERIU-AS pelos bytes,
			// e a re-hospedagem substitui o Goal pelo selado — pelo que elas não são seladas e nenhuma
			// tool as verá. Descartá-las em silêncio, com um 201 na resposta, era o aceitar-e-ignorar
			// que esta conferência condena e que a recusa de 400 da porta invoca como razão. Nenhum
			// byte de conteúdo vai ao log: só a contagem.
			if n := len(goal.MediaInputs); n > 0 {
				s.log("POST de um run que ja existe (AOS-559/AOS-561): run %q — %d entrada(s) de media do corpo foram DESCARTADAS e NAO foram seladas: o run e re-hospedado com o Goal SELADO, e as entradas de media que ele tenha vieram da PRIMEIRA hospedagem", runID, n)
			}
			if existente.Objective != goal.Objective {
				s.log("POST de um run que ja existe (AOS-559): run %q — o `objective` do corpo NAO e o que esta selado e foi IGNORADO; o run continua com o objectivo SELADO (re-ingerido pela ingestao, como nas outras duas vias). Os dois nao sao comparaveis nesta rota — a ingestao redige o selado e o do corpo e cru (AOS-091/AOS-208) —, pelo que a divergencia e registada e nao recusada", runID)
			}
			goal = existente
			rs.existente = true
		}
	}
	if resuming {
		rs.existente = true
	}

	// (2-quater) QUOTA DE DESPESA POR PRINCIPAL (AOS-457). Reserva o tecto por-run inteiro contra a
	// quota mensal do principal, no Event Store — por isso FORA do mutex, com a reserva em `s.runs` a
	// segurar o `run_id` e `unreserve` a desfazê-la se falhar. Depois das guardas de estado, pela
	// mesma razão do tecto por-chamador: um pedido recusado por elas não deve gastar quota.
	//
	// RETOMA ISENTA: o run já reservou quando foi admitido; reservar de novo na retoma contaria o
	// mesmo run duas vezes. É também por isto que a reserva é idempotente por `run_id`.
	//
	// FAIL-CLOSED: uma leitura do registo da quota que falha recusa a admissão.
	if !resuming && s.node != nil && s.node.QuotaPorPrincipal != nil {
		if qerr := s.node.QuotaPorPrincipal.reservar(ctx, rs.principal, runID); qerr != nil {
			s.unreserve(rs)
			return qerr
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
	// O EMISSOR DA RETOMA (AOS-292) é outro desses valores, e pela mesma razão: o
	// `SteerChannel.Resume` que levanta a pausa corre em `hostRun`, sob o runCtx, e exige o
	// emissor ASSINADO que só existe no corpo do pedido. Perdê-lo aqui faria a retoma de um
	// run pausado recusar sempre — e recusar é o lado seguro, mas seria uma recusa por
	// acidente de plumbing, não por decisão. Re-anexa-se só este valor, como o plano.
	if em, ok := resumeEmitterFrom(ctx); ok {
		runCtx = withResumeEmitter(runCtx, em)
	}
	// IDENTIDADE POR-RUN DO MODELO (AOS-278, CUTOVER DURO). O token NHI do RUN
	// (goal.Credential) — o MESMO que cada tool call mediada verifica — anexa-se ao runCtx
	// PRÓPRIO do run para o caminho do modelo o apresentar ao estágio authn REAL do GW.
	// Anexa-se aqui, no runCtx que sobrevive ao retorno de Submit (o ctx de entrada e os seus
	// valores morrem com o pedido), a par do plano de replay. Vazio ⇒ não anexa: o estágio
	// authn nega atribuívelmente (sem principal forjado). Cobre a RETOMA — o Resume repovoa
	// goal.Credential com a credencial fresca antes de re-submeter.
	runCtx = withModelCredential(runCtx, goal.Credential)
	// OFERTA DE TOOLS DO RUN (AOS-486). A lista-branca do run viaja no mesmo runCtx para o
	// adaptador do gateway enviar ao modelo só o schema das tools que o run pode chamar. nil ⇒
	// não anexa e o pedido fica como sempre foi; vazia anexa-se e não oferece nenhuma. Este é o
	// ponto comum à submissão, à retoma e ao crash-resume, que chegam aqui com o goal do registo
	// de retoma (e esse leva a lista).
	runCtx = withRunToolAllowlist(runCtx, goal.AllowedTools)
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
		// AOS-208: sob RemoveAllPolicy (minimização, sem tokenização) `subject` só alimenta o
		// SubjectID do registo de audit; ver o aviso de crypto-shredding em
		// integration.IngestObjective antes de habilitar tokenização por-titular.
		//
		// AOS-440: é o TITULAR DOS DADOS ([agentruntime.Goal.Titular]) — o submissor, num run
		// filho de um plano; o principal do run nos outros, como até aqui. O agente (quarto
		// argumento) continua a ser o principal.
		subject := goal.Titular()
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

	if aoHospedar != nil {
		aoHospedar(goal) // AOS-506: o Goal que o run vai ver, já com o objectivo minimizado
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

	// AOS-299: o token do lease entra no CONTEXTO do run. É daqui que o Event Store fenceado o
	// lê em cada escrita do step-ledger e do checkpointer — o ledger é composto UMA vez e
	// partilhado por todos os runs, pelo que o token não pode viver nele. Mesmo idioma de
	// [durable.ContextWithTitular], e pela mesma razão.
	//
	// AQUI, E NÃO MAIS ABAIXO: a goroutine do heartbeat (logo a seguir) fecha sobre a VARIÁVEL
	// `ctx`, não sobre o seu valor. Reatribuí-la depois de a goroutine arrancar é uma corrida de
	// dados — foi assim que a primeira versão desta linha ficou, e foi o `-race` que a apanhou.
	ctx = durable.ContextWithFencingToken(ctx, rs.lease.Token)

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
	// AOS-293: e a projecção do CANAL DE CONTROLO, pela mesma razão e no mesmo instante.
	//
	// O log de controlo é durável — `control.pause`, `control.steer`, `control.resume` e
	// `control.correction_consumed` são todos escritos — mas `SteerChannel.Rebuild` não tinha
	// um único chamador de produção. Depois de um reinício, `c.runs` está vazio e o
	// `control/doc.go` prometia o contrário: «um crash em paused → um worker novo relê o log e
	// recupera a correcção INTACTA».
	//
	// POR HOSPEDAGEM, E NÃO NO ARRANQUE, apesar de a AC do ticket dizer «no arranque». Três
	// razões, todas verificadas: (a) a varredura de arranque SALTA deliberadamente os runs
	// `paused` (crash_resume.go trata só `state.Running`), pelo que um rebuild no arranque não
	// tocaria no caso que interessa; (b) o padrão do repositório é este — o `RebuildLedger`
	// acima, a `state.Machine` no `Open` abaixo, e o `RunToolSets.Rebuild`, que foi fechado
	// exactamente assim depois de ter estado sem chamador; (c) um rebuild global no arranque
	// exigiria reler todos os streams por inteiro DUAS vezes, e não cobriria os runs
	// hospedados depois.
	//
	// TEM DE SER ANTES do `resumeIfWaiting` (abaixo): desde AOS-292, retomar uma pausa passa
	// por `SteerChannel.Resume`, que RECUSA quando não há pausa pendente na projecção. Com a
	// projecção vazia após restart, um run pausado deixava de se conseguir retomar de todo —
	// e isso é uma regressão que AOS-292 introduziu e que este ticket fecha.
	//
	// FAIL-CLOSED como os vizinhos: um log de controlo corrupto não deixa o run correr com uma
	// projecção que não representa o que foi decidido.
	//
	// CUSTO, declarado porque não é óbvio: o `Rebuild` relê o stream INTEIRO do run e filtra os
	// quatro tipos `control.*` em memória — o store não filtra por tipo. Com o `RebuildLedger`
	// logo acima a fazer a sua própria leitura completa, são DUAS passagens sobre todos os
	// eventos do run a cada hospedagem, e o custo cresce com a idade do run. É aceitável porque
	// uma hospedagem é rara (arranque de run ou retoma), mas quem investigar uma retoma lenta
	// num run antigo tem de encontrar isto escrito, e não a descobri-lo com um profiler.
	if s.node.Steer != nil {
		if err := s.node.Steer.Rebuild(ctx, rs.runID); err != nil {
			s.mu.Lock()
			rs.err = fmt.Errorf("aos: rebuild da projeccao do canal de controlo do run %q (AOS-293): %w", rs.runID, err)
			s.mu.Unlock()
			return
		}
	}
	// AOS-290: e à SAÍDA da hospedagem, larga-se o que se acabou de repor. O mapa do ledger é
	// PARTILHADO por todos os runs do nó e nunca era podado — crescia linearmente com
	// Σ(runs × passos), sem patamar, num processo de vida longa.
	//
	// O `defer` fica AQUI, colado ao rebuild, e não junto aos de `breakers`/`progress`: aquele
	// bloco corre só com `stateGates` composto, e `stateGates` e `Ledger` são compostos
	// INDEPENDENTEMENTE — a poda teria deixado de correr precisamente numa composição
	// degradada, que é onde ninguém iria procurá-la. Aqui é simétrica com a reposição, que é
	// também o que a torna segura: o que se larga é relido do Event Store se o run voltar.
	//
	// O que NÃO volta é o de um titular apagado — o `Rebuild` salta o que já não decifra. Essa
	// assimetria não é um defeito: é o Art. 17 a funcionar.
	if s.node.Ledger != nil {
		defer s.node.Ledger.ForgetRun(rs.runID)
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
			// AOS-254: o titular acompanha o selo — a saga de rollback que um desfecho `failed`
			// aciona corre no step-ledger cifrado por-titular, que o exige. AOS-440: o titular
			// dos DADOS (goal.Titular()), o mesmo sob o qual o step-ledger selou os outputs.
			selado := s.sealTerminalState(rs, goal.Titular(), res, err, r != nil)
			// AOS-493: o run conta-se pelo estado em que o log durável ficou, e pela razão do
			// veredicto.
			s.desfechos.contar(selado, res)
			// AOS-520 (PR 2): e pelo MODELO em que o run ficou — o alias do Goal, nunca o que o
			// fornecedor declarou. Só com um candidato a servir; sem ele não há séries e nada soma.
			s.node.seriesDoModelo().contarDesfecho(goal.Model.ModelID, selado, res)
			// AOS-521: o desfecho de um run do CANDIDATO vai para a janela do disjuntor, no livro
			// durável. Só com o disjuntor ligado; um run do titular nunca lá chega.
			s.observarDesfechoNoDisjuntor(rs.runID, goal, selado, res, err, r != nil)
			// E só AGORA se diz o estado durável de um run não cumprido: depois de o selo ter
			// sido tentado, e com o que ele devolveu (revisão M3).
			s.dizerNaoCumprido(rs.runID, res, selado)
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
			if err := gate.resumeIfWaiting(ctx, func(c context.Context) error {
				return s.retomarPausaPeloCanal(c, rs.runID, gate)
			}); err != nil {
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
	// AOS-396: o modelo pedido entra no Goal ANTES do registo de crash-resume e do primeiro
	// turno, pelo que o manifesto de cada turno, o span `chat` e a admissão falam do mesmo
	// modelo. Esta é a via única por onde passam a submissão, a retoma e a varredura de
	// crash-resume.
	//
	// AOS-520 (ADR-041 §2.4): com um candidato a servir, é AQUI que o modelo de um run novo é
	// escolhido — uma vez, pelo livro do canary — e gravado, em fail-closed, antes do registo de
	// retoma e do primeiro turno. «Novo» é o run que o LOG diz não ter história; um run que já deu
	// turnos ou já tem registo de retoma fica com o que está selado, venha por que via vier. Sem candidato a servir é o [Node.fixarModelo] de sempre. Uma
	// recusa fecha o run `failed` sem dar um turno: nunca é servido pelo titular «na dúvida».
	var ferr error
	if goal, ferr = s.escolherModeloDoRun(ctx, rs, goal); ferr != nil {
		err = fmt.Errorf("aos: run %q: %w", rs.runID, ferr)
		s.mu.Lock()
		rs.err = err
		s.mu.Unlock()
		s.log("run %q RECUSADO pela fixacao do modelo (AOS-520): causa=%s — o run fecha failed (%s) sem dar um turno; nao e servido pelo titular: %v",
			rs.runID, causaDaFixacaoDoModelo(ferr), reasonModelPinRefused, ferr)
		return
	}
	// AOS-489: o LAYOUT de montagem do prompt fica fixado no Goal ANTES do registo de retoma e
	// do primeiro turno, pela mesma razão do modelo: o que o registo guarda, o que o runtime
	// monta e o que o manifesto de cada turno grava são o mesmo valor. Um run NOVO chega aqui
	// sem versão e fica na dos runs novos; um run RETOMADO (aprovação ou crash-resume) chega
	// com a do seu registo de retoma ([integration.ResumeRecord.GoalWith]) e não é tocado.
	//
	// AOS-514: com a captura do estado opaco do provider ligada, o layout dos runs novos deste
	// nó é a 1.5.0 (a de sempre mais o rótulo `state_digest`); desligada, é a de sempre.
	//
	// PEDIR ANTES DE LER (ADR-034 §4): com o interruptor ligado, um run novo com `plan_input` fica
	// na 1.6.0, em que os `plan_input` só entram depois do primeiro turno. Os outros não mudam.
	layoutNovo := layoutDoRunNovo(s.node.estadoDoProvider != nil, s.node.pedirAntesDeLer, s.node.media != nil, goal)
	if goal.AssemblyVersion == "" {
		goal.AssemblyVersion = layoutNovo
	}
	goal = fixarLayout(goal)
	// AOS-513: a VERSÃO DA PROJECÇÃO que o perfil da rota do run declara fica fixada no Goal no
	// mesmo ponto e pela mesma razão. Sem perfil que a declare — todos os de hoje — o Goal fica
	// sem versão e a projecção é a do interruptor do nó, como sempre. Um run retomado traz a do
	// seu registo, ou a marca de que começou sem nenhuma, e não é tocado.
	goal = s.node.fixarProjeccaoDoRun(goal)
	// AOS-520: o DIGEST DO PERFIL DA ROTA em vigor para o modelo do run fica fixado no Goal no
	// mesmo ponto — depois do modelo, antes do registo de retoma e do primeiro turno. Só um nó com
	// perfis de rota assinados fixa; sem eles o Goal fica como está. Um run retomado traz o do seu
	// registo, ou a marca de que começou sem fixação, e não é tocado: quem o confere com o perfil
	// em vigor é a retoma (antes de chegar aqui) e o adaptador do gateway (em cada turno).
	goal = s.node.fixarPerfilDoRun(goal)
	// AOS-493: o MODO DE APLICAÇÃO do veredicto de conclusão fica fixado no Goal no mesmo ponto
	// e pela mesma razão: o registo de retoma, o runtime e o manifesto de cada turno têm o
	// mesmo valor. Um run novo fica no modo do nó; um retomado traz o do seu registo.
	goal = s.node.fixarConclusao(goal)
	// O LAYOUT EM USO, À VISTA (AOS-489). Conta-se cada hospedagem pelo seu layout, e diz-se em
	// voz alta quando NÃO é o dos runs novos: é o caso de um run começado antes desta versão e
	// re-hospedado agora (o registo de retoma não tem o campo, ou tem o layout antigo) — o run
	// que continua a ver o prompt sem a sua própria tool call. Sem isto só o WAL o dizia.
	s.layouts.contar(goal.AssemblyVersion)
	if goal.AssemblyVersion != layoutNovo {
		s.log("run %q hospedado no layout de prompt %s, e nao no dos runs novos (%s): o layout e fixado por run e veio do registo de retoma (AOS-489) — o run continua no layout em que os seus turnos foram gravados",
			rs.runID, goal.AssemblyVersion, layoutNovo)
	}
	// AOS-551: o PRODUTOR do `plan_input` por handle. ANTES do registo de retoma, que leva os
	// `Inputs` e é por isso que as referências sobrevivem à retoma sem serem re-seladas. Fora de
	// um layout por handle não toca em nada. Fail-closed: sem selo o run morria no primeiro tail.
	//
	// DEPOIS do observador do Goal hospedado, e deliberadamente — mas não pela razão que esta linha
	// dizia primeiro. O selo precisa do LAYOUT fixado, e o layout é fixado aqui, depois da escolha
	// do modelo; é essa a razão estrutural de ele não poder subir para o observador, que corre
	// noutra goroutine antes desta. O argumento «uma recusa da fixação do modelo deixaria handles
	// selados de um run que nunca arranca» NÃO distingue as duas posições, e dizê-lo era medir um
	// custo que as duas têm por igual: essa mesma janela já existia aqui, pelos portões do
	// [agentruntime.Runtime.Run] logo a seguir — e o AOS-557 fechou-a para os dois que eram
	// accionáveis por quem submete, correndo-os antes do selo; ver o bloco do selo, abaixo. O que o
	// observador deixa de ver está fechado onde a medição se lê — ver
	// [NodeService.SubmitObservando] e [sementeSemReferencias].
	//
	// E DEPOIS DE CADA SELO HAVER UM `handle.created`: uma entrada que recuse com as anteriores já
	// seladas DESFAZ os bytes delas (AOS-556, em [selarEntradasDoPlano]), mas o `handle.created`
	// de cada uma fica — o stream é append-only e não há evento de compensação. Uma declaração de
	// um run que nunca corre é inofensiva: o que ela declarava deixou de existir e nenhum run a
	// pode usar.
	//
	// OS PORTÕES ACCIONÁVEIS POR QUEM SUBMETE JÁ CORRERAM (AOS-557, logo acima): o contrato de
	// conclusão, a origem declarada da saída, o modo do veredicto que os precede e as
	// pré-condições do Goal (id do run e principal) são função só do Goal e da oferta do nó, e
	// recusavam DEPOIS deste selo — um run condenado deixava aqui handles cunhados e bytes que
	// ninguém apaga. Não se desfazia, e a razão continua a ser dirimente: o registo de retoma é
	// gravado a seguir, pelo que apagar deixaria um run RETOMÁVEL cujas entradas não têm bytes —
	// perda silenciosa de capacidade em vez de bytes a mais, que é a troca errada.
	//
	// O QUE **NÃO** SUBIU, UM A UM, E A RAZÃO DE CADA (AOS-557). O [agentruntime.Runtime.Run] tem
	// SETE sítios de recusa antes do primeiro turno (o enunciado do ticket conta seis portões por
	// nome e deixou de fora dois: as pré-condições do Goal e o modo do veredicto). Subiram dois
	// desses sítios — o das pré-condições do Goal e o que decide o modo, o contrato e a origem.
	// Ficam quatro, e nenhum deles fica a dever bytes a este selo:
	//
	//   - A COMPOSIÇÃO DO RUNTIME (`rt.validate`, na metade que confere o cliente de modelo, o
	//     Reference Monitor e o gravador) é função do runtime e não do Goal, e o nó não tem por
	//     onde a perguntar. A metade que é só do Goal — id do run e principal — SUBIU, e subiu
	//     porque deixava resíduo: um Goal com `Subject` preenchido e `Principal.NHIID` vazio tem
	//     titular não vazio, pelo que a quarentena o aceita e este selo CUNHA antes de o `Run`
	//     recusar ([agentruntime.ErrNoPrincipal]). Inalcançável pela porta HTTP, alcançável por um
	//     `Submit` programático — ver [agentruntime.RefuseAtStartup].
	//   - A SEMENTE DO TAIL (`abrirTailDoRun`) NÃO PODE subir, e é o único caso em que «não pode»
	//     quer dizer isso: num layout por handle ela EXIGE a referência de cada entrada
	//     ([agentruntime.ErrPlanInputWithoutHandle]), que é precisamente o que este selo produz.
	//     Corrê-la antes recusaria todos os runs. Ela leva DENTRO um segundo portão que é só do
	//     Goal — o vocabulário do aviso de nova tentativa (AOS-506) —, e esse podia subir; não
	//     subiu porque o `RetryNotice` é escrito pelo nó de um vocabulário fechado e não por quem
	//     submete, pelo que nenhuma submissão o acciona.
	//   - O LAYOUT (`layoutDoRun`) não deixa resíduo, e isso é demonstrável: pergunta o layout ao
	//     MESMO `layoutFor` a que [agentruntime.HasPlanInputsByHandle] pergunta, com a MESMA versão
	//     (o Goal chega aqui com ela fixada). Uma versão que ele recuse não é um layout por handle,
	//     e este selo sai sem selar nada.
	//   - O PORTÃO DA AUTORIDADE POR ARGUMENTO é INALCANÇÁVEL na composição deste nó: a
	//     `PerArgumentAuthority` é declarada incondicionalmente e a cadeia de reescrita do efeito
	//     inclui sempre a `reescritaDaAutoridade`, pelo que o `emCadeia` nunca devolve nil e as
	//     duas condições que ele exige estão sempre satisfeitas. É a declaração do KERNEL, para uma
	//     composição que não é a nossa.
	//   - A JANELA (`openWindow`) é função da composição do [agentruntime.Runtime], que o nó não
	//     vê: a fábrica de janelas é privada dele. Subir esta exigiria o runtime expor se a sua
	//     janela abre para este Goal, e isso é mudar a porta do kernel por um portão que no nó
	//     composto nunca recusou — não se faz aqui.
	//
	// E UMA PRECISÃO SOBRE «SEM UM ÚNICO EFEITO», que vale para o kernel e não para o caminho do
	// nó: o [integration.SecuredRuntime.Run] PERSISTE o snapshot do tool set antes de chamar o
	// `Runtime.Run`, pelo que os portões do kernel recusam depois dessa escrita durável. A
	// conferência de cima evita-a também. O que ela não evita é o que corre ANTES dela nesta
	// função — o claim `ready→running` da máquina de estados, que é a primeira escrita durável
	// desta hospedagem.
	//
	// A JANELA DO CRASH CONTINUA ABERTA, e o AOS-557 **não** a fecha: o processo morrer entre estes
	// selos e o registo de retoma deixa handles cunhados e bytes sem run que os use, e não há
	// `defer` que sobreviva a isso. É outro problema, com outra forma — precisa de uma passagem que
	// limpe o espaço de um run sem história —, e tem ticket próprio: o reaper por run, **AOS-554**.

	// AOS-553: A TRAVESSIA DA ARESTA DO PLANO POR HANDLE (ADR-045). Uma entrada que o `aos-orq`
	// DECLAROU como aresta (`inputs[].from_run`) — o que ele faz quando o run produtor correu num
	// layout por handle e o kernel designou a própria referência como origem da saída — resolve-se
	// aqui, do armazém do run produtor, sob as seis conferências de `travessia_da_aresta.go`. A
	// porta é a aresta declarada e NUNCA a forma dos bytes: um texto com forma de referência não
	// decide nada, e o porquê está no corpo daquela função.
	//
	// ANTES DO SELO, e tem de ser: é o selo a seguir que põe estes bytes no espaço DESTE run e lhes
	// cunha o token que o tail da 1.8.0 mostra. Resolver depois selava a referência como se fosse
	// conteúdo — o lixo silencioso que este ticket fecha.
	//
	// E DEPOIS DA FIXAÇÃO DO LAYOUT, embora não dependa dela: a travessia é a mesma em qualquer
	// layout, porque quem decide se há referência a resolver é o run PRODUTOR e não este. Fica
	// junto do selo para as duas metades da aresta se lerem no mesmo sítio.
	//
	// NÃO ACRESCENTA JANELA NENHUMA à que o bloco abaixo declara: a travessia só LÊ do armazém do
	// produtor, pelo que uma recusa dela não deixa bytes atrás de si.
	//
	// E CORRE ANTES DA CONFERÊNCIA DE ARRANQUE (AOS-557, a seguir), de propósito: pô-la depois
	// mudava a causa que um run com a aresta E o contrato maus reporta, sem mudar o desfecho. Como
	// não deixa bytes, a ordem entre as duas não muda o resíduo de ninguém.
	var terr error
	if goal, terr = atravessarArestasDoPlano(ctx, s.node.planoHandles, s, rs.plano.pedido, rs.runsProdutores, goal); terr != nil {
		err = fmt.Errorf("aos: run %q: %w", rs.runID, terr)
		s.mu.Lock()
		rs.err = err
		s.mu.Unlock()
		s.log("run %q RECUSADO pela travessia da aresta do plano (AOS-553): uma entrada vem por referencia de handle e nao se resolveu — o run fecha failed sem dar um turno, em vez de o modelo receber um token que este run nao abre: %v", rs.runID, terr)
		return
	}

	// AOS-557: OS PORTÕES ACCIONÁVEIS POR QUEM SUBMETE CORREM AQUI, ANTES DO SELO. O contrato de
	// conclusão e a origem da saída são função só do Goal e da oferta do nó, e o kernel recusava-os
	// DEPOIS do selo — um run condenado deixava handles cunhados e bytes cifrados que ninguém
	// apaga. É a MESMA função que decide nos dois sítios, contra a mesma regra; o que difere é a
	// oferta, e porque é fiel (e porque isto só corre no caminho de ARRANQUE) está em
	// [NodeService.recusaAntesDoSelo]. O portão do `Run` MANTÉM-SE: é ele a garantia.
	if rerr := s.recusaAntesDoSelo(ctx, rs, goal); rerr != nil {
		// SEM EMBRULHO, ao contrário dos vizinhos deste bloco, e a razão é dirimente: este erro é
		// servido em TEXTO no `GET /runs/{id}` (`resp.Error`), e até este ticket o texto de uma
		// recusa de arranque era o erro NU que o `Run` devolvia. Acrescentar-lhe um prefixo
		// mudaria a resposta a quem submete por uma recusa que é a mesma. Os vizinhos embrulham
		// porque a recusa deles não tem um «antes» com que ter de coincidir.
		err = rerr
		s.mu.Lock()
		rs.err = err
		s.mu.Unlock()
		// A CAUSA VEM NO ERRO, e a linha não a adivinha: a conferência recusa pelo contrato de
		// conclusao, pela origem da saida ou porque a oferta do no nao se leu — tres causas, e
		// afirmar uma delas no cabecalho seria atribuir mal duas em cada tres.
		s.log("run %q RECUSADO pela conferencia de arranque ANTES do selo (AOS-557): o run fecha failed sem dar um turno e SEM selar entrada nenhuma — o portao do kernel recusava-o igualmente, mas depois de os handles estarem cunhados: %v", rs.runID, rerr)
		return
	}

	// AOS-561: AS ENTRADAS DE MEDIA SELAM PRIMEIRO, e a ordem e um CONTRATO: o nome que o no
	// cunha e `<tool>-<ordinal>` com o ordinal GLOBAL do run, pelo que selar a media antes das
	// entradas do plano da-lhe nomes previsiveis pela ordem de submissao — que e como o OBJECTIVO
	// do run as pode nomear. Selar depois fazia o nome depender de quantas entradas de plano
	// houvesse. Ver [selarMediaDoPedido].
	// E O INTERRUPTOR DO OPERADOR VALE NO CAMINHO EM-PROCESSO TAMBÉM (achado da revisão
	// adversarial): a porta HTTP recusa `media_inputs` num nó com a via desligada, mas um
	// `Submit`/`SubmitObservando` não passa pela porta. Sem esta guarda, `AOS_ENTRADA_DE_MEDIA=off`
	// não impedia nada por essa via — e um interruptor que só metade dos caminhos respeita não é um
	// interruptor.
	//
	// A GUARDA PERGUNTA «VAI SELAR?», E NÃO «TEM MEDIA?», e a primeira versão perguntava a segunda —
	// o que a tornava o molde exacto da «correcção ingénua» que o AOS-557 declara ser pior do que o
	// defeito. Um run hospedado com a via LIGADA e retomado depois de o operador a desligar chega
	// aqui com as entradas JÁ SELADAS, do seu registo de retoma: recusá-lo matava um run
	// RECUPERÁVEL, e o interruptor passava a alcançar o que está em voo. O interruptor governa
	// ADMITIR conteúdo novo, não CONTINUAR um run — a mesma regra que o `AOS_CONTEUDO_POR_HANDLE`
	// já declara («runs retomados não mudam: continuam no layout do seu registo»). É por isso a
	// MESMA pergunta que o âmbito dos portões do arranque faz ([haEntradasPorSelar]): vai ser
	// escrito algum byte novo?
	if mediaPorSelar(goal) && !s.node.entradaDeMedia {
		err = fmt.Errorf("aos: run %q: %w", rs.runID, errMediaViaDesligada)
		s.mu.Lock()
		rs.err = err
		s.mu.Unlock()
		s.log("run %q RECUSADO: tem entradas de media e este no nao habilita a via (AOS_ENTRADA_DE_MEDIA desligada, ou sem AOS_HANDLE_DIR) — o run fecha failed sem dar um turno e sem selar nada", rs.runID)
		return
	}

	var merr error
	if goal, merr = selarMediaDoPedido(ctx, s.node.planoHandles, goal); merr != nil {
		err = fmt.Errorf("aos: run %q: %w", rs.runID, merr)
		s.mu.Lock()
		rs.err = err
		s.mu.Unlock()
		s.log("run %q RECUSADO pela selagem das entradas de media (AOS-561): o run fecha failed sem dar um turno e sem deixar bytes no espaco de handles: %v", rs.runID, merr)
		return
	}

	var herr error
	if goal, herr = selarEntradasDoPlano(ctx, s.node.planoHandles, goal); herr != nil {
		err = fmt.Errorf("aos: run %q: %w", rs.runID, herr)
		s.mu.Lock()
		rs.err = err
		s.mu.Unlock()
		s.log("run %q RECUSADO pela selagem das entradas do plano (AOS-551): o run fecha failed sem dar um turno — sem handle o tail do primeiro turno recusaria (ErrPlanInputWithoutHandle): %v", rs.runID, herr)
		return
	}
	s.persistCrashResumeRecord(ctx, goal)

	// AOS-520: o alias de um run FIXADO POR ESCOLHA vai no contexto com que o runtime é chamado, para
	// o despachante escolher o adaptador desse alias ([clientePorModelo]). Um run sem a marca não
	// anexa nada, e é servido pelo adaptador do titular, como sempre. Numa variável NOVA: a goroutine
	// do heartbeat fecha sobre `ctx`, que não se reatribui (ver o cimo desta função).
	ctxDoRun := ctx
	if goal.ModelPinnedBy != "" {
		ctxDoRun = withModeloDoRun(ctx, goal.Model.ModelID)
	}
	res, _, err = s.node.Runtime.Run(ctxDoRun, goal, nil)

	// DEGRADAÇÃO DECLARADA POR ORÇAMENTO (AOS-260): o run parou porque a admissão do turno de
	// modelo negou headroom, SEM erro e SEM resposta final. Sem esta linha o desfecho seria
	// legível apenas no log durável (`timed_out`/`budget_exhausted`, ver [runGate.sealTerminal])
	// e o operador que olha para o log do processo veria um run a acabar em silêncio.
	//
	// Este caminho é o do prompt de exaustão DESARMADO: com ele armado o run não chega aqui
	// assim — foi suspenso e o sinal viaja no `err`, absorvido mais abaixo.
	if res.BudgetExhausted {
		s.log("run %q PAROU por ORCAMENTO ESGOTADO (AOS-260) ao fim de %d turnos completos — %s. Estado duravel: timed_out (%s), NAO failed: um tecto defensivo atingido nao e uma falha recuperavel por compensacao. Para o trabalho prosseguir e preciso levantar o tecto (AOS_BUDGET_MAX_TOKENS / AOS_BUDGET_MAX_COST_MICRO_USD) e submete-lo com um run_id NOVO: este tem desfecho no log e a re-submissao do mesmo id e recusada (AOS-457)",
			rs.runID, res.Turns, res.BudgetExhaustionReason, reasonBudgetExhausted)
	}

	// O VEREDICTO DO KERNEL, À VISTA (AOS-493). Um veredicto negativo diz-se no log do processo
	// nos dois modos: em imposição o run fechou sem concluir; em observação concluiu como
	// sempre e esta linha é o que o modo de imposição teria feito dele.
	//
	// AQUI SÓ SE DIZ O OBSERVADO, que não afirma nada sobre o log durável. A linha do run NÃO
	// CUMPRIDO afirma um estado durável («failed») e por isso sai do `defer` do selo, depois de
	// ele correr ([NodeService.dizerNaoCumprido], revisão M3). Num nó sem máquina de estados
	// esse `defer` não existe, e a linha sai daqui a dizer que não há selo.
	if v := res.Verdict; v != nil && !v.Fulfilled && !res.Unfulfilled {
		s.log("run %q concluido com VEREDICTO NEGATIVO em observacao (AOS-493): %q, %d tool call(s) pedida(s), vector %s. O desfecho nao mudou; com AOS_COMPLETION_VERDICT=enforce este run terminava failed",
			rs.runID, v.Reason, v.ToolCallsRequested, vectorDoVeredicto(v))
	}
	if s.node.stateGates == nil {
		s.dizerNaoCumprido(rs.runID, res, "")
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
		if perr := s.putResumeRecord(ctx, goal); perr != nil {
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
	// AOS-440: o guard pergunta pelo TITULAR (goal.Titular()), que é sob quem o Put sela — igual
	// ao de sempre quando o run não tem Subject.
	if s.node == nil || s.node.ResumeRecords == nil || goal.Titular() == "" {
		return
	}
	if err := s.putResumeRecord(ctx, goal); err != nil {
		s.log("registo de retoma de arranque do run %q FALHOU (AOS-253) — o run corre, mas um crash a meio NAO sera retomavel pela varredura de arranque: %v", goal.RunID, err)
	}
}

// putResumeRecord projecta o Goal no registo de retoma e grava-o. É a ÚNICA via por que o nó
// escreve um registo de retoma: um Goal que [resumeRecordFromGoal] recuse (sem o modo do
// veredicto fixado) não chega ao Event Store.
func (s *NodeService) putResumeRecord(ctx context.Context, goal agentruntime.Goal) error {
	rec, err := resumeRecordFromGoal(goal)
	if err != nil {
		return err
	}
	return s.node.ResumeRecords.Put(ctx, rec)
}

// dizerNaoCumprido escreve no log do processo a linha de um run que acabou SEM CONCLUIR
// (veredicto negativo imposto, AOS-493), com o estado em que o log durável ficou DE FACTO.
// `selado` é o que [NodeService.sealTerminalState] devolveu: `failed` quando o selo foi escrito;
// vazio quando não foi (sem máquina, sem gate, ou a transição falhou — a linha anterior deste
// run diz qual); outro estado quando o selo foi no-op porque o run já estava fechado ou parado
// por outro condutor.
//
// Existe porque a linha dizia «Estado duravel: failed» ANTES de o selo correr (revisão M3): se
// ele falhasse, o log do processo tinha afirmado um facto durável que não existe.
func (s *NodeService) dizerNaoCumprido(runID string, res agentruntime.Result, selado state.State) {
	v := res.Verdict
	if !res.Unfulfilled || v == nil {
		return
	}
	cabeca := fmt.Sprintf("run %q NAO CUMPRIDO (AOS-493) ao fim de %d turno(s): veredicto do kernel %q, %d tool call(s) pedida(s), vector %s",
		runID, res.Turns, v.Reason, v.ToolCallsRequested, vectorDoVeredicto(v))
	switch selado {
	case state.Failed:
		s.log("%s. Estado duravel: failed (%s), sem texto final", cabeca, reasonRunUnfulfilled)
	case "":
		s.log("%s. O SELO NAO FOI ESCRITO: o log duravel NAO regista failed para este run (a causa esta na linha anterior). Sem texto final", cabeca)
	default:
		s.log("%s. O selo foi no-op: o estado duravel e %s, e NAO failed (%s) — o run ja tinha sido fechado ou parado por outro condutor. Sem texto final", cabeca, selado, reasonRunUnfulfilled)
	}
}

// fixarLayout escreve no Goal o layout de montagem do prompt em que o run fica FIXADO (AOS-489).
// Um Goal sem versão é um run NOVO: fica no layout dos runs novos ([agentruntime.AssemblyVersion]
// — o que o [agentruntime.Runtime] do nó usaria por omissão; o nó não o sobrepõe). Um Goal que já
// traz versão — o de uma retoma — fica como está: é isso que mantém em 1.3.0 um run começado
// antes desta versão.
func fixarLayout(goal agentruntime.Goal) agentruntime.Goal {
	if goal.AssemblyVersion == "" {
		goal.AssemblyVersion = agentruntime.AssemblyVersion
	}
	return goal
}

// resumeRecordFromGoal projecta o Goal no registo de retoma. A Credential é
// DELIBERADAMENTE omitida — ver [integration.ResumeRecord].
//
// O LAYOUT VAI SEMPRE EXPLÍCITO (AOS-489), mesmo que o Goal recebido ainda não o tenha fixado:
// um registo SEM versão é lido como «run anterior ao AOS-489» e retomado em 1.3.0
// ([integration.ResumeRecordLegacyAssemblyVersion]). Deixar a versão vazia aqui poria um run
// novo, com os primeiros turnos em 1.4.0, a continuar em 1.3.0 depois da retoma.
//
// O MODO DO VEREDICTO TEM DE VIR FIXADO (AOS-493, revisão M4). Um registo sem ele é lido como
// «run anterior ao AOS-493» e retomado SEM veredicto ([integration.ResumeRecord.ModoDeConclusao]).
// Ao contrário do layout, aqui não há valor por omissão que se possa escrever sem decidir pelo
// chamador: gravar `off` punha um run novo a retomar sem veredicto, em silêncio; gravar o modo
// do nó exigia conhecê-lo. Quem chama fixa-o antes ([Node.fixarConclusao], no `hostRun`); um
// Goal que chegue aqui sem modo é um defeito de cablagem e é recusado ([errResumeRecordSemModo]).
func resumeRecordFromGoal(goal agentruntime.Goal) (integration.ResumeRecord, error) {
	goal = fixarLayout(goal)
	if goal.CompletionMode == "" {
		return integration.ResumeRecord{}, fmt.Errorf("%w (run %q)", errResumeRecordSemModo, goal.RunID)
	}
	if _, err := agentruntime.ParseCompletionMode(string(goal.CompletionMode)); err != nil {
		return integration.ResumeRecord{}, fmt.Errorf("aos: registo de retoma do run %q: %w", goal.RunID, err)
	}
	return integration.ResumeRecord{
		RunID:             goal.RunID,
		Principal:         goal.Principal,
		Subject:           goal.Subject, // AOS-440: o titular dos dados sobrevive à retoma
		Scope:             goal.Scope,
		Model:             goal.Model,
		System:            goal.System,
		Tools:             goal.Tools,
		AllowedTools:      goal.AllowedTools,
		Inputs:            goal.Inputs,
		MediaInputs:       goal.MediaInputs, // AOS-561: já seladas e sem os bytes
		RetryNotice:       goal.RetryNotice, // AOS-506: a retoma semeia o tail com o mesmo aviso
		Skills:            goal.Skills,
		Objective:         goal.Objective,
		MemoryContext:     goal.MemoryContext,
		MaxTurns:          goal.MaxTurns,
		ParentTraceParent: goal.ParentTraceParent,
		AssemblyVersion:   goal.AssemblyVersion, // AOS-489: o run continua no layout em que começou
		// AOS-493: o contrato de conclusão e o modo do veredicto sobrevivem à retoma.
		CompletionRequires: goal.CompletionRequires,
		CompletionMode:     goal.CompletionMode,
		// AOS-497: a origem declarada da saída e o seu vínculo sobrevivem à retoma.
		OutputFromTool:      goal.OutputFromTool,
		OutputSourceBinding: goal.OutputSourceBinding,
		// AOS-513: a versão da projecção em que o run está fixado sobrevive à retoma. Um run
		// sem versão fixada grava o campo vazio — os bytes de sempre.
		ProjectionVersion: versaoDaProjeccaoParaORegisto(goal),
		// AOS-520: o digest do perfil da rota em que o run está fixado sobrevive à retoma. Um run
		// sem perfil fixado grava o campo vazio — os bytes de sempre.
		RouteProfileDigest: digestDoPerfilParaORegisto(goal),
		// AOS-520: a marca de modelo fixado por escolha sobrevive à retoma. Um run sem ela grava o
		// campo vazio — os bytes de sempre.
		ModelPinnedBy: goal.ModelPinnedBy,
	}, nil
}

// errResumeRecordSemModo — pediu-se o registo de retoma de um Goal sem o modo de aplicação do
// veredicto fixado. Fail-closed: o registo não é escrito. Ver [resumeRecordFromGoal].
var errResumeRecordSemModo = errors.New("aos: registo de retoma sem o modo do veredicto de conclusao fixado (Goal.CompletionMode vazio) — o Goal tem de passar por fixarConclusao antes de ser gravado; sem isso o run seria retomado sem veredicto, em silencio")

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

// liquidarQuota grava o consumo real do run contra a reserva da quota por principal (AOS-457).
// Chamada só quando o desfecho do run ficou registado no log durável: um run suspenso, pausado ou
// interrompido não terminou, e liquidá-lo libertaria a reserva de um run que ainda vai gastar.
// No-op sem quota composta.
func (s *NodeService) liquidarQuota(runID, principal string) {
	if s == nil || s.node == nil || s.node.QuotaPorPrincipal == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.node.QuotaPorPrincipal.liquidar(ctx, principal, runID)
}

// liquidarQuotaDoSuspenso liquida um run suspenso que foi terminado sem voltar a ser hospedado (o
// abort por exaustão, AOS-263). O principal vem do balde de suspensos; depois de um restart o
// balde está vazio e a reserva fica até ao fim do mês — declarado no ticket.
func (s *NodeService) liquidarQuotaDoSuspenso(runID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	rs, ok := s.suspended[runID]
	s.mu.Unlock()
	if ok {
		s.liquidarQuota(runID, rs.principal)
	}
}

// desfechoDuravelError é a recusa de re-submeter um run cujo desfecho está no LOG (AOS-457). É um
// [ErrRunAlreadyCompleted] — a API trata-o como re-submissão idempotente —, mas com a mensagem certa:
// a do cache fala de «desfecho retido nesta réplica», que aqui seria falso, e o operador precisa de
// saber que voltar a correr o trabalho exige um `run_id` novo.
type desfechoDuravelError struct {
	runID  string
	estado state.State
}

func (e *desfechoDuravelError) Error() string {
	return fmt.Sprintf("aos: run %q ja terminado (desfecho %q registado no log duravel) — re-submissao recusada; para voltar a correr o trabalho submeta-o com um run_id NOVO", e.runID, e.estado)
}

func (e *desfechoDuravelError) Unwrap() error { return ErrRunAlreadyCompleted }
