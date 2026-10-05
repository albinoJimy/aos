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
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
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
// As recusas e as falhas são reais: a recusa é a de um hook do Reference Monitor que nega a
// tool; a falha é a da tool `falha` da bancada. A recusa NÃO é a da lista-branca do run: uma
// tool do contrato fora da lista-branca é um contrato impossível, e o run nem arranca
// ([TestAOS493_ContratoImpossivel_NaoArranca]).

// negaATool é o hook que nega uma tool pelo nome: uma recusa de política sobre uma tool que o
// run tem no tool set e na lista-branca.
type negaATool struct{ tool string }

func (negaATool) Name() string { return "politica-de-teste" }
func (h negaATool) Evaluate(_ context.Context, call *referencemonitor.Call) (referencemonitor.HookResult, error) {
	if call.ToolID == h.tool {
		return referencemonitor.HookResult{Decision: referencemonitor.HookDeny, Reason: "negada pela politica de teste"}, nil
	}
	return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
}

// aos493Bancada devolve a bancada do caso: com o hook de recusa quando o caso nega uma tool.
func aos493Bancada(t *testing.T, negada string) *aos489Bancada {
	t.Helper()
	if negada == "" {
		return novaBancada(t)
	}
	return novaBancada(t, negaATool{tool: negada})
}

// aos493Caso é um guião, o contrato do run e o veredicto que o kernel lhe dá.
type aos493Caso struct {
	nome     string
	guiao    []agentruntime.ModelResponse
	contrato []string
	// permitidas é a lista-branca do run; nil ⇒ sem restrição.
	permitidas []string
	// negada é a tool que o Reference Monitor recusa (por um hook); vazia ⇒ nenhuma.
	negada  string
	turnos  int
	razao   agentruntime.OutcomeReason
	vector  []agentruntime.ToolEvidence
	pedidas int
	// sobre é o desfecho do último turno que despachou tool calls ([Result.LastToolOutcome]).
	sobre string
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
			// A classe que o veredicto NÃO apanha e que a medição conta (revisão I6): sem
			// contrato, o run acaba sobre uma recusa e conclui.
			nome:   "sem contrato, acaba sobre uma recusa",
			guiao:  []agentruntime.ModelResponse{pede("vou ler", echo(`{"doc_id":"notes"}`)), fim("nao consegui ler")},
			negada: "echo", turnos: 2, pedidas: 1, sobre: agentruntime.ToolOutcomeDenied,
		},
		{
			// Com o contrato cumprido por uma chamada anterior, o run acaba sobre uma falha de
			// tool e o veredicto é positivo.
			nome:     "contrato cumprido, acaba sobre uma falha de tool",
			guiao:    []agentruntime.ModelResponse{pede("leio", echo(`{"n":1}`)), pede("tento", falha), fim("a segunda falhou")},
			contrato: []string{"echo"}, turnos: 3, pedidas: 2, sobre: agentruntime.ToolOutcomeFailed,
			vector: []agentruntime.ToolEvidence{{Tool: "echo", Requested: 1, Effective: 1, Last: agentruntime.ToolOutcomeEffective}},
		},
		{
			nome:     "contrato cumprido por uma chamada efectiva",
			guiao:    []agentruntime.ModelResponse{pede("vou ler", echo(`{"doc_id":"notes"}`)), fim("lido")},
			contrato: []string{"echo"}, turnos: 2, pedidas: 1, sobre: agentruntime.ToolOutcomeEffective,
			vector: []agentruntime.ToolEvidence{{Tool: "echo", Requested: 1, Effective: 1, Last: agentruntime.ToolOutcomeEffective}},
		},
		{
			// A tool exigida foi pedida e o Reference Monitor recusou-a. Uma chamada recusada não
			// é efectiva. O run tem lista-branca, e a tool do contrato está nela.
			nome:     "contrato por cumprir depois de uma recusa",
			guiao:    []agentruntime.ModelResponse{pede("vou ler", echo(`{"doc_id":"notes"}`)), fim("nao consegui ler")},
			contrato: []string{"echo"}, permitidas: []string{"echo"}, negada: "echo", turnos: 2, pedidas: 1,
			razao: agentruntime.OutcomeContractAfterDenial, sobre: agentruntime.ToolOutcomeDenied,
			vector: []agentruntime.ToolEvidence{{Tool: "echo", Requested: 1, Denied: 1, Last: agentruntime.ToolOutcomeDenied}},
		},
		{
			// A tool exigida foi permitida e a execução falhou. Uma chamada falhada não é efectiva.
			nome:     "contrato por cumprir depois de uma falha de tool",
			guiao:    []agentruntime.ModelResponse{pede("vou tentar", falha), fim("a tool falhou")},
			contrato: []string{"falha"}, turnos: 2, pedidas: 1,
			razao: agentruntime.OutcomeContractAfterToolError, sobre: agentruntime.ToolOutcomeFailed,
			vector: []agentruntime.ToolEvidence{{Tool: "falha", Requested: 1, Failed: 1, Last: agentruntime.ToolOutcomeFailed}},
		},
		{
			// O vector é POR TOOL: uma tool cumprida não absolve a outra.
			nome:     "duas tools exigidas, so uma chamada",
			guiao:    []agentruntime.ModelResponse{pede("uma", echo(`{"n":1}`)), fim("feito")},
			contrato: []string{"echo", "falha"}, turnos: 2, pedidas: 1,
			razao: agentruntime.OutcomeContractNoCall, sobre: agentruntime.ToolOutcomeEffective,
			vector: []agentruntime.ToolEvidence{
				{Tool: "echo", Requested: 1, Effective: 1, Last: agentruntime.ToolOutcomeEffective},
				{Tool: "falha"},
			},
		},
		{
			// A falha de uma tool que o contrato não exige não o descumpre. O run acabou sobre um
			// turno com uma falha: é o pior do turno que conta, não a última chamada.
			nome:     "contrato cumprido e outra tool falhada no mesmo turno",
			guiao:    []agentruntime.ModelResponse{pede("uma", falhaDepois(falha, echo(`{"n":1}`))...), fim("feito")},
			contrato: []string{"echo"}, turnos: 2, pedidas: 2, sobre: agentruntime.ToolOutcomeFailed,
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
					b := aos493Bancada(t, c.negada)
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
					sobre := c.sobre
					if sobre == "" {
						sobre = agentruntime.ToolOutcomeNone
					}
					if doLoop.LastToolOutcome != sobre {
						t.Fatalf("loop: o run acabou sobre %q, queria %q", doLoop.LastToolOutcome, sobre)
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

// O MOTOR LÊ O MODO DO TURNO TERMINAL, e não o do primeiro (revisão M1). Só se distinguem num
// log MISTO — um run cujos turnos não gravaram todos o mesmo `manifest.completion`, que é o que
// um rollback a meio do run deixa: os turnos dados pelo binário antigo não têm o campo. A regra
// é a do desfecho: quem selou o run foi quem deu o turno terminal, e é o manifesto desse turno
// que diz com que regra.
func TestAOS493_OReplayLeOModoDoTurnoTerminal(t *testing.T) {
	uso := agentruntime.Usage{InputTokens: 10, OutputTokens: 5}
	guiao := []agentruntime.ModelResponse{
		{Text: "vou tentar", StopReason: agentruntime.StopToolCalls, Usage: uso, ToolCalls: []agentruntime.ToolInvocation{
			{ToolID: "falha", Capability: "cap:echo", Input: []byte(`{"url":"http://exemplo.invalid"}`)},
		}},
		{Text: "a tool falhou", Final: true, StopReason: agentruntime.StopStop, Usage: uso},
	}
	// reescrever troca o `completion` do manifesto do turno `turno` (1 ou 2) no log.
	reescrever := func(evs []eventstore.Event, turno int, de, para string) []eventstore.Event {
		out := append([]eventstore.Event(nil), evs...)
		n := 0
		for i := range out {
			if out[i].Type != agentruntime.EventTypeTurnRecorded {
				continue
			}
			n++
			if n != turno {
				continue
			}
			if !bytes.Contains(out[i].Payload, []byte(de)) {
				t.Fatalf("o turno %d nao tem %s no manifesto: %s", turno, de, out[i].Payload)
			}
			out[i].Payload = bytes.Replace(append([]byte(nil), out[i].Payload...), []byte(de), []byte(para), 1)
		}
		if n != 2 {
			t.Fatalf("o run tem %d turno(s) gravado(s); o caso precisa de 2", n)
		}
		return out
	}
	const completo = `,"completion":{"mode":"enforce","requires":["falha"]}`

	casos := []struct {
		nome string
		// turno e de/para dizem que manifesto é reescrito e como.
		turno      int
		de, para   string
		imposto    bool
		temVerdict bool
	}{
		{nome: "turno 1 em observacao, terminal em imposicao: imposto", turno: 1,
			de: `"mode":"enforce"`, para: `"mode":"observe"`, imposto: true, temVerdict: true},
		{nome: "turno 1 sem o campo (binario antigo), terminal em imposicao: imposto", turno: 1,
			de: completo, para: ``, imposto: true, temVerdict: true},
		{nome: "turno 1 em imposicao, terminal em observacao: concluido", turno: 2,
			de: `"mode":"enforce"`, para: `"mode":"observe"`, imposto: false, temVerdict: true},
		{nome: "turno 1 em imposicao, terminal sem o campo (rollback): concluido sem veredicto", turno: 2,
			de: completo, para: ``, imposto: false, temVerdict: false},
	}
	for i, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			b := novaBancada(t)
			goal := aos489Goal("run-aos493-misto-" + itoa(i))
			goal.CompletionRequires = []string{"falha"}
			goal.CompletionMode = agentruntime.CompletionEnforce
			doLoop, err := b.correr(goal, guiao)
			if err != nil || !doLoop.Unfulfilled {
				t.Fatalf("Run: o run de partida tinha de sair nao cumprido: %+v err=%v", doLoop, err)
			}
			motor, err := NewEngine(logFixo{goal.RunID: reescrever(b.eventos(goal.RunID), c.turno, c.de, c.para)})
			if err != nil {
				t.Fatalf("NewEngine: %v", err)
			}
			res, err := motor.Replay(context.Background(), goal.RunID, Options{Spec: aos489SpecDe(goal)})
			exigirFiel(t, res, err, 2)
			if res.Unfulfilled != c.imposto || res.Terminated == c.imposto {
				t.Fatalf("o motor deu Unfulfilled=%v Terminated=%v; o manifesto do turno TERMINAL manda Unfulfilled=%v",
					res.Unfulfilled, res.Terminated, c.imposto)
			}
			if (res.Verdict != nil) != c.temVerdict {
				t.Fatalf("veredicto reproduzido = %+v; queria veredicto=%v", res.Verdict, c.temVerdict)
			}
			if c.temVerdict && res.Verdict.Reason != agentruntime.OutcomeContractAfterToolError {
				t.Fatalf("razao reproduzida = %q", res.Verdict.Reason)
			}
		})
	}
}

// CONTRATO IMPOSSÍVEL ⇒ O RUN NÃO ARRANCA (revisão I5). Uma tool exigida que o run não tem no
// tool set, ou que a sua lista-branca não admite, nunca pode ter uma chamada efectiva. O run
// recusa-se antes do primeiro turno, com erro próprio: não grava eventos, não interroga o
// modelo e não acaba em `contract_unmet_no_call` — que diria «o modelo não chamou» de um defeito
// de quem compôs o run.
func TestAOS493_ContratoImpossivel_NaoArranca(t *testing.T) {
	casos := []struct {
		nome       string
		contrato   []string
		permitidas []string
		semTools   bool
		diz        string
	}{
		{nome: "tool que nao existe no tool set", contrato: []string{"doc_read"}, diz: `"doc_read" nao esta no tool set do run`},
		{nome: "outra caixa", contrato: []string{"Echo"}, diz: `"Echo" nao esta no tool set do run`},
		{nome: "espaco a esquerda", contrato: []string{" echo"}, diz: `" echo" nao esta no tool set do run`},
		{nome: "espaco a direita", contrato: []string{"echo "}, diz: `"echo " nao esta no tool set do run`},
		{nome: "fora da lista-branca", contrato: []string{"echo"}, permitidas: []string{"falha"}, diz: `"echo" esta fora da lista-branca do run`},
		{nome: "lista-branca vazia nega todas", contrato: []string{"echo"}, permitidas: []string{}, diz: `"echo" esta fora da lista-branca do run`},
		{nome: "run sem tool set", contrato: []string{"echo"}, semTools: true, diz: `"echo" nao esta no tool set do run`},
		{nome: "uma possivel e uma impossivel", contrato: []string{"echo", "doc_write"}, diz: `"doc_write" nao esta no tool set do run`},
	}
	for i, c := range casos {
		// A recusa NÃO DEPENDE DO MODO: em observação o run também não arranca. Um contrato
		// impossível não é um veredicto a observar, é um run mal composto.
		for _, modo := range []agentruntime.CompletionMode{agentruntime.CompletionObserve, agentruntime.CompletionEnforce} {
			t.Run(c.nome+"/"+string(modo), func(t *testing.T) {
				b := novaBancada(t)
				goal := aos489Goal("run-aos493-impossivel-" + itoa(i) + "-" + string(modo))
				goal.CompletionRequires = c.contrato
				goal.AllowedTools = c.permitidas
				goal.CompletionMode = modo
				if c.semTools {
					goal.Tools = nil
				}
				interrogado := false
				res, err := b.correrCom(goal, func() { interrogado = true })
				if !errors.Is(err, agentruntime.ErrImpossibleCompletionContract) {
					t.Fatalf("um contrato impossivel tinha de recusar o arranque com ErrImpossibleCompletionContract; veio res=%+v err=%v", res, err)
				}
				if !bytes.Contains([]byte(err.Error()), []byte(c.diz)) {
					t.Fatalf("o erro tem de dizer que tool e porque: quero %q em %q", c.diz, err)
				}
				if interrogado || res.Turns != 0 || res.Terminated || res.Unfulfilled || res.Verdict != nil {
					t.Fatalf("o run recusado nao pode gastar turnos nem ter veredicto: interrogado=%v res=%+v", interrogado, res)
				}
				if _, rerr := b.store.Read(context.Background(), goal.RunID, 1); !errors.Is(rerr, eventstore.ErrStreamNotFound) {
					t.Fatalf("o run recusado nao podia ter gravado eventos: %v", rerr)
				}
			})
		}
		// EM `off` O CONTRATO É IGNORADO, como sempre: o run arranca e conclui pela regra antiga.
		for _, modo := range []agentruntime.CompletionMode{"", agentruntime.CompletionOff} {
			t.Run(c.nome+"/desligado-"+string(modo), func(t *testing.T) {
				b := novaBancada(t)
				goal := aos489Goal("run-aos493-impossivel-off-" + itoa(i) + "-" + string(modo))
				goal.CompletionRequires = c.contrato
				goal.AllowedTools = c.permitidas
				goal.CompletionMode = modo
				if c.semTools {
					goal.Tools = nil
				}
				res, err := b.correr(goal, []agentruntime.ModelResponse{{Text: "feito", Final: true, StopReason: agentruntime.StopStop}})
				if err != nil || !res.Terminated || res.Verdict != nil {
					t.Fatalf("com o veredicto desligado o contrato nao e lido e o run arranca: res=%+v err=%v", res, err)
				}
			})
		}
	}
}

// UM CONTRATO POSSÍVEL ARRANCA: a tool está no tool set e, havendo lista-branca, está nela. O
// nome vazio e o repetido continuam a ser retirados antes da verificação.
func TestAOS493_ContratoPossivel_Arranca(t *testing.T) {
	for i, c := range []struct {
		nome       string
		contrato   []string
		permitidas []string
	}{
		{nome: "sem lista-branca", contrato: []string{"echo"}},
		{nome: "na lista-branca", contrato: []string{"echo"}, permitidas: []string{"falha", "echo"}},
		{nome: "duas tools", contrato: []string{"echo", "falha"}},
		{nome: "nome vazio e repetido", contrato: []string{"echo", "", "echo"}},
		{nome: "sem contrato e sem tools permitidas", contrato: nil, permitidas: []string{}},
	} {
		t.Run(c.nome, func(t *testing.T) {
			b := novaBancada(t)
			goal := aos489Goal("run-aos493-possivel-" + itoa(i))
			goal.CompletionRequires = c.contrato
			goal.AllowedTools = c.permitidas
			goal.CompletionMode = agentruntime.CompletionEnforce
			res, err := b.correr(goal, []agentruntime.ModelResponse{{Text: "feito", Final: true, StopReason: agentruntime.StopStop}})
			if err != nil || res.Turns != 1 || res.Verdict == nil {
				t.Fatalf("um contrato possivel tinha de arrancar e chegar ao veredicto: res=%+v err=%v", res, err)
			}
		})
	}
}
