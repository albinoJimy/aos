// Package plannerevents define o domínio de eventos append-only `aos.planner.v1`
// (tecnica/18 §6.1) e a sua reconstrução por replay (ADR-010), assente no Event
// Store (AOS-013) e no motor de replay read-only (AOS-016) EXISTENTES.
//
// AOS-235. O ciclo de vida de um meta-run — intake → planeamento → validação →
// gate → materialização, com desvios de capability-gap e re-planeamento — é
// gravado como uma sequência de factos imutáveis num único stream (o plan_id). O
// documento APROVADO no log é o input do restante run; por isso a reconstrução é
// read-only e byte-a-byte, SEM re-chamar o modelo (§3.4/§6.1).
//
// Três invariantes deste pacote:
//
//  1. Constantes junto do emissor (disciplina do event-catalog, tecnica/13 §3.3):
//     os tipos são CONSTANTES declaradas aqui, nunca literais no caminho de
//     emissão. A família `plan.*` é nova — carece de entrada na taxonomia de
//     tecnica/13 §3.3; ver o report de AOS-235.
//
//  2. Ordem idêntica na reconstrução (ADR-010): [Reconstruct] devolve os factos
//     na MESMA ordem em que foram apensos (a ordem total por-stream do Event
//     Store), preservando os bytes do payload. Nada de re-ordenar por tipo nem de
//     projecção por mapa.
//
//  3. Sem eco de conteúdo sensível/PII em `plan.validation_failed` (§6.1, CA(c)):
//     o payload do evento carrega SÓ metadados classificados (regra violada,
//     código de diagnóstico limitado, tentativa n/N, hash). O detalhe cru do
//     validador — que PODE referenciar o conteúdo untrusted do PlanDocument
//     (ADR-005) — NUNCA entra no evento; é descartado por [NewValidationFailed].
package plannerevents

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// DomainVersion é a versão do domínio de eventos deste pacote. É o `plan_version`
// de schema (tecnica/18 §3.6): as capturas ficam carimbadas e a reconstrução
// rejeita fail-closed um envelope de versão desconhecida (ver [Reconstruct]).
const DomainVersion = "aos.planner.v1"

// Tipos de evento append-only do domínio `aos.planner.v1` (tecnica/18 §6.1).
//
// São a FONTE DE VERDADE do catálogo desta família — declarados como constantes
// junto do emissor ([Recorder]), nunca como literais no caminho de emissão
// (tecnica/13 §3.3; o gate `event-catalog` verifica-o). A ordem de declaração
// espelha a ordem canónica do ciclo de vida (§3.2), que NÃO é alfabética — de
// propósito: uma reconstrução que ordenasse por nome de tipo produziria uma
// sequência diferente e os testes de ordem apanham-na.
const (
	// EventIntakeClassified — o intake classificou o Goal (meta-nível vs. tarefa
	// simples) por heurística declarativa, não por LLM (§3.5).
	EventIntakeClassified = "plan.intake_classified"
	// EventPlannerAdmitted — reserva de planeamento admitida para `agent:planner`.
	EventPlannerAdmitted = "plan.planner_admitted"
	// EventProposed — o PLN propôs um PlanDocument (dados untrusted, ADR-005): só
	// o hash e o planner_meta entram no log, nunca o conteúdo cru.
	EventProposed = "plan.proposed"
	// EventValidationFailed — a validação estrutural fail-closed rejeitou a
	// proposta. SEM eco de conteúdo sensível (§6.1, CA(c)).
	EventValidationFailed = "plan.validation_failed"
	// EventValidated — a proposta passou a validação pura e determinística.
	EventValidated = "plan.validated"
	// EventApproved — o gate (AOS-121) aprovou o plano com decisão assinada.
	EventApproved = "plan.approved"
	// EventRejected — o gate rejeitou o plano.
	EventRejected = "plan.rejected"
	// EventEdited — o cidadão editou o organigrama; segue nova validação.
	EventEdited = "plan.edited"
	// EventMaterialized — o plano APROVADO virou eventos do DAG / spawns delegados.
	EventMaterialized = "plan.materialized"
	// EventBranchDecided — o despachante avaliou as arestas CONDICIONAIS de um nó
	// (ADR-022 §2.1, AOS-270) e fixou o ramo: tomado ou não-tomado. É o facto que
	// torna a decisão REPRODUZÍVEL sem re-avaliação — o replay LÊ o ramo, não o
	// recalcula (ADR-010).
	EventBranchDecided = "plan.branch_decided"
	// EventVerdictRecorded — um nó com o papel RESERVADO [plan.RoleVerifier] emitiu o
	// seu VEREDICTO ESTRUTURADO (ADR-022 §2.2, AOS-271): pass/fail + razões + métricas
	// sobre os nós que examinou. É o ÚNICO tipo de resultado que as condições de
	// qualidade de §2.1 podem consumir, e é um FACTO — não uma conversa.
	EventVerdictRecorded = "plan.verdict_recorded"
	// EventPayloadPublished — um nó publicou um dos seus contratos de saída (ADR-022
	// §2.3, AOS-272). O facto é a REFERÊNCIA ao registo — locator + digest + taint
	// efectivo + proveniência —, NUNCA o conteúdo: é isto que faz do transporte de
	// payload o oposto de um blackboard (rejeição (c) do ADR).
	EventPayloadPublished = "plan.payload_published"
	// EventCapabilityGapOpened — um nó abriu um gap de capacidade (skill em falta).
	EventCapabilityGapOpened = "plan.capability_gap_opened"
	// EventCapabilityGapResolved — o gap foi ratificado e resolvido (AOS-096).
	EventCapabilityGapResolved = "plan.capability_gap_resolved"
	// EventReplanRequested — pedido de re-planeamento de um subgrafo (§4.2).
	EventReplanRequested = "plan.replan_requested"
	// EventReplanApplied — o re-plano foi aplicado ao subgrafo com novo hash.
	EventReplanApplied = "plan.replan_applied"
)

// canonicalLifecycle é a ordem canónica dos tipos do ciclo de vida (§3.2), usada
// só para a auto-verificação de não-vacuidade dos testes e como documentação
// executável. NÃO é imposta à reconstrução — a ordem que [Reconstruct] devolve é
// a ordem REAL de append do stream, não esta.
var canonicalLifecycle = []string{
	EventIntakeClassified,
	EventPlannerAdmitted,
	EventProposed,
	EventValidationFailed,
	EventValidated,
	EventApproved,
	EventRejected,
	EventEdited,
	EventMaterialized,
	EventVerdictRecorded,
	EventPayloadPublished,
	EventBranchDecided,
	EventCapabilityGapOpened,
	EventCapabilityGapResolved,
	EventReplanRequested,
	EventReplanApplied,
}

// knownType indica se t é um tipo do domínio (fail-closed: a reconstrução recusa
// um envelope com um tipo `plan.*` desconhecido em vez de o aceitar em silêncio).
func knownType(t string) bool {
	for _, k := range canonicalLifecycle {
		if k == t {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Payloads — todos SEM PII: só ids opacos, hashes, versões e enums classificados.
// ---------------------------------------------------------------------------

// Classification é o resultado do intake: rota do run (§3.5). Fail-safe na
// direcção da supervisão — em ambiguidade classifica-se `meta`, nunca `simple`.
type Classification string

const (
	// ClassificationMeta — rota de planeamento (PlanDocument + gate).
	ClassificationMeta Classification = "meta"
	// ClassificationSimple — rota directa (tarefa de 1 nó, sem planeamento).
	ClassificationSimple Classification = "simple"
)

func (c Classification) valid() bool {
	return c == ClassificationMeta || c == ClassificationSimple
}

// IntakeClassifiedPayload — corpo de `plan.intake_classified`. `heuristic` é o id
// da heurística declarativa aplicada (§3.5), não texto livre do Goal.
type IntakeClassifiedPayload struct {
	PlanID         string         `json:"plan_id"`
	GoalID         string         `json:"goal_id"`
	Classification Classification `json:"classification"`
	Heuristic      string         `json:"heuristic"`
}

// PlannerAdmittedPayload — corpo de `plan.planner_admitted`: a reserva de
// planeamento admitida para o `agent:planner` (contexto, tabela de preços, factor
// de retry). Sem conteúdo do pedido — só metadados da reserva.
type PlannerAdmittedPayload struct {
	PlanID              string `json:"plan_id"`
	PlannerNHI          string `json:"planner_nhi"`
	PricingTableVersion string `json:"pricing_table_version"`
	RetryFactor         int    `json:"retry_factor"`
	MaxAttempts         int    `json:"max_attempts"`
}

// PlannerMeta pina a proveniência da proposta para reprodutibilidade (§3.6): os
// três carimbos (modelo, versão do prompt, hash das capabilities). São
// identificadores/hashes — nunca o prompt nem a resposta crua.
type PlannerMeta struct {
	Model            string `json:"model"`
	PromptVersion    string `json:"prompt_version"`
	CapabilitiesHash string `json:"capabilities_hash"`
}

// ProposedPayload — corpo de `plan.proposed`. Só o HASH do PlanDocument (dados
// untrusted, ADR-005) e o planner_meta; o documento cru vive fora do log.
type ProposedPayload struct {
	PlanID   string      `json:"plan_id"`
	PlanHash string      `json:"plan_hash"`
	Meta     PlannerMeta `json:"planner_meta"`
	Attempt  int         `json:"attempt"`
}

// Rule é a regra de validação estrutural violada (§3.3, regras 1–6). Enum
// classificado e fail-closed — NUNCA texto livre que pudesse arrastar conteúdo.
type Rule string

const (
	RuleSchema            Rule = "schema"             // campos desconhecidos / tipos
	RuleAcyclicity        Rule = "acyclicity"         // proposta cíclica
	RuleToolResolution    Rule = "tool_resolution"    // tool inexistente/deprecada/fora da allowlist
	RuleStructuralCeiling Rule = "structural_ceiling" // depth/fanout/cardinalidade
	RuleBudget            Rule = "budget"             // budget_total / teto por-ramo
	RuleRisk              Rule = "risk"               // risk_class abaixo do piso derivado
)

func (r Rule) valid() bool {
	switch r {
	case RuleSchema, RuleAcyclicity, RuleToolResolution, RuleStructuralCeiling, RuleBudget, RuleRisk:
		return true
	default:
		return false
	}
}

// ValidationOutcome é o resultado INTERNO do validador. PODE referenciar o
// conteúdo untrusted do PlanDocument (ADR-005) — o literal ofensor, texto cru do
// modelo — em [ValidationOutcome.RawDetail], que serve APENAS logs locais sob o
// gate soberano do nó. NÃO é um evento: nunca chega ao Event Store. A projecção
// para o evento ([NewValidationFailed]) DESCARTA RawDetail.
type ValidationOutcome struct {
	PlanID      string
	PlanHash    string
	Rule        Rule
	Attempt     int
	MaxAttempts int
	// RawDetail é o detalhe cru do validador — POSSIVELMENTE SENSÍVEL/PII. Existe
	// para diagnóstico local; é PROIBIDO ecoá-lo no evento (§6.1, CA(c)).
	RawDetail string
}

// ValidationFailedPayload — corpo de `plan.validation_failed`. Só metadados
// classificados: a regra violada, um código de diagnóstico LIMITADO (derivado da
// regra, content-free), e a tentativa n/N. Sem eco de conteúdo sensível — o campo
// que poderia carregá-lo (RawDetail) não existe aqui, por construção.
type ValidationFailedPayload struct {
	PlanID      string `json:"plan_id"`
	PlanHash    string `json:"plan_hash"`
	Rule        Rule   `json:"rule"`
	Diagnostic  string `json:"diagnostic"`
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"max_attempts"`
}

// ErrUnknownRule é devolvido quando a regra de validação não é classificada —
// fail-closed: sem um diagnóstico limitado conhecido, não se emite o evento (não
// se inventa um código, nem se cai para texto livre que pudesse vazar conteúdo).
var ErrUnknownRule = errors.New("plannerevents: regra de validação desconhecida")

// diagnosticFor mapeia uma regra para um código de diagnóstico LIMITADO e
// content-free. É a única forma do «diagnóstico» de §6.1 — um rótulo estável, não
// uma mensagem. Fail-closed para regras desconhecidas.
func diagnosticFor(r Rule) (string, bool) {
	switch r {
	case RuleSchema:
		return "schema_violation", true
	case RuleAcyclicity:
		return "cycle_detected", true
	case RuleToolResolution:
		return "tool_unresolved", true
	case RuleStructuralCeiling:
		return "ceiling_exceeded", true
	case RuleBudget:
		return "budget_exceeded", true
	case RuleRisk:
		return "risk_floor_violated", true
	default:
		return "", false
	}
}

// NewValidationFailed projecta um [ValidationOutcome] no payload do evento,
// DESCARTANDO RawDetail — só metadados classificados sobrevivem (§6.1, CA(c)).
// Fail-closed: uma regra desconhecida devolve [ErrUnknownRule] e o evento não é
// emitido. Esta é a fronteira que impede o vazamento de PII em
// `plan.validation_failed`; alterá-la para copiar RawDetail parte os testes de
// não-vazamento.
func NewValidationFailed(o ValidationOutcome) (ValidationFailedPayload, error) {
	if !o.Rule.valid() {
		return ValidationFailedPayload{}, fmt.Errorf("%w: %q", ErrUnknownRule, o.Rule)
	}
	diag, ok := diagnosticFor(o.Rule)
	if !ok {
		return ValidationFailedPayload{}, fmt.Errorf("%w: %q", ErrUnknownRule, o.Rule)
	}
	return ValidationFailedPayload{
		PlanID:      o.PlanID,
		PlanHash:    o.PlanHash,
		Rule:        o.Rule,
		Diagnostic:  diag,
		Attempt:     o.Attempt,
		MaxAttempts: o.MaxAttempts,
	}, nil
}

// ValidatedPayload — corpo de `plan.validated`: hash, nº de nós, budget_total e os
// tectos estruturais aplicados (§3.3, regra 4).
type ValidatedPayload struct {
	PlanID      string `json:"plan_id"`
	PlanHash    string `json:"plan_hash"`
	NodeCount   int    `json:"node_count"`
	BudgetTotal int64  `json:"budget_total"`
	MaxDepth    int    `json:"max_depth"`
	MaxFanout   int    `json:"max_fanout"`
	MaxNodes    int    `json:"max_nodes"`
}

// Decision é a decisão do gate de aprovação-de-plano (AOS-121).
type Decision string

const (
	DecisionApproved Decision = "approved"
	DecisionRejected Decision = "rejected"
	DecisionEdited   Decision = "edited"
)

func (d Decision) valid() bool {
	return d == DecisionApproved || d == DecisionRejected || d == DecisionEdited
}

// eventForDecision devolve o tipo de evento correspondente à decisão. Fail-closed.
func eventForDecision(d Decision) (string, bool) {
	switch d {
	case DecisionApproved:
		return EventApproved, true
	case DecisionRejected:
		return EventRejected, true
	case DecisionEdited:
		return EventEdited, true
	default:
		return "", false
	}
}

// DecisionPayload — corpo de `plan.approved`/`rejected`/`edited`. Carrega o hash
// final, a REFERÊNCIA à decisão assinada (hitl.Channel, AOS-121) — não a
// assinatura crua nem PII do decisor — e o hash do diff estrutural da edição.
type DecisionPayload struct {
	PlanID       string   `json:"plan_id"`
	PlanHash     string   `json:"plan_hash"`
	Decision     Decision `json:"decision"`
	DecisionRef  string   `json:"decision_ref"`
	StructDiffID string   `json:"struct_diff_id,omitempty"`
}

// SpawnKind distingue como um nó materializa (§4.1/§6.1).
type SpawnKind string

const (
	// SpawnLeaf — nó-folha: `task.node.created` (AOS-025).
	SpawnLeaf SpawnKind = "leaf"
	// SpawnRole — papel-que-expande: `Delegator.Spawn` (AOS-026).
	SpawnRole SpawnKind = "role"
)

func (k SpawnKind) valid() bool { return k == SpawnLeaf || k == SpawnRole }

// MaterializedNode é o mapeamento de um nó do plano para o DAG. `tools[]` vincula
// o `Authority[]` da NHI filha (issuer_child) — são ids de tool, não credenciais.
type MaterializedNode struct {
	NodeID string    `json:"node_id"`
	Kind   SpawnKind `json:"kind"`
	Tools  []string  `json:"tools,omitempty"`
}

// MaterializedPayload — corpo de `plan.materialized`: o hash aprovado e o mapa
// node_id → materialização. Determinístico a partir do documento gravado (§3.6).
type MaterializedPayload struct {
	PlanID   string             `json:"plan_id"`
	PlanHash string             `json:"plan_hash"`
	Nodes    []MaterializedNode `json:"nodes"`
}

// ---------------------------------------------------------------------------
// VEREDICTO ESTRUTURADO do papel verificador (ADR-022 §2.2, AOS-271).
// ---------------------------------------------------------------------------

// VerdictOutcome é a disposição do veredicto. Enum FECHADO de dois símbolos, e são
// EXACTAMENTE os símbolos da partição de [plan.SubjectVerdict] na gramática das
// condições — não «os mesmos por convenção»: as constantes DERIVAM de
// [plan.EnumPass]/[plan.EnumFail], pelo que o alfabeto que o verificador ESCREVE e o
// alfabeto que a condição LÊ são o mesmo por construção. Não há tabela de tradução
// entre o emissor e o consumidor, logo não há tabela que possa divergir.
//
// O valor-zero (string vazia) NÃO é admissível: um veredicto por preencher não é um
// resultado — [NewVerdictRecorded] recusa-o fail-closed.
type VerdictOutcome string

const (
	// VerdictPass — o trabalho examinado satisfaz o critério do verificador.
	VerdictPass VerdictOutcome = VerdictOutcome(plan.EnumPass)
	// VerdictFail — não satisfaz.
	VerdictFail VerdictOutcome = VerdictOutcome(plan.EnumFail)
)

func (o VerdictOutcome) valid() bool { return o == VerdictPass || o == VerdictFail }

// VerdictMetric é UMA métrica DECLARADA do veredicto: nome do subconjunto fechado
// (ver [NewVerdictRecorded]) e valor INTEIRO.
//
// SEM VÍRGULA FLUTUANTE, como em toda a gramática de condições: uma comparação sobre
// float não é reproduzível byte-a-byte entre plataformas e partiria ADR-010. Uma
// «cobertura de 87.4%» exprime-se como `coverage_permille = 874` — a escala é do
// emissor, o determinismo é do sistema.
type VerdictMetric struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

// Tectos de ARIDADE do veredicto. Não são política do operador: são o limite
// ESTRUTURAL que mantém o facto pequeno, legível no approval-card do gate (ADR-013) e
// barato de reproduzir no replay.
const (
	maxVerdictSubjects = 32
	maxVerdictReasons  = 16
	maxVerdictMetrics  = 32
)

// VerdictRecordedPayload — corpo de `plan.verdict_recorded`: O VEREDICTO TIPADO de
// ADR-022 §2.2, o objecto que o ADR exige («pass/fail + razões + métricas») e que o
// §4 deixou explicitamente por desenhar.
//
// # O SCHEMA, E PORQUE CADA CAMPO TEM A FORMA QUE TEM
//
//   - `node_id` é o VERIFICADOR que emitiu. Não é decorativo: é a chave da atribuição
//     — sem ele, um veredicto é uma opinião anónima e a regra «produtor ≠
//     verificador» (imposta na admissão por `planvalidate`) não teria a que se
//     ancorar no log.
//   - `subjects` são os nós EXAMINADOS, pela ordem declarada. É o SUJEITO do
//     veredicto: o que torna o facto auditável («este pass é sobre quê?») e o que
//     permite a um revisor ligar, no stream, o veredicto ao trabalho.
//   - `outcome` é o símbolo que a condição consome ([VerdictOutcome]).
//   - `reasons` são CÓDIGOS, nunca frases. A distinção é a substância deste payload,
//     não uma preferência de estilo: o verificador olha para trabalho UNTRUSTED
//     (ADR-005), e se a sua razão pudesse ser texto livre, o veredicto seria o canal
//     por onde o conteúdo desse trabalho — prosa do modelo, excertos, PII — entrava
//     no Event Store por baixo da mesa. Mesmo raciocínio que faz
//     [NewValidationFailed] descartar o RawDetail.
//   - `metrics` são pares (nome do subconjunto fechado, INTEIRO) — o material dos
//     predicados `metric` de §2.1.
//
// CONTENT-FREE como todo o domínio: ids estruturais, símbolos de enum fechado,
// códigos de identificador e inteiros. Não há campo que possa carregar conteúdo do
// trabalho verificado — por construção, não por disciplina do chamador.
type VerdictRecordedPayload struct {
	PlanID   string          `json:"plan_id"`
	NodeID   string          `json:"node_id"`
	Subjects []string        `json:"subjects"`
	Outcome  VerdictOutcome  `json:"outcome"`
	Reasons  []string        `json:"reasons,omitempty"`
	Metrics  []VerdictMetric `json:"metrics,omitempty"`
}

// ErrInvalidVerdict é devolvido quando o veredicto proposto não conforma ao schema
// tipado — fail-closed: sem um facto bem-formado não se emite evento nenhum, e o
// ramo de qualidade a jusante fica sem observável (não despacha, que é a direcção
// segura).
var ErrInvalidVerdict = errors.New("plannerevents: veredicto estruturado inválido")

// NewVerdictRecorded valida e normaliza o veredicto tipado, devolvendo o payload
// pronto a apensar. É a FRONTEIRA que impede um veredicto malformado — ou um que
// tente veicular conteúdo, ou atribuir-se a trabalho que não observou — de entrar no
// log. Fail-closed em tudo:
//
//	plan_id/node_id      não-vazios e conformes a [plan.ValidNodeID];
//	verifier             o NÓ DO DOCUMENTO APROVADO que emite: tem de ser o mesmo
//	                     node_id e ter o papel RESERVADO [plan.RoleVerifier];
//	subjects             1..[maxVerdictSubjects], cada um [plan.ValidNodeID], sem
//	                     duplicados, NUNCA o próprio emissor, e — a amarra ao grafo —
//	                     cada um TEM de ser uma ARESTA DE ENTRADA do verificador;
//	outcome              do enum fechado;
//	reasons              0..[maxVerdictReasons] CÓDIGOS de [plan.ValidIdentifier],
//	                     sem duplicados;
//	metrics              0..[maxVerdictMetrics], nomes de [plan.ValidIdentifier],
//	                     sem nome repetido.
//
// # PORQUE O CONSTRUTOR PASSOU A EXIGIR O NÓ (correcção da auditoria da wave)
//
// Os `subjects[]` eram validados só quanto à grammar: um veredicto sobre
// «um-no-que-nao-existe» — ou sobre um nó real que o verificador nunca observou — era
// ACEITE, e o log ficava com um facto que PARECE atribuído e não está ligado a nada. A
// atribuição que a admissão constrói (`planvalidate`: o verificador tem de ser
// descendente do trabalho que liberta) era decorativa em runtime.
//
// Exigir o [plan.Node] do documento APROVADO fecha isso sem inventar porta nenhuma: o
// chamador que emite um veredicto TEM o documento (é dele que sabe que o nó é um
// verificador), e o `plan_hash` já viaja pelo mesmo caminho. A noção de «aresta de
// entrada» é a MESMA do validador ([plan.Node.IncomingEdges]) — uma definição, não
// duas que possam divergir.
//
// Determinística: PRESERVA a ordem declarada (não ordena) — o facto descreve o que o
// verificador reportou, e o replay reproduz-o byte-a-byte. Não muta o input: os
// slices são copiados.
func NewVerdictRecorded(p VerdictRecordedPayload, verifier plan.Node) (VerdictRecordedPayload, error) {
	if p.PlanID == "" {
		return VerdictRecordedPayload{}, fmt.Errorf("%w: plan_id vazio", ErrInvalidVerdict)
	}
	if !plan.ValidNodeID(p.NodeID) {
		return VerdictRecordedPayload{}, fmt.Errorf("%w: node_id do verificador fora da grammar", ErrInvalidVerdict)
	}
	if verifier.NodeID != p.NodeID {
		return VerdictRecordedPayload{}, fmt.Errorf("%w: o nó fornecido (%q) não é o emissor (%q)", ErrInvalidVerdict, verifier.NodeID, p.NodeID)
	}
	if !verifier.IsVerifier() {
		return VerdictRecordedPayload{}, fmt.Errorf("%w: o nó %q não declara o papel reservado %q", ErrInvalidVerdict, p.NodeID, plan.RoleVerifier)
	}
	if !p.Outcome.valid() {
		return VerdictRecordedPayload{}, fmt.Errorf("%w: outcome %q fora do enum {pass,fail}", ErrInvalidVerdict, p.Outcome)
	}
	if len(p.Subjects) == 0 || len(p.Subjects) > maxVerdictSubjects {
		return VerdictRecordedPayload{}, fmt.Errorf("%w: %d sujeitos (esperado 1..%d)", ErrInvalidVerdict, len(p.Subjects), maxVerdictSubjects)
	}
	observed := verifier.IncomingEdges()
	subjects := make([]string, 0, len(p.Subjects))
	seenSubj := make(map[string]struct{}, len(p.Subjects))
	for _, s := range p.Subjects {
		if !plan.ValidNodeID(s) {
			return VerdictRecordedPayload{}, fmt.Errorf("%w: sujeito fora da grammar de node_id", ErrInvalidVerdict)
		}
		if s == p.NodeID {
			return VerdictRecordedPayload{}, fmt.Errorf("%w: o verificador %q é sujeito do seu próprio veredicto", ErrInvalidVerdict, p.NodeID)
		}
		if _, dup := seenSubj[s]; dup {
			return VerdictRecordedPayload{}, fmt.Errorf("%w: sujeito %q repetido", ErrInvalidVerdict, s)
		}
		if !containsID(observed, s) {
			return VerdictRecordedPayload{}, fmt.Errorf("%w: o sujeito %q não é aresta de entrada do verificador %q (veredicto sobre trabalho que não observou)", ErrInvalidVerdict, s, p.NodeID)
		}
		seenSubj[s] = struct{}{}
		subjects = append(subjects, s)
	}
	if len(p.Reasons) > maxVerdictReasons {
		return VerdictRecordedPayload{}, fmt.Errorf("%w: %d razões > %d", ErrInvalidVerdict, len(p.Reasons), maxVerdictReasons)
	}
	var reasons []string
	if len(p.Reasons) > 0 {
		reasons = make([]string, 0, len(p.Reasons))
		seen := make(map[string]struct{}, len(p.Reasons))
		for _, r := range p.Reasons {
			if !plan.ValidIdentifier(r) {
				// Uma razão é um CÓDIGO. Uma frase — com espaços, maiúsculas ou
				// pontuação — cai aqui, que é o ponto do campo.
				return VerdictRecordedPayload{}, fmt.Errorf("%w: razão fora da grammar de código", ErrInvalidVerdict)
			}
			if _, dup := seen[r]; dup {
				return VerdictRecordedPayload{}, fmt.Errorf("%w: razão %q repetida", ErrInvalidVerdict, r)
			}
			seen[r] = struct{}{}
			reasons = append(reasons, r)
		}
	}
	if len(p.Metrics) > maxVerdictMetrics {
		return VerdictRecordedPayload{}, fmt.Errorf("%w: %d métricas > %d", ErrInvalidVerdict, len(p.Metrics), maxVerdictMetrics)
	}
	var metrics []VerdictMetric
	if len(p.Metrics) > 0 {
		metrics = make([]VerdictMetric, 0, len(p.Metrics))
		seen := make(map[string]struct{}, len(p.Metrics))
		for _, m := range p.Metrics {
			if !plan.ValidIdentifier(m.Name) {
				return VerdictRecordedPayload{}, fmt.Errorf("%w: nome de métrica fora da grammar", ErrInvalidVerdict)
			}
			if _, dup := seen[m.Name]; dup {
				return VerdictRecordedPayload{}, fmt.Errorf("%w: métrica %q repetida", ErrInvalidVerdict, m.Name)
			}
			seen[m.Name] = struct{}{}
			metrics = append(metrics, m)
		}
	}
	return VerdictRecordedPayload{
		PlanID:   p.PlanID,
		NodeID:   p.NodeID,
		Subjects: subjects,
		Outcome:  p.Outcome,
		Reasons:  reasons,
		Metrics:  metrics,
	}, nil
}

// containsID indica se id consta da lista. Linear e sem alocação: as listas de
// arestas são curtas por tecto estrutural, e uma varredura linear evita um mapa cuja
// ordem de iteração nunca é a do documento.
func containsID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// REFERÊNCIA DE PAYLOAD publicada por um nó (ADR-022 §2.3, AOS-272).
// ---------------------------------------------------------------------------

// PayloadStore é o REGISTO onde o payload ficou apenso. Enum FECHADO de dois: os dois
// registos que ADR-022 §2.3 nomeia («referência a registo no Event Store/MEM»).
//
// É o registo, e não uma URL: uma referência que pudesse apontar para qualquer sítio
// seria um canal de egress com nome de contrato. O valor-zero (vazio) NÃO é
// admissível.
type PayloadStore string

const (
	// PayloadStoreEventStore — o registo append-only do run (`substrate/eventstore`).
	PayloadStoreEventStore PayloadStore = "eventstore"
	// PayloadStoreMemory — a memória governada, com quarentena e proveniência
	// (`platform/memory`).
	PayloadStoreMemory PayloadStore = "mem"
)

func (s PayloadStore) valid() bool {
	return s == PayloadStoreEventStore || s == PayloadStoreMemory
}

// PayloadRecordRef é A REFERÊNCIA — o locator do registo onde o payload vive, e o
// digest do que lá está. NÃO tem campo de conteúdo, e a ausência é o ponto: um payload
// só se lê indo ao registo, sob a governação desse registo (quarentena da MEM, TTL,
// erasure), e não copiando-o para dentro de um facto do plano.
//
// O DIGEST é o que torna a referência VERIFICÁVEL: quem a resolve confere que o que
// leu é o que foi publicado. Sem ele, uma referência a um registo mutável seria um
// blackboard com um passo de indirecção — exactamente a rejeição (c) do ADR.
type PayloadRecordRef struct {
	// Store é o registo (enum fechado).
	Store PayloadStore `json:"store"`
	// Stream é o id do stream/colecção dentro do registo.
	Stream string `json:"stream"`
	// Seq é a posição na ordem total do stream (0 quando o registo não a define).
	Seq uint64 `json:"seq,omitempty"`
	// Digest é o carimbo do conteúdo referido (`sha256:<hex>` ou equivalente do
	// registo). Obrigatório.
	Digest string `json:"digest"`
}

// PayloadOrigin é UMA origem de proveniência: o nó e o contrato de que este payload
// deriva. Ids estruturais, nunca conteúdo.
//
// É esta lista que faz a PROVENIÊNCIA sobreviver à derivação, como o `taint.Value`
// canónico de ADR-005 faz com as suas `origins` — se um payload untrusted contaminar
// uma cadeia de resumos, a cadeia lê-se no log em vez de se adivinhar.
type PayloadOrigin struct {
	NodeID string `json:"node_id"`
	Output string `json:"output"`
}

// maxPayloadOrigins limita a proveniência declarada de um payload — tecto ESTRUTURAL,
// como os do veredicto: mantém o facto pequeno e barato de reproduzir.
const maxPayloadOrigins = 32

// ClosedPayload é O CONTEÚDO de um payload de FORMA FECHADA ([plan.PayloadType.ClosedForm]),
// transportado INLINE no facto e validado pelo construtor.
//
// # PORQUE O CONTEÚDO FECHADO VIAJA, QUANDO O ABERTO NÃO VIAJA
//
// Não é uma excepção à rejeição (c) do ADR — é o que a torna VERDADEIRA. O rótulo
// `trusted` de uma forma fechada assenta na afirmação «não há por onde o trabalho
// entrar». Enquanto o facto carregava apenas um LOCATOR opaco, essa afirmação era uma
// promessa do documento: um nó declarava `type: metrics` e publicava uma referência a
// prosa, e o consumidor privilegiado recebia-a com rótulo trusted. A auditoria
// adversarial da wave demonstrou-o num plano ADMITIDO.
//
// Carregando o conteúdo INLINE, a forma deixa de ser uma promessa e passa a ser
// verificada onde tem de ser — na FRONTEIRA DE EMISSÃO, pelo mesmo construtor que
// valida o veredicto: símbolos de enum, códigos de [plan.ValidIdentifier] e INTEIROS.
// Não existe campo onde prosa caiba. E porque o conteúdo está no facto, um payload
// fechado NÃO TEM locator ([PayloadRecordRef] vazio): não há segundo sítio de onde o
// ler, logo não há sítio nenhum que possa divergir do que foi validado.
//
// A simetria é a regra inteira, e é imposta pelo construtor:
//
//	forma FECHADA (metrics/verdict)  ⇒ `closed` OBRIGATÓRIO, `record` PROIBIDO;
//	forma ABERTA (summary/…)         ⇒ `record` OBRIGATÓRIO, `closed` PROIBIDO.
//
// Os campos são os do veredicto de §2.2 — deliberadamente os mesmos, e não uma segunda
// taxonomia: `verdict` transporta os três, `metrics` só as métricas.
type ClosedPayload struct {
	// Outcome é a disposição — SÓ para `type: verdict` (vazio em `metrics`).
	Outcome VerdictOutcome `json:"outcome,omitempty"`
	// Reasons são CÓDIGOS, nunca frases — SÓ para `type: verdict`.
	Reasons []string `json:"reasons,omitempty"`
	// Metrics são pares (nome de charset fechado, INTEIRO). Obrigatórias em `metrics`.
	Metrics []VerdictMetric `json:"metrics,omitempty"`
}

// PayloadPublishedPayload — corpo de `plan.payload_published`: A REFERÊNCIA a um
// contrato de saída que um nó cumpriu.
//
// # O SCHEMA, E PORQUE NÃO TEM CONTEÚDO
//
// ADR-022 §2.3 rejeita o blackboard e prescreve o que fica no lugar: «referência a
// registo no Event Store/MEM com proveniência, respeitando “contexto ≠ registo” — o
// consumidor recebe resumo/referência, não o histórico bruto». Este payload é
// literalmente isso, e a propriedade é ESTRUTURAL, não de disciplina do chamador: não
// existe campo onde o conteúdo pudesse ir. Um consumidor que queira o material vai ao
// registo com a referência, e o registo é que governa o acesso.
//
//   - `node_id`/`output` identificam o CONTRATO cumprido — a chave da atribuição, e o
//     par que o consumidor declarou em `consumes`.
//   - `type`/`taint` são o contrato RESOLVIDO: o schema declarado e o taint EFECTIVO
//     (piso derivado do tipo, elevado — nunca baixado — pelo advisory do documento).
//     Viajam no facto para que o consumidor não tenha de reabrir o PlanDocument para
//     saber o que está a receber.
//   - `contract_digest` é [plan.OutputDigest] do contrato: AMARRA a referência ao
//     contrato exacto que a autorizou. Um digest divergente no replay significa
//     documento editado — e um plano editado não é um replay.
//   - `record` é o locator + digest ([PayloadRecordRef]) — SÓ para formas ABERTAS.
//   - `closed` é o CONTEÚDO inline — SÓ para formas FECHADAS (ver [ClosedPayload]).
//   - `derived_from` é a PROVENIÊNCIA: os contratos de que este payload deriva.
//
// CONTENT-FREE onde tem de ser: nas formas abertas não existe campo onde o trabalho
// caiba; nas fechadas o único conteúdo admissível são símbolos, códigos e inteiros,
// validados pelo construtor. `type`, `taint` e `contract_digest` NÃO são aceites do
// chamador — são DERIVADOS do contrato do documento aprovado ([NewPayloadPublished]).
type PayloadPublishedPayload struct {
	PlanID         string            `json:"plan_id"`
	NodeID         string            `json:"node_id"`
	Output         string            `json:"output"`
	Type           plan.PayloadType  `json:"type"`
	Taint          plan.PayloadTaint `json:"taint"`
	ContractDigest string            `json:"contract_digest"`
	// Record fica no ZERO nas formas fechadas (encoding/json não omite structs; a
	// ausência lê-se pelos campos vazios, e o construtor garante-a).
	Record      PayloadRecordRef `json:"record"`
	Closed      *ClosedPayload   `json:"closed,omitempty"`
	DerivedFrom []PayloadOrigin  `json:"derived_from,omitempty"`
}

// ErrInvalidPayloadRef é devolvido quando a referência proposta não conforma ao
// contrato — fail-closed: sem um facto bem-formado não se emite evento nenhum, e o
// consumidor a jusante fica sem referência (não lê, que é a direcção segura).
var ErrInvalidPayloadRef = errors.New("plannerevents: referencia de payload invalida")

// NewPayloadPublished valida e normaliza a referência contra o CONTRATO do documento
// APROVADO, devolvendo o payload pronto a apensar. É a FRONTEIRA que impede uma
// referência malformada — ou uma que tente veicular conteúdo por um campo que não é o
// seu, ou desclassificar-se — de entrar no log.
//
// # O QUE É DERIVADO E O QUE É ACEITE (a correcção da auditoria da wave)
//
// `type`, `taint` e `contract_digest` NÃO são aceites do chamador: são DERIVADOS do
// [plan.Output] que o `producer` declara no documento aprovado. A versão anterior
// aceitava os três, e as consequências eram exactamente as que se esperam de um campo
// aceite sem fonte: um `summary` publicado com `taint: trusted` era admitido (o
// construtor só verificava que o rótulo pertencia ao enum), e o `contract_digest` — a
// amarra que promete detectar um documento editado — era uma string qualquer que
// ninguém comparava com nada.
//
// Fail-closed em tudo:
//
//	plan_id/node_id   não-vazios, conformes a [plan.ValidNodeID], e o node_id É o do
//	                  `producer` (o facto atribui-se a quem o documento declara);
//	output            [plan.ValidIdentifier] E declarado pelo producer;
//	type/taint/digest DERIVADOS do contrato (ver acima);
//	forma FECHADA     `closed` obrigatório e válido (símbolos/códigos/inteiros),
//	                  `record` PROIBIDO — não há locator opaco para uma forma fechada;
//	forma ABERTA      `record` obrigatório (store do enum, stream e digest não-vazios),
//	                  `closed` PROIBIDO — o conteúdo do trabalho não viaja;
//	derived_from      0..[maxPayloadOrigins], node_ids/outputs conformes, sem
//	                  duplicados e NUNCA o próprio contrato emitido (um payload não
//	                  deriva de si mesmo — seria uma proveniência circular no log).
//
// Determinística: PRESERVA a ordem declarada da proveniência e do conteúdo fechado
// (não ordena) — o facto descreve o que o emissor reportou, e o replay reproduz-o
// byte-a-byte. Não muta o input: os slices são copiados.
func NewPayloadPublished(p PayloadPublishedPayload, producer plan.Node) (PayloadPublishedPayload, error) {
	if p.PlanID == "" {
		return PayloadPublishedPayload{}, fmt.Errorf("%w: plan_id vazio", ErrInvalidPayloadRef)
	}
	if !plan.ValidNodeID(p.NodeID) {
		return PayloadPublishedPayload{}, fmt.Errorf("%w: node_id do produtor fora da grammar", ErrInvalidPayloadRef)
	}
	if producer.NodeID != p.NodeID {
		return PayloadPublishedPayload{}, fmt.Errorf("%w: o nó fornecido (%q) não é o produtor (%q)", ErrInvalidPayloadRef, producer.NodeID, p.NodeID)
	}
	if !plan.ValidIdentifier(p.Output) {
		return PayloadPublishedPayload{}, fmt.Errorf("%w: nome de output fora da grammar", ErrInvalidPayloadRef)
	}
	contract, ok := producer.FindOutput(p.Output)
	if !ok {
		return PayloadPublishedPayload{}, fmt.Errorf("%w: o nó %q não declara o output %q no documento aprovado", ErrInvalidPayloadRef, p.NodeID, p.Output)
	}
	if !contract.Type.Valid() {
		return PayloadPublishedPayload{}, fmt.Errorf("%w: tipo %q fora do enum", ErrInvalidPayloadRef, contract.Type)
	}
	// DERIVADOS — nunca aceites. O rótulo é o EFECTIVO do produtor (forma fechada E
	// papel verificador), e o digest fecha sobre esse rótulo: um documento editado
	// entre a publicação e o consumo produz um digest diferente, que é o que torna a
	// amarra observável a jusante ([plandispatch.PayloadResolver.Inbox]).
	typ := contract.Type
	taint := producer.EffectiveOutputTaint(contract)
	digest := plan.OutputDigest(producer, contract)

	// SIMETRIA forma-fechada/forma-aberta. Ver [ClosedPayload] para o argumento: é
	// esta simetria que faz de «fechado por construção» uma propriedade imposta na
	// emissão em vez de uma palavra do documento untrusted.
	closed, err := normalizeClosed(typ, p.Closed)
	if err != nil {
		return PayloadPublishedPayload{}, err
	}
	if typ.ClosedForm() {
		if p.Record != (PayloadRecordRef{}) {
			return PayloadPublishedPayload{}, fmt.Errorf("%w: um payload de forma fechada (%q) NÃO tem locator — o conteúdo viaja inline e validado", ErrInvalidPayloadRef, typ)
		}
	} else {
		if !p.Record.Store.valid() {
			return PayloadPublishedPayload{}, fmt.Errorf("%w: store %q fora do enum {eventstore,mem}", ErrInvalidPayloadRef, p.Record.Store)
		}
		if p.Record.Stream == "" || p.Record.Digest == "" {
			return PayloadPublishedPayload{}, fmt.Errorf("%w: referencia sem stream ou sem digest", ErrInvalidPayloadRef)
		}
	}
	if len(p.DerivedFrom) > maxPayloadOrigins {
		return PayloadPublishedPayload{}, fmt.Errorf("%w: %d origens > %d", ErrInvalidPayloadRef, len(p.DerivedFrom), maxPayloadOrigins)
	}
	var origins []PayloadOrigin
	if len(p.DerivedFrom) > 0 {
		origins = make([]PayloadOrigin, 0, len(p.DerivedFrom))
		seen := make(map[PayloadOrigin]struct{}, len(p.DerivedFrom))
		for _, o := range p.DerivedFrom {
			if !plan.ValidNodeID(o.NodeID) || !plan.ValidIdentifier(o.Output) {
				return PayloadPublishedPayload{}, fmt.Errorf("%w: origem de proveniencia fora da grammar", ErrInvalidPayloadRef)
			}
			if o.NodeID == p.NodeID && o.Output == p.Output {
				return PayloadPublishedPayload{}, fmt.Errorf("%w: o payload %q/%q deriva de si mesmo", ErrInvalidPayloadRef, p.NodeID, p.Output)
			}
			if _, dup := seen[o]; dup {
				return PayloadPublishedPayload{}, fmt.Errorf("%w: origem %q/%q repetida", ErrInvalidPayloadRef, o.NodeID, o.Output)
			}
			seen[o] = struct{}{}
			origins = append(origins, o)
		}
	}
	return PayloadPublishedPayload{
		PlanID:         p.PlanID,
		NodeID:         p.NodeID,
		Output:         p.Output,
		Type:           typ,
		Taint:          taint,
		ContractDigest: digest,
		Record:         p.Record,
		Closed:         closed,
		DerivedFrom:    origins,
	}, nil
}

// normalizeClosed valida e copia o CONTEÚDO INLINE de um payload de forma fechada, e
// impõe a outra metade da simetria: uma forma ABERTA não pode trazer conteúdo fechado.
//
// A validação é a MESMA de [NewVerdictRecorded] — códigos e inteiros, nomes de
// [plan.ValidIdentifier], sem duplicados, sob os mesmos tectos de aridade — e é
// deliberadamente a mesma: se um payload `verdict` pudesse ser validado por regras
// mais frouxas do que o facto `plan.verdict_recorded`, o canal de payload virava a
// porta das traseiras do veredicto. Determinística (preserva a ordem declarada), pura.
func normalizeClosed(typ plan.PayloadType, in *ClosedPayload) (*ClosedPayload, error) {
	if !typ.ClosedForm() {
		if in != nil {
			return nil, fmt.Errorf("%w: um payload de forma aberta (%q) NÃO carrega conteúdo — o material fica no registo", ErrInvalidPayloadRef, typ)
		}
		return nil, nil
	}
	if in == nil {
		return nil, fmt.Errorf("%w: um payload de forma fechada (%q) tem de carregar o conteúdo inline (é o que torna a forma verificável)", ErrInvalidPayloadRef, typ)
	}
	out := &ClosedPayload{}
	switch typ {
	case plan.PayloadVerdict:
		if !in.Outcome.valid() {
			return nil, fmt.Errorf("%w: outcome %q fora do enum {pass,fail}", ErrInvalidPayloadRef, in.Outcome)
		}
		out.Outcome = in.Outcome
	default: // plan.PayloadMetrics
		if in.Outcome != "" || len(in.Reasons) > 0 {
			return nil, fmt.Errorf("%w: um payload %q só carrega métricas (outcome/reasons são do veredicto)", ErrInvalidPayloadRef, typ)
		}
		if len(in.Metrics) == 0 {
			return nil, fmt.Errorf("%w: um payload %q sem métricas não tem conteúdo nenhum", ErrInvalidPayloadRef, typ)
		}
	}
	if len(in.Reasons) > maxVerdictReasons {
		return nil, fmt.Errorf("%w: %d razões > %d", ErrInvalidPayloadRef, len(in.Reasons), maxVerdictReasons)
	}
	if len(in.Reasons) > 0 {
		out.Reasons = make([]string, 0, len(in.Reasons))
		seen := make(map[string]struct{}, len(in.Reasons))
		for _, r := range in.Reasons {
			if !plan.ValidIdentifier(r) {
				// Uma razão é um CÓDIGO. Uma frase — com espaços, maiúsculas ou
				// pontuação — cai aqui, que é o ponto do campo.
				return nil, fmt.Errorf("%w: razão fora da grammar de código", ErrInvalidPayloadRef)
			}
			if _, dup := seen[r]; dup {
				return nil, fmt.Errorf("%w: razão %q repetida", ErrInvalidPayloadRef, r)
			}
			seen[r] = struct{}{}
			out.Reasons = append(out.Reasons, r)
		}
	}
	if len(in.Metrics) > maxVerdictMetrics {
		return nil, fmt.Errorf("%w: %d métricas > %d", ErrInvalidPayloadRef, len(in.Metrics), maxVerdictMetrics)
	}
	if len(in.Metrics) > 0 {
		out.Metrics = make([]VerdictMetric, 0, len(in.Metrics))
		seen := make(map[string]struct{}, len(in.Metrics))
		for _, m := range in.Metrics {
			if !plan.ValidIdentifier(m.Name) {
				return nil, fmt.Errorf("%w: nome de métrica fora da grammar", ErrInvalidPayloadRef)
			}
			if _, dup := seen[m.Name]; dup {
				return nil, fmt.Errorf("%w: métrica %q repetida", ErrInvalidPayloadRef, m.Name)
			}
			seen[m.Name] = struct{}{}
			out.Metrics = append(out.Metrics, m)
		}
	}
	return out, nil
}

// BranchDecidedPayload — corpo de `plan.branch_decided` (ADR-022 §2.1, AOS-270).
// A DECISÃO de ramo de UM nó, fixada pelo despachante a partir do resultado
// REGISTADO das origens.
//
// Content-free como todo este domínio: ids estruturais (node_ids, já limitados pela
// grammar de AOS-231), um booleano e o DIGEST canónico da expressão avaliada. NUNCA
// o valor observado (uma métrica pode ser derivada de conteúdo untrusted), nunca o
// texto da condição, nunca prosa do modelo.
//
// O digest é o que amarra a decisão à expressão EXACTA que a produziu: no replay,
// um digest divergente diz «o documento mudou» — e um plano editado não é um
// replay, é um plano novo que volta ao gate (fail-closed a jusante).
type BranchDecidedPayload struct {
	PlanID string `json:"plan_id"`
	NodeID string `json:"node_id"`
	// Taken indica se o ramo foi TOMADO (todas as condições satisfeitas). Falso
	// significa ramo DEFINITIVAMENTE não tomado — o nó não será despachado neste
	// plano (o retorno a um nó já executado é replan de subgrafo, AOS-239, nunca
	// uma re-avaliação).
	Taken bool `json:"taken"`
	// ConditionDigest é [plan.ConditionDigest] das arestas condicionais do nó.
	ConditionDigest string `json:"condition_digest"`
	// Sources são os node_ids das ORIGENS avaliadas, pela ordem declarada.
	Sources []string `json:"sources"`
}

// GapState distingue a fase do gap de capacidade.
type GapState string

const (
	GapOpened   GapState = "opened"
	GapResolved GapState = "resolved"
)

func (g GapState) valid() bool { return g == GapOpened || g == GapResolved }

func eventForGap(g GapState) (string, bool) {
	switch g {
	case GapOpened:
		return EventCapabilityGapOpened, true
	case GapResolved:
		return EventCapabilityGapResolved, true
	default:
		return "", false
	}
}

// CapabilityGapPayload — corpo de `plan.capability_gap_opened`/`resolved`:
// node_id, skill candidata (id) e RatificationID (AOS-096). Ids opacos, sem PII.
type CapabilityGapPayload struct {
	PlanID         string   `json:"plan_id"`
	NodeID         string   `json:"node_id"`
	State          GapState `json:"state"`
	CandidateSkill string   `json:"candidate_skill"`
	RatificationID string   `json:"ratification_id,omitempty"`
}

// ReplanPhase distingue o pedido da aplicação do re-plano.
type ReplanPhase string

const (
	ReplanRequested ReplanPhase = "requested"
	ReplanApplied   ReplanPhase = "applied"
)

func (p ReplanPhase) valid() bool { return p == ReplanRequested || p == ReplanApplied }

func eventForReplan(p ReplanPhase) (string, bool) {
	switch p {
	case ReplanRequested:
		return EventReplanRequested, true
	case ReplanApplied:
		return EventReplanApplied, true
	default:
		return "", false
	}
}

// ReplanPayload — corpo de `plan.replan_requested`/`applied`: o subgrafo afectado
// (ids de nós), o orçamento residual e o novo hash (§4.2).
type ReplanPayload struct {
	PlanID         string      `json:"plan_id"`
	Phase          ReplanPhase `json:"phase"`
	Subgraph       []string    `json:"subgraph"`
	ResidualBudget int64       `json:"residual_budget"`
	NewPlanHash    string      `json:"new_plan_hash"`
}

// marshal serializa um payload de domínio de forma determinística (encoding/json
// é estável para structs: ordem dos campos). Erro raro (só tipos não
// serializáveis, que não existem aqui) propaga fail-closed.
func marshal(v any) (json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}
