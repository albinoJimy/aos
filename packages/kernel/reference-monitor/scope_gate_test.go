package referencemonitor_test

import (
	"context"
	"reflect"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
)

// Capabilities de exemplo para os testes de escopo (AOS-071).
const (
	capRead  = "cap:doc.read"
	capWrite = "cap:doc.write"
	capAdmin = "cap:admin.grant"
)

// Sujeitos da cadeia on-behalf-of.
const (
	humAlice     = "human:alice"
	humBob       = "human:bob"
	agResearcher = "agent:researcher"
	agSummarizer = "agent:summarizer"
	agPrivileged = "agent:privileged"
	scopeToolID  = "tool.scope.echo"
)

// newScopeSource constrói a fonte de autoridade determinista dos testes:
//   - alice: {read, write, admin}  (utilizador com muita autoridade)
//   - bob:   {read}                 (outro utilizador — para confused deputy)
//   - classe researcher: {read, write}  (não concede admin)
//   - classe summarizer: {read}          (sub-agente ainda mais restrito)
//   - classe privileged: {read, write, admin}  (classe-folha forjável usada para
//     provar que o eixo CLASSE deriva do principal AUTENTICADO, não da cadeia)
func newScopeSource() authz.AuthoritySource {
	return authz.NewStaticAuthoritySource().
		Set(humAlice, capRead, capWrite, capAdmin).
		Set(humBob, capRead).
		Set(agResearcher, capRead, capWrite).
		Set(agSummarizer, capRead).
		Set(agPrivileged, capRead, capWrite, capAdmin)
}

// newMonitorWithScopeGate constrói um RM com o ScopeGate na cadeia, uma tool
// registada e um sink em memória (auditoria disponível).
func newMonitorWithScopeGate(t *testing.T) (*referencemonitor.Monitor, *spySink) {
	t.Helper()
	sink := &spySink{}
	m := referencemonitor.New(
		referencemonitor.WithHooks(referencemonitor.DefaultHooksWithScope(newScopeSource())...),
		referencemonitor.WithEventSink(sink),
	)
	if err := m.Register(scopeToolID, func(_ context.Context, in []byte) ([]byte, error) {
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return m, sink
}

// scopedCall monta uma tool call com uma cadeia de delegação (root humano →
// agentes) e a capability/taint/autoridade-reclamada dados.
func scopedCall(capability, taint string, claimed []string, chain ...referencemonitor.DelegationHop) referencemonitor.Call {
	return referencemonitor.Call{
		RunID:      "run-scope",
		StepID:     "s1",
		ToolID:     scopeToolID,
		Capability: capability,
		Principal: referencemonitor.Principal{
			NHIID:           "nhi-1",
			AgentClass:      "researcher",
			DelegationChain: chain,
			Authority:       claimed,
		},
		Context: referencemonitor.CallContext{Taint: taint},
	}
}

// hop é um atalho para um elo da cadeia.
func hop(sub, actAs string) referencemonitor.DelegationHop {
	return referencemonitor.DelegationHop{Sub: sub, ActAs: actAs}
}

// aliceResearcher é a cadeia alice → researcher (escopo efectivo {read, write}).
func aliceResearcher() []referencemonitor.DelegationHop {
	return []referencemonitor.DelegationHop{hop(humAlice, agResearcher)}
}

// aliceResearcherSummarizer é a cadeia alice → researcher → summarizer
// (escopo efectivo {read}).
func aliceResearcherSummarizer() []referencemonitor.DelegationHop {
	return []referencemonitor.DelegationHop{
		hop(humAlice, agResearcher),
		hop(agResearcher, agSummarizer),
	}
}

// --- (1) Autorização: dentro do escopo permitida; fora negada -------------

func TestScope_Autorizacao_DentroPermitida_ForaNegada(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		capability string
		wantPermit bool
	}{
		{"read_dentro_da_interseccao", capRead, true},
		{"write_dentro_da_interseccao", capWrite, true},
		{"admin_fora_da_interseccao", capAdmin, false}, // alice tem admin, a classe nao
		{"capability_desconhecida", "cap:desconhecida", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, sink := newMonitorWithScopeGate(t)
			call := scopedCall(tc.capability, "trusted", nil, aliceResearcher()...)
			dec, err := m.Mediate(context.Background(), call)
			if err != nil {
				t.Fatalf("Mediate erro: %v", err)
			}
			if tc.wantPermit {
				if dec.Effect != referencemonitor.EffectPermit {
					t.Fatalf("quer permit, obteve %s (%s)", dec.Effect, dec.Reason)
				}
			} else {
				if dec.Effect != referencemonitor.EffectDeny {
					t.Fatalf("quer deny, obteve %s", dec.Effect)
				}
				if dec.DeniedBy != "scope" {
					t.Fatalf("quer DeniedBy=scope, obteve %q", dec.DeniedBy)
				}
			}
			// Escopo efectivo observável no audit: {read, write} em qualquer caso.
			rec := lastRecord(t, sink)
			if !reflect.DeepEqual(rec.Principal.Authority, []string{capRead, capWrite}) {
				t.Fatalf("escopo efectivo no audit = %v, quer [read write]", rec.Principal.Authority)
			}
		})
	}
}

// --- (2) Delegação (teste NEGATIVO): sub-agente não escala ----------------

func TestScope_Delegacao_SubAgenteNaoEscala(t *testing.T) {
	t.Parallel()
	m, sink := newMonitorWithScopeGate(t)

	// Sub-agente summarizer (escopo efectivo {read}) tenta "write", que o
	// principal-pai (researcher) tem mas ele NÃO — a cadeia só restringe.
	call := scopedCall(capWrite, "trusted", nil, aliceResearcherSummarizer()...)
	dec, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	if dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "scope" {
		t.Fatalf("sub-agente escalou: %s DeniedBy=%q", dec.Effect, dec.DeniedBy)
	}
	rec := lastRecord(t, sink)
	if !reflect.DeepEqual(rec.Principal.Authority, []string{capRead}) {
		t.Fatalf("escopo efectivo do sub-agente = %v, quer [read]", rec.Principal.Authority)
	}
	if rec.Effect != referencemonitor.EffectDeny {
		t.Fatalf("negacao nao foi auditada: %s", rec.Effect)
	}

	// "read" (dentro do escopo do sub-agente) continua permitido.
	ok, err := m.Mediate(context.Background(), scopedCall(capRead, "trusted", nil, aliceResearcherSummarizer()...))
	if err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	if ok.Effect != referencemonitor.EffectPermit {
		t.Fatalf("read devia ser permitido ao sub-agente, obteve %s", ok.Effect)
	}
}

func TestScope_Delegacao_ReclamacaoAcimaDoPrincipal_Negada(t *testing.T) {
	t.Parallel()
	m, sink := newMonitorWithScopeGate(t)

	// O sub-agente RECLAMA {read, write, admin} embora o seu escopo efectivo seja
	// {read}. A reivindicação acima do concedido é escalada explícita → negada.
	claimed := []string{capRead, capWrite, capAdmin}
	call := scopedCall(capRead, "trusted", claimed, aliceResearcherSummarizer()...)
	dec, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	if dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "scope" {
		t.Fatalf("reclamacao de escalada nao foi negada: %s DeniedBy=%q", dec.Effect, dec.DeniedBy)
	}
	rec := lastRecord(t, sink)
	// Mesmo na negação por escalada, o escopo efectivo (ground truth) é registado.
	if !reflect.DeepEqual(rec.Principal.Authority, []string{capRead}) {
		t.Fatalf("escopo efectivo no audit = %v, quer [read]", rec.Principal.Authority)
	}
}

// --- (3) Confused deputy: usar autoridade alheia é negado e auditado ------

func TestScope_ConfusedDeputy_AutoridadeAlheia_NegadaEAuditada(t *testing.T) {
	t.Parallel()
	m, sink := newMonitorWithScopeGate(t)

	// O agente researcher age on-behalf-of alice, mas é INDUZIDO a exercer "admin"
	// — uma autoridade que NÃO está na intersecção alice ∩ classe (a classe não a
	// concede). É o vector confused deputy: negado e registado.
	call := scopedCall(capAdmin, "trusted", nil, aliceResearcher()...)
	dec, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	if dec.Effect != referencemonitor.EffectDeny {
		t.Fatalf("confused deputy nao negado: %s", dec.Effect)
	}
	if dec.DeniedBy != "scope" {
		t.Fatalf("quer DeniedBy=scope, obteve %q", dec.DeniedBy)
	}

	// REGISTADO: existe um evento de negação atribuível (deny + scope + escopo).
	recs := sink.records
	if len(recs) == 0 {
		t.Fatal("nenhum evento de audit gravado")
	}
	rec := recs[len(recs)-1]
	if rec.Effect != referencemonitor.EffectDeny || rec.DeniedBy != "scope" {
		t.Fatalf("audit da confused deputy incorrecto: Effect=%s DeniedBy=%q", rec.Effect, rec.DeniedBy)
	}
	if rec.Capability != capAdmin {
		t.Fatalf("audit deve registar a capability tentada %q, obteve %q", capAdmin, rec.Capability)
	}
	if !reflect.DeepEqual(rec.Principal.Authority, []string{capRead, capWrite}) {
		t.Fatalf("escopo efectivo no audit = %v, quer [read write]", rec.Principal.Authority)
	}
}

// --- (4) Untrusted não eleva a autoridade efectiva ------------------------

func TestScope_Untrusted_NaoEleva(t *testing.T) {
	t.Parallel()
	m, sink := newMonitorWithScopeGate(t)

	// Um pedido originado em conteúdo UNTRUSTED não altera o escopo efectivo: a
	// intersecção deriva só da identidade. "admin" (fora do escopo) continua
	// negado, e o escopo efectivo registado é idêntico ao caso trusted.
	untrusted := scopedCall(capAdmin, "untrusted", nil, aliceResearcher()...)
	dec, err := m.Mediate(context.Background(), untrusted)
	if err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	if dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "scope" {
		t.Fatalf("untrusted devia continuar negado por escopo: %s DeniedBy=%q", dec.Effect, dec.DeniedBy)
	}

	// O escopo efectivo é o MESMO com ou sem taint (untrusted não eleva).
	untrustedScope := lastRecord(t, sink).Principal.Authority

	m2, sink2 := newMonitorWithScopeGate(t)
	if _, err := m2.Mediate(context.Background(), scopedCall(capAdmin, "trusted", nil, aliceResearcher()...)); err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	trustedScope := lastRecord(t, sink2).Principal.Authority

	if !reflect.DeepEqual(untrustedScope, trustedScope) {
		t.Fatalf("taint alterou o escopo efectivo: untrusted=%v trusted=%v", untrustedScope, trustedScope)
	}

	// Uma capability DENTRO do escopo continua permitida sob untrusted (untrusted é
	// DADOS legítimos; o escopo não muda). Prova que o taint não é usado pelo gate
	// de escopo para elevar NEM para restringir arbitrariamente.
	m3, _ := newMonitorWithScopeGate(t)
	ok, err := m3.Mediate(context.Background(), scopedCall(capRead, "untrusted", nil, aliceResearcher()...))
	if err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	if ok.Effect != referencemonitor.EffectPermit {
		t.Fatalf("read dentro do escopo devia ser permitido sob untrusted, obteve %s", ok.Effect)
	}
}

// --- Fail-closed: cadeia órfã e fonte ausente -----------------------------

func TestScope_FailClosed_OrfaESemFonte(t *testing.T) {
	t.Parallel()

	t.Run("cadeia_orfa_sem_raiz_humana", func(t *testing.T) {
		t.Parallel()
		m, _ := newMonitorWithScopeGate(t)
		// Raiz não-humana (sem prefixo "human:") ⇒ sem principal atribuível.
		call := scopedCall(capRead, "trusted", nil, hop("agent:x", agResearcher))
		dec, err := m.Mediate(context.Background(), call)
		if err != nil {
			t.Fatalf("Mediate erro: %v", err)
		}
		if dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "scope" {
			t.Fatalf("cadeia orfa devia ser negada: %s DeniedBy=%q", dec.Effect, dec.DeniedBy)
		}
	})

	t.Run("cadeia_vazia", func(t *testing.T) {
		t.Parallel()
		m, _ := newMonitorWithScopeGate(t)
		call := scopedCall(capRead, "trusted", nil) // sem cadeia
		dec, err := m.Mediate(context.Background(), call)
		if err != nil {
			t.Fatalf("Mediate erro: %v", err)
		}
		if dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "scope" {
			t.Fatalf("cadeia vazia devia ser negada: %s DeniedBy=%q", dec.Effect, dec.DeniedBy)
		}
	})

	t.Run("fonte_nil_fail_closed", func(t *testing.T) {
		t.Parallel()
		sink := &spySink{}
		m := referencemonitor.New(
			referencemonitor.WithHooks(referencemonitor.DefaultHooksWithScope(nil)...),
			referencemonitor.WithEventSink(sink),
		)
		if err := m.Register(scopeToolID, func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
			t.Fatalf("Register: %v", err)
		}
		dec, err := m.Mediate(context.Background(), scopedCall(capRead, "trusted", nil, aliceResearcher()...))
		if err != nil {
			t.Fatalf("Mediate erro: %v", err)
		}
		if dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "scope" {
			t.Fatalf("fonte nil devia negar fail-closed: %s DeniedBy=%q", dec.Effect, dec.DeniedBy)
		}
	})
}

// --- Composição das duas barreiras (AOS-069 + AOS-071) --------------------

func TestScope_ComposicaoComTaintGate(t *testing.T) {
	t.Parallel()
	sink := &spySink{}
	priv := referencemonitor.NewStaticPrivilegedSet(capWrite) // write é privilegiada
	m := referencemonitor.New(
		referencemonitor.WithHooks(referencemonitor.DefaultHooksWithTaintAndScope(priv, newScopeSource())...),
		referencemonitor.WithEventSink(sink),
	)
	if err := m.Register(scopeToolID, func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// write está DENTRO do escopo (alice ∩ researcher), mas é PRIVILEGIADA e a
	// autorização é UNTRUSTED ⇒ o TaintGate corta-a ANTES do ScopeGate.
	dec, err := m.Mediate(context.Background(), scopedCall(capWrite, "untrusted", nil, aliceResearcher()...))
	if err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	if dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "taint" {
		t.Fatalf("privilegiada+untrusted devia ser negada pelo taint: %s DeniedBy=%q", dec.Effect, dec.DeniedBy)
	}

	// write privilegiada mas TRUSTED e dentro do escopo ⇒ permitida (ambas passam).
	ok, err := m.Mediate(context.Background(), scopedCall(capWrite, "trusted", nil, aliceResearcher()...))
	if err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	if ok.Effect != referencemonitor.EffectPermit {
		t.Fatalf("write trusted dentro do escopo devia passar ambas as barreiras, obteve %s (%s)", ok.Effect, ok.Reason)
	}
}

// scopedCallWithClass é como scopedCall mas permite fixar a AgentClass do
// principal autenticado independentemente da cadeia — para exercitar o eixo CLASSE
// amarrado ao principal (AOS-071 F1).
func scopedCallWithClass(capability, taint, class string, claimed []string, chain ...referencemonitor.DelegationHop) referencemonitor.Call {
	c := scopedCall(capability, taint, claimed, chain...)
	c.Principal.AgentClass = class
	return c
}

// --- (F1) Eixo CLASSE amarrado ao principal autenticado, não à cadeia ------

// TestScope_ClasseAmarradaAoPrincipal_ClasseFolhaForjada garante que uma cadeia
// que declara uma classe-folha MAIS privilegiada do que a classe AUTENTICADA não
// eleva a autoridade: o eixo CLASSE deriva de Principal.AgentClass e é SEMPRE
// intersectado. Sem esta amarração (bug AOS-071 F1) effective = alice ∩ privileged
// concederia admin.
func TestScope_ClasseAmarradaAoPrincipal_ClasseFolhaForjada(t *testing.T) {
	t.Parallel()
	m, sink := newMonitorWithScopeGate(t)

	// Cadeia bem-formada alice → agent:privileged (classe-folha forjada com admin),
	// mas o principal AUTENTICADO é da classe "researcher" (sem admin).
	chain := []referencemonitor.DelegationHop{hop(humAlice, agPrivileged)}
	call := scopedCallWithClass(capAdmin, "trusted", "researcher", nil, chain...)
	dec, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	if dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "scope" {
		t.Fatalf("classe-folha forjada escalou: %s DeniedBy=%q", dec.Effect, dec.DeniedBy)
	}
	// Escopo efectivo = alice ∩ privileged ∩ researcher = {read, write} (sem admin).
	rec := lastRecord(t, sink)
	if !reflect.DeepEqual(rec.Principal.Authority, []string{capRead, capWrite}) {
		t.Fatalf("escopo efectivo = %v, quer [read write] (classe real impõe o tecto)", rec.Principal.Authority)
	}
}

// TestScope_CadeiaDegenerada_NaoColapsaParaAutoridadePlena garante que uma cadeia
// degenerada (raiz humana sem delegação a um agente) NÃO colapsa para a autoridade
// plena do utilizador. É negada fail-closed (elo sem ActAs = cadeia mal-formada).
func TestScope_CadeiaDegenerada_NaoColapsaParaAutoridadePlena(t *testing.T) {
	t.Parallel()
	m, _ := newMonitorWithScopeGate(t)

	// [{human:alice, ""}] — sem hop de agente. Antes de AOS-071 F1 colapsava para
	// subjects=[alice] ⇒ admin PERMIT (autoridade plena de alice). Agora: DENY.
	call := scopedCallWithClass(capAdmin, "trusted", "researcher", nil, hop(humAlice, ""))
	dec, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	if dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "scope" {
		t.Fatalf("cadeia degenerada concedeu autoridade plena: %s DeniedBy=%q", dec.Effect, dec.DeniedBy)
	}
}

// TestScope_ClasseIndeterminada_FailClosed garante que sem AgentClass resolúvel o
// gate nega (não há tecto de menor privilégio a impor).
func TestScope_ClasseIndeterminada_FailClosed(t *testing.T) {
	t.Parallel()
	m, _ := newMonitorWithScopeGate(t)

	// AgentClass vazia: mesmo uma capability que alice tem ({read}) é negada — sem
	// eixo CLASSE o escopo é indeterminável.
	call := scopedCallWithClass(capRead, "trusted", "", nil, aliceResearcher()...)
	dec, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	if dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "scope" {
		t.Fatalf("classe indeterminada devia negar fail-closed: %s DeniedBy=%q", dec.Effect, dec.DeniedBy)
	}
}

// --- (F2) Continuidade da cadeia + autoridade alheia cross-principal --------

// TestScope_CadeiaDescontinua_FailClosed garante que uma cadeia cujos elos não
// encadeiam (chain[i].ActAs != chain[i+1].Sub) é rejeitada — indício de forja.
func TestScope_CadeiaDescontinua_FailClosed(t *testing.T) {
	t.Parallel()
	m, _ := newMonitorWithScopeGate(t)

	// Descontinuidade: elo 0 delega a agent:researcher, mas elo 1 declara Sub=
	// agent:outro (≠). Cadeia mal-formada ⇒ fail-closed.
	chain := []referencemonitor.DelegationHop{
		hop(humAlice, agResearcher),
		hop("agent:outro", agSummarizer),
	}
	call := scopedCallWithClass(capRead, "trusted", "summarizer", nil, chain...)
	dec, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	if dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "scope" {
		t.Fatalf("cadeia descontínua devia negar fail-closed: %s DeniedBy=%q", dec.Effect, dec.DeniedBy)
	}
}

// TestScope_ConfusedDeputy_AutoridadeDeOutroPrincipal reforça o critério 4 com o
// vector cross-principal EXPLÍCITO: uma cadeia REALMENTE rooteada num utilizador
// pouco privilegiado (bob={read}) não concede a autoridade da sua classe além do
// que o utilizador-raiz permite. O escopo efectivo é a intersecção da cadeia real
// (bob ∩ researcher = {read}); tentar exercer "write" (que a classe tem mas bob
// NÃO) é negado e auditado — o agente não pode exercer autoridade que o principal
// humano da cadeia não possui.
func TestScope_ConfusedDeputy_AutoridadeDeOutroPrincipal(t *testing.T) {
	t.Parallel()
	m, sink := newMonitorWithScopeGate(t)

	bobResearcher := []referencemonitor.DelegationHop{hop(humBob, agResearcher)}

	// write ∈ classe researcher, mas ∉ bob ⇒ fora do escopo efectivo {read}.
	call := scopedCallWithClass(capWrite, "trusted", "researcher", nil, bobResearcher...)
	dec, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	if dec.Effect != referencemonitor.EffectDeny || dec.DeniedBy != "scope" {
		t.Fatalf("autoridade alheia (write de researcher sobre raiz bob) não negada: %s DeniedBy=%q", dec.Effect, dec.DeniedBy)
	}
	rec := lastRecord(t, sink)
	if rec.Effect != referencemonitor.EffectDeny || rec.DeniedBy != "scope" {
		t.Fatalf("negação cross-principal não auditada: Effect=%s DeniedBy=%q", rec.Effect, rec.DeniedBy)
	}
	if !reflect.DeepEqual(rec.Principal.Authority, []string{capRead}) {
		t.Fatalf("escopo efectivo cross-principal = %v, quer [read] (bob ∩ researcher)", rec.Principal.Authority)
	}

	// read (∈ bob ∩ researcher) continua permitido — o escopo é a intersecção real.
	ok, err := m.Mediate(context.Background(), scopedCallWithClass(capRead, "trusted", "researcher", nil, bobResearcher...))
	if err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	if ok.Effect != referencemonitor.EffectPermit {
		t.Fatalf("read dentro de bob ∩ researcher devia ser permitido, obteve %s", ok.Effect)
	}
}

// --- (F3) Guarda de drift do prefixo da raiz humana ------------------------

// TestScope_HumanRootPrefix_PinnedToDelegationSource fixa (behavioralmente) o
// prefixo canónico da raiz humana espelhado zero-dep em scope_gate.go, para
// detectar DRIFT face à fonte canónica delegation.HumanPrefix == "human:"
// (packages/platform/identity/delegation/chain.go). Não se importa o pacote
// delegation (evita o ciclo de layering kernel→platform); se a fonte evoluir,
// actualizar humanRootPrefix e este teste em conjunto. AOS-071 F3.
func TestScope_HumanRootPrefix_PinnedToDelegationSource(t *testing.T) {
	t.Parallel()

	// Fonte que reconhece uma raiz com o prefixo canónico exacto.
	src := authz.NewStaticAuthoritySource().
		Set("human:carol", capRead).
		Set(agResearcher, capRead, capWrite)
	sink := &spySink{}
	m := referencemonitor.New(
		referencemonitor.WithHooks(referencemonitor.DefaultHooksWithScope(src)...),
		referencemonitor.WithEventSink(sink),
	)
	if err := m.Register(scopeToolID, func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// (a) Prefixo EXACTO "human:" ⇒ raiz atribuível ⇒ read (∈ carol ∩ researcher)
	// permitido. Se humanRootPrefix divergisse de "human:", esta raiz ficaria órfã.
	okChain := []referencemonitor.DelegationHop{hop("human:carol", agResearcher)}
	dec, err := m.Mediate(context.Background(), scopedCallWithClass(capRead, "trusted", "researcher", nil, okChain...))
	if err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	if dec.Effect != referencemonitor.EffectPermit {
		t.Fatalf("raiz com prefixo canónico \"human:\" devia ser atribuível: %s (%s)", dec.Effect, dec.Reason)
	}

	// (b) Um prefixo VIZINHO (superstring) NÃO conta como raiz humana ⇒ órfã.
	badChain := []referencemonitor.DelegationHop{hop("humanoid:carol", agResearcher)}
	dec2, err := m.Mediate(context.Background(), scopedCallWithClass(capRead, "trusted", "researcher", nil, badChain...))
	if err != nil {
		t.Fatalf("Mediate erro: %v", err)
	}
	if dec2.Effect != referencemonitor.EffectDeny || dec2.DeniedBy != "scope" {
		t.Fatalf("prefixo não-canónico devia ser órfão (fail-closed): %s DeniedBy=%q", dec2.Effect, dec2.DeniedBy)
	}
}

// lastRecord devolve o último MediationRecord gravado (falha se não houver).
func lastRecord(t *testing.T, sink *spySink) referencemonitor.MediationRecord {
	t.Helper()
	if len(sink.records) == 0 {
		t.Fatal("nenhum MediationRecord gravado")
	}
	return sink.records[len(sink.records)-1]
}
