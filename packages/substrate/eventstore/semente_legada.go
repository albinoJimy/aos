package eventstore

// semente_legada.go — A COSTURA QUE DEIXA UM TESTE CONSTRUIR O MUNDO «ANTES».
//
// # PORQUE É QUE ISTO TEM DE EXISTIR
//
// Quando o [Store.Append] passou a impor [ValidarStreamID], uma classe inteira de testes deixou
// de poder correr: os das PRÓPRIAS MIGRAÇÕES. Um teste que prova que a migração transporta os
// factos de `gov.approvals` para `aos-internal/gov/approvals` tem primeiro de POR factos em
// `gov.approvals` — e o nome legado é, por definição, um nome que a regra nova recusa.
//
// Sem costura, esses testes teriam de ser apagados ou enfraquecidos, e perder-se-ia a prova de
// que a migração preserva o uso-único de um grant já consumido. Essa prova é a razão pela qual a
// migração existe.
//
// # PORQUE É QUE NÃO É UMA AppendOption PÚBLICA
//
// Porque seria um buraco na regra com a forma de uma API. O AOS-424 nasceu de a regra ter três
// cópias e nenhuma imposição; acrescentar uma maneira suportada de a contornar em produção seria
// repor o problema com outro nome.
//
// Esta função está protegida por DUAS barreiras independentes:
//
//  1. [testing.Testing], em runtime. Fora de um binário de teste recusa, e não há bandeira que a
//     ligue. É a barreira que conta.
//  2. O gate `scripts/ci/stream-names`, que recusa qualquer chamador fora de um `_test.go`. É
//     barata e apanha a intenção antes do CI.
//
// # PORQUE É QUE SEMEIA POR AQUI E NÃO CONSTRÓI O Event À MÃO
//
// Havia uma alternativa sem API nova: construir `[]Event` à mão e chamar [Store.IngestStream],
// que não valida. Foi rejeitada porque o teste passaria a ser responsável por montar o envelope
// — `EventID`, `Seq`, `IdempotencyKey`, carimbos — e um envelope montado à mão que diverge do que
// o binário antigo escrevia produz um mundo «antes» FALSO. O teste continuaria verde e deixaria
// de provar o que diz provar.
//
// Por aqui, o facto semeado atravessa exactamente o mesmo caminho que o [Store.Append] percorre —
// idempotência, CAS, quórum, WAL, fanout —, menos a validação do nome. É a única diferença, e é
// a que se quer.

import (
	"context"
	"fmt"
	"testing"
)

// semValidacaoDeNome desliga [ValidarStreamID] neste append.
//
// É deliberadamente NÃO-EXPORTADA: [AppendOption] é um `func(*appendOpts)` sobre um tipo não
// exportado, pelo que nenhum pacote de fora pode construir uma. A única porta é
// [SemearStreamLegado], e essa tem guarda.
func semValidacaoDeNome() AppendOption {
	return func(o *appendOpts) { o.semValidacaoDeNome = true }
}

// SementeDeStream é o subconjunto do Event Store de que a costura precisa.
//
// É uma INTERFACE e não o `*Store` concreto porque o sítio que semeia é, quase sempre, um teste
// de integração que só tem a PORTA composta no nó (`node.EventStore`). Exigir o tipo concreto
// obrigaria cada teste a fazer uma asserção de tipo, e uma asserção de tipo num teste é uma
// linha que falha por uma razão que não é a do teste.
type SementeDeStream interface {
	Append(ctx context.Context, streamID string, in EventInput, opts ...AppendOption) (AppendResult, error)
}

// SemearStreamLegado escreve um facto num `stream_id` que a regra de nomes RECUSA, para que um
// teste possa construir o estado que existia antes de uma migração.
//
// Recusa fora de um binário de teste. Em tudo o resto comporta-se como [Store.Append].
//
// A opção que desliga a validação é NÃO-EXPORTADA, pelo que uma implementação de
// [SementeDeStream] de fora deste pacote não a sabe interpretar — e não precisa: uma
// implementação de fora não impõe [ValidarStreamID] para começar, porque a imposição vive aqui e
// no `subjectDe`. O que a costura garante é que a porta REAL a honra.
//
// Se estiver a pensar em chamar isto de código de produção para «desbloquear» uma escrita: o
// nome que quer escrever não é representável sobre JetStream, e o que vai acontecer é a escrita
// passar hoje e o stream ficar ilegível no dia da migração. Renomeie o stream e migre os factos
// com [CopiarStream].
func SemearStreamLegado(ctx context.Context, s SementeDeStream, streamID string, in EventInput, opts ...AppendOption) (AppendResult, error) {
	if !testing.Testing() {
		return AppendResult{}, fmt.Errorf("%w: SemearStreamLegado so corre sob `go test` — "+
			"em producao um stream_id tem de ser representavel nos dois backends (stream_id %q)",
			ErrConfig, streamID)
	}
	return s.Append(ctx, streamID, in, append(opts, semValidacaoDeNome())...)
}
