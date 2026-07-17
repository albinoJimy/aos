package otelgenai

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// manualRawHash reproduz o hash pré-AOS-081 (bytes crus) para provar a compat do fallback.
func manualRawHash(toolID string, args []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(toolID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(args)
	return ToolCallHashPrefix + hex.EncodeToString(h.Sum(nil))
}

// TestCanonicalToolCallHashFormat: o hash tem sempre o prefixo canónico e o comprimento
// de um SHA-256 em hex (64 nibbles).
func TestCanonicalToolCallHashFormat(t *testing.T) {
	got := CanonicalToolCallHash("fs.read", []byte(`{"path":"/etc/hosts"}`))
	if !strings.HasPrefix(got, ToolCallHashPrefix) {
		t.Fatalf("hash sem prefixo %q: %q", ToolCallHashPrefix, got)
	}
	hexPart := strings.TrimPrefix(got, ToolCallHashPrefix)
	if len(hexPart) != 64 {
		t.Fatalf("hex de SHA-256 deve ter 64 chars, tem %d: %q", len(hexPart), hexPart)
	}
}

// TestCanonicalHashStableAcrossKeyOrderAndWhitespace é o critério CENTRAL de AOS-081: args
// SEMANTICAMENTE EQUIVALENTES (mesmas chaves por outra ordem + espaço/indentação diferente)
// produzem o MESMO hash — sem falso-negativo de dedup por formatação.
func TestCanonicalHashStableAcrossKeyOrderAndWhitespace(t *testing.T) {
	a := []byte(`{"alpha":1,"beta":{"x":10,"y":20},"gamma":"z"}`)
	b := []byte("  {\n  \"gamma\": \"z\",\n  \"beta\": { \"y\": 20, \"x\": 10 },\n  \"alpha\": 1\n}\n")
	ha := CanonicalToolCallHash("tool", a)
	hb := CanonicalToolCallHash("tool", b)
	if ha != hb {
		t.Fatalf("args equivalentes deviam dar o MESMO hash:\n a=%s\n b=%s", ha, hb)
	}
}

// TestCanonicalHashDistinctForDifferentArgs: args com valores diferentes dão hashes
// diferentes (não há colisão trivial).
func TestCanonicalHashDistinctForDifferentArgs(t *testing.T) {
	h1 := CanonicalToolCallHash("tool", []byte(`{"path":"/a"}`))
	h2 := CanonicalToolCallHash("tool", []byte(`{"path":"/b"}`))
	if h1 == h2 {
		t.Fatalf("args diferentes deviam dar hashes diferentes: %s", h1)
	}
}

// TestCanonicalHashArrayOrderIsSemantic: a ordem de um array É semântica — NÃO se
// normaliza. Dois arrays com os mesmos elementos por ordens diferentes dão hashes
// DIFERENTES (evita fundir acções distintas).
func TestCanonicalHashArrayOrderIsSemantic(t *testing.T) {
	h1 := CanonicalToolCallHash("tool", []byte(`{"items":[1,2,3]}`))
	h2 := CanonicalToolCallHash("tool", []byte(`{"items":[3,2,1]}`))
	if h1 == h2 {
		t.Fatalf("arrays em ordem diferente deviam dar hashes DIFERENTES (ordem é semântica): %s", h1)
	}
}

// TestCanonicalHashToolIDBoundary: o separador nulo entre tool_id e args evita colisões de
// fronteira — mover um caractere do fim do tool_id para o início dos args muda o hash.
func TestCanonicalHashToolIDBoundary(t *testing.T) {
	h1 := CanonicalToolCallHash("ab", []byte("c"))
	h2 := CanonicalToolCallHash("a", []byte("bc"))
	if h1 == h2 {
		t.Fatalf("o separador nulo devia evitar a colisão de fronteira: %s", h1)
	}
}

// TestCanonicalHashDependsOnToolID: o mesmo args em tools diferentes dá hashes diferentes.
func TestCanonicalHashDependsOnToolID(t *testing.T) {
	args := []byte(`{"q":"x"}`)
	if CanonicalToolCallHash("t1", args) == CanonicalToolCallHash("t2", args) {
		t.Fatal("o tool_id devia participar no hash")
	}
}

// TestCanonicalHashNonJSONFallbackRaw: para args NÃO-JSON o hash recai nos bytes crus —
// idêntico ao comportamento pré-AOS-081 (retro-compat). Provamos que bytes crus iguais dão
// o mesmo hash e que a formatação não é normalizada (não é JSON).
func TestCanonicalHashNonJSONFallbackRaw(t *testing.T) {
	raw := []byte("olá mundo, isto não é JSON")
	// Duas cópias independentes dos mesmos bytes devem dar o mesmo hash (estabilidade).
	rawCopy := append([]byte(nil), raw...)
	if CanonicalToolCallHash("tool", raw) != CanonicalToolCallHash("tool", rawCopy) {
		t.Fatal("hash de bytes crus idênticos devia ser estável")
	}
	// Bytes crus com espaçamento diferente NÃO são normalizados (não é JSON) — hashes diferem.
	h1 := CanonicalToolCallHash("tool", []byte("a b"))
	h2 := CanonicalToolCallHash("tool", []byte("a  b"))
	if h1 == h2 {
		t.Fatalf("bytes crus (não-JSON) não devem ser normalizados: %s", h1)
	}
}

// TestCanonicalHashRawEqualsManualSHA garante que o fallback não-JSON é EXACTAMENTE
// sha256(tool ‖ 0x00 ‖ args) — a compatibilidade byte-a-byte com o hash pré-AOS-081 do RM.
func TestCanonicalHashRawEqualsManualSHA(t *testing.T) {
	// "hello" não é JSON válido (sem aspas) → fallback cru.
	toolID := "echo"
	args := []byte("hello")
	got := CanonicalToolCallHash(toolID, args)
	want := manualRawHash(toolID, args)
	if got != want {
		t.Fatalf("fallback cru deve ser byte-idêntico ao sha256 manual:\n got=%s\nwant=%s", got, want)
	}
}

// TestCanonicalHashEmptyArgsFallback: args vazios recaem no fallback (bytes crus vazios),
// mantendo compat e determinismo.
func TestCanonicalHashEmptyArgsFallback(t *testing.T) {
	got := CanonicalToolCallHash("tool", nil)
	want := manualRawHash("tool", nil)
	if got != want {
		t.Fatalf("args vazios deviam recair no hash cru: got=%s want=%s", got, want)
	}
}

// TestCanonicalHashTrailingGarbageIsRaw: um documento com conteúdo residual após o primeiro
// valor JSON ("{}{}") NÃO é um documento único → fallback cru (não normaliza).
func TestCanonicalHashTrailingGarbageIsRaw(t *testing.T) {
	args := []byte(`{}{}`)
	got := CanonicalToolCallHash("tool", args)
	want := manualRawHash("tool", args)
	if got != want {
		t.Fatalf("conteúdo residual devia recair no hash cru: got=%s want=%s", got, want)
	}
}

// TestCanonicalHashScalarJSONNormalized: um JSON escalar (número/string) é canonicalizado —
// o mesmo valor com espaço à volta dá o mesmo hash.
func TestCanonicalHashScalarJSONNormalized(t *testing.T) {
	if CanonicalToolCallHash("t", []byte("42")) != CanonicalToolCallHash("t", []byte("  42 ")) {
		t.Fatal("um número JSON com espaço à volta devia canonicalizar para o mesmo hash")
	}
	if CanonicalToolCallHash("t", []byte(`"x"`)) != CanonicalToolCallHash("t", []byte(` "x" `)) {
		t.Fatal("uma string JSON com espaço à volta devia canonicalizar para o mesmo hash")
	}
}
