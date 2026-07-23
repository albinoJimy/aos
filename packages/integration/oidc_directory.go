package integration

import (
	"context"

	"github.com/aos-ref/integration/oidc"
)

// OIDCDirectory é a impl REAL de [HumanDirectory] (AOS-174, frente 1 do D4): autentica
// o humano validando um ID-token OIDC contra um IdP (discovery + JWKS + verificação de
// assinatura do JWS + validação de claims), só com a stdlib (ver o subpacote oidc). É o
// que substitui a [AllowlistDirectory] demo-grade como fronteira de autenticação humana
// a montante do mint.
//
// A prova entra pela via [OIDCDirectory.AuthenticateAssertion] (o ID-token → sub
// verificado → humanID), consumida por [IssuerAuthority.MintForAssertion]. A via
// só-humanID [OIDCDirectory.Authenticate] é recusada fail-closed com
// [ErrAssertionRequired]: sem o ID-token não há prova, logo não se autentica ninguém
// por mero nome.
//
// O ID-token é material sensível e NUNCA é logado (o subpacote oidc não escreve logs).
type OIDCDirectory struct {
	verifier *oidc.Verifier
}

// NewOIDCDirectory constrói o directório OIDC a partir da config do verifier (issuer,
// audience/client, jwks_uri ou discovery, http.Client e relógio injectáveis). Recusa
// fail-closed a config incompleta (issuer/audience em falta) via o próprio verifier.
func NewOIDCDirectory(cfg oidc.Config) (*OIDCDirectory, error) {
	v, err := oidc.NewVerifier(cfg)
	if err != nil {
		return nil, err
	}
	return &OIDCDirectory{verifier: v}, nil
}

// NewOIDCDirectoryFromVerifier embrulha um [oidc.Verifier] já construído (útil quando o
// verifier é partilhado ou pré-configurado noutro ponto do wiring).
func NewOIDCDirectoryFromVerifier(v *oidc.Verifier) *OIDCDirectory {
	return &OIDCDirectory{verifier: v}
}

// Authenticate implementa a via só-humanID de [HumanDirectory]. Fail-closed: uma
// autoridade OIDC exige a PROVA (o ID-token), logo confirmar um humano por mero nome é
// sempre recusado com [ErrAssertionRequired]. Isto garante que um chamador não pode
// contornar a validação do ID-token invocando a via legada [IssuerAuthority.MintForHuman].
func (d *OIDCDirectory) Authenticate(_ context.Context, _ string) error {
	return ErrAssertionRequired
}

// AuthenticateAssertion valida o ID-token OIDC e devolve o sub VERIFICADO como humanID.
// Qualquer falha de validação (assinatura, alg, iss/aud, janela, kid, JWKS…) devolve o
// erro tipado do subpacote oidc (comparável com errors.Is), sem humanID.
func (d *OIDCDirectory) AuthenticateAssertion(ctx context.Context, assertion string) (string, error) {
	claims, err := d.verifier.Validate(ctx, assertion)
	if err != nil {
		return "", err
	}
	return claims.Subject, nil
}
