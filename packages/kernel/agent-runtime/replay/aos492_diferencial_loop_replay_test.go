package replay

import (
	"context"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// AOS-492 — TESTE DIFERENCIAL LOOP↔REPLAY: o motor de replay termina o run no MESMO turno em que
// o loop o terminou.
//
// # PORQUE EXISTE
//
// O teste estrutural ([agentruntime.TurnEndsRun] chamada nos dois sítios, sem a condição
// escrita à mão) é sintáctico: garante que a chamada EXISTE, não que o seu resultado DECIDE. Um
// motor que chame a função, deite fora o resultado e decida por uma regra sua passa nele — foi
// medido na revisão do ticket (mutação S13), e passava também na suite de replay inteira, porque
// nenhum guião tinha um turno em que as duas regras discordassem.
//
// Este teste mede o COMPORTAMENTO e não a forma do código: para cada resposta do modelo em que a
// regra tem alguma coisa a decidir, corre o loop real com captura, reproduz o log com o motor, e
// exige dos dois o mesmo número de turnos, o mesmo `Terminated` e o mesmo texto final. Apanha
// qualquer reescrita de um dos lados — variável intermédia, `switch`, função auxiliar —, que é o
// que o teste estrutural deixa escapar.
//
// # PORQUE A TABELA TEM O VALOR ESPERADO
//
// Comparar só o loop com o replay deixava passar um erro IGUAL nos dois (a regra mudada dentro
// da função única). Cada caso diz por isso em que turno o run acaba e com que texto, e os dois
// lados são comparados com esse valor e um com o outro.

// aos492Caso é um guião e o desfecho que a regra de terminação lhe dá.
type aos492Caso struct {
	nome   string
	guiao  []agentruntime.ModelResponse
	turnos int
	texto  string
}

func aos492Casos() []aos492Caso {
	uso := agentruntime.Usage{InputTokens: 10, OutputTokens: 5}
	echo := func(in string) agentruntime.ToolInvocation {
		return agentruntime.ToolInvocation{ToolID: "echo", Capability: "cap:echo", ResourceType: "doc", ResourceValue: "notes", Input: []byte(in)}
	}
	falha := agentruntime.ToolInvocation{ToolID: "falha", Capability: "cap:echo", Input: []byte(`{"url":"http://exemplo.invalid"}`)}
	// sobra é o turno que NÃO pode ser alcançado: se a regra deixar passar o turno que devia
	// terminar o run, o loop chega aqui e o caso falha pelo número de turnos e pelo texto.
	sobra := agentruntime.ModelResponse{Text: "turno que nao devia existir", Final: true, Usage: uso}
	return []aos492Caso{
		{
			nome:   "texto final sem tool calls",
			guiao:  []agentruntime.ModelResponse{{Text: "concluido", Final: true, StopReason: agentruntime.StopStop, Usage: uso}, sobra},
			turnos: 1, texto: "concluido",
		},
		{
			nome: "tool call seguida de final",
			guiao: []agentruntime.ModelResponse{
				{Text: "vou ler", ToolCalls: []agentruntime.ToolInvocation{echo(`{"doc_id":"notes"}`)}, StopReason: agentruntime.StopToolCalls, Usage: uso},
				{Text: "lido", Final: true, StopReason: agentruntime.StopStop, Usage: uso},
				sobra,
			},
			turnos: 2, texto: "lido",
		},
		{
			// O caso em que as duas metades da regra discordam: `Final` diz que acaba, as tool
			// calls dizem que continua. Hoje o gateway não o produz (só dá `Final` sem tool
			// calls), mas a porta permite-o, e é aqui que uma regra local só por `len == 0`
			// se separa da de [agentruntime.TurnEndsRun].
			nome: "final declarado com tool calls",
			guiao: []agentruntime.ModelResponse{
				{Text: "final com chamadas", Final: true, ToolCalls: []agentruntime.ToolInvocation{echo(`{"doc_id":"notes"}`)}, Usage: uso},
				sobra,
			},
			turnos: 1, texto: "final com chamadas",
		},
		{
			nome: "varias tool calls num turno",
			guiao: []agentruntime.ModelResponse{
				{Text: "tres de uma vez", ToolCalls: []agentruntime.ToolInvocation{echo(`{"n":1}`), falha, echo(`{"n":2}`)}, StopReason: agentruntime.StopToolCalls, Usage: uso},
				{ToolCalls: []agentruntime.ToolInvocation{echo(`{"n":3}`), echo(`{"n":4}`)}, StopReason: agentruntime.StopToolCalls, Usage: uso},
				{Text: "feito", Final: true, Usage: uso},
				sobra,
			},
			turnos: 3, texto: "feito",
		},
		{
			nome:   "texto vazio sem tool calls e sem final",
			guiao:  []agentruntime.ModelResponse{{Usage: uso}, sobra},
			turnos: 1, texto: "",
		},
		{
			// Uma resposta cortada pelo limite de tokens: o adaptador não a marca `Final`, e sem
			// tool calls o run acaba nesse turno. O motivo não entra na regra (AOS-491).
			nome:   "motivo length sem tool calls",
			guiao:  []agentruntime.ModelResponse{{Text: "cortado a mei", StopReason: agentruntime.StopLength, Usage: uso}, sobra},
			turnos: 1, texto: "cortado a mei",
		},
		{
			// O par do anterior: o mesmo motivo COM tool calls não termina.
			nome: "motivo length com tool calls",
			guiao: []agentruntime.ModelResponse{
				{Text: "cortado mas pede", ToolCalls: []agentruntime.ToolInvocation{echo(`{"n":1}`)}, StopReason: agentruntime.StopLength, Usage: uso},
				{Text: "depois do corte", Final: true, Usage: uso},
				sobra,
			},
			turnos: 2, texto: "depois do corte",
		},
	}
}

func TestAOS492_Diferencial_OReplayTerminaOndeOLoopTerminou(t *testing.T) {
	versoes := []string{agentruntime.AssemblyVersion130, agentruntime.AssemblyVersion140}
	if got := agentruntime.SupportedAssemblyVersions(); len(got) != len(versoes) {
		t.Fatalf("o assembler monta %v e este teste so cobre %v: um layout novo tem de entrar na tabela", got, versoes)
	}
	for v, versao := range versoes {
		for i, c := range aos492Casos() {
			t.Run(versao+"/"+c.nome, func(t *testing.T) {
				b := novaBancada(t)
				// O run_id não leva a versão: um ponto não é representável num stream_id.
				goal := aos489Goal("run-aos492-dif-" + itoa(v) + "-" + itoa(i))
				goal.AssemblyVersion = versao

				// (1) O LOOP real, com captura.
				doLoop, err := b.correr(goal, c.guiao)
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				if !doLoop.Terminated || doLoop.Turns != c.turnos || doLoop.FinalText != c.texto {
					t.Fatalf("loop: terminou=%v em %d turno(s) com %q; a regra da %d turno(s) com %q",
						doLoop.Terminated, doLoop.Turns, doLoop.FinalText, c.turnos, c.texto)
				}
				ms := manifestosDe(t, b.eventos(goal.RunID))
				if len(ms) != c.turnos {
					t.Fatalf("o log tem %d turn.recorded, queria %d", len(ms), c.turnos)
				}
				for _, m := range ms {
					if m.AssemblyVersion != versao {
						t.Fatalf("o run devia ter corrido em %s e gravou um turno em %q", versao, m.AssemblyVersion)
					}
				}

				// (2) O MOTOR DE REPLAY sobre o log que o loop gravou.
				spec := aos489SpecDe(goal)
				spec.AssemblyVersion = versao
				doReplay, err := b.motor().Replay(context.Background(), goal.RunID, Options{Spec: spec})
				exigirFiel(t, doReplay, err, c.turnos)

				// (3) OS DOIS DIZEM O MESMO — e o mesmo que a tabela.
				if len(doReplay.Steps) != doLoop.Turns {
					t.Fatalf("o replay reproduziu %d turno(s) e o loop deu %d", len(doReplay.Steps), doLoop.Turns)
				}
				if doReplay.Terminated != doLoop.Terminated {
					t.Fatalf("Terminated: replay=%v loop=%v — o motor nao termina onde o loop terminou", doReplay.Terminated, doLoop.Terminated)
				}
				if doReplay.FinalText != doLoop.FinalText {
					t.Fatalf("texto final: replay=%q loop=%q", doReplay.FinalText, doLoop.FinalText)
				}
			})
		}
	}
}
