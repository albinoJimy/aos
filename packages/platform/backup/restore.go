package backup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"sync"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// Restorer executa o Point-In-Time Recovery a partir de um backup imutável,
// VERIFICANDO o manifesto hash-chain no processo (ADR-010). Seguro para
// concorrência.
type Restorer struct {
	backup ImmutableStore
	vault  audit.KeyVault
	pub    ed25519.PublicKey
	now    func() time.Time

	mu           sync.Mutex
	lastEvidence RestoreEvidence
}

// RestorerOption configura o [Restorer].
type RestorerOption func(*Restorer)

// WithRestoreClock injecta o relógio (uso interno/testes determinísticos).
func WithRestoreClock(f func() time.Time) RestorerOption {
	return func(r *Restorer) { r.now = f }
}

// NewRestorer constrói um restaurador que lê os segmentos de backup e a KEK do
// vault, verificando os checkpoints contra a chave pública pub.
func NewRestorer(backup ImmutableStore, vault audit.KeyVault, pub ed25519.PublicKey, opts ...RestorerOption) (*Restorer, error) {
	if backup == nil || vault == nil {
		return nil, ErrConfig
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, ErrInvalidKey
	}
	r := &Restorer{backup: backup, vault: vault, pub: pub, now: time.Now}
	for _, o := range opts {
		o(r)
	}
	if r.now == nil {
		r.now = time.Now
	}
	return r, nil
}

// RestoreEvidence é a EVIDÊNCIA de um restauro (AC6): prova, com timestamp, que o
// backup foi restaurado e VERIFICADO por hash-chain até um head por stream.
type RestoreEvidence struct {
	Timestamp      time.Time         `json:"timestamp"`
	Verified       bool              `json:"verified"`
	Cycle          uint64            `json:"cycle"`
	Region         string            `json:"region"`
	HeadSeq        map[string]uint64 `json:"head_seq"`
	EventsRestored uint64            `json:"events_restored"`
	CheckpointHead []byte            `json:"checkpoint_head"`
}

// VerifyManifest verifica a integridade do backup (AC3) SEM restaurar:
//
//  1. valida a assinatura do checkpoint contra a chave pública (raiz de confiança);
//  2. rejeita ([ErrCheckpointStale]) um checkpoint anterior a expectedHead
//     (rollback/truncatura de tail — molde de audit.VerifyFromCheckpointAtHead);
//  3. confirma que o comprimento do manifesto == cp.Cycle e a região coincide;
//  4. recomputa a hash-chain de segmento em segmento: para cada segmento, busca o
//     blob no ImmutableStore, compara o SHA-256 com o ContentHash selado
//     ([ErrSegmentTampered]) e recomputa o EntryHash a partir do conteúdo canónico
//     ([ErrChainBroken] se divergir ou o encadeamento não fechar);
//  5. exige que o head recomputado == cp.HeadHash assinado.
//
// Uma adulteração de um segmento (o blob em repouso) OU de qualquer campo do
// manifesto (Ref, ContentHash, StreamHeads, ...) é detectada aqui.
func (r *Restorer) VerifyManifest(m Manifest, cp Checkpoint, expectedHead uint64) error {
	if err := VerifyCheckpoint(r.pub, cp); err != nil {
		return err
	}
	if cp.Cycle < expectedHead {
		return ErrCheckpointStale
	}
	if normalizeRegion(cp.Region) != normalizeRegion(m.Region) {
		return ErrChainBroken
	}
	if uint64(len(m.Segments)) != cp.Cycle {
		// Manifesto mais curto que o checkpoint ⇒ tail truncado; mais longo ⇒
		// checkpoint desactualizado para este manifesto. Em ambos os casos a âncora
		// não corresponde: recusa fail-closed.
		return ErrChainBroken
	}
	prev := genesisHash(m.Region)
	for i := range m.Segments {
		seg := m.Segments[i]
		if seg.Index != uint64(i)+1 {
			return ErrChainBroken
		}
		blob, err := r.backup.Get(seg.Ref)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(blob)
		if !bytes.Equal(sum[:], seg.ContentHash) {
			return ErrSegmentTampered
		}
		if !bytes.Equal(seg.PrevHash, prev) {
			return ErrChainBroken
		}
		if !bytes.Equal(computeEntryHash(prev, seg), seg.EntryHash) {
			return ErrChainBroken
		}
		prev = seg.EntryHash
	}
	if !bytes.Equal(prev, cp.HeadHash) {
		return ErrChainBroken
	}
	return nil
}

// RestoreTo executa o PITR até target (seq-alvo por stream; um stream ausente de
// target é restaurado por inteiro). Primeiro VERIFICA o backup (fail-closed: se a
// verificação falha, ABORTA antes de escrever qualquer evento); depois decifra os
// segmentos por ordem e reinsere os eventos com o ENVELOPE preservado no sink
// (eventstore.RestoreSink), até ao seq-alvo. Devolve e regista a [RestoreEvidence].
func (r *Restorer) RestoreTo(ctx context.Context, m Manifest, cp Checkpoint, expectedHead uint64, target map[string]uint64, sink eventstore.RestoreSink) (RestoreEvidence, error) {
	if err := ctx.Err(); err != nil {
		return RestoreEvidence{}, err
	}
	// 1) Verificação íntegra ANTES de qualquer escrita.
	if err := r.VerifyManifest(m, cp, expectedHead); err != nil {
		return RestoreEvidence{}, err
	}

	// 2) Decifra os segmentos por ordem e acumula os eventos por stream (contíguos e
	//    crescentes em seq, porque os segmentos estão em ordem de exportação).
	byStream := make(map[string][]eventstore.Event)
	for i := range m.Segments {
		seg := m.Segments[i]
		blob, err := r.backup.Get(seg.Ref)
		if err != nil {
			return RestoreEvidence{}, err
		}
		enc, err := unmarshalSegment(blob)
		if err != nil {
			return RestoreEvidence{}, ErrSegmentTampered
		}
		plaintext, err := openSegment(r.vault, enc)
		if err != nil {
			return RestoreEvidence{}, err
		}
		var sp segmentPayload
		if err := json.Unmarshal(plaintext, &sp); err != nil {
			return RestoreEvidence{}, ErrSegmentTampered
		}
		for st, evs := range sp.Streams {
			byStream[st] = append(byStream[st], evs...)
		}
	}

	// 3) Aplica o alvo de PITR por stream e reinsere (envelope preservado).
	heads := make(map[string]uint64)
	var totalRestored uint64
	for _, st := range sortedStreams(streamKeys(byStream)) {
		evs := byStream[st]
		if tgt, ok := target[st]; ok {
			evs = truncateToSeq(evs, tgt)
		}
		if len(evs) == 0 {
			continue
		}
		if err := sink.IngestStream(ctx, st, evs); err != nil {
			return RestoreEvidence{}, err
		}
		heads[st] = evs[len(evs)-1].Seq
		totalRestored += uint64(len(evs))
	}

	ev := RestoreEvidence{
		Timestamp:      r.now().UTC(),
		Verified:       true,
		Cycle:          cp.Cycle,
		Region:         normalizeRegion(m.Region),
		HeadSeq:        heads,
		EventsRestored: totalRestored,
		CheckpointHead: cloneBytes(cp.HeadHash),
	}
	r.mu.Lock()
	r.lastEvidence = ev
	r.mu.Unlock()
	return ev, nil
}

// LastEvidence devolve a evidência do último restauro bem-sucedido (AC6). Verified
// == false se ainda nenhum restauro correu.
func (r *Restorer) LastEvidence() RestoreEvidence {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastEvidence
}

// truncateToSeq devolve o prefixo de evs com seq <= tgt (os eventos vêm ordenados
// por seq ascendente e gapless).
func truncateToSeq(evs []eventstore.Event, tgt uint64) []eventstore.Event {
	n := 0
	for _, e := range evs {
		if e.Seq > tgt {
			break
		}
		n++
	}
	return evs[:n]
}

func streamKeys(m map[string][]eventstore.Event) map[string]uint64 {
	out := make(map[string]uint64, len(m))
	for k := range m {
		out[k] = 0
	}
	return out
}
