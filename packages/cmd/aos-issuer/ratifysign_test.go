package main

// AOS-275 — prova de que `ratify-sign` produz uma ratificação que o nó pode MESMO verificar.
//
// O risco que este teste cobre é o único que a ferramenta tem: assinar o canónico ERRADO. Uma
// assinatura sintacticamente perfeita sobre outros bytes seria indistinguível de uma correcta
// até ao momento em que o nó respondesse 403 `ratification_forged` — e o operador não teria como
// saber porquê. As duas asserções abaixo fecham-no sem depender do nó:
//
//	(1) o `request_id` emitido É o [hitl.SelfModArtifact.RatificationID] do artefacto RECONSTRUÍDO
//	    a partir do próprio JSON de saída (anti-transplante: se um campo do artefacto não entrasse
//	    no canónico, ou entrasse por outra ordem, o id divergia);
//	(2) a assinatura emitida é BYTE-A-BYTE a que [hitl.SignApproval] produz para a mesma decisão
//	    (ed25519 é determinístico) — ou seja, cobre exactamente o canónico que o gate verifica.
//
// Nenhuma chave é fixa em código: a seed vem de CSPRNG e vive num ficheiro temporário.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hitl "github.com/aos-ref/control-plane/governance/hitl"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// ratifyTestKey gera um par ed25519 e escreve a SEED em hex num ficheiro temporário (o formato
// que --key-file espera). Devolve o caminho e a privada (para a re-assinatura de controlo).
func ratifyTestKey(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	path := filepath.Join(t.TempDir(), "ratifier.seed")
	if err := os.WriteFile(path, []byte(hex.EncodeToString(priv.Seed())), 0o600); err != nil {
		t.Fatalf("escrever seed: %v", err)
	}
	return path, priv
}

func TestRatifySign_ProduzRatificacaoVerificavel(t *testing.T) {
	keyFile, priv := ratifyTestKey(t)
	const ratifier = "human:ratifier-alice"
	contentHash := []byte{0xde, 0xad, 0xbe, 0xef}
	traceHex := "0102030405060708090a0b0c0d0e0f10"

	var out bytes.Buffer
	err := runRatifySign([]string{
		"--artifact-id", "skill.summarize",
		"--kind", "skill",
		"--version", "1.4.0",
		"--content-hash", base64.StdEncoding.EncodeToString(contentHash),
		"--canary-passed",
		"--eval-suite", "golden.summarize",
		"--eval-id", "eval-abc-123",
		"--eval-dataset", "golden",
		"--eval-verdict", "pass",
		"--eval-score", "0.95",
		"--eval-trace", traceHex,
		"--ratifier", ratifier,
		"--key-file", keyFile,
	}, &out)
	if err != nil {
		t.Fatalf("ratify-sign: %v", err)
	}

	var body promoteBodyWire
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("saida nao e o corpo JSON de POST /promote: %v (%s)", err, out.String())
	}
	// A saída NUNCA pode conter material de chave privada — a seed vive só no ficheiro montado.
	if strings.Contains(out.String(), hex.EncodeToString(priv.Seed())) {
		t.Fatal("a saida contem a SEED do ratificador — a privada nunca sai da ferramenta")
	}

	// (1) RECONSTRUÇÃO do artefacto A PARTIR DO WIRE emitido, e comparação do RatificationID.
	hashBytes, err := base64.StdEncoding.DecodeString(body.Artifact.ContentHash)
	if err != nil {
		t.Fatalf("content_hash emitido nao e base64: %v", err)
	}
	artifact := hitl.SelfModArtifact{
		ID:      body.Artifact.ID,
		Kind:    hitl.ArtifactKind(body.Artifact.Kind),
		Version: body.Artifact.Version,
		EvalResult: otelgenai.EvaluationResult{
			Suite:   body.Artifact.Eval.Suite,
			EvalID:  body.Artifact.Eval.EvalID,
			Dataset: otelgenai.EvalDataset(body.Artifact.Eval.Dataset),
			Verdict: otelgenai.EvalVerdict(body.Artifact.Eval.Verdict),
			Score:   body.Artifact.Eval.Score,
		},
		CanaryPassed: body.Artifact.CanaryPassed,
		ContentHash:  hashBytes,
	}
	traceBytes, err := hex.DecodeString(body.Artifact.Eval.TargetTraceID)
	if err != nil || len(traceBytes) != len(artifact.EvalResult.TargetTraceID) {
		t.Fatalf("target_trace_id emitido invalido: %q (%v)", body.Artifact.Eval.TargetTraceID, err)
	}
	copy(artifact.EvalResult.TargetTraceID[:], traceBytes)

	if body.Ratification.RequestID != artifact.RatificationID() {
		t.Fatalf("request_id emitido = %q, quero o RatificationID do artefacto = %q — sem esta igualdade o no responde ratification_transplant",
			body.Ratification.RequestID, artifact.RatificationID())
	}
	if body.Ratification.Ratifier != ratifier || !body.Ratification.Approved {
		t.Fatalf("ratificacao emitida = (%q, approved=%v), quero (%q, true)", body.Ratification.Ratifier, body.Ratification.Approved, ratifier)
	}

	// (2) RE-ASSINATURA DE CONTROLO: os MESMOS campos, assinados por [hitl.SignApproval] com a
	// mesma chave, têm de dar a MESMA assinatura (ed25519 é determinístico). Se a ferramenta
	// assinasse outros bytes — outro domínio, outro campo, outra ordem — divergiria aqui.
	nonce, err := base64.StdEncoding.DecodeString(body.Ratification.Nonce)
	if err != nil {
		t.Fatalf("nonce emitido nao e base64: %v", err)
	}
	if len(nonce) != ratifyNonceLen {
		t.Fatalf("nonce com %d bytes, quero %d (uso-unico duravel)", len(nonce), ratifyNonceLen)
	}
	issuedAt, err := time.Parse(time.RFC3339Nano, body.Ratification.IssuedAt)
	if err != nil {
		t.Fatalf("issued_at emitido nao e RFC3339: %v", err)
	}
	control, err := hitl.SignApproval(context.Background(), ed25519Signer{priv: priv}, hitl.SignedApproval{
		RequestID: body.Ratification.RequestID,
		Approver:  body.Ratification.Ratifier,
		Approved:  body.Ratification.Approved,
		Nonce:     nonce,
		IssuedAt:  issuedAt,
	})
	if err != nil {
		t.Fatalf("re-assinatura de controlo: %v", err)
	}
	if got := base64.StdEncoding.EncodeToString(control.Signature); got != body.Ratification.Signature {
		t.Fatalf("a assinatura emitida NAO cobre o canonico de hitl.SignApproval:\nemitida  = %s\ncontrolo = %s", body.Ratification.Signature, got)
	}
}

// TestRatifySign_FailClosed prova que a ferramenta recusa, ao pé do operador, as entradas que o
// nó só poderia recusar com um erro criptográfico enigmático — ou, pior, que produziriam em
// silêncio uma ratificação para OUTRA identidade de artefacto.
func TestRatifySign_FailClosed(t *testing.T) {
	keyFile, _ := ratifyTestKey(t)
	base := []string{
		"--artifact-id", "skill.summarize",
		"--version", "1.4.0",
		"--content-hash", base64.StdEncoding.EncodeToString([]byte{1, 2, 3}),
		"--ratifier", "human:alice",
		"--key-file", keyFile,
	}
	semArg := func(nome string) []string {
		out := make([]string, 0, len(base))
		for i := 0; i < len(base); i += 2 {
			if base[i] == nome {
				continue
			}
			out = append(out, base[i], base[i+1])
		}
		return out
	}
	casos := []struct {
		nome string
		args []string
	}{
		{"sem artifact-id", semArg("--artifact-id")},
		{"sem version", semArg("--version")},
		{"sem content-hash", semArg("--content-hash")},
		{"sem ratifier", semArg("--ratifier")},
		{"sem key-file", semArg("--key-file")},
		{"kind desconhecido", append(append([]string{}, base...), "--kind", "skills")},
		{"content-hash nao-base64", append(semArg("--content-hash"), "--content-hash", "nao!!base64")},
		{"eval-trace curto", append(append([]string{}, base...), "--eval-trace", "abcd")},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			var out bytes.Buffer
			if err := runRatifySign(c.args, &out); err == nil {
				t.Fatalf("%s devia recusar fail-closed; saida = %s", c.nome, out.String())
			}
			if out.Len() != 0 {
				t.Fatalf("%s recusou mas escreveu na saida: %s", c.nome, out.String())
			}
		})
	}
}
