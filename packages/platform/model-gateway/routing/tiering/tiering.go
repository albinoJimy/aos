// Package tiering é a TABELA DE TIERS de modelo do Model Gateway e as regras de
// selecção por CUSTO/CAPACIDADE e por LATÊNCIA-vs-BATCH (AOS-059, tecnica/06 §6).
//
// # O que este pacote decide (e o que NÃO decide)
//
// O *model tiering* classifica os modelos por CUSTO (CostRank, menor = mais
// barato) e por CAPACIDADE (o mais capaz serve raciocínio; o económico serve
// classificação/extracção). A regra central é: escolher o tier MAIS BARATO que
// SATISFAZ a capacidade exigida pela tarefa. Chamadas INTERACTIVAS favorecem
// menor latência (o tier mais rápido dentro da capacidade); chamadas BATCH
// toleram tiers mais lentos e baratos.
//
// Este pacote é PURO e DETERMINÍSTICO: sem I/O, sem relógio, sem aleatoriedade.
// Não conhece carga, soberania nem orçamento — esses sinais são compostos pelo
// router (routing/router), que sobrepõe a sua escolha SEMPRE dentro da fronteira
// de soberania (AOS-058). O tiering é a dimensão CUSTO/CAPACIDADE isolada.
//
// # Relação com a porta scheduler.ModelTierRouter (AOS-031)
//
// [Ladder.Cheaper] desce UM degrau na escada de custo (o tier imediatamente mais
// barato) — a mesma semântica do StaticModelTierRouter de referência do
// Escalonador. O adaptador de produção (routing/tieradapter) traduz esta escada
// para a porta scheduler.ModelTierRouter SEM o Escalonador reimplementar a
// degradação: o GW é dono da ESCOLHA de tier, o Escalonador da CADEIA.
package tiering

import "sort"

// Capability é o grau de capacidade EXIGIDO por uma tarefa ou OFERECIDO por um
// tier. É uma escada ordenada: um tier satisfaz a tarefa se a sua capacidade for
// >= à exigida. Modelos "frontier" oferecem [CapabilityFrontier] (raciocínio);
// modelos económicos oferecem [CapabilityBasic] (classificação/extracção).
type Capability int

const (
	// CapabilityBasic — classificação, extracção, tarefas simples (tier económico).
	CapabilityBasic Capability = iota
	// CapabilityStandard — geração/sumarização de dificuldade média.
	CapabilityStandard
	// CapabilityFrontier — raciocínio complexo (tier frontier, o mais capaz).
	CapabilityFrontier
)

// Class é a classe de latência/prioridade da chamada: interactiva favorece
// latência; batch tolera tiers mais lentos e baratos.
type Class int

const (
	// ClassInteractive — chamada interactiva: favorece MENOR latência (tier rápido)
	// dentro da capacidade exigida.
	ClassInteractive Class = iota
	// ClassBatch — chamada batch: tolera tiers mais LENTOS e BARATOS; a escolha
	// prefere o custo absoluto mais baixo dentro da capacidade.
	ClassBatch
)

// Tier é um degrau da escada de tiers: um nome lógico, o modelo concreto, o custo
// (CostRank, menor = mais barato), a capacidade OFERECIDA e se o endpoint é de
// baixa latência (Fast). A escada ordena-se por CostRank ascendente.
type Tier struct {
	// Name é o nome lógico do tier (ex.: "frontier", "standard", "economy").
	Name string
	// Model é o identificador do modelo concreto do tier.
	Model string
	// CostRank ordena por custo: quanto MENOR, mais barato. Cheaper desce para o
	// CostRank imediatamente inferior.
	CostRank int
	// Capability é a capacidade OFERECIDA pelo tier (satisfaz tarefas que exijam
	// capacidade <= a esta).
	Capability Capability
	// Fast marca um endpoint/modelo de baixa latência (preferido por chamadas
	// interactivas dentro da capacidade exigida).
	Fast bool
}

// Request é o pedido de selecção de tier: a capacidade EXIGIDA pela tarefa e a
// classe (interactiva vs batch). Sem carga/soberania/orçamento — esses são do
// router.
type Request struct {
	// Capability é a capacidade mínima que o tier escolhido tem de satisfazer.
	Capability Capability
	// Class guia o ramo latência-vs-batch.
	Class Class
}

// Ladder é a escada de tiers ordenada por CostRank ascendente (índice 0 = mais
// barato). Determinística e imutável após [NewLadder]. Segura para uso
// concorrente (só-leitura).
type Ladder struct {
	tiers []Tier
	// idxByName localiza o tier corrente pelo nome lógico.
	idxByName map[string]int
	// idxByModel localiza o tier corrente pelo modelo concreto (o Escalonador passa
	// o modelo corrente; o tier pode ser derivado dele).
	idxByModel map[string]int
}

// NewLadder constrói a escada, ordenando por CostRank ascendente (desempate
// estável por Name) para que a selecção seja determinística. Tiers com Name vazio
// são ignorados. Nomes/modelos duplicados: o PRIMEIRO na ordenação vence a
// localização (evite duplicados na configuração).
func NewLadder(tiers ...Tier) *Ladder {
	clean := make([]Tier, 0, len(tiers))
	for _, t := range tiers {
		if t.Name == "" {
			continue
		}
		clean = append(clean, t)
	}
	sort.SliceStable(clean, func(i, j int) bool {
		if clean[i].CostRank != clean[j].CostRank {
			return clean[i].CostRank < clean[j].CostRank
		}
		return clean[i].Name < clean[j].Name
	})
	l := &Ladder{
		tiers:      clean,
		idxByName:  make(map[string]int, len(clean)),
		idxByModel: make(map[string]int, len(clean)),
	}
	for i, t := range clean {
		if _, ok := l.idxByName[t.Name]; !ok {
			l.idxByName[t.Name] = i
		}
		if t.Model != "" {
			if _, ok := l.idxByModel[t.Model]; !ok {
				l.idxByModel[t.Model] = i
			}
		}
	}
	return l
}

// Tiers devolve a escada ordenada (cópia defensiva) para introspecção/teste.
func (l *Ladder) Tiers() []Tier {
	out := make([]Tier, len(l.tiers))
	copy(out, l.tiers)
	return out
}

// Filter é um predicado que aceita/rejeita um tier candidato ANTES da selecção —
// o router injecta aqui a guarda de soberania (AOS-058) e a allowlist regional,
// de modo que a selecção de tiering NUNCA considere um modelo fora da fronteira.
// Nil = todos os tiers são elegíveis.
type Filter func(Tier) bool

// Select escolhe o tier MAIS BARATO que satisfaz a capacidade exigida, aplicando
// o ramo latência-vs-batch e o filtro (soberania/allowlist) do router:
//
//   - elegíveis = tiers com Capability >= req.Capability E que passem o filtro;
//   - INTERACTIVO: favorece LATÊNCIA — prefere um tier Fast; entre os de igual
//     rapidez, o mais barato (CostRank menor). Assim uma chamada interactiva
//     nunca é servida por um tier lento se houver um rápido dentro da capacidade.
//   - BATCH: favorece CUSTO — o CostRank mais baixo dentro da capacidade
//     (tolerando tiers lentos e baratos).
//
// Desempate final estável por Name (determinismo). Devolve ok=false se NENHUM
// tier elegível satisfaz a capacidade (dentro do filtro) — o router traduz isto
// numa rejeição fail-closed (nunca escolhe um tier abaixo da capacidade).
func (l *Ladder) Select(req Request, filter Filter) (Tier, bool) {
	var best Tier
	found := false
	for _, t := range l.tiers {
		if t.Capability < req.Capability {
			continue // não satisfaz a capacidade exigida
		}
		if filter != nil && !filter(t) {
			continue // fora da fronteira de soberania/allowlist: descartado
		}
		if !found || betterThan(t, best, req.Class) {
			best, found = t, true
		}
	}
	return best, found
}

// betterThan reporta se o tier a é PREFERÍVEL a b para a classe dada. Interactivo
// prioriza Fast (latência) e depois custo; batch prioriza custo. Desempate por
// Name (estável/determinista).
func betterThan(a, b Tier, class Class) bool {
	if class == ClassInteractive && a.Fast != b.Fast {
		return a.Fast // um tier rápido bate um lento na chamada interactiva
	}
	if a.CostRank != b.CostRank {
		return a.CostRank < b.CostRank // mais barato bate mais caro
	}
	return a.Name < b.Name
}

// TierOf localiza o tier corrente pelo NOME lógico (ex.: alvo de um Cheaper). ok=
// false se desconhecido.
func (l *Ladder) TierOf(name string) (Tier, bool) {
	i, ok := l.idxByName[name]
	if !ok {
		return Tier{}, false
	}
	return l.tiers[i], true
}

// TierOfModel localiza o tier corrente pelo MODELO concreto (o Escalonador passa o
// modelo corrente). ok=false se desconhecido.
func (l *Ladder) TierOfModel(model string) (Tier, bool) {
	i, ok := l.idxByModel[model]
	if !ok {
		return Tier{}, false
	}
	return l.tiers[i], true
}

// Cheaper desce UM degrau na escada de CUSTO a partir do tier corrente (por nome),
// devolvendo o tier imediatamente mais barato que PASSA o filtro (soberania/
// allowlist). Percorre para baixo saltando os tiers filtrados — nunca devolve um
// tier fora da fronteira (a prova estrutural: o filtro é aplicado ANTES de
// devolver). ok=false se o corrente já é o mais barato elegível, ou desconhecido
// — não há para onde descer (nunca um "upgrade" acidental). Determinística.
//
// É a semântica que a porta scheduler.ModelTierRouter exige (Cheaper desce um
// degrau); o filtro é o que a torna cost/load/soberania-aware face à referência
// estática do Escalonador.
func (l *Ladder) Cheaper(currentName string, filter Filter) (Tier, bool) {
	i, ok := l.idxByName[currentName]
	if !ok {
		return Tier{}, false
	}
	for j := i - 1; j >= 0; j-- {
		t := l.tiers[j]
		if filter != nil && !filter(t) {
			continue // degrau fora da fronteira: salta (nunca degrada para fora)
		}
		return t, true
	}
	return Tier{}, false
}

// CheaperByModel é [Ladder.Cheaper] a partir do MODELO corrente (o Escalonador
// identifica o corrente pelo modelo). ok=false se desconhecido ou já o mais barato.
func (l *Ladder) CheaperByModel(currentModel string, filter Filter) (Tier, bool) {
	i, ok := l.idxByModel[currentModel]
	if !ok {
		return Tier{}, false
	}
	for j := i - 1; j >= 0; j-- {
		t := l.tiers[j]
		if filter != nil && !filter(t) {
			continue
		}
		return t, true
	}
	return Tier{}, false
}
