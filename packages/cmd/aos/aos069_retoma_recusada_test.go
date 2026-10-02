package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// AOS-069 — um registo de retoma RECUSADO pela leitura (não é o que o Put do nó escreveria) é
// um conflito com o estado do run: 409, como o ErrNoResumeRecord, e não o 500 «retoma falhou».
// Aqui: um segundo registo acrescentado cru ao stream de governação, com outro run_id de
// envelope (a forma de escapar à dedup do Put), com um Goal sem os `inputs`.
func TestAOS069_RetomaComRegistoAdulteradoDa409(t *testing.T) {
	node, svc, h, _ := aos263Node(t)
	const run = "run-069-retoma"
	aos263TornaRetomavel(t, node, svc, run)

	adulterado := `{"run_id":"` + run + `","body":{"RunID":"` + run + `","Objective":"outro objectivo"}}`
	if _, err := node.EventStore.Append(context.Background(), "aos-internal/gov/approvals", eventstore.EventInput{
		Type: "run.resume.record", RunID: "escritor-cru", StepID: "resume-" + run, Payload: []byte(adulterado),
	}); err != nil {
		t.Fatalf("Append cru: %v", err)
	}

	rec := postJSON(h, "POST", "/runs/"+run+"/resume", map[string]any{"credential": "cred-fresca"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("um registo de retoma recusado devia dar 409, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "registo de retoma recusado") {
		t.Fatalf("a resposta devia dizer que o registo foi recusado: %s", rec.Body.String())
	}
}

// Controlo: o mesmo run sem a escrita crua não é recusado por esta razão.
func TestAOS069_RetomaSemAdulteracaoNaoERecusadaPorRegisto(t *testing.T) {
	node, svc, h, _ := aos263Node(t)
	const run = "run-069-retoma-limpa"
	aos263TornaRetomavel(t, node, svc, run)
	rec := postJSON(h, "POST", "/runs/"+run+"/resume", map[string]any{"credential": "cred-fresca"})
	if strings.Contains(rec.Body.String(), "registo de retoma recusado") {
		t.Fatalf("sem adulteracao o registo nao pode ser recusado: %d %s", rec.Code, rec.Body.String())
	}
}
