package modelgateway_test

import (
	"errors"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-540 — a projecção nativa de um tail da 1.6.0 («pedir antes de ler»). As vistas saem da
// semente e da sequência do kernel ([agentruntime.OpenRunTail]), as que o loop usa.

func aos540Vista(t *testing.T, versao string, turnos ...func(seq *agentruntime.TailSequence) []agentruntime.TailSegment) agentruntime.PromptView {
	t.Helper()
	goal := agentruntime.Goal{
		Objective: "Faz OCR da fatura-5 e resume-a.",
		Inputs:    []agentruntime.PlanInput{{From: "n0", Output: "document_content", Digest: "sha256:00", Content: []byte("dados do passo anterior")}},
	}
	tail, seq, err := agentruntime.OpenRunTail(versao, goal)
	if err != nil {
		t.Fatal(err)
	}
	for _, turno := range turnos {
		tail = append(tail, turno(seq)...)
	}
	asm, err := agentruntime.NewPromptAssemblerFor(versao, "sistema", nil)
	if err != nil {
		t.Fatal(err)
	}
	return asm.Assemble(len(turnos)+1, tail)
}

func aos540SoTexto(passo, texto string) func(seq *agentruntime.TailSequence) []agentruntime.TailSegment {
	return func(seq *agentruntime.TailSequence) []agentruntime.TailSegment {
		segs, _ := seq.TurnWithState(passo, texto, "", nil)
		return segs
	}
}

func aos540ComTool(passo string) func(seq *agentruntime.TailSequence) []agentruntime.TailSegment {
	return func(seq *agentruntime.TailSequence) []agentruntime.TailSegment {
		segs, _ := seq.TurnWithState(passo, "leio", "", []agentruntime.CapturedToolResult{{
			Invocation: agentruntime.ToolInvocation{ToolID: "doc_read", Input: []byte(`{"doc_id":"fatura-5"}`)},
			Result:     agentruntime.Untrusted([]byte("texto da fatura")),
		}})
		return segs
	}
}

func aos540Papeis(msgs []port.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = string(m.Role)
	}
	return out
}

// O TURNO 1 SÓ COM TEXTO projecta-se num `assistant` sem tool calls, seguido de um `user` com os
// dados e o aviso de entrega. Na 1.6.0, e só no primeiro turno.
func TestAOS540_ProjeccaoNativa_Turno1SoComTexto(t *testing.T) {
	v := aos540Vista(t, agentruntime.AssemblyVersion160, aos540SoTexto("step-000001", "espero pelos dados"))
	msgs, err := modelgateway.ProjectNative(v)
	if err != nil {
		t.Fatalf("ProjectNative: %v", err)
	}
	if got := strings.Join(aos540Papeis(msgs), ","); got != "system,user,assistant,user" {
		t.Fatalf("papeis = %s", got)
	}
	if msgs[2].Content != "espero pelos dados" || len(msgs[2].ToolCalls) != 0 {
		t.Fatalf("assistant = %+v", msgs[2])
	}
	if strings.Contains(msgs[1].Content, "dados do passo anterior") || !strings.Contains(msgs[3].Content, "dados do passo anterior") ||
		!strings.Contains(msgs[3].Content, "plan_input_delivered") {
		t.Fatalf("os dados tinham de ir so no user a seguir ao turno 1:\n%s\n---\n%s", msgs[1].Content, msgs[3].Content)
	}
	// Com uma tool call no turno 1: o `user` com os dados vem depois das mensagens `tool`.
	v = aos540Vista(t, agentruntime.AssemblyVersion160, aos540ComTool("step-000001"))
	msgs, err = modelgateway.ProjectNative(v)
	if err != nil {
		t.Fatalf("ProjectNative: %v", err)
	}
	if got := strings.Join(aos540Papeis(msgs), ","); got != "system,user,assistant,tool,user" {
		t.Fatalf("papeis = %s", got)
	}
}

// A EXCEPÇÃO É SÓ DO PRIMEIRO TURNO, e só na 1.6.0: o mesmo tail montado noutro layout, ou um
// segundo turno só com texto a meio do tail, continuam a ser recusados.
func TestAOS540_ProjeccaoNativa_ExcepcaoSoNoPrimeiroTurnoDa160(t *testing.T) {
	v := aos540Vista(t, agentruntime.AssemblyVersion160, aos540SoTexto("step-000001", "espero pelos dados"))
	v.AssemblyVersion = agentruntime.AssemblyVersion150
	if _, err := modelgateway.ProjectNative(v); !errors.Is(err, modelgateway.ErrNativeProjection) {
		t.Fatalf("na 1.5.0 um turno so com texto a meio do tail e recusado: %v", err)
	}
	// Um segundo turno só com texto, seguido de outro segmento, na 1.6.0.
	v = aos540Vista(t, agentruntime.AssemblyVersion160, aos540ComTool("step-000001"), aos540SoTexto("step-000002", "texto"),
		func(seq *agentruntime.TailSequence) []agentruntime.TailSegment {
			return seq.Correction([]byte("continua"))
		})
	if _, err := modelgateway.ProjectNative(v); !errors.Is(err, modelgateway.ErrNativeProjection) {
		t.Fatalf("um segundo turno so com texto a meio do tail e recusado: %v", err)
	}
}
