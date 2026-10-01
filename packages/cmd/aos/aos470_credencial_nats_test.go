package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// AOS-470 — o nó autentica-se no cluster NATS com uma nkey, e em produção não se liga sem ela.

// TestAOS470_ProducaoComNATSSemCredencialRecusa: o estado que o ticket fecha. Antes dele, um
// nó de produção ligava-se ao cluster por CONNECT anónimo, e quem alcançasse a porta de cliente
// escrevia no mesmo log.
func TestAOS470_ProducaoComNATSSemCredencialRecusa(t *testing.T) {
	aos300ProducaoQuaseCompleta(t)
	t.Setenv("AOS_EVENTSTORE_PATH", "")
	t.Setenv("AOS_EVENTSTORE_NATS", "aos-es-0:4222")
	t.Setenv("AOS_EVENTSTORE_NATS_NKEY_FILE", "")

	if _, err := nodeConfigFromEnv(); !errors.Is(err, ErrProductionNeedsNATSCredential) {
		t.Fatalf("produção com AOS_EVENTSTORE_NATS e sem credencial devia recusar com ErrProductionNeedsNATSCredential, veio: %v", err)
	}
}

// TestAOS470_ProducaoComNATSECredencialPassaAGuarda: a guarda não pode exigir mais do que a
// credencial — nem que ela seja lida aqui (é lida no Bootstrap, ao abrir o substrato).
func TestAOS470_ProducaoComNATSECredencialPassaAGuarda(t *testing.T) {
	aos300ProducaoQuaseCompleta(t)
	t.Setenv("AOS_EVENTSTORE_PATH", "")
	t.Setenv("AOS_EVENTSTORE_NATS", "aos-es-0:4222")
	t.Setenv("AOS_EVENTSTORE_NATS_NKEY_FILE", "/run/secrets/aos-nats.nk")

	cfg, err := nodeConfigFromEnv()
	if errors.Is(err, ErrProductionNeedsNATSCredential) {
		t.Fatal("credencial declarada e a guarda recusou")
	}
	if err == nil && cfg.EventStoreNATSNKeyFile != "/run/secrets/aos-nats.nk" {
		t.Fatalf("Config.EventStoreNATSNKeyFile = %q — a variável não chegou à Config", cfg.EventStoreNATSNKeyFile)
	}
}

// TestAOS470_OErroNomeiaAVariavelEOFicheiro: o fail-closed diz o que definir, e que é por ficheiro.
func TestAOS470_OErroNomeiaAVariavelEOFicheiro(t *testing.T) {
	msg := ErrProductionNeedsNATSCredential.Error()
	for _, v := range []string{"AOS_EVENTSTORE_NATS_NKEY_FILE", "FICHEIRO", "nats-nkey gerar"} {
		if !strings.Contains(msg, v) {
			t.Errorf("o erro tem de nomear %q; veio: %s", v, msg)
		}
	}
}

// TestAOS470_CredencialSemNATSRecusa: credencial que nada usa é configuração que mente.
func TestAOS470_CredencialSemNATSRecusa(t *testing.T) {
	t.Setenv("AOS_MODE", "")
	t.Setenv("AOS_EVENTSTORE_NATS", "")
	t.Setenv("AOS_EVENTSTORE_NATS_NKEY_FILE", "/run/secrets/aos-nats.nk")
	if _, err := nodeConfigFromEnv(); !errors.Is(err, ErrEventStoreNATSCredentialWithoutNATS) {
		t.Fatalf("veio %v, queria ErrEventStoreNATSCredentialWithoutNATS", err)
	}
}

// TestAOS470_BootstrapComSeedIlegivelAbortaAntesDeLigar: o ficheiro é lido no Bootstrap e o
// erro aborta o arranque — nunca degrada para uma ligação anónima.
func TestAOS470_BootstrapComSeedIlegivelAbortaAntesDeLigar(t *testing.T) {
	p := filepath.Join(t.TempDir(), "aos-nats.nk")
	if err := os.WriteFile(p, []byte("isto não é uma seed\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	cfg := tnBaseConfig()
	cfg.EventStoreNATS, cfg.EventStoreNATSNKeyFile = "127.0.0.1:1", p
	// 127.0.0.1:1 nunca responde: se a seed fosse ignorada, o erro seria de ligação, não de
	// credencial — é isso que distingue «abortou na credencial» de «abortou por acaso».
	_, err := Bootstrap(t.Context(), cfg, &bytes.Buffer{})
	if !errors.Is(err, natsjs.ErrNKeyInvalida) {
		t.Fatalf("Bootstrap com seed inválida: err = %v, quer natsjs.ErrNKeyInvalida", err)
	}
}

func TestAOS470_NATSNKeyGerarEPublicaSaoCoerentes(t *testing.T) {
	var seed bytes.Buffer
	if err := dispatch([]string{"nats-nkey", "gerar"}, &seed); err != nil {
		t.Fatal(err)
	}
	s := strings.TrimSpace(seed.String())
	if !strings.HasPrefix(s, "SU") {
		t.Fatalf("gerar imprimiu %q, quer uma seed SU…", s)
	}

	// publica pelo stdin (o caminho do contentor uid 65532) …
	var pubStdin bytes.Buffer
	if err := cmdNATSNKey([]string{"publica"}, strings.NewReader(string(rune(0xFEFF))+s+"\r\n"), &pubStdin); err != nil {
		t.Fatalf("publica (stdin, com BOM do PowerShell): %v", err)
	}
	// … e por ficheiro dão a mesma chave, que é a da seed.
	p := filepath.Join(t.TempDir(), "aos-nats.nk")
	if err := os.WriteFile(p, []byte(s+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	var pubFich bytes.Buffer
	if err := cmdNATSNKey([]string{"publica", "--key", p}, strings.NewReader(""), &pubFich); err != nil {
		t.Fatalf("publica --key: %v", err)
	}
	k, err := natsjs.ParseNKeySeed([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	for nome, got := range map[string]string{"stdin": pubStdin.String(), "ficheiro": pubFich.String()} {
		if strings.TrimSpace(got) != k.Publica() {
			t.Errorf("publica (%s) = %q, quer %q", nome, strings.TrimSpace(got), k.Publica())
		}
	}
}

func TestAOS470_NATSNKeyPublicaRecusaAPublica(t *testing.T) {
	_, pub, err := natsjs.GerarNKeyUtilizador(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmdNATSNKey([]string{"publica"}, strings.NewReader(pub), &bytes.Buffer{}); !errors.Is(err, natsjs.ErrNKeyInvalida) {
		t.Fatalf("publica aceitou uma chave pública como seed: %v", err)
	}
}

func TestAOS470_NATSNKeySemSubcomandoOuDesconhecidoFalha(t *testing.T) {
	for _, args := range [][]string{{}, {"apagar"}, {"gerar", "extra"}} {
		if err := cmdNATSNKey(args, strings.NewReader(""), &bytes.Buffer{}); err == nil {
			t.Errorf("nats-nkey %v aceite", args)
		}
	}
}
