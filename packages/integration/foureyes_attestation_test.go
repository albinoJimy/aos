package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"

	hitl "github.com/aos-ref/control-plane/governance/hitl"
)

// AOS-177 — o 4-eyes com ATTESTATION DE DISPOSITIVO ligada. A verificação criptográfica de
// WebAuthn (CBOR/COSE/x509) é testada NÃO-VACUOSAMENTE no módulo que a implementa
// (packages/platform/attestation, com CA e attestations sintetizadas de verdade). O que se
// testa AQUI é a outra metade: que o GATE usa a porta, a exige, lhe passa o challenge certo,
// e converte o resultado na decisão — incluindo a negação nova (mesmo dispositivo) que a
// igualdade-de-string não conseguia dar.

// fakeAttestor é um duplo da porta [DeviceAttestationVerifier]. NÃO faz criptografia: mapeia
// o attestationObject para um deviceID determinista e regista o que recebeu, para os testes
// poderem afirmar que o gate lhe passou o CHALLENGE DA PERNA (a ligação attestation↔perna).
type fakeAttestor struct {
	mu sync.Mutex
	// fail força uma recusa (simula CBOR malformado, AAGUID fora da allowlist, etc.).
	fail error
	// emptyID simula um verificador que devolve (vazio, nil) — o gate tem de tratar isso
	// como recusa, não como "dispositivo desconhecido mas aceite".
	emptyID bool
	// deviceFor força o deviceID por attestationObject (para simular dois "aprovadores" a
	// usar o MESMO dispositivo físico).
	deviceFor map[string][]byte
	// calls regista (attestationObject, clientData, challenge) de cada chamada.
	calls []attestorCall
}

type attestorCall struct {
	att       []byte
	clientDat []byte
	challenge []byte
}

func (f *fakeAttestor) VerifyDeviceAttestation(ctx context.Context, attestationObject, clientDataJSON, expectedChallenge []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, attestorCall{
		att:       append([]byte(nil), attestationObject...),
		clientDat: append([]byte(nil), clientDataJSON...),
		challenge: append([]byte(nil), expectedChallenge...),
	})
	if f.fail != nil {
		return nil, f.fail
	}
	if f.emptyID {
		return nil, nil
	}
	if id, ok := f.deviceFor[string(attestationObject)]; ok {
		return id, nil
	}
	sum := sha256.Sum256(attestationObject)
	return sum[:], nil
}

func (f *fakeAttestor) snapshot() []attestorCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]attestorCall(nil), f.calls...)
}

// newAttestedGate monta um gate com a porta de attestation LIGADA.
func newAttestedGate(t *testing.T, at DeviceAttestationVerifier) (*FourEyesGate, *hitl.MemApproverRegistry) {
	t.Helper()
	reg := hitl.NewMemApproverRegistry()
	nonces, _ := newNonceStore(t)
	gate, err := NewFourEyesGate(reg, nonces, WithDeviceAttestation(at))
	if err != nil {
		t.Fatalf("NewFourEyesGate com attestation: %v", err)
	}
	return gate, reg
}

// Duas pernas com attestation VÁLIDA de dispositivos DISTINTOS ⇒ AUTORIZA. E o gate passou
// ao verificador o CHALLENGE DE CADA PERNA — o que impede re-colar uma attestation noutra
// perna (o challenge está dentro do tuplo assinado).
func TestFourEyesAttestation_DistinctDevicesAuthorized(t *testing.T) {
	at := &fakeAttestor{}
	gate, reg := newAttestedGate(t, at)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")

	chA, chB := challenge32(t), challenge32(t)
	legA := SignFourEyesLegAttested(privA, dualReq, "human:alice", "sess-A", "cred-A", chA, []byte("attobj-device-A"), []byte(`{"type":"webauthn.create"}`))
	legB := SignFourEyesLegAttested(privB, dualReq, "human:bob", "sess-B", "cred-B", chB, []byte("attobj-device-B"), []byte(`{"type":"webauthn.create"}`))

	dec, err := gate.Authorize(context.Background(), dualReq, legA, legB)
	if err != nil {
		t.Fatalf("dois dispositivos atestados distintos deviam AUTORIZAR, veio: %v", err)
	}
	if !dec.Authorized || len(dec.Approvers) != 2 {
		t.Fatalf("decisão = %+v, quer autorizada com 2 aprovadores", dec)
	}

	calls := at.snapshot()
	if len(calls) != 2 {
		t.Fatalf("o verificador foi chamado %d vezes, quer 2 (uma por perna)", len(calls))
	}
	if !bytes.Equal(calls[0].challenge, chA) || !bytes.Equal(calls[1].challenge, chB) {
		t.Fatal("o gate tem de passar o CHALLENGE DA PERNA ao verificador (é o que liga a attestation à perna assinada)")
	}
	if !bytes.Equal(calls[0].att, []byte("attobj-device-A")) || !bytes.Equal(calls[1].att, []byte("attobj-device-B")) {
		t.Fatal("o gate tem de passar o attestationObject da perna correspondente")
	}
	if len(calls[0].clientDat) == 0 || len(calls[1].clientDat) == 0 {
		t.Fatal("o gate tem de passar o clientDataJSON da perna")
	}
}

// MESMO DISPOSITIVO nas duas pernas ⇒ RECUSA com ErrSameDevice. É a negação NOVA: os três
// eixos estruturais estão todos DISTINTOS (principal, sessão, credencial) e as assinaturas
// são válidas — só a prova física é que revela que foi o mesmo autenticador a assinar as
// duas vezes. Sem attestation, isto passava.
func TestFourEyesAttestation_SameDeviceRejected(t *testing.T) {
	sameID := sha256.Sum256([]byte("o-mesmo-autenticador-fisico"))
	at := &fakeAttestor{deviceFor: map[string][]byte{
		"attobj-1": sameID[:],
		"attobj-2": sameID[:],
	}}
	gate, reg := newAttestedGate(t, at)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")

	legA := SignFourEyesLegAttested(privA, dualReq, "human:alice", "sess-A", "cred-A", challenge32(t), []byte("attobj-1"), []byte("cd-1"))
	legB := SignFourEyesLegAttested(privB, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), []byte("attobj-2"), []byte("cd-2"))

	dec, err := gate.Authorize(context.Background(), dualReq, legA, legB)
	if !errors.Is(err, ErrSameDevice) {
		t.Fatalf("mesmo dispositivo atestado devia dar ErrSameDevice, veio: %v", err)
	}
	if dec.Authorized {
		t.Fatal("mesmo dispositivo NÃO devia autorizar")
	}

	// NÃO-VACUOSIDADE: as MESMAS pernas, com dispositivos distintos, autorizam.
	at2 := &fakeAttestor{}
	gate2, reg2 := newAttestedGate(t, at2)
	privC := approverKP(t, reg2, "human:alice")
	privD := approverKP(t, reg2, "human:bob")
	legC := SignFourEyesLegAttested(privC, dualReq, "human:alice", "sess-A", "cred-A", challenge32(t), []byte("attobj-1"), []byte("cd-1"))
	legD := SignFourEyesLegAttested(privD, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), []byte("attobj-2"), []byte("cd-2"))
	if _, err := gate2.Authorize(context.Background(), dualReq, legC, legD); err != nil {
		t.Fatalf("com dispositivos distintos devia AUTORIZAR, veio: %v", err)
	}
}

// PERNA SEM ATTESTATION com a porta ligada ⇒ RECUSA (nunca degrada para o modo estrutural).
// Cobre o attestationObject em falta e o clientDataJSON em falta, e o caso reversível.
func TestFourEyesAttestation_MissingAttestationRejected(t *testing.T) {
	at := &fakeAttestor{}
	gate, reg := newAttestedGate(t, at)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")

	// (1) A 2.ª perna não traz attestation nenhuma (é a perna "legacy" do stub).
	legA := SignFourEyesLegAttested(privA, dualReq, "human:alice", "sess-A", "cred-A", challenge32(t), []byte("attobj-A"), []byte("cd-A"))
	legB := SignFourEyesLeg(privB, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), nil)
	if _, err := gate.Authorize(context.Background(), dualReq, legA, legB); !errors.Is(err, ErrMissingDeviceAttestation) {
		t.Fatalf("perna sem attestation devia dar ErrMissingDeviceAttestation, veio: %v", err)
	}

	// (2) Traz o attestationObject mas não o clientDataJSON — meia prova não é prova.
	legC := SignFourEyesLegAttested(privB, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), []byte("attobj-B"), nil)
	if _, err := gate.Authorize(context.Background(), dualReq, legA, legC); !errors.Is(err, ErrMissingDeviceAttestation) {
		t.Fatalf("perna sem clientDataJSON devia dar ErrMissingDeviceAttestation, veio: %v", err)
	}

	// (3) Acção REVERSÍVEL (1 perna) também exige attestation quando a porta está ligada.
	singleReq := FourEyesRequest{RequestID: "req-rev-1", Preview: []byte("acção reversível"), RiskClass: dualReq.RiskClass}
	legNoAtt := SignFourEyesLeg(privA, singleReq, "human:alice", "sess-A", "cred-A", challenge32(t), nil)
	if _, err := gate.Authorize(context.Background(), singleReq, legNoAtt); !errors.Is(err, ErrMissingDeviceAttestation) {
		t.Fatalf("perna única sem attestation devia dar ErrMissingDeviceAttestation, veio: %v", err)
	}
}

// ATTESTATION RECUSADA pelo verificador ⇒ a perna é recusada, com o erro concreto envolvido
// (atribuível no audit). E um verificador que devolva (vazio, nil) também não passa.
func TestFourEyesAttestation_VerifierRejectionIsFailClosed(t *testing.T) {
	errAAGUID := errors.New("attestation: AAGUID fora da allowlist")
	at := &fakeAttestor{fail: errAAGUID}
	gate, reg := newAttestedGate(t, at)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")

	legA := SignFourEyesLegAttested(privA, dualReq, "human:alice", "sess-A", "cred-A", challenge32(t), []byte("attobj-A"), []byte("cd-A"))
	legB := SignFourEyesLegAttested(privB, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), []byte("attobj-B"), []byte("cd-B"))

	dec, err := gate.Authorize(context.Background(), dualReq, legA, legB)
	if !errors.Is(err, ErrDeviceAttestationRejected) {
		t.Fatalf("attestation recusada devia dar ErrDeviceAttestationRejected, veio: %v", err)
	}
	if dec.Authorized {
		t.Fatal("attestation recusada NÃO devia autorizar")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("AAGUID")) {
		t.Fatalf("o erro devia envolver a causa concreta do verificador, veio: %v", err)
	}

	// Verificador "silencioso": sem erro mas sem identificador ⇒ não provou dispositivo.
	silent := &fakeAttestor{emptyID: true}
	gate2, reg2 := newAttestedGate(t, silent)
	privC := approverKP(t, reg2, "human:alice")
	privD := approverKP(t, reg2, "human:bob")
	legC := SignFourEyesLegAttested(privC, dualReq, "human:alice", "sess-A", "cred-A", challenge32(t), []byte("attobj-A"), []byte("cd-A"))
	legD := SignFourEyesLegAttested(privD, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), []byte("attobj-B"), []byte("cd-B"))
	if _, err := gate2.Authorize(context.Background(), dualReq, legC, legD); !errors.Is(err, ErrDeviceAttestationRejected) {
		t.Fatalf("deviceID vazio devia dar ErrDeviceAttestationRejected, veio: %v", err)
	}
}

// A attestation NÃO GASTA CHALLENGES: uma perna com attestation inválida é recusada ANTES do
// consumo durável, pelo que o par de challenges continua utilizável numa submissão correcta.
// (Mesma ordem fail-closed que já protegia as assinaturas.)
func TestFourEyesAttestation_RejectionDoesNotConsumeChallenges(t *testing.T) {
	at := &fakeAttestor{fail: errors.New("attestation: cadeia não confiável")}
	reg := hitl.NewMemApproverRegistry()
	nonces, _ := newNonceStore(t)
	gate, err := NewFourEyesGate(reg, nonces, WithDeviceAttestation(at))
	if err != nil {
		t.Fatalf("NewFourEyesGate: %v", err)
	}
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")

	chA, chB := challenge32(t), challenge32(t)
	legA := SignFourEyesLegAttested(privA, dualReq, "human:alice", "sess-A", "cred-A", chA, []byte("attobj-A"), []byte("cd-A"))
	legB := SignFourEyesLegAttested(privB, dualReq, "human:bob", "sess-B", "cred-B", chB, []byte("attobj-B"), []byte("cd-B"))
	if _, err := gate.Authorize(context.Background(), dualReq, legA, legB); !errors.Is(err, ErrDeviceAttestationRejected) {
		t.Fatalf("quer ErrDeviceAttestationRejected, veio: %v", err)
	}

	// Mesmo store, mesmos challenges, agora com um verificador que aceita ⇒ tem de AUTORIZAR
	// (se a tentativa recusada os tivesse consumido, viria ErrReplayedChallenge).
	ok, err := NewFourEyesGate(reg, nonces, WithDeviceAttestation(&fakeAttestor{}))
	if err != nil {
		t.Fatalf("NewFourEyesGate: %v", err)
	}
	if _, err := ok.Authorize(context.Background(), dualReq, legA, legB); err != nil {
		t.Fatalf("os challenges não deviam ter sido gastos pela recusa, veio: %v", err)
	}
}

// RETRO-COMPATIBILIDADE: SEM a porta, o comportamento estrutural fica INTACTO — pernas sem
// qualquer attestation autorizam, e o campo DeviceAttestation continua a ser transportado
// sem influenciar a decisão (o modo em que o binário zero-dep do nó corre).
func TestFourEyesAttestation_WithoutPortStructuralBehaviourUnchanged(t *testing.T) {
	gate, reg := newFourEyesGate(t)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")

	legA := SignFourEyesLeg(privA, dualReq, "human:alice", "sess-A", "cred-A", challenge32(t), nil)
	legB := SignFourEyesLeg(privB, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), nil)
	if dec, err := gate.Authorize(context.Background(), dualReq, legA, legB); err != nil || !dec.Authorized {
		t.Fatalf("sem a porta, duas pernas estruturalmente distintas deviam autorizar: dec=%+v err=%v", dec, err)
	}

	// Attestations IGUAIS (mesmo dispositivo) passam sem a porta — é precisamente o buraco
	// que a porta fecha, e fica registado aqui como diferença observável entre os dois modos.
	gate2, reg2 := newFourEyesGate(t)
	privC := approverKP(t, reg2, "human:alice")
	privD := approverKP(t, reg2, "human:bob")
	same := []byte("mesma-attestation-de-dispositivo")
	legC := SignFourEyesLegAttested(privC, dualReq, "human:alice", "sess-A", "cred-A", challenge32(t), same, []byte("cd"))
	legD := SignFourEyesLegAttested(privD, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), same, []byte("cd"))
	if dec, err := gate2.Authorize(context.Background(), dualReq, legC, legD); err != nil || !dec.Authorized {
		t.Fatalf("sem a porta o modo estrutural mantém-se (retro-compat): dec=%+v err=%v", dec, err)
	}

	// A MESMA submissão com a porta ligada é RECUSADA — a diferença é a porta, nada mais.
	at := &fakeAttestor{}
	gate3, reg3 := newAttestedGate(t, at)
	privE := approverKP(t, reg3, "human:alice")
	privF := approverKP(t, reg3, "human:bob")
	legE := SignFourEyesLegAttested(privE, dualReq, "human:alice", "sess-A", "cred-A", challenge32(t), same, []byte("cd"))
	legF := SignFourEyesLegAttested(privF, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), same, []byte("cd"))
	if _, err := gate3.Authorize(context.Background(), dualReq, legE, legF); !errors.Is(err, ErrSameDevice) {
		t.Fatalf("com a porta, o mesmo dispositivo devia dar ErrSameDevice, veio: %v", err)
	}
}

// Construção fail-closed: WithDeviceAttestation(nil) NÃO produz um gate silenciosamente
// permissivo — recusa-se a construir. Uma opção nil idem.
func TestFourEyesAttestation_NilVerifierRefused(t *testing.T) {
	reg := hitl.NewMemApproverRegistry()
	nonces, _ := newNonceStore(t)

	if _, err := NewFourEyesGate(reg, nonces, WithDeviceAttestation(nil)); !errors.Is(err, ErrNilDeviceAttestationVerifier) {
		t.Fatalf("verificador nil devia dar ErrNilDeviceAttestationVerifier, veio: %v", err)
	}
	if _, err := NewFourEyesGate(reg, nonces, nil); !errors.Is(err, ErrNilFourEyesOption) {
		t.Fatalf("opção nil devia dar ErrNilFourEyesOption, veio: %v", err)
	}
	// Uma interface não-nil com valor nil por baixo NÃO é apanhável aqui; o que se garante é
	// que o caminho explícito (nil literal) é recusado e que a construção sem opções
	// continua a produzir o gate estrutural.
	if _, err := NewFourEyesGate(reg, nonces); err != nil {
		t.Fatalf("construção sem opções devia continuar válida, veio: %v", err)
	}
}
