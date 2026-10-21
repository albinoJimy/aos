package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
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

// Revisão adversarial do AOS-542 (achado ALTO): o nó só anuncia se TODAS as tools que o `aos-orq`
// conta como de efeito forem privilegiadas para o gate composto. Uma tool de efeito fora do gate
// (`mail_send` sem eixos, com a capability fora de AOS_PRIVILEGED_CAPS) passaria no turno 2.
func TestAOS542_ToolDeEfeitoForaDoGateNaoAnuncia(t *testing.T) {
	t.Setenv("AOS_MODEL_ENDPOINT", "http://modelo.invalido")
	manifesto := `[
	 {"name":"doc_read","capability":"cap:fs.read","resource_type":"file","resource_value":"doc://{doc_id}","egress":"none","mutation":"none","reversibility":"reversible"},
	 {"name":"web_post","capability":"cap:http.post","resource_type":"url","resource_value":"https://x","egress":"external","mutation":"mutates","reversibility":"irreversible"},
	 {"name":"mail_send","capability":"cap:mail.send","resource_type":"mail","resource_value":"mail://x"}
	]`
	t.Setenv("AOS_MODEL_TOOLS", writeTools(t, manifesto))
	dests, err := toolDestinationsFromEnv()
	if err != nil {
		t.Fatalf("destinos: %v", err)
	}
	privileged := privilegedComDestinos(referencemonitor.NewStaticPrivilegedSet("cap:http.post"), dests)
	fora, err := toolsDeEfeitoForaDoGate(privileged)
	if err != nil {
		t.Fatalf("toolsDeEfeitoForaDoGate: %v", err)
	}
	if len(fora) != 1 || fora[0] != "mail_send" {
		t.Fatalf("tools de efeito fora do gate = %v, quero [mail_send]", fora)
	}
	// Com a capability no conjunto, fica coberta e o nó pode anunciar.
	privileged = privilegedComDestinos(referencemonitor.NewStaticPrivilegedSet("cap:http.post", "cap:mail.send"), dests)
	if fora, _ := toolsDeEfeitoForaDoGate(privileged); len(fora) != 0 {
		t.Fatalf("com cap:mail.send no conjunto nada devia ficar fora: %v", fora)
	}
}

// O manifesto de PRODUÇÃO com o conjunto de produção (`cap:http.post,cap:fs.read`): a `ocr_read` e a
// `video_read` (egress externo) estão cobertas; a `doc_read` é leitura local. O nó anuncia.
func TestAOS542_ManifestoDeProducaoCobertoPeloGate(t *testing.T) {
	t.Setenv("AOS_MODEL_ENDPOINT", "http://modelo.invalido")
	t.Setenv("AOS_MODEL_TOOLS", aos441ManifestoDeProducao)
	dests, err := toolDestinationsFromEnv()
	if err != nil {
		t.Fatalf("destinos: %v", err)
	}
	privileged := privilegedComDestinos(referencemonitor.NewStaticPrivilegedSet("cap:http.post", "cap:fs.read"), dests)
	if fora, err := toolsDeEfeitoForaDoGate(privileged); err != nil || len(fora) != 0 {
		t.Fatalf("manifesto de producao: fora=%v err=%v", fora, err)
	}
}

// Revisão adversarial do AOS-542 (achado MÉDIO 1): um `POST /runs` que exige o primeiro turno
// confiável é recusado (400) por um nó que não o garante, antes de qualquer escrita.
func TestAOS542_No_ExigenciaPorRun(t *testing.T) {
	for _, c := range []struct {
		nome   string
		ligado bool
		quer   int
	}{{"nao garante", false, http.StatusBadRequest}, {"garante", true, http.StatusCreated}} {
		t.Run(c.nome, func(t *testing.T) {
			n := aos540No(t, c.ligado)
			soma := sha256.Sum256([]byte(aos540Documento))
			rec := postJSON(n.h, "POST", "/runs", map[string]any{
				"run_id": "run-542-" + c.nome[:3], "objective": aos540Objectivo,
				"principal_nhi": durAgent, "credential": n.tok, "tools": []string{"arquivo"},
				"inputs": []map[string]string{{"from": "n0", "output": "document_content",
					"digest": "sha256:" + hex.EncodeToString(soma[:]), "content": aos540Documento}},
				"max_turns": 4, "requires_first_turn_trusted": true,
			})
			if c.quer == http.StatusBadRequest && !strings.Contains(rec.Body.String(), "requires_first_turn_trusted") {
				t.Fatalf("a recusa tem de ser a da exigencia, nao outra: %s", rec.Body.String())
			}
			if rec.Code != c.quer {
				t.Fatalf("POST /runs: %d %s, quero %d", rec.Code, rec.Body.String(), c.quer)
			}
		})
	}
}

// Re-revisão do AOS-542 (Mu6): no NÓ COMPOSTO, com o gate armado mas sem cobrir as tools de efeito
// do manifesto (as do harness não declaram eixos e a capability delas não está no conjunto), o nó
// não anuncia e diz quais ficaram fora no arranque.
func TestAOS542_No_ToolsForaDoGateNaoAnuncia(t *testing.T) {
	aos504SemVariavel(t, "AOS_MODEL_PROJECTION_VERSION")
	n := aos486ComporCom(t, "native", func(cfg *Config) {
		cfg.PlanInputDepoisDoPrimeiroTurno = true
		cfg.Privileged = referencemonitor.NewStaticPrivilegedSet("cap:http.post")
	})
	if n.node.primeiroTurnoConfiavel {
		t.Fatal("com tools de efeito fora do gate o no nao podia anunciar")
	}
	rec := getReq(n.h, "/tools", map[string]string{"Authorization": "Bearer " + n.tok})
	if strings.Contains(rec.Body.String(), "plan_input_deferral") {
		t.Fatalf("o GET /tools anunciou: %s", rec.Body.String())
	}
	if !strings.Contains(n.log.String(), "primeiro turno confiavel (AOS-542): NAO ANUNCIADO") {
		t.Fatalf("o arranque tinha de dizer porque nao anuncia:\n%s", n.log.String())
	}
}

// Re-revisão do AOS-542 (Mu1): o egress IMPLÍCITO conta como efeito mesmo com a mutação e a
// reversibilidade benignas e explícitas — o catálogo serve-o `unknown`, e o aos-orq conta-o.
func TestAOS542_EgressImplicitoContaComoEfeito(t *testing.T) {
	t.Setenv("AOS_MODEL_ENDPOINT", "http://modelo.invalido")
	t.Setenv("AOS_MODEL_TOOLS", writeTools(t, `[{"name":"sonda","capability":"cap:net.probe","resource_type":"host","resource_value":"x","mutation":"none","reversibility":"reversible"}]`))
	dests, err := toolDestinationsFromEnv()
	if err != nil {
		t.Fatalf("destinos: %v", err)
	}
	fora, err := toolsDeEfeitoForaDoGate(privilegedComDestinos(referencemonitor.NewStaticPrivilegedSet("cap:http.post"), dests))
	if err != nil || len(fora) != 1 || fora[0] != "sonda" {
		t.Fatalf("fora=%v err=%v, quero [sonda]", fora, err)
	}
}
