package modelgateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/wirefake"
)

// AOS-508 (revisão, M-8) — O QUE O GATEWAY FAZ AO QUE O PROXY DE PRODUÇÃO ENTREGA.
//
// Os falsos de `casos/` são fiéis como PROVIDER; o que o gateway vê em produção é o que o proxy
// faz a esses corpos. `casos_pos_proxy/` são os corpos que a imagem de produção do proxy
// entregou na corrida do `ci-wire-live` (um por caso a que respondeu 200), congelados. Este
// teste põe cada um à frente do gateway de produção, com a medição da forma ligada, e prende o
// resultado em `comportamento_pos_proxy.json`.
//
//	AOS_WIREFAKE_UPDATE=1 go test -run TestAOS508_PosProxy .   # regenera o registo
func TestAOS508_PosProxy_OQueOGatewayFazAoQueOProxyEntrega(t *testing.T) {
	nomes := wirefake.NomesPosProxy()
	if len(nomes) < 60 {
		t.Fatalf("so ha %d casos pos-proxy; a corrida do gate entregou mais de 60", len(nomes))
	}
	hoje := map[string]wirefake.Comportamento{}
	for _, caso := range nomes {
		corpo := wirefake.CorpoPosProxy(caso)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(corpo)
		}))
		cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), []modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu"}})
		cfg.ResponseShape = modelgateway.ResponseShapeObserve
		gw, err := modelgateway.NewProduction(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		out, err := modelgateway.NewModelClient(gw, "gpt-4o", modelgateway.WithPrincipal("tok"), modelgateway.WithRegionBoard("eu", "board-eu")).
			Call(context.Background(), agentruntime.PromptView{Materialized: []byte("olá")})
		srv.Close()
		if err != nil {
			causa := modelgateway.ResponseRejectionCause(err)
			if causa == "" {
				causa = err.Error()
			}
			hoje[caso] = wirefake.Comportamento{Desfecho: wirefake.DesfechoRecusada, Erro: causa}
			continue
		}
		// A ficha de um corpo real do proxy cabe sempre no vocabulário do runtime.
		if out.Shape == nil || out.Shape.Normalizado().Unreadable {
			t.Errorf("%s: a ficha do corpo entregue pelo proxy e ilegivel ou falta: %+v", caso, out.Shape)
		}
		c := wirefake.Comportamento{
			Desfecho: wirefake.DesfechoTurno, Text: out.Text, StopReason: string(out.StopReason), Final: out.Final,
			Reasoning: out.Reasoning, Model: out.Model,
			InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens,
			CacheReadTokens: out.Usage.CacheReadTokens, UsageAusente: out.Usage.Ausente,
		}
		for _, tc := range out.ToolCalls {
			c.ToolCalls = append(c.ToolCalls, wirefake.ChamadaDeTool{Tool: tc.ToolID, Input: string(tc.Input)})
		}
		hoje[caso] = c
	}
	if os.Getenv("AOS_WIREFAKE_UPDATE") != "" {
		cru, err := json.MarshalIndent(hoje, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join("internal", "wirefake", "comportamento_pos_proxy.json"), append(cru, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("regenerado: comportamento_pos_proxy.json (%d casos)", len(hoje))
		return
	}
	registado := wirefake.ComportamentoPosProxy()
	if len(registado) != len(hoje) {
		t.Errorf("o registo tem %d casos e ha %d corpos pos-proxy", len(registado), len(hoje))
	}
	for caso, got := range hoje {
		if quer, ha := registado[caso]; !ha || !reflect.DeepEqual(got, quer) {
			t.Errorf("%s: o gateway ja nao faz ao corpo do proxy o que esta registado:\n faz:       %+v\n registado: %+v", caso, got, quer)
		}
	}
}
