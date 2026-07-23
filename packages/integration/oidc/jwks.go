package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
)

// b64urlDecode descodifica base64url SEM padding (o encoding JOSE/JWK). Aceita também
// a variante com padding por robustez (alguns IdPs incluem-no nos campos JWK).
func b64urlDecode(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// jwk é uma chave pública no formato JWK (só os campos que consumimos).
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"` // RSA modulus (base64url)
	E   string `json:"e"` // RSA exponent (base64url)
	Crv string `json:"crv"`
	X   string `json:"x"` // EC coordenada x (base64url)
	Y   string `json:"y"` // EC coordenada y (base64url)
}

// jwkSet é o documento JWKS (conjunto de chaves).
type jwkSet struct {
	Keys []jwk `json:"keys"`
}

// fetchJWKS busca e parseia o JWKS, devolvendo um mapa kid -> chave pública. Erro de
// rede, status != 200 ou documento inválido ⇒ [ErrJWKSUnavailable] (fail-closed).
func fetchJWKS(ctx context.Context, hc *http.Client, uri string) (map[string]crypto.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrJWKSUnavailable, err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrJWKSUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrJWKSUnavailable, resp.StatusCode)
	}
	var set jwkSet
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, fmt.Errorf("%w: json invalido: %v", ErrJWKSUnavailable, err)
	}

	keys := make(map[string]crypto.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		// Só chaves de assinatura. "use" ausente é tolerado (muitos IdPs omitem-no); se
		// presente, tem de ser "sig".
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		pub, err := parseJWK(k)
		if err != nil {
			// Uma chave individual não suportada não invalida o conjunto inteiro; é
			// simplesmente ignorada (o kid respectivo cairá em ErrUnknownKeyID).
			continue
		}
		if k.Kid == "" {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: nenhuma chave de assinatura utilizavel", ErrJWKSUnavailable)
	}
	return keys, nil
}

// parseJWK converte um JWK numa chave pública crypto. Suporta RSA e EC P-256 (ES256).
func parseJWK(k jwk) (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		nb, err := b64urlDecode(k.N)
		if err != nil {
			return nil, fmt.Errorf("%w: n invalido", ErrUnsupportedKey)
		}
		eb, err := b64urlDecode(k.E)
		if err != nil {
			return nil, fmt.Errorf("%w: e invalido", ErrUnsupportedKey)
		}
		n := new(big.Int).SetBytes(nb)
		e := new(big.Int).SetBytes(eb)
		if n.Sign() == 0 || e.Sign() == 0 {
			return nil, fmt.Errorf("%w: RSA n/e nulo", ErrUnsupportedKey)
		}
		// O expoente tem de caber num int não-negativo razoável (tipicamente 65537).
		if e.BitLen() > 31 {
			return nil, fmt.Errorf("%w: expoente RSA fora de alcance", ErrUnsupportedKey)
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		if k.Crv != "P-256" {
			return nil, fmt.Errorf("%w: curva EC %q (so P-256/ES256)", ErrUnsupportedKey, k.Crv)
		}
		xb, err := b64urlDecode(k.X)
		if err != nil {
			return nil, fmt.Errorf("%w: x invalido", ErrUnsupportedKey)
		}
		yb, err := b64urlDecode(k.Y)
		if err != nil {
			return nil, fmt.Errorf("%w: y invalido", ErrUnsupportedKey)
		}
		x := new(big.Int).SetBytes(xb)
		y := new(big.Int).SetBytes(yb)
		return &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, nil
	default:
		return nil, fmt.Errorf("%w: kty %q", ErrUnsupportedKey, k.Kty)
	}
}
