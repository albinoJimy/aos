package routingstage_test

import (
	"errors"
	"testing"

	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/routing/routingstage"
	"github.com/aos-ref/platform/model-gateway/routing/sovereignty"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// staticInv é o inventário de teste (a mesma forma que failover.StaticInventory).
type staticInv map[string][]sovereignty.Endpoint

func (m staticInv) Endpoints(provider string) []sovereignty.Endpoint { return m[provider] }

// prodLadder é a escada de teste: os dois modelos que a allowlist EMBEBIDA permite
// ao board-eu, com capacidades distintas para se ver o piso de capacidade a operar.
func prodLadder() *tiering.Ladder {
	return tiering.NewLadder(
		tiering.Tier{Name: "standard", Model: "gpt-4o", CostRank: 2, Capability: tiering.CapabilityStandard, Fast: true},
		tiering.Tier{Name: "economy", Model: "gpt-4o-mini", CostRank: 1, Capability: tiering.CapabilityBasic},
	)
}

func embeddedPolicy(t *testing.T) *allowlist.Policy {
	t.Helper()
	pol, err := allowlist.LoadPolicy()
	if err != nil {
		t.Fatalf("allowlist.LoadPolicy: %v", err)
	}
	return pol
}

// TestProductionClassifier_Unconfigured — sem policy, inventário ou escada, o
// classificador NÃO se constrói (fail-closed na construção, não em runtime).
func TestProductionClassifier_Unconfigured(t *testing.T) {
	full := routingstage.ClassifierConfig{
		Policy: embeddedPolicy(t), Inventory: staticInv{}, Ladder: prodLadder(),
	}
	for name, mutate := range map[string]func(c *routingstage.ClassifierConfig){
		"sem policy":     func(c *routingstage.ClassifierConfig) { c.Policy = nil },
		"sem inventario": func(c *routingstage.ClassifierConfig) { c.Inventory = nil },
		"sem escada":     func(c *routingstage.ClassifierConfig) { c.Ladder = nil },
	} {
		cfg := full
		mutate(&cfg)
		if _, err := routingstage.NewProductionClassifier(cfg); !errors.Is(err, routingstage.ErrClassifierUnconfigured) {
			t.Fatalf("%s: devia ser fail-closed; got %v", name, err)
		}
	}
	if _, err := routingstage.NewProductionClassifier(full); err != nil {
		t.Fatalf("configuração completa devia construir: %v", err)
	}
}

// TestProductionClassifier_CandidatesFromInventory — os candidatos vêm do INVENTÁRIO
// e são filtrados pelas duas restrições que já existem: as regiões LEGAIS do board
// para o modelo (allowlist) e a SAÚDE do endpoint (a decisão do failover). Sem isto o
// refino não teria o que comparar — ou compararia coisas que não pode escolher.
func TestProductionClassifier_CandidatesFromInventory(t *testing.T) {
	cls, err := routingstage.NewProductionClassifier(routingstage.ClassifierConfig{
		Policy: embeddedPolicy(t),
		Inventory: staticInv{"openai": {
			{KeyID: "k-eu", Region: "eu"},
			{KeyID: "k-euw", Region: "eu-west"},
			{KeyID: "k-us", Region: "us-east"},  // FORA da fronteira legal do board-eu
			{KeyID: "k-doente", Region: "eu"},   // intra-fronteira mas DOENTE
			{KeyID: "", Region: "eu"},           // chave indefinida: nunca candidato
			{KeyID: "k-sem-regiao", Region: ""}, // jurisdição indefinida: nunca candidato
		}},
		Ladder: prodLadder(),
		Health: func(e sovereignty.Endpoint) bool { return e.KeyID != "k-doente" },
	})
	if err != nil {
		t.Fatalf("NewProductionClassifier: %v", err)
	}
	task := cls.Classify(&pipeline.Exchange{
		Board: "board-eu", RequestedModel: "gpt-4o", RequestedProvider: "openai", RequestedRegion: "eu",
	})
	got := map[string]string{}
	for _, c := range task.Candidates {
		got[c.KeyID] = c.Region
	}
	if len(got) != 2 || got["k-eu"] != "eu" || got["k-euw"] != "eu-west" {
		t.Fatalf("candidatos = %+v; queria só as contas legais e saudáveis (k-eu@eu, k-euw@eu-west)", task.Candidates)
	}
	// A CAPACIDADE é a do tier do modelo PEDIDO — o piso que o refino nunca desce.
	if task.Capability != tiering.CapabilityStandard {
		t.Fatalf("capacidade derivada = %v; o gpt-4o é Standard na escada", task.Capability)
	}
	if task.NoRefine != "" {
		t.Fatalf("um modelo declarado na escada é caracterizável: %q", task.NoRefine)
	}
	// A porta [Classifier] que o estágio consome é a MESMA classificação (é assim que
	// o composition root a liga com WithClassifier).
	viaPort := cls.Classifier()(&pipeline.Exchange{
		Board: "board-eu", RequestedModel: "gpt-4o", RequestedProvider: "openai", RequestedRegion: "eu",
	})
	if viaPort.Capability != task.Capability || len(viaPort.Candidates) != len(task.Candidates) {
		t.Fatalf("a porta Classifier() diverge de Classify: %+v vs %+v", viaPort, task)
	}
}

// TestProductionClassifier_UsesResolvedInput — o classificador parte da MESMA entrada
// que o estágio: a região/provider RESOLVIDOS pelo estágio anterior. Se divergisse,
// os candidatos seriam derivados de uma chamada e a decisão ancorada noutra.
func TestProductionClassifier_UsesResolvedInput(t *testing.T) {
	cls, err := routingstage.NewProductionClassifier(routingstage.ClassifierConfig{
		Policy:    embeddedPolicy(t),
		Inventory: staticInv{"openai": {{KeyID: "k-euw", Region: "eu-west"}}, "outro": {{KeyID: "x", Region: "eu"}}},
		Ladder:    prodLadder(),
	})
	if err != nil {
		t.Fatalf("NewProductionClassifier: %v", err)
	}
	task := cls.Classify(&pipeline.Exchange{
		Board: "board-eu", RequestedModel: "gpt-4o",
		RequestedProvider: "outro", ResolvedProvider: "openai", // resolvido primeiro
		RequestedRegion: "eu", ResolvedRegion: "eu-west",
	})
	if len(task.Candidates) != 1 || task.Candidates[0].KeyID != "k-euw" {
		t.Fatalf("candidatos = %+v; o inventário tem de vir do provedor RESOLVIDO", task.Candidates)
	}
}

// TestProductionClassifier_UnknownModelNoRefine — um modelo fora da escada não é
// caracterizável: o classificador declara-o (com razão) e o refino não corre. Nunca
// uma capacidade inventada, que autorizaria uma descida cega de qualidade.
func TestProductionClassifier_UnknownModelNoRefine(t *testing.T) {
	cls, err := routingstage.NewProductionClassifier(routingstage.ClassifierConfig{
		Policy: embeddedPolicy(t), Inventory: staticInv{}, Ladder: prodLadder(),
	})
	if err != nil {
		t.Fatalf("NewProductionClassifier: %v", err)
	}
	task := cls.Classify(&pipeline.Exchange{Board: "board-eu", RequestedModel: "modelo-nao-declarado"})
	if task.NoRefine == "" {
		t.Fatal("um modelo fora da escada tem de declarar NoRefine (com razão), não uma capacidade inventada")
	}
	if len(task.Candidates) != 0 {
		t.Fatalf("sem caracterização não se oferecem candidatos: %+v", task.Candidates)
	}
}

// TestProductionClassifier_ProfileFromClass — o perfil de pesos é a INTENÇÃO derivada
// da classe da chamada; e [Profiles] enumera exactamente os nomes que podem ser
// pedidos (o composition root valida-os contra a tabela assinada no arranque).
func TestProductionClassifier_ProfileFromClass(t *testing.T) {
	cls, err := routingstage.NewProductionClassifier(routingstage.ClassifierConfig{
		Policy: embeddedPolicy(t), Inventory: staticInv{}, Ladder: prodLadder(),
		DefaultProfile: "balanced",
		ProfileByClass: map[tiering.Class]string{tiering.ClassBatch: "cheap"},
		ClassOf: func(ex *pipeline.Exchange) tiering.Class {
			if ex.AgentClass == "lote" {
				return tiering.ClassBatch
			}
			return tiering.ClassInteractive
		},
	})
	if err != nil {
		t.Fatalf("NewProductionClassifier: %v", err)
	}
	inter := cls.Classify(&pipeline.Exchange{Board: "board-eu", RequestedModel: "gpt-4o"})
	if inter.Profile != "balanced" || inter.Class != tiering.ClassInteractive {
		t.Fatalf("interactivo: perfil=%q classe=%v", inter.Profile, inter.Class)
	}
	batch := cls.Classify(&pipeline.Exchange{Board: "board-eu", RequestedModel: "gpt-4o", AgentClass: "lote"})
	if batch.Profile != "cheap" || batch.Class != tiering.ClassBatch {
		t.Fatalf("batch: perfil=%q classe=%v", batch.Profile, batch.Class)
	}
	profiles := cls.Profiles()
	if len(profiles) != 2 || profiles[0] != "balanced" || profiles[1] != "cheap" {
		t.Fatalf("Profiles() = %v; queria os nomes pedíveis, ordenados", profiles)
	}
}
