package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	pdp "github.com/aos-ref/control-plane/pdp"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/substrate/eventstore"
	network "github.com/aos-ref/substrate/sandbox/network"
)

// AOS-169 — ACEITAÇÃO SISTÉMICA NÃO-VACUOSA (E6), §13.1 MEDIAÇÃO no caminho POSITIVO
// (allow) E negativo (deny), e §13.1 NO-BYPASS fim-a-fim através do nó REAL.
//
// A propriedade §13.1 é: "nenhum caminho de código executa uma tool call sem atravessar o
// Reference Monitor; verificável por instrumentação + testes negativos". Prová-la de forma
// NÃO-VACUOSA exige exercitar os DOIS sentidos da mediação:
//
//   - PERMITIDO (allow): um modelo que EMITE uma tool call cuja capability a política REAL
//     (bundle Cedar assinado, committado em control-plane/pdp/policies) PERMITE — a call
//     atravessa a cadeia REAL identity→policy(PDP)→taint→scope→egress, é PERMITIDA e a tool
//     EXECUTA (o output volta ao loop). Sem isto, um "teste" que só nega seria vacuoso quanto
//     ao caminho positivo (não provaria que a mediação DEIXA passar o legítimo).
//   - NEGADO (deny): o MESMO chain, o MESMO modelo-que-emite, mas uma capability FORA da
//     allowlist ⇒ o PDP NEGA (DeniedBy="policy"); a tool NUNCA executa.
//   - NO-BYPASS fim-a-fim: um nó REAL (Bootstrap+NodeService+NewAPIHandler), um run submetido
//     pela API REAL (POST /runs) cujo modelo EMITE uma tool call ⇒ a call é MEDIADA pelo RM do
//     nó (o contador de mediação move-se): nenhum caminho a saltou.
//
// FRONTEIRA HONESTA (D4). O caminho POSITIVO usa o bundle de política assinado + um token NHI
// emitido por um issuer de TESTE (Ed25519 determinístico) que o verifier confia via trust
// anchor local — a MESMA fronteira demo-emitida que enforcement_guard_test.go declara como
// AOS-156/D4 (a NÃO-FORJABILIDADE de produção — IdP real — é o eixo IDENTIDADE, o único
// deferido). A ESTRUTURA da mediação (cada barreira decide; o permit é mintado só no allow; a
// tool só executa sob permit) é REAL e não-forjável: o [referencemonitor.Decision.permit] é um
// token não-exportado. O nó de produção compõe o MESMO RM (via integration.NewSecuredRuntime);
// a razão de o caminho POSITIVO fim-a-fim pela API do nó ficar sob o mesmo D4 é que o nó arranca
// com o PDP NÃO-carregado (pdp.NewUnloaded, deny fail-closed) até haver bundle assinado provisionado
// — pelo que o full-node e2e prova o NO-BYPASS (a call é mediada e negada), e o allow é provado
// aqui sobre o MESMO tipo de RM com o bundle committado carregado.

const (
	medIssuerID = "iss:aos169-test"
	medUserID   = "human:alice"         // raiz humana da cadeia de delegação (§13.2 estrutura)
	medAgentID  = "agt-169"             // NHI (act-as da raiz)
	medClass    = "agent-worker"        // classe na allowlist do bundle committado
	medCapOK    = "cap:fs.read"         // PERMITIDA por allow_fs_read (bundle assinado)
	medCapDeny  = "cap:payments.charge" // FORA da allowlist de agent-worker ⇒ PDP nega
	// pdpPoliciesDir é o bundle de política ASSINADO committado (fonte de verdade única; não
	// duplicado). Relativo ao CWD de `go test` (o directório do pacote packages/cmd/aos).
	pdpPoliciesDir = "../../control-plane/pdp/policies"
)

// medClock é o relógio determinístico partilhado por issuer e verifier (o token nunca é visto
// como expirado num caminho de decisão).
func medClock() func() time.Time {
	return func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
}

// toolEmittingModel é o modelo NÃO-VACUOSO do caminho positivo: no 1.º turno EMITE UMA tool
// call (a INTENÇÃO que o RT medeia — nunca executa directamente); no 2.º turno conclui. Assim o
// loop de agente atravessa a mediação de uma call REAL emitida por um modelo, não um turno vazio.
type toolEmittingModel struct {
	inv   agentruntime.ToolInvocation
	turns int64
}

func (m *toolEmittingModel) Call(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	m.turns++
	if m.turns == 1 {
		return agentruntime.ModelResponse{
			Text:      "", // sem final: força o loop a mediar a tool call e a continuar
			ToolCalls: []agentruntime.ToolInvocation{m.inv},
			Usage:     agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
		}, nil
	}
	return agentruntime.ModelResponse{
		Text:  "run concluido",
		Final: true,
		Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

// medKeys deriva um par Ed25519 DETERMINÍSTICO (reprodutível, sem aleatoriedade num caminho de
// decisão).
func medKeys(seedByte byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = seedByte
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return priv.Public().(ed25519.PublicKey), priv
}

// medRealChain compõe o MESMO tipo de Reference Monitor de produção que o nó usa
// (referencemonitor.NewProductionSecure — a via estrita que RECUSA IdentityStub/EgressStub e
// exige ScopeGate activo), com a cadeia REAL identity→policy(PDP)→taint→scope→egress e o bundle
// de política ASSINADO committado carregado. Devolve o RM e o token NHI legítimo a usar como
// credencial do run.
func medRealChain(t *testing.T) (*referencemonitor.Monitor, string) {
	t.Helper()

	// (identidade REAL, demo-emitida — eixo D4) issuer de teste + verifier que confia nele.
	pub, priv := medKeys(0x21)
	iss, err := identity.NewIssuer(medIssuerID, priv, map[string]identity.ClassPolicy{
		medClass: {TTL: 5 * time.Minute, Scope: []string{medCapOK, medCapDeny}},
	}, identity.WithIssuerClock(medClock()))
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	verifier := identity.NewVerifier(
		identity.WithTrustedIssuer(medIssuerID, pub),
		identity.WithVerifierClock(medClock()),
	)

	// (política REAL) bundle Cedar ASSINADO committado — verificado contra o trust anchor no Open.
	policyDP, err := pdp.Open(pdpPoliciesDir)
	if err != nil {
		t.Fatalf("pdp.Open(%q) — bundle assinado committado: %v", pdpPoliciesDir, err)
	}

	// (autoridade user∩classe REAL) o tecto de cada sujeito da cadeia concede medCapOK — a
	// autoridade RECLAMADA pelo token (medCapOK) fica DENTRO do tecto ⇒ o ScopeGate não a trata
	// como escalada. medCapDeny NÃO é concedido: o caminho negativo bate no PDP ANTES do scope.
	authority := authz.NewStaticAuthoritySource().
		Set(medUserID, medCapOK).
		Set(medAgentID, medCapOK).
		Set("agent:"+medClass, medCapOK)

	// (egress REAL, AOS-067) sobre um WORM — o MESMO tipo de sink que o nó usa. Uma call sem
	// recurso de rede (fs.read) faz o hook abster-se (HookAllow); está aqui para a cadeia ser a
	// de produção, não uma reduzida.
	worm := audit.NewMemStore()
	resolver, err := network.NewEmbeddedResolver()
	if err != nil {
		t.Fatalf("NewEmbeddedResolver: %v", err)
	}
	egressFilter, err := network.NewEgressFilter(resolver, network.WithSecurityAuditSink(network.NewWORMSecuritySink(worm)))
	if err != nil {
		t.Fatalf("NewEgressFilter: %v", err)
	}
	egressHook, err := network.NewEgressHook(egressFilter)
	if err != nil {
		t.Fatalf("NewEgressHook: %v", err)
	}

	privileged := referencemonitor.NewStaticPrivilegedSet() // fs.read NÃO é privilegiada
	rm, err := referencemonitor.NewProductionSecure(privileged,
		referencemonitor.WithHooks(
			identity.NewIdentityCheck(verifier),       // identity (AOS-005) — resolve Principal
			pdp.NewPolicyCheck(policyDP),              // policy (PDP real, AOS-004)
			referencemonitor.NewTaintGate(privileged), // taint (AOS-069)
			referencemonitor.NewScopeGate(authority),  // scope (AOS-071)
			egressHook,                                // egress (AOS-067)
		),
		referencemonitor.WithEventSink(audit.NewMediationSink(worm)),
	)
	if err != nil {
		t.Fatalf("NewProductionSecure (cadeia real do nó): %v", err)
	}

	// Token NHI legítimo: raiz humana medUserID ⇒ NHI medAgentID, classe na allowlist,
	// autoridade {medCapOK}. É a credencial que o run propaga a cada tool call.
	tok, err := iss.Issue(context.Background(), identity.IssueRequest{
		UserID:        medUserID,
		AgentID:       medAgentID,
		AgentClass:    medClass,
		PolicyRef:     "policy://agent-worker@1",
		UserAuthority: []string{medCapOK},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return rm, tok.Compact
}

// runLoopWithModel corre um loop de agente REAL (agentruntime.Runtime — o MESMO que o nó
// embrulha) sobre o RM dado e um modelo que emite a tool call `inv`, devolvendo o resultado.
func runLoopWithModel(t *testing.T, rm *referencemonitor.Monitor, credential string, inv agentruntime.ToolInvocation) agentruntime.Result {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer es.Close()
	recorder := agentruntime.NewTurnRecorder(es)
	model := &toolEmittingModel{inv: inv}
	rt := agentruntime.New(model, rm, recorder)

	res, err := rt.Run(context.Background(), agentruntime.Goal{
		RunID:      "run-aos169-med",
		Principal:  referencemonitor.Principal{NHIID: medAgentID},
		Credential: credential,
		Objective:  "aceitacao sistemica AOS-169",
		MaxTurns:   4,
	})
	if err != nil {
		t.Fatalf("Runtime.Run: %v", err)
	}
	if model.turns < 2 {
		t.Fatalf("o modelo devia ter emitido a tool call (turno 1) e concluido (turno 2); turnos=%d (teste vacuoso)", model.turns)
	}
	return res
}

// TestAOS169_Mediation_PermitPath_ToolExecutes prova o caminho POSITIVO de §13.1: um modelo
// EMITE uma tool call cuja capability o PDP REAL PERMITE ⇒ a call atravessa a cadeia real, é
// PERMITIDA (permit++), e a tool EXECUTA (o output "ping" volta ao loop). NÃO-VACUOSO: assere o
// permit E o output real; um bypass ou uma negação indevida falhariam.
func TestAOS169_Mediation_PermitPath_ToolExecutes(t *testing.T) {
	rm, credential := medRealChain(t)
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		return in, nil // eco: o output prova que a tool EXECUTOU sob permit
	}); err != nil {
		t.Fatalf("Register(echo): %v", err)
	}

	res := runLoopWithModel(t, rm, credential, agentruntime.ToolInvocation{
		ToolID:     "echo",
		Capability: medCapOK,
		Input:      []byte("ping"),
	})

	permits, denials, _ := rm.Metrics().Snapshot()
	if permits < 1 {
		t.Fatalf("permits=%d — a tool call legitima devia ter sido PERMITIDA (allow) pela cadeia real", permits)
	}
	if denials != 0 {
		t.Fatalf("denials=%d — a tool call legitima NAO devia ter sido negada por nenhuma barreira", denials)
	}
	if len(res.ToolResults) != 1 {
		t.Fatalf("ToolResults=%d, quero 1 (a tool devia ter executado sob permit)", len(res.ToolResults))
	}
	if got := string(res.ToolResults[0].Value); got != "ping" {
		t.Fatalf("output da tool = %q, quero %q (a execucao permitida devia ecoar o input)", got, "ping")
	}
	if !res.Terminated {
		t.Fatalf("o run devia ter concluido, veio %+v", res)
	}
}

// TestAOS169_Mediation_DenyPath_ToolBlocked prova o caminho NEGATIVO de §13.1: o MESMO chain e o
// MESMO modelo-que-emite, mas uma capability FORA da allowlist de agent-worker ⇒ o PDP NEGA
// (DeniedBy="policy"); a tool NUNCA executa (output vazio, denials++). É o teste negativo que
// torna a prova de mediação real nos dois sentidos.
func TestAOS169_Mediation_DenyPath_ToolBlocked(t *testing.T) {
	rm, credential := medRealChain(t)
	// A MESMA tool registada, mas invocada com uma capability que o PDP nega.
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		return in, nil
	}); err != nil {
		t.Fatalf("Register(echo): %v", err)
	}

	res := runLoopWithModel(t, rm, credential, agentruntime.ToolInvocation{
		ToolID:     "echo",
		Capability: medCapDeny, // fora da allowlist ⇒ PDP deny
		Input:      []byte("ping"),
	})

	permits, denials, _ := rm.Metrics().Snapshot()
	if denials < 1 {
		t.Fatalf("denials=%d — a tool call fora da allowlist devia ter sido NEGADA pela cadeia real", denials)
	}
	if permits != 0 {
		t.Fatalf("permits=%d — nenhum permit devia ser mintado para uma call negada", permits)
	}
	// A tool NÃO executou: o resultado devolvido ao loop (untrusted) não tem output (deny não
	// produz Output).
	if len(res.ToolResults) == 1 && len(res.ToolResults[0].Value) != 0 {
		t.Fatalf("a tool negada NAO devia ter executado, mas veio output %q", string(res.ToolResults[0].Value))
	}
}

// TestAOS169_Mediation_NoBypass_FullNodeAPI prova §13.1 NO-BYPASS fim-a-fim através do nó REAL:
// Bootstrap (cadeia de produção real) + NodeService + NewAPIHandler; um run submetido pela API
// REAL (POST /runs) cujo modelo EMITE uma tool call. A call é MEDIADA pelo Reference Monitor do
// nó (o contador de mediação move-se) — nenhum caminho de código a saltou. NÃO-VACUOSO: sem
// mediação o contador ficaria a zero. (Sob o chain default do nó o PDP está NÃO-carregado ⇒ a
// call é negada; é exactamente essa negação que testemunha que a call ATRAVESSOU o RM. O allow
// end-to-end pela API depende do bundle assinado provisionado — eixo D4 — e é provado acima
// sobre o MESMO tipo de RM com o bundle committado carregado.)
func TestAOS169_Mediation_NoBypass_FullNodeAPI(t *testing.T) {
	cfg := tnBaseConfig()
	model := &toolEmittingModel{inv: agentruntime.ToolInvocation{
		ToolID:     "echo",
		Capability: tnCap, // cap:doc.read — registada na classe do nó de teste
		Input:      []byte("ping"),
	}}
	cfg.Model = model
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()

	// Regista a tool no RM do nó (pré-condição de despacho; a mediação corre-a ou nega-a).
	if err := node.Runtime.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		return in, nil
	}); err != nil {
		t.Fatalf("Register(echo) no RM do no: %v", err)
	}

	// Credencial REAL emitida pela autoridade do próprio nó (identity passa; a negação, se
	// houver, é a JUSANTE — provando que a call foi ALÉM da fronteira de identidade, no RM).
	tok, err := node.Authority.MintForHuman(context.Background(), tnHuman, tnAgent, tnClass, []string{tnCap})
	if err != nil {
		t.Fatalf("MintForHuman: %v", err)
	}

	svc, h := newAPI(t, node)

	before, _, _ := node.Runtime.Monitor().Metrics().Snapshot()

	rec := postJSON(h, "POST", "/runs", map[string]any{
		"run_id":        "run-nobypass",
		"objective":     "prova de nao-bypass",
		"principal_nhi": medAgentID,
		"credential":    tok.Compact,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	oc, ok, werr := svc.Wait(waitCtx, "run-nobypass")
	if werr != nil || !ok {
		t.Fatalf("run devia ter sido hospedado/concluido: ok=%v err=%v", ok, werr)
	}
	if !oc.Result.Terminated {
		t.Fatalf("run devia ter concluido, veio %+v", oc)
	}

	if model.turns < 2 {
		t.Fatalf("o modelo do nó devia ter EMITIDO a tool call (turno 1) e concluido (turno 2); turnos=%d", model.turns)
	}

	after, afterDen, _ := node.Runtime.Monitor().Metrics().Snapshot()
	// NO-BYPASS: a tool call emitida ATRAVESSOU o Reference Monitor — o total de mediações
	// (permits+denials) aumentou. Sem mediação (bypass) o contador ficaria imóvel.
	if (after + afterDen) <= before {
		t.Fatalf("a tool call emitida NAO passou pelo Reference Monitor (mediacoes antes=%d, depois=permits%d/denials%d) — nao-bypass violado", before, after, afterDen)
	}

	// Estado reflectido pela API (fotografia do desfecho).
	grec := postJSON(h, "GET", "/runs/run-nobypass", nil)
	if grec.Code != http.StatusOK {
		t.Fatalf("GET do run terminado devia dar 200, veio %d", grec.Code)
	}
	var st runStateResponse
	if err := json.Unmarshal(grec.Body.Bytes(), &st); err != nil {
		t.Fatalf("GET nao descodifica: %v", err)
	}
	if st.Status != "completed" {
		t.Fatalf("estado devia ser completed, veio %+v", st)
	}
}
