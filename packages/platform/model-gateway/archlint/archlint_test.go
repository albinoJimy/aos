package archlint_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/archlint"
)

// TestAnalyze_CasoBom: consumidor que só usa a porta do GW não é sinalizado.
func TestAnalyze_CasoBom(t *testing.T) {
	t.Parallel()
	vs, err := archlint.AnalyzeDir("testdata/good", archlint.Options{})
	if err != nil {
		t.Fatalf("AnalyzeDir(good): %v", err)
	}
	if len(vs) != 0 {
		t.Fatalf("caso BOM devia ter 0 violacoes, obtidas %d: %v", len(vs), vs)
	}
}

// TestAnalyze_CasoMau: acesso directo a provider (import de SDK + endpoint
// literal) é sinalizado nas duas formas.
func TestAnalyze_CasoMau(t *testing.T) {
	t.Parallel()
	vs, err := archlint.AnalyzeDir("testdata/bad", archlint.Options{})
	if err != nil {
		t.Fatalf("AnalyzeDir(bad): %v", err)
	}
	if len(vs) < 2 {
		t.Fatalf("caso MAU devia sinalizar >= 2 violacoes, obtidas %d: %v", len(vs), vs)
	}
	kinds := map[string]bool{}
	for _, v := range vs {
		kinds[v.Kind] = true
		if v.Line == 0 || v.File == "" {
			t.Errorf("violacao sem posicao: %+v", v)
		}
	}
	if !kinds["provider-sdk-import"] {
		t.Errorf("faltou sinalizar import de SDK de provider; violacoes=%v", vs)
	}
	if !kinds["provider-endpoint-literal"] {
		t.Errorf("faltou sinalizar endpoint de provider hard-coded; violacoes=%v", vs)
	}
}

// TestAnalyze_ImportAdaptadoresGW: importar o pacote de ADAPTADORES do próprio GW
// fora do GW é bypass da porta — deve ser sinalizado (defesa-em-profundidade da
// garantia estrutural internal/).
func TestAnalyze_ImportAdaptadoresGW(t *testing.T) {
	t.Parallel()
	vs, err := archlint.AnalyzeDir("testdata/badgwadapters", archlint.Options{})
	if err != nil {
		t.Fatalf("AnalyzeDir(badgwadapters): %v", err)
	}
	found := false
	for _, v := range vs {
		if v.Kind == "provider-sdk-import" && strings.Contains(v.Message, "adapters") {
			found = true
		}
	}
	if !found {
		t.Fatalf("import dos adaptadores do GW devia ser sinalizado; violacoes=%v", vs)
	}
}

// TestAnalyzeTree_IsencaoPorSegmento prova que a isenção do GW é por FRONTEIRA de
// segmento, não por substring solta: um consumidor sob um caminho que meramente
// CONTÉM "model-gateway" (ex.: "foo-model-gateway-client") NÃO é isentado — só o
// directório cujo elemento de caminho é exactamente "model-gateway".
func TestAnalyzeTree_IsencaoPorSegmento(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Consumidor camuflado: o path contém "model-gateway" como substring mas NÃO
	// como segmento — deve ser analisado e sinalizado.
	src := "package c\nimport _ \"github.com/sashabaranov/go-openai\"\n"
	mustWrite(t, filepath.Join(root, "foo-model-gateway-client"), "c.go", src)
	// GW legítimo: segmento exacto "model-gateway" — isento.
	mustWrite(t, filepath.Join(root, "model-gateway"), "gw.go", src)

	vs, err := archlint.AnalyzeTree(root, "model-gateway", archlint.Options{})
	if err != nil {
		t.Fatalf("AnalyzeTree: %v", err)
	}
	var flaggedCamuflado, flaggedGW bool
	for _, v := range vs {
		sl := filepath.ToSlash(v.File)
		if strings.Contains(sl, "foo-model-gateway-client") {
			flaggedCamuflado = true
		}
		if strings.Contains(sl, "/model-gateway/") {
			flaggedGW = true
		}
	}
	if !flaggedCamuflado {
		t.Errorf("consumidor camuflado ('foo-model-gateway-client') devia ser sinalizado, nao isentado; violacoes=%v", vs)
	}
	if flaggedGW {
		t.Errorf("o GW legitimo (segmento 'model-gateway') devia ser isento; violacoes=%v", vs)
	}
}

func mustWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", name, err)
	}
}

// TestAnalyze_GatewayIsento: com IsGateway a análise devolve vazio (o adaptador
// do próprio GW fala com o provedor por desenho — é o gate legítimo).
func TestAnalyze_GatewayIsento(t *testing.T) {
	t.Parallel()
	vs, err := archlint.AnalyzeDir("testdata/bad", archlint.Options{IsGateway: true})
	if err != nil {
		t.Fatalf("AnalyzeDir: %v", err)
	}
	if len(vs) != 0 {
		t.Errorf("GW isento devia ter 0 violacoes, obtidas %v", vs)
	}
}

// TestAnalyzeTree_ConsumidoresLimpos varre RECURSIVAMENTE a árvore de packages do
// AOS e prova que NENHUM consumidor fora do model-gateway invoca um provider
// directamente (isentando o próprio GW pelo marcador de caminho). É o teste de
// arquitectura central do critério AOS-055.
func TestAnalyzeTree_ConsumidoresLimpos(t *testing.T) {
	t.Parallel()
	// Raiz de packages/ do monorepo (três níveis acima de archlint/).
	vs, err := archlint.AnalyzeTree("../../..", "model-gateway", archlint.Options{})
	if err != nil {
		t.Fatalf("AnalyzeTree: %v", err)
	}
	if len(vs) != 0 {
		var b strings.Builder
		for _, v := range vs {
			b.WriteString(v.String())
			b.WriteString("\n")
		}
		t.Fatalf("ha %d invocacao(oes) directa(s) de provider fora do GW:\n%s", len(vs), b.String())
	}
}
