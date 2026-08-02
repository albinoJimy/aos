package intake

import "github.com/aos-ref/control-plane/orchestrator/plannerevents"

// Level é o nível de autonomia L0–L5 do chamador (tecnica/18 §7.2, ADR-014). O
// planeador nasce a L0 (todo o plano exige aprovação humana); a promoção é por
// fiabilidade medida. Este pacote NÃO decide aprovação a partir do nível — isso é
// política do [SpawnGate] (ADR-013/AOS-236). O nível viaja no [SpawnAttempt] para
// que a reentrada por-spawn seja avaliada ao nível do chamador, não a um nível
// inventado.
type Level uint8

const (
	L0 Level = iota // aprovação humana de todo o plano (nível de arranque)
	L1
	L2
	L3
	L4 // auto-aprovação dentro de envelope; danger/capability_gap forçam revisão
	L5
)

// Valid indica se l está no intervalo fechado L0–L5. Fail-closed: um nível fora do
// intervalo é lixo e não deve ser tratado como uma autonomia válida.
func (l Level) Valid() bool { return l <= L5 }

// IntakeMode é o sinal de routing EXPLÍCITO do principal, recebido por um canal de
// controlo autenticado (tecnica/18 §3.5). É um campo DECLARATIVO — um enum fechado,
// nunca texto livre — pelo que não é uma superfície de taint como o `objective`.
type IntakeMode string

const (
	// IntakeModeUnset — o principal não declarou rota. Sem um sinal positivo de
	// simplicidade, a ambiguidade resolve para meta (fail-safe).
	IntakeModeUnset IntakeMode = ""
	// IntakeModeSimple — o principal pede explicitamente a rota directa de 1 nó.
	// Só é honrado na ausência de qualquer sinal de meta (defesa anti-gaming).
	IntakeModeSimple IntakeMode = "simple"
	// IntakeModeMeta — o principal pede explicitamente planeamento + gate.
	IntakeModeMeta IntakeMode = "meta"
)

func (m IntakeMode) valid() bool {
	return m == IntakeModeUnset || m == IntakeModeSimple || m == IntakeModeMeta
}

// trivialCeiling é o tecto estrutural de um run de 1 nó: profundidade/fan-out <= 1.
// Um tecto PEDIDO acima disto é um sinal de meta (o principal antecipa um subgrafo).
// 0 significa "não pedido" e é trivial por omissão.
const trivialCeiling = 1

// Goal é a superfície de intake de um objectivo de meta-nível. Carrega os ids
// opacos para a proveniência do evento, o `objective` UNTRUSTED (ADR-005) e os
// campos DECLARATIVOS de routing.
//
// O `Objective` é transportado para o planeamento a jusante e para a proveniência,
// mas NUNCA é input de classificação: [Goal.Signals] descarta-o por construção
// (não há campo para ele em [Signals]). É essa fronteira que torna a rota imune a
// injecção no objectivo — nenhuma heurística lê o texto.
type Goal struct {
	GoalID string
	PlanID string
	// Objective é o texto de linguagem natural do objectivo (dados untrusted,
	// ADR-005). NÃO é lido pela classificação.
	Objective string

	// Campos DECLARATIVOS de routing (os ÚNICOS inputs de [Classify]):
	IntakeMode         IntakeMode // rota explícita do principal (canal autenticado)
	RootBudgetMicroUSD uint64     // orçamento raiz pedido, em micro-USD
	RequestedMaxDepth  int        // tecto de profundidade pedido (0 = não pedido)
	RequestedMaxFanout int        // tecto de fan-out pedido (0 = não pedido)
	// RoleCardinality é o nº de papéis/capabilities do pedido ESTRUTURADO (não do
	// texto): 0 = não declarado (ambíguo), 1 = um papel (candidato a 1 nó), >1 =
	// multi-papel (sinal de meta).
	RoleCardinality int
}

// Signals é a projecção DECLARATIVA de um [Goal] — os únicos campos que [Classify]
// observa. NÃO tem campo `Objective`: a imunidade a injecção no objectivo é
// estrutural, garantida pelo sistema de tipos, não por uma promessa em comentário.
type Signals struct {
	IntakeMode         IntakeMode
	RootBudgetMicroUSD uint64
	RequestedMaxDepth  int
	RequestedMaxFanout int
	RoleCardinality    int
}

// Signals extrai os sinais declarativos de g, DESCARTANDO o `objective`. Dois Goals
// com objectivos diferentes mas os mesmos campos declarativos produzem [Signals]
// iguais — logo a mesma rota. É a fronteira anti-taint do §3.5.
func (g Goal) Signals() Signals {
	return Signals{
		IntakeMode:         g.IntakeMode,
		RootBudgetMicroUSD: g.RootBudgetMicroUSD,
		RequestedMaxDepth:  g.RequestedMaxDepth,
		RequestedMaxFanout: g.RequestedMaxFanout,
		RoleCardinality:    g.RoleCardinality,
	}
}

// TenantPolicy é o SNAPSHOT de política por-tenant contra o qual se classifica
// (tecnica/18 §3.5). Entra como ARGUMENTO de [Classify] — nunca por lookup vivo —
// para que a classificação seja pura e replayable (mesmo Goal + mesma política ⇒
// mesmo veredicto, mesmo em replay meses depois).
type TenantPolicy struct {
	TenantID string
	// SimpleBudgetCeilingMicroUSD é o orçamento MÁXIMO de um run simples. Um
	// orçamento ESTRITAMENTE acima é um sinal de meta. 0 (não configurado) é o mais
	// restritivo: qualquer orçamento positivo passa a exigir supervisão.
	SimpleBudgetCeilingMicroUSD uint64
}

// Heurísticas aplicadas — o `heuristic` de `plan.intake_classified` (§3.5). São
// rótulos ESTÁVEIS e content-free (nunca texto do Goal), para o evento ser
// replayable e auditável sem arrastar conteúdo untrusted.
const (
	// HeuristicExplicitMeta — o principal pediu `meta` explicitamente.
	HeuristicExplicitMeta = "explicit_intake_mode_meta"
	// HeuristicBudgetOverCeiling — orçamento raiz acima do tecto de simples do tenant.
	HeuristicBudgetOverCeiling = "budget_over_tenant_ceiling"
	// HeuristicCeilingRequested — tecto estrutural pedido acima do trivial.
	HeuristicCeilingRequested = "structural_ceiling_requested"
	// HeuristicMultiRole — cardinalidade de papéis > 1 (organigrama, não 1 nó).
	HeuristicMultiRole = "role_cardinality_multi"
	// HeuristicExplicitSimple — o principal pediu `simple` e não há sinal de meta.
	HeuristicExplicitSimple = "explicit_intake_mode_simple"
	// HeuristicSingleRole — exactamente um papel declarado e nenhum sinal de meta.
	HeuristicSingleRole = "role_cardinality_single"
	// HeuristicAmbiguousMeta — sem sinal positivo de simples nem de meta: fail-safe.
	HeuristicAmbiguousMeta = "ambiguous_defaults_to_meta"
)

// Result é o veredicto de intake: a rota e a heurística que a determinou. A rota
// reutiliza [plannerevents.Classification] (meta|simple) — não se cunha um tipo
// paralelo — para alimentar directamente `plan.intake_classified`.
type Result struct {
	Classification plannerevents.Classification
	Heuristic      string
}

// Classify decide a rota de s contra policy, de forma DETERMINÍSTICA e TOTAL (nunca
// erra — a ambiguidade resolve para meta, não para uma falha). Função pura: sem
// I/O, sem relógio, sem mapas — mesmo input ⇒ mesmo output, sempre.
//
// A ordem de avaliação é fixa e o primeiro predicado verdadeiro fixa a rota e a
// heurística:
//
//  1. `intake_mode == meta`            → META  (supervisão pedida explicitamente)
//  2. orçamento > tecto do tenant      → META  (sinal de meta; vence `simple` explícito)
//  3. tecto estrutural pedido > trivial → META  (idem)
//  4. cardinalidade de papéis > 1      → META  (organigrama, não 1 nó)
//  5. `intake_mode == simple`          → SIMPLE (pedido explícito, sem sinal de meta)
//  6. cardinalidade de papéis == 1     → SIMPLE (um papel: candidato a 1 nó)
//  7. caso restante (ambíguo)          → META  (fail-safe)
//
// Os passos 2–4 ANTES dos passos 5–6 são a defesa anti-gaming: declarar `simple`
// não anula um orçamento gigante, um tecto pedido nem multi-papel. Um `intake_mode`
// não reconhecido é tratado como não-declarado (cai no ramo ambíguo → META).
func Classify(s Signals, policy TenantPolicy) Result {
	mode := s.IntakeMode
	if !mode.valid() {
		mode = IntakeModeUnset // input inválido não vira rota `simple`: fail-safe
	}

	// (1) Supervisão pedida explicitamente.
	if mode == IntakeModeMeta {
		return Result{plannerevents.ClassificationMeta, HeuristicExplicitMeta}
	}

	// (2)–(4) Sinais de meta. Avaliados ANTES de honrar `simple` — anti-gaming.
	if s.RootBudgetMicroUSD > policy.SimpleBudgetCeilingMicroUSD {
		return Result{plannerevents.ClassificationMeta, HeuristicBudgetOverCeiling}
	}
	if s.RequestedMaxDepth > trivialCeiling || s.RequestedMaxFanout > trivialCeiling {
		return Result{plannerevents.ClassificationMeta, HeuristicCeilingRequested}
	}
	if s.RoleCardinality > 1 {
		return Result{plannerevents.ClassificationMeta, HeuristicMultiRole}
	}

	// (5)–(6) Sinais POSITIVOS de simplicidade (só chegam aqui sem sinal de meta).
	if mode == IntakeModeSimple {
		return Result{plannerevents.ClassificationSimple, HeuristicExplicitSimple}
	}
	if s.RoleCardinality == 1 {
		return Result{plannerevents.ClassificationSimple, HeuristicSingleRole}
	}

	// (7) Nada indica simples positivamente e nada força meta: ambíguo → META.
	return Result{plannerevents.ClassificationMeta, HeuristicAmbiguousMeta}
}

// ClassifyGoal é o açúcar sobre [Classify] que primeiro projecta os sinais
// declarativos de g (descartando o `objective`) e depois classifica. É o caminho
// que os chamadores usam; garante que o `objective` nunca chega a [Classify].
func ClassifyGoal(g Goal, policy TenantPolicy) Result {
	return Classify(g.Signals(), policy)
}
