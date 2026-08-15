package main

// SEAM DO NÓ PARA O gVISOR REAL (AOS-064). Irmão de firecrackerexecutor.go, e pelas MESMAS
// razões: o sandbox.GVisorDriver é um skeleton, a orquestração real (runsc, bundles OCI,
// interposição de syscalls) vive FORA do módulo Go do nó por desenho (ADR-017, zero-dep), e o nó
// liga-se a ela por HTTP stdlib contra um COMPONENTE EXTERNO.
//
// ─── PORQUE EXISTE, ao lado do Firecracker ───────────────────────────────────────────────────
// O Firecracker exige `/dev/kvm`. Num host que é ele próprio um convidado sem virtualização
// aninhada — o caso de um VPS típico — esse caminho está FECHADO, e a alternativa não é config:
// o driver gVisor tinha a porta (`WithGVisorExecutor`) mas ninguém a injectava, pelo que
// `AOS_SANDBOX_DRIVER=gvisor` devolvia ErrDriverUnavailable no exec.
//
// O gVisor NÃO precisa de KVM: as plataformas `systrap` e `ptrace` interpõem syscalls em
// user-space. A fronteira é diferente da do Firecracker (kernel do guest em Go, não uma microVM
// com KVM) e isso deve ser lido como o que é — mais forte do que um jail in-process, mais fraca
// do que virtualização de hardware.
//
// O contrato de fio é o MESMO do orchestrator do Firecracker (uma tool call entra, um resultado
// sai), de propósito: o nó não sabe qual dos dois está do outro lado, e trocar de driver é
// topologia, não código.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/aos-ref/substrate/sandbox"
)

// remoteGVisorExecutor implementa [sandbox.GuestExecutor] delegando a execução no componente
// externo que conduz o runsc. Injectado no [sandbox.GVisorDriver] via WithGVisorExecutor.
type remoteGVisorExecutor struct {
	url    string
	client *http.Client
}

// gvToolCall / gvExecInput / gvResult espelham o contrato JSON do componente. São LOCAIS ao nó
// DE PROPÓSITO, tal como os equivalentes do Firecracker: importar o módulo do componente puxaria
// as suas dependências para dentro do binário do nó e quebraria o zero-dep (ADR-017).
type gvToolCall struct {
	ToolID  string   `json:"tool_id"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Path    string   `json:"path,omitempty"`
	Write   []byte   `json:"write,omitempty"`
}

type gvExecInput struct {
	RunID  string     `json:"run_id"`
	StepID string     `json:"step_id"`
	Call   gvToolCall `json:"call"`
}

type gvResult struct {
	Stdout   []byte `json:"stdout,omitempty"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

// RunInGuest envia a tool call ao componente e devolve o resultado.
//
// O erro de transporte, o HTTP != 200 e o erro reportado pelo guest propagam-se TODOS: o
// [sandbox.GVisorDriver] impõe o taint untrusted no [sandbox.ExecResult] a jusante, pelo que
// nada por esta via é tratado como confiável. Um componente indisponível NEGA a execução — nunca
// degrada para uma execução no host.
func (e *remoteGVisorExecutor) RunInGuest(ctx context.Context, inst sandbox.Instance, call sandbox.ToolCall) ([]byte, []sandbox.Artifact, int, error) {
	body, err := json.Marshal(gvExecInput{
		RunID: inst.ID,
		Call:  gvToolCall{ToolID: call.ToolID, Command: call.Command, Args: call.Args, Path: call.Path, Write: call.Write},
	})
	if err != nil {
		return nil, nil, 1, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, 1, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, nil, 1, fmt.Errorf("componente gvisor: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, 1, fmt.Errorf("componente gvisor: estado HTTP %d", resp.StatusCode)
	}
	var r gvResult
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, nil, 1, fmt.Errorf("componente gvisor: resposta inválida: %w", err)
	}
	if r.Error != "" {
		return r.Stdout, nil, r.ExitCode, errors.New(r.Error)
	}
	return r.Stdout, nil, r.ExitCode, nil
}

var _ sandbox.GuestExecutor = (*remoteGVisorExecutor)(nil)
