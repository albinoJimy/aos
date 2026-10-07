package main

// AOS-506 — O AVISO NA NOVA TENTATIVA, E A PROJECÇÃO 1.2.0 NO NÓ COMPOSTO (emendas ao ADR-039
// §2.7 e ao ADR-036 §2.4).
//
// Os testes correm pela API HTTP do nó composto dos testes do AOS-502: gate soberano, fila de
// pedidos de plano, mediação real, execução durável e o adaptador real do Model Gateway contra um
// provider falso que guarda o corpo de cada pedido. O que se compara são esses corpos — o que o
// provider recebe.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// aos506AvisoNoPedido é o segmento do aviso tal como chega ao provider na projecção 1.0.0,
// escrito à mão (o golden do kernel).
const aos506AvisoNoPedido = "<notice taint=trusted about=previous_attempt>\n" +
	"An earlier attempt at this task ended with a reply that made no function call, so no tool ran, and it failed because a tool it had to use was never called. This is a new attempt. The only way to use a tool is a function call made through the function-calling interface of this API. The runtime does not read a tool request written as text in a reply, in any notation, and nothing runs from it.\n"

// aos506RespostaAnterior é o que o modelo responde na tentativa que falha: uma tool call escrita
// como texto (a forma medida em produção a 2026-10-07), com um marcador que não existe em mais
// lado nenhum. Nenhum byte dela pode aparecer no pedido da tentativa seguinte.
const aos506RespostaAnterior = `MARCADOR-506-DO-RUN-ANTERIOR <functions.doc_read:0>{"doc_id": "notes"}</functions.doc_read>`

// aos506Compor levanta o nó do AOS-502 com o tecto dado, o aviso ligado ou não, e a versão da
// projecção nativa dada ("" ⇒ a variável fica vazia: a omissão).
func aos506Compor(t *testing.T, tecto int, aviso bool, versao string) *aos502No {
	t.Helper()
	t.Setenv("AOS_MODEL_PROJECTION_VERSION", versao)
	n := aos502Compor(t, agentruntime.CompletionEnforce, tecto)
	cat, err := catalogoDeToolsDoAmbiente()
	if err != nil {
		t.Fatalf("catalogoDeToolsDoAmbiente: %v", err)
	}
	opts := []APIOption{WithToolCatalog(cat)}
	if tecto > 0 {
		opts = append(opts, WithRunRetryMax(tecto))
	}
	if aviso {
		opts = append(opts, WithRunRetryNotice())
	}
	if n.h, err = NewAPIHandler(n.svc, n.node, opts...); err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	return n
}

// aos506Mensagem é uma mensagem do pedido ao provider.
type aos506Mensagem struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// pedidos devolve as mensagens de cada pedido que o provider recebeu, pela ordem.
func aos506Pedidos(t *testing.T, n *aos502No) ([][]aos506Mensagem, [][]byte) {
	t.Helper()
	n.upstream.mu.Lock()
	corpos := append([][]byte(nil), n.upstream.corpos...)
	n.upstream.mu.Unlock()
	var out [][]aos506Mensagem
	for i, corpo := range corpos {
		var wire struct {
			Messages []aos506Mensagem `json:"messages"`
		}
		if err := json.Unmarshal(corpo, &wire); err != nil {
			t.Fatalf("pedido %d ao provider ilegivel: %v", i, err)
		}
		out = append(out, wire.Messages)
	}
	return out, corpos
}

// aos506LevaAviso diz se alguma mensagem do pedido leva o segmento do aviso. Procura-se no
// CONTEÚDO DESCODIFICADO das mensagens, e não no corpo JSON cru: o wire escapa `<` como `\u003c`,
// pelo que `<notice` nunca é substring do corpo — a verificação negativa feita sobre ele passava
// com qualquer pedido (revisão do AOS-506, M-1).
func aos506LevaAviso(pedido []aos506Mensagem) bool {
	for _, m := range pedido {
		if strings.Contains(m.Content, "<notice") || strings.Contains(m.Content, "previous_attempt") {
			return true
		}
	}
	return false
}

// aos506PrimeiroTurno lê o primeiro `turn.recorded` de um run, na forma que a prova usa.
func aos506PrimeiroTurno(t *testing.T, n *aos502No, runID string) turnoDaProva {
	t.Helper()
	evs, err := n.node.EventStore.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("ler o stream de %s: %v", runID, err)
	}
	for _, ev := range evs {
		if ev.Type == agentruntime.EventTypeTurnRecorded {
			var turno turnoDaProva
			if err := json.Unmarshal(ev.Payload, &turno); err != nil {
				t.Fatalf("turn.recorded ilegivel: %v", err)
			}
			return turno
		}
	}
	t.Fatalf("o run %s nao tem turn.recorded", runID)
	return turnoDaProva{}
}

// aos506FalharSemChamar corre a primeira tentativa com a resposta do marcador.
func aos506FalharSemChamar(t *testing.T, n *aos502No, plano string, ger int) {
	t.Helper()
	n.modeloNaoChama(aos506RespostaAnterior, "stop")
	if r := n.primeira(t, plano, ger); r.Status != "failed" || r.OutcomeReason != string(agentruntime.OutcomeContractNoCall) {
		t.Fatalf("pre-condicao: a primeira tentativa fecha failed por contract_unmet_no_call; veio %+v", r)
	}
}

// COM `off` — A OMISSÃO —, A TENTATIVA É A DE HOJE, BYTE A BYTE. O pedido que o provider recebe
// na tentativa 2 é o da primeira; o `prompt_hash` é o mesmo; o `run.plan_origin` não tem campo
// novo; e o `/metrics` não tem série nova nem outro texto de ajuda.
func TestAOS506_Off_ATentativaEADeHoje(t *testing.T) {
	n := aos506Compor(t, 2, false, "")
	const plano = "plano-506-off"
	ger := n.pedir(t, plano)
	aos506FalharSemChamar(t, n, plano, ger)
	n.modeloChama(aos502Leitura)
	if codigo, corpo := n.pedirTentativa(t, plano, ger, 2); codigo != http.StatusCreated {
		t.Fatalf("a tentativa 2 tinha de ser admitida; veio %d %s", codigo, corpo)
	}
	id1, id2 := idDaTentativa(plano, aos502No_, 1), idDaTentativa(plano, aos502No_, 2)
	n.esperar(t, id2)

	_, corpos := aos506Pedidos(t, n)
	if len(corpos) < 2 || string(corpos[1]) != string(corpos[0]) {
		t.Fatalf("com o aviso desligado, o primeiro pedido da tentativa 2 e o da primeira, byte a byte:\n 1: %s\n 2: %s", corpos[0], corpos[1])
	}
	if pedidos, _ := aos506Pedidos(t, n); aos506LevaAviso(pedidos[1]) {
		t.Fatalf("com o aviso desligado a tentativa nao leva aviso: %s", corpos[1])
	}
	if a, b := aos506PrimeiroTurno(t, n, id1), aos506PrimeiroTurno(t, n, id2); a.Manifest.PromptHash != b.Manifest.PromptHash {
		t.Fatal("com o aviso desligado o prompt_hash da tentativa e o da anterior")
	}
	_, cru := n.origemDe(t, id2)
	if quer := `{"v":"1.0","plan_request":{"stream":"` + planRequestStream + `","run_id":"` + plano + `","generation":1},"plan_id":"` + aos502PlanID + `","node_id":"` + aos502No_ + `","attempt":2,"retry_of":"` + id1 + `"}`; string(cru) != quer {
		t.Fatalf("o run.plan_origin de uma tentativa sem aviso tem os bytes de antes:\n quer: %s\n veio: %s", quer, cru)
	}
	time.Sleep(300 * time.Millisecond)
	m := n.metricas(t)
	if strings.Contains(m, "aos_runs_retry_notice_total") || strings.Contains(m, "AOS-506") {
		t.Fatalf("com o aviso desligado o /metrics nao ganha nada:\n%s", aos502SoRetry(m))
	}
	if !aos502TemSerie(m, "aos_runs_retry_prompt_hash_diferente_total", 0) || !strings.Contains(m, "NAO teve o hash do da tentativa anterior (AOS-502). TEM DE SER ZERO: a tentativa repete o mesmo pedido.") {
		t.Fatalf("a serie do hash e o seu texto de ajuda sao os de antes:\n%s", m)
	}
	// O registo de retoma de um run sem aviso não tem o campo.
	rec, err := resumeRecordFromGoal(agentruntime.Goal{RunID: id2, CompletionMode: agentruntime.CompletionEnforce})
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(rec); strings.Contains(string(raw), "RetryNotice") {
		t.Fatalf("o registo de retoma de um run sem aviso tem os bytes de sempre: %s", raw)
	}
}

// COM `on`, A TENTATIVA ADMITIDA LEVA O AVISO, E SÓ ELE. O pedido da tentativa é o da primeira
// com o segmento do golden a seguir ao objectivo; a mensagem `system` é a mesma; nenhum byte da
// resposta do run anterior lá está; a primeira tentativa não o leva; a terceira leva-o uma vez.
func TestAOS506_On_ATentativaLevaOAvisoConstante(t *testing.T) {
	n := aos506Compor(t, 2, true, "")
	const plano = "plano-506-on"
	ger := n.pedir(t, plano)
	aos506FalharSemChamar(t, n, plano, ger)
	// A tentativa 2 volta a falhar, com OUTRA resposta; a 3 chama a tool.
	n.modeloNaoChama("SEGUNDA-RESPOSTA-506 <invoke name=\"doc_read\"></invoke>", "stop")
	if codigo, corpo := n.pedirTentativa(t, plano, ger, 2); codigo != http.StatusCreated {
		t.Fatalf("a tentativa 2 tinha de ser admitida; veio %d %s", codigo, corpo)
	}
	id1, id2, id3 := idDaTentativa(plano, aos502No_, 1), idDaTentativa(plano, aos502No_, 2), idDaTentativa(plano, aos502No_, 3)
	n.esperar(t, id2)
	n.modeloChama(aos502Leitura)
	if codigo, corpo := n.pedirTentativa(t, plano, ger, 3); codigo != http.StatusCreated {
		t.Fatalf("a tentativa 3 tinha de ser admitida sobre a 2 (a prova nao muda com o aviso); veio %d %s", codigo, corpo)
	}
	n.esperar(t, id3)
	if _, r := n.ler(t, id3); r.Status != "completed" {
		t.Fatalf("a tentativa 3 chama a tool e conclui; veio %+v", r)
	}

	pedidos, corpos := aos506Pedidos(t, n)
	if len(pedidos) < 3 {
		t.Fatalf("o provider tinha de receber pelo menos tres pedidos; recebeu %d", len(pedidos))
	}
	p1, p2, p3 := pedidos[0], pedidos[1], pedidos[2]
	if len(p1) != 2 || len(p2) != 2 || len(p3) != 2 || p1[0].Role != "system" || p1[1].Role != "user" {
		t.Fatalf("o primeiro pedido de cada run e system + user; vieram %d, %d e %d mensagens", len(p1), len(p2), len(p3))
	}
	if aos506LevaAviso(p1) {
		t.Fatalf("a PRIMEIRA tentativa nao leva aviso, mesmo com o interruptor ligado: %s", corpos[0])
	}
	for i, p := range [][]aos506Mensagem{p2, p3} {
		if p[0] != p1[0] {
			t.Fatalf("tentativa %d: a mensagem system tinha de ser a da primeira", i+2)
		}
		if quer := p1[1].Content + aos506AvisoNoPedido; p[1].Content != quer {
			t.Fatalf("tentativa %d: a mensagem da semente e a da primeira seguida do aviso do golden, e mais nada:\n veio:  %q\n quero: %q", i+2, p[1].Content, quer)
		}
	}
	// NENHUM BYTE DO RUN ANTERIOR: nem o marcador, nem a notação que o modelo escreveu.
	for i, corpo := range corpos[1:3] {
		for _, eco := range []string{"MARCADOR-506", "SEGUNDA-RESPOSTA-506", "functions.doc_read", "invoke", "doc_id"} {
			if strings.Contains(string(corpo), eco) {
				t.Fatalf("o pedido da tentativa %d leva %q, que so existe na resposta do run anterior: %s", i+2, eco, corpo)
			}
		}
	}

	// O LOG: a origem das tentativas diz que levaram o aviso; a da primeira tem os bytes de sempre.
	for _, id := range []string{id2, id3} {
		if o, _ := n.origemDe(t, id); o.RetryNotice != "no_function_call" {
			t.Fatalf("o run.plan_origin de %s tinha de levar retry_notice=no_function_call; veio %+v", id, o)
		}
	}
	if _, cru := n.origemDe(t, id1); strings.Contains(string(cru), "retry_notice") {
		t.Fatalf("a origem da primeira tentativa nao tem o campo: %s", cru)
	}

	// A MEDIÇÃO: o hash da tentativa difere do anterior DE PROPÓSITO, e a série fica a zero —
	// porque a única diferença é o aviso.
	t1, t2, t3 := aos506PrimeiroTurno(t, n, id1), aos506PrimeiroTurno(t, n, id2), aos506PrimeiroTurno(t, n, id3)
	if t1.Manifest.PromptHash == t2.Manifest.PromptHash || t2.Manifest.PromptHash != t3.Manifest.PromptHash {
		t.Fatal("o prompt_hash da tentativa com aviso difere do da primeira, e e o mesmo nas tentativas 2 e 3")
	}
	// A tentativa 2 compara-se com a primeira (sem aviso); a 3 com a 2, que JÁ o levava.
	semente := sementeDaTentativa{objective: "Le o documento notes", aviso: agentruntime.RetryNoticeNoFunctionCall}
	if promptDaTentativaDifere(semente, agentruntime.RetryNoticeNone, t1.Manifest.PromptHash, t2) ||
		promptDaTentativaDifere(semente, agentruntime.RetryNoticeNoFunctionCall, t2.Manifest.PromptHash, t3) {
		t.Fatal("as duas tentativas cumprem os dois hashes esperados")
	}
	if !promptDaTentativaDifere(semente, agentruntime.RetryNoticeNone, t2.Manifest.PromptHash, t3) {
		t.Fatal("controlo: a tentativa 3 comparada como se a 2 nao tivesse aviso tinha de diferir")
	}
	time.Sleep(400 * time.Millisecond)
	m := n.metricas(t)
	if !aos502TemSerie(m, "aos_runs_retry_notice_total", 2) || !aos502TemSerie(m, "aos_runs_retry_admitted_total", 2) {
		t.Fatalf("duas tentativas admitidas, as duas com aviso:\n%s", aos502SoRetry(m))
	}
	if !aos502TemSerie(m, "aos_runs_retry_prompt_hash_diferente_total", 0) {
		t.Fatalf("a unica diferenca das tentativas e o aviso: a serie do hash tem de ficar a zero:\n%s", aos502SoRetry(m))
	}
	if execs := *n.execs[aos502Leitura]; execs != 1 {
		t.Fatalf("a tool correu uma vez em tres runs; correu %d", execs)
	}
}

// A PROVA DIZ À MEDIÇÃO COM QUE AVISO A ANTERIOR FOI SEMEADA, lido do `run.plan_origin` que só o
// nó escreve: vazio para a primeira tentativa, o valor declarado para uma tentativa com aviso.
func TestAOS506_Medicao_AProvaLevaOAvisoDaAnterior(t *testing.T) {
	n := aos506Compor(t, 2, true, "")
	const plano = "plano-506-terceira"
	ger := n.pedir(t, plano)
	aos506FalharSemChamar(t, n, plano, ger)
	api := aos502Interno(t, n, n.node.EventStore)
	chamador := readerIdentity{principal: govReader, board: govBoard, region: govRegion}
	prova2, causa, _ := api.provarTentativa(context.Background(), chamador, *aos502Vinculo(plano, aos502No_, ger, 2))
	if causa != "" || prova2.avisoAnterior != agentruntime.RetryNoticeNone {
		t.Fatalf("a anterior da tentativa 2 e a primeira, sem aviso; veio causa=%q aviso=%q", causa, prova2.avisoAnterior)
	}
	if codigo, corpo := n.pedirTentativa(t, plano, ger, 2); codigo != http.StatusCreated {
		t.Fatalf("a tentativa 2: %d %s", codigo, corpo)
	}
	n.esperar(t, idDaTentativa(plano, aos502No_, 2))
	prova3, causa, _ := api.provarTentativa(context.Background(), chamador, *aos502Vinculo(plano, aos502No_, ger, 3))
	t2 := aos506PrimeiroTurno(t, n, idDaTentativa(plano, aos502No_, 2))
	if causa != "" || prova3.avisoAnterior != agentruntime.RetryNoticeNoFunctionCall || prova3.promptHash != t2.Manifest.PromptHash {
		t.Fatalf("a prova da tentativa 3 leva o aviso e o hash da tentativa 2; veio causa=%q aviso=%q", causa, prova3.avisoAnterior)
	}
	// Uma origem que não foi o nó a escrever não conta.
	evs, _ := n.node.EventStore.Read(context.Background(), idDaTentativa(plano, aos502No_, 2), 1)
	for i := range evs {
		if evs[i].Type == EventTypeRunPlanOrigin {
			evs[i].Producer.NHIID = "nhi:outro"
		}
	}
	if got := avisoDaOrigem(evs); got != agentruntime.RetryNoticeNone {
		t.Fatalf("avisoDaOrigem leu uma origem que o no nao escreveu: %q", got)
	}
}

// O AVISO SÓ ENTRA NUMA TENTATIVA QUE O NÓ PROVOU. Um run que não é tentativa, um `POST /runs`
// directo e uma tentativa recusada nunca o levam, com o interruptor ligado; e quem pede não tem
// campo por onde o pedir nem por onde lhe escrever.
func TestAOS506_On_SoNumaTentativaProvada(t *testing.T) {
	// A decisão, isolada: é a conjunção do interruptor e da prova, e devolve constantes.
	ligado, desligado := &apiHandler{cfg: apiConfig{runRetryMax: 2, runRetryNotice: true}}, &apiHandler{cfg: apiConfig{runRetryMax: 2}}
	prova := &provaDaTentativa{anterior: "p~n", promptHash: "sha256:x"}
	for nome, c := range map[string]struct {
		h     *apiHandler
		prova *provaDaTentativa
		quer  agentruntime.RetryNotice
	}{
		"ligado, com prova":    {ligado, prova, agentruntime.RetryNoticeNoFunctionCall},
		"ligado, sem prova":    {ligado, nil, agentruntime.RetryNoticeNone},
		"desligado, com prova": {desligado, prova, agentruntime.RetryNoticeNone},
		"desligado, sem prova": {desligado, nil, agentruntime.RetryNoticeNone},
	} {
		if got := c.h.avisoDaTentativa(c.prova); got != c.quer {
			t.Fatalf("%s: avisoDaTentativa = %q, queria %q", nome, got, c.quer)
		}
	}

	n := aos506Compor(t, 2, true, "")
	const plano = "plano-506-so-provada"
	ger := n.pedir(t, plano)
	// Um `POST /runs` DIRECTO, sem vínculo ao pedido: corre, e não leva aviso.
	n.modeloNaoChama(aos506RespostaAnterior, "stop")
	if codigo, corpo := n.submeter(t, "run-506-directo", nil, []string{aos502Leitura}, []string{aos502Leitura}); codigo != http.StatusCreated {
		t.Fatalf("POST /runs directo: %d %s", codigo, corpo)
	}
	n.esperar(t, "run-506-directo")
	// A primeira tentativa do nó do plano, que CHAMA a tool e falha por outra razão não existe
	// aqui: usa-se a que conclui. A tentativa 2 sobre ela é recusada, e não hospeda nada.
	n.modeloChama(aos502Leitura)
	if r := n.primeira(t, plano, ger); r.Status != "completed" {
		t.Fatalf("pre-condicao: a primeira tentativa conclui; veio %+v", r)
	}
	antes, _ := aos506Pedidos(t, n)
	if codigo, _ := n.pedirTentativa(t, plano, ger, 2); codigo != http.StatusForbidden {
		t.Fatalf("a tentativa sobre um run que concluiu e recusada; veio %d", codigo)
	}
	depois, corpos := aos506Pedidos(t, n)
	if len(depois) != len(antes) {
		t.Fatal("uma tentativa recusada nao chega ao modelo")
	}
	for i, pedido := range depois {
		if aos506LevaAviso(pedido) {
			t.Fatalf("o pedido %d leva um aviso e nenhum destes runs e uma tentativa provada: %s", i, corpos[i])
		}
	}
	if m := n.metricas(t); !aos502TemSerie(m, "aos_runs_retry_notice_total", 0) {
		t.Fatalf("nenhuma tentativa com aviso:\n%s", aos502SoRetry(m))
	}

	// QUEM PEDE NÃO TEM CAMPO NENHUM. Um corpo que traga `retry_notice` (no topo ou no vínculo) é
	// recusado como qualquer campo desconhecido: o `POST /runs` não ganhou superfície.
	for nome, corpo := range map[string]map[string]any{
		"no topo": {"run_id": "run-506-campo", "objective": "x", "principal_nhi": durAgent, "credential": n.tok, "tools": []string{aos502Leitura}, "retry_notice": "no_function_call"},
		"no vinculo": {"run_id": idDaTentativa(plano, aos502No_, 2), "objective": "x", "principal_nhi": durAgent, "credential": n.tok, "tools": []string{aos502Leitura},
			"plan_request": map[string]any{"run_id": plano, "generation": ger, "plan_id": aos502PlanID, "node_id": aos502No_, "attempt": 2, "retry_notice": "o texto que eu quiser"}},
	} {
		if r := postReq(n.h, "/runs", corpo, govHeaders()); r.Code < 400 || r.Code >= 500 {
			t.Fatalf("%s: um corpo com retry_notice tinha de ser recusado (4xx); veio %d %s", nome, r.Code, r.Body.String())
		}
	}
	if n.existe(t, "run-506-campo") {
		t.Fatal("um corpo com retry_notice nao hospeda nada")
	}
}

// A MEDIÇÃO DO HASH CONTINUA A DETECTAR QUALQUER OUTRA DIFERENÇA, com o aviso ligado. Pela API:
// uma tentativa com outro objectivo conta. E pela função, um facto de cada vez: outro objectivo,
// outras entradas, outro system, outras tools, outro layout e um hash anterior que não é o do
// pedido — todos diferem; a semente certa não.
func TestAOS506_On_AMedicaoDetectaOutraDiferenca(t *testing.T) {
	n := aos506Compor(t, 2, true, "")
	const plano = "plano-506-medicao"
	ger := n.pedir(t, plano)
	aos506FalharSemChamar(t, n, plano, ger)
	id1, id2 := idDaTentativa(plano, aos502No_, 1), idDaTentativa(plano, aos502No_, 2)
	corpo := map[string]any{
		"run_id": id2, "objective": "Um objectivo que nao e o da primeira tentativa", "principal_nhi": durAgent, "credential": n.tok,
		"tools": []string{aos502Leitura}, "inputs": []any{}, "completion_requires": []string{aos502Leitura},
		"plan_request": aos502Vinculo(plano, aos502No_, ger, 2),
	}
	if r := postReq(n.h, "/runs", corpo, govHeaders()); r.Code != http.StatusCreated {
		t.Fatalf("a tentativa e hospedada mesmo com outro objectivo; veio %d %s", r.Code, r.Body.String())
	}
	n.esperar(t, id2)
	prazo := time.Now().Add(10 * time.Second)
	for !aos502TemSerie(n.metricas(t), "aos_runs_retry_prompt_hash_diferente_total", 1) {
		if time.Now().After(prazo) {
			t.Fatalf("com o aviso ligado, um objectivo diferente do da tentativa anterior tinha de contar:\n%s", aos502SoRetry(n.metricas(t)))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if m := n.metricas(t); !strings.Contains(m, "NAO teve o hash ESPERADO (AOS-502, AOS-506)") {
		t.Fatal("com o aviso ligado o texto de ajuda da serie diz que compara com o hash esperado")
	}

	// A FUNÇÃO, sobre os turnos reais: `t1` é o da primeira tentativa, `t2` o da tentativa com
	// aviso e outro objectivo.
	t1, t2 := aos506PrimeiroTurno(t, n, id1), aos506PrimeiroTurno(t, n, id2)
	certa := sementeDaTentativa{objective: "Um objectivo que nao e o da primeira tentativa", aviso: agentruntime.RetryNoticeNoFunctionCall}
	if h, err := hashDaSementeCom(certa, certa.aviso, t2); err != nil || h != t2.Manifest.PromptHash {
		t.Fatalf("controlo: com a semente que o run levou, o hash com aviso e o que o run gravou: err=%v", err)
	}
	// Controlo positivo: a semente do pedido da primeira tentativa dá, sem aviso, o hash dela.
	daPrimeira := sementeDaTentativa{objective: "Le o documento notes", aviso: agentruntime.RetryNoticeNoFunctionCall}
	semAviso, err := hashDaSementeCom(daPrimeira, agentruntime.RetryNoticeNone, t1)
	if err != nil || semAviso != t1.Manifest.PromptHash {
		t.Fatalf("o recalculo do prompt SEM aviso tinha de dar o hash que a primeira tentativa gravou: err=%v", err)
	}
	comAviso, err := hashDaSementeCom(daPrimeira, daPrimeira.aviso, t1)
	if err != nil || comAviso == semAviso {
		t.Fatalf("o prompt com aviso tem outro hash: err=%v", err)
	}
	igual := turnoDaProva{}
	igual.Manifest = t1.Manifest
	igual.Manifest.PromptHash = comAviso
	if promptDaTentativaDifere(daPrimeira, agentruntime.RetryNoticeNone, t1.Manifest.PromptHash, igual) {
		t.Fatal("controlo: o pedido repetido mais o aviso NAO difere")
	}
	entrada := []agentruntime.PlanInput{{From: "n0", Output: "doc", Content: []byte("dados")}}
	for nome, muda := range map[string]func(*sementeDaTentativa, *turnoDaProva, *string){
		"outro objectivo": func(s *sementeDaTentativa, _ *turnoDaProva, _ *string) { s.objective += "." },
		"outras entradas": func(s *sementeDaTentativa, _ *turnoDaProva, _ *string) { s.inputs = entrada },
		"outro system":    func(s *sementeDaTentativa, _ *turnoDaProva, _ *string) { s.system = "outro" },
		"memoria a mais":  func(s *sementeDaTentativa, _ *turnoDaProva, _ *string) { s.memory = []byte("m") },
		"outras tools":    func(_ *sementeDaTentativa, tr *turnoDaProva, _ *string) { tr.Manifest.Tools = nil },
		"outro layout": func(_ *sementeDaTentativa, tr *turnoDaProva, _ *string) {
			tr.Manifest.AssemblyVersion = agentruntime.AssemblyVersion130
		},
		"layout ilegivel":    func(_ *sementeDaTentativa, tr *turnoDaProva, _ *string) { tr.Manifest.AssemblyVersion = "9.9.9" },
		"aviso desconhecido": func(s *sementeDaTentativa, _ *turnoDaProva, _ *string) { s.aviso = "outro" },
		"o run gravou outro hash": func(_ *sementeDaTentativa, tr *turnoDaProva, _ *string) {
			tr.Manifest.PromptHash = t1.Manifest.PromptHash
		},
		"o anterior tinha outro hash": func(_ *sementeDaTentativa, _ *turnoDaProva, anterior *string) { *anterior = comAviso },
	} {
		s, tr, anterior := daPrimeira, igual, t1.Manifest.PromptHash
		muda(&s, &tr, &anterior)
		if !promptDaTentativaDifere(s, agentruntime.RetryNoticeNone, anterior, tr) {
			t.Fatalf("%s: a medicao tinha de contar a diferenca", nome)
		}
	}
	// Sem aviso a regra é a de sempre: o hash da tentativa é o da anterior, e mais nada se calcula.
	semAvisoNenhum := sementeDaTentativa{objective: "qualquer"}
	if promptDaTentativaDifere(semAvisoNenhum, agentruntime.RetryNoticeNone, t1.Manifest.PromptHash, t1) ||
		!promptDaTentativaDifere(semAvisoNenhum, agentruntime.RetryNoticeNone, t1.Manifest.PromptHash, t2) {
		t.Fatal("sem aviso a medicao compara os dois hashes directamente")
	}
}

// O AVISO SOBREVIVE À RETOMA: o registo de retoma leva-o, e o Goal reconstruído semeia o tail
// com o mesmo aviso — a retoma reproduz o turno 1 com os mesmos bytes.
func TestAOS506_RegistoDeRetoma_LevaOAviso(t *testing.T) {
	goal := agentruntime.Goal{
		RunID: "plano~read_notes~2", Objective: "Le o documento notes", CompletionMode: agentruntime.CompletionEnforce,
		RetryNotice: agentruntime.RetryNoticeNoFunctionCall,
	}
	rec, err := resumeRecordFromGoal(goal)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var lido integration.ResumeRecord
	if err := json.Unmarshal(raw, &lido); err != nil {
		t.Fatal(err)
	}
	retomado := lido.GoalWith("credencial-fresca")
	if retomado.RetryNotice != agentruntime.RetryNoticeNoFunctionCall {
		t.Fatalf("o Goal de retoma perdeu o aviso: %q", retomado.RetryNotice)
	}
	a, err := agentruntime.SeedTail(agentruntime.AssemblyVersion140, fixarLayout(goal))
	if err != nil {
		t.Fatal(err)
	}
	b, err := agentruntime.SeedTail(retomado.AssemblyVersion, retomado)
	if err != nil {
		t.Fatal(err)
	}
	asm := agentruntime.NewPromptAssembler("", nil)
	if ha, hb := asm.Assemble(1, a).PromptHash, asm.Assemble(1, b).PromptHash; ha != hb || len(b) != 2 {
		t.Fatalf("a semente da retoma tinha de ser a do run (objectivo + aviso): %d segmentos, %s contra %s", len(b), ha, hb)
	}
}

// A VARIÁVEL É DE VOCABULÁRIO FECHADO: vazia e `off` são a omissão, `on` liga, e qualquer outro
// valor não deixa o nó arrancar. O banner só sai com `on`, e diz que não tem efeito com o tecto
// a zero.
func TestAOS506_Env_VocabularioFechadoEBanner(t *testing.T) {
	for valor, quer := range map[string]bool{"": false, "off": false, " off ": false, "on": true, " on ": true} {
		t.Setenv("AOS_RUN_RETRY_NOTICE", valor)
		opt, ligado, err := apiRunRetryNoticeOptionFromEnv()
		if err != nil || ligado != quer || (opt != nil) != quer {
			t.Fatalf("AOS_RUN_RETRY_NOTICE=%q: queria ligado=%t; veio ligado=%t opt=%t err=%v", valor, quer, ligado, opt != nil, err)
		}
		if opt != nil {
			var cfg apiConfig
			opt(&cfg)
			if !cfg.runRetryNotice {
				t.Fatal("a opcao tinha de ligar o aviso")
			}
		}
	}
	for _, mau := range []string{"On", "ON", "1", "true", "yes", "observe", "enforce", "on,off", "0", "ligado"} {
		t.Setenv("AOS_RUN_RETRY_NOTICE", mau)
		if opt, ligado, err := apiRunRetryNoticeOptionFromEnv(); !errors.Is(err, ErrBadRunRetryNotice) || ligado || opt != nil {
			t.Fatalf("AOS_RUN_RETRY_NOTICE=%q tinha de recusar o arranque; veio ligado=%t err=%v", mau, ligado, err)
		}
	}
	semEfeito, ligado := runRetryNoticeBanner(0), runRetryNoticeBanner(2)
	if !strings.Contains(semEfeito, "SEM EFEITO") || !strings.Contains(semEfeito, "AOS_RUN_RETRY_MAX") {
		t.Fatalf("com o tecto a zero o banner diz que o aviso nao tem efeito: %s", semEfeito)
	}
	if !strings.Contains(ligado, "LIGADO") || strings.Contains(ligado, "SEM EFEITO") || !strings.Contains(ligado, "AOS_RUN_RETRY_NOTICE=on") {
		t.Fatalf("banner com o aviso ligado: %s", ligado)
	}
	// O banner não mostra o texto do aviso nem exemplo nenhum de chamada.
	for _, b := range []string{semEfeito, ligado} {
		if strings.ContainsAny(b, "<>{}") {
			t.Fatalf("o banner nao leva marcacao: %s", b)
		}
	}
}

// A PROJECÇÃO 1.2.0 NO NÓ. A variável aceita `1.2.0`; o provider recebe o protocolo da 1.2.0 na
// mensagem `system` e a mensagem da semente da 1.1.0; o turno declara `native/1.2.0`; e o
// `prompt_hash` é o do mesmo run na omissão. Com aviso e 1.2.0, o aviso fecha com a sua linha de
// fim, como qualquer segmento.
func TestAOS506_No_Projeccao120(t *testing.T) {
	t.Setenv("AOS_MODEL_PROJECTION_VERSION", "1.2.0")
	if got, err := parseModelProjectionVersionFromEnv(); err != nil || got != "1.2.0" {
		t.Fatalf("AOS_MODEL_PROJECTION_VERSION=1.2.0: veio (%q, %v)", got, err)
	}
	linhas := modelProjectionBannerFor(true, "native", "1.2.0")
	if len(linhas) != 2 || !strings.Contains(linhas[0], "versao 1.2.0") || !strings.Contains(linhas[1], "AOS-506") || !strings.Contains(linhas[1], "NAO interpreta texto do modelo como tool call") {
		t.Fatalf("banner da 1.2.0: %v", linhas)
	}
	if a, b := modelProjectionBannerFor(true, "native", "1.1.0"), modelProjectionBannerFor(true, "native", "1.0.0"); len(a) != 2 || len(b) != 1 || strings.Contains(a[1], "AOS-506") {
		t.Fatalf("os banners da 1.0.0 e da 1.1.0 sao os de antes: %v / %v", b, a)
	}

	hashes := map[string]string{}
	sementes := map[string]string{}
	for _, versao := range []string{"", "1.1.0", "1.2.0"} {
		n := aos506Compor(t, 2, true, versao)
		plano := "plano-506-v" + strings.ReplaceAll(versao, ".", "")
		ger := n.pedir(t, plano)
		aos506FalharSemChamar(t, n, plano, ger)
		n.modeloChama(aos502Leitura)
		if codigo, corpo := n.pedirTentativa(t, plano, ger, 2); codigo != http.StatusCreated {
			t.Fatalf("%s: a tentativa 2: %d %s", versao, codigo, corpo)
		}
		id2 := idDaTentativa(plano, aos502No_, 2)
		n.esperar(t, id2)
		pedidos, _ := aos506Pedidos(t, n)
		sistema, semente := pedidos[1][0].Content, pedidos[1][1].Content
		hashes[versao] = aos506PrimeiroTurno(t, n, id2).Manifest.PromptHash
		sementes[versao] = semente
		evs, _ := n.node.EventStore.Read(context.Background(), id2, 1)
		manifesto := string(aos486LerTurnos(t, evs)[0].manifesto)
		switch versao {
		case "1.2.0":
			if !strings.Contains(sistema, "- To use a tool, make a function call through the function-calling interface of this API") || strings.Contains(sistema, "tool_call") {
				t.Fatalf("a mensagem system da 1.2.0 leva o protocolo novo: %s", sistema)
			}
			if !strings.Contains(manifesto, `"projection":"native","projection_version":"1.2.0"`) {
				t.Fatalf("o turno tinha de declarar native/1.2.0: %s", manifesto)
			}
			if !strings.HasSuffix(semente, aos506AvisoNoPedido+"</notice>\n") {
				t.Fatalf("na 1.2.0 o aviso fecha com a sua linha de fim: %q", semente)
			}
		case "":
			if strings.Contains(sistema, "function-calling interface") || !strings.HasSuffix(semente, aos506AvisoNoPedido) {
				t.Fatalf("a omissao e a 1.0.0: o protocolo de sempre, e o aviso sem linha de fim: %q", semente)
			}
		}
	}
	if hashes[""] != hashes["1.1.0"] || hashes[""] != hashes["1.2.0"] {
		t.Fatalf("o prompt_hash nao depende da versao da projeccao: %v", hashes)
	}
	if sementes["1.1.0"] != sementes["1.2.0"] || sementes[""] == sementes["1.2.0"] {
		t.Fatal("a mensagem da semente da 1.2.0 e a da 1.1.0, byte a byte, e nao a da 1.0.0")
	}
}
