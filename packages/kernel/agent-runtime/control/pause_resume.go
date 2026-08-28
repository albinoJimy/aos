package control

import (
	"context"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
)

// TailSteer é o kind do segmento de tail de uma correcção de steer. Marca, no prompt
// materializado, que aquela instrução veio do CANAL DE CONTROLO e é CONFIÁVEL — visível
// e distinta dos segmentos untrusted (history/tool_result) do loop base (ADR-005).
const TailSteer agentruntime.TailKind = "steer"

// StateGate é o subconjunto da máquina de estados durável de AOS-017 de que o canal
// depende para MATERIALIZAR as transições running→paused (graceful pause) e
// paused→running (resume). É uma interface mínima — não expõe os tipos internos da
// máquina — para que o canal fique fracamente acoplado e testável com um gate falso.
// [MachineGate] adapta um *[state.Machine] real a este contrato.
type StateGate interface {
	// Pause materializa running→paused (graceful pause no fim do turno).
	Pause(ctx context.Context, reason string) error
	// Resume materializa paused→running (retoma sob o lease já detido — não re-exige
	// fencing token, ao contrário do claim inicial).
	Resume(ctx context.Context, reason string) error
}

// MachineGate adapta um *[state.Machine] de AOS-017 ao [StateGate]. É o ponto de
// ligação ADITIVO com a máquina de estados: usa as arestas running→paused e
// paused→running já EXPOSTAS por AOS-017 ([state.Machine.Pause]/[state.Machine.Resume]),
// sem as duplicar nem alterar a tabela declarativa.
type MachineGate struct {
	Machine *state.Machine
}

// NewMachineGate constrói um [MachineGate] sobre a máquina de um run.
func NewMachineGate(m *state.Machine) *MachineGate { return &MachineGate{Machine: m} }

// Pause implementa [StateGate] via [state.Machine.Pause] (running→paused).
func (g *MachineGate) Pause(ctx context.Context, reason string) error {
	return g.Machine.Pause(ctx, state.TransitionEvent{Reason: reason})
}

// Resume implementa [StateGate] via [state.Machine.Resume] (paused→running).
func (g *MachineGate) Resume(ctx context.Context, reason string) error {
	return g.Machine.Resume(ctx, state.TransitionEvent{Reason: reason})
}

// Razões canónicas gravadas na transição de estado (campo Reason da máquina). São
// rótulos de auditoria legíveis, não segredos.
const (
	// ReasonGracefulPause — motivo da transição running→paused pela pausa graciosa.
	ReasonGracefulPause = "steer_graceful_pause"
	// ReasonSteerResume — motivo da transição paused→running pela retoma com correcção.
	ReasonSteerResume = "steer_resume"
)

// Correction é a correcção de steer APLICADA na retoma, entregue ao loop como
// INSTRUÇÃO DE CONTROLO CONFIÁVEL. É o oposto de um dado untrusted (ADR-005): entrou
// pelo canal de controlo, foi autenticada e o seu taint é [agentruntime.TaintTrusted].
// Present distingue "retoma sem correcção" (só resume) de "retoma com correcção".
type Correction struct {
	// Value é o conteúdo da correcção (a instrução confiável).
	Value []byte
	// EmitterID é o emissor autenticado que a injectou (rasto de não-repúdio).
	EmitterID string
	// Present indica se houve de facto uma correcção a aplicar.
	Present bool
}

// Tainted embrulha a correcção como valor CONFIÁVEL ([agentruntime.TaintTrusted]) — o
// contrato de proveniência que o loop usa para saber que esta instrução, ao contrário
// dos resultados de tool, PODE dirigir o agente. Nunca devolve untrusted: uma correcção
// só existe porque a sua assinatura de emissor validou.
func (c Correction) Tainted() agentruntime.Tainted {
	return agentruntime.Tainted{Value: c.Value, Taint: agentruntime.TaintTrusted}
}

// TailSegment materializa a correcção como um segmento de tail CONFIÁVEL a injectar no
// prompt do turno seguinte à retoma. O prefixo "taint=trusted" torna a proveniência
// explícita no prompt materializado (coerente com o esquema de marcação do loop base),
// e o kind [TailSteer] distingue-a visivelmente dos segmentos untrusted.
func (c Correction) TailSegment() agentruntime.TailSegment {
	// AssemblyVersion 1.3.0: o rotulo de proveniencia passou do CORPO para a LINHA DE
	// DELIMITACAO — `<steer taint=trusted>`. Este segmento e o SEGUNDO rotulo trusted da
	// janela (o outro e o TailCorrection do proprio loop), pelo que tinha exactamente a
	// mesma exposicao: escrito no corpo, ficava no mesmo espaco de linhas que o valor.
	return agentruntime.TailSegment{
		Kind:    TailSteer,
		Meta:    []agentruntime.TailMeta{{Key: "taint", Value: agentruntime.TaintTrusted}},
		Content: c.Value,
	}
}

// GracefulPause é a BARREIRA DE FIM DE TURNO. O loop chama-a no FIM do turno corrente
// (nunca a meio de uma activity): se houver uma pausa pendente para runID, materializa
// running→paused via o [StateGate] (AOS-017) e devolve (true, nil); caso contrário não
// faz nada e devolve (false, nil). É isto que concretiza a PAUSA GRACIOSA — um pause
// emitido a meio de um turno só toma efeito depois de todas as activities do turno
// terem confirmado, sem deixar efeitos parciais.
//
// A transição de estado é o facto durável autoritativo (evento run.state.transition de
// AOS-017); a projecção pauseRequested mantém-se até uma retoma (a pausa continua "em
// efeito"), pelo que uma chamada repetida enquanto ainda running tentaria pausar de
// novo — o loop chama-a UMA vez por turno, ao cruzar a fronteira. Idempotência ao nível
// da máquina: uma segunda tentativa a partir de paused seria recusada pela tabela
// declarativa (paused→paused não é válida).
func (c *SteerChannel) GracefulPause(ctx context.Context, runID string, gate StateGate) (bool, error) {
	if gate == nil {
		return false, ErrNilGate
	}
	if !c.PendingPause(runID) {
		return false, nil
	}
	if err := gate.Pause(ctx, ReasonGracefulPause); err != nil {
		return false, err
	}
	return true, nil
}

// Resume RETOMA um run paused: autentica o emissor, consome a correcção pendente,
// materializa paused→running via o [StateGate] (AOS-017) e grava o evento append-only
// `control.resume` (não-repúdio: quem retomou). Devolve a [Correction] a aplicar —
// uma instrução CONFIÁVEL (taint trusted), NÃO um dado untrusted. Se não havia
// correcção pendente, devolve uma Correction com Present=false (retoma limpa).
//
// # Ordem (durabilidade) — AUDIT-FIRST
//
// O evento de audit control.resume é gravado ANTES de a transição paused→running ser
// materializada. Esta ordem torna a retoma consistente sob falha do audit: se o append
// falha, NADA transita — a máquina fica paused e a correcção pendente fica INTACTA, e a
// projecção do canal iguala o que [SteerChannel.Rebuild] produz do log (sem re-pausa
// espúria nem re-aplicação da correcção num worker que recupere por replay). A ordem
// inversa (transitar primeiro) deixaria, no crash entre a transição e o audit, a
// máquina em running mas o log sem control.resume — divergência de replay e lacuna de
// não-repúdio (quem retomou ficaria só na projecção volátil).
//
// A retoma é recusada ANTES de qualquer escrita quando NÃO há pausa pendente na
// projecção (não há o que retomar): rejeita com [state.ErrInvalidTransition] sem tocar
// no log nem consumir a correcção, espelhando a recusa da aresta running→running da
// tabela declarativa de AOS-017. Se o audit tiver sucesso mas a materialização da
// transição falhar (falha do Event Store de estado), o control.resume já é durável e a
// máquina permanece paused — o lado SEGURO (fail-closed): uma retoma futura reconcilia.
//
// Rejeita [ErrUnauthenticated] se a assinatura do emissor não validar (fronteira de
// segurança: conteúdo untrusted não pode retomar o run).
func (c *SteerChannel) Resume(ctx context.Context, runID string, emitter Emitter, gate StateGate) (Correction, error) {
	if runID == "" {
		return Correction{}, ErrEmptyRunID
	}
	if emitter.ID == "" {
		return Correction{}, ErrEmptyEmitterID
	}
	if gate == nil {
		return Correction{}, ErrNilGate
	}
	// FRONTEIRA DE SEGURANÇA (ADR-013/005): a retoma é autenticada como qualquer sinal.
	if err := c.auth.Authenticate(ctx, runID, SignalResume, nil, emitter); err != nil {
		return Correction{}, ErrUnauthenticated
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	rc := c.runState(runID)

	// (0) Sem pausa pendente na projecção ⇒ não há o que retomar. Rejeita SEM tocar no
	// log nem consumir a correcção (mantém a correcção pendente intacta perante uma
	// retoma indevida de um run não pausado).
	if !rc.pauseRequested {
		return Correction{}, state.ErrInvalidTransition
	}

	corr := Correction{Present: rc.hasCorrection}
	if rc.hasCorrection {
		corr.Value = make([]byte, len(rc.correction))
		copy(corr.Value, rc.correction)
		corr.EmitterID = rc.correctionBy
	}

	// (1) AUDIT-FIRST: grava control.resume (com a identidade do emissor — não-repúdio)
	// ANTES da transição. Se falhar, nada transita: máquina paused, correcção intacta,
	// projecção == Rebuild.
	rec := c.newRecord(SignalResume, emitter, nil)
	if err := c.appendControl(ctx, runID, rc, rec); err != nil {
		return Correction{}, err
	}
	// O control.resume é agora durável; a projecção TEM de o dobrar para que o estado
	// in-memory iguale sempre o que Rebuild reconstrói do log.
	c.apply(rc, rec)

	// (2) Materializa paused→running (AOS-017). Se recusada aqui, o control.resume já é
	// durável e a correcção já foi consumida; a máquina fica paused (fail-closed) e uma
	// retoma futura reconcilia. O erro é devolvido ao chamador.
	if err := gate.Resume(ctx, ReasonSteerResume); err != nil {
		return Correction{}, err
	}

	c.emitSpan(ctx, runID, SignalResume, emitter.ID)
	return corr, nil
}
