// orchestrator é o COMPONENTE HOST-SIDE que conduz o Firecracker (a integração real que o
// skeleton sandbox.FirecrackerDriver deixa fora do módulo Go do nó). Corre privilegiado com
// /dev/kvm; o NÓ (zero-dep) fala com ele por HTTP stdlib — o mesmo padrão do serviço attestation.
//
// Por POST /exec: arranca UMA microVM DEDICADA (microVM-por-tool-call, ADR-004), passa a tool
// call ao guest-agent por vsock, recolhe o Result e mata a VM. Sem API socket exposta ao guest,
// sem namespace de rede/PID partilhado (fronteira de virtualização), rootfs read-only.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aos-ref/deploy/firecracker/wire"
)

var (
	fcBin      = env("FC_BIN", "/usr/local/bin/firecracker")
	kernelPath = env("FC_KERNEL", "/art/vmlinux")
	rootfsPath = env("FC_ROOTFS", "/art/rootfs.ext4")
	listenAddr = env("FC_ADDR", ":9100")
	runDir     = env("FC_RUNDIR", "/run/fc")
	seq        atomic.Uint64
)

func env(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}

func main() {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/exec", handleExec)
	logf("orchestrator firecracker a escutar em %s (kernel=%s rootfs=%s)", listenAddr, kernelPath, rootfsPath)
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
	var in wire.ExecInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, wire.Result{ExitCode: 1, Error: "pedido inválido: " + err.Error()})
		return
	}
	writeJSON(w, runMicroVM(r.Context(), in))
}

// runMicroVM arranca uma microVM dedicada, entrega a tool call por vsock e devolve o Result.
func runMicroVM(ctx context.Context, in wire.ExecInput) wire.Result {
	id := fmt.Sprintf("%d-%s-%s", seq.Add(1), sanitize(in.RunID), sanitize(in.StepID))
	work := filepath.Join(runDir, id)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return wire.Result{ExitCode: 1, Error: err.Error()}
	}
	defer os.RemoveAll(work)

	vsockUDS := filepath.Join(work, "v.sock")
	cfgPath := filepath.Join(work, "cfg.json")
	if err := os.WriteFile(cfgPath, fcConfig(vsockUDS), 0o644); err != nil {
		return wire.Result{ExitCode: 1, Error: err.Error()}
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, fcBin, "--no-api", "--config-file", cfgPath)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr // serial da microVM → logs do orchestrator
	if err := cmd.Start(); err != nil {
		return wire.Result{ExitCode: 1, Error: "arranque do firecracker: " + err.Error()}
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	conn, br, err := vsockDial(vsockUDS, wire.VsockPort, 15*time.Second)
	if err != nil {
		return wire.Result{ExitCode: 1, Error: "vsock: " + err.Error()}
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(in); err != nil {
		return wire.Result{ExitCode: 1, Error: "envio ao guest: " + err.Error()}
	}
	var res wire.Result
	if err := json.NewDecoder(br).Decode(&res); err != nil {
		return wire.Result{ExitCode: 1, Error: "resposta do guest: " + err.Error()}
	}
	return res
}

// fcConfig é o JSON de arranque do Firecracker: kernel + rootfs read-only + vsock, 1 vCPU/128MiB.
// O init é o guest-agent; sem rede (a tool de referência não faz egress — AOS-067 por omissão).
func fcConfig(vsockUDS string) []byte {
	cfg := map[string]any{
		"boot-source": map[string]any{
			"kernel_image_path": kernelPath,
			"boot_args":         "console=ttyS0 reboot=k panic=1 pci=off i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd init=/init root=/dev/vda ro",
		},
		"drives": []any{map[string]any{
			"drive_id": "rootfs", "path_on_host": rootfsPath,
			"is_root_device": true, "is_read_only": true,
		}},
		"machine-config": map[string]any{"vcpu_count": 1, "mem_size_mib": 128},
		"vsock":          map[string]any{"guest_cid": 3, "uds_path": vsockUDS},
	}
	b, _ := json.Marshal(cfg)
	return b
}

// vsockDial implementa o protocolo host→guest do vsock do Firecracker: liga ao uds, envia
// "CONNECT <port>\n", espera "OK ...\n". Repete até o guest estar a escutar (a microVM ainda a
// arrancar) ou expirar. Devolve a conn e o bufio.Reader (que pode ter bytes da resposta).
func vsockDial(uds string, port uint32, timeout time.Duration) (net.Conn, *bufio.Reader, error) {
	deadline := time.Now().Add(timeout)
	for {
		if c, err := net.Dial("unix", uds); err == nil {
			if _, werr := fmt.Fprintf(c, "CONNECT %d\n", port); werr == nil {
				br := bufio.NewReader(c)
				if line, rerr := br.ReadString('\n'); rerr == nil && strings.HasPrefix(line, "OK") {
					return c, br, nil
				}
			}
			_ = c.Close()
		}
		if time.Now().After(deadline) {
			return nil, nil, fmt.Errorf("timeout à espera do guest-agent (microVM não escutou o vsock)")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			return r
		default:
			return '_'
		}
	}, s)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func logf(f string, a ...any) { fmt.Fprintf(os.Stderr, "[fc-orch] "+f+"\n", a...) }
func fatal(err error)         { fmt.Fprintln(os.Stderr, "[fc-orch] FATAL:", err); os.Exit(1) }
