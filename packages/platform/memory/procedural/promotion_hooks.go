package procedural

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"strconv"

	"github.com/aos-ref/platform/memory/schema"
)

// ---------------------------------------------------------------------------
// Assinatura (ed25519, stdlib — AOS-005/006).
// ---------------------------------------------------------------------------

// Signer é a PORTA de assinatura das transições do pipeline. É ed25519 real, mas
// a chave é INJECTÁVEL (chaves de teste determinísticas). Assinar cada transição
// torna o audit trail não só tamper-evident (hash-chain) mas também atribuível a
// uma chave concreta. ed25519.Sign é determinístico (RFC 8032) — sem rand no
// caminho de decisão.
type Signer interface {
	// Sign assina msg e devolve a assinatura (64 bytes ed25519).
	Sign(msg []byte) []byte
	// PublicKey devolve a chave pública para verificação.
	PublicKey() ed25519.PublicKey
	// KID identifica a chave (key id) selado no registo para rotação/auditoria.
	KID() string
}

// Ed25519Signer é a impl de referência de [Signer] sobre uma chave ed25519.
type Ed25519Signer struct {
	priv ed25519.PrivateKey
	kid  string
}

// NewEd25519Signer constrói um Signer a partir de uma chave privada e um kid.
func NewEd25519Signer(priv ed25519.PrivateKey, kid string) *Ed25519Signer {
	return &Ed25519Signer{priv: priv, kid: kid}
}

// Sign implementa [Signer].
func (s *Ed25519Signer) Sign(msg []byte) []byte { return ed25519.Sign(s.priv, msg) }

// PublicKey implementa [Signer].
func (s *Ed25519Signer) PublicKey() ed25519.PublicKey {
	return s.priv.Public().(ed25519.PublicKey)
}

// KID implementa [Signer].
func (s *Ed25519Signer) KID() string { return s.kid }

// ---------------------------------------------------------------------------
// Ratificação humana assinada (ADR-012).
// ---------------------------------------------------------------------------

// Ratification é a ratificação HUMANA assinada exigida antes de produção. O
// humano assina (ed25519, fora do sistema) a mensagem canónica que liga o
// ratificador ao alvo (skill,versão); o [SkillMemory] verifica-a contra a
// allowlist de chaves de ratificadores autorizados. É o gate final: sem uma
// ratificação assinada VÁLIDA a activação é recusada.
type Ratification struct {
	// SkillName e Version identificam o alvo ratificado.
	SkillName string
	Version   schema.Version
	// RatifierID identifica o humano ratificador (indexa a chave pública autorizada).
	RatifierID string
	// Signature é a assinatura ed25519 sobre [CanonicalRatification].
	Signature []byte
}

// CanonicalRatification é a mensagem canónica e determinística que o humano
// assina para ratificar (skill,versão). Liga a assinatura ao alvo — ratificador,
// skill, versão E ContentHash do artefacto — para que a assinatura humana fique
// criptograficamente ligada aos BYTES exactos revistos, não apenas ao rótulo
// SemVer. Impede replay noutra skill/versão e defende em profundidade caso a
// imutabilidade (name,version)→conteúdo seja algum dia enfraquecida (ex.: um
// Registry EPIC-05 distribuído): a ratificação nunca cobre bytes diferentes dos
// revistos.
func CanonicalRatification(ratifierID, skillName string, v schema.Version, contentHash []byte) []byte {
	buf := make([]byte, 0, 96)
	buf = putString(buf, "aos.procedural.ratification.v1")
	buf = putString(buf, ratifierID)
	buf = putString(buf, skillName)
	buf = putString(buf, v.String())
	buf = putBytes(buf, contentHash)
	return buf
}

// SignRatification produz uma [Ratification] assinada (conveniência para o lado
// que detém a chave humana; em produção a assinatura é feita fora do sistema). O
// contentHash é o pin de integridade do artefacto revisto (Manifest.ContentHash),
// ligado à assinatura pela mensagem canónica.
func SignRatification(priv ed25519.PrivateKey, ratifierID, skillName string, v schema.Version, contentHash []byte) Ratification {
	msg := CanonicalRatification(ratifierID, skillName, v, contentHash)
	return Ratification{
		SkillName:  skillName,
		Version:    v,
		RatifierID: ratifierID,
		Signature:  ed25519.Sign(priv, msg),
	}
}

// ---------------------------------------------------------------------------
// Porta SkillRegistry (EPIC-05 — pin+hash+assinatura). NÃO implementa o EPIC-05.
// ---------------------------------------------------------------------------

// RegistrationRequest é o pedido de registo (pin) de um artefacto de skill no
// Skill/Tool Registry: identidade SemVer, hash de conteúdo e assinatura do
// manifesto. É o ponto de integração com o supply-chain do EPIC-05.
type RegistrationRequest struct {
	SkillName   string
	Version     schema.Version
	ContentHash []byte
	AuthorAgent string
	OriginRunID string
	Signature   []byte
	SignerKID   string
}

// PinRef é a referência PINADA devolvida pelo Registry: liga a (skill,versão) ao
// hash de conteúdo e à assinatura, imutavelmente. Reverificada na activação em
// produção (integridade supply-chain, fail-closed).
type PinRef struct {
	Ref         string
	ContentHash []byte
	Signature   []byte
	SignerKID   string
}

// SkillRegistry é a PORTA para o Skill/Tool Registry (EPIC-05): pin+hash+
// assinatura de artefactos. As impls reais (supply-chain, transparência) são do
// EPIC-05; aqui vive só o contrato e uma impl de referência in-memory.
type SkillRegistry interface {
	// Register pina o artefacto (imutável por (name,version)). Devolve a PinRef.
	Register(ctx context.Context, req RegistrationRequest) (PinRef, error)
	// Resolve devolve a PinRef de (name,version) e se existe.
	Resolve(ctx context.Context, name string, version schema.Version) (PinRef, bool, error)
}

// InMemorySkillRegistry é a impl de referência de [SkillRegistry]: pina em
// memória, imutável por (name,version). Segura para concorrência via cópia dos
// blobs; o [SkillMemory] serializa as escritas, mas Resolve pode ser concorrente.
type InMemorySkillRegistry struct {
	pins map[string]PinRef
}

// NewInMemorySkillRegistry constrói um registry de referência vazio.
func NewInMemorySkillRegistry() *InMemorySkillRegistry {
	return &InMemorySkillRegistry{pins: make(map[string]PinRef)}
}

func registryKey(name string, v schema.Version) string { return name + "@" + v.String() }

// Register implementa [SkillRegistry.Register]. Fail-closed: repin de uma
// (name,version) já existente é rejeitado (imutabilidade SemVer).
func (r *InMemorySkillRegistry) Register(_ context.Context, req RegistrationRequest) (PinRef, error) {
	k := registryKey(req.SkillName, req.Version)
	if _, ok := r.pins[k]; ok {
		return PinRef{}, ErrDuplicateVersion
	}
	pin := PinRef{
		Ref:         "registry://" + k,
		ContentHash: cloneBytes(req.ContentHash),
		Signature:   cloneBytes(req.Signature),
		SignerKID:   req.SignerKID,
	}
	r.pins[k] = pin
	return clonePin(pin), nil
}

// Resolve implementa [SkillRegistry.Resolve].
func (r *InMemorySkillRegistry) Resolve(_ context.Context, name string, version schema.Version) (PinRef, bool, error) {
	pin, ok := r.pins[registryKey(name, version)]
	if !ok {
		return PinRef{}, false, nil
	}
	return clonePin(pin), true, nil
}

func clonePin(p PinRef) PinRef {
	p.ContentHash = cloneBytes(p.ContentHash)
	p.Signature = cloneBytes(p.Signature)
	return p
}

// ---------------------------------------------------------------------------
// Porta EvalGate (EPIC-08 — golden-set + trace-diffing). NÃO implementa EPIC-08.
// ---------------------------------------------------------------------------

// EvalRequest é o pedido ao eval-gate: avalia a (skill,versão) candidata contra
// o golden-set e por trace-diffing vs a versão baseline (a prod actual).
type EvalRequest struct {
	SkillName       string
	Version         schema.Version
	BaselineVersion schema.Version
}

// EvalResult é o veredicto do eval-gate. Passed=true exige golden-set acima do
// limiar E zero (ou abaixo do limiar) regressões de trace-diffing. É o que é
// emitido no span OTel gen_ai.evaluation.result.
type EvalResult struct {
	Passed               bool
	GoldenSetScore       float64
	TraceDiffRegressions int
	BaselineVersion      schema.Version
	Detail               string
}

// EvalGate é a PORTA do eval-gate (EPIC-08). A impl real (golden-set curado,
// trace-diffing) é do EPIC-08; aqui vive o contrato e uma impl de referência.
type EvalGate interface {
	Evaluate(ctx context.Context, req EvalRequest) (EvalResult, error)
}

// ThresholdEvalGate é a impl de referência de [EvalGate]: aplica limiares
// determinísticos a métricas INJECTADAS (sem I/O, sem rand). Metrics devolve o
// golden-set score e o nº de regressões de trace-diffing da (skill,versão).
type ThresholdEvalGate struct {
	MinGoldenSetScore       float64
	MaxTraceDiffRegressions int
	Metrics                 func(name string, v schema.Version) (goldenScore float64, traceDiffRegressions int)
}

// Evaluate implementa [EvalGate]. Fail-closed: sem Metrics injectado, RECUSA.
func (g ThresholdEvalGate) Evaluate(_ context.Context, req EvalRequest) (EvalResult, error) {
	if g.Metrics == nil {
		return EvalResult{Passed: false, BaselineVersion: req.BaselineVersion, Detail: "sem metrics: fail-closed"}, nil
	}
	score, regressions := g.Metrics(req.SkillName, req.Version)
	passed := score >= g.MinGoldenSetScore && regressions <= g.MaxTraceDiffRegressions
	return EvalResult{
		Passed:               passed,
		GoldenSetScore:       score,
		TraceDiffRegressions: regressions,
		BaselineVersion:      req.BaselineVersion,
	}, nil
}

// ---------------------------------------------------------------------------
// Porta CanaryGate (EPIC-08 — success-rate + unsafe-action rate).
// ---------------------------------------------------------------------------

// CanaryRequest é o pedido ao canary: expõe a (skill,versão) a uma fatia e
// observa success-rate e unsafe-action rate.
type CanaryRequest struct {
	SkillName string
	Version   schema.Version
}

// CanaryResult é o veredicto do canary. Passed=true exige success-rate acima do
// limiar E unsafe-action rate abaixo do limiar.
type CanaryResult struct {
	Passed           bool
	SuccessRate      float64
	UnsafeActionRate float64
	Detail           string
}

// CanaryGate é a PORTA do canary (EPIC-08). Contrato + impl de referência.
type CanaryGate interface {
	Evaluate(ctx context.Context, req CanaryRequest) (CanaryResult, error)
}

// ThresholdCanaryGate é a impl de referência de [CanaryGate]: limiares
// determinísticos sobre métricas injectadas.
type ThresholdCanaryGate struct {
	MinSuccessRate      float64
	MaxUnsafeActionRate float64
	Metrics             func(name string, v schema.Version) (successRate float64, unsafeActionRate float64)
}

// Evaluate implementa [CanaryGate]. Fail-closed: sem Metrics injectado, RECUSA.
func (g ThresholdCanaryGate) Evaluate(_ context.Context, req CanaryRequest) (CanaryResult, error) {
	if g.Metrics == nil {
		return CanaryResult{Passed: false, Detail: "sem metrics: fail-closed"}, nil
	}
	success, unsafe := g.Metrics(req.SkillName, req.Version)
	passed := success >= g.MinSuccessRate && unsafe <= g.MaxUnsafeActionRate
	return CanaryResult{
		Passed:           passed,
		SuccessRate:      success,
		UnsafeActionRate: unsafe,
	}, nil
}

// ---------------------------------------------------------------------------
// Serialização canónica determinística (partilhada; espelha o audit AOS-011).
// ---------------------------------------------------------------------------

// computeContentHash devolve SHA-256(domínio || conteúdo) — o pin de integridade
// do artefacto de skill. O prefixo de domínio evita colisão cross-subsistema.
func computeContentHash(content []byte) []byte {
	h := sha256.New()
	h.Write([]byte("aos.procedural.content.v1:"))
	h.Write(content)
	sum := h.Sum(nil)
	return sum[:]
}

// putString escreve uma string com length-prefix (uvarint). Determinístico e
// estável cross-SO (mesmo padrão do audit AOS-011).
func putString(buf []byte, s string) []byte {
	var lb [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lb[:], uint64(len(s)))
	buf = append(buf, lb[:n]...)
	return append(buf, s...)
}

// putSortedMap escreve um mapa por ordem de chave (determinismo — mapas não têm
// ordem de iteração estável).
func putSortedMap(buf []byte, m map[string]string) []byte {
	var lb [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lb[:], uint64(len(m)))
	buf = append(buf, lb[:n]...)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		buf = putString(buf, k)
		buf = putString(buf, m[k])
	}
	return buf
}

// canonicalManifest é a serialização canónica e determinística do manifesto — a
// mensagem assinada no registo (pin) do artefacto (autor/run_id/hash/versão).
func canonicalManifest(m Manifest) []byte {
	buf := make([]byte, 0, 96)
	buf = putString(buf, "aos.procedural.manifest.v1")
	buf = putString(buf, m.SkillName)
	buf = putString(buf, m.Version.String())
	buf = putString(buf, m.AuthorAgent)
	buf = putString(buf, m.OriginRunID)
	buf = putBytes(buf, m.ContentHash)
	return buf
}

// canonicalTransition é a serialização canónica e determinística de uma transição
// de estado — a mensagem assinada por cada transição do pipeline. Liga a skill, a
// versão, o par (from,to), o actor, o timestamp e a evidência (extra ordenado).
func canonicalTransition(name string, v schema.Version, from, to Stage, actor string, tsUnixNano int64, extra map[string]string) []byte {
	buf := make([]byte, 0, 128)
	buf = putString(buf, "aos.procedural.transition.v1")
	buf = putString(buf, name)
	buf = putString(buf, v.String())
	buf = putString(buf, string(from))
	buf = putString(buf, string(to))
	buf = putString(buf, actor)
	buf = putInt64(buf, tsUnixNano)
	buf = putSortedMap(buf, extra)
	return buf
}

// ftoa serializa um float determinístico para o audit (evidência de gate).
func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 6, 64) }

// itoa serializa um int para o audit.
func itoa(n int) string { return strconv.Itoa(n) }

// putBytes escreve um blob com length-prefix (uvarint).
func putBytes(buf []byte, b []byte) []byte {
	var lb [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lb[:], uint64(len(b)))
	buf = append(buf, lb[:n]...)
	return append(buf, b...)
}

// putInt64 escreve um int64 em big-endian (largura fixa, determinístico).
func putInt64(buf []byte, v int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	return append(buf, b[:]...)
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
