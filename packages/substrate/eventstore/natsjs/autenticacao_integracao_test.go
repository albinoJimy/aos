package natsjs_test

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// envNKey é a seed do cluster autorizado que scripts/ci/nats-cluster.sh levanta (AOS-470).
// Vazia ⇒ cluster anónimo (AOS_NATS_AUTH=0), e a ligação vai sem credencial.
const envNKey = "AOS_NATS_NKEY_FILE"

// credencial devolve a nkey do cluster de teste, ou nil se ele não exigir autenticação.
func credencial(t *testing.T) *natsjs.NKey {
	t.Helper()
	p := os.Getenv(envNKey)
	if p == "" {
		return nil
	}
	k, err := natsjs.LerNKeyFicheiro(p)
	if err != nil {
		t.Fatalf("%s: %v", envNKey, err)
	}
	return k
}

// clusterAutorizado salta quando o cluster não exige autenticação: as provas abaixo medem a
// RECUSA do servidor, e contra um servidor sem `authorization` passariam a medir a ausência
// dela. No gate `nats` o cluster é sempre autorizado, pelo que ali não saltam.
func clusterAutorizado(t *testing.T) (string, *natsjs.NKey) {
	t.Helper()
	addr := servidor(t)
	k := credencial(t)
	if k == nil {
		t.Skipf("cluster sem authorization (%s vazio) — a recusa do servidor não é observável", envNKey)
	}
	return addr, k
}

// TestIntegracao_ServidorRecusaCONNECTAnonimo é o critério de conclusão do AOS-470, medido NO
// FIO e não no cliente: um CONNECT sem credencial — o que o natsjs enviava antes deste ticket,
// byte a byte — é recusado PELO SERVIDOR com «Authorization Violation». Medir só pelo cliente
// provaria a recusa do cliente, que é a outra metade.
func TestIntegracao_ServidorRecusaCONNECTAnonimo(t *testing.T) {
	addr, _ := clusterAutorizado(t)
	c, err := net.DialTimeout("tcp", addr, prazo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(prazo))
	br := bufio.NewReader(c)
	info, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(info, `"auth_required":true`) || !strings.Contains(info, `"nonce"`) {
		t.Fatalf("o servidor não anuncia autenticação por nkey: %s", info)
	}
	// O CONNECT anterior ao AOS-470, literal — e uma escrita a seguir, que é o que um atacante
	// com acesso à porta faria.
	if _, err := io.WriteString(c, `CONNECT {"verbose":false,"pedantic":false,"tls_required":false,"headers":true,"no_responders":true,"name":"aos-eventstore","lang":"go","version":"stdlib"}`+"\r\n"+
		"PUB aos.anonimo 1\r\nx\r\nPING\r\n"); err != nil {
		t.Fatal(err)
	}
	resposta, _ := io.ReadAll(br)
	if !strings.Contains(string(resposta), "Authorization Violation") {
		t.Fatalf("CONNECT anónimo não foi recusado pelo servidor; respondeu %q", resposta)
	}
	if strings.Contains(string(resposta), "PONG") {
		t.Fatalf("o servidor processou comandos de uma sessão anónima: %q", resposta)
	}
}

// TestIntegracao_ClienteSemCredencialEChaveDesconhecidaSaoRecusados: as duas formas de não ter
// a identidade certa dão ErrAutenticacao no Connect, e não uma ligação «aceite» que morre depois.
func TestIntegracao_ClienteSemCredencialEChaveDesconhecidaSaoRecusados(t *testing.T) {
	addr, _ := clusterAutorizado(t)

	if _, err := natsjs.Connect(addr, prazo); !errors.Is(err, natsjs.ErrAutenticacao) {
		t.Fatalf("sem credencial: err = %v, quer ErrAutenticacao", err)
	}

	// Uma nkey bem formada que NÃO está na authorization: aqui a recusa é do SERVIDOR, que
	// verifica a assinatura e não encontra a chave pública.
	seed, _, err := natsjs.GerarNKeyUtilizador(nil)
	if err != nil {
		t.Fatal(err)
	}
	estranha, err := natsjs.ParseNKeySeed([]byte(seed))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := natsjs.ConnectServersCom([]string{addr}, prazo, estranha); !errors.Is(err, natsjs.ErrAutenticacao) {
		t.Fatalf("chave desconhecida: err = %v, quer ErrAutenticacao", err)
	}
}

// TestIntegracao_ComCredencialLigaEEscreve: a outra metade — a seed declarada abre a sessão e
// o JetStream responde. É também a prova de que a codificação nkey e a assinatura do nonce de
// natsjs/nkey.go são as da NATS: o servidor verificou-as.
func TestIntegracao_ComCredencialLigaEEscreve(t *testing.T) {
	addr, k := clusterAutorizado(t)
	cn, err := natsjs.ConnectServersCom([]string{addr}, prazo, k)
	if err != nil {
		t.Fatalf("com credencial: %v", err)
	}
	defer func() { _ = cn.Close() }()
	if _, err := cn.Request("$JS.API.INFO", nil, nil, prazo); err != nil {
		t.Fatalf("JetStream não respondeu a uma sessão autenticada: %v", err)
	}
}
