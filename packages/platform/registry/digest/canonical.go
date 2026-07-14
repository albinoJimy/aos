// Package digest implementa o HASHING criptográfico canonicalizado do
// Skill/Tool Registry (REG) — AOS-047, EPIC-05, ADR-012/ADR-009. Substitui o
// PlaceholderDigester determinista-mas-não-criptográfico de AOS-045 por um
// Digester SHA-256 sobre CONTEÚDO CANONICALIZADO e fornece a comparação de
// digest usada na resolução (fail-closed, [ErrDigestMismatch]).
//
// # Canonicalização determinística e reproduzível
//
// O digest é SHA-256 do conteúdo canonicalizado. A canonicalização é PURA
// (sem time.Now/rand, serialização estável) e garante três propriedades
// essenciais de supply-chain:
//
//   - o MESMO conteúdo produz SEMPRE o mesmo digest;
//   - uma mudança MÍNIMA de conteúdo produz um digest DIFERENTE;
//   - uma mudança SÓ de ordem-de-chaves ou de whitespace insignificante NÃO
//     altera o digest (JSON canónico: chaves ordenadas recursivamente,
//     whitespace normalizado, UTF-8).
//
// # Três tipos de conteúdo (tecnica/05 §5)
//
//   - schema da tool (JSON) e manifesto de capabilities (JSON) → [DigestJSON]
//     (JSON canónico + SHA-256);
//   - binário do servidor (bytes) → [DigestBytes] (SHA-256 dos bytes crus).
//
// A API é REUTILIZÁVEL pela revalidação por chamada (AOS-051) e pelo
// congelamento por run (AOS-050): ambos recalculam/comparam o mesmo digest.
package digest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/aos-ref/platform/registry/domain"
)

// CanonicalJSON produz a forma CANÓNICA de um documento JSON: objectos com as
// chaves ordenadas recursivamente e todo o whitespace insignificante removido,
// preservando a ordem dos arrays (semântica) e o texto exacto dos números
// (precisão inteira via json.Number). Dois documentos que difiram apenas na
// ordem das chaves ou no whitespace canonicalizam para os MESMOS bytes.
//
// Fail-closed: JSON inválido (ou tokens em excesso) devolve [ErrInvalidJSON];
// o chamador decide o fallback. Um documento vazio/só-whitespace devolve nil
// (ausência de conteúdo, não um erro).
//
// CHAVES DUPLICADAS SÃO REJEITADAS (fail-closed, AOS-047): um objecto com a
// MESMA chave repetida devolve [ErrInvalidJSON] em vez de colapsar
// silenciosamente para o último valor. A RFC 8259 PERMITE duplicados e os
// parsers reais divergem (first-wins vs last-wins), pelo que um documento com
// uma chave-sombra teria o MESMO digest de um documento sem ela mas semântica
// DIFERENTE para um consumidor first-wins (colisão semântica). Recusar o
// duplicado elimina esse vector de substituição supply-chain: um único
// significado por objecto é a condição para o digest ser um fingerprint fiel.
func CanonicalJSON(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	// UseNumber preserva o texto exacto dos números (sem colapsar inteiros
	// grandes em float64), mantendo a canonicalização fiel ao conteúdo.
	dec.UseNumber()
	// Descodifica por TOKENS (não via Decode para map[string]any, que colapsaria
	// chaves duplicadas last-wins) para poder detectar e recusar duplicados.
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	// Rejeita lixo à cauda (ex.: "{} {}"): um único valor por documento.
	if dec.More() {
		return nil, fmt.Errorf("%w: tokens em excesso apos o valor", ErrInvalidJSON)
	}
	return marshalCanonical(v)
}

// decodeValue lê UM valor JSON do decoder por tokens, reconstruindo
// map[string]any / []any / escalares de forma equivalente a json.Decode MAS
// rejeitando chaves duplicadas dentro de um mesmo objecto (fail-closed). Cada
// objecto tem o seu próprio conjunto de chaves vistas (a detecção é por-objecto,
// aninhada). Devolve [ErrInvalidJSON] em qualquer erro de sintaxe ou duplicado.
func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		// Escalar (string, json.Number, bool ou nil) — passa intacto.
		return tok, nil
	}
	switch delim {
	case '{':
		m := make(map[string]any)
		seen := make(map[string]struct{})
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
			}
			key, ok := keyTok.(string)
			if !ok {
				return nil, fmt.Errorf("%w: chave de objecto nao-string", ErrInvalidJSON)
			}
			if _, dup := seen[key]; dup {
				return nil, fmt.Errorf("%w: chave duplicada %q no mesmo objecto", ErrInvalidJSON, key)
			}
			seen[key] = struct{}{}
			val, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			m[key] = val
		}
		// Consome o '}' de fecho.
		if _, err := dec.Token(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
		}
		return m, nil
	case '[':
		arr := make([]any, 0)
		for dec.More() {
			val, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, val)
		}
		// Consome o ']' de fecho.
		if _, err := dec.Token(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
		}
		return arr, nil
	default:
		return nil, fmt.Errorf("%w: delimitador inesperado %q", ErrInvalidJSON, delim)
	}
}

// marshalCanonical serializa v de forma estável: mapas com chaves ordenadas,
// output compacto, sem escape HTML (para que o digest reflicta o conteúdo e não
// a política de escape do encoder). Recorre a normalize para garantir que os
// mapas produzidos pelo decoder são reescritos com ordenação determinística.
func marshalCanonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(normalize(v)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	// Encode acrescenta um '\n' terminal; removê-lo mantém a forma canónica
	// estável e independente do encoder.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// normalize converte recursivamente os mapas desserializados em canonicalMap
// (que serializa com chaves ordenadas) e percorre os arrays preservando a
// ordem. Valores escalares (string/json.Number/bool/nil) passam intactos.
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		m := make(canonicalMap, 0, len(t))
		for _, k := range keys {
			m = append(m, canonicalPair{Key: k, Val: normalize(t[k])})
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normalize(e)
		}
		return out
	default:
		return v
	}
}

// canonicalPair é uma entrada chave→valor já normalizada.
type canonicalPair struct {
	Key string
	Val any
}

// canonicalMap serializa um objecto JSON com as chaves na ordem em que foram
// inseridas (que normalize garante ser a ordem lexicográfica estável), sem
// depender da ordenação implícita do encoder de mapas.
type canonicalMap []canonicalPair

// MarshalJSON implementa json.Marshaler emitindo {"k":v,...} por ordem estável.
func (m canonicalMap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, p := range m {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := marshalScalar(p.Key)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		val, err := marshalScalar(p.Val)
		if err != nil {
			return nil, err
		}
		buf.Write(val)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// marshalScalar serializa um valor já normalizado (escalar, canonicalMap ou
// array), mantendo a desactivação do escape HTML de forma consistente.
func marshalScalar(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// canonicalContract serializa o CONTRATO público de um artefacto de forma
// determinística e sem ambiguidade de fronteiras: cada campo é precedido do seu
// comprimento (u32 big-endian) para que a concatenação nunca colida (domain
// separation). Os schemas de I/O são reduzidos à sua forma JSON canónica (ordem
// de chaves/whitespace irrelevantes). Os credential scopes são ordenados e
// deduplicados (a ordem de declaração não é semântica).
//
// A porta de FAIL-CLOSED para schemas malformados é a publicação: o REG valida
// que InputSchema/OutputSchema são JSON bem-formado ANTES de admitir a entrada
// (registry.Publish), pelo que uma entrada admitida nunca chega aqui com um
// schema inválido. O fallback de canonicalOrRaw para bytes crus é apenas uma
// última defesa determinista (nunca em pânico) para o caso de o Digester ser
// invocado isoladamente sobre conteúdo não-validado.
func canonicalContract(kind domain.ArtifactKind, c domain.Contract) []byte {
	var buf bytes.Buffer
	writeField := func(b []byte) {
		var n [4]byte
		putUint32(n[:], uint32(len(b)))
		buf.Write(n[:])
		buf.Write(b)
	}

	writeField([]byte(kind))
	writeField([]byte(c.Egress))
	writeField(canonicalOrRaw(c.InputSchema))
	writeField(canonicalOrRaw(c.OutputSchema))

	scopes := canonicalScopes(c.CredentialScopes)
	var n [4]byte
	putUint32(n[:], uint32(len(scopes)))
	buf.Write(n[:])
	for _, s := range scopes {
		writeField([]byte(s))
	}
	return buf.Bytes()
}

// canonicalOrRaw devolve a forma JSON canónica de raw; se raw não for JSON
// válido, devolve os bytes crus (fail-safe determinista — nunca em pânico).
func canonicalOrRaw(raw []byte) []byte {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if canon, err := CanonicalJSON(raw); err == nil {
		return canon
	}
	return raw
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

// putUint32 escreve n em big-endian em b[:4] (sem importar encoding/binary para
// manter a fronteira de campo local e explícita).
func putUint32(b []byte, n uint32) {
	b[0] = byte(n >> 24)
	b[1] = byte(n >> 16)
	b[2] = byte(n >> 8)
	b[3] = byte(n)
}
