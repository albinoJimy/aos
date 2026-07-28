// AOS-205 — CREDENCIAL FORTE do read-path soberano. Fecha o eixo de DEF-204/207/208: os
// headers X-Aos-Reader/X-Aos-Board deixam de ser AUTO-DECLARADOS. O leitor de governação e o
// operador DSAR apresentam uma CREDENCIAL FORTE (um ID-token OIDC) que o nó VERIFICA contra o
// IdP — reutilizando a validação REAL de AOS-174 (packages/integration/oidc: discovery + JWKS +
// verificação de assinatura JWS + anti-alg-confusion + anti-replay). O board e o principal são
// DERIVADOS das CLAIMS VERIFICADAS (`sub` e `board`), NÃO de um cabeçalho cru — pelo que um
// X-Aos-Board forjado (board válido mas sem credencial, ou credencial de OUTRO titular com outro
// board) é RECUSADO fail-closed.
//
// NÃO se reimplementa verificação de token: [oidc.Verifier.Validate] é a fronteira; aqui só se
// extrai o Bearer do pedido e se lê o par (sub, board) das claims que ela devolve já verificadas.
// O ID-token é material sensível e NUNCA é logado.
//
// mTLS é a OUTRA via de credencial forte prevista (certificado de cliente terminado no nó): fica
// como impl alternativa da MESMA porta [readCredentialVerifier], a par da autenticação mútua do
// plano de controlo — o eixo do transporte mútuo está registado em DEF-012 (AOS-209). Este
// ficheiro entrega a via OIDC, que é a que o IdP de soberania serve.
package main

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/aos-ref/integration/oidc"
)

// Erros da credencial forte de leitura (fail-closed, comparáveis com errors.Is).
var (
	// ErrNoReadCredential — o pedido não trouxe credencial forte (sem `Authorization: Bearer`).
	// Uma leitura de governação NUNCA é autorizada por um header auto-declarado sem prova.
	ErrNoReadCredential = errors.New("aos: read-path soberano exige credencial forte (Authorization: Bearer <id-token OIDC>) — o header X-Aos-Board auto-declarado nao autoriza")
	// ErrNoBoardClaim — a credencial verificou mas NÃO traz o claim `board`. Sem board
	// verificado não há fronteira de soberania a resolver ⇒ deny (nunca se cai no header cru).
	ErrNoBoardClaim = errors.New("aos: id-token verificado sem claim `board` — a fronteira de soberania tem de vir das claims verificadas, nao de um header")
)

// readCredentialVerifier é a PORTA da credencial forte do read-path soberano: valida a
// credencial do pedido e devolve o principal e o board DERIVADOS das claims VERIFICADAS. Uma
// credencial ausente/inválida ⇒ erro (fail-closed). Impls: OIDC ([oidcReadCredential]); mTLS é a
// via alternativa prevista (DEF-012/AOS-209).
type readCredentialVerifier interface {
	verify(ctx context.Context, r *http.Request) (principal, board string, err error)
}

// oidcReadCredential verifica a credencial forte por ID-token OIDC, reutilizando o verifier REAL
// de AOS-174. Concorrente-seguro na medida em que o [oidc.Verifier] o é.
type oidcReadCredential struct {
	verifier *oidc.Verifier
}

// newOIDCReadCredential embrulha um verifier OIDC já construído como porta de credencial de
// leitura.
func newOIDCReadCredential(v *oidc.Verifier) *oidcReadCredential {
	return &oidcReadCredential{verifier: v}
}

// verify extrai o Bearer, valida-o contra o IdP (AOS-174) e devolve (sub, board) VERIFICADOS. O
// board vem do claim `board` do MESMO payload assinado — imune a forja por header. Qualquer
// falha (sem Bearer, assinatura/iss/aud/janela/replay inválidos, sem board) ⇒ erro fail-closed.
func (o *oidcReadCredential) verify(ctx context.Context, r *http.Request) (string, string, error) {
	tok := bearerToken(r)
	if tok == "" {
		return "", "", ErrNoReadCredential
	}
	claims, err := o.verifier.Validate(ctx, tok)
	if err != nil {
		// O erro tipado de oidc (comparável com errors.Is) é propagado SEM o token cru.
		return "", "", err
	}
	board := strings.TrimSpace(claims.Board)
	if board == "" {
		return "", "", ErrNoBoardClaim
	}
	principal := strings.TrimSpace(claims.Subject)
	if principal == "" {
		// oidc.Validate já recusa sub vazio (ErrNoSubject); guarda de profundidade.
		return "", "", ErrNoReadCredential
	}
	return principal, board, nil
}

// bearerToken extrai o token do cabeçalho `Authorization: Bearer <token>` (esquema
// case-insensitive, RFC 6750). Devolve "" quando ausente ou noutro esquema — nunca adivinha uma
// credencial. O valor NÃO é logado em lado nenhum (material sensível).
func bearerToken(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h == "" {
		return ""
	}
	const prefix = "bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}
