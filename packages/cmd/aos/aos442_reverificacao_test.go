package main

// aos442_reverificacao_test.go — UM PLANO À ESPERA DE HUMANO VOLTA A SER OFERECIDO (AOS-442).
//
// Até aqui o `aguarda_humano` contava como terminado: depois da decisão humana, nada no caminho da
// fila voltava a correr o pedido. Passa a ESTACIONAR — fora da fila durante o
// [intervaloDeReverificacao], e de novo oferecido a seguir, para o consumidor verificar se a
// decisão já existe. O nó não sabe o que é uma decisão (ADR-018); sabe re-oferecer.
//
// Tudo aqui corre sobre a projecção pura, com o `agora` dado pelo teste: o intervalo é de minutos
// e nenhum teste espera por ele.

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// evDesfechoEm é o [evDesfecho] com o carimbo do envelope — que é o que ancora a re-oferta.
func evDesfechoEm(seq uint64, runID string, ger int, classe string, quando time.Time) eventstore.Event {
	ev := evDesfecho(seq, runID, ger, classe)
	ev.Ts = quando.Format(time.RFC3339Nano)
	return ev
}

// evDesfechoComCodigo é um desfecho com código e detalhe, para os testes do estado servido.
func evDesfechoComCodigo(seq uint64, runID string, ger int, classe string, codigo int, quando time.Time) eventstore.Event {
	p, _ := json.Marshal(desfechoPayload{Versao: "1.0", RunID: runID, Classe: classe, CodigoDe: codigo, Detalhe: "d" + strconv.Itoa(ger)})
	return eventstore.Event{
		Seq: seq, Type: EventTypePlanRequestOutcome,
		StepID: prefixoDesfecho + strconv.Itoa(ger) + "-" + runID, Payload: p,
		Ts: quando.Format(time.RFC3339Nano),
	}
}

func TestAOS442AguardaHumanoEstacionaEVoltaDepoisDoIntervalo(t *testing.T) {
	agora := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	recente := agora.Add(-time.Minute)
	passado := agora.Add(-intervaloDeReverificacao - time.Second)

	casos := []struct {
		nome    string
		eventos []eventstore.Event
		geracao int // 0 ⇒ não elegível
		porque  string
	}{
		{
			nome: "estacionado dentro do intervalo",
			eventos: []eventstore.Event{
				evSubmetido(1, "a", "eu"), evReclamado(2, "a", 1, recente),
				evDesfechoEm(3, "a", 1, DesfechoAguardaHumano, recente),
			},
			porque: "ninguém decidiu ainda; re-oferecer a cada drenagem era ruído no log",
		},
		{
			nome: "RE-OFERECIDO depois do intervalo, na geração seguinte",
			eventos: []eventstore.Event{
				evSubmetido(1, "a", "eu"), evReclamado(2, "a", 1, passado),
				evDesfechoEm(3, "a", 1, DesfechoAguardaHumano, passado),
			},
			geracao: 2,
			porque:  "FALHA-ANTES: o aguarda_humano fechava o pedido, e depois da decisão ele nunca mais corria",
		},
		{
			nome: "re-oferta reclamada: a reclamação viva segura-o",
			eventos: []eventstore.Event{
				evSubmetido(1, "a", "eu"), evReclamado(2, "a", 1, passado),
				evDesfechoEm(3, "a", 1, DesfechoAguardaHumano, passado),
				evReclamado(4, "a", 2, recente),
			},
			porque: "alguém está a verificá-lo agora",
		},
		{
			nome: "re-verificado e AINDA à espera: estaciona de novo",
			eventos: []eventstore.Event{
				evSubmetido(1, "a", "eu"), evReclamado(2, "a", 1, passado),
				evDesfechoEm(3, "a", 1, DesfechoAguardaHumano, passado),
				evReclamado(4, "a", 2, recente), evDesfechoEm(5, "a", 2, DesfechoAguardaHumano, recente),
			},
			porque: "o intervalo conta da ÚLTIMA verificação, não da primeira",
		},
		{
			nome: "depois da decisão o consumidor fecha-o: terminal não volta, nem depois do intervalo",
			eventos: []eventstore.Event{
				evSubmetido(1, "a", "eu"), evReclamado(2, "a", 1, passado),
				evDesfechoEm(3, "a", 1, DesfechoAguardaHumano, passado),
				evReclamado(4, "a", 2, passado), evDesfechoEm(5, "a", 2, DesfechoTerminal, passado),
			},
			porque: "o plano correu (ou foi recusado): retentá-lo era um laço",
		},
		{
			nome: "aguarda antigo seguido de transitório: volta JÁ",
			eventos: []eventstore.Event{
				evSubmetido(1, "a", "eu"), evReclamado(2, "a", 1, passado),
				evDesfechoEm(3, "a", 1, DesfechoAguardaHumano, passado),
				evReclamado(4, "a", 2, recente), evDesfechoEm(5, "a", 2, DesfechoTransitorio, recente),
			},
			geracao: 3,
			porque:  "só a ÚLTIMA geração estaciona; uma falha transitória depois da aprovação não espera o intervalo",
		},
		{
			nome: "carimbo ilegível re-oferece em vez de estacionar para sempre",
			eventos: []eventstore.Event{
				evSubmetido(1, "a", "eu"), evReclamado(2, "a", 1, passado),
				evDesfecho(3, "a", 1, DesfechoAguardaHumano),
			},
			geracao: 2,
			porque:  "estacionar sem prazo é o defeito que o AOS-442 fecha; re-oferecer custa uma verificação",
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			fila := projectarFila(c.eventos, agora)
			if c.geracao == 0 {
				if len(fila) != 0 {
					t.Fatalf("não devia ser elegível, veio %v — %s", idsDe(fila), c.porque)
				}
				return
			}
			if len(fila) != 1 || fila[0].RunID != "a" || fila[0].Geracao != c.geracao {
				t.Fatalf("esperava «a» na geração %d, veio %+v — %s", c.geracao, fila, c.porque)
			}
		})
	}
}

// O ESTACIONADO NÃO É TERMINADO para a marca de água: cortar o log acima dele esconderia a
// re-oferta, e o pedido ficava parado de outra maneira.
func TestAOS442EstacionadoSeguraAMarcaDeAgua(t *testing.T) {
	agora := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	recente := agora.Add(-time.Minute)
	eventos := []eventstore.Event{
		evSubmetido(1, "a", "eu"), evReclamado(2, "a", 1, recente),
		evDesfechoEm(3, "a", 1, DesfechoAguardaHumano, recente),
		evSubmetido(4, "b", "eu"), evReclamado(5, "b", 1, recente),
		evDesfechoEm(6, "b", 1, DesfechoTerminal, recente),
	}
	_, marca := projectarFilaComMarca(eventos, agora)
	if marca != 0 {
		t.Fatalf("a marca avançou para %d por cima de um pedido À ESPERA DE HUMANO:\n"+
			"a leitura seguinte começava acima dele e a re-oferta nunca era vista", marca)
	}
}

// UM PEDIDO ILEGÍVEL NÃO TAPA OS SEGUINTES.
//
// Um pedido cujo objectivo selado já não abre (a KEK do titular destruída por `/dsar/erase`)
// devolvia 503 à reclamação, e o `consume` abortava a drenagem inteira. Com a re-oferta dos
// pedidos à espera de humano, isso repetia-se a cada expiração da reclamação. FALHA-ANTES: a
// primeira reclamação dava 503, e o pedido legível atrás dele não era entregue.
func TestAOS442PedidoIlegivelNaoTapaOsSeguintes(t *testing.T) {
	node, h := noComFila(t)
	ctx := context.Background()
	morto, _ := json.Marshal(planRequestPayload{
		Versao: planRequestVersao, RunID: "run-442-morto", Principal: "human:apagado",
		ObjetivoSelado: []byte(`{"isto":"ja nao abre"}`),
	})
	if _, err := node.EventStore.Append(ctx, planRequestStream, eventstore.EventInput{
		Type: EventTypePlanRequestSubmitted, Payload: morto, RunID: planRequestRunID,
		StepID: prefixoPedido + "run-442-morto", Producer: eventstore.Producer{NHIID: planIngressNHI},
	}); err != nil {
		t.Fatalf("pedido morto: %v", err)
	}
	if sub := postReq(h, "/plans", map[string]any{"run_id": "run-442-vivo", "objective": "o"}, euReaderHeaders()); sub.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", sub.Code)
	}

	rec := postReq(h, "/plans/claim", nil, euReaderHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("o pedido legível tinha de ser entregue apesar do ilegível à frente, veio %d (%s)", rec.Code, rec.Body.String())
	}
	var p respostaDeReclamo
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if p.RunID != "run-442-vivo" {
		t.Fatalf("entregou %q, esperava o legível", p.RunID)
	}
	// Só o ilegível na fila (reclamado e à espera do TTL): nada entregável ⇒ 204, porque o
	// ilegível está preso pela sua reclamação e não volta a ser tentado já.
	if outra := postReq(h, "/plans/claim", nil, euReaderHeaders()); outra.Code != http.StatusNoContent {
		t.Fatalf("sem mais nada entregável esperava 204, veio %d (%s)", outra.Code, outra.Body.String())
	}
}

// storeDeEventos é o Event Store visto pelo estado de um pedido: só o Read é chamado.
type storeDeEventos struct {
	EventStorePort
	eventos []eventstore.Event
}

func (s storeDeEventos) Read(context.Context, string, uint64) ([]eventstore.Event, error) {
	return s.eventos, nil
}

// O ESTADO SERVIDO segue a re-oferta, e é DETERMINISTA.
//
// FALHA-ANTES: a derivação percorria o mapa de desfechos e devolvia a primeira entrada terminal OU
// à-espera-de-humano — com `aguarda_humano` na geração 1 e `terminal` na 2, a resposta dependia da
// ordem (aleatória) do mapa. Repete-se para a apanhar.
func TestAOS442EstadoDoPedidoSegueAReoferta(t *testing.T) {
	agora := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	recente := agora.Add(-time.Minute)
	passado := agora.Add(-intervaloDeReverificacao - time.Second)
	base := []eventstore.Event{
		{Seq: 1, Type: EventTypePlanRequestSubmitted, StepID: prefixoPedido + "a",
			Payload: json.RawMessage(`{"v":"1.0","run_id":"a","principal":"p"}`)},
		evReclamado(2, "a", 1, passado),
		evDesfechoComCodigo(3, "a", 1, DesfechoAguardaHumano, 6, passado),
	}
	mais := func(evs ...eventstore.Event) []eventstore.Event {
		return append(append([]eventstore.Event(nil), base...), evs...)
	}
	casos := []struct {
		nome    string
		eventos []eventstore.Event
		estado  string
		codigo  int
	}{
		{"estacionado, mesmo já re-oferecível", base, EstadoPlanoAguardaHumano, 6},
		{"a ser re-verificado", mais(evReclamado(4, "a", 2, recente)), EstadoPlanoEmCurso, 0},
		{"aprovado e corrido", mais(evReclamado(4, "a", 2, recente), evDesfechoComCodigo(5, "a", 2, DesfechoTerminal, 0, recente)), EstadoPlanoTerminado, 0},
		{"aprovado e a falhar transitoriamente", mais(evReclamado(4, "a", 2, recente), evDesfechoComCodigo(5, "a", 2, DesfechoTransitorio, 8, recente)), EstadoPlanoPendente, 8},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			for i := 0; i < 64; i++ {
				e, achado, err := estadoDoPedido(context.Background(), storeDeEventos{eventos: c.eventos}, "a", agora)
				if err != nil || !achado {
					t.Fatalf("estadoDoPedido: achado=%v err=%v", achado, err)
				}
				if e.resposta.Estado != c.estado || e.resposta.CodigoSaida != c.codigo {
					t.Fatalf("iteração %d: estado=%q codigo=%d, esperava %q/%d",
						i, e.resposta.Estado, e.resposta.CodigoSaida, c.estado, c.codigo)
				}
			}
		})
	}
}
