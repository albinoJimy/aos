package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync/atomic"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	"github.com/aos-ref/kernel/agent-runtime/breaker"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-090 / DEF-908 — A METADE DE SEGURANÇA DA AUTONOMIA, LIGADA: a DEMOÇÃO automática.
//
// O [autonomy.Controller] existe desde AOS-090 (promoção por fiabilidade e demoção
// automática em anomalia) mas nunca teve chamador — o nó compunha só o [autonomy.LevelRegistry].
// Consequência declarada em ADR-014 §5: um par promovido fica no nível alto até um humano
// intervir. Este ficheiro liga o único sinal de anomalia que o nó produz HONESTAMENTE hoje: o
// TRIP do disjuntor multi-sinal (AOS-080). A promoção fica noutro eixo (a fiabilidade não é
// mensurável sem registo de desfecho pós-efeito — ver o subsistema de promoção).
//
// DEMOVE A CLASSE, não a instância. É a decisão de desenho central, e fecha os dois críticos
// que a revisão adversarial mediu:
//
//   - O nível efectivo que o PDP lê resolve instância→CLASSE→piso ([LevelForAgentOrClass]); a
//     unidade ESTÁVEL é a classe (os agent_id são cunhados por run). Demover a instância seria
//     efémero E, por sombrear a entrada de classe, poderia ELEVAR o efectivo. Demover a classe
//     baixa mesmo o que o PDP lê, e é durável (o invariante de releitura em [autonomy] só
//     aceita registos do controlador sobre chaves de classe que DESCEM).
//
// A CLASSE VEM DO EVENT STORE, não do WORM. O selo do WORM (audit.Principal) NÃO carrega a
// classe do agente — só o NHIID; carregá-la seria uma migração de SchemaVersion da hash-chain
// (autonomy_simular.go). O canal `tool.call.mediated` do Event Store (AOS-379, composto por
// MediationEvents) carrega `agent_class` E a capability de onde o domínio deriva. É a fonte
// honesta de (classe, domínio) de um run — a mesma [autonomy.DomainOf] que o resto do nó usa.

// autonomyControlConfigFromEnv resolve a policy-as-code do controlador (CA3 do AOS-090):
// AOS_AUTONOMY_CONTROL com JSON inline ⇒ [autonomy.LoadAutonomyControlConfig] (validada, com
// SemVer + ContentHash); vazio ⇒ [autonomy.DefaultAutonomyControlConfig] (a política de
// referência do blueprint, tecnica/09). É o que torna os limiares de demoção
// (salto, piso) CONFIGURÁVEIS por política em vez de fixos em código.
func autonomyControlConfigFromEnv() (autonomy.AutonomyControlConfig, error) {
	raw := os.Getenv("AOS_AUTONOMY_CONTROL")
	if raw == "" {
		return autonomy.DefaultAutonomyControlConfig(), nil
	}
	return autonomy.LoadAutonomyControlConfig([]byte(raw))
}

// filaDeAnomalias é o tecto da fila entre o disjuntor e o encaminhador. Pequeno de propósito:
// o [breaker.AlertSink] TEM de retornar promptamente (contrato de AOS-291), pelo que a fila
// desacopla, não acumula. Cheia, o alerta é DESCARTADO e o descarte é DENUNCIADO — um trip
// perdido em silêncio seria a mesma classe de defeito que este ficheiro fecha.
const filaDeAnomalias = 64

// prazoDaDemocao limita cada ciclo (ler o stream do run no Event Store + selar por classe).
// A demoção é prova de facto consumado — o trip já aconteceu e o run já está a ser parado —
// pelo que o prazo protege a goroutine do encaminhador, não a decisão.
const prazoDaDemocao = 10 * time.Second

// autonomiaAnomalias encaminha TRIPS do disjuntor para [autonomy.Controller.OnAnomaly],
// demovendo a CLASSE de cada par (classe, domínio) que o run tocou.
type autonomiaAnomalias struct {
	ctrl *autonomy.Controller
	es   eventstore.EventStore
	fila chan breaker.Alert
	log  func(string, ...any)

	// descartados conta os alertas perdidos por fila cheia. Só cresce. ATÓMICO porque
	// [Alert] é chamado das goroutines dos RUNS — várias em paralelo, uma por run com
	// disjuntor — e um `++` não protegido seria uma corrida a sério.
	descartados atomic.Uint64
}

// novasAnomaliasDeAutonomia constrói o encaminhador. ctrl ou es nil ⇒ (nil, nil): sem
// controlador não há a quem encaminhar, e sem Event Store não há como traduzir um run em
// (classe, domínio). Devolver nil mantém o disjuntor a funcionar sem esta ligação, em vez de
// meio-ligado.
func novasAnomaliasDeAutonomia(ctrl *autonomy.Controller, es eventstore.EventStore, log func(string, ...any)) *autonomiaAnomalias {
	if ctrl == nil || es == nil {
		return nil
	}
	if log == nil {
		log = func(f string, a ...any) { fmt.Fprintf(os.Stderr, "aos: "+f+"\n", a...) }
	}
	return &autonomiaAnomalias{
		ctrl: ctrl,
		es:   es,
		fila: make(chan breaker.Alert, filaDeAnomalias),
		log:  log,
	}
}

// Alert implementa [breaker.AlertSink]. NÃO BLOQUEIA — enfileira e devolve, como o contrato
// de AOS-291 exige. Todo o trabalho real (ler o stream, selar por classe) corre em [correr].
func (a *autonomiaAnomalias) Alert(_ context.Context, al breaker.Alert) {
	if a == nil {
		return
	}
	// SÓ O TRIP AUTOMÁTICO. [breaker.AlertEscalate] e [breaker.AlertAbort] são acções MANUAIS
	// de um operador (escalar a um humano, ou abortar). Despromover um agente porque um humano
	// decidiu olhar para ele inverteria o incentivo: quem escala perderia autonomia por escalar.
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

// correr consome a fila até stop fechar. Segue o padrão dos varredores do nó: um canal de
// paragem partilhado (sweepStop), fechado uma vez no Shutdown.
//
// DRAIN NO ENCERRAMENTO (o defeito que o WIP tinha): ao fechar, DRENA os trips já enfileirados
// antes de sair, em vez de os perder. Um trip que o disjuntor já entregou é um facto consumado;
// abandoná-lo por o Shutdown ter começado deixaria um agente inseguro no nível alto. Fica um
// resíduo declarado: um trip que chegue DEPOIS deste drain (de um run ainda vivo na janela de
// drain do nó) não é apanhado — fechá-lo exigiria o encaminhador participar no WaitGroup dos runs.
func (a *autonomiaAnomalias) correr(stop <-chan struct{}) {
	if a == nil {
		return
	}
	for {
		select {
		case <-stop:
			for {
				select {
				case al := <-a.fila:
					a.despromover(al)
				default:
					return
				}
			}
		case al := <-a.fila:
			a.despromover(al)
		}
	}
}

// despromover traduz um trip em demoções de CLASSE e sela-as. Cada par é independente: uma
// selagem falhada num par não impede os outros — o disjuntor já parou o run.
func (a *autonomiaAnomalias) despromover(al breaker.Alert) {
	ctx, cancel := context.WithTimeout(context.Background(), prazoDaDemocao)
	defer cancel()

	pares := a.paresDoRun(ctx, al.RunID)
	if len(pares) == 0 {
		// UM TRIP QUE NÃO DESPROMOVE NADA TEM DE SER DITO. Acontece legitimamente — um run que
		// dispara por wall-clock sem ter mediado uma única tool call não tem classe/domínio a
		// que aplicar a decisão. Em silêncio, seria indistinguível de o mecanismo estar avariado.
		a.log("autonomia/anomalia: TRIP do run %q (sinal %v) sem mediacoes com classe — nenhuma classe a despromover",
			al.RunID, al.Signal)
		return
	}
	for _, p := range pares {
		chave := autonomy.ClassPrefix + p.classe
		ch, mudou, err := a.ctrl.OnAnomaly(ctx, chave, p.dominio, autonomy.AnomalyUnsafeAction)
		switch {
		case err != nil:
			a.log("autonomia/anomalia: democao de classe %s:%s (run %q) FALHOU a selar: %v",
				chave, p.dominio, al.RunID, err)
		case !mudou:
			a.log("autonomia/anomalia: classe %s:%s (run %q) ja estava no piso — sem democao",
				chave, p.dominio, al.RunID)
		default:
			a.log("autonomia/anomalia: classe %s:%s DESPROMOVIDA %s -> %s por %s (run %q, sinal %v)",
				chave, p.dominio, ch.Old, ch.New, autonomy.AnomalyUnsafeAction, al.RunID, al.Signal)
		}
	}
}

// classeDominio é um par (classe de agente, domínio) que um run tocou.
type classeDominio struct {
	classe  string
	dominio string
}

// mediacaoAgentClass é o subconjunto MÍNIMO do payload de `tool.call.mediated` de que a
// demoção precisa: a classe do agente e a capability/recurso de onde o domínio deriva. As tags
// espelham o payload do RM (reference-monitor/eventsink.go, com `port_version` a versioná-lo);
// desserializar só o que se usa evita acoplar o nó ao payload inteiro.
type mediacaoAgentClass struct {
	Capability string `json:"capability"`
	Resource   struct {
		Value string `json:"value"`
	} `json:"resource"`
	Principal struct {
		AgentClass string `json:"agent_class"`
	} `json:"principal"`
}

// paresDoRun deriva os pares (classe, domínio) que o run tocou, lendo o stream do Event Store
// (stream_id == run_id) e filtrando os eventos `tool.call.mediated`. Devolve-os ordenados para
// a saída ser determinista. Um evento sem classe é ignorado — sem classe não há o que demover
// (a demoção é por classe), e é preferível não demover a demover a classe errada.
func (a *autonomiaAnomalias) paresDoRun(ctx context.Context, runID string) []classeDominio {
	if runID == "" {
		return nil
	}
	eventos, err := a.es.Read(ctx, runID, 1)
	if err != nil {
		// Ler o stream falhou. Não é «sem mediações»: é não saber. Denuncia-se (o chamador
		// distingue-o de «nenhuma classe» pelo log) e não se demove nada — demover às cegas
		// seria pior do que não demover.
		a.log("autonomia/anomalia: ler o stream do run %q falhou (%v) — sem democao", runID, err)
		return nil
	}
	vistos := make(map[classeDominio]struct{}, len(eventos))
	for _, ev := range eventos {
		if ev.Type != referencemonitor.EventTypeMediated {
			continue
		}
		var m mediacaoAgentClass
		if err := json.Unmarshal(ev.Payload, &m); err != nil {
			continue
		}
		if m.Principal.AgentClass == "" {
			continue
		}
		vistos[classeDominio{
			classe:  m.Principal.AgentClass,
			dominio: autonomy.DomainOf(m.Capability, m.Resource.Value),
		}] = struct{}{}
	}
	out := make([]classeDominio, 0, len(vistos))
	for p := range vistos {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].classe != out[j].classe {
			return out[i].classe < out[j].classe
		}
		return out[i].dominio < out[j].dominio
	})
	return out
}
