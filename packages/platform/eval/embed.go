package eval

import (
	"embed"
	"io/fs"
	"sort"
)

// goldensets/*.json são os golden-sets VERSIONADOS e revisáveis embebidos no binário
// (o artefacto dashboard-as-code: reproduzível a partir dos builders — ver
// builders.go — e garantido não-divergente por teste de round-trip). Sem segredos: só
// inputs curados e critérios de expectativa.
//
//go:embed goldensets/*.json
var embeddedFS embed.FS

// embeddedGoldenDir é a raiz dos ficheiros embebidos.
const embeddedGoldenDir = "goldensets"

// EmbeddedSuites carrega e VALIDA todos os golden-sets embebidos, por ordem estável de
// nome de ficheiro (determinista). Fail-closed: um ficheiro JSON mal-formado ou um
// golden-set inválido faz devolver erro — nunca um conjunto parcial silencioso. É o
// ponto de entrada de produção do harness (os golden-sets reproduzíveis a partir do
// artefacto versionado, não configurados à mão).
func EmbeddedSuites() ([]GoldenSet, error) {
	entries, err := fs.ReadDir(embeddedFS, embeddedGoldenDir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := make([]GoldenSet, 0, len(names))
	for _, name := range names {
		data, rerr := embeddedFS.ReadFile(embeddedGoldenDir + "/" + name)
		if rerr != nil {
			return nil, rerr
		}
		gs, lerr := LoadGoldenSet(data)
		if lerr != nil {
			return nil, lerr
		}
		out = append(out, gs)
	}
	return out, nil
}

// EmbeddedSuitesFor devolve os golden-sets embebidos da classe de artefacto dada
// (ambos os datasets — golden E failure_derived), por ordem estável. Fail-closed via
// [EmbeddedSuites].
func EmbeddedSuitesFor(kind ArtifactKind) ([]GoldenSet, error) {
	all, err := EmbeddedSuites()
	if err != nil {
		return nil, err
	}
	var out []GoldenSet
	for _, gs := range all {
		if gs.ArtifactKind == kind {
			out = append(out, gs)
		}
	}
	return out, nil
}
