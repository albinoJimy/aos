package main

import (
	"strings"
	"testing"

	"github.com/aos-ref/platform/broker"
)

// AOS-332 — O BANNER DECLARA A POSTURA DOS DOIS EIXOS DE POLÍTICA DO BROKER.
//
// O `DEF-218` exige que o wiring asserte que a postura selada diz `enforced` — mas isso só é
// verificável a partir de um `credential.exchange.issued`, ou seja DEPOIS da primeira troca
// bem-sucedida. Um nó em `unset` que ainda não trocou nada era indistinguível de um em
// `enforced`. Esta linha quebra a circularidade: a postura passa a ser observável no ARRANQUE.

func TestAOS332_BannerDeclaraNaoAplicabilidadeQuandoNaoComposto(t *testing.T) {
	t.Parallel()
	linhas := brokerPolicyPostureBanner(posturaDaPoliticaDoBroker{})
	if len(linhas) == 0 {
		t.Fatal("o banner devia produzir pelo menos uma linha")
	}
	todo := strings.Join(linhas, "\n")

	if !strings.Contains(todo, "NAO-APLICAVEL") {
		t.Errorf("o ramo nao-composto tem de declarar NAO-APLICABILIDADE:\n%s", todo)
	}
	// O QUE ESTE TESTE EXISTE PARA IMPEDIR: declarar `unset` derivado de um nil que nunca
	// chega a ser política. `unset` é uma política não-declarada num broker que EXISTE; aqui o
	// broker não existe, e usar a mesma palavra faria o operador ler uma postura onde não há
	// nenhuma. É a regra do cabeçalho deste ficheiro — uma linha que fala de postura sobre algo
	// que não está composto é pior do que o silêncio que substitui.
	if strings.Contains(todo, "provider_policy=unset") || strings.Contains(todo, ": unset") {
		t.Errorf("o ramo nao-composto NAO pode declarar uma postura `unset` inventada:\n%s", todo)
	}
	// E tem de nomear o que o wiring terá de declarar — senão não é informação, é uma recusa.
	for _, quer := range []string{"WithClassProviders", "WithGateProviderHosts", "DEF-218"} {
		if !strings.Contains(todo, quer) {
			t.Errorf("o banner devia nomear %q:\n%s", quer, todo)
		}
	}
}

// TestAOS332_OsEstadosCompostosProduzemLinhasDistintas é o CONTROLO que impede que o banner seja
// uma constante. Quatro combinações dos dois eixos têm de produzir quatro textos distintos —
// senão declarar a postura não declara nada.
func TestAOS332_OsEstadosCompostosProduzemLinhasDistintas(t *testing.T) {
	t.Parallel()
	combinacoes := []posturaDaPoliticaDoBroker{
		{Composto: true, Provider: broker.ProviderPostureUnset, Recurso: broker.ResourceBindingUnset},
		{Composto: true, Provider: broker.ProviderPostureEnforced, Recurso: broker.ResourceBindingUnset},
		{Composto: true, Provider: broker.ProviderPostureUnset, Recurso: broker.ResourceBindingEnforced},
		{Composto: true, Provider: broker.ProviderPostureEnforced, Recurso: broker.ResourceBindingEnforced},
	}
	vistos := map[string]int{}
	for i, c := range combinacoes {
		txt := strings.Join(brokerPolicyPostureBanner(c), "\n")
		if j, repetido := vistos[txt]; repetido {
			t.Errorf("as combinacoes %d e %d produzem o MESMO texto — o banner nao distingue as posturas", j, i)
		}
		vistos[txt] = i
		// Cada linha tem de nomear o eixo a que se refere, senão o operador não sabe qual
		// das duas posturas está a ler.
		if !strings.Contains(txt, "eixo provider") || !strings.Contains(txt, "eixo recurso") {
			t.Errorf("combinacao %d nao nomeia os dois eixos:\n%s", i, txt)
		}
	}
	// E o ramo composto tem de ser distinto do não-composto, que é a distinção que mais importa.
	naoComposto := strings.Join(brokerPolicyPostureBanner(posturaDaPoliticaDoBroker{}), "\n")
	if _, colide := vistos[naoComposto]; colide {
		t.Error("o ramo nao-composto e indistinguivel de um ramo composto")
	}
}

// TestAOS332_BannerSaiNoArranqueReal liga a função pura ao caminho de arranque REAL: se alguém a
// escrever e nunca a chamar, o defeito continua de pé e este teste cai. É o molde de
// `TestAOS248_BannerSaiNoArranqueReal`, e existe pela mesma razão — uma função de banner que não
// é chamada declara-se a si própria.
func TestAOS332_BannerSaiNoArranqueReal(t *testing.T) {
	banner := runWithoutTouchingBoardRegions(t)
	const marcador = "politica do broker (AOS-324/AOS-330/AOS-331): NAO-APLICAVEL"
	if !strings.Contains(banner, marcador) {
		t.Errorf("o arranque real devia imprimir %q — a postura tem de sair do BANNER, nao so da funcao pura.\nBanner:\n%s", marcador, banner)
	}
}
