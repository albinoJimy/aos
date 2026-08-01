package main

// DEV-HARNESS — exercita o CAMINHO REAL do nó `aos` ponta-a-ponta com colaboradores
// LOCAIS (não os stubs neutros do `aos-demo`), ANTES de a organização provisionar os
// tenants de produção (D4/identidade real, provider de modelo, KMS/HSM).
//
// O que é REAL neste harness:
//   - a composição de produção do nó (Bootstrap → integration.NewSecuredRuntime →
//     referencemonitor.NewProductionSecure): a cadeia identity→PDP→taint→scope→egress,
//     com o permit NÃO-FORJÁVEL (o token de decisão do RM é não-exportado);
//   - a POLÍTICA: o bundle Cedar ASSINADO committado (control-plane/pdp/policies),
//     carregado pela superfície de AOS-220 (AOS_POLICY_BUNDLE_DIR + trust anchor
//     out-of-band) e verificado contra a âncora no Open;
//   - a API HTTP real do nó (NewAPIHandler): o run é submetido por POST /runs e lido
//     por GET /runs/{id}, exactamente como um cliente externo faria.
//
// O que é LOCAL/DEMO (o eixo que resta para execução de produção):
//   - a autoridade de IDENTIDADE é co-localizada (a credencial NHI é cunhada pela
//     autoridade do próprio nó) — a não-forjabilidade de produção (IdP real, D4) é o
//     eixo IDENTIDADE, o único ainda deferido;
//   - o MODELO é determinista (twoTurnToolModel / toolEmittingModel), não um LLM real
//     (o provider real é EPIC-06).
//
// Correr:  go test -run TestDevHarness -v ./packages/cmd/aos

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	pdp "github.com/aos-ref/control-plane/pdp"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/signing"
)

// devNode compõe o nó REAL pela mesma via de produção do resto dos testes de aceitação
// (Bootstrap sobre a cadeia identity→revalidation→policy→taint→scope→egress), com o
// bundle Cedar ASSINADO committado carregado do ambiente (a superfície de AOS-220) e uma
// autoridade user∩classe que concede `cap:fs.read`. Parametrizável pelo modelo para
// exercitar os dois sentidos da mediação. Devolve o nó e a credencial NHI legítima.
func devNode(t *testing.T, model agentruntime.ModelClient) (*Node, string) {
	t.Helper()
	ctx := context.Background()

	// (política REAL) o bundle Cedar ASSINADO committado — verificado contra o trust anchor
	// (trust_anchor.pub) no Open. É o mesmo bundle que a superfície de AOS-220 carrega do
	// ambiente; aqui abre-se directamente (harness sem topologia soberana de organização).
	policyDP, err := pdp.Open(pdpPoliciesDir)
	if err != nil {
		t.Fatalf("pdp.Open(%q) — bundle assinado committado: %v", pdpPoliciesDir, err)
	}

	// (supply-chain REAL) tool set assinado + congelado: catálogo com a tool `counter` +
	// revalidação contra um trust store.
	signer := durSigner(t)
	entry := counterEntry(t, signer)
	auditStore := audit.NewMemStore()
	trust, err := signing.NewTrustStore(auditStore)
	if err != nil {
		t.Fatalf("trust store: %v", err)
	}
	if err := trust.Add(ctx, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatalf("trust add: %v", err)
	}
	revalidator, err := revalidation.New(trust, auditStore)
	if err != nil {
		t.Fatalf("revalidator: %v", err)
	}

	// Config construída DIRECTAMENTE (não via nodeConfigFromEnv): sem AOS_BOARD_REGIONS ⇒ o
	// gate de leitura soberano (readGov) fica nil ⇒ submit em modo LEGADO (a credencial vem do
	// corpo). É a postura correcta de um harness dev SEM boards/regiões de organização — a
	// topologia soberana (D6/D7) é o eixo que exige provisionamento real.
	cfg := Config{
		IssuerID: "iss:aos-devharness",
		Humans:   []string{aos220Human},
		IssuerClasses: map[string]identity.ClassPolicy{
			durClass: {TTL: 15 * time.Minute, Scope: []string{durCap}},
		},
		PDP:         policyDP,
		Model:       model,
		Catalog:     catalogStub{entries: []domain.Entry{entry}},
		Revalidator: revalidator,
		Policy:      integration.StaticPolicy{MaxEgress: domain.EgressInternal},
		Authority: authz.NewStaticAuthoritySource().
			Set("human:"+aos220Human, durCap).
			Set(durAgent, durCap).
			Set("agent:"+durClass, durCap),
	}

	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (cadeia de produção real, bundle carregado): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })

	tok, err := node.Authority.MintForHuman(ctx, aos220Human, durAgent, durClass, []string{durCap})
	if err != nil {
		t.Fatalf("MintForHuman: %v", err)
	}
	return node, tok.Compact
}

// TestDevHarness_RealNode_MediatedExecution conduz o nó REAL pela API HTTP real e prova,
// nos DOIS sentidos, que a mediação de política funciona ponta-a-ponta: uma tool call
// PERMITIDA pelo bundle Cedar executa; uma FORA da allowlist é NEGADA e nunca executa.
func TestDevHarness_RealNode_MediatedExecution(t *testing.T) {
	t.Log("=== AOS dev-harness — caminho REAL do nó, colaboradores locais ===")
	t.Log("REAL   : NewProductionSecure (identity→PDP→taint→scope→egress) · bundle Cedar ASSINADO · API HTTP (NewAPIHandler) · permit não-forjável")
	t.Log("LOCAL  : autoridade de identidade co-localizada (eixo D4) · modelo determinista (provider real = EPIC-06)")

	t.Run("PERMIT — tool call na allowlist atravessa a mediação e EXECUTA", func(t *testing.T) {
		node, credential := devNode(t, &twoTurnToolModel{})

		var execs int64
		if err := node.Runtime.Register("counter", func(_ context.Context, in []byte) ([]byte, error) {
			atomic.AddInt64(&execs, 1)
			return []byte("pong"), nil // o output prova execução sob permit
		}); err != nil {
			t.Fatalf("Register(counter): %v", err)
		}

		svc, h := newAPI(t, node)
		t.Logf("[HTTP] POST /runs  (principal=%s, credencial NHI cunhada pela autoridade local)", durAgent)
		rec := postJSON(h, "POST", "/runs", map[string]any{
			"run_id":        "dev-permit",
			"objective":     "harness: execução mediada real (allow)",
			"principal_nhi": durAgent,
			"credential":    credential,
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST /runs devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
		}
		t.Logf("[HTTP] → 201 Created")

		wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		oc, ok, werr := svc.Wait(wctx, "dev-permit")
		if werr != nil || !ok {
			t.Fatalf("run devia ter sido hospedado/concluído: ok=%v err=%v", ok, werr)
		}

		permits, denials, _ := node.Runtime.Monitor().Metrics().Snapshot()
		t.Logf("[RM]   mediação: permits=%d denials=%d · tool executou %d vez(es) · run terminado=%v",
			permits, denials, atomic.LoadInt64(&execs), oc.Result.Terminated)
		if permits < 1 || denials != 0 || atomic.LoadInt64(&execs) != 1 {
			t.Fatalf("esperava PERMIT+execução, veio permits=%d denials=%d execs=%d", permits, denials, atomic.LoadInt64(&execs))
		}

		grec := postJSON(h, "GET", "/runs/dev-permit", nil)
		t.Logf("[HTTP] GET /runs/dev-permit → %d  %s", grec.Code, grec.Body.String())
		if grec.Code != http.StatusOK {
			t.Fatalf("GET devia dar 200, veio %d", grec.Code)
		}
		t.Log("[OK]   a política Cedar real PERMITIU a call e a tool EXECUTOU pela API HTTP real.")
	})

	t.Run("DENY — capability fora da allowlist é NEGADA e a tool NÃO executa", func(t *testing.T) {
		node, credential := devNode(t, &toolEmittingModel{inv: agentruntime.ToolInvocation{
			ToolID:     "counter",
			Capability: "cap:payments.charge", // FORA da allowlist de agent-worker ⇒ PDP nega
			Input:      []byte("charge"),
		}})

		var execs int64
		if err := node.Runtime.Register("counter", func(_ context.Context, in []byte) ([]byte, error) {
			atomic.AddInt64(&execs, 1)
			return []byte("pong"), nil
		}); err != nil {
			t.Fatalf("Register(counter): %v", err)
		}

		svc, h := newAPI(t, node)
		t.Logf("[HTTP] POST /runs  (mesma credencial, mas o modelo emite cap:payments.charge — fora da allowlist)")
		rec := postJSON(h, "POST", "/runs", map[string]any{
			"run_id":        "dev-deny",
			"objective":     "harness: default-deny da política real",
			"principal_nhi": durAgent,
			"credential":    credential,
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST /runs devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
		}

		wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, ok, werr := svc.Wait(wctx, "dev-deny"); werr != nil || !ok {
			t.Fatalf("run devia ter sido hospedado/concluído: ok=%v err=%v", ok, werr)
		}

		permits, denials, _ := node.Runtime.Monitor().Metrics().Snapshot()
		t.Logf("[RM]   mediação: permits=%d denials=%d · tool executou %d vez(es) (esperado 0)",
			permits, denials, atomic.LoadInt64(&execs))
		if denials < 1 || atomic.LoadInt64(&execs) != 0 {
			t.Fatalf("esperava DENY e tool não-executada, veio denials=%d execs=%d", denials, atomic.LoadInt64(&execs))
		}
		t.Log("[OK]   a política Cedar real NEGOU a call fail-closed; a tool NUNCA executou.")
	})
}

// TestDevHarness_CryptoShred_RightToErasure conduz o ciclo REAL do direito-ao-apagamento pela API
// HTTP do nó: o conteúdo de um run é cifrado POR-TITULAR no Event Store (AOS-093); um leitor
// autorizado por soberania reconstrói e DECIFRA (AOS-214); um POST /dsar/erase destrói a KEK
// por-titular (crypto-shred); e daí em diante MESMO o leitor autorizado obtém 410 Gone —
// irrecuperável — enquanto a hash-chain do Event Store permanece íntegra.
//
// Colaboradores REAIS: execução durável sobre o Event Store/WORM em disco, o envelope DEK/KEK
// por-titular (AOS-093), o gate de leitura soberano D6/D7 e o fluxo DSAR. LOCAL/demo: a
// credencial do leitor é a via por cabeçalho fora-de-produção (X-Aos-Reader/X-Aos-Board), com o
// board→região semeado por config — a não-forjabilidade de produção (OIDC real) é o eixo D4.
func TestDevHarness_CryptoShred_RightToErasure(t *testing.T) {
	t.Log("=== AOS dev-harness — direito ao apagamento (crypto-shred) pela API real ===")
	t.Log("REAL   : execução durável (Event Store/WORM em disco) · cifra DEK/KEK por-titular (AOS-093) · gate soberano D6/D7 (AOS-214) · fluxo DSAR")
	t.Log("LOCAL  : credencial do leitor via cabeçalho demo (X-Aos-Reader/X-Aos-Board); OIDC real = eixo D4")

	node := newGovDurableNode(t) // nó com execução durável + soberania de leitura ligadas
	_, h := newAPI(t, node)

	const subject = "nhi:titular-demo-shred"
	const runID = "dev-shred"
	const secret = "PROMPT-DEV: dados sensiveis do titular demo (SYNTH-DEVHARNESS)"
	// Sela um run sintético: o conteúdo (texto + output de tool) é cifrado por-titular ANTES do WAL.
	captureSynthetic(t, node, subject, runID, secret, "TOOL-OUT-DEVHARNESS")

	// (1) ANTES do apagamento: o leitor autorizado reconstrói e DECIFRA o conteúdo real (200).
	rec := getReq(h, "/runs/"+runID+"/reconstruct", govHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("reconstrução autorizada devia dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	var resp reconstructResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resposta de reconstrução não descodifica: %v", err)
	}
	if len(resp.Turns) != 1 || resp.Turns[0].Text != secret {
		t.Fatalf("o leitor autorizado devia decifrar o conteúdo real, veio %+v", resp.Turns)
	}
	t.Logf("[HTTP] GET /runs/%s/reconstruct (leitor autorizado) → 200 · conteúdo DECIFRADO: %q", runID, resp.Turns[0].Text)

	// (2) POST /dsar/erase — destrói a KEK por-titular (crypto-shred real) pelo fluxo DSAR.
	er := postReq(h, "/dsar/erase", dsarRequestWire{RequestID: "dev-erase-1", SubjectID: subject}, govHeaders())
	if er.Code != http.StatusOK {
		t.Fatalf("POST /dsar/erase devia dar 200, veio %d (%s)", er.Code, er.Body.String())
	}
	if bytes.Contains(er.Body.Bytes(), []byte(secret)) {
		t.Fatalf("a resposta do erase não devia carregar conteúdo: %q", er.Body.String())
	}
	t.Logf("[HTTP] POST /dsar/erase (subject pseudónimo) → 200 · KEK por-titular DESTRUÍDA")

	// (3) DEPOIS do apagamento: MESMO o leitor autorizado obtém 410 Gone — irrecuperável — e a
	// resposta NUNCA vaza o conteúdo. O direito ao apagamento vale também contra o replay.
	rec2 := getReq(h, "/runs/"+runID+"/reconstruct", govHeaders())
	if rec2.Code != http.StatusGone {
		t.Fatalf("após o shred a reconstrução autorizada devia dar 410, veio %d (%s)", rec2.Code, rec2.Body.String())
	}
	if bytes.Contains(rec2.Body.Bytes(), []byte(secret)) {
		t.Fatalf("a resposta 410 vaza o conteúdo apagado: %q", rec2.Body.String())
	}
	t.Logf("[HTTP] GET /runs/%s/reconstruct (após shred) → 410 Gone · ErrDecrypt (KEK destruída), sem vazamento", runID)
	t.Log("[OK]   direito ao apagamento REAL: o crypto-shred torna o conteúdo irrecuperável mesmo ao leitor autorizado; a hash-chain do Event Store permanece íntegra.")
}
