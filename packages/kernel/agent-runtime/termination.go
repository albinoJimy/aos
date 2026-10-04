package agentruntime

// A REGRA DE TERMINAÇÃO DO RUN, NUM SÓ SÍTIO (AOS-492).
//
// Até aqui a decisão «este turno acaba o run» estava escrita duas vezes: no loop (`loop.go`)
// e, copiada à mão, no motor de replay (`replay/engine.go`). Nada mantinha as duas cópias
// iguais. Uma mudança da regra que entrasse numa e não na outra fazia o replay parar num turno
// diferente daquele em que o run parou — e com fidelidade aparente de 100%, porque o replay só
// compara os turnos que chega a reproduzir.
//
// [TurnEndsRun] é essa decisão. O loop e o motor chamam-na; nenhum dos dois a tem escrita
// localmente, e [TestAOS492_ARegraDeTerminacaoNaoEstaEscritaAMao] avermelha se voltar a estar.

// TurnEndsRun diz se a resposta do modelo de um turno TERMINA o run, no layout em que o turno
// foi montado.
//
// A regra, igual nos layouts 1.3.0 e 1.4.0: o turno termina o run quando o modelo o declara
// final ([ModelResponse.Final]) ou quando não pede nenhuma tool call.
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
