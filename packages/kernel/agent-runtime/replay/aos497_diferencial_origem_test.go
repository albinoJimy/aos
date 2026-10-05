package replay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-497 — TESTE DIFERENCIAL DA ORIGEM DA SAÍDA: o motor de replay designa a MESMA origem que o
// loop, com o mesmo passo e o mesmo digest, e os dois dão o que a tabela diz.
//
// Alarga o diferencial do AOS-493: corre o loop real (Reference Monitor, captura), reproduz o
// log com o motor, e compara a âncora dos dois uma com a outra e com o valor esperado — que é
// calculado AQUI, dos bytes que a tool da bancada devolve, e não pela função em teste.

// aos497DigestDe é o digest esperado dos bytes de um resultado.
func aos497DigestDe(valor []byte) string {
	soma := sha256.Sum256(valor)
	return "sha256:" + hex.EncodeToString(soma[:])
}

// aos497Echo é o que a `echo` da bancada devolve para a entrada dada.
func aos497Echo(in string) []byte { return append([]byte("echoed:"), in...) }

type aos497Caso struct {
	nome   string
	guiao  []agentruntime.ModelResponse
	origem string
	// contrato, permitidas, negada e inputs compõem o run.
	contrato   []string
	permitidas []string
	negada     string
	inputs     []agentruntime.PlanInput
	turnos     int
	estado     agentruntime.OutputSourceState
	// turnoDaOrigem (a contar de 1), indice (a contar de 0) e bytesDaOrigem dizem qual chamada é
	// a designada e o que devolveu. Só em `designated`.
	turnoDaOrigem int
	indice        int
	bytesDaOrigem []byte
	// vinculada é a razão do veredicto com a declaração vinculativa em imposição; semDeclaracao
	// é a de um run igual sem declaração (a de todas as outras combinações).
	vinculada     agentruntime.OutcomeReason
	semDeclaracao agentruntime.OutcomeReason
}

func aos497Casos() []aos497Caso {
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
	const doc = `{"doc_id":"notes"}`
	return []aos497Caso{
		{
			nome: "zero chamadas", origem: "echo",
			guiao: []agentruntime.ModelResponse{fim("um resumo")}, turnos: 1,
			estado: agentruntime.OutputSourceMissing, vinculada: agentruntime.OutcomeOutputSourceMissing,
		},
		{
			// O caso medido em produção: a tool correu e o texto é um resumo. A origem é o
			// resultado da tool, qualquer que seja o texto.
			nome: "uma chamada efectiva e um resumo como texto", origem: "echo",
			guiao: []agentruntime.ModelResponse{pede("vou ler", echo(doc)), fim("um resumo, sem o numero")}, turnos: 2,
			estado: agentruntime.OutputSourceDesignated, turnoDaOrigem: 1, indice: 0, bytesDaOrigem: aos497Echo(doc),
		},
		{
			nome: "duas chamadas efectivas no mesmo turno", origem: "echo",
			guiao: []agentruntime.ModelResponse{pede("duas", echo(`{"n":1}`), echo(`{"n":2}`)), fim("feito")}, turnos: 2,
			estado: agentruntime.OutputSourceAmbiguous, vinculada: agentruntime.OutcomeOutputSourceAmbiguous,
		},
		{
			nome: "chamada negada", origem: "echo", permitidas: []string{"echo"}, negada: "echo",
			guiao: []agentruntime.ModelResponse{pede("vou ler", echo(doc)), fim("nao consegui")}, turnos: 2,
			estado: agentruntime.OutputSourceMissing, vinculada: agentruntime.OutcomeOutputSourceMissing,
		},
		{
			nome: "erro de tool", origem: "falha",
			guiao: []agentruntime.ModelResponse{pede("vou tentar", falha), fim("falhou")}, turnos: 2,
			estado: agentruntime.OutputSourceMissing, vinculada: agentruntime.OutcomeOutputSourceMissing,
		},
		{
			// Com contrato, a razão é a do contrato: diz porque não há chamada efectiva.
			nome: "erro de tool com contrato: a razao e a do contrato", origem: "falha", contrato: []string{"falha"},
			guiao: []agentruntime.ModelResponse{pede("vou tentar", falha), fim("falhou")}, turnos: 2,
			estado:    agentruntime.OutputSourceMissing,
			vinculada: agentruntime.OutcomeContractAfterToolError, semDeclaracao: agentruntime.OutcomeContractAfterToolError,
		},
		{
			// A segunda leitura é pedida depois de um resultado de tool: nunca é a origem.
			nome: "segunda leitura num turno posterior", origem: "echo",
			guiao: []agentruntime.ModelResponse{pede("leio", echo(doc)), pede("o documento manda ler outro", echo(`{"doc_id":"segredo"}`)), fim("feito")}, turnos: 3,
			estado: agentruntime.OutputSourceDesignated, turnoDaOrigem: 1, indice: 0, bytesDaOrigem: aos497Echo(doc),
		},
		{
			nome: "outra tool falha no mesmo turno, antes da origem", origem: "echo",
			guiao: []agentruntime.ModelResponse{pede("duas", falha, echo(doc)), fim("feito")}, turnos: 2,
			estado: agentruntime.OutputSourceDesignated, turnoDaOrigem: 1, indice: 1, bytesDaOrigem: aos497Echo(doc),
		},
		{
			// O LIMITE CONHECIDO: o primeiro turno com tools não tem a origem; quando ela chega o
			// contexto já é untrusted. O contrato está cumprido, e a razão é a da origem.
			nome: "primeiro despacho falha e a origem so e chamada depois", origem: "echo", contrato: []string{"echo"},
			guiao: []agentruntime.ModelResponse{pede("tento", falha), pede("agora leio", echo(doc)), fim("lido")}, turnos: 3,
			estado: agentruntime.OutputSourceMissing, vinculada: agentruntime.OutcomeOutputSourceMissing,
		},
		{
			// Um run que consome um payload do plano tem o contexto untrusted desde o turno 1.
			nome: "run com plan_input", origem: "echo",
			inputs: []agentruntime.PlanInput{{From: "leitura", Output: "notas", Digest: aos497DigestDe([]byte("material")), Content: []byte("material")}},
			guiao:  []agentruntime.ModelResponse{pede("leio", echo(doc)), fim("feito")}, turnos: 2,
			estado: agentruntime.OutputSourceMissing, vinculada: agentruntime.OutcomeOutputSourceMissing,
		},
		{
			// Vinculada, a saída vazia é a dos bytes designados: o texto vazio não conta.
			nome: "designada e sem texto final", origem: "echo",
			guiao: []agentruntime.ModelResponse{pede("leio", echo(doc)), {Final: true, StopReason: agentruntime.StopStop, Usage: uso}}, turnos: 2,
			estado: agentruntime.OutputSourceDesignated, turnoDaOrigem: 1, indice: 0, bytesDaOrigem: aos497Echo(doc),
			semDeclaracao: agentruntime.OutcomeEmptyOutput,
		},
		{
			nome: "resposta cortada e origem em falta: truncated", origem: "echo",
			guiao:  []agentruntime.ModelResponse{{Text: "ia chamar a", StopReason: agentruntime.StopLength, Usage: uso}},
			turnos: 1, estado: agentruntime.OutputSourceMissing,
			vinculada: agentruntime.OutcomeTruncated, semDeclaracao: agentruntime.OutcomeTruncated,
		},
	}
}

// aos497Goal compõe o goal de um caso.
func aos497Goal(runID, versao string, modo agentruntime.CompletionMode, vinculo agentruntime.OutputSourceBinding, c aos497Caso) agentruntime.Goal {
	goal := aos489Goal(runID)
	goal.AssemblyVersion = versao
	goal.AllowedTools = c.permitidas
	goal.Inputs = c.inputs
	goal.CompletionRequires = c.contrato
	goal.CompletionMode = modo
	goal.OutputFromTool = c.origem
	goal.OutputSourceBinding = vinculo
	return goal
}

// aos497PassoDoTurno devolve o `step_id` do `turn.recorded` do turno n (a contar de 1).
func aos497PassoDoTurno(t *testing.T, evs []eventstore.Event, n int) string {
	t.Helper()
	visto := 0
	for _, ev := range evs {
		if ev.Type != agentruntime.EventTypeTurnRecorded {
			continue
		}
		visto++
		if visto == n {
			return ev.StepID
		}
	}
	t.Fatalf("o log nao tem o turno %d", n)
	return ""
}

func TestAOS497_Diferencial_OReplayDesignaAOrigemDoLoop(t *testing.T) {
	versoes := []string{agentruntime.AssemblyVersion130, agentruntime.AssemblyVersion140}
	if got := agentruntime.SupportedAssemblyVersions(); len(got) != len(versoes) {
		t.Fatalf("o assembler monta %v e este teste so cobre %v: um layout novo tem de entrar na tabela", got, versoes)
	}
	modos := []agentruntime.CompletionMode{agentruntime.CompletionObserve, agentruntime.CompletionEnforce}
	for v, versao := range versoes {
		for m, modo := range modos {
			for k, vinculo := range agentruntime.OutputSourceBindings() {
				for i, c := range aos497Casos() {
					t.Run(versao+"/"+string(modo)+"/"+string(vinculo)+"/"+c.nome, func(t *testing.T) {
						sufixo := itoa(v) + "-" + itoa(m) + "-" + itoa(k) + "-" + itoa(i)
						b := aos493Bancada(t, c.negada)
						goal := aos497Goal("run-aos497-dif-"+sufixo, versao, modo, vinculo, c)

						// (1) O LOOP real, com captura.
						doLoop, err := b.correr(goal, c.guiao)
						if err != nil {
							t.Fatalf("Run: %v", err)
						}
						evs := b.eventos(goal.RunID)
						quer := &agentruntime.OutputSource{Tool: c.origem, Binding: vinculo, State: c.estado}
						if c.estado == agentruntime.OutputSourceDesignated {
							quer.StepID = agentruntime.ToolStepID(aos497PassoDoTurno(t, evs, c.turnoDaOrigem), c.indice)
							quer.Digest = aos497DigestDe(c.bytesDaOrigem)
							quer.Bytes = len(c.bytesDaOrigem)
						}
						if !reflect.DeepEqual(doLoop.OutputSource, quer) {
							t.Fatalf("loop: ancora\n  %+v\nqueria\n  %+v", doLoop.OutputSource, quer)
						}
						if doLoop.Turns != c.turnos {
							t.Fatalf("loop: %d turno(s), queria %d", doLoop.Turns, c.turnos)
						}
						// O passo designado é o de um evento de mediação do mesmo log.
						if quer.StepID != "" {
							mediado := false
							for _, ev := range evs {
								mediado = mediado || (ev.StepID == quer.StepID && ev.Type != agentruntime.EventTypeTurnRecorded)
							}
							if !mediado {
								t.Fatalf("o passo designado %q nao e o de nenhuma mediacao do log", quer.StepID)
							}
						}

						// (2) O DESFECHO. As razões da origem só entram com a declaração vinculativa
						// em imposição; em qualquer outra combinação o run acaba como o MESMO run
						// sem declaração — que se corre aqui, ao lado, para comparar.
						vincula := vinculo == agentruntime.OutputSourceBinds && modo == agentruntime.CompletionEnforce
						semDecl := goal
						semDecl.RunID = "run-aos497-ref-" + sufixo
						semDecl.OutputFromTool, semDecl.OutputSourceBinding = "", ""
						ref, err := aos493Bancada(t, c.negada).correr(semDecl, c.guiao)
						if err != nil {
							t.Fatalf("Run de referencia: %v", err)
						}
						if ref.OutputSource != nil || ref.Verdict == nil || ref.Verdict.Reason != c.semDeclaracao {
							t.Fatalf("referencia sem declaracao: ancora=%+v veredicto=%+v; queria sem ancora e a razao %q", ref.OutputSource, ref.Verdict, c.semDeclaracao)
						}
						if vincula {
							negativo := c.vinculada != agentruntime.OutcomeFulfilled
							if doLoop.Verdict == nil || doLoop.Verdict.Reason != c.vinculada || doLoop.Unfulfilled != negativo || doLoop.Terminated == negativo {
								t.Fatalf("vinculativa em imposicao: veredicto=%+v Unfulfilled=%v Terminated=%v; queria a razao %q",
									doLoop.Verdict, doLoop.Unfulfilled, doLoop.Terminated, c.vinculada)
							}
						} else {
							if !reflect.DeepEqual(doLoop.Verdict, ref.Verdict) || doLoop.Unfulfilled != ref.Unfulfilled ||
								doLoop.Terminated != ref.Terminated || doLoop.FinalText != ref.FinalText {
								t.Fatalf("fora de vinculativa em imposicao o desfecho tinha de ser o do run sem declaracao:\n  veio  veredicto=%+v Unfulfilled=%v Terminated=%v texto=%q\n  quero veredicto=%+v Unfulfilled=%v Terminated=%v texto=%q",
									doLoop.Verdict, doLoop.Unfulfilled, doLoop.Terminated, doLoop.FinalText,
									ref.Verdict, ref.Unfulfilled, ref.Terminated, ref.FinalText)
							}
						}

						// (3) O manifesto de CADA turno grava a origem e o vínculo: é dele que o motor
						// os lê. O `prompt_hash` não muda com a declaração — o modelo não é avisado.
						msRef := manifestosDe(t, aos493Bancada(t, c.negada).eventosDe(t, semDecl, c.guiao))
						for n, mf := range manifestosDe(t, evs) {
							if mf.Completion == nil || mf.Completion.OutputFrom != c.origem || mf.Completion.OutputBinding != vinculo {
								t.Fatalf("turno %d: manifest.completion = %+v, queria a origem %q e o vinculo %q", n+1, mf.Completion, c.origem, vinculo)
							}
							// Só os turnos que os dois runs deram são comparáveis (o run vinculado
							// pode acabar no mesmo turno com outro desfecho, nunca noutro turno).
							if n < len(msRef) && mf.PromptHash != msRef[n].PromptHash {
								t.Fatalf("turno %d: o prompt_hash mudou com a declaracao de origem", n+1)
							}
						}

						// (4) O MOTOR DE REPLAY sobre o log que o loop gravou — sem lhe dizer origem,
						// vínculo nem modo.
						doReplay, err := b.motor().Replay(context.Background(), goal.RunID, Options{Spec: aos489SpecDe(goal)})
						exigirFiel(t, doReplay, err, c.turnos)
						if !reflect.DeepEqual(doReplay.OutputSource, doLoop.OutputSource) {
							t.Fatalf("ancora:\n  replay %+v\n  loop   %+v", doReplay.OutputSource, doLoop.OutputSource)
						}
						if !reflect.DeepEqual(doReplay.Verdict, doLoop.Verdict) || doReplay.Unfulfilled != doLoop.Unfulfilled ||
							doReplay.Terminated != doLoop.Terminated || doReplay.FinalText != doLoop.FinalText {
							t.Fatalf("desfecho:\n  replay veredicto=%+v Unfulfilled=%v Terminated=%v\n  loop   veredicto=%+v Unfulfilled=%v Terminated=%v",
								doReplay.Verdict, doReplay.Unfulfilled, doReplay.Terminated, doLoop.Verdict, doLoop.Unfulfilled, doLoop.Terminated)
						}
					})
				}
			}
		}
	}
}

// eventosDe corre o goal numa bancada e devolve os eventos gravados.
func (b *aos489Bancada) eventosDe(t *testing.T, goal agentruntime.Goal, guiao []agentruntime.ModelResponse) []eventstore.Event {
	t.Helper()
	if _, err := b.correr(goal, guiao); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return b.eventos(goal.RunID)
}

// A PARIDADE VALE SOBRE A CAPTURA SELADA, que é a de produção: o motor decifra o conteúdo
// por-titular e chega ao digest que o loop calculou sobre os bytes em claro. E a âncora não
// está em parte nenhuma do log do run em claro: é o kernel que a devolve.
func TestAOS497_Paridade_SobreCapturaSelada(t *testing.T) {
	cipher := newFakeSubjectCipher()
	b := novaBancada(t)
	selado, err := NewCapturer(b.store, WithContentSealer(cipher), WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	b.cap = selado
	c := aos497Casos()[1]
	goal := aos497Goal("run-aos497-selado", agentruntime.AssemblyVersion140, agentruntime.CompletionEnforce, agentruntime.OutputSourceBinds, c)
	doLoop, err := b.correr(goal, c.guiao)
	if err != nil || doLoop.OutputSource == nil || doLoop.OutputSource.Digest != aos497DigestDe(c.bytesDaOrigem) {
		t.Fatalf("Run: ancora=%+v err=%v", doLoop.OutputSource, err)
	}
	for _, ev := range b.eventos(goal.RunID) {
		if ev.Type == EventTypeCaptured && bytes.Contains(ev.Payload, []byte("echoed:")) {
			t.Fatalf("pre-condicao: a captura devia estar selada: %s", ev.Payload)
		}
	}
	motor, err := NewEngine(b.store, WithContentOpener(cipher, authorizedAccessor()))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	doReplay, err := motor.Replay(context.Background(), goal.RunID, Options{Spec: aos489SpecDe(goal)})
	exigirFiel(t, doReplay, err, c.turnos)
	if !reflect.DeepEqual(doReplay.OutputSource, doLoop.OutputSource) {
		t.Fatalf("ancora sobre a captura selada:\n  replay %+v\n  loop   %+v", doReplay.OutputSource, doLoop.OutputSource)
	}
}

// O MOTOR LÊ A DECLARAÇÃO DO MANIFESTO, e não de quem reproduz: o mesmo log com o vínculo do
// turno terminal reescrito reproduz-se com o outro desfecho, e sem a declaração reproduz-se sem
// âncora. É o que prova que nada vem de configuração.
func TestAOS497_OReplayLeADeclaracaoDoManifesto(t *testing.T) {
	c := aos497Casos()[0] // zero chamadas: em falta
	b := novaBancada(t)
	goal := aos497Goal("run-aos497-manifesto", agentruntime.AssemblyVersion140, agentruntime.CompletionEnforce, agentruntime.OutputSourceBinds, c)
	doLoop, err := b.correr(goal, c.guiao)
	if err != nil || !doLoop.Unfulfilled || doLoop.Verdict.Reason != agentruntime.OutcomeOutputSourceMissing {
		t.Fatalf("Run: o run de partida tinha de sair nao cumprido por origem em falta: %+v err=%v", doLoop, err)
	}
	reescrito := func(de, para string) []eventstore.Event {
		out := append([]eventstore.Event(nil), b.eventos(goal.RunID)...)
		n := 0
		for i := range out {
			if out[i].Type == agentruntime.EventTypeTurnRecorded && bytes.Contains(out[i].Payload, []byte(de)) {
				out[i].Payload = bytes.Replace(append([]byte(nil), out[i].Payload...), []byte(de), []byte(para), 1)
				n++
			}
		}
		if n != 1 {
			t.Fatalf("queria reescrever um manifesto com %s; reescrevi %d", de, n)
		}
		return out
	}
	const declaracao = `,"output_from":"echo","output_binding":"binding"`

	// (a) O vínculo passa a «só medição»: conclui, com a âncora em falta.
	motor, _ := NewEngine(logFixo{goal.RunID: reescrito(`"output_binding":"binding"`, `"output_binding":"measure"`)})
	res, err := motor.Replay(context.Background(), goal.RunID, Options{Spec: aos489SpecDe(goal)})
	exigirFiel(t, res, err, 1)
	if !res.Terminated || res.Unfulfilled || res.Verdict == nil || !res.Verdict.Fulfilled ||
		res.OutputSource == nil || res.OutputSource.State != agentruntime.OutputSourceMissing || res.OutputSource.Binding != agentruntime.OutputSourceMeasure {
		t.Fatalf("so medicao no manifesto: Terminated=%v Unfulfilled=%v veredicto=%+v ancora=%+v", res.Terminated, res.Unfulfilled, res.Verdict, res.OutputSource)
	}

	// (b) Sem a declaração (o que um binário anterior gravaria): conclui, sem âncora.
	motor, _ = NewEngine(logFixo{goal.RunID: reescrito(declaracao, ``)})
	res, err = motor.Replay(context.Background(), goal.RunID, Options{Spec: aos489SpecDe(goal)})
	exigirFiel(t, res, err, 1)
	if !res.Terminated || res.Unfulfilled || res.OutputSource != nil {
		t.Fatalf("sem declaracao no manifesto: Terminated=%v Unfulfilled=%v ancora=%+v", res.Terminated, res.Unfulfilled, res.OutputSource)
	}
}

// UM LOG GRAVADO ANTES DO AOS-497 reproduz-se sem âncora e com o desfecho que teve.
func TestAOS497_LogAnteriorReproduzSeSemAncora(t *testing.T) {
	fx := carregarFixture130(t)
	for _, r := range fx.Runs {
		res, err := motorSobre(t, r).Replay(context.Background(), r.RunID, Options{Spec: r.spec()})
		exigirFiel(t, res, err, len(manifestosDe(t, r.Events)))
		if res.OutputSource != nil || !res.Terminated || res.Unfulfilled {
			t.Fatalf("%s: um log anterior ao AOS-497 reproduz-se concluido e sem ancora: %+v", r.RunID, res)
		}
	}
}

// DECLARAÇÃO IMPOSSÍVEL ⇒ O RUN NÃO ARRANCA, nos dois vínculos e nos dois modos: sem eventos e
// sem interrogar o modelo. Com o veredicto desligado a declaração é ignorada e não há âncora.
func TestAOS497_DeclaracaoImpossivel_NaoArranca(t *testing.T) {
	casos := []struct {
		nome       string
		origem     string
		vinculo    agentruntime.OutputSourceBinding
		permitidas []string
		erro       error
	}{
		{"tool fora do tool set", "doc_read", agentruntime.OutputSourceMeasure, nil, agentruntime.ErrImpossibleOutputSource},
		{"tool fora da lista-branca, so medicao", "echo", agentruntime.OutputSourceMeasure, []string{"falha"}, agentruntime.ErrImpossibleOutputSource},
		{"tool fora da lista-branca, vinculativa", "echo", agentruntime.OutputSourceBinds, []string{"falha"}, agentruntime.ErrImpossibleOutputSource},
		{"origem sem vinculo", "echo", "", nil, agentruntime.ErrBadOutputSourceBinding},
		{"vinculo sem origem", "", agentruntime.OutputSourceBinds, nil, agentruntime.ErrBadOutputSourceBinding},
	}
	for i, c := range casos {
		for _, modo := range []agentruntime.CompletionMode{agentruntime.CompletionObserve, agentruntime.CompletionEnforce} {
			t.Run(c.nome+"/"+string(modo), func(t *testing.T) {
				b := novaBancada(t)
				goal := aos489Goal("run-aos497-impossivel-" + itoa(i) + "-" + string(modo))
				goal.AllowedTools = c.permitidas
				goal.CompletionMode = modo
				goal.OutputFromTool, goal.OutputSourceBinding = c.origem, c.vinculo
				interrogado := false
				res, err := b.correrCom(goal, func() { interrogado = true })
				if !errors.Is(err, c.erro) {
					t.Fatalf("a declaracao tinha de recusar o arranque com %v; veio res=%+v err=%v", c.erro, res, err)
				}
				if interrogado || res.Turns != 0 || res.OutputSource != nil || res.Verdict != nil {
					t.Fatalf("o run recusado nao pode gastar turnos nem ter ancora: interrogado=%v res=%+v", interrogado, res)
				}
				if _, rerr := b.store.Read(context.Background(), goal.RunID, 1); !errors.Is(rerr, eventstore.ErrStreamNotFound) {
					t.Fatalf("o run recusado nao podia ter gravado eventos: %v", rerr)
				}
			})
		}
		for _, modo := range []agentruntime.CompletionMode{"", agentruntime.CompletionOff} {
			t.Run(c.nome+"/desligado-"+string(modo), func(t *testing.T) {
				b := novaBancada(t)
				goal := aos489Goal("run-aos497-impossivel-off-" + itoa(i) + "-" + string(modo))
				goal.AllowedTools = c.permitidas
				goal.CompletionMode = modo
				goal.OutputFromTool, goal.OutputSourceBinding = c.origem, c.vinculo
				res, err := b.correr(goal, []agentruntime.ModelResponse{{Text: "feito", Final: true, StopReason: agentruntime.StopStop}})
				if err != nil || !res.Terminated || res.OutputSource != nil {
					t.Fatalf("com o veredicto desligado a declaracao nao e lida: res=%+v err=%v", res, err)
				}
				for _, ev := range b.eventos(goal.RunID) {
					for _, campo := range []string{"output_from", "output_binding", "output_source"} {
						if bytes.Contains(ev.Payload, []byte(campo)) {
							t.Fatalf("com o veredicto desligado nenhum evento leva a declaracao (%s): %s %s", campo, ev.Type, ev.Payload)
						}
					}
				}
			})
		}
	}
}
