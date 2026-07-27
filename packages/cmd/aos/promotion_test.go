package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hitl "github.com/aos-ref/control-plane/governance/hitl"
	audit "github.com/aos-ref/platform/audit"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// AOS-206 (achado DEF-03) — PROVA de que o nó compõe um promotion controller REAL pela via
// sancionada [hitl.NewProductionRatificationGate] e que o anti-replay durável vale PELO
// CAMINHO DO NÓ, não só no gate isolado.

// tnRatifyClock é um relógio determinista partilhado pelo gate de ratificação e pela
// ratificação assinada, para que o IssuedAt caia SEMPRE dentro da janela de frescura (a via
// sancionada força-a; sem um relógio fixo o teste dependeria do relógio real).
func tnRatifyClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC) }
}

// ratifierSigner é o custodiante server-side de teste: guarda a chave PRIVADA do ratificador e
// assina do seu lado, devolvendo SÓ a assinatura (a privada nunca sai — molde de
// messaging.Signer / do fakeVault de hitl). Nenhuma chave é hard-coded: a seed vem de CSPRNG.
type ratifierSigner struct {
	priv map[string]ed25519.PrivateKey
}

func (s ratifierSigner) Sign(_ context.Context, principal string, message []byte) ([]byte, error) {
	priv, ok := s.priv[principal]
	if !ok {
		return nil, errors.New("ratifierSigner: sem material para o principal")
	}
	return ed25519.Sign(priv, message), nil
}

// newRatifier gera um par ed25519 EFÉMERO por CSPRNG e devolve o signer (detém a privada) e a
// pubkey (o único material que sai). Sem chaves em código/fixtures.
func newRatifier(t *testing.T, principal string) (ratifierSigner, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return ratifierSigner{priv: map[string]ed25519.PrivateKey{principal: priv}}, pub
}

// tnPromotableArtifact devolve um artefacto de auto-modificação que passa a pré-condição
// eval-gate+canary do controller (veredicto pass, score >= defaultPromotionMinScore, canary ok).
func tnPromotableArtifact() hitl.SelfModArtifact {
	return hitl.SelfModArtifact{
		ID:      "skill.summarize",
		Kind:    hitl.ArtifactSkill,
		Version: "1.4.0",
		EvalResult: otelgenai.EvaluationResult{
			Suite:         "golden.summarize",
			EvalID:        "eval-abc-123",
			Dataset:       otelgenai.EvalDatasetGolden,
			Verdict:       otelgenai.EvalPass,
			Score:         0.95, // >= defaultPromotionMinScore (0.8)
			TargetTraceID: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		},
		CanaryPassed: true,
		ContentHash:  []byte{0xde, 0xad, 0xbe, 0xef},
	}
}

// signRatification constrói e assina (via o custodiante) uma ratificação ligada à IDENTIDADE do
// artefacto (RequestID = artifact.RatificationID) — reutiliza hitl.SignApproval de AOS-095.
func signRatification(t *testing.T, signer ratifierSigner, principal string, artifact hitl.SelfModArtifact, clock func() time.Time) hitl.SignedApproval {
	t.Helper()
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("rand nonce: %v", err)
	}
	signed, err := hitl.SignApproval(context.Background(), signer, hitl.SignedApproval{
		RequestID: artifact.RatificationID(),
		Approver:  principal,
		Approved:  true,
		Nonce:     nonce,
		IssuedAt:  clock(),
	})
	if err != nil {
		t.Fatalf("SignApproval: %v", err)
	}
	return signed
}

// ratificationReason lê o motivo selado da ÚLTIMA decisão de ratificação na cadeia do
// artefacto no WORM do nó. Prova que a decisão passou pelo caminho de produção (o gate sela
// SEMPRE a decisão terminal), e distingue um bloqueio de replay de outro qualquer.
func ratificationReason(t *testing.T, worm audit.Store, artifactID string) (string, bool) {
	t.Helper()
	recs, err := worm.Read(context.Background(), "ratification:"+artifactID, 1, 1<<62)
	if err != nil {
		t.Fatalf("WORM.Read: %v", err)
	}
	if len(recs) == 0 {
		return "", false
	}
	rec := recs[len(recs)-1]
	for _, ob := range rec.Obligations {
		if ob.Type == "ratification_decision" {
			return ob.Params["reason"], true
		}
	}
	return "", false
}

// TestNodePromotionController_ReplayBlockedThroughNode é a PROVA DE ÁPICE de AOS-206 (CA3): uma
// ratificação promove um artefacto PELO CAMINHO DO NÓ (node.Promotion.Promote) e, re-submetida
// APÓS consumo, é bloqueada com [hitl.ReasonRatificationReplayed].
//
// NÃO-VACUIDADE. A asserção não é vácua porque o replay tem MESMO de atravessar o caminho de
// produção do nó:
//   - a 1ª promoção devolve admit=true — logo a pré-condição, a assinatura, a autoridade
//     "ratify:production" e a frescura foram TODAS satisfeitas pelo caminho REAL (um gate
//     desligado ou mal composto falharia já aqui);
//   - a 2ª promoção usa a MESMA SignedApproval (mesmo nonce) e devolve admit=false com o motivo
//     ratification_replayed SELADO no WORM do nó. Esse motivo só é alcançável quando o
//     nonce-store durável está composto (WithRatifyNonceStore) — a via sancionada FORÇA-o. Se o
//     nó usasse o hitl.NewRatificationGate cru (anti-replay opcional/desligado, que este ticket
//     proíbe), a 2ª promoção devolveria admit=true de novo (a assinatura é reutilizável N vezes)
//     e o motivo seria "ratified", não "ratification_replayed" — o teste falharia. É, portanto,
//     falsificável e discrimina a via de produção da via crua.
func TestNodePromotionController_ReplayBlockedThroughNode(t *testing.T) {
	ctx := context.Background()
	const ratifier = "human:ratifier-alice"
	signer, pub := newRatifier(t, ratifier)

	cfg := tnBaseConfig()
	cfg.Ratifiers = []RatifierConfig{{Principal: ratifier, PubKey: pub}}
	cfg.RatifyClock = tnRatifyClock() // frescura determinista (a via sancionada força a janela)

	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer node.Close()

	if node.Promotion == nil {
		t.Fatal("o no nao compos o promotion controller (AOS-206): node.Promotion e nil")
	}
	if got := node.Promotion.RatifierCount(); got != 1 {
		t.Fatalf("RatifierCount=%d, quero 1", got)
	}

	art := tnPromotableArtifact()
	signed := signRatification(t, signer, ratifier, art, tnRatifyClock())

	// 1ª promoção PELO CAMINHO DO NÓ: nonce fresco ⇒ promove.
	admit, err := node.Promotion.Promote(ctx, art, signed)
	if err != nil {
		t.Fatalf("1a Promote erro: %v", err)
	}
	if !admit {
		reason, _ := ratificationReason(t, node.WORM, art.ID)
		t.Fatalf("1a promocao devia admitir (admit=true); motivo selado=%q", reason)
	}
	if reason, ok := ratificationReason(t, node.WORM, art.ID); !ok || reason != hitl.ReasonRatified {
		t.Fatalf("1a decisao selada: motivo=%q ok=%v, quero %q", reason, ok, hitl.ReasonRatified)
	}

	// 2ª promoção com a MESMA ratificação (mesmo nonce) APÓS consumo: replay ⇒ NEGA.
	admit, err = node.Promotion.Promote(ctx, art, signed)
	if err != nil {
		t.Fatalf("2a Promote erro: %v", err)
	}
	if admit {
		t.Fatal("replay da ratificacao devia bloquear a re-promocao pelo caminho do no (admit=false)")
	}
	reason, ok := ratificationReason(t, node.WORM, art.ID)
	if !ok {
		t.Fatal("decisao do replay nao foi selada no WORM do no")
	}
	if reason != hitl.ReasonRatificationReplayed {
		t.Fatalf("motivo do replay=%q, quero %q (prova de que o nonce-store durable da via sancionada esta composto)", reason, hitl.ReasonRatificationReplayed)
	}
}

// TestNode_UsesSanctionedRatificationPathOnly é a PROVA NEGATIVA de AOS-206 (CA4): o nó usa
// EXCLUSIVAMENTE a via sancionada [hitl.NewProductionRatificationGate] (que FORÇA freshness +
// nonce-store durável) e NUNCA o [hitl.NewRatificationGate] cru (que deixaria o anti-replay
// opcional e por omissão DESLIGADO — o anti-padrão que este ticket fecha).
//
// É um guarda de FONTE, no molde de dep_isolation_test.go: a invariante é estrutural (nenhum
// caminho de composição do nó pode alcançar o construtor cru), pelo que se verifica sobre o
// código do próprio comando, não só sobre um caminho de execução. Interroga a CHAMADA
// (substring com '(') — as menções em godoc a "NewRatificationGate cru" não têm parêntese e
// não contam. `NewProductionRatificationGate(` NÃO contém `NewRatificationGate(` como
// substring (há "Production" entre "New" e "Ratification"), pelo que a busca não colide.
func TestNode_UsesSanctionedRatificationPathOnly(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	sawProduction := false
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue // só o código de produção do comando (os testes referenciam ambos por nome).
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("ReadFile %q: %v", name, err)
		}
		text := string(src)
		if strings.Contains(text, "NewProductionRatificationGate(") {
			sawProduction = true
		}
		if strings.Contains(text, "NewRatificationGate(") {
			t.Errorf("%s chama o construtor CRU hitl.NewRatificationGate( — a via sancionada (freshness+nonce FORCADOS) e NewProductionRatificationGate; o cru deixaria o anti-replay opcional/desligado (AOS-206/DEF-03)", name)
		}
	}
	if !sawProduction {
		t.Fatal("nenhum ficheiro de producao do no chama hitl.NewProductionRatificationGate( — o promotion controller nao esta composto pela via sancionada (AOS-206)")
	}
}

// TestNodePromotionController_AlwaysComposedNoRatifiers prova a decisão de composição
// INCONDICIONAL (CA1) e a honestidade do banner: mesmo SEM ratificadores, o nó compõe o
// controller (não é nil) — mas toda a promoção é NEGADA fail-closed (ratifier_unknown), o
// default seguro. É a contraparte de não-vacuidade: o controller existe sempre, e sem roster
// não promove nada.
func TestNodePromotionController_AlwaysComposedNoRatifiers(t *testing.T) {
	ctx := context.Background()
	cfg := tnBaseConfig()
	cfg.RatifyClock = tnRatifyClock()
	// SEM cfg.Ratifiers.

	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer node.Close()

	if node.Promotion == nil {
		t.Fatal("o controller deve ser composto SEMPRE (AOS-206), mesmo sem ratificadores")
	}
	if got := node.Promotion.RatifierCount(); got != 0 {
		t.Fatalf("RatifierCount=%d, quero 0", got)
	}

	// Uma ratificação de um principal NÃO registado (o roster está vazio) é negada: o gate está
	// composto e fail-closed, não ausente.
	signer, _ := newRatifier(t, "human:stranger")
	art := tnPromotableArtifact()
	signed := signRatification(t, signer, "human:stranger", art, tnRatifyClock())
	admit, err := node.Promotion.Promote(ctx, art, signed)
	if err != nil {
		t.Fatalf("Promote erro: %v", err)
	}
	if admit {
		t.Fatal("sem ratificadores registados a promocao deve ser NEGADA (ratifier_unknown)")
	}
	if reason, ok := ratificationReason(t, node.WORM, art.ID); ok && reason == hitl.ReasonRatified {
		t.Fatalf("motivo=%q: uma promocao sem ratificador registado nunca pode ser 'ratified'", reason)
	}
}
