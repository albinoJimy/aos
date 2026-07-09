package pdp

import (
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	rm "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-007 — testes da capability allowlist default-deny.
//
// A allowlist de referência (policies/capabilities/allowlist.json) concede:
//   - agent-worker      → {cap:http.post, cap:fs.read}
//   - agent-reader      → {cap:fs.read}
//   - agent-break-glass → wildcard JUSTIFICADO (qualquer capability)

// TestAllowlist_Permits_TabelaVerdade exercita directamente o gate default-deny
// sobre a allowlist COMPILADA do bundle assinado de referência.
func TestAllowlist_Permits_TabelaVerdade(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)
	al := p.engine.allow

	tests := []struct {
		name      string
		class     string
		cap       string
		wantAllow bool
	}{
		{"worker_cap_listada_http", "agent-worker", "cap:http.post", true},
		{"worker_cap_listada_fs", "agent-worker", "cap:fs.read", true},
		{"worker_cap_nao_listada", "agent-worker", "cap:http.get", false},
		{"reader_cap_listada", "agent-reader", "cap:fs.read", true},
		{"reader_cap_nao_listada", "agent-reader", "cap:http.post", false},
		{"break_glass_wildcard_justificado", "agent-break-glass", "cap:qualquer.coisa", true},
		{"classe_desconhecida_deny", "classe-inexistente", "cap:fs.read", false},
		{"classe_vazia_deny", "", "cap:fs.read", false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ok, reason := al.permits(tc.class, tc.cap)
			if ok != tc.wantAllow {
				t.Fatalf("permits(%q,%q)=%v reason=%q, esperava %v", tc.class, tc.cap, ok, reason, tc.wantAllow)
			}
			if !ok && !contains(reason, "default-deny") && !contains(reason, "default-deny)") {
				// Toda a negação da allowlist deve ser inequívoca no audit.
				if !contains(reason, "default-deny") {
					t.Errorf("motivo de negacao %q nao contem 'default-deny'", reason)
				}
			}
		})
	}
}

// TestAllowlist_Classes cobre o helper de introspecção Classes(): devolve as
// classes conhecidas ORDENADAS a partir do bundle assinado de referência, e nil
// para uma allowlist nil (fail-safe, sem panic). Não participa na decisão, mas
// fixa o contrato de regressão do auxiliar exportado.
func TestAllowlist_Classes(t *testing.T) {
	t.Parallel()

	// Allowlist nil ⇒ nil (nunca entra em panic).
	var nilAL *Allowlist
	if got := nilAL.Classes(); got != nil {
		t.Errorf("Classes() de allowlist nil = %v, esperava nil", got)
	}

	// Bundle de referência: as três classes conhecidas, ordenadas.
	p := mustOpen(t)
	got := p.engine.allow.Classes()
	want := []string{"agent-break-glass", "agent-reader", "agent-worker"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Classes() = %v, esperava %v (ordenadas)", got, want)
	}
}

// TestDecide_DefaultDeny_CapabilityAusente cobre o critério nuclear (AC#2): uma
// capability ausente da allowlist da classe é NEGADA por omissão, mesmo com
// autoridade, região e taint que de outro modo permitiriam.
func TestDecide_DefaultDeny_CapabilityAusente(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)

	in := httpPost() // agent-worker, contexto que permite
	in.Capability = "cap:http.get"
	in.Principal.Authority = []string{"cap:http.get"} // até com autoridade...

	d, err := p.Decide(context.Background(), in)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Effect != Deny {
		t.Fatalf("Effect=%q, esperava Deny (capability fora da allowlist)", d.Effect)
	}
	if !contains(d.Reason, "allowlist") || !contains(d.Reason, "default-deny") {
		t.Errorf("reason=%q, esperava referencia a allowlist + default-deny", d.Reason)
	}
	if d.PolicyVersion != p.Version() {
		t.Errorf("PolicyVersion=%q, esperava %q", d.PolicyVersion, p.Version())
	}
}

// TestDecide_ToolNovaFalhaFechada é o teste de "tool nova falha fechada" (AC#5):
// uma capability recém-introduzida, ainda SEM entrada na allowlist, é negada até
// ser explicitamente permitida por política re-assinada — o oposto da blocklist
// que falhava aberta.
func TestDecide_ToolNovaFalhaFechada(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)

	in := httpPost()
	in.Capability = "cap:email.send" // "tool nova", nunca allowlisted
	in.Principal.Authority = []string{"cap:email.send"}

	d, _ := p.Decide(context.Background(), in)
	if d.Effect != Deny {
		t.Fatalf("tool nova devia falhar fechada, obtive %q (%s)", d.Effect, d.Reason)
	}
	// Nem sequer a classe break-glass (wildcard) foi pedida: sem entrada ⇒ deny.
	if d.Permitted() {
		t.Error("Permitted() devia ser false para tool nova nao listada")
	}
}

// TestDecide_AllowExplicitoPorPoliticaAssinada cobre o AC#3/AC#4: uma capability
// que CONSTA da allowlist assinada é permitida quando as demais regras Cedar
// passam. Distingue-se por classe: a mesma capability é negada a uma classe que
// não a lista.
func TestDecide_AllowExplicitoPorPoliticaAssinada(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)
	ctx := context.Background()

	// agent-worker lista cap:http.post ⇒ permit (região eu, trusted, autoridade).
	permit, err := p.Decide(ctx, httpPost())
	if err != nil || permit.Effect != Permit {
		t.Fatalf("agent-worker/cap:http.post devia permitir: effect=%q err=%v", permit.Effect, err)
	}

	// agent-reader NÃO lista cap:http.post ⇒ deny pela allowlist, apesar de tudo o
	// resto permitir.
	reader := httpPost()
	reader.Principal.AgentClass = "agent-reader"
	deny, _ := p.Decide(ctx, reader)
	if deny.Effect != Deny {
		t.Fatalf("agent-reader/cap:http.post devia ser negado pela allowlist, obtive %q", deny.Effect)
	}
	if !contains(deny.Reason, "agent-reader") {
		t.Errorf("reason=%q devia nomear a classe negada", deny.Reason)
	}
}

// TestDecide_WildcardExigeJustificacao assevera a regra "sem wildcards perigosos
// por omissão": um wildcard COM justificação concede; um wildcard SEM
// justificação é ignorado (não concede nada).
func TestDecide_WildcardExigeJustificacao(t *testing.T) {
	t.Parallel()

	// Wildcard justificado (classe de referência agent-break-glass) concede qualquer
	// capability no gate.
	p := mustOpen(t)
	if ok, _ := p.engine.allow.permits("agent-break-glass", "cap:tudo.pode"); !ok {
		t.Error("wildcard JUSTIFICADO devia conceder")
	}

	// Wildcard SEM justificação: parseAllowlist ignora-o ⇒ a classe não concede nada.
	doc := []byte(`{"schema_version":1,"classes":{"perigosa":{"capabilities":[{"cap":"*"}]}}}`)
	al, err := parseAllowlist(map[string][]byte{capabilitiesDir + "/x.json": doc})
	if err != nil {
		t.Fatalf("parseAllowlist: %v", err)
	}
	if ok, _ := al.permits("perigosa", "cap:o.que.for"); ok {
		t.Error("wildcard SEM justificacao NAO devia conceder (sem allow implicito)")
	}
}

// TestDecide_Fuzz_ZeroFalsoAllow é o fuzz de capabilities (AC#2/DoD): milhares de
// capabilities aleatórias NÃO-listadas são todas negadas — 0 falso allow. Usa um
// PRNG semeado para ser determinístico e reproduzível no CI.
func TestDecide_Fuzz_ZeroFalsoAllow(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)
	ctx := context.Background()
	rng := rand.New(rand.NewSource(0xA05007))

	// Conjunto de capabilities REALMENTE concedidas a agent-worker: qualquer
	// aleatória fora deste conjunto deve ser negada.
	listed := map[string]struct{}{"cap:http.post": {}, "cap:fs.read": {}}

	const n = 3000
	falseAllows := 0
	for i := 0; i < n; i++ {
		cap := randomCap(rng)
		if _, ok := listed[cap]; ok {
			continue // colisão rara com uma capability legítima: não é "indevido"
		}
		in := httpPost() // classe agent-worker, contexto que de outro modo permitiria
		in.Capability = cap
		in.Principal.Authority = []string{cap} // dá-lhe autoridade: só a allowlist a barra
		d, _ := p.Decide(ctx, in)
		if d.Permitted() {
			falseAllows++
			t.Errorf("FALSO ALLOW para capability aleatoria %q", cap)
		}
	}
	if falseAllows != 0 {
		t.Fatalf("fuzz: %d falsos allow em %d (esperava 0)", falseAllows, n)
	}
}

// randomCap gera uma capability pseudo-aleatória (comprimento e alfabeto
// variados, incluindo prefixo cap: e formas degeneradas).
func randomCap(rng *rand.Rand) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz.:_-*/0123456789 "
	n := rng.Intn(24)
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[rng.Intn(len(alphabet))]
	}
	s := string(b)
	if rng.Intn(2) == 0 {
		s = "cap:" + s
	}
	return s
}

// FuzzDecide_CapabilityNuncaEscapaAllowlist é o fuzz nativo (go test -fuzz) do
// gate: para QUALQUER (classe, capability) gerados, um permit implica sempre que
// a allowlist concede essa (classe, capability). Nunca há allow sem allowlist.
func FuzzDecide_CapabilityNuncaEscapaAllowlist(f *testing.F) {
	p, err := Open("policies")
	if err != nil {
		f.Fatalf("Open: %v", err)
	}
	f.Add("agent-worker", "cap:http.post")
	f.Add("agent-reader", "cap:http.post")
	f.Add("", "cap:fs.read")
	f.Add("agent-break-glass", "cap:whatever")
	f.Add("classe-x", "cap:y")

	ctx := context.Background()
	f.Fuzz(func(t *testing.T, class, capb string) {
		if capb == "" {
			return // capability vazia é E_MALFORMED_REQUEST, coberto noutro teste
		}
		in := httpPost()
		in.Principal.AgentClass = class
		in.Capability = capb
		in.Principal.Authority = []string{capb}
		d, _ := p.Decide(ctx, in)
		if d.Permitted() {
			if ok, _ := p.engine.allow.permits(class, capb); !ok {
				t.Fatalf("permit sem allowlist: classe=%q capability=%q", class, capb)
			}
		}
	})
}

// TestAllowlist_AdicaoExigeReAssinatura assevera o AC#4/DoD: alterar a allowlist
// no disco SEM re-assinar invalida o bundle (fail-closed) — adicionar uma
// capability exige política re-assinada, nunca um allow implícito.
func TestAllowlist_AdicaoExigeReAssinatura(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, priv := newKeypair(t)
	writeSignedDir(t, dir, "1.0.0", priv)

	// Sanidade: bundle intacto carrega.
	if _, err := Open(dir); err != nil {
		t.Fatalf("bundle intacto devia carregar: %v", err)
	}

	// Adiciona uma capability à allowlist SEM re-assinar: content_hash deixa de bater.
	tampered := []byte(`{"schema_version":1,"classes":{"agent-worker":{"capabilities":[{"cap":"cap:http.post"},{"cap":"cap:admin.root"}]}}}`)
	if err := os.WriteFile(filepath.Join(dir, capabilitiesDir, "allowlist.json"), tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("allowlist adulterada sem re-assinatura devia falhar fail-closed")
	} else if !isSignatureErr(err) {
		t.Fatalf("esperava ErrSignatureInvalid, obtive %v", err)
	}
}

// isSignatureErr é um helper local (evita import de errors só para isto).
func isSignatureErr(err error) bool {
	return err != nil && contains(err.Error(), ErrSignatureInvalid.Code)
}

// mediationPayloadView é a vista do payload do evento de mediação usada para
// asseverar que a negação da allowlist é auditada com capability + principal.
type mediationPayloadView struct {
	Decision   string `json:"decision"`
	Reason     string `json:"reason"`
	DeniedBy   string `json:"denied_by"`
	Capability string `json:"capability"`
	Principal  struct {
		NHIID      string   `json:"nhi_id"`
		AgentClass string   `json:"agent_class"`
		Authority  []string `json:"authority"`
	} `json:"principal"`
}

// TestIntegration_RM_AllowlistDeny_Auditado é a integração RM (AC#2/DoD): uma
// capability fora da allowlist é negada pela mediação e o evento de negação
// regista capability + principal (nhi_id, agent_class) — "quem" pediu "o quê".
func TestIntegration_RM_AllowlistDeny_Auditado(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()

	m := buildRM(t, store)
	var dispatched bool
	_ = m.Register("tool.http", func(_ context.Context, in []byte) ([]byte, error) {
		dispatched = true
		return in, nil
	})

	// agent-reader não lista cap:http.post ⇒ negado pela allowlist no PDP.
	call := rm.Call{
		RequestID: "req-al", RunID: "run-allowlist-deny", StepID: "s1",
		ToolID: "tool.http", Capability: "cap:http.post",
		Resource:  rm.Resource{Type: "url", Value: "https://api.example.com/x", Region: "eu"},
		Principal: rm.Principal{NHIID: "nhi-9", AgentClass: "agent-reader", Authority: []string{"cap:http.post"}},
		Context:   rm.CallContext{Taint: "trusted"},
		Input:     []byte("body"),
	}

	d, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != rm.EffectDeny {
		t.Fatalf("esperava deny da allowlist, obtive %q (%s)", d.Effect, d.Reason)
	}
	if d.DeniedBy != "policy" {
		t.Errorf("DeniedBy=%q, esperava policy", d.DeniedBy)
	}
	if dispatched {
		t.Error("a tool NAO devia ser despachada num deny da allowlist")
	}

	ev := readOne(t, store, "run-allowlist-deny")
	if ev.Type != rm.EventTypeDenied {
		t.Errorf("Type=%q, esperava %q", ev.Type, rm.EventTypeDenied)
	}
	var pl mediationPayloadView
	if err := json.Unmarshal(ev.Payload, &pl); err != nil {
		t.Fatalf("payload invalido: %v", err)
	}
	if pl.Decision != "deny" {
		t.Errorf("decision=%q, esperava deny", pl.Decision)
	}
	if pl.Capability != "cap:http.post" {
		t.Errorf("capability=%q no evento, esperava cap:http.post", pl.Capability)
	}
	if pl.Principal.NHIID != "nhi-9" || pl.Principal.AgentClass != "agent-reader" {
		t.Errorf("principal no evento=%+v, esperava nhi-9/agent-reader", pl.Principal)
	}
	if !contains(pl.Reason, "allowlist") {
		t.Errorf("reason auditado=%q devia referir a allowlist", pl.Reason)
	}
}
