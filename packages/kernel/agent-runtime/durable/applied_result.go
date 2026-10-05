package durable

import (
	"context"
	"errors"
	"fmt"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// ErrAppliedResultNotFound — o log do run não tem um `step.ledger.applied` para o passo pedido.
// É DEFINITIVO: o passo não foi aplicado sob execução durável (o run correu sem step-ledger), ou
// o stream não é o do run. Não se confunde com conteúdo que existe e não abre
// ([ErrConteudoIndisponivel], o erro de decifração do cifrador).
var ErrAppliedResultNotFound = errors.New("durable: o step-ledger do run nao tem resultado aplicado para o passo pedido")

// ErrAppliedResultUnreadable — o resultado do passo NÃO SE CONSEGUE LER deste log, e não se vai
// conseguir: o stream do run tem um `step.ledger.applied` que não descodifica, ou o par
// (run, passo) pedido não forma uma chave de idempotência ([IdempotencyKey] recusa-o). É
// DEFINITIVO — um log danificado e um identificador mal formado não se corrigem com o tempo, e
// quem os tratasse como transitórios ficava a perguntar para sempre (revisão do AOS-498, M1).
//
// O erro que o causou vai embrulhado ao lado ([errors.Is] encontra os dois).
var ErrAppliedResultUnreadable = errors.New("durable: o resultado aplicado do passo nao e legivel deste log (registo do step-ledger ilegivel, ou passo que nao forma uma chave de idempotencia)")

// ErrAppliedResultInClear — o registo do passo tem conteúdo e NÃO está selado por-titular. Esta
// leitura só devolve conteúdo que o opener abriu (revisão do AOS-498, M2): um registo em claro
// não tem titular cuja KEK o apagamento destrua, e devolvê-lo era servir conteúdo por um caminho
// que nenhum gate de decifração guarda. A composição de produção nunca o escreve
// ([WithRequireTitular]); encontrá-lo é um log de outra composição. É DEFINITIVO.
var ErrAppliedResultInClear = errors.New("durable: o registo do passo tem conteudo em claro (nao selado por-titular) — esta leitura so devolve conteudo aberto pelo opener")

// AppliedResultReader é o que [ReadAppliedResult] precisa do Event Store: só ler.
type AppliedResultReader interface {
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// ReadAppliedResult lê do LOG o resultado que o step-ledger gravou para UM passo de um run
// (AOS-498, ADR-038 §2.3 «A fonte dos bytes»): os bytes tal como o efeito os devolveu, decifrados
// por-titular com o opener dado (AOS-093).
//
// # Porque existe ao lado de [StepLedger.Rebuild]
//
// O `Rebuild` re-hidrata a projecção EM MEMÓRIA do ledger, que o nó partilha entre todos os runs e
// que guarda o resultado em claro. Servir uma leitura por ele deixava conteúdo de um titular em
// memória depois de o run acabar, à espera da poda. Esta função não tem estado: lê, decifra um
// registo e devolve. Não escreve no Event Store nem toca em projecção nenhuma.
//
// # Só devolve conteúdo que o opener abriu
//
// O opener é OBRIGATÓRIO: sem ele não se lê nada, nem o log. E um registo com conteúdo que não
// esteja selado por-titular não é devolvido ([ErrAppliedResultInClear]) — o opener é a única
// porta por onde sai conteúdo desta função, para que quem o compõe atrás de um gate tenha o gate
// em TODAS as leituras, e para que o apagamento do titular (que destrói a KEK) retire sempre o
// conteúdo. Um resultado VAZIO não é conteúdo: o ledger não o sela, e lê-se vazio.
//
// # O que NÃO faz
//
// Não autoriza ninguém e não confere o resultado contra nada. Quem chama tem de ter autorizado o
// leitor ANTES (o opener abre conteúdo de um titular), e tem de conferir os bytes contra o digest
// que o kernel selou para o passo — o `result_hash` do registo é do mesmo log e não é prova.
//
// # Erros
//
//   - [ErrSealedResultNoCipher]: não foi dado opener;
//   - [ErrAppliedResultNotFound]: não há registo para o passo (stream inexistente incluído);
//   - [ErrAppliedResultUnreadable]: o stream tem um registo do ledger que não descodifica, ou o
//     par (run, passo) não forma uma chave de idempotência — definitivo;
//   - [ErrAppliedResultInClear]: o registo do passo tem conteúdo e não está selado — definitivo;
//   - o erro do opener, tal qual, quando a decifração falha — a KEK destruída pelo
//     crypto-shredding, ou a custódia fechada ([ErrConteudoIndisponivel]);
//   - o erro de leitura do Event Store, tal qual.
//
// A chave do registo é a de [IdempotencyKey], comparada INTEIRA: um registo de outro run que
// esteja neste stream, com o mesmo passo, não é o pedido. Havendo mais de um registo para a
// chave, vale o último, como no [StepLedger.Rebuild].
func ReadAppliedResult(ctx context.Context, reader AppliedResultReader, opener agentruntime.ContentOpener, runID, stepID string) ([]byte, error) {
	if reader == nil {
		return nil, ErrNilStore
	}
	if opener == nil {
		return nil, ErrSealedResultNoCipher
	}
	key, err := IdempotencyKey(runID, stepID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAppliedResultUnreadable, err)
	}
	events, err := reader.Read(ctx, runID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil, ErrAppliedResultNotFound
		}
		return nil, err
	}
	var (
		rec   ledgerRecord
		found bool
	)
	for _, e := range events {
		if e.Type != EventTypeLedgerApplied {
			continue
		}
		r, derr := decodeRecord(e.Payload)
		if derr != nil {
			return nil, fmt.Errorf("%w: stream de %q: %w", ErrAppliedResultUnreadable, runID, derr)
		}
		if r.Key == key {
			rec, found = r, true
		}
	}
	if !found {
		return nil, ErrAppliedResultNotFound
	}
	if !rec.Sealed {
		if len(rec.Result) != 0 {
			return nil, ErrAppliedResultInClear
		}
		return nil, nil
	}
	return opener.OpenContent(ctx, rec.Subject, rec.Result)
}
