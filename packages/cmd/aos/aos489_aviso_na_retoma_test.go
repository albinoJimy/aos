package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// AOS-489 — O AVISO DE REPETIÇÃO ATRAVESSA A APROVAÇÃO HUMANA.
//
// O aviso não é capturado nem gravado: é função das tool calls do run e dos seus desfechos, e
// cada hospedagem refá-lo ao percorrer o run desde o turno 1. Este teste põe-no no pior sítio
// para isso — o nó REAL do ciclo de aprovação (approval_cycle_node_test.go), em que CADA tool
// call é escalada, o run é suspenso, aprovado por uma cerimónia four-eyes e retomado:
//
//	turno 1..4: a MESMA chamada (mesma tool, mesmos argumentos); turno 5: conclui.
//
// São quatro suspensões e quatro retomas. Em cada hospedagem a última chamada sai ESCALADA (um
// desfecho diferente do das anteriores, já aprovadas e executadas), pelo que a série de três
// desfechos iguais só se completa na hospedagem em que a terceira chamada já vem aprovada. O
// que se exige:
//
//   - o aviso aparece pela primeira vez no prompt do turno 4, a seguir ao resultado da terceira
//     chamada, com o `ref` na primeira;
//   - na hospedagem SEGUINTE (depois de mais uma suspensão e retoma) está NO MESMO SÍTIO: o
//     prompt do turno 4 é, byte a byte, o princípio do prompt do turno 5;
//   - e só há um.
func TestAOS489_AvisoDeRepeticaoAtravessaAAprovacaoHumana(t *testing.T) {
	ctx := context.Background()
	h := newACNHarness(t)

	// O guião: a mesma chamada em quatro turnos, depois conclui. Guarda o prompt de cada turno
	// que chegou AO VIVO ao modelo (os reproduzidos da captura não chegam cá).
	var mu sync.Mutex
	vistos := map[int][]byte{}
	h.model.guiao = func(view agentruntime.PromptView) agentruntime.ModelResponse {
		mu.Lock()
		vistos[view.Turn] = append([]byte(nil), view.Materialized...)
		mu.Unlock()
		if view.Turn <= 4 {
			return acnCall("passo_um", "doc-a")
		}
		return agentruntime.ModelResponse{Text: "concluido", Final: true}
	}
	prompt := func(turno int) []byte {
		mu.Lock()
		defer mu.Unlock()
		return vistos[turno]
	}
	avisos := func(p []byte) int { return bytes.Count(p, []byte("\n<notice ")) }

	h.submete(t) // turno 1: escalada
	for ciclo, id := range []string{"req-489-1", "req-489-2", "req-489-3", "req-489-4"} {
		if _, susp := h.svc.Suspended(ctx, acnRunID); !susp {
			t.Fatalf("ciclo %d: o run devia estar SUSPENSO a espera de humano", ciclo+1)
		}
		p := h.aprovaPendente(t, id)
		if p.ToolID != "passo_um" || p.Turn != ciclo+1 {
			t.Fatalf("ciclo %d: o pendente devia ser a chamada do turno %d; veio %q turno %d", ciclo+1, ciclo+1, p.ToolID, p.Turn)
		}
		if err := h.svc.Resume(ctx, acnRunID, h.token(t)); err != nil {
			t.Fatalf("Resume (ciclo %d): %v", ciclo+1, err)
		}
		h.esperaFim(t, acnRunID)
	}
	fim, done := h.svc.Outcome(acnRunID)
	if !done || !fim.Result.Terminated {
		t.Fatalf("o run devia ter TERMINADO; done=%t res=%+v", done, fim.Result)
	}
	// A tool correu UMA vez por chamada aprovada — as retomas não repetiram efeitos.
	if got := h.execucoes("passo_um"); got != 4 {
		t.Fatalf("quatro chamadas aprovadas, quatro execucoes; correu %d", got)
	}

	// Turnos 2 e 3: ainda sem aviso (uma e duas chamadas com resultado).
	for _, turno := range []int{2, 3} {
		if p := prompt(turno); p == nil || avisos(p) != 0 {
			t.Fatalf("turno %d: prompt em falta ou com aviso antes da terceira chamada com o mesmo desfecho:\n%s", turno, p)
		}
	}

	// Turno 4, visto ao vivo na hospedagem em que a terceira chamada já vem aprovada: o aviso.
	p4, p5 := prompt(4), prompt(5)
	if p4 == nil || p5 == nil {
		t.Fatalf("faltam os prompts dos turnos 4 e 5 (4=%v 5=%v)", p4 != nil, p5 != nil)
	}
	const abre = "\n<tool_call taint=untrusted id="
	i := bytes.Index(p4, []byte(abre))
	if i < 0 {
		t.Fatalf("o prompt do turno 4 nao tem tool calls:\n%s", p4)
	}
	primeiroID := string(p4[i+len(abre):])
	primeiroID = primeiroID[:strings.IndexByte(primeiroID, ' ')]
	linha := "<notice taint=trusted ref=" + primeiroID + ">\n"
	if avisos(p4) != 1 || !bytes.Contains(p4, []byte("\n"+linha)) {
		t.Fatalf("o prompt do turno 4 devia ter UM aviso, com ref na primeira chamada (%s):\n%s", primeiroID, p4)
	}
	// …e é a ÚLTIMA coisa do tail: vem a seguir ao resultado da terceira chamada.
	if j := bytes.LastIndex(p4, []byte("\n<")); !bytes.HasPrefix(p4[j+1:], []byte(linha)) {
		t.Fatalf("o aviso devia ser o ultimo segmento do tail do turno 4:\n%s", p4)
	}
	if n := bytes.Count(p4, []byte("\n<tool_result ")); n != 3 {
		t.Fatalf("no turno 4 o tail devia ter 3 resultados, tem %d", n)
	}

	// Turno 5, visto na hospedagem SEGUINTE (mais uma suspensão, aprovação e retoma): o aviso
	// está no mesmo sítio, e não há outro pela quarta chamada.
	if !bytes.HasPrefix(p5, p4) {
		t.Fatalf("o prompt do turno 5 devia comecar, byte a byte, pelo do turno 4 — a retoma refez o tail de outra maneira:\n--- turno 4 ---\n%s\n--- turno 5 ---\n%s", p4, p5)
	}
	if avisos(p5) != 1 {
		t.Fatalf("o prompt do turno 5 devia continuar com UM aviso, tem %d:\n%s", avisos(p5), p5)
	}
}
