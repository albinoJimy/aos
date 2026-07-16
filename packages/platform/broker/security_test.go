package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/substrate/sandbox"
)

// TestSeguranca_SegredoNuncaObservavel é o teste central de ADR-006: após um
// fluxo completo (troca + injecção), o valor do segredo NÃO aparece em nenhuma
// superfície observável pelo agente — output da troca, redação dos portadores, ou
// eventos do Event Store. O único sítio onde o valor existe é server-side, entre o
// Vault e o guest de injecção.
func TestSeguranca_SegredoNuncaObservavel(t *testing.T) {
	st := newStack(t, time.Minute)
	inj, err := st.broker.NewInjector(st.guest)
	if err != nil {
		t.Fatalf("NewInjector: %v", err)
	}

	ctx := context.Background()
	h, err := st.broker.Exchange(ctx, request("run-1", provInScopeCap))
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if err := inj.Inject(ctx, string(h), sandbox.Instance{ID: "vm-1"}); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	// scan é o predicado de fuga: falha se o sentinel aparecer.
	scan := func(where, s string) {
		t.Helper()
		if strings.Contains(s, sentinel) {
			t.Fatalf("SEGREDO observavel em %s: %s", where, s)
		}
	}

	// 1) output da troca (o que o agente recebe).
	scan("handle (output da troca)", string(h))

	// 2) redação dos portadores server-side (String/%v/%s/%#v/JSON).
	lease, ok := st.broker.store.get(h)
	if !ok {
		t.Fatal("lease inexistente")
	}
	scan("Lease.String()", lease.String())
	scan("Lease %v", fmt.Sprintf("%v", lease))
	scan("Lease %#v", fmt.Sprintf("%#v", lease))
	leaseJSON, err := json.Marshal(lease)
	if err != nil {
		t.Fatalf("marshal lease: %v", err)
	}
	scan("Lease JSON", string(leaseJSON))
	// a redação é explícita, não apenas ausência.
	if !strings.Contains(string(leaseJSON), "REDACTED") {
		t.Errorf("Lease JSON sem marca de redacao: %s", leaseJSON)
	}

	// 3) o portador do Vault (Secret) redige em todos os caminhos.
	sec, err := st.vault.Fetch(ctx, VaultKey{Provider: provider, Region: region, Capability: provInScopeCap})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	scan("Secret.String()", sec.String())
	scan("Secret %v", fmt.Sprintf("%v", sec))
	scan("Secret %#v", fmt.Sprintf("%#v", sec))
	secJSON, err := json.Marshal(sec)
	if err != nil {
		t.Fatalf("marshal secret: %v", err)
	}
	scan("Secret JSON", string(secJSON))

	// 4) TODOS os eventos do Event Store (mediação + registo da troca).
	for _, e := range readStream(t, st.es, "run-1") {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		scan("Event Store ("+e.Type+")", string(raw))
	}

	// 5) o guest recebeu a injecção server-side (o destino legítimo), mas não
	//    expõe o valor (a impl de referência não o retém).
	if st.guest.Injections() != 1 {
		t.Fatalf("injeccao server-side nao ocorreu: %d", st.guest.Injections())
	}
}
