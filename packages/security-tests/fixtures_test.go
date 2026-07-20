package securitytests

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"path"
	"strings"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/sandbox/network"
)

// ===========================================================================
// Corpus versionado e extensível (testdata/corpus.json)
// ===========================================================================

//go:embed testdata/corpus.json
var corpusJSON []byte

// corpus é o corpo de payloads adversariais VERSIONADO. A harness é table-driven:
// acrescentar um vector é acrescentar uma entrada ao JSON, sem reescrever a harness.
type corpus struct {
	Version          string                  `json:"version"`
	PromptInjections []promptInjectionVector `json:"prompt_injections"`
	ExfilEgress      []egressVector          `json:"exfil_egress"`
	ExfilDNS         []dnsVector             `json:"exfil_dns"`
}

// promptInjectionVector é uma injecção de conteúdo untrusted (tool result / web /
// memória / mcp / model output). O que importa para o TaintGate é a ORIGEM (que
// classifica untrusted), não o conteúdo — o payload prova que a defesa é estrutural.
type promptInjectionVector struct {
	ID           string `json:"id"`
	Origin       string `json:"origin"`   // tool_result | web | derived_memory | mcp_schema | model_output
	Encoding     string `json:"encoding"` // plain | base64 | homoglyph
	Payload      string `json:"payload"`
	AssertMarker string `json:"assert_marker"` // substring esperado no payload efectivo ("" = skip)
}

// egressVector é uma tentativa de exfiltração por egress de rede.
type egressVector struct {
	ID           string `json:"id"`
	ResourceType string `json:"resource_type"` // url | net | file (mislabelado)
	Target       string `json:"target"`
	Capability   string `json:"capability"`
	Kind         string `json:"kind"`
}

// dnsVector é uma tentativa de exfiltração por DNS (fora da allowlist ou tunneling).
type dnsVector struct {
	ID    string `json:"id"`
	QName string `json:"qname"`
	Kind  string `json:"kind"`
}

// loadCorpus desserializa o corpus embebido. T-free (usável nos probes do relatório).
func loadCorpus() (*corpus, error) {
	var c corpus
	if err := json.Unmarshal(corpusJSON, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// mustCorpus carrega o corpus ou falha o teste.
func mustCorpus(t *testing.T) *corpus {
	t.Helper()
	c, err := loadCorpus()
	if err != nil {
		t.Fatalf("loadCorpus: %v", err)
	}
	return c
}

// effectivePayload decodifica o payload conforme a codificação, revelando a instrução/
// alvo EFECTIVO que a ofuscação esconde: base64 → texto; symlink → o caminho CANÓNICO
// (deref de symlinks sintéticos + colapso lexical de travessia .,..); plain/homoglyph →
// tal e qual. Um vector com codificação desconhecida é um erro (fail-closed: uma
// má-entrada de corpus não passa despercebida).
func effectivePayload(v promptInjectionVector) (string, error) {
	switch v.Encoding {
	case "plain", "homoglyph":
		return v.Payload, nil
	case "base64":
		raw, err := base64.StdEncoding.DecodeString(v.Payload)
		if err != nil {
			return "", fmt.Errorf("base64 invalido em %q: %w", v.ID, err)
		}
		return string(raw), nil
	case "symlink":
		return resolveObfuscatedPath(v.Payload), nil
	default:
		return "", fmt.Errorf("codificacao desconhecida %q em %q", v.Encoding, v.ID)
	}
}

// testSymlinks é uma tabela SINTÉTICA de symlinks (determinista, offline — NUNCA toca no
// filesystem real; sem alvos/segredos reais). Modela um link cujo destino é um directório
// sensível, para exercitar a ofuscação por symlink sem I/O.
var testSymlinks = map[string]string{
	"/srv/exports": "/etc", // /srv/exports é um symlink para /etc
}

// resolveObfuscatedPath de-ofusca um caminho: dereferencia symlinks sintéticos (por
// prefixo mais-longo, [testSymlinks]) e colapsa a travessia lexical (. e ..) com
// [path.Clean], revelando o ALVO CANÓNICO que a ofuscação symlink/path-traversal esconde
// — o análogo, para caminhos, do decode base64. É puro, determinista e limitado (sem
// ciclos de symlink). Um caminho já-canónico é ponto-fixo (idempotente).
func resolveObfuscatedPath(p string) string {
	const maxHops = 16
	for range make([]struct{}, maxHops) {
		cleaned := path.Clean(p)
		// Deref do symlink de PREFIXO MAIS-LONGO (determinista, independente da ordem
		// de iteração do map).
		bestSrc, bestDst := "", ""
		for src, dst := range testSymlinks {
			if cleaned != src && !strings.HasPrefix(cleaned, src+"/") {
				continue
			}
			if len(src) > len(bestSrc) {
				bestSrc, bestDst = src, dst
			}
		}
		if bestSrc == "" {
			return cleaned // ponto-fixo: sem mais symlinks a resolver
		}
		p = bestDst + cleaned[len(bestSrc):]
	}
	return path.Clean(p)
}

// ===========================================================================
// Relógio determinista + scan de sentinela
// ===========================================================================

// fixedClock devolve um relógio determinista. NUNCA é usado numa decisão de
// segurança (as decisões dos controlos são puras); só carimba timestamps
// observacionais de audit, para que a cadeia WORM seja reproduzível.
func fixedClock() func() time.Time {
	base := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	return func() time.Time { return base }
}

// scanLeak é o predicado de fuga de segredo: verdadeiro se o sentinela aparecer no
// texto dado, VERBATIM ou por uma transformação de exfiltração comum. É a base do
// cenário SEGREDOS (AOS-070) e a sua não-vacuidade é provada por um meta-teste que lhe
// entrega vazamentos sintéticos (plain E codificados/fragmentados).
//
// O adversário do próprio cenário (padrão CamoLeak) NÃO exfiltra o segredo em claro —
// codifica-o (base64/hex) ou fragmenta-o (chunking por delimitadores/espaços) para
// escapar a um `Contains` verbatim. Um predicado só-substring seria mais fraco do que o
// ataque que diz cobrir. Este scan fecha essa lacuna cobrindo, sobre CADA superfície
// observável:
//
//   - o sentinela VERBATIM;
//   - as codificações comuns do sentinela — base64 (std/url, com e sem padding) e hex;
//   - o sentinela FRAGMENTADO por espaços em branco (reassembla e reavalia verbatim +
//     codificado, sem corromper alfabetos base64/hex);
//   - o sentinela FRAGMENTADO por delimitadores estruturais (colapsa a mesma classe de
//     delimitadores no palheiro E na agulha e compara — apanha o secret partido por
//     aspas/vírgulas/hífens/etc.).
//
// Fail-closed no espírito: na dúvida entre variantes, qualquer casamento conta como fuga.
func scanLeak(where, s, sentinel string) bool {
	if sentinel == "" {
		return false
	}
	// (1) Verbatim.
	if strings.Contains(s, sentinel) {
		return true
	}
	// (2) Palheiro com espaços em branco removidos: apanha exfil fragmentada por
	// espaços/quebras de linha SEM corromper alfabetos base64/hex (que não os contêm).
	ws := stripWhitespace(s)
	if strings.Contains(ws, sentinel) {
		return true
	}
	// (3) Codificações comuns do sentinela (base64 std/url ± padding, hex), no palheiro
	// bruto E no colapsado por espaços (o base64/hex pode ter sido partido por espaços).
	for _, n := range encodedNeedles(sentinel) {
		if strings.Contains(s, n) || strings.Contains(ws, n) {
			return true
		}
	}
	// (4) Fragmentação por delimitadores estruturais: colapsa a MESMA classe de
	// delimitadores em ambos os lados e compara (o sentinela contém '-', logo tem de ser
	// colapsado também). Apanha o secret partido por aspas/vírgulas/hífens/pontos/etc.
	if strings.Contains(collapseDelims(s), collapseDelims(sentinel)) {
		return true
	}
	return false
}

// encodedNeedles devolve as codificações comuns do sentinela que um broker malicioso
// usaria para exfiltrar sem casar um `Contains` verbatim (padrão CamoLeak): base64
// (std/url, com e sem padding) e hex. Todas são strings longas e específicas do
// sentinela — o risco de falso-positivo numa superfície legítima é desprezável.
func encodedNeedles(sentinel string) []string {
	raw := []byte(sentinel)
	return []string{
		base64.StdEncoding.EncodeToString(raw),
		base64.URLEncoding.EncodeToString(raw),
		base64.RawStdEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw),
		hex.EncodeToString(raw),
	}
}

// stripWhitespace remove todo o espaço em branco (incl. quebras de linha e tabs). Não
// toca nos alfabetos base64/hex (que não contêm espaços), pelo que reassembla uma
// exfiltração fragmentada por espaços sem corromper uma codificação.
func stripWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' {
			return -1
		}
		return r
	}, s)
}

// leakDelims é a classe de delimitadores estruturais que uma exfiltração fragmentada
// usaria para partir o segredo em claro (espaços, pontuação, aspas, hífens...). Colapsar
// esta classe reassembla o segredo verbatim. NÃO se usa nos caminhos base64/hex (esses
// alfabetos incluem alguns destes caracteres) — só na comparação verbatim colapsada.
const leakDelims = " \t\n\r\v\f-_.,:;/\\|\"'`()[]{}<>=*+~"

// collapseDelims remove a classe [leakDelims] de uma string. Aplicado a AMBOS o palheiro
// e a agulha, torna a comparação insensível a fragmentação por delimitadores.
func collapseDelims(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(leakDelims, r) {
			return -1
		}
		return r
	}, s)
}

// ===========================================================================
// Ledger de atestação da suite (WORM tamper-evident) — AOS-072
// ===========================================================================

// suiteLedgerPartition é a partição WORM onde a suite adversarial atesta CADA bloqueio
// provado, de forma uniforme. É o rasto tamper-evident da corrida (complementa os
// registos NATIVOS que os controlos que selam já produzem — egress/DNS).
const suiteLedgerPartition = "security.attestations"

// attestBlock sela na hash-chain WORM (AOS-072) a atestação de UM bloqueio provado:
// decisão deny, a capability = nome do cenário, o sujeito (ToolID) e a razão
// (Resource.Value). NÃO reimplementa nenhum controlo — grava o rasto tamper-evident da
// suite através do MESMO audit.Store real. Fail-closed se o append falhar (um bloqueio
// não-atestável não conta como provado).
func attestBlock(t *testing.T, store audit.Store, scenario, subject, reason string) {
	t.Helper()
	rec := audit.AuditRecord{
		Partition:  suiteLedgerPartition,
		Timestamp:  fixedClock()(),
		Decision:   audit.DecisionDeny,
		Capability: "security.block:" + scenario,
		ToolID:     subject,
		Resource:   audit.Resource{Type: "security.reason", Value: reason},
		Context:    audit.CallContext{Taint: "untrusted"},
		RunID:      suiteLedgerPartition,
		StepID:     scenario + ":" + subject,
	}
	if _, err := store.Append(context.Background(), rec); err != nil {
		t.Fatalf("attestBlock(%s): append WORM falhou: %v", scenario, err)
	}
}

// verifyWORM corre [audit.Verify] sobre TODA a partição (génese→head) e devolve os
// registos. Falha o teste se a cadeia não estiver íntegra — prova tamper-evident de que
// o rasto do bloqueio não foi adulterado. Uma partição vazia é um erro (um bloqueio TEM
// de deixar rasto).
func verifyWORM(t *testing.T, store audit.Store, partition string) []audit.AuditRecord {
	t.Helper()
	head, err := store.Head(context.Background(), partition)
	if err != nil {
		t.Fatalf("audit.Head(%s): %v", partition, err)
	}
	if head == 0 {
		t.Fatalf("partição de audit %q vazia: um bloqueio tem de deixar rasto WORM", partition)
	}
	if err := audit.Verify(context.Background(), store, partition, 1, head); err != nil {
		t.Fatalf("audit.Verify(%s, 1, %d) = %v, quer cadeia íntegra", partition, head, err)
	}
	recs, err := store.Read(context.Background(), partition, 1, head)
	if err != nil {
		t.Fatalf("audit.Read(%s): %v", partition, err)
	}
	return recs
}

// findDeny devolve o PRIMEIRO registo deny cujo ToolID coincide, ou falha.
func findDeny(t *testing.T, recs []audit.AuditRecord, toolID string) audit.AuditRecord {
	t.Helper()
	for _, r := range recs {
		if r.Decision == audit.DecisionDeny && r.ToolID == toolID {
			return r
		}
	}
	t.Fatalf("nenhum registo deny para ToolID=%q na cadeia WORM (%d registos)", toolID, len(recs))
	return audit.AuditRecord{}
}

// ===========================================================================
// Fábricas dos controlos REAIS (t-free, reutilizadas pelos probes do relatório)
// ===========================================================================

// egressPrincipal é o principal de teste cuja classe (web-fetcher) casa a regra
// r-web-fetcher da allowlist embebida (egress_policy.json): permite api.github.com:443
// e o CIDR 93.184.216.0/24. Qualquer outro destino é default-deny.
func egressPrincipal() referencemonitor.Principal {
	return referencemonitor.Principal{
		NHIID:      "agent-fetcher-01",
		AgentID:    "agent-1",
		AgentClass: "web-fetcher",
	}
}

// egressToolID é o id sob o qual a tool de rede é registada no RM.
const egressToolID = "sandbox.network.egress"

// buildEgressRM constrói um Reference Monitor cuja cadeia de mediação insere o
// EgressHook REAL (AOS-067) no slot de egress, selando os bloqueios no audit WORM
// tamper-evident (AOS-072) via WORMSecuritySink sobre o store dado. É o RM que APLICA
// a decisão — a execução permanece mediada (ADR-002). Uma tool benigna é registada
// para o caminho de allow não ser tautológico.
func buildEgressRM(store audit.Store, es eventstore.EventStore) (*referencemonitor.Monitor, error) {
	resolver, err := network.NewEmbeddedResolver()
	if err != nil {
		return nil, err
	}
	sink := network.NewWORMSecuritySink(store)
	filter, err := network.NewEgressFilter(resolver, network.WithSecurityAuditSink(sink))
	if err != nil {
		return nil, err
	}
	hook, err := network.NewEgressHook(filter)
	if err != nil {
		return nil, err
	}
	hooks := []referencemonitor.Hook{
		referencemonitor.IdentityStub{},
		referencemonitor.PolicyStub{},
		referencemonitor.BudgetStub{},
		hook, // egress REAL (substitui o EgressStub neutro)
		referencemonitor.AuditStub{},
	}
	rm := referencemonitor.New(
		referencemonitor.WithHooks(hooks...),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)),
	)
	if err := rm.Register(egressToolID, func(_ context.Context, _ []byte) ([]byte, error) {
		return []byte("ok"), nil // tool benigna: só é despachada num permit
	}); err != nil {
		return nil, err
	}
	return rm, nil
}

// egressCall monta um referencemonitor.Call para um vector de egress do corpus.
func egressCall(v egressVector) referencemonitor.Call {
	return referencemonitor.Call{
		RunID:      "run-exfil",
		StepID:     "step-" + v.ID,
		ToolID:     egressToolID,
		Capability: v.Capability,
		Resource:   referencemonitor.Resource{Type: v.ResourceType, Value: v.Target},
		Principal:  egressPrincipal(),
		Credential: "tok-test",
	}
}

// buildDNSFilter constrói o DNSFilter REAL (AOS-068) sobre um resolvedor controlado
// (StaticResolver) e a allowlist embebida, selando bloqueios no audit WORM. O resolver
// resolve api.github.com para um IP dentro do CIDR permitido (para o caminho de allow
// não ser tautológico); domínios de exfiltração nem chegam a resolver.
func buildDNSFilter(store audit.Store) (*network.DNSFilter, error) {
	policies, err := network.NewEmbeddedResolver()
	if err != nil {
		return nil, err
	}
	resolver := network.NewStaticResolver(map[string][]net.IP{
		"api.github.com": {net.ParseIP("93.184.216.34")},
	})
	sink := network.NewWORMSecuritySink(store)
	return network.NewDNSFilter(resolver, policies, network.WithDNSSecurityAuditSink(sink))
}
