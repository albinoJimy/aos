package replay

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-493 — TESTE DIFERENCIAL DO VEREDICTO: o motor de replay chega ao MESMO desfecho que o
// loop, e os dois ao que a tabela diz.
//
// O diferencial do AOS-492 garante que os dois PARAM no mesmo turno. Este garante que dão ao
// run o mesmo DESFECHO: concluído ou não cumprido, com que razão e com que vector. Corre o loop
// real (Reference Monitor, captura), reproduz o log com o motor, e compara os dois um com o
// outro e com o valor esperado — sem o valor esperado, um erro igual dentro de
// [agentruntime.ConcludeRun] passava.
//
// As recusas e as falhas são reais: a recusa é a da lista-branca do run, imposta pelo
// Reference Monitor; a falha é a da tool `falha` da bancada.

// aos493Caso é um guião, o contrato do run e o veredicto que o kernel lhe dá.
type aos493Caso struct {
	nome     string
	guiao    []agentruntime.ModelResponse
	contrato []string
	// permitidas é a lista-branca do run; nil ⇒ sem restrição.
	permitidas []string
	turnos     int
	razao      agentruntime.OutcomeReason
	vector     []agentruntime.ToolEvidence
	pedidas    int
}

// aos493RespostasDeProducao lê as duas respostas de produção de 2026-10-04.
func aos493RespostasDeProducao(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, plano := range []string{"plan-e2e-v0145-1791115918", "plan-e2e-v0145-1791117087"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "aos493_producao", plano+".txt"))
		if err != nil {
			t.Fatalf("fixture de producao: %v", err)
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			t.Fatalf("a fixture %s esta vazia: o caso passaria a medir a saida vazia e nao o contrato", plano)
		}
		out[plano] = string(raw)
	}
	return out
}

func aos493Casos(t *testing.T) []aos493Caso {
	uso := agentruntime.Usage{InputTokens: 10, OutputTokens: 5}
	echo := func(in string) agentruntime.ToolInvocation {
		return agentruntime.ToolInvocation{ToolID: "echo", Capability: "cap:echo", ResourceType: "doc", ResourceValue: "notes", Input: []byte(in)}
	}
	falha := agentruntime.ToolInvocation{ToolID: "falha", Capability: "cap:echo", Input: []byte(`{"url":"http://exemplo.invalid"}`)}
	pede := func(texto string, calls ...agentruntime.ToolInvocation) agentruntime.ModelResponse {
		return agentruntime.ModelResponse{Text: texto, ToolCalls: calls, StopReason: agentruntime.StopToolCalls, Usage: uso}
	}
	fim := func(texto string) agentruntime.ModelResponse {
		return agentruntime.ModelResponse{Text: texto, Final: true, StopReason: agentruntime.StopStop, Usage: uso}
	}
	casos := []aos493Caso{
		{
			nome:  "sem contrato, texto final",
			guiao: []agentruntime.ModelResponse{fim("concluido")}, turnos: 1,
		},
		{
			nome:     "contrato cumprido por uma chamada efectiva",
			guiao:    []agentruntime.ModelResponse{pede("vou ler", echo(`{"doc_id":"notes"}`)), fim("lido")},
			contrato: []string{"echo"}, turnos: 2, pedidas: 1,
			vector: []agentruntime.ToolEvidence{{Tool: "echo", Requested: 1, Effective: 1, Last: agentruntime.ToolOutcomeEffective}},
		},
		{
			// A tool exigida foi pedida e o Reference Monitor recusou-a (fora da lista-branca do
			// run). Uma chamada recusada não é efectiva.
			nome:     "contrato por cumprir depois de uma recusa",
			guiao:    []agentruntime.ModelResponse{pede("vou ler", echo(`{"doc_id":"notes"}`)), fim("nao consegui ler")},
			contrato: []string{"echo"}, permitidas: []string{"falha"}, turnos: 2, pedidas: 1,
			razao:  agentruntime.OutcomeContractAfterDenial,
			vector: []agentruntime.ToolEvidence{{Tool: "echo", Requested: 1, Denied: 1, Last: agentruntime.ToolOutcomeDenied}},
		},
		{
			// A tool exigida foi permitida e a execução falhou. Uma chamada falhada não é efectiva.
			nome:     "contrato por cumprir depois de uma falha de tool",
			guiao:    []agentruntime.ModelResponse{pede("vou tentar", falha), fim("a tool falhou")},
			contrato: []string{"falha"}, turnos: 2, pedidas: 1,
			razao:  agentruntime.OutcomeContractAfterToolError,
			vector: []agentruntime.ToolEvidence{{Tool: "falha", Requested: 1, Failed: 1, Last: agentruntime.ToolOutcomeFailed}},
		},
		{
			// O vector é POR TOOL: uma tool cumprida não absolve a outra.
			nome:     "duas tools exigidas, so uma chamada",
			guiao:    []agentruntime.ModelResponse{pede("uma", echo(`{"n":1}`)), fim("feito")},
			contrato: []string{"echo", "falha"}, turnos: 2, pedidas: 1,
			razao: agentruntime.OutcomeContractNoCall,
			vector: []agentruntime.ToolEvidence{
				{Tool: "echo", Requested: 1, Effective: 1, Last: agentruntime.ToolOutcomeEffective},
				{Tool: "falha"},
			},
		},
		{
			// A falha de uma tool que o contrato não exige não o descumpre.
			nome:     "contrato cumprido e outra tool falhada no mesmo turno",
			guiao:    []agentruntime.ModelResponse{pede("uma", falhaDepois(echo(`{"n":1}`), falha)...), fim("feito")},
			contrato: []string{"echo"}, turnos: 2, pedidas: 2,
			vector: []agentruntime.ToolEvidence{{Tool: "echo", Requested: 1, Effective: 1, Last: agentruntime.ToolOutcomeEffective}},
		},
		{
			nome:   "resposta cortada, sem contrato",
			guiao:  []agentruntime.ModelResponse{{Text: "cortado a mei", StopReason: agentruntime.StopLength, Usage: uso}},
			turnos: 1, razao: agentruntime.OutcomeTruncated,
		},
		{
			// O corte vem antes do contrato na razão; o vector continua a dizer o que falta.
			nome:     "resposta cortada com contrato por cumprir",
			guiao:    []agentruntime.ModelResponse{{Text: "ia chamar a", StopReason: agentruntime.StopLength, Usage: uso}},
			contrato: []string{"echo"}, turnos: 1, razao: agentruntime.OutcomeTruncated,
			vector: []agentruntime.ToolEvidence{{Tool: "echo"}},
		},
		{
			nome:   "saida vazia, sem contrato",
			guiao:  []agentruntime.ModelResponse{{Final: true, StopReason: agentruntime.StopStop, Usage: uso}},
			turnos: 1, razao: agentruntime.OutcomeEmptyOutput,
		},
		{
			// Um motivo de paragem fora do mapa conhecido NÃO é veredicto negativo.
			nome:   "motivo de paragem desconhecido, com texto",
			guiao:  []agentruntime.ModelResponse{{Text: "acabei", StopReason: agentruntime.StopOther, Usage: uso}},
			turnos: 1,
		},
	}
	for plano, texto := range aos493RespostasDeProducao(t) {
		casos = append(casos, aos493Caso{
			nome:     "producao " + plano,
			guiao:    []agentruntime.ModelResponse{fim(texto)},
			contrato: []string{"echo"}, turnos: 1,
			razao:  agentruntime.OutcomeContractNoCall,
			vector: []agentruntime.ToolEvidence{{Tool: "echo"}},
		})
	}
	return casos
}

// falhaDepois devolve as invocações pela ordem dada (legibilidade da tabela).
func falhaDepois(calls ...agentruntime.ToolInvocation) []agentruntime.ToolInvocation { return calls }

func TestAOS493_Diferencial_OReplayChegaAoVeredictoDoLoop(t *testing.T) {
	versoes := []string{agentruntime.AssemblyVersion130, agentruntime.AssemblyVersion140}
	if got := agentruntime.SupportedAssemblyVersions(); len(got) != len(versoes) {
		t.Fatalf("o assembler monta %v e este teste so cobre %v: um layout novo tem de entrar na tabela", got, versoes)
	}
	modos := []agentruntime.CompletionMode{agentruntime.CompletionObserve, agentruntime.CompletionEnforce}
	for v, versao := range versoes {
		for m, modo := range modos {
			for i, c := range aos493Casos(t) {
				t.Run(versao+"/"+string(modo)+"/"+c.nome, func(t *testing.T) {
					b := novaBancada(t)
					goal := aos489Goal("run-aos493-dif-" + itoa(v) + "-" + itoa(m) + "-" + itoa(i))
					goal.AssemblyVersion = versao
					goal.AllowedTools = c.permitidas
					goal.CompletionRequires = c.contrato
					goal.CompletionMode = modo

					// (1) O LOOP real, com captura.
					doLoop, err := b.correr(goal, c.guiao)
					if err != nil {
						t.Fatalf("Run: %v", err)
					}
					negativo := c.razao != agentruntime.OutcomeFulfilled
					imposto := negativo && modo == agentruntime.CompletionEnforce
					texto := c.guiao[len(c.guiao)-1].Text
					if imposto {
						texto = ""
					}
					quer := &agentruntime.Verdict{
						Mode: modo, Fulfilled: !negativo, Reason: c.razao,
						Tools: c.vector, ToolCallsRequested: c.pedidas,
					}
					if doLoop.Turns != c.turnos {
						t.Fatalf("loop: %d turno(s), queria %d", doLoop.Turns, c.turnos)
					}
					if doLoop.Unfulfilled != imposto || doLoop.Terminated != !imposto {
						t.Fatalf("loop: Unfulfilled=%v Terminated=%v; com veredicto %q em modo %s queria Unfulfilled=%v Terminated=%v",
							doLoop.Unfulfilled, doLoop.Terminated, c.razao, modo, imposto, !imposto)
					}
					if doLoop.FinalText != texto {
						t.Fatalf("loop: texto final %q, queria %q", doLoop.FinalText, texto)
					}
					if !reflect.DeepEqual(doLoop.Verdict, quer) {
						t.Fatalf("loop: veredicto\n  %+v\nqueria\n  %+v", doLoop.Verdict, quer)
					}
					if doLoop.ToolCallsRequested != c.pedidas {
						t.Fatalf("loop: %d tool call(s) pedida(s), queria %d", doLoop.ToolCallsRequested, c.pedidas)
					}

					// O manifesto de CADA turno grava o modo e o contrato: é dele que o motor os lê.
					for n, mf := range manifestosDe(t, b.eventos(goal.RunID)) {
						if mf.Completion == nil || mf.Completion.Mode != modo || !reflect.DeepEqual(mf.Completion.Requires, c.contrato) {
							t.Fatalf("turno %d: manifest.completion = %+v, queria modo %s e contrato %v", n+1, mf.Completion, modo, c.contrato)
						}
					}

					// (2) O MOTOR DE REPLAY sobre o log que o loop gravou.
					doReplay, err := b.motor().Replay(context.Background(), goal.RunID, Options{Spec: aos489SpecDe(goal)})
					exigirFiel(t, doReplay, err, c.turnos)

					// (3) OS DOIS DIZEM O MESMO.
					if doReplay.Terminated != doLoop.Terminated || doReplay.Unfulfilled != doLoop.Unfulfilled {
						t.Fatalf("desfecho: replay Terminated=%v Unfulfilled=%v; loop Terminated=%v Unfulfilled=%v",
							doReplay.Terminated, doReplay.Unfulfilled, doLoop.Terminated, doLoop.Unfulfilled)
					}
					if doReplay.FinalText != doLoop.FinalText {
						t.Fatalf("texto final: replay=%q loop=%q", doReplay.FinalText, doLoop.FinalText)
					}
					if !reflect.DeepEqual(doReplay.Verdict, doLoop.Verdict) {
						t.Fatalf("veredicto:\n  replay %+v\n  loop   %+v", doReplay.Verdict, doLoop.Verdict)
					}
				})
			}
		}
	}
}

// O MOTOR USA O MODO GRAVADO, NÃO UM MODO SEU. O mesmo guião e o mesmo contrato, corridos uma
// vez em observação e outra em imposição, reproduzem-se cada um com o desfecho que teve — pelo
// mesmo motor, sem lhe dizer modo nenhum.
func TestAOS493_OReplayUsaOModoQueORunGravou(t *testing.T) {
	texto := aos493RespostasDeProducao(t)["plan-e2e-v0145-1791115918"]
	guiao := []agentruntime.ModelResponse{{Text: texto, Final: true, StopReason: agentruntime.StopStop, Usage: agentruntime.Usage{InputTokens: 10, OutputTokens: 5}}}
	b := novaBancada(t)
	for _, modo := range []agentruntime.CompletionMode{agentruntime.CompletionObserve, agentruntime.CompletionEnforce} {
		goal := aos489Goal("run-aos493-modo-" + string(modo))
		goal.CompletionRequires = []string{"echo"}
		goal.CompletionMode = modo
		if _, err := b.correr(goal, guiao); err != nil {
			t.Fatalf("Run (%s): %v", modo, err)
		}
		res, err := b.motor().Replay(context.Background(), goal.RunID, Options{Spec: aos489SpecDe(goal)})
		exigirFiel(t, res, err, 1)
		if res.Verdict == nil || res.Verdict.Mode != modo || res.Verdict.Reason != agentruntime.OutcomeContractNoCall {
			t.Fatalf("%s: veredicto reproduzido = %+v", modo, res.Verdict)
		}
		imposto := modo == agentruntime.CompletionEnforce
		if res.Unfulfilled != imposto || res.Terminated == imposto {
			t.Fatalf("%s: o replay deu Unfulfilled=%v Terminated=%v — nao e o desfecho com que o run correu", modo, res.Unfulfilled, res.Terminated)
		}
		if imposto && res.FinalText != "" {
			t.Fatalf("um run nao cumprido nao tem texto final; o replay devolveu %q", res.FinalText)
		}
		if !imposto && res.FinalText != texto {
			t.Fatalf("em observacao o texto final e o de sempre; o replay devolveu %q", res.FinalText)
		}
	}
}

// UM RUN SEM VEREDICTO REPRODUZ-SE COMO SEMPRE. Com o modo desligado (ou vazio) o manifesto não
// ganha campo nenhum, o loop conclui pela regra antiga e o motor também — mesmo com um contrato
// declarado e um guião que, com o veredicto ligado, daria negativo.
func TestAOS493_SemVeredicto_ODesfechoEOManifestoSaoOsDeSempre(t *testing.T) {
	for i, modo := range []agentruntime.CompletionMode{"", agentruntime.CompletionOff} {
		b := novaBancada(t)
		goal := aos489Goal("run-aos493-off-" + itoa(i))
		goal.CompletionRequires = []string{"echo"}
		goal.CompletionMode = modo
		guiao := []agentruntime.ModelResponse{{Text: "cortado a mei", StopReason: agentruntime.StopLength, Usage: agentruntime.Usage{InputTokens: 10, OutputTokens: 5}}}
		doLoop, err := b.correr(goal, guiao)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !doLoop.Terminated || doLoop.Unfulfilled || doLoop.Verdict != nil || doLoop.FinalText != "cortado a mei" {
			t.Fatalf("modo %q: o loop devia concluir como sempre e sem veredicto: %+v", modo, doLoop)
		}
		for _, ev := range b.eventos(goal.RunID) {
			if ev.Type == agentruntime.EventTypeTurnRecorded && bytes.Contains(ev.Payload, []byte("completion")) {
				t.Fatalf("modo %q: o turn.recorded ganhou o campo do veredicto: %s", modo, ev.Payload)
			}
		}
		res, err := b.motor().Replay(context.Background(), goal.RunID, Options{Spec: aos489SpecDe(goal)})
		exigirFiel(t, res, err, 1)
		if !res.Terminated || res.Unfulfilled || res.Verdict != nil || res.FinalText != "cortado a mei" {
			t.Fatalf("modo %q: o replay devia reproduzir a conclusao de sempre: %+v", modo, res)
		}
	}
}

// UM LOG GRAVADO ANTES DO AOS-493 reproduz-se com o desfecho que teve: a fixture 1.3.0 fixada em
// disco não tem veredicto no manifesto, e o motor não lho inventa.
func TestAOS493_LogAnteriorReproduzSeComODesfechoQueTeve(t *testing.T) {
	fx := carregarFixture130(t)
	for _, r := range fx.Runs {
		ms := manifestosDe(t, r.Events)
		res, err := motorSobre(t, r).Replay(context.Background(), r.RunID, Options{Spec: r.spec()})
		exigirFiel(t, res, err, len(ms))
		if !res.Terminated || res.Unfulfilled || res.Verdict != nil {
			t.Fatalf("%s: um log anterior ao AOS-493 tem de reproduzir-se concluido e sem veredicto: Terminated=%v Unfulfilled=%v Verdict=%+v",
				r.RunID, res.Terminated, res.Unfulfilled, res.Verdict)
		}
	}
}

// Um manifesto com um modo que este binário não conhece não se reproduz «no modo mais parecido».
func TestAOS493_ModoDesconhecido_NaoArrancaNemSeReproduz(t *testing.T) {
	b := novaBancada(t)
	goal := aos489Goal("run-aos493-modo-desconhecido")
	goal.CompletionMode = "Enforce"
	_, err := b.correr(goal, []agentruntime.ModelResponse{{Text: "x", Final: true}})
	if !errors.Is(err, agentruntime.ErrUnknownCompletionMode) {
		t.Fatalf("um modo fora do vocabulario tinha de recusar o arranque do run; veio %v", err)
	}
	if _, rerr := b.store.Read(context.Background(), goal.RunID, 1); !errors.Is(rerr, eventstore.ErrStreamNotFound) {
		t.Fatalf("o run recusado nao podia ter gravado eventos: %v", rerr)
	}

	// O mesmo no motor: um log cujo manifesto traga um modo desconhecido e recusado com erro.
	boa := aos489Goal("run-aos493-modo-adulterado")
	boa.CompletionMode = agentruntime.CompletionEnforce
	if _, err := b.correr(boa, []agentruntime.ModelResponse{{Text: "x", Final: true}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	evs := b.eventos(boa.RunID)
	for i := range evs {
		if evs[i].Type == agentruntime.EventTypeTurnRecorded {
			evs[i].Payload = bytes.Replace(evs[i].Payload, []byte(`"mode":"enforce"`), []byte(`"mode":"impor"`), 1)
		}
	}
	motor, err := NewEngine(logFixo{boa.RunID: evs})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := motor.Replay(context.Background(), boa.RunID, Options{Spec: aos489SpecDe(boa)}); !errors.Is(err, agentruntime.ErrUnknownCompletionMode) {
		t.Fatalf("o motor tinha de recusar um modo desconhecido no manifesto; veio %v", err)
	}
}
