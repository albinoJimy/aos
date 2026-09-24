package main

// aos427_emissor_mandatado_test.go — o emissor AUTOMÁTICO no nó (AOS-427, ADR-033).
//
// A biblioteca prova que o [identity.Verifier] impõe o mandato. Aqui prova-se o que só o nó pode
// estragar: que as três variáveis chegam ao verificador COMPOSTO, que as colisões que anulariam o
// mandato abortam o arranque, e que o banner diz o que o nó faz.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	identity "github.com/aos-ref/platform/identity"
)

const issAutoDeTeste = "iss:aos-issuer-auto"

func chaveAos427(t *testing.T, b byte) ed25519.PrivateKey {
	t.Helper()
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{b}, ed25519.SeedSize))
}

func hexPub(k ed25519.PrivateKey) string { return hex.EncodeToString(k.Public().(ed25519.PublicKey)) }

// noComEmissorMandatado levanta um nó ENDURECIDO com o emissor manual E o automático, e devolve
// o nó, o log do arranque, a chave do emissor automático e a do humano pinado.
func noComEmissorMandatado(t *testing.T) (*Node, string, ed25519.PrivateKey, ed25519.PrivateKey) {
	t.Helper()
	manual, auto, humano := chaveAos427(t, 1), chaveAos427(t, 2), chaveAos427(t, 3)
	var log bytes.Buffer
	node, err := Bootstrap(context.Background(), Config{
		IssuerID:             "iss:aos-issuer",
		IssuerPubKey:         manual.Public().(ed25519.PublicKey),
		IssuerClasses:        tnBaseConfig().IssuerClasses,
		VerifierClock:        tnClock(),
		MandatedIssuerID:     issAutoDeTeste,
		MandatedIssuerPubKey: auto.Public().(ed25519.PublicKey),
		MandateSigners:       map[string]ed25519.PublicKey{"alice": humano.Public().(ed25519.PublicKey)},
	}, &log)
	if err != nil {
		t.Fatalf("Bootstrap com emissor mandatado: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node, log.String(), auto, humano
}

// cunharSobMandato cunha como o timer do servidor cunharia: `mint-mandated`, sem flags de
// identidade, tudo do mandato.
func cunharSobMandato(t *testing.T, auto, humano ed25519.PrivateKey, sm *identity.SignedMandate) string {
	t.Helper()
	agora := tnClock()()
	if sm == nil {
		m, err := identity.SignMandate(humano, identity.Mandate{
			ID: "m-teste", Human: "alice", Board: "board-eu", AgentID: "agent:aos-orq",
			AgentClass: "planner", PolicyRef: "policy://planner", Scope: []string{"run:submit"},
			Issuer: issAutoDeTeste, MaxTTLSeconds: 2700,
			NotBefore: agora.Add(-time.Hour).Unix(), NotAfter: agora.Add(24 * time.Hour).Unix(),
		})
		if err != nil {
			t.Fatal(err)
		}
		sm = &m
	}
	iss, err := identity.NewIssuer(sm.Mandate.Issuer, auto, map[string]identity.ClassPolicy{
		"planner": {TTL: 45 * time.Minute, Scope: []string{"run:submit"}},
	}, identity.WithIssuerClock(tnClock()))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := iss.Issue(context.Background(), identity.IssueRequest{
		UserID: "alice", AgentID: "agent:aos-orq", AgentClass: "planner", PolicyRef: "policy://planner",
		Board: "board-eu", UserAuthority: []string{"run:submit"}, Mandate: sm,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok.Compact
}

// O caminho inteiro: o verificador que o nó COMPÔS aceita o token cunhado sob o mandato, e a
// revogação do mandato, pelo registo durável do nó, mata-o.
func TestAOS427NoAceitaOEmissorAutomaticoDentroDoMandato(t *testing.T) {
	node, _, auto, humano := noComEmissorMandatado(t)
	ctx := context.Background()
	tok := cunharSobMandato(t, auto, humano, nil)
	p, err := node.Verifier.Verify(ctx, tok)
	if err != nil {
		t.Fatalf("o no tinha de aceitar o token cunhado sob o mandato: %v", err)
	}
	if p.MandateID != "m-teste" {
		t.Fatalf("o Principal tem de nomear o mandato, veio %q", p.MandateID)
	}
	if err := node.Revocations.Revoke(ctx, identity.MandateRevocationKey("m-teste")); err != nil {
		t.Fatalf("revogar o mandato: %v", err)
	}
	if _, err := node.Verifier.Verify(ctx, tok); !errors.Is(err, identity.ErrMandateRevoked) {
		t.Fatalf("depois de revogar o mandato o token tinha de dar ErrMandateRevoked, veio %v", err)
	}
	// E a credencial é recusada na PORTA, pelo mesmo verificador (AOS-428).
	if credencialDoRunRecusadaNoNo(ctx, node, tok) == "" {
		t.Fatal("a porta do POST /runs tinha de recusar a credencial sob mandato revogado")
	}
}

// O emissor comprometido: assina o seu próprio mandato com uma chave que não está pinada.
func TestAOS427NoRecusaMandatoDeChaveNaoPinada(t *testing.T) {
	node, _, auto, _ := noComEmissorMandatado(t)
	intruso := chaveAos427(t, 9)
	tok := cunharSobMandato(t, auto, intruso, nil)
	if _, err := node.Verifier.Verify(context.Background(), tok); !errors.Is(err, identity.ErrMandateInvalid) {
		t.Fatalf("mandato de chave nao pinada tinha de dar ErrMandateInvalid no no, veio %v", err)
	}
}

func TestAOS427BannerDeclaraOEmissorMandatado(t *testing.T) {
	_, log, _, _ := noComEmissorMandatado(t)
	if !strings.Contains(log, "emissor MANDATADO (AOS-427, ADR-033): COMPOSTO") || !strings.Contains(log, "1 humano(s) pinado(s)") {
		t.Fatalf("o banner tinha de declarar o emissor mandatado composto e o numero de humanos pinados")
	}
	var semLog bytes.Buffer
	node, err := Bootstrap(context.Background(), tnBaseConfig(), &semLog)
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	if !strings.Contains(semLog.String(), "emissor MANDATADO (AOS-427, ADR-033): NAO COMPOSTO") {
		t.Fatal("sem as variaveis o banner tinha de declarar NAO COMPOSTO")
	}
}

// As colisões que anulariam o mandato abortam o arranque.
func TestAOS427ColisoesQueAnulamOMandatoAbortam(t *testing.T) {
	manual, auto, humano := chaveAos427(t, 1), chaveAos427(t, 2), chaveAos427(t, 3)
	base := func() Config {
		return Config{
			IssuerID: "iss:aos-issuer", IssuerPubKey: manual.Public().(ed25519.PublicKey),
			IssuerClasses: tnBaseConfig().IssuerClasses, VerifierClock: tnClock(),
			MandatedIssuerID: issAutoDeTeste, MandatedIssuerPubKey: auto.Public().(ed25519.PublicKey),
			MandateSigners: map[string]ed25519.PublicKey{"alice": humano.Public().(ed25519.PublicKey)},
		}
	}
	for _, caso := range []struct {
		nome  string
		mudar func(*Config)
	}{
		{"mesmo nome que o manual", func(c *Config) { c.MandatedIssuerID = c.IssuerID }},
		{"mesma chave que o manual — cunharia como o manual, sem mandato", func(c *Config) {
			c.MandatedIssuerPubKey = manual.Public().(ed25519.PublicKey)
		}},
		{"humano com a chave do emissor", func(c *Config) {
			c.MandateSigners = map[string]ed25519.PublicKey{"alice": auto.Public().(ed25519.PublicKey)}
		}},
		{"sem humanos", func(c *Config) { c.MandateSigners = nil }},
		{"pubkey curta", func(c *Config) { c.MandatedIssuerPubKey = c.MandatedIssuerPubKey[:16] }},
		{"sem id", func(c *Config) { c.MandatedIssuerID = "" }},
	} {
		t.Run(caso.nome, func(t *testing.T) {
			cfg := base()
			caso.mudar(&cfg)
			node, err := Bootstrap(context.Background(), cfg, io.Discard)
			if err == nil {
				_ = node.Close()
			}
			if !errors.Is(err, ErrBadMandatedIssuer) {
				t.Fatalf("%s: tinha de abortar com ErrBadMandatedIssuer, veio %v", caso.nome, err)
			}
		})
	}
}

func TestAOS427VariaveisDoEmissorMandatado(t *testing.T) {
	a, h := chaveAos427(t, 2), chaveAos427(t, 3)
	id, pub, hum, err := parseMandatedIssuer("", " ", "")
	if err != nil || id != "" || pub != nil || hum != nil {
		t.Fatalf("todas vazias tem de ser nao configurado, veio %q %v %v %v", id, pub, hum, err)
	}
	if _, _, _, err := parseMandatedIssuer(issAutoDeTeste, hexPub(a), ""); !errors.Is(err, ErrBadMandatedIssuer) ||
		!strings.Contains(err.Error(), "AOS_MANDATE_SIGNERS") {
		t.Fatalf("parcial tem de abortar a nomear a que falta, veio %v", err)
	}
	for _, mau := range []string{
		"alice",                    // sem '='
		"human:alice=" + hexPub(h), // prefixo
		"alice=" + hexPub(h) + ",alice=" + hexPub(a), // nome repetido
		"alice=" + hexPub(h) + ",bob=" + hexPub(h),   // chave repetida
		"alice=zz", // pubkey invalida
		" , ",      // nada
	} {
		if _, _, _, err := parseMandatedIssuer(issAutoDeTeste, hexPub(a), mau); !errors.Is(err, ErrBadMandatedIssuer) {
			t.Errorf("AOS_MANDATE_SIGNERS=%q tinha de abortar, veio %v", mau, err)
		}
	}
	id, _, hum, err = parseMandatedIssuer(issAutoDeTeste, hexPub(a), "alice="+hexPub(h)+", bob="+hexPub(a))
	if err != nil || id != issAutoDeTeste || len(hum) != 2 {
		t.Fatalf("configuracao valida recusada: %v", err)
	}
}

// Achado da revisão adversarial: a colisão de chaves também existe no modo de REFERÊNCIA, onde a
// chave da autoridade co-localizada não está na Config e a validação da Config não a vê.
func TestAOS427ColisaoComAAutoridadeDeReferenciaAborta(t *testing.T) {
	auto, humano := chaveAos427(t, 2), chaveAos427(t, 3)
	cfg := tnBaseConfig()
	cfg.IssuerSigningKey = auto
	cfg.MandatedIssuerID = issAutoDeTeste
	cfg.MandatedIssuerPubKey = auto.Public().(ed25519.PublicKey)
	cfg.MandateSigners = map[string]ed25519.PublicKey{"alice": humano.Public().(ed25519.PublicKey)}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err == nil {
		_ = node.Close()
	}
	if !errors.Is(err, ErrBadMandatedIssuer) {
		t.Fatalf("a chave da autoridade de referencia igual a do emissor automatico tinha de abortar, veio %v", err)
	}
}

// Uma chave pinada inválida numa Config composta em código abortava em silêncio no verificador,
// e o banner contava-a como humano pinado.
func TestAOS427ChavePinadaInvalidaAborta(t *testing.T) {
	manual, auto, humano := chaveAos427(t, 1), chaveAos427(t, 2), chaveAos427(t, 3)
	node, err := Bootstrap(context.Background(), Config{
		IssuerID: "iss:aos-issuer", IssuerPubKey: manual.Public().(ed25519.PublicKey),
		IssuerClasses: tnBaseConfig().IssuerClasses, VerifierClock: tnClock(),
		MandatedIssuerID: issAutoDeTeste, MandatedIssuerPubKey: auto.Public().(ed25519.PublicKey),
		MandateSigners: map[string]ed25519.PublicKey{
			"alice": humano.Public().(ed25519.PublicKey), "bob": ed25519.PublicKey{1, 2, 3},
		},
	}, io.Discard)
	if err == nil {
		_ = node.Close()
	}
	if !errors.Is(err, ErrBadMandatedIssuer) {
		t.Fatalf("uma chave pinada invalida tinha de abortar, veio %v", err)
	}
}
