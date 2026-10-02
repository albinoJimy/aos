package main

// aos439_janela_v1_test.go — A JANELA DE MIGRAÇÃO DOS MANDATOS v1 CHEGA AO VERIFICADOR COMPOSTO
// (AOS-439).
//
// A biblioteca prova a regra; aqui prova-se o que só o nó pode estragar: que AOS_MANDATE_V1_UNTIL
// chega ao verificador, que sem ela um v1 é recusado, e que o banner diz a verdade.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
)

// noComJanela é o [noComEmissorMandatado] com a janela dada.
func noComJanela(t *testing.T, ate time.Time) (*Node, string, ed25519.PrivateKey, ed25519.PrivateKey) {
	t.Helper()
	manual, auto, humano := chaveAos427(t, 1), chaveAos427(t, 2), chaveAos427(t, 3)
	var log bytes.Buffer
	node, err := Bootstrap(context.Background(), Config{
		IssuerID: "iss:aos-issuer", IssuerPubKey: manual.Public().(ed25519.PublicKey),
		IssuerClasses: tnBaseConfig().IssuerClasses, VerifierClock: tnClock(),
		MandatedIssuerID: issAutoDeTeste, MandatedIssuerPubKey: auto.Public().(ed25519.PublicKey),
		MandateSigners: pinosDeSoftwareNo(map[string]ed25519.PublicKey{"alice": humano.Public().(ed25519.PublicKey)}),
		MandateV1Until: ate,
	}, &log)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node, log.String(), auto, humano
}

// mandatoV1DeTeste é o mandato que corre hoje em produção: sem requesters, assinado antes do
// AOS-439 (a SignMandate já o recusa, pelo que se assina à mão).
func mandatoV1DeTeste(humano ed25519.PrivateKey) *identity.SignedMandate {
	agora := tnClock()()
	m := identity.Mandate{
		ID: "m-v1", Human: "alice", Board: "board-eu", AgentID: "agent:aos-orq",
		AgentClass: "planner", PolicyRef: "policy://planner", Scope: []string{"run:submit"},
		Issuer: issAutoDeTeste, MaxTTLSeconds: 2700,
		NotBefore: agora.Add(-time.Hour).Unix(), NotAfter: agora.Add(24 * time.Hour).Unix(),
	}
	sig := ed25519.Sign(humano, m.SigningInput())
	return &identity.SignedMandate{Mandate: m, Signature: base64.RawURLEncoding.EncodeToString(sig)}
}

func TestAOS439JanelaV1ChegaAoVerificadorDoNo(t *testing.T) {
	agora := tnClock()()
	aberta, logAberta, auto, humano := noComJanela(t, agora.Add(time.Hour))
	tok := cunharSobMandato(t, auto, humano, mandatoV1DeTeste(humano))
	p, err := aberta.Verifier.Verify(context.Background(), tok)
	if err != nil || p.MandateID != "m-v1" {
		t.Fatalf("dentro da janela o v1 tinha de verificar no no: %+v %v", p, err)
	}
	if !strings.Contains(logAberta, "mandatos v1 (sem requesters, AOS-439): ACEITES ate") {
		t.Fatalf("o banner tem de declarar a janela aberta:\n%s", logAberta)
	}

	fechada, logFechada, auto2, humano2 := noComJanela(t, time.Time{})
	tok2 := cunharSobMandato(t, auto2, humano2, mandatoV1DeTeste(humano2))
	if _, err := fechada.Verifier.Verify(context.Background(), tok2); !errors.Is(err, identity.ErrMandateV1Closed) {
		t.Fatalf("sem AOS_MANDATE_V1_UNTIL o v1 tinha de ser recusado, veio %v", err)
	}
	if credencialDoRunRecusadaNoNo(context.Background(), fechada, tok2) == "" {
		t.Fatal("a porta do POST /runs tinha de recusar o v1 fora da janela")
	}
	if !strings.Contains(logFechada, "mandatos v1 (sem requesters, AOS-439): RECUSADOS") {
		t.Fatalf("o banner tem de declarar a janela fechada:\n%s", logFechada)
	}
}

func TestAOS439ParseDaJanelaV1(t *testing.T) {
	agora := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	if got, err := parseMandateV1Until("", agora); err != nil || !got.IsZero() {
		t.Fatalf("vazia tem de dar zero (fechada): %v %v", got, err)
	}
	if got, err := parseMandateV1Until(" 2026-10-25T23:59:59Z ", agora); err != nil || got.Format(time.RFC3339) != "2026-10-25T23:59:59Z" {
		t.Fatalf("RFC 3339 valida: %v %v", got, err)
	}
	for _, mau := range []string{"2026-10-25", "amanha", "2099-01-01T00:00:00Z"} {
		if _, err := parseMandateV1Until(mau, agora); !errors.Is(err, ErrBadMandateV1Until) {
			t.Errorf("AOS_MANDATE_V1_UNTIL=%q tinha de abortar, veio %v", mau, err)
		}
	}
}

// A VIA DE ACESSO do operador (`aos audit-trail`) mostra quem pediu e sob que mandato — é por ela
// que se verifica o critério de produção — e não muda a linha de um registo que não os tem.
func TestAOS439AuditTrailMostraQuemPediu(t *testing.T) {
	com := audit.AuditRecord{SchemaVersion: audit.SchemaV4, Principal: audit.Principal{RequestedBy: "sub-alice", MandateID: "m-1"}}
	if got := quemPediu(com); got != " requested_by=sub-alice mandate=m-1" {
		t.Fatalf("quemPediu = %q", got)
	}
	// Num v2/v3 os campos não estão selados: não se mostram, estejam ou não no registo.
	for _, v := range []uint8{0, audit.SchemaV2, audit.SchemaV3} {
		naoSelado := com
		naoSelado.SchemaVersion = v
		if got := quemPediu(naoSelado); got != "" {
			t.Fatalf("v%d: o audit-trail mostrou campos que o selo nao cobre: %q", v, got)
		}
	}
	if got := quemPediu(audit.AuditRecord{}); got != "" {
		t.Fatalf("um registo sem submissor nao ganha texto: %q", got)
	}
}

func TestAOS439ParseEBannerDoWORMV4(t *testing.T) {
	for _, s := range []string{"", "0", "off", "false"} {
		if v, err := parseAuditWriteV4(s); err != nil || v {
			t.Errorf("AOS_AUDIT_WRITE_V4=%q tem de dar v3: %v %v", s, v, err)
		}
	}
	if v, err := parseAuditWriteV4(" 1 "); err != nil || !v {
		t.Fatalf("AOS_AUDIT_WRITE_V4=1 liga o v4: %v %v", v, err)
	}
	if _, err := parseAuditWriteV4("talvez"); !errors.Is(err, ErrBadAuditWriteV4) {
		t.Fatalf("valor invalido tem de abortar, veio %v", err)
	}
	// AOS-446 fase 1: o banner passou a receber a EPOCA (ha tres) em vez de um booleano, e a
	// instrucao passou a nomear a variavel nova. A do AOS-439 continua a funcionar — ver
	// TestAOS446EpocaDeEscritaDoWORM, que prova as duas e o desacordo entre elas.
	if l := strings.Join(wormV4PostureBanner(audit.SchemaV3, true), ""); !strings.Contains(l, "escreve v3") || !strings.Contains(l, "AOS_AUDIT_WRITE_SCHEMA=5") {
		t.Fatalf("banner v3: %s", l)
	}
	if l := strings.Join(wormV4PostureBanner(audit.SchemaV4, true), ""); !strings.Contains(l, "escreve v4") || !strings.Contains(l, "rollback") {
		t.Fatalf("banner v4: %s", l)
	}
}

func TestAOS439BannerDaJanelaSoComEmissorMandatado(t *testing.T) {
	if l := janelaV1PostureBanner("", time.Now(), time.Now()); l != nil {
		t.Fatalf("sem emissor mandatado nao ha mandatos a declarar: %v", l)
	}
	agora := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	if l := strings.Join(janelaV1PostureBanner("iss", agora.Add(-time.Second), agora), ""); !strings.Contains(l, "fechou em") {
		t.Fatalf("janela no passado tem de dizer que fechou: %s", l)
	}
}
