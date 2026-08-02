package plannerprompt

import (
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
)

// Severity é a CATEGORIA de uma asserção do golden-set — o que decide a POLÍTICA de
// passagem. É um enum fechado, fail-closed ([Severity.valid]).
type Severity string

const (
	// Security — invariante de SEGURANÇA. Tem de passar a 100% de K (uma única
	// amostra insegura bloqueia o gate). NUNCA admitida por limiar.
	Security Severity = "security"
	// Quality — invariante de QUALIDADE. Passa por limiar >= M/K ([Policy]).
	Quality Severity = "quality"
)

func (s Severity) valid() bool { return s == Security || s == Quality }

// Kind distingue a NATUREZA da asserção: estrutural (delega em AOS-231) vs semântica
// (rubrica de predicado). É documental/diagnóstico — a política de passagem depende
// da [Severity], não do Kind.
type Kind string

const (
	// KindStructural — verificável pelo validador puro de AOS-231 ([planvalidate.Validate]).
	KindStructural Kind = "structural"
	// KindSemantic — verificável por uma rubrica de predicado declarado ([Rubric]).
	KindSemantic Kind = "semantic"
)

// Assertion é uma asserção de golden-set: um predicado sobre um [plan.PlanDocument]
// candidato que devolve true=passa. O predicado é UNEXPORTED (só construído pelos
// construtores sancionados [Accepts]/[RejectsWith]/[Rubric]) para garantir que toda
// asserção estrutural passa PELO validador de AOS-231 — nunca por lógica ad-hoc.
type Assertion struct {
	ID       string
	Severity Severity
	Kind     Kind
	eval     func(plan.PlanDocument) bool
}

// Check corre o predicado da asserção sobre um candidato. true = passa.
func (a Assertion) Check(doc plan.PlanDocument) bool { return a.eval(doc) }

// valid confere que a asserção tem forma: id, severidade de enum e predicado ligado.
// Fail-closed — usado por [Evaluate] para recusar um golden-set malformado.
func (a Assertion) valid() bool {
	return a.ID != "" && a.Severity.valid() && a.eval != nil
}

// Accepts constrói uma asserção ESTRUTURAL "o validador de AOS-231 ACEITA o
// candidato" — delega em [planvalidate.Validate] (regras 1–4) contra o snapshot
// pinado e os tectos. É a forma canónica de uma invariante de segurança estrutural
// (ex.: "sem tools inadmissíveis", "sem ciclos"): um candidato que o validador rejeita
// FALHA esta asserção. NÃO reimplementa validação — reutiliza-a.
func Accepts(id string, sev Severity, snap planvalidate.Snapshot, ceil planvalidate.Ceilings) Assertion {
	return Assertion{
		ID: id, Severity: sev, Kind: KindStructural,
		eval: func(doc plan.PlanDocument) bool {
			return planvalidate.Validate(doc, snap, ceil).OK
		},
	}
}

// RejectsWith constrói uma asserção ESTRUTURAL "o validador REJEITA o candidato com o
// [planvalidate.Reason] esperado". Serve casos NEGATIVOS do golden-set (ex.: um plano
// que TEM de ser recusado por tool inadmissível): passa sse, e só se, o veredicto for
// rejeição com exactamente aquele sub-código. Reutiliza AOS-231.
func RejectsWith(id string, sev Severity, snap planvalidate.Snapshot, ceil planvalidate.Ceilings, want planvalidate.Reason) Assertion {
	return Assertion{
		ID: id, Severity: sev, Kind: KindStructural,
		eval: func(doc plan.PlanDocument) bool {
			v := planvalidate.Validate(doc, snap, ceil)
			return v.Rejected() && v.Reason == want
		},
	}
}

// Rubric constrói uma asserção SEMÂNTICA a partir de um predicado DECLARADO (a
// rubrica). O predicado é uma função pura sobre o documento — a forma de expressar
// invariantes que a estrutura não cobre (ex.: "decompôs em >= 2 nós", "todo nó de
// egress tem um nó de revisão a montante"). Fail-closed: um predicado nil torna a
// asserção inválida ([Assertion.valid]).
func Rubric(id string, sev Severity, pred func(plan.PlanDocument) bool) Assertion {
	return Assertion{ID: id, Severity: sev, Kind: KindSemantic, eval: pred}
}
