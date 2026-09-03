package durable

// FENCING DAS ESCRITAS DO LEDGER E DO CHECKPOINTER (AOS-299).
//
// A AC de `EPIC-02:428` — «escritas no Event Store carregam o fencing token; escritas com token
// inferior ao corrente são rejeitadas» — estava por cumprir para as duas escritas do caminho de
// run: [StepLedger.Apply] e [EventStoreCheckpointer.Checkpoint] escreviam sobre um [EventStore]
// CRU, não sobre um [FencedAppender]. Só o `leaseRecord` e o `transitionRecord` carregavam token.
//
// # PORQUÊ UM ADAPTADOR, E PORQUÊ AQUI
//
// As assinaturas divergem: [EventStore.Append] é `(ctx, streamID, in, opts...)` e
// [FencedAppender.Append] é `(ctx, runID, token, in, opts...)`. O token tem de vir de algum lado
// no ponto da escrita — e o ledger do nó é composto UMA VEZ, partilhado por todos os runs,
// enquanto o token é POR-RUN. É o mesmo problema que [ContextWithTitular] já resolve para o
// titular do crypto-shredding, e resolve-se da mesma maneira: o token viaja no contexto, anexado
// por quem CONHECE o run.
//
// Vive em `durable` e não no composition-root por uma razão concreta: o guard-test de veracidade
// de AOS-222 (`cmd/aos/aos222_fencing_truthfulness_test.go`) faz `t.Skip` quando encontra a
// substring `NewFencedAppender` ou `worker.NewWorker` num `.go` não-teste do pacote do nó. É uma
// verificação TEXTUAL, não semântica. Escrever este adaptador em `cmd/aos` desarmaria o guarda em
// silêncio — e desarmá-lo seria mentira, porque o `worker.Worker` continua por compor e as
// restantes escritas do caminho de run continuam sem fencing. O guarda tem de continuar a vigiar,
// e continua.

import (
	"context"
	"fmt"

	"github.com/aos-ref/substrate/eventstore"
)

// fencingTokenKey é a chave privada do token no contexto (tipo não-exportado ⇒ nenhum outro
// pacote pode colidir com ela).
type fencingTokenKey struct{}

// ContextWithFencingToken anexa o fencing token do run ao contexto, para que as escritas do
// [StepLedger] e do [EventStoreCheckpointer] o possam apresentar sem que toda a cadeia intermédia
// o transporte na assinatura.
//
// Quem o anexa é quem DETÉM o lease: no nó, a hospedagem do run e a condução da saga de
// compensação — os dois pontos que têm o token em mão. Um token inválido é ignorado (o contexto
// segue sem ele), e o [FencedStore] trata a ausência como obsoleto: fail-closed.
func ContextWithFencingToken(ctx context.Context, token FencingToken) context.Context {
	if !token.Valid() {
		return ctx
	}
	return context.WithValue(ctx, fencingTokenKey{}, token)
}

// FencingTokenFrom devolve o token anexado por [ContextWithFencingToken]. Ausente ⇒ o token ZERO,
// que [FencingToken.Valid] recusa — logo a ausência e um token inválido são o MESMO caso para o
// appender, e nenhum dos dois escreve.
func FencingTokenFrom(ctx context.Context) FencingToken {
	t, _ := ctx.Value(fencingTokenKey{}).(FencingToken)
	return t
}

// FencedStore é um [EventStore] que impõe o fencing em cada Append, apresentando ao
// [FencedAppender] o token que [ContextWithFencingToken] anexou.
//
// # O QUE ACONTECE SEM TOKEN NO CONTEXTO, E PORQUÊ NÃO É UMA RECUSA CEGA
//
// A propriedade que a AC exige é «escritas com token INFERIOR AO CORRENTE são rejeitadas» — ou
// seja: um escritor SUPERADO não escreve. Sem token no contexto, a pergunta certa não é «trouxe
// token?» mas «há alguém a quem este escritor possa estar a suceder?»:
//
//   - a autoridade não responde (erro) ⇒ RECUSA. Não se escreve sobre um estado de posse que não
//     se conseguiu ler;
//   - existe um token corrente (> 0) ⇒ RECUSA com [ErrStaleFencingToken]. Há um detentor, e quem
//     escreve sem apresentar token não é ele — é exactamente o escritor obsoleto que o fencing
//     existe para barrar;
//   - não existe token corrente (run NUNCA reclamado) ⇒ escreve. Não há posse a superar, logo
//     não há nada que o fencing pudesse proteger. Recusar aqui não fecharia defeito nenhum e
//     partiria a superfície de embedding — um [Bootstrap] com execução durável e sem serviço
//     (que é como uma dúzia de testes deste repositório conduz o `Runtime`) deixaria de poder
//     escrever, sem que houvesse concorrente nenhum.
//
// Assim que o run é reclamado — e no nó é sempre, antes de ser hospedado — a primeira regra em
// vigor passa a ser a segunda, e toda a escrita tem de apresentar token.
//
// Read NÃO é fenceado, e é deliberado: o fencing protege contra um escritor SUPERADO, não contra
// um leitor. `RebuildLedger` relê o log no início de cada hospedagem, ANTES de o run reclamar
// seja o que for — exigir token aí impediria a reconstrução de que a própria posse depende.
type FencedStore struct {
	fenced *FencedAppender
	tokens TokenSource
	store  EventStore
}

// NewFencedStore constrói o [EventStore] fenceado sobre um store e uma autoridade de token.
// Ambos obrigatórios ([ErrNilStore] / [ErrNilTokenSource]) — a construção falha em vez de
// devolver um store que não fenceia nada.
func NewFencedStore(store EventStore, tokens TokenSource, opts ...FencedOption) (*FencedStore, error) {
	fenced, err := NewFencedAppender(store, tokens, opts...)
	if err != nil {
		return nil, err
	}
	return &FencedStore{fenced: fenced, tokens: tokens, store: store}, nil
}

// Append delega no [FencedAppender] com o token do contexto. O `streamID` é o run: é a convenção
// que a máquina de estados de AOS-017 e o ledger de AOS-014 já partilham, e é o mesmo
// identificador cujo token corrente o appender consulta.
func (s *FencedStore) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	token := FencingTokenFrom(ctx)
	if token.Valid() {
		return s.fenced.Append(ctx, streamID, token, in, opts...)
	}
	// Sem token: decide-se pela EXISTÊNCIA DE DETENTOR, não pela ausência de token. Ver o
	// doc-comment do tipo.
	current, err := s.tokens.CurrentToken(ctx, streamID)
	if err != nil {
		return eventstore.AppendResult{}, err
	}
	if current.Valid() {
		return eventstore.AppendResult{}, fmt.Errorf(
			"%w: escrita SEM token num run com detentor (corrente=%d, run %s) — quem escreve sem apresentar token nao e o detentor (AOS-299)",
			ErrStaleFencingToken, current.Value(), streamID)
	}
	return s.store.Append(ctx, streamID, in, opts...)
}

// Read delega directamente — ver a nota sobre leituras no doc-comment do tipo.
func (s *FencedStore) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	return s.store.Read(ctx, streamID, fromSeq)
}

var _ EventStore = (*FencedStore)(nil)
