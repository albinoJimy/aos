package audit

import (
	"bytes"
	"context"
	"sort"
)

// AOS-221 — IMPOSIÇÃO da tamper-evidence do WORM. Este ficheiro dá aos consumidores
// do [Store] a forma de IMPOR (não apenas ter latente) a tamper-evidence da hash-chain
// no arranque e após um crypto-shred: re-encadear e verificar TODAS as partições, não
// só validar o CRC de framing do WAL. O CRC do WAL prova que os bytes gravados não se
// corromperam em disco; NÃO prova que o CONTEÚDO selado encadeia — um WAL cujo registo
// foi adulterado e o CRC recalculado passa o replay mas parte a hash-chain. A verificação
// aqui fecha exactamente esse vector.
//
// SEM CHAVE PRIVADA: re-encadear a hash-chain é um encadeamento de HASHES (SHA-256), não
// uma assinatura — não exige, nem toca, material de chave. O [Signer]/checkpoint assinado
// (âncora de FRESCURA que fecharia adicionalmente a truncatura do TAIL — ver o comentário
// de [Verify]) exige a chave PRIVADA do operador para SELAR checkpoints e, por isso, NÃO é
// composto no runtime do nó (a chave privada não vive no processo do nó). A verificação de
// hash-chain que aqui se entrega detecta MUTAÇÃO, REMOÇÃO INTERNA, INSERÇÃO e ENCADEAMENTO
// QUEBRADO com 100% de fiabilidade; a truncatura do tail permanece o único vector coberto
// só por um head assinado out-of-process (eixo AOS-072 — checkpoint como âncora de frescura;
// custódia da chave de assinatura out-of-process no mesmo molde de AOS-156).

// PartitionLister é a capacidade OPCIONAL de um [Store] enumerar as suas partições. O
// [Store] mínimo não a expõe (a fronteira estável é Append/Read/Head/At); mas para
// re-encadear a cadeia INTEIRA no arranque é preciso saber QUAIS partições existem. As
// implementações de referência [MemStore] e [FileStore] satisfazem-na. Um storage WORM
// externo que não a implemente não pode ser verificado por [VerifyStore] — que o declara
// fail-closed via [ErrPartitionsUnavailable] em vez de fingir que verificou.
type PartitionLister interface {
	// Partitions devolve os nomes de todas as partições com pelo menos um registo,
	// em ordem determinística (ordenada), para que a verificação seja reproduzível.
	Partitions() []string
}

// VerifyStore re-encadeia e verifica a hash-chain de TODAS as partições do store, de
// audit_seq=1 até ao head de cada uma, usando o [Verify] público (o MESMO verificador
// que detecta mutação/remoção/inserção/encadeamento-quebrado). Devolve o número de
// partições verificadas e, na PRIMEIRA cadeia adulterada, o [*VerifyError] (que
// desembrulha para [ErrTampered]) — o chamador impõe fail-closed (recusa arrancar/servir).
//
// É a via SEM CHAVE PRIVADA: não sela nem verifica assinaturas — só re-encadeia hashes.
// Um store que não saiba enumerar partições ([PartitionLister]) devolve
// [ErrPartitionsUnavailable] (fail-closed: não se declara verificado o que não se pôde
// percorrer). Uma partição vazia (head==0) é saltada. Um store sem partições devolve
// (0, nil) — não há cadeia a adulterar.
func VerifyStore(ctx context.Context, store Store) (int, error) {
	lister, ok := store.(PartitionLister)
	if !ok {
		return 0, ErrPartitionsUnavailable
	}
	verified := 0
	for _, p := range lister.Partitions() {
		head, err := store.Head(ctx, p)
		if err != nil {
			return verified, err
		}
		if head == 0 {
			continue
		}
		if err := Verify(ctx, store, p, 1, head); err != nil {
			return verified, err
		}
		verified++
	}
	return verified, nil
}

// verifyReplayedChain re-encadeia uma partição reconstruída de um WAL (fatia em ordem de
// escrita) a partir da GÉNESE, detectando a adulteração que o CRC de framing NÃO vê. É o
// núcleo que [OpenFileStore] corre no load, ANTES de servir a partição: sem chave privada,
// prova que os registos formam uma cadeia contígua e íntegra (audit_seq 1..k gapless,
// PrevHash encadeado, EntryHash recomputável). Devolve um [*VerifyError] no primeiro
// registo adulterado. Uma fatia vazia é uma cadeia válida vazia (nil).
func verifyReplayedChain(partition string, recs []AuditRecord) error {
	expectedSeq := uint64(1)
	expectedPrev := GenesisHash(partition)
	// Guarda o EntryHash de cada seq já verificado, para poder responder à pergunta que
	// distingue uma CORRIDA de uma ADULTERAÇÃO: o registo repetido encadeia no elo
	// anterior e recomputa? Sem isto, o duplicado é indistinguível de uma inserção.
	//
	// MAPA e não fatia: indexar uma fatia por um seq obriga a converter uint64→int, e
	// essa conversão é um transbordo à espera de acontecer (G115 do gosec, que a apanhou
	// aqui). Um mapa chaveado pelo próprio seq dispensa a conversão e a verificação de
	// limites — e diz melhor o que guarda.
	hashPorSeq := make(map[uint64][]byte, len(recs))
	for _, rec := range recs {
		switch {
		case rec.AuditSeq > expectedSeq:
			return tamper(TamperRemoval, partition, expectedSeq, "audit_seq em falta no replay (esperado contiguo)")
		case rec.AuditSeq < expectedSeq:
			// O REGISTO REPETIDO. Duas causas com o mesmo sintoma e remédios opostos.
			//
			// Se este registo encadeia no elo ANTERIOR e o seu EntryHash recomputa,
			// então é um segundo ramo BEM FORMADO — a assinatura de dois escritores que
			// partiram da mesma vista (AOS-284), e não a de quem enfia um registo no
			// meio de um log. Ver TamperFork para o que esta distinção NÃO prova.
			prevEsperadoAqui := GenesisHash(partition)
			if h, ok := hashPorSeq[rec.AuditSeq-1]; ok {
				prevEsperadoAqui = h
			}
			if bytes.Equal(rec.PrevHash, prevEsperadoAqui) &&
				bytes.Equal(ComputeEntryHash(rec.PrevHash, rec), rec.EntryHash) {
				return tamper(TamperFork, partition, rec.AuditSeq,
					"dois registos bem formados disputam este audit_seq — assinatura de escritores concorrentes na mesma particao, nao de adulteracao (AOS-284)")
			}
			return tamper(TamperInsertion, partition, rec.AuditSeq, "audit_seq duplicado ou fora de ordem no replay")
		}
		if !bytes.Equal(rec.PrevHash, expectedPrev) {
			return tamper(TamperChainBroken, partition, rec.AuditSeq, "prev_hash nao encadeia no entry_hash anterior (replay)")
		}
		if !bytes.Equal(ComputeEntryHash(rec.PrevHash, rec), rec.EntryHash) {
			return tamper(TamperMutation, partition, rec.AuditSeq, "entry_hash recalculado diverge do armazenado (replay)")
		}
		hashPorSeq[rec.AuditSeq] = rec.EntryHash
		expectedPrev = rec.EntryHash
		expectedSeq++
	}
	return nil
}

// sortedPartitions devolve as chaves de um mapa partição→registos por ordem
// determinística (usado por [MemStore.Partitions]/[FileStore.Partitions]).
func sortedPartitions(parts map[string][]AuditRecord) []string {
	out := make([]string, 0, len(parts))
	for p := range parts {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
