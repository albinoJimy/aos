package messaging

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aos-ref/platform/audit"
)

// Motivos de rejeição, estáveis e legíveis-por-máquina. Selados na cadeia de audit
// (numa [audit.Obligation]) para que cada rejeição seja atribuível ao emissor
// clamado, à referência E à natureza da falha.
const (
	ReasonInvalidMessage       = "invalid_message"
	ReasonUnknownOrigin        = "unknown_origin"
	ReasonForgedOrigin         = "forged_origin"
	ReasonStaleMessage         = "stale_message"
	ReasonReplayedNonce        = "replayed_nonce"
	ReasonAuthorityNotCovered  = "authority_not_covered"
	ReasonReferenceNotFound    = "reference_not_found"
	ReasonReferenceInauthentic = "reference_inauthentic"
)

// toolIDVerify identifica a fronteira de verificação nas rejeições seladas.
const toolIDVerify = "messaging.verify"

// partitionUnauth é a partição de audit de QUARENTENA onde se selam as rejeições
// cuja origem ainda NÃO está autenticada (forma inválida, origem desconhecida,
// origem forjada). A origem nessas rejeições é apenas CLAMADA e spoofável: selá-la
// na cadeia atribuível da NHI clamada permitiria a um atacante poluir a cadeia da
// vítima com floods de forjas "em nome dela". Concentrar as rejeições
// não-autenticadas numa única cadeia de quarentena — com a origem clamada
// registada como CLAIM, não como principal responsável — impede essa poluição
// dirigida. Só depois de a assinatura validar é que a rejeição passa a ser
// atribuída ao emissor REAL (partição [Verifier.partition]).
const partitionUnauth = "msg-verify-unauth"

// Defaults da janela de frescura anti-replay.
const (
	defaultFreshnessWindow = 5 * time.Minute
	defaultClockSkew       = 1 * time.Minute
)

// VerifiedMessage é o resultado de uma verificação bem-sucedida: a mensagem cuja
// ORIGEM, AUTORIDADE e REFERÊNCIA foram criptograficamente comprovadas. A
// Authority devolvida é a AUTORITATIVA (resolvida do directório de identidade),
// não a auto-declarada na mensagem — é sobre esta que o receptor pode agir.
type VerifiedMessage struct {
	Origin    string
	Action    string
	Authority []string
	Reference Reference
	Payload   []byte
}

// Verifier verifica mensagens inter-agente ANTES de o receptor agir. Compõe a
// identidade ([NHIRegistry]), o resolvedor de referências ([ReferenceResolver]) e
// o audit tamper-evident ([audit.Store]) — todos OBRIGATÓRIOS (fail-closed).
// Construir com [NewVerifier]. Seguro para uso concorrente na medida em que os
// colaboradores o forem (o [audit.MemStore] é concorrente-seguro).
type Verifier struct {
	registry      NHIRegistry
	refs          ReferenceResolver
	sealer        audit.Store
	tracer        Tracer
	now           func() time.Time
	partition     func(origin string) string
	policyVersion string

	// Anti-replay (finding replay-no-freshness). freshnessWindow é a idade máxima
	// aceite; maxClockSkew é a tolerância a timestamps futuros. O seen-set deduplica
	// (Origin, Nonce) e é protegido por mu (verificação concorrente-segura).
	freshnessWindow time.Duration
	maxClockSkew    time.Duration
	mu              sync.Mutex
	seen            map[string]time.Time // key = Origin\x00Nonce → IssuedAt
}

// VerifierOption configura o Verifier.
type VerifierOption func(*Verifier)

// WithVerifierClock injecta o relógio usado no timestamp OBSERVACIONAL das
// rejeições seladas (a decisão de verificação NÃO depende do relógio). Uso
// interno/testes determinísticos.
func WithVerifierClock(f func() time.Time) VerifierOption {
	return func(v *Verifier) {
		if f != nil {
			v.now = f
		}
	}
}

// WithPartitioner define como derivar a partição de audit a partir da origem
// clamada. Default: "msg-verify:<origin>" (uma cadeia por emissor clamado).
func WithPartitioner(f func(origin string) string) VerifierOption {
	return func(v *Verifier) {
		if f != nil {
			v.partition = f
		}
	}
}

// WithPolicyVersion define a versão de política registada nas rejeições seladas.
func WithPolicyVersion(s string) VerifierOption {
	return func(v *Verifier) {
		if s != "" {
			v.policyVersion = s
		}
	}
}

// WithTracer injecta a porta de observabilidade (default [NoopTracer]). O span
// [OpMessageVerify] cobre a decisão de verificação com atributos NÃO-secretos
// (origem clamada, acção, referência, decisão, motivo) — nunca o payload/chaves.
func WithTracer(t Tracer) VerifierOption {
	return func(v *Verifier) {
		if t != nil {
			v.tracer = t
		}
	}
}

// WithFreshnessWindow ajusta a janela anti-replay: maxAge é a idade máxima aceite
// (mensagens mais antigas ⇒ [ErrStaleMessage]); maxSkew é a tolerância a
// timestamps futuros (relógios dessincronizados). Valores <= 0 são ignorados
// (mantêm-se os defaults). A frescura NÃO pode ser desativada — é o que limita o
// crescimento do seen-set (nonces fora da janela são podados).
func WithFreshnessWindow(maxAge, maxSkew time.Duration) VerifierOption {
	return func(v *Verifier) {
		if maxAge > 0 {
			v.freshnessWindow = maxAge
		}
		if maxSkew > 0 {
			v.maxClockSkew = maxSkew
		}
	}
}

// NewVerifier constrói um Verifier. registry (chave pública pinada + autoridade
// autoritativa), refs (existência/autenticidade da referência) e sealer (audit
// WORM) são OBRIGATÓRIOS — a sua ausência é fail-closed ([ErrNilDeps]).
func NewVerifier(registry NHIRegistry, refs ReferenceResolver, sealer audit.Store, opts ...VerifierOption) (*Verifier, error) {
	if registry == nil || refs == nil || sealer == nil {
		return nil, ErrNilDeps
	}
	v := &Verifier{
		registry:        registry,
		refs:            refs,
		sealer:          sealer,
		tracer:          NoopTracer{},
		now:             time.Now,
		partition:       func(origin string) string { return "msg-verify:" + origin },
		policyVersion:   "messaging/AOS-073",
		freshnessWindow: defaultFreshnessWindow,
		maxClockSkew:    defaultClockSkew,
		seen:            make(map[string]time.Time),
	}
	for _, o := range opts {
		o(v)
	}
	return v, nil
}

// Verify verifica uma mensagem inter-agente ANTES de o receptor agir. Impõe, por
// esta ordem e fail-closed em cada passo, as invariantes de AOS-073:
//
//	(a) ORIGEM — a assinatura valida contra a chave pública PINADA da NHI que a
//	    mensagem CLAMA ser o emissor. Uma NHI desconhecida ([ErrUnknownOrigin]) ou
//	    um emissor forjado ([ErrForgedOrigin]) é rejeitado.
//	(f) FRESCURA/REPLAY — só DEPOIS de a origem estar autenticada se confia no
//	    material anti-replay (nonce/timestamp) que a assinatura cobre: a mensagem
//	    tem de estar dentro da janela de frescura ([ErrStaleMessage]) e o par
//	    (Origin, Nonce) não pode ter sido já consumido ([ErrReplayedNonce]).
//	(b) AUTORIDADE — a autoridade AUTORITATIVA do emissor cobre a Action pedida, e
//	    a autoridade CLAMADA não excede a autoritativa. Senão [ErrAuthorityNotCovered].
//	(c) REFERÊNCIA — o item referenciado EXISTE ([ErrReferenceNotFound]) e é
//	    AUTÊNTICO — o hash autêntico casa com o coberto pela assinatura
//	    ([ErrReferenceInauthentic]).
//
// Toda a rejeição é SELADA na cadeia de audit tamper-evident e coberta por um span
// [OpMessageVerify] (fail-closed: uma rejeição não-selável junta [ErrSealFailed],
// nunca vira aceitação). As rejeições ANTERIORES à autenticação da origem (forma
// inválida/origem desconhecida/forjada) vão para a partição de QUARENTENA
// ([partitionUnauth]) com a origem registada como CLAIM não-autenticado; só depois
// de a assinatura validar é que a rejeição é atribuída ao emissor REAL. Só o
// sucesso devolve uma [VerifiedMessage] — o receptor age exclusivamente sobre ela.
//
// A ORDEM coloca a verificação criptográfica ANTES de frescura/autoridade/
// referência: nunca se confia em metadados (incluindo o nonce/timestamp) de uma
// mensagem cuja origem não foi comprovada.
func (v *Verifier) Verify(ctx context.Context, msg Message) (VerifiedMessage, error) {
	if v == nil {
		return VerifiedMessage{}, ErrNilDeps
	}

	ctx, span := v.tracer.StartSpan(ctx, OpMessageVerify)
	defer span.End()
	span.SetAttribute(AttrOperationName, OpMessageVerify)
	span.SetAttribute(AttrClaimedOrigin, msg.Origin)
	span.SetAttribute(AttrAction, msg.Action)
	span.SetAttribute(AttrReference, msg.Reference.ID)
	span.SetAttribute(AttrPolicyVersion, v.policyVersion)

	// 0) Forma mínima: origem, acção, referência, assinatura de dimensão certa e
	// material anti-replay presente (nonce >= mínimo, timestamp não-zero). Uma
	// mensagem que pede acção TEM de referenciar um item autêntico (é sobre ele que
	// se age). Origem ainda NÃO autenticada ⇒ rejeição de quarentena.
	if msg.Origin == "" || msg.Action == "" || msg.Reference.ID == "" || len(msg.Reference.Hash) == 0 ||
		len(msg.Signature) != ed25519.SignatureSize || len(msg.Nonce) < nonceMinLen || msg.IssuedAt.IsZero() {
		return VerifiedMessage{}, v.reject(ctx, span, msg, ReasonInvalidMessage, false,
			fmt.Errorf("%w: campos obrigatorios em falta ou assinatura/nonce/timestamp malformados", ErrInvalidMessage))
	}

	// (a) ORIGEM — resolver a chave pública pinada e a autoridade autoritativa da NHI
	// CLAMADA. Uma NHI desconhecida não é autenticável (quarentena).
	pub, authAuthority, ok, err := v.registry.Lookup(ctx, msg.Origin)
	if err != nil {
		return VerifiedMessage{}, v.reject(ctx, span, msg, ReasonUnknownOrigin, false,
			fmt.Errorf("%w: %v", ErrUnknownOrigin, err))
	}
	if !ok || len(pub) != ed25519.PublicKeySize {
		return VerifiedMessage{}, v.reject(ctx, span, msg, ReasonUnknownOrigin, false,
			fmt.Errorf("%w: nhi=%q", ErrUnknownOrigin, msg.Origin))
	}

	// (a) ORIGEM — a assinatura TEM de validar contra a chave da NHI clamada. É aqui
	// que a elevação do hallucination gate morde: mesmo com um ID que existe, uma
	// assinatura forjada (feita por OUTRA chave) ou adulterada não valida (quarentena).
	if !ed25519.Verify(pub, canonicalBytes(msg), msg.Signature) {
		return VerifiedMessage{}, v.reject(ctx, span, msg, ReasonForgedOrigin, false,
			fmt.Errorf("%w: nhi=%q", ErrForgedOrigin, msg.Origin))
	}

	// --- ORIGEM AUTENTICADA: a partir daqui o nonce/timestamp são de confiança e as
	// rejeições são atribuíveis ao emissor REAL. ---
	span.SetAttribute(AttrOriginAuthenticated, true)
	now := v.now()

	// (f) FRESCURA — a mensagem tem de estar dentro da janela: nem demasiado antiga
	// (replay de uma captura antiga) nem com timestamp futuro além do skew tolerado.
	if !v.fresh(msg.IssuedAt, now) {
		return VerifiedMessage{}, v.reject(ctx, span, msg, ReasonStaleMessage, true,
			fmt.Errorf("%w: issuedAt=%s now=%s", ErrStaleMessage, msg.IssuedAt.UTC(), now.UTC()))
	}

	// (f) REPLAY — consumir o par (Origin, Nonce). Se já foi consumido, é um reenvio
	// de uma mensagem já verificada: rejeitar para não re-autorizar a mesma acção.
	if !v.consumeNonce(msg.Origin, msg.Nonce, msg.IssuedAt, now) {
		return VerifiedMessage{}, v.reject(ctx, span, msg, ReasonReplayedNonce, true,
			fmt.Errorf("%w: nhi=%q", ErrReplayedNonce, msg.Origin))
	}

	// (b) AUTORIDADE — a autoridade CLAMADA não pode exceder a AUTORITATIVA (a
	// mensagem não se auto-concede autoridade), e a acção pedida tem de estar coberta
	// pela autoridade autoritativa. Não basta o emissor ser autêntico: a sua
	// autoridade tem de cobrir a acção.
	if !subset(msg.Authority, authAuthority) || !contains(authAuthority, msg.Action) {
		return VerifiedMessage{}, v.reject(ctx, span, msg, ReasonAuthorityNotCovered, true,
			fmt.Errorf("%w: accao=%q", ErrAuthorityNotCovered, msg.Action))
	}

	// (c) REFERÊNCIA — o item referenciado tem de existir e o seu hash autêntico tem
	// de casar com o coberto pela assinatura. Distingue "o ID existe" de "a
	// referência é autêntica".
	authHash, ok, err := v.refs.Resolve(ctx, msg.Reference.ID)
	if err != nil {
		return VerifiedMessage{}, v.reject(ctx, span, msg, ReasonReferenceNotFound, true,
			fmt.Errorf("%w: %v", ErrReferenceNotFound, err))
	}
	if !ok {
		return VerifiedMessage{}, v.reject(ctx, span, msg, ReasonReferenceNotFound, true,
			fmt.Errorf("%w: ref=%q", ErrReferenceNotFound, msg.Reference.ID))
	}
	if !bytes.Equal(authHash, msg.Reference.Hash) {
		return VerifiedMessage{}, v.reject(ctx, span, msg, ReasonReferenceInauthentic, true,
			fmt.Errorf("%w: ref=%q", ErrReferenceInauthentic, msg.Reference.ID))
	}

	// Sucesso: origem autêntica, mensagem fresca e não-replay, autoridade cobre a
	// acção, referência autêntica. O receptor age SÓ sobre esta VerifiedMessage, com
	// a autoridade AUTORITATIVA.
	span.SetAttribute(AttrDecision, decisionAllow)
	return VerifiedMessage{
		Origin:    msg.Origin,
		Action:    msg.Action,
		Authority: append([]string(nil), authAuthority...),
		Reference: Reference{ID: msg.Reference.ID, Hash: append([]byte(nil), msg.Reference.Hash...)},
		Payload:   append([]byte(nil), msg.Payload...),
	}, nil
}

// fresh indica se issuedAt cai dentro da janela de frescura relativa a now: nem
// mais antigo que freshnessWindow, nem no futuro além de maxClockSkew.
func (v *Verifier) fresh(issuedAt, now time.Time) bool {
	age := now.Sub(issuedAt)
	if age > v.freshnessWindow {
		return false // demasiado antiga (replay de captura antiga)
	}
	if age < -v.maxClockSkew {
		return false // futuro além do skew tolerado
	}
	return true
}

// consumeNonce regista o par (origin, nonce) e devolve true se era NOVO (aceite),
// false se já tinha sido consumido (replay). Poda oportunista dos nonces já fora da
// janela de frescura (que falhariam [Verifier.fresh] de qualquer forma), mantendo o
// seen-set limitado. Concurrency-safe via mu.
func (v *Verifier) consumeNonce(origin string, nonce []byte, issuedAt, now time.Time) bool {
	key := origin + "\x00" + string(nonce)
	v.mu.Lock()
	defer v.mu.Unlock()
	cutoff := now.Add(-v.freshnessWindow)
	for k, t := range v.seen {
		if t.Before(cutoff) {
			delete(v.seen, k)
		}
	}
	if _, dup := v.seen[key]; dup {
		return false
	}
	v.seen[key] = issuedAt
	return true
}

// reject regista a decisão de deny no span, sela a rejeição no audit tamper-evident
// e devolve a causa. authenticated indica se, no ponto da rejeição, a ORIGEM já
// estava criptograficamente comprovada — o que decide a partição/atribuição do
// selo (ver [Verifier.seal]). Fail-closed: se a selagem falhar, a causa é JUNTADA
// com [ErrSealFailed] (a rejeição mantém-se — uma rejeição não-auditável nunca vira
// aceitação).
func (v *Verifier) reject(ctx context.Context, span Span, msg Message, reason string, authenticated bool, cause error) error {
	span.SetAttribute(AttrDecision, decisionDeny)
	span.SetAttribute(AttrRejectReason, reason)
	if serr := v.seal(ctx, msg, reason, authenticated); serr != nil {
		return errors.Join(cause, fmt.Errorf("%w: %v", ErrSealFailed, serr))
	}
	return cause
}

// seal grava um evento de rejeição (Decision=deny) na cadeia de audit WORM, à
// acção (Capability), à referência (Resource) e ao motivo (Obligation). NUNCA sela
// o payload da mensagem — só metadados de responsabilização.
//
// A ATRIBUIÇÃO depende de authenticated:
//
//   - authenticated=true — a origem foi comprovada por assinatura: o selo é
//     atribuível ao emissor REAL (Principal.NHIID = origem) na sua partição
//     ([Verifier.partition]).
//   - authenticated=false — a origem é apenas CLAMADA e spoofável (forma inválida/
//     origem desconhecida/forjada): NÃO se atribui à NHI clamada nem se polui a sua
//     cadeia. Sela-se na partição de QUARENTENA ([partitionUnauth]) SEM principal
//     autenticado, com a origem clamada registada numa obligation "claimed_origin"
//     (authenticated=false) — um CLAIM para forense, não uma atribuição.
func (v *Verifier) seal(ctx context.Context, msg Message, reason string, authenticated bool) error {
	rec := audit.AuditRecord{
		Timestamp:     v.now(),
		Decision:      audit.DecisionDeny,
		Capability:    msg.Action,
		PolicyVersion: v.policyVersion,
		ToolID:        toolIDVerify,
		Resource:      audit.Resource{Type: "message-ref", Value: msg.Reference.ID},
	}
	if authenticated {
		rec.Partition = v.partition(msg.Origin)
		rec.Principal = audit.Principal{NHIID: msg.Origin}
		rec.Obligations = []audit.Obligation{{
			Type:   "reject_reason",
			Params: map[string]string{"reason": reason},
		}}
	} else {
		rec.Partition = partitionUnauth
		rec.Principal = audit.Principal{} // sem principal autenticado
		rec.Obligations = []audit.Obligation{
			{Type: "reject_reason", Params: map[string]string{"reason": reason}},
			{Type: "claimed_origin", Params: map[string]string{
				"claimed_origin": msg.Origin,
				"authenticated":  "false",
			}},
		}
	}
	// O CTX DO CHAMADOR NÃO CANCELA ESTE SELO (achado de revisão adversarial sobre
	// AOS-311). Este selo só existe no caminho de REJEIÇÃO ([Verifier.reject]): quando se
	// chega aqui a mensagem JÁ foi recusada e nenhum efeito depende desta escrita — é a
	// PROVA de uma decisão consumada. Desde que o AOS-311 pôs o `audit.Store.Append` a
	// respeitar o ctx, uma rejeição decidida com o contexto do consumidor já morto ficava
	// sem rasto na cadeia de quarentena, que é precisamente onde a forense de mensagens
	// forjadas vive. O erro continua a subir (o `reject` junta-o ao `ErrSealFailed`): o
	// que muda é que já não é o cancelamento a produzi-lo.
	selCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rejectSealTimeout)
	defer cancel()
	_, err := v.sealer.Append(selCtx, rec)
	return err
}

// rejectSealTimeout é o prazo PRÓPRIO do selo de rejeição, que deixou de herdar o
// cancelamento do chamador: sem prazo nenhum, um WORM pendurado prenderia a verificação.
const rejectSealTimeout = 5 * time.Second

// contains indica se s está em set.
func contains(set []string, s string) bool {
	for _, x := range set {
		if x == s {
			return true
		}
	}
	return false
}

// subset indica se todos os elementos de a estão em b (a ⊆ b). Usado para impor
// que a autoridade CLAMADA não excede a AUTORITATIVA.
func subset(a, b []string) bool {
	if len(a) == 0 {
		return true
	}
	in := make(map[string]struct{}, len(b))
	for _, x := range b {
		in[x] = struct{}{}
	}
	for _, x := range a {
		if _, ok := in[x]; !ok {
			return false
		}
	}
	return true
}
