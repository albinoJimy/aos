package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// injectingResolver modela o broker server-side (ADR-006): recebe o HANDLE opaco,
// resolve o segredo do lado seguro e injecta-o no env do guest. O segredo NUNCA é
// devolvido ao chamador. Regista o handle recebido (nunca o segredo em log).
type injectingResolver struct {
	driver  *FakeDriver
	secrets map[string]string // handle → segredo (só do lado seguro)
	seen    []string          // handles observados (asserção; sem segredos)
}

func (r *injectingResolver) Inject(_ context.Context, handle string, inst Instance) error {
	r.seen = append(r.seen, handle)
	secret, ok := r.secrets[handle]
	if !ok {
		return fmt.Errorf("handle desconhecido: %s", handle) // sem segredo no erro
	}
	// Injecção server-side: o segredo entra no env privado do guest, nunca sai.
	r.driver.InjectEnv(inst, "DOWNSTREAM_TOKEN", secret)
	return nil
}

// TestCredentials_HandleNeverRevealsSecret prova o critério: o credentials_handle
// nunca revela o segredo — o segredo não aparece no resultado, nos eventos nem nos
// spans (ADR-006).
func TestCredentials_HandleNeverRevealsSecret(t *testing.T) {
	const handle = "cred-handle-abc123"
	const secret = "super-secret-downstream-value-xyz"

	store := newStore(t)
	fake := NewFakeDriver()
	resolver := &injectingResolver{driver: fake, secrets: map[string]string{handle: secret}}
	tracer := &recordingTracer{}
	launcher, err := NewLauncher(fake,
		WithEventSink(NewEventStoreSink(store)),
		WithCredentialInjector(resolver),
		WithTracer(tracer),
	)
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	rm := newPermitMonitor(store)
	ml, err := NewMediatedLauncher(rm, launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}

	res, err := ml.Execute(context.Background(), defaultAuthz(), ExecRequest{
		RunID: "run-cred", StepID: "step-cred",
		Call:              ToolCall{ToolID: "call-api", Command: "curl", Args: []string{"api"}},
		CredentialsHandle: handle,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// O injector viu o HANDLE (não o segredo).
	if len(resolver.seen) != 1 || resolver.seen[0] != handle {
		t.Fatalf("injector viu %v, esperado [%q]", resolver.seen, handle)
	}

	// 1) O segredo não está no resultado (stdout/artefactos).
	if bytes.Contains(res.Stdout, []byte(secret)) {
		t.Fatal("segredo no stdout do resultado")
	}
	for _, a := range res.Artifacts {
		if bytes.Contains(a.Data, []byte(secret)) {
			t.Fatal("segredo num artefacto do resultado")
		}
	}

	// 2) O segredo não está em NENHUM evento do Event Store.
	for _, e := range readEvents(t, store, "run-cred") {
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), secret) {
			t.Fatalf("segredo presente no evento %s", e.Type)
		}
	}

	// 3) O segredo não está em NENHUM atributo de span.
	for _, v := range tracer.attrValues() {
		if s, ok := v.(string); ok && strings.Contains(s, secret) {
			t.Fatal("segredo presente num atributo de span")
		}
	}

	// 4) O HANDLE (opaco, não-secreto) PODE aparecer para correlação — confirma que
	// a via de credencial foi exercitada sem expor o segredo.
	foundHandle := false
	for _, e := range readEvents(t, store, "run-cred") {
		var p lifecyclePayload
		if json.Unmarshal(e.Payload, &p) == nil && p.CredentialsHandle == handle {
			foundHandle = true
		}
	}
	if !foundHandle {
		t.Fatal("handle opaco ausente dos eventos (esperado para correlacao)")
	}
}

// TestCredentials_HandleOptional prova que sem injector o handle é apenas
// propagado (opaco) e a execução corre na mesma.
func TestCredentials_HandleOptional(t *testing.T) {
	store := newStore(t)
	launcher, err := NewLauncher(NewFakeDriver(), WithEventSink(NewEventStoreSink(store)))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	res, err := launcher.run(context.Background(), ExecRequest{
		RunID: "r", StepID: "s", Call: ToolCall{Command: "ok"}, CredentialsHandle: "h-1",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.IsUntrusted() {
		t.Fatal("resultado nao untrusted")
	}
}
