// Package scoring é o SCORING PONDERADO DETERMINÍSTICO do Model Gateway (AOS-269,
// ADR-021, tecnica/06 §6.1): substitui a ordenação LEXICOGRÁFICA do router
// (AOS-059) por uma soma ponderada de factores sobre os candidatos que JÁ
// SOBREVIVERAM às guardas estruturais.
//
// # O que este pacote NÃO faz (a regra 1 do ADR-021, inegociável)
//
// Este pacote NÃO conhece soberania, allowlist nem piso de capacidade — e é
// deliberado. A partição de soberania (cross-border descartado), a allowlist
// assinada do board (default-deny) e o piso de capacidade do tier correm no ROUTER,
// ANTES de aqui chegar qualquer candidato. O scorer recebe apenas SOBREVIVENTES:
// intra-fronteira, permitidos e capazes. Nenhum peso — por mais alto que seja —
// pode ressuscitar um candidato descartado, porque um candidato descartado NUNCA
// entra na lista que o scorer vê. É uma garantia ESTRUTURAL (ausência), não uma
// verificação que se possa esquecer de fazer.
//
// # Factores como PORTAS injectáveis (regra 2)
//
// Cada factor — health, headroom, custo, latência, task-fit, estabilidade — entra
// por uma porta [FactorProvider] com implementação de referência determinística, à
// imagem de router.LoadProvider / degradation.BudgetProvider. O HEADROOM em
// particular NÃO é um sinal novo: [HeadroomFromReader] deriva-o da porta de carga
// que o router já tem (LoadProvider, AOS-057/059) — o subsistema existente é ligado,
// não reimplementado.
//
// # ARITMÉTICA INTEIRA — zero floats (regra 2)
//
// Todos os valores são inteiros em MILÉSIMOS ([Scale] = 1000): cada factor devolve
// 0..1000, cada peso é 0..1000, a soma ponderada é int64 e a normalização é uma
// divisão INTEIRA (truncada, portanto determinista e replayável byte-a-byte). Não
// há float32/float64 em nenhum ponto do caminho de decisão — o zero-dep e o
// determinismo (ADR-010) dependem disso. Um teste do gate (routingtests) prova por
// AST que nenhum ficheiro deste pacote, de policy/weights ou do router declara ou
// usa vírgula flutuante.
//
// # Calibração OFFLINE, nunca online (regra 4)
//
// Não há bandit, não há exploração, não há aprendizagem em runtime: nem `rand`, nem
// relógio, nem estado mutável partilhado no caminho de decisão. O sinal de task-fit
// e a evolução dos pesos vêm de análise OFFLINE (eval harness EPIC-08 + DecisionSink)
// promovida por uma NOVA VERSÃO da tabela assinada (policy/weights, ADR-012). A
// decisão é função pura de (task, candidatos, sinais das portas, pesos).
package scoring

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/aos-ref/platform/model-gateway/policy/weights"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// Scale é a escala de PONTO FIXO do scoring: um factor normalizado vale 0..Scale
// (milésimos) e um peso vale 0..weights.MaxWeight (também milésimos). Toda a
// aritmética é inteira; Scale existe precisamente para não haver fracções.
const Scale = 1000

// Erros fail-closed da construção do scorer. Um scorer que não se construa é um
// router que RECUSA rotear (ADR-021 regra 3: sem tabela válida/assinada, não há
// pesos implícitos).
var (
	// ErrNoWeightTable — nenhuma tabela de pesos fornecida. Fail-closed: não se
	// assume um perfil "razoável" por omissão.
	ErrNoWeightTable = errors.New("scoring: sem tabela de pesos valida/assinada (fail-closed, ADR-021)")
	// ErrProfileUnknown — o perfil pedido não existe na tabela carregada. Fail-closed:
	// um perfil desconhecido NÃO cai silenciosamente no default (isso seria rotear com
	// pesos que ninguém pediu nem reviu).
	ErrProfileUnknown = errors.New("scoring: perfil de pesos desconhecido na tabela (fail-closed)")
)

// Task é a intenção da chamada tal como o scorer a vê: a dimensão de soberania/
// orçamento (board/tenant, para as portas que segmentam sinais por board), a
// capacidade EXIGIDA, a classe latência-vs-batch e o PERFIL de pesos pedido. É o
// input do lado da TAREFA da função pura de scoring.
type Task struct {
	Board      string
	Tenant     string
	Provider   string
	Capability tiering.Capability
	Class      tiering.Class
	// Profile é o PERFIL DE PESOS pedido POR ESTA DECISÃO (ex.: "cheap" para um
	// batch que quer o mais barato possível, "quality" para um interactivo que quer
	// qualidade acima de tudo). É a forma declarada de exprimir intenção que o
	// ADR-021 §1 gap 2 identifica como ausente — sem ela, trocar de perfil exigiria
	// re-compor o router em código, que é exactamente o «cada novo perfil exigiria
	// código» dado como razão para rejeitar a alternativa (a) em §3.
	//
	// VAZIO ⇒ o perfil COMPOSTO no [NewScorer] (um nome explícito, revisto e
	// assinado — não um implícito de código). Um perfil DESCONHECIDO não cai no
	// composto nem no default: [Scorer.HasProfile] devolve false e o router RECUSA
	// (fail-closed, à imagem de [ErrProfileUnknown] na construção).
	Profile string
}

// Candidate é UM sobrevivente das guardas: um tier da escada (routing/tiering — o
// tipo existente, não uma cópia) numa região intra-fronteira concreta. O router só
// constrói Candidates para pares (região, tier) que passaram soberania + allowlist +
// piso de capacidade.
type Candidate struct {
	// Region é a região intra-fronteira do candidato (já validada pela guarda).
	Region string
	// Tier é o degrau da escada (modelo, custo, capacidade, bit Fast).
	Tier tiering.Tier
}

// Factors é o vector de factores JÁ NORMALIZADOS (0..Scale) de um candidato — o que
// fica registado no span/DecisionSink por decisão (ADR-021 regra 5 + ADR-010: a
// decisão é auditável nos seus componentes, não só no total).
type Factors struct {
	Health    int
	Headroom  int
	Cost      int
	Latency   int
	TaskFit   int
	Stability int
}

// String serializa os factores de forma ESTÁVEL (ordem fixa, inteiros) para o
// atributo de span e para a razão registada. Sem mapas — sem ordem não-determinista.
func (f Factors) String() string {
	return "health=" + strconv.Itoa(f.Health) +
		",headroom=" + strconv.Itoa(f.Headroom) +
		",custo=" + strconv.Itoa(f.Cost) +
		",latencia=" + strconv.Itoa(f.Latency) +
		",task_fit=" + strconv.Itoa(f.TaskFit) +
		",estabilidade=" + strconv.Itoa(f.Stability)
}

// Result é o veredicto do scoring para um candidato: o total normalizado, os
// factores que o compuseram, o perfil aplicado e a versão EXACTA da tabela de pesos
// (tamper-evident). É o que o router regista.
type Result struct {
	// Profile é o perfil de pesos aplicado (ex.: "balanced").
	Profile string
	// WeightsVersion é a identidade versionada da tabela ("versão#digest12").
	WeightsVersion string
	// Score é a soma ponderada NORMALIZADA (0..Scale), por divisão inteira.
	Score int
	// Weighted é a soma ponderada BRUTA (Σ peso×factor) antes da normalização —
	// preservada para diagnóstico/calibração offline sem perda por truncatura.
	Weighted int64
	// Factors são os factores normalizados que compuseram o score.
	Factors Factors
}

// FactorProvider é a PORTA COMUM de um factor: dado (tarefa, candidato) devolve um
// valor normalizado 0..[Scale]. Determinística — sem relógio, sem rand, sem estado
// evolutivo. Um erro NÃO exclui o candidato (excluir é papel das GUARDAS, não do
// score): resolve-se pelo lado seguro, valor 0 — o sinal ausente nunca INVENTA
// qualidade nem folga.
type FactorProvider interface {
	Factor(ctx context.Context, t Task, c Candidate) (int, error)
}

// FactorFunc adapta uma função a [FactorProvider] (à imagem de http.HandlerFunc).
type FactorFunc func(ctx context.Context, t Task, c Candidate) (int, error)

// Factor implementa [FactorProvider].
func (f FactorFunc) Factor(ctx context.Context, t Task, c Candidate) (int, error) {
	return f(ctx, t, c)
}

// WeightTable é a PORTA da tabela de pesos vista pelo scorer. *weights.Table
// satisfá-la via [TableFrom] — o scorer não conhece embed, assinatura nem trust
// anchor (isso é policy/weights), só pesos versionados.
type WeightTable interface {
	// Weights devolve os pesos do perfil nomeado; ok=false ⇒ perfil desconhecido.
	Weights(profile string) (weights.Weights, bool)
	// Default é o nome do perfil por omissão da tabela.
	Default() string
	// Version é a identidade versionada+tamper-evident da tabela.
	Version() string
}

// TableFrom adapta a *weights.Table (artefacto assinado) à porta [WeightTable] — o
// gémeo de routingstage.AllowlistFrom para a allowlist. Uma tabela nil devolve nil:
// [NewScorer] recusa com [ErrNoWeightTable] (fail-closed, nunca pesos implícitos).
func TableFrom(t *weights.Table) WeightTable {
	if t == nil {
		return nil
	}
	return tableAdapter{t: t}
}

type tableAdapter struct{ t *weights.Table }

func (a tableAdapter) Weights(profile string) (weights.Weights, bool) { return a.t.Lookup(profile) }
func (a tableAdapter) Default() string                                { return a.t.Default() }
func (a tableAdapter) Version() string                                { return a.t.Version() }

// Scorer ordena sobreviventes por soma ponderada determinística. Construir com
// [NewScorer]. Imutável e stateless (o estado dos sinais vive nas portas): seguro
// para uso concorrente e para replay.
type Scorer struct {
	table   WeightTable
	profile string
	w       weights.Weights

	health    FactorProvider
	headroom  FactorProvider
	cost      FactorProvider
	latency   FactorProvider
	taskFit   FactorProvider
	stability FactorProvider
}

// Option configura o [Scorer] — o mesmo padrão de router.Option.
type Option func(*Scorer)

// WithHealth injecta a porta do factor SAÚDE (0 = doente, Scale = são).
func WithHealth(p FactorProvider) Option {
	return func(s *Scorer) {
		if p != nil {
			s.health = p
		}
	}
}

// WithHeadroom injecta a porta do factor HEADROOM (folga de TPM/RPM). A impl de
// referência [HeadroomFromReader] deriva-a da porta de carga JÁ EXISTENTE do router.
func WithHeadroom(p FactorProvider) Option {
	return func(s *Scorer) {
		if p != nil {
			s.headroom = p
		}
	}
}

// WithCost injecta a porta do factor CUSTO (mais barato = mais alto). A impl de
// referência é [CostFromLadder], derivada da escada de custo já existente.
func WithCost(p FactorProvider) Option {
	return func(s *Scorer) {
		if p != nil {
			s.cost = p
		}
	}
}

// WithLatency injecta a porta do factor LATÊNCIA (mais rápido = mais alto).
func WithLatency(p FactorProvider) Option {
	return func(s *Scorer) {
		if p != nil {
			s.latency = p
		}
	}
}

// WithTaskFit injecta a porta do factor TASK-FIT (qualidade por tipo de tarefa,
// calibrada OFFLINE pelo eval harness — nunca aprendida em runtime).
func WithTaskFit(p FactorProvider) Option {
	return func(s *Scorer) {
		if p != nil {
			s.taskFit = p
		}
	}
}

// WithStability injecta a porta do factor ESTABILIDADE (taxa de sucesso histórica,
// também offline).
func WithStability(p FactorProvider) Option {
	return func(s *Scorer) {
		if p != nil {
			s.stability = p
		}
	}
}

// NewScorer constrói o scorer sobre uma tabela de pesos VÁLIDA e um perfil
// EXISTENTE. Fail-closed (ADR-021 regra 3):
//
//   - tabela nil ⇒ [ErrNoWeightTable] (não há pesos por omissão);
//   - profile "" ⇒ usa o default DECLARADO NA TABELA (que é um nome revisto e
//     assinado, não um implícito de código);
//   - perfil inexistente ⇒ [ErrProfileUnknown].
//
// Um factor não injectado vale 0 (lado seguro) — o scorer nunca inventa sinal.
func NewScorer(t WeightTable, profile string, opts ...Option) (*Scorer, error) {
	if t == nil {
		return nil, ErrNoWeightTable
	}
	if profile == "" {
		profile = t.Default()
	}
	w, ok := t.Weights(profile)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrProfileUnknown, profile)
	}
	s := &Scorer{table: t, profile: profile, w: w}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Armed reporta se o scorer está EFECTIVAMENTE armado: construído sobre uma tabela
// de pesos verificada e com um perfil de soma > 0. Um Scorer de valor-zero (ou com
// tabela perdida) devolve false — e o router traduz isso numa RECUSA fail-closed em
// vez de rotear com pesos implícitos. É a defesa em profundidade da regra 3: nem
// por construção directa (bypass de [NewScorer]) se consegue um scoring sem pesos.
func (s *Scorer) Armed() bool {
	return s != nil && s.table != nil && s.profile != "" && s.w.Sum() > 0
}

// Profile devolve o nome do perfil de pesos COMPOSTO (o que vale quando a decisão
// não pede um perfil próprio em [Task.Profile]).
func (s *Scorer) Profile() string {
	if s == nil {
		return ""
	}
	return s.profile
}

// resolve devolve os pesos a aplicar A ESTA DECISÃO. Um perfil vazio é o COMPOSTO;
// qualquer outro é procurado na MESMA tabela assinada — nunca inventado, nunca
// substituído em silêncio pelo composto. ok=false ⇒ perfil inexistente ou de soma
// zero (que não ordenaria nada): o chamador RECUSA, não escolhe por ele.
//
// PORQUE POR DECISÃO E NÃO POR CONSTRUÇÃO. Os pesos vêm todos do MESMO artefacto
// assinado e verificado uma única vez ([NewScorer]); o que varia por pedido é qual
// dos perfis REVISTOS se aplica. A superfície de confiança não cresce — o que
// deixa de existir é a necessidade de re-compor o router para exprimir «este
// pedido quer o mais barato».
func (s *Scorer) resolve(profile string) (weights.Weights, string, bool) {
	if s == nil || s.table == nil {
		return weights.Weights{}, "", false
	}
	if profile == "" || profile == s.profile {
		return s.w, s.profile, s.w.Sum() > 0
	}
	w, ok := s.table.Weights(profile)
	if !ok || w.Sum() <= 0 {
		return weights.Weights{}, "", false
	}
	return w, profile, true
}

// HasProfile reporta se o perfil PEDIDO por uma decisão é resolúvel na tabela em
// vigor. O router chama-o ANTES de pontuar para poder distinguir, na razão
// registada, «perfil desconhecido» de «nenhum sobrevivente» — um deny atribuível
// (ADR-010/AOS-011) não pode culpar a allowlist por um erro de perfil.
func (s *Scorer) HasProfile(profile string) bool {
	if !s.Armed() {
		return false
	}
	_, _, ok := s.resolve(profile)
	return ok
}

// WeightsVersion devolve a identidade versionada da tabela em vigor
// ("versão#digest12") — registada em cada decisão.
func (s *Scorer) WeightsVersion() string {
	if s == nil || s.table == nil {
		return ""
	}
	return s.table.Version()
}

// Score calcula o [Result] de UM candidato. Função PURA dos inputs e dos sinais das
// portas: sem relógio, sem rand, sem estado mutável. Aritmética inteira:
//
//	Weighted = Σ peso_f × factor_f            (int64, factores e pesos em 0..1000)
//	Score    = Weighted / Σ peso_f            (divisão INTEIRA, truncada)
//
// Um factor sem porta injectada, ou cuja porta devolva erro, vale 0 (lado seguro).
// Valores fora de [0,Scale] são fixados ao intervalo (uma porta mal comportada não
// distorce a escala nem provoca overflow).
//
// Os PESOS são os do perfil pedido em [Task.Profile] (vazio ⇒ o composto). Um
// perfil não resolúvel devolve o [Result] ZERO — nunca pesos implícitos; o
// chamador tem de o ter filtrado com [Scorer.HasProfile] e RECUSAR.
func (s *Scorer) Score(ctx context.Context, t Task, c Candidate) Result {
	w, profile, ok := s.resolve(t.Profile)
	if !ok {
		return Result{}
	}
	f := Factors{
		Health:    read(ctx, s.health, t, c),
		Headroom:  read(ctx, s.headroom, t, c),
		Cost:      read(ctx, s.cost, t, c),
		Latency:   read(ctx, s.latency, t, c),
		TaskFit:   read(ctx, s.taskFit, t, c),
		Stability: read(ctx, s.stability, t, c),
	}
	var weighted int64
	weighted += int64(w.Health) * int64(f.Health)
	weighted += int64(w.Headroom) * int64(f.Headroom)
	weighted += int64(w.Cost) * int64(f.Cost)
	weighted += int64(w.Latency) * int64(f.Latency)
	weighted += int64(w.TaskFit) * int64(f.TaskFit)
	weighted += int64(w.Stability) * int64(f.Stability)

	sum := int64(w.Sum())
	score := 0
	if sum > 0 {
		score = int(weighted / sum) // divisão INTEIRA: determinista, sem fracções
	}
	return Result{
		Profile:        profile,
		WeightsVersion: s.WeightsVersion(),
		Score:          score,
		Weighted:       weighted,
		Factors:        f,
	}
}

// Best ordena os SOBREVIVENTES e devolve o melhor com o seu [Result]. A ordenação é
// TOTALMENTE determinística — o desempate nunca depende da ordem de entrada:
//
//  1. maior Score (soma ponderada normalizada);
//  2. em empate, MENOR CostRank (o mais barato — preserva o viés de custo do AOS-059);
//  3. em empate, nome do MODELO ascendente;
//  4. em empate, REGIÃO ascendente;
//  5. em empate, nome do TIER ascendente.
//
// ok=false para lista vazia OU para perfil pedido não resolúvel — o router traduz
// ambos numa REJEIÇÃO fail-closed com razão PRÓPRIA (nunca "escolhe na mesma", e
// nunca com a razão do outro caso). Não muta a slice do chamador (ordena uma cópia).
func (s *Scorer) Best(ctx context.Context, t Task, cands []Candidate) (Candidate, Result, bool) {
	if len(cands) == 0 {
		return Candidate{}, Result{}, false
	}
	if _, _, ok := s.resolve(t.Profile); !ok {
		return Candidate{}, Result{}, false
	}
	type scored struct {
		c Candidate
		r Result
	}
	all := make([]scored, 0, len(cands))
	for _, c := range cands {
		all = append(all, scored{c: c, r: s.Score(ctx, t, c)})
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.r.Score != b.r.Score {
			return a.r.Score > b.r.Score
		}
		if a.c.Tier.CostRank != b.c.Tier.CostRank {
			return a.c.Tier.CostRank < b.c.Tier.CostRank
		}
		if a.c.Tier.Model != b.c.Tier.Model {
			return a.c.Tier.Model < b.c.Tier.Model
		}
		if a.c.Region != b.c.Region {
			return a.c.Region < b.c.Region
		}
		return a.c.Tier.Name < b.c.Tier.Name
	})
	return all[0].c, all[0].r, true
}

// Reason devolve a RAZÃO legível de uma decisão por scoring — perfil, versão dos
// pesos, score e factores. É o texto que entra na Decision.Reason do router e que,
// por composição da pipeline, chega à variância model_swap (ADR-021 regra 5: o
// scoring nunca troca em silêncio; a razão da troca inclui score e perfil).
func Reason(r Result) string {
	return "scoring ponderado determinista (ADR-021): perfil=" + r.Profile +
		" pesos=" + r.WeightsVersion +
		" score=" + strconv.Itoa(r.Score) + "/" + strconv.Itoa(Scale) +
		" factores[" + r.Factors.String() + "]"
}

// read lê uma porta de factor com a política fail-safe do ADR-021 §5: porta ausente
// ou erro ⇒ 0 (o sinal em falta nunca beneficia o candidato); valor fixado a
// [0,Scale].
func read(ctx context.Context, p FactorProvider, t Task, c Candidate) int {
	if p == nil {
		return 0
	}
	v, err := p.Factor(ctx, t, c)
	if err != nil {
		return 0
	}
	return clamp(v)
}

// clamp fixa um valor ao intervalo [0,Scale] (inteiro).
func clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > Scale {
		return Scale
	}
	return v
}
