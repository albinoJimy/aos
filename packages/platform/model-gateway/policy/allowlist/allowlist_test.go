package allowlist_test

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
)

// testSeedLabel deriva uma chave ED25519 determinista USADA SÓ NOS TESTES para
// assinar as policies CUSTOM deste ficheiro (não a policy embebida). NÃO tem relação
// com o trust anchor de produção: o [allowlist.LoadSignedPolicy] usado aqui verifica
// contra a pública que lhe é passada e NÃO pina fingerprint, pelo que uma chave de
// teste arbitrária serve. A policy EMBEBIDA é assinada por uma chave gerada offline
// com entropia real (crypto/rand), cuja pública está pinada por fingerprint em código
// — ver TestEmbeddedTrustAnchorPinned. A privada de produção nunca entra no repo.
const testSeedLabel = "aos-058-allowlist-TEST-ONLY-signing-key/v1"

// testKeys devolve uma chave privada de TESTE e a pública em base64 (para assinar
// policies custom nos testes; sem ligação ao anchor de produção).
func testKeys() (ed25519.PrivateKey, string) {
	seed := sha256.Sum256([]byte(testSeedLabel))
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := priv.Public().(ed25519.PublicKey)
	return priv, base64.StdEncoding.EncodeToString(pub)
}

// sign assina o digest canónico de policyJSON com priv e devolve a assinatura base64.
func sign(t *testing.T, priv ed25519.PrivateKey, policyJSON []byte) string {
	t.Helper()
	d, err := allowlist.Digest(policyJSON)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(d)))
}

// customPolicy é um documento de allowlist de teste (dois boards, regiões distintas).
const customPolicy = `{
  "version": "gw-allowlist-test/v1",
  "default": "deny",
  "rules": [
    {"id": "eu", "board": "board-eu", "models": ["gpt-4o", "text-embedding-3"], "regions": ["eu", "eu-west"]},
    {"id": "us", "board": "board-us", "models": ["gpt-4o"], "regions": ["us-east"]}
  ]
}`

// TestEmbeddedPolicyLoads — a allowlist EMBEBIDA carrega e verifica a assinatura
// embebida contra a chave pública embebida (o caminho de produção, fail-closed).
func TestEmbeddedPolicyLoads(t *testing.T) {
	p, err := allowlist.LoadPolicy()
	if err != nil {
		t.Fatalf("LoadPolicy embebida: %v", err)
	}
	if !strings.HasPrefix(p.Version(), "gw-allowlist/v1#") {
		t.Fatalf("versao inesperada: %q", p.Version())
	}
}

// TestEmbeddedTrustAnchorPinned — guard do TRUST ANCHOR (AOS058-Q1): a policy embebida
// só carrega porque a pública embebida bate com o fingerprint pinado em código. Se
// alguém trocar allowlist_policy.pub por outra chave (para reassinar uma policy
// adulterada), o fingerprint deixa de bater e LoadPolicy falha fail-closed — a raiz de
// confiança é a constante revista, não o ficheiro. Aqui provamos o caminho positivo
// (o material embebido é coerente e verifica sob o anchor pinado); o caminho de
// rejeição (pub errada ⇒ ErrTrustAnchorMismatch) é coberto em branco em
// trustanchor_internal_test.go.
func TestEmbeddedTrustAnchorPinned(t *testing.T) {
	p, err := allowlist.LoadPolicy()
	if err != nil {
		t.Fatalf("LoadPolicy embebida devia verificar sob o anchor pinado: %v", err)
	}
	if p == nil {
		t.Fatal("policy embebida nula")
	}
}

// TestEvaluate_DefaultDeny cobre allow (modelo permitido) e deny (fora da allowlist).
func TestEvaluate_DefaultDeny(t *testing.T) {
	priv, pub := testKeys()
	pol, err := allowlist.LoadSignedPolicy([]byte(customPolicy), sign(t, priv, []byte(customPolicy)), pub)
	if err != nil {
		t.Fatalf("LoadSignedPolicy: %v", err)
	}
	cases := []struct {
		name string
		in   allowlist.Input
		want allowlist.Effect
	}{
		{"eu-modelo-regiao-permitidos", allowlist.Input{Board: "board-eu", Model: "gpt-4o", Region: "eu"}, allowlist.EffectAllow},
		{"eu-regiao-alternativa-permitida", allowlist.Input{Board: "board-eu", Model: "gpt-4o", Region: "eu-west"}, allowlist.EffectAllow},
		{"us-permitido", allowlist.Input{Board: "board-us", Model: "gpt-4o", Region: "us-east"}, allowlist.EffectAllow},
		{"modelo-nao-listado", allowlist.Input{Board: "board-eu", Model: "claude-3", Region: "eu"}, allowlist.EffectDeny},
		{"regiao-fora-da-fronteira", allowlist.Input{Board: "board-eu", Model: "gpt-4o", Region: "us-east"}, allowlist.EffectDeny},
		{"board-desconhecido", allowlist.Input{Board: "board-zz", Model: "gpt-4o", Region: "eu"}, allowlist.EffectDeny},
		{"cross-board-modelo-de-outro-board", allowlist.Input{Board: "board-us", Model: "gpt-4o", Region: "eu"}, allowlist.EffectDeny},
		{"tudo-vazio", allowlist.Input{}, allowlist.EffectDeny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pol.Evaluate(tc.in); got != tc.want {
				t.Fatalf("Evaluate(%+v) = %q; quero %q", tc.in, got, tc.want)
			}
		})
	}
}

// wildcardRegionPolicy — uma policy cujo board usa regiao curinga ("*"): NÃO deve
// autorizar qualquer regiao (seria fail-open cross-border). O board/modelo curinga
// continuam a ser semantica legitima; a regiao curinga é o escape a fechar.
const wildcardRegionPolicy = `{
  "version": "gw-allowlist-wildcard/v1",
  "default": "deny",
  "rules": [
    {"id": "eu-any-region", "board": "board-eu", "models": ["gpt-4o"], "regions": ["*"]},
    {"id": "all-boards", "board": "*", "models": ["*"], "regions": ["*"]}
  ]
}`

// TestEvaluate_WildcardRegionNeverAllows (AOS058-Q4) — uma regra com regions:["*"]
// NÃO autoriza nenhuma regiao concreta nem a regiao vazia (fail-closed), alinhando
// Evaluate com AllowedRegions. Um board:"*"+models:["*"]+regions:["*"] tambem NÃO
// compõe allow-all: a fronteira de soberania nunca é curinga.
func TestEvaluate_WildcardRegionNeverAllows(t *testing.T) {
	priv, pub := testKeys()
	pol, err := allowlist.LoadSignedPolicy([]byte(wildcardRegionPolicy), sign(t, priv, []byte(wildcardRegionPolicy)), pub)
	if err != nil {
		t.Fatalf("LoadSignedPolicy: %v", err)
	}
	cases := []struct {
		name string
		in   allowlist.Input
	}{
		{"regiao-concreta-sob-curinga", allowlist.Input{Board: "board-eu", Model: "gpt-4o", Region: "us-east"}},
		{"regiao-eu-sob-curinga", allowlist.Input{Board: "board-eu", Model: "gpt-4o", Region: "eu"}},
		{"regiao-vazia", allowlist.Input{Board: "board-eu", Model: "gpt-4o", Region: ""}},
		{"allow-all-total-nao-passa", allowlist.Input{Board: "qualquer", Model: "qualquer", Region: "us-east"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pol.Evaluate(tc.in); got != allowlist.EffectDeny {
				t.Fatalf("Evaluate(%+v) = %q; regiao curinga nao devia autorizar (quero deny)", tc.in, got)
			}
		})
	}
	// E a fronteira derivada continua vazia (curinga nao expande soberania).
	if r := pol.AllowedRegions("board-eu", "gpt-4o"); len(r) != 0 {
		t.Fatalf("AllowedRegions sob curinga devia ser vazia; got %v", r)
	}
}

// TestSignatureInvalid_FailClosed — assinatura inválida (adulterada) FALHA fail-closed.
func TestSignatureInvalid_FailClosed(t *testing.T) {
	priv, pub := testKeys()
	good := sign(t, priv, []byte(customPolicy))

	// Adultera 1 byte da assinatura.
	rawSig, _ := base64.StdEncoding.DecodeString(good)
	rawSig[0] ^= 0xFF
	tampered := base64.StdEncoding.EncodeToString(rawSig)

	if _, err := allowlist.LoadSignedPolicy([]byte(customPolicy), tampered, pub); !errors.Is(err, allowlist.ErrSignatureInvalid) {
		t.Fatalf("assinatura adulterada devia falhar ErrSignatureInvalid; got %v", err)
	}
}

// TestPolicyTampered_SignatureBreaks — alterar o CONTEÚDO da policy depois de assinada
// muda o digest e invalida a assinatura (tamper-evident).
func TestPolicyTampered_SignatureBreaks(t *testing.T) {
	priv, pub := testKeys()
	sig := sign(t, priv, []byte(customPolicy))

	// Injecta um modelo extra (escalada) mantendo a assinatura antiga.
	tamperedPolicy := strings.Replace(customPolicy, `["gpt-4o", "text-embedding-3"]`, `["gpt-4o", "text-embedding-3", "claude-3"]`, 1)
	if tamperedPolicy == customPolicy {
		t.Fatal("substituicao de teste nao aplicou")
	}
	if _, err := allowlist.LoadSignedPolicy([]byte(tamperedPolicy), sig, pub); !errors.Is(err, allowlist.ErrSignatureInvalid) {
		t.Fatalf("policy adulterada devia falhar ErrSignatureInvalid; got %v", err)
	}
}

// TestWrongKey_FailClosed — assinatura de OUTRA chave não verifica contra a pública
// de confiança (fail-closed).
func TestWrongKey_FailClosed(t *testing.T) {
	_, pub := testKeys()
	// Outra chave (seed diferente).
	other := sha256.Sum256([]byte("chave-atacante"))
	otherPriv := ed25519.NewKeyFromSeed(other[:])
	badSig := sign(t, otherPriv, []byte(customPolicy))

	if _, err := allowlist.LoadSignedPolicy([]byte(customPolicy), badSig, pub); !errors.Is(err, allowlist.ErrSignatureInvalid) {
		t.Fatalf("assinatura de outra chave devia falhar; got %v", err)
	}
}

// TestDefaultNotDeny_FailClosed — uma policy com default != deny é recusada mesmo com
// assinatura válida (fail-open é inaceitável).
func TestDefaultNotDeny_FailClosed(t *testing.T) {
	_, pub := testKeys()
	failOpen := strings.Replace(customPolicy, `"default": "deny"`, `"default": "allow"`, 1)
	// Uma policy fail-open é recusada no PARSE (antes da verificação de assinatura):
	// não é sequer possível produzir um digest para ela, pelo que qualquer assinatura
	// serve — a recusa ErrPolicyMalformed precede a verificação criptográfica.
	if _, err := allowlist.LoadSignedPolicy([]byte(failOpen), base64.StdEncoding.EncodeToString(make([]byte, 64)), pub); !errors.Is(err, allowlist.ErrPolicyMalformed) {
		t.Fatalf("default=allow devia falhar ErrPolicyMalformed; got %v", err)
	}
}

// TestBadPubKey_FailClosed — chave pública malformada é recusada.
func TestBadPubKey_FailClosed(t *testing.T) {
	priv, _ := testKeys()
	sig := sign(t, priv, []byte(customPolicy))
	if _, err := allowlist.LoadSignedPolicy([]byte(customPolicy), sig, "nao-e-base64!!!"); !errors.Is(err, allowlist.ErrPubKeyInvalid) {
		t.Fatalf("pública malformada devia falhar ErrPubKeyInvalid; got %v", err)
	}
	if _, err := allowlist.LoadSignedPolicy([]byte(customPolicy), sig, base64.StdEncoding.EncodeToString([]byte("curta"))); !errors.Is(err, allowlist.ErrPubKeyInvalid) {
		t.Fatalf("pública de tamanho errado devia falhar ErrPubKeyInvalid; got %v", err)
	}
}

// TestAllowedRegions — a fronteira de soberania (regiões permitidas por board+modelo)
// que a guarda de failover consome. Wildcards e vazios não expandem a fronteira.
func TestAllowedRegions(t *testing.T) {
	priv, pub := testKeys()
	pol, err := allowlist.LoadSignedPolicy([]byte(customPolicy), sign(t, priv, []byte(customPolicy)), pub)
	if err != nil {
		t.Fatalf("LoadSignedPolicy: %v", err)
	}
	got := pol.AllowedRegions("board-eu", "gpt-4o")
	want := []string{"eu", "eu-west"}
	if len(got) != len(want) {
		t.Fatalf("AllowedRegions = %v; quero %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AllowedRegions[%d] = %q; quero %q", i, got[i], want[i])
		}
	}
	if r := pol.AllowedRegions("board-zz", "gpt-4o"); len(r) != 0 {
		t.Fatalf("board desconhecido devia ter fronteira vazia; got %v", r)
	}
}

// TestVersionStableAndDigest — a versão é "tag#digest12" e muda com o conteúdo.
func TestVersionStableAndDigest(t *testing.T) {
	priv, pub := testKeys()
	pol, _ := allowlist.LoadSignedPolicy([]byte(customPolicy), sign(t, priv, []byte(customPolicy)), pub)
	v1 := pol.Version()
	if !strings.HasPrefix(v1, "gw-allowlist-test/v1#") || len(v1) <= len("gw-allowlist-test/v1#") {
		t.Fatalf("versao malformada: %q", v1)
	}
	// Reordenar as regras/campos não muda o digest (canonicalização estável).
	reordered := `{
      "version": "gw-allowlist-test/v1",
      "default": "deny",
      "rules": [
        {"id": "us", "board": "board-us", "regions": ["us-east"], "models": ["gpt-4o"]},
        {"id": "eu", "board": "board-eu", "regions": ["eu-west", "eu"], "models": ["text-embedding-3", "gpt-4o"]}
      ]
    }`
	pol2, err := allowlist.LoadSignedPolicy([]byte(reordered), sign(t, priv, []byte(reordered)), pub)
	if err != nil {
		t.Fatalf("LoadSignedPolicy reordered: %v", err)
	}
	if pol2.Version() != v1 {
		t.Fatalf("digest devia ser estável sob reordenacao: %q != %q", pol2.Version(), v1)
	}
}
