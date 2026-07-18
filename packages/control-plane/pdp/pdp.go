package pdp

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// PDP é o Policy Decision Point: avalia a política de referência compilada em
// memória e devolve decisões deterministas (contrato C1). É seguro para uso
// concorrente; o hot-reload troca atomicamente o motor sob um RWMutex sem mutar
// decisões já emitidas.
type PDP struct {
	mu       sync.RWMutex
	engine   *cedarEngine      // nil ⇒ política indisponível (fail-closed)
	curFP    bundleFingerprint // impressão digital do bundle em vigor (para o diff)
	anchor   ed25519.PublicKey // trust anchor para hot-reload
	dir      string            // directório do bundle (para Reload)
	onReload func(PolicyChangeEvent) error
	// sealMu serializa a SELAGEM do changelog `policy.changed` na ordem exacta em
	// que as versões são aplicadas (ver [PDP.Reload]). É distinto do RWMutex do
	// motor para não bloquear Decide() durante o Append (I/O) do audit.
	sealMu sync.Mutex
	tracer otelgenai.Tracer // span do reload (DoD AOS-088); nil ⇒ Noop
	// autonomy é o oráculo de níveis L0–L5 (AOS-089) que o PDP consulta em cada
	// decisão para compor o oversight (nível × classe de risco). nil ⇒ o overlay de
	// autonomia é inerte (o PDP decide como antes — a autonomia é opt-in e só
	// TIGHTENS uma decisão, nunca a afrouxa). Ligado por [WithAutonomyOracle].
	autonomyOracle autonomy.Oracle
	// boardRegions é o registo GOV board→região autorizada (AOS-094) que o PDP
	// compõe para EMITIR a obrigação `region` a partir do board do escopo de
	// identidade. nil ⇒ soberania por board inerte (opt-in; o PDP decide como antes).
	// Só TIGHTENS: pode negar fail-closed um board desconhecido ou anexar a
	// obrigação de região, nunca afrouxar um permit. Ligado por [WithBoardRegions].
	boardRegions *govsov.Registry
}

// ReloadRequest transporta a atribuição do carregamento de política: QUEM o
// efectua (Author, o principal — AC5) e PORQUÊ (Reason, o motivo do changelog —
// AC2). É propagada ao [PolicyChangeEvent] e, através do adaptador de audit
// ([AuditReloadSink]), selada no changelog `policy.changed` da hash-chain WORM.
type ReloadRequest struct {
	// Author é o principal (autor) que efectua o reload — vai no changelog (AC5).
	Author string
	// Reason é o motivo da alteração de política — vai no changelog (AC2).
	Reason string
}

// PolicyChangeEvent descreve uma alteração de política aplicada por um hot-reload
// bem-sucedido. É entregue ao callback registado via [WithReloadAudit] para que
// o chamador grave o changelog `policy.changed` de PRIMEIRA CLASSE da transição
// no audit trail hash-chained (AC2) — sem acoplar o PDP ao subsistema de audit.
type PolicyChangeEvent struct {
	// OldVersion é a policy_version em vigor antes do reload ("" se não carregado).
	OldVersion string
	// NewVersion é a policy_version que passou a vigorar.
	NewVersion string
	// ContentHash é o sha256 canónico do bundle novo (liga a alteração ao conteúdo).
	ContentHash string
	// Author é o principal que efectuou o reload (AC5), propagado do [ReloadRequest].
	Author string
	// Reason é o motivo declarado da alteração (AC2), propagado do [ReloadRequest].
	Reason string
	// Diff resume, de forma DETERMINISTA, o que mudou entre o bundle anterior e o
	// novo: ficheiros adicionados/removidos/alterados (por hash) e capabilities
	// adicionadas/removidas na allowlist assinada (AC2 — o "diff" do changelog).
	Diff PolicyDiff
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

// WithReloadAudit regista um OBSERVADOR invocado após CADA hot-reload
// bem-sucedido com a transição de versão ([PolicyChangeEvent]). É a variante para
// callbacks que NÃO podem falhar (ex. métricas/notificação); para um sink que sela
// o changelog e cuja falha deve ser observável, usar [WithReloadAuditSink] com
// [AuditReloadSink]. O callback é serializado na ordem de aplicação das versões
// (ver [PDP.Reload]) mas corre fora do lock do motor; não deve entrar em pânico.
func WithReloadAudit(fn func(PolicyChangeEvent)) Option {
	return func(p *PDP) {
		if fn == nil {
			p.onReload = nil
			return
		}
		p.onReload = func(ev PolicyChangeEvent) error { fn(ev); return nil }
	}
}

// WithReloadAuditSink regista o sink que SELA o changelog `policy.changed` de cada
// hot-reload bem-sucedido (AC2/AC5). Ao contrário de [WithReloadAudit], o sink
// devolve erro: uma selagem falhada (ex. Store WORM indisponível) NÃO é engolida —
// o [PDP.Reload] anota-a no span do reload (`aos.policy.audit_sealed=false` + o
// erro), tornando detectável uma alteração de política sem changelog selado. O
// reload em si mantém-se aplicado (sem janela de política ausente — AC4). O sink é
// serializado na ordem exacta de aplicação das versões. Ver [AuditReloadSink].
func WithReloadAuditSink(fn func(PolicyChangeEvent) error) Option {
	return func(p *PDP) { p.onReload = fn }
}

// WithTracer liga o [otelgenai.Tracer] usado para instrumentar o carregamento/
// verificação de política (DoD AOS-088): cada [PDP.Reload] abre um span
// "aos.policy.reload" com as versões, o content_hash e o resultado
// (applied/rejected) — NUNCA a chave nem qualquer segredo. Sem esta opção o PDP
// usa o [otelgenai.NoopTracer] (comportamento idêntico ao de antes).
func WithTracer(t otelgenai.Tracer) Option {
	return func(p *PDP) {
		if t != nil {
			p.tracer = t
		}
	}
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
	fp, err := computeFingerprint(rb.PolicyFiles)
	if err != nil {
		return nil, err
	}
	p.engine = eng
	p.curFP = fp
	return p, nil
}

// NewUnloaded devolve um PDP SEM política carregada. Toda a decisão é deny com
// ErrPolicyUnavailable até um Reload bem-sucedido — o estado seguro por omissão.
func NewUnloaded() *PDP { return &PDP{} }

// ActiveVersion devolve a policy_version assinada actualmente EM VIGOR ("" se não
// carregado) — a rastreabilidade da política em runtime à versão assinada do
// bundle (AC3): coincide sempre com o [Manifest].PolicyVersion do bundle que a
// última verificação de assinatura aceitou. É um alias explícito de [Version],
// nomeado pela sua função de traceabilidade.
//
// REFERÊNCIA RASTREÁVEL. O par (PolicyVersion SemVer, ContentHash sha256 canónico
// do bundle) É a referência assinada da política — não há um campo GitRef/SHA
// separado por desenho: o PolicyVersion é o ref versionado e o ContentHash liga-o
// univocamente ao conteúdo exacto assinado (ambos selados no changelog
// `policy.changed`). O pipeline de assinatura mapeia SemVer↔commit fora de banda.
func (p *PDP) ActiveVersion() string { return p.Version() }

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
// DETERMINISTA: a mesma (Input, policy_version, nível de autonomia) produz sempre
// a mesma Decision. Nunca devolve ausência de resposta — em qualquer erro de porta
// a Decision é Deny (fail-closed) e o erro correspondente é devolvido à parte.
//
// AUTONOMIA (AOS-089). Quando um [autonomy.Oracle] está ligado ([WithAutonomyOracle]),
// uma decisão de BASE permit é sobreposta pelo overlay de oversight (nível × classe
// de risco): ver [PDP.applyAutonomy]. O overlay só TIGHTENS (permit→escalate) — nunca
// transforma um deny em permit. Sem oráculo, o comportamento é idêntico ao anterior.
func (p *PDP) Decide(ctx context.Context, in Input) (Decision, error) {
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

	// GATE default-deny da allowlist de capabilities (AOS-007). Impõe-se ANTES de
	// qualquer regra Cedar: a capability pedida TEM de constar explicitamente da
	// allowlist assinada da agent_class do principal. A ausência de concessão é
	// recusa (fail-closed) — uma tool/capability nova sem entrada é negada até ser
	// explicitamente permitida por política re-assinada. A decisão final permit
	// exige, assim, allowlist ∧ regras Cedar. A negação é auditada pelo RM (o
	// evento de mediação carrega capability + principal + policy_version).
	//
	// FRONTEIRA DE CONFIANÇA. O gate keia em in.Principal.AgentClass e ASSUME-A já
	// resolvida de uma NHI verificada — o hook de identidade real (AOS-005/006)
	// substitui o Principal inteiro a partir do token verificado ANTES deste gate.
	// É INSEGURO compor o PDP atrás de um IdentityStub pass-through: aí a agent_class
	// vem do Call bruto do caller e é forjável, amplificando capabilities. Ver a nota
	// "Fronteira de confiança" no README e a prova em identity_gate_integration_test.go.
	if ok, greason := eng.allow.permits(in.Principal.AgentClass, in.Capability); !ok {
		return Decision{Effect: Deny, Reason: greason, PolicyVersion: eng.version}, nil
	}

	allow, reason, err := eng.evaluate(in)
	if err != nil {
		return Decision{Effect: Deny, Reason: reason, PolicyVersion: eng.version}, err
	}
	if !allow {
		return Decision{Effect: Deny, Reason: reason, PolicyVersion: eng.version}, nil
	}
	base := Decision{
		Effect:        Permit,
		Reason:        reason,
		PolicyVersion: eng.version,
		Obligations:   obligationsFor(in),
	}
	// SOBERANIA POR BOARD (AOS-094): resolve o board do escopo de identidade para a
	// sua região autorizada e anexa a obrigação `region` (que o PEP de AOS-087 impõe),
	// ou NEGA fail-closed um board desconhecido. Aplica-se ANTES do overlay de
	// autonomia; se negar, applyAutonomy é no-op (só actua sobre base permit).
	base = p.applySovereignty(in, base)
	return p.applyAutonomy(ctx, in, base), nil
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
//
// req transporta a atribuição (Author/Reason) propagada ao [PolicyChangeEvent]
// e ao changelog `policy.changed` (AC2/AC5). O carregamento/verificação abre um
// span "aos.policy.reload" (DoD) com versões/resultado, sem chave nem segredos.
func (p *PDP) Reload(ctx context.Context, req ReloadRequest) (err error) {
	p.mu.RLock()
	anchor := p.anchor
	tracer := p.tracer
	curVer := ""
	if p.engine != nil {
		curVer = p.engine.version
	}
	p.mu.RUnlock()
	if tracer == nil {
		tracer = otelgenai.NoopTracer{}
	}

	// Span do carregamento/verificação (DoD): anota versões e resultado em TODOS os
	// caminhos (applied/rejected/erro), NUNCA a chave nem qualquer segredo.
	_, span := tracer.StartSpan(ctx, opPolicyReload)
	span.SetAttribute(otelgenai.AttrOperationName, opPolicyReload)
	span.SetAttribute(attrPolicyVersionOld, curVer)
	if req.Author != "" {
		span.SetAttribute(otelgenai.AttrPrincipalNHI, req.Author)
	}
	result := reloadResultRejected
	newVer := ""
	defer func() {
		span.SetAttribute(attrPolicyReloadResult, result)
		if newVer != "" {
			span.SetAttribute(attrPolicyVersionNew, newVer)
		}
		if err != nil {
			span.SetAttribute(otelgenai.AttrErrorType, errCode(err))
		}
		span.End()
	}()

	rb, err := loadRawBundle(p.dir)
	if err != nil {
		return err
	}
	if len(anchor) == 0 {
		err = fmt.Errorf("%w: PDP sem trust anchor para reload", ErrPolicyUnavailable)
		return err
	}
	if err = rb.Verify(anchor); err != nil {
		return err
	}
	newVer = rb.Manifest.PolicyVersion
	span.SetAttribute(attrPolicyContentHash, rb.Manifest.ContentHash)
	// Compila fora da secção crítica (caro); a decisão de trocar (verificação de
	// monotonia) é re-avaliada e efectuada atomicamente sob o Lock abaixo.
	eng, err := newCedarEngine(rb.PolicyFiles, newVer)
	if err != nil {
		return err
	}
	newFP, err := computeFingerprint(rb.PolicyFiles)
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
		err = fmt.Errorf("%w: versao %q nao e mais recente que a em vigor %q (hot-reload rejeitado)",
			ErrStalePolicyVersion, newVer, cur)
		return err
	}
	oldFP := p.curFP
	p.engine = eng
	p.curFP = newFP
	cb := p.onReload
	// ORDENAÇÃO DO CHANGELOG (accountability). Adquire o mutex de selagem AINDA sob
	// o Lock de escrita do motor: como o Lock serializa as transições, a ordem por
	// que os reloads adquirem o sealMu coincide SEMPRE com a ordem por que aplicam
	// as versões — logo o Append ao audit sela na mesma ordem, mesmo com reloads
	// concorrentes (fecha a inversão de ordem do changelog). O Lock do motor é
	// libertado ANTES do Append (I/O) para não bloquear Decide() durante a selagem.
	if cb != nil {
		p.sealMu.Lock()
	}
	p.mu.Unlock()

	result = reloadResultApplied
	diff := diffFingerprints(oldFP, newFP)
	span.SetAttribute(attrPolicyDiffChanged, diff.ChangedCount())

	if cb != nil {
		// A libertação do sealMu é diferida DENTRO da selagem: se o sink entrar em
		// pânico (contra o contrato), o mutex é na mesma libertado no desenrolar do
		// pânico — não deixa a selagem trancada a bloquear reloads futuros.
		sealErr := func() error {
			defer p.sealMu.Unlock()
			return cb(PolicyChangeEvent{
				OldVersion:  cur,
				NewVersion:  newVer,
				ContentHash: rb.Manifest.ContentHash,
				Author:      req.Author,
				Reason:      req.Reason,
				Diff:        diff,
				At:          time.Now().UTC(),
			})
		}()
		// AC2/AC5 OBSERVÁVEL: uma selagem falhada (ex. Store WORM indisponível) não é
		// engolida — anota-se no span (audit_sealed=false + o erro) para que uma
		// alteração de política sem changelog selado seja detectável por quem observa
		// o reload. O reload mantém-se aplicado (sem janela de política ausente — AC4);
		// o erro de audit não regride a política já em vigor. O texto do erro é
		// operacional (falha de store), nunca a chave nem segredos.
		if sealErr != nil {
			span.SetAttribute(attrPolicyAuditSealed, false)
			span.SetAttribute(attrPolicyAuditSealError, sealErr.Error())
		} else {
			span.SetAttribute(attrPolicyAuditSealed, true)
		}
	}
	return nil
}

// errCode devolve o Code estável de um erro de porta C1 (ex. E_SIGNATURE_INVALID)
// para anotar error.type no span sem expor mensagens livres. Um erro sem Code
// resolve para "error".
func errCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "error"
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
