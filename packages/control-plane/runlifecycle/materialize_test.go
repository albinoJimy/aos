package runlifecycle_test

import (
	"context"
	"errors"
	"testing"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planmaterialize"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
	"github.com/aos-ref/control-plane/runlifecycle"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/substrate/eventstore"
)

// materialize_test.go — O ORÁCULO DE EFEITO REAL (DEF-273, segunda metade).
//
// A propriedade sob prova não é «o oráculo foi passado» — isso seria testar uma
// atribuição. É a CONSEQUÊNCIA observável: um nó com o papel reservado `verifier`
// materializa com a sua autoridade READ-ONLY INTACTA e a de EFEITO retirada.
//
// Com o `DefaultEffectOracle` (o que estava em vigor por não haver composition root),
// TUDO conta como efeito: o verificador sai com `Tools` VAZIO e sem tool call. O teste
// de não-vacuidade abaixo reproduz esse estado exacto, para que o verde do primeiro
// teste não possa vir de acidente.

// --- ferramentas pinadas do snapshot ---------------------------------------
//
// `IsEffectTool(c) = c.Egress != EgressNone || c.Reversibility.IsIrreversible()`.
// Uma é leitura local reversível (SEM efeito); a outra fala para fora (COM efeito).

func toolLeitura() plan.ToolRef {
	return plan.ToolRef{Name: "fs.read", Version: "1.0.0", Digest: "sha256:aaa"}
}

func toolEfeito() plan.ToolRef {
	return plan.ToolRef{Name: "http.post", Version: "2.0.0", Digest: "sha256:bbb"}
}

// snapshotPinado é o conjunto PINADO contra o qual o oráculo resolve.
func snapshotPinado() planvalidate.Snapshot {
	return planvalidate.Snapshot{
		Hash: "sha256:snap",
		Tools: []planvalidate.Capability{
			{
				Name: "fs.read", Version: "1.0.0", Digest: "sha256:aaa",
				Admissible:    true,
				Sensitivity:   risk.SensitivityPublic,
				Egress:        risk.EgressNone,
				Reversibility: risk.Reversible,
			},
			{
				Name: "http.post", Version: "2.0.0", Digest: "sha256:bbb",
				Admissible:    true,
				Sensitivity:   risk.SensitivityPublic,
				Egress:        risk.EgressExternal,
				Reversibility: risk.Reversible,
			},
		},
	}
}

// docComVerificador é o documento APROVADO: um produtor comum e um verificador que
// declara AS DUAS tools. É o verificador que expõe o clamp — o nó comum não é
// clampado por papel nenhum.
func docComVerificador() plan.PlanDocument {
	return plan.PlanDocument{Nodes: []plan.Node{
		{
			NodeID:    "build",
			Objective: "compilar",
			Tools:     []plan.ToolRef{toolLeitura()},
		},
		{
			NodeID:    "verif",
			Role:      plan.RoleVerifier,
			Objective: "verificar o build",
			DependsOn: []string{"build"},
			// A ORDEM importa: a tool de EFEITO vem PRIMEIRO. Se o clamp não corresse,
			// `primaryTool` devolveria `http.post` — a tool call de um verificador a
			// falar para fora. É o pior caso, e é por isso que está aqui.
			Tools: []plan.ToolRef{toolEfeito(), toolLeitura()},
		},
	}}
}

// spawnerNulo satisfaz a porta de spawn sem delegar nada: o documento de teste não
// tem papéis-que-expandem, pelo que ela nunca é chamada. Declarado em vez de omitido
// porque o materializador a exige — e um duplo que registasse chamadas daria a
// impressão de que este teste as exercita, o que não faz.
type spawnerNulo struct{ chamado bool }

func (s *spawnerNulo) Spawn(context.Context, planmaterialize.RoleSpawn) error {
	s.chamado = true
	return nil
}

// materializa corre a composição sob posse e devolve o payload registado.
func materializa(ctx context.Context, t *testing.T, snap planvalidate.Snapshot, opts ...planmaterialize.Option) plannerevents.MaterializedPayload {
	t.Helper()
	store := newStore(t)
	lm := replica(t, store, newClock(), "proc-orq")
	const runID = "run-mat"
	const planID = "plan-mat"

	ten, err := runlifecycle.Claim(ctx, store, lm, runID)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	rec, err := runlifecycle.NewPlanRecorder(ten, planID, eventstore.Producer{NHIID: "nhi:orq"})
	if err != nil {
		t.Fatalf("NewPlanRecorder: %v", err)
	}

	b, err := budget.New(runID, budget.Amount{Tokens: 1_000_000, CostMicroUSD: 1_000_000})
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	adm, err := runlifecycle.NewBudgetAdmission(b, runID)
	if err != nil {
		t.Fatalf("NewBudgetAdmission: %v", err)
	}

	m, err := ten.Materializer(ctx, snap, rec, adm, &spawnerNulo{}, opts...)
	if err != nil {
		t.Fatalf("Materializer: %v", err)
	}
	payload, err := m.Materialize(ctx, planmaterialize.Request{
		RunID:          runID,
		PlanID:         planID,
		ParentToken:    "tok-pai",
		RootBudgetNode: runID,
		Doc:            docComVerificador(),
	})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if err := adm.Commit(ctx); err != nil {
		t.Fatalf("Commit das reservas: %v", err)
	}
	if adm.Pendentes() != 0 {
		t.Fatalf("ficaram %d reserva(s) por saldar após Commit", adm.Pendentes())
	}
	return payload
}

// toolsDe devolve as tools materializadas de um nó.
func toolsDe(t *testing.T, p plannerevents.MaterializedPayload, nodeID string) []string {
	t.Helper()
	for _, n := range p.Nodes {
		if n.NodeID == nodeID {
			return n.Tools
		}
	}
	t.Fatalf("nó %q ausente de plan.materialized", nodeID)
	return nil
}

// ---------------------------------------------------------------------------
// TESTE — COM O ORÁCULO REAL: o verificador MANTÉM a autoridade read-only.
//
// Falha-antes (o estado registado no DEF-273): sem o oráculo ligado, `Tools` do
// verificador vem VAZIO — «o AC1 de AOS-271 cumpre-se por o verificador não conseguir
// fazer nada».
// ---------------------------------------------------------------------------

func TestDEF273_OraculoReal_VerificadorMantemAutoridadeReadOnly(t *testing.T) {
	ctx := context.Background()
	payload := materializa(ctx, t, snapshotPinado())

	verif := toolsDe(t, payload, "verif")
	if len(verif) == 0 {
		t.Fatal("o verificador materializou com autoridade VAZIA — é exactamente o DEF-273 por fechar: o oráculo de efeito real não está a chegar ao materializador")
	}
	// A de LEITURA sobrevive; a de EFEITO foi retirada. As duas asserções são
	// necessárias: só a primeira passaria com um oráculo que não retira nada.
	if !contem(verif, "cap:tool:fs.read") {
		t.Fatalf("tools do verificador = %v — a tool READ-ONLY foi retirada; o clamp está a cortar a mais", verif)
	}
	if contem(verif, "cap:tool:http.post") {
		t.Fatalf("tools do verificador = %v — a tool DE EFEITO sobreviveu ao clamp; um verificador não emite autoridade de efeito (ADR-022 §2.2)", verif)
	}

	// O nó COMUM não é clampado: o clamp é do papel, não de toda a gente.
	if b := toolsDe(t, payload, "build"); !contem(b, "cap:tool:fs.read") {
		t.Fatalf("tools do nó comum = %v, quer conter cap:tool:fs.read — o clamp do verificador escapou para nós que não o são", b)
	}
}

// ---------------------------------------------------------------------------
// TESTE — NÃO-VACUIDADE: com o oráculo por OMISSÃO, o verificador fica sem nada.
//
// Reproduz o estado que o DEF-273 regista, para provar que o verde do teste anterior
// vem do oráculo REAL e não de o clamp nunca correr. Constrói o materializador
// DIRECTAMENTE (não pela via da posse), porque a via da posse — de propósito — não
// deixa chegar aqui.
// ---------------------------------------------------------------------------

func TestDEF273_NaoVacuidade_OraculoPorOmissaoNeutralizaOVerificador(t *testing.T) {
	ctx := context.Background()
	m, err := planmaterialize.NewMaterializer(
		admissaoSempreOK{},
		folhaIgnorada{},
		&spawnerNulo{},
		&gravadorEmMemoria{},
		// SEM WithEffectOracle: fica o DefaultEffectOracle (tudo é efeito).
	)
	if err != nil {
		t.Fatalf("NewMaterializer: %v", err)
	}
	payload, err := m.Materialize(ctx, planmaterialize.Request{
		RunID: "r", PlanID: "p", ParentToken: "tok", RootBudgetNode: "r",
		Doc: docComVerificador(),
	})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if got := toolsDe(t, payload, "verif"); len(got) != 0 {
		t.Fatalf("com o oráculo por OMISSÃO o verificador saiu com %v — o teste deixou de reproduzir o defeito do DEF-273, e o teste anterior deixou de provar alguma coisa", got)
	}
}

// ---------------------------------------------------------------------------
// TESTE — O ORÁCULO NÃO É SUBSTITUÍVEL PELO CHAMADOR.
//
// A via da posse acrescenta o oráculo real DEPOIS das opções do chamador, pelo que um
// `WithEffectOracle` permissivo que chegasse por `opts` não pode baixar o clamp. É a
// diferença entre «o wiring certo foi feito» e «o wiring errado é impossível».
// ---------------------------------------------------------------------------

func TestDEF273_OraculoDoChamadorNaoSubstituiOReal(t *testing.T) {
	ctx := context.Background()
	// Um oráculo que declara que NADA tem efeito: se vencesse, a tool de efeito
	// sobreviveria ao clamp do verificador.
	permissivo := planmaterialize.WithEffectOracle(func(plan.ToolRef) bool { return false })

	payload := materializa(ctx, t, snapshotPinado(), permissivo)
	if got := toolsDe(t, payload, "verif"); contem(got, "cap:tool:http.post") {
		t.Fatalf("tools do verificador = %v — o oráculo PERMISSIVO do chamador venceu o real; o clamp é substituível por quem compõe", got)
	}
}

// ---------------------------------------------------------------------------
// TESTE — SEM SNAPSHOT NÃO HÁ MATERIALIZADOR (fail-closed).
//
// Um snapshot vazio faria o oráculo devolver «efeito» para tudo (nada resolve),
// reproduzindo o default que esta via existe para eliminar. Recusa-se em vez de o
// reproduzir em silêncio.
// ---------------------------------------------------------------------------

func TestDEF273_SemSnapshotRecusa(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	lm := replica(t, store, newClock(), "proc-orq")
	ten, err := runlifecycle.Claim(ctx, store, lm, "run-sem-snap")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	rec, err := runlifecycle.NewPlanRecorder(ten, "plan-sem-snap", eventstore.Producer{})
	if err != nil {
		t.Fatalf("NewPlanRecorder: %v", err)
	}
	b, _ := budget.New("t", budget.Amount{Tokens: 10})
	adm, _ := runlifecycle.NewBudgetAdmission(b, "t")

	_, err = ten.Materializer(ctx, planvalidate.Snapshot{}, rec, adm, &spawnerNulo{})
	if !errors.Is(err, runlifecycle.ErrSemSnapshot) {
		t.Fatalf("materializador com snapshot vazio = %v, quer runlifecycle.ErrSemSnapshot", err)
	}
}

// ---------------------------------------------------------------------------
// TESTE — A ADMISSÃO SOBRE O ORÇAMENTO NEGA SEM HEADROOM, E NÃO VAZA RESERVAS.
//
// A negação aborta o plano INTEIRO antes de qualquer efeito (duas fases), e as
// reservas dos nós já admitidos têm de ser DEVOLVIDAS — senão cada tentativa falhada
// encolhia a árvore até negar tudo.
// ---------------------------------------------------------------------------

func TestBudgetAdmission_SemHeadroomNegaENaoVaza(t *testing.T) {
	ctx := context.Background()
	b, err := budget.New("arvore", budget.Amount{Tokens: 10})
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	adm, err := runlifecycle.NewBudgetAdmission(b, "arvore")
	if err != nil {
		t.Fatalf("NewBudgetAdmission: %v", err)
	}

	// Cabe.
	v, err := adm.Admit(ctx, planmaterialize.AdmitRequest{PlanID: "p", NodeID: "a", Tokens: 8})
	if err != nil || !v.Admitted {
		t.Fatalf("primeiro Admit = (%+v, %v), quer admitido", v, err)
	}
	// Não cabe: 8+8 > 10.
	v, err = adm.Admit(ctx, planmaterialize.AdmitRequest{PlanID: "p", NodeID: "b", Tokens: 8})
	if err != nil {
		t.Fatalf("segundo Admit devolveu erro (%v) — falta de headroom é NEGAÇÃO declarada, não avaria", err)
	}
	if v.Admitted {
		t.Fatal("o segundo nó foi admitido sem headroom — a admissão não está a impor o tecto da árvore")
	}
	if v.Reason == "" {
		t.Fatal("negação sem razão legível — ela viaja para o plan.materialized")
	}

	// O plano aborta: a reserva do nó JÁ admitido é DEVOLVIDA.
	if adm.Pendentes() != 1 {
		t.Fatalf("reservas pendentes = %d, quer 1 (a do nó admitido)", adm.Pendentes())
	}
	if err := adm.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if adm.Pendentes() != 0 {
		t.Fatalf("ficaram %d reserva(s) após Release — vazamento", adm.Pendentes())
	}

	// E a árvore recuperou o headroom: o mesmo pedido que falhou agora passa.
	v, err = adm.Admit(ctx, planmaterialize.AdmitRequest{PlanID: "p2", NodeID: "b", Tokens: 8})
	if err != nil || !v.Admitted {
		t.Fatalf("após Release, Admit = (%+v, %v), quer admitido — o headroom não foi devolvido à árvore", v, err)
	}
}

// ---------------------------------------------------------------------------
// TESTE — RE-ADMITIR O MESMO NÓ NÃO RESERVA DUAS VEZES.
//
// A materialização é re-invocável; um retry não pode drenar a árvore.
// ---------------------------------------------------------------------------

func TestBudgetAdmission_RetryNaoDrenaAArvore(t *testing.T) {
	ctx := context.Background()
	b, _ := budget.New("arvore", budget.Amount{Tokens: 10})
	adm, _ := runlifecycle.NewBudgetAdmission(b, "arvore")

	for i := 0; i < 5; i++ {
		v, err := adm.Admit(ctx, planmaterialize.AdmitRequest{PlanID: "p", NodeID: "a", Tokens: 8})
		if err != nil || !v.Admitted {
			t.Fatalf("Admit #%d = (%+v, %v) — um retry do MESMO nó foi negado: a reserva está a ser repetida e a árvore drenada", i, v, err)
		}
	}
	if got := adm.Pendentes(); got != 1 {
		t.Fatalf("reservas pendentes = %d após 5 retries do mesmo nó, quer 1", got)
	}
}

// --- duplos mínimos para o teste de não-vacuidade ---------------------------

type admissaoSempreOK struct{}

func (admissaoSempreOK) Admit(context.Context, planmaterialize.AdmitRequest) (planmaterialize.AdmitVerdict, error) {
	return planmaterialize.AdmitVerdict{Admitted: true}, nil
}

type folhaIgnorada struct{}

func (folhaIgnorada) AdmitLeaf(context.Context, planmaterialize.LeafNode) error { return nil }

type gravadorEmMemoria struct {
	ultimo plannerevents.MaterializedPayload
}

func (g *gravadorEmMemoria) RecordMaterialized(_ context.Context, p plannerevents.MaterializedPayload) (uint64, error) {
	g.ultimo = p
	return 1, nil
}

func contem(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
