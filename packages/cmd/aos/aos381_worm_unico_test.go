package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/signing"
)

// AOS-381 — WORM ÚNICO ESTENDIDO À SUPPLY-CHAIN.
//
// Até AOS-381, o trust store e a revalidação por chamada selavam num [audit.NewMemStore]
// volátil que NENHUM leitor lia: o sistema de tipos exigia audit mas não DURABILIDADE, pelo
// que o código parecia auditado e não era (a prova evaporava-se ao restart). Agora ambas as
// vias selam no WORM ÚNICO do nó. Este ficheiro prova, sobre o NÓ COMPOSTO (não a biblioteca):
//
//   - AC3: depois de uma revalidação, as partições registry.truststore e registry.revalidation
//     do WORM do nó têm registos — o primeiro LEITOR não-teste-de-biblioteca dessas selagens;
//   - AC8 (gate): com um WORM DURÁVEL em disco, a partição registry.revalidation SOBREVIVE ao
//     processo (reabre-se o FileStore read-only e lê-se ≥1). Se a selagem regredir para
//     in-memory, o ficheiro não contém nada e o reabrir lê zero — a prova de não-vacuidade.

// aos381NoDuravelComRegistoAssinado compõe o nó pela via de produção (nodeConfigFromEnv +
// Bootstrap sobre a cadeia real), com um WORM DURÁVEL em `wormPath` e o registo assinado de
// tools entregue por DADOS (SignedToolRegistrySpec) — o Bootstrap constrói o revalidador e o
// trust store SELADOS nesse WORM. Devolve o nó e a credencial de um humano autorizado.
func aos381NoDuravelComRegistoAssinado(t *testing.T, wormPath string, model agentruntime.ModelClient) (*Node, string) {
	t.Helper()
	ctx := context.Background()

	t.Setenv("AOS_POLICY_BUNDLE_DIR", pdpPoliciesDir)
	t.Setenv("AOS_POLICY_TRUST_ANCHOR", aos220CommittedAnchorHex(t))

	cfg, err := nodeConfigFromEnv()
	if err != nil {
		t.Fatalf("nodeConfigFromEnv: %v", err)
	}

	signer := durSigner(t)
	entry := counterEntry(t, signer)

	cfg.WORMPath = wormPath // WORM DURÁVEL em disco — a durabilidade que AOS-381 exige.
	cfg.Model = model
	cfg.Catalog = catalogStub{entries: []domain.Entry{entry}}
	// A via opt-in por DADOS: o Bootstrap sela a revalidação + trust store no WORM do nó.
	cfg.SignedToolRegistry = nodeSignedRegistrySpec(signer, integration.StaticPolicy{MaxEgress: domain.EgressInternal}, entry)
	cfg.IssuerClasses = map[string]identity.ClassPolicy{
		durClass: {TTL: 15 * time.Minute, Scope: []string{durCap}},
	}
	cfg.Policy = integration.StaticPolicy{MaxEgress: domain.EgressInternal}
	cfg.Authority = authz.NewStaticAuthoritySource().
		Set("human:"+aos220Human, durCap).
		Set(durAgent, durCap).
		Set("agent:"+durClass, durCap)

	node, err := Bootstrap(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("Bootstrap (nó durável com registo assinado): %v", err)
	}
	tok, err := node.Authority.MintForHuman(ctx, aos220Human, durAgent, durClass, []string{durCap})
	if err != nil {
		_ = node.Close()
		t.Fatalf("MintForHuman: %v", err)
	}
	return node, tok.Compact
}

// regLine devolve a linha "registry (REG/EPIC-05..." do banner de plataforma.
func regLine(t *testing.T, p posturaDosServicosDePlataforma) string {
	t.Helper()
	for _, l := range plataformaPostureBanner(p) {
		if strings.HasPrefix(l, "registry (REG") {
			return l
		}
	}
	t.Fatal("linha do registry ausente no banner de plataforma")
	return ""
}

// TestAOS381_BannerDeclaraDurabilidadeDaSelagem é o AC7: o banner declara se as selagens do
// trust store/revalidação são DURÁVEIS ou VOLÁTEIS, derivado do estado REAL (SelagemRegDuravel).
func TestAOS381_BannerDeclaraDurabilidadeDaSelagem(t *testing.T) {
	duravel := regLine(t, posturaDosServicosDePlataforma{
		CatalogoInjectado: true, RevalidadorInjectado: true, SelagemRegDuravel: true,
	})
	if !strings.Contains(duravel, "DURAVEL") || strings.Contains(duravel, "VOLATIL") {
		t.Fatalf("com WORM durável o banner devia declarar DURAVEL (e nunca VOLATIL): %q", duravel)
	}

	volatil := regLine(t, posturaDosServicosDePlataforma{
		CatalogoInjectado: true, RevalidadorInjectado: true, SelagemRegDuravel: false,
	})
	if !strings.Contains(volatil, "VOLATIL") {
		t.Fatalf("sem WORM durável o banner devia declarar VOLATIL: %q", volatil)
	}
	// Ambos os estados nomeiam a selagem no WORM único do nó (a doutrina AOS-381).
	if !strings.Contains(duravel, "WORM UNICO do no") || !strings.Contains(volatil, "WORM UNICO do no") {
		t.Fatalf("o banner devia declarar a selagem no WORM único do nó em ambos os estados:\n duravel=%q\n volatil=%q", duravel, volatil)
	}
}

// TestAOS381_SelagensDaSupplyChainSaoLidasNoNo é o AC3: sobre o nó composto, após uma
// revalidação por chamada, as partições registry.truststore e registry.revalidation do WORM do
// nó têm ≥1 registo cada. É o primeiro leitor de produção dessas selagens.
func TestAOS381_SelagensDaSupplyChainSaoLidasNoNo(t *testing.T) {
	ctx := context.Background()
	wormPath := filepath.Join(t.TempDir(), "worm.wal")
	node, credential := aos381NoDuravelComRegistoAssinado(t, wormPath, &twoTurnToolModel{})
	t.Cleanup(func() { _ = node.Close() })

	if err := node.Runtime.Register("counter", func(_ context.Context, in []byte) ([]byte, error) {
		return []byte("pong"), nil
	}); err != nil {
		t.Fatalf("Register(counter): %v", err)
	}

	// Conduz UM run pela cadeia real — media exactamente uma tool call, que a revalidação SELA.
	if _, _, err := node.Runtime.Run(ctx, agentruntime.Goal{
		RunID:      "run-aos381-leitor",
		Principal:  referencemonitor.Principal{NHIID: durAgent},
		Credential: credential,
		Objective:  "AOS-381: selagem da revalidacao no WORM do no",
		MaxTurns:   4,
	}, nil); err != nil {
		t.Fatalf("Runtime.Run: %v", err)
	}

	// LEITOR no nó: as duas partições da supply-chain têm de conter registos DURÁVEIS.
	trustRecs, err := node.WORM.Read(ctx, signing.DefaultTrustStorePartition, 1, 1<<20)
	if err != nil {
		t.Fatalf("ler registry.truststore no WORM do nó: %v", err)
	}
	if len(trustRecs) < 1 {
		t.Fatalf("registry.truststore vazia — o Add do publicador devia ter selado no WORM do nó (veio %d)", len(trustRecs))
	}
	revalRecs, err := node.WORM.Read(ctx, revalidation.DefaultPartition, 1, 1<<20)
	if err != nil {
		t.Fatalf("ler registry.revalidation no WORM do nó: %v", err)
	}
	if len(revalRecs) < 1 {
		t.Fatalf("registry.revalidation vazia — a revalidação por chamada devia ter selado no WORM do nó (veio %d)", len(revalRecs))
	}
}

// TestAOS381_RevalidacaoSobreviveAoProcesso é o AC8 (gate): com um WORM DURÁVEL em disco, a
// partição registry.revalidation SOBREVIVE ao encerramento do nó. Reabre-se o MESMO ficheiro
// read-only (novo "processo") e lê-se ≥1. NÃO-VACUIDADE: se a selagem regredir para in-memory,
// o ficheiro nunca recebe estes registos e o reabrir lê zero — o teste avermelha.
func TestAOS381_RevalidacaoSobreviveAoProcesso(t *testing.T) {
	ctx := context.Background()
	wormPath := filepath.Join(t.TempDir(), "worm.wal")

	// --- 1.ª vida do processo: compõe, revalida, encerra (liberta o descritor do FileStore). ---
	func() {
		node, credential := aos381NoDuravelComRegistoAssinado(t, wormPath, &twoTurnToolModel{})
		defer func() { _ = node.Close() }()
		if err := node.Runtime.Register("counter", func(_ context.Context, in []byte) ([]byte, error) {
			return []byte("pong"), nil
		}); err != nil {
			t.Fatalf("Register(counter): %v", err)
		}
		if _, _, err := node.Runtime.Run(ctx, agentruntime.Goal{
			RunID:      "run-aos381-sobrevive",
			Principal:  referencemonitor.Principal{NHIID: durAgent},
			Credential: credential,
			Objective:  "AOS-381: durabilidade da revalidacao",
			MaxTurns:   4,
		}, nil); err != nil {
			t.Fatalf("Runtime.Run: %v", err)
		}
	}()

	// --- 2.ª vida (reabertura read-only): a hash-chain do WORM em disco é relida do zero. ---
	fs, err := audit.OpenFileStoreReadOnly(wormPath)
	if err != nil {
		t.Fatalf("reabrir o WORM durável read-only: %v", err)
	}
	revalRecs, err := fs.Read(ctx, revalidation.DefaultPartition, 1, 1<<20)
	if err != nil {
		t.Fatalf("ler registry.revalidation do WORM reaberto: %v", err)
	}
	if len(revalRecs) < 1 {
		t.Fatalf("registry.revalidation NÃO sobreviveu ao processo (veio %d) — a revalidação não selou num WORM durável", len(revalRecs))
	}
}
