package modelgateway

import (
	"errors"
	"fmt"
	"strings"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/port"
)

// A FORMA DA RESPOSTA DO PROVIDER (AOS-507).
//
// Com a medição ligada, o adaptador HTTP calcula do corpo cru de cada resposta de chat uma ficha
// ([port.ResponseShape]) — que campos vieram, em que forma JSON e com quantos bytes, sem nenhum
// byte de valor — e o adaptador do runtime leva-a para [agentruntime.ModelResponse.Shape], de
// onde o loop a grava no `turn.recorded`. É medição: nada no gateway nem no runtime decide com
// ela, e por isso não há modo de imposição.

// Modos de [ProductionConfig.ResponseShape].
const (
	// ResponseShapeOff — a sonda não corre. É a omissão, e o gateway é o de antes.
	ResponseShapeOff = "off"
	// ResponseShapeObserve — cada resposta de chat síncrona leva a ficha da sua forma.
	ResponseShapeObserve = "observe"
)

// ErrBadResponseShape — o modo da medição da forma não é do vocabulário fechado.
var ErrBadResponseShape = errors.New("modelgateway: modo da medicao da forma da resposta invalido — valores aceites: off, observe")

// ParseResponseShape valida o modo da medição da forma. O vazio NÃO é aceite aqui: quem lê a
// configuração decide a omissão.
func ParseResponseShape(mode string) (string, error) {
	switch m := strings.TrimSpace(mode); m {
	case ResponseShapeOff, ResponseShapeObserve:
		return m, nil
	default:
		return "", fmt.Errorf("%w (veio %q)", ErrBadResponseShape, mode)
	}
}

// ResponseShapeObserver recebe, por cada turno com ficha, os três valores de vocabulário fechado
// que a métrica do nó rotula: a forma do conteúdo, o campo do raciocínio e o motivo de paragem
// normalizado. Uma ficha ilegível chega com content [agentruntime.ShapeUnreadable] e reasoning
// [agentruntime.ShapeReasoningNone].
type ResponseShapeObserver func(content, reasoning string, stop agentruntime.StopReason)

// WithResponseShapeObserver liga um observador das fichas ao adaptador do runtime (AOS-507). Só
// é chamado em turnos cuja resposta traz ficha — com a medição desligada, nunca. fn nil ⇒ opção
// inerte.
func WithResponseShapeObserver(fn ResponseShapeObserver) RuntimeAdapterOption {
	return func(a *ModelClientAdapter) {
		if fn != nil {
			a.formaObs = fn
		}
	}
}

// fichaDoRuntime traduz a ficha da porta na do runtime. É uma cópia campo a campo: os dois
// vocabulários têm os mesmos textos, e o runtime fecha-os outra vez à entrada do registo
// ([agentruntime.ResponseShape.Normalizado]).
func fichaDoRuntime(s *port.ResponseShape) *agentruntime.ResponseShape {
	if s == nil {
		return nil
	}
	if s.Unreadable {
		return &agentruntime.ResponseShape{Unreadable: true}
	}
	simNao := func(b bool) string {
		if b {
			return agentruntime.ShapeYes
		}
		return agentruntime.ShapeNo
	}
	out := &agentruntime.ResponseShape{
		Content:                 s.Content,
		ContentBytes:            s.ContentBytes,
		Reasoning:               s.Reasoning,
		ReasoningForm:           s.ReasoningForm,
		ReasoningBytes:          s.ReasoningBytes,
		ReasoningSigned:         simNao(s.ReasoningSigned),
		Refusal:                 s.Refusal,
		ProviderFieldsRefusal:   s.PSFRefusal,
		ProviderFieldsReasoning: s.PSFReasoning,
		ToolCallsN:              s.ToolCallsN,
		ToolCallID:              s.ToolCallID,
		ToolCallIDMaxBytes:      s.ToolCallIDMaxBytes,
		ArgumentsForm:           s.ArgumentsForm,
		LegacyFunctionCall:      simNao(s.LegacyFunctionCall),
		ChoicesN:                s.ChoicesN,
		FinishReasonMapped:      simNao(s.FinishReasonMapped),
		SystemFingerprint:       simNao(s.SystemFingerprint),
		UnknownKeysN:            s.UnknownKeysN,
		ShapeDigest:             s.Digest,
	}
	if s.HasReasoningTokens {
		v := s.ReasoningTokens
		out.ReasoningTokens = &v
	}
	return out
}

// observarForma entrega ao observador os rótulos da ficha de um turno.
func observarForma(obs ResponseShapeObserver, ficha *agentruntime.ResponseShape, stop agentruntime.StopReason) {
	if obs == nil || ficha == nil {
		return
	}
	f := ficha.Normalizado()
	if f.Unreadable {
		obs(agentruntime.ShapeUnreadable, agentruntime.ShapeReasoningNone, stop.Normalizado())
		return
	}
	obs(f.Content, f.Reasoning, stop.Normalizado())
}
