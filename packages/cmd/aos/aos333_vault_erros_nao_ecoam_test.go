package main

import (
	"context"
	"strings"
	"testing"
)

// AOS-333 (revisão adversarial, achado F2) — OS CAMINHOS QUE FALAM COM A REDE.
//
// A correcção original parou no banner, e os erros dos clientes Vault continuavam a ecoar o
// endereço por DOIS ramos com naturezas diferentes:
//
//   - o `http.NewRequestWithContext` devolve um `*url.Error` com a URL COMO FOI ESCRITA — sem
//     redacção nenhuma. Era o único sítio do nó onde a SENHA ia INTEIRA para o `/readyz`;
//   - o `http.Client.Do` devolve um `*url.Error` em que o `net/http` já redige a senha
//     (`http://admin:***@host/…`) e deixa o UTILIZADOR intacto — que numa URL de Vault é quem
//     identifica o principal, e é o argumento pelo qual o banner deste ticket também o deita fora.
//
// O ambiente já não deixa compor um endereço com credenciais (`CheckSecureTransportURL`
// recusa-o). Estes testes cobrem a via que continua aberta — `Config` composta
// programaticamente —, que é a mesma via que o teste do banner invoca como justificação da sua
// defesa em profundidade. Um controlo que só existe num sítio deixa de existir no dia em que
// esse sítio mudar.

const aos333F2Senha = "s3nh4-do-transit-que-nao-pode-vazar"

// exigeSemCredenciais é o critério mínimo: nem a senha, nem o utilizador. Aplica-se aos DOIS
// ramos, incluindo aquele em que o endereço nem se consegue analisar para redigir.
func exigeSemCredenciais(t *testing.T, msg string) {
	t.Helper()
	if strings.Contains(msg, aos333F2Senha) {
		t.Errorf("a mensagem contem a SENHA do Vault — vai para o /readyz e para o banner: %s", msg)
	}
	if strings.Contains(msg, "admin") {
		t.Errorf("a mensagem contem o utilizador, que numa URL de Vault identifica o principal: %s", msg)
	}
}

// TestAOS333_ParseFalhadoNaoEcoaAURLCrua cobre o ramo PIOR: uma porta inválida faz o
// `url.Parse` do `NewRequest` recusar, e era aí que o valor inteiro — senha incluída — ia para a
// mensagem, porque o `net/http` só redige nos erros que ELE constrói.
func TestAOS333_ParseFalhadoNaoEcoaAURLCrua(t *testing.T) {
	t.Parallel()
	// A porta com espaço é o que o `url.Parse` recusa («invalid port»).
	v := newVaultKeyVault("https://admin:"+aos333F2Senha+"@vault.interno:82 00", "transit", "tok")

	_, _, err := v.do("GET", "/v1/transit/keys/x", nil)
	if err == nil {
		t.Fatal("um endereco que nao produz pedido devia dar erro")
	}
	exigeSemCredenciais(t, err.Error())

	if err := v.ready(context.Background()); err == nil {
		t.Fatal("ready sobre um endereco que nao produz pedido devia dar erro")
	} else {
		exigeSemCredenciais(t, err.Error())
	}
}

// TestAOS333_ErroDeTransporteNaoEcoaOUtilizador cobre o ramo em que o endereço É analisável: o
// pedido chega a sair, o transporte falha, e o `*url.Error` do `net/http` traz `admin:***@`.
//
// O CONTROLO vive aqui e não no teste acima: com um endereço analisável há uma forma publicável
// para dar, e a mensagem TEM de a dar. Um `/readyz` que não diz onde o nó falhou a falar troca
// uma fuga por um diagnóstico inútil — e um redactor que apagasse tudo passaria
// `exigeSemCredenciais` por vacuidade.
func TestAOS333_ErroDeTransporteNaoEcoaOUtilizador(t *testing.T) {
	t.Parallel()
	// Porta 1 em loopback: analisável, e a ligação é recusada de imediato.
	v := newVaultKeyVault("http://admin:"+aos333F2Senha+"@127.0.0.1:1", "transit", "tok")

	_, _, err := v.do("GET", "/v1/transit/keys/x", nil)
	if err == nil {
		t.Fatal("um vault inalcancavel devia dar erro")
	}
	exigeSemCredenciais(t, err.Error())
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("a mensagem devia continuar a nomear o host e a porta: %s", err)
	}

	rerr := v.ready(context.Background())
	if rerr == nil {
		t.Fatal("ready sobre um vault inalcancavel devia dar erro")
	}
	exigeSemCredenciais(t, rerr.Error())
	if !strings.Contains(rerr.Error(), "127.0.0.1:1") {
		t.Errorf("a mensagem de ready devia continuar a nomear o host e a porta: %s", rerr)
	}
}
