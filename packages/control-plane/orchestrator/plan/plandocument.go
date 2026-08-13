package plan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// RiskClass é o rótulo de risco SA-ROC de um nó do plano (safe/gray/danger).
//
// ADVISORY (tecnica/18 §3.3 regra 6; AOS-232). O valor que chega no PlanDocument é
// uma PROPOSTA do LLM: só é aceite por AOS-232 se for >= ao piso DERIVADO das
// ferramentas pinadas do nó. Por outras palavras, o rótulo do documento só pode
// ELEVAR o risco derivado — NUNCA baixá-lo. Um efeito irreversível ou egress
// externo sensível força `danger` (não auto-aprovável) independentemente do que o
// LLM proponha. Este pacote apenas valida a FORMA do rótulo (enum fechado); a
// derivação e a regra "só eleva" são enforcement de AOS-232.
type RiskClass string

const (
	// RiskUnset — campo ausente/omitido. Advisory não fornecido; AOS-232 usa o
	// piso derivado. É um valor de enum ADMISSÍVEL (o campo é opcional).
	RiskUnset RiskClass = ""
	// RiskSafe — sem efeito irreversível nem egress sensível (proposta).
	RiskSafe RiskClass = "safe"
	// RiskGray — risco intermédio que exige revisão item-a-item no gate (proposta).
	RiskGray RiskClass = "gray"
	// RiskDanger — efeito irreversível ou egress sensível; nunca auto-aprovável.
	RiskDanger RiskClass = "danger"
)

// Valid indica se rc é um dos valores do enum fechado (incluindo o vazio, que é
// "não fornecido"). Fail-closed: qualquer outro literal (ex.: "critical") é
// rejeitado por [Decode].
func (rc RiskClass) Valid() bool {
	switch rc {
	case RiskUnset, RiskSafe, RiskGray, RiskDanger:
		return true
	default:
		return false
	}
}

// BudgetEstimate é a estimativa de custo de um ramo ou do plano (tokens/$). O
// custo em micro-dólares (inteiro) evita imprecisão de vírgula flutuante e mantém
// o round-trip determinístico. É apenas a estimativa PROPOSTA — AOS-232 re-preça
// cada ramo de forma independente e determinística (tabela AOS-062), não ecoando
// este valor.
type BudgetEstimate struct {
	Tokens       uint64 `json:"tokens"`
	CostMicroUSD uint64 `json:"cost_micro_usd"`
}

// ToolRef é uma ferramenta referida POR REFERÊNCIA PINADA ao REG (tecnica/18
// §3.3): nome + versão + digest. A resolução contra o snapshot de capabilities é
// AOS-231; aqui exige-se apenas que os três campos estejam presentes — uma
// referência incompleta é lixo de schema.
type ToolRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// Node é um nó-papel do organigrama. `depends_on` são as arestas (por node_id); a
// aciclicidade e a resolução de dependências são AOS-231 — aqui valida-se só a
// forma (ids não-vazios, sem duplicados triviais).
type Node struct {
	NodeID         string         `json:"node_id"`
	Role           string         `json:"role"`
	Objective      string         `json:"objective"`
	Tools          []ToolRef      `json:"tools"`
	DependsOn      []string       `json:"depends_on"`
	BudgetEstimate BudgetEstimate `json:"budget_estimate"`
	// RiskClass é ADVISORY (ver [RiskClass]): proposta do LLM que só eleva o piso
	// derivado em AOS-232, nunca o baixa.
	RiskClass RiskClass `json:"risk_class"`
	// ConditionalOn são as arestas CONDICIONAIS de ADR-022 §2.1 (AOS-270): arestas
	// origem→este-nó guardadas por uma expressão do subconjunto fechado
	// (condition.go). OPCIONAL e ADITIVO: a ausência do campo (o caso de todos os
	// planos existentes) significa «sem condições» e deixa a semântica de despacho
	// exactamente como estava — por isso a extensão NÃO consome um MAJOR de
	// `plan_version` (ver [CurrentPlanVersion] e o comentário de compatibilidade em
	// [Decode]). Tal como `depends_on`, a existência da origem, a aciclicidade e a
	// não-sobreposição com `depends_on` são AOS-231; aqui valida-se só a forma.
	ConditionalOn []ConditionalEdge `json:"conditional_on,omitempty"`
}

// PlannerMeta são os metadados de topo do planeamento, pinados para
// reprodutibilidade (§3.6): o modelo usado, a versão do prompt de decomposição
// (artefacto comportamental SemVer, ADR-012) e o hash do snapshot de capabilities
// contra o qual AOS-231 valida.
type PlannerMeta struct {
	Model            string `json:"model"`
	PromptVersion    string `json:"prompt_version"`
	CapabilitiesHash string `json:"capabilities_hash"`
}

// PlanDocument é o contrato declarativo de schema FECHADO entre o LLM e o sistema
// (§3.3). É DADOS *untrusted* (ADR-005): definido e desserializado aqui, validado
// estruturalmente em AOS-231 e orçamentado/risco-derivado em AOS-232 — nunca
// executado.
type PlanDocument struct {
	PlanVersion PlanVersion    `json:"plan_version"`
	Objective   string         `json:"objective"`
	BudgetTotal BudgetEstimate `json:"budget_total"`
	PlannerMeta PlannerMeta    `json:"planner_meta"`
	Nodes       []Node         `json:"nodes"`
}

// Erros de forma do schema. São todos fail-closed: a desserialização recusa o
// documento inteiro, sem correcção silenciosa.
var (
	// ErrTrailingData — há bytes JSON para além do único documento esperado.
	ErrTrailingData = errors.New("plan: dados JSON em excesso apos o documento")
	// ErrNoNodes — o plano não tem nós (nada a materializar).
	ErrNoNodes = errors.New("plan: documento sem nos")
	// ErrMissingObjective — falta o objectivo de topo.
	ErrMissingObjective = errors.New("plan: objective de topo em falta")
	// ErrMissingPlannerMeta — planner_meta incompleto (modelo/prompt/hash).
	ErrMissingPlannerMeta = errors.New("plan: planner_meta incompleto (exige model+prompt_version+capabilities_hash)")
	// ErrDuplicateNodeID — dois nós partilham o mesmo node_id.
	ErrDuplicateNodeID = errors.New("plan: node_id duplicado")
	// ErrEmptyNodeField — um nó tem node_id/role/objective vazio.
	ErrEmptyNodeField = errors.New("plan: no com node_id/role/objective vazio")
	// ErrIncompleteToolRef — uma tool[] não tem name+version+digest completos.
	ErrIncompleteToolRef = errors.New("plan: tool sem name+version+digest")
	// ErrInvalidRiskClass — risk_class fora do enum {safe,gray,danger} (ou vazio).
	ErrInvalidRiskClass = errors.New("plan: risk_class invalido (enum: safe|gray|danger)")
	// ErrInvalidDependsOn — depends_on com id vazio ou duplicado.
	ErrInvalidDependsOn = errors.New("plan: depends_on com id vazio ou duplicado")
)

// Decode desserializa e valida a FORMA de um PlanDocument fail-closed a partir de
// bytes JSON (sem I/O: opera sobre o buffer que recebe). Espelha a disciplina de
// config-loading do nó: [json.Decoder.DisallowUnknownFields] rejeita qualquer
// campo desconhecido — em qualquer nível de aninhamento — em vez de o ignorar.
//
// Não faz validação de GRAFO (aciclicidade, resolução de tools, tectos: AOS-231)
// nem de risco/orçamento (AOS-232): valida apenas que o documento tem a forma, os
// tipos e as cardinalidades do contrato. Um documento cuja forma é inválida é
// recusado por inteiro — nunca corrigido "por baixo da mesa".
//
// COMPATIBILIDADE DE MAJOR — FRONTEIRA DELIBERADA. Decode exige apenas que o
// `plan_version` PARSEIE como SemVer estrito e não seja o sentinela {0,0,0}; NÃO
// rejeita um MAJOR diferente do [CurrentPlanVersion]. A decisão "este materializador
// consegue ler este MAJOR?" (via [PlanVersion.Compatible]) é semântica, não de
// forma, e pertence ao validador de AOS-231 — que invalida o plano e o devolve a
// planeamento numa quebra de MAJOR (§3.6). Manter isto fora de Decode preserva a
// fronteira forma-vs-semântica; não é esquecimento. [CurrentPlanVersion] e
// [PlanVersion.Compatible] são o mecanismo público que AOS-231 consome.
func Decode(data []byte) (PlanDocument, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var doc PlanDocument
	if err := dec.Decode(&doc); err != nil {
		return PlanDocument{}, fmt.Errorf("plan: decode: %w", err)
	}
	// Recusa lixo a seguir ao documento (um único plano por payload).
	if dec.More() {
		return PlanDocument{}, ErrTrailingData
	}
	if err := doc.validateShape(); err != nil {
		return PlanDocument{}, err
	}
	return doc, nil
}

// Encode serializa o documento na forma canónica de wire (plan_version como
// "X.Y.Z"). Estável e determinística — suporta o round-trip.
func Encode(doc PlanDocument) ([]byte, error) {
	return json.Marshal(doc)
}

// validateShape confere as invariantes de FORMA do contrato (schema fechado),
// para além do que o [json.Decoder] já garante em tipos e campos desconhecidos.
// Tudo o que respeite grafo, capabilities, risco derivado e orçamento é
// deliberadamente deixado para AOS-231/232 — este passo não deve overreach.
func (d PlanDocument) validateShape() error {
	if d.PlanVersion.IsZero() {
		// Campo ausente ou "0.0.0": sentinela de não-carimbado, inadmissível.
		return ErrInvalidPlanVersion
	}
	if d.Objective == "" {
		return ErrMissingObjective
	}
	if d.PlannerMeta.Model == "" || d.PlannerMeta.PromptVersion == "" || d.PlannerMeta.CapabilitiesHash == "" {
		return ErrMissingPlannerMeta
	}
	if len(d.Nodes) == 0 {
		return ErrNoNodes
	}
	seen := make(map[string]struct{}, len(d.Nodes))
	for _, n := range d.Nodes {
		if n.NodeID == "" || n.Role == "" || n.Objective == "" {
			return ErrEmptyNodeField
		}
		if _, dup := seen[n.NodeID]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateNodeID, n.NodeID)
		}
		seen[n.NodeID] = struct{}{}
		if !n.RiskClass.Valid() {
			return fmt.Errorf("%w: %q", ErrInvalidRiskClass, n.RiskClass)
		}
		for _, t := range n.Tools {
			if t.Name == "" || t.Version == "" || t.Digest == "" {
				return fmt.Errorf("%w (no %q)", ErrIncompleteToolRef, n.NodeID)
			}
		}
		deps := make(map[string]struct{}, len(n.DependsOn))
		for _, dep := range n.DependsOn {
			if dep == "" {
				return fmt.Errorf("%w (no %q)", ErrInvalidDependsOn, n.NodeID)
			}
			if _, dup := deps[dep]; dup {
				return fmt.Errorf("%w: %q em %q", ErrInvalidDependsOn, dep, n.NodeID)
			}
			deps[dep] = struct{}{}
		}
		// Arestas condicionais (ADR-022 §2.1): forma da gramática do subconjunto
		// fechado. A semântica de grafo (origem existente, ciclo, sobreposição com
		// `depends_on`) fica para AOS-231, como em `depends_on`.
		if err := validateConditional(n.NodeID, n.ConditionalOn); err != nil {
			return err
		}
	}
	return nil
}
