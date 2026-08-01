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

	identity "github.com/aos-ref/platform/identity"
)

const usage = `aos-issuer — autoridade de identidade externa (D4)
uso:
  aos-issuer pubkey --key-file <ficheiro>
  aos-issuer mint   --key-file <ficheiro> --issuer <id> --human <id> --agent <id> --class <c> --caps <c1,c2> [--ttl 15m] [--auth-method manual]`

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
	case "mint":
		return cmdMint(args[1:], out)
	default:
		return fmt.Errorf("subcomando desconhecido %q\n%s", args[0], usage)
	}
}

// cmdPubkey imprime a chave PÚBLICA do issuer em hex — o valor a passar ao nó como
// AOS_ISSUER_PUBKEY. É a ÚNICA saída de material de chave: a privada nunca sai do ficheiro/HSM.
func cmdPubkey(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("pubkey", flag.ContinueOnError)
	keyFile := fs.String("key-file", "issuer.key", "ficheiro da chave de assinatura (seed ed25519 em hex)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	priv, err := loadOrCreateKey(*keyFile)
	if err != nil {
		return err
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
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
	issuerID := fs.String("issuer", "iss:aos-issuer", "id do issuer (== AOS_ISSUER_ID do nó)")
	human := fs.String("human", "", "humano responsável (raiz da cadeia de delegação, ADR-003)")
	agent := fs.String("agent", "", "id do agente (NHI a criar)")
	class := fs.String("class", "", "classe do agente (selecciona a ClassPolicy)")
	caps := fs.String("caps", "", "capabilities CSV que o utilizador possui (a autoridade é a intersecção com a classe)")
	ttl := fs.Duration("ttl", 15*time.Minute, "TTL do token")
	authMethod := fs.String("auth-method", "manual", "rótulo do método de autenticação humana (binding audit AOS-176; NUNCA PII)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *human == "" || *agent == "" || *class == "" {
		return errors.New("mint exige --human, --agent e --class")
	}
	scope := splitCSV(*caps)

	priv, err := loadOrCreateKey(*keyFile)
	if err != nil {
		return err
	}
	// A via de custódia externa (AOS-175): o Issuer assina ATRAVÉS de um crypto.Signer. Aqui o
	// signer é a ed25519.PrivateKey do ficheiro; em produção seria um adaptador HSM/KMS cuja
	// chave nunca entra no processo. O nó continua a verificar só com a pubkey.
	iss, err := identity.NewIssuerWithSigner(*issuerID, priv, map[string]identity.ClassPolicy{
		*class: {TTL: *ttl, Scope: scope},
	})
	if err != nil {
		return fmt.Errorf("construir issuer: %w", err)
	}
	tok, err := iss.Issue(context.Background(), identity.IssueRequest{
		UserID:        *human,
		AgentID:       *agent,
		AgentClass:    *class,
		PolicyRef:     "policy://" + *class,
		UserAuthority: scope,
		AuthMethod:    *authMethod,
	})
	if err != nil {
		return fmt.Errorf("mint: %w", err)
	}
	_, err = fmt.Fprintln(out, tok.Compact)
	return err
}

// loadOrCreateKey carrega a seed ed25519 (32 bytes em hex) do ficheiro; se não existir, gera uma
// e grava-a com permissões 0600. É a via de DEV: a chave privada vive num ficheiro que o operador
// controla, FORA do processo do nó. Em PRODUÇÃO, substituir por um crypto.Signer HSM/KMS (AOS-175)
// — a chave nunca entra em processo nenhum. NUNCA se ecoa o material da chave.
func loadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		seed, derr := hex.DecodeString(strings.TrimSpace(string(raw)))
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
