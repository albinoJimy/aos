package modelgateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/wirefake"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-507 — A FORMA DA RESPOSTA, PELO GATEWAY DE PRODUÇÃO. O provider é o falso de wire
// (AOS-508): cada caso é um corpo de resposta em ficheiro.

// wirefakeMontagem é o gateway de produção à frente de um provider falso de wire.
type wirefakeMontagem struct {
	gw      *modelgateway.Gateway
	falso   *wirefake.Servidor
	rotulos [][3]string
}

func (m *wirefakeMontagem) observar(content, reasoning string, stop agentruntime.StopReason) {
	m.rotulos = append(m.rotulos, [3]string{content, reasoning, string(stop)})
}

// wirefakeCompor monta o gateway de produção com a medição da forma no modo dado.
func wirefakeCompor(t *testing.T, caso, modoDaForma string) *wirefakeMontagem {
	t.Helper()
	falso := wirefake.NovoServidor(caso)
	srv := httptest.NewServer(falso)
	t.Cleanup(srv.Close)
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(),
		[]modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu"}})
	cfg.ResponseShape = modoDaForma
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewProduction(forma=%q): %v", modoDaForma, err)
	}
	return &wirefakeMontagem{gw: gw, falso: falso}
}

// turno faz um turno pelo adaptador do runtime, como o nó o faz.
func (m *wirefakeMontagem) turno() (agentruntime.ModelResponse, error) {
	return modelgateway.NewModelClient(m.gw, "gpt-4o", modelgateway.WithPrincipal("tok"), modelgateway.WithRegionBoard("eu", "board-eu"),
		modelgateway.WithResponseShapeObserver(m.observar)).
		Call(context.Background(), agentruntime.PromptView{Materialized: []byte("olá")})
}

// COM A MEDIÇÃO DESLIGADA A SONDA NÃO CORRE: a resposta da porta e o turno do runtime não levam
// ficha, o observador nunca é chamado, e — para TODOS os casos do wirefake — o turno (ou o erro)
// é o mesmo que com a medição ligada, tirando a ficha. O pedido ao provider é o mesmo nos dois.
func TestAOS507_Off_ASondaNaoCorre(t *testing.T) {
	for _, caso := range wirefake.Nomes() {
		ligada := wirefakeCompor(t, caso, modelgateway.ResponseShapeObserve)
		comFicha, errCom := ligada.turno()
		for _, modo := range []string{"", modelgateway.ResponseShapeOff} {
			m := wirefakeCompor(t, caso, modo)
			resp, errPorta := m.gw.Chat(context.Background(), aos505Pedido("gpt-4o"))
			if errPorta == nil && resp.Shape != nil {
				t.Fatalf("%s, modo %q: a porta devolveu uma ficha com a medicao desligada", caso, modo)
			}
			out, err := m.turno()
			if (err == nil) != (errCom == nil) || (err != nil && err.Error() != errCom.Error()) {
				t.Fatalf("%s, modo %q: o erro mudou com a medicao: %v contra %v", caso, modo, err, errCom)
			}
			if out.Shape != nil || len(m.rotulos) != 0 {
				t.Fatalf("%s, modo %q: o turno leva ficha (%+v) ou o observador foi chamado (%d)", caso, modo, out.Shape, len(m.rotulos))
			}
			semFicha := comFicha
			semFicha.Shape = nil
			if !reflect.DeepEqual(out, semFicha) {
				t.Fatalf("%s, modo %q: fora da ficha o turno mudou com a medicao:\n off:     %+v\n observe: %+v", caso, modo, out, semFicha)
			}
			pOff, pOn := m.falso.Pedidos(), ligada.falso.Pedidos()
			if string(pOff[len(pOff)-1].Corpo) != string(pOn[len(pOn)-1].Corpo) {
				t.Fatalf("%s, modo %q: a medicao mudou o pedido ao provider", caso, modo)
			}
		}
	}
}

// EM OBSERVAÇÃO: cada turno aceite leva a ficha, dentro do vocabulário do runtime; as oito formas
// do `empty_output` dão oito fichas distintas no que o `turn.recorded` grava; e os rótulos que o
// observador recebe são do vocabulário fechado.
func TestAOS507_Observe_FichaEmCadaTurno(t *testing.T) {
	conteudos := map[string]bool{agentruntime.ShapeUnreadable: true}
	for _, c := range agentruntime.ShapeContents() {
		conteudos[c] = true
	}
	raciocinios := map[string]bool{}
	for _, r := range agentruntime.ShapeReasonings() {
		raciocinios[r] = true
	}
	gravadas := map[string]string{}
	for _, caso := range wirefake.Nomes() {
		m := wirefakeCompor(t, caso, modelgateway.ResponseShapeObserve)
		out, err := m.turno()
		if err != nil {
			if len(m.rotulos) != 0 {
				t.Errorf("%s: um turno que falhou nao conta", caso)
			}
			continue
		}
		if out.Shape == nil {
			t.Fatalf("%s: o turno nao leva ficha", caso)
		}
		if n := out.Shape.Normalizado(); n.Unreadable || !reflect.DeepEqual(n, out.Shape) {
			t.Errorf("%s: a ficha da porta nao cabe no vocabulario do runtime: %+v", caso, out.Shape)
		}
		if len(m.rotulos) != 1 || !conteudos[m.rotulos[0][0]] || !raciocinios[m.rotulos[0][1]] || m.rotulos[0][2] != string(out.StopReason) {
			t.Errorf("%s: rotulos do observador fora do vocabulario: %v", caso, m.rotulos)
		}
		if strings.HasPrefix(caso, "h") && caso[1] >= '1' && caso[1] <= '8' && caso[2] == '_' {
			if out.Text != "" && strings.TrimSpace(out.Text) != "" || len(out.ToolCalls) != 0 || out.StopReason != agentruntime.StopStop {
				t.Errorf("%s: as oito formas dao hoje texto vazio, zero tool calls e `stop`: %+v", caso, out)
			}
			semDigest := *out.Shape
			semDigest.ShapeDigest = ""
			cru, _ := json.Marshal(semDigest)
			if outro, ha := gravadas[string(cru)]; ha {
				t.Errorf("%s e %s gravam a MESMA ficha: %s", caso, outro, cru)
			}
			gravadas[string(cru)] = caso
		}
	}
	if len(gravadas) != 8 {
		t.Fatalf("queria as oito formas H1 a H8, vieram %d", len(gravadas))
	}
}

// SEM CONTEÚDO, pelo caminho inteiro: nenhum valor do corpo e nenhum nome de chave desconhecido
// aparece na ficha gravada.
func TestAOS507_Observe_SemConteudoNaFicha(t *testing.T) {
	for _, caso := range []string{"rac_varios_nomes", "extra_system_fingerprint_e_chaves", "h5_recusa", "tools_paralelas", "rac_blocos_assinados"} {
		m := wirefakeCompor(t, caso, modelgateway.ResponseShapeObserve)
		out, err := m.turno()
		if err != nil {
			t.Fatalf("%s: %v", caso, err)
		}
		cru, _ := json.Marshal(out.Shape)
		for _, proibido := range []string{"resposta final", "penso", "campo", "bloco", "fp_falso", "provider_specific", "annotations", "arquivo", "counter", "call_um", "c2ln", "nao posso", "notas"} {
			if strings.Contains(string(cru), proibido) {
				t.Errorf("%s: a ficha leva %q: %s", caso, proibido, cru)
			}
		}
	}
}

func TestAOS507_ModoInvalidoRecusaAComposicao(t *testing.T) {
	for _, modo := range []string{"enforce", "on", "Observe", "1"} {
		cfg := prodConfig(audit.NewMemStore(), "http://127.0.0.1:1", nil, []modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu"}})
		cfg.ResponseShape = modo
		if gw, err := modelgateway.NewProduction(context.Background(), cfg); !errors.Is(err, modelgateway.ErrBadResponseShape) || gw != nil {
			t.Errorf("modo %q: queria ErrBadResponseShape e nenhum gateway; veio %v", modo, err)
		}
	}
	if port.Version != "1.5.0" && port.Version != "1.6.0" && port.Version != "1.7.0" {
		t.Errorf("a ficha entrou na porta na 1.5.0; a versao e %s", port.Version)
	}
}
