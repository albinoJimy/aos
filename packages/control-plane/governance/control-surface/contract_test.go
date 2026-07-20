package controlsurface_test

import (
	"bytes"
	"errors"
	"testing"

	controlsurface "github.com/aos-ref/control-plane/governance/control-surface"
)

// TestContract_ValidMessagesPassSchema — AC1/AC5: cada mensagem do contrato (steer,
// interrupt, resume, state) valida contra o schema na versão corrente.
func TestContract_ValidMessagesPassSchema(t *testing.T) {
	cur := controlsurface.CurrentVersion
	valid := []controlsurface.ControlMessage{
		controlsurface.NewInterrupt(testRunID, controlsurface.ChannelDesktop, testEmitter, []byte("sig")),
		controlsurface.NewSteer(testRunID, controlsurface.ChannelChatbot, testEmitter, []byte("sig"), []byte("vira à esquerda")),
		controlsurface.NewResume(testRunID, controlsurface.ChannelAPI, testEmitter, []byte("sig")),
		controlsurface.NewResumeWithCorrection(testRunID, controlsurface.ChannelAPI, testEmitter, []byte("sig"), []byte("correcção"), []byte("corrsig")),
		controlsurface.NewStateQuery(testRunID, controlsurface.ChannelDesktop),
	}
	for _, m := range valid {
		if err := m.Validate(cur); err != nil {
			t.Errorf("Validate(%s) inesperadamente falhou: %v", m.Kind, err)
		}
	}
}

// TestContract_FailClosed — AC1: cada regra fail-closed do schema rejeita a mensagem
// malformada (campo em falta, kind desconhecido).
func TestContract_FailClosed(t *testing.T) {
	cur := controlsurface.CurrentVersion
	cases := []struct {
		name string
		msg  controlsurface.ControlMessage
		want error
	}{
		{
			name: "run_id vazio",
			msg:  controlsurface.ControlMessage{SchemaVersion: cur.String(), Kind: controlsurface.KindInterrupt, EmitterID: testEmitter},
			want: controlsurface.ErrEmptyRunID,
		},
		{
			name: "kind desconhecido",
			msg:  controlsurface.ControlMessage{SchemaVersion: cur.String(), Kind: "control.explode", RunID: testRunID, EmitterID: testEmitter},
			want: controlsurface.ErrUnknownKind,
		},
		{
			name: "interrupt sem emitter",
			msg:  controlsurface.ControlMessage{SchemaVersion: cur.String(), Kind: controlsurface.KindInterrupt, RunID: testRunID},
			want: controlsurface.ErrEmptyEmitter,
		},
		{
			name: "steer sem correcção",
			msg:  controlsurface.ControlMessage{SchemaVersion: cur.String(), Kind: controlsurface.KindSteer, RunID: testRunID, EmitterID: testEmitter},
			want: controlsurface.ErrEmptyCorrection,
		},
		{
			name: "schema_version não parseável",
			msg:  controlsurface.ControlMessage{SchemaVersion: "1.0", Kind: controlsurface.KindState, RunID: testRunID},
			want: controlsurface.ErrInvalidSchemaVersion,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.msg.Validate(cur)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Validate=%v, quero %v", err, tc.want)
			}
		})
	}
}

// TestContract_IncompatibleMajorRejected — AC5: uma mensagem com MAJOR diferente da
// superfície corrente é REJEITADA (quebra de contrato, fail-closed); um MINOR/PATCH
// diferente dentro do mesmo MAJOR é aceite (retrocompatível).
func TestContract_IncompatibleMajorRejected(t *testing.T) {
	cur := controlsurface.ControlSchemaVersion{Major: 1, Minor: 0, Patch: 0}

	incompat := controlsurface.ControlMessage{SchemaVersion: "2.0.0", Kind: controlsurface.KindState, RunID: testRunID}
	if err := incompat.Validate(cur); !errors.Is(err, controlsurface.ErrIncompatibleSchemaVersion) {
		t.Fatalf("MAJOR incompatível: Validate=%v, quero ErrIncompatibleSchemaVersion", err)
	}

	compatMinor := controlsurface.ControlMessage{SchemaVersion: "1.4.2", Kind: controlsurface.KindState, RunID: testRunID}
	if err := compatMinor.Validate(cur); err != nil {
		t.Fatalf("MINOR/PATCH compatível inesperadamente rejeitado: %v", err)
	}
}

// TestContract_SemVerParseCompareClassify — AC5: parse fail-closed, ordenação total,
// classificação de mudança (MAJOR/MINOR/PATCH) e o domínio versionado.
func TestContract_SemVerParseCompareClassify(t *testing.T) {
	if controlsurface.SchemaDomain != "aos.control.surface.v1" {
		t.Fatalf("SchemaDomain=%q, quero aos.control.surface.v1", controlsurface.SchemaDomain)
	}

	// Parse fail-closed estrito.
	for _, bad := range []string{"", "1", "1.2", "1.2.3.4", "1.x.0", "-1.0.0", "v1.0.0", "1..3"} {
		if _, err := controlsurface.ParseControlSchemaVersion(bad); !errors.Is(err, controlsurface.ErrInvalidSchemaVersion) {
			t.Errorf("ParseControlSchemaVersion(%q) devia falhar fail-closed, err=%v", bad, err)
		}
	}
	v, err := controlsurface.ParseControlSchemaVersion(" 1.2.3 ")
	if err != nil || v.String() != "1.2.3" {
		t.Fatalf("ParseControlSchemaVersion(1.2.3)=%v,%v", v, err)
	}

	// Ordenação total.
	a := controlsurface.ControlSchemaVersion{Major: 1, Minor: 2, Patch: 3}
	b := controlsurface.ControlSchemaVersion{Major: 1, Minor: 3, Patch: 0}
	if !a.Less(b) || a.Equal(b) || a.Compare(a) != 0 {
		t.Fatalf("ordenação: a<b=%v, a==b=%v", a.Less(b), a.Equal(b))
	}

	// Classificação de mudança.
	cases := []struct {
		from, to controlsurface.ControlSchemaVersion
		want     controlsurface.ChangeKind
	}{
		{a, a, controlsurface.ChangeNone},
		{controlsurface.ControlSchemaVersion{Major: 1}, controlsurface.ControlSchemaVersion{Major: 2}, controlsurface.ChangeMajor},
		{controlsurface.ControlSchemaVersion{Major: 1, Minor: 1}, controlsurface.ControlSchemaVersion{Major: 1, Minor: 2}, controlsurface.ChangeMinor},
		{controlsurface.ControlSchemaVersion{Major: 1, Minor: 1, Patch: 1}, controlsurface.ControlSchemaVersion{Major: 1, Minor: 1, Patch: 2}, controlsurface.ChangePatch},
		// Downgrade de MAJOR é na mesma uma quebra (fail-closed simétrico).
		{controlsurface.ControlSchemaVersion{Major: 2}, controlsurface.ControlSchemaVersion{Major: 1}, controlsurface.ChangeMajor},
	}
	for _, tc := range cases {
		if got := controlsurface.Classify(tc.from, tc.to); got != tc.want {
			t.Errorf("Classify(%s→%s)=%s, quero %s", tc.from, tc.to, got, tc.want)
		}
	}
}

// TestContract_JSONRoundTrip — AC1: o codec JSON é estável (round-trip preserva todos
// os campos, incluindo a assinatura e a correcção binárias).
func TestContract_JSONRoundTrip(t *testing.T) {
	orig := controlsurface.NewResumeWithCorrection(testRunID, controlsurface.ChannelChatbot, testEmitter, []byte{0x01, 0x02, 0x03}, []byte("corrige o rumo"), []byte{0x09, 0x08})
	raw, err := orig.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := controlsurface.DecodeMessage(raw)
	if err != nil {
		t.Fatalf("DecodeMessage: %v", err)
	}
	if got.SchemaVersion != orig.SchemaVersion || got.Kind != orig.Kind || got.RunID != orig.RunID ||
		got.Channel != orig.Channel || got.EmitterID != orig.EmitterID ||
		!bytes.Equal(got.Signature, orig.Signature) || !bytes.Equal(got.Correction, orig.Correction) ||
		!bytes.Equal(got.CorrectionSignature, orig.CorrectionSignature) {
		t.Fatalf("round-trip divergente:\n orig=%+v\n got =%+v", orig, got)
	}
	if err := got.Validate(controlsurface.CurrentVersion); err != nil {
		t.Fatalf("mensagem round-tripped não valida: %v", err)
	}
}
