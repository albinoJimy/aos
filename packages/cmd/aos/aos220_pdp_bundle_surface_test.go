package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pdp "github.com/aos-ref/control-plane/pdp"
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

// AOS-220 — SUPERFÍCIE DE CARREGAMENTO DO BUNDLE PDP (achado #5 / DEF-604).
//
// O defeito verificado: [Config.PDP] nunca era preenchido a partir do ambiente, pelo que o nó
// caía SEMPRE em [pdp.NewUnloaded] ⇒ default-deny de TODA a tool call mediada, sem SUPERFÍCIE
// para carregar política. Estes testes provam, AO NÍVEL DO NÓ e nos DOIS sentidos, que:
//
//   - com AOS_POLICY_BUNDLE_DIR + AOS_POLICY_TRUST_ANCHOR (anchor out-of-band), [nodeConfigFromEnv]
//     carrega o bundle assinado, o nó compõe o PDP CARREGADO, e uma tool call PERMITIDA pela
//     política PASSA a mediação e a tool EXECUTA (hoje — antes do fix — seria negada);
//   - sem as variáveis, [Config.PDP] é nil ⇒ o nó compõe [pdp.NewUnloaded] e a MESMA tool call é
//     NEGADA fail-closed (retro-compat: o binário arranca na mesma, default-deny EXPLÍCITO);
//   - o trust anchor é FORÇADO out-of-band: um anchor DIFERENTE (válido mas não o do bundle)
//     RECUSA o bundle, provando que a âncora vem do ambiente e não do trust_anchor.pub do dir.
//
// FALHA-ANTES (falsificabilidade). Antes de [loadPolicyBundleFromEnv] + a ligação em
// [nodeConfigFromEnv], cfg.PDP era SEMPRE nil: o subteste "carregado permite" negava a tool call
// (a asserção de permit/execução falharia) e TestAOS220_ConfigFromEnv_WiresPDP veria nil com as
// variáveis definidas. O subteste negativo passaria em ambos os mundos — é o par que torna a
// prova não-vacuosa.

// aos220CommittedAnchorHex lê o trust anchor do bundle assinado committado (trust_anchor.pub,
// base64) e devolve-o em HEX — o formato que AOS_POLICY_TRUST_ANCHOR espera. É o anchor CORRECTO
// (o mesmo com que o bundle foi assinado); passá-lo out-of-band verifica o bundle.
func aos220CommittedAnchorHex(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(pdpPoliciesDir, "trust_anchor.pub"))
	if err != nil {
		t.Fatalf("ler trust_anchor.pub committado: %v", err)
	}
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("descodificar trust_anchor.pub (base64): %v", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Fatalf("trust anchor committado tem %d bytes, esperava %d", len(pub), ed25519.PublicKeySize)
	}
	return hex.EncodeToString(pub)
}

// aos220WrongAnchorHex devolve uma pubkey ed25519 VÁLIDA mas DIFERENTE da do bundle — para provar
// que o anchor é forçado do ambiente (out-of-band): assinado por outra chave, o bundle é recusado.
func aos220WrongAnchorHex(t *testing.T) string {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = 0xAB // determinístico e ≠ da chave de assinatura do bundle
	}
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	return hex.EncodeToString(pub)
}

// aos220PermitNode compõe o NÓ pela MESMA via de produção usada pelos outros testes de aceitação
// (Bootstrap sobre a cadeia real identity→revalidation→policy→taint→scope→egress), mas com a
// config a NASCER de [nodeConfigFromEnv] — a costura exacta que AOS-220 preenche. withBundleEnv
// controla se AOS_POLICY_BUNDLE_DIR + AOS_POLICY_TRUST_ANCHOR estão definidos. Devolve o nó e a
// credencial (token NHI) a propagar no goal.
func aos220PermitNode(t *testing.T, withBundleEnv bool) (*Node, string) {
	t.Helper()
	ctx := context.Background()

	if withBundleEnv {
		t.Setenv("AOS_POLICY_BUNDLE_DIR", pdpPoliciesDir)
		t.Setenv("AOS_POLICY_TRUST_ANCHOR", aos220CommittedAnchorHex(t))
	}

	// A COSTURA sob teste: cfg.PDP nasce (ou não) daqui, do ambiente.
	cfg, err := nodeConfigFromEnv()
	if err != nil {
		t.Fatalf("nodeConfigFromEnv: %v", err)
	}
	if withBundleEnv && cfg.PDP == nil {
		t.Fatal("com AOS_POLICY_BUNDLE_DIR+ANCHOR definidos, nodeConfigFromEnv devia ter CARREGADO o PDP (cfg.PDP != nil) — a superficie de AOS-220 falhou")
	}
	if !withBundleEnv && cfg.PDP != nil {
		t.Fatal("sem as variaveis, cfg.PDP devia ser nil (default-deny EXPLICITO via NewUnloaded)")
	}

	// Restante cadeia de PERMIT (não é novo — campos de Config já existentes): supply-chain
	// assinada (revalidação), catálogo com a tool, autoridade user∩classe, classe de identidade.
	signer := durSigner(t)
	entry := counterEntry(t, signer)
	auditStore := audit.NewMemStore()
	trust, err := signing.NewTrustStore(auditStore)
	if err != nil {
		t.Fatalf("trust store: %v", err)
	}
	if err := trust.Add(ctx, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatalf("trust add: %v", err)
	}
	revalidator, err := revalidation.New(trust, auditStore)
	if err != nil {
		t.Fatalf("revalidator: %v", err)
	}

	cfg.Model = &twoTurnToolModel{}
	cfg.Catalog = catalogStub{entries: []domain.Entry{entry}}
	cfg.Revalidator = revalidator
	cfg.IssuerClasses = map[string]identity.ClassPolicy{
		durClass: {TTL: 15 * time.Minute, Scope: []string{durCap}},
	}
	cfg.Policy = integration.StaticPolicy{MaxEgress: domain.EgressInternal}
	cfg.Authority = authz.NewStaticAuthoritySource().
		Set("human:"+aos220Human, durCap).
		Set(durAgent, durCap).
		Set("agent:"+durClass, durCap)

	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (no com cadeia de permit, PDP do ambiente): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })

	tok, err := node.Authority.MintForHuman(ctx, aos220Human, durAgent, durClass, []string{durCap})
	if err != nil {
		t.Fatalf("MintForHuman: %v", err)
	}
	return node, tok.Compact
}

// aos220Human é o humano da allowlist do nó — o default de AOS_HUMANS ("operator"), raiz da
// cadeia de delegação. nodeConfigFromEnv usa-o quando AOS_HUMANS não está definido.
const aos220Human = "operator"

// TestAOS220_NodeMediation_BundleFromEnv_TwoWays é a prova de dois-sentidos AO NÍVEL DO NÓ: a
// MESMA tool call (`counter`/cap:fs.read) é PERMITIDA quando o bundle é carregado do ambiente e
// NEGADA quando não há bundle (NewUnloaded). O par falsifica: antes do fix, o ramo "carregado"
// negaria na mesma (cfg.PDP ficava nil) e a asserção de permit/execução falharia.
func TestAOS220_NodeMediation_BundleFromEnv_TwoWays(t *testing.T) {
	// A tool call (`counter`/cap:fs.read) é EMITIDA pelo modelo (twoTurnToolModel) no 1.º turno —
	// o loop de agente medeia-a pela cadeia real do nó. É a MESMA call nos dois ramos.

	t.Run("bundle CARREGADO do ambiente => a tool call PERMITIDA passa a mediacao e EXECUTA", func(t *testing.T) {
		node, credential := aos220PermitNode(t, true)

		var execs int64
		if err := node.Runtime.Register("counter", func(_ context.Context, in []byte) ([]byte, error) {
			atomic.AddInt64(&execs, 1)
			return []byte("pong"), nil // o output prova que a tool EXECUTOU sob permit
		}); err != nil {
			t.Fatalf("Register(counter): %v", err)
		}

		res, _, err := node.Runtime.Run(context.Background(), agentruntime.Goal{
			RunID:      "run-aos220-permit",
			Principal:  referencemonitor.Principal{NHIID: durAgent},
			Credential: credential,
			Objective:  "AOS-220 mediacao com bundle carregado do ambiente",
			MaxTurns:   4,
		}, nil)
		if err != nil {
			t.Fatalf("Runtime.Run: %v", err)
		}

		permits, denials, _ := node.Runtime.Monitor().Metrics().Snapshot()
		if permits < 1 {
			t.Fatalf("permits=%d — com o bundle CARREGADO do ambiente a tool call legitima devia ter sido PERMITIDA (antes do fix, cfg.PDP era nil => NewUnloaded => deny)", permits)
		}
		if denials != 0 {
			t.Fatalf("denials=%d — a tool call legitima NAO devia ter sido negada por nenhuma barreira", denials)
		}
		if got := atomic.LoadInt64(&execs); got != 1 {
			t.Fatalf("execucoes da tool=%d, quero 1 (a tool devia ter executado sob permit)", got)
		}
		if len(res.ToolResults) != 1 || string(res.ToolResults[0].Value) != "pong" {
			t.Fatalf("ToolResults=%+v, quero exactamente 1 com output %q", res.ToolResults, "pong")
		}
	})

	t.Run("SEM bundle (NewUnloaded) => a MESMA tool call e NEGADA fail-closed e NAO executa", func(t *testing.T) {
		node, credential := aos220PermitNode(t, false)

		var execs int64
		if err := node.Runtime.Register("counter", func(_ context.Context, in []byte) ([]byte, error) {
			atomic.AddInt64(&execs, 1)
			return []byte("pong"), nil
		}); err != nil {
			t.Fatalf("Register(counter): %v", err)
		}

		res, _, err := node.Runtime.Run(context.Background(), agentruntime.Goal{
			RunID:      "run-aos220-deny",
			Principal:  referencemonitor.Principal{NHIID: durAgent},
			Credential: credential,
			Objective:  "AOS-220 default-deny explicito sem bundle",
			MaxTurns:   4,
		}, nil)
		if err != nil {
			t.Fatalf("Runtime.Run: %v", err)
		}

		permits, denials, _ := node.Runtime.Monitor().Metrics().Snapshot()
		if denials < 1 {
			t.Fatalf("denials=%d — sem bundle (NewUnloaded) a tool call devia ter sido NEGADA fail-closed", denials)
		}
		if permits != 0 {
			t.Fatalf("permits=%d — nenhum permit devia ser mintado sem politica carregada", permits)
		}
		if got := atomic.LoadInt64(&execs); got != 0 {
			t.Fatalf("execucoes da tool=%d, quero 0 (a tool NUNCA devia executar sob deny)", got)
		}
		for _, tr := range res.ToolResults {
			if len(tr.Value) != 0 {
				t.Fatalf("uma call negada nao devia produzir output, veio %q", string(tr.Value))
			}
		}
	})
}

// TestAOS220_ConfigFromEnv_WiresPDP isola a costura: [nodeConfigFromEnv] preenche cfg.PDP a
// partir do ambiente (e o PDP carregado DECIDE permit para a capability da política), ou deixa-o
// nil sem as variáveis. Prova adicional de que o anchor CORRECTO out-of-band verifica o bundle.
func TestAOS220_ConfigFromEnv_WiresPDP(t *testing.T) {
	t.Run("com variaveis => cfg.PDP carregado e DECIDE permit", func(t *testing.T) {
		t.Setenv("AOS_POLICY_BUNDLE_DIR", pdpPoliciesDir)
		t.Setenv("AOS_POLICY_TRUST_ANCHOR", aos220CommittedAnchorHex(t))

		cfg, err := nodeConfigFromEnv()
		if err != nil {
			t.Fatalf("nodeConfigFromEnv: %v", err)
		}
		if cfg.PDP == nil {
			t.Fatal("cfg.PDP devia estar CARREGADO (a superficie de AOS-220 preenche-o do ambiente)")
		}
		if v := cfg.PDP.ActiveVersion(); v == "" {
			t.Fatal("o PDP carregado devia reportar uma policy_version em vigor (bundle verificado)")
		}
		dec, err := cfg.PDP.Decide(context.Background(), pdp.Input{
			Principal: pdp.Principal{
				AgentClass: durClass,         // na allowlist assinada de agent-worker
				Authority:  []string{durCap}, // a regra Cedar allow_fs_read exige-a
			},
			Capability: durCap, // cap:fs.read → Action Cedar
		})
		if err != nil {
			t.Fatalf("Decide (cap permitida): %v", err)
		}
		if dec.Effect != pdp.Permit {
			t.Fatalf("Decide(%q) = %v, quero Permit — o bundle carregado devia PERMITIR a capability da allowlist", durCap, dec.Effect)
		}
	})

	t.Run("sem variaveis => cfg.PDP nil (default-deny explicito, retro-compat)", func(t *testing.T) {
		// Garante o ambiente limpo mesmo que o processo de teste tenha herdado a variavel.
		t.Setenv("AOS_POLICY_BUNDLE_DIR", "")
		cfg, err := nodeConfigFromEnv()
		if err != nil {
			t.Fatalf("nodeConfigFromEnv: %v", err)
		}
		if cfg.PDP != nil {
			t.Fatal("sem AOS_POLICY_BUNDLE_DIR, cfg.PDP devia ser nil (o composition-root compoe NewUnloaded)")
		}
	})
}

// TestAOS220_FailClosed_ConfigSurface prova o fail-closed da superfície: dir sem anchor, anchor
// malformado, e — a prova do OUT-OF-BAND — um anchor válido mas ERRADO recusa o bundle assinado
// (o anchor vem do ambiente, não do trust_anchor.pub do próprio dir).
func TestAOS220_FailClosed_ConfigSurface(t *testing.T) {
	t.Run("dir sem anchor => ErrPolicyBundleNeedsTrustAnchor (anchor obrigatorio out-of-band)", func(t *testing.T) {
		t.Setenv("AOS_POLICY_BUNDLE_DIR", pdpPoliciesDir)
		t.Setenv("AOS_POLICY_TRUST_ANCHOR", "")
		if _, err := nodeConfigFromEnv(); !errors.Is(err, ErrPolicyBundleNeedsTrustAnchor) {
			t.Fatalf("esperava ErrPolicyBundleNeedsTrustAnchor, veio: %v", err)
		}
	})

	t.Run("anchor malformado => ErrBadPolicyTrustAnchor", func(t *testing.T) {
		t.Setenv("AOS_POLICY_BUNDLE_DIR", pdpPoliciesDir)
		t.Setenv("AOS_POLICY_TRUST_ANCHOR", "nao-e-hex")
		if _, err := nodeConfigFromEnv(); !errors.Is(err, ErrBadPolicyTrustAnchor) {
			t.Fatalf("esperava ErrBadPolicyTrustAnchor, veio: %v", err)
		}
	})

	t.Run("anchor VALIDO mas ERRADO => ErrPolicyBundleLoad (out-of-band: nao le o anchor do bundle)", func(t *testing.T) {
		t.Setenv("AOS_POLICY_BUNDLE_DIR", pdpPoliciesDir)
		t.Setenv("AOS_POLICY_TRUST_ANCHOR", aos220WrongAnchorHex(t))
		_, err := nodeConfigFromEnv()
		if !errors.Is(err, ErrPolicyBundleLoad) {
			t.Fatalf("esperava ErrPolicyBundleLoad (bundle recusado pelo anchor errado), veio: %v", err)
		}
		// A prova positiva do par: o MESMO dir COM o anchor correcto carrega — logo a recusa
		// acima e do anchor out-of-band, nao de um dir inerentemente invalido.
		t.Setenv("AOS_POLICY_TRUST_ANCHOR", aos220CommittedAnchorHex(t))
		cfg, err := nodeConfigFromEnv()
		if err != nil || cfg.PDP == nil {
			t.Fatalf("com o anchor CORRECTO o MESMO bundle devia carregar (cfg.PDP != nil), veio err=%v pdp=%v", err, cfg.PDP)
		}
	})

	t.Run("run() aborta fail-closed com anchor errado", func(t *testing.T) {
		t.Setenv("AOS_POLICY_BUNDLE_DIR", pdpPoliciesDir)
		t.Setenv("AOS_POLICY_TRUST_ANCHOR", aos220WrongAnchorHex(t))
		if err := run(io.Discard); !errors.Is(err, ErrPolicyBundleLoad) {
			t.Fatalf("run() devia abortar com ErrPolicyBundleLoad, veio: %v", err)
		}
	})
}
