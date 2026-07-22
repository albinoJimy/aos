package integration

import (
	"context"
	"crypto/ed25519"
	"errors"
	"reflect"
	"testing"
	"time"

	identity "github.com/aos-ref/platform/identity"
)

// Este ficheiro prova a espinha de token self-hosted Nível 2 (AOS-156): a
// [IssuerAuthority] é uma autoridade SEPARADA que detém a chave; o nó só recebe a
// pubkey (trust anchor). As quatro provas:
//
//	(a) um token mintado pela autoridade VERIFICA sob o verifier da sua pubkey;
//	(b) um token de uma chave ROGUE (issuer diferente, ausente do trust anchor) é
//	    REJEITADO — não-forjabilidade relativa ao nó;
//	(c) o verifier/nó NÃO tem forma de mintar (prova ESTRUTURAL: nenhuma API expõe a
//	    chave privada);
//	(d) um humano não-registado ⇒ mint RECUSADO fail-closed.

const (
	iaIssuerID = "iss:aos-idp"
	iaRogueID  = "iss:rogue-idp"
	iaHuman    = "alice"
	iaAgent    = "agt-42"
	iaClass    = "researcher"
	iaCapRead  = "cap:doc.read"
	iaCapWrite = "cap:doc.write"
)

// iaClock é o relógio determinístico partilhado por issuer e verifier (o token nunca
// é visto como expirado num caminho de decisão de teste).
func iaClock() func() time.Time {
	return func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
}

func iaClasses() map[string]identity.ClassPolicy {
	return map[string]identity.ClassPolicy{
		iaClass: {TTL: 10 * time.Minute, Scope: []string{iaCapRead, iaCapWrite}},
	}
}

// newAuthority constrói uma [IssuerAuthority] de teste com relógio determinístico e o
// humano iaHuman já registado na allowlist.
func newAuthority(t *testing.T, issuerID string) *IssuerAuthority {
	t.Helper()
	auth, err := NewIssuerAuthority(AuthorityConfig{
		IssuerID:      issuerID,
		Classes:       iaClasses(),
		Directory:     NewAllowlistDirectory(iaHuman),
		IssuerOptions: []identity.IssuerOption{identity.WithIssuerClock(iaClock())},
	})
	if err != nil {
		t.Fatalf("NewIssuerAuthority(%q): %v", issuerID, err)
	}
	return auth
}

// (a) Um token mintado pela autoridade verifica sob o verifier construído SÓ do seu
// trust anchor (pubkey), e o humano fica na RAIZ da cadeia de delegação.
func TestIssuerAuthority_MintVerifiesUnderTrustAnchor(t *testing.T) {
	ctx := context.Background()
	auth := newAuthority(t, iaIssuerID)

	tok, err := auth.MintForHuman(ctx, iaHuman, iaAgent, iaClass, []string{iaCapRead})
	if err != nil {
		t.Fatalf("MintForHuman: %v", err)
	}

	// O nó constrói o verifier a partir SÓ do trust anchor — nunca de uma chave que
	// controle. Relógio determinístico para não colidir com a janela temporal.
	verifier := NewVerifierFromAuthority(auth, identity.WithVerifierClock(iaClock()))

	principal, err := verifier.Verify(ctx, tok.Compact)
	if err != nil {
		t.Fatalf("Verify de token legítimo falhou: %v", err)
	}
	if principal.AgentID != iaAgent {
		t.Fatalf("AgentID=%q, quero %q", principal.AgentID, iaAgent)
	}
	if !principal.Allows(iaCapRead) {
		t.Fatalf("escopo do principal nao contem %q: %v", iaCapRead, principal.Scope)
	}
	// Humano na RAIZ da cadeia de delegação (reconstrução de autoria "quem autorizou").
	human, err := principal.HumanPrincipal()
	if err != nil {
		t.Fatalf("HumanPrincipal: %v", err)
	}
	if want := "human:" + iaHuman; human != want {
		t.Fatalf("raiz da cadeia=%q, quero %q", human, want)
	}
}

// (b) Não-forjabilidade: um token assinado por uma chave ROGUE (issuer diferente,
// ausente do trust anchor do nó) é rejeitado. Um nó comprometido, sem a chave da
// autoridade legítima, não forja identidades que o verifier aceite.
func TestIssuerAuthority_RogueKeyRejected(t *testing.T) {
	ctx := context.Background()
	legit := newAuthority(t, iaIssuerID)
	rogue := newAuthority(t, iaRogueID) // chave distinta, gerada em runtime; issuer distinto

	// A autoridade rogue minta um token bem-formado para o mesmo humano/agente.
	rogueTok, err := rogue.MintForHuman(ctx, iaHuman, iaAgent, iaClass, []string{iaCapRead})
	if err != nil {
		t.Fatalf("MintForHuman (rogue): %v", err)
	}

	// O verifier do nó confia SÓ na autoridade legítima (o seu trust anchor).
	verifier := NewVerifierFromAuthority(legit, identity.WithVerifierClock(iaClock()))

	if _, err := verifier.Verify(ctx, rogueTok.Compact); err == nil {
		t.Fatal("token de issuer rogue foi ACEITE — nao-forjabilidade violada")
	} else if !errors.Is(err, identity.ErrUnknownIssuer) {
		t.Fatalf("erro=%v, quero ErrUnknownIssuer (issuer rogue ausente do trust anchor)", err)
	}
}

// (b') Não-forjabilidade CRIPTOGRÁFICA — o NÚCLEO do deliverable, e o ataque realista
// que (b) NÃO exercita. Um nó comprometido não tem a chave da autoridade legítima, mas
// pode pôr QUALQUER string em `iss`; logo o ataque não é usar um issuer-id distinto
// (isso pára no passo 2 do verifier, ErrUnknownIssuer, antes da cripto) — é fazer-se
// passar PELO issuer CONFIADO: mintar um token com iss=iaIssuerID assinado com a SUA
// PRÓPRIA chave. Esse token PASSA o passo 2 (issuer conhecido) e alcança o passo 3
// (ed25519.Verify em verifier.go:133), que TEM de o rejeitar com ErrSignatureInvalid.
// É o único caso que prova a integridade de assinatura — a essência da
// não-forjabilidade relativa ao nó. Sem ele, a suite passaria mesmo com ed25519.Verify
// desligado.
func TestIssuerAuthority_ImpostorSameIssuerIDRejected(t *testing.T) {
	ctx := context.Background()
	legit := newAuthority(t, iaIssuerID)

	// Impostor: MESMO issuer-id CONFIADO (iaIssuerID), mas chave de assinatura DISTINTA
	// e determinística — garantidamente != da chave (gerada por CSPRNG) da autoridade
	// legítima. Modela um nó comprometido a assinar como se fosse o issuer legítimo.
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = 0x7e
	}
	impostor, err := NewIssuerAuthority(AuthorityConfig{
		IssuerID:      iaIssuerID, // MESMO id do trust anchor legítimo
		Classes:       iaClasses(),
		Directory:     NewAllowlistDirectory(iaHuman),
		SigningKey:    ed25519.NewKeyFromSeed(seed), // chave do atacante, != da legítima
		IssuerOptions: []identity.IssuerOption{identity.WithIssuerClock(iaClock())},
	})
	if err != nil {
		t.Fatalf("NewIssuerAuthority (impostor): %v", err)
	}

	forged, err := impostor.MintForHuman(ctx, iaHuman, iaAgent, iaClass, []string{iaCapRead})
	if err != nil {
		t.Fatalf("MintForHuman (impostor): %v", err)
	}

	// O verifier do nó confia SÓ no trust anchor da autoridade LEGÍTIMA (mesmo id, pubkey
	// legítima). O token forjado tem iss=iaIssuerID ⇒ passa o passo 2 e chega à
	// verificação de assinatura, que o recusa: a chave do impostor não valida sob a
	// pubkey legítima.
	verifier := NewVerifierFromAuthority(legit, identity.WithVerifierClock(iaClock()))

	if _, err := verifier.Verify(ctx, forged.Compact); err == nil {
		t.Fatal("token de impostor (mesmo issuer-id, chave distinta) foi ACEITE — nao-forjabilidade criptografica violada")
	} else if !errors.Is(err, identity.ErrSignatureInvalid) {
		t.Fatalf("erro=%v, quero ErrSignatureInvalid (a assinatura do impostor tem de falhar no passo 3 do verifier)", err)
	}
}

// (c) Prova ESTRUTURAL de que o nó não pode mintar: nenhuma superfície pública
// ([IssuerAuthority], [identity.Verifier], [identity.Token]) devolve a chave privada
// ed25519. O nó recebe SÓ a pubkey (via TrustAnchor); não há via para obter o
// material de assinatura, logo não há como forjar.
func TestIssuerAuthority_NoPrivateKeyEscape(t *testing.T) {
	privType := reflect.TypeOf(ed25519.PrivateKey{})
	// Um accessor que devolvesse *identity.Issuer é tão perigoso como devolver a chave
	// crua: o detentor pode chamar Issuer.Issue e mintar tokens arbitrários (equivalente
	// a forja). Nenhuma superfície do nó o pode expor.
	issuerPtrType := reflect.TypeOf(&identity.Issuer{})

	targets := []reflect.Type{
		reflect.TypeOf(&IssuerAuthority{}),
		reflect.TypeOf(&identity.Verifier{}),
		reflect.TypeOf(identity.Token{}),
		reflect.TypeOf(identity.Principal{}),
	}
	for _, target := range targets {
		for i := 0; i < target.NumMethod(); i++ {
			m := target.Method(i)
			for j := 0; j < m.Type.NumOut(); j++ {
				switch m.Type.Out(j) {
				case privType:
					t.Fatalf("%s.%s expõe ed25519.PrivateKey — o no poderia mintar", target, m.Name)
				case issuerPtrType:
					t.Fatalf("%s.%s expõe *identity.Issuer — o detentor poderia mintar (equivalente a forja)", target, m.Name)
				}
			}
		}
	}

	// TrustAnchor devolve APENAS (string, ed25519.PublicKey): confirma que a única
	// saída de material de chave é a PUBKEY, nunca a privada.
	auth := newAuthority(t, iaIssuerID)
	mv := reflect.ValueOf(auth.TrustAnchor)
	if mv.Type().NumOut() != 2 {
		t.Fatalf("TrustAnchor devolve %d valores, quero 2 (issuerID, pubkey)", mv.Type().NumOut())
	}
	if mv.Type().Out(1) != reflect.TypeOf(ed25519.PublicKey{}) {
		t.Fatalf("TrustAnchor.out(1)=%v, quero ed25519.PublicKey", mv.Type().Out(1))
	}
}

// (d) Humano não-registado ⇒ mint recusado fail-closed (nenhum token emitido).
func TestIssuerAuthority_UnregisteredHumanRefused(t *testing.T) {
	ctx := context.Background()
	auth := newAuthority(t, iaIssuerID) // allowlist só tem iaHuman

	tok, err := auth.MintForHuman(ctx, "mallory", iaAgent, iaClass, []string{iaCapRead})
	if err == nil {
		t.Fatal("mint para humano nao-registado foi ACEITE — fail-closed violado")
	}
	if !errors.Is(err, ErrHumanNotAuthenticated) {
		t.Fatalf("erro=%v, quero ErrHumanNotAuthenticated", err)
	}
	if !errors.Is(err, ErrHumanNotRegistered) {
		t.Fatalf("erro=%v, quero envolver ErrHumanNotRegistered (causa concreta)", err)
	}
	if tok.Compact != "" {
		t.Fatal("token nao-vazio devolvido numa recusa — nenhum material devia ser emitido")
	}
}

// Guarda de construção: sem IssuerID / sem Directory ⇒ fail-closed.
func TestNewIssuerAuthority_FailClosed(t *testing.T) {
	if _, err := NewIssuerAuthority(AuthorityConfig{Directory: NewAllowlistDirectory()}); !errors.Is(err, ErrNoIssuerID) {
		t.Fatalf("sem IssuerID: erro=%v, quero ErrNoIssuerID", err)
	}
	if _, err := NewIssuerAuthority(AuthorityConfig{IssuerID: iaIssuerID, Classes: iaClasses()}); !errors.Is(err, ErrNoHumanDirectory) {
		t.Fatalf("sem Directory: erro=%v, quero ErrNoHumanDirectory", err)
	}
}

// Demonstra o wiring: o verifier construído da autoridade liga-se a SecuredConfig.Verifier
// (o nó recebe só a pubkey; a chave privada nunca entra na config).
func TestNewVerifierFromAuthority_WiresSecuredConfig(t *testing.T) {
	auth := newAuthority(t, iaIssuerID)
	cfg := SecuredConfig{Verifier: NewVerifierFromAuthority(auth)}
	if cfg.Verifier == nil {
		t.Fatal("SecuredConfig.Verifier nil — o wiring do trust anchor falhou")
	}
}
