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
	"regexp"
	"strings"
	"testing"
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
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("um run_id legitimo devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
}

// A REGRA DUPLICADA NÃO PODE DERIVAR DA FONTE.
//
// O nó não pode importar o backend JetStream para lhe perguntar a regra — um `import` só para
// isto arrastaria o cliente NATS para o caminho de ingresso —, pelo que
// [caracteresNaoRepresentaveis] é uma CÓPIA. Uma cópia sem detector apodrece; este teste é o
// detector, e lê o original em vez de o repetir.
func TestAOS424RegraDuplicadaCoincideComAFonte(t *testing.T) {
	const fonte = "../../substrate/eventstore/jetstream/store.go"
	bruto, err := os.ReadFile(fonte)
	if err != nil {
		t.Fatalf("ler %s: %v — sem a fonte este teste nao tem o que comparar", fonte, err)
	}
	m := regexp.MustCompile(`ContainsAny\(streamID, "([^"]*)"\)`).FindSubmatch(bruto)
	if m == nil {
		t.Fatalf("nao encontrei a regra em %s: a guarda do subject NATS mudou de forma.\n"+
			"Actualize este teste para ler a regra nova — NAO o relaxe.", fonte)
	}
	daFonte := strings.NewReplacer(`\t`, "\t", `\r`, "\r", `\n`, "\n").Replace(string(m[1]))

	// CONTROLO: a regra lida tem de apanhar um valor conhecidamente mau. Se a extracção
	// partir, o teste falha em vez de comparar duas coisas vazias.
	if !strings.ContainsAny("a.b", daFonte) {
		t.Fatalf("a regra lida (%q) nao apanha um valor com ponto — a extraccao esta errada", daFonte)
	}

	if ordenar(daFonte) != ordenar(caracteresNaoRepresentaveis) {
		t.Errorf("a regra do no (%q) DIVERGIU da fonte (%q):\n"+
			"um run_id que o no aceite e o subject NATS recuse volta a ser aceite sobre WAL e a "+
			"partir sobre JetStream — que e exactamente o defeito que o AOS-424 fechou.",
			caracteresNaoRepresentaveis, daFonte)
	}
}

// ordenar normaliza um conjunto de caracteres para comparação independente da ordem.
func ordenar(s string) string {
	r := []rune(s)
	for i := 1; i < len(r); i++ {
		for j := i; j > 0 && r[j] < r[j-1]; j-- {
			r[j], r[j-1] = r[j-1], r[j]
		}
	}
	return string(r)
}

// A ASSIMETRIA FICA FIXADA POR TESTE, E O TESTE LÊ A CAUSA — NÃO A CONSEQUÊNCIA.
//
// O `POST /runs` ainda aceita um `run_id` com ponto, e tem de continuar a aceitar enquanto o
// `plan.ValidNodeID` admitir `.` e `:` num `node_id`: o `childRunID` compõe `<run>~<node_id>` e
// submete-o por esta rota, pelo que apertar aqui partiria os planos cujos nós usem esses
// caracteres — em produção, que corre sobre WAL, onde esses ids funcionam.
//
// # PORQUE É QUE ESTE TESTE LÊ UM FICHEIRO DE OUTRO MÓDULO
//
// A primeira versão afirmava, no ticket, que «fica vermelho no dia em que a causa
// desaparecer». **Era falso, e uma revisão adversarial provou-o:** apertou o `ValidNodeID` e
// este teste ficou VERDE. Reagia à CONSEQUÊNCIA (alguem ligar a guarda), não à CAUSA (o
// charset do `node_id`). Quem apertasse o `ValidNodeID` veria vermelho apenas noutro módulo,
// com uma mensagem que não fala de `run_id` nem desta rota — ajustava aquele caso de teste,
// seguia, e a rota ficava sem guarda para sempre.
//
// Agora o teste LÊ o charset da fonte. Não se importa `control-plane/orchestrator/plan`: o
// `layer-lint` (ADR-018/ADR-019) proíbe o nó de o importar, e a proibição é o que mantém a
// fronteira honesta. Ler o ficheiro é a mesma técnica de
// [TestAOS424RegraDuplicadaCoincideComAFonte] e do guard do AOS-417 — e é o que torna o
// acoplamento VISÍVEL em vez de documentado.
func TestAOS424PostRunsAindaNaoValidaEPorque(t *testing.T) {
	const fonte = "../../control-plane/orchestrator/plan/plandocument.go"
	bruto, err := os.ReadFile(fonte)
	if err != nil {
		t.Fatalf("ler %s: %v \u2014 sem a causa este teste nao sabe o que afirmar", fonte, err)
	}
	corpo := string(bruto)
	j := strings.Index(corpo, "func ValidNodeID(")
	if j < 0 {
		t.Fatalf("nao encontrei `func ValidNodeID(` em %s: a grammar do node_id mudou de forma.\n"+
			"Actualize este teste \u2014 NAO o relaxe: e ele que amarra a decisao de nao validar o "+
			"run_id no POST /runs \u00e0 razao que a justifica.", fonte)
	}
	// O charset admitido, tal como a função o escreve: `case c == '_' || c == '-' || ...`.
	grammar := corpo[j:]
	if k := strings.Index(grammar, "\n}"); k > 0 {
		grammar = grammar[:k]
	}
	admitePonto := strings.Contains(grammar, "c == '.'")
	admiteDoisPontos := strings.Contains(grammar, "c == ':'")

	// CONTROLO DE NÃO-VACUIDADE: a extracção tem de ver o charset. Se não vir NADA do que
	// espera, falha em vez de concluir «já não admite» por não ter conseguido ler.
	if !strings.Contains(grammar, "c == '_'") && !strings.Contains(grammar, "c == '-'") {
		t.Fatalf("a extraccao do charset de ValidNodeID falhou (nem `_` nem `-` encontrados):\n" +
			"este teste nao pode decidir nada. Corrija a extraccao.")
	}

	no, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = no.Close() }()
	_, h := newAPI(t, no)

	// O id que um nó de plano chamado `analise.dados` produziria.
	const filho = "run-424~analise.dados"
	rec := postJSON(h, "POST", "/runs", map[string]any{
		"run_id": filho, "principal_nhi": "nhi:x",
	})

	if admitePonto || admiteDoisPontos {
		// A CAUSA AINDA EXISTE: a rota tem de continuar permissiva.
		if rec.Code != http.StatusCreated {
			t.Fatalf("o `POST /runs` tem de continuar a aceitar %q (veio %d): o ValidNodeID ainda "+
				"admite %s, e o childRunID compoe `<run>~<node_id>` para esta rota. Ligar a guarda "+
				"agora parte os planos cujos nos usem esses caracteres.",
				filho, rec.Code, charsAdmitidos(admitePonto, admiteDoisPontos))
		}
		return
	}

	// A CAUSA DESAPARECEU: o `ValidNodeID` já não admite `.` nem `:`, logo nenhum `node_id`
	// legitimo produz um `run_id` irrepresentável — e a guarda TEM de passar a valer aqui.
	if rec.Code == http.StatusCreated {
		t.Fatalf("o `ValidNodeID` deixou de admitir `.` e `:`, mas o `POST /runs` continua a "+
			"aceitar %q.\n"+
			"A razao que bloqueava a guarda DESAPARECEU: ligue `runIDInvalido(req.RunID)` ao "+
			"`handleSubmit` (api.go), como o `POST /plans` ja faz, e actualize este teste.\n"+
			"Eixo: AOS-424.", filho)
	}
}

// charsAdmitidos formata os caracteres que a grammar do node_id ainda admite, para a mensagem
// de falha nomear o que bloqueia.
func charsAdmitidos(ponto, doisPontos bool) string {
	switch {
	case ponto && doisPontos:
		return "`.` e `:`"
	case ponto:
		return "`.`"
	default:
		return "`:`"
	}
}
