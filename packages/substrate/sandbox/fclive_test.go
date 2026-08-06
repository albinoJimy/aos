//go:build fclive

package sandbox

// Teste AO VIVO (build-tag fclive): conduz uma microVM Firecracker REAL pelo caminho canónico do
// nó — MediatedLauncher → RM (permit) → FirecrackerDriver → GuestExecutor → orchestrator → microVM
// (KVM) → lê o documento semeado → devolve. Exige KVM e um orchestrator a correr (FC_ORCH_URL).
// NÃO corre na CI normal (sem KVM/orchestrator): é a prova de integração ponta-a-ponta manual.
//
//	go test -tags fclive -run TestFCLive_RealMicroVM ./...   (com FC_ORCH_URL definido)

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"
)

// httpGuestExecutor é o MESMO shape do remoteFirecrackerExecutor do nó (stdlib HTTP → orchestrator).
type httpGuestExecutor struct{ url string }

func (e httpGuestExecutor) RunInGuest(ctx context.Context, inst Instance, call ToolCall) ([]byte, []Artifact, int, error) {
	body, _ := json.Marshal(map[string]any{
		"run_id": inst.ID,
		"call": map[string]any{
			"tool_id": call.ToolID, "command": call.Command,
			"path": call.Path, "args": call.Args, "write": call.Write,
		},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return nil, nil, 1, err
	}
	defer resp.Body.Close()
	var r struct {
		Stdout   []byte `json:"stdout"`
		ExitCode int    `json:"exit_code"`
		Error    string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, nil, 1, err
	}
	if r.Error != "" {
		return r.Stdout, nil, r.ExitCode, errors.New(r.Error)
	}
	return r.Stdout, nil, r.ExitCode, nil
}

func TestFCLive_RealMicroVM(t *testing.T) {
	url := os.Getenv("FC_ORCH_URL")
	if url == "" {
		t.Skip("FC_ORCH_URL não definido (precisa de um orchestrator firecracker a correr)")
	}
	ctx := context.Background()
	store := newStore(t)
	rm := newPermitMonitor(store)

	// Driver firecracker REAL com o executor a apontar ao orchestrator (microVM sobre KVM).
	driver := NewFirecrackerDriver(WithFirecrackerExecutor(httpGuestExecutor{url: url}))
	launcher, err := NewLauncher(driver, WithEventSink(NewEventStoreSink(store)))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	ml, err := NewMediatedLauncher(rm, launcher, "doc_read")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}

	res, err := ml.Execute(ctx, defaultAuthz(), ExecRequest{
		RunID: "run-fc-live", StepID: "s1",
		Call: ToolCall{ToolID: "doc_read", Command: "read", Path: "notes"},
	})
	if err != nil {
		t.Fatalf("Execute (microVM real): %v", err)
	}
	want := "Reuniao 3a: rever o plano de migracao. Owner: alice."
	got := string(bytes.TrimSpace(res.Stdout))
	if got != want {
		t.Fatalf("conteúdo da microVM inesperado: %q", res.Stdout)
	}
	if res.Taint() != TaintUntrusted {
		t.Fatalf("resultado da microVM devia ser untrusted por tipo, veio %v", res.Taint())
	}
	t.Logf("\n"+
		"  CAMINHO   : MediatedLauncher -> RM(permit) -> FirecrackerDriver -> GuestExecutor -> orchestrator\n"+
		"  microVM   : firecracker/KVM real, dedicada, isolada por hardware\n"+
		"  OUTPUT    : %s\n"+
		"  TAINT     : %v (untrusted por tipo)",
		got, res.Taint())
}
