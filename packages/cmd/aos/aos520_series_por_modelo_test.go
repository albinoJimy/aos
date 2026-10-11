package main

// AOS-520 (PR 2) — AS SÉRIES POR MODELO (ADR-041 §2.8), medidas pelo nó composto.
//
// O que se prende: cada turno, cada resposta recusada e cada run selado conta no modelo PEDIDO — o
// alias do titular ou o do candidato —, nunca no que o fornecedor declara; o rótulo é de conjunto
// fechado e o número de séries tem um tecto; e sem candidato a servir nenhuma das três famílias
// existe. Que as famílias que já existiam não mudam está em `aos520_series_inercia_test.go`.

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// aos520sAmostras devolve as amostras (sem as linhas de comentário) da família dada, por rótulos.
func aos520sAmostras(t *testing.T, metricas, familia string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, linha := range strings.Split(metricas, "\n") {
		if !strings.HasPrefix(linha, familia+"{") {
			continue
		}
		fim := strings.LastIndex(linha, "} ")
		if fim < 0 {
			t.Fatalf("amostra sem valor: %q", linha)
		}
		v, err := strconv.Atoi(linha[fim+2:])
		if err != nil {
			t.Fatalf("amostra com valor nao inteiro: %q", linha)
		}
		out[linha[len(familia):fim+1]] = v
	}
	return out
}

var aos520sRotuloModel = regexp.MustCompile(`model="([^"]*)"`)

// aos520sModelos devolve os valores distintos do rótulo `model` nas três famílias novas.
func aos520sModelos(t *testing.T, metricas string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	novas, _, _ := aos520sSeparar(metricas)
	for _, linha := range novas {
		if strings.HasPrefix(linha, "#") {
			continue
		}
		m := aos520sRotuloModel.FindStringSubmatch(linha)
		if m == nil {
			t.Fatalf("amostra de uma familia por modelo sem o rotulo model: %q", linha)
		}
		out[m[1]] = true
	}
	return out
}

// aos520sRespostaComModeloCru é a resposta do provider a declarar que serviu um modelo que não é
// nenhum dos aliases do nó.
const aos520sModeloCru = "fornecedor/modelo-cru-2026"

func aos520sRespostaComModeloCru(pedeTool bool, tool string) []byte {
	return []byte(strings.Replace(string(aos490RespostaDoProvider(pedeTool, tool)), `"model":"gpt-4o"`, `"model":"`+aos520sModeloCru+`"`, 1))
}

// CADA TURNO E CADA RUN CONTAM NO MODELO PEDIDO. Com `on` e a fatia a 10%, o run "0" é do candidato
// e o run "a" do titular; o provider declara, nos dois, ter servido um terceiro nome. Cada modelo
// fica com os seus dois turnos (um `tool_calls`, um `stop`) e o seu run `complete`; o nome que o
// provider declarou não aparece em rótulo nenhum; e a soma dos modelos, por estado e razão, é a
// família de sempre.
func TestAOS520_Series_CadaTurnoECadaRunContamNoModeloPedido(t *testing.T) {
	n := aos520cNo(t, aos520NovoSubstrato(t), "on", aos520cCand(10))
	n.upstream.mu.Lock()
	n.upstream.responde = aos520sRespostaComModeloCru
	n.upstream.mu.Unlock()
	n.correr(t, "0", nil)
	n.correr(t, "a", nil)
	metricas := aos520cMetricas(t, n.aos486No)

	turnos := aos520sAmostras(t, metricas, "aos_model_turns_by_model_total")
	for _, alias := range []string{aos486Modelo, aos520AliasCand} {
		for motivo, quer := range map[string]int{"tool_calls": 1, "stop": 1, "length": 0, "unreported": 0} {
			chave := `{model="` + alias + `",stop_reason="` + motivo + `"}`
			if got, tem := turnos[chave]; !tem || got != quer {
				t.Fatalf("aos_model_turns_by_model_total%s = %d (existe=%v); quer %d\n%v", chave, got, tem, quer, turnos)
			}
		}
	}
	terminados := aos520sAmostras(t, metricas, "aos_runs_finished_by_model_total")
	for _, alias := range []string{aos486Modelo, aos520AliasCand} {
		chave := `{model="` + alias + `",outcome="complete",reason="none"}`
		if terminados[chave] != 1 {
			t.Fatalf("aos_runs_finished_by_model_total%s = %d; quer 1\n%v", chave, terminados[chave], terminados)
		}
	}
	// A SOMA DOS MODELOS É A FAMÍLIA DE SEMPRE, par a par.
	deSempre := aos520sAmostras(t, metricas, "aos_runs_finished_total")
	if len(deSempre) == 0 {
		t.Fatal("aos_runs_finished_total nao foi lida")
	}
	for par, quer := range deSempre {
		soma := 0
		for _, alias := range []string{aos486Modelo, aos520AliasCand} {
			soma += terminados[`{model="`+alias+`",`+par[1:]]
		}
		if soma != quer {
			t.Fatalf("a soma dos modelos em %s e %d; aos_runs_finished_total diz %d", par, soma, quer)
		}
	}
	// O RÓTULO É DE CONJUNTO FECHADO: os dois aliases, e nunca o modelo que o provider declarou.
	modelos := aos520sModelos(t, metricas)
	if len(modelos) != 2 || !modelos[aos486Modelo] || !modelos[aos520AliasCand] {
		t.Fatalf("os valores do rotulo model sao %v; quer exactamente o titular e o candidato", modelos)
	}
	novas, _, _ := aos520sSeparar(metricas)
	if strings.Contains(strings.Join(novas, "\n"), aos520sModeloCru) {
		t.Fatal("o modelo que o provider declarou ter servido apareceu numa serie por modelo")
	}
}

// O TECTO. Duas vezes (seis motivos + cinco causas + três estados × oito razões) = 70 séries, todas
// sempre presentes; nem uma a mais depois de os dois modelos terem servido.
func TestAOS520_Series_OTectoDeSeries(t *testing.T) {
	const querPorModelo = 6 + 5 + 3*8
	if maxModelosNasSeries != 2 || maxSeriesPorModelo() != 2*querPorModelo {
		t.Fatalf("tecto = %d modelos, %d series; o ADR-041 2.8 escreve 2 e %d", maxModelosNasSeries, maxSeriesPorModelo(), 2*querPorModelo)
	}
	n := aos520cNo(t, aos520NovoSubstrato(t), "on", aos520cCand(10))
	conferir := func(quando string) {
		t.Helper()
		metricas := aos520cMetricas(t, n.aos486No)
		total := 0
		for familia, quer := range map[string]int{
			"aos_model_turns_by_model_total":             2 * 6,
			"aos_model_response_rejected_by_model_total": 2 * 5,
			"aos_runs_finished_by_model_total":           2 * 3 * 8,
		} {
			got := len(aos520sAmostras(t, metricas, familia))
			if got != quer {
				t.Fatalf("%s: %s tem %d series; quer %d", quando, familia, got, quer)
			}
			total += got
		}
		if total != maxSeriesPorModelo() {
			t.Fatalf("%s: %d series por modelo; o tecto e %d", quando, total, maxSeriesPorModelo())
		}
	}
	conferir("no arranque")
	n.correr(t, "0", nil)
	n.correr(t, "a", nil)
	conferir("depois de os dois modelos servirem")
}

// A RESPOSTA RECUSADA CONTA NO MODELO QUE A PEDIU. O provider devolve um corpo que não é JSON a um
// run do candidato: a causa conta no candidato, o titular fica a zero, o run fecha `failed` na série
// do candidato, e a família de sempre conta a mesma recusa.
func TestAOS520_Series_ARespostaRecusadaContaNoModeloQueAPediu(t *testing.T) {
	n := aos520cNo(t, aos520NovoSubstrato(t), "on", aos520cCand(10))
	n.upstream.mu.Lock()
	n.upstream.responde = func(bool, string) []byte { return []byte(`{isto nao e json`) }
	n.upstream.mu.Unlock()
	rec := postJSON(n.h, "POST", "/runs", map[string]any{"run_id": "0", "objective": aos490Objectivo, "principal_nhi": durAgent, "credential": n.tok, "max_turns": 3})
	if rec.Code != 201 {
		t.Fatalf("POST /runs: %d %s", rec.Code, rec.Body.String())
	}
	if oc, ok, err := n.svc.Wait(t.Context(), "0"); err != nil || !ok || oc.Err == nil {
		t.Fatalf("o run tinha de falhar com a resposta recusada: ok=%v err=%v", ok, err)
	}
	metricas := aos520cMetricas(t, n.aos486No)
	recusadas := aos520sAmostras(t, metricas, "aos_model_response_rejected_by_model_total")
	if got := recusadas[`{model="`+aos520AliasCand+`",causa="json_invalido"}`]; got != 1 {
		t.Fatalf("a recusa nao contou no candidato: %v", recusadas)
	}
	for chave, v := range recusadas {
		if strings.Contains(chave, `model="`+aos486Modelo+`"`) && v != 0 {
			t.Fatalf("a recusa de um pedido do candidato contou no titular: %s = %d", chave, v)
		}
	}
	if got := aos520sAmostras(t, metricas, "aos_model_response_rejected_total")[`{causa="json_invalido"}`]; got != 1 {
		t.Fatalf("a familia de sempre tinha de contar a mesma recusa: %d", got)
	}
	terminados := aos520sAmostras(t, metricas, "aos_runs_finished_by_model_total")
	if terminados[`{model="`+aos520AliasCand+`",outcome="failed",reason="none"}`] != 1 {
		t.Fatalf("o run falhado nao contou no candidato: %v", terminados)
	}
	for chave, v := range aos520sAmostras(t, metricas, "aos_model_turns_by_model_total") {
		if v != 0 {
			t.Fatalf("um pedido com a resposta recusada contou como turno: %s = %d", chave, v)
		}
	}
}

// SEM CANDIDATO A SERVIR AS TRÊS FAMÍLIAS NÃO EXISTEM — com `off` e o registo na pasta (o despachante
// existe, e não conta nada), com `on` e a fatia a zero, e sem a pasta.
func TestAOS520_Series_SemCandidatoAServirNaoExistem(t *testing.T) {
	for nome, compor := range map[string]func(t *testing.T) *aos520No{
		"off com registo": func(t *testing.T) *aos520No { return aos520cNo(t, aos520NovoSubstrato(t), "off", aos520cCand(10)) },
		"on com fatia 0":  func(t *testing.T) *aos520No { return aos520cNo(t, aos520NovoSubstrato(t), "on", aos520cCand(0)) },
		"sem a pasta": func(t *testing.T) *aos520No {
			aos504SemVariavel(t, "AOS_MODEL_ROUTE_PROFILES_DIR")
			aos504SemVariavel(t, "AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR")
			aos520Interruptor(t, "")
			return aos520ComporComPasta(t, aos520NovoSubstrato(t))
		},
	} {
		t.Run(nome, func(t *testing.T) {
			n := compor(t)
			n.correr(t, "0", nil)
			if novas, _, _ := aos520sSeparar(aos520cMetricas(t, n.aos486No)); len(novas) != 0 {
				t.Fatalf("sem candidato a servir o /metrics tem linhas das familias por modelo:\n%s", strings.Join(novas, "\n"))
			}
			if n.node.seriesDoModelo() != nil {
				t.Fatal("sem candidato a servir nao se cria contador nenhum por modelo")
			}
		})
	}
}

// O DESPACHANTE SÓ CONTA COM O CANDIDATO A SERVIR. Com `off` e com a fatia a zero o despachante existe
// (para um run já fixado acabar) e não tem contadores; com `on` e fatia tem os dos dois aliases.
func TestAOS520_Series_ODespachanteSoContaComCandidatoAServir(t *testing.T) {
	despacha := func(interruptor string, fatia int) *clientePorModelo {
		aos519AmbienteDoGateway(t)
		aos520PastaCom(t, aos520cCand(fatia))
		aos520Interruptor(t, interruptor)
		cliente, _, err := parseModelFromEnv(false)
		if err != nil {
			t.Fatal(err)
		}
		d, ok := aos520cDespachante(cliente)
		if !ok {
			t.Fatalf("interruptor %q, fatia %d: sem despachante", interruptor, fatia)
		}
		if perfisDoCliente(cliente).porModelo != d.series {
			t.Fatal("as series do despachante e as que o /metrics le nao sao as mesmas")
		}
		return d
	}
	if d := despacha("off", 10); d.series != nil {
		t.Fatal("off: o despachante nao conta por modelo")
	}
	if d := despacha("", 10); d.series != nil {
		t.Fatal("ausente: o despachante nao conta por modelo")
	}
	if d := despacha("on", 0); d.series != nil {
		t.Fatal("on com a fatia a zero: o despachante nao conta por modelo")
	}
	d := despacha("on", 10)
	if d.series == nil || len(d.series.modelos) != 2 || d.series.modelos[0] != aos486Modelo || d.series.modelos[1] != aos520AliasCand {
		t.Fatalf("on com fatia: series = %+v", d.series)
	}
}

// OS CONTADORES, SOZINHOS: o alias fora do conjunto não soma (o run recusado pela fixação não chegou
// a ter modelo); um estado que não é terminal não soma; um erro que não é uma resposta recusada não
// soma; um motivo de paragem fora do vocabulário soma em `other`; e o ponteiro nil não faz nada.
func TestAOS520_Series_OsContadores(t *testing.T) {
	s := novasSeriesPorModelo("titular", "candidato")
	if len(s.modelos) != 2 || s.modelos[0] != "titular" || s.modelos[1] != "candidato" {
		t.Fatalf("modelos = %q", s.modelos)
	}
	if um := novasSeriesPorModelo("titular", "titular"); len(um.modelos) != 1 {
		t.Fatalf("um candidato com o nome do titular nao duplica o rotulo: %q", um.modelos)
	}
	s.contarDesfecho("", state.Failed, agentruntime.Result{})
	s.contarDesfecho("fornecedor/outro", state.Complete, agentruntime.Result{})
	s.contarDesfecho("candidato", state.Paused, agentruntime.Result{})
	s.contarDesfecho("candidato", state.Failed, agentruntime.Result{})
	s.contarDesfecho("candidato", state.Complete, agentruntime.Result{Verdict: &agentruntime.Verdict{Reason: agentruntime.OutcomeEmptyOutput}})
	total := 0
	for _, m := range s.modelos {
		for _, e := range s.estados {
			for _, r := range s.razoes {
				total += int(s.lidoTerminados(m, e, r))
			}
		}
	}
	if total != 2 || s.lidoTerminados("candidato", state.Failed, agentruntime.OutcomeFulfilled) != 1 || s.lidoTerminados("candidato", state.Complete, agentruntime.OutcomeEmptyOutput) != 1 {
		t.Fatalf("desfechos contados = %d; quer so os dois do candidato", total)
	}

	s.observarTurno("candidato", agentruntime.ModelResponse{StopReason: "motivo-inventado"}, nil)
	s.observarTurno("candidato", agentruntime.ModelResponse{}, errors.New("rede em baixo"))
	s.observarTurno("candidato", agentruntime.ModelResponse{}, modelgateway.ErrRespostaSemChoices)
	s.observarTurno("fornecedor/outro", agentruntime.ModelResponse{StopReason: agentruntime.StopStop}, nil)
	if s.lidoTurnos("candidato", agentruntime.StopOther) != 1 || s.lidoRecusadas("candidato", modelgateway.RejectNoChoices) != 1 {
		t.Fatalf("turnos other=%d recusadas sem_choices=%d; quer 1 e 1", s.lidoTurnos("candidato", agentruntime.StopOther), s.lidoRecusadas("candidato", modelgateway.RejectNoChoices))
	}
	// UM ALIAS FORA DO CONJUNTO NÃO GANHA SÉRIE: o conjunto é o da construção.
	if len(s.modelos) != 2 || s.lidoTurnos("fornecedor/outro", agentruntime.StopStop) != 0 || len(s.turnos) != 2 {
		t.Fatalf("um alias fora do conjunto ganhou uma serie: modelos=%q", s.modelos)
	}
	for _, motivo := range s.motivos {
		if s.lidoTurnos("titular", motivo) != 0 {
			t.Fatalf("o titular contou um turno do candidato em %q", motivo)
		}
	}

	var nulo *seriesPorModelo
	nulo.observarTurno("x", agentruntime.ModelResponse{}, nil)
	nulo.contarDesfecho("x", state.Complete, agentruntime.Result{})
	if nulo.lidoTurnos("x", agentruntime.StopStop) != 0 || nulo.lidoRecusadas("x", "y") != 0 || nulo.lidoTerminados("x", state.Complete, agentruntime.OutcomeFulfilled) != 0 {
		t.Fatal("o ponteiro nil tem de ler zero")
	}
	if got := rotuloDoModelo("a\"b\\c\nd"); got != `a\"b\\c\nd` {
		t.Fatalf("rotulo escapado = %q", got)
	}
}
