package identity

// aos427_mandato_test.go — O MANDATO (AOS-427, ADR-033).
//
// O emissor automático vive num servidor cujo Vault se destrava sozinho. Estes testes assumem o
// pior caso — o emissor COMPROMETIDO — e provam que o nó só aceita o que o HUMANO assinou. Os
// tokens fora do mandato são assinados DIRECTAMENTE com `signToken`, contornando a verificação de
// cortesia do [Issuer]: é isso que um atacante com acesso ao Vault faria.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/platform/identity/delegation"
	"github.com/aos-ref/substrate/eventstore"
)

const (
	issAuto   = "iss:aos-issuer-auto"
	issManual = "iss:aos-issuer"
)

var t0 = time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)

// revogados é um registo de revogação de teste (jti e chaves de mandato).
type revogados map[string]bool

func (r revogados) IsRevoked(_ context.Context, k string) (bool, error) { return r[k], nil }

type cenarioMandato struct {
	humano   ed25519.PrivateKey // a chave do humano, que NUNCA esteve no servidor
	emissor  ed25519.PrivateKey // a chave do emissor automático (no Vault do servidor)
	manual   ed25519.PrivateKey // o emissor manual, confiado por inteiro
	mandato  SignedMandate
	revogado revogados
}

func novoCenario(t *testing.T) *cenarioMandato {
	t.Helper()
	c := &cenarioMandato{humano: chaveDeTeste(t), emissor: chaveDeTeste(t), manual: chaveDeTeste(t), revogado: revogados{}}
	m := Mandate{
		ID: "m-1", Human: "alice", Board: "board-eu",
		AgentID: "agent:aos-orq", AgentClass: "planner", PolicyRef: "policy://planner",
		Scope:  []string{"run:submit", "model:invoke"},
		Issuer: issAuto, MaxTTLSeconds: int64((45 * time.Minute).Seconds()),
		NotBefore: t0.Unix(), NotAfter: t0.Add(30 * 24 * time.Hour).Unix(),
	}
	sm, err := SignMandate(c.humano, m)
	if err != nil {
		t.Fatalf("assinar o mandato: %v", err)
	}
	c.mandato = sm
	return c
}

func (c *cenarioMandato) verificador(agora time.Time) *Verifier {
	return NewVerifier(
		WithTrustedIssuer(issManual, c.manual.Public().(ed25519.PublicKey)),
		WithMandatedIssuer(issAuto, c.emissor.Public().(ed25519.PublicKey),
			map[string]ed25519.PublicKey{"alice": c.humano.Public().(ed25519.PublicKey)}),
		WithRevocations(c.revogado),
		WithVerifierClock(func() time.Time { return agora }),
		WithVerifierLeeway(0),
	)
}

// cunharHonesto cunha pelo caminho normal do Issuer, dentro do mandato.
func (c *cenarioMandato) cunharHonesto(t *testing.T, sm *SignedMandate) Token {
	t.Helper()
	iss, err := NewIssuer(issAuto, c.emissor, map[string]ClassPolicy{
		"planner": {TTL: 45 * time.Minute, Scope: []string{"run:submit", "model:invoke"}},
	}, WithIssuerClock(func() time.Time { return t0.Add(time.Hour) }))
	if err != nil {
		t.Fatalf("emissor: %v", err)
	}
	tok, err := iss.Issue(context.Background(), IssueRequest{
		UserID: "alice", AgentID: "agent:aos-orq", AgentClass: "planner", PolicyRef: "policy://planner",
		Board: "board-eu", UserAuthority: []string{"run:submit"}, Mandate: sm,
	})
	if err != nil {
		t.Fatalf("cunhar dentro do mandato: %v", err)
	}
	return tok
}

// claimsDentro são claims que o mandato cobre, para os testes as desviarem campo a campo.
func (c *cenarioMandato) claimsDentro(t *testing.T) Claims {
	t.Helper()
	return c.cunharHonesto(t, &c.mandato).Claims
}

// assinarComoAtacante assina claims arbitrárias com a chave do emissor automático — sem passar
// pelo Issuer. É o que tem quem compromete o servidor e pede assinaturas ao Vault.
func (c *cenarioMandato) assinarComoAtacante(t *testing.T, cl Claims) string {
	t.Helper()
	compact, err := signToken(c.emissor, issAuto, cl)
	if err != nil {
		t.Fatalf("assinar: %v", err)
	}
	return compact
}

func TestAOS427MandatoDentroDosLimitesVerifica(t *testing.T) {
	c := novoCenario(t)
	tok := c.cunharHonesto(t, &c.mandato)
	p, err := c.verificador(t0.Add(time.Hour)).Verify(context.Background(), tok.Compact)
	if err != nil {
		t.Fatalf("um token dentro do mandato tinha de verificar: %v", err)
	}
	if p.MandateID != "m-1" {
		t.Fatalf("o Principal tem de nomear o mandato verificado, veio %q", p.MandateID)
	}
}

// Sem mandato, o emissor automático não é confiado — nem com a assinatura certa.
func TestAOS427EmissorMandatadoSemMandatoERecusado(t *testing.T) {
	c := novoCenario(t)
	cl := c.claimsDentro(t)
	cl.Mandate = nil
	_, err := c.verificador(t0.Add(time.Hour)).Verify(context.Background(), c.assinarComoAtacante(t, cl))
	if !errors.Is(err, ErrMandateRequired) {
		t.Fatalf("token do emissor automatico sem mandato tinha de dar ErrMandateRequired, veio %v", err)
	}
}

// O ATAQUE CENTRAL: quem compromete o servidor não tem a chave do humano, mas pode gerar uma sua
// e assinar o mandato que quiser. O nó só aceita a chave PINADA.
func TestAOS427MandatoAssinadoPorOutraChaveERecusado(t *testing.T) {
	c := novoCenario(t)
	cl := c.claimsDentro(t)
	forjado, err := SignMandate(chaveDeTeste(t), c.mandato.Mandate)
	if err != nil {
		t.Fatalf("assinar o mandato forjado: %v", err)
	}
	cl.Mandate = &forjado
	_, err = c.verificador(t0.Add(time.Hour)).Verify(context.Background(), c.assinarComoAtacante(t, cl))
	if !errors.Is(err, ErrMandateInvalid) {
		t.Fatalf("mandato assinado por uma chave nao pinada tinha de dar ErrMandateInvalid, veio %v", err)
	}
}

// Alargar o mandato depois de assinado parte a assinatura do humano.
func TestAOS427MandatoAdulteradoERecusado(t *testing.T) {
	c := novoCenario(t)
	cl := c.claimsDentro(t)
	adulterado := c.mandato
	adulterado.Mandate.Scope = append(append([]string(nil), adulterado.Mandate.Scope...), "cap:admin")
	cl.Mandate = &adulterado
	cl.Scope = append(cl.Scope, "cap:admin")
	cl.DelegationChain[0].Authority = append(cl.DelegationChain[0].Authority, "cap:admin")
	_, err := c.verificador(t0.Add(time.Hour)).Verify(context.Background(), c.assinarComoAtacante(t, cl))
	if !errors.Is(err, ErrMandateInvalid) {
		t.Fatalf("mandato alargado depois de assinado tinha de dar ErrMandateInvalid, veio %v", err)
	}
}

// Um mandato para outro humano pinado não autoriza cunhar em nome de alice.
func TestAOS427MandatoDeUmHumanoNaoCunhaParaOutro(t *testing.T) {
	c := novoCenario(t)
	bob := chaveDeTeste(t)
	mb := c.mandato.Mandate
	mb.ID, mb.Human = "m-bob", "bob"
	smb, err := SignMandate(bob, mb)
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(
		WithMandatedIssuer(issAuto, c.emissor.Public().(ed25519.PublicKey), map[string]ed25519.PublicKey{
			"alice": c.humano.Public().(ed25519.PublicKey), "bob": bob.Public().(ed25519.PublicKey),
		}),
		WithVerifierClock(func() time.Time { return t0.Add(time.Hour) }), WithVerifierLeeway(0),
	)
	cl := c.claimsDentro(t) // user_id = alice
	cl.Mandate = &smb       // mandato válido, mas do bob
	_, err = v.Verify(context.Background(), c.assinarComoAtacante(t, cl))
	if !errors.Is(err, ErrMandateViolated) {
		t.Fatalf("mandato do bob num token da alice tinha de dar ErrMandateViolated, veio %v", err)
	}
}

// Cada campo que o mandato fixa, desviado um a um por um emissor comprometido.
func TestAOS427TokenForaDoMandatoERecusado(t *testing.T) {
	for _, caso := range []struct {
		nome   string
		mudar  func(*Claims)
		quando time.Time
	}{
		{"outro agente", func(c *Claims) { c.AgentID = "agent:outro"; c.DelegationChain[0].ActAs = "agent:outro" }, t0.Add(time.Hour)},
		{"outra classe", func(c *Claims) { c.AgentClass = "admin" }, t0.Add(time.Hour)},
		{"outra politica", func(c *Claims) { c.PolicyRef = "policy://admin" }, t0.Add(time.Hour)},
		{"outro board", func(c *Claims) { c.Board = "board-us" }, t0.Add(time.Hour)},
		{"escopo alargado", func(c *Claims) {
			c.Scope = append(c.Scope, "cap:admin")
			c.DelegationChain[0].Authority = append(c.DelegationChain[0].Authority, "cap:admin")
		}, t0.Add(time.Hour)},
		{"TTL de uma hora", func(c *Claims) { c.Expiry = c.IssuedAt + 3600 }, t0.Add(time.Hour)},
		{"nasce antes do mandato", func(c *Claims) {
			c.IssuedAt, c.NotBefore, c.Expiry = t0.Unix()-60, t0.Unix()-60, t0.Unix()+600
		}, t0.Add(time.Minute)},
		{"morre depois do mandato", func(c *Claims) {
			fim := c.Mandate.Mandate.NotAfter
			c.IssuedAt, c.NotBefore, c.Expiry = fim-600, fim-600, fim+600
		}, time.Unix(c0NotAfter()-300, 0)},
	} {
		t.Run(caso.nome, func(t *testing.T) {
			c := novoCenario(t)
			cl := c.claimsDentro(t)
			caso.mudar(&cl)
			_, err := c.verificador(caso.quando).Verify(context.Background(), c.assinarComoAtacante(t, cl))
			if !errors.Is(err, ErrMandateViolated) {
				t.Fatalf("%s: tinha de dar ErrMandateViolated, veio %v", caso.nome, err)
			}
		})
	}
}

// c0NotAfter é o fim da janela do mandato de [novoCenario].
func c0NotAfter() int64 { return t0.Add(30 * 24 * time.Hour).Unix() }

// Revogar o MANDATO mata todos os tokens cunhados sob ele, sem saber os seus jti.
func TestAOS427MandatoRevogadoMataOsTokensVivos(t *testing.T) {
	c := novoCenario(t)
	tok := c.cunharHonesto(t, &c.mandato)
	v := c.verificador(t0.Add(time.Hour))
	if _, err := v.Verify(context.Background(), tok.Compact); err != nil {
		t.Fatalf("antes da revogacao tinha de verificar: %v", err)
	}
	c.revogado[MandateRevocationKey("m-1")] = true
	_, err := v.Verify(context.Background(), tok.Compact)
	if !errors.Is(err, ErrMandateRevoked) {
		t.Fatalf("depois de revogar o mandato, o token vivo tinha de dar ErrMandateRevoked, veio %v", err)
	}
}

// O emissor manual não muda: continua confiado por inteiro, e um mandato embebido nele não é
// lido nem devolvido.
func TestAOS427EmissorManualNaoMuda(t *testing.T) {
	c := novoCenario(t)
	cl := c.claimsDentro(t)
	cl.Issuer = issManual
	compact, err := signToken(c.manual, issManual, cl)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.verificador(t0.Add(time.Hour)).Verify(context.Background(), compact)
	if err != nil {
		t.Fatalf("o emissor manual tinha de continuar a verificar: %v", err)
	}
	if p.MandateID != "" {
		t.Fatalf("um mandato que ninguem verificou nao se devolve, veio MandateID=%q", p.MandateID)
	}
}

// Composição fail-closed: sem nenhum signatário válido, o emissor automático fica SEM anchor —
// nunca passa a verificar sem mandato.
func TestAOS427EmissorMandatadoSemSignatariosNaoEConfiado(t *testing.T) {
	c := novoCenario(t)
	v := NewVerifier(
		WithMandatedIssuer(issAuto, c.emissor.Public().(ed25519.PublicKey), nil),
		WithVerifierClock(func() time.Time { return t0.Add(time.Hour) }),
	)
	_, err := v.Verify(context.Background(), c.cunharHonesto(t, &c.mandato).Compact)
	if !errors.Is(err, ErrUnknownIssuer) {
		t.Fatalf("sem signatarios o emissor automatico tinha de ficar sem anchor (ErrUnknownIssuer), veio %v", err)
	}
}

// A ordem das opções não reabre a porta: registar o mesmo iss também como confiado por inteiro
// não o dispensa do mandato.
func TestAOS427RegistoDuploNaoDispensaOMandato(t *testing.T) {
	c := novoCenario(t)
	pub := c.emissor.Public().(ed25519.PublicKey)
	signers := map[string]ed25519.PublicKey{"alice": c.humano.Public().(ed25519.PublicKey)}
	for _, ordem := range [][]VerifierOption{
		{WithTrustedIssuer(issAuto, pub), WithMandatedIssuer(issAuto, pub, signers)},
		{WithMandatedIssuer(issAuto, pub, signers), WithTrustedIssuer(issAuto, pub)},
	} {
		v := NewVerifier(append(ordem, WithVerifierClock(func() time.Time { return t0.Add(time.Hour) }))...)
		cl := c.claimsDentro(t)
		cl.Mandate = nil
		if _, err := v.Verify(context.Background(), c.assinarComoAtacante(t, cl)); !errors.Is(err, ErrMandateRequired) {
			t.Fatalf("registo duplo nao pode dispensar o mandato, veio %v", err)
		}
	}
}

// O emissor honesto recusa cunhar fora do mandato (cortesia — quem decide é o nó).
func TestAOS427EmissorHonestoRecusaForaDoMandato(t *testing.T) {
	c := novoCenario(t)
	iss, err := NewIssuer(issAuto, c.emissor, map[string]ClassPolicy{
		"planner": {TTL: 45 * time.Minute, Scope: []string{"run:submit"}},
	}, WithIssuerClock(func() time.Time { return t0.Add(time.Hour) }))
	if err != nil {
		t.Fatal(err)
	}
	_, err = iss.Issue(context.Background(), IssueRequest{
		UserID: "alice", AgentID: "agent:outro", AgentClass: "planner", PolicyRef: "policy://planner",
		Board: "board-eu", UserAuthority: []string{"run:submit"}, Mandate: &c.mandato,
	})
	if !errors.Is(err, ErrMandateViolated) {
		t.Fatalf("o emissor honesto tinha de recusar cunhar fora do mandato, veio %v", err)
	}
}

func TestAOS427FormaDoMandato(t *testing.T) {
	base := novoCenario(t).mandato.Mandate
	for _, caso := range []struct {
		nome  string
		mudar func(*Mandate)
	}{
		{"janela acima do tecto", func(m *Mandate) { m.NotAfter = m.NotBefore + int64((MandatoValidadeMaxima + time.Second).Seconds()) }},
		{"janela invertida", func(m *Mandate) { m.NotAfter = m.NotBefore }},
		{"TTL acima do tecto da biblioteca", func(m *Mandate) { m.MaxTTLSeconds = int64((TTLMaximo + time.Second).Seconds()) }},
		{"TTL zero", func(m *Mandate) { m.MaxTTLSeconds = 0 }},
		{"escopo vazio", func(m *Mandate) { m.Scope = nil }},
		{"escopo repetido", func(m *Mandate) { m.Scope = []string{"a", "a"} }},
		{"humano com prefixo", func(m *Mandate) { m.Human = "human:alice" }},
		{"sem board", func(m *Mandate) { m.Board = "" }},
		{"sem politica", func(m *Mandate) { m.PolicyRef = "" }},
		{"sem id", func(m *Mandate) { m.ID = "" }},
	} {
		t.Run(caso.nome, func(t *testing.T) {
			m := base
			m.Scope = append([]string(nil), base.Scope...)
			caso.mudar(&m)
			if _, err := SignMandate(chaveDeTeste(t), m); !errors.Is(err, ErrMandateInvalid) {
				t.Fatalf("%s: SignMandate tinha de recusar com ErrMandateInvalid, veio %v", caso.nome, err)
			}
		})
	}
}

// A forma canónica não depende da ordem do escopo, e distingue campos que um concatenar ingénuo
// confundiria.
func TestAOS427FormaCanonicaDoMandato(t *testing.T) {
	a := novoCenario(t).mandato.Mandate
	b := a
	b.Scope = []string{a.Scope[1], a.Scope[0]}
	if string(a.SigningInput()) != string(b.SigningInput()) {
		t.Fatal("a ordem do escopo nao pode mudar os bytes assinados")
	}
	x, y := a, a
	x.AgentID, x.AgentClass = "ab", "c"
	y.AgentID, y.AgentClass = "a", "bc"
	if string(x.SigningInput()) == string(y.SigningInput()) {
		t.Fatal("campos adjacentes tem de ser separados sem ambiguidade (netstrings)")
	}
}

// ---------------------------------------------------------------------------------------------
// ACHADOS DA REVISÃO ADVERSARIAL (2026-09-25). Cada um tem o seu sensor.
// ---------------------------------------------------------------------------------------------

// ALTO: o TTL do mandato conta a partir de AGORA. Um iat no futuro (ou um nbf a zero, que o
// passo 4 do Verify salta) fazia de um token de 45 minutos um token de 30 dias.
func TestAOS427TTLDoMandatoContaAPartirDeAgora(t *testing.T) {
	agora := t0.Add(time.Hour)
	fim := c0NotAfter()
	for _, caso := range []struct {
		nome  string
		mudar func(*Claims)
	}{
		{"iat no fim do mandato, nbf zero", func(c *Claims) { c.IssuedAt, c.NotBefore, c.Expiry = fim-2700, 0, fim }},
		{"iat no fim do mandato, nbf no inicio", func(c *Claims) { c.IssuedAt, c.NotBefore, c.Expiry = fim-2700, t0.Unix(), fim }},
		{"nbf diferente de iat", func(c *Claims) { c.NotBefore = c.IssuedAt - 1 }},
	} {
		t.Run(caso.nome, func(t *testing.T) {
			c := novoCenario(t)
			cl := c.claimsDentro(t)
			caso.mudar(&cl)
			_, err := c.verificador(agora).Verify(context.Background(), c.assinarComoAtacante(t, cl))
			if !errors.Is(err, ErrMandateViolated) {
				t.Fatalf("%s: tinha de dar ErrMandateViolated, veio %v", caso.nome, err)
			}
		})
	}
}

// BAIXO: elos intermédios que o humano não autorizou não entram na auditoria.
func TestAOS427EmissorMandatadoSoCunhaARaiz(t *testing.T) {
	c := novoCenario(t)
	cl := c.claimsDentro(t)
	raiz, err := delegation.NewRoot("human:alice", "agent:fantasma", []string{"run:submit", "cap:admin"})
	if err != nil {
		t.Fatal(err)
	}
	cadeia, err := raiz.Extend("agent:aos-orq", []string{"run:submit"})
	if err != nil {
		t.Fatal(err)
	}
	cl.DelegationChain = cadeia
	_, err = c.verificador(t0.Add(time.Hour)).Verify(context.Background(), c.assinarComoAtacante(t, cl))
	if !errors.Is(err, ErrMandateViolated) {
		t.Fatalf("cadeia com elo intermedio tinha de dar ErrMandateViolated, veio %v", err)
	}
}

// BAIXO: um mandato assinado para OUTRO emissor não serve a este.
func TestAOS427MandatoDeOutroEmissorNaoServe(t *testing.T) {
	c := novoCenario(t)
	m := c.mandato.Mandate
	m.Issuer = "iss:outro"
	outro, err := SignMandate(c.humano, m)
	if err != nil {
		t.Fatal(err)
	}
	cl := c.claimsDentro(t)
	cl.Mandate = &outro
	_, err = c.verificador(t0.Add(time.Hour)).Verify(context.Background(), c.assinarComoAtacante(t, cl))
	if !errors.Is(err, ErrMandateViolated) {
		t.Fatalf("mandato de outro emissor tinha de dar ErrMandateViolated, veio %v", err)
	}
}

// BAIXO: os tectos comparam-se em segundos, sem a volta do time.Duration; e o ID é o que a rota
// de revogação consegue revogar.
func TestAOS427FormaDoMandatoSemOverflowNemIDsIrrevogaveis(t *testing.T) {
	base := novoCenario(t).mandato.Mandate
	for _, caso := range []struct {
		nome  string
		mudar func(*Mandate)
	}{
		{"max_ttl que da a volta", func(m *Mandate) { m.MaxTTLSeconds = 18446744074 }},
		{"janela que da a volta", func(m *Mandate) { m.NotAfter = m.NotBefore + 18446744074 }},
		{"nbf antes da epoch", func(m *Mandate) { m.NotBefore = -9e18 }},
		{"id com espaco", func(m *Mandate) { m.ID = "m1 " }},
		{"id com dois pontos", func(m *Mandate) { m.ID = "mandate:x" }},
	} {
		t.Run(caso.nome, func(t *testing.T) {
			m := base
			m.Scope = append([]string(nil), base.Scope...)
			caso.mudar(&m)
			if err := m.Validate(); !errors.Is(err, ErrMandateInvalid) {
				t.Fatalf("%s: tinha de dar ErrMandateInvalid, veio %v", caso.nome, err)
			}
		})
	}
}

// A revogação de um mandato sobrevive ao restart: o registo reconstrói-se do Event Store.
func TestAOS427RevogacaoDoMandatoSobreviveAoRestart(t *testing.T) {
	ctx := context.Background()
	es, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := NewRevocations(es).Revoke(ctx, MandateRevocationKey("m-1")); err != nil {
		t.Fatalf("revogar: %v", err)
	}
	depois := NewRevocations(es)
	if err := depois.Rebuild(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	c := novoCenario(t)
	tok := c.cunharHonesto(t, &c.mandato)
	v := NewVerifier(
		WithMandatedIssuer(issAuto, c.emissor.Public().(ed25519.PublicKey),
			map[string]ed25519.PublicKey{"alice": c.humano.Public().(ed25519.PublicKey)}),
		WithRevocations(depois),
		WithVerifierClock(func() time.Time { return t0.Add(time.Hour) }),
	)
	if _, err := v.Verify(ctx, tok.Compact); !errors.Is(err, ErrMandateRevoked) {
		t.Fatalf("depois do restart o mandato revogado tinha de dar ErrMandateRevoked, veio %v", err)
	}
}
