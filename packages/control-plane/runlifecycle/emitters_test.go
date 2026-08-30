package runlifecycle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plandispatch"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/control-plane/runlifecycle"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/substrate/eventstore"
)

// emitters_test.go — DEF-272 E DEF-273, FECHADOS DE PONTA A PONTA.
//
// O registo de deferimentos é preciso sobre o que faltava, e não era contrato nem
// validação: era QUEM CHAMASSE (DEF-272) e QUEM IMPLEMENTASSE a porta (DEF-273).
// Estes testes exercem exactamente o circuito completo — emitir sob posse, e o
// despachante LER o que foi emitido pela porta que ele declara — porque só o circuito
// fechado prova que os dois lados falam a mesma língua.

const (
	planID = "plan-def"
	runID  = "run-def"
)

// verificador é o nó do documento APROVADO que emite o veredicto: papel reservado
// `verifier` e uma aresta de entrada (o sujeito que ele observou).
func verificador() plan.Node {
	return plan.Node{
		NodeID:    "verif",
		Role:      plan.RoleVerifier,
		Objective: "verificar o trabalho de build",
		DependsOn: []string{"build"},
	}
}

// produtor é o nó que declara o contrato de saída publicado.
func produtor() plan.Node {
	return plan.Node{
		NodeID:    "build",
		Objective: "compilar",
		Outputs: []plan.Output{{
			Name: "relatorio",
			Type: plan.PayloadSummary,
		}},
	}
}

// consumidor declara ler o contrato do produtor.
func consumidor() plan.Node {
	return plan.Node{
		NodeID:    "deploy",
		Objective: "publicar",
		DependsOn: []string{"build"},
		Consumes: []plan.PayloadEdge{{
			From:   "build",
			Output: "relatorio",
			Type:   plan.PayloadSummary,
		}},
	}
}

func documento() plan.PlanDocument {
	return plan.PlanDocument{Nodes: []plan.Node{produtor(), verificador(), consumidor()}}
}

// posseComEmissor reclama o run e devolve a posse mais o emissor do domínio do plano.
func posseComEmissor(ctx context.Context, t *testing.T, store *eventstore.Store, lm *durable.LeaseManager) (*runlifecycle.Tenure, *runlifecycle.PlanRecorder) {
	t.Helper()
	ten, err := runlifecycle.Claim(ctx, store, lm, runID)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	rec, err := runlifecycle.NewPlanRecorder(ten, planID, eventstore.Producer{NHIID: "nhi:orq"})
	if err != nil {
		t.Fatalf("NewPlanRecorder: %v", err)
	}
	return ten, rec
}

// ---------------------------------------------------------------------------
// DEF-272 — O VEREDICTO TEM EMISSOR DE PRODUÇÃO, E O DESPACHANTE LÊ-O.
//
// Falha-antes (o estado registado no deferimento): sem chamador de RecordVerdict,
// `ResultView.Result` devolvia sempre ok=false e o observável `verdict` ficava
// [plandispatch.VerdictAbsent] — INDECIDO para sempre. Metade de um organigrama
// aprovado nunca corria.
// ---------------------------------------------------------------------------

func TestDEF272_VeredictoEmitidoEConsumido(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	lm := replica(t, store, newClock(), "proc-orq")
	_, rec := posseComEmissor(ctx, t, store, lm)

	// (1) EMISSÃO sob posse — o chamador de produção que não existia.
	seq, err := rec.RecordVerdict(ctx, plannerevents.VerdictRecordedPayload{
		NodeID:   "verif",
		Subjects: []string{"build"},
		Outcome:  plannerevents.VerdictPass,
		Reasons:  []string{"cobertura_ok"},
		Metrics:  []plannerevents.VerdictMetric{{Name: "cobertura", Value: 93}},
	}, verificador())
	if err != nil {
		t.Fatalf("RecordVerdict: %v", err)
	}
	if seq == 0 {
		t.Fatal("seq = 0 — o facto não foi apenso")
	}

	// (2) CONSUMO pela porta que o despachante declara.
	rr, err := runlifecycle.NewResultReader(store, planID)
	if err != nil {
		t.Fatalf("NewResultReader: %v", err)
	}
	snap, err := rr.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	got, ok, err := snap.Result(ctx, planID, "verif")
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if !ok {
		t.Fatal("Result devolveu ok=false para um veredicto JÁ APENSO — é exactamente o DEF-272 por fechar")
	}
	if got.Verdict != plandispatch.VerdictPass {
		t.Fatalf("verdict = %q, quer %q", got.Verdict, plandispatch.VerdictPass)
	}
	// A ATRIBUIÇÃO atravessa: sem os sujeitos, um ramo não pode recusar um veredicto
	// que examinou outro trabalho que não o que a aresta guarda.
	if len(got.Subjects) != 1 || got.Subjects[0] != "build" {
		t.Fatalf("subjects = %v, quer [build] — a atribuição não atravessou a projecção", got.Subjects)
	}
	if got.Metrics["cobertura"] != 93 {
		t.Fatalf("métrica cobertura = %d, quer 93", got.Metrics["cobertura"])
	}
	// AUSÊNCIA continua a ser INDECISÃO, nunca falsidade: um nó sem veredicto.
	if _, ok, _ := snap.Result(ctx, planID, "build"); ok {
		t.Fatal("um nó SEM veredicto reportou ok=true — a ausência tem de ficar indecisa")
	}
}

// ---------------------------------------------------------------------------
// DEF-273 — O PAYLOAD TEM EMISSOR E A PayloadView TEM IMPLEMENTAÇÃO.
//
// Falha-antes: `PayloadResolver.Inbox` devolvia ErrPayloadNotPublished em QUALQUER
// passagem, porque não existia implementação da porta nem chamador do emissor.
//
// O teste fecha o circuito pelo `PayloadResolver` — e não pela porta em cru — de
// propósito: é o resolvedor que RE-VERIFICA a referência contra o contrato do
// documento aprovado (tipo, taint efectivo, contract_digest). Testar só a porta
// provaria que o facto entra e sai; testar pelo resolvedor prova que ele passa a
// re-verificação, que é o que o consumidor real exige.
// ---------------------------------------------------------------------------

func TestDEF273_PayloadPublicadoEResolvido(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	lm := replica(t, store, newClock(), "proc-orq")
	_, rec := posseComEmissor(ctx, t, store, lm)

	// (1) PUBLICAÇÃO sob posse — o chamador de produção que não existia.
	if _, err := rec.RecordPayloadPublished(ctx, plannerevents.PayloadPublishedPayload{
		NodeID: "build",
		Output: "relatorio",
		Record: plannerevents.PayloadRecordRef{
			Store:  plannerevents.PayloadStoreEventStore,
			Stream: runID,
			Seq:    7,
			Digest: "sha256:abc",
		},
	}, produtor()); err != nil {
		t.Fatalf("RecordPayloadPublished: %v", err)
	}

	// (2) A IMPLEMENTAÇÃO da porta que não existia.
	pr, err := runlifecycle.NewPayloadReader(store, planID)
	if err != nil {
		t.Fatalf("NewPayloadReader: %v", err)
	}
	snap, err := pr.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// (3) O CONSUMIDOR REAL, com a re-verificação contra o documento aprovado.
	resolver, err := plandispatch.NewPayloadResolver(snap, documento())
	if err != nil {
		t.Fatalf("NewPayloadResolver: %v", err)
	}
	inbox, err := resolver.Inbox(ctx, planID, consumidor())
	if err != nil {
		t.Fatalf("Inbox: %v — é exactamente o ErrPayloadNotPublished do DEF-273 se a porta não estiver implementada", err)
	}
	if len(inbox) != 1 {
		t.Fatalf("inbox = %d referências, quer 1", len(inbox))
	}
	ref := inbox[0]
	if ref.From != "build" || ref.Output != "relatorio" {
		t.Fatalf("referência = %s/%s, quer build/relatorio", ref.From, ref.Output)
	}
	if ref.Digest != "sha256:abc" || ref.Stream != runID || ref.Seq != 7 {
		t.Fatalf("locator = %s/%s/%d, quer %s/sha256:abc/7", ref.Stream, ref.Digest, ref.Seq, runID)
	}
	// O TAINT não desclassifica: um `summary` de um nó NÃO-verificador é untrusted, e
	// é o resolvedor que o exige contra o documento — não o emissor que o declara.
	if ref.Taint != plan.TaintUntrusted {
		t.Fatalf("taint = %q, quer %q — a referência desclassificou", ref.Taint, plan.TaintUntrusted)
	}
	if !ref.IsUntrusted() {
		t.Fatal("IsUntrusted() = false num summary de nó não-verificador")
	}
}

// ---------------------------------------------------------------------------
// DEF-273 (metade fail-closed) — SEM PUBLICAÇÃO, O CONSUMIDOR NÃO LÊ NADA.
//
// Não-vacuidade do teste anterior: prova que o `Inbox` verde vem DA PUBLICAÇÃO e não
// de o resolvedor ser permissivo.
// ---------------------------------------------------------------------------

func TestDEF273_SemPublicacao_ConsumidorNaoLe(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	pr, err := runlifecycle.NewPayloadReader(store, planID)
	if err != nil {
		t.Fatalf("NewPayloadReader: %v", err)
	}
	snap, err := pr.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot de um plano sem factos: %v", err)
	}
	resolver, err := plandispatch.NewPayloadResolver(snap, documento())
	if err != nil {
		t.Fatalf("NewPayloadResolver: %v", err)
	}
	_, err = resolver.Inbox(ctx, planID, consumidor())
	if !errors.Is(err, plandispatch.ErrPayloadNotPublished) {
		t.Fatalf("Inbox sem publicação = %v, quer ErrPayloadNotPublished (espera legítima, nunca um valor por omissão)", err)
	}
}

// ---------------------------------------------------------------------------
// OS EMISSORES SÃO FENCED (ADR-023 §2.4) — mesmo escrevendo no stream do PLANO.
//
// É o teste do [planFenced]: a autoridade é o lease do RUN, o destino é o stream do
// PLANO. Um emissor cujo dono foi superado não escreve, e o facto NÃO chega ao log.
//
// Falha-antes: com o Recorder ligado directamente ao Event Store — que é a forma
// óbvia e a que qualquer wiring apressado escreveria — o dono superado publicaria
// veredictos e payloads de um run que já não serve.
// ---------------------------------------------------------------------------

func TestEmissor_DonoSuperado_NaoEscreveNoStreamDoPlano(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	clk := newClock()

	a := replica(t, store, clk, "proc-a")
	_, rec := posseComEmissor(ctx, t, store, a)

	// Enquanto é dono, emite.
	if _, err := rec.RecordVerdict(ctx, plannerevents.VerdictRecordedPayload{
		NodeID:   "verif",
		Subjects: []string{"build"},
		Outcome:  plannerevents.VerdictPass,
	}, verificador()); err != nil {
		t.Fatalf("emissão do dono legítimo: %v", err)
	}
	before := streamLen(t, store, planID)

	// A expira; B supera-o.
	clk.advance(testTTL + 1)
	b := replica(t, store, clk, "proc-b")
	if _, err := runlifecycle.Claim(ctx, store, b, runID); err != nil {
		t.Fatalf("claim de B: %v", err)
	}

	// A tenta publicar um payload com a posse já superada.
	_, err := rec.RecordPayloadPublished(ctx, plannerevents.PayloadPublishedPayload{
		NodeID: "build",
		Output: "relatorio",
		Record: plannerevents.PayloadRecordRef{
			Store: plannerevents.PayloadStoreEventStore, Stream: runID, Digest: "sha256:zombie",
		},
	}, produtor())
	if !errors.Is(err, durable.ErrStaleFencingToken) {
		t.Fatalf("emissão do dono superado = %v, quer durable.ErrStaleFencingToken", err)
	}
	if after := streamLen(t, store, planID); after != before {
		t.Fatalf("o stream do plano cresceu de %d para %d — o facto do dono superado CHEGOU ao log", before, after)
	}
}

// ---------------------------------------------------------------------------
// O JOURNAL DE RAMOS DO DESPACHANTE ESCREVE DECISÕES, NÃO ESTADO (ADR-023 §2.2).
//
// A metade comportamental do guarda de source: o que o despachante escreve através
// desta porta é `plan.branch_decided`, no stream do PLANO — e o stream do RUN não
// ganha um único facto por causa disso.
// ---------------------------------------------------------------------------

func TestBranchJournal_EscreveDecisaoNaoEstado(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	lm := replica(t, store, newClock(), "proc-orq")
	ten, rec := posseComEmissor(ctx, t, store, lm)

	// O run tem um facto de ciclo de vida, escrito por quem TEM autoridade para isso.
	if _, err := ten.Append(ctx, lifecycleEvent(runID, "estado-inicial")); err != nil {
		t.Fatalf("escrita de ciclo de vida pelo dono: %v", err)
	}
	runAntes := streamLen(t, store, runID)

	j := rec.BranchJournal()
	d := plandispatch.BranchDecision{
		NodeID:          "deploy",
		Taken:           true,
		ConditionDigest: "sha256:cond",
		Sources:         []string{"build"},
	}
	if err := j.Record(ctx, planID, d); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// (a) O stream do RUN não cresceu: o despachante não moveu ciclo de vida nenhum.
	if runDepois := streamLen(t, store, runID); runDepois != runAntes {
		t.Fatalf("o stream do RUN cresceu de %d para %d por causa de uma decisão de ramo — o despachante escreveu ciclo de vida", runAntes, runDepois)
	}

	// (b) A decisão é lida de volta, íntegra.
	got, err := j.Decisions(ctx, planID)
	if err != nil {
		t.Fatalf("Decisions: %v", err)
	}
	back, ok := got["deploy"]
	if !ok {
		t.Fatal("a decisão registada não foi lida de volta")
	}
	if back.Taken != d.Taken || back.ConditionDigest != d.ConditionDigest {
		t.Fatalf("decisão lida = %+v, quer %+v", back, d)
	}
	if len(back.Sources) != 1 || back.Sources[0] != "build" {
		t.Fatalf("sources = %v, quer [build]", back.Sources)
	}

	// (c) IDEMPOTENTE por (plan_id, node_id): a decisão de um nó é um facto ÚNICO.
	antes := streamLen(t, store, planID)
	if err := j.Record(ctx, planID, d); err != nil {
		t.Fatalf("Record repetido: %v", err)
	}
	if depois := streamLen(t, store, planID); depois != antes {
		t.Fatalf("um segundo Record da MESMA decisão apensou um facto novo (%d→%d) — um ramo podia mudar no replay", antes, depois)
	}
}

// ---------------------------------------------------------------------------
// O EMISSOR RECUSA UM PLANO ESTRANHO.
// ---------------------------------------------------------------------------

func TestEmissor_RecusaPlanoEstranho(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	lm := replica(t, store, newClock(), "proc-orq")
	_, rec := posseComEmissor(ctx, t, store, lm)

	_, err := rec.RecordVerdict(ctx, plannerevents.VerdictRecordedPayload{
		PlanID:   "plan-alheio",
		NodeID:   "verif",
		Subjects: []string{"build"},
		Outcome:  plannerevents.VerdictPass,
	}, verificador())
	if !errors.Is(err, runlifecycle.ErrForeignPlan) {
		t.Fatalf("veredicto para outro plano = %v, quer runlifecycle.ErrForeignPlan", err)
	}
}
