package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// ArtifactKind é a classe de artefacto comportamental sob avaliação. Espelha (por
// VALOR de string) os kinds de [github.com/aos-ref/platform/control-plane/governance/hitl]
// e [github.com/aos-ref/platform/memory/procedural] sem os importar no core — o core
// só depende do módulo folha otel-genai.
type ArtifactKind string

const (
	// ArtifactSkill — uma skill auto-escrita.
	ArtifactSkill ArtifactKind = "skill"
	// ArtifactProceduralMemory — uma memória procedural auto-escrita.
	ArtifactProceduralMemory ArtifactKind = "procedural_memory"
)

// valid reporta se o kind é um dos reconhecidos (fail-closed: vazio/desconhecido não).
func (k ArtifactKind) valid() bool {
	return k == ArtifactSkill || k == ArtifactProceduralMemory
}

// Erros de validação do golden-set (fail-closed, revisáveis).
var (
	// ErrEmptyGoldenSet — o golden-set não tem casos (um set vazio passaria vacuamente).
	ErrEmptyGoldenSet = errors.New("eval: golden-set sem casos (vazio)")
	// ErrInvalidVersion — a versão não é SemVer numérico X.Y.Z.
	ErrInvalidVersion = errors.New("eval: versão de golden-set inválida (esperado X.Y.Z)")
	// ErrInvalidKind — o ArtifactKind não é skill|procedural_memory.
	ErrInvalidKind = errors.New("eval: ArtifactKind inválido (esperado skill|procedural_memory)")
	// ErrInvalidDataset — o Dataset não é golden|failure_derived.
	ErrInvalidDataset = errors.New("eval: Dataset inválido (esperado golden|failure_derived)")
	// ErrDuplicateCaseID — dois casos partilham o mesmo ID (ambiguidade de scoring).
	ErrDuplicateCaseID = errors.New("eval: ID de caso duplicado no golden-set")
	// ErrEmptyCaseID — um caso sem ID (não referenciável/auditável).
	ErrEmptyCaseID = errors.New("eval: caso sem ID")
	// ErrEmptyCaseInput — um caso sem input (nada para conduzir o candidato).
	ErrEmptyCaseInput = errors.New("eval: caso sem input")
	// ErrVacuousCase — um caso sem NENHUMA expectativa (substring/required/forbidden):
	// passaria sempre, não testa nada. Fail-closed.
	ErrVacuousCase = errors.New("eval: caso vácuo (sem expectativa de output nem de acções)")
)

// GoldenCase é um caso curado: um input e o critério de expectativa sobre o
// comportamento observável do candidato (output final + acções). Revisável em PR
// porque é um registo declarativo e estável.
type GoldenCase struct {
	// ID identifica o caso de forma estável (referência de audit/relatório). Único no set.
	ID string `json:"id"`
	// Input é o estímulo entregue ao candidato.
	Input string `json:"input"`
	// ExpectSubstring, se não-vazio, EXIGE que o output final do candidato o CONTENHA.
	ExpectSubstring string `json:"expect_substring,omitempty"`
	// RequiredActions, se não-vazio, EXIGE que TODAS estas acções (tool-calls) ocorram
	// (por presença, não ordem). A ausência de qualquer uma reprova o caso.
	RequiredActions []string `json:"required_actions,omitempty"`
	// ForbiddenActions são acções UNSAFE: a ocorrência de QUALQUER uma marca o caso como
	// unsafe (e reprova-o) — é o sinal de unsafe-action-rate. Fail-closed.
	ForbiddenActions []string `json:"forbidden_actions,omitempty"`
}

// hasExpectation reporta se o caso tem ALGUMA expectativa (não é vácuo).
func (c GoldenCase) hasExpectation() bool {
	return c.ExpectSubstring != "" || len(c.RequiredActions) > 0 || len(c.ForbiddenActions) > 0
}

// GoldenSet é um golden-set curado e VERSIONADO para uma classe de artefacto e um
// tipo de dataset. É a unidade revisável: JSON round-trip byte-estável (ver
// [GoldenSet.CanonicalJSON]) e [GoldenSet.Validate] fail-closed.
type GoldenSet struct {
	// Version é a versão SemVer numérica (X.Y.Z) do golden-set — versiona o CONTRATO de
	// avaliação, não o candidato. Revisável e monótona por convenção.
	Version string `json:"version"`
	// ArtifactKind é a classe de artefacto comportamental que este set avalia.
	ArtifactKind ArtifactKind `json:"artifact_kind"`
	// Dataset distingue o golden curado (regressões NOVAS) do failure_derived
	// (regressões CONHECIDAS). É o [otelgenai.EvalDataset] herdado, não um tipo novo.
	Dataset otelgenai.EvalDataset `json:"dataset"`
	// Cases são os casos curados (não-vazio, IDs únicos).
	Cases []GoldenCase `json:"cases"`
}

// Suite devolve o identificador da suite avaliada (a classe de artefacto). É o
// [otelgenai.EvaluationResult.Suite] emitido no span.
func (gs GoldenSet) Suite() string { return string(gs.ArtifactKind) }

// EvalID devolve um identificador ESTÁVEL e determinista desta suite+versão+dataset,
// usado como [otelgenai.EvaluationResult.EvalID]. Determinismo: sem timestamp/rand.
func (gs GoldenSet) EvalID() string {
	return gs.Suite() + "@" + gs.Version + "/" + string(gs.Dataset)
}

// Validate aplica as invariantes fail-closed do golden-set. Devolve o PRIMEIRO erro
// encontrado (nunca aceita um set mal-formado silenciosamente).
func (gs GoldenSet) Validate() error {
	if _, err := parseSemVer(gs.Version); err != nil {
		return fmt.Errorf("%w: %q", ErrInvalidVersion, gs.Version)
	}
	if !gs.ArtifactKind.valid() {
		return fmt.Errorf("%w: %q", ErrInvalidKind, gs.ArtifactKind)
	}
	if gs.Dataset != otelgenai.EvalDatasetGolden && gs.Dataset != otelgenai.EvalDatasetFailureDerived {
		return fmt.Errorf("%w: %q", ErrInvalidDataset, gs.Dataset)
	}
	if len(gs.Cases) == 0 {
		return ErrEmptyGoldenSet
	}
	seen := make(map[string]struct{}, len(gs.Cases))
	for _, c := range gs.Cases {
		if c.ID == "" {
			return ErrEmptyCaseID
		}
		if _, dup := seen[c.ID]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateCaseID, c.ID)
		}
		seen[c.ID] = struct{}{}
		if c.Input == "" {
			return fmt.Errorf("%w: %q", ErrEmptyCaseInput, c.ID)
		}
		if !c.hasExpectation() {
			return fmt.Errorf("%w: %q", ErrVacuousCase, c.ID)
		}
	}
	return nil
}

// CanonicalJSON serializa o golden-set na forma canónica e byte-estável (indent de 2
// espaços, ordem de campos fixa da struct, terminado por newline). É a forma
// versionada no repo: [LoadGoldenSet] seguida de CanonicalJSON reproduz byte-a-byte o
// artefacto embebido (garantido por teste de round-trip).
func (gs GoldenSet) CanonicalJSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(gs); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// LoadGoldenSet descodifica e VALIDA um golden-set de JSON (fail-closed: JSON
// mal-formado ou inválido devolve erro, nunca um set parcial silencioso). Rejeita
// campos desconhecidos para que um golden-set desactualizado face ao formato não
// passe silenciosamente.
func LoadGoldenSet(data []byte) (GoldenSet, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var gs GoldenSet
	if err := dec.Decode(&gs); err != nil {
		return GoldenSet{}, fmt.Errorf("eval: golden-set JSON inválido: %w", err)
	}
	if err := gs.Validate(); err != nil {
		return GoldenSet{}, err
	}
	return gs, nil
}

// semVer é uma versão numérica X.Y.Z (parsing local; o core não importa domain/schema).
type semVer struct{ major, minor, patch int }

// parseSemVer aceita ESTRITAMENTE "X.Y.Z" com inteiros não-negativos. Fail-closed.
func parseSemVer(s string) (semVer, error) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) != 3 {
		return semVer{}, ErrInvalidVersion
	}
	var out semVer
	for i, p := range parts {
		if p == "" {
			return semVer{}, ErrInvalidVersion
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semVer{}, ErrInvalidVersion
		}
		switch i {
		case 0:
			out.major = n
		case 1:
			out.minor = n
		case 2:
			out.patch = n
		}
	}
	return out, nil
}
