package replay

// ÂNCORAS VERIFICADAS — «não divergiu» deixa de ser indistinguível de «não foi comparado».
//
// # O DEFEITO, medido
//
// As três comparações NÃO-prompt do replay são opt-in: `model` só corre com
// `Spec.Model.ModelID != ""`, `assembly_version` com `Spec.AssemblyVersion != ""`, e
// `step_id` com `Options.StepIdentity != nil`. O opt-in é deliberado — é a
// retro-compatibilidade que o próprio comentário do [TrajectorySpec] declara.
//
// O que estava errado era o SILÊNCIO. Medido a 2026-08-28: o MESMO log, com Model e
// AssemblyVersion omitidos da spec, devolvia `Fidelity=1, Divergence=nil` — bytes de
// resultado indistinguíveis de uma verificação completa. Metade das comparações
// desligadas, e nada o dizia.
//
// É a mesma classe de defeito que o `outcome_anchored` do harness fecha: uma
// verificação que NÃO corre tem de ser visível, senão um verde fraco lê-se como um
// verde forte.

import (
	"context"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// identFixa é uma [agentruntime.StepIdentity] que devolve sempre o mesmo step_id —
// suficiente para ligar a âncora e, no teste de espelho, para a fazer divergir.
type identFixa struct{ valor string }

func (i identFixa) StepID(string, int) string { return i.valor }

// TestAnchorsVerifiedDeclaraOQueFoiComparado cobre o caso que motivou tudo: uma spec
// mínima passa, mas passa a DIZER que passou sem as comparações não-prompt.
func TestAnchorsVerifiedDeclaraOQueFoiComparado(t *testing.T) {
	ctx := context.Background()
	or := runOriginal(t, "run_ancoras_declaradas")
	e := mustEngine(t, or)

	// Sem StepIdentity DE PROPÓSITO: uma que não reproduza a derivação gravada diverge
	// por `step_id sequence` e destruiria o pressuposto deste teste — os dois replays
	// têm de ser igualmente VERDES para o contraste ser sobre a VISIBILIDADE e não sobre
	// o veredicto. A âncora `step_id` é coberta por [TestAnchorsVerifiedEspelhaOQueEComparado].
	res, err := e.Replay(ctx, or.goal.RunID, Options{Spec: or.spec})
	if err != nil {
		t.Fatalf("replay completo: %v", err)
	}
	if len(res.AnchorsVerified) != 2 {
		t.Fatalf("spec com Model e AssemblyVersion: âncoras = %v, queria as duas", res.AnchorsVerified)
	}

	fraca := or.spec
	fraca.Model = agentruntime.ModelConfig{}
	fraca.AssemblyVersion = ""
	res2, err := e.Replay(ctx, or.goal.RunID, Options{Spec: fraca})
	if err != nil {
		t.Fatalf("replay fraco: %v", err)
	}
	if res2.Divergence != nil || res2.Fidelity != 1.0 {
		t.Fatalf("o replay fraco devia continuar verde (o opt-in mantém-se): %+v", res2.Divergence)
	}
	if len(res2.AnchorsVerified) != 0 {
		t.Fatalf("sem Model/AssemblyVersion/StepIdentity: âncoras = %v, queria nenhuma", res2.AnchorsVerified)
	}
	// O ponto: os dois resultados são igualmente VERDES e agora DISTINGUÍVEIS.
	if res.Fidelity != res2.Fidelity || (res.Divergence == nil) != (res2.Divergence == nil) {
		t.Fatal("pressuposto do teste partido: os dois replays deviam ser igualmente verdes")
	}
}

// TestAnchorsVerifiedEspelhaOQueEComparado é o que impede [activeAnchors] de mentir.
//
// [activeAnchors] e [ReplayEngine.detectDivergence] têm as MESMAS três condições escritas
// em dois sítios. Duas cópias divergem, e a que mentiria seria o relatório — a dizer que
// comparou o que ninguém comparou. Este teste amarra-as: por cada âncora declarada activa,
// força a divergência correspondente e exige que ela saia com a razão certa. Se uma
// condição mudar num sítio e não no outro, fica vermelho.
func TestAnchorsVerifiedEspelhaOQueEComparado(t *testing.T) {
	ctx := context.Background()
	or := runOriginal(t, "run_ancoras_espelho")
	e := mustEngine(t, or)

	casos := map[string]struct {
		opts  Options
		razao string
	}{
		"model": {
			opts: Options{Spec: func() TrajectorySpec {
				s := or.spec
				s.Model.Seed = s.Model.Seed + 1 // divergente
				return s
			}()},
			razao: "model",
		},
		"assembly_version": {
			opts: Options{Spec: func() TrajectorySpec {
				s := or.spec
				s.AssemblyVersion = "0.0.0-divergente"
				return s
			}()},
			razao: "assembly_version",
		},
		"step_id": {
			opts: Options{
				Spec:         or.spec,
				StepIdentity: identFixa{valor: "step-divergente"},
			},
			razao: "step_id sequence",
		},
	}

	for _, nome := range activeAnchors(or.spec, identFixa{}) {
		caso, ok := casos[nome]
		if !ok {
			t.Fatalf("activeAnchors declara a âncora %q e este teste não a sabe forçar — "+
				"acrescentou-se uma âncora sem prova de que é COMPARADA", nome)
		}
		t.Run(nome, func(t *testing.T) {
			res, err := e.Replay(ctx, or.goal.RunID, caso.opts)
			if err != nil {
				t.Fatalf("replay: %v", err)
			}
			if res.Divergence == nil {
				t.Fatalf("a âncora %q é declarada ACTIVA mas o replay não a comparou: "+
					"activeAnchors e detectDivergence divergiram", nome)
			}
			if res.Divergence.Reason != caso.razao {
				t.Fatalf("âncora %q: razão %q, queria %q", nome, res.Divergence.Reason, caso.razao)
			}
		})
	}
}
