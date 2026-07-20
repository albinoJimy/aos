package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_Stdin: sem args lê de stdin e emite LCOV.
func TestRun_Stdin(t *testing.T) {
	t.Parallel()
	in := strings.NewReader("mode: set\npkg/a.go:1.1,1.2 1 1\n")
	var out strings.Builder
	if err := run(nil, in, &out); err != nil {
		t.Fatalf("run(stdin): %v", err)
	}
	if !strings.Contains(out.String(), "SF:pkg/a.go") {
		t.Fatalf("saida sem SF: %q", out.String())
	}
}

// TestRun_Ficheiros: agrega dois perfis passados como argumentos.
func TestRun_Ficheiros(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p1 := filepath.Join(dir, "p1.out")
	p2 := filepath.Join(dir, "p2.out")
	if err := os.WriteFile(p1, []byte("mode: atomic\nm1/a.go:1.1,1.2 1 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p2, []byte("mode: atomic\nm2/b.go:2.1,2.2 1 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := run([]string{p1, p2}, nil, &out); err != nil {
		t.Fatalf("run(ficheiros): %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "SF:m1/a.go") || !strings.Contains(got, "SF:m2/b.go") {
		t.Fatalf("agregacao de dois perfis falhou:\n%s", got)
	}
}

// TestRun_FicheiroInexistente: um caminho inválido devolve erro (fail-closed).
func TestRun_FicheiroInexistente(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	if err := run([]string{filepath.Join(t.TempDir(), "nao-existe.out")}, nil, &out); err == nil {
		t.Fatal("esperava erro para ficheiro inexistente")
	}
}
