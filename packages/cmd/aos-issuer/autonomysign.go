package main

import (
	"encoding/base64"
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

// runAutonomySign produz o CORPO ASSINADO de um POST /autonomy.
//
// Existe pela mesma razão que o `delegation-nonce`: para que quem emite o pedido NÃO reimplemente
// o tuplo canónico. Duas implementações do mesmo encoding divergem, e a divergência aqui não dá
// um erro legível — dá "emissor nao autorizado", que se lê como chave errada e manda o operador
// procurar no sítio errado durante uma hora.
//
// A chave privada do operador NUNCA sai desta máquina: entra por --key-file, assina, e o que vai
// para a rede é a assinatura.
func runAutonomySign(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("autonomy-sign", flag.ContinueOnError)
	emitterID := fs.String("emitter", "", "id do operador (o mesmo que consta de AOS_OPERATORS no no)")
	keyFile := fs.String("key-file", "", "ficheiro com a seed ed25519 (32 bytes em hex) do operador")
	agent := fs.String("agent", "", "id do agente")
	domain := fs.String("domain", "", "dominio (ex.: fs, http) — o primeiro segmento da capability")
	level := fs.String("level", "", "nivel L0..L5")
	reason := fs.String("reason", "", "motivo da mudanca — OBRIGATORIO, e fica SELADO na hash-chain")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*emitterID) == "" || strings.TrimSpace(*keyFile) == "" {
		return errors.New("autonomy-sign exige --emitter e --key-file")
	}
	if strings.TrimSpace(*agent) == "" || strings.TrimSpace(*domain) == "" || strings.TrimSpace(*level) == "" {
		return errors.New("autonomy-sign exige --agent, --domain e --level")
	}
	// O motivo é exigido AQUI e no nó. Não é redundância: exigi-lo só do lado do servidor
	// deixaria o operador descobrir a obrigação depois de assinar, e a tentação seria escrever
	// qualquer coisa para o pedido passar. Pedi-lo antes de assinar põe a justificação no
	// momento da decisão, que é onde ela existe.
	if strings.TrimSpace(*reason) == "" {
		return errors.New("autonomy-sign exige --reason: uma mudanca de nivel sem motivo nao e auditavel")
	}
	nivel := strings.ToUpper(strings.TrimSpace(*level))
	switch nivel {
	case "L0", "L1", "L2", "L3", "L4", "L5":
	default:
		return fmt.Errorf("level invalido %q (esperado L0..L5)", *level)
	}

	priv, err := loadApproverKey(*keyFile)
	if err != nil {
		return err
	}
	payload := integration.CanonicalAutonomyPayload(
		strings.TrimSpace(*agent), strings.TrimSpace(*domain), nivel, strings.TrimSpace(*reason))
	em, err := integration.SignEmitter(strings.TrimSpace(*emitterID), priv,
		integration.AutonomyScope, control.SignalAutonomy, payload, time.Now().UTC())
	if err != nil {
		return err
	}

	corpo, err := json.Marshal(map[string]any{
		"emitter": map[string]any{
			"id":        em.ID,
			"signature": base64.StdEncoding.EncodeToString(em.Signature),
			"nonce":     base64.StdEncoding.EncodeToString(em.Nonce),
			"issued_at": em.IssuedAt.Format(time.RFC3339Nano),
		},
		"agent":  strings.TrimSpace(*agent),
		"domain": strings.TrimSpace(*domain),
		"level":  nivel,
		"reason": strings.TrimSpace(*reason),
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(corpo))
	return err
}
