package integration

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"

	autonomy "github.com/aos-ref/control-plane/governance/autonomy"
	pdp "github.com/aos-ref/control-plane/pdp"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/memory/provenance"
	domain "github.com/aos-ref/platform/registry/domain"
	toolset "github.com/aos-ref/platform/registry/toolset"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// AOS-021/AOS-087 — o ciclo de aprovação pela CADEIA DE PRODUÇÃO
// ---------------------------------------------------------------------------
//
// PORQUE ESTE FICHEIRO EXISTE. [TestApprovalCycle_RetomaNaoRepeteEfeitos] cobre o ciclo,
// mas sobre um Reference Monitor construído À MÃO, com um `riskEscalateHook` escrito para o
// teste. Isso prova que as peças funcionam — não que ENCAIXAM. A composição real falhou de
// quatro maneiras que aquela montagem não podia observar, todas encontradas só num run vivo:
//
//	(a) o ApprovalGate NUNCA era ligado à cadeia (secured.go não o compunha);
//	(b) o ESCALATE real não vem de um hook de risco — vem do ORÁCULO DE AUTONOMIA dentro
//	    do PDP, que não sabia da aprovação e voltava a exigir o gate para sempre;
//	(c) o PEP não sabia cumprir a obligation `autonomy` e NEGAVA todo o permit do oráculo;
//	(d) a reescrita do efeito corria depois de a preview ter sido calculada.
//
// Aqui a cadeia é a de [NewSecuredRuntime] (NewProductionSecure: approval → identity →
// revalidation → policy(PDP com bundle ASSINADO e oráculo ligado) → taint → scope → budget
// → egress), com issuer/verifier reais, catálogo assinado, WORM e execução durável.
// Determinista e offline.

const (
	acrIssuerID = "iss:test-approval-chain"
	acrUser     = "human:alice"
	acrAgent    = "agt-approval-chain"
	acrClass    = "agent-worker"
	acrCap      = "cap:fs.read"
	acrRunID    = "run-approval-chain"
)

// acrChain é a montagem completa: devolve o runtime, o broker (que é simultaneamente a
// fonte de evidência e o verificador), o sink que capta a escalada e um contador de
// execuções REAIS da tool.
type acrChain struct {
	sec    *SecuredRuntime
	broker *ApprovalBroker
	sink   *acrSink
	execs  *int
	goal   agentruntime.Goal
	// keys são as privadas dos aprovadores. Vivem SÓ deste lado: o gate é non-signing por
	// desenho e nunca vê material privado — só verifica assinaturas contra pubkeys pinadas.
	keys [2]ed25519.PrivateKey
}

// acrSink capta os pendentes (o que o nó faria: registar e suspender).
type acrSink struct{ pending []agentruntime.PendingApproval }

func (s *acrSink) Escalate(_ context.Context, p agentruntime.PendingApproval) error {
	s.pending = append(s.pending, p)
	return nil
}

// newACRChain monta a cadeia REAL com o oráculo de autonomia a exigir gate humano.
//
// O nível é L4 no domínio "fs": a classe de risco não é computada neste caminho, logo o
// PDP trata-a como `danger` (fail-closed) e L4×danger ⇒ `confirm` ⇒ requer humano. É
// exactamente a configuração do deployment endurecido (`AOS_AUTONOMY_LEVELS=…:fs=L4`).
func newACRChain(t *testing.T) *acrChain {
	return newACRChainComOpcoes(t, audit.NewMemStore(), true)
}

// newACRChainComWORM é o construtor com o WORM injectado, para os testes que precisam de
// LER os registos de mediação selados.
func newACRChainComWORM(t *testing.T, worm audit.Store) *acrChain {
	return newACRChainComOpcoes(t, worm, true)
}

// newACRChainComOpcoes monta a cadeia. comGate=false OMITE o ApprovalVerifier — a
// configuração exacta que existia em produção e que deixava o bridge inerte.
func newACRChainComOpcoes(t *testing.T, worm audit.Store, comGate bool) *acrChain {
	t.Helper()
	ctx := context.Background()

	// --- PDP REAL: bundle de referência ASSINADO + ORÁCULO DE AUTONOMIA ligado ---
	anchor, err := base64.StdEncoding.DecodeString("tNHbo3n7mNWtl5Gt+GdRSkdUyrBjCdA+8TuoSPGReoY=")
	if err != nil {
		t.Fatalf("anchor: %v", err)
	}
	levels := autonomy.NewLevelRegistry()
	if _, err := levels.SetLevel(ctx, acrAgent, "fs", autonomy.L4, "teste de composicao", "operador"); err != nil {
		t.Fatalf("SetLevel: %v", err)
	}
	policyDP, err := pdp.Open("../control-plane/pdp/policies",
		pdp.WithTrustAnchor(ed25519.PublicKey(anchor)),
		pdp.WithAutonomyOracle(levels))
	if err != nil {
		t.Fatalf("pdp.Open: %v", err)
	}

	// --- IDENTIDADE REAL: o issuer computa Scope = UserAuthority ∩ ClassPolicy.Scope ---
	pub, priv := enfKeys(0x31)
	classes := map[string]identity.ClassPolicy{acrClass: {TTL: 5 * time.Minute, Scope: []string{acrCap}}}
	iss, err := identity.NewIssuer(acrIssuerID, priv, classes, identity.WithIssuerClock(enfClock()))
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	verifier := identity.NewVerifier(identity.WithTrustedIssuer(acrIssuerID, pub), identity.WithVerifierClock(enfClock()))
	tok, err := iss.Issue(ctx, identity.IssueRequest{
		UserID: acrUser, AgentID: acrAgent, AgentClass: acrClass,
		PolicyRef: "policy://agent-worker@1", UserAuthority: []string{acrCap},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// --- REVALIDAÇÃO REAL: catálogo assinado + trust store ---
	signer := testSigner(t)
	auditStore := audit.NewMemStore()
	trust := newTrust(t, ctx, auditStore, signer)
	entry := signedEntry(t, signer, "doc_read", "1.0.0", domain.Contract{Egress: domain.EgressNone})
	catalog := &fakeCatalog{entries: []domain.Entry{entry}}
	rv := newRevalidator(t, trust, auditStore,
		NewProvenanceQuarantiner(provenance.NewPartition(nil), WithQuarantineClock(fixedClock())),
		NewRecordingAlerter())

	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	// --- BRIDGE DE APROVAÇÃO: o MESMO broker emite, verifica e consome o grant ---
	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")
	broker, err := NewApprovalBroker(gate, NewMemApprovalStore())
	if err != nil {
		t.Fatalf("NewApprovalBroker: %v", err)
	}
	// --- EXECUÇÃO DURÁVEL: é o step-ledger que impede a dupla execução ---
	ledger, err := durable.NewStepLedger(es)
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}

	// Modelo determinista: pede a tool no turno 1 e conclui no 2. Nesta camada testa-se a
	// CADEIA DE MEDIAÇÃO; a reprodução da trajectória a partir das capturas REAIS é o que
	// o teste de ciclo do nó (cmd/aos) cobre — deliberadamente não se simula aqui.
	model := agentruntime.ModelClientFunc(func(_ context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		if view.Turn == 1 {
			return agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{{
				ToolID: "doc_read", Capability: acrCap, Input: []byte(`{"doc_id":"notes"}`),
				ResourceType: "doc", ResourceValue: "notes", ResourceRegion: "eu",
			}}}, nil
		}
		return agentruntime.ModelResponse{Text: "concluido", Final: true}, nil
	})

	sink := &acrSink{}
	// O gate é o TERCEIRO lado do bridge. Sem ele a evidência viaja e ninguém a lê.
	var verificador referencemonitor.ApprovalVerifier
	if comGate {
		verificador = broker
	}
	sec, err := NewSecuredRuntime(SecuredConfig{
		Model:       model,
		Recorder:    agentruntime.NewTurnRecorder(es),
		Catalog:     catalog,
		Revalidator: rv,
		Policy:      StaticPolicy{MaxEgress: domain.EgressExternal},
		WORM:        worm,
		Verifier:    verifier,
		PDP:         policyDP,
		Ledger:      ledger,
		// OS TRÊS LADOS DO BRIDGE. Faltando qualquer um, o ciclo não fecha — foi o que
		// aconteceu em produção com o ApprovalVerifier ausente.
		EscalationSink:   sink,
		ApprovalEvidence: broker,
		ApprovalVerifier: verificador,
		FreezeOptions:    []toolset.Option{toolset.WithClock(fixedClock())},
	})
	if err != nil {
		t.Fatalf("NewSecuredRuntime: %v", err)
	}

	execs := 0
	if err := sec.Register("doc_read", func(_ context.Context, in []byte) ([]byte, error) {
		execs++
		return []byte(`{"content":"Reuniao 3a: rever o plano de migracao."}`), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	return &acrChain{
		sec: sec, broker: broker, sink: sink, execs: &execs,
		keys: [2]ed25519.PrivateKey{privA, privB},
		goal: agentruntime.Goal{
			RunID: acrRunID,
			Principal: referencemonitor.Principal{
				NHIID: acrAgent, AgentID: acrAgent, AgentClass: acrClass, Authority: []string{acrCap},
				DelegationChain: []referencemonitor.DelegationHop{{Sub: acrUser, ActAs: acrAgent}},
			},
			Scope:      []string{acrCap},
			Credential: tok.Compact,
			Model:      agentruntime.ModelConfig{ModelID: "claude-opus-4-8", Seed: 42},
			System:     "assistente de leitura de documentos",
			Objective:  "le o documento notes",
		},
	}
}

// aprova corre a cerimónia four-eyes REAL sobre a preview do pendente e devolve o grant.
func (c *acrChain) aprova(t *testing.T, preview []byte) ApprovalGrant {
	t.Helper()
	req := FourEyesRequest{
		RequestID: "req-" + acrRunID, Preview: preview,
		RiskClass: risk.ClassDanger, DualControlRequired: true,
	}
	// DUAS pernas estruturalmente distintas nos três eixos (principal, sessão, credencial).
	legA := SignFourEyesLeg(c.keys[0], req, "human:alice", "sess-A", "cred-A", challenge32(t), nil)
	legB := SignFourEyesLeg(c.keys[1], req, "human:bob", "sess-B", "cred-B", challenge32(t), nil)
	grant, err := c.broker.Approve(context.Background(), req.RequestID, req, legA, legB)
	if err != nil {
		t.Fatalf("cerimonia four-eyes: %v", err)
	}
	return grant
}

// TestApprovalChainReal_CicloCompleto é o teste que faltava: o ciclo inteiro pela cadeia de
// produção. Sem aprovação a acção ESCALA e nada executa; depois da cerimónia, a MESMA acção
// atravessa a cadeia toda e a tool corre — exactamente uma vez.
func TestApprovalChainReal_CicloCompleto(t *testing.T) {
	ctx := context.Background()
	c := newACRChain(t)

	// ---------- 1.ª passagem: o oráculo exige humano ----------
	res1, _, err := c.sec.Run(ctx, c.goal, nil)
	if err != nil {
		t.Fatalf("1.ª passagem: %v", err)
	}
	if !res1.Escalated {
		t.Fatalf("o oraculo de autonomia (L4 x danger = confirm) tinha de ESCALAR; res=%+v", res1)
	}
	if *c.execs != 0 {
		t.Fatalf("uma accao ESCALADA nao produz efeito nenhum; execucoes=%d", *c.execs)
	}
	if len(c.sink.pending) != 1 {
		t.Fatalf("devia registar 1 pendente para o operador decidir; veio %d", len(c.sink.pending))
	}
	preview := c.sink.pending[0].Preview
	if len(preview) == 0 {
		t.Fatal("o pendente tem de trazer a preview — é o digest que as pernas assinam")
	}

	// ---------- cerimónia four-eyes REAL ----------
	grant := c.aprova(t, preview)
	if len(grant.Approvers) != 2 || !grant.DualControl {
		t.Fatalf("o grant tem de registar as DUAS pernas e o dual-control: %+v", grant)
	}

	// ---------- 2.ª passagem: a acção aprovada atravessa a cadeia ----------
	res2, _, err := c.sec.Run(ctx, c.goal, nil)
	if err != nil {
		t.Fatalf("2.ª passagem: %v", err)
	}
	if res2.Escalated {
		t.Fatal("com aprovacao VERIFICADA o oraculo nao pode voltar a exigir o gate — era o ciclo infinito")
	}
	if !res2.Terminated {
		t.Fatalf("o run devia prosseguir ate ao fim; res=%+v", res2)
	}
	if *c.execs != 1 {
		t.Fatalf("a accao aprovada tem de executar EXACTAMENTE uma vez; execucoes=%d", *c.execs)
	}
}

// TestApprovalChainReal_SemAprovacaoNadaExecuta é a guarda negativa e a âncora de
// não-vacuidade: repetir a passagem SEM cerimónia nunca destrava nada. Se destravasse, o
// teste acima estaria a medir outra coisa.
func TestApprovalChainReal_SemAprovacaoNadaExecuta(t *testing.T) {
	ctx := context.Background()
	c := newACRChain(t)
	for i := 1; i <= 3; i++ {
		res, _, err := c.sec.Run(ctx, c.goal, nil)
		if err != nil {
			t.Fatalf("passagem %d: %v", i, err)
		}
		if !res.Escalated {
			t.Fatalf("passagem %d: sem grant a accao tem de continuar a escalar; res=%+v", i, res)
		}
	}
	if *c.execs != 0 {
		t.Fatalf("sem aprovacao NENHUMA execucao pode ocorrer; execucoes=%d", *c.execs)
	}
}

// TestApprovalChainReal_GrantDeOutraAccaoNaoServe: o grant está amarrado à PREVIEW. Um
// grant emitido para outro digest é consumido (uso-único, queima-se) mas NÃO destrava —
// a acção continua a escalar.
func TestApprovalChainReal_GrantDeOutraAccaoNaoServe(t *testing.T) {
	ctx := context.Background()
	c := newACRChain(t)
	if _, _, err := c.sec.Run(ctx, c.goal, nil); err != nil {
		t.Fatalf("1.ª passagem: %v", err)
	}

	// Cerimónia legítima, mas sobre a preview de OUTRA acção.
	c.aprova(t, []byte("preview-de-outra-accao-completamente-diferente"))

	res, _, err := c.sec.Run(ctx, c.goal, nil)
	if err != nil {
		t.Fatalf("2.ª passagem: %v", err)
	}
	if !res.Escalated {
		t.Fatal("um grant de OUTRA accao nao pode destravar esta — a amarra e a preview")
	}
	if *c.execs != 0 {
		t.Fatalf("nada podia ter executado; execucoes=%d", *c.execs)
	}
}

// TestApprovalChainReal_AprovacaoNaoAlteraOTaint sela P4 (AOS-069) através da cadeia real:
// a aprovação humana remove UM obstáculo (o oversight de autonomia); NÃO promove a
// autorização a trusted. O taint da call permanece untrusted no registo de mediação.
func TestApprovalChainReal_AprovacaoNaoAlteraOTaint(t *testing.T) {
	ctx := context.Background()
	worm := audit.NewMemStore()
	c := newACRChainComWORM(t, worm)

	if _, _, err := c.sec.Run(ctx, c.goal, nil); err != nil {
		t.Fatalf("1.ª passagem: %v", err)
	}
	c.aprova(t, c.sink.pending[0].Preview)
	if _, _, err := c.sec.Run(ctx, c.goal, nil); err != nil {
		t.Fatalf("2.ª passagem: %v", err)
	}
	if *c.execs != 1 {
		t.Fatalf("pre-condicao: a accao aprovada devia ter executado; execucoes=%d", *c.execs)
	}

	recs, err := worm.Read(ctx, acrRunID, 1, 1<<20)
	if err != nil {
		t.Fatalf("ler o WORM: %v", err)
	}
	if len(recs) < 2 {
		t.Fatalf("esperava pelo menos a escalada e a permissao seladas; veio %d", len(recs))
	}
	for _, r := range recs {
		if r.Context.Taint != agentruntime.TaintUntrusted {
			t.Fatalf("o taint da autorizacao NUNCA muda com a aprovacao (P4): %q", r.Context.Taint)
		}
	}
	// A última mediação tem de ser a PERMITIDA — a aprovação destravou de facto.
	if got := recs[len(recs)-1].Decision; got != audit.DecisionAllow {
		t.Fatalf("a ultima mediacao devia ser allow; veio %q", got)
	}
}

// TestApprovalChainReal_SemGateOBridgeFicaInerte é a ÂNCORA DE NÃO-VACUIDADE do teste do
// ciclo: omitindo o ApprovalVerifier — a configuração que existia em produção — a cerimónia
// four-eyes corre, o grant é emitido, a evidência é anexada à call… e a acção continua a
// escalar para sempre, porque nada verifica a evidência. Se este teste passasse a fechar o
// ciclo, [TestApprovalChainReal_CicloCompleto] estaria a medir outra coisa.
func TestApprovalChainReal_SemGateOBridgeFicaInerte(t *testing.T) {
	ctx := context.Background()
	c := newACRChainComOpcoes(t, audit.NewMemStore(), false)

	if _, _, err := c.sec.Run(ctx, c.goal, nil); err != nil {
		t.Fatalf("1.ª passagem: %v", err)
	}
	if len(c.sink.pending) != 1 {
		t.Fatalf("pre-condicao: devia ter escalado; pendentes=%d", len(c.sink.pending))
	}
	c.aprova(t, c.sink.pending[0].Preview) // cerimónia LEGÍTIMA, grant válido

	res, _, err := c.sec.Run(ctx, c.goal, nil)
	if err != nil {
		t.Fatalf("2.ª passagem: %v", err)
	}
	if !res.Escalated {
		t.Fatal("sem ApprovalVerifier ninguem le a evidencia: a accao TEM de continuar a escalar")
	}
	if *c.execs != 0 {
		t.Fatalf("sem gate nada pode executar; execucoes=%d", *c.execs)
	}
}
