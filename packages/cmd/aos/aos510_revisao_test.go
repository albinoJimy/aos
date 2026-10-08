package main

// AOS-510 — O QUE A REVISÃO ADVERSARIAL DE 2026-10-08 PEDIU.
//
// M-1: a cablagem `AOS_RUN_RETRY_EMPTY` → handler não tinha teste — todos os outros montam o
// handler à mão, e apagar o `append` da opção em [serveAPI] deixava a suite verde.
//
// M-3: AS CADEIAS DE CLASSE MISTA são alcançáveis no nó. O corpo de cada tentativa escolhe o
// contrato (precedente do AOS-502), pelo que um chamador consegue 1.ª vazia → 2.ª com contrato
// (`contract_unmet_no_call`) → 3.ª, e o inverso. O `aos-orq` nunca as produz (as elegibilidades
// são disjuntas), mas o nó tem de se portar bem com elas: o tecto de três runs é um só, cada elo
// prova-se na classe da razão dele, o aviso entra só no elo da classe do AOS-502, e a medição do
// `prompt_hash` não conta diferença onde só o aviso mudou.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// TestAOS510_ServeAPI_LigaOInterruptorLidoDoAmbiente: o servidor que o ENTRYPOINT constrói anuncia
// a classe quando a variável está em `on`, e não a anuncia sem ela. É o `GET /tools` do processo,
// e não o de um handler montado pelo teste.
func TestAOS510_ServeAPI_LigaOInterruptorLidoDoAmbiente(t *testing.T) {
	for _, c := range []struct {
		valor   string
		anuncia bool
	}{{"on", true}, {"off", false}, {"", false}} {
		t.Run("AOS_RUN_RETRY_EMPTY="+c.valor, func(t *testing.T) {
			clearIngressEnv(t)
			aos441Ambiente(t, aos441ManifestoDeProducao)
			t.Setenv("AOS_RUN_RETRY_MAX", "2")
			t.Setenv("AOS_RUN_RETRY_EMPTY", c.valor)
			node, _ := newAPINode(t, &countingModel{}, false)
			defer func() { _ = node.Close() }()
			addr := portaLivreLoopback(t)
			ctx, cancel := context.WithCancel(context.Background())
			var out bytes.Buffer
			fim := make(chan error, 1)
			go func() { fim <- serveAPI(ctx, &out, node, addr) }()
			esperarPorta(t, addr)
			resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + addr + "/tools")
			if err != nil {
				t.Fatalf("GET /tools: %v", err)
			}
			corpo, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			cancel()
			select {
			case err := <-fim:
				if err != nil {
					t.Errorf("serveAPI devia encerrar graciosamente, veio %v", err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("serveAPI nao encerrou apos cancelamento do ctx")
			}
			quer := `"run_retry":{"max":2}}`
			if c.anuncia {
				quer = `"run_retry":{"max":2,"empty_output":true}}`
			}
			if resp.StatusCode != http.StatusOK || !strings.HasSuffix(strings.TrimSpace(string(corpo)), quer) {
				t.Fatalf("o servidor de serveAPI tinha de acabar o GET /tools com %s; veio %d %s", quer, resp.StatusCode, corpo)
			}
			if banner := strings.Contains(out.String(), "nova tentativa por resposta vazia (EPIC-19/AOS-510"); banner != c.anuncia {
				t.Fatalf("o banner da classe so se imprime com on; veio banner=%t:\n%s", banner, out.String())
			}
		})
	}
	// Um valor fora do vocabulário recusa o arranque, pelo entrypoint.
	clearIngressEnv(t)
	aos441Ambiente(t, aos441ManifestoDeProducao)
	t.Setenv("AOS_RUN_RETRY_EMPTY", "observe")
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	if err := serveAPI(context.Background(), io.Discard, node, portaLivreLoopback(t)); err == nil || !strings.Contains(err.Error(), "AOS_RUN_RETRY_EMPTY") {
		t.Fatalf("um valor invalido tinha de recusar o arranque com o erro da variavel; veio %v", err)
	}
}

// aos510Serie devolve o valor da série no `/metrics`, ou "<ausente>".
func aos510Serie(m, serie string) string {
	for _, l := range strings.Split(m, "\n") {
		if strings.HasPrefix(l, serie+" ") {
			return strings.TrimSpace(strings.TrimPrefix(l, serie))
		}
	}
	return "<ausente>"
}

// aos510ComContrato é o que o corpo de uma tentativa acrescenta para o run levar contrato.
var aos510ComContrato = map[string]any{"tools": []string{aos502Leitura}, "completion_requires": []string{aos502Leitura}}

// TestAOS510_CadeiaMista_VazioDepoisNoCall: 1.ª vazia (sem contrato) → 2.ª admitida pela classe
// da resposta vazia, com contrato no corpo, fecha `contract_unmet_no_call` → 3.ª admitida pela
// classe do AOS-502. Uma admissão por classe; o aviso só na 3.ª; e não há 4.ª.
func TestAOS510_CadeiaMista_VazioDepoisNoCall(t *testing.T) {
	n := aos510Compor(t, agentruntime.CompletionEnforce, 2, true, true)
	const plano = "plano-510-mista-a"
	ger := n.pedir(t, plano)
	aos510FalharVazio(t, n, plano, ger)
	id2, id3 := idDaTentativa(plano, aos510No_, 2), idDaTentativa(plano, aos510No_, 3)
	if codigo, corpo := aos510Submeter(n, aos510Corpo(n, plano, ger, 2, aos510ComContrato)); codigo != http.StatusCreated {
		t.Fatalf("a tentativa 2 e admitida pela classe da resposta vazia; veio %d %s", codigo, corpo)
	}
	n.esperar(t, id2)
	_, r2 := n.ler(t, id2)
	if o2, cru2 := n.origemDe(t, id2); o2.RetryReason != "empty_output" || o2.RetryNotice != "" {
		t.Fatalf("a tentativa 2 foi admitida por vazio: leva retry_reason e nao leva aviso; veio %s", cru2)
	}
	if r2.OutcomeReason != string(agentruntime.OutcomeContractNoCall) {
		t.Fatalf("a tentativa 2, com contrato, fecha contract_unmet_no_call; veio %+v", r2)
	}
	if codigo, corpo := aos510Submeter(n, aos510Corpo(n, plano, ger, 3, aos510ComContrato)); codigo != http.StatusCreated {
		t.Fatalf("a tentativa 3 e admitida pela classe do AOS-502 sobre uma 2 admitida por vazio; veio %d %s", codigo, corpo)
	}
	n.esperar(t, id3)
	if o3, cru3 := n.origemDe(t, id3); o3.RetryReason != "" || o3.RetryNotice == "" || o3.Attempt != 3 || o3.RetryOf != id2 {
		t.Fatalf("a tentativa 3 e da classe do AOS-502: leva o aviso e nao leva retry_reason; veio %s", cru3)
	}
	m := n.metricas(t)
	if aos510Serie(m, "aos_runs_retry_admitted_total") != "1" || aos510Serie(m, "aos_runs_retry_empty_admitted_total") != "1" || aos510Serie(m, "aos_runs_retry_notice_total") != "1" {
		t.Fatalf("uma admissao por classe, e um so aviso:\n%s", aos502SoRetry(m))
	}
	// O TECTO É UM SÓ: três runs do nó do plano, qualquer que seja a mistura de classes.
	if codigo, _ := aos510Submeter(n, map[string]any{
		"run_id": id3 + "x", "objective": "x", "principal_nhi": durAgent, "credential": n.tok, "tools": []string{}, "inputs": []any{},
		"plan_request": aos502Vinculo(plano, aos510No_, ger, 4),
	}); codigo != http.StatusForbidden {
		t.Fatalf("nao ha tentativa 4; veio %d", codigo)
	}
}

// TestAOS510_CadeiaMista_NoCallDepoisVazio: 1.ª com contrato fecha `contract_unmet_no_call` → 2.ª
// admitida pela classe do AOS-502 (com aviso), sem contrato no corpo, responde vazio → 3.ª. Com o
// interruptor DESLIGADO a 3.ª é recusada como sempre, na série do AOS-502 e sem séries novas;
// LIGADO é admitida por vazio, sem aviso, e a medição do `prompt_hash` não conta diferença.
func TestAOS510_CadeiaMista_NoCallDepoisVazio(t *testing.T) {
	for _, vazia := range []bool{false, true} {
		nome := "InterruptorDesligado"
		if vazia {
			nome = "InterruptorLigado"
		}
		t.Run(nome, func(t *testing.T) {
			n := aos510Compor(t, agentruntime.CompletionEnforce, 2, vazia, true)
			const plano = "plano-510-mista-b"
			ger := n.pedir(t, plano)
			n.modeloNaoChama("", "stop")
			if r := aos510Primeira(t, n, plano, ger, aos510ComContrato); r.OutcomeReason != string(agentruntime.OutcomeContractNoCall) {
				t.Fatalf("pre-condicao: a primeira, com contrato, fecha contract_unmet_no_call; veio %+v", r)
			}
			id2, id3 := idDaTentativa(plano, aos510No_, 2), idDaTentativa(plano, aos510No_, 3)
			if codigo, corpo := aos510PedirTentativa(n, plano, ger, 2); codigo != http.StatusCreated {
				t.Fatalf("a tentativa 2 e admitida pela classe do AOS-502; veio %d %s", codigo, corpo)
			}
			n.esperar(t, id2)
			_, r2 := n.ler(t, id2)
			if o2, cru2 := n.origemDe(t, id2); r2.OutcomeReason != string(agentruntime.OutcomeEmptyOutput) || o2.RetryNotice == "" || o2.RetryReason != "" {
				t.Fatalf("a tentativa 2 leva o aviso, nao leva retry_reason e responde vazio; veio %+v %s", r2, cru2)
			}
			time.Sleep(400 * time.Millisecond)
			antes := aos510Serie(n.metricas(t), "aos_runs_retry_prompt_hash_diferente_total")
			n.modeloNaoChama("Resumo.", "stop")
			codigo, corpo := aos510PedirTentativa(n, plano, ger, 3)
			if !vazia {
				if codigo != http.StatusForbidden || n.existe(t, id3) {
					t.Fatalf("com o interruptor desligado a tentativa 3 sobre uma 2 empty_output e recusada; veio %d %s", codigo, corpo)
				}
				m := n.metricas(t)
				if aos510Serie(m, `aos_runs_retry_refused_total{causa="`+causaRetryOutraRazao+`"}`) != "1" || strings.Contains(m, "aos_runs_retry_empty_") {
					t.Fatalf("a recusa conta na serie do AOS-502, e nao ha series novas:\n%s", aos502SoRetry(m))
				}
				return
			}
			if codigo != http.StatusCreated {
				t.Fatalf("a tentativa 3 e admitida por vazio sobre uma 2 da classe do AOS-502; veio %d %s", codigo, corpo)
			}
			n.esperar(t, id3)
			if o3, cru3 := n.origemDe(t, id3); o3.RetryReason != "empty_output" || o3.RetryNotice != "" {
				t.Fatalf("a tentativa 3 e da classe da resposta vazia: sem aviso; veio %s", cru3)
			}
			pedidos, crus := aos506Pedidos(t, n)
			ultimo := len(pedidos) - 1
			if aos506LevaAviso(pedidos[ultimo]) || strings.Contains(string(crus[ultimo]), "previous_attempt") {
				t.Fatalf("o pedido da tentativa 3 (por vazio) leva o aviso:\n%s", crus[ultimo])
			}
			if !aos506LevaAviso(pedidos[ultimo-1]) {
				t.Fatal("controlo: a tentativa 2 (classe do AOS-502) levava o aviso")
			}
			// A tentativa 3 repete o pedido da 2 SEM o aviso: a única diferença é o aviso, e a
			// medição não a conta.
			time.Sleep(600 * time.Millisecond)
			if depois := aos510Serie(n.metricas(t), "aos_runs_retry_prompt_hash_diferente_total"); antes != depois {
				t.Fatalf("a medicao do prompt_hash nao pode contar diferenca onde so o aviso mudou (antes=%s depois=%s)", antes, depois)
			}
		})
	}
}
