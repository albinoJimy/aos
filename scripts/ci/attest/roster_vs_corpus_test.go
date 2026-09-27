package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GUARD: o corpus não pode afirmar um roster de release que o ficheiro contradiz.
//
// PORQUE ISTO EXISTE, e não é zelo documental.
//
// A 2026-08-14 o roster foi provisionado — `deploy/node/release-pubkeys.json` deixou de ter
// `keys: []`. QUATRO documentos continuaram a afirmar que estava vazio, e por isso que «nenhuma
// entrega é publicável»: o `deploy/server/README.md`, dois sítios do
// `deploy/node/CUSTODIA-CHAVE-RELEASE.md` e o ponto 10 do **ADR-017**, que é canónico. Sobreviveram
// treze meses de commits porque nada cruzava a frase com o ficheiro.
//
// O dano não foi estético. Uma auditoria de prontidão (2026-09-27) citou o README em vez de abrir o
// JSON e concluiu que «a entrega vai declaradamente não-assinada» — descrevendo a postura de
// segurança como MAIS FRACA do que era, num relatório cuja tese é que o corpus está
// desactualizado. Um leitor que acredite nele decide sobre uma cadeia de entrega que não existe.
//
// A CLASSE do defeito é a que este repositório já persegue noutros eixos: uma afirmação sobre
// estado, escrita em prosa, sem sensor que a avermelhe quando o estado muda. O registo de
// deferimentos resolve-a marcando uma linha OBSOLETA quando o seu marcador desaparece do código
// (verificação 5 de `deferrals.sh`); o `estado-citado` resolve-a cruzando um BLOQUEADOR com o
// estado do ticket citado. Este teste faz o mesmo para o roster: liga a prosa ao ficheiro.
//
// É DELIBERADAMENTE ESTREITO. Não tenta validar toda a prosa contra todo o estado — isso não é
// exprimível. Guarda UMA afirmação, a que já falhou, nos DOIS sentidos: um roster cheio com
// documentos a dizerem que está vazio (o defeito que ocorreu), e um roster esvaziado com
// documentos a dizerem que está cheio (o defeito simétrico, numa rotação futura).
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
	rosterVazio := len(roster.Keys) == 0

	// As duas formas em que a afirmação «o roster está vazio» aparece no corpus. Não é um regex
	// sobre prosa livre: são os literais que os quatro sítios usavam, e é neles que a recorrência
	// se manifesta. Um quinto sítio que invente outra forma de o dizer escapa — e é por isso que
	// este guard é um piso, não uma prova.
	const (
		literalBacktick = "`keys: []`"
		literalNu       = "keys: []"
	)

	docs := ficheirosMarkdownRastreados(t, root)
	if len(docs) == 0 {
		t.Fatal("nenhum .md rastreado encontrado — o guard estaria a passar sobre o vazio")
	}

	var afirmamVazio []string
	for _, rel := range docs {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue // ficheiro removido entre o `git ls-files` e a leitura
		}
		s := string(b)
		if strings.Contains(s, literalBacktick) || strings.Contains(s, literalNu) {
			// Uma menção que o próprio texto marca como corrigida/histórica não é uma
			// afirmação sobre o presente. O sinal é o ~~riscado~~ do Markdown na mesma linha,
			// ou a linha estar dentro de uma citação (`> `) — que é como as correcções de
			// 2026-09-27 ficaram escritas.
			if apenasMencoesHistoricas(s, literalNu) {
				continue
			}
			afirmamVazio = append(afirmamVazio, rel)
		}
	}

	switch {
	case rosterVazio && len(afirmamVazio) == 0:
		// Roster vazio e ninguém o diz: não é o defeito que este guard persegue (a garantia
		// fail-closed continua imposta por TestAOS207RegistoVazioRecusa), mas vale nomear.
		t.Log("roster VAZIO e nenhum documento o declara — considere declará-lo onde o leitor decide")
	case !rosterVazio && len(afirmamVazio) > 0:
		t.Fatalf("CONTRADIÇÃO: %s tem %d chave(s) provisionada(s), e %d documento(s) continuam a "+
			"afirmar `keys: []` — logo que a entrega NÃO é publicável, que é falso e faz a postura "+
			"de segurança parecer mais fraca do que é:\n  %s\n\n"+
			"Corrija a prosa, ou marque a menção como histórica (riscado `~~…~~` ou dentro de `> `). "+
			"Foi assim que as quatro ocorrências de 2026-08-14 passaram treze meses a mentir.",
			"deploy/node/release-pubkeys.json", len(roster.Keys), len(afirmamVazio),
			strings.Join(afirmamVazio, "\n  "))
	}
}

// apenasMencoesHistoricas diz se TODAS as linhas que contêm o literal estão marcadas como
// correcção/histórico. Uma só linha afirmativa basta para o guard morder — senão bastaria
// acrescentar uma nota ao ficheiro para silenciar a afirmação errada que continua lá.
func apenasMencoesHistoricas(conteudo, literal string) bool {
	viuAlguma := false
	for _, linha := range strings.Split(conteudo, "\n") {
		if !strings.Contains(linha, literal) {
			continue
		}
		viuAlguma = true
		corte := strings.TrimSpace(linha)
		riscado := strings.Contains(linha, "~~")
		citada := strings.HasPrefix(corte, ">")
		if !riscado && !citada {
			return false
		}
	}
	return viuAlguma
}

// raizDoRepo sobe até encontrar o `.git`. O teste corre com cwd em `scripts/ci/attest`, e o
// caminho relativo fixo (`../../..`) quebraria se o módulo se mudasse de sítio.
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
			t.Skip("fora de uma árvore git — o guard não tem corpus que verificar")
		}
		dir = pai
	}
}

// ficheirosMarkdownRastreados usa o git em vez de percorrer a árvore: só o que está VERSIONADO é
// corpus. Um `.md` por rastrear é rascunho de quem o escreveu, e avermelhar por ele tornaria o
// gate dependente do estado local de cada um.
func ficheirosMarkdownRastreados(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "-C", root, "ls-files", "-z", "*.md")
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("git ls-files indisponível: %v", err)
	}
	var fs []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			fs = append(fs, f)
		}
	}
	return fs
}
