// Command aos-issuer é a AUTORIDADE DE IDENTIDADE EXTERNA do AOS (D4): detém a chave de
// assinatura e minta tokens NHI que o nó `aos` verifica TRUST-ANCHOR-ONLY. O nó NUNCA detém a
// chave de assinatura — é isso que torna a não-forjabilidade real (ver
// docs/reports/D4-escalacao-autoridade-identidade.md §2; ADR-006; AOS-175).
//
// É um PROCESSO SEPARADO por desenho: quem minta não pode ser quem verifica (senão o nó
// "verificaria" tokens que ele próprio mintou — teatro criptográfico). Em PRODUÇÃO a chave vive
// num HSM/KMS através da costura `crypto.Signer` (AOS-175) — a chave nunca entra em processo
// nenhum; aqui, para DEV, vive num ficheiro que o operador controla (declarado, nunca em
// código/log). A chave NUNCA é ecoada.
//
// Uso:
//
//	aos-issuer pubkey --key-file issuer.key
//	    → imprime a pubkey ed25519 em hex (64 chars) = o AOS_ISSUER_PUBKEY do nó.
//	aos-issuer mint --key-file issuer.key --issuer iss:aos-issuer \
//	    --human human:alice --agent agt-1 --class agent-worker --caps cap:fs.read
//	    → imprime o token NHI compact a apresentar ao nó (credencial do POST /runs).
//
// Fluxo de dev com o nó:
//  1. PUB=$(aos-issuer pubkey --key-file issuer.key)
//  2. correr o nó com AOS_ISSUER_ID=iss:aos-issuer AOS_ISSUER_PUBKEY=$PUB (modo endurecido)
//  3. TOK=$(aos-issuer mint --key-file issuer.key --issuer iss:aos-issuer --human … --caps …)
//  4. POST /runs com "credential": "$TOK" — o nó verifica trust-anchor-only, sem deter a chave.
package main

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	oidc "github.com/aos-ref/integration/oidc"
	identity "github.com/aos-ref/platform/identity"
)

const usage = `aos-issuer — autoridade de identidade externa (D4)
uso:
  aos-issuer pubkey --key-file <ficheiro>
  aos-issuer mint   --key-file <ficheiro> --issuer <id> --human <id> --agent <id> --class <c> --caps <c1,c2> [--ttl 15m] [--auth-method manual]
  aos-issuer approve-sign --request-id <id> --preview <b64> --approver <id> --session <s> --credential <c> --key-file <ficheiro>
  aos-issuer ratify-sign  --artifact-id <id> --version <semver> --content-hash <b64> --ratifier <id> --key-file <ficheiro> [--canary-passed] [--eval-*]
  aos-issuer delegation-nonce --agent <id> --class <c> --caps <c1,c2> [--ttl 15m]
  aos-issuer autonomy-sign --emitter <id> --key-file <ficheiro> --agent <id> --domain <d> --level <L0..L5> --reason <texto> [--co-emitter <id> --co-key-file <ficheiro>]
                          → imprime o corpo JSON de POST /autonomy; L4/L5 exigem a segunda assinatura (AOS-305)
  aos-issuer challenge-sign --approver <id> --key-file <ficheiro> --run <id> --request-id <id>
                          → imprime o corpo JSON de POST /runs/{run}/challenge (AOS-308: o aprovador pede o seu challenge)
  aos-issuer revoke-sign   --emitter <id> --key-file <ficheiro> --jti <jti> --reason <texto>
                          → imprime o corpo JSON de POST /nhi/revoke (AOS-288)
  aos-issuer worm-seal --worm <ficheiro> --key-file <ficheiro> [--partition <p>] [--anterior <ficheiro>] [--heads]
                          → imprime o corpo JSON de POST /promote (AOS-275)`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "aos-issuer:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "pubkey":
		return cmdPubkey(args[1:], out)
	case "approve-sign":
		return runApproveSign(args[1:])
	case "ratify-sign":
		return runRatifySign(args[1:], out)
	case "mint":
		return cmdMint(args[1:], out)
	case "delegation-nonce":
		return cmdDelegationNonce(args[1:], out)
	case "autonomy-sign":
		return runAutonomySign(args[1:], out)
	case "challenge-sign":
		return runChallengeSign(args[1:], out)
	case "revoke-sign":
		return runRevokeSign(args[1:], out)
	case "worm-seal":
		return runWormSeal(args[1:], out)
	default:
		return fmt.Errorf("subcomando desconhecido %q\n%s", args[0], usage)
	}
}

// cmdPubkey imprime a chave PÚBLICA do issuer em hex — o valor a passar ao nó como
// AOS_ISSUER_PUBKEY. É a ÚNICA saída de material de chave: a privada nunca sai do ficheiro/HSM.
func cmdPubkey(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("pubkey", flag.ContinueOnError)
	keyFile := fs.String("key-file", "issuer.key", "ficheiro da chave de assinatura (seed ed25519 em hex)")
	buildSigner := vaultSignerFlags(fs, keyFile)
	if err := fs.Parse(args); err != nil {
		return err
	}
	signer, err := buildSigner()
	if err != nil {
		return err
	}
	pub, ok := signer.Public().(ed25519.PublicKey)
	if !ok {
		return errors.New("chave sem pubkey ed25519")
	}
	_, err = fmt.Fprintln(out, hex.EncodeToString(pub))
	return err
}

// cmdMint emite um token NHI assinado pela chave do issuer e imprime a sua forma compact. O nó
// verifica-o contra o AOS_ISSUER_PUBKEY (a mesma pubkey de `pubkey`), sem nunca deter a chave.
func cmdMint(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("mint", flag.ContinueOnError)
	keyFile := fs.String("key-file", "issuer.key", "ficheiro da chave de assinatura")
	buildSigner := vaultSignerFlags(fs, keyFile)
	issuerID := fs.String("issuer", "iss:aos-issuer", "id do issuer (== AOS_ISSUER_ID do nó)")
	human := fs.String("human", "", "humano responsável (raiz da delegação); alternativa a --assertion (via manual/allowlist)")
	agent := fs.String("agent", "", "id do agente (NHI a criar)")
	class := fs.String("class", "", "classe do agente (selecciona a ClassPolicy)")
	caps := fs.String("caps", "", "capabilities CSV que o utilizador possui (a autoridade é a intersecção com a classe)")
	ttl := fs.Duration("ttl", 15*time.Minute, "TTL do token")
	authMethod := fs.String("auth-method", "manual", "rótulo do método de autenticação humana (binding audit AOS-176; NUNCA PII)")
	// AUTENTICAÇÃO OIDC do humano (front 1 do D4, AOS-174): com --assertion o humano-raiz é
	// DERIVADO do `sub` de um ID-token VERIFICADO contra o IdP, não auto-declarado por flag.
	assertion := fs.String("assertion", "", "ID-token OIDC do humano (autentica-o contra o IdP; alternativa a --human)")
	oidcIssuer := fs.String("oidc-issuer", "", "issuer OIDC (URL) — exigido com --assertion")
	oidcAudience := fs.String("oidc-audience", "", "audience OIDC — exigido com --assertion")
	oidcJWKS := fs.String("oidc-jwks", "", "JWKS URI do IdP (opcional; vazio ⇒ discovery via issuer)")
	oidcCA := fs.String("oidc-ca", "", "CA PEM do IdP quando serve TLS com CA privada (vazio ⇒ trust store do sistema)")
	// LIGAÇÃO À DELEGAÇÃO: por omissão, --assertion EXIGE que o humano se tenha autenticado PARA
	// ESTA delegação (nonce == digest de agent/class/caps/ttl). Desligá-la é possível e fica
	// ESCRITO NO REGISTO — o rótulo desce de oidc-bound: para oidc:, e um auditor distingue
	// "autorizou isto" de "esteve presente". Existe porque nem toda a autenticação tem nonce (o
	// password grant não tem), não porque a ligação seja opcional em produção.
	assertionUnbound := fs.Bool("assertion-unbound", false, "aceitar a asserção SEM a ligar a esta delegação (rótulo desce a oidc:<iss>)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *agent == "" || *class == "" {
		return errors.New("mint exige --agent e --class")
	}

	// Resolver o humano-raiz da delegação: via OIDC (autenticado) ou via flag (manual).
	rootHuman, method := *human, *authMethod
	if *assertion != "" {
		if *oidcIssuer == "" || *oidcAudience == "" {
			return errors.New("--assertion exige --oidc-issuer e --oidc-audience")
		}
		// HTTPClient/Clock nil ⇒ defaults endurecidos do verificador (AOS-229: TLS 1.2 + timeout +
		// limite de redirects + anti-SSRF); sem AllowInsecureTransport ⇒ https exigido ao IdP.
		hc, err := idpHTTPClient(*oidcCA)
		if err != nil {
			return err
		}
		// O nonce esperado é DERIVADO das flags que estamos a cunhar — nunca recebido por
		// parâmetro. Um nonce fornecido ao lado dos parâmetros far-se-ia coincidir com o que
		// quer que se estivesse a cunhar, e a "ligação" não ligaria nada.
		nonce := ""
		if !*assertionUnbound {
			nonce = delegationNonce(*agent, *class, splitCSV(*caps), *ttl)
		}
		h, m, err := authenticateOIDC(context.Background(), oidc.Config{
			Issuer:     *oidcIssuer,
			Audience:   *oidcAudience,
			JWKSURI:    *oidcJWKS, // vazio ⇒ discovery via issuer
			HTTPClient: hc,        // nil quando não há --oidc-ca ⇒ default endurecido de AOS-229
			Nonce:      nonce,     // vazio ⇒ sem ligação (rótulo desce; ver --assertion-unbound)
			// TECTO DE IDADE. Sem isto o token era aceite durante toda a janela do exp, o que
			// fazia deste o MAIS FRACO dos três verificadores OIDC do sistema (o read soberano e
			// o directório humano do nó já impunham 5 min). Não se liga aqui o RequireJTI: o
			// armazém anti-replay é um campo do Verifier, e este binário é um processo de vida
			// curta — o mapa nasce vazio a cada invocação, pelo que pareceria anti-replay e não
			// seria nenhum. O que serve de facto é este tecto, mais a ligação ao nonce.
			MaxAge: assertionMaxAge,
		}, *assertion)
		if err != nil {
			return fmt.Errorf("autenticação OIDC do humano: %w", err)
		}
		rootHuman, method = h, m
		if !*assertionUnbound {
			// Rótulo FORTE: o humano autenticou-se PARA ESTA delegação, não apenas "esteve
			// presente". *oidcIssuer é seguro como fonte porque Validate já exigiu que o `iss`
			// do token lhe fosse igual.
			method = "oidc-bound:" + *oidcIssuer
		}
	}
	if rootHuman == "" {
		return errors.New("mint exige --human ou --assertion (o humano-raiz da delegação)")
	}
	scope := splitCSV(*caps)

	signer, err := buildSigner()
	if err != nil {
		return err
	}
	// A via de custódia externa (AOS-175): o Issuer assina ATRAVÉS de um crypto.Signer. Com
	// --vault-addr o signer é o Vault Transit (a chave ed25519 vive no Vault e NUNCA entra no
	// processo — a realização da custódia HSM/KMS); senão, a ed25519.PrivateKey do ficheiro. O nó
	// continua a verificar só com a pubkey (trust-anchor-only).
	iss, err := identity.NewIssuerWithSigner(*issuerID, signer, map[string]identity.ClassPolicy{
		*class: {TTL: *ttl, Scope: scope},
	})
	if err != nil {
		return fmt.Errorf("construir issuer: %w", err)
	}
	tok, err := iss.Issue(context.Background(), identity.IssueRequest{
		UserID:        rootHuman,
		AgentID:       *agent,
		AgentClass:    *class,
		PolicyRef:     "policy://" + *class,
		UserAuthority: scope,
		AuthMethod:    method,
	})
	if err != nil {
		return fmt.Errorf("mint: %w", err)
	}
	_, err = fmt.Fprintln(out, tok.Compact)
	return err
}

// authenticateOIDC verifica o ID-token do humano contra o IdP com o verificador OIDC REAL de
// AOS-174 (discovery/JWKS + assinatura JWS + anti-alg-confusion + aud/exp/iat) e devolve o
// humano-raiz (`human:<sub verificado>`) e o rótulo de método para o binding audit
// (`oidc:<issuer>`, AOS-176). Fail-closed: qualquer falha de verificação propaga-se — nenhum
// humano é derivado de um token não-verificado, e nenhum token NHI é emitido. É a costura front-1
// do D4 (substitui a allowlist demo pela autenticação real), cbor-free: usa só `integration/oidc`
// (stdlib JWS/JWKS), não o pacote `integration` (que traria a lib WebAuthn da attestation).
//
// TRANSPORTE ENDURECIDO: com `cfg.HTTPClient` nil (o caminho do `mint`), `oidc.NewVerifier` usa o
// cliente DEFAULT endurecido de AOS-229 (TLS 1.2 + timeout + limite de redirects + anti-SSRF); e,
// sem `AllowInsecureTransport`, exige https ao IdP (loopback exceptuado). Um chamador que injecte
// um `cfg.HTTPClient` (ex.: httptest) é respeitado tal-qual.
func authenticateOIDC(ctx context.Context, cfg oidc.Config, assertion string) (human, method string, err error) {
	v, err := oidc.NewVerifier(cfg)
	if err != nil {
		return "", "", err
	}
	claims, err := v.Validate(ctx, assertion)
	if err != nil {
		return "", "", err
	}
	return "human:" + claims.Subject, "oidc:" + claims.Issuer, nil
}

// loadOrCreateKey carrega a seed ed25519 (32 bytes em hex) do ficheiro; se não existir, gera uma
// e grava-a com permissões 0600. É a via de DEV: a chave privada vive num ficheiro que o operador
// vaultSignerFlags regista as flags de custódia da chave do issuer no Vault (AOS-175) num FlagSet
// e devolve um construtor que, após o Parse, produz o crypto.Signer: o signer Vault (a chave vive
// no Transit, NUNCA no processo) quando --vault-addr está presente, senão a chave de ficheiro
// local. Partilhado por `mint` e `pubkey` para que a pubkey exportada (o trust-anchor do nó) venha
// SEMPRE da mesma fonte que assina os tokens.
func vaultSignerFlags(fs *flag.FlagSet, keyFile *string) func() (crypto.Signer, error) {
	addr := fs.String("vault-addr", "", "endereco do Vault (ex.: http://vault:8200) — custodia da chave do issuer no Transit (AOS-175); vazio => --key-file")
	mount := fs.String("vault-mount", "transit", "mount do motor Transit do Vault")
	key := fs.String("vault-key", "", "nome da chave ed25519 do issuer no Transit")
	tokenPath := fs.String("vault-token-path", "", "ficheiro com o token do Vault (material privado nunca por flag)")
	caPath := fs.String("vault-ca", "", "CA PEM do Vault quando serve TLS com CA privada (vazio ⇒ trust store do sistema)")
	return func() (crypto.Signer, error) {
		if strings.TrimSpace(*addr) == "" {
			return loadOrCreateKey(*keyFile)
		}
		if strings.TrimSpace(*key) == "" || strings.TrimSpace(*tokenPath) == "" {
			return nil, errors.New("--vault-addr exige --vault-key e --vault-token-path")
		}
		tb, err := os.ReadFile(*tokenPath)
		if err != nil {
			return nil, fmt.Errorf("ler token do Vault: %w", err)
		}
		return newVaultTransitSigner(*addr, *mount, *key, strings.TrimSpace(string(tb)), *caPath)
	}
}

// controla, FORA do processo do nó. Em PRODUÇÃO, substituir por um crypto.Signer HSM/KMS (AOS-175)
// — a chave nunca entra em processo nenhum. NUNCA se ecoa o material da chave.
func loadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		texto, cerr := limparSeedHex(raw) // ver [limparSeedHex].
		if cerr != nil {
			return nil, cerr
		}
		seed, derr := hex.DecodeString(texto)
		if derr != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("ficheiro de chave %q inválido (esperado seed ed25519 de %d bytes em hex)", path, ed25519.SeedSize)
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(seed)), 0o600); err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
