package main

// COMPOSIÇÃO DO CIRCUIT BREAKER POR-RUN (AOS-080/081).
//
// O breaker é POR-RUN: detém a máquina de estados durável desse run para materializar a
// transição do disparo. A máquina JÁ EXISTE — é a mesma que o steer e a escalada usam
// ([runStateGates]) — e reutiliza-se, não se duplica.
//
// O sinal de NO-PROGRESS vem do detector de acções repetidas ([actiondedup]): o mesmo
// hash de tool call, repetido, é ausência de progresso. O hash é o MESMO que o Reference
// Monitor já anota no span execute_tool — não se inventa uma segunda noção de "acção".

import (
	"errors"
	"fmt"
	"os"
	"sync"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/breaker"
	"github.com/aos-ref/kernel/agent-runtime/breaker/actiondedup"
)

// breakerClass é a classe de agente com que o nó resolve o breaker por-run. É "" porque os
// limiares são uniformes (ver staticThresholds em breaker_thresholds.go). Está nomeada para
// que o gate de arranque interrogue o ThresholdProvider EXACTAMENTE com a mesma chave que o
// [runBreakers.resolve] usará mais tarde — um gate que olhasse para outra classe validaria
// uma configuração diferente da que corre.
const breakerClass = ""

// ErrBreakerVelocitySourceUnwired — os limiares de VELOCIDADE DE QUEIMA estão ligados (>0)
// mas este nó não cabla nenhuma [breaker.VelocitySource]. Fail-closed NO ARRANQUE (AOS-246).
//
// PORQUE É FATAL E NÃO UM AVISO: sem a fonte, [breaker.NewBreaker] recusa a construção com
// ErrVelocitySourceMissing e o run ficava SEM DISJUNTOR NENHUM — ligar o tecto de custo
// desligava também o no-progress e o wall-clock, em silêncio. O operador que provisiona a
// env está a pedir MAIS protecção e recebia ZERO. Entre arrancar sem a protecção anunciada
// e não arrancar, o nó não arranca.
var ErrBreakerVelocitySourceUnwired = errors.New("aos: AOS_BREAKER_MAX_COST_MICRO_USD_PER_SEC / AOS_BREAKER_MAX_TOKENS_PER_SEC estao ligados (>0) mas este no nao cabla nenhuma VelocitySource — o disjuntor NAO seria composto e o run correria sem no-progress nem wall-clock. Ponha as duas envs a 0 (ou remova-as) ate existir fonte de velocity")

// runBreakers constrói e guarda um [breaker.Breaker] por run, sobre a máquina de estados
// que o [runStateGates] já detém. Seguro para uso concorrente.
type runBreakers struct {
	gates    *runStateGates
	provider breaker.ThresholdProvider
	progress *actiondedup.Registry

	mu       sync.Mutex
	breakers map[string]*breaker.Breaker
	// reported limita a denúncia de um erro de construção inesperado do resolve() a UMA POR
	// RUN. O resolve corre por-iteração, pelo que sem limite um erro persistente encheria o
	// stderr; mas um `sync.Once` por PROCESSO devolvia o silêncio de F2 aos runs 2..N — o
	// primeiro run afectado deixava rasto e todos os seguintes corriam sem disjuntor sem uma
	// linha, enquanto o banner de postura já tinha dito ao operador que o run está protegido
	// pelo disjuntor (achado F-A7 da auditoria da W0). Por-run é o grão certo: um rasto por
	// run desprotegido, sem repetição por iteração.
	//
	// Limitado em memória pelo mesmo [forget] que poda os breakers (o run sai do registo de
	// em-curso e leva a sua entrada).
	reported map[string]struct{}
}

// newRunBreakers constrói o registo. provider nil ⇒ (nil, nil) (disjuntor não composto: um
// disjuntor sem limiares seria cego e daria a ilusão de protecção).
//
// O GATE FAIL-CLOSED DE CABLAGEM VIVE AQUI, NO ARRANQUE (AOS-246), e não no [resolve]: o
// resolve corre por-run, tarde, e não tem por onde devolver um erro — recusar lá seria
// recusar UM run, silenciosamente, depois de o nó já ter anunciado a protecção. Este
// construtor é o único sítio que conhece as DUAS metades do contrato: os limiares que o
// operador pediu (provider) e o conjunto de fontes que o nó vai efectivamente cablar
// (só WithProgressSource, no resolve abaixo). Confrontá-las aqui é o que transforma uma
// configuração impossível em falha de arranque em vez de um disjuntor ausente.
func newRunBreakers(gates *runStateGates, provider breaker.ThresholdProvider) (*runBreakers, error) {
	if gates == nil || provider == nil {
		return nil, nil
	}
	// Mesma chave que o resolve usará; ver [breakerClass].
	if th := provider.Thresholds(breakerClass); th.MaxCostMicroUSDPerSecond > 0 || th.MaxTokensPerSecond > 0 {
		return nil, fmt.Errorf("%w (custo/s=%g, tokens/s=%g)",
			ErrBreakerVelocitySourceUnwired, th.MaxCostMicroUSDPerSecond, th.MaxTokensPerSecond)
	}
	return &runBreakers{
		gates:    gates,
		provider: provider,
		// Janela e limiar do detector de repetição: 3 ocorrências do MESMO hash numa
		// janela de 8 acções recentes contam como ausência de progresso. Alinhado com o
		// default de iterações estéreis (o run patológico observado repetia a mesma call
		// indefinidamente); a janela é folgada para não confundir um retry legítimo
		// intercalado com trabalho real.
		progress: actiondedup.NewRegistry(actiondedup.Config{WindowSize: 8, Threshold: 3}),
		breakers: make(map[string]*breaker.Breaker),
		reported: make(map[string]struct{}),
	}, nil
}

// resolve devolve (construindo à primeira vez) o breaker do run. Devolve nil se o run não
// tiver máquina de estados aberta — sem ela não há transição durável a materializar, e um
// disjuntor que não consegue parar o run não deve fingir que o faz.
func (b *runBreakers) resolve(runID string) *breaker.Breaker {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if br, ok := b.breakers[runID]; ok {
		return br
	}
	gate := b.gates.resolveGate(runID)
	if gate == nil {
		return nil
	}
	// O sinal de no-progress precisa da fonte ARMADA antes da 1.ª observação; o registo
	// cria o detector por-run a pedido.
	br, err := breaker.NewBreaker(gate.m, b.provider, breakerClass,
		breaker.WithProgressSource(b.progress.Source(runID)),
	)
	if err != nil {
		// Construção recusada. Depois do gate de arranque ([newRunBreakers]) esta via é
		// INESPERADA — a configuração impossível já não chega aqui. Continua a não se
		// compor um disjuntor meio-ligado (o run corre sem ele), mas o erro NÃO vira
		// silêncio: um disjuntor ausente sem rasto era exactamente a falha que AOS-246
		// corrige. Denuncia-se uma vez POR RUN (ver [runBreakers.reported]), no mesmo canal
		// que o main usa para falhas do nó — cada run que corre desprotegido deixa rasto.
		if _, visto := b.reported[runID]; !visto {
			b.reported[runID] = struct{}{}
			fmt.Fprintf(os.Stderr, "aos: disjuntor do agente vivo NAO composto no run %s: %v\n", runID, err)
		}
		return nil
	}
	b.breakers[runID] = br
	return br
}

// observeAction alimenta o sinal de no-progress com o hash canónico de uma tool call. É o
// [agentruntime.ActionObserver] do runtime de produção (AOS-251): o bootstrap liga-o por
// method value a [agentruntime.WithActionObserver] e o loop invoca-o no fecho de CADA
// mediação (permit, deny ou escalate — o veredicto não interessa ao detector de repetição).
func (b *runBreakers) observeAction(runID, hash string) {
	if b == nil {
		return
	}
	b.progress.Observe(runID, hash)
}

// forget liberta o estado do run (chamado quando o run sai do registo de em-curso).
func (b *runBreakers) forget(runID string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	delete(b.breakers, runID)
	delete(b.reported, runID)
	b.mu.Unlock()
	b.progress.Forget(runID)
}

// livenessAdapter devolve o [agentruntime.LivenessBreaker] que o loop consulta, ou nil
// quando o disjuntor não está composto.
func (b *runBreakers) livenessAdapter() agentruntime.LivenessBreaker {
	if b == nil {
		return nil
	}
	ad, err := integration.NewLivenessBreakerAdapter(func(runID string) (*breaker.Breaker, bool) {
		br := b.resolve(runID)
		return br, br != nil
	})
	if err != nil {
		return nil
	}
	return ad
}
