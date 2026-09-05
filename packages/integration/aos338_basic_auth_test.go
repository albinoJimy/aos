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
