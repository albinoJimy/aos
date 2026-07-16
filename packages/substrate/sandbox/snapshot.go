package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// ImageVersion identifica a versão da imagem base de um [Snapshot]. É o eixo de
// imutabilidade: um snapshot base é imutável POR versão de imagem — dois restores
// da mesma versão partilham o MESMO base read-only e produzem overlays efémeros
// independentes.
type ImageVersion string

// Limites canónicos do tempo de RESTORE modelado (AOS-065, driver NFR): cada
// restore de uma microVM a partir do snapshot base custa 5–30 ms. O ambiente é
// Windows sem KVM — o timing REAL não é medível aqui; modela-se por uma
// [RestoreModel] injectável e a lógica (pool/reserva/reposição/métricas) é Go real
// e determinista. Os limites são IMPOSTOS por clamp em [Snapshot.Restore] para que
// a invariante "restore ∈ [5,30] ms" seja estrutural, não uma convenção do modelo.
const (
	// MinRestore é o piso do restore modelado (5 ms).
	MinRestore = 5 * time.Millisecond
	// MaxRestore é o tecto do restore modelado (30 ms).
	MaxRestore = 30 * time.Millisecond
)

// RestoreModel devolve a duração MODELADA do restore da atribuição de sequência
// seq. É injectável (determinismo): sem time.Now/rand reais na asserção. O valor é
// sempre fixado a [MinRestore, MaxRestore] por [Snapshot.Restore] (defesa em
// profundidade — um modelo fora de gama nunca quebra a invariante).
type RestoreModel func(seq uint64) time.Duration

// DefaultRestoreModel espalha determinísticamente as durações de restore por toda
// a gama [5,30] ms em função da sequência (26 valores distintos). Não usa relógio
// nem rand — é reprodutível call-a-call.
func DefaultRestoreModel(seq uint64) time.Duration {
	span := uint64((MaxRestore - MinRestore) / time.Millisecond) // 25
	return MinRestore + time.Duration(seq%(span+1))*time.Millisecond
}

// Snapshot é o snapshot BASE imutável de uma versão de imagem (AOS-065). É a raiz
// da invariante de isolamento: os bytes base NUNCA são mutados após [NewSnapshot];
// cada atribuição chama [Snapshot.Restore] e recebe um [Overlay] efémero (cópia-em-
// escrita) que escreve SÓ no seu próprio mapa. A execução N+1 nunca observa
// artefactos da execução N porque (1) o base é read-only e partilhado inalterado, e
// (2) o overlay sujo de N é DESCARTADO ([Overlay.Discard]) e nunca reciclado.
//
// Concorrente-seguro: o base é imutável (sem lock na leitura) e a sequência de
// restore é um contador atómico. Liga a AOS-066 (o overlay CoW aqui PREPARA o
// FS read-only + overlay efémero + seccomp concreto).
type Snapshot struct {
	version ImageVersion
	base    map[string][]byte // IMUTÁVEL após NewSnapshot; nunca mutado
	digest  string            // fingerprint estável do base (prova de imutabilidade)
	restore RestoreModel
	seq     atomic.Uint64 // sequência determinista de overlays
}

// SnapshotOption configura um [Snapshot].
type SnapshotOption func(*Snapshot)

// WithRestoreModel injecta o modelo de duração de restore (default
// [DefaultRestoreModel]). O valor é sempre fixado a [MinRestore, MaxRestore].
func WithRestoreModel(m RestoreModel) SnapshotOption {
	return func(s *Snapshot) {
		if m != nil {
			s.restore = m
		}
	}
}

// NewSnapshot constrói o snapshot base imutável de uma versão de imagem. O mapa
// base é COPIADO na construção (o chamador não pode mutar o base depois) e o digest
// é calculado uma vez — qualquer restore posterior prova, por [Overlay.BaseDigest],
// que o base é o mesmo. Fail-closed se a versão for vazia.
func NewSnapshot(version ImageVersion, base map[string][]byte, opts ...SnapshotOption) (*Snapshot, error) {
	if version == "" {
		return nil, ErrEmptyImageVersion
	}
	cp := make(map[string][]byte, len(base))
	for k, v := range base {
		b := make([]byte, len(v))
		copy(b, v)
		cp[k] = b
	}
	s := &Snapshot{
		version: version,
		base:    cp,
		restore: DefaultRestoreModel,
	}
	for _, o := range opts {
		o(s)
	}
	s.digest = digestOf(cp)
	return s, nil
}

// Version devolve a versão de imagem do snapshot.
func (s *Snapshot) Version() ImageVersion { return s.version }

// Digest devolve o fingerprint estável do base imutável (introspecção/testes). É
// constante ao longo da vida do snapshot — a prova de que nenhum restore o mutou.
func (s *Snapshot) Digest() string { return s.digest }

// Restore materializa uma microVM NOVA a partir do snapshot base: um [Overlay]
// efémero (cópia-em-escrita) com o base read-only partilhado e um mapa de escritas
// VAZIO. Devolve também a duração MODELADA do restore, fixada a [MinRestore,
// MaxRestore]. É a operação que garante o isolamento por execução: cada chamada
// começa de estado limpo; o overlay anterior nunca é reutilizado.
func (s *Snapshot) Restore() (*Overlay, time.Duration) {
	seq := s.seq.Add(1)
	d := s.restore(seq)
	if d < MinRestore {
		d = MinRestore
	}
	if d > MaxRestore {
		d = MaxRestore
	}
	return &Overlay{
		snap:   s,
		id:     string(s.version) + "-overlay-" + strconv.FormatUint(seq, 10),
		writes: map[string][]byte{},
	}, d
}

// Overlay é a vista de FS efémera (cópia-em-escrita) de UMA execução sobre o
// snapshot base imutável. As leituras caem no base read-only quando não há escrita
// local; as escritas ficam CONTIDAS no mapa privado writes — o base NUNCA é mutado.
// No fim, [Overlay.Discard] deita fora o estado sujo. Isto materializa a invariante
// de AOS-065 "cada execução recebe uma VM restaurada de snapshot limpo": não há
// caminho que recicle o overlay de uma execução para outra (o pool descarta-o e
// restaura um novo do base).
type Overlay struct {
	snap      *Snapshot
	id        string
	mu        sync.Mutex
	writes    map[string][]byte // cópia-em-escrita: SÓ as escritas desta execução
	discarded bool
}

// ID devolve o identificador efémero do overlay (único por restore).
func (o *Overlay) ID() string { return o.id }

// ImageVersion devolve a versão de imagem do snapshot de origem.
func (o *Overlay) ImageVersion() ImageVersion { return o.snap.version }

// BaseDigest devolve o digest do snapshot base — prova, entre execuções, que a base
// é a MESMA imagem imutável (o overlay nunca a altera).
func (o *Overlay) BaseDigest() string { return o.snap.digest }

// Read lê um caminho: devolve a escrita local (cópia-em-escrita) se existir; caso
// contrário, uma CÓPIA do base read-only. ok é falso se o caminho não existe nem no
// overlay nem no base, ou se o overlay já foi descartado. Nunca devolve o buffer
// interno (defesa: o chamador não pode mutar base nem writes por aliasing).
func (o *Overlay) Read(pathKey string) ([]byte, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.discarded {
		return nil, false
	}
	if v, ok := o.writes[pathKey]; ok {
		return cloneBytes(v), true
	}
	if v, ok := o.snap.base[pathKey]; ok {
		return cloneBytes(v), true
	}
	return nil, false
}

// Write escreve no overlay (cópia-em-escrita). NUNCA toca o base imutável. Falha
// com [ErrOverlayDiscarded] se o overlay já foi descartado (estado sujo não
// ressuscita).
func (o *Overlay) Write(pathKey string, data []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.discarded {
		return ErrOverlayDiscarded
	}
	o.writes[pathKey] = cloneBytes(data)
	return nil
}

// Dirty reporta se o overlay tem escritas locais (estado desta execução). Um
// overlay recém-restaurado é sempre limpo (Dirty()==false).
func (o *Overlay) Dirty() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.writes) > 0
}

// Discarded reporta se o overlay já foi deitado fora.
func (o *Overlay) Discarded() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.discarded
}

// Discard deita fora o estado sujo do overlay (idempotente). Após Discard as
// escrituras desaparecem e o overlay não volta a ser usável — o pool restaura um
// overlay NOVO do base para a próxima execução. É a prova estrutural de que o
// estado de uma execução nunca é reciclado.
func (o *Overlay) Discard() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.discarded = true
	o.writes = nil
}

// digestOf calcula um fingerprint estável (SHA-256) sobre o conteúdo do base,
// independente da ordem de iteração do mapa (chaves ordenadas). Stdlib apenas.
func digestOf(base map[string][]byte) string {
	keys := make([]string, 0, len(base))
	for k := range base {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	var lenbuf [8]byte
	for _, k := range keys {
		putUint64(lenbuf[:], uint64(len(k)))
		_, _ = h.Write(lenbuf[:])
		_, _ = h.Write([]byte(k))
		v := base[k]
		putUint64(lenbuf[:], uint64(len(v)))
		_, _ = h.Write(lenbuf[:])
		_, _ = h.Write(v)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// putUint64 serializa n em big-endian em b[:8] (comprimento-prefixo do digest).
func putUint64(b []byte, n uint64) {
	b[0] = byte(n >> 56)
	b[1] = byte(n >> 48)
	b[2] = byte(n >> 40)
	b[3] = byte(n >> 32)
	b[4] = byte(n >> 24)
	b[5] = byte(n >> 16)
	b[6] = byte(n >> 8)
	b[7] = byte(n)
}

// cloneBytes devolve uma cópia defensiva de b (nil-safe).
func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
