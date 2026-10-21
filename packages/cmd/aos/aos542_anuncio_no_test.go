package main

import (
	"encoding/json"
	"net/http"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// AOS-542: o nó anuncia `plan_input_deferral.first_turn_trusted` no `GET /tools` só com o «pedir
// antes de ler» ligado E o gate de taint armado com o conjunto composto. É a condição de o
// `aos-orq` relaxar a regra P4 do validador do plano.
func TestAOS542_No_AnunciaSoComAsDuasCondicoes(t *testing.T) {
	for _, c := range []struct {
		nome         string
		ligado, gate bool
		querAnuncio  bool
	}{
		{"ligado e armado", true, true, true},
		{"ligado sem gate", true, false, false},
		{"desligado com gate", false, true, false},
		{"desligado sem gate", false, false, false},
	} {
		t.Run(c.nome, func(t *testing.T) {
			aos504SemVariavel(t, "AOS_MODEL_PROJECTION_VERSION")
			n := aos486ComporCom(t, "native", func(cfg *Config) {
				cfg.PlanInputDepoisDoPrimeiroTurno = c.ligado
				cfg.Privileged = nil
				if c.gate {
					cfg.Privileged = referencemonitor.NewStaticPrivilegedSet("cap:http.post", durCap)
				}
			})
			rec := getReq(n.h, "/tools", map[string]string{"Authorization": "Bearer " + n.tok})
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /tools: %d %s", rec.Code, rec.Body.String())
			}
			var corpo struct {
				PlanInputDeferral *struct {
					FirstTurnTrusted bool `json:"first_turn_trusted"`
				} `json:"plan_input_deferral"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &corpo); err != nil {
				t.Fatalf("corpo: %v", err)
			}
			tem := corpo.PlanInputDeferral != nil && corpo.PlanInputDeferral.FirstTurnTrusted
			if tem != c.querAnuncio || n.node.primeiroTurnoConfiavel != c.querAnuncio {
				t.Fatalf("anuncio=%v no=%v, quero %v", tem, n.node.primeiroTurnoConfiavel, c.querAnuncio)
			}
			if !c.querAnuncio && corpo.PlanInputDeferral != nil {
				t.Fatal("sem as duas condicoes o campo tem de estar ausente (bytes de antes)")
			}
		})
	}
}

// O critério do gate armado é o do kernel: nil desarmado; um conjunto vazio desarmado; um
// conjunto com capabilities armado.
func TestAOS542_GateDeTaintArmado(t *testing.T) {
	if gateDeTaintArmado(nil) || gateDeTaintArmado(referencemonitor.NewStaticPrivilegedSet()) {
		t.Fatal("sem capabilities o gate nao esta armado")
	}
	if !gateDeTaintArmado(referencemonitor.NewStaticPrivilegedSet("cap:http.post")) {
		t.Fatal("com capabilities o gate esta armado")
	}
}
