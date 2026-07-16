package network

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
)

// mutableClock é um relógio determinista avançável (janela de volume sem time.Now).
type mutableClock struct {
	mu sync.Mutex
	t  time.Time
}

func newMutableClock() *mutableClock {
	return &mutableClock{t: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)}
}
func (c *mutableClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *mutableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// staticResolver constrói o resolvedor controlado de referência a partir de pares
// nome→IP (uma string por IP).
func staticResolver(m map[string][]string) *StaticResolver {
	out := make(map[string][]net.IP, len(m))
	for name, ips := range m {
		parsed := make([]net.IP, 0, len(ips))
		for _, s := range ips {
			parsed = append(parsed, net.ParseIP(s))
		}
		out[name] = parsed
	}
	return NewStaticResolver(out)
}

// newDNSFilter monta um [DNSFilter] sobre a allowlist embutida (AOS-067), um
// resolvedor controlado e opções dadas.
func newDNSFilter(t *testing.T, resolver Resolver, opts ...DNSFilterOption) *DNSFilter {
	t.Helper()
	policies, err := NewEmbeddedResolver()
	if err != nil {
		t.Fatalf("NewEmbeddedResolver: %v", err)
	}
	base := []DNSFilterOption{withDNSClock(fixedClock())}
	f, err := NewDNSFilter(resolver, policies, append(base, opts...)...)
	if err != nil {
		t.Fatalf("NewDNSFilter: %v", err)
	}
	return f
}

// TestDNS_NaAllowlist_Resolvido cobre o ALLOW: um nome na allowlist do principal que
// resolve para um IP coerente com a allowlist é resolvido; por omissão não sela.
func TestDNS_NaAllowlist_Resolvido(t *testing.T) {
	store := audit.NewMemStore()
	res := staticResolver(map[string][]string{
		"api.github.com": {"93.184.216.34"}, // ∈ 93.184.216.0/24 (allowlist web-fetcher)
	})
	f := newDNSFilter(t, res, WithDNSSecurityAuditSink(NewWORMSecuritySink(store)))

	principal := principalClass("web-fetcher")
	ips, dec, err := f.Resolve(context.Background(), principal, "api.github.com")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !dec.Allow {
		t.Fatalf("nome na allowlist deveria resolver, reason=%q", dec.Reason)
	}
	if len(ips) != 1 || ips[0].String() != "93.184.216.34" {
		t.Fatalf("IPs = %v, quero [93.184.216.34]", ips)
	}
	if !strings.HasPrefix(dec.PolicyVersion, "sbx-egress/v1#") || !strings.Contains(dec.PolicyVersion, "dns-exfil/v1") {
		t.Fatalf("policy_version DNS = %q (quero allowlist+exfil versionadas)", dec.PolicyVersion)
	}
	// Um allow não sela evento de segurança por omissão.
	if head, _ := store.Head(context.Background(), EgressAuditPartition(principal)); head != 0 {
		t.Fatalf("allow não deveria selar, head=%d", head)
	}
}

// TestDNS_ForaDaAllowlist_NegadoEAuditado cobre o critério SEGURANÇA: a resolução de um
// domínio FORA da allowlist é negada E selada como evento de segurança no WORM
// tamper-evident (verificado por audit.Verify). O nome nem chega a ser resolvido.
func TestDNS_ForaDaAllowlist_NegadoEAuditado(t *testing.T) {
	store := audit.NewMemStore()
	// O resolvedor SABE resolver o domínio malicioso — a negação tem de vir da
	// allowlist, não de uma falha de resolução.
	res := staticResolver(map[string][]string{"evil.example.com": {"1.2.3.4"}})
	f := newDNSFilter(t, res, WithDNSSecurityAuditSink(NewWORMSecuritySink(store)))

	principal := principalClass("web-fetcher")
	ips, dec, err := f.Resolve(context.Background(), principal, "evil.example.com")
	if err != nil {
		t.Fatalf("Resolve erro inesperado: %v", err)
	}
	if dec.Allow || ips != nil {
		t.Fatalf("domínio fora da allowlist deveria ser NEGADO, dec=%+v ips=%v", dec, ips)
	}
	if dec.Reason != ReasonDNSNotInList {
		t.Fatalf("razão = %q, quero %q", dec.Reason, ReasonDNSNotInList)
	}
	// Evento selado no WORM, atribuível ao principal e ao NOME consultado.
	part := EgressAuditPartition(principal)
	head, err := store.Head(context.Background(), part)
	if err != nil || head != 1 {
		t.Fatalf("esperava 1 evento selado, head=%d err=%v", head, err)
	}
	if err := audit.Verify(context.Background(), store, part, 1, head); err != nil {
		t.Fatalf("audit.Verify da cadeia DNS: %v", err)
	}
	recs, _ := store.Read(context.Background(), part, 1, 1)
	rec := recs[0]
	if rec.Decision != audit.DecisionDeny {
		t.Fatalf("decisão audit = %q, quero deny", rec.Decision)
	}
	if rec.Obligations[0].Params["dest_host"] != "evil.example.com" {
		t.Fatalf("nome auditado = %q", rec.Obligations[0].Params["dest_host"])
	}
	if rec.Principal.NHIID != "class:web-fetcher" {
		t.Fatalf("atribuição = %q", rec.Principal.NHIID)
	}
	assertNoSecret(t, rec)
}

// TestDNS_Rebinding_Negado cobre ANTI-REBINDING (coerência nome→IP): um nome na
// allowlist que resolve para um IP FORA da allowlist do principal é rejeitado, com o
// IP ofensor selado no evento.
func TestDNS_Rebinding_Negado(t *testing.T) {
	store := audit.NewMemStore()
	// api.github.com É host permitido, mas resolve para um IP fora de 93.184.216.0/24.
	res := staticResolver(map[string][]string{"api.github.com": {"6.6.6.6"}})
	f := newDNSFilter(t, res, WithDNSSecurityAuditSink(NewWORMSecuritySink(store)))

	principal := principalClass("web-fetcher")
	ips, dec, err := f.Resolve(context.Background(), principal, "api.github.com")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec.Allow || ips != nil {
		t.Fatal("rebinding (IP fora da allowlist) deveria ser NEGADO")
	}
	if dec.Reason != ReasonDNSRebinding {
		t.Fatalf("razão = %q, quero %q", dec.Reason, ReasonDNSRebinding)
	}
	recs, _ := store.Read(context.Background(), EgressAuditPartition(principal), 1, 1)
	if len(recs) != 1 || recs[0].Obligations[0].Params["dest_ip"] != "6.6.6.6" {
		t.Fatalf("IP ofensor não selado: %+v", recs)
	}
}

// TestDNS_Rebinding_UmIPMauNegaTudo garante que, com MÚLTIPLOS IPs, um único IP fora
// da allowlist chumba a resolução inteira (coerência estrita).
func TestDNS_Rebinding_UmIPMauNegaTudo(t *testing.T) {
	res := staticResolver(map[string][]string{
		"api.github.com": {"93.184.216.34", "6.6.6.6"}, // um bom, um mau
	})
	f := newDNSFilter(t, res, WithDNSSecurityAuditSink(&recordingSink{}))
	_, dec, _ := f.Resolve(context.Background(), principalClass("web-fetcher"), "api.github.com")
	if dec.Allow || dec.Reason != ReasonDNSRebinding {
		t.Fatalf("um IP fora da allowlist deveria negar tudo, dec=%+v", dec)
	}
}

// TestDNS_Exfil_Entropia_Bloqueado cobre EXFILTRAÇÃO por alta entropia: uma consulta
// com um label de alta entropia (dados encapsulados) é bloqueada mesmo que o domínio
// pai estivesse na allowlist — e nem chega a resolver.
func TestDNS_Exfil_Entropia_Bloqueado(t *testing.T) {
	store := audit.NewMemStore()
	res := staticResolver(map[string][]string{
		"mfrggzdfmztwq2lknnwg23tpobyxe43uov3ho.api.github.com": {"93.184.216.34"},
	})
	f := newDNSFilter(t, res, WithDNSSecurityAuditSink(NewWORMSecuritySink(store)))

	principal := principalClass("web-fetcher")
	name := "mfrggzdfmztwq2lknnwg23tpobyxe43uov3ho.api.github.com" // label base32, H≈4.4
	_, dec, err := f.Resolve(context.Background(), principal, name)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if dec.Allow {
		t.Fatal("consulta de alta entropia deveria ser BLOQUEADA")
	}
	if dec.Reason != ReasonDNSExfilEntropy {
		t.Fatalf("razão = %q, quero %q", dec.Reason, ReasonDNSExfilEntropy)
	}
	// Selado como evento de segurança.
	if head, _ := store.Head(context.Background(), EgressAuditPartition(principal)); head != 1 {
		t.Fatalf("exfil deveria selar evento, head=%d", head)
	}
}

// TestDNS_Exfil_Entropia_Hex_Bloqueado cobre a REGRESSÃO do finding: um payload
// codificado em HEX tem entropia H em [3.5, 4.0) — o tecto teórico do hex é log2(16)=4.0
// e um label hex realista NUNCA o atinge — pelo que o antigo limiar 4.0 deixava passar
// sistematicamente exfil em hex. O default 3.5 deteta-o. Testa 32 e 63 chars (label DNS
// máximo). NEGATIVO por construção: as strings têm distribuições que garantem H<4.0.
func TestDNS_Exfil_Entropia_Hex_Bloqueado(t *testing.T) {
	// hex32: 8 símbolos ×3 + 8 símbolos ×1 = 32 chars ⇒ H≈3.81.
	var b32 strings.Builder
	for i, c := range "0123456789abcdef" {
		n := 1
		if i < 8 {
			n = 3
		}
		b32.WriteString(strings.Repeat(string(c), n))
	}
	// hex63: 15 símbolos ×4 + 1 símbolo ×3 = 63 chars ⇒ H≈3.997 (< 4.0, como o finding).
	var b63 strings.Builder
	for i, c := range "0123456789abcdef" {
		n := 4
		if i == 15 {
			n = 3
		}
		b63.WriteString(strings.Repeat(string(c), n))
	}
	for _, h := range []string{b32.String(), b63.String()} {
		// Prova do finding: o antigo limiar 4.0 NUNCA apanharia este label (H < 4.0).
		if e := shannonBitsPerChar(h); e < 3.5 || e >= 4.0 {
			t.Fatalf("entropia do hex (%d chars) = %v, quero [3.5, 4.0)", len(h), e)
		}
		name := h + ".api.github.com"
		f := newDNSFilter(t, staticResolver(map[string][]string{}), WithDNSSecurityAuditSink(&recordingSink{}))
		if _, dec, _ := f.Resolve(context.Background(), principalClass("web-fetcher"), name); dec.Allow || dec.Reason != ReasonDNSExfilEntropy {
			t.Fatalf("payload hex (%d chars, H=%v) deveria ser detetado como exfil, dec=%+v", len(h), shannonBitsPerChar(h), dec)
		}
	}
}

// TestDNS_Exfil_Entropia_Fragmentada_Bloqueado cobre o ramo anti-FRAGMENTAÇÃO: um
// payload distribuído por vários labels curtos (< MinLabelLen), cada um individualmente
// ignorado pela verificação por-label, é detetado pela entropia da CONCATENAÇÃO dos
// labels de subdomínio (ramo (b) de highEntropy).
func TestDNS_Exfil_Entropia_Fragmentada_Bloqueado(t *testing.T) {
	// Labels de 4 chars (todos < MinLabelLen 20); a concatenação do subdomínio
	// "a1b2c3d4e5f6f7a89b0capi" tem 23 chars e alta entropia.
	name := "a1b2.c3d4.e5f6.f7a8.9b0c.api.github.com"
	// Nenhum label isolado atinge MinLabelLen — a deteção TEM de vir do ramo (b).
	for _, lbl := range strings.Split(name, ".") {
		if len(lbl) >= DefaultExfilConfig().MinLabelLen {
			t.Fatalf("label %q >= MinLabelLen invalida a premissa do teste", lbl)
		}
	}
	f := newDNSFilter(t, staticResolver(map[string][]string{}), WithDNSSecurityAuditSink(&recordingSink{}))
	if _, dec, _ := f.Resolve(context.Background(), principalClass("web-fetcher"), name); dec.Allow || dec.Reason != ReasonDNSExfilEntropy {
		t.Fatalf("payload fragmentado deveria ser detetado como exfil, dec=%+v", dec)
	}
}

// TestDNS_Exfil_Volume_NaoEnvenenadoPorNaoAllowlisted cobre o finding: a contagem de
// volume corre SÓ APÓS o gate de allowlist. Consultas a subdomínios FORA da allowlist
// (negadas NotInList) NÃO envenenam o contador do registeredDomain partilhado com um
// host legítimo — que continua a resolver.
func TestDNS_Exfil_Volume_NaoEnvenenadoPorNaoAllowlisted(t *testing.T) {
	clk := newMutableClock()
	res := staticResolver(map[string][]string{"api.github.com": {"93.184.216.34"}})
	cfg := ExfilConfig{
		Version:              "dns-exfil/test",
		EntropyBitsThreshold: 4.0,
		MinLabelLen:          20,
		MaxQueriesPerWindow:  5,
		Window:               60 * time.Second,
	}
	policies, _ := NewEmbeddedResolver()
	f, err := NewDNSFilter(res, policies, WithDNSSecurityAuditSink(&recordingSink{}), WithExfilConfig(cfg), withDNSClock(clk.now))
	if err != nil {
		t.Fatalf("NewDNSFilter: %v", err)
	}
	principal := principalClass("web-fetcher")
	// 20 consultas a *.github.com FORA da allowlist (host inexacto): negadas NotInList,
	// NÃO contadas (sob o código antigo, envenenariam o contador de github.com).
	for i := 0; i < 20; i++ {
		if _, dec, _ := f.Resolve(context.Background(), principal, "sub"+strconv.Itoa(i)+".github.com"); dec.Reason != ReasonDNSNotInList {
			t.Fatalf("subdomínio fora da allowlist devia ser NotInList, dec=%+v", dec)
		}
	}
	// As 5 consultas legítimas a api.github.com (registeredDomain github.com) continuam
	// a passar — o contador não foi envenenado pelos nomes negados.
	for i := 0; i < 5; i++ {
		if _, dec, _ := f.Resolve(context.Background(), principal, "api.github.com"); !dec.Allow {
			t.Fatalf("consulta legítima %d devia passar (contador não envenenado), dec=%+v", i, dec)
		}
	}
}

// TestExfilDetector_EvictaChavesExpiradas cobre a EVICÇÃO de chaves: o mapa de estado
// não cresce sem teto — chaves cuja janela expirou são removidas numa varredura, em vez
// de ficarem com um slice vazio-mas-presente para sempre.
func TestExfilDetector_EvictaChavesExpiradas(t *testing.T) {
	cfg := DefaultExfilConfig()
	d := newExfilDetector(cfg)
	t0 := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 100; i++ {
		d.record("p", "d"+strconv.Itoa(i)+".com", t0)
	}
	if got := len(d.hits); got != 100 {
		t.Fatalf("esperava 100 chaves antes da evicção, got %d", got)
	}
	// Uma consulta para além da janela dispara a varredura: as 100 chaves expiradas são
	// evictadas, restando só a nova.
	d.record("p", "novo.com", t0.Add(2*cfg.Window))
	if got := len(d.hits); got != 1 {
		t.Fatalf("após evicção esperava 1 chave, got %d", got)
	}
}

// TestDNS_ExfilConfig_Invalido_FailClosed cobre o fail-closed de config: um ExfilConfig
// incompleto (via WithExfilConfig) desligaria silenciosamente a deteção — o construtor
// recusa-o em vez de arrancar com deteção vácua.
func TestDNS_ExfilConfig_Invalido_FailClosed(t *testing.T) {
	res := staticResolver(map[string][]string{})
	policies, _ := NewEmbeddedResolver()
	// zero-value: versão vazia e limiares 0.
	if _, err := NewDNSFilter(res, policies, WithDNSSecurityAuditSink(&recordingSink{}), WithExfilConfig(ExfilConfig{})); err != ErrExfilConfigInvalid {
		t.Fatalf("ExfilConfig zero-value: err=%v, quero ErrExfilConfigInvalid", err)
	}
	// versão presente mas um limiar 0 também é recusado (fail-closed).
	partial := ExfilConfig{Version: "v", EntropyBitsThreshold: 3.5, MinLabelLen: 20, MaxQueriesPerWindow: 0, Window: time.Second}
	if _, err := NewDNSFilter(res, policies, WithDNSSecurityAuditSink(&recordingSink{}), WithExfilConfig(partial)); err != ErrExfilConfigInvalid {
		t.Fatalf("ExfilConfig com limiar 0: err=%v, quero ErrExfilConfigInvalid", err)
	}
	// um config completo é aceite.
	if _, err := NewDNSFilter(res, policies, WithDNSSecurityAuditSink(&recordingSink{}), WithExfilConfig(DefaultExfilConfig())); err != nil {
		t.Fatalf("ExfilConfig completo devia ser aceite: %v", err)
	}
}

// TestDNS_Exfil_Volume_Bloqueado cobre EXFILTRAÇÃO por volume: muitas consultas ao
// mesmo domínio numa janela ultrapassam o limiar e a consulta seguinte é bloqueada.
func TestDNS_Exfil_Volume_Bloqueado(t *testing.T) {
	clk := newMutableClock()
	res := staticResolver(map[string][]string{"api.github.com": {"93.184.216.34"}})
	cfg := ExfilConfig{
		Version:              "dns-exfil/test",
		EntropyBitsThreshold: 4.0,
		MinLabelLen:          20,
		MaxQueriesPerWindow:  5,
		Window:               60 * time.Second,
	}
	policies, _ := NewEmbeddedResolver()
	f, err := NewDNSFilter(res, policies,
		WithDNSSecurityAuditSink(&recordingSink{}),
		WithExfilConfig(cfg),
		withDNSClock(clk.now),
	)
	if err != nil {
		t.Fatalf("NewDNSFilter: %v", err)
	}
	principal := principalClass("web-fetcher")

	// As primeiras 5 consultas (limiar) passam; o label "api" é o subdomínio de
	// api.github.com — baixa entropia. Todas dentro da janela.
	for i := 0; i < 5; i++ {
		if _, dec, _ := f.Resolve(context.Background(), principal, "api.github.com"); !dec.Allow {
			t.Fatalf("consulta %d dentro do limiar deveria passar, reason=%q", i, dec.Reason)
		}
		clk.advance(time.Second)
	}
	// A 6ª (> limiar, mesma janela) é bloqueada por volume.
	if _, dec, _ := f.Resolve(context.Background(), principal, "api.github.com"); dec.Allow || dec.Reason != ReasonDNSExfilVolume {
		t.Fatalf("consulta acima do volume deveria ser bloqueada, dec=%+v", dec)
	}
	// Após a janela expirar, o contador reinicia e volta a permitir (janela desliza).
	clk.advance(2 * time.Minute)
	if _, dec, _ := f.Resolve(context.Background(), principal, "api.github.com"); !dec.Allow {
		t.Fatalf("após a janela expirar deveria voltar a permitir, dec=%+v", dec)
	}
}

// TestDNS_FailClosed_SemPolitica cobre FAIL-CLOSED: sem allowlist resolúvel (resolver
// de políticas devolve nil), a resolução é negada; um erro é igualmente deny.
func TestDNS_FailClosed_SemPolitica(t *testing.T) {
	res := staticResolver(map[string][]string{"api.github.com": {"93.184.216.34"}})
	nilPolicies := ResolverFunc(func(context.Context, referencemonitor.Principal) (*Policy, error) {
		return nil, nil // sem allowlist ⇒ fail-closed
	})
	f, err := NewDNSFilter(res, nilPolicies, WithDNSSecurityAuditSink(&recordingSink{}), withDNSClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewDNSFilter: %v", err)
	}
	if _, dec, _ := f.Resolve(context.Background(), principalClass("web-fetcher"), "api.github.com"); dec.Allow || dec.Reason != ReasonDNSNoPolicy {
		t.Fatalf("sem política a resolução deve ser negada, dec=%+v", dec)
	}

	errPolicies := ResolverFunc(func(context.Context, referencemonitor.Principal) (*Policy, error) {
		return nil, errors.New("pdp indisponivel")
	})
	f2, _ := NewDNSFilter(res, errPolicies, WithDNSSecurityAuditSink(&recordingSink{}), withDNSClock(fixedClock()))
	if _, dec, _ := f2.Resolve(context.Background(), principalClass("web-fetcher"), "api.github.com"); dec.Allow {
		t.Fatal("erro de resolução de política deve negar (fail-closed)")
	}
}

// TestDNS_FailClosed_SemResolucao cobre FAIL-CLOSED do resolvedor CONTROLADO: um nome
// na allowlist que o resolvedor controlado não conhece (NXDOMAIN) é negado — NUNCA se
// cai para o resolver do host/público.
func TestDNS_FailClosed_SemResolucao(t *testing.T) {
	res := staticResolver(map[string][]string{}) // resolvedor controlado vazio
	f := newDNSFilter(t, res, WithDNSSecurityAuditSink(&recordingSink{}))
	// api.github.com É host permitido, mas o resolvedor controlado não o resolve.
	if _, dec, _ := f.Resolve(context.Background(), principalClass("web-fetcher"), "api.github.com"); dec.Allow || dec.Reason != ReasonDNSResolveFailed {
		t.Fatalf("NXDOMAIN no resolvedor controlado deve negar, dec=%+v", dec)
	}
}

// TestDNS_FailClosed_Construtor cobre a recusa fail-closed de construção sem
// resolvedor controlado, sem allowlist ou sem sink WORM.
func TestDNS_FailClosed_Construtor(t *testing.T) {
	res := staticResolver(map[string][]string{})
	policies, _ := NewEmbeddedResolver()
	if _, err := NewDNSFilter(nil, policies, WithDNSSecurityAuditSink(&recordingSink{})); err != ErrNilDNSResolver {
		t.Fatalf("sem resolvedor: err=%v, quero ErrNilDNSResolver", err)
	}
	if _, err := NewDNSFilter(res, nil, WithDNSSecurityAuditSink(&recordingSink{})); err != ErrNilResolver {
		t.Fatalf("sem allowlist: err=%v, quero ErrNilResolver", err)
	}
	if _, err := NewDNSFilter(res, policies); err != ErrNilSink {
		t.Fatalf("sem sink: err=%v, quero ErrNilSink", err)
	}
	if _, err := NewDNSFilter(res, policies, WithDNSSecurityAuditSink(&recordingSink{})); err != nil {
		t.Fatalf("construção válida: %v", err)
	}
}

// TestDNS_EscopoPorPrincipal cobre o ESCOPO por principal reutilizando a allowlist de
// AOS-067: o mesmo nome é resolvido para o principal certo e negado para outro.
func TestDNS_EscopoPorPrincipal(t *testing.T) {
	res := staticResolver(map[string][]string{"payments.internal.aos": {"10.20.0.5"}})
	f := newDNSFilter(t, res, WithDNSSecurityAuditSink(&recordingSink{}))

	// billing: payments.internal.aos é host permitido e 10.20.0.5 ∈ 10.20.0.0/16.
	if _, dec, _ := f.Resolve(context.Background(), principalClass("billing"), "payments.internal.aos"); !dec.Allow {
		t.Fatalf("billing deveria resolver o seu próprio host, reason=%q", dec.Reason)
	}
	// web-fetcher: payments.internal.aos NÃO é host da sua allowlist.
	if _, dec, _ := f.Resolve(context.Background(), principalClass("web-fetcher"), "payments.internal.aos"); dec.Allow || dec.Reason != ReasonDNSNotInList {
		t.Fatalf("web-fetcher NÃO deveria resolver host de billing, dec=%+v", dec)
	}
}

// TestDNS_NomeInvalido cobre o fail-closed de nome malformado.
func TestDNS_NomeInvalido(t *testing.T) {
	f := newDNSFilter(t, staticResolver(map[string][]string{}), WithDNSSecurityAuditSink(&recordingSink{}))
	for _, name := range []string{"", "   ", ".", "a..b"} {
		if _, dec, _ := f.Resolve(context.Background(), principalClass("web-fetcher"), name); dec.Allow || dec.Reason != ReasonDNSInvalidName {
			t.Fatalf("nome inválido %q deveria negar fail-closed, dec=%+v", name, dec)
		}
	}
}

// TestDNS_AuditIndisponivel_FailClosed cobre o fail-closed do AUDIT: se a selagem da
// negação no WORM falhar, a decisão permanece deny e o erro é surfaçado.
func TestDNS_AuditIndisponivel_FailClosed(t *testing.T) {
	failing := &recordingSink{failWith: errors.New("worm offline")}
	res := staticResolver(map[string][]string{"evil.example.com": {"1.2.3.4"}})
	f := newDNSFilter(t, res, WithDNSSecurityAuditSink(failing))
	_, dec, err := f.Resolve(context.Background(), principalClass("web-fetcher"), "evil.example.com")
	if dec.Allow {
		t.Fatal("negação nunca abre, mesmo com audit a falhar")
	}
	if err == nil {
		t.Fatal("falha de selagem da negação deve ser surfaçada")
	}
}

// TestDNS_Span_SemSegredo cobre o DoD de observabilidade: o span DNS transporta a
// decisão (principal, nome, allow/deny, versão) e NENHUM segredo.
func TestDNS_Span_SemSegredo(t *testing.T) {
	tr := newRecordingTracer()
	res := staticResolver(map[string][]string{"evil.example.com": {"1.2.3.4"}})
	f := newDNSFilter(t, res, WithDNSTracer(tr), WithDNSSecurityAuditSink(NewWORMSecuritySink(audit.NewMemStore())))
	_, _, _ = f.Resolve(context.Background(), principalClass("web-fetcher"), "evil.example.com")

	if v, ok := tr.span.get(AttrDNSAllowed); !ok || v != false {
		t.Fatalf("span deve marcar allowed=false, obtive %v (ok=%v)", v, ok)
	}
	if v, ok := tr.span.get(AttrDNSReason); !ok || v != ReasonDNSNotInList {
		t.Fatalf("span deve ter a razão, obtive %v", v)
	}
	if !tr.span.ended {
		t.Fatal("o span deve ser fechado")
	}
	for k, v := range tr.span.attrs {
		if s, ok := v.(string); ok && looksSecret(s) {
			t.Fatalf("atributo de span %q parece segredo: %q", k, s)
		}
	}
}

// TestDNS_EscopoPorNHI cobre o escopo por NHI (não só por classe): um selector nhi:
// da allowlist casa o principal por NHIID.
func TestDNS_EscopoPorNHI(t *testing.T) {
	res := staticResolver(map[string][]string{"api.github.com": {"93.184.216.34"}})
	f := newDNSFilter(t, res, WithDNSSecurityAuditSink(&recordingSink{}))
	if _, dec, _ := f.Resolve(context.Background(), principalNHI("agent-fetcher-01"), "api.github.com"); !dec.Allow {
		t.Fatalf("NHI agent-fetcher-01 deveria resolver, reason=%q", dec.Reason)
	}
}

// TestShannonBitsPerChar cobre a função de entropia (determinista).
func TestShannonBitsPerChar(t *testing.T) {
	if h := shannonBitsPerChar("aaaa"); h != 0 {
		t.Fatalf("entropia de string uniforme = %v, quero 0", h)
	}
	if h := shannonBitsPerChar(""); h != 0 {
		t.Fatalf("entropia de string vazia = %v, quero 0", h)
	}
	// 2 símbolos equiprováveis ⇒ 1 bit/carácter.
	if h := shannonBitsPerChar("abab"); h != 1 {
		t.Fatalf("entropia de 'abab' = %v, quero 1", h)
	}
	// base32-like tem entropia claramente acima do limiar por omissão (4.0).
	if h := shannonBitsPerChar("mfrggzdfmztwq2lknnwg23tpobyxe43u"); h < 4.0 {
		t.Fatalf("entropia base32 = %v, quero >= 4.0", h)
	}
}

// TestRegisteredDomain cobre a agregação de domínio para o contador de volume.
func TestRegisteredDomain(t *testing.T) {
	cases := map[string]string{
		"a1b2.exfil.com":      "exfil.com",
		"x.y.z.exfil.com":     "exfil.com",
		"github.com":          "github.com",
		"localhost":           "localhost",
		"api.internal.aos":    "internal.aos",
		"payments.corp.local": "corp.local",
	}
	for in, want := range cases {
		if got := registeredDomain(in); got != want {
			t.Fatalf("registeredDomain(%q) = %q, quero %q", in, got, want)
		}
	}
}

// TestStaticResolver_Normaliza cobre a normalização de nomes no resolvedor controlado.
func TestStaticResolver_Normaliza(t *testing.T) {
	r := staticResolver(map[string][]string{"API.GitHub.Com.": {"93.184.216.34"}})
	ips, err := r.Resolve(context.Background(), "api.github.com")
	if err != nil || len(ips) != 1 {
		t.Fatalf("resolução case/dot-insensitive falhou: ips=%v err=%v", ips, err)
	}
	if _, err := r.Resolve(context.Background(), "desconhecido.com"); err != ErrNXDOMAIN {
		t.Fatalf("nome desconhecido: err=%v, quero ErrNXDOMAIN", err)
	}
}
