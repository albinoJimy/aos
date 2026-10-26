package agentruntime

// AOS-557 — A RECUSA DE ARRANQUE É UMA SÓ IMPLEMENTAÇÃO.
//
// O critério 5 do ticket: nenhum portão passa a correr duas vezes com semânticas diferentes. A
// forma de o trancar não é comparar duas cópias da regra — é provar que, para o MESMO Goal, o que
// o [Runtime.Run] recusa e o que [RefuseAtStartup] recusa é o MESMO erro, com a mesma causa.
//
// Comparar pelo erro, e não por um booleano, é deliberado: duas regras podiam concordar em
// «recusa» e discordar na CAUSA, e é a causa que o nó sela no estado terminal e serve no
// `GET /runs/{id}`.

import (
	"context"
	"errors"
	"testing"
)

// aos557GoalDeclarando é o Goal de exemplo com a oferta, o contrato e a origem dados. A
// LISTA-BRANCA FICA NIL — «sem restrição além do token» —, para que o único eixo que decide nestes
// casos seja a PERTENÇA À OFERTA, que é o que distingue esta conferência da da porta HTTP. O caso
// da lista-branca tem a sua própria linha, com a lista explícita.
func aos557GoalDeclarando(contrato []string, origem string, oferta ...string) Goal {
	g := sampleGoal()
	g.CompletionMode = CompletionObserve
	g.CompletionRequires = contrato
	g.AllowedTools = nil
	g.Tools = make([]ToolSpec, len(oferta))
	for i, nome := range oferta {
		g.Tools[i] = ToolSpec{Name: nome, Version: "1.0.0", Digest: "sha256:beef"}
	}
	if origem != "" {
		g.OutputFromTool, g.OutputSourceBinding = origem, OutputSourceMeasure
	}
	return g
}

// aos557Nomes projecta a oferta de um Goal nos nomes — o que [RefuseAtStartup] recebe.
func aos557Nomes(specs []ToolSpec) []string {
	out := make([]string, len(specs))
	for i := range specs {
		out[i] = specs[i].Name
	}
	return out
}

// O PORTÃO DO `Run` E A [RefuseAtStartup] DÃO O MESMO ERRO para o mesmo Goal. É a amarração do
// critério 5: se alguém reintroduzir uma segunda cópia da regra num dos dois lados, isto fica
// vermelho.
//
// O modelo deste harness falharia se fosse chamado — e não é, em nenhum dos casos recusados: a
// recusa é ANTES do primeiro turno, sem um único efeito. O caso ADMITIDO está no fim, e é ele que
// prova que este teste não passa por o `Run` recusar tudo.
func TestAOS557_RunERefuseAtStartupDaoAMesmaRecusa(t *testing.T) {
	for nome, c := range map[string]struct {
		contrato []string
		origem   string
		oferta   []string
		lista    []string
		recusa   error
	}{
		"contrato fora da oferta": {[]string{"ausente_da_oferta"}, "", []string{"doc_read"}, nil, ErrImpossibleCompletionContract},
		"contrato fora da lista":  {[]string{"doc_read"}, "", []string{"doc_read"}, []string{"outra"}, ErrImpossibleCompletionContract},
		"origem fora da oferta":   {nil, "ausente_da_oferta", []string{"doc_read"}, nil, ErrImpossibleOutputSource},
		"origem fora da lista":    {nil, "doc_read", []string{"doc_read"}, []string{"outra"}, ErrImpossibleOutputSource},
		"contrato e origem maus":  {[]string{"ausente_da_oferta"}, "ausente_da_oferta", []string{"doc_read"}, nil, ErrImpossibleCompletionContract},
		"oferta vazia":            {[]string{"doc_read"}, "", nil, nil, ErrImpossibleCompletionContract},
	} {
		goal := aos557GoalDeclarando(c.contrato, c.origem, c.oferta...)
		goal.AllowedTools = c.lista
		h := newHarness(t, nil)
		model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
			t.Errorf("%s: o modelo foi chamado — a recusa não é antes do primeiro turno", nome)
			return ModelResponse{Text: "x", Final: true}, nil
		})
		_, doRun := New(model, h.rm, h.recorder).Run(context.Background(), goal)
		deCima := RefuseAtStartup(goal, aos557Nomes(goal.Tools))

		switch {
		case doRun == nil || deCima == nil:
			t.Errorf("%s: Run=%v, RefuseAtStartup=%v — os dois tinham de recusar", nome, doRun, deCima)
		case !errors.Is(doRun, c.recusa):
			t.Errorf("%s: o Run recusou por %v, esperava %v", nome, doRun, c.recusa)
		case doRun.Error() != deCima.Error():
			t.Errorf("%s: causas DIFERENTES:\n  Run:  %v\n  cima: %v", nome, doRun, deCima)
		}
	}

	// E A NÃO-VACUIDADE: um Goal cuja oferta nomeia a tool do contrato E da origem é ADMITIDO
	// pelos dois. Sem este caso, tudo acima passaria com dois portões que recusassem sempre.
	goal := aos557GoalDeclarando([]string{"doc_read"}, "doc_read", "doc_read")
	if err := RefuseAtStartup(goal, aos557Nomes(goal.Tools)); err != nil {
		t.Fatalf("RefuseAtStartup recusou um Goal cumprivel: %v", err)
	}
	h := newHarness(t, nil)
	model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		return ModelResponse{Text: "feito", Final: true}, nil
	})
	if _, err := New(model, h.rm, h.recorder).Run(context.Background(), goal); err != nil {
		t.Fatalf("Run recusou um Goal cumprivel: %v", err)
	}
}

// A [RefuseAtStartup] SUBSTITUI O [Goal.Tools] PELA OFERTA DADA, e é dessa substituição que
// depende poder correr num chamador que ainda não tem o tool set congelado — onde o campo está
// vazio e recusaria todos os contratos.
func TestAOS557_RefuseAtStartupIgnoraOToolSetDoGoal(t *testing.T) {
	// Um Goal cujo `Tools` NÃO nomeia a tool do contrato, e uma oferta que a nomeia: admite.
	goal := aos557GoalDeclarando([]string{"doc_read"}, "", "outra_qualquer")
	if err := RefuseAtStartup(goal, []string{"doc_read"}); err != nil {
		t.Fatalf("a oferta dada tinha de ganhar ao Goal.Tools: %v", err)
	}
	// E ao contrário: um Goal cujo `Tools` a nomeia, com uma oferta que não: recusa.
	goal2 := aos557GoalDeclarando([]string{"doc_read"}, "", "doc_read")
	if err := RefuseAtStartup(goal2, []string{"outra_qualquer"}); !errors.Is(err, ErrImpossibleCompletionContract) {
		t.Fatalf("a oferta dada tinha de ganhar ao Goal.Tools; err=%v", err)
	}
	// Com o veredicto DESLIGADO não se lê declaração nenhuma — a regra de sempre.
	off := aos557GoalDeclarando([]string{"ausente_da_oferta"}, "ausente_da_oferta")
	off.CompletionMode = CompletionOff
	if err := RefuseAtStartup(off, nil); err != nil {
		t.Fatalf("com o veredicto desligado nada se confere: %v", err)
	}
	// E um modo fora do vocabulário é a recusa do próprio [completionDoGoal], como no `Run`.
	mau := aos557GoalDeclarando(nil, "")
	mau.CompletionMode = "nao_existe"
	if err := RefuseAtStartup(mau, nil); err == nil {
		t.Fatal("um modo de veredicto fora do vocabulario tinha de recusar")
	}
}
