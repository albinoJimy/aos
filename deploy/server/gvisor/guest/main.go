// Comando guest — o que corre DENTRO do sandbox gVisor.
//
// Recebe uma ToolCall em JSON no stdin e devolve um Result em JSON no stdout. Nada mais é
// escrito no stdout: o componente lê-o como o resultado, e ruído aí seria indistinguível de
// dados. Diagnóstico vai para stderr.
//
// É o irmão do guest-agent do Firecracker, com uma diferença: lá o guest é o init de uma
// microVM e fala por vsock; aqui é um processo comum dentro do sandbox e fala por stdin/stdout.
// O CONTRATO é o mesmo, de propósito.
//
// ⚠️ A contenção real é a interposição de syscalls do runsc, não este código. A verificação de
// path abaixo é defesa em profundidade — a raiz semeada é montada read-only e o processo não
// tem rede nem capabilities — mas não é ela que segura a fronteira.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// seedRoot é a raiz read-only semeada, montada pelo componente. Fora dela não há nada a ler.
var seedRoot = envOr("AOS_SEED_ROOT", "/seed")

func envOr(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}

func main() {
	var in execInput
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		emit(result{ExitCode: 1, Error: "guest: input invalido: " + err.Error()})
		return
	}
	out, code, err := run(in.Call)
	r := result{Stdout: out, ExitCode: code}
	if err != nil {
		r.Error = err.Error()
	}
	emit(r)
}

// run executa o verbo. O Command vem do registry TRUSTED do nó, nunca do modelo — por isso o
// default é recusar em vez de tentar interpretar.
func run(call toolCall) (stdout []byte, exitCode int, err error) {
	switch call.Command {
	case "read":
		rel := strings.TrimPrefix(filepath.Clean("/"+call.Path), "/")
		full := filepath.Join(seedRoot, rel)
		if full != seedRoot && !strings.HasPrefix(full, seedRoot+string(os.PathSeparator)) {
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

func emit(r result) {
	if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
		fmt.Fprintln(os.Stderr, "[guest] falha a escrever o resultado:", err)
		os.Exit(1)
	}
}
