package main

// aos424_run_id_test.go — O `run_id` É O NOME DE UM STREAM, E NEM TODO O TEXTO O PODE SER.
//
// Até ao AOS-424 as duas rotas de submissão validavam o `run_id` contra duas coisas: não ser
// vazio e não invadir o prefixo reservado. Tudo o resto passava — e o `run_id` de um run É o seu
// stream no Event Store, pelo que um cliente escolhia livremente o nome de um stream.
//
// O efeito medido: um `run_id` com ponto é aceite sobre WAL e recusado com `E_CONFIG` sobre
// JetStream. Funciona em desenvolvimento e parte na única topologia que arbitra entre processos
// (DEF-282) — que é a que o caminho do plano precisa para ter consumidor.

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// UM `run_id` QUE NÃO PODE SER UM STREAM É RECUSADO NO `POST /plans`.
//
// A recusa é `400` e não `503`: é o pedido que está mal, não o nó que está em baixo.
//
// SÓ NESTA ROTA, e o teste abaixo fixa a assimetria em vez de a deixar por explicar — ver
// [TestAOS424PostRunsAindaNaoValidaEPorque].
func TestAOS424RunIDInvalidoERecusadoNoIngressoDoPlano(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	maus := []struct{ nome, id string }{
		{"ponto", "cliente.pedido-1"},
		{"espaco", "run com espaco"},
		{"curinga asterisco", "run-*"},
		{"curinga maior", "run->tudo"},
		{"tab", "run\tcom-tab"},
		{"nova linha", "run\ncom-linha"},
	}
	for _, m := range maus {
		t.Run(m.nome, func(t *testing.T) {
			plans := postJSON(h, "POST", "/plans", map[string]any{
				"run_id": m.id, "objective": "objectivo",
			})
			if plans.Code != http.StatusBadRequest {
				t.Errorf("POST /plans com run_id %q devia dar 400, veio %d (%s)",
					m.id, plans.Code, plans.Body.String())
			}
		})
	}
}

// CONTROLO DE NÃO-VACUIDADE. Sem isto, uma guarda que recusasse tudo passaria no teste acima.
//
// Inclui de propósito os caracteres que um `run_id` legítimo USA e que o subject NATS aceita —
// o hífen, o sublinhado e a barra — para que apertar a regra por engano fique vermelho.
func TestAOS424RunIDLegitimoContinuaAceite(t *testing.T) {
	bons := []string{"run-424-simples", "run_com_sublinhado", "run-424/sub", "RUN424", "r1"}
	for _, id := range bons {
		if runIDInvalido(id) {
			t.Errorf("o run_id legitimo %q foi recusado — a regra apertou demais", id)
		}
	}

	// E a via HTTP, ponta a ponta, para o caso mais simples.
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)
	rec := postJSON(h, "POST", "/runs", map[string]any{
		"run_id": "run-424-simples", "principal_nhi": "nhi:run-424-simples",
		"credential": credencialDeTeste(t, node),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("um run_id legitimo devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
}

// A REGRA NÃO PODE VOLTAR A SER DUPLICADA.
//
// # O QUE ESTE TESTE MEDE, E PORQUE MUDOU
//
// A primeira versão comparava a cópia do nó com a fonte, para que não derivassem. Entretanto a
// cópia desapareceu: a regra vive em [eventstore.ValidarStreamID] e o nó chama-a. O que passou
// a importar não é que duas cópias coincidam — é que **não volte a haver duas**.
//
// O AOS-424 mediu duas vezes o que a duplicação custa: a subtileza do `ErrConfig` deixou um
// defeito CRÍTICO voltar a meio do mesmo ticket, e a regra dos caracteres esteve a um
// `ContainsAny` acrescentado de fazer o gate medir só o ponto, em verde.
func TestAOS424RegraDoStreamIDNaoEDuplicadaNoNo(t *testing.T) {
	entradas, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ler o pacote: %v", err)
	}
	// Os caracteres proibidos, escritos aqui SÓ para os procurar — não para os impor.
	const assinatura = `". *>`
	var reincidentes []string
	for _, e := range entradas {
		nome := e.Name()
		if e.IsDir() || !strings.HasSuffix(nome, ".go") || nome == "aos424_run_id_test.go" {
			continue
		}
		bruto, rerr := os.ReadFile(nome)
		if rerr != nil {
			t.Fatalf("ler %s: %v", nome, rerr)
		}
		if strings.Contains(string(bruto), assinatura) {
			reincidentes = append(reincidentes, nome)
		}
	}
	if len(reincidentes) > 0 {
		t.Errorf("a lista de caracteres proibidos voltou a ser escrita no no (%v):\n"+
			"a regra vive em `eventstore.ValidarStreamID` e chama-se de la. Uma copia deriva em\n"+
			"silencio — foi assim que o AOS-424 deixou um defeito CRITICO voltar a meio do proprio\n"+
			"ticket. Se precisar da regra, chame-a; se precisar de a MUDAR, mude-a na fonte.",
			reincidentes)
	}
}

// O NÓ USA A FONTE, e não uma aproximação dela.
//
// Controlo de não-vacuidade do teste acima: sem isto, apagar o `runIDInvalido` inteiro também
// deixaria de haver duplicação — e de haver validação.
func TestAOS424RunIDInvalidoUsaAFonte(t *testing.T) {
	for _, mau := range []string{"a.b", "a b", "a*b", "a>b", "a\tb"} {
		if !runIDInvalido(mau) {
			t.Errorf("o run_id %q devia ser recusado: e o que `eventstore.ValidarStreamID` diz", mau)
		}
		if eventstore.ValidarStreamID(mau) == nil {
			t.Errorf("a fonte deixou de recusar %q — e a fonte que manda", mau)
		}
	}
	for _, bom := range []string{"run-1", "run_2", "aos-internal/x", "RUN3"} {
		if runIDInvalido(bom) {
			t.Errorf("o run_id legitimo %q foi recusado", bom)
		}
	}
}

// O `POST /runs` VALIDA, E A RAZÃO QUE O IMPEDIA ESTÁ FECHADA PELO ESCAPE.
//
// # HISTÓRIA, porque este teste já afirmou o contrário
//
// Esta rota esteve deliberadamente SEM validação, e o teste que aqui estava exigia que assim
// continuasse enquanto o `plan.ValidNodeID` admitisse `.` e `:`: o `childRunID` compõe
// `<run>~<node_id>` e submete-o POR AQUI, pelo que validar partia os planos cujos nós usassem
// esses caracteres.
//
// O ADR-029 §3 deixou DUAS saídas: apertar o `ValidNodeID` (com prompt novo e revalidação com o
// modelo vivo) ou ESCAPAR o `node_id` no `childRunID`. Escolheu-se a segunda — e o teste
// anterior **só antecipava a primeira**: lia o charset do `ValidNodeID` e, como ele continua a
// admitir pontos, teria mantido esta rota permissiva para sempre, muito depois de a razão ter
// desaparecido.
//
// É uma lição sobre guardas que citam uma causa: ela pode deixar de valer por um caminho que o
// guarda não conhece. Este mede a PROPRIEDADE — «o id do run filho é sempre válido» — e não o
// mecanismo que a produz.
func TestAOS424PostRunsValidaEOPlanoContinuaAPassar(t *testing.T) {
	no, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = no.Close() }()
	_, h := newAPI(t, no)

	// (a) O que a guarda passa a recusar: um `run_id` de cliente que não pode ser um stream.
	mau := postJSON(h, "POST", "/runs", map[string]any{
		"run_id": "cliente.pedido-1", "principal_nhi": "nhi:x",
		"credential": credencialDeTeste(t, no),
	})
	if mau.Code != http.StatusBadRequest {
		t.Errorf("o `POST /runs` devia recusar um run_id com ponto (veio %d): o run_id E o nome "+
			"de um stream, e o escape do childRunID fechou a razao que impedia esta guarda",
			mau.Code)
	}

	// (b) E o que ela NÃO pode recusar: o id que o executor de nós submete para um nó de plano
	// com ponto — que agora chega ESCAPADO. É esta metade que prova que o caminho do plano
	// continua a passar.
	//
	// O valor é o que o `childRunID` produz hoje para `run-424` + `analise.dados`. Está escrito
	// à mão de propósito: o nó NÃO importa o `aos-orq` (são binários distintos), e um teste que
	// derivasse o valor da mesma função que o produz não provaria que as duas pontas concordam.
	// Se o escape mudar de forma, este teste fica vermelho — e é isso que se quer.
	const filhoEscapado = "run-424~analise+2edados"
	bom := postJSON(h, "POST", "/runs", map[string]any{
		"run_id": filhoEscapado, "principal_nhi": "nhi:x",
		"credential": credencialDeTeste(t, no),
	})
	if bom.Code != http.StatusCreated {
		t.Fatalf("o `POST /runs` tem de aceitar %q (veio %d): e o id que o childRunID produz para "+
			"um no de plano chamado `analise.dados`. Se este id deixou de ser aceite, o caminho "+
			"do plano parte \u2014 verifique se o escape mudou de forma.", filhoEscapado, bom.Code)
	}
}
