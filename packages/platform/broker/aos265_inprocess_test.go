package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// AOS-265 — a porta de aquisição com contexto (consumo in-process, D8) exercida pela
// CADEIA REAL do broker: o mesmo Reference Monitor (com o ScopeGate + EventSink
// durável no Event Store), o Vault de referência e o broker do [newStack]. Prova as
// quatro invariantes do ticket:
//   - Principal/run transportados; recordExchange DISTINGUE runs (partição por RunID);
//   - troca negada ⇒ falha RUÍDOSA atribuída (*DeniedError), NUNCA bearer vazio;
//   - o bearer chega ao PROCESSO (Reveal), nunca ao agente, e REDIGE-se em log/JSON;
//   - lease revogada ⇒ o consumo in-process é cortado fail-closed.

// TestAcquireInProcess_BearerAoProcesso_RunsDistintos cobre o caminho feliz pela
// cadeia real e a distinção de runs no registo de troca.
func TestAcquireInProcess_BearerAoProcesso_RunsDistintos(t *testing.T) {
	st := newStack(t, time.Minute)
	ctx := context.Background()

	credA, err := st.broker.AcquireInProcess(ctx, request("run-A", provInScopeCap))
	if err != nil {
		t.Fatalf("AcquireInProcess run-A: %v", err)
	}
	credB, err := st.broker.AcquireInProcess(ctx, request("run-B", provInScopeCap))
	if err != nil {
		t.Fatalf("AcquireInProcess run-B: %v", err)
	}

	// O PROCESSO obtém o bearer (a única egress controlada). O agente nunca teve via.
	if credA.Reveal() != sentinel {
		t.Fatalf("run-A: o processo nao recebeu o bearer via Reveal()")
	}
	if credB.Reveal() != sentinel {
		t.Fatalf("run-B: o processo nao recebeu o bearer via Reveal()")
	}
	if credA.IsZero() || credB.IsZero() {
		t.Fatal("credencial zero num retorno de sucesso")
	}

	// REDACÇÃO no processo (ADR-006): nenhuma superfície de fmt/JSON serializa o valor.
	for _, s := range []string{
		credA.String(),
		fmt.Sprintf("%v", credA),
		fmt.Sprintf("%#v", credA),
	} {
		if strings.Contains(s, sentinel) {
			t.Fatalf("SEGREDO numa superficie de fmt: %q", s)
		}
	}
	if raw, _ := json.Marshal(credA); strings.Contains(string(raw), sentinel) {
		t.Fatalf("SEGREDO no JSON da credencial: %s", raw)
	}

	// recordExchange DISTINGUE runs: cada run tem o seu registo de troca, na sua
	// partição do Event Store (stream = RunID). run-A não vê o de run-B e vice-versa.
	assertOneExchangeRecord(t, st, "run-A")
	assertOneExchangeRecord(t, st, "run-B")

	// e o valor NUNCA aparece em nenhum evento (agente/log/span/Event Store).
	for _, run := range []string{"run-A", "run-B"} {
		for _, e := range readStream(t, st.es, run) {
			if raw, _ := json.Marshal(e); strings.Contains(string(raw), sentinel) {
				t.Fatalf("SEGREDO no Event Store (%s): %s", run, raw)
			}
		}
	}
}

// assertOneExchangeRecord confirma que o run tem EXACTAMENTE um registo de troca
// (credential.exchange.issued), com quem/para quê NÃO-SECRETOS — a prova de que
// recordExchange atribui a troca ao run certo.
func assertOneExchangeRecord(t *testing.T, st *stack, run string) {
	t.Helper()
	n := 0
	for _, e := range readStream(t, st.es, run) {
		if e.Type != exchangeEventType {
			continue
		}
		n++
		var pay exchangePayload
		if err := json.Unmarshal(e.Payload, &pay); err != nil {
			t.Fatalf("%s: payload de troca: %v", run, err)
		}
		if pay.PrincipalNHI != nhiID || pay.Capability != provInScopeCap {
			t.Errorf("%s: registo sem quem/para quê: %+v", run, pay)
		}
	}
	if n != 1 {
		t.Fatalf("%s: esperado 1 registo de troca, obtido %d", run, n)
	}
}

// TestAcquireInProcess_Negada_FailClosedAtribuida cobre a invariante crítica: uma
// troca negada pela mediação (aqui o ScopeGate, capability fora do escopo efectivo)
// devolve o *DeniedError ATRIBUÍDO e NENHUMA credencial — nunca um bearer vazio
// disfarçado de sucesso, nem um registo de troca selado.
func TestAcquireInProcess_Negada_FailClosedAtribuida(t *testing.T) {
	st := newStack(t, time.Minute)

	// classScopedCap está no escopo da CLASSE mas não na autoridade do UTILIZADOR
	// canónico (só charge) ⇒ o ScopeGate nega na mediação.
	cred, err := st.broker.AcquireInProcess(context.Background(), request("run-deny", classScopedCap))

	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("esperado *DeniedError atribuido, obtido %v", err)
	}
	if denied.Reason == "" && denied.Code == "" {
		t.Error("negacao sem atribuicao (nem razao nem codigo)")
	}
	if !cred.IsZero() || cred.Reveal() != "" {
		t.Fatalf("BEARER emitido numa troca negada: %q", cred.Reveal())
	}

	// nenhuma troca foi selada para o run negado.
	for _, e := range readStream(t, st.es, "run-deny") {
		if e.Type == exchangeEventType {
			t.Fatal("registo de troca selado apesar da negacao")
		}
	}
}

// TestAcquireInProcess_SemMaterial_FailClosed: pedido IN-SCOPE mas o Vault não tem
// material ⇒ ErrNoMaterial, sem credencial (nunca bearer vazio silencioso).
func TestAcquireInProcess_SemMaterial_FailClosed(t *testing.T) {
	st := newStack(t, time.Minute)
	st.vault.Remove(VaultKey{Provider: provider, Region: region, Capability: provInScopeCap})

	cred, err := st.broker.AcquireInProcess(context.Background(), request("run-1", provInScopeCap))
	if !errors.Is(err, ErrNoMaterial) {
		t.Fatalf("esperado ErrNoMaterial, obtido %v", err)
	}
	if !cred.IsZero() {
		t.Fatalf("credencial emitida apesar de fail-closed: %q", cred.Reveal())
	}
}

// TestInProcessResolution_LeaseRevogada_CortaConsumo prova que o consumo IN-PROCESS
// honra a revogação: o MESMO caminho de resolução ([Lease.injectInto] para o
// [inProcessSink]) que [Broker.AcquireInProcess] usa recusa entregar o valor a uma
// lease revogada — o corte de [Broker.Revoke] aplica-se ao sink in-process, não só à
// injecção na sandbox.
func TestInProcessResolution_LeaseRevogada_CortaConsumo(t *testing.T) {
	st := newStack(t, time.Minute)
	ctx := context.Background()

	// aquisição feliz para obter o handle/lease.
	cred, err := st.broker.AcquireInProcess(ctx, request("run-1", provInScopeCap))
	if err != nil {
		t.Fatalf("AcquireInProcess: %v", err)
	}

	// revoga por id NÃO-SECRETO (o corte central do broker).
	if !st.broker.Revoke(cred.LeaseID()) {
		t.Fatalf("Revoke(%q) devolveu false", cred.LeaseID())
	}

	// re-resolver o MESMO handle pelo caminho in-process tem de falhar fail-closed,
	// sem entregar o valor.
	lease, ok := st.broker.store.get(cred.Handle())
	if !ok {
		t.Fatal("lease sumiu do store antes da revogacao ser testada")
	}
	var sink inProcessSink
	if err := lease.injectInto(&sink, st.clock.now()); !errors.Is(err, ErrLeaseRevoked) {
		t.Fatalf("esperado ErrLeaseRevoked no consumo in-process, obtido %v", err)
	}
	if sink.v != "" {
		t.Fatal("valor entregue a um sink apesar da lease revogada")
	}
}

// verifica em compile-time que o inProcessSink satisfaz a porta de sink server-side —
// a mesma [GuestSink] da injecção, garantindo que o consumo in-process passa pela
// única saída do valor ([internal/vault.Secret.DeliverTo]).
var _ GuestSink = (*inProcessSink)(nil)
