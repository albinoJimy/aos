package plannerevents

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/substrate/eventstore"
)

// captureStore capta os EventInput apensos sem tocar num Event Store real: estes
// testes provam o CONTRATO do facto (tipo, step id, schema version), não a
// durabilidade — essa tem os seus testes em recorder_replay_test.go.
type captureStore struct{ appends []eventstore.EventInput }

func (s *captureStore) Append(_ context.Context, _ string, in eventstore.EventInput, _ ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	s.appends = append(s.appends, in)
	return eventstore.AppendResult{Seq: uint64(len(s.appends))}, nil
}

// verdict_test.go — O VEREDICTO TIPADO como FACTO (ADR-022 §2.2, AOS-271, AC3).
// O que se prova: o schema é imposto pelo construtor (não pela boa vontade do
// chamador), o alfabeto é o MESMO da gramática das condições, e não há campo por onde
// o conteúdo untrusted do trabalho verificado entre no Event Store.

// reviewNode é o VERIFICADOR do documento aprovado que emite [goodVerdict]: observa
// `work` e `docs`, que são exactamente os sujeitos que o veredicto declara. É o
// argumento que o construtor passou a exigir — sem ele, `subjects[]` não se amarrava a
// grafo nenhum.
func reviewNode() plan.Node {
	return plan.Node{
		NodeID:    "review",
		Role:      plan.RoleVerifier,
		DependsOn: []string{"work", "docs"},
	}
}

func goodVerdict() VerdictRecordedPayload {
	return VerdictRecordedPayload{
		PlanID:   "plan-1",
		NodeID:   "review",
		Subjects: []string{"work", "docs"},
		Outcome:  VerdictPass,
		Reasons:  []string{"coverage_met", "no_regressions"},
		Metrics:  []VerdictMetric{{Name: "coverage_permille", Value: 874}},
	}
}

// TestVerdictOutcomeSharesAlphabetWithConditionGrammar — o veredicto EMITIDO e o
// veredicto CONSUMIDO falam o mesmo alfabeto POR CONSTRUÇÃO (as constantes derivam de
// [plan.EnumPass]/[plan.EnumFail]).
//
// FALHA-ANTES: com literais independentes nas duas pontas, um renomear numa delas
// produzia um veredicto que nenhum ramo satisfazia — e o modo de falha era o silencioso
// («ausente ⇒ falso»), não um erro.
func TestVerdictOutcomeSharesAlphabetWithConditionGrammar(t *testing.T) {
	if string(VerdictPass) != string(plan.EnumPass) || string(VerdictFail) != string(plan.EnumFail) {
		t.Fatalf("alfabetos divergentes: evento (%q,%q) vs gramática (%q,%q)",
			VerdictPass, VerdictFail, plan.EnumPass, plan.EnumFail)
	}
}

// TestNewVerdictRecordedAccepta — não-vacuidade: um veredicto bem-formado passa e
// PRESERVA a ordem declarada (o facto descreve o que o verificador reportou).
func TestNewVerdictRecordedAccepta(t *testing.T) {
	got, err := NewVerdictRecorded(goodVerdict(), reviewNode())
	if err != nil {
		t.Fatalf("veredicto bem-formado rejeitado: %v", err)
	}
	if got.Subjects[0] != "work" || got.Subjects[1] != "docs" {
		t.Fatalf("ordem dos sujeitos alterada: %v", got.Subjects)
	}
	if got.Reasons[0] != "coverage_met" || got.Metrics[0].Value != 874 {
		t.Fatalf("payload normalizado incorrectamente: %+v", got)
	}
}

// TestNewVerdictRecordedFailsClosed — a tabela de recusas. Cada linha é um modo de
// falha concreto, não uma variação sintáctica.
func TestNewVerdictRecordedFailsClosed(t *testing.T) {
	cases := []struct {
		name  string
		mutar func(*VerdictRecordedPayload)
	}{
		{"plan_id vazio", func(p *VerdictRecordedPayload) { p.PlanID = "" }},
		{"emissor vazio", func(p *VerdictRecordedPayload) { p.NodeID = "" }},
		{"emissor fora da grammar", func(p *VerdictRecordedPayload) { p.NodeID = "revisor com espaços" }},
		{"outcome ausente", func(p *VerdictRecordedPayload) { p.Outcome = "" }},
		{"outcome forjado", func(p *VerdictRecordedPayload) { p.Outcome = "maybe" }},
		{"sem sujeito", func(p *VerdictRecordedPayload) { p.Subjects = nil }},
		{"sujeito repetido", func(p *VerdictRecordedPayload) { p.Subjects = []string{"work", "work"} }},
		{"sujeito fora da grammar", func(p *VerdictRecordedPayload) { p.Subjects = []string{"work\nmais"} }},
		// AUTO-VERIFICAÇÃO no próprio facto: o emissor não é sujeito do seu veredicto.
		{"emissor como sujeito", func(p *VerdictRecordedPayload) { p.Subjects = []string{"work", "review"} }},
		// A razão é um CÓDIGO. Uma FRASE — o vector de vazamento — cai aqui.
		{"razão em prosa", func(p *VerdictRecordedPayload) {
			p.Reasons = []string{"o autor escreveu que a chave secreta e AKIA..."}
		}},
		{"razão repetida", func(p *VerdictRecordedPayload) { p.Reasons = []string{"x", "x"} }},
		{"métrica fora da grammar", func(p *VerdictRecordedPayload) {
			p.Metrics = []VerdictMetric{{Name: "Cobertura Total", Value: 1}}
		}},
		{"métrica repetida", func(p *VerdictRecordedPayload) {
			p.Metrics = []VerdictMetric{{Name: "m", Value: 1}, {Name: "m", Value: 2}}
		}},
		// A AMARRA AO GRAFO (correcção da auditoria da wave): um sujeito que não é
		// aresta de entrada do verificador é trabalho que ele NUNCA OBSERVOU. Antes,
		// bastava a grammar — e o log ficava com um facto que PARECE atribuído.
		{"sujeito que o verificador nao observa", func(p *VerdictRecordedPayload) {
			p.Subjects = []string{"um-no-que-nao-existe"}
		}},
		{"sujeitos acima do tecto", func(p *VerdictRecordedPayload) {
			p.Subjects = make([]string, 0, maxVerdictSubjects+1)
			for i := 0; i <= maxVerdictSubjects; i++ {
				p.Subjects = append(p.Subjects, "n"+strings.Repeat("x", i))
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := goodVerdict()
			tc.mutar(&p)
			if _, err := NewVerdictRecorded(p, reviewNode()); !errors.Is(err, ErrInvalidVerdict) {
				t.Fatalf("esperava ErrInvalidVerdict, veio %v", err)
			}
		})
	}
}

// TestVerdictRequiresTheApprovedVerifierNode — o construtor recusa emitir em nome de
// um nó que o documento não declara como verificador, ou de um nó diferente do
// emissor. Sem isto, a semântica de §2.2 vivia só na admissão e o log aceitava
// veredictos de qualquer coisa.
func TestVerdictRequiresTheApprovedVerifierNode(t *testing.T) {
	naoVerificador := reviewNode()
	naoVerificador.Role = "producer"
	if _, err := NewVerdictRecorded(goodVerdict(), naoVerificador); !errors.Is(err, ErrInvalidVerdict) {
		t.Fatalf("um nó SEM o papel reservado não pode emitir veredicto; veio %v", err)
	}
	outroNo := reviewNode()
	outroNo.NodeID = "outro"
	if _, err := NewVerdictRecorded(goodVerdict(), outroNo); !errors.Is(err, ErrInvalidVerdict) {
		t.Fatalf("o nó fornecido tem de ser o EMISSOR; veio %v", err)
	}
}

// TestNewVerdictRecordedNaoMutaOInput — o construtor copia; um chamador que reutilize
// o seu slice não pode alterar o facto já projectado.
func TestNewVerdictRecordedNaoMutaOInput(t *testing.T) {
	in := goodVerdict()
	out, err := NewVerdictRecorded(in, reviewNode())
	if err != nil {
		t.Fatalf("NewVerdictRecorded: %v", err)
	}
	in.Subjects[0] = "outro"
	if out.Subjects[0] != "work" {
		t.Fatal("o payload projectado partilha o array do input — uma mutação a jusante reescreveria o facto")
	}
}

// TestRecordVerdictEmiteFactoUnicoPorVerificador — o step id é por NÓ (sem
// discriminador de tentativa), pelo que a idempotency_key do Event Store torna o
// veredicto de um verificador um facto ÚNICO: não há caminho por onde um segundo
// `pass` silencioso substitua um `fail` registado.
func TestRecordVerdictEmiteFactoUnicoPorVerificador(t *testing.T) {
	store := &captureStore{}
	r, err := NewRecorder(store)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if _, err := r.RecordVerdict(context.Background(), goodVerdict(), reviewNode()); err != nil {
		t.Fatalf("RecordVerdict: %v", err)
	}
	if len(store.appends) != 1 {
		t.Fatalf("esperava 1 append, veio %d", len(store.appends))
	}
	ev := store.appends[0]
	if ev.Type != EventVerdictRecorded {
		t.Fatalf("tipo = %q; queria %q", ev.Type, EventVerdictRecorded)
	}
	if ev.SchemaVersion != DomainVersion {
		t.Fatalf("schema version = %q; queria %q", ev.SchemaVersion, DomainVersion)
	}
	if ev.StepID != stepVerdictRecorded+":review" {
		t.Fatalf("step id = %q; queria um step POR NÓ", ev.StepID)
	}
	if !knownType(EventVerdictRecorded) {
		t.Fatal("o tipo novo não consta do ciclo de vida canónico — a reconstrução rejeitá-lo-ia fail-closed")
	}
}

// TestRecordVerdictRecusaMalformado — a validação não é opcional: o Recorder passa
// SEMPRE por [NewVerdictRecorded], pelo que um veredicto malformado não apensa nada.
func TestRecordVerdictRecusaMalformado(t *testing.T) {
	store := &captureStore{}
	r, err := NewRecorder(store)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	bad := goodVerdict()
	bad.Outcome = "approved-ish"
	if _, err := r.RecordVerdict(context.Background(), bad, reviewNode()); !errors.Is(err, ErrInvalidVerdict) {
		t.Fatalf("esperava ErrInvalidVerdict, veio %v", err)
	}
	if len(store.appends) != 0 {
		t.Fatalf("um veredicto malformado NÃO pode apensar factos (veio %d)", len(store.appends))
	}
}
