package signing

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"

	"github.com/aos-ref/platform/audit"
)

// pubFromSeed devolve a chave PÚBLICA determinística do par gerado por keyFromSeed.
func pubFromSeed(b byte) ed25519.PublicKey {
	return keyFromSeed(b).Public().(ed25519.PublicKey)
}

// bytesEqualPub compara duas chaves públicas por conteúdo.
func bytesEqualPub(a, b ed25519.PublicKey) bool { return bytes.Equal(a, b) }

// errAuditDown é o erro simulado de um audit store indisponível.
var errAuditDown = errors.New("audit indisponivel")

// failingAuditStore é um [audit.Store] cujo Append falha SEMPRE — modela a
// auditoria indisponível para provar o comportamento fail-closed (uma acção
// não-auditável é recusada). As leituras devolvem vazio.
type failingAuditStore struct{}

func (failingAuditStore) Append(context.Context, audit.AuditRecord) (audit.AuditRecord, error) {
	return audit.AuditRecord{}, errAuditDown
}
func (failingAuditStore) Read(context.Context, string, uint64, uint64) ([]audit.AuditRecord, error) {
	return nil, nil
}
func (failingAuditStore) Head(context.Context, string) (uint64, error) { return 0, nil }
func (failingAuditStore) At(context.Context, string, uint64) (audit.AuditRecord, bool, error) {
	return audit.AuditRecord{}, false, nil
}
