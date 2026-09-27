package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// GUARD: o corpus não pode afirmar um roster de release que o ficheiro contradiz.
//
// PORQUE ISTO EXISTE, e não é zelo documental.
//
// A 2026-08-14 o roster foi provisionado — `deploy/node/release-pubkeys.json` deixou de ter as
// chaves vazias. CINCO documentos continuaram a afirmar que estava vazio, e por isso que «nenhuma
// entrega é publicável»: o `deploy/server/README.md`, dois sítios do
// `deploy/node/CUSTODIA-CHAVE-RELEASE.md`, o ponto 10 do **ADR-017** (canónico) e a §750 do
// `specs/EPIC-18`. Sobreviveram porque nada cruzava a frase com o ficheiro.
//
// O dano não foi estético. Uma auditoria de prontidão (2026-09-27) citou o README em vez de abrir o
// JSON e concluiu que «a entrega vai declaradamente não-assinada» — descrevendo a postura de
// segurança como MAIS FRACA do que era. Um leitor que acredite nele decide sobre uma cadeia de
// entrega que não existe.
//
// ---------------------------------------------------------------------------
// A PRIMEIRA VERSÃO DESTE GUARD NASCEU VERDE SOBRE O DEFEITO QUE PERSEGUIA.
//
// Procurava os literais `keys: []` e "`keys: []`" — com o espaço. O `specs/EPIC-18:750` escreve
// `keys:[]`, sem espaço: o QUINTO sítio, que não é «outra forma de o dizer» mas a mesma frase
// menos um carácter. O guard passava, e o «verificado por mutação» que o acompanhava só provou que
// apanhava a grafia que o autor tinha escrito à mão. Uma revisão adversarial independente mediu
// sete variantes de evasão (`keys:[]`, `keys: [ ]`, `"keys": []`, `keys : []`, bloco cercado…) e
// seis escapavam — entre elas a grafia do PRÓPRIO ficheiro JSON, que é a que qualquer pessoa
// copiaria ao explicar a semântica.
//
// Daí as três mudanças de desenho desta versão:
//
//  1. DETECÇÃO POR FORMA, NÃO POR LITERAL. [reRosterVazio] normaliza espaços e aspas. Enumerar
//     grafias à mão é o modo de falha que `packages/cmd/aos/planos.go` descreve nos seus próprios
//     comentários: um allowlist escrito à mão cujo esquecimento produz silêncio, e cujo detector
//     partilha o modo de falha do defeito que persegue.
//
//  2. ISENÇÃO EXPLÍCITA, NÃO HEURÍSTICA. A versão anterior tratava como «histórica» qualquer linha
//     com `~~` em qualquer posição, ou começada por `>`. Ambas eram exploráveis e a segunda era
//     pior do que inútil: `> **Nota:**` é a forma CANÓNICA deste repositório de escrever uma
//     afirmação ENFÁTICA SOBRE O PRESENTE (o próprio `AGENTS.md` a usa). O escape em uso era
//     exactamente o que qualquer afirmação falsa podia invocar — e dois tildes soltos no fim de uma
//     linha desarmavam o guard sem corrigir nada. Passa a ser um marcador nomeado
//     [marcadorHistorico], que ninguém escreve por acidente e que se encontra com um `grep`.
//
//  3. NÃO SALTA. A versão anterior fazia `t.Skip` se o `git ls-files` falhasse ou se não achasse
//     `.git` — e um `git` a recusar por `dubious ownership` (UID de CI ≠ dono do checkout) dava
//     PASS e saída 0 num gate BLOQUEANTE, com o `.md` culpado presente. `test.sh` e `lib.sh` não
//     observam `--- SKIP`, pelo que o salto não era redeclarado em sítio nenhum — ao contrário do
//     que o `AGENTS.md` §4 exige (`AOS_SKIPPED_STEP`). Um gate que não consegue medir FALHA.
//
// O QUE ESTE GUARD NÃO FAZ, declarado em vez de insinuado: é UNIDIRECCIONAL. Detecta «o corpus diz
// vazio, o ficheiro tem chaves». NÃO detecta o simétrico («o corpus diz provisionado, o ficheiro
// está vazio») — não há forma canónica de escrever essa afirmação, e um detector de prosa livre
// seria falso-positivo por construção. A versão anterior PROMETIA os dois sentidos no comentário e
// entregava um só, e emitia um `t.Log` a dizer «nenhum documento o declara» sem o ter verificado.
// ---------------------------------------------------------------------------

// reRosterVazio casa a afirmação «o roster não tem chaves» em qualquer grafia plausível: com ou sem
// aspas na chave, com ou sem espaços em torno dos dois pontos e dentro dos parênteses rectos. Cobre
// a grafia do próprio JSON (`"keys": []`), que é a que se copia ao explicar a semântica.
var reRosterVazio = regexp.MustCompile(`(?i)"?\bkeys"?\s*:\s*\[\s*\]`)

// marcadorHistorico isenta uma linha. Vai na própria linha ou na linha imediatamente acima.
//
// É um comentário HTML e não markup de estilo, por três razões: não se escreve por acidente, não
// colide com nenhuma convenção de escrita deste repositório, e encontra-se com
// `git grep roster:historico` — o que torna auditável QUANTAS isenções existem e onde. Uma isenção
// que se invoca sem intenção não é uma isenção, é um buraco.
const marcadorHistorico = "<!-- roster:historico -->"

func TestRosterDeReleaseNaoContradizOCorpus(t *testing.T) {
	root := raizDoRepo(t)

	rosterPath := filepath.Join(root, "deploy", "node", "release-pubkeys.json")
	raw, err := os.ReadFile(rosterPath)
	if err != nil {
		t.Fatalf("roster ilegível em %s: %v", rosterPath, err)
	}
	var roster struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(raw, &roster); err != nil {
		t.Fatalf("roster não é JSON válido: %v", err)
	}
	if len(roster.Keys) == 0 {
		// Roster vazio: a afirmação «está vazio» é VERDADEIRA e nada há a contradizer. A garantia
		// fail-closed continua imposta por TestAOS207RegistoVazioRecusa, que é o teste que prova
		// que um roster vazio recusa qualquer envelope.
		t.Log("roster VAZIO — este guard não se aplica (nada no corpus o contradiz)")
		return
	}

	docs := ficheirosDeDocumentacao(t, root)
	if len(docs) == 0 {
		t.Fatal("nenhum ficheiro de documentação rastreado — o guard estaria a passar sobre o vazio")
	}

	type achado struct {
		ficheiro string
		linha    int
		texto    string
	}
	var achados []achado

	for _, rel := range docs {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // removido entre o `git ls-files` e a leitura — a única corrida benigna
			}
			// Qualquer outro erro (permissões, por exemplo) esconderia um ficheiro culpado em
			// silêncio. Um guard que não consegue ler o corpus não sabe se o corpus está certo.
			t.Fatalf("não foi possível ler %s: %v", rel, err)
		}
		linhas := strings.Split(string(b), "\n")
		for i, linha := range linhas {
			if !reRosterVazio.MatchString(linha) {
				continue
			}
			if strings.Contains(linha, marcadorHistorico) {
				continue
			}
			if i > 0 && strings.Contains(linhas[i-1], marcadorHistorico) {
				continue
			}
			achados = append(achados, achado{rel, i + 1, strings.TrimSpace(linha)})
		}
	}

	if len(achados) == 0 {
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "CONTRADIÇÃO: deploy/node/release-pubkeys.json tem %d chave(s) provisionada(s), "+
		"e %d linha(s) do corpus afirmam que não tem nenhuma — logo que a entrega NÃO é publicável, "+
		"que é FALSO e faz a postura de segurança parecer mais fraca do que é.\n\n",
		len(roster.Keys), len(achados))
	for _, a := range achados {
		texto := a.texto
		if len(texto) > 160 {
			texto = texto[:160] + "…"
		}
		fmt.Fprintf(&b, "  %s:%d\n    %s\n", a.ficheiro, a.linha, texto)
	}
	fmt.Fprintf(&b, "\nEscolha UMA de duas: corrija a afirmação, ou — se ela descreve de propósito um "+
		"estado PASSADO — marque-a com `%s`, na própria linha ou na linha acima.\n"+
		"O marcador é explícito e auditável (`git grep roster:historico`) porque a versão anterior "+
		"deste guard aceitava `~~` em qualquer posição e qualquer linha começada por `>`, e ambos se "+
		"invocavam sem intenção — `> **Nota:**` é a forma canónica deste repo de AFIRMAR o presente.",
		marcadorHistorico)
	t.Fatal(b.String())
}

// raizDoRepo sobe até encontrar `.git`. Num worktree o `.git` é um FICHEIRO e não um directório:
// `os.Stat` não distingue, e é isso que se quer.
//
// FALHA em vez de saltar. Um gate bloqueante que não encontra o repositório não sabe nada sobre o
// corpus, e dizer PASS nesse estado é fail-open.
func raizDoRepo(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		pai := filepath.Dir(dir)
		if pai == dir {
			t.Fatal("não foi encontrada a raiz do repositório (`.git`) a partir do cwd — este guard " +
				"não pode verificar o corpus, e um gate bloqueante que não mede FALHA em vez de saltar")
		}
		dir = pai
	}
}

// ficheirosDeDocumentacao lista os ficheiros de documentação VERSIONADOS. Só o que está rastreado é
// corpus: um `.md` por rastrear é rascunho de quem o escreveu, e avermelhar por ele faria o gate
// depender do estado local de cada um.
//
// O pathspec cobre as três extensões plausíveis e usa `:(icase)` porque o pathspec do git é
// sensível à caixa e um `README.MD` seria invisível.
func ficheirosDeDocumentacao(t *testing.T, root string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "ls-files", "-z",
		":(icase)*.md", ":(icase)*.markdown", ":(icase)*.mdx").Output()
	if err != nil {
		// NÃO salta. `git ls-files` sai 128 em `dubious ownership` — UID de CI diferente do dono do
		// checkout —, e a versão anterior deste guard dava PASS e saída 0 nesse estado, num gate
		// bloqueante, com o ficheiro culpado presente e rastreado.
		t.Fatalf("`git ls-files` falhou em %s: %v — o guard não consegue enumerar o corpus, e um "+
			"gate bloqueante que não mede FALHA (ver `dubious ownership` em CI containerizado)", root, err)
	}
	var fs []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			fs = append(fs, f)
		}
	}
	return fs
}
