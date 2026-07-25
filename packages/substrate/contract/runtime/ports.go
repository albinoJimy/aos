package runtime

import (
	"context"

	rm "github.com/aos-ref/substrate/contract/rm"
)

// WindowSignal é o sinal de ocupação da janela na fronteira de fim-de-turno, num
// tipo kernel-local (o loop nunca importa platform/memory). O adaptador de
// produção mapeia-o de/para o sinal rico do pilar. O valor-zero (Triggered=false)
// é a resposta do [inlineWindow] default — sem pressão, sem compressão.
type WindowSignal struct {
	// Triggered indica que a ocupação cruzou o limiar de exaustão graciosa (~80%).
	Triggered bool
	// Action é o rótulo OPACO da acção recomendada ("", "mark_for_compression",
	// "escalate") — espelha working.ExhaustionAction sem importar o pilar.
	Action string
	// OccupancyTokens é a ocupação corrente em tokens (0 no default inline).
	OccupancyTokens int
	// LimitTokens é o limite de tokens do modelo para a janela (0 no default inline).
	LimitTokens int
}

// WindowFactory constrói o [WindowPort] POR RUN a partir dos inputs congelados do
// prefixo (run_id + system + tool set). É injectada via [WithWindowFactory]; o
// default ([defaultWindowFactory]) constrói um [inlineWindow] sobre [PromptAssembler].
type WindowFactory interface {
	// NewWindow congela o prefixo do run e devolve o gestor da janela. Fail-closed:
	// um erro aborta o run antes do primeiro turno (sem janela não há prompt a montar).
	NewWindow(runID, system string, tools []ToolSpec) (WindowPort, error)
}

// WindowPort é o DONO ÚNICO da janela de contexto de um run: o prefixo imutável
// (congelado na construção) e o tail append-only. O loop delega-lhe a posse do
// tail e a materialização da vista, pelo que há UM só assembler / prefix-hash por
// run — a resolução da decisão D-TAIL. NUNCA muta o prefixo (a estabilidade cache
// — ADR-009 — é estrutural). Não é seguro para uso concorrente pelo MESMO run
// (a hot path do loop é sequencial por run).
type WindowPort interface {
	// Append acrescenta um segmento ao tail append-only; nunca muta nem reordena o
	// prefixo. O loop chama-o para semear (memory/objective) e a cada turno
	// (history/tool_result/correction).
	Append(seg TailSegment)
	// Assemble materializa a vista cache-estável do turno (prefixo imutável ++ tail
	// serializado). O PrefixHash é byte-idêntico entre turnos do mesmo run. O ctx é
	// o do turno (para o adaptador ligar o seu span de janela à árvore invoke_agent);
	// o default inline ignora-o.
	Assemble(ctx context.Context, turn int) PromptView
	// SystemHash devolve sha256("<system>") no formato "sha256:<hex>" — o system_hash
	// que o manifesto por trajectória grava (ADR-010).
	SystemHash() string
	// Signal devolve o sinal de ocupação corrente (consumido pelo [CompactionTrigger]
	// na fronteira de fim-de-turno). O default inline devolve o valor-zero.
	Signal() WindowSignal
}

// CompactionTrigger observa o sinal de ocupação da janela na fronteira de fim-de-turno
// e pode enfileirar compressão assíncrona (AOS-043). É injectado via
// [WithCompactionTrigger]; o default ([noopCompactionTrigger]) nunca observa nem
// comprime. A compressão em si corre FORA do turno (num checkpoint), nunca na
// hot path — este ponto de ligação só ENTREGA o sinal.
type CompactionTrigger interface {
	// Observe reporta o sinal de fim-de-turno. Devolve se uma compactação foi
	// enfileirada (observacional) e um erro FATAL (fail-closed: aborta o run).
	Observe(ctx context.Context, runID string, turn int, sig WindowSignal) (bool, error)
}

// ActivityDispatcher despacha uma [rm.Call] JÁ construída pelo loop, podendo
// acrescentar idempotência/replay durável (AOS-021, step-ledger) à volta de
// Mediate. É injectado via [WithActivityDispatcher]; o default chama Mediate
// directamente.
//
// A porta recebe o Call COMPLETO — com o Credential (o token NHI, AOS-152) e o
// taint da autorização (AOS-069) que o loop preenche —, pelo que o despacho durável
// NUNCA perde a identidade nem degrada o taint.
type ActivityDispatcher interface {
	Dispatch(ctx context.Context, call rm.Call) (rm.Decision, error)
}
