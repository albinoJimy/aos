package trajectorysurface

import (
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// EvalLink é a ligação de NAVEGAÇÃO de um span à avaliação (gen_ai.evaluation.result)
// da sua trajectória, QUANDO disponível (AC5). É uma REFERÊNCIA — o veredicto/score já
// calculado por AOS-084 —, nunca um recálculo do eval. Available=false quando não há
// eval ligada (não se inventa).
type EvalLink struct {
	// Available indica se existe uma eval ligada à trajectória deste span.
	Available bool
	// EvalID é o identificador da execução de avaliação (aos.eval.id).
	EvalID string
	// Suite é a suite/classe de artefacto avaliada (aos.eval.suite).
	Suite string
	// Verdict é o veredicto pass|fail já apurado pela avaliação.
	Verdict string
	// Score é a métrica numérica associada ao veredicto.
	Score float64
	// Dataset é a origem do dataset avaliado (golden|failure_derived).
	Dataset string
}

// ReplayLink é a ligação de NAVEGAÇÃO de um span ao replay determinista da sua
// trajectória (AOS-079), QUANDO disponível (AC5). É uma REFERÊNCIA opaca (ex. o
// handle/URL do replay), nunca uma re-execução do replay aqui. Available=false quando
// não há replay disponível.
type ReplayLink struct {
	// Available indica se existe um replay referenciável para a trajectória.
	Available bool
	// Ref é a referência opaca ao replay (handle/URL); "" se indisponível.
	Ref string
}

// EvalLinkSource é a PORTA (opcional) que, dado o trace_id de uma trajectória,
// devolve o [otelgenai.EvaluationResult] já registado por AOS-084, se existir. O
// adaptador concreto (leitura do backend de spans/eval) é wiring de deployment; aqui
// só a porta. NUNCA corre a avaliação — só a LOCALIZA.
type EvalLinkSource interface {
	// EvalFor devolve o resultado da eval ligada a traceIDHex e ok=true, ou ok=false
	// se não há eval para essa trajectória.
	EvalFor(traceIDHex string) (otelgenai.EvaluationResult, bool)
}

// ReplayLinkSource é a PORTA (opcional) que, dado o trace_id, devolve a referência de
// replay da trajectória, se existir. NUNCA re-executa o replay — só o LOCALIZA.
type ReplayLinkSource interface {
	// ReplayFor devolve a referência de replay para traceIDHex e ok=true, ou ok=false.
	ReplayFor(traceIDHex string) (string, bool)
}

// LinkEval devolve a [EvalLink] de NAVEGAÇÃO de node à eval da sua trajectória (AC5),
// consultando a porta. Sem porta (nil), sem nó, ou sem eval registada => Available
// false (não inventa). É leitura pura: NÃO recalcula o eval — projecta o
// [otelgenai.EvaluationResult] já apurado.
func LinkEval(node *SpanNode, port EvalLinkSource) EvalLink {
	if node == nil || port == nil {
		return EvalLink{}
	}
	res, ok := port.EvalFor(node.Span.SpanContext.TraceIDHex())
	if !ok {
		return EvalLink{}
	}
	return EvalLink{
		Available: true,
		EvalID:    res.EvalID,
		Suite:     res.Suite,
		Verdict:   string(res.Verdict),
		Score:     res.Score,
		Dataset:   string(res.Dataset),
	}
}

// LinkReplay devolve a [ReplayLink] de NAVEGAÇÃO de node ao replay da sua trajectória
// (AC5), consultando a porta. Sem porta (nil), sem nó, ou sem replay => Available
// false. NÃO re-executa o replay — só o referencia.
func LinkReplay(node *SpanNode, port ReplayLinkSource) ReplayLink {
	if node == nil || port == nil {
		return ReplayLink{}
	}
	ref, ok := port.ReplayFor(node.Span.SpanContext.TraceIDHex())
	if !ok || ref == "" {
		return ReplayLink{}
	}
	return ReplayLink{Available: true, Ref: ref}
}
