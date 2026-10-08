package modelgateway

import (
	"encoding/json"
	"errors"

	"github.com/aos-ref/platform/model-gateway/port"
)

// A RESPOSTA RECUSADA, COM CAUSA (AOS-509).
//
// Uma resposta do provider que o gateway não consegue transformar num turno faz falhar o turno.
// A causa fica num vocabulário FECHADO, sem nenhum byte da resposta, para quem conta
// (`aos_model_response_rejected_total{causa}` no nó).

// Causas de uma resposta recusada.
const (
	// RejectContentPart — `content` em partes com uma parte que não é de texto.
	RejectContentPart = "content_parte_nao_texto"
	// RejectContentForm — `content` numa forma que não é string, `null` nem lista de partes.
	RejectContentForm = "content_forma"
	// RejectArgumentsForm — `function.arguments` numa forma que não é string, `null` nem objecto.
	RejectArgumentsForm = "arguments_forma"
	// RejectJSON — o corpo não é o JSON de uma resposta de chat (sintaxe, ou um campo com um
	// tipo que o contrato não aceita).
	RejectJSON = "json_invalido"
	// RejectNoChoices — a resposta não traz nenhuma escolha ([ErrRespostaSemChoices]).
	RejectNoChoices = "sem_choices"
)

// ResponseRejectionCauses devolve o vocabulário fechado das causas, numa ordem fixa.
func ResponseRejectionCauses() []string {
	return []string{RejectContentPart, RejectContentForm, RejectArgumentsForm, RejectJSON, RejectNoChoices}
}

// ResponseRejectionCause classifica o erro de um turno: devolve a causa quando o erro é o de uma
// RESPOSTA recusada, e vazio para qualquer outro (rede, um status que não é 200, uma negação da
// pipeline do gateway) — esses não são respostas recusadas.
func ResponseRejectionCause(err error) string {
	var sintaxe *json.SyntaxError
	var tipo *json.UnmarshalTypeError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, port.ErrContentPartNotText):
		return RejectContentPart
	case errors.Is(err, port.ErrContentForm):
		return RejectContentForm
	case errors.Is(err, port.ErrArgumentsForm):
		return RejectArgumentsForm
	case errors.Is(err, ErrRespostaSemChoices):
		return RejectNoChoices
	case errors.As(err, &sintaxe), errors.As(err, &tipo):
		return RejectJSON
	}
	return ""
}

// ResponseRejectedObserver recebe a causa de cada resposta recusada.
type ResponseRejectedObserver func(causa string)

// WithResponseRejectedObserver liga ao adaptador do runtime um observador das respostas
// recusadas (AOS-509). fn nil ⇒ opção inerte.
func WithResponseRejectedObserver(fn ResponseRejectedObserver) RuntimeAdapterOption {
	return func(a *ModelClientAdapter) {
		if fn != nil {
			a.rejeicaoObs = fn
		}
	}
}

// observarRejeicao conta o erro se ele for o de uma resposta recusada.
func (a *ModelClientAdapter) observarRejeicao(err error) {
	if a.rejeicaoObs == nil {
		return
	}
	if causa := ResponseRejectionCause(err); causa != "" {
		a.rejeicaoObs(causa)
	}
}
