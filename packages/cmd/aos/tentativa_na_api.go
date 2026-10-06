package main

// tentativa_na_api.go — O `GET /runs/{id}` DE UMA NOVA TENTATIVA DIZ DE QUE PEDIDO ELA É
// (AOS-502, ADR-039 §2.7; revisão adversarial, I2).
//
// # O DEFEITO
//
// O `aos-orq` que RETOMA um plano lê, do log do plano, que o nó `read_notes` vai na tentativa 2,
// e pergunta ao nó pelo run `<pedido>~read_notes~2`. Se o run existia, seguia-o — sem nada que
// dissesse que tinha sido ESTE pedido a criá-lo. O `POST /runs` não reserva a forma do id: quem
// tem credencial de submissão na mesma região cria um run com esse id por um `POST /runs` directo.
// Bastava um aborto entre o facto e o pedido (um crash, um 5xx) para a geração seguinte adoptar o
// desfecho — e a saída — de um run alheio, e entregá-la ao nó consumidor do plano.
//
// # O QUE ESTE FICHEIRO DÁ
//
// O nó já tem o facto: o `run.plan_origin`, que só ELE escreve, só depois de verificar o vínculo
// ao pedido e, numa tentativa, só depois da prova. A resposta do `GET /runs/{id}` de um run que é
// uma nova tentativa passa a levá-lo, em `plan_attempt`. Quem retoma confere-o contra o seu
// pedido, o seu nó e a tentativa que o log regista, e não segue um run que não o traga.
//
// # PORQUE É QUE O NÓ COM O TECTO A ZERO CONTINUA A SER O DE ANTES
//
// O campo só existe num run cuja origem, escrita pelo nó, tem `attempt >= 2` — e uma origem
// dessas só se escreve depois de uma tentativa admitida, isto é, com o tecto acima de zero. Um
// run sem ela responde os bytes de sempre. A leitura do stream só se faz para um id com dois
// separadores, que é a única forma que uma tentativa tem.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/aos-ref/substrate/eventstore"
)

// tentativaNaAPI é o `plan_attempt` do `GET /runs/{id}`: de que pedido, de que plano e de que nó
// o run é a tentativa `attempt`. Só ids e inteiros — o que o `run.plan_origin` do próprio run já
// diz a quem o lê com a mesma autorização.
type tentativaNaAPI struct {
	// PlanRequest é o `run_id` do pedido de plano, VERIFICADO pelo nó (o vínculo do AOS-439).
	PlanRequest string `json:"plan_request"`
	// Generation é a geração da reclamação com que a tentativa foi pedida.
	Generation int `json:"generation"`
	// PlanID é DECLARADO pelo drenador; NodeID é conferido pelo nó contra o id do run.
	PlanID string `json:"plan_id"`
	NodeID string `json:"node_id"`
	// Attempt é a tentativa (>= 2) que o nó ADMITIU depois da prova.
	Attempt int `json:"attempt"`
}

// podeSerTentativa diz se o id tem a forma que só uma tentativa tem: pelo menos dois separadores
// (`<pedido>~<nó>~<n>`). É um filtro de custo, e não uma prova: quem decide é a origem gravada.
func podeSerTentativa(runID string) bool {
	return strings.Count(runID, separadorDoRunFilho) >= 2
}

// tentativaNaResposta preenche `plan_attempt` quando o run é uma nova tentativa hospedada por
// este nó. Devolve false quando o stream do run não se leu AGORA: quem chama responde 503 — uma
// resposta sem o campo seria lida, por quem retoma, como «este run não é uma tentativa», e o nó
// do plano fechava `failed` por uma avaria de leitura.
//
// SÓ A ORIGEM QUE O NÓ ESCREVEU CONTA, e só a primeira: a mesma regra da prova
// ([julgarTentativaAnterior]). Uma origem que não se descodifica não dá campo nenhum — a direcção
// segura, porque quem retoma não segue um run sem ele.
func (h *apiHandler) tentativaNaResposta(ctx context.Context, runID string, resp *runStateResponse) bool {
	if !podeSerTentativa(runID) || h.node == nil || h.node.EventStore == nil {
		return true
	}
	eventos, err := h.node.EventStore.Read(ctx, runID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return true
		}
		h.logf("GET /runs/%q (AOS-502): o stream do run nao se leu para declarar a tentativa — responde 503: %v", runID, err)
		return false
	}
	for _, ev := range eventos {
		if ev.Type != EventTypeRunPlanOrigin || ev.Producer.NHIID != origemNHI {
			continue
		}
		var o origemDoRunFilho
		if json.Unmarshal(ev.Payload, &o) != nil || o.Attempt < 2 || o.Pedido.Stream != planRequestStream {
			return true
		}
		resp.PlanAttempt = &tentativaNaAPI{
			PlanRequest: o.Pedido.RunID,
			Generation:  o.Pedido.Geracao,
			PlanID:      o.PlanID,
			NodeID:      o.NodeID,
			Attempt:     o.Attempt,
		}
		return true
	}
	return true
}
