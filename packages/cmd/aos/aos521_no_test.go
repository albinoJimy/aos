package main

// AOS-521 — O DISJUNTOR DO CANDIDATO, medido pelo nó COMPOSTO (ADR-041 §2.9).
//
// O cliente de modelo sai de [parseModelFromEnv] com a pasta de perfis assinados, o interruptor do
// candidato e o modo do disjuntor; o nó é composto pelo [Bootstrap] e fala com um provider em
// httptest. A regra sozinha está em `aos521_regra_test.go`; o livro sozinho em
// `aos521_livro_test.go`; a inércia com o disjuntor desligado em `aos521_inercia_test.go`.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/canary"
	"github.com/aos-ref/substrate/eventstore"
)

// aos521ModeloDoCandidato é o modelo que o perfil assinado do candidato espera.
const aos521ModeloDoCandidato = "openai/modelo-novo-4.5"

// aos521No levanta um nó com o candidato a servir (`on`), o disjuntor no modo dado (vazio ⇒ a
// variável não existe) e a governação da rota no modo dado (vazio ⇒ a variável não existe). É o
// [aos520ComporComPasta] com essas duas variáveis.
func aos521No(t *testing.T, sub *aos520Substrato, disjuntor, governacao string, regs ...aos520Registo) *aos520No {
	t.Helper()
	aos520PastaCom(t, regs...)
	aos520Interruptor(t, "on")
	if disjuntor == "" {
		aos504SemVariavel(t, "AOS_MODEL_BREAKER")
	} else {
		t.Setenv("AOS_MODEL_BREAKER", disjuntor)
	}
	pinBreakerEnv(t, "0", "0", "0", "0")
	if governacao == "" {
		aos504SemVariavel(t, "AOS_MODEL_ROUTE_GOVERNANCE")
	} else {
		t.Setenv("AOS_MODEL_ROUTE_GOVERNANCE", governacao)
	}
	// A governação em imposição exige o host esperado do endpoint: o do api_base que o proxy declara.
	t.Setenv("AOS_MODEL_ROUTE_API_HOST", "")
	if governacao == "enforce" {
		t.Setenv("AOS_MODEL_ROUTE_API_HOST", "api.b.exemplo.test")
	}
	aos504SemVariavel(t, "AOS_MODEL_PROJECTION_VERSION")
	n := aos486ComporCom(t, "native", func(cfg *Config) {
		cfg.EventStore, cfg.EventStorePath = sub.store, ""
		cfg.DSARVault = sub.vault
		cfg.Approvers = sub.approvers
	})
	n.upstream.pede = "arquivo"
	n.upstream.responde = aos490RespostaDoProvider
	// O proxy declara, por omissão, o modelo que cada perfil espera.
	aos521Declarar(n, aos521ModeloDoCandidato)
	log := &bytes.Buffer{}
	n.svc.logMu.Lock()
	n.svc.logw = log
	n.svc.logMu.Unlock()
	// O livro do canary fala pelo log do ARRANQUE; aqui passa a falar para o mesmo sítio que o
	// serviço, para o teste ler o que o disjuntor diz. Antes de qualquer run.
	if c := n.node.canary; c != nil && c.livro != nil {
		c.livro.log = func(f string, a ...any) {
			n.svc.logMu.Lock()
			defer n.svc.logMu.Unlock()
			fmt.Fprintf(log, f+"\n", a...)
		}
	}
	return &aos520No{aos486No: n, log: log}
}

// aos521Declarar faz o proxy declarar, nos pedidos ao CANDIDATO, que serviu `doCandidato`; nos do
// titular declara sempre o modelo que o perfil do titular espera.
func aos521Declarar(n *aos486No, doCandidato string) {
	n.upstream.mu.Lock()
	defer n.upstream.mu.Unlock()
	n.upstream.cabecalhos = func(pedido int) map[string]string {
		corpos := n.upstream.pedidos()
		if pedido < len(corpos) && strings.Contains(string(corpos[pedido]), `"model":"`+aos520AliasCand+`"`) {
			return aos505NoCabecalhos(doCandidato, aos505NoAPIBaseB)
		}
		return aos505NoCabecalhos(aos505NoEsperado, aos505NoAPIBaseB)
	}
}

// aos521Avariar faz o provider responder com o estado HTTP dado aos pedidos do alias dado; zero
// repõe-no.
func aos521Avariar(n *aos486No, alias string, estado int) {
	n.upstream.mu.Lock()
	defer n.upstream.mu.Unlock()
	if estado == 0 {
		n.upstream.avaria = nil
		return
	}
	n.upstream.avaria = func(corpo []byte) int {
		if strings.Contains(string(corpo), `"model":"`+alias+`"`) {
			return estado
		}
		return 0
	}
}

// aos521Submeter submete um run e espera que acabe, bem ou mal. Devolve o erro do run.
func aos521Submeter(t *testing.T, n *aos520No, runID string) error {
	t.Helper()
	rec := postJSON(n.h, "POST", "/runs", map[string]any{"run_id": runID, "objective": aos490Objectivo, "principal_nhi": durAgent, "credential": n.tok, "max_turns": 3, "tools": []string{"arquivo"}, "completion_requires": []string{"arquivo"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs %s: %d %s", runID, rec.Code, rec.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	oc, ok, err := n.svc.Wait(ctx, runID)
	if err != nil || !ok {
		t.Fatalf("Wait(%s): ok=%v err=%v", runID, ok, err)
	}
	return oc.Err
}

// aos521Papel devolve o papel e a razão com que o run ficou fixado.
func aos521Papel(t *testing.T, n *aos520No, runID string) (papel, razao string) {
	t.Helper()
	fix, tem := aos520cFixacao(t, n.node.EventStore, runID)
	if !tem {
		t.Fatalf("o run %s nao tem run.model_pinned", runID)
	}
	return fix.Role, fix.Reason
}

// aos521Gauge lê do /metrics o valor de uma família sem rótulos; `tem` falso ⇒ a família não existe.
func aos521Gauge(t *testing.T, n *aos520No, familia string) (valor string, tem bool) {
	t.Helper()
	for _, linha := range strings.Split(aos520cMetricas(t, n.aos486No), "\n") {
		if strings.HasPrefix(linha, familia+" ") {
			return strings.TrimPrefix(linha, familia+" "), true
		}
	}
	return "", false
}

// aos521Desfechos devolve os desfechos que o livro do nó tem, por run.
func aos521Desfechos(t *testing.T, n *aos520No) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, d := range aos521Marcas(t, n.node.EventStore, EventTypeModelBreakerRunOutcome) {
		out[d.RunID] = d.Signal
	}
	return out
}

// O PROVIDER QUE DEGRADA A MEIO, EM `enforce`. O candidato é bom durante quatro runs e depois passa
// a responder 503. O disjuntor abre AO TERCEIRO run falhado — nem antes, nem depois —, a abertura
// fica no livro com a janela, e a partir daí nenhum plano novo vai para o candidato: zero em 200
// identificadores que a função lhe daria (a fatia é 100%), e os runs novos correm no titular.
func TestAOS521_No_Enforce_OProviderQueDegradaAMeio(t *testing.T) {
	n := aos521No(t, aos520NovoSubstrato(t), "enforce", "observe", aos520cCand(100))
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("bom-%d", i)
		if err := aos521Submeter(t, n, id); err != nil {
			t.Fatalf("run bom %d: %v", i, err)
		}
		if papel, _ := aos521Papel(t, n, id); papel != "candidato" {
			t.Fatalf("antes da degradacao o run %d e do %s", i, papel)
		}
	}
	aos521Avariar(n.aos486No, aos520AliasCand, http.StatusServiceUnavailable)
	for i := 1; i <= 3; i++ {
		if aberto, _ := aos521Gauge(t, n, "aos_model_breaker_open"); aberto != "0" {
			t.Fatalf("antes do %d.o run falhado o disjuntor ja estava aberto (%s)", i, aberto)
		}
		id := fmt.Sprintf("mau-%d", i)
		if err := aos521Submeter(t, n, id); err == nil {
			t.Fatalf("o run %d depois da degradacao tinha de falhar", i)
		}
		if papel, _ := aos521Papel(t, n, id); papel != "candidato" {
			t.Fatalf("o %d.o run falhado tinha de ser do candidato: %s", i, papel)
		}
	}
	abertas := aos521Marcas(t, n.node.EventStore, EventTypeModelBreakerOpened)
	if len(abertas) != 1 || abertas[0].Rule != regraFalhasNaJanela || abertas[0].EntryDigest != n.node.canary.candidato.entryDigest ||
		abertas[0].Counters[sinalErroDoProvider] != 3 || len(abertas[0].Window) != 7 || abertas[0].Window[6] != (itemDaJanela{"mau-3", sinalErroDoProvider}) {
		t.Fatalf("a abertura: %+v", abertas)
	}
	desfechos := aos521Desfechos(t, n)
	for id, quer := range map[string]string{"bom-1": sinalCumprido, "bom-4": sinalCumprido, "mau-1": sinalErroDoProvider, "mau-3": sinalErroDoProvider} {
		if desfechos[id] != quer {
			t.Fatalf("desfecho de %s = %q; quer %q (%v)", id, desfechos[id], quer, desfechos)
		}
	}
	for familia, quer := range map[string]string{"aos_model_breaker_open": "1", "aos_model_breaker_would_open": "1", "aos_model_breaker_window_failures": "3", "aos_model_breaker_window_units": "7", "aos_model_breaker_unrecorded_total": "0"} {
		if got, tem := aos521Gauge(t, n, familia); !tem || got != quer {
			t.Fatalf("%s = %q (existe=%v); quer %s", familia, got, tem, quer)
		}
	}
	if !strings.Contains(aos520cMetricas(t, n.aos486No), `aos_model_breaker_signals{signal="erro_do_provider"} 3`) {
		t.Fatal("o contador do sinal erro_do_provider nao esta a 3 no /metrics")
	}
	if !strings.Contains(n.lido(), "DISJUNTOR DO MODELO CANDIDATO ABERTO") {
		t.Fatalf("a abertura nao ficou no log do no:\n%s", n.lido())
	}

	// A PARTIR DAQUI NENHUM PLANO NOVO VAI PARA O CANDIDATO.
	atribuidos := len(aos520EventosDoLivro(t, n.node.EventStore, EventTypeModelCanaryPlanAssigned))
	for i := 0; i < 200; i++ {
		chave := fmt.Sprintf("plano-depois-%03d", i)
		if canary.Choose(chave, 100) != canary.RoleCandidate {
			t.Fatalf("a funcao tinha de dar %q ao candidato", chave)
		}
		d, err := n.node.canary.livro.decidir(context.Background(), n.node.canary.candidato, chaveDeRun(chave), 0)
		if err != nil || d.papel != canary.RoleIncumbent || d.razao != razaoDisjuntorAberto {
			t.Fatalf("com o disjuntor aberto o plano %d foi para %+v (%v)", i, d, err)
		}
	}
	if depois := len(aos520EventosDoLivro(t, n.node.EventStore, EventTypeModelCanaryPlanAssigned)); depois != atribuidos {
		t.Fatalf("depois da abertura foram atribuidos %d plano(s) ao candidato", depois-atribuidos)
	}
	// E um run novo a sério corre e conclui NO TITULAR, com a razão própria gravada.
	desde := len(n.upstream.pedidos())
	if err := aos521Submeter(t, n, "depois-da-abertura"); err != nil {
		t.Fatalf("o run novo, no titular, tinha de concluir: %v", err)
	}
	if papel, razao := aos521Papel(t, n, "depois-da-abertura"); papel != "titular" || razao != razaoDisjuntorAberto {
		t.Fatalf("o run novo ficou %s/%s; quer titular/%s", papel, razao, razaoDisjuntorAberto)
	}
	if m := aos520cModelosPedidos(t, n.aos486No, desde); !aos520cTodos(m, aos486Modelo) {
		t.Fatalf("o run novo pediu %q; quer o titular", m)
	}
}

// O CONTROLO NEGATIVO DA DEGRADAÇÃO: o MESMO cenário em `observe` continua a dar planos ao candidato
// — o teste do `enforce` não passa por o candidato nunca ter recebido nada. E fica gravado o que o
// `enforce` teria feito, com as mesmas contagens.
func TestAOS521_No_Observe_OMesmoCenarioNaoCorta(t *testing.T) {
	n := aos521No(t, aos520NovoSubstrato(t), "observe", "observe", aos520cCand(100))
	for i := 1; i <= 4; i++ {
		if err := aos521Submeter(t, n, fmt.Sprintf("bom-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	aos521Avariar(n.aos486No, aos520AliasCand, http.StatusServiceUnavailable)
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("mau-%d", i)
		if err := aos521Submeter(t, n, id); err == nil {
			t.Fatalf("o run %d tinha de falhar", i)
		}
		if papel, razao := aos521Papel(t, n, id); papel != "candidato" || razao != razaoFuncao {
			t.Fatalf("em observe o run %d ficou %s/%s; o candidato continua a receber", i, papel, razao)
		}
	}
	teria := aos521Marcas(t, n.node.EventStore, EventTypeModelBreakerWouldOpen)
	if len(teria) != 1 || teria[0].Rule != regraFalhasNaJanela || teria[0].Counters[sinalErroDoProvider] != 3 || len(teria[0].Window) != 7 {
		t.Fatalf("«teria aberto»: %+v", teria)
	}
	if n := len(aos521Marcas(t, n.node.EventStore, EventTypeModelBreakerOpened)); n != 0 {
		t.Fatalf("em observe gravou-se a abertura: %d", n)
	}
	if aberto, _ := aos521Gauge(t, n, "aos_model_breaker_open"); aberto != "0" {
		t.Fatalf("em observe aos_model_breaker_open = %s", aberto)
	}
	if teria, _ := aos521Gauge(t, n, "aos_model_breaker_would_open"); teria != "1" {
		t.Fatalf("em observe aos_model_breaker_would_open = %s; quer 1", teria)
	}
	if !strings.Contains(n.lido(), "TERIA ABERTO") {
		t.Fatal("«teria aberto» nao ficou no log do no")
	}
}

// VINTE RESPOSTAS 429 SEGUIDAS, E O DISJUNTOR FECHADO. O limite de ritmo é da conta, não do modelo:
// os runs falham, nenhum entra na janela, e o candidato continua a receber.
func TestAOS521_No_OLimiteDeRitmoNaoConta(t *testing.T) {
	n := aos521No(t, aos520NovoSubstrato(t), "enforce", "observe", aos520cCand(100))
	aos521Avariar(n.aos486No, aos520AliasCand, http.StatusTooManyRequests)
	for i := 1; i <= 20; i++ {
		if err := aos521Submeter(t, n, fmt.Sprintf("ritmo-%02d", i)); err == nil {
			t.Fatalf("o run %d tinha de falhar com o 429", i)
		}
	}
	if d := aos521Desfechos(t, n); len(d) != 0 {
		t.Fatalf("um 429 entrou na janela: %v", d)
	}
	if n := len(aos521Marcas(t, n.node.EventStore, EventTypeModelBreakerOpened)); n != 0 {
		t.Fatal("vinte 429 abriram o disjuntor")
	}
	aos521Avariar(n.aos486No, aos520AliasCand, 0)
	if err := aos521Submeter(t, n, "depois-do-ritmo"); err != nil {
		t.Fatal(err)
	}
	if papel, razao := aos521Papel(t, n, "depois-do-ritmo"); papel != "candidato" || razao != razaoFuncao {
		t.Fatalf("depois de vinte 429 o run novo ficou %s/%s; quer o candidato", papel, razao)
	}
	// CONTROLO: o mesmo provider a responder 500 abre ao terceiro.
	aos521Avariar(n.aos486No, aos520AliasCand, http.StatusInternalServerError)
	for i := 1; i <= 3; i++ {
		_ = aos521Submeter(t, n, fmt.Sprintf("erro-%d", i))
	}
	if n := len(aos521Marcas(t, n.node.EventStore, EventTypeModelBreakerOpened)); n != 1 {
		t.Fatalf("tres 500 tinham de abrir: %d marca(s)", n)
	}
}

// O MODELO SERVIDO DIFERENTE ABRE NO TURNO. Com a governação da rota em observação, o proxy declara
// nos pedidos do candidato outro modelo: o disjuntor abre no PRIMEIRO turno — antes de o run dar o
// segundo —, com a janela vazia; o run em curso ACABA com o modelo fixado; e o run seguinte é do
// titular. Com a governação em imposição o turno é recusado, e o disjuntor abre na mesma.
func TestAOS521_No_OModeloServidoDiferenteAbreNoTurno(t *testing.T) {
	for _, governacao := range []string{"observe", "enforce"} {
		t.Run(governacao, func(t *testing.T) {
			n := aos521No(t, aos520NovoSubstrato(t), "enforce", governacao, aos520cCand(100))
			aos521Declarar(n.aos486No, "openai/outro-modelo")
			// No SEGUNDO pedido do run o livro já tem de ter a abertura: o disjuntor abriu no turno.
			abertasNoSegundoPedido := -1
			n.upstream.mu.Lock()
			responde := n.upstream.responde
			pedidos := 0
			n.upstream.responde = func(pedeTool bool, tool string) []byte {
				pedidos++
				if pedidos == 2 {
					abertasNoSegundoPedido = len(aos521Marcas(t, n.node.EventStore, EventTypeModelBreakerOpened))
				}
				return responde(pedeTool, tool)
			}
			n.upstream.mu.Unlock()

			err := aos521Submeter(t, n, "run-trocado")
			abertas := aos521Marcas(t, n.node.EventStore, EventTypeModelBreakerOpened)
			if len(abertas) != 1 || abertas[0].Rule != regraModeloDiferente || len(abertas[0].Window) != 0 || abertas[0].Counters[sinalModeloDiferente] != 1 {
				t.Fatalf("a abertura pelo modelo diferente: %+v", abertas)
			}
			if turnos := aos521Marcas(t, n.node.EventStore, EventTypeModelBreakerRouteMismatch); len(turnos) != 1 || turnos[0].RunID != "run-trocado" {
				t.Fatalf("o turno gravado: %+v", turnos)
			}
			modelos := aos520cModelosPedidos(t, n.aos486No, 0)
			if governacao == "observe" {
				// O RUN EM CURSO ACABA, com o modelo fixado.
				if err != nil || len(modelos) != 2 || !aos520cTodos(modelos, aos520AliasCand) {
					t.Fatalf("o run em curso tinha de acabar no candidato: err=%v pedidos=%q", err, modelos)
				}
				if abertasNoSegundoPedido != 1 {
					t.Fatalf("no segundo pedido do run o livro tinha %d abertura(s); o disjuntor tinha de abrir no primeiro turno", abertasNoSegundoPedido)
				}
			} else if err == nil || len(modelos) != 1 {
				t.Fatalf("em imposicao o turno e recusado: err=%v pedidos=%q", err, modelos)
			}
			aos521Declarar(n.aos486No, aos521ModeloDoCandidato)
			if err := aos521Submeter(t, n, "run-seguinte"); err != nil {
				t.Fatal(err)
			}
			if papel, razao := aos521Papel(t, n, "run-seguinte"); papel != "titular" || razao != razaoDisjuntorAberto {
				t.Fatalf("o run seguinte ficou %s/%s", papel, razao)
			}
		})
	}
}

// O PLANO A MEIO É RECUSADO, E O RUN JÁ FIXADO CONTINUA. Com o disjuntor aberto: um run NOVO de um
// plano que o livro diz atribuído ao candidato fecha `failed` com a causa própria, sem um pedido ao
// provider — não passa para o titular. Um run do candidato que JÁ estava fixado e é re-submetido
// (a retoma por outro nome) continua no candidato: o disjuntor não mata o que está em curso.
func TestAOS521_No_OPlanoAMeioERecusado_EORunFixadoContinua(t *testing.T) {
	sub := aos520NovoSubstrato(t)
	n := aos521No(t, sub, "enforce", "observe", aos520cCand(100))
	// Um plano atribuído ao candidato, com nós por correr.
	if d, err := n.node.canary.livro.decidir(context.Background(), n.node.canary.candidato, chaveDeRun("plano-a-meio"), 0); err != nil || d.papel != canary.RoleCandidate {
		t.Fatalf("atribuir o plano: %+v %v", d, err)
	}
	// Um run do candidato em curso: deu o turno 1 e o processo caiu.
	aos520cTurnoUmECrash(t, n, "run-em-curso")
	// O disjuntor abre.
	aos521Avariar(n.aos486No, aos520AliasCand, http.StatusBadGateway)
	for i := 1; i <= 3; i++ {
		_ = aos521Submeter(t, n, fmt.Sprintf("mau-%d", i))
	}
	aos521Avariar(n.aos486No, aos520AliasCand, 0)
	if aberto, _ := aos521Gauge(t, n, "aos_model_breaker_open"); aberto != "1" {
		t.Fatalf("o disjuntor tinha de estar aberto: %s", aberto)
	}

	aos520cRecusado(t, n, "plano-a-meio", causaDisjuntorAberto)

	desde := len(n.upstream.pedidos())
	n.correr(t, "run-em-curso", nil)
	if modelos := aos520cModelosPedidos(t, n.aos486No, desde); len(modelos) == 0 || !aos520cTodos(modelos, aos520AliasCand) {
		t.Fatalf("o run ja fixado no candidato, re-submetido com o disjuntor aberto, pediu %q; quer o candidato", modelos)
	}
	if st := aos520Estado(t, n, "run-em-curso"); st != state.Complete {
		t.Fatalf("o run em curso ficou %q; quer complete", st)
	}
}

// SOBREVIVE A UM REINÍCIO, NÃO FECHA COM `off`, E SÓ UM REGISTO NOVO REABRE. Aberto num nó; o nó é
// recriado (a) igual, (b) com o disjuntor desligado, (c) em observação: em todos o run novo é do
// titular, com a razão do disjuntor. Recriado com um registo NOVO — outra assinatura, outro digest —
// o candidato volta a receber. E reposto o registo ANTIGO, continua aberto.
func TestAOS521_No_ReinicioOffERegistoNovo(t *testing.T) {
	sub := aos520NovoSubstrato(t)
	a := aos521No(t, sub, "enforce", "observe", aos520cCand(100))
	aos521Avariar(a.aos486No, aos520AliasCand, http.StatusBadGateway)
	for i := 1; i <= 3; i++ {
		_ = aos521Submeter(t, a, fmt.Sprintf("mau-%d", i))
	}
	antigo := a.node.canary.candidato.entryDigest
	a.fechar(t)

	for i, modo := range []string{"enforce", "", "observe"} {
		b := aos521No(t, sub, modo, "observe", aos520cCand(100))
		if b.node.canary.candidato.entryDigest != antigo {
			t.Fatal("o teste precisa do MESMO registo depois do reinicio")
		}
		id := fmt.Sprintf("depois-do-reinicio-%d", i)
		if err := aos521Submeter(t, b, id); err != nil {
			t.Fatalf("modo %q: %v", modo, err)
		}
		if papel, razao := aos521Papel(t, b, id); papel != "titular" || razao != razaoDisjuntorAberto {
			t.Fatalf("modo %q depois do reinicio: o run novo ficou %s/%s; o disjuntor nao fecha sozinho", modo, papel, razao)
		}
		if _, tem := aos521Gauge(t, b, "aos_model_breaker_open"); tem != (modo != "") {
			t.Fatalf("modo %q: a familia aos_model_breaker_open existe=%v", modo, tem)
		}
		if modo != "" {
			if aberto, _ := aos521Gauge(t, b, "aos_model_breaker_open"); aberto != "1" {
				t.Fatalf("modo %q depois do reinicio: aos_model_breaker_open = %s", modo, aberto)
			}
			if falhas, _ := aos521Gauge(t, b, "aos_model_breaker_window_failures"); falhas != "3" {
				t.Fatalf("modo %q depois do reinicio: as contagens mudaram (%s falhas)", modo, falhas)
			}
		}
		b.fechar(t)
	}
	if n := len(aos521Marcas(t, sub.store, EventTypeModelBreakerOpened)); n != 1 {
		t.Fatalf("marcas de abertura depois de tres reinicios = %d; quer 1", n)
	}

	// UM REGISTO NOVO, ASSINADO, COMEÇA FECHADO. (Outra fatia ⇒ outro registo ⇒ outro digest.)
	c := aos521No(t, sub, "enforce", "observe", aos520cCand(99))
	if c.node.canary.candidato.entryDigest == antigo {
		t.Fatal("o registo novo tinha de ter outro digest")
	}
	if err := aos521Submeter(t, c, "com-o-registo-novo"); err != nil {
		t.Fatal(err)
	}
	if papel, _ := aos521Papel(t, c, "com-o-registo-novo"); papel != "candidato" {
		t.Fatalf("com um registo novo o candidato volta a receber: %s", papel)
	}
	if aberto, _ := aos521Gauge(t, c, "aos_model_breaker_open"); aberto != "0" {
		t.Fatalf("o registo novo comeca fechado: %s", aberto)
	}
	c.fechar(t)

	// O REGISTO ANTIGO REPOSTO CONTINUA ABERTO. (A chave de assinatura é nova em cada pasta de teste,
	// mas o digest do registo não a inclui: a mesma fatia e o mesmo perfil dão o mesmo registo.)
	d := aos521No(t, sub, "enforce", "observe", aos520cCand(100))
	if d.node.canary.candidato.entryDigest != antigo {
		t.Fatal("o teste precisa de o registo reposto ter o digest do antigo")
	}
	if err := aos521Submeter(t, d, "com-o-registo-antigo-reposto"); err != nil {
		t.Fatal(err)
	}
	if papel, razao := aos521Papel(t, d, "com-o-registo-antigo-reposto"); papel != "titular" || razao != razaoDisjuntorAberto {
		t.Fatalf("o registo antigo reposto reabriu: %s/%s", papel, razao)
	}
}

// O TITULAR NUNCA É TOCADO. Com o disjuntor em `enforce` e a fatia a 10%, o titular falha das
// maneiras que o nó vê num run real — resposta recusada, erro do provider, modelo servido diferente
// — e, pela função que leva os desfechos ao livro, com os CINCO sinais: o livro não ganha um evento
// do disjuntor, nenhuma leitura é feita, e o titular continua a receber todos os runs.
func TestAOS521_No_OTitularNuncaETocado(t *testing.T) {
	sub := aos520NovoSubstrato(t)
	conta := &aos520StoreQueConta{Store: sub.store}
	aos520PastaCom(t, aos520cCand(10))
	aos520Interruptor(t, "on")
	t.Setenv("AOS_MODEL_BREAKER", "enforce")
	pinBreakerEnv(t, "0", "0", "0", "0")
	t.Setenv("AOS_MODEL_ROUTE_GOVERNANCE", "observe")
	t.Setenv("AOS_MODEL_ROUTE_API_HOST", "")
	aos504SemVariavel(t, "AOS_MODEL_PROJECTION_VERSION")
	n486 := aos486ComporCom(t, "native", func(cfg *Config) {
		cfg.EventStore, cfg.EventStorePath = conta, ""
		cfg.DSARVault = sub.vault
		cfg.Approvers = sub.approvers
	})
	n486.upstream.pede = "arquivo"
	n486.upstream.responde = aos490RespostaDoProvider
	log := &bytes.Buffer{}
	n486.svc.logMu.Lock()
	n486.svc.logw = log
	n486.svc.logMu.Unlock()
	n := &aos520No{aos486No: n486, log: log}
	if n.node.disjuntorLigado() == nil {
		t.Fatal("o teste precisa do disjuntor ligado")
	}
	semEventos := func(quando string) {
		t.Helper()
		evs, err := sub.store.Read(context.Background(), canaryStream, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			if tipoDoDisjuntor(ev.Type) {
				t.Fatalf("%s: o livro ganhou um %s por causa do titular", quando, ev.Type)
			}
		}
		if v := n.node.disjuntorLigado(); v.abertoVisto.Load() != 0 || v.disparouVisto.Load() != 0 || v.falhasVistas.Load() != 0 || v.unidadesVistas.Load() != 0 || v.naoGravadas.Load() != 0 {
			t.Fatalf("%s: o estado do disjuntor mudou por causa do titular", quando)
		}
	}
	// Ids que a função dá ao titular a 10% (ADR-041 §2.3).
	k := 0
	proximo := func() string {
		for ; k < 1000; k++ {
			id := fmt.Sprintf("titular-%03d", k)
			if canary.Choose(id, 10) == canary.RoleIncumbent {
				k++
				return id
			}
		}
		t.Fatal("sem ids do titular")
		return ""
	}
	// (F4) O titular responde 503, três vezes.
	aos521Avariar(n.aos486No, aos486Modelo, http.StatusServiceUnavailable)
	for i := 0; i < 4; i++ {
		id := proximo()
		if err := aos521Submeter(t, n, id); err == nil {
			t.Fatal("o run do titular tinha de falhar com o 503")
		}
		if papel, _ := aos521Papel(t, n, id); papel != "titular" {
			t.Fatalf("o run %s e do %s", id, papel)
		}
	}
	semEventos("titular com 503")
	aos521Avariar(n.aos486No, aos486Modelo, 0)
	// (F3) A resposta do titular é recusada pelo gateway, quatro vezes.
	n.upstream.mu.Lock()
	n.upstream.responde = func(bool, string) []byte { return []byte(`{isto nao e json`) }
	n.upstream.mu.Unlock()
	for i := 0; i < 4; i++ {
		if err := aos521Submeter(t, n, proximo()); err == nil {
			t.Fatal("o run do titular tinha de falhar com a resposta recusada")
		}
	}
	semEventos("titular com a resposta recusada")
	n.upstream.mu.Lock()
	n.upstream.responde = aos490RespostaDoProvider
	// (F5) O proxy declara, nos pedidos do TITULAR, outro modelo.
	n.upstream.cabecalhos = func(int) map[string]string { return aos505NoCabecalhos("openai/outro-modelo", aos505NoAPIBaseB) }
	n.upstream.mu.Unlock()
	for i := 0; i < 2; i++ {
		id := proximo()
		if err := aos521Submeter(t, n, id); err != nil {
			t.Fatalf("com a governacao em observacao o run do titular conclui: %v", err)
		}
		if papel, _ := aos521Papel(t, n, id); papel != "titular" {
			t.Fatalf("o run %s e do %s", id, papel)
		}
	}
	if !strings.Contains(aos520cMetricas(t, n.aos486No), `aos_model_route_checks_total{result="diferente"`) {
		t.Fatal("o teste precisa de a governacao da rota ter visto o modelo diferente")
	}
	semEventos("titular com o modelo servido diferente")

	// OS CINCO SINAIS, pela função que leva os desfechos ao livro: um run do titular sai sem uma
	// leitura, seja qual for o desfecho.
	antes := conta.lidas(canaryStream)
	titular := agentruntime.Goal{RunID: "x", ModelPinnedBy: versaoDaEscolha}
	titular.Model.ModelID = aos486Modelo
	negativo := func(r agentruntime.OutcomeReason) agentruntime.Result {
		return agentruntime.Result{Unfulfilled: true, Verdict: &agentruntime.Verdict{Reason: r}}
	}
	for nome, c := range map[string]struct {
		res agentruntime.Result
		err error
	}{
		"F1": {negativo(agentruntime.OutcomeContractNoCall), nil},
		"F2": {negativo(agentruntime.OutcomeEmptyOutput), nil},
		"F3": {agentruntime.Result{}, errors.New("x")},
		"F4": {agentruntime.Result{}, errors.New("y")},
		"F5": {agentruntime.Result{}, errors.New("z")},
	} {
		n.svc.observarDesfechoNoDisjuntor("run-do-titular-"+nome, titular, state.Failed, c.res, c.err, false)
	}
	// CONTROLO: o mesmo desfecho num Goal do CANDIDATO chega a ler (o run não existe, e por isso não
	// grava) — a função não sai antes por acaso.
	doCandidato := titular
	doCandidato.Model.ModelID = aos520AliasCand
	n.svc.observarDesfechoNoDisjuntor("run-que-nao-existe", doCandidato, state.Failed, negativo(agentruntime.OutcomeContractNoCall), nil, false)
	if conta.lidas("run-que-nao-existe") == 0 {
		t.Fatal("o controlo nao leu o run do candidato: o teste do titular nao prova nada")
	}
	if conta.lidas(canaryStream) != antes {
		t.Fatal("um desfecho do titular fez ler o livro")
	}
	for _, nome := range []string{"F1", "F2", "F3", "F4", "F5"} {
		if conta.lidas("run-do-titular-"+nome) != 0 {
			t.Fatalf("o desfecho %s do titular fez ler o stream do run", nome)
		}
	}
	// O TITULAR CONTINUA A RECEBER.
	n.upstream.mu.Lock()
	n.upstream.cabecalhos = nil
	n.upstream.mu.Unlock()
	aos521Declarar(n.aos486No, aos521ModeloDoCandidato)
	id := proximo()
	if err := aos521Submeter(t, n, id); err != nil {
		t.Fatalf("o titular tinha de continuar a servir: %v", err)
	}
	if papel, razao := aos521Papel(t, n, id); papel != "titular" || razao != razaoFuncao {
		t.Fatalf("o run do titular ficou %s/%s", papel, razao)
	}
	// E o candidato também: o disjuntor não abriu.
	if err := aos521Submeter(t, n, "0"); err != nil {
		t.Fatal(err)
	}
	if papel, _ := aos521Papel(t, n, "0"); papel != "candidato" {
		t.Fatalf("o run \"0\" e do candidato a 10%%: ficou %s", papel)
	}
}

// A UNIDADE SAI DO LOG DO RUN. O registo do candidato e a unidade da janela lêem-se do que o nó
// selou no stream do run: o `run.model_pinned` (com o papel) e o `run.plan_origin` (a tentativa). Uma
// nova tentativa pertence à unidade da PRIMEIRA; um run do titular, ou sem fixação, não é do
// candidato.
func TestAOS521_CandidatoDoRun_LidoDoLog(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	fixar := func(runID, papel string) {
		t.Helper()
		p, _ := json.Marshal(modeloFixadoPayload{V: "1.0", Role: papel, Alias: aos520Cand.alias, ProfileDigest: aos520Cand.profileDigest, EntryDigest: aos520Cand.entryDigest, SharePercent: 10, Function: versaoDaEscolha, Reason: razaoFuncao, PlanKey: "p", PlanKeyKind: tipoDePedido})
		if _, err := es.Append(ctx, runID, eventstore.EventInput{Type: EventTypeRunModelPinned, Payload: p, RunID: runID, StepID: passoDoModeloFixado, Producer: eventstore.Producer{NHIID: modeloFixadoNHI}}); err != nil {
			t.Fatal(err)
		}
	}
	origem := func(runID string, tentativa int, emissor string) {
		t.Helper()
		o := origemDoRunFilho{Versao: versaoDaOrigem, Pedido: refDoPedidoDeOrigem{Stream: "s", RunID: "pedido-1", Geracao: 1}, PlanID: "plano", NodeID: "no a"}
		if tentativa >= 2 {
			o.Attempt, o.RetryOf = tentativa, idDaTentativa("pedido-1", "no a", tentativa-1)
		}
		p, _ := json.Marshal(o)
		if _, err := es.Append(ctx, runID, eventstore.EventInput{Type: EventTypeRunPlanOrigin, Payload: p, RunID: runID, StepID: "origem", Producer: eventstore.Producer{NHIID: emissor}}); err != nil {
			t.Fatal(err)
		}
	}
	primeira, segunda, terceira := idDaTentativa("pedido-1", "no a", 1), idDaTentativa("pedido-1", "no a", 2), idDaTentativa("pedido-1", "no a", 3)
	fixar(primeira, "candidato")
	origem(primeira, 1, origemNHI)
	fixar(segunda, "candidato")
	origem(segunda, 2, origemNHI)
	fixar(terceira, "candidato")
	origem(terceira, 3, origemNHI)
	fixar("avulso", "candidato")
	fixar("do-titular", "titular")
	fixar("origem-forjada~x~2", "candidato")
	origem("origem-forjada~x~2", 2, "nhi:outro/emissor")
	// Um `run.model_pinned` que NÃO é do nó (outro emissor) não faz de um run um run do candidato.
	forjado, _ := json.Marshal(modeloFixadoPayload{V: "1.0", Role: "candidato", Alias: aos520Cand.alias, ProfileDigest: aos520Cand.profileDigest, EntryDigest: aos520Cand.entryDigest, Function: versaoDaEscolha, Reason: razaoFuncao, PlanKey: "p", PlanKeyKind: tipoDeRun})
	if _, err := es.Append(ctx, "fixacao-forjada", eventstore.EventInput{Type: EventTypeRunModelPinned, Payload: forjado, RunID: "fixacao-forjada", StepID: "x", Producer: eventstore.Producer{NHIID: "nhi:outro/emissor"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := es.Append(ctx, "sem-fixacao", eventstore.EventInput{Type: "outra.coisa", Payload: []byte(`{}`), RunID: "sem-fixacao", StepID: "x"}); err != nil {
		t.Fatal(err)
	}
	for runID, quer := range map[string]struct {
		unidade string
		ok      bool
	}{
		primeira:             {primeira, true},
		segunda:              {primeira, true},
		terceira:             {primeira, true},
		"avulso":             {"avulso", true},
		"do-titular":         {"", false},
		"sem-fixacao":        {"", false},
		"fixacao-forjada":    {"", false},
		"origem-forjada~x~2": {"origem-forjada~x~2", true}, // um `run.plan_origin` que não é do nó não conta
	} {
		reg, unidade, ok, err := candidatoDoRun(ctx, es, runID)
		if err != nil || ok != quer.ok || unidade != quer.unidade {
			t.Fatalf("%s: unidade=%q ok=%v err=%v; quer %q %v", runID, unidade, ok, err, quer.unidade, quer.ok)
		}
		if ok && reg != aos520Cand.registo() {
			t.Fatalf("%s: registo %+v; quer %+v", runID, reg, aos520Cand.registo())
		}
	}
	if _, _, ok, err := candidatoDoRun(ctx, es, "run-que-nao-existe"); err == nil || ok {
		t.Fatalf("um run cujo stream nao se le nao e do candidato, e o erro sobe: ok=%v err=%v", ok, err)
	}
}

// O ARRANQUE. Um valor fora do vocabulário não arranca, com ou sem gateway; `enforce` com a
// governação da rota desligada não arranca, com causa própria; `observe` sem ela arranca e o banner
// di-lo; `off` não diz nada.
func TestAOS521_Arranque_ModosEGovernacaoDaRota(t *testing.T) {
	compor := func(t *testing.T, disjuntor, governacao string) (*perfisAssinados, error) {
		aos519AmbienteDoGateway(t)
		aos520PastaCom(t, aos520cCand(10))
		aos520Interruptor(t, "on")
		if disjuntor == "" {
			aos504SemVariavel(t, "AOS_MODEL_BREAKER")
		} else {
			t.Setenv("AOS_MODEL_BREAKER", disjuntor)
		}
		if governacao == "" {
			aos504SemVariavel(t, "AOS_MODEL_ROUTE_GOVERNANCE")
		} else {
			t.Setenv("AOS_MODEL_ROUTE_GOVERNANCE", governacao)
		}
		cliente, _, err := parseModelFromEnv(false)
		if err != nil {
			return nil, err
		}
		return perfisDoCliente(cliente), nil
	}
	if _, err := compor(t, "talvez", "observe"); !errors.Is(err, ErrBadModelBreaker) {
		t.Fatalf("valor invalido: %v", err)
	}
	t.Run("valor invalido sem gateway", func(t *testing.T) {
		t.Setenv("AOS_MODEL_ENDPOINT", "")
		t.Setenv("AOS_MODEL_BREAKER", "ligado")
		if _, _, err := parseModelFromEnv(false); !errors.Is(err, ErrBadModelBreaker) {
			t.Fatalf("valor invalido sem gateway: %v", err)
		}
	})
	for _, governacao := range []string{"", "off"} {
		if _, err := compor(t, "enforce", governacao); !errors.Is(err, ErrModelBreakerWithoutRouteGovernance) {
			t.Fatalf("enforce com a governacao %q: %v; quer a recusa propria", governacao, err)
		}
	}
	for _, governacao := range []string{"observe"} {
		p, err := compor(t, "enforce", governacao)
		if err != nil || p.disjuntor != modelBreakerEnforce {
			t.Fatalf("enforce com a governacao %q: %v", governacao, err)
		}
		linhas := strings.Join(modelBreakerBanner(p, aos486Modelo, governacao), "\n")
		if !strings.Contains(linhas, "EM IMPOSICAO") || strings.Contains(linhas, "NAO EXISTE") {
			t.Fatalf("banner de enforce:\n%s", linhas)
		}
	}
	p, err := compor(t, "observe", "")
	if err != nil || p.disjuntor != modelBreakerObserve {
		t.Fatalf("observe sem governacao arranca: %v", err)
	}
	linhas := strings.Join(modelBreakerBanner(p, aos486Modelo, ""), "\n")
	if !strings.Contains(linhas, "EM OBSERVACAO") || !strings.Contains(linhas, "«modelo servido diferente» NAO EXISTE") {
		t.Fatalf("em observe, sem a governacao da rota, o banner tem de dizer que o sinal nao existe:\n%s", linhas)
	}
	for _, desligado := range []string{"", "off"} {
		p, err := compor(t, desligado, "")
		if err != nil || p.disjuntor != "" {
			t.Fatalf("%q: %v", desligado, err)
		}
		if l := modelBreakerBanner(p, aos486Modelo, ""); len(l) != 0 {
			t.Fatalf("desligado, o banner nao diz nada: %q", l)
		}
		if p.despachante == nil || p.despachante.aoTurno.Load() != nil {
			t.Fatal("desligado, o despachante nao tem a quem dizer o resultado de cada pedido")
		}
	}
	if l := modelBreakerBanner(nil, aos486Modelo, ""); len(l) != 0 {
		t.Fatalf("sem perfis e com o disjuntor desligado: %q", l)
	}
}

// O ARRANQUE DIZ O ESTADO, E FALHA FECHADO. Com o disjuntor ligado o arranque lê o livro e diz se
// está fechado, aberto ou «teria aberto»; com ele desligado e a marca de aberto no livro, avisa que a
// marca vale; e com um evento do disjuntor que não se sabe ler, o nó não arranca.
func TestAOS521_Arranque_DizOEstadoEFalhaFechado(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	carregar := func(t *testing.T, modo string) *perfisAssinados {
		aos519AmbienteDoGateway(t)
		aos520PastaCom(t, aos520cCand(10))
		assinados, err := loadSignedRouteProfilesFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		assinados.candidatoLigado, assinados.disjuntor = true, modo
		assinados.despachante = &clientePorModelo{titular: aos486Modelo}
		return assinados
	}
	arrancar := func(t *testing.T, modo string) (*canaryDoNo, *perfisAssinados, string, error) {
		var dito []string
		p := carregar(t, modo)
		c, err := comporCanaryDoNo(ctx, es, p, aos486Modelo, false, func(f string, a ...any) { dito = append(dito, fmt.Sprintf(f, a...)) })
		return c, p, strings.Join(dito, "\n"), err
	}
	c, p, dito, err := arrancar(t, modelBreakerObserve)
	if err != nil || !strings.Contains(dito, "disjuntor do modelo candidato (AOS-521): FECHADO") || p.despachante.aoTurno.Load() == nil {
		t.Fatalf("arranque em observe: %v\n%s", err, dito)
	}
	aos521TresFalhas(t, c.livro, c.candidato)
	if _, _, dito, err = arrancar(t, modelBreakerObserve); err != nil || !strings.Contains(dito, "TERIA ABERTO") {
		t.Fatalf("arranque em observe depois de a regra disparar: %v\n%s", err, dito)
	}
	if _, p, dito, err = arrancar(t, ""); err != nil || strings.Contains(dito, "AOS-521") || p.despachante.aoTurno.Load() != nil {
		t.Fatalf("desligado e sem marca de aberto, o arranque nao diz nada do disjuntor nem liga o despachante: %v\n%s", err, dito)
	}
	if _, _, dito, err = arrancar(t, modelBreakerEnforce); err != nil || !strings.Contains(dito, "DISJUNTOR DO MODELO CANDIDATO ABERTO") || !strings.Contains(dito, "disjuntor do modelo candidato (AOS-521): ABERTO") {
		t.Fatalf("arranque em enforce sobre um registo cujo observe teria aberto: %v\n%s", err, dito)
	}
	if _, _, dito, err = arrancar(t, ""); err != nil || !strings.Contains(dito, "a marca VALE NA MESMA") {
		t.Fatalf("desligado, com a marca de aberto no livro, o arranque avisa: %v\n%s", err, dito)
	}
	if _, err := es.Append(ctx, canaryStream, eventstore.EventInput{Type: EventTypeModelBreakerOpened, Payload: []byte(`{"v":"1.0"}`), RunID: canaryRunID, StepID: "estragado"}); err != nil {
		t.Fatal(err)
	}
	for _, modo := range []string{modelBreakerEnforce, modelBreakerObserve} {
		if _, _, _, err := arrancar(t, modo); !errors.Is(err, ErrCanaryUnreadable) {
			t.Fatalf("modo %q: com um evento do disjuntor ilegivel o no nao arranca: %v", modo, err)
		}
	}
	// DESLIGADO, o nó é o do AOS-520: arranca, e avisa.
	if _, _, dito, err = arrancar(t, ""); err != nil || !strings.Contains(dito, "NAO SABE LER") || !strings.Contains(dito, "NAO DECIDIVEL") {
		t.Fatalf("desligado, um evento do disjuntor ilegivel nao impede o arranque, e o arranque avisa: %v\n%s", err, dito)
	}
}

// A LEITURA DO DISJUNTOR NO ARRANQUE FALHA FECHADO — E DESLIGADO NÃO EXISTE. O livro lê-se bem na
// abertura do canary e falha na leitura seguinte: com o disjuntor ligado o nó não arranca (não se
// arranca sem saber se o disjuntor está aberto); com ele desligado não há leitura seguinte, e o nó
// arranca como no AOS-520.
func TestAOS521_Arranque_ALeituraDoDisjuntorFalhaFechado(t *testing.T) {
	ctx := context.Background()
	arrancar := func(t *testing.T, modo string) (leituras int, err error) {
		aos519AmbienteDoGateway(t)
		aos520PastaCom(t, aos520cCand(10))
		assinados, lerr := loadSignedRouteProfilesFromEnv()
		if lerr != nil {
			t.Fatal(lerr)
		}
		assinados.candidatoLigado, assinados.disjuntor = true, modo
		conta := &aos520StoreQueConta{Store: aos520Store(t)}
		doLivro := 0
		conta.falha = func(streamID string) error {
			if streamID != canaryStream {
				return nil
			}
			if doLivro++; doLivro >= 2 {
				return errors.New("disco avariado")
			}
			return nil
		}
		_, err = comporCanaryDoNo(ctx, conta, assinados, aos486Modelo, false, func(string, ...any) {})
		return conta.lidas(canaryStream), err
	}
	for _, modo := range []string{modelBreakerObserve, modelBreakerEnforce} {
		if lidas, err := arrancar(t, modo); !errors.Is(err, ErrCanaryUnreadable) || lidas < 2 {
			t.Fatalf("modo %q: a leitura do disjuntor falhou e o no arrancou (leituras=%d): %v", modo, lidas, err)
		}
	}
	lidas, err := arrancar(t, "")
	if err != nil {
		t.Fatalf("desligado, o arranque nao le o disjuntor e nao pode falhar por ele: %v", err)
	}
	ligado, _ := arrancar(t, modelBreakerObserve)
	if ligado != lidas+1 {
		t.Fatalf("o disjuntor ligado le o livro %d vez(es) no arranque e desligado %d; quer exactamente uma a mais", ligado, lidas)
	}
}

// A FALHA QUE O NÓ NÃO SABE CLASSIFICAR CONTA — é o lado fechado. Um run do candidato fixado, cujo
// desfecho selado é `failed` com um erro que não é de nenhuma classe conhecida nem está na lista do
// que não é do modelo (aqui, o 200 sem `usage` como o gateway o devolve), entra na janela pela via
// do nó — a função que a hospedagem chama depois do selo —, e três abrem o disjuntor. (Antes desta
// correcção nenhum entrava, e um candidato que falhasse sempre assim nunca abria.)
func TestAOS521_No_AFalhaNaoClassificadaAbre(t *testing.T) {
	n := aos521No(t, aos520NovoSubstrato(t), "enforce", "observe", aos520cCand(100))
	semUsage := fmt.Errorf("aos: run: %w", fmt.Errorf("%w: turno 1: %w", agentruntime.ErrModelCall, modelgateway.ErrUsageAusente))
	for i := 1; i <= 3; i++ {
		if aberto, _ := aos521Gauge(t, n, "aos_model_breaker_open"); aberto != "0" {
			t.Fatalf("antes do %d.o run o disjuntor ja estava aberto", i)
		}
		// Um run do candidato a sério, para o seu stream ter o `run.model_pinned` que o nó escreveu.
		id := fmt.Sprintf("sem-usage-%d", i)
		goal := aos520cTurnoUmECrash(t, n, id)
		n.svc.observarDesfechoNoDisjuntor(id, goal, state.Failed, agentruntime.Result{}, semUsage, false)
	}
	for id, sinal := range aos521Desfechos(t, n) {
		if sinal != sinalFalhaNaoClassificada {
			t.Fatalf("desfecho de %s = %q; quer %s", id, sinal, sinalFalhaNaoClassificada)
		}
	}
	abertas := aos521Marcas(t, n.node.EventStore, EventTypeModelBreakerOpened)
	if len(abertas) != 1 || abertas[0].Counters[sinalFalhaNaoClassificada] != 3 || len(abertas[0].Window) != 3 {
		t.Fatalf("tres falhas nao classificadas tinham de abrir: %+v", abertas)
	}
	if !strings.Contains(aos520cMetricas(t, n.aos486No), `aos_model_breaker_signals{signal="falha_nao_classificada"} 3`) {
		t.Fatal("o contador do sinal falha_nao_classificada nao esta a 3 no /metrics")
	}
}

// A CREDENCIAL DA CONTA NÃO É DO MODELO. Vinte respostas 401, e vinte 403: os runs falham, nenhum
// entra na janela, o disjuntor fica fechado — como com o 429. O 408 e o 500 contam.
func TestAOS521_No_ACredencialDaContaNaoConta(t *testing.T) {
	n := aos521No(t, aos520NovoSubstrato(t), "enforce", "observe", aos520cCand(100))
	for _, estado := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		aos521Avariar(n.aos486No, aos520AliasCand, estado)
		for i := 1; i <= 6; i++ {
			if err := aos521Submeter(t, n, fmt.Sprintf("conta-%d-%d", estado, i)); err == nil {
				t.Fatalf("o run %d tinha de falhar com o %d", i, estado)
			}
		}
	}
	if d := aos521Desfechos(t, n); len(d) != 0 {
		t.Fatalf("um 401 ou um 403 entrou na janela: %v", d)
	}
	if len(aos521Marcas(t, n.node.EventStore, EventTypeModelBreakerOpened)) != 0 {
		t.Fatal("a credencial da conta abriu o disjuntor")
	}
	// CONTROLO: o 408 conta, e três abrem.
	aos521Avariar(n.aos486No, aos520AliasCand, http.StatusRequestTimeout)
	for i := 1; i <= 3; i++ {
		_ = aos521Submeter(t, n, fmt.Sprintf("tempo-%d", i))
	}
	abertas := aos521Marcas(t, n.node.EventStore, EventTypeModelBreakerOpened)
	if len(abertas) != 1 || abertas[0].Counters[sinalErroDoProvider] != 3 {
		t.Fatalf("tres 408 tinham de abrir: %+v", abertas)
	}
}
