package dsar_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	audit "github.com/aos-ref/platform/audit"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
	redaction "github.com/aos-ref/substrate/redaction"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
)

// ---------------------------------------------------------------------------
// Harness sintético — compõe o audit (AOS-083) e a redação (AOS-091) por baixo
// do fluxo DSAR (AOS-093). Toda a PII usada nos testes é SINTÉTICA.
// ---------------------------------------------------------------------------

// synthPII é PII sintética (nunca real) para provar a irrecuperabilidade.
const (
	synthPII     = "Titular Sintetico, email syn@example.test"
	synthEmail   = "syn@example.test"
	synthSubject = "subject-synthetic-1"
)

// detRand devolve uma fonte determinística (contador) — reprodutibilidade em teste,
// NUNCA em produção. Serve o vault do audit e o KeySource da redação (mesma assinatura).
func detRand() func(p []byte) error {
	var n byte
	return func(p []byte) error {
		for i := range p {
			p[i] = n
			n++
		}
		return nil
	}
}

// tokenizePolicy tokeniza email/phone/iban (reversível ⇒ shreddable) e remove o resto.
func tokenizePolicy(t *testing.T) redaction.Policy {
	t.Helper()
	pol, err := redaction.NewPolicy("v1", map[redaction.Class]redaction.Action{
		redaction.ClassEmail:      redaction.ActionTokenize,
		redaction.ClassPhone:      redaction.ActionTokenize,
		redaction.ClassIBAN:       redaction.ActionTokenize,
		redaction.ClassCreditCard: redaction.ActionRemove,
		redaction.ClassIPv4:       redaction.ActionRemove,
	})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	return pol
}

// harness reúne as peças montadas para um teste.
type harness struct {
	ctx      context.Context
	store    *audit.MemStore
	pipe     *audit.IngestPipeline
	vault    *audit.InMemoryKeyVault
	holds    *audit.LegalHold
	index    *audit.InMemorySubjectPartitionIndex
	shredder *audit.Shredder
	ks       *redaction.InMemoryKeySource
	engine   *redaction.Engine
	policy   redaction.Policy
	flow     *dsar.Flow
	tracer   *otelgenai.RecordingTracer
}

// newHarness monta audit + redação + fluxo DSAR determinísticos.
func newHarness(t *testing.T, opts ...dsar.Option) *harness {
	t.Helper()
	store := audit.NewMemStore()
	vault := audit.NewInMemoryKeyVault(detRand())
	payloads := audit.NewInMemoryPayloadStore()
	index := audit.NewInMemorySubjectPartitionIndex()
	pipe := audit.NewIngestPipeline(store, vault, payloads,
		audit.WithIngestRand(detRand()), audit.WithIngestSubjectIndex(index))

	holds := audit.NewLegalHold()
	shredder := audit.NewShredder(vault, holds, audit.NewRetentionPolicy(nil),
		audit.WithShredderSubjectIndex(index))

	ks := redaction.NewInMemoryKeySource(detRand())
	engine := redaction.NewEngine(ks)
	policy := tokenizePolicy(t)

	tracer := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})

	stores := []dsar.ShreddableKeyStore{
		dsar.AuditStore("audit", shredder),
		dsar.RedactionStore("redaction", ks, shredder),
	}
	allOpts := append([]dsar.Option{dsar.WithTracer(tracer)}, opts...)
	flow := dsar.NewFlow(pipe, shredder, stores, allOpts...)

	return &harness{
		ctx: context.Background(), store: store, pipe: pipe, vault: vault,
		holds: holds, index: index, shredder: shredder, ks: ks, engine: engine,
		policy: policy, flow: flow, tracer: tracer,
	}
}

// seedAudit ingere um registo de PII no audit e devolve o PayloadRef selado.
func (h *harness) seedAudit(t *testing.T, partition, subject string, pii []byte) audit.PayloadRef {
	t.Helper()
	sealed, err := h.pipe.Ingest(h.ctx, audit.RawRecord{
		Record:    audit.AuditRecord{Partition: partition, Decision: audit.DecisionAllow, Capability: "tool.call"},
		SubjectID: subject,
		PII:       pii,
	})
	if err != nil {
		t.Fatalf("seedAudit Ingest: %v", err)
	}
	if sealed.PayloadRef == nil {
		t.Fatal("seedAudit: PayloadRef nil")
	}
	return *sealed.PayloadRef
}

// seedRedaction tokeniza uma PII na redação e devolve o token + KeyRef.
func (h *harness) seedRedaction(t *testing.T, subject, text string) (token, keyRef string) {
	t.Helper()
	_, refs, err := h.engine.RedactText(text, subject, h.policy)
	if err != nil {
		t.Fatalf("seedRedaction RedactText: %v", err)
	}
	if len(refs) == 0 {
		t.Fatalf("seedRedaction: nenhum token gerado para %q", text)
	}
	return refs[0].Token, refs[0].KeyRef
}

// ---------------------------------------------------------------------------
// AC2 + AC3 + testes de irrecuperabilidade e integridade.
// ---------------------------------------------------------------------------

// TestErasureIrrecoverableAcrossStoresKeepsChain — o cenário ponta-a-ponta: PII
// cifrada no audit E tokenizada na redação, recuperável ANTES; o DSAR destrói a
// chave em AMBOS os stores; DEPOIS a decifração falha deterministicamente nos dois;
// e a cadeia de audit (dos dados do titular E dos eventos DSAR) verifica ANTES e DEPOIS.
func TestErasureIrrecoverableAcrossStoresKeepsChain(t *testing.T) {
	h := newHarness(t)

	// Semear PII nos dois stores.
	ref := h.seedAudit(t, "run-1", synthSubject, []byte(synthPII))
	token, keyRef := h.seedRedaction(t, synthSubject, "contacto "+synthEmail)

	// Capturar o material de chave ANTES do shred — para depois provar que NUNCA
	// aparece em nenhum evento DSAR (isolamento do segredo).
	auditKey, ok := h.vault.Key(ref.KeyRef)
	if !ok {
		t.Fatal("pré-condição: chave de audit devia existir")
	}
	redKey, ok := h.ks.KeyByRef(keyRef)
	if !ok {
		t.Fatal("pré-condição: chave de redação devia existir")
	}

	// ANTES: ambos recuperáveis; cadeia íntegra.
	if _, err := h.pipe.Recover(ref); err != nil {
		t.Fatalf("audit PII devia ser recuperável pré-shred: %v", err)
	}
	if got, ok := redaction.Resolve(token, redKey); !ok || got != synthEmail {
		t.Fatalf("redação devia resolver pré-shred: got=%q ok=%v", got, ok)
	}
	if err := audit.Verify(h.ctx, h.store, "run-1", 1, 1); err != nil {
		t.Fatalf("cadeia dos dados devia verificar pré-shred: %v", err)
	}

	// DSAR.
	res, err := h.flow.Receive(h.ctx, dsar.Request{RequestID: "dsar-req-1", SubjectID: synthSubject})
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if res.Blocked {
		t.Fatal("apagamento não devia ser bloqueado sem legal hold")
	}
	if len(res.StoresShredded) != 2 {
		t.Fatalf("esperava 2 stores shredded, veio %v", res.StoresShredded)
	}

	// DEPOIS: irrecuperável em AMBOS (determinístico).
	if _, err := h.pipe.Recover(ref); !errors.Is(err, audit.ErrShredded) {
		t.Fatalf("audit PII devia ser irrecuperável (ErrShredded), veio %v", err)
	}
	if _, ok := h.ks.KeyByRef(keyRef); ok {
		t.Fatal("chave de redação devia ter sido destruída")
	}
	// A chave destruída já não está no vault: mesmo o caminho de resolução não a obtém.
	if _, ok := h.vault.Key(ref.KeyRef); ok {
		t.Fatal("chave de audit devia ter sido destruída")
	}

	// AC3: a cadeia dos dados do titular permanece ÍNTEGRA (o DSAR não a reescreve).
	if err := audit.Verify(h.ctx, h.store, "run-1", 1, 1); err != nil {
		t.Fatalf("cadeia dos dados devia permanecer íntegra pós-shred: %v", err)
	}
	// E a cadeia dos próprios eventos DSAR verifica (received + key_destroyed).
	head, _ := h.store.Head(h.ctx, "governance.dsar")
	if head != 2 {
		t.Fatalf("esperava 2 eventos DSAR selados, head=%d", head)
	}
	if err := audit.Verify(h.ctx, h.store, "governance.dsar", 1, head); err != nil {
		t.Fatalf("cadeia dos eventos DSAR devia verificar: %v", err)
	}

	// Isolamento do segredo: nenhum evento DSAR contém as chaves destruídas.
	assertNoSecretInPartition(t, h, "governance.dsar", [][]byte{auditKey, redKey}, []string{synthPII})
}

// ---------------------------------------------------------------------------
// AC5 — legal hold bloqueia (subject e partição), fail-closed.
// ---------------------------------------------------------------------------

// TestLegalHoldSubjectBlocksErasure — titular sob hold por-titular: o DSAR é
// bloqueado (dsar.blocked), NENHUMA chave é destruída, a PII continua recuperável.
func TestLegalHoldSubjectBlocksErasure(t *testing.T) {
	h := newHarness(t)
	ref := h.seedAudit(t, "run-h", synthSubject, []byte(synthPII))
	token, keyRef := h.seedRedaction(t, synthSubject, "contacto "+synthEmail)
	h.holds.HoldSubject(synthSubject)

	res, err := h.flow.Receive(h.ctx, dsar.Request{RequestID: "dsar-hold-1", SubjectID: synthSubject})
	if !errors.Is(err, dsar.ErrLegalHold) {
		t.Fatalf("DSAR sob legal hold devia dar ErrLegalHold, veio %v", err)
	}
	if !res.Blocked {
		t.Fatal("Result.Blocked devia ser true sob legal hold")
	}
	if len(res.StoresShredded) != 0 {
		t.Fatalf("nenhum store devia ter sido shredded sob hold, veio %v", res.StoresShredded)
	}
	if res.Partial {
		t.Fatal("bloqueio no gate inicial não destrói nada: Partial devia ser false")
	}

	// Chaves PRESERVADAS: ambos continuam recuperáveis/resolúveis.
	if _, err := h.pipe.Recover(ref); err != nil {
		t.Fatalf("audit PII devia continuar recuperável sob hold: %v", err)
	}
	redKey, ok := h.ks.KeyByRef(keyRef)
	if !ok {
		t.Fatal("chave de redação devia continuar presente sob hold")
	}
	if got, ok := redaction.Resolve(token, redKey); !ok || got != synthEmail {
		t.Fatalf("redação devia continuar a resolver sob hold: got=%q ok=%v", got, ok)
	}

	// Eventos: received + blocked (SEM key_destroyed).
	assertEventVerbs(t, h, "governance.dsar", []string{dsar.EventReceived, dsar.EventBlocked})
}

// TestLegalHoldPartitionBlocksErasure — titular SEM hold por-titular mas com dados
// numa partição sob hold: o índice titular→partição faz valer o hold (fail-closed).
func TestLegalHoldPartitionBlocksErasure(t *testing.T) {
	h := newHarness(t)
	// A ingestão liga synthSubject→"board-42" no índice partilhado.
	ref := h.seedAudit(t, "board-42", synthSubject, []byte(synthPII))
	h.holds.HoldPartition("board-42") // NB: sem hold por-titular.

	res, err := h.flow.Receive(h.ctx, dsar.Request{RequestID: "dsar-part-1", SubjectID: synthSubject})
	if !errors.Is(err, dsar.ErrLegalHold) {
		t.Fatalf("DSAR sob hold por-partição devia dar ErrLegalHold, veio %v", err)
	}
	if !res.Blocked {
		t.Fatal("Result.Blocked devia ser true sob hold por-partição")
	}
	if _, err := h.pipe.Recover(ref); err != nil {
		t.Fatalf("audit PII devia continuar recuperável sob hold por-partição: %v", err)
	}

	// Levantado o hold, o DSAR procede e a PII torna-se irrecuperável.
	h.holds.ReleasePartition("board-42")
	res2, err := h.flow.Receive(h.ctx, dsar.Request{RequestID: "dsar-part-2", SubjectID: synthSubject})
	if err != nil {
		t.Fatalf("Receive após levantar hold: %v", err)
	}
	if res2.Blocked {
		t.Fatal("apagamento não devia ser bloqueado após levantar o hold")
	}
	if _, err := h.pipe.Recover(ref); !errors.Is(err, audit.ErrShredded) {
		t.Fatalf("audit PII devia ser irrecuperável após shred, veio %v", err)
	}
}

// ---------------------------------------------------------------------------
// AC4 + AC6 — auditabilidade, metadados preservados, sem PII nos eventos.
// ---------------------------------------------------------------------------

// TestEventsAreAuditableWithoutPII — o fluxo gera dsar.received e dsar.key_destroyed
// selados na hash-chain; nenhum evento contém a PII sintética; os metadados
// (quem/o quê: ToolID, subjectID pseudónimo, verbo) são preservados como facto.
func TestEventsAreAuditableWithoutPII(t *testing.T) {
	h := newHarness(t)
	h.seedAudit(t, "run-x", synthSubject, []byte(synthPII))

	if _, err := h.flow.Receive(h.ctx, dsar.Request{RequestID: "dsar-audit-1", SubjectID: synthSubject}); err != nil {
		t.Fatalf("Receive: %v", err)
	}

	recs := readPartition(t, h, "governance.dsar")
	assertEventVerbs(t, h, "governance.dsar", []string{dsar.EventReceived, dsar.EventKeyDestroyed})

	// Metadados de conformidade preservados no dsar.received.
	received := recs[0]
	if received.Capability != dsar.EventReceived {
		t.Fatalf("received.Capability=%q", received.Capability)
	}
	if received.Decision != audit.DecisionAllow {
		t.Fatalf("received.Decision=%q", received.Decision)
	}
	if received.ToolID != "gov.dsar" {
		t.Fatalf("received.ToolID=%q, esperado gov.dsar", received.ToolID)
	}
	if received.Resource.Value != synthSubject {
		t.Fatalf("received deve rotular o subjectID pseudónimo, veio %q", received.Resource.Value)
	}
	if received.RequestID != "dsar-audit-1" {
		t.Fatalf("received.RequestID=%q", received.RequestID)
	}
	if received.PayloadRef != nil {
		t.Fatal("evento DSAR NÃO deve ter PayloadRef (é só metadados, sem PII)")
	}

	// key_destroyed enumera os stores destruídos (rótulos, sem PII).
	destroyed := recs[1]
	if destroyed.Capability != dsar.EventKeyDestroyed {
		t.Fatalf("destroyed.Capability=%q", destroyed.Capability)
	}
	if len(destroyed.Obligations) != 1 || destroyed.Obligations[0].Type != "dsar.stores" {
		t.Fatalf("key_destroyed devia enumerar os stores, veio %+v", destroyed.Obligations)
	}
	gotStores := destroyed.Obligations[0].Fields
	if len(gotStores) != 2 {
		t.Fatalf("esperava 2 rótulos de store, veio %v", gotStores)
	}

	// Nenhum evento contém a PII sintética.
	assertNoSecretInPartition(t, h, "governance.dsar", nil, []string{synthPII, synthEmail})

	// AC4: os metadados PERSISTEM mesmo após o shred (a cadeia não é reescrita).
	if err := audit.Verify(h.ctx, h.store, "governance.dsar", 1, 2); err != nil {
		t.Fatalf("cadeia de eventos devia verificar (metadados preservados): %v", err)
	}
}

// TestSecretNeverExposed — a chave por-titular NUNCA é devolvida pelo fluxo nem
// aparece em nenhum evento/span. O Result não tem campo de chave (garantia
// estrutural); aqui provamos que o material de chave não fuga para o audit nem para
// os spans.
func TestSecretNeverExposed(t *testing.T) {
	h := newHarness(t)
	ref := h.seedAudit(t, "run-s", synthSubject, []byte(synthPII))
	_, keyRef := h.seedRedaction(t, synthSubject, "contacto "+synthEmail)

	auditKey, _ := h.vault.Key(ref.KeyRef)
	redKey, _ := h.ks.KeyByRef(keyRef)

	if _, err := h.flow.Receive(h.ctx, dsar.Request{RequestID: "dsar-sec-1", SubjectID: synthSubject}); err != nil {
		t.Fatalf("Receive: %v", err)
	}

	// Nem no audit...
	assertNoSecretInPartition(t, h, "governance.dsar", [][]byte{auditKey, redKey}, nil)

	// ...nem nos spans.
	spans := h.tracer.SpansByOperation("gov.dsar.erase")
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span gov.dsar.erase, veio %d", len(spans))
	}
	blob := fmt.Sprintf("%+v", spans[0].Attributes)
	for _, k := range [][]byte{auditKey, redKey} {
		if strings.Contains(blob, hex.EncodeToString(k)) {
			t.Fatal("o span NÃO deve conter material de chave (ADR-006)")
		}
	}
	if strings.Contains(blob, synthPII) {
		t.Fatal("o span NÃO deve conter PII")
	}
	// O span carrega o subjectID pseudónimo (permitido) e o desfecho.
	if !strings.Contains(blob, synthSubject) {
		t.Fatalf("o span devia carregar o subjectID; attrs=%v", spans[0].Attributes)
	}
}

// ---------------------------------------------------------------------------
// Idempotência + fronteiras.
// ---------------------------------------------------------------------------

// TestReErasureIsIdempotent — re-submeter um DSAR de um titular já apagado não
// falha nem re-destrói indevidamente; os factos de conformidade voltam a ser selados.
func TestReErasureIsIdempotent(t *testing.T) {
	h := newHarness(t)
	ref := h.seedAudit(t, "run-i", synthSubject, []byte(synthPII))

	if _, err := h.flow.Receive(h.ctx, dsar.Request{RequestID: "dsar-i-1", SubjectID: synthSubject}); err != nil {
		t.Fatalf("Receive #1: %v", err)
	}
	if _, err := h.pipe.Recover(ref); !errors.Is(err, audit.ErrShredded) {
		t.Fatalf("PII devia estar irrecuperável após #1, veio %v", err)
	}

	// Segundo DSAR do mesmo titular: no-op de destruição, mas auditável e sem erro.
	res2, err := h.flow.Receive(h.ctx, dsar.Request{RequestID: "dsar-i-2", SubjectID: synthSubject})
	if err != nil {
		t.Fatalf("Receive #2 (idempotente) devia proceder sem erro, veio %v", err)
	}
	if res2.Blocked {
		t.Fatal("re-DSAR não devia ser bloqueado")
	}
	if len(res2.StoresShredded) != 2 {
		t.Fatalf("re-DSAR devia voltar a passar por ambos os stores (no-op), veio %v", res2.StoresShredded)
	}
	if _, err := h.pipe.Recover(ref); !errors.Is(err, audit.ErrShredded) {
		t.Fatalf("PII devia permanecer irrecuperável após #2, veio %v", err)
	}

	// 4 eventos selados (received+key_destroyed x2), cadeia íntegra.
	head, _ := h.store.Head(h.ctx, "governance.dsar")
	if head != 4 {
		t.Fatalf("esperava 4 eventos DSAR, head=%d", head)
	}
	if err := audit.Verify(h.ctx, h.store, "governance.dsar", 1, 4); err != nil {
		t.Fatalf("cadeia de eventos devia verificar após re-DSAR: %v", err)
	}
}

// TestReceiveEmptySubjectFailsClosed — DSAR sem subjectID é recusado antes de
// qualquer selagem (fail-closed).
func TestReceiveEmptySubjectFailsClosed(t *testing.T) {
	h := newHarness(t)
	if _, err := h.flow.Receive(h.ctx, dsar.Request{RequestID: "x", SubjectID: "   "}); !errors.Is(err, dsar.ErrNoSubject) {
		t.Fatalf("subject vazio devia dar ErrNoSubject, veio %v", err)
	}
	if head, _ := h.store.Head(h.ctx, "governance.dsar"); head != 0 {
		t.Fatalf("nenhum evento devia ter sido selado, head=%d", head)
	}
}

// refusingStore recusa sempre o shred, para exercitar o caminho fail-closed do fluxo.
type refusingStore struct {
	name string
	err  error
}

func (r refusingStore) Name() string       { return r.name }
func (r refusingStore) Shred(string) error { return r.err }

// TestStoreRefusalFailsClosed — se um store recusa o shred, o fluxo sela
// dsar.blocked e ABORTA sem selar key_destroyed (erasure incompleta não é declarada
// satisfeita). Um erro audit.ErrLegalHold do store mapeia para dsar.ErrLegalHold.
func TestStoreRefusalFailsClosed(t *testing.T) {
	store := audit.NewMemStore()
	vault := audit.NewInMemoryKeyVault(detRand())
	payloads := audit.NewInMemoryPayloadStore()
	pipe := audit.NewIngestPipeline(store, vault, payloads, audit.WithIngestRand(detRand()))
	holds := audit.NewLegalHold()
	shredder := audit.NewShredder(vault, holds, audit.NewRetentionPolicy(nil))

	sentinel := errors.New("store indisponivel")
	stores := []dsar.ShreddableKeyStore{
		dsar.AuditStore("audit", shredder),
		refusingStore{name: "broken", err: sentinel},
	}
	flow := dsar.NewFlow(pipe, shredder, stores)

	ctx := context.Background()
	res, err := flow.Receive(ctx, dsar.Request{RequestID: "dsar-refuse", SubjectID: synthSubject})
	if !errors.Is(err, sentinel) {
		t.Fatalf("recusa do store devia propagar o erro, veio %v", err)
	}
	if !res.Blocked {
		t.Fatal("Result.Blocked devia ser true quando um store recusa")
	}
	// O audit foi destruído antes de 'broken' recusar ⇒ erasure parcial sinalizada.
	if !res.Partial || len(res.StoresShredded) != 1 || res.StoresShredded[0] != "audit" {
		t.Fatalf("erasure parcial devia constar (Partial + StoresShredded=[audit]), veio Partial=%v stores=%v", res.Partial, res.StoresShredded)
	}
	// received + blocked, SEM key_destroyed.
	head, _ := store.Head(ctx, "governance.dsar")
	if head != 2 {
		t.Fatalf("esperava received+blocked (2 eventos), head=%d", head)
	}
	recs, _ := store.Read(ctx, "governance.dsar", 1, 2)
	if recs[1].Capability != dsar.EventBlocked {
		t.Fatalf("segundo evento devia ser dsar.blocked, veio %q", recs[1].Capability)
	}
	if err := audit.Verify(ctx, store, "governance.dsar", 1, 2); err != nil {
		t.Fatalf("cadeia devia verificar: %v", err)
	}
}

// TestStoreLegalHoldMapsToErrLegalHold — quando o store shreddable (audit) recusa
// por legal hold re-checado, o fluxo devolve dsar.ErrLegalHold (não o erro cru).
func TestStoreLegalHoldMapsToErrLegalHold(t *testing.T) {
	// Oráculo de hold que reporta "não retido", mas o store audit está sob hold: força
	// a divergência para exercitar o mapeamento do erro do store.
	store := audit.NewMemStore()
	vault := audit.NewInMemoryKeyVault(detRand())
	payloads := audit.NewInMemoryPayloadStore()
	pipe := audit.NewIngestPipeline(store, vault, payloads, audit.WithIngestRand(detRand()))
	if _, _, err := vault.EnsureKey(synthSubject); err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	holds := audit.NewLegalHold()
	holds.HoldSubject(synthSubject)
	shredder := audit.NewShredder(vault, holds, audit.NewRetentionPolicy(nil))

	// HoldOracle que mente (nunca retido) — o store é a última linha fail-closed.
	flow := dsar.NewFlow(pipe, neverHeld{}, []dsar.ShreddableKeyStore{dsar.AuditStore("audit", shredder)})

	ctx := context.Background()
	res, err := flow.Receive(ctx, dsar.Request{RequestID: "dsar-map", SubjectID: synthSubject})
	if !errors.Is(err, dsar.ErrLegalHold) {
		t.Fatalf("recusa por legal hold no store devia mapear para dsar.ErrLegalHold, veio %v", err)
	}
	if !res.Blocked {
		t.Fatal("Result.Blocked devia ser true")
	}
	if _, ok := vault.Key(audit.KeyRefFor(synthSubject)); !ok {
		t.Fatal("a chave NÃO devia ter sido destruída (fail-closed no store)")
	}
}

// neverHeld é um HoldOracle que reporta sempre "não retido".
type neverHeld struct{}

func (neverHeld) Held(string) bool { return false }

// flipHold é um HoldOracle mutável: começa "não retido" e passa a "retido" quando
// activado — simula um legal hold colocado CONCORRENTEMENTE a meio da erasure
// unificada (a janela TOCTOU hold-vs-DSAR).
type flipHold struct{ held bool }

func (f *flipHold) Held(string) bool { return f.held }

// flippingStore activa o hold partilhado ao ser shredado — simula o hold a chegar
// depois deste store mas ANTES do próximo ser alcançado.
type flippingStore struct {
	name string
	hold *flipHold
}

func (s flippingStore) Name() string { return s.name }
func (s flippingStore) Shred(string) error {
	s.hold.held = true // hold colocado imediatamente após este shred
	return nil
}

// TestLegalHoldMidErasureBlocksBeforeNonEnforcingStore — REGRESSÃO (achado MEDIUM
// AOS-093): a enforcement do legal hold é AUTORITATIVA por-store, não dependente da
// ordem de wiring. Com um store NÃO-enforcing (redaction real) ordenado ANTES do
// re-check do audit, um hold que apareça a meio da erasure é apanhado pelo re-check
// do fluxo ANTES de tocar na redação — a KEK de tokenização SOBREVIVE. Sem o
// re-check por-store, a redação teria sido shredada sob hold activo.
func TestLegalHoldMidErasureBlocksBeforeNonEnforcingStore(t *testing.T) {
	store := audit.NewMemStore()
	vault := audit.NewInMemoryKeyVault(detRand())
	payloads := audit.NewInMemoryPayloadStore()
	pipe := audit.NewIngestPipeline(store, vault, payloads, audit.WithIngestRand(detRand()))

	// Redaction real (AOS-091): semear um token do titular para provar que sobrevive.
	ks := redaction.NewInMemoryKeySource(detRand())
	engine := redaction.NewEngine(ks)
	policy := tokenizePolicy(t)
	_, refs, err := engine.RedactText("contacto "+synthEmail, synthSubject, policy)
	if err != nil {
		t.Fatalf("RedactText: %v", err)
	}
	token, keyRef := refs[0].Token, refs[0].KeyRef
	redKeyBefore, ok := ks.KeyByRef(keyRef)
	if !ok {
		t.Fatal("pré-condição: chave de redação devia existir")
	}

	hold := &flipHold{}
	// Ordem deliberada: um store que activa o hold ao shredar, SEGUIDO da redação. O hold
	// aparece entre os dois, e o fluxo re-consulta antes de cada store.
	//
	// A redação DEIXOU de ser não-enforcing (2026-08-22): passa a consultar o hold e a tomar a
	// barreira, como o store de audit. Este teste continua a provar o mesmo — que o fluxo bloqueia
	// ANTES de lá chegar — e deixou de depender de o segundo store nao se defender sozinho.
	stores := []dsar.ShreddableKeyStore{
		flippingStore{name: "trigger", hold: hold},
		dsar.RedactionStore("redaction", ks, hold),
	}
	flow := dsar.NewFlow(pipe, hold, stores)

	ctx := context.Background()
	res, err := flow.Receive(ctx, dsar.Request{RequestID: "dsar-toctou", SubjectID: synthSubject})
	if !errors.Is(err, dsar.ErrLegalHold) {
		t.Fatalf("hold a meio da erasure devia bloquear com ErrLegalHold, veio %v", err)
	}
	if !res.Blocked {
		t.Fatal("Result.Blocked devia ser true")
	}
	// A redação NUNCA foi alcançada: o hold foi re-checado antes do seu shred.
	if got := res.StoresShredded; len(got) != 1 || got[0] != "trigger" {
		t.Fatalf("só o store 'trigger' devia constar como destruído, veio %v", got)
	}
	// Erasure PARCIAL e irreversível (o 'trigger' já foi destruído) — sinalizada.
	if !res.Partial {
		t.Fatal("Result.Partial devia sinalizar a erasure parcial irreversível")
	}
	// A KEK de tokenização da redação SOBREVIVE ao hold (o ponto do achado).
	redKeyAfter, ok := ks.KeyByRef(keyRef)
	if !ok {
		t.Fatal("a chave de redação NÃO devia ter sido destruída sob hold (achado MEDIUM)")
	}
	if !bytes.Equal(redKeyBefore, redKeyAfter) {
		t.Fatal("a chave de redação devia manter-se intacta")
	}
	if got, ok := redaction.Resolve(token, redKeyAfter); !ok || got != synthEmail {
		t.Fatalf("o token da redação devia continuar resolúvel: got=%q ok=%v", got, ok)
	}

	// received + blocked selados; sem key_destroyed.
	recs, _ := store.Read(ctx, "governance.dsar", 1, 2)
	if len(recs) != 2 || recs[1].Capability != dsar.EventBlocked {
		t.Fatalf("esperava received+blocked, veio %+v", recs)
	}
	if err := audit.Verify(ctx, store, "governance.dsar", 1, 2); err != nil {
		t.Fatalf("cadeia devia verificar: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Opções e defaults.
// ---------------------------------------------------------------------------

// TestClockAndPartitionOptions — WithClock timestampa os eventos e WithPartition
// (e o override por-pedido) direccionam a partição de audit dos eventos DSAR.
func TestClockAndPartitionOptions(t *testing.T) {
	store := audit.NewMemStore()
	vault := audit.NewInMemoryKeyVault(detRand())
	payloads := audit.NewInMemoryPayloadStore()
	pipe := audit.NewIngestPipeline(store, vault, payloads, audit.WithIngestRand(detRand()))
	shredder := audit.NewShredder(vault, audit.NewLegalHold(), audit.NewRetentionPolicy(nil))

	fixed := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	flow := dsar.NewFlow(pipe, shredder, []dsar.ShreddableKeyStore{dsar.AuditStore("audit", shredder)},
		dsar.WithClock(func() time.Time { return fixed }),
		dsar.WithPartition("custom.dsar"))

	ctx := context.Background()
	// Sem override: usa a partição da opção.
	if _, err := flow.Receive(ctx, dsar.Request{RequestID: "opt-1", SubjectID: synthSubject}); err != nil {
		t.Fatalf("Receive: %v", err)
	}
	recs, _ := store.Read(ctx, "custom.dsar", 1, 2)
	if len(recs) != 2 {
		t.Fatalf("esperava 2 eventos em custom.dsar, veio %d", len(recs))
	}
	if !recs[0].Timestamp.Equal(fixed) {
		t.Fatalf("timestamp do evento=%v, esperado %v (relógio injectado)", recs[0].Timestamp, fixed)
	}

	// Override por-pedido: os eventos vão para a partição do Request.
	if _, err := flow.Receive(ctx, dsar.Request{RequestID: "opt-2", SubjectID: "outro", Partition: "per-req.dsar"}); err != nil {
		t.Fatalf("Receive override: %v", err)
	}
	if head, _ := store.Head(ctx, "per-req.dsar"); head != 2 {
		t.Fatalf("esperava 2 eventos em per-req.dsar, head=%d", head)
	}
}

// TestNilHoldsWithExplicitNoHoldProceeds — a renúncia à preservação exige um
// [dsar.NoHold]{} EXPLÍCITO (opt-in auditável): com ele o apagamento procede. (Um
// holds nil, ao invés, é recusado fail-closed — ver TestNilHoldOracleFailsClosed.)
func TestNilHoldsWithExplicitNoHoldProceeds(t *testing.T) {
	store := audit.NewMemStore()
	vault := audit.NewInMemoryKeyVault(detRand())
	payloads := audit.NewInMemoryPayloadStore()
	pipe := audit.NewIngestPipeline(store, vault, payloads, audit.WithIngestRand(detRand()))
	if _, _, err := vault.EnsureKey(synthSubject); err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	shredder := audit.NewShredder(vault, audit.NewLegalHold(), audit.NewRetentionPolicy(nil))

	flow := dsar.NewFlow(pipe, dsar.NoHold{}, []dsar.ShreddableKeyStore{dsar.AuditStore("audit", shredder)})
	res, err := flow.Receive(context.Background(), dsar.Request{RequestID: "nohold", SubjectID: synthSubject})
	if err != nil {
		t.Fatalf("Receive com NoHold{} explicito: %v", err)
	}
	if res.Blocked {
		t.Fatal("NoHold{} devia proceder (não bloqueado)")
	}
	if _, ok := vault.Key(audit.KeyRefFor(synthSubject)); ok {
		t.Fatal("a chave devia ter sido destruída")
	}
}

// TestReceiveWithoutSealerFailsClosed — sem EventSealer o apagamento não é
// auditável: fail-closed com ErrNoSealer, sem tocar nas chaves.
func TestReceiveWithoutSealerFailsClosed(t *testing.T) {
	vault := audit.NewInMemoryKeyVault(detRand())
	if _, _, err := vault.EnsureKey(synthSubject); err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	shredder := audit.NewShredder(vault, audit.NewLegalHold(), audit.NewRetentionPolicy(nil))
	flow := dsar.NewFlow(nil, shredder, []dsar.ShreddableKeyStore{dsar.AuditStore("audit", shredder)})

	if _, err := flow.Receive(context.Background(), dsar.Request{RequestID: "x", SubjectID: synthSubject}); !errors.Is(err, dsar.ErrNoSealer) {
		t.Fatalf("sem sealer devia dar ErrNoSealer, veio %v", err)
	}
	if _, ok := vault.Key(audit.KeyRefFor(synthSubject)); !ok {
		t.Fatal("a chave NÃO devia ter sido destruída (fail-closed antes de shredar)")
	}
}

// ---------------------------------------------------------------------------
// Helpers de asserção.
// ---------------------------------------------------------------------------

func readPartition(t *testing.T, h *harness, partition string) []audit.AuditRecord {
	t.Helper()
	head, err := h.store.Head(h.ctx, partition)
	if err != nil {
		t.Fatalf("Head(%q): %v", partition, err)
	}
	recs, err := h.store.Read(h.ctx, partition, 1, head)
	if err != nil {
		t.Fatalf("Read(%q): %v", partition, err)
	}
	return recs
}

// assertEventVerbs confirma a sequência exacta de verbos (Capability) selados.
func assertEventVerbs(t *testing.T, h *harness, partition string, want []string) {
	t.Helper()
	recs := readPartition(t, h, partition)
	if len(recs) != len(want) {
		t.Fatalf("partição %q: esperava %d eventos %v, veio %d", partition, len(want), want, len(recs))
	}
	for i, w := range want {
		if recs[i].Capability != w {
			t.Fatalf("evento #%d = %q, esperado %q", i, recs[i].Capability, w)
		}
	}
}

// assertNoSecretInPartition varre todos os registos da partição e falha se algum
// contiver material de chave (hex) ou PII em claro. Varre a representação completa
// do registo (todos os campos selados).
func assertNoSecretInPartition(t *testing.T, h *harness, partition string, keys [][]byte, plaintexts []string) {
	t.Helper()
	for _, rec := range readPartition(t, h, partition) {
		blob := fmt.Sprintf("%+v", rec)
		for _, k := range keys {
			if len(k) == 0 {
				continue
			}
			if strings.Contains(blob, hex.EncodeToString(k)) {
				t.Fatalf("evento %q contém material de chave (ADR-006)", rec.Capability)
			}
			if bytes.Contains([]byte(blob), k) {
				t.Fatalf("evento %q contém bytes de chave", rec.Capability)
			}
		}
		for _, pt := range plaintexts {
			if pt != "" && strings.Contains(blob, pt) {
				t.Fatalf("evento %q contém PII em claro %q", rec.Capability, pt)
			}
		}
	}
}

// TestNilHoldOracleFailsClosed: um fluxo construido com holds=nil recusa o
// apagamento fail-closed (ErrNoHoldOracle) em vez de fail-open silencioso; a
// renuncia a preservacao exige um NoHold{} explicito.
func TestNilHoldOracleFailsClosed(t *testing.T) {
	store := audit.NewMemStore()
	vault := audit.NewInMemoryKeyVault(detRand())
	payloads := audit.NewInMemoryPayloadStore()
	pipe := audit.NewIngestPipeline(store, vault, payloads, audit.WithIngestRand(detRand()))
	holds := audit.NewLegalHold()
	shredder := audit.NewShredder(vault, holds, audit.NewRetentionPolicy(nil))
	stores := []dsar.ShreddableKeyStore{dsar.AuditStore("audit", shredder)}
	ctx := context.Background()

	flowNil := dsar.NewFlow(pipe, nil, stores)
	if _, err := flowNil.Receive(ctx, dsar.Request{SubjectID: synthSubject, RequestID: "r-nil"}); !errors.Is(err, dsar.ErrNoHoldOracle) {
		t.Fatalf("holds=nil devia recusar fail-closed com ErrNoHoldOracle, veio %v", err)
	}
	flowNoHold := dsar.NewFlow(pipe, dsar.NoHold{}, stores)
	if _, err := flowNoHold.Receive(ctx, dsar.Request{SubjectID: synthSubject, RequestID: "r-nohold"}); err != nil {
		t.Fatalf("NoHold{} explicito devia permitir o apagamento, veio %v", err)
	}
}
