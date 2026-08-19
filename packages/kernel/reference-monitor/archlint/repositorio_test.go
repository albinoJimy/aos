package archlint

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// A REGRA AOS-003 APLICADA AO REPOSITÓRIO INTEIRO.
//
// `TestArchLint_RMNaoTemBypass` corre `AnalyzeDir("..")` — que NÃO é recursivo — pelo que a única
// imposição desta regra cobria os ficheiros directamente em `packages/kernel/reference-monitor/`.
// Entretanto o inventário de conceitos afirmava, sobre o eixo C, que «as tools só entram pelo
// MediatedLauncher», e citava como prova uma linha de BANNER: o nó a descrever a sua própria
// cablagem.
//
// Uma auditoria adversarial a esse inventário (2026-08-19) apanhou a discrepância. A afirmação era
// mais larga do que a verificação — o que não quer dizer que fosse falsa; quer dizer que ninguém
// saberia se deixasse de o ser.
//
// Este teste fecha a distância: percorre TODOS os pacotes e exige zero violações fora de uma lista
// explícita, curta e justificada.
// ---------------------------------------------------------------------------

// excepcao é uma violação CONHECIDA e aceite, com a razão pela qual não é um bypass.
type excepcao struct {
	ficheiro string // caminho relativo à raiz, com "/"
	tipo     string
	porque   string
}

// excepcoes são as três colisões de NOME que a regra produz, e nenhuma é despacho de tool.
//
// A regra sinaliza qualquer chamada a um identificador chamado `dispatch` (e sinónimos). É
// deliberadamente grosseira — prefere um falso positivo a um bypass por passar despercebido —, e o
// preço são estas entradas. Cada uma foi lida à mão.
var excepcoes = []excepcao{
	{
		ficheiro: "packages/kernel/reference-monitor/monitor.go",
		tipo:     "forbidden-dispatch",
		porque: "o `m.dispatch` do PRÓPRIO Reference Monitor — é o dispatcher mediado, não um " +
			"desvio dele. `TestArchLint_RMNaoTemBypass` já o filtrava da mesma maneira.",
	},
	{
		ficheiro: "packages/cmd/aos/main.go",
		tipo:     "forbidden-dispatch",
		porque: "o despachante de SUBCOMANDOS da CLI (`dispatch(os.Args[1:], os.Stdout)`). Não " +
			"toca em tools: escolhe qual comando do binário corre.",
	},
	{
		ficheiro: "packages/control-plane/scheduler/scheduler.go",
		tipo:     "forbidden-dispatch",
		porque: "método próprio do Scheduler que ENTREGA eventos agendados, com guard de " +
			"idempotência por step_id. O efeito da tool continua a passar pelo RM.",
	},
}

// TestArchLint_NenhumBypassNoRepositorio é o gate que faltava.
func TestArchLint_NenhumBypassNoRepositorio(t *testing.T) {
	raiz := raizDoRepositorio(t)

	violacoes, err := AnalyzeTree(filepath.Join(raiz, "packages"))
	if err != nil {
		t.Fatalf("AnalyzeTree: %v", err)
	}

	// CONTROLO DE ÂMBITO: se o varredor deixasse de encontrar ficheiros, o teste passaria a verde
	// sem verificar nada — que é exactamente o defeito que ele existe para fechar.
	if n := pacotesAnalisados(t, filepath.Join(raiz, "packages")); n < 100 {
		t.Fatalf("so analisei %d pacotes — o varredor partiu-se e este gate seria vacuoso", n)
	}

	conhecidas := map[string]excepcao{}
	for _, e := range excepcoes {
		conhecidas[e.ficheiro+"|"+e.tipo] = e
	}

	vistas := map[string]bool{}
	var novas []string
	for _, v := range violacoes {
		rel := strings.ReplaceAll(strings.TrimPrefix(filepath.ToSlash(v.File), filepath.ToSlash(raiz)+"/"), "\\", "/")
		chave := rel + "|" + v.Kind
		if _, ok := conhecidas[chave]; ok {
			vistas[chave] = true
			continue
		}
		novas = append(novas, v.Kind+"  "+rel+":"+itoa(v.Line))
	}

	sort.Strings(novas)
	if len(novas) > 0 {
		t.Errorf("BYPASS do Reference Monitor fora da lista de excepcoes:\n  %s\n\n"+
			"Uma tool que nao passe pelo caminho mediado nao e decidida pelo PDP, nao entra no\n"+
			"orcamento e NAO E SELADA no WORM. Se a chamada for legitima, acrescente-a a\n"+
			"`excepcoes` COM A RAZAO — a lista existe para ser lida, nao para crescer.",
			strings.Join(novas, "\n  "))
	}

	// CONTROLO ANTI-APODRECIMENTO: uma excepção que já não reproduz é ruído que passa a esconder o
	// que vier a seguir no mesmo ficheiro. Sem isto, a lista só cresce.
	for chave, e := range conhecidas {
		if !vistas[chave] {
			t.Errorf("a excepcao %q ja NAO reproduz — remova-a (%s)", chave, e.porque)
		}
	}
}

// TestArchLint_InvocacaoDirectaDeToolFuncEZero fixa a metade FORTE da regra.
//
// `ToolFunc` é EXPORTADO: qualquer pacote lhe pode chamar directamente, e é esse o bypass que
// importa — o `dispatch` do RM é minúsculo e Go já o esconde. Uma invocação directa de `ToolFunc`
// executa o efeito SEM decisão do PDP, SEM orçamento e SEM selo no WORM.
//
// Hoje são ZERO em todo o repositório. Este teste existe para que continue a ser um facto
// verificado, e não uma coincidência.
func TestArchLint_InvocacaoDirectaDeToolFuncEZero(t *testing.T) {
	raiz := raizDoRepositorio(t)
	violacoes, err := AnalyzeTree(filepath.Join(raiz, "packages"))
	if err != nil {
		t.Fatalf("AnalyzeTree: %v", err)
	}
	var directas []string
	for _, v := range violacoes {
		if v.Kind == "tool-func-invocation" {
			directas = append(directas, v.String())
		}
	}
	if len(directas) > 0 {
		t.Errorf("invocacao DIRECTA de ToolFunc (executa o efeito sem PDP, sem orcamento e sem selo):\n  %s",
			strings.Join(directas, "\n  "))
	}
}

// raizDoRepositorio sobe até encontrar o `.git`, em vez de contar `..` — um caminho relativo
// fixo parte-se em silêncio quando o pacote muda de sítio, e um gate que não encontra a árvore
// passa a verde sem verificar nada.
func raizDoRepositorio(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		pai := filepath.Dir(dir)
		if pai == dir {
			break
		}
		dir = pai
	}
	t.Fatal("nao encontrei a raiz do repositorio (.git) a subir a partir de archlint/")
	return ""
}

func pacotesAnalisados(t *testing.T, root string) int {
	t.Helper()
	n := 0
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() {
			return nil
		}
		switch info.Name() {
		case ".git", "vendor", "node_modules", "testdata":
			return filepath.SkipDir
		}
		n++
		return nil
	})
	return n
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
