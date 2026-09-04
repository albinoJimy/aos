package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aos-ref/integration"
	"github.com/aos-ref/kernel/agent-runtime/control"
)

// runChallengeSign produz o CORPO ASSINADO de um POST /runs/{run}/challenge (AOS-308).
//
// Desde AOS-308 o no so emite um challenge a pedido do PROPRIO aprovador que o vai usar, assinado
// com a chave pinada no roster (AOS_APPROVERS_FILE). A cerimonia passa a ser: o aprovador assina o
// pedido de challenge (aqui) -> o no emite -> o aprovador assina a perna com esse challenge
// (`approve-sign --challenge`). A chave privada nunca sai desta maquina.
func runChallengeSign(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("challenge-sign", flag.ContinueOnError)
	approver := fs.String("approver", "", "id do aprovador (o mesmo principal de AOS_APPROVERS_FILE) — e quem assina")
	keyFile := fs.String("key-file", "", "ficheiro com a seed ed25519 (32 bytes em hex) do aprovador")
	runID := fs.String("run", "", "id do run da cerimonia (o {id} de /runs/{id}/challenge)")
	requestID := fs.String("request-id", "", "request_id da cerimonia de aprovacao")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*approver) == "" || strings.TrimSpace(*keyFile) == "" {
		return errors.New("challenge-sign exige --approver e --key-file")
	}
	if strings.TrimSpace(*runID) == "" || strings.TrimSpace(*requestID) == "" {
		return errors.New("challenge-sign exige --run e --request-id")
	}
	priv, err := loadApproverKey(*keyFile)
	if err != nil {
		return err
	}
	payload := integration.CanonicalChallengePayload(strings.TrimSpace(*runID), strings.TrimSpace(*requestID), strings.TrimSpace(*approver))
	em, err := integration.SignEmitter(strings.TrimSpace(*approver), priv,
		integration.ChallengeRequestScope, control.SignalChallenge, payload, time.Now().UTC())
	if err != nil {
		return err
	}
	corpo, err := json.Marshal(map[string]any{
		"emitter":    emitterJSON(em),
		"request_id": strings.TrimSpace(*requestID),
		"approver":   strings.TrimSpace(*approver),
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(corpo))
	return err
}
