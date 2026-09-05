package redaction_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/aos-ref/substrate/redaction"
)

// AOS-337 — a redacção ESTRUTURAL de endereços é testada no pacote que a possui.
//
// Os consumidores (`platform/broker`, `integration`, `cmd/aos`) têm os seus próprios testes de
// que a usam; estes fixam o CONTRATO. Vive aqui porque foi a ausência de um sítio partilhado que
// produziu três cópias, e a terceira divergiu da primeira no dia em que nasceu.

const segredo = "s3nh4-que-nao-pode-sair"

func TestAOS337_URL(t *testing.T) {
	t.Parallel()
	casos := []struct{ nome, entrada, quer string }{
		{"user-info deitado fora", "https://admin:" + segredo + "@vault.interno:8200", "https://vault.interno:8200"},
		{"só utilizador deitado fora", "https://admin@vault.interno:8200", "https://vault.interno:8200"},
		{"caminho deitado fora", "https://vault.interno:8200/v1/secret/data/prod", "https://vault.interno:8200"},
		{"query deitada fora", "https://vault.interno:8200?token=" + segredo, "https://vault.interno:8200"},
		{"fragmento deitado fora", "https://vault.interno:8200/x#" + segredo, "https://vault.interno:8200"},
		{"forma limpa fica igual", "https://vault.interno:8200", "https://vault.interno:8200"},
		{"loopback fica igual", "http://127.0.0.1:8200", "http://127.0.0.1:8200"},
		{"IPv6", "https://[::1]:8200/x", "https://[::1]:8200"},
		{"sem esquema continua re-analisavel", "//admin:" + segredo + "@vault.interno:8200", "//vault.interno:8200"},
		{"espacos sao aparados", "  https://vault.interno:8200  ", "https://vault.interno:8200"},
		// O host verdadeiro é o que vem DEPOIS do último `@`. Um endereço construído para
		// parecer outro não engana o redactor — e é o host real que tem de aparecer, porque é
		// com esse que o nó falou.
		{"host verdadeiro e nao o que parece", "https://vault.interno:8200@evil.example:9000", "https://evil.example:9000"},
		{"invalida nao devolve o valor", "://" + segredo, "(inválida)"},
		{"vazia nao devolve o valor", "", "(inválida)"},
		{"opaca nao devolve o valor", "mailto:" + segredo + "@exemplo.pt", "(inválida)"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Parallel()
			got := redaction.URL(c.entrada)
			if got != c.quer {
				t.Errorf("URL(%q) = %q, quer %q", c.entrada, got, c.quer)
			}
			if strings.Contains(got, segredo) {
				t.Errorf("a forma redigida contem o segredo: %q", got)
			}
		})
	}
}

func TestAOS337_TransportError(t *testing.T) {
	t.Parallel()

	t.Run("o utilizador nao sobrevive", func(t *testing.T) {
		t.Parallel()
		// A forma que o `net/http` produz: senha já redigida, utilizador intacto.
		err := &url.Error{
			Op:  "Get",
			URL: "http://admin:***@vault.interno:8200/v1/secret/data/prod",
			Err: errors.New("dial tcp: connection refused"),
		}
		got := redaction.TransportError("http://vault.interno:8200", err).Error()
		if strings.Contains(got, "admin") {
			t.Errorf("o utilizador sobreviveu: %s", got)
		}
		if !strings.Contains(got, "vault.interno:8200") {
			t.Errorf("o host desapareceu — redigir nao pode custar o diagnostico: %s", got)
		}
		if !strings.Contains(got, "connection refused") {
			t.Errorf("a causa desapareceu: %s", got)
		}
		if !strings.Contains(got, "Get") {
			t.Errorf("o Op desapareceu: %s", got)
		}
	})

	t.Run("desce pelos aninhados", func(t *testing.T) {
		t.Parallel()
		interior := &url.Error{
			Op:  "parse",
			URL: "http://proxy-admin:" + segredo + "@proxy.interno:3128",
			Err: errors.New("invalid port"),
		}
		exterior := &url.Error{Op: "Get", URL: "http://vault.interno:8200/x", Err: interior}
		got := redaction.TransportError("http://vault.interno:8200", exterior).Error()
		if strings.Contains(got, segredo) || strings.Contains(got, "proxy-admin") {
			t.Errorf("a credencial do nivel INTERIOR saiu: %s", got)
		}
		// Cada nível é redigido com o SEU endereço: perde-se a credencial, não a informação
		// de contra quem se falhou.
		if !strings.Contains(got, "proxy.interno:3128") {
			t.Errorf("o endereco do proxy desapareceu — deixa de se saber contra quem falhou: %s", got)
		}
	})

	t.Run("erro sem endereco usa o do chamador", func(t *testing.T) {
		t.Parallel()
		err := &url.Error{Op: "Get", URL: "", Err: errors.New("x")}
		got := redaction.TransportError("https://admin:"+segredo+"@vault.interno:8200", err).Error()
		if strings.Contains(got, segredo) || strings.Contains(got, "admin") {
			t.Errorf("o recurso ao endereco do chamador nao pode vazar: %s", got)
		}
		if !strings.Contains(got, "vault.interno:8200") {
			t.Errorf("sem endereco no erro, o do chamador tinha de aparecer redigido: %s", got)
		}
	})

	t.Run("o que nao e url.Error passa tal-qual", func(t *testing.T) {
		t.Parallel()
		outro := errors.New("erro que nao e de URL")
		if got := redaction.TransportError("https://admin:pw@vault:8200", outro); !errors.Is(got, outro) {
			t.Errorf("nao se inventa redaccao sobre uma forma que nao se conhece: %v", got)
		}
	})

	t.Run("errors.Is continua a atravessar", func(t *testing.T) {
		t.Parallel()
		// A causa é envolvida com `%w`: quem faz `errors.Is` sobre a causa continua a
		// encontrá-la depois da redacção. Sem isto, redigir partiria consumidores.
		sentinela := errors.New("sentinela")
		err := &url.Error{Op: "Get", URL: "http://vault:8200", Err: sentinela}
		if !errors.Is(redaction.TransportError("http://vault:8200", err), sentinela) {
			t.Error("a redaccao partiu a cadeia de errors.Is")
		}
	})
}
