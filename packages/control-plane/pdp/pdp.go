package pdp

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PDP é o Policy Decision Point: avalia a política de referência compilada em
// memória e devolve decisões deterministas (contrato C1). É seguro para uso
// concorrente; o hot-reload troca atomicamente o motor sob um RWMutex sem mutar
// decisões já emitidas.
type PDP struct {
	mu       sync.RWMutex
	engine   *cedarEngine      // nil ⇒ política indisponível (fail-closed)
	anchor   ed25519.PublicKey // trust anchor para hot-reload
	dir      string            // directório do bundle (para Reload)
	onReload func(PolicyChangeEvent)
}

// PolicyChangeEvent descreve uma alteração de política aplicada por um hot-reload
// bem-sucedido. É entregue ao callback registado via [WithReloadAudit] para que
// o chamador (RM / Event Store, AOS-002/003) grave um registo de audit de
// PRIMEIRA CLASSE da transição — fechando o AC#4 ("changelog reflectido no audit
// trail, verificável") sem acoplar o PDP ao Event Store.
type PolicyChangeEvent struct {
	// OldVersion é a policy_version em vigor antes do reload ("" se não carregado).
	OldVersion string
	// NewVersion é a policy_version que passou a vigorar.
	NewVersion string
	// ContentHash é o sha256 canónico do bundle novo (liga a alteração ao conteúdo).
	ContentHash string
	// At é o instante (UTC) em que a troca foi aplicada.
	At time.Time
}

// Option configura um [PDP] em [Open].
type Option func(*PDP)

// WithTrustAnchor fornece o trust anchor a partir de uma fonte CONFIÁVEL,
// provisionada out-of-band (VCS/deploy, permissões read-only), em vez de o ler
// do próprio directório mutável do bundle. Fecha o vector de cold-start em que um
// adversário com escrita no dir do bundle substitui o anchor E re-assina o bundle
// com a sua chave, contornando a verificação. Preferir este modo em produção.
func WithTrustAnchor(anchor ed25519.PublicKey) Option {
	return func(p *PDP) { p.anchor = append(ed25519.PublicKey(nil), anchor...) }
}

// WithReloadAudit regista um callback invocado após CADA hot-reload bem-sucedido
// com a transição de versão ([PolicyChangeEvent]). Permite ao chamador emitir um
// evento de audit da alteração de política (AC#4) sem que o PDP dependa do Event
// Store. O callback corre fora do lock; não deve entrar em pânico.
func WithReloadAudit(fn func(PolicyChangeEvent)) Option {
	return func(p *PDP) { p.onReload = fn }
}

// Open carrega, verifica e compila o bundle do directório dado, devolvendo um
// PDP pronto. Passos (todos fail-closed):
//  1. obtém o trust anchor — de [WithTrustAnchor] se fornecido (recomendado),
//     senão lê trust_anchor.pub do dir do bundle;
//  2. lê o bundle (.cedar + manifest.json + aos_authz.sig);
//  3. verifica content_hash + assinatura ed25519 contra o trust anchor;
//  4. compila a policy set Cedar em memória (uma vez).
//
// TRUST MODEL. Sem [WithTrustAnchor], o anchor é lido do MESMO directório que o
// bundle e a assinatura; isso só é seguro se esse directório for provisionado
// out-of-band e read-only (nunca o mesmo local mutável de um bundle
// hot-reloaded). Em contextos onde o dir do bundle é gravável por terceiros,
// fornecer o anchor via [WithTrustAnchor] a partir de uma fonte confiável.
//
// Um bundle ausente devolve ErrPolicyUnavailable; não-assinado/adulterado
// devolve ErrSignatureInvalid.
func Open(dir string, opts ...Option) (*PDP, error) {
	p := &PDP{dir: dir}
	for _, opt := range opts {
		opt(p)
	}
	if len(p.anchor) == 0 {
		anchor, err := loadTrustAnchor(dir)
		if err != nil {
			return nil, err
		}
		p.anchor = anchor
	}
	rb, err := loadRawBundle(dir)
	if err != nil {
		return nil, err
	}
	if err := rb.Verify(p.anchor); err != nil {
		return nil, err
	}
	eng, err := newCedarEngine(rb.PolicyFiles, rb.Manifest.PolicyVersion)
	if err != nil {
		return nil, err
	}
	p.engine = eng
	return p, nil
}

// NewUnloaded devolve um PDP SEM política carregada. Toda a decisão é deny com
// ErrPolicyUnavailable até um Reload bem-sucedido — o estado seguro por omissão.
func NewUnloaded() *PDP { return &PDP{} }

// Version devolve a policy_version actualmente em vigor ("" se não carregado).
func (p *PDP) Version() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.engine == nil {
		return ""
	}
	return p.engine.version
}

// Decide avalia um pedido de decisão e devolve o veredicto (contrato C1). É
// PURA e SEM EFEITOS: a mesma (Input, policy_version) produz sempre a mesma
// Decision. Nunca devolve ausência de resposta — em qualquer erro de porta a
// Decision é Deny (fail-closed) e o erro correspondente é devolvido à parte.
func (p *PDP) Decide(_ context.Context, in Input) (Decision, error) {
	p.mu.RLock()
	eng := p.engine
	p.mu.RUnlock()

	if eng == nil {
		return Decision{Effect: Deny, Reason: ErrPolicyUnavailable.Msg}, ErrPolicyUnavailable
	}
	if err := in.validate(); err != nil {
		return Decision{Effect: Deny, Reason: err.Error(), PolicyVersion: eng.version},
			fmt.Errorf("%w: %v", ErrMalformedRequest, err)
	}

	allow, reason, err := eng.evaluate(in)
	if err != nil {
		return Decision{Effect: Deny, Reason: reason, PolicyVersion: eng.version}, err
	}
	if !allow {
		return Decision{Effect: Deny, Reason: reason, PolicyVersion: eng.version}, nil
	}
	return Decision{
		Effect:        Permit,
		Reason:        reason,
		PolicyVersion: eng.version,
		Obligations:   obligationsFor(in),
	}, nil
}

// Reload verifica e compila o bundle no directório do PDP e, SÓ se a sua
// policy_version for estritamente mais recente (SemVer) do que a em vigor,
// troca-o atomicamente. Decisões já emitidas não são afectadas (o motor antigo
// permanece válido para chamadas em curso até libertarem a referência).
//
// Rejeita, fail-closed: bundle não-assinado/adulterado (ErrSignatureInvalid) e
// versão não-crescente (ErrStalePolicyVersion — sentinela DEDICADA, distinta de
// falha criptográfica, para não regredir política em vigor). Uma versão igual ou
// anterior devolve erro sem alterar o estado.
//
// Concorrência: a verificação de monotonia e a troca do motor são ATÓMICAS sob o
// mesmo Lock de escrita — dois Reload concorrentes nunca regridem a versão
// abaixo da mais recente já aplicada (sem o TOCTOU de ler a versão sob RLock e
// trocar sob um Lock separado).
func (p *PDP) Reload() error {
	rb, err := loadRawBundle(p.dir)
	if err != nil {
		return err
	}
	p.mu.RLock()
	anchor := p.anchor
	p.mu.RUnlock()

	if len(anchor) == 0 {
		return fmt.Errorf("%w: PDP sem trust anchor para reload", ErrPolicyUnavailable)
	}
	if err := rb.Verify(anchor); err != nil {
		return err
	}
	newVer := rb.Manifest.PolicyVersion
	// Compila fora da secção crítica (caro); a decisão de trocar (verificação de
	// monotonia) é re-avaliada e efectuada atomicamente sob o Lock abaixo.
	eng, err := newCedarEngine(rb.PolicyFiles, newVer)
	if err != nil {
		return err
	}

	p.mu.Lock()
	var cur string
	if p.engine != nil {
		cur = p.engine.version
	}
	if cur != "" && !semverGreater(newVer, cur) {
		p.mu.Unlock()
		return fmt.Errorf("%w: versao %q nao e mais recente que a em vigor %q (hot-reload rejeitado)",
			ErrStalePolicyVersion, newVer, cur)
	}
	p.engine = eng
	cb := p.onReload
	p.mu.Unlock()

	if cb != nil {
		cb(PolicyChangeEvent{
			OldVersion:  cur,
			NewVersion:  newVer,
			ContentHash: rb.Manifest.ContentHash,
			At:          time.Now().UTC(),
		})
	}
	return nil
}

// semverGreater indica se a > b para versões MAJOR.MINOR.PATCH. Pré-releases e
// metadados de build são ignorados (a política de referência usa SemVer
// numérico simples). Versões malformadas comparam como não-maiores (seguro).
func semverGreater(a, b string) bool {
	pa, oka := parseSemver(a)
	pb, okb := parseSemver(b)
	if !oka || !okb {
		return false
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parseSemver(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	// Descarta pré-release/build.
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
