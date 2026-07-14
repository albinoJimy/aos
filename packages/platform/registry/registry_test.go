package registry

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/substrate/eventstore"
)

// --- helpers deterministas -------------------------------------------------

// ver constrói uma versão pinada com campos nomeados (vet: composite literal).
func ver(mj, mn, p int) domain.Version {
	return domain.Version{Major: mj, Minor: mn, Patch: p}
}

func fixedClock() func() time.Time {
	base := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	return func() time.Time { return base }
}

func newTestRegistry(t *testing.T, opts ...Option) (*Registry, eventstore.EventStore) {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// O default do REG é FAIL-CLOSED (denyVerifier): injectamos allowVerifier ANTES
	// de opts... para que os testes de ciclo de vida que não exercitam a assinatura
	// consigam promover a active; um verifier passado em opts (spy/deny) sobrepõe-se
	// (a última Option vence).
	all := append([]Option{WithClock(fixedClock()), WithAdmissionVerifier(allowVerifier{})}, opts...)
	reg, err := New(store, all...)
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	return reg, store
}

func toolReq(id string, v domain.Version) PublishRequest {
	return PublishRequest{
		ID:        id,
		Version:   v,
		Kind:      domain.KindTool,
		Origin:    "git+https://example/tools",
		Publisher: "pub:acme",
		Contract: domain.Contract{
			Egress:           domain.EgressExternal,
			CredentialScopes: []string{"vault:http"},
		},
	}
}

func mustPublish(t *testing.T, reg *Registry, req PublishRequest) domain.Entry {
	t.Helper()
	e, err := reg.Publish(context.Background(), req)
	if err != nil {
		t.Fatalf("Publish(%s): %v", req.ID, err)
	}
	return e
}

func mustSetStatus(t *testing.T, reg *Registry, id string, v domain.Version, to domain.Status) domain.Entry {
	t.Helper()
	e, err := reg.SetStatus(context.Background(), id, v, to)
	if err != nil {
		t.Fatalf("SetStatus(%s,%s): %v", id, to, err)
	}
	return e
}

// --- New -------------------------------------------------------------------

func TestNew_NoStoreFailsClosed(t *testing.T) {
	t.Parallel()
	if _, err := New(nil); !errors.Is(err, ErrNoStore) {
		t.Fatalf("New(nil) = %v, quer ErrNoStore", err)
	}
}

// --- Publish: entra em staging, nunca active -------------------------------

func TestPublish_EntersStagingNeverActive(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	v := ver(1, 0, 0)
	e := mustPublish(t, reg, toolReq("tool.http.get", v))
	if e.Status != domain.StatusStaging {
		t.Fatalf("publish devia entrar em staging, got %s", e.Status)
	}
	if e.Provenance.Trust != domain.TrustFirstSeen {
		t.Fatalf("confianca inicial devia ser first_seen, got %s", e.Provenance.Trust)
	}
	if e.Digest == "" {
		t.Fatal("digest devia estar preenchido (placeholder)")
	}
	if e.Provenance.Timestamp == "" {
		t.Fatal("timestamp de proveniencia ausente")
	}
	// Confirma que resolve encontra a entrada em staging.
	got, err := reg.Resolve(context.Background(), "tool.http.get", v)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Status != domain.StatusStaging {
		t.Fatalf("estado resolvido = %s", got.Status)
	}
}

func TestPublish_InvalidRequest(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	cases := []struct {
		name string
		req  PublishRequest
	}{
		{"sem id", PublishRequest{Version: ver(1, 0, 0), Kind: domain.KindTool}},
		{"versao zero", PublishRequest{ID: "x", Version: domain.Version{}, Kind: domain.KindTool}},
		{"kind invalido", PublishRequest{ID: "x", Version: ver(1, 0, 0), Kind: "plugin"}},
		{"egress invalido", PublishRequest{ID: "y", Version: ver(1, 0, 0), Kind: domain.KindTool, Contract: domain.Contract{Egress: "bad"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := reg.Publish(ctx, tc.req); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Publish(%s) = %v, quer ErrInvalidRequest", tc.name, err)
			}
		})
	}
}

// --- Append-only: editar versao existente FALHA ----------------------------

func TestPublish_AppendOnlyImmutability(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	v := ver(1, 0, 0)
	mustPublish(t, reg, toolReq("tool.x", v))

	// Republicar a MESMA (id,version) — mesmo com contrato alterado — FALHA.
	edited := toolReq("tool.x", v)
	edited.Contract.Egress = domain.EgressInternal
	if _, err := reg.Publish(ctx, edited); !errors.Is(err, ErrVersionExists) {
		t.Fatalf("republicar versao existente = %v, quer ErrVersionExists", err)
	}

	// A entrada original permanece intacta (imutavel).
	got, err := reg.Resolve(ctx, "tool.x", v)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Contract.Egress != domain.EgressExternal {
		t.Fatalf("entrada foi mutada: egress = %s", got.Contract.Egress)
	}

	// Uma NOVA versao e aceite (append-only: alteracoes produzem novas versoes).
	if _, err := reg.Publish(ctx, toolReq("tool.x", ver(1, 0, 1))); err != nil {
		t.Fatalf("nova versao devia ser aceite: %v", err)
	}
}

// --- Append-only sob TOCTOU: publish concorrente da MESMA (id,version) ------

// TestPublish_ConcurrentSameVersionNoFalseSuccess prova o contrato append-only sob
// corrida: duas publicacoes concorrentes da MESMA (id,version) com conteudos
// DISTINTOS nunca podem AMBAS devolver sucesso. O Event Store deduplica por
// idempotency_key (verificado ANTES do expected_seq), logo o escritor perdedor
// recebe StatusDuplicate; o REG deixou de o descartar e devolve ErrVersionExists em
// vez de um falso sucesso com o SEU proprio conteudo nao-armazenado. Exactamente um
// vencedor; o catalogo guarda um UNICO conteudo e nunca e sobre-escrito.
func TestPublish_ConcurrentSameVersionNoFalseSuccess(t *testing.T) {
	t.Parallel()
	for iter := 0; iter < 200; iter++ {
		reg, _ := newTestRegistry(t)
		v := ver(1, 0, 0)

		// Conteudo (credential_scopes) distinto -> digests distintos: se ambos
		// "vencessem", o Resolve exporia a inconsistencia.
		reqA := toolReq("tool.race", v)
		reqA.Contract.CredentialScopes = []string{"vault:A"}
		reqB := toolReq("tool.race", v)
		reqB.Contract.CredentialScopes = []string{"vault:B"}

		errs := make([]error, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		run := func(i int, req PublishRequest) {
			defer wg.Done()
			<-start // liberta as duas goroutines em simultaneo (maximiza a corrida).
			_, errs[i] = reg.Publish(context.Background(), req)
		}
		go run(0, reqA)
		go run(1, reqB)
		close(start)
		wg.Wait()

		nilCount := 0
		for i, err := range errs {
			switch {
			case err == nil:
				nilCount++
			case errors.Is(err, ErrVersionExists):
				// perdedor esperado (StatusDuplicate -> ErrVersionExists).
			case errors.Is(err, ErrConcurrentWrite):
				// aceitavel sob contencao extrema (retries esgotados); NUNCA um
				// falso sucesso.
			default:
				t.Fatalf("iter %d goroutine %d: erro inesperado %v", iter, i, err)
			}
		}
		if nilCount != 1 {
			t.Fatalf("iter %d: %d sucessos concorrentes (esperado exactamente 1) — falso sucesso do perdedor", iter, nilCount)
		}

		// O catalogo guarda um UNICO conteudo (o do vencedor); nunca sobre-escrito.
		got, err := reg.Resolve(context.Background(), "tool.race", v)
		if err != nil {
			t.Fatalf("iter %d: Resolve pos-corrida: %v", iter, err)
		}
		scopes := got.Contract.CredentialScopes
		if len(scopes) != 1 || (scopes[0] != "vault:A" && scopes[0] != "vault:B") {
			t.Fatalf("iter %d: conteudo armazenado inconsistente: %v", iter, scopes)
		}
	}
}

// --- Resolve: pinado, nunca latest -----------------------------------------

func TestResolve_PinnedExact(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	mustPublish(t, reg, toolReq("tool.x", ver(1, 0, 0)))
	mustPublish(t, reg, toolReq("tool.x", ver(2, 0, 0)))

	e, err := reg.Resolve(ctx, "tool.x", ver(1, 0, 0))
	if err != nil {
		t.Fatalf("Resolve v1: %v", err)
	}
	if !e.Version.Equal(ver(1, 0, 0)) {
		t.Fatalf("resolveu versao errada: %v", e.Version)
	}
	// Versao inexistente -> not found (default-deny base).
	if _, err := reg.Resolve(ctx, "tool.x", ver(9, 9, 9)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("versao inexistente = %v, quer ErrNotFound", err)
	}
}

func TestResolve_UnpinnedRejected(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	// Versao zero (sem versao) -> recusada.
	if _, err := reg.Resolve(ctx, "tool.x", domain.Version{}); !errors.Is(err, ErrUnpinnedResolution) {
		t.Fatalf("Resolve versao-zero = %v, quer ErrUnpinnedResolution", err)
	}
}

func TestResolveString_RejectsFloating(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	mustPublish(t, reg, toolReq("tool.x", ver(1, 2, 3)))

	for _, ref := range []string{"latest", "main", "", "^1.0.0", "1.x", "1.2"} {
		if _, err := reg.ResolveString(ctx, "tool.x", ref); !errors.Is(err, ErrFloatingResolution) {
			t.Fatalf("ResolveString(%q) = %v, quer ErrFloatingResolution", ref, err)
		}
	}
	// Referencia pinada exacta funciona.
	e, err := reg.ResolveString(ctx, "tool.x", "1.2.3")
	if err != nil {
		t.Fatalf("ResolveString exacta: %v", err)
	}
	if !e.Version.Equal(ver(1, 2, 3)) {
		t.Fatalf("versao errada: %v", e.Version)
	}
}

// --- SetStatus: ciclo de vida ----------------------------------------------

func TestSetStatus_LifecycleHappyPath(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	v := ver(1, 0, 0)
	mustPublish(t, reg, toolReq("tool.x", v))

	active := mustSetStatus(t, reg, "tool.x", v, domain.StatusActive)
	if active.Status != domain.StatusActive {
		t.Fatalf("status = %s", active.Status)
	}
	dep := mustSetStatus(t, reg, "tool.x", v, domain.StatusDeprecated)
	if dep.Status != domain.StatusDeprecated {
		t.Fatalf("status = %s", dep.Status)
	}
	// Reactivacao permitida (deprecated->active).
	mustSetStatus(t, reg, "tool.x", v, domain.StatusActive)
	// Revogacao (terminal).
	rev := mustSetStatus(t, reg, "tool.x", v, domain.StatusRevoked)
	if rev.Status != domain.StatusRevoked {
		t.Fatalf("status = %s", rev.Status)
	}
	// A partir de revoked nao ha transicao valida.
	if _, err := reg.SetStatus(context.Background(), "tool.x", v, domain.StatusActive); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("revoked->active = %v, quer ErrInvalidTransition", err)
	}
}

func TestSetStatus_InvalidTransitionFailsClosed(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	v := ver(1, 0, 0)
	mustPublish(t, reg, toolReq("tool.x", v))
	// staging->deprecated nao e permitido (tem de passar por active).
	if _, err := reg.SetStatus(ctx, "tool.x", v, domain.StatusDeprecated); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("staging->deprecated = %v, quer ErrInvalidTransition", err)
	}
	// setStatus sobre id inexistente -> not found.
	if _, err := reg.SetStatus(ctx, "nope", v, domain.StatusActive); !errors.Is(err, ErrNotFound) {
		t.Fatalf("setStatus inexistente = %v, quer ErrNotFound", err)
	}
	// estado invalido.
	if _, err := reg.SetStatus(ctx, "tool.x", v, domain.Status("bogus")); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("estado bogus = %v, quer ErrInvalidRequest", err)
	}
}

func TestSetStatus_Idempotent(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	v := ver(1, 0, 0)
	mustPublish(t, reg, toolReq("tool.x", v))
	mustSetStatus(t, reg, "tool.x", v, domain.StatusActive)
	// Repetir a mesma transicao (active->active) e no-op idempotente.
	e := mustSetStatus(t, reg, "tool.x", v, domain.StatusActive)
	if e.Status != domain.StatusActive {
		t.Fatalf("status = %s", e.Status)
	}
}

// --- Gate de admissao: staging->active passa pelo verifier -----------------

// denyVerifier recusa toda a promocao (simula um hash/assinatura invalido futuro).
type denyVerifier struct{ err error }

func (d denyVerifier) Verify(context.Context, domain.Entry) error { return d.err }

func TestSetStatus_AdmissionGateBlocks(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("verificacao falhou (hash/assinatura ausente)")
	reg, _ := newTestRegistry(t, WithAdmissionVerifier(denyVerifier{err: sentinel}))
	ctx := context.Background()
	v := ver(1, 0, 0)
	mustPublish(t, reg, toolReq("tool.x", v))

	_, err := reg.SetStatus(ctx, "tool.x", v, domain.StatusActive)
	if !errors.Is(err, ErrAdmissionDenied) {
		t.Fatalf("promocao com verifier a negar = %v, quer ErrAdmissionDenied", err)
	}
	// O artefacto permanece em staging (nunca saltou para active).
	got, _ := reg.Resolve(ctx, "tool.x", v)
	if got.Status != domain.StatusStaging {
		t.Fatalf("apos gate negado, status = %s (devia ficar staging)", got.Status)
	}
}

// verifierSpy prova que o gate é consultado em CADA promocao a active (staging->
// active E deprecated->active), e NUNCA nas restantes transicoes.
type verifierSpy struct{ calls int }

func (s *verifierSpy) Verify(context.Context, domain.Entry) error { s.calls++; return nil }

func TestSetStatus_GateOnEveryPromotionToActive(t *testing.T) {
	t.Parallel()
	spy := &verifierSpy{}
	reg, _ := newTestRegistry(t, WithAdmissionVerifier(spy))
	v := ver(1, 0, 0)
	mustPublish(t, reg, toolReq("tool.x", v))
	mustSetStatus(t, reg, "tool.x", v, domain.StatusActive)     // staging->active: gate +1
	mustSetStatus(t, reg, "tool.x", v, domain.StatusDeprecated) // active->deprecated: sem gate
	mustSetStatus(t, reg, "tool.x", v, domain.StatusActive)     // deprecated->active: gate +1 (AOS-048 Q1)
	mustSetStatus(t, reg, "tool.x", v, domain.StatusRevoked)    // active->revoked: sem gate
	if spy.calls != 2 {
		t.Fatalf("gate consultado %d vezes, esperado 2 (cada promocao a active)", spy.calls)
	}
}

// TestSetStatus_DefaultVerifierFailsClosed prova o default FAIL-CLOSED (AOS-048 Q3):
// um Registry construido SEM WithAdmissionVerifier NAO promove nada a active — o
// denyVerifier por omissao recusa a promocao com ErrNoAdmissionVerifier (embrulhado
// em ErrAdmissionDenied), em vez do antigo placeholder fail-open que admitia tudo.
func TestSetStatus_DefaultVerifierFailsClosed(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// New SEM WithAdmissionVerifier: default denyVerifier (fail-closed).
	reg, err := New(store, WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	v := ver(1, 0, 0)
	mustPublish(t, reg, toolReq("tool.x", v))

	_, err = reg.SetStatus(ctx, "tool.x", v, domain.StatusActive)
	if !errors.Is(err, ErrAdmissionDenied) {
		t.Fatalf("promocao com default fail-closed = %v, quer ErrAdmissionDenied", err)
	}
	// O artefacto permanece em staging (nunca saltou para active).
	got, _ := reg.Resolve(ctx, "tool.x", v)
	if got.Status != domain.StatusStaging {
		t.Fatalf("apos default deny, status = %s (devia ficar staging)", got.Status)
	}
	// O verificador por omissao e o defaultDenyVerifier, que recusa com o sentinela
	// especifico ErrNoAdmissionVerifier (o SetStatus embrulha-o em ErrAdmissionDenied
	// via %v, pelo que o sentinela interno nao vaza no chain do chamador — por design).
	if verr := (defaultDenyVerifier{}).Verify(ctx, got); !errors.Is(verr, ErrNoAdmissionVerifier) {
		t.Fatalf("defaultDenyVerifier.Verify = %v, quer ErrNoAdmissionVerifier", verr)
	}
}

// --- Default-deny: IsAdmissible --------------------------------------------

func TestIsAdmissible_DefaultDeny(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	v := ver(1, 0, 0)

	// (1) Fora do catalogo -> negado.
	ok, reason, err := reg.IsAdmissible(ctx, "ghost.tool", v)
	if err != nil {
		t.Fatalf("IsAdmissible err: %v", err)
	}
	if ok {
		t.Fatal("capacidade fora do catalogo devia ser NEGADA (default-deny)")
	}
	if reason == "" {
		t.Fatal("negacao devia ter razao")
	}

	// (2) No catalogo mas em staging -> negado (nao-active).
	mustPublish(t, reg, toolReq("tool.x", v))
	if ok, _, _ := reg.IsAdmissible(ctx, "tool.x", v); ok {
		t.Fatal("artefacto em staging nao e admissivel")
	}

	// (3) Promovido a active -> admissivel.
	mustSetStatus(t, reg, "tool.x", v, domain.StatusActive)
	if ok, _, _ := reg.IsAdmissible(ctx, "tool.x", v); !ok {
		t.Fatal("artefacto active devia ser admissivel")
	}

	// (4) Revogado -> volta a ser negado (bloqueio).
	mustSetStatus(t, reg, "tool.x", v, domain.StatusRevoked)
	if ok, _, _ := reg.IsAdmissible(ctx, "tool.x", v); ok {
		t.Fatal("artefacto revogado nao e admissivel")
	}

	// (5) Referencia nao pinada -> negada.
	if ok, _, _ := reg.IsAdmissible(ctx, "tool.x", domain.Version{}); ok {
		t.Fatal("referencia nao pinada nao e admissivel")
	}
}

// --- Persistencia: reconstrucao por replay (fonte de verdade = ES) ---------

func TestReplay_RebuildsFromEventStore(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	v := ver(1, 0, 0)

	reg1, err := New(store, WithClock(fixedClock()), WithAdmissionVerifier(allowVerifier{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := reg1.Publish(ctx, toolReq("tool.x", v)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if _, err := reg1.SetStatus(ctx, "tool.x", v, domain.StatusActive); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	// Uma NOVA instancia do REG sobre o MESMO store reconstroi o estado por replay
	// (nao ha estado autoritativo em RAM partilhado).
	reg2, err := New(store, WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := reg2.Resolve(ctx, "tool.x", v)
	if err != nil {
		t.Fatalf("Resolve na instancia 2: %v", err)
	}
	if got.Status != domain.StatusActive {
		t.Fatalf("estado reconstruido = %s, quer active", got.Status)
	}
}
