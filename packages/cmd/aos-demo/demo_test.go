package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestAposDemoComposesAndRuns exercita o ápice mínimo (AOS-149) end-to-end: corre
// runDemo contra um buffer in-process (ZERO rede, ZERO processo) e assere que não
// devolve erro e que o output contém os marcos esperados de cada pilar composto e de
// cada passo do fluxo de demonstração.
func TestAposDemoComposesAndRuns(t *testing.T) {
	var buf bytes.Buffer
	if err := runDemo(&buf); err != nil {
		t.Fatalf("runDemo devolveu erro: %v", err)
	}
	out := buf.String()

	// Marcos esperados: os sete pilares compostos pela ordem da AC, mais os passos
	// do fluxo (reflexão do estado durável, turno corrido, render de superfície e o
	// steer/pause out-of-band) e as duas limitações reimpressas.
	marcos := []string{
		"1) Event Store composto",
		"2) Authenticator HMAC composto",
		"3) SteerChannel composto",
		"4) State Machine do run",
		"5) StateProjector composto",
		"6) Modelo FAKE composto",
		"7) Reference Monitor (stubs neutros)",
		"transição durável ready → running",
		"Current() = running",              // reflexão do estado durável via o StateProjector
		"terminated=true",                  // o turno correu e terminou (modelo fake)
		"superfície renderizada (desktop)", // render via o Renderer do surface-adapter
		"steer OUT-OF-BAND despachado",     // steer out-of-band no SteerChannel
		"eco de pausa pendente: true",      // pause out-of-band ecoado
		"LIMITAÇÃO (a)",                    // loop não consome o SteerChannel (AOS-158)
		"LIMITAÇÃO (b)",                    // RM usa stubs neutros (AOS-152/153/154)
		"demo concluído com sucesso",
	}
	for _, m := range marcos {
		if !strings.Contains(out, m) {
			t.Errorf("output do demo não contém o marco esperado %q\n---output---\n%s", m, out)
		}
	}
}
