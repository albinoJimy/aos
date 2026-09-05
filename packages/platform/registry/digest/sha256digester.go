package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

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

// BemFormado reporta se `d` tem a FORMA de um digest deste pacote: o prefixo canónico seguido de
// 64 dígitos hexadecimais minúsculos.
//
// Existe porque a verificação de PRESENÇA não chega onde o valor vem de fora (AOS-334): um campo
// de digest preenchido com uma constante qualquer passa um teste de não-vazio e volta a produzir
// o digest-constante-da-classe que o AOS-320 existe para eliminar.
//
// NÃO É PROVENIÊNCIA, e a distinção importa: isto diz que o valor TEM A FORMA de um SHA-256, não
// que foi derivado do conteúdo que diz representar. Quem publica pode fornecer o sha256
// bem-formado de outra coisa. Fechar esse eixo exige assinatura sobre o material observado.
func BemFormado(d string) bool {
	if !strings.HasPrefix(d, Prefix) {
		return false
	}
	hexa := d[len(Prefix):]
	if len(hexa) != 2*sha256.Size {
		return false
	}
	for i := 0; i < len(hexa); i++ {
		c := hexa[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
