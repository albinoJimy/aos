package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// AOS-222 — GUARDA DE VERACIDADE do fencing no caminho de posse (achado #10).
//
// O nó ANUNCIAVA "fencing" no caminho de posse (`hostRun`/`heartbeat` em service.go) mas NÃO
// compõe o [durable.FencedAppender]/[worker.Worker] — não há chamador de produção de
// `durable.NewFencedAppender` nem de `worker.NewWorker` no processo do nó (dark-code). O
// anti-duplo-efeito REAL naquele caminho é a soma de (1) posse por CAS atómico do lease
// (worker.Assigner), (2) cancel cooperativo e (3) idempotência do step-ledger (chave =
// f(RunID,StepID)). Afirmar que um "fencing barra/rejeita as escritas tardias" é, portanto, um
// defeito de VERACIDADE do log/comentário. Ver ADR-018 §5-bis.
//
// ÂNCORA (justificação). O defeito é de VERACIDADE do SOURCE — comentários e strings de log são
// texto estático do ficheiro; a barreira que se alega (ou não) vive nesse texto, não num
// comportamento observável fácil de forçar sem uma corrida de lease timing-dependente. Por isso
// a guarda VARRE O SOURCE do caminho de posse (as funções `hostRun` e `heartbeat`, isoladas por
// AST — doc-comment + comentários do corpo + literais de string/log do corpo) em vez de tentar
// disparar uma perda de lease. É determinística (-race trivial, sem timing) e inspecciona o
// PRÓPRIO literal do log (não um proxy).
//
// NÃO-FRÁGIL / NÃO-TAUTOLÓGICA. A guarda não procura a string literal que o fix apagou; procura
// a ASSINATURA SEMÂNTICA da afirmação falsa — um lexema de fencing ("fencing"/"fenced") +
// referência a escritas ("escrit") + um verbo de barreira ("barr"/"rejeit"/"impede"/"bloque") na
// MESMA linha. Assim: (a) apanha qualquer reformulação de "o fencing barra/rejeita/impede as
// escritas", não só as duas frases originais; (b) NÃO trip a menção legítima do "fencing token"
// do lease (que carece de "escrit"), que continua verdadeira em hostRun (é o token threaded ao
// stateGate de AOS-218). O detector é ele próprio testado nos dois sentidos em
// TestAOS222_ClaimDetector_TwoSided.
//
// CONDICIONAL / PREMISSA. A proibição só se aplica ENQUANTO o nó não compõe o FencedAppender.
// Se um dia o pacote do nó o compuser (chamada a NewFencedAppender/worker.NewWorker), o claim de
// fencing passa a ser legítimo e a guarda auto-desactiva-se (skip), lembrando o mantenedor de
// actualizar a narrativa.
//
// FALHA-ANTES (prova de falsificabilidade): com o log/comentário ORIGINAIS ("o fencing
// rejeitaria as nossas escritas tardias" / "fencing barra escritas tardias") a guarda FALHA
// (duas ocorrências); depois do fix (que nomeia o mecanismo real) a guarda PASSA.

type aos222Piece struct {
	line int
	text string
	kind string
}

// claimsFencingBarsWrites reporta se UMA linha afirma que um fencing BARRA/REJEITA as ESCRITAS
// — a assinatura semântica do claim falso que AOS-222 corrige. Exige os três elementos na mesma
// linha (fencing + escrita + verbo de barreira), de modo a apanhar qualquer reformulação sem
// falso-positivar a menção legítima do "fencing token" do lease.
func claimsFencingBarsWrites(line string) bool {
	l := strings.ToLower(line)
	hasFencing := strings.Contains(l, "fencing") || strings.Contains(l, "fenced")
	hasWrite := strings.Contains(l, "escrit")
	hasBarrier := strings.Contains(l, "barr") || strings.Contains(l, "rejeit") ||
		strings.Contains(l, "impede") || strings.Contains(l, "bloque")
	return hasFencing && hasWrite && hasBarrier
}

// nodeComposesFencedAppender indica se o PACOTE do nó (ficheiros .go não-teste do comando) já
// cabla o enforcement de fencing de escritas — chamando NewFencedAppender ou worker.NewWorker.
// Enquanto for false, o caminho de posse não pode alegar um fencing de escritas.
func nodeComposesFencedAppender(t *testing.T) bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir do pacote do nó: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		s := string(b)
		if strings.Contains(s, "NewFencedAppender") || strings.Contains(s, "worker.NewWorker") {
			return true
		}
	}
	return false
}

// TestAOS222_OwnershipPathDoesNotClaimUncomposedFencing é a guarda de veracidade: nenhum
// comentário/log do caminho de posse (hostRun/heartbeat em service.go) pode afirmar que um
// fencing barra/rejeita as escritas enquanto o FencedAppender não estiver composto no nó.
func TestAOS222_OwnershipPathDoesNotClaimUncomposedFencing(t *testing.T) {
	const srcFile = "service.go"

	// Premissa condicional: se o nó já compõe o FencedAppender, o claim passa a ser legítimo.
	if nodeComposesFencedAppender(t) {
		t.Skip("o pacote do nó compõe FencedAppender/worker.Worker: o claim de fencing de escritas " +
			"passa a ser legítimo — actualizar/retirar esta guarda e a narrativa (ADR-018 §5-bis).")
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, srcFile, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", srcFile, err)
	}

	targets := map[string]bool{"hostRun": true, "heartbeat": true}
	var pieces []aos222Piece

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !targets[fn.Name.Name] {
			continue
		}
		// doc-comment da função
		if fn.Doc != nil {
			for _, c := range fn.Doc.List {
				pieces = append(pieces, aos222Piece{fset.Position(c.Pos()).Line, c.Text, "doc-comment de " + fn.Name.Name})
			}
		}
		bodyStart, bodyEnd := fn.Body.Pos(), fn.Body.End()
		// comentários DENTRO do corpo
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if c.Pos() >= bodyStart && c.End() <= bodyEnd {
					pieces = append(pieces, aos222Piece{fset.Position(c.Pos()).Line, c.Text, "comentário no corpo de " + fn.Name.Name})
				}
			}
		}
		// literais de string (incl. formatos de log) DENTRO do corpo
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				pieces = append(pieces, aos222Piece{fset.Position(lit.Pos()).Line, lit.Value, "literal/log no corpo de " + fn.Name.Name})
			}
			return true
		})
	}

	// Guarda-da-guarda: se a âncora AST não recolher nada, um refactor mudou os nomes das
	// funções e a guarda ficou cega — falha em vez de passar vacuamente.
	if len(pieces) == 0 {
		t.Fatalf("guarda cega: nenhum comentário/literal recolhido de hostRun/heartbeat em %s "+
			"(as funções foram renomeadas? actualizar a âncora)", srcFile)
	}

	var violations []string
	for _, p := range pieces {
		for i, line := range strings.Split(p.text, "\n") {
			if claimsFencingBarsWrites(line) {
				violations = append(violations, fmt.Sprintf("  %s:%d — %s: %q", srcFile, p.line+i, p.kind, strings.TrimSpace(line)))
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("VERACIDADE (AOS-222): o caminho de posse afirma que um fencing barra/rejeita as "+
			"escritas, mas o nó NÃO compõe o FencedAppender (ver ADR-018 §5-bis). O anti-duplo-efeito "+
			"real é lease-CAS (worker.Assigner) + cancel cooperativo + idempotência do step-ledger "+
			"f(RunID,StepID). Ocorrências:\n%s", strings.Join(violations, "\n"))
	}
}

// TestAOS222_ClaimDetector_TwoSided prova o detector nos DOIS sentidos (não-tautológico): apanha
// as afirmações falsas (as originais + variações semânticas) e NÃO apanha as menções legítimas
// nem o texto já corrigido.
func TestAOS222_ClaimDetector_TwoSided(t *testing.T) {
	// DEVE apanhar — afirmações de que um fencing barra/rejeita/impede/bloqueia as escritas:
	positives := []string{
		"(cancel): já não somos o dono, e o fencing rejeitaria as nossas escritas tardias; parar", // original (comentário)
		"a cancelar (sem duplo-efeito; fencing barra escritas tardias)",                           // original (log)
		"o fencing impede as escritas obsoletas do detentor superado",                             // variação semântica
		"o appender fenced bloqueia a escrita tardia no ponto de append",                          // variação semântica
	}
	for _, s := range positives {
		if !claimsFencingBarsWrites(s) {
			t.Errorf("detector FALHOU a apanhar uma afirmação falsa: %q", s)
		}
	}

	// NÃO deve apanhar — menções legítimas do token do lease, ausência declarada, ou o texto
	// corrigido (que nomeia o mecanismo real sem alegar um fencing de escritas):
	negatives := []string{
		"com o fencing token do lease JÁ detido (rs.lease foi",          // token real threaded ao stateGate (AOS-218)
		"este caminho NAO compoe fencing de escritas",                   // texto corrigido do log (sem verbo de barreira)
		"o nó NÃO compõe o [durable.FencedAppender] (eixo nomeado)",     // declara a AUSÊNCIA
		"a idempotência do step-ledger deduplica o efeito já aplicado",  // mecanismo real (sem fencing)
		"a barreira REAL é a posse por CAS atómico do lease e o cancel", // fala de barreira, mas sem fencing+escrita
		"o lease durável não minta token (reutiliza a cópia capturada)", // sem fencing/escrita
	}
	for _, s := range negatives {
		if claimsFencingBarsWrites(s) {
			t.Errorf("detector FALSO-POSITIVO em texto legítimo/corrigido: %q", s)
		}
	}
}
