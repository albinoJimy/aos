package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	autonomy "github.com/aos-ref/control-plane/governance/autonomy"
	pdp "github.com/aos-ref/control-plane/pdp"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	domain "github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/signing"
)

// ---------------------------------------------------------------------------
// AOS-021 §7 item 4 — o ciclo COMPLETO ao nível do NÓ
// ---------------------------------------------------------------------------
//
// O teste de composição do pilar (integration/approval_chain_real_test.go) cobre a CADEIA
// DE MEDIAÇÃO. Este cobre o que só existe no nó, e que a corrida ao vivo mostrou estar
// partido de três maneiras que nenhum teste unitário podia ver:
//
//	(1) a escalada não CAPTURAVA o turno ⇒ a retoma encontrava a trajectória vazia;
//	(2) o run suspenso segurava o LEASE durável ⇒ a retoma não o conseguia re-hospedar;
//	(3) a suspensão é durável ⇒ uma SEGUNDA escalada morria em waiting_on_human→waiting_on_human.
//
// A escalada vem do ORÁCULO DE AUTONOMIA — a única fonte alcançável em produção (Cedar só
// exprime permit/deny). Nada aqui injecta hooks de teste na cadeia: é a cadeia do nó.
//
// A propriedade que mais importa é NEGATIVA: o efeito aplicado no 1.º ciclo NÃO pode correr
// segunda vez no 2.º. E o turno reproduzido vem das CAPTURAS REAIS — mede-se contando as
// idas ao modelo.

const (
	acnClass    = "agent-worker" // classe presente na allowlist da política de referência
	acnCap      = "cap:fs.read"  // capability permitida por allowlist + regra Cedar
	acnAgent    = "agt-ciclo"
	acnRunID    = "run-ciclo-no"
	acnPublisher = "publisher-ciclo"
)

// acnModel pede UMA tool call por turno (turnos 1 e 2) e conclui no 3.º. Conta as idas ao
// modelo: é assim que se prova que a retoma reproduziu da captura em vez de reinterrogar.
type acnModel struct{ hits int64 }

func (m *acnModel) Call(_ context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	atomic.AddInt64(&m.hits, 1)
	switch view.Turn {
	case 1:
		return acnCall("passo_um", "doc-a"), nil
	case 2:
		return acnCall("passo_dois", "doc-b"), nil
	default:
		return agentruntime.ModelResponse{Text: "concluido", Final: true}, nil
	}
}

func (m *acnModel) chamadas() int64 { return atomic.LoadInt64(&m.hits) }

func acnCall(tool, res string) agentruntime.ModelResponse {
	return agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{{
		ToolID: tool, Capability: acnCap, Input: []byte(`{"doc":"` + res + `"}`),
		ResourceType: "doc", ResourceValue: res, ResourceRegion: "eu",
	}}}
}

// acnHarness é o nó completo + o serviço, com o bridge de aprovação ligado.
type acnHarness struct {
	node  *Node
	svc   *NodeService
	model *acnModel
	execs map[string]*int64
	keys  [2]ed25519.PrivateKey
}

// newACNHarness arranca um nó REAL: execução durável, PDP com bundle assinado + oráculo de
// autonomia em L4 (danger ⇒ confirm ⇒ requer humano), e four-eyes com dois aprovadores
// pinados. É a configuração do deployment endurecido.
func newACNHarness(t *testing.T) *acnHarness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	signer := acnSigner(t)
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

	levels := autonomy.NewLevelRegistry()
	if _, err := levels.SetLevel(ctx, acnAgent, "fs", autonomy.L4, "teste de ciclo", "operador"); err != nil {
		t.Fatalf("SetLevel: %v", err)
	}
	policyDP, err := pdp.Open("../../control-plane/pdp/policies", pdp.WithAutonomyOracle(levels))
	if err != nil {
		t.Fatalf("abrir bundle de politica de referencia: %v", err)
	}

	pubA, privA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, privB, _ := ed25519.GenerateKey(rand.Reader)

	model := &acnModel{}
	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.Model = model
	cfg.Catalog = catalogStub{entries: []domain.Entry{
		acnEntry(t, signer, "passo_um"), acnEntry(t, signer, "passo_dois"),
	}}
	cfg.Revalidator = revalidator
	cfg.IssuerClasses = map[string]identity.ClassPolicy{
		acnClass: {TTL: 15 * time.Minute, Scope: []string{acnCap}},
	}
	cfg.Policy = integration.StaticPolicy{MaxEgress: domain.EgressInternal}
	cfg.PDP = policyDP
	cfg.Authority = authz.NewStaticAuthoritySource().
		Set("human:"+tnHuman, acnCap).
		Set(acnAgent, acnCap).
		Set("agent:"+acnClass, acnCap)
	cfg.Approvers = []ApproverConfig{
		{Principal: "human:alice", PubKey: pubA, Authority: []string{"approve:danger", "approve:gray"}},
		{Principal: "human:bob", PubKey: pubB, Authority: []string{"approve:danger", "approve:gray"}},
	}

	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })

	h := &acnHarness{node: node, model: model, keys: [2]ed25519.PrivateKey{privA, privB},
		execs: map[string]*int64{"passo_um": new(int64), "passo_dois": new(int64)}}
	for tool, n := range h.execs {
		contador := n
		conteudo := []byte(`{"content":"` + tool + `"}`)
		if err := node.Runtime.Register(tool, func(context.Context, []byte) ([]byte, error) {
			atomic.AddInt64(contador, 1)
			return conteudo, nil
		}); err != nil {
			t.Fatalf("Register %s: %v", tool, err)
		}
	}

	svc, err := NewNodeService(node)
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	h.svc = svc
	return h
}

func acnSigner(t *testing.T) *signing.Signer {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 7)
	}
	s, err := signing.NewSigner(acnPublisher, ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}

// acnEntry constrói uma entrada de catálogo ASSINADA, sem egress.
func acnEntry(t *testing.T, signer *signing.Signer, id string) domain.Entry {
	t.Helper()
	ver := domain.Version{Major: 1}
	contract := domain.Contract{Egress: domain.EgressNone}
	dig := digest.SHA256Digester{}.Digest(domain.KindTool, contract)
	return domain.Entry{
		ID: id, Version: ver, Kind: domain.KindTool, Digest: dig,
		Signature: signer.Sign(id, ver, dig), Contract: contract,
		Provenance: domain.Provenance{
			Origin: "mcp://aos-021-ciclo", Publisher: signer.KeyID(),
			Timestamp: "2026-08-08T00:00:00Z", Trust: domain.TrustFirstSeen,
		},
		Status: domain.StatusActive,
	}
}

// token minta uma credencial NHI FRESCA — a retoma exige-o (a original não é persistida).
func (h *acnHarness) token(t *testing.T) string {
	t.Helper()
	tok, err := h.node.Authority.MintForHuman(context.Background(), tnHuman, acnAgent, acnClass, []string{acnCap})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return tok.Compact
}

// aprovaPendente corre a cerimónia four-eyes REAL sobre o pendente durável do run.
// requestID muda a cada cerimónia: é o âmbito anti-replay dos challenges.
func (h *acnHarness) aprovaPendente(t *testing.T, requestID string) integration.PendingRecord {
	t.Helper()
	ctx := context.Background()
	pendentes, err := h.node.PendingApprovals.ListForRun(ctx, acnRunID)
	if err != nil {
		t.Fatalf("ListForRun: %v", err)
	}
	if len(pendentes) != 1 {
		t.Fatalf("devia haver exactamente 1 pendente por decidir; veio %d", len(pendentes))
	}
	p := pendentes[0]
	req := integration.FourEyesRequest{
		RequestID: requestID, Preview: p.Preview,
		RiskClass: risk.ClassDanger, DualControlRequired: true,
	}
	legA := integration.SignFourEyesLeg(h.keys[0], req, "human:alice", "sess-A", "cred-A", acnChallenge(requestID, 'a'), nil)
	legB := integration.SignFourEyesLeg(h.keys[1], req, "human:bob", "sess-B", "cred-B", acnChallenge(requestID, 'b'), nil)
	grant, err := h.node.ApprovalBroker.Approve(ctx, requestID, req, legA, legB)
	if err != nil {
		t.Fatalf("cerimonia four-eyes (%s): %v", requestID, err)
	}
	if len(grant.Approvers) != 2 || !grant.DualControl {
		t.Fatalf("o grant tem de registar as DUAS pernas e o dual-control: %+v", grant)
	}
	// O pendente decidido sai da lista do operador.
	if err := h.node.PendingApprovals.Expire(ctx, p.RunID, p.StepID); err != nil {
		t.Fatalf("Expire do pendente decidido: %v", err)
	}
	return p
}

// acnChallenge deriva um challenge de 32 bytes distinto por (pedido, aprovador).
func acnChallenge(requestID string, quem byte) []byte {
	c := make([]byte, 32)
	for i := range c {
		c[i] = quem ^ byte(i) ^ requestID[i%len(requestID)]
	}
	return c
}

// esperaFim espera que o run saia do registo de em-curso.
func (h *acnHarness) esperaFim(t *testing.T, runID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, _, err := h.svc.Wait(ctx, runID); err != nil {
		t.Fatalf("Wait(%s): %v", runID, err)
	}
}

func (h *acnHarness) execucoes(tool string) int64 { return atomic.LoadInt64(h.execs[tool]) }

func (h *acnHarness) submete(t *testing.T) {
	t.Helper()
	if err := h.svc.Submit(context.Background(), agentruntime.Goal{
		RunID:      acnRunID,
		Principal:  referencemonitor.Principal{NHIID: acnAgent},
		Credential: h.token(t),
		Objective:  "dois passos governados",
		MaxTurns:   5,
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	h.esperaFim(t, acnRunID)
}

// TestApprovalCycleNode_DoisCiclosSemRepetirEfeitos é o teste de composição do §7 item 4.
//
// Dois ciclos completos no MESMO run: cada turno pede uma acção que o oráculo escala, cada
// uma é aprovada por uma cerimónia própria, e a retoma reproduz os turnos já vividos a
// partir das capturas. O 2.º ciclo é o que prova o essencial — a acção do 1.º NÃO volta a
// executar, e a segunda suspensão funciona (era o par que matava o run como FALHADO).
func TestApprovalCycleNode_DoisCiclosSemRepetirEfeitos(t *testing.T) {
	ctx := context.Background()
	h := newACNHarness(t)

	// ---------- ciclo 1: turno 1 escala ----------
	h.submete(t)
	oc, susp := h.svc.Suspended(ctx, acnRunID)
	if !susp {
		t.Fatal("o run devia ter ficado SUSPENSO a espera de humano")
	}
	if !oc.Result.Escalated {
		t.Fatalf("o desfecho parcial devia declarar a escalada: %+v", oc.Result)
	}
	if h.execucoes("passo_um") != 0 || h.execucoes("passo_dois") != 0 {
		t.Fatalf("uma accao ESCALADA nao produz efeito nenhum; um=%d dois=%d",
			h.execucoes("passo_um"), h.execucoes("passo_dois"))
	}
	p1 := h.aprovaPendente(t, "req-ciclo-1")
	if p1.ToolID != "passo_um" || p1.Turn != 1 {
		t.Fatalf("o pendente devia descrever a accao do turno 1; veio %q turno %d", p1.ToolID, p1.Turn)
	}

	chamadasApos1 := h.model.chamadas()
	if err := h.svc.Resume(ctx, acnRunID, h.token(t)); err != nil {
		t.Fatalf("Resume (ciclo 1): %v", err)
	}
	h.esperaFim(t, acnRunID)

	// A acção aprovada executou; a seguinte escalou. É a SEGUNDA suspensão do mesmo run.
	if got := h.execucoes("passo_um"); got != 1 {
		t.Fatalf("a accao aprovada do turno 1 devia executar 1 vez; correu %d", got)
	}
	if got := h.execucoes("passo_dois"); got != 0 {
		t.Fatalf("a accao do turno 2 ainda nao foi aprovada; correu %d", got)
	}
	if _, susp := h.svc.Suspended(ctx, acnRunID); !susp {
		t.Fatal("a SEGUNDA suspensao tem de funcionar — nao pode matar o run como falhado")
	}
	// Na retoma, o turno 1 veio da CAPTURA: só o turno NOVO (o 2) foi ao modelo.
	if delta := h.model.chamadas() - chamadasApos1; delta != 1 {
		t.Fatalf("o turno reproduzido nao pode reinterrogar o modelo; idas novas=%d", delta)
	}

	// ---------- ciclo 2: aprova o turno 2 e conclui ----------
	p2 := h.aprovaPendente(t, "req-ciclo-2")
	if p2.ToolID != "passo_dois" || p2.Turn != 2 {
		t.Fatalf("o pendente devia descrever a accao do turno 2; veio %q turno %d", p2.ToolID, p2.Turn)
	}
	chamadasApos2 := h.model.chamadas()
	if err := h.svc.Resume(ctx, acnRunID, h.token(t)); err != nil {
		t.Fatalf("Resume (ciclo 2): %v", err)
	}
	h.esperaFim(t, acnRunID)

	// *** A PROPRIEDADE NEGATIVA ***: o efeito já aplicado NÃO se repete.
	if got := h.execucoes("passo_um"); got != 1 {
		t.Fatalf("a accao do turno 1 NAO podia correr segunda vez na retoma; total=%d", got)
	}
	if got := h.execucoes("passo_dois"); got != 1 {
		t.Fatalf("a accao aprovada do turno 2 devia executar exactamente 1 vez; correu %d", got)
	}
	// Turnos 1 e 2 vieram das capturas; só o turno 3 (o que conclui) foi ao modelo.
	if delta := h.model.chamadas() - chamadasApos2; delta != 1 {
		t.Fatalf("dois turnos reproduzidos e um novo: idas novas=%d", delta)
	}
	fim, done := h.svc.Outcome(acnRunID)
	if !done || !fim.Result.Terminated {
		t.Fatalf("o run devia ter TERMINADO; done=%t res=%+v", done, fim.Result)
	}
	if _, ainda := h.svc.Suspended(ctx, acnRunID); ainda {
		t.Fatal("um run concluido nao pode continuar a constar como suspenso")
	}
}

// TestApprovalCycleNode_SemAprovacaoARetomaNaoDestrava é a guarda negativa e a âncora de
// não-vacuidade: retomar SEM cerimónia não executa nada — o run volta a suspender. Se
// destravasse, o teste acima estaria a medir "a retoma executa tudo", que seria um bypass
// da mediação, não uma aprovação.
func TestApprovalCycleNode_SemAprovacaoARetomaNaoDestrava(t *testing.T) {
	ctx := context.Background()
	h := newACNHarness(t)

	h.submete(t)
	if _, susp := h.svc.Suspended(ctx, acnRunID); !susp {
		t.Fatal("pre-condicao: o run devia ter suspendido")
	}

	// RETOMA SEM APROVAR — três vezes, para excluir qualquer efeito de acumulação.
	for i := 1; i <= 3; i++ {
		if err := h.svc.Resume(ctx, acnRunID, h.token(t)); err != nil {
			t.Fatalf("Resume %d: %v", i, err)
		}
		h.esperaFim(t, acnRunID)
		if _, susp := h.svc.Suspended(ctx, acnRunID); !susp {
			t.Fatalf("retoma %d: sem aprovacao o run tem de voltar a suspender, nao a falhar", i)
		}
	}
	if h.execucoes("passo_um") != 0 || h.execucoes("passo_dois") != 0 {
		t.Fatalf("sem aprovacao NENHUMA accao pode executar; um=%d dois=%d",
			h.execucoes("passo_um"), h.execucoes("passo_dois"))
	}
}
