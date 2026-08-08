package main

// CLI do nó `aos` (AOS-165) — só stdlib. Subcomandos:
//
//   - serve                : arranca o nó (config por ambiente; AOS_API_ADDR levanta a API,
//                            AOS-163/166 via run) — o único comando de SERVIDOR;
//   - run/observe/steer/pause : CLIENTES HTTP da API (AOS-166).
//
// O `steer`/`pause` ASSINAM o sinal de controlo com a chave PRIVADA do operador (lida de um
// ficheiro, na máquina do operador — NUNCA no nó), via [integration.SignSignal] (o mesmo
// tuplo canónico injectivo que o Ed25519Authenticator do nó verifica, AOS-160). A CLI é um
// cliente: o nó é que impõe a autenticação/anti-replay/frescura — a CLI só transporta a
// assinatura. Um nonce fresco (crypto/rand) e o carimbo (time.Now) por sinal.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aos-ref/integration"
	"github.com/aos-ref/kernel/agent-runtime/control"
)

// Erros da CLI (fail-closed; comparáveis com errors.Is).
var (
	// ErrAddrRequired — falta o --addr (URL base da API) num comando cliente.
	ErrAddrRequired = errors.New("aos: --addr obrigatorio (URL base da API, ex.: http://127.0.0.1:8080)")
	// ErrRunIDRequired — falta o --run-id.
	ErrRunIDRequired = errors.New("aos: --run-id obrigatorio")
	// ErrEmitterRequired — falta o --emitter (ID do operador) num comando de controlo.
	ErrEmitterRequired = errors.New("aos: --emitter obrigatorio")
	// ErrBadOperatorKey — o ficheiro da chave do operador não é uma seed ed25519 válida.
	ErrBadOperatorKey = errors.New("aos: chave do operador invalida (esperado seed ed25519 de 32 bytes em hex)")
)

// dispatch encaminha argv[1:] para o subcomando. Sem subcomando (ou `serve`) arranca o nó
// (comportamento de AOS-163/166 via [run]); os restantes são clientes HTTP da API.
func dispatch(args []string, w io.Writer) error {
	if len(args) == 0 {
		return run(w)
	}
	switch args[0] {
	case "serve":
		return run(w)
	case "run":
		return cmdRun(args[1:], w)
	case "observe":
		return cmdObserve(args[1:], w)
	case "steer":
		return cmdControl(args[1:], control.SignalSteer, w)
	case "pause":
		return cmdControl(args[1:], control.SignalPause, w)
	case "operator-pubkey":
		return cmdOperatorPubKey(args[1:], w)
	case "wal-count":
		return cmdWALCount(args[1:], w)
	case "audit-trail":
		return cmdAuditTrail(args[1:], w)
	case "help", "-h", "--help":
		printUsage(w)
		return nil
	default:
		return fmt.Errorf("aos: subcomando desconhecido %q (use: serve|run|observe|steer|pause|operator-pubkey|wal-count|audit-trail|help)", args[0])
	}
}

// printUsage escreve o resumo de utilização.
func printUsage(w io.Writer) {
	fmt.Fprint(w, `aos — no de referencia do Agentic OS
uso: aos <subcomando> [flags]

  serve                        arranca o no (config por ambiente; AOS_API_ADDR levanta a API)
  run     --addr URL --objective TXT [--nhi ID] [--credential TOK] [--run-id ID] [--system TXT] [--scope CSV] [--max-turns N]
  observe --addr URL --run-id ID [--reader ID] [--board ID]   (--reader/--board exigidos por um no com soberania de leitura)
  steer   --addr URL --run-id ID --emitter ID --key FICHEIRO --correction TXT
  pause   --addr URL --run-id ID --emitter ID --key FICHEIRO
  operator-pubkey --key FICHEIRO            (imprime a PUBKEY hex da seed do operador, para AOS_OPERATORS)
  wal-count --path WAL --run ID [--turns]   (diagnostico read-only de durabilidade do Event Store)
  audit-trail --path WORM --run ID [--denied-only]  (diagnostico read-only: o que foi decidido, por quem e porque)
`)
}

// cmdRun submete um goal via POST /runs.
func cmdRun(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(w)
	addr := fs.String("addr", strings.TrimSpace(os.Getenv("AOS_API_ADDR")), "URL base da API")
	objective := fs.String("objective", "", "objectivo do run")
	runID := fs.String("run-id", "", "RunID estavel (o servidor pode exigi-lo)")
	nhi := fs.String("nhi", "", "principal NHI")
	cred := fs.String("credential", "", "token NHI (Credential)")
	system := fs.String("system", "", "system prompt")
	scope := fs.String("scope", "", "scopes separados por virgula")
	maxTurns := fs.Int("max-turns", 0, "tecto de turnos")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*addr) == "" {
		return ErrAddrRequired
	}
	var resp submitResponse
	if err := apiCall(*addr, http.MethodPost, "/runs", submitRequest{
		RunID: *runID, Objective: *objective, PrincipalNHI: *nhi,
		Credential: *cred, System: *system, Scope: splitCSV(*scope), MaxTurns: *maxTurns,
	}, &resp); err != nil {
		return err
	}
	fmt.Fprintf(w, "run submetido: run_id=%s status=%s\n", resp.RunID, resp.Status)
	return nil
}

// cmdObserve lê o estado/desfecho de um run via GET /runs/{id}.
func cmdObserve(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("observe", flag.ContinueOnError)
	fs.SetOutput(w)
	addr := fs.String("addr", strings.TrimSpace(os.Getenv("AOS_API_ADDR")), "URL base da API")
	runID := fs.String("run-id", "", "RunID a observar")
	// Identidade de LEITURA de governação (AOS-172, D7). Um nó com soberania de leitura ligada
	// EXIGE-a (fail-closed): board vazio/desconhecido ⇒ 404. A CLI só a transporta nos headers;
	// o nó impõe a regra board→região e sela a leitura no WORM (D6). Defaults por ambiente.
	reader := fs.String("reader", strings.TrimSpace(os.Getenv("AOS_READER")), "principal (NHI) de leitura de governacao")
	board := fs.String("board", strings.TrimSpace(os.Getenv("AOS_BOARD")), "board de governacao do leitor (resolve a regiao autorizada)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*addr) == "" {
		return ErrAddrRequired
	}
	if strings.TrimSpace(*runID) == "" {
		return ErrRunIDRequired
	}
	var headers map[string]string
	if strings.TrimSpace(*reader) != "" || strings.TrimSpace(*board) != "" {
		headers = map[string]string{HeaderReaderPrincipal: *reader, HeaderReaderBoard: *board}
	}
	var st runStateResponse
	if err := apiCall(*addr, http.MethodGet, "/runs/"+*runID, nil, &st, headers); err != nil {
		return err
	}
	fmt.Fprintf(w, "run %s: status=%s terminated=%t paused=%t panicked=%t turns=%d%s%s\n",
		st.RunID, st.Status, st.Terminated, st.Paused, st.Panicked, st.Turns,
		optField(" final=", st.FinalText), optField(" error=", st.Error))
	return nil
}

// cmdControl assina (com a chave do operador) e envia um sinal de controlo (steer/pause).
func cmdControl(args []string, kind control.SignalKind, w io.Writer) error {
	fs := flag.NewFlagSet(string(kind), flag.ContinueOnError)
	fs.SetOutput(w)
	addr := fs.String("addr", strings.TrimSpace(os.Getenv("AOS_API_ADDR")), "URL base da API")
	runID := fs.String("run-id", "", "RunID a conduzir")
	emitterID := fs.String("emitter", "", "ID do operador emissor (pubkey registada no no)")
	keyPath := fs.String("key", "", "ficheiro da seed ed25519 (32 bytes hex) do operador")
	correction := fs.String("correction", "", "correccao a injectar (so steer)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(*addr) == "":
		return ErrAddrRequired
	case strings.TrimSpace(*runID) == "":
		return ErrRunIDRequired
	case strings.TrimSpace(*emitterID) == "":
		return ErrEmitterRequired
	}
	priv, err := loadOperatorKey(*keyPath)
	if err != nil {
		return err
	}

	// SÓ steer carrega payload; pause é sem payload. O nonce fresco e o carimbo são
	// gerados por sinal (anti-replay/frescura impostos no nó).
	var payload []byte
	if kind == control.SignalSteer {
		payload = []byte(*correction)
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("aos: gerar nonce: %w", err)
	}
	em := integration.SignSignal(priv, *emitterID, *runID, kind, payload, nonce, time.Now().UTC())
	wire := emitterWire{
		ID:        em.ID,
		Signature: base64.StdEncoding.EncodeToString(em.Signature),
		Nonce:     base64.StdEncoding.EncodeToString(em.Nonce),
		IssuedAt:  em.IssuedAt,
	}

	var path string
	var body any
	if kind == control.SignalPause {
		path, body = "/runs/"+*runID+"/pause", pauseRequest{Emitter: wire}
	} else {
		path, body = "/runs/"+*runID+"/steer", steerRequest{Emitter: wire, Payload: base64.StdEncoding.EncodeToString(payload)}
	}
	if err := apiCall(*addr, http.MethodPost, path, body, nil); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s enviado a %s (emissor %s)\n", kind, *runID, *emitterID)
	return nil
}

// cmdOperatorPubKey deriva e imprime a PUBKEY ed25519 (64 hex chars) da seed do operador, no
// formato EXACTO que AOS_OPERATORS espera (AOS-193). Corre na máquina do OPERADOR, sobre o
// MESMO ficheiro de seed que `aos steer`/`aos pause` usam para assinar — fecha o ciclo
// "gerar chave → registar no nó" sem obrigar a ferramenta externa alguma (zero-dep) e sem que
// o operador tenha de manipular material privado à mão.
//
// SÓ SAI MATERIAL PÚBLICO: imprime a pubkey; a seed/chave privada NUNCA é impressa nem
// devolvida. É a contraparte declarativa da regra do nó ("só pubkeys entram").
func cmdOperatorPubKey(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("operator-pubkey", flag.ContinueOnError)
	fs.SetOutput(w)
	keyPath := fs.String("key", "", "ficheiro da seed ed25519 (32 bytes hex) do operador")
	emitterID := fs.String("emitter", "", "se dado, imprime a entrada completa \"emitterID=hexpubkey\" pronta para AOS_OPERATORS")
	if err := fs.Parse(args); err != nil {
		return err
	}
	priv, err := loadOperatorKey(*keyPath)
	if err != nil {
		return err
	}
	pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	if id := strings.TrimSpace(*emitterID); id != "" {
		fmt.Fprintf(w, "%s=%s\n", id, pub)
		return nil
	}
	fmt.Fprintf(w, "%s\n", pub)
	return nil
}

// loadOperatorKey lê a seed ed25519 (32 bytes em hex) do operador de um ficheiro e deriva a
// chave privada. A chave vive na máquina do OPERADOR, nunca no nó (a CLI é a contraparte de
// assinatura de AOS-160). Fail-closed: ficheiro em falta/inválido não produz chave.
func loadOperatorKey(path string) (ed25519.PrivateKey, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("aos: --key obrigatorio (ficheiro da seed do operador): %w", ErrBadOperatorKey)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("aos: ler chave do operador: %w", err)
	}
	seed, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, ErrBadOperatorKey
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// apiCall é o cliente HTTP mínimo (stdlib): serializa reqBody (se não-nil) em JSON, chama
// method base+path com os headers dados (se algum), e descodifica a resposta em out (se
// não-nil). Um status >= 300 é erro. Os headers transportam a identidade de leitura de
// governação (AOS-172) sem que a CLI imponha qualquer regra — o nó é que autentica/autoriza.
func apiCall(base, method, path string, reqBody, out any, headers ...map[string]string) error {
	var r io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("aos: serializar pedido: %w", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimRight(base, "/")+path, r)
	if err != nil {
		return fmt.Errorf("aos: pedido invalido: %w", err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, hs := range headers {
		for k, v := range hs {
			if strings.TrimSpace(v) != "" {
				req.Header.Set(k, v)
			}
		}
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("aos: chamada a API: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("aos: API devolveu %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("aos: descodificar resposta: %w", err)
		}
	}
	return nil
}

// optField devolve prefix+v se v não-vazio, senão "".
func optField(prefix, v string) string {
	if v == "" {
		return ""
	}
	return prefix + v
}
