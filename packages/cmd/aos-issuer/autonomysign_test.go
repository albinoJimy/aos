package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// O STDOUT DO autonomy-sign É O CORPO, E SÓ O CORPO.
//
// Achado do E2E manual de 2026-09-15: sem --co-emitter, o aviso de L4/L5 saía no mesmo writer do
// JSON. Quem fazia o que a ajuda sugere (`B=$(aos-issuer autonomy-sign ...)`) enviava um corpo a
// começar por `#`, o nó respondia 400 "corpo invalido" e a recusa 403 que explica a falta da
// segunda assinatura (AOS-305) nunca chegava ao operador — o aviso escondia o erro que avisava.
func TestAutonomySign_AvisoL4L5NaoContaminaOCorpo(t *testing.T) {
	seed, _ := seedDeTeste(t)
	for _, nivel := range []string{"L4", "L5"} {
		t.Run(nivel, func(t *testing.T) {
			var out, diag bytes.Buffer
			if err := run([]string{"autonomy-sign",
				"--emitter", "op:jimy", "--key-file", seed,
				"--agent", "agt-1", "--domain", "fs", "--level", nivel, "--reason", "x",
			}, &out, &diag); err != nil {
				t.Fatalf("autonomy-sign: %v", err)
			}
			var corpo map[string]any
			if err := json.Unmarshal(out.Bytes(), &corpo); err != nil {
				t.Fatalf("o stdout nao e JSON valido (%v) — o no responderia 400 em vez do 403 "+
					"da segunda assinatura:\n%s", err, out.String())
			}
			if corpo["level"] != nivel {
				t.Errorf("level = %v, esperado %s", corpo["level"], nivel)
			}
			if _, tem := corpo["co_emitter"]; tem {
				t.Error("co_emitter presente sem --co-emitter")
			}
			// O aviso nao desaparece: muda de canal. Sem ele o operador so descobre a segunda
			// assinatura depois de enviar.
			if !strings.Contains(diag.String(), "segunda assinatura") {
				t.Errorf("o aviso de L4/L5 sem co-emissor devia sair no canal de diagnostico, veio %q",
					diag.String())
			}
		})
	}
}

// Contra-prova: com o co-emissor (e abaixo de L4) nao ha nada a avisar, e o canal de
// diagnostico fica vazio — o aviso nao e ruido constante que o operador aprenda a ignorar.
func TestAutonomySign_SemAvisoQuandoNaoHaNadaAAvisar(t *testing.T) {
	seed, _ := seedDeTeste(t)
	coSeed, _ := seedDeTeste(t)
	casos := map[string][]string{
		"L3 sozinho":        {"--level", "L3"},
		"L5 com co-emissor": {"--level", "L5", "--co-emitter", "op:maria", "--co-key-file", coSeed},
	}
	for nome, extra := range casos {
		t.Run(nome, func(t *testing.T) {
			var out, diag bytes.Buffer
			args := append([]string{"autonomy-sign",
				"--emitter", "op:jimy", "--key-file", seed,
				"--agent", "agt-1", "--domain", "fs", "--reason", "x",
			}, extra...)
			if err := run(args, &out, &diag); err != nil {
				t.Fatalf("autonomy-sign: %v", err)
			}
			if err := json.Unmarshal(out.Bytes(), &map[string]any{}); err != nil {
				t.Fatalf("o stdout nao e JSON valido: %v\n%s", err, out.String())
			}
			if diag.Len() != 0 {
				t.Errorf("nada a avisar, e o diagnostico trouxe %q", diag.String())
			}
		})
	}
}
