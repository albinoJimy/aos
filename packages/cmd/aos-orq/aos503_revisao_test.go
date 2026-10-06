package main

// AOS-503 — O QUE A REVISÃO ADVERSARIAL ENCONTROU (I1, I2, I3).
//
//   - I1 — o facto da tentativa recusava um `retry_of` acima de 128 bytes, e o `serve` abortava em
//     todas as gerações: um plano que em `off` saía terminal/13 passava a não sair.
//     [TestAOS503_Revisao_NodeIDLongo] (o binário real) e
//     [TestAOS503_Revisao_FactoQueNaoCabeFechaONo] (a rede: o facto que mesmo assim não cabe).
//   - I2 — a retoma seguia qualquer run que existisse com o id da tentativa.
//     [TestAOS503_Revisao_ARetomaNaoAdoptaUmRunAlheio] (o binário real) e
//     [TestAOS503_Revisao_AOrigemDaTentativa] (a regra, caso a caso).
//   - I3 — duas mutações sobreviviam: o prazo do `serve` não travava a tentativa (RO13,
//     [TestAOS503_Revisao_OPrazoTravaATentativa]) e a retoma podia re-submeter mais de uma vez
//     (RO16, [TestAOS503_Revisao_ARetomaReSubmeteNoMaximoUmaVez]).

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	plan "github.com/aos-ref/control-plane/orchestrator/plan"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// aos503Runner é o nó `aos` das provas de unidade: o estado por id de run (sem entrada ⇒ 404), o
// erro de cada submissão por id, e a lista do que foi submetido.
type aos503Runner struct {
	estados    map[string]estadoDoRun
	erros      map[string]error
	submetidos []string
	leituras   []string
}

func (r *aos503Runner) Submit(_ context.Context, p pedidoDeRun) error {
	r.submetidos = append(r.submetidos, p.RunID)
	return r.erros[p.RunID]
}

func (r *aos503Runner) Status(_ context.Context, runID string) (estadoDoRun, bool, error) {
	r.leituras = append(r.leituras, runID)
	st, existe := r.estados[runID]
	return st, existe, nil
}

// aos503Executor monta um executor de unidade para o nó `n1` do pedido `runID`, com a nova
// tentativa em `on`, o vínculo ao pedido e o relógio dado.
func aos503Executor(t *testing.T, runID string, cli nodeRunner, agora func() time.Time) *executorDeNos {
	t.Helper()
	return &executorDeNos{
		cli: cli, rec: planRecorderDeTeste(t, runID, runID+"-plan"), runID: runID,
		nos:             map[string]plan.Node{"n1": {NodeID: "n1", Role: "reader", Objective: "ler o documento"}},
		tools:           map[string][]string{"n1": {"cap:tool:doc_read"}},
		geracaoDoPedido: 2, declararOrigem: true, medicao: &medicaoDoContrato{}, agora: agora,
		emVoo: map[string]struct{}{"n1": {}}, sumidos: map[string]time.Time{}, causas: map[string]string{},
		payloads: map[chaveDePayload]string{}, candidatos: map[string]bool{}, declaradas: map[string]declaracaoDeOrigem{},
		tentativas: map[string]int{}, tentativaContada: map[string]bool{}, esgotados: map[string]bool{},
		recusasDeTentativa: map[string]string{}, retomadas: map[string]bool{}, submetidosAqui: map[string]bool{},
		conferidas: map[string]bool{}, semOrigemDesde: map[string]time.Time{},
		nt: configDaNovaTentativa{modo: novaTentativaOn, tectoDoNo: 2, tectoDoPlano: 4},
	}
}

// aos503SemChamar é o estado de um run que fechou failed por contract_unmet_no_call sem tool calls.
func aos503SemChamar(id string) estadoDoRun {
	return estadoDoRun{RunID: id, Status: "failed", OutcomeReason: string(agentruntime.OutcomeContractNoCall),
		Verdict: &agentruntime.Verdict{Mode: agentruntime.CompletionEnforce, Reason: agentruntime.OutcomeContractNoCall}}
}

// TestAOS503_Revisao_OPrazoTravaATentativa (RO13): uma tentativa não começa depois do prazo do
// `serve` — nem no instante dele. O nó fecha `failed` com `tentativa_recusada=prazo`, sem facto e
// sem pedido ao nó. Antes do prazo, o mesmo run dá a tentativa: é o controlo.
func TestAOS503_Revisao_OPrazoTravaATentativa(t *testing.T) {
	agora := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for nome, c := range map[string]struct {
		prazo  time.Time
		tentar bool
	}{
		"prazo-passado":   {agora.Add(-time.Second), false},
		"no-instante":     {agora, false},
		"antes-do-prazo":  {agora.Add(time.Second), true},
		"serve-sem-prazo": {time.Time{}, true},
	} {
		t.Run(nome, func(t *testing.T) {
			cli := &aos503Runner{}
			e := aos503Executor(t, "plano", cli, func() time.Time { return agora })
			e.nt.prazo = c.prazo
			outra, err := e.novaTentativa(context.Background(), "n1", aos503SemChamar("plano~n1"), true)
			if err != nil {
				t.Fatalf("novaTentativa: %v", err)
			}
			if c.tentar {
				if !outra || fmt.Sprint(cli.submetidos) != "[plano~n1~2]" || e.factosDeTentativa != 1 || e.recusasDeTentativa["n1"] != "" {
					t.Fatalf("controlo: dentro do prazo a tentativa faz-se — um facto e um pedido; outra=%t submetidos=%v factos=%d recusa=%q",
						outra, cli.submetidos, e.factosDeTentativa, e.recusasDeTentativa["n1"])
				}
				return
			}
			if outra || len(cli.submetidos) != 0 || e.factosDeTentativa != 0 {
				t.Fatalf("fora do prazo nao ha tentativa, nem facto, nem pedido ao no; outra=%t submetidos=%v factos=%d", outra, cli.submetidos, e.factosDeTentativa)
			}
			if e.recusasDeTentativa["n1"] != recusaPrazo || e.medicao.tentativasRecusadas[recusaPrazo] != 1 ||
				e.sufixoDasTentativas("n1") != " tentativa_recusada="+recusaPrazo {
				t.Fatalf("o no fecha com tentativa_recusada=prazo, contado uma vez; recusa=%q metrica=%v sufixo=%q",
					e.recusasDeTentativa["n1"], e.medicao.tentativasRecusadas, e.sufixoDasTentativas("n1"))
			}
		})
	}
}

// TestAOS503_Revisao_FactoQueNaoCabeFechaONo (I1, a rede): um id de run anterior que passa do
// tecto do facto é determinista para o plano. O nó fecha com `tentativa_recusada=facto_invalido`
// — `novaTentativa` NÃO devolve erro, que era o que abortava o `serve` em todas as gerações.
func TestAOS503_Revisao_FactoQueNaoCabeFechaONo(t *testing.T) {
	runID := strings.Repeat("p", 1100)
	cli := &aos503Runner{}
	e := aos503Executor(t, runID, cli, time.Now)
	outra, err := e.novaTentativa(context.Background(), "n1", aos503SemChamar(runID+"~n1"), true)
	if err != nil {
		t.Fatalf("um facto que nao cabe nao e um erro do serve (repetia-se em todas as geracoes); veio %v", err)
	}
	if outra || len(cli.submetidos) != 0 || e.factosDeTentativa != 0 || e.tentativaDe("n1") != 1 {
		t.Fatalf("sem facto nao ha pedido: outra=%t submetidos=%d factos=%d tentativa=%d", outra, len(cli.submetidos), e.factosDeTentativa, e.tentativaDe("n1"))
	}
	if e.recusasDeTentativa["n1"] != recusaFactoInvalido || e.medicao.tentativasRecusadas[recusaFactoInvalido] != 1 {
		t.Fatalf("o motivo e facto_invalido, contado uma vez: %q %v", e.recusasDeTentativa["n1"], e.medicao.tentativasRecusadas)
	}
	// Controlo: o pior caso que o plano admite sem um pedido desmedido — cabe, e a tentativa faz-se.
	curto := strings.Repeat("p", 500)
	cli2 := &aos503Runner{}
	e2 := aos503Executor(t, curto, cli2, time.Now)
	if outra, err := e2.novaTentativa(context.Background(), "n1", aos503SemChamar(curto+"~n1"), true); err != nil || !outra || len(cli2.submetidos) != 1 {
		t.Fatalf("controlo: um id de run de 503 bytes cabe no facto; outra=%t err=%v submetidos=%v", outra, err, cli2.submetidos)
	}
}

// TestAOS503_Revisao_ARetomaReSubmeteNoMaximoUmaVez (RO16): o log regista a tentativa 2, o nó
// `aos` não conhece o run, e a re-submissão não o faz aparecer (409 e o `GET` continua 404, ou
// 201 e o `GET` continua 404). A retoma submete UMA vez; nas passagens seguintes espera, e não
// volta a pedir.
func TestAOS503_Revisao_ARetomaReSubmeteNoMaximoUmaVez(t *testing.T) {
	for nome, erro := range map[string]error{"o-no-responde-409": errRunFilhoJaExiste, "o-no-responde-201": nil} {
		t.Run(nome, func(t *testing.T) {
			cli := &aos503Runner{erros: map[string]error{"plano~n1~2": erro}}
			agora := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
			e := aos503Executor(t, "plano", cli, func() time.Time { return agora })
			e.tentativas["n1"] = 2
			e.factosDeTentativa = 1
			for i := 0; i < 4; i++ {
				fechados, err := e.recolher(context.Background())
				if err != nil || fechados != 0 {
					t.Fatalf("passagem %d: o no continua em voo; fechados=%d err=%v", i+1, fechados, err)
				}
				agora = agora.Add(time.Second)
			}
			if fmt.Sprint(cli.submetidos) != "[plano~n1~2]" {
				t.Fatalf("a retoma re-submete a tentativa UMA vez em quatro passagens; submeteu %v", cli.submetidos)
			}
			if _, emVoo := e.emVoo["n1"]; !emVoo || e.factosDeTentativa != 1 {
				t.Fatalf("o no fica em voo e nenhum facto novo se grava: emVoo=%t factos=%d", emVoo, e.factosDeTentativa)
			}
		})
	}
}

// TestAOS503_Revisao_AOrigemDaTentativa (I2, a regra): o run com o id da tentativa só é o desta
// tentativa quando o NÓ `aos` o declara — o pedido, o plano, o nó e o número —, e a geração da
// reclamação não é posterior à deste `serve`.
func TestAOS503_Revisao_AOrigemDaTentativa(t *testing.T) {
	const id = "plano~n1~2"
	agora := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	relogio := func() time.Time { return agora }
	certa := origemDaTentativa{PlanRequest: "plano", Generation: 1, PlanID: "plano-plan", NodeID: "n1", Attempt: 2}
	com := func(status string, mudar func(*origemDaTentativa)) estadoDoRun {
		o := certa
		if mudar != nil {
			mudar(&o)
		}
		return estadoDoRun{RunID: id, Status: status, Terminated: status == "completed", PlanAttempt: &o}
	}
	novo := func() *executorDeNos {
		e := aos503Executor(t, "plano", &aos503Runner{}, relogio)
		e.tentativas["n1"] = 2
		return e
	}
	for nome, c := range map[string]struct {
		st   estadoDoRun
		quer int
	}{
		"confere-concluido":          {com("completed", nil), origemConfere},
		"confere-em-curso":           {com("in_progress", nil), origemConfere},
		"confere-geracao-deste":      {com("failed", func(o *origemDaTentativa) { o.Generation = 2 }), origemConfere},
		"outro-pedido":               {com("completed", func(o *origemDaTentativa) { o.PlanRequest = "plano~n1" }), origemAlheia},
		"outro-plano":                {com("completed", func(o *origemDaTentativa) { o.PlanID = "outro-plan" }), origemAlheia},
		"outro-no":                   {com("completed", func(o *origemDaTentativa) { o.NodeID = "2" }), origemAlheia},
		"outra-tentativa":            {com("completed", func(o *origemDaTentativa) { o.Attempt = 3 }), origemAlheia},
		"geracao-futura":             {com("completed", func(o *origemDaTentativa) { o.Generation = 3 }), origemAlheia},
		"geracao-zero":               {com("completed", func(o *origemDaTentativa) { o.Generation = 0 }), origemAlheia},
		"resposta-de-outro-run":      {estadoDoRun{RunID: "plano~n1~3", Status: "completed", Terminated: true, PlanAttempt: &certa}, origemAlheia},
		"sem-origem-concluido":       {estadoDoRun{RunID: id, Status: "completed", Terminated: true, FinalText: "TEXTO ALHEIO"}, origemAlheia},
		"sem-origem-failed":          {estadoDoRun{RunID: id, Status: "failed"}, origemAlheia},
		"sem-origem-em-curso-agora":  {estadoDoRun{RunID: id, Status: "in_progress"}, origemPorDeclarar},
		"sem-origem-a-espera-humana": {estadoDoRun{RunID: id, Status: "waiting_on_human"}, origemPorDeclarar},
	} {
		if veio := novo().tentativaDestePedido("n1", c.st); veio != c.quer {
			t.Errorf("%s: queria %d, veio %d", nome, c.quer, veio)
		}
	}
	// EM CURSO E SEM ORIGEM: espera-se a janela em que o nó `aos` a grava, e não mais do que ela.
	e := novo()
	emCurso := estadoDoRun{RunID: id, Status: "in_progress"}
	if e.tentativaDestePedido("n1", emCurso) != origemPorDeclarar {
		t.Fatal("a primeira leitura sem origem de um run em curso espera")
	}
	agora = agora.Add(toleranciaSemOrigem - time.Second)
	if e.tentativaDestePedido("n1", emCurso) != origemPorDeclarar {
		t.Fatal("dentro da tolerancia continua a esperar")
	}
	agora = agora.Add(2 * time.Second)
	if e.tentativaDestePedido("n1", emCurso) != origemAlheia {
		t.Fatal("passada a tolerancia, um run em curso sem origem nao e desta tentativa")
	}
}

// TestAOS503_Revisao_NodeIDLongo (I1, o binário real): um `node_id` de 121 bytes faz o id do run
// anterior passar dos 128 bytes de um passo. Em `off` o plano sai terminal/13; em `on` tem de
// sair TERMINAL também — aqui recuperado, porque a tentativa 2 conclui —, com o facto no log, e
// nunca `transitorio/1` em todas as gerações.
func TestAOS503_Revisao_NodeIDLongo(t *testing.T) {
	bin := construir(t)
	p := aos495FormaDeProducao(t, "enforce")
	longo := "read_notes_" + strings.Repeat("x", 110)
	plano := strings.ReplaceAll(p.plano, `"read_notes"`, `"`+longo+`"`)
	id1 := aos503Run + "~" + longo
	if len(longo) != 121 || len(id1) <= 128 {
		t.Fatalf("pre-condicao: node_id de 121 bytes e id do run acima de 128; veio %d e %d", len(longo), len(id1))
	}
	falhada1 := aos503ComId(t, aos503FioDoNo(t, "tentativa-1-falhada"), aos503Leitor1, id1)

	fOff := &aos503No{catalogo: aos503Catalogo(t, 2), respostas: map[string][]byte{id1: falhada1}}
	sOff := aos503Abrir(t, bin, fOff, plano, p.snapshot)
	if d := sOff.drenar(t, 1, "", "30s"); d.classe != "terminal" || d.codigo != exitNosFalhados {
		t.Fatalf("pre-condicao: em off o plano sai terminal/13; saiu %s/%d %q", d.classe, d.codigo, d.detalhe)
	}

	f := &aos503No{catalogo: aos503Catalogo(t, 2), respostas: map[string][]byte{id1: falhada1}}
	s := aos503Abrir(t, bin, f, plano, p.snapshot)
	d := s.drenar(t, 1, "on", "30s")
	if d.classe != "terminal" {
		t.Fatalf("em on o plano com um node_id de 121 bytes tem de sair TERMINAL, e nao abortar; saiu %s/%d %q\n%s\n%s", d.classe, d.codigo, d.detalhe, d.stdout, d.stderr)
	}
	factos := aos503Factos(t, s.wal)
	if d.codigo != exitOK || len(factos) != 1 || factos[0].RetryOf != id1 || factos[0].Attempt != 2 {
		t.Fatalf("a tentativa 2 faz-se e conclui: queria terminal/0 e o facto com retry_of=%d bytes; saiu %d, factos=%+v\n%s", len(id1), d.codigo, factos, d.stdout)
	}
	if quer := []string{id1, id1 + "~2", aos503Resumo}; fmt.Sprint(f.submetidos()) != fmt.Sprint(quer) {
		t.Fatalf("os POST: a primeira, a tentativa 2 e o consumidor:\n  quero: %v\n  veio:  %v", quer, f.submetidos())
	}
	if strings.Contains(d.stdout+d.stderr, "retry_of sem a forma") {
		t.Fatalf("o facto ja nao e recusado pela forma do retry_of:\n%s\n%s", d.stdout, d.stderr)
	}
}

// TestAOS503_Revisao_ARetomaNaoAdoptaUmRunAlheio (I2, o binário real). O `POST /runs` da
// tentativa 2 responde 500 e o `serve` aborta com o facto no log. Passa a existir no nó `aos` um
// run com o id da tentativa 2 que este plano NÃO submeteu. A geração seguinte lê o estado do id —
// e NÃO o segue: o nó do plano fecha `failed`, o plano sai terminal/13, e o consumidor não corre
// com a saída alheia.
func TestAOS503_Revisao_ARetomaNaoAdoptaUmRunAlheio(t *testing.T) {
	bin := construir(t)
	p := aos495FormaDeProducao(t, "enforce")
	falhada1 := aos503FioDoNo(t, "tentativa-1-falhada")
	const textoAlheio = "TEXTO DE UM RUN ALHEIO"
	semOrigem := `{"run_id":"` + aos503Leitor2 + `","status":"completed","terminated":true,"final_text":"` + textoAlheio + `","turns":1}`
	origem := func(pedido, no string, tentativa int) string {
		return strings.TrimSuffix(semOrigem, "}") + fmt.Sprintf(`,"plan_attempt":{"plan_request":%q,"generation":1,"plan_id":"plan-aos503-fio-plan","node_id":%q,"attempt":%d}}`, pedido, no, tentativa)
	}
	for nome, c := range map[string]struct {
		alheio string
		// colide: o run alheio aparece ENTRE a leitura (404) e a re-submissão da retoma, que
		// responde 409. Senão, já existe quando a geração seguinte lê o estado.
		colide bool
		// anteriorConcluido: na geração seguinte o nó `aos` passa a responder `completed` pelo
		// run da PRIMEIRA tentativa, que o facto do log diz ter falhado. Não é dele que o plano
		// tira uma saída: o nó fecha `failed` como run perdido.
		anteriorConcluido bool
	}{
		"um-run-directo-sem-origem":      {semOrigem, false, false},
		"um-run-de-outro-pedido-com-til": {origem(aos503Leitor1, "2", 2), false, false},
		"a-tentativa-de-outro-no":        {origem(aos503Run, "summarize", 2), false, false},
		"aparece-na-re-submissao-409":    {semOrigem, true, false},
		"e-o-run-anterior-diz-concluido": {semOrigem, false, true},
	} {
		t.Run(nome, func(t *testing.T) {
			f := &aos503No{catalogo: aos503Catalogo(t, 2), respostas: map[string][]byte{aos503Leitor1: falhada1, aos503Leitor2: []byte(c.alheio)}}
			geracao := 1
			f.estadoDoPost = func(id string, _ int) int {
				if id != aos503Leitor2 {
					return 0
				}
				if geracao == 1 {
					return http.StatusInternalServerError
				}
				// Na retoma: o run alheio passou a existir, e o nó responde 409.
				f.mu.Lock()
				f.aceites[aos503Leitor2] = true
				f.mu.Unlock()
				return http.StatusConflict
			}
			s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
			d1 := s.drenar(t, 1, "on", "30s")
			if d1.classe == "terminal" || len(aos503Factos(t, s.wal)) != 1 || f.aceite(aos503Leitor2) {
				t.Fatalf("pre-condicao: o serve aborta com o facto da tentativa 2 no log e sem o no a ter aceite; saiu %s/%d factos=%d\n%s", d1.classe, d1.codigo, len(aos503Factos(t, s.wal)), d1.stdout)
			}
			geracao = 2
			causa := "contract_unmet_no_call"
			if c.anteriorConcluido {
				causa = causaRunPerdido
				f.responder(aos503Leitor1, []byte(`{"run_id":"`+aos503Leitor1+`","status":"completed","terminated":true,"final_text":"`+textoAlheio+`","turns":1}`))
			}
			if !c.colide {
				f.mu.Lock()
				f.aceites[aos503Leitor2] = true
				f.mu.Unlock()
			}
			aos503EsperarAPosse()
			d2 := s.drenar(t, 2, "on", "30s")
			if d2.classe != "terminal" || d2.codigo != exitNosFalhados {
				t.Fatalf("a retoma NAO segue um run que nao e desta tentativa deste pedido: queria terminal/13; saiu %s/%d %q\n%s", d2.classe, d2.codigo, d2.detalhe, d2.stdout)
			}
			if len(f.corpos(aos503Resumo)) != 0 {
				t.Fatalf("o consumidor NAO corre — e muito menos com a saida alheia: %s", f.corpos(aos503Resumo)[0])
			}
			if strings.Contains(d2.stdout, "RECUPERADO") || strings.Contains(d2.stdout+d2.stderr+d2.detalhe, textoAlheio) {
				t.Fatalf("o no nunca fecha «recuperado» sobre um run alheio, e o texto dele nao aparece em lado nenhum:\n%s", d2.stdout)
			}
			if !strings.Contains(d2.detalhe, " causa=") || !strings.Contains(d2.detalhe, causa+":1") || !strings.Contains(d2.detalhe, "entrada_por_cumprir:1") ||
				!strings.Contains(d2.detalhe, " recuperados=0 tentativa_recusada="+recusaRunDeOutraOrigem) ||
				!temSerie(d2.metricas, serie(metricaTentativasRecusadas, "causa", recusaRunDeOutraOrigem), 1) {
				t.Fatalf("o no fecha com a causa do run anterior e o motivo %s: %q\n%s", recusaRunDeOutraOrigem, d2.detalhe, d2.metricas)
			}
			if !strings.Contains(d2.stdout, "(run "+aos503Leitor1+") causa="+causa) {
				t.Fatalf("o no fecha sobre o run que falhou, e nao sobre o alheio:\n%s", d2.stdout)
			}
			posts := 0
			for _, id := range f.submetidos() {
				if id == aos503Leitor2 {
					posts++
				}
			}
			if quer := map[bool]int{false: 1, true: 2}[c.colide]; posts != quer || len(aos503Factos(t, s.wal)) != 1 {
				t.Fatalf("os pedidos da tentativa 2: queria %d (o que deu 500%s) e um so facto; veio %d, factos=%d",
					quer, map[bool]string{false: "", true: ", e o da retoma que deu 409"}[c.colide], posts, len(aos503Factos(t, s.wal)))
			}
		})
	}
}
