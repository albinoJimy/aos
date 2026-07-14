package registry

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/registry/adapters"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/substrate/eventstore"
)

// Atributos de span específicos do REG (públicos por natureza — id/version/digest
// não são segredos; os credential_scopes são declarações). Reutilizam a porta
// Tracer zero-dep do Agent Runtime (AOS-013). NENHUM segredo é jamais colocado num
// span (nem signature em claro além do facto de existir, nem valores de credencial).
const (
	// AttrArtifactID — gen_ai.tool.name reutilizado para o id do artefacto.
	AttrArtifactID = agentruntime.AttrToolName
	// AttrArtifactVersion — versão SemVer pinada consultada.
	AttrArtifactVersion = "aos.registry.version"
	// AttrArtifactDigest — digest esperado devolvido (para o RM).
	AttrArtifactDigest = "aos.registry.digest"
	// AttrArtifactKind — tipo de artefacto.
	AttrArtifactKind = "aos.registry.kind"
	// AttrDecision — veredicto da consulta ("resolved"|"admitted"|"denied"|"not_found").
	AttrDecision = "aos.registry.decision"
)

// Nomes de operação dos spans de consulta do REG.
const (
	opResolve      = "registry.resolve"
	opGetDigest    = "registry.get_digest"
	opIsAdmissible = "registry.is_admissible"
)

// maxWriteRetries limita as retentativas de escrita optimista (CAS) antes de
// desistir com ErrConcurrentWrite (fail-closed, sem loop infinito).
const maxWriteRetries = 8

// AdmissionVerifier é o GATE de verificação da transição staging→active. É o PONTO
// DE EXTENSÃO onde AOS-047 (hash), AOS-048 (assinatura) e AOS-053 (eval-gate)
// impõem a verificação antes de promover um artefacto a active. Um Verify que
// devolva erro IMPEDE a promoção (fail-closed): o artefacto permanece em staging.
type AdmissionVerifier interface {
	// Verify decide se a entrada pode ser promovida a active. nil = admitido.
	Verify(ctx context.Context, entry domain.Entry) error
}

// allowVerifier é o AdmissionVerifier por omissão de AOS-045: admite qualquer
// entrada bem-formada. NÃO é um bypass — é o placeholder do gate cuja lógica
// criptográfica é dos tickets seguintes. A garantia estrutural de AOS-045 é que
// NENHUM artefacto salta para active sem PASSAR por este gate (publish só cria
// staging; active exige SetStatus, que invoca o verifier).
type allowVerifier struct{}

func (allowVerifier) Verify(context.Context, domain.Entry) error { return nil }

// PublishRequest descreve a publicação de um artefacto. A publicação entra SEMPRE
// em staging (nunca active). O digest é derivado do conteúdo canónico pelo Digester
// configurado; o timestamp e o estado de confiança inicial (first_seen) são
// atribuídos pelo REG.
type PublishRequest struct {
	// ID é o identificador estável do artefacto. Obrigatório.
	ID string
	// Version é a versão SemVer exacta. Obrigatória e não-zero (0.0.0 é o sentinela
	// de "sem versão", reservado à recusa de resolução flutuante).
	Version domain.Version
	// Kind é o tipo (skill/tool/servidor MCP). Obrigatório e válido.
	Kind domain.ArtifactKind
	// Contract é o contrato público de capability.
	Contract domain.Contract
	// Origin e Publisher preenchem a proveniência.
	Origin    string
	Publisher string
	// Signature é o campo RESERVADO da assinatura (AOS-048). Pode vir vazio.
	Signature string
}

// Registry é o serviço REG: o catálogo append-only e versionado sobre o Event Store
// (fonte de verdade, ADR-007). É seguro para concorrência (a ES serializa a escrita
// por quórum; o REG usa CAS optimista para a imutabilidade append-only).
type Registry struct {
	journal  *adapters.Journal
	tracer   agentruntime.Tracer
	digester domain.Digester
	verifier AdmissionVerifier
	now      func() time.Time
}

// Option configura o Registry.
type Option func(*Registry)

// WithTracer injecta a porta de observabilidade (spans OTel GenAI). Por omissão
// NoopTracer.
func WithTracer(t agentruntime.Tracer) Option {
	return func(r *Registry) {
		if t != nil {
			r.tracer = t
		}
	}
}

// WithDigester injecta o Digester. Por omissão o SHA-256 canonicalizado de
// AOS-047 (digest.SHA256Digester). Continua a ser o ponto de extensão para
// futuras gerações de hashing.
func WithDigester(d domain.Digester) Option {
	return func(r *Registry) {
		if d != nil {
			r.digester = d
		}
	}
}

// WithAdmissionVerifier injecta o gate de verificação staging→active (AOS-047/048/
// 053). Por omissão allowVerifier (placeholder que admite formas bem-formadas).
func WithAdmissionVerifier(v AdmissionVerifier) Option {
	return func(r *Registry) {
		if v != nil {
			r.verifier = v
		}
	}
}

// WithClock injecta o relógio (determinismo em testes; nunca time.Now numa decisão).
func WithClock(f func() time.Time) Option {
	return func(r *Registry) {
		if f != nil {
			r.now = f
		}
	}
}

// New constrói um Registry sobre o Event Store dado. store nil devolve ErrNoStore
// (a persistência append-only é a fonte de verdade; sem ela o serviço é inútil).
func New(store eventstore.EventStore, opts ...Option) (*Registry, error) {
	if store == nil {
		return nil, ErrNoStore
	}
	r := &Registry{
		journal: adapters.NewJournal(store, streamRegistry),
		tracer:  agentruntime.NoopTracer{},
		// AOS-047: o Digester por omissão é agora o SHA-256 canonicalizado (real),
		// substituindo o PlaceholderDigester não-criptográfico de AOS-045. Injectável
		// via WithDigester para testes/futuras gerações de hashing.
		digester: digest.SHA256Digester{},
		verifier: allowVerifier{},
		now:      time.Now,
	}
	for _, o := range opts {
		o(r)
	}
	return r, nil
}

// Publish admite um artefacto no catálogo em STAGING (nunca active). É append-only:
// publicar uma (id, version) já existente devolve ErrVersionExists (a imutabilidade
// impede a edição in-place; uma alteração exige uma NOVA versão). O digest é
// derivado do conteúdo canónico; a proveniência arranca em first_seen.
func (r *Registry) Publish(ctx context.Context, req PublishRequest) (domain.Entry, error) {
	if req.ID == "" || !req.Kind.Valid() || req.Version.IsZero() {
		return domain.Entry{}, ErrInvalidRequest
	}
	// AOS-047 (fail-closed): os schemas de I/O do contrato TÊM de ser JSON
	// bem-formado antes de serem pinados. Rejeitar aqui impede que um schema
	// malformado (ou com chaves duplicadas) seja hasheado como se fosse canónico
	// — o digest de um artefacto admitido cobre sempre conteúdo canonicalizável,
	// nunca bytes crus opacos. É o ponto de enforcement que fecha o fail-open do
	// fallback determinista de canonicalOrRaw.
	if err := validateContractSchemas(req.Contract); err != nil {
		return domain.Entry{}, err
	}
	entry := domain.Entry{
		ID:        req.ID,
		Version:   req.Version,
		Kind:      req.Kind,
		Digest:    r.digester.Digest(req.Kind, req.Contract),
		Signature: req.Signature,
		Contract:  req.Contract,
		Provenance: domain.Provenance{
			Origin:    req.Origin,
			Publisher: req.Publisher,
			Timestamp: r.now().UTC().Format(time.RFC3339Nano),
			// Nada é tratado como já-confiado: a admissão arranca em first_seen; a
			// ratificação a pinned e a detecção de changed são AOS-049.
			Trust: domain.TrustFirstSeen,
		},
		// Estado inicial OBRIGATÓRIO: staging. Nunca active na publicação.
		Status: domain.StatusStaging,
	}
	if err := entry.Validate(); err != nil {
		return domain.Entry{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}

	key := entry.Key()
	stepID := "published:" + keyString(key)
	for attempt := 0; attempt < maxWriteRetries; attempt++ {
		proj, lastSeq, err := r.snapshot(ctx)
		if err != nil {
			return domain.Entry{}, err
		}
		if _, exists := proj[key]; exists {
			return domain.Entry{}, ErrVersionExists
		}
		payload, err := encodePublished(entry)
		if err != nil {
			return domain.Entry{}, err
		}
		res, err := r.journal.Append(ctx, EventTypePublished, payload, stepID, req.Publisher, lastSeq)
		if err == nil {
			// TOCTOU: sob duas publish concorrentes da MESMA (id,version), o snapshot
			// acima pode ver o catálogo vazio em ambas antes de qualquer commit. O
			// Event Store verifica a idempotência (idempotency_key = stream:stepID,
			// com stepID "published:id@version") ANTES do expected_seq, logo o escritor
			// perdedor recebe StatusDuplicate + nil-error com o Event REALMENTE
			// armazenado (o do VENCEDOR). O primeiro commit vence e nunca é mutado — a
			// invariante append-only mantém-se — mas devolver entry.Clone() (o NOSSO
			// conteúdo, que não ficou armazenado) mentiria ao chamador com um falso
			// sucesso. Fail-closed: sinalizamos ErrVersionExists, coerente com o caminho
			// sequencial (a versão já existe; append-only exige uma nova versão).
			if res.Status == eventstore.StatusDuplicate {
				return domain.Entry{}, ErrVersionExists
			}
			return entry.Clone(), nil
		}
		if isConcurrency(err) {
			continue // outro escritor avançou o stream; reler e reavaliar.
		}
		return domain.Entry{}, err
	}
	return domain.Entry{}, ErrConcurrentWrite
}

// SetStatus transita o estado do ciclo de vida de uma versão pinada. A máquina de
// estados é fail-closed (domain.CanTransition); a transição staging→active passa
// OBRIGATORIAMENTE pelo gate de verificação (AdmissionVerifier) — é o único caminho
// para active e nunca um salto directo. Uma transição para o estado corrente é um
// no-op idempotente.
func (r *Registry) SetStatus(ctx context.Context, id string, v domain.Version, to domain.Status) (domain.Entry, error) {
	if id == "" || v.IsZero() {
		return domain.Entry{}, ErrInvalidRequest
	}
	if !to.Valid() {
		return domain.Entry{}, fmt.Errorf("%w: %v", ErrInvalidRequest, domain.ErrInvalidStatus)
	}
	key := domain.Key{ID: id, Version: v}
	for attempt := 0; attempt < maxWriteRetries; attempt++ {
		proj, lastSeq, err := r.snapshot(ctx)
		if err != nil {
			return domain.Entry{}, err
		}
		cur, ok := proj[key]
		if !ok {
			return domain.Entry{}, ErrNotFound
		}
		from := cur.Status
		if from == to {
			return cur.Clone(), nil // idempotente: sem facto novo.
		}
		if !domain.CanTransition(from, to) {
			return domain.Entry{}, fmt.Errorf("%w (%s->%s)", domain.ErrInvalidTransition, from, to)
		}
		// Gate de verificação da promoção a active (ponto de extensão AOS-047/048/053).
		if domain.RequiresAdmissionGate(from, to) {
			if verr := r.verifier.Verify(ctx, cur.Clone()); verr != nil {
				return domain.Entry{}, fmt.Errorf("%w: %v", ErrAdmissionDenied, verr)
			}
		}
		ts := r.now().UTC().Format(time.RFC3339Nano)
		payload, err := encodeStatusChanged(id, v, from, to, ts)
		if err != nil {
			return domain.Entry{}, err
		}
		// stepID único por posição (lastSeq) para que transições distintas nunca
		// colidam na chave de idempotência do Event Store; a correcção sob
		// concorrência é garantida pelo CAS (WithExpectedSeq), não pela dedup.
		stepID := "status:" + keyString(key) + ":" + string(to) + ":@" + strconv.FormatUint(lastSeq, 10)
		_, err = r.journal.Append(ctx, EventTypeStatusChanged, payload, stepID, cur.Provenance.Publisher, lastSeq)
		if err == nil {
			updated := cur.Clone()
			updated.Status = to
			return updated, nil
		}
		if isConcurrency(err) {
			continue
		}
		return domain.Entry{}, err
	}
	return domain.Entry{}, ErrConcurrentWrite
}

// Resolve devolve a entrada da (id, version) PINADA exacta. NÃO existe resolução por
// "latest"/referência flutuante: a versão tem de ser explícita e não-zero. Uma
// versão ausente (0.0.0) é recusada com ErrUnpinnedResolution; uma (id, version)
// fora do catálogo devolve ErrNotFound (default-deny).
func (r *Registry) Resolve(ctx context.Context, id string, v domain.Version) (domain.Entry, error) {
	ctx, span := r.tracer.StartSpan(ctx, opResolve)
	defer span.End()
	span.SetAttribute(AttrArtifactID, id)
	span.SetAttribute(AttrArtifactVersion, v.String())

	if id == "" || v.IsZero() {
		span.SetAttribute(AttrDecision, "denied")
		return domain.Entry{}, ErrUnpinnedResolution
	}
	proj, _, err := r.snapshot(ctx)
	if err != nil {
		span.SetAttribute(AttrDecision, "error")
		return domain.Entry{}, err
	}
	e, ok := proj[domain.Key{ID: id, Version: v}]
	if !ok {
		span.SetAttribute(AttrDecision, "not_found")
		return domain.Entry{}, ErrNotFound
	}
	span.SetAttribute(AttrArtifactKind, string(e.Kind))
	span.SetAttribute(AttrArtifactDigest, e.Digest)
	// AOS-047: COMPARAÇÃO NA RESOLUÇÃO (fail-closed). Recalcula o digest sobre o
	// conteúdo canonicalizado e compara com o digest ESPERADO gravado na entrada.
	// Uma divergência (conteúdo adulterado / digest forjado no log) BLOQUEIA a
	// admissão do artefacto no run com ErrDigestMismatch — nunca se resolve um
	// artefacto cujo conteúdo não coincide com o seu digest pinado.
	if err := r.verifyDigest(e); err != nil {
		span.SetAttribute(AttrDecision, "digest_mismatch")
		return domain.Entry{}, err
	}
	span.SetAttribute(AttrDecision, "resolved")
	return e.Clone(), nil
}

// validateContractSchemas impõe (fail-closed) que os schemas de I/O do contrato
// são JSON bem-formado e canonicalizável (sem chaves duplicadas). Um schema
// ausente/vazio é permitido (omitempty). Um schema malformado devolve um erro
// que satisfaz TANTO errors.Is(ErrInvalidRequest) — é um pedido de publicação
// inválido — QUANTO errors.Is(digest.ErrInvalidJSON) — a causa raiz é o JSON
// inválido —, permitindo ao chamador distinguir a natureza da recusa.
func validateContractSchemas(c domain.Contract) error {
	if _, err := digest.CanonicalJSON(c.InputSchema); err != nil {
		return fmt.Errorf("%w: input_schema: %w", ErrInvalidRequest, err)
	}
	if _, err := digest.CanonicalJSON(c.OutputSchema); err != nil {
		return fmt.Errorf("%w: output_schema: %w", ErrInvalidRequest, err)
	}
	return nil
}

// verifyDigest recalcula o digest do conteúdo canonicalizado da entrada e
// compara-o com o digest esperado (e.Digest). Devolve ErrDigestMismatch em caso
// de divergência (fail-closed). É a peça de integridade reutilizada pela
// resolução (RT) e pela consulta de digest (RM); a revalidação por chamada
// (AOS-051) reutilizará a MESMA porta digest.Compare.
func (r *Registry) verifyDigest(e domain.Entry) error {
	computed := r.digester.Digest(e.Kind, e.Contract)
	return digest.Compare(e.Digest, computed)
}

// ResolveString resolve por uma referência textual, REJEITANDO explicitamente as
// referências flutuantes ("latest", "main", vazio, ou qualquer coisa que não seja um
// SemVer X.Y.Z exacto) com ErrFloatingResolution. É o ponto onde o pinning obrigatório
// se torna visível ao chamador (o RT nunca resolve por latest).
func (r *Registry) ResolveString(ctx context.Context, id, ref string) (domain.Entry, error) {
	v, err := domain.ParseVersion(ref)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("%w: %q", ErrFloatingResolution, ref)
	}
	return r.Resolve(ctx, id, v)
}

// GetDigest devolve o digest ESPERADO da (id, version) pinada, para o Reference
// Monitor revalidar a definição prestes a executar (AOS-051 fará a comparação). Uma
// capacidade fora do catálogo devolve ErrNotFound (default-deny); o zero-value ""
// nunca passa como digest válido (fail-closed no caminho de ausência).
//
// CONTRATO (default-deny, ADR-002): GetDigest NÃO É um gate de autorização e NUNCA
// autoriza despacho por si só. Deliberadamente NÃO filtra por status — devolve o
// digest mesmo de artefactos staging/deprecated/revoked — porque o seu único papel é
// entregar o digest esperado para comparação de integridade. A decisão de
// despachabilidade pertence SEMPRE a IsAdmissible (que aplica o default-deny por
// estado). O RM DEVE combinar GetDigest com IsAdmissible: usar o digest isoladamente
// aceitaria o digest de um artefacto revogado.
func (r *Registry) GetDigest(ctx context.Context, id string, v domain.Version) (string, error) {
	ctx, span := r.tracer.StartSpan(ctx, opGetDigest)
	defer span.End()
	span.SetAttribute(AttrArtifactID, id)
	span.SetAttribute(AttrArtifactVersion, v.String())

	if id == "" || v.IsZero() {
		span.SetAttribute(AttrDecision, "denied")
		return "", ErrUnpinnedResolution
	}
	proj, _, err := r.snapshot(ctx)
	if err != nil {
		span.SetAttribute(AttrDecision, "error")
		return "", err
	}
	e, ok := proj[domain.Key{ID: id, Version: v}]
	if !ok {
		span.SetAttribute(AttrDecision, "not_found")
		return "", ErrNotFound
	}
	span.SetAttribute(AttrArtifactDigest, e.Digest)
	// AOS-047: o digest entregue ao RM é ele próprio verificado contra o conteúdo
	// (fail-closed) — um digest que não coincida com o conteúdo canonicalizado
	// nunca sai como "esperado" para revalidação.
	if err := r.verifyDigest(e); err != nil {
		span.SetAttribute(AttrDecision, "digest_mismatch")
		return "", err
	}
	span.SetAttribute(AttrDecision, "resolved")
	return e.Digest, nil
}

// IsAdmissible é o ponto de consulta do DEFAULT-DENY (ADR-002): uma capacidade só é
// despachável se estiver no catálogo E em estado active. Tudo o resto — fora do
// catálogo, em staging, deprecated ou revoked — é NEGADO. Devolve o veredicto e uma
// razão legível (não-erro: a não-admissão é um resultado normal, não uma falha).
//
// AOS-047 (defense-in-depth, fail-closed): mesmo para uma entrada active, a
// admissibilidade RECALCULA e compara o digest do conteúdo. Uma divergência de
// integridade (conteúdo adulterado / digest forjado no log) NÃO é um "deny"
// normal mas uma FALHA de integridade — devolvida como erro ([ErrDigestMismatch]),
// coerente com Resolve/GetDigest. Assim o gate fecha mesmo quando um integrador
// despacha só com base no veredicto de admissibilidade, sem passar por Resolve.
func (r *Registry) IsAdmissible(ctx context.Context, id string, v domain.Version) (bool, string, error) {
	ctx, span := r.tracer.StartSpan(ctx, opIsAdmissible)
	defer span.End()
	span.SetAttribute(AttrArtifactID, id)
	span.SetAttribute(AttrArtifactVersion, v.String())

	if id == "" || v.IsZero() {
		span.SetAttribute(AttrDecision, "denied")
		return false, "referencia nao pinada", nil
	}
	proj, _, err := r.snapshot(ctx)
	if err != nil {
		span.SetAttribute(AttrDecision, "error")
		return false, "", err
	}
	e, ok := proj[domain.Key{ID: id, Version: v}]
	if !ok {
		span.SetAttribute(AttrDecision, "denied")
		return false, "fora do catalogo (default-deny)", nil
	}
	if e.Status != domain.StatusActive {
		span.SetAttribute(AttrArtifactKind, string(e.Kind))
		span.SetAttribute(AttrDecision, "denied")
		return false, "estado nao-active: " + string(e.Status), nil
	}
	span.SetAttribute(AttrArtifactKind, string(e.Kind))
	span.SetAttribute(AttrArtifactDigest, e.Digest)
	// Verificação de integridade fail-closed: um artefacto active cujo conteúdo
	// já não coincide com o digest pinado NÃO é admissível (erro de integridade,
	// não deny de política).
	if err := r.verifyDigest(e); err != nil {
		span.SetAttribute(AttrDecision, "digest_mismatch")
		return false, "", err
	}
	span.SetAttribute(AttrDecision, "admitted")
	return true, "", nil
}

// snapshot relê o log e reconstrói a projecção corrente do catálogo (a ES é a fonte
// de verdade; não há estado autoritativo em RAM). Devolve a projecção e o último seq
// (para o CAS optimista das escritas).
func (r *Registry) snapshot(ctx context.Context) (map[domain.Key]domain.Entry, uint64, error) {
	evs, last, err := r.journal.ReadAll(ctx)
	if err != nil {
		return nil, 0, err
	}
	proj, err := foldProjection(evs)
	if err != nil {
		return nil, 0, err
	}
	return proj, last, nil
}

// foldProjection reconstrói o estado corrente por replay determinístico do log:
// published cria a entrada (staging); status_changed avança o estado. Fail-closed:
// um payload corrompido no log é propagado (o replay nunca inventa estado).
func foldProjection(evs []eventstore.Event) (map[domain.Key]domain.Entry, error) {
	proj := make(map[domain.Key]domain.Entry, len(evs))
	for _, ev := range evs {
		switch ev.Type {
		case EventTypePublished:
			e, err := decodePublished(ev.Payload)
			if err != nil {
				return nil, err
			}
			proj[e.Key()] = e
		case EventTypeStatusChanged:
			p, err := decodeStatusChanged(ev.Payload)
			if err != nil {
				return nil, err
			}
			v, err := domain.ParseVersion(p.Version)
			if err != nil {
				return nil, err
			}
			key := domain.Key{ID: p.ID, Version: v}
			if e, ok := proj[key]; ok {
				e.Status = p.To
				proj[key] = e
			}
		default:
			// Tipos desconhecidos são ignorados (tolerância a eventos futuros no
			// mesmo stream); nunca alteram o estado do catálogo.
		}
	}
	return proj, nil
}

// keyString devolve a forma estável "id@version" de uma chave pinada (para stepIDs
// de idempotência e diagnóstico).
func keyString(k domain.Key) string { return k.ID + "@" + k.Version.String() }

// isConcurrency indica se o erro do Event Store é um conflito de concorrência
// optimista (o chamador deve reler e reavaliar) — ver a semântica de WithExpectedSeq
// em eventstore.Append (tanto ErrSeqConflict como ErrAppendOnlyViolation sinalizam
// "reavaliar", não uma violação genuína neste protocolo de retry).
func isConcurrency(err error) bool {
	return errors.Is(err, eventstore.ErrSeqConflict) || errors.Is(err, eventstore.ErrAppendOnlyViolation)
}
