package main

// AOS-418 — UM `serve` QUE MORRESSE A MEIO LEVAVA OS PAYLOADS COM ELE.
//
// O conteúdo que os nós trocam vivia só no mapa em memória do executor (decisão (A) do dono no
// AOS-414, declarada como resíduo). Um processo novo sobre o MESMO plano via o mapa vazio, e o
// consumidor falhava com [ErrPayloadPerdido] — apesar de a saída do produtor existir, durável, no
// log e no run filho.
//
// Estes testes medem a RECONSTRUÇÃO: o que o log já carrega chega para repor o mapa, e o que não
// se consegue confirmar não entra.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/control-plane/runlifecycle"
	"github.com/aos-ref/substrate/eventstore"
)

// storeDePayloads devolve um stream com os `plan.payload_published` que lhe dermos.
type storeDePayloads struct{ eventos []eventoDeTeste }

type eventoDeTeste struct {
	tipo    string
	payload any
}

func (s *storeDePayloads) Read(ctx context.Context, stream string, fromSeq uint64) ([]eventstore.Event, error) {
	out := make([]eventstore.Event, 0, len(s.eventos))
	for _, e := range s.eventos {
		bruto, err := json.Marshal(e.payload)
		if err != nil {
			return nil, err
		}
		out = append(out, eventstore.Event{Type: e.tipo, Payload: bruto})
	}
	return out, nil
}

// Append nunca é chamado: a reidratação só LÊ. Está aqui para satisfazer a interface.
func (s *storeDePayloads) Append(ctx context.Context, stream string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	panic("a reidratacao de payloads NAO escreve (AOS-418)")
}

// clienteDeRehidratacao é o nó visto pela reidratação: devolve uma saída e conta consultas.
type clienteDeRehidratacao struct {
	saida     string
	existe    bool
	erro      error
	consultas int
}

func (c *clienteDeRehidratacao) Submit(ctx context.Context, p pedidoDeRun) error {
	panic("a reidratacao de payloads NAO submete runs (AOS-418)")
}

func (c *clienteDeRehidratacao) Status(ctx context.Context, runID string) (estadoDoRun, bool, error) {
	c.consultas++
	if c.erro != nil {
		return estadoDoRun{}, false, c.erro
	}
	return estadoDoRun{FinalText: c.saida}, c.existe, nil
}

// executorReidratado compõe o executor pelo CONSTRUTOR, que é quem reidrata. Os testes passam
// por aqui de propósito: com a reidratação no wiring, tirá-la deixava a suite inteira verde —
// foi o que a revisão adversarial mediu.
func executorReidratado(t *testing.T, cli nodeRunner, store runlifecycle.EventStore) *executorDeNos {
	t.Helper()
	e, err := novoExecutorDeNos(context.Background(), cli, nil, nil, "run-418",
		plan.PlanDocument{}, nil, nil, store, "plan-418")
	if err != nil {
		t.Fatalf("novoExecutorDeNos: %v", err)
	}
	return e
}

// executorSemLog compõe um executor sem fonte de reidratação — o estado de antes do AOS-418.
func executorSemLog(t *testing.T) *executorDeNos {
	t.Helper()
	e, err := novoExecutorDeNos(context.Background(), nil, nil, nil, "run-418",
		plan.PlanDocument{}, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("novoExecutorDeNos: %v", err)
	}
	return e
}

// TestAOS418_FormaFechadaReconstroiSeDoLog: o veredicto viaja inteiro no evento, por isso a
// reconstrução não precisa de falar com ninguém.
func TestAOS418_FormaFechadaReconstroiSeDoLog(t *testing.T) {
	esperado, err := conteudoFechado("pass", []string{"documento_lido"})
	if err != nil {
		t.Fatalf("conteudoFechado: %v", err)
	}

	store := &storeDePayloads{eventos: []eventoDeTeste{{
		tipo: plannerevents.EventPayloadPublished,
		payload: plannerevents.PayloadPublishedPayload{
			NodeID: "verificador", Output: "veredicto",
			Closed: &plannerevents.ClosedPayload{Outcome: "pass", Reasons: []string{"documento_lido"}},
		},
	}}}
	e := executorReidratado(t, nil, store)

	got, ok := e.payloads[chaveDePayload{no: "verificador", output: "veredicto"}]
	if !ok {
		t.Fatal("o payload de forma FECHADA não foi reconstruído do log — o conteúdo está inteiro no evento (AOS-418)")
	}
	if got != esperado {
		t.Fatalf("conteúdo reconstruído = %q, quero %q — a forma canónica divergiu entre publicar e reidratar", got, esperado)
	}
}

// TestAOS418_FormaAbertaReleDoRunFilhoEConfereODigest: o evento só carrega referência e digest,
// por isso o conteúdo relê-se — e confirma-se.
func TestAOS418_FormaAbertaReleDoRunFilhoEConfereODigest(t *testing.T) {
	conteudo := "o texto que o no produziu"
	cli := &clienteDeRehidratacao{saida: conteudo, existe: true}

	store := &storeDePayloads{eventos: []eventoDeTeste{{
		tipo: plannerevents.EventPayloadPublished,
		payload: plannerevents.PayloadPublishedPayload{
			NodeID: "leitor", Output: "documento",
			Record: plannerevents.PayloadRecordRef{
				Store:  plannerevents.PayloadStoreEventStore,
				Stream: childRunID("run-418", "leitor"),
				Digest: digestDoConteudo(conteudo),
			},
		},
	}}}
	e := executorReidratado(t, cli, store)

	if got := e.payloads[chaveDePayload{no: "leitor", output: "documento"}]; got != conteudo {
		t.Fatalf("payload de forma ABERTA = %q, quero %q — a referência do evento aponta ao run filho (AOS-418)", got, conteudo)
	}
	if cli.consultas != 1 {
		t.Fatalf("o run filho foi consultado %d vezes, quero 1", cli.consultas)
	}
}

// TestAOS418_DigestQueNaoBateNaoEntra é a guarda que separa «reconstruir» de «aceitar o que
// voltar». Se a saída do run filho mudou, entregá-la seria substituir o payload por outro sem
// ninguém dar por isso.
func TestAOS418_DigestQueNaoBateNaoEntra(t *testing.T) {
	cli := &clienteDeRehidratacao{saida: "OUTRA coisa qualquer", existe: true}

	store := &storeDePayloads{eventos: []eventoDeTeste{{
		tipo: plannerevents.EventPayloadPublished,
		payload: plannerevents.PayloadPublishedPayload{
			NodeID: "leitor", Output: "documento",
			Record: plannerevents.PayloadRecordRef{
				Store:  plannerevents.PayloadStoreEventStore,
				Stream: childRunID("run-418", "leitor"),
				Digest: digestDoConteudo("o conteudo que foi PUBLICADO"),
			},
		},
	}}}
	e := executorReidratado(t, cli, store)

	if _, ok := e.payloads[chaveDePayload{no: "leitor", output: "documento"}]; ok {
		t.Fatal("um payload cujo digest NÃO bate entrou no mapa — o consumidor receberia bytes que ninguém publicou (AOS-418)")
	}
}

// TestAOS418_RunFilhoDesaparecidoNaoEntra: sem fonte, o payload fica por cumprir e o consumidor
// falha como antes — que é a direcção segura.
func TestAOS418_RunFilhoDesaparecidoNaoEntra(t *testing.T) {
	cli := &clienteDeRehidratacao{existe: false}

	store := &storeDePayloads{eventos: []eventoDeTeste{{
		tipo: plannerevents.EventPayloadPublished,
		payload: plannerevents.PayloadPublishedPayload{
			NodeID: "leitor", Output: "documento",
			Record: plannerevents.PayloadRecordRef{
				Store: plannerevents.PayloadStoreEventStore, Stream: childRunID("run-418", "leitor"), Digest: "sha256:0",
			},
		},
	}}}
	e := executorReidratado(t, cli, store)

	if len(e.payloads) != 0 {
		t.Fatalf("o mapa ficou com %d payloads sobre um run filho que o nó não conhece", len(e.payloads))
	}
}

// TestAOS418_SemReidratacaoOConsumidorPerdeOPayload é o controlo que dá sentido aos outros: prova
// que o mapa vazio É o modo de falha, e não uma hipótese.
func TestAOS418_SemReidratacaoOConsumidorPerdeOPayload(t *testing.T) {
	e := executorSemLog(t)
	no := plan.Node{NodeID: "consumidor", Consumes: []plan.PayloadEdge{{From: "leitor", Output: "documento"}}}
	e.nos["consumidor"] = no

	_, err := e.entradasDe(no)
	if !errors.Is(err, ErrPayloadPerdido) {
		t.Fatalf("entradasDe sobre um mapa vazio = %v, quero ErrPayloadPerdido — é este o modo de falha que o AOS-418 fecha", err)
	}
}

// TestAOS418_RazoesVaziasNaoDivergemEntrePublicarEReidratar é o achado CRÍTICO da revisão
// adversarial. A publicação calculava o conteúdo das razões CRUAS e a reidratação das razões do
// evento, que o `normalizeClosed` já reduziu — uma lista vazia vira `nil`, porque o campo é
// `omitempty`. Medido na altura: publicado `{"outcome":"pass","reasons":[]}`, reidratado
// `{"outcome":"pass","reasons":null}`. Uma função partilhada fecha o eixo da EXPRESSÃO; este é o
// das ENTRADAS.
func TestAOS418_RazoesVaziasNaoDivergemEntrePublicarEReidratar(t *testing.T) {
	naPublicacao, err := conteudoFechado("pass", []string{})
	if err != nil {
		t.Fatalf("conteudoFechado (cru): %v", err)
	}
	naReidratacao, err := conteudoFechado("pass", nil)
	if err != nil {
		t.Fatalf("conteudoFechado (normalizado): %v", err)
	}
	if naPublicacao != naReidratacao {
		t.Fatalf("publicado=%s reidratado=%s — o consumidor recebe bytes diferentes conforme o processo tenha reiniciado (AOS-418)", naPublicacao, naReidratacao)
	}
}

// TestAOS418_EventoIlegivelNaoTrancaOPlano: um `plan.payload_published` corrompido não desaparece
// de um log append-only. Abortar por causa dele repetia-se em TODAS as retomas e deixava o plano
// irrecuperável por linha de comando.
func TestAOS418_EventoIlegivelNaoTrancaOPlano(t *testing.T) {
	store := &storeDeBytes{bruto: [][]byte{[]byte("{isto nao e json")}}
	e, err := novoExecutorDeNos(context.Background(), nil, nil, nil, "run-418",
		plan.PlanDocument{}, nil, nil, store, "plan-418")
	if err != nil {
		t.Fatalf("um evento ilegível trancou o arranque: %v — o plano ficaria irrecuperável (AOS-418)", err)
	}
	if len(e.payloads) != 0 {
		t.Fatalf("o mapa ficou com %d payloads a partir de um evento ilegível", len(e.payloads))
	}
}

// storeDeBytes devolve payloads crus, para poder entregar um evento malformado.
type storeDeBytes struct{ bruto [][]byte }

func (s *storeDeBytes) Read(ctx context.Context, stream string, fromSeq uint64) ([]eventstore.Event, error) {
	out := make([]eventstore.Event, 0, len(s.bruto))
	for _, b := range s.bruto {
		out = append(out, eventstore.Event{Type: plannerevents.EventPayloadPublished, Payload: b})
	}
	return out, nil
}

func (s *storeDeBytes) Append(ctx context.Context, stream string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	panic("a reidratacao de payloads NAO escreve (AOS-418)")
}
