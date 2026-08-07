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
	"sync"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/breaker"
	"github.com/aos-ref/kernel/agent-runtime/breaker/actiondedup"
)

// runBreakers constrói e guarda um [breaker.Breaker] por run, sobre a máquina de estados
// que o [runStateGates] já detém. Seguro para uso concorrente.
type runBreakers struct {
	gates    *runStateGates
	provider breaker.ThresholdProvider
	progress *actiondedup.Registry

	mu       sync.Mutex
	breakers map[string]*breaker.Breaker
}

// newRunBreakers constrói o registo. provider nil ⇒ nil (disjuntor não composto: um
// disjuntor sem limiares seria cego e daria a ilusão de protecção).
func newRunBreakers(gates *runStateGates, provider breaker.ThresholdProvider) *runBreakers {
	if gates == nil || provider == nil {
		return nil
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
	}
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
	br, err := breaker.NewBreaker(gate.m, b.provider, "",
		breaker.WithProgressSource(b.progress.Source(runID)),
	)
	if err != nil {
		// Construção recusada (ex.: limiar de no-progress sem fonte armada). Não se
		// compõe um disjuntor meio-ligado: o run corre sem ele, como antes.
		return nil
	}
	b.breakers[runID] = br
	return br
}

// observeAction alimenta o sinal de no-progress com o hash canónico de uma tool call.
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
