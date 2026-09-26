package backup

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aos-ref/platform/audit"
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
// [verifyCycleRecord] fecha isso com verificações baratas: a assinatura do checkpoint contra a chave
// pública do signer, o índice e a região do registo, o EntryHash do ÚLTIMO elo recomputado a partir
// do conteúdo canónico (que COBRE os StreamHeads), e a igualdade entre o head assinado e esse
// EntryHash. Qualquer uma que falhe é [ErrResumeUnverifiable] e o exportador NÃO é construído. O
// PrevHash desse elo NÃO é conferido contra o elo anterior — isso é a verificação integral, que vive
// no [Restorer.VerifyManifest].
//
// E o segmento desse último elo tem de estar no destino, bater com o content-hash selado e ABRIR com
// a KEK do vault deste exportador ([checkLastSegmentOpens]). Sem isto, um processo com outra KEK
// (o vault de referência, em memória, nasce vazio a cada arranque; ou uma custódia cuja KEK do
// backup foi destruída ou é de outro mount) retomava a cadeia, o manifesto
// verificava, e o restauro falhava no dia do DR com [ErrSegmentTampered] — lido como adulteração
// quando a causa é uma custódia de chaves que não sobreviveu ao processo.
//
// Nada disto prova que a cadeia é deste LOG, e não se tenta no arranque: um PITR, um WAL truncado
// ou o DR real deixam o log atrás do cursor, e recusar a construção impediria o nó de subir quando
// mais precisa. É o [Exporter.Export] que recusa, sem escrever, com [ErrSourceBehindBackup]. O que
// nenhuma das duas apanha, e fica dito: uma cadeia de OUTRO log com a mesma chave cujo cursor está
// abaixo do head desta fonte, ou um log rebobinado que já voltou a crescer PARA LÁ do cursor — o head
// passa, e a história é outra. Distingui-lo exigiria o elo selar a identidade do último evento de
// cada stream, que hoje não carrega.
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

// lastSealedCycle descobre o último ciclo selado no destino, em O(log N) gets: sondagem nas
// potências de dois até ao PRIMEIRO ciclo presente, depois exponencial até passar o fim, depois
// bissecção. Devolve 0 se nenhuma potência de dois tiver registo.
//
// # A contiguidade NÃO é garantida, e o que se faz com isso
//
// Os registos são write-once sob object-lock — mas o object-lock só vale DENTRO da retenção. Quando
// ela expira, o ciclo de vida do destino apaga-os, e apaga PRIMEIRO os mais antigos: o prefixo da
// cadeia desaparece, que é o caso normal e não uma avaria. Por isso a sondagem não se fica pelo
// ciclo 1 (que leria como «destino virgem» toda a cadeia com o prefixo expirado, e recomeçaria no
// ciclo 1 por cima dela): procura o primeiro presente nas potências de dois — e, se o ciclo 1 falta
// e outro existe, RECUSA ([ErrResumeUnverifiable]). Uma cadeia INCREMENTAL sem génese não se
// restaura ([Restorer.LoadManifest] começa no ciclo 1, [Restorer.VerifyManifest] exige a génese, e
// cada segmento só tem o incremento): continuá-la seria anunciar «RETOMADA» sobre um backup que já
// não serve para nada. Um buraco no MEIO é apanhado por [checkNoCyclesBeyond].
//
// Uma retenção FINITA exige, por isso, snapshots completos periódicos (uma génese nova antes de a
// anterior expirar) — que este módulo não faz. Fica declarado.
//
// O QUE FICA POR APANHAR, e fica dito: uma janela retida que não contenha nenhuma potência de dois
// (todos os ciclos presentes entre 2^k+1 e 2^(k+1)-1) lê-se como destino virgem — e o banner diz
// «cadeia NOVA». O exportador selaria então um ciclo 1 novo, e a colisão chegaria mais tarde, no
// primeiro índice que ainda existe — como [ErrChainOwned], com a causa mal atribuída. Distingui-lo
// exigiria uma âncora que não expire, e a porta não tem listagem.
func lastSealedCycle(store ImmutableStore, region string) (uint64, error) {
	var lo uint64
	for p := uint64(1); p <= maxCycleProbe; p *= 2 {
		ok, err := cycleExists(store, region, p)
		if err != nil {
			return 0, err
		}
		if ok {
			lo = p
			break
		}
	}
	if lo == 0 {
		return 0, nil
	}
	if lo > 1 {
		return 0, fmt.Errorf("%w: o INICIO da cadeia expirou — o ciclo 1 falta e o ciclo %d existe; um backup incremental sem genese nao se restaura, e continua-lo seria anunciar uma retoma de um backup inutil. E preciso um destino (uma genese) novo", ErrResumeUnverifiable, lo)
	}
	hi := lo * 2
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

// checkNoCyclesBeyond exige que, depois do último ciclo encontrado, não haja mais nenhum à frente
// nas distâncias last+2, last+4, last+8, … . A bissecção garante que last+1 falta; se algum ciclo
// existir mais adiante, há um BURACO na cadeia, e retomar de `last` escreveria no buraco um elo que
// diverge do que lá esteve — uma cadeia que deixa de verificar a partir dele. Fail-closed:
// [ErrResumeUnverifiable]. ~40 gets de objectos pequenos, só no arranque. Um buraco cujo resto da
// cadeia não caia em nenhuma dessas distâncias escapa, e fica dito.
func checkNoCyclesBeyond(store ImmutableStore, region string, last uint64) error {
	for d := uint64(2); d <= maxCycleProbe; d *= 2 {
		idx := last + d
		ok, err := cycleExists(store, region, idx)
		if err != nil {
			return err
		}
		if ok {
			return fmt.Errorf("%w: buraco na cadeia de registos de ciclo — o ciclo %d falta e o ciclo %d existe; retomar escreveria no buraco um elo divergente", ErrResumeUnverifiable, last+1, idx)
		}
	}
	return nil
}

// checkLastSegmentOpens exige que o segmento do último elo esteja no destino, bata com o
// content-hash selado e ABRA com a KEK do vault deste exportador. É a verificação que a assinatura
// não faz: o checkpoint prova quem selou a cadeia, e nada diz sobre se esta custódia de chaves
// ainda decifra o que foi selado. Um segmento ausente é [ErrResumeUnverifiable] (o registo aponta
// para nada); um destino que não responde propaga o erro dele (não se lê como «em falta»).
func checkLastSegmentOpens(store ImmutableStore, vault audit.KeyVault, subjectID string, entry SegmentEntry) error {
	blob, err := store.Get(entry.Ref)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: o segmento %q do ciclo %d nao esta no destino", ErrResumeUnverifiable, entry.Ref, entry.Index)
		}
		return err
	}
	sum := sha256.Sum256(blob)
	if !bytes.Equal(sum[:], entry.ContentHash) {
		return fmt.Errorf("%w: o segmento %q do ciclo %d nao bate com o content-hash selado", ErrResumeUnverifiable, entry.Ref, entry.Index)
	}
	enc, err := unmarshalSegment(blob)
	if err != nil {
		return fmt.Errorf("%w: o segmento %q do ciclo %d e ilegivel: %v", ErrResumeUnverifiable, entry.Ref, entry.Index, err)
	}
	if _, err := openSegment(vault, subjectID, enc); err != nil {
		return fmt.Errorf("%w: a KEK do backup NAO e a que selou a cadeia — o segmento %q do ciclo %d nao abre com o vault deste exportador (%v). Retomar daria uma cadeia que verifica e nao restaura. A custodia RESPONDEU a sonda de composicao (nao esta em baixo): a KEK que selou esta cadeia nao e a dela — foi destruida (crypto-shred do titular do backup), e de outra custodia/mount, ou era o vault de referencia em memoria, que morre com o processo. Use um destino novo (epoca nova) ou reponha a custodia que selou a cadeia", ErrResumeUnverifiable, entry.Ref, entry.Index, err)
	}
	return nil
}

// conditionalProbeRef é a referência fixa da sonda de escrita condicional.
func conditionalProbeRef(region string) string {
	return normalizeRegion(region) + "/probe-conditional"
}

// probeConditionalPut faz valer, na construção, a propriedade de que a retoma depende: um Put numa
// referência existente devolve [ErrImmutable]. Escreve duas vezes a mesma sonda; a primeira pode já
// colidir (a sonda de um arranque anterior, num destino durável), a segunda TEM de colidir. Um
// destino que a aceite é [ErrDestinationNotConditional]; um que falhe de outra forma propaga o erro.
func probeConditionalPut(store ImmutableStore, region string, retainUntil time.Time) error {
	ref := conditionalProbeRef(region)
	blob := []byte("aos-backup: sonda de escrita condicional (AOS-101)")
	if err := store.Put(ref, blob, retainUntil); err != nil && !errors.Is(err, ErrImmutable) {
		return fmt.Errorf("backup: sonda de escrita condicional no destino %q: %w", ref, err)
	}
	err := store.Put(ref, blob, retainUntil)
	switch {
	case errors.Is(err, ErrImmutable):
		return nil
	case err == nil:
		return fmt.Errorf("%w (ref %q)", ErrDestinationNotConditional, ref)
	default:
		return fmt.Errorf("backup: sonda de escrita condicional no destino %q: %w", ref, err)
	}
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
