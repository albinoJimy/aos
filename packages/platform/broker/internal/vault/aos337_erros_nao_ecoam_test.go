package vault

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// AOS-337 — OS ERROS DO CLIENTE VAULT DO BROKER DEIXAM DE ECOAR CREDENCIAIS.
//
// Achado na revisão adversarial do AOS-333, que fechou a fuga no helper de validação e no banner
// do nó e parou aí. Os caminhos que FALAM COM A REDE continuavam a ecoar o endereço, por dois
// ramos de naturezas diferentes — e o ticket contava três sítios quando são QUATRO, porque o
// ramo de `NewRequest` do `Fetch` tem o mesmo defeito do de `Ready`:
//
//   - `NewRequest` devolve um `*url.Error` com o endereço COMO FOI ESCRITO, sem redacção
//     nenhuma. É o pior dos dois: a SENHA vai INTEIRA;
//   - `Do` devolve um `*url.Error` em que o `net/http` já redige a senha e deixa o UTILIZADOR,
//     que numa URL de Vault é quem identifica o principal.
//
// Os dois sobem ao `/readyz` e ao banner de prontidão do nó, que é onde um segredo seria mais
// lido.
//
// ALCANCE. A via do AMBIENTE está fechada desde o AOS-333: `AOS_BROKER_VAULT_ADDR` passa por
// `integration.CheckSecureTransportURL`, que recusa user-info. Fica aberta a via PROGRAMÁTICA —
// `NewKVv2` só verifica `addr != ""` —, e é essa que estes testes exercem. Um controlo que só
// existe no parser do ambiente deixa de existir no dia em que alguém compuser `KVv2Config` por
// outra via.

const aos337Senha = "s3nh4-do-broker-que-nao-pode-vazar"

// exigeSemCredenciais é o critério comum aos quatro ramos: nem a senha, nem o utilizador.
func exigeSemCredenciais(t *testing.T, msg string) {
	t.Helper()
	if strings.Contains(msg, aos337Senha) {
		t.Errorf("a mensagem contem a SENHA do Vault — vai para o /readyz e para o banner: %s", msg)
	}
	if strings.Contains(msg, "admin") {
		t.Errorf("a mensagem contem o utilizador, que numa URL de Vault identifica o principal: %s", msg)
	}
}

func clienteAOS337(t *testing.T, addr string) *KVv2 {
	t.Helper()
	c, err := NewKVv2(KVv2Config{Addr: addr, Token: "token-de-teste"})
	if err != nil {
		t.Fatalf("NewKVv2(%q): %v", addr, err)
	}
	return c
}

// TestAOS337_ParseFalhadoNaoEcoaOEnderecoCru cobre o ramo PIOR. Uma porta inválida faz o
// `url.Parse` do `NewRequest` recusar, e era aí que o valor inteiro — senha incluída — ia para a
// mensagem, porque o `net/http` só redige nos erros que ELE constrói.
func TestAOS337_ParseFalhadoNaoEcoaOEnderecoCru(t *testing.T) {
	t.Parallel()
	// A porta com espaço é o que o `url.Parse` recusa («invalid port»).
	c := clienteAOS337(t, "https://admin:"+aos337Senha+"@vault.interno:82 00")
	ctx := context.Background()

	t.Run("Fetch", func(t *testing.T) {
		_, err := c.Fetch(ctx, Key{Provider: "p", Region: "eu", Capability: "cap:http.get"})
		if err == nil {
			t.Fatal("um endereco que nao produz pedido devia dar erro")
		}
		if !errors.Is(err, ErrKVFetch) {
			t.Errorf("o erro tem de ser ATRIBUIVEL: %v", err)
		}
		exigeSemCredenciais(t, err.Error())
	})

	t.Run("Ready", func(t *testing.T) {
		err := c.Ready(ctx)
		if err == nil {
			t.Fatal("um endereco que nao produz pedido devia dar erro")
		}
		// Este ramo devolvia o `*url.Error` cru, SEM sentinela nenhuma: o /readyz recebia um
		// erro que nem era atribuivel ao Vault.
		if !errors.Is(err, ErrKVFetch) {
			t.Errorf("o erro tem de ser ATRIBUIVEL: %v", err)
		}
		exigeSemCredenciais(t, err.Error())
	})
}

// TestAOS337_ErroDeTransporteNaoEcoaOUtilizador cobre o ramo em que o endereço É analisável: o
// pedido sai, o transporte falha, e o `*url.Error` do `net/http` traz `admin:***@`.
//
// O CONTROLO vive aqui e não no teste acima: com um endereço analisável há uma forma publicável
// para dar, e a mensagem TEM de a dar. Sem isto, um redactor que apagasse tudo passaria
// `exigeSemCredenciais` por vacuidade — e um `/readyz` que não diz onde o nó falhou a falar
// troca uma fuga por um diagnóstico inútil.
func TestAOS337_ErroDeTransporteNaoEcoaOUtilizador(t *testing.T) {
	t.Parallel()
	// Porta 1 em loopback: analisável, e a ligação é recusada de imediato.
	c := clienteAOS337(t, "http://admin:"+aos337Senha+"@127.0.0.1:1")
	ctx := context.Background()

	t.Run("Fetch", func(t *testing.T) {
		_, err := c.Fetch(ctx, Key{Provider: "p", Region: "eu", Capability: "cap:http.get"})
		if err == nil {
			t.Fatal("um vault inalcancavel devia dar erro")
		}
		exigeSemCredenciais(t, err.Error())
		if !strings.Contains(err.Error(), "127.0.0.1:1") {
			t.Errorf("a mensagem devia continuar a nomear o host e a porta: %v", err)
		}
	})

	t.Run("Ready", func(t *testing.T) {
		err := c.Ready(ctx)
		if err == nil {
			t.Fatal("um vault inalcancavel devia dar erro")
		}
		exigeSemCredenciais(t, err.Error())
		if !strings.Contains(err.Error(), "127.0.0.1:1") {
			t.Errorf("a mensagem devia continuar a nomear o host e a porta: %v", err)
		}
	})
}

// TestAOS337_RedactURL fixa o que o redactor pode devolver. É a cópia deliberada de
// `integration.RedactURL` — este módulo não pode importar o composition-root (ADR-019) — e a
// cópia só serve se se comportar igual, pelo que os casos são os mesmos.
func TestAOS337_RedactURL(t *testing.T) {
	t.Parallel()
	casos := []struct{ nome, entrada, quer string }{
		{"credenciais são deitadas fora", "https://admin:" + aos337Senha + "@vault.interno:8200", "https://vault.interno:8200"},
		{"caminho é deitado fora", "https://vault.interno:8200/v1/secret/data/prod", "https://vault.interno:8200"},
		{"query é deitada fora", "https://vault.interno:8200?token=" + aos337Senha, "https://vault.interno:8200"},
		{"forma limpa fica igual", "https://vault.interno:8200", "https://vault.interno:8200"},
		{"loopback fica igual", "http://127.0.0.1:8200", "http://127.0.0.1:8200"},
		{"sem esquema mantém-se re-analisável", "//admin:" + aos337Senha + "@vault.interno:8200", "//vault.interno:8200"},
		{"inválido não devolve o valor", "://" + aos337Senha, "(inválido)"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Parallel()
			got := redactURL(c.entrada)
			if got != c.quer {
				t.Errorf("redactURL(%q) = %q, quer %q", c.entrada, got, c.quer)
			}
			if strings.Contains(got, aos337Senha) {
				t.Errorf("a forma redigida contem o segredo: %q", got)
			}
		})
	}
}

// TestAOS337_ErroRedigidoPreservaACausa é o segundo CONTROLO. Redigir não pode custar o
// diagnóstico: o `Op` e a causa são o que distingue «DNS não resolve» de «ligação recusada» de
// «TLS inválido», e um erro que não seja `*url.Error` tem de passar tal-qual.
func TestAOS337_ErroRedigidoPreservaACausa(t *testing.T) {
	t.Parallel()
	outro := errors.New("erro que nao e de URL")
	if got := erroRedigido("https://admin:pw@vault:8200", outro); !errors.Is(got, outro) {
		t.Errorf("um erro que nao e *url.Error tem de passar tal-qual: %v", got)
	}

	c := clienteAOS337(t, "http://admin:"+aos337Senha+"@127.0.0.1:1")
	_, err := c.Fetch(context.Background(), Key{Provider: "p", Region: "eu", Capability: "cap:http.get"})
	if err == nil {
		t.Fatal("um vault inalcancavel devia dar erro")
	}
	// A causa concreta do transporte tem de sobreviver à redacção.
	if !strings.Contains(err.Error(), "Get ") {
		t.Errorf("o Op perdeu-se na redaccao: %v", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "refused") && !strings.Contains(strings.ToLower(err.Error()), "recus") {
		t.Logf("nota: a causa do sistema nao menciona recusa (pode variar por SO): %v", err)
	}
}
