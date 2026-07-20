package pdp

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/authz"
)

// AOS-113 — Suite de testes de POLÍTICA (policy-as-code) sobre o PDP REAL.
//
// Esta suite COMPÕE o PDP de referência (motor Cedar + bundle assinado) — NÃO um
// fake — para provar, ligado ao gate 7:
//   - AC1: cobertura POR-REGRA (cada regra Cedar tem casos allow E deny; a suite
//     FALHA se uma regra ficar sem cobertura) — [TestPolicyRuleCoverage];
//   - AC3: a autoridade de um agente não excede a do seu principal (utilizador ∩
//     classe) — [TestDelegation_AgentNaoExcedeEscopoDoPrincipal];
//   - DoD: o deny-rate do PDP é COMPUTADO e REPORTADO — [TestPolicyDenyRate].
//
// AC2 (default-deny para capability desconhecida) e AC4 (política assinada/pinada)
// já estão cobertos e ligados ao gate por testes dedicados existentes
// (TestDecide_DefaultDeny_CapabilityAusente / TestDecide_ToolNovaFalhaFechada;
// TestReferenceBundle_Assinado / TestVerify_FailClosed / TestOpen_TamperedOnDisk).

// ruleCase é um caso da matriz de cobertura por-regra: um [Input] concreto,
// TAGGED com o @id da regra Cedar que exercita e o efeito esperado. Um caso allow
// é uma decisão que a regra PERMITE (o permit nomeia-a por @id — cross-check
// real); um caso deny é uma decisão que ALCANÇA a regra (passa o gate default-deny
// da allowlist para a mesma capability) mas é RECUSADA por uma guarda da regra
// falhar (region/taint/authority) — a negação é, assim, atribuível à regra.
type ruleCase struct {
	ruleID     string
	name       string
	in         Input
	wantEffect Effect
}

// ruleCoverageTable é a matriz allow/deny POR-REGRA da política de referência.
// Cada regra Cedar committada (aos_authz.cedar) tem, no mínimo, um caso allow e um
// caso deny. Os casos deny passam DELIBERADAMENTE o gate da allowlist para a mesma
// capability e falham só a guarda Cedar da regra — de modo que a negação pertença
// à regra, não ao gate default-deny da allowlist (coberto à parte, AC2).
//
// Adicionar uma regra nova a aos_authz.cedar SEM acrescentar aqui o seu par
// allow+deny faz [TestPolicyRuleCoverage] FALHAR (a regra aparece em RuleIDs() mas
// sem cobertura) — é essa a prova viva do AC1.
func ruleCoverageTable() []ruleCase {
	mut := func(base Input, f func(*Input)) Input {
		in := base
		f(&in)
		return in
	}
	return []ruleCase{
		// --- allow_http_post: permite cap:http.post em região eu, não-untrusted ---
		{
			ruleID:     "allow_http_post",
			name:       "http_post_allow_eu_trusted",
			in:         httpPost(),
			wantEffect: Permit,
		},
		{
			ruleID:     "allow_http_post",
			name:       "http_post_deny_regiao_nao_eu",
			in:         mut(httpPost(), func(i *Input) { i.Resource.Region = "us" }),
			wantEffect: Deny,
		},
		{
			ruleID:     "allow_http_post",
			name:       "http_post_deny_taint_untrusted",
			in:         mut(httpPost(), func(i *Input) { i.Context.Taint = "untrusted" }),
			wantEffect: Deny,
		},
		// --- allow_fs_read: permite cap:fs.read com a capability na authority -----
		{
			ruleID:     "allow_fs_read",
			name:       "fs_read_allow",
			in:         fsRead(),
			wantEffect: Permit,
		},
		{
			// Passa o gate (agent-worker lista cap:fs.read) mas a authority NÃO contém
			// cap:fs.read: a guarda da regra Cedar falha → deny atribuível à regra.
			ruleID:     "allow_fs_read",
			name:       "fs_read_deny_sem_authority",
			in:         mut(fsRead(), func(i *Input) { i.Principal.Authority = []string{"cap:http.post"} }),
			wantEffect: Deny,
		},
	}
}

// TestPolicyRuleCoverage é o CERNE do AC1: enumera as regras Cedar REAIS em vigor
// (p.RuleIDs()) e exige que CADA UMA tenha, na matriz de cobertura, pelo menos um
// caso allow E um caso deny que o PDP real confirme. Falha fail-closed se:
//   - uma regra compilada ficar sem caso allow ou sem caso deny (regra descoberta,
//     cobertura em falta) — o cenário que uma regra nova sem par deny dispararia;
//   - um caso allow não for permitido, ou o seu permit não nomear a regra (@id);
//   - um caso deny não for negado;
//   - um caso referenciar um @id que não consta de RuleIDs() (tag órfã/typo).
func TestPolicyRuleCoverage(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)
	ctx := context.Background()

	ruleIDs := p.RuleIDs()
	if len(ruleIDs) == 0 {
		t.Fatal("RuleIDs() vazio: sem regras compiladas (bundle de referência devia ter regras)")
	}
	known := make(map[string]bool, len(ruleIDs))
	for _, id := range ruleIDs {
		known[id] = true
	}

	allowByRule := make(map[string]int)
	denyByRule := make(map[string]int)

	for _, c := range ruleCoverageTable() {
		if !known[c.ruleID] {
			t.Errorf("caso %q referencia regra %q que NÃO consta de RuleIDs() %v (tag órfã?)",
				c.name, c.ruleID, ruleIDs)
			continue
		}
		d, err := p.Decide(ctx, c.in)
		if err != nil {
			t.Fatalf("caso %q: Decide erro inesperado: %v", c.name, err)
		}
		if d.Effect != c.wantEffect {
			t.Errorf("caso %q: Effect=%q, esperava %q (reason=%q)", c.name, d.Effect, c.wantEffect, d.Reason)
			continue
		}
		switch c.wantEffect {
		case Permit:
			// Cross-check REAL: o permit da política nomeia a regra pelo seu @id, logo
			// o caso allow exercita mesmo ESTA regra (não outra qualquer).
			if !contains(d.Reason, c.ruleID) {
				t.Errorf("caso allow %q: reason=%q não nomeia a regra %q", c.name, d.Reason, c.ruleID)
			}
			allowByRule[c.ruleID]++
		case Deny:
			if !contains(d.Reason, "default-deny") {
				t.Errorf("caso deny %q: reason=%q devia indicar default-deny", c.name, d.Reason)
			}
			denyByRule[c.ruleID]++
		}
	}

	// AC1: cada regra COMPILADA tem cobertura allow E deny. Enumerar RuleIDs() (e
	// não uma lista à mão) é o que torna a falha REAL: uma regra nova sem par de
	// cobertura entra neste laço com contagem 0 e avermelha a suite.
	for _, id := range ruleIDs {
		if allowByRule[id] == 0 {
			t.Errorf("REGRA SEM COBERTURA ALLOW: %q (AC1 exige ≥1 caso allow por regra)", id)
		}
		if denyByRule[id] == 0 {
			t.Errorf("REGRA SEM COBERTURA DENY: %q (AC1 exige ≥1 caso deny por regra)", id)
		}
	}
}

// TestDelegation_AgentNaoExcedeEscopoDoPrincipal cobre o AC3: um agente NÃO excede
// o escopo do seu principal (utilizador ∩ classe de agente). O PDP de referência
// não avalia a DelegationChain (input.go §9: a intersecção é resolvida A MONTANTE,
// no RM/identidade); este teste COMPÕE honestamente as duas camadas:
//
//	PARTE A — enforcement capability-scoped NO PDP: uma classe mais RESTRITA não
//	exerce uma capability de uma classe mais ampla (o gate default-deny da
//	allowlist é keyed por agent_class).
//	PARTE B — a intersecção user ∩ classe a MONTANTE (authz.FoldScope /
//	CheckNoEscalation): o escopo efectivo NUNCA amplia, uma reclamação fora dele
//	ESCALA (rejeitada), e o PDP alimentado com o escopo efectivo nega a capability
//	que ficou de fora — mesmo para uma classe que, isolada, a permitiria.
func TestDelegation_AgentNaoExcedeEscopoDoPrincipal(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)
	ctx := context.Background()

	// PARTE A — o escopo do agente é limitado pela sua CLASSE no PDP. agent-reader
	// não lista cap:http.post: pedi-la é negado pela allowlist, mesmo com autoridade,
	// região e taint que de outro modo permitiriam.
	reader := httpPost()
	reader.Principal.AgentClass = "agent-reader"
	d, err := p.Decide(ctx, reader)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Effect != Deny {
		t.Fatalf("agent-reader/cap:http.post: Effect=%q, esperava Deny (fora do escopo da classe)", d.Effect)
	}
	if !contains(d.Reason, "agent-reader") || !contains(d.Reason, "allowlist") {
		t.Errorf("reason=%q devia nomear a classe restrita e a allowlist", d.Reason)
	}

	// PARTE B — intersecção user ∩ classe a montante. O utilizador só delega
	// cap:fs.read; a classe permitiria também cap:http.post. O escopo EFECTIVO é a
	// dobra de intersecções — e nunca amplia para lá do que o utilizador concede.
	user := []string{"cap:fs.read"}
	class := []string{"cap:fs.read", "cap:http.post"}
	eff := authz.FoldScope(user, class)
	if len(eff) != 1 || eff[0] != "cap:fs.read" {
		t.Fatalf("FoldScope(user,class)=%v, esperava [cap:fs.read] (a intersecção não amplia)", eff)
	}

	// Um agente que RECLAME cap:http.post (fora de user ∩ classe) tenta escalar: é
	// rejeitado a montante com ErrScopeEscalation, ANTES de qualquer decisão do PDP.
	if err := authz.CheckNoEscalation([]string{"cap:http.post"}, eff); !errors.Is(err, authz.ErrScopeEscalation) {
		t.Fatalf("reclamar cap:http.post fora do escopo efectivo devia dar ErrScopeEscalation, obtive %v", err)
	}
	// Uma reclamação DENTRO do escopo efectivo não escala.
	if err := authz.CheckNoEscalation([]string{"cap:fs.read"}, eff); err != nil {
		t.Fatalf("reclamar cap:fs.read (dentro do escopo) não devia escalar, obtive %v", err)
	}

	// O PDP alimentado com o escopo EFECTIVO (não com uma autoridade forjada mais
	// ampla) decide dentro dele: cap:fs.read permite...
	scoped := fsRead()
	scoped.Principal.Authority = append([]string(nil), eff...)
	sd, err := p.Decide(ctx, scoped)
	if err != nil {
		t.Fatalf("Decide (scoped fs.read): %v", err)
	}
	if sd.Effect != Permit {
		t.Fatalf("cap:fs.read dentro do escopo efectivo: Effect=%q, esperava Permit", sd.Effect)
	}

	// ...e cap:http.post, FORA do escopo efectivo, é negada mesmo pela classe
	// agent-worker que a listaria: a authority delegada (user ∩ classe) não a inclui,
	// logo a guarda Cedar da regra falha. O agente não excede a autoridade do
	// principal, ainda que a sua classe, isolada, a concedesse.
	forged := httpPost() // agent-worker, cap:http.post, região/taint que permitiriam
	forged.Principal.Authority = append([]string(nil), eff...)
	fd, err := p.Decide(ctx, forged)
	if err != nil {
		t.Fatalf("Decide (forged http.post): %v", err)
	}
	if fd.Effect != Deny {
		t.Fatalf("cap:http.post fora do escopo efectivo do principal: Effect=%q, esperava Deny", fd.Effect)
	}
}

// denyRateMatrix é a matriz de decisão sobre a qual o deny-rate é computado:
// reúne os casos de cobertura por-regra (allow+deny) e casos de default-deny
// (capability fora da allowlist, taint, região) — um espectro representativo das
// classes de decisão por tool call.
func denyRateMatrix() []Input {
	mut := func(base Input, f func(*Input)) Input {
		in := base
		f(&in)
		return in
	}
	inputs := make([]Input, 0)
	for _, c := range ruleCoverageTable() {
		inputs = append(inputs, c.in)
	}
	// Casos adicionais de negação por default-deny (não atribuíveis a uma regra):
	// capability desconhecida e capability fora da allowlist da classe.
	inputs = append(inputs,
		mut(httpPost(), func(i *Input) { i.Capability = "cap:http.get"; i.Principal.Authority = []string{"cap:http.get"} }),
		mut(httpPost(), func(i *Input) { i.Capability = "cap:email.send"; i.Principal.Authority = []string{"cap:email.send"} }),
		mut(httpPost(), func(i *Input) { i.Principal.AgentClass = "classe-inexistente" }),
	)
	return inputs
}

// TestPolicyDenyRate COMPUTA e REPORTA o deny-rate do PDP (DoD): a fracção de
// decisões Deny sobre a matriz de decisão representativa. Emite uma linha marcada
// e estável (AOS_POLICY_DENY_RATE <json>), no molde do AOS_REPLAY_REPORT, que o
// gate 7 capta via `go test -v`. Não instrumenta produção — é uma métrica derivada
// da suite. Assevera invariantes de sanidade (0 < deny-rate < 1: a matriz tem
// mesmo casos allow E deny) para o relatório nunca ser vacuoso.
func TestPolicyDenyRate(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)
	ctx := context.Background()

	matrix := denyRateMatrix()
	total := len(matrix)
	if total == 0 {
		t.Fatal("matriz de decisão vazia: deny-rate não mensurável")
	}
	denies := 0
	for _, in := range matrix {
		d, err := p.Decide(ctx, in)
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if d.Effect == Deny {
			denies++
		}
	}
	rate := float64(denies) / float64(total)

	// Sanidade anti-vacuidade: a matriz mistura mesmo allow e deny.
	if denies == 0 {
		t.Fatal("deny-rate=0: a matriz não exercita nenhuma negação (relatório vacuoso)")
	}
	if denies == total {
		t.Fatal("deny-rate=1: a matriz não exercita nenhum permit (relatório vacuoso)")
	}

	// EMISSÃO: uma única linha marcada, estável e consumível pelo gate.
	fmt.Printf("AOS_POLICY_DENY_RATE {\"denies\":%d,\"total\":%d,\"deny_rate\":%.4f,\"policy_version\":%q}\n",
		denies, total, rate, p.Version())
	t.Logf("PDP deny-rate = %d/%d = %.2f%% (policy_version=%s)", denies, total, rate*100, p.Version())
}
