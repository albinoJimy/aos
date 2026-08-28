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
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pdp "github.com/aos-ref/control-plane/pdp"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	control "github.com/aos-ref/kernel/agent-runtime/control"
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

// TestDevHarness_SteerReachesLoop exercita a via REAL do plano de controlo (AOS-218): uma
// correcção de operador ASSINADA (ed25519), pendente na fronteira de fim-de-turno, é CONSUMIDA
// pelo loop de produção e injectada — marcada `taint=trusted` — no prompt do turno seguinte.
// Dois sentidos: sem steer, o prompt é inalterado. Usa a submissão in-process do canal de steer
// (`node.Steer`, o MESMO que o handler POST /runs/{id}/steer invoca após autenticar o sinal) por
// ser determinista — evitar correr contra a fronteira de turno de um run já em curso via HTTP.
func TestDevHarness_SteerReachesLoop(t *testing.T) {
	t.Log("=== AOS dev-harness — steer→loop (plano de controlo) pela via real ===")
	t.Log("REAL   : canal de steer ed25519 (AOS-160/193) · LoopSteer composto no runtime de produção (AOS-218) · StateGate durável por-run")

	ctx := context.Background()
	correction := []byte("prioriza a superficie desktop")

	// (a) COM steer: submetido ANTES de o run correr ⇒ pendente na fronteira do turno 1 ⇒
	// injectado no prompt do turno 2.
	var views []agentruntime.PromptView
	node, priv, opID := steerNode(t, &views)
	const runID = "dev-steer"
	emit := signedSignal(t, priv, opID, runID, control.SignalSteer, correction)
	if err := node.Steer.Steer(ctx, runID, correction, emit); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	t.Logf("[STEER] operador assina (ed25519) a correção %q e submete-a pendente para o run %q", string(correction), runID)
	if _, _, err := node.Runtime.Run(ctx, steerGoal(runID), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(views) < 2 {
		t.Fatalf("esperava >= 2 turnos (a fronteira de fim-de-turno não foi cruzada), tive %d", len(views))
	}
	if !strings.Contains(string(views[1].Materialized), "<correction taint=trusted>\nprioriza a superficie desktop") {
		t.Fatalf("a correção NÃO chegou ao prompt do turno 2 — steer não ligado ao loop de produção\nprompt: %s", views[1].Materialized)
	}
	if !strings.Contains(string(views[1].Materialized), "taint="+agentruntime.TaintTrusted) {
		t.Fatal("a correção no prompt não está marcada taint=trusted (dado de controlo, não untrusted)")
	}
	t.Log("[LOOP] turno 2: o prompt materializado contém a correção marcada taint=trusted — o loop de produção CONSUMIU o steer")

	// (b) SEM steer: o mesmo prompt do turno 2 NÃO contém a correção — o efeito é aditivo.
	var views2 []agentruntime.PromptView
	node2, _, _ := steerNode(t, &views2)
	if _, _, err := node2.Runtime.Run(ctx, steerGoal("dev-no-steer"), nil); err != nil {
		t.Fatalf("Run (sem steer): %v", err)
	}
	if len(views2) >= 2 && strings.Contains(string(views2[1].Materialized), "<correction") {
		t.Fatal("o prompt sem steer NÃO devia conter uma correção — o efeito não seria aditivo")
	}
	t.Log("[OK]   steer→loop: a correção humana chega ao loop (AOS-218); sem steer, o prompt é inalterado (dois sentidos).")
}

// TestDevHarness_RestartVerifiesWORM exercita a imposição REAL da tamper-evidence do WORM
// (AOS-221): ao reiniciar, o nó re-encadeia e VERIFICA a hash-chain durável no load (não só o
// CRC de framing). Dois sentidos: um WORM íntegro reabre e arranca; um WORM adulterado (CRC
// recalculado ⇒ framing intacto, hash-chain partida) faz o arranque ABORTAR fail-closed.
func TestDevHarness_RestartVerifiesWORM(t *testing.T) {
	t.Log("=== AOS dev-harness — restart re-encadeia e verifica o WORM (tamper-evidence) ===")
	t.Log("REAL   : Event Store/WORM durável em disco · audit.VerifyStore no arranque (AOS-221) · re-encadeamento SHA-256 da hash-chain")

	ctx := context.Background()

	// (a) ÍNTEGRO: sela um registo, fecha, e reinicia — o nó arranca e a verificação fecha.
	wormPath := filepath.Join(t.TempDir(), "worm.wal")
	cfg := tnBaseConfig()
	cfg.WORMPath = wormPath
	n1, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("bootstrap (vida 1): %v", err)
	}
	sealMarkerRecord(t, ctx, n1)
	t.Log("[WORM] vida 1: selado um registo na cadeia; a fechar o nó")
	if err := n1.Close(); err != nil {
		t.Fatalf("close (vida 1): %v", err)
	}
	n2, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("re-bootstrap de um WORM ÍNTEGRO não devia falhar: %v", err)
	}
	if err := n2.VerifyWORM(ctx); err != nil {
		_ = n2.Close()
		t.Fatalf("VerifyWORM de um WORM íntegro: %v", err)
	}
	_ = n2.Close()
	t.Log("[WORM] vida 2 (restart íntegro): re-encadeou e VERIFICOU a hash-chain no load; o nó ARRANCOU")

	// (b) ADULTERADO: recalcula o CRC (framing intacto) mas parte a hash-chain ⇒ o arranque
	// ABORTA fail-closed com audit.ErrTampered.
	wormPath2 := filepath.Join(t.TempDir(), "worm.wal")
	cfg2 := tnBaseConfig()
	cfg2.WORMPath = wormPath2
	m1, err := Bootstrap(ctx, cfg2, io.Discard)
	if err != nil {
		t.Fatalf("bootstrap (tamper vida 1): %v", err)
	}
	sealMarkerRecord(t, ctx, m1)
	if err := m1.Close(); err != nil {
		t.Fatalf("close (tamper vida 1): %v", err)
	}
	tamperWALMarker(t, wormPath2, aos221Marker)
	t.Log("[WORM] adulterado um registo do WAL — CRC recalculado (framing intacto), hash-chain PARTIDA")
	m2, err := Bootstrap(ctx, cfg2, io.Discard)
	if err == nil {
		_ = m2.Close()
		t.Fatal("o arranque devia ABORTAR fail-closed com o WORM adulterado (CRC válido)")
	}
	if !errors.Is(err, audit.ErrTampered) {
		t.Fatalf("erro de arranque devia desembrulhar para audit.ErrTampered, veio: %v", err)
	}
	t.Log("[OK]   tamper-evidence imposta: o reinício sobre um WORM adulterado é RECUSADO fail-closed (audit.ErrTampered) — AOS-221.")
}

// TestDevHarness_SovereignSubmit_TitularAndResidency exercita o submit SOBERANO pela API real
// (AOS-182 + AOS-217): (1) sem credencial do submissor o submit é RECUSADO fail-closed (403) e
// nada persiste; (2) com credencial, o TITULAR do run é DERIVADO do submissor VERIFICADO — um
// `principal_nhi` DECOY no corpo é IGNORADO (AOS-217) — e o conteúdo é cifrado por-titular no
// WAL; (3) a RESIDÊNCIA do run (board→região) é SELADA por-run no WORM (AOS-182), para que uma
// leitura futura possa exigir leitor.região == run.região.
//
// REAL: gate soberano D6/D7 + selo de residência WORM + cifra por-titular. LOCAL/demo: a
// credencial do submissor é a via por cabeçalho fora-de-produção; OIDC real = eixo D4.
func TestDevHarness_SovereignSubmit_TitularAndResidency(t *testing.T) {
	t.Log("=== AOS dev-harness — submit SOBERANO (titular derivado + residência) pela API real ===")
	t.Log("REAL   : gate soberano D6/D7 · derivação de titular da credencial (AOS-217) · selo de residência por-run (AOS-182) · cifra por-titular")
	t.Log("LOCAL  : credencial do submissor via cabeçalho demo (X-Aos-Reader/X-Aos-Board); OIDC real = eixo D4")

	ctx := context.Background()
	node, esPath := newDurableGovNode(t, &synthPIIModel{})
	svc, h := newAPI(t, node)

	// (1) FAIL-CLOSED: sem credencial resolvível não há titular sob o qual cifrar ⇒ 403, nada persiste.
	const runIDDeny = "dev-sov-deny"
	rec := postReq(h, "/runs", submitRequest{RunID: runIDDeny, PrincipalNHI: "nhi:auto-declarado"}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("submit soberano SEM credencial devia dar 403, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if head, _ := node.WORM.Head(ctx, readResidencyPartition(runIDDeny)); head != 0 {
		t.Fatalf("um submit recusado NÃO devia selar residência, head=%d", head)
	}
	t.Log("[HTTP] POST /runs (anónimo, sem credencial) → 403 · nada persiste (sem residência, run não hospedado)")

	// (2) AUTORIZADO + DERIVAÇÃO DE TITULAR: com credencial + um DECOY no corpo ⇒ 201; o titular
	// selado é o do submissor verificado, NÃO o DECOY.
	const runID = "dev-sov"
	const decoy = "nhi:DECOY-ATACANTE"
	submitSovereignAndWait(t, svc, h,
		submitRequest{RunID: runID, Objective: "trabalho soberano demo", PrincipalNHI: decoy},
		govHeaders())
	t.Logf("[HTTP] POST /runs (credencial do submissor + corpo principal_nhi=%q DECOY) → 201", decoy)

	_, subject := sealedContentOf(t, node, runID)
	if subject != govReader {
		t.Fatalf("titular selado devia ser o submissor derivado %q, veio %q (corpo não foi ignorado)", govReader, subject)
	}
	wal, err := os.ReadFile(esPath)
	if err != nil {
		t.Fatalf("ler WAL: %v", err)
	}
	if bytes.Contains(wal, []byte(aos217SynthPII)) {
		t.Fatal("o conteúdo do run não devia aparecer em CLARO no WAL — o titular derivado devia tê-lo selado")
	}
	t.Logf("[SEAL] titular DERIVADO do submissor verificado = %q (o DECOY do corpo foi IGNORADO); conteúdo cifrado no WAL", subject)

	// (3) RESIDÊNCIA SELADA POR-RUN: a região do run está durável e tamper-evidente no WORM.
	head, err := node.WORM.Head(ctx, readResidencyPartition(runID))
	if err != nil || head < 1 {
		t.Fatalf("a residência do run devia estar SELADA (head>=1), veio head=%d err=%v", head, err)
	}
	t.Logf("[WORM] residência do run SELADA por-run (região=%q, head=%d) — uma leitura futura exige leitor.região == run.região", govRegion, head)
	t.Log("[OK]   submit soberano: fail-closed sem credencial; titular derivado do submissor (AOS-217); residência selada por-run (AOS-182).")
}

// TestDevHarness_ExternalIssuer_NodeVerifiesTrustAnchorOnly demonstra o coração da
// não-forjabilidade do D4: um issuer EXTERNO detém a chave de assinatura (via `crypto.Signer`,
// AOS-175) e minta um token NHI; o nó, em modo ENDURECIDO (trust-anchor-only, `AOS_ISSUER_PUBKEY`
// = pubkey do issuer), VERIFICA esse token **sem nunca deter a chave privada**. Dois sentidos: um
// issuer ROGUE (outra chave, não confiada) é NEGADO em identity. É a prova de que o nó consome uma
// autoridade externa — o que o `cmd/aos-issuer` runnable embrulha num processo deployável.
//
// REAL: verifier trust-anchor-only do nó (AOS-174 + AOS-225), issuer via `crypto.Signer` (AOS-175
// — em prod o signer é um adaptador HSM/KMS cuja chave nunca entra em processo nenhum). LOCAL/demo:
// a chave do issuer é uma ed25519 gerada no teste (em prod: custódia HSM/KMS).
func TestDevHarness_ExternalIssuer_NodeVerifiesTrustAnchorOnly(t *testing.T) {
	t.Log("=== AOS dev-harness — o nó verifica um issuer EXTERNO trust-anchor-only (D4) ===")
	t.Log("REAL   : verifier trust-anchor-only (AOS-174/225) · issuer via crypto.Signer (AOS-175) · nó NUNCA detém a chave")
	t.Log("LOCAL  : a chave do issuer é ed25519 gerada no teste; em prod é custódia HSM/KMS")

	ctx := context.Background()

	// (1) ISSUER EXTERNO: detém a chave de assinatura; exporta SÓ a pubkey. A privada não vai ao nó.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	const issuerID = "iss:aos-devissuer"
	iss, err := identity.NewIssuerWithSigner(issuerID, priv, map[string]identity.ClassPolicy{
		tnClass: {TTL: 15 * time.Minute, Scope: []string{tnCap}},
	}, identity.WithIssuerClock(tnClock()))
	if err != nil {
		t.Fatalf("NewIssuerWithSigner: %v", err)
	}
	t.Logf("[ISSUER] issuer externo %q detém a chave; exporta a pubkey (%d bytes) → AOS_ISSUER_PUBKEY do nó", issuerID, len(iss.PublicKey()))

	// (2) NÓ em modo ENDURECIDO: trust anchor = pubkey do issuer externo; nenhuma chave no nó.
	cfg := tnBaseConfig()
	cfg.IssuerID = issuerID
	cfg.IssuerPubKey = iss.PublicKey()
	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (endurecido, trust anchor externo): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	if node.IdentityMode != IdentityModeRealHardened {
		t.Fatalf("o nó devia estar em modo ENDURECIDO (trust-anchor-only), veio %q", node.IdentityMode)
	}
	t.Logf("[NÓ] modo=%s — verifica trust-anchor-only; nunca detém a chave privada de assinatura", node.IdentityMode)

	// (3) O ISSUER EXTERNO minta um token NHI (o nó NUNCA o mintou nem tem a chave).
	tok, err := iss.Issue(ctx, identity.IssueRequest{
		UserID:        tnHuman,
		AgentID:       tnAgent,
		AgentClass:    tnClass,
		PolicyRef:     "policy://" + tnClass,
		UserAuthority: []string{tnCap},
		AuthMethod:    "oidc:demo", // rótulo de método p/ o binding audit (AOS-176), nunca PII
	})
	if err != nil {
		t.Fatalf("Issue (issuer externo): %v", err)
	}

	// (4) O NÓ ACEITA a identidade do token do issuer externo (verifier real ⇒ DeniedBy != identity).
	rm := node.Runtime.Monitor()
	dec, err := rm.Mediate(ctx, tnCall(tok.Compact))
	if err != nil {
		t.Fatalf("Mediate (token do issuer externo): %v", err)
	}
	if dec.DeniedBy == "identity" {
		t.Fatalf("o nó devia ACEITAR a identidade do issuer externo; foi NEGADO em identity (DeniedBy=%q)", dec.DeniedBy)
	}
	t.Log("[OK]   o nó VERIFICOU o token de um issuer EXTERNO sem deter a chave — não-forjabilidade real (D4).")

	// (5) DOIS SENTIDOS: um issuer ROGUE (outra chave, não confiada pelo anchor) é NEGADO em identity.
	_, roguePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (rogue): %v", err)
	}
	rogue, err := identity.NewIssuerWithSigner("iss:rogue", roguePriv, map[string]identity.ClassPolicy{
		tnClass: {TTL: 15 * time.Minute, Scope: []string{tnCap}},
	}, identity.WithIssuerClock(tnClock()))
	if err != nil {
		t.Fatalf("NewIssuerWithSigner (rogue): %v", err)
	}
	rogueTok, err := rogue.Issue(ctx, identity.IssueRequest{
		UserID: tnHuman, AgentID: tnAgent, AgentClass: tnClass,
		PolicyRef: "policy://" + tnClass, UserAuthority: []string{tnCap}, AuthMethod: "oidc:demo",
	})
	if err != nil {
		t.Fatalf("Issue (rogue): %v", err)
	}
	decRogue, err := rm.Mediate(ctx, tnCall(rogueTok.Compact))
	if err != nil {
		t.Fatalf("Mediate (rogue): %v", err)
	}
	if decRogue.DeniedBy != "identity" {
		t.Fatalf("token de um issuer NÃO-confiado devia ser NEGADO em identity, veio DeniedBy=%q", decRogue.DeniedBy)
	}
	t.Log("[OK]   dois sentidos: um issuer não-confiado (outra chave) é NEGADO em identity — o trust anchor é a única fonte.")
}

// buildAOSIssuer compila o binário `cmd/aos-issuer` (módulo separado) para um caminho temporário.
func buildAOSIssuer(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "aos-issuer")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "../aos-issuer" // cwd de `go test` = packages/cmd/aos ⇒ ../aos-issuer = o módulo do issuer
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compilar aos-issuer: %v\n%s", err, out)
	}
	return bin
}

// runAOSIssuer executa o binário aos-issuer como PROCESSO SEPARADO e devolve o stdout (aparado).
func runAOSIssuer(t *testing.T, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("aos-issuer %v: %v\n%s", args, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

// TestDevHarness_IssuerSubprocess_NodeVerifiesRealBinary fecha o ciclo "DOIS PROCESSOS" do D4: o
// binário `aos-issuer` corre como PROCESSO SEPARADO — detém a chave, exporta a pubkey e minta um
// token NHI — e o nó (processo distinto), em modo endurecido com essa pubkey como trust anchor,
// VERIFICA o token sem nunca deter a chave. Dois sentidos: um token assinado por OUTRA chave (outro
// key-file) é negado em identity. É a mesma prova de AOS-226 mas com o issuer a correr de facto como
// binário, não in-process.
func TestDevHarness_IssuerSubprocess_NodeVerifiesRealBinary(t *testing.T) {
	t.Log("=== AOS dev-harness — o nó verifica um issuer a correr como PROCESSO SEPARADO (D4) ===")
	t.Log("REAL   : o binário aos-issuer detém a chave e minta; o nó (processo distinto) verifica trust-anchor-only")

	ctx := context.Background()
	bin := buildAOSIssuer(t)
	keyFile := filepath.Join(t.TempDir(), "issuer.key")
	const issuerID = "iss:aos-subprocess"

	// PROCESSO 1 (aos-issuer): exporta a pubkey — o trust anchor do nó (AOS_ISSUER_PUBKEY).
	pubHex := runAOSIssuer(t, bin, "pubkey", "--key-file", keyFile)
	pub, err := hex.DecodeString(pubHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("pubkey do issuer inválida: %v (len=%d)", err, len(pub))
	}
	t.Logf("[PROC 1: aos-issuer] pubkey → %s… (%d bytes) = AOS_ISSUER_PUBKEY do nó", pubHex[:16], len(pub))

	// PROCESSO 1 (aos-issuer): minta um token NHI — a chave NUNCA sai deste processo.
	tok := runAOSIssuer(t, bin, "mint", "--key-file", keyFile, "--issuer", issuerID,
		"--human", "human:alice", "--agent", tnAgent, "--class", tnClass, "--caps", tnCap)
	if tok == "" {
		t.Fatal("o issuer não produziu token")
	}
	t.Logf("[PROC 1: aos-issuer] mint → token NHI (%d chars)", len(tok))

	// PROCESSO 2 (nó): modo endurecido, trust anchor = pubkey do issuer. RELÓGIO REAL (o subprocesso
	// mintou com time.Now), pelo que o verificador do nó também usa time.Now.
	cfg := tnBaseConfig()
	cfg.IssuerID = issuerID
	cfg.IssuerPubKey = ed25519.PublicKey(pub)
	cfg.VerifierClock = time.Now
	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (endurecido, trust anchor do subprocesso): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	if node.IdentityMode != IdentityModeRealHardened {
		t.Fatalf("o nó devia estar em modo ENDURECIDO, veio %q", node.IdentityMode)
	}
	t.Logf("[PROC 2: nó] modo=%s — trust anchor = pubkey do issuer; nunca detém a chave", node.IdentityMode)

	// O NÓ verifica o token do PROCESSO SEPARADO (identidade aceite ⇒ DeniedBy != identity).
	dec, err := node.Runtime.Monitor().Mediate(ctx, tnCall(tok))
	if err != nil {
		t.Fatalf("Mediate (token do subprocesso): %v", err)
	}
	if dec.DeniedBy == "identity" {
		t.Fatalf("o nó devia ACEITAR a identidade do token do issuer subprocesso, foi negado em identity")
	}
	t.Log("[OK]   dois processos: o issuer mintou (detém a chave), o nó verificou (não a detém) — não-forjabilidade real.")

	// DOIS SENTIDOS: um token do MESMO issuerID mas assinado por OUTRA chave (outro key-file) é
	// NEGADO em identity — o anchor (pubkey da 1ª chave) é a única fonte de confiança.
	rogueKey := filepath.Join(t.TempDir(), "rogue.key")
	rogueTok := runAOSIssuer(t, bin, "mint", "--key-file", rogueKey, "--issuer", issuerID,
		"--human", "human:alice", "--agent", tnAgent, "--class", tnClass, "--caps", tnCap)
	decRogue, err := node.Runtime.Monitor().Mediate(ctx, tnCall(rogueTok))
	if err != nil {
		t.Fatalf("Mediate (rogue): %v", err)
	}
	if decRogue.DeniedBy != "identity" {
		t.Fatalf("token assinado por OUTRA chave devia ser NEGADO em identity, veio DeniedBy=%q", decRogue.DeniedBy)
	}
	t.Log("[OK]   dois sentidos: um token do mesmo issuerID mas OUTRA chave é NEGADO em identity — o anchor manda.")
}
