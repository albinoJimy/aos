package control

import (
	"bytes"
	"context"
	"sync"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// LoopSteer adapta o [SteerChannel] à porta [agentruntime.SteerSource] — o wiring que
// liga o canal de controlo out-of-band (AOS-023) ao loop base (AOS-158). É o
// "adaptador-no-pilar" do idioma AOS-060: o kernel define a porta, o pacote de controlo
// (que já depende do kernel) fornece o concreto, sem o kernel importar `control`.
//
// O loop consulta-o na fronteira de fim-de-turno: [LoopSteer.GracefulPause] materializa
// a pausa durável (running→paused) via o [StateGate] do run, e
// [LoopSteer.PendingCorrection] expõe a correcção humana trusted a injectar no tail.
//
// # Semântica UMA-VEZ da correcção (AOS-218)
//
// O loop consulta [LoopSteer.PendingCorrection] em TODA a fronteira de fim-de-turno, mas
// a correcção durável do canal permanece pendente até uma resume a limpar (e a resume só é
// alcançável após uma pausa). Sem cuidado, um live-steer (correcção sem pausa/resume) seria
// re-injectado no tail em TODO o turno seguinte, crescendo o tail sem limite. O loop base
// documenta a intenção como "injectada no tail do turno SEGUINTE" (singular): o adaptador
// materializa-a devolvendo cada correcção DISTINTA uma ÚNICA vez por run. Guarda-se a última
// correcção injectada por run (in-process, volátil); uma correcção idêntica já injectada não
// se repete, uma NOVA correcção (bytes diferentes) aplica-se, e quando o canal deixa de ter
// correcção pendente (resume/nunca-steerado) o marcador é limpo — pelo que um steer futuro,
// mesmo de conteúdo igual, volta a aplicar-se uma vez. A durabilidade/sobrevivência-a-crash da
// correcção continua a ser do [SteerChannel] (projecção reconstruível por Rebuild); este
// marcador é só a de-duplicação do consumo ao vivo e NÃO afecta a fidelidade de replay (a
// captura por-turno reproduz exactamente o que cada turno injectou).
type LoopSteer struct {
	ch    *SteerChannel
	gates func(runID string) StateGate

	mu      sync.Mutex
	applied map[string][]byte // última correcção já injectada por run (de-dup do consumo ao vivo)
}

// NewLoopSteer constrói o adaptador. gates resolve o [StateGate] (a máquina de estados
// durável, AOS-017) de cada run — o [SteerChannel.GracefulPause] precisa dele para a
// transição running→paused. Um gates que devolva nil para um run desliga a pausa desse
// run (o loop continua), fail-safe.
func NewLoopSteer(ch *SteerChannel, gates func(runID string) StateGate) *LoopSteer {
	return &LoopSteer{ch: ch, gates: gates, applied: make(map[string][]byte)}
}

// GracefulPause implementa [agentruntime.SteerSource]: se um interrupt estiver pendente
// para o run, materializa a pausa durável via o StateGate e devolve true (o loop pára).
func (a *LoopSteer) GracefulPause(ctx context.Context, runID string) (bool, error) {
	if a == nil || a.ch == nil || a.gates == nil {
		return false, nil
	}
	gate := a.gates(runID)
	if gate == nil {
		return false, nil
	}
	return a.ch.GracefulPause(ctx, runID, gate)
}

// PendingCorrection implementa [agentruntime.SteerSource]: expõe a correcção pendente do
// run (dado de controlo trusted), ou (nil, false). Aplica a semântica UMA-VEZ (ver o doc do
// [LoopSteer]): uma correcção DISTINTA é devolvida uma única vez por run; a mesma correcção
// já injectada não se repete turno-a-turno; quando o canal deixa de ter correcção pendente
// (resume/nunca-steerado), o marcador é limpo para que um steer futuro volte a aplicar-se.
func (a *LoopSteer) PendingCorrection(ctx context.Context, runID string) ([]byte, bool) {
	if a == nil || a.ch == nil {
		return nil, false
	}
	corr, ok := a.ch.PendingCorrection(runID)
	a.mu.Lock()
	defer a.mu.Unlock()
	if !ok {
		// Sem correcção pendente (nunca steerado ou já retomado): limpa o marcador para não
		// suprimir um steer futuro de conteúdo idêntico ao de um ciclo anterior.
		delete(a.applied, runID)
		return nil, false
	}
	if prev, seen := a.applied[runID]; seen && bytes.Equal(prev, corr) {
		// Já injectada neste processo — não repetir no turno seguinte (evita o crescimento
		// ilimitado do tail num live-steer sem resume).
		return nil, false
	}
	// NOVA correcção (ou primeira): injecta uma vez e memoriza-a.
	stored := make([]byte, len(corr))
	copy(stored, corr)
	a.applied[runID] = stored

	// A ENTREGA CONSOME-A, DURAVELMENTE (AOS-292). O marcador `applied` acima é in-process e
	// volátil: sozinho, de-duplica no mesmo processo e não sobrevive a um restart. Sem o
	// registo durável, um nó reiniciado reconstruía a correcção como pendente e injectava-a
	// segunda vez — num turno cujo prompt já foi capturado, fazendo o replay divergir por
	// `prompt_hash`. São duas de-duplicações a níveis diferentes, e ambas fazem falta.
	//
	// Um erro NÃO impede a entrega: a correcção já foi decidida e o loop vai injectá-la. O
	// que se perde é a marca durável, e o pior caso é uma re-injecção depois de um restart —
	// que o `applied` absorve enquanto o processo viver. Perder a correcção seria pior.
	if _, err := a.ch.ConsumeCorrection(ctx, runID); err != nil {
		// Sem log próprio aqui — o adaptador não tem logger e o canal já selou o que pôde.
		_ = err
	}
	return corr, true
}

// Assegura em compile-time que LoopSteer satisfaz a porta do loop.
var _ agentruntime.SteerSource = (*LoopSteer)(nil)
