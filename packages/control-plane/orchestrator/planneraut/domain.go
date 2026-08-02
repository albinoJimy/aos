package planneraut

import (
	"sort"
	"strings"
)

// DomainKey identifica um DOMÍNIO à granularidade DECLARADA de AOS-242: o par
// (tenant, ASSINATURA estrutural de capabilities/papéis). É COMPARÁVEL (chave de
// mapa). A assinatura deriva SÓ de rótulos ESTRUTURAIS (classes de capability /
// papéis do organigrama) — NUNCA do `objective` de texto livre untrusted (ADR-005),
// que não é entrada de nenhuma decisão de autonomia.
type DomainKey struct {
	// TenantID é o tenant soberano do domínio.
	TenantID string
	// Signature é o conjunto NORMALIZADO (ordenado, sem vazios, junto por NUL) das
	// classes de capability/papéis do pedido. Dois objectivos com o mesmo conjunto
	// de classes — em qualquer ordem — partilham assinatura, logo domínio.
	Signature string
}

// signatureSep separa as classes na assinatura. NUL nunca ocorre num rótulo de
// classe estrutural, pelo que a junção é injectiva (sem colisão por concatenação).
const signatureSep = "\x00"

// NewDomainKey constrói a chave de domínio à granularidade declarada: normaliza as
// classes de capability (descarta vazias, ORDENA, junta) numa assinatura estável.
// A ordenação torna o domínio INSENSÍVEL à ordem em que as classes chegam — a
// recorrência é medida sobre o CONJUNTO, não sobre a sequência. Puro e
// determinístico.
func NewDomainKey(tenantID string, capabilityClasses ...string) DomainKey {
	cleaned := make([]string, 0, len(capabilityClasses))
	for _, c := range capabilityClasses {
		if c != "" {
			cleaned = append(cleaned, c)
		}
	}
	sort.Strings(cleaned)
	return DomainKey{TenantID: tenantID, Signature: strings.Join(cleaned, signatureSep)}
}

// IsZero indica se a chave é o valor-zero (sem tenant nem assinatura).
func (k DomainKey) IsZero() bool { return k.TenantID == "" && k.Signature == "" }

// String rende a chave de forma legível para audit (nunca carrega texto livre — só
// tenant e assinatura estrutural).
func (k DomainKey) String() string {
	return k.TenantID + "/[" + k.Signature + "]"
}
