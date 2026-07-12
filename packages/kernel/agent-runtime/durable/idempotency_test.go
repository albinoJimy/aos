package durable

import (
	"errors"
	"testing"
)

// TestIdempotencyKeyDeterminism verifica que a mesma entrada produz sempre a
// mesma chave (pureza/determinismo) — critério de aceitação AOS-014.
func TestIdempotencyKeyDeterminism(t *testing.T) {
	t.Parallel()
	cases := []struct {
		run, step, want string
	}{
		{"run-1", "step-000001", "run-1:step-000001"},
		{"r", "s", "r:s"},
		{"run-abc", "step-000042-tool-3", "run-abc:step-000042-tool-3"},
	}
	for _, c := range cases {
		var last string
		for i := 0; i < 100; i++ {
			got, err := IdempotencyKey(c.run, c.step)
			if err != nil {
				t.Fatalf("IdempotencyKey(%q,%q) erro inesperado: %v", c.run, c.step, err)
			}
			if got != c.want {
				t.Fatalf("IdempotencyKey(%q,%q) = %q, quero %q", c.run, c.step, got, c.want)
			}
			if i > 0 && got != last {
				t.Fatalf("não-determinístico: %q != %q", got, last)
			}
			last = got
		}
	}
}

// TestIdempotencyKeyInjective prova ausência de colisão sobre um conjunto
// representativo, INCLUINDO o caso adversarial de deslocamento do delimitador
// ("a","bc") vs ("ab","c") — ambos rejeitados por conterem ':' quando construídos
// via um step contendo ':'. Aqui usamos o par que colidiria SE o ':' fosse livre.
func TestIdempotencyKeyInjective(t *testing.T) {
	t.Parallel()
	valid := [][2]string{
		{"a", "b"},
		{"ab", "c"},
		{"a", "bc"},
		{"abc", ""}, // inválido (vazio) — filtrado abaixo
		{"run-1", "step-000001"},
		{"run-1", "step-000002"},
		{"run-2", "step-000001"},
		{"x", "y-tool-1"},
		{"x", "y-tool-2"},
	}
	seen := map[string][2]string{}
	for _, p := range valid {
		key, err := IdempotencyKey(p[0], p[1])
		if err != nil {
			continue // inputs inválidos não entram no espaço de chaves
		}
		if prev, ok := seen[key]; ok && prev != p {
			t.Fatalf("COLISÃO: %v e %v → mesma chave %q", prev, p, key)
		}
		seen[key] = p
		// A inversa recupera exactamente os inputs (bijecção).
		gotRun, gotStep, serr := SplitKey(key)
		if serr != nil {
			t.Fatalf("SplitKey(%q) erro: %v", key, serr)
		}
		if gotRun != p[0] || gotStep != p[1] {
			t.Fatalf("SplitKey(%q) = (%q,%q), quero (%q,%q)", key, gotRun, gotStep, p[0], p[1])
		}
	}
	// ("a","bc") e ("ab","c") são AMBOS válidos e produzem chaves DISTINTAS
	// ("a:bc" vs "ab:c") — a colisão clássica está fechada pelo delimitador.
	k1, _ := IdempotencyKey("a", "bc")
	k2, _ := IdempotencyKey("ab", "c")
	if k1 == k2 {
		t.Fatalf("colisão de deslocamento não fechada: %q == %q", k1, k2)
	}
}

// TestIdempotencyKeyAdversarialDelimiter verifica que inputs contendo ':' — a
// única via de colisão — são REJEITADOS (fecho da injectividade).
func TestIdempotencyKeyAdversarialDelimiter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, run, step string
		wantErr         error
	}{
		{"run com ':'", "a:b", "c", ErrDelimiterInInput},
		{"step com ':'", "a", "b:c", ErrDelimiterInInput},
		{"ambos com ':'", "a:", ":c", ErrDelimiterInInput},
		{"run vazio", "", "s", ErrEmptyRunID},
		{"step vazio", "r", "", ErrEmptyStepID},
		{"ambos vazios", "", "", ErrEmptyRunID},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key, err := IdempotencyKey(c.run, c.step)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("IdempotencyKey(%q,%q) erro = %v, quero %v", c.run, c.step, err, c.wantErr)
			}
			if key != "" {
				t.Fatalf("chave devia ser vazia em erro, veio %q", key)
			}
		})
	}
}

// TestSplitKeyMalformed verifica a rejeição de chaves não-canónicas.
func TestSplitKeyMalformed(t *testing.T) {
	t.Parallel()
	bad := []string{"", "no-delim", ":leading", "trailing:", "a:b:c", ":"}
	for _, k := range bad {
		if _, _, err := SplitKey(k); !errors.Is(err, ErrMalformedKey) {
			t.Fatalf("SplitKey(%q) erro = %v, quero ErrMalformedKey", k, err)
		}
	}
}

// TestOpaqueKeyDeterministicAndDistinct verifica a forma opaca (hash) para logs:
// determinística e distinta para chaves distintas.
func TestOpaqueKeyDeterministicAndDistinct(t *testing.T) {
	t.Parallel()
	a1, err := OpaqueKey("run-1", "step-1")
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := OpaqueKey("run-1", "step-1")
	b, _ := OpaqueKey("run-1", "step-2")
	if a1 != a2 {
		t.Fatalf("OpaqueKey não-determinística: %q != %q", a1, a2)
	}
	if a1 == b {
		t.Fatalf("OpaqueKey colidiu para chaves distintas")
	}
	if len(a1) != 64 { // sha256 hex
		t.Fatalf("OpaqueKey deve ser sha256 hex (64 chars), veio %d", len(a1))
	}
	if _, err := OpaqueKey("bad:run", "s"); !errors.Is(err, ErrDelimiterInInput) {
		t.Fatalf("OpaqueKey devia propagar validação, veio %v", err)
	}
}
