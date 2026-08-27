package otelgenai

// A AMOSTRA DO SLI — qual dos dois `execute_tool` conta, e o que isso NÃO resolve.
//
// `execute_tool` é emitido por DOIS produtores, e o segundo é filho do primeiro:
//
//	worker  (agent-runtime/worker) — gate fenced + ledger.Apply(mediação + despacho)
//	monitor (reference-monitor)    — cadeia de política + DESPACHO da tool
//
// AMBOS incluem a execução da tool: [Monitor.evaluate] chama `m.dispatch` antes de devolver a
// decisão. O filtro da decisão escolhe a JANELA MAIS ESTREITA — a do monitor, que ao menos
// exclui o gate fenced e o ledger de idempotência — mas o SLI continua a NÃO medir overhead de
// mediação, e o SLO de 15ms continua inalcançável com tráfego real. É DEF-281.
//
// MEDIDO EM PRODUÇÃO a 2026-08-27, com o filtro activo: uma amostra, `3,047s`. Antes: duas
// amostras, `4,017s`.
//
// Estes testes trancam o que o filtro FAZ — escolher uma população e não duas — e não uma
// promessa sobre o que o número significa. O discriminador é a DECISÃO: [Monitor.Mediate]
// anota-a em todos os caminhos de retorno e a cadeia é fail-closed; o span do worker nunca a
// anota, nem o do Launcher (EPIC-07), que também usa `OpExecuteTool`.

import (
	"testing"
	"time"
)

// spanDoMonitor é o span do Reference Monitor: decidiu. A sua latência inclui o despacho — é a
// janela mais estreita disponível, NÃO o overhead de mediação (DEF-281).
func spanDoMonitor(trace string, d time.Duration) WideEvent {
	return WideEvent{Operation: OpExecuteTool, TraceIDHex: trace, Decision: DecisionPermit, LatencyNanos: int64(d)}
}

// spanDoWorker é o span exterior: mesma operação, SEM decisão, e a sua latência inclui a
// execução do efeito. Não é overhead de mediação e não pode entrar na amostra.
func spanDoWorker(trace string, d time.Duration) WideEvent {
	return WideEvent{Operation: OpExecuteTool, TraceIDHex: trace, LatencyNanos: int64(d)}
}

// TestOverheadP95_ContaSoOSpanDoMonitor tranca a escolha da população: entre os DOIS spans com
// o mesmo nome, conta-se o do monitor — a janela mais estreita — e não o exterior do worker.
//
// Sem o filtro o percentil mistura duas populações e reporta a mais larga. NÃO afirma que 5ms
// seja overhead de mediação: em produção este span inclui o despacho (DEF-281).
func TestOverheadP95_ContaSoOSpanDoMonitor(t *testing.T) {
	tecto := int64(15 * time.Millisecond)
	eventos := []WideEvent{
		spanDoWorker("t1", 4017*time.Millisecond), // a tool a correr em gVisor
		spanDoMonitor("t1", 5*time.Millisecond),   // o span do monitor, dentro dele
	}

	sli := overheadP95SLI(eventos, tecto)

	if sli.Samples != 1 {
		t.Fatalf("a amostra tem de conter SÓ o span do monitor: %d amostras", sli.Samples)
	}
	if got := time.Duration(sli.Value); got != 5*time.Millisecond {
		t.Fatalf("p95 = %v — devia ser a latência do span do MONITOR (5ms), não a do span exterior do worker", got)
	}
	if !sli.Met {
		t.Fatalf("um span contado de 5ms CUMPRE um SLO de 15ms; o SLI diz que não (valor=%v)", time.Duration(sli.Value))
	}
}

// TestOverheadP95_ContinuaADispararQuandoOSpanContadoEstaLento é a metade que impede o filtro de
// se tornar um silenciador. O filtro remove ruído; não pode remover o SINAL.
//
// Sem este caso, `return sli` logo no início passaria os outros testes — um SLI que nunca
// dispara cumpre sempre.
func TestOverheadP95_ContinuaADispararQuandoOSpanContadoEstaLento(t *testing.T) {
	tecto := int64(15 * time.Millisecond)
	eventos := []WideEvent{
		spanDoWorker("t1", 30*time.Millisecond),
		spanDoMonitor("t1", 50*time.Millisecond), // o span CONTADO é que está lento
	}

	sli := overheadP95SLI(eventos, tecto)

	if sli.Met {
		t.Fatal("um span contado de 50ms VIOLA um SLO de 15ms — o SLI tinha de o dizer")
	}
	if len(sli.Offenders) == 0 {
		t.Fatal("um SLI degradado tem de nomear os trace_ids para o drill-down")
	}
}

// TestOverheadP95_SemSpansComDecisaoNaoEAvaliado: um nó cujo tráfego só produziu spans exteriores não
// tem nada para reportar. `Samples == 0` é o que faz o rótulo `avaliavel="0"`
// dizer a verdade — inventar um p95 a partir de spans sem decisão seria pior que não medir.
func TestOverheadP95_SemSpansComDecisaoNaoEAvaliado(t *testing.T) {
	sli := overheadP95SLI([]WideEvent{spanDoWorker("t1", time.Second)}, int64(15*time.Millisecond))

	if sli.Samples != 0 {
		t.Fatalf("sem spans com decisão não há amostra: %d", sli.Samples)
	}
	if sli.Value != 0 {
		t.Fatalf("sem amostra o valor tem de ficar em zero, não inventado: %v", sli.Value)
	}
}
