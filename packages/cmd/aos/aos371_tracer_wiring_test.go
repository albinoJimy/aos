package main

import (
	"context"
	"crypto/ed25519"
	"io"
	"os"
	"path/filepath"
	"testing"

	autonomy "github.com/aos-ref/control-plane/governance/autonomy"
	pdp "github.com/aos-ref/control-plane/pdp"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// AOS-371 — O NÓ PASSA O TRACER PARTILHADO AO PDP.
//
// `pdp.WithTracer` existe e é testado, mas o composition-root abre o PDP
// (nodeConfigFromEnv → loadPolicyBundleFromEnv → pdp.Open) ANTES de o tracer
// partilhado do nó existir (composto em Bootstrap). Por isso o PDP ficava com o
// NoopTracer implícito e os spans aos.policy.reload (DoD AOS-088) e
// aos.autonomy.level (AC4/DoD AOS-089) NUNCA eram emitidos no binário entregue:
// o regime sob o qual as decisões são tomadas (nível de autonomia, modo de
// oversight) e os reloads de política eram invisíveis no trace.
//
// A correcção injecta o tracer DEPOIS de composto, via [pdp.PDP.SetTracer], no
// mesmo ramo `tracingEnabled` de Bootstrap onde o tracer real nasce. Estes testes
// provam que o PDP passa a partilhar EXACTAMENTE o mesmo tracer (logo o mesmo
// exporter) do RM/Runtime.

// aos371CopyRefBundle copia o bundle de referência committado (regras Cedar reais
// que PERMITEM cap:fs.read + a allowlist assinada) para um directório temporário e
// re-assina-o com um par NOVO controlado pelo teste. Devolve o dir e o par: como o
// teste detém a chave privada, PODE re-assinar para uma versão superior e exercitar
// [pdp.PDP.Reload] (o bundle committado é assinado com uma chave que o teste não tem).
func aos371CopyRefBundle(t *testing.T) (dir string, pub ed25519.PublicKey, priv ed25519.PrivateKey) {
	t.Helper()
	dir = t.TempDir()
	// pdpPoliciesDir é o bundle committado (definido em acceptance_mediation_test.go).
	src := pdpPoliciesDir
	copyFile := func(rel string) {
		b, err := os.ReadFile(filepath.Join(src, rel))
		if err != nil {
			t.Fatalf("ler %q do bundle committado: %v", rel, err)
		}
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", filepath.Dir(dst), err)
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			t.Fatalf("escrever %q: %v", dst, err)
		}
	}
	copyFile("aos_authz.cedar")
	copyFile(filepath.Join("capabilities", "allowlist.json"))

	var err error
	pub, priv, err = ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if _, err := pdp.SignBundle(dir, "1.0.0", priv); err != nil {
		t.Fatalf("SignBundle 1.0.0: %v", err)
	}
	return dir, pub, priv
}

// aos371 FS-read Input que a política de referência PERMITE (domínio "fs"), com a
// RiskClass dada. Espelha o pdp.fsReadPermit do pacote pdp (que não é exportável).
func aos371FSReadPermit(agent, riskClass string) pdp.Input {
	return pdp.Input{
		Principal:  pdp.Principal{ID: agent, AgentClass: "agent-worker", Authority: []string{"cap:fs.read"}},
		Capability: "cap:fs.read",
		Resource:   pdp.Resource{Type: "file", Value: "/etc/data"},
		Context:    pdp.DecisionContext{Taint: "trusted", RiskClass: riskClass},
	}
}

// TestAOS371_NoInjectaTracerNoPDP_ReloadEDecisaoNoMesmoRecorder é o teste do WIRING
// END-TO-END (AC1/AC2/AC3) e o ALVO DA MUTAÇÃO (AC6): um nó REAL (Bootstrap) com um
// único RecordingExporter injectado. Depois do arranque:
//   - uma mediação pelo RM do nó emite o span execute_tool com o atributo aos.decision;
//   - um [pdp.PDP.Reload] no MESMO ponteiro *pdp.PDP que o nó recebeu emite o span
//     aos.policy.reload.
//
// AMBOS caem no MESMO recorder — prova de que o PDP passou a partilhar o tracer do
// nó. Remover a linha `cfg.PDP.SetTracer(tracer)` de bootstrap.go faz a asserção do
// span aos.policy.reload avermelhar (o PDP mantém o NoopTracer e nunca exporta).
func TestAOS371_NoInjectaTracerNoPDP_ReloadEDecisaoNoMesmoRecorder(t *testing.T) {
	ctx := context.Background()
	dir, pub, priv := aos371CopyRefBundle(t)

	rec := &otelgenai.RecordingExporter{}
	// Oracle de autonomia composto no MESMO PDP, para provar AC4 end-to-end VIA BOOTSTRAP:
	// não basta o span de reload — a decisão escalada pelo overlay tem de emitir
	// aos.autonomy.level no recorder do NÓ com o tracer que só o Bootstrap injecta.
	const agentAuto = "agt-371-e2e"
	levels := autonomy.NewLevelRegistry()
	if _, err := levels.SetLevel(ctx, agentAuto, "fs", autonomy.L4, "AOS-371 e2e", "operador"); err != nil {
		t.Fatalf("SetLevel: %v", err)
	}
	p, err := pdp.Open(dir, pdp.WithTrustAnchor(pub), pdp.WithAutonomyOracle(levels))
	if err != nil {
		t.Fatalf("pdp.Open (bundle re-assinado): %v", err)
	}

	cfg := tnBaseConfig()
	cfg.OTLPExporter = rec // ⇒ tracingEnabled: o tracer partilhado envolve este recorder
	cfg.PDP = p            // o nó injecta o tracer NESTE ponteiro (via SetTracer)

	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()

	// (RM) uma mediação atravessa o Reference Monitor do nó, que abre o span
	// execute_tool e anota aos.decision. Basta a call ATRAVESSAR o RM (mesmo negada a
	// jusante) para o span existir.
	if err := node.Runtime.Register("tool", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register(tool): %v", err)
	}
	tok, err := node.Authority.MintForHuman(ctx, tnHuman, tnAgent, tnClass, []string{tnCap})
	if err != nil {
		t.Fatalf("MintForHuman: %v", err)
	}
	if _, err := node.Runtime.Monitor().Mediate(ctx, tnCall(tok.Compact)); err != nil {
		t.Fatalf("Mediate: %v", err)
	}

	// (PDP) um reload no MESMO ponteiro: re-assina para uma versão superior e recarrega.
	if _, err := pdp.SignBundle(dir, "1.1.0", priv); err != nil {
		t.Fatalf("SignBundle 1.1.0: %v", err)
	}
	if err := p.Reload(ctx, pdp.ReloadRequest{Author: "gov@aos", Reason: "AOS-371 rollout"}); err != nil {
		t.Fatalf("Reload 1.1.0: %v", err)
	}

	// AC3: o span do reload chegou ao recorder do nó (só possível se o PDP usa o tracer
	// injectado por Bootstrap — o NoopTracer implícito nunca exportaria).
	reloadSpans := rec.SpansByName("aos.policy.reload")
	if len(reloadSpans) == 0 {
		t.Fatalf("nenhum span aos.policy.reload no recorder do nó — o tracer partilhado NÃO foi injectado no PDP (regressão do wiring AOS-371)")
	}
	if v, ok := reloadSpans[len(reloadSpans)-1].Attribute("aos.policy.reload.result"); !ok || v != "applied" {
		t.Fatalf("span de reload sem result=applied; veio %v (ok=%v)", v, ok)
	}

	// AC2: o MESMO recorder colecta também o span do RM (execute_tool + aos.decision) —
	// um só exporter recolhe PDP e RM.
	toolSpans := rec.SpansByName("execute_tool")
	if len(toolSpans) == 0 {
		t.Fatal("nenhum span execute_tool no recorder — a mediação do RM não exportou (pré-condição do teste AC2)")
	}
	sawDecision := false
	for _, s := range toolSpans {
		if _, ok := s.Attribute("aos.decision"); ok {
			sawDecision = true
			break
		}
	}
	if !sawDecision {
		t.Fatal("span execute_tool sem atributo aos.decision — o RM não anotou a decisão")
	}

	// AC4 END-TO-END VIA BOOTSTRAP: uma decisão escalada pelo overlay de autonomia (L4×danger)
	// no ponteiro do PDP que o Bootstrap injectou emite aos.autonomy.level no recorder do NÓ.
	// Prova directa (não por transitividade) de que o tracer partilhado que o Bootstrap ligou
	// alcança applyAutonomy, e não só o caminho do reload.
	dec, err := p.Decide(ctx, aos371FSReadPermit(agentAuto, "")) // RiskClass "" ⇒ danger ⇒ L4×danger escala
	if err != nil {
		t.Fatalf("Decide (autonomia): %v", err)
	}
	if dec.Effect != pdp.Escalate {
		t.Fatalf("effect = %s; L4×danger devia ESCALAR (o span de autonomia só é significativo sob TIGHTEN)", dec.Effect)
	}
	autoSpans := rec.SpansByName(autonomy.OpAutonomyLevel)
	if len(autoSpans) == 0 {
		t.Fatal("nenhum span aos.autonomy.level no recorder do nó — o tracer injectado por Bootstrap não chegou a applyAutonomy (AC4)")
	}
	sp := autoSpans[len(autoSpans)-1]
	if v, ok := sp.Attribute(autonomy.AttrAutonomyLevel); !ok || v != "L4" {
		t.Fatalf("span de autonomia sem nível L4; veio %v (ok=%v)", v, ok)
	}
	if _, ok := sp.Attribute(autonomy.AttrAutonomyOversight); !ok {
		t.Fatal("span de autonomia sem o modo de oversight anotado")
	}
}

// TestAOS371_AutonomiaEReloadPartilhamUmRecorder prova AC4 (span aos.autonomy.level)
// e AC2 (as DUAS espécies de span do PDP num só recorder), exercitando EXACTAMENTE o
// código alterado: [pdp.PDP.SetTracer] (a injecção que Bootstrap faz) e a leitura de
// p.tracer sob RLock em applyAutonomy. Um oráculo L4 no domínio "fs" com uma base
// permit e classe de risco `danger` (fail-closed) compõe L4×danger ⇒ confirm ⇒
// ESCALATE, emitindo o span aos.autonomy.level. Um reload no mesmo PDP emite
// aos.policy.reload. Ambos caem no MESMO exporter.
func TestAOS371_AutonomiaEReloadPartilhamUmRecorder(t *testing.T) {
	ctx := context.Background()
	dir, pub, priv := aos371CopyRefBundle(t)

	const agent = "agt-371"
	levels := autonomy.NewLevelRegistry()
	if _, err := levels.SetLevel(ctx, agent, "fs", autonomy.L4, "AOS-371 teste", "operador"); err != nil {
		t.Fatalf("SetLevel: %v", err)
	}

	p, err := pdp.Open(dir, pdp.WithTrustAnchor(pub), pdp.WithAutonomyOracle(levels))
	if err != nil {
		t.Fatalf("pdp.Open: %v", err)
	}

	// Injecção IDÊNTICA à de Bootstrap: o tracer partilhado (sobre o recorder) é dado ao
	// PDP DEPOIS do Open.
	rec := &otelgenai.RecordingExporter{}
	shared := otelgenai.NewTracer(rec)
	p.SetTracer(shared)

	// (autonomia) base permit + L4×danger ⇒ escalate, com o span aos.autonomy.level.
	dec, err := p.Decide(ctx, aos371FSReadPermit(agent, "")) // RiskClass "" ⇒ danger (fail-closed)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.Effect != pdp.Escalate {
		t.Fatalf("effect = %s; L4×danger devia ESCALAR (o span só é significativo sob um TIGHTEN)", dec.Effect)
	}

	// (reload) no MESMO ponteiro e MESMO recorder.
	if _, err := pdp.SignBundle(dir, "1.1.0", priv); err != nil {
		t.Fatalf("SignBundle 1.1.0: %v", err)
	}
	if err := p.Reload(ctx, pdp.ReloadRequest{Author: "gov@aos", Reason: "AOS-371"}); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// AC4: span de autonomia presente, com nível e oversight.
	autoSpans := rec.SpansByName(autonomy.OpAutonomyLevel)
	if len(autoSpans) == 0 {
		t.Fatal("nenhum span aos.autonomy.level — o tracer injectado via SetTracer não chegou a applyAutonomy")
	}
	sp := autoSpans[len(autoSpans)-1]
	if v, ok := sp.Attribute(autonomy.AttrAutonomyLevel); !ok || v != "L4" {
		t.Fatalf("aos.autonomy.level = %v (ok=%v); quer L4", v, ok)
	}
	if v, ok := sp.Attribute(autonomy.AttrAutonomyOversight); !ok || v != "confirm" {
		t.Fatalf("aos.autonomy.oversight = %v (ok=%v); quer confirm", v, ok)
	}

	// AC2: as DUAS espécies do PDP no MESMO recorder.
	if len(rec.SpansByName("aos.policy.reload")) == 0 {
		t.Fatal("nenhum span aos.policy.reload no MESMO recorder que colheu a autonomia (AC2)")
	}
}

// TestAOS371_PDPNaoCarregadoFalhaFechadoSemSpans é o CONTROLO NEGATIVO (AC5): um nó
// composto com [pdp.NewUnloaded] (o default-deny explícito de produção) continua a
// decidir DENY fail-closed SEM pânico e NÃO emite nenhum dos spans de política. Prova
// que a passagem do tracer não criou dependência de arranque: SetTracer sobre um PDP
// não-carregado é inócuo e o nó arranca e medeia na mesma.
func TestAOS371_PDPNaoCarregadoFalhaFechadoSemSpans(t *testing.T) {
	ctx := context.Background()
	rec := &otelgenai.RecordingExporter{}

	cfg := tnBaseConfig()
	cfg.OTLPExporter = rec
	cfg.PDP = pdp.NewUnloaded() // não-nil ⇒ SetTracer É chamado; PDP sem motor ⇒ deny fail-closed

	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap com PDP nao-carregado nao devia falhar: %v", err)
	}
	defer func() { _ = node.Close() }()

	if err := node.Runtime.Register("tool", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register(tool): %v", err)
	}
	tok, err := node.Authority.MintForHuman(ctx, tnHuman, tnAgent, tnClass, []string{tnCap})
	if err != nil {
		t.Fatalf("MintForHuman: %v", err)
	}

	dec, err := node.Runtime.Monitor().Mediate(ctx, tnCall(tok.Compact))
	if err != nil {
		t.Fatalf("Mediate nao devia dar erro (deny e uma decisao, nao um erro): %v", err)
	}
	if dec.Effect != referencemonitor.EffectDeny {
		t.Fatalf("PDP nao-carregado devia NEGAR fail-closed; veio effect=%q DeniedBy=%q", dec.Effect, dec.DeniedBy)
	}

	if n := len(rec.SpansByName("aos.policy.reload")); n != 0 {
		t.Fatalf("um PDP nao-carregado nunca faz reload; spans aos.policy.reload=%d", n)
	}
	if n := len(rec.SpansByName(autonomy.OpAutonomyLevel)); n != 0 {
		t.Fatalf("sem oraculo nem permit de base nao ha span de autonomia; spans aos.autonomy.level=%d", n)
	}
}
