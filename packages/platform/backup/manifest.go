package backup

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"time"
)

// Domínios de separação (evitam confundir cadeias/checkpoints de partições ou
// versões distintas).
const (
	genesisPrefix    = "aos.backup.genesis:"
	segmentDomain    = "aos.backup.segment.v1"
	checkpointDomain = "aos.backup.checkpoint.v1"
)

// SegmentEntry é um elo do MANIFESTO hash-chain: descreve um segmento imutável do
// backup e encadeia-o ao anterior. O Event Store não tem cadeia nativa — ela é
// construída AQUI sobre os segmentos (molde de audit.ComputeEntryHash):
//
//	EntryHash = SHA-256( PrevHash || canonicalSegment(entry) )
//
// canonicalSegment cobre o Index, a Ref, o ContentHash (SHA-256 do ciphertext em
// repouso), a contagem de eventos e os StreamHeads — pelo que adulterar QUALQUER
// destes campos no manifesto (ou o blob apontado por ContentHash) quebra a cadeia
// e é detectado na verificação.
type SegmentEntry struct {
	Index       uint64            `json:"index"`        // 1-based, ordem de exportação
	Ref         string            `json:"ref"`          // ref no ImmutableStore
	ContentHash []byte            `json:"content_hash"` // SHA-256 do blob ciphertext
	Events      uint64            `json:"events"`       // nº de eventos no segmento
	StreamHeads map[string]uint64 `json:"stream_heads"` // head cumulativo por stream após este segmento
	PrevHash    []byte            `json:"prev_hash"`
	EntryHash   []byte            `json:"entry_hash"`
	CreatedAt   time.Time         `json:"created_at"` // observacional
}

// Manifest é o índice hash-chain do backup de uma região. Não contém plaintext
// nem segredos — só hashes, refs e cobertura de seq por stream.
type Manifest struct {
	Region   string         `json:"region"`
	Segments []SegmentEntry `json:"segments"`
}

// genesisHash devolve o PrevHash fixo do primeiro segmento de uma região.
func genesisHash(region string) []byte {
	sum := sha256.Sum256([]byte(genesisPrefix + normalizeRegion(region)))
	return sum[:]
}

// head devolve o EntryHash do último segmento, ou o genesis se o manifesto estiver
// vazio.
func (m *Manifest) head() []byte {
	if len(m.Segments) == 0 {
		return genesisHash(m.Region)
	}
	return cloneBytes(m.Segments[len(m.Segments)-1].EntryHash)
}

// canonicalSegment serializa os campos SEMÂNTICOS do segmento de forma
// determinística e estável cross-SO (comprimento-prefixado, sem depender da ordem
// de iteração de mapas — os StreamHeads são ordenados por chave).
func canonicalSegment(e SegmentEntry) []byte {
	buf := make([]byte, 0, 128)
	buf = putString(buf, segmentDomain)
	buf = putUint64(buf, e.Index)
	buf = putString(buf, e.Ref)
	buf = putBytes(buf, e.ContentHash)
	buf = putUint64(buf, e.Events)
	keys := make([]string, 0, len(e.StreamHeads))
	for k := range e.StreamHeads {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf = putUint64(buf, uint64(len(keys)))
	for _, k := range keys {
		buf = putString(buf, k)
		buf = putUint64(buf, e.StreamHeads[k])
	}
	return buf
}

// computeEntryHash calcula EntryHash = SHA-256(prevHash || canonicalSegment(e)).
func computeEntryHash(prevHash []byte, e SegmentEntry) []byte {
	h := sha256.New()
	h.Write(prevHash)
	h.Write(canonicalSegment(e))
	return h.Sum(nil)
}

// --- Checkpoint assinado ---------------------------------------------------

// Checkpoint é a âncora assinada do head do manifesto: sela, no ciclo Cycle, o
// EntryHash acumulado (HeadHash). Assinado com ed25519 — sem a chave privada não
// se forjam checkpoints novos, pelo que um manifesto truncado/adulterado não passa
// a verificação (ADR-010).
type Checkpoint struct {
	Region    string    `json:"region"`
	Cycle     uint64    `json:"cycle"` // == nº de segmentos selados
	HeadHash  []byte    `json:"head_hash"`
	Timestamp time.Time `json:"timestamp"`
	Signature []byte    `json:"signature"`
}

// canonicalCheckpoint serializa o checkpoint EXCLUINDO a Signature.
func canonicalCheckpoint(cp Checkpoint) []byte {
	buf := make([]byte, 0, 96)
	buf = putString(buf, checkpointDomain)
	buf = putString(buf, normalizeRegion(cp.Region))
	buf = putUint64(buf, cp.Cycle)
	buf = putBytes(buf, cp.HeadHash)
	buf = putInt64(buf, cp.Timestamp.UTC().UnixNano())
	return buf
}

// Signer assina checkpoints. A chave privada vive FORA do repositório (KMS/HSM em
// produção); os testes usam pares efémeros (ed25519.GenerateKey).
type Signer interface {
	Sign(message []byte) []byte
	Public() ed25519.PublicKey
}

// Ed25519Signer é a implementação de referência do [Signer].
type Ed25519Signer struct {
	priv ed25519.PrivateKey
}

// NewEd25519Signer constrói um Signer a partir de uma chave privada ed25519.
func NewEd25519Signer(priv ed25519.PrivateKey) (*Ed25519Signer, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, ErrInvalidKey
	}
	return &Ed25519Signer{priv: priv}, nil
}

// Sign implementa [Signer].
func (s *Ed25519Signer) Sign(message []byte) []byte { return ed25519.Sign(s.priv, message) }

// Public implementa [Signer].
func (s *Ed25519Signer) Public() ed25519.PublicKey { return s.priv.Public().(ed25519.PublicKey) }

// sealCheckpoint sela um head num checkpoint assinado.
//
// Recebe o ciclo e o head EXPLICITAMENTE, e não um *Manifest de onde os derivar, porque desde a
// retoma (AOS-101) o manifesto em memória deixou de ser a cadeia toda: um exportador retomado
// continua o ciclo N+1 com zero segmentos em memória, e `len(m.Segments)` daria 1. Derivar o ciclo
// da memória do processo produziria checkpoints com um Cycle errado — assinados, e portanto
// convincentes.
func sealCheckpoint(signer Signer, region string, cycle uint64, head []byte, now time.Time) Checkpoint {
	cp := Checkpoint{
		Region:    normalizeRegion(region),
		Cycle:     cycle,
		HeadHash:  cloneBytes(head),
		Timestamp: now.UTC(),
	}
	cp.Signature = signer.Sign(canonicalCheckpoint(cp))
	return cp
}

// VerifyCheckpoint valida a assinatura de um checkpoint contra a chave pública.
func VerifyCheckpoint(pub ed25519.PublicKey, cp Checkpoint) error {
	if len(pub) != ed25519.PublicKeySize {
		return ErrInvalidKey
	}
	if !ed25519.Verify(pub, canonicalCheckpoint(cp), cp.Signature) {
		return ErrCheckpointSignature
	}
	return nil
}

// --- Helpers de serialização canónica (comprimento-prefixado) --------------

func putUint64(buf []byte, v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return append(buf, b[:]...)
}

func putInt64(buf []byte, v int64) []byte { return putUint64(buf, uint64(v)) }

func putBytes(buf, b []byte) []byte {
	buf = putUint64(buf, uint64(len(b)))
	return append(buf, b...)
}

func putString(buf []byte, s string) []byte { return putBytes(buf, []byte(s)) }

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
