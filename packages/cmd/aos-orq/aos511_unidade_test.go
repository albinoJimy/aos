package main

// AOS-511 — A NOVA TENTATIVA POR RESPOSTA VAZIA, EM UNIDADE: o interruptor, a elegibilidade, os
// tectos partilhados com o AOS-503, a retoma, e as métricas por classe.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	plan "github.com/aos-ref/control-plane/orchestrator/plan"
	plannerevents "github.com/aos-ref/control-plane/orchestrator/plannerevents"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// aos511Executor é o [aos503Executor] com um segundo nó, `n2`, SEM tools (o nó de resumo), a
// classe da resposta vazia em `on` e anunciada pelo nó, e a do AOS-503 também em `on`.
func aos511Executor(t *testing.T, runID string, cli nodeRunner, agora func() time.Time) *executorDeNos {
	t.Helper()
	e := aos503Executor(t, runID, cli, agora)
	e.nos["n2"] = plan.Node{NodeID: "n2", Role: "summarizer", Objective: "resumir o documento"}
	e.emVoo["n2"] = struct{}{}
	e.nt.modoVazia, e.nt.vaziaAnunciada = novaTentativaOn, true
	return e
}

// aos511Vazio é o estado de um run que fechou failed por empty_output sem tool calls.
func aos511Vazio(id string) estadoDoRun {
	return estadoDoRun{RunID: id, Status: "failed", OutcomeReason: string(agentruntime.OutcomeEmptyOutput),
		Verdict: &agentruntime.Verdict{Mode: agentruntime.CompletionEnforce, Reason: agentruntime.OutcomeEmptyOutput}}
}

// TestAOS511_Interruptor: `AOS_ORQ_NOVA_TENTATIVA_VAZIA` aceita off, observe e on; vazia é off; o
// resto recusa o arranque, sem o valor na mensagem. É independente do `AOS_ORQ_NOVA_TENTATIVA`.
func TestAOS511_Interruptor(t *testing.T) {
	for bruto, quer := range map[string]string{"": "off", "off": "off", "observe": "observe", "on": "on"} {
		if modo, err := modoDaTentativaVazia(bruto); err != nil || modo != quer {
			t.Fatalf("%q: queria %q; veio %q (%v)", bruto, quer, modo, err)
		}
	}
	for _, mau := range []string{"On", "ON", "1", "true", "enforce", "on,observe", "SEGREDO-DO-OPERADOR"} {
		modo, err := modoDaTentativaVazia(mau)
		if !errors.Is(err, ErrNovaTentativa) || modo != "" {
			t.Fatalf("%q tinha de recusar o arranque; veio %q (%v)", mau, modo, err)
		}
		if strings.Contains(err.Error(), "SEGREDO") || !strings.Contains(err.Error(), "AOS_ORQ_NOVA_TENTATIVA_VAZIA") {
			t.Fatalf("a mensagem nomeia a variavel e nao leva o valor recusado: %v", err)
		}
	}
	t.Setenv("AOS_ORQ_NOVA_TENTATIVA", "on")
	t.Setenv("AOS_ORQ_NOVA_TENTATIVA_VAZIA", "")
	if modo, err := modoDaTentativaVaziaDoAmbiente(); err != nil || modo != novaTentativaOff {
		t.Fatalf("o interruptor do AOS-503 nao liga este: queria off; veio %q (%v)", modo, err)
	}
	t.Setenv("AOS_ORQ_NOVA_TENTATIVA", "")
	t.Setenv("AOS_ORQ_NOVA_TENTATIVA_VAZIA", " observe ")
	if modo, err := modoDaTentativaVaziaDoAmbiente(); err != nil || modo != novaTentativaObserve {
		t.Fatalf("as pontas aparam-se: queria observe; veio %q (%v)", modo, err)
	}
	if modo, err := modoDaNovaTentativaDoAmbiente(); err != nil || modo != novaTentativaOff {
		t.Fatalf("este interruptor nao liga o do AOS-503: queria off; veio %q (%v)", modo, err)
	}
	for nome, c := range map[string]struct {
		cfg  configDaNovaTentativa
		quer string
	}{
		"observe":       {configDaNovaTentativa{modoVazia: novaTentativaObserve}, "EM OBSERVACAO"},
		"nao-anunciado": {configDaNovaTentativa{modoVazia: novaTentativaOn, tectoDoNo: 2, tectoDoPlano: 4}, "LIGADA E NAO APLICADA"},
		"tecto-a-zero":  {configDaNovaTentativa{modoVazia: novaTentativaOn, vaziaAnunciada: true, tectoDoPlano: 4}, "LIGADA E NAO APLICADA"},
		"ligada":        {configDaNovaTentativa{modoVazia: novaTentativaOn, vaziaAnunciada: true, tectoDoNo: 2, tectoDoPlano: 4}, "LIGADA — "},
	} {
		if b := bannerDaTentativaVazia(c.cfg); !strings.Contains(b, c.quer) || !strings.Contains(b, "AOS-511") {
			t.Fatalf("%s: o banner tinha de dizer %q; veio %q", nome, c.quer, b)
		}
	}
}

// TestAOS511_Elegibilidade: um caso por condição. A função lê vocabulário fechado e a estrutura
// do plano; o texto final e o raciocínio do run falhado NÃO entram na decisão.
func TestAOS511_Elegibilidade(t *testing.T) {
	const id = "plano~n2"
	semTools := plan.Node{NodeID: "n2", Role: "summarizer"}
	verificador := plan.Node{NodeID: "n2", Role: "verifier"}
	if !verificador.IsVerifier() {
		t.Fatal("pre-condicao: o papel verifier e um verificador")
	}
	comOrigem := plan.Node{NodeID: "n2", Role: "summarizer", Outputs: []plan.Output{{Name: "conteudo", FromTool: "doc_read"}}}
	if !comOrigem.DeclaresOutputSource() {
		t.Fatal("pre-condicao: o no declara a origem de uma saida")
	}
	vazio := aos511Vazio(id)
	if !elegivelParaTentativaVazia(semTools, []string{}, id, vazio, true) {
		t.Fatal("controlo: um no sem tools cujo run fechou failed por empty_output sem tool calls e elegivel")
	}
	mudar := func(f func(*estadoDoRun)) estadoDoRun {
		st := aos511Vazio(id)
		v := *st.Verdict
		st.Verdict = &v
		f(&st)
		return st
	}
	for nome, c := range map[string]struct {
		no     plan.Node
		tools  []string
		st     estadoDoRun
		existe bool
	}{
		"no-com-tools":             {semTools, []string{"doc_read"}, vazio, true},
		"no-verificador":           {verificador, []string{}, vazio, true},
		"no-com-from_tool":         {comOrigem, []string{}, vazio, true},
		"run-inexistente":          {semTools, []string{}, vazio, false},
		"resposta-de-outro-run":    {semTools, []string{}, mudar(func(s *estadoDoRun) { s.RunID = "plano~outro" }), true},
		"run-concluido":            {semTools, []string{}, mudar(func(s *estadoDoRun) { s.Status = "completed" }), true},
		"run-em-curso":             {semTools, []string{}, mudar(func(s *estadoDoRun) { s.Status = "in_progress" }), true},
		"razao-truncated":          {semTools, []string{}, mudar(func(s *estadoDoRun) { s.OutcomeReason = string(agentruntime.OutcomeTruncated) }), true},
		"razao-no-call":            {semTools, []string{}, mudar(func(s *estadoDoRun) { s.OutcomeReason = string(agentruntime.OutcomeContractNoCall) }), true},
		"timed-out-sem-razao":      {semTools, []string{}, mudar(func(s *estadoDoRun) { s.Status, s.OutcomeReason = "timed_out", "" }), true},
		"failed-sem-razao":         {semTools, []string{}, mudar(func(s *estadoDoRun) { s.OutcomeReason = "" }), true},
		"sem-veredicto":            {semTools, []string{}, mudar(func(s *estadoDoRun) { s.Verdict = nil }), true},
		"veredicto-cumprido":       {semTools, []string{}, mudar(func(s *estadoDoRun) { s.Verdict.Fulfilled = true }), true},
		"com-tool-calls-pedidas":   {semTools, []string{}, mudar(func(s *estadoDoRun) { s.Verdict.ToolCallsRequested = 1 }), true},
		"razao-com-outra-caixa":    {semTools, []string{}, mudar(func(s *estadoDoRun) { s.OutcomeReason = "Empty_Output" }), true},
		"razao-com-espaco-no-fim":  {semTools, []string{}, mudar(func(s *estadoDoRun) { s.OutcomeReason = "empty_output " }), true},
		"razao-de-outro-vocabular": {semTools, []string{}, mudar(func(s *estadoDoRun) { s.OutcomeReason = "origem_vazia" }), true},
	} {
		if elegivelParaTentativaVazia(c.no, c.tools, id, c.st, c.existe) {
			t.Errorf("%s: NAO e elegivel para a tentativa por resposta vazia", nome)
		}
	}
	// O TEXTO NÃO ENTRA: com texto final (que o nó nem devolve num run falhado) a decisão é a mesma.
	comTexto := mudar(func(s *estadoDoRun) { s.FinalText = "<think>um raciocinio longo</think>" })
	if !elegivelParaTentativaVazia(semTools, []string{}, id, comTexto, true) {
		t.Fatal("a decisao nao le o texto final: um run failed por empty_output continua elegivel com ele presente")
	}
	semRazaoComTextoVazio := mudar(func(s *estadoDoRun) { s.OutcomeReason, s.FinalText = string(agentruntime.OutcomeTruncated), "" })
	if elegivelParaTentativaVazia(semTools, []string{}, id, semRazaoComTextoVazio, true) {
		t.Fatal("um texto final vazio nao faz elegivel um run que fechou por outra razao")
	}
	// As duas classes não se sobrepõem: o que uma aceita a outra recusa.
	if elegivelParaNovaTentativa(semTools, []string{}, id, vazio, true) || elegivelParaTentativaVazia(plan.Node{NodeID: "n1"}, []string{"doc_read"}, "plano~n1", aos503SemChamar("plano~n1"), true) {
		t.Fatal("um run e de uma classe ou da outra, nunca das duas")
	}
}

// TestAOS511_OffEObserve_NaoTentam: com o interruptor desta classe em `off` nada se conta, grava
// ou submete — mesmo com o do AOS-503 em `on`; em `observe` conta-se e diz-se, e não se tenta.
func TestAOS511_OffEObserve_NaoTentam(t *testing.T) {
	for _, modo := range []string{"", novaTentativaOff, novaTentativaObserve} {
		t.Run("modo="+modo, func(t *testing.T) {
			cli := &aos503Runner{}
			e := aos511Executor(t, "plano", cli, time.Now)
			e.nt.modoVazia = modo
			outra, err := e.novaTentativa(context.Background(), "n2", aos511Vazio("plano~n2"), true)
			if err != nil || outra {
				t.Fatalf("em %q nao ha tentativa; outra=%t err=%v", modo, outra, err)
			}
			if len(cli.submetidos) != 0 || e.factosDeTentativa != 0 || e.tentativaDe("n2") != 1 || len(e.recusasDeTentativa) != 0 || len(e.esgotados) != 0 {
				t.Fatalf("em %q nada se submete, grava ou marca: submetidos=%v factos=%d", modo, cli.submetidos, e.factosDeTentativa)
			}
			m := e.medicao
			if modo == novaTentativaObserve {
				if m.primeirasVazias != 1 || m.tentativasVaziasEmObservacao != 1 {
					t.Fatalf("em observe conta a primeira resposta vazia e a que tentaria; veio %d %d", m.primeirasVazias, m.tentativasVaziasEmObservacao)
				}
			} else if m.primeirasVazias != 0 || m.tentativasVaziasEmObservacao != 0 {
				t.Fatalf("em off nao se conta nada; veio %d %d", m.primeirasVazias, m.tentativasVaziasEmObservacao)
			}
			// As séries do AOS-503 não se mexem, em nenhum modo.
			if len(m.primeirasFalhas)+len(m.tentativasFeitas)+len(m.tentativasRecusadas)+len(m.tentativasEmObservacao)+len(m.nosRecuperados) != 0 {
				t.Fatalf("as medicoes do AOS-503 nao podem mudar por causa desta classe: %+v", m)
			}
		})
	}
	// INDEPENDÊNCIA, no outro sentido: esta classe em `on` e a do AOS-503 em `off` — o run que
	// terminou sem chamar a tool NÃO é tentado.
	cli := &aos503Runner{}
	e := aos511Executor(t, "plano", cli, time.Now)
	e.nt.modo = novaTentativaOff
	if outra, err := e.novaTentativa(context.Background(), "n1", aos503SemChamar("plano~n1"), true); err != nil || outra || len(cli.submetidos) != 0 || e.factosDeTentativa != 0 {
		t.Fatalf("com o AOS-503 em off o no com tools nao e tentado por esta classe estar ligada; outra=%t submetidos=%v", outra, cli.submetidos)
	}
	// Sem o vínculo ao pedido não há tentativa, nem contagem.
	e = aos511Executor(t, "plano", cli, time.Now)
	e.geracaoDoPedido = 0
	if outra, _ := e.novaTentativa(context.Background(), "n2", aos511Vazio("plano~n2"), true); outra || e.medicao.primeirasVazias != 0 {
		t.Fatal("sem o vinculo ao pedido nao ha tentativa nem contagem")
	}
}

// TestAOS511_On_FactoComARazaoEOsTectosPartilhados: em `on` grava-se o facto com
// `reason=empty_output` e submete-se a tentativa; os tectos por nó e por plano são os MESMOS das
// duas classes — os factos de uma gastam o tecto da outra.
func TestAOS511_On_FactoComARazaoEOsTectosPartilhados(t *testing.T) {
	cli := &aos503Runner{}
	e := aos511Executor(t, "plano", cli, time.Now)
	outra, err := e.novaTentativa(context.Background(), "n2", aos511Vazio("plano~n2"), true)
	if err != nil || !outra || fmt.Sprint(cli.submetidos) != "[plano~n2~2]" {
		t.Fatalf("a tentativa 2 faz-se: outra=%t err=%v submetidos=%v", outra, err, cli.submetidos)
	}
	if e.factosDeTentativa != 1 || e.factosDeTentativaVazia != 1 || !e.tentativaPorVazio("n2") || e.tentativaDe("n2") != 2 {
		t.Fatalf("um facto, desta classe, e a tentativa corrente e a 2: %d %d", e.factosDeTentativa, e.factosDeTentativaVazia)
	}
	// A tentativa 2 volta a responder vazio: a 3; e a 3 esgota (duas a mais por nó).
	if outra, _ := e.novaTentativa(context.Background(), "n2", aos511Vazio("plano~n2~2"), true); !outra || e.tentativaDe("n2") != 3 {
		t.Fatalf("a tentativa 3 faz-se; outra=%t tentativa=%d", outra, e.tentativaDe("n2"))
	}
	if outra, _ := e.novaTentativa(context.Background(), "n2", aos511Vazio("plano~n2~3"), true); outra || !e.esgotados["n2"] || !e.esgotadosPorVazio["n2"] {
		t.Fatalf("a terceira resposta vazia esgota as tentativas: outra=%t esgotados=%v", outra, e.esgotados)
	}
	if fmt.Sprint(cli.submetidos) != "[plano~n2~2 plano~n2~3]" || e.sufixoDasTentativas("n2") != " tentativas=3 tentativas_esgotadas" {
		t.Fatalf("no maximo duas tentativas a mais por no: %v %q", cli.submetidos, e.sufixoDasTentativas("n2"))
	}
	m := e.medicao
	if m.primeirasVazias != 1 ||
		m.tentativasVaziasFeitas[chaveDeTentativa{tentativa: "2", desfecho: tentativaVoltouAFalhar}] != 1 ||
		m.tentativasVaziasFeitas[chaveDeTentativa{tentativa: "3", desfecho: tentativaVoltouAFalhar}] != 1 {
		t.Fatalf("uma primeira resposta vazia e duas recorrencias: %+v", m)
	}
	if len(m.primeirasFalhas)+len(m.tentativasFeitas)+len(m.tentativasRecusadas)+len(m.nosRecuperados) != 0 {
		t.Fatalf("as medicoes do AOS-503 nao mudam por causa desta classe: %+v", m)
	}

	// O TECTO POR PLANO É UM SÓ: os factos da classe do AOS-503 gastam-no para esta, e vice-versa.
	cli = &aos503Runner{}
	e = aos511Executor(t, "plano", cli, time.Now)
	e.nt.tectoDoPlano = 1
	if outra, _ := e.novaTentativa(context.Background(), "n1", aos503SemChamar("plano~n1"), true); !outra {
		t.Fatal("pre-condicao: a tentativa do no com tools gasta a unica tentativa do plano")
	}
	if outra, _ := e.novaTentativa(context.Background(), "n2", aos511Vazio("plano~n2"), true); outra || e.recusasDeTentativa["n2"] != recusaTectoDoPlano {
		t.Fatalf("o plano ja gastou a sua tentativa na outra classe: esta nao ganha mais; outra=%t recusa=%q", outra, e.recusasDeTentativa["n2"])
	}
	if e.medicao.tentativasVaziasRecusadas[recusaTectoDoPlano] != 1 || len(e.medicao.tentativasRecusadas) != 0 || fmt.Sprint(cli.submetidos) != "[plano~n1~2]" {
		t.Fatalf("a recusa conta na serie DESTA classe, e nada se submete: %+v %v", e.medicao, cli.submetidos)
	}
	cli = &aos503Runner{}
	e = aos511Executor(t, "plano", cli, time.Now)
	e.nt.tectoDoPlano = 1
	if outra, _ := e.novaTentativa(context.Background(), "n2", aos511Vazio("plano~n2"), true); !outra {
		t.Fatal("pre-condicao: a tentativa por vazio gasta a unica tentativa do plano")
	}
	if outra, _ := e.novaTentativa(context.Background(), "n1", aos503SemChamar("plano~n1"), true); outra || e.recusasDeTentativa["n1"] != recusaTectoDoPlano {
		t.Fatalf("e no outro sentido: a classe do AOS-503 nao ganha tentativas por esta existir; outra=%t recusa=%q", outra, e.recusasDeTentativa["n1"])
	}
	if e.medicao.tentativasRecusadas[recusaTectoDoPlano] != 1 || len(e.medicao.tentativasVaziasRecusadas) != 0 {
		t.Fatalf("essa recusa conta na serie do AOS-503: %+v", e.medicao)
	}
}

// TestAOS511_On_RecusasSemTentativa: o nó que não anuncia a classe, o tecto do nó mais apertado,
// o prazo, e a recusa do nó `aos` — cada uma fecha o nó com o motivo, na série desta classe.
func TestAOS511_On_RecusasSemTentativa(t *testing.T) {
	agora := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for nome, c := range map[string]struct {
		ajuste     func(*executorDeNos, *aos503Runner)
		motivo     string
		submetidos string
		factos     int
	}{
		"no-nao-anuncia-a-classe": {func(e *executorDeNos, _ *aos503Runner) { e.nt.vaziaAnunciada = false }, recusaNaoAnunciado, "[]", 0},
		"no-nao-anuncia-tecto":    {func(e *executorDeNos, _ *aos503Runner) { e.nt.tectoDoNo = 0 }, recusaNaoAnunciado, "[]", 0},
		"prazo-do-serve":          {func(e *executorDeNos, _ *aos503Runner) { e.nt.prazo = agora }, recusaPrazo, "[]", 0},
		"tecto-do-plano-a-zero":   {func(e *executorDeNos, _ *aos503Runner) { e.nt.tectoDoPlano = 0 }, recusaTectoDoPlano, "[]", 0},
		"quota-429": {func(_ *executorDeNos, r *aos503Runner) {
			r.erros = map[string]error{"plano~n2~2": &erroDeSubmissao{status: http.StatusTooManyRequests}}
		}, recusaQuota, "[plano~n2~2]", 1},
		"recusada-pelo-no-403": {func(_ *executorDeNos, r *aos503Runner) {
			r.erros = map[string]error{"plano~n2~2": &erroDeSubmissao{status: http.StatusForbidden}}
		}, recusaPeloNo, "[plano~n2~2]", 1},
	} {
		t.Run(nome, func(t *testing.T) {
			cli := &aos503Runner{}
			e := aos511Executor(t, "plano", cli, func() time.Time { return agora })
			c.ajuste(e, cli)
			outra, err := e.novaTentativa(context.Background(), "n2", aos511Vazio("plano~n2"), true)
			if err != nil || outra {
				t.Fatalf("a tentativa nao se faz e nao e um erro do serve; outra=%t err=%v", outra, err)
			}
			if e.recusasDeTentativa["n2"] != c.motivo || e.medicao.tentativasVaziasRecusadas[c.motivo] != 1 || len(e.medicao.tentativasRecusadas) != 0 {
				t.Fatalf("o motivo e %q, contado na serie desta classe; veio %q %+v", c.motivo, e.recusasDeTentativa["n2"], e.medicao)
			}
			if fmt.Sprint(cli.submetidos) != c.submetidos || e.factosDeTentativa != c.factos || e.tentativaDe("n2") != 1 {
				t.Fatalf("submetidos=%v factos=%d tentativa=%d", cli.submetidos, e.factosDeTentativa, e.tentativaDe("n2"))
			}
		})
	}
	// O tecto do nó `aos` mais apertado (1): a segunda resposta vazia não esgota, é recusada.
	cli := &aos503Runner{}
	e := aos511Executor(t, "plano", cli, time.Now)
	e.nt.tectoDoNo = 1
	if outra, _ := e.novaTentativa(context.Background(), "n2", aos511Vazio("plano~n2"), true); !outra {
		t.Fatal("com o tecto do no a 1 a tentativa 2 faz-se")
	}
	if outra, _ := e.novaTentativa(context.Background(), "n2", aos511Vazio("plano~n2~2"), true); outra || e.recusasDeTentativa["n2"] != recusaTectoDoNo || e.esgotados["n2"] {
		t.Fatalf("vence o tecto mais apertado, o do no aos: recusa=%q", e.recusasDeTentativa["n2"])
	}
	// Um 5xx na submissão é um erro do `serve`, e o facto fica no log: a retoma continua dele.
	cli = &aos503Runner{erros: map[string]error{"plano~n2~2": &erroDeSubmissao{status: http.StatusInternalServerError}}}
	e = aos511Executor(t, "plano", cli, time.Now)
	if outra, err := e.novaTentativa(context.Background(), "n2", aos511Vazio("plano~n2"), true); err == nil || outra || e.factosDeTentativa != 1 {
		t.Fatalf("um 5xx propaga-se, com o facto ja gravado: outra=%t err=%v factos=%d", outra, err, e.factosDeTentativa)
	}
}

// TestAOS511_Retoma_PelaClasseDoFactoEComAOrigemConferida: a tentativa corrente lê-se do facto, e
// é a RAZÃO dele que diz que interruptor a retoma. Um run que este processo não submeteu só se
// segue com a origem que o nó declara.
func TestAOS511_Retoma_PelaClasseDoFactoEComAOrigemConferida(t *testing.T) {
	agora := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	facto := plannerevents.NodeAttemptStartedPayload{PlanID: "plano-plan", NodeID: "n2", Attempt: 2, RetryOf: "plano~n2", Reason: plannerevents.AttemptReasonEmptyOutput}
	novo := func(cli *aos503Runner) *executorDeNos {
		e := aos511Executor(t, "plano", cli, func() time.Time { return agora })
		delete(e.emVoo, "n1")
		e.lerFactoDeTentativa(facto)
		if e.tentativaDe("n2") != 2 || !e.tentativaPorVazio("n2") || e.factosDeTentativa != 1 || e.factosDeTentativaVazia != 1 || e.runDoNo("n2") != "plano~n2~2" {
			t.Fatalf("o facto lido do log fixa a tentativa corrente, a classe e o run que se segue: %d %v", e.tentativaDe("n2"), e.razoesDeTentativa)
		}
		return e
	}
	// ENTRE O FACTO E A SUBMISSÃO: o nó não conhece o run. Lê-se primeiro e submete-se uma vez.
	cli := &aos503Runner{}
	e := novo(cli)
	for i := 0; i < 3; i++ {
		if fechados, err := e.recolher(context.Background()); err != nil || fechados != 0 {
			t.Fatalf("passagem %d: o no continua em voo; fechados=%d err=%v", i+1, fechados, err)
		}
	}
	if fmt.Sprint(cli.submetidos) != "[plano~n2~2]" || cli.leituras[0] != "plano~n2~2" || e.factosDeTentativa != 1 {
		t.Fatalf("a retoma le o estado do id da tentativa e so depois a submete, UMA vez, sem outro facto: leituras=%v submetidos=%v", cli.leituras, cli.submetidos)
	}
	// O MESMO LOG com o interruptor DESTA classe desligado e o do AOS-503 ligado: não se retoma —
	// é a razão do facto, e não um interruptor qualquer, que decide.
	cli = &aos503Runner{}
	e = novo(cli)
	e.nt.modoVazia = novaTentativaOff
	if _, err := e.recolher(context.Background()); err != nil || len(cli.submetidos) != 0 {
		t.Fatalf("com esta classe desligada a tentativa por vazio nao e retomada, mesmo com o AOS-503 em on; submetidos=%v err=%v", cli.submetidos, err)
	}
	// E um facto do AOS-503 com SÓ esta classe ligada: também não.
	cli = &aos503Runner{}
	e = aos511Executor(t, "plano", cli, func() time.Time { return agora })
	delete(e.emVoo, "n2")
	e.nt.modo = novaTentativaOff
	e.lerFactoDeTentativa(plannerevents.NodeAttemptStartedPayload{PlanID: "plano-plan", NodeID: "n1", Attempt: 2, RetryOf: "plano~n1", Reason: plannerevents.AttemptReasonContractUnmetNoCall})
	if _, err := e.recolher(context.Background()); err != nil || len(cli.submetidos) != 0 || e.tentativaPorVazio("n1") {
		t.Fatalf("um facto do AOS-503 nao e retomado por esta classe estar ligada; submetidos=%v err=%v", cli.submetidos, err)
	}

	// ENTRE A SUBMISSÃO E A RECOLHA: o run existe e este processo não o submeteu. Só se segue com
	// a origem conferida; um run em curso sem origem espera, e não é re-submetido.
	emCurso := estadoDoRun{RunID: "plano~n2~2", Status: "in_progress"}
	cli = &aos503Runner{estados: map[string]estadoDoRun{"plano~n2~2": emCurso}}
	e = novo(cli)
	if fechados, err := e.recolher(context.Background()); err != nil || fechados != 0 || len(cli.submetidos) != 0 || e.conferidas["n2"] {
		t.Fatalf("um run em curso sem origem espera: nao se segue nem se re-submete; fechados=%d submetidos=%v", fechados, cli.submetidos)
	}
	emCurso.PlanAttempt = &origemDaTentativa{PlanRequest: "plano", Generation: 1, PlanID: "plano-plan", NodeID: "n2", Attempt: 2}
	cli.estados["plano~n2~2"] = emCurso
	if fechados, err := e.recolher(context.Background()); err != nil || fechados != 0 || !e.conferidas["n2"] || len(cli.submetidos) != 0 {
		t.Fatalf("com a origem que o no declara a conferir, o run segue-se: conferidas=%v err=%v", e.conferidas, err)
	}
	// A origem de OUTRO nó, de outro pedido ou de outra tentativa não confere.
	for nome, o := range map[string]origemDaTentativa{
		"outro-pedido":      {PlanRequest: "outro", Generation: 1, PlanID: "plano-plan", NodeID: "n2", Attempt: 2},
		"outro-no":          {PlanRequest: "plano", Generation: 1, PlanID: "plano-plan", NodeID: "n1", Attempt: 2},
		"outra-tentativa":   {PlanRequest: "plano", Generation: 1, PlanID: "plano-plan", NodeID: "n2", Attempt: 3},
		"outro-plano":       {PlanRequest: "plano", Generation: 1, PlanID: "outro-plan", NodeID: "n2", Attempt: 2},
		"geracao-posterior": {PlanRequest: "plano", Generation: 9, PlanID: "plano-plan", NodeID: "n2", Attempt: 2},
	} {
		o := o
		e = novo(&aos503Runner{})
		if got := e.tentativaDestePedido("n2", estadoDoRun{RunID: "plano~n2~2", Status: "completed", Terminated: true, PlanAttempt: &o}); got != origemAlheia {
			t.Errorf("%s: a origem nao e a desta tentativa deste pedido; veio %d", nome, got)
		}
	}
	e = novo(&aos503Runner{})
	if got := e.tentativaDestePedido("n2", estadoDoRun{RunID: "plano~n2~2", Status: "completed", Terminated: true}); got != origemAlheia {
		t.Fatalf("um run terminal sem origem nao e desta tentativa; veio %d", got)
	}
}

// TestAOS511_Metricas_PorClasseESemPartirAsDoAOS503: as séries desta classe têm nomes próprios e
// vocabulário fechado; as do AOS-503 não ganham valores por causa dela, nem por plano.
func TestAOS511_Metricas_PorClasseESemPartirAsDoAOS503(t *testing.T) {
	c := &medicaoDoContrato{}
	c.primeiraRespostaVazia()
	c.tentativaVaziaFeita(2, tentativaVoltouAFalhar)
	c.tentativaVaziaFeita(3, tentativaRecuperou)
	c.tentativaVaziaFeita(4, tentativaRecuperou)
	c.tentativaVaziaFeita(2, "um_desfecho_novo")
	c.tentativaVaziaRecusada(recusaNaoAnunciado)
	c.tentativaVaziaRecusada("um motivo com texto do no")
	c.tentativaVaziaEmObservacao()
	m := &metricasDoConsumo{series: map[string]float64{}}
	m.registarNovaTentativa(c)
	// Um plano recuperado SÓ por esta classe, e outro que esgotou SÓ nesta classe.
	m.registarPlanoComTentativas(&resumoDasTentativas{ligadaVazia: true, tentativas: 2, tentativasVazias: 2, recuperados: 1, recuperadosPorVazio: 1}, exitOK)
	m.registarPlanoComTentativas(&resumoDasTentativas{ligadaVazia: true, tentativas: 2, tentativasVazias: 2, esgotados: 1, esgotadosPorVazio: 1}, exitNosFalhados)
	texto := string(m.texto())
	for _, quer := range []string{
		metricaPrimeirasVazias + " 1\n",
		serie(metricaTentativasVazia, "tentativa", "2", "desfecho", tentativaVoltouAFalhar) + " 1\n",
		serie(metricaTentativasVazia, "tentativa", "3", "desfecho", tentativaRecuperou) + " 1\n",
		serie(metricaTentativasVaziaRecusadas, "causa", recusaNaoAnunciado) + " 1\n",
		metricaTentativasVaziaEmObservacao + " 1\n",
		metricaPlanosRecuperadosVazia + " 1\n",
		metricaPlanosEsgotadosVazia + " 1\n",
	} {
		if !strings.Contains("\n"+texto, "\n"+quer) {
			t.Fatalf("faltou %q:\n%s", quer, texto)
		}
	}
	for _, proibido := range []string{"um_desfecho_novo", "texto do no", `tentativa="4"`,
		metricaPrimeirasFalhas, metricaTentativas + "{", metricaNosRecuperados, metricaPlanosRecuperados + " ", metricaPlanosEsgotados + " ",
		metricaTentativasPorPlano, metricaTentativasRecusadas, metricaTentativasEmObservacao} {
		if strings.Contains(texto, proibido) {
			t.Fatalf("esta classe nao pode criar a serie %q (fora do vocabulario, ou do AOS-503):\n%s", proibido, texto)
		}
	}
	// AS DO AOS-503 FICAM COMO ERAM: o mesmo resumo de sempre dá as mesmas séries de sempre.
	antes := &metricasDoConsumo{series: map[string]float64{}}
	antes.registarPlanoComTentativas(&resumoDasTentativas{ligada: true, tentativas: 2, recuperados: 1}, exitOK)
	antes.registarPlanoComTentativas(&resumoDasTentativas{tentativas: 1, esgotados: 1}, exitNosFalhados)
	antes.registarPlanoComTentativas(&resumoDasTentativas{ligada: true}, exitOK)
	ta := string(antes.texto())
	for _, quer := range []string{
		metricaPlanosRecuperados + " 1\n", metricaPlanosEsgotados + " 1\n",
		serie(metricaTentativasPorPlano, "tentativas", "2") + " 1\n", serie(metricaTentativasPorPlano, "tentativas", "1") + " 1\n",
		serie(metricaTentativasPorPlano, "tentativas", "0") + " 1\n",
	} {
		if !strings.Contains("\n"+ta, "\n"+quer) {
			t.Fatalf("a serie do AOS-503 %q tinha de continuar a contar como antes:\n%s", quer, ta)
		}
	}
	if strings.Contains(ta, "vazia") {
		t.Fatalf("sem tentativas desta classe nao ha series dela:\n%s", ta)
	}
	// Um plano com as DUAS classes: cada série conta a sua parte.
	misto := &metricasDoConsumo{series: map[string]float64{}}
	misto.registarPlanoComTentativas(&resumoDasTentativas{ligada: true, ligadaVazia: true, tentativas: 3, tentativasVazias: 1, recuperados: 2, recuperadosPorVazio: 1}, exitOK)
	tm := string(misto.texto())
	for _, quer := range []string{metricaPlanosRecuperados + " 1\n", metricaPlanosRecuperadosVazia + " 1\n", serie(metricaTentativasPorPlano, "tentativas", "2") + " 1\n"} {
		if !strings.Contains("\n"+tm, "\n"+quer) {
			t.Fatalf("num plano com as duas classes faltou %q:\n%s", quer, tm)
		}
	}
	// Vazio ⇒ nada.
	vazio := &metricasDoConsumo{series: map[string]float64{}}
	vazio.registarTentativaVazia(&medicaoDoContrato{})
	if len(vazio.texto()) != 0 {
		t.Fatalf("sem medicoes desta classe o ficheiro e o de antes:\n%s", vazio.texto())
	}
}

// TestAOS511_Anuncio_OClienteLeOFioDoNo: o cliente lê do `GET /tools` REAL do nó (ficheiros de
// fio) se a classe é anunciada. Um nó anterior — o fio do AOS-502 — não a anuncia.
func TestAOS511_Anuncio_OClienteLeOFioDoNo(t *testing.T) {
	for nome, c := range map[string]struct {
		corpo      []byte
		tentativas int
		vazia      bool
	}{
		"no-novo-com-a-classe-ligada": {aos511FioDoNo(t, "tools-enforce-retry-2-empty"), 2, true},
		"no-do-AOS-502":               {aos503FioDoNo(t, "tools-enforce-retry-2"), 2, false},
		"no-sem-tecto":                {aos503Catalogo(t, 0), 0, false},
		"classe-sem-tecto-nao-conta":  {[]byte(`{"tools":[],"run_retry":{"max":0,"empty_output":true}}`), 0, false},
	} {
		t.Run(nome, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(c.corpo) }))
			defer srv.Close()
			cli := &nodeClient{base: srv.URL, http: srv.Client()}
			anuncio, err := cli.ContratoDeConclusao(context.Background())
			if err != nil || anuncio.tentativas != c.tentativas || anuncio.tentativaVazia != c.vazia {
				t.Fatalf("queria tentativas=%d vazia=%t; veio %+v (%v)", c.tentativas, c.vazia, anuncio, err)
			}
		})
	}
}

// TestAOS511_Facto_OConstrutorAceitaAsDuasRazoesESoElas: o vocabulário de `reason` tem dois
// valores; tudo o resto é recusado pelo construtor.
func TestAOS511_Facto_OConstrutorAceitaAsDuasRazoesESoElas(t *testing.T) {
	no := plan.Node{NodeID: "n2", Role: "summarizer"}
	base := plannerevents.NodeAttemptStartedPayload{PlanID: "p-plan", NodeID: "n2", Attempt: 2, RetryOf: "p~n2"}
	if fmt.Sprint(plannerevents.AttemptReasons()) != "[contract_unmet_no_call empty_output]" {
		t.Fatalf("o enum tem dois valores, por esta ordem: %v", plannerevents.AttemptReasons())
	}
	for _, r := range plannerevents.AttemptReasons() {
		p := base
		p.Reason = r
		if got, err := plannerevents.NewNodeAttemptStarted(p, no); err != nil || got.Reason != r {
			t.Fatalf("a razao %q e do enum: %v", r, err)
		}
	}
	for _, r := range []plannerevents.AttemptReason{"", "truncated", "Empty_Output", "empty_output ", "origem_vazia", "timed_out"} {
		p := base
		p.Reason = r
		if _, err := plannerevents.NewNodeAttemptStarted(p, no); !errors.Is(err, plannerevents.ErrInvalidNodeAttempt) {
			t.Fatalf("a razao %q esta fora do enum e tinha de ser recusada; veio %v", r, err)
		}
	}
}
