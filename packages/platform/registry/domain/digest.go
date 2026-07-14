package domain

import (
	"encoding/binary"
	"hash/fnv"
	"sort"
	"strconv"
)

// Canonicalize produz uma serialização DETERMINÍSTICA e reproduzível do conteúdo
// que identifica um artefacto — o seu tipo e o seu contrato público (schema de I/O,
// scopes de credencial, classe de egress). É a base do digest: o mesmo conteúdo
// produz sempre exactamente os mesmos bytes, e qualquer mudança mínima produz bytes
// diferentes.
//
// Regras de canonicalização (estáveis por desenho):
//   - campos escritos por ordem fixa, cada um precedido do seu comprimento (u32
//     big-endian) para que fronteiras de campo nunca colidam por concatenação;
//   - os CredentialScopes são ORDENADOS antes de serializar (a ordem de declaração
//     não é semântica) e deduplicados de forma estável.
//
// PONTO DE EXTENSÃO (AOS-047): a canonicalização criptográfica definitiva do
// conteúdo binário/manifesto e o hashing SHA-256 são de AOS-047. Aqui a
// canonicalização cobre o contrato (suficiente para o campo digest desta fundação)
// e o hashing concreto é injectável via Digester.
func Canonicalize(kind ArtifactKind, c Contract) []byte {
	var buf []byte
	writeField := func(b []byte) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(b)))
		buf = append(buf, n[:]...)
		buf = append(buf, b...)
	}

	writeField([]byte(kind))
	writeField([]byte(c.Egress))
	writeField(c.InputSchema)
	writeField(c.OutputSchema)

	scopes := canonicalScopes(c.CredentialScopes)
	// número de scopes primeiro, depois cada scope enquadrado.
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(scopes)))
	buf = append(buf, n[:]...)
	for _, s := range scopes {
		writeField([]byte(s))
	}
	return buf
}

// canonicalScopes devolve os scopes ordenados e deduplicados (ordem estável,
// independente da ordem de declaração do publicador).
func canonicalScopes(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Digester calcula o digest de um artefacto a partir do seu conteúdo canonicalizado.
// É a PORTA de hashing: AOS-045 fornece PlaceholderDigester (determinista, não
// criptográfico) e AOS-047 injectará um SHA-256 sobre o conteúdo canónico completo,
// sem alterar a API do REG.
type Digester interface {
	// Digest devolve o digest do artefacto de tipo kind com o contrato c. Tem de
	// ser determinístico: o mesmo (kind, c) produz sempre o mesmo digest.
	Digest(kind ArtifactKind, c Contract) string
}

// PlaceholderDigester é o Digester por omissão de AOS-045. Deriva um digest
// DETERMINÍSTICO do conteúdo canónico via FNV-1a de 64 bits e prefixa-o com
// "placeholder-fnv1a:" para deixar EXPLÍCITO que NÃO é o hash criptográfico final
// (SHA-256, AOS-047) — é apenas o valor reservado no campo digest, com a
// propriedade essencial já garantida: determinismo e sensibilidade a qualquer
// mudança de conteúdo.
type PlaceholderDigester struct{}

// Digest implementa Digester.
func (PlaceholderDigester) Digest(kind ArtifactKind, c Contract) string {
	h := fnv.New64a()
	_, _ = h.Write(Canonicalize(kind, c))
	return placeholderPrefix + strconv.FormatUint(h.Sum64(), 16)
}

// placeholderPrefix marca um digest como o placeholder de AOS-045 (ponto de
// extensão para o SHA-256 de AOS-047).
const placeholderPrefix = "placeholder-fnv1a:"
