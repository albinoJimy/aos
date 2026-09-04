// Package supplychaintests é a SUITE ADVERSARIAL DE SUPPLY-CHAIN do AOS (AOS-054) —
// o ÚLTIMO ticket do EPIC-05 e o safety net de QA da fronteira de segurança do REG.
//
// É DELIBERADAMENTE um pacote SÓ DE TESTES (ficheiros _test.go): NÃO reimplementa
// nenhum controlo — ORQUESTRA os controlos REAIS já implementados (AOS-045..053) e
// prova, por adversário, que cada vector da tabela de riscos de tecnica/05 §9 é
// REPRODUZIDO e BLOQUEADO. "Uma defesa só existe se for provada por adversário."
//
// Os SETE VECTORES (cada um um teste que reproduz o ataque e prova o bloqueio):
//
//  1. RUG-PULL              — conteúdo re-hasheado sem assinatura legítima → recusa
//     de admissão (AOS-045 gate + AOS-048 assinatura).
//  2. SCHEMA DRIFT          — servidor MCP muta o schema após pinned → changed →
//     bloqueado (AOS-049 TOFU).
//  3. RUG-PULL A MEIO DO RUN — definição em backing store diverge do congelado →
//     revalidação por chamada bloqueia + quarentena
//     (AOS-050 congelamento + AOS-051 revalidação).
//  4. TOOL POISONING        — descrição MCP com instrução injectada permanece
//     UNTRUSTED e não comanda o planeador (ADR-005; barreira
//     AOS-042 reutilizada por AOS-046).
//  5. RESOLUÇÃO POR LATEST   — referência flutuante REJEITADA (AOS-047/045).
//  6. CAPACIDADE FORA DO CATÁLOGO — recusada por DEFAULT-DENY (ADR-002; AOS-045).
//  7. REPLAY INFIEL         — o manifesto de dependências por trajectória reproduz
//     o passado apesar da evolução posterior de tool
//     (ADR-012; AOS-052).
//
// AUDIT WORM: cada BLOQUEIO é atestado na hash-chain WORM tamper-evident (AOS-011) e
// re-verificado com [audit.Verify] + os campos do registo. Para os vectores cujos
// controlos SELAM nativamente uma decisão (rug-pull → registry.admission; schema
// drift → registry.tofu; rug-pull mid-run → registry.revalidation) a suite verifica
// ADICIONALMENTE o registo NATIVO produzido pelo controlo. Os restantes (barreira
// estrutural / rejeição de query / reconstrução por manifesto) são atestados no
// LEDGER de supply-chain da própria suite — o rasto tamper-evident da corrida
// adversarial.
//
// META-TESTES (prova de detecção, não green-vazio): para cada vector há um
// TestMetaDetects_* que reproduz o MESMO ataque com o controlo CONTORNADO e prova que
// o ataque PASSA (não é bloqueado). Se a asserção de bloqueio do vector fosse vácua
// (sempre verdadeira), o meta-teste — que assere o NÃO-bloqueio com o controlo
// desligado — falharia. Juntos provam que a suite discrimina genuinamente. O
// self-test scripts/ci/selftest.sh (secção F) corre ainda um teste-VENENO
// (TestSelftestSupplychainBypassReddensGate) que prova que um vector desbloqueado
// torna o gate scripts/ci/supplychain.sh VERMELHO (fail-closed).
//
// Cross-ref: esta suite é o harness adversarial de supply-chain registado em
// specs/EPIC-11_Testes_Qualidade.md (nota AOS-054/EPIC-05) e reproduz a tabela de
// riscos de tecnica/05 §9 — os dois specs onde o cross-ref do DoD de AOS-054 vive.
//
// Determinismo: relógios/chaves/ids são deterministas (sem time.Now/rand numa
// decisão); as fixtures são serializadas de forma estável e NÃO contêm segredos —
// as chaves privadas são derivadas de seeds constantes de teste, nunca material real.
package supplychaintests

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/signing"
	"github.com/aos-ref/platform/registry/toolset"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Relógio / chaves / versões deterministas
// ---------------------------------------------------------------------------

// fixedClock devolve um relógio determinista. NUNCA é usado numa decisão de
// segurança (as decisões dos controlos são puras); só carimba timestamps
// observacionais de audit, para que a cadeia WORM seja reproduzível.
func fixedClock() func() time.Time {
	base := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	return func() time.Time { return base }
}

// ver constrói uma versão SemVer pinada com campos nomeados.
func ver(mj, mn, p int) domain.Version {
	return domain.Version{Major: mj, Minor: mn, Patch: p}
}

// keyFromSeed produz um par Ed25519 DETERMINÍSTICO a partir de um byte de seed.
// É material de TESTE derivado de uma constante — nunca uma chave real (sem segredos).
func keyFromSeed(b byte) ed25519.PrivateKey {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = b
	}
	return ed25519.NewKeyFromSeed(seed)
}

// Publicadores de teste: um legítimo (confiável) e um atacante (nunca confiável).
const (
	keyLegit    = "pub:acme"
	keyAttacker = "pub:evil"
	seedLegit   = byte(7)
	seedAttack  = byte(66)
)

// sha256Digester é o digester criptográfico de AOS-047 — o MESMO que a suite injecta
// no registo e no revalidador para que digest e assinatura sejam coerentes.
var sha256Digester = digest.SHA256Digester{}

// newSigner constrói um assinante determinista (AOS-048).
func newSigner(t *testing.T, keyID string, seed byte) *signing.Signer {
	t.Helper()
	s, err := signing.NewSigner(keyID, keyFromSeed(seed))
	if err != nil {
		t.Fatalf("NewSigner(%s): %v", keyID, err)
	}
	return s
}

// newTrust constrói um TrustStore real (AOS-048) com as chaves públicas dos signers.
func newTrust(t *testing.T, signers ...*signing.Signer) *signing.TrustStore {
	t.Helper()
	ts, err := signing.NewTrustStore(audit.NewMemStore(), signing.WithTrustClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewTrustStore: %v", err)
	}
	for _, s := range signers {
		if err := ts.Add(context.Background(), s.KeyID(), s.PublicKey()); err != nil {
			t.Fatalf("TrustStore.Add(%s): %v", s.KeyID(), err)
		}
	}
	return ts
}

// ---------------------------------------------------------------------------
// Contratos / entradas deterministas (sem segredos)
// ---------------------------------------------------------------------------

// contractWith constrói um contrato de teste com um marcador no InputSchema (para
// forçar digests distintos entre variantes íntegra/mutada), scopes e classe de egress.
func contractWith(marker string, egress domain.EgressClass, scopes ...string) domain.Contract {
	in, _ := json.Marshal(map[string]string{"marker": marker})
	return domain.Contract{
		InputSchema:      json.RawMessage(in),
		CredentialScopes: scopes,
		Egress:           egress,
	}
}

// mcpServerContract constrói o contrato de uma entrada kind=mcp_server no molde de
// AOS-320: a classe de egress MAIS o digest do manifesto de capacidades ANCORADO na
// referência local (transporte/endpoint), que é o que mcp.Host.stage passa a gravar.
//
// O valor é um digest OPACO — a sua composição exacta (superfície anunciada + âncora
// não-forjável) é provada em registry/mcp/manifesto_test.go. Aqui interessa a
// propriedade que o REG consome: manifestos ou endpoints diferentes produzem
// ManifestDigest diferentes, logo entradas com digests diferentes.
//
// contractLegadoMCPServer (abaixo) é a forma PRÉ-AOS-320 — só a classe de egress —,
// usada como CONTROLO para mostrar que ela colidia.
func mcpServerContract(t *testing.T, endpoint, manifesto string, egress domain.EgressClass) domain.Contract {
	t.Helper()
	doc, err := json.Marshal(map[string]string{"endpoint": endpoint, "manifest": manifesto})
	if err != nil {
		t.Fatalf("marshal da forma ancorada: %v", err)
	}
	dig, err := digest.DigestJSON(doc)
	if err != nil {
		t.Fatalf("DigestJSON da forma ancorada: %v", err)
	}
	return domain.Contract{Egress: egress, ManifestDigest: dig}
}

// contractLegadoMCPServer é o contrato que o REG gravava para um mcp_server ANTES de
// AOS-320: SÓ a classe de egress. Existe para o controlo negativo dos vectores.
func contractLegadoMCPServer(egress domain.EgressClass) domain.Contract {
	return domain.Contract{Egress: egress}
}

// signedEntry constrói uma domain.Entry COERENTE: digest = SHA-256 real do
// (kind, contract) via AOS-047, e assinatura do signer sobre (id, version, digest)
// via AOS-048. É o artefacto "íntegro" contra o qual o congelado casa.
func signedEntry(id string, v domain.Version, kind domain.ArtifactKind, c domain.Contract, signer *signing.Signer) domain.Entry {
	dig := sha256Digester.Digest(kind, c)
	sig := signer.Sign(id, v, dig)
	return domain.Entry{
		ID: id, Version: v, Kind: kind, Digest: dig, Signature: sig,
		Contract:   c,
		Provenance: domain.Provenance{Origin: "mcp://" + id, Publisher: signer.KeyID(), Trust: domain.TrustPinned},
		Status:     domain.StatusActive,
	}
}

// ---------------------------------------------------------------------------
// Registry de teste + catálogo fake para congelamento
// ---------------------------------------------------------------------------

// newRegistry constrói um Registry sobre um Event Store real (fonte de verdade,
// ADR-007) com o digester SHA-256 e relógio determinista. As opções extra permitem
// injectar o gate de admissão de assinatura (AOS-045+048).
func newRegistry(t *testing.T, opts ...registry.Option) *registry.Registry {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	all := append([]registry.Option{
		registry.WithClock(fixedClock()),
		registry.WithDigester(sha256Digester),
	}, opts...)
	reg, err := registry.New(store, all...)
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	return reg
}

// probeEventStore constrói um Event Store real (para os probes puros do relatório,
// sem *testing.T). Reutiliza o mesmo backend de verdade que newRegistry.
func probeEventStore() (eventstore.EventStore, error) {
	return eventstore.New()
}

// fakeCatalog implementa toolset.Catalog: devolve o conjunto dado como "active".
type fakeCatalog struct{ entries []domain.Entry }

func (f fakeCatalog) ActiveEntries(context.Context) ([]domain.Entry, error) {
	out := make([]domain.Entry, len(f.entries))
	copy(out, f.entries)
	return out, nil
}

// ---------------------------------------------------------------------------
// Leitura da hash-chain WORM
// ---------------------------------------------------------------------------

// auditRecords lê TODOS os registos selados numa partição, por ordem, e falha o
// teste se a leitura falhar. Um head 0 (partição vazia) devolve nil.
func auditRecords(t *testing.T, store audit.Store, partition string) []audit.AuditRecord {
	t.Helper()
	head, err := store.Head(context.Background(), partition)
	if err != nil {
		t.Fatalf("audit.Head(%s): %v", partition, err)
	}
	if head == 0 {
		return nil
	}
	recs, err := store.Read(context.Background(), partition, 1, head)
	if err != nil {
		t.Fatalf("audit.Read(%s): %v", partition, err)
	}
	return recs
}

// verifyWORM corre [audit.Verify] sobre TODA a partição (génese→head) e devolve os
// registos. Falha o teste se a cadeia não estiver íntegra — é a prova tamper-evident
// de que o rasto do bloqueio não foi adulterado. Uma partição vazia é um erro (um
// bloqueio TEM de deixar rasto).
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
	return auditRecords(t, store, partition)
}

// findDeny devolve o PRIMEIRO registo de decisão deny cujo ToolID coincide, ou falha.
// É como a suite localiza o registo ESPERADO de um bloqueio na cadeia WORM.
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

// ---------------------------------------------------------------------------
// Ledger de atestação de supply-chain (WORM da própria suite)
// ---------------------------------------------------------------------------

// scLedgerPartition é a partição WORM onde a suite adversarial atesta CADA bloqueio
// provado. É o rasto tamper-evident uniforme da corrida (complementa os registos
// NATIVOS que os controlos que selam já produzem).
const scLedgerPartition = "supplychain.attestations"

// attestBlock sela na hash-chain WORM (AOS-011) a atestação de UM bloqueio provado:
// decisão deny, a capability = nome do vector, o artefacto (ToolID) e a razão
// (Resource.Value). NÃO reimplementa nenhum controlo — grava o rasto tamper-evident
// da suite através do MESMO audit.Store real. Falha fail-closed se o append falhar
// (um bloqueio não-atestável não conta como provado).
func attestBlock(t *testing.T, store audit.Store, vector, toolID, reason string) {
	t.Helper()
	rec := audit.AuditRecord{
		Partition:  scLedgerPartition,
		Timestamp:  fixedClock()(),
		Decision:   audit.DecisionDeny,
		Capability: "supplychain.block:" + vector,
		ToolID:     toolID,
		Resource:   audit.Resource{Type: "supplychain.reason", Value: reason},
		Context:    audit.CallContext{Taint: "untrusted"},
		RunID:      scLedgerPartition,
		StepID:     vector + ":" + toolID,
	}
	if _, err := store.Append(context.Background(), rec); err != nil {
		t.Fatalf("attestBlock(%s): append WORM falhou: %v", vector, err)
	}
}

// frozenDigest devolve o digest ESPERADO (congelado) de uma tool no conjunto, ou
// falha. É o discriminador contra o qual a revalidação por chamada compara.
func frozenDigest(t *testing.T, frozen *toolset.FrozenToolSet, id string) string {
	t.Helper()
	dig, ok := frozen.ExpectedDigest(id)
	if !ok {
		t.Fatalf("tool %q ausente do conjunto congelado", id)
	}
	return dig
}

// ---------------------------------------------------------------------------
// Recorders de quarentena / alerta (AOS-051): implementam as portas reais
// revalidation.Quarantiner / revalidation.Alerter para asserção.
// ---------------------------------------------------------------------------

type recordingQuarantine struct {
	mu   sync.Mutex
	arts []revalidation.Artifact
}

func (q *recordingQuarantine) Quarantine(_ context.Context, art revalidation.Artifact) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.arts = append(q.arts, art)
	return nil
}

func (q *recordingQuarantine) count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.arts)
}

func (q *recordingQuarantine) last() (revalidation.Artifact, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.arts) == 0 {
		return revalidation.Artifact{}, false
	}
	return q.arts[len(q.arts)-1], true
}

type recordingAlerter struct {
	mu     sync.Mutex
	alerts []revalidation.Alert
}

func (a *recordingAlerter) Alert(_ context.Context, al revalidation.Alert) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.alerts = append(a.alerts, al)
}

func (a *recordingAlerter) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.alerts)
}

// ---------------------------------------------------------------------------
// Guarda de determinismo do tracer (partilhada; sem segredos nos spans)
// ---------------------------------------------------------------------------

// noSecretTracer é um agentruntime.Tracer NoopTracer explícito para deixar claro que
// a suite não depende de observabilidade para as suas asserções (o veredicto vem dos
// controlos, não dos spans).
var _ agentruntime.Tracer = agentruntime.NoopTracer{}
