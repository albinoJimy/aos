package replay

import (
	"context"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// replayModelClient implementa [agentruntime.ModelClient] devolvendo a resposta do
// modelo REGISTADA no log para cada turno — NUNCA chama um modelo ao vivo. É a
// materialização do "o replay lê a resposta do modelo do log, nunca ao vivo".
//
// A resposta é indexada pelo nº do turno ([agentruntime.PromptView].Turn), o mesmo
// índice sob que a captura a gravou. Um turno sem captura devolve
// [ErrMissingCapture] (nunca uma chamada ao vivo como fallback).
type replayModelClient struct {
	byTurn map[int]agentruntime.ModelResponse
}

// newReplayModelClient constrói o cliente a partir das capturas indexadas por turno.
func newReplayModelClient(captures map[int]capturePayload) *replayModelClient {
	byTurn := make(map[int]agentruntime.ModelResponse, len(captures))
	for turn, capt := range captures {
		byTurn[turn] = capt.Response.decode()
	}
	return &replayModelClient{byTurn: byTurn}
}

// Call implementa [agentruntime.ModelClient]: devolve a resposta REGISTADA do turno.
func (c *replayModelClient) Call(_ context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	resp, ok := c.byTurn[view.Turn]
	if !ok {
		return agentruntime.ModelResponse{}, ErrMissingCapture
	}
	return resp, nil
}

// Verificação em tempo de compilação: o cliente de replay É um ModelClient — pode
// substituir o modelo ao vivo em qualquer ponto que aceite a porta, provando que o
// replay não precisa (nem tem) de um modelo real.
var _ agentruntime.ModelClient = (*replayModelClient)(nil)

// recordedResult é o resultado REGISTADO de uma tool call (valor untrusted + erro +
// negação sanitizada). A negação faz parte do registo porque o loop a materializa no
// tail: reproduzi-la é condição de o prompt_hash do replay bater com o original.
type recordedResult struct {
	value   agentruntime.Tainted
	toolErr error
	denial  *agentruntime.ToolDenial
}

// replayDispatcher devolve o resultado REGISTADO de cada tool call — NUNCA executa
// uma tool nem atravessa um Reference Monitor. É a materialização do "as activities
// devolvem o resultado registado (via ledger/captura), sem re-executar o efeito".
//
// Indexado por (turno, índice da tool call). O índice segue a ordem em que o loop
// despachou as tool calls do turno — a MESMA ordem em que a captura as gravou.
type replayDispatcher struct {
	byTurn map[int][]recordedResult
}

// newReplayDispatcher constrói o dispatcher a partir das capturas indexadas por turno.
func newReplayDispatcher(captures map[int]capturePayload) *replayDispatcher {
	byTurn := make(map[int][]recordedResult, len(captures))
	for turn, capt := range captures {
		results := make([]recordedResult, 0, len(capt.ToolResults))
		for _, tr := range capt.ToolResults {
			value, toolErr, denial := tr.decode()
			results = append(results, recordedResult{value: value, toolErr: toolErr, denial: denial})
		}
		byTurn[turn] = results
	}
	return &replayDispatcher{byTurn: byTurn}
}

// Dispatch devolve o resultado REGISTADO da idx-ésima tool call do turno. Nunca executa nada
// ao vivo.
//
// # O INVARIANTE, CORRIGIDO (AOS-289)
//
// Este comentário declarava «o motor garante idx < len(ToolCalls) porque itera sobre as tool
// calls da própria resposta registada». Era verdade e era IRRELEVANTE: a guarda abaixo testa
// `len(results)`, que são os TOOL RESULTS — outro número. Quando a captura era truncada (turno
// escalado), os índices sem registo caíam no fallback e o motor dobrava no tail um segmento
// untrusted vazio que o run original nunca produziu, com `Fidelity=1` a afirmá-lo.
//
// O invariante que agora VALE é o que [capturaCompleta] impõe no gate de admissão:
// `len(ToolCalls) <= len(ToolResults)` em cada turno, verificado antes de reproduzir seja o que
// for. O fallback fica como defesa em profundidade — inalcançável por [ReplayEngine.Replay],
// e fail-safe para qualquer chamador futuro que construa um dispatcher sem passar pelo gate.
func (d *replayDispatcher) Dispatch(turn, idx int) (agentruntime.Tainted, error, *agentruntime.ToolDenial) {
	results := d.byTurn[turn]
	if idx < 0 || idx >= len(results) {
		return agentruntime.Untrusted(nil), nil, nil
	}
	return results[idx].value, results[idx].toolErr, results[idx].denial
}
