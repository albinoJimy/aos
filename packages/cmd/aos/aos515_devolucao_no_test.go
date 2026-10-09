package main

// AOS-515 — A DEVOLUÇÃO DO ESTADO OPACO, NO NÓ. O nó trabalha só com a tabela de perfis em
// código, e nenhum perfil de hoje devolve estado: a versão 1.3.0 é aceite, e com ela os pedidos
// são os da 1.2.0 (provado no gateway, TestAOS515_Nunca_A130EA120ByteAByte); a família da
// métrica não existe; e o arranque diz o que a versão exige.

import (
	"strings"
	"testing"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

func TestAOS515_No_A130EAceiteEAMetricaNaoExisteComOsPerfisDeHoje(t *testing.T) {
	t.Setenv("AOS_MODEL_PROJECTION_VERSION", "1.3.0")
	v, err := parseModelProjectionVersionFromEnv()
	if err != nil || v != modelgateway.NativeProjectionVersion130 {
		t.Fatalf("AOS_MODEL_PROJECTION_VERSION=1.3.0: %q %v", v, err)
	}
	linhas := strings.Join(modelProjectionBannerFor(true, modelgateway.ProjectionNative, v), "\n")
	for _, quero := range []string{"AOS-515", "AOS_MODEL_PROVIDER_STATE=capture", "AOS_MODEL_ROUTE_GOVERNANCE", "devolver=nunca"} {
		if !strings.Contains(linhas, quero) {
			t.Fatalf("o arranque nao diz %q:\n%s", quero, linhas)
		}
	}
	if algumPerfilDevolveEstado() || estadoDevolvido.activo {
		t.Fatalf("um perfil da tabela devolve estado: a rota de producao so muda quando o dono assinar outro perfil")
	}
	c := novosContadoresDaDevolucao(true)
	c.observar(modelgateway.StateReturnObservation{Result: modelgateway.StateReturnAll})
	c.observar(modelgateway.StateReturnObservation{Result: modelgateway.StateReturnRefused, Cause: modelgateway.StateCauseOtherRoute})
	c.observar(modelgateway.StateReturnObservation{Result: "texto livre"})
	if c.lido(modelgateway.StateReturnAll) != 1 || c.lido(modelgateway.StateReturnRefused) != 1 || c.lido("texto livre") != 0 {
		t.Fatalf("contagem errada")
	}
}
