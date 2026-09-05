package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// AOS-338 — O BANNER DECLARA COMO O NÓ SE AUTENTICA, E DERIVA-O DO ESTADO.
//
// Antes disto o banner dizia «attestation LIGADA» e calava-se sobre autenticação: um nó que
// autentica e um nó que fala anónimo com o componente de autoridade produziam a MESMA linha, e
// são posturas materialmente diferentes. A linha passa a nomear o esquema COMPOSTO — lido do
// verificador construído, via `AuthScheme()`, e não da configuração pedida.
//
// A distinção importa porque o banner é o que um operador lê para saber o que o nó realmente
// tem. Um banner derivado da intenção mentiria no dia em que a intenção não se realizasse.

func aos338Banner(t *testing.T, ajusta func(cfg *Config)) string {
	t.Helper()
	srv := fakeAttestationServer(t)

	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("gerar chave: %v", err)
	}
	const approverID = "nhi:aprovador-aos338"

	cfg := tnBaseConfig()
	cfg.Model = &countingModel{}
	cfg.Approvers = []ApproverConfig{{
		Principal: approverID,
		PubKey:    pub,
		Authority: []string{"approve:" + risk.ClassSafe.String()},
	}}
	cfg.AttestationVerifierURL = srv.URL // loopback http ⇒ aceite
	ajusta(&cfg)

	var banner bytes.Buffer
	node, err := Bootstrap(context.Background(), cfg, &banner)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()
	return banner.String()
}

func TestAOS338_BannerDeclaraOEsquemaComposto(t *testing.T) {
	const utilizador = "svc-attestation"
	const senha = "s3nh4-do-banner-que-nao-pode-vazar"

	casos := []struct {
		nome    string
		ajusta  func(cfg *Config)
		quer    string
		naoQuer []string
	}{
		{
			nome:    "basic",
			ajusta:  func(cfg *Config) { cfg.AttestationVerifierBasic = utilizador + ":" + senha },
			quer:    "Authorization: Basic",
			naoQuer: []string{senha, utilizador},
		},
		{
			nome:    "bearer",
			ajusta:  func(cfg *Config) { cfg.AttestationVerifierToken = "token-opaco-do-banner" },
			quer:    "Authorization: Bearer",
			naoQuer: []string{"token-opaco-do-banner"},
		},
		{
			// Falar sem autenticação é uma composição legítima, mas TEM de ser declarada: é a
			// que um operador mais precisa de ver, porque é a que ele pode não ter escolhido.
			nome:   "sem autenticacao",
			ajusta: func(cfg *Config) {},
			quer:   "SEM AUTENTICACAO",
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			banner := aos338Banner(t, c.ajusta)
			if !strings.Contains(banner, "attestation de dispositivo (AOS-177): LIGADA") {
				t.Fatalf("premissa do teste partida: a attestation devia estar composta:\n%s", banner)
			}
			if !strings.Contains(banner, c.quer) {
				t.Errorf("o banner nao declara o esquema %q:\n%s", c.quer, banner)
			}
			// NENHUM VALOR de credencial no banner de arranque, que e recolhido e retido.
			for _, proibido := range c.naoQuer {
				if strings.Contains(banner, proibido) {
					t.Errorf("o banner imprime a credencial (%q):\n%s", proibido, banner)
				}
			}
		})
	}
}

// TestAOS338_OsEsquemasSaoDistinguiveisNoBanner é o CONTROLO. Sem ele, um banner que imprimisse
// sempre a mesma frase — ou que declarasse sempre «SEM AUTENTICACAO» — passaria os casos acima
// naquilo que eles afirmam individualmente. O que se quer provar é que o banner DISTINGUE.
func TestAOS338_OsEsquemasSaoDistinguiveisNoBanner(t *testing.T) {
	basic := aos338Banner(t, func(cfg *Config) { cfg.AttestationVerifierBasic = "u:p" })
	bearer := aos338Banner(t, func(cfg *Config) { cfg.AttestationVerifierToken = "tok" })
	nenhum := aos338Banner(t, func(cfg *Config) {})

	linha := func(banner string) string {
		for _, l := range strings.Split(banner, "\n") {
			if strings.Contains(l, "attestation de dispositivo (AOS-177): LIGADA") {
				return l
			}
		}
		return ""
	}
	lb, lr, ln := linha(basic), linha(bearer), linha(nenhum)
	if lb == "" || lr == "" || ln == "" {
		t.Fatal("a linha de attestation devia existir nos tres casos")
	}
	if lb == lr || lb == ln || lr == ln {
		t.Errorf("os tres estados produzem linhas indistinguiveis:\n  basic:  %s\n  bearer: %s\n  nenhum: %s", lb, lr, ln)
	}
}
