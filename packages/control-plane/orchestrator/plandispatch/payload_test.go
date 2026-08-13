package plandispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// payload_test.go — O consumo de payload POR REFERÊNCIA (ADR-022 §2.3, AOS-272). O
// que estes testes provam não é «o resolvedor funciona»: é que ele NÃO É UM
// BLACKBOARD — o conjunto legível de um nó é o seu contrato, e mais nada.

// stubView é uma [PayloadView] alimentada por um mapa de referências publicadas, mais
// um CONTADOR de chaves pedidas. O contador é o que torna a propriedade
// anti-blackboard verificável: se o resolvedor fosse buscar algo que o nó não
// declarou, apareceria aqui.
type stubView struct {
	refs   map[string]PayloadRef
	asked  []string
	failOn string
}

func (v *stubView) Payload(_ context.Context, _, producer, output string) (PayloadRef, bool, error) {
	k := producer + "/" + output
	v.asked = append(v.asked, k)
	if k == v.failOn {
		return PayloadRef{}, false, errors.New("registo indisponivel")
	}
	r, ok := v.refs[k]
	return r, ok, nil
}

// producerNode é o PRODUTOR tal como o DOCUMENTO APROVADO o declara. É a fonte contra
// a qual [PayloadResolver.Inbox] re-verifica tipo, taint e contract_digest — sem ela, a
// re-verificação era metade de si própria (só comparava a referência com o que o
// CONSUMIDOR pediu, e um taint desclassificado atravessava intacto).
func producerNode() plan.Node {
	return plan.Node{
		NodeID: "src",
		Role:   "produtor",
		Outputs: []plan.Output{
			{Name: "resumo", Type: plan.PayloadSummary},
			{Name: "cobertura", Type: plan.PayloadMetrics},
			{Name: "segredo", Type: plan.PayloadArtifact},
		},
	}
}

// approvedDoc é o documento aprovado com o produtor e o consumidor.
func approvedDoc() plan.PlanDocument {
	return plan.PlanDocument{Nodes: []plan.Node{producerNode(), consumerNode()}}
}

// publishedRef é uma referência bem-formada para (from,output) do tipo dado, com o
// taint e o digest DERIVADOS do contrato — como a emissão real os deriva.
func publishedRef(from, output string, typ plan.PayloadType) PayloadRef {
	contract := plan.Output{Name: output, Type: typ}
	producer := producerNode()
	return PayloadRef{
		From: from, Output: output, Type: typ,
		Taint:          producer.EffectiveOutputTaint(contract),
		ContractDigest: plan.OutputDigest(producer, contract),
		Store:          string(plannerevents.PayloadStoreEventStore),
		Stream:         "run-1", Seq: 7, Digest: "sha256:conteudo",
	}
}

// consumerNode é um nó com dois contratos declarados.
func consumerNode() plan.Node {
	return plan.Node{
		NodeID:    "sink",
		DependsOn: []string{"src"},
		Consumes: []plan.PayloadEdge{
			{From: "src", Output: "resumo", Type: plan.PayloadSummary},
			{From: "src", Output: "cobertura", Type: plan.PayloadMetrics},
		},
	}
}

// TestInboxResolvesOnlyDeclaredContracts é o teste ANTI-BLACKBOARD. O registo tem um
// terceiro payload que o nó NÃO declarou; o resolvedor devolve dois e NUNCA o pede.
//
// FALHA-ANTES: com um estado partilhado, `segredo` era legível por quem soubesse a
// chave — e o organigrama aprovado no gate deixava de descrever quem lê o quê.
func TestInboxResolvesOnlyDeclaredContracts(t *testing.T) {
	view := &stubView{refs: map[string]PayloadRef{
		"src/resumo":    publishedRef("src", "resumo", plan.PayloadSummary),
		"src/cobertura": publishedRef("src", "cobertura", plan.PayloadMetrics),
		"src/segredo":   publishedRef("src", "segredo", plan.PayloadArtifact),
	}}
	r, err := NewPayloadResolver(view, approvedDoc())
	if err != nil {
		t.Fatalf("NewPayloadResolver: %v", err)
	}
	refs, err := r.Inbox(context.Background(), "plan-1", consumerNode())
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("Inbox devolveu %d referências, quer 2", len(refs))
	}
	// ORDEM DECLARADA — determinística (ADR-010).
	if refs[0].Output != "resumo" || refs[1].Output != "cobertura" {
		t.Fatalf("ordem não é a declarada: %q, %q", refs[0].Output, refs[1].Output)
	}
	for _, k := range view.asked {
		if k == "src/segredo" {
			t.Fatalf("o resolvedor pediu um payload que o nó NÃO declarou consumir")
		}
	}
	// A referência não carrega conteúdo — carrega locator + digest.
	if refs[0].Digest == "" || refs[0].Stream == "" {
		t.Fatalf("a referência tem de ser resolúvel E verificável: %+v", refs[0])
	}
}

// TestInboxFailsClosedOnUnpublished — sem referência publicada não há leitura, e não
// há entrega PARCIAL. Um consumidor que recebesse metade dos inputs sem o saber é a
// forma silenciosa de o contrato não valer nada.
func TestInboxFailsClosedOnUnpublished(t *testing.T) {
	view := &stubView{refs: map[string]PayloadRef{
		"src/resumo": publishedRef("src", "resumo", plan.PayloadSummary),
	}}
	r, _ := NewPayloadResolver(view, approvedDoc())
	refs, err := r.Inbox(context.Background(), "plan-1", consumerNode())
	if !errors.Is(err, ErrPayloadNotPublished) {
		t.Fatalf("err = %v, quer ErrPayloadNotPublished", err)
	}
	if refs != nil {
		t.Fatalf("resolução falhada não devia entregar nada, veio %d refs", len(refs))
	}
}

// TestInboxRejectsContractMismatch — defesa-em-profundidade sobre a fronteira: uma
// referência que chegue com OUTRO tipo (registo trocado, wiring errado) morre aqui em
// vez de ser entregue ao consumidor como se fosse o que ele pediu.
func TestInboxRejectsContractMismatch(t *testing.T) {
	bad := publishedRef("src", "resumo", plan.PayloadArtifact) // pedido: summary
	view := &stubView{refs: map[string]PayloadRef{
		"src/resumo":    bad,
		"src/cobertura": publishedRef("src", "cobertura", plan.PayloadMetrics),
	}}
	r, _ := NewPayloadResolver(view, approvedDoc())
	if _, err := r.Inbox(context.Background(), "plan-1", consumerNode()); !errors.Is(err, ErrPayloadContractMismatch) {
		t.Fatalf("err = %v, quer ErrPayloadContractMismatch", err)
	}
}

// TestInboxSurfacesPortErrors — um erro da porta é propagado, nunca engolido.
func TestInboxSurfacesPortErrors(t *testing.T) {
	view := &stubView{refs: map[string]PayloadRef{}, failOn: "src/resumo"}
	r, _ := NewPayloadResolver(view, approvedDoc())
	if _, err := r.Inbox(context.Background(), "plan-1", consumerNode()); err == nil {
		t.Fatalf("erro da porta devia ser propagado")
	}
}

// TestPayloadResolverFailsClosedWithoutPort — sem porta ligada não há resolvedor. A
// alternativa (um resolvedor que devolve o conjunto vazio) falharia em silêncio.
func TestPayloadResolverFailsClosedWithoutPort(t *testing.T) {
	if _, err := NewPayloadResolver(nil, approvedDoc()); !errors.Is(err, ErrPayloadDeps) {
		t.Fatalf("err = %v, quer ErrPayloadDeps", err)
	}
}

// TestInboxEmptyForNodeWithoutContracts — um nó sem `consumes` não toca na porta.
func TestInboxEmptyForNodeWithoutContracts(t *testing.T) {
	view := &stubView{refs: map[string]PayloadRef{"src/resumo": publishedRef("src", "resumo", plan.PayloadSummary)}}
	r, _ := NewPayloadResolver(view, approvedDoc())
	refs, err := r.Inbox(context.Background(), "plan-1", plan.Node{NodeID: "solo"})
	if err != nil || refs != nil {
		t.Fatalf("nó sem contratos: refs=%v err=%v", refs, err)
	}
	if len(view.asked) != 0 {
		t.Fatalf("nó sem contratos não devia interrogar a porta, pediu %v", view.asked)
	}
}

// TestRefFromPublishedPreservesProvenance prova que a PROVENIÊNCIA atravessa a
// projecção — é o que permite ler no log a cadeia por onde um payload untrusted
// contaminou um resumo, em vez de a adivinhar.
func TestRefFromPublishedPreservesProvenance(t *testing.T) {
	fact := plannerevents.PayloadPublishedPayload{
		PlanID: "plan-1", NodeID: "src", Output: "resumo",
		Type: plan.PayloadSummary, Taint: plan.TaintUntrusted,
		ContractDigest: "sha256:contrato",
		Record: plannerevents.PayloadRecordRef{
			Store: plannerevents.PayloadStoreMemory, Stream: "mem-1", Seq: 3, Digest: "sha256:conteudo",
		},
		DerivedFrom: []plannerevents.PayloadOrigin{
			{NodeID: "fetch", Output: "pagina"},
			{NodeID: "extract", Output: "campos"},
		},
	}
	ref := RefFromPublished(fact)
	if ref.From != "src" || ref.Output != "resumo" || ref.Type != plan.PayloadSummary {
		t.Fatalf("projecção do contrato errada: %+v", ref)
	}
	if ref.Store != "mem" || ref.Stream != "mem-1" || ref.Seq != 3 || ref.Digest != "sha256:conteudo" {
		t.Fatalf("locator não atravessou: %+v", ref)
	}
	if len(ref.DerivedFrom) != 2 || ref.DerivedFrom[0].NodeID != "fetch" || ref.DerivedFrom[1].Output != "campos" {
		t.Fatalf("proveniência não atravessou pela ordem: %+v", ref.DerivedFrom)
	}
	if !ref.IsUntrusted() {
		t.Fatalf("o rótulo efectivo tem de atravessar: %+v", ref)
	}
	// Fail-closed pelo tipo: qualquer rótulo que não seja EXACTAMENTE trusted lê-se
	// como untrusted a jusante.
	if (PayloadRef{}).IsUntrusted() != true {
		t.Fatalf("referência vazia devia ler-se untrusted")
	}
}

// TestInboxRejectsDeclassifiedTaint — a re-verificação contra o CONTRATO, não só contra
// o pedido do consumidor. Uma referência que chegue com um rótulo ABAIXO do efectivo
// derivado do documento morre aqui.
//
// FALHA-ANTES (verificada): `Inbox` comparava apenas From/Output/Type; `RefFromPublished`
// copiava o taint tal-qual e `PayloadRef.IsUntrusted()` confiava no campo. Um publicador
// com bug — ou uma [PayloadView] comprometida do outro lado da fronteira — marcava um
// `summary` como trusted e todo o consumo a jusante tratava saída de modelo como
// material que autoriza elevação.
func TestInboxRejectsDeclassifiedTaint(t *testing.T) {
	bad := publishedRef("src", "resumo", plan.PayloadSummary)
	bad.Taint = plan.TaintTrusted // desclassificação
	view := &stubView{refs: map[string]PayloadRef{
		"src/resumo":    bad,
		"src/cobertura": publishedRef("src", "cobertura", plan.PayloadMetrics),
	}}
	r, err := NewPayloadResolver(view, approvedDoc())
	if err != nil {
		t.Fatalf("NewPayloadResolver: %v", err)
	}
	if _, err := r.Inbox(context.Background(), "plan-1", consumerNode()); !errors.Is(err, ErrPayloadContractMismatch) {
		t.Fatalf("err = %v, quer ErrPayloadContractMismatch (a referência não desclassifica)", err)
	}
}

// TestInboxRejectsContractDigestFromAnotherDocument — a AMARRA do `contract_digest`
// passa a ser observável. O digest é recomputado a partir do documento aprovado e
// comparado; um documento editado entre a publicação e o consumo deixa de ser
// indistinguível de um replay honesto.
//
// FALHA-ANTES (verificada): nem o construtor, nem `RefFromPublished`, nem `Inbox`
// invocavam `plan.OutputDigest`. A propriedade que o comentário do campo prometia —
// «no replay, um digest divergente significa que o documento mudou» — não era
// observável por caminho nenhum.
func TestInboxRejectsContractDigestFromAnotherDocument(t *testing.T) {
	stale := publishedRef("src", "resumo", plan.PayloadSummary)
	stale.ContractDigest = "sha256:de-outro-documento"
	view := &stubView{refs: map[string]PayloadRef{
		"src/resumo":    stale,
		"src/cobertura": publishedRef("src", "cobertura", plan.PayloadMetrics),
	}}
	r, _ := NewPayloadResolver(view, approvedDoc())
	if _, err := r.Inbox(context.Background(), "plan-1", consumerNode()); !errors.Is(err, ErrPayloadContractMismatch) {
		t.Fatalf("err = %v, quer ErrPayloadContractMismatch (contract_digest divergente)", err)
	}
}

// TestClosedPayloadContentSurvivesProjection — o conteúdo INLINE de uma forma fechada
// atravessa a projecção do facto para a referência (senão o consumidor recebia um
// rótulo trusted e nada para ler).
func TestClosedPayloadContentSurvivesProjection(t *testing.T) {
	ref := RefFromPublished(plannerevents.PayloadPublishedPayload{
		PlanID: "plan-1", NodeID: "judge", Output: "cobertura",
		Type: plan.PayloadMetrics, Taint: plan.TaintTrusted, ContractDigest: "sha256:c",
		Closed: &plannerevents.ClosedPayload{
			Outcome: plannerevents.VerdictPass,
			Metrics: []plannerevents.VerdictMetric{{Name: "coverage_permille", Value: 874}},
		},
	})
	if ref.Closed == nil || len(ref.Closed.Metrics) != 1 || ref.Closed.Metrics[0].Value != 874 {
		t.Fatalf("o conteúdo fechado não atravessou: %+v", ref.Closed)
	}
	if ref.Closed.Outcome != string(plannerevents.VerdictPass) {
		t.Fatalf("outcome = %q; queria pass", ref.Closed.Outcome)
	}
	if ref.Store != "" || ref.Digest != "" {
		t.Fatalf("uma forma fechada não tem locator: %+v", ref)
	}
}
