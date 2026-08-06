package main

// SEAM DO NÓ PARA O FIRECRACKER REAL (AOS-064). O sandbox.FirecrackerDriver é um skeleton: a
// orquestração real da microVM (jailer + API socket + KVM) está FORA do módulo Go do nó por
// desenho (ADR-017, zero-dep). Este ficheiro liga o nó a essa integração SEM a importar: um
// sandbox.GuestExecutor que fala por HTTP stdlib com o COMPONENTE EXTERNO
// (deploy/node/dev-hardened/firecracker/orchestrator), exactamente como o
// RemoteDeviceAttestationVerifier fala com o serviço de attestation. O nó continua a só depender
// de stdlib + substrate/sandbox.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aos-ref/substrate/sandbox"
)

// remoteFirecrackerExecutor implementa [sandbox.GuestExecutor] delegando a execução da microVM no
// orchestrator externo. Injectado no [sandbox.FirecrackerDriver] via WithFirecrackerExecutor.
type remoteFirecrackerExecutor struct {
	url    string
	client *http.Client
}

// fcToolCall / fcExecInput / fcResult espelham o contrato JSON do orchestrator (o seu package
// wire). São LOCAIS ao nó DE PROPÓSITO: importar o módulo do orchestrator puxaria a dependência
// de vsock para dentro do binário do nó, quebrando o zero-dep (ADR-017).
type fcToolCall struct {
	ToolID  string   `json:"tool_id"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Path    string   `json:"path,omitempty"`
	Write   []byte   `json:"write,omitempty"`
}

type fcExecInput struct {
	RunID  string     `json:"run_id"`
	StepID string     `json:"step_id"`
	Call   fcToolCall `json:"call"`
}

type fcResult struct {
	Stdout   []byte `json:"stdout,omitempty"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

// RunInGuest envia a tool call ao orchestrator e devolve o resultado. Um erro de transporte/HTTP
// ou um erro reportado pelo guest propaga-se — o [sandbox.FirecrackerDriver] impõe o taint
// untrusted no [sandbox.ExecResult] a jusante (o resultado nunca é trusted por esta via).
func (e *remoteFirecrackerExecutor) RunInGuest(ctx context.Context, inst sandbox.Instance, call sandbox.ToolCall) ([]byte, []sandbox.Artifact, int, error) {
	body, err := json.Marshal(fcExecInput{
		RunID: inst.ID,
		Call:  fcToolCall{ToolID: call.ToolID, Command: call.Command, Args: call.Args, Path: call.Path, Write: call.Write},
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
		return nil, nil, 1, fmt.Errorf("orchestrator firecracker: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, 1, fmt.Errorf("orchestrator firecracker: estado HTTP %d", resp.StatusCode)
	}
	var r fcResult
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, nil, 1, fmt.Errorf("orchestrator firecracker: resposta inválida: %w", err)
	}
	if r.Error != "" {
		return r.Stdout, nil, r.ExitCode, errors.New(r.Error)
	}
	return r.Stdout, nil, r.ExitCode, nil
}

var _ sandbox.GuestExecutor = (*remoteFirecrackerExecutor)(nil)

// buildSandboxDriver constrói o driver de sandbox do kind pedido. Para firecracker, quando
// AOS_SANDBOX_FIRECRACKER_URL está definido, INJECTA o executor remoto (o componente externo que
// conduz o firecracker REAL sobre KVM) — e a execução deixa de ser ErrDriverUnavailable. Sem a
// URL, firecracker devolve o skeleton (ErrDriverUnavailable no exec: o gap honesto, o executor
// não está provisionado). fake/gvisor vêm de [sandbox.NewDriver].
func buildSandboxDriver(kind sandbox.DriverKind) (sandbox.SandboxDriver, error) {
	if kind == sandbox.DriverFirecracker {
		if url := strings.TrimSpace(os.Getenv("AOS_SANDBOX_FIRECRACKER_URL")); url != "" {
			exec := &remoteFirecrackerExecutor{url: url, client: &http.Client{Timeout: 60 * time.Second}}
			return sandbox.NewFirecrackerDriver(sandbox.WithFirecrackerExecutor(exec)), nil
		}
	}
	return sandbox.NewDriver(kind)
}
