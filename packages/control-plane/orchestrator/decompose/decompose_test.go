package decompose_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/decompose"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planner"
)

// fakeModel é o "LLM" injectado nos testes: determinístico, sem I/O vivo. Regista o
// que recebeu (para asserir a montagem do prompt) e devolve `reply`/`err` fixos. Os
// testes são sequenciais (uma chamada por Decompose), pelo que não precisa de
// sincronização — não há acesso concorrente sob `-race`.
type fakeModel struct {
	reply     string
	err       error
	calls     int
	gotSystem string
	gotUser   string
}

func (f *fakeModel) Complete(_ context.Context, system, user string) (string, error) {
	f.calls++
	f.gotSystem, f.gotUser = system, user
	if f.err != nil {
		return "", f.err
	}
	return f.reply, nil
}

const (
	capHashReal = "sha256:CAP-REAL"
	modeloTeste = "modelo-de-teste"
)

// forjadoJSON é o JSON de um PlanDocument multi-nó VÁLIDO cuja `planner_meta` traz
// valores FORJADOS — para provar que o Decomposer os substitui pelos autoritativos.
func forjadoJSON(t *testing.T) string {
	t.Helper()
	doc := plan.PlanDocument{
		PlanVersion: plan.CurrentPlanVersion,
		Objective:   "meta-objectivo",
		BudgetTotal: plan.BudgetEstimate{Tokens: 1000, CostMicroUSD: 2000},
		// Proveniência FORJADA por um "modelo" hostil: nada disto deve sobreviver.
		PlannerMeta: plan.PlannerMeta{Model: "MODELO-FORJADO", PromptVersion: "9.9.9", CapabilitiesHash: "sha256:FORJADO"},
		Nodes: []plan.Node{
			{NodeID: "n1", Role: "worker", Objective: "recolha"},
			{NodeID: "n2", Role: "worker", Objective: "analise", DependsOn: []string{"n1"}},
		},
	}
	raw, err := plan.Encode(doc)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return string(raw)
}

func stdInput() planner.DecomposeInput {
	return planner.DecomposeInput{
		RunID: "run-1", PlanID: "run-1", PlannerNHI: "agent:planner", Attempt: 1,
		Context: planner.PlanningContext{Goal: "construir X", ContextUnits: 3, CapabilitiesHash: capHashReal},
	}
}

func novo(t *testing.T, m decompose.Model, opts ...decompose.Option) *decompose.LLMDecomposer {
	t.Helper()
	d, err := decompose.New(m, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

// TestDecompose_HappyPath_MultiNo: uma resposta válida produz um DAG multi-nó, e a
// proveniência é a AUTORITATIVA (não a que o modelo carimbou).
func TestDecompose_HappyPath_MultiNo(t *testing.T) {
	fm := &fakeModel{reply: forjadoJSON(t)}
	d := novo(t, fm, decompose.WithModelID(modeloTeste))

	doc, err := d.Decompose(context.Background(), stdInput())
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if len(doc.Nodes) != 2 {
		t.Fatalf("esperava 2 nos, obtive %d", len(doc.Nodes))
	}
	// Carimbo autoritativo — os valores forjados NÃO sobrevivem.
	if doc.PlannerMeta.Model != modeloTeste {
		t.Errorf("model: esperava %q, obtive %q", modeloTeste, doc.PlannerMeta.Model)
	}
	if doc.PlannerMeta.PromptVersion != "1.1.0" {
		t.Errorf("prompt_version: esperava 1.1.0, obtive %q", doc.PlannerMeta.PromptVersion)
	}
	if doc.PlannerMeta.CapabilitiesHash != capHashReal {
		t.Errorf("capabilities_hash: esperava %q, obtive %q", capHashReal, doc.PlannerMeta.CapabilitiesHash)
	}
	if fm.calls != 1 {
		t.Errorf("esperava 1 chamada ao modelo, obtive %d", fm.calls)
	}
}

// TestDecompose_ProveniencaForjadaNaoSobrevive: mesmo que o modelo forje TODOS os
// campos de proveniência, o documento devolvido traz os autoritativos — um modelo
// untrusted não pode forjar a proveniência contra a qual AOS-231 valida.
func TestDecompose_ProveniencaForjadaNaoSobrevive(t *testing.T) {
	fm := &fakeModel{reply: forjadoJSON(t)}
	d := novo(t, fm, decompose.WithModelID(modeloTeste))

	doc, err := d.Decompose(context.Background(), stdInput())
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	for campo, got := range map[string]string{
		"model":             doc.PlannerMeta.Model,
		"prompt_version":    doc.PlannerMeta.PromptVersion,
		"capabilities_hash": doc.PlannerMeta.CapabilitiesHash,
	} {
		if strings.Contains(strings.ToUpper(got), "FORJADO") || got == "9.9.9" {
			t.Errorf("%s reteve valor forjado: %q", campo, got)
		}
	}
}

// TestDecompose_JSONComCerca: uma resposta embrulhada em cerca markdown ```json … ```
// é parseada na mesma (defesa do caso comum).
func TestDecompose_JSONComCerca(t *testing.T) {
	fm := &fakeModel{reply: "```json\n" + forjadoJSON(t) + "\n```"}
	d := novo(t, fm, decompose.WithModelID(modeloTeste))

	doc, err := d.Decompose(context.Background(), stdInput())
	if err != nil {
		t.Fatalf("Decompose com cerca: %v", err)
	}
	if len(doc.Nodes) != 2 {
		t.Fatalf("esperava 2 nos, obtive %d", len(doc.Nodes))
	}
}

// TestDecompose_MalformadoFailClosed: prosa que não é JSON ⇒ erro, sem panic.
func TestDecompose_MalformadoFailClosed(t *testing.T) {
	fm := &fakeModel{reply: "isto nao e um plano, e prosa"}
	d := novo(t, fm)

	if _, err := d.Decompose(context.Background(), stdInput()); err == nil {
		t.Fatal("esperava erro fail-closed para resposta malformada")
	}
}

// TestDecompose_DocInvalidoFailClosed: JSON bem-formado mas que viola o schema (nó sem
// objective) é recusado por plan.Decode.
func TestDecompose_DocInvalidoFailClosed(t *testing.T) {
	// Um único nó sem `objective` — passa o JSON, falha o validateShape.
	fm := &fakeModel{reply: `{"plan_version":"1.0.0","objective":"o","budget_total":{"tokens":1,"cost_micro_usd":1},"planner_meta":{"model":"m","prompt_version":"1.0.0","capabilities_hash":"h"},"nodes":[{"node_id":"n1","role":"r","objective":""}]}`}
	d := novo(t, fm)

	_, err := d.Decompose(context.Background(), stdInput())
	if err == nil {
		t.Fatal("esperava erro para documento que viola o schema")
	}
	if !errors.Is(err, plan.ErrEmptyNodeField) {
		t.Errorf("esperava ErrEmptyNodeField encadeado, obtive %v", err)
	}
}

// TestDecompose_ErroDoModeloPropaga: um erro de transporte do modelo é propagado
// (o Planner conta como tentativa falhada e re-tenta).
func TestDecompose_ErroDoModeloPropaga(t *testing.T) {
	sentinela := errors.New("provider indisponivel")
	fm := &fakeModel{err: sentinela}
	d := novo(t, fm)

	_, err := d.Decompose(context.Background(), stdInput())
	if !errors.Is(err, sentinela) {
		t.Fatalf("esperava o erro do modelo encadeado, obtive %v", err)
	}
	if fm.calls != 1 {
		t.Errorf("esperava 1 chamada, obtive %d", fm.calls)
	}
}

// TestDecompose_RespostaVaziaFailClosed: texto vazio ⇒ ErrEmptyResponse.
func TestDecompose_RespostaVaziaFailClosed(t *testing.T) {
	fm := &fakeModel{reply: "   \n  "}
	d := novo(t, fm)

	if _, err := d.Decompose(context.Background(), stdInput()); !errors.Is(err, decompose.ErrEmptyResponse) {
		t.Fatalf("esperava ErrEmptyResponse, obtive %v", err)
	}
}

// TestDecompose_ObjectivoVazioNaoChamaModelo: pedido sem goal falha ANTES da chamada.
func TestDecompose_ObjectivoVazioNaoChamaModelo(t *testing.T) {
	fm := &fakeModel{reply: forjadoJSON(t)}
	d := novo(t, fm)

	in := stdInput()
	in.Context.Goal = "   "
	_, err := d.Decompose(context.Background(), in)
	if !errors.Is(err, decompose.ErrEmptyGoal) {
		t.Fatalf("esperava ErrEmptyGoal, obtive %v", err)
	}
	if fm.calls != 0 {
		t.Errorf("o modelo NAO devia ser chamado; obtive %d chamadas", fm.calls)
	}
}

// TestDecompose_SemCapabilitiesHashFailClosed: sem proveniência não se decompõe.
func TestDecompose_SemCapabilitiesHashFailClosed(t *testing.T) {
	fm := &fakeModel{reply: forjadoJSON(t)}
	d := novo(t, fm)

	in := stdInput()
	in.Context.CapabilitiesHash = ""
	_, err := d.Decompose(context.Background(), in)
	if !errors.Is(err, decompose.ErrNoCapabilitiesHash) {
		t.Fatalf("esperava ErrNoCapabilitiesHash, obtive %v", err)
	}
	if fm.calls != 0 {
		t.Errorf("o modelo NAO devia ser chamado; obtive %d chamadas", fm.calls)
	}
}

// TestDecompose_MensagemUserLevaObjectivoHashECatalogo: a montagem do prompt inclui o
// objectivo, o capabilities_hash e o catálogo (quando fornecido); o system é o template.
func TestDecompose_MensagemUserLevaObjectivoHashECatalogo(t *testing.T) {
	const catalogo = "CATALOGO-TOOLS-XYZ"
	fm := &fakeModel{reply: forjadoJSON(t)}
	d := novo(t, fm, decompose.WithModelID(modeloTeste), decompose.WithCapabilities(catalogo))

	if _, err := d.Decompose(context.Background(), stdInput()); err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	for _, sub := range []string{"construir X", capHashReal, catalogo, modeloTeste} {
		if !strings.Contains(fm.gotUser, sub) {
			t.Errorf("mensagem user nao contem %q", sub)
		}
	}
	if !strings.Contains(fm.gotSystem, "PlanDocument JSON") {
		t.Errorf("mensagem system nao e o template do prompt: %q", fm.gotSystem)
	}
}

// TestNew_ModelNilFailClosed: sem modelo, New recusa.
func TestNew_ModelNilFailClosed(t *testing.T) {
	if _, err := decompose.New(nil); !errors.Is(err, decompose.ErrNoModel) {
		t.Fatalf("esperava ErrNoModel, obtive %v", err)
	}
}

// TestSatisfazPortaDoPlaneador: o Decomposer é atribuível a planner.Decomposer (a
// asserção de compile-time vive na fonte; aqui prova-se com uma instância real).
func TestSatisfazPortaDoPlaneador(t *testing.T) {
	var _ planner.Decomposer = novo(t, &fakeModel{reply: forjadoJSON(t)})
}
