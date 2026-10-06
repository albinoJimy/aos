package plannerevents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-501 — A ORIGEM NO LOG DO PLANO.
//
// Dois factos passam a dizer, sem conteúdo, de onde vem uma saída por referência:
//
//   - `plan.payload_published` ganha `source`, com uma regra SIMÉTRICA imposta na construção:
//     contrato com `from_tool` obriga a `source`; sem ele, `source` é proibido;
//   - `plan.output_source_declared` regista, antes do pedido ao nó, com que vínculo o run de um
//     nó foi pedido.

const (
	aos501DigestDaAncora = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	aos501DigestEntregue = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// aos501Produtor é o nó de leitura, com a origem da saída declarada (ou não).
func aos501Produtor(origem string) plan.Node {
	n := plan.Node{NodeID: "recolha", Role: "searcher", Objective: "o",
		Tools:   []plan.ToolRef{{Name: "doc_read", Version: "1.0.0", Digest: "sha256:d"}},
		Outputs: []plan.Output{{Name: "achados", Type: plan.PayloadRecord, FromTool: origem}}}
	return n
}

// aos501RefPorReferencia é a publicação de uma saída por referência: o digest do que foi
// entregue em `record`, e a âncora e a forma da extracção em `source`.
func aos501RefPorReferencia() PayloadPublishedPayload {
	return PayloadPublishedPayload{
		PlanID: "plan-aos500", NodeID: "recolha", Output: "achados",
		Record: PayloadRecordRef{Store: PayloadStoreEventStore, Stream: "run-aos500~recolha", Digest: aos501DigestEntregue},
		Source: &PayloadSource{StepID: "step-000001-tool-1", AnchorDigest: aos501DigestDaAncora, AnchorBytes: 97,
			Extraction: PayloadExtractionSandboxStdoutText},
	}
}

// TestAOS501_Source_ORegistoQueFicaNoEvento: a forma exacta do evento de uma saída por
// referência. `kind` e `tool` vêm do contrato; o digest publicado é o do entregue.
func TestAOS501_Source_ORegistoQueFicaNoEvento(t *testing.T) {
	p, err := NewPayloadPublished(aos501RefPorReferencia(), aos501Produtor("doc_read"))
	if err != nil {
		t.Fatalf("NewPayloadPublished: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	quer := `"record":{"store":"eventstore","stream":"run-aos500~recolha","digest":"` + aos501DigestEntregue + `"},` +
		`"source":{"kind":"tool_result","tool":"doc_read","step_id":"step-000001-tool-1","anchor_digest":"` + aos501DigestDaAncora + `","anchor_bytes":97,"extraction":"sandbox_stdout_text"}}`
	if !strings.HasSuffix(string(raw), quer) {
		t.Fatalf("o evento de uma saida por referencia nao tem a forma fixada:\n got=%s\nquer o fim=%s", raw, quer)
	}
	if p.Taint != plan.TaintUntrusted {
		t.Fatalf("uma saida por referencia e sempre untrusted; veio %q", p.Taint)
	}
}

// TestAOS501_Source_KindEToolSaoDoContrato: o chamador não escolhe o tipo nem a tool da origem —
// um valor dado à mão é ignorado, e o evento leva os do documento aprovado.
func TestAOS501_Source_KindEToolSaoDoContrato(t *testing.T) {
	ref := aos501RefPorReferencia()
	ref.Source.Kind, ref.Source.Tool = "model_text", "web_post"
	p, err := NewPayloadPublished(ref, aos501Produtor("doc_read"))
	if err != nil {
		t.Fatalf("NewPayloadPublished: %v", err)
	}
	if p.Source.Kind != PayloadSourceToolResult || p.Source.Tool != "doc_read" {
		t.Fatalf("kind e tool tem de vir do contrato (tool_result, doc_read); vieram %q, %q", p.Source.Kind, p.Source.Tool)
	}
	if ref.Source.Tool != "web_post" {
		t.Fatal("o construtor mutou o input")
	}
}

// TestAOS501_Source_ARegraSimetrica: as duas mentiras possíveis são recusadas na construção.
func TestAOS501_Source_ARegraSimetrica(t *testing.T) {
	// Contrato COM origem, publicação SEM `source`: era o texto do modelo sob um contrato que
	// promete o resultado da tool.
	sem := aos501RefPorReferencia()
	sem.Source = nil
	if _, err := NewPayloadPublished(sem, aos501Produtor("doc_read")); !errors.Is(err, ErrInvalidPayloadRef) {
		t.Fatalf("contrato com from_tool e publicacao sem source tinha de ser recusado; veio %v", err)
	}
	// Contrato SEM origem, publicação COM `source`.
	if _, err := NewPayloadPublished(aos501RefPorReferencia(), aos501Produtor("")); !errors.Is(err, ErrInvalidPayloadRef) {
		t.Fatalf("contrato sem from_tool e publicacao com source tinha de ser recusado; veio %v", err)
	}
	// E um contrato sem origem, sem `source`, continua a publicar-se (o evento de sempre).
	sem.Record.Digest = "sha256:conteudo"
	if p, err := NewPayloadPublished(sem, aos501Produtor("")); err != nil || p.Source != nil {
		t.Fatalf("um contrato sem origem publica-se como sempre, sem source; veio %+v, %v", p.Source, err)
	}
}

// TestAOS501_Source_AFormaEValidada: o passo, a âncora e a extracção não são sítios onde caiba
// texto. Cada caso muda UMA coisa numa publicação válida.
func TestAOS501_Source_AFormaEValidada(t *testing.T) {
	for nome, estragar := range map[string]func(*PayloadPublishedPayload){
		"passo vazio":                   func(p *PayloadPublishedPayload) { p.Source.StepID = "" },
		"passo com espaco":              func(p *PayloadPublishedPayload) { p.Source.StepID = "step 1" },
		"passo com quebra de linha":     func(p *PayloadPublishedPayload) { p.Source.StepID = "step\n1" },
		"passo enorme":                  func(p *PayloadPublishedPayload) { p.Source.StepID = strings.Repeat("a", 129) },
		"digest da ancora sem prefixo":  func(p *PayloadPublishedPayload) { p.Source.AnchorDigest = strings.Repeat("1", 64) },
		"digest da ancora curto":        func(p *PayloadPublishedPayload) { p.Source.AnchorDigest = "sha256:1111" },
		"digest da ancora em maiuscula": func(p *PayloadPublishedPayload) { p.Source.AnchorDigest = "sha256:" + strings.Repeat("A", 64) },
		"tamanho negativo":              func(p *PayloadPublishedPayload) { p.Source.AnchorBytes = -1 },
		"extraccao fora do enum":        func(p *PayloadPublishedPayload) { p.Source.Extraction = "resumo" },
		"extraccao vazia":               func(p *PayloadPublishedPayload) { p.Source.Extraction = "" },
		"digest entregue sem forma":     func(p *PayloadPublishedPayload) { p.Record.Digest = "sha256:conteudo" },
	} {
		ref := aos501RefPorReferencia()
		estragar(&ref)
		if _, err := NewPayloadPublished(ref, aos501Produtor("doc_read")); !errors.Is(err, ErrInvalidPayloadRef) {
			t.Errorf("%s: tinha de ser recusado com ErrInvalidPayloadRef; veio %v", nome, err)
		}
	}
}

// TestAOS501_Source_RawExigeODigestDaAncora: «tal como a tool devolveu» só se pode dizer de bytes
// cujo digest É o que o kernel selou.
func TestAOS501_Source_RawExigeODigestDaAncora(t *testing.T) {
	ref := aos501RefPorReferencia()
	ref.Source.Extraction = PayloadExtractionRaw
	if _, err := NewPayloadPublished(ref, aos501Produtor("doc_read")); !errors.Is(err, ErrInvalidPayloadRef) {
		t.Fatalf("extraccao raw com um digest entregue diferente do da ancora tinha de ser recusada; veio %v", err)
	}
	ref.Record.Digest = aos501DigestDaAncora
	if _, err := NewPayloadPublished(ref, aos501Produtor("doc_read")); err != nil {
		t.Fatalf("extraccao raw com o digest da ancora publica-se: %v", err)
	}
}

// TestAOS501_Declaracao_DerivadaDoDocumento: a saída, a tool e o digest do contrato do facto
// `plan.output_source_declared` saem do nó aprovado; do chamador só vem o vínculo.
func TestAOS501_Declaracao_DerivadaDoDocumento(t *testing.T) {
	no := aos501Produtor("doc_read")
	for _, vinculo := range []OutputSourceBinding{OutputSourceBindingBinds, OutputSourceBindingMeasure} {
		p, err := NewOutputSourceDeclared(OutputSourceDeclaredPayload{
			PlanID: "p", NodeID: "recolha", Binding: vinculo, Output: "inventada", Tool: "web_post", ContractDigest: "sha256:mentira",
		}, no)
		if err != nil {
			t.Fatalf("vinculo %q: %v", vinculo, err)
		}
		if p.Output != "achados" || p.Tool != "doc_read" || p.ContractDigest != plan.OutputDigest(no, no.Outputs[0]) || p.Binding != vinculo {
			t.Fatalf("vinculo %q: o facto tem de levar a saida, a tool e o digest do documento, e o vinculo dado: %+v", vinculo, p)
		}
	}
	for nome, c := range map[string]struct {
		p  OutputSourceDeclaredPayload
		no plan.Node
	}{
		"vinculo vazio":           {OutputSourceDeclaredPayload{PlanID: "p", NodeID: "recolha"}, no},
		"vinculo fora do enum":    {OutputSourceDeclaredPayload{PlanID: "p", NodeID: "recolha", Binding: "enforce"}, no},
		"sem plan_id":             {OutputSourceDeclaredPayload{NodeID: "recolha", Binding: OutputSourceBindingBinds}, no},
		"no de outro facto":       {OutputSourceDeclaredPayload{PlanID: "p", NodeID: "outro", Binding: OutputSourceBindingBinds}, no},
		"no sem origem declarada": {OutputSourceDeclaredPayload{PlanID: "p", NodeID: "recolha", Binding: OutputSourceBindingBinds}, aos501Produtor("")},
		"no com duas origens": {OutputSourceDeclaredPayload{PlanID: "p", NodeID: "recolha", Binding: OutputSourceBindingBinds}, plan.Node{NodeID: "recolha",
			Outputs: []plan.Output{{Name: "a", Type: plan.PayloadRecord, FromTool: "doc_read"}, {Name: "b", Type: plan.PayloadRecord, FromTool: "doc_read"}}}},
	} {
		if _, err := NewOutputSourceDeclared(c.p, c.no); !errors.Is(err, ErrInvalidOutputSourceDeclaration) {
			t.Errorf("%s: tinha de ser recusado; veio %v", nome, err)
		}
	}
}

// TestAOS501_Declaracao_UmFactoPorNoEAPrimeiraFica: o passo é um por nó. Uma segunda declaração
// do mesmo nó — com outro vínculo — não substitui a primeira: quem lê o log lê a que ficou.
func TestAOS501_Declaracao_UmFactoPorNoEAPrimeiraFica(t *testing.T) {
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	rec, err := NewRecorder(store)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	no := aos501Produtor("doc_read")
	primeiro, err := rec.RecordOutputSourceDeclared(ctx, OutputSourceDeclaredPayload{PlanID: "plano-501", NodeID: "recolha", Binding: OutputSourceBindingMeasure}, no)
	if err != nil {
		t.Fatalf("primeira declaracao: %v", err)
	}
	segundo, err := rec.RecordOutputSourceDeclared(ctx, OutputSourceDeclaredPayload{PlanID: "plano-501", NodeID: "recolha", Binding: OutputSourceBindingBinds}, no)
	if err != nil || segundo != primeiro {
		t.Fatalf("a segunda declaracao do mesmo no tem de ser o MESMO facto (seq %d); veio seq %d, %v", primeiro, segundo, err)
	}
	seq, err := Reconstruct(ctx, store, "plano-501")
	if err != nil {
		t.Fatalf("Reconstruct: %v — o tipo novo tem de estar no catalogo do dominio", err)
	}
	if len(seq) != 1 || seq[0].Type != EventOutputSourceDeclared || seq[0].StepID != "planstep:output_source_declared:recolha" {
		t.Fatalf("queria um so facto plan.output_source_declared, com o passo do no: %+v", seq)
	}
	var lido OutputSourceDeclaredPayload
	if err := json.Unmarshal(seq[0].Payload, &lido); err != nil || lido.Binding != OutputSourceBindingMeasure {
		t.Fatalf("o facto que fica e o PRIMEIRO (measure); ficou %+v, %v", lido, err)
	}
	if !knownType(EventOutputSourceDeclared) || !strings.HasPrefix(EventOutputSourceDeclared, "plan.") {
		t.Fatal("o tipo novo tem de pertencer ao catalogo e a familia plan.*")
	}
}

// TestAOS501_Source_OTaintDoEventoESempreUntrusted: o `taint` que fica no evento de um payload
// por referência é `untrusted`, diga o documento o que disser. O advisory `trusted` de uma saída
// com origem não a desclassifica: o que se entrega é o que uma tool devolveu — conteúdo de
// terceiros —, e é pelo taint do evento que um leitor do log sabe o que aquela aresta levou.
//
// A revisão adversarial de 2026-10-06 (M6) mostrou que só [plan.Node.EffectiveOutputTaint]
// estava testada; o campo do evento aceitava o advisory sem nenhum teste dar por isso (R05).
func TestAOS501_Source_OTaintDoEventoESempreUntrusted(t *testing.T) {
	for _, advisory := range []plan.PayloadTaint{"", plan.TaintUntrusted, plan.TaintTrusted} {
		for _, tipo := range []plan.PayloadType{plan.PayloadRecord, plan.PayloadArtifact} {
			produtor := aos501Produtor("doc_read")
			produtor.Outputs[0].Taint, produtor.Outputs[0].Type = advisory, tipo
			p, err := NewPayloadPublished(aos501RefPorReferencia(), produtor)
			if err != nil {
				t.Fatalf("advisory %q, tipo %q: NewPayloadPublished: %v", advisory, tipo, err)
			}
			if p.Taint != plan.TaintUntrusted {
				t.Fatalf("advisory %q, tipo %q: o evento de um payload por referencia diz taint=%q; e sempre untrusted", advisory, tipo, p.Taint)
			}
			if p.Source == nil || p.Type != tipo {
				t.Fatalf("advisory %q, tipo %q: pre-condicao: o evento e o de uma saida por referencia desse tipo: %+v", advisory, tipo, p)
			}
		}
	}
	// E o passo: a pergunta exportada é a do construtor.
	for passo, quer := range map[string]bool{"step-000001-tool-1": true, "": false, "passo com espaco": false, "x\ny": false, strings.Repeat("a", 128): true, strings.Repeat("a", 129): false} {
		if ValidSourceStepID(passo) != quer {
			t.Errorf("ValidSourceStepID(%d bytes) = %v; quero %v", len(passo), !quer, quer)
		}
		ref := aos501RefPorReferencia()
		ref.Source.StepID = passo
		if _, err := NewPayloadPublished(ref, aos501Produtor("doc_read")); (err == nil) != quer {
			t.Errorf("o construtor e a pergunta exportada discordam no passo de %d bytes: err=%v", len(passo), err)
		}
	}
}
