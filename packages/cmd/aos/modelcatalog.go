package main

// AOS_MODEL_TOOLS_REGISTER — REGISTA as tools de AOS_MODEL_TOOLS como um CATÁLOGO ASSINADO E
// CONGELÁVEL, para que a REVALIDAÇÃO do Reference Monitor (AOS-051, o 2.º hook da cadeia) as
// ADMITA. Sem isto, uma tool oferecida ao modelo mas SEM contrato assinado registado é recusada
// pela revalidação (trust store vazio ⇒ ReasonNotFrozen) ANTES de a decisão chegar ao PDP. Com
// isto, a revalidação passa e a decisão passa ao GATE SEGUINTE — o PDP/Cedar (3.º hook) — que
// avalia a Capability da tool (ex.: cap:http.post) sob o taint da autorização. Uma tool call
// originada pelo modelo tem taint=untrusted, pelo que a regra Cedar `allow_http_post`
// (`context.taint != "untrusted"`) NÃO dá permit → deny no PDP (taint-gate, P4). Ou seja: registar
// o contrato assinado MOVE a negação da revalidação para o taint-gate Cedar.
//
// DEV-GRADE (auto-assinado): o nó gera uma chave ed25519 EFÉMERA ao arranque, assina o catálogo com
// ela e confia nela (trust store in-process). É o análogo, no eixo do registry, da identidade
// demo-only self-minted até ao D4: prova o CAMINHO de mediação sem a autoridade externa. Em
// produção o catálogo é assinado OUT-OF-BAND por um publicador confiável (o publisher da entry) e a
// pubkey é forçada por config — nunca a própria chave do nó. O objectivo deste seam é exercitar a
// governança, não substituir a cadeia de supply-chain assinada.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	integration "github.com/aos-ref/integration"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/signing"
	"github.com/aos-ref/platform/registry/toolset"
)

// modelToolRegistryKeyID nomeia a chave de dev (efémera) que assina o catálogo. Não é segredo — é
// só um rótulo; a chave PRIVADA vive-e-morre no processo e a pública entra no trust store local.
const modelToolRegistryKeyID = "key:aos-node-dev-tool-registry"

// ErrBadModelToolsRegister — AOS_MODEL_TOOLS_REGISTER está ligado mas o registry não pôde ser
// composto (egress inválido num spec, ou AOS_MODEL_TOOLS ausente/mal formado). Fail-closed.
var ErrBadModelToolsRegister = errors.New("aos: AOS_MODEL_TOOLS_REGISTER ligado mas o catalogo assinado nao pode ser composto — exige AOS_MODEL_TOOLS bem formado; cada `egress`, se presente, tem de ser none|internal|external")

// modelToolCatalog implementa [toolset.Catalog]: devolve as entries assinadas do registry. É a
// MESMA fonte para o freeze do run (req.Frozen) e para o resolve da definição actual (req.Current).
type modelToolCatalog struct{ entries []domain.Entry }

func (c modelToolCatalog) ActiveEntries(context.Context) ([]domain.Entry, error) {
	return append([]domain.Entry(nil), c.entries...), nil
}

// parseEgressClass mapeia a string do spec para a EgressClass canónica. Vazio ⇒ none.
func parseEgressClass(s string) (domain.EgressClass, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none":
		return domain.EgressNone, nil
	case "internal":
		return domain.EgressInternal, nil
	case "external":
		return domain.EgressExternal, nil
	default:
		return "", fmt.Errorf("%w: egress %q", ErrBadModelToolsRegister, s)
	}
}

// egressRank ordena as classes para computar o MaxEgress da policy (espelha revalidation.egressRank).
func egressRank(e domain.EgressClass) int {
	switch e {
	case domain.EgressExternal:
		return 2
	case domain.EgressInternal:
		return 1
	default:
		return 0
	}
}

// buildSignedToolRegistryFromEnv compõe, quando AOS_MODEL_TOOLS_REGISTER está ligado, o catálogo
// ASSINADO + o Revalidator (com a pubkey do assinante no trust store) + a Policy de revalidação a
// partir dos specs de AOS_MODEL_TOOLS. Devolve (nil,nil,nil,nil) quando o registo está desligado ou
// não há tools — o nó fica com o catálogo/revalidador de referência (comportamento inalterado).
func buildSignedToolRegistryFromEnv() (toolset.Catalog, *revalidation.Revalidator, integration.PolicyProvider, error) {
	if !parseModelToolsRegister(os.Getenv("AOS_MODEL_TOOLS_REGISTER")) {
		return nil, nil, nil, nil
	}
	specs, err := readModelToolSpecs()
	if err != nil {
		return nil, nil, nil, err
	}
	if len(specs) == 0 {
		// Registo pedido mas sem tools para registar ⇒ fail-closed (config incoerente).
		return nil, nil, nil, fmt.Errorf("%w: AOS_MODEL_TOOLS vazio/ausente", ErrBadModelToolsRegister)
	}

	// Assinante dev EFÉMERO: a chave privada nunca sai do processo; a pública entra no trust store.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: gerar chave: %v", ErrBadModelToolsRegister, err)
	}
	signer, err := signing.NewSigner(modelToolRegistryKeyID, priv)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: signer: %v", ErrBadModelToolsRegister, err)
	}

	entries := make([]domain.Entry, 0, len(specs))
	scopeSet := map[string]struct{}{}
	maxEgress := domain.EgressNone
	ver := domain.Version{Major: 1, Minor: 0, Patch: 0}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, s := range specs {
		egress, eerr := parseEgressClass(s.Egress)
		if eerr != nil {
			return nil, nil, nil, eerr
		}
		contract := domain.Contract{
			InputSchema:      s.Parameters,
			CredentialScopes: append([]string(nil), s.CredentialScopes...),
			Egress:           egress,
		}
		dig := digest.SHA256Digester{}.Digest(domain.KindTool, contract)
		name := strings.TrimSpace(s.Name)
		entries = append(entries, domain.Entry{
			ID:        name,
			Version:   ver,
			Kind:      domain.KindTool,
			Digest:    dig,
			Signature: signer.Sign(name, ver, dig),
			Contract:  contract,
			Provenance: domain.Provenance{
				Origin:    "aos-node:AOS_MODEL_TOOLS",
				Publisher: signer.KeyID(),
				Timestamp: now,
				Trust:     domain.TrustFirstSeen,
			},
			Status: domain.StatusActive,
		})
		for _, sc := range s.CredentialScopes {
			scopeSet[sc] = struct{}{}
		}
		if egressRank(egress) > egressRank(maxEgress) {
			maxEgress = egress
		}
	}

	auditStore := audit.NewMemStore()
	trust, err := signing.NewTrustStore(auditStore)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: trust store: %v", ErrBadModelToolsRegister, err)
	}
	if err := trust.Add(context.Background(), signer.KeyID(), signer.PublicKey()); err != nil {
		return nil, nil, nil, fmt.Errorf("%w: trust add: %v", ErrBadModelToolsRegister, err)
	}
	revalidator, err := revalidation.New(trust, auditStore)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: revalidator: %v", ErrBadModelToolsRegister, err)
	}

	allowedScopes := make([]string, 0, len(scopeSet))
	for sc := range scopeSet {
		allowedScopes = append(allowedScopes, sc)
	}
	// Policy da revalidação: admite os scopes declarados pelas tools e o egress máximo entre elas.
	// NÃO é a fronteira de autorização (essa é o PDP/Cedar sobre a Capability) — só o gate de
	// supply-chain (o contrato não pode pedir mais scope/egress do que o run permite).
	policy := integration.StaticPolicy{AllowedScopes: allowedScopes, MaxEgress: maxEgress}
	return modelToolCatalog{entries: entries}, revalidator, policy, nil
}

// parseModelToolsRegister interpreta o booleano de AOS_MODEL_TOOLS_REGISTER (mesma gramática dos
// outros toggles do nó). Vazio/desconhecido ⇒ false (desligado, fail-safe).
func parseModelToolsRegister(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "t", "yes", "y", "on":
		return true
	default:
		return false
	}
}
