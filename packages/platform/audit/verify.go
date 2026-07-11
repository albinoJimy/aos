package audit

import (
	"bytes"
	"context"
)

// Verify percorre a hash-chain da partição de audit_seq=from a to (inclusive) e
// detecta com 100% de fiabilidade qualquer adulteração INTERNA ao intervalo pedido:
//
//   - MUTAÇÃO: o EntryHash recalculado a partir do conteúdo diverge do armazenado.
//   - REMOÇÃO INTERNA: gap ascendente em audit_seq, ou falta de registos até `to`.
//   - INSERÇÃO: audit_seq fora de ordem/duplicado, ou PrevHash que não encadeia.
//
// LIMITE (truncatura do tail): `to` é validado contra Head() — pedir to>head
// devolve [ErrRangeBeyondHead]. Logo, remover os registos MAIS RECENTES (truncar
// o tail) e depois chamar Verify(ctx, store, p, from, Head()) NÃO é detectável:
// o store reporta um head já reduzido e a verificação cobre só o que resta. A
// truncatura do tail só é detectável (a) ancorando num [Checkpoint] assinado cujo
// AuditSeq == head esperado (ver [VerifyFromCheckpoint], que rejeita to>head sobre
// o head assinado), ou (b) verificando até um `to` conhecido de forma independente
// do store (ex.: o último audit_seq registado por um observador externo). Produção
// DEVE, por disciplina operacional, ancorar a verificação num head assinado.
//
// A âncora do primeiro registo verificado é a génese da partição quando from==1;
// caso contrário é o EntryHash do registo em from-1 (lido do store). Para
// verificar grandes intervalos ancorados numa raiz de confiança assinada, use
// [VerifyFromCheckpoint].
//
// Devolve nil se a cadeia está íntegra no intervalo, ou um [*VerifyError]
// (que desembrulha para [ErrTampered]) identificando o registo e o tipo.
func Verify(ctx context.Context, store Store, partition string, from, to uint64) error {
	if from < 1 || to < from {
		return ErrInvalidRange
	}
	head, err := store.Head(ctx, partition)
	if err != nil {
		return err
	}
	if head == 0 {
		return ErrUnknownPartition
	}
	if to > head {
		return ErrRangeBeyondHead
	}

	// Âncora do primeiro registo do intervalo.
	var anchorPrev []byte
	if from == 1 {
		anchorPrev = GenesisHash(partition)
	} else {
		prevRec, ok, err := store.At(ctx, partition, from-1)
		if err != nil {
			return err
		}
		if !ok {
			// O registo âncora imediatamente anterior não existe: remoção.
			return tamper(TamperRemoval, partition, from-1, "registo ancora anterior ausente")
		}
		anchorPrev = prevRec.EntryHash
	}
	return verifyRange(ctx, store, partition, from, to, anchorPrev)
}

// verifyRange é o núcleo partilhado por [Verify] e [VerifyFromCheckpoint]:
// verifica que os registos [from,to] formam uma cadeia contígua e íntegra
// começando com PrevHash == anchorPrev.
func verifyRange(ctx context.Context, store Store, partition string, from, to uint64, anchorPrev []byte) error {
	recs, err := store.Read(ctx, partition, from, to)
	if err != nil {
		return err
	}

	expectedSeq := from
	expectedPrev := anchorPrev
	for _, rec := range recs {
		switch {
		case rec.AuditSeq > expectedSeq:
			// Faltam registos entre expectedSeq e este: remoção.
			return tamper(TamperRemoval, partition, expectedSeq,
				"audit_seq em falta (esperado contiguo)")
		case rec.AuditSeq < expectedSeq:
			// audit_seq repetido ou fora de ordem: inserção.
			return tamper(TamperInsertion, partition, rec.AuditSeq,
				"audit_seq duplicado ou fora de ordem")
		}

		// Encadeamento: o PrevHash tem de ser o EntryHash do anterior (ou a âncora).
		if !bytes.Equal(rec.PrevHash, expectedPrev) {
			return tamper(TamperChainBroken, partition, rec.AuditSeq,
				"prev_hash nao corresponde ao entry_hash anterior")
		}

		// Integridade do próprio registo: recalcular o EntryHash a partir do
		// conteúdo. Divergência ⇒ algum campo foi mutado.
		if !bytes.Equal(ComputeEntryHash(rec.PrevHash, rec), rec.EntryHash) {
			return tamper(TamperMutation, partition, rec.AuditSeq,
				"entry_hash recalculado diverge do armazenado")
		}

		expectedPrev = rec.EntryHash
		expectedSeq++
	}

	// Registos em falta no fim do intervalo (removidos do tail).
	if expectedSeq != to+1 {
		return tamper(TamperRemoval, partition, expectedSeq,
			"registos em falta no fim do intervalo")
	}
	return nil
}
