package jetstream

import (
	"os"
	"testing"

	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// CredencialDeTeste é a opção que liga as suites deste pacote ao cluster AUTORIZADO de
// scripts/ci/nats-cluster.sh (AOS-470): a seed vem de AOS_NATS_NKEY_FILE, a mesma que o
// servidor de teste declara na `authorization`. Sem a variável não acrescenta nada e a ligação
// é anónima — o que só um cluster com AOS_NATS_AUTH=0 aceita.
//
// Exportada (só em teste) porque metade das suites vive no pacote externo jetstream_test.
func CredencialDeTeste(t testing.TB) Option {
	t.Helper()
	p := os.Getenv("AOS_NATS_NKEY_FILE")
	if p == "" {
		return func(*config) {}
	}
	k, err := natsjs.LerNKeyFicheiro(p)
	if err != nil {
		t.Fatalf("AOS_NATS_NKEY_FILE: %v", err)
	}
	return ComCredencial(k)
}
