package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	integration "github.com/aos-ref/integration"
	risk "github.com/aos-ref/kernel/reference-monitor/risk"
)

// AOS-266 (achado F10) — ATRIBUIÇÃO dispositivo↔aprovador: prova de NÓ de que, com a porta
// integration.WithDeviceEnrollment composta pelo Bootstrap, uma perna cujo dispositivo atestado
// NÃO está registado para o aprovador é RECUSADA — e que o dispositivo REGISTADO é aceite. O
// componente de attestation é substituído por um FALSO (httptest), que devolve um device_id
// determinista do attestationObject: é o que torna a atribuição testável ponta-a-ponta sem o
// verificador CBOR real (que vive fora do nó).

// fakeAttestationServer devolve device_id = base64(sha256(attestation_object)). Determinista:
// attestations diferentes ⇒ dispositivos diferentes, o que permite enrolar um e testar outro.
func fakeAttestationServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			AttestationObject string `json:"attestation_object"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		att, err := base64.StdEncoding.DecodeString(req.AttestationObject)
		if err != nil || len(att) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_id": base64.StdEncoding.EncodeToString(fakeDeviceID(att)),
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeDeviceID espelha o cálculo do servidor falso (o mesmo que o teste usa para enrolar).
func fakeDeviceID(attestationObject []byte) []byte {
	sum := sha256.Sum256(attestationObject)
	return sum[:]
}

// attestedApproveBody constrói o corpo de /approve para uma perna reversível assinada COM
// attestation (attestationObject + clientDataJSON em base64).
func attestedApproveBody(req integration.FourEyesRequest, leg integration.ApprovalLeg) map[string]any {
	return map[string]any{
		"request": map[string]any{
			"request_id":            req.RequestID,
			"preview":               base64.StdEncoding.EncodeToString(req.Preview),
			"risk_class":            uint8(req.RiskClass),
			"dual_control_required": req.DualControlRequired,
		},
		"legs": []map[string]any{{
			"approver":           leg.Approver,
			"session":            leg.Session,
			"credential":         leg.Credential,
			"challenge":          base64.StdEncoding.EncodeToString(leg.Challenge),
			"device_attestation": base64.StdEncoding.EncodeToString(leg.DeviceAttestation),
			"device_client_data": base64.StdEncoding.EncodeToString(leg.DeviceClientData),
			"signature":          base64.StdEncoding.EncodeToString(leg.Signature),
		}},
	}
}

// TestAOS266EnrollmentAcceptsRegistedRejectsForeign prova AS DUAS pernas da atribuição:
//   - dispositivo REGISTADO para o aprovador ⇒ autorizado (200);
//   - dispositivo (attestation) ESTRANHO, não registado ⇒ recusado (403, ErrDeviceNotEnrolled).
func TestAOS266EnrollmentAcceptsRegistedRejectsForeign(t *testing.T) {
	srv := fakeAttestationServer(t)

	approverID := "human:approver-1"
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// attestationObject REGISTADO e o seu device_id.
	enrolledAtt := []byte("attestation-object-do-dispositivo-registado")
	enrolledDevice := fakeDeviceID(enrolledAtt)

	cfg := tnBaseConfig()
	cfg.Model = &countingModel{}
	cfg.Approvers = []ApproverConfig{{
		Principal: approverID,
		PubKey:    pub,
		Authority: []string{"approve:" + risk.ClassSafe.String()},
	}}
	cfg.AttestationVerifierURL = srv.URL // loopback http ⇒ aceite
	cfg.DeviceEnrollment = map[string][][]byte{approverID: {enrolledDevice}}

	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()
	if node.FourEyes == nil {
		t.Fatal("o gate four-eyes devia estar composto")
	}
	_, h := newAPI(t, node)

	newLeg := func(requestID string, att []byte) (integration.FourEyesRequest, integration.ApprovalLeg) {
		req := integration.FourEyesRequest{
			RequestID:           requestID,
			Preview:             []byte("efeito exibido ao humano"),
			RiskClass:           risk.ClassSafe,
			DualControlRequired: false,
		}
		challenge := make([]byte, 32)
		_, _ = rand.Read(challenge)
		clientData := []byte(`{"challenge":"x"}`)
		leg := integration.SignFourEyesLegAttested(priv, req, approverID, "sess-1", "cred-1", challenge, att, clientData)
		return req, leg
	}

	// (i) Dispositivo REGISTADO ⇒ 200.
	reqOK, legOK := newLeg("req-enroll-ok", enrolledAtt)
	rec := postJSON(h, "POST", "/runs/run-x/approve", attestedApproveBody(reqOK, legOK))
	if rec.Code != http.StatusOK {
		t.Fatalf("/approve com dispositivo REGISTADO devia autorizar (200), veio %d (%s)", rec.Code, rec.Body.String())
	}

	// (ii) Dispositivo ESTRANHO (attestation diferente ⇒ device_id diferente, não enrolado) ⇒ 403.
	reqBad, legBad := newLeg("req-enroll-bad", []byte("attestation-object-de-um-dispositivo-ESTRANHO"))
	rec2 := postJSON(h, "POST", "/runs/run-x/approve", attestedApproveBody(reqBad, legBad))
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("/approve com dispositivo NAO REGISTADO devia dar 403 (ErrDeviceNotEnrolled), veio %d (%s)", rec2.Code, rec2.Body.String())
	}
}

// TestAOS266EnrollmentWithoutAttestationAborts prova a guarda fail-loud: dispositivos configurados
// SEM AOS_ATTESTATION_VERIFIER_URL abortam o boot (não há deviceID a confrontar) em vez de compor
// um gate que descarta a atribuição em silêncio.
func TestAOS266EnrollmentWithoutAttestationAborts(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	cfg := tnBaseConfig()
	cfg.Model = &countingModel{}
	cfg.Approvers = []ApproverConfig{{
		Principal: "human:approver-1",
		PubKey:    pub,
		Authority: []string{"approve:" + risk.ClassSafe.String()},
	}}
	cfg.DeviceEnrollment = map[string][][]byte{"human:approver-1": {[]byte("device")}}
	// SEM AttestationVerifierURL.
	_, err = Bootstrap(context.Background(), cfg, io.Discard)
	if !errors.Is(err, ErrEnrollmentWithoutAttestationURL) {
		t.Fatalf("Bootstrap devia abortar com ErrEnrollmentWithoutAttestationURL, veio %v", err)
	}
}

// TestAOS266ParseDeviceEnrollmentFile cobre o loader por ficheiro (molde AOS-193, fail-loud).
func TestAOS266ParseDeviceEnrollmentFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		return p
	}
	dev := base64.StdEncoding.EncodeToString([]byte("device-id-opaco"))

	// (i) Ficheiro com dispositivos ⇒ mapa preenchido.
	valid := write("valid.json", `{"approvers":[{"principal":"human:alice","pubkey":"","authority":[],"devices":["`+dev+`"]}]}`)
	m, err := parseDeviceEnrollmentFile(valid, true)
	if err != nil {
		t.Fatalf("parseDeviceEnrollmentFile(valido): %v", err)
	}
	if len(m["human:alice"]) != 1 {
		t.Fatalf("esperado 1 dispositivo para alice, veio %d", len(m["human:alice"]))
	}

	// (ii) Path vazio ⇒ (nil, nil).
	if m, err := parseDeviceEnrollmentFile("", false); err != nil || m != nil {
		t.Fatalf("path vazio devia dar (nil,nil), veio (%v,%v)", m, err)
	}

	// (iii) Ficheiro EXPLICITO sem dispositivos ⇒ erro fail-loud.
	empty := write("empty.json", `{"approvers":[{"principal":"human:alice","pubkey":"","authority":[]}]}`)
	if _, err := parseDeviceEnrollmentFile(empty, true); !errors.Is(err, ErrBadDeviceEnrollmentFile) {
		t.Fatalf("ficheiro explicito sem dispositivos devia dar ErrBadDeviceEnrollmentFile, veio %v", err)
	}
	// (iii-b) O MESMO ficheiro herdado por omissão (explicit=false) ⇒ dormente (nil,nil).
	if m, err := parseDeviceEnrollmentFile(empty, false); err != nil || m != nil {
		t.Fatalf("ficheiro herdado sem dispositivos devia dar (nil,nil), veio (%v,%v)", m, err)
	}

	// (iv) device_id não-base64 ⇒ erro.
	badDev := write("baddev.json", `{"approvers":[{"principal":"human:alice","pubkey":"","authority":[],"devices":["nao!!base64"]}]}`)
	if _, err := parseDeviceEnrollmentFile(badDev, true); !errors.Is(err, ErrBadDeviceEnrollmentFile) {
		t.Fatalf("device_id nao-base64 devia dar ErrBadDeviceEnrollmentFile, veio %v", err)
	}
}

// TestAOS266ParseChallengeIssuance cobre a gramática booleana fail-closed.
func TestAOS266ParseChallengeIssuance(t *testing.T) {
	for _, s := range []string{"1", "true", "on", "YES"} {
		if on, err := parseChallengeIssuance(s); err != nil || !on {
			t.Fatalf("%q devia ligar, veio (%v,%v)", s, on, err)
		}
	}
	for _, s := range []string{"", "0", "off", "no"} {
		if on, err := parseChallengeIssuance(s); err != nil || on {
			t.Fatalf("%q devia desligar sem erro, veio (%v,%v)", s, on, err)
		}
	}
	if _, err := parseChallengeIssuance("tru"); !errors.Is(err, ErrBadChallengeIssuance) {
		t.Fatalf("valor lixo devia dar ErrBadChallengeIssuance, veio %v", err)
	}
}
