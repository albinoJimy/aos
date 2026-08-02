package planmaterialize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	identity "github.com/aos-ref/platform/identity"
)

// clampU64ToInt64 converte um uint64 UNTRUSTED (ex.: BudgetEstimate.Tokens/CostMicroUSD
// vindos do PlanDocument proposto pelo LLM) para int64 SATURANDO em [math.MaxInt64] em vez
// de transbordar para negativo. Um valor >= 2^63 num orçamento estimado não deve virar um
// débito NEGATIVO na admissão/reserva a jusante (corromperia a contabilidade fail-closed);
// satura-se determinística e não-silenciosamente no tecto. Espelha o clamp de AOS-232
// (planvalidate) para a mesma classe de input untrusted.
func clampU64ToInt64(u uint64) int64 {
	if u > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(u)
}

// Sentinelas de erro do materializador (comparáveis por errors.Is — fail-closed).
var (
	// ErrDeps — dependências obrigatórias do Materializer em falta.
	ErrDeps = errors.New("planmaterialize: dependências em falta (admission/leaf/spawner/recorder)")
	// ErrInvalidRequest — pedido de materialização malformado (run_id/plan_id vazio,
	// documento sem nós, ou node_id vazio/duplicado).
	ErrInvalidRequest = errors.New("planmaterialize: pedido de materialização inválido")
	// ErrNodeNotAdmitted — a admissão global (AOS-027/028) recusou um nó. Fail-closed:
	// o plano APROVADO não materializa parcialmente — a recusa aborta antes de
	// qualquer spawn/nó (nenhum efeito parcial).
	ErrNodeNotAdmitted = errors.New("planmaterialize: nó não admitido pela admissão global (fail-closed)")
	// ErrInvalidSpawnKind — o classificador devolveu um kind fora de {leaf, role}.
	ErrInvalidSpawnKind = errors.New("planmaterialize: spawn kind inválido (esperado leaf|role)")
)

// CapabilityMapper reconcilia a granularidade das ferramentas PINADAS no REG
// (name+version+digest, tecnica/18 §3.3) com as capabilities COARSE do modelo de
// autoridade da NHI (identity.ChildRequest.Authority são strings tipo "cap:*").
//
// POLÍTICA DE MAPEAMENTO (declarada). Uma tool pinada mapeia para UMA capability
// coarse. O [DefaultCapabilityMapper] deriva "cap:tool:"+Name — DESCARTANDO version
// e digest de propósito: várias versões pinadas da MESMA ferramenta colapsam na
// mesma capability (a autoridade é sobre "que ferramenta", não "que digest"; a
// resolução do digest é gate de proposta, AOS-231). O wiring pode injectar um mapper
// diferente (ex.: consultar a taxonomia de capabilities do REG). Um mapper que
// devolva "" para uma tool omite-a da autoridade (fail-closed: sem capability, sem
// autoridade — nunca uma autoridade "por defeito").
type CapabilityMapper func(plan.ToolRef) string

// DefaultCapabilityMapper mapeia uma ToolRef pinada para a capability coarse
// "cap:tool:"+Name. Ver [CapabilityMapper].
func DefaultCapabilityMapper(t plan.ToolRef) string {
	if t.Name == "" {
		return ""
	}
	return "cap:tool:" + t.Name
}

// SpawnClassifier decide, DETERMINISTICAMENTE a partir do documento, se um nó é uma
// FOLHA ([plannerevents.SpawnLeaf] → task.node.created) ou um PAPEL-QUE-EXPANDE
// ([plannerevents.SpawnRole] → Delegator.Spawn).
//
// POLÍTICA (declarada) — ver [DefaultClassifier]. O schema CONGELADO do PlanDocument
// (AOS-230) não tem marcador explícito folha-vs-papel; esta é a fronteira honesta
// (§5): aplica-se um default topológico substituível pelo wiring.
type SpawnClassifier func(node plan.Node, doc plan.PlanDocument) plannerevents.SpawnKind

// DefaultClassifier classifica um nó como PAPEL-QUE-EXPANDE se e só se ALGUM outro
// nó do plano o declara em `depends_on` (tem ≥1 dependente) — um nó de que outros
// dependem encabeça uma sub-árvore de trabalho a jusante e materializa-se como
// sub-agente delegado (a "organização" efémera do meta-run, §4.1). Um nó sem
// dependentes (sumidouro do DAG de dependências) é uma FOLHA: uma unidade de
// trabalho terminal, materializada directamente como nó-tarefa.
//
// É determinístico (só lê o documento) e estável. É uma POLÍTICA declarada, não uma
// verdade universal: o schema não distingue os dois casos, pelo que o wiring pode
// injectar um [SpawnClassifier] dirigido por metadados de papel do REG.
func DefaultClassifier(node plan.Node, doc plan.PlanDocument) plannerevents.SpawnKind {
	for _, n := range doc.Nodes {
		for _, dep := range n.DependsOn {
			if dep == node.NodeID {
				return plannerevents.SpawnRole
			}
		}
	}
	return plannerevents.SpawnLeaf
}

// AdmitRequest é o pedido de admissão global de UM nó (AOS-027/028). Content-free:
// ids, papel, kind e a estimativa de orçamento do nó — nunca conteúdo untrusted.
type AdmitRequest struct {
	RunID        string
	PlanID       string
	NodeID       string
	Role         string
	Kind         plannerevents.SpawnKind
	Tokens       int64
	CostMicroUSD int64
}

// AdmitVerdict é o veredicto da admissão global. Fail-closed: Admitted=false impede
// a materialização do nó (e, por ser em duas fases, de todo o plano).
type AdmitVerdict struct {
	Admitted bool
	Reason   string
}

// Admission é a PORTA da admissão global por nó (AOS-027/028). O escalonador pode
// não estar no módulo, pelo que é modelada como interface que o wiring liga. Nenhum
// nó materializa sem um veredicto Admitted.
type Admission interface {
	Admit(ctx context.Context, req AdmitRequest) (AdmitVerdict, error)
}

// LeafNode descreve a materialização de um nó-FOLHA como nó-tarefa (task.node.created,
// AOS-025). ToolID/Capability é a tool call concreta (a primeira tool do papel, em
// ordem do documento); Capabilities é o conjunto coarse completo (autoridade do nó).
type LeafNode struct {
	RunID        string
	PlanID       string
	NodeID       string
	Role         string
	ToolID       string
	Capability   string
	Capabilities []string
}

// LeafAdmitter é a PORTA de admissão de um nó-folha no DAG (AOS-025). Ligada pelo
// wiring a *orchestrator.GraphBuilder (ver adapters.go): AdmitLeaf produz
// task.node.created.
type LeafAdmitter interface {
	AdmitLeaf(ctx context.Context, node LeafNode) error
}

// RoleSpawn descreve o spawn de um PAPEL-QUE-EXPANDE como sub-agente delegado
// (Delegator.Spawn, AOS-026). Child.Authority JÁ vem CLAMPADA às tools do papel
// (ver [Materializer.authorityForNode]): é o vínculo tools[] → Authority[] da NHI
// filha exigido pela tabela de `plan.materialized` (§6.1).
type RoleSpawn struct {
	RunID                 string
	PlanID                string
	NodeID                string
	Role                  string
	ParentToken           string
	ParentBudgetNode      string
	ChildBudgetNode       string
	InheritedTokens       int64
	InheritedCostMicroUSD int64
	Child                 identity.ChildRequest
}

// Spawner é a PORTA de spawn de sub-agente delegado (AOS-026). Ligada pelo wiring a
// *orchestrator.Delegator (ver adapters.go). O clamp da autoridade é do
// materializador (Child.Authority); o issuer_child ainda a intersecta com o
// escopo-da-classe e recusa escalada face à folha do pai (defesa-em-profundidade).
type Spawner interface {
	Spawn(ctx context.Context, req RoleSpawn) error
}

// MaterializeRecorder é a PORTA que apensa `plan.materialized` (constante
// [plannerevents.EventMaterialized]). *plannerevents.Recorder satisfá-la via
// RecordMaterialized — reutiliza a constante do catálogo (nunca um literal novo).
type MaterializeRecorder interface {
	RecordMaterialized(ctx context.Context, p plannerevents.MaterializedPayload) (uint64, error)
}

// Request é o input de uma materialização: a identificação do run/plano, a
// credencial NHI do pai (on-behalf-of das filhas), o nó de orçamento raiz do run
// (pai dos spawns) e o DOCUMENTO APROVADO gravado.
type Request struct {
	RunID string
	// PlanID é o stream do plano (plan_id). Correlaciona `plan.materialized`.
	PlanID string
	// PlanHash é o hash do documento APROVADO (da decisão do gate). Se vazio, é
	// derivado canonicamente do documento (determinístico).
	PlanHash string
	// ParentToken é o token NHI compacto do run/planeador (Credential das filhas).
	ParentToken string
	// RootBudgetNode é o nó de orçamento raiz do run: o ParentBudgetNode dos spawns.
	RootBudgetNode string
	// Doc é o PlanDocument APROVADO (não a saída crua do LLM).
	Doc plan.PlanDocument
}

// Materializer materializa um plano aprovado. Construir com [NewMaterializer]. É
// imutável após a construção; a segurança concorrente é a das portas ligadas.
type Materializer struct {
	admission  Admission
	leaf       LeafAdmitter
	spawner    Spawner
	recorder   MaterializeRecorder
	mapper     CapabilityMapper
	classify   SpawnClassifier
	childClass string
}

// Option configura o Materializer.
type Option func(*Materializer)

// WithCapabilityMapper injecta a política de mapeamento tool→capability (default:
// [DefaultCapabilityMapper]).
func WithCapabilityMapper(m CapabilityMapper) Option {
	return func(mt *Materializer) {
		if m != nil {
			mt.mapper = m
		}
	}
}

// WithClassifier injecta a política de classificação folha-vs-papel (default:
// [DefaultClassifier]).
func WithClassifier(c SpawnClassifier) Option {
	return func(mt *Materializer) {
		if c != nil {
			mt.classify = c
		}
	}
}

// WithChildAgentClass define a classe NHI das identidades filhas dos papéis
// (default "worker"). A classe governa TTL e escopo-máximo (issuer_child); é config
// de wiring, não afecta o clamp da autoridade.
func WithChildAgentClass(class string) Option {
	return func(mt *Materializer) {
		if class != "" {
			mt.childClass = class
		}
	}
}

// NewMaterializer constrói um Materializer. admission, leaf, spawner e recorder são
// OBRIGATÓRIOS — a sua ausência é fail-closed ([ErrDeps]).
func NewMaterializer(admission Admission, leaf LeafAdmitter, spawner Spawner, recorder MaterializeRecorder, opts ...Option) (*Materializer, error) {
	if admission == nil || leaf == nil || spawner == nil || recorder == nil {
		return nil, ErrDeps
	}
	m := &Materializer{
		admission:  admission,
		leaf:       leaf,
		spawner:    spawner,
		recorder:   recorder,
		mapper:     DefaultCapabilityMapper,
		classify:   DefaultClassifier,
		childClass: "worker",
	}
	for _, o := range opts {
		o(m)
	}
	if m.mapper == nil {
		m.mapper = DefaultCapabilityMapper
	}
	if m.classify == nil {
		m.classify = DefaultClassifier
	}
	return m, nil
}

// plannedNode é o resultado, por nó, da classificação + clamp de autoridade,
// calculado UMA vez e reutilizado nas duas fases (admissão, materialização) —
// garantindo que o que foi admitido é exactamente o que se materializa.
type plannedNode struct {
	node plan.Node
	kind plannerevents.SpawnKind
	caps []string
}

// Materialize materializa o documento APROVADO, DETERMINISTICAMENTE (§3.6):
//
//  1. valida o pedido e ordena os nós por node_id (ordem canónica, independente da
//     ordem do slice — o mesmo documento produz sempre a mesma sequência);
//  2. classifica cada nó (folha vs papel) e calcula a autoridade CLAMPADA às suas
//     tools;
//  3. FASE 1 — admissão global de TODOS os nós (AOS-027/028). Uma negação aborta
//     fail-closed ANTES de qualquer efeito (zero materialização parcial);
//  4. FASE 2 — materializa: folha → [LeafAdmitter] (task.node.created); papel →
//     [Spawner] (Delegator.Spawn) com a NHI filha limitada às tools do papel;
//  5. apensa `plan.materialized` com o mapa node_id → materialização.
//
// Devolve o payload apenso. Fail-closed em qualquer passo — um erro de porta aborta
// e propaga (o consolidação de reservas já efectuadas é do ciclo-de-vida do run).
func (m *Materializer) Materialize(ctx context.Context, req Request) (plannerevents.MaterializedPayload, error) {
	var empty plannerevents.MaterializedPayload
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if req.RunID == "" || req.PlanID == "" || len(req.Doc.Nodes) == 0 {
		return empty, ErrInvalidRequest
	}

	// Ordem canónica: cópia ordenada por node_id (nunca a ordem do slice de entrada
	// nem a de um mapa). Valida node_ids não-vazios e sem duplicados (fail-closed).
	order := make([]plan.Node, len(req.Doc.Nodes))
	copy(order, req.Doc.Nodes)
	sort.Slice(order, func(i, j int) bool { return order[i].NodeID < order[j].NodeID })
	seen := make(map[string]struct{}, len(order))
	for _, n := range order {
		if n.NodeID == "" {
			return empty, fmt.Errorf("%w: node_id vazio", ErrInvalidRequest)
		}
		if _, dup := seen[n.NodeID]; dup {
			return empty, fmt.Errorf("%w: node_id duplicado %q", ErrInvalidRequest, n.NodeID)
		}
		seen[n.NodeID] = struct{}{}
	}

	// Classificação + clamp de autoridade, uma vez por nó.
	planned := make([]plannedNode, 0, len(order))
	for _, n := range order {
		kind := m.classify(n, req.Doc)
		if kind != plannerevents.SpawnLeaf && kind != plannerevents.SpawnRole {
			return empty, fmt.Errorf("%w: %q (node %q)", ErrInvalidSpawnKind, kind, n.NodeID)
		}
		planned = append(planned, plannedNode{node: n, kind: kind, caps: m.authorityForNode(n)})
	}

	// FASE 1 — admissão global de TODOS os nós antes de qualquer efeito. Fail-closed.
	for _, p := range planned {
		v, err := m.admission.Admit(ctx, AdmitRequest{
			RunID: req.RunID, PlanID: req.PlanID, NodeID: p.node.NodeID, Role: p.node.Role,
			Kind:   p.kind,
			Tokens: clampU64ToInt64(p.node.BudgetEstimate.Tokens), CostMicroUSD: clampU64ToInt64(p.node.BudgetEstimate.CostMicroUSD),
		})
		if err != nil {
			return empty, fmt.Errorf("planmaterialize: admissão do nó %q: %w", p.node.NodeID, err)
		}
		if !v.Admitted {
			return empty, fmt.Errorf("%w: nó %q: %s", ErrNodeNotAdmitted, p.node.NodeID, v.Reason)
		}
	}

	// FASE 2 — materialização determinística.
	matNodes := make([]plannerevents.MaterializedNode, 0, len(planned))
	for _, p := range planned {
		switch p.kind {
		case plannerevents.SpawnLeaf:
			ln := LeafNode{
				RunID: req.RunID, PlanID: req.PlanID, NodeID: p.node.NodeID, Role: p.node.Role,
				Capabilities: p.caps,
			}
			if len(p.node.Tools) > 0 {
				ln.ToolID = p.node.Tools[0].Name
				ln.Capability = m.mapper(p.node.Tools[0])
			}
			if err := m.leaf.AdmitLeaf(ctx, ln); err != nil {
				return empty, fmt.Errorf("planmaterialize: admitir nó-folha %q: %w", p.node.NodeID, err)
			}
		case plannerevents.SpawnRole:
			// ORÇAMENTO ACHATADO (fronteira honesta, §5). Todo o spawn de papel pende
			// do nó de orçamento RAIZ do run (req.RootBudgetNode), não do papel-pai
			// topológico. A materialização projecta a organização à CABEÇA, num único
			// passe; o aninhamento de orçamento papel-sob-papel e a consolidação por
			// sub-run pertencem ao ciclo-de-vida/despacho (AOS-238), a jusante e fora
			// de AOS-237. O clamp de AUTORIDADE (Child.Authority) é por-nó e não é
			// afectado por isto.
			rs := RoleSpawn{
				RunID: req.RunID, PlanID: req.PlanID, NodeID: p.node.NodeID, Role: p.node.Role,
				ParentToken: req.ParentToken, ParentBudgetNode: req.RootBudgetNode, ChildBudgetNode: p.node.NodeID,
				InheritedTokens: clampU64ToInt64(p.node.BudgetEstimate.Tokens), InheritedCostMicroUSD: clampU64ToInt64(p.node.BudgetEstimate.CostMicroUSD),
				Child: identity.ChildRequest{
					AgentID:    req.RunID + "/" + p.node.NodeID,
					AgentClass: m.childClass,
					// Authority CLAMPADA às tools do papel — ver authorityForNode.
					Authority: p.caps,
				},
			}
			if err := m.spawner.Spawn(ctx, rs); err != nil {
				return empty, fmt.Errorf("planmaterialize: spawn do papel %q: %w", p.node.NodeID, err)
			}
		}
		matNodes = append(matNodes, plannerevents.MaterializedNode{NodeID: p.node.NodeID, Kind: p.kind, Tools: p.caps})
	}

	payload := plannerevents.MaterializedPayload{
		PlanID:   req.PlanID,
		PlanHash: m.planHash(req),
		Nodes:    matNodes,
	}
	if _, err := m.recorder.RecordMaterialized(ctx, payload); err != nil {
		return empty, fmt.Errorf("planmaterialize: apensar plan.materialized: %w", err)
	}
	return payload, nil
}

// authorityForNode calcula a autoridade da NHI do nó CLAMPADA às tools do PAPEL: o
// conjunto das capabilities coarse derivadas EXCLUSIVAMENTE de node.Tools, sem
// duplicados e ordenado (determinístico).
//
// O CLAMP É INTRÍNSECO: nada fora de node.Tools alimenta o conjunto. Uma tool que
// pertence a OUTRO papel nunca entra nesta autoridade — é isto que impede a escalada
// de privilégio na materialização (a falha-antes: uma implementação que usasse as
// tools do PLANO INTEIRO em vez das do nó incluiria capabilities de papéis alheios).
func (m *Materializer) authorityForNode(n plan.Node) []string {
	seen := make(map[string]struct{}, len(n.Tools))
	caps := make([]string, 0, len(n.Tools))
	for _, t := range n.Tools {
		c := m.mapper(t)
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		caps = append(caps, c)
	}
	sort.Strings(caps)
	return caps
}

// planHash devolve o hash do documento aprovado: o fornecido (da decisão do gate)
// ou, se vazio, o hash canónico derivado do documento (determinístico via
// [plan.Encode]).
func (m *Materializer) planHash(req Request) string {
	if req.PlanHash != "" {
		return req.PlanHash
	}
	raw, err := plan.Encode(req.Doc)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
