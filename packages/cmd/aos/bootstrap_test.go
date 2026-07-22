package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"testing"
	"time"

	control "github.com/aos-ref/kernel/agent-runtime/control"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	risk "github.com/aos-ref/kernel/reference-monitor/risk"
	identity "github.com/aos-ref/platform/identity"

	integration "github.com/aos-ref/integration"
)

// Estes testes provam que o nó `aos` compõe a via de PRODUÇÃO REAL — não os stubs/HMAC
// do demo. Evitam vacuidade exercitando o caminho REAL: cada asserção distingue o
// comportamento do verifier/authenticator REAL do que o stub/HMAC demo dariam.

const (
	tnIssuerID = "iss:aos-test"
	tnHuman    = "operator-alice"
	tnAgent    = "agt-1"
	tnClass    = "researcher"
	tnCap      = "cap:doc.read"
)

// tnClock é um relógio determinístico partilhado por issuer e verifier (o token nunca é
// visto como expirado num caminho de decisão).
func tnClock() func() time.Time {
	return func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
}

// tnBaseConfig é a config mínima VÁLIDA do nó de teste: identidade real (issuer + humano
// autorizado + classe), sem operadores (cada teste acrescenta os que precisa).
func tnBaseConfig() Config {
	return Config{
		IssuerID: tnIssuerID,
		Humans:   []string{tnHuman},
		IssuerClasses: map[string]identity.ClassPolicy{
			tnClass: {TTL: 15 * time.Minute, Scope: []string{tnCap}},
		},
		IssuerClock:   tnClock(),
		VerifierClock: tnClock(),
	}
}

// TestNodeComposesRealVerifier prova (AC1/AC3, Teste (a)) que o nó liga o VERIFIER REAL
// da autoridade AOS-156, NÃO o IdentityStub do demo nem o default sem anchors:
//
//   - um token emitido pela IssuerAuthority do nó PASSA a barreira de identidade da
//     cadeia real (DeniedBy != "identity" — foi negado a JUSANTE, não pela identidade),
//     provando que o verifier real o ACEITOU;
//   - um token de um issuer NÃO-confiado (rogue) é NEGADO EM "identity";
//   - uma chamada ANÓNIMA (sem credencial) é NEGADA EM "identity".
//
// Se a cadeia usasse o IdentityStub, a chamada anónima PASSARIA a identidade (falha do
// teste). Se usasse o default identity.NewVerifier() sem anchors, o token legítimo seria
// NEGADO em "identity" (falha do teste). Só o verifier REAL satisfaz as três asserções.
func TestNodeComposesRealVerifier(t *testing.T) {
	ctx := context.Background()
	node, err := Bootstrap(ctx, tnBaseConfig(), io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer node.Close()

	// Regista uma tool para que a mediação alcance a cadeia (default-deny para não
	// registadas mascararia a atribuição da negação).
	if err := node.Runtime.Register("tool", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	rm := node.Runtime.Monitor()

	// (i) Token LEGÍTIMO emitido pela autoridade do próprio nó (humano autenticado na
	// allowlist ⇒ raiz da cadeia de delegação).
	tok, err := node.Authority.MintForHuman(ctx, tnHuman, tnAgent, tnClass, []string{tnCap})
	if err != nil {
		t.Fatalf("MintForHuman: %v", err)
	}
	decOK, err := rm.Mediate(ctx, tnCall(tok.Compact))
	if err != nil {
		t.Fatalf("Mediate (token legitimo): %v", err)
	}
	// O verifier REAL aceita o token ⇒ a identidade NÃO é quem nega. (A jusante, a
	// revalidação nega por não haver tool set congelado — prova que passámos a
	// identidade sem precisar de um permit end-to-end, que exige bundle PDP assinado /
	// D4.)
	if decOK.DeniedBy == "identity" {
		t.Fatalf("token legitimo NEGADO em identity — o verifier real devia te-lo aceite (DeniedBy=%q, reason=%q)", decOK.DeniedBy, decOK.Reason)
	}

	// (ii) Token de um issuer NÃO-confiado (rogue): autoridade separada, ausente do
	// trust anchor do nó. O verifier real REJEITA-o na identidade.
	rogue, err := integration.NewIssuerAuthority(integration.AuthorityConfig{
		IssuerID:      "iss:rogue",
		Classes:       tnBaseConfig().IssuerClasses,
		Directory:     integration.NewAllowlistDirectory(tnHuman),
		IssuerOptions: []identity.IssuerOption{identity.WithIssuerClock(tnClock())},
	})
	if err != nil {
		t.Fatalf("NewIssuerAuthority (rogue): %v", err)
	}
	rogueTok, err := rogue.MintForHuman(ctx, tnHuman, tnAgent, tnClass, []string{tnCap})
	if err != nil {
		t.Fatalf("MintForHuman (rogue): %v", err)
	}
	decRogue, err := rm.Mediate(ctx, tnCall(rogueTok.Compact))
	if err != nil {
		t.Fatalf("Mediate (rogue): %v", err)
	}
	if decRogue.Effect != referencemonitor.EffectDeny || decRogue.DeniedBy != "identity" {
		t.Fatalf("token rogue devia ser NEGADO em identity, veio effect=%q DeniedBy=%q", decRogue.Effect, decRogue.DeniedBy)
	}

	// (iii) Chamada ANÓNIMA (sem credencial): a identidade real nega (ADR-003). Um
	// IdentityStub deixaria passar — esta asserção é o que o distingue.
	decAnon, err := rm.Mediate(ctx, tnCall(""))
	if err != nil {
		t.Fatalf("Mediate (anonima): %v", err)
	}
	if decAnon.Effect != referencemonitor.EffectDeny || decAnon.DeniedBy != "identity" {
		t.Fatalf("chamada anonima devia ser NEGADA em identity (nao ha IdentityStub), veio effect=%q DeniedBy=%q", decAnon.Effect, decAnon.DeniedBy)
	}
}

// TestNodeSteerUsesEd25519NotHMAC prova (Teste (b)) que o SteerChannel do nó usa o
// Ed25519Authenticator de AOS-160, NÃO o HMACAuthenticator do demo:
//
//   - um sinal ed25519 VÁLIDO de um operador registado é ACEITE;
//   - um sinal produzido pelo HMACAuthenticator demo é RECUSADO (fail-closed).
//
// Se o nó usasse o HMACAuthenticator, o sinal HMAC seria aceite (falha do teste); só o
// autenticador ed25519 real o recusa.
func TestNodeSteerUsesEd25519NotHMAC(t *testing.T) {
	ctx := context.Background()

	const operatorID = "human:operator-carol"
	opPub, opPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	cfg := tnBaseConfig()
	cfg.Operators = map[string]ed25519.PublicKey{operatorID: opPub}
	cfg.SteerClock = tnClock() // frescura determinística

	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer node.Close()

	const runID = "run-steer"
	correction := []byte("aperta o ambito ao ticket")
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("rand nonce: %v", err)
	}

	// (i) Sinal ed25519 VÁLIDO (assinado pela privada do operador, que vive só do lado
	// do emissor) ⇒ aceite pelo canal do nó.
	valid := integration.SignSignal(opPriv, operatorID, runID, control.SignalSteer, correction, nonce, tnClock()())
	if err := node.Steer.Steer(ctx, runID, correction, valid); err != nil {
		t.Fatalf("sinal ed25519 valido devia ser aceite pelo canal do no, veio: %v", err)
	}
	if got, ok := node.Steer.PendingCorrection(runID); !ok || string(got) != string(correction) {
		t.Fatalf("correccao pendente = (%q,%v); quer (%q,true)", got, ok, correction)
	}

	// (ii) Sinal HMAC (demo) para o MESMO emitterID ⇒ RECUSADO. Prova que o canal do nó
	// não é o HMACAuthenticator: um HMAC não valida como ed25519 (e não traz nonce/
	// issued_at), logo o autenticador ed25519 recusa-o fail-closed.
	hmacAuth := control.NewHMACAuthenticator()
	hmacKey := make([]byte, 32)
	if _, err := rand.Read(hmacKey); err != nil {
		t.Fatalf("rand hmac: %v", err)
	}
	hmacAuth.Register(operatorID, hmacKey)
	hmacEmitter, err := hmacAuth.Sign(runID, control.SignalSteer, correction, operatorID)
	if err != nil {
		t.Fatalf("hmac Sign: %v", err)
	}
	if err := node.Steer.Steer(ctx, "run-steer-hmac", correction, hmacEmitter); err == nil {
		t.Fatal("sinal HMAC demo foi ACEITE pelo canal do no — o no nao estaria a usar o Ed25519Authenticator")
	} else if !errors.Is(err, control.ErrUnauthenticated) {
		t.Fatalf("sinal HMAC devia dar ErrUnauthenticated (recusado fail-closed), veio: %v", err)
	}
}

// TestBootstrapFailClosed prova (Teste (c)) que um colaborador de IDENTIDADE obrigatório
// em falta ABORTA o arranque — o nó nunca sobe sem a identidade real ligada.
func TestBootstrapFailClosed(t *testing.T) {
	ctx := context.Background()

	// Sem IssuerID: sem trust anchor, o verifier real não pode existir ⇒ aborta.
	noIssuer := tnBaseConfig()
	noIssuer.IssuerID = ""
	if _, err := Bootstrap(ctx, noIssuer, io.Discard); !errors.Is(err, ErrNoIssuerID) {
		t.Fatalf("sem IssuerID devia abortar com ErrNoIssuerID, veio: %v", err)
	}

	// Sem humanos autorizados: a autoridade não teria quem autenticar na raiz da cadeia
	// de delegação ⇒ aborta.
	noHumans := tnBaseConfig()
	noHumans.Humans = nil
	if _, err := Bootstrap(ctx, noHumans, io.Discard); !errors.Is(err, ErrNoHumans) {
		t.Fatalf("sem humanos devia abortar com ErrNoHumans, veio: %v", err)
	}

	// Sanidade: a config base (com identidade completa) NÃO aborta.
	ok, err := Bootstrap(ctx, tnBaseConfig(), io.Discard)
	if err != nil {
		t.Fatalf("config base valida devia compor, veio: %v", err)
	}
	if ok.IdentityMode != IdentityModeReal {
		t.Fatalf("modo de identidade = %q, quero %q (declarado no arranque)", ok.IdentityMode, IdentityModeReal)
	}
	ok.Close()
}

// TestNodeComposesFourEyesGate prova que o ramo de composição do FourEyesGate (AOS-162)
// está MESMO ligado no nó quando há aprovadores em config — fechando a lacuna de cobertura
// (o wiring NewMemApproverRegistry + registry.Register + NewFourEyesGate + node.FourEyes).
// Não-vacuoso: o gate autoriza uma aprovação assinada pelo aprovador PINADO e NEGA um
// aprovador desconhecido — logo o registo foi de facto populado com a pubkey certa (não é
// um stub permissivo), e um erro de wiring (gate não composto, ou sem os aprovadores)
// seria apanhado.
func TestNodeComposesFourEyesGate(t *testing.T) {
	ctx := context.Background()

	const approver = "human:approver-bob"
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// Sanidade: sem aprovadores o gate NÃO é composto (o ramo é gated).
	baseNode, err := Bootstrap(ctx, tnBaseConfig(), io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (base): %v", err)
	}
	if baseNode.FourEyes != nil {
		t.Fatal("sem cfg.Approvers o FourEyesGate NAO devia ser composto (node.FourEyes devia ser nil)")
	}
	baseNode.Close()

	// Com >= 1 aprovador: o gate é composto e o aprovador fica pinado.
	cfg := tnBaseConfig()
	cfg.Approvers = []ApproverConfig{{
		Principal: approver,
		PubKey:    pub,
		Authority: []string{"approve:" + risk.ClassGray.String()},
	}}
	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (com aprovador): %v", err)
	}
	defer node.Close()

	if node.FourEyes == nil {
		t.Fatal("com cfg.Approvers o FourEyesGate devia estar composto (node.FourEyes != nil)")
	}

	// Pedido REVERSÍVEL (1 aprovação basta) da classe gray ⇒ exige approve:gray, que o
	// aprovador pinado detém.
	req := integration.FourEyesRequest{
		RequestID:           "req-4eyes",
		Preview:             []byte("efeito exibido ao humano"),
		RiskClass:           risk.ClassGray,
		DualControlRequired: false,
	}
	challenge := make([]byte, 32)
	if _, err := rand.Read(challenge); err != nil {
		t.Fatalf("rand challenge: %v", err)
	}

	// (i) Aprovação assinada pelo aprovador PINADO ⇒ autorizada. Prova que o registo tem a
	// pubkey certa (o wiring registry.Register correu com o ApproverConfig).
	leg := integration.SignFourEyesLeg(priv, req, approver, "sess-1", "cred-1", challenge, nil)
	dec, err := node.FourEyes.Authorize(ctx, req, leg)
	if err != nil {
		t.Fatalf("aprovacao do aprovador pinado devia autorizar, veio erro: %v (reason=%q)", err, dec.Reason)
	}
	if !dec.Authorized {
		t.Fatalf("decisao devia ser Authorized=true para o aprovador pinado, veio %+v", dec)
	}

	// (ii) Aprovador DESCONHECIDO (não pinado) ⇒ negado fail-closed. Prova que o gate não é
	// um stub permissivo — só reconhece quem foi registado a partir da config.
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (outro): %v", err)
	}
	challenge2 := make([]byte, 32)
	if _, err := rand.Read(challenge2); err != nil {
		t.Fatalf("rand challenge2: %v", err)
	}
	req2 := req
	req2.RequestID = "req-4eyes-rogue"
	rogueLeg := integration.SignFourEyesLeg(otherPriv, req2, "human:rogue", "sess-9", "cred-9", challenge2, nil)
	if _, err := node.FourEyes.Authorize(ctx, req2, rogueLeg); !errors.Is(err, integration.ErrUnknownApprover) {
		t.Fatalf("aprovador desconhecido devia dar ErrUnknownApprover (fail-closed), veio: %v", err)
	}
}

// TestNodeTrustAnchorOnlyHasNoAuthorityInProcess prova o modo ENDURECIDO (AC3b): quando a
// config injecta APENAS o trust anchor (IssuerID+pubkey) de uma autoridade EXTERNA, o nó
// compõe o verifier REAL mas NÃO tem autoridade in-process (node.Authority == nil) —
// nenhuma chave de assinatura vive no processo, fechando de facto a não-forjabilidade
// relativa ao nó. Não-vacuoso: o verifier aceita tokens da autoridade externa e nega o
// anónimo, provando que é o anchor real (não um stub nem o default sem anchors).
func TestNodeTrustAnchorOnlyHasNoAuthorityInProcess(t *testing.T) {
	ctx := context.Background()

	// Autoridade EXTERNA (simula o out-of-process): detém a chave; o nó só verá a pubkey.
	extAuth, err := integration.NewIssuerAuthority(integration.AuthorityConfig{
		IssuerID:      "iss:external-authority",
		Classes:       tnBaseConfig().IssuerClasses,
		Directory:     integration.NewAllowlistDirectory(tnHuman),
		IssuerOptions: []identity.IssuerOption{identity.WithIssuerClock(tnClock())},
	})
	if err != nil {
		t.Fatalf("NewIssuerAuthority (externa): %v", err)
	}
	issuerID, pub := extAuth.TrustAnchor()

	// Config ENDURECIDA: só o trust anchor. SEM Humans, SEM signing key.
	cfg := Config{
		IssuerID:      issuerID,
		IssuerPubKey:  pub,
		IssuerClasses: tnBaseConfig().IssuerClasses,
		VerifierClock: tnClock(),
	}
	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (endurecido): %v", err)
	}
	defer node.Close()

	if node.Authority != nil {
		t.Fatal("no modo endurecido (trust-anchor-only) NAO devia haver autoridade in-process (node.Authority devia ser nil)")
	}
	if node.IdentityMode != IdentityModeRealHardened {
		t.Fatalf("modo de identidade = %q, quero %q (trust-anchor-only)", node.IdentityMode, IdentityModeRealHardened)
	}

	// Prova não-vacuosa: o verifier REAL do anchor injectado aceita um token da autoridade
	// externa (passa a barreira de identidade) e nega o anónimo.
	if err := node.Runtime.Register("tool", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	rm := node.Runtime.Monitor()

	tok, err := extAuth.MintForHuman(ctx, tnHuman, tnAgent, tnClass, []string{tnCap})
	if err != nil {
		t.Fatalf("MintForHuman (externa): %v", err)
	}
	decOK, err := rm.Mediate(ctx, tnCall(tok.Compact))
	if err != nil {
		t.Fatalf("Mediate (token externo legitimo): %v", err)
	}
	if decOK.DeniedBy == "identity" {
		t.Fatalf("token da autoridade externa NEGADO em identity — o anchor injectado devia te-lo aceite (DeniedBy=%q, reason=%q)", decOK.DeniedBy, decOK.Reason)
	}

	decAnon, err := rm.Mediate(ctx, tnCall(""))
	if err != nil {
		t.Fatalf("Mediate (anonima): %v", err)
	}
	if decAnon.Effect != referencemonitor.EffectDeny || decAnon.DeniedBy != "identity" {
		t.Fatalf("chamada anonima devia ser NEGADA em identity, veio effect=%q DeniedBy=%q", decAnon.Effect, decAnon.DeniedBy)
	}
}

// TestBootstrapHardenedRejectsSigningKey prova o fail-closed do modo endurecido: pedir
// trust-anchor-only (IssuerPubKey) E fornecer uma IssuerSigningKey é uma contradição
// (uma chave de assinatura no processo derrotaria a propriedade) ⇒ aborta.
func TestBootstrapHardenedRejectsSigningKey(t *testing.T) {
	ctx := context.Background()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	cfg := Config{
		IssuerID:         "iss:external-authority",
		IssuerPubKey:     pub,
		IssuerSigningKey: priv, // <-- contradição: chave de assinatura no modo trust-anchor-only
		IssuerClasses:    tnBaseConfig().IssuerClasses,
	}
	if _, err := Bootstrap(ctx, cfg, io.Discard); !errors.Is(err, ErrConflictingIssuerKey) {
		t.Fatalf("modo endurecido com signing key devia abortar com ErrConflictingIssuerKey, veio: %v", err)
	}
}

// tnCall constrói uma [referencemonitor.Call] de teste com a credencial dada (o token
// NHI compacto, ou "" para anónima). A capability/recurso são triviais — o que o teste
// distingue é QUEM nega (identidade real vs a jusante).
func tnCall(credential string) referencemonitor.Call {
	return referencemonitor.Call{
		RunID:      "run-verify",
		StepID:     "s1",
		ToolID:     "tool",
		Capability: tnCap,
		Credential: credential,
	}
}
