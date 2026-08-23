package hitl

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"math"
	"strconv"
	"time"

	"github.com/aos-ref/platform/audit"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// AOS-096 — Gate de ratificação de auto-modificação (tecnica/09 §9, ADR-012, ADR-010).
//
// A auto-modificação (skills auto-escritas, memória procedural) é a mudança de MAIOR
// risco do sistema — a misevolution ocorre mesmo sem atacante. O pipeline completo é
// staging → eval-gate → canary → RATIFICAÇÃO → produção (SemVer + rollback atómico).
// Este ficheiro entrega o ponto não-negociável da governação: NENHUM artefacto
// auto-escrito chega a PRODUÇÃO sem RATIFICAÇÃO HUMANA ASSINADA. O [RatificationGate]
// interpõe-se entre o canary e a produção no promotion controller.
//
// # COMPÕE, não reimplementa
//
// O gate é uma COMPOSIÇÃO de peças já existentes — não duplica assinatura nem
// eval-gate:
//   - [otelgenai.EvalGate] + [otelgenai.EvaluationResult] (AOS-084): a PRÉ-CONDIÇÃO de
//     eval. Só o que o eval-gate ADMITE (e passou o canary) é apresentável a ratificação.
//   - [SignedApproval] + [verifyApproval] + [ApproverRegistry] (AOS-095): a ratificação
//     É uma aprovação assinada ed25519 (não-repúdio), verificada contra a chave PINADA
//     do humano responsável. Reutiliza-se o mesmo [SignApproval]/[verifyApproval] — a
//     ratificação é a decisão assinada cujo RequestID É a identidade do artefacto+eval.
//   - [audit.Store] (AOS-072): a cadeia WORM tamper-evident onde a ratificação/recusa é
//     SELADA (quem/SemVer/eval/timestamp), ligada ao gen_ai.evaluation.result.
//
// # Fail-closed em toda a fronteira
//
// O default é NÃO promover. A promoção (admit=true) exige, cumulativamente: (1) a
// pré-condição eval-gate+canary; (2) uma ratificação assinada VERIFICÁVEL de um humano
// responsável AUTORIZADO; (3) a selagem no audit. Falha de qualquer uma → admit=false,
// o artefacto fica em canary/staging, NUNCA em prod. Ratificação ausente, forjada, de
// não-autorizado, ou recusa assinada → block.
//
// # Anti-transplante
//
// O canónico assinado AMARRA a ratificação a ESTE artefacto+eval: o RequestID da
// [SignedApproval] tem de ser exactamente [SelfModArtifact.RatificationID], que sela o
// Kind, a versão SemVer, o ContentHash e a identidade do [otelgenai.EvaluationResult]
// (suite/eval-id/dataset/veredicto/score/trace-alvo). Uma ratificação de um artefacto
// NÃO vale para outro — o RequestID diverge (bloqueio) e, mesmo forçado, a assinatura
// (que cobre o RequestID) não valida.

// ratificationDomain é o separador de domínio versionado prefixado ao canónico da
// IDENTIDADE do artefacto+eval, para que o token de ratificação nunca colida com o de
// outro subsistema nem entre versões do formato.
const ratificationDomain = "aos.ratification.v1"

// DefaultRatifyAuthority é a capability de ratificação que o humano responsável tem de
// deter (na sua autoridade autoritativa registada no [ApproverRegistry]) para poder
// ratificar uma promoção a PRODUÇÃO. É a expressão concreta do "humano responsável
// autorizado" (AC2): não basta a assinatura ser autêntica, a autoridade tem de cobrir
// a ratificação de produção. Ajustável via [WithRatifyAuthority].
const DefaultRatifyAuthority = "ratify:production"

// OpRatify é o nome do span que cobre uma decisão do gate de ratificação (AC5). Como
// o resto do módulo, o gate expõe apenas as portas mínimas [Tracer]/[Span]; o wiring
// (EPIC-08) liga um exportador OTel real.
const OpRatify = "ratification_gate"

// Atributos de span canónicos da decisão de ratificação. NENHUM transporta segredo: o
// veredicto, a versão SemVer, a decisão, o motivo e o hash de conteúdo NÃO são
// segredos; o conteúdo do artefacto e QUALQUER chave nunca entram num span. O
// eval-id/trace-alvo LIGAM a decisão ao gen_ai.evaluation.result (AC5).
const (
	AttrRatDecision        = "aos.ratification.decision"
	AttrRatReason          = "aos.ratification.reason"
	AttrRatArtifactID      = "aos.ratification.artifact_id"
	AttrRatArtifactKind    = "aos.ratification.artifact_kind"
	AttrRatVersion         = "aos.ratification.version"
	AttrRatApprover        = "aos.ratification.approver"
	AttrRatEvalVerdict     = "gen_ai.evaluation.result"
	AttrRatEvalScore       = "gen_ai.evaluation.score"
	AttrRatEvalID          = "gen_ai.evaluation.eval_id"
	AttrRatEvalTargetTrace = "gen_ai.evaluation.target_trace_id"
	AttrRatCanaryPassed    = "aos.ratification.canary_passed"
)

// Motivos de decisão do gate de ratificação, estáveis e legíveis-por-máquina. Selados
// no audit (numa [audit.Obligation]) para que cada decisão seja atribuível.
const (
	// ReasonRatified — ratificação assinada, verificada, por humano responsável
	// autorizado, sobre um artefacto que passou eval-gate+canary (o ÚNICO caminho que
	// devolve admit=true).
	ReasonRatified = "ratified"
	// ReasonRatificationRefused — o humano responsável RECUSOU explicitamente (decisão
	// assinada com Approved=false). É também não-repúdio e é selada; admit=false.
	ReasonRatificationRefused = "ratification_refused"
	// ReasonPreconditionFailed — o artefacto NÃO passou a pré-condição (eval-gate não
	// admite OU canary falhou): não é sequer apresentável a ratificação. Fail-closed
	// ANTES da ratificação (AC4).
	ReasonPreconditionFailed = "precondition_failed"
	// ReasonRatifierUnknown — o ratificador não consta do registo (sem chave pública
	// pinada): não é autenticável. Fail-closed.
	ReasonRatifierUnknown = "ratifier_unknown"
	// ReasonRatifierUnauthorized — o ratificador é autêntico mas a sua autoridade NÃO
	// cobre a ratificação de produção. Fail-closed (AC2).
	ReasonRatifierUnauthorized = "ratifier_unauthorized"
	// ReasonRatificationForged — a assinatura não valida contra a chave pinada do
	// ratificador clamado (forjada/adulterada). Fail-closed.
	ReasonRatificationForged = "ratification_forged"
	// ReasonRatificationTransplant — a ratificação assinada é de OUTRO artefacto+eval (o
	// RequestID não corresponde à identidade deste artefacto). Anti-transplante: uma
	// ratificação não é transferível. Fail-closed.
	ReasonRatificationTransplant = "ratification_transplant"
	// ReasonRatificationMalformed — a decisão assinada está malformada (nonce curto,
	// timestamp zero ou assinatura de dimensão errada). Fail-closed.
	ReasonRatificationMalformed = "ratification_malformed"
	// ReasonRatificationStale — a ratificação é autêntica mas está FORA da janela de
	// frescura configurada ([WithRatifyFreshness]): o signed.IssuedAt é demasiado
	// antigo (ou demasiado no futuro face à tolerância de relógio). Defesa-em-
	// profundidade anti-replay: limita a janela em que uma ratificação assinada pode
	// promover. Fail-closed. Só se aplica quando a frescura está configurada.
	ReasonRatificationStale = "ratification_stale"
	// ReasonRatificationReplayed — a ratificação é autêntica e fresca mas o seu nonce
	// JÁ foi consumido no [RatificationNonceStore] configurado ([WithRatifyNonceStore]):
	// é uma REUTILIZAÇÃO de uma ratificação já usada para promover. Defesa-em-
	// profundidade de uso-único (anti-replay/anti-re-promoção pós-rollback). Fail-closed.
	// Só se aplica quando um nonce-store está configurado.
	ReasonRatificationReplayed = "ratification_replayed"
)

// ArtifactKind é a classe do artefacto auto-escrito sujeito a ratificação.
type ArtifactKind string

const (
	// ArtifactSkill — uma skill auto-escrita.
	ArtifactSkill ArtifactKind = "skill"
	// ArtifactProceduralMemory — uma memória procedural auto-escrita.
	ArtifactProceduralMemory ArtifactKind = "procedural_memory"
)

// SelfModArtifact é o artefacto de auto-modificação candidato a promoção a produção: a
// unidade que o [RatificationGate] avalia. Carrega SÓ identificadores e a prova das
// etapas anteriores (o [otelgenai.EvaluationResult] e o veredicto do canary) — NUNCA o
// conteúdo do artefacto (só o seu ContentHash) nem qualquer segredo.
type SelfModArtifact struct {
	// ID identifica o artefacto (correlação/audit; ex.: nome da skill ou da memória).
	ID string
	// Kind é a classe (skill|procedural_memory).
	Kind ArtifactKind
	// Version é a versão SemVer do artefacto (REG, EPIC-05). Selada no audit e no
	// canónico de ratificação — o humano ratifica ESTA versão.
	Version string
	// EvalResult é o resultado do eval-gate (AOS-084) desta versão, ligado por trace_id
	// à trajectória avaliada. É a base da pré-condição E o alvo da ligação de audit
	// (gen_ai.evaluation.result). O canónico de ratificação amarra a sua identidade.
	EvalResult otelgenai.EvaluationResult
	// CanaryPassed reporta se o artefacto passou a fase de canary (EPIC-08). Parte da
	// pré-condição: false ⇒ não apresentável a ratificação (fail-closed).
	CanaryPassed bool
	// ContentHash é o hash do CONTEÚDO do artefacto (não o conteúdo). Amarra a
	// ratificação ao bytes exacto promovido; nunca entra o conteúdo/segredo.
	ContentHash []byte
}

// canonical devolve a serialização canónica, determinística e estável cross-SO da
// IDENTIDADE do artefacto+eval — a base do token de ratificação. Amarra o Kind, a
// versão SemVer, o ContentHash e a identidade do [otelgenai.EvaluationResult]. Usa o
// mesmo molde length-prefixed de [canonicalApproval]/[audit] (domínio à cabeça, ordem
// fixa, blobs com uvarint, float64 por bits para determinismo).
func (a SelfModArtifact) canonical() []byte {
	buf := make([]byte, 0, 160)
	buf = putString(buf, ratificationDomain)
	buf = putString(buf, a.ID)
	buf = putString(buf, string(a.Kind))
	buf = putString(buf, a.Version)
	buf = putBytes(buf, a.ContentHash)
	r := a.EvalResult
	buf = putString(buf, r.Suite)
	buf = putString(buf, r.EvalID)
	buf = putString(buf, string(r.Dataset))
	buf = putString(buf, string(r.Verdict))
	buf = putUint64(buf, math.Float64bits(r.Score))
	buf = putBytes(buf, r.TargetTraceID[:])
	// O CANÁRIO ENTRA NO CANÓNICO, e é a correcção do achado da verificação de completude de
	// 2026-08-23.
	//
	// O `CanaryPassed` estava FORA daquilo que a assinatura humana cobre, e mesmo assim o nó
	// fazia DUAS coisas com ele: recusava a promoção quando era falso (pré-condição, ver o gate)
	// e SELAVA-O na cadeia como facto (`"canary_passed"` no registo de ratificação). Flipá-lo não
	// mudava o [SelfModArtifact.RatificationID] e não invalidava assinatura nenhuma.
	//
	// O QUE ISTO NÃO ERA, e a distinção importa: não é um bypass de autorização. O nó DECLARA no
	// arranque que «o PIPELINE de promoção (staging→eval-gate→canary→produção) continua a montante
	// e FORA DO NÓ» — nunca prometeu verificar o canário. O que ele promete é que nada chega a
	// produção sem ratificação humana assinada, fresca e de uso-único.
	//
	// O QUE ERA: a cadeia registava `canary_passed=true` como parte de uma promoção RATIFICADA, e
	// o humano não tinha atestado esse facto. É o mesmo eixo dos outros achados desta varredura —
	// o registo tamper-evident a afirmar o que ninguém assinou.
	//
	// CONSEQUÊNCIA DECLARADA: o `RatificationID` MUDA. Uma ratificação assinada antes desta
	// alteração deixa de casar e é recusada. A janela é curta por desenho — as ratificações são
	// frescas e de uso-único, com TTL — mas existe, e uma que esteja em voo no momento do deploy
	// tem de ser re-assinada.
	buf = putBool(buf, a.CanaryPassed)
	return buf
}

// putBool serializa um booleano no canónico com um byte fixo. Não se reutiliza `putString` com
// "true"/"false": o canónico é comparado byte a byte e uma representação textual convidaria a
// variações ("1", "True") que produziriam ids diferentes para o mesmo facto.
func putBool(buf []byte, v bool) []byte {
	if v {
		return append(buf, 1)
	}
	return append(buf, 0)
}

// RatificationID é o token estável que a [SignedApproval] de ratificação tem de
// referenciar no seu RequestID (SHA-256 hex do [SelfModArtifact.canonical]). É o
// coração do anti-transplante (AC-anti-transplante): amarra a assinatura a ESTE
// artefacto+eval. Uma ratificação de outro artefacto tem outro RatificationID —
// diverge (bloqueio) e, mesmo forçada, a assinatura (que cobre o RequestID via
// [canonicalApproval]) não valida. O humano assina exactamente este token.
func (a SelfModArtifact) RatificationID() string {
	sum := sha256.Sum256(a.canonical())
	return hex.EncodeToString(sum[:])
}

// RatificationNonceStore é a porta OPCIONAL de uso-único das ratificações: um livro-
// razão que consome o nonce de cada ratificação para que uma ratificação assinada
// válida NÃO seja reutilizável indefinidamente (anti-replay/anti-re-promoção). É
// injectável ([WithRatifyNonceStore]) porque a durabilidade e o âmbito (por-processo,
// partilhado, com TTL) são decisões de wiring (EPIC-08) — o gate só define o contrato.
//
// Fail-closed: o gate consome o nonce APENAS no caminho de promoção (aprovação
// assinada, fresca, verificada). ConsumeNonce tem de ser ATÓMICO (check-and-set): um
// nonce nunca-visto é gravado e reportado fresh=true; um já-visto devolve fresh=false
// (replay). Qualquer erro do backend é tratado como replay/bloqueio (fail-closed).
// NOTA: o gate consome o nonce ANTES de selar a decisão no audit; se a selagem falhar
// a promoção é negada (audit-before-effect) mas o nonce fica consumido — o desfecho é
// sempre pelo lado seguro (bloqueia; o humano re-ratifica), nunca uma promoção.
type RatificationNonceStore interface {
	// ConsumeNonce regista atomicamente (scope, nonce) como usado e reporta se era
	// FRESCO (nunca visto). Devolve (false, nil) para um replay e (false, err) em erro
	// de backend (ambos tratados como bloqueio pelo gate). scope é o
	// [SelfModArtifact.RatificationID] (uso-único por identidade de artefacto+eval).
	ConsumeNonce(ctx context.Context, scope string, nonce []byte) (bool, error)
}

// RatificationGate é o gate de ratificação de auto-modificação (AOS-096). COMPÕE, por
// porta: a [otelgenai.EvalGate] (pré-condição de eval, AOS-084), o [ApproverRegistry]
// (chaves pinadas + autoridade, AOS-095), o [verifyApproval]/[SignedApproval] (a
// assinatura, AOS-095) e o [audit.Store] (selagem WORM, AOS-072). Seguro para
// concorrência na medida em que os colaboradores o forem. Construir com
// [NewRatificationGate].
type RatificationGate struct {
	eval      otelgenai.EvalGate
	registry  ApproverRegistry
	sealer    audit.Store
	tracer    Tracer
	now       func() time.Time
	authority string
	partition func(a SelfModArtifact) string
	// freshness > 0 activa a janela de frescura anti-replay; 0 (default) desactiva-a
	// (idempotência histórica preservada — ver o godoc de [RatificationGate.Ratify]).
	freshness time.Duration
	// skew é a tolerância para ratificações com IssuedAt ligeiramente no futuro
	// (relógios adiantados), aplicada só quando freshness > 0. Default 0.
	skew time.Duration
	// nonces, quando não-nil, impõe uso-único por nonce; nil (default) desactiva-o.
	nonces RatificationNonceStore
}

// RatificationOption configura o [RatificationGate].
type RatificationOption func(*RatificationGate)

// WithRatifyTracer injecta a porta de observabilidade (default [NoopTracer]).
func WithRatifyTracer(t Tracer) RatificationOption {
	return func(g *RatificationGate) {
		if t != nil {
			g.tracer = t
		}
	}
}

// WithRatifyClock injecta o relógio (default [time.Now]) do timestamp observacional do
// selo de audit. Uso interno/testes deterministas.
func WithRatifyClock(f func() time.Time) RatificationOption {
	return func(g *RatificationGate) {
		if f != nil {
			g.now = f
		}
	}
}

// WithRatifyAuthority substitui a capability exigida ao ratificador (default
// [DefaultRatifyAuthority]). Um valor vazio mantém o default.
func WithRatifyAuthority(cap string) RatificationOption {
	return func(g *RatificationGate) {
		if cap != "" {
			g.authority = cap
		}
	}
}

// WithRatifyPartitioner define como derivar a partição de audit a partir do artefacto.
// Default: "ratification:<artifact-id>" (uma cadeia de ratificações por artefacto).
func WithRatifyPartitioner(f func(a SelfModArtifact) string) RatificationOption {
	return func(g *RatificationGate) {
		if f != nil {
			g.partition = f
		}
	}
}

// WithRatifyFreshness activa a janela de frescura anti-replay (defesa-em-profundidade):
// uma ratificação cujo IssuedAt esteja fora de [now-ttl, now+skew] é rejeitada como
// [ReasonRatificationStale] (fail-closed). ttl <= 0 mantém a frescura DESACTIVADA
// (default — a ratificação é aceite sem limite temporal, ver o godoc de
// [RatificationGate.Ratify]); skew < 0 é ignorado. Limita a janela em que uma
// ratificação assinada válida pode promover, sem, por si só, garantir uso-único
// (para isso, combinar com [WithRatifyNonceStore]).
func WithRatifyFreshness(ttl, skew time.Duration) RatificationOption {
	return func(g *RatificationGate) {
		if ttl > 0 {
			g.freshness = ttl
		}
		if skew >= 0 {
			g.skew = skew
		}
	}
}

// WithRatifyNonceStore injecta o livro-razão de uso-único ([RatificationNonceStore]):
// o nonce de cada ratificação promovida é consumido, pelo que uma ratificação assinada
// válida NÃO pode re-promover o mesmo artefacto (anti-replay/anti-re-promoção pós-
// rollback). nil (default) mantém o uso-único DESACTIVADO. Ver o godoc de
// [RatificationGate.Ratify].
func WithRatifyNonceStore(s RatificationNonceStore) RatificationOption {
	return func(g *RatificationGate) {
		if s != nil {
			g.nonces = s
		}
	}
}

// NewRatificationGate constrói o gate. eval (pré-condição de eval), registry (chaves
// pinadas + autoridade) e sealer (audit WORM) são OBRIGATÓRIOS — a sua ausência é
// fail-closed ([ErrNilDeps]). Por omissão usa [NoopTracer], [time.Now],
// [DefaultRatifyAuthority] e a partição "ratification:<artifact-id>".
func NewRatificationGate(eval otelgenai.EvalGate, registry ApproverRegistry, sealer audit.Store, opts ...RatificationOption) (*RatificationGate, error) {
	if eval == nil || registry == nil || sealer == nil {
		return nil, ErrNilDeps
	}
	g := &RatificationGate{
		eval:      eval,
		registry:  registry,
		sealer:    sealer,
		tracer:    NoopTracer{},
		now:       time.Now,
		authority: DefaultRatifyAuthority,
		partition: func(a SelfModArtifact) string { return "ratification:" + a.ID },
	}
	for _, o := range opts {
		o(g)
	}
	if g.now == nil {
		g.now = time.Now
	}
	if g.partition == nil {
		g.partition = func(a SelfModArtifact) string { return "ratification:" + a.ID }
	}
	return g, nil
}

// Ratify decide se artifact pode ser PROMOVIDO a produção, dada a ratificação assinada
// signed. Devolve admit=true SÓ se, cumulativamente: (1) a PRÉ-CONDIÇÃO eval-gate+canary
// (AC4); (2) a RATIFICAÇÃO assinada é VERIFICÁVEL contra a chave pinada de um humano
// responsável AUTORIZADO, é para ESTE artefacto+eval (anti-transplante) e é uma
// APROVAÇÃO (não recusa); (3) a decisão é SELADA no audit. Falha de qualquer uma →
// admit=false, fail-closed — o artefacto fica em canary/staging, nunca em prod.
//
// A decisão terminal é SEMPRE selada no audit tamper-evident (quem/SemVer/eval/
// timestamp), ligada ao gen_ai.evaluation.result (AC5): um bloqueio de pré-condição, uma
// recusa assinada e uma ratificação são todos auditáveis. Uma decisão não-selável NUNCA
// vira promoção (audit-before-effect): se a selagem falhar, força-se admit=false.
//
// # Frescura e uso-único (idempotência vs. replay)
//
// Por omissão, Ratify é uma função PURA de (artifact, signed): o [RatificationID]
// (RequestID da assinatura) é uma identidade ESTÁVEL de conteúdo (artefacto+eval), pelo
// que a MESMA ratificação assinada valida SEMPRE e é reutilizável N vezes. Isto amarra a
// ratificação a ESTE artefacto+eval (anti-transplante), mas NÃO a um evento de promoção
// único: um chamador que re-invoque Ratify com a mesma assinatura obtém admit=true de
// novo — incluindo depois de um rollback que marcou a versão como má. O gate garante que
// NENHUM conteúdo nunca-ratificado é promovido; NÃO garante, por si só, uso-único.
//
// Cabe portanto ao CHAMADOR (o promotion controller a montante) garantir que cada
// admit=true dá origem a NO MÁXIMO uma promoção — tratando o [RatificationID]/nonce como
// uso-único e revalidando contra o estado de rollback/revogação. Em alternativa (defesa-
// em-profundidade no próprio gate): [WithRatifyFreshness] limita a janela temporal em que
// a ratificação promove, e [WithRatifyNonceStore] impõe uso-único do nonce (o gate
// consome-o no caminho de promoção; uma reutilização vira [ReasonRatificationReplayed]).
// Ambos são opcionais e, quando não configurados, o comportamento é o idempotente acima.
func (g *RatificationGate) Ratify(ctx context.Context, artifact SelfModArtifact, signed SignedApproval) (bool, error) {
	ctx, span := g.tracer.StartSpan(ctx, OpRatify)
	defer span.End()

	ratID := artifact.RatificationID()
	span.SetAttribute(AttrRatArtifactID, artifact.ID)
	span.SetAttribute(AttrRatArtifactKind, string(artifact.Kind))
	span.SetAttribute(AttrRatVersion, artifact.Version)
	span.SetAttribute(AttrRatEvalVerdict, string(artifact.EvalResult.Verdict))
	span.SetAttribute(AttrRatEvalScore, artifact.EvalResult.Score)
	span.SetAttribute(AttrRatEvalID, artifact.EvalResult.EvalID)
	if h := artifact.EvalResult.TargetTraceIDHex(); h != "" {
		span.SetAttribute(AttrRatEvalTargetTrace, h)
	}
	span.SetAttribute(AttrRatCanaryPassed, artifact.CanaryPassed)

	// (1) PRÉ-CONDIÇÃO (AC4): só o que o eval-gate ADMITE e passou o canary é
	// APRESENTÁVEL a ratificação. Falha aqui ⇒ nem se olha para a ratificação: bloqueia
	// fail-closed ANTES dela e sela o bloqueio (auditável).
	if g.eval == nil || !g.eval.Admit(artifact.EvalResult) || !artifact.CanaryPassed {
		return g.finish(ctx, span, artifact, ratID, SignedApproval{}, false, ReasonPreconditionFailed, false)
	}

	// (2) RATIFICAÇÃO assinada. Resolve o ratificador clamado: chave pública pinada +
	// autoridade autoritativa. Desconhecido/erro ⇒ não autenticável, fail-closed.
	pub, authority, ok, lerr := g.registry.Lookup(ctx, signed.Approver)
	if lerr != nil || !ok || len(pub) != ed25519.PublicKeySize {
		return g.finish(ctx, span, artifact, ratID, signed, false, ReasonRatifierUnknown, false)
	}

	// AUTORIDADE (AC2): a autoridade autoritativa do ratificador tem de cobrir a
	// ratificação de produção. Autêntico mas SEM autoridade ⇒ fail-closed.
	if !contains(authority, g.authority) {
		return g.finish(ctx, span, artifact, ratID, signed, false, ReasonRatifierUnauthorized, false)
	}

	// ANTI-TRANSPLANTE: a decisão tem de referenciar a identidade DESTE artefacto+eval.
	// Uma ratificação de outro artefacto tem outro RequestID ⇒ bloqueio.
	if signed.RequestID != ratID {
		return g.finish(ctx, span, artifact, ratID, signed, false, ReasonRatificationTransplant, false)
	}

	// Forma da decisão assinada (nonce/timestamp/assinatura bem-formados).
	if len(signed.Nonce) < approvalNonceMinLen || signed.IssuedAt.IsZero() ||
		len(signed.Signature) != ed25519.SignatureSize {
		return g.finish(ctx, span, artifact, ratID, signed, false, ReasonRatificationMalformed, false)
	}

	// ASSINATURA (não-repúdio): tem de validar contra a chave PINADA do ratificador
	// (reutiliza [verifyApproval] de AOS-095). Forjada/adulterada ⇒ fail-closed.
	if !verifyApproval(pub, signed) {
		return g.finish(ctx, span, artifact, ratID, signed, false, ReasonRatificationForged, false)
	}

	// A assinatura JÁ é válida e atribuível (verified=true): sela-se na cadeia com a
	// assinatura, mesmo numa RECUSA (não-repúdio da recusa), mas a decisão é DENY.
	if !signed.Approved {
		return g.finish(ctx, span, artifact, ratID, signed, false, ReasonRatificationRefused, true)
	}

	// DEFESA-EM-PROFUNDIDADE anti-replay (opcional; ver o godoc de Ratify). A assinatura
	// amarra o artefacto+eval mas, por si só, é válida para sempre e reutilizável. Ambas
	// as verificações só se aplicam no caminho de PROMOÇÃO (aprovação verificada) e, se
	// configuradas, negam fail-closed uma ratificação stale/reutilizada (atribuível ao
	// ratificador, verified=true, selada na cadeia do artefacto).
	if g.freshness > 0 {
		age := g.now().Sub(signed.IssuedAt)
		if age > g.freshness || age < -g.skew {
			return g.finish(ctx, span, artifact, ratID, signed, false, ReasonRatificationStale, true)
		}
	}
	if g.nonces != nil {
		fresh, nerr := g.nonces.ConsumeNonce(ctx, ratID, signed.Nonce)
		if nerr != nil || !fresh {
			return g.finish(ctx, span, artifact, ratID, signed, false, ReasonRatificationReplayed, true)
		}
	}

	// Ratificação assinada, verificada, autorizada, para ESTE artefacto+eval, fresca/
	// uso-único (quando configurado), e é uma APROVAÇÃO: a promoção pode prosseguir
	// (sujeita à selagem no audit).
	return g.finish(ctx, span, artifact, ratID, signed, true, ReasonRatified, true)
}

// finish é o ponto ÚNICO de saída: sela a decisão no audit tamper-evident, emite os
// atributos de decisão no span e devolve o veredicto. verified indica que a decisão foi
// assinada+verificada por um ratificador autorizado (o único caso em que o selo é
// atribuível ao humano REAL e carrega a assinatura de não-repúdio). Uma decisão
// não-selável força admit=false (audit-before-effect).
func (g *RatificationGate) finish(ctx context.Context, span Span, artifact SelfModArtifact, ratID string, signed SignedApproval, admit bool, reason string, verified bool) (bool, error) {
	sealed := g.seal(ctx, artifact, ratID, signed, admit, reason, verified)
	if !sealed {
		admit = false
	}
	span.SetAttribute(AttrRatDecision, decisionString(admit))
	span.SetAttribute(AttrRatReason, reason)
	if verified {
		span.SetAttribute(AttrRatApprover, signed.Approver)
	}
	return admit, nil
}

// seal grava a decisão de ratificação na cadeia de audit WORM tamper-evident. Devolve
// false se a selagem falhar (o chamador força admit=false — audit-before-effect).
//
// ATRIBUIÇÃO (molde do [Channel]): uma decisão VERIFICADA (assinada por ratificador
// autorizado) é atribuível ao humano REAL — Principal=ratificador — e carrega a
// ASSINATURA (não-repúdio, re-verificável a partir do audit). A ligação ao eval
// (gen_ai.evaluation.result via eval-id/trace-alvo) e a versão SemVer são seladas em
// TODAS as decisões (AC5). NUNCA sela o conteúdo do artefacto nem qualquer segredo — só
// metadados de responsabilização, hashes e a assinatura (que é pública).
func (g *RatificationGate) seal(ctx context.Context, artifact SelfModArtifact, ratID string, signed SignedApproval, admit bool, reason string, verified bool) bool {
	decision := audit.DecisionDeny
	if admit {
		decision = audit.DecisionAllow
	}

	obs := []audit.Obligation{{
		Type: "ratification_decision",
		Params: map[string]string{
			"reason":                reason,
			"ratification_id":       ratID,
			"artifact_id":           artifact.ID,
			"artifact_kind":         string(artifact.Kind),
			"version":               artifact.Version,
			"content_hash":          hex.EncodeToString(artifact.ContentHash),
			"canary_passed":         boolStr(artifact.CanaryPassed),
			"eval_verdict":          string(artifact.EvalResult.Verdict),
			"eval_score":            strconv.FormatFloat(artifact.EvalResult.Score, 'g', -1, 64),
			"eval_suite":            artifact.EvalResult.Suite,
			"eval_id":               artifact.EvalResult.EvalID,
			"eval_dataset":          string(artifact.EvalResult.Dataset),
			"eval_target_trace_id":  artifact.EvalResult.TargetTraceIDHex(),
			"eval_result_attribute": string(otelgenai.OpEvaluation),
		},
	}}

	rec := audit.AuditRecord{
		Timestamp:     g.now(),
		Decision:      decision,
		Capability:    g.authority,
		PolicyVersion: artifact.Version, // versão SemVer do artefacto ratificado (AC5)
		ToolID:        "governance.ratification",
		RequestID:     ratID,
		Resource:      audit.Resource{Type: "self_mod_artifact", Value: artifact.ID},
	}

	if verified {
		// Atribuível ao ratificador REAL; carrega a assinatura de não-repúdio, o nonce e
		// o timestamp para re-verificação a partir da cadeia.
		rec.Partition = g.partition(artifact)
		rec.Principal = audit.Principal{NHIID: signed.Approver}
		obs = append(obs, audit.Obligation{
			Type: "ratification_signature",
			Params: map[string]string{
				"ratifier": signed.Approver,
				// A decisão TAL COMO ASSINADA (base do não-repúdio e da re-verificação a
				// partir da cadeia). Pode divergir do Decision do registo quando a selagem
				// falha; aqui coincide (aprovação assinada ⇒ allow, recusa assinada ⇒ deny).
				"approved":   boolStr(signed.Approved),
				"nonce":      base64.StdEncoding.EncodeToString(signed.Nonce),
				"issued_at":  signed.IssuedAt.UTC().Format(time.RFC3339Nano),
				"signature":  base64.StdEncoding.EncodeToString(signed.Signature),
				"sig_domain": approvalDomain,
			},
		})
	} else {
		// Quarentena: sem ratificador autenticado (pré-condição falhada, ratificador
		// desconhecido/sem autoridade, transplante, malformada, assinatura forjada). O
		// ratificador clamado é apenas um claim, não um principal.
		rec.Partition = partitionUnratified
		obs = append(obs, audit.Obligation{
			Type: "ratification_unauthenticated",
			Params: map[string]string{
				"claimed_ratifier": signed.Approver,
				"authenticated":    "false",
			},
		})
	}
	rec.Obligations = obs

	_, err := g.sealer.Append(ctx, rec)
	return err == nil
}

// partitionUnratified é a partição de audit de QUARENTENA das decisões cuja origem NÃO
// está autenticada (pré-condição falhada, ratificador desconhecido/sem autoridade,
// transplante, assinatura forjada). Concentrá-las numa cadeia própria — com o
// ratificador clamado como claim, não como principal — impede que um atacante polua a
// cadeia de ratificações de um artefacto com bloqueios "em nome" de um humano (molde do
// [Channel]).
const partitionUnratified = "ratification-unratified"
