package referencemonitor

import (
	"context"

	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// AOS-478 — o PRINCIPAL DA TOOL CALL chega aos factos que ela causa
// ---------------------------------------------------------------------------
//
// `tecnica/13_Modelo_Dados_Eventos.md` §3.1 promete o `producer` (nhi_id + cadeia de
// delegação + scope) POR EVENTO. Os selos de mediação cumpriam-no; os factos que uma tool
// call PERMITIDA causa a jusante — o ciclo de vida da sandbox (`sandbox.*`) e a reclamação
// de uma aprovação (`approval.consumed`) — gravavam o envelope vazio, porque quem os emite
// recebe só o input opaco da tool e não tem por onde saber quem a pediu.
//
// O RM é quem resolve esse principal (o hook de identidade substitui-o a partir do token
// VERIFICADO). Em vez de alargar a assinatura de [ToolFunc] — que todas as tools do
// repositório implementam —, o RM anexa-o ao contexto que entrega à tool (ver
// [Monitor.dispatch]) e ao verificador de aprovações (ver [ApprovalGate.Evaluate]).
//
// É ATRIBUIÇÃO, NUNCA AUTORIZAÇÃO. Nenhum gate lê este valor para decidir: a autorização
// continua a ser a cadeia de hooks sobre o [Call]. Quem o ler só o usa para preencher o
// envelope de um facto que vai gravar.

// mediatedPrincipalKey é a chave (tipo privado, não-colidível) do principal no contexto.
type mediatedPrincipalKey struct{}

// ContextWithMediatedPrincipal anexa ao contexto o principal da tool call em mediação.
// Exportada para os testes dos emissores a jusante e para adaptadores que reproduzam o
// despacho do RM; o RM é o único que a usa no caminho de produção.
func ContextWithMediatedPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, mediatedPrincipalKey{}, p)
}

// MediatedPrincipalFrom devolve o principal anexado por [ContextWithMediatedPrincipal].
// ok=false quando não há (o facto não foi causado por uma tool call mediada).
func MediatedPrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(mediatedPrincipalKey{}).(Principal)
	return p, ok
}

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

// ProducerFromContext devolve o `producer` do principal mediado no contexto, ou o valor
// zero quando não há. Atalho para os emissores a jusante (sandbox, aprovações).
func ProducerFromContext(ctx context.Context) eventstore.Producer {
	p, ok := MediatedPrincipalFrom(ctx)
	if !ok {
		return eventstore.Producer{}
	}
	return p.EventProducer()
}
