package main

// ratify-sign — assina a RATIFICAÇÃO de um artefacto de auto-modificação (AOS-096/AOS-159/
// AOS-206) FORA do nó, e imprime o CORPO COMPLETO de `POST /promote` (AOS-275).
//
// PORQUE ESTA FERRAMENTA EXISTE: é a contraparte exacta do `approve-sign`. O promotion
// controller é NON-SIGNING pelo mesmo desenho do four-eyes — o nó verifica assinaturas contra
// pubkeys PINADAS (`AOS_RATIFIERS`) e nunca detém a chave privada de um ratificador. Sem esta
// ferramenta, a rota `POST /promote` existiria mas NENHUM humano conseguiria produzir o corpo:
// a assinatura cobre um canónico com separador de domínio e o `request_id` tem de ser o
// `RatificationID` do artefacto (SHA-256 do canónico do artefacto+eval) — nada disso se calcula
// à mão. Seria a MESMA lacuna "mecanismo sem via de acesso" que AOS-275 fecha, um nível acima.
//
// A chave privada do ratificador é lida de um ficheiro montado e NUNCA sai daqui: o que se
// imprime é a ratificação (principal, nonce, timestamp, assinatura) junto do artefacto que ela
// amarra — pronto a `POST`. O NONCE é gerado por CSPRNG a cada invocação: é ele que dá o
// uso-único durável do lado do nó (uma ratificação re-submetida ⇒ `ratification_replayed`), pelo
// que NÃO é configurável — reutilizá-lo seria fabricar um replay.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	hitl "github.com/aos-ref/control-plane/governance/hitl"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// ratifyNonceLen é a dimensão do nonce de uso-único (bytes). O gate exige um mínimo; 32 bytes de
// CSPRNG é a mesma folga usada no resto do sistema.
const ratifyNonceLen = 32

// promoteBodyWire espelha, campo a campo, o corpo que `POST /promote` aceita. O nó descodifica
// com `DisallowUnknownFields`, pelo que um nome fora de sítio aqui seria um 400 — o espelho é
// deliberado e tem de se manter em passo com promotion_api.go.
type promoteBodyWire struct {
	Artifact     artifactBodyWire     `json:"artifact"`
	Ratification ratificationBodyWire `json:"ratification"`
}

type artifactBodyWire struct {
	ID           string       `json:"id"`
	Kind         string       `json:"kind"`
	Version      string       `json:"version"`
	ContentHash  string       `json:"content_hash"`
	CanaryPassed bool         `json:"canary_passed"`
	Eval         evalBodyWire `json:"eval"`
}

type evalBodyWire struct {
	Suite         string  `json:"suite"`
	EvalID        string  `json:"eval_id"`
	Dataset       string  `json:"dataset"`
	Verdict       string  `json:"verdict"`
	Score         float64 `json:"score"`
	TargetTraceID string  `json:"target_trace_id,omitempty"`
}

type ratificationBodyWire struct {
	RequestID string `json:"request_id"`
	Ratifier  string `json:"ratifier"`
	Approved  bool   `json:"approved"`
	Nonce     string `json:"nonce"`
	IssuedAt  string `json:"issued_at"`
	Signature string `json:"signature"`
}

// ed25519Signer é o custodiante local da chave do ratificador: assina in-process e devolve SÓ a
// assinatura. Molde do `messaging.Signer` que [hitl.SignApproval] consome — a privada não
// atravessa a fronteira da função.
type ed25519Signer struct{ priv ed25519.PrivateKey }

func (s ed25519Signer) Sign(_ context.Context, _ string, message []byte) ([]byte, error) {
	if len(s.priv) != ed25519.PrivateKeySize {
		return nil, errors.New("ratify-sign: chave do ratificador invalida")
	}
	return ed25519.Sign(s.priv, message), nil
}

// runRatifySign constrói a identidade do artefacto, assina a ratificação e imprime o corpo de
// `POST /promote`.
func runRatifySign(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("ratify-sign", flag.ContinueOnError)
	artifactID := fs.String("artifact-id", "", "id do artefacto auto-escrito (ex.: skill.summarize)")
	kind := fs.String("kind", string(hitl.ArtifactSkill), "classe do artefacto: skill|procedural_memory")
	version := fs.String("version", "", "versao SemVer do artefacto — o humano ratifica ESTA versao")
	contentHash := fs.String("content-hash", "", "hash do CONTEUDO do artefacto, em base64 (nunca o conteudo)")
	canary := fs.Bool("canary-passed", false, "o artefacto passou a fase de canary (pre-condicao; false ⇒ o no recusa)")
	evalSuite := fs.String("eval-suite", "", "suite do eval-gate que avaliou esta versao")
	evalID := fs.String("eval-id", "", "id da execucao de avaliacao")
	evalDataset := fs.String("eval-dataset", string(otelgenai.EvalDatasetGolden), "origem do dataset: golden|failure_derived")
	evalVerdict := fs.String("eval-verdict", string(otelgenai.EvalPass), "veredicto do eval-gate: pass|fail")
	evalScore := fs.Float64("eval-score", 0, "score do eval (0..1) — o gate do no tem limiar proprio")
	evalTrace := fs.String("eval-trace", "", "trace_id-alvo da trajectoria avaliada, em hex de 16 bytes (opcional)")
	ratifier := fs.String("ratifier", "", "principal do ratificador (tem de constar de AOS_RATIFIERS)")
	keyFile := fs.String("key-file", "", "ficheiro com a seed ed25519 (32 bytes em hex) do ratificador")
	refuse := fs.Bool("refuse", false, "assina uma RECUSA em vez de uma aprovacao (tambem e nao-repudio e tambem e selada)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(*artifactID) == "":
		return errors.New("ratify-sign: --artifact-id obrigatorio")
	case strings.TrimSpace(*version) == "":
		return errors.New("ratify-sign: --version obrigatoria (a versao SemVer e selada no WORM)")
	case strings.TrimSpace(*contentHash) == "":
		return errors.New("ratify-sign: --content-hash obrigatorio (base64) — e o que amarra a ratificacao aos BYTES promovidos")
	case strings.TrimSpace(*ratifier) == "":
		return errors.New("ratify-sign: --ratifier obrigatorio")
	case strings.TrimSpace(*keyFile) == "":
		return errors.New("ratify-sign: --key-file obrigatorio")
	}
	artKind, err := parseArtifactKind(*kind)
	if err != nil {
		return err
	}
	hashBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*contentHash))
	if err != nil || len(hashBytes) == 0 {
		return errors.New("ratify-sign: --content-hash tem de ser base64 nao-vazio")
	}
	priv, err := loadApproverKey(*keyFile) // MESMO carregador do approve-sign: a seed nunca é ecoada.
	if err != nil {
		return err
	}

	artifact := hitl.SelfModArtifact{
		ID:      strings.TrimSpace(*artifactID),
		Kind:    artKind,
		Version: strings.TrimSpace(*version),
		EvalResult: otelgenai.EvaluationResult{
			Suite:   strings.TrimSpace(*evalSuite),
			EvalID:  strings.TrimSpace(*evalID),
			Dataset: otelgenai.EvalDataset(strings.TrimSpace(*evalDataset)),
			Verdict: otelgenai.EvalVerdict(strings.TrimSpace(*evalVerdict)),
			Score:   *evalScore,
		},
		CanaryPassed: *canary,
		ContentHash:  hashBytes,
	}
	if raw := strings.TrimSpace(*evalTrace); raw != "" {
		b, derr := hex.DecodeString(raw)
		if derr != nil || len(b) != len(artifact.EvalResult.TargetTraceID) {
			return errors.New("ratify-sign: --eval-trace tem de ser hex de 16 bytes")
		}
		copy(artifact.EvalResult.TargetTraceID[:], b)
	}

	nonce := make([]byte, ratifyNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("ratify-sign: nonce: %w", err)
	}
	// A assinatura cobre o canónico da decisão, cujo RequestID É o RatificationID do artefacto —
	// é isso que a torna INTRANSFERÍVEL para outro artefacto/eval (anti-transplante). O IssuedAt
	// é o instante REAL: o nó impõe uma janela de frescura e recusa (ratification_stale) o que
	// for velho, pelo que a ratificação deve ser submetida logo a seguir.
	signed, err := hitl.SignApproval(context.Background(), ed25519Signer{priv: priv}, hitl.SignedApproval{
		RequestID: artifact.RatificationID(),
		Approver:  strings.TrimSpace(*ratifier),
		Approved:  !*refuse,
		Nonce:     nonce,
		IssuedAt:  time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("ratify-sign: assinar: %w", err)
	}

	body, err := json.Marshal(promoteBodyWire{
		Artifact: artifactBodyWire{
			ID:           artifact.ID,
			Kind:         string(artifact.Kind),
			Version:      artifact.Version,
			ContentHash:  base64.StdEncoding.EncodeToString(artifact.ContentHash),
			CanaryPassed: artifact.CanaryPassed,
			Eval: evalBodyWire{
				Suite:         artifact.EvalResult.Suite,
				EvalID:        artifact.EvalResult.EvalID,
				Dataset:       string(artifact.EvalResult.Dataset),
				Verdict:       string(artifact.EvalResult.Verdict),
				Score:         artifact.EvalResult.Score,
				TargetTraceID: artifact.EvalResult.TargetTraceIDHex(),
			},
		},
		Ratification: ratificationBodyWire{
			RequestID: signed.RequestID,
			Ratifier:  signed.Approver,
			Approved:  signed.Approved,
			Nonce:     base64.StdEncoding.EncodeToString(signed.Nonce),
			IssuedAt:  signed.IssuedAt.Format(time.RFC3339Nano),
			Signature: base64.StdEncoding.EncodeToString(signed.Signature),
		},
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(body))
	return err
}

// parseArtifactKind impõe o VOCABULÁRIO FECHADO da classe: o Kind entra no canónico assinado,
// pelo que um typo produziria em silêncio outra identidade de ratificação (e o nó responderia
// com um "transplante" incompreensível). Fail-closed aqui, ao pé do operador.
func parseArtifactKind(s string) (hitl.ArtifactKind, error) {
	switch strings.TrimSpace(s) {
	case string(hitl.ArtifactSkill):
		return hitl.ArtifactSkill, nil
	case string(hitl.ArtifactProceduralMemory):
		return hitl.ArtifactProceduralMemory, nil
	default:
		return "", fmt.Errorf("ratify-sign: --kind %q invalido (skill|procedural_memory)", s)
	}
}
