package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestParseModelEgressTimeoutFromEnv fixa a gramática de AOS_MODEL_EGRESS_TIMEOUT: vazio ⇒ 0 (o
// default do caminho), valores dentro de [1s, 30m] passam tal como pedidos, e tudo o resto ABORTA
// com ErrBadModelEgressTimeout — nunca cai em silêncio no default.
func TestParseModelEgressTimeoutFromEnv(t *testing.T) {
	validos := []struct {
		raw  string
		want time.Duration
	}{
		{"", 0},
		{"  ", 0},
		{"1s", time.Second},
		{"120s", 120 * time.Second},
		{"2m", 2 * time.Minute},
		{"30m", 30 * time.Minute},
	}
	for _, c := range validos {
		t.Run("valido_"+c.raw, func(t *testing.T) {
			t.Setenv("AOS_MODEL_EGRESS_TIMEOUT", c.raw)
			got, err := parseModelEgressTimeoutFromEnv()
			if err != nil {
				t.Fatalf("%q devia ser aceite; got %v", c.raw, err)
			}
			if got != c.want {
				t.Fatalf("%q: o valor anunciado tem de ser o ligado — want %v, got %v", c.raw, c.want, got)
			}
		})
	}

	invalidos := []string{"abc", "120", "0", "0s", "-5s", "500ms", "31m", "1h"}
	for _, raw := range invalidos {
		t.Run("invalido_"+raw, func(t *testing.T) {
			t.Setenv("AOS_MODEL_EGRESS_TIMEOUT", raw)
			if _, err := parseModelEgressTimeoutFromEnv(); !errors.Is(err, ErrBadModelEgressTimeout) {
				t.Fatalf("%q devia abortar com ErrBadModelEgressTimeout; got %v", raw, err)
			}
		})
	}
}

// TestModelEgressTimeout_InvalidoAbortaAComposicao — a validação corre na composição do gateway
// (parseModelFromEnv), nos DOIS modos: um valor mau não chega a produzir cliente de modelo.
func TestModelEgressTimeout_InvalidoAbortaAComposicao(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	for _, producao := range []bool{true, false} {
		aos366ProdModelEnv(t)
		t.Setenv("AOS_MODEL_ENDPOINT", srv.URL+"/v1")
		t.Setenv("AOS_MODEL_EGRESS_TIMEOUT", "10ms")
		if _, _, err := parseModelFromEnv(producao); !errors.Is(err, ErrBadModelEgressTimeout) {
			t.Fatalf("producao=%v: AOS_MODEL_EGRESS_TIMEOUT invalida devia abortar a composicao; got %v", producao, err)
		}
	}
}

// TestModelEgressTimeout_ValidoCompoe — com um valor válido o gateway compõe em produção (https na
// allowlist derivada do endpoint), tal como sem a variável.
func TestModelEgressTimeout_ValidoCompoe(t *testing.T) {
	aos366ProdModelEnv(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	t.Setenv("AOS_MODEL_ENDPOINT", srv.URL+"/v1")
	t.Setenv("AOS_MODEL_EGRESS_TIMEOUT", "120s")

	mc, _, err := parseModelFromEnv(true)
	if err != nil {
		t.Fatalf("120s e valido e devia compor; got %v", err)
	}
	if mc == nil {
		t.Fatal("o cliente de modelo devia estar composto")
	}
}
