package domain

import "encoding/json"

// EgressClass é a classe de egress declarada no contrato de uma capability — a
// fronteira de rede que o artefacto declara precisar. É parte do contrato público
// (ADR-005/006): o RM e a allowlist de egress (EPIC-07) avaliam-na antes do
// despacho. Aqui é apenas declarada e canonicalizada no digest; a sua imposição em
// runtime é dos tickets de segurança seguintes.
type EgressClass string

const (
	// EgressNone — o artefacto não faz egress de rede (ex.: transformação pura).
	EgressNone EgressClass = "none"
	// EgressInternal — egress limitado a endpoints internos/allowlist.
	EgressInternal EgressClass = "internal"
	// EgressExternal — egress para a Internet pública (a classe de maior risco).
	EgressExternal EgressClass = "external"
)

// Valid indica se e é uma das classes canónicas (fail-closed).
func (e EgressClass) Valid() bool {
	switch e {
	case EgressNone, EgressInternal, EgressExternal:
		return true
	default:
		return false
	}
}

// Contract é o CONTRATO PÚBLICO de capability de um artefacto (tecnica/05 §3): o
// schema de I/O, os scopes de credencial que a capability pode pedir e a classe de
// egress. É a âncora de compatibilidade do SemVer (uma mudança incompatível deste
// contrato exige um bump MAJOR — AOS-052) e o conteúdo primário sobre o qual o
// digest é calculado.
//
// Os CredentialScopes são apenas a DECLARAÇÃO do que a capability pode pedir; a
// injecção do segredo é server-side pelo Credential Broker JIT (ADR-006) — o
// artefacto nunca vê o segredo em claro, e nenhum segredo é armazenado aqui.
type Contract struct {
	// InputSchema é o schema (JSON) da entrada da capability. Opaco ao REG.
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	// OutputSchema é o schema (JSON) da saída da capability. Opaco ao REG.
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	// CredentialScopes são os scopes de credencial que a capability declara pedir
	// (ex.: "vault:db.read"). NUNCA contêm segredos — só a declaração.
	CredentialScopes []string `json:"credential_scopes,omitempty"`
	// Egress é a classe de egress declarada.
	Egress EgressClass `json:"egress"`
	// ManifestDigest é o digest do MANIFESTO DE CAPACIDADES do artefacto, para os
	// tipos cuja superfície de capacidade NÃO cabe nos schemas de I/O — hoje só
	// kind=mcp_server (AOS-320), cujo contrato não tem schemas próprios (os schemas
	// das tools descobertas vivem em entradas kind=tool separadas) e cuja identidade
	// real é o par (âncora local de transporte/endpoint, superfície anunciada).
	// Sem este campo o digest de um mcp_server seria uma constante da classe de
	// egress — três valores para todo o universo de servidores MCP.
	//
	// É um digest JÁ CALCULADO (string opaca com prefixo de algoritmo), não o
	// manifesto: o REG pina a impressão digital, nunca o conteúdo untrusted.
	//
	// VAZIO nos artefactos sem manifesto (tool/skill) — e, quando vazio, NÃO entra
	// na canonicalização ([Canonicalize] e digest.canonicalContract), o que preserva
	// byte-a-byte os digests de todas as entradas anteriores a AOS-320.
	ManifestDigest string `json:"manifest_digest,omitempty"`
}

// clone devolve uma cópia profunda do contrato para que o estado do catálogo nunca
// partilhe slices/bytes mutáveis com o chamador (imutabilidade das entradas).
func (c Contract) clone() Contract {
	cp := Contract{Egress: c.Egress, ManifestDigest: c.ManifestDigest}
	if c.InputSchema != nil {
		cp.InputSchema = make(json.RawMessage, len(c.InputSchema))
		copy(cp.InputSchema, c.InputSchema)
	}
	if c.OutputSchema != nil {
		cp.OutputSchema = make(json.RawMessage, len(c.OutputSchema))
		copy(cp.OutputSchema, c.OutputSchema)
	}
	if c.CredentialScopes != nil {
		cp.CredentialScopes = make([]string, len(c.CredentialScopes))
		copy(cp.CredentialScopes, c.CredentialScopes)
	}
	return cp
}
