package modelgateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/wirefake"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-508 — OS PROVIDERS FALSOS DE WIRE, E O QUE O GATEWAY FAZ A CADA CASO.

// wirefakeComportamento corre um caso pelo gateway de produção e pelo adaptador do runtime, com
// a medição da forma desligada, e devolve o que o runtime recebe.
func wirefakeComportamento(t *testing.T, caso string) wirefake.Comportamento {
	t.Helper()
	out, err := wirefakeCompor(t, caso, "").turno()
	if err != nil {
		return wirefake.Comportamento{Desfecho: wirefake.DesfechoRecusada, Erro: err.Error()}
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
	return c
}

// O QUE O GATEWAY FAZ HOJE A CADA CASO fica num ficheiro versionado
// (`internal/wirefake/comportamento.json`) e este teste prende-o: um caso novo, ou uma mudança
// no que o gateway faz a um caso, obriga a mudar o ficheiro no mesmo commit.
//
//	AOS_WIREFAKE_UPDATE=1 go test -run TestAOS508_OQueOGatewayFazACadaCaso .   # regenera-o
func TestAOS508_OQueOGatewayFazACadaCaso(t *testing.T) {
	hoje := map[string]wirefake.Comportamento{}
	for _, caso := range wirefake.Nomes() {
		hoje[caso] = wirefakeComportamento(t, caso)
	}
	if modo := os.Getenv("AOS_WIREFAKE_UPDATE"); modo != "" {
		cru, err := json.MarshalIndent(hoje, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		cru = append(cru, '\n')
		ficheiros := []string{"comportamento.json"}
		if modo == "linha-de-base" {
			ficheiros = append(ficheiros, "linha_de_base_aos508.json")
		}
		for _, f := range ficheiros {
			if err := os.WriteFile(filepath.Join("internal", "wirefake", f), cru, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("regenerado: %v (%d casos)", ficheiros, len(hoje))
		return
	}
	registado := wirefake.ComportamentoDeHoje()
	if len(registado) != len(hoje) {
		t.Errorf("o registo tem %d casos e o wirefake tem %d", len(registado), len(hoje))
	}
	for caso, got := range hoje {
		quer, ha := registado[caso]
		if !ha {
			t.Errorf("%s: caso sem registo do que o gateway lhe faz", caso)
			continue
		}
		if !reflect.DeepEqual(got, quer) {
			t.Errorf("%s: o gateway ja nao faz o que esta registado:\n faz:       %+v\n registado: %+v", caso, got, quer)
		}
	}
	base := wirefake.LinhaDeBase()
	for caso := range hoje {
		if _, ha := base[caso]; !ha {
			t.Errorf("%s: caso sem linha de base (os casos novos entram com o que o gateway fazia antes do AOS-509)", caso)
		}
	}
}

// AS VARIAÇÕES QUE O TICKET NOMEIA têm cada uma o seu caso.
func TestAOS508_VariacoesCobertas(t *testing.T) {
	tem := map[string]bool{}
	for _, n := range wirefake.Nomes() {
		tem[n] = true
		if !json.Valid(wirefake.Corpo(n)) {
			t.Errorf("%s: o corpo nao e JSON valido", n)
		}
	}
	for _, quer := range []string{
		"h1_raciocinio_em_reasoning_content", "h2_raciocinio_noutro_campo", "h3_raciocinio_escondido", "h4_so_brancos",
		"h5_recusa", "h6_nada", "h7_function_call_antigo_stop", "h8_resposta_na_segunda_choice",
		"content_nulo", "content_nulo_com_tool", "content_ausente", "content_ausente_com_tool", "content_vazio", "content_vazio_com_tool",
		"content_texto", "content_texto_com_tool", "content_partes_texto", "content_partes_texto_com_tool", "content_partes_com_imagem",
		"args_objecto", "args_string_nao_json", "args_vazio",
		"rac_reasoning_content_string", "rac_reasoning_objecto", "rac_reasoning_details_lista", "rac_thinking_blocks_lista", "rac_thinking_string",
		"rac_blocos_assinados", "rac_assinatura_na_tool_call", "rac_varios_nomes",
		"fc_antigo_finish_function_call",
		"id_functions_ponto", "id_numerico", "id_uuid", "id_vazio", "id_ausente", "id_repetido", "id_comprimento_fixo",
		"finish_end_turn", "finish_tool_use", "finish_stop_maiusculas", "finish_ausente", "finish_nulo",
		"usage_ausente", "usage_vazio", "usage_so_total", "usage_com_reasoning_tokens",
		"tools_paralelas",
	} {
		if !tem[quer] {
			t.Errorf("falta o caso %s", quer)
		}
	}
}

// OS FALSOS NÃO SÃO ALCANÇÁVEIS POR CÓDIGO DE PRODUÇÃO: nenhum ficheiro que não seja de teste, no
// módulo do gateway, importa o pacote. Fora do módulo não é importável (`internal/`).
func TestAOS508_NenhumCodigoDeProducaoImportaOsFalsos(t *testing.T) {
	vistos := 0
	err := filepath.WalkDir(".", func(caminho string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(caminho, ".go") || strings.HasSuffix(caminho, "_test.go") {
			return nil
		}
		if strings.HasPrefix(filepath.ToSlash(caminho), "internal/wirefake/") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), caminho, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		vistos++
		for _, imp := range f.Imports {
			if strings.Contains(imp.Path.Value, "internal/wirefake") {
				t.Errorf("%s importa os providers falsos — so os testes o podem fazer", caminho)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if vistos < 20 {
		t.Fatalf("so foram lidos %d ficheiros: o teste nao esta a varrer o modulo", vistos)
	}
}

// wirefakeSegundoTurno faz os dois turnos de um run com uma tool call contra um falso que valida
// o segundo pedido, e devolve o erro do segundo.
func wirefakeSegundoTurno(t *testing.T, v *wirefake.Validador) error {
	t.Helper()
	srv := httptest.NewServer(v)
	t.Cleanup(srv.Close)
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), []modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu"}})
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	pedido := aos505Pedido("gpt-4o")
	primeiro, err := gw.Chat(context.Background(), pedido)
	if err != nil {
		t.Fatalf("primeiro turno: %v", err)
	}
	msg := primeiro.Choices[0].Message
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != wirefake.IDEmitido || msg.ReasoningContent != wirefake.RaciocinioEmitido {
		t.Fatalf("o falso nao emitiu a tool call, o id e o raciocinio: %+v", msg)
	}
	// O segundo pedido como o gateway o faz hoje: o id da tool call é o do RUNTIME
	// (`<passo>-tool-<n>`), e a mensagem `assistant` vai tal como veio — o raciocínio é retirado
	// à saída por [port.ChatRequest.MarshalWire], e a assinatura nunca foi descodificada.
	const idDoRuntime = "step-000001-tool-1"
	msg.ToolCalls[0].ID = idDoRuntime
	pedido.Messages = append(pedido.Messages, msg, port.Message{Role: port.RoleTool, ToolCallID: idDoRuntime, Content: "conteudo"})
	_, err = gw.Chat(context.Background(), pedido)
	return err
}

// O SEGUNDO TURNO CONTRA UM PROVIDER QUE EXIGE O SEU ESTADO DE VOLTA — NÃO SUPORTADO HOJE. O
// gateway devolve o id do runtime e não devolve raciocínio nem assinatura: os três falsos
// respondem 400. É o comportamento esperado de hoje, registado; passa a verde só quando o
// trabalho do estado opaco existir, e nesse dia este teste tem de ser invertido.
//
// CONTROLO NEGATIVO REAL: o mesmo segundo pedido, com o estado que o falso emitiu, é ACEITE — o
// 400 vem de o estado faltar, e não de o falso recusar tudo.
func TestAOS508_SegundoTurno_EstadoOpacoNaoSuportadoHoje(t *testing.T) {
	for nome, exige := range map[string]wirefake.Exigencia{
		"id que o provider emitiu": wirefake.ExigeIDEmitido, "raciocinio de volta": wirefake.ExigeRaciocinio, "assinatura de volta": wirefake.ExigeAssinatura,
	} {
		t.Run(nome, func(t *testing.T) {
			v := &wirefake.Validador{Exige: exige}
			err := wirefakeSegundoTurno(t, v)
			if err == nil || !strings.Contains(err.Error(), "status 400") {
				t.Fatalf("hoje o segundo turno e recusado com 400 por este falso; veio %v — se o estado opaco ja volta, inverte este teste e a matriz de suporte", err)
			}
			// O controlo: o segundo pedido que o gateway fez, com o estado reposto, passa.
			pedidos := v.Pedidos()
			segundo := pedidos[len(pedidos)-1].Corpo
			reposto := bytes.ReplaceAll(segundo, []byte("step-000001-tool-1"), []byte(wirefake.IDEmitido))
			reposto = bytes.Replace(reposto, []byte(`"tool_calls":[{"id":"`+wirefake.IDEmitido+`"`),
				[]byte(`"reasoning_content":"`+wirefake.RaciocinioEmitido+`","tool_calls":[{"thought_signature":"`+wirefake.AssinaturaEmitida+`","id":"`+wirefake.IDEmitido+`"`), 1)
			srv := httptest.NewServer(v)
			defer srv.Close()
			resp, perr := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(reposto))
			if perr != nil {
				t.Fatal(perr)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("controlo: com o estado reposto o falso tinha de aceitar; deu %d", resp.StatusCode)
			}
			sem, perr := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(segundo))
			if perr != nil {
				t.Fatal(perr)
			}
			defer func() { _ = sem.Body.Close() }()
			if sem.StatusCode != http.StatusBadRequest {
				t.Fatalf("controlo: sem o estado o falso tinha de recusar; deu %d", sem.StatusCode)
			}
		})
	}
	// O comprimento do id.
	v := &wirefake.Validador{Exige: wirefake.ExigeIDEmitido, MaxID: 9}
	if err := wirefakeSegundoTurno(t, v); err == nil || !strings.Contains(err.Error(), "demasiado longo") {
		t.Fatalf("um id do runtime com mais de 9 bytes e recusado por um provider de ids de comprimento fixo; veio %v", err)
	}
}
