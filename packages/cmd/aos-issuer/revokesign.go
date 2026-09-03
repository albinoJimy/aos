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

// runRevokeSign produz o CORPO ASSINADO de um POST /nhi/revoke (AOS-288).
//
// Existe pela mesma razão que o `autonomy-sign`: para que quem revoga NÃO reimplemente o tuplo
// canónico. Duas implementações do mesmo encoding divergem, e a divergência aqui não dá um erro
// legível — dá «emissor nao autorizado», que se lê como chave errada e manda o operador
// procurar no sítio errado durante uma hora.
//
// A chave privada do operador NUNCA sai desta máquina: entra por --key-file, assina, e o que
// vai para a rede é a assinatura. O nó nunca detém material com que possa forjar uma revogação.
func runRevokeSign(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("revoke-sign", flag.ContinueOnError)
	emitterID := fs.String("emitter", "", "id do operador (o mesmo que consta de AOS_OPERATORS no no)")
	keyFile := fs.String("key-file", "", "ficheiro com a seed ed25519 (32 bytes em hex) do operador")
	jti := fs.String("jti", "", "jti do token NHI a revogar")
	reason := fs.String("reason", "", "motivo da revogacao — OBRIGATORIO, e fica SELADO na hash-chain")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*emitterID) == "" || strings.TrimSpace(*keyFile) == "" {
		return errors.New("revoke-sign exige --emitter e --key-file")
	}
	if strings.TrimSpace(*jti) == "" {
		return errors.New("revoke-sign exige --jti (o token a revogar)")
	}
	// O motivo é exigido AQUI e no nó, e não é redundância: exigi-lo só do lado do servidor
	// deixaria o operador descobrir a obrigação depois de assinar, e a tentação seria escrever
	// qualquer coisa para o pedido passar. Pedi-lo antes de assinar põe a justificação no
	// momento da decisão, que é onde ela existe.
	if strings.TrimSpace(*reason) == "" {
		return errors.New("revoke-sign exige --reason: uma revogacao sem motivo nao e auditavel")
	}

	priv, err := loadApproverKey(*keyFile)
	if err != nil {
		return err
	}
	payload := integration.CanonicalRevokePayload(strings.TrimSpace(*jti), strings.TrimSpace(*reason))
	em, err := integration.SignEmitter(strings.TrimSpace(*emitterID), priv,
		integration.RevokeScope, control.SignalRevoke, payload, time.Now().UTC())
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
		"jti":    strings.TrimSpace(*jti),
		"reason": strings.TrimSpace(*reason),
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(corpo))
	return err
}
