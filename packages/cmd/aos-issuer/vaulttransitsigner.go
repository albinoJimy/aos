package main

// Custódia da chave de assinatura do issuer no HashiCorp Vault (motor Transit), pela costura
// stdlib crypto.Signer que o Issuer sempre previu (AOS-175). É a REALIZAÇÃO em Vault do adaptador
// HSM/KMS: a chave ed25519 do issuer vive DENTRO do Vault e NUNCA entra no processo — a assinatura
// (transit/sign) e a leitura da pubkey (transit/keys) correm no Vault. Fecha o "teatro
// criptográfico" do D4: quem detém a autoridade de identidade é o Vault, não o binário; o nó
// verifica só com a pubkey (trust-anchor-only), sem nunca poder mintar tokens.
//
// ZERO-DEP: fala com o Vault pela API HTTP usando só a stdlib (mesma disciplina do vaultKeyVault
// do nó). ed25519 é PureEdDSA — assina a mensagem crua (o Issuer passa crypto.Hash(0)); o Transit
// ed25519 também assina o input directamente, pelo que a assinatura valida contra a pubkey.

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type vaultTransitSigner struct {
	addr, mount, key, token string
	hc                      *http.Client
	pub                     ed25519.PublicKey
}

// newVaultTransitSigner liga-se ao Vault e obtém a pubkey ed25519 da chave Transit nomeada
// (fail-closed: a chave tem de existir e ser ed25519). NÃO cria a chave — o provisionamento
// (transit/keys/<name> type=ed25519) é um passo do operador/up-script, separado do uso.
func newVaultTransitSigner(addr, mount, key, token string) (*vaultTransitSigner, error) {
	if addr == "" || key == "" || token == "" {
		return nil, errors.New("vault signer exige addr, key e token")
	}
	if mount == "" {
		mount = "transit"
	}
	s := &vaultTransitSigner{
		addr:  strings.TrimRight(addr, "/"),
		mount: strings.Trim(mount, "/"),
		key:   key,
		token: token,
		hc:    &http.Client{Timeout: 10 * time.Second},
	}
	pub, err := s.fetchPublicKey()
	if err != nil {
		return nil, err
	}
	s.pub = pub
	return s, nil
}

// Public devolve a pubkey ed25519 do issuer (o trust-anchor do nó). É a ÚNICA saída de material
// de chave; a privada nunca sai do Vault.
func (s *vaultTransitSigner) Public() crypto.PublicKey { return s.pub }

// Sign assina a mensagem crua via transit/sign (ed25519, sem pré-hash). O Issuer chama-o com
// opts=crypto.Hash(0); qualquer outro valor é recusado (não seria ed25519 puro).
func (s *vaultTransitSigner) Sign(_ io.Reader, message []byte, opts crypto.SignerOpts) ([]byte, error) {
	if opts != nil && opts.HashFunc() != crypto.Hash(0) {
		return nil, errors.New("vault signer: ed25519 exige opts.HashFunc()==0 (mensagem sem pre-hash)")
	}
	reqBody, _ := json.Marshal(map[string]string{"input": base64.StdEncoding.EncodeToString(message)})
	body, code, err := s.do(http.MethodPost, "/v1/"+s.mount+"/sign/"+s.key, reqBody)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("vault sign HTTP %d", code)
	}
	var r struct {
		Data struct {
			Signature string `json:"signature"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	// Formato "vault:v<n>:<base64(sig)>".
	parts := strings.SplitN(r.Data.Signature, ":", 3)
	if len(parts) != 3 {
		return nil, errors.New("assinatura vault malformada")
	}
	sig, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, err
	}
	if len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("assinatura ed25519 de tamanho errado: %d", len(sig))
	}
	return sig, nil
}

func (s *vaultTransitSigner) fetchPublicKey() (ed25519.PublicKey, error) {
	body, code, err := s.do(http.MethodGet, "/v1/"+s.mount+"/keys/"+s.key, nil)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("vault get key HTTP %d (a chave %q existe e e ed25519?)", code, s.key)
	}
	var r struct {
		Data struct {
			Type          string `json:"type"`
			LatestVersion int    `json:"latest_version"`
			Keys          map[string]struct {
				PublicKey string `json:"public_key"`
			} `json:"keys"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if r.Data.Type != "ed25519" {
		return nil, fmt.Errorf("chave vault %q nao e ed25519 (e %q)", s.key, r.Data.Type)
	}
	kv, ok := r.Data.Keys[fmt.Sprint(r.Data.LatestVersion)]
	if !ok || kv.PublicKey == "" {
		return nil, fmt.Errorf("sem public_key na versao %d da chave %q", r.Data.LatestVersion, s.key)
	}
	pub, err := base64.StdEncoding.DecodeString(kv.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("public_key vault nao e base64: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("pubkey ed25519 de tamanho errado: %d", len(pub))
	}
	return ed25519.PublicKey(pub), nil
}

func (s *vaultTransitSigner) do(method, path string, body []byte) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, s.addr+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-Vault-Token", s.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return rb, resp.StatusCode, nil
}
