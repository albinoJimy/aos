package main

import (
	"path/filepath"
	"testing"
)

// TestRegistriesDoRepoArrancam guarda os registries REAIS deste repositório contra a validação de
// arranque de [validateResourceBinding].
//
// Existe porque a correcção do achado A1 é FAIL-CLOSED: uma tool cujo efeito o modelo parametriza
// mas cujo `resource_value` é constante deixa o nó recusar arrancar. Isso é o comportamento
// desejado — e torna cada registry versionado num ficheiro que pode partir a produção em silêncio
// até alguém fazer deploy. Este teste faz a validação correr em CI sobre os ficheiros a sério,
// para que a migração não dependa de ninguém se lembrar.
func TestRegistriesDoRepoArrancam(t *testing.T) {
	for _, rel := range []string{
		"../../../deploy/server/model-tools/tools.json",
		"../../../deploy/node/dev-hardened/model-tools/tools.json",
	} {
		t.Run(filepath.Base(filepath.Dir(filepath.Dir(rel))), func(t *testing.T) {
			t.Setenv("AOS_MODEL_TOOLS", rel)
			if _, _, err := loadModelToolsFromEnv(); err != nil {
				t.Fatalf("%s nao passa a validacao de arranque — o no recusaria arrancar com ele: %v", rel, err)
			}
		})
	}
}
