package mcp

import (
	"context"
	"fmt"
	"io"
	"os/exec"
)

// OSSandboxLauncher é a impl de REFERÊNCIA de [SandboxLauncher]. Lança o servidor
// como subprocesso via os/exec com o isolamento MÍNIMO que uma impl sem microVM pode
// garantir e que DOCUMENTA a fronteira que EPIC-07 (AOS-064, Firecracker/gVisor)
// endurecerá:
//
//   - AMBIENTE NÃO HERDADO: cmd.Env = spec.Env (vazio por omissão). O processo NÃO
//     recebe o ambiente do host — sem segredos, sem variáveis que exponham sockets.
//   - SEM DESCRITORES EXTRA: só se ligam os pipes stdin/stdout. cmd.ExtraFiles fica
//     nil, pelo que NENHUM socket/descritor do host é passado ao filho.
//   - stderr descartado (não é canal de protocolo; evita ruído/vazamento no host).
//
// NÃO é uma microVM: o isolamento de kernel/rede/FS é EPIC-07. Esta impl serve o
// contrato da porta e prova, em teste, que o STDIO passa SEMPRE por aqui (nunca há
// execução directa fora da porta de sandbox).
type OSSandboxLauncher struct{}

// Launch implementa [SandboxLauncher].
func (OSSandboxLauncher) Launch(ctx context.Context, spec LaunchSpec) (SandboxProcess, error) {
	if spec.Command == "" {
		return nil, fmt.Errorf("%w: comando vazio", ErrHandshakeFailed)
	}
	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
	// Ambiente EXPLÍCITO — nunca o do host. Env nil em cmd significaria "herda o do
	// host"; forçamos um slice não-nil (mesmo que vazio) para cortar essa herança.
	if spec.Env != nil {
		cmd.Env = spec.Env
	} else {
		cmd.Env = []string{}
	}
	// Sem ExtraFiles: nenhum descritor/socket do host atravessa para o filho.
	cmd.ExtraFiles = nil

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: stdin pipe: %v", ErrHandshakeFailed, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: stdout pipe: %v", ErrHandshakeFailed, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: start: %v", ErrHandshakeFailed, err)
	}
	return &osProcess{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}

// osProcess é o [SandboxProcess] sobre um subprocesso os/exec.
type osProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

func (p *osProcess) Stdin() interface{ Write([]byte) (int, error) } { return p.stdin }
func (p *osProcess) Stdout() interface{ Read([]byte) (int, error) } { return p.stdout }

// Close fecha o stdin (sinaliza EOF ao servidor), termina o processo e faz reap.
func (p *osProcess) Close() error {
	_ = p.stdin.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	// Wait faz reap do processo e liberta recursos; ignoramos o erro de "killed".
	_ = p.cmd.Wait()
	_ = p.stdout.Close()
	return nil
}
