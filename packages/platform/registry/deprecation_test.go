package registry

import (
	"context"
	"errors"
	"sync"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/registry/domain"
)

// TestDeprecatedNotRemovableWhileReferenced é o teste de INTEGRAÇÃO do ticket: uma
// versão deprecated NÃO é removível enquanto uma trajectória a referenciar
// (ErrStillReferenced). Só após libertar a referência é retirável.
func TestDeprecatedNotRemovableWhileReferenced(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	l := NewLifecycle("tool.http.get")
	v1 := ver(1, 0, 0)
	if err := l.Track(v1, domain.StatusActive); err != nil {
		t.Fatalf("Track: %v", err)
	}

	// Uma trajectória referencia v1 (gravou-a no seu manifesto imutável).
	if err := l.Reference(ctx, v1, "traj-A"); err != nil {
		t.Fatalf("Reference: %v", err)
	}

	// Retirada de uma versão ainda active é recusada (deprecacao formal previa).
	if err := l.Retire(ctx, v1); !errors.Is(err, ErrNotDeprecated) {
		t.Fatalf("Retire(active) = %v, quer ErrNotDeprecated", err)
	}

	// Deprecação formal.
	if err := l.Deprecate(ctx, v1); err != nil {
		t.Fatalf("Deprecate: %v", err)
	}
	if st, _ := l.Status(v1); st != domain.StatusDeprecated {
		t.Fatalf("estado = %v, quer deprecated", st)
	}

	// Deprecated MAS referenciada: retirada recusada (fail-closed).
	if err := l.Retire(ctx, v1); !errors.Is(err, ErrStillReferenced) {
		t.Fatalf("Retire(referenciada) = %v, quer ErrStillReferenced", err)
	}
	if _, ok := l.Status(v1); !ok {
		t.Fatalf("v1 nao devia ter sido removida")
	}

	// Libertar a referência e retirar.
	if err := l.Release(v1, "traj-A"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if l.RefCount(v1) != 0 {
		t.Fatalf("RefCount = %d, quer 0", l.RefCount(v1))
	}
	if err := l.Retire(ctx, v1); err != nil {
		t.Fatalf("Retire(sem refs) = %v, quer nil", err)
	}
	if _, ok := l.Status(v1); ok {
		t.Fatalf("v1 devia ter sido retirada")
	}
}

// TestReferenceCounterMultipleTrajectories garante que a retirada só é permitida
// quando TODAS as trajectórias libertaram a referência (contador por trajectória).
func TestReferenceCounterMultipleTrajectories(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	l := NewLifecycle("skill.summarize")
	v := ver(2, 1, 0)
	if err := l.Track(v, domain.StatusActive); err != nil {
		t.Fatalf("Track: %v", err)
	}
	if err := l.Reference(ctx, v, "A"); err != nil {
		t.Fatalf("ref A: %v", err)
	}
	if err := l.Reference(ctx, v, "B"); err != nil {
		t.Fatalf("ref B: %v", err)
	}
	if err := l.Reference(ctx, v, "A"); err != nil { // A referencia duas vezes
		t.Fatalf("ref A2: %v", err)
	}
	if l.RefCount(v) != 3 {
		t.Fatalf("RefCount = %d, quer 3", l.RefCount(v))
	}
	if err := l.Deprecate(ctx, v); err != nil {
		t.Fatalf("Deprecate: %v", err)
	}
	// Libertar só uma: ainda referenciada.
	_ = l.Release(v, "A")
	_ = l.Release(v, "A")
	if err := l.Retire(ctx, v); !errors.Is(err, ErrStillReferenced) {
		t.Fatalf("Retire = %v, quer ErrStillReferenced (B ainda referencia)", err)
	}
	if err := l.Release(v, "B"); err != nil {
		t.Fatalf("Release B: %v", err)
	}
	if err := l.Retire(ctx, v); err != nil {
		t.Fatalf("Retire final = %v, quer nil", err)
	}
}

// TestReleaseUnknownReference garante o fail-closed do contador (nunca negativo).
func TestReleaseUnknownReference(t *testing.T) {
	t.Parallel()
	l := NewLifecycle("tool.x")
	v := ver(1, 0, 0)
	_ = l.Track(v, domain.StatusActive)
	if err := l.Release(v, "nunca-referenciou"); !errors.Is(err, ErrNoReference) {
		t.Fatalf("Release = %v, quer ErrNoReference", err)
	}
}

// TestRollbackAtomicRestoresPrevious é o teste de DOMÍNIO do rollback: repõe a
// versão anterior como active num swap único; a active corrente passa a deprecated.
func TestRollbackAtomicRestoresPrevious(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	l := NewLifecycle("tool.http.get")
	v1 := ver(1, 0, 0)
	v2 := ver(1, 1, 0)
	if err := l.Track(v1, domain.StatusDeprecated); err != nil { // versão anterior ainda no catálogo
		t.Fatalf("Track v1: %v", err)
	}
	if err := l.Track(v2, domain.StatusActive); err != nil {
		t.Fatalf("Track v2: %v", err)
	}
	if a, _ := l.Active(); !a.Equal(v2) {
		t.Fatalf("active inicial = %v, quer v2", a)
	}

	if err := l.Rollback(ctx, v1); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	// Swap: v1 active, v2 deprecated. Exactamente uma active.
	snap := l.Snapshot()
	if !snap.HasActive || !snap.Active.Equal(v1) {
		t.Fatalf("active pos-rollback = %v (has=%v), quer v1", snap.Active, snap.HasActive)
	}
	if snap.Statuses[v1] != domain.StatusActive {
		t.Fatalf("v1 = %v, quer active", snap.Statuses[v1])
	}
	if snap.Statuses[v2] != domain.StatusDeprecated {
		t.Fatalf("v2 = %v, quer deprecated", snap.Statuses[v2])
	}
	if n := countActive(snap); n != 1 {
		t.Fatalf("nº de versoes active = %d, quer exactamente 1", n)
	}
}

// TestRollbackRejects cobre os fail-closed do rollback.
func TestRollbackRejects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	l := NewLifecycle("tool.x")
	v1 := ver(1, 0, 0)
	v2 := ver(2, 0, 0)
	vrev := ver(1, 5, 0)
	_ = l.Track(v1, domain.StatusActive)
	_ = l.Track(vrev, domain.StatusRevoked)

	// Alvo inexistente.
	if err := l.Rollback(ctx, v2); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("Rollback(inexistente) = %v, quer ErrUnknownVersion", err)
	}
	// Alvo revogado.
	if err := l.Rollback(ctx, vrev); !errors.Is(err, ErrVersionRevoked) {
		t.Fatalf("Rollback(revogado) = %v, quer ErrVersionRevoked", err)
	}
	// Alvo já active.
	if err := l.Rollback(ctx, v1); !errors.Is(err, ErrNotRollbackable) {
		t.Fatalf("Rollback(ja active) = %v, quer ErrNotRollbackable", err)
	}
}

// stubLifecycleVerifier é um LifecycleAdmissionVerifier de teste: devolve err.
type stubLifecycleVerifier struct {
	err  error
	seen []domain.Version
}

func (s *stubLifecycleVerifier) Verify(_ context.Context, v domain.Version) error {
	s.seen = append(s.seen, v)
	return s.err
}

// TestRollbackRejectsStaging garante que o rollback NÃO promove uma versão staging
// (nunca verificada) a active — fecha o bypass staging→active de AOS-047/048
// (AOS-052-Q2).
func TestRollbackRejectsStaging(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	l := NewLifecycle("tool.x")
	sv := ver(0, 1, 0)
	if err := l.Track(sv, domain.StatusStaging); err != nil {
		t.Fatalf("Track staging: %v", err)
	}
	if err := l.Rollback(ctx, sv); !errors.Is(err, ErrNotRollbackable) {
		t.Fatalf("Rollback(staging) = %v, quer ErrNotRollbackable", err)
	}
	// Estado inalterado: continua staging, sem active.
	if st, _ := l.Status(sv); st != domain.StatusStaging {
		t.Fatalf("estado = %v, quer staging (rollback nao devia promover)", st)
	}
	if _, ok := l.Active(); ok {
		t.Fatalf("nao devia existir active")
	}
}

// TestRollbackAdmissionGate garante que, com um LifecycleAdmissionVerifier injectado,
// a re-promoção deprecated→active RE-VERIFICA (AOS-048 Q1): um Verify que falha aborta
// o swap sem mutação; um Verify que passa permite o rollback.
func TestRollbackAdmissionGate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	v1 := ver(1, 0, 0)
	v2 := ver(1, 1, 0)

	// Verificador a NEGAR: rollback recusado, estado intacto.
	deny := &stubLifecycleVerifier{err: errors.New("chave revogada")}
	l := NewLifecycle("tool.x", WithLifecycleAdmissionVerifier(deny))
	if err := l.Track(v1, domain.StatusDeprecated); err != nil {
		t.Fatalf("Track v1: %v", err)
	}
	if err := l.Track(v2, domain.StatusActive); err != nil {
		t.Fatalf("Track v2: %v", err)
	}
	if err := l.Rollback(ctx, v1); !errors.Is(err, ErrAdmissionDenied) {
		t.Fatalf("Rollback(deny) = %v, quer ErrAdmissionDenied", err)
	}
	if len(deny.seen) != 1 || !deny.seen[0].Equal(v1) {
		t.Fatalf("verificador nao chamado com o alvo: %v", deny.seen)
	}
	snap := l.Snapshot()
	if !snap.Active.Equal(v2) || snap.Statuses[v1] != domain.StatusDeprecated {
		t.Fatalf("estado mutado apos deny: active=%v v1=%v", snap.Active, snap.Statuses[v1])
	}

	// Verificador a PERMITIR: rollback procede.
	allow := &stubLifecycleVerifier{}
	l2 := NewLifecycle("tool.x", WithLifecycleAdmissionVerifier(allow))
	_ = l2.Track(v1, domain.StatusDeprecated)
	_ = l2.Track(v2, domain.StatusActive)
	if err := l2.Rollback(ctx, v1); err != nil {
		t.Fatalf("Rollback(allow) = %v, quer nil", err)
	}
	if a, _ := l2.Active(); !a.Equal(v1) {
		t.Fatalf("active pos-rollback = %v, quer v1", a)
	}
}

// TestReferenceManifestBridge cobre a ligação manifesto→refcount (AOS-052-Q3): uma
// versão gravada por uma trajectória num DependencyManifest imutável NÃO é retirável
// enquanto a referência do manifesto viver; só as dependências cujo nome corresponde
// à linha de versões são contadas.
func TestReferenceManifestBridge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	v1 := ver(1, 0, 0)
	m, err := FromRuntimeManifest("traj-M", agentruntime.Manifest{
		Model:      agentruntime.ModelManifest{ModelID: "claude-opus-4-8"},
		PromptHash: "sha256:prompt",
		Tools: []agentruntime.PinnedDep{
			{Name: "tool.http.get", Version: "1.0.0", Digest: "sha256:x"},
			{Name: "tool.OUTRO", Version: "2.0.0", Digest: "sha256:y"}, // outra linha: ignorada
		},
	})
	if err != nil {
		t.Fatalf("FromRuntimeManifest: %v", err)
	}

	l := NewLifecycle("tool.http.get")
	if err := l.Track(v1, domain.StatusActive); err != nil {
		t.Fatalf("Track: %v", err)
	}
	if err := l.ReferenceManifest(ctx, m); err != nil {
		t.Fatalf("ReferenceManifest: %v", err)
	}
	// Só a dependência tool.http.get@1.0.0 conta; tool.OUTRO é de outra linha.
	if l.RefCount(v1) != 1 {
		t.Fatalf("RefCount = %d, quer 1", l.RefCount(v1))
	}

	// Deprecated + referenciada por manifesto: retirada recusada (perda de replay).
	if err := l.Deprecate(ctx, v1); err != nil {
		t.Fatalf("Deprecate: %v", err)
	}
	if err := l.Retire(ctx, v1); !errors.Is(err, ErrStillReferenced) {
		t.Fatalf("Retire(referenciada por manifesto) = %v, quer ErrStillReferenced", err)
	}

	// Libertar a referência do manifesto torna a versão retirável.
	if err := l.ReleaseManifest(m); err != nil {
		t.Fatalf("ReleaseManifest: %v", err)
	}
	if l.RefCount(v1) != 0 {
		t.Fatalf("RefCount pos-release = %d, quer 0", l.RefCount(v1))
	}
	if err := l.Retire(ctx, v1); err != nil {
		t.Fatalf("Retire(sem refs) = %v, quer nil", err)
	}
}

// TestReferenceManifestFailClosed cobre os ramos fail-closed da ponte.
func TestReferenceManifestFailClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	l := NewLifecycle("tool.http.get")
	_ = l.Track(ver(1, 0, 0), domain.StatusActive)

	// Manifesto zero (trajectória vazia) → ErrInvalidManifest.
	if err := l.ReferenceManifest(ctx, DependencyManifest{}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("ReferenceManifest(zero) = %v, quer ErrInvalidManifest", err)
	}
	if err := l.ReleaseManifest(DependencyManifest{}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("ReleaseManifest(zero) = %v, quer ErrInvalidManifest", err)
	}

	// Dependência da linha com versão inválida → ErrInvalidRequest.
	m, err := FromRuntimeManifest("traj-Z", agentruntime.Manifest{
		Model:      agentruntime.ModelManifest{ModelID: "m"},
		PromptHash: "sha256:p",
		Tools:      []agentruntime.PinnedDep{{Name: "tool.http.get", Version: "nao-semver"}},
	})
	if err != nil {
		t.Fatalf("FromRuntimeManifest: %v", err)
	}
	if err := l.ReferenceManifest(ctx, m); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("ReferenceManifest(versao invalida) = %v, quer ErrInvalidRequest", err)
	}
}

// TestRollbackAtomicUnderConcurrency exercita o swap sob concorrência com -race: um
// escritor faz rollbacks alternados enquanto leitores tiram fotografias. A
// invariante — NUNCA mais do que uma versão active numa fotografia — nunca é
// violada (sem estado híbrido observável).
func TestRollbackAtomicUnderConcurrency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	l := NewLifecycle("tool.http.get")
	v1 := ver(1, 0, 0)
	v2 := ver(1, 1, 0)
	_ = l.Track(v1, domain.StatusDeprecated)
	_ = l.Track(v2, domain.StatusActive)

	var writer, readers sync.WaitGroup
	stop := make(chan struct{})

	// Escritor: alterna rollback entre v1 e v2.
	writer.Add(1)
	go func() {
		defer writer.Done()
		targets := []domain.Version{v1, v2}
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Alvo = o que NÃO está active (o outro dá ErrNotRollbackable, ignorado).
			_ = l.Rollback(ctx, targets[i%2])
			i++
		}
	}()

	// Leitores: verificam a invariante de atomicidade.
	for r := 0; r < 4; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for j := 0; j < 2000; j++ {
				snap := l.Snapshot()
				if n := countActive(snap); n > 1 {
					t.Errorf("estado hibrido observado: %d versoes active", n)
					return
				}
			}
		}()
	}
	// Leitores terminam as suas iteracoes; sinaliza stop e espera o escritor.
	readers.Wait()
	close(stop)
	writer.Wait()

	// Estado final coerente: exactamente uma active.
	snap := l.Snapshot()
	if countActive(snap) != 1 {
		t.Fatalf("estado final = %d active, quer 1", countActive(snap))
	}
}

func countActive(s LifecycleSnapshot) int {
	n := 0
	for _, st := range s.Statuses {
		if st == domain.StatusActive {
			n++
		}
	}
	return n
}

// TestLifecycleEdges cobre os ramos fail-closed e acessores utilitários.
func TestLifecycleEdges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	l := NewLifecycle("tool.edge")
	if l.ID() != "tool.edge" {
		t.Fatalf("ID = %q", l.ID())
	}

	// Track fail-closed: versão zero e estado inválido.
	if err := l.Track(domain.Version{}, domain.StatusActive); !errors.Is(err, ErrUnpinnedResolution) {
		t.Fatalf("Track(zero) = %v, quer ErrUnpinnedResolution", err)
	}
	if err := l.Track(ver(1, 0, 0), domain.Status("bogus")); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Track(estado invalido) = %v, quer ErrInvalidRequest", err)
	}

	// Active vazia inicialmente.
	if _, ok := l.Active(); ok {
		t.Fatalf("Active devia ser vazia")
	}

	// Operações sobre versão desconhecida.
	unk := ver(9, 9, 9)
	if err := l.Reference(ctx, unk, "A"); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("Reference(desconhecida) = %v", err)
	}
	if err := l.Reference(ctx, ver(1, 0, 0), ""); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Reference(traj vazia) = %v", err)
	}
	if err := l.Deprecate(ctx, unk); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("Deprecate(desconhecida) = %v", err)
	}
	if err := l.Retire(ctx, unk); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("Retire(desconhecida) = %v", err)
	}
	if l.RefCount(unk) != 0 {
		t.Fatalf("RefCount(desconhecida) != 0")
	}

	// Staging não transita directamente para deprecated (máquina de estados).
	sv := ver(0, 1, 0)
	_ = l.Track(sv, domain.StatusStaging)
	if err := l.Deprecate(ctx, sv); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("Deprecate(staging) = %v, quer ErrInvalidTransition", err)
	}

	// Deprecate idempotente sobre já-deprecated.
	av := ver(1, 0, 0)
	_ = l.Track(av, domain.StatusActive)
	if err := l.Deprecate(ctx, av); err != nil {
		t.Fatalf("Deprecate: %v", err)
	}
	if err := l.Deprecate(ctx, av); err != nil {
		t.Fatalf("Deprecate idempotente = %v", err)
	}

	// Versão revogada: referência/deprecate/retire recusados.
	rv := ver(2, 0, 0)
	_ = l.Track(rv, domain.StatusRevoked)
	if err := l.Reference(ctx, rv, "A"); !errors.Is(err, ErrVersionRevoked) {
		t.Fatalf("Reference(revogada) = %v", err)
	}
	if err := l.Deprecate(ctx, rv); !errors.Is(err, ErrVersionRevoked) {
		t.Fatalf("Deprecate(revogada) = %v", err)
	}
	if err := l.Retire(ctx, rv); !errors.Is(err, ErrVersionRevoked) {
		t.Fatalf("Retire(revogada) = %v", err)
	}

	// Track actualiza o estado de uma versão existente.
	_ = l.Track(av, domain.StatusActive) // reactiva av (deprecated -> active via Track/projeccao)
	if st, _ := l.Status(av); st != domain.StatusActive {
		t.Fatalf("Track update: estado = %v, quer active", st)
	}

	// Versions em ordem SemVer crescente.
	vs := l.Versions()
	for i := 1; i < len(vs); i++ {
		if !vs[i-1].Less(vs[i]) {
			t.Fatalf("Versions nao ordenada: %v", vs)
		}
	}
}

// TestLifecycleSpans confirma que as operações emitem spans (observabilidade).
func TestLifecycleSpans(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tr := &agentruntime.RecordingTracer{}
	l := NewLifecycle("tool.x", WithLifecycleTracer(tr))
	v := ver(1, 0, 0)
	_ = l.Track(v, domain.StatusActive)
	_ = l.Reference(ctx, v, "A")
	_ = l.Deprecate(ctx, v)
	for _, op := range []string{opReference, opDeprecate} {
		if len(tr.SpansByOperation(op)) == 0 {
			t.Fatalf("sem span para %q", op)
		}
	}
}
