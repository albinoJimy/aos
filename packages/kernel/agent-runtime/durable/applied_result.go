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

// AppliedResultReader é o que [ReadAppliedResult] precisa do Event Store: só ler.
type AppliedResultReader interface {
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// ReadAppliedResult lê do LOG o resultado que o step-ledger gravou para UM passo de um run
// (AOS-498, ADR-038 §2.3 «A fonte dos bytes»): os bytes tal como o efeito os devolveu, decifrados
// por-titular com o opener dado quando o registo está selado (AOS-093).
//
// # Porque existe ao lado de [StepLedger.Rebuild]
//
// O `Rebuild` re-hidrata a projecção EM MEMÓRIA do ledger, que o nó partilha entre todos os runs e
// que guarda o resultado em claro. Servir uma leitura por ele deixava conteúdo de um titular em
// memória depois de o run acabar, à espera da poda. Esta função não tem estado: lê, decifra um
// registo e devolve. Não escreve no Event Store nem toca em projecção nenhuma.
//
// # O que NÃO faz
//
// Não autoriza ninguém e não confere o resultado contra nada. Quem chama tem de ter autorizado o
// leitor ANTES (o opener abre conteúdo de um titular), e tem de conferir os bytes contra o digest
// que o kernel selou para o passo — o `result_hash` do registo é do mesmo log e não é prova.
//
// # Erros
//
//   - [ErrAppliedResultNotFound]: não há registo para o passo (stream inexistente incluído);
//   - [ErrSealedResultNoCipher]: o registo está selado e não foi dado opener;
//   - o erro do opener, tal qual, quando a decifração falha — a KEK destruída pelo
//     crypto-shredding, ou a custódia fechada ([ErrConteudoIndisponivel]);
//   - o erro de leitura do Event Store, tal qual.
//
// Havendo mais de um registo para a chave, vale o último, como no [StepLedger.Rebuild].
func ReadAppliedResult(ctx context.Context, reader AppliedResultReader, opener agentruntime.ContentOpener, runID, stepID string) ([]byte, error) {
	if reader == nil {
		return nil, ErrNilStore
	}
	key, err := IdempotencyKey(runID, stepID)
	if err != nil {
		return nil, err
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
			return nil, fmt.Errorf("durable: registo do step-ledger de %q ilegivel: %w", runID, derr)
		}
		if r.Key == key {
			rec, found = r, true
		}
	}
	if !found {
		return nil, ErrAppliedResultNotFound
	}
	if !rec.Sealed {
		return rec.Result, nil
	}
	if opener == nil {
		return nil, ErrSealedResultNoCipher
	}
	return opener.OpenContent(ctx, rec.Subject, rec.Result)
}
