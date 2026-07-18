package pdp

import (
	"context"
	"testing"

	"github.com/aos-ref/control-plane/governance/autonomy"
)

// stubOracle é um [autonomy.Oracle] de teste: devolve um nível fixo por par, ou o
// fail-closed L0 se o par não estiver no mapa.
type stubOracle map[[2]string]autonomy.Level

func (o stubOracle) LevelFor(agent, domain string) autonomy.Level {
	if l, ok := o[[2]string{agent, domain}]; ok {
		return l
	}
	return autonomy.L0
}

// openWithOracle carrega o bundle de referência committado com um oráculo de
// autonomia ligado.
func openWithOracle(t *testing.T, o autonomy.Oracle) *PDP {
	t.Helper()
	p, err := Open("policies", WithAutonomyOracle(o))
	if err != nil {
		t.Fatalf("Open(policies): %v", err)
	}
	return p
}

// httpPermit devolve um Input cap:http.post que a política de referência PERMITE
// (region eu, taint trusted, authority com a capability), com a RiskClass dada.
func httpPermit(agent, riskClass string) Input {
	return Input{
		Principal:  Principal{ID: agent, AgentClass: "agent-worker", Authority: []string{"cap:http.post", "cap:fs.read"}},
		Capability: "cap:http.post",
		Resource:   Resource{Type: "url", Value: "https://api.example.com/x", Region: "eu"},
		Context:    DecisionContext{Taint: "trusted", RiskClass: riskClass},
	}
}

// fsReadPermit devolve um Input cap:fs.read (domínio "fs") que a política PERMITE.
func fsReadPermit(agent, riskClass string) Input {
	return Input{
		Principal:  Principal{ID: agent, AgentClass: "agent-worker", Authority: []string{"cap:http.post", "cap:fs.read"}},
		Capability: "cap:fs.read",
		Resource:   Resource{Type: "file", Value: "/etc/data"},
		Context:    DecisionContext{Taint: "trusted", RiskClass: riskClass},
	}
}

// TestAutonomyOverlayReflectsLevelTimesRisk é o teste de INTEGRAÇÃO PDP exigido: a
// decisão reflecte NÍVEL × CLASSE DE RISCO. Para a MESMA capability/base-permit,
// níveis e classes diferentes produzem efeitos/oversight diferentes.
func TestAutonomyOverlayReflectsLevelTimesRisk(t *testing.T) {
	agent := "nhi-A"
	cases := []struct {
		name       string
		level      autonomy.Level
		riskClass  string
		wantEffect Effect
		wantMode   string
	}{
		{"L0-safe-suggest-escala", autonomy.L0, "safe", Escalate, "suggest"},
		{"L1-safe-escala", autonomy.L1, "safe", Escalate, "confirm"},
		{"L2-gray-escala-batch", autonomy.L2, "gray", Escalate, "batch"},
		{"L3-safe-permit-run", autonomy.L3, "safe", Permit, "run"},
		{"L3-danger-escala", autonomy.L3, "danger", Escalate, "confirm"},
		{"L4-gray-permit-run", autonomy.L4, "gray", Permit, "run"},
		{"L4-danger-escala", autonomy.L4, "danger", Escalate, "confirm"},
		{"L5-danger-permit-posthoc", autonomy.L5, "danger", Permit, "post_hoc_sample"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			oracle := stubOracle{{agent, "http"}: c.level}
			p := openWithOracle(t, oracle)
			dec, err := p.Decide(context.Background(), httpPermit(agent, c.riskClass))
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if dec.Effect != c.wantEffect {
				t.Errorf("effect = %s; quer %s", dec.Effect, c.wantEffect)
			}
			ob := findObligation(dec, obligationAutonomy)
			if ob == nil {
				t.Fatal("obligation autonomy ausente")
			}
			if ob.Params["oversight"] != c.wantMode {
				t.Errorf("oversight = %q; quer %q", ob.Params["oversight"], c.wantMode)
			}
			if ob.Params["level"] != c.level.String() {
				t.Errorf("level obligation = %q; quer %q", ob.Params["level"], c.level.String())
			}
			if ob.Params["domain"] != "http" {
				t.Errorf("domain = %q; quer http", ob.Params["domain"])
			}
			if ob.Params["risk_class"] != c.riskClass {
				t.Errorf("risk_class = %q; quer %q", ob.Params["risk_class"], c.riskClass)
			}
		})
	}
}

// TestAutonomyGranularityPerDomainViaPDP prova que o MESMO agente é tratado
// diferentemente por domínio no caminho do PDP (L4 em http, L1 em fs).
func TestAutonomyGranularityPerDomainViaPDP(t *testing.T) {
	agent := "nhi-B"
	oracle := stubOracle{
		{agent, "http"}: autonomy.L4,
		{agent, "fs"}:   autonomy.L1,
	}
	p := openWithOracle(t, oracle)

	// http (L4) + safe → corre (permit).
	httpDec, err := p.Decide(context.Background(), httpPermit(agent, "safe"))
	if err != nil {
		t.Fatal(err)
	}
	if httpDec.Effect != Permit {
		t.Errorf("http/L4/safe effect = %s; quer permit", httpDec.Effect)
	}

	// fs (L1) + safe → escala (confirm) — MESMO agente, MESMA classe, domínio diferente.
	fsDec, err := p.Decide(context.Background(), fsReadPermit(agent, "safe"))
	if err != nil {
		t.Fatal(err)
	}
	if fsDec.Effect != Escalate {
		t.Errorf("fs/L1/safe effect = %s; quer escalate", fsDec.Effect)
	}
	if ob := findObligation(fsDec, obligationAutonomy); ob == nil || ob.Params["domain"] != "fs" {
		t.Errorf("esperava domínio fs; obteve %+v", ob)
	}
}

// TestAutonomyFailClosedUnregisteredPairEscalates prova o fail-closed no PDP: um par
// SEM nível registado resolve para L0 → suggest → escalate, mesmo para uma acção safe.
func TestAutonomyFailClosedUnregisteredPairEscalates(t *testing.T) {
	oracle := stubOracle{} // vazio: todo o par é L0
	p := openWithOracle(t, oracle)
	dec, err := p.Decide(context.Background(), httpPermit("nhi-Z", "safe"))
	if err != nil {
		t.Fatal(err)
	}
	if dec.Effect != Escalate {
		t.Errorf("par não registado (L0) effect = %s; quer escalate (fail-closed)", dec.Effect)
	}
	ob := findObligation(dec, obligationAutonomy)
	if ob == nil || ob.Params["level"] != "L0" || ob.Params["oversight"] != "suggest" {
		t.Errorf("esperava L0/suggest; obteve %+v", ob)
	}
}

// TestAutonomyEmptyRiskClassFailClosedDanger prova que uma RiskClass vazia é tratada
// como danger (fail-closed): a L3, safe correria mas danger confirma → escalate.
func TestAutonomyEmptyRiskClassFailClosedDanger(t *testing.T) {
	oracle := stubOracle{{"nhi-C", "http"}: autonomy.L3}
	p := openWithOracle(t, oracle)
	dec, err := p.Decide(context.Background(), httpPermit("nhi-C", "")) // RiskClass ausente
	if err != nil {
		t.Fatal(err)
	}
	if dec.Effect != Escalate {
		t.Errorf("RiskClass vazia deve ser danger (fail-closed) → escalate; obteve %s", dec.Effect)
	}
}

// TestAutonomyNoOracleUnchanged prova que sem oráculo o PDP decide como antes: uma
// base permit permanece permit, sem obligation de autonomia.
func TestAutonomyNoOracleUnchanged(t *testing.T) {
	p := mustOpen(t) // sem WithAutonomyOracle
	dec, err := p.Decide(context.Background(), httpPermit("nhi-D", "danger"))
	if err != nil {
		t.Fatal(err)
	}
	if dec.Effect != Permit {
		t.Errorf("sem oráculo: effect = %s; quer permit (comportamento inalterado)", dec.Effect)
	}
	if findObligation(dec, obligationAutonomy) != nil {
		t.Error("sem oráculo não deve haver obligation de autonomia")
	}
}

// TestAutonomyNeverLoosensDeny prova que o overlay NUNCA converte um deny em permit:
// uma capability fora da allowlist é negada e permanece negada, sem overlay — mesmo
// com o nível máximo (L5).
func TestAutonomyNeverLoosensDeny(t *testing.T) {
	oracle := stubOracle{{"nhi-E", "fs"}: autonomy.L5}
	p := openWithOracle(t, oracle)
	dec, err := p.Decide(context.Background(), Input{
		Principal:  Principal{ID: "nhi-E", AgentClass: "agent-worker", Authority: []string{"cap:fs.delete"}},
		Capability: "cap:fs.delete", // NÃO consta da allowlist da classe → deny de base
		Resource:   Resource{Type: "file", Value: "/etc/x"},
		Context:    DecisionContext{Taint: "trusted", RiskClass: "safe"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Effect != Deny {
		t.Errorf("deny de base com L5 deve permanecer deny; obteve %s", dec.Effect)
	}
	if findObligation(dec, obligationAutonomy) != nil {
		t.Error("um deny não deve receber overlay de autonomia")
	}
}

func findObligation(d Decision, typ string) *Obligation {
	for i := range d.Obligations {
		if d.Obligations[i].Type == typ {
			return &d.Obligations[i]
		}
	}
	return nil
}
