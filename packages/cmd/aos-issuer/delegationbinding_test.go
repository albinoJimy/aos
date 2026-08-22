package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
	"time"

	oidc "github.com/aos-ref/integration/oidc"
)

// signIDTokenComNonce é o signIDToken do arnês existente, com o claim `nonce` — que é a peça que
// liga a autenticação do humano a uma delegação concreta.
func signIDTokenComNonce(t *testing.T, idp *testIDP, sub, nonce string) string {
	t.Helper()
	now := idpClock()().Unix()
	claims := map[string]any{
		"iss": idpIssuer, "sub": sub, "aud": idpAudience,
		"exp": now + 3600, "iat": now - 30, "nbf": now - 30,
	}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	hdr, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": idpKid})
	pb, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(pb)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, idp.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("assinar id-token: %v", err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// TestAssercaoLigadaADelegacao é o CONTROLO que torna a ligação real em vez de decorativa.
//
// Sem o caso (2) este mecanismo seria vacuoso: um token que passa é compatível com "o verificador
// aceita tudo". A propriedade só fica provada quando um token EMITIDO PARA OUTRA DELEGAÇÃO, com
// assinatura igualmente válida e o mesmo humano, é RECUSADO. É a diferença entre o humano ter-se
// autenticado e o humano ter autorizado ISTO.
func TestAssercaoLigadaADelegacao(t *testing.T) {
	idp := newTestIDP(t)
	cfgCom := func(nonce string) oidc.Config {
		return oidc.Config{
			Issuer: idpIssuer, Audience: idpAudience, JWKSURI: idp.jwksURI(),
			HTTPClient: idp.server.Client(), Clock: idpClock(), Nonce: nonce,
		}
	}
	// A delegação que o humano tem à frente quando se autentica.
	esperado := delegationNonce("agt-leitor", "agent-worker", []string{"cap:fs.read"}, 45*time.Minute)

	// (1) o humano autenticou-se PARA ESTA delegação ⇒ aceite.
	tokBom := signIDTokenComNonce(t, idp, "jimy-uuid", esperado)
	human, _, err := authenticateOIDC(context.Background(), cfgCom(esperado), tokBom)
	if err != nil {
		t.Fatalf("token ligado a esta delegação devia ser aceite: %v", err)
	}
	if human != "human:jimy-uuid" {
		t.Fatalf("humano = %q, quero human:jimy-uuid (o sub verificado)", human)
	}

	// (2) CONTROLO: token do MESMO humano, assinatura igualmente válida, mas emitido para OUTRA
	// delegação (mais poder: ganha cap:fs.write). Tem de ser RECUSADO — é a escalada que a
	// ligação existe para impedir.
	outra := delegationNonce("agt-leitor", "agent-worker", []string{"cap:fs.read", "cap:fs.write"}, 45*time.Minute)
	tokOutro := signIDTokenComNonce(t, idp, "jimy-uuid", outra)
	if h, _, err := authenticateOIDC(context.Background(), cfgCom(esperado), tokOutro); err == nil {
		t.Fatalf("token de OUTRA delegação foi aceite (humano=%q) — a ligação não liga nada", h)
	}

	// (3) CONTROLO: token SEM nonce nenhum (ex.: password grant) com ligação exigida ⇒ recusado.
	// Sem isto, bastava um IdP que não ecoasse o nonce para a exigência evaporar em silêncio.
	tokSemNonce := signIDTokenComNonce(t, idp, "jimy-uuid", "")
	if _, _, err := authenticateOIDC(context.Background(), cfgCom(esperado), tokSemNonce); err == nil {
		t.Fatal("token SEM nonce foi aceite com ligação exigida")
	}

	// (4) o mesmo token sem nonce PASSA quando a ligação não é exigida — é o caminho
	// --assertion-unbound, que existe e fica declarado no rótulo.
	if _, m, err := authenticateOIDC(context.Background(), cfgCom(""), tokSemNonce); err != nil {
		t.Fatalf("sem ligação exigida o token devia passar: %v", err)
	} else if want := "oidc:" + idpIssuer; m != want {
		t.Fatalf("rótulo não-ligado = %q, quero %q", m, want)
	}
}

// tectoAssercaoDefensavel é o limite superior ARGUMENTADO do tecto de idade, e é uma constante DO
// TESTE — nunca derivada de [assertionMaxAge], que é o que se quer vigiar.
//
// Acima disto o tecto deixa de limitar o que quer que seja: a janela em que um ID-token humano
// roubado ainda serve para mintar uma delegação passa a ser da ordem da própria vida do token.
const tectoAssercaoDefensavel = 15 * time.Minute

// TestAssercaoTemTectoDeIdade — o mint impunha MaxAge 0 (nenhum), o que o tornava o mais fraco
// dos três verificadores OIDC do sistema. Um ID-token antigo mas ainda não expirado tem de ser
// recusado.
//
// A VERSÃO ANTERIOR DESTE TESTE NÃO PODIA FALHAR, e a varredura adversarial de 2026-08-21
// demonstrou-o: `assertionMaxAge` mutado para DEZ ANOS ficava tudo verde. Duas razões, e ambas
// se repetem noutros sítios:
//
//  1. AUTO-REFERÊNCIA DE PARÂMETRO — o relógio de teste era `idpClock() + assertionMaxAge + 10min`,
//     ou seja, construído a partir da própria constante sob teste. Movia-se com ela, e por isso
//     o token estava sempre «para lá do tecto», fosse o tecto qual fosse;
//  2. `assertionMaxAge <= 0` NÃO É A PROPRIEDADE — dez anos é maior que zero e é indistinguível
//     de não haver tecto.
//
// Fica com um limite de valor ABSOLUTO e desvios de relógio ABSOLUTOS, que enquadram a constante
// pelos dois lados sem nunca a consultar.
func TestAssercaoTemTectoDeIdade(t *testing.T) {
	if assertionMaxAge <= 0 {
		t.Fatal("assertionMaxAge tem de ser > 0 — sem tecto, a janela de replay é o exp inteiro")
	}
	if assertionMaxAge > tectoAssercaoDefensavel {
		t.Fatalf("assertionMaxAge = %v, acima do tecto defensável de %v — um tecto desta ordem nao "+
			"limita nada: a assercao roubada serve durante quase toda a vida do exp",
			assertionMaxAge, tectoAssercaoDefensavel)
	}

	idp := newTestIDP(t)
	tok := signIDTokenComNonce(t, idp, "jimy-uuid", "")
	cfgAos := func(desvio time.Duration) oidc.Config {
		return oidc.Config{
			Issuer: idpIssuer, Audience: idpAudience, JWKSURI: idp.jwksURI(),
			HTTPClient: idp.server.Client(), MaxAge: assertionMaxAge,
			Clock: func() time.Time { return idpClock()().Add(desvio) },
		}
	}

	// FORA: 20 minutos depois, com o token ainda MUITO dentro do exp (que é de uma hora). O 20 é
	// absoluto e não sabe quanto vale o tecto.
	if _, _, err := authenticateOIDC(context.Background(), cfgAos(20*time.Minute), tok); err == nil {
		t.Fatal("um ID-token com 20 minutos foi ACEITE — o tecto de idade nao esta a limitar nada")
	}
	// DENTRO: 4 minutos depois passa. Este ramo enquadra por BAIXO — sem ele, um tecto reduzido a
	// um segundo (ou um verificador que recusasse tudo por outra razão: JWKS em baixo, assinatura
	// má) faria o ramo acima passar sem que fosse a IDADE a decidir.
	if _, _, err := authenticateOIDC(context.Background(), cfgAos(4*time.Minute), tok); err != nil {
		t.Fatalf("o mesmo token com 4 minutos devia passar: %v", err)
	}
}

// TestOMintLIGAOTectoDeIdade é o teste de CABLAGEM, e é a décima vez que este padrão aparece no
// repositório: a constante pode estar certa e não ser passada a ninguém.
//
// O `oidc.Config` de produção é um literal INLINE dentro do fluxo do `main.go`, inalcançável por
// teste sem um refactor daquele caminho. Lê-se a FONTE, no mesmo idioma — e pela mesma razão — de
// `cli_subcomandos_test.go` no nó: qualquer verificação que partilhasse a convenção que quer
// vigiar não a vigiaria.
//
// RESIDUAL DECLARADO: isto prova que a linha ESTÁ ESCRITA, não que corre. É estritamente menos do
// que um teste de integração daquele caminho, e estritamente mais do que nada — que era o que
// havia.
func TestOMintLIGAOTectoDeIdade(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse de main.go: %v", err)
	}
	var achouConfig, achouMaxAge bool
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := cl.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Config" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "oidc" {
			return true
		}
		achouConfig = true
		for _, el := range cl.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "MaxAge" {
				continue
			}
			if v, ok := kv.Value.(*ast.Ident); ok && v.Name == "assertionMaxAge" {
				achouMaxAge = true
			}
		}
		return true
	})
	// CONTROLO DO PRÓPRIO TESTE: se o parser não encontrar o literal, a asserção seguinte seria
	// vacuosa — e vacuosa a acusar, que é a pior forma.
	if !achouConfig {
		t.Fatal("o parser nao encontrou nenhum oidc.Config em main.go — a leitura da fonte falhou " +
			"e a asercao seguinte nao significaria nada")
	}
	if !achouMaxAge {
		t.Error("o oidc.Config do mint NAO passa MaxAge: assertionMaxAge — a constante existe, esta " +
			"certa, e nao chega ao verificador. O tecto e decorativo")
	}
}

// TestDelegationNonceDistingueParametros — o digest TEM de mudar quando muda qualquer coisa que o
// humano estaria a autorizar. Um digest que não distinguisse era pior do que nenhum: daria a
// aparência de ligação sem a propriedade, que é exactamente o modo de falha que estas ligações
// existem para evitar.
func TestDelegationNonceDistingueParametros(t *testing.T) {
	base := delegationNonce("agt-1", "agent-worker", []string{"cap:fs.read"}, 15*time.Minute)

	casos := map[string]string{
		"outro agente":      delegationNonce("agt-2", "agent-worker", []string{"cap:fs.read"}, 15*time.Minute),
		"outra classe":      delegationNonce("agt-1", "agent-admin", []string{"cap:fs.read"}, 15*time.Minute),
		"capability a mais": delegationNonce("agt-1", "agent-worker", []string{"cap:fs.read", "cap:fs.write"}, 15*time.Minute),
		"outra capability":  delegationNonce("agt-1", "agent-worker", []string{"cap:fs.write"}, 15*time.Minute),
		"TTL maior":         delegationNonce("agt-1", "agent-worker", []string{"cap:fs.read"}, 60*time.Minute),
	}
	for nome, n := range casos {
		if n == base {
			t.Errorf("%s: digest IGUAL ao base — a delegação não fica ligada", nome)
		}
	}
}

// TestDelegationNonceEstavelNaOrdemDasCaps — o humano autoriza um CONJUNTO de poderes. Reordenar
// a flag não é uma autorização diferente, e um mismatch aqui pareceria um ataque sendo um
// acidente de escrita.
func TestDelegationNonceEstavelNaOrdemDasCaps(t *testing.T) {
	a := delegationNonce("agt-1", "c", []string{"cap:b", "cap:a"}, time.Minute)
	b := delegationNonce("agt-1", "c", []string{"cap:a", "cap:b"}, time.Minute)
	if a != b {
		t.Fatalf("a ordem das capabilities mudou o digest: %s != %s", a, b)
	}
	// Espaços e entradas vazias são ruído de escrita, não poder.
	if c := delegationNonce("agt-1", "c", []string{" cap:a ", "", "cap:b"}, time.Minute); c != b {
		t.Fatalf("normalização falhou: %s != %s", c, b)
	}
}

// TestDelegationNonceResisteADeslizamentoDeFronteira — é a razão de ser do length-prefix. Com um
// separador simples, (agent="a", class="bc") e (agent="ab", class="c") produziriam os MESMOS
// bytes, e quem controlasse um campo deslizava a fronteira para o seguinte: obtinha um token que
// o humano autorizou para uma delegação e usava-o noutra.
func TestDelegationNonceResisteADeslizamentoDeFronteira(t *testing.T) {
	if delegationNonce("a", "bc", nil, 0) == delegationNonce("ab", "c", nil, 0) {
		t.Fatal("colisão por deslizamento de fronteira entre agent e class")
	}
	// O mesmo entre a última capability e o campo seguinte.
	if delegationNonce("x", "y", []string{"a", "b"}, 0) == delegationNonce("x", "y", []string{"ab"}, 0) {
		t.Fatal("colisão por deslizamento de fronteira entre capabilities")
	}
}

// TestIdPHTTPClientFailClosed — um --oidc-ca que não é PEM tem de ABORTAR, não cair em silêncio
// para a trust store do sistema. Degradar aqui seria trocar a CA que o operador nomeou por outra
// qualquer, sem o dizer.
func TestIdPHTTPClientFailClosed(t *testing.T) {
	if hc, err := idpHTTPClient(""); err != nil || hc != nil {
		t.Fatalf("caPath vazio ⇒ (nil,nil) para o default endurecido; obtive (%v,%v)", hc, err)
	}
	lixo := filepath.Join(t.TempDir(), "nao-e-pem.crt")
	if err := os.WriteFile(lixo, []byte("isto nao e um certificado\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := idpHTTPClient(lixo); err == nil {
		t.Fatal("um ficheiro sem PEM foi ACEITE — degradaria para a trust store do sistema em silêncio")
	}
	if _, err := idpHTTPClient(filepath.Join(t.TempDir(), "nao-existe.crt")); err == nil {
		t.Fatal("um caminho inexistente foi ACEITE")
	}
}
