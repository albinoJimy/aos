package main

// AOS-413 (ADR-027) — o cliente do nó: duas credenciais que não se confundem, um Bearer NOVO por
// chamada (o nó aceita cada `jti` uma só vez), a submissão idempotente e o 404 distinguido.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Nomes das variáveis de ambiente do executor (o código de produção usa-os em literal, que é o
// que o gate da superfície de ambiente lê).
const (
	envNodeURL            = "AOS_ORQ_NODE_URL"
	envNodeCredentialFile = "AOS_ORQ_NODE_CREDENTIAL_FILE"
	envOIDCTokenURL       = "AOS_ORQ_OIDC_TOKEN_URL"
	envOIDCClientID       = "AOS_ORQ_OIDC_CLIENT_ID"
	envOIDCSecretFile     = "AOS_ORQ_OIDC_CLIENT_SECRET_FILE"
)

type aos413NoFalso struct {
	mu        sync.Mutex
	tokens    []string // Authorization recebidos, por ordem
	emitidos  int
	submissao map[string]any
	codigo    int // resposta ao POST /runs
}

func (f *aos413NoFalso) servidor(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != "aos-orq" || r.Form.Get("client_secret") != "segredo-do-cliente" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.emitidos++
		n := f.emitidos
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": fmt.Sprintf("tok-%d", n)})
	})
	mux.HandleFunc("POST /runs", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.tokens = append(f.tokens, r.Header.Get("Authorization"))
		_ = json.NewDecoder(r.Body).Decode(&f.submissao)
		codigo := f.codigo
		f.mu.Unlock()
		w.WriteHeader(codigo)
	})
	mux.HandleFunc("GET /runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.tokens = append(f.tokens, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if r.PathValue("id") != "run-413.n1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"run_id": "run-413.n1", "status": "completed", "terminated": true, "final_text": "feito",
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func aos413ClienteDoAmbiente(t *testing.T, base string) *nodeClient {
	t.Helper()
	dir := t.TempDir()
	cred := filepath.Join(dir, "nhi.jwt")
	escrever(t, cred, "nhi-do-operador\n")
	segredo := filepath.Join(dir, "client-secret")
	escrever(t, segredo, "segredo-do-cliente\n")
	t.Setenv("AOS_MODE", "")
	t.Setenv(envNodeURL, base)
	t.Setenv(envNodeCredentialFile, cred)
	t.Setenv(envOIDCTokenURL, base+"/token")
	t.Setenv(envOIDCClientID, "aos-orq")
	t.Setenv(envOIDCSecretFile, segredo)
	c, err := nodeClientDoAmbiente()
	if err != nil || c == nil {
		t.Fatalf("nodeClientDoAmbiente: c=%v err=%v", c, err)
	}
	return c
}

func TestAOS413_ClienteDoNoSubmeteComAsDuasCredenciais(t *testing.T) {
	f := &aos413NoFalso{codigo: http.StatusCreated}
	srv := f.servidor(t)
	c := aos413ClienteDoAmbiente(t, srv.URL)
	ctx := context.Background()

	if err := c.Submit(ctx, pedidoDeRun{RunID: "run-413.n1", Objective: "ler", Tools: []string{"fs.read"}}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if f.submissao["credential"] != "nhi-do-operador" {
		t.Fatalf("o NHI do ficheiro tinha de ir no corpo, foi %v", f.submissao["credential"])
	}
	if tools, _ := f.submissao["tools"].([]any); len(tools) != 1 || tools[0] != "fs.read" {
		t.Fatalf("a lista-branca do nó tinha de ir em tools, foi %v", f.submissao["tools"])
	}
	e, ok, err := c.Status(ctx, "run-413.n1")
	if err != nil || !ok || !e.concluiu() || e.FinalText != "feito" {
		t.Fatalf("Status: e=%+v ok=%v err=%v", e, ok, err)
	}
	// Um Bearer NOVO por chamada: o nó recusa um `jti` repetido.
	if len(f.tokens) != 2 || f.tokens[0] == f.tokens[1] || !strings.HasPrefix(f.tokens[0], "Bearer tok-") {
		t.Fatalf("cada chamada tinha de levar um Bearer novo do IdP: %v", f.tokens)
	}
}

func TestAOS413_ClienteDoNoSubmissaoIdempotenteE404(t *testing.T) {
	f := &aos413NoFalso{codigo: http.StatusConflict}
	srv := f.servidor(t)
	c := aos413ClienteDoAmbiente(t, srv.URL)
	ctx := context.Background()
	// 409: o nó já tem um run com este id, e não foi este plano que o criou.
	if err := c.Submit(ctx, pedidoDeRun{RunID: "run-413.n1", Objective: "ler"}); !errors.Is(err, errRunFilhoJaExiste) {
		t.Fatalf("409 tinha de ser errRunFilhoJaExiste, veio %v", err)
	}
	if _, ok, err := c.Status(ctx, "run-413.desconhecido"); err != nil || ok {
		t.Fatalf("404 tinha de dar (_, false, nil): ok=%v err=%v", ok, err)
	}
	f.codigo = http.StatusForbidden
	if err := c.Submit(ctx, pedidoDeRun{RunID: "run-413.n2", Objective: "ler"}); err == nil {
		t.Fatal("um 403 do nó tinha de ser erro")
	}
}

func TestAOS413_ClienteDoNoConfiguracaoFailClosed(t *testing.T) {
	dir := t.TempDir()
	cred := filepath.Join(dir, "nhi.jwt")
	escrever(t, cred, "nhi")
	casos := []struct {
		nome string
		env  map[string]string
	}{
		{"URL sem NHI", map[string]string{envNodeURL: "http://no:8080"}},
		{"produção por http fora da rede interna", map[string]string{"AOS_MODE": "production", envNodeURL: "http://aos.example.com:8080", envNodeCredentialFile: cred}},
		{"produção sem Bearer", map[string]string{"AOS_MODE": "production", envNodeURL: "https://no:8443", envNodeCredentialFile: cred}},
		{"Bearer incompleto", map[string]string{envNodeURL: "http://no:8080", envNodeCredentialFile: cred, envOIDCTokenURL: "http://idp/token"}},
		{"produção com token do IdP por http", map[string]string{"AOS_MODE": "production", envNodeURL: "http://aos:8080", envNodeCredentialFile: cred, envOIDCTokenURL: "http://idp:8080/token", envOIDCClientID: "c", envOIDCSecretFile: cred}},
	}
	for _, k := range casos {
		t.Run(k.nome, func(t *testing.T) {
			for _, v := range []string{"AOS_MODE", envNodeURL, envNodeCredentialFile, envOIDCTokenURL, envOIDCClientID, envOIDCSecretFile} {
				t.Setenv(v, k.env[v])
			}
			if c, err := nodeClientDoAmbiente(); !errors.Is(err, ErrNodeClientConfig) || c != nil {
				t.Fatalf("tinha de recusar com ErrNodeClientConfig: c=%v err=%v", c, err)
			}
		})
	}
	// Sem URL: o executor não está composto — não é erro.
	t.Setenv(envNodeURL, "")
	if c, err := nodeClientDoAmbiente(); c != nil || err != nil {
		t.Fatalf("sem %s o executor não se compõe: c=%v err=%v", envNodeURL, c, err)
	}
}

// Em produção o troço interno do compose (o que o edge já usa até ao nó) é aceite por http; um
// host público não.
func TestAOS413_ClienteDoNoHttpSoNaRedeInterna(t *testing.T) {
	dir := t.TempDir()
	cred := filepath.Join(dir, "nhi.jwt")
	escrever(t, cred, "nhi")
	segredo := filepath.Join(dir, "s")
	escrever(t, segredo, "s")
	t.Setenv("AOS_MODE", "production")
	t.Setenv(envNodeCredentialFile, cred)
	t.Setenv(envOIDCTokenURL, "https://idp:8443/realms/aos/protocol/openid-connect/token")
	t.Setenv(envOIDCClientID, "aos-reader")
	t.Setenv(envOIDCSecretFile, segredo)
	for url, aceita := range map[string]bool{
		"http://aos:8080":                 true,
		"http://127.0.0.1:8080":           true,
		"https://aos.elysiumii.site:8444": true,
		"http://aos.elysiumii.site:8444":  false,
		"http://10.0.0.5:8080":            false,
	} {
		t.Setenv(envNodeURL, url)
		c, err := nodeClientDoAmbiente()
		if aceita && (err != nil || c == nil) {
			t.Fatalf("%s tinha de ser aceite em produção: %v", url, err)
		}
		if !aceita && !errors.Is(err, ErrNodeClientConfig) {
			t.Fatalf("%s tinha de ser recusado em produção, veio %v", url, err)
		}
	}
}

// Num redirect o Go reenviava o CORPO — com o NHI do run — para o destino novo. O cliente não
// segue redirects.
func TestAOS413_ClienteDoNoNaoSegueRedirects(t *testing.T) {
	recebeu := false
	destino := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recebeu = true
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(destino.Close)
	origem := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destino.URL+"/runs", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(origem.Close)
	dir := t.TempDir()
	cred := filepath.Join(dir, "nhi.jwt")
	escrever(t, cred, "nhi-secreto")
	for _, v := range []string{"AOS_MODE", envOIDCTokenURL, envOIDCClientID, envOIDCSecretFile} {
		t.Setenv(v, "")
	}
	t.Setenv(envNodeURL, origem.URL)
	t.Setenv(envNodeCredentialFile, cred)
	c, err := nodeClientDoAmbiente()
	if err != nil {
		t.Fatalf("nodeClientDoAmbiente: %v", err)
	}
	if err := c.Submit(context.Background(), pedidoDeRun{RunID: "r~n1", Objective: "x", Tools: []string{}}); err == nil {
		t.Fatal("um redirect tinha de ser erro")
	}
	if recebeu {
		t.Fatal("o corpo com o NHI seguiu o redirect para outro destino")
	}
}

// Um 404 passageiro (outra réplica, ou a janela antes da retoma de arranque do nó) não dá o run
// por perdido; uma leitura falhada também não fecha nada.
func TestAOS413_404ELeituraFalhadaNaoFechamONo(t *testing.T) {
	agora := time.Unix(1_800_000_000, 0)
	cli := &aos413RunnerFalso{respostas: map[string]aos413Resposta{"r~a": {existe: false}, "r~b": {err: errors.New("rede")}}}
	e := &executorDeNos{cli: cli, runID: "r", emVoo: map[string]struct{}{"a": {}, "b": {}},
		sumidos: map[string]time.Time{}, agora: func() time.Time { return agora }}
	for i := 0; i < 3; i++ {
		fechados, err := e.recolher(context.Background())
		if err != nil || fechados != 0 {
			t.Fatalf("passagem %d: fechados=%d err=%v — nada podia fechar dentro da tolerância", i, fechados, err)
		}
		agora = agora.Add(toleranciaA404 / 4)
	}
	if len(e.emVoo) != 2 {
		t.Fatalf("os dois nós tinham de continuar em voo: %v", e.emVoo)
	}
}

type aos413Resposta struct {
	st     estadoDoRun
	existe bool
	err    error
}

type aos413RunnerFalso struct{ respostas map[string]aos413Resposta }

func (f *aos413RunnerFalso) Submit(context.Context, pedidoDeRun) error { return nil }
func (f *aos413RunnerFalso) Status(_ context.Context, id string) (estadoDoRun, bool, error) {
	r := f.respostas[id]
	return r.st, r.existe, r.err
}

func TestAOS413_GramaticaDoVeredicto(t *testing.T) {
	for saida, quer := range map[string]string{
		`{"outcome":"pass","reasons":["ok"]}`:    "pass",
		`  {"reasons":[],"outcome":"fail"}  `:    "fail",
		`{"outcome":"pass"}`:                     "pass",
		`{"outcome":"fail","outcome":"pass"}`:    "fail:ilegivel",
		`{"OUTCOME":"pass"}`:                     "fail:ilegivel",
		`{"outcome":"pass"}}`:                    "fail:ilegivel",
		`{"outcome":"pass"} {"x":1}`:             "fail:ilegivel",
		`{"outcome":"Pass"}`:                     "fail:ilegivel",
		`{"outcome":"pass","reasons":["a b"]}`:   "fail:ilegivel",
		`{"outcome":"pass","reasons":["a","a"]}`: "fail:ilegivel",
		`["pass"]`:                               "fail:ilegivel",
		``:                                       "fail:ilegivel",
	} {
		v := veredictoDaSaida(saida)
		got := string(v.Outcome)
		if len(v.Reasons) == 1 && v.Reasons[0] == razaoVeredictoIlegivel {
			got += ":ilegivel"
		}
		if got != quer {
			t.Errorf("%q → %s, quero %s", saida, got, quer)
		}
	}
}
