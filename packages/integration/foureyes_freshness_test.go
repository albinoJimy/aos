package integration

import (
	"context"
	"errors"
	"sync"
	"testing"

	hitl "github.com/aos-ref/control-plane/governance/hitl"
)

// REMEDIAÇÃO AOS-177 — os dois buracos que a attestation SOZINHA não fechava:
//
//  1. FRESCURA. O anti-replay era "nunca visto"; o challenge era escolhido pelo CLIENTE e o
//     scope é por-request_id, pelo que a MESMA prova (assinatura + attestation) podia ser
//     reapresentada num pedido NOVO. O teste (1) REPRODUZ esse ataque e mostra-o fechado pela
//     porta [ChallengeIssuance].
//  2. ATRIBUIÇÃO. Qualquer autenticador de um modelo permitido servia qualquer perna de
//     qualquer aprovador. O teste (2) mostra a negação por [DeviceEnrollment].

// fakeIssuance é um duplo do registo de emissão: só conhece os challenges que lhe forem
// EMITIDOS, por (scope, aprovador).
type fakeIssuance struct {
	mu     sync.Mutex
	issued map[string]struct{}
	fail   error
}

func newFakeIssuance() *fakeIssuance {
	return &fakeIssuance{issued: make(map[string]struct{})}
}

func (f *fakeIssuance) issue(scope, approver string, challenge []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issued[scope+"|"+approver+"|"+string(challenge)] = struct{}{}
}

func (f *fakeIssuance) IsChallengeIssued(_ context.Context, scope, approver string, challenge []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return false, f.fail
	}
	_, ok := f.issued[scope+"|"+approver+"|"+string(challenge)]
	return ok, nil
}

// fakeEnrollment é um duplo do registo de enrollment (aprovador → deviceIDs).
type fakeEnrollment struct {
	byApprover map[string]map[string]struct{}
	fail       error
}

func newFakeEnrollment() *fakeEnrollment {
	return &fakeEnrollment{byApprover: make(map[string]map[string]struct{})}
}

func (f *fakeEnrollment) enroll(approver string, deviceID []byte) {
	set, ok := f.byApprover[approver]
	if !ok {
		set = make(map[string]struct{})
		f.byApprover[approver] = set
	}
	set[string(deviceID)] = struct{}{}
}

func (f *fakeEnrollment) IsEnrolled(_ context.Context, approver string, deviceID []byte) (bool, error) {
	if f.fail != nil {
		return false, f.fail
	}
	set, ok := f.byApprover[approver]
	if !ok {
		return false, nil
	}
	_, ok = set[string(deviceID)]
	return ok, nil
}

// (1) O ATAQUE DE REAPRESENTAÇÃO, reproduzido e fechado.
//
// Primeira parte: SEM emissão server-side, a MESMA perna (mesma assinatura, mesmo challenge,
// mesma attestation) autoriza num request_id NOVO — porque o scope do anti-replay é
// por-pedido e o challenge é escolhido pelo cliente. Segunda parte: com [WithChallengeIssuance]
// ligada, o pedido novo NEGA com [ErrChallengeNotIssued], porque o servidor nunca emitiu
// aquele challenge para aquele pedido.
func TestFourEyes_ChallengeIssuance_ClosesCrossRequestReplay(t *testing.T) {
	reqA := FourEyesRequest{RequestID: "req-AUDIT-1", Preview: []byte("APAGAR bucket"), RiskClass: dualReq.RiskClass, DualControlRequired: true}
	reqB := FourEyesRequest{RequestID: "req-AUDIT-2", Preview: []byte("APAGAR bucket"), RiskClass: dualReq.RiskClass, DualControlRequired: true}

	// --- SEM emissão: a reapresentação PASSA (o buraco que a auditoria provou) ---
	{
		gate, reg := newFourEyesGate(t)
		privA := approverKP(t, reg, "human:alice")
		privB := approverKP(t, reg, "human:bob")
		chA, chB := challenge32(t), challenge32(t)

		legA := SignFourEyesLeg(privA, reqA, "human:alice", "sess-A", "cred-A", chA, nil)
		legB := SignFourEyesLeg(privB, reqA, "human:bob", "sess-B", "cred-B", chB, nil)
		if _, err := gate.Authorize(context.Background(), reqA, legA, legB); err != nil {
			t.Fatalf("pedido 1 devia autorizar, veio: %v", err)
		}
		// MESMOS challenges, pedido NOVO: as pernas são re-assinadas para o novo request_id
		// (quem detém a chave fá-lo), mas a PROVA DE POSSE reutilizada é o challenge — e passa.
		legA2 := SignFourEyesLeg(privA, reqB, "human:alice", "sess-A", "cred-A", chA, nil)
		legB2 := SignFourEyesLeg(privB, reqB, "human:bob", "sess-B", "cred-B", chB, nil)
		if _, err := gate.Authorize(context.Background(), reqB, legA2, legB2); err != nil {
			t.Fatalf("SEM emissão, a reapresentação passa (é o buraco documentado); veio: %v", err)
		}
	}

	// --- COM emissão: o mesmo challenge num pedido NOVO é recusado ---
	iss := newFakeIssuance()
	reg := hitl.NewMemApproverRegistry()
	nonces, _ := newNonceStore(t)
	gate, err := NewFourEyesGate(reg, nonces, WithChallengeIssuance(iss))
	if err != nil {
		t.Fatalf("NewFourEyesGate com emissão: %v", err)
	}
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")

	chA, chB := challenge32(t), challenge32(t)
	iss.issue(challengeScope(reqA.RequestID), "human:alice", chA)
	iss.issue(challengeScope(reqA.RequestID), "human:bob", chB)

	legA := SignFourEyesLeg(privA, reqA, "human:alice", "sess-A", "cred-A", chA, nil)
	legB := SignFourEyesLeg(privB, reqA, "human:bob", "sess-B", "cred-B", chB, nil)
	if _, err := gate.Authorize(context.Background(), reqA, legA, legB); err != nil {
		t.Fatalf("com challenges EMITIDOS o pedido devia autorizar, veio: %v", err)
	}

	legA2 := SignFourEyesLeg(privA, reqB, "human:alice", "sess-A", "cred-A", chA, nil)
	legB2 := SignFourEyesLeg(privB, reqB, "human:bob", "sess-B", "cred-B", chB, nil)
	dec, err := gate.Authorize(context.Background(), reqB, legA2, legB2)
	if !errors.Is(err, ErrChallengeNotIssued) {
		t.Fatalf("challenge não emitido para o pedido novo devia dar ErrChallengeNotIssued, veio: %v", err)
	}
	if dec.Authorized {
		t.Fatal("reapresentação NÃO devia autorizar com emissão ligada")
	}
}

// O challenge é emitido A UM APROVADOR: um challenge emitido para alice não serve a perna de
// bob (senão bastaria pedir um challenge para si e usá-lo na perna do outro).
func TestFourEyes_ChallengeIssuance_BoundToApprover(t *testing.T) {
	iss := newFakeIssuance()
	reg := hitl.NewMemApproverRegistry()
	nonces, _ := newNonceStore(t)
	gate, err := NewFourEyesGate(reg, nonces, WithChallengeIssuance(iss))
	if err != nil {
		t.Fatalf("NewFourEyesGate: %v", err)
	}
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")

	chA, chB := challenge32(t), challenge32(t)
	iss.issue(challengeScope(dualReq.RequestID), "human:alice", chA)
	iss.issue(challengeScope(dualReq.RequestID), "human:alice", chB) // emitido a ALICE, usado por BOB

	legA := SignFourEyesLeg(privA, dualReq, "human:alice", "sess-A", "cred-A", chA, nil)
	legB := SignFourEyesLeg(privB, dualReq, "human:bob", "sess-B", "cred-B", chB, nil)
	if _, err := gate.Authorize(context.Background(), dualReq, legA, legB); !errors.Is(err, ErrChallengeNotIssued) {
		t.Fatalf("challenge emitido a outro aprovador devia dar ErrChallengeNotIssued, veio: %v", err)
	}

	// Contraprova: emitido ao aprovador CERTO ⇒ autoriza.
	iss.issue(challengeScope(dualReq.RequestID), "human:bob", chB)
	if _, err := gate.Authorize(context.Background(), dualReq, legA, legB); err != nil {
		t.Fatalf("com o challenge emitido a bob devia autorizar, veio: %v", err)
	}
}

// Backend do registo de emissão em erro ⇒ NEGA (fail-closed), e a recusa NÃO consome
// challenges (a consulta acontece antes do consumo durável).
func TestFourEyes_ChallengeIssuance_BackendFailureDenies(t *testing.T) {
	iss := newFakeIssuance()
	iss.fail = errors.New("registo indisponível")
	reg := hitl.NewMemApproverRegistry()
	nonces, _ := newNonceStore(t)
	gate, err := NewFourEyesGate(reg, nonces, WithChallengeIssuance(iss))
	if err != nil {
		t.Fatalf("NewFourEyesGate: %v", err)
	}
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")
	chA, chB := challenge32(t), challenge32(t)
	legA := SignFourEyesLeg(privA, dualReq, "human:alice", "sess-A", "cred-A", chA, nil)
	legB := SignFourEyesLeg(privB, dualReq, "human:bob", "sess-B", "cred-B", chB, nil)

	if _, err := gate.Authorize(context.Background(), dualReq, legA, legB); !errors.Is(err, ErrChallengeIssuanceBackend) {
		t.Fatalf("backend em erro devia dar ErrChallengeIssuanceBackend, veio: %v", err)
	}

	// Os challenges não foram gastos: com o registo a funcionar, a MESMA submissão autoriza.
	iss.fail = nil
	iss.issue(challengeScope(dualReq.RequestID), "human:alice", chA)
	iss.issue(challengeScope(dualReq.RequestID), "human:bob", chB)
	if _, err := gate.Authorize(context.Background(), dualReq, legA, legB); err != nil {
		t.Fatalf("a recusa por backend não devia gastar challenges, veio: %v", err)
	}
}

// (2) ATRIBUIÇÃO: o dispositivo atestado tem de estar REGISTADO para o aprovador da perna.
// Sem enrollment, um autenticador permitido serve qualquer perna — o que anula o dual-control
// para um insider com dois autenticadores (ou duas credenciais).
func TestFourEyes_DeviceEnrollment_BindsDeviceToApprover(t *testing.T) {
	at := &fakeAttestor{}
	enr := newFakeEnrollment()
	reg := hitl.NewMemApproverRegistry()
	nonces, _ := newNonceStore(t)
	gate, err := NewFourEyesGate(reg, nonces, WithDeviceAttestation(at), WithDeviceEnrollment(enr))
	if err != nil {
		t.Fatalf("NewFourEyesGate: %v", err)
	}
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")

	attA, attB := []byte("attobj-device-A"), []byte("attobj-device-B")
	devA, _ := at.VerifyDeviceAttestation(context.Background(), attA, []byte("cd"), challenge32(t))
	devB, _ := at.VerifyDeviceAttestation(context.Background(), attB, []byte("cd"), challenge32(t))

	// Só alice tem dispositivo registado ⇒ a perna de bob NEGA (default-deny).
	enr.enroll("human:alice", devA)
	legA := SignFourEyesLegAttested(privA, dualReq, "human:alice", "sess-A", "cred-A", challenge32(t), attA, []byte("cd-A"))
	legB := SignFourEyesLegAttested(privB, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), attB, []byte("cd-B"))
	if _, err := gate.Authorize(context.Background(), dualReq, legA, legB); !errors.Is(err, ErrDeviceNotEnrolled) {
		t.Fatalf("dispositivo não registado devia dar ErrDeviceNotEnrolled, veio: %v", err)
	}

	// O DISPOSITIVO DE OUTRO APROVADOR também não serve: bob a apresentar o dispositivo de
	// alice é recusado (é o caso do autenticador emprestado/roubado).
	legBWithA := SignFourEyesLegAttested(privB, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), attA, []byte("cd-B"))
	if _, err := gate.Authorize(context.Background(), dualReq, legA, legBWithA); err == nil {
		t.Fatal("bob a usar o dispositivo de alice devia ser recusado")
	}

	// Contraprova: com AMBOS registados, autoriza.
	enr.enroll("human:bob", devB)
	if dec, err := gate.Authorize(context.Background(), dualReq, legA, legB); err != nil || !dec.Authorized {
		t.Fatalf("com os dois dispositivos registados devia autorizar: dec=%+v err=%v", dec, err)
	}
}

// Backend do enrollment em erro ⇒ NEGA (fail-closed, nunca "assume registado").
func TestFourEyes_DeviceEnrollment_BackendFailureDenies(t *testing.T) {
	at := &fakeAttestor{}
	enr := newFakeEnrollment()
	enr.fail = errors.New("directório indisponível")
	reg := hitl.NewMemApproverRegistry()
	nonces, _ := newNonceStore(t)
	gate, err := NewFourEyesGate(reg, nonces, WithDeviceAttestation(at), WithDeviceEnrollment(enr))
	if err != nil {
		t.Fatalf("NewFourEyesGate: %v", err)
	}
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")
	legA := SignFourEyesLegAttested(privA, dualReq, "human:alice", "sess-A", "cred-A", challenge32(t), []byte("a"), []byte("cd"))
	legB := SignFourEyesLegAttested(privB, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), []byte("b"), []byte("cd"))
	if _, err := gate.Authorize(context.Background(), dualReq, legA, legB); !errors.Is(err, ErrDeviceEnrollmentBackend) {
		t.Fatalf("backend do enrollment em erro devia negar, veio: %v", err)
	}
}

// Construção fail-closed das portas novas: nil recusado, e enrollment SEM attestation recusado
// (seria uma porta inerte a dar impressão de atribuição).
func TestFourEyes_NewPortsFailClosedConstruction(t *testing.T) {
	reg := hitl.NewMemApproverRegistry()
	nonces, _ := newNonceStore(t)

	if _, err := NewFourEyesGate(reg, nonces, WithChallengeIssuance(nil)); !errors.Is(err, ErrNilChallengeIssuance) {
		t.Fatalf("emissão nil devia dar ErrNilChallengeIssuance, veio: %v", err)
	}
	if _, err := NewFourEyesGate(reg, nonces, WithDeviceEnrollment(nil)); !errors.Is(err, ErrNilDeviceEnrollment) {
		t.Fatalf("enrollment nil devia dar ErrNilDeviceEnrollment, veio: %v", err)
	}
	if _, err := NewFourEyesGate(reg, nonces, WithDeviceEnrollment(newFakeEnrollment())); !errors.Is(err, ErrEnrollmentWithoutAttestation) {
		t.Fatalf("enrollment sem attestation devia dar ErrEnrollmentWithoutAttestation, veio: %v", err)
	}
}

// A regra de dispositivos distintos é condicionada à CAPACIDADE (porta ligada), não aos DADOS:
// com a porta ligada, um deviceID vazio NEGA em vez de fazer a regra saltar em silêncio.
func TestFourEyes_EmptyDeviceIDDeniesInsteadOfSkippingRule(t *testing.T) {
	silent := &fakeAttestor{emptyID: true}
	gate, reg := newAttestedGate(t, silent)
	privA := approverKP(t, reg, "human:alice")
	privB := approverKP(t, reg, "human:bob")
	legA := SignFourEyesLegAttested(privA, dualReq, "human:alice", "sess-A", "cred-A", challenge32(t), []byte("a"), []byte("cd"))
	legB := SignFourEyesLegAttested(privB, dualReq, "human:bob", "sess-B", "cred-B", challenge32(t), []byte("b"), []byte("cd"))

	dec, err := gate.Authorize(context.Background(), dualReq, legA, legB)
	if !errors.Is(err, ErrDeviceAttestationRejected) {
		t.Fatalf("deviceID vazio devia NEGAR, veio: %v", err)
	}
	if dec.Authorized {
		t.Fatal("deviceID vazio não pode autorizar")
	}
}

// StaticDeviceEnrollment: default-deny por construção, entradas inválidas descartadas, e
// imunidade a mutação do mapa do chamador depois da construção.
func TestStaticDeviceEnrollment(t *testing.T) {
	devAlice := []byte{0x01, 0x02, 0x03}
	src := map[string][][]byte{
		"human:alice": {devAlice},
		"":            {{0x09}},      // aprovador vazio ⇒ descartado
		"human:bob":   {{}, nil},     // deviceIDs vazios ⇒ descartados
		"human:carol": {{0xaa, 0xbb}},
	}
	e := NewStaticDeviceEnrollment(src)

	// Mutação posterior do mapa de origem não pode alterar o registo.
	src["human:mallory"] = [][]byte{devAlice}

	cases := []struct {
		approver string
		id       []byte
		want     bool
	}{
		{"human:alice", devAlice, true},
		{"human:alice", []byte{0x01, 0x02}, false},
		{"human:bob", []byte{}, false},
		{"human:carol", []byte{0xaa, 0xbb}, true},
		{"human:mallory", devAlice, false},
		{"", devAlice, false},
	}
	for _, c := range cases {
		got, err := e.IsEnrolled(context.Background(), c.approver, c.id)
		if err != nil {
			t.Fatalf("IsEnrolled(%q): %v", c.approver, err)
		}
		if got != c.want {
			t.Fatalf("IsEnrolled(%q, %x) = %v, quer %v", c.approver, c.id, got, c.want)
		}
	}
}
