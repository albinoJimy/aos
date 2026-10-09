package agentruntime

import (
	"bytes"
	"context"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// AOS-515 — o loop entrega a quem faz o pedido os estados opacos dos turnos anteriores, pela
// chave do digest com que o tail os refere. É por esse digest — e por mais nada — que o estado
// se junta ao turno.

// aos515Vistas corre um run de três turnos com os estados dados e devolve as vistas recebidas.
func aos515Vistas(t *testing.T, layout string, estados ...*ProviderState) []PromptView {
	t.Helper()
	h := newHarness(t, map[string]referencemonitor.ToolFunc{
		"echo": func(_ context.Context, in []byte) ([]byte, error) { return in, nil },
	})
	guiao := []ModelResponse{
		{StopReason: StopToolCalls, Usage: Usage{InputTokens: 5}, ToolCalls: []ToolInvocation{aos514Echo(`{"n":1}`)}},
		{Text: "mais uma", StopReason: StopToolCalls, Usage: Usage{InputTokens: 5}, ToolCalls: []ToolInvocation{aos514Echo(`{"n":2}`)}},
		{Text: "feito", Final: true, StopReason: StopStop, Usage: Usage{InputTokens: 5}},
	}
	for i := range guiao {
		if i < len(estados) {
			guiao[i].State = estados[i]
		}
	}
	var vistas []PromptView
	model := ModelClientFunc(func(_ context.Context, v PromptView) (ModelResponse, error) {
		vistas = append(vistas, v)
		return guiao[v.Turn-1], nil
	})
	goal := sampleGoal()
	goal.AssemblyVersion = layout
	if _, err := New(model, h.rm, h.recorder).Run(context.Background(), goal); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return vistas
}

// aos515Rotulos devolve os valores dos rótulos `state_digest` do tail de uma vista, pela ordem.
func aos515Rotulos(v PromptView) (out []string) {
	for _, seg := range v.Tail {
		for _, m := range seg.Meta {
			if m.Key == StateDigestLabel {
				out = append(out, m.Value)
			}
		}
	}
	return out
}

func TestAOS515_Loop_OsEstadosSeguemNaVistaPeloDigestDoTail(t *testing.T) {
	// Sem estado: a vista não leva mapa nenhum — é a de sempre.
	for _, v := range aos515Vistas(t, AssemblyVersion150) {
		if v.ProviderStates != nil {
			t.Fatalf("turno %d: um run sem estado leva estados na vista: %v", v.Turn, v.ProviderStates)
		}
	}
	e1, e2 := aos514Estado("-1"), aos514Estado("-2")
	vistas := aos515Vistas(t, AssemblyVersion150, e1, e2)
	if vistas[0].ProviderStates != nil {
		t.Fatalf("o primeiro turno nao tem turnos anteriores: %v", vistas[0].ProviderStates)
	}
	for i, quer := range [][]*ProviderState{{e1}, {e1, e2}} {
		v := vistas[i+1]
		rotulos := aos515Rotulos(v)
		if len(rotulos) != len(quer) || len(v.ProviderStates) != len(quer) {
			t.Fatalf("turno %d: %d rotulos e %d estados; queria %d de cada", v.Turn, len(rotulos), len(v.ProviderStates), len(quer))
		}
		for n, e := range quer {
			// A chave é o rótulo do tail, e o rótulo é o sha256 dos bytes.
			if rotulos[n] != sha256Tagged(e.Bytes) || !bytes.Equal(v.ProviderStates[rotulos[n]], e.Bytes) {
				t.Fatalf("turno %d: o estado %d nao esta na vista pela chave do rotulo do tail", v.Turn, n+1)
			}
		}
	}
	// O mapa da vista é uma cópia: quem a recebe não muda o que o turno seguinte vê.
	delete(vistas[1].ProviderStates, aos515Rotulos(vistas[1])[0])
	if len(vistas[2].ProviderStates) != 2 {
		t.Fatalf("apagar uma entrada da vista de um turno mudou a do seguinte")
	}
	// «Não devolvível» e só-referência não têm bytes: não entram no mapa.
	naoDev := &ProviderState{Status: ProviderStateNotReturnable}
	ref := &ProviderState{Status: ProviderStateReference, Digest: sha256Tagged([]byte("x"))}
	for _, v := range aos515Vistas(t, AssemblyVersion150, naoDev, ref) {
		if v.ProviderStates != nil {
			t.Fatalf("turno %d: um estado sem bytes entrou na vista: %v", v.Turn, v.ProviderStates)
		}
	}
	// Num layout sem rótulo (1.4.0) o tail não refere o estado: os bytes seguem na vista, mas
	// não há rótulo por onde os juntar a um turno.
	for _, v := range aos515Vistas(t, AssemblyVersion140, e1, e2) {
		if len(aos515Rotulos(v)) != 0 {
			t.Fatalf("a 1.4.0 ganhou o rotulo do estado")
		}
	}
}
