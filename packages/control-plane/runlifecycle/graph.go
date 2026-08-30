package runlifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/substrate/eventstore"
)

// ErrForeignStream — pediu-se a esta via de escrita um stream que não é o do run
// possuído. Fail-closed: o token desta posse autoriza escritas DESTE run e de mais
// nenhum.
var ErrForeignStream = errors.New("runlifecycle: escrita pedida num stream que não é o do run possuído")

// fencedStore adapta a via de escrita de uma [Tenure] à forma
// [orchestrator.EventStore] que o `GraphBuilder` de AOS-025 consome.
//
// # Porque um adaptador, e não fazer o orquestrador conhecer o token
//
// O `GraphBuilder` escreve por uma interface de duas funções (`Append`/`Read`) — a
// MESMA forma que `state.EventStore` e `durable.EventStore` declaram. Encaminhar as
// suas escritas pelo [durable.FencedAppender] não exige mudar-lhe uma linha de lógica:
// exige dar-lhe um `EventStore` cujo `Append` seja fenced. É o que isto é.
//
// A alternativa — passar o token ao orquestrador — obrigá-lo-ia a importar a
// autoridade de lease e faria da posse um argumento a viajar por caminhos que ninguém
// audita. Foi assim que o `GraphBuilder` ficou sem token nenhum. Aqui o token é
// INVISÍVEL para o orquestrador e AUTORITATIVO no ponto de escrita, que é onde tem de
// valer.
//
// `Append` RECUSA um streamID que não seja o do run possuído. Não é zelo: o
// [durable.FencedAppender] resolve o token corrente a partir do run, pelo que escrever
// noutro stream com este token seria escrever factos de um run sob a autoridade de
// outro. O `GraphBuilder` nunca o faz (escreve sempre em `b.dag.runID`); a recusa é o
// que garante que continua a não fazer.
type fencedStore struct {
	t *Tenure
}

// Compile-time: a via fenced satisfaz o contrato que o orquestrador consome.
var _ orchestrator.EventStore = fencedStore{}

func (f fencedStore) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if streamID != f.t.runID {
		return eventstore.AppendResult{}, fmt.Errorf("%w: pedido %q, posse %q", ErrForeignStream, streamID, f.t.runID)
	}
	return f.t.Append(ctx, in, opts...)
}

func (f fencedStore) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	return f.t.Read(ctx, streamID, fromSeq)
}

// newRehydratedGraph constrói o `GraphBuilder` desta posse pela ÚNICA via que este
// pacote conhece: [orchestrator.NewGraphBuilderFromLog], que parte do DAG que
// [orchestrator.RebuildDAG] reconstrói do log — nunca de um DAG vazio.
//
// Não existe neste pacote caminho para o outro construtor. É essa ausência, e não
// uma verificação, que torna o «builder cego» inconstruível a partir daqui: o guarda
// [TestGuard_SemBuilderCego] falsifica-a varrendo o source de produção.
//
// As escritas do builder saem todas por [fencedStore] e, portanto, pelo
// [durable.FencedAppender] — que é a outra metade da regra (ADR-023 §2.4).
func newRehydratedGraph(ctx context.Context, t *Tenure, producer eventstore.Producer, opts ...orchestrator.GraphOption) (*orchestrator.GraphBuilder, error) {
	return orchestrator.NewGraphBuilderFromLog(ctx, fencedStore{t}, t.runID, producer, opts...)
}
