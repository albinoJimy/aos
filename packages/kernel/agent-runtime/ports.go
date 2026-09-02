package agentruntime

import (
	"context"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// Este ficheiro define as PORTAS RT/RM que o composition root ápice liga aos concretos
// que importam este pacote (activity/engine/working/compression) — o idioma AOS-060
// (porta no kernel, adaptador no pilar), pelo qual o kernel NUNCA importa o pilar.
// Todas as portas têm um DEFAULT que reproduz o comportamento de AOS-013 byte-a-byte:
// sem adaptador ligado, o loop base corre exactamente como antes.
//
//   - WindowFactory/WindowPort (AOS-037) — o DONO ÚNICO do tail append-only e da
//     montagem cache-estável do prompt. Resolve a decisão D-TAIL: o loop delega a
//     posse do tail/assembly à porta, pelo que existe UM só PromptAssembler / um só
//     prefix-hash por run (o da porta), em vez de o loop e o WindowManager manterem
//     cada um o seu. O default ([inlineWindow]) É o PromptAssembler + tail inline que
//     o loop usava — byte-idêntico.
//   - CompactionTrigger (AOS-043) — observa o sinal de ocupação da janela na fronteira
//     de fim-de-turno e pode enfileirar compressão assíncrona. Default no-op.
//   - ActivityDispatcher (AOS-021) — despacha uma [referencemonitor.Call] JÁ construída
//     pelo loop (com Credential+taint), podendo acrescentar idempotência/replay durável
//     à volta de Mediate. Default = Mediate directo. Como a porta recebe o Call
//     completo, a identidade (AOS-152) e o taint da autorização (AOS-069) do loop nunca
//     se perdem — o dispatcher só ENVOLVE a mediação, nunca reconstrói o Call.

// ---------------------------------------------------------------------------
// AOS-037 — WindowFactory / WindowPort (dono único do tail/assembly, D-TAIL)
// ---------------------------------------------------------------------------

// A PORTA DE SINAL DE OCUPAÇÃO FOI REMOVIDA (AOS-298).
//
// Existiu aqui um `WindowSignal` (ocupação/limiar de exaustão), um método `Signal()` no
// [WindowPort] e um `CompactionTrigger` que o loop observava na fronteira de fim-de-turno. A
// cadeia inteira tinha ZERO chamadores de produção — e não só a porta: o `EvictToTailBudget`
// que aliviaria a janela, o `EvictionSink` que preservaria o despejado, e o `RunCheckpoint`
// que drenaria a fila do gatilho, todos a zero ao mesmo tempo. O sinal atravessava quatro
// camadas e terminava num `append` a um slice que ninguém consumia: expunha pressão e não a
// aliviava.
//
// O QUE A REMOÇÃO FECHA, e é a razão de ser de AOS-298: a eviction era **inalcançável** pela
// porta ([WindowPort] declarava quatro métodos e nenhum era eviction), mas se alguém a ligasse,
// o motor de replay dobraria o tail integralmente a partir da captura, sem notícia de que
// segmentos saíram da vista — e a divergência sairia como `prompt_hash`, INATRIBUÍVEL. Enquanto
// a porta existisse ligada-mas-inerte, esse era um defeito à espera de um chamador.
//
// A [WindowFactory] e o resto do [WindowPort] FICAM: têm prova de equivalência byte-a-byte com
// o caminho inline (`integration.TestWindowManagerFactory_ByteIdenticalToInline`), que é o
// contrato de D-TAIL. O que saiu foi o sinal, não a posse do tail.

// WindowFactory constrói o [WindowPort] POR RUN a partir dos inputs congelados do
// prefixo (run_id + system + tool set). É injectada via [WithWindowFactory]; o default
// ([defaultWindowFactory]) constrói um [inlineWindow] sobre [PromptAssembler] — o
// comportamento exacto de AOS-013.
type WindowFactory interface {
	// NewWindow congela o prefixo do run e devolve o gestor da janela. Fail-closed: um
	// erro aborta o run antes do primeiro turno (sem janela não há prompt a montar).
	NewWindow(runID, system string, tools []ToolSpec) (WindowPort, error)
}

// WindowPort é o DONO ÚNICO da janela de contexto de um run: o prefixo imutável
// (congelado na construção) e o tail append-only. O loop delega-lhe a posse do tail e
// a materialização da vista, pelo que há UM só assembler / prefix-hash por run — a
// resolução da decisão D-TAIL. NUNCA muta o prefixo (a estabilidade cache — ADR-009 —
// é estrutural). Não é seguro para uso concorrente pelo MESMO run (a hot path do loop
// é sequencial por run).
type WindowPort interface {
	// Append acrescenta um segmento ao tail append-only; nunca muta nem reordena o
	// prefixo. O loop chama-o para semear (memory/objective) e a cada turno (history/
	// tool_result/correction).
	Append(seg TailSegment)
	// Assemble materializa a vista cache-estável do turno (prefixo imutável ++ tail
	// serializado). O PrefixHash é byte-idêntico entre turnos do mesmo run. O ctx é o do
	// turno (para o adaptador ligar o seu span de janela à árvore invoke_agent); o
	// default inline ignora-o.
	Assemble(ctx context.Context, turn int) PromptView
	// SystemHash devolve sha256("<system>") no formato "sha256:<hex>" — o system_hash
	// que o manifesto por trajectória grava (ADR-010).
	SystemHash() string
}

// inlineWindow é o [WindowPort] DEFAULT: o [PromptAssembler] + tail inline que o loop
// base usava antes de AOS-157. Produz bytes IDÊNTICOS ao loop original (mesmo
// assembler, mesmo tail, mesma ordem) — a garantia de AOS-013 byte-a-byte quando
// nenhuma WindowFactory está ligada.
type inlineWindow struct {
	asm  *PromptAssembler
	tail []TailSegment
}

func (w *inlineWindow) Append(seg TailSegment) { w.tail = append(w.tail, seg) }
func (w *inlineWindow) Assemble(_ context.Context, turn int) PromptView {
	return w.asm.Assemble(turn, w.tail)
}
func (w *inlineWindow) SystemHash() string { return w.asm.SystemHash() }

// defaultWindowFactory constrói um [inlineWindow] — o comportamento AOS-013.
type defaultWindowFactory struct{}

func (defaultWindowFactory) NewWindow(_ /*runID*/, system string, tools []ToolSpec) (WindowPort, error) {
	return &inlineWindow{asm: NewPromptAssembler(system, tools)}, nil
}

var _ WindowPort = (*inlineWindow)(nil)
var _ WindowFactory = defaultWindowFactory{}

// ---------------------------------------------------------------------------
// AOS-021 — ActivityDispatcher (despacho durável idempotente do efeito)
// ---------------------------------------------------------------------------

// ActivityDispatcher despacha uma [referencemonitor.Call] JÁ construída pelo loop,
// podendo acrescentar idempotência/replay durável (AOS-021, step-ledger) à volta de
// Mediate. É injectado via [WithActivityDispatcher]; o default ([directDispatcher])
// chama Mediate directamente (byte-idêntico AOS-013).
//
// A porta recebe o Call COMPLETO — com o Credential (o token NHI, AOS-152) e o taint da
// autorização (AOS-069) que o loop preenche —, pelo que o despacho durável NUNCA perde
// a identidade nem degrada o taint. O contrato de retorno é o do RM ([Decision]): o
// adaptador de um dispatcher com semântica de deny-como-erro (activity.Dispatcher)
// traduz esse erro numa Decision de Deny para o loop o materializar no tail como antes.
type ActivityDispatcher interface {
	Dispatch(ctx context.Context, call referencemonitor.Call) (referencemonitor.Decision, error)
}

// directDispatcher é o default: Mediate directo sobre o Reference Monitor do runtime —
// o MESMO caminho de despacho de AOS-013 (o span execute_tool continua a abrir dentro
// de Mediate, o ponto único de mediação).
type directDispatcher struct {
	rm *referencemonitor.Monitor
}

func (d directDispatcher) Dispatch(ctx context.Context, call referencemonitor.Call) (referencemonitor.Decision, error) {
	return d.rm.Mediate(ctx, call)
}

var _ ActivityDispatcher = directDispatcher{}

// ---------------------------------------------------------------------------
// Options
// ---------------------------------------------------------------------------

// WithWindowFactory injecta a fábrica da janela de contexto (AOS-037). Um valor nil é
// ignorado (mantém o default [inlineWindow] byte-idêntico).
func WithWindowFactory(f WindowFactory) Option {
	return func(rt *Runtime) {
		if f != nil {
			rt.windowFactory = f
		}
	}
}

// WithActivityDispatcher injecta o dispatcher de efeitos durável (AOS-021). Um valor
// nil é ignorado (mantém o default [directDispatcher] = Mediate directo).
func WithActivityDispatcher(d ActivityDispatcher) Option {
	return func(rt *Runtime) {
		if d != nil {
			rt.dispatcher = d
		}
	}
}
