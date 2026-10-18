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
//   - OVERRIDE-RATE — proxy de ESCALADA: a fracção das CALLS que precisou de um humano
//     (emenda de 2026-10-18 ao ADR-025, decisão do dono, opção b). Cada call conta UMA vez,
//     identificada por (stream do run, `step_id` da call): a escalada conta uma vez e o permit
//     ou a recusa que a seguem no mesmo passo NÃO são decisão nova; um permit directo conta como
//     call sem humano; as recusas (`tool.call.denied`, incluindo as da lista-branca do run) NÃO
//     entram no denominador — uma recusa garantida não diz nada da fiabilidade do agente. Até
//     ao AOS-481 o permit pós-aprovação nunca chegava ao stream; quando chegou, contar decisões
//     em vez de calls pesava cada escalada a metade e promovia indevidamente. O sinal preciso
//     (desfecho de aprovação do hitl) fica como residual — o hitl.Channel não está composto.
//
// REGRAS DA PROMOÇÃO (emenda de 2026-10-18 ao ADR-025), além dos limiares:
//   - SÓ CLASSES CONFIGURADAS: só se julga uma (classe, domínio) provisionada em
//     AOS_AUTONOMY_LEVELS ([fiabilidadeAgregada.comParesConfigurados]); uma classe observada
//     e não registada nunca é promovida.
//   - A JANELA RECOMEÇA A CADA MUDANÇA DE NÍVEL, venha de onde vier — promoção do controlador,
//     demoção por anomalia, `POST /autonomy` do operador: as amostras de antes não servem; o
//     próximo degrau exige uma janela inteira observada no nível novo. Sem isto, o Evaluate de
//     hora a hora subia um degrau por tick sobre a mesma história, e desfazia no tick seguinte
//     uma demoção ou uma decisão do operador com a história do nível anterior.
//   - UMA CALL QUE PRECISOU DE HUMANO CONTA COMO TAL mesmo que a escalada não tenha sido vista
//     (escalada antes de um reinício, ou cujo registo best-effort falhou): o selo do permit diz
//     `human_approved` quando a aprovação foi verificada pelo [referencemonitor.ApprovalGate].
//   - JANELA COM BURACOS ⇒ WindowOK=false (ADR-025 §1): um permit (`tool.call.mediated`) sem
//     desfecho ao fim de min([prazoDoDesfecho], janela/2) torna a janela dessa chave inválida.
//   - Uma re-entrega do Event Store (mesmo stream, seq já visto) não conta a dobrar.
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

// prazoDoDesfecho é quanto um permit pode esperar pelo seu `tool.call.outcome` antes de contar
// como BURACO na janela (ADR-025 §1). O RM grava o desfecho logo a seguir ao despacho, na mesma
// goroutine e com prazo de escrita próprio de 2 s: o único atraso legítimo entre o selo e o
// desfecho é a execução da tool. Uma hora cobre com folga a tool mais lenta do nó e é a cadência
// do Evaluate por omissão; uma tool que demore mais deixa a janela inválida por excesso de
// cautela, nunca o contrário. Um buraco dentro da janela de 30 dias trava a promoção dessa
// (classe, domínio) até sair da janela — é o «janela com buracos ⇒ não promove» do ADR.
//
// Com uma janela curta (AOS_AUTONOMY_CONTROL), o prazo efectivo é metade da janela
// ([prazoEfectivoDoDesfecho]): com uma janela de 1 h ou menos, um prazo de 1 h nunca se vencia
// dentro dela e a regra morria. Prefere-se encurtar o prazo a recusar a configuração: janelas
// curtas são legítimas em ensaio, e um prazo mais curto só torna a regra mais cautelosa.
const prazoDoDesfecho = time.Hour

// prazoEfectivoDoDesfecho é min([prazoDoDesfecho], janela/2).
func prazoEfectivoDoDesfecho(janela time.Duration) time.Duration {
	if janela/2 < prazoDoDesfecho {
		return janela / 2
	}
	return prazoDoDesfecho
}

// estadoChamada é o que a agregação sabe de UMA tool call (stream do run + `step_id` da call):
// se já contou como decisão, e se o seu permit espera ainda pelo desfecho.
type estadoChamada struct {
	ts              time.Time // a primeira observação — para a podar com a janela
	decidida        bool      // já contou no denominador (escalada ou permit directo)
	permitPendente  bool      // houve permit e o desfecho ainda não chegou
	permitEm        time.Time // quando o permit foi observado
	desfechoChegado bool      // o desfecho chegou antes do permit (ordem de entrega)
}

// janelaClasse acumula as amostras de uma (classe, domínio), em ordem de observação.
type janelaClasse struct {
	// inicio é o início da observação desta chave NO NÍVEL ACTUAL: zero ⇒ o início do processo;
	// reposto a cada mudança de nível (a janela recomeça, emenda de 2026-10-18 ao ADR-025).
	inicio time.Time
	// nivel é o nível da chave no registo quando a janela (re)começou; nivelConhecido diz se já
	// foi lido (uma janela criada antes de o registo estar ligado adopta o primeiro que ler).
	nivel          autonomy.Level
	nivelConhecido bool
	desfechos      []amostraFiab
	decisoes       []amostraFiab
	chamadas       map[string]*estadoChamada
	inseridas      int // chamadas inseridas desde a última poda
}

// podaDasChamadas é de quantas em quantas chamadas novas se poda o mapa por idade.
const podaDasChamadas = 256

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
	// configurados são as (classe, domínio) provisionadas em AOS_AUTONOMY_LEVELS — as únicas
	// que a promoção julga. Vazio ⇒ nenhuma promove (fail-closed).
	configurados map[classeDominio]struct{}
	// ultimoSeq são os seqs vistos por stream (os dos eventos tool.call.* do run, poucos por
	// call), com o instante em que o stream foi visto pela última vez: uma re-entrega (seq já
	// visto) é ignorada. Um conjunto e não só o maior: uma entrega fora de ordem (o desfecho antes
	// do permit) não é re-entrega. Podado pela idade da janela, com a cadência das chamadas
	// ([podaDasChamadas]) — um stream por run, e os runs não param.
	ultimoSeq    map[string]*seqVisto
	novosStreams int
	// nivelDe lê o nível em vigor de uma chave no registo do controlador ([comControlador]).
	nivelDe func(agent, domain string) autonomy.Level
	// subscrita, se não-nil, é fechado quando a subscrição de [correr] fica activa. Só os testes
	// o usam, para não perderem eventos gravados antes de a subscrição existir.
	subscrita chan struct{}
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
		ultimoSeq: make(map[string]*seqVisto),
	}
}

// seqVisto são os seqs vistos de um stream, o maior deles, e quando o stream foi visto.
type seqVisto struct {
	seqs map[uint64]struct{}
	seq  uint64
	ts   time.Time
}

// comParesConfigurados regista as (classe, domínio) que a promoção pode julgar: as entradas de
// AOS_AUTONOMY_LEVELS cujo agente é uma CLASSE (`class:<classe>`). Uma entrada por instância
// (`agt-1:fs=L2`) não torna a classe configurada. Receptor nil ⇒ no-op.
func (f *fiabilidadeAgregada) comParesConfigurados(specs []autonomyLevelSpec) {
	if f == nil {
		return
	}
	pares := make(map[classeDominio]struct{}, len(specs))
	for _, sp := range specs {
		if !strings.HasPrefix(sp.agent, autonomy.ClassPrefix) {
			continue
		}
		pares[classeDominio{classe: strings.TrimPrefix(sp.agent, autonomy.ClassPrefix), dominio: sp.domain}] = struct{}{}
	}
	f.mu.Lock()
	f.configurados = pares
	f.mu.Unlock()
}

// configurado diz se a (classe, domínio) foi provisionada. Chamado com f.mu tomado.
func (f *fiabilidadeAgregada) configurado(k classeDominio) bool {
	_, ok := f.configurados[k]
	return ok
}

// comControlador liga o controlador que o Evaluate periódico invoca e a leitura do nível em vigor
// no registo dele (`nivelDe`, tipicamente [autonomy.LevelRegistry.LevelFor]). Chamado pelo
// Bootstrap depois de [autonomy.NewController] (o agregador é a fonte desse controlador, logo
// existe antes dele). É pela leitura do nível que a janela recomeça em qualquer mudança —
// promoção, demoção por anomalia ou operador. Receptor nil ⇒ no-op.
func (f *fiabilidadeAgregada) comControlador(c *autonomy.Controller, nivelDe func(agent, domain string) autonomy.Level) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.ctrl = c
	f.nivelDe = nivelDe
	f.mu.Unlock()
}

// acertarNivel recomeça a janela da chave se o nível em vigor mudou desde que ela começou (ou
// adopta-o, se ainda não o conhecia). Chamado com f.mu tomado, antes de juntar uma amostra e
// antes de julgar. Sem `nivelDe` ligado não há como saber, e nada muda.
func (f *fiabilidadeAgregada) acertarNivel(k classeDominio, j *janelaClasse, agora time.Time) {
	if f.nivelDe == nil {
		return
	}
	n := f.nivelDe(autonomy.ClassPrefix+k.classe, k.dominio)
	if !j.nivelConhecido {
		j.nivel, j.nivelConhecido = n, true
		return
	}
	if n != j.nivel {
		j.nivel = n
		j.inicio = agora
		j.desfechos, j.decisoes = nil, nil
	}
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
	// HumanApproved: o permit veio de uma aprovação humana verificada (AOS-481).
	HumanApproved bool `json:"human_approved"`
	// StepID é o `step_id` da call no payload (AOS-481): presente em denied/escalated/outcome,
	// cujo envelope leva o prefixo do tipo; ausente no mediated, cujo envelope já é o passo.
	StepID string `json:"step_id"`
}

// Reliability implementa [autonomy.ReliabilitySource]: a fiabilidade sustentada de uma classe
// (o `agent` vem prefixado por ClassPrefix — a demoção e a promoção governam classes) num
// domínio, sobre a janela. WindowOK só é true com observação suficiente E amostras bastantes.
func (f *fiabilidadeAgregada) Reliability(agent, domain string, window time.Duration) autonomy.Reliability {
	classe := strings.TrimPrefix(agent, autonomy.ClassPrefix)
	f.mu.Lock()
	defer f.mu.Unlock()
	agora := f.now()
	chave := classeDominio{classe: classe, dominio: domain}
	// Só classes provisionadas em AOS_AUTONOMY_LEVELS (emenda de 2026-10-18 ao ADR-025).
	if !f.configurado(chave) {
		return autonomy.Reliability{WindowOK: false}
	}
	j := f.porChave[chave]
	if j == nil {
		return autonomy.Reliability{WindowOK: false}
	}
	f.acertarNivel(chave, j, agora)
	// Observação insuficiente ⇒ não julgável: não há como afirmar "sustentado durante a janela".
	// A observação conta desde o arranque OU desde a última mudança de nível desta chave, o que
	// for mais recente — a janela recomeça a cada mudança.
	inicio := f.inicio
	if j.inicio.After(inicio) {
		inicio = j.inicio
	}
	if agora.Sub(inicio) < window {
		return autonomy.Reliability{WindowOK: false}
	}
	corte := agora.Add(-window)
	// Janela com BURACOS ⇒ não julgável (ADR-025 §1): um permit dentro da janela cujo desfecho
	// não chegou em min([prazoDoDesfecho], janela/2).
	prazo := prazoEfectivoDoDesfecho(window)
	for _, c := range j.chamadas {
		if c.permitPendente && !c.permitEm.Before(corte) && agora.Sub(c.permitEm) > prazo {
			return autonomy.Reliability{WindowOK: false}
		}
	}
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
	// RE-ENTREGA: o mesmo evento entregue outra vez pelo transporte (seq já visto no stream) não
	// conta a dobrar. Só com seq e stream conhecidos — um evento sem eles não é deduplicável.
	if ev.Seq > 0 && ev.StreamID != "" {
		visto := f.ultimoSeq[ev.StreamID]
		if visto == nil {
			f.podarStreams(agora)
			visto = &seqVisto{seqs: make(map[uint64]struct{})}
			f.ultimoSeq[ev.StreamID] = visto
		}
		if _, ja := visto.seqs[ev.Seq]; ja {
			return
		}
		visto.seqs[ev.Seq] = struct{}{}
		visto.ts = agora
		if ev.Seq > visto.seq {
			visto.seq = ev.Seq
		}
	}
	chave := classeDominio{classe: classe, dominio: dom}
	j := f.porChave[chave]
	if j == nil {
		j = &janelaClasse{chamadas: make(map[string]*estadoChamada)}
		f.porChave[chave] = j
	}
	f.acertarNivel(chave, j, agora)
	// A CALL a que o evento pertence: o `step_id` da call (no payload, ou o do envelope no
	// mediated) no stream do run. Sem passo não há call identificável: o evento conta sozinho.
	passo := p.StepID
	if passo == "" {
		passo = ev.StepID
	}
	var c *estadoChamada
	if passo != "" {
		c = j.chamada(ev.StreamID+"\x00"+passo, agora, f.maxIdade)
	}
	switch ev.Type {
	case referencemonitor.EventTypeOutcome:
		j.desfechos = append(podarAmostras(j.desfechos, agora, f.maxIdade), amostraFiab{ts: agora, erro: p.Outcome == referencemonitor.OutcomeError})
		if c != nil {
			if c.permitPendente {
				c.permitPendente = false
			} else {
				c.desfechoChegado = true
			}
		}
	case referencemonitor.EventTypeEscalated, referencemonitor.EventTypeMediated:
		// Precisou de humano: a escalada, ou o permit que diz que veio de uma aprovação verificada
		// (a escalada pode não estar no que este processo viu — reinício, registo falhado).
		escala := ev.Type == referencemonitor.EventTypeEscalated || p.HumanApproved
		// UMA decisão por call: a escalada conta, e o permit que a segue no mesmo passo não.
		if c == nil || !c.decidida {
			j.decisoes = append(podarAmostras(j.decisoes, agora, f.maxIdade), amostraFiab{ts: agora, escala: escala})
			if c != nil {
				c.decidida = true
			}
		}
		if !escala && c != nil {
			if c.desfechoChegado {
				c.desfechoChegado = false
			} else {
				c.permitPendente, c.permitEm = true, agora
			}
		}
	case referencemonitor.EventTypeDenied:
		// As recusas NÃO entram no denominador (emenda de 2026-10-18 ao ADR-025).
	}
}

// chamada devolve o estado da call `k`, criando-o; poda as chamadas mais velhas do que a
// janela de [podaDasChamadas] em [podaDasChamadas] inserções. Chamado com f.mu tomado.
func (j *janelaClasse) chamada(k string, agora time.Time, idade time.Duration) *estadoChamada {
	if c := j.chamadas[k]; c != nil {
		return c
	}
	j.inseridas++
	if j.inseridas >= podaDasChamadas {
		j.inseridas = 0
		corte := agora.Add(-idade)
		for chave, c := range j.chamadas {
			if c.ts.Before(corte) {
				delete(j.chamadas, chave)
			}
		}
	}
	c := &estadoChamada{ts: agora}
	j.chamadas[k] = c
	return c
}

// podarStreams apaga, de [podaDasChamadas] em [podaDasChamadas] streams novos, os streams não
// vistos há mais do que a janela. Chamado com f.mu tomado.
func (f *fiabilidadeAgregada) podarStreams(agora time.Time) {
	f.novosStreams++
	if f.novosStreams < podaDasChamadas {
		return
	}
	f.novosStreams = 0
	corte := agora.Add(-f.maxIdade)
	for s, v := range f.ultimoSeq {
		if v.ts.Before(corte) {
			delete(f.ultimoSeq, s)
		}
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
	if f.subscrita != nil {
		close(f.subscrita)
	}

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
		f.mu.Lock()
		ok := f.configurado(k)
		f.mu.Unlock()
		if !ok {
			continue // só classes configuradas (emenda de 2026-10-18 ao ADR-025)
		}
		// Uma promoção muda o nível, e a janela recomeça na leitura seguinte ([acertarNivel]),
		// como em qualquer outra mudança de nível.
		_, _, _ = ctrl.Evaluate(ctx, autonomy.ClassPrefix+k.classe, k.dominio)
	}
}
