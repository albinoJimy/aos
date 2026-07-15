package supplychaintests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aos-ref/platform/audit"
	memdomain "github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/provenance"
	"github.com/aos-ref/platform/registry"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/signing"
	"github.com/aos-ref/platform/registry/tofu"
	"github.com/aos-ref/platform/registry/toolset"
)

// META-TESTES — PROVA DE DETECÇÃO (não green-vazio).
//
// Cada TestMetaDetects_* reproduz o MESMO ataque do vector homónimo mas com o
// controlo CONTORNADO/desligado, e prova que o ataque PASSA (não é bloqueado). São o
// controlo NEGATIVO: se a asserção de bloqueio de um vector fosse vácua (sempre
// "bloqueado"), o meta-teste correspondente — que assere o NÃO-bloqueio com o
// controlo desligado — falharia. Juntos, vector + meta provam que a suite discrimina
// genuinamente entre o ataque bloqueado e o mesmo fluxo sem o controlo.

// meta 1 — RUG-PULL com a chave do atacante ADICIONADA ao trust store (fronteira de
// confiança quebrada): a assinatura passa a validar e a promoção é ADMITIDA. Prova
// que o bloqueio do vector 1 depende de o publicador do rug-pull ser NÃO-confiável.
func TestMetaDetects_RugPull(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	legit := newSigner(t, keyLegit, seedLegit)
	attacker := newSigner(t, keyAttacker, seedAttack)
	// CONTROLO DESLIGADO: o trust store confia TAMBÉM no atacante.
	trust := newTrust(t, legit, attacker)
	verifier, err := signing.NewVerifier(trust, audit.NewMemStore(), signing.WithVerifierClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	reg := newRegistry(t, registry.WithAdmissionVerifier(verifier))

	const id = "tool.http.get"
	v := ver(2, 0, 0)
	c := contractWith("rug-pull-exfiltra", domain.EgressExternal, "vault:http")
	dig := sha256Digester.Digest(domain.KindTool, c)
	if _, err := reg.Publish(ctx, registry.PublishRequest{
		ID: id, Version: v, Kind: domain.KindTool, Contract: c,
		Origin: "x", Publisher: keyAttacker, Signature: attacker.Sign(id, v, dig),
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// Com o controlo contornado, o rug-pull é ADMITIDO — o vector NÃO bloquearia.
	if _, err := reg.SetStatus(ctx, id, v, domain.StatusActive); err != nil {
		t.Fatalf("com trust boundary quebrado o rug-pull devia ser admitido (prova de detecção): %v", err)
	}
}

// meta 2 — SCHEMA DRIFT sem mutação: observar o MESMO digest pinado NÃO é drift e
// mantém-se admitido. Prova que o bloqueio do vector 2 é causado pela divergência de
// digest, não por um "changed" espúrio.
func TestMetaDetects_SchemaDrift(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mon, err := tofu.NewMonitor(audit.NewMemStore(), tofu.WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewMonitor: %v", err)
	}
	const identity = "mcp://filesystem.host"
	v := ver(1, 0, 0)
	dig0, _ := tofu.DigestManifest([]byte(`{"tools":[{"name":"read_file"}]}`))
	if _, err := mon.Observe(ctx, tofu.Observation{Identity: identity, Version: v, Digest: dig0}); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if err := mon.Ratify(ctx, identity, v, dig0); err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	// CONTROLO NEGATIVO: sem mutação (mesmo digest) NÃO há drift → admitido.
	out, err := mon.Observe(ctx, tofu.Observation{Identity: identity, Version: v, Digest: dig0})
	if err != nil {
		t.Fatalf("re-observação idêntica devia passar (prova de detecção): %v", err)
	}
	if !out.Admitted || out.Drift {
		t.Fatalf("sem mutação o outcome = %+v, quer admitido e sem drift", out)
	}
}

// meta 3 — RUG-PULL MID-RUN sem mutação: a definição actual == congelada → PERMITIDA.
// Prova que o bloqueio do vector 3 é causado pela divergência de digest da definição.
func TestMetaDetects_RugPullMidRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	legit := newSigner(t, keyLegit, seedLegit)
	trust := newTrust(t, legit)
	const id = "tool.http.post"
	v := ver(1, 3, 0)
	entry0 := signedEntry(id, v, domain.KindTool, contractWith("frozen", domain.EgressExternal, "vault:http"), legit)
	frozen, err := toolset.FreezeToolSet(ctx, fakeCatalog{entries: []domain.Entry{entry0}}, "run-42", nil, toolset.WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("FreezeToolSet: %v", err)
	}
	rev, err := revalidation.New(trust, audit.NewMemStore(),
		revalidation.WithDigester(sha256Digester), revalidation.WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("revalidation.New: %v", err)
	}
	// CONTROLO NEGATIVO: definição íntegra (não mutada) → despacho PERMITIDO.
	dec, err := rev.Revalidate(ctx, revalidation.Request{
		RunID: "run-42", StepID: "step-1", ToolID: id,
		Current: entry0, Frozen: frozen,
		Policy: revalidation.Policy{AllowedScopes: []string{"vault:http"}, MaxEgress: domain.EgressExternal},
	})
	if err != nil {
		t.Fatalf("Revalidate: %v", err)
	}
	if !dec.Allowed {
		t.Fatalf("definição íntegra devia ser permitida (prova de detecção): stage=%s reason=%s", dec.Stage, dec.Reason)
	}
}

// meta 4 — TOOL POISONING via fonte CONFIÁVEL: o MESMO texto envenenado, se ingerido
// como authenticated_user (trusted), iria para a TrustedView (control-plane) e PODERIA
// comandar o planeador. Prova que a neutralização do vector 4 é causada pela
// classificação untrusted de mcp_schema — se AOS-042 a misclassificasse, o poison
// comandaria o planeador.
func TestMetaDetects_ToolPoisoning(t *testing.T) {
	t.Parallel()
	if provenance.Classify(provenance.SourceAuthenticatedUser) != provenance.Trusted {
		t.Fatal("fixture inválida: authenticated_user devia ser trusted")
	}
	ing := provenance.NewIngestor(nil)
	part := provenance.NewPartition(nil)
	// CONTROLO NEGATIVO: o MESMO conteúdo por uma fonte TRUSTED cai no control-plane.
	part.Admit(ingestMCPSchema(t, ing, "tool.evil", poisonMarker, provenance.SourceAuthenticatedUser))
	if part.TrustedView().Len() != 1 {
		t.Fatalf("conteúdo trusted devia entrar no control-plane (prova de detecção): TrustedView len=%d", part.TrustedView().Len())
	}
	if part.Quarantine().Len() != 0 {
		t.Fatalf("conteúdo trusted NÃO devia ser posto em quarentena: len=%d", part.Quarantine().Len())
	}
}

// meta 5 — RESOLUÇÃO PINADA (não flutuante): uma versão SemVer exacta resolve com
// sucesso. Prova que a rejeição do vector 5 é específica das referências flutuantes,
// não uma recusa cega de toda a resolução.
func TestMetaDetects_FloatingResolution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reg := newRegistry(t)
	const id = "tool.search"
	v := ver(1, 0, 0)
	if _, err := reg.Publish(ctx, registry.PublishRequest{
		ID: id, Version: v, Kind: domain.KindTool, Contract: contractWith("v1", domain.EgressInternal), Origin: "self", Publisher: keyLegit,
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// CONTROLO NEGATIVO: a resolução PINADA exacta passa.
	if _, err := reg.ResolveString(ctx, id, "1.0.0"); err != nil {
		t.Fatalf("resolução pinada exacta devia passar (prova de detecção): %v", err)
	}
}

// meta 6 — CAPACIDADE NO CATÁLOGO: uma tool publicada e activa é admissível. Prova
// que o default-deny do vector 6 é específico da AUSÊNCIA, não um deny universal.
func TestMetaDetects_OutOfCatalog(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	legit := newSigner(t, keyLegit, seedLegit)
	verifier, err := signing.NewVerifier(newTrust(t, legit), audit.NewMemStore(), signing.WithVerifierClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	reg := newRegistry(t, registry.WithAdmissionVerifier(verifier))
	const id = "tool.present"
	v := ver(1, 0, 0)
	c := contractWith("present", domain.EgressInternal)
	dig := sha256Digester.Digest(domain.KindTool, c)
	if _, err := reg.Publish(ctx, registry.PublishRequest{
		ID: id, Version: v, Kind: domain.KindTool, Contract: c, Origin: "self", Publisher: keyLegit, Signature: legit.Sign(id, v, dig),
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if _, err := reg.SetStatus(ctx, id, v, domain.StatusActive); err != nil {
		t.Fatalf("SetStatus active: %v", err)
	}
	// CONTROLO NEGATIVO: uma capacidade PRESENTE e activa é admissível.
	ok, _, err := reg.IsAdmissible(ctx, id, v)
	if err != nil || !ok {
		t.Fatalf("capacidade no catálogo devia ser admissível (prova de detecção): ok=%v err=%v", ok, err)
	}
}

// meta 7 — REPLAY INFIEL demonstrado: resolver a tool EVOLUÍDA (v2) dá um digest
// DIFERENTE do pinado no manifesto. Prova que o manifesto é LOAD-BEARING para a
// fidelidade — um replay que ignorasse o pin e resolvesse a versão corrente
// reproduziria o passado de forma INFIEL.
func TestMetaDetects_UnfaithfulReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reg := newRegistry(t)
	const id = "tool.summarize"
	v1 := ver(1, 0, 0)
	if _, err := reg.Publish(ctx, registry.PublishRequest{
		ID: id, Version: v1, Kind: domain.KindTool, Contract: contractWith("v1", domain.EgressNone), Origin: "self", Publisher: keyLegit,
	}); err != nil {
		t.Fatalf("Publish v1: %v", err)
	}
	e1, err := reg.Resolve(ctx, id, v1)
	if err != nil {
		t.Fatalf("Resolve v1: %v", err)
	}
	manifest, err := registry.NewDependencyManifest("traj-1", "model-x", "prompt-hash-abc", []domain.Entry{e1})
	if err != nil {
		t.Fatalf("NewDependencyManifest: %v", err)
	}
	pinnedDigest := manifest.Deps()[0].Digest

	v2 := ver(2, 0, 0)
	if _, err := reg.Publish(ctx, registry.PublishRequest{
		ID: id, Version: v2, Kind: domain.KindTool, Contract: contractWith("v2-evoluido", domain.EgressExternal, "vault:new"), Origin: "self", Publisher: keyLegit,
	}); err != nil {
		t.Fatalf("Publish v2: %v", err)
	}
	e2, err := reg.Resolve(ctx, id, v2)
	if err != nil {
		t.Fatalf("Resolve v2: %v", err)
	}
	// CONTROLO NEGATIVO: a versão evoluída diverge do pin — um replay que a usasse
	// seria INFIEL. É exactamente o que o manifesto impede ao pinar version+digest.
	if e2.Digest == pinnedDigest {
		t.Fatal("a evolução devia divergir do pin (prova de detecção): sem divergência não haveria replay infiel a prevenir")
	}
}

// ---------------------------------------------------------------------------
// RELATÓRIO DA SUITE — capturado pelo gate CI (scripts/ci/supplychain.sh).
// ---------------------------------------------------------------------------

// TestSuiteReportEmitted emite o RELATÓRIO agregado (linha marcada
// AOS_SUPPLYCHAIN_REPORT) que o gate CI captura e sobre o qual falha-fecha (pass
// agregado != true). Corre os predicados de BLOQUEIO dos sete vectores (têm de estar
// TODOS bloqueados) e os predicados de DETECÇÃO (o mesmo fluxo com o controlo
// desligado NÃO bloqueia) — o veredicto só é true se ambos se verificarem. À imagem
// do AOS_REPLAY_REPORT (AOS-024) e do AOS_MEMORY_REPORT (AOS-044): o campo agregado
// "pass" é o ÚLTIMO do objecto (…,"pass":true}), pelo que o gate ancora ao fim da linha.
func TestSuiteReportEmitted(t *testing.T) {
	checks := []struct {
		name string
		ok   bool
	}{
		{"rug_pull_blocked", probeRugPullBlocked()},
		{"schema_drift_blocked", probeSchemaDriftBlocked()},
		{"rug_pull_mid_run_blocked", probeMidRunBlocked()},
		{"tool_poisoning_untrusted", probePoisoningUntrusted()},
		{"floating_resolution_rejected", probeFloatingRejected()},
		{"out_of_catalog_default_deny", probeOutOfCatalogDenied()},
		{"unfaithful_replay_prevented", probeReplayFaithful()},
		// Detecção (meta): com o controlo desligado, o rug-pull PASSA (não-vazio).
		{"detection_nonvacuous", probeRugPullAdmittedWhenTrustBroken()},
	}
	pass := true
	var b strings.Builder
	b.WriteString("AOS_SUPPLYCHAIN_REPORT {")
	for i, c := range checks {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\"" + c.name + "\":" + boolStr(c.ok))
		if !c.ok {
			pass = false
		}
	}
	b.WriteString(",\"pass\":" + boolStr(pass) + "}")
	if !pass {
		t.Fatalf("relatório da suite indica falha: %s", b.String())
	}
	fmt.Println(b.String())
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// ---------------------------------------------------------------------------
// Probes: predicados PUROS reutilizados pelo relatório (sem *testing.T), cada um
// orquestrando o controlo real e devolvendo se o vector é bloqueado/detectado.
// ---------------------------------------------------------------------------

func probeRugPullBlocked() bool {
	ctx := context.Background()
	legit, _ := signing.NewSigner(keyLegit, keyFromSeed(seedLegit))
	attacker, _ := signing.NewSigner(keyAttacker, keyFromSeed(seedAttack))
	ts, _ := signing.NewTrustStore(audit.NewMemStore(), signing.WithTrustClock(fixedClock()))
	_ = ts.Add(ctx, legit.KeyID(), legit.PublicKey())
	verifier, _ := signing.NewVerifier(ts, audit.NewMemStore(), signing.WithVerifierClock(fixedClock()))
	reg := probeRegistry(registry.WithAdmissionVerifier(verifier))
	const id = "tool.x"
	v := ver(2, 0, 0)
	c := contractWith("rug", domain.EgressExternal, "vault:http")
	dig := sha256Digester.Digest(domain.KindTool, c)
	if _, err := reg.Publish(ctx, registry.PublishRequest{ID: id, Version: v, Kind: domain.KindTool, Contract: c, Origin: "x", Publisher: keyLegit, Signature: attacker.Sign(id, v, dig)}); err != nil {
		return false
	}
	_, err := reg.SetStatus(ctx, id, v, domain.StatusActive)
	return errors.Is(err, registry.ErrAdmissionDenied)
}

func probeRugPullAdmittedWhenTrustBroken() bool {
	ctx := context.Background()
	legit, _ := signing.NewSigner(keyLegit, keyFromSeed(seedLegit))
	attacker, _ := signing.NewSigner(keyAttacker, keyFromSeed(seedAttack))
	ts, _ := signing.NewTrustStore(audit.NewMemStore(), signing.WithTrustClock(fixedClock()))
	_ = ts.Add(ctx, legit.KeyID(), legit.PublicKey())
	_ = ts.Add(ctx, attacker.KeyID(), attacker.PublicKey()) // fronteira quebrada
	verifier, _ := signing.NewVerifier(ts, audit.NewMemStore(), signing.WithVerifierClock(fixedClock()))
	reg := probeRegistry(registry.WithAdmissionVerifier(verifier))
	const id = "tool.x"
	v := ver(2, 0, 0)
	c := contractWith("rug", domain.EgressExternal, "vault:http")
	dig := sha256Digester.Digest(domain.KindTool, c)
	if _, err := reg.Publish(ctx, registry.PublishRequest{ID: id, Version: v, Kind: domain.KindTool, Contract: c, Origin: "x", Publisher: keyAttacker, Signature: attacker.Sign(id, v, dig)}); err != nil {
		return false
	}
	_, err := reg.SetStatus(ctx, id, v, domain.StatusActive)
	return err == nil // ADMITIDO: prova de que o bloqueio não é vácuo
}

func probeSchemaDriftBlocked() bool {
	ctx := context.Background()
	mon, _ := tofu.NewMonitor(audit.NewMemStore(), tofu.WithClock(fixedClock()))
	const identity = "mcp://h"
	v := ver(1, 0, 0)
	dig0, _ := tofu.DigestManifest([]byte(`{"a":1}`))
	dig1, _ := tofu.DigestManifest([]byte(`{"a":1,"b":2}`))
	_, _ = mon.Observe(ctx, tofu.Observation{Identity: identity, Version: v, Digest: dig0})
	_ = mon.Ratify(ctx, identity, v, dig0)
	out, err := mon.Observe(ctx, tofu.Observation{Identity: identity, Version: v, Digest: dig1})
	return errors.Is(err, tofu.ErrSchemaDrift) && out.State == domain.TrustChanged && !out.Admitted
}

func probeMidRunBlocked() bool {
	ctx := context.Background()
	legit, _ := signing.NewSigner(keyLegit, keyFromSeed(seedLegit))
	ts, _ := signing.NewTrustStore(audit.NewMemStore(), signing.WithTrustClock(fixedClock()))
	_ = ts.Add(ctx, legit.KeyID(), legit.PublicKey())
	const id = "tool.y"
	v := ver(1, 0, 0)
	entry0 := signedEntryPure(id, v, contractWith("f", domain.EgressExternal, "vault:http"), legit)
	frozen, err := toolset.FreezeToolSet(ctx, fakeCatalog{entries: []domain.Entry{entry0}}, "run", nil, toolset.WithClock(fixedClock()))
	if err != nil {
		return false
	}
	rev, _ := revalidation.New(ts, audit.NewMemStore(), revalidation.WithDigester(sha256Digester), revalidation.WithClock(fixedClock()))
	mutated := signedEntryPure(id, v, contractWith("mut", domain.EgressExternal, "vault:http"), legit)
	dec, err := rev.Revalidate(ctx, revalidation.Request{RunID: "run", StepID: "s", ToolID: id, Current: mutated, Frozen: frozen, Policy: revalidation.Policy{AllowedScopes: []string{"vault:http"}, MaxEgress: domain.EgressExternal}})
	return err == nil && !dec.Allowed && dec.Reason == revalidation.ReasonDigestMismatch
}

func probePoisoningUntrusted() bool {
	if provenance.Classify(provenance.SourceMCPSchema) != provenance.Untrusted {
		return false
	}
	ing := provenance.NewIngestor(nil)
	part := provenance.NewPartition(nil)
	rec := memdomain.Record{ID: "t", Class: memdomain.ClassWorking, Metadata: memdomain.Metadata{AgentID: "a", RunID: "r", CreatedAt: fixedClock()(), TTLClass: memdomain.TTLEphemeral, SchemaVersion: "1.0.0"}, Body: memdomain.WorkingBody{Content: poisonMarker}}
	ing2, err := ing.Ingest(context.Background(), rec, provenance.SourceMCPSchema)
	if err != nil {
		return false
	}
	part.Admit(ing2)
	if part.TrustedView().Len() != 0 || part.Quarantine().Len() != 1 {
		return false
	}
	for _, item := range part.Quarantine().Items() {
		if item.Taint() != provenance.Untrusted {
			return false
		}
		if _, ok := any(item).(provenance.PrivilegedAuthorizer); ok {
			return false
		}
	}
	return true
}

func probeFloatingRejected() bool {
	ctx := context.Background()
	reg := probeRegistry()
	const id = "tool.z"
	v := ver(1, 0, 0)
	if _, err := reg.Publish(ctx, registry.PublishRequest{ID: id, Version: v, Kind: domain.KindTool, Contract: contractWith("v", domain.EgressInternal), Origin: "self", Publisher: keyLegit}); err != nil {
		return false
	}
	_, err := reg.ResolveString(ctx, id, "latest")
	return errors.Is(err, registry.ErrFloatingResolution)
}

func probeOutOfCatalogDenied() bool {
	ctx := context.Background()
	reg := probeRegistry()
	if _, err := reg.GetDigest(ctx, "tool.absent", ver(1, 0, 0)); !errors.Is(err, registry.ErrNotFound) {
		return false
	}
	ok, _, err := reg.IsAdmissible(ctx, "tool.absent", ver(1, 0, 0))
	return err == nil && !ok
}

func probeReplayFaithful() bool {
	ctx := context.Background()
	reg := probeRegistry()
	const id = "tool.s"
	v1 := ver(1, 0, 0)
	if _, err := reg.Publish(ctx, registry.PublishRequest{ID: id, Version: v1, Kind: domain.KindTool, Contract: contractWith("v1", domain.EgressNone), Origin: "self", Publisher: keyLegit}); err != nil {
		return false
	}
	e1, err := reg.Resolve(ctx, id, v1)
	if err != nil {
		return false
	}
	manifest, err := registry.NewDependencyManifest("traj", "m", "ph", []domain.Entry{e1})
	if err != nil {
		return false
	}
	pinned := manifest.Deps()[0].Digest
	v2 := ver(2, 0, 0)
	if _, err := reg.Publish(ctx, registry.PublishRequest{ID: id, Version: v2, Kind: domain.KindTool, Contract: contractWith("v2", domain.EgressExternal, "vault:new"), Origin: "self", Publisher: keyLegit}); err != nil {
		return false
	}
	replayed, err := reg.Resolve(ctx, id, v1)
	if err != nil {
		return false
	}
	e2, err := reg.Resolve(ctx, id, v2)
	if err != nil {
		return false
	}
	return replayed.Digest == pinned && e2.Digest != pinned
}

// probeRegistry constrói um Registry de probe (sem *testing.T) — Event Store real,
// digester SHA-256, relógio determinista.
func probeRegistry(opts ...registry.Option) *registry.Registry {
	store, err := probeEventStore()
	if err != nil {
		panic(err)
	}
	all := append([]registry.Option{registry.WithClock(fixedClock()), registry.WithDigester(sha256Digester)}, opts...)
	reg, err := registry.New(store, all...)
	if err != nil {
		panic(err)
	}
	return reg
}

// signedEntryPure é a variante de [signedEntry] sem *testing.T, para os probes.
func signedEntryPure(id string, v domain.Version, c domain.Contract, signer *signing.Signer) domain.Entry {
	dig := sha256Digester.Digest(domain.KindTool, c)
	return domain.Entry{
		ID: id, Version: v, Kind: domain.KindTool, Digest: dig, Signature: signer.Sign(id, v, dig),
		Contract:   c,
		Provenance: domain.Provenance{Origin: "mcp://" + id, Publisher: signer.KeyID(), Trust: domain.TrustPinned},
		Status:     domain.StatusActive,
	}
}
