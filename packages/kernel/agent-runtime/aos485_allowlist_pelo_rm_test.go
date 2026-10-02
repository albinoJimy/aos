package agentruntime

// AOS-485 — a recusa pela lista-branca do run passa a sair do Reference Monitor. O ciclo já não
// nega: entrega a lista ao RM em cada call e deixa de construir como efeito a tool que vai ser
// negada. Os testes do AOS-413 (aos413_allowlist_test.go) continuam a provar que a tool não
// executa e que o modelo vê o mesmo texto; estes provam a pegada e a reescrita.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// A recusa fica no stream do run como `tool.call.denied`, com o `step_id` da call
// (`<passo>-tool-1`) e o `parent_step_id` do turno que a pediu, e conta em Denials. Com a lista
// VAZIA — o caso medido em produção.
func TestAOS485_RecusaPelaListaDeixaEventoEContador(t *testing.T) {
	h, execucoes := aos413Echo(t)
	model := &capturingPrompts{responder: aos413ChamaEFinaliza("echo")}
	goal := sampleGoal()
	goal.AllowedTools = []string{}
	if _, err := New(model, h.rm, h.recorder).Run(context.Background(), goal); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if execucoes["echo"] != 0 {
		t.Fatalf("a tool EXECUTOU %d vez(es)", execucoes["echo"])
	}
	permitidas, negadas, _ := h.rm.Metrics().Snapshot()
	if permitidas != 0 || negadas != 1 {
		t.Fatalf("contadores do RM: permits=%d denials=%d, quero 0 e 1 — a recusa tem de passar pelo RM", permitidas, negadas)
	}

	evs, err := h.store.Read(context.Background(), goal.RunID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	turno1 := ""
	var negados []int
	for i, ev := range evs {
		switch ev.Type {
		case EventTypeTurnRecorded:
			if turno1 == "" {
				turno1 = ev.StepID
			}
		case referencemonitor.EventTypeDenied:
			negados = append(negados, i)
		case referencemonitor.EventTypeMediated:
			t.Fatalf("há um tool.call.mediated num run em que nada podia ser permitido: %s", ev.Payload)
		}
	}
	if turno1 == "" || len(negados) != 1 {
		t.Fatalf("queria o turn.recorded do turno 1 e exactamente 1 tool.call.denied; turno1=%q negados=%d", turno1, len(negados))
	}
	ev := evs[negados[0]]
	if ev.ParentStepID != turno1 || ev.StepID != turno1+"-tool-1" {
		t.Fatalf("step_id=%q parent_step_id=%q, quero %q e %q", ev.StepID, ev.ParentStepID, turno1+"-tool-1", turno1)
	}
	var p struct {
		Code     string `json:"code"`
		DeniedBy string `json:"denied_by"`
		ToolID   string `json:"tool_id"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if p.Code != CodeToolOutsideRunAllowlist || p.DeniedBy != "run_tool_allowlist" || p.ToolID != "echo" {
		t.Fatalf("payload da recusa: %s", ev.Payload)
	}
	// O que o modelo vê não mudou: o mesmo código e o mesmo denied_by do AOS-413.
	if len(model.views) < 2 ||
		!bytes.Contains(model.views[1], []byte("denied_code=E_TOOL_OUTSIDE_RUN_ALLOWLIST")) ||
		!bytes.Contains(model.views[1], []byte("denied_by=run_tool_allowlist")) {
		t.Fatalf("o tail tinha de levar a negação com o texto de sempre; views=%q", model.views)
	}
}

// Uma tool fora da lista não é construída como efeito: a reescrita não corre para ela. Com um
// rewriter que FALHA, a diferença vê-se no código — sem a condição, a call saía
// `E_EFFECT_REWRITE`, sem chegar ao RM e sem pegada.
func TestAOS485_ToolForaDaListaNaoEReescrita(t *testing.T) {
	h, execucoes := aos413Echo(t)
	reescritas := 0
	rewriter := func(referencemonitor.Call) (referencemonitor.Call, error) {
		reescritas++
		return referencemonitor.Call{}, errors.New("args malformados")
	}
	model := &capturingPrompts{responder: aos413ChamaEFinaliza("outra")}
	goal := sampleGoal()
	goal.AllowedTools = []string{"echo"}
	if _, err := New(model, h.rm, h.recorder, WithCallRewriter(rewriter)).Run(context.Background(), goal); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if reescritas != 0 {
		t.Fatalf("a reescrita correu %d vez(es) para uma tool fora da lista", reescritas)
	}
	if execucoes["outra"] != 0 {
		t.Fatalf("a tool fora da lista EXECUTOU %d vez(es)", execucoes["outra"])
	}
	if len(model.views) < 2 || !bytes.Contains(model.views[1], []byte("denied_code="+CodeToolOutsideRunAllowlist)) {
		t.Fatalf("a recusa tinha de sair com o código da lista e não com o da reescrita; views=%q", model.views)
	}
	if _, negadas, _ := h.rm.Metrics().Snapshot(); negadas != 1 {
		t.Fatalf("a recusa tinha de chegar ao RM: denials=%d", negadas)
	}

	// CONTROLO: para uma tool DA lista a reescrita corre como sempre (e, falhando, nega com o
	// código dela). Sem isto o teste acima passava com a reescrita simplesmente desligada.
	h2, _ := aos413Echo(t)
	model2 := &capturingPrompts{responder: aos413ChamaEFinaliza("echo")}
	if _, err := New(model2, h2.rm, h2.recorder, WithCallRewriter(rewriter)).Run(context.Background(), goal); err != nil {
		t.Fatalf("Run (controlo): %v", err)
	}
	if reescritas != 1 {
		t.Fatalf("controlo: a reescrita tinha de correr 1 vez para a tool da lista, correu %d", reescritas)
	}
	if len(model2.views) < 2 || !bytes.Contains(model2.views[1], []byte("denied_code="+CodeEffectRewrite)) {
		t.Fatalf("controlo: a reescrita falhada tinha de negar com %s; views=%q", CodeEffectRewrite, model2.views)
	}
}

// A lista é a do goal, qualquer que seja a Call que o rewriter devolve: um rewriter que
// reconstrói a Call (sem a lista) e lhe troca a tool não a tira de baixo da restrição.
func TestAOS485_ReescritaNaoTiraAListaDaCall(t *testing.T) {
	h, execucoes := aos413Echo(t)
	rewriter := func(c referencemonitor.Call) (referencemonitor.Call, error) {
		return referencemonitor.Call{
			RunID: c.RunID, StepID: c.StepID, ParentStepID: c.ParentStepID,
			ToolID: "outra", Capability: c.Capability, Principal: c.Principal,
			Credential: c.Credential, Context: c.Context, Input: c.Input,
		}, nil
	}
	model := &capturingPrompts{responder: aos413ChamaEFinaliza("echo")}
	goal := sampleGoal()
	goal.AllowedTools = []string{"echo"}
	if _, err := New(model, h.rm, h.recorder, WithCallRewriter(rewriter)).Run(context.Background(), goal); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if execucoes["outra"] != 0 {
		t.Fatalf("a tool fora da lista EXECUTOU %d vez(es) por a reescrita ter largado a lista", execucoes["outra"])
	}
	if len(model.views) < 2 || !bytes.Contains(model.views[1], []byte("denied_code="+CodeToolOutsideRunAllowlist)) {
		t.Fatalf("o RM tinha de negar a call reescrita para fora da lista; views=%q", model.views)
	}
}
