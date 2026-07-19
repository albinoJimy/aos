package compliance

import (
	"testing"

	"github.com/aos-ref/platform/audit"
)

// actionRec constrói um AuditRecord de ACÇÃO (mediação de tool call) com o principal
// dado, para os testes de completude. Sem partição/selagem — o verificador opera
// sobre o conteúdo.
func actionRec(nhi string, chain []audit.DelegationHop, cap string) audit.AuditRecord {
	return audit.AuditRecord{
		Decision:   audit.DecisionAllow,
		Capability: cap,
		ToolID:     "tool.http",
		Principal:  audit.Principal{NHIID: nhi, DelegationChain: chain},
	}
}

// TestAccountability_DetectsAnonymousActions é o teste de COMPLETUDE (AC1): uma acção
// sem principal completo — cadeia vazia, raiz não-humana, elo órfão ou ActAs vazio —
// é DETECTADA como anónima; uma acção com cadeia completa até um humano NÃO é.
func TestAccountability_DetectsAnonymousActions(t *testing.T) {
	v := NewAccountabilityVerifier()

	complete := []audit.DelegationHop{{Sub: "human:alice", ActAs: "agentA"}}
	twoHop := []audit.DelegationHop{
		{Sub: "human:alice", ActAs: "agentA"},
		{Sub: "agentA", ActAs: "agentB"},
	}
	orphan := []audit.DelegationHop{
		{Sub: "human:alice", ActAs: "agentA"},
		{Sub: "agentX", ActAs: "agentB"}, // descontinuidade: agentA != agentX
	}

	cases := []struct {
		name      string
		rec       audit.AuditRecord
		anonymous bool
	}{
		{"cadeia_completa_1hop", actionRec("agentA", complete, "http:get"), false},
		{"cadeia_completa_2hop", actionRec("agentB", twoHop, "http:get"), false},
		{"cadeia_vazia", actionRec("ghost", nil, "http:get"), true},
		{"raiz_nao_humana", actionRec("agentA", []audit.DelegationHop{{Sub: "agent:root", ActAs: "agentA"}}, "http:get"), true},
		{"raiz_human_sem_id", actionRec("agentA", []audit.DelegationHop{{Sub: "human:", ActAs: "agentA"}}, "http:get"), true},
		{"actas_vazio", actionRec("agentA", []audit.DelegationHop{{Sub: "human:alice", ActAs: ""}}, "http:get"), true},
		{"elo_orfao", actionRec("agentB", orphan, "http:get"), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			anon := v.Verify([]audit.AuditRecord{tc.rec})
			if tc.anonymous && len(anon) != 1 {
				t.Fatalf("esperava 1 accao anonima, obtive %d: %+v", len(anon), anon)
			}
			if !tc.anonymous && len(anon) != 0 {
				t.Fatalf("esperava 0 accoes anonimas, obtive %d: %+v", len(anon), anon)
			}
			if tc.anonymous && anon[0].Reason == "" {
				t.Fatal("acção anónima sem motivo forense")
			}
		})
	}
}

// TestAccountability_SkipsGovernanceEvents garante que os eventos de governação
// (policy.changed, retention, DSAR, HITL) — que têm outro modelo de principal
// (autor/aprovador, sem cadeia de delegação) — NÃO são falsos-positivos de
// anonimato: a completude só se exige às ACÇÕES de agente.
func TestAccountability_SkipsGovernanceEvents(t *testing.T) {
	v := NewAccountabilityVerifier()
	govEvents := []audit.AuditRecord{
		{Decision: audit.DecisionAllow, Resource: audit.Resource{Type: labelPolicyChanged}, Principal: audit.Principal{NHIID: "human:admin"}},
		{Decision: audit.DecisionAllow, Resource: audit.Resource{Type: labelRetentionExpire}},
		{Decision: audit.DecisionAllow, ToolID: toolDSAR, Capability: labelDSARReceived, Resource: audit.Resource{Type: labelDSARSubjectType, Value: "subj-01"}},
		{Decision: audit.DecisionAllow, ToolID: toolHITL, Obligations: []audit.Obligation{{Type: obHITLDecision}}},
	}
	if anon := v.Verify(govEvents); len(anon) != 0 {
		t.Fatalf("eventos de governação não são acções de agente; esperava 0 anónimos, obtive %d: %+v", len(anon), anon)
	}
}

// TestAccountability_LaunderingReservedLabel é o teste de REGRESSÃO de AC1: uma acção
// ANÓNIMA (tool call de agente com cadeia de delegação vazia) NÃO se pode isentar da
// completude exibindo um rótulo de governação reservado. Para cada rótulo derivado do
// conteúdo action-controlled (Resource.Type/Capability/Obligation.Type), o registo
// continua a ser detectado como anónimo — o discriminador é a ToolID producer-bound,
// não os campos que a própria tool call controla.
func TestAccountability_LaunderingReservedLabel(t *testing.T) {
	v := NewAccountabilityVerifier()

	// Um registo BASE de tool call de agente anónimo: ToolID de agente real (não um
	// marcador de produtor de governação) e cadeia de delegação VAZIA.
	base := func() audit.AuditRecord {
		return audit.AuditRecord{
			Decision:   audit.DecisionDeny,
			Capability: "http:get",
			ToolID:     "tool.http",
			Principal:  audit.Principal{NHIID: "ghostAgent"}, // sem cadeia → anónimo
		}
	}

	cases := []struct {
		name  string
		spoof func(*audit.AuditRecord)
	}{
		{"resource_type_dsar_subject", func(r *audit.AuditRecord) { r.Resource.Type = labelDSARSubjectType }},
		{"resource_type_policy_changed", func(r *audit.AuditRecord) { r.Resource.Type = labelPolicyChanged }},
		{"resource_type_retention_expired", func(r *audit.AuditRecord) { r.Resource.Type = labelRetentionExpire }},
		{"capability_dsar_received", func(r *audit.AuditRecord) { r.Capability = labelDSARReceived }},
		{"capability_dsar_key_destroyed", func(r *audit.AuditRecord) { r.Capability = labelDSARDestroyed }},
		{"capability_dsar_blocked", func(r *audit.AuditRecord) { r.Capability = labelDSARBlocked }},
		{"obligation_hitl_decision", func(r *audit.AuditRecord) { r.Obligations = []audit.Obligation{{Type: obHITLDecision}} }},
		{"obligation_hitl_signature", func(r *audit.AuditRecord) { r.Obligations = []audit.Obligation{{Type: obHITLSignature}} }},
		{"obligation_hitl_unauthenticated", func(r *audit.AuditRecord) { r.Obligations = []audit.Obligation{{Type: obHITLUnauthed}} }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := base()
			tc.spoof(&rec)
			anon := v.Verify([]audit.AuditRecord{rec})
			if len(anon) != 1 {
				t.Fatalf("acção anónima com rótulo %q devia ser detectada; obtive %d anónimos: %+v", tc.name, len(anon), anon)
			}
			if anon[0].Reason == "" {
				t.Fatal("acção anónima branqueada sem motivo forense")
			}
		})
	}
}

// TestAccountability_GovernanceMarkerWithDelegationChain é a defesa-em-profundidade de
// AC1: um registo que exibe a ToolID de produtor de governação MAS carrega uma cadeia
// de delegação (a forma de uma acção mediada) NÃO é isentado — se a cadeia for
// incompleta, é sinalizado como anónimo. Um evento de governação genuíno (autor/
// aprovador, SEM cadeia) continua isento.
func TestAccountability_GovernanceMarkerWithDelegationChain(t *testing.T) {
	v := NewAccountabilityVerifier()

	orphanChain := []audit.DelegationHop{{Sub: "agent:root", ActAs: "agentA"}} // raiz não-humana

	for _, toolID := range []string{toolDSAR, toolHITL} {
		t.Run("acao_disfarcada_"+toolID, func(t *testing.T) {
			rec := audit.AuditRecord{
				Decision:    audit.DecisionAllow,
				ToolID:      toolID,
				Principal:   audit.Principal{NHIID: "agentA", DelegationChain: orphanChain},
				Obligations: []audit.Obligation{{Type: obHITLDecision}},
			}
			if anon := v.Verify([]audit.AuditRecord{rec}); len(anon) != 1 {
				t.Fatalf("acção com cadeia sob marcador %q devia ser sinalizada; obtive %d", toolID, len(anon))
			}
		})
		t.Run("evento_genuino_"+toolID, func(t *testing.T) {
			// Modelo autor/aprovador: sem cadeia de delegação → isento.
			rec := audit.AuditRecord{
				Decision:  audit.DecisionAllow,
				ToolID:    toolID,
				Principal: audit.Principal{NHIID: "human:carol"},
			}
			if anon := v.Verify([]audit.AuditRecord{rec}); len(anon) != 0 {
				t.Fatalf("evento de governação genuíno (sem cadeia) sob %q não é acção; obtive %d anónimos", toolID, len(anon))
			}
		})
	}
}

// TestAccountability_CustomPrefix confirma que o prefixo de raiz humana é
// configurável (composição/teste) mantendo a mesma disciplina.
func TestAccountability_CustomPrefix(t *testing.T) {
	v := NewAccountabilityVerifier(WithHumanRootPrefix("person:"))
	rec := actionRec("agentA", []audit.DelegationHop{{Sub: "person:bob", ActAs: "agentA"}}, "http:get")
	if anon := v.Verify([]audit.AuditRecord{rec}); len(anon) != 0 {
		t.Fatalf("raiz person: devia ser humana sob o prefixo custom; obtive %d anónimos", len(anon))
	}
	// A raiz human: já não conta sob o prefixo custom.
	rec2 := actionRec("agentA", []audit.DelegationHop{{Sub: "human:alice", ActAs: "agentA"}}, "http:get")
	if anon := v.Verify([]audit.AuditRecord{rec2}); len(anon) != 1 {
		t.Fatalf("sob prefixo person:, a raiz human: não é atribuível; esperava 1 anónimo, obtive %d", len(anon))
	}
}
