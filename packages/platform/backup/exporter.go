package backup

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// segmentPayload é o PLAINTEXT de um segmento antes da cifra: os eventos NOVOS
// (envelope intacto) por stream neste ciclo incremental. json.Marshal ordena as
// chaves de mapa, pelo que a serialização é determinística (reprodutível).
type segmentPayload struct {
	Streams map[string][]eventstore.Event `json:"streams"`
}

// Exporter exporta o Event Store para backup imutável de forma CONTÍNUA/
// INCREMENTAL. A cada ciclo ([Export]) lê os eventos novos (por stream, seq >
// último exportado), cifra-os em repouso, escreve-os como um segmento imutável no
// [ImmutableStore] e sela o head do manifesto hash-chain num [Checkpoint] assinado.
// Seguro para concorrência.
type Exporter struct {
	src      eventstore.BackupSource
	dst      ImmutableStore
	vault    audit.KeyVault
	signer   Signer
	rand     audit.RandSource
	subject  string
	policy   audit.RetentionPolicy
	retClass audit.DataClass

	periodicity time.Duration
	now         func() time.Time

	mu             sync.Mutex
	manifest       Manifest
	lastExported   map[string]uint64
	lastCheckpoint Checkpoint
	lastExportAt   time.Time
	started        bool
}

// ExporterOption configura o [Exporter].
type ExporterOption func(*Exporter)

// WithKeyVault injecta o audit.KeyVault (KMS/Vault) que detém a KEK do backup. Por
// omissão usa um InMemoryKeyVault de referência com a RandSource configurada.
func WithKeyVault(v audit.KeyVault) ExporterOption { return func(e *Exporter) { e.vault = v } }

// WithRandSource injecta a fonte de entropia (determinística em testes).
func WithRandSource(r audit.RandSource) ExporterOption { return func(e *Exporter) { e.rand = r } }

// WithPeriodicity define a periodicidade-alvo do ciclo de exportação (base do RPO).
func WithPeriodicity(d time.Duration) ExporterOption {
	return func(e *Exporter) { e.periodicity = d }
}

// WithRetention define a política de object-lock/retenção dos segmentos (reutiliza
// audit.RetentionPolicy). class é a classe de retenção aplicada aos segmentos.
func WithRetention(policy audit.RetentionPolicy, class audit.DataClass) ExporterOption {
	return func(e *Exporter) { e.policy = policy; e.retClass = class }
}

// WithClock injecta o relógio (uso interno/testes determinísticos).
func WithClock(f func() time.Time) ExporterOption { return func(e *Exporter) { e.now = f } }

// NewExporter constrói um exportador para (src → dst), validando a soberania
// fail-closed (ADR-011): se o destino cruza a fronteira regional do board, ou não
// declara região, devolve [ErrSovereigntyViolation] — o backup NUNCA é criado
// cross-border. signer sela os checkpoints (chave privada fora do repo).
func NewExporter(src eventstore.BackupSource, dst ImmutableStore, signer Signer, opts ...ExporterOption) (*Exporter, error) {
	if src == nil || dst == nil || signer == nil {
		return nil, ErrConfig
	}
	if err := checkSovereignty(src.Region(), dst.Region()); err != nil {
		return nil, err
	}
	e := &Exporter{
		src:          src,
		dst:          dst,
		signer:       signer,
		retClass:     audit.ClassAudit,
		periodicity:  30 * time.Second,
		now:          time.Now,
		manifest:     Manifest{Region: normalizeRegion(dst.Region())},
		lastExported: make(map[string]uint64),
	}
	for _, o := range opts {
		o(e)
	}
	if e.rand == nil {
		e.rand = cryptoRand
	}
	if e.vault == nil {
		e.vault = audit.NewInMemoryKeyVault(e.rand)
	}
	// O titular da KEK do backup é derivado da região de soberania: uma chave por
	// fronteira, nunca partilhada entre regiões.
	e.subject = backupSubjectPrefix + normalizeRegion(dst.Region())
	return e, nil
}

// ExportResult descreve o resultado de um ciclo de exportação.
type ExportResult struct {
	// Created indica se um segmento novo foi escrito (false ⇒ backup já em dia).
	Created bool
	// Cycle é o índice do último segmento selado (número de segmentos no manifesto).
	Cycle uint64
	// Ref é a referência do segmento escrito neste ciclo ("" se Created==false).
	Ref string
	// Events é o nº de eventos exportados neste ciclo.
	Events uint64
	// StreamHeads é o head cumulativo por stream após este ciclo.
	StreamHeads map[string]uint64
	// At é o instante do ciclo.
	At time.Time
	// Checkpoint é o checkpoint assinado do head após este ciclo.
	Checkpoint Checkpoint
}

// Export corre um ciclo incremental: captura os eventos committed ainda não
// exportados (por stream), cifra-os, escreve um segmento imutável e sela o head.
// Fail-closed: revalida a soberania a cada ciclo. Se nada mudou desde o último
// ciclo, não escreve segmento mas actualiza o relógio de RPO (o backup confirmou
// estar em dia com o head do Store).
func (e *Exporter) Export(ctx context.Context) (ExportResult, error) {
	if err := ctx.Err(); err != nil {
		return ExportResult{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	// Revalidação defensiva da fronteira (o destino não deve mudar de região, mas
	// nunca se assume — fail-closed).
	if err := checkSovereignty(e.src.Region(), e.dst.Region()); err != nil {
		return ExportResult{}, err
	}

	now := e.now().UTC()

	// 1) Recolhe os eventos novos por stream (incremental, gapless).
	streams := e.src.Streams()
	newByStream := make(map[string][]eventstore.Event)
	cumHeads := make(map[string]uint64)
	var total uint64
	for _, st := range streams {
		head, err := e.src.StreamHead(ctx, st)
		if err != nil {
			return ExportResult{}, err
		}
		cumHeads[st] = head
		from := e.lastExported[st]
		if head <= from {
			continue
		}
		evs, err := e.src.SnapshotStream(ctx, st, head)
		if err != nil {
			return ExportResult{}, err
		}
		// evs[i] tem seq i+1; os novos são os de índice >= from.
		newEvents := evs[from:]
		if len(newEvents) == 0 {
			continue
		}
		newByStream[st] = newEvents
		total += uint64(len(newEvents))
	}

	// 2) Nada novo: backup em dia. Actualiza o relógio de RPO e devolve.
	if total == 0 {
		e.lastExportAt = now
		return ExportResult{
			Created:     false,
			Cycle:       uint64(len(e.manifest.Segments)),
			StreamHeads: e.copyHeads(),
			At:          now,
			Checkpoint:  e.lastCheckpoint,
		}, nil
	}

	// 3) Serializa (determinístico) e cifra em repouso (envelope AES-256-GCM).
	plaintext, err := json.Marshal(segmentPayload{Streams: newByStream})
	if err != nil {
		return ExportResult{}, err
	}
	seg, err := sealSegment(e.vault, e.subject, plaintext, e.rand)
	if err != nil {
		return ExportResult{}, err
	}
	blob, err := marshalSegment(seg)
	if err != nil {
		return ExportResult{}, err
	}
	contentHash := sha256.Sum256(blob)

	// 4) Escreve o segmento IMUTÁVEL (write-once) com object-lock.
	index := uint64(len(e.manifest.Segments)) + 1
	ref := fmt.Sprintf("%s/seg-%08d", e.manifest.Region, index)
	retainUntil := retainUntilFor(e.policy, e.retClass, now)
	if err := e.dst.Put(ref, blob, retainUntil); err != nil {
		return ExportResult{}, err
	}

	// 5) Encadeia no manifesto e atribui o head cumulativo por stream.
	//    Os streams sem novidade herdam o head do último segmento.
	entryHeads := e.mergeHeads(cumHeads)
	entry := SegmentEntry{
		Index:       index,
		Ref:         ref,
		ContentHash: contentHash[:],
		Events:      total,
		StreamHeads: entryHeads,
		PrevHash:    e.manifest.head(),
		CreatedAt:   now,
	}
	entry.EntryHash = computeEntryHash(entry.PrevHash, entry)
	e.manifest.Segments = append(e.manifest.Segments, entry)

	// 6) Avança o cursor incremental e sela o head num checkpoint assinado.
	for st, h := range cumHeads {
		if h > e.lastExported[st] {
			e.lastExported[st] = h
		}
	}
	e.lastCheckpoint = sealCheckpoint(e.signer, &e.manifest, now)
	e.lastExportAt = now
	e.started = true

	// 7) PERSISTE o estado, para que um processo NOVO continue este backup em vez de
	//    recomeçar do génesis e colidir no primeiro segmento que já lá está. Ver retoma.go.
	if err := e.persistirEstado(index); err != nil {
		return ExportResult{}, err
	}

	return ExportResult{
		Created:     true,
		Cycle:       index,
		Ref:         ref,
		Events:      total,
		StreamHeads: cloneHeads(entryHeads),
		At:          now,
		Checkpoint:  e.lastCheckpoint,
	}, nil
}

// mergeHeads produz o head cumulativo por stream: os streams com novidade tomam o
// novo head; os restantes herdam o head do último segmento do manifesto.
func (e *Exporter) mergeHeads(cur map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64)
	if n := len(e.manifest.Segments); n > 0 {
		for k, v := range e.manifest.Segments[n-1].StreamHeads {
			out[k] = v
		}
	}
	for k, v := range cur {
		if v > out[k] {
			out[k] = v
		}
	}
	return out
}

func (e *Exporter) copyHeads() map[string]uint64 {
	if n := len(e.manifest.Segments); n > 0 {
		return cloneHeads(e.manifest.Segments[n-1].StreamHeads)
	}
	return map[string]uint64{}
}

func cloneHeads(m map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Manifest devolve uma cópia profunda do manifesto corrente (índice hash-chain).
func (e *Exporter) Manifest() Manifest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return cloneManifest(e.manifest)
}

// Checkpoint devolve o último checkpoint assinado do head do backup.
func (e *Exporter) Checkpoint() Checkpoint {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastCheckpoint
}

// Vault devolve o KeyVault usado (necessário ao restauro para decifrar).
func (e *Exporter) Vault() audit.KeyVault { return e.vault }

// Public devolve a chave pública de verificação dos checkpoints.
func (e *Exporter) Public() ed25519.PublicKey { return e.signer.Public() }

// Immutable devolve o ImmutableStore de destino.
func (e *Exporter) Immutable() ImmutableStore { return e.dst }

// Periodicity devolve a periodicidade-alvo configurada (base do RPO).
func (e *Exporter) Periodicity() time.Duration { return e.periodicity }

// WithinRPO indica se a periodicidade satisfaz o RPO-alvo (AC4): sob um ciclo a
// cada Periodicity(), a janela máxima de perda é limitada pela periodicidade.
func (e *Exporter) WithinRPO(target time.Duration) bool {
	return e.periodicity > 0 && e.periodicity <= target
}

// RPOWindow devolve a janela efectiva de RPO no instante now: o tempo desde que o
// backup confirmou estar em dia com o head do Store (now − último Export). Sob um
// loop a cada Periodicity(), este valor mantém-se <= Periodicity().
func (e *Exporter) RPOWindow(now time.Time) time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lastExportAt.IsZero() {
		return 0
	}
	return now.UTC().Sub(e.lastExportAt)
}

func cloneManifest(m Manifest) Manifest {
	out := Manifest{Region: m.Region, Segments: make([]SegmentEntry, len(m.Segments))}
	for i, s := range m.Segments {
		cp := s
		cp.ContentHash = cloneBytes(s.ContentHash)
		cp.PrevHash = cloneBytes(s.PrevHash)
		cp.EntryHash = cloneBytes(s.EntryHash)
		cp.StreamHeads = cloneHeads(s.StreamHeads)
		out.Segments[i] = cp
	}
	return out
}

// sortedStreams devolve os streams do manifesto ordenados (uso em restauro).
func sortedStreams(heads map[string]uint64) []string {
	out := make([]string, 0, len(heads))
	for k := range heads {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
