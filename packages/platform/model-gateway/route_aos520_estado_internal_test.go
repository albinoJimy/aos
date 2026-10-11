package modelgateway

// AOS-520 (PR 0) — UM PERFIL ASSINADO SUBSTITUI UMA ENTRADA DA TABELA QUE DEVOLVE ESTADO.
//
// Por registo assinado só entram perfis que NÃO devolvem o estado opaco (AOS-519, D8). Se a tabela
// em código tiver, para um alias, uma entrada que o devolve, e um perfil assinado a substituir, um
// run começado antes — sem perfil fixado — passava a seguir por um perfil que não devolve o estado
// que o provider exige, em silêncio. Recusa-se, com causa própria.
//
// Teste INTERNO porque nenhuma entrada da tabela de hoje devolve estado: a tabela é trocada pela
// duração do teste. Não corre em paralelo com nada (não chama t.Parallel).

import (
	"context"
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/port"
)

// aos520GatewayQueConta conta os pedidos que lhe chegam.
type aos520GatewayQueConta struct {
	port.Gateway
	pedidos int
}

func (g *aos520GatewayQueConta) Chat(context.Context, port.ChatRequest) (port.ChatResponse, error) {
	g.pedidos++
	return port.ChatResponse{Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "ok"}, FinishReason: "stop"}}}, nil
}

func TestAOS520_PerfilAssinadoSobreEntradaQueDevolveEstado(t *testing.T) {
	const comEstado, semEstado = "rota-com-estado", "rota-sem-estado"
	entradaComEstado := RouteProfile{Requested: comEstado, ExpectedModel: "openai/com-estado", WireClass: WireOpenAIChat, Capabilities: []string{CapabilityTools},
		ProjectionVersion: NativeProjectionVersion130, StateReturn: StateReturnRequired}
	entradaSemEstado := RouteProfile{Requested: semEstado, ExpectedModel: "openai/sem-estado", WireClass: WireOpenAIChat, Capabilities: []string{CapabilityTools}}
	original := routeProfiles
	routeProfiles = append(append([]RouteProfile(nil), original...), entradaComEstado, entradaSemEstado)
	t.Cleanup(func() { routeProfiles = original })

	// Os «assinados»: um para cada alias, nenhum devolve estado (é o que a via assinada aceita).
	assinadoSobreEstado := RouteProfile{Requested: comEstado, ExpectedModel: "openai/novo", WireClass: WireOpenAIChat, Capabilities: []string{CapabilityTools}}
	assinadoSobreSemEstado := RouteProfile{Requested: semEstado, ExpectedModel: "openai/novo", WireClass: WireOpenAIChat, Capabilities: []string{CapabilityTools}}
	assinadoNovo := RouteProfile{Requested: "alias-novo", ExpectedModel: "openai/novo", WireClass: WireOpenAIChat, Capabilities: []string{CapabilityTools}}
	set, err := NewRouteProfileSet(assinadoSobreEstado, assinadoSobreSemEstado, assinadoNovo)
	if err != nil {
		t.Fatal(err)
	}
	const marca = agentruntime.RouteProfileDigestUnpinned

	// O caso: run sem perfil fixado, numa rota cuja entrada da tabela devolvia estado, agora
	// substituída por um perfil assinado.
	if err := set.CheckPinnedProfile(comEstado, marca); !errors.Is(err, ErrPinnedRouteProfile) || PinnedRouteProfileCauseOf(err) != PinnedCauseSignedOverState {
		t.Fatalf("o run sem fixacao sobre a rota que devolvia estado tinha de ser recusado com %s; veio %v", PinnedCauseSignedOverState, err)
	}
	// Controlos — nenhum destes perde estado, e o run sem fixação continua como hoje:
	for nome, c := range map[string]struct {
		set   *RouteProfileSet
		alias string
	}{
		"o assinado substitui uma entrada que NAO devolvia estado": {set, semEstado},
		"o assinado acrescenta um alias que a tabela nao tinha":    {set, "alias-novo"},
		"sem perfis assinados (a tabela em codigo)":                {nil, comEstado},
	} {
		if err := c.set.CheckPinnedProfile(c.alias, marca); err != nil {
			t.Errorf("%s: recusado sem razao: %v", nome, err)
		}
	}
	// Um candidato IGUAL à entrada que substitui não muda nada: o estado continua a ser devolvido.
	igual, err := NewRouteProfileSet(entradaComEstado)
	if err != nil {
		t.Fatal(err)
	}
	if err := igual.CheckPinnedProfile(comEstado, marca); err != nil {
		t.Fatalf("um perfil igual a entrada que substitui foi recusado: %v", err)
	}
	// Um run NOVO deste nó (sem valor: ainda não foi re-hospedado) não é este caso — quem o
	// hospeda fixa-o no perfil assinado antes do primeiro turno.
	if err := set.CheckPinnedProfile(comEstado, ""); err != nil {
		t.Fatalf("um run sem valor foi recusado: %v", err)
	}
	// E um run FIXADO no perfil que devolvia estado é recusado pela causa de sempre.
	if err := set.CheckPinnedProfile(comEstado, entradaComEstado.Digest()); PinnedRouteProfileCauseOf(err) != PinnedCauseProfileDifferent {
		t.Fatalf("run fixado no perfil antigo: %v", err)
	}

	// NO ADAPTADOR: o turno é recusado ANTES de qualquer pedido ao gateway.
	gw := &aos520GatewayQueConta{}
	cliente := NewModelClient(gw, comEstado, WithRouteProfileSet(set))
	_, err = cliente.Call(context.Background(), agentruntime.PromptView{Materialized: []byte("ola"), RouteProfileDigest: marca})
	if PinnedRouteProfileCauseOf(err) != PinnedCauseSignedOverState || gw.pedidos != 0 {
		t.Fatalf("adaptador: err=%v pedidos=%d; quer a causa %s e nenhum pedido", err, gw.pedidos, PinnedCauseSignedOverState)
	}
	// Controlo do adaptador: o mesmo run na rota sem estado segue, e não declara digest (não está fixado).
	out, err := NewModelClient(gw, semEstado, WithRouteProfileSet(set)).Call(context.Background(), agentruntime.PromptView{Materialized: []byte("ola"), RouteProfileDigest: marca})
	if err != nil || gw.pedidos != 1 || out.RouteProfileDigest != "" {
		t.Fatalf("adaptador, rota sem estado: err=%v pedidos=%d digest=%q", err, gw.pedidos, out.RouteProfileDigest)
	}
}
