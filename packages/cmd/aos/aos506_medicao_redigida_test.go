package main

// AOS-506 (revisão, I-1 e M-2) — A MEDIÇÃO DO PROMPT RECALCULA SOBRE O QUE O SERVIÇO HOSPEDOU.
//
// O `POST /runs` tem o Goal ANTES da ingestão; o serviço minimiza o objectivo depois (AOS-208).
// Uma medição feita com a semente do pedido dava falso positivo em qualquer nó cujo objectivo
// tivesse um e-mail ou um telefone. A semente passa a ser a do Goal hospedado
// ([NodeService.SubmitObservando]), e só existe com aviso em jogo.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// aos506ComPII é um objectivo que a ingestão redige: um e-mail e um telefone.
const aos506ComPII = "Le o documento notes e envia a alice.silva@example.com, tel +351 912 345 678"

// aos506SubmeterCom envia a tentativa `tentativa` do nó do plano com o objectivo dado, e espera
// o fim do run.
func aos506SubmeterCom(t *testing.T, n *aos502No, plano string, ger, tentativa int, objectivo string) {
	t.Helper()
	v := aos502Vinculo(plano, aos502No_, ger, tentativa)
	if tentativa == 1 {
		v.Attempt = 0
	}
	corpo := map[string]any{
		"run_id": idDaTentativa(plano, aos502No_, tentativa), "objective": objectivo, "principal_nhi": durAgent, "credential": n.tok,
		"tools": []string{aos502Leitura}, "inputs": []any{}, "completion_requires": []string{aos502Leitura}, "plan_request": v,
	}
	if r := postReq(n.h, "/runs", corpo, govHeaders()); r.Code != http.StatusCreated {
		t.Fatalf("tentativa %d: %d %s", tentativa, r.Code, r.Body.String())
	}
	n.esperar(t, idDaTentativa(plano, aos502No_, tentativa))
}

// COM UM OBJECTIVO REDIGÍVEL, A SÉRIE DO HASH FICA A ZERO numa tentativa que só difere pelo
// aviso, e continua a subir com uma diferença real. Com `off` nada muda.
func TestAOS506_On_AMedicaoUsaOObjectivoRedigido(t *testing.T) {
	for _, aviso := range []bool{false, true} {
		n := aos506Compor(t, 2, aviso, "")
		plano := "plano-506-pii-off"
		declarado := agentruntime.RetryNoticeNone
		if aviso {
			plano, declarado = "plano-506-pii-on", agentruntime.RetryNoticeNoFunctionCall
		}
		ger := n.pedir(t, plano)
		id1, id2 := idDaTentativa(plano, aos502No_, 1), idDaTentativa(plano, aos502No_, 2)
		n.modeloNaoChama(aos506RespostaAnterior, "stop")
		aos506SubmeterCom(t, n, plano, ger, 1, aos506ComPII)
		if _, r := n.ler(t, id1); r.Status != "failed" || r.OutcomeReason != string(agentruntime.OutcomeContractNoCall) {
			t.Fatalf("pre-condicao: a primeira tentativa fecha failed por contract_unmet_no_call; veio %+v", r)
		}
		aos506SubmeterCom(t, n, plano, ger, 2, aos506ComPII)

		// Controlo: a ingestão REDIGIU, e a tentativa leva o aviso só com o interruptor ligado.
		const redigido = "Le o documento notes e envia a [REDACTED:email], tel [REDACTED:phone]"
		pedidos, _ := aos506Pedidos(t, n)
		semente := pedidos[1][1].Content
		if strings.Contains(semente, "alice.silva@example.com") || strings.Contains(semente, "912 345 678") || !strings.Contains(semente, redigido) {
			t.Fatalf("aviso=%t: pre-condicao: o objectivo chega ao modelo redigido; veio %q", aviso, semente)
		}
		if aos506LevaAviso(pedidos[1]) != aviso {
			t.Fatalf("aviso=%t: a tentativa 2 leva o aviso so com o interruptor ligado", aviso)
		}

		// A SÉRIE FICA A ZERO, depois de a goroutine da medição ter tido tempo.
		time.Sleep(400 * time.Millisecond)
		if m := n.metricas(t); !aos502TemSerie(m, "aos_runs_retry_prompt_hash_diferente_total", 0) {
			t.Fatalf("aviso=%t: FALSO POSITIVO — a tentativa so difere pelo aviso e a serie do hash saiu de zero:\n%s", aviso, aos502SoRetry(m))
		}
		if aviso {
			// A FUNÇÃO, sobre os turnos reais. Com a semente HOSPEDADA (objectivo redigido) os
			// dois hashes batem; com a do PEDIDO (em claro) não — é o defeito que se fechou, e o
			// controlo de que este teste exercita mesmo a redacção.
			t1, t2 := aos506PrimeiroTurno(t, n, id1), aos506PrimeiroTurno(t, n, id2)
			if promptDaTentativaDifere(sementeDaTentativa{objective: redigido, aviso: declarado}, agentruntime.RetryNoticeNone, t1.Manifest.PromptHash, t2) {
				t.Fatal("com a semente hospedada (objectivo redigido) a tentativa cumpre os dois hashes")
			}
			if !promptDaTentativaDifere(sementeDaTentativa{objective: aos506ComPII, aviso: declarado}, agentruntime.RetryNoticeNone, t1.Manifest.PromptHash, t2) {
				t.Fatal("controlo: com o objectivo EM CLARO o recalculo tinha de falhar")
			}
		}

		// UMA DIFERENÇA REAL CONTINUA A CONTAR: a tentativa 3 traz outro objectivo, também
		// redigível. A 2 não chamou a tool, logo a 3 é admitida. Conta UMA vez: se a medição da
		// tentativa 2 tivesse contado tarde, a série passava de 1.
		aos506SubmeterCom(t, n, plano, ger, 3, aos506ComPII+" e mais uma frase")
		prazo := time.Now().Add(10 * time.Second)
		for !aos502TemSerie(n.metricas(t), "aos_runs_retry_prompt_hash_diferente_total", 1) {
			if time.Now().After(prazo) {
				t.Fatalf("aviso=%t: outro objectivo na tentativa 3 tinha de contar:\n%s", aviso, aos502SoRetry(n.metricas(t)))
			}
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(300 * time.Millisecond)
		if m := n.metricas(t); !aos502TemSerie(m, "aos_runs_retry_prompt_hash_diferente_total", 1) {
			t.Fatalf("aviso=%t: so a tentativa 3 difere; a serie tinha de ficar em 1:\n%s", aviso, aos502SoRetry(m))
		}
	}
}

// O OBSERVADOR VÊ O GOAL HOSPEDADO, UMA VEZ, E SÓ QUANDO O RUN É HOSPEDADO. E o handler só o
// pede com aviso em jogo: sem aviso, a medição não guarda nada do pedido (M-2).
func TestAOS506_SubmitObservando_EMedicaoSemSemente(t *testing.T) {
	n := aos506Compor(t, 2, true, "")
	const plano = "plano-506-observador"
	ger := n.pedir(t, plano)
	aos506FalharSemChamar(t, n, plano, ger)
	id1, id2 := idDaTentativa(plano, aos502No_, 1), idDaTentativa(plano, aos502No_, 2)

	// Uma submissão recusada (o run já terminou e está retido) não chama o observador.
	vezes := 0
	if err := n.svc.SubmitObservando(context.Background(), agentruntime.Goal{RunID: id1}, func(agentruntime.Goal) { vezes++ }); err == nil || vezes != 0 {
		t.Fatalf("uma submissao recusada nao hospeda nada nem chama o observador; err=%v vezes=%d", err, vezes)
	}

	// A MEDIÇÃO SEM SEMENTE. `nil` e a anterior sem aviso ⇒ a comparação directa do AOS-502.
	api := aos502Interno(t, n, n.node.EventStore)
	chamador := readerIdentity{principal: govReader, board: govBoard, region: govRegion}
	prova2, causa, _ := api.provarTentativa(context.Background(), chamador, *aos502Vinculo(plano, aos502No_, ger, 2))
	if causa != "" {
		t.Fatalf("a prova tinha de passar; causa=%q", causa)
	}
	n.modeloNaoChama("outra resposta sem chamada", "stop")
	if codigo, corpo := n.pedirTentativa(t, plano, ger, 2); codigo != http.StatusCreated {
		t.Fatalf("tentativa 2: %d %s", codigo, corpo)
	}
	n.esperar(t, id2)
	api.medirPromptDaTentativa(context.Background(), id2, prova2, nil)
	if got := api.tentativas.promptDiferente.Load(); got != 1 {
		t.Fatalf("sem semente a medicao compara os hashes directamente, e a tentativa 2 levou o aviso: contava 1; contou %d", got)
	}
	// `nil` com a anterior COM aviso é um defeito de cablagem: conta, não se lê como «igual».
	prova3, causa, _ := api.provarTentativa(context.Background(), chamador, *aos502Vinculo(plano, aos502No_, ger, 3))
	if causa != "" || prova3.avisoAnterior != agentruntime.RetryNoticeNoFunctionCall {
		t.Fatalf("a prova da tentativa 3 leva o aviso da 2; causa=%q aviso=%q", causa, prova3.avisoAnterior)
	}
	api.medirPromptDaTentativa(context.Background(), id2, prova3, nil)
	if got := api.tentativas.promptDiferente.Load(); got != 2 {
		t.Fatalf("sem semente e com a anterior com aviso a medicao conta; contou %d", got)
	}
}
