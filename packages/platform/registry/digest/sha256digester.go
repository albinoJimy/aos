package digest

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/aos-ref/platform/registry/domain"
)

// Prefix marca EXPLICITAMENTE um digest como SHA-256 canonicalizado de AOS-047
// (substitui o "placeholder-fnv1a:" de AOS-045). O prefixo agnostiza o
// algoritmo no campo digest e permite distinguir gerações de hashing.
const Prefix = "sha256:"

// SHA256Digester é o Digester real de AOS-047: o digest de um artefacto é o
// SHA-256 do seu conteúdo CANONICALIZADO (contrato público — kind, egress,
// schemas de I/O em JSON canónico, scopes ordenados). Implementa
// [domain.Digester] e é injectável via registry.WithDigester, substituindo o
// PlaceholderDigester sem alterar a API do REG.
//
// É DETERMINÍSTICO e PURO: o mesmo (kind, contract) produz sempre o mesmo
// digest; sem estado, sem relógio, seguro para uso concorrente.
type SHA256Digester struct{}

// Digest implementa [domain.Digester]: SHA-256 hex-encoded, com [Prefix].
func (SHA256Digester) Digest(kind domain.ArtifactKind, c domain.Contract) string {
	sum := sha256.Sum256(canonicalContract(kind, c))
	return Prefix + hex.EncodeToString(sum[:])
}

// DigestJSON calcula o digest de um documento JSON (schema da tool ou manifesto
// de capabilities) sobre a sua forma CANÓNICA — ordem de chaves e whitespace
// irrelevantes. Fail-closed: JSON inválido devolve [ErrInvalidJSON] (o chamador
// não deve pinar conteúdo malformado). Reutilizável por AOS-050/051.
func DigestJSON(raw []byte) (string, error) {
	canon, err := CanonicalJSON(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return Prefix + hex.EncodeToString(sum[:]), nil
}

// DigestBytes calcula o digest de um conteúdo binário opaco (ex.: o binário de
// um servidor MCP STDIO) — SHA-256 dos bytes CRUS, sem canonicalização (um
// binário não tem estrutura JSON a normalizar). Reutilizável por AOS-050/051.
func DigestBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return Prefix + hex.EncodeToString(sum[:])
}

// Compare é a COMPARAÇÃO de digest usada na resolução e na revalidação: devolve
// nil se computed == expected e [ErrDigestMismatch] caso contrário. É a peça
// fail-closed que BLOQUEIA a admissão de conteúdo adulterado (o digest calculado
// não coincide com o esperado no REG). Reutilizável tal-e-qual pela revalidação
// por chamada (AOS-051) e pelo congelamento por run (AOS-050).
//
// Um esperado VAZIO é sempre uma divergência (nunca se aceita "sem digest" como
// coincidência — fail-closed no caminho de ausência).
func Compare(expected, computed string) error {
	if expected == "" || expected != computed {
		return &MismatchError{Expected: expected, Computed: computed}
	}
	return nil
}
