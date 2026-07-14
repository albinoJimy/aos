package procedural

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/memory/schema"
)

// ---------------------------------------------------------------------------
// Harness determinístico (relógio/chaves injectados; sem time.Now/rand).
// ---------------------------------------------------------------------------

const ratifierID = "human:ana.governanca"

func fixedClock() func() time.Time {
	t := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// keyFromSeed constrói uma chave ed25519 determinística a partir de um byte de
// semente (chaves de teste — nunca em produção).
func keyFromSeed(b byte) ed25519.PrivateKey {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = b
	}
	return ed25519.NewKeyFromSeed(seed)
}

func ver(major, minor, patch int) schema.Version {
	return schema.Version{Major: major, Minor: minor, Patch: patch}
}

// skillContentHash devolve o pin de integridade do conteúdo que harness.submit
// gera para (name,version) — usado para ligar a ratificação ao conteúdo revisto
// (a mensagem canónica de ratificação inclui o ContentHash).
func skillContentHash(name string, v schema.Version) []byte {
	return computeContentHash([]byte("skill body " + name + " " + v.String()))
}

// harness reúne o SkillMemory e os colaboradores para inspecção nos testes.
type harness struct {
	mem          *SkillMemory
	store        *audit.MemStore
	registry     *InMemorySkillRegistry
	signerKey    ed25519.PrivateKey
	ratifierKey  ed25519.PrivateKey
	tracer       *agentruntime.RecordingTracer
	evalMetrics  func(string, schema.Version) (float64, int)
	canaryMetric func(string, schema.Version) (float64, float64)
}

// newHarness cria um SkillMemory com gates que PASSAM por omissão (limiares
// generosos); os testes podem substituir as métricas para forçar falhas.
func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		store:       audit.NewMemStore(),
		registry:    NewInMemorySkillRegistry(),
		signerKey:   keyFromSeed(1),
		ratifierKey: keyFromSeed(2),
		tracer:      &agentruntime.RecordingTracer{},
	}
	// Métricas verdes por omissão.
	h.evalMetrics = func(string, schema.Version) (float64, int) { return 0.98, 0 }
	h.canaryMetric = func(string, schema.Version) (float64, float64) { return 0.99, 0.0 }

	evalGate := ThresholdEvalGate{
		MinGoldenSetScore:       0.90,
		MaxTraceDiffRegressions: 0,
		Metrics:                 func(n string, v schema.Version) (float64, int) { return h.evalMetrics(n, v) },
	}
	canaryGate := ThresholdCanaryGate{
		MinSuccessRate:      0.95,
		MaxUnsafeActionRate: 0.01,
		Metrics:             func(n string, v schema.Version) (float64, float64) { return h.canaryMetric(n, v) },
	}
	mem, err := NewSkillMemory(h.store, NewEd25519Signer(h.signerKey, "sys-kid-1"), h.registry, evalGate, canaryGate,
		WithClock(fixedClock()),
		WithTracer(h.tracer),
		WithRatifier(ratifierID, h.ratifierKey.Public().(ed25519.PublicKey)),
	)
	if err != nil {
		t.Fatalf("NewSkillMemory: %v", err)
	}
	h.mem = mem
	return h
}

func (h *harness) submit(t *testing.T, name string, v schema.Version) {
	t.Helper()
	content := []byte("skill body " + name + " " + v.String())
	man := NewManifest(name, v, "agent:planner", "run-123", content)
	if _, err := h.mem.Submit(context.Background(), man, content); err != nil {
		t.Fatalf("Submit(%s@%s): %v", name, v, err)
	}
}

// promoteToProd corre o pipeline completo até produção (gates verdes).
func (h *harness) promoteToProd(t *testing.T, name string, v schema.Version) {
	t.Helper()
	ctx := context.Background()
	h.submit(t, name, v)
	if _, _, err := h.mem.RunEvalGate(ctx, name, v); err != nil {
		t.Fatalf("RunEvalGate: %v", err)
	}
	if _, _, err := h.mem.RunCanary(ctx, name, v); err != nil {
		t.Fatalf("RunCanary: %v", err)
	}
	rat := SignRatification(h.ratifierKey, ratifierID, name, v, skillContentHash(name, v))
	if _, err := h.mem.Ratify(ctx, rat); err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if _, err := h.mem.Activate(ctx, name, v); err != nil {
		t.Fatalf("Activate: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Manifesto + SemVer + integração com o Registry.
// ---------------------------------------------------------------------------

func TestSubmit_ManifestSemVerAndRegistry(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name, v := "summarize", ver(1, 2, 0)
	content := []byte("body")
	man := NewManifest(name, v, "agent:planner", "run-1", content)

	tr, err := h.mem.Submit(ctx, man, content)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if tr.To != StageStaging {
		t.Fatalf("estado inicial = %q, quer staging", tr.To)
	}
	// O artefacto ficou pinado no Registry (pin+hash+assinatura).
	pin, ok, err := h.registry.Resolve(ctx, name, v)
	if err != nil || !ok {
		t.Fatalf("Resolve: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(pin.ContentHash, man.ContentHash) {
		t.Fatalf("pin ContentHash != manifesto")
	}
	if len(pin.Signature) != ed25519.SignatureSize {
		t.Fatalf("pin sem assinatura ed25519")
	}
	// A projecção para a classe procedural de AOS-035 carrega SemVer + stage.
	body := (Skill{Manifest: man}).ProceduralBody(StageStaging)
	if body.Version != "1.2.0" || body.Stage != "staging" {
		t.Fatalf("ProceduralBody = %+v", body)
	}
}

func TestSubmit_FailClosed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	content := []byte("body")
	goodMan := NewManifest("s", ver(1, 0, 0), "agent:x", "run-1", content)

	tests := []struct {
		name    string
		man     Manifest
		content []byte
		want    error
	}{
		{"manifesto sem nome", NewManifest("", ver(1, 0, 0), "agent:x", "run", content), content, ErrInvalidManifest},
		{"manifesto sem autor", NewManifest("s", ver(1, 0, 0), "", "run", content), content, ErrInvalidManifest},
		{"manifesto sem run_id", NewManifest("s", ver(1, 0, 0), "agent:x", "", content), content, ErrInvalidManifest},
		{"versao zero", NewManifest("s", ver(0, 0, 0), "agent:x", "run", content), content, ErrInvalidManifest},
		{"hash nao corresponde", goodMan, []byte("outro conteudo"), ErrContentHashMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := h.mem.Submit(ctx, tc.man, tc.content); !errors.Is(err, tc.want) {
				t.Fatalf("Submit err = %v, quer %v", err, tc.want)
			}
		})
	}

	// Duplicado: mesma (skill,versão) rejeitada (imutabilidade SemVer).
	if _, err := h.mem.Submit(ctx, goodMan, content); err != nil {
		t.Fatalf("primeiro Submit: %v", err)
	}
	if _, err := h.mem.Submit(ctx, goodMan, content); !errors.Is(err, ErrDuplicateVersion) {
		t.Fatalf("Submit duplicado err = %v, quer ErrDuplicateVersion", err)
	}
}

// ---------------------------------------------------------------------------
// BLOQUEIO: skill em staging NUNCA executável em prod (allowlist fail-closed).
// ---------------------------------------------------------------------------

func TestAllowlist_StagingNeverExecutableInProd(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name, v := "risky", ver(1, 0, 0)

	// Progressão parcial pelo pipeline; em CADA passo antes de Activate a skill NÃO
	// é executável em prod (a allowlist de prod exclui-a estruturalmente).
	steps := []struct {
		label string
		run   func()
	}{
		{"apos submit (staging)", func() { h.submit(t, name, v) }},
		{"apos eval-gate", func() {
			if _, _, err := h.mem.RunEvalGate(ctx, name, v); err != nil {
				t.Fatalf("RunEvalGate: %v", err)
			}
		}},
		{"apos canary", func() {
			if _, _, err := h.mem.RunCanary(ctx, name, v); err != nil {
				t.Fatalf("RunCanary: %v", err)
			}
		}},
		{"apos ratificacao", func() {
			if _, err := h.mem.Ratify(ctx, SignRatification(h.ratifierKey, ratifierID, name, v, skillContentHash(name, v))); err != nil {
				t.Fatalf("Ratify: %v", err)
			}
		}},
	}
	for _, s := range steps {
		s.run()
		if h.mem.IsExecutableInProd(name, v) {
			t.Fatalf("%s: skill executável em prod ANTES de Activate (allowlist furada)", s.label)
		}
		if err := h.mem.ExecuteInProd(ctx, name, v); !errors.Is(err, ErrNotExecutableInProd) {
			t.Fatalf("%s: ExecuteInProd err = %v, quer ErrNotExecutableInProd", s.label, err)
		}
		if _, ok := h.mem.ProdVersion(name); ok {
			t.Fatalf("%s: existe versão prod antes de Activate", s.label)
		}
	}

	// Só depois de Activate (pipeline completo) a skill entra na allowlist.
	if _, err := h.mem.Activate(ctx, name, v); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if !h.mem.IsExecutableInProd(name, v) {
		t.Fatalf("skill não executável em prod após pipeline completo")
	}
	if err := h.mem.ExecuteInProd(ctx, name, v); err != nil {
		t.Fatalf("ExecuteInProd após activação: %v", err)
	}
}

// ---------------------------------------------------------------------------
// GATE: sem eval-gate verde + canary + ratificação assinada → activação RECUSADA.
// Cobre também a impossibilidade de auto-promoção (activar sem gates externos).
// ---------------------------------------------------------------------------

func TestActivation_RefusedWithoutAllGates(t *testing.T) {
	ctx := context.Background()
	name, v := "gated", ver(2, 0, 0)

	tests := []struct {
		name   string
		arrive func(t *testing.T, h *harness) // leva a skill até ao ponto pré-Activate
		want   error
	}{
		{
			name:   "sem nada (auto-promocao) recusada",
			arrive: func(t *testing.T, h *harness) { h.submit(t, name, v) },
			want:   ErrActivationRefused,
		},
		{
			name: "eval-gate vermelho recusa",
			arrive: func(t *testing.T, h *harness) {
				h.evalMetrics = func(string, schema.Version) (float64, int) { return 0.50, 3 } // abaixo do limiar
				h.submit(t, name, v)
				if _, _, err := h.mem.RunEvalGate(ctx, name, v); !errors.Is(err, ErrEvalGateNotPassed) {
					t.Fatalf("RunEvalGate err = %v, quer ErrEvalGateNotPassed", err)
				}
			},
			want: ErrActivationRefused,
		},
		{
			name: "so eval-gate (sem canary) recusa",
			arrive: func(t *testing.T, h *harness) {
				h.submit(t, name, v)
				if _, _, err := h.mem.RunEvalGate(ctx, name, v); err != nil {
					t.Fatalf("RunEvalGate: %v", err)
				}
			},
			want: ErrActivationRefused,
		},
		{
			name: "eval+canary (sem ratificacao) recusa",
			arrive: func(t *testing.T, h *harness) {
				h.submit(t, name, v)
				if _, _, err := h.mem.RunEvalGate(ctx, name, v); err != nil {
					t.Fatalf("RunEvalGate: %v", err)
				}
				if _, _, err := h.mem.RunCanary(ctx, name, v); err != nil {
					t.Fatalf("RunCanary: %v", err)
				}
			},
			want: ErrActivationRefused,
		},
		{
			name: "ratificacao com assinatura invalida recusa",
			arrive: func(t *testing.T, h *harness) {
				h.submit(t, name, v)
				if _, _, err := h.mem.RunEvalGate(ctx, name, v); err != nil {
					t.Fatalf("RunEvalGate: %v", err)
				}
				if _, _, err := h.mem.RunCanary(ctx, name, v); err != nil {
					t.Fatalf("RunCanary: %v", err)
				}
				// Assinatura de uma chave NÃO autorizada (impostor).
				bad := SignRatification(keyFromSeed(9), ratifierID, name, v, skillContentHash(name, v))
				if _, err := h.mem.Ratify(ctx, bad); !errors.Is(err, ErrRatificationInvalid) {
					t.Fatalf("Ratify(impostor) err = %v, quer ErrRatificationInvalid", err)
				}
			},
			want: ErrActivationRefused,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tc.arrive(t, h)
			before, _ := h.store.Head(ctx, auditPartition(name))
			if _, err := h.mem.Activate(ctx, name, v); !errors.Is(err, tc.want) {
				t.Fatalf("Activate err = %v, quer %v", err, tc.want)
			}
			if h.mem.IsExecutableInProd(name, v) {
				t.Fatalf("skill recusada mas executável em prod")
			}
			// AOS-040-C2: a activação recusada por gates incompletos é selada como deny
			// no audit assinado (evento de segurança com rasto forense).
			assertLastDeny(t, h, name, "memory:procedural:activate", "gates_incomplete", before)
		})
	}
}

func TestRatify_NotAuthorizedRatifier(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name, v := "s", ver(1, 0, 0)
	h.submit(t, name, v)
	if _, _, err := h.mem.RunEvalGate(ctx, name, v); err != nil {
		t.Fatalf("eval: %v", err)
	}
	if _, _, err := h.mem.RunCanary(ctx, name, v); err != nil {
		t.Fatalf("canary: %v", err)
	}
	// Ratificador desconhecido (não na allowlist), assinatura bem-formada.
	before, _ := h.store.Head(ctx, auditPartition(name))
	rat := SignRatification(h.ratifierKey, "human:desconhecido", name, v, skillContentHash(name, v))
	if _, err := h.mem.Ratify(ctx, rat); !errors.Is(err, ErrRatifierNotAuthorized) {
		t.Fatalf("Ratify err = %v, quer ErrRatifierNotAuthorized", err)
	}
	// AOS-040-C2: a tentativa de ratificação por chave não autorizada é selada como
	// deny no audit assinado (evento de segurança com rasto forense).
	assertLastDeny(t, h, name, "memory:procedural:ratify", "ratifier_not_authorized", before)
}

// assertLastDeny confirma que a última entrada de audit da partição de name é uma
// DecisionDeny selada com a capability e o refused_reason esperados, e que o head
// avançou face a before (a recusa foi mesmo registada na hash-chain assinada).
func assertLastDeny(t *testing.T, h *harness, name, capability, reason string, before uint64) {
	t.Helper()
	ctx := context.Background()
	part := auditPartition(name)
	head, _ := h.store.Head(ctx, part)
	if head <= before {
		t.Fatalf("recusa não selou audit: head %d ≤ before %d", head, before)
	}
	recs, err := h.store.Read(ctx, part, head, head)
	if err != nil || len(recs) != 1 {
		t.Fatalf("Read(last): err=%v n=%d", err, len(recs))
	}
	rec := recs[0]
	if rec.Decision != audit.DecisionDeny {
		t.Fatalf("última entrada decision = %v, quer deny", rec.Decision)
	}
	if rec.Capability != capability {
		t.Fatalf("última entrada capability = %q, quer %q", rec.Capability, capability)
	}
	if len(rec.Obligations) != 1 || rec.Obligations[0].Params["refused_reason"] != reason {
		t.Fatalf("última entrada refused_reason = %q, quer %q", rec.Obligations[0].Params["refused_reason"], reason)
	}
	// A recusa também é ASSINADA (fica atribuível, não só tamper-evident).
	if rec.Obligations[0].Params["sig_alg"] != "ed25519" || rec.Obligations[0].Params["signature"] == "" {
		t.Fatalf("recusa selada sem assinatura ed25519")
	}
}

// ---------------------------------------------------------------------------
// ROLLBACK ATÓMICO em regressão (retorno à versão anterior sem downtime).
// ---------------------------------------------------------------------------

func TestRollback_AtomicOnRegression(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := "planner"
	v1, v2 := ver(1, 0, 0), ver(1, 1, 0)

	h.promoteToProd(t, name, v1)
	if got, _ := h.mem.ProdVersion(name); !got.Equal(v1) {
		t.Fatalf("prod = %v, quer v1", got)
	}
	h.promoteToProd(t, name, v2)
	if got, _ := h.mem.ProdVersion(name); !got.Equal(v2) {
		t.Fatalf("prod = %v, quer v2", got)
	}
	// v1 já não é a activa (v2 é); só v2 é executável.
	if h.mem.IsExecutableInProd(name, v1) || !h.mem.IsExecutableInProd(name, v2) {
		t.Fatalf("allowlist errada após activar v2")
	}

	// Regressão detectada em v2 → rollback ATÓMICO para v1.
	tr, err := h.mem.HandleRegression(ctx, name, RegressionSignal{Regressed: true, Reason: "success_rate<0.9"})
	if err != nil {
		t.Fatalf("HandleRegression: %v", err)
	}
	if tr.To != StageRolledBack {
		t.Fatalf("transição = %q, quer rolled_back", tr.To)
	}
	// v1 restaurada e executável; v2 revertida e NÃO executável.
	if got, _ := h.mem.ProdVersion(name); !got.Equal(v1) {
		t.Fatalf("após rollback prod = %v, quer v1", got)
	}
	if !h.mem.IsExecutableInProd(name, v1) || h.mem.IsExecutableInProd(name, v2) {
		t.Fatalf("allowlist errada após rollback")
	}
	if st, _ := h.mem.StageOf(name, v2); st != StageRolledBack {
		t.Fatalf("v2 stage = %q, quer rolled_back", st)
	}
	if st, _ := h.mem.StageOf(name, v1); st != StageProduction {
		t.Fatalf("v1 stage = %q, quer production", st)
	}
}

func TestRollback_NoPreviousDeactivates(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name, v1 := "solo", ver(1, 0, 0)
	h.promoteToProd(t, name, v1)

	// Regressão na primeira versão em prod: sem alvo de reversão → DESACTIVA.
	_, err := h.mem.HandleRegression(ctx, name, RegressionSignal{Regressed: true})
	if !errors.Is(err, ErrNoPreviousVersion) {
		t.Fatalf("HandleRegression err = %v, quer ErrNoPreviousVersion", err)
	}
	if _, ok := h.mem.ProdVersion(name); ok {
		t.Fatalf("skill continua em prod após rollback sem alvo (devia desactivar)")
	}
	if h.mem.IsExecutableInProd(name, v1) {
		t.Fatalf("skill desactivada mas executável")
	}
}

func TestHandleRegression_NoRegressionIsNoop(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name, v1 := "s", ver(1, 0, 0)
	h.promoteToProd(t, name, v1)
	before, _ := h.store.Head(ctx, auditPartition(name))
	if _, err := h.mem.HandleRegression(ctx, name, RegressionSignal{Regressed: false}); err != nil {
		t.Fatalf("HandleRegression(no regression): %v", err)
	}
	after, _ := h.store.Head(ctx, auditPartition(name))
	if before != after {
		t.Fatalf("no-op de regressão tocou no audit (%d→%d)", before, after)
	}
	if !h.mem.IsExecutableInProd(name, v1) {
		t.Fatalf("skill deixou de ser executável sem regressão")
	}
}

// Rollback sem downtime: durante o swap, um leitor concorrente vê SEMPRE uma
// versão prod válida (v1 ou v2), nunca vazio. Corre com -race.
func TestRollback_NoDowntimeUnderConcurrentReads(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := "hot"
	v1, v2 := ver(1, 0, 0), ver(2, 0, 0)
	h.promoteToProd(t, name, v1)
	h.promoteToProd(t, name, v2)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					v, ok := h.mem.ProdVersion(name)
					if !ok || (!v.Equal(v1) && !v.Equal(v2)) {
						t.Errorf("leitura de prod inválida durante swap: %v ok=%v", v, ok)
						return
					}
				}
			}
		}()
	}
	if _, err := h.mem.HandleRegression(ctx, name, RegressionSignal{Regressed: true}); err != nil {
		t.Fatalf("HandleRegression: %v", err)
	}
	close(stop)
	wg.Wait()
	if got, _ := h.mem.ProdVersion(name); !got.Equal(v1) {
		t.Fatalf("prod final = %v, quer v1", got)
	}
}

// ---------------------------------------------------------------------------
// AUDIT TRAIL ASSINADO: cada transição selada na hash-chain E assinada ed25519.
// ---------------------------------------------------------------------------

func TestSignedTransitions_EveryTransitionAuditedAndSigned(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := "audited"
	v1, v2 := ver(1, 0, 0), ver(1, 1, 0)
	h.promoteToProd(t, name, v1)
	h.promoteToProd(t, name, v2)
	if _, err := h.mem.HandleRegression(ctx, name, RegressionSignal{Regressed: true, Reason: "unsafe_rate>0.05"}); err != nil {
		t.Fatalf("HandleRegression: %v", err)
	}

	part := auditPartition(name)
	head, _ := h.store.Head(ctx, part)
	// v1: submit, eval, canary, ratify, activate = 5; v2: idem = 5; rollback = 1.
	if head != 11 {
		t.Fatalf("registos de audit = %d, quer 11", head)
	}
	recs, err := h.store.Read(ctx, part, 1, head)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	pub := h.signerKey.Public().(ed25519.PublicKey)
	for _, rec := range recs {
		if len(rec.Obligations) != 1 || rec.Obligations[0].Type != "procedural_transition" {
			t.Fatalf("seq %d: obligation inesperada %+v", rec.AuditSeq, rec.Obligations)
		}
		params := rec.Obligations[0].Params
		if params["sig_alg"] != "ed25519" || params["signature"] == "" {
			t.Fatalf("seq %d: transição sem assinatura selada", rec.AuditSeq)
		}
		// A assinatura selada verifica contra a chave do sistema sobre o payload
		// canónico reconstruído a partir do registo.
		sig, err := base64.RawStdEncoding.DecodeString(params["signature"])
		if err != nil {
			t.Fatalf("seq %d: base64 assinatura: %v", rec.AuditSeq, err)
		}
		payload := canonicalTransition(name, versionFromResource(t, rec.Resource.Value),
			Stage(params["from"]), Stage(params["to"]), params["actor"], rec.Timestamp.UnixNano(), transitionExtra(params))
		if !ed25519.Verify(pub, payload, sig) {
			t.Fatalf("seq %d (%s→%s): assinatura ed25519 inválida", rec.AuditSeq, params["from"], params["to"])
		}
	}
	// A ratificação carrega a assinatura HUMANA selada.
	foundRatify := false
	for _, rec := range recs {
		if rec.Obligations[0].Params["to"] == string(StageRatified) {
			foundRatify = true
			if rec.Decision != audit.DecisionEscalate {
				t.Fatalf("ratificação não é escalate: %v", rec.Decision)
			}
			hs := rec.Obligations[0].Params["human_signature"]
			if hs == "" {
				t.Fatalf("ratificação sem assinatura humana selada")
			}
			raw, err := base64.RawStdEncoding.DecodeString(hs)
			if err != nil || len(raw) != ed25519.SignatureSize {
				t.Fatalf("assinatura humana malformada")
			}
		}
	}
	if !foundRatify {
		t.Fatalf("nenhuma transição de ratificação no audit")
	}
}

func TestVerifySignedTransition(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name, v := "s", ver(1, 0, 0)
	tr, err := func() (SignedTransition, error) {
		content := []byte("b")
		man := NewManifest(name, v, "agent:x", "run", content)
		return h.mem.Submit(ctx, man, content)
	}()
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	pub := h.signerKey.Public().(ed25519.PublicKey)
	if !VerifySignedTransition(pub, tr) {
		t.Fatalf("VerifySignedTransition falhou para transição válida")
	}
	// Chave errada não verifica.
	other := keyFromSeed(7).Public().(ed25519.PublicKey)
	if VerifySignedTransition(other, tr) {
		t.Fatalf("VerifySignedTransition aceitou chave errada")
	}
	// Payload adulterado não verifica.
	tr.Payload = append(tr.Payload, 0xFF)
	if VerifySignedTransition(pub, tr) {
		t.Fatalf("VerifySignedTransition aceitou payload adulterado")
	}
}

// ---------------------------------------------------------------------------
// AUDIT-BEFORE-EFFECT / fail-closed sobre a porta de audit (PROC-001/PROC-002):
// se o Append do Store falhar, a transição NÃO se compromete (a skill nunca fica
// executável/avançada à frente da cadeia de audit assinada).
// ---------------------------------------------------------------------------

var errAuditDown = errors.New("audit indisponivel")

// flakyStore embrulha um audit.Store e permite forçar a falha de Append (audit
// indisponível) para exercitar o caminho fail-closed que o MemStore nunca activa.
type flakyStore struct {
	audit.Store
	failAppend bool
}

func (s *flakyStore) Append(ctx context.Context, rec audit.AuditRecord) (audit.AuditRecord, error) {
	if s.failAppend {
		return audit.AuditRecord{}, errAuditDown
	}
	return s.Store.Append(ctx, rec)
}

func newFlakyHarness(t *testing.T) (*SkillMemory, *flakyStore, ed25519.PrivateKey) {
	t.Helper()
	fs := &flakyStore{Store: audit.NewMemStore()}
	ratKey := keyFromSeed(2)
	evalGate := ThresholdEvalGate{
		MinGoldenSetScore:       0.90,
		MaxTraceDiffRegressions: 0,
		Metrics:                 func(string, schema.Version) (float64, int) { return 0.98, 0 },
	}
	canaryGate := ThresholdCanaryGate{
		MinSuccessRate:      0.95,
		MaxUnsafeActionRate: 0.01,
		Metrics:             func(string, schema.Version) (float64, float64) { return 0.99, 0.0 },
	}
	mem, err := NewSkillMemory(fs, NewEd25519Signer(keyFromSeed(1), "sys-kid-1"), NewInMemorySkillRegistry(), evalGate, canaryGate,
		WithClock(fixedClock()),
		WithRatifier(ratifierID, ratKey.Public().(ed25519.PublicKey)),
	)
	if err != nil {
		t.Fatalf("NewSkillMemory: %v", err)
	}
	return mem, fs, ratKey
}

func TestActivate_AuditFailClosedDoesNotActivate(t *testing.T) {
	ctx := context.Background()
	mem, fs, ratKey := newFlakyHarness(t)
	name, v := "failclosed", ver(1, 0, 0)
	content := []byte("skill body " + name + " " + v.String())
	man := NewManifest(name, v, "agent:planner", "run-123", content)

	if _, err := mem.Submit(ctx, man, content); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if _, _, err := mem.RunEvalGate(ctx, name, v); err != nil {
		t.Fatalf("eval: %v", err)
	}
	if _, _, err := mem.RunCanary(ctx, name, v); err != nil {
		t.Fatalf("canary: %v", err)
	}
	if _, err := mem.Ratify(ctx, SignRatification(ratKey, ratifierID, name, v, skillContentHash(name, v))); err != nil {
		t.Fatalf("Ratify: %v", err)
	}

	// Audit INDISPONÍVEL no instante exacto da activação.
	fs.failAppend = true
	if _, err := mem.Activate(ctx, name, v); !errors.Is(err, errAuditDown) {
		t.Fatalf("Activate err = %v, quer errAuditDown", err)
	}
	// PROC-001: a skill NÃO ficou executável em prod nem avançou para production
	// (audit-before-effect: nenhum efeito sem trilho forense assinado).
	if mem.IsExecutableInProd(name, v) {
		t.Fatalf("FAIL-OPEN: skill executável em prod apesar de o audit ter falhado")
	}
	if err := mem.ExecuteInProd(ctx, name, v); !errors.Is(err, ErrNotExecutableInProd) {
		t.Fatalf("ExecuteInProd err = %v, quer ErrNotExecutableInProd", err)
	}
	if st, _ := mem.StageOf(name, v); st == StageProduction {
		t.Fatalf("stage = production apesar de o audit ter falhado")
	}

	// Recuperado o audit, a activação volta a ser possível E selada.
	fs.failAppend = false
	if _, err := mem.Activate(ctx, name, v); err != nil {
		t.Fatalf("Activate após recuperação: %v", err)
	}
	if !mem.IsExecutableInProd(name, v) {
		t.Fatalf("skill não executável após activação bem-sucedida pós-recuperação")
	}
}

func TestRunEvalGate_AuditFailClosedDoesNotAdvance(t *testing.T) {
	ctx := context.Background()
	mem, fs, _ := newFlakyHarness(t)
	name, v := "evalfailclosed", ver(1, 0, 0)
	content := []byte("skill body " + name + " " + v.String())
	man := NewManifest(name, v, "agent:planner", "run-123", content)
	if _, err := mem.Submit(ctx, man, content); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Audit indisponível durante o eval-gate: NÃO avança de staging.
	fs.failAppend = true
	if _, _, err := mem.RunEvalGate(ctx, name, v); !errors.Is(err, errAuditDown) {
		t.Fatalf("RunEvalGate err = %v, quer errAuditDown", err)
	}
	if st, _ := mem.StageOf(name, v); st != StageStaging {
		t.Fatalf("PROC-002: stage = %q após audit falhar, quer staging (não avançou)", st)
	}
	// Como o eval-gate não avançou, o canary continua bloqueado (fail-closed).
	if _, _, err := mem.RunCanary(ctx, name, v); !errors.Is(err, ErrEvalGateNotPassed) {
		t.Fatalf("RunCanary err = %v, quer ErrEvalGateNotPassed", err)
	}
}

// ---------------------------------------------------------------------------
// Construção fail-closed.
// ---------------------------------------------------------------------------

func TestNewSkillMemory_NilPortsFailClosed(t *testing.T) {
	store := audit.NewMemStore()
	signer := NewEd25519Signer(keyFromSeed(1), "k")
	reg := NewInMemorySkillRegistry()
	eg := ThresholdEvalGate{Metrics: func(string, schema.Version) (float64, int) { return 1, 0 }}
	cg := ThresholdCanaryGate{Metrics: func(string, schema.Version) (float64, float64) { return 1, 0 }}

	tests := []struct {
		name string
		call func() (*SkillMemory, error)
		want error
	}{
		{"nil store", func() (*SkillMemory, error) { return NewSkillMemory(nil, signer, reg, eg, cg) }, ErrNilAuditStore},
		{"nil signer", func() (*SkillMemory, error) { return NewSkillMemory(store, nil, reg, eg, cg) }, ErrNilSigner},
		{"nil registry", func() (*SkillMemory, error) { return NewSkillMemory(store, signer, nil, eg, cg) }, ErrNilRegistry},
		{"nil eval", func() (*SkillMemory, error) { return NewSkillMemory(store, signer, reg, nil, cg) }, ErrNilEvalGate},
		{"nil canary", func() (*SkillMemory, error) { return NewSkillMemory(store, signer, reg, eg, nil) }, ErrNilCanaryGate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.call(); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, quer %v", err, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// OTel: os spans de eval-gate e canary carregam gen_ai.evaluation.result.
// ---------------------------------------------------------------------------

func TestSpans_EvaluationResultLinked(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name, v := "obs", ver(1, 0, 0)
	h.submit(t, name, v)
	if _, _, err := h.mem.RunEvalGate(ctx, name, v); err != nil {
		t.Fatalf("eval: %v", err)
	}
	if _, _, err := h.mem.RunCanary(ctx, name, v); err != nil {
		t.Fatalf("canary: %v", err)
	}
	assertEvalResult(t, h.tracer, opEvalGate, "pass")
	assertEvalResult(t, h.tracer, opCanary, "pass")
}

func assertEvalResult(t *testing.T, tr *agentruntime.RecordingTracer, op, want string) {
	t.Helper()
	for _, s := range tr.SpansByOperation(op) {
		if got, ok := s.Attributes[attrEvalResult]; ok {
			if got != want {
				t.Fatalf("span %s: %s = %v, quer %v", op, attrEvalResult, got, want)
			}
			return
		}
	}
	t.Fatalf("span %s sem atributo %s", op, attrEvalResult)
}

// ---------------------------------------------------------------------------
// helpers de reconstrução para o teste de audit.
// ---------------------------------------------------------------------------

func versionFromResource(t *testing.T, resourceValue string) schema.Version {
	t.Helper()
	// resourceValue = "<name>@X.Y.Z"; extrai a versão após o último '@'.
	at := -1
	for i := len(resourceValue) - 1; i >= 0; i-- {
		if resourceValue[i] == '@' {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatalf("resource sem versão: %q", resourceValue)
	}
	v, err := schema.ParseVersion(resourceValue[at+1:])
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", resourceValue[at+1:], err)
	}
	return v
}

// transitionExtra reconstrói o mapa "extra" (os params que NÃO são os campos
// fixos da transição) para reproduzir o payload canónico assinado.
func transitionExtra(params map[string]string) map[string]string {
	fixed := map[string]bool{
		"from": true, "to": true, "actor": true, "signer_kid": true,
		"signature": true, "sig_alg": true, "human_signature": true,
	}
	out := make(map[string]string)
	for k, v := range params {
		if !fixed[k] {
			out[k] = v
		}
	}
	return out
}
