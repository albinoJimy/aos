package scheduler

// aos425_chave_de_quota_test.go — A CHAVE DE QUOTA É UM `stream_id`, E QUEM A DEFENDE ESTÁ A
// MONTANTE.
//
// # A DECISÃO, ESCRITA
//
// `admission/bucket/<provider>:<model>:<region>` é composta em runtime a partir de valores que
// não são escolhidos aqui: o `Model` vem de `tier.Model`, da escada de `RoutingConfig.Tiers`; a
// `Region` de `AOS_MODEL_REGION` ou do inventário de endpoints; o `Provider`, do PEDIDO.
//
// Decidiu-se **validar na ENTRADA e não normalizar aqui**:
//
//   - Validar na entrada: a composição da escada recusa no ARRANQUE um modelo (ou a região do
//     gateway) que não possa aparecer num `stream_id` — `ErrRoutingModelNaoRepresentavel`, em
//     `model-gateway/production_routing.go`, ao lado da cobertura de preço, que é o precedente
//     exacto.
//
//     **A primeira versão pôs isso na carga da ALLOWLIST, e estava errado**: a allowlist
//     AUTORIZA um modelo, a escada FORNECE-O — validar lá não tocava no valor que compõe a
//     chave, e partia a razão de ser documentada do bundle externo («pedir nomes de modelo
//     reais»), fazendo o nó recusar arrancar sobre um bundle assinado e válido.
//   - NÃO normalizar (`.` → `-`): duas chaves distintas colapsariam no mesmo bucket de quota, e
//     dois modelos partilhariam o mesmo token-bucket em silêncio. É a mesma razão pela qual o
//     `subjectDe` recusa em vez de escapar. Para nomes de instância INTERNOS (`queue/<name>`,
//     `routing/<name>`, …) a normalização seria aceitável — mas esses valores são escolhidos em
//     código, e a decisão escrita para eles é «não se normaliza porque não é preciso».
//
// # ESTADO REAL DESTE CAMINHO, MEDIDO
//
// A cadeia de admissão **não está ligada a binário nenhum**: nada na árvore constrói
// `[]tiering.Tier` fora de testes, e o wiring do nó declara-o como deferimento (`DEF-280-NO`).
// Isto não é uma desculpa para não decidir — é o que torna a decisão barata AGORA e cara depois.
// Estes testes existem para que o dia em que alguém ligar a cadeia não seja o dia em que
// descobre o acoplamento.

import (
	"strings"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// A CHAVE COMPOSTA A PARTIR DA ALLOWLIST EM VIGOR É UM `stream_id` VÁLIDO.
//
// Os modelos são os da policy embebida (`gpt-4o`, `gpt-4o-mini`, `text-embedding-3-large`) e as
// regiões as suas. Escritos à mão de propósito: o `scheduler` não importa o Model Gateway, e um
// teste que derivasse os valores da mesma fonte que os produz não provaria que as duas pontas
// concordam.
func TestAOS425ChaveDeQuotaDaAllowlistEmVigorEeValida(t *testing.T) {
	for _, modelo := range []string{"gpt-4o", "gpt-4o-mini", "text-embedding-3-large"} {
		for _, regiao := range []string{"eu", "eu-west", "us-east"} {
			k := ProviderKey{Provider: "openai", Model: modelo, Region: regiao}
			for _, prefixo := range []string{bucketStreamPrefix, auditStreamPrefix} {
				nome := prefixo + k.String()
				if err := eventstore.ValidarStreamID(nome); err != nil {
					t.Errorf("a chave %q nao e um stream_id valido: %v", nome, err)
				}
			}
		}
	}
}

// E COM UM NOME DE MODELO REAL QUE TENHA PONTO, NÃO É — que é a razão de ser da defesa a
// montante.
//
// Este teste NÃO pede que o `scheduler` recuse: pede que fique registado que ele não pode
// aceitar. Se alguém acrescentar normalização aqui, isto fica vermelho e obriga a ler a decisão
// escrita no topo do ficheiro — normalizar colapsaria dois modelos no mesmo bucket de quota.
func TestAOS425ModeloComPontoTornaAChaveInvalida(t *testing.T) {
	for _, modelo := range []string{"gpt-4.1", "claude-3.5-sonnet", "text-embedding-3.5"} {
		k := ProviderKey{Provider: "openai", Model: modelo, Region: "eu-west"}
		nome := bucketStreamPrefix + k.String()
		err := eventstore.ValidarStreamID(nome)
		if err == nil {
			t.Errorf("a chave %q passou a ser valida: se isto mudou por normalizacao, leia a\n"+
				"decisao no topo deste ficheiro — dois modelos distintos partilhariam bucket", nome)
			continue
		}
		if !strings.Contains(err.Error(), modelo) {
			t.Errorf("o erro nao nomeia o modelo %q: %v", modelo, err)
		}
	}
}

// O `run_id` QUE ENTRA NA CHAVE DE SPAWN herda a validação das portas de entrada.
//
// `spawn-admission/<run_id>`: o `run_id` é validado no `POST /runs`, no `POST /plans` e agora
// também no `--run` do `aos-orq`. Este teste fixa que o PREFIXO não estraga um id já válido —
// é a única parte que este pacote controla.
func TestAOS425PrefixoDeSpawnNaoEstragaUmRunIDValido(t *testing.T) {
	for _, run := range []string{"run-425", "run-425~analise+2edados", "aos-internal/x"} {
		if err := eventstore.ValidarStreamID(run); err != nil {
			t.Fatalf("fixture invalida: %q ja nao e um run_id valido: %v", run, err)
		}
		if err := eventstore.ValidarStreamID(spawnAdmissionStreamPrefix + run); err != nil {
			t.Errorf("o prefixo de spawn tornou invalido o run_id valido %q: %v", run, err)
		}
	}
}

// OS NOMES DE INSTÂNCIA INTERNOS — a linha BAIXO da tabela do AOS-425.
//
// `degradation/<name>`, `backpressure/queue/<name>`, `routing/<name>`,
// `scheduling/dispatch/<name>`, `backpressure/policy-audit/<name>`. O `name` é escolhido em
// CÓDIGO por quem compõe (default `"default"`, alterável pelas opções `With*Name`), e não há
// chamador fora de testes.
//
// Decisão escrita: **não se valida nem se normaliza** — valida-se implicitamente, porque o
// `Append` recusa e o valor é escolhido por quem escreve o código que compõe. O que este teste
// acrescenta é o registo de que o default é seguro e de que um nome com ponto NÃO é, para que
// quem passar uma `With*Name` veja a restrição num teste em vez de num incidente.
func TestAOS425NomesDeInstanciaODefaultEeSeguroEOPontoNao(t *testing.T) {
	// AS CONSTANTES DO PACOTE, e não literais reescritos aqui. Com literais, mudar uma
	// constante para um valor com ponto deixava este teste VERDE — que é precisamente a
	// forma de falha que o AOS-424 passou dez gates a exibir.
	//
	// Inclui o `budget-breaker/<treeID>`, que a primeira versão deixou de fora apesar de o
	// comentário do `--run` o nomear como um dos quatro nomes de stream.
	prefixos := []string{
		degradationStreamPrefix, queueStreamPrefix, routingStreamPrefix,
		dispatchStreamPrefix, policyAuditStreamPrefix, breakerStreamPrefix,
	}
	for _, p := range prefixos {
		if err := eventstore.ValidarStreamID(p + "default"); err != nil {
			t.Errorf("o nome de instancia por omissao produz um stream invalido em %q: %v", p, err)
		}
		if eventstore.ValidarStreamID(p+"equipa.pagamentos") == nil {
			t.Errorf("%q + um nome com ponto passou a ser valido; a restricao das opcoes "+
				"With*Name deixou de estar registada", p)
		}
	}
}
