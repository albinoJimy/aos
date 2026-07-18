package referencemonitor

import (
	"context"
	"strings"
	"testing"
)

// mediateWithObligations constrói um Monitor cuja cadeia é identity(stub) +
// policy(spy que devolve `obs`) + audit(stub), regista tool.echo (captura o input
// despachado) e medeia `call`. Devolve a decisão e o input REALMENTE visto pelo
// efeito (nil se a tool não foi despachada). É o harness do enforcement de
// obrigações (AOS-087, AC4): o que a política obriga vs o que o efeito vê.
func mediateWithObligations(t *testing.T, call Call, obs []Obligation) (Decision, []byte) {
	t.Helper()
	var dispatched []byte
	var didDispatch bool
	sink := &fakeSink{}
	m := New(
		WithHooks(
			IdentityStub{},
			&spyHook{name: "policy", result: HookResult{Decision: HookAllow, Obligations: obs}},
			AuditStub{},
		),
		WithEventSink(sink),
	)
	if err := m.Register(call.ToolID, func(_ context.Context, in []byte) ([]byte, error) {
		didDispatch = true
		dispatched = append([]byte(nil), in...)
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	d, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if !didDispatch {
		return d, nil
	}
	return d, dispatched
}

// jsonCall devolve um baseCall com Input JSON contendo PII em objecto de topo,
// objecto aninhado e dentro de um array (exercita a redação recursiva completa).
func jsonCall() Call {
	c := baseCall()
	c.Input = []byte(`{"body":"hi","email":"a@b.com","nested":{"phone":"999"},"contacts":[{"phone":"111"},{"phone":"222"}]}`)
	return c
}

// TestEnforce_RedactPII_AppliedBeforeEffect: a obrigação redact_pii é APLICADA aos
// args ANTES do efeito — a tool não vê os campos redigidos em claro, mesmo aninhados.
func TestEnforce_RedactPII_AppliedBeforeEffect(t *testing.T) {
	t.Parallel()
	d, input := mediateWithObligations(t, jsonCall(),
		[]Obligation{{Type: ObligationRedactPII, Fields: []string{"email", "phone"}}})
	if !d.Permitted() {
		t.Fatalf("esperava permit, obtive %q (%s)", d.Effect, d.Reason)
	}
	s := string(input)
	if strings.Contains(s, "a@b.com") || strings.Contains(s, "999") ||
		strings.Contains(s, "111") || strings.Contains(s, "222") {
		t.Errorf("PII nao redigida antes do efeito (incl. aninhada/array): %s", s)
	}
	if !strings.Contains(s, redactedMarker) {
		t.Errorf("esperava marcador de redacao no input despachado: %s", s)
	}
	// Campos não-PII preservam-se.
	if !strings.Contains(s, `"body":"hi"`) {
		t.Errorf("campo nao-PII foi alterado: %s", s)
	}
}

// TestEnforce_RedactPII_NonJSONDeny: uma obrigação redact_pii sobre input não-JSON
// não-vazio é não-garantível ⇒ deny fail-closed (o efeito nunca corre).
func TestEnforce_RedactPII_NonJSONDeny(t *testing.T) {
	t.Parallel()
	c := baseCall()
	c.Input = []byte("plaintext com a@b.com")
	d, input := mediateWithObligations(t, c,
		[]Obligation{{Type: ObligationRedactPII, Fields: []string{"email"}}})
	if d.Effect != EffectDeny {
		t.Fatalf("esperava deny (redacao nao-garantida), obtive %q", d.Effect)
	}
	if d.DeniedBy != "obligation" || d.Code != CodeObligationUnsatisfied {
		t.Errorf("atribuicao errada: DeniedBy=%q Code=%q", d.DeniedBy, d.Code)
	}
	if input != nil {
		t.Error("a tool NAO devia ter sido despachada num deny de obrigacao")
	}
}

// TestEnforce_RedactPII_NoFieldsDeny: redact_pii sem campos tem alvo indeterminado ⇒
// deny fail-closed (não sabemos o que redigir).
func TestEnforce_RedactPII_NoFieldsDeny(t *testing.T) {
	t.Parallel()
	d, _ := mediateWithObligations(t, jsonCall(), []Obligation{{Type: ObligationRedactPII}})
	if d.Effect != EffectDeny || d.Code != CodeObligationUnsatisfied {
		t.Fatalf("esperava deny CodeObligationUnsatisfied, obtive %q/%q", d.Effect, d.Code)
	}
}

// TestEnforce_RedactPII_EmptyInputPermit: sem payload não há PII a redigir — a
// obrigação é satisfeita e o efeito corre.
func TestEnforce_RedactPII_EmptyInputPermit(t *testing.T) {
	t.Parallel()
	c := baseCall()
	c.Input = nil
	d, _ := mediateWithObligations(t, c,
		[]Obligation{{Type: ObligationRedactPII, Fields: []string{"email"}}})
	if !d.Permitted() {
		t.Fatalf("esperava permit com input vazio, obtive %q (%s)", d.Effect, d.Reason)
	}
}

// TestEnforce_Region_CrossBorderDeny: uma call cuja região do recurso viola a região
// exigida pela obrigação é NEGADA antes do dispatch (soberania de dados).
func TestEnforce_Region_CrossBorderDeny(t *testing.T) {
	t.Parallel()
	c := baseCall()
	c.Resource.Region = "us"
	d, input := mediateWithObligations(t, c,
		[]Obligation{{Type: ObligationRegion, Params: map[string]string{"region": "eu"}}})
	if d.Effect != EffectDeny {
		t.Fatalf("esperava deny cross-border, obtive %q", d.Effect)
	}
	if d.DeniedBy != "obligation" || d.Code != CodeObligationUnsatisfied {
		t.Errorf("atribuicao errada: DeniedBy=%q Code=%q", d.DeniedBy, d.Code)
	}
	if !strings.Contains(d.Reason, "cross-border") {
		t.Errorf("reason devia nomear cross-border: %q", d.Reason)
	}
	if input != nil {
		t.Error("a tool NAO devia ser despachada num deny de regiao")
	}
}

// TestEnforce_Region_MatchPermit: região do recurso == região exigida ⇒ o efeito corre.
func TestEnforce_Region_MatchPermit(t *testing.T) {
	t.Parallel()
	c := baseCall() // Region: "eu"
	d, input := mediateWithObligations(t, c,
		[]Obligation{{Type: ObligationRegion, Params: map[string]string{"region": "EU"}}}) // case-insensitive
	if !d.Permitted() {
		t.Fatalf("esperava permit (regiao coincide), obtive %q (%s)", d.Effect, d.Reason)
	}
	if input == nil {
		t.Error("a tool devia ter sido despachada")
	}
}

// TestEnforce_Region_NoRequiredRegionDeny: obrigação de região sem região exigida é
// não-satisfazível ⇒ deny.
func TestEnforce_Region_NoRequiredRegionDeny(t *testing.T) {
	t.Parallel()
	d, _ := mediateWithObligations(t, baseCall(),
		[]Obligation{{Type: ObligationRegion, Params: map[string]string{}}})
	if d.Effect != EffectDeny || d.Code != CodeObligationUnsatisfied {
		t.Fatalf("esperava deny, obtive %q/%q", d.Effect, d.Code)
	}
}

// TestEnforce_Region_ResourceWithoutRegionDeny: recurso sem região resolvida sob uma
// obrigação de região ⇒ deny (não se pode confirmar a soberania).
func TestEnforce_Region_ResourceWithoutRegionDeny(t *testing.T) {
	t.Parallel()
	c := baseCall()
	c.Resource.Region = ""
	d, _ := mediateWithObligations(t, c,
		[]Obligation{{Type: ObligationRegion, Params: map[string]string{"region": "eu"}}})
	if d.Effect != EffectDeny || d.Code != CodeObligationUnsatisfied {
		t.Fatalf("esperava deny, obtive %q/%q", d.Effect, d.Code)
	}
}

// TestEnforce_UnknownObligationDeny: uma obrigação de tipo desconhecido é
// não-satisfazível ⇒ deny fail-closed (nunca ignorada silenciosamente).
func TestEnforce_UnknownObligationDeny(t *testing.T) {
	t.Parallel()
	d, input := mediateWithObligations(t, baseCall(),
		[]Obligation{{Type: "quantum_entangle"}})
	if d.Effect != EffectDeny {
		t.Fatalf("esperava deny para obrigacao desconhecida, obtive %q", d.Effect)
	}
	if d.DeniedBy != "obligation" || d.Code != CodeObligationUnsatisfied {
		t.Errorf("atribuicao errada: DeniedBy=%q Code=%q", d.DeniedBy, d.Code)
	}
	if !strings.Contains(d.Reason, "quantum_entangle") {
		t.Errorf("reason devia nomear a obrigacao: %q", d.Reason)
	}
	if input != nil {
		t.Error("a tool NAO devia ser despachada")
	}
}

// TestEnforce_TTLAndAuditPropagated: ttl e audit são obrigações conhecidas — o efeito
// corre e as obrigações VIAJAM na Decision para o consumidor as impor.
func TestEnforce_TTLAndAuditPropagated(t *testing.T) {
	t.Parallel()
	obs := []Obligation{
		{Type: ObligationTTL, Params: map[string]string{"seconds": "3600"}},
		{Type: ObligationAudit, Params: map[string]string{"level": "full"}},
	}
	d, input := mediateWithObligations(t, baseCall(), obs)
	if !d.Permitted() {
		t.Fatalf("esperava permit, obtive %q (%s)", d.Effect, d.Reason)
	}
	if input == nil {
		t.Error("a tool devia ter sido despachada")
	}
	if len(d.Obligations) != 2 {
		t.Fatalf("obrigacoes nao propagaram na Decision: %+v", d.Obligations)
	}
}

// TestEnforce_AppliedObligationsSealedInAudit: as obrigações aplicadas ficam no
// registo de mediação (AC6 — audit da decisão com o principal e as obrigações).
func TestEnforce_AppliedObligationsSealedInAudit(t *testing.T) {
	t.Parallel()
	var dispatched bool
	sink := &fakeSink{}
	m := New(
		WithHooks(
			IdentityStub{},
			&spyHook{name: "policy", result: HookResult{Decision: HookAllow,
				Obligations: []Obligation{{Type: ObligationRedactPII, Fields: []string{"email"}}, {Type: ObligationAudit}}}},
			AuditStub{},
		),
		WithEventSink(sink),
	)
	_ = m.Register("tool.echo", toolSpy(&dispatched, nil))
	c := baseCall()
	c.Input = []byte(`{"email":"a@b.com"}`)
	d, err := m.Mediate(context.Background(), c)
	if err != nil || !d.Permitted() {
		t.Fatalf("esperava permit, obtive %q err=%v", d.Effect, err)
	}
	recs := sink.all()
	if len(recs) != 1 {
		t.Fatalf("esperava 1 registo, obtive %d", len(recs))
	}
	if recs[0].Principal.NHIID != "nhi-1" {
		t.Errorf("registo sem principal completo: %+v", recs[0].Principal)
	}
	if len(recs[0].Obligations) != 2 {
		t.Errorf("obrigacoes aplicadas nao seladas no audit: %+v", recs[0].Obligations)
	}
}

// TestEnforceObligations_Unit exercita directamente os ramos puros do enforcement
// (sem RM) para cobertura determinista das fronteiras.
func TestEnforceObligations_Unit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		call Call
		obs  []Obligation
		ok   bool
	}{
		{"vazio_ok", baseCall(), nil, true},
		{"audit_ok", baseCall(), []Obligation{{Type: ObligationAudit}}, true},
		{"ttl_ok", baseCall(), []Obligation{{Type: ObligationTTL}}, true},
		{"regiao_ok", baseCall(), []Obligation{{Type: ObligationRegion, Params: map[string]string{"allowed": "eu"}}}, true},
		{"regiao_mismatch", func() Call { c := baseCall(); c.Resource.Region = "us"; return c }(),
			[]Obligation{{Type: ObligationRegion, Params: map[string]string{"region": "eu"}}}, false},
		{"desconhecida", baseCall(), []Obligation{{Type: "x"}}, false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := tc.call
			_, ok := enforceObligations(&c, tc.obs)
			if ok != tc.ok {
				t.Fatalf("ok=%v, esperava %v", ok, tc.ok)
			}
		})
	}
}
