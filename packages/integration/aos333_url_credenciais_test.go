package integration_test

import (
	"strings"
	"testing"

	"github.com/aos-ref/integration"
)

// TestAOS333_URLComCredenciaisERecusada fecha uma via de fuga de segredo que o doc-comment do
// próprio helper declarava fechada.
//
// A nota dizia: «a mensagem nunca inclui credenciais (user-info numa URL não é suportado por
// nenhum chamador)». Era falsa nas duas metades. Nada rejeitava `https://user:pass@vault:8200`,
// e o ramo de parse falhado devolvia o URL cru — que o `%v` do wrap de cada chamador ecoava
// inteiro para o log de arranque.
//
// Uma senha de Vault num log recolhido e retido é o mesmo segredo em claro que o ADR-006 proíbe;
// muda só quem o lê. E embutir credenciais num URL é forma legítima de as passar a um Vault, pelo
// que a segurança não podia assentar em ninguém as usar.
func TestAOS333_URLComCredenciaisERecusada(t *testing.T) {
	t.Parallel()
	const senha = "s3nh4-que-nao-pode-aparecer"
	casos := []struct {
		nome string
		url  string
	}{
		{"https com utilizador e senha", "https://admin:" + senha + "@vault.interno:8200"},
		{"https só com utilizador", "https://admin@vault.interno:8200"},
		{"http em loopback também é recusado", "http://admin:" + senha + "@127.0.0.1:8200"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Parallel()
			err := integration.CheckSecureTransportURL(c.url)
			if err == nil {
				t.Fatalf("URL com credenciais devia ser recusado: %q", c.url)
			}
			// O QUE MAIS IMPORTA: a recusa não pode ela própria vazar o segredo.
			if strings.Contains(err.Error(), senha) {
				t.Errorf("a MENSAGEM DE ERRO contém a senha: %v", err)
			}
			if strings.Contains(err.Error(), "admin") {
				t.Errorf("a mensagem contém o utilizador, que numa URL de Vault identifica o principal: %v", err)
			}
		})
	}
}

// TestAOS333_ErroDeParseNaoEcoaOValor cobre o ramo que era o pior dos dois: uma URL que o parser
// recusa é precisamente onde uma credencial mal escapada tem mais probabilidade de estar, e era
// aí que o valor inteiro ia para a mensagem.
func TestAOS333_ErroDeParseNaoEcoaOValor(t *testing.T) {
	t.Parallel()
	const senha = "s3nh4-secreta"
	malformadas := []string{
		"://admin:" + senha + "@vault:8200",
		"h ttps://admin:" + senha + "@vault",
		"admin:" + senha + "@vault:8200",
	}
	for _, raw := range malformadas {
		err := integration.CheckSecureTransportURL(raw)
		if err == nil {
			t.Fatalf("URL malformada devia ser recusada: %q", raw)
		}
		if strings.Contains(err.Error(), senha) {
			t.Errorf("a mensagem de parse falhado ecoa o valor com a senha: %v", err)
		}
	}
}

// TestAOS333_CriteriosLegitimosContinuamAPassar é o CONTROLO. Sem ele, um helper que recusasse
// tudo passaria os testes acima — e a recusa universal seria um defeito pior do que o que se
// fecha, porque abortaria o arranque de todos os deployments correctos.
func TestAOS333_CriteriosLegitimosContinuamAPassar(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"https://vault.interno:8200",
		"https://vault.interno",
		"http://127.0.0.1:8200",
		"http://localhost:8200",
		"http://[::1]:8200",
	} {
		if err := integration.CheckSecureTransportURL(raw); err != nil {
			t.Errorf("URL legítimo recusado: %q -> %v", raw, err)
		}
	}
}

// TestAOS333_RedactURL fixa o que o banner pode imprimir. Preserva o que o operador precisa para
// saber ONDE o nó fala — esquema, host, porta — e deita fora tudo o que possa carregar segredo.
func TestAOS333_RedactURL(t *testing.T) {
	t.Parallel()
	const senha = "s3nh4-secreta"
	casos := []struct{ nome, entrada, quer string }{
		{"credenciais são deitadas fora", "https://admin:" + senha + "@vault.interno:8200", "https://vault.interno:8200"},
		{"caminho é deitado fora", "https://vault.interno:8200/v1/secret/data/prod", "https://vault.interno:8200"},
		{"query é deitada fora", "https://vault.interno:8200?token=" + senha, "https://vault.interno:8200"},
		{"forma limpa fica igual", "https://vault.interno:8200", "https://vault.interno:8200"},
		{"loopback fica igual", "http://127.0.0.1:8200", "http://127.0.0.1:8200"},
		{"inválida não devolve o valor", "://" + senha, "(inválida)"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Parallel()
			got := integration.RedactURL(c.entrada)
			if got != c.quer {
				t.Errorf("RedactURL(%q) = %q, quer %q", c.entrada, got, c.quer)
			}
			if strings.Contains(got, senha) {
				t.Errorf("a forma redigida contém o segredo: %q", got)
			}
		})
	}
}
