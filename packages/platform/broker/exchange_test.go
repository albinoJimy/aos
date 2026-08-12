package broker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

func TestExchange_HappyPath_DevolveHandleOpaco(t *testing.T) {
	st := newStack(t, time.Minute)
	h, err := st.broker.Exchange(context.Background(), request("run-1", provInScopeCap))
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if h == "" {
		t.Fatal("handle vazio")
	}
	if !strings.HasPrefix(string(h), "h-") {
		t.Fatalf("handle sem prefixo opaco: %q", h)
	}
	if strings.Contains(string(h), sentinel) {
		t.Fatalf("SEGREDO no handle: %q", h)
	}
}

// assertHandleOpaco prova ESTRUTURALMENTE (sem depender da ausência de substrings
// curtas num blob aleatório) que o handle é um token opaco de alta entropia e NÃO
// deriva de campos não-secretos. Forma exigida: prefixo "h-" seguido do encoding
// base64url de EXACTAMENTE handleEntropyBytes bytes de entropia crua.
//
// Esta forma é a que apanha a regressão de BRK-01 de maneira DETERMINISTA: o handle
// antigo enumerável ("h-"+leaseID, ex.: "h-lease-stripe-eu-1") NÃO decodifica para
// 16 bytes de base64url — ou o comprimento é inválido, ou o número de bytes não bate
// —, logo falha aqui SEMPRE. Já a verificação antiga por strings.Contains(h, region)
// colidia por acaso ("eu" ⊂ "...bEUz...") tornando o teste flaky. Ao exigir que TODO
// o corpo do handle seja entropia crua de 16 bytes, provamos que nada mais (provider,
// region, contador) está embebido.
func assertHandleOpaco(t *testing.T, h Handle) {
	t.Helper()
	rest, ok := strings.CutPrefix(string(h), "h-")
	if !ok {
		t.Fatalf("handle sem prefixo opaco \"h-\": %q", h)
	}
	raw, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil {
		t.Fatalf("corpo do handle nao e base64url de entropia crua (enumeravel?): %q: %v", h, err)
	}
	if len(raw) != handleEntropyBytes {
		t.Fatalf("handle nao carrega %d bytes de entropia (obteve %d): %q", handleEntropyBytes, len(raw), h)
	}
}

// TestExchange_Handle_NaoAdivinhavel prova o corte de BRK-01: o handle NÃO é
// derivável de campos não-secretos (provider/region/contador) nem sequencial — é
// um token opaco de alta entropia. Como a injecção é autorizada por posse do
// handle, um handle enumerável permitiria usar uma lease fora do escopo.
//
// A não-enumerabilidade é verificada por PROPRIEDADE ESTRUTURAL (ver
// assertHandleOpaco), não por ausência de substrings num blob aleatório — esta
// última colidia por acaso com a região curta "eu" e tornava o teste flaky.
func TestExchange_Handle_NaoAdivinhavel(t *testing.T) {
	st := newStack(t, time.Minute)
	seen := make(map[Handle]struct{})
	for i := 0; i < 8; i++ {
		h, err := st.broker.Exchange(context.Background(), request("run-1", provInScopeCap))
		if err != nil {
			t.Fatalf("Exchange: %v", err)
		}
		// Forma opaca: "h-" + 16 bytes de entropia base64url e NADA mais embebido
		// (nem provider/region, nem o lease-id sequencial). Comprimento fixo implícito.
		assertHandleOpaco(t, h)
		if _, dup := seen[h]; dup {
			t.Fatalf("handle repetido (sem entropia): %q", h)
		}
		seen[h] = struct{}{}
	}
}

// TestNewHandle_OpacoEDeAltaEntropia prova no GERADOR (não no caminho completo) a
// mesma propriedade, de forma determinista e com grande N: newHandle() não recebe
// provider/region/contador, logo — por CONSTRUÇÃO — não os pode embeber; e produz
// tokens opacos de 128 bits distintos. 1000 amostras sem colisão amarram a entropia
// sem qualquer dependência de blobs aleatórios "não conterem" substrings.
func TestNewHandle_OpacoEDeAltaEntropia(t *testing.T) {
	const n = 1000
	seen := make(map[Handle]struct{}, n)
	for i := 0; i < n; i++ {
		h, err := newHandle()
		if err != nil {
			t.Fatalf("newHandle: %v", err)
		}
		assertHandleOpaco(t, h)
		if _, dup := seen[h]; dup {
			t.Fatalf("colisao de handle em %d amostras (entropia insuficiente): %q", n, h)
		}
		seen[h] = struct{}{}
	}
}

// TestExchange_MediadaPeloRM_RegistadaSemValor prova que a troca atravessa o
// Reference Monitor (evento tool.call.mediated com quem/para quê/quando) e que o
// broker sela o registo da troca (credential.exchange.issued com lease-id/handle)
// — ambos SEM o valor do segredo.
func TestExchange_MediadaPeloRM_RegistadaSemValor(t *testing.T) {
	st := newStack(t, time.Minute)
	if _, err := st.broker.Exchange(context.Background(), request("run-1", provInScopeCap)); err != nil {
		t.Fatalf("Exchange: %v", err)
	}

	evs := readStream(t, st.es, "run-1")
	var mediated, issued *eventstore.Event
	for i := range evs {
		switch evs[i].Type {
		case referencemonitor.EventTypeMediated:
			mediated = &evs[i]
		case exchangeEventType:
			issued = &evs[i]
		}
	}
	if mediated == nil {
		t.Fatal("sem evento de mediacao (troca nao mediada pelo RM)")
	}
	if issued == nil {
		t.Fatal("sem evento credential.exchange.issued (troca nao registada)")
	}

	// quem/para quê/quando na mediação, sem valor.
	medJSON := string(mediated.Payload)
	for _, want := range []string{nhiID, provInScopeCap, resourceURL} {
		if !strings.Contains(medJSON, want) {
			t.Errorf("mediacao sem %q: %s", want, medJSON)
		}
	}
	if mediated.Ts == "" {
		t.Error("mediacao sem timestamp (quando)")
	}

	// o registo da troca tem lease-id/handle NÃO-SECRETOS.
	var pay exchangePayload
	if err := json.Unmarshal(issued.Payload, &pay); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if pay.LeaseID == "" || pay.Handle == "" {
		t.Errorf("registo sem lease-id/handle: %+v", pay)
	}
	if pay.PrincipalNHI != nhiID || pay.Capability != provInScopeCap {
		t.Errorf("registo sem quem/para quê: %+v", pay)
	}

	// nenhum evento contém o valor.
	for _, e := range evs {
		if raw, _ := json.Marshal(e); strings.Contains(string(raw), sentinel) {
			t.Fatalf("SEGREDO no Event Store: %s", raw)
		}
	}
}

func TestExchange_SemMaterial_FailClosed(t *testing.T) {
	st := newStack(t, time.Minute)
	// remove o material (revogação central no vault); o pedido é IN-SCOPE.
	st.vault.Remove(VaultKey{Provider: provider, Region: region, Capability: provInScopeCap})

	h, err := st.broker.Exchange(context.Background(), request("run-1", provInScopeCap))
	if !errors.Is(err, ErrNoMaterial) {
		t.Fatalf("esperado ErrNoMaterial, obtido %v", err)
	}
	if h != "" {
		t.Fatalf("handle emitido apesar de fail-closed: %q", h)
	}
}

func TestNew_FailClosed_Construcao(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	rm := referencemonitor.New()
	vlt := NewMemoryVault()

	tests := []struct {
		name    string
		rm      *referencemonitor.Monitor
		vlt     VaultClient
		es      eventstore.EventStore
		opts    []Option
		wantErr error
	}{
		{"nil monitor", nil, vlt, es, nil, ErrNilMonitor},
		{"nil vault", rm, nil, es, nil, ErrNilVault},
		{"nil eventstore", rm, vlt, nil, nil, ErrNilEventStore},
		{"tool id vazio", rm, vlt, es, []Option{WithToolID("")}, ErrEmptyToolID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.rm, tc.vlt, tc.es, tc.opts...)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("esperado %v, obtido %v", tc.wantErr, err)
			}
		})
	}
}

// TestExchange_DefesaServerSide_ForaDeEscopo cobre a camada belt-and-suspenders:
// um broker construído COM WithClassScopes mas SEM o ScopeGate ligado aos hooks do
// RM. O gate não nega na mediação (não está presente), logo a mediação PERMITE e
// despacha; a verificação DEFENSIVA server-side em dispatch é quem nega, devolvendo
// ErrOutOfScope via dec.ToolErr — sem emitir handle e sem registar a troca.
func TestExchange_DefesaServerSide_ForaDeEscopo(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	// RM com os DefaultHooks (permitem o call trusted) mas SEM o ScopeGate do broker.
	rm := referencemonitor.New(
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)),
	)

	vlt := NewMemoryVault()
	// aprovisiona material para a capability FORA de escopo: isola a negação — se
	// chegasse a Fetch falharia por ErrNoMaterial, não por escopo. Assim, um handle
	// só NÃO é emitido por causa da defesa de escopo.
	vlt.Put(VaultKey{Provider: provider, Region: region, Capability: classScopedCap}, sentinel)

	clock := newTestClock()
	b, err := New(rm, vlt, es,
		WithClock(clock.now),
		WithTTL(time.Minute),
		WithClassScopes(defaultClassScopes()),
	)
	if err != nil {
		t.Fatalf("broker.New: %v", err)
	}

	// pedido fora de escopo: a classe tem refund, mas o utilizador (canónico) só tem
	// charge → autoridade efectiva não inclui refund.
	h, err := b.Exchange(context.Background(), request("run-1", classScopedCap))
	if !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("esperado ErrOutOfScope via defesa server-side, obtido %v", err)
	}
	if h != "" {
		t.Fatalf("handle emitido apesar da defesa server-side: %q", h)
	}

	// a mediação PERMITIU (o gate não estava) e despachou, mas NENHUM registo de
	// troca foi selado (dispatch negou antes de recordExchange).
	for _, e := range readStream(t, es, "run-1") {
		if e.Type == exchangeEventType {
			t.Fatal("troca registada apesar de negada pela defesa server-side")
		}
	}
}

// TestBroker_Acessores_ScopeGateEToolID cobre os acessores de wiring: ToolID() e
// ScopeGate(). O gate produzido pelo helper tem de ser CONSISTENTE com o toolID e
// os classScopes efectivos do broker (nega fora de escopo, permite dentro).
func TestBroker_Acessores_ScopeGateEToolID(t *testing.T) {
	st := newStack(t, time.Minute)

	if got := st.broker.ToolID(); got != DefaultExchangeToolID {
		t.Fatalf("ToolID() = %q, esperado %q", got, DefaultExchangeToolID)
	}

	g := st.broker.ScopeGate()
	if g.Name() != "broker-scope" {
		t.Fatalf("ScopeGate().Name() = %q", g.Name())
	}

	ctx := context.Background()
	// in-scope (utilizador tem charge): permite.
	res, err := g.Evaluate(ctx, &referencemonitor.Call{
		ToolID:     st.broker.ToolID(),
		Capability: provInScopeCap,
		Principal:  principal(provInScopeCap),
	})
	if err != nil {
		t.Fatalf("Evaluate in-scope: %v", err)
	}
	if res.Decision != referencemonitor.HookAllow {
		t.Fatalf("in-scope devia Allow, obtido %v", res.Decision)
	}

	// out-of-scope (classe tem refund, utilizador não): nega fail-closed.
	res2, err := g.Evaluate(ctx, &referencemonitor.Call{
		ToolID:     st.broker.ToolID(),
		Capability: classScopedCap,
		Principal:  principal(provInScopeCap),
	})
	if err != nil {
		t.Fatalf("Evaluate out-of-scope: %v", err)
	}
	if res2.Decision != referencemonitor.HookDeny {
		t.Fatalf("out-of-scope devia Deny, obtido %v", res2.Decision)
	}
}

func TestNew_ToolIDDuplicado_Rejeitado(t *testing.T) {
	st := newStack(t, time.Minute)        // já registou DefaultExchangeToolID no RM
	_, err := New(st.rm, st.vault, st.es) // segundo registo do mesmo toolID
	if err == nil {
		t.Fatal("esperado erro de tool id duplicado")
	}
}
