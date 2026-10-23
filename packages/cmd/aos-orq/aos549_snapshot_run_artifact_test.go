package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
)

// AOS-549: o snapshot pinado aceita a escrita local do run no eixo de mutação, e lê-a como tal.
func TestAOS549_SnapshotLeAEscritaLocalDoRun(t *testing.T) {
	snap, err := carregarSnapshot(escreverTmp(t, `{
  "hash": "sha256:snap-aos549",
  "tools": [
    {"name":"artifact_write","version":"1.0.0","digest":"sha256:aaa","admissible":true,
     "egress":"none","reversibility":"reversible","mutation":"run_artifact"}
  ]
}`))
	if err != nil {
		t.Fatalf("carregarSnapshot: %v", err)
	}
	if len(snap.Tools) != 1 || snap.Tools[0].Mutation != planvalidate.MutationRunArtifact {
		t.Fatalf("mutacao lida: %+v", snap.Tools)
	}
}

// Revisão do AOS-549: `run_artifact` fora da forma fechada é recusado no carregamento — noutra tool
// baixaria o risco do nó (muta sem ser irreversível).
func TestAOS549_SnapshotRecusaEscritaLocalForaDaForma(t *testing.T) {
	casos := map[string]string{
		"outra tool":   `"name":"fs_write","egress":"none","reversibility":"reversible"`,
		"com egress":   `"name":"artifact_write","egress":"external","reversibility":"reversible"`,
		"irreversivel": `"name":"artifact_write","egress":"none","reversibility":"irreversible"`,
	}
	for nome, eixos := range casos {
		_, err := carregarSnapshot(escreverTmp(t, `{"hash":"sha256:x","tools":[{`+eixos+
			`,"version":"1.0.0","digest":"sha256:aaa","admissible":true,"mutation":"run_artifact"}]}`))
		if !errors.Is(err, ErrEscritaLocalForaDaForma) {
			t.Errorf("%s: aceite (%v)", nome, err)
		}
	}
}

// Revisão do AOS-549: o snapshot diz `run_artifact` e o nó `mutates` para a mesma tool ⇒ o snapshot
// declara menos risco (não irreversível) e é recusado.
func TestAOS549_SnapshotRunArtifactContraNoMutatesAvermelha(t *testing.T) {
	snap, err := carregarSnapshot(escreverTmp(t, `{"hash":"sha256:x","tools":[{"name":"artifact_write","version":"1.0.0",
	  "digest":"sha256:aaa","admissible":true,"egress":"none","reversibility":"reversible","mutation":"run_artifact"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	no := []toolDoNo{{Name: "artifact_write", Version: "1.0.0", Digest: "sha256:aaa", Egress: "none", Reversibility: "reversible", Mutation: "mutates"}}
	if err := compararSnapshotComCatalogo(snap, no); !errors.Is(err, ErrSnapshotDivergeDoNo) || !strings.Contains(err.Error(), "menos risco") {
		t.Fatalf("run_artifact contra mutates no nó passou: %v", err)
	}
	no[0].Mutation = "run_artifact"
	if err := compararSnapshotComCatalogo(snap, no); err != nil {
		t.Fatalf("iguais tinham de passar: %v", err)
	}
}
