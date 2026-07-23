package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// TestRunProductionRequiresHardenedIdentity prova o fail-closed do entrypoint: sob
// AOS_MODE=production o nó RECUSA arrancar no modo de referência (autoridade
// co-localizada) — exige o trust-anchor-only via AOS_ISSUER_PUBKEY. Um operador não pode,
// por engano, correr o arranque de referência como fronteira de produção endurecida.
func TestRunProductionRequiresHardenedIdentity(t *testing.T) {
	t.Setenv("AOS_MODE", "production")
	t.Setenv("AOS_ISSUER_PUBKEY", "") // sem trust anchor ⇒ só resta o modo de referência

	if err := run(io.Discard); !errors.Is(err, ErrProductionNeedsHardenedIdentity) {
		t.Fatalf("production sem AOS_ISSUER_PUBKEY devia abortar com ErrProductionNeedsHardenedIdentity, veio: %v", err)
	}
}

// TestRunRejectsMalformedIssuerPubKey prova que um AOS_ISSUER_PUBKEY malformado aborta
// fail-closed — um anchor inválido nunca compõe o verifier.
func TestRunRejectsMalformedIssuerPubKey(t *testing.T) {
	t.Setenv("AOS_MODE", "production")
	t.Setenv("AOS_ISSUER_PUBKEY", "nao-e-hex")

	if err := run(io.Discard); !errors.Is(err, ErrBadIssuerPubKey) {
		t.Fatalf("AOS_ISSUER_PUBKEY malformado devia abortar com ErrBadIssuerPubKey, veio: %v", err)
	}
}

// TestRunProductionWithTrustAnchorSucceeds prova que, dado um trust anchor válido, o modo
// de produção arranca (trust-anchor-only) sem qualquer chave de assinatura no processo.
func TestRunProductionWithTrustAnchorSucceeds(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	var sb strings.Builder
	t.Setenv("AOS_MODE", "production")
	t.Setenv("AOS_ISSUER_ID", "iss:external-authority")
	t.Setenv("AOS_ISSUER_PUBKEY", hex.EncodeToString(pub))

	if err := run(&sb); err != nil {
		t.Fatalf("production com trust anchor valido devia arrancar, veio: %v", err)
	}
	if !strings.Contains(sb.String(), IdentityModeRealHardened) {
		t.Fatalf("o banner devia declarar o modo %q, veio:\n%s", IdentityModeRealHardened, sb.String())
	}
}

// TestParseBoardRegions prova o fail-closed de CONFIG da soberania de leitura (AOS-172, D7):
// vazio ⇒ (nil, nil) legado deliberado; entrada MALFORMADA não-vazia ⇒ erro (aborta, nunca
// degrada em silêncio); segmentos vazios (vírgula final) tolerados.
func TestParseBoardRegions(t *testing.T) {
	t.Parallel()
	okCases := []struct {
		name string
		in   string
		want map[string]string
	}{
		{"vazio", "", nil},
		{"so espacos", "   ", nil},
		{"um par", "board:a=eu", map[string]string{"board:a": "eu"}},
		{"varios pares", "board:a=eu,board:b=us", map[string]string{"board:a": "eu", "board:b": "us"}},
		{"virgula final toleravel", "board:a=eu,", map[string]string{"board:a": "eu"}},
		{"espacos internos", " board:a = eu ", map[string]string{"board:a": "eu"}},
	}
	for _, c := range okCases {
		got, err := parseBoardRegions(c.in)
		if err != nil {
			t.Fatalf("%s: parseBoardRegions(%q) devia ter sucesso, veio erro %v", c.name, c.in, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("%s: mapa %v != esperado %v", c.name, got, c.want)
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Fatalf("%s: got[%q]=%q, esperado %q", c.name, k, got[k], v)
			}
		}
	}
	badCases := []struct {
		name string
		in   string
	}{
		{"sem igual (typo)", "aos-demo"},
		{"par sem igual no meio", "board:a=eu,aos-demo"},
		{"board vazio", "=eu"},
		{"regiao vazia", "board:a="},
		{"so virgulas", ",,"},
	}
	for _, c := range badCases {
		got, err := parseBoardRegions(c.in)
		if !errors.Is(err, ErrBadBoardRegions) {
			t.Fatalf("%s: parseBoardRegions(%q) devia abortar com ErrBadBoardRegions, veio (%v, %v)", c.name, c.in, got, err)
		}
		if got != nil {
			t.Fatalf("%s: uma config invalida NAO devia devolver mapa (fail-closed), veio %v", c.name, got)
		}
	}
}

// TestRunRejectsMalformedBoardRegions prova o fail-closed no ENTRYPOINT: um AOS_BOARD_REGIONS
// malformado (typo) ABORTA o arranque em vez de silenciosamente abrir o read-path (sem authz
// D7 nem selo D6). Simetria com ErrBadIssuerPubKey.
func TestRunRejectsMalformedBoardRegions(t *testing.T) {
	t.Setenv("AOS_BOARD_REGIONS", "aos-demo") // typo: falta o '='

	if err := run(io.Discard); !errors.Is(err, ErrBadBoardRegions) {
		t.Fatalf("AOS_BOARD_REGIONS malformado devia abortar com ErrBadBoardRegions, veio: %v", err)
	}
}

// TestRunProductionRequiresSovereignRead prova que a produção NÃO pode servir o read-path
// legado: AOS_MODE=production com a soberania de leitura DELIBERADAMENTE desligada
// (AOS_BOARD_REGIONS vazio explícito) aborta com ErrProductionNeedsSovereignRead — a par da
// exigência de identidade endurecida.
func TestRunProductionRequiresSovereignRead(t *testing.T) {
	t.Setenv("AOS_MODE", "production")
	t.Setenv("AOS_BOARD_REGIONS", "") // opt-out explícito ⇒ recusado em produção

	if err := run(io.Discard); !errors.Is(err, ErrProductionNeedsSovereignRead) {
		t.Fatalf("production sem soberania de leitura devia abortar com ErrProductionNeedsSovereignRead, veio: %v", err)
	}
}

// TestServeAPIRefusesNonLoopbackWithoutAuth prova que o BIND-GUARDRAIL corre no CAMINHO DE
// PRODUÇÃO (o wiring de AOS-166): serveAPI — a função que o entrypoint invoca quando
// AOS_API_ADDR está definido — RECUSA um bind não-loopback quando o canal de controlo não
// está autenticado, devolvendo [ErrRefuseNonLoopbackBind] SEM abrir socket. Fecha o achado
// nº4 (canal de controlo exposto sem authn) no arranque real, não só em httptest.
func TestServeAPIRefusesNonLoopbackWithoutAuth(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()

	// Nó SEM autenticação do canal de controlo (SteerAuth removido) ⇒ controlAuthenticated
	// falso ⇒ o guardrail tem de recusar um bind não-loopback.
	unauth := *node
	unauth.SteerAuth = nil

	err := serveAPI(context.Background(), io.Discard, &unauth, "0.0.0.0:0")
	if err == nil {
		t.Fatal("serveAPI a addr nao-loopback sem authn devia RECUSAR (bind-guardrail no caminho de producao)")
	}
	if !errors.Is(err, ErrRefuseNonLoopbackBind) {
		t.Fatalf("serveAPI devia devolver ErrRefuseNonLoopbackBind, veio %v", err)
	}
}

// TestServeAPILoopbackStartsAndShutsDown prova o wiring completo: serveAPI levanta a API num
// addr LOOPBACK (permitido sob o guardrail), serve, e encerra GRACIOSAMENTE quando o ctx é
// cancelado (o caminho SIGINT/SIGTERM do entrypoint). Não-vacuoso: sem o wiring serveAPI não
// existiria, e um guardrail mal-colocado recusaria também o loopback.
func TestServeAPILoopbackStartsAndShutsDown(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, true)
	defer func() { _ = node.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveAPI(ctx, io.Discard, node, "127.0.0.1:0") }()

	// Dá tempo ao Serve para abrir o listener, depois pede paragem graciosa.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveAPI loopback devia encerrar graciosamente (nil), veio %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveAPI nao encerrou apos cancelamento do ctx (shutdown gracioso preso?)")
	}
}
