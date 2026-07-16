package network

import (
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// TestDestinationFromResource cobre a derivação do destino a partir do recurso
// mediado, incluindo o fail-closed (recurso de rede malformado → destino inválido →
// deny; recurso não-rede → ok=false, o hook abstém-se).
func TestDestinationFromResource(t *testing.T) {
	cases := []struct {
		name     string
		res      referencemonitor.Resource
		wantOK   bool
		wantHost string
		wantIP   string
		wantPort int
		valid    bool
	}{
		{"url https", referencemonitor.Resource{Type: "url", Value: "https://api.github.com/x"}, true, "api.github.com", "", 443, true},
		{"url http", referencemonitor.Resource{Type: "url", Value: "http://a.io"}, true, "a.io", "", 80, true},
		{"url porta explicita", referencemonitor.Resource{Type: "url", Value: "https://a.io:8443/p"}, true, "a.io", "", 8443, true},
		{"url ip", referencemonitor.Resource{Type: "url", Value: "https://93.184.216.34/"}, true, "", "93.184.216.34", 443, true},
		{"net host:port", referencemonitor.Resource{Type: "net", Value: "a.io:443"}, true, "a.io", "", 443, true},
		{"net ip:port", referencemonitor.Resource{Type: "net", Value: "10.0.0.1:443"}, true, "", "10.0.0.1", 443, true},
		// Fail-closed: rede sem porta / valor vazio → ok=true mas destino inválido.
		{"net sem porta", referencemonitor.Resource{Type: "net", Value: "a.io"}, true, "", "", 0, false},
		{"url esquema sem porta default", referencemonitor.Resource{Type: "url", Value: "ftp://a.io"}, true, "a.io", "", 0, false},
		{"rede valor vazio", referencemonitor.Resource{Type: "host", Value: ""}, true, "", "", 0, false},
		// Não-rede → ok=false (o hook abstém-se).
		{"file nao e rede", referencemonitor.Resource{Type: "file", Value: "/etc/passwd"}, false, "", "", 0, false},
		{"db nao e rede", referencemonitor.Resource{Type: "db", Value: "orders"}, false, "", "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := DestinationFromResource(tc.res)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, quero %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if d.Host != tc.wantHost || d.IP != tc.wantIP || d.Port != tc.wantPort {
				t.Fatalf("destino = %+v, quero host=%q ip=%q port=%d", d, tc.wantHost, tc.wantIP, tc.wantPort)
			}
			if d.valid() != tc.valid {
				t.Fatalf("valid() = %v, quero %v (destino %+v)", d.valid(), tc.valid, d)
			}
		})
	}
}

// TestNewDestination_Normaliza cobre a normalização de host (minúsculas, ponto final)
// e a distinção host vs IP literal.
func TestNewDestination_Normaliza(t *testing.T) {
	if d := NewDestination("API.GitHub.Com.", 443); d.Host != "api.github.com" || d.IP != "" {
		t.Fatalf("host não normalizado: %+v", d)
	}
	if d := NewDestination("93.184.216.34", 443); d.IP != "93.184.216.34" || d.Host != "" {
		t.Fatalf("IP literal deveria ir para IP: %+v", d)
	}
	if got := NewDestination("a.io", 443).String(); got != "a.io:443" {
		t.Fatalf("String() = %q", got)
	}
}
