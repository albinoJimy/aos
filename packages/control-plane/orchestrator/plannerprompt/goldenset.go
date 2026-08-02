package plannerprompt

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Case é UM caso do golden-set: um objectivo (+ contexto) e as asserções que os K
// candidatos daquele objectivo têm de satisfazer. `Hard` marca um CASO DIFÍCIL —
// protegido contra remoção E contra esvaziamento silencioso das suas asserções
// ([ValidateGoldenMutation]): é a fronteira anti-envenenamento (cegar cobertura
// difícil — retirando o caso ou neutralizando a sua invariante — é o vector de ataque
// ao gate).
type Case struct {
	// ID identifica o objectivo de forma estável (liga o caso às suas amostras/fixtures).
	ID string
	// Objective/Context são o input do caso (o que o prompt decompõe). São dados de
	// teste, não vão a nenhum evento.
	Objective string
	Context   string
	// Hard marca cobertura DIFÍCIL cuja remoção exige aprovação explícita.
	Hard bool
	// Assertions são as invariantes (segurança + qualidade) do caso. >= 1 exigida.
	Assertions []Assertion
}

// GoldenSet é o conjunto VERSIONADO de casos com DONO (DoD: "golden-set versionado
// com dono"). O `Version` é o seu SemVer próprio (governado pelo mesmo pipeline
// ADR-012); o `Owner` é obrigatório — um golden-set sem dono é inadmissível
// (fail-closed em [Evaluate] e [ValidateGoldenMutation]).
type GoldenSet struct {
	Version PromptVersion
	Owner   string
	Cases   []Case
}

// Erros do golden-set — comparáveis por errors.Is.
var (
	// ErrNoOwner — golden-set sem dono (DoD viola-se). Fail-closed.
	ErrNoOwner = errors.New("plannerprompt: golden-set sem dono (owner obrigatorio)")
	// ErrEmptyCase — um caso sem asserções (nada a verificar): vacuidade recusada.
	ErrEmptyCase = errors.New("plannerprompt: caso do golden-set sem assercoes")
	// ErrInvalidAssertion — uma asserção sem forma (id/severidade/predicado).
	ErrInvalidAssertion = errors.New("plannerprompt: assercao invalida no golden-set")
	// ErrDuplicateCaseID — dois casos partilham o mesmo ID (amostras ambíguas).
	ErrDuplicateCaseID = errors.New("plannerprompt: case_id duplicado no golden-set")
	// ErrHardCaseRemoval — remoção de um caso DIFÍCIL sem aprovação explícita
	// (anti-envenenamento). Fail-closed: bloqueia a mutação.
	ErrHardCaseRemoval = errors.New("plannerprompt: remocao de caso dificil sem aprovacao (anti-envenenamento)")
	// ErrGoldenNotBumped — o conjunto de casos mudou mas a versão do golden-set não subiu.
	ErrGoldenNotBumped = errors.New("plannerprompt: golden-set mudou sem bump de versao")
	// ErrHardCaseGutted — um caso DIFÍCIL foi RETIDO (mesmo id) mas a sua assinatura de
	// asserções (id/severidade/natureza) mudou sem aprovação. É o vector de
	// "gutting sem remoção": manter o id e neutralizar a invariante (ex.: trocar uma
	// asserção de SEGURANÇA por uma rubrica de QUALIDADE) cega o caso tão eficazmente
	// como removê-lo. Fail-closed: exige a MESMA aprovação explícita que a remoção.
	ErrHardCaseGutted = errors.New("plannerprompt: assercoes de caso dificil mudadas sem aprovacao (anti-envenenamento)")
)

// assertionSignature é a assinatura ESTÁVEL do conjunto de asserções de um caso: o
// multiset ORDENADO de (severidade, natureza, id) de cada asserção. Captura a
// IDENTIDADE e a SEVERIDADE de cada invariante — logo, trocar uma asserção de
// SEGURANÇA por uma de QUALIDADE, remover/adicionar uma asserção, ou trocar uma
// asserção ESTRUTURAL (delega em AOS-231) por uma rubrica SEMÂNTICA muda a assinatura.
//
// Limite honesto (não coberto): NÃO abrange o CORPO do predicado. Duas rubricas
// SEMÂNTICAS com o mesmo (id, severidade) mas predicados diferentes têm a mesma
// assinatura — o enfraquecimento de um predicado semântico sob o mesmo id/severidade
// não é detectável por assinatura estrutural (closures não são comparáveis). O vector
// CONFIRMADO (Segurança→Qualidade / estrutural→semântica) é coberto porque muda a
// severidade ou a natureza.
func (c Case) assertionSignature() string {
	sigs := make([]string, 0, len(c.Assertions))
	for _, a := range c.Assertions {
		sigs = append(sigs, string(a.Severity)+"|"+string(a.Kind)+"|"+a.ID)
	}
	sort.Strings(sigs)
	return strings.Join(sigs, ";")
}

// validate confere a forma do golden-set: dono presente, ids únicos, cada caso com
// asserções válidas. Puro, fail-closed.
func (g GoldenSet) validate() error {
	if g.Owner == "" {
		return ErrNoOwner
	}
	seen := make(map[string]struct{}, len(g.Cases))
	for _, c := range g.Cases {
		if c.ID == "" {
			return fmt.Errorf("%w: id vazio", ErrDuplicateCaseID)
		}
		if _, dup := seen[c.ID]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateCaseID, c.ID)
		}
		seen[c.ID] = struct{}{}
		if len(c.Assertions) == 0 {
			return fmt.Errorf("%w: %q", ErrEmptyCase, c.ID)
		}
		for _, a := range c.Assertions {
			if !a.valid() {
				return fmt.Errorf("%w: caso %q assercao %q", ErrInvalidAssertion, c.ID, a.ID)
			}
		}
	}
	return nil
}

// RemovalApproval é o SINAL de aprovação anti-envenenamento: um aprovador e o conjunto
// EXPLÍCITO de case_ids cuja MUTAÇÃO de cobertura difícil foi ratificada — quer a
// REMOÇÃO do caso quer o ESVAZIAMENTO das suas asserções ([Case.assertionSignature]).
// Modelado como flag verificado ([ValidateGoldenMutation]) — sem CI real. Mutar um
// caso difícil só é admitido se o seu id constar aqui E houver aprovador. Fail-closed
// por omissão.
type RemovalApproval struct {
	Approver string
	CaseIDs  map[string]bool
}

func (a RemovalApproval) approves(caseID string) bool {
	return a.Approver != "" && a.CaseIDs[caseID]
}

// ValidateGoldenMutation é o GATE ANTI-ENVENENAMENTO da mutação old→new do
// golden-set. Regras (fail-closed):
//
//   - new tem de ter forma ([GoldenSet.validate]);
//   - todo caso HARD presente em old e AUSENTE em new (removido) exige aprovação
//     explícita ([RemovalApproval]) — sem ela, [ErrHardCaseRemoval];
//   - todo caso HARD RETIDO (mesmo id em old e new) cuja ASSINATURA de asserções
//     ([Case.assertionSignature]) mudou exige a MESMA aprovação explícita — sem ela,
//     [ErrHardCaseGutted]. Isto fecha o vector de "gutting sem remoção" (manter o id e
//     neutralizar a invariante), tão perigoso como a remoção;
//   - se o conjunto de casos ou a cobertura difícil mudou, a versão do golden-set tem
//     de subir estritamente ([ErrGoldenNotBumped]).
//
// Cegar cobertura DIFÍCIL — por remoção OU por esvaziamento da asserção — é
// precisamente como se cega um eval-gate; por isso ambos exigem um segundo par de
// olhos. A aprovação [RemovalApproval] cobre as duas formas: o seu conjunto de
// case_ids é o de casos difíceis cuja MUTAÇÃO (remoção ou mudança de asserções) foi
// ratificada. Puro.
func ValidateGoldenMutation(old, new GoldenSet, ap RemovalApproval) error {
	if err := new.validate(); err != nil {
		return err
	}
	newByID := make(map[string]Case, len(new.Cases))
	for _, c := range new.Cases {
		newByID[c.ID] = c
	}
	changed := len(old.Cases) != len(new.Cases)
	for _, oc := range old.Cases {
		nc, kept := newByID[oc.ID]
		if !kept {
			changed = true
			if oc.Hard && !ap.approves(oc.ID) {
				return fmt.Errorf("%w: %q", ErrHardCaseRemoval, oc.ID)
			}
			continue
		}
		// Caso RETIDO por id: se é DIFÍCIL e a assinatura de asserções mudou, é
		// gutting — exige aprovação explícita e conta como mudança (bump).
		if oc.Hard && oc.assertionSignature() != nc.assertionSignature() {
			changed = true
			if !ap.approves(oc.ID) {
				return fmt.Errorf("%w: %q", ErrHardCaseGutted, oc.ID)
			}
		}
	}
	// Detecta também casos NOVOS (ids em new ausentes de old) como mudança.
	if !changed {
		oldIDs := make(map[string]struct{}, len(old.Cases))
		for _, c := range old.Cases {
			oldIDs[c.ID] = struct{}{}
		}
		for _, c := range new.Cases {
			if _, existed := oldIDs[c.ID]; !existed {
				changed = true
				break
			}
		}
	}
	if changed && new.Version.Compare(old.Version) <= 0 {
		return fmt.Errorf("%w: %s -> %s", ErrGoldenNotBumped, old.Version, new.Version)
	}
	return nil
}
