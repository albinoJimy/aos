package main

// Custódia da KEK por-titular em HashiCorp Vault (Transit), pela porta [audit.KeyWrapper]
// (AOS-216, residual de DEF-302/AOS-215). É a alternativa REAL ao vault in-memory demo-grade: a
// KEK do titular vive DENTRO do Vault e NUNCA entra no processo do nó — o embrulho/desembrulho da
// DEK corre no Vault (Transit encrypt/decrypt); o crypto-shred (GDPR Art. 17) é a DESTRUIÇÃO da
// chave Transit do titular, após a qual o UnwrapDEK falha fechado e o conteúdo é irrecuperável.
//
// ZERO-DEP (ADR-017/Carta §4.1): fala com o Vault pela API HTTP usando SÓ a stdlib — não importa o
// SDK Go do Vault. É a mesma disciplina de todos os adaptadores de porta do nó; o go.mod não é
// tocado. O comentário de [audit.KeyWrapper] nomeia explicitamente "HashiCorp Vault Transit" como
// backing sancionado desta porta.
//
// DEV vs PRODUÇÃO: aqui o token do Vault é lido de um FICHEIRO montado (material privado nunca por
// variável de ambiente, no padrão de AOS_ISSUER_KEY_PATH); em produção o transporte é https e o
// token vem de um AppRole/Kubernetes-auth de curta duração. O contrato de custódia (key-never-leaves
// + shred por destruição de chave) é o mesmo.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	audit "github.com/aos-ref/platform/audit"
)

// ErrVaultKEK — falha ao provisionar/embrulhar contra o Vault (WrapDEK/EnsureKey). Fail-closed: a
// ingestão de um registo com PII propaga o erro pela cadeia de cifra; NUNCA há fallback silencioso
// para custódia mais fraca.
var ErrVaultKEK = errors.New("aos: custódia da KEK no Vault falhou")

// vaultKeyVault implementa [audit.KeyWrapper] (+ [audit.KeyVault]) sobre o motor Transit do Vault.
// A KEK por-titular é uma chave Transit nomeada de forma DETERMINÍSTICA e SEM PII (sha256 do
// keyRef); a KEK crua nunca é devolvida ao nó.
type vaultKeyVault struct {
	addr  string // ex.: "http://vault:8200" (dev) ou "https://vault:8200" (prod, via SSL_CERT_FILE)
	mount string // mount do motor Transit (default "transit")
	token string // token do Vault (lido de ficheiro montado; nunca logado)
	hc    *http.Client
}

// newVaultKeyVault constrói o adaptador. addr/mount/token já validados por quem chama.
func newVaultKeyVault(addr, mount, token string) *vaultKeyVault {
	return &vaultKeyVault{
		addr:  strings.TrimRight(addr, "/"),
		mount: strings.Trim(mount, "/"),
		token: token,
		hc:    &http.Client{Timeout: 10 * time.Second},
	}
}

// vaultKeyName mapeia o keyRef da camada de auditoria (KeyRefFor(subjectID) = prefixo+subjectID,
// que pode conter ':' e não é um nome de chave Transit válido) para um nome DETERMINÍSTICO e
// Vault-safe. O sha256 também impede que o subjectID (potencial PII) apareça no nome da chave.
func vaultKeyName(keyRef string) string {
	sum := sha256.Sum256([]byte(keyRef))
	return "aos-kek-" + hex.EncodeToString(sum[:])
}

// do executa um pedido HTTP autenticado ao Vault e devolve corpo+status. Erros de transporte e
// status >= 400 (exceto os que o chamador tolera) viram erro tipado.
func (v *vaultKeyVault) do(method, path string, body any) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, v.addr+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-Vault-Token", v.token) // nunca logado
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := v.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return rb, resp.StatusCode, nil
}

// ensureTransitKey garante (idempotente) que a chave Transit do titular existe. Criar uma chave já
// existente é no-op no Vault (204).
func (v *vaultKeyVault) ensureTransitKey(name string) error {
	_, code, err := v.do(http.MethodPost, "/v1/"+v.mount+"/keys/"+name, map[string]string{"type": "aes256-gcm96"})
	if err != nil {
		return fmt.Errorf("%w: criar chave: %v", ErrVaultKEK, err)
	}
	if code != http.StatusOK && code != http.StatusNoContent {
		return fmt.Errorf("%w: criar chave: status %d", ErrVaultKEK, code)
	}
	return nil
}

// WrapDEK embrulha a DEK sob a chave Transit do titular DENTRO do Vault (encrypt). A KEK não sai.
// Devolve o ciphertext opaco do Vault ("vault:v1:...") e o keyRef da camada de auditoria — que TEM
// de ser exatamente [audit.KeyRefFor](subjectID) para o subject-binding de [audit.OpenContent].
func (v *vaultKeyVault) WrapDEK(subjectID string, dek []byte) ([]byte, string, error) {
	if subjectID == "" {
		return nil, "", audit.ErrNoSubject
	}
	ref := audit.KeyRefFor(subjectID)
	name := vaultKeyName(ref)
	if err := v.ensureTransitKey(name); err != nil {
		return nil, "", err
	}
	rb, code, err := v.do(http.MethodPost, "/v1/"+v.mount+"/encrypt/"+name,
		map[string]string{"plaintext": base64.StdEncoding.EncodeToString(dek)})
	if err != nil {
		return nil, "", fmt.Errorf("%w: encrypt: %v", ErrVaultKEK, err)
	}
	if code != http.StatusOK {
		return nil, "", fmt.Errorf("%w: encrypt: status %d", ErrVaultKEK, code)
	}
	var out struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rb, &out); err != nil || out.Data.Ciphertext == "" {
		return nil, "", fmt.Errorf("%w: resposta de encrypt sem ciphertext", ErrVaultKEK)
	}
	return []byte(out.Data.Ciphertext), ref, nil
}

// UnwrapDEK desembrulha a DEK DENTRO do Vault (decrypt). FAIL-CLOSED: chave destruída (crypto-shred)
// ou ciphertext adulterado ⇒ o Vault devolve erro ⇒ (nil,false); a DEK — logo o conteúdo — é
// irrecuperável. A KEK nunca entra no processo.
func (v *vaultKeyVault) UnwrapDEK(keyRef string, wrapped []byte) ([]byte, bool) {
	name := vaultKeyName(keyRef)
	rb, code, err := v.do(http.MethodPost, "/v1/"+v.mount+"/decrypt/"+name,
		map[string]string{"ciphertext": string(wrapped)})
	if err != nil || code != http.StatusOK {
		return nil, false // chave destruída/ausente ou embrulho inválido — irrecuperável
	}
	var out struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rb, &out); err != nil || out.Data.Plaintext == "" {
		return nil, false
	}
	dek, err := base64.StdEncoding.DecodeString(out.Data.Plaintext)
	if err != nil {
		return nil, false
	}
	return dek, true
}

// EnsureKey implementa [audit.KeyVault] honrando key-never-leaves: provisiona a chave Transit mas
// NUNCA devolve a KEK crua (key=nil). O caminho de escrita usa [WrapDEK], não isto.
func (v *vaultKeyVault) EnsureKey(subjectID string) ([]byte, string, error) {
	if subjectID == "" {
		return nil, "", audit.ErrNoSubject
	}
	ref := audit.KeyRefFor(subjectID)
	if err := v.ensureTransitKey(vaultKeyName(ref)); err != nil {
		return nil, "", err
	}
	return nil, ref, nil
}

// Key implementa [audit.KeyVault] honrando key-never-leaves: a KEK crua NUNCA é surrendida ⇒
// devolve sempre (nil,false). É esta recusa que força o caminho de envelope em [audit.OpenContent].
func (v *vaultKeyVault) Key(keyRef string) ([]byte, bool) { return nil, false }

// Delete implementa [audit.KeyVault] (crypto-shredding, GDPR Art. 17): destrói a chave Transit do
// titular no Vault ⇒ [UnwrapDEK] passa a falhar e o conteúdo fica irrecuperável. Idempotente
// (destruir o que não existe é no-op). A destruição exige deletion_allowed=true primeiro.
//
// LIMITAÇÃO da porta: [audit.KeyVault.Delete] não devolve erro — uma falha de rede aqui não pode
// ser sinalizada ao chamador (o mesmo vale para o vault in-memory). Em produção, o operador do
// pipeline DSAR deve VERIFICAR a destruição (ex.: reconciliação contra o Vault) antes de selar o
// desfecho como irrecuperável; a re-verificação de hash-chain do WORM não cobre a custódia externa.
func (v *vaultKeyVault) Delete(subjectID string) {
	name := vaultKeyName(audit.KeyRefFor(subjectID))
	// Habilita a destruição (config) e destrói. Best-effort idempotente; ignora 404 (já destruída).
	_, _, _ = v.do(http.MethodPost, "/v1/"+v.mount+"/keys/"+name+"/config", map[string]bool{"deletion_allowed": true})
	_, _, _ = v.do(http.MethodDelete, "/v1/"+v.mount+"/keys/"+name, nil)
}

// Asserções de compile-time: o adaptador satisfaz ambas as portas (como o wrapper de referência).
var (
	_ audit.KeyVault   = (*vaultKeyVault)(nil)
	_ audit.KeyWrapper = (*vaultKeyVault)(nil)
)
