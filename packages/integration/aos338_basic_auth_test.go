package integration_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aos-ref/integration"
)

// AOS-338 — BASIC-AUTH PARA O VERIFICADOR DE ATTESTATION, POR FICHEIRO MONTADO.
//
// O AOS-333 passou a recusar credenciais embutidas no URL, e isso fechou uma via que
// FUNCIONAVA: o `net/http` converte `req.URL.User` em `Authorization: Basic`, pelo que um
// verificador atrás de um reverse-proxy com basic-auth autenticava assim. A recusa mantém-se —
// uma credencial num URL de ambiente aparece na tabela de processos, no `inspect` do contentor e
// em qualquer erro que ecoe o endereço — e este é o caminho que a substitui.
//
// A INVARIANTE VIVE NO TIPO QUE A VIOLARIA. A exclusão mútua entre Bearer e Basic é imposta no
// construtor do adaptador e não no wiring do nó: é o adaptador que emitiria os dois cabeçalhos,
// e pô-la no wiring dispararia antes de a URL sequer ser validada.

const (
	aos338Utilizador = "svc-attestation"
	aos338Senha      = "s3nh4-do-proxy-que-nao-pode-vazar"
	aos338Par        = aos338Utilizador + ":" + aos338Senha
)

// TestAOS338_BasicProduzOCabecalhoCerto prova o header EFECTIVAMENTE enviado, não o campo
// guardado: liga um servidor e lê o que chegou. Um teste sobre a struct provaria a intenção.
func TestAOS338_BasicProduzOCabecalhoCerto(t *testing.T) {
	t.Parallel()
	recebido := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recebido <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_id":"ZGV2LTE="}`))
	}))
	defer srv.Close()

	v, err := integration.NewRemoteDeviceAttestationVerifier(integration.RemoteAttestationConfig{
		URL:       srv.URL,
		BasicAuth: aos338Par,
	})
	if err != nil {
		t.Fatalf("construir: %v", err)
	}
	if v.AuthScheme() != integration.AuthSchemeBasic {
		t.Errorf("AuthScheme() = %q, quer %q", v.AuthScheme(), integration.AuthSchemeBasic)
	}
	if _, err := v.VerifyDeviceAttestation(context.Background(), []byte("ao"), []byte("cd"), []byte("ch")); err != nil {
		t.Fatalf("verificar: %v", err)
	}

	got := <-recebido
	quer := "Basic " + base64.StdEncoding.EncodeToString([]byte(aos338Par))
	if got != quer {
		t.Fatalf("Authorization = %q, quer %q", got, quer)
	}
	// CONTROLO DE FORMA: o valor tem de ser base64 do par, não o par em claro. Um `Basic` com
	// a credencial literal seria aceite por alguns servidores permissivos e poria a senha em
	// texto claro em cada pedido.
	if strings.Contains(got, aos338Senha) {
		t.Errorf("a senha vai em CLARO no cabecalho: %q", got)
	}
}

// TestAOS338_OAlfabetoBase64EOPadrao fecha um buraco de GATE, não de código (achado A2 da
// revisão adversarial). O código estava certo; a prova é que não estava.
//
// A minha prova de mutação removia o `base64` por inteiro — o mutante grosseiro, apanhado pelo
// `Contains(got, senha)`. O realista é trocar `StdEncoding` por `URLEncoding`, e esse SOBREVIVIA:
// medido, o par-fixture codifica IDENTICAMENTE nos dois alfabetos, porque nenhum dos seus bytes
// mapeia para `+` ou `/`. Um `Basic` em base64url é recusado por servidores que sigam o RFC 7617.
//
// Os pares abaixo são escolhidos para DISTINGUIR os alfabetos, e o valor esperado é escrito à
// mão: um teste que recalcule com a mesma função que testa não prova nada.
func TestAOS338_OAlfabetoBase64EOPadrao(t *testing.T) {
	t.Parallel()
	casos := []struct{ par, quer string }{
		{"u:a?b>c", "Basic dTphP2I+Yw=="}, // base64url daria `dTphP2I-Yw==`
		{"u:~~~?", "Basic dTp+fn4/"},      // base64url daria `dTp-fn4_`
	}
	for _, c := range casos {
		recebido := make(chan string, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recebido <- r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"device_id":"ZGV2LTE="}`))
		}))
		v, err := integration.NewRemoteDeviceAttestationVerifier(integration.RemoteAttestationConfig{
			URL:       srv.URL,
			BasicAuth: c.par,
		})
		if err != nil {
			srv.Close()
			t.Fatalf("construir com %q: %v", c.par, err)
		}
		if _, err := v.VerifyDeviceAttestation(context.Background(), []byte("ao"), []byte("cd"), []byte("ch")); err != nil {
			srv.Close()
			t.Fatalf("verificar com %q: %v", c.par, err)
		}
		got := <-recebido
		srv.Close()
		if got != c.quer {
			t.Errorf("cabecalho de %q = %q, quer %q (alfabeto base64 PADRAO, nao url-safe)", c.par, got, c.quer)
		}
	}
}

// TestAOS338_ControlosNoBearerSaoRecusados fecha o outro buraco de gate do A2, e um defeito real
// de código (achado M1).
//
// A guarda estava invertida: o ramo BASIC — onde o `base64` já neutraliza qualquer byte — tinha
// cobertura, e o ramo BEARER — onde o valor vai CRU e é a única via de injecção — não tinha
// nenhuma. E o critério era `\r\n\x00`, mais estreito do que o do `net/http`, que recusa TODO o
// controlo excepto `\t`.
//
// O que isso produzia: boot verde, banner a declarar «autentica com Authorization: Bearer», e
// CADA verificação a falhar no envio — todas as pernas de aprovação negadas. Fail-late num gate.
func TestAOS338_ControlosNoBearerSaoRecusados(t *testing.T) {
	t.Parallel()
	for _, tok := range []string{"a\rb", "a\nb", "a\x00b", "a\vb", "a\fb", "a\x1bb", "a\x7fb"} {
		_, err := integration.NewRemoteDeviceAttestationVerifier(integration.RemoteAttestationConfig{
			URL:       "https://attest.interno/verify",
			AuthToken: tok,
		})
		if err == nil {
			t.Errorf("token com controlo %q devia abortar no ARRANQUE, nao falhar em cada verificacao", tok)
			continue
		}
		if !errors.Is(err, integration.ErrRemoteAttestationAuth) {
			t.Errorf("o erro tem de ser ATRIBUIVEL para %q: %v", tok, err)
		}
	}
	// CONTROLO: o `\t` é aceite pelo `net/http` num valor de cabeçalho, e recusá-lo seria
	// endurecer para lá do critério — o que partiria credenciais legítimas.
	if _, err := integration.NewRemoteDeviceAttestationVerifier(integration.RemoteAttestationConfig{
		URL:       "https://attest.interno/verify",
		AuthToken: "a\tb",
	}); err != nil {
		t.Errorf("o TAB e aceite num cabecalho HTTP; recusa-lo endurece para la do criterio: %v", err)
	}
}

// TestAOS338_TokenComEsquemaColadoERecusado — um ficheiro que leve `Bearer <tok>` em vez de
// `<tok>` produziria `Authorization: Bearer Bearer <tok>`. Recusa-se em vez de se normalizar:
// normalizar esconderia o erro de montagem e o operador nunca o corrigiria (achado B3).
func TestAOS338_TokenComEsquemaColadoERecusado(t *testing.T) {
	t.Parallel()
	for _, tok := range []string{"Bearer tok", "bearer tok", "Basic dTpw"} {
		_, err := integration.NewRemoteDeviceAttestationVerifier(integration.RemoteAttestationConfig{
			URL:       "https://attest.interno/verify",
			AuthToken: tok,
		})
		if !errors.Is(err, integration.ErrRemoteAttestationAuth) {
			t.Errorf("token %q ja traz o esquema colado e devia ser recusado: %v", tok, err)
		}
	}
}

// TestAOS338_CredencialAcimaDoTectoERecusada (achado B2): um `Authorization` de megabytes é
// recusado por qualquer servidor, e o nó arrancaria verde a negar todas as pernas.
func TestAOS338_CredencialAcimaDoTectoERecusada(t *testing.T) {
	t.Parallel()
	gigante := "u:" + strings.Repeat("x", 8192)
	_, err := integration.NewRemoteDeviceAttestationVerifier(integration.RemoteAttestationConfig{
		URL:       "https://attest.interno/verify",
		BasicAuth: gigante,
	})
	if !errors.Is(err, integration.ErrRemoteAttestationAuth) {
		t.Errorf("uma credencial gigante devia ser recusada no arranque: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "xxxx") {
		t.Errorf("a mensagem ecoa a credencial: %v", err)
	}
}

// TestAOS338_EspacosNaoDesarmamAExclusaoMutua fecha o achado M2. O `TrimSpace` corria ANTES do
// teste de conflito, pelo que um dos lados só com espaços desaparecia e o outro era escolhido em
// SILÊNCIO — com os dois definidos, que é o que o critério de aceitação proíbe.
func TestAOS338_EspacosNaoDesarmamAExclusaoMutua(t *testing.T) {
	t.Parallel()
	casos := []struct{ nome, bearer, basic string }{
		{"bearer so com espacos", "   ", "u:p"},
		{"basic so com tabs", "tok", "\t\t"},
		{"ambos reais", "tok", "u:p"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Parallel()
			_, err := integration.NewRemoteDeviceAttestationVerifier(integration.RemoteAttestationConfig{
				URL:       "https://attest.interno/verify",
				AuthToken: c.bearer,
				BasicAuth: c.basic,
			})
			if !errors.Is(err, integration.ErrRemoteAttestationAuth) {
				t.Errorf("dois esquemas DEFINIDOS tem de abortar, mesmo que um seja so espacos: %v", err)
			}
		})
	}
}

// TestAOS338_BearerContinuaAFuncionar é o CONTROLO de não-regressão: o esquema que já existia
// não pode ter-se partido ao acrescentar o segundo. Sem ele, uma implementação que só soubesse
// Basic passaria o teste acima.
func TestAOS338_BearerContinuaAFuncionar(t *testing.T) {
	t.Parallel()
	recebido := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recebido <- r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"device_id":"ZGV2LTE="}`))
	}))
	defer srv.Close()

	v, err := integration.NewRemoteDeviceAttestationVerifier(integration.RemoteAttestationConfig{
		URL:       srv.URL,
		AuthToken: "token-opaco",
	})
	if err != nil {
		t.Fatalf("construir: %v", err)
	}
	if v.AuthScheme() != integration.AuthSchemeBearer {
		t.Errorf("AuthScheme() = %q, quer %q", v.AuthScheme(), integration.AuthSchemeBearer)
	}
	if _, err := v.VerifyDeviceAttestation(context.Background(), []byte("ao"), []byte("cd"), []byte("ch")); err != nil {
		t.Fatalf("verificar: %v", err)
	}
	if got := <-recebido; got != "Bearer token-opaco" {
		t.Fatalf("Authorization = %q, quer Bearer token-opaco", got)
	}
}

// TestAOS338_SemCredencialNaoEnviaCabecalho é o outro CONTROLO: falar sem autenticação é uma
// composição legítima (mTLS, rede fechada), e transformá-la em erro partiria os deployments que
// existem hoje. Um cabeçalho `Authorization` vazio seria pior do que nenhum.
func TestAOS338_SemCredencialNaoEnviaCabecalho(t *testing.T) {
	t.Parallel()
	recebido := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recebido <- r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"device_id":"ZGV2LTE="}`))
	}))
	defer srv.Close()

	v, err := integration.NewRemoteDeviceAttestationVerifier(integration.RemoteAttestationConfig{URL: srv.URL})
	if err != nil {
		t.Fatalf("construir: %v", err)
	}
	if v.AuthScheme() != "" {
		t.Errorf("AuthScheme() = %q, quer vazio", v.AuthScheme())
	}
	if _, err := v.VerifyDeviceAttestation(context.Background(), []byte("ao"), []byte("cd"), []byte("ch")); err != nil {
		t.Fatalf("verificar: %v", err)
	}
	if got := <-recebido; got != "" {
		t.Fatalf("nao devia ir cabecalho Authorization nenhum, foi %q", got)
	}
}

// TestAOS338_OsDoisEsquemasAbortam é a AC que decide. Um pedido HTTP tem UM cabeçalho
// `Authorization`: com os dois definidos, um seria descartado — e o operador ficaria a crer que
// a credencial descartada estava em uso. Um segredo que se julga activo e não está é pior do que
// nenhum, porque ninguém o vai rodar nem revogar.
func TestAOS338_OsDoisEsquemasAbortam(t *testing.T) {
	t.Parallel()
	_, err := integration.NewRemoteDeviceAttestationVerifier(integration.RemoteAttestationConfig{
		URL:       "https://attest.interno/verify",
		AuthToken: "token-opaco",
		BasicAuth: aos338Par,
	})
	if err == nil {
		t.Fatal("os dois esquemas definidos tinham de ABORTAR a construcao")
	}
	if !errors.Is(err, integration.ErrRemoteAttestationAuth) {
		t.Errorf("o erro tem de ser ATRIBUIVEL: %v", err)
	}
	exigeSemCredencial(t, err.Error())
}

// TestAOS338_ParMalformadoAborta fixa o formato declarado — `utilizador:senha`, molde do `-u` do
// curl — e o fail-closed de cada desvio.
func TestAOS338_ParMalformadoAborta(t *testing.T) {
	t.Parallel()
	casos := []struct{ nome, par, porque string }{
		{"sem separador", aos338Utilizador + aos338Senha, "sem ':' nao ha como separar utilizador de senha"},
		{"sem utilizador", ":" + aos338Senha, "um Basic sem utilizador nao identifica principal nenhum"},
		{"com CR", aos338Utilizador + ":a\rb", "injeccao de cabecalho"},
		{"com LF", aos338Utilizador + ":a\nb", "injeccao de cabecalho"},
		{"com NUL", aos338Utilizador + ":a\x00b", "injeccao de cabecalho"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Parallel()
			_, err := integration.NewRemoteDeviceAttestationVerifier(integration.RemoteAttestationConfig{
				URL:       "https://attest.interno/verify",
				BasicAuth: c.par,
			})
			if err == nil {
				t.Fatalf("devia abortar: %s", c.porque)
			}
			if !errors.Is(err, integration.ErrRemoteAttestationAuth) {
				t.Errorf("o erro tem de ser ATRIBUIVEL: %v", err)
			}
			exigeSemCredencial(t, err.Error())
		})
	}
}

// TestAOS338_SenhaComDoisPontosEAceite — o `-u` do curl deixa a senha conter `:`; só o primeiro
// separa. Sem este caso, uma implementação que partisse por TODOS os `:` passaria os testes de
// recusa e rejeitaria senhas geradas legítimas.
func TestAOS338_SenhaComDoisPontosEAceite(t *testing.T) {
	t.Parallel()
	const par = aos338Utilizador + ":aa:bb:cc"
	v, err := integration.NewRemoteDeviceAttestationVerifier(integration.RemoteAttestationConfig{
		URL:       "https://attest.interno/verify",
		BasicAuth: par,
	})
	if err != nil {
		t.Fatalf("uma senha com ':' e legitima: %v", err)
	}
	if v.AuthScheme() != integration.AuthSchemeBasic {
		t.Errorf("AuthScheme() = %q", v.AuthScheme())
	}
}

// TestAOS338_OTransporteNaoRelaxa — o Basic não compra excepção ao critério de transporte. Basic
// sobre claro é a credencial em claro, que é o mesmo eixo do AOS-249.
func TestAOS338_OTransporteNaoRelaxa(t *testing.T) {
	t.Parallel()
	_, err := integration.NewRemoteDeviceAttestationVerifier(integration.RemoteAttestationConfig{
		URL:       "http://attest.interno/verify", // http fora de loopback
		BasicAuth: aos338Par,
	})
	if err == nil {
		t.Fatal("http fora de loopback tinha de ser recusado, com ou sem basic")
	}
	if !errors.Is(err, integration.ErrRemoteAttestationURL) {
		t.Errorf("a recusa tem de ser a do TRANSPORTE, nao a da autenticacao: %v", err)
	}
	exigeSemCredencial(t, err.Error())

	// CONTROLO: loopback continua a ser aceite, senão o critério teria endurecido de lado.
	if _, err := integration.NewRemoteDeviceAttestationVerifier(integration.RemoteAttestationConfig{
		URL:       "http://127.0.0.1:8090/verify",
		BasicAuth: aos338Par,
	}); err != nil {
		t.Errorf("loopback continua legitimo: %v", err)
	}
}

// TestAOS338_AOrdemDaRecusaEOTransportePrimeiro — um endereço inaceitável não deve produzir uma
// queixa sobre a credencial. É a ordem dos dois Vaults, e é a que faz sentido para quem lê o
// aborto: corrige-se o que está errado primeiro.
func TestAOS338_AOrdemDaRecusaEOTransportePrimeiro(t *testing.T) {
	t.Parallel()
	_, err := integration.NewRemoteDeviceAttestationVerifier(integration.RemoteAttestationConfig{
		URL:       "http://attest.interno/verify", // mau
		AuthToken: "token-opaco",                  // e os dois esquemas, tambem mau
		BasicAuth: aos338Par,
	})
	if !errors.Is(err, integration.ErrRemoteAttestationURL) {
		t.Fatalf("com transporte E autenticacao maus, a queixa tem de ser do TRANSPORTE: %v", err)
	}
}

// exigeSemCredencial: nem a senha, nem o utilizador. Numa basic-auth o utilizador identifica o
// principal e a senha vem colada a ele — é o critério que o AOS-333 fixou.
func exigeSemCredencial(t *testing.T, msg string) {
	t.Helper()
	if strings.Contains(msg, aos338Senha) {
		t.Errorf("a mensagem contem a SENHA: %s", msg)
	}
	if strings.Contains(msg, aos338Utilizador) {
		t.Errorf("a mensagem contem o UTILIZADOR, que identifica o principal: %s", msg)
	}
}
