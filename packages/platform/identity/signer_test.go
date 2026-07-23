package identity

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha512"
	"errors"
	"io"
	"testing"
)

// ---------------------------------------------------------------------------
// AOS-175: custódia de chave EXTERNA via crypto.Signer.
//
// O Issuer assina ATRAVÉS de um crypto.Signer. Estes testes provam que:
//   - a via legada (ed25519.PrivateKey crua) continua a mintar e a verificar (compat);
//   - um crypto.Signer EXTERNO que NUNCA expõe os bytes da chave minta um token que o
//     verifier ACEITA (não-forjabilidade: o Issuer nunca toca a chave privada);
//   - fail-closed para signer nil / Public() não-ed25519 / assinatura de tamanho errado.
// ---------------------------------------------------------------------------

// externalSigner é um DOUBLE de custódia externa (a modelar um HSM/KMS): detém a chave
// ed25519 num campo NÃO-EXPORTADO e implementa crypto.Signer expondo APENAS Public() e
// Sign(). Não há nenhum método/acesso que devolva os bytes da chave privada — o
// compilador garante que o Issuer nunca os pode obter. Regista se foi realmente usado.
type externalSigner struct {
	priv   ed25519.PrivateKey // encapsulada; nunca devolvida
	signed int
}

func newExternalSigner(t *testing.T) *externalSigner {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return &externalSigner{priv: priv}
}

func (s *externalSigner) Public() crypto.PublicKey { return s.priv.Public() }

func (s *externalSigner) Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	// ed25519 puro: opts tem de ser crypto.Hash(0) (assina a mensagem, sem pré-hash).
	if opts.HashFunc() != crypto.Hash(0) {
		return nil, errors.New("externalSigner: espera crypto.Hash(0) para ed25519")
	}
	s.signed++
	return ed25519.Sign(s.priv, digest), nil
}

// TestIssuer_LegacyKeyPath_CompatIntact — a via NewIssuer(priv) continua a mintar e o
// verifier aceita (compatibilidade preservada).
func TestIssuer_LegacyKeyPath_CompatIntact(t *testing.T) {
	t.Parallel()
	iss, pub := newIssuer(t, researcherClasses(), nil)

	tok, err := iss.Issue(context.Background(), IssueRequest{
		UserID: "human:alice", AgentID: "agt-1", AgentClass: "researcher",
		UserAuthority: []string{"cap:http.get"},
	})
	if err != nil {
		t.Fatalf("Issue (via legada): %v", err)
	}

	v := NewVerifier(WithTrustedIssuer(testIssuerID, pub), WithVerifierClock(fixedClock(baseTime)))
	if _, err := v.Verify(context.Background(), tok.Compact); err != nil {
		t.Fatalf("Verify (via legada) devia aceitar: %v", err)
	}
}

// TestIssuer_ExternalSignerPath_VerifierAccepts — o cerne de AOS-175: um Issuer
// construído com um crypto.Signer EXTERNO (que nunca expõe a chave) minta um token e o
// verifier ACEITA. Prova que a assinatura via abstracão é válida e que o Issuer nunca
// precisou dos bytes da chave.
func TestIssuer_ExternalSignerPath_VerifierAccepts(t *testing.T) {
	t.Parallel()
	signer := newExternalSigner(t)

	iss, err := NewIssuerWithSigner(testIssuerID, signer, researcherClasses(),
		WithIssuerClock(fixedClock(baseTime)))
	if err != nil {
		t.Fatalf("NewIssuerWithSigner: %v", err)
	}

	// PublicKey deriva do signer.Public() e é a que o verifier confia como trust anchor.
	pub := iss.PublicKey()
	if !pub.Equal(signer.priv.Public().(ed25519.PublicKey)) {
		t.Fatal("PublicKey do Issuer nao corresponde a do signer externo")
	}

	tok, err := iss.Issue(context.Background(), IssueRequest{
		UserID: "human:bob", AgentID: "agt-ext", AgentClass: "researcher",
		UserAuthority: []string{"cap:http.get", "cap:fs.read"},
	})
	if err != nil {
		t.Fatalf("Issue (via signer externo): %v", err)
	}
	if signer.signed == 0 {
		t.Fatal("o signer externo nunca foi chamado — o Issuer nao assinou atraves da abstracao")
	}

	v := NewVerifier(WithTrustedIssuer(testIssuerID, pub), WithVerifierClock(fixedClock(baseTime)))
	p, err := v.Verify(context.Background(), tok.Compact)
	if err != nil {
		t.Fatalf("Verify (assinatura via abstracao) devia aceitar: %v", err)
	}
	if got, _ := p.HumanPrincipal(); got != "human:bob" {
		t.Fatalf("human principal = %q, esperava human:bob", got)
	}

	// Não-forjabilidade / defesa em profundidade: a assinatura tem MESMO de casar com a
	// pubkey do signer; uma outra chave rejeita (a assinatura não é vácua).
	_, otherPriv := testKeys(t)
	vBad := NewVerifier(WithTrustedIssuer(testIssuerID, otherPriv.Public().(ed25519.PublicKey)),
		WithVerifierClock(fixedClock(baseTime)))
	if _, err := vBad.Verify(context.Background(), tok.Compact); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("chave errada devia dar ErrSignatureInvalid, obtive %v", err)
	}
}

// TestIssueChild_ExternalSigner — a emissão de filho (que também assina e re-verifica o
// pai com a pubkey do signer) funciona pela via de custódia externa.
func TestIssueChild_ExternalSigner(t *testing.T) {
	t.Parallel()
	classes := map[string]ClassPolicy{
		"researcher": {TTL: 5 * baseTTL, Scope: []string{"cap:http.get", "cap:fs.read"}},
		"tool":       {TTL: 5 * baseTTL, Scope: []string{"cap:http.get"}},
	}
	signer := newExternalSigner(t)
	iss, err := NewIssuerWithSigner(testIssuerID, signer, classes, WithIssuerClock(fixedClock(baseTime)))
	if err != nil {
		t.Fatalf("NewIssuerWithSigner: %v", err)
	}

	parent, err := iss.Issue(context.Background(), IssueRequest{
		UserID: "human:carol", AgentID: "agt-parent", AgentClass: "researcher",
		UserAuthority: []string{"cap:http.get", "cap:fs.read"},
	})
	if err != nil {
		t.Fatalf("Issue pai: %v", err)
	}
	child, err := iss.IssueChild(context.Background(), parent.Compact, ChildRequest{
		AgentID: "agt-child", AgentClass: "tool", Authority: []string{"cap:http.get"},
	})
	if err != nil {
		t.Fatalf("IssueChild via signer externo: %v", err)
	}

	v := NewVerifier(WithTrustedIssuer(testIssuerID, iss.PublicKey()), WithVerifierClock(fixedClock(baseTime)))
	if _, err := v.Verify(context.Background(), child.Compact); err != nil {
		t.Fatalf("Verify filho devia aceitar: %v", err)
	}
}

// TestNewIssuerWithSigner_FailClosed — signer nil, Public() não-ed25519 e iss vazio.
func TestNewIssuerWithSigner_FailClosed(t *testing.T) {
	t.Parallel()

	if _, err := NewIssuerWithSigner(testIssuerID, nil, nil); !errors.Is(err, ErrInvalidSigner) {
		t.Fatalf("signer nil devia dar ErrInvalidSigner, obtive %v", err)
	}
	if _, err := NewIssuerWithSigner("", newExternalSigner(t), nil); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("iss vazio devia dar ErrInvalidRequest, obtive %v", err)
	}
	if _, err := NewIssuerWithSigner(testIssuerID, notEd25519Signer{}, nil); !errors.Is(err, ErrInvalidSigner) {
		t.Fatalf("Public() nao-ed25519 devia dar ErrInvalidSigner, obtive %v", err)
	}
}

// TestSignToken_WrongSignatureSize_FailClosed — um signer cuja Public() é ed25519 válida
// (passa a construção) mas cujo Sign devolve uma assinatura de tamanho ERRADO faz a
// emissão falhar fail-closed com ErrInvalidSigner (nenhum bearer inválido é produzido).
func TestSignToken_WrongSignatureSize_FailClosed(t *testing.T) {
	t.Parallel()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	iss, err := NewIssuerWithSigner(testIssuerID, &shortSigSigner{pub: pub}, researcherClasses(),
		WithIssuerClock(fixedClock(baseTime)))
	if err != nil {
		t.Fatalf("NewIssuerWithSigner (pubkey valida): %v", err)
	}
	_, err = iss.Issue(context.Background(), IssueRequest{
		UserID: "human:dave", AgentID: "agt-x", AgentClass: "researcher",
		UserAuthority: []string{"cap:http.get"},
	})
	if !errors.Is(err, ErrInvalidSigner) {
		t.Fatalf("assinatura de tamanho errado devia dar ErrInvalidSigner, obtive %v", err)
	}
}

// TestSignToken_WrongKeyHandle_FailClosedAtOrigin — um adaptador HSM/KMS com o HANDLE de
// chave ERRADO: Public() reporta uma pubkey, mas Sign assina com OUTRA chave. A assinatura
// tem 64 bytes (passa o gate de tamanho) mas NÃO valida contra a pubkey reportada. O Issuer
// tem de a recusar na ORIGEM (ErrInvalidSigner), nunca emitindo um bearer que só o verifier
// downstream rejeitaria — defesa-em-profundidade no ponto exacto que AOS-175 endurece.
func TestSignToken_WrongKeyHandle_FailClosedAtOrigin(t *testing.T) {
	t.Parallel()
	reportedPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey (reportada): %v", err)
	}
	_, otherPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey (handle errado): %v", err)
	}
	iss, err := NewIssuerWithSigner(testIssuerID,
		&wrongHandleSigner{pub: reportedPub, signPriv: otherPriv}, researcherClasses(),
		WithIssuerClock(fixedClock(baseTime)))
	if err != nil {
		t.Fatalf("NewIssuerWithSigner (pubkey valida): %v", err)
	}
	_, err = iss.Issue(context.Background(), IssueRequest{
		UserID: "human:erin", AgentID: "agt-y", AgentClass: "researcher",
		UserAuthority: []string{"cap:http.get"},
	})
	if !errors.Is(err, ErrInvalidSigner) {
		t.Fatalf("assinatura com handle de chave errado devia dar ErrInvalidSigner na origem, obtive %v", err)
	}
}

// TestSignToken_Ed25519ph_FailClosedAtOrigin — responde directamente ao eixo de AOS-175:
// um adaptador que IGNORA opts=crypto.Hash(0) e assina em modo Ed25519ph (pré-hash SHA-512)
// produz 64 bytes válidos EM Ed25519ph mas que o ed25519 PURO do verifier rejeita. Tem de
// ser recusado na EMISSÃO — nenhum bearer inseguro/inválido é alguma vez devolvido.
func TestSignToken_Ed25519ph_FailClosedAtOrigin(t *testing.T) {
	t.Parallel()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	iss, err := NewIssuerWithSigner(testIssuerID, &prehashSigner{priv: priv}, researcherClasses(),
		WithIssuerClock(fixedClock(baseTime)))
	if err != nil {
		t.Fatalf("NewIssuerWithSigner: %v", err)
	}
	_, err = iss.Issue(context.Background(), IssueRequest{
		UserID: "human:frank", AgentID: "agt-ph", AgentClass: "researcher",
		UserAuthority: []string{"cap:http.get"},
	})
	if !errors.Is(err, ErrInvalidSigner) {
		t.Fatalf("assinatura Ed25519ph (pre-hash) devia dar ErrInvalidSigner na origem, obtive %v", err)
	}
}

// TestNewIssuerWithSigner_MalformedRawKey_NoPanic — uma ed25519.PrivateKey MALFORMADA
// (len<32) passada DIRECTAMENTE a NewIssuerWithSigner (contornando o guard de tamanho de
// NewIssuer) faria signer.Public() entrar em pânico (copy(pub, priv[32:]) — slice bounds).
// Tem de devolver o sentinela fail-closed ErrInvalidSigner, NUNCA derrubar o processo.
func TestNewIssuerWithSigner_MalformedRawKey_NoPanic(t *testing.T) {
	t.Parallel()
	malformed := ed25519.PrivateKey([]byte{0x01, 0x02, 0x03}) // len 3 << 32
	if _, err := NewIssuerWithSigner(testIssuerID, malformed, researcherClasses()); !errors.Is(err, ErrInvalidSigner) {
		t.Fatalf("chave crua malformada devia dar ErrInvalidSigner (sem panic), obtive %v", err)
	}
}

// wrongHandleSigner tem uma Public() ed25519 VÁLIDA (passa a construção) mas Sign assina
// com uma chave DIFERENTE — 64 bytes que não validam contra a pubkey reportada.
type wrongHandleSigner struct {
	pub      ed25519.PublicKey
	signPriv ed25519.PrivateKey
}

func (s *wrongHandleSigner) Public() crypto.PublicKey { return s.pub }
func (s *wrongHandleSigner) Sign(_ io.Reader, digest []byte, _ crypto.SignerOpts) ([]byte, error) {
	return ed25519.Sign(s.signPriv, digest), nil
}

// prehashSigner assina em Ed25519ph (pré-hash SHA-512), ignorando opts=crypto.Hash(0):
// produz 64 bytes que o ed25519 puro do verifier NÃO aceita.
type prehashSigner struct{ priv ed25519.PrivateKey }

func (s *prehashSigner) Public() crypto.PublicKey { return s.priv.Public() }
func (s *prehashSigner) Sign(_ io.Reader, msg []byte, _ crypto.SignerOpts) ([]byte, error) {
	d := sha512.Sum512(msg)
	return s.priv.Sign(nil, d[:], crypto.SHA512)
}

// baseTTL é um TTL curto de conveniência para os testes deste ficheiro.
const baseTTL = 60_000_000_000 // 1m em ns

// notEd25519Signer implementa crypto.Signer mas Public() devolve um tipo não-ed25519.
type notEd25519Signer struct{}

func (notEd25519Signer) Public() crypto.PublicKey { return "not-a-key" }
func (notEd25519Signer) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("nao devia ser chamado")
}

// shortSigSigner tem uma Public() ed25519 VÁLIDA (passa a construção) mas Sign devolve
// uma assinatura demasiado curta — o Issuer tem de recusar fail-closed na origem.
type shortSigSigner struct{ pub ed25519.PublicKey }

func (s *shortSigSigner) Public() crypto.PublicKey { return s.pub }
func (s *shortSigSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return []byte{0x01, 0x02, 0x03}, nil // != ed25519.SignatureSize (64)
}
