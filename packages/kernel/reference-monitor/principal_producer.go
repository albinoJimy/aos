package referencemonitor

import (
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// AOS-478 — o PRINCIPAL DA TOOL CALL chega aos factos que ela causa
// ---------------------------------------------------------------------------
//
// `tecnica/13_Modelo_Dados_Eventos.md` §3.1 promete o `producer` (nhi_id + cadeia de
// delegação + scope) POR EVENTO. Os selos de mediação cumpriam-no; os factos que uma tool
// call causa a jusante — o ciclo de vida da sandbox (`sandbox.*`) e a reclamação de uma
// aprovação (`approval.consumed`) — gravavam o envelope vazio, porque quem os emite recebe
// só o input opaco da tool e não tem por onde saber quem a pediu.
//
// O RM é quem resolve esse principal (o hook de identidade substitui-o a partir do token
// VERIFICADO). Em vez de alargar a assinatura de [ToolFunc], o RM anexa o `producer` ao
// contexto que entrega à tool ([Monitor.dispatch]) e ao verificador de aprovações
// ([ApprovalGate.Evaluate]), com [eventstore.ContextWithProducer]. O emissor a jusante lê-o
// com [eventstore.ProducerFromContext], sem importar o RM.
//
// É ATRIBUIÇÃO, NUNCA AUTORIZAÇÃO: nenhum gate lê esse valor para decidir.

// EventProducer projecta o principal no `producer` do envelope do Event Store: o NHIID, a
// cadeia de delegação (raiz humana → agente) e a autoridade em vigor como scope. É a MESMA
// projecção que os selos de mediação usam ([NewEventStoreSink]), para que um facto causado
// por uma tool call identifique exactamente o mesmo principal que o `tool.call.*` do passo.
func (p Principal) EventProducer() eventstore.Producer {
	return eventstore.Producer{
		NHIID:           p.NHIID,
		DelegationChain: toStoreChain(p.DelegationChain),
		Scope:           p.Authority,
	}
}
