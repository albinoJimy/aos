package agentruntime

// A REGRA DE TERMINAÇÃO DO RUN, NUM SÓ SÍTIO (AOS-492).
//
// Até aqui a decisão «este turno acaba o run» estava escrita duas vezes: no loop (`loop.go`)
// e, copiada à mão, no motor de replay (`replay/engine.go`). Nada mantinha as duas cópias
// iguais. Uma mudança da regra que entrasse numa e não na outra fazia o replay parar num turno
// diferente daquele em que o run parou — e com fidelidade aparente de 100%, porque o replay só
// compara os turnos que chega a reproduzir.
//
// [TurnEndsRun] é essa decisão. O loop e o motor chamam-na, e nenhum dos dois a tem escrita
// localmente. Duas coisas o seguram, e cada uma garante uma coisa diferente:
//
//   - o teste ESTRUTURAL (`TestAOS492_ARegraDeTerminacaoNaoEstaEscritaAMao`) lê o código dos
//     dois ficheiros. APANHA a cópia verbatim da condição, a closure local com a regra, a sua
//     negação e a remoção da chamada a [TurnEndsRun]. É sintáctico, e por isso ESCAPA-LHE uma
//     reescrita que não tenha essa forma: os campos lidos para uma variável intermédia, um
//     `switch` sobre o comprimento, uma função auxiliar noutro ficheiro do pacote. Garante que
//     a chamada existe — NÃO que o seu resultado é o que decide;
//   - o teste DIFERENCIAL (`TestAOS492_Diferencial_OReplayTerminaOndeOLoopTerminou`, no pacote
//     `replay`) é o que garante a igualdade de COMPORTAMENTO: para uma tabela de respostas do
//     modelo, nos dois layouts, corre o loop com captura, reproduz o log com o motor e exige o
//     mesmo número de turnos, o mesmo `Terminated` e o mesmo texto final. Não olha para a forma
//     do código, pelo que apanha as reescritas que escapam ao estrutural quando mudam o
//     desfecho — incluindo um chamador que chame a função e ignore o resultado.
//
// Quem mudar a regra (um layout novo) apoia-se no diferencial, e acrescenta-lhe os casos em que
// a regra nova decide de outra maneira.

// TurnEndsRun diz se a resposta do modelo de um turno TERMINA o run, no layout em que o turno
// foi montado.
//
// A regra, igual em todos os layouts: o turno termina o run quando o modelo o declara
// final ([ModelResponse.Final]) ou quando não pede nenhuma tool call. A 1.6.0 acrescenta-lhe uma
// excepção que depende do turno e não só da resposta — o turno que entrega os `plan_input`
// adiados não termina —, e por isso o loop e o motor chamam [TailSequence.TurnEndsRun], que
// aplica esta regra e depois a excepção.
//
// TERMINAR NÃO É CONCLUIR (AOS-493). Esta função diz só que o run não tem mais turnos; se
// acabou cumprido ou não é de [ConcludeRun], que o loop e o motor chamam a seguir.
//
// PORQUE RECEBE O LAYOUT, se hoje a regra é a mesma em todos. A versão de layout é o que um run
// fixa à partida e o que cada turno grava no manifesto: é a única coordenada por que o replay
// consegue escolher, turno a turno, a regra com que o run correu. Uma regra nova entra por um
// layout novo, e os runs gravados nos anteriores continuam a terminar onde terminaram. Por isso
// o parâmetro existe desde já — quem chama não tem de mudar quando a regra variar.
//
// Uma versão que este assembler não conhece é [ErrUnknownAssemblyVersion], como em [layoutFor]:
// nunca se decide a terminação de um turno pela regra «mais recente» por omissão. Nos dois
// chamadores a versão já foi validada antes (o loop resolveu o layout do run; o motor admitiu o
// log), pelo que o erro só aparece se essa validação deixar de existir.
func TurnEndsRun(resp ModelResponse, assemblyVersion string) (bool, error) {
	if _, err := layoutFor(assemblyVersion); err != nil {
		return false, err
	}
	return resp.Final || len(resp.ToolCalls) == 0, nil
}

// TurnEndsRun é a regra de terminação do turno que esta sequência acabou de fechar: a de
// [TurnEndsRun] no layout do run, com UMA diferença, que só a 1.6.0 tem — o turno em que os
// `plan_input` adiados entraram no tail ([TailSequence.TurnWithState]) NÃO acaba o run, seja qual
// for a resposta. O modelo desse turno não viu os dados que o run lhe deu; uma resposta final ou
// sem tool calls dele não é a resposta à tarefa, e o run segue para o turno em que os vê.
//
// Chama-se DEPOIS do [TailSequence.TurnWithState] do mesmo turno: é ele que diz se o turno
// entregou os dados. O loop e o motor de replay chamam-na nessa ordem (AOS-492: a regra continua
// num só sítio — aqui e em [TurnEndsRun], que esta chama).
func (s *TailSequence) TurnEndsRun(resp ModelResponse) (bool, error) {
	termina, err := TurnEndsRun(resp, s.lay.version)
	if err != nil || !termina {
		return termina, err
	}
	if s.entregouNoUltimoTurno {
		return false, nil
	}
	return true, nil
}

// StopReasonStats recebe, por cada turno em que o modelo respondeu, o motivo de paragem desse
// turno, já no vocabulário fechado de [StopReason] (AOS-491). É a medição que faltava para ver
// de fora respostas cortadas ou filtradas: quem a liga ([WithStopReasonStats]) soma-a onde a
// quiser expor.
//
// É LEITURA: não decide nada e não pode parar o run. Como o [ToolCallStats], é por turno
// PERCORRIDO — um run re-hospedado (retoma, crash-resume) volta a percorrer os turnos já dados
// e volta a reportá-los.
type StopReasonStats func(runID string, motivo StopReason)

// WithStopReasonStats liga o observador do motivo de paragem. Um valor nil é ignorado.
func WithStopReasonStats(o StopReasonStats) Option {
	return func(rt *Runtime) {
		if o != nil {
			rt.stopReasonStats = o
		}
	}
}
