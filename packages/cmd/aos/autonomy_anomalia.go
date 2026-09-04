package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync/atomic"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	"github.com/aos-ref/kernel/agent-runtime/breaker"
	"github.com/aos-ref/platform/audit"
)

// AOS-090 / DEF-908 — A METADE DE SEGURANÇA DA AUTONOMIA, LIGADA.
//
// O [autonomy.Controller] existe desde AOS-090, com promoção por fiabilidade sustentada e
// DEMOÇÃO AUTOMÁTICA E IMEDIATA em anomalia. Nunca teve chamador: o nó compunha só o
// [autonomy.LevelRegistry]. Consequência medida em `analises/10` §3.3 — quando a autonomia
// é ligada, é ligada sem a metade que a desliga, e um par promovido a L5 fica a L5 até
// alguém reparar.
//
// Este ficheiro liga o único sinal de anomalia que o nó produz HONESTAMENTE hoje: o TRIP do
// disjuntor multi-sinal. Os outros dois que [autonomy.AnomalyKind] nomeia ficam de fora, e a
// razão de cada um é uma medição, não uma omissão:
//
//   - PICO DE OVERRIDE-RATE. O cálculo vive em [hitl.Channel.finish], e o `hitl.Channel` NÃO
//     está composto no nó (`integration/secured.go` declara que não há `risk.Gate` composto).
//     A métrica é infra-estrutura inerte: `RecordOverrideRate` nunca produz um valor. Ligar
//     um sink a uma fonte que não corre daria um caminho verde que nunca dispara — o teste a
//     passar pela razão errada, na direcção perigosa.
//   - DRIFT. O [revalidation.Alert] transporta `ToolID`, `Version`, `Digest`, `Stage` e
//     `Reason` — e NENHUM identificador de agente. Não é uma lacuna de wiring: quem derivou
//     foi o ARTEFACTO. Despromover um agente porque uma tool que ele chamou trocou de
//     assinatura puniria o agente pelo defeito do registry.
//
// A PROMOÇÃO NÃO É LIGADA, e isso é deliberado. [autonomy.ReliabilitySource] pede uma taxa
// de erro sustentada, e o nó não a consegue medir: o selo de mediação é escrito ANTES do
// despacho (audit-before-effect), pelo que o erro de execução da tool nunca chega ao WORM.
// Um agente cujas tools rebentem todas tem taxa de erro ZERO em qualquer fonte derivável do
// trilho. O [autonomy.Controller] trata `src == nil` como «nunca promove» — é um fail-safe
// projectado, e usá-lo para o que foi projectado é mais honesto do que promover sobre uma
// taxa de negação de política a fingir de fiabilidade.

// filaDeAnomalias é o tecto da fila entre o disjuntor e o encaminhador. Pequeno de propósito:
// o [breaker.AlertSink] TEM de retornar promptamente (contrato de AOS-291), pelo que a fila
// existe para desacoplar, não para acumular. Cheia, o alerta é DESCARTADO e o descarte é
// denunciado — um trip perdido em silêncio seria a mesma classe de defeito que este ficheiro
// fecha.
const filaDeAnomalias = 64

// prazoDaDemocao limita cada ciclo de demoção (leitura do trilho do run + selagem por par).
// A demoção é prova de facto consumado — o trip já aconteceu e o run já está a ser parado —
// pelo que o prazo protege o worker, não a decisão.
const prazoDaDemocao = 10 * time.Second

// autonomiaAnomalias encaminha TRIPS do disjuntor para [autonomy.Controller.OnAnomaly].
//
// O PROBLEMA QUE RESOLVE, e que não é óbvio: o [breaker.Alert] identifica um RUN; o nível de
// autonomia é propriedade de um PAR (agente, domínio). A tradução não é 1:1 e não existia.
// A fonte usada é o próprio trilho selado: [audit] parte o WORM POR RunID
// (`rmadapter.go:defaultPartition`), pelo que a partição do run dá, sem instrumentação nova,
// o `Principal.NHIID` de cada mediação e a capability de onde [autonomy.DomainOf] deriva o
// domínio. É a mesma derivação que `autonomy_simular.go` já faz — deliberadamente a mesma,
// para que a simulação e a demoção real nunca discordem sobre o que é um par.
type autonomiaAnomalias struct {
	ctrl *autonomy.Controller
	worm audit.Store
	fila chan breaker.Alert
	log  func(string, ...any)

	// descartados conta os alertas perdidos por fila cheia. Só cresce.
	//
	// ATÓMICO, e não um int simples: [Alert] é chamado das goroutines dos RUNS — várias em
	// paralelo, uma por run com disjuntor — e um `++` não protegido seria uma corrida a sério.
	// O `-race` da suite não a apanhava, porque o teste que enche a fila escreve de UMA só
	// goroutine; a corrida só existe em produção, que é o pior sítio para a descobrir.
	descartados atomic.Uint64
}

// novasAnomaliasDeAutonomia constrói o encaminhador. ctrl ou worm nil ⇒ (nil, nil): sem
// controlador não há a quem encaminhar, e sem trilho não há como traduzir um run num par.
// Devolver nil é o que mantém o disjuntor a funcionar sem esta ligação, em vez de meio-ligado.
func novasAnomaliasDeAutonomia(ctrl *autonomy.Controller, worm audit.Store, log func(string, ...any)) *autonomiaAnomalias {
	if ctrl == nil || worm == nil {
		return nil
	}
	if log == nil {
		log = func(f string, a ...any) { fmt.Fprintf(os.Stderr, "aos: "+f+"\n", a...) }
	}
	return &autonomiaAnomalias{
		ctrl: ctrl,
		worm: worm,
		fila: make(chan breaker.Alert, filaDeAnomalias),
		log:  log,
	}
}

// Alert implementa [breaker.AlertSink]. NÃO BLOQUEIA — enfileira e devolve, como o contrato
// de AOS-291 exige. Todo o trabalho real (ler o trilho, selar por par) corre em [correr].
func (a *autonomiaAnomalias) Alert(_ context.Context, al breaker.Alert) {
	if a == nil {
		return
	}
	// SÓ O TRIP AUTOMÁTICO. [breaker.AlertEscalate] e [breaker.AlertAbort] são acções
	// MANUAIS de um operador — escalar a um humano ou abortar graciosamente. Despromover um
	// agente porque um humano decidiu olhar para ele inverteria o incentivo: quem escala
	// perderia autonomia por escalar, e a escalada é exactamente o comportamento que se quer.
	if al.Kind != breaker.AlertTrip {
		return
	}
	select {
	case a.fila <- al:
	default:
		a.log("autonomia/anomalia: fila cheia — TRIP do run %q DESCARTADO sem democao (descartados=%d)",
			al.RunID, a.descartados.Add(1))
	}
}

// correr consome a fila até stop fechar. Segue o padrão dos varredores do nó
// (`approval_sweeper.go`): um canal de paragem partilhado, fechado uma vez no Shutdown.
func (a *autonomiaAnomalias) correr(stop <-chan struct{}) {
	if a == nil {
		return
	}
	for {
		select {
		case <-stop:
			return
		case al := <-a.fila:
			a.despromover(al)
		}
	}
}

// despromover traduz um trip em demoções e sela-as. Cada par é independente: uma selagem
// falhada num par não pode impedir os outros — o disjuntor já parou o run, e perder as
// demoções restantes por causa de uma seria agravar o incidente.
func (a *autonomiaAnomalias) despromover(al breaker.Alert) {
	ctx, cancel := context.WithTimeout(context.Background(), prazoDaDemocao)
	defer cancel()

	pares := a.paresDoRun(ctx, al.RunID)
	if len(pares) == 0 {
		// UM TRIP QUE NÃO DESPROMOVE NADA TEM DE SER DITO. Acontece legitimamente — um run
		// que dispara por wall-clock sem ter chegado a mediar uma única tool call não tem
		// par nenhum a que aplicar a decisão. Em silêncio, seria indistinguível de o
		// mecanismo estar avariado.
		a.log("autonomia/anomalia: TRIP do run %q (sinal %v) sem mediacoes seladas — nenhum par a despromover",
			al.RunID, al.Signal)
		return
	}
	for _, p := range pares {
		ch, mudou, err := a.ctrl.OnAnomaly(ctx, p.Agent, p.Domain, autonomy.AnomalyUnsafeAction)
		switch {
		case err != nil:
			a.log("autonomia/anomalia: democao de %s:%s (run %q) FALHOU a selar: %v",
				p.Agent, p.Domain, al.RunID, err)
		case !mudou:
			a.log("autonomia/anomalia: %s:%s (run %q) ja estava no piso — sem democao",
				p.Agent, p.Domain, al.RunID)
		default:
			a.log("autonomia/anomalia: %s:%s DESPROMOVIDO %s -> %s por %s (run %q, sinal %v)",
				p.Agent, p.Domain, ch.Old, ch.New, autonomy.AnomalyUnsafeAction, al.RunID, al.Signal)
		}
	}
}

// paresDoRun deriva os pares (agente, domínio) que o run tocou, lendo a partição do WORM
// que É o RunID. Devolve-os ordenados para a saída ser determinista.
func (a *autonomiaAnomalias) paresDoRun(ctx context.Context, runID string) []autonomy.Pair {
	if runID == "" {
		return nil
	}
	head, err := a.worm.Head(ctx, runID)
	if err != nil || head == 0 {
		return nil
	}
	recs, err := a.worm.Read(ctx, runID, 1, head)
	if err != nil {
		return nil
	}
	vistos := make(map[autonomy.Pair]struct{}, len(recs))
	for _, rec := range recs {
		// Mesmo filtro de `lerMediacoes`: sem ToolID/Capability não é uma mediação de tool
		// call, é outro selo que calhou na partição.
		if rec.ToolID == "" || rec.Capability == "" || rec.Principal.NHIID == "" {
			continue
		}
		vistos[autonomy.Pair{
			Agent:  rec.Principal.NHIID,
			Domain: autonomy.DomainOf(rec.Capability, rec.Resource.Value),
		}] = struct{}{}
	}
	out := make([]autonomy.Pair, 0, len(vistos))
	for p := range vistos {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		return out[i].Domain < out[j].Domain
	})
	return out
}
