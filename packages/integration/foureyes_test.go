package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"

	hitl "github.com/aos-ref/control-plane/governance/hitl"
	risk "github.com/aos-ref/kernel/reference-monitor/risk"
)

// approverKP gera um par de chaves ed25519 para um aprovador e regista a PUBKEY no registo
// (a privada vive só no lado do teste/emissor, nunca no gate). Devolve a privada para o
// teste assinar as pernas. Sem autoridade explícita, regista as capabilities approve:danger
// + approve:gray (o aprovador PODE aprovar as duas classes usadas nos testes); um teste que
// queira provar a falta de autoridade passa uma autoridade que NÃO cobre a classe da acção.
func approverKP(t *testing.T, reg *hitl.MemApproverRegistry, approver string, authority ...string) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if len(authority) == 0 {
		authority = []string{hitl.RequiredAuthority(risk.ClassDanger), hitl.RequiredAuthority(risk.ClassGray)}
	}
	reg.Register(approver, pub, authority...)
	return priv
}

func challenge32(t *testing.T) []byte {
	t.Helper()
	n := make([]byte, 32)
	if _, err := rand.Read(n); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return n
}

// newFourEyesGate monta um gate com registo in-memory e nonce-store durável (o
// EventStoreNonceStore de AOS-159, reutilizado via a interface NonceConsumer).
func newFourEyesGate(t *testing.T) (*FourEyesGate, *hitl.MemApproverRegistry) {
	t.Helper()
	reg := hitl.NewMemApproverRegistry()
	nonces, _ := newNonceStore(t) // helper partilhado com steer_authenticator_test.go
	gate, err := NewFourEyesGate(reg, nonces)
	if err != nil {
		t.Fatalf("NewFourEyesGate: %v", err)
	}
	return gate, reg
}

var dualReq = FourEyesRequest{
	RequestID:           "req-danger-1",
	Preview:             []byte("APAGAR bucket prod://logs (IRREVERSÍVEL)"),
	RiskClass:           risk.ClassDanger,
	DualControlRequired: true,
}

// (a) Duas aprovações de principal/sessão/credencial DISTINTOS, com challenges válidos e
// assinaturas válidas ⇒ AUTORIZADO. É o caminho feliz do dual-control.
func TestFourEyes_TwoDistinctApprovalsAuthorized(t *testing.T) {
	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")

	legA := SignFourEyesLeg(privA, dualReq, "human:alice", "sess-A", "cred-A", challenge32(t), []byte("attest-A-stub"))
	legB := SignFourEyesLeg(privB, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), []byte("attest-B-stub"))

	dec, err := gate.Authorize(context.Background(), dualReq, legA, legB)
	if err != nil {
		t.Fatalf("duas aprovações distintas deviam AUTORIZAR, veio erro: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("dec.Authorized = false, quer true (%s)", dec.Reason)
	}
	if len(dec.Approvers) != 2 || dec.Approvers[0] != "human:alice" || dec.Approvers[1] != "human:bob" {
		t.Fatalf("Approvers = %v, quer [alice bob]", dec.Approvers)
	}
}

// (b) MESMO PRINCIPAL nas duas pernas (auto-aprovação) ⇒ recusado com ErrSamePrincipal.
// NÃO-VACUOSO: sessões E credenciais DISTINTAS e ambas as assinaturas VÁLIDAS, para a
// execução chegar mesmo à regra do principal (e não parar antes noutra).
func TestFourEyes_SamePrincipalRejected(t *testing.T) {
	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")

	// Mesmo principal a assinar duas pernas, mas sessão/credencial/challenge DISTINTOS.
	legA := SignFourEyesLeg(privA, dualReq, "human:alice", "sess-A", "cred-A", challenge32(t), nil)
	legB := SignFourEyesLeg(privA, dualReq, "human:alice", "sess-B", "cred-B", challenge32(t), nil)

	dec, err := gate.Authorize(context.Background(), dualReq, legA, legB)
	if !errors.Is(err, ErrSamePrincipal) {
		t.Fatalf("auto-aprovação devia dar ErrSamePrincipal, veio: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("auto-aprovação NÃO devia autorizar")
	}
}

// (c) MESMA SESSÃO nas duas pernas ⇒ recusado com ErrSameSession. NÃO-VACUOSO: principais E
// credenciais DISTINTOS, assinaturas válidas ⇒ passa o check do principal e chega ao da
// sessão.
func TestFourEyes_SameSessionRejected(t *testing.T) {
	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")

	const sharedSession = "sess-SHARED"
	legA := SignFourEyesLeg(privA, dualReq, "human:alice", sharedSession, "cred-A", challenge32(t), nil)
	legB := SignFourEyesLeg(privB, dualReq, "human:bob", sharedSession, "cred-B", challenge32(t), nil)

	dec, err := gate.Authorize(context.Background(), dualReq, legA, legB)
	if !errors.Is(err, ErrSameSession) {
		t.Fatalf("mesma sessão devia dar ErrSameSession, veio: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("mesma sessão NÃO devia autorizar")
	}
}

// (d) MESMA CREDENCIAL nas duas pernas ⇒ recusado com ErrSameCredential. NÃO-VACUOSO:
// principais E sessões DISTINTOS, assinaturas válidas ⇒ passa principal e sessão e chega ao
// da credencial. (O mesmo humano/dispositivo a assinar duas vezes é o cenário real.)
func TestFourEyes_SameCredentialRejected(t *testing.T) {
	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")

	const sharedCred = "cred-SHARED-device"
	legA := SignFourEyesLeg(privA, dualReq, "human:alice", "sess-A", sharedCred, challenge32(t), nil)
	legB := SignFourEyesLeg(privB, dualReq, "human:bob", "sess-B", sharedCred, challenge32(t), nil)

	dec, err := gate.Authorize(context.Background(), dualReq, legA, legB)
	if !errors.Is(err, ErrSameCredential) {
		t.Fatalf("mesma credencial devia dar ErrSameCredential, veio: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("mesma credencial NÃO devia autorizar")
	}
}

// (e) CHALLENGE de perna REPLAY/reutilizado ⇒ recusado com ErrReplayedChallenge.
// NÃO-VACUOSO: principais/sessões/credenciais DISTINTOS e assinaturas VÁLIDAS (a 2.ª perna
// ASSINA o challenge reutilizado, logo a sua assinatura valida) ⇒ passa toda a distinção
// estrutural e falha SÓ no consumo do challenge.
func TestFourEyes_ReplayedLegChallengeRejected(t *testing.T) {
	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")

	shared := challenge32(t)
	legA := SignFourEyesLeg(privA, dualReq, "human:alice", "sess-A", "cred-A", shared, nil)
	// A 2.ª perna reutiliza o MESMO challenge (perna-replay) — assinado, logo sig válida.
	legB := SignFourEyesLeg(privB, dualReq, "human:bob", "sess-B", "cred-B", shared, nil)

	dec, err := gate.Authorize(context.Background(), dualReq, legA, legB)
	if !errors.Is(err, ErrReplayedChallenge) {
		t.Fatalf("challenge reutilizado devia dar ErrReplayedChallenge, veio: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("challenge reutilizado NÃO devia autorizar")
	}
}

// (e') Replay DURÁVEL entre pedidos: um challenge já consumido num pedido anterior é
// recusado mesmo num pedido novo com o MESMO request_id (o scope é o request_id). Prova que
// o anti-replay passa pelo store durável, não é um check local de igualdade dos dois legs.
func TestFourEyes_ReplayAcrossAuthorizeCalls(t *testing.T) {
	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")
	privC := approverKP(t, reg, "human:carol")

	chalA := challenge32(t)
	legA := SignFourEyesLeg(privA, dualReq, "human:alice", "sess-A", "cred-A", chalA, nil)
	legB := SignFourEyesLeg(privB, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), nil)

	if _, err := gate.Authorize(context.Background(), dualReq, legA, legB); err != nil {
		t.Fatalf("1ª autorização devia passar, veio: %v", err)
	}

	// Nova tentativa no MESMO pedido reutilizando o challenge JÁ CONSUMIDO da alice, com um
	// 2.º aprovador diferente e tudo distinto — recusado pelo store durável.
	legAreplay := legA // mesmo challenge chalA já consumido
	legC := SignFourEyesLeg(privC, dualReq, "human:carol", "sess-C", "cred-C", challenge32(t), nil)
	_, err := gate.Authorize(context.Background(), dualReq, legAreplay, legC)
	if !errors.Is(err, ErrReplayedChallenge) {
		t.Fatalf("challenge consumido num pedido anterior devia dar ErrReplayedChallenge, veio: %v", err)
	}
}

// (f) ASSINATURA INVÁLIDA numa perna ⇒ recusada com ErrBadLegSignature. NÃO-VACUOSO: a
// OUTRA perna é válida e tudo o resto distinto; a perna má é assinada por uma chave que NÃO
// corresponde à pubkey pinada do principal reivindicado.
func TestFourEyes_BadLegSignatureRejected(t *testing.T) {
	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")
	_ = approverKP(t, reg, "human:bob") // bob registado com a SUA pubkey…

	legA := SignFourEyesLeg(privA, dualReq, "human:alice", "sess-A", "cred-A", challenge32(t), nil)
	// …mas a perna do bob é assinada por uma chave IMPOSTORA (não a pinada do bob).
	_, impostor, _ := ed25519.GenerateKey(rand.Reader)
	legB := SignFourEyesLeg(impostor, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), nil)

	dec, err := gate.Authorize(context.Background(), dualReq, legA, legB)
	if !errors.Is(err, ErrBadLegSignature) {
		t.Fatalf("assinatura impostora devia dar ErrBadLegSignature, veio: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("assinatura inválida NÃO devia autorizar")
	}
}

// (f') WYSIWYS: uma perna que assinou um PREVIEW diferente do que vai executar não valida
// (o gate reconstrói o tuplo com o req.Preview autoritativo). Cobre a invariante ADR-016 §2.
func TestFourEyes_WrongPreviewRejected(t *testing.T) {
	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")

	// Alice assina um preview ADULTERADO (o que ela "viu" difere do efeito real do pedido).
	forged := FourEyesRequest{RequestID: dualReq.RequestID, Preview: []byte("efeito benigno falso"), DualControlRequired: true}
	legA := SignFourEyesLeg(privA, forged, "human:alice", "sess-A", "cred-A", challenge32(t), nil)
	legB := SignFourEyesLeg(privB, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), nil)

	_, err := gate.Authorize(context.Background(), dualReq, legA, legB)
	if !errors.Is(err, ErrBadLegSignature) {
		t.Fatalf("preview divergente (WYSIWYS) devia dar ErrBadLegSignature, veio: %v", err)
	}
}

// (f”) DOWNGRADE dual→single (o vector do ADR-016 §4): uma perna legitimamente assinada
// sobre um preview IRREVERSÍVEL (DualControlRequired=true) NÃO pode ser re-submetida por um
// relay/BFF non-signing como single-approval (DualControlRequired=false) do MESMO preview. O
// bit dual_required entra no tuplo assinado, logo a assinatura não valida quando reconstruída
// como single ⇒ ErrBadLegSignature. Fecha o bypass central do ticket (uma pessoa não autoriza
// sozinha um irreversível rebaixando a exigência).
func TestFourEyes_DowngradeDualToSingleRejected(t *testing.T) {
	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")

	// Perna assinada para a acção IRREVERSÍVEL (dual, danger) — exactamente o que o aprovador viu.
	legDual := SignFourEyesLeg(privA, dualReq, "human:alice", "sess-A", "cred-A", challenge32(t), nil)

	// O relay tenta rebaixar: MESMO request_id, MESMO preview, MESMA classe, mas dual=false, e
	// submete a única perna como se bastasse uma aprovação.
	downgraded := FourEyesRequest{RequestID: dualReq.RequestID, Preview: dualReq.Preview, RiskClass: dualReq.RiskClass, DualControlRequired: false}
	dec, err := gate.Authorize(context.Background(), downgraded, legDual)
	if !errors.Is(err, ErrBadLegSignature) {
		t.Fatalf("downgrade dual→single devia dar ErrBadLegSignature (bit dual_required no tuplo), veio: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("downgrade dual→single NÃO devia autorizar um irreversível com 1 aprovação")
	}
}

// (f”') AUTORIDADE approve:<classe> por-perna: dois principais AUTÊNTICOS e estruturalmente
// distintos, com assinaturas E challenges válidos, mas cuja autoridade autoritativa NÃO cobre
// a classe da acção (só approve:gray para uma acção danger) ⇒ ErrInsufficientAuthority.
// NÃO-VACUOSO: tudo o resto passa; falha SÓ na autoridade. Prova que o gate é uma decisão de
// autorização completa (autenticidade não basta) e não autoriza irreversíveis por principais
// sem capability de aprovação.
func TestFourEyes_InsufficientAuthorityRejected(t *testing.T) {
	gate, reg := newFourEyesGate(t)
	// Registados com autoridade que NÃO cobre danger (só gray).
	privA := approverKP(t, reg, "human:alice", hitl.RequiredAuthority(risk.ClassGray))
	privB := approverKP(t, reg, "human:bob", hitl.RequiredAuthority(risk.ClassGray))

	legA := SignFourEyesLeg(privA, dualReq, "human:alice", "sess-A", "cred-A", challenge32(t), nil)
	legB := SignFourEyesLeg(privB, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), nil)

	dec, err := gate.Authorize(context.Background(), dualReq, legA, legB)
	if !errors.Is(err, ErrInsufficientAuthority) {
		t.Fatalf("autoridade insuficiente para danger devia dar ErrInsufficientAuthority, veio: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("principal sem approve:danger NÃO devia autorizar uma acção danger")
	}
}

// (g) Acção REVERSÍVEL (não dual): UMA aprovação autenticada basta ⇒ AUTORIZADO.
func TestFourEyes_ReversibleSingleApprovalSuffices(t *testing.T) {
	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")

	req := FourEyesRequest{RequestID: "req-rev-1", Preview: []byte("re-tentar chamada idempotente"), RiskClass: risk.ClassGray, DualControlRequired: false}
	leg := SignFourEyesLeg(privA, req, "human:alice", "sess-A", "cred-A", challenge32(t), nil)

	dec, err := gate.Authorize(context.Background(), req, leg)
	if err != nil {
		t.Fatalf("reversível com 1 aprovação devia autorizar, veio: %v", err)
	}
	if !dec.Authorized || len(dec.Approvers) != 1 || dec.Approvers[0] != "human:alice" {
		t.Fatalf("dec = %+v, quer autorizado com 1 aprovador", dec)
	}

	// E uma acção reversível com DUAS pernas é rejeitada (contagem errada, fail-closed).
	leg2 := SignFourEyesLeg(privA, req, "human:alice", "sess-B", "cred-B", challenge32(t), nil)
	if _, err := gate.Authorize(context.Background(), req, leg, leg2); !errors.Is(err, ErrWrongLegCount) {
		t.Fatalf("reversível com 2 pernas devia dar ErrWrongLegCount, veio: %v", err)
	}
}

// Attestation de dispositivo é STUB (D4): uma attestation ausente/lixo NÃO impede a
// autorização — prova que o campo é registado mas NÃO verificado. Documenta a condicional.
func TestFourEyes_DeviceAttestationIsStub(t *testing.T) {
	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")

	// Attestations manifestamente inválidas/ausentes.
	legA := SignFourEyesLeg(privA, dualReq, "human:alice", "sess-A", "cred-A", challenge32(t), []byte("AAGUID-INVENTADO-NAO-VERIFICADO"))
	legB := SignFourEyesLeg(privB, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), nil)

	dec, err := gate.Authorize(context.Background(), dualReq, legA, legB)
	if err != nil || !dec.Authorized {
		t.Fatalf("attestation stub não devia bloquear a autorização (D4), veio dec=%+v err=%v", dec, err)
	}
}

// Fail-closed na construção e no pedido: sem registo, sem store, sem request_id, sem preview,
// aprovador desconhecido, campos em falta e challenge curto.
func TestFourEyes_ConstructionAndInputFailClosed(t *testing.T) {
	nonces, _ := newNonceStore(t)
	if _, err := NewFourEyesGate(nil, nonces); !errors.Is(err, ErrNoApproverRegistry) {
		t.Fatalf("registo nil devia dar ErrNoApproverRegistry, veio: %v", err)
	}
	if _, err := NewFourEyesGate(hitl.NewMemApproverRegistry(), nil); !errors.Is(err, ErrNoChallengeStore) {
		t.Fatalf("store nil devia dar ErrNoChallengeStore, veio: %v", err)
	}

	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")
	ctx := context.Background()

	// Sem request_id.
	noID := FourEyesRequest{Preview: []byte("x"), DualControlRequired: true}
	if _, err := gate.Authorize(ctx, noID); !errors.Is(err, ErrMissingRequestID) {
		t.Fatalf("sem request_id devia dar ErrMissingRequestID, veio: %v", err)
	}
	// Sem preview.
	noPrev := FourEyesRequest{RequestID: "r", DualControlRequired: true}
	if _, err := gate.Authorize(ctx, noPrev); !errors.Is(err, ErrMissingPreview) {
		t.Fatalf("sem preview devia dar ErrMissingPreview, veio: %v", err)
	}
	// Contagem errada (1 perna numa acção dual).
	legA := SignFourEyesLeg(privA, dualReq, "human:alice", "sess-A", "cred-A", challenge32(t), nil)
	if _, err := gate.Authorize(ctx, dualReq, legA); !errors.Is(err, ErrWrongLegCount) {
		t.Fatalf("1 perna num dual devia dar ErrWrongLegCount, veio: %v", err)
	}
	// Aprovador desconhecido (não registado).
	_, privX, _ := ed25519.GenerateKey(rand.Reader)
	unknown := SignFourEyesLeg(privX, dualReq, "human:mallory", "sess-M", "cred-M", challenge32(t), nil)
	if _, err := gate.Authorize(ctx, dualReq, unknown, legA); !errors.Is(err, ErrUnknownApprover) {
		t.Fatalf("aprovador desconhecido devia dar ErrUnknownApprover, veio: %v", err)
	}
	// Campo em falta (sessão vazia) — assinamos com sessão vazia para a sig ser coerente.
	badField := SignFourEyesLeg(privB, dualReq, "human:bob", "", "cred-B", challenge32(t), nil)
	if _, err := gate.Authorize(ctx, dualReq, legA, badField); !errors.Is(err, ErrInvalidLeg) {
		t.Fatalf("campo em falta devia dar ErrInvalidLeg, veio: %v", err)
	}
	// Challenge curto (< MinNonceLen).
	short := make([]byte, MinNonceLen-1)
	shortLeg := SignFourEyesLeg(privB, dualReq, "human:bob", "sess-B", "cred-B", short, nil)
	if _, err := gate.Authorize(ctx, dualReq, legA, shortLeg); !errors.Is(err, ErrChallengeTooShort) {
		t.Fatalf("challenge curto devia dar ErrChallengeTooShort, veio: %v", err)
	}
}
