package modelgateway_test

// aos425_escada_nomes_de_stream_test.go — O NOME DO MODELO ENTRA NUM `stream_id`, E O SÍTIO
// ONDE ELE ENTRA É A ESCADA DE TIERS.
//
// Com o refino armado, o `Reserve` da admissão compõe
// `admission/bucket/<provider>:<model>:<region>` a partir de `tier.Model` — e esse nome é um
// `stream_id`, que não pode conter `.`. `gpt-4.1` e `claude-3.5-sonnet` são nomes de modelo
// reais.
//
// # PORQUE É QUE ESTE TESTE ESTÁ AQUI E NÃO NA ALLOWLIST
//
// Porque foi aqui que o AOS-425 acabou, depois de uma revisão adversarial mostrar que a primeira
// tentativa estava no sítio errado: a allowlist AUTORIZA um modelo, a escada FORNECE-O. Validar
// na allowlist não tocava no valor que compõe a chave, e partia a razão de ser do bundle externo
// («pedir nomes de modelo reais»).
//
// A recusa é de ARRANQUE, pela mesma razão que a cobertura de preço: com o refino armado, a
// lacuna só apareceria quando aquele tier GANHASSE uma decisão de roteamento, e o sintoma seria
// um run não admitido — um modo de falha intermitente que nenhum smoke apanha.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// UM MODELO DA ESCADA COM PONTO RECUSA O ARRANQUE, E O ERRO NOMEIA O MODELO.
func TestAOS425EscadaComModeloNaoRepresentavelRecusaOArranque(t *testing.T) {
	for _, modelo := range []string{"gpt-4.1", "claude-3.5-sonnet", "modelo com espaco"} {
		srv := okOpenAIServer(t, new(int))
		cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), twoRegionAccounts())
		cfg.Routing = modelgateway.RoutingConfig{
			Tiers: []tiering.Tier{{Name: "t1", Model: modelo}},
		}
		_, err := modelgateway.NewProduction(context.Background(), cfg)
		if !errors.Is(err, modelgateway.ErrRoutingModelNaoRepresentavel) {
			t.Errorf("uma escada com o modelo %q devia recusar o arranque com "+
				"ErrRoutingModelNaoRepresentavel, veio: %v\n"+
				"esse nome entra em `admission/bucket/<provider>:<model>:<region>`, que e um stream_id",
				modelo, err)
			continue
		}
		if !strings.Contains(err.Error(), modelo) {
			t.Errorf("o erro nao nomeia o modelo %q: %v", modelo, err)
		}
	}
}

// E UM MODELO NORMAL NÃO É AFECTADO.
//
// A metade que impede a guarda de recusar tudo. O erro que sobra (se houver) não pode ser o
// desta guarda — é o que prova que os modelos correntes continuam a compor.
func TestAOS425EscadaComModeloRepresentavelNaoEeAfectada(t *testing.T) {
	srv := okOpenAIServer(t, new(int))
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), twoRegionAccounts())
	cfg.Routing = modelgateway.RoutingConfig{Tiers: chainTiers()}
	if _, err := modelgateway.NewProduction(context.Background(), cfg); errors.Is(err, modelgateway.ErrRoutingModelNaoRepresentavel) {
		t.Fatalf("a escada corrente foi recusada pela guarda de nomes: %v", err)
	}
}

// A REGIÃO DO GATEWAY TAMBÉM ENTRA NA CHAVE.
//
// `DefaultRegion` vem de `AOS_MODEL_REGION`, uma env var do operador — e é o terceiro campo de
// `<provider>:<model>:<region>`. O `provider` NÃO é alcançável aqui: vem do pedido, e fica
// declarado como resíduo no AOS-425.
func TestAOS425RegiaoDoGatewayNaoRepresentavelRecusaOArranque(t *testing.T) {
	srv := okOpenAIServer(t, new(int))
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), twoRegionAccounts())
	cfg.DefaultRegion = "eu.west"
	cfg.Routing = modelgateway.RoutingConfig{Tiers: chainTiers()}
	if _, err := modelgateway.NewProduction(context.Background(), cfg); !errors.Is(err, modelgateway.ErrRoutingModelNaoRepresentavel) {
		t.Fatalf("uma DefaultRegion com ponto devia recusar o arranque, veio: %v", err)
	}
}
