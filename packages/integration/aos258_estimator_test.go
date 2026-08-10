package integration

// AOS-258 — provas do ESTIMADOR REAL (o que ele conta, o que não conta, e que o placeholder
// deixou de correr em produção).
//
// A prova de nó (permit dentro do tecto / deny além dele, com `denied_by=budget` selado e
// atribuído no WORM) vive em `packages/cmd/aos/aos258_budget_permit_node_test.go`: é lá que
// há um [main.Bootstrap] real. Aqui provam-se as PROPRIEDADES do estimador — as que um teste
// de nó não consegue isolar.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	budget "github.com/aos-ref/control-plane/budget"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// estCall é uma Call materializada mínima com o envelope preenchido.
func estCall(input string) *referencemonitor.Call {
	return &referencemonitor.Call{
		ToolID:     "doc_read",
		Capability: "cap:fs.read",
		Resource:   referencemonitor.Resource{Type: "doc", Value: "notes", Region: "eu"},
		Input:      []byte(input),
	}
}

// TestAOS258_EstimaAPegadaMaterializadaENaoSoOsArgumentos é o primeiro eixo do critério (a):
// o estimador conta o que a tool call MATERIALIZA na transcrição, e não apenas o payload.
//
// Sem isto, uma tool call cujo peso está no RECURSO (um URL longo) seria estimada como se
// fosse grátis — e o [budget.DefaultEstimator], que só via `call.Input`, fazia exactamente
// isso.
func TestAOS258_EstimaAPegadaMaterializadaENaoSoOsArgumentos(t *testing.T) {
	t.Parallel()

	nu := &referencemonitor.Call{Input: []byte(`{"doc_id":"notes"}`)}
	comEnvelope := estCall(`{"doc_id":"notes"}`)

	if TokenOnlyEstimator(comEnvelope).Tokens <= TokenOnlyEstimator(nu).Tokens {
		t.Errorf("a MESMA call com tool_id/capability/resource preenchidos devia estimar MAIS (nu=%d, com envelope=%d): o envelope ocupa contexto tanto quanto os argumentos",
			TokenOnlyEstimator(nu).Tokens, TokenOnlyEstimator(comEnvelope).Tokens)
	}

	// O peso do RECURSO tem de mover a estimativa — é o caso que o placeholder ignorava.
	curto := estCall(`{}`)
	longo := estCall(`{}`)
	longo.Resource.Value = "https://api.example.com/v1/orders?expand=lines&limit=1000&cursor=abcdef0123456789"
	if TokenOnlyEstimator(longo).Tokens <= TokenOnlyEstimator(curto).Tokens {
		t.Errorf("um recurso de %d bytes nao moveu a estimativa (curto=%d, longo=%d) — o estimador estaria a ignorar o envelope",
			len(longo.Resource.Value), TokenOnlyEstimator(curto).Tokens, TokenOnlyEstimator(longo).Tokens)
	}
}

// TestAOS258_NuncaSubestimaOPlaceholder é a propriedade de SEGURANÇA da substituição: trocar
// [budget.DefaultEstimator] pelo estimador real só pode APERTAR o tecto, nunca afrouxá-lo.
//
// Uma troca que baixasse a estimativa nalgum payload seria um relaxamento SILENCIOSO de um
// controlo de admissão: o mesmo tecto passaria a deixar passar mais gasto, e nada no banner
// ou na config o denunciaria.
func TestAOS258_NuncaSubestimaOPlaceholder(t *testing.T) {
	t.Parallel()

	payloads := []string{
		"",
		"a",
		"the quick brown fox jumps over the lazy dog",
		`{"doc_id":"notes","limit":100,"expand":["lines","totals"]}`,
		"aGVsbG8gd29ybGQgdGhpcyBpcyBiYXNlNjQgcGFkZGluZw==",
		strings.Repeat(" ", 200),                       // degenerado: só brancos (o piso de bytes é quem decide)
		strings.Repeat("x", 4096),                      // uma corrida alfanumérica muito longa
		"relatório trimestral — ação, avaliação, ções", // latino acentuado
		"这是一个中文的工具呼叫参数",                                // CJK
	}

	for _, p := range payloads {
		call := estCall(p)
		estimado := TokenOnlyEstimator(call).Tokens
		placeholder := budget.DefaultEstimator(call).Tokens
		if estimado < placeholder {
			t.Errorf("o estimador real SUBESTIMA o placeholder num payload de %d bytes (estimado=%d, placeholder=%d): a troca afrouxaria o tecto em silencio\npayload=%.60q",
				len(p), estimado, placeholder, p)
		}
	}
}

// TestAOS258_ElevaOsPayloadsEstruturados prova a razão de o estimador existir: `bytes/4`
// SUBESTIMA JSON/URLs, onde quase todo o byte estrutural é um token por si só. Subestimar é a
// direcção fail-OPEN de um controlo de admissão.
func TestAOS258_ElevaOsPayloadsEstruturados(t *testing.T) {
	t.Parallel()

	estruturado := &referencemonitor.Call{Input: []byte(`{"a":1,"b":2,"c":[3,4,5]}`)}
	estimado := TokenOnlyEstimator(estruturado).Tokens
	placeholder := budget.DefaultEstimator(estruturado).Tokens
	if estimado <= placeholder {
		t.Errorf("num payload JSON denso o estimador real devia ser ESTRITAMENTE maior que a heuristica de bytes (estimado=%d, placeholder=%d) — se for igual, o estimador nao esta a contar a estrutura e o ticket nao mudou nada", estimado, placeholder)
	}
}

// TestAOS258_Monotono sela a propriedade sem a qual um estimador de orçamento é contornável:
// acrescentar bytes NUNCA pode baixar a estimativa.
func TestAOS258_Monotono(t *testing.T) {
	t.Parallel()

	base := `{"doc_id":"`
	anterior := int64(0)
	for i := 0; i < 256; i++ {
		call := estCall(base + strings.Repeat("a", i) + `"}`)
		agora := TokenOnlyEstimator(call).Tokens
		if agora < anterior {
			t.Fatalf("estimativa DESCEU ao crescer o payload (i=%d: %d < %d) — um payload maior nunca pode custar menos", i, agora, anterior)
		}
		anterior = agora
	}
}

// TestAOS258_NuncaZeroENuncaDolares fecha os dois invariantes de forma da quantia devolvida.
//
// Zero seria fatal por uma razão pouco óbvia: [budget.Amount.validReserve] recusa uma reserva
// nula, o adaptador converte a recusa em deny — e uma tool call de argumentos vazios negaria
// TUDO. Dólares seriam a capacidade-fantasma que AOS-255 existe para impedir.
func TestAOS258_NuncaZeroENuncaDolares(t *testing.T) {
	t.Parallel()

	for nome, call := range map[string]*referencemonitor.Call{
		"nil":           nil,
		"tudo vazio":    {},
		"input vazio":   {ToolID: "t"},
		"so envelope":   estCall(""),
		"call completa": estCall(`{"doc_id":"notes"}`),
	} {
		amt := TokenOnlyEstimator(call)
		if amt.Tokens < 1 {
			t.Errorf("%s: estimativa de %d tokens — uma reserva nula e invalida e o adaptador converte-a em DENY (tudo negado)", nome, amt.Tokens)
		}
		if amt.CostMicroUSD != 0 {
			t.Errorf("%s: CostMicroUSD=%d — a dimensao em dolares TEM de ficar a zero enquanto o canal de custo nao estiver ligado ponta a ponta (AOS-259); um tecto em $ comparado com consumo contado a zero e uma capacidade-fantasma", nome, amt.CostMicroUSD)
		}
	}
}

// TestAOS258_ProducaoNaoUsaODefaultEstimator é o critério (c) do ticket, mecanizado: o
// [budget.DefaultEstimator] — que o seu próprio autor documenta como PLACEHOLDER — deixa de
// ser usado em produção.
//
// Por AST e não por grep, pela mesma razão do gate de AOS-255: este ficheiro e o
// `budget_estimator.go` NOMEIAM o DefaultEstimator em prosa (é preciso dizer o que se
// substituiu), e um gate que confundisse a menção com a chamada avermelhava sem defeito.
func TestAOS258_ProducaoNaoUsaODefaultEstimator(t *testing.T) {
	t.Parallel()

	// As duas árvores que compõem o nó de produção: a cadeia (`integration`) e o
	// composition-root (`cmd/aos`).
	for _, raiz := range []string{".", "../cmd/aos"} {
		if onde := chamaDefaultEstimator(t, raiz); onde != "" {
			t.Errorf("%s chama budget.DefaultEstimator — e um PLACEHOLDER (so ve call.Input e inventa uma tarifa fixa de 10 micro-USD/token). Producao usa [TokenOnlyEstimator] (AOS-258); se o estimador real foi removido, o banner tem de deixar de o anunciar.", onde)
		}
	}
}

// chamaDefaultEstimator devolve "ficheiro:linha" da primeira referência ao identificador
// DefaultEstimator em código NÃO-teste sob root, ou "" se não houver.
func chamaDefaultEstimator(t *testing.T, root string) string {
	t.Helper()

	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		t.Fatalf("arvore %q inacessivel (%v) — se o codigo mudou de sitio actualize a lista; o gate NAO pode ignora-la em silencio", root, err)
	}

	fset := token.NewFileSet()
	ficheiros := 0
	var achado string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		nome := d.Name()
		if !strings.HasSuffix(nome, ".go") || strings.HasSuffix(nome, "_test.go") {
			return nil
		}
		ficheiros++
		file, perr := parser.ParseFile(fset, path, nil, 0) // sem ParseComments: a prosa não conta
		if perr != nil {
			t.Fatalf("parser.ParseFile(%q): %v", filepath.ToSlash(path), perr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "DefaultEstimator" || achado != "" {
				return true
			}
			pos := fset.Position(sel.Pos())
			achado = filepath.ToSlash(path) + ":" + strconv.Itoa(pos.Line)
			return false
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("WalkDir(%q): %v", root, walkErr)
	}
	if ficheiros == 0 {
		t.Fatalf("nenhum ficheiro .go nao-teste varrido em %q — o gate ficaria vacuamente verde", root)
	}
	return achado
}
