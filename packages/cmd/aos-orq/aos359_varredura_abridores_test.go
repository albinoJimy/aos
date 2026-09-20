package main

// AOS-359 — A VARREDURA DEIXA DE SER UMA TABELA NUM `.md`.
//
// O diagnóstico deste ticket é preciso: «o defeito era a varredura incompleta, não a
// linha». O AOS-347 corrigiu as vias do nó e declarou os chamadores numa tabela; a
// tabela caducou no dia em que alguém escreveu a via do `aos-orq`, e ninguém reparou
// durante um ticket inteiro.
//
// Remediar isso com OUTRA tabela seria repetir o gesto que falhou. Este teste é o
// sensor: um abridor de ESCRITA do Event Store durável fora da lista nomeada avermelha
// aqui, em vez de esperar pela próxima auditoria adversarial.
//
// O que a lista nomeia é o CAMINHO DE ESCRITA. Quem precisar de abrir para escrever
// acrescenta-se a si próprio à lista — o que é uma decisão consciente, com nome e
// revisão — e quem só quer ler usa [eventstore.OpenReadOnly], que não tem por onde
// truncar nem por onde ganhar uma cabeça de append.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// abridorPermitido é uma chamada de escrita nomeada: quantas vezes o ficheiro pode
// abrir o Event Store para escrita, e porquê.
//
// A permissão é por CONTAGEM e não por ficheiro, e a diferença é o ponto todo. O
// `substrato.go` tem uma chamada legítima (`abrirParaEscrita`) e tinha uma ilegítima
// (`abrirParaLeitura`) no MESMO ficheiro — uma lista por ficheiro daria verde ao defeito
// que este ticket corrige, o que é um sensor a passar pela razão errada.
type abridorPermitido struct {
	chamadas int
	razao    string
}

// abridoresDeEscritaPermitidos são os chamadores de [eventstore.Open]/[eventstore.Reopen]
// do lado da escrita, por desenho. Caminhos relativos à raiz do repositório, com `/`.
var abridoresDeEscritaPermitidos = map[string]abridorPermitido{
	"packages/cmd/aos/bootstrap.go":     {1, "o store durável do nó — escritor legítimo"},
	"packages/cmd/aos-orq/substrato.go": {1, "abrirParaEscrita, DEPOIS de LockWAL — a via de LEITURA do mesmo ficheiro usa OpenReadOnly"},
}

// raizDoRepo sobe a partir do directório do teste até achar a raiz (a pasta que tem
// `packages` e `specs`).
func raizDoRepo(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 12; i++ {
		_, errP := os.Stat(filepath.Join(dir, "packages"))
		_, errS := os.Stat(filepath.Join(dir, "specs"))
		if errP == nil && errS == nil {
			return dir
		}
		pai := filepath.Dir(dir)
		if pai == dir {
			break
		}
		dir = pai
	}
	t.Skip("raiz do repositório não encontrada a partir do directório do teste — varredura saltada")
	return ""
}

// TestAOS359_AbridorDeEscritaForaDaListaAvermelha varre a árvore e exige que cada
// chamada a um abridor de ESCRITA do Event Store durável, fora de testes, esteja
// nomeada. É o critério de aceitação 3 convertido de declaração em propriedade.
func TestAOS359_AbridorDeEscritaForaDaListaAvermelha(t *testing.T) {
	raiz := raizDoRepo(t)
	// Reopen é alias exportado de Open, com a MESMA semântica de truncar: uma varredura
	// que só procurasse `Open(` não o apanharia, e foi assim que a do AOS-347 falhou.
	agulhas := []string{"eventstore.Open(", "eventstore.Reopen("}

	var intrusos []string
	err := filepath.Walk(filepath.Join(raiz, "packages"), func(caminho string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(caminho, ".go") || strings.HasSuffix(caminho, "_test.go") {
			return nil
		}
		conteudo, errL := os.ReadFile(caminho)
		if errL != nil {
			return errL
		}
		vistas := 0
		for _, agulha := range agulhas {
			vistas += strings.Count(string(conteudo), agulha)
		}
		if vistas == 0 {
			return nil
		}
		rel, errR := filepath.Rel(raiz, caminho)
		if errR != nil {
			return errR
		}
		rel = filepath.ToSlash(rel)
		permitido, ok := abridoresDeEscritaPermitidos[rel]
		switch {
		case !ok:
			intrusos = append(intrusos, fmt.Sprintf("%s (%d chamada(s), nenhuma declarada)", rel, vistas))
		case vistas != permitido.chamadas:
			intrusos = append(intrusos, fmt.Sprintf("%s (%d chamada(s), %d declarada(s): %s)", rel, vistas, permitido.chamadas, permitido.razao))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("varrer a árvore: %v", err)
	}

	if len(intrusos) > 0 {
		t.Fatalf("abridor de ESCRITA do Event Store fora da lista nomeada: %v\n"+
			"Se a via é de LEITURA, use eventstore.OpenReadOnly — Open/Reopen TRUNCAM a cauda "+
			"e anexam o WAL em append (AOS-347/AOS-359). Se é mesmo de escrita, acrescente-a a "+
			"abridoresDeEscritaPermitidos com a razão.", intrusos)
	}
}

// TestAOS359_AListaNaoTemEntradasMortas impede o outro modo de caducar: uma lista que
// cresce e nunca encolhe deixa de descrever a árvore e passa a descrever a história.
func TestAOS359_AListaNaoTemEntradasMortas(t *testing.T) {
	raiz := raizDoRepo(t)
	for rel, permitido := range abridoresDeEscritaPermitidos {
		conteudo, err := os.ReadFile(filepath.Join(raiz, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("entrada %q (%s) já não existe na árvore: %v", rel, permitido.razao, err)
		}
		if !strings.Contains(string(conteudo), "eventstore.Open(") && !strings.Contains(string(conteudo), "eventstore.Reopen(") {
			t.Fatalf("entrada %q (%s) já não abre o Event Store para escrita — tire-a da lista", rel, permitido.razao)
		}
	}
}
