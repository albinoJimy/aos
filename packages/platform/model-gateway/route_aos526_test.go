package modelgateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-526 — A REGRA `params.tool_choice` DO PERFIL DA ROTA. Com ela, o pedido leva `tool_choice`
// enquanto o run não pediu tool nenhuma e o contrato tem tools por cumprir; sem ela, o pedido é o de antes.

const aos526Regra = `"params":{"tool_choice":"enquanto_o_contrato_nao_estiver_satisfeito"}`

// aos526Tools são as tools que o adaptador oferece nos testes.
func aos526Tools() []port.Tool {
	return []port.Tool{
		{Type: "function", Function: port.FunctionDef{Name: "doc_read", Parameters: json.RawMessage(`{"type":"object"}`)}},
		{Type: "function", Function: port.FunctionDef{Name: "doc_write", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
}

// aos526Turno faz um turno pelo adaptador do runtime, com as tools por cumprir dadas, e devolve o
// corpo que chegou ao provider e a resposta do turno.
func aos526Turno(t *testing.T, m *aos513Montagem, tools []port.Tool, pendentes []string) (string, agentruntime.ModelResponse, error) {
	t.Helper()
	antes := len(m.provider.pedidos())
	out, err := modelgateway.NewModelClient(m.gw, "gpt-4o", modelgateway.WithPrincipal("tok"), modelgateway.WithRegionBoard("eu", "board-eu"), modelgateway.WithTools(tools)).
		Call(context.Background(), agentruntime.PromptView{Materialized: []byte("olá"), CompletionToForce: pendentes})
	corpos := m.provider.pedidos()
	if len(corpos) != antes+1 {
		t.Fatalf("o turno fez %d pedidos ao provider", len(corpos)-antes)
	}
	return string(corpos[antes]), out, err
}

// A LEITURA DO PARÂMETRO: o valor do vocabulário lê-se; `nunca` e a ausência são a omissão; um
// valor fora do vocabulário — incluindo os do wire — recusa, sem repetir o valor; e a regra exige
// a capacidade `tools`.
func TestAOS526_Perfil_LeituraDoParametro(t *testing.T) {
	ligado := aos513Perfil(t, `{`+aos513Base+`,`+aos526Regra+`}`)
	if ligado.Params == nil || ligado.Params.ToolChoice != port.ToolChoiceWhileContractPending {
		t.Fatalf("a regra nao se leu: %+v", ligado.Params)
	}
	for _, doc := range []string{`{` + aos513Base + `}`, `{` + aos513Base + `,"params":{"tool_choice":"nunca"}}`, `{` + aos513Base + `,"params":{}}`} {
		p := aos513Perfil(t, doc)
		if p.Params != nil && (p.Params.ToolChoice != port.ToolChoiceNever || !p.Params.IsZero()) {
			t.Fatalf("%s: a omissao leu-se como regra: %+v", doc, p.Params)
		}
	}
	const marca = "VALOR-RECUSADO-526"
	for nome, doc := range map[string]string{
		"fora do vocabulario":     `{` + aos513Base + `,"params":{"tool_choice":"` + marca + `"}}`,
		"valor do wire required":  `{` + aos513Base + `,"params":{"tool_choice":"required"}}`,
		"valor do wire auto":      `{` + aos513Base + `,"params":{"tool_choice":"auto"}}`,
		"noutra caixa":            `{` + aos513Base + `,"params":{"tool_choice":"Enquanto_o_contrato_nao_estiver_satisfeito"}}`,
		"objecto do wire":         `{` + aos513Base + `,"params":{"tool_choice":{"type":"function"}}}`,
		"tipo errado":             `{` + aos513Base + `,"params":{"tool_choice":true}}`,
		"chave noutra caixa":      `{` + aos513Base + `,"params":{"Tool_Choice":"nunca"}}`,
		"repetida":                `{` + aos513Base + `,"params":{"tool_choice":"nunca","tool_choice":"enquanto_o_contrato_nao_estiver_satisfeito"}}`,
		"sem a capacidade tools":  `{"requested":"gpt-4o","expected_model":"openai/k3","wire_class":"openai-chat-completions","capabilities":[],` + aos526Regra + `}`,
		"na raiz e nao em params": `{` + aos513Base + `,"tool_choice":"enquanto_o_contrato_nao_estiver_satisfeito"}`,
		"espaco a volta do valor": `{` + aos513Base + `,"params":{"tool_choice":" nunca"}}`,
		"nome de tool como valor": `{` + aos513Base + `,"params":{"tool_choice":"doc_read"}}`,
		"valor do wire none":      `{` + aos513Base + `,"params":{"tool_choice":"none"}}`,
	} {
		_, err := modelgateway.ParseRouteProfile([]byte(doc))
		if !errors.Is(err, modelgateway.ErrBadRouteProfile) {
			t.Errorf("%s: tinha de recusar com ErrBadRouteProfile; veio %v", nome, err)
			continue
		}
		if strings.Contains(err.Error(), marca) {
			t.Errorf("%s: a mensagem repete o valor recusado: %v", nome, err)
		}
	}
	// Um perfil em código com a regra fora do vocabulário não compõe gateway.
	mau := ligado
	mau.Params = &port.RequestParams{ToolChoice: "required"}
	if _, err := modelgateway.NewRouteProfileSet(mau); !errors.Is(err, modelgateway.ErrBadRouteProfile) {
		t.Fatalf("o conjunto de perfis aceitou a regra fora do vocabulario: %v", err)
	}
}

// O DIGEST DE UM PERFIL SEM O CAMPO É O DE ANTES. Os valores pinados são de antes do AOS-526: os
// quatro da tabela em código (base c3db6ad6, AOS-513) e dois perfis com parâmetros (AOS-516). A
// omissão escrita por extenso (`"tool_choice":"nunca"`) também não o muda; a regra muda-o.
func TestAOS526_Digest_PerfilSemOCampoEODeAntes(t *testing.T) {
	for _, p := range modelgateway.RouteProfiles() {
		if p.Digest() != aos513Digests[p.Requested] {
			t.Fatalf("o digest do perfil %q mudou: %s (antes %s)", p.Requested, p.Digest(), aos513Digests[p.Requested])
		}
	}
	comParametros := map[string]string{
		`{"requested":"rota-de-ensaio","expected_model":"anthropic/PREENCHER-NOME-DO-MODELO","wire_class":"openai-chat-completions","capabilities":["tools"],"params":{"thinking":{"type":"enabled","budget_tokens":2048},"max_tokens":16000%s},"projection_version":"1.3.0","devolver":"obrigatorio"}`: "sha256:b27665e779dc8cbc70339a7e66d016bdabaccd11da549ef3637435e71117769f",
		`{"requested":"r","expected_model":"openrouter/a/m","wire_class":"openai-chat-completions","capabilities":["tools"],"params":{"reasoning_effort":"medium","max_tokens":16000%s},"projection_version":"1.3.0","devolver":"opcional","tool_call_id":"provider"}`:                                  "sha256:6717a0159a8e2138c181565cd534df8dbcee91a948eb45f854f53a31121c93a7",
	}
	for molde, quer := range comParametros {
		for _, extra := range []string{"", `,"tool_choice":"nunca"`} {
			if got := aos513Perfil(t, strings.Replace(molde, "%s", extra, 1)).Digest(); got != quer {
				t.Fatalf("o digest de um perfil sem a regra mudou (%q): %s, quero %s", extra, got, quer)
			}
		}
		if got := aos513Perfil(t, strings.Replace(molde, "%s", `,"tool_choice":"enquanto_o_contrato_nao_estiver_satisfeito"`, 1)).Digest(); got == quer {
			t.Fatalf("a regra nao entrou no digest do perfil")
		}
	}
	base := aos513Digests["gpt-4o"]
	if aos513Perfil(t, `{`+aos513Base+`,"params":{"tool_choice":"nunca"}}`).Digest() != base {
		t.Fatalf("`nunca` por extenso mudou o digest do perfil da tabela")
	}
	if aos513Perfil(t, `{`+aos513Base+`,`+aos526Regra+`}`).Digest() == base {
		t.Fatalf("a regra nao mudou o digest do perfil")
	}
}

// O PEDIDO COM A REGRA. Uma tool por cumprir oferecida ⇒ nomeada; duas ⇒ `required`; o contrato
// satisfeito (a vista sem nada por cumprir — o turno a seguir à tool call que o cumpriu), um nó
// sem contrato e um nó sem tools ⇒ o campo não vai, e o corpo é o de um perfil sem a regra.
func TestAOS526_Pedido_LevaToolChoiceSoEnquantoOContratoNaoEstaSatisfeito(t *testing.T) {
	com := aos513Compor(t, &aos513Provider{}, aos513Perfil(t, `{`+aos513Base+`,`+aos526Regra+`}`))
	sem := aos513Compor(t, &aos513Provider{})
	if !reflect.DeepEqual(com.set.WithParams(), []string{"gpt-4o"}) {
		t.Fatalf("a rota com a regra tem de estar no rotulo das recusas de parametros: %v", com.set.WithParams())
	}
	const cabeca = `{"model":"gpt-4o","messages":[{"role":"user","content":"olá"}],"tools":[{"type":"function","function":{"name":"doc_read","parameters":{"type":"object"}}},{"type":"function","function":{"name":"doc_write","parameters":{"type":"object"}}}]`

	// UMA tool por cumprir: a forma exacta do pedido.
	corpo, out, err := aos526Turno(t, com, aos526Tools(), []string{"doc_read"})
	if err != nil {
		t.Fatal(err)
	}
	if quero := cabeca + `,"tool_choice":{"type":"function","function":{"name":"doc_read"}}}`; corpo != quero {
		t.Fatalf("uma tool por cumprir:\n veio:  %s\n quero: %s", corpo, quero)
	}
	if quero := map[string]string{port.ParamKeyToolChoice: port.ToolChoiceManifestNamed}; !reflect.DeepEqual(agentruntime.NormalizeRequestParams(out.RequestParams), quero) {
		t.Fatalf("o turno nao declara o tool_choice na forma que o runtime aceita: %v", out.RequestParams)
	}

	// DUAS por cumprir: `required`.
	corpo, out, err = aos526Turno(t, com, aos526Tools(), []string{"doc_write", "doc_read"})
	if err != nil || corpo != cabeca+`,"tool_choice":"required"}` || out.RequestParams[port.ParamKeyToolChoice] != port.ToolChoiceRequired {
		t.Fatalf("duas tools por cumprir: %s %v (%v)", corpo, out.RequestParams, err)
	}

	// SEM NADA POR CUMPRIR, com o perfil com a regra: o corpo é o de um perfil sem ela.
	controlo, _, err := aos526Turno(t, sem, aos526Tools(), nil)
	if err != nil || controlo != cabeca+`}` {
		t.Fatalf("o controlo sem a regra: %s (%v)", controlo, err)
	}
	for nome, c := range map[string]struct {
		tools     []port.Tool
		pendentes []string
		quero     string
	}{
		"contrato satisfeito":       {aos526Tools(), nil, controlo},
		"no sem contrato":           {aos526Tools(), []string{}, controlo},
		"tool por cumprir nao dada": {aos526Tools(), []string{"outra"}, controlo},
		"no sem tools":              {nil, []string{"doc_read"}, `{"model":"gpt-4o","messages":[{"role":"user","content":"olá"}]}`},
		"no sem tools nem contrato": {nil, nil, `{"model":"gpt-4o","messages":[{"role":"user","content":"olá"}]}`},
	} {
		corpo, out, err := aos526Turno(t, com, c.tools, c.pendentes)
		if err != nil || corpo != c.quero || out.RequestParams != nil {
			t.Fatalf("%s: o pedido levou tool_choice ou parametros:\n veio:  %s %v\n quero: %s (%v)", nome, corpo, out.RequestParams, c.quero, err)
		}
	}

	// O CONTROLO: o perfil SEM a regra nunca o leva, mesmo com tools por cumprir.
	for _, pendentes := range [][]string{{"doc_read"}, {"doc_read", "doc_write"}} {
		corpo, out, err := aos526Turno(t, sem, aos526Tools(), pendentes)
		if err != nil || corpo != controlo || out.RequestParams != nil {
			t.Fatalf("sem a regra, com %v por cumprir: %s %v (%v)", pendentes, corpo, out.RequestParams, err)
		}
	}
}

// O `tool_choice` DE UM CHAMADOR. Numa rota com a regra é só a regra que o escreve (o do chamador
// é apagado quando nada está por cumprir). Numa rota sem a regra o pedido não é tocado: é o
// comportamento de antes.
func TestAOS526_Pedido_ARegraEDonaDoCampo(t *testing.T) {
	com := aos513Compor(t, &aos513Provider{}, aos513Perfil(t, `{`+aos513Base+`,`+aos526Regra+`}`))
	sem := aos513Compor(t, &aos513Provider{})
	pedido := aos505Pedido("gpt-4o")
	pedido.Tools, pedido.ToolChoice = aos526Tools(), port.ToolChoiceAuto
	if _, err := com.gw.Chat(context.Background(), pedido); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(com.provider.pedidos()[0], []byte("tool_choice")) {
		t.Fatalf("numa rota com a regra, o tool_choice do chamador saiu: %s", com.provider.pedidos()[0])
	}
	if _, err := sem.gw.Chat(context.Background(), pedido); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(sem.provider.pedidos()[0], []byte(`,"tool_choice":"auto"}`)) {
		t.Fatalf("numa rota sem a regra, o pedido do chamador mudou: %s", sem.provider.pedidos()[0])
	}
}

// UM 4xx A UM PEDIDO QUE LEVOU `tool_choice` CONTA COMO RECUSA DE PARÂMETROS; um 4xx a um pedido
// da mesma rota que não o levou, não. O gateway não retira o campo nem repete o pedido.
func TestAOS526_Recusa_Um4xxAoToolChoiceContaComoRecusaDeParametros(t *testing.T) {
	provider := &aos513Provider{status: http.StatusBadRequest, corpo: `{"error":{"message":"tool_choice nao suportado","type":"invalid_request_error"}}`}
	m := aos513Compor(t, provider, aos513Perfil(t, `{`+aos513Base+`,`+aos526Regra+`}`))
	if _, _, err := aos526Turno(t, m, aos526Tools(), []string{"doc_read"}); err == nil {
		t.Fatal("um 400 do provider tinha de subir como erro")
	}
	if quero := []modelgateway.RouteParamsRejection{{Route: "gpt-4o", Status: "400"}}; !reflect.DeepEqual(m.recusas, quero) {
		t.Fatalf("recusas contadas: %v, quero %v", m.recusas, quero)
	}
	if n := len(provider.pedidos()); n != 1 {
		t.Fatalf("o gateway repetiu o pedido: %d pedidos", n)
	}
	if _, _, err := aos526Turno(t, m, aos526Tools(), nil); err == nil {
		t.Fatal("um 400 do provider tinha de subir como erro")
	}
	if len(m.recusas) != 1 {
		t.Fatalf("um 4xx sem tool_choice no pedido contou como recusa de parametros: %v", m.recusas)
	}
}

// DETERMINISMO: o mesmo turno dá os mesmos bytes.
func TestAOS526_Pedido_Determinista(t *testing.T) {
	m := aos513Compor(t, &aos513Provider{}, aos513Perfil(t, `{`+aos513Base+`,`+aos526Regra+`}`))
	um, _, _ := aos526Turno(t, m, aos526Tools(), []string{"doc_write", "doc_read"})
	outro, _, _ := aos526Turno(t, m, aos526Tools(), []string{"doc_write", "doc_read"})
	if um != outro {
		t.Fatalf("dois turnos iguais deram corpos diferentes:\n%s\n%s", um, outro)
	}
}

// A VIA ASSINADA ATÉ AO PEDIDO: um registo de entrada com a regra carrega, o perfil dele compõe o
// gateway, e o pedido leva `tool_choice` enquanto há uma tool por cumprir.
func TestAOS526_Registo_ARegraDoRegistoChegaAoPedido(t *testing.T) {
	pol, err := allowlist.LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	key := rt.NewKey(t)
	dir := t.TempDir()
	comRegra := strings.Replace(rt.ProfileJSON, `"capabilities":["tools"]`, `"capabilities":["tools"],`+aos526Regra, 1)
	rt.Good(t, dir, "com-regra", comRegra, rt.Board, rt.Region, key)
	entradas, err := routeentry.LoadDir(routeentry.Config{Dir: dir, TrustAnchor: key.Anchor(), Board: rt.Board, Region: rt.Region, Allowlist: pol})
	if err != nil || len(entradas) != 1 {
		t.Fatalf("o registo nao carregou: %v", err)
	}
	m := aos513Compor(t, &aos513Provider{}, entradas[0].Profile)
	corpo, _, err := aos526Turno(t, m, aos526Tools(), []string{"doc_read"})
	if err != nil || !strings.HasSuffix(corpo, `,"tool_choice":{"type":"function","function":{"name":"doc_read"}}}`) {
		t.Fatalf("o pedido da rota do registo nao levou o tool_choice: %s (%v)", corpo, err)
	}
	corpo, _, err = aos526Turno(t, m, aos526Tools(), nil)
	if err != nil || strings.Contains(corpo, "tool_choice") {
		t.Fatalf("com o contrato satisfeito o pedido levou o tool_choice: %s (%v)", corpo, err)
	}
}

// O ADAPTADOR FORÇA SÓ O QUE O KERNEL DEIXA FORÇAR. Depois da primeira tool call do run, as tools
// por cumprir estão em `CompletionPending` e não em `CompletionToForce`: o pedido não leva o campo.
// O adaptador lê só a segunda lista: com duas a forçar vai `required`, com uma vai nomeada.
func TestAOS526_Pedido_SoSeForcaOQueOKernelDeixa(t *testing.T) {
	m := aos513Compor(t, &aos513Provider{}, aos513Perfil(t, `{`+aos513Base+`,`+aos526Regra+`}`))
	turno := func(pendentes, aForcar []string) string {
		t.Helper()
		antes := len(m.provider.pedidos())
		if _, err := modelgateway.NewModelClient(m.gw, "gpt-4o", modelgateway.WithPrincipal("tok"), modelgateway.WithRegionBoard("eu", "board-eu"), modelgateway.WithTools(aos526Tools())).
			Call(context.Background(), agentruntime.PromptView{Materialized: []byte("olá"), CompletionPending: pendentes, CompletionToForce: aForcar}); err != nil {
			t.Fatal(err)
		}
		return string(m.provider.pedidos()[antes])
	}
	if corpo := turno([]string{"doc_read", "doc_write"}, nil); strings.Contains(corpo, "tool_choice") {
		t.Fatalf("depois da primeira tool call do run, o campo foi: %s", corpo)
	}
	if corpo := turno([]string{"doc_read", "doc_write"}, []string{"doc_read", "doc_write"}); !strings.HasSuffix(corpo, `,"tool_choice":"required"}`) {
		t.Fatalf("antes da primeira tool call, com duas por cumprir: %s", corpo)
	}
	if corpo := turno([]string{"doc_read"}, []string{"doc_read"}); !strings.HasSuffix(corpo, `,"tool_choice":{"type":"function","function":{"name":"doc_read"}}}`) {
		t.Fatalf("o controlo: uma tool a forcar vai nomeada: %s", corpo)
	}
}
