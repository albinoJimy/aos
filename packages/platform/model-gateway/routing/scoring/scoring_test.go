package scoring_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/platform/model-gateway/policy/weights"
	"github.com/aos-ref/platform/model-gateway/routing/scoring"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// fakeTable é uma WeightTable de teste (sem passar pelo artefacto assinado) para
// exercitar a aritmética do scorer com pesos escolhidos à mão.
type fakeTable struct {
	profiles map[string]weights.Weights
	def      string
	version  string
}

func (f fakeTable) Weights(p string) (weights.Weights, bool) { w, ok := f.profiles[p]; return w, ok }
func (f fakeTable) Default() string                          { return f.def }
func (f fakeTable) Version() string                          { return f.version }

func table() fakeTable {
	return fakeTable{
		def:     "balanced",
		version: "t/v1#abcdef123456",
		profiles: map[string]weights.Weights{
			// balanced: só custo e task-fit contam, em partes iguais.
			"balanced": {Cost: 500, TaskFit: 500},
			// cheap: só custo.
			"cheap": {Cost: 1000},
			// quality: só task-fit.
			"quality": {TaskFit: 1000},
		},
	}
}

func ladder() *tiering.Ladder {
	return tiering.NewLadder(
		tiering.Tier{Name: "economy", Model: "econo", CostRank: 1, Capability: tiering.CapabilityBasic},
		tiering.Tier{Name: "standard", Model: "std", CostRank: 3, Capability: tiering.CapabilityStandard},
		tiering.Tier{Name: "frontier", Model: "front", CostRank: 5, Capability: tiering.CapabilityFrontier, Fast: true},
	)
}

func cand(l *tiering.Ladder, name, region string) scoring.Candidate {
	t, ok := l.TierOf(name)
	if !ok {
		panic("tier de teste inexistente: " + name)
	}
	return scoring.Candidate{Region: region, Tier: t}
}

// TestNewScorer_FailClosed prova a regra 3 do ADR-021 na construção: sem tabela não
// há scorer; um perfil desconhecido NÃO cai silenciosamente no default.
func TestNewScorer_FailClosed(t *testing.T) {
	if _, err := scoring.NewScorer(nil, "balanced"); !errors.Is(err, scoring.ErrNoWeightTable) {
		t.Fatalf("sem tabela devia falhar ErrNoWeightTable, obtive %v", err)
	}
	if _, err := scoring.NewScorer(table(), "perfil-que-nao-existe"); !errors.Is(err, scoring.ErrProfileUnknown) {
		t.Fatalf("perfil desconhecido devia falhar ErrProfileUnknown, obtive %v", err)
	}
	// TableFrom(nil) é nil (fail-closed) — o gémeo de AllowlistFrom(nil) default-deny.
	if scoring.TableFrom(nil) != nil {
		t.Fatal("TableFrom(nil) tem de ser nil para que NewScorer recuse")
	}
	// Perfil vazio ⇒ default DECLARADO NA TABELA (nome revisto e assinado).
	s, err := scoring.NewScorer(table(), "")
	if err != nil {
		t.Fatalf("perfil vazio devia usar o default da tabela: %v", err)
	}
	if s.Profile() != "balanced" {
		t.Errorf("perfil resolvido = %q, esperava balanced", s.Profile())
	}
	if !s.Armed() {
		t.Error("um scorer construído sobre tabela válida tem de estar ARMADO")
	}
	// Um Scorer de valor-zero (bypass do construtor) NÃO está armado — é o que faz o
	// router recusar em vez de rotear com pesos implícitos.
	if (&scoring.Scorer{}).Armed() {
		t.Error("um Scorer de valor-zero NUNCA pode estar armado (fail-closed)")
	}
	var nilScorer *scoring.Scorer
	if nilScorer.Armed() {
		t.Error("um Scorer nil NUNCA pode estar armado")
	}
}

// TestScore_AritmeticaInteira verifica a soma ponderada exacta (valores esperados
// calculados à mão) — a prova de que a normalização é divisão INTEIRA e não uma
// média com vírgula.
func TestScore_AritmeticaInteira(t *testing.T) {
	l := ladder()
	fit := scoring.NewStaticTaskFit(0).Set("econo", tiering.CapabilityBasic, 200)
	s, err := scoring.NewScorer(table(), "balanced",
		scoring.WithCost(scoring.CostFromLadder(l)),
		scoring.WithTaskFit(fit),
	)
	if err != nil {
		t.Fatal(err)
	}
	// economy: CostRank 1 = o mínimo da escada ⇒ custo 1000; task-fit 200.
	// score = (500*1000 + 500*200) / 1000 = 600.
	got := s.Score(context.Background(), scoring.Task{Capability: tiering.CapabilityBasic}, cand(l, "economy", "eu-west"))
	if got.Score != 600 {
		t.Errorf("score esperado 600 (aritmética inteira), obtive %d (factores %s)", got.Score, got.Factors)
	}
	if got.Weighted != 600_000 {
		t.Errorf("soma ponderada bruta esperada 600000, obtive %d", got.Weighted)
	}
	if got.Factors.Cost != 1000 || got.Factors.TaskFit != 200 {
		t.Errorf("factores inesperados: %s", got.Factors)
	}
	// Factores NÃO injectados valem 0 (lado seguro: nunca inventam sinal).
	if got.Factors.Health != 0 || got.Factors.Headroom != 0 || got.Factors.Stability != 0 || got.Factors.Latency != 0 {
		t.Errorf("factores sem porta injectada têm de valer 0: %s", got.Factors)
	}
	if got.Profile != "balanced" || got.WeightsVersion != "t/v1#abcdef123456" {
		t.Errorf("perfil/versão não propagados: %+v", got)
	}
}

// TestScore_PortaComErroResolvePeloLadoSeguro — ADR-021 §5: um sinal ausente ou em
// erro NÃO exclui o candidato (excluir é das guardas) e NÃO o beneficia: vale 0.
func TestScore_PortaComErroResolvePeloLadoSeguro(t *testing.T) {
	l := ladder()
	boom := scoring.FactorFunc(func(context.Context, scoring.Task, scoring.Candidate) (int, error) {
		return 999, errors.New("sinal indisponível")
	})
	// Uma porta mal comportada que devolve valores fora da escala é FIXADA a [0,1000].
	tooBig := scoring.FactorFunc(func(context.Context, scoring.Task, scoring.Candidate) (int, error) {
		return 999999, nil
	})
	negative := scoring.FactorFunc(func(context.Context, scoring.Task, scoring.Candidate) (int, error) {
		return -50, nil
	})
	s, err := scoring.NewScorer(table(), "quality",
		scoring.WithTaskFit(boom), scoring.WithCost(tooBig), scoring.WithHealth(negative))
	if err != nil {
		t.Fatal(err)
	}
	got := s.Score(context.Background(), scoring.Task{}, cand(l, "economy", "eu-west"))
	if got.Factors.TaskFit != 0 {
		t.Errorf("porta em erro tem de valer 0 (lado seguro), obtive %d", got.Factors.TaskFit)
	}
	if got.Factors.Cost != scoring.Scale {
		t.Errorf("valor acima da escala tem de ser fixado a %d, obtive %d", scoring.Scale, got.Factors.Cost)
	}
	if got.Factors.Health != 0 {
		t.Errorf("valor negativo tem de ser fixado a 0, obtive %d", got.Factors.Health)
	}
	if got.Score != 0 { // perfil quality só pesa task-fit, que ficou a 0
		t.Errorf("score esperado 0, obtive %d", got.Score)
	}
}

// TestBest_DeterministaEIndependenteDaOrdem prova que o vencedor NÃO depende da
// ordem de entrada e que os desempates são estáveis (condição do replay, ADR-010).
func TestBest_DeterministaEIndependenteDaOrdem(t *testing.T) {
	l := ladder()
	s, err := scoring.NewScorer(table(), "cheap", scoring.WithCost(scoring.CostFromLadder(l)))
	if err != nil {
		t.Fatal(err)
	}
	asc := []scoring.Candidate{cand(l, "economy", "eu-west"), cand(l, "standard", "eu-west"), cand(l, "frontier", "eu-west")}
	desc := []scoring.Candidate{cand(l, "frontier", "eu-west"), cand(l, "standard", "eu-west"), cand(l, "economy", "eu-west")}
	a, ra, ok := s.Best(context.Background(), scoring.Task{}, asc)
	if !ok {
		t.Fatal("Best devia eleger um candidato")
	}
	b, rb, _ := s.Best(context.Background(), scoring.Task{}, desc)
	if a.Tier.Name != b.Tier.Name || ra.Score != rb.Score {
		t.Fatalf("a eleição depende da ordem de entrada: %s(%d) vs %s(%d)", a.Tier.Name, ra.Score, b.Tier.Name, rb.Score)
	}
	if a.Tier.Name != "economy" {
		t.Errorf("com perfil cheap o vencedor tem de ser o mais barato, obtive %s", a.Tier.Name)
	}
	// Empate total (mesmo score) ⇒ desempate por CostRank, depois modelo, região, tier.
	flat := scoring.FactorFunc(func(context.Context, scoring.Task, scoring.Candidate) (int, error) { return 500, nil })
	s2, err := scoring.NewScorer(table(), "cheap", scoring.WithCost(flat))
	if err != nil {
		t.Fatal(err)
	}
	w, _, _ := s2.Best(context.Background(), scoring.Task{}, desc)
	if w.Tier.Name != "economy" {
		t.Errorf("empate tem de desempatar pelo mais barato, obtive %s", w.Tier.Name)
	}
	// Lista vazia ⇒ ok=false (o router traduz numa rejeição fail-closed).
	if _, _, ok := s.Best(context.Background(), scoring.Task{}, nil); ok {
		t.Error("Best de lista vazia tem de devolver ok=false")
	}
}

// TestBest_MesmoScoreRegioesDesempatePorRegiao cobre o desempate por REGIÃO com
// tudo o resto igual (determinismo entre regiões intra-fronteira).
func TestBest_MesmoScoreRegioesDesempatePorRegiao(t *testing.T) {
	l := ladder()
	flat := scoring.FactorFunc(func(context.Context, scoring.Task, scoring.Candidate) (int, error) { return 700, nil })
	s, err := scoring.NewScorer(table(), "cheap", scoring.WithCost(flat))
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Best(context.Background(), scoring.Task{}, []scoring.Candidate{
		cand(l, "economy", "eu-west"), cand(l, "economy", "eu-central"),
	})
	if got.Region != "eu-central" {
		t.Errorf("desempate por região tem de ser ascendente (eu-central), obtive %s", got.Region)
	}
}

// TestReferenceFactors cobre as impls de referência determinísticas: custo derivado
// da escada REAL, headroom derivado de uma porta de carga, latência p95/Fast,
// health/estabilidade/task-fit por mapa fixo.
func TestReferenceFactors(t *testing.T) {
	ctx := context.Background()
	l := ladder()
	cost := scoring.CostFromLadder(l)
	// escada 1..5: economy(1)=1000, standard(3)=500, frontier(5)=0.
	for _, c := range []struct {
		tier string
		want int
	}{{"economy", 1000}, {"standard", 500}, {"frontier", 0}} {
		got, _ := cost.Factor(ctx, scoring.Task{}, cand(l, c.tier, "eu"))
		if got != c.want {
			t.Errorf("custo(%s) = %d, esperava %d", c.tier, got, c.want)
		}
	}
	// Escada degenerada (um único CostRank) não discrimina: todos a Scale.
	flatLadder := tiering.NewLadder(tiering.Tier{Name: "a", Model: "a", CostRank: 2}, tiering.Tier{Name: "b", Model: "b", CostRank: 2})
	fc := scoring.CostFromLadder(flatLadder)
	if v, _ := fc.Factor(ctx, scoring.Task{}, scoring.Candidate{Tier: tiering.Tier{CostRank: 2}}); v != scoring.Scale {
		t.Errorf("escada degenerada devia dar %d, obtive %d", scoring.Scale, v)
	}
	if v, _ := scoring.CostFromLadder(nil).Factor(ctx, scoring.Task{}, scoring.Candidate{}); v != scoring.Scale {
		t.Errorf("escada nil devia dar %d, obtive %d", scoring.Scale, v)
	}

	// HEADROOM derivado de uma porta de carga (o mesmo sinal do router, não um novo).
	hr := scoring.HeadroomFromReader(readerFunc(func(_ context.Context, _, region string) (int64, int64, bool, error) {
		switch region {
		case "cheia":
			return 100, 100, false, nil
		case "saturada":
			return 0, 100, true, nil
		case "erro":
			return 0, 0, false, errors.New("sem sinal")
		case "sem-limite":
			return 5, 0, false, nil
		default:
			return 25, 100, false, nil
		}
	}))
	for _, c := range []struct {
		region string
		want   int
	}{{"eu-west", 750}, {"cheia", 0}, {"saturada", 0}, {"erro", 0}, {"sem-limite", 0}} {
		got, _ := hr.Factor(ctx, scoring.Task{Provider: "openai"}, scoring.Candidate{Region: c.region})
		if got != c.want {
			t.Errorf("headroom(%s) = %d, esperava %d", c.region, got, c.want)
		}
	}
	if v, _ := scoring.HeadroomFromReader(nil).Factor(ctx, scoring.Task{}, scoring.Candidate{}); v != 0 {
		t.Errorf("headroom sem reader devia valer 0 (lado seguro), obtive %d", v)
	}

	// LATÊNCIA: p95 medido offline; sem p95 recorre ao bit Fast da escada.
	lat := scoring.NewStaticLatency(true).SetP95("std", 400).SetP95("front", 100)
	if v, _ := lat.Factor(ctx, scoring.Task{}, cand(l, "standard", "eu")); v != 250 {
		t.Errorf("latencia(std, p95=400 vs min=100) = %d, esperava 250", v)
	}
	if v, _ := lat.Factor(ctx, scoring.Task{}, cand(l, "frontier", "eu")); v != 1000 {
		t.Errorf("latencia(front, p95 mínimo) = %d, esperava 1000", v)
	}
	if v, _ := lat.Factor(ctx, scoring.Task{}, cand(l, "economy", "eu")); v != 500 {
		t.Errorf("latencia(sem p95, tier lento) = %d, esperava 500 (recurso ao bit Fast)", v)
	}
	noFallback := scoring.NewStaticLatency(false)
	if v, _ := noFallback.Factor(ctx, scoring.Task{}, cand(l, "frontier", "eu")); v != 0 {
		t.Errorf("sem p95 e sem recurso estrutural o lado seguro é 0, obtive %d", v)
	}

	// HEALTH / TASK-FIT / ESTABILIDADE por mapa fixo, com omissão explícita.
	h := scoring.NewStaticHealth(300).Set("openai", "eu-west", 900)
	if v, _ := h.Factor(ctx, scoring.Task{Provider: "openai"}, scoring.Candidate{Region: "eu-west"}); v != 900 {
		t.Errorf("health configurada = %d", v)
	}
	if v, _ := h.Factor(ctx, scoring.Task{Provider: "openai"}, scoring.Candidate{Region: "eu-central"}); v != 300 {
		t.Errorf("health por omissão = %d", v)
	}
	fit := scoring.NewStaticTaskFit(0).Set("front", tiering.CapabilityFrontier, 950)
	if v, _ := fit.Factor(ctx, scoring.Task{Capability: tiering.CapabilityFrontier}, cand(l, "frontier", "eu")); v != 950 {
		t.Errorf("task-fit configurado = %d", v)
	}
	// A MESMA modelo noutra capacidade não herda o crédito (a chave é o par).
	if v, _ := fit.Factor(ctx, scoring.Task{Capability: tiering.CapabilityBasic}, cand(l, "frontier", "eu")); v != 0 {
		t.Errorf("task-fit não pode transbordar entre capacidades, obtive %d", v)
	}
	st := scoring.NewStaticStability(500).Set("econo", 1000)
	if v, _ := st.Factor(ctx, scoring.Task{}, cand(l, "economy", "eu")); v != 1000 {
		t.Errorf("estabilidade configurada = %d", v)
	}
	if v, _ := st.Factor(ctx, scoring.Task{}, cand(l, "standard", "eu")); v != 500 {
		t.Errorf("estabilidade por omissão = %d", v)
	}
}

// TestReason_IncluiPerfilEScore prova a regra 5 do ADR-021 na origem: a razão
// registada traz perfil, versão dos pesos, score e factores — é o texto que chega à
// variância model_swap pela pipeline.
func TestReason_IncluiPerfilEScore(t *testing.T) {
	r := scoring.Result{Profile: "quality", WeightsVersion: "t/v1#deadbeef1234", Score: 812,
		Factors: scoring.Factors{Health: 1000, Headroom: 900, Cost: 600, Latency: 1000, TaskFit: 700, Stability: 800}}
	got := scoring.Reason(r)
	for _, want := range []string{"perfil=quality", "pesos=t/v1#deadbeef1234", "score=812/1000", "health=1000", "task_fit=700", "estabilidade=800"} {
		if !contains(got, want) {
			t.Errorf("a razão de scoring tem de conter %q; obtive %q", want, got)
		}
	}
}

// TestTableFrom_AdaptaArtefactoAssinado prova que a tabela ASSINADA real (o
// artefacto embebido de policy/weights) satisfaz a porta do scorer — o caminho de
// produção, não um duplo.
func TestTableFrom_AdaptaArtefactoAssinado(t *testing.T) {
	tab, err := weights.LoadTable()
	if err != nil {
		t.Fatalf("LoadTable: %v", err)
	}
	wt := scoring.TableFrom(tab)
	if wt.Default() != tab.Default() || wt.Version() != tab.Version() {
		t.Error("o adaptador tem de expor default/versão da tabela assinada")
	}
	if _, ok := wt.Weights("balanced"); !ok {
		t.Error("o perfil balanced do artefacto assinado tem de resolver")
	}
	s, err := scoring.NewScorer(wt, "cheap")
	if err != nil || !s.Armed() {
		t.Fatalf("scorer sobre o artefacto assinado devia armar: err=%v", err)
	}
	if s.WeightsVersion() != tab.Version() {
		t.Errorf("versão dos pesos registada = %q, esperava %q", s.WeightsVersion(), tab.Version())
	}
	var nilScorer *scoring.Scorer
	if nilScorer.Profile() != "" || nilScorer.WeightsVersion() != "" {
		t.Error("um scorer nil não pode reportar perfil/versão")
	}
}

// readerFunc adapta uma função à porta scoring.LoadReader.
type readerFunc func(ctx context.Context, provider, region string) (int64, int64, bool, error)

func (f readerFunc) Load(ctx context.Context, provider, region string) (int64, int64, bool, error) {
	return f(ctx, provider, region)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
