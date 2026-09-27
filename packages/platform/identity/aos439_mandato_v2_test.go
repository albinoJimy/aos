package identity

// aos439_mandato_v2_test.go — O MANDATO NOMEIA POR QUEM O EMISSOR AGE (AOS-439, emenda ADR-033 §2.1).
//
// O mandato v1 autorizava o emissor automático a cunhar para o drenador, e o drenador corria planos
// de QUALQUER submissor: o humano que assinou respondia por pedidos que nunca viu. O v2 enumera os
// `requesters`, assinados sob um domínio NOVO. Estes testes provam:
//
//   - que os bytes de um v1 não mudaram (um mandato em vigor continua a verificar);
//   - que v1 e v2 não se convertem um no outro sem a chave do humano;
//   - a janela de migração dos v1 (aceites até à data, recusados depois, e fechada por omissão);
//   - que o Principal expõe os requesters e que a decisão «este submissor pode» é exacta.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
)

// mandatoV1 assina um mandato SEM requesters, pelo caminho que o `mandate-sign` usava antes do
// AOS-439 — a [SignMandate] já o recusa, e é por isso que se assina à mão aqui.
func mandatoV1(t *testing.T, humano ed25519.PrivateKey, m Mandate) SignedMandate {
	t.Helper()
	m.Requesters = nil
	if err := m.Validate(); err != nil {
		t.Fatalf("mandato v1 da fixture invalido: %v", err)
	}
	return SignedMandate{Mandate: m, Signature: b64enc(ed25519.Sign(humano, m.SigningInput()))}
}

func (c *cenarioMandato) verificadorComJanela(agora, ate time.Time) *Verifier {
	return NewVerifier(
		WithTrustedIssuer(issManual, c.manual.Public().(ed25519.PublicKey)),
		WithMandatedIssuer(issAuto, c.emissor.Public().(ed25519.PublicKey),
			map[string]ed25519.PublicKey{"alice": c.humano.Public().(ed25519.PublicKey)}),
		WithRevocations(c.revogado),
		WithVerifierClock(func() time.Time { return agora }),
		WithVerifierLeeway(0),
		WithMandateV1Until(ate),
	)
}

// OS BYTES DE UM v1 NÃO MUDARAM. Fixados à mão: se o SigningInput de um mandato sem requesters
// mudasse, o mandato que corre hoje em produção deixaria de verificar no deploy.
func TestAOS439SigningInputV1InalteradoEV2Distinto(t *testing.T) {
	m := Mandate{
		ID: "m", Human: "h", Board: "b", AgentID: "a", AgentClass: "c", PolicyRef: "p",
		Scope: []string{"s2", "s1"}, Issuer: "i", MaxTTLSeconds: 60, NotBefore: 1, NotAfter: 2,
	}
	const v1 = "23:aos.identity.mandate.v1,1:m,1:h,1:b,1:a,1:c,1:p,1:i,2:60,1:1,1:2,1:2,2:s1,2:s2,"
	if got := string(m.SigningInput()); got != v1 {
		t.Fatalf("os bytes de um mandato v1 mudaram:\n veio %q\n era  %q", got, v1)
	}
	m.Requesters = []string{"r2", "r1"}
	const v2 = "23:aos.identity.mandate.v2,1:m,1:h,1:b,1:a,1:c,1:p,1:i,2:60,1:1,1:2,1:2,2:s1,2:s2,1:2,2:r1,2:r2,"
	if got := string(m.SigningInput()); got != v2 {
		t.Fatalf("forma canonica do v2 (dominio novo, requesters ordenados no fim):\n veio %q\n era  %q", got, v2)
	}
}

// ARRANCAR OS REQUESTERS NÃO PRODUZ UM v1 VÁLIDO, e acrescentá-los não produz um v2 válido.
func TestAOS439V1EV2NaoSeConvertemSemAChaveDoHumano(t *testing.T) {
	c := novoCenario(t)
	pub := c.humano.Public().(ed25519.PublicKey)

	despido := c.mandato
	despido.Mandate.Requesters = nil
	if err := despido.VerifySignature(pub); !errors.Is(err, ErrMandateInvalid) {
		t.Fatalf("um v2 sem os requesters nao pode verificar como v1, veio %v", err)
	}

	v1 := mandatoV1(t, c.humano, c.mandato.Mandate)
	vestido := v1
	vestido.Mandate.Requesters = []string{"sub-bob"}
	if err := vestido.VerifySignature(pub); !errors.Is(err, ErrMandateInvalid) {
		t.Fatalf("um v1 com requesters acrescentados nao pode verificar como v2, veio %v", err)
	}

	alargado := c.mandato
	alargado.Mandate.Requesters = append(append([]string(nil), c.mandato.Mandate.Requesters...), "sub-intruso")
	if err := alargado.VerifySignature(pub); !errors.Is(err, ErrMandateInvalid) {
		t.Fatalf("um requester acrescentado depois de assinado tem de invalidar, veio %v", err)
	}
}

func TestAOS439SignMandateRecusaV1(t *testing.T) {
	m := novoCenario(t).mandato.Mandate
	m.Requesters = nil
	if _, err := SignMandate(chaveDeTeste(t), m); !errors.Is(err, ErrMandateInvalid) {
		t.Fatalf("SignMandate tinha de recusar um mandato sem requesters, veio %v", err)
	}
}

func TestAOS439FormaDosRequesters(t *testing.T) {
	base := novoCenario(t).mandato.Mandate
	muitos := make([]string, MandatoRequerentesMaximo+1)
	for i := range muitos {
		muitos[i] = "r" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	for _, caso := range []struct {
		nome string
		rs   []string
	}{
		{"curinga", []string{"*"}},
		{"curinga no meio", []string{"sub-*"}},
		{"vazio", []string{""}},
		{"espaco", []string{"sub bob"}},
		{"virgula", []string{"a,b"}},
		{"repetido", []string{"a", "a"}},
		{"acima do tecto", muitos},
	} {
		t.Run(caso.nome, func(t *testing.T) {
			m := base
			m.Requesters = caso.rs
			if _, err := SignMandate(chaveDeTeste(t), m); !errors.Is(err, ErrMandateInvalid) {
				t.Fatalf("%s: tinha de recusar com ErrMandateInvalid, veio %v", caso.nome, err)
			}
		})
	}
}

// O v2 verifica, e o Principal traz os requesters que o humano assinou.
func TestAOS439V2VerificaEExpoeOsRequesters(t *testing.T) {
	c := novoCenario(t)
	tok := c.cunharHonesto(t, &c.mandato)
	p, err := c.verificador(t0.Add(time.Hour)).Verify(context.Background(), tok.Compact)
	if err != nil {
		t.Fatalf("v2 dentro do mandato tinha de verificar sem janela nenhuma: %v", err)
	}
	if len(p.MandateRequesters) != 2 || p.MandateRequesters[0] != "sub-bob" {
		t.Fatalf("o Principal tem de expor os requesters, veio %v", p.MandateRequesters)
	}
	if err := p.MandateAdmitsRequester("sub-bob"); err != nil {
		t.Fatalf("sub-bob esta no mandato: %v", err)
	}
	for _, fora := range []string{"sub-intruso", "", "SUB-BOB", "sub-bob "} {
		if err := p.MandateAdmitsRequester(fora); !errors.Is(err, ErrMandateRequester) {
			t.Errorf("requested_by=%q nao esta no mandato e tinha de dar ErrMandateRequester, veio %v", fora, err)
		}
	}
}

// A JANELA DE MIGRAÇÃO: um v1 verifica antes da data, é recusado a partir dela, e sem janela
// configurada é recusado sempre (fail-closed).
func TestAOS439JanelaDeMigracaoDosV1(t *testing.T) {
	c := novoCenario(t)
	v1 := mandatoV1(t, c.humano, c.mandato.Mandate)
	tok := c.cunharHonesto(t, &v1)
	agora := t0.Add(time.Hour)

	p, err := c.verificadorComJanela(agora, agora.Add(time.Minute)).Verify(context.Background(), tok.Compact)
	if err != nil {
		t.Fatalf("dentro da janela o v1 tinha de verificar: %v", err)
	}
	if p.MandateID != "m-1" || len(p.MandateRequesters) != 0 {
		t.Fatalf("v1: MandateID=%q requesters=%v", p.MandateID, p.MandateRequesters)
	}
	// Dentro da janela, o v1 não se decide por requesters (não os tem).
	if err := p.MandateAdmitsRequester("qualquer"); err != nil {
		t.Fatalf("um v1 dentro da janela nao restringe o submissor: %v", err)
	}

	for _, caso := range []struct {
		nome string
		v    *Verifier
	}{
		{"no instante do fim (exclusivo)", c.verificadorComJanela(agora, agora)},
		{"depois do fim", c.verificadorComJanela(agora, agora.Add(-time.Second))},
		{"sem janela configurada", c.verificador(agora)},
	} {
		t.Run(caso.nome, func(t *testing.T) {
			if _, err := caso.v.Verify(context.Background(), tok.Compact); !errors.Is(err, ErrMandateV1Closed) {
				t.Fatalf("%s: o v1 tinha de ser recusado com ErrMandateV1Closed, veio %v", caso.nome, err)
			}
		})
	}
}

// Um token de um emissor NÃO mandatado não é restringido pelos requesters — o emissor manual é
// confiado por inteiro, e isso não muda aqui.
func TestAOS439EmissorManualNaoTemRequesters(t *testing.T) {
	var p Principal
	if err := p.MandateAdmitsRequester(""); err != nil {
		t.Fatalf("sem mandato nao ha restricao por requester: %v", err)
	}
}
