package replay

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// AOS-507 — A FICHA DA FORMA NÃO ENTRA NA CAPTURA, E O REPLAY DE UM RUN GRAVADO COM ELA É FIEL.
//
// Os casos são os do diferencial do AOS-492. Cada um corre duas vezes — sem ficha e com uma ficha
// em todas as respostas — e exige: a parte em claro de cada captura é byte a byte a mesma nas
// duas corridas (o precedente do `ToolsOffered`); o `turn.recorded` da segunda leva a ficha; o
// replay da segunda é fiel e termina onde o loop terminou; e um turno reproduzido volta SEM ficha.
func TestAOS507_Diferencial_FichaForaDaCapturaEReplayFiel(t *testing.T) {
	tokens := int64(3)
	ficha := &agentruntime.ResponseShape{
		Content: agentruntime.ShapeContentText, ContentBytes: 9, Reasoning: agentruntime.ShapeReasoningNone,
		ReasoningSigned: agentruntime.ShapeNo, Refusal: agentruntime.ShapeRefusalAbsent, ToolCallID: agentruntime.ShapeIDNone,
		LegacyFunctionCall: agentruntime.ShapeNo, ChoicesN: 1, FinishReasonMapped: agentruntime.ShapeYes,
		ReasoningTokens: &tokens, SystemFingerprint: agentruntime.ShapeNo,
		ShapeDigest: "sha256:" + strings.Repeat("ab", 32),
	}
	capturas := func(b *aos489Bancada, runID string) (claro []string, comFicha int) {
		for _, ev := range b.eventos(runID) {
			switch ev.Type {
			case EventTypeCaptured:
				var campos map[string]json.RawMessage
				if err := json.Unmarshal(ev.Payload, &campos); err != nil {
					t.Fatalf("captura ilegivel: %v", err)
				}
				claro = append(claro, string(campos["response"]))
				if strings.Contains(string(ev.Payload), "shape") {
					t.Errorf("a captura leva a ficha: %s", ev.Payload)
				}
			case agentruntime.EventTypeTurnRecorded:
				if strings.Contains(string(ev.Payload), `"response_shape":{"content":"texto","content_bytes":9,`) {
					comFicha++
				}
			}
		}
		return claro, comFicha
	}
	for i, c := range aos492Casos() {
		t.Run(c.nome, func(t *testing.T) {
			b := novaBancada(t)
			semFicha := aos489Goal("run-aos507-sem-" + itoa(i))
			if _, err := b.correr(semFicha, c.guiao); err != nil {
				t.Fatalf("Run sem ficha: %v", err)
			}
			guiao := make([]agentruntime.ModelResponse, len(c.guiao))
			for j, r := range c.guiao {
				r.Shape = ficha
				guiao[j] = r
			}
			comFicha := aos489Goal("run-aos507-com-" + itoa(i))
			doLoop, err := b.correr(comFicha, guiao)
			if err != nil {
				t.Fatalf("Run com ficha: %v", err)
			}
			if !doLoop.Terminated || doLoop.Turns != c.turnos || doLoop.FinalText != c.texto {
				t.Fatalf("a ficha mudou o desfecho do loop: terminou=%v, %d turno(s), %q", doLoop.Terminated, doLoop.Turns, doLoop.FinalText)
			}
			claroSem, nSem := capturas(b, semFicha.RunID)
			claroCom, nCom := capturas(b, comFicha.RunID)
			if nSem != 0 || nCom != c.turnos {
				t.Fatalf("turn.recorded com ficha: %d sem a declarar e %d a declarar; quero 0 e %d", nSem, nCom, c.turnos)
			}
			if len(claroSem) == 0 || strings.Join(claroSem, "\n") != strings.Join(claroCom, "\n") {
				t.Fatalf("a parte em claro das capturas mudou com a ficha:\n sem: %v\n com: %v", claroSem, claroCom)
			}
			doReplay, err := b.motor().Replay(context.Background(), comFicha.RunID, Options{Spec: aos489SpecDe(comFicha)})
			exigirFiel(t, doReplay, err, c.turnos)
			if doReplay.Terminated != doLoop.Terminated || doReplay.FinalText != doLoop.FinalText {
				t.Fatalf("o replay nao termina onde o loop terminou")
			}
			for j, s := range doReplay.Steps {
				if s.Response.Shape != nil {
					t.Errorf("turno %d: um turno reproduzido volta sem ficha; veio %+v", j+1, s.Response.Shape)
				}
			}
		})
	}
}
