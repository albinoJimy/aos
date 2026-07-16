package network

import (
	"context"
	"errors"
	"math"
	"net"
	"strings"
	"sync"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// Razões estáveis de uma decisão de resolução DNS (para o chamador ramificar sem
// parse de texto, e para o audit/span). Espelham as Reason* de egress (AOS-067).
const (
	// ReasonDNSResolved — o nome está na allowlist do principal E todos os IPs
	// resolvidos são coerentes com a allowlist (anti-rebinding). Única via de allow.
	ReasonDNSResolved = "resolucao DNS permitida (nome e IPs coerentes com a allowlist)"
	// ReasonDNSNotInList — o nome consultado NÃO é um host permitido na egress
	// allowlist do principal (default-deny). O nome nem chega a ser resolvido.
	ReasonDNSNotInList = "nome fora da allowlist de egress (default-deny)"
	// ReasonDNSRebinding — o nome está na allowlist mas resolve para um IP FORA da
	// allowlist do principal (coerência nome→IP violada: DNS rebinding).
	ReasonDNSRebinding = "IP resolvido fora da allowlist do principal (anti-rebinding)"
	// ReasonDNSExfilEntropy — um label da consulta tem entropia de Shannon acima do
	// limiar (dados encapsulados/tunneling); a consulta é bloqueada antes de resolver.
	ReasonDNSExfilEntropy = "consulta DNS de alta entropia (exfiltracao)"
	// ReasonDNSExfilVolume — volume de consultas ao mesmo domínio numa janela acima do
	// limiar (tunneling por volume).
	ReasonDNSExfilVolume = "volume de consultas DNS acima do limiar (exfiltracao)"
	// ReasonDNSNoPolicy — sem allowlist resolúvel para o principal (fail-closed): a
	// resolução é negada, nunca por omissão permitida.
	ReasonDNSNoPolicy = "sem allowlist DNS para o principal (fail-closed)"
	// ReasonDNSResolveFailed — o resolvedor CONTROLADO não resolve o nome (NXDOMAIN/
	// erro): fail-closed, nega em vez de cair para o resolver do host/público.
	ReasonDNSResolveFailed = "nome nao resolvido pelo resolvedor controlado (fail-closed)"
	// ReasonDNSInvalidName — o nome é vazio/malformado: fail-closed.
	ReasonDNSInvalidName = "nome DNS invalido (fail-closed)"
	// ReasonDNSAuditFailed — a selagem do evento de segurança no WORM falhou: a
	// resolução degrada para deny (audit-before-effect).
	ReasonDNSAuditFailed = "audit de resolucao DNS indisponivel (fail-closed)"
)

// Erros fail-closed do construtor do DNS filter.
var (
	// ErrNilDNSResolver — [NewDNSFilter] sem [Resolver] controlado. Sem resolvedor
	// controlado a única alternativa seria o resolver do host/público — proibido
	// (fail-closed): a construção recusa.
	ErrNilDNSResolver = errors.New("network: resolvedor DNS controlado nil")
	// ErrExfilConfigInvalid — [WithExfilConfig] recebeu um [ExfilConfig] incompleto
	// (versão vazia ou algum limiar <= 0), o que desligaria silenciosamente a deteção de
	// exfiltração. Fail-closed no arranque: uma má-config não passa por deteção vácua.
	ErrExfilConfigInvalid = errors.New("network: ExfilConfig invalido (versao vazia ou limiares <= 0)")
)

// Atributos de span da decisão DNS (não-secretos: nome/decisão não são segredos).
const (
	OpDNSDecision   = "dns_decision"
	AttrDNSName     = "aos.dns.name"
	AttrDNSAllowed  = "aos.dns.allowed"
	AttrDNSReason   = "aos.dns.reason"
	AttrDNSResolved = "aos.dns.resolved_ips"
)

// Resolver é a PORTA do resolvedor DNS CONTROLADO da sandbox: dado um nome, devolve
// os IPs a que resolve. É deliberadamente o ponto de substituição do resolver do
// host/público — a sandbox NUNCA cai para a resolução arbitrária do sistema. A impl
// de referência ([StaticResolver]) é um mapa injectável nome→IPs (determinista, sem
// rede real); um deployment ligaria aqui um resolvedor DNS interno controlado.
type Resolver interface {
	// Resolve devolve os IPs do nome, ou um erro se o nome não é resolúvel. Um erro
	// (NXDOMAIN/timeout) é fail-closed no [DNSFilter]: nega, nunca cai para o host.
	Resolve(ctx context.Context, name string) ([]net.IP, error)
}

// ErrNXDOMAIN — nome desconhecido do resolvedor controlado.
var ErrNXDOMAIN = errors.New("network: nome DNS nao encontrado (NXDOMAIN)")

// StaticResolver é a impl de REFERÊNCIA de [Resolver]: um mapa imutável nome→IPs.
// Determinista e sem rede real — modela o resolvedor controlado da sandbox. Seguro
// para leitura concorrente (sem estado mutável após construção).
type StaticResolver struct {
	entries map[string][]net.IP
}

// NewStaticResolver constrói o resolvedor controlado a partir de um mapa nome→IPs. Os
// nomes são normalizados (minúsculas, sem ponto final); os IPs são copiados.
func NewStaticResolver(m map[string][]net.IP) *StaticResolver {
	entries := make(map[string][]net.IP, len(m))
	for name, ips := range m {
		n := normalizeName(name)
		if n == "" {
			continue
		}
		cp := make([]net.IP, 0, len(ips))
		for _, ip := range ips {
			if ip != nil {
				cp = append(cp, append(net.IP(nil), ip...))
			}
		}
		entries[n] = cp
	}
	return &StaticResolver{entries: entries}
}

// Resolve implementa [Resolver]: devolve os IPs do nome ou [ErrNXDOMAIN].
func (r *StaticResolver) Resolve(_ context.Context, name string) ([]net.IP, error) {
	ips, ok := r.entries[normalizeName(name)]
	if !ok || len(ips) == 0 {
		return nil, ErrNXDOMAIN
	}
	out := make([]net.IP, len(ips))
	for i, ip := range ips {
		out[i] = append(net.IP(nil), ip...)
	}
	return out, nil
}

// ExfilConfig é o limiar VERSIONADO de deteção de exfiltração por DNS. A "política
// DNS" é a egress allowlist de AOS-067 MAIS estes limiares versionados: um label de
// alta entropia (dados encapsulados) ou um volume de consultas acima do limiar numa
// janela marcam a consulta como exfiltração e bloqueiam-na.
type ExfilConfig struct {
	// Version identifica a versão dos limiares (selada no audit junto da allowlist).
	Version string
	// EntropyBitsThreshold é a entropia de Shannon (bits/carácter) a partir da qual um
	// label é suspeito de encapsular dados. base32≈4.4-5, hex≈3.7-4.0, texto natural
	// tende a <3.5. O tecto teórico é log2(alfabeto): hex NUNCA atinge 4.0 (tecto exacto
	// do hex) e base32 curto fica abaixo de 4.0, pelo que um limiar de 4.0 deixava passar
	// exfil em hex/base32-curto — o default é conservador (ver [DefaultExfilConfig]).
	EntropyBitsThreshold float64
	// MinLabelLen é o comprimento mínimo de um label para a entropia ser avaliada
	// (labels curtos têm entropia pouco fiável).
	MinLabelLen int
	// MaxQueriesPerWindow é o nº máximo de consultas ao MESMO domínio (por principal)
	// dentro de [Window] antes de a consulta seguinte ser bloqueada por volume.
	MaxQueriesPerWindow int
	// Window é a janela deslizante da contagem de volume.
	Window time.Duration
}

// DefaultExfilConfig devolve limiares por omissão sensatos e deterministas. O limiar
// de entropia é 3.5 (não 4.0): o tecto teórico do hex é log2(16)=4.0 e um label hex
// realista (32-63 chars) mede H≈3.66-4.0 — NUNCA ≥4.0 — logo 4.0 deixava passar
// sistematicamente exfil codificada em hex e base32 curto. 3.5 apanha hex/base32 e
// mantém a maioria do texto natural (labels de palavras) de fora; é ainda assim um
// sinal de defense-in-depth (a prevenção real é o match-EXACTO de host da allowlist).
func DefaultExfilConfig() ExfilConfig {
	return ExfilConfig{
		Version:              "dns-exfil/v1",
		EntropyBitsThreshold: 3.5,
		MinLabelLen:          20,
		MaxQueriesPerWindow:  20,
		Window:               60 * time.Second,
	}
}

// validate impõe fail-closed que um [ExfilConfig] fornecido é COMPLETO: versão
// não-vazia e todos os limiares > 0. Um zero-value (thresholds 0) desligaria
// silenciosamente a deteção de entropia (:EntropyBitsThreshold<=0) e de volume
// (:MaxQueriesPerWindow<=0) enquanto [dnsPolicyVersion] ainda produziria uma string de
// versão não-vazia — uma má-config passaria despercebida. Por isso o construtor
// recusa-a em vez de arrancar com deteção vácua.
func (c ExfilConfig) validate() error {
	if c.Version == "" ||
		c.EntropyBitsThreshold <= 0 ||
		c.MinLabelLen <= 0 ||
		c.MaxQueriesPerWindow <= 0 ||
		c.Window <= 0 {
		return ErrExfilConfigInvalid
	}
	return nil
}

// DNSDecision é o veredicto de uma resolução DNS: análogo a [Decision] do egress.
type DNSDecision struct {
	Allow         bool
	Reason        string
	PolicyVersion string
}

// DNSFilter é o resolvedor DNS por sandbox (AOS-068) que COMPÕE a rede default-deny
// de AOS-067: envolve um [Resolver] CONTROLADO e só devolve IPs quando o nome está na
// egress allowlist do principal E todos os IPs resolvidos são coerentes com essa
// allowlist (anti-rebinding). Consultas fora da allowlist ou padrões de exfiltração
// (alta entropia/volume) são NEGADOS e SELADOS como evento de segurança no WORM
// (reutiliza o [SecurityAuditSink] de AOS-067). FAIL-CLOSED em toda a borda: sem
// allowlist, sem resolução ou audit indisponível ⇒ deny, nunca fallback ao host.
type DNSFilter struct {
	resolver Resolver             // resolvedor CONTROLADO (nunca o do host/público)
	policies EgressPolicyResolver // allowlist de egress por principal (AOS-067)
	sink     SecurityAuditSink    // WORM tamper-evident (obrigatório)
	exfil    *exfilDetector
	tracer   Tracer
	now      func() time.Time
}

// DNSFilterOption configura o [DNSFilter].
type DNSFilterOption func(*DNSFilter)

// WithDNSSecurityAuditSink injecta o sink WORM (OBRIGATÓRIO, como no egress: uma
// negação DNS é um evento de segurança que TEM de ser selado).
func WithDNSSecurityAuditSink(s SecurityAuditSink) DNSFilterOption {
	return func(f *DNSFilter) { f.sink = s }
}

// WithExfilConfig substitui os limiares de exfiltração por omissão.
func WithExfilConfig(c ExfilConfig) DNSFilterOption {
	return func(f *DNSFilter) { f.exfil = newExfilDetector(c) }
}

// WithDNSTracer injecta a porta de observabilidade (default [NoopTracer]).
func WithDNSTracer(t Tracer) DNSFilterOption { return func(f *DNSFilter) { f.tracer = t } }

// withDNSClock injecta o relógio da janela de volume (uso interno/testes). O
// timestamp NÃO entra na coerência nome→IP (determinismo) — só na janela de volume e
// no registo observacional.
func withDNSClock(fn func() time.Time) DNSFilterOption {
	return func(f *DNSFilter) { f.now = fn }
}

// NewDNSFilter constrói o filtro DNS sobre um resolvedor controlado (obrigatório), a
// allowlist de egress por principal (obrigatória) e um sink WORM (obrigatório). Todos
// fail-closed no arranque: sem resolvedor controlado ([ErrNilDNSResolver]) a única
// alternativa seria o resolver do host (proibido); sem allowlist ([ErrNilResolver])
// nada é resolúvel; sem sink ([ErrNilSink]) uma negação ficaria por selar.
func NewDNSFilter(resolver Resolver, policies EgressPolicyResolver, opts ...DNSFilterOption) (*DNSFilter, error) {
	if resolver == nil {
		return nil, ErrNilDNSResolver
	}
	if policies == nil {
		return nil, ErrNilResolver
	}
	f := &DNSFilter{
		resolver: resolver,
		policies: policies,
		tracer:   NoopTracer{},
		exfil:    newExfilDetector(DefaultExfilConfig()),
		now:      time.Now,
	}
	for _, o := range opts {
		o(f)
	}
	// FAIL-CLOSED: um ExfilConfig fornecido (via WithExfilConfig) tem de ser completo —
	// caso contrário a deteção de entropia/volume ficaria silenciosamente desligada. O
	// default é sempre válido.
	if err := f.exfil.cfg.validate(); err != nil {
		return nil, err
	}
	if f.sink == nil {
		return nil, ErrNilSink
	}
	if f.tracer == nil {
		f.tracer = NoopTracer{}
	}
	if f.now == nil {
		f.now = time.Now
	}
	return f, nil
}

// Resolve é a operação central (AOS-068): resolve um nome PARA um principal através do
// resolvedor controlado, mas SÓ devolve IPs se o nome e os IPs resolvidos forem
// coerentes com a egress allowlist do principal. Devolve os IPs (nil num deny), a
// [DNSDecision] e, separadamente, um erro operacional APENAS quando a selagem no WORM
// falha (a resolução é então forçada a deny — audit-before-effect). Em toda a outra
// borda a decisão é deny fail-closed sem erro. NUNCA resolve por omissão nem cai para
// o resolver do host.
func (f *DNSFilter) Resolve(ctx context.Context, principal referencemonitor.Principal, name string) ([]net.IP, DNSDecision, error) {
	ctx, span := f.tracer.StartSpan(ctx, OpDNSDecision)
	defer span.End()
	span.SetAttribute(AttrOperationName, OpDNSDecision)
	span.SetAttribute(AttrPrincipalNHI, principal.NHIID)
	span.SetAttribute(AttrPrincipalClass, principal.AgentClass)

	qname := normalizeName(name)
	span.SetAttribute(AttrDNSName, qname)

	// (1) FAIL-CLOSED: nome vazio/malformado não é avaliável.
	if qname == "" || !isPlausibleName(qname) {
		return f.deny(ctx, span, principal, qname, nil, ReasonDNSInvalidName, "")
	}

	// (2) Resolve a allowlist do principal. Ausente/erro ⇒ default-deny total.
	policy, err := f.policies.Resolve(ctx, principal)
	if err != nil || policy == nil {
		return f.deny(ctx, span, principal, qname, nil, ReasonDNSNoPolicy, "")
	}
	version := dnsPolicyVersion(policy.Version(), f.exfil.cfg.Version)

	// (3) DETEÇÃO DE EXFILTRAÇÃO POR ENTROPIA (antes da allowlist): é uma verificação
	// PURA (não muta estado), logo pode correr para qualquer nome sem risco de DoS. Um
	// label de alta entropia é negado com razão específica mesmo que o domínio pai
	// estivesse na allowlist — preserva o sinal distinto de tunneling.
	if f.exfil.highEntropy(qname) {
		return f.deny(ctx, span, principal, qname, nil, ReasonDNSExfilEntropy, version)
	}

	// (4) O NOME tem de ser um host permitido na allowlist do principal. Um nome fora
	// da allowlist NEM chega a ser resolvido (fecha o canal de exfiltração por DNS).
	if !policy.hostAllowed(principal, qname) {
		return f.deny(ctx, span, principal, qname, nil, ReasonDNSNotInList, version)
	}

	// (4b) DETEÇÃO DE EXFILTRAÇÃO POR VOLUME — só APÓS o gate de allowlist. Contar o
	// volume ANTES do gate deixava um atacante (a) fazer crescer o mapa de estado sem
	// limite com nomes arbitrários (todos negados no passo 4) e (b) envenenar o contador
	// do registeredDomain de um host legítimo (ex.: consultas negadas a *.github.com
	// inflavam a janela partilhada com api.github.com), negando resoluções legítimas por
	// falso-positivo. Agora só nomes JÁ na allowlist contam para o volume.
	now := f.now()
	f.exfil.record(principalID(principal), registeredDomain(qname), now)
	if f.exfil.overVolume(principalID(principal), registeredDomain(qname), now) {
		return f.deny(ctx, span, principal, qname, nil, ReasonDNSExfilVolume, version)
	}

	// (5) Resolve pelo resolvedor CONTROLADO. Erro/vazio ⇒ fail-closed (nunca host).
	ips, err := f.resolver.Resolve(ctx, qname)
	if err != nil || len(ips) == 0 {
		return f.deny(ctx, span, principal, qname, nil, ReasonDNSResolveFailed, version)
	}

	// (6) ANTI-REBINDING (coerência nome→IP): TODOS os IPs resolvidos têm de ser
	// destinos permitidos para o principal na allowlist. Um nome permitido que resolva
	// para um IP fora da allowlist é rejeitado (rebinding), selando o IP ofensor.
	for _, ip := range ips {
		if !policy.ipAllowed(principal, ip) {
			return f.denyIP(ctx, span, principal, qname, ip, ReasonDNSRebinding, version)
		}
	}

	// (7) ALLOW: nome na allowlist e todos os IPs coerentes.
	span.SetAttribute(AttrDNSAllowed, true)
	span.SetAttribute(AttrDNSReason, ReasonDNSResolved)
	span.SetAttribute(AttrPolicyVersion, version)
	span.SetAttribute(AttrDNSResolved, ipsString(ips))
	return ips, DNSDecision{Allow: true, Reason: ReasonDNSResolved, PolicyVersion: version}, nil
}

// deny materializa um bloqueio: anota o span, SELA o evento de segurança no WORM
// (atribuível a principal + nome) e devolve DNSDecision{Allow:false}. Se a selagem
// falhar, a decisão permanece deny e o erro é surfaçado (o WORM não registou).
func (f *DNSFilter) deny(ctx context.Context, span Span, principal referencemonitor.Principal, name string, offendingIP net.IP, reason, version string) ([]net.IP, DNSDecision, error) {
	return f.denyIP(ctx, span, principal, name, offendingIP, reason, version)
}

// denyIP é o deny com um IP ofensor opcional (rebinding) selado no evento.
func (f *DNSFilter) denyIP(ctx context.Context, span Span, principal referencemonitor.Principal, name string, offendingIP net.IP, reason, version string) ([]net.IP, DNSDecision, error) {
	span.SetAttribute(AttrDNSAllowed, false)
	span.SetAttribute(AttrDNSReason, reason)
	if version != "" {
		span.SetAttribute(AttrPolicyVersion, version)
	}
	sealErr := f.seal(ctx, principal, name, offendingIP, reason, version)
	return nil, DNSDecision{Allow: false, Reason: reason, PolicyVersion: version}, sealErr
}

// seal sela um evento de segurança no WORM reutilizando o [SecurityAuditSink] de
// AOS-067: o nome consultado vai em Destination.Host (Port 0 — DNS é port-agnóstico),
// o IP ofensor (rebinding) em Destination.IP quando presente. A razão distingue o
// evento DNS. Não transporta segredos (nome/IP/decisão não são segredos).
func (f *DNSFilter) seal(ctx context.Context, principal referencemonitor.Principal, name string, offendingIP net.IP, reason, version string) error {
	corr := correlationFrom(ctx)
	dest := Destination{Host: name}
	if offendingIP != nil {
		dest.IP = offendingIP.String()
	}
	return f.sink.Seal(ctx, SecurityEvent{
		Principal:     principal,
		Destination:   dest,
		Decision:      SecurityBlocked,
		Reason:        reason,
		PolicyVersion: version,
		RunID:         corr.runID,
		StepID:        corr.stepID,
		Timestamp:     f.now(),
	})
}

// hostAllowed indica se o NOME é um host explicitamente listado numa regra que casa o
// principal (port-agnóstico: a resolução DNS não tem porta). É a base do critério "só
// resolve nomes na allowlist". Método na mesma package (compõe [Policy] de AOS-067).
func (p *Policy) hostAllowed(principal referencemonitor.Principal, host string) bool {
	for _, r := range p.compiled {
		if !principalInScope(r.principals, principal) {
			continue
		}
		for _, d := range r.dests {
			if _, ok := d.hosts[host]; ok {
				return true
			}
		}
	}
	return false
}

// ipAllowed indica se o IP pertence a um CIDR permitido numa regra que casa o
// principal (port-agnóstico). É a base da coerência nome→IP (anti-rebinding): um IP
// resolvido que não seja destino permitido do principal não passa.
//
// GRANULARIDADE (deliberada): a coerência é imposta ao nível da allowlist INTEIRA do
// principal, NÃO do destino específico que lista o nome. Um nome pode assim ligar-se a
// QUALQUER IP allowlisted do principal, mesmo o CIDR de outro destino da mesma
// allowlist. Isto é intencional porque o modelo de política separa hosts e CIDRs em
// destinos distintos (ex.: api.github.com num dest só-hosts e 93.184.216.0/24 num dest
// só-CIDR): exigir o CIDR do PRÓPRIO destino do nome negaria resoluções legítimas cujo
// dest de host não tem CIDR. A invariante central (anti-rebinding) mantém-se — o IP TEM
// de estar na allowlist do principal, logo um rebinding para um IP FORA é rejeitado — e
// o [EgressFilter] permitiria esse mesmo IP na conexão, pelo que não há escape de
// fronteira; apenas a granularidade é "qualquer destino do principal", não "o CIDR do
// próprio nome".
func (p *Policy) ipAllowed(principal referencemonitor.Principal, ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, r := range p.compiled {
		if !principalInScope(r.principals, principal) {
			continue
		}
		for _, d := range r.dests {
			for _, n := range d.cidrs {
				if n.Contains(ip) {
					return true
				}
			}
		}
	}
	return false
}

// exfilDetector deteta padrões de exfiltração por DNS: entropia (por label) e volume
// (por (principal,domínio) numa janela). O estado de volume é protegido por mutex
// (concorrência/-race); o relógio é injectável (determinismo).
type exfilDetector struct {
	cfg ExfilConfig
	mu  sync.Mutex
	// hits mapeia "principal|domínio" → timestamps das consultas na janela.
	hits map[string][]time.Time
	// lastSweep é o instante da última varredura de evicção (limita o custo O(n) da
	// remoção de chaves expiradas a uma vez por janela).
	lastSweep time.Time
}

func newExfilDetector(cfg ExfilConfig) *exfilDetector {
	return &exfilDetector{cfg: cfg, hits: make(map[string][]time.Time)}
}

// record acrescenta uma consulta à janela do par (principal,domínio) e poda as antigas.
// Corre só APÓS o gate de allowlist (ver Resolve passo 4b), pelo que as chaves são
// limitadas ao conjunto (principal, domínio-registado-allowlisted) — bounded. Uma
// varredura periódica evita, ainda assim, que chaves de principais/domínios que ficaram
// inactivos persistam para sempre (crescimento não-limitado ao longo da vida do processo).
func (d *exfilDetector) record(principal, domain string, now time.Time) {
	key := principal + "|" + domain
	cutoff := now.Add(-d.cfg.Window)
	d.mu.Lock()
	defer d.mu.Unlock()
	ts := d.hits[key]
	kept := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	d.hits[key] = kept
	d.sweepLocked(now)
}

// sweepLocked evicta chaves cuja janela ficou vazia (todas as consultas expiraram),
// no máximo uma vez por [ExfilConfig.Window] para amortizar o custo. Chamado com o mutex
// já preso. É a evicção que impede o mapa hits de crescer sem teto: uma chave inactiva
// (sem consultas na janela) é removida em vez de ficar com um slice vazio-mas-presente.
func (d *exfilDetector) sweepLocked(now time.Time) {
	if now.Sub(d.lastSweep) < d.cfg.Window {
		return
	}
	d.lastSweep = now
	cutoff := now.Add(-d.cfg.Window)
	for k, ts := range d.hits {
		fresh := false
		for _, t := range ts {
			if t.After(cutoff) {
				fresh = true
				break
			}
		}
		if !fresh {
			delete(d.hits, k)
		}
	}
}

// overVolume indica se o nº de consultas na janela excede o limiar.
func (d *exfilDetector) overVolume(principal, domain string, now time.Time) bool {
	if d.cfg.MaxQueriesPerWindow <= 0 {
		return false
	}
	key := principal + "|" + domain
	cutoff := now.Add(-d.cfg.Window)
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, t := range d.hits[key] {
		if t.After(cutoff) {
			n++
		}
	}
	return n > d.cfg.MaxQueriesPerWindow
}

// highEntropy indica se o nome carrega dados encapsulados (tunneling): (a) ALGUM label
// com comprimento ≥ MinLabelLen tem entropia de Shannon ≥ ao limiar, OU (b) a
// CONCATENAÇÃO dos labels de subdomínio (tudo excepto o domínio registado) tem entropia
// ≥ ao limiar. O ramo (b) fecha a evasão por FRAGMENTAÇÃO: distribuir o payload por
// vários labels curtos (< MinLabelLen), individualmente ignorados por (a), mas de alta
// entropia quando reagregados. É um SINAL de audit (defense-in-depth), não o gate: a
// fronteira real é o match-exacto de host da allowlist.
func (d *exfilDetector) highEntropy(name string) bool {
	if d.cfg.EntropyBitsThreshold <= 0 {
		return false
	}
	labels := strings.Split(name, ".")
	// (a) por-label.
	for _, label := range labels {
		if len(label) < d.cfg.MinLabelLen {
			continue
		}
		if shannonBitsPerChar(label) >= d.cfg.EntropyBitsThreshold {
			return true
		}
	}
	// (b) concatenação dos labels de subdomínio (exclui os 2 últimos = domínio
	// registado, texto tipicamente natural). Só avaliada se ≥ MinLabelLen, evitando
	// falsos-positivos em subdomínios curtos legítimos (ex.: "api" em api.github.com).
	if len(labels) > 2 {
		sub := strings.Join(labels[:len(labels)-2], "")
		if len(sub) >= d.cfg.MinLabelLen && shannonBitsPerChar(sub) >= d.cfg.EntropyBitsThreshold {
			return true
		}
	}
	return false
}

// shannonBitsPerChar calcula a entropia de Shannon (bits por carácter) de uma string.
// Determinista: só depende das frequências dos bytes.
func shannonBitsPerChar(s string) float64 {
	if s == "" {
		return 0
	}
	var freq [256]int
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
	}
	n := float64(len(s))
	var h float64
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}

// normalizeName normaliza um nome DNS: minúsculas, sem espaços e sem ponto final.
func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
}

// isPlausibleName é uma validação sintáctica grosseira (fail-closed): um nome tem de
// ter ao menos um carácter não-espaço e nenhum label vazio. Não valida a existência.
func isPlausibleName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") || strings.Contains(name, "..") {
		return false
	}
	return true
}

// registeredDomain devolve o domínio agregador para a contagem de volume: os últimos
// dois labels (ex.: "a1b2.exfil.com" → "exfil.com"), de modo a que muitos subdomínios
// do mesmo domínio caiam no MESMO contador. Sem PSL: heurística determinista.
//
// LIMITAÇÃO CONHECIDA (aceite): sem uma Public Suffix List, (i) um atacante que espalhe
// as consultas por vários domínios apex distintos mantém cada contador abaixo do limiar
// (a agregação de volume é por-domínio, não por-principal); e (ii) sufixos multi-label
// (foo.co.uk → co.uk) agregam domínios não-relacionados. Não se usa PSL de propósito: o
// módulo é ZERO-dependências-externas (go.mod, integração por path local), e a
// prevenção REAL de exfiltração é o match-EXACTO de host da allowlist (um subdomínio de
// exfil não é um host exacto, logo é negado no passo 4 antes de sequer contar volume) —
// a contagem por registeredDomain é um sinal complementar, não a fronteira.
func registeredDomain(name string) string {
	labels := strings.Split(name, ".")
	if len(labels) <= 2 {
		return name
	}
	return strings.Join(labels[len(labels)-2:], ".")
}

// dnsPolicyVersion combina a versão da egress allowlist (AOS-067) com a versão dos
// limiares de exfiltração — a "política DNS" versionada selada no audit.
func dnsPolicyVersion(policyVersion, exfilVersion string) string {
	if policyVersion == "" {
		return exfilVersion
	}
	return policyVersion + "+" + exfilVersion
}

// ipsString devolve os IPs numa forma canónica e não-secreta para o span.
func ipsString(ips []net.IP) string {
	parts := make([]string, len(ips))
	for i, ip := range ips {
		parts[i] = ip.String()
	}
	return strings.Join(parts, ",")
}
