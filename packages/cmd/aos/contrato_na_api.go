package main

// O CONTRATO DE CONCLUSÃO NA API DO NÓ (AOS-494).
//
// O AOS-493 pôs o desfecho de um run a ser um veredicto do kernel sobre um contrato de
// conclusão, mas o contrato só entrava por código. Este ficheiro é a superfície HTTP dele:
//
//   - o `POST /runs` aceita `completion_requires` e valida-o contra a lista-branca do run;
//   - o `GET /tools` anuncia que o nó o aceita, para quem submete saber ANTES de submeter;
//   - o `GET /runs/{id}` devolve a razão e o vector do veredicto, e o ramo durável passa a
//     devolver também a saída.
//
// O MODO NÃO VEM DO CORPO. Quem submete declara de que tools a conclusão depende; o que o nó
// faz com o veredicto (`AOS_COMPLETION_VERDICT`) é do nó, e fixa-se por run no arranque do run.

import (
	"context"
	"errors"
	"net/http"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// Mensagens das recusas do contrato no `POST /runs`. São do pedido e não do run: dizem a quem
// submete o que está mal no corpo que enviou, com nomes que ele próprio escreveu.
const (
	erroContratoComEntradaVazia = "completion_requires com entrada vazia"
	erroContratoSemListaBranca  = "completion_requires exige a lista-branca do run (tools)"
	erroContratoForaDaLista     = "completion_requires exige uma tool fora da lista-branca do run (tools)"
)

// validarContratoDeConclusao verifica o contrato de conclusão de um `POST /runs` contra a
// lista-branca do MESMO pedido. Devolve a mensagem da recusa, ou vazio.
//
// A pertença decide-se por [referencemonitor.RunAllowsTool], a regra que o Reference Monitor
// impõe em cada chamada: comparação exacta, sem aparar nem normalizar a caixa. Um contrato que
// exigisse uma tool que a mediação recusa nunca se cumpria, e o run gastava os turnos todos
// para sair «o modelo não chamou» de um defeito de quem compôs o pedido.
//
// SEM LISTA-BRANCA NÃO HÁ CONTRATO. `tools` ausente quer dizer «sem restrição além do token»;
// aceitar um contrato aí era aceitar que ele «pertence» a uma lista que não existe. Quem declara
// de que tools a conclusão depende sabe que tools o run tem.
//
// O QUE ISTO NÃO VÊ: se o nó oferece a tool. O tool set do run é congelado no arranque do run,
// e não na porta; essa metade é do kernel ([agentruntime.ErrImpossibleCompletionContract]) e
// chega a quem submete pelo `GET /runs/{id}`.
func validarContratoDeConclusao(contrato, listaBranca []string) string {
	if len(contrato) == 0 {
		return ""
	}
	for _, tool := range contrato {
		if tool == "" {
			return erroContratoComEntradaVazia
		}
	}
	if listaBranca == nil {
		return erroContratoSemListaBranca
	}
	for _, tool := range contrato {
		if !referencemonitor.RunAllowsTool(listaBranca, tool) {
			return erroContratoForaDaLista
		}
	}
	return ""
}

// anuncioDoContrato é o que o `GET /tools` diz sobre o contrato de conclusão. A PRESENÇA do
// objecto é o anúncio: este nó aceita `completion_requires` no `POST /runs`. Um nó anterior
// não o devolve, e quem submete não envia o campo — o decoder desse nó recusava-o com 400.
type anuncioDoContrato struct {
	// Mode é o que o nó faz com o veredicto dos runs novos (`observe`, `enforce` ou `off`). É
	// informação: quem submete não decide nada por ela, e o desfecho de cada run vem no
	// `GET /runs/{id}`.
	Mode agentruntime.CompletionMode `json:"mode"`
}

// anuncioDoContratoDoNo compõe o anúncio com o modo em vigor no nó.
func anuncioDoContratoDoNo(n *Node) *anuncioDoContrato {
	modo := defaultCompletionVerdict
	if n != nil && n.completionVerdict != "" {
		modo = n.completionVerdict
	}
	return &anuncioDoContrato{Mode: modo}
}

// veredictoNaResposta escreve na resposta do `GET /runs/{id}` o veredicto do kernel: o vector
// inteiro e, quando é negativo, a razão. Os dois ramos do `GET` chamam esta função, para que o
// desfecho em memória e o durável não possam dizer a razão de maneiras diferentes.
func veredictoNaResposta(resp *runStateResponse, v *agentruntime.Verdict) {
	if v == nil {
		return
	}
	resp.Verdict = v
	if !v.Fulfilled {
		resp.OutcomeReason = string(v.Reason)
	}
}

// DurableOutcome devolve o estado durável do run e o veredicto gravado na sua última transição
// (AOS-494). É o [NodeService.DurableState] com a razão: os dois saem do mesmo evento.
func (s *NodeService) DurableOutcome(ctx context.Context, runID string) (state.State, *agentruntime.Verdict, error) {
	if s.node == nil || s.node.stateGates == nil {
		return "", nil, nil
	}
	return s.node.stateGates.currentOutcome(ctx, runID)
}

// currentOutcome é o [runStateGates.currentState] com o veredicto da última transição.
func (g *runStateGates) currentOutcome(ctx context.Context, runID string) (state.State, *agentruntime.Verdict, error) {
	m, err := state.NewMachine(g.store, runID)
	if err != nil {
		return "", nil, err
	}
	return m.RebuildOutcome(ctx)
}

// errSaidaDuravelSemGate — o ramo durável do `GET /runs/{id}` não lê a saída: o gate soberano
// de leitura ou a cifra por-titular não estão compostos neste nó.
var errSaidaDuravelSemGate = errors.New("aos: a saida duravel do run so se le atras do gate soberano de leitura, com a cifra por-titular composta")

// errSaidaDuravelSemTurnos — a reconstrução não devolveu turno nenhum.
var errSaidaDuravelSemTurnos = errors.New("aos: a reconstrucao do run nao devolveu turnos")

// saidaDuravel lê do LOG a saída de um run concluído que já não está na memória deste processo
// (AOS-494): o texto do turno que o terminou, da captura desse turno.
//
// # De onde vem, e porque é legítimo lê-la aqui
//
// O `final_text` do ramo em memória vive num registo de desfechos que um reinício do nó (ou a
// poda FIFO) esvazia. A fonte durável é a captura do turno terminal, CIFRADA POR-TITULAR
// (AOS-093). Abre-se pelo MESMO motor e com a MESMA autorização da reconstrução soberana
// ([apiHandler.newReaderReplayEngine], AOS-214): o leitor já passou o gate D7 deste `GET`, a
// leitura já foi selada no WORM (D6), e é o mesmo leitor a quem o ramo em memória entrega este
// mesmo texto. Não é uma via nova para o conteúdo: é a mesma resposta, depois de um reinício.
//
// # Quando não se lê
//
//   - sem o gate soberano de leitura (nó legado, que não autentica ninguém) ou sem a cifra
//     por-titular: decifrar conteúdo de um titular para um chamador que ninguém autenticou
//     seria abrir uma via sem gate — a mesma recusa do `GET /runs/{id}/reconstruct`;
//   - quando a captura não abre: o titular foi apagado (`/dsar/erase`), a captura está
//     incompleta, a custódia não responde, ou o run não correu com execução durável.
//
// Em todos estes casos devolve erro, e o `GET` responde `output_unavailable` em vez de um
// `completed` sem texto: quem lê distingue «o run não escreveu nada» de «o nó já não consegue
// servir o que o run escreveu».
func (h *apiHandler) saidaDuravel(ctx context.Context, reader readerIdentity, runID string) (string, error) {
	if h.readGov == nil || h.node == nil || h.node.EventStore == nil || h.node.contentOpener == nil {
		return "", errSaidaDuravelSemGate
	}
	engine, err := h.newReaderReplayEngine(reader)
	if err != nil {
		return "", err
	}
	turnos, err := engine.Reconstruct(ctx, runID)
	if err != nil {
		return "", err
	}
	if len(turnos) == 0 {
		return "", errSaidaDuravelSemTurnos
	}
	// O estado durável é `complete`: o run concluiu no último turno gravado, e o texto final de
	// um run concluído é o texto desse turno ([agentruntime.ConcludeRun]).
	return turnos[len(turnos)-1].Response.Text, nil
}

// desfechoDuravelNaResposta completa a resposta do ramo durável de um run `complete` com a
// saída lida do log. Chama-se DEPOIS do selo de leitura sensível: é ele a pré-condição de
// servir conteúdo.
func (h *apiHandler) desfechoDuravelNaResposta(r *http.Request, reader readerIdentity, resp *runStateResponse) {
	texto, err := h.saidaDuravel(r.Context(), reader, resp.RunID)
	if err != nil {
		resp.OutputUnavailable = true
		// A causa fica no log do operador, sem conteúdo: o erro da reconstrução nunca o leva.
		h.logf("GET /runs/%q (AOS-494): run concluido cuja saida NAO se le do log — responde output_unavailable: %v", resp.RunID, err)
		return
	}
	resp.FinalText = texto
}
