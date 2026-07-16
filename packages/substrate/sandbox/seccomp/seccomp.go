// Package seccomp é o perfil SECCOMP mínimo da microVM (AOS-066, EPIC-07, ADR-004).
// Modela a redução da superfície de syscalls exploráveis como uma ALLOWLIST
// VERSIONADA e tamper-evident: só o conjunto MÍNIMO de syscalls necessárias é
// permitido e QUALQUER syscall fora da allowlist é BLOQUEADA por omissão
// (default-deny, fail-closed) — nunca allow por ausência de regra.
//
// # Policy-as-code versionada (molde AOS-058)
//
// O perfil vive num ficheiro VERSIONADO (profile.json), embebido no binário via
// embed, com um DIGEST de conteúdo (sha256 canónico) que o torna tamper-evident:
// [Profile.Version] devolve "versão#digest12" e [Profile.Hash] o digest completo.
// Esse HASH é gravado no MANIFESTO da execução (o evento de ciclo de vida do
// Reference-Monitor-mediated launcher e o span OTel) — ligando cada trajectória à
// versão EXACTA do perfil em vigor, para replay/auditoria. O digest é canónico
// (independente de espaços/ordem no JSON), logo estável call-a-call (determinismo).
//
// # Ambiente sem seccomp REAL
//
// Este ambiente é Windows, sem o BPF seccomp do kernel Linux. O perfil é aqui um
// MODELO verificável: [Profile.Allows] impõe a semântica default-deny em Go puro
// (um verificador que os drivers reais — firecracker/gvisor — traduziriam para o
// filtro BPF equivalente na montagem da microVM). A allowlist e a negação por
// omissão são testadas (allow/deny) sem depender de um kernel Linux.
package seccomp

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

//go:embed profile.json
var embeddedProfile []byte

// Action é a decisão por omissão do perfil. Só "deny" é aceite: um perfil cujo
// default não seja deny é REJEITADO fail-closed (ver [ErrProfileMalformed]).
type Action string

const (
	// ActionDeny — nega a syscall (default do perfil e resultado de qualquer
	// syscall fora da allowlist).
	ActionDeny Action = "deny"
	// ActionAllow — permite a syscall (só as explicitamente listadas).
	ActionAllow Action = "allow"
)

// Erros fail-closed do carregamento/avaliação do perfil.
var (
	// ErrProfileMalformed — o documento é inválido, a versão é vazia, ou o seu
	// default_action não é "deny". Um perfil cujo default não seja deny é REJEITADO
	// (fail-closed): a microVM recusa arrancar sobre um perfil fail-open.
	ErrProfileMalformed = errors.New("seccomp: perfil malformado (default_action tem de ser deny)")

	// ErrEmptySyscall — [Profile.Allows] com syscall vazia. Uma syscall indefinida
	// nunca é permitida (fail-closed).
	ErrEmptySyscall = errors.New("seccomp: nome de syscall vazio")
)

// profileDoc é a forma serializada do perfil (profile.json).
type profileDoc struct {
	Version         string   `json:"version"`
	DefaultAction   string   `json:"default_action"`
	AllowedSyscalls []string `json:"allowed_syscalls"`
}

// Profile é o perfil seccomp carregado, versionado e verificável. É IMUTÁVEL após
// [Load]/[Parse]: a allowlist é copiada e o digest calculado uma vez. Seguro para
// leitura concorrente (sem estado mutável) — o -race guarda [Profile.Allows] em
// paralelo.
type Profile struct {
	versionTag string
	def        Action
	allowed    map[string]struct{}
	ordered    []string // allowlist ordenada (introspecção/digest determinista)
	digest     string   // sha256 hex do conteúdo canónico (versão tamper-evident)
}

// Load carrega o perfil EMBEBIDO (profile.json). Fail-closed se o perfil estiver
// malformado ou o seu default não for deny — a microVM nunca corre sem um perfil
// seccomp default-deny válido.
func Load() (*Profile, error) {
	return Parse(embeddedProfile)
}

// Parse desserializa e valida um perfil a partir do JSON, impõe o default-deny e
// calcula o digest canónico (versão tamper-evident). É o único construtor: não há
// caminho que produza um [Profile] sem validar o default-deny (fail-closed por
// construção).
func Parse(data []byte) (*Profile, error) {
	var doc profileDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProfileMalformed, err)
	}
	if doc.Version == "" {
		return nil, ErrProfileMalformed
	}
	// FAIL-CLOSED: o default TEM de ser deny. Um perfil allow-por-omissão é recusado.
	if Action(doc.DefaultAction) != ActionDeny {
		return nil, ErrProfileMalformed
	}
	allowed := make(map[string]struct{}, len(doc.AllowedSyscalls))
	for _, s := range doc.AllowedSyscalls {
		if s == "" {
			// Uma entrada vazia na allowlist é malformada (nunca autorizaria nada útil
			// e mascararia um erro de edição do perfil).
			return nil, ErrProfileMalformed
		}
		allowed[s] = struct{}{}
	}
	ordered := make([]string, 0, len(allowed))
	for s := range allowed {
		ordered = append(ordered, s)
	}
	sort.Strings(ordered)
	p := &Profile{
		versionTag: doc.Version,
		def:        ActionDeny,
		allowed:    allowed,
		ordered:    ordered,
	}
	p.digest = canonicalDigest(doc.Version, ordered)
	return p, nil
}

// Allows decide se a syscall é permitida. DEFAULT-DENY: devolve true APENAS se a
// syscall estiver explicitamente na allowlist; qualquer syscall fora da allowlist
// (ou vazia) é bloqueada (false). Não há caminho que permita por omissão.
func (p *Profile) Allows(syscall string) bool {
	if syscall == "" {
		return false
	}
	_, ok := p.allowed[syscall]
	return ok
}

// Decision devolve a [Action] para a syscall (allow se listada, senão deny). É a
// forma explícita de [Profile.Allows] para quem regista a decisão.
func (p *Profile) Decision(syscall string) Action {
	if p.Allows(syscall) {
		return ActionAllow
	}
	return ActionDeny
}

// Default devolve a acção por omissão do perfil (sempre [ActionDeny]).
func (p *Profile) Default() Action { return p.def }

// VersionTag devolve a etiqueta de versão declarada (ex.: "sbx-seccomp/v1").
func (p *Profile) VersionTag() string { return p.versionTag }

// Version devolve a identidade VERSIONADA e tamper-evident do perfil:
// "versão#digest12". É o valor legível gravado no manifesto/span para ligar a
// execução à versão EXACTA do perfil em vigor.
func (p *Profile) Version() string {
	return p.versionTag + "#" + p.digest[:12]
}

// Hash devolve o digest sha256 COMPLETO (hex) do conteúdo canónico do perfil — o
// HASH gravado no MANIFESTO da execução (evento de ciclo de vida + span OTel). É
// estável call-a-call e muda se qualquer syscall/versão for alterada (tamper-evident).
func (p *Profile) Hash() string { return p.digest }

// AllowedSyscalls devolve uma CÓPIA ordenada da allowlist (introspecção/testes). O
// chamador não pode mutar o estado interno do perfil.
func (p *Profile) AllowedSyscalls() []string {
	out := make([]string, len(p.ordered))
	copy(out, p.ordered)
	return out
}

// canonicalDigest calcula um sha256 determinista sobre a versão e a allowlist
// ORDENADA — independente de espaços/ordem no JSON original. É a base da versão
// tamper-evident e do hash do manifesto. Stdlib apenas.
func canonicalDigest(version string, orderedSyscalls []string) string {
	h := sha256.New()
	var lenbuf [8]byte
	writeField := func(s string) {
		putUint64(lenbuf[:], uint64(len(s)))
		_, _ = h.Write(lenbuf[:])
		_, _ = h.Write([]byte(s))
	}
	writeField("version")
	writeField(version)
	writeField("default_action")
	writeField(string(ActionDeny))
	putUint64(lenbuf[:], uint64(len(orderedSyscalls)))
	_, _ = h.Write(lenbuf[:])
	for _, s := range orderedSyscalls {
		writeField(s)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// putUint64 serializa n em big-endian em b[:8] (comprimento-prefixo do digest,
// evita ambiguidade de fronteira entre campos concatenados).
func putUint64(b []byte, n uint64) {
	b[0] = byte(n >> 56)
	b[1] = byte(n >> 48)
	b[2] = byte(n >> 40)
	b[3] = byte(n >> 32)
	b[4] = byte(n >> 24)
	b[5] = byte(n >> 16)
	b[6] = byte(n >> 8)
	b[7] = byte(n)
}
