// guest-agent corre como PID 1 (init) DENTRO da microVM Firecracker. Serve UMA tool call por
// vsock e desliga a VM. É o único processo do guest: não há shell, rede, nem socket do host — a
// fronteira é hardware (kernel do guest separado). O binário é estático (CGO desligado).
//
// Ciclo: monta devtmpfs (para /dev/vsock) + proc → escuta AF_VSOCK na porta acordada → aceita 1
// ligação → descodifica o ExecInput → executa → devolve o Result → poweroff. Qualquer falha ⇒
// poweroff na mesma (o defer), para a microVM nunca ficar pendurada.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/aos-ref/deploy/firecracker/wire"
	"github.com/mdlayher/vsock"
)

// seedRoot é o RootFS BASE read-only (AOS-066) semeado no rootfs pelo build: as tools de leitura
// resolvem os seus paths AQUI, nunca no resto do sistema de ficheiros do guest.
const seedRoot = "/seed"

// dbg imprime no serial (console) — visível nos logs do orchestrator. Diagnóstico do arranque.
func dbg(f string, a ...any) { fmt.Fprintf(os.Stdout, "[guest-agent] "+f+"\n", a...) }

func main() {
	// devtmpfs dá /dev/vsock (que o mdlayher/vsock precisa para o CID local); proc é higiene.
	errDev := syscall.Mount("dev", "/dev", "devtmpfs", 0, "")
	_ = syscall.Mount("proc", "/proc", "proc", 0, "")
	_, statErr := os.Stat("/dev/vsock")
	dbg("arranque: mount devtmpfs err=%v; /dev/vsock stat err=%v", errDev, statErr)
	defer poweroff() // a microVM desliga-se em QUALQUER caminho de saída.

	l, err := vsock.Listen(wire.VsockPort, nil)
	if err != nil {
		dbg("vsock.Listen(%d) FALHOU: %v", wire.VsockPort, err)
		return
	}
	defer l.Close()
	dbg("a escutar vsock na porta %d; à espera de ligação...", wire.VsockPort)

	conn, err := l.Accept()
	if err != nil {
		dbg("Accept FALHOU: %v", err)
		return
	}
	defer conn.Close()
	dbg("ligação aceite")

	var in wire.ExecInput
	if err := json.NewDecoder(conn).Decode(&in); err != nil {
		dbg("decode do pedido FALHOU: %v", err)
		_ = json.NewEncoder(conn).Encode(wire.Result{ExitCode: 1, Error: "decode: " + err.Error()})
		return
	}
	dbg("pedido: cmd=%q path=%q", in.Call.Command, in.Call.Path)
	stdout, code, execErr := run(in.Call)
	res := wire.Result{Stdout: stdout, ExitCode: code}
	if execErr != nil {
		res.Error = execErr.Error()
		if res.ExitCode == 0 {
			res.ExitCode = 1
		}
	}
	if err := json.NewEncoder(conn).Encode(res); err != nil {
		dbg("envio da resposta FALHOU: %v", err)
		return
	}
	dbg("resposta enviada (%d bytes stdout, exit=%d); à espera do fecho do host", len(stdout), res.ExitCode)
	// Espera o host FECHAR a ligação (lê até EOF) ANTES do poweroff. Sem isto, o poweroff pode
	// destruir a microVM antes de a resposta drenar pelas filas do virtio-vsock — a race que dava
	// "EOF" no host. Ler até EOF garante que o host recebeu tudo e fechou.
	_, _ = io.Copy(io.Discard, conn)
}

// run executa a tool call. O Command é FIXO (do registry trusted); "read" devolve o conteúdo de
// um ficheiro semeado, com contenção de path (o isolamento REAL é a microVM, isto é higiene).
func run(call wire.ToolCall) (stdout []byte, exitCode int, err error) {
	switch call.Command {
	case "read":
		rel := strings.TrimPrefix(filepath.Clean("/"+call.Path), "/")
		full := filepath.Join(seedRoot, rel)
		if full != seedRoot && !strings.HasPrefix(full, seedRoot+"/") {
			return nil, 1, errors.New("path foge da raiz semeada")
		}
		b, rerr := os.ReadFile(full)
		if rerr != nil {
			return nil, 1, rerr
		}
		return b, 0, nil
	default:
		return nil, 1, errors.New("comando desconhecido: " + call.Command)
	}
}

// poweroff sincroniza e desliga a microVM (PID 1 tem CAP_SYS_BOOT). O firecracker termina.
func poweroff() {
	syscall.Sync()
	_ = syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
}
