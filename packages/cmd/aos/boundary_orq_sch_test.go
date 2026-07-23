package main

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// AOS-164b — FRONTEIRA nó↔ORQ/SCH, tornada VERIFICÁVEL em compilação/importação (ver
// docs/adr/ADR-018-fronteira-no-orq-sch.md). Na v1 single-host (Carta §7, emenda 1.2), o
// loop de serviço do nó ([NodeService], ciclo de vida por LEASE durável) é a FONTE ÚNICA DE
// VERDADE do ciclo de vida. As portas EPIC-03 Orchestrator (decompõe goal→DAG) e Scheduler
// (despacha task.ready via RM) são consumidas DENTRO da execução de um run, NUNCA como uma
// autoridade concorrente do ciclo de vida — o que eliminaria as "duas fontes de verdade".
//
// Este teste IMPÕE essa fronteira: o código de PRODUÇÃO do nó (ficheiros .go não-teste)
// NÃO importa os módulos do Orquestrador nem do Escalonador — não há um segundo Scheduler
// a ser instanciado/arrancado a disputar o ciclo de vida com o loop por-lease. Se um dia se
// consumir uma dessas portas DENTRO de um run, o wiring far-se-á num colaborador dedicado
// (não neste comando) e este teste sinaliza a mudança para revisão consciente da fronteira.
// forbiddenLifecycleModules — módulos cujo IMPORT pelo NÓ indicaria uma segunda autoridade
// de ciclo de vida co-residente no processo do loop de serviço (segunda fonte de verdade).
var forbiddenLifecycleModules = []string{
	"github.com/aos-ref/control-plane/orchestrator",
	"github.com/aos-ref/control-plane/scheduler",
}

// matchesForbidden reporta se o caminho de import p é (ou está sob) um dos módulos proibidos.
func matchesForbidden(p string) string {
	for _, bad := range forbiddenLifecycleModules {
		if p == bad || strings.HasPrefix(p, bad+"/") {
			return bad
		}
	}
	return ""
}

func TestBoundary_NodeDoesNotImportConcurrentOrchestratorOrScheduler(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir do pacote do nó: %v", err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse de %q: %v", name, err)
		}
		checked++
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if bad := matchesForbidden(p); bad != "" {
				t.Errorf("%s importa %q — viola ADR-018: o nó não instancia um Orquestrador/Escalonador concorrente como autoridade do ciclo de vida (a fonte única é o loop por-lease)", name, p)
			}
		}
	}
	if checked == 0 {
		t.Fatal("nenhum ficheiro de produção verificado — o guarda de fronteira ficou vacuoso")
	}
}

// TestBoundary_NodeBuildGraphExcludesConcurrentOrchestratorOrScheduler — ADR-018 §5 afirma a
// invariante sobre o PROCESSO DO NÓ, não apenas sobre os .go deste comando. O guarda de
// imports acima só vê os imports DIRECTOS de cmd/aos; um Escalonador/Orquestrador concorrente
// arrastado TRANSITIVAMENTE (p.ex. via a raiz de composição `integration`, que este comando
// requer) passaria despercebido. Este teste fecha esse ponto-cego: interroga o GRAFO DE BUILD
// EFECTIVO do binário do nó (`go list -deps .`) e falha se QUALQUER pacote — directo ou
// transitivo — pertencer aos módulos do ciclo de vida concorrente. É tão abrangente quanto a
// afirmação do ADR (o processo do nó), não apenas os ficheiros do comando.
func TestBoundary_NodeBuildGraphExcludesConcurrentOrchestratorOrScheduler(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		// Sem toolchain, a verificação transitiva não é possível; o guarda de imports
		// directos (acima) permanece activo. Sinaliza-se o skip em vez de falso-verde.
		t.Skipf("toolchain `go` indisponível — verificação do grafo de build transitivo saltada (guarda de imports directos mantém-se): %v", err)
	}

	// Grafo de dependências COMPLETO do pacote do nó (inclui deps cross-módulo como
	// `integration`, a raiz de composição real). `-deps` lista o fecho transitivo.
	out, err := exec.Command(goBin, "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("`go list -deps .` no pacote do nó falhou: %v\n%s", err, out)
	}

	pkgs := strings.Split(strings.TrimSpace(string(out)), "\n")
	seen := 0
	for _, line := range pkgs {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		seen++
		if bad := matchesForbidden(p); bad != "" {
			t.Errorf("o grafo de build do nó inclui %q (sob %s) — viola ADR-018 §5: o PROCESSO do nó não pode arrastar um Orquestrador/Escalonador concorrente como autoridade do ciclo de vida, nem transitivamente (a fonte única é o loop por-lease)", p, bad)
		}
	}
	if seen == 0 {
		t.Fatal("`go list -deps .` não devolveu pacotes — o guarda transitivo ficou vacuoso")
	}
}
