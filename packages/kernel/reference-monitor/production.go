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
	// contém um TaintGate WIRED (presente com authorizer não-nil). Manifesta-se se um
	// override via [WithHooks] remover a barreira control/data-plane ou a substituir por
	// um gate com authorizer nil. Fail-closed: construção nega. NOTA (AOS-219): esta é a
	// invariante ESTRUTURAL (presença); a EFICÁCIA (conjunto privileged não-vazio) é
	// aferida à parte por [Monitor.HasActiveTaintGate] / [ErrTaintGateInert].
	ErrTaintGateMissing = &MonitorError{Code: "E_TAINT_GATE_MISSING", msg: "cadeia de produção sem TaintGate wired (barreira control/data-plane ADR-005 ausente)"}

	// ErrTaintGateInert — a cadeia FINAL contém um TaintGate WIRED mas INERTE: o conjunto
	// privileged é VAZIO ([Monitor.HasActiveTaintGate] falso), logo nenhuma promoção de
	// escopo tainted é jamais barrada. [NewProductionHardenedTaint] recusa-o fail-closed —
	// a via ENDURECIDA no eixo do taint não arranca a alegar barreira control/data-plane
	// com um gate no-op. Distinto de [ErrTaintGateMissing] (gate AUSENTE): aqui o gate
	// existe mas não classifica nada como privilegiado (eixo AOS-183/DEF-808).
	ErrTaintGateInert = &MonitorError{Code: "E_TAINT_GATE_INERT", msg: "modo endurecido: TaintGate presente mas INERTE (conjunto privileged vazio ⇒ nenhuma promoção tainted é barrada); exige um PrivilegedAuthorizer não-vazio (AOS-183)"}

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

	if !m.hasWiredTaintGate() {
		return nil, ErrTaintGateMissing
	}
	if !m.hasDurableAudit() {
		return nil, ErrNoDurableAudit
	}
	return m, nil
}

// hasWiredTaintGate reporta se a cadeia mediadora contém um [TaintGate] com um
// [PrivilegedAuthorizer] NÃO-NIL — i.e. a barreira control/data-plane está
// estruturalmente PRESENTE (não removida por um override [WithHooks], não um gate com
// authorizer nil que nunca bloqueia). É a invariante ESTRUTURAL que [NewProduction]
// exige — impede que a via sancionada regrida para uma cadeia sem barreira. É mais forte
// que casar pelo Name(), mas NÃO afere EFICÁCIA: um gate wired com conjunto privileged
// VAZIO passa aqui e ainda assim é inerte (ver [Monitor.hasActiveTaintGate] e AOS-219).
func (m *Monitor) hasWiredTaintGate() bool {
	for _, h := range m.hooks {
		if g, ok := h.(TaintGate); ok && g.privileged != nil {
			return true
		}
	}
	return false
}

// hasActiveTaintGate reporta se a cadeia contém um [TaintGate] EFICAZ — presente E com um
// classificador que trata ALGUMA capability como privilegiada ([privilegedIsEffective]). É
// ESTRITAMENTE mais forte que [hasWiredTaintGate]: um gate wired mas com o conjunto
// privileged VAZIO é INERTE (nenhuma promoção de escopo tainted é jamais barrada) e NÃO
// conta como activo. É a correcção de AOS-219 (eficácia-vs-presença): o predicado de
// postura endurecida só é verdadeiro quando o enforcement de taint é EFECTIVO, não um
// placeholder no-op. Um authorizer nil ⇒ false (herdado de [privilegedIsEffective]).
func (m *Monitor) hasActiveTaintGate() bool {
	for _, h := range m.hooks {
		if g, ok := h.(TaintGate); ok && privilegedIsEffective(g.privileged) {
			return true
		}
	}
	return false
}

// HasActiveTaintGate expõe o predicado de EFICÁCIA da barreira control/data-plane para o
// composition root ápice DECLARAR a postura de forma HONESTA e VISÍVEL (AOS-219). Contrato:
//
//   - true  ⇒ o [TaintGate] está presente E o conjunto privileged é não-vazio: uma promoção
//     de escopo tainted é EFECTIVAMENTE barrada — o nó PODE alegar postura de taint endurecida.
//   - false ⇒ o gate está presente mas INERTE (conjunto privileged vazio) ou ausente: nenhuma
//     promoção tainted é barrada — o nó NÃO deve alegar postura de taint endurecida.
//
// [NewProduction]/[NewProductionSecure] arrancam com um gate wired-mas-inerte (o conjunto
// privileged real está DIFERIDO em AOS-183/DEF-808), pelo que o ápice DEVE consultar este
// predicado antes de alegar endurecimento — e adoptar [NewProductionHardenedTaint] (que recusa
// fail-closed o gate inerte) quando um classificador real e não-vazio existir.
func (m *Monitor) HasActiveTaintGate() bool { return m.hasActiveTaintGate() }

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

	// ErrEgressStub — a cadeia FINAL contém o [EgressStub] neutro, i.e. o slot de egress
	// está ocupado por um hook que PERMITE sempre: o default-deny de rede (AOS-067) está
	// inerte. Recusado. É a metade de SUBSTITUIÇÃO do eixo do egress; a metade de OMISSÃO
	// (slot vazio, nenhum hook de egress na cadeia) é [ErrEgressHookMissing] — as duas
	// juntas é que dão o "exige o hook de egress real" que esta via promete (AOS-355).
	ErrEgressStub = &MonitorError{Code: "E_EGRESS_STUB", msg: "produção-segura: cadeia final contém o EgressStub neutro (egress default-deny inactivo — hook de egress real AOS-067 ausente)"}

	// ErrEgressHookMissing — a cadeia FINAL não contém hook nenhum a ocupar o slot de
	// egress (nem sequer o stub). Manifesta-se quando um override [WithHooks] substitui
	// a cadeia base por inteiro e OMITE o egress — o caso que [ErrEgressStub] nunca
	// apanhava, porque testava a PRESENÇA DO STUB em vez da PRESENÇA DO HOOK (AOS-355).
	// Sem hook de egress a mediação não consulta allowlist nenhuma e toda a exfiltração
	// via tool "benigna" passa; recusado fail-closed. Sentinela PRÓPRIA e não reutilização
	// de [ErrEgressStub]: a causa é oposta (slot vazio vs. slot ocupado por um no-op) e a
	// correcção do chamador também — acrescentar o hook vs. substituir o stub.
	ErrEgressHookMissing = &MonitorError{Code: "E_EGRESS_HOOK_MISSING", msg: "produção-segura: cadeia final sem hook de egress (slot \"egress\" ausente — default-deny de rede AOS-067 não corre)"}

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
//   - não contiver hook nenhum no slot de egress ⇒ [ErrEgressHookMissing];
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
	// PRESENÇA, não só ausência-do-stub (AOS-355). A guarda acima só via a mutação por
	// SUBSTITUIÇÃO; esta vê a OMISSÃO. Corre DEPOIS para que uma cadeia com o stub
	// continue a diagnosticar-se como [ErrEgressStub] (causa mais específica).
	if !m.hasActiveEgressHook() {
		return nil, ErrEgressHookMissing
	}
	if !m.hasActiveScopeGate() {
		return nil, ErrScopeGateMissing
	}
	return m, nil
}

// NewProductionHardenedTaint é a via de produção ENDURECIDA no eixo do taint: herda TODAS as
// invariantes de [NewProductionSecure] (identidade/egress reais, ScopeGate com autoridade,
// TaintGate wired, auditoria durável) e ACRESCENTA a exigência de EFICÁCIA do TaintGate —
// recusa fail-closed com [ErrTaintGateInert] se o conjunto privileged for VAZIO (gate inerte).
//
// Fecha a metade de wiring de AOS-219/DEF-808 SEM inventar um conjunto privileged: é a costura
// que o composition root ápice adopta QUANDO existir um [PrivilegedAuthorizer] REAL e não-vazio
// (eixo AOS-183). Até lá o conjunto real está DIFERIDO e o ápice arranca por [NewProductionSecure]
// (gate wired-mas-inerte) DECLARANDO a postura de forma honesta via [Monitor.HasActiveTaintGate] —
// nunca alegando endurecimento de taint que não tem. O guard-test garante que a via não regride.
func NewProductionHardenedTaint(privileged PrivilegedAuthorizer, opts ...Option) (*Monitor, error) {
	m, err := NewProductionSecure(privileged, opts...)
	if err != nil {
		return nil, err
	}
	if !m.hasActiveTaintGate() {
		return nil, ErrTaintGateInert
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

// egressHookSlot é o nome canónico do slot de egress na cadeia de mediação (identity →
// policy → taint → scope → budget → egress → audit). É o que [EgressStub.Name] devolve e
// o que o hook REAL de AOS-067 (network.EgressHook) devolve — a costura pela qual um hook
// se declara competente pelo eixo do egress.
const egressHookSlot = "egress"

// hasActiveEgressHook reporta se a cadeia contém um hook a OCUPAR o slot de egress que
// não é o [EgressStub] neutro — i.e. o default-deny de rede (AOS-067) está estruturalmente
// PRESENTE, não removido por um override [WithHooks]. É a mesma lógica "gate activo, não
// só nome" de [hasActiveScopeGate], aplicada ao único eixo em que a guarda de
// [NewProductionSecure] testava a AUSÊNCIA DO STUB em vez da PRESENÇA DO HOOK (AOS-355):
// uma cadeia passada por [WithHooks] SEM egress nenhum satisfazia a via estrita.
//
// PORQUE CASA PELO NOME e não pelo tipo, ao contrário de [hasActiveScopeGate] e de
// [hasWiredTaintGate]: o [ScopeGate] e o [TaintGate] vivem NESTE package e o predicado
// pode inspeccionar-lhes os campos; o hook de egress real vive no substrato
// (substrate/sandbox/network) e a fronteira canónica de camadas — control-plane → kernel →
// platform/substrate — proíbe o kernel de o importar. O nome do slot é, por isso, o único
// sinal estrutural disponível aqui.
//
// LIMITE, declarado em vez de presumido: isto é PRESENÇA, não EFICÁCIA. Um hook que ocupe o
// slot "egress" e permita tudo passa este predicado, tal como um [TaintGate] wired mas com
// conjunto privileged vazio passa [hasWiredTaintGate]. A eficácia do egress é aferida pelo
// guard-test de comportamento do ápice (a negação atribuível a "egress"), não pela
// construção.
func (m *Monitor) hasActiveEgressHook() bool {
	for _, h := range m.hooks {
		if _, stub := h.(EgressStub); stub {
			continue
		}
		if h != nil && h.Name() == egressHookSlot {
			return true
		}
	}
	return false
}
