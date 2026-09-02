package backup

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// RETOMA DE MANIFESTO (AOS-101) — o que destranca um destino DURÁVEL.
//
// # O limite que este ficheiro fecha
//
// Até aqui [NewExporter] começava SEMPRE do génesis: manifesto vazio, cursor vazio, e por isso a
// referência do primeiro segmento de qualquer arranque era a MESMA. Sobre um destino que
// sobrevive ao processo, o segundo arranque colidia com [ErrImmutable] — e colidia para sempre,
// porque o índice nunca avançava. Era o motivo declarado pelo qual o nó exigia o destino
// INJECTADO e não inventava nenhum: nenhum backend durável era utilizável.
//
// # Onde o estado de retoma vive, e porquê AQUI e não num ponteiro mutável
//
// A cada ciclo escreve-se, no MESMO [ImmutableStore], um segundo objecto pequeno:
//
//	<região>/cycle-%08d   ⇒  { entry, checkpoint }
//
// A alternativa óbvia — um ponteiro mutável do tipo `latest-manifest` — seria exactamente o vector
// de rollback que [ErrCheckpointStale] existe para negar: quem conseguisse reescrever o ponteiro
// ressuscitava um head antigo. Um registo write-once sob object-lock não pode ser revertido, porque
// não pode ser apagado dentro do período de retenção. A imutabilidade que protege os segmentos
// passa a proteger também a âncora que os indexa, sem uma segunda porta e sem um segundo backend
// para operar.
//
// # Só o ÚLTIMO elo é preciso para retomar
//
// As três coisas que [NewExporter] tem de recuperar vivem todas no último [SegmentEntry]:
//
//	manifest.head() (o PrevHash do próximo elo)  ⇐  entry.EntryHash
//	lastExported[stream] (o cursor incremental)  ⇐  entry.StreamHeads
//	o próximo índice                             ⇐  entry.Index + 1
//
// Por isso NÃO se persiste o manifesto inteiro por ciclo: seria O(n²) de armazenamento para
// reconstruir uma coisa de que o arranque só precisa da última linha. A cadeia completa continua
// reconstruível — por [Restorer.LoadManifest], que a lê quando alguém RESTAURA, que é quando ela
// é de facto precisa.
//
// # O arranque VERIFICA antes de confiar, e recusa arrancar se não fechar
//
// Um registo de ciclo forjado com StreamHeads acima do real faria o exportador SALTAR eventos —
// perda de dados silenciosa dentro do próprio backup, que é o pior modo de falha deste módulo.
// [verifyCycleRecord] fecha isso com três verificações baratas: a assinatura do checkpoint contra
// a chave pública do signer, o EntryHash recomputado a partir do conteúdo canónico (que COBRE os
// StreamHeads), e a igualdade entre o head assinado e esse EntryHash. Qualquer uma que falhe é
// [ErrResumeUnverifiable] e o exportador NÃO é construído.
//
// # O que o arranque deliberadamente NÃO faz
//
// Não percorre a cadeia toda. Verificar N elos exigiria N gets dos SEGMENTOS (os objectos
// grandes) a cada reinício, e a verificação integral já vive onde pertence: em
// [Restorer.VerifyManifest], fail-closed, antes de um restauro. O arranque verifica O(1) elos e
// descobre o último ciclo em O(log N) gets de objectos pequenos.

// cycleRecord é o estado de retoma de UM ciclo: o elo do manifesto e o checkpoint que o autentica,
// escritos juntos num único objecto write-once. Juntos e não em dois objectos porque separá-los
// criaria um estado intermédio — elo sem checkpoint — que nada saberia interpretar.
type cycleRecord struct {
	Entry      SegmentEntry `json:"entry"`
	Checkpoint Checkpoint   `json:"checkpoint"`
}

// segmentRefHashBytes é quantos bytes do content-hash entram na referência do segmento. 8 bytes
// (16 hex) tornam uma colisão de prefixo praticamente impossível — e mesmo assim ela NÃO é
// assumida: [Exporter.putSegment] compara o hash INTEIRO antes de aceitar uma ref já existente.
const segmentRefHashBytes = 8

// cycleRef é a referência do registo de ciclo. É endereçada pelo ÍNDICE, e isso é deliberado: é
// o que a torna sondável em O(log N) e é o que faz com que dois exportadores sobre o mesmo destino
// COLIDAM aqui ([ErrChainOwned]) em vez de bifurcarem a cadeia em silêncio.
func cycleRef(region string, index uint64) string {
	return fmt.Sprintf("%s/cycle-%08d", normalizeRegion(region), index)
}

// segmentRef é a referência do segmento. É endereçada por CONTEÚDO — e é essa a diferença que
// torna a colisão estruturalmente impossível em vez de meramente evitada.
//
// O cenário que isto fecha: o ciclo escreve o segmento e o processo morre ANTES de escrever o
// registo de ciclo. No arranque seguinte a retoma dá o ciclo N-1, o próximo índice é N, e com uma
// ref puramente indexada o Put colidiria — o MESMO defeito, adiado um reinício. Com a ref a
// depender do conteúdo, a re-tentativa escreve conteúdo diferente (DEK fresca por segmento) e
// portanto uma ref diferente; o órfão fica retido pelo object-lock, não referenciado por registo
// de ciclo nenhum, identificável como lixo — e não como um bloqueio permanente.
//
// Inverter a ordem das duas escritas NÃO seria uma alternativa: um registo de ciclo a apontar
// para um segmento inexistente faria a verificação inteira do backup falhar, que é pior do que um
// órfão.
func segmentRef(region string, index uint64, contentHash []byte) string {
	h := contentHash
	if len(h) > segmentRefHashBytes {
		h = h[:segmentRefHashBytes]
	}
	return fmt.Sprintf("%s/seg-%08d-%s", normalizeRegion(region), index, hex.EncodeToString(h))
}

// maxCycleProbe limita a sondagem exponencial. Não é um tecto de ciclos do backup — é um travão
// contra um [ImmutableStore] avariado que devolvesse sempre "existe" e levasse a sondagem a
// duplicar o índice indefinidamente.
const maxCycleProbe = uint64(1) << 40

// cycleExists reporta se o registo do ciclo index está no destino. [ErrNotFound] é uma RESPOSTA
// (não existe), qualquer outro erro é propagado — um destino que não sabe responder não pode ser
// interpretado como um destino virgem.
func cycleExists(store ImmutableStore, region string, index uint64) (bool, error) {
	if _, err := store.Get(cycleRef(region, index)); err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// lastSealedCycle descobre o último ciclo selado no destino, em O(log N) gets: sondagem
// exponencial até passar o fim, depois bissecção. Devolve 0 num destino virgem.
//
// Assume a CONTIGUIDADE dos registos de ciclo — que é uma propriedade e não uma esperança: os
// registos são write-once sob object-lock, pelo que não podem ser apagados dentro do período de
// retenção, e o exportador só sela o ciclo N depois de ter selado o N-1. Um buraco no meio exigiria
// que a retenção tivesse expirado a meio da cadeia, que é a mesma condição que já tornaria o
// backup não-restaurável (o segmento correspondente também teria expirado).
func lastSealedCycle(store ImmutableStore, region string) (uint64, error) {
	ok, err := cycleExists(store, region, 1)
	if err != nil || !ok {
		return 0, err
	}
	lo, hi := uint64(1), uint64(2)
	for {
		ok, err := cycleExists(store, region, hi)
		if err != nil {
			return 0, err
		}
		if !ok {
			break
		}
		lo = hi
		if hi > maxCycleProbe {
			return 0, fmt.Errorf("%w: a sondagem passou %d ciclos sem encontrar o fim da cadeia", ErrResumeUnverifiable, maxCycleProbe)
		}
		hi *= 2
	}
	for hi-lo > 1 {
		mid := lo + (hi-lo)/2
		ok, err := cycleExists(store, region, mid)
		if err != nil {
			return 0, err
		}
		if ok {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo, nil
}

// loadCycleRecord lê e desserializa o registo do ciclo index. Um registo ilegível é
// [ErrResumeUnverifiable] e não um erro de JSON cru: para quem lê o log, "o estado de retoma não
// verifica" é a causa, e o detalhe do unmarshal é o sintoma.
func loadCycleRecord(store ImmutableStore, region string, index uint64) (cycleRecord, error) {
	blob, err := store.Get(cycleRef(region, index))
	if err != nil {
		return cycleRecord{}, err
	}
	var rec cycleRecord
	if err := json.Unmarshal(blob, &rec); err != nil {
		return cycleRecord{}, fmt.Errorf("%w: registo do ciclo %d ilegivel: %v", ErrResumeUnverifiable, index, err)
	}
	return rec, nil
}

// verifyCycleRecord faz valer, no ARRANQUE, que o estado de retoma é autêntico e coerente. Falha
// qualquer uma ⇒ [ErrResumeUnverifiable] e o exportador não chega a existir (fail-closed): é
// preferível um nó que recusa arrancar a um nó que continua uma cadeia que não prova ser sua.
func verifyCycleRecord(pub ed25519.PublicKey, region string, index uint64, rec cycleRecord) error {
	// 1) A assinatura é a raiz: sem ela, tudo o resto é auto-declarado pelo destino.
	if err := VerifyCheckpoint(pub, rec.Checkpoint); err != nil {
		return fmt.Errorf("%w: checkpoint do ciclo %d nao valida contra a chave publica do signer (%v) — o destino contem uma cadeia assinada por OUTRA chave, ou o registo foi forjado", ErrResumeUnverifiable, index, err)
	}
	// 2) O registo tem de dizer respeito ao ciclo que fomos buscar, e à região deste destino.
	if rec.Entry.Index != index || rec.Checkpoint.Cycle != index {
		return fmt.Errorf("%w: o registo do ciclo %d declara Entry.Index=%d e Checkpoint.Cycle=%d", ErrResumeUnverifiable, index, rec.Entry.Index, rec.Checkpoint.Cycle)
	}
	if normalizeRegion(rec.Checkpoint.Region) != normalizeRegion(region) {
		return fmt.Errorf("%w: o registo do ciclo %d e da regiao %q e o destino e da regiao %q (ADR-011)", ErrResumeUnverifiable, index, rec.Checkpoint.Region, region)
	}
	// 3) O elo recomputa. canonicalSegment COBRE os StreamHeads, que é o que torna esta
	//    verificação suficiente contra um cursor adulterado — um StreamHeads acima do real faria
	//    o exportador saltar eventos, e é o modo de falha que esta linha existe para excluir.
	if !bytes.Equal(computeEntryHash(rec.Entry.PrevHash, rec.Entry), rec.Entry.EntryHash) {
		return fmt.Errorf("%w: o EntryHash do ciclo %d nao recomputa a partir do conteudo canonico (ref, content-hash, contagem ou stream-heads adulterados)", ErrResumeUnverifiable, index)
	}
	// 4) E o head assinado é o desse elo — sem isto, (1) e (3) seriam verdadeiras sobre elos
	//    DIFERENTES e o exportador encadearia a partir do errado.
	if !bytes.Equal(rec.Checkpoint.HeadHash, rec.Entry.EntryHash) {
		return fmt.Errorf("%w: o head assinado no ciclo %d nao e o EntryHash do elo que o acompanha", ErrResumeUnverifiable, index)
	}
	return nil
}
