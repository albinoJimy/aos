package agentruntime

import (
	"context"

	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// CAPTURA DE NÃO-DETERMINISMO (AOS-016) — ponto de ligação ADITIVO.
//
// O loop base de AOS-013 grava o evento "turn.recorded" com o MANIFESTO (hashes,
// model-id/params/seed, versões pinadas) mas NÃO persiste os inputs
// não-determinísticos crus — a resposta do modelo (texto + tool calls) nem os
// outputs das tools. Sem eles o replay (AOS-016) consegue DETECTAR divergência
// (pelo prompt_hash) mas não RECONSTRUIR a trajectória.
//
// Este ficheiro fixa o PONTO DE LIGAÇÃO (interface opcional com default no-op)
// para que AOS-016 capture esses inputs de forma ADITIVA, sem alterar a forma do
// loop nem o contrato de AOS-013. O default [noopCapturer] não persiste nada — o
// comportamento de AOS-013 fica byte-idêntico quando nenhum capturer é injectado.
// A implementação real ([replay.EventStoreCapturer]) vive no subpacote replay.
// ---------------------------------------------------------------------------

// CapturedToolResult é o registo de UMA tool call despachada num turno, com a
// intenção original (a invocação pretendida pelo modelo) e o resultado observado
// (output untrusted + eventual erro de execução). É o que o replay precisa para
// devolver o resultado REGISTADO sem re-executar o efeito externo.
type CapturedToolResult struct {
	// Invocation é a tool call PRETENDIDA pelo modelo (ToolID/Capability/Input…).
	Invocation ToolInvocation
	// Result é o resultado devolvido ao loop (SEMPRE untrusted, ADR-005).
	Result Tainted
	// ToolError é o erro de execução da tool PERMITIDA (nil se não houve). É
	// materializado no tail do turno seguinte — o replay tem de o reproduzir para
	// o prompt_hash bater.
	ToolError error
}

// TurnCapture é o pacote de inputs não-determinísticos de um turno entregue ao
// [Capturer]: a resposta do modelo COMPLETA (texto + tool calls + uso + custo +
// final) e o resultado de cada tool call despachada. O relógio de captura é
// carimbado pelo próprio capturer (concern de captura, não do loop); o seed vem
// do manifesto por trajectória (model.seed).
type TurnCapture struct {
	RunID       string
	StepID      string
	Turn        int
	Response    ModelResponse
	ToolResults []CapturedToolResult
	Producer    eventstore.Producer
}

// Capturer persiste os inputs não-determinísticos de um turno para o replay
// determinístico (AOS-016). É o ponto de ligação ADITIVO: o default no-op não
// persiste nada, mantendo AOS-013 inalterado.
type Capturer interface {
	Capture(ctx context.Context, c TurnCapture) error
}

// noopCapturer é o default: não persiste nada. Preserva o comportamento de
// AOS-013 quando nenhum capturer é injectado.
type noopCapturer struct{}

func (noopCapturer) Capture(context.Context, TurnCapture) error { return nil }
