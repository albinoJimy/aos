package modelgateway

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/port"
)

// A DEVOLUÇÃO DO ESTADO OPACO AO PROVIDER (AOS-515, ADR-040 §2.9 e §2.11; ADR-036 §2.4).
//
// O AOS-514 guarda, selado na captura de cada turno, o que o provider mandou e pode exigir de
// volta. Aqui decide-se quando VOLTA, e a quem. São precisas as três coisas, todas:
//
//  1. a projecção nativa 1.3.0, que agarra o estado de cada turno à mensagem `assistant` desse
//     turno ([estadoDoTurno]) — juntando-o pelo rótulo `state_digest` do tail, e só depois de
//     conferir que o `sha256` dos bytes é o rótulo;
//  2. um perfil de rota cuja classe de estado não seja `nunca` ([RouteProfile.StateReturn]);
//  3. o estado ter sido produzido por ESSA rota: o digest do perfil e o modelo servido que o
//     envelope gravou são os da rota a que o pedido vai sair.
//
// A decisão (2) e (3) toma-se no gateway, DEPOIS do roteamento ([Gateway.armarDevolucao]): quem
// projecta não sabe a que rota o pedido acaba por ir, e num failover a segunda rota não recebe um
// byte do que a primeira produziu.
//
// FALHA FECHADO. Numa rota `obrigatorio`, um turno com tool calls sem estado devolvível — ausente,
// só referência, com o digest em desacordo, de outra rota — faz o pedido NÃO SAIR: o run falha
// com uma causa em vocabulário fechado ([StateReturnError]). Não se envia sem o estado à espera
// de que o provider aceite, nem de que ele desligue o raciocínio em silêncio. Numa rota
// `opcional`, o pedido segue sem o estado desse turno, e conta-se.
//
// O QUE NUNCA ACONTECE AQUI. O gateway não lê os valores do estado, não os altera e não os põe
// em texto: copia os bytes para o sítio de onde vieram ([port.MessageState]). O raciocínio não é
// resposta e não dá autoridade (decisão D3).

// Como os ids das tool calls vão no wire de uma rota ([RouteProfile.ToolCallID]). Vocabulário
// FECHADO.
const (
	// ToolCallIDRuntime — o id do runtime (`<passo>-tool-<n>`), como sempre. É a omissão (o
	// campo vazio).
	ToolCallIDRuntime = ""
	// ToolCallIDRuntimeName é a forma escrita de [ToolCallIDRuntime] num perfil lido de JSON.
	ToolCallIDRuntimeName = "runtime"
	// ToolCallIDProvider — o id que o provider deu a cada tool call volta-lhe no `assistant` e
	// na mensagem `tool`, nos turnos cujo estado é devolvido. Só com uma classe de estado que
	// não seja `nunca`: o id do provider faz parte do estado.
	ToolCallIDProvider = "provider"
)

// Causas de um turno cujo estado não é devolvido, além das de [port.MessageState.Missing].
// Vocabulário FECHADO: é a causa do erro numa rota `obrigatorio` e a do contador.
const (
	// StateCauseNoProjection — a mensagem `assistant` não traz estado nenhum agarrado: o pedido
	// não vem da projecção 1.3.0.
	StateCauseNoProjection = "projeccao_sem_estado"
	// StateCauseNoRoute — o envelope não diz a que rota pertence (o turno correu com a
	// governação da rota desligada): não se prova que é desta.
	StateCauseNoRoute = "estado_sem_rota"
	// StateCauseRouteUnproven — o turno que produziu o estado não teve a rota comparada como
	// `igual` (diferente, ou por reportar): não se prova que o estado é desta rota.
	StateCauseRouteUnproven = "estado_de_rota_nao_provada"
	// StateCauseOtherRoute — o estado foi produzido por outra rota ou servido por outro modelo.
	StateCauseOtherRoute = "estado_de_outra_rota"
	// StateCauseProviderID — a rota pede os ids do provider e os deste turno não servem: em
	// falta, fora do alfabeto aceite, repetidos, ou iguais a um id já usado no pedido.
	StateCauseProviderID = "id_do_provider_inutilizavel"
)

// StateReturnCauses devolve o vocabulário das causas, numa ordem fixa.
func StateReturnCauses() []string {
	return []string{port.StateMissingAbsent, port.StateMissingReference, port.StateMissingDigest, port.StateMissingUnreadable,
		port.StateMissingMisaligned, StateCauseNoProjection, StateCauseNoRoute, StateCauseRouteUnproven, StateCauseOtherRoute, StateCauseProviderID}
}

// Resultados da devolução num pedido ([StateReturnObservation.Result]). Vocabulário FECHADO.
const (
	// StateReturnAll — todos os turnos com tool calls do pedido levaram o seu estado.
	StateReturnAll = "devolvido"
	// StateReturnPartial — rota `opcional`: uns levaram, outros não.
	StateReturnPartial = "parcial"
	// StateReturnNone — rota `opcional`: nenhum levou.
	StateReturnNone = "sem_estado"
	// StateReturnRefused — rota `obrigatorio`: faltava o estado de um turno, e o pedido NÃO foi
	// enviado.
	StateReturnRefused = "recusado"
)

// StateReturnResults devolve o vocabulário dos resultados, numa ordem fixa.
func StateReturnResults() []string {
	return []string{StateReturnAll, StateReturnPartial, StateReturnNone, StateReturnRefused}
}

// StateReturnObservation é o que o gateway reporta por cada pedido a uma rota cuja classe de
// estado não é `nunca` e que leva pelo menos um turno com tool calls. Só vocabulário fechado.
type StateReturnObservation struct {
	// Result é um dos valores de [StateReturnResults].
	Result string
	// Cause é a causa do PRIMEIRO turno sem estado ([StateReturnCauses]); vazia em `devolvido`.
	Cause string
}

// ErrStateReturnRequired — a rota exige o estado opaco de volta e o de um turno não se pode
// devolver. O pedido não foi enviado.
var ErrStateReturnRequired = errors.New("model-gateway: a rota exige o estado opaco do provider de volta e o de um turno nao se pode devolver — pedido nao enviado")

// StateReturnError é o erro de um pedido recusado por faltar o estado numa rota `obrigatorio`.
// Casa com [ErrStateReturnRequired] por [errors.Is]. Só leva vocabulário fechado.
type StateReturnError struct {
	// Cause é a causa, uma das de [StateReturnCauses].
	Cause string
}

// Error implementa error.
func (e *StateReturnError) Error() string {
	return fmt.Sprintf("%v: causa=%s", ErrStateReturnRequired, e.Cause)
}

// Unwrap liga o erro a [ErrStateReturnRequired].
func (e *StateReturnError) Unwrap() error { return ErrStateReturnRequired }

// WithStateReturnObserver liga quem conta as devoluções (AOS-515). nil ⇒ ninguém conta.
func WithStateReturnObserver(obs func(StateReturnObservation)) Option {
	return func(g *Gateway) { g.estadoObs = obs }
}

// estadoDoTurno constrói o [port.MessageState] de um turno do modelo para a projecção 1.3.0.
//
// A JUNÇÃO É PELO RÓTULO DO TAIL, COM VERIFICAÇÃO. `rotulo` é o valor do `state_digest` do
// primeiro segmento do turno (vazio ⇒ o tail não refere estado). Os bytes procuram-se em
// `estados` por essa chave, e só valem se o `sha256` DELES for o rótulo: a chave do mapa não é
// de confiança, o rótulo é — está na linha de delimitação, que só o runtime escreve, e é com ele
// que o `prompt_hash` do turno se comprometeu. Qualquer desacordo dá um estado «em falta», com a
// causa, e nenhum byte: o turno vai sem estado.
//
// `chamadas` é o número de tool calls do turno PROJECTADO. Um envelope com outro número (o turno
// escalou a meio e só parte das chamadas foi despachada) não se devolve: a n-ésima entrada do
// estado tem de ser a n-ésima tool call da mensagem.
func estadoDoTurno(estados map[string][]byte, rotulo string, chamadas int) *port.MessageState {
	if rotulo == "" {
		return &port.MessageState{Missing: port.StateMissingAbsent}
	}
	cru := estados[rotulo]
	if len(cru) == 0 {
		return &port.MessageState{Missing: port.StateMissingReference}
	}
	soma := sha256.Sum256(cru)
	if "sha256:"+hex.EncodeToString(soma[:]) != rotulo {
		return &port.MessageState{Missing: port.StateMissingDigest}
	}
	env, err := port.UnmarshalProviderStateEnvelope(cru)
	if err != nil {
		return &port.MessageState{Missing: port.StateMissingUnreadable}
	}
	if len(env.ToolCalls) != 0 && len(env.ToolCalls) != chamadas {
		return &port.MessageState{Missing: port.StateMissingMisaligned}
	}
	return &port.MessageState{
		RouteProfileDigest: env.RouteProfileDigest,
		ServedModel:        env.ServedModel,
		RouteCheck:         env.RouteCheck,
		Fields:             env.Fields,
		ToolCalls:          env.ToolCalls,
	}
}

// armarDevolucao decide, para o pedido que vai sair para a rota de `perfil`, que turnos levam o
// seu estado opaco, e escreve a decisão em cada mensagem ([port.MessageState.Return]). Trabalha
// sobre uma CÓPIA das mensagens: as do chamador ficam como estavam.
//
// Devolve quantos turnos com tool calls o pedido tem e quantos levam estado, e a causa do
// primeiro que não leva. Numa rota `obrigatorio` devolve [*StateReturnError] ao primeiro turno
// sem estado devolvível — quem chama NÃO envia o pedido.
//
// SEM CLASSE, NADA SAI. Com a classe `nunca` — a omissão, e a de uma rota sem perfil — qualquer
// estado que as mensagens tragam é retirado, e o pedido é o da projecção 1.2.0, byte a byte. O
// que um chamador tenha marcado em [port.MessageState.Return] é sempre sobreposto.
//
// ESTÁVEL POR PREFIXO. A decisão de um turno só depende desse turno e dos ANTERIORES (os ids já
// usados no pedido): acrescentar turnos no fim não muda o que os primeiros levam.
func armarDevolucao(req *port.ChatRequest, perfil RouteProfile, temPerfil bool) (turnos, devolvidos int, causa string, err error) {
	classe := StateReturnNever
	if temPerfil {
		classe = perfil.StateReturn
	}
	if classe == StateReturnNever {
		for i := range req.Messages {
			if req.Messages[i].State != nil {
				msgs := append([]port.Message(nil), req.Messages...)
				for j := range msgs {
					msgs[j].State = nil
				}
				req.Messages = msgs
				break
			}
		}
		return 0, 0, "", nil
	}
	digest := perfil.Digest()
	msgs := append([]port.Message(nil), req.Messages...)
	usados := map[string]bool{}
	for i := range msgs {
		m := &msgs[i]
		original := m.State
		m.State = nil
		if m.Role != port.RoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		turnos++
		motivo := ""
		switch {
		case original == nil:
			motivo = StateCauseNoProjection
		case original.Missing != "":
			motivo = original.Missing
		case original.RouteProfileDigest == "":
			motivo = StateCauseNoRoute
		case original.RouteCheck != port.RouteCheckEqual:
			// O turno que produziu o estado NÃO provou a rota: o endpoint era outro ou não foi
			// reportado, ou o nome do modelo só coincide depois de saneado. O digest do perfil e
			// o nome saneado casavam na mesma — por isso a decisão lê o resultado gravado, e não
			// o recalcula de entradas mais fracas. Vale em `observe` como em `enforce`.
			motivo = StateCauseRouteUnproven
		case original.RouteProfileDigest != digest || original.ServedModel != perfil.ExpectedModel:
			motivo = StateCauseOtherRoute
		case perfil.ToolCallID == ToolCallIDProvider && !idsDoProviderServem(original, len(m.ToolCalls), usados):
			motivo = StateCauseProviderID
		}
		if motivo != "" {
			if classe == StateReturnRequired {
				return turnos, devolvidos, motivo, &StateReturnError{Cause: motivo}
			}
			if causa == "" {
				causa = motivo
			}
			for _, tc := range m.ToolCalls {
				usados[tc.ID] = true
			}
			continue
		}
		st := *original
		st.Return, st.ProviderIDs = true, perfil.ToolCallID == ToolCallIDProvider
		// O sítio é o do perfil da rota, e só o dele: o que o estado trouxesse é sobreposto.
		st.Placement = perfil.StateReturnAt
		m.State = &st
		devolvidos++
		for n, tc := range m.ToolCalls {
			if st.ProviderIDs {
				usados[st.ToolCalls[n].IDValue] = true
			} else {
				usados[tc.ID] = true
			}
		}
	}
	req.Messages = msgs
	return turnos, devolvidos, causa, nil
}

// idsDoProviderServem diz se os ids que o provider deu às tool calls de um turno podem ir no
// wire: um por chamada, todos utilizáveis ([port.ProviderStateToolCall.IDUsable]), diferentes
// entre si e de todos os ids já usados no pedido, e nenhum com a forma de um id do runtime.
func idsDoProviderServem(st *port.MessageState, chamadas int, usados map[string]bool) bool {
	if len(st.ToolCalls) != chamadas {
		return false
	}
	doTurno := map[string]bool{}
	for _, tc := range st.ToolCalls {
		if !tc.IDUsable || tc.IDValue == "" || usados[tc.IDValue] || doTurno[tc.IDValue] {
			return false
		}
		// Um id do provider com a FORMA de um id do runtime (`<passo>-tool-<n>`) não serve: um
		// turno posterior que vá com os ids do runtime podia ter exactamente esse, e o pedido
		// ficava com dois ids iguais. A regra olha só para o próprio id — não para os turnos
		// seguintes —, pelo que a decisão de um turno continua a não depender do que vem depois.
		if _, _, forma := agentruntime.ToolStepParent(tc.IDValue); forma {
			return false
		}
		doTurno[tc.IDValue] = true
	}
	return true
}

// devolverEstado é o passo do gateway: decide a devolução para a rota de req.Model e reporta o
// resultado. Devolve quantos turnos levam estado, e o erro que impede o pedido de sair.
func (g *Gateway) devolverEstado(req *port.ChatRequest) (devolvidos int, err error) {
	perfil, tem := g.perfis.For(req.Model)
	turnos, devolvidos, causa, err := armarDevolucao(req, perfil, tem)
	if turnos == 0 && err == nil {
		return 0, nil
	}
	if g.estadoObs != nil {
		obs := StateReturnObservation{Result: StateReturnAll, Cause: causa}
		switch {
		case err != nil:
			obs.Result = StateReturnRefused
		case devolvidos == 0:
			obs.Result = StateReturnNone
		case devolvidos < turnos:
			obs.Result = StateReturnPartial
		}
		g.estadoObs(obs)
	}
	return devolvidos, err
}

// semCorpoDoErro retira de um erro de status do provider o corpo da resposta. Usa-se quando o
// pedido levou estado opaco: um provider que recuse o pedido pode ecoar no erro o raciocínio que
// recebeu, e esse texto não pode aparecer numa mensagem de erro, num log nem num evento.
func semCorpoDoErro(err error) error {
	var se *adapters.StatusError
	if errors.As(err, &se) {
		return se.SemCorpo()
	}
	return err
}
