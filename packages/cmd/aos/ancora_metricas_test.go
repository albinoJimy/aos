package main

import (
	"bytes"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------------------------
// A COBERTURA DA ÂNCORA TEM DE SER ALERTÁVEL, NÃO SÓ LEGÍVEL NO BANNER.
//
// A âncora é produzida FORA do nó, por uma tarefa que corre sozinha. Se essa tarefa morrer, nada
// no nó dá por isso: a verificação continua a passar — o que ela verifica não mudou — e a
// cobertura congela enquanto o WORM continua a crescer.
//
// Uma âncora de há um ano verifica EXACTAMENTE como a de ontem: a assinatura é válida e o piso
// está satisfeito. O que envelhece não é a validade; é a cobertura. E o `Timestamp` do
// checkpoint, que É assinado, nunca era lido por ninguém.
// ---------------------------------------------------------------------------------------------

func metricasDe(t *testing.T, h *apiHandler) string {
	t.Helper()
	w := httptest.NewRecorder()
	h.handleMetrics(w, httptest.NewRequest("GET", "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/metrics devolveu %d", w.Code)
	}
	return w.Body.String()
}

func valorDe(t *testing.T, corpo, serie string) (float64, bool) {
	t.Helper()
	for _, l := range strings.Split(corpo, "\n") {
		if strings.HasPrefix(l, serie+" ") {
			v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(l, serie+" ")), 64)
			if err != nil {
				t.Fatalf("serie %s com valor ilegivel: %q", serie, l)
			}
			return v, true
		}
	}
	return 0, false
}

func TestCoberturaDaAncoraSaiEmMetrics(t *testing.T) {
	worm := audit.NewMemStore()
	ctx := t.Context()
	for _, p := range []string{"run-a", "run-b", "run-c"} {
		if _, err := worm.Append(ctx, audit.AuditRecord{
			Partition: p, RunID: p, StepID: "s", ToolID: "t", Capability: "cap:fs.read",
		}); err != nil {
			t.Fatal(err)
		}
	}
	pub, _, _ := ed25519.GenerateKey(nil)
	selado := time.Now().Add(-30 * time.Hour)
	anc := &WormAnchor{
		Public: pub,
		// A âncora cobre DUAS das três: a terceira nasceu depois da selagem, que é o caso normal.
		Checkpoints: []audit.Checkpoint{
			{Partition: "run-a", AuditSeq: 1, Timestamp: selado},
			{Partition: "run-b", AuditSeq: 1, Timestamp: selado},
		},
		ExpectedHeads: map[string]uint64{"run-a": 1, "run-b": 1},
	}
	h := &apiHandler{node: &Node{WORM: worm, ancora: anc}, svc: &NodeService{}}
	corpo := metricasDe(t, h)

	vivas, ok := valorDe(t, corpo, "aos_worm_partitions")
	if !ok || vivas != 3 {
		t.Errorf("aos_worm_partitions = %v (presente=%v), queria 3 — a contagem tem de ser a de AGORA", vivas, ok)
	}
	ancoradas, ok := valorDe(t, corpo, "aos_worm_partitions_anchored")
	if !ok || ancoradas != 2 {
		t.Errorf("aos_worm_partitions_anchored = %v (presente=%v), queria 2", ancoradas, ok)
	}
	idade, ok := valorDe(t, corpo, "aos_worm_anchor_age_seconds")
	if !ok {
		t.Fatal("aos_worm_anchor_age_seconds AUSENTE — e a unica serie que deteta a tarefa de selagem morta")
	}
	// ~30h. Uma janela larga: o que importa e a ordem de grandeza, nao o segundo.
	if idade < 29*3600 || idade > 31*3600 {
		t.Errorf("idade = %.0f s, esperava ~%d s (30h)", idade, 30*3600)
	}
}

// TestSemAncoraNAOSaemSeriesDeAncora é o CONTROLO, e é o que impede a métrica de mentir.
//
// Emitir `aos_worm_partitions_anchored 0` e `age 0` num nó SEM âncora faria um nó desprotegido
// parecer um nó acabado de selar — que é pior do que não emitir nada. É a mesma regra que já se
// aplica às séries de OTLP.
//
// A contagem de partições SAI na mesma: é um facto sobre o store, não sobre a âncora.
func TestSemAncoraNAOSaemSeriesDeAncora(t *testing.T) {
	worm := audit.NewMemStore()
	if _, err := worm.Append(t.Context(), audit.AuditRecord{
		Partition: "run-a", RunID: "run-a", StepID: "s", ToolID: "t", Capability: "cap:fs.read",
	}); err != nil {
		t.Fatal(err)
	}
	h := &apiHandler{node: &Node{WORM: worm}, svc: &NodeService{}}
	corpo := metricasDe(t, h)

	if _, ok := valorDe(t, corpo, "aos_worm_partitions_anchored"); ok {
		t.Error("sem ancora saiu aos_worm_partitions_anchored — um no desprotegido pareceria coberto")
	}
	if _, ok := valorDe(t, corpo, "aos_worm_anchor_age_seconds"); ok {
		t.Error("sem ancora saiu aos_worm_anchor_age_seconds — idade 0 le-se como «acabado de selar»")
	}
	if v, ok := valorDe(t, corpo, "aos_worm_partitions"); !ok || v != 1 {
		t.Errorf("aos_worm_partitions = %v (presente=%v) — e um facto do STORE e sai sempre", v, ok)
	}
}

// TestIdadeUsaOSeloMAISRECENTE fixa qual dos instantes se usa.
//
// Todos os checkpoints de uma selagem partilham o instante, mas um ficheiro pode acumular selos
// de datas diferentes se alguém o compuser à mão. O mais recente responde «quando foi a última
// vez que isto correu»; o mais antigo responde a outra pergunta, e alertaria para sempre.
func TestIdadeUsaOSeloMAISRECENTE(t *testing.T) {
	worm := audit.NewMemStore()
	pub, _, _ := ed25519.GenerateKey(nil)
	h := &apiHandler{node: &Node{WORM: worm, ancora: &WormAnchor{
		Public: pub,
		Checkpoints: []audit.Checkpoint{
			{Partition: "antiga", AuditSeq: 1, Timestamp: time.Now().Add(-400 * time.Hour)},
			{Partition: "recente", AuditSeq: 1, Timestamp: time.Now().Add(-2 * time.Hour)},
		},
		ExpectedHeads: map[string]uint64{"antiga": 1, "recente": 1},
	}}, svc: &NodeService{}}

	idade, ok := valorDe(t, metricasDe(t, h), "aos_worm_anchor_age_seconds")
	if !ok {
		t.Fatal("serie ausente")
	}
	if idade > 3*3600 {
		t.Errorf("idade = %.0f s — usou o selo MAIS ANTIGO, e essa leitura alertaria para sempre", idade)
	}
}

// TestOArranqueLIGAAAncoraAoMetrics é o teste de CABLAGEM, e apareceu por mutação.
//
// Os testes acima constroem o `Node` à mão. Uma mutação que removesse `ancora: cfg.WORMAnchor`
// do construtor passava em todos eles: a métrica continuava correcta e ninguém a alimentava.
// Em produção, `/metrics` ficaria eternamente sem as séries de âncora — exactamente o silêncio
// que este trabalho existe para acabar.
//
// É a sétima vez que este padrão aparece no repositório, e a razão pela qual passou a haver
// sempre uma mutação de cablagem.
func TestOArranqueLIGAAAncoraAoMetrics(t *testing.T) {
	dir := t.TempDir()
	wormPath := filepath.Join(dir, "worm.wal")

	// Um WORM com um registo, e uma âncora REAL assinada sobre ele — o `Bootstrap` verifica-a
	// (fail-closed), portanto uma âncora inventada nao passaria daqui.
	store, err := audit.OpenFileStore(wormPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if _, err := store.Append(ctx, audit.AuditRecord{
		Partition: "run-a", RunID: "run-a", StepID: "s", ToolID: "t", Capability: "cap:fs.read",
	}); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := signer.Seal(ctx, store, "run-a", 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	cfg := tnBaseConfig()
	cfg.WORMPath = wormPath
	cfg.WORMAnchor = &WormAnchor{
		Public:        pub,
		Checkpoints:   []audit.Checkpoint{cp},
		ExpectedHeads: map[string]uint64{"run-a": 1},
	}

	var banner bytes.Buffer
	node, err := Bootstrap(ctx, cfg, &banner)
	if err != nil {
		t.Fatalf("Bootstrap com ancora valida falhou: %v", err)
	}
	defer node.Close()

	h := &apiHandler{node: node, svc: &NodeService{}}
	corpo := metricasDe(t, h)
	if v, ok := valorDe(t, corpo, "aos_worm_partitions_anchored"); !ok || v != 1 {
		t.Errorf("o ARRANQUE nao ligou a ancora ao /metrics: aos_worm_partitions_anchored = %v "+
			"(presente=%v). A metrica esta certa e ninguem a alimenta", v, ok)
	}
	if _, ok := valorDe(t, corpo, "aos_worm_anchor_age_seconds"); !ok {
		t.Error("o arranque nao ligou a idade da ancora ao /metrics")
	}
	// CONTROLO do proprio cenario: a ancora foi mesmo VERIFICADA no arranque, senao este teste
	// estaria a medir um `Bootstrap` que a ignora.
	if !strings.Contains(banner.String(), "ancorad") {
		t.Errorf("o banner nao declara a verificacao ancorada — o cenario nao foi montado:\n%s", banner.String())
	}
}

// ---------------------------------------------------------------------------------------------
// A FOLGA DO ORÇAMENTO — o tecto que nunca morde tem de ser visível.
// ---------------------------------------------------------------------------------------------

// TestFolgaDoOrcamentoSaiEmMetrics — as duas séries que permitem calcular a folga.
func TestFolgaDoOrcamentoSaiEmMetrics(t *testing.T) {
	rb, err := integration.NewRunBudget(200000)
	if err != nil {
		t.Fatal(err)
	}
	h := &apiHandler{node: &Node{orcamento: rb}, svc: &NodeService{}}

	corpo := metricasDe(t, h)
	if v, ok := valorDe(t, corpo, "aos_budget_max_tokens_per_run"); !ok || v != 200000 {
		t.Errorf("aos_budget_max_tokens_per_run = %v (presente=%v), queria 200000", v, ok)
	}
	// CONTROLO: sem nenhum run fechado o PICO nao sai. Emitir 0 diria «nada gasta nada» e a
	// folga apareceria infinita num no que ainda nao mediu coisa nenhuma — a leitura mais errada
	// possivel, e a que faria um tecto por calibrar parecer calibrado.
	if _, ok := valorDe(t, corpo, "aos_budget_run_tokens_peak"); ok {
		t.Error("o pico saiu ANTES de qualquer run fechar")
	}
}

// TestSemOrcamentoNAOSaemSeriesDeOrcamento — a mesma regra das outras famílias.
func TestSemOrcamentoNAOSaemSeriesDeOrcamento(t *testing.T) {
	h := &apiHandler{node: &Node{}, svc: &NodeService{}}
	corpo := metricasDe(t, h)
	if _, ok := valorDe(t, corpo, "aos_budget_max_tokens_per_run"); ok {
		t.Error("sem orcamento composto sairam series de orcamento — um no sem tecto pareceria ter um")
	}
}
