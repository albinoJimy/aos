//go:build ignore

// gen_goldensets.go regenera os artefactos JSON versionados dos golden-sets embebidos
// a partir da FONTE DE VERDADE (os builders em builders.go), na forma canónica
// byte-estável. Correr após alterar os builders:
//
//	go run gen_goldensets.go
//
// O teste de round-trip (embed_test.go) falha se os ficheiros divergirem dos builders,
// pelo que esquecer de regenerar bloqueia o gate — nunca há drift silencioso.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	eval "github.com/aos-ref/platform/eval"
)

func main() {
	dir := "goldensets"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(err)
	}
	for _, gs := range eval.BuiltSuites() {
		data, err := gs.CanonicalJSON()
		if err != nil {
			fail(err)
		}
		name := string(gs.ArtifactKind) + "-" + string(gs.Dataset) + ".json"
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			fail(err)
		}
		fmt.Printf("escrito %s (%d bytes)\n", path, len(data))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "erro:", err)
	os.Exit(1)
}
