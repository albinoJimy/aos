package main

// aos426_streams_internos_test.go — O READ-PATH DOS RUNS NÃO SERVE O INTERIOR DO NÓ.
//
// Estes testes nasceram de uma medição, e a medição foi feita porque a revisão adversarial do
// AOS-417 tinha encontrado exactamente este defeito NUMA superfície nova — a fila de pedidos de
// plano — e ninguém tinha perguntado se as superfícies ANTIGAS tinham o mesmo.
//
// Tinham. Treze streams internos respondiam `200` a `GET /runs/{id}/trajectory` com um leitor
// autenticado de OUTRA região, e o discriminador entre os expostos e os seguros era um acaso: os
// seguros tinham barra no nome. A barra estava lá para namespacing; a protecção veio de lambuja.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/platform/memory/compression"
	"github.com/aos-ref/platform/memory/episodic"
	"github.com/aos-ref/platform/memory/semantic"
	"github.com/aos-ref/substrate/eventstore"
)

// pedirTrajectoria faz o GET do SSE com prazo — sem ele o handler de um stream servido nunca
// retorna, e o teste ficaria pendurado em vez de falhar.
func pedirTrajectoria(t *testing.T, h http.Handler, id string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("GET", "/runs/"+id+"/trajectory", nil).WithContext(ctx)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// OS STREAMS INTERNOS DO NÓ NÃO SÃO SERVIDOS PELO READ-PATH DOS RUNS.
//
// A lista traz os nomes REAIS — e, onde a constante é exportada, **a própria constante** em vez
// de uma cópia do seu valor. A diferença não é estilística: com cópias, o AOS-424 renomeou três
// streams e esta lista passou a testar nomes MORTOS sem que nada avisasse. Inclui também os oito
// que já estavam seguros — porque um teste que só enumerasse os expostos deixaria de detectar o
// dia em que um dos seguros deixasse de o ser.
func TestAOS426ReadPathNaoServeStreamsInternos(t *testing.T) {
	internos := []struct{ nome, stream, dono string }{
		{"aprovacoes four-eyes", "gov.approvals", "integration/approval_store_durable.go"},
		{"memoria episodica", "memory.episodic", "platform/memory/adapters"},
		{"memoria semantica", "memory.semantic", "platform/memory/adapters"},
		{"memoria procedural", "memory.procedural", "platform/memory/adapters"},
		{"memoria working", "memory.working", "platform/memory/adapters"},
		// AS CONSTANTES VIVAS, e não cópias do valor.
		//
		// A primeira versão desta lista trazia os valores literais (`memory.semantic.knowledge`,
		// …). O AOS-424 renomeou-os, esta lista NÃO foi actualizada, e os três subtestes
		// passaram a correr contra nomes MORTOS — verdes, a medir streams que nenhum componente
		// escreve, enquanto as constantes vivas ficaram sem cobertura. Uma revisão adversarial
		// mediu-o. Referenciar a constante torna a deriva impossível.
		{"conhecimento", semantic.KnowledgeStreamID, "platform/memory/semantic"},
		{"trajectorias de memoria", episodic.EpisodicStreamID, "platform/memory/episodic"},
		{"sumarios de compactacao", compression.CompressionStreamID, "platform/memory/compression"},
		// `migrationStream` não é exportada, pelo que aqui não há constante para referenciar. O
		// valor fica em literal e o gate `stream-names` é que vigia o original.
		{"migracoes de memoria", "aos-internal/memory/migrations", "platform/memory/migrations"},
		{"identidade", "identity", "platform/identity/events.go"},
		{"registo de artefactos", "registry", "platform/registry/events.go"},
		{"posse de run", "lease:run-alheio", "agent-runtime/durable/lease.go"},
		{"nonce de ratificacao", "ratify-nonce:escopo:deadbeef", "governance/hitl/nonce_store.go"},
		{"challenge 4-eyes", "4eyes-challenge:escopo:deadbeef", "governance/hitl/challenge_issuer.go"},
		// Os que a barra ja protegia. Ficam na lista para que a proteccao deixe de depender dela.
		{"balde de admissao", "admission/bucket/openai:gpt-4o:eu", "scheduler/admission.go"},
		{"audit de admissao", "admission/audit/openai:gpt-4o:eu", "scheduler/admission.go"},
		{"fila de backpressure", "backpressure/queue/default", "scheduler/queue.go"},
		{"degradacao", "degradation/default", "scheduler/degradation.go"},
		{"roteamento", "routing/default", "scheduler/routing.go"},
		{"despacho", "scheduling/dispatch/default", "scheduler/priority.go"},
		{"disjuntor de orcamento", "budget-breaker/arvore-1", "scheduler/breaker.go"},
		{"fila de pedidos de plano", "aos-internal/plan-requests", "cmd/aos/plan_ingress.go"},
	}

	node := newTwoRegionGovNode(t, &countingModel{})
	_, h := newAPI(t, node)

	const marca = "CONTEUDO-INTERNO-DO-NO"
	for _, c := range internos {
		t.Run(c.nome, func(t *testing.T) {
			// O facto é gravado como o componente REAL o grava: o `RunID` do evento é o do run
			// que o produziu (ou um id sintético), NUNCA o nome do stream. É essa a diferença
			// que a trava lê — ver streams_internos.go.
			if _, err := node.EventStore.Append(context.Background(), c.stream, eventstore.EventInput{
				Type:     "teste.facto.interno",
				Payload:  []byte(`{"conteudo":"` + marca + `"}`),
				RunID:    "run-que-produziu-o-facto",
				StepID:   "s1",
				Producer: eventstore.Producer{NHIID: "nhi:componente"},
			}); err != nil {
				t.Fatalf("Append em %q: %v", c.stream, err)
			}

			rec := pedirTrajectoria(t, h, c.stream, usReaderHeaders())
			if rec.Code != http.StatusNotFound {
				t.Errorf("EXPOSTO — GET /runs/%s/trajectory devia dar 404, veio %d (dono: %s)",
					c.stream, rec.Code, c.dono)
			}
			if strings.Contains(rec.Body.String(), marca) {
				t.Errorf("EXPOSTO — o corpo serviu conteudo interno de %q (dono: %s):\n%.400s",
					c.stream, c.dono, rec.Body.String())
			}
		})
	}
}

// CONTROLO DE NÃO-VACUIDADE, e é a metade que impede a correcção de ser «negar tudo».
//
// Um run REAL, submetido pela rota, com eventos seus, TEM de continuar a ser servido ao seu
// leitor. Sem esta asserção, uma trava que recusasse toda a gente passaria no teste acima.
func TestAOS426RunLegitimoContinuaAServirTrajectoria(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	svc, h := newAPI(t, node)

	const runID = "run-426-legitimo"
	if rec := postReq(h, "/runs", submitRequest{RunID: runID, PrincipalNHI: "nhi:" + runID}, euReaderHeaders()); rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, ok, err := svc.Wait(waitCtx, runID); err != nil || !ok {
		t.Fatalf("Wait(%s): ok=%t err=%v", runID, ok, err)
	}

	rec := pedirTrajectoria(t, h, runID, euReaderHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("a trajectória de um run LEGITIMO devia dar 200, veio %d (%s) — a trava do "+
			"AOS-426 passou a recusar o que devia servir", rec.Code, rec.Body.String())
	}
	if len(rec.Body.String()) == 0 {
		t.Fatal("a trajectoria do run veio VAZIA — 200 sem conteudo nao prova que o backfill serve")
	}
}

// A TRAVA DECIDE PELOS DADOS, e o teste prova-o sem passar pelo HTTP: o MESMO nome de stream é
// servível ou não consoante os eventos declararem, ou não, pertencer-lhe.
//
// É o que distingue esta correcção de uma lista de nomes proibidos: um stream interno NOVO fica
// coberto no dia em que nasce, sem ninguém o declarar em lado nenhum.
func TestAOS426TravaDecidePelosDadosENaoPeloNome(t *testing.T) {
	const id = "um-nome-qualquer"

	doRun := []eventstore.Event{{RunID: id}, {RunID: id}}
	if !streamDeRun(id, doRun) {
		t.Error("eventos que declaram pertencer ao run pedido deviam ser aceites como stream de run")
	}

	// Um SÓ evento discordante chega para recusar: num stream em que os eventos de um run
	// tenham sido misturados com os de outra coisa, aceitar pela maioria serviria o resto.
	misturado := []eventstore.Event{{RunID: id}, {RunID: "outro"}}
	if streamDeRun(id, misturado) {
		t.Error("um stream com eventos de OUTRO dono nao pode ser servido como stream de run")
	}

	// Vazio nao e prova de nada — quem trata desse caso e a posse local, que e outro facto.
	if streamDeRun(id, nil) {
		t.Error("um stream vazio nao pode ser afirmado como stream de run por falta de evidencia")
	}
}
