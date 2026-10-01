package integration

import (
	"os"
	"testing"

	"github.com/aos-ref/substrate/eventstore/jetstream"
	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// CredencialNATSDeTeste liga as suites deste módulo ao cluster AUTORIZADO de
// scripts/ci/nats-cluster.sh (AOS-470): a seed vem de AOS_NATS_NKEY_FILE. Sem a variável não
// acrescenta nada, e a ligação é anónima — que só um cluster com AOS_NATS_AUTH=0 aceita.
//
// Exportada (só em teste) porque o backup_replicado_test.go vive no pacote externo.
func CredencialNATSDeTeste(t testing.TB) jetstream.Option {
	t.Helper()
	p := os.Getenv("AOS_NATS_NKEY_FILE")
	if p == "" {
		return jetstream.ComCredencial(nil) // nil ⇒ ligação anónima
	}
	k, err := natsjs.LerNKeyFicheiro(p)
	if err != nil {
		t.Fatalf("AOS_NATS_NKEY_FILE: %v", err)
	}
	return jetstream.ComCredencial(k)
}
