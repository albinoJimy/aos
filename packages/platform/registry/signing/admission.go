package signing

import (
	"context"
	"sort"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry/domain"
)

// DefaultAdmissionPartition é a partição da hash-chain de audit onde as decisões
// de verificação de admissão (aceite/recusada) se selam por omissão.
const DefaultAdmissionPartition = "registry.admission"

// capAdmissionVerify é a capability selada em cada decisão de verificação de
// assinatura (vocabulário estável, tamper-evident).
const capAdmissionVerify = "registry.admission.verify"

// Atributos de span da verificação (públicos por natureza — id/version/digest NÃO
// são segredos; a assinatura em si NUNCA entra num span, apenas o veredicto e a
// razão). Reutilizam a porta Tracer zero-dep do Agent Runtime (AOS-013).
const (
	attrArtifactID      = agentruntime.AttrToolName
	attrArtifactVersion = "aos.registry.version"
	attrArtifactDigest  = "aos.registry.digest"
	attrKeyID           = "aos.registry.key_id"
	attrDecision        = "aos.registry.decision"
	attrReason          = "aos.registry.reason"
)

const opVerifySignature = "registry.verify_signature"

// Result é o resultado de uma verificação de assinatura BEM-SUCEDIDA. Expõe o
// tuplo autenticado (id, version, digest), o key id do publicador confiável e —
// invariante ADR-006 — os AuthorizedScopes: os ÚNICOS scopes de credencial que o
// broker (BRK, EPIC-06) aceitará conceder à tool. Como a assinatura cobre o digest
// e o digest cobre o contrato, estes scopes ficam ligados à assinatura do
// publicador. NUNCA contém um segredo — só a DECLARAÇÃO de scopes.
type Result struct {
	// ID, Version, Digest são o tuplo autenticado pela assinatura.
	ID      string
	Version domain.Version
	Digest  string
	// KeyID é o publicador confiável cuja chave validou a assinatura.
	KeyID string
	// AuthorizedScopes são os scopes de credencial declarados no contract, em forma
	// canónica (ordenados, deduplicados). O broker concede EXACTAMENTE estes e nada
	// mais; o agente nunca vê o segredo subjacente.
	AuthorizedScopes []string
}

// Verifier verifica a assinatura de origem de artefactos do REG contra um
// [TrustStore] e sela cada decisão no audit WORM. Implementa (estruturalmente) a
// porta registry.AdmissionVerifier — o gate FAIL-CLOSED da transição staging→
// active de AOS-045 —, substituindo o placeholder allowVerifier pela verificação
// criptográfica real. É REUTILIZÁVEL pela revalidação por chamada (AOS-051) via
// [Verifier.VerifyEntry]. Construir com [NewVerifier].
type Verifier struct {
	trust     *TrustStore
	audit     audit.Store
	partition string
	tracer    agentruntime.Tracer
	now       func() time.Time
}

// VerifierOption configura o Verifier.
type VerifierOption func(*Verifier)

// WithAdmissionPartition define a partição de audit das decisões de verificação.
// Por omissão [DefaultAdmissionPartition].
func WithAdmissionPartition(p string) VerifierOption {
	return func(v *Verifier) {
		if p != "" {
			v.partition = p
		}
	}
}

// WithTracer injecta a porta de observabilidade (spans OTel GenAI). Por omissão
// NoopTracer. Os spans levam id/version/digest/decisão — nunca a assinatura nem
// qualquer segredo.
func WithTracer(t agentruntime.Tracer) VerifierOption {
	return func(v *Verifier) {
		if t != nil {
			v.tracer = t
		}
	}
}

// WithVerifierClock injecta o relógio (determinismo em testes; só para timestamps
// de audit).
func WithVerifierClock(f func() time.Time) VerifierOption {
	return func(v *Verifier) {
		if f != nil {
			v.now = f
		}
	}
}

// NewVerifier constrói o verificador de admissão sobre um [TrustStore] e um audit
// store. Fail-closed: trust store nil ou audit store nil devolvem erro — sem
// chaves de confiança nenhuma assinatura é verificável, e sem audit nenhuma
// decisão é selável (ambas pré-condições, ADR-010/012).
func NewVerifier(trust *TrustStore, auditStore audit.Store, opts ...VerifierOption) (*Verifier, error) {
	if trust == nil {
		return nil, ErrUntrustedKey
	}
	if auditStore == nil {
		return nil, ErrNoAuditStore
	}
	v := &Verifier{
		trust:     trust,
		audit:     auditStore,
		partition: DefaultAdmissionPartition,
		tracer:    agentruntime.NoopTracer{},
		now:       time.Now,
	}
	for _, o := range opts {
		o(v)
	}
	return v, nil
}

// Verify implementa a porta registry.AdmissionVerifier: decide se a entrada pode
// ser promovida a active. nil = admitida; um erro RECUSA a promoção (o artefacto
// permanece em staging). É um adaptador fino sobre [Verifier.VerifyEntry] que
// descarta o [Result] (o gate de admissão só precisa do veredicto).
func (v *Verifier) Verify(ctx context.Context, entry domain.Entry) error {
	_, err := v.VerifyEntry(ctx, entry)
	return err
}

// VerifyEntry é a verificação de assinatura REUTILIZÁVEL (admissão + revalidação
// por chamada AOS-051). Verifica, FAIL-CLOSED e por esta ordem:
//
//	(a) signature PRESENTE          → senão ErrSignatureMissing;
//	(b) publicador CONFIÁVEL        → chave no trust store, não-revogada, senão
//	                                  ErrUntrustedKey;
//	(c) assinatura VÁLIDA sobre o tuplo (id, version, digest) com essa chave →
//	                                  senão ErrSignatureInvalid.
//
// Cada decisão (aceite/recusada) sela-se no audit WORM com id, version, digest e
// resultado ANTES de devolver; uma falha de audit é ela própria fail-closed
// (ErrAuditFailed). Em sucesso devolve o [Result] com os AuthorizedScopes.
//
// A ordem — presença, depois confiança, depois criptografia — devolve o erro mais
// específico primeiro sem nunca revelar informação sensível (todos os caminhos são
// recusas explícitas). NOTA anti rug-pull: o passo (c) usa a chave PÚBLICA do
// publicador legítimo; um atacante que recalcule um digest coerente sobre conteúdo
// adulterado não consegue produzir uma assinatura que valide sob essa chave.
func (v *Verifier) VerifyEntry(ctx context.Context, entry domain.Entry) (Result, error) {
	ctx, span := v.tracer.StartSpan(ctx, opVerifySignature)
	defer span.End()
	span.SetAttribute(attrArtifactID, entry.ID)
	span.SetAttribute(attrArtifactVersion, entry.Version.String())
	span.SetAttribute(attrArtifactDigest, entry.Digest)

	// (a) Presença.
	if entry.Signature == "" {
		return v.deny(ctx, span, entry, "signature_missing", ErrSignatureMissing)
	}
	// (b) Confiança: a chave do publicador tem de estar no trust store e não-revogada.
	keyID := entry.Provenance.Publisher
	span.SetAttribute(attrKeyID, keyID)
	pub, ok := v.trust.Lookup(keyID)
	if !ok {
		return v.deny(ctx, span, entry, "untrusted_key", ErrUntrustedKey)
	}
	// (c) Criptografia: a assinatura tem de validar sobre (id, version, digest).
	if err := Verify(pub, entry.ID, entry.Version, entry.Digest, entry.Signature); err != nil {
		return v.deny(ctx, span, entry, "signature_invalid", ErrSignatureInvalid)
	}

	// Aceite: sela a decisão e devolve o resultado com os scopes autorizados.
	if err := v.record(ctx, entry, audit.DecisionAllow); err != nil {
		// Uma aceitação não-auditável não é admissível: degrada para recusa.
		span.SetAttribute(attrDecision, "error")
		span.SetAttribute(attrReason, "audit_failed")
		return Result{}, err
	}
	span.SetAttribute(attrDecision, "admitted")
	return Result{
		ID:               entry.ID,
		Version:          entry.Version,
		Digest:           entry.Digest,
		KeyID:            keyID,
		AuthorizedScopes: canonicalScopes(entry.Contract.CredentialScopes),
	}, nil
}

// deny sela a decisão de RECUSA no audit e devolve o erro sentinela. Se o próprio
// audit falhar, prevalece o ErrAuditFailed (fail-closed sobre fail-closed): a
// recusa mantém-se, mas o chamador sabe que nem sequer o rasto foi selado.
func (v *Verifier) deny(ctx context.Context, span agentruntime.Span, entry domain.Entry, reason string, cause error) (Result, error) {
	span.SetAttribute(attrDecision, "denied")
	span.SetAttribute(attrReason, reason)
	if err := v.record(ctx, entry, audit.DecisionDeny); err != nil {
		return Result{}, err
	}
	return Result{}, cause
}

// record sela uma decisão de verificação na hash-chain de audit com o tuplo
// exigido pelo critério de AOS-048: id (ToolID), version (PolicyVersion), digest
// (Resource.Value) e resultado (Decision allow/deny). StepID correlaciona o
// registo com a (id, version) concreta. Fail-closed: erro do store → ErrAuditFailed.
func (v *Verifier) record(ctx context.Context, entry domain.Entry, decision audit.Decision) error {
	rec := audit.AuditRecord{
		Partition:     v.partition,
		Timestamp:     v.now(),
		Decision:      decision,
		Capability:    capAdmissionVerify,
		ToolID:        entry.ID,
		PolicyVersion: entry.Version.String(),
		Resource:      audit.Resource{Type: "artifact.digest", Value: entry.Digest},
		RunID:         v.partition,
		StepID:        entry.ID + "@" + entry.Version.String(),
	}
	if _, err := v.audit.Append(ctx, rec); err != nil {
		return ErrAuditFailed
	}
	return nil
}

// canonicalScopes devolve os scopes ordenados e deduplicados — a MESMA forma
// canónica que o digest de AOS-047 sela no contrato, para que os AuthorizedScopes
// expostos coincidam EXACTAMENTE com os scopes cobertos pela assinatura.
func canonicalScopes(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
