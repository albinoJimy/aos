package referencemonitor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// AOS-538 — o gate de taint decide pelo DESTINO da tool (emenda de 2026-10-19 ao ADR-034).
//
// Os destinos são os do manifesto de produção (deploy/server/model-tools/tools.json) e do dos
// demos (deploy/node/dev-hardened), mais as formas que a regra tem de recusar: um eixo implícito,
// um efeito declarado com a capability fora do conjunto, e uma call que diz o nome de uma tool de
// leitura com a capability de outra.

func aos538Destinos() ToolDestinations {
	return NewToolDestinations(map[string]ToolDestination{
		"doc_read":   {Capability: "cap:fs.read", Egress: "none", Mutation: "none", Reversibility: "reversible"},
		"ocr_read":   {Capability: "cap:fs.read", Egress: "external", Mutation: "none", Reversibility: "reversible"},
		"video_read": {Capability: "cap:fs.read", Egress: "external", Mutation: "none", Reversibility: "reversible"},
		"web_post":   {Capability: "cap:http.post", Egress: "external", Mutation: "mutates"},
		// Eixo implícito: o egress não foi declarado ⇒ a regra de antes.
		"doc_read_implicito": {Capability: "cap:fs.read", Mutation: "none", Reversibility: "reversible"},
		// Efeito declarado com a capability fora de AOS_PRIVILEGED_CAPS.
		"fs_write": {Capability: "cap:fs.write", Egress: "none", Mutation: "mutates", Reversibility: "reversible"},
		// Leitura local com uma capability que ESTÁ no conjunto armado: a regra nova liberta-a.
		"http_get_local": {Capability: "cap:http.post", Egress: "none", Mutation: "none", Reversibility: "reversible"},
	})
}

// aos538Mediar medeia UMA call com o TaintGate armado com `caps` e o destino das tools, e devolve
// a decisão e o payload JSON do evento de mediação que o Event Store gravaria.
func aos538Mediar(t *testing.T, caps []string, dests ToolDestinations, tool, capability, rotulo, forjado string) (Decision, mediationPayload) {
	t.Helper()
	store := &storeQueCaptura{}
	priv := NewDestinationPrivileged(NewStaticPrivilegedSet(caps...), dests)
	m := New(WithHooks(DefaultHooksWithTaint(priv)...), WithEventSink(&eventStoreSink{store: store}))
	if err := m.Register(tool, func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	dec, err := m.Mediate(context.Background(), Call{
		RunID: "run-538", StepID: "s1", ToolID: tool, Capability: capability,
		Principal: Principal{NHIID: "nhi-538"},
		Context:   CallContext{Taint: rotulo, TaintPrivilege: forjado},
	})
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	var p mediationPayload
	if err := json.Unmarshal(store.ultimo, &p); err != nil {
		t.Fatalf("payload ilegivel: %v (%s)", err, store.ultimo)
	}
	return dec, p
}

func aos538Caps() []string { return []string{"cap:http.post", "cap:fs.read"} }

func TestAOS538_GateDecidePeloDestino(t *testing.T) {
	casos := []struct {
		nome, tool, capability string
		rotulo                 string
		querDeny               bool
		querRazao              string
	}{
		// A regra nova: leitura local passa com o contexto untrusted, mesmo com cap:fs.read armada.
		{"doc_read-untrusted-passa", "doc_read", "cap:fs.read", taint.StringUntrusted, false, TaintPrivilegeLocalRead},
		{"leitura-local-com-cap-armada-de-rede", "http_get_local", "cap:http.post", taint.StringUntrusted, false, TaintPrivilegeLocalRead},
		// Egress externo: continuam privilegiadas pela capability (a leitura única por run mantém-se).
		{"ocr_read-untrusted-negada", "ocr_read", "cap:fs.read", taint.StringUntrusted, true, TaintPrivilegeCapability},
		{"video_read-untrusted-negada", "video_read", "cap:fs.read", taint.StringUntrusted, true, TaintPrivilegeCapability},
		{"web_post-untrusted-negada", "web_post", "cap:http.post", taint.StringUntrusted, true, TaintPrivilegeCapability},
		// Eixo implícito ⇒ a regra de antes (fail-closed): cap:fs.read armada nega.
		{"eixo-implicito-regra-de-antes", "doc_read_implicito", "cap:fs.read", taint.StringUntrusted, true, TaintPrivilegeCapability},
		// Tool sem entrada no registo ⇒ a regra de antes.
		{"tool-desconhecida-regra-de-antes", "fora_do_registo", "cap:fs.read", taint.StringUntrusted, true, TaintPrivilegeCapability},
		{"tool-desconhecida-cap-livre", "fora_do_registo", "cap:mem.read", taint.StringUntrusted, false, TaintPrivilegeNotListed},
		// O nome de uma leitura local com a capability de outra tool não herda a excepção.
		{"nome-de-leitura-com-outra-capability", "doc_read", "cap:http.post", taint.StringUntrusted, true, TaintPrivilegeCapability},
		// Efeito declarado com a capability fora do conjunto: privilegiada também.
		{"efeito-declarado-fora-do-conjunto", "fs_write", "cap:fs.write", taint.StringUntrusted, true, TaintPrivilegeDeclaredEffect},
		// Contexto trusted: nada disto nega.
		{"ocr_read-trusted-passa", "ocr_read", "cap:fs.read", taint.StringTrusted, false, TaintPrivilegeCapability},
		{"efeito-declarado-trusted-passa", "fs_write", "cap:fs.write", taint.StringTrusted, false, TaintPrivilegeDeclaredEffect},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			dec, p := aos538Mediar(t, aos538Caps(), aos538Destinos(), c.tool, c.capability, c.rotulo, "")
			negou := dec.Effect == EffectDeny
			if negou != c.querDeny {
				t.Fatalf("efeito %s (denied_by=%q), queria deny=%v", dec.Effect, dec.DeniedBy, c.querDeny)
			}
			if negou && dec.DeniedBy != "taint" {
				t.Fatalf("negada pelo motivo errado: denied_by=%q", dec.DeniedBy)
			}
			// A decisão fica auditável: o evento diz porque a call foi (ou não) privilegiada.
			if p.Context.TaintPrivilege != c.querRazao {
				t.Fatalf("evento com taint_privilege=%q, queria %q", p.Context.TaintPrivilege, c.querRazao)
			}
		})
	}
}

// Com AOS_PRIVILEGED_CAPS vazia o gate está desarmado e nada muda: nem o efeito declarado arma o
// gate, nem o evento ganha o campo novo.
func TestAOS538_GateDesarmadoNadaMuda(t *testing.T) {
	for _, tool := range []string{"web_post", "fs_write", "ocr_read", "doc_read"} {
		capability := map[string]string{"web_post": "cap:http.post", "fs_write": "cap:fs.write", "ocr_read": "cap:fs.read", "doc_read": "cap:fs.read"}[tool]
		dec, p := aos538Mediar(t, nil, aos538Destinos(), tool, capability, taint.StringUntrusted, "")
		if dec.Effect != EffectPermit {
			t.Fatalf("%s: gate desarmado negou (%s, %q)", tool, dec.Effect, dec.DeniedBy)
		}
		if p.Context.TaintPrivilege != "" {
			t.Fatalf("%s: gate desarmado carimbou %q", tool, p.Context.TaintPrivilege)
		}
	}
	if NewDestinationPrivileged(NewStaticPrivilegedSet(), aos538Destinos()).HasPrivileged() {
		t.Fatal("os destinos declarados armaram um conjunto vazio")
	}
	if NewDestinationPrivileged(nil, aos538Destinos()).HasPrivileged() {
		t.Fatal("base nil tinha de ficar desarmada")
	}
}

// Sem o destino das tools, o gate é o de antes, e o carimbo forjado pelo chamador nunca chega ao
// evento como se fosse a razão do gate.
func TestAOS538_SemDestinosRegraDeAntesECarimboNaoForjavel(t *testing.T) {
	dec, p := aos538Mediar(t, aos538Caps(), NewToolDestinations(nil), "doc_read", "cap:fs.read", taint.StringUntrusted, TaintPrivilegeLocalRead)
	if dec.Effect != EffectDeny || dec.DeniedBy != "taint" {
		t.Fatalf("sem destinos a doc_read untrusted tinha de ser negada pelo taint, veio %s/%q", dec.Effect, dec.DeniedBy)
	}
	if p.Context.TaintPrivilege != TaintPrivilegeCapability {
		t.Fatalf("o carimbo forjado sobreviveu ou a razão está errada: %q", p.Context.TaintPrivilege)
	}

	// Classificador sem a extensão (o StaticPrivilegedSet de antes): o carimbo é apagado.
	store := &storeQueCaptura{}
	m := New(WithHooks(DefaultHooksWithTaint(NewStaticPrivilegedSet(aos538Caps()...))...), WithEventSink(&eventStoreSink{store: store}))
	if err := m.Register("doc_read", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Mediate(context.Background(), Call{RunID: "r", StepID: "s", ToolID: "doc_read", Capability: "cap:fs.read",
		Context: CallContext{Taint: taint.StringTrusted, TaintPrivilege: TaintPrivilegeLocalRead}}); err != nil {
		t.Fatal(err)
	}
	var q mediationPayload
	if err := json.Unmarshal(store.ultimo, &q); err != nil {
		t.Fatal(err)
	}
	if q.Context.TaintPrivilege != "" {
		t.Fatalf("com o classificador de antes o carimbo forjado chegou ao evento: %q", q.Context.TaintPrivilege)
	}
}

func TestAOS538_ToolDestination(t *testing.T) {
	if !(ToolDestination{Egress: " NONE ", Mutation: "none", Reversibility: "Reversible"}).IsLocalRead() {
		t.Fatal("leitura local normalizada nao reconhecida")
	}
	for _, d := range []ToolDestination{
		{Mutation: "none", Reversibility: "reversible"},
		{Egress: "none", Reversibility: "reversible"},
		{Egress: "none", Mutation: "none"},
		{Egress: "internal", Mutation: "none", Reversibility: "reversible"},
	} {
		if d.IsLocalRead() {
			t.Fatalf("%+v nao e leitura local", d)
		}
	}
	for _, d := range []ToolDestination{
		{Egress: "internal"}, {Egress: "external"}, {Mutation: "mutates"}, {Reversibility: "irreversible"},
	} {
		if !d.DeclaresEffect() {
			t.Fatalf("%+v declara um efeito", d)
		}
	}
	if (ToolDestination{}).DeclaresEffect() || (ToolDestination{Egress: "none", Mutation: "none", Reversibility: "reversible"}).DeclaresEffect() {
		t.Fatal("implícito ou leitura local nao declaram efeito")
	}
	got := aos538Destinos().LocalReads()
	if len(got) != 2 || got[0] != "doc_read" || got[1] != "http_get_local" {
		t.Fatalf("LocalReads=%v", got)
	}
}
