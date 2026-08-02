package planneraut

// ---------------------------------------------------------------------------
// Contadores da janela e sinais derivados (função pura).
// ---------------------------------------------------------------------------

// Counters é a tália CRUA de uma janela de planeamento. São contagens medidas — o
// numerador/denominador de cada sinal —, não rates auto-declaradas. A taxa de
// override NÃO vive aqui: é autoritativa de AOS-095 e entra por porta
// ([OverrideRateSource]), fora do alcance do que o planeador pode fabricar.
type Counters struct {
	// Plans é o nº de decisões de planeamento na janela (denominador das rates
	// por-plano). É o tamanho de amostra: abaixo de [Config.MinSample] a janela não
	// rende juízo (nem promove nem demove).
	Plans int64
	// ApprovedNoEdit é o nº de planos aprovados com ZERO edições humanas (numerador
	// da taxa de aprovação sem edição — o sinal de que o planeador acerta à primeira).
	ApprovedNoEdit int64
	// Replans é o nº de re-planos despoletados (numerador da taxa de re-plano —
	// instabilidade/retrabalho).
	Replans int64
	// InvalidProposals é o nº de propostas recusadas pelo validador (AOS-231/232)
	// (numerador da taxa de propostas inválidas).
	InvalidProposals int64
	// CostSamples é o nº de amostras de calibração de custo (AOS-124). Denominador da
	// calibração; 0 = sem evidência (a calibração conta como sã, não penaliza).
	CostSamples int64
	// CostWithinTolerance é o nº de amostras cujo custo RE-PREÇADO caiu dentro da
	// tolerância da estimativa (AOS-124) (numerador da calibração de custo).
	CostWithinTolerance int64
	// PlanningUnits é o esforço (tokens/µ$/tempo — unidade estável) atribuído a
	// PLANEAR na janela. Numerador do SLI de fracção de planeamento.
	PlanningUnits int64
	// ExecutionUnits é o esforço atribuído a EXECUTAR. Planning+Execution é o
	// denominador do SLI.
	ExecutionUnits int64
}

// Signals é a projecção DERIVADA (rates em [0,1]) de uma janela. É o que o envelope
// avalia. Produzido por [ComputeSignals] — puro, determinístico, sem I/O nem relógio.
type Signals struct {
	// ApprovalNoEditRate — fracção de planos aprovados sem edição humana. PISO sadio
	// (abaixo do mínimo é anomalia).
	ApprovalNoEditRate float64
	// ReplanRate — re-planos por plano. TECTO sadio (acima do máximo é anomalia).
	ReplanRate float64
	// CostCalibration — fracção de estimativas de custo dentro de tolerância (AOS-124).
	// PISO sadio.
	CostCalibration float64
	// InvalidRate — fracção de propostas inválidas. TECTO sadio.
	InvalidRate float64
	// OverrideRate — taxa de override humano AUTORITATIVA de AOS-095 (porta). TECTO
	// sadio. É o ANCORAMENTO não-gameável da promoção.
	OverrideRate float64
	// PlanningFraction — SLI: fracção do esforço gasta a planear. TECTO sadio
	// ([DefaultMaxPlanningFraction]).
	PlanningFraction float64
}

// ratio devolve num/den, ou 0 se den<=0 (fail-safe: sem denominador não há rate).
func ratio(num, den int64) float64 {
	if den <= 0 {
		return 0
	}
	return float64(num) / float64(den)
}

// clamp01 confina x a [0,1]. Uma taxa de override reportada fora de [0,1] pela porta
// é um bug da fonte; confina-se fail-closed (um valor >1 vira 1 e continua a
// disparar o tecto de override — demove — em vez de passar despercebido).
func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// ComputeSignals deriva os [Signals] de uma janela a partir dos [Counters] e da taxa
// de override AUTORITATIVA (de AOS-095, por porta). PURA: mesmo input ⇒ mesmo output,
// sempre. A calibração de custo sem amostras conta como 1.0 (sã por ausência de
// evidência de má calibração — não penaliza um domínio que simplesmente não teve
// amostras de custo nesta janela).
func ComputeSignals(c Counters, overrideRate float64) Signals {
	cost := 1.0
	if c.CostSamples > 0 {
		cost = ratio(c.CostWithinTolerance, c.CostSamples)
	}
	return Signals{
		ApprovalNoEditRate: ratio(c.ApprovedNoEdit, c.Plans),
		ReplanRate:         ratio(c.Replans, c.Plans),
		CostCalibration:    cost,
		InvalidRate:        ratio(c.InvalidProposals, c.Plans),
		OverrideRate:       clamp01(overrideRate),
		PlanningFraction:   ratio(c.PlanningUnits, c.PlanningUnits+c.ExecutionUnits),
	}
}

// PlanningWithinSLI indica se a fracção de planeamento respeita o tecto do SLI.
func (s Signals) PlanningWithinSLI(maxFraction float64) bool {
	return s.PlanningFraction <= maxFraction
}

// ---------------------------------------------------------------------------
// Envelope de saúde e brechas.
// ---------------------------------------------------------------------------

// DefaultMaxPlanningFraction é o tecto do SLI de fracção de planeamento (5%,
// AOS-242). Exposto para o wiring e os testes.
const DefaultMaxPlanningFraction = 0.05

// Nomes ESTÁVEIS e content-free dos sinais (para brechas auditáveis sem arrastar
// conteúdo untrusted). Enum fechado de rótulos.
const (
	SignalApprovalNoEdit   = "approval_no_edit_rate"
	SignalReplan           = "replan_rate"
	SignalCostCalibration  = "cost_calibration"
	SignalInvalid          = "invalid_proposal_rate"
	SignalOverride         = "override_rate"
	SignalPlanningFraction = "planning_fraction"
)

// Direcção da brecha (abaixo de um piso vs. acima de um tecto).
const (
	DirectionBelowMin = "below_min"
	DirectionAboveMax = "above_max"
)

// Envelope declara os limites SADIOS de cada sinal. Um sinal fora do seu limite é
// uma anomalia que demove. Os pisos/tectos são propriedade de configuração
// (política), não do documento untrusted.
type Envelope struct {
	MinApprovalNoEditRate float64 // piso: aprovação sem edição não pode cair abaixo
	MaxReplanRate         float64 // tecto: re-planos por plano não podem subir acima
	MinCostCalibration    float64 // piso: calibração de custo (AOS-124)
	MaxInvalidRate        float64 // tecto: propostas inválidas
	MaxOverrideRate       float64 // tecto: override humano (AOS-095) — ancoramento
	MaxPlanningFraction   float64 // tecto: SLI de fracção de planeamento
}

// DefaultEnvelope devolve um envelope conservador de arranque, com o SLI de
// planeamento no tecto de 5%.
func DefaultEnvelope() Envelope {
	return Envelope{
		MinApprovalNoEditRate: 0.90,
		MaxReplanRate:         0.20,
		MinCostCalibration:    0.90,
		MaxInvalidRate:        0.05,
		MaxOverrideRate:       0.05,
		MaxPlanningFraction:   DefaultMaxPlanningFraction,
	}
}

// valid confere que o envelope é utilizável: o tecto do SLI tem de ser positivo
// (senão nenhuma fracção o respeita e a autonomia era inalcançável). Os restantes
// limites em [0,1] são política; não os re-inventamos aqui.
func (e Envelope) valid() bool { return e.MaxPlanningFraction > 0 }

// Breach descreve UM sinal fora do envelope. É content-free (só rótulos e números),
// admissível em audit.
type Breach struct {
	Signal    string
	Value     float64
	Bound     float64
	Direction string
}

// Evaluate devolve TODAS as brechas de s contra o envelope (vazio ⇒ janela sã). A
// ordem é fixa e determinística. Não curto-circuita: reporta todas as anomalias de
// uma vez para o audit.
func (e Envelope) Evaluate(s Signals) []Breach {
	var b []Breach
	if s.ApprovalNoEditRate < e.MinApprovalNoEditRate {
		b = append(b, Breach{SignalApprovalNoEdit, s.ApprovalNoEditRate, e.MinApprovalNoEditRate, DirectionBelowMin})
	}
	if s.ReplanRate > e.MaxReplanRate {
		b = append(b, Breach{SignalReplan, s.ReplanRate, e.MaxReplanRate, DirectionAboveMax})
	}
	if s.CostCalibration < e.MinCostCalibration {
		b = append(b, Breach{SignalCostCalibration, s.CostCalibration, e.MinCostCalibration, DirectionBelowMin})
	}
	if s.InvalidRate > e.MaxInvalidRate {
		b = append(b, Breach{SignalInvalid, s.InvalidRate, e.MaxInvalidRate, DirectionAboveMax})
	}
	if s.OverrideRate > e.MaxOverrideRate {
		b = append(b, Breach{SignalOverride, s.OverrideRate, e.MaxOverrideRate, DirectionAboveMax})
	}
	if s.PlanningFraction > e.MaxPlanningFraction {
		b = append(b, Breach{SignalPlanningFraction, s.PlanningFraction, e.MaxPlanningFraction, DirectionAboveMax})
	}
	return b
}
