package modelgateway

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/port"
)

// O ESTADO OPACO DO PROVIDER (AOS-514, ADR-040).
//
// Com a captura ligada, o adaptador HTTP tira do corpo cru de cada resposta o que o provider
// mandou e pode exigir de volta ([port.ProviderState]): os campos de raciocínio em todos os
// nomes, os blocos assinados e os redigidos, e o id e a assinatura de cada tool call. O
// adaptador do runtime fecha-o num envelope com a rota a que pertence e um nonce
// ([port.ProviderStateEnvelope]) e entrega os bytes em [agentruntime.ModelResponse.State], de
// onde o loop os leva para a captura selada do turno.
//
// ÀS ESCURAS. Nada disto volta ao provider: o pedido continua a sair por
// [port.ChatRequest.MarshalWire], que não conhece o estado e retira o raciocínio de todas as
// mensagens. Devolvê-lo é o AOS-515.

// Modos de [ProductionConfig.ProviderState].
const (
	// ProviderStateOff — a sonda não corre. É a omissão, e o gateway é o de antes.
	ProviderStateOff = "off"
	// ProviderStateCapture — cada resposta de chat síncrona leva o seu estado opaco.
	ProviderStateCapture = "capture"
)

// ErrBadProviderState — o modo da captura do estado não é do vocabulário fechado.
var ErrBadProviderState = errors.New("modelgateway: modo da captura do estado opaco do provider invalido — valores aceites: off, capture")

// ParseProviderState valida o modo da captura do estado. O vazio NÃO é aceite aqui: quem lê a
// configuração decide a omissão.
func ParseProviderState(mode string) (string, error) {
	switch m := strings.TrimSpace(mode); m {
	case ProviderStateOff, ProviderStateCapture:
		return m, nil
	default:
		return "", fmt.Errorf("%w (veio %q)", ErrBadProviderState, mode)
	}
}

// Tectos do envelope de estado de um turno, em bytes.
const (
	// DefaultProviderStateMaxBytes é o tecto de um nó que não o configura: 64 KiB.
	DefaultProviderStateMaxBytes = 64 << 10
	// MinProviderStateMaxBytes é o menor tecto configurável: abaixo dele nem o envelope de um
	// bloco assinado pequeno cabia, e a captura ligada não capturava nada.
	MinProviderStateMaxBytes = 1 << 10
	// MaxProviderStateMaxBytes é o maior tecto configurável: o tecto absoluto do runtime
	// ([agentruntime.MaxProviderStateBytes], 96 KiB), acima do qual o estado nem chegaria à
	// captura. A razão do número — três passagens por base64 e o raciocínio duas vezes na
	// captura, medido com o selador real — está no comentário dessa constante.
	MaxProviderStateMaxBytes = agentruntime.MaxProviderStateBytes
)

// ErrBadProviderStateMaxBytes — o tecto está fora de
// [[MinProviderStateMaxBytes], [MaxProviderStateMaxBytes]].
var ErrBadProviderStateMaxBytes = errors.New("modelgateway: tecto de bytes do estado opaco do provider fora do intervalo aceite (1024 a 98304)")

// Resultados da captura do estado de um turno, no vocabulário FECHADO que a métrica do nó
// rotula. Um turno cuja resposta não traz estado não tem resultado: não é contado.
const (
	// ProviderStateResultCaptured — o envelope coube no tecto e seguiu para a captura.
	ProviderStateResultCaptured = "capturado"
	// ProviderStateResultOverCeiling — o envelope excedia o tecto. Não foi guardado nem
	// truncado: o turno leva a marca de «não devolvível».
	ProviderStateResultOverCeiling = "nao_devolvivel_tecto"
	// ProviderStateResultNoNonce — não foi possível obter o nonce do envelope. Sem nonce o
	// digest seria confirmável por tentativas, pelo que o estado não é guardado.
	ProviderStateResultNoNonce = "nao_devolvivel_nonce"
	// ProviderStateResultMisaligned — a sonda do estado e o descodificador da resposta não
	// viram a mesma mensagem ([port.ProbeProviderStateFor]): um corpo anómalo, com chaves
	// noutra caixa ou `message` repetida. Um estado desalinhado nunca é guardado.
	ProviderStateResultMisaligned = "nao_devolvivel_desalinhado"
)

// ProviderStateResults devolve o vocabulário de resultados, numa ordem fixa.
func ProviderStateResults() []string {
	return []string{ProviderStateResultCaptured, ProviderStateResultOverCeiling, ProviderStateResultNoNonce, ProviderStateResultMisaligned}
}

// ProviderStateObserver recebe, por cada turno cuja resposta trouxe estado, o resultado da
// captura (vocabulário fechado de [ProviderStateResults]). Nenhum byte do estado, e nem o seu
// tamanho — esse vai na ficha do turno, quando a medição da forma está ligada.
type ProviderStateObserver func(resultado string)

// capturaDoEstado é a configuração da captura no adaptador do runtime.
type capturaDoEstado struct {
	maxBytes int
	obs      ProviderStateObserver
	// nonce preenche b com bytes aleatórios. É `crypto/rand.Read`; os testes deste pacote
	// trocam-no para terem envelopes deterministas.
	nonce func(b []byte) error
}

// WithProviderStateCapture liga a captura do estado opaco do provider no adaptador do runtime
// (AOS-514): o estado que a resposta traga ([port.ChatResponse.State], que só o adaptador HTTP
// com a sonda ligada preenche) é fechado num envelope e entregue em
// [agentruntime.ModelResponse.State].
//
// maxBytes é o tecto do envelope por turno. Um envelope maior NÃO é truncado — uma assinatura
// cortada é inválida —: o turno leva [agentruntime.ProviderStateNotReturnable], sem bytes, e
// segue como um turno sem estado. Um tecto fora de [[MinProviderStateMaxBytes],
// [MaxProviderStateMaxBytes]] é ignorado aqui — fica [DefaultProviderStateMaxBytes] —, e é a
// quem lê a configuração que cabe recusá-lo ([ValidateProviderStateMaxBytes]). obs pode ser nil.
func WithProviderStateCapture(maxBytes int, obs ProviderStateObserver) RuntimeAdapterOption {
	return func(a *ModelClientAdapter) {
		if ValidateProviderStateMaxBytes(maxBytes) != nil {
			maxBytes = DefaultProviderStateMaxBytes
		}
		a.estado = &capturaDoEstado{maxBytes: maxBytes, obs: obs, nonce: func(b []byte) error {
			_, err := rand.Read(b)
			return err
		}}
	}
}

// ValidateProviderStateMaxBytes diz se maxBytes é um tecto aceite.
func ValidateProviderStateMaxBytes(maxBytes int) error {
	if maxBytes < MinProviderStateMaxBytes || maxBytes > MaxProviderStateMaxBytes {
		return fmt.Errorf("%w (veio %d)", ErrBadProviderStateMaxBytes, maxBytes)
	}
	return nil
}

// capturar fecha o estado de resp num envelope e põe-no em out.State. Com a captura desligada
// (c nil) ou uma resposta sem estado não faz nada: out fica como estava.
//
// O envelope liga o estado à ROTA: o digest do perfil com que o turno foi comparado, o nome
// pedido e o modelo que serviu — os mesmos valores que o turno grava no manifesto. Quando a
// ficha da forma existe (AOS-507), ganha o resultado e o tamanho do envelope.
func (c *capturaDoEstado) capturar(out *agentruntime.ModelResponse, resp port.ChatResponse, pedido string) {
	if c == nil || resp.State == nil {
		return
	}
	resultado, tamanho := c.fechar(out, resp, pedido)
	if out.Shape != nil && !out.Shape.Unreadable {
		out.Shape.ProviderState = string(out.State.Status)
		out.Shape.ProviderStateBytes = int64(tamanho)
	}
	if c.obs != nil {
		c.obs(resultado)
	}
}

func (c *capturaDoEstado) fechar(out *agentruntime.ModelResponse, resp port.ChatResponse, pedido string) (resultado string, tamanho int) {
	naoDevolvivel := &agentruntime.ProviderState{Status: agentruntime.ProviderStateNotReturnable}
	if resp.State.Misaligned {
		out.State = naoDevolvivel
		return ProviderStateResultMisaligned, 0
	}
	env := port.ProviderStateEnvelope{
		Version:            port.ProviderStateEnvelopeVersion,
		Nonce:              make([]byte, port.ProviderStateNonceBytes),
		RouteProfileDigest: agentruntime.NormalizeRouteProfileDigest(out.RouteProfileDigest),
		RequestedModel:     modeloServido(pedido),
		ServedModel:        out.Model,
		// O resultado da comparação da rota DESTE turno (vazio sem governação). Fechado no
		// vocabulário do runtime antes de entrar no envelope.
		RouteCheck:    string(out.RouteCheck.Normalizado()),
		ProviderState: *resp.State,
	}
	if err := c.nonce(env.Nonce); err != nil {
		out.State = naoDevolvivel
		return ProviderStateResultNoNonce, 0
	}
	cru, err := env.Marshal()
	if err != nil {
		// Só acontece com um nonce do tamanho errado, que a linha acima exclui.
		out.State = naoDevolvivel
		return ProviderStateResultNoNonce, 0
	}
	if len(cru) > c.maxBytes {
		out.State = naoDevolvivel
		return ProviderStateResultOverCeiling, len(cru)
	}
	out.State = &agentruntime.ProviderState{Bytes: cru, Status: agentruntime.ProviderStateCaptured}
	return ProviderStateResultCaptured, len(cru)
}
