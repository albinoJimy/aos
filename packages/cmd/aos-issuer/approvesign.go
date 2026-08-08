package main

// approve-sign — assina UMA perna de aprovação four-eyes (AOS-162/AOS-021) FORA do nó.
//
// PORQUE ESTA FERRAMENTA EXISTE: o four-eyes é NON-SIGNING por desenho — o nó nunca assina
// nada e nunca vê uma chave privada de aprovador; só verifica assinaturas contra pubkeys
// pinadas. Faltava, porém, o outro lado: nada no repositório produzia a perna assinada que
// o `POST /runs/{id}/approve` exige. Sem isto, o endpoint existia mas NENHUM operador o
// conseguia usar — o mesmo tipo de lacuna "mecanismo sem via de acesso" que já apareceu
// noutras partes do sistema.
//
// A chave privada do aprovador é lida de um ficheiro montado e NUNCA sai daqui: o que se
// imprime é a perna (principal, sessão, credencial, challenge, assinatura), pronta a
// colar no corpo do pedido.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	integration "github.com/aos-ref/integration"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// legWire é a face de wire de uma perna, igual à que a API do nó aceita.
type legWire struct {
	Approver   string `json:"approver"`
	Session    string `json:"session"`
	Credential string `json:"credential"`
	Challenge  string `json:"challenge"`
	Signature  string `json:"signature"`
}

// runApproveSign assina uma perna e imprime-a em JSON.
func runApproveSign(args []string) error {
	fs := flag.NewFlagSet("approve-sign", flag.ContinueOnError)
	requestID := fs.String("request-id", "", "id do pedido de aprovacao (ambito anti-replay dos challenges)")
	preview := fs.String("preview", "", "preview em base64 — o digest da accao, tal como GET /runs/{id} o expoe")
	riskClass := fs.String("risk-class", "danger", "classe de risco: safe|gray|danger")
	dual := fs.Bool("dual", true, "acçao irreversivel: exige DUAS pernas estruturalmente distintas")
	approver := fs.String("approver", "", "principal do aprovador (ex.: human:alice)")
	session := fs.String("session", "", "identificador da SESSAO do aprovador (eixo 2 da distincao)")
	credential := fs.String("credential", "", "identificador da CREDENCIAL que assina (eixo 3 da distincao)")
	keyFile := fs.String("key-file", "", "ficheiro com a seed ed25519 (32 bytes em hex) do aprovador")
	challengeHex := fs.String("challenge", "", "challenge em hex (>=32 bytes); vazio ⇒ derivado do request-id+approver")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(*requestID) == "":
		return errors.New("approve-sign: --request-id obrigatorio")
	case strings.TrimSpace(*preview) == "":
		return errors.New("approve-sign: --preview obrigatorio (base64, de GET /runs/{id})")
	case strings.TrimSpace(*approver) == "":
		return errors.New("approve-sign: --approver obrigatorio")
	case strings.TrimSpace(*session) == "":
		return errors.New("approve-sign: --session obrigatorio (duas pernas da MESMA sessao nao sao four-eyes)")
	case strings.TrimSpace(*credential) == "":
		return errors.New("approve-sign: --credential obrigatorio (a mesma credencial nao pode assinar as duas pernas)")
	case strings.TrimSpace(*keyFile) == "":
		return errors.New("approve-sign: --key-file obrigatorio")
	}

	previewBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*preview))
	if err != nil {
		return fmt.Errorf("approve-sign: preview invalido (esperado base64): %w", err)
	}
	priv, err := loadApproverKey(*keyFile)
	if err != nil {
		return err
	}
	challenge, err := resolveChallenge(*challengeHex, *requestID, *approver)
	if err != nil {
		return err
	}

	req := integration.FourEyesRequest{
		RequestID:           strings.TrimSpace(*requestID),
		Preview:             previewBytes,
		RiskClass:           parseRiskClass(*riskClass),
		DualControlRequired: *dual,
	}
	// A assinatura cobre o TUPLO canónico (pedido + perna): uma perna assinada para outro
	// preview, outra classe de risco ou outro modo dual NÃO valida — é o que fecha o relay
	// e o downgrade dual→single.
	leg := integration.SignFourEyesLeg(priv, req, strings.TrimSpace(*approver),
		strings.TrimSpace(*session), strings.TrimSpace(*credential), challenge, nil)

	out, err := json.Marshal(legWire{
		Approver:   leg.Approver,
		Session:    leg.Session,
		Credential: leg.Credential,
		Challenge:  base64.StdEncoding.EncodeToString(leg.Challenge),
		Signature:  base64.StdEncoding.EncodeToString(leg.Signature),
	})
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

// loadApproverKey lê a seed ed25519 (32 bytes em hex) e deriva a chave privada.
func loadApproverKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("approve-sign: ler chave do aprovador: %w", err)
	}
	seed, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("approve-sign: chave do aprovador nao e hex: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("approve-sign: seed com %d bytes, esperado %d", len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// resolveChallenge usa o challenge dado ou deriva um determinista de (request, aprovador).
//
// NOTA DE SEGURANÇA: um challenge DERIVADO só dá anti-replay de uso-único (o gate recusa
// o mesmo challenge duas vezes no mesmo pedido); NÃO prova frescura. A frescura real exige
// o modo issue-then-consume do gate (WithChallengeIssuance), em que o servidor EMITE o
// challenge por (pedido, aprovador) com TTL. Para uma cerimónia a sério, passe --challenge
// com o valor emitido pelo servidor.
func resolveChallenge(given, requestID, approver string) ([]byte, error) {
	if s := strings.TrimSpace(given); s != "" {
		b, err := hex.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("approve-sign: challenge nao e hex: %w", err)
		}
		if len(b) < 32 {
			return nil, fmt.Errorf("approve-sign: challenge com %d bytes, minimo 32", len(b))
		}
		return b, nil
	}
	sum := sha256.Sum256([]byte("aos-approve-challenge|" + requestID + "|" + approver))
	return sum[:], nil
}

// parseRiskClass traduz o nome da classe; desconhecido ⇒ danger (fail-closed, coerente
// com o valor-zero de risk.Class).
func parseRiskClass(s string) risk.Class {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "safe":
		return risk.ClassSafe
	case "gray":
		return risk.ClassGray
	default:
		return risk.ClassDanger
	}
}
