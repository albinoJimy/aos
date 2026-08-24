package integration

import (
	"context"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/memory/provenance"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/toolset"
	"github.com/aos-ref/substrate/eventstore"
)

// secParaCablagem levanta um SecuredRuntime MINIMO sobre um catalogo e um ToolSetStore
// DURAVEL dados. Cada chamada simula uma INCARNACAO do processo: registo de tool sets em
// memoria vazio, mesmo Event Store por baixo.
func secParaCablagem(t *testing.T, store ToolSetStore, catalog *fakeCatalog) *SecuredRuntime {
	t.Helper()
	ctx := context.Background()
	signer := testSigner(t)
	auditStore := audit.NewMemStore()
	trust := newTrust(t, ctx, auditStore, signer)
	rv := newRevalidator(t, trust, auditStore, NewProvenanceQuarantiner(provenance.NewPartition(nil), WithQuarantineClock(fixedClock())), NewRecordingAlerter())

	trajStore, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = trajStore.Close() })

	sec, err := NewSecuredRuntime(SecuredConfig{
		Model:         &scriptedModel{responses: []agentruntime.ModelResponse{{Text: "feito", Final: true}}},
		Recorder:      agentruntime.NewTurnRecorder(trajStore),
		Catalog:       catalog,
		Revalidator:   rv,
		Policy:        StaticPolicy{MaxEgress: domain.EgressExternal},
		WORM:          audit.NewMemStore(),
		ToolSetStore:  store,
		FreezeOptions: []toolset.Option{toolset.WithClock(fixedClock())},
	})
	if err != nil {
		t.Fatalf("NewSecuredRuntime: %v", err)
	}
	return sec
}

// A RE-HOSPEDAGEM USA O SNAPSHOT DURAVEL, NAO UM CONGELAMENTO NOVO DO CATALOGO VIVO.
//
// E o achado E. `RunToolSets.Rebuild` existia desde AOS-155 com ZERO chamadores de
// producao: toda a re-hospedagem re-congelava do catalogo actual. A divergencia era
// SILENCIOSA porque o StepID do snapshot e fixo ("toolset-freeze") e a re-escrita era
// engolida por dedup — o disco ficava com o snapshot ORIGINAL e a memoria com o NOVO.
//
// Consequencia: a revalidacao por chamada comparava congelado(actual) contra actual. Uma
// tautologia. A barreira corria e nao negava nada.
//
// ESTE TESTE FALHA SEM A CABLAGEM. O `freeze_durable_test.go` que ja existia exercita a
// `Rebuild` DIRECTAMENTE e por isso passava com o defeito intacto — provava que a unidade
// funciona, nunca que alguem a chama.
func TestCablagem_ReHospedagemUsaOSnapshotDuravelENaoOCatalogoVivo(t *testing.T) {
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()

	signer := testSigner(t)
	catalogoA := &fakeCatalog{entries: []domain.Entry{
		signedEntry(t, signer, "echo", "1.0.0", domain.Contract{Egress: domain.EgressNone}),
	}}
	// O catalogo MUDA entre incarnacoes — e o que um deploy faz.
	catalogoB := &fakeCatalog{entries: []domain.Entry{
		signedEntry(t, signer, "echo", "2.0.0", domain.Contract{Egress: domain.EgressNone}),
	}}

	const runID = "run-atravessa-o-deploy"

	// (1) PRIMEIRA INCARNACAO, catalogo A.
	secA := secParaCablagem(t, store, catalogoA)
	_, frozenA, err := secA.Run(ctx, testGoal(runID), nil)
	if err != nil {
		t.Fatalf("Run (incarnacao A): %v", err)
	}

	// PRECONDICAO: os dois catalogos TEM mesmo de produzir snapshots diferentes. Sem esta
	// asercao, um `fakeCatalog` que ignorasse a versao tornaria todo o teste vacuo — o
	// hash seria igual e o "usou o original" passaria sem provar nada.
	secCtl := secParaCablagem(t, store, catalogoB)
	_, frozenB, err := secCtl.Run(ctx, testGoal("run-novo-sob-B"), nil)
	if err != nil {
		t.Fatalf("Run (controlo sob B): %v", err)
	}
	if frozenA.Hash() == frozenB.Hash() {
		t.Fatalf("PRECONDICAO: os catalogos A e B produzem o MESMO hash (%q) — o teste nao distinguiria nada", frozenA.Hash())
	}

	// (2) RE-HOSPEDAGEM do MESMO run numa incarnacao nova, ja com o catalogo B.
	secB := secParaCablagem(t, store, catalogoB)
	_, reposto, err := secB.Run(ctx, testGoal(runID), nil)
	if err != nil {
		t.Fatalf("Run (re-hospedagem sob B): %v", err)
	}

	// O ACHADO: tem de ser o snapshot ORIGINAL.
	if reposto.Hash() != frozenA.Hash() {
		t.Fatalf("a re-hospedagem congelou do catalogo VIVO: hash %q, esperava o original %q — "+
			"a revalidacao passaria a comparar actual contra actual", reposto.Hash(), frozenA.Hash())
	}
	if reposto.Hash() == frozenB.Hash() {
		t.Fatal("a re-hospedagem usou o catalogo NOVO — e exactamente o drift que AOS-050/AOS-155 existem para apanhar")
	}
}

// CONTROLO ANTI-VACUIDADE: UM RUN NOVO CONGELA DO CATALOGO ACTUAL.
//
// Sem este caso, uma implementacao que devolvesse SEMPRE o primeiro snapshot que
// encontrasse — ou que nunca congelasse de todo — passaria o teste de cima.
func TestCablagem_RunNovoCongelaDoCatalogoActual(t *testing.T) {
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()

	signer := testSigner(t)
	catalogo := &fakeCatalog{entries: []domain.Entry{
		signedEntry(t, signer, "echo", "3.0.0", domain.Contract{Egress: domain.EgressNone}),
	}}

	sec := secParaCablagem(t, store, catalogo)
	_, frozen, err := sec.Run(ctx, testGoal("run-inedito"), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	esperado, err := toolset.FreezeToolSet(ctx, catalogo, "run-inedito", nil, toolset.WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("FreezeToolSet: %v", err)
	}
	if frozen.Hash() != esperado.Hash() {
		t.Fatalf("CONTROLO: um run INEDITO devia congelar do catalogo actual; hash %q != %q", frozen.Hash(), esperado.Hash())
	}
}
