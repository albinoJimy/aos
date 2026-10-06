package main

// AOS-503 — as peças da nova tentativa que se testam sem processo: o interruptor e o tecto, a
// forma do id, a elegibilidade (um caso por desfecho que NÃO admite tentativa), a classificação
// das recusas do nó, o anúncio lido do fio do nó real, e as métricas de vocabulário fechado.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	plan "github.com/aos-ref/control-plane/orchestrator/plan"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

func TestAOS503_Interruptor(t *testing.T) {
	for bruto, quer := range map[string]string{"": novaTentativaOff, "off": novaTentativaOff, "observe": novaTentativaObserve, "on": novaTentativaOn} {
		if got, err := modoDaNovaTentativa(bruto); err != nil || got != quer {
			t.Fatalf("modo %q: queria %q; veio %q (%v)", bruto, quer, got, err)
		}
	}
	for _, bruto := range []string{"On", "ON", "true", "1", "enforce", "observe ", "o n", "off,on"} {
		if got, err := modoDaNovaTentativa(bruto); err == nil || !errors.Is(err, ErrNovaTentativa) || got != "" {
			t.Fatalf("o valor %q tinha de recusar o arranque; veio %q (%v)", bruto, got, err)
		}
	}
	// A mensagem não repete o valor recusado: vem de um ficheiro de configuração e vai para o log.
	if _, err := modoDaNovaTentativa("um-valor-com-segredo"); err == nil || strings.Contains(err.Error(), "segredo") {
		t.Fatalf("a mensagem nao repete o valor recusado: %v", err)
	}
	// Do ambiente: as pontas aparam-se, e só espaços é a omissão.
	for valor, quer := range map[string]string{" on\n": novaTentativaOn, "   ": novaTentativaOff, "observe": novaTentativaObserve} {
		t.Setenv("AOS_ORQ_NOVA_TENTATIVA", valor)
		if got, err := modoDaNovaTentativaDoAmbiente(); err != nil || got != quer {
			t.Fatalf("AOS_ORQ_NOVA_TENTATIVA=%q: queria %q; veio %q (%v)", valor, quer, got, err)
		}
	}
	t.Setenv("AOS_ORQ_NOVA_TENTATIVA", "ligado")
	if _, err := modoDaNovaTentativaDoAmbiente(); !errors.Is(err, ErrNovaTentativa) {
		t.Fatalf("um valor desconhecido no ambiente recusa o arranque: %v", err)
	}
}

func TestAOS503_TectoPorPlano(t *testing.T) {
	for bruto, quer := range map[string]int{"": tectoDeTentativasPorPlanoPorOmissao, "0": 0, "1": 1, "4": 4, "64": 64} {
		if got, err := tectoDeTentativasPorPlano(bruto); err != nil || got != quer {
			t.Fatalf("tecto %q: queria %d; veio %d (%v)", bruto, quer, got, err)
		}
	}
	if tectoDeTentativasPorPlanoPorOmissao != 4 {
		t.Fatalf("a omissao do tecto por plano e quatro tentativas a mais (decisao do dono); esta a %d", tectoDeTentativasPorPlanoPorOmissao)
	}
	for _, bruto := range []string{"-1", "65", "quatro", "4.0", "04", "+4", "4 "} {
		if _, err := tectoDeTentativasPorPlano(bruto); !errors.Is(err, ErrNovaTentativa) {
			t.Fatalf("o tecto %q tinha de recusar o arranque: %v", bruto, err)
		}
	}
	t.Setenv("AOS_ORQ_NOVA_TENTATIVA_MAX_POR_PLANO", " 2 ")
	if got, err := tectoDeTentativasPorPlanoDoAmbiente(); err != nil || got != 2 {
		t.Fatalf("do ambiente, com as pontas aparadas: %d (%v)", got, err)
	}
	// O tecto por nó é o mais apertado entre o do aos-orq (2) e o que o nó anuncia.
	for anunciado, quer := range map[int]int{0: 0, 1: 1, 2: 2, 5: 2} {
		if got := (configDaNovaTentativa{tectoDoNo: anunciado}).porNo(); got != quer {
			t.Fatalf("no anuncia %d: o tecto por no e %d; veio %d", anunciado, quer, got)
		}
	}
}

// TestAOS503_Fio_VectoresDoId GERA os vectores da forma do id, que o teste do NÓ confere contra o
// que ele compõe, e prova a injectividade dentro de um pedido.
func TestAOS503_Fio_VectoresDoId(t *testing.T) {
	type vector struct {
		Plano     string `json:"plano"`
		NodeID    string `json:"node_id"`
		Tentativa int    `json:"tentativa"`
		ID        string `json:"id"`
	}
	var vectores []vector
	vistos := map[string]string{}
	for _, plano := range []string{"plan-aos503-fio", "run-x"} {
		for _, no := range []string{"read_notes", "n1", "n1~2", "n1+7e2", "n1.2", "n1:2", "a.b", "a+2eb", "2", "n1-2", "n_1", "~", "+"} {
			for tentativa := 1; tentativa <= 3; tentativa++ {
				id := idDaTentativa(plano, no, tentativa)
				par := fmt.Sprintf("(%q, %q, %d)", plano, no, tentativa)
				if outro, ja := vistos[id]; ja {
					t.Fatalf("%s e %s produzem o mesmo id %q", outro, par, id)
				}
				vistos[id] = par
				if err := eventstore.ValidarStreamID(id); err != nil {
					t.Fatalf("o id %q de %s nao e um stream_id: %v", id, par, err)
				}
				vectores = append(vectores, vector{plano, no, tentativa, id})
			}
		}
	}
	if idDaTentativa("run-x", "n1", 1) != childRunID("run-x", "n1") || idDaTentativa("run-x", "n1", 0) != childRunID("run-x", "n1") {
		t.Fatal("a primeira tentativa tem o id do run filho de sempre")
	}
	if idDaTentativa("run-x", "n1", 2) != "run-x~n1~2" || idDaTentativa("run-x", "a.b", 3) != "run-x~a+2eb~3" {
		t.Fatalf("a forma e <run>~<no escapado>~<n>: %q %q", idDaTentativa("run-x", "n1", 2), idDaTentativa("run-x", "a.b", 3))
	}
	cru, err := json.MarshalIndent(vectores, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	aos503Fio(t, "vectores-do-id", cru)
}

// TestAOS503_Elegibilidade: um caso por desfecho que NÃO admite nova tentativa, e o único que
// admite. A decisão lê só o vocabulário fechado do veredicto.
func TestAOS503_Elegibilidade(t *testing.T) {
	const id = "run-x~n1"
	leitor := plan.Node{NodeID: "n1", Role: "reader"}
	comConsumes := plan.Node{NodeID: "n1", Role: "summarizer", Consumes: []plan.PayloadEdge{{From: "n0", Output: "conteudo"}}}
	verificador := plan.Node{NodeID: "n1", Role: plan.RoleVerifier}
	tools := []string{"doc_read"}
	semChamar := func(mudar func(*estadoDoRun)) estadoDoRun {
		st := estadoDoRun{RunID: id, Status: "failed", OutcomeReason: string(agentruntime.OutcomeContractNoCall),
			Verdict: &agentruntime.Verdict{Mode: agentruntime.CompletionEnforce, Reason: agentruntime.OutcomeContractNoCall}}
		if mudar != nil {
			mudar(&st)
		}
		return st
	}
	if !elegivelParaNovaTentativa(leitor, tools, id, semChamar(nil), true) {
		t.Fatal("controlo: no nao-verificador com tools, run failed por contract_unmet_no_call sem tool calls — e elegivel")
	}
	if !elegivelParaNovaTentativa(comConsumes, tools, id, semChamar(nil), true) {
		t.Fatal("decisao 3 do dono: um no com consumes e elegivel pela mesma regra")
	}
	// O texto final não entra: com ele ou sem ele a resposta é a mesma.
	if !elegivelParaNovaTentativa(leitor, tools, id, semChamar(func(st *estadoDoRun) { st.FinalText = "nao consigo ler o documento" }), true) {
		t.Fatal("a decisao nao le o texto final")
	}
	for nome, c := range map[string]struct {
		n      plan.Node
		tools  []string
		st     estadoDoRun
		existe bool
	}{
		"verificador":       {verificador, tools, semChamar(nil), true},
		"no-sem-tools":      {leitor, nil, semChamar(nil), true},
		"run-perdido":       {leitor, tools, estadoDoRun{}, false},
		"resposta-de-outro": {leitor, tools, semChamar(func(st *estadoDoRun) { st.RunID = "run-x~n2" }), true},
		"after-denial": {leitor, tools, semChamar(func(st *estadoDoRun) {
			st.OutcomeReason = string(agentruntime.OutcomeContractAfterDenial)
			st.Verdict.Reason, st.Verdict.ToolCallsRequested = agentruntime.OutcomeContractAfterDenial, 1
		}), true},
		"after-tool-error": {leitor, tools, semChamar(func(st *estadoDoRun) {
			st.OutcomeReason = string(agentruntime.OutcomeContractAfterToolError)
			st.Verdict.Reason, st.Verdict.ToolCallsRequested = agentruntime.OutcomeContractAfterToolError, 1
		}), true},
		"truncated": {leitor, tools, semChamar(func(st *estadoDoRun) {
			st.OutcomeReason, st.Verdict.Reason = string(agentruntime.OutcomeTruncated), agentruntime.OutcomeTruncated
		}), true},
		"empty-output": {leitor, tools, semChamar(func(st *estadoDoRun) {
			st.OutcomeReason, st.Verdict.Reason = string(agentruntime.OutcomeEmptyOutput), agentruntime.OutcomeEmptyOutput
		}), true},
		"origem-em-falta": {leitor, tools, semChamar(func(st *estadoDoRun) {
			st.OutcomeReason, st.Verdict.Reason = string(agentruntime.OutcomeOutputSourceMissing), agentruntime.OutcomeOutputSourceMissing
		}), true},
		"origem-ambigua": {leitor, tools, semChamar(func(st *estadoDoRun) {
			st.OutcomeReason, st.Verdict.Reason = string(agentruntime.OutcomeOutputSourceAmbiguous), agentruntime.OutcomeOutputSourceAmbiguous
		}), true},
		// A RAZÃO É `contract_unmet_no_call` E HOUVE UMA TOOL CALL: o contrato exige duas tools, o
		// modelo chamou uma e parou. O run tem um efeito aplicado — nunca se repete.
		"no-call-com-uma-tool-call-pedida": {leitor, tools, semChamar(func(st *estadoDoRun) { st.Verdict.ToolCallsRequested = 1 }), true},
		"sem-vector":                       {leitor, tools, semChamar(func(st *estadoDoRun) { st.Verdict = nil }), true},
		"veredicto-cumprido":               {leitor, tools, semChamar(func(st *estadoDoRun) { st.Verdict.Fulfilled = true }), true},
		"timed-out":                        {leitor, tools, semChamar(func(st *estadoDoRun) { st.Status = "timed_out" }), true},
		"killed":                           {leitor, tools, semChamar(func(st *estadoDoRun) { st.Status = "killed" }), true},
		"failed-sem-razao":                 {leitor, tools, estadoDoRun{RunID: id, Status: "failed"}, true},
		"nao-concluiu-sem-terminated":      {leitor, tools, estadoDoRun{RunID: id, Status: "completed"}, true},
		// Em observação do veredicto o nó devolve a razão ao lado de um run `completed`: não falhou.
		"observado-e-concluido": {leitor, tools, semChamar(func(st *estadoDoRun) { st.Status, st.Terminated = "completed", true }), true},
		"razao-desconhecida":    {leitor, tools, semChamar(func(st *estadoDoRun) { st.OutcomeReason = "uma_razao_nova" }), true},
		"em-curso":              {leitor, tools, estadoDoRun{RunID: id, Status: "in_progress"}, true},
	} {
		if elegivelParaNovaTentativa(c.n, c.tools, id, c.st, c.existe) {
			t.Errorf("%s: NAO pode haver nova tentativa", nome)
		}
	}
}

func TestAOS503_MotivoDaRecusa(t *testing.T) {
	for nome, c := range map[string]struct {
		err      error
		motivo   string
		recusada bool
	}{
		"429":                {&erroDeSubmissao{status: 429, msg: "x"}, recusaQuota, true},
		"403":                {&erroDeSubmissao{status: 403, msg: "x"}, recusaPeloNo, true},
		"409":                {fmt.Errorf("%w: id", errRunFilhoJaExiste), recusaPeloNo, true},
		"500":                {&erroDeSubmissao{status: 500, msg: "x"}, "", false},
		"503":                {&erroDeSubmissao{status: 503, msg: "x"}, "", false},
		"400":                {&erroDeSubmissao{status: 400, msg: "x"}, "", false},
		"rede":               {errors.New("connection refused"), "", false},
		"mandato":            {fmt.Errorf("%w: id", errRequerenteForaDoMandato), "", false},
		"embrulhado-com-429": {fmt.Errorf("contexto: %w", &erroDeSubmissao{status: 429, msg: "x"}), recusaQuota, true},
	} {
		motivo, recusada := motivoDaRecusaDaTentativa(c.err)
		if motivo != c.motivo || recusada != c.recusada {
			t.Errorf("%s: queria (%q, %t); veio (%q, %t)", nome, c.motivo, c.recusada, motivo, recusada)
		}
		if recusada {
			achou := false
			for _, v := range recusasDeTentativa {
				achou = achou || v == motivo
			}
			if !achou {
				t.Errorf("%s: o motivo %q esta fora do vocabulario fechado", nome, motivo)
			}
		}
	}
}

// TestAOS503_Submit_OEstadoViajaEOTextoEODeSempre: o erro de uma submissão recusada leva o estado
// HTTP e mantém o texto que tinha antes deste ticket; e o corpo da tentativa leva `attempt`.
func TestAOS503_Submit_OEstadoViajaEOTextoEODeSempre(t *testing.T) {
	var corpo map[string]json.RawMessage
	estado := http.StatusTooManyRequests
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&corpo)
		w.WriteHeader(estado)
		_, _ = w.Write([]byte(`{"error":"submissao recusada"}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	cred := dir + "/nhi.jwt"
	escrever(t, cred, "nhi")
	cli := &nodeClient{base: srv.URL, http: srv.Client(), credFile: cred, principal: "agent:aos-orq"}
	p := pedidoDeRun{RunID: "run-x~n1~2", Objective: "o", Tools: []string{"doc_read"},
		PlanRequest: &vinculoAoPedido{RunID: "run-x", Geracao: 1, PlanID: "run-x-plan", NodeID: "n1", Attempt: 2}}
	err := cli.Submit(context.Background(), p)
	var es *erroDeSubmissao
	if !errors.As(err, &es) || es.status != http.StatusTooManyRequests {
		t.Fatalf("o estado HTTP viaja no erro; veio %v", err)
	}
	if quer := `submeter run-x~n1~2 ao nó: HTTP 429 {"error":"submissao recusada"}`; err.Error() != quer {
		t.Fatalf("o texto do erro e o de antes:\n  quero: %s\n  veio:  %s", quer, err)
	}
	if string(corpo["plan_request"]) != `{"run_id":"run-x","generation":1,"plan_id":"run-x-plan","node_id":"n1","attempt":2}` {
		t.Fatalf("o vinculo da tentativa leva attempt: %s", corpo["plan_request"])
	}
	// A primeira tentativa NÃO leva o campo: um nó anterior ao AOS-502 recusava-o com 400.
	estado = http.StatusCreated
	p.RunID, p.PlanRequest.Attempt = "run-x~n1", 0
	if err := cli.Submit(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(corpo["plan_request"]), "attempt") {
		t.Fatalf("a primeira tentativa nao leva attempt: %s", corpo["plan_request"])
	}
}

// TestAOS503_Anuncio_OClienteLeOFioDoNo: o cliente lê do `GET /tools` do NÓ REAL (ficheiro de fio
// do AOS-502) o tecto de tentativas; um nó sem o anúncio dá zero, e um valor que este binário não
// sabe usar fica dentro dos seus limites.
func TestAOS503_Anuncio_OClienteLeOFioDoNo(t *testing.T) {
	ler := func(corpo []byte) anuncioDoNo {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(corpo) }))
		defer srv.Close()
		a, err := (&nodeClient{base: srv.URL, http: &http.Client{Timeout: 5 * time.Second}}).ContratoDeConclusao(context.Background())
		if err != nil {
			t.Fatalf("anuncio: %v", err)
		}
		return a
	}
	if a := ler(aos503Catalogo(t, 2)); a.tentativas != 2 || !a.aceita || !a.vinculativa {
		t.Fatalf("o fio do no com o tecto a 2: tentativas=2 e os outros anuncios como antes; veio %+v", a)
	}
	if a := ler(aos503Catalogo(t, 1)); a.tentativas != 1 {
		t.Fatalf("tecto 1: %+v", a)
	}
	if a := ler(aos503Catalogo(t, 0)); a.tentativas != 0 || !a.aceita {
		t.Fatalf("um no sem o anuncio (anterior ao AOS-502, ou com o tecto a zero) da zero tentativas: %+v", a)
	}
	for corpo, quer := range map[string]int{
		`{"tools":[],"run_retry":{"max":7}}`:  maxTentativasAMaisPorNo,
		`{"tools":[],"run_retry":{"max":-3}}`: 0,
		`{"tools":[],"run_retry":{"max":0}}`:  0,
		`{"tools":[],"run_retry":{}}`:         0,
		`{"tools":[],"run_retry":null}`:       0,
	} {
		if a := ler([]byte(corpo)); a.tentativas != quer {
			t.Fatalf("%s: queria %d tentativas; veio %d", corpo, quer, a.tentativas)
		}
	}
}

// TestAOS503_Metricas_SoVocabularioFechado: a escrita só aceita rótulos das listas fechadas; o
// que vier de fora não cria série. E o resumo do desfecho só ganha campos quando há o que dizer.
func TestAOS503_Metricas_SoVocabularioFechado(t *testing.T) {
	c := &medicaoDoContrato{}
	c.primeiraFalhaSemChamar("false")
	c.primeiraFalhaSemChamar("true")
	c.primeiraFalhaSemChamar("talvez")
	c.tentativaFeita(2, tentativaVoltouAFalhar, "false")
	c.tentativaFeita(3, tentativaRecuperou, "true")
	c.tentativaFeita(4, tentativaRecuperou, "false")
	c.tentativaFeita(2, "um_desfecho_novo", "false")
	c.noRecuperado("true")
	c.tentativaRecusada(recusaQuota)
	c.tentativaRecusada("um motivo com texto do no")
	c.tentativaEmObservacao("false")
	m := &metricasDoConsumo{series: map[string]float64{}}
	m.registarNovaTentativa(c)
	texto := string(m.texto())
	for _, quer := range []string{
		serie(metricaPrimeirasFalhas, "com_consumes", "false") + " 1\n",
		serie(metricaPrimeirasFalhas, "com_consumes", "true") + " 1\n",
		serie(metricaTentativas, "tentativa", "2", "desfecho", tentativaVoltouAFalhar, "com_consumes", "false") + " 1\n",
		serie(metricaTentativas, "tentativa", "3", "desfecho", tentativaRecuperou, "com_consumes", "true") + " 1\n",
		serie(metricaNosRecuperados, "com_consumes", "true") + " 1\n",
		serie(metricaTentativasRecusadas, "causa", recusaQuota) + " 1\n",
		serie(metricaTentativasEmObservacao, "com_consumes", "false") + " 1\n",
	} {
		if !strings.Contains(texto, quer) {
			t.Fatalf("faltou %q:\n%s", quer, texto)
		}
	}
	for _, proibido := range []string{"talvez", "um_desfecho_novo", "texto do no", `tentativa="4"`} {
		if strings.Contains(texto, proibido) {
			t.Fatalf("um valor fora do vocabulario criou uma serie (%q):\n%s", proibido, texto)
		}
	}
	// Vazio ⇒ nada: com o interruptor em off o ficheiro é o de antes.
	vazio := &metricasDoConsumo{series: map[string]float64{}}
	vazio.registarNovaTentativa(&medicaoDoContrato{})
	vazio.registarPlanoComTentativas(nil, exitOK)
	if len(vazio.texto()) != 0 {
		t.Fatalf("sem medicoes nao se escreve serie nenhuma:\n%s", vazio.texto())
	}
	// POR PLANO.
	p := &metricasDoConsumo{series: map[string]float64{}}
	p.registarPlanoComTentativas(&resumoDasTentativas{ligada: true, tentativas: 2, recuperados: 1}, exitOK)
	p.registarPlanoComTentativas(&resumoDasTentativas{ligada: true, tentativas: 2, recuperados: 1, esgotados: 1}, exitNosFalhados)
	p.registarPlanoComTentativas(&resumoDasTentativas{ligada: true, tentativas: 9}, exitNosFalhados)
	plano := string(p.texto())
	for _, quer := range []string{
		metricaPlanosRecuperados + " 1\n", metricaPlanosEsgotados + " 1\n",
		serie(metricaTentativasPorPlano, "tentativas", "2") + " 2\n", serie(metricaTentativasPorPlano, "tentativas", "4_ou_mais") + " 1\n",
	} {
		if !strings.Contains(plano, quer) {
			t.Fatalf("faltou %q:\n%s", quer, plano)
		}
	}
	// O RESUMO.
	base := resumoDoPedido{origem: origemDecomposicao, geracao: 1, nos: 2}
	antes := base.linha()
	base.tentativas = &resumoDasTentativas{}
	if base.linha() != antes {
		t.Fatalf("um resumo sem tentativas e a linha de sempre: %q / %q", base.linha(), antes)
	}
	base.tentativas = &resumoDasTentativas{tentativas: 3, recuperados: 1, esgotados: 1, recusadas: []string{recusaQuota, recusaTectoDoPlano}}
	if quer := antes + " tentativas=3 recuperados=1 tentativas_esgotadas=1 tentativa_recusada=quota,tecto_do_plano"; base.linha() != quer {
		t.Fatalf("o resumo com tentativas:\n  quero: %q\n  veio:  %q", quer, base.linha())
	}
	for _, classe := range []int{0, 1, 2, 3, 4, 50, -1} {
		achou := false
		for _, v := range classesDeTentativasPorPlano {
			achou = achou || v == classeDeTentativasPorPlano(classe)
		}
		if !achou {
			t.Fatalf("a classe de %d tentativas esta fora da lista fechada: %q", classe, classeDeTentativasPorPlano(classe))
		}
	}
}
