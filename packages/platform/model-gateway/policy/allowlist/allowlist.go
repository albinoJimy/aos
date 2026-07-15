// Package allowlist é a ALLOWLIST REGIONAL por BOARD do Model Gateway (AOS-058,
// tecnica/06 §5, EPIC-09, ADR-011). Substitui o estágio allowlist pass-through de
// AOS-055 pela guarda REAL de soberania de dados imposta por desenho: cada board
// (unidade de tenancy/soberania) declara o conjunto de (modelo, região) que lhe é
// legalmente permitido, e um (board, modelo, região) NÃO explicitamente permitido
// é recusado FAIL-CLOSED (default-deny) — nunca allow por omissão.
//
// # Policy-as-code versionada e ASSINADA (ADR-011)
//
// A allowlist vive num ficheiro VERSIONADO (allowlist_policy.json), embebido no
// binário via go:embed, com:
//
//   - um DIGEST de conteúdo (sha256 canónico) que a torna tamper-evident:
//     [Policy.Version] devolve "versão#digest", selado no audit WORM em cada
//     decisão — a decisão fica ligada à versão EXACTA da política em vigor;
//   - uma ASSINATURA ed25519 (crypto/ed25519 stdlib) sobre esse digest. O
//     carregamento VERIFICA a assinatura contra a chave PÚBLICA de confiança
//     embebida; uma policy com assinatura inválida FALHA fail-closed
//     ([ErrSignatureInvalid]) — o gateway recusa arrancar sobre política adulterada
//     ou não-assinada. A chave PRIVADA de assinatura NUNCA entra no runtime nem no
//     repositório do binário (ADR-006): assina-se offline; só a pública e a
//     assinatura são embebidas.
//
// # Trust anchor PINADO em código (não-forjável a partir do repo)
//
// A confiança na assinatura vale exactamente o que valer a chave PÚBLICA embebida:
// se um atacante puder TROCAR allowlist_policy.pub por uma pública sua (e reassinar
// a policy adulterada com a privada correspondente), o controlo é teatro. Para o
// impedir, o TRUST ANCHOR é a pública cujo fingerprint (sha256) está PINADO e
// revisto em código ([trustAnchorFingerprint]): [LoadPolicy] recusa fail-closed
// ([ErrTrustAnchorMismatch]) qualquer allowlist_policy.pub cujo fingerprint não
// bata com a constante revista. A privada correspondente foi gerada OFFLINE com
// entropia real (crypto/rand) e NUNCA é reconstrutível a partir do repositório (não
// deriva de nenhuma seed/etiqueta pública) — trocar o ficheiro .pub deixa de chegar:
// é preciso alterar o anchor revisto em código (mudança auditável por code-review) E
// deter a chave privada custodiada offline.
//
// A semântica default-deny é idêntica à do cedar/PDP (sem permit explícito ⇒
// deny); é expressa aqui como policy-as-code equivalente, versionada, assinada e
// testada (allow/deny), mantendo o data-plane do gateway ZERO-dep — a mesma opção
// coerente com AOS-057 (pipeline/authn).
package allowlist

import (
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

//go:embed allowlist_policy.json
var embeddedPolicy []byte

//go:embed allowlist_policy.sig
var embeddedSig []byte

//go:embed allowlist_policy.pub
var embeddedPub []byte

// wildcard corresponde a qualquer valor num campo de correspondência da regra.
const wildcard = "*"

// trustAnchorFingerprint é o SHA-256 (hex minúsculo) da chave PÚBLICA de confiança
// da allowlist regional — o TRUST ANCHOR, pinado e revisto EM CÓDIGO. A privada
// correspondente foi gerada offline com entropia real (crypto/rand) e é custodiada
// fora do repositório (ADR-006); NÃO deriva de nenhuma seed/etiqueta pública, logo
// não é reconstrutível a partir do repo. [LoadPolicy] exige que o fingerprint da
// pública embebida (allowlist_policy.pub) bata EXACTAMENTE com esta constante:
// trocar o ficheiro .pub por outra chave é rejeitado fail-closed
// ([ErrTrustAnchorMismatch]), pois não bate com o anchor revisto aqui. Rodar a chave
// exige uma alteração auditável desta constante (code-review) + a nova assinatura
// produzida offline (ver gen_signature.go).
const trustAnchorFingerprint = "6be7ec9e530ee0b0fb6400e2dbc19b6187a8ef13cf0d309f2c0a1503d20c1698"

// Effect é o veredicto da allowlist.
type Effect string

const (
	// EffectAllow — existe uma regra do board que permite (modelo, região).
	EffectAllow Effect = "allow"
	// EffectDeny — nenhuma regra aplicável (default-deny) ou default explícito.
	EffectDeny Effect = "deny"
)

// Erros fail-closed do carregamento/avaliação.
var (
	// ErrPolicyMalformed — o documento é inválido ou o seu default não é "deny".
	// Uma allowlist cujo default não seja deny é REJEITADA (fail-closed): o gateway
	// recusa arrancar com uma política fail-open.
	ErrPolicyMalformed = errors.New("allowlist: policy regional malformada (default tem de ser deny)")
	// ErrSignatureInvalid — a assinatura ed25519 da policy não verifica contra a
	// chave pública de confiança. Fail-closed: política adulterada/não-assinada é
	// recusada no carregamento.
	ErrSignatureInvalid = errors.New("allowlist: assinatura da policy regional invalida (fail-closed)")
	// ErrPubKeyInvalid — a chave pública de confiança é inválida (tamanho errado).
	ErrPubKeyInvalid = errors.New("allowlist: chave publica de confianca da policy invalida")
	// ErrTrustAnchorMismatch — a chave pública EMBEBIDA não corresponde ao trust
	// anchor pinado em código ([trustAnchorFingerprint]). Fail-closed: trocar
	// allowlist_policy.pub por outra chave (para reassinar uma policy adulterada) é
	// recusado no carregamento — a raiz de confiança é a constante revista, não o
	// ficheiro embebido.
	ErrTrustAnchorMismatch = errors.New("allowlist: chave publica embebida nao corresponde ao trust anchor pinado (fail-closed)")
)

// Rule é uma regra da allowlist de UM board: os modelos e as regiões/endpoints
// permitidos dentro da sua fronteira legal. Uma chamada (board, modelo, região) é
// permitida por esta regra se o board corresponder (exacto ou "*") E o modelo
// estiver em Models E a região estiver em Regions (valor exacto ou "*").
type Rule struct {
	ID      string   `json:"id"`
	Board   string   `json:"board"`
	Models  []string `json:"models"`
	Regions []string `json:"regions"`
}

// Policy é a allowlist regional carregada, versionada e verificada.
type Policy struct {
	VersionTag string `json:"version"`
	Default    string `json:"default"`
	Rules      []Rule `json:"rules"`

	digest string // sha256 hex do conteúdo canónico (integridade/versão/material assinado)
}

// Input é o pedido de decisão da allowlist: o board (soberania), o modelo alvo e a
// região pedida.
type Input struct {
	Board  string
	Model  string
	Region string
}

// LoadPolicy carrega a allowlist EMBEBIDA (allowlist_policy.json), verificando a
// assinatura ed25519 contra a chave pública de confiança embebida. Antes disso PINA
// o trust anchor: a pública embebida tem de bater com [trustAnchorFingerprint]
// (fail-closed [ErrTrustAnchorMismatch]) — trocar allowlist_policy.pub por outra
// chave não permite reassinar a policy. Fail-closed também se malformada, se o
// default não for deny, ou se a assinatura não verificar.
func LoadPolicy() (*Policy, error) {
	pubB64 := strings.TrimSpace(string(embeddedPub))
	if err := verifyTrustAnchor(pubB64); err != nil {
		return nil, err
	}
	return LoadSignedPolicy(embeddedPolicy, strings.TrimSpace(string(embeddedSig)), pubB64)
}

// verifyTrustAnchor confirma que a chave pública embebida é EXACTAMENTE a do trust
// anchor pinado em código (fingerprint sha256). É o gate que torna a assinatura
// não-forjável por troca do ficheiro .pub: sem a privada custodiada offline, produzir
// uma pública que bata com o fingerprint revisto é computacionalmente inviável.
func verifyTrustAnchor(pubB64 string) error {
	raw, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPubKeyInvalid, err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != trustAnchorFingerprint {
		return ErrTrustAnchorMismatch
	}
	return nil
}

// LoadSignedPolicy carrega e valida uma allowlist a partir do JSON, de uma
// assinatura (base64) e de uma chave pública ed25519 (base64) — o caminho de
// verificação usado tanto pelo carregamento embebido como pelos testes. É o ÚNICO
// carregador público: não há caminho que aceite uma policy SEM verificar a
// assinatura (fail-closed por construção). Calcula o digest de conteúdo (versão
// tamper-evident) e impõe o default-deny ANTES de verificar a assinatura sobre
// esse digest.
func LoadSignedPolicy(policyJSON []byte, signatureB64, pubKeyB64 string) (*Policy, error) {
	pub, err := decodePubKey(pubKeyB64)
	if err != nil {
		return nil, err
	}
	p, err := parse(policyJSON)
	if err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return nil, fmt.Errorf("%w: assinatura nao-base64: %v", ErrSignatureInvalid, err)
	}
	// A assinatura cobre o DIGEST canónico (material assinado). Uma alteração de
	// qualquer regra muda o digest e invalida a assinatura — tamper-evident.
	if !ed25519.Verify(pub, []byte(p.digest), sig) {
		return nil, ErrSignatureInvalid
	}
	return p, nil
}

// Digest devolve o digest canónico (sha256 hex) do documento de policy SEM
// verificar assinatura. É o MATERIAL ASSINADO — usado offline para produzir a
// assinatura embebida e nos testes para (re)assinar. Não é um carregador: não
// devolve uma Policy utilizável (essa exige verificação de assinatura).
func Digest(policyJSON []byte) (string, error) {
	p, err := parse(policyJSON)
	if err != nil {
		return "", err
	}
	return p.digest, nil
}

// parse desserializa e valida a estrutura (default-deny) e calcula o digest —
// SEM verificar assinatura (uso interno partilhado por [LoadSignedPolicy]/[Digest]).
func parse(policyJSON []byte) (*Policy, error) {
	var p Policy
	if err := json.Unmarshal(policyJSON, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPolicyMalformed, err)
	}
	if p.Default != string(EffectDeny) {
		return nil, ErrPolicyMalformed
	}
	if p.VersionTag == "" {
		return nil, ErrPolicyMalformed
	}
	p.digest = canonicalDigest(&p)
	return &p, nil
}

// decodePubKey descodifica a chave pública ed25519 (base64) e valida o tamanho.
func decodePubKey(pubKeyB64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPubKeyInvalid, err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, ErrPubKeyInvalid
	}
	return ed25519.PublicKey(raw), nil
}

// Version devolve a identidade VERSIONADA e tamper-evident da allowlist:
// "versão#digest12". É o valor selado no audit WORM em cada decisão para ligar a
// decisão à versão EXACTA da política em vigor.
func (p *Policy) Version() string {
	return p.VersionTag + "#" + p.digest[:12]
}

// Evaluate decide allow/deny para (board, modelo, região). DEFAULT-DENY: só
// devolve allow se existir uma regra do board que permita explicitamente o par
// (modelo, região); a ausência de correspondência é deny (fail-closed).
//
// A REGIÃO é uma fronteira de SOBERANIA: ao contrário de board/modelo, NÃO admite
// wildcard (ver [regionMatches]). Uma regra com regions:["*"] não autoriza qualquer
// região — seria um escape fail-open cross-border dentro de uma política default-deny
// e divergiria de [AllowedRegions] (que ignora "*"). Uma região pedida vazia também
// nunca corresponde (jurisdição indefinida ⇒ deny fail-closed).
func (p *Policy) Evaluate(in Input) Effect {
	for _, r := range p.Rules {
		if !boardMatches(r.Board, in.Board) {
			continue
		}
		if !matches(r.Models, in.Model) {
			continue
		}
		if !regionMatches(r.Regions, in.Region) {
			continue
		}
		return EffectAllow
	}
	return EffectDeny
}

// AllowedRegions devolve o conjunto ORDENADO de regiões que a allowlist permite ao
// par (board, modelo) — a FRONTEIRA DE SOBERANIA legal desse board para esse
// modelo. A guarda de failover (routing/sovereignty) consome-o para restringir os
// candidatos de failover às regiões legalmente permitidas. Um wildcard de região
// numa regra NÃO expande a fronteira (uma fronteira "qualquer região" não é uma
// fronteira de soberania): wildcards são ignorados aqui.
func (p *Policy) AllowedRegions(board, model string) []string {
	set := make(map[string]struct{})
	for _, r := range p.Rules {
		if !boardMatches(r.Board, board) || !matches(r.Models, model) {
			continue
		}
		for _, reg := range r.Regions {
			if reg != wildcard && reg != "" {
				set[reg] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for reg := range set {
		out = append(out, reg)
	}
	sort.Strings(out)
	return out
}

// boardMatches indica se o board da regra corresponde ao board pedido (valor
// exacto ou "*"). Um board de regra vazio NÃO corresponde a nada (fail-closed:
// uma regra sem board declarado nunca autoriza).
func boardMatches(ruleBoard, board string) bool {
	if ruleBoard == "" {
		return false
	}
	return ruleBoard == wildcard || ruleBoard == board
}

// matches indica se value corresponde a algum elemento de list (valor exacto ou
// wildcard "*"). Uma lista vazia NÃO corresponde a nada (fail-closed). Usado para
// board/modelo, onde o wildcard é uma escolha legítima de produto — NUNCA para a
// região (ver [regionMatches]).
func matches(list []string, value string) bool {
	for _, item := range list {
		if item == wildcard || item == value {
			return true
		}
	}
	return false
}

// regionMatches indica se region corresponde a alguma das regiões da regra por valor
// EXACTO. Ao contrário de [matches], IGNORA o wildcard "*" (uma fronteira "qualquer
// região" não é uma fronteira de soberania) e uma região vazia (jurisdição indefinida
// ⇒ fail-closed). É coerente com [AllowedRegions], que também ignora "*"/vazios — os
// dois consumidores da mesma policy (estágio allowlist e guarda de failover) usam
// assim a MESMA semântica de fronteira.
func regionMatches(regions []string, region string) bool {
	if region == "" {
		return false
	}
	for _, item := range regions {
		if item != wildcard && item == region {
			return true
		}
	}
	return false
}

// canonicalDigest calcula um sha256 determinista do CONTEÚDO da allowlist
// (versão, default e regras normalizadas), independente de espaços/ordem do JSON
// original. É a base da versão tamper-evident E o material assinado.
func canonicalDigest(p *Policy) string {
	type canonRule struct {
		ID      string   `json:"id"`
		Board   string   `json:"board"`
		Models  []string `json:"models"`
		Regions []string `json:"regions"`
	}
	rules := make([]canonRule, 0, len(p.Rules))
	for _, r := range p.Rules {
		rules = append(rules, canonRule{
			ID:      r.ID,
			Board:   r.Board,
			Models:  sortedCopy(r.Models),
			Regions: sortedCopy(r.Regions),
		})
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	canon := struct {
		Version string      `json:"version"`
		Default string      `json:"default"`
		Rules   []canonRule `json:"rules"`
	}{Version: p.VersionTag, Default: p.Default, Rules: rules}
	b, _ := json.Marshal(canon)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
