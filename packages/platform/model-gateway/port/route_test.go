package port_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-505 — a rota servida lê-se de DOIS cabeçalhos, e do api_base fica só o host.

func TestAOS505_HostOfAPIBase_SoOHost(t *testing.T) {
	for _, c := range []struct{ entra, sai string }{
		{"http://a505-fake-a:8080/v1", "a505-fake-a:8080"},
		{"https://api.kimi.com/coding/v1", "api.kimi.com"},
		{"https://API.Kimi.COM/coding/v1", "api.kimi.com"},
		{"https://utilizador:segredo@api.exemplo.test:8443/caminho/v1?chave=valor#frag", "api.exemplo.test:8443"},
		{"api.exemplo.test:8443/v1", "api.exemplo.test:8443"},
		{"api.exemplo.test", "api.exemplo.test"},
		{"https://[::1]:8080/v1", "[::1]:8080"},
		{"  https://api.exemplo.test/v1  ", "api.exemplo.test"},
		{"", ""},
		{"   ", ""},
		{"https://", ""},
		{"/so/um/caminho", ""},
		{"https://host com espaco/v1", ""},
		{"https://host_com_sublinhado/v1", ""},
		{"https://" + strings.Repeat("a", 300) + ".test/v1", ""},
		{"https://api.exemplo.test/" + strings.Repeat("x", 3000), ""},
		// O ponto final do nome absoluto sai; a porta fica como vem.
		{"https://API.Kimi.COM./coding/v1", "api.kimi.com"},
		{"https://api.kimi.com.:8443/v1", "api.kimi.com:8443"},
		{"https://api.kimi.com:443/v1", "api.kimi.com:443"},
		{"https://api.kimi.com../v1", ""},
		{"https://./v1", ""},
		// Caracteres invisíveis no host: não é um nome DNS, fica por reportar.
		{"https://api.kimi\u200b.com/v1", ""},
		{"https://api.kimi.com\u202e/v1", ""},
	} {
		if got := port.HostOfAPIBase(c.entra); got != c.sai {
			t.Errorf("HostOfAPIBase(%q) = %q, quero %q", c.entra, got, c.sai)
		}
	}
}

func TestAOS505_SanitizeServedModel(t *testing.T) {
	if got := port.SanitizeServedModel("  openai/kimi-for-coding\r\n"); got != "openai/kimi-for-coding" {
		t.Errorf("saneado = %q", got)
	}
	if got := port.SanitizeServedModel("a\x00b\x1b[31mc"); got != "ab[31mc" {
		t.Errorf("os caracteres de controlo tinham de sair: %q", got)
	}
	longo := port.SanitizeServedModel(strings.Repeat("é", 400))
	if len(longo) > port.MaxServedModel || !utf8.ValidString(longo) {
		t.Errorf("o corte tinha de respeitar o tecto (%d) sem partir um caracter: %d bytes, valido=%v", port.MaxServedModel, len(longo), utf8.ValidString(longo))
	}
}

// Dos cabeçalhos que o proxy emite só DOIS são lidos. O identificador do deployment — um hash
// que inclui a chave do provider — não é pedido ao leitor de cabeçalhos, com este ou com
// qualquer outro nome.
func TestAOS505_ServedRouteFromHeaders_SoLeDoisCabecalhos(t *testing.T) {
	h := http.Header{}
	h.Set("x-litellm-model-name", "openai/kimi-for-coding")
	h.Set("x-litellm-model-api-base", "https://u:p@api.kimi.com/coding/v1?k=1")
	h.Set("x-litellm-model-id", "49a2733634d98e6913cebc7bba9c1d21e5377b2383bdd941f545526c168b5dbe")
	h.Set("x-litellm-model-group", "gpt-4o-mini")
	var pedidos []string
	got := port.ServedRouteFromHeaders(func(nome string) []string {
		pedidos = append(pedidos, strings.ToLower(nome))
		return h.Values(nome)
	})
	if got != (port.ServedRoute{Model: "openai/kimi-for-coding", APIHost: "api.kimi.com"}) {
		t.Fatalf("rota = %+v", got)
	}
	if strings.Join(pedidos, ",") != "x-litellm-model-name,x-litellm-model-api-base" {
		t.Fatalf("cabecalhos lidos = %v; quero so o nome do modelo e o api_base", pedidos)
	}
	if vazio := port.ServedRouteFromHeaders(http.Header{}.Values); vazio != (port.ServedRoute{}) {
		t.Fatalf("sem cabecalhos a rota fica por reportar; veio %+v", vazio)
	}
	if nulo := port.ServedRouteFromHeaders(nil); nulo != (port.ServedRoute{}) {
		t.Fatalf("leitor nil: %+v", nulo)
	}
}

// A rota não vai no wire: uma resposta serializada não a leva.
func TestAOS505_ARotaNaoVaiNoWire(t *testing.T) {
	resp := port.ChatResponse{Model: "gpt-4o", Route: port.ServedRoute{Model: "openai/k3", APIHost: "api.exemplo.test", Check: port.RouteCheckEqual, ProfileDigest: "sha256:abc"}}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	raw := string(b)
	for _, proibido := range []string{"openai/k3", "api.exemplo.test", "igual", "sha256:abc", "Route", "route"} {
		if strings.Contains(raw, proibido) {
			t.Errorf("a resposta serializada leva %q: %s", proibido, raw)
		}
	}
}

// M4 — A NORMALIZAÇÃO DO HOST é a mesma dos dois lados: minúsculas e sem ponto final. A porta
// compara-se como está — `nome:443` não é `nome`.
func TestAOS505_NormalizeAPIHost_MinusculasEPontoFinal(t *testing.T) {
	for entra, sai := range map[string]string{
		"api.kimi.com":         "api.kimi.com",
		"API.Kimi.Com":         "api.kimi.com",
		"api.kimi.com.":        "api.kimi.com",
		" Api.Kimi.Com. ":      "api.kimi.com",
		"api.kimi.com:443":     "api.kimi.com:443",
		"api.kimi.com.:443":    "api.kimi.com:443",
		"API.KIMI.COM.:8443":   "api.kimi.com:8443",
		"[::1]:8080":           "[::1]:8080",
		"[::1]":                "[::1]",
		"localhost":            "localhost",
		"api.kimi.com..":       "",
		".":                    "",
		".:443":                "",
		":443":                 "",
		"https://api.kimi.com": "",
		"api.kimi.com/v1":      "",
		"u@api.kimi.com":       "",
		"api.kimi\u200b.com":   "",
	} {
		if got := port.NormalizeAPIHost(entra); got != sai {
			t.Errorf("NormalizeAPIHost(%q) = %q, quero %q", entra, got, sai)
		}
	}
	// Os dois lados encontram-se: o que o proxy declara e o que o operador escreve.
	if a, b := port.HostOfAPIBase("https://API.Kimi.Com./coding/v1"), port.NormalizeAPIHost("api.kimi.com"); a != b || a == "" {
		t.Errorf("o declarado (%q) e o esperado (%q) tinham de coincidir", a, b)
	}
	// A porta NÃO é normalizada.
	if a, b := port.HostOfAPIBase("https://api.kimi.com:443/v1"), port.NormalizeAPIHost("api.kimi.com"); a == b {
		t.Errorf("a porta compara-se como esta: %q nao pode ser %q", a, b)
	}
}

// E1 — COMPARA-SE O VALOR CRU. Um nome que o saneamento altere sai marcado como inexacto: o
// texto saneado serve para gravar, não para comparar.
func TestAOS505_ServedRouteFromHeaders_ValorCruMarcaOQueOSaneamentoAltera(t *testing.T) {
	for _, c := range []struct {
		nome, cru, saneado string
		inexacto           bool
	}{
		{"limpo", "openai/k3", "openai/k3", false},
		{"espacos e tabulacoes das pontas nao sao do valor", " \topenai/k3\t ", "openai/k3", false},
		{"largura zero e direccao do texto", "openai/k\u200b3\u202e", "openai/k3", true},
		{"largura zero no fim", "openai/k3\u200b", "openai/k3", true},
		{"espaco nao separavel no fim", "openai/k3\u00a0", "openai/k3", true},
		{"caracter de controlo", "openai/k3\x00", "openai/k3", true},
		{"so invisiveis", "\u200b\u200b", "", true},
		{"corte no tecto", strings.Repeat("A", port.MaxServedModel+1), strings.Repeat("A", port.MaxServedModel), true},
		{"exactamente no tecto", strings.Repeat("A", port.MaxServedModel), strings.Repeat("A", port.MaxServedModel), false},
	} {
		h := http.Header{"X-Litellm-Model-Name": {c.cru}}
		got := port.ServedRouteFromHeaders(h.Values)
		if got.Model != c.saneado || got.ModelInexact != c.inexacto {
			t.Errorf("%s: modelo %q inexacto=%v; quero %q inexacto=%v", c.nome, got.Model, got.ModelInexact, c.saneado, c.inexacto)
		}
	}
}

// E3 — CABEÇALHO REPETIDO. Lêem-se todas as ocorrências: iguais entre si, segue; diferentes, o
// valor fica marcado como inexacto — venha o esperado à frente ou atrás.
func TestAOS505_ServedRouteFromHeaders_CabecalhoRepetido(t *testing.T) {
	for _, c := range []struct {
		nome             string
		modelos, bases   []string
		modelo, host     string
		mInexacto, hInex bool
	}{
		{"iguais", []string{"openai/k3", "openai/k3"}, []string{"https://api.a.test/v1", "https://API.a.test./v2"}, "openai/k3", "api.a.test", false, false},
		{"modelo: o esperado a frente", []string{"openai/k3", "openai/outro"}, nil, "openai/k3", "", true, false},
		{"modelo: o esperado atras", []string{"openai/outro", "openai/k3"}, nil, "openai/outro", "", true, false},
		{"modelo: um vazio", []string{"openai/k3", ""}, nil, "openai/k3", "", true, false},
		{"modelo: so difere em invisiveis", []string{"openai/k3", "openai/k3\u200b"}, nil, "openai/k3", "", true, false},
		{"endpoint: hosts diferentes", []string{"openai/k3"}, []string{"https://api.a.test/v1", "https://api.b.test/v1"}, "openai/k3", "api.a.test", false, true},
		{"endpoint: um ilegivel", []string{"openai/k3"}, []string{"https://api.a.test/v1", "nao e um url"}, "openai/k3", "api.a.test", false, true},
	} {
		h := http.Header{}
		for _, v := range c.modelos {
			h.Add("x-litellm-model-name", v)
		}
		for _, v := range c.bases {
			h.Add("x-litellm-model-api-base", v)
		}
		quer := port.ServedRoute{Model: c.modelo, ModelInexact: c.mInexacto, APIHost: c.host, APIHostInexact: c.hInex}
		if got := port.ServedRouteFromHeaders(h.Values); got != quer {
			t.Errorf("%s: %+v\n quero %+v", c.nome, got, quer)
		}
	}
}
