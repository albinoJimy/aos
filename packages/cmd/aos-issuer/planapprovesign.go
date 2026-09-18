package main

// plan-approve-sign — assina a decisão de um PLANO pendente (AOS-408), FORA do processo que a
// verifica.
//
// PORQUE ESTA FERRAMENTA EXISTE: o gate de aprovação de plano do `aos-orq` é non-signing por
// desenho (ADR-016 §1) — verifica assinaturas contra chaves públicas PINADAS e nunca vê uma chave
// privada de aprovador. Sem um comando que produza a decisão assinada, o gate existiria e nenhum
// operador o conseguiria usar: a mesma lacuna «mecanismo sem via de acesso» que o `approve-sign`
// fechou para as pernas four-eyes do nó.
//
// A chave privada é lida de um ficheiro montado e NUNCA sai daqui: o que se imprime é o JSON da
// decisão assinada, pronto a passar em `aos-orq decide --approval`.
//
// A AMARRA ao plano concreto é o `--request-id`, que o `aos-orq plans` imprime na forma
// `plan:<plan_id>:<plan_hash>`. Assinar cobre-o, pelo que uma decisão sobre um organigrama não
// pode ser reapresentada noutro — e um plano que mude de conteúdo muda de hash e invalida a
// assinatura.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aos-ref/control-plane/governance/hitl"
)

// planApprovalWire é a face JSON da decisão assinada — o que o `aos-orq decide` consome.
type planApprovalWire struct {
	RequestID string `json:"request_id"`
	Approver  string `json:"approver"`
	Approved  bool   `json:"approved"`
	Nonce     string `json:"nonce"`
	IssuedAt  string `json:"issued_at"`
	Signature string `json:"signature"`
}

// assinadorLocal é um [messaging.Signer] sobre uma chave ed25519 lida de ficheiro. Existe para a
// assinatura canónica ser produzida pela MESMA função que o verificador usa ([hitl.SignApproval]):
// duplicar aqui a serialização canónica seria criar uma segunda definição do que é uma decisão
// assinada, e as duas divergiriam no primeiro campo novo.
type assinadorLocal struct{ priv ed25519.PrivateKey }

func (a assinadorLocal) Sign(_ context.Context, _ string, message []byte) ([]byte, error) {
	return ed25519.Sign(a.priv, message), nil
}

// runPlanApproveSign assina a decisão e imprime-a em JSON.
func runPlanApproveSign(args []string) error {
	fs := flag.NewFlagSet("plan-approve-sign", flag.ContinueOnError)
	requestID := fs.String("request-id", "", "amarra ao plano: plan:<plan_id>:<plan_hash>, tal como `aos-orq plans` a imprime")
	approver := fs.String("approver", "", "principal do aprovador (ex.: human:alice), com a pubkey PINADA no registo do aos-orq")
	aprovar := fs.Bool("approve", false, "true aprova, false RECUSA — ambas são assinadas (uma recusa também é não-repúdio)")
	keyFile := fs.String("key-file", "", "ficheiro com a seed ed25519 (32 bytes em hex) do aprovador")
	saida := fs.String("out", "", "ficheiro onde escrever o JSON (vazio ⇒ stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(*requestID) == "":
		return errors.New("plan-approve-sign: --request-id obrigatorio (vem de `aos-orq plans`)")
	case strings.TrimSpace(*approver) == "":
		return errors.New("plan-approve-sign: --approver obrigatorio")
	case strings.TrimSpace(*keyFile) == "":
		return errors.New("plan-approve-sign: --key-file obrigatorio (a chave privada nunca viaja em flag)")
	}
	priv, err := lerSeedDeAprovador(*keyFile)
	if err != nil {
		return err
	}

	// NONCE FRESCO por decisão: é o que o `aos-orq` consome com CAS durável. Gerá-lo aqui (e não
	// pedi-lo ao operador) evita a classe de erro «reutilizei o nonce anterior», que o verificador
	// recusaria sem explicar porquê.
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("plan-approve-sign: nonce: %w", err)
	}

	assinada, err := hitl.SignApproval(context.Background(), assinadorLocal{priv: priv}, hitl.SignedApproval{
		RequestID: *requestID,
		Approver:  *approver,
		Approved:  *aprovar,
		Nonce:     nonce,
		IssuedAt:  time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("plan-approve-sign: %w", err)
	}

	raw, err := json.MarshalIndent(planApprovalWire{
		RequestID: assinada.RequestID,
		Approver:  assinada.Approver,
		Approved:  assinada.Approved,
		Nonce:     hex.EncodeToString(assinada.Nonce),
		// RFC3339Nano: o canónico assinado cobre o instante em UnixNano, e segundos truncariam-no.
		IssuedAt:  assinada.IssuedAt.Format(time.RFC3339Nano),
		Signature: hex.EncodeToString(assinada.Signature),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("plan-approve-sign: serializacao: %w", err)
	}
	if *saida == "" {
		fmt.Println(string(raw))
		return nil
	}
	if err := os.WriteFile(*saida, raw, 0o600); err != nil {
		return fmt.Errorf("plan-approve-sign: escrita em %q: %w", *saida, err)
	}
	fmt.Printf("decisao assinada escrita: %s (aprovada=%v)\n", *saida, assinada.Approved)
	return nil
}

// lerSeedDeAprovador lê a seed ed25519 (32 bytes em hex) e devolve a chave privada. Fail-closed:
// uma seed com o tamanho errado não é uma chave.
func lerSeedDeAprovador(caminho string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(caminho)
	if err != nil {
		return nil, fmt.Errorf("plan-approve-sign: chave do aprovador %q: %w", caminho, err)
	}
	seed, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("plan-approve-sign: chave do aprovador %q nao e hex: %w", caminho, err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("plan-approve-sign: chave do aprovador %q tem %d bytes, quero %d", caminho, len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
