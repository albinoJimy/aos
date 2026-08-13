package plannerevents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// payload_test.go — A REFERÊNCIA DE PAYLOAD como FACTO (ADR-022 §2.3, AOS-272, AC2).
// O que se prova: o facto é uma referência COM PROVENIÊNCIA e SEM conteúdo, o schema é
// imposto pelo construtor (não pela boa vontade do chamador), e o step id por contrato
// torna a publicação um facto único — que é o que separa isto de um blackboard, onde o
// valor de uma chave muda debaixo de quem a lê.

// srcNode e o PRODUTOR do documento APROVADO: declara os dois contratos que estes
// testes publicam. E o argumento que o construtor passou a exigir — `type`, `taint` e
// `contract_digest` sao DERIVADOS dele, nunca aceites do chamador.
func srcNode() plan.Node {
	return plan.Node{
		NodeID: "src",
		Role:   "produtor",
		Outputs: []plan.Output{
			{Name: "resumo", Type: plan.PayloadSummary},
			{Name: "cobertura", Type: plan.PayloadMetrics},
		},
	}
}

// judgeNode e um VERIFICADOR — o unico produtor cujas formas fechadas ficam `trusted`
// (ADR-022 §2.2: o ponto de desclassificacao sancionado).
func judgeNode() plan.Node {
	return plan.Node{
		NodeID:  "judge",
		Role:    plan.RoleVerifier,
		Outputs: []plan.Output{{Name: "cobertura", Type: plan.PayloadMetrics}},
	}
}

// goodPayloadRef e a publicacao de um contrato de forma ABERTA: locator, sem conteudo.
func goodPayloadRef() PayloadPublishedPayload {
	return PayloadPublishedPayload{
		PlanID: "plan-1", NodeID: "src", Output: "resumo",
		Record: PayloadRecordRef{
			Store: PayloadStoreEventStore, Stream: "run-1", Seq: 12, Digest: "sha256:conteudo",
		},
		DerivedFrom: []PayloadOrigin{{NodeID: "fetch", Output: "pagina"}},
	}
}

// closedPayloadRef e a publicacao de um contrato de forma FECHADA: conteudo INLINE
// validado, SEM locator.
func closedPayloadRef() PayloadPublishedPayload {
	return PayloadPublishedPayload{
		PlanID: "plan-1", NodeID: "src", Output: "cobertura",
		Closed: &ClosedPayload{Metrics: []VerdictMetric{{Name: "coverage_permille", Value: 874}}},
	}
}

// TestNewPayloadPublishedAccepta — não-vacuidade, e a ordem da proveniência é
// PRESERVADA (o facto descreve o que o emissor reportou; o replay reproduz-o).
func TestNewPayloadPublishedAccepta(t *testing.T) {
	got, err := NewPayloadPublished(goodPayloadRef(), srcNode())
	if err != nil {
		t.Fatalf("referência bem-formada rejeitada: %v", err)
	}
	if len(got.DerivedFrom) != 1 || got.DerivedFrom[0].NodeID != "fetch" {
		t.Fatalf("proveniência não preservada: %+v", got.DerivedFrom)
	}
	// DERIVADOS do contrato, nunca aceites do chamador.
	if got.Type != plan.PayloadSummary || got.Taint != plan.TaintUntrusted {
		t.Fatalf("tipo/taint deviam ser derivados do contrato: %+v", got)
	}
	if got.ContractDigest != plan.OutputDigest(srcNode(), plan.Output{Name: "resumo", Type: plan.PayloadSummary}) {
		t.Fatalf("contract_digest devia ser derivado do documento aprovado: %q", got.ContractDigest)
	}
}

// TestClosedFormCarriesContentInlineAndNoLocator — A CORRECCAO DO BLOCKER. Uma forma
// fechada carrega o conteudo INLINE (simbolos/codigos/inteiros, validados aqui) e NAO
// tem locator; uma forma aberta e o inverso exacto.
//
// FALHA-ANTES (verificada): o facto validava so metadados — tipo no enum, store/stream/
// digest nao-vazios. O conteudo vivia atras de um locator opaco e NUNCA atravessava
// validador de forma fechada nenhum, pelo que «fechado por construcao» — a unica razao
// pela qual um payload pode ser `trusted` — era uma palavra do documento untrusted.
func TestClosedFormCarriesContentInlineAndNoLocator(t *testing.T) {
	got, err := NewPayloadPublished(closedPayloadRef(), srcNode())
	if err != nil {
		t.Fatalf("forma fechada bem-formada rejeitada: %v", err)
	}
	if got.Closed == nil || len(got.Closed.Metrics) != 1 || got.Closed.Metrics[0].Value != 874 {
		t.Fatalf("o conteudo fechado devia viajar inline: %+v", got.Closed)
	}
	if got.Record != (PayloadRecordRef{}) {
		t.Fatalf("uma forma fechada NAO tem locator: %+v", got.Record)
	}
	// O produtor NAO e verificador ⇒ a forma fechada continua UNTRUSTED (a forma e
	// necessaria; a autoridade e a outra metade).
	if got.Taint != plan.TaintUntrusted {
		t.Fatalf("forma fechada de um no comum devia ser untrusted, veio %q", got.Taint)
	}
	// … e a MESMA publicacao por um VERIFICADOR fica trusted.
	fromJudge := closedPayloadRef()
	fromJudge.NodeID = "judge"
	byJudge, err := NewPayloadPublished(fromJudge, judgeNode())
	if err != nil {
		t.Fatalf("forma fechada de um verificador rejeitada: %v", err)
	}
	if byJudge.Taint != plan.TaintTrusted {
		t.Fatalf("forma fechada de um VERIFICADOR devia ser trusted, veio %q", byJudge.Taint)
	}
}

// TestPublishedRefCannotDeclassify — o construtor DERIVA o rotulo; nao ha campo por
// onde um publicador com bug (ou uma implementacao comprometida do outro lado da
// fronteira) marque um `summary` como trusted.
//
// FALHA-ANTES (verificada): `Taint: plan.TaintTrusted` num `summary` devolvia err=nil.
// A validacao so exigia que o rotulo pertencesse ao enum.
func TestPublishedRefCannotDeclassify(t *testing.T) {
	p := goodPayloadRef()
	p.Taint = plan.TaintTrusted // a tentativa de desclassificacao
	p.Type = plan.PayloadMetrics
	p.ContractDigest = "sha256:inventado"
	got, err := NewPayloadPublished(p, srcNode())
	if err != nil {
		t.Fatalf("NewPayloadPublished: %v", err)
	}
	if got.Taint != plan.TaintUntrusted || got.Type != plan.PayloadSummary {
		t.Fatalf("os campos do chamador venceram a derivacao: %+v", got)
	}
	if got.ContractDigest == "sha256:inventado" {
		t.Fatal("o contract_digest foi ACEITE do chamador — a amarra ao contrato nao existe")
	}
}

// TestPayloadRefHasNoContentField prova ESTRUTURALMENTE a rejeição (c) do ADR: não há
// campo onde o conteúdo do trabalho pudesse viajar. A propriedade é do schema, não da
// disciplina do chamador — por isso mede-se sobre o JSON serializado, que é o que
// chega ao Event Store.
func TestPayloadRefHasNoContentField(t *testing.T) {
	p, err := NewPayloadPublished(goodPayloadRef(), srcNode())
	if err != nil {
		t.Fatalf("NewPayloadPublished: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	allowed := map[string]bool{
		"plan_id": true, "node_id": true, "output": true, "type": true,
		"taint": true, "contract_digest": true, "record": true, "derived_from": true,
		// `closed` so existe nas formas FECHADAS; consta da allowlist porque o schema
		// o admite, e a assercao a seguir prova que numa forma ABERTA nem aparece.
		"closed": true,
	}
	for k := range fields {
		if !allowed[k] {
			t.Fatalf("campo inesperado no facto de referência: %q (o conteúdo NÃO viaja)", k)
		}
	}
	for _, forbidden := range []string{"content", "body", "payload", "text", "summary_text"} {
		if _, ok := fields[forbidden]; ok {
			t.Fatalf("o facto tem um campo de conteúdo (%q) — isso é um blackboard", forbidden)
		}
	}
	// Numa forma ABERTA o campo `closed` nem e serializado: o conteudo do trabalho nao
	// tem por onde viajar.
	if _, ok := fields["closed"]; ok {
		t.Fatal("uma forma ABERTA nao pode trazer conteudo inline")
	}
}

// TestNewPayloadPublishedFailsClosed cobre cada porta do construtor. Fail-closed em
// tudo: sem um facto bem-formado não se emite evento nenhum, e o consumidor a jusante
// fica sem referência (não lê, que é a direcção segura).
func TestNewPayloadPublishedFailsClosed(t *testing.T) {
	tests := []struct {
		name  string
		mutar func(*PayloadPublishedPayload)
		frag  string
	}{
		{"plan_id vazio", func(p *PayloadPublishedPayload) { p.PlanID = "" }, "plan_id"},
		{"node_id fora da grammar", func(p *PayloadPublishedPayload) { p.NodeID = "no com espaços" }, "node_id"},
		{"output fora da grammar", func(p *PayloadPublishedPayload) { p.Output = "Resumo Final" }, "output"},
		{"output que o produtor nao declara", func(p *PayloadPublishedPayload) { p.Output = "fantasma" }, "declara"},
		{"forma aberta com conteudo inline", func(p *PayloadPublishedPayload) {
			p.Closed = &ClosedPayload{Metrics: []VerdictMetric{{Name: "m", Value: 1}}}
		}, "forma aberta"},
		{"store fora do enum", func(p *PayloadPublishedPayload) { p.Record.Store = "http" }, "store"},
		{"sem stream", func(p *PayloadPublishedPayload) { p.Record.Stream = "" }, "stream"},
		{"sem digest do conteúdo", func(p *PayloadPublishedPayload) { p.Record.Digest = "" }, "digest"},
		{"origem fora da grammar", func(p *PayloadPublishedPayload) {
			p.DerivedFrom = []PayloadOrigin{{NodeID: "fetch", Output: "Página Inicial"}}
		}, "grammar"},
		{"proveniência circular", func(p *PayloadPublishedPayload) {
			p.DerivedFrom = []PayloadOrigin{{NodeID: "src", Output: "resumo"}}
		}, "deriva de si mesmo"},
		{"origem repetida", func(p *PayloadPublishedPayload) {
			p.DerivedFrom = []PayloadOrigin{{NodeID: "fetch", Output: "pagina"}, {NodeID: "fetch", Output: "pagina"}}
		}, "repetida"},
		{"proveniência acima do tecto", func(p *PayloadPublishedPayload) {
			o := make([]PayloadOrigin, 0, maxPayloadOrigins+1)
			for i := 0; i <= maxPayloadOrigins; i++ {
				o = append(o, PayloadOrigin{NodeID: "n" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Output: "o"})
			}
			p.DerivedFrom = o
		}, "origens"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := goodPayloadRef()
			tc.mutar(&p)
			_, err := NewPayloadPublished(p, srcNode())
			if !errors.Is(err, ErrInvalidPayloadRef) {
				t.Fatalf("err = %v, quer ErrInvalidPayloadRef", err)
			}
			if !strings.Contains(err.Error(), tc.frag) {
				t.Fatalf("o erro tem de nomear o que está errado; err = %v, quer conter %q", err, tc.frag)
			}
		})
	}
}

// TestRecordPayloadPublishedContract prova o CONTRATO do facto apenso: o tipo é a
// constante do catálogo, o schema version é a do domínio, e o step id é UM POR
// CONTRATO — a idempotency_key do Event Store torna a publicação um facto único e
// imutável.
//
// FALHA-ANTES: sem step id por contrato, um nó publicava duas referências para o mesmo
// output e a segunda substituía a que o consumidor já tinha lido — que é exactamente a
// propriedade do blackboard que ADR-022 §2.3 recusa.
func TestRecordPayloadPublishedContract(t *testing.T) {
	store := &captureStore{}
	rec, err := NewRecorder(store)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if _, err := rec.RecordPayloadPublished(context.Background(), goodPayloadRef(), srcNode()); err != nil {
		t.Fatalf("RecordPayloadPublished: %v", err)
	}
	// Um segundo contrato do MESMO nó tem step id DIFERENTE (coexistem no stream).
	second := closedPayloadRef()
	if _, err := rec.RecordPayloadPublished(context.Background(), second, srcNode()); err != nil {
		t.Fatalf("RecordPayloadPublished (segundo contrato): %v", err)
	}
	if len(store.appends) != 2 {
		t.Fatalf("apensos = %d, quer 2", len(store.appends))
	}
	for _, in := range store.appends {
		if in.Type != EventPayloadPublished {
			t.Fatalf("tipo = %q, quer %q", in.Type, EventPayloadPublished)
		}
		if in.SchemaVersion != DomainVersion {
			t.Fatalf("schema version = %q, quer %q", in.SchemaVersion, DomainVersion)
		}
	}
	if store.appends[0].StepID == store.appends[1].StepID {
		t.Fatalf("dois contratos do mesmo nó partilharam step id %q", store.appends[0].StepID)
	}
	if !strings.HasSuffix(store.appends[0].StepID, ":src:resumo") {
		t.Fatalf("step id devia identificar o contrato, veio %q", store.appends[0].StepID)
	}
	// Repetir o MESMO contrato produz o MESMO step id — a idempotência é do Event
	// Store, e é ela que impede a substituição silenciosa.
	if _, err := rec.RecordPayloadPublished(context.Background(), goodPayloadRef(), srcNode()); err != nil {
		t.Fatalf("RecordPayloadPublished (repetido): %v", err)
	}
	if store.appends[2].StepID != store.appends[0].StepID {
		t.Fatalf("o mesmo contrato devia dar o mesmo step id: %q vs %q", store.appends[2].StepID, store.appends[0].StepID)
	}
}

// TestRecordPayloadPublishedRefusesMalformed — a validação é do construtor, não do
// chamador: um payload malformado não chega ao store.
func TestRecordPayloadPublishedRefusesMalformed(t *testing.T) {
	store := &captureStore{}
	rec, _ := NewRecorder(store)
	bad := goodPayloadRef()
	bad.Record.Digest = ""
	if _, err := rec.RecordPayloadPublished(context.Background(), bad, srcNode()); !errors.Is(err, ErrInvalidPayloadRef) {
		t.Fatalf("err = %v, quer ErrInvalidPayloadRef", err)
	}
	if len(store.appends) != 0 {
		t.Fatalf("nada devia ser apenso, veio %d", len(store.appends))
	}
}

// TestPayloadEventIsCatalogued prova que o tipo novo entra no catálogo do domínio —
// senão a reconstrução (fail-closed) rejeitaria o stream que ele próprio produziu.
func TestPayloadEventIsCatalogued(t *testing.T) {
	if !knownType(EventPayloadPublished) {
		t.Fatalf("%q tem de estar no catálogo do domínio", EventPayloadPublished)
	}
	if !strings.HasPrefix(EventPayloadPublished, "plan.") {
		t.Fatalf("o tipo tem de pertencer à família plan.*, veio %q", EventPayloadPublished)
	}
}
