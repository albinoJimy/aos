package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// AOS-470 — o aos-orq autentica-se no cluster como o nó, e em produção não se liga anónimo.

func TestAOS470_ORQProducaoSemCredencialRecusaAntesDeLigar(t *testing.T) {
	t.Setenv("AOS_MODE", "production")
	// 127.0.0.1:1 nunca responde: se a guarda faltasse o erro seria de ligação, não este.
	_, _, err := substrato{nats: "127.0.0.1:1"}.abrirReplicado(true)
	if !errors.Is(err, errProducaoSemCredencialNATS) {
		t.Fatalf("produção com --nats e sem --nats-nkey-file: err = %v, quer errProducaoSemCredencialNATS", err)
	}
}

func TestAOS470_ORQSeedInvalidaRecusaAntesDeLigar(t *testing.T) {
	t.Setenv("AOS_MODE", "")
	p := filepath.Join(t.TempDir(), "orq.nk")
	if err := os.WriteFile(p, []byte("nao-e-seed"), 0o400); err != nil {
		t.Fatal(err)
	}
	_, _, err := substrato{nats: "127.0.0.1:1", nkey: p}.abrirReplicado(true)
	if !errors.Is(err, natsjs.ErrNKeyInvalida) {
		t.Fatalf("seed inválida: err = %v, quer natsjs.ErrNKeyInvalida", err)
	}
}

func TestAOS470_ORQCredencialSemNATSERecusada(t *testing.T) {
	if err := (substrato{wal: "/tmp/x.wal", nkey: "/run/k"}).validar(); err == nil {
		t.Fatal("--nats-nkey-file sem --nats foi aceite")
	}
}
