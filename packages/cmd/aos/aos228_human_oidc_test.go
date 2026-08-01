package main

import (
	"context"
	"errors"
	"io"
	"testing"

	integration "github.com/aos-ref/integration"
	oidc "github.com/aos-ref/integration/oidc"
)

// AOS-228 (fecha DEF-110) — o nó compõe o directório de autenticação humana OIDC por
// CONFIGURAÇÃO (`AOS_HUMAN_OIDC_*` ⇒ `integration.NewOIDCDirectory`), em vez de SEMPRE a
// allowlist DEMO-GRADE. Espelha a costura injectável de AOS-220 (o PDP). Prova nos DOIS sentidos,
// SEM mock IdP: a via allowlist-sem-prova do OIDCDirectory recusa (`ErrAssertionRequired`) ANTES
// de qualquer chamada de rede, pelo que basta injectar/compor o directório para o distinguir.

const (
	// Loopback ⇒ http é permitido pelo verificador (transporte não-https só fora de loopback); a
	// porta é fechada de propósito — a construção do OIDCDirectory NÃO se conecta (JWKS é lazy).
	aos228Issuer   = "http://127.0.0.1:9"
	aos228Audience = "aos-human-client"
)

func aos228OIDCDir(t *testing.T) integration.HumanDirectory {
	t.Helper()
	dir, err := integration.NewOIDCDirectory(oidc.Config{
		Issuer:   aos228Issuer,
		Audience: aos228Audience,
		JWKSURI:  aos228Issuer + "/jwks",
	})
	if err != nil {
		t.Fatalf("NewOIDCDirectory: %v", err)
	}
	return dir
}

// (1) A COSTURA de config: `AOS_HUMAN_OIDC_*` ⇒ `nodeConfigFromEnv` compõe o OIDCDirectory (a via
// sem-prova recusa `ErrAssertionRequired`); config incompleta ⇒ `ErrBadHumanOIDC`; ausente ⇒ nil
// (allowlist de referência, retro-compat).
func TestAOS228_ConfigFromEnv_WiresHumanOIDCDirectory(t *testing.T) {
	ctx := context.Background()

	t.Run("com AOS_HUMAN_OIDC_* => OIDCDirectory composto", func(t *testing.T) {
		t.Setenv("AOS_HUMAN_OIDC_ISSUER", aos228Issuer)
		t.Setenv("AOS_HUMAN_OIDC_AUDIENCE", aos228Audience)
		cfg, err := nodeConfigFromEnv()
		if err != nil {
			t.Fatalf("nodeConfigFromEnv: %v", err)
		}
		if cfg.HumanDirectory == nil {
			t.Fatal("cfg.HumanDirectory devia estar COMPOSTO (a costura de AOS-228)")
		}
		// É o OIDCDirectory: a via sem-prova é recusada fail-closed (a allowlist não daria isto).
		if err := cfg.HumanDirectory.Authenticate(ctx, "human:x"); !errors.Is(err, integration.ErrAssertionRequired) {
			t.Fatalf("o directório composto devia recusar a via sem-prova (ErrAssertionRequired), veio: %v", err)
		}
	})

	t.Run("config incompleta (só issuer) => ErrBadHumanOIDC fail-closed", func(t *testing.T) {
		t.Setenv("AOS_HUMAN_OIDC_ISSUER", aos228Issuer)
		t.Setenv("AOS_HUMAN_OIDC_AUDIENCE", "")
		if _, err := nodeConfigFromEnv(); !errors.Is(err, ErrBadHumanOIDC) {
			t.Fatalf("config OIDC incompleta devia dar ErrBadHumanOIDC, veio: %v", err)
		}
	})

	t.Run("sem as variaveis => nil (allowlist de referencia, retro-compat)", func(t *testing.T) {
		t.Setenv("AOS_HUMAN_OIDC_ISSUER", "")
		t.Setenv("AOS_HUMAN_OIDC_AUDIENCE", "")
		cfg, err := nodeConfigFromEnv()
		if err != nil {
			t.Fatalf("nodeConfigFromEnv: %v", err)
		}
		if cfg.HumanDirectory != nil {
			t.Fatal("sem AOS_HUMAN_OIDC_*, cfg.HumanDirectory devia ser nil (allowlist de referência)")
		}
	})
}

// (2) O nó COMPÕE o directório injectado: com um OIDCDirectory, `node.Authority.MintForHuman` (a
// via allowlist sem prova) é RECUSADO `ErrAssertionRequired`; sem injecção (allowlist), FUNCIONA.
// É a prova de que o Bootstrap usa `Config.HumanDirectory` com precedência sobre a allowlist.
func TestAOS228_NodeComposesInjectedHumanDirectory(t *testing.T) {
	ctx := context.Background()

	// (a) COM OIDCDirectory injectado: a via sem prova é recusada.
	cfg := tnBaseConfig()
	cfg.HumanDirectory = aos228OIDCDir(t)
	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (OIDCDirectory injectado): %v", err)
	}
	defer node.Close()
	if _, err := node.Authority.MintForHuman(ctx, tnHuman, tnAgent, tnClass, []string{tnCap}); !errors.Is(err, integration.ErrAssertionRequired) {
		t.Fatalf("com OIDCDirectory, MintForHuman (sem prova) devia dar ErrAssertionRequired, veio: %v", err)
	}

	// (b) SEM injecção (allowlist de referência): MintForHuman FUNCIONA (retro-compat).
	node2, err := Bootstrap(ctx, tnBaseConfig(), io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (allowlist): %v", err)
	}
	defer node2.Close()
	if _, err := node2.Authority.MintForHuman(ctx, tnHuman, tnAgent, tnClass, []string{tnCap}); err != nil {
		t.Fatalf("sem injecção (allowlist), MintForHuman devia funcionar (retro-compat), veio: %v", err)
	}
}
