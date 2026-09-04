package routingstage

import (
	"errors"
	"fmt"
	"sort"

	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/routing/sovereignty"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// classifier.go é o CLASSIFICADOR DE PRODUÇÃO (AOS-280) — a peça que faltava para o
// router de AOS-059 ter efeito real quando composto no pipeline.
//
// # Porque o DefaultClassifier não serve produção
//
// [DefaultClassifier] devolve capacidade STANDARD, classe INTERACTIVA e NENHUM
// candidato. Sem [Task.Candidates] o `partition` do router trata a região pedida
// como ÚNICO sobrevivente e, a partir daí, tudo o que o router sabe fazer fica
// degenerado: o headroom não tem regiões que comparar, o tiering escolhe dentro de
// uma só região e o ranking ponderado ordena um conjunto que não tem alternativas.
// Compor o estágio com ele seria ligar uma capacidade que continua sem efeito.
//
// # O que o classificador de produção deriva (e de que fonte REAL)
//
//   - CAPACIDADE — do tier do MODELO pedido na escada declarada pelo deployment
//     ([tiering.Ladder]). É o pedido que declara o piso de capacidade: quem pede um
//     modelo frontier exige raciocínio, e o refino nunca desce abaixo disso. Um
//     modelo fora da escada não é caracterizável ⇒ [Task.NoRefine] (a resolução do
//     failover fica de pé), NUNCA uma capacidade inventada que autorizaria uma
//     descida cega de qualidade.
//   - CANDIDATOS — do INVENTÁRIO de contas de infra (a mesma fonte de verdade que
//     alimenta o keypool e o router de failover), filtrado por DUAS restrições que
//     já existem e não são inventadas aqui:
//     (a) REGIÕES LEGAIS — `policy.AllowedRegions(board, modelo)`, exactamente a
//     fronteira que o failover deriva. O refino nunca considera uma região que a
//     allowlist regional não permita a este board para este modelo;
//     (b) SAÚDE — a MESMA [sovereignty.HealthFunc] que o failover usa. A saúde é
//     DECISÃO (do failover), não peso: um endpoint que a guarda de saúde excluiu
//     não volta a entrar como candidato do ranking. É isto que impede o refino de
//     desfazer um failover por saúde deliberado, mesmo com o score a favor.
//   - PERFIL DE PESOS — da CLASSE da chamada (a intenção declarada, ADR-021 §1 gap
//     2), por um mapa REVISTO pelo deployment. Vazio ⇒ o perfil composto no scorer
//     (o default DECLARADO na tabela assinada), nunca um nome inventado aqui.
//
// # Determinismo
//
// Sem relógio, sem rede e sem aleatoriedade: mapas pequenos, ordem estável e
// aritmética inteira. A saúde e a política de classe são portas injectadas.

// ErrClassifierUnconfigured — o classificador de produção foi pedido sem uma das
// suas três fontes obrigatórias (policy de allowlist, inventário, escada de tiers).
// Fail-closed na CONSTRUÇÃO: sem elas classificaria às cegas, e um classificador que
// erra é um router que rota mal — melhor não existir do que existir errado.
var ErrClassifierUnconfigured = errors.New("routingstage: classificador de producao exige policy de allowlist, inventario e escada de tiers (fail-closed)")

// Inventory é a porta do INVENTÁRIO de endpoints de infra por provider — a mesma
// forma que o router de failover consome (failover.StaticInventory satisfá-la). É
// declarada aqui, e não importada de lá, para o classificador não depender do
// estágio vizinho: o composition root passa O MESMO inventário aos dois, que é o que
// os mantém coerentes.
type Inventory interface {
	// Endpoints devolve os endpoints de infra do provider (em qualquer região).
	Endpoints(provider string) []sovereignty.Endpoint
}

// ClassifierConfig configura o [ProductionClassifier].
type ClassifierConfig struct {
	// Policy é a allowlist regional EM VIGOR (a activada pelo composition root). É a
	// fonte da fronteira legal de cada chamada. OBRIGATÓRIA.
	Policy *allowlist.Policy
	// Inventory é o inventário de contas de infra. OBRIGATÓRIO.
	Inventory Inventory
	// Ladder é a escada de tiers declarada pelo deployment (custo/capacidade dos
	// modelos que este nó pode servir). OBRIGATÓRIA.
	Ladder *tiering.Ladder
	// Health é a saúde do endpoint — a MESMA função que o estágio de failover usa.
	// Nil ⇒ todos saudáveis (nenhum candidato é excluído por saúde).
	Health sovereignty.HealthFunc
	// ClassOf deriva a CLASSE (interactiva vs batch) dos hints da chamada. Nil ⇒
	// [tiering.ClassInteractive] para tudo — o lado conservador: tratar como batch
	// uma chamada interactiva trocaria latência por custo em silêncio, e o Exchange
	// não transporta hoje um sinal de batch (os hints disponíveis são a operação, a
	// classe de agente e o board — que só o deployment sabe interpretar).
	ClassOf func(ex *pipeline.Exchange) tiering.Class
	// ProfileByClass mapeia a classe da chamada para o PERFIL de pesos (ADR-021). Os
	// nomes têm de existir na tabela ASSINADA em vigor — o composition root valida-o
	// no arranque ([ProductionClassifier.Profiles]), para que um perfil inexistente
	// seja uma recusa de BOOT e não uma rejeição por chamada em produção. Ausente ⇒
	// [ClassifierConfig.DefaultProfile].
	ProfileByClass map[tiering.Class]string
	// DefaultProfile é o perfil aplicado quando a classe não tem um próprio. Vazio ⇒
	// o perfil COMPOSTO no scorer (o default declarado na tabela assinada).
	DefaultProfile string
}

// ProductionClassifier é o [Classifier] de produção. Imutável após a construção e
// seguro para uso concorrente (só-leitura sobre estruturas fixas).
type ProductionClassifier struct {
	policy    *allowlist.Policy
	inventory Inventory
	ladder    *tiering.Ladder
	health    sovereignty.HealthFunc
	classOf   func(ex *pipeline.Exchange) tiering.Class
	byClass   map[tiering.Class]string
	defProf   string
}

// NewProductionClassifier constrói o classificador de produção. Fail-closed: sem
// policy, inventário ou escada devolve [ErrClassifierUnconfigured].
func NewProductionClassifier(cfg ClassifierConfig) (*ProductionClassifier, error) {
	if cfg.Policy == nil || cfg.Inventory == nil || cfg.Ladder == nil {
		return nil, ErrClassifierUnconfigured
	}
	c := &ProductionClassifier{
		policy:    cfg.Policy,
		inventory: cfg.Inventory,
		ladder:    cfg.Ladder,
		health:    cfg.Health,
		classOf:   cfg.ClassOf,
		defProf:   cfg.DefaultProfile,
	}
	if len(cfg.ProfileByClass) > 0 {
		c.byClass = make(map[tiering.Class]string, len(cfg.ProfileByClass))
		for k, v := range cfg.ProfileByClass {
			c.byClass[k] = v
		}
	}
	return c, nil
}

// Classifier devolve a função [Classifier] deste classificador (o que o estágio
// consome via [WithClassifier]).
func (c *ProductionClassifier) Classifier() Classifier { return c.Classify }

// Profiles devolve, ORDENADOS, os nomes de perfil NÃO-VAZIOS que este classificador
// pode pedir. O composition root valida-os contra a tabela de pesos assinada ANTES
// de servir tráfego: um perfil que a tabela não conhece é uma rejeição fail-closed
// do router (por desenho, sem queda silenciosa no default) — e essa rejeição deve
// acontecer no arranque, uma vez, e não em cada chamada de modelo em produção.
func (c *ProductionClassifier) Profiles() []string {
	set := make(map[string]struct{}, len(c.byClass)+1)
	if c.defProf != "" {
		set[c.defProf] = struct{}{}
	}
	for _, p := range c.byClass {
		if p != "" {
			set[p] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Classify implementa [Classifier]. Ver o cabeçalho do ficheiro para as fontes de
// cada campo. Determinística.
func (c *ProductionClassifier) Classify(ex *pipeline.Exchange) Task {
	model := ex.ResolvedModel
	if model == "" {
		model = ex.RequestedModel
	}
	tier, known := c.ladder.TierOfModel(model)
	if !known {
		return Task{NoRefine: fmt.Sprintf(
			"sem refino: o modelo %q esta fora da escada de tiers declarada — capacidade/custo nao caracterizaveis; mantem-se a rota resolvida pela guarda de soberania",
			model)}
	}
	class := tiering.ClassInteractive
	if c.classOf != nil {
		class = c.classOf(ex)
	}
	return Task{
		Capability: tier.Capability,
		Class:      class,
		Profile:    c.profileFor(class),
		Candidates: c.candidates(ex, model),
		// DEFERIDO (DEF-280-TOKENS) — EstimatedTokens fica a ZERO: o [pipeline.Exchange] não
		// transporta o prompt (a fachada do GW só o passa ao adaptador), pelo que não há aqui
		// por onde estimar tokens sem inventar. A reserva de admissão degrada, então, para o
		// custo MÍNIMO (1 pedido) — coordena na dimensão de pedidos, não na de tokens.
		//
		// O MARCADOR ESTAVA EM FALTA e a dívida não: o gate `deferrals` casava os IDs do registo
		// com `DEF-\d{3}` seguido de barra, pelo que a linha `DEF-280-TOKENS` era descartada em
		// silêncio e ninguém confrontava o registo com o código. Alargada a regex (achado E-03
		// de `analises/10`), o gate passou a acusar esta entrada como apodrecida — e a resposta
		// certa é repor o marcador, porque a dívida continua exactamente aqui, e não remover a
		// linha, que seria apagar do registo uma dívida que existe.
		EstimatedTokens: 0,
	}
}

// profileFor resolve o perfil de pesos da classe (mapa revisto pelo deployment),
// caindo no default declarado — nunca num nome inventado.
func (c *ProductionClassifier) profileFor(class tiering.Class) string {
	if p, ok := c.byClass[class]; ok && p != "" {
		return p
	}
	return c.defProf
}

// candidates constrói os endpoints candidatos ao refino: os do inventário do
// provedor da chamada que estão (a) numa região LEGAL para (board, modelo) e (b)
// SAUDÁVEIS. A ordem é a do inventário; o router normaliza-a por KeyID, pelo que a
// decisão não depende desta ordem.
func (c *ProductionClassifier) candidates(ex *pipeline.Exchange, model string) []sovereignty.Endpoint {
	legal := c.policy.AllowedRegions(ex.Board, model)
	if len(legal) == 0 {
		return nil // nenhuma região legal: o router fica-se pela região já resolvida
	}
	allowed := make(map[string]struct{}, len(legal))
	for _, r := range legal {
		allowed[r] = struct{}{}
	}
	eps := c.inventory.Endpoints(InputProvider(ex))
	out := make([]sovereignty.Endpoint, 0, len(eps))
	for _, e := range eps {
		if e.KeyID == "" || e.Region == "" {
			continue // jurisdição/chave indefinida: fail-closed (nunca candidato)
		}
		if _, ok := allowed[e.Region]; !ok {
			continue // fora da fronteira legal do board para este modelo
		}
		if c.health != nil && !c.health(e) {
			continue // saúde é DECISÃO do failover, não peso do score
		}
		out = append(out, e)
	}
	return out
}

// Compile-time: o classificador de produção satisfaz a porta [Classifier].
var _ Classifier = (*ProductionClassifier)(nil).Classify
