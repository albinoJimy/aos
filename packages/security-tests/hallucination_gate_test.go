package securitytests

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/messaging"
)

// ===========================================================================
// CENÁRIO 6 — HALLUCINATION GATE (AOS-073)
//
// Uma mensagem inter-agente só é accionável se a sua ORIGEM (assinatura ed25519 vs
// chave PINADA), a sua AUTORIDADE (autoritativa cobre a acção; clamada ⊆ autoritativa)
// e a sua REFERÊNCIA (existe + hash autêntico casa o assinado) forem criptograficamente
// comprovadas, e se for FRESCA (anti-replay). Uma origem forjada, autoridade não coberta,
// referência inautêntica ou replay são REJEITADOS fail-closed E SELADOS no audit WORM
// tamper-evident. ORQUESTRA messaging.Verifier real com fakes de identidade/referência e
// um audit.Store real; não reimplementa o gate. As chaves ed25519 são SINTÉTICAS,
// derivadas de seeds deterministas no teste — sem segredos reais.
// ===========================================================================

// halTime é o relógio determinista do verifier e o instante de emissão das mensagens
// (age 0 ⇒ dentro da janela de frescura). Nunca entra numa decisão criptográfica.
var halTime = time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)

// halSeed devolve um seed ed25519 de 32 bytes determinista (chave SINTÉTICA de teste).
func halSeed(b byte) []byte {
	s := make([]byte, ed25519.SeedSize)
	for i := range s {
		s[i] = b
	}
	return s
}

// halNonce devolve um nonce determinista de 16 bytes (>= nonceMinLen do módulo).
func halNonce(b byte) []byte { return bytes.Repeat([]byte{b}, 16) }

// halVault modela o custodiante server-side (broker/Vault, AOS-070): guarda a chave
// PRIVADA por NHI e assina do seu lado, devolvendo só a assinatura. Satisfaz
// messaging.Signer. A chave privada nunca sai — sintética e efémera.
type halVault struct{ priv map[string]ed25519.PrivateKey }

func newHalVault() *halVault { return &halVault{priv: map[string]ed25519.PrivateKey{}} }

func (v *halVault) provision(nhi string, seed byte) ed25519.PublicKey {
	p := ed25519.NewKeyFromSeed(halSeed(seed))
	v.priv[nhi] = p
	return p.Public().(ed25519.PublicKey)
}

func (v *halVault) Sign(_ context.Context, nhi string, message []byte) ([]byte, error) {
	p, ok := v.priv[nhi]
	if !ok {
		return nil, errors.New("halVault: sem material para a NHI")
	}
	return ed25519.Sign(p, message), nil
}

// halRegistry satisfaz messaging.NHIRegistry: a chave pública PINADA + a autoridade
// AUTORITATIVA de cada NHI.
type halRegistry struct {
	entries map[string]struct {
		pub  ed25519.PublicKey
		auth []string
	}
}

func newHalRegistry() *halRegistry {
	return &halRegistry{entries: map[string]struct {
		pub  ed25519.PublicKey
		auth []string
	}{}}
}

func (r *halRegistry) put(nhi string, pub ed25519.PublicKey, authority ...string) {
	r.entries[nhi] = struct {
		pub  ed25519.PublicKey
		auth []string
	}{pub: pub, auth: authority}
}

func (r *halRegistry) Lookup(_ context.Context, nhi string) (ed25519.PublicKey, []string, bool, error) {
	e, ok := r.entries[nhi]
	if !ok {
		return nil, nil, false, nil
	}
	return e.pub, e.auth, true, nil
}

// halRefs satisfaz messaging.ReferenceResolver: id → hash de conteúdo autêntico.
type halRefs struct{ items map[string][]byte }

func newHalRefs() *halRefs { return &halRefs{items: map[string][]byte{}} }

func (r *halRefs) put(id string, content []byte) []byte {
	h := sha256.Sum256(content)
	r.items[id] = h[:]
	return h[:]
}

func (r *halRefs) Resolve(_ context.Context, id string) ([]byte, bool, error) {
	h, ok := r.items[id]
	if !ok {
		return nil, false, nil
	}
	return h, true, nil
}

// newHalVerifier constrói um Verifier sobre os fakes dados e um audit.Store real, com
// relógio determinista.
func newHalVerifier(t *testing.T, reg messaging.NHIRegistry, refs messaging.ReferenceResolver, store audit.Store) *messaging.Verifier {
	t.Helper()
	v, err := messaging.NewVerifier(reg, refs, store, messaging.WithVerifierClock(func() time.Time { return halTime }))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

// halSign assina uma mensagem com o vault (a chave da sua Origin). Preenche IssuedAt no
// instante determinista se ausente.
func halSign(t *testing.T, vault messaging.Signer, msg messaging.Message) messaging.Message {
	t.Helper()
	if msg.IssuedAt.IsZero() {
		msg.IssuedAt = halTime
	}
	signed, err := messaging.SignMessage(context.Background(), vault, msg)
	if err != nil {
		t.Fatalf("SignMessage: %v", err)
	}
	return signed
}

const halSender = "nhi-sender-1"

// TestHallucinationGate_LegitMessagePasses prova a não-tautologia: uma mensagem
// genuinamente assinada (chave pinada), com autoridade a cobrir a acção e referência
// autêntica, PASSA — o gate não recusa tudo cegamente.
func TestHallucinationGate_LegitMessagePasses(t *testing.T) {
	t.Parallel()
	vault := newHalVault()
	reg := newHalRegistry()
	refs := newHalRefs()
	store := audit.NewMemStore()

	pub := vault.provision(halSender, 0x11)
	reg.put(halSender, pub, "act:summarize", "act:report")
	refHash := refs.put("ref-1", []byte("sub-resultado autentico"))

	msg := messaging.Message{
		Origin: halSender, Authority: []string{"act:summarize"}, Action: "act:summarize",
		Nonce: halNonce(0x01), IssuedAt: halTime,
		Reference: messaging.Reference{ID: "ref-1", Hash: refHash},
		Payload:   []byte("faz o resumo"),
	}
	v := newHalVerifier(t, reg, refs, store)
	vm, err := v.Verify(context.Background(), halSign(t, vault, msg))
	if err != nil {
		t.Fatalf("mensagem legítima rejeitada: %v (gate seria tautológico)", err)
	}
	if vm.Origin != halSender || vm.Action != "act:summarize" {
		t.Fatalf("VerifiedMessage = {origin=%q action=%q}, quer {%q, act:summarize}", vm.Origin, vm.Action, halSender)
	}
}

// TestHallucinationGate_ForgedOrigin_BlockedAndSealed — a assinatura NÃO valida contra a
// chave PINADA da NHI clamada (foi feita por outra chave): ReasonForgedOrigin, selado na
// partição de QUARENTENA (origem ainda não autenticada).
func TestHallucinationGate_ForgedOrigin_BlockedAndSealed(t *testing.T) {
	t.Parallel()
	vault := newHalVault()
	reg := newHalRegistry()
	refs := newHalRefs()
	store := audit.NewMemStore()

	// O vault assina com a chave 0x11 do emissor; o registo PINA uma chave DIFERENTE
	// (0x22) para a mesma NHI — a assinatura genuína não valida contra a pinada.
	vault.provision(halSender, 0x11)
	attackerPub := ed25519.NewKeyFromSeed(halSeed(0x22)).Public().(ed25519.PublicKey)
	reg.put(halSender, attackerPub, "act:summarize")
	refHash := refs.put("ref-1", []byte("sub-resultado"))

	msg := messaging.Message{
		Origin: halSender, Authority: []string{"act:summarize"}, Action: "act:summarize",
		Nonce: halNonce(0x02), Reference: messaging.Reference{ID: "ref-1", Hash: refHash},
	}
	v := newHalVerifier(t, reg, refs, store)
	_, err := v.Verify(context.Background(), halSign(t, vault, msg))
	if !errors.Is(err, messaging.ErrForgedOrigin) {
		t.Fatalf("origem forjada = %v, quer ErrForgedOrigin", err)
	}
	// Rejeição pré-autenticação → partição de quarentena.
	recs := verifyWORM(t, store, "msg-verify-unauth")
	if len(recs) == 0 {
		t.Fatal("rejeição de origem forjada não selada no WORM")
	}
	ledger := audit.NewMemStore()
	attestBlock(t, ledger, "hallucination_forged_origin", "messaging.verify", messaging.ReasonForgedOrigin)
	verifyWORM(t, ledger, suiteLedgerPartition)
}

// TestHallucinationGate_AuthorityNotCovered_BlockedAndSealed — assinatura válida, mas a
// autoridade AUTORITATIVA do emissor NÃO cobre a acção pedida: ReasonAuthorityNotCovered,
// selado na partição do emissor REAL (já autenticado).
func TestHallucinationGate_AuthorityNotCovered_BlockedAndSealed(t *testing.T) {
	t.Parallel()
	vault := newHalVault()
	reg := newHalRegistry()
	refs := newHalRefs()
	store := audit.NewMemStore()

	pub := vault.provision(halSender, 0x11)
	reg.put(halSender, pub, "act:summarize") // autoritativa NÃO inclui act:transfer
	refHash := refs.put("ref-1", []byte("sub-resultado"))

	msg := messaging.Message{
		Origin: halSender, Authority: []string{"act:summarize"}, Action: "act:transfer.funds",
		Nonce: halNonce(0x03), Reference: messaging.Reference{ID: "ref-1", Hash: refHash},
	}
	v := newHalVerifier(t, reg, refs, store)
	_, err := v.Verify(context.Background(), halSign(t, vault, msg))
	if !errors.Is(err, messaging.ErrAuthorityNotCovered) {
		t.Fatalf("autoridade não coberta = %v, quer ErrAuthorityNotCovered", err)
	}
	recs := verifyWORM(t, store, "msg-verify:"+halSender)
	if len(recs) == 0 {
		t.Fatal("rejeição de autoridade não selada no WORM")
	}
	ledger := audit.NewMemStore()
	attestBlock(t, ledger, "hallucination_authority", "messaging.verify", messaging.ReasonAuthorityNotCovered)
	verifyWORM(t, ledger, suiteLedgerPartition)
}

// TestHallucinationGate_ReferenceInauthentic_BlockedAndSealed — assinatura válida,
// autoridade cobre a acção, mas o hash da referência coberto pela assinatura DIVERGE do
// hash autêntico resolvido: ReasonReferenceInauthentic.
func TestHallucinationGate_ReferenceInauthentic_BlockedAndSealed(t *testing.T) {
	t.Parallel()
	vault := newHalVault()
	reg := newHalRegistry()
	refs := newHalRefs()
	store := audit.NewMemStore()

	pub := vault.provision(halSender, 0x11)
	reg.put(halSender, pub, "act:summarize")
	refs.put("ref-1", []byte("conteudo autentico")) // hash autêntico A
	forgedHash := sha256.Sum256([]byte("conteudo adulterado"))

	msg := messaging.Message{
		Origin: halSender, Authority: []string{"act:summarize"}, Action: "act:summarize",
		Nonce: halNonce(0x04), Reference: messaging.Reference{ID: "ref-1", Hash: forgedHash[:]},
	}
	v := newHalVerifier(t, reg, refs, store)
	_, err := v.Verify(context.Background(), halSign(t, vault, msg))
	if !errors.Is(err, messaging.ErrReferenceInauthentic) {
		t.Fatalf("referência inautêntica = %v, quer ErrReferenceInauthentic", err)
	}
	recs := verifyWORM(t, store, "msg-verify:"+halSender)
	if len(recs) == 0 {
		t.Fatal("rejeição de referência não selada no WORM")
	}
	ledger := audit.NewMemStore()
	attestBlock(t, ledger, "hallucination_reference", "messaging.verify", messaging.ReasonReferenceInauthentic)
	verifyWORM(t, ledger, suiteLedgerPartition)
}

// TestHallucinationGate_Replay_Blocked — uma mensagem legítima re-apresentada (mesmo par
// Origin+Nonce) é rejeitada como replay: a primeira passa, a segunda dá ErrReplayedNonce.
func TestHallucinationGate_Replay_Blocked(t *testing.T) {
	t.Parallel()
	vault := newHalVault()
	reg := newHalRegistry()
	refs := newHalRefs()
	store := audit.NewMemStore()

	pub := vault.provision(halSender, 0x11)
	reg.put(halSender, pub, "act:summarize")
	refHash := refs.put("ref-1", []byte("sub-resultado"))

	signed := halSign(t, vault, messaging.Message{
		Origin: halSender, Authority: []string{"act:summarize"}, Action: "act:summarize",
		Nonce: halNonce(0x05), Reference: messaging.Reference{ID: "ref-1", Hash: refHash},
	})
	v := newHalVerifier(t, reg, refs, store)
	if _, err := v.Verify(context.Background(), signed); err != nil {
		t.Fatalf("primeira entrega legítima rejeitada: %v", err)
	}
	_, err := v.Verify(context.Background(), signed) // re-apresentação do MESMO nonce
	if !errors.Is(err, messaging.ErrReplayedNonce) {
		t.Fatalf("replay = %v, quer ErrReplayedNonce", err)
	}
	ledger := audit.NewMemStore()
	attestBlock(t, ledger, "hallucination_replay", "messaging.verify", messaging.ReasonReplayedNonce)
	verifyWORM(t, ledger, suiteLedgerPartition)
}

// TestMetaDetects_HallucinationGate_WhenForgedAccepted — com o controlo CONTORNADO (a
// chave PINADA passa a ser a que REALMENTE assinou), a MESMA mensagem "forjada" VERIFICA
// e passa: o ataque passa. Prova que a rejeição de origem forjada vem MESMO da
// reconciliação assinatura↔chave-pinada, não de uma asserção vácua.
func TestMetaDetects_HallucinationGate_WhenForgedAccepted(t *testing.T) {
	t.Parallel()
	vault := newHalVault()
	reg := newHalRegistry()
	refs := newHalRefs()
	store := audit.NewMemStore()

	// Controlo contornado: pina-se a chave 0x11 — a MESMA que assina — em vez da 0x22.
	pub := vault.provision(halSender, 0x11)
	reg.put(halSender, pub, "act:summarize")
	refHash := refs.put("ref-1", []byte("sub-resultado"))

	msg := messaging.Message{
		Origin: halSender, Authority: []string{"act:summarize"}, Action: "act:summarize",
		Nonce: halNonce(0x06), Reference: messaging.Reference{ID: "ref-1", Hash: refHash},
	}
	v := newHalVerifier(t, reg, refs, store)
	if _, err := v.Verify(context.Background(), halSign(t, vault, msg)); err != nil {
		t.Fatalf("com a chave correcta pinada, a mensagem devia PASSAR; got %v (deteção vácua?)", err)
	}
}
