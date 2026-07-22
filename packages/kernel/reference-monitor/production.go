package referencemonitor

// Sentinelas de construção de produção. Todas comparáveis com errors.Is. São o
// enforcement fail-closed do handoff de wiring do AOS-069 (F5): um Monitor
// destinado a produção NÃO pode ser construído sem a barreira control/data-plane
// (ADR-005) activa nem sem auditoria durável — em vez de depender de convenção
// (o default de [New] é [DefaultHooks], SEM TaintGate), [NewProduction] verifica
// estruturalmente as invariantes e recusa-se a devolver um Monitor mal-configurado.
var (
	// ErrNoPrivilegedAuthorizer — [NewProduction] recebeu um [PrivilegedAuthorizer]
	// nil. Sem classificador de capabilities privilegiadas o TaintGate é um no-op
	// (nada é privilegiado ⇒ nada bloqueia), o que anularia a defesa ASI01; recusado.
	ErrNoPrivilegedAuthorizer = &MonitorError{Code: "E_NO_PRIVILEGED_AUTHORIZER", msg: "produção exige um PrivilegedAuthorizer não-nil (sem ele o TaintGate é no-op)"}

	// ErrTaintGateMissing — a cadeia de hooks FINAL (após aplicar as [Option]s) não
	// contém um TaintGate activo (com authorizer não-nil). Manifesta-se se um override
	// via [WithHooks] remover a barreira control/data-plane. Fail-closed: construção nega.
	ErrTaintGateMissing = &MonitorError{Code: "E_TAINT_GATE_MISSING", msg: "cadeia de produção sem TaintGate activo (barreira control/data-plane ADR-005 inactiva)"}

	// ErrNoDurableAudit — o [EventSink] final é não-durável (o discard por omissão).
	// Um RM sem auditoria durável nunca dispara o fail-closed de auditoria (a acção
	// seria permitida sem rasto); produção exige [WithEventSink] com um sink real.
	ErrNoDurableAudit = &MonitorError{Code: "E_NO_DURABLE_AUDIT", msg: "produção exige um EventSink durável (WithEventSink); o discard anula o fail-closed de auditoria"}
)

// NewProduction constrói um Monitor SEGURO-PARA-PRODUÇÃO, fechando estruturalmente
// as duas misconfigurações silenciosas que [New] permite (documentadas no seu
// contrato e no handoff do AOS-069 F5):
//
//   - ZERO enforcement de taint — [New] usa [DefaultHooks] por omissão, que NÃO
//     inclui o [TaintGate]; a invariante P0 do ADR-005 ("conteúdo untrusted não
//     autoriza acção privilegiada") ficaria silenciosamente inactiva.
//   - Auditoria não-durável — [New] usa o [discardSink] por omissão; o fail-closed
//     de auditoria (uma acção não-auditável não é permitida) nunca disparava.
//
// Ao contrário de [New], NewProduction NÃO devolve um Monitor mal-configurado: a
// cadeia canónica COM TaintGate ([DefaultHooksWithTaint]) é a base, as [Option]s
// aplicam-se por cima (podendo substituir hooks/sink), e as invariantes são
// RE-VERIFICADAS na cadeia FINAL. Um override que remova o TaintGate ou o sink
// durável faz a construção falhar fail-closed (ErrTaintGateMissing / ErrNoDurableAudit),
// em vez de degradar silenciosamente a postura de segurança.
//
// É a costura sancionada para o composition root ápice (packages/integration,
// EPIC-12) montar o RM de produção — e o guard-test [TestNewProduction] garante
// que a via não regride. privileged é o conjunto (real) de capabilities cuja
// autorização o AOS exige que provenha de dados trusted (ver [PrivilegedAuthorizer]).
func NewProduction(privileged PrivilegedAuthorizer, opts ...Option) (*Monitor, error) {
	if privileged == nil {
		return nil, ErrNoPrivilegedAuthorizer
	}
	// Base: cadeia canónica identity → policy → taint → budget → egress → audit.
	// As opções do chamador aplicam-se DEPOIS (WithEventSink real, e overrides
	// eventuais); as invariantes são re-afirmadas a seguir, não presumidas.
	base := make([]Option, 0, len(opts)+1)
	base = append(base, WithHooks(DefaultHooksWithTaint(privileged)...))
	base = append(base, opts...)
	m := New(base...)

	if !m.hasActiveTaintGate() {
		return nil, ErrTaintGateMissing
	}
	if !m.hasDurableAudit() {
		return nil, ErrNoDurableAudit
	}
	return m, nil
}

// hasActiveTaintGate reporta se a cadeia mediadora contém um [TaintGate] com um
// [PrivilegedAuthorizer] não-nil — i.e. enforcement de taint EFECTIVO, não um
// placeholder no-op. É mais forte que casar pelo Name(): um gate com authorizer
// nil nunca bloqueia e, para efeitos de produção, equivale a não ter gate.
func (m *Monitor) hasActiveTaintGate() bool {
	for _, h := range m.hooks {
		if g, ok := h.(TaintGate); ok && g.privileged != nil {
			return true
		}
	}
	return false
}

// hasDurableAudit reporta se o sink materializa auditoria durável — i.e. não é o
// [discardSink] (nem nil). Só um sink durável dá efeito ao fail-closed de auditoria.
func (m *Monitor) hasDurableAudit() bool {
	if m.sink == nil {
		return false
	}
	_, discard := m.sink.(discardSink)
	return !discard
}

// Sentinelas adicionais de [NewProductionSecure] (AOS-153). Comparáveis com errors.Is.
var (
	// ErrIdentityStub — a cadeia FINAL contém o [IdentityStub] neutro: sem hook de
	// identidade real a NHI não é verificada e a autoridade é forjável (AOS-005). A via
	// sancionada estrita recusa-o.
	ErrIdentityStub = &MonitorError{Code: "E_IDENTITY_STUB", msg: "produção-segura: cadeia final contém o IdentityStub neutro (identidade forjável — hook de identidade real AOS-005 ausente)"}

	// ErrEgressStub — a cadeia FINAL contém o [EgressStub] neutro: sem hook de egress
	// real o default-deny de rede (AOS-067) não corre. Recusado.
	ErrEgressStub = &MonitorError{Code: "E_EGRESS_STUB", msg: "produção-segura: cadeia final contém o EgressStub neutro (egress default-deny inactivo — hook de egress real AOS-067 ausente)"}

	// ErrScopeGateMissing — a cadeia FINAL não contém um [ScopeGate] com uma
	// [authz.AuthoritySource] não-nil: sem tecto de autoridade o escopo user∩classe
	// (AOS-071) não é imposto. Recusado.
	ErrScopeGateMissing = &MonitorError{Code: "E_SCOPE_GATE_MISSING", msg: "produção-segura: cadeia final sem ScopeGate activo (tecto de autoridade user∩classe AOS-071 ausente)"}
)

// NewProductionSecure é a costura de produção ESTRITA: herda TODAS as invariantes de
// [NewProduction] (PrivilegedAuthorizer não-nil, TaintGate activo, auditoria durável) e
// ACRESCENTA a rejeição dos STUBS NEUTROS que [NewProduction] ainda tolera. Fecha o
// buraco de um Monitor construído por [NewProduction] passar a própria guarda com o
// [IdentityStub] (identidade forjável) e o [EgressStub] (egress inerte) — a razão pela
// qual [NewProduction] é necessário-MAS-insuficiente. Recusa fail-closed, com erro
// tipado, se a cadeia FINAL (após aplicar as [Option]s):
//
//   - contiver o [IdentityStub] neutro ⇒ [ErrIdentityStub];
//   - contiver o [EgressStub] neutro ⇒ [ErrEgressStub];
//   - não contiver um [ScopeGate] com [authz.AuthoritySource] não-nil ⇒
//     [ErrScopeGateMissing].
//
// É a via sancionada para o composition root ápice (packages/integration) montar o RM
// de produção com a cadeia REAL (identity→reval→policy→taint→scope→budget→egress→audit,
// AOS-154): o chamador monta a cadeia via [WithHooks] com hooks reais e um ScopeGate com
// autoridade. Como [NewProduction], não impõe uma composição literal única — re-verifica
// as invariantes na cadeia final. O guard-test garante que a via não regride.
func NewProductionSecure(privileged PrivilegedAuthorizer, opts ...Option) (*Monitor, error) {
	m, err := NewProduction(privileged, opts...)
	if err != nil {
		return nil, err
	}
	if m.containsHook(func(h Hook) bool { _, ok := h.(IdentityStub); return ok }) {
		return nil, ErrIdentityStub
	}
	if m.containsHook(func(h Hook) bool { _, ok := h.(EgressStub); return ok }) {
		return nil, ErrEgressStub
	}
	if !m.hasActiveScopeGate() {
		return nil, ErrScopeGateMissing
	}
	return m, nil
}

// containsHook reporta se algum hook da cadeia mediadora satisfaz pred.
func (m *Monitor) containsHook(pred func(Hook) bool) bool {
	for _, h := range m.hooks {
		if pred(h) {
			return true
		}
	}
	return false
}

// hasActiveScopeGate reporta se a cadeia contém um [ScopeGate] com uma
// [authz.AuthoritySource] não-nil — i.e. tecto de autoridade EFECTIVO, não um gate sem
// fonte (que resolveria autoridade vazia). É a mesma lógica "gate activo, não só nome"
// de [hasActiveTaintGate].
func (m *Monitor) hasActiveScopeGate() bool {
	for _, h := range m.hooks {
		if g, ok := h.(ScopeGate); ok && g.authority != nil {
			return true
		}
	}
	return false
}
