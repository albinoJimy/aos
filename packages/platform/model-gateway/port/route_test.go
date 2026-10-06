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
	got := port.ServedRouteFromHeaders(func(nome string) string {
		pedidos = append(pedidos, strings.ToLower(nome))
		return h.Get(nome)
	})
	if got != (port.ServedRoute{Model: "openai/kimi-for-coding", APIHost: "api.kimi.com"}) {
		t.Fatalf("rota = %+v", got)
	}
	if strings.Join(pedidos, ",") != "x-litellm-model-name,x-litellm-model-api-base" {
		t.Fatalf("cabecalhos lidos = %v; quero so o nome do modelo e o api_base", pedidos)
	}
	if vazio := port.ServedRouteFromHeaders(http.Header{}.Get); vazio != (port.ServedRoute{}) {
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
