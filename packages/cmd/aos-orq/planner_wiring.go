package main

// planner_wiring.go — T2 do AOS-388: compõe o pipeline goal→DAG multi-nó no aos-orq.
//
// É a metade que faltava do fluxo F2E-02: em vez de receber os nós por `--nodes` ou um
// PlanDocument pronto por `--plan-doc`, o `--goal` corre o PLANEADOR GOVERNADO —
// `planner.Planner.Decompose` (mediação RM, reserva de orçamento CAS, emissão da NHI
// `agent:planner`, N tentativas) → `planvalidate.Validate` (AOS-231) → materialização
// com o DELEGATOR REAL (fim do `recusaSpawn`). O gate humano (AOS-236) fica FORA deste
// âmbito por decisão do dono — é o eixo do DEF-274, ticket próprio.
//
// # A costura do modelo
//
// O `decompose.LLMDecomposer` chama uma porta `decompose.Model`. O modelo REAL (sobre o
// Model Gateway) é a tarefa T2-B; até lá, o único `Model` disponível neste binário é o
// [fixtureModel] — DECLARADAMENTE NÃO-PRODUÇÃO — que devolve um PlanDocument de um
// ficheiro, para o pipeline (governação + validação + spawn real) ser exercido
// ponta-a-ponta sem um LLM vivo. Sem `--decompose-fixture` e sem o gateway, o `--goal`
// recusa fail-closed.
//
// # Identidade
//
// O backbone de identidade é REAL: um emissor ed25519 EFÉMERO (uma execução = um
// processo sob lease) sela uma raiz `human:<worker>` e emite o token NHI do run, cuja
// cadeia de delegação termina no humano (ADR-003). O RM é o mínimo (`rm.New()` +
// `Register`) — o "RM real" do próprio teste do planeador; a cadeia PDP completa
// (NewProductionSecure) é endurecimento posterior.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	budget "github.com/aos-ref/control-plane/budget"
	decompose "github.com/aos-ref/control-plane/orchestrator/decompose"
	planmaterialize "github.com/aos-ref/control-plane/orchestrator/planmaterialize"
	planner "github.com/aos-ref/control-plane/orchestrator/planner"
	planvalidate "github.com/aos-ref/control-plane/orchestrator/planvalidate"
	runlifecycle "github.com/aos-ref/control-plane/runlifecycle"
	rm "github.com/aos-ref/kernel/reference-monitor"
	identity "github.com/aos-ref/platform/identity"
)

const (
	// capPlan é a capability que flui do token do run para a NHI agent:planner (o
	// IssueChild exige Authority ⊆ folha-do-pai ∩ Scope-da-classe-do-filho).
	capPlan = "cap:plan"
	// classeCoordenador é a classe do agente-run (raiz da árvore de delegação do run).
	classeCoordenador = "coordinator"
	// classePlaneador é a classe da NHI agent:planner (o sub-agente que decompõe).
	classePlaneador = "planner"
	// tokenTTL é o tempo de vida dos tokens efémeros deste processo. Curto: o run vive
	// numa só execução sob lease.
	tokenTTL = 30 * time.Minute
)

// fixtureModel é um [decompose.Model] NÃO-PRODUÇÃO: devolve sempre o mesmo texto (um
// PlanDocument lido de ficheiro). Existe para exercitar o pipeline do Planner
// ponta-a-ponta sem um LLM vivo, até o Model Gateway ser composto (T2-B). O binário só o
// usa quando `--decompose-fixture` é dado.
type fixtureModel struct{ conteudo string }

func (m fixtureModel) Complete(_ context.Context, _, _ string) (string, error) {
	return m.conteudo, nil
}

// modeloDeDecomposicao escolhe o [decompose.Model] do `--goal`. Enquanto o Model Gateway
// não é composto (T2-B), a única fonte é o fixture NÃO-PRODUÇÃO; sem ele, o `--goal`
// recusa fail-closed — não há decomposição real por LLM neste binário ainda, e admitir
// um modelo vazio seria a capacidade-fantasma que o banner de postura existe para evitar.
func modeloDeDecomposicao(fixturePath string) (decompose.Model, error) {
	if fixturePath == "" {
		return nil, errors.New("--goal sem modelo: o Model Gateway ainda nao esta composto neste binario (T2-B); para exercitar o pipeline use --decompose-fixture com um PlanDocument")
	}
	return carregarFixtureModel(fixturePath)
}

// carregarFixtureModel lê o ficheiro-fixture do decompositor. Fail-closed: um ficheiro
// vazio não é um modelo.
func carregarFixtureModel(path string) (fixtureModel, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fixtureModel{}, fmt.Errorf("fixture do decompositor %q: %w", path, err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return fixtureModel{}, fmt.Errorf("fixture do decompositor %q vazio", path)
	}
	return fixtureModel{conteudo: string(raw)}, nil
}

// decomporEMaterializar corre o pipeline goal→DAG multi-nó sob a posse deste run: compõe
// o backbone (identidade real + RM mínimo + orçamento partilhado), decompõe o objectivo
// pelo PLANEADOR GOVERNADO, valida a estrutura (AOS-231) e materializa com o DELEGATOR
// REAL. Fail-closed em cada passo. O gate humano fica de fora (DEF-274).
func decomporEMaterializar(ctx context.Context, ten *runlifecycle.Tenure, rec *runlifecycle.PlanRecorder, snap planvalidate.Snapshot, goal string, model decompose.Model, worker string) error {
	runID := ten.RunID()

	// (1) BACKBONE DE IDENTIDADE REAL — emissor efémero + raiz humana + token do run.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("chave do emissor: %w", err)
	}
	classes := map[string]identity.ClassPolicy{
		classeCoordenador: {TTL: tokenTTL, Scope: []string{capPlan}},
		classePlaneador:   {TTL: tokenTTL, Scope: []string{capPlan}},
	}
	iss, err := identity.NewIssuer("iss:aos-orq", priv, classes)
	if err != nil {
		return fmt.Errorf("emissor de identidade: %w", err)
	}
	runTok, err := iss.Issue(ctx, identity.IssueRequest{
		UserID:        "human:" + worker,
		AgentID:       "agt-" + runID,
		AgentClass:    classeCoordenador,
		UserAuthority: []string{capPlan},
	})
	if err != nil {
		return fmt.Errorf("token NHI do run: %w", err)
	}

	// (2) RM MÍNIMO — permite genuinamente e regista a tool do planeador (senão
	// default-deny). É o "RM real" do teste do planner; a cadeia PDP completa é posterior.
	mon := rm.New()
	if err := mon.Register("agent.plan", func(context.Context, []byte) ([]byte, error) { return nil, nil }); err != nil {
		return fmt.Errorf("registo da tool do planeador: %w", err)
	}

	// (3) ORÇAMENTO PARTILHADO por Planner + Delegator + admissão da materialização (uma
	// só árvore, raiz = runID). Tecto local generoso (o tecto real vem do plano de
	// controlo — limitação de escopo deste binário, como em materializar).
	bud, err := budget.New(runID, budget.Amount{Tokens: materializeBudgetTokens, CostMicroUSD: materializeBudgetCost})
	if err != nil {
		return fmt.Errorf("orçamento da árvore: %w", err)
	}

	// (4) DECOMPOSER + PLANEADOR GOVERNADO.
	dec, err := decompose.New(model, decompose.WithModelID("aos-orq/decompose"), decompose.WithCapabilities(renderCapabilities(snap)))
	if err != nil {
		return fmt.Errorf("decompositor: %w", err)
	}
	pl, err := planner.NewPlanner(bud, mon, iss, dec)
	if err != nil {
		return fmt.Errorf("planeador: %w", err)
	}

	// (5) DECOMPOSE(goal) — sob mediação, reserva e NHI filha reais.
	res, err := pl.Decompose(ctx, planner.DecomposeRequest{
		RunID:             runID,
		PlanID:            rec.PlanID(),
		ParentBudgetNode:  runID,
		PlannerBudgetNode: runID + "-planner",
		ParentToken:       runTok.Compact,
		Child: identity.ChildRequest{
			AgentID:    "agent:planner",
			AgentClass: classePlaneador,
			Authority:  []string{capPlan},
		},
		Context: planner.PlanningContext{
			Goal:             goal,
			ContextUnits:     int64(len(goal)/4) + 1,
			CapabilitiesHash: snap.Hash,
		},
	})
	if err != nil {
		return fmt.Errorf("decomposição do objectivo: %w", err)
	}
	fmt.Printf("decomposto: objectivo -> plano de %d nos (tentativas=%d, planner_nhi=%s)\n", len(res.Doc.Nodes), res.Attempts, res.PlannerNHI)

	// (6) VALIDAÇÃO ESTRUTURAL (AOS-231) — fail-closed. O documento é untrusted; a forma
	// já passou em plan.Decode, aqui valida-se aciclicidade/tools/tectos contra o snapshot
	// pinado. O tecto de cardinalidade é o DERIVADO da revisibilidade humana.
	if v := planvalidate.Validate(res.Doc, snap, planvalidate.Ceilings{MaxNodes: planvalidate.DefaultMaxNodes}); v.Rejected() {
		return fmt.Errorf("plano rejeitado na validação estrutural (AOS-231): %s", v.Reason)
	}

	// (7) MATERIALIZAÇÃO ADMISSÃO-PURA (AOS-390, ADR-024). A materialização admite os nós
	// no DAG — folhas com a sua tool call, papéis-que-expandem como nós PENDENTES sem tool
	// — e NÃO produz efeito. O spawn de papéis (Delegator.Spawn, AOS-026) e o arranque de
	// folhas são do despacho governado (plandispatch.Dispatcher/DispatchSink), disparados
	// por elegibilidade. A composição desse laço de despacho no `serve` é o passo aditivo
	// seguinte deste ticket; até lá, `--goal` admite o plano sem despachar.
	adm, err := runlifecycle.NewBudgetAdmission(bud, runID)
	if err != nil {
		return err
	}
	m, err := ten.Materializer(ctx, snap, rec, adm)
	if err != nil {
		return fmt.Errorf("materializador: %w", err)
	}
	payload, err := m.Materialize(ctx, planmaterialize.Request{
		RunID:          runID,
		PlanID:         rec.PlanID(),
		ParentToken:    runTok.Compact,
		RootBudgetNode: runID,
		Doc:            res.Doc,
	})
	if err != nil {
		if rerr := adm.Release(ctx); rerr != nil {
			return fmt.Errorf("materialização falhou (%w) e a devolução das reservas também: %v", err, rerr)
		}
		return fmt.Errorf("materialização: %w", err)
	}
	if err := adm.Commit(ctx); err != nil {
		return fmt.Errorf("confirmação das reservas de admissão: %w", err)
	}

	fmt.Printf("materializado: plano=%s nos=%d oraculo=snapshot(%s)\n", payload.PlanID, len(payload.Nodes), snap.Hash)
	for _, n := range payload.Nodes {
		fmt.Printf("  no=%s kind=%s tools=%s\n", n.NodeID, n.Kind, strings.Join(n.Tools, "|"))
	}
	return nil
}

// renderCapabilities serializa o catálogo pinado do snapshot como texto para a mensagem
// user do decompositor (as tools que o modelo pode referenciar por {name,version,digest}).
// É apresentação, não política — a resolução real das tools é de AOS-231.
func renderCapabilities(snap planvalidate.Snapshot) string {
	if len(snap.Tools) == 0 {
		return "(catálogo de capabilities vazio)"
	}
	var b strings.Builder
	for _, t := range snap.Tools {
		fmt.Fprintf(&b, "- %s@%s (%s)\n", t.Name, t.Version, t.Digest)
	}
	return b.String()
}
