//go:build aoslive

package main

// AOS-292 AC2 — O CICLO pause → steer → resume PELO NÓ REAL, AO VIVO.
//
// Teste AO VIVO (build-tag `aoslive`): conduz um nó `aos` REAL, já a correr, pelo ciclo completo
// do canal de controlo — `POST /runs/{id}/pause` → `POST /runs/{id}/steer` →
// `POST /runs/{id}/resume` — e exige que o run deixe de estar pausado e que a correcção tenha
// sido ENTREGUE ao loop. NÃO corre na CI normal.
//
//	go test -tags aoslive -run TestAOSLive_292 ./...
//
// # PORQUE É QUE ESTE TESTE VIVE AQUI E NÃO NA SUITE NORMAL
//
// Esta AC ficou por fechar em `d169198` com a razão errada. Escrevi então que fechá-la «exige uma
// fixture que corra um turno real antes de pausar» — e uma fixture que finge o ambiente produz um
// teste que passa sem provar nada, que é exactamente a falsificação de teste que o EPIC-21 existe
// para perseguir. A resposta é ambiente real, não fixture melhor.
//
// O obstáculo concreto é este: os runs das fixtures deste pacote nunca correram um turno a sério.
// Não têm capturas, e a retoma falha no plano de replay antes de chegar ao canal — o comentário de
// `aos263_decisao_simetrica_test.go` já reconhece que nenhum teste do repositório consegue hoje uma
// retoma HTTP bem-sucedida. Com um Model Gateway real por trás, o run executa turnos, produz
// capturas, e a retoma percorre o caminho todo.
//
// # NÃO CORRIDO
//
// Este ficheiro foi escrito contra os contratos do nó (rotas, corpos, códigos de estado, formato do
// emissor assinado) e NUNCA foi executado: esta máquina não tem cluster. Não é evidência de que a
// AC2 passa — é a evidência que falta produzir, e o sítio onde produzi-la. Quem o correr pela
// primeira vez deve esperar ter de o corrigir.
//
// # O QUE O TESTE PROVA, E O QUE NÃO CONSEGUE PROVAR
//
// A AC pede «a correcção materializada no `PromptView`». O `PromptView` NÃO é observável por HTTP:
// o evento `turn.recorded` carrega o `prompt_hash`, não o texto do prompt, e comparar hashes entre
// dois runs provaria só que algo mudou. O que este teste usa como prova de materialização é o
// evento `control.correction_consumed` (AOS-292), que é escrito NO PONTO DA ENTREGA ao loop — se
// ele existe, a correcção chegou ao prompt; se não existe, não chegou. É mais preciso do que ler
// texto, e está declarado aqui para ninguém o confundir com uma leitura do prompt.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	control "github.com/aos-ref/kernel/agent-runtime/control"
	eventstore "github.com/aos-ref/substrate/eventstore"
)

// liveEnv é o ambiente que o teste exige. Cada campo nomeia a variável que o preenche, para a
// mensagem de skip poder dizer exactamente o que falta em vez de «ambiente incompleto».
type liveEnv struct {
	addr       string // AOS_LIVE_ADDR          — base do plano de controlo, ex. https://aos.svc:8443
	operatorID string // AOS_LIVE_OPERATOR_ID   — id de um operador em AOS_OPERATORS do nó
	keyPath    string // AOS_LIVE_OPERATOR_KEY  — seed ed25519 do operador (NUNCA no nó)
	credential string // AOS_LIVE_CREDENTIAL    — credencial FRESCA para a retoma
	reader     string // AOS_LIVE_READER        — principal do read-path soberano
	board      string // AOS_LIVE_BOARD         — board do read-path soberano
	principal  string // AOS_LIVE_PRINCIPAL     — NHI submissor do run
	// Opcionais.
	goal     string // AOS_LIVE_GOAL — objectivo do run (default: um que dá vários turnos)
	walPath  string // AOS_LIVE_WAL  — WAL do nó; activa a fase durável (a prova da AC2)
	certPath string // AOS_LIVE_CLIENT_CERT — mTLS do plano de controlo
	keyFile  string // AOS_LIVE_CLIENT_KEY
	caPath   string // AOS_LIVE_CA
}

func liveEnvOrSkip(t *testing.T) liveEnv {
	t.Helper()
	e := liveEnv{
		addr:       os.Getenv("AOS_LIVE_ADDR"),
		operatorID: os.Getenv("AOS_LIVE_OPERATOR_ID"),
		keyPath:    os.Getenv("AOS_LIVE_OPERATOR_KEY"),
		credential: os.Getenv("AOS_LIVE_CREDENTIAL"),
		reader:     os.Getenv("AOS_LIVE_READER"),
		board:      os.Getenv("AOS_LIVE_BOARD"),
		principal:  os.Getenv("AOS_LIVE_PRINCIPAL"),
		goal:       os.Getenv("AOS_LIVE_GOAL"),
		walPath:    os.Getenv("AOS_LIVE_WAL"),
		certPath:   os.Getenv("AOS_LIVE_CLIENT_CERT"),
		keyFile:    os.Getenv("AOS_LIVE_CLIENT_KEY"),
		caPath:     os.Getenv("AOS_LIVE_CA"),
	}
	var falta []string
	for _, par := range [][2]string{
		{"AOS_LIVE_ADDR", e.addr},
		{"AOS_LIVE_OPERATOR_ID", e.operatorID},
		{"AOS_LIVE_OPERATOR_KEY", e.keyPath},
		{"AOS_LIVE_CREDENTIAL", e.credential},
		{"AOS_LIVE_READER", e.reader},
		{"AOS_LIVE_BOARD", e.board},
		{"AOS_LIVE_PRINCIPAL", e.principal},
	} {
		if strings.TrimSpace(par[1]) == "" {
			falta = append(falta, par[0])
		}
	}
	if len(falta) > 0 {
		// Skip e não Fatal: o build-tag já garante que isto não corre na CI, pelo que um skip aqui
		// só acontece a quem PEDIU a tag e não montou o ambiente — e a mensagem tem de dizer o quê.
		t.Skipf("ambiente ao vivo incompleto: falta %s (ver o cabeçalho deste ficheiro)", strings.Join(falta, ", "))
	}
	if e.goal == "" {
		e.goal = "investiga o estado do sistema e resume as conclusoes em varios passos"
	}
	return e
}

// liveClient constrói o cliente HTTP, com mTLS quando o plano de controlo o exige (o nó recusa
// rotas de controlo sem certificado de cliente quando o bind não é loopback).
func liveClient(t *testing.T, e liveEnv) *http.Client {
	t.Helper()
	c := &http.Client{Timeout: 30 * time.Second}
	if e.certPath == "" && e.caPath == "" {
		return c
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if e.certPath != "" {
		cert, err := tls.LoadX509KeyPair(e.certPath, e.keyFile)
		if err != nil {
			t.Fatalf("mTLS: carregar par cliente (%s/%s): %v", e.certPath, e.keyFile, err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	if e.caPath != "" {
		pem, err := os.ReadFile(e.caPath)
		if err != nil {
			t.Fatalf("mTLS: ler CA %s: %v", e.caPath, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			t.Fatalf("mTLS: CA %s nao contem certificado PEM", e.caPath)
		}
		cfg.RootCAs = pool
	}
	c.Transport = &http.Transport{TLSClientConfig: cfg}
	return c
}

// livePost envia um JSON e devolve o código e o corpo, sem interpretar nenhum dos dois — quem
// chama é que sabe o que esperar, e uma asserção sobre o corpo tem de ver o corpo real.
func livePost(t *testing.T, c *http.Client, e liveEnv, path string, body any) (int, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal do corpo para %s: %v", path, err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, e.addr+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("montar POST %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// liveGetRun lê o estado do run pelo read-path SOBERANO — com os headers de leitor e board, que o
// nó exige fail-closed (board vazio ou desconhecido ⇒ 404, e não 403: não enumera runs alheios).
func liveGetRun(t *testing.T, c *http.Client, e liveEnv, runID string) (int, runStateResponse, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, e.addr+"/runs/"+runID, nil)
	if err != nil {
		t.Fatalf("montar GET /runs/%s: %v", runID, err)
	}
	req.Header.Set(HeaderReaderPrincipal, e.reader)
	req.Header.Set(HeaderReaderBoard, e.board)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET /runs/%s: %v", runID, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var vista runStateResponse
	_ = json.Unmarshal(b, &vista)
	return resp.StatusCode, vista, string(b)
}

// liveEmitter assina um sinal de controlo com a chave PRIVADA do operador — nesta máquina, nunca no
// nó (ADR-016 §1). Nonce fresco por sinal: o nó consome-os de forma durável e de uso único, pelo que
// reutilizar um é fabricar um replay e o nó recusa-o.
func liveEmitter(t *testing.T, e liveEnv, runID string, kind control.SignalKind, payload []byte) emitterWire {
	t.Helper()
	priv, err := loadOperatorKey(e.keyPath)
	if err != nil {
		t.Fatalf("carregar a seed do operador %s: %v", e.keyPath, err)
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("gerar nonce: %v", err)
	}
	em := integration.SignSignal(priv, e.operatorID, runID, kind, payload, nonce, time.Now().UTC())
	return emitterWire{
		ID:        em.ID,
		Signature: base64.StdEncoding.EncodeToString(em.Signature),
		Nonce:     base64.StdEncoding.EncodeToString(em.Nonce),
		IssuedAt:  em.IssuedAt,
	}
}

// aguardar sonda uma condição até ao prazo. Não há sleep fixo: um turno real demora o que demorar, e
// um prazo fixo produziria um teste flaky que se «arranja» aumentando o número — que é a forma mais
// comum de um teste ao vivo passar a mentir.
func aguardar(t *testing.T, prazo time.Duration, oQue string, cond func() (bool, string)) string {
	t.Helper()
	fim := time.Now().Add(prazo)
	var ultimo string
	for time.Now().Before(fim) {
		ok, estado := cond()
		ultimo = estado
		if ok {
			return estado
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("esgotou %s a esperar por %s; ultimo estado observado: %s", prazo, oQue, ultimo)
	return ultimo
}

// TestAOSLive_292_CicloDeControloPeloNoReal é a AC2.
func TestAOSLive_292_CicloDeControloPeloNoReal(t *testing.T) {
	e := liveEnvOrSkip(t)
	c := liveClient(t, e)
	runID := fmt.Sprintf("aoslive-292-%d", time.Now().UnixNano())
	correccao := fmt.Sprintf("AOS292-LIVE-%d: ignora o rumo anterior e enumera as premissas", time.Now().Unix())

	// (1) SUBMETE um run que dá para vários turnos — a pausa tem de apanhar o run VIVO.
	if st, body := livePost(t, c, e, "/runs", submitRequest{
		RunID: runID, Objective: e.goal, PrincipalNHI: e.principal,
		Credential: e.credential, MaxTurns: 8,
	}); st != http.StatusCreated && st != http.StatusAccepted {
		t.Fatalf("POST /runs = %d, quero 201/202: %s", st, body)
	}
	aguardar(t, 60*time.Second, "o run comecar a correr", func() (bool, string) {
		st, v, raw := liveGetRun(t, c, e, runID)
		if st != http.StatusOK {
			return false, fmt.Sprintf("GET=%d %s", st, raw)
		}
		return v.Status == "running", "status=" + v.Status
	})

	// (2) PAUSA assinada.
	if st, body := livePost(t, c, e, "/runs/"+runID+"/pause",
		pauseRequest{Emitter: liveEmitter(t, e, runID, control.SignalPause, nil)}); st != http.StatusAccepted && st != http.StatusOK {
		t.Fatalf("POST /pause = %d, quero 202: %s", st, body)
	}
	// A pausa é GRACIOSA: materializa-se no FIM do turno, não a meio. Esperar por ela é parte do
	// contrato, não impaciência.
	aguardar(t, 120*time.Second, "a pausa materializar-se no fim do turno", func() (bool, string) {
		_, v, raw := liveGetRun(t, c, e, runID)
		if v.Paused {
			return true, "paused=true"
		}
		return false, raw
	})

	// (3) STEER assinado, com uma correcção distinguível.
	payload := []byte(correccao)
	if st, body := livePost(t, c, e, "/runs/"+runID+"/steer", steerRequest{
		Emitter: liveEmitter(t, e, runID, control.SignalSteer, payload),
		Payload: base64.StdEncoding.EncodeToString(payload),
	}); st != http.StatusAccepted && st != http.StatusOK {
		t.Fatalf("POST /steer = %d, quero 202: %s", st, body)
	}

	// (4) RETOMA SEM EMISSOR — TEM de ser recusada. Esta é a metade da AC1 que só se prova por
	// HTTP: antes de AOS-292 a rota não passava pelo canal e quem detivesse a credencial do run
	// desfazia a pausa de um operador sem assinar nada. Um 202 aqui é a regressão.
	st, body := livePost(t, c, e, "/runs/"+runID+"/resume", resumeRequest{Credential: e.credential})
	if st != http.StatusForbidden {
		t.Fatalf("POST /resume SEM emissor = %d, quero 403 — a credencial do run NAO pode levantar uma pausa de operador (AOS-292): %s", st, body)
	}
	if !strings.Contains(body, "emitter") {
		t.Errorf("o 403 nao nomeia o que falta; corpo=%s", body)
	}

	// (5) RETOMA COM EMISSOR ASSINADO.
	if st, body := livePost(t, c, e, "/runs/"+runID+"/resume", resumeRequest{
		Credential: e.credential,
		Emitter:    liveEmitter(t, e, runID, control.SignalResume, nil),
	}); st != http.StatusAccepted && st != http.StatusOK {
		t.Fatalf("POST /resume COM emissor = %d, quero 202: %s", st, body)
	}

	// (6) `Result.Paused == false` no turno seguinte — a primeira metade da AC2.
	final := aguardar(t, 180*time.Second, "o run deixar de estar pausado", func() (bool, string) {
		_, v, raw := liveGetRun(t, c, e, runID)
		if !v.Paused {
			return true, "status=" + v.Status
		}
		return false, raw
	})
	t.Logf("run %s retomado: %s", runID, final)

	// (7) A CORRECÇÃO MATERIALIZADA — a segunda metade da AC2, e a que exige o log durável.
	if e.walPath == "" {
		t.Fatalf("AOS_LIVE_WAL nao definido: os passos 1-6 correram, mas a materializacao da correccao NAO foi verificada — a AC2 pede as duas metades, e declarar esta por verificar seria fechar a AC com meia prova")
	}
	verificarLogDeControlo(t, e.walPath, runID)
}

// verificarLogDeControlo exige, no log DURÁVEL do nó, os quatro sinais do ciclo.
//
// O `control.correction_consumed` é a prova de materialização: é escrito no ponto em que o loop
// RECEBE a correcção para o prompt. O `control.resume` prova a AC3 sobre um nó real — que a retoma
// passou pelo canal e não por uma via paralela.
func verificarLogDeControlo(t *testing.T, walPath, runID string) {
	t.Helper()
	es, err := eventstore.Open(walPath)
	if err != nil {
		t.Fatalf("abrir o WAL do no em %s: %v", walPath, err)
	}
	defer es.Close()

	visto := map[string]int{}
	for _, s := range es.Streams() {
		if !strings.Contains(s, runID) {
			continue
		}
		evs, err := es.Read(context.Background(), s, 1)
		if err != nil {
			t.Fatalf("ler o stream %q: %v", s, err)
		}
		for _, ev := range evs {
			if strings.HasPrefix(ev.Type, "control.") {
				visto[ev.Type]++
			}
		}
	}
	for _, tipo := range []string{
		control.EventTypeControlPause,
		control.EventTypeControlSteer,
		control.EventTypeControlResume,
		control.EventTypeControlCorrectionConsumed,
	} {
		if visto[tipo] == 0 {
			t.Errorf("o log de controlo do run %s nao tem %q; visto=%v", runID, tipo, visto)
		}
	}
	// A correcção é entregue UMA vez: a de-duplicação durável de AOS-292 existe para que um
	// reinício não a injecte segunda vez num turno cujo prompt já foi capturado.
	if n := visto[control.EventTypeControlCorrectionConsumed]; n > 1 {
		t.Errorf("a correccao foi consumida %d vezes, quero 1 — a de-duplicacao duravel nao esta a segurar", n)
	}
}
