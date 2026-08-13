package scoring

import (
	"context"
	"sync"

	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// Este ficheiro reúne as IMPLEMENTAÇÕES DE REFERÊNCIA determinísticas das seis
// portas de factor (ADR-021 regra 2). São o análogo exacto do router.StaticLoadProvider
// (AOS-059) e do degradation.StaticBudgetProvider: fecham o contrato da porta para
// teste e arranque, SEM I/O, SEM relógio e SEM aleatoriedade — e em ARITMÉTICA
// INTEIRA (nenhum float em todo o ficheiro).
//
// Duas delas NÃO inventam sinal novo: derivam-no de subsistemas que já existem —
// [CostFromLadder] da escada de custo (routing/tiering) e [HeadroomFromReader] da
// porta de carga do router (LoadProvider, o mesmo headroom TPM/RPM do keypool
// AOS-057). Produção liga as restantes (health, task-fit, estabilidade) aos sinais
// reais: health/estabilidade da observabilidade, task-fit do eval harness (EPIC-08),
// SEMPRE por promoção OFFLINE de uma nova versão assinada — nunca aprendidos em
// runtime (regra 4).

// ---------------------------------------------------------------------------
// CUSTO — derivado da escada de tiers existente (routing/tiering).
// ---------------------------------------------------------------------------

// LadderCost é a impl de referência do factor CUSTO: normaliza o CostRank do tier
// sobre o intervalo [min,max] da escada, de modo que o MAIS BARATO valha [Scale] e
// o mais caro valha 0. Aritmética inteira pura:
//
//	factor = Scale − Scale×(rank−min) / (max−min)
//
// Uma escada com um único CostRank (max==min) dá Scale a todos (o custo não
// discrimina — e é honesto dizê-lo em vez de inventar uma ordem).
type LadderCost struct {
	min, max int
}

// CostFromLadder constrói o factor de custo a partir da escada REAL (não de uma
// cópia da tabela de custos): o intervalo é derivado dos tiers registados.
func CostFromLadder(l *tiering.Ladder) *LadderCost {
	c := &LadderCost{}
	if l == nil {
		return c
	}
	tiers := l.Tiers()
	if len(tiers) == 0 {
		return c
	}
	c.min, c.max = tiers[0].CostRank, tiers[0].CostRank
	for _, t := range tiers[1:] {
		if t.CostRank < c.min {
			c.min = t.CostRank
		}
		if t.CostRank > c.max {
			c.max = t.CostRank
		}
	}
	return c
}

// Factor implementa [FactorProvider].
func (c *LadderCost) Factor(_ context.Context, _ Task, cand Candidate) (int, error) {
	if c.max <= c.min {
		return Scale, nil
	}
	rank := cand.Tier.CostRank
	if rank <= c.min {
		return Scale, nil
	}
	if rank >= c.max {
		return 0, nil
	}
	return Scale - Scale*(rank-c.min)/(c.max-c.min), nil
}

// ---------------------------------------------------------------------------
// HEADROOM — derivado da porta de CARGA que o router já tem (não é sinal novo).
// ---------------------------------------------------------------------------

// LoadReader é a porta MÍNIMA de leitura de carga que o factor headroom consome:
// (usado, limite, saturado). O router adapta-lhe a sua LoadProvider existente
// (router.HeadroomReaderFrom) — este pacote não reimplementa token-bucket nem
// contabilidade de TPM/RPM, apenas normaliza o que o keypool/LoadProvider já sabe.
type LoadReader interface {
	Load(ctx context.Context, provider, region string) (used, limit int64, saturated bool, err error)
}

// HeadroomFactor é a impl de referência do factor HEADROOM: a folga do eixo mais
// carregado, em milésimos, por aritmética inteira:
//
//	factor = Scale − Scale×used/limit
//
// Um endpoint SATURADO vale 0 (o router, por sua vez, descarta-o como guarda — o
// score nunca é o que o "salva"); um limite <= 0 ou um erro de leitura vale 0
// (lado seguro: sem sinal de folga não se presume folga).
type HeadroomFactor struct{ r LoadReader }

// HeadroomFromReader liga o factor headroom à porta de carga existente.
func HeadroomFromReader(r LoadReader) *HeadroomFactor { return &HeadroomFactor{r: r} }

// Factor implementa [FactorProvider].
func (h *HeadroomFactor) Factor(ctx context.Context, t Task, c Candidate) (int, error) {
	if h.r == nil {
		return 0, nil
	}
	used, limit, saturated, err := h.r.Load(ctx, t.Provider, c.Region)
	if err != nil || saturated || limit <= 0 {
		return 0, nil
	}
	if used < 0 {
		used = 0
	}
	if used >= limit {
		return 0, nil
	}
	return int(int64(Scale) - int64(Scale)*used/limit), nil
}

// ---------------------------------------------------------------------------
// LATÊNCIA — p95 medido offline, com recurso ao bit Fast da escada.
// ---------------------------------------------------------------------------

// StaticLatency é a impl de referência do factor LATÊNCIA: um mapa fixo de p95 em
// MILISSEGUNDOS por modelo, normalizado contra o MENOR p95 conhecido:
//
//	factor = Scale×minMs/ms
//
// Modelos sem p95 declarado caem no ramo estrutural da escada: um tier `Fast` vale
// [Scale], um tier lento vale Scale/2 — o mesmo viés latência-vs-batch de AOS-059,
// agora como FACTOR em vez de prioridade lexicográfica gravada. Os p95 vêm de
// medição OFFLINE; não há aqui relógio nem medição em runtime.
//
// CLASSE DA TAREFA (AOS-059, preservada sob scoring). O recurso estrutural ao bit
// `Fast` vale para tarefas INTERACTIVAS. Em [tiering.ClassBatch] o bit Fast NÃO dá
// bónus: um batch «tolera tiers lentos/baratos», e dar-lhe Scale contra Scale/2
// injectaria uma vantagem ponderada (peso_latencia × Scale/2) capaz de inverter a
// escolha para um Fast mais caro — encarecendo sistematicamente o tráfego batch e
// perdendo, em silêncio, a semântica de classe de AOS-059. Para batch sem p95
// declarado a latência simplesmente NÃO DISCRIMINA (o mesmo valor para todos), e
// quem decide passam a ser custo e task-fit. Um p95 MEDIDO continua a valer para
// as duas classes: é sinal real, não um proxy estrutural.
type StaticLatency struct {
	mu     sync.RWMutex
	ms     map[string]int64
	minMs  int64
	fastOn bool
}

// NewStaticLatency constrói o factor de latência. fastFallback=true usa o bit Fast
// da escada para os modelos sem p95 declarado (recomendado: preserva o
// comportamento interactivo de AOS-059).
func NewStaticLatency(fastFallback bool) *StaticLatency {
	return &StaticLatency{ms: make(map[string]int64), fastOn: fastFallback}
}

// SetP95 declara o p95 (ms, inteiro) de um modelo, medido offline.
func (l *StaticLatency) SetP95(model string, ms int64) *StaticLatency {
	if ms <= 0 {
		return l
	}
	l.mu.Lock()
	l.ms[model] = ms
	if l.minMs == 0 || ms < l.minMs {
		l.minMs = ms
	}
	l.mu.Unlock()
	return l
}

// Factor implementa [FactorProvider].
func (l *StaticLatency) Factor(_ context.Context, t Task, c Candidate) (int, error) {
	l.mu.RLock()
	ms, ok := l.ms[c.Tier.Model]
	fastest := l.minMs
	fastOn := l.fastOn
	l.mu.RUnlock()
	if ok && fastest > 0 {
		v := int(int64(Scale) * fastest / ms)
		return clamp(v), nil
	}
	if !fastOn {
		return 0, nil // sem sinal e sem recurso estrutural: lado seguro
	}
	if t.Class == tiering.ClassBatch {
		// Batch: o bit Fast não é bónus (ver a nota de CLASSE no doc do tipo). Valor
		// NEUTRO e igual para todos os tiers — a latência não ordena nada aqui.
		return Scale / 2, nil
	}
	if c.Tier.Fast {
		return Scale, nil
	}
	return Scale / 2, nil
}

// ---------------------------------------------------------------------------
// HEALTH / TASK-FIT / ESTABILIDADE — sinais calibrados OFFLINE, mapa fixo.
// ---------------------------------------------------------------------------

// staticTable é o núcleo partilhado das impls de referência baseadas em mapa fixo:
// um valor por chave + um valor por omissão. Sem I/O, sem relógio; seguro para uso
// concorrente (o gate corre -race).
type staticTable struct {
	mu    sync.RWMutex
	byKey map[string]int
	def   int
}

func newStaticTable(def int) staticTable {
	return staticTable{byKey: make(map[string]int), def: clamp(def)}
}

func (s *staticTable) set(key string, v int) {
	s.mu.Lock()
	s.byKey[key] = clamp(v)
	s.mu.Unlock()
}

func (s *staticTable) get(key string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.byKey[key]; ok {
		return v
	}
	return s.def
}

// StaticHealth é a impl de referência do factor SAÚDE por (provider, região):
// 0 = doente (o router não o exclui — excluir é papel das guardas; o score
// desprioriza-o), [Scale] = são. Produção liga a observabilidade real.
type StaticHealth struct{ t staticTable }

// NewStaticHealth constrói o factor com um valor por omissão (endpoint não
// configurado). Um def de 0 significa "sem sinal ⇒ não beneficia" (lado seguro).
func NewStaticHealth(def int) *StaticHealth { return &StaticHealth{t: newStaticTable(def)} }

// Set fixa a saúde de um endpoint (provider, região).
func (h *StaticHealth) Set(provider, region string, v int) *StaticHealth {
	h.t.set(provider+"|"+region, v)
	return h
}

// Factor implementa [FactorProvider].
func (h *StaticHealth) Factor(_ context.Context, t Task, c Candidate) (int, error) {
	return h.t.get(t.Provider + "|" + c.Region), nil
}

// StaticTaskFit é a impl de referência do factor TASK-FIT: o sinal de QUALIDADE por
// (modelo, capacidade exigida) produzido pelo EVAL HARNESS (EPIC-08) — o gap que o
// ADR-021 fecha. Calibrado OFFLINE e promovido por nova versão da tabela/fixtures;
// NUNCA aprendido em runtime (regra 4: sem bandit, sem exploração).
type StaticTaskFit struct{ t staticTable }

// NewStaticTaskFit constrói o factor com um valor por omissão. Um def de 0 é o
// lado seguro: um modelo sem evidência de eval não recebe crédito de qualidade.
func NewStaticTaskFit(def int) *StaticTaskFit { return &StaticTaskFit{t: newStaticTable(def)} }

// Set fixa o task-fit de (modelo, capacidade exigida pela tarefa).
func (f *StaticTaskFit) Set(model string, capability tiering.Capability, v int) *StaticTaskFit {
	f.t.set(taskFitKey(model, capability), v)
	return f
}

// Factor implementa [FactorProvider].
func (f *StaticTaskFit) Factor(_ context.Context, t Task, c Candidate) (int, error) {
	return f.t.get(taskFitKey(c.Tier.Model, t.Capability)), nil
}

// taskFitKey compõe a chave (modelo, capacidade) sem formatação dependente de
// locale — inteiro convertido por aritmética de runes ASCII.
func taskFitKey(model string, capability tiering.Capability) string {
	return model + "|" + capLabel(capability)
}

// capLabel dá um rótulo ESTÁVEL à capacidade (a chave do mapa não pode depender da
// representação numérica do iota, que pode mudar).
func capLabel(c tiering.Capability) string {
	switch c {
	case tiering.CapabilityBasic:
		return "basic"
	case tiering.CapabilityStandard:
		return "standard"
	case tiering.CapabilityFrontier:
		return "frontier"
	default:
		return "desconhecida"
	}
}

// StaticStability é a impl de referência do factor ESTABILIDADE por modelo: a taxa
// de sucesso histórica em milésimos, calibrada OFFLINE a partir do DecisionSink/
// spans (ADR-010). Nunca actualizada em runtime.
type StaticStability struct{ t staticTable }

// NewStaticStability constrói o factor com um valor por omissão.
func NewStaticStability(def int) *StaticStability { return &StaticStability{t: newStaticTable(def)} }

// Set fixa a estabilidade de um modelo.
func (s *StaticStability) Set(model string, v int) *StaticStability {
	s.t.set(model, v)
	return s
}

// Factor implementa [FactorProvider].
func (s *StaticStability) Factor(_ context.Context, _ Task, c Candidate) (int, error) {
	return s.t.get(c.Tier.Model), nil
}
