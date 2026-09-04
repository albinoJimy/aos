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
	// SEGUNDA ASSINATURA (AOS-305): mudar PARA L4/L5 exige dois operadores DISTINTOS com
	// autonomy:set. Os dois assinam o MESMO payload, cada um com o seu nonce; o no recusa
	// (403) uma unica assinatura para esse limiar. Ambas as flags, ou nenhuma.
	coEmitterID := fs.String("co-emitter", "", "id do SEGUNDO operador (obrigatorio para --level L4/L5)")
	coKeyFile := fs.String("co-key-file", "", "ficheiro com a seed ed25519 do segundo operador")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*emitterID) == "" || strings.TrimSpace(*keyFile) == "" {
		return errors.New("autonomy-sign exige --emitter e --key-file")
	}
	if (strings.TrimSpace(*coEmitterID) == "") != (strings.TrimSpace(*coKeyFile) == "") {
		return errors.New("autonomy-sign: --co-emitter e --co-key-file vao juntos (ambos ou nenhum)")
	}
	if strings.TrimSpace(*coEmitterID) != "" && strings.TrimSpace(*coEmitterID) == strings.TrimSpace(*emitterID) {
		return errors.New("autonomy-sign: --co-emitter tem de ser um operador DISTINTO de --emitter (duas pessoas, nao duas assinaturas da mesma)")
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

	body := map[string]any{
		"emitter": emitterJSON(em),
		"agent":   strings.TrimSpace(*agent),
		"domain":  strings.TrimSpace(*domain),
		"level":   nivel,
		"reason":  strings.TrimSpace(*reason),
	}
	if strings.TrimSpace(*coEmitterID) != "" {
		coPriv, err := loadApproverKey(*coKeyFile)
		if err != nil {
			return fmt.Errorf("co-key-file: %w", err)
		}
		co, err := integration.SignEmitter(strings.TrimSpace(*coEmitterID), coPriv,
			integration.AutonomyScope, control.SignalAutonomy, payload, time.Now().UTC())
		if err != nil {
			return err
		}
		body["co_emitter"] = emitterJSON(co)
	} else if nivel == "L4" || nivel == "L5" {
		// Aviso e não erro: o no e quem decide o limiar (e pode mudar). Mas quem assina sozinho
		// para L4/L5 vai receber 403, e e melhor sabe-lo antes de enviar do que depois.
		fmt.Fprintln(out, "# aviso: mudar para L4/L5 exige uma segunda assinatura (--co-emitter/--co-key-file); sem ela o no recusa (AOS-305)")
	}
	corpo, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(corpo))
	return err
}

// emitterJSON e a face de wire de um emissor assinado (o mesmo formato que o no descodifica em
// emitterWire). Partilhada pelas duas pernas para que nao divirjam.
func emitterJSON(em control.Emitter) map[string]any {
	return map[string]any{
		"id":        em.ID,
		"signature": base64.StdEncoding.EncodeToString(em.Signature),
		"nonce":     base64.StdEncoding.EncodeToString(em.Nonce),
		"issued_at": em.IssuedAt.Format(time.RFC3339Nano),
	}
}
