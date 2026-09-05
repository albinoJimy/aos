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
		// F4 da revisão adversarial: o ramo SEM esquema não tinha caso nenhum, e devolvia
		// `vault:8200` — uma forma que o doc-comment não promete e que num banner se lê
		// como um host solto em vez de um endereço.
		{"sem esquema mantém-se re-analisável", "//admin:" + senha + "@vault.interno:8200", "//vault.interno:8200"},
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

// TestAOS333_EspacosNaoTornamOURLMalformado fecha o F5 da revisão adversarial.
//
// `CheckSecureTransportURL` só fazia `TrimSpace` no teste de vazio, e `RedactURL` fazia-o antes do
// `Parse`. A mesma entrada era, portanto, «malformada» numa e publicável na outra — e como o ramo
// de parse falhado não pode ecoar o valor, o operador via o arranque abortar com «malformada
// (valor omitido)» sem nada que lhe dissesse que o problema era um espaço num `.env`.
func TestAOS333_EspacosNaoTornamOURLMalformado(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"https://vault.interno:8200 ",
		" https://vault.interno:8200",
		"\thttps://vault.interno:8200\n",
	} {
		if err := integration.CheckSecureTransportURL(raw); err != nil {
			t.Errorf("URL legítimo com espaços recusado: %q -> %v", raw, err)
		}
	}
	// CONTROLO: aparar espaços NÃO pode aparar o critério. Um URL com credenciais e um
	// espaço continua a ser recusado, e a recusa continua a não ecoar a senha.
	const senha = "s3nh4-com-espacos"
	err := integration.CheckSecureTransportURL(" https://admin:" + senha + "@vault.interno:8200 ")
	if err == nil {
		t.Fatal("aparar espaços nao pode fazer passar um URL com credenciais")
	}
	if strings.Contains(err.Error(), senha) || strings.Contains(err.Error(), "admin") {
		t.Errorf("a recusa vaza: %v", err)
	}
}

// TestAOS333_MensagemNomeiaOsDoisRemedios fecha o F1 da revisão adversarial.
//
// A recusa de user-info É uma quebra: o `net/http` converte `req.URL.User` em
// `Authorization: Basic`, pelo que um verificador de attestation atrás de um proxy com basic-auth
// funcionava assim. A primeira mensagem mandava «passe-as pelo ficheiro de token» — que dá
// `Bearer`, não `Basic`. Seguir a instrução não repunha esse deployment.
//
// Um operador cujo arranque aborta só tem esta linha. Ela tem de nomear os caminhos que existem.
//
// ACTUALIZADO EM AOS-338. Quando este teste foi escrito havia dois remédios: o ficheiro de token
// (Bearer) e terminar a autenticação no proxy. Passou a haver três — o AOS-338 deu à basic-auth
// um caminho próprio por ficheiro montado, `AOS_ATTESTATION_VERIFIER_BASIC_PATH` —, e uma
// mensagem que continuasse a oferecer só dois mandaria pelo proxy quem já não precisa de lá ir.
//
// A mensagem é PARTILHADA pelos três chamadores, e o Basic só se aplica a um: os dois Vaults
// autenticam por `X-Vault-Token` e nunca usaram basic-auth. Por isso a mensagem NOMEIA o
// chamador a que o caminho novo pertence, em vez de o oferecer a todos.
func TestAOS333_MensagemNomeiaOsDoisRemedios(t *testing.T) {
	t.Parallel()
	err := integration.CheckSecureTransportURL("https://admin:pw@vault.interno:8200")
	if err == nil {
		t.Fatal("devia recusar")
	}
	msg := err.Error()
	for _, quer := range []string{"FICHEIRO MONTADO", "Bearer", "Basic", "AOS_ATTESTATION_VERIFIER_BASIC_PATH", "attestation", "proxy"} {
		if !strings.Contains(msg, quer) {
			t.Errorf("a mensagem devia nomear %q — sem isso o operador nao tem por onde migrar: %v", quer, err)
		}
	}
}
