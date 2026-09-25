package main

// aos423_reclamacao_test.go — A FILA GANHA QUEM A DRENE, E O `201` DEIXA DE MENTIR.
//
// O AOS-417 abriu o `POST /plans` e não pôs ninguém do outro lado. Estes testes impõem a outra
// metade (ADR-030): um pedido pode ser RECLAMADO uma só vez, o desfecho decide se volta à fila, e
// nada disto enumera a fila nem a serve a quem não pode agir sobre ela.

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------------------------
// A PROJECÇÃO — onde vive toda a lógica de elegibilidade, e por isso testada sozinha.
// ---------------------------------------------------------------------------------------------

func evSubmetido(seq uint64, runID, regiao string) eventstore.Event {
	p, _ := json.Marshal(planRequestPayload{Versao: "1.0", RunID: runID, Objective: "obj " + runID, Region: regiao})
	return eventstore.Event{Seq: seq, Type: EventTypePlanRequestSubmitted, StepID: prefixoPedido + runID, Payload: p}
}

func evReclamado(seq uint64, runID string, ger int, quando time.Time) eventstore.Event {
	return eventstore.Event{
		Seq: seq, Type: EventTypePlanRequestClaimed,
		StepID: prefixoReclamo + strconv.Itoa(ger) + "-" + runID,
		Ts:     quando.Format(time.RFC3339Nano),
	}
}

func evDesfecho(seq uint64, runID string, ger int, classe string) eventstore.Event {
	p, _ := json.Marshal(desfechoPayload{Versao: "1.0", RunID: runID, Classe: classe})
	return eventstore.Event{
		Seq: seq, Type: EventTypePlanRequestOutcome,
		StepID: prefixoDesfecho + strconv.Itoa(ger) + "-" + runID, Payload: p,
	}
}

func idsDe(ps []pedidoNaFila) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.RunID)
	}
	return out
}

func TestAOS423ProjeccaoDaFila(t *testing.T) {
	agora := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	recente := agora.Add(-time.Minute)
	velho := agora.Add(-2 * ttlDaReclamacao)

	casos := []struct {
		nome      string
		eventos   []eventstore.Event
		elegiveis []string
		porque    string
	}{
		{
			nome:      "um pedido por reclamar",
			eventos:   []eventstore.Event{evSubmetido(1, "a", "eu")},
			elegiveis: []string{"a"},
			porque:    "o caso base: submetido e sem mais nada",
		},
		{
			nome:      "reclamacao VIVA segura o pedido",
			eventos:   []eventstore.Event{evSubmetido(1, "a", "eu"), evReclamado(2, "a", 1, recente)},
			elegiveis: nil,
			porque:    "alguem esta a trabalhar nele; entregar duas vezes da duas corridas",
		},
		{
			nome:      "reclamacao EXPIRADA devolve o pedido",
			eventos:   []eventstore.Event{evSubmetido(1, "a", "eu"), evReclamado(2, "a", 1, velho)},
			elegiveis: []string{"a"},
			porque:    "o consumidor morreu entre reclamar e reportar; sem isto o pedido perdia-se",
		},
		{
			nome: "desfecho TRANSITORIO devolve JA, sem esperar o TTL",
			eventos: []eventstore.Event{
				evSubmetido(1, "a", "eu"), evReclamado(2, "a", 1, recente),
				evDesfecho(3, "a", 1, DesfechoTransitorio),
			},
			elegiveis: []string{"a"},
			porque:    "lease detido nao e razao para meia hora de silencio",
		},
		{
			nome: "desfecho TERMINAL nao volta",
			eventos: []eventstore.Event{
				evSubmetido(1, "a", "eu"), evReclamado(2, "a", 1, recente),
				evDesfecho(3, "a", 1, DesfechoTerminal),
			},
			elegiveis: nil,
			porque:    "um plano recusado retentado para sempre e um laco",
		},
		{
			// AOS-442: estaciona em vez de fechar. A re-oferta DEPOIS do intervalo está em
			// aos442_reverificacao_test.go; aqui fica a metade que o AOS-423 já exigia.
			nome: "AGUARDA_HUMANO nao volta ANTES do intervalo de re-oferta, e nao falha",
			eventos: []eventstore.Event{
				evSubmetido(1, "a", "eu"), evReclamado(2, "a", 1, recente),
				evDesfechoEm(3, "a", 1, DesfechoAguardaHumano, recente),
			},
			elegiveis: nil,
			porque:    "ninguem decidiu ainda; retentar seria pedir de novo o que espera um humano",
		},
		{
			nome:      "reclamacao ORFA nao inventa um pedido",
			eventos:   []eventstore.Event{evReclamado(1, "fantasma", 1, recente)},
			elegiveis: nil,
			porque:    "nao se serve o que nunca foi submetido",
		},
		{
			nome: "ORDEM DE CHEGADA",
			eventos: []eventstore.Event{
				evSubmetido(3, "c", "eu"), evSubmetido(1, "a", "eu"), evSubmetido(2, "b", "eu"),
			},
			elegiveis: []string{"a", "b", "c"},
			porque:    "sem ordem, um pedido azarado fica para tras indefinidamente",
		},
		{
			nome: "payload ILEGIVEL nao entra na fila",
			eventos: []eventstore.Event{{
				Seq: 1, Type: EventTypePlanRequestSubmitted,
				StepID: prefixoPedido + "mau", Payload: json.RawMessage(`{isto nao e json`),
			}},
			elegiveis: nil,
			porque:    "um consumidor nao pode honrar o que nao sabe ler, e a regiao perde-se em silencio",
		},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got := idsDe(projectarFila(c.eventos, agora))
			if len(got) != len(c.elegiveis) {
				t.Fatalf("elegiveis = %v, esperava %v — %s", got, c.elegiveis, c.porque)
			}
			for i := range got {
				if got[i] != c.elegiveis[i] {
					t.Fatalf("elegiveis = %v, esperava %v — %s", got, c.elegiveis, c.porque)
				}
			}
		})
	}
}

// A GERAÇÃO AVANÇA, e é ela que amarra o desfecho à tentativa.
//
// Sem geração, um desfecho reportado tarde fecharia uma tentativa que já não é a corrente — e um
// pedido que voltou à fila por expiração ficaria terminado por um relatório de uma corrida
// anterior.
func TestAOS423GeracaoAvanca(t *testing.T) {
	agora := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	velho := agora.Add(-2 * ttlDaReclamacao)

	fila := projectarFila([]eventstore.Event{evSubmetido(1, "a", "eu")}, agora)
	if len(fila) != 1 || fila[0].Geracao != 1 {
		t.Fatalf("um pedido novo devia sair na geracao 1, veio %+v", fila)
	}
	fila = projectarFila([]eventstore.Event{
		evSubmetido(1, "a", "eu"), evReclamado(2, "a", 1, velho),
	}, agora)
	if len(fila) != 1 || fila[0].Geracao != 2 {
		t.Fatalf("apos uma reclamacao expirada devia sair na geracao 2, veio %+v", fila)
	}
}

// ---------------------------------------------------------------------------------------------
// A ROTA
// ---------------------------------------------------------------------------------------------

// noComFila devolve um nó com gate soberano composto e a rota de reclamação a servir.
func noComFila(t *testing.T) (*Node, http.Handler) {
	t.Helper()
	node := newTwoRegionGovNode(t, &countingModel{})
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	regions := govsov.NewRegistry(map[string]string{govBoard: govRegion, govBoardUS: govRegionUS})
	h, err := NewAPIHandler(svc, node, WithReadSovereignty(regions, node.WORM))
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	return node, h
}

// SEM GATE SOBERANO A ROTA RECUSA, e recusa com 501 e não 403.
//
// A diferença é diagnóstica: «não estás autorizado» manda o operador procurar credenciais;
// «este nó não sabe autorizar ninguém» manda-o compor o gate. Servir sem gate entregaria o
// objectivo de um pedido a qualquer chamador autenticado, sem fronteira de região.
func TestAOS423ReclamacaoRecusaSemGateSoberano(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	rec := postJSON(h, "POST", "/plans/claim", nil)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("sem gate soberano a reclamacao devia dar 501, veio %d (%s)", rec.Code, rec.Body.String())
	}
	rec = postJSON(h, "POST", "/plans/outcome", map[string]any{"run_id": "x", "generation": 1, "classe": "terminal"})
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("sem gate soberano o desfecho devia dar 501, veio %d", rec.Code)
	}
}

// O CAMINHO COMPLETO: submeter, reclamar, e a fila esvazia.
func TestAOS423ReclamarUmPedidoSubmetido(t *testing.T) {
	_, h := noComFila(t)

	sub := postReq(h, "/plans", map[string]any{"run_id": "run-423-a", "objective": "resolver X"}, euReaderHeaders())
	if sub.Code != http.StatusCreated {
		t.Fatalf("submissao devia dar 201, veio %d (%s)", sub.Code, sub.Body.String())
	}

	rec := postReq(h, "/plans/claim", nil, euReaderHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("a reclamacao devia devolver o pedido (200), veio %d (%s)", rec.Code, rec.Body.String())
	}
	var p respostaDeReclamo
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("resposta ilegivel: %v", err)
	}
	if p.RunID != "run-423-a" || p.Objective != "resolver X" || p.Geracao != 1 {
		t.Errorf("reclamado = %+v, esperava run-423-a / resolver X / geracao 1", p)
	}

	// RECLAMAR DUAS VEZES NÃO DÁ O MESMO PEDIDO DUAS VEZES. É a propriedade central: dois
	// consumidores a correr o mesmo pedido dariam duas corridas do mesmo objectivo.
	segunda := postReq(h, "/plans/claim", nil, euReaderHeaders())
	if segunda.Code != http.StatusNoContent {
		t.Errorf("a segunda reclamacao devia dar 204 (nada elegivel), veio %d (%s)",
			segunda.Code, segunda.Body.String())
	}
}

// FILA VAZIA DÁ 204, e o 204 é o mesmo quer não haja nada, quer o que há seja de outra região.
//
// É a postura do ADR-030 §2.1: não se revela a EXISTÊNCIA de um recurso a quem não pode agir
// sobre ele. Este teste fixa que os dois casos são indistinguíveis da resposta.
func TestAOS423FilaVaziaEOutraRegiaoSaoIndistinguiveis(t *testing.T) {
	_, h := noComFila(t)

	vazia := postReq(h, "/plans/claim", nil, euReaderHeaders())
	if vazia.Code != http.StatusNoContent {
		t.Fatalf("fila vazia devia dar 204, veio %d", vazia.Code)
	}

	// Um pedido submetido por um leitor de OUTRA região.
	if sub := postReq(h, "/plans", map[string]any{"run_id": "run-423-us", "objective": "obj"}, usReaderHeaders()); sub.Code != http.StatusCreated {
		t.Fatalf("submissao us devia dar 201, veio %d", sub.Code)
	}
	outra := postReq(h, "/plans/claim", nil, euReaderHeaders())
	if outra.Code != http.StatusNoContent {
		t.Fatalf("um pedido de outra regiao NAO pode ser entregue aqui (esperava 204), veio %d (%s)",
			outra.Code, outra.Body.String())
	}
	if vazia.Body.String() != outra.Body.String() {
		t.Errorf("a resposta distingue «fila vazia» de «nada para a tua regiao»: %q vs %q\n"+
			"isso e um oraculo sobre o que existe fora da fronteira do chamador",
			vazia.Body.String(), outra.Body.String())
	}
	// E o dono da região certa recebe-o.
	seu := postReq(h, "/plans/claim", nil, usReaderHeaders())
	if seu.Code != http.StatusOK {
		t.Errorf("o leitor da regiao do pedido devia recebe-lo (200), veio %d", seu.Code)
	}
}

// O DESFECHO TRANSITÓRIO DEVOLVE O PEDIDO À FILA, e o TERMINAL não.
func TestAOS423DesfechoDecideSeVolta(t *testing.T) {
	for _, c := range []struct {
		classe string
		volta  bool
	}{
		{DesfechoTransitorio, true},
		{DesfechoTerminal, false},
		{DesfechoAguardaHumano, false},
	} {
		t.Run(c.classe, func(t *testing.T) {
			_, h := noComFila(t)
			if sub := postReq(h, "/plans", map[string]any{"run_id": "run-423-d", "objective": "o"}, euReaderHeaders()); sub.Code != http.StatusCreated {
				t.Fatalf("submissao: %d", sub.Code)
			}
			rec := postReq(h, "/plans/claim", nil, euReaderHeaders())
			if rec.Code != http.StatusOK {
				t.Fatalf("reclamacao: %d", rec.Code)
			}
			var p respostaDeReclamo
			_ = json.Unmarshal(rec.Body.Bytes(), &p)

			des := postReq(h, "/plans/outcome", map[string]any{
				"run_id": p.RunID, "generation": p.Geracao, "classe": c.classe, "codigo_saida": 3,
			}, euReaderHeaders())
			if des.Code != http.StatusNoContent {
				t.Fatalf("desfecho devia dar 204, veio %d (%s)", des.Code, des.Body.String())
			}

			outra := postReq(h, "/plans/claim", nil, euReaderHeaders())
			if c.volta && outra.Code != http.StatusOK {
				t.Errorf("com desfecho %s o pedido devia VOLTAR a fila (200), veio %d:\n"+
					"sem isto, uma falha transitoria custa o TTL inteiro de silencio", c.classe, outra.Code)
			}
			if !c.volta && outra.Code != http.StatusNoContent {
				t.Errorf("com desfecho %s o pedido NAO devia voltar (204), veio %d:\n"+
					"retentar uma recusa determinista e um laco infinito", c.classe, outra.Code)
			}
		})
	}
}

// O VOCABULÁRIO DE CLASSES É FECHADO. O nó valida a palavra; a decisão de qual usar é do
// `aos-orq`, que é o único que conhece a semântica dos códigos de saída do `serve` (ADR-018).
func TestAOS423ClasseDeDesfechoInvalidaERecusada(t *testing.T) {
	_, h := noComFila(t)
	for _, classe := range []string{"", "falhou", "TERMINAL", "transitório"} {
		rec := postReq(h, "/plans/outcome", map[string]any{
			"run_id": "run-x", "generation": 1, "classe": classe,
		}, euReaderHeaders())
		if rec.Code != http.StatusBadRequest {
			t.Errorf("a classe %q devia ser recusada com 400, veio %d", classe, rec.Code)
		}
	}
}

// O PREDICADO DO BANNER CASA COM O DO `newAPI`.
//
// Se divergirem, o nó anuncia no arranque uma postura de reclamação que a rota não tem — e um
// banner que mente é pior do que nenhum. Esta é a metade em Go; a metade que lê o `bootstrap.go`
// está em `aos417_banner_test.go`.
func TestAOS423PredicadoDoBannerCasaComOReadGov(t *testing.T) {
	// Nó SEM soberania: o `newAPI` não auto-deriva gate, e o predicado tem de dizer o mesmo.
	semGate, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = semGate.Close() }()
	if filaReclamavel(semGate) {
		t.Error("filaReclamavel disse SIM num no sem soberania composta: o banner anunciaria uma " +
			"fila drenavel enquanto POST /plans/claim recusa 501")
	}
	// Nó COM soberania: auto-deriva, e o predicado tem de acompanhar.
	comGate := newTwoRegionGovNode(t, &countingModel{})
	if !filaReclamavel(comGate) {
		t.Error("filaReclamavel disse NAO num no com WORM e autoridade de soberania compostos: o " +
			"banner diria que ninguem pode drenar a fila quando a rota serve")
	}
}

// ---------------------------------------------------------------------------------------------
// A CORRIDA — o sensor que faltava, e a razão de ele ter faltado.
// ---------------------------------------------------------------------------------------------

// storeQueEsconde é o Event Store real com uma leitura CEGA a um StepID.
//
// # PORQUE É QUE ISTO EXISTE
//
// A propriedade central da reclamação é que dois consumidores nunca recebem o mesmo pedido. O
// árbitro é o `StatusDuplicate` do Append. Mas um teste que reclame duas vezes EM SÉRIE nunca lá
// chega: a segunda projecção já vê a reclamação da primeira e salta o pedido antes de tentar
// escrever.
//
// Foi medido por MUTAÇÃO: apagar o ramo do `StatusDuplicate` deixava a suite VERDE. O guard não
// tinha sensor nenhum, e era o guard mais importante do ficheiro.
//
// Este store torna a corrida DETERMINISTA em vez de a deixar à sorte de duas goroutines: a
// leitura esconde a reclamação que outro consumidor já gravou, pelo que a projecção acha o
// pedido elegível na MESMA geração — exactamente o estado de quem leu antes de o outro escrever.
// O Append, esse, vê a verdade.
type storeQueEsconde struct {
	EventStorePort
	esconder string
}

func (s storeQueEsconde) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	evs, err := s.EventStorePort.Read(ctx, streamID, fromSeq)
	if err != nil {
		return nil, err
	}
	out := evs[:0:0]
	for _, ev := range evs {
		if ev.StepID == s.esconder {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

// DOIS CONSUMIDORES NA MESMA GERAÇÃO: só um leva o pedido.
//
// Sem o `StatusDuplicate` como árbitro, os dois levavam — e o mesmo objectivo corria duas vezes,
// com dois runs a disputar o mesmo lease.
func TestAOS423DoisConsumidoresNaoLevamOMesmoPedido(t *testing.T) {
	node, h := noComFila(t)

	if sub := postReq(h, "/plans", map[string]any{"run_id": "run-423-corrida", "objective": "o"}, euReaderHeaders()); sub.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", sub.Code)
	}
	// O consumidor A reclama, e ganha.
	primeira := postReq(h, "/plans/claim", nil, euReaderHeaders())
	if primeira.Code != http.StatusOK {
		t.Fatalf("a primeira reclamacao devia ganhar (200), veio %d", primeira.Code)
	}
	var pa respostaDeReclamo
	_ = json.Unmarshal(primeira.Body.Bytes(), &pa)

	// O consumidor B tinha lido ANTES de A escrever: projecta o pedido como elegível na MESMA
	// geração. É o que este store simula.
	cego := storeQueEsconde{
		EventStorePort: node.EventStore,
		esconder:       prefixoReclamo + strconv.Itoa(pa.Geracao) + "-" + pa.RunID,
	}
	hA := &apiHandler{node: &Node{EventStore: cego, WORM: node.WORM}}
	segundo, err := hA.reclamarUm(context.Background(), readerIdentity{principal: "p", board: govBoard, region: govRegion})
	if err != nil {
		t.Fatalf("a segunda reclamacao nao devia ERRAR (o duplicado nao e falha): %v", err)
	}
	if segundo != nil {
		t.Errorf("DOIS consumidores levaram o pedido %q na geracao %d.\n"+
			"O arbitro e o StatusDuplicate do Append: sem ele, o mesmo objectivo corre duas vezes\n"+
			"e os dois runs disputam o mesmo lease.", segundo.RunID, segundo.Geracao)
	}
}
