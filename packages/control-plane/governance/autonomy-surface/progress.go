package autonomysurface

import (
	"fmt"

	"github.com/aos-ref/control-plane/governance/autonomy"
)

// ProgressToPromotion é o PROGRESSO legível rumo à próxima promoção (AC2): a
// fiabilidade MEDIDA vs o LIMIAR exigido pela policy-as-code de AOS-090, mais a fracção
// consumida e os critérios em texto. É DERIVADO da [ReliabilityReader] — a MESMA fonte
// que o Controller consulta — pelo que reflecte fielmente a métrica em que a decisão
// assenta. É LEITURA, nunca decisão: mostrar Fraction>=1.0 não promove — só sinaliza
// que o par cumpre o critério headline; a promoção continua a ser decidida pela
// política (AC4).
type ProgressToPromotion struct {
	// Measured é a fiabilidade SUSTENTADA medida (1 − override-rate) sobre a janela da
	// política — o sinal headline de maturidade (anti-rubber-stamping). Maior é melhor.
	Measured float64
	// Threshold é a fiabilidade sustentada EXIGIDA para promover (1 − override_rate_max
	// da policy-as-code). Measured >= Threshold cumpre o critério de override.
	Threshold float64
	// ErrorMeasured é a fiabilidade medida na dimensão de ERRO (1 − error-rate) sobre a
	// janela — a SEGUNDA condição que o gate de promoção de AOS-090 impõe. Maior é melhor.
	ErrorMeasured float64
	// ErrorThreshold é a fiabilidade de erro EXIGIDA (1 − error_rate_max da policy-as-code).
	ErrorThreshold float64
	// Fraction é o progresso normalizado GOVERNADO PELA RESTRIÇÃO VINCULATIVA: o MÍNIMO
	// das fracções de override e de erro (bounded, >= 0). >= 1.0 só quando AMBAS as
	// condições do gate de AOS-090 são cumpridas — reflecte fielmente o gate multi-
	// dimensional (não um único eixo optimista). A promoção continua a ser decidida pela
	// política (AC4); Fraction>=1.0 só sinaliza elegibilidade a SOLICITAR revisão.
	Fraction float64
	// WindowOK indica se a janela deslizante tem COBERTURA suficiente para julgar a
	// fiabilidade sustentada. false ⇒ ainda não há histórico bastante e a promoção é
	// conservadoramente NEGADA (nunca elegível por uma janela incompleta).
	WindowOK bool
	// Criteria é a descrição legível dos critérios de promoção (AC2): que métricas, que
	// limiares e que janela a política exige. Texto de apresentação, nunca segredo.
	Criteria string
}

// Criteria/legendas fixas do progresso (apresentação; sem segredos).
const (
	// criteriaAtCeiling descreve a ausência de próxima promoção no tecto/L5.
	criteriaAtCeiling = "nivel maximo por politica: sem proxima promocao"
	// criteriaNoSignal descreve a indisponibilidade do sinal de fiabilidade.
	criteriaNoSignal = "sinal de fiabilidade indisponivel: progresso nao computavel"
)

// computeProgress deriva o [ProgressToPromotion] rumo à promoção seguinte LENDO o sinal
// de fiabilidade de AOS-090 vs o limiar da policy-as-code (AC2). No tecto (Current >=
// promotionCeil) não há próximo nível; sem [ReliabilityReader] o progresso é
// indisponível. É LEITURA pura — não decide nem promove.
func (s *Surface) computeProgress(agent, domain string, current, ceil autonomy.Level) ProgressToPromotion {
	if current >= ceil {
		return ProgressToPromotion{Criteria: criteriaAtCeiling}
	}
	if s.rel == nil {
		return ProgressToPromotion{Criteria: criteriaNoSignal}
	}

	window := s.cfg.Window()
	rel := s.rel.Reliability(agent, domain, window)

	overrideMax := s.cfg.OverrideRateMax()
	measured := 1 - clamp01(rel.OverrideRate)
	threshold := 1 - clamp01(overrideMax)

	// SEGUNDA dimensão do gate de AOS-090: error-rate <= error_rate_max. A elegibilidade
	// exige AMBAS, logo a fracção-progresso é a MENOR das duas (a condição que ainda falta).
	errorMax := s.cfg.ErrorRateMax()
	errMeasured := 1 - clamp01(rel.ErrorRate)
	errThreshold := 1 - clamp01(errorMax)
	overallFraction := fraction(measured, threshold)
	if ef := fraction(errMeasured, errThreshold); ef < overallFraction {
		overallFraction = ef
	}

	return ProgressToPromotion{
		Measured:       measured,
		Threshold:      threshold,
		ErrorMeasured:  errMeasured,
		ErrorThreshold: errThreshold,
		Fraction:       overallFraction,
		WindowOK:       rel.WindowOK,
		Criteria: fmt.Sprintf(
			"promover %s->%s exige override_rate<=%.2f e error_rate<=%.2f sustentados por %s (policy v%s); fiabilidade medida %.2f vs limiar %.2f",
			current.String(), (current + 1).String(),
			overrideMax, s.cfg.ErrorRateMax(), window, s.cfg.Version(),
			measured, threshold,
		),
	}
}

// clamp01 restringe um valor ao domínio [0,1] (defesa contra uma fonte que devolva uma
// taxa fora do intervalo).
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// fraction calcula Measured/Threshold protegido: um limiar <= 0 é não-vinculativo
// (qualquer fiabilidade cumpre ⇒ 1.0); caso contrário devolve o rácio com piso em 0.
func fraction(measured, threshold float64) float64 {
	if threshold <= 0 {
		return 1.0
	}
	f := measured / threshold
	if f < 0 {
		return 0
	}
	return f
}
