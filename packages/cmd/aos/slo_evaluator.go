package main

// AOS-274 (F8) — O PRODUTOR DE SLOs/ALERTAS EM RUNTIME.
//
// O DEFEITO. `BuildDashboard` (AOS-085), `EvaluateAlerts` (AOS-086), `DashboardCatalog.Render`
// (AOS-104) e `EvaluateOperationalAlerts` (AOS-105) estavam escritos, testados e completos — e
// NÃO tinham um único chamador fora de testes. Os runbooks de AOS-106 encaminhavam alertas que
// nenhum processo produzia, e o export OTLP levava traces (factos por-span) mas nenhuma
// AGREGAÇÃO com SLO avaliado. O resultado era um SLO que existia como documento e não como
// SINAL: ninguém no nó sabia dizer, em nenhum instante, se estava a cumpri-lo.
//
// O QUE ESTE FICHEIRO ACRESCENTA (e o que deliberadamente NÃO acrescenta):
//
//   - Um LAÇO PERIÓDICO no loop de serviço, no molde EXACTO de approval_sweeper.go e
//     retention_sweeper.go: um ticker que termina com o MESMO `sweepStop` fechado pelo Shutdown.
//   - Um avaliador COMPOSTO: as DUAS famílias de alerta corridas na MESMA passagem, sobre a
//     MESMA janela de dados — os 4 SLIs da MEDIAÇÃO (AOS-085/086) e os 7 SLIs CANÓNICOS
//     operacionais (AOS-104/105). Compostos, não fundidos: cada família mantém a sua config, a
//     sua janela sustentada e o seu vocabulário de alerta (os nomes não colidem — `..._op` no
//     lado operacional), porque fundi-las obrigaria a escolher um dos dois SLOs para os dois
//     SLIs que ambas observam e a decisão ficaria escondida numa fusão em vez de declarada.
//   - ZERO métricas novas. Nenhum SLI é recalculado aqui: agrega-se o que o substrato já sabe
//     derivar, sobre os spans que o nó já emitia. Escrever um segundo derivador seria criar uma
//     contabilidade paralela — o defeito que AOS-085 existe para não ter.
//
// # DE ONDE VÊM OS DADOS (todos REAIS; nada é fabricado)
//
//   - SPANS DO PRÓPRIO NÓ, pela torneira de slo_span_tap.go: os `execute_tool` (o ponto ÚNICO de
//     mediação — a sua latência É o overhead de mediação), os `chat` (que já carregam o custo do
//     turno, AttrCostMicroUSD) e o `aos.decision` de cada um (escalate ⇒ override-rate). Se o
//     Model Gateway e o pool de sandbox estiverem instrumentados no mesmo tracer, os seus
//     atributos (`aos.cache.hit_rate`, `aos.sandbox.cold_start_ms`) entram pelo MESMO caminho,
//     sem uma linha a mais aqui.
//   - SONDA DE PRONTIDÃO do plano de controlo, uma observação por tick: a MESMA condição que
//     `/readyz` responde (ver [NodeService.controlPlaneAvailable]). A disponibilidade é a fracção
//     de observações boas na janela — uma medição do nó sobre si próprio, não um valor injectado.
//   - VERIFICAÇÃO DA HASH-CHAIN do WORM ([Node.VerifyWORM], AOS-221): um `*audit.VerifyError` é
//     ADULTERAÇÃO (o SLI vai a 0 e o alerta encaminha para PROC-DR); um WORM opaco ou um erro de
//     I/O NÃO é — fica sem amostra. Ver [sloEvaluator.probeWORM].
//
// O que NÃO tem produtor no nó (headroom do scheduler, fidelidade de replay) fica sem amostras e,
// pela regra anti-vacuidade de AOS-085 (`Samples == 0` ⇒ nem breach nem cumprimento afirmado),
// NÃO dispara e NÃO se declara cumprido. Injectar um valor plausível para "preencher o painel"
// seria exactamente o alerta sintético que o critério de aceitação proíbe.
//
// # FAIL-OPEN — A EXCEPÇÃO DECLARADA DESTE NÓ
//
// Todo o resto deste binário é FAIL-CLOSED: uma config ilegível aborta o arranque, um selo
// recusado impede a acção, uma autoridade em falta nega. AQUI NÃO, e a inversão é deliberada:
//
//	A OBSERVABILIDADE NUNCA DERRUBA O NÓ.
//
// Porquê, em concreto: um avaliador fail-closed transformaria cada avaria da sua própria fonte
// numa avaria do nó. O Vault inalcançável durante a sonda, um WORM opaco, um anel de spans vazio,
// um bug de divisão por zero num percentil — tudo isso são coisas que se querem VER, e nenhuma é
// razão para deixar de servir runs. Um sistema que se auto-desliga porque não consegue medir-se é
// estritamente pior do que um que corre sem medida: perde as duas propriedades em vez de uma. É
// também a assimetria certa de risco — o custo de um alerta em falta é uma avaria diagnosticada
// mais tarde; o custo de um nó em baixo por causa do medidor é a avaria em si.
//
// Como está materializado (não é uma intenção, são quatro mecanismos):
//
//  1. PÂNICO CONTIDO. Cada passagem corre sob `recover` ([sloEvaluator.evaluateSafely]). Sem ele
//     um pânico numa goroutine derruba o PROCESSO INTEIRO — o modo de falha exacto que esta
//     declaração existe para excluir, e a razão de o recover aqui não ser zelo redundante.
//  2. SEM PROPAGAÇÃO. Nenhum caminho deste ficheiro devolve erro a quem executa runs; nada é
//     partilhado com a admissão nem com a mediação. Falhar a avaliar é registar e seguir.
//  3. SEM BLOQUEIO INDEFINIDO. Cada sonda corre com timeout próprio; o laço nunca segura um lock
//     do serviço nem espera por I/O sem prazo.
//  4. O LAÇO NÃO PÁRA. Ao contrário do de retenção (que PÁRA ao detectar a cadeia comprometida,
//     porque continuar a DESTRUIR sobre um log que já não se verifica seria pior), este continua:
//     não destrói nada, e uma adulteração detectada é precisamente quando o operador mais precisa
//     que o sinal continue a chegar.
//
// A FRONTEIRA da excepção: ela cobre a AVALIAÇÃO, não a CONFIGURAÇÃO. `AOS_SLO_EVAL_INTERVAL` /
// `AOS_SLO_WINDOW` malformadas ABORTAM o arranque ([ErrBadSLOEvalInterval]/[ErrBadSLOWindow]),
// como todas as outras variáveis do nó — um operador que pede uma cadência e fica com outra não
// tem forma de o notar, e essa classe de silêncio não é observabilidade a falhar: é config a
// mentir.
//
// # LIGAÇÃO AO REGISTO DE RUNBOOKS (AOS-106)
//
// Um alerta sem runbook é ruído. Cada alerta disparado é resolvido em
// [runbooks.Lookup] (o registo canónico) e a linha de log estruturado leva o TÍTULO, o CAMINHO
// DO DOC e o ADR de conformidade — o operador salta do sinal para o procedimento sem procurar. Um
// runbook que não resolve é gritado como `runbook_ORFAO` em vez de silenciosamente omitido: é o
// mesmo invariante que [runbooks.Validate] verifica no CI, aqui em runtime, onde a config pode
// ter sido substituída por uma do operador.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	audit "github.com/aos-ref/platform/audit"
	runbooks "github.com/aos-ref/platform/runbooks"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// DefaultSLOEvalInterval é a cadência por omissão da avaliação.
//
// UM MINUTO, o mesmo do varrimento de aprovações e não a hora do scheduler de retenção, porque o
// que se mede é diferente: a retenção mede prazos em dias, isto mede DEGRADAÇÃO. Com janelas
// sustentadas de 2-3 observações (as configs de AOS-086/105), um minuto põe um alerta crítico a
// disparar em 2-3 minutos de violação persistente — rápido o suficiente para agir, lento o
// suficiente para um pico transitório não gerar fadiga.
const DefaultSLOEvalInterval = 1 * time.Minute

// DefaultSLOWindow é a janela de dados por omissão: só entram na agregação os spans que FECHARAM
// nos últimos 5 minutos.
//
// A janela existe porque o anel da torneira é limitado por CONTAGEM, não por tempo: sem ela, um
// nó que ficou ocioso continuaria a publicar (e a alertar sobre) os percentis do último burst,
// para sempre. Cinco minutos são ~5 observações à cadência por omissão — cobre folgadamente a
// maior janela sustentada em uso (3) sem que uma violação já resolvida sobreviva na média.
const DefaultSLOWindow = 5 * time.Minute

// maxAvailabilitySamples limita o histórico de observações de prontidão retido. A janela
// pretendida é `window/interval`; o tecto protege de uma config com cadência minúscula e janela
// enorme pedir um anel absurdo — a mesma disciplina de memória-limitada da torneira de spans.
const maxAvailabilitySamples = 4096

// sloProbeTimeout é o prazo de cada sonda de dependência (custódia da KEK, hash-chain do WORM).
// É a materialização do ponto (3) do fail-open: uma dependência PENDURADA atrasa no máximo uma
// passagem, nunca segura o laço nem cresce a fila de goroutines.
const sloProbeTimeout = 5 * time.Second

// Erros de CONFIG (fail-closed — ver a nota sobre a fronteira da excepção, no cabeçalho).
var (
	// ErrBadSLOEvalInterval — AOS_SLO_EVAL_INTERVAL definida mas não é uma duração Go >= 0.
	ErrBadSLOEvalInterval = errors.New("aos: AOS_SLO_EVAL_INTERVAL invalida (esperada uma duracao Go >= 0, ex.: \"1m\") — e a cadencia com que o avaliador de SLOs/alertas (AOS-274) corre no loop de servico; \"0\" DESLIGA-O explicitamente (a observabilidade e fail-open: nao ha razao para obrigar um no a medir-se), mas um valor ilegivel ABORTA em vez de degradar para o default")

	// ErrBadSLOWindow — AOS_SLO_WINDOW definida mas não é uma duração Go > 0.
	ErrBadSLOWindow = errors.New("aos: AOS_SLO_WINDOW invalida (esperada uma duracao Go > 0, ex.: \"5m\") — e a janela de dados que a avaliacao agrega (so entram spans FECHADOS dentro dela); 0 nao e aceite porque uma janela vazia deixaria todos os SLIs derivados de spans permanentemente sem amostras, o que e indistinguivel de um no calmo")
)

// sloEvalIntervalFromEnv resolve a cadência a partir do ambiente. Vazia ⇒ [DefaultSLOEvalInterval];
// "0" ⇒ 0 (DESLIGADO, explicitamente); malformada ou negativa ⇒ [ErrBadSLOEvalInterval].
func sloEvalIntervalFromEnv() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_SLO_EVAL_INTERVAL"))
	if raw == "" {
		return DefaultSLOEvalInterval, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%w: AOS_SLO_EVAL_INTERVAL=%q", ErrBadSLOEvalInterval, raw)
	}
	return d, nil
}

// sloWindowFromEnv resolve a janela de dados. Vazia ⇒ [DefaultSLOWindow]; malformada ou <= 0 ⇒
// [ErrBadSLOWindow].
func sloWindowFromEnv() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_SLO_WINDOW"))
	if raw == "" {
		return DefaultSLOWindow, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%w: AOS_SLO_WINDOW=%q", ErrBadSLOWindow, raw)
	}
	return d, nil
}

// WithSLOEvalInterval sobrepõe a cadência da avaliação. <= 0 DESLIGA o laço (os testes conduzem
// a passagem à mão via [NodeService.EvaluateSLOsNow]). Arma a flag Set, sem a qual o
// "explicitamente 0" seria indistinguível de "não configurado" e o default ganharia.
func WithSLOEvalInterval(d time.Duration) NodeServiceOption {
	return func(c *nodeServiceConfig) {
		c.sloEvalInterval = d
		c.sloEvalIntervalSet = true
	}
}

// WithSLOWindow sobrepõe a janela de dados agregada. <= 0 é ignorado (mantém o default) — ao
// contrário da cadência, uma janela nula não é uma forma de desligar nada, só de esvaziar todos
// os SLIs.
func WithSLOWindow(d time.Duration) NodeServiceOption {
	return func(c *nodeServiceConfig) {
		if d > 0 {
			c.sloWindow = d
		}
	}
}

// ---------------------------------------------------------------------------
// Janela de prontidão — a fonte REAL do SLI de disponibilidade do plano de controlo.
// ---------------------------------------------------------------------------

// availabilityWindow é o anel de observações de prontidão (uma por tick). A disponibilidade é a
// FRACÇÃO de observações boas — uma medição acumulada do nó sobre si próprio, com a mesma
// disciplina de memória-limitada da torneira de spans.
type availabilityWindow struct {
	mu   sync.Mutex
	buf  []bool
	next int
	full bool
}

// newAvailabilityWindow dimensiona o anel para cobrir `window` à cadência `interval`, com o tecto
// de [maxAvailabilitySamples] e um mínimo de uma observação.
func newAvailabilityWindow(window, interval time.Duration) *availabilityWindow {
	n := 1
	if interval > 0 && window > interval {
		n = int(window / interval)
	}
	if n > maxAvailabilitySamples {
		n = maxAvailabilitySamples
	}
	return &availabilityWindow{buf: make([]bool, n)}
}

// observe regista uma observação de prontidão.
func (a *availabilityWindow) observe(ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.buf[a.next] = ok
	a.next++
	if a.next == len(a.buf) {
		a.next = 0
		a.full = true
	}
}

// fraction devolve a fracção de observações boas e o número de amostras. n == 0 ⇒ ainda não há
// observações (o SLI fica não avaliado — anti-vacuidade).
func (a *availabilityWindow) fraction() (float64, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := a.next
	if a.full {
		n = len(a.buf)
	}
	if n == 0 {
		return 0, 0
	}
	good := 0
	for i := 0; i < n; i++ {
		idx := i
		if a.full {
			idx = (a.next + i) % len(a.buf)
		}
		if a.buf[idx] {
			good++
		}
	}
	return float64(good) / float64(n), n
}

// ---------------------------------------------------------------------------
// O resultado de UMA passagem — a superfície que /metrics e os testes lêem.
// ---------------------------------------------------------------------------

// SLOEvaluation é a fotografia IMUTÁVEL de uma passagem do avaliador. É publicada por ponteiro
// atómico ([sloEvaluator.last]) para que o `/metrics` a leia sem lock nenhum: um scrape não pode
// contender com o laço, nem o laço atrasar um scrape.
type SLOEvaluation struct {
	// At é o instante da passagem (UTC).
	At time.Time
	// Mediation é o dashboard dos 4 SLIs da mediação (AOS-085) desta janela.
	Mediation otelgenai.DashboardSnapshot
	// Operational é o snapshot renderizado dos 7 SLIs canónicos (AOS-104) desta janela.
	Operational otelgenai.OperationalSnapshot
	// Alerts são TODOS os alertas avaliados (disparados ou não) das duas famílias, com o streak
	// da janela sustentada. Um alerta com Fired=false é informação: prova que a regra CORREU.
	Alerts []SLOAlert
	// SpansInWindow é quantos spans reais entraram nesta agregação.
	SpansInWindow int
	// SpansObserved é o total de spans que a torneira viu desde o arranque (incluindo os já
	// sobrescritos pelo anel) — distingue "nó calmo" de "torneira nunca ligada".
	SpansObserved uint64
	// AvailabilitySamples é quantas observações de prontidão suportam o SLI de disponibilidade.
	AvailabilitySamples int
	// WORMChecked indica se a hash-chain foi de facto verificada nesta passagem (false quando o
	// WORM é opaco/inalcançável — nesse caso o SLI fica sem amostras, não em breach).
	WORMChecked bool
}

// SLOAlert é um alerta avaliado JÁ LIGADO ao registo de runbooks (AOS-106).
type SLOAlert struct {
	// Alert é o alerta tal como o substrato o produziu (nome, severidade, valor, limiar, rota,
	// Fired, Streak, Offenders).
	Alert otelgenai.Alert
	// Catalog distingue a família: "mediation" (AOS-085/086) ou "operational" (AOS-104/105). É
	// necessário porque dois SLIs (cache-hit-rate e overhead p95) são observados pelas duas, com
	// SLOs potencialmente distintos.
	Catalog string
	// Runbook é a entrada do registo para onde o alerta encaminha.
	Runbook runbooks.Entry
	// RunbookResolved é false quando o Route.Runbook do alerta NÃO tem entrada no registo — um
	// ÓRFÃO. Não se omite: grita-se.
	RunbookResolved bool
}

// Famílias de catálogo (rótulos estáveis, usados no log e nas labels de /metrics).
const (
	sloCatalogMediation   = "mediation"
	sloCatalogOperational = "operational"
)

// Firing devolve só os alertas DISPARADOS (janela sustentada satisfeita).
func (e *SLOEvaluation) Firing() []SLOAlert {
	if e == nil {
		return nil
	}
	var out []SLOAlert
	for _, a := range e.Alerts {
		if a.Alert.Fired {
			out = append(out, a)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// O avaliador.
// ---------------------------------------------------------------------------

// sloEvaluator é o produtor: guarda as duas configs, os dois avaliadores com janela sustentada
// (que são ESTADO — o streak vive entre passagens) e a última avaliação publicada.
type sloEvaluator struct {
	node     *Node
	interval time.Duration
	window   time.Duration

	sloCfg  otelgenai.SLOConfig
	catalog otelgenai.DashboardCatalog

	// Os dois avaliadores sustentados. NÃO são seguros para uso concorrente — só o laço (ou o
	// [NodeService.EvaluateSLOsNow] dos testes) lhes toca, serializado por `passMu`.
	passMu sync.Mutex
	med    *otelgenai.AlertEvaluator
	ops    *otelgenai.OperationalAlertEvaluator
	avail  *availabilityWindow

	last   atomic.Pointer[SLOEvaluation]
	total  atomic.Uint64
	panics atomic.Uint64
}

// newSLOEvaluator compõe o avaliador com as configs de REFERÊNCIA do substrato (AOS-085/086/104/
// 105). As configs são as default por decisão: são as que os runbooks de AOS-106 assumem e as que
// o gate de não-órfãos do CI valida — um limiar local divergente tornaria o alerta do nó
// inconciliável com o procedimento documentado.
func newSLOEvaluator(node *Node, interval, window time.Duration) *sloEvaluator {
	return &sloEvaluator{
		node:     node,
		interval: interval,
		window:   window,
		sloCfg:   otelgenai.DefaultSLOConfig(),
		catalog:  otelgenai.DefaultDashboardCatalog(),
		med:      otelgenai.NewAlertEvaluator(otelgenai.DefaultAlertConfig()),
		ops:      otelgenai.NewOperationalAlertEvaluator(otelgenai.DefaultOperationalAlertConfig()),
		avail:    newAvailabilityWindow(window, interval),
	}
}

// sloEvaluatorArmed decide se o laço ARRANCA. Ao contrário do de retenção — cuja conjunção é uma
// barreira de segurança (nada se destrói sem legal hold e sem selo) — aqui a condição é só
// "existe alguma coisa que se possa medir": cadência > 0 e nó composto. Não se exige a torneira
// de spans, porque as duas fontes que não dependem dela (prontidão do plano de controlo e
// integridade do WORM) são exactamente as de maior severidade.
func sloEvaluatorArmed(node *Node, interval time.Duration) bool {
	return interval > 0 && node != nil
}

// Last devolve a última avaliação publicada (nil se ainda não houve nenhuma). Lock-free.
func (e *sloEvaluator) Last() *SLOEvaluation {
	if e == nil {
		return nil
	}
	return e.last.Load()
}

// Evaluations devolve quantas passagens completaram desde o arranque.
func (e *sloEvaluator) Evaluations() uint64 {
	if e == nil {
		return 0
	}
	return e.total.Load()
}

// Panics devolve quantas passagens abortaram por pânico CONTIDO. Um valor > 0 é um defeito do
// avaliador a corrigir — e o facto de o nó continuar a servir runs é o fail-open a funcionar,
// não uma desculpa para o não corrigir.
func (e *sloEvaluator) Panics() uint64 {
	if e == nil {
		return 0
	}
	return e.panics.Load()
}

// ---------------------------------------------------------------------------
// O laço, no molde dos outros varrimentos do loop de serviço.
// ---------------------------------------------------------------------------

// evaluateSLOs é o laço periódico. Termina QUANDO — e SÓ quando — stop fecha (shutdown do
// serviço). Não há condição de paragem por erro: ver o ponto (4) do fail-open no cabeçalho.
func (s *NodeService) evaluateSLOs(stop <-chan struct{}) {
	if s.slo == nil {
		return
	}
	t := time.NewTicker(s.slo.interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.slo.evaluateSafely(context.Background(), s.controlPlaneAvailable, s.log)
		}
	}
}

// EvaluateSLOsNow corre UMA avaliação imediatamente e devolve-a. Existe para os testes a
// conduzirem de forma determinista, sem esperar pelo ticker (molde de
// [NodeService.SweepApprovalsNow]/[NodeService.SweepRetentionNow]).
func (s *NodeService) EvaluateSLOsNow(ctx context.Context) *SLOEvaluation {
	if s.slo == nil {
		return nil
	}
	return s.slo.evaluateSafely(ctx, s.controlPlaneAvailable, s.log)
}

// SLOSnapshot devolve a última avaliação publicada (nil se o avaliador está desligado ou ainda
// não correu). É o que o `/metrics` lê.
func (s *NodeService) SLOSnapshot() *SLOEvaluation { return s.slo.Last() }

// SLOEvaluatorArmed reporta se o laço está ligado nesta réplica.
func (s *NodeService) SLOEvaluatorArmed() bool { return s.slo != nil }

// controlPlaneAvailable é a SONDA de prontidão do plano de controlo — a MESMA condição que
// `/readyz` responde, menos o drain.
//
// O drain fica DE FORA de propósito: durante um shutdown gracioso o nó está deliberadamente
// não-pronto, e contá-lo como indisponibilidade transformaria toda a manutenção planeada numa
// violação de SLO. (Na prática o laço já parou — o Shutdown fecha o `sweepStop` antes de drenar —
// mas a exclusão é explícita para que a semântica não dependa dessa ordem.)
//
// O que a sonda cobre, honestamente: o substrato de eventos e a custódia da KEK. NÃO cobre o PDP
// (o nó não retém um handle para o sondar) — o SLI mede a disponibilidade do plano de controlo
// DESTE nó, e o alerta encaminha para RB-04 porque é lá que o diagnóstico de PDP começa.
func (s *NodeService) controlPlaneAvailable(ctx context.Context) bool {
	if s.node == nil || s.node.EventStore == nil || !s.node.EventStore.Healthy() {
		return false
	}
	if p, ok := s.node.DSARVault.(readinessProber); ok {
		probeCtx, cancel := context.WithTimeout(ctx, sloProbeTimeout)
		defer cancel()
		if err := p.ready(probeCtx); err != nil {
			return false
		}
	}
	return true
}

// evaluateSafely corre UMA passagem sob `recover`. É o ponto (1) do fail-open: sem ele, um pânico
// nesta goroutine derrubaria o PROCESSO — a observabilidade a matar o nó, exactamente o que a
// excepção declarada existe para impedir. Devolve nil se a passagem abortou.
func (e *sloEvaluator) evaluateSafely(ctx context.Context, probe func(context.Context) bool, log func(string, ...any)) (ev *SLOEvaluation) {
	defer func() {
		if r := recover(); r != nil {
			e.panics.Add(1)
			ev = nil
			log("avaliador de SLOs (AOS-274): PANICO CONTIDO na passagem — o no NAO cai (fail-open declarado: a observabilidade nunca derruba o no); a passagem seguinte volta a tentar. CORRIJA: %v", r)
		}
	}()
	return e.evaluate(ctx, probe, log)
}

// evaluate é a passagem: recolhe as fontes REAIS, avalia as duas famílias sobre a MESMA janela,
// liga cada alerta ao runbook e publica o resultado.
func (e *sloEvaluator) evaluate(ctx context.Context, probe func(context.Context) bool, log func(string, ...any)) *SLOEvaluation {
	e.passMu.Lock()
	defer e.passMu.Unlock()

	now := time.Now().UTC()

	// (1) SPANS REAIS da janela. Sem torneira (observabilidade OTLP desligada) o slice é vazio e
	// os SLIs derivados de spans ficam SEM AMOSTRAS — não a zero, não "cumpridos".
	spans := e.node.sloTap.snapshot(now.Add(-e.window).UnixNano())
	events := make([]otelgenai.WideEvent, 0, len(spans))
	for _, sd := range spans {
		events = append(events, otelgenai.WideEventFromSpanData(sd))
	}

	// (2) MEDIAÇÃO (AOS-085/086) — 4 SLIs, janela sustentada própria.
	med := otelgenai.BuildDashboard(events, spans, e.sloCfg)
	medAlerts := e.med.Observe(med)

	// (3) PRONTIDÃO do plano de controlo: UMA observação por passagem, acumulada na janela.
	available := false
	if probe != nil {
		available = probe(ctx)
	}
	e.avail.observe(available)
	availFrac, availN := e.avail.fraction()
	var availPtr *float64
	if availN > 0 {
		availPtr = &availFrac
	}

	// (4) INTEGRIDADE da hash-chain do WORM.
	wormIntact, wormChecked := e.probeWORM(ctx)
	var wormPtr *bool
	if wormChecked {
		wormPtr = &wormIntact
	}

	// (5) OPERACIONAL (AOS-104/105) — 7 SLIs canónicos, sobre os MESMOS spans/events. Os três
	// fallbacks injectáveis (cold-start, headroom, fidelidade de replay) ficam a nil DE
	// PROPÓSITO: o nó não tem produtor para eles, e um valor inventado seria o alerta sintético
	// que o critério de aceitação recusa.
	opsSnap := e.catalog.Render(otelgenai.OperationalInputs{
		Events:                   events,
		Spans:                    spans,
		ControlPlaneAvailability: availPtr,
		AuditWORMIntact:          wormPtr,
	})
	opsAlerts := e.ops.Observe(opsSnap)

	// (6) LIGAÇÃO AO REGISTO DE RUNBOOKS (AOS-106) — o passo que transforma sinal em acção.
	all := make([]SLOAlert, 0, len(medAlerts)+len(opsAlerts))
	all = append(all, linkRunbooks(medAlerts, sloCatalogMediation)...)
	all = append(all, linkRunbooks(opsAlerts, sloCatalogOperational)...)

	ev := &SLOEvaluation{
		At:                  now,
		Mediation:           med,
		Operational:         opsSnap,
		Alerts:              all,
		SpansInWindow:       len(spans),
		SpansObserved:       e.node.sloTap.observedTotal(),
		AvailabilitySamples: availN,
		WORMChecked:         wormChecked,
	}
	e.last.Store(ev)
	e.total.Add(1)
	e.emit(ev, log)
	return ev
}

// probeWORM verifica a hash-chain e classifica o resultado em TRÊS estados, não dois:
//
//   - ADULTERAÇÃO (`*audit.VerifyError`): (false, true) — o SLI vai a 0 e o alerta crítico
//     encaminha para PROC-DR. É o único caso que dispara.
//   - ÍNTEGRA: (true, true).
//   - NÃO VERIFICÁVEL (WORM ausente, opaco — [audit.ErrPartitionsUnavailable] — ou erro de I/O):
//     (_, false) ⇒ SEM AMOSTRA. Chamar "adulterado" a um erro de leitura seria fabricar o
//     incidente mais grave do catálogo a partir de um disco lento: um falso positivo aqui manda
//     um operador correr o procedimento de DR sem haver DR nenhum a fazer.
func (e *sloEvaluator) probeWORM(ctx context.Context) (intact bool, checked bool) {
	if e.node == nil || e.node.WORM == nil {
		return false, false
	}
	probeCtx, cancel := context.WithTimeout(ctx, sloProbeTimeout)
	defer cancel()
	err := e.node.VerifyWORM(probeCtx)
	switch {
	case err == nil:
		return true, true
	case errors.As(err, new(*audit.VerifyError)):
		return false, true
	default:
		return false, false
	}
}

// linkRunbooks resolve o Route.Runbook de cada alerta no registo canónico de AOS-106.
func linkRunbooks(alerts []otelgenai.Alert, catalog string) []SLOAlert {
	out := make([]SLOAlert, 0, len(alerts))
	for _, a := range alerts {
		entry, ok := runbooks.Lookup(a.Route.Runbook)
		out = append(out, SLOAlert{Alert: a, Catalog: catalog, Runbook: entry, RunbookResolved: ok})
	}
	return out
}

// emit escreve o LOG ESTRUTURADO da passagem: uma linha de resumo + uma linha por alerta
// DISPARADO, com o runbook resolvido (título, doc, ADR). Só os disparados ganham linha própria —
// as regras que correram e não dispararam ficam contadas no resumo e legíveis no `/metrics`; uma
// linha por regra a cada minuto seria fadiga no log em vez de sinal.
func (e *sloEvaluator) emit(ev *SLOEvaluation, log func(string, ...any)) {
	firing := ev.Firing()
	log("avaliador de SLOs (AOS-274): passagem at=%s janela=%s spans_na_janela=%d spans_desde_arranque=%d regras=%d a_disparar=%d prontidao_amostras=%d worm_verificado=%t (FAIL-OPEN: nada aqui derruba o no)",
		ev.At.Format(time.RFC3339), e.window, ev.SpansInWindow, ev.SpansObserved, len(ev.Alerts), len(firing), ev.AvailabilitySamples, ev.WORMChecked)
	for _, sa := range firing {
		a := sa.Alert
		if !sa.RunbookResolved {
			// ÓRFÃO em runtime: o mesmo invariante que [runbooks.Validate] verifica no CI. Grita-se
			// em vez de omitir — um alerta crítico sem procedimento é pior do que não ter alerta,
			// porque parece accionável.
			log("SLO/ALERTA (AOS-274) A DISPARAR alerta=%q catalogo=%q severidade=%q sli=%q valor=%g slo=%g direccao=%q streak=%d owner=%q runbook_ORFAO=%q — o alerta encaminha para um runbook SEM entrada no registo (AOS-106); corrija o registo ou a rota",
				a.Name, sa.Catalog, a.Severity, a.SLI, a.Value, a.Threshold, a.Direction, a.Streak, a.Route.Owner, a.Route.Runbook)
			continue
		}
		log("SLO/ALERTA (AOS-274) A DISPARAR alerta=%q catalogo=%q severidade=%q sli=%q valor=%g slo=%g direccao=%q streak=%d owner=%q runbook=%q runbook_titulo=%q runbook_doc=%q runbook_adr=%q ofensores=%d — %s",
			a.Name, sa.Catalog, a.Severity, a.SLI, a.Value, a.Threshold, a.Direction, a.Streak, a.Route.Owner,
			sa.Runbook.ID, sa.Runbook.Title, sa.Runbook.DocPath, sa.Runbook.ADR, len(a.Offenders), a.Message)
	}
}

// ---------------------------------------------------------------------------
// Exposição em GET /metrics — o ENDPOINT do critério de aceitação.
// ---------------------------------------------------------------------------

// writeSLOMetrics escreve a última avaliação no corpo de `/metrics`, em formato de exposição
// Prometheus. Escolheu-se ESTA superfície e não uma rota nova por três razões: já existe e já é
// scrapada; já é não-autenticada com a disciplina de não revelar RunIDs; e um SLO que só se lê
// numa rota própria continuaria a não chegar ao sítio onde os alertas de um deployment real
// vivem.
//
// Ao contrário do helper `g` do handler, os nomes repetem-se com labels diferentes (um sample por
// SLI, um por alerta) — pelo que HELP/TYPE são emitidos UMA vez por nome, como o formato exige.
//
// O que NÃO sai daqui: os [otelgenai.Alert.Offenders] (trace_ids). Ver a nota no chamador.
func (h *apiHandler) writeSLOMetrics(b *strings.Builder) {
	armed := h.svc != nil && h.svc.SLOEvaluatorArmed()
	fmt.Fprintf(b, "# HELP aos_slo_evaluator_armed O avaliador de SLOs/alertas (AOS-274) esta ligado no loop de servico (1) ou desligado (0).\n# TYPE aos_slo_evaluator_armed gauge\naos_slo_evaluator_armed %d\n", boolMetric(armed))
	if !armed {
		return
	}
	fmt.Fprintf(b, "# HELP aos_slo_evaluations_total Passagens do avaliador de SLOs concluidas desde o arranque.\n# TYPE aos_slo_evaluations_total counter\naos_slo_evaluations_total %d\n", h.svc.slo.Evaluations())
	// Um valor > 0 é um DEFEITO do avaliador — e o nó continuar de pé é o fail-open declarado a
	// funcionar, não uma razão para o ignorar. Exposto para que seja alertável a partir de fora.
	fmt.Fprintf(b, "# HELP aos_slo_evaluator_panics_total Passagens abortadas por panico CONTIDO (fail-open: o no nao cai; corrigir o avaliador).\n# TYPE aos_slo_evaluator_panics_total counter\naos_slo_evaluator_panics_total %d\n", h.svc.slo.Panics())

	ev := h.svc.SLOSnapshot()
	if ev == nil {
		return // ligado mas ainda sem passagem concluída: nada a afirmar (anti-vacuidade).
	}
	fmt.Fprintf(b, "# HELP aos_slo_last_evaluation_timestamp_seconds Instante da ultima passagem do avaliador (unix).\n# TYPE aos_slo_last_evaluation_timestamp_seconds gauge\naos_slo_last_evaluation_timestamp_seconds %d\n", ev.At.Unix())
	fmt.Fprintf(b, "# HELP aos_slo_spans_in_window Spans REAIS agregados na ultima janela.\n# TYPE aos_slo_spans_in_window gauge\naos_slo_spans_in_window %d\n", ev.SpansInWindow)
	fmt.Fprintf(b, "# HELP aos_slo_spans_observed_total Spans que passaram pela torneira desde o arranque (distingue no calmo de torneira desligada).\n# TYPE aos_slo_spans_observed_total counter\naos_slo_spans_observed_total %d\n", ev.SpansObserved)

	// Os SLIs. `samples` é tão informativo quanto `value`: um SLI com samples=0 NÃO está cumprido
	// nem violado — está sem produtor neste deployment (anti-vacuidade de AOS-085). Emiti-lo com
	// value=0 e sem o samples ao lado seria a mentira que a regra existe para evitar.
	b.WriteString("# HELP aos_slo_sli Valor observado de um SLI na ultima janela.\n# TYPE aos_slo_sli gauge\n")
	forEachSLI(ev, func(catalog string, v otelgenai.SLIValue) {
		fmt.Fprintf(b, "aos_slo_sli{catalog=%q,sli=%q} %g\n", catalog, v.Name, v.Value)
	})
	b.WriteString("# HELP aos_slo_target Alvo (SLO) contra o qual o SLI foi avaliado.\n# TYPE aos_slo_target gauge\n")
	forEachSLI(ev, func(catalog string, v otelgenai.SLIValue) {
		fmt.Fprintf(b, "aos_slo_target{catalog=%q,sli=%q,direction=%q} %g\n", catalog, v.Name, v.Direction, v.SLO)
	})
	b.WriteString("# HELP aos_slo_samples Amostras que suportam o SLI (0 = SEM produtor no no; nem cumprido nem violado).\n# TYPE aos_slo_samples gauge\n")
	forEachSLI(ev, func(catalog string, v otelgenai.SLIValue) {
		fmt.Fprintf(b, "aos_slo_samples{catalog=%q,sli=%q} %d\n", catalog, v.Name, v.Samples)
	})
	b.WriteString("# HELP aos_slo_breached O SLI foi AVALIADO e nao cumpre o SLO (1).\n# TYPE aos_slo_breached gauge\n")
	forEachSLI(ev, func(catalog string, v otelgenai.SLIValue) {
		fmt.Fprintf(b, "aos_slo_breached{catalog=%q,sli=%q} %d\n", catalog, v.Name, boolMetric(v.Breached()))
	})

	// Os alertas, com o RUNBOOK como label — é a ligação a AOS-106 visível no próprio sinal: quem
	// receber o alerta no alertmanager já sabe qual o procedimento, sem cruzar tabelas. Um alerta
	// cujo runbook não resolve leva `runbook_orphan="1"` em vez de ser omitido.
	// AVALIÁVEL — o rótulo que faltava, e o achado da verificação de completude de 2026-08-23.
	//
	// O HELP dizia «0 = regra avaliada sem disparar», e para três regras isso era FALSO: os SLIs
	// `headroom_tokens` e `replay_fidelity` não têm produtor neste binário (o escalonador e o
	// motor de replay vivem noutro processo), pelo que nunca terão amostra e a regra é
	// ESTRUTURALMENTE incapaz de disparar. Medido em produção: `aos_slo_samples` a 0 para ambos,
	// e o `firing` a 0 — indistinguível do `audit_worm_integrity_broken`, que TEM amostra e
	// reporta 0 legitimamente.
	//
	// Um painel todo a verde não distinguia «avaliado e bem» de «nunca poderá ficar vermelho», e
	// três dos cinco runbooks canónicos estavam desse lado.
	//
	// PORQUE UM RÓTULO E NÃO A OMISSÃO, que é a disciplina do resto do `/metrics` («uma série que
	// mentiria fica AUSENTE»): para ALERTAS esta casa já decidiu o contrário, duas linhas acima —
	// «um alerta cujo runbook não resolve leva `runbook_orphan="1"` EM VEZ DE SER OMITIDO».
	// Omitir uma regra fá-la desaparecer do painel, que é perder a própria regra. A ressalva
	// viaja no sinal, como a do runbook.
	//
	// COMPATÍVEL COM AS CONSULTAS EXISTENTES: um `aos_alert_firing{alert="x"}` continua a casar,
	// porque os matchers do Prometheus são por subconjunto de rótulos.
	amostras := make(map[string]int, 16)
	forEachSLI(ev, func(catalog string, v otelgenai.SLIValue) {
		// Chave por CATÁLOGO e nome: `cache_hit_rate` existe nos DOIS catálogos e são SLIs
		// distintos — juntá-los faria um herdar as amostras do outro.
		//
		// RESIDUAL DECLARADO: é correcção por CONSTRUÇÃO e não está provada por teste. Hoje os dois
		// `cache_hit_rate` têm ZERO amostras, pelo que juntá-los daria o mesmo resultado e a
		// mutação que remove o catálogo da chave NÃO cai. Fica escrito em vez de contar como
		// coberto: quando um dos dois ganhar produtor, esta linha passa a morder — e é aí que um
		// teste a poderia apanhar.
		amostras[catalog+"\x00"+v.Name] = v.Samples
	})
	b.WriteString("# HELP aos_alert_firing Alerta de SLO a disparar (1) apos a janela sustentada. 0 com avaliavel=\"1\" = regra AVALIADA sem disparar; 0 com avaliavel=\"0\" = o SLI nao tem produtor neste no e a regra NUNCA pode disparar.\n# TYPE aos_alert_firing gauge\n")
	for _, sa := range ev.Alerts {
		orphan := 0
		if !sa.RunbookResolved {
			orphan = 1
		}
		avaliavel := boolMetric(amostras[sa.Catalog+"\x00"+sa.Alert.SLI] > 0)
		fmt.Fprintf(b, "aos_alert_firing{alert=%q,catalog=%q,severity=%q,sli=%q,runbook=%q,owner=%q,runbook_orphan=\"%d\",avaliavel=\"%d\"} %d\n",
			sa.Alert.Name, sa.Catalog, string(sa.Alert.Severity), sa.Alert.SLI,
			sa.Alert.Route.Runbook, sa.Alert.Route.Owner, orphan, avaliavel, boolMetric(sa.Alert.Fired))
	}
	b.WriteString("# HELP aos_alert_streak Observacoes consecutivas em breach que suportam o alerta.\n# TYPE aos_alert_streak gauge\n")
	for _, sa := range ev.Alerts {
		fmt.Fprintf(b, "aos_alert_streak{alert=%q,catalog=%q} %d\n", sa.Alert.Name, sa.Catalog, sa.Alert.Streak)
	}
}

// forEachSLI percorre os SLIs das DUAS famílias por ordem estável, rotulando cada um com a sua.
// A rotulagem não é decoração: cache-hit-rate e overhead p95 são observados pelas duas com SLOs
// potencialmente distintos, e sem a label os dois samples colidiriam na mesma série temporal.
func forEachSLI(ev *SLOEvaluation, fn func(catalog string, v otelgenai.SLIValue)) {
	for _, v := range ev.Mediation.SLIs() {
		fn(sloCatalogMediation, v)
	}
	for _, rp := range ev.Operational.Panels {
		fn(sloCatalogOperational, rp.SLI)
	}
}

// boolMetric projecta um booleano no 1/0 do formato de exposição.
func boolMetric(v bool) int {
	if v {
		return 1
	}
	return 0
}

// sloEvaluatorBanner declara a postura do avaliador a partir do estado REALMENTE composto (a
// disciplina de AOS-248: postura anunciada = postura ligada), incluindo — e isto é o ponto — QUE
// SLIs têm produtor neste deployment e quais ficam sem amostras. Um painel vazio sem explicação é
// indistinguível de um nó saudável.
func sloEvaluatorBanner(node *Node, interval, window time.Duration) string {
	if !sloEvaluatorArmed(node, interval) {
		if node == nil {
			return "avaliador de SLOs (AOS-274): DESLIGADO — sem no composto"
		}
		return "avaliador de SLOs (AOS-274): DESLIGADO (AOS_SLO_EVAL_INTERVAL=0) — os SLOs voltam a existir so como documento: nenhum alerta e produzido, o /metrics nao expoe aos_slo_* e os runbooks de AOS-106 ficam sem quem os accione"
	}
	fontes := "prontidao do plano de controlo (sonda igual a do /readyz) e integridade da hash-chain do WORM"
	derivados := "DORMENTES (observabilidade OTLP desligada: sem spans nao ha cache-hit, overhead p95, custo por trajectoria, override-rate nem cold-start — ficam SEM AMOSTRAS, nunca 'cumpridos'); defina AOS_OTLP_ENDPOINT para os ligar"
	if node.sloTap != nil {
		derivados = "LIGADOS pela torneira de spans (os MESMOS spans que saem para o colector OTLP): overhead de mediacao p95 dos execute_tool, custo por trajectoria dos chat, override-rate das decisoes; cache-hit-rate e cold-start entram se o gateway/pool partilharem o tracer"
	}
	return fmt.Sprintf("avaliador de SLOs (AOS-274): LIGADO — avalia de %s em %s sobre uma janela de %s, compondo os 4 SLIs da MEDIACAO (AOS-085/086) e os 7 CANONICOS operacionais (AOS-104/105) na MESMA passagem, com janela sustentada (um pico transitorio nao alerta); cada alerta disparado e ligado ao registo de runbooks (AOS-106) no log estruturado e exposto em GET /metrics (aos_slo_*/aos_alert_firing). FONTES SEMPRE ACTIVAS: %s. SLIs derivados de spans: %s. Sem produtor no no e SEM valor inventado (anti-vacuidade de AOS-085): headroom do scheduler e fidelidade de replay. FAIL-OPEN DECLARADO — ao contrario de todo o resto do no, a observabilidade NUNCA o derruba: panico contido, sem propagacao ao caminho de execucao, sondas com prazo, e o laco NAO para nem quando deteta adulteracao (nao destroi nada, e e quando o sinal mais faz falta)",
		interval, interval, window, fontes, derivados)
}
