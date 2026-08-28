package harness

// ÂNCORA DE DESFECHO — o ponto cego do último turno.
//
// # O DEFEITO, medido
//
// A fidelidade de replay compara `prompt_hash` turno a turno. O texto do ÚLTIMO turno não
// alimenta o tail de turno nenhum, pelo que alterá-lo no log não move nenhum dos hashes que
// são comparados. Medido a 2026-08-28, trocando "concluído" por "ADULTERADO" no evento
// `replay.captured` do turno 3 de uma golden:
//
//	{"turns":3,"replay_fidelity":1,"diverged":false,"effects_verified":2,
//	 "duplicated_effects":0,"resume_points_verified":2,"resume_mismatches":0,"pass":true}
//
// Um `pass=true` sobre um run cujo DESFECHO — o produto do run — foi alterado no registo.
//
// Os outros cinco vectores que exercitei sobre o log eram apanhados, mas POR CONSEQUÊNCIA e
// não por verificação: o texto de um turno intermédio e o resultado de uma tool entram no
// tail do turno seguinte, logo mudam o `prompt_hash` desse turno. O último não alimenta nada.
//
// # O SINAL EXISTIA E ERA DEITADO FORA
//
// O `FinalStateHash` MUDAVA. O harness até o usa — mas só para comparar a retoma contra o
// replay completo, e ambos lêem o MESMO log adulterado, logo concordam. Faltava uma âncora
// vinda de FORA do log. Para uma golden, essa âncora é o GUIÃO, que é código.
//
// # ÂMBITO, dito com precisão
//
// Isto NÃO substitui a integridade do registo. Em produção, com `AOS_DURABLE_EXECUTION=1`,
// o capturer sela a resposta com AES-256-GCM por-titular (AOS-093) e o replay decifra
// fail-closed — uma adulteração falha a autenticação do GCM. A âncora fecha o buraco na
// camada que o MEDE, em vez de o deixar depender de o armazenamento estar cifrado; nas
// fixtures, que são texto-claro inline, não havia nada.

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	"github.com/aos-ref/substrate/eventstore"
)

// leitorComTroca embrulha um EventReader e reescreve o payload dos eventos EM VOO. O Event
// Store fica intacto — o que muda é o que o motor de replay lê, que é a forma de simular um
// registo adulterado sem precisar de escrever num log append-only.
type leitorComTroca struct {
	base    replay.EventReader
	de      []byte
	para    []byte
	tipo    string
	turno   int
	aplicou int
}

func (l *leitorComTroca) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	evs, err := l.base.Read(ctx, streamID, fromSeq)
	if err != nil {
		return nil, err
	}
	out := make([]eventstore.Event, len(evs))
	for i, e := range evs {
		cp := e
		cp.Payload = append(json.RawMessage(nil), e.Payload...)
		if cp.Type == l.tipo && bytes.Contains(cp.Payload, l.de) {
			var cab struct {
				Turn int `json:"turn"`
			}
			if json.Unmarshal(cp.Payload, &cab) == nil && cab.Turn == l.turno {
				cp.Payload = bytes.Replace(cp.Payload, l.de, l.para, 1)
				l.aplicou++
			}
		}
		out[i] = cp
	}
	return out, nil
}

// TestAncoraDeDesfecho_TextoFinalAdulteradoEhApanhado é o teste que nasceu VERMELHO: antes da
// âncora, este mesmo caso devolvia pass=true.
func TestAncoraDeDesfecho_TextoFinalAdulteradoEhApanhado(t *testing.T) {
	f, err := BuildEchoGolden("golden_desfecho_adulterado")
	if err != nil {
		t.Fatalf("BuildEchoGolden: %v", err)
	}
	defer f.Close()

	c := f.Case()
	if c.ExpectedFinalText == "" {
		t.Fatal("a fixture não trouxe âncora de desfecho — sem ela este teste não prova nada")
	}
	troca := &leitorComTroca{
		base: c.Reader, tipo: "replay.captured", turno: 3,
		de: []byte(`"text":"concluído"`), para: []byte(`"text":"ADULTERADO"`),
	}
	c.Reader = troca

	rep, err := Verify(context.Background(), c)
	if err != nil {
		t.Fatalf("Verify (operacional): %v", err)
	}
	// Uma mutação que não aplica lê-se como robustez do sistema e nunca como teste fraco.
	if troca.aplicou == 0 {
		t.Fatal(">>> A ADULTERAÇÃO NÃO APLICOU <<< o payload não continha o texto procurado")
	}
	if rep.Pass {
		t.Fatalf("harness aceitou um run com o DESFECHO adulterado:\n%s", rep.JSON())
	}
	if !rep.OutcomeMismatch {
		t.Fatalf("esperava OutcomeMismatch, obtive:\n%s", rep.JSON())
	}
	if rep.Err() == nil {
		t.Fatal("Err() devia ser não-nil com o desfecho a divergir")
	}
	// A NATUREZA da falha tem de ficar clara: o replay continua fiel turno a turno — a
	// divergência não está no prompt. Confundir as duas mandaria quem diagnostica para o
	// lado errado.
	if rep.Diverged || rep.ReplayFidelity != 1.0 {
		t.Fatalf("a falha é de DESFECHO, não de replay: diverged=%v fidelidade=%v", rep.Diverged, rep.ReplayFidelity)
	}
}

// TestAncoraDeDesfecho_LogIntactoContinuaVerde é a metade que impede a âncora de virar um
// falso positivo permanente. Um mecanismo que reprova tudo não distingue nada.
func TestAncoraDeDesfecho_LogIntactoContinuaVerde(t *testing.T) {
	agg, closer, err := GoldenReport(context.Background())
	if err != nil {
		t.Fatalf("GoldenReport: %v", err)
	}
	defer closer()

	if err := agg.Err(); err != nil {
		t.Fatalf("o golden set com âncora deixou de passar: %v\n%s", err, agg.JSON())
	}
	for _, c := range agg.Cases {
		if !c.OutcomeAnchored {
			t.Errorf("caso %q sem âncora de desfecho — a verificação não correu, e um pass "+
				"sem âncora é indistinguível de um pass com âncora satisfeita", c.Name)
		}
	}
}

// TestAncoraDeDesfecho_SemAncoraOsBytesNaoMudam fixa a retro-compatibilidade. Os dois campos
// novos são `omitempty`: sem âncora ficam false e OMITIDOS, logo o relatório serializa
// EXACTAMENTE como antes — o que importa porque o gate 8 lê esta linha e ancora o veredicto
// ao `"pass"` FINAL.
func TestAncoraDeDesfecho_SemAncoraOsBytesNaoMudam(t *testing.T) {
	rep := FidelityReport{
		Name: "n", RunID: "r", Turns: 1, ReplayFidelity: 1.0,
		EffectsVerified: 1, ResumePoints: 1, Pass: true,
	}
	got := string(rep.CompactJSON())
	quero := `{"name":"n","run_id":"r","turns":1,"replay_fidelity":1,"diverged":false,` +
		`"effects_verified":1,"duplicated_effects":0,"resume_points_verified":1,` +
		`"resume_mismatches":0,"pass":true}`
	if got != quero {
		t.Errorf("os bytes do relatório mudaram sem âncora.\nobtido: %s\nquero : %s", got, quero)
	}

	// E com âncora satisfeita, o campo aparece ANTES do "pass" — a âncora do gate 8
	// ("pass":<bool>} no FIM) tem de continuar a valer.
	comAncora := rep
	comAncora.OutcomeAnchored = true
	linha := string(comAncora.CompactJSON())
	if !bytes.HasSuffix([]byte(linha), []byte(`"pass":true}`)) {
		t.Errorf("o \"pass\" deixou de ser o último campo — o gate 8 ancora nele: %s", linha)
	}
	if !bytes.Contains([]byte(linha), []byte(`"outcome_anchored":true`)) {
		t.Errorf("a âncora satisfeita não é visível no relatório: %s", linha)
	}
}

// TestAncoraDeDesfecho_RelatorioDeclaraAsAncorasDoReplay é o irmão do
// [TestAncoraDeDesfecho_LogIntactoContinuaVerde] para as comparações NÃO-prompt: as três
// são opt-in no replay e uma spec que as omita desliga-as EM SILÊNCIO. O relatório passa a
// dizer quais correram, pela MESMA razão que diz se a âncora de desfecho correu.
func TestAncoraDeDesfecho_RelatorioDeclaraAsAncorasDoReplay(t *testing.T) {
	f, err := BuildEchoGolden("golden_ancoras_do_replay")
	if err != nil {
		t.Fatalf("BuildEchoGolden: %v", err)
	}
	defer f.Close()
	c := f.Case()

	rep, err := Verify(context.Background(), c)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	quero := map[string]bool{"model": true, "assembly_version": true}
	for _, a := range rep.AnchorsVerified {
		delete(quero, a)
	}
	if len(quero) != 0 {
		t.Fatalf("a fixture re-fornece Model e AssemblyVersion, logo as duas âncoras correm; "+
			"o relatório declarou %v", rep.AnchorsVerified)
	}

	// E uma spec sem elas continua VERDE — o opt-in mantém-se — mas o relatório deixa de
	// afirmar comparações que não fez. É o contraste inteiro deste campo.
	fraca := c
	fraca.Spec.Model = agentruntime.ModelConfig{}
	fraca.Spec.AssemblyVersion = ""
	repFraco, err := Verify(context.Background(), fraca)
	if err != nil {
		t.Fatalf("Verify (spec fraca): %v", err)
	}
	if !repFraco.Pass {
		t.Fatalf("o opt-in mantém-se: a spec fraca devia continuar a passar\n%s", repFraco.JSON())
	}
	if len(repFraco.AnchorsVerified) != 0 {
		t.Fatalf("spec sem Model nem AssemblyVersion: âncoras = %v, queria nenhuma", repFraco.AnchorsVerified)
	}
}
