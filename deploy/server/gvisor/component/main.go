// Comando component — o lado HOST do executor gVisor.
//
// Expõe o MESMO contrato HTTP do orchestrator do Firecracker (`/healthz`, `POST /exec`), de
// propósito: o nó fala com um ou com outro sem saber qual, e trocar de driver é topologia.
//
// Cada execução corre num sandbox gVisor DEDICADO e EFÉMERO:
//   - bundle OCI novo por chamada, apagado no fim — a execução N+1 nunca observa artefactos de N;
//   - rootfs READ-ONLY; a raiz semeada entra como bind read-only em /seed;
//   - sem rede (`--network=none` + namespace de rede vazio), sem capabilities, noNewPrivileges;
//   - o processo corre como não-root dentro do sandbox.
//
// ─── A fronteira, com honestidade ────────────────────────────────────────────────────────────
// O gVisor interpõe syscalls em user-space (plataforma `systrap` por omissão). É mais forte do
// que um jail in-process — o guest fala com o kernel do gVisor, não com o do host — e mais fraca
// do que a microVM do Firecracker, que tem uma fronteira de virtualização de hardware. Escolhe-se
// gVisor quando o host NÃO expõe `/dev/kvm`, que é o caso de qualquer máquina que seja ela
// própria um convidado sem virtualização aninhada.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type toolCall struct {
	ToolID  string   `json:"tool_id"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Path    string   `json:"path,omitempty"`
	Write   []byte   `json:"write,omitempty"`
}

type execInput struct {
	RunID  string   `json:"run_id"`
	StepID string   `json:"step_id"`
	Call   toolCall `json:"call"`
}

type result struct {
	Stdout   []byte `json:"stdout,omitempty"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

var (
	listenAddr = envOr("GV_ADDR", ":9101")
	seedDir    = envOr("GV_SEED_DIR", "/seed")
	guestBin   = envOr("GV_GUEST_BIN", "/guest")
	runscBin   = envOr("GV_RUNSC", "runsc")
	platform   = envOr("GV_PLATFORM", "systrap")
	execTMO    = envDur("GV_EXEC_TIMEOUT", 45*time.Second)
)

func envOr(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}

func envDur(k string, d time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		if p, err := time.ParseDuration(v); err == nil && p > 0 {
			return p
		}
	}
	return d
}

func main() {
	if _, err := exec.LookPath(runscBin); err != nil {
		fatal(fmt.Errorf("runsc nao encontrado (%q): %w", runscBin, err))
	}
	if _, err := os.Stat(guestBin); err != nil {
		fatal(fmt.Errorf("binario do guest ausente (%q): %w", guestBin, err))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/exec", handleExec)

	logf("a escutar em %s (runsc=%s platform=%s seed=%s)", listenAddr, runscBin, platform, seedDir)
	srv := &http.Server{Addr: listenAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		fatal(err)
	}
}

func handleExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in execInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, result{ExitCode: 1, Error: "input invalido: " + err.Error()})
		return
	}
	writeJSON(w, runSandboxed(r.Context(), in))
}

// runSandboxed monta o bundle OCI, corre o runsc e devolve o resultado. Qualquer falha é um
// resultado com Error — nunca uma execução fora do sandbox.
func runSandboxed(ctx context.Context, in execInput) result {
	ctx, cancel := context.WithTimeout(ctx, execTMO)
	defer cancel()

	id := sanitize(in.RunID)
	if id == "" {
		id = "gv"
	}
	bundle, err := os.MkdirTemp("", "aos-gv-"+id+"-")
	if err != nil {
		return result{ExitCode: 1, Error: "criar bundle: " + err.Error()}
	}
	// EFEMERIDADE: o bundle inteiro desaparece no fim, sempre. É o que garante que a execução
	// seguinte não observa artefactos desta (a invariante de AOS-066 nesta camada).
	defer os.RemoveAll(bundle)

	rootfs := filepath.Join(bundle, "rootfs")
	if err := os.MkdirAll(filepath.Join(rootfs, "seed"), 0o755); err != nil {
		return result{ExitCode: 1, Error: "criar rootfs: " + err.Error()}
	}
	if err := os.MkdirAll(filepath.Join(rootfs, "proc"), 0o755); err != nil {
		return result{ExitCode: 1, Error: "criar /proc: " + err.Error()}
	}
	if err := copyFile(guestBin, filepath.Join(rootfs, "guest"), 0o755); err != nil {
		return result{ExitCode: 1, Error: "copiar guest: " + err.Error()}
	}
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), ociConfig(seedDir), 0o644); err != nil {
		return result{ExitCode: 1, Error: "escrever config.json: " + err.Error()}
	}

	payload, err := json.Marshal(in)
	if err != nil {
		return result{ExitCode: 1, Error: "serializar input: " + err.Error()}
	}

	// --network=none: sem stack de rede no sandbox. Uma tool com egress externo NUNCA deve
	// chegar aqui — é o PDP que a nega a montante —, mas a defesa em profundidade é gratuita.
	//
	// ⚠️ ORDEM DOS ARGUMENTOS: `--platform`/`--network` são flags GLOBAIS (antes do subcomando),
	// `--bundle` é flag DO SUBCOMANDO `run` (depois dele). Trocá-las dá
	// "flag provided but not defined: -bundle" — e o runsc responde com o usage inteiro, o que
	// torna o erro fácil de ler mas fácil de escrever mal.
	cmd := exec.CommandContext(ctx, runscBin,
		"--platform="+platform,
		"--network=none",
		"run", "--bundle", bundle,
		"aos-"+id+"-"+sanitize(in.StepID)+"-"+fmt.Sprint(time.Now().UnixNano()),
	)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// O guest pode ter escrito um resultado antes de sair != 0.
			if r, ok := decodeResult(stdout.Bytes()); ok {
				return r
			}
			return result{ExitCode: ee.ExitCode(), Error: "runsc: " + trim(stderr.String())}
		}
		return result{ExitCode: 1, Error: "runsc: " + err.Error() + " " + trim(stderr.String())}
	}
	r, ok := decodeResult(stdout.Bytes())
	if !ok {
		return result{ExitCode: 1, Error: "guest nao devolveu um resultado legivel: " + trim(stderr.String())}
	}
	return r
}

// decodeResult lê o Result que o guest escreveu no stdout. Fail-closed: stdout que não seja um
// Result é tratado como ausência de resultado, nunca como sucesso vazio.
func decodeResult(b []byte) (result, bool) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return result{}, false
	}
	var r result
	if err := json.Unmarshal(b, &r); err != nil {
		return result{}, false
	}
	return r, true
}

// ociConfig devolve o config.json do bundle. As opções são o contrato de isolamento desta
// camada, e cada uma existe por uma razão: rootfs read-only, seed em bind read-only, zero
// capabilities, noNewPrivileges, namespaces próprios (incluindo rede, vazia) e um uid não-root.
func ociConfig(seed string) []byte {
	cfg := map[string]any{
		"ociVersion": "1.0.2",
		"process": map[string]any{
			"terminal": false,
			"user":     map[string]any{"uid": 65532, "gid": 65532},
			"args":     []string{"/guest"},
			"env":      []string{"PATH=/", "AOS_SEED_ROOT=/seed"},
			"cwd":      "/",
			"capabilities": map[string]any{
				"bounding": []string{}, "effective": []string{},
				"permitted": []string{}, "inheritable": []string{},
			},
			"noNewPrivileges": true,
		},
		"root":     map[string]any{"path": "rootfs", "readonly": true},
		"hostname": "aos-sandbox",
		"mounts": []any{
			map[string]any{"destination": "/proc", "type": "proc", "source": "proc"},
			map[string]any{
				"destination": "/seed", "type": "bind", "source": seed,
				"options": []string{"rbind", "ro", "nosuid", "nodev", "noexec"},
			},
		},
		"linux": map[string]any{
			"namespaces": []any{
				map[string]any{"type": "pid"},
				map[string]any{"type": "ipc"},
				map[string]any{"type": "uts"},
				map[string]any{"type": "mount"},
				map[string]any{"type": "network"},
			},
		},
	}
	b, _ := json.Marshal(cfg)
	return b
}

func copyFile(src, dst string, mode os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, mode)
}

// sanitize reduz um identificador ao que é seguro num nome de ficheiro/container.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func logf(f string, a ...any) { fmt.Fprintf(os.Stderr, "[gv-comp] "+f+"\n", a...) }
func fatal(err error)         { fmt.Fprintln(os.Stderr, "[gv-comp] FATAL:", err); os.Exit(1) }
