package compliance

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

const testPartition = "run-1"

// fixedClock devolve um relógio determinista.
func fixedClock() func() time.Time {
	ts := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return ts }
}

func mustAppend(t *testing.T, store audit.Store, rec audit.AuditRecord) audit.AuditRecord {
	t.Helper()
	rec.Partition = testPartition
	sealed, err := store.Append(context.Background(), rec)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	return sealed
}

// seedCleanAudit sela um audit CONHECIDO numa só partição (cadeia contígua) com uma
// mistura determinista de acções, sovereignty, HITL, DSAR, policy.changed e
// retention — TODAS as acções com principal completo até um humano. Devolve o store e
// o último audit_seq.
func seedCleanAudit(t *testing.T) (*audit.MemStore, uint64) {
	t.Helper()
	store := audit.NewMemStore()

	aliceChain := []audit.DelegationHop{{Sub: "human:alice", ActAs: "agentA"}}
	bobChain := []audit.DelegationHop{{Sub: "human:bob", ActAs: "agentB"}}

	// seq1: acção permit (alice→agentA).
	mustAppend(t, store, audit.AuditRecord{
		Decision: audit.DecisionAllow, Capability: "http:get", ToolID: "tool.http",
		Principal: audit.Principal{NHIID: "agentA", DelegationChain: aliceChain},
		Resource:  audit.Resource{Type: "url", Value: "https://api.example.com/orders"},
	})
	// seq2: acção permit COM obrigação region (sovereignty permit, alice→agentA).
	mustAppend(t, store, audit.AuditRecord{
		Decision: audit.DecisionAllow, Capability: "http:get", ToolID: "tool.http",
		Principal:   audit.Principal{NHIID: "agentA", DelegationChain: aliceChain},
		Resource:    audit.Resource{Type: "url", Value: "https://api.eu.example.com/x", Region: "eu"},
		Obligations: []audit.Obligation{{Type: obRegion, Params: map[string]string{paramRegion: "eu"}}},
	})
	// seq3: acção deny com Resource.Region (sovereignty deny, bob→agentB).
	mustAppend(t, store, audit.AuditRecord{
		Decision: audit.DecisionDeny, Capability: "fs:write", ToolID: "tool.fs",
		Principal: audit.Principal{NHIID: "agentB", DelegationChain: bobChain},
		Resource:  audit.Resource{Type: "file", Value: "/data/x", Region: "us"},
	})
	// seq4: acção escalate (alice→agentA).
	mustAppend(t, store, audit.AuditRecord{
		Decision: audit.DecisionEscalate, Capability: "payment:send", ToolID: "tool.pay",
		Principal: audit.Principal{NHIID: "agentA", DelegationChain: aliceChain},
		Resource:  audit.Resource{Type: "url", Value: "https://pay.example.com"},
	})
	// seq5: HITL aprovado (aprovador autenticado).
	mustAppend(t, store, audit.AuditRecord{
		Decision: audit.DecisionAllow, Capability: "payment:send", ToolID: toolHITL,
		Principal: audit.Principal{NHIID: "human:carol"},
		Resource:  audit.Resource{Type: "action", Value: "pay-approve"},
		Obligations: []audit.Obligation{
			{Type: obHITLDecision, Params: map[string]string{paramHITLReason: "aprovado"}},
			{Type: obHITLSignature, Params: map[string]string{paramHITLApprover: "human:carol"}},
		},
	})
	// seq6: HITL negado e NÃO autenticado (quarentena).
	mustAppend(t, store, audit.AuditRecord{
		Decision: audit.DecisionDeny, Capability: "payment:send", ToolID: toolHITL,
		Resource: audit.Resource{Type: "action", Value: "pay-approve-2"},
		Obligations: []audit.Obligation{
			{Type: obHITLDecision, Params: map[string]string{paramHITLReason: "timeout"}},
			{Type: obHITLUnauthed, Params: map[string]string{paramHITLClaimedApp: "human:mallory", paramHITLAuthed: "false"}},
		},
	})
	// seq7: DSAR recebido (subject pseudónimo).
	mustAppend(t, store, audit.AuditRecord{
		Decision: audit.DecisionAllow, Capability: labelDSARReceived, ToolID: toolDSAR,
		RequestID: "dsar-req-01",
		Resource:  audit.Resource{Type: labelDSARSubjectType, Value: "subj-tok-eu-01"},
	})
	// seq8: DSAR key_destroyed com rótulos de stores.
	mustAppend(t, store, audit.AuditRecord{
		Decision: audit.DecisionAllow, Capability: labelDSARDestroyed, ToolID: toolDSAR,
		RequestID:   "dsar-req-01",
		Resource:    audit.Resource{Type: labelDSARSubjectType, Value: "subj-tok-eu-01"},
		Obligations: []audit.Obligation{{Type: labelDSARStores, Fields: []string{"audit", "redaction"}}},
	})
	// seq9: policy.changed (autor, sem cadeia — não é acção de agente).
	mustAppend(t, store, audit.AuditRecord{
		Decision: audit.DecisionAllow, Capability: "policy:reload", PolicyVersion: "2.0.0",
		Principal: audit.Principal{NHIID: "human:admin"},
		Resource:  audit.Resource{Type: labelPolicyChanged, Value: "hash-abc"},
	})
	// seq10: retention.expired (evento de sistema).
	mustAppend(t, store, audit.AuditRecord{
		Decision: audit.DecisionAllow, Capability: "retention:expire",
		Resource: audit.Resource{Type: labelRetentionExpire, Value: "rec-77"},
	})

	head, _ := store.Head(context.Background(), testPartition)
	if head != 10 {
		t.Fatalf("esperava head=10, obtive %d", head)
	}
	return store, head
}

// TestGenerateReport_KnownAudit é o teste de RELATÓRIO (AC3): sobre um audit conhecido,
// as contagens/secções batem — atribuição, decisões PDP, aprovações HITL +
// override-rate, DSARs e soberania.
func TestGenerateReport_KnownAudit(t *testing.T) {
	store, head := seedCleanAudit(t)

	report, err := GenerateReport(context.Background(), store, testPartition, 1, head, WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}

	// PDP: 2 permits (seq1,2), 1 deny (seq3), 1 escalate (seq4). Os eventos de
	// governação NÃO contam como decisões PDP.
	if report.PDP.Permits != 2 || report.PDP.Denies != 1 || report.PDP.Escalates != 1 {
		t.Fatalf("PDP inesperado: %+v", report.PDP)
	}
	if report.PDP.Total() != 4 {
		t.Fatalf("PDP total esperado 4, obtive %d", report.PDP.Total())
	}

	// Atribuição: alice com 3 acções (seq1,2,4), bob com 1 (seq3).
	if len(report.Attribution) != 2 {
		t.Fatalf("esperava 2 atribuições, obtive %d: %+v", len(report.Attribution), report.Attribution)
	}
	if report.Attribution[0].Human != "human:alice" || report.Attribution[0].Actions != 3 {
		t.Fatalf("atribuição alice inesperada: %+v", report.Attribution[0])
	}
	if report.Attribution[1].Human != "human:bob" || report.Attribution[1].Actions != 1 {
		t.Fatalf("atribuição bob inesperada: %+v", report.Attribution[1])
	}
	if len(report.Attribution[0].Principals) != 1 || report.Attribution[0].Principals[0] != "agentA" {
		t.Fatalf("principals de alice inesperados: %+v", report.Attribution[0].Principals)
	}

	// HITL: 2 prompted, 1 aprovado, 1 negado, 1 não-autenticado, override-rate 0.5.
	if report.HITL.Prompted != 2 || report.HITL.Approved != 1 || report.HITL.Denied != 1 {
		t.Fatalf("HITL inesperado: %+v", report.HITL)
	}
	if report.HITL.Unauthenticated != 1 {
		t.Fatalf("HITL unauthenticated esperado 1, obtive %d", report.HITL.Unauthenticated)
	}
	if report.HITL.OverrideRate != 0.5 {
		t.Fatalf("override-rate esperado 0.5, obtive %v", report.HITL.OverrideRate)
	}

	// DSARs: 2 eventos; o key_destroyed enumera os stores.
	if len(report.DSARs) != 2 {
		t.Fatalf("esperava 2 DSARs, obtive %d: %+v", len(report.DSARs), report.DSARs)
	}
	var destroyed *DSAREvent
	for i := range report.DSARs {
		if report.DSARs[i].Event == labelDSARDestroyed {
			destroyed = &report.DSARs[i]
		}
	}
	if destroyed == nil {
		t.Fatal("faltou o evento dsar.key_destroyed")
	}
	if len(destroyed.Stores) != 2 {
		t.Fatalf("esperava 2 stores shredded, obtive %+v", destroyed.Stores)
	}

	// Soberania: seq2 (region obligation eu) + seq3 (Resource.Region us) = 2.
	if len(report.Sovereignty) != 2 {
		t.Fatalf("esperava 2 eventos de soberania, obtive %d: %+v", len(report.Sovereignty), report.Sovereignty)
	}
	regions := map[string]audit.Decision{}
	for _, s := range report.Sovereignty {
		regions[s.Region] = s.Decision
	}
	if regions["eu"] != audit.DecisionAllow || regions["us"] != audit.DecisionDeny {
		t.Fatalf("soberania por região inesperada: %+v", report.Sovereignty)
	}

	// Sem anomalias e conforme.
	if report.HasAnonymousActions() {
		t.Fatalf("audit limpo não devia ter acções anónimas: %+v", report.Anomalies)
	}
	if !report.Clean() {
		t.Fatal("relatório de audit limpo e verificado devia ser Clean()")
	}
	if report.GeneratedAt.IsZero() {
		t.Fatal("GeneratedAt não foi carimbado")
	}
}

// TestGenerateReport_IntegrityProof é o teste de INTEGRIDADE (AC4, caminho feliz): o
// relatório derivado passa a verificação de hash do audit e carrega a prova (range +
// EntryHash do head verificado).
func TestGenerateReport_IntegrityProof(t *testing.T) {
	store, head := seedCleanAudit(t)
	report, err := GenerateReport(context.Background(), store, testPartition, 1, head)
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if !report.Integrity.Verified {
		t.Fatal("integridade devia estar verificada")
	}
	if report.Integrity.From != 1 || report.Integrity.To != head {
		t.Fatalf("range da prova inesperado: %+v", report.Integrity)
	}
	// O EntryHash do head verificado deve bater com o registo real.
	rec, ok, _ := store.At(context.Background(), testPartition, head)
	if !ok {
		t.Fatal("registo do head ausente")
	}
	if report.Integrity.HeadEntryHashHex == "" {
		t.Fatal("prova de integridade sem EntryHash do head")
	}
	if got := hexOf(rec.EntryHash); got != report.Integrity.HeadEntryHashHex {
		t.Fatalf("EntryHash da prova (%s) diverge do head real (%s)", report.Integrity.HeadEntryHashHex, got)
	}
}

// mutatingStore embrulha um [audit.Store] e ADULTERA o registo de targetSeq ao ser
// lido — muta a Decision SEM tocar no EntryHash armazenado. Simula uma adulteração
// de conteúdo: [audit.Verify] recomputa o EntryHash a partir do conteúdo mutado e
// detecta a divergência (TamperMutation).
type mutatingStore struct {
	audit.Store
	targetSeq uint64
}

func (m mutatingStore) mutate(rec audit.AuditRecord) audit.AuditRecord {
	if rec.AuditSeq == m.targetSeq {
		// deny→allow: altera o conteúdo canónico, o EntryHash armazenado deixa de bater.
		rec.Decision = audit.DecisionAllow
	}
	return rec
}

func (m mutatingStore) Read(ctx context.Context, partition string, from, to uint64) ([]audit.AuditRecord, error) {
	recs, err := m.Store.Read(ctx, partition, from, to)
	if err != nil {
		return nil, err
	}
	for i := range recs {
		recs[i] = m.mutate(recs[i])
	}
	return recs, nil
}

func (m mutatingStore) At(ctx context.Context, partition string, seq uint64) (audit.AuditRecord, bool, error) {
	rec, ok, err := m.Store.At(ctx, partition, seq)
	if err != nil || !ok {
		return rec, ok, err
	}
	return m.mutate(rec), ok, nil
}

// TestGenerateReport_TamperedAuditFails é o teste de INTEGRIDADE (AC4, caminho
// adverso): um audit ADULTERADO faz o relatório NÃO se gerar (fail-closed), e o erro
// desembrulha para [audit.ErrTampered].
func TestGenerateReport_TamperedAuditFails(t *testing.T) {
	base, head := seedCleanAudit(t)
	tampered := mutatingStore{Store: base, targetSeq: 3} // adultera a decisão do seq3

	report, err := GenerateReport(context.Background(), tampered, testPartition, 1, head)
	if err == nil {
		t.Fatal("esperava erro sobre audit adulterado; relatório não devia ser gerado")
	}
	if report != nil {
		t.Fatalf("não se gera relatório sobre audit adulterado; obtive %+v", report)
	}
	if !errors.Is(err, ErrTamperedAudit) {
		t.Fatalf("erro devia ser ErrTamperedAudit, obtive %v", err)
	}
	if !errors.Is(err, audit.ErrTampered) {
		t.Fatalf("erro devia desembrulhar para audit.ErrTampered, obtive %v", err)
	}
}

// TestGenerateReport_AnonymousActionFailClosed é o teste de COMPLETUDE integrado
// (AC1): um audit com uma acção sem principal completo faz o relatório SINALIZAR
// [ErrAnonymousAction] (fail-closed), com a acção anónima em Anomalies para forense.
func TestGenerateReport_AnonymousActionFailClosed(t *testing.T) {
	store := audit.NewMemStore()
	aliceChain := []audit.DelegationHop{{Sub: "human:alice", ActAs: "agentA"}}
	// seq1: acção legítima.
	mustAppend(t, store, audit.AuditRecord{
		Decision: audit.DecisionAllow, Capability: "http:get", ToolID: "tool.http",
		Principal: audit.Principal{NHIID: "agentA", DelegationChain: aliceChain},
	})
	// seq2: acção ANÓNIMA — sem cadeia de delegação.
	mustAppend(t, store, audit.AuditRecord{
		Decision: audit.DecisionAllow, Capability: "http:get", ToolID: "tool.http",
		Principal: audit.Principal{NHIID: "ghostAgent"},
	})
	head, _ := store.Head(context.Background(), testPartition)

	report, err := GenerateReport(context.Background(), store, testPartition, 1, head)
	if !errors.Is(err, ErrAnonymousAction) {
		t.Fatalf("esperava ErrAnonymousAction, obtive %v", err)
	}
	if report == nil {
		t.Fatal("o relatório deve vir não-nil (forense) mesmo com acção anónima")
	}
	if len(report.Anomalies) != 1 {
		t.Fatalf("esperava 1 acção anónima, obtive %d: %+v", len(report.Anomalies), report.Anomalies)
	}
	if report.Anomalies[0].AuditSeq != 2 {
		t.Fatalf("a acção anónima devia ser o seq2, obtive %d", report.Anomalies[0].AuditSeq)
	}
	if report.Clean() {
		t.Fatal("um relatório com acção anónima não é Clean()")
	}
	// A integridade em si passou (o audit não foi adulterado) — o fail é de anonimato.
	if !report.Integrity.Verified {
		t.Fatal("o audit não foi adulterado; a integridade devia estar verificada")
	}
}

// TestGenerateReport_LaunderedAnonymousActionFailClosed é o teste de REGRESSÃO
// integrado de AC1: uma acção ANÓNIMA (tool call de agente com cadeia vazia) que
// exibe SIMULTANEAMENTE três rótulos de governação reservados (Resource.Type=dsar,
// Capability=dsar.blocked, Obligation=hitl_decision) NÃO se pode isentar da
// completude — o relatório sinaliza [ErrAnonymousAction] e a acção surge em Anomalies.
// Além disso, o registo branqueado NÃO corrompe as contagens: não é contado como DSAR
// nem como HITL, e É contado no PDP pela sua decisão real.
func TestGenerateReport_LaunderedAnonymousActionFailClosed(t *testing.T) {
	store := audit.NewMemStore()
	aliceChain := []audit.DelegationHop{{Sub: "human:alice", ActAs: "agentA"}}

	// seq1: acção legítima (permit) — atribuível a alice.
	mustAppend(t, store, audit.AuditRecord{
		Decision: audit.DecisionAllow, Capability: "http:get", ToolID: "tool.http",
		Principal: audit.Principal{NHIID: "agentA", DelegationChain: aliceChain},
	})
	// seq2: acção ANÓNIMA branqueada — ToolID de agente real, cadeia VAZIA, a exibir
	// os três rótulos de governação reservados ao mesmo tempo.
	mustAppend(t, store, audit.AuditRecord{
		Decision: audit.DecisionDeny, Capability: labelDSARBlocked, ToolID: "tool.http",
		Principal:   audit.Principal{NHIID: "ghostAgent"}, // sem cadeia → anónima
		Resource:    audit.Resource{Type: labelDSARSubjectType, Value: "subj-spoof"},
		Obligations: []audit.Obligation{{Type: obHITLDecision}},
	})
	head, _ := store.Head(context.Background(), testPartition)

	report, err := GenerateReport(context.Background(), store, testPartition, 1, head)
	if !errors.Is(err, ErrAnonymousAction) {
		t.Fatalf("esperava ErrAnonymousAction sobre acção branqueada, obtive %v", err)
	}
	if report == nil {
		t.Fatal("o relatório deve vir não-nil (forense) mesmo com acção anónima branqueada")
	}
	if len(report.Anomalies) != 1 || report.Anomalies[0].AuditSeq != 2 {
		t.Fatalf("a acção branqueada (seq2) devia estar em Anomalies: %+v", report.Anomalies)
	}
	// A acção branqueada NÃO se disfarça de evento de governação nas projecções.
	if len(report.DSARs) != 0 {
		t.Fatalf("a acção branqueada não devia contar como DSAR: %+v", report.DSARs)
	}
	if report.HITL.Prompted != 0 {
		t.Fatalf("a acção branqueada não devia contar como HITL: %+v", report.HITL)
	}
	// E É contabilizada no PDP pela sua decisão real (deny), não descartada.
	if report.PDP.Permits != 1 || report.PDP.Denies != 1 {
		t.Fatalf("PDP devia contar o permit legítimo e o deny branqueado: %+v", report.PDP)
	}
	// A atribuição só cobre a acção atribuível (alice); a anónima não é atribuída.
	if len(report.Attribution) != 1 || report.Attribution[0].Human != "human:alice" {
		t.Fatalf("só a acção legítima é atribuível: %+v", report.Attribution)
	}
	if report.Clean() {
		t.Fatal("um relatório com acção anónima branqueada não é Clean()")
	}
}

var piiPattern = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)

// TestGenerateReport_NoPII é o teste de AUSÊNCIA DE PII (AC5): o relatório usa só os
// campos já redigidos/tokenizados e nunca decifra PayloadRef. Um titular shredded
// aparece como REFERÊNCIA (pseudónimo), e nenhum valor pessoal em claro (ex.: email)
// nem parâmetros não-projectados de obrigações vazam para o relatório.
func TestGenerateReport_NoPII(t *testing.T) {
	store := audit.NewMemStore()
	aliceChain := []audit.DelegationHop{{Sub: "human:alice", ActAs: "agentA"}}

	// Uma acção com PayloadRef (referência cifrada por-titular): o relatório NUNCA a lê.
	mustAppend(t, store, audit.AuditRecord{
		Decision: audit.DecisionAllow, Capability: "http:get", ToolID: "tool.http",
		Principal:  audit.Principal{NHIID: "agentA", DelegationChain: aliceChain},
		Resource:   audit.Resource{Type: "url", Value: "https://api.example.com/x"},
		PayloadRef: &audit.PayloadRef{ContentHash: []byte{0xde, 0xad}, KeyRef: "kv:key-01", SubjectID: "subj-tok-eu-01"},
	})
	// Um HITL cujo Param de obrigação contém um SENTINELA de PII que o relatório NÃO
	// projecta (só conta HITL): guarda de regressão contra dump cego de params.
	mustAppend(t, store, audit.AuditRecord{
		Decision: audit.DecisionAllow, Capability: "payment:send", ToolID: toolHITL,
		Principal: audit.Principal{NHIID: "human:carol"},
		Obligations: []audit.Obligation{
			{Type: obHITLDecision, Params: map[string]string{paramHITLReason: "leak-sentinel@pii.example"}},
		},
	})
	// DSAR de um titular shredded — aparece como referência pseudónima.
	mustAppend(t, store, audit.AuditRecord{
		Decision: audit.DecisionAllow, Capability: labelDSARDestroyed, ToolID: toolDSAR,
		RequestID: "dsar-req-09",
		Resource:  audit.Resource{Type: labelDSARSubjectType, Value: "subj-tok-eu-01"},
	})
	head, _ := store.Head(context.Background(), testPartition)

	report, err := GenerateReport(context.Background(), store, testPartition, 1, head)
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}

	blob, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	text := string(blob)

	// Nenhum email/PII em claro.
	if m := piiPattern.FindString(text); m != "" {
		t.Fatalf("relatório contém padrão de PII em claro: %q", m)
	}
	// O sentinela de PII (num param de obrigação não-projectado) NÃO vaza.
	if regexp.MustCompile(`leak-sentinel`).MatchString(text) {
		t.Fatal("um param de obrigação não-projectado vazou para o relatório (dump cego)")
	}
	// O titular shredded aparece como REFERÊNCIA (o pseudónimo), não como PII.
	if !regexp.MustCompile(`subj-tok-eu-01`).MatchString(text) {
		t.Fatal("o titular DSAR devia aparecer como referência pseudónima")
	}
}

// TestGenerateReport_WithVerifierAndRegionFallback cobre a opção [WithVerifier]
// (verificador custom injectado) e o fallback de soberania: uma obrigação `region`
// SEM param explícito cai na Resource.Region.
func TestGenerateReport_WithVerifierAndRegionFallback(t *testing.T) {
	store := audit.NewMemStore()
	// Acção cuja raiz usa o prefixo custom "person:" — só o verificador injectado a
	// reconhece como completa (o default rejeitá-la-ia).
	personChain := []audit.DelegationHop{{Sub: "person:dave", ActAs: "agentD"}}
	// Obrigação region sem Params["region"]: a região vem da Resource.Region.
	mustAppend(t, store, audit.AuditRecord{
		Decision: audit.DecisionAllow, Capability: "http:get", ToolID: "tool.http",
		Principal:   audit.Principal{NHIID: "agentD", DelegationChain: personChain},
		Resource:    audit.Resource{Type: "url", Value: "https://api.pt.example.com", Region: "pt"},
		Obligations: []audit.Obligation{{Type: obRegion}}, // sem param region
	})
	head, _ := store.Head(context.Background(), testPartition)

	v := NewAccountabilityVerifier(WithHumanRootPrefix("person:"))
	report, err := GenerateReport(context.Background(), store, testPartition, 1, head, WithVerifier(v))
	if err != nil {
		t.Fatalf("GenerateReport com verificador custom: %v", err)
	}
	if report.HasAnonymousActions() {
		t.Fatalf("a raiz person: devia ser aceite pelo verificador injectado: %+v", report.Anomalies)
	}
	if len(report.Sovereignty) != 1 || report.Sovereignty[0].Region != "pt" {
		t.Fatalf("fallback de região (Resource.Region) falhou: %+v", report.Sovereignty)
	}
	if len(report.Attribution) != 1 || report.Attribution[0].Human != "person:dave" {
		t.Fatalf("atribuição sob prefixo custom inesperada: %+v", report.Attribution)
	}
}

func hexOf(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexdigits[c>>4]
		out[i*2+1] = hexdigits[c&0x0f]
	}
	return string(out)
}

// TestGenerateReport_SpanNoPII confirma que o span OTel da geração leva só metadados
// (range, contagens, integridade) e NENHUMA PII.
func TestGenerateReport_SpanNoPII(t *testing.T) {
	store, head := seedCleanAudit(t)
	tr := otelgenai.NewRecordingTracer(nil)

	if _, err := GenerateReport(context.Background(), store, testPartition, 1, head, WithTracer(tr)); err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	spans := tr.SpansByOperation(opGenerateReport)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span %q, obtive %d", opGenerateReport, len(spans))
	}
	sp := spans[0]
	if !sp.Ended {
		t.Fatal("o span da geração não foi fechado")
	}
	if v, ok := sp.Attributes[attrVerified]; !ok || v != true {
		t.Fatalf("span devia marcar integridade verificada, obtive %v", sp.Attributes[attrVerified])
	}
	if v, ok := sp.Attributes[attrPermits]; !ok || v.(int64) != 2 {
		t.Fatalf("span devia contar 2 permits, obtive %v", sp.Attributes[attrPermits])
	}
	// Nenhum atributo do span deve conter PII.
	for k, v := range sp.Attributes {
		if s, ok := v.(string); ok {
			if piiPattern.MatchString(s) {
				t.Fatalf("atributo de span %q contém PII: %q", k, s)
			}
		}
	}
}
