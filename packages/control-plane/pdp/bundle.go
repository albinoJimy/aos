package pdp

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Nomes canónicos dos ficheiros do bundle no directório de políticas.
const (
	manifestFile    = "manifest.json"
	signatureFile   = "aos_authz.sig"
	trustAnchorFile = "trust_anchor.pub"
	signingKeyFile  = "signing.key" // PRIVADA — gitignored (*.key). Nunca committar.
	cedarExt        = ".cedar"
)

// signingDomain é o prefixo de domínio da mensagem assinada. Liga a assinatura
// a este esquema/versão e evita reutilização cross-protocolo.
const signingDomain = "aos.policy.bundle.v1"

// Manifest descreve o bundle: versão SemVer da política, hash canónico do
// conteúdo (.cedar) e instante de criação. É o objecto cujos campos são ligados
// pela assinatura (via [signingMessage]).
type Manifest struct {
	PolicyVersion string `json:"policy_version"`
	ContentHash   string `json:"content_hash"`
	CreatedAt     string `json:"created_at"`
}

// RawBundle é o bundle tal como reside no disco/memória, antes de compilado: os
// ficheiros de política (.cedar), o manifest e a assinatura ed25519 crua.
type RawBundle struct {
	// PolicyFiles mapeia nome-de-ficheiro → fonte Cedar. O nome participa no hash
	// canónico para que renomear um ficheiro invalide a assinatura.
	PolicyFiles map[string][]byte
	Manifest    Manifest
	Signature   []byte
}

// canonicalContentHash calcula um sha256 determinístico sobre os ficheiros de
// política. Canónico: ficheiros ordenados por nome; para cada um mistura
// nome\0 sha256(conteúdo)\0. Independente da ordem de leitura do disco.
func canonicalContentHash(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)

	h := sha256.New()
	for _, n := range names {
		_, _ = h.Write([]byte(n))
		_, _ = h.Write([]byte{0})
		sum := sha256.Sum256(files[n])
		_, _ = h.Write(sum[:])
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// signingMessage é a mensagem canónica assinada por ed25519. Liga o domínio, a
// policy_version e o content_hash: assim a assinatura cobre tanto o CONTEÚDO
// (via hash) como a VERSÃO declarada — adulterar qualquer um invalida a
// verificação.
func signingMessage(m Manifest) []byte {
	return []byte(signingDomain + "\n" + m.PolicyVersion + "\n" + m.ContentHash + "\n")
}

// Verify confere a integridade e autenticidade do bundle contra o trust anchor.
// Fail-closed:
//   - content_hash recomputado ≠ declarado no manifest → adulteração → ErrSignatureInvalid;
//   - assinatura ed25519 não verifica contra a chave pública → ErrSignatureInvalid.
//
// Não compila a política; ver [compilePolicies].
func (rb RawBundle) Verify(pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: trust anchor com tamanho invalido (%d)", ErrSignatureInvalid, len(pub))
	}
	if len(rb.Signature) != ed25519.SignatureSize {
		return fmt.Errorf("%w: assinatura ausente ou com tamanho invalido (%d)", ErrSignatureInvalid, len(rb.Signature))
	}
	got := canonicalContentHash(rb.PolicyFiles)
	if got != rb.Manifest.ContentHash {
		return fmt.Errorf("%w: content_hash nao corresponde (esperado %s, obtido %s)",
			ErrSignatureInvalid, rb.Manifest.ContentHash, got)
	}
	if !ed25519.Verify(pub, signingMessage(rb.Manifest), rb.Signature) {
		return fmt.Errorf("%w: verificacao ed25519 falhou", ErrSignatureInvalid)
	}
	return nil
}

// readPolicyFiles lê todos os *.cedar de um directório para um mapa nome→fonte.
func readPolicyFiles(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: ler dir de politicas %q: %v", ErrPolicyUnavailable, dir, err)
	}
	files := make(map[string][]byte)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), cedarExt) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("%w: ler %q: %v", ErrPolicyUnavailable, e.Name(), err)
		}
		files[e.Name()] = b
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: nenhum ficheiro %s em %q", ErrPolicyUnavailable, cedarExt, dir)
	}
	return files, nil
}

// loadRawBundle lê o directório de políticas: todos os *.cedar, o manifest.json
// e a assinatura aos_authz.sig (base64). Não lê a chave privada.
func loadRawBundle(dir string) (RawBundle, error) {
	var rb RawBundle
	files, err := readPolicyFiles(dir)
	if err != nil {
		return rb, err
	}
	rb.PolicyFiles = files

	mraw, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if err != nil {
		return rb, fmt.Errorf("%w: ler %s: %v", ErrPolicyUnavailable, manifestFile, err)
	}
	if err := json.Unmarshal(mraw, &rb.Manifest); err != nil {
		return rb, fmt.Errorf("%w: manifest invalido: %v", ErrPolicyUnavailable, err)
	}

	sraw, err := os.ReadFile(filepath.Join(dir, signatureFile))
	if err != nil {
		return rb, fmt.Errorf("%w: ler %s: %v", ErrSignatureInvalid, signatureFile, err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sraw)))
	if err != nil {
		return rb, fmt.Errorf("%w: assinatura nao e base64 valido: %v", ErrSignatureInvalid, err)
	}
	rb.Signature = sig
	return rb, nil
}

// SignBundle (re)assina o bundle no directório dado: valida que os .cedar
// compilam, calcula o content_hash canónico, materializa o manifest.json (com a
// policy_version dada e created_at UTC) e escreve a assinatura ed25519 em
// aos_authz.sig (base64). Não gere chaves nem escreve o trust anchor — isso é
// responsabilidade do chamador (ver cmd/policy-sign), que mantém a chave privada
// fora do repositório. Usada pela ferramenta de assinatura e reutilizável nos
// testes para forjar bundles válidos/adulterados.
func SignBundle(dir, version string, priv ed25519.PrivateKey) (Manifest, error) {
	var m Manifest
	files, err := readPolicyFiles(dir)
	if err != nil {
		return m, err
	}
	if _, err := compilePolicies(files); err != nil {
		return m, fmt.Errorf("politica invalida, assinatura abortada: %w", err)
	}
	m = Manifest{
		PolicyVersion: version,
		ContentHash:   canonicalContentHash(files),
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	sig := ed25519.Sign(priv, signingMessage(m))

	mraw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, err
	}
	if err := os.WriteFile(filepath.Join(dir, manifestFile), append(mraw, '\n'), 0o644); err != nil {
		return m, fmt.Errorf("escrever %s: %w", manifestFile, err)
	}
	sigB64 := base64.StdEncoding.EncodeToString(sig)
	if err := os.WriteFile(filepath.Join(dir, signatureFile), []byte(sigB64+"\n"), 0o644); err != nil {
		return m, fmt.Errorf("escrever %s: %w", signatureFile, err)
	}
	return m, nil
}

// loadTrustAnchor lê a chave pública ed25519 (base64) do trust anchor committado.
func loadTrustAnchor(dir string) (ed25519.PublicKey, error) {
	raw, err := os.ReadFile(filepath.Join(dir, trustAnchorFile))
	if err != nil {
		return nil, fmt.Errorf("%w: ler %s: %v", ErrPolicyUnavailable, trustAnchorFile, err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("%w: trust anchor nao e base64 valido: %v", ErrSignatureInvalid, err)
	}
	if len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: trust anchor com tamanho invalido (%d)", ErrSignatureInvalid, len(key))
	}
	return ed25519.PublicKey(key), nil
}
