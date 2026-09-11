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

// EffectOracle decide se uma ferramenta PINADA tem EFEITO — o predicado que torna
// «read-only por construção» (ADR-022 §2.2, AOS-271) uma propriedade da NHI emitida e
// não uma promessa do organigrama.
//
// O CRITÉRIO NÃO VIVE AQUI, e isso é o ponto. O materializador não tem (nem deve ter)
// o snapshot de capabilities pinado nem a taxonomia de risco: o critério é
// `planvalidate.IsEffectTool` — derivado dos eixos de risco PINADOS que a regra 6
// (AOS-232) já consome — e chega cá como `planvalidate.Snapshot.EffectOracle()`,
// ligado pelo composition root POR TIPO ESTRUTURAL. Uma só definição de «efeito»,
// dois pontos de enforcement (a admissão rejeita, a materialização clampa), zero
// import entre os pacotes.
type EffectOracle func(plan.ToolRef) bool

// DefaultEffectOracle é o oráculo por omissão e é FAIL-CLOSED ATÉ AO FIM: sem
// conhecimento dos eixos pinados, TODA a ferramenta conta como de efeito.
//
// A consequência é deliberada e vale a pena dizê-la em voz alta: um wiring que se
// esqueça de ligar o oráculo real materializa verificadores com autoridade VAZIA — os
// verificadores ficam inúteis, e nota-se. A alternativa (assumir «sem efeito» por
// omissão) daria a um verificador toda a autoridade das suas tools num sistema onde
// ninguém olhou, e NÃO se notava. Entre falhar visivelmente e falhar em silêncio, o
// default escolhe o primeiro.
//
// Não afecta nós não-verificadores: o clamp de §2.2 só se aplica a [plan.Node.IsVerifier].
func DefaultEffectOracle(plan.ToolRef) bool { return true }

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

// O EFEITO DE SPAWN SAIU DA MATERIALIZAÇÃO (AOS-390, ADR-024). A porta `Spawner` e o
// tipo `RoleSpawn` que aqui viviam foram removidos: um papel-que-expande já NÃO é
// spawnado na materialização — é admitido no DAG como nó pendente (sem tool) e o
// `Delegator.Spawn` passa a ser disparado pelo `DispatchSink` do despacho governado,
// só quando o nó fica elegível. A autoridade CLAMPADA do papel (o vínculo tools[] →
// Authority[]) continua a ser calculada aqui ([Materializer.authorityForNode]) e
// REGISTADA em `plan.materialized.Nodes[].Tools` — é dali que o sink a reconstrói para
// o spawn, sem uma segunda fonte de verdade.

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
	admission Admission
	leaf      LeafAdmitter
	recorder  MaterializeRecorder
	mapper    CapabilityMapper
	classify  SpawnClassifier
	effect    EffectOracle
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

// WithEffectOracle injecta o predicado «esta tool tem efeito?» que governa o clamp
// read-only da NHI do verificador (ADR-022 §2.2). O composition root liga-o a
// `planvalidate.Snapshot.EffectOracle()` — o MESMO critério que a admissão usou para
// rejeitar. Default: [DefaultEffectOracle] (fail-closed).
func WithEffectOracle(o EffectOracle) Option {
	return func(mt *Materializer) {
		if o != nil {
			mt.effect = o
		}
	}
}

// NewMaterializer constrói um Materializer. admission, leaf e recorder são
// OBRIGATÓRIOS — a sua ausência é fail-closed ([ErrDeps]). Já NÃO recebe `spawner`: o
// spawn de papéis saiu da materialização para o despacho governado (AOS-390, ADR-024).
func NewMaterializer(admission Admission, leaf LeafAdmitter, recorder MaterializeRecorder, opts ...Option) (*Materializer, error) {
	if admission == nil || leaf == nil || recorder == nil {
		return nil, ErrDeps
	}
	m := &Materializer{
		admission: admission,
		leaf:      leaf,
		recorder:  recorder,
		mapper:    DefaultCapabilityMapper,
		classify:  DefaultClassifier,
		effect:    DefaultEffectOracle,
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
	if m.effect == nil {
		m.effect = DefaultEffectOracle
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
//  4. FASE 2 — ADMITE cada nó no DAG como PENDENTE ([LeafAdmitter], task.node.created):
//     a folha com a sua tool call, o papel-que-expande SEM tool (placeholder). NÃO
//     produz efeito — o spawn do papel e o arranque da folha são do despacho governado
//     (plandispatch.Dispatcher/DispatchSink), disparados por elegibilidade (ADR-024);
//  5. apensa `plan.materialized` com o mapa node_id → materialização (kind + autoridade
//     clampada), a fonte de verdade que o sink lê para spawnar.
//
// Devolve o payload apenso. Fail-closed em qualquer passo — um erro de porta aborta
// e propaga (a consolidação de reservas já efectuadas é do ciclo-de-vida do run).
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

	// NOTA (AOS-390, ADR-024): a materialização é ADMISSÃO-PURA e NÃO lê `conditional_on`
	// — e isso é seguro precisamente porque admitir um nó condicional no DAG não produz
	// efeito nenhum (o guard fail-closed de AOS-389 deixou de ser necessário). O efeito
	// por-nó é do despacho governado (plandispatch.Dispatcher), que avalia `conditional_on`
	// e poda `branch_not_taken` antes de qualquer spawn/arranque; um Dispatcher composto
	// sem as portas de ramos recusa o plano fail-closed (ErrConditionalUnsupported). Sem
	// despachante nenhum, um nó condicional fica pendente para sempre — fail-closed também.

	// Classificação + clamp de autoridade, uma vez por nó.
	planned := make([]plannedNode, 0, len(order))
	for _, n := range order {
		kind := m.classify(n, req.Doc)
		if kind != plannerevents.SpawnLeaf && kind != plannerevents.SpawnRole {
			return empty, fmt.Errorf("%w: %q (node %q)", ErrInvalidSpawnKind, kind, n.NodeID)
		}
		// UM VERIFICADOR É SEMPRE FOLHA (ADR-022 §2.2, correcção da auditoria da
		// wave). A enumeração de §2.2 exclui o SPAWN da autoridade do verificador —
		// e «spawn», no organigrama, não é uma tool: é materializar-se como
		// [plannerevents.SpawnRole], encabeçando uma sub-árvore de delegação com
		// `identity.ChildRequest` própria. O clamp de [authorityForNode] só filtrava
		// capabilities derivadas de TOOLS, pelo que um verificador com dependentes
		// era classificado papel-que-expande pelo [DefaultClassifier] e ganhava, por
		// via da topologia, a autoridade de delegação que o ADR lhe nega.
		//
		// A admissão já recusa o plano (`planvalidate`, regra V4: nenhum nó declara
		// um verificador em `depends_on`); este forço é a SEGUNDA linha, para
		// documentos que cheguem por outra porta (replan, migração, edição no gate)
		// ou com um [SpawnClassifier] injectado pelo wiring — e é por isso que NÃO
		// depende do classificador: um verificador não delega, independentemente de
		// quem classifica.
		if n.IsVerifier() {
			kind = plannerevents.SpawnLeaf
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

	// FASE 2 — ADMISSÃO no DAG (AOS-390, ADR-024): SEM efeito. Todos os nós — folhas E
	// papéis-que-expandem — são admitidos como nós PENDENTES no DAG (`task.node.created`).
	// A folha leva a sua tool call concreta; o papel é admitido SEM tool (placeholder
	// pendente). NodeSpec.ToolID/Capability são omitempty e um nó tool-less é um estado
	// válido do DAG (AOS-025); o despachante vê-o NodePending e o `DispatchSink` produz o
	// efeito — spawn do sub-agente (papel) ou arranque (folha) — só quando o nó fica
	// ELEGÍVEL (gate + deps + condição + cartão + headroom). É a separação de ADR-024:
	// materializar admite, despachar produz efeito.
	//
	// A autoridade CLAMPADA do nó ([authorityForNode]) viaja em
	// `plan.materialized.Nodes[].Tools` — é dali (uma só fonte de verdade) que o sink a
	// reconstrói para o spawn do papel, com o orçamento estimado do documento.
	matNodes := make([]plannerevents.MaterializedNode, 0, len(planned))
	for _, p := range planned {
		ln := LeafNode{
			RunID: req.RunID, PlanID: req.PlanID, NodeID: p.node.NodeID, Role: p.node.Role,
			Capabilities: p.caps,
		}
		if p.kind == plannerevents.SpawnLeaf {
			if t, ok := m.primaryTool(p.node); ok {
				ln.ToolID = t.Name
				ln.Capability = m.mapper(t)
			}
		}
		if err := m.leaf.AdmitLeaf(ctx, ln); err != nil {
			return empty, fmt.Errorf("planmaterialize: admitir nó %q (%s): %w", p.node.NodeID, p.kind, err)
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
//
// SEGUNDO CLAMP, PARA O VERIFICADOR (ADR-022 §2.2, AOS-271). Se o nó declara o papel
// reservado [plan.RoleVerifier], as tools DE EFEITO ([EffectOracle]) são retiradas
// ANTES do mapeamento: a NHI que se emite não tem sequer a capability, pelo que não
// há nada para o RM negar no caminho quente. É o mesmo ponto do código, o mesmo
// vínculo tools[] → Authority[] — um filtro a mais, não um caminho novo.
//
// DUAS LINHAS, NESTA ORDEM. A admissão (`planvalidate`, regra V3) já REJEITA um
// verificador que pine tools de efeito, pelo que num plano APROVADO este filtro não
// retira nada: é defesa-em-profundidade contra um documento que chegue por outra
// porta (replan, migração, edição no gate). O que ele retirar fica VISÍVEL no facto
// `plan.materialized` (Nodes[].Tools é a autoridade CLAMPADA, não a declarada) —
// nunca é uma remoção silenciosa. E o RiskGate do RM (AOS-074) continua a ser a
// TERCEIRA linha, fail-closed, para lá destas duas.
func (m *Materializer) authorityForNode(n plan.Node) []string {
	verifier := n.IsVerifier()
	seen := make(map[string]struct{}, len(n.Tools))
	caps := make([]string, 0, len(n.Tools))
	for _, t := range n.Tools {
		if verifier && m.effect(t) {
			continue // read-only por construção: a autoridade de efeito não é emitida
		}
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

// primaryTool devolve a tool call CONCRETA de um nó-folha: a PRIMEIRA tool do papel
// que sobrevive ao clamp de autoridade (ordem do documento).
//
// PORQUE NÃO É SIMPLESMENTE `Tools[0]`. Era, e num nó verificador isso seria um
// buraco silencioso: se a primeira tool declarada fosse de efeito, o nó-folha ia para
// o DAG com uma tool call que a sua PRÓPRIA autoridade clampada não cobre — um pedido
// que nasce condenado (o RM nega) ou, pior, um pedido cuja autoridade alguém a
// jusante fosse tentado a «arranjar». A tool call e a autoridade têm de sair do MESMO
// filtro. Determinística e sem alocação.
func (m *Materializer) primaryTool(n plan.Node) (plan.ToolRef, bool) {
	verifier := n.IsVerifier()
	for _, t := range n.Tools {
		if verifier && m.effect(t) {
			continue
		}
		return t, true
	}
	return plan.ToolRef{}, false
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
