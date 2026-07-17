package breaker

import "time"

// Composition governa como os sinais se COMBINAM para dar trip. É o "qualquer sinal OU
// uma composição configurável" da spec: por omissão QUALQUER sinal cruzado dispara;
// [CompositionAll] exige que TODOS os sinais LIGADOS estejam cruzados em simultâneo.
type Composition string

const (
	// CompositionAny — trip se ALGUM sinal ligado cruzar o limiar (o default, e o mais
	// fail-closed: na dúvida, interrompe).
	CompositionAny Composition = "any"
	// CompositionAll — trip só se TODOS os sinais LIGADOS (limiar > 0) cruzarem em
	// simultâneo. Útil para reduzir falsos-positivos exigindo corroboração.
	CompositionAll Composition = "all"
)

// Thresholds são os limiares CONFIGURÁVEIS de um breaker (por classe de agente — nunca
// constantes hard-coded). Um limiar <= 0 DESLIGA o respectivo sinal (não participa nem
// na composição). A comparação é `>=` (fail-closed: ATINGIR o limiar já dispara).
type Thresholds struct {
	// MaxCostMicroUSDPerSecond é o tecto de cost velocity (micro-USD/s). <=0 desliga.
	MaxCostMicroUSDPerSecond float64
	// MaxTokensPerSecond é o tecto de token velocity (tokens/s). <=0 desliga.
	MaxTokensPerSecond float64
	// MaxWallClock é o tecto de tempo ABSOLUTO em running. <=0 desliga. Cruzá-lo leva a
	// [state.TimedOut].
	MaxWallClock time.Duration
	// MaxStaleIterations é o tecto de iterações estéreis consecutivas (sem progresso).
	// <=0 desliga.
	MaxStaleIterations int
	// Composition é o modo de combinação dos sinais ([CompositionAny] por omissão — o
	// valor-zero "" resolve-se a any).
	Composition Composition
}

// ThresholdProvider resolve os limiares por CLASSE DE AGENTE. É a PORTA que torna os
// limiares configuráveis (não constantes); o [Breaker] recebe-os já resolvidos na
// construção. [StaticThresholdProvider] é a impl de referência.
type ThresholdProvider interface {
	// Thresholds devolve os limiares efectivos para uma classe de agente.
	Thresholds(class string) Thresholds
}

// StaticThresholdProvider é a impl de referência determinística do [ThresholdProvider]:
// um default global com overrides por classe. Sem I/O nem relógio — segura para replay.
// Molde do StaticThresholdProvider do breaker de orçamento (AOS-029), SEM o reusar (para
// não acoplar o runtime ao control-plane).
type StaticThresholdProvider struct {
	def     Thresholds
	byClass map[string]Thresholds
}

// NewStaticThresholdProvider constrói o provider com um default global (aplicado a
// qualquer classe sem override próprio).
func NewStaticThresholdProvider(def Thresholds) *StaticThresholdProvider {
	return &StaticThresholdProvider{def: def, byClass: make(map[string]Thresholds)}
}

// SetClass fixa limiares para uma classe de agente. Devolve o próprio provider para
// encadeamento fluente.
func (p *StaticThresholdProvider) SetClass(class string, t Thresholds) *StaticThresholdProvider {
	p.byClass[class] = t
	return p
}

// Thresholds resolve por especificidade: override de classe > default global.
func (p *StaticThresholdProvider) Thresholds(class string) Thresholds {
	if t, ok := p.byClass[class]; ok {
		return t
	}
	return p.def
}

// resolvedComposition normaliza o valor-zero de [Composition] para o default
// [CompositionAny].
func (t Thresholds) resolvedComposition() Composition {
	if t.Composition == CompositionAll {
		return CompositionAll
	}
	return CompositionAny
}
