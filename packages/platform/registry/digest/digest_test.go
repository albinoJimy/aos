package digest

import (
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/platform/registry/domain"
)

// --- Canonicalização determinística: mesmo conteúdo -> mesmo digest ---------

// TestCanonicalJSON_KeyOrderAndWhitespaceInvariant prova a invariante central de
// AOS-047: dois documentos JSON que difiram APENAS na ordem das chaves ou no
// whitespace insignificante canonicalizam para os MESMOS bytes (logo, o mesmo
// digest). Uma mudança de VALOR muda o resultado.
func TestCanonicalJSON_KeyOrderAndWhitespaceInvariant(t *testing.T) {
	t.Parallel()
	// Mesma semântica, ordem de chaves e whitespace diferentes (chaves aninhadas
	// também reordenadas).
	a := []byte(`{"b":1,"a":{"y":2,"x":[1,2,3]},"c":"v"}`)
	b := []byte("  {\n  \"a\": { \"x\": [1, 2, 3], \"y\": 2 },\n  \"c\":\"v\",\n  \"b\": 1\n }\t")

	ca, err := CanonicalJSON(a)
	if err != nil {
		t.Fatalf("CanonicalJSON(a): %v", err)
	}
	cb, err := CanonicalJSON(b)
	if err != nil {
		t.Fatalf("CanonicalJSON(b): %v", err)
	}
	if string(ca) != string(cb) {
		t.Fatalf("ordem-de-chaves/whitespace mudaram a forma canonica:\n a=%s\n b=%s", ca, cb)
	}
	// A forma canónica é compacta e com chaves ordenadas.
	want := `{"a":{"x":[1,2,3],"y":2},"b":1,"c":"v"}`
	if string(ca) != want {
		t.Fatalf("forma canonica = %s, quer %s", ca, want)
	}
}

func TestDigestJSON_Determinism(t *testing.T) {
	t.Parallel()
	a := []byte(`{"type":"object","required":["id"],"props":{"id":{"type":"string"}}}`)
	// Reordenado + whitespace.
	b := []byte(`{ "props" : { "id" : { "type":"string" } }, "type":"object", "required":["id"] }`)

	da, err := DigestJSON(a)
	if err != nil {
		t.Fatalf("DigestJSON(a): %v", err)
	}
	db, err := DigestJSON(b)
	if err != nil {
		t.Fatalf("DigestJSON(b): %v", err)
	}
	if da != db {
		t.Fatalf("mesmo schema (ordem/whitespace) -> digests diferentes: %s vs %s", da, db)
	}
	if !strings.HasPrefix(da, Prefix) {
		t.Fatalf("digest %q sem prefixo %q", da, Prefix)
	}
	// Comprimento: sha256: + 64 hex chars.
	if len(da) != len(Prefix)+64 {
		t.Fatalf("comprimento inesperado do digest: %d", len(da))
	}
}

// TestDigestJSON_MinimalChangeDiffers: uma mudança MÍNIMA de conteúdo (um valor)
// produz um digest DIFERENTE.
func TestDigestJSON_MinimalChangeDiffers(t *testing.T) {
	t.Parallel()
	base := []byte(`{"type":"object","maxLen":10}`)
	// Só o valor de maxLen muda (10 -> 11): array-order/whitespace idênticos.
	changed := []byte(`{"type":"object","maxLen":11}`)
	d1, _ := DigestJSON(base)
	d2, _ := DigestJSON(changed)
	if d1 == d2 {
		t.Fatal("mudanca minima de valor devia mudar o digest")
	}
	// A ordem de um ARRAY é semântica: reordenar altera o digest.
	arrA := []byte(`{"e":[1,2,3]}`)
	arrB := []byte(`{"e":[3,2,1]}`)
	da, _ := DigestJSON(arrA)
	db, _ := DigestJSON(arrB)
	if da == db {
		t.Fatal("ordem de array e semantica; devia mudar o digest")
	}
}

func TestCanonicalJSON_InvalidRejected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
	}{
		{"lixo", "{not json"},
		{"tokens a mais", "{} {}"},
		{"aspas soltas", `{"a":}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := CanonicalJSON([]byte(tc.in)); !errors.Is(err, ErrInvalidJSON) {
				t.Fatalf("CanonicalJSON(%q) = %v, quer ErrInvalidJSON", tc.in, err)
			}
			if _, err := DigestJSON([]byte(tc.in)); !errors.Is(err, ErrInvalidJSON) {
				t.Fatalf("DigestJSON(%q) devia falhar com ErrInvalidJSON", tc.in)
			}
		})
	}
}

// TestCanonicalJSON_DuplicateKeysRejected prova o fail-closed de AOS-047 contra a
// COLISÃO SEMÂNTICA por chave duplicada: um objecto com a mesma chave repetida é
// recusado com ErrInvalidJSON em vez de colapsar silenciosamente para o último
// valor (last-wins). Sem esta recusa, `{"tool":"a","tool":"b"}` e `{"tool":"b"}`
// canonicalizariam para os MESMOS bytes (mesmo digest, semântica divergente para
// um consumidor first-wins).
func TestCanonicalJSON_DuplicateKeysRejected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
	}{
		{"top-level", `{"tool":"a","tool":"b"}`},
		{"nested", `{"outer":{"k":1,"k":2}}`},
		{"dentro de array", `[{"k":1,"k":2}]`},
		{"tres repeticoes", `{"a":1,"a":2,"a":3}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := CanonicalJSON([]byte(tc.in)); !errors.Is(err, ErrInvalidJSON) {
				t.Fatalf("CanonicalJSON(%q) = %v, quer ErrInvalidJSON", tc.in, err)
			}
			if _, err := DigestJSON([]byte(tc.in)); !errors.Is(err, ErrInvalidJSON) {
				t.Fatalf("DigestJSON(%q) devia falhar com ErrInvalidJSON", tc.in)
			}
		})
	}
}

// TestCanonicalJSON_NoDuplicateKeyCollision confirma directamente que o vector de
// colisão está fechado: a forma com a chave-sombra NÃO produz o mesmo digest da
// forma sem ela — a primeira é REJEITADA (nunca chega a hashear).
func TestCanonicalJSON_NoDuplicateKeyCollision(t *testing.T) {
	t.Parallel()
	shadow := []byte(`{"tool":"a","tool":"b"}`)
	plain := []byte(`{"tool":"b"}`)
	if _, err := DigestJSON(shadow); !errors.Is(err, ErrInvalidJSON) {
		t.Fatalf("forma com chave-sombra devia ser rejeitada, got %v", err)
	}
	// A forma legítima (sem duplicado) continua a hashear normalmente.
	if _, err := DigestJSON(plain); err != nil {
		t.Fatalf("forma sem duplicado devia hashear: %v", err)
	}
}

// TestCanonicalJSON_DistinctKeysStillCanonicalize garante que a detecção de
// duplicados NÃO afecta objectos legítimos: chaves distintas (mesmo muitas,
// aninhadas, reordenadas) canonicalizam de forma estável e determinista.
func TestCanonicalJSON_DistinctKeysStillCanonicalize(t *testing.T) {
	t.Parallel()
	a := []byte(`{"z":1,"a":{"n":[1,2],"m":true},"k":null}`)
	b := []byte(`{ "a": { "m": true, "n": [1,2] }, "k": null, "z": 1 }`)
	ca, err := CanonicalJSON(a)
	if err != nil {
		t.Fatalf("CanonicalJSON(a): %v", err)
	}
	cb, err := CanonicalJSON(b)
	if err != nil {
		t.Fatalf("CanonicalJSON(b): %v", err)
	}
	if string(ca) != string(cb) {
		t.Fatalf("chaves distintas reordenadas divergiram:\n a=%s\n b=%s", ca, cb)
	}
	want := `{"a":{"m":true,"n":[1,2]},"k":null,"z":1}`
	if string(ca) != want {
		t.Fatalf("forma canonica = %s, quer %s", ca, want)
	}
}

func TestCanonicalJSON_EmptyIsNil(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "   ", "\t\n"} {
		got, err := CanonicalJSON([]byte(in))
		if err != nil {
			t.Fatalf("CanonicalJSON(%q) err: %v", in, err)
		}
		if got != nil {
			t.Fatalf("CanonicalJSON(%q) = %v, quer nil", in, got)
		}
	}
}

// --- DigestBytes: binário do servidor (bytes crus, sem canonicalização) -----

func TestDigestBytes_Determinism(t *testing.T) {
	t.Parallel()
	bin := []byte{0x00, 0x01, 0x02, 0xff, 0xfe}
	d1 := DigestBytes(bin)
	d2 := DigestBytes([]byte{0x00, 0x01, 0x02, 0xff, 0xfe})
	if d1 != d2 {
		t.Fatal("mesmo binario -> mesmo digest")
	}
	// Um byte diferente -> digest diferente.
	if d1 == DigestBytes([]byte{0x00, 0x01, 0x02, 0xff, 0x00}) {
		t.Fatal("byte diferente devia mudar o digest")
	}
	// SHA-256 conhecido do input vazio (ancora o algoritmo).
	if got := DigestBytes(nil); got != Prefix+"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("SHA-256 do vazio inesperado: %s", got)
	}
}

// --- Digester (contrato): kind + egress + schemas JSON + scopes -------------

func TestSHA256Digester_ContractCanonicalization(t *testing.T) {
	t.Parallel()
	d := SHA256Digester{}
	c1 := domain.Contract{
		InputSchema:      []byte(`{"a":1,"b":2}`),
		Egress:           domain.EgressExternal,
		CredentialScopes: []string{"vault:a", "vault:b"},
	}
	// Schema com ordem-de-chaves e whitespace diferentes + scopes reordenados e
	// duplicados -> MESMO digest (canonicalização).
	c2 := domain.Contract{
		InputSchema:      []byte(`{ "b":2, "a":1 }`),
		Egress:           domain.EgressExternal,
		CredentialScopes: []string{"vault:b", "vault:a", "vault:a"},
	}
	if d.Digest(domain.KindTool, c1) != d.Digest(domain.KindTool, c2) {
		t.Fatal("mesmo conteudo canonico deve produzir mesmo digest")
	}
	// Mudança mínima (egress) -> digest diferente.
	c3 := c1
	c3.Egress = domain.EgressInternal
	if d.Digest(domain.KindTool, c1) == d.Digest(domain.KindTool, c3) {
		t.Fatal("mudanca de egress deve mudar o digest")
	}
	// Tipo diferente (mesmo contrato) -> digest diferente (domain separation).
	if d.Digest(domain.KindTool, c1) == d.Digest(domain.KindSkill, c1) {
		t.Fatal("tipo diferente deve mudar o digest")
	}
	// Prefixo SHA-256 real (não placeholder).
	got := d.Digest(domain.KindTool, c1)
	if !strings.HasPrefix(got, Prefix) || len(got) != len(Prefix)+64 {
		t.Fatalf("digest %q nao e um SHA-256 hex prefixado", got)
	}
}

// TestSHA256Digester_InvalidSchemaFallsBackToRaw: um schema que NÃO é JSON válido
// não faz o Digester entrar em pânico — cai para os bytes crus, permanecendo
// determinista e sensível ao conteúdo.
func TestSHA256Digester_InvalidSchemaFallsBackToRaw(t *testing.T) {
	t.Parallel()
	d := SHA256Digester{}
	c := domain.Contract{InputSchema: []byte("not-json"), Egress: domain.EgressNone}
	g1 := d.Digest(domain.KindTool, c)
	g2 := d.Digest(domain.KindTool, c)
	if g1 != g2 {
		t.Fatal("fallback para bytes crus deve ser determinista")
	}
	c2 := domain.Contract{InputSchema: []byte("not-json!"), Egress: domain.EgressNone}
	if g1 == d.Digest(domain.KindTool, c2) {
		t.Fatal("conteudo cru diferente devia mudar o digest")
	}
}

// --- Compare / ErrDigestMismatch --------------------------------------------

func TestCompare(t *testing.T) {
	t.Parallel()
	if err := Compare("sha256:abc", "sha256:abc"); err != nil {
		t.Fatalf("digests iguais nao deviam divergir: %v", err)
	}
	if err := Compare("sha256:abc", "sha256:xyz"); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("digests diferentes = %v, quer ErrDigestMismatch", err)
	}
	// Esperado vazio é sempre divergência (fail-closed no caminho de ausência).
	if err := Compare("", ""); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("esperado vazio = %v, quer ErrDigestMismatch", err)
	}
	// MismatchError expõe os valores para diagnóstico auditável.
	var me *MismatchError
	if err := Compare("sha256:want", "sha256:got"); !errors.As(err, &me) {
		t.Fatalf("erro devia ser *MismatchError, got %T", err)
	} else if me.Expected != "sha256:want" || me.Computed != "sha256:got" {
		t.Fatalf("MismatchError sem valores: %+v", me)
	}
	// A mensagem torna a ausência legível.
	if msg := (&MismatchError{Expected: "", Computed: "sha256:x"}).Error(); !strings.Contains(msg, "<vazio>") {
		t.Fatalf("mensagem sem marcador de ausencia: %s", msg)
	}
}
