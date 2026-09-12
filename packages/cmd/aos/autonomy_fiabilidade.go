package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// autonomy_fiabilidade.go — AOS-090/ADR-025, Fase D: a ReliabilitySource de PRODUÇÃO que
// alimenta a PROMOÇÃO automática de autonomia. Subscreve os eventos do RM no Event Store e
// mantém janelas deslizantes por (classe, domínio):
//
//   - TAXA DE ERRO — dos desfechos pós-efeito (tool.call.outcome, Fase C): erros/(ok+erros).
//     É o sinal honesto que o selo de decisão (permit, antes do efeito) é cego.
//   - OVERRIDE-RATE — proxy de ESCALADA (tool.call.escalated / total de decisões), o mesmo
//     proxy que o SLI overrideRateSLI do repo já usa (substrate/otel-genai/slo.go). O sinal
//     preciso (desfecho de aprovação do hitl) fica como residual — o hitl.Channel não está
//     composto no nó.
//
// WINDOWOK HONESTO: a subscrição só vê eventos NOVOS (desde o arranque), pelo que a janela só
// é julgável depois de a observação cobrir a duração da janela. Afirmar "fiabilidade
// sustentada durante 30 dias" sem 30 dias de dados seria mentir — e a promoção sobre uma
// janela incompleta é exactamente o que não se quer. A agregação sobre HISTÓRICO (replay ou
// aggregate durável) fica declarada como residual.

// minAmostrasFiabilidade é o mínimo de amostras (de desfecho E de decisão) para a janela ser
// julgável. Sem um mínimo, uma classe com 1 sucesso teria "0% de erro" e promoveria sobre ruído.
const minAmostrasFiabilidade = 20

// intervaloAvaliacaoPromocaoDefault é a cadência do Evaluate periódico. Sobre uma janela de
// dias, avaliar de hora a hora é folgado; configurável por AOS_AUTONOMY_EVAL_INTERVAL.
const intervaloAvaliacaoPromocaoDefault = time.Hour

// autonomyEvalIntervalFromEnv resolve a cadência do Evaluate periódico (AOS_AUTONOMY_EVAL_INTERVAL,
// duração; default [intervaloAvaliacaoPromocaoDefault]).
func autonomyEvalIntervalFromEnv() time.Duration {
	if v := os.Getenv("AOS_AUTONOMY_EVAL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return intervaloAvaliacaoPromocaoDefault
}

// capPromocaoAbaixoDeDualControl trava o tecto de promoção AUTOMÁTICA abaixo do limiar de
// dual-control (ADR-025 §3): o controlador nunca auto-promove para L4/L5 — chegar lá exige a
// cerimónia assinada (AOS-305/AOS-377). Fecha a via de contorno do dual-control por promoção.
// O maior nível que não exige four-eyes é o que [autonomyDualControlRequired] deixa passar (L3).
func capPromocaoAbaixoDeDualControl(cfg autonomy.AutonomyControlConfig) (autonomy.AutonomyControlConfig, error) {
	if !autonomyDualControlRequired(cfg.PromotionCeil()) {
		return cfg, nil
	}
	return autonomy.NewAutonomyControlConfig(
		cfg.Version(), cfg.ErrorRateMax(), cfg.OverrideRateMax(), cfg.Window(),
		cfg.DemotionDrop(), cfg.DemotionFloor(), autonomy.L3,
	)
}

// amostraFiab é um evento observado, com o instante para a janela deslizante.
type amostraFiab struct {
	ts     time.Time
	erro   bool // desfechos: a execução falhou
	escala bool // decisões: foi uma escalada (proxy de override)
}

// janelaClasse acumula as amostras de uma (classe, domínio), em ordem de observação.
type janelaClasse struct {
	desfechos []amostraFiab
	decisoes  []amostraFiab
}

// fiabilidadeAgregada implementa [autonomy.ReliabilitySource] mantendo janelas por (classe,
// domínio) alimentadas por uma subscrição ao Event Store. Seguro para concorrência.
type fiabilidadeAgregada struct {
	es        eventstore.EventStore
	now       func() time.Time
	maxIdade  time.Duration // retenção das amostras (= a janela da política)
	intervalo time.Duration
	inicio    time.Time // início da observação — para o WindowOK honesto

	mu       sync.Mutex
	porChave map[classeDominio]*janelaClasse
	ctrl     *autonomy.Controller // ligado após a construção do controlador (comControlador)
}

// novaFiabilidade constrói o agregador. es nil ⇒ nil (sem substrato não há o que observar).
func novaFiabilidade(es eventstore.EventStore, now func() time.Time, janela, intervalo time.Duration) *fiabilidadeAgregada {
	if es == nil {
		return nil
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if intervalo <= 0 {
		intervalo = intervaloAvaliacaoPromocaoDefault
	}
	return &fiabilidadeAgregada{
		es:        es,
		now:       now,
		maxIdade:  janela,
		intervalo: intervalo,
		inicio:    now(),
		porChave:  make(map[classeDominio]*janelaClasse),
	}
}

// comControlador liga o controlador que o Evaluate periódico invoca. Chamado pelo Bootstrap
// depois de [autonomy.NewController] (o agregador é a fonte desse controlador, logo existe
// antes dele). Receptor nil ⇒ no-op.
func (f *fiabilidadeAgregada) comControlador(c *autonomy.Controller) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.ctrl = c
	f.mu.Unlock()
}

// amostraPayload é o subconjunto dos payloads de mediação (principal.agent_class) e de
// desfecho (agent_class no topo) de que a agregação precisa. Espelha as tags do RM.
type amostraPayload struct {
	Capability string `json:"capability"`
	Resource   struct {
		Value string `json:"value"`
	} `json:"resource"`
	AgentClass string `json:"agent_class"` // eventos de desfecho (topo)
	Principal  struct {
		AgentClass string `json:"agent_class"`
	} `json:"principal"` // eventos de decisão (aninhado)
	Outcome string `json:"outcome"` // desfechos
}

// Reliability implementa [autonomy.ReliabilitySource]: a fiabilidade sustentada de uma classe
// (o `agent` vem prefixado por ClassPrefix — a demoção e a promoção governam classes) num
// domínio, sobre a janela. WindowOK só é true com observação suficiente E amostras bastantes.
func (f *fiabilidadeAgregada) Reliability(agent, domain string, window time.Duration) autonomy.Reliability {
	classe := strings.TrimPrefix(agent, autonomy.ClassPrefix)
	f.mu.Lock()
	defer f.mu.Unlock()
	agora := f.now()
	// Observação insuficiente ⇒ não julgável: não há como afirmar "sustentado durante a janela".
	if agora.Sub(f.inicio) < window {
		return autonomy.Reliability{WindowOK: false}
	}
	j := f.porChave[classeDominio{classe: classe, dominio: domain}]
	if j == nil {
		return autonomy.Reliability{WindowOK: false}
	}
	corte := agora.Add(-window)
	var erros, totDesf, escalas, totDec int
	for _, a := range j.desfechos {
		if a.ts.Before(corte) {
			continue
		}
		totDesf++
		if a.erro {
			erros++
		}
	}
	for _, a := range j.decisoes {
		if a.ts.Before(corte) {
			continue
		}
		totDec++
		if a.escala {
			escalas++
		}
	}
	if totDesf < minAmostrasFiabilidade || totDec < minAmostrasFiabilidade {
		return autonomy.Reliability{WindowOK: false}
	}
	return autonomy.Reliability{
		ErrorRate:    float64(erros) / float64(totDesf),
		OverrideRate: float64(escalas) / float64(totDec),
		WindowOK:     true,
	}
}

// observar é o [eventstore.Handler] da subscrição: classifica um evento do RM e acrescenta-o
// à janela da sua (classe, domínio). Usa o relógio de observação como instante da amostra (a
// subscrição é push, logo observação ≈ ocorrência) — determinista em teste via `now`.
func (f *fiabilidadeAgregada) observar(ev eventstore.Event) {
	var p amostraPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return
	}
	classe := p.AgentClass
	if classe == "" {
		classe = p.Principal.AgentClass
	}
	if classe == "" {
		return // sem classe não há a que agregar (a promoção é por classe)
	}
	dom := autonomy.DomainOf(p.Capability, p.Resource.Value)
	agora := f.now()
	f.mu.Lock()
	defer f.mu.Unlock()
	chave := classeDominio{classe: classe, dominio: dom}
	j := f.porChave[chave]
	if j == nil {
		j = &janelaClasse{}
		f.porChave[chave] = j
	}
	switch ev.Type {
	case referencemonitor.EventTypeOutcome:
		j.desfechos = append(podarAmostras(j.desfechos, agora, f.maxIdade), amostraFiab{ts: agora, erro: p.Outcome == referencemonitor.OutcomeError})
	case referencemonitor.EventTypeMediated, referencemonitor.EventTypeEscalated, referencemonitor.EventTypeDenied:
		j.decisoes = append(podarAmostras(j.decisoes, agora, f.maxIdade), amostraFiab{ts: agora, escala: ev.Type == referencemonitor.EventTypeEscalated})
	}
}

// podarAmostras remove as amostras mais velhas do que `idade` (janela). As amostras estão em
// ordem de observação (append), pelo que basta avançar o início.
func podarAmostras(s []amostraFiab, agora time.Time, idade time.Duration) []amostraFiab {
	corte := agora.Add(-idade)
	i := 0
	for i < len(s) && s[i].ts.Before(corte) {
		i++
	}
	if i == 0 {
		return s
	}
	return append([]amostraFiab(nil), s[i:]...)
}

// chaves devolve as (classe, domínio) observadas — o conjunto que o Evaluate periódico visita.
func (f *fiabilidadeAgregada) chaves() []classeDominio {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]classeDominio, 0, len(f.porChave))
	for k := range f.porChave {
		out = append(out, k)
	}
	return out
}

// correr subscreve os eventos do RM e corre o Evaluate periódico até stop fechar (AOS-090:
// a metade de PROMOÇÃO). Arranca no loop de serviço, no sweepStop partilhado. Sem controlador
// ligado (comControlador) é no-op — não há a quem pedir a promoção.
func (f *fiabilidadeAgregada) correr(stop <-chan struct{}) {
	if f == nil || f.es == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// O ctx da subscrição morre quando stop fecha ⇒ o Event Store remove a subscrição e
	// liberta a goroutine e a fila (Subscribe governa o ciclo de vida pelo ctx).
	go func() {
		<-stop
		cancel()
	}()
	sub, err := f.es.Subscribe(ctx, eventstore.Filter{Types: []string{
		referencemonitor.EventTypeOutcome,
		referencemonitor.EventTypeMediated,
		referencemonitor.EventTypeEscalated,
		referencemonitor.EventTypeDenied,
	}}, f.observar)
	if err != nil {
		return
	}
	defer sub.Unsubscribe()

	t := time.NewTicker(f.intervalo)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			f.avaliarPromocoes(ctx)
		}
	}
}

// avaliarPromocoes pede ao controlador para promover cada (classe, domínio) observada. O
// controlador só promove se a fiabilidade (Reliability) o justificar e nunca acima do tecto
// (travado abaixo de L4 no nó) — uma anomalia em corrida vence sempre (serializado no
// controlador). Falhas de selagem são engolidas aqui: a próxima passagem re-tenta.
func (f *fiabilidadeAgregada) avaliarPromocoes(ctx context.Context) {
	f.mu.Lock()
	ctrl := f.ctrl
	f.mu.Unlock()
	if ctrl == nil {
		return
	}
	for _, k := range f.chaves() {
		_, _, _ = ctrl.Evaluate(ctx, autonomy.ClassPrefix+k.classe, k.dominio)
	}
}
