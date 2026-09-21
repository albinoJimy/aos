package main

// aos417_ingresso_test.go — O INGRESSO DO CAMINHO DO PLANO GRAVA UM FACTO, E SÓ UM (AOS-417).
//
// O que estes testes impõem, e que nenhum outro teste do pacote cobria: existe uma superfície
// de rede por onde um objectivo entra no caminho do plano; o que ela produz é um facto DURÁVEL
// (não uma goroutine, não uma promessa); repeti-la não duplica o pedido; e a resposta não
// distingue um pedido novo de um repetido — a garantia de não-oracularidade do ADR-016, que o
// ADR-028 §2.3 herda de propósito.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// lerFilaDePedidos devolve os factos de pedido de plano gravados no nó.
func lerFilaDePedidos(t *testing.T, node *Node) []planRequestPayload {
	t.Helper()
	if node.EventStore == nil {
		t.Fatalf("o no de teste devia ter Event Store — sem ele o ingresso nao tem onde gravar")
	}
	evs, err := node.EventStore.Read(context.Background(), planRequestStream, 1)
	// FILA VAZIA E FILA INEXISTENTE SÃO O MESMO. O stream só passa a existir no primeiro
	// append, pelo que «nunca ninguém pediu nada» chega aqui como erro e não como lista vazia.
	// Distinguir os dois faria o teste da recusa falhar por uma razão que não é a que ele mede.
	if errors.Is(err, eventstore.ErrStreamNotFound) {
		return nil
	}
	if err != nil {
		t.Fatalf("Read(%s): %v", planRequestStream, err)
	}
	var out []planRequestPayload
	for _, ev := range evs {
		if ev.Type != EventTypePlanRequestSubmitted {
			t.Fatalf("facto inesperado no stream da fila: %q", ev.Type)
		}
		var p planRequestPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("payload do pedido nao descodifica: %v", err)
		}
		out = append(out, p)
	}
	return out
}

// Um pedido válido responde 201 E DEIXA O FACTO. A segunda metade é o que torna o teste
// não-vacuoso: um handler que respondesse 201 sem gravar nada passaria na primeira e falharia
// aqui, e é exactamente esse o modo de falha que o ticket existe para fechar — prometer uma
// corrida que ninguém vai consumir.
func TestAOS417PedidoValidoGravaOFacto(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	rec := postJSON(h, "POST", "/plans", map[string]any{
		"run_id":    "plan-req-1",
		"objective": "decompor o trabalho de referencia",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /plans valido devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
	var resp planRequestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resposta 201 nao descodifica: %v", err)
	}
	if resp.RunID != "plan-req-1" || resp.Status != "accepted" {
		t.Fatalf("resposta devia trazer o run e o estado, veio %+v", resp)
	}

	fila := lerFilaDePedidos(t, node)
	if len(fila) != 1 {
		t.Fatalf("devia haver 1 pedido na fila, ha %d", len(fila))
	}
	if fila[0].RunID != "plan-req-1" {
		t.Fatalf("o facto devia amarrar o run pedido, veio %q", fila[0].RunID)
	}
	// O OBJECTIVO tem de sobreviver: é a única coisa que o consumidor não consegue
	// reconstruir de mais lado nenhum. Um facto que só guardasse o run_id seria um pedido
	// sem pedido.
	if fila[0].Objective != "decompor o trabalho de referencia" {
		t.Fatalf("o facto devia guardar o objectivo, veio %q", fila[0].Objective)
	}
}

// Fail-closed na fronteira: sem run_id e sem objectivo não há pedido que se possa consumir.
func TestAOS417PedidoIncompletoERecusado(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	casos := []struct {
		nome  string
		corpo map[string]any
	}{
		{"sem run_id", map[string]any{"objective": "sem run"}},
		{"sem objective", map[string]any{"run_id": "plan-req-vazio"}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			rec := postJSON(h, "POST", "/plans", c.corpo)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("POST /plans %s devia dar 400, veio %d", c.nome, rec.Code)
			}
		})
	}

	// E a recusa é SILENCIOSA quanto a efeitos: nada foi para a fila.
	if fila := lerFilaDePedidos(t, node); len(fila) != 0 {
		t.Fatalf("um pedido recusado nao devia deixar facto, ha %d", len(fila))
	}
}

// IDEMPOTÊNCIA COM EFEITO. Repetir o pedido responde o MESMO 201 — e deixa UM SÓ facto.
//
// As duas metades têm de ser asseridas juntas, porque cada uma sozinha passa com um defeito
// diferente: verificar só o código deixa passar um handler que grava duas vezes (o consumidor
// correria o plano a dobrar); verificar só a contagem deixa passar um handler que devolve 409
// na segunda — o oráculo de existência que o ADR-016 fecha.
func TestAOS417PedidoRepetidoNaoDuplicaNemRevela(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	corpo := map[string]any{"run_id": "plan-req-dup", "objective": "o mesmo objectivo"}

	primeira := postJSON(h, "POST", "/plans", corpo)
	segunda := postJSON(h, "POST", "/plans", corpo)

	if primeira.Code != http.StatusCreated || segunda.Code != http.StatusCreated {
		t.Fatalf("ambas as submissoes deviam dar 201, vieram %d e %d", primeira.Code, segunda.Code)
	}
	// NÃO-ENUMERÁVEL: a resposta é indistinguível byte-a-byte. Um `409` acidental aqui é uma
	// regressão de SEGURANÇA, não de comportamento — revela a um chamador a existência de um
	// pedido que pode não ser dele.
	if primeira.Body.String() != segunda.Body.String() {
		t.Fatalf("a repeticao devia ser indistinguivel da primeira; veio %q vs %q",
			primeira.Body.String(), segunda.Body.String())
	}

	fila := lerFilaDePedidos(t, node)
	if len(fila) != 1 {
		t.Fatalf("a repeticao nao devia duplicar o pedido na fila, ha %d", len(fila))
	}
}

// Dois runs diferentes são dois pedidos. É a contraprova do teste acima: a deduplicação é por
// run, e não um tecto que engolisse tudo depois do primeiro.
func TestAOS417RunsDiferentesSaoPedidosDiferentes(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	for _, id := range []string{"plan-req-a", "plan-req-b"} {
		rec := postJSON(h, "POST", "/plans", map[string]any{"run_id": id, "objective": "trabalho " + id})
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST /plans %s devia dar 201, veio %d", id, rec.Code)
		}
	}
	fila := lerFilaDePedidos(t, node)
	if len(fila) != 2 {
		t.Fatalf("dois runs deviam dar dois pedidos, ha %d", len(fila))
	}
	if fila[0].RunID == fila[1].RunID {
		t.Fatalf("os dois pedidos deviam ser de runs distintos, vieram ambos %q", fila[0].RunID)
	}
}

// O INGRESSO NÃO HOSPEDA O RUN. É a fronteira do ADR-018 vista do lado do comportamento: o nó
// grava o pedido e não toca no ciclo de vida. Um handler que — por zelo — submetesse o run
// localmente passaria em todos os testes acima e quebraria esta, que é a que importa: o plano
// é corrido pelo `aos-orq`, sob a posse que ele já governa, e não por esta rota.
func TestAOS417IngressoNaoHospedaORun(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	svc, h := newAPI(t, node)

	rec := postJSON(h, "POST", "/plans", map[string]any{
		"run_id":    "plan-req-nao-hospeda",
		"objective": "isto nao corre aqui",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /plans devia dar 201, veio %d", rec.Code)
	}
	if n := svc.InProgressCount(); n != 0 {
		t.Fatalf("o ingresso nao devia hospedar run nenhum, ha %d em curso", n)
	}
	// E o GET do run continua a não o conhecer: pedir um plano não cria um run.
	grec := postJSON(h, "GET", "/runs/plan-req-nao-hospeda", nil)
	if grec.Code == http.StatusOK {
		t.Fatalf("um pedido de plano nao devia tornar o run legivel, veio 200 (%s)", grec.Body.String())
	}
}

// A ADMISSAO DECLARADA TEM DE EXISTIR. O codigo, a tabela de rotas e o banner afirmam os tres
// que esta rota "atravessa a MESMA admissao" do POST /runs. Sem este teste, a afirmacao nao
// tinha sensor nenhum: uma revisao adversarial removeu os dois `if` de admissao e a suite
// COMPLETA do pacote ficou verde.
//
// Nota sobre o que NAO se testa aqui, porque seria testar uma guarda inexistente: o tecto de
// runs em curso (`maxInFlight`) nao se aplica a esta rota e foi deliberadamente NAO copiado do
// `handleSubmit` — conta runs hospedados, e esta rota nao hospeda nenhum. Ver o comentario de
// admissao em plan_ingress.go.
func TestAOS417IngressoPassaPelaAdmissao(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	// Balde com burst 1 e reposicao 0: o segundo pedido no mesmo instante NAO e admitido.
	_, h := newAPI(t, node, WithRateLimit(0, 1))

	primeiro := postJSON(h, "POST", "/plans", map[string]any{"run_id": "plan-adm-1", "objective": "passa"})
	if primeiro.Code != http.StatusCreated {
		t.Fatalf("o primeiro pedido devia ser admitido (201), veio %d", primeiro.Code)
	}
	segundo := postJSON(h, "POST", "/plans", map[string]any{"run_id": "plan-adm-2", "objective": "nao passa"})
	if segundo.Code != http.StatusTooManyRequests {
		t.Fatalf("o segundo pedido devia ser recusado pelo balde (429), veio %d (%s)",
			segundo.Code, segundo.Body.String())
	}

	// E a recusa e ANTES do efeito: o pedido recusado nao deixa facto.
	fila := lerFilaDePedidos(t, node)
	if len(fila) != 1 {
		t.Fatalf("so o pedido admitido devia ter deixado facto, ha %d", len(fila))
	}
}

// UM OBJECTIVO NAO E UM FICHEIRO. O tecto de corpo admite ~1 MiB de texto livre e untrusted, que
// iria em claro para um log append-only que vai ao WAL e aos backups — numa fila que ainda nao
// tem quem a consuma nem retencao que a encolha.
func TestAOS417ObjectivoTemTecto(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	rec := postJSON(h, "POST", "/plans", map[string]any{
		"run_id":    "plan-gordo",
		"objective": strings.Repeat("a", maxObjetivoBytes+1),
	})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("objectivo acima do tecto devia dar 413, veio %d", rec.Code)
	}
	if fila := lerFilaDePedidos(t, node); len(fila) != 0 {
		t.Fatalf("um pedido recusado por tamanho nao devia deixar facto, ha %d", len(fila))
	}
}

// O CRITERIO DO TICKET E SOBRE UM RUN COM POSSE, e nao sobre um pedido repetido.
//
// Sao coisas diferentes e a primeira versao dos testes so cobria a segunda: a deduplicacao da
// fila e por "pedido-deste-run", nao por posse, pelo que o desfecho certo saia por acidente de
// nomes. Aqui o run existe MESMO, esta em curso e tem lease — e o pedido de plano continua a
// responder 201 sem revelar nada, que e o que o ADR-028 2.3 fixa.
func TestAOS417PedidoParaRunComPosse(t *testing.T) {
	model := &aos277BlockingModel{entered: make(chan struct{}, 4), release: make(chan struct{})}
	node, _ := newAPINode(t, model, false)
	defer func() { _ = node.Close() }()
	defer model.releaseAll()
	svc, h := newAPI(t, node)

	const runID = "run-417-com-posse"
	if rec := postJSON(h, "POST", "/runs", map[string]any{
		"run_id":        runID,
		"objective":     "trabalho bloqueado",
		"principal_nhi": "nhi:" + runID,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
	// O run esta MESMO a correr, e nao so hospedado: sem isto a posse nao seria significativa.
	select {
	case <-model.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("o run nunca chegou ao modelo — nao ha posse que exercitar")
	}
	if n := svc.InProgressCount(); n != 1 {
		t.Fatalf("o run devia estar em curso, in-flight=%d", n)
	}

	// O PEDIDO DE PLANO para esse run responde 201 e nao revela que o run existe.
	comPosse := postJSON(h, "POST", "/plans", map[string]any{
		"run_id": runID, "objective": "plano para um run que ja corre",
	})
	if comPosse.Code != http.StatusCreated {
		t.Fatalf("POST /plans para um run COM POSSE devia dar 201 (nao-oraculo), veio %d (%s)",
			comPosse.Code, comPosse.Body.String())
	}
	inedito := postJSON(h, "POST", "/plans", map[string]any{
		"run_id": "run-417-inexistente", "objective": "plano para um run que nao existe",
	})
	if inedito.Code != comPosse.Code {
		t.Fatalf("o status distingue um run COM POSSE (%d) de um INEXISTENTE (%d) — e um oraculo "+
			"de existencia", comPosse.Code, inedito.Code)
	}
	// A FORMA da resposta tem de coincidir; o conteudo difere so no run_id que o chamador ja sabia.
	var a, b planRequestResponse
	if err := json.Unmarshal(comPosse.Body.Bytes(), &a); err != nil {
		t.Fatalf("resposta nao descodifica: %v", err)
	}
	if err := json.Unmarshal(inedito.Body.Bytes(), &b); err != nil {
		t.Fatalf("resposta nao descodifica: %v", err)
	}
	if a.Status != b.Status {
		t.Fatalf("o estado devolvido distingue posse (%q) de inexistencia (%q)", a.Status, b.Status)
	}
}

// O FACTO E UM CONTRATO, e o seu unico consumidor previsto vive noutro modulo e nao o pode
// importar (esta em `package main`). Vai reescrever a struct a mao, e nenhum gate liga as duas
// copias — pelo que a forma tem de ser fixada AQUI, por teste, e nao por convencao.
//
// A asserção e sobre as CHAVES JSON e a versao, e nao sobre os nomes dos campos Go: e a forma
// serializada que atravessa a fronteira.
func TestAOS417FormaDoFactoEEstavel(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	_, h := newAPI(t, node)

	rec := postReq(h, "/plans", planRequest{RunID: "run-417-forma", Objective: "objectivo"}, euReaderHeaders())
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /plans devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}

	evs, err := node.EventStore.Read(context.Background(), planRequestStream, 1)
	if err != nil || len(evs) != 1 {
		t.Fatalf("devia haver 1 facto: err=%v n=%d", err, len(evs))
	}
	var bruto map[string]any
	if err := json.Unmarshal(evs[0].Payload, &bruto); err != nil {
		t.Fatalf("payload nao descodifica: %v", err)
	}

	// As chaves que o consumidor PODE contar que existem. Acrescentar uma e compativel; tirar
	// ou renomear uma quebra quem la esta do outro lado sem que nada avise.
	for _, chave := range []string{"v", "run_id", "objective", "principal", "board", "region"} {
		if _, ok := bruto[chave]; !ok {
			t.Errorf("o facto perdeu a chave %q: um consumidor noutro modulo deixa de a ler, e "+
				"nenhum gate liga as duas copias da struct", chave)
		}
	}
	if bruto["v"] != planRequestVersao {
		t.Errorf("a versao do payload devia ser %q, veio %v", planRequestVersao, bruto["v"])
	}
	// O ENVELOPE tem a sua propria versao, e NAO e a mesma coisa: versiona a forma do envelope,
	// nao a do corpo. Fica asserido para que a distincao nao se perca.
	if evs[0].SchemaVersion == "" {
		t.Error("o envelope devia trazer schema_version preenchido pelo store")
	}
}
