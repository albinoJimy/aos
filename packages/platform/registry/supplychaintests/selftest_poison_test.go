package supplychaintests

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/signing"
)

// TestSelftestSupplychainBypassReddensGate é um teste-VENENO: só corre com
// AOS_SUPPLYCHAIN_SELFTEST=1. Reproduz o vector RUG-PULL mas com o controlo
// CONTORNADO (a chave do atacante adicionada ao trust store, quebrando a fronteira de
// confiança), pelo que a promoção é ADMITIDA — e depois assere FALSAMENTE que foi
// BLOQUEADA (ErrAdmissionDenied). Como o rug-pull passou, a asserção FALHA de
// propósito, PROVANDO que um vector desbloqueado torna o gate scripts/ci/supplychain.sh
// VERMELHO (fail-closed). O self-test scripts/ci/selftest.sh (secção F) corre-o com a
// env var e EXIGE que falhe. Fora do self-test é ignorado (não polui a suite verde).
func TestSelftestSupplychainBypassReddensGate(t *testing.T) {
	if os.Getenv("AOS_SUPPLYCHAIN_SELFTEST") != "1" {
		t.Skip("teste-veneno do self-test (correr com AOS_SUPPLYCHAIN_SELFTEST=1 via scripts/ci/selftest.sh)")
	}
	ctx := context.Background()
	legit := newSigner(t, keyLegit, seedLegit)
	attacker := newSigner(t, keyAttacker, seedAttack)
	// CONTROLO DESLIGADO: o trust store confia TAMBÉM no atacante (rug-pull passa).
	trust := newTrust(t, legit, attacker)
	verifier, err := signing.NewVerifier(trust, audit.NewMemStore(), signing.WithVerifierClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	reg := newRegistry(t, registry.WithAdmissionVerifier(verifier))

	const id = "tool.http.get"
	v := ver(2, 0, 0)
	c := contractWith("rug-pull-exfiltra", domain.EgressExternal, "vault:http")
	dig := sha256Digester.Digest(domain.KindTool, c)
	if _, err := reg.Publish(ctx, registry.PublishRequest{
		ID: id, Version: v, Kind: domain.KindTool, Contract: c,
		Origin: "x", Publisher: keyAttacker, Signature: attacker.Sign(id, v, dig),
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	_, err = reg.SetStatus(ctx, id, v, domain.StatusActive)

	// Asserção do self-test: assevera (FALSAMENTE) que o rug-pull foi BLOQUEADO. Com o
	// controlo contornado, foi ADMITIDO (err == nil) — e esta asserção FALHA de
	// propósito, tornando o gate VERMELHO como o self-test exige.
	if !errors.Is(err, registry.ErrAdmissionDenied) {
		t.Fatalf("rug-pull com controlo desligado foi detectado como NÃO bloqueado (esperado no self-test): SetStatus err = %v", err)
	}
}
