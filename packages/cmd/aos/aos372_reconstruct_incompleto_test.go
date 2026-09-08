package main

// AOS-372 (EPIC-18) — O READ-PATH SOBERANO NÃO SERVE 200 SOBRE UMA TRAJECTÓRIA INCOMPLETA.
//
// O defeito: `Reconstruct` construía `order` a partir das capturas (replay.captured). Um turno com
// `turn.recorded` mas com o `replay.captured` INTEIRAMENTE ausente desaparecia em silêncio —
// `Reconstruct` devolvia MENOS turnos com err=nil, e GET /runs/{id}/reconstruct respondia 200 com
// uma trajectória curta (um auditor via menos turnos, sem aviso). Depois do fix, `Reconstruct`
// recusa a DIVERGÊNCIA fail-closed com ErrIncompleteCapture, e o handler mapeia-o a 422 (a
// trajectória EXISTE mas está incompleta), com corpo uniforme que nunca vaza o conteúdo.
//
// Correr SEMPRE com -race.

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// aos372Secret é o conteúdo sintético do turno CAPTURADO que uma resposta 422 uniforme NUNCA pode
// deixar escapar.
const aos372Secret = "PROMPT-372: contactar SUJEITO-ZORG-372 sobre caso SYNTH-372"

// TestNode_AOS372_ReconstructIncompletoDa422 é a AC4.
//
// Injecta um run com um turno CAPTURADO (turno 1, selado por-titular) e, a seguir, um `turn.recorded`
// SOLITÁRIO para o turno 2 SEM o `replay.captured` correspondente — a divergência que o defeito
// deixava passar em silêncio. O endpoint soberano tem de recusar com 422 (não 200 curto) e o corpo
// não pode conter o conteúdo decifrado.
func TestNode_AOS372_ReconstructIncompletoDa422(t *testing.T) {
	node := newGovDurableNode(t)
	_, h := newAPI(t, node)
	const subject, runID = "nhi:agent-372", "run-372-incompleto"

	// Turno 1: captura REAL, selada por-titular (emite replay.captured para o turno 1).
	captureSynthetic(t, node, subject, runID, aos372Secret, "TOOL-OUT-372")

	// Turno 2: um turn.recorded SOLITÁRIO, sem qualquer replay.captured — a DIVERGÊNCIA. StepID
	// distinto para não colidir com a deduplicação run_id:step_id do Event Store.
	rec := agentruntime.NewTurnRecorder(node.EventStore)
	if _, err := rec.Record(context.Background(), agentruntime.TurnRecord{
		RunID:  runID,
		StepID: "step-000002",
		Turn:   2,
		Manifest: agentruntime.Manifest{
			PromptHash: "sha256:aos372-turno2",
			Model:      agentruntime.ModelManifest{ModelID: "claude-opus-4-8", Seed: 42},
		},
		Final: true,
	}); err != nil {
		t.Fatalf("Record turn.recorded solitário: %v", err)
	}

	resp := getReq(h, "/runs/"+runID+"/reconstruct", govHeaders())
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("uma trajectória incompleta (turno com turn.recorded sem replay.captured) devia dar 422, veio %d (%s)", resp.Code, resp.Body.String())
	}
	// O corpo é uniforme ("reconstrucao indisponivel") e NUNCA carrega o conteúdo decifrado.
	if bytes.Contains(resp.Body.Bytes(), []byte(aos372Secret)) {
		t.Fatalf("a resposta 422 vaza o conteúdo decifrado: %q", resp.Body.String())
	}
	if bytes.Contains(resp.Body.Bytes(), []byte("TOOL-OUT-372")) {
		t.Fatalf("a resposta 422 vaza o output de tool decifrado: %q", resp.Body.String())
	}
}

// TestNode_AOS372_ReconstructCompletoContinua200 é o controlo de NÃO-VACUIDADE do AC4: sem o
// turn.recorded solitário, o MESMO run (só o turno 1 capturado) reconstrói com 200. Sem isto, um
// handler avariado que devolvesse sempre 422 passaria no teste acima.
func TestNode_AOS372_ReconstructCompletoContinua200(t *testing.T) {
	node := newGovDurableNode(t)
	_, h := newAPI(t, node)
	const subject, runID = "nhi:agent-372-ok", "run-372-completo"
	captureSynthetic(t, node, subject, runID, aos372Secret, "TOOL-OUT-372-OK")

	resp := getReq(h, "/runs/"+runID+"/reconstruct", govHeaders())
	if resp.Code != http.StatusOK {
		t.Fatalf("um run sem divergência devia dar 200, veio %d (%s)", resp.Code, resp.Body.String())
	}
}
