package referencemonitor

import (
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// --- SAROC-02 / fail-open-edge: egress fail-closed + forma nua + allowlist local ---

func TestEgressForCall_FailClosedDefault(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		call Call
		want risk.Egress
	}{
		// Rede explícita (sub-acção) ⇒ externo.
		{"http_post", Call{Capability: "cap:http.post"}, risk.EgressExternal},
		{"net_connect", Call{Capability: "cap:net.connect"}, risk.EgressExternal},
		{"mail_send", Call{Capability: "cap:mail.send"}, risk.EgressExternal},
		{"webhook_post", Call{Capability: "cap:webhook.post"}, risk.EgressExternal},
		// Forma NUA (sem sub-acção) também casa (fail-open-edge): "cap:http"/"cap:net".
		{"http_nu", Call{Capability: "cap:http"}, risk.EgressExternal},
		{"net_nu", Call{Capability: "cap:net"}, risk.EgressExternal},
		// Recurso de rede ⇒ externo, mesmo com capability desconhecida.
		{"resource_url", Call{Capability: "cap:tool.run", Resource: Resource{Type: "url", Value: "https://x"}}, risk.EgressExternal},
		// Provadamente local ⇒ sem egress.
		{"doc_read", Call{Capability: "cap:doc.read", Resource: Resource{Type: "file", Value: "/x"}}, risk.EgressNone},
		{"fs_stat", Call{Capability: "cap:fs.stat"}, risk.EgressNone},
		// NÃO catalogado como rede NEM local ⇒ fail-closed (Unknown → externo na Classify).
		{"s3_upload", Call{Capability: "cap:s3.upload", Resource: Resource{Type: "blob", Value: "b"}}, risk.EgressUnknown},
		{"slack_post", Call{Capability: "cap:slack.post"}, risk.EgressUnknown},
		{"ftp_put", Call{Capability: "cap:ftp.put"}, risk.EgressUnknown},
		{"dns_exfil", Call{Capability: "cap:dns.exfil"}, risk.EgressUnknown},
		// Tipo de recurso não-rede + capability desconhecida ⇒ ainda fail-closed.
		{"unknown", Call{Capability: "cap:whatever.do", Resource: Resource{Type: "db"}}, risk.EgressUnknown},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := egressForCall(&tc.call); got != tc.want {
				t.Errorf("egressForCall(%q, %q) = %v, quer %v", tc.call.Capability, tc.call.Resource.Type, got, tc.want)
			}
		})
	}
}

// reuse-drift: coerência com o contrato de network.IsNetworkCapability (AOS-067) SEM
// importar o sandbox (evita ciclo). Fixa a lista local ao contrato: toda a capability
// que o RM considera de rede resolve egress EXTERNO. Espelha a lógica de AOS-067
// (forma nua + sub-acção) — se AOS-067 mudar, este corpus deve ser revisto.
func TestEgressForCall_CoerenteComContratoDeRede(t *testing.T) {
	t.Parallel()
	// Corpus mínimo que AOS-067 (cap:http / cap:net, nu e sub-acção) trata como rede.
	netCorpus := []string{"cap:http", "cap:http.post", "cap:http.get", "cap:net", "cap:net.connect"}
	for _, c := range netCorpus {
		if !isNetworkCapability(c) {
			t.Errorf("isNetworkCapability(%q) = false, quer true (coerência AOS-067)", c)
		}
		if got := egressForCall(&Call{Capability: c}); got != risk.EgressExternal {
			t.Errorf("egressForCall(%q) = %v, quer External (capability de rede)", c, got)
		}
	}
	// Capabilities locais provadas NÃO devem ser marcadas de rede.
	for _, c := range []string{"cap:doc.read", "cap:fs.stat"} {
		if isNetworkCapability(c) {
			t.Errorf("isNetworkCapability(%q) = true, quer false (capability local)", c)
		}
	}
	// cap:https/cap:mail são egress no RM (SUPERSET deliberado de AOS-067, que só cobre
	// http/net): garantimos que o RM as trata como egress externo (mais estrito), nunca menos.
	for _, c := range []string{"cap:https.post", "cap:mail.send"} {
		if egressForCall(&Call{Capability: c}) != risk.EgressExternal {
			t.Errorf("egressForCall(%q) = não-externo, quer External (superset deliberado)", c)
		}
	}
}

// --- SAROC-01: piso de reversibilidade derivado da capability -----------------

func TestReversibilityForCall_PisoDaCapability(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		call Call
		want risk.Reversibility
	}{
		// Capability inerentemente irreversível: o texto "reversible" NÃO a baixa.
		{"delete_declara_reversible", Call{Capability: "cap:fs.delete", Context: CallContext{Reversibility: "reversible"}}, risk.Irreversible},
		{"send_declara_reversible", Call{Capability: "cap:mail.send", Context: CallContext{Reversibility: "reversible"}}, risk.Irreversible},
		{"transfer_declara_reversible", Call{Capability: "cap:bank.transfer", Context: CallContext{Reversibility: "reversible"}}, risk.Irreversible},
		{"db_delete", Call{Capability: "cap:db.delete", Context: CallContext{Reversibility: "reversible"}}, risk.Irreversible},
		// Capability não-destrutiva: o texto manda (só "reversible" ⇒ reversível).
		{"read_reversible", Call{Capability: "cap:doc.read", Context: CallContext{Reversibility: "reversible"}}, risk.Reversible},
		{"read_vazio_failclosed", Call{Capability: "cap:doc.read", Context: CallContext{Reversibility: ""}}, risk.ReversibilityUnknown},
		{"read_irreversible", Call{Capability: "cap:doc.read", Context: CallContext{Reversibility: "irreversible"}}, risk.ReversibilityUnknown},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := reversibilityForCall(&tc.call); got != tc.want {
				t.Errorf("reversibilityForCall(%q, %q) = %v, quer %v", tc.call.Capability, tc.call.Context.Reversibility, got, tc.want)
			}
		})
	}
}

// --- SAROC-01: piso de sensibilidade derivado do egress -----------------------

func TestSensitivityForCall_PisoDoEgress(t *testing.T) {
	t.Parallel()
	// Egress externo declarado "public" ⇒ elevado ao piso interno (o texto não baixa).
	if got := sensitivityForCall(&Call{Context: CallContext{Sensitivity: "public"}}, risk.EgressExternal); got != risk.SensitivityInternal {
		t.Errorf("egress externo + public: sensibilidade = %v, quer >= interna (piso)", got)
	}
	// Sem egress, o texto manda: public fica public.
	if got := sensitivityForCall(&Call{Context: CallContext{Sensitivity: "public"}}, risk.EgressNone); got != risk.SensitivityPublic {
		t.Errorf("sem egress + public: sensibilidade = %v, quer public", got)
	}
	// O texto pode ELEVAR acima do piso: egress externo + pii continua sensível.
	if got := sensitivityForCall(&Call{Context: CallContext{Sensitivity: "pii"}}, risk.EgressExternal); got != risk.SensitivitySensitive {
		t.Errorf("egress externo + pii: sensibilidade = %v, quer sensível", got)
	}
}

// --- SAROC-03: chave de lote agrupa só acções equivalentes --------------------

func TestBatchKeyForCall_AgrupaEquivalentes(t *testing.T) {
	t.Parallel()
	a := Call{RunID: "r1", Capability: "cap:doc.read", Resource: Resource{Type: "file", Value: "/x"}}
	b := Call{RunID: "r1", Capability: "cap:doc.read", Resource: Resource{Type: "file", Value: "/x"}}
	c := Call{RunID: "r1", Capability: "cap:doc.read", Resource: Resource{Type: "file", Value: "/OUTRO"}}
	d := Call{RunID: "r1", Capability: "cap:http.post", Resource: Resource{Type: "file", Value: "/x"}}
	if batchKeyForCall(&a) != batchKeyForCall(&b) {
		t.Errorf("acções equivalentes deviam partilhar a chave de lote")
	}
	if batchKeyForCall(&a) == batchKeyForCall(&c) {
		t.Errorf("destinos diferentes NÃO deviam partilhar a chave de lote")
	}
	if batchKeyForCall(&a) == batchKeyForCall(&d) {
		t.Errorf("capabilities diferentes NÃO deviam partilhar a chave de lote")
	}
	if batchKeyForCall(&Call{RunID: "", Capability: "cap:doc.read"}) != "" {
		t.Errorf("run vazio deviaria produzir chave vazia (confirmação individual)")
	}
}
