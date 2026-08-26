package otelgenai

// OVERHEAD DE MEDIAÇÃO — o SLI tem de medir a MEDIAÇÃO, não a tool call inteira.
//
// `execute_tool` deixou de identificar uma coisa só. Há DOIS spans com esse nome, e o segundo
// é filho do primeiro:
//
//	worker  (agent-runtime/worker) — gate fenced + ledger.Apply(mediação + EXECUÇÃO DO EFEITO)
//	monitor (reference-monitor)    — só a mediação: fecha quando Mediate devolve o veredicto
//
// Cada um projecta o seu próprio [WideEvent]. Como isto é um PERCENTIL 95, contar os dois faz
// o valor ser dominado pela população mais lenta — a do worker, que inclui a execução em
// sandbox. O SLO de 15ms está calibrado para o custo da MEDIAÇÃO, pelo que o alerta `critical`
// (RB-04) disparava sempre que uma tool call real acontecia, por construção.
//
// O discriminador é a DECISÃO: [Monitor.Mediate] anota-a em todos os caminhos de retorno e a
// cadeia é fail-closed; o span do worker nunca a anota, nem o do Launcher (EPIC-07), que também
// usa `OpExecuteTool`.

import (
	"testing"
	"time"
)

// spanDeMediacao é o span do Reference Monitor: decidiu, e a sua latência É o overhead.
func spanDeMediacao(trace string, d time.Duration) WideEvent {
	return WideEvent{Operation: OpExecuteTool, TraceIDHex: trace, Decision: DecisionPermit, LatencyNanos: int64(d)}
}

// spanDoWorker é o span exterior: mesma operação, SEM decisão, e a sua latência inclui a
// execução do efeito. Não é overhead de mediação e não pode entrar na amostra.
func spanDoWorker(trace string, d time.Duration) WideEvent {
	return WideEvent{Operation: OpExecuteTool, TraceIDHex: trace, LatencyNanos: int64(d)}
}

// TestOverheadP95_IgnoraOSpanQueExecutaOEfeito é o caso que motivou a correcção, com os números
// que se mediram em produção a 2026-08-26: uma tool call cujo efeito demorou ~4s e cuja mediação
// demorou milissegundos.
//
// Sem o filtro, o p95 reporta o tempo do EFEITO e o SLO de 15ms é violado por duas ordens de
// grandeza — um `critical` garantido em qualquer nó que sirva tráfego real.
func TestOverheadP95_IgnoraOSpanQueExecutaOEfeito(t *testing.T) {
	tecto := int64(15 * time.Millisecond)
	eventos := []WideEvent{
		spanDoWorker("t1", 4017*time.Millisecond), // a tool a correr em gVisor
		spanDeMediacao("t1", 5*time.Millisecond),  // a mediação dentro dela
	}

	sli := overheadP95SLI(eventos, tecto)

	if sli.Samples != 1 {
		t.Fatalf("a amostra tem de conter SÓ a mediação: %d amostras", sli.Samples)
	}
	if got := time.Duration(sli.Value); got != 5*time.Millisecond {
		t.Fatalf("p95 = %v — devia ser a latência da MEDIAÇÃO (5ms), não a da tool call inteira", got)
	}
	if !sli.Met {
		t.Fatalf("uma mediação de 5ms CUMPRE um SLO de 15ms; o SLI diz que não (valor=%v)", time.Duration(sli.Value))
	}
}

// TestOverheadP95_ContinuaADispararQuandoAMediacaoEstaLenta é a metade que impede a correcção de
// se tornar um silenciador. O filtro remove ruído; não pode remover o SINAL.
//
// Sem este caso, `return sli` logo no início passaria os outros testes — um SLI que nunca
// dispara cumpre sempre.
func TestOverheadP95_ContinuaADispararQuandoAMediacaoEstaLenta(t *testing.T) {
	tecto := int64(15 * time.Millisecond)
	eventos := []WideEvent{
		spanDoWorker("t1", 30*time.Millisecond),
		spanDeMediacao("t1", 50*time.Millisecond), // a MEDIAÇÃO é que está lenta
	}

	sli := overheadP95SLI(eventos, tecto)

	if sli.Met {
		t.Fatal("uma mediação de 50ms VIOLA um SLO de 15ms — o SLI tinha de o dizer")
	}
	if len(sli.Offenders) == 0 {
		t.Fatal("um SLI degradado tem de nomear os trace_ids para o drill-down")
	}
}

// TestOverheadP95_SemMediacoesNaoEAvaliado: um nó cujo tráfego só produziu spans exteriores não
// tem overhead de mediação para reportar. `Samples == 0` é o que faz o rótulo `avaliavel="0"`
// dizer a verdade — inventar um p95 a partir de spans que não mediaram seria pior que não medir.
func TestOverheadP95_SemMediacoesNaoEAvaliado(t *testing.T) {
	sli := overheadP95SLI([]WideEvent{spanDoWorker("t1", time.Second)}, int64(15*time.Millisecond))

	if sli.Samples != 0 {
		t.Fatalf("sem mediações não há amostra: %d", sli.Samples)
	}
	if sli.Value != 0 {
		t.Fatalf("sem amostra o valor tem de ficar em zero, não inventado: %v", sli.Value)
	}
}
