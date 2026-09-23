package allowlist_test

// aos425_nomes_que_viram_stream_test.go — A ALLOWLIST AUTORIZA UM MODELO; NÃO O FORNECE.
//
// # A CORRECÇÃO QUE ESTES TESTES FIXAM
//
// A primeira versão do AOS-425 pôs aqui uma verificação que RECUSAVA uma policy com um nome de
// modelo não representável num `stream_id` (`gpt-4.1`). Uma revisão adversarial mostrou que
// estava errado nas duas pontas:
//
//   - O valor que compõe `admission/bucket/<provider>:<model>:<region>` é `tier.Model`, da
//     escada de `RoutingConfig.Tiers`. **Não vem desta policy.** Validar aqui cumpria a letra de
//     «validar onde o valor entra» e falhava o sentido.
//   - E partia a razão de ser documentada do bundle externo: o `deploy/node/README.md` diz que
//     `AOS_MODEL_ALLOWLIST_BUNDLE_DIR` existe para o nó «pedir nomes de modelo REAIS (fim dos
//     aliases)». Um bundle bem assinado com `gpt-4.1` ficava incarregável, o nó recusava
//     arrancar, e a mensagem dizia «bundle adulterado».
//
// A imposição vive agora em `ErrRoutingModelNaoRepresentavel` (production_routing.go), na
// composição da escada. O que fica aqui é o AVISO no ficheiro da policy — documentação para quem
// cura o catálogo — e os testes que garantem que este carregador NÃO recusa.

import (
	"os"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/substrate/eventstore"
)

// policyComModelo devolve um documento de allowlist com o modelo dado.
func policyComModelo(modelo string) string {
	return `{"version":"aos425/v1","default":"deny","rules":[` +
		`{"id":"r1","board":"b","models":["` + modelo + `"],"regions":["eu-west"]}]}`
}

// UM BUNDLE ASSINADO COM UM NOME DE MODELO REAL CARREGA.
//
// É o teste que faltava à primeira versão e que teria apanhado o defeito: os testes de então
// chamavam o parser interno, e nunca o caminho que o nó usa. `gpt-4.1` é o exemplo do próprio
// ticket e a razão de ser do bundle externo.
func TestAOS425BundleComNomeDeModeloRealCarrega(t *testing.T) {
	priv, pub := testKeys()
	for _, modelo := range []string{"gpt-4.1", "claude-3.5-sonnet", "text-embedding-3.5"} {
		doc := policyComModelo(modelo)
		pol, err := allowlist.LoadSignedPolicy([]byte(doc), sign(t, priv, []byte(doc)), pub)
		if err != nil {
			t.Errorf("um bundle ASSINADO com o modelo real %q devia carregar, veio: %v\n"+
				"o `AOS_MODEL_ALLOWLIST_BUNDLE_DIR` existe precisamente para permitir nomes de\n"+
				"modelo reais (deploy/node/README.md); recusa-lo faz o no nao arrancar", modelo, err)
			continue
		}
		if pol.Evaluate(allowlist.Input{Board: "b", Model: modelo, Region: "eu-west"}) != allowlist.EffectAllow {
			t.Errorf("a policy carregou mas nao autoriza o modelo %q que declara", modelo)
		}
	}
}

// E CONTINUA A ASSINAR-SE. O `Digest` é o material assinado; se ele recusasse o documento,
// `gen_signature.go` não conseguiria produzir a assinatura e não haveria saída nenhuma.
func TestAOS425DigestAceitaNomeDeModeloReal(t *testing.T) {
	if _, err := allowlist.Digest([]byte(policyComModelo("gpt-4.1"))); err != nil {
		t.Fatalf("o Digest recusou um catalogo com um modelo real: %v\n"+
			"sem Digest nao ha assinatura, e o operador fica sem saida nenhuma", err)
	}
}

// A POLICY EM VIGOR CONTINUA A CARREGAR, e os nomes que ela traz são de facto representáveis —
// medido, não assumido. É o que diz que a allowlist EMBEBIDA (a que corre em produção) não
// depende da correcção acima para estar sã.
func TestAOS425PolicyEmVigorContinuaACarregar(t *testing.T) {
	pol, err := allowlist.LoadPolicy()
	if err != nil {
		t.Fatalf("a allowlist embebida deixou de carregar: %v", err)
	}
	for _, r := range pol.Rules {
		for _, m := range r.Models {
			if m == "*" {
				continue
			}
			if err := eventstore.ValidarStreamID(m); err != nil {
				t.Errorf("a policy EMBEBIDA traz o modelo %q, que nao e representavel: %v\n"+
					"ela nao e recusada (a imposicao esta na escada de tiers), mas declara-la numa\n"+
					"escada passaria a abortar o arranque do gateway", m, err)
			}
		}
	}
}

// O AVISO ESTÁ NO FICHEIRO QUE O REVISOR LÊ.
//
// Critério de aceitação do AOS-425. O aviso não é um controlo — não entra no digest assinado — e
// este teste não o trata como tal: só garante que não desaparece em silêncio.
func TestAOS425AvisoDeAcoplamentoPresenteNoFicheiroDaPolicy(t *testing.T) {
	// Lido do DISCO e não do `go:embed`: este teste é do pacote `allowlist_test`, que não vê
	// a variável embebida. É o mesmo ficheiro — e é o ficheiro que um humano abre para rever a
	// política, que é exactamente o que o critério pede.
	cru, err := os.ReadFile("allowlist_policy.json")
	if err != nil {
		t.Fatalf("ler o allowlist_policy.json: %v", err)
	}
	bruto := string(cru)
	if !strings.Contains(bruto, "_aviso_nomes_de_stream") {
		t.Error("o `allowlist_policy.json` perdeu o aviso `_aviso_nomes_de_stream`:\n" +
			"quem cura este catalogo deixa de ver que um modelo com ponto nao podera ser\n" +
			"declarado numa escada de tiers")
	}
	if !strings.Contains(bruto, "gpt-4.1") {
		t.Error("o aviso deixou de dar o exemplo concreto; sem ele nao se percebe que a forma\n" +
			"NORMAL de um id de modelo e a que da problema")
	}
}

// E O AVISO NÃO ENTRA NO MATERIAL ASSINADO — que é a razão pela qual pôde ser acrescentado sem
// re-assinar, e também a razão pela qual não protege nada por si.
func TestAOS425AvisoNaoEntraNoDigest(t *testing.T) {
	semNota := `{"version":"t/v1","default":"deny","rules":[{"id":"r1","board":"b",` +
		`"models":["gpt-4o"],"regions":["eu"]}]}`
	comNota := `{"_aviso_nomes_de_stream":["x"],"version":"t/v1","default":"deny",` +
		`"rules":[{"id":"r1","board":"b","models":["gpt-4o"],"regions":["eu"]}]}`

	a, err := allowlist.Digest([]byte(semNota))
	if err != nil {
		t.Fatalf("Digest sem nota: %v", err)
	}
	b, err := allowlist.Digest([]byte(comNota))
	if err != nil {
		t.Fatalf("Digest com nota: %v", err)
	}
	if a != b {
		t.Errorf("o campo de aviso MUDOU o digest assinado (%s != %s): acrescenta-lo exigiria\n"+
			"re-assinar a policy, e o aviso deixaria de poder ser mantido por quem o le", a, b)
	}
}
