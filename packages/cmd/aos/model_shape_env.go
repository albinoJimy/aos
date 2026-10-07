package main

// A FORMA DA RESPOSTA DO PROVIDER (AOS-507) — `AOS_MODEL_RESPONSE_SHAPE`.
//
// Uma resposta que gasta tokens de saída e chega com o texto vazio dá sempre o mesmo registo,
// qualquer que tenha sido o corpo que o provider mandou. Com a medição ligada, o gateway calcula
// do corpo cru de cada resposta uma FICHA — que campos vieram, em que forma JSON e com quantos
// bytes, em vocabulário fechado e sem nenhum byte de valor — e o runtime grava-a em
// `response_shape` do `turn.recorded`.
//
//   - `off` (por omissão): a sonda não corre. Pedidos, eventos, capturas e `/metrics` são os de
//     antes.
//   - `observe`: cada turno ao vivo leva a ficha, e `aos_model_response_shape_total` conta-as.
//
// Não há `enforce`: é medição e não decide nada. RECUO: voltar a `off`. Os eventos já gravados com
// a ficha continuam legíveis — o campo é aditivo e um binário anterior ignora-o.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// defaultModelResponseShape é o modo de um nó que não define AOS_MODEL_RESPONSE_SHAPE.
const defaultModelResponseShape = modelgateway.ResponseShapeOff

// ErrBadModelResponseShape — AOS_MODEL_RESPONSE_SHAPE está definida com um valor fora do
// vocabulário. O nó recusa arrancar: um valor mal escrito não pode cair em silêncio para `off` e
// deixar o operador convencido de que está a medir.
var ErrBadModelResponseShape = errors.New("aos: AOS_MODEL_RESPONSE_SHAPE invalida — valores aceites: off (por omissao; a sonda nao corre) e observe (cada turno grava a ficha da forma da resposta, sem conteudo)")

// parseModelResponseShapeFromEnv lê AOS_MODEL_RESPONSE_SHAPE. Vazia ⇒
// [defaultModelResponseShape]. Um valor fora do vocabulário ⇒ [ErrBadModelResponseShape].
func parseModelResponseShapeFromEnv() (string, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_MODEL_RESPONSE_SHAPE"))
	if raw == "" {
		return defaultModelResponseShape, nil
	}
	mode, err := modelgateway.ParseResponseShape(raw)
	if err != nil {
		return "", fmt.Errorf("%w (veio %q)", ErrBadModelResponseShape, raw)
	}
	return mode, nil
}

// modelResponseShapeFromEnv lê a medição da forma para a composição do gateway: o modo, os
// contadores do `/metrics` e a opção do adaptador que os alimenta. Com `off` os contadores são
// nil e a opção é inerte — o adaptador é o de sempre.
func modelResponseShapeFromEnv() (string, *contadoresDaForma, modelgateway.RuntimeAdapterOption, error) {
	mode, err := parseModelResponseShapeFromEnv()
	if err != nil {
		return "", nil, nil, err
	}
	if mode != modelgateway.ResponseShapeObserve {
		return mode, nil, modelgateway.WithResponseShapeObserver(nil), nil
	}
	contadores := novosContadoresDaForma()
	return mode, contadores, modelgateway.WithResponseShapeObserver(contadores.observar), nil
}

// modelResponseShapeBanner é a linha de arranque da medição. Com `off`, ou sem gateway
// composto, não sai linha nenhuma.
func modelResponseShapeBanner(gatewayComposed bool, mode string) []string {
	if !gatewayComposed || mode != modelgateway.ResponseShapeObserve {
		return nil
	}
	return []string{"forma da resposta do provider em medicao (EPIC-06/AOS-507): AOS_MODEL_RESPONSE_SHAPE=observe — cada turno ao vivo grava em response_shape do turn.recorded a ficha do corpo da resposta (presenca, forma JSON e bytes de cada campo, em vocabulario fechado; nenhum valor e nenhum nome de chave do provider) e conta em aos_model_response_shape_total. Nao decide nada. So o caminho sincrono; so o no (as chamadas do aos-orq nao sao medidas). Recuo: off"}
}

// modelResponseShapeBannerFromEnv relê o ambiente para declarar a medição no arranque. O valor
// já foi validado em [parseModelFromEnv]; um erro aqui não acrescenta linha.
func modelResponseShapeBannerFromEnv(gatewayComposed bool) []string {
	mode, err := parseModelResponseShapeFromEnv()
	if err != nil {
		return nil
	}
	return modelResponseShapeBanner(gatewayComposed, mode)
}

// contadoresDaForma conta, por processo, os turnos ao vivo por (forma do conteúdo, campo do
// raciocínio, motivo de paragem) — a família `aos_model_response_shape_total`.
//
// CARDINALIDADE MÁXIMA: 300 séries. São 7 formas de conteúdo × 7 campos de raciocínio × 6 motivos
// de paragem (294), mais a ficha ilegível, que só existe com `reasoning="nenhum"` (6). Os três
// rótulos são de vocabulário fechado; um valor fora dele não é contado com o texto recebido.
type contadoresDaForma struct {
	conteudos   []string
	raciocinios []string
	motivos     []agentruntime.StopReason
	total       map[[3]string]*atomic.Int64
}

func novosContadoresDaForma() *contadoresDaForma {
	c := &contadoresDaForma{
		conteudos:   agentruntime.ShapeContents(),
		raciocinios: agentruntime.ShapeReasonings(),
		motivos:     agentruntime.StopReasons(),
		total:       map[[3]string]*atomic.Int64{},
	}
	for _, chave := range c.series() {
		c.total[chave] = new(atomic.Int64)
	}
	return c
}

// series devolve todas as séries da família, numa ordem fixa.
func (c *contadoresDaForma) series() [][3]string {
	var out [][3]string
	for _, m := range c.motivos {
		for _, ct := range c.conteudos {
			for _, r := range c.raciocinios {
				out = append(out, [3]string{ct, r, rotuloDoMotivo(m)})
			}
		}
		out = append(out, [3]string{agentruntime.ShapeUnreadable, agentruntime.ShapeReasoningNone, rotuloDoMotivo(m)})
	}
	return out
}

// observar é o [modelgateway.ResponseShapeObserver]. Um trio fora do vocabulário conta como
// ficha ilegível, e nunca com o texto recebido.
func (c *contadoresDaForma) observar(content, reasoning string, stop agentruntime.StopReason) {
	if c == nil {
		return
	}
	motivo := rotuloDoMotivo(stop.Normalizado())
	n := c.total[[3]string{content, reasoning, motivo}]
	if n == nil {
		n = c.total[[3]string{agentruntime.ShapeUnreadable, agentruntime.ShapeReasoningNone, motivo}]
	}
	n.Add(1)
}

// lido devolve o total de uma série.
func (c *contadoresDaForma) lido(chave [3]string) int64 {
	if c == nil || c.total[chave] == nil {
		return 0
	}
	return c.total[chave].Load()
}

// clienteComForma é o cliente de modelo do nó com os contadores da forma à mão, no molde de
// [clienteComRota]: não muda nenhuma chamada, existe para o [Bootstrap] os encontrar.
type clienteComForma struct {
	inner      agentruntime.ModelClient
	contadores *contadoresDaForma
}

// Call implementa [agentruntime.ModelClient], sem tocar em nada.
func (c clienteComForma) Call(ctx context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return c.inner.Call(ctx, view)
}

func (c clienteComForma) formaDaResposta() *contadoresDaForma { return c.contadores }

// rotaDoModelo passa adiante os contadores da rota do cliente envolvido (AOS-505).
func (c clienteComForma) rotaDoModelo() *contadoresDaRota { return rotaDoCliente(c.inner) }

// fonteDaForma é o que um cliente de modelo (ou um decorador dele) expõe para os contadores da
// forma serem encontrados.
type fonteDaForma interface {
	formaDaResposta() *contadoresDaForma
}

// formaDoCliente devolve os contadores da forma do cliente de modelo dado, ou nil — a medição
// desligada, o modelo de referência, ou um cliente injectado que não os tem.
func formaDoCliente(c agentruntime.ModelClient) *contadoresDaForma {
	if f, ok := c.(fonteDaForma); ok {
		return f.formaDaResposta()
	}
	return nil
}
