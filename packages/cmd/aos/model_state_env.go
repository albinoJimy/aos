package main

// O ESTADO OPACO DO PROVIDER (AOS-514, ADR-040) — `AOS_MODEL_PROVIDER_STATE` e
// `AOS_MODEL_PROVIDER_STATE_MAX_BYTES`.
//
// Um provider devolve com cada resposta material que o runtime não interpreta e que o mesmo
// provider pode exigir de volta no turno seguinte: o raciocínio em todos os nomes em que veio,
// os blocos assinados e os redigidos, o id que deu a cada tool call. Com a captura ligada, o
// gateway tira-o do corpo cru, byte a byte, e o runtime guarda-o SELADO na captura do turno e
// refere-o no tail por digest.
//
//   - `off` (por omissão): a sonda não corre. Pedidos, eventos, capturas, `prompt_hash` e
//     `/metrics` são os de antes, e os runs novos ficam no layout de sempre.
//   - `capture`: cada turno ao vivo cuja resposta traz estado guarda-o na captura; os runs NOVOS
//     ficam no layout 1.5.0, que é o de sempre mais o rótulo `state_digest` nos turnos com
//     estado; `aos_model_provider_state_total` conta os resultados.
//
// ÀS ESCURAS: em nenhum dos modos o estado volta ao provider. Não há `enforce`: é transporte e
// não decide nada. RECUO: voltar a `off`. Os runs começados em 1.5.0 continuam nela até ao fim
// (o layout é fixado por run) e reproduzem-se como foram gravados; um binário anterior a este
// ticket não os sabe retomar nem reproduzir.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// defaultModelProviderState é o modo de um nó que não define AOS_MODEL_PROVIDER_STATE.
const defaultModelProviderState = modelgateway.ProviderStateOff

// ErrBadModelProviderState — AOS_MODEL_PROVIDER_STATE está definida com um valor fora do
// vocabulário. O nó recusa arrancar: um valor mal escrito não pode cair em silêncio para `off`.
var ErrBadModelProviderState = errors.New("aos: AOS_MODEL_PROVIDER_STATE invalida — valores aceites: off (por omissao; a sonda nao corre) e capture (o estado opaco do provider fica selado na captura de cada turno; nada e reenviado)")

// ErrBadModelProviderStateMaxBytes — AOS_MODEL_PROVIDER_STATE_MAX_BYTES não é um inteiro dentro
// do intervalo aceite.
var ErrBadModelProviderStateMaxBytes = errors.New("aos: AOS_MODEL_PROVIDER_STATE_MAX_BYTES invalida — um inteiro de 1024 a 262144 (bytes do envelope de estado por turno; por omissao 65536)")

// parseModelProviderStateFromEnv lê AOS_MODEL_PROVIDER_STATE e AOS_MODEL_PROVIDER_STATE_MAX_BYTES.
// Vazias ⇒ [defaultModelProviderState] e [modelgateway.DefaultProviderStateMaxBytes]. O tecto é
// validado mesmo com a captura desligada: um valor mal escrito não fica à espera do dia em que
// alguém a ligue.
func parseModelProviderStateFromEnv() (mode string, maxBytes int, err error) {
	mode = defaultModelProviderState
	if raw := strings.TrimSpace(os.Getenv("AOS_MODEL_PROVIDER_STATE")); raw != "" {
		if mode, err = modelgateway.ParseProviderState(raw); err != nil {
			return "", 0, fmt.Errorf("%w (veio %q)", ErrBadModelProviderState, raw)
		}
	}
	maxBytes = modelgateway.DefaultProviderStateMaxBytes
	if raw := strings.TrimSpace(os.Getenv("AOS_MODEL_PROVIDER_STATE_MAX_BYTES")); raw != "" {
		n, perr := strconv.Atoi(raw)
		if perr != nil || modelgateway.ValidateProviderStateMaxBytes(n) != nil {
			return "", 0, fmt.Errorf("%w (veio %q)", ErrBadModelProviderStateMaxBytes, raw)
		}
		maxBytes = n
	}
	return mode, maxBytes, nil
}

// modelProviderStateFromEnv lê a captura do estado para a composição do gateway: o modo, os
// contadores do `/metrics` e a opção do adaptador. Com `off` os contadores são nil e não há
// opção — o adaptador é o de sempre.
func modelProviderStateFromEnv() (string, *contadoresDoEstado, []modelgateway.RuntimeAdapterOption, error) {
	mode, maxBytes, err := parseModelProviderStateFromEnv()
	if err != nil {
		return "", nil, nil, err
	}
	if mode != modelgateway.ProviderStateCapture {
		return mode, nil, nil, nil
	}
	contadores := novosContadoresDoEstado()
	return mode, contadores, []modelgateway.RuntimeAdapterOption{modelgateway.WithProviderStateCapture(maxBytes, contadores.observar)}, nil
}

// modelProviderStateBanner é a linha de arranque da captura. Com `off`, ou sem gateway composto,
// não sai linha nenhuma.
func modelProviderStateBanner(gatewayComposed bool, mode string, maxBytes int) []string {
	if !gatewayComposed || mode != modelgateway.ProviderStateCapture {
		return nil
	}
	return []string{fmt.Sprintf("estado opaco do provider em captura (EPIC-06/AOS-514, ADR-040): AOS_MODEL_PROVIDER_STATE=capture — o raciocinio em todos os nomes, os blocos assinados e redigidos e o id de tool call do provider ficam SELADOS na captura de cada turno ao vivo, byte a byte, e o tail refere-os por digest (runs novos no layout %s). Nada e reenviado ao provider. Tecto por turno: %d bytes; acima dele o estado nao e truncado, fica marcado como nao devolvivel e conta em aos_model_provider_state_total. So o caminho sincrono; so o no. Recuo: off",
		agentruntime.AssemblyVersion150, maxBytes)}
}

// modelProviderStateBannerFromEnv relê o ambiente para declarar a captura no arranque. O valor
// já foi validado em [parseModelFromEnv]; um erro aqui não acrescenta linha.
func modelProviderStateBannerFromEnv(gatewayComposed bool) []string {
	mode, maxBytes, err := parseModelProviderStateFromEnv()
	if err != nil {
		return nil
	}
	return modelProviderStateBanner(gatewayComposed, mode, maxBytes)
}

// contadoresDoEstado conta, por processo, os turnos ao vivo cuja resposta trouxe estado opaco,
// pelo resultado da captura — a família `aos_model_provider_state_total`. Três séries, de
// vocabulário fechado ([modelgateway.ProviderStateResults]).
type contadoresDoEstado struct {
	resultados []string
	total      map[string]*atomic.Int64
}

func novosContadoresDoEstado() *contadoresDoEstado {
	c := &contadoresDoEstado{resultados: modelgateway.ProviderStateResults(), total: map[string]*atomic.Int64{}}
	for _, r := range c.resultados {
		c.total[r] = new(atomic.Int64)
	}
	return c
}

// observar é o [modelgateway.ProviderStateObserver]. Um resultado fora do vocabulário não é
// contado: nunca se rotula uma série com o texto recebido.
func (c *contadoresDoEstado) observar(resultado string) {
	if c == nil {
		return
	}
	if n := c.total[resultado]; n != nil {
		n.Add(1)
	}
}

// lido devolve o total de um resultado.
func (c *contadoresDoEstado) lido(resultado string) int64 {
	if c == nil || c.total[resultado] == nil {
		return 0
	}
	return c.total[resultado].Load()
}

// clienteComEstado é o cliente de modelo do nó com os contadores do estado à mão, no molde de
// [clienteComForma]: não muda nenhuma chamada, existe para o [Bootstrap] os encontrar — e é por
// eles existirem que o nó sabe que a captura está ligada.
type clienteComEstado struct {
	inner      agentruntime.ModelClient
	contadores *contadoresDoEstado
}

// Call implementa [agentruntime.ModelClient], sem tocar em nada.
func (c clienteComEstado) Call(ctx context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return c.inner.Call(ctx, view)
}

func (c clienteComEstado) estadoDoProvider() *contadoresDoEstado { return c.contadores }

// rotaDoModelo, formaDaResposta e respostasRecusadas passam adiante os contadores do cliente
// envolvido (AOS-505, AOS-507, AOS-509).
func (c clienteComEstado) rotaDoModelo() *contadoresDaRota     { return rotaDoCliente(c.inner) }
func (c clienteComEstado) formaDaResposta() *contadoresDaForma { return formaDoCliente(c.inner) }
func (c clienteComEstado) respostasRecusadas() *contadoresDeRejeicao {
	return rejeicoesDoCliente(c.inner)
}

// fonteDoEstado é o que um cliente de modelo (ou um decorador dele) expõe para os contadores do
// estado serem encontrados.
type fonteDoEstado interface {
	estadoDoProvider() *contadoresDoEstado
}

// estadoDoCliente devolve os contadores do estado do cliente de modelo dado, ou nil — a captura
// desligada, o modelo de referência, ou um cliente injectado que não os tem.
func estadoDoCliente(c agentruntime.ModelClient) *contadoresDoEstado {
	if f, ok := c.(fonteDoEstado); ok {
		return f.estadoDoProvider()
	}
	return nil
}

// layoutDosRunsNovos é o layout de montagem do prompt em que um run NOVO deste nó fica fixado:
// o de sempre ([agentruntime.AssemblyVersion]), ou — com a captura do estado ligada — a 1.5.0,
// que é a mesma montagem mais o rótulo `state_digest` nos turnos com estado. Um run retomado
// não passa por aqui: continua no layout do seu registo de retoma.
func layoutDosRunsNovos(capturaDoEstado bool) string {
	if capturaDoEstado {
		return agentruntime.AssemblyVersion150
	}
	return agentruntime.AssemblyVersion
}
