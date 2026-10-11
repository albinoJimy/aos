package main

// AOS-520 (PR 1) — O NÓ SERVE UM TITULAR E UM CANDIDATO, medido pelo nó COMPOSTO (ADR-041).
//
// O cliente de modelo sai de [parseModelFromEnv] — com a pasta de perfis assinados e o interruptor
// do candidato —, o nó é composto pelo [Bootstrap] e fala com um provider em httptest que grava o
// corpo de cada pedido: é pelo campo `model` desse corpo que se vê que modelo serviu cada turno.
//
// A inércia (sem candidato a servir, o nó é o da base) está em `aos520_inercia_test.go`; o livro do
// canary, sozinho, em `aos520_canary_livro_test.go`; a função de escolha, no pacote `policy/canary`.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/canary"
	"github.com/aos-ref/substrate/eventstore"
)

// aos520cNo levanta um nó com o interruptor e os registos dados.
func aos520cNo(t *testing.T, sub *aos520Substrato, interruptor string, regs ...aos520Registo) *aos520No {
	t.Helper()
	aos520PastaCom(t, regs...)
	aos520Interruptor(t, interruptor)
	return aos520ComporComPasta(t, sub)
}

func aos520cCand(fatia int) aos520Registo {
	return aos520Registo{"candidato", aos520PerfilCand, fatia}
}

// aos520cModeloDoPedido lê o campo `model` do corpo de um pedido ao provider.
func aos520cModeloDoPedido(t *testing.T, cru []byte) string {
	t.Helper()
	var p struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(cru, &p); err != nil || p.Model == "" {
		t.Fatalf("pedido sem modelo: %v\n%s", err, cru)
	}
	return p.Model
}

// aos520cModelosPedidos devolve o modelo de cada pedido que o provider recebeu desde `desde`.
func aos520cModelosPedidos(t *testing.T, n *aos486No, desde int) []string {
	t.Helper()
	var out []string
	for _, cru := range n.upstream.pedidos()[desde:] {
		out = append(out, aos520cModeloDoPedido(t, cru))
	}
	return out
}

// aos520cFixacao devolve o `run.model_pinned` do stream de um run, e a sua posição entre os eventos.
func aos520cFixacao(t *testing.T, es eventstore.EventStore, runID string) (modeloFixadoPayload, bool) {
	t.Helper()
	evs, err := es.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("ler o stream de %s: %v", runID, err)
	}
	var (
		out        modeloFixadoPayload
		visto      bool
		turnoAntes bool
		quantos    int
	)
	for _, ev := range evs {
		switch ev.Type {
		case agentruntime.EventTypeTurnRecorded:
			if !visto {
				turnoAntes = true
			}
		case EventTypeRunModelPinned:
			quantos++
			visto = true
			if err := json.Unmarshal(ev.Payload, &out); err != nil {
				t.Fatal(err)
			}
			if ev.Producer.NHIID != modeloFixadoNHI || ev.StepID != passoDoModeloFixado {
				t.Fatalf("run.model_pinned: producer=%q step=%q", ev.Producer.NHIID, ev.StepID)
			}
		}
	}
	if quantos > 1 {
		t.Fatalf("o run %s tem %d run.model_pinned; quer no maximo 1", runID, quantos)
	}
	if visto && turnoAntes {
		t.Fatalf("o run.model_pinned do run %s foi gravado DEPOIS de um turno", runID)
	}
	return out, visto
}

// aos520cManifestos devolve, por turno gravado, o modelo pedido e o digest do perfil do manifesto.
func aos520cManifestos(t *testing.T, eventos []eventstore.Event) (modelos, digests []string) {
	t.Helper()
	for _, ev := range eventos {
		if ev.Type != agentruntime.EventTypeTurnRecorded {
			continue
		}
		var p struct {
			Manifest struct {
				Model struct {
					ModelID string `json:"model_id"`
					Digest  string `json:"route_profile_digest"`
				} `json:"model"`
			} `json:"manifest"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		modelos, digests = append(modelos, p.Manifest.Model.ModelID), append(digests, p.Manifest.Model.Digest)
	}
	return modelos, digests
}

func aos520cTodos(valores []string, quer string) bool {
	if len(valores) == 0 {
		return false
	}
	for _, v := range valores {
		if v != quer {
			return false
		}
	}
	return true
}

func aos520cMetricas(t *testing.T, n *aos486No) string {
	t.Helper()
	r := getReq(n.h, "/metrics", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("GET /metrics: %d", r.Code)
	}
	return r.Body.String()
}

// O RUN DO CANDIDATO, DE PONTA A PONTA. Com `on` e a fatia a 100%, um run novo: todos os pedidos ao
// provider pedem o alias do candidato; o manifesto de cada turno traz esse alias e o digest do
// perfil do candidato; o `run.model_pinned` foi gravado ANTES do primeiro turno, com o papel, o
// alias, os digests, a fatia, a versão da função, a chave e a razão; o registo de retoma traz a
// marca; o livro tem o plano atribuído; o /metrics tem as duas séries do canary; e o replay
// reproduz o run a 100%.
func TestAOS520_No_OCandidatoServeORunEFicaTudoGravado(t *testing.T) {
	ctx := context.Background()
	n := aos520cNo(t, aos520NovoSubstrato(t), "on", aos520cCand(100))
	digestCand := aos520Digest(t, aos520PerfilCand)
	const runID = "run-520-do-candidato"
	m := n.correr(t, runID, nil)

	if modelos := aos520cModelosPedidos(t, n.aos486No, 0); len(modelos) != 2 || !aos520cTodos(modelos, aos520AliasCand) {
		t.Fatalf("os pedidos ao provider pediram %q; todos tinham de pedir o candidato %q", modelos, aos520AliasCand)
	}
	modelos, digests := aos520cManifestos(t, m.eventos)
	if len(modelos) != 2 || !aos520cTodos(modelos, aos520AliasCand) || !aos520cTodos(digests, digestCand) {
		t.Fatalf("manifestos: modelos=%q digests=%q; quer o alias %q e o digest %s em todos", modelos, digests, aos520AliasCand, digestCand)
	}
	fix, tem := aos520cFixacao(t, n.node.EventStore, runID)
	if !tem {
		t.Fatal("o run do candidato nao tem run.model_pinned")
	}
	entrada := n.node.canary.candidato.entryDigest
	quer := modeloFixadoPayload{V: "1.0", Role: "candidato", Alias: aos520AliasCand, ProfileDigest: digestCand, EntryDigest: entrada,
		SharePercent: 100, Function: "aos-canary/v1", Reason: "funcao", PlanKey: runID, PlanKeyKind: "run"}
	if fix != quer || !strings.HasPrefix(entrada, "sha256:") {
		t.Fatalf("run.model_pinned:\n veio:  %+v\n quero: %+v", fix, quer)
	}
	rec, ok, err := n.node.ResumeRecords.Get(ctx, runID)
	if err != nil || !ok {
		t.Fatalf("registo de retoma: ok=%v err=%v", ok, err)
	}
	if rec.ModelPinnedBy != "aos-canary/v1" || rec.Model.ModelID != aos520AliasCand || rec.RouteProfileDigest != digestCand {
		t.Fatalf("registo de retoma: marca=%q modelo=%q perfil=%q", rec.ModelPinnedBy, rec.Model.ModelID, rec.RouteProfileDigest)
	}
	if cru, _ := json.Marshal(rec); !strings.Contains(string(cru), `"ModelPinnedBy":"aos-canary/v1"`) {
		t.Fatalf("o registo de retoma nao traz a marca: %s", cru)
	}
	atribuidos := aos520EventosDoLivro(t, n.node.EventStore, EventTypeModelCanaryPlanAssigned)
	if len(atribuidos) != 1 || atribuidos[0].PlanKey != runID || atribuidos[0].Alias != aos520AliasCand || atribuidos[0].ProfileDigest != digestCand || atribuidos[0].EntryDigest != entrada {
		t.Fatalf("livro do canary: %+v", atribuidos)
	}
	metricas := aos520cMetricas(t, n.aos486No)
	for _, linha := range []string{"\naos_model_canary_plans_assigned 1\n", "\naos_model_canary_closed 0\n"} {
		if !strings.Contains(metricas, linha) {
			t.Fatalf("o /metrics nao tem %q", strings.TrimSpace(linha))
		}
	}
	// REPLAY A 100% de um run servido pelo candidato.
	aos505NoReplayCom(t, n.aos486No, runID, m, aos520AliasCand)
}

// A FUNÇÃO REPARTE, E OS DOIS LADOS FICAM FIXADOS. Com `on` e a fatia a 10%: o run "0" (balde 168)
// é do candidato; o run "a" (balde 8230) é do titular — os pedidos são os goldens de sempre, o
// manifesto traz o alias e o perfil do titular, e a fixação fica gravada com o papel `titular`.
func TestAOS520_No_AFuncaoReparteEOsDoisLadosFicamFixados(t *testing.T) {
	ctx := context.Background()
	n := aos520cNo(t, aos520NovoSubstrato(t), "on", aos520cCand(10))
	if canary.Choose("0", 10) != canary.RoleCandidate || canary.Choose("a", 10) != canary.RoleIncumbent {
		t.Fatal("os dois ids deste teste ja nao caem um de cada lado")
	}
	mc := n.correr(t, "0", nil)
	if modelos := aos520cModelosPedidos(t, n.aos486No, 0); !aos520cTodos(modelos, aos520AliasCand) {
		t.Fatalf("o run \"0\" pediu %q", modelos)
	}
	if modelos, _ := aos520cManifestos(t, mc.eventos); !aos520cTodos(modelos, aos520AliasCand) {
		t.Fatalf("manifestos do run \"0\": %q", modelos)
	}

	antes := len(n.upstream.pedidos())
	mt := n.correr(t, "a", nil)
	if modelos := aos520cModelosPedidos(t, n.aos486No, antes); len(modelos) != 2 || !aos520cTodos(modelos, aos486Modelo) {
		t.Fatalf("o run \"a\" pediu %q; quer o titular", modelos)
	}
	// O run "a" não usa o objectivo dos goldens, mas o wire é o do titular: só muda o `model`.
	modelos, digests := aos520cManifestos(t, mt.eventos)
	if !aos520cTodos(modelos, aos486Modelo) || !aos520cTodos(digests, aos519DigestsDaTabela[aos486Modelo]) {
		t.Fatalf("manifestos do run \"a\": modelos=%q digests=%q", modelos, digests)
	}
	fix, tem := aos520cFixacao(t, n.node.EventStore, "a")
	if !tem || fix.Role != "titular" || fix.Alias != aos486Modelo || fix.Reason != "funcao" || fix.PlanKey != "a" || fix.SharePercent != 10 ||
		fix.ProfileDigest != aos519DigestsDaTabela[aos486Modelo] || fix.EntryDigest != n.node.canary.candidato.entryDigest {
		t.Fatalf("run.model_pinned do run do titular: tem=%v %+v", tem, fix)
	}
	rec, _, err := n.node.ResumeRecords.Get(ctx, "a")
	if err != nil || rec.ModelPinnedBy != "aos-canary/v1" || rec.Model.ModelID != aos486Modelo {
		t.Fatalf("registo de retoma do run do titular: err=%v marca=%q modelo=%q", err, rec.ModelPinnedBy, rec.Model.ModelID)
	}
	// Só o plano do candidato está no livro.
	if a := aos520EventosDoLivro(t, n.node.EventStore, EventTypeModelCanaryPlanAssigned); len(a) != 1 || a[0].PlanKey != "0" {
		t.Fatalf("livro do canary: %+v", a)
	}
}

// A MESMA SÉRIE, COM O INTERRUPTOR A `off` E COM A FATIA A 100%. Vinte runs com os mesmos ids: com
// `off` nenhum pedido pede o candidato, nenhum run tem fixação e o livro não existe; com `on` e
// 100%, todos os pedidos pedem o candidato.
func TestAOS520_No_AMesmaSerie_OffDaZeroECemDaTodos(t *testing.T) {
	serie := func(t *testing.T, n *aos520No) (candidato, titular, fixados int) {
		for i := 0; i < 20; i++ {
			runID := fmt.Sprintf("serie-520-%02d", i)
			n.correr(t, runID, nil)
			if _, tem := aos520cFixacao(t, n.node.EventStore, runID); tem {
				fixados++
			}
		}
		for _, modelo := range aos520cModelosPedidos(t, n.aos486No, 0) {
			switch modelo {
			case aos520AliasCand:
				candidato++
			case aos486Modelo:
				titular++
			default:
				t.Fatalf("pedido a um modelo que o no nao serve: %q", modelo)
			}
		}
		return candidato, titular, fixados
	}
	t.Run("off", func(t *testing.T) {
		n := aos520cNo(t, aos520NovoSubstrato(t), "off", aos520cCand(100))
		c, tit, fixados := serie(t, n)
		if c != 0 || tit != 40 || fixados != 0 {
			t.Fatalf("com off: %d pedidos ao candidato, %d ao titular, %d runs fixados; quer 0, 40 e 0", c, tit, fixados)
		}
		if _, err := n.node.EventStore.Read(context.Background(), canaryStream, 1); !errors.Is(err, eventstore.ErrStreamNotFound) {
			t.Fatalf("com off o livro do canary nao existe: %v", err)
		}
		if strings.Contains(aos520cMetricas(t, n.aos486No), "aos_model_canary") {
			t.Fatal("com off o /metrics nao tem as series do canary")
		}
	})
	t.Run("on a 100%", func(t *testing.T) {
		n := aos520cNo(t, aos520NovoSubstrato(t), "on", aos520cCand(100))
		c, tit, fixados := serie(t, n)
		if c != 40 || tit != 0 || fixados != 20 {
			t.Fatalf("com on a 100%%: %d pedidos ao candidato, %d ao titular, %d runs fixados; quer 40, 0 e 20", c, tit, fixados)
		}
	})
}

// QUEM SUBMETE NÃO ESCOLHE — o campo do corpo e o cabeçalho. Com a fatia a 10% e um run cuja chave
// a função dá ao titular: um corpo com um campo de modelo é recusado (400) e o pedido de run não
// tem campo por onde o modelo entre; um cabeçalho a pedir o candidato não muda nada. (A terceira
// via, o `plan_id` declarado, está em [TestAOS520_No_Plano_TresNosEDuasTentativas_UmSoModelo].)
func TestAOS520_No_QuemSubmeteNaoEscolhe_CorpoECabecalho(t *testing.T) {
	n := aos520cNo(t, aos520NovoSubstrato(t), "on", aos520cCand(10))
	base := map[string]any{"run_id": "a", "objective": "Le o documento notes e resume", "principal_nhi": durAgent, "credential": n.tok, "max_turns": 3}
	for _, campo := range []string{"model", "model_id", "modelo", "candidate", "model_pinned_by", "ModelPinnedBy", "route", "alias"} {
		corpo := map[string]any{}
		for k, v := range base {
			corpo[k] = v
		}
		corpo[campo] = aos520AliasCand
		if r := postJSON(n.h, "POST", "/runs", corpo); r.Code != http.StatusBadRequest {
			t.Fatalf("um corpo com o campo %q tinha de ser recusado (400); veio %d %s", campo, r.Code, r.Body.String())
		}
	}
	campos := reflect.TypeOf(submitRequest{})
	for i := 0; i < campos.NumField(); i++ {
		nome := strings.ToLower(campos.Field(i).Name + " " + string(campos.Field(i).Tag))
		for _, proibido := range []string{"model", "modelo", "candidat", "pinned", "alias", "canary"} {
			if strings.Contains(nome, proibido) {
				t.Fatalf("o pedido de run ganhou um campo por onde o modelo entra: %s", campos.Field(i).Name)
			}
		}
	}
	if len(n.upstream.pedidos()) != 0 {
		t.Fatal("um pedido recusado chegou ao provider")
	}
	// O cabeçalho: o run corre, e é do titular — como a função manda para a chave "a".
	cabecalhos := map[string]string{"X-AOS-Model": aos520AliasCand, "X-Model": aos520AliasCand, "X-AOS-Model-Candidate": "on", "X-AOS-Canary": "candidato", "Model": aos520AliasCand}
	if r := postReq(n.h, "/runs", base, cabecalhos); r.Code != http.StatusCreated {
		t.Fatalf("POST /runs com cabecalhos: %d %s", r.Code, r.Body.String())
	}
	aos486Esperar(t, n.svc, "a")
	if modelos := aos520cModelosPedidos(t, n.aos486No, 0); len(modelos) != 2 || !aos520cTodos(modelos, aos486Modelo) {
		t.Fatalf("com um cabecalho a pedir o candidato, o run pediu %q; quer o titular", modelos)
	}
	if fix, tem := aos520cFixacao(t, n.node.EventStore, "a"); !tem || fix.Role != "titular" {
		t.Fatalf("fixacao do run: tem=%v %+v", tem, fix)
	}
}

// aos520cPlanoNo levanta o nó SOBERANO dos testes de plano (o do AOS-502/510: fila de pedidos,
// reclamação, vínculo verificado, novas tentativas das duas classes), com o candidato dado.
func aos520cPlanoNo(t *testing.T, sub *aos520Substrato, interruptor string, regs ...aos520Registo) *aos502No {
	t.Helper()
	aos520PastaCom(t, regs...)
	aos520Interruptor(t, interruptor)
	aos504SemVariavel(t, "AOS_MODEL_ROUTE_GOVERNANCE")
	aos504SemVariavel(t, "AOS_MODEL_PROJECTION_VERSION")
	return aos510ComporCom(t, agentruntime.CompletionEnforce, 2, true, false, func(cfg *Config) {
		if sub != nil {
			cfg.EventStore, cfg.EventStorePath = sub.store, ""
			cfg.DSARVault = sub.vault
		}
	}, aos502Leitura)
}

// aos520cCorrerPlano corre um plano inteiro pelo `POST /runs`, como o drenador: três nós, e dois
// deles com uma nova tentativa — um por falta de tool call (AOS-502), outro por resposta vazia
// (AOS-510). Devolve os ids dos cinco runs. `planID` é o `plan_id` DECLARADO em todos os pedidos.
func aos520cCorrerPlano(t *testing.T, n *aos502No, plano, planID string) []string {
	t.Helper()
	ger := n.pedir(t, plano)
	vinculo := func(no string, tentativa int) *vinculoAoPedido {
		return &vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: planID, NodeID: no, Attempt: tentativa}
	}
	submeter := func(runID string, v *vinculoAoPedido, tools, contrato []string) {
		t.Helper()
		if codigo, corpo := n.submeter(t, runID, v, tools, contrato); codigo != http.StatusCreated {
			t.Fatalf("POST /runs %s: %d %s", runID, codigo, corpo)
		}
		n.esperar(t, runID)
	}
	leitura := []string{aos502Leitura}
	// Nó 1 (AOS-502): a primeira tentativa não chama a tool e fecha failed; a segunda chama e conclui.
	n.modeloNaoChama(aos493RespostasDeProducao(t)["plan-e2e-v0145-1791115918"], "stop")
	no1a, no1b := idDaTentativa(plano, aos502No_, 1), idDaTentativa(plano, aos502No_, 2)
	submeter(no1a, vinculo(aos502No_, 0), leitura, leitura)
	n.modeloChama(aos502Leitura)
	submeter(no1b, vinculo(aos502No_, 2), leitura, leitura)
	// Nó 2 (AOS-510): a primeira tentativa responde vazio e fecha failed; a segunda responde texto.
	n.modeloNaoChama("", "stop")
	no2a, no2b := idDaTentativa(plano, aos510No_, 1), idDaTentativa(plano, aos510No_, 2)
	submeter(no2a, vinculo(aos510No_, 0), []string{}, nil)
	n.modeloNaoChama("Resumo: o documento tem tres notas.", "stop")
	submeter(no2b, vinculo(aos510No_, 2), []string{}, nil)
	// Nó 3: uma tentativa, com a tool.
	n.modeloChama(aos502Leitura)
	no3 := idDoRunFilho(plano, "terceiro")
	submeter(no3, vinculo("terceiro", 0), leitura, leitura)
	for id, quer := range map[string]string{no1a: "failed", no1b: "completed", no2a: "failed", no2b: "completed", no3: "completed"} {
		if _, r := n.ler(t, id); r.Status != quer {
			t.Fatalf("o run %s ficou %q; quer %q", id, r.Status, quer)
		}
	}
	return []string{no1a, no1b, no2a, no2b, no3}
}

// UM PLANO, UM SÓ MODELO — três nós e duas novas tentativas (AOS-502 e AOS-510). Com a fatia a 10%,
// dois planos: o plano "0" (balde 168) é do candidato e o plano "a" (balde 8230) é do titular. Em
// cada um, os cinco runs — os três nós e as duas tentativas a mais — pedem o MESMO modelo em todos
// os pedidos e gravam-no em todos os manifestos, e a fixação de cada run cita a chave do PLANO.
//
// E QUEM SUBMETE NÃO ESCOLHE PELO `plan_id`: o plano "a" corre com o `plan_id` declarado "0", que a
// função daria ao candidato — e é servido pelo titular, porque a chave é o pedido que o nó provou.
// O plano "0" corre com o `plan_id` "a", e é do candidato.
func TestAOS520_No_Plano_TresNosEDuasTentativas_UmSoModelo(t *testing.T) {
	n := aos520cPlanoNo(t, nil, "on", aos520cCand(10))
	if canary.Choose("0", 10) != canary.RoleCandidate || canary.Choose("a", 10) != canary.RoleIncumbent {
		t.Fatal("os dois planos deste teste ja nao caem um de cada lado")
	}
	for _, c := range []struct {
		plano, planIDDeclarado, alias, papel string
	}{
		{"0", "a", aos520AliasCand, "candidato"},
		{"a", "0", aos486Modelo, "titular"},
	} {
		antes := len(n.upstream.pedidos())
		runs := aos520cCorrerPlano(t, n, c.plano, c.planIDDeclarado)
		if modelos := aos520cModelosPedidos(t, n.aos486No, antes); len(modelos) < 5 || !aos520cTodos(modelos, c.alias) {
			t.Fatalf("plano %q: os pedidos pediram %q; todos tinham de pedir %q", c.plano, modelos, c.alias)
		}
		for i, runID := range runs {
			evs, err := n.node.EventStore.Read(context.Background(), runID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if modelos, _ := aos520cManifestos(t, evs); !aos520cTodos(modelos, c.alias) {
				t.Fatalf("plano %q, run %s: manifestos com %q; quer %q", c.plano, runID, modelos, c.alias)
			}
			fix, tem := aos520cFixacao(t, n.node.EventStore, runID)
			querRazao := "funcao"
			if c.papel == "candidato" && i > 0 {
				querRazao = "plano_ja_atribuido"
			}
			if !tem || fix.Role != c.papel || fix.Alias != c.alias || fix.PlanKey != c.plano || fix.PlanKeyKind != "pedido" || fix.Reason != querRazao {
				t.Fatalf("plano %q, run %s: fixacao tem=%v %+v; quer papel=%s chave=%q razao=%s", c.plano, runID, tem, fix, c.papel, c.plano, querRazao)
			}
		}
	}
	// O livro: UM plano atribuído ao candidato — o "0" —, uma vez, apesar de cinco runs.
	if a := aos520EventosDoLivro(t, n.node.EventStore, EventTypeModelCanaryPlanAssigned); len(a) != 1 || a[0].PlanKey != "0" {
		t.Fatalf("livro do canary: %+v; quer so o plano \"0\"", a)
	}
}

// O PLANO ANTERIOR À ABERTURA É DO TITULAR. O plano é pedido com o nó SEM candidato; o nó é
// recriado com o candidato a 100%, e a abertura do canary grava a marca da fila. O nó desse plano
// que corre depois é do titular (`plano_anterior_a_abertura`), embora a 100% a função desse o
// candidato; um plano pedido DEPOIS da abertura é do candidato.
func TestAOS520_No_OPlanoAnteriorAAberturaEDoTitular(t *testing.T) {
	sub := aos520NovoSubstrato(t)
	antes := aos520cPlanoNo(t, sub, "off", aos520cCand(100))
	const plano = "plano-anterior"
	ger := antes.pedir(t, plano)
	if err := antes.svc.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = antes.node.Close()

	n := aos520cPlanoNo(t, sub, "on", aos520cCand(100))
	abertas := aos520EventosDoLivro(t, n.node.EventStore, EventTypeModelCanaryOpened)
	if len(abertas) != 1 || abertas[0].PlanRequestMark == 0 {
		t.Fatalf("a abertura tinha de gravar a marca da fila de pedidos: %+v", abertas)
	}
	n.modeloChama(aos502Leitura)
	runID := idDoRunFilho(plano, "no")
	if codigo, corpo := n.submeter(t, runID, &vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: aos502PlanID, NodeID: "no"}, []string{aos502Leitura}, []string{aos502Leitura}); codigo != http.StatusCreated {
		t.Fatalf("POST /runs: %d %s", codigo, corpo)
	}
	n.esperar(t, runID)
	if modelos := aos520cModelosPedidos(t, n.aos486No, 0); !aos520cTodos(modelos, aos486Modelo) {
		t.Fatalf("o no do plano anterior a abertura pediu %q; quer o titular", modelos)
	}
	if fix, tem := aos520cFixacao(t, n.node.EventStore, runID); !tem || fix.Role != "titular" || fix.Reason != "plano_anterior_a_abertura" || fix.PlanKey != plano {
		t.Fatalf("fixacao: tem=%v %+v", tem, fix)
	}
	// Um plano pedido DEPOIS da abertura vai à função: a 100%, é do candidato.
	const novo = "plano-novo"
	gerNovo := n.pedir(t, novo)
	desde := len(n.upstream.pedidos())
	runNovo := idDoRunFilho(novo, "no")
	if codigo, corpo := n.submeter(t, runNovo, &vinculoAoPedido{RunID: novo, Geracao: gerNovo, PlanID: aos502PlanID, NodeID: "no"}, []string{aos502Leitura}, []string{aos502Leitura}); codigo != http.StatusCreated {
		t.Fatalf("POST /runs: %d %s", codigo, corpo)
	}
	n.esperar(t, runNovo)
	if modelos := aos520cModelosPedidos(t, n.aos486No, desde); !aos520cTodos(modelos, aos520AliasCand) {
		t.Fatalf("o no do plano novo pediu %q; quer o candidato", modelos)
	}
}

// UM RUN EM CURSO RE-SUBMETIDO NÃO É NOVO (a sonda da revisão). O run dá o turno 1 no titular, num
// nó sem candidato a servir, e o nó cai. O nó é recriado com o candidato a 100%, e o MESMO `run_id`
// volta por `POST /runs` — a via da submissão, não a da retoma. O log diz que o run já tem turno e
// registo de retoma: não se escolhe. O turno seguinte é do titular, o run não ganha
// `run.model_pinned`, o registo de retoma fica sem a marca, e o livro não gasta um lugar.
//
// E o caso do run que tem registo de retoma mas ainda não deu turno nenhum: também não é novo.
func TestAOS520_No_RunEmCursoReSubmetidoNaoENovo(t *testing.T) {
	ctx := context.Background()
	for _, comTurno := range []bool{true, false} {
		nome := "com um turno dado"
		if !comTurno {
			nome = "so com o registo de retoma"
		}
		t.Run(nome, func(t *testing.T) {
			sub := aos520NovoSubstrato(t)
			const runID = "run-520-re-submetido"
			a := aos520cNo(t, sub, "off", aos520cCand(100))
			if comTurno {
				if goal := aos520cTurnoUmECrash(t, a, runID); goal.ModelPinnedBy != "" || goal.Model.ModelID != aos486Modelo {
					t.Fatalf("o turno 1 tinha de ser do titular, sem marca: %+v", goal.Model)
				}
			} else {
				goal := aos520ComoOHostRun(a.node, agentruntime.Goal{RunID: runID, Principal: referencemonitor.Principal{NHIID: durAgent}, Objective: aos490Objectivo, MaxTurns: 3})
				if err := a.svc.putResumeRecord(ctx, goal); err != nil {
					t.Fatal(err)
				}
			}
			a.fechar(t)

			b := aos520cNo(t, sub, "on", aos520cCand(100))
			b.correr(t, runID, nil)
			modelos := aos520cModelosPedidos(t, b.aos486No, 0)
			if len(modelos) == 0 || !aos520cTodos(modelos, aos486Modelo) {
				t.Fatalf("o run re-submetido pediu %q; ja tinha historia no titular e NAO pode passar para o candidato", modelos)
			}
			if _, tem := aos520cFixacao(t, b.node.EventStore, runID); tem {
				t.Fatal("o run re-submetido ganhou um run.model_pinned — foi escolhido como se fosse novo")
			}
			if at := aos520EventosDoLivro(t, b.node.EventStore, EventTypeModelCanaryPlanAssigned); len(at) != 0 {
				t.Fatalf("o run re-submetido gastou um lugar do canary: %+v", at)
			}
			if rec, ok, err := b.node.ResumeRecords.Get(ctx, runID); err != nil || !ok || rec.ModelPinnedBy != "" || rec.Model.ModelID != aos486Modelo {
				t.Fatalf("registo de retoma: ok=%v err=%v marca=%q modelo=%q", ok, err, rec.ModelPinnedBy, rec.Model.ModelID)
			}
			// Controlo: no mesmo nó, um run SEM história é escolhido — e vai para o candidato.
			desde := len(b.upstream.pedidos())
			b.correr(t, "run-520-novo-de-verdade", nil)
			if modelos := aos520cModelosPedidos(t, b.aos486No, desde); !aos520cTodos(modelos, aos520AliasCand) {
				t.Fatalf("o run novo pediu %q; quer o candidato", modelos)
			}
		})
	}
}

// UM RUN FIXADO RE-SUBMETIDO FICA COM O QUE ESTÁ SELADO. O run começa no candidato, dá um turno, e
// o nó cai. O mesmo `run_id` volta por `POST /runs` num nó com o interruptor a `off` (perfil ainda
// carregado): o Goal do pedido não traz marca nenhuma, e o nó vai buscá-la ao log — o turno seguinte
// é do candidato. Num nó sem o registo do candidato, o run é recusado: não passa para o titular.
func TestAOS520_No_RunFixadoReSubmetidoFicaComOSelado(t *testing.T) {
	sub := aos520NovoSubstrato(t)
	const runID = "run-520-fixado-re-submetido"
	a := aos520cNo(t, sub, "on", aos520cCand(100))
	aos520cTurnoUmECrash(t, a, runID)
	a.fechar(t)

	corpo := map[string]any{"run_id": runID, "objective": aos490Objectivo, "principal_nhi": durAgent, "credential": "", "max_turns": 3}

	semRegisto := aos520cNo(t, sub, "on")
	corpo["credential"] = semRegisto.tok
	if r := postJSON(semRegisto.h, "POST", "/runs", corpo); r.Code != http.StatusCreated {
		t.Fatalf("POST /runs: %d %s", r.Code, r.Body.String())
	}
	oc, ok, err := semRegisto.svc.Wait(context.Background(), runID)
	if err != nil || !ok || !errors.Is(oc.Err, ErrModelPinRefused) || causaDaFixacaoDoModelo(oc.Err) != modelgateway.PinnedCauseModelNotServed {
		t.Fatalf("sem o registo do candidato o run fixado tinha de ser recusado (%s); veio ok=%v err=%v run=%v", modelgateway.PinnedCauseModelNotServed, ok, err, oc.Err)
	}
	if n := len(semRegisto.upstream.pedidos()); n != 0 {
		t.Fatalf("o run recusado fez %d pedido(s) ao provider — nao pode ser servido pelo titular", n)
	}
}

// O MESMO, COM O PERFIL CARREGADO: o run fixado no candidato e re-submetido num nó com o interruptor
// a `off` continua no candidato — o nó foi buscar a fixação ao log, e não escolheu.
func TestAOS520_No_RunFixadoReSubmetidoContinuaNoCandidato(t *testing.T) {
	sub := aos520NovoSubstrato(t)
	const runID = "run-520-fixado-re-submetido-off"
	a := aos520cNo(t, sub, "on", aos520cCand(100))
	aos520cTurnoUmECrash(t, a, runID)
	a.fechar(t)

	b := aos520cNo(t, sub, "off", aos520cCand(100))
	b.correr(t, runID, nil)
	if modelos := aos520cModelosPedidos(t, b.aos486No, 0); len(modelos) == 0 || !aos520cTodos(modelos, aos520AliasCand) {
		t.Fatalf("o run fixado no candidato e re-submetido pediu %q; quer o candidato", modelos)
	}
	if at := aos520EventosDoLivro(t, b.node.EventStore, EventTypeModelCanaryPlanAssigned); len(at) != 1 {
		t.Fatalf("a re-submissao nao pode mexer no livro: %+v", at)
	}
}

// UM RUN AVULSO COM O NOME DE UM PEDIDO DE PLANO NÃO ATRIBUI O PLANO. O plano "plano-anterior" é
// pedido antes de o canary abrir: é do titular. Depois da abertura, alguém submete um run AVULSO
// cujo `run_id` é "plano-anterior": a 100% vai para o candidato, e fica no livro — como chave de
// RUN. O nó seguinte do plano não casa com essa atribuição: continua no titular, pela marca.
func TestAOS520_No_RunAvulsoComONomeDeUmPedidoNaoAtribuiOPlano(t *testing.T) {
	sub := aos520NovoSubstrato(t)
	antes := aos520cPlanoNo(t, sub, "off", aos520cCand(100))
	const plano = "plano-anterior"
	ger := antes.pedir(t, plano)
	if err := antes.svc.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = antes.node.Close()

	n := aos520cPlanoNo(t, sub, "on", aos520cCand(100))
	n.modeloChama(aos502Leitura)
	// O run avulso, com o nome do pedido. Sem `plan_request`.
	if codigo, corpo := n.submeter(t, plano, nil, []string{aos502Leitura}, []string{aos502Leitura}); codigo != http.StatusCreated {
		t.Fatalf("POST /runs do run avulso: %d %s", codigo, corpo)
	}
	n.esperar(t, plano)
	at := aos520EventosDoLivro(t, n.node.EventStore, EventTypeModelCanaryPlanAssigned)
	if len(at) != 1 || at[0].PlanKey != plano || at[0].PlanKeyKind != "run" {
		t.Fatalf("o run avulso tinha de ficar no livro como chave de run: %+v", at)
	}
	// O nó do plano com o mesmo nome: anterior à abertura, do titular.
	desde := len(n.upstream.pedidos())
	runID := idDoRunFilho(plano, "no")
	if codigo, corpo := n.submeter(t, runID, &vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: aos502PlanID, NodeID: "no"}, []string{aos502Leitura}, []string{aos502Leitura}); codigo != http.StatusCreated {
		t.Fatalf("POST /runs do no do plano: %d %s", codigo, corpo)
	}
	n.esperar(t, runID)
	if modelos := aos520cModelosPedidos(t, n.aos486No, desde); !aos520cTodos(modelos, aos486Modelo) {
		t.Fatalf("o no do plano anterior a abertura pediu %q; a atribuicao do run avulso com o mesmo nome nao vale para o plano", modelos)
	}
	if fix, tem := aos520cFixacao(t, n.node.EventStore, runID); !tem || fix.Role != "titular" || fix.Reason != "plano_anterior_a_abertura" || fix.PlanKeyKind != "pedido" {
		t.Fatalf("fixacao do no do plano: tem=%v %+v", tem, fix)
	}
}

// A MARCA NO GOAL NÃO VALE POR VIR NO GOAL. Um embedder chama [NodeService.Submit] com um Goal que
// já traz a marca de modelo fixado e o alias do candidato, para um run que não existe — num nó com
// o interruptor a `off`, num nó com o candidato a servir, e num nó SEM a pasta de perfis. Em todos o
// run é recusado com a causa própria, sem um pedido ao provider, sem evento e sem mexer no livro. O
// mesmo com uma marca que não é versão de função nenhuma, e com a marca certa e outro alias.
func TestAOS520_No_AMarcaNoGoalSoValeSeEstiverSelada(t *testing.T) {
	ctx := context.Background()
	nos := map[string]func(t *testing.T) *aos520No{
		"interruptor a off":  func(t *testing.T) *aos520No { return aos520cNo(t, aos520NovoSubstrato(t), "off", aos520cCand(100)) },
		"candidato a servir": func(t *testing.T) *aos520No { return aos520cNo(t, aos520NovoSubstrato(t), "on", aos520cCand(100)) },
		"sem a pasta": func(t *testing.T) *aos520No {
			aos520Interruptor(t, "")
			return aos520Compor(t, aos520NovoSubstrato(t), "", "", aos505NoEsperado)
		},
	}
	for nome, compor := range nos {
		t.Run(nome, func(t *testing.T) {
			n := compor(t)
			for i, g := range []agentruntime.Goal{
				{Model: agentruntime.ModelConfig{ModelID: aos520AliasCand}, ModelPinnedBy: versaoDaEscolha},
				{Model: agentruntime.ModelConfig{ModelID: aos520AliasCand}, ModelPinnedBy: "o-que-eu-quiser"},
				{Model: agentruntime.ModelConfig{ModelID: aos486Modelo}, ModelPinnedBy: versaoDaEscolha},
			} {
				g.RunID = fmt.Sprintf("run-520-marca-forjada-%d", i)
				g.Principal = referencemonitor.Principal{NHIID: durAgent}
				g.Objective, g.MaxTurns, g.Credential = aos490Objectivo, 3, n.tok
				if err := n.svc.Submit(ctx, g); err != nil {
					t.Fatalf("Submit: %v", err)
				}
				oc, ok, err := n.svc.Wait(ctx, g.RunID)
				if err != nil || !ok || !errors.Is(oc.Err, ErrModelPinRefused) || causaDaFixacaoDoModelo(oc.Err) != causaMarcaSemFixacaoGravada {
					t.Fatalf("goal %d: uma marca sem fixacao gravada tinha de ser recusada; veio ok=%v err=%v run=%v", i, ok, err, oc.Err)
				}
				if _, tem := aos520cFixacao(t, n.node.EventStore, g.RunID); tem {
					t.Fatalf("goal %d: o run recusado ganhou um run.model_pinned", i)
				}
			}
			if p := len(n.upstream.pedidos()); p != 0 {
				t.Fatalf("um run com a marca forjada fez %d pedido(s) ao provider", p)
			}
			if evs, _ := n.node.EventStore.Read(ctx, canaryStream, 1); len(evs) > 1 {
				t.Fatalf("a marca forjada mexeu no livro: %d eventos", len(evs))
			}
		})
	}
	// A retoma: um registo com uma marca que este binário não conhece não se serve.
	no := &Node{modelID: aos486Modelo, modeloAutoritativo: true}
	rec := integration.ResumeRecord{RunID: "r", Model: agentruntime.ModelConfig{ModelID: aos486Modelo}, ModelPinnedBy: "aos-canary/v9"}
	if err := no.conferirPerfilFixado(rec); causaDoPerfilFixado(err) != modelgateway.PinnedCauseModelNotServed {
		t.Fatalf("um registo com uma marca de uma versao desconhecida: %v", err)
	}
	if !marcaDeEscolhaConhecida("aos-canary/v1") || marcaDeEscolhaConhecida("") || marcaDeEscolhaConhecida("aos-canary/v2") {
		t.Fatal("o conjunto das marcas conhecidas e fechado: so a versao desta funcao")
	}
}

// UM EVENTO DE UMA VERSÃO MAIS RECENTE NO LIVRO NÃO DERRUBA O NÓ. O livro tem um plano atribuído ao
// candidato e um evento de um tipo que este binário não conhece (o que o AOS-521 vai escrever).
//
//   - Com o interruptor a `off` (ou ausente): o nó ARRANCA e é inerte — um run novo é do titular,
//     sem fixação; o plano que era do candidato continua a ser recusado.
//   - Com `on`: o nó arranca e SERVE O TITULAR; nenhum plano novo vai para o candidato, embora a
//     fatia seja 100%; o plano já atribuído ao candidato é recusado — não é servido por ninguém.
//
// Nos dois o /metrics diz que o livro tem eventos de uma versão mais recente.
func TestAOS520_No_LivroComEventoDeVersaoMaisRecente(t *testing.T) {
	semear := func(t *testing.T) *aos520Substrato {
		sub := aos520NovoSubstrato(t)
		a := aos520cNo(t, sub, "on", aos520cCand(100))
		if _, err := a.node.canary.livro.decidir(context.Background(), a.node.canary.candidato, chaveDeRun("plano-do-candidato"), 0); err != nil {
			t.Fatal(err)
		}
		a.fechar(t)
		aos520EventoFuturo(t, sub.store, "futuro-1")
		return sub
	}
	for _, interruptor := range []string{"off", ""} {
		t.Run("interruptor "+interruptor, func(t *testing.T) {
			n := aos520cNo(t, semear(t), interruptor, aos520cCand(100))
			n.correr(t, "run-novo", nil)
			if modelos := aos520cModelosPedidos(t, n.aos486No, 0); !aos520cTodos(modelos, aos486Modelo) {
				t.Fatalf("o run novo pediu %q", modelos)
			}
			if _, tem := aos520cFixacao(t, n.node.EventStore, "run-novo"); tem {
				t.Fatal("sem candidato a servir o run nao e fixado")
			}
			aos520cRecusado(t, n, "plano-do-candidato", causaPlanoDoCandidatoSemCandidato)
			if !strings.Contains(aos520cMetricas(t, n.aos486No), "\naos_model_canary_newer_events 1\n") {
				t.Fatal("o /metrics nao diz que o livro tem eventos de uma versao mais recente")
			}
		})
	}
	t.Run("on", func(t *testing.T) {
		n := aos520cNo(t, semear(t), "on", aos520cCand(100))
		n.correr(t, "run-novo", nil)
		if modelos := aos520cModelosPedidos(t, n.aos486No, 0); len(modelos) != 2 || !aos520cTodos(modelos, aos486Modelo) {
			t.Fatalf("com um evento futuro no livro, o run novo pediu %q; quer o titular", modelos)
		}
		if fix, tem := aos520cFixacao(t, n.node.EventStore, "run-novo"); !tem || fix.Role != "titular" || fix.Reason != "livro_de_versao_mais_recente" {
			t.Fatalf("fixacao: tem=%v %+v", tem, fix)
		}
		if at := aos520EventosDoLivro(t, n.node.EventStore, EventTypeModelCanaryPlanAssigned); len(at) != 1 {
			t.Fatalf("com um evento futuro atribuiu-se um plano novo: %+v", at)
		}
		aos520cRecusado(t, n, "plano-do-candidato", causaLivroDeVersaoMaisRecente)
		if !strings.Contains(aos520cMetricas(t, n.aos486No), "\naos_model_canary_newer_events 1\n") {
			t.Fatal("o /metrics nao diz que o livro tem eventos de uma versao mais recente")
		}
	})
	// Sem eventos futuros, a série existe com o candidato a servir (a zero) e não existe sem ele.
	if m := aos520cMetricas(t, aos520cNo(t, aos520NovoSubstrato(t), "on", aos520cCand(100)).aos486No); !strings.Contains(m, "\naos_model_canary_newer_events 0\n") {
		t.Fatal("com o candidato a servir a serie existe, a zero")
	}
	if m := aos520cMetricas(t, aos520cNo(t, aos520NovoSubstrato(t), "off", aos520cCand(100)).aos486No); strings.Contains(m, "aos_model_canary") {
		t.Fatal("sem candidato a servir e sem eventos futuros o /metrics nao tem series do canary")
	}
}

// aos520cRecusado submete um run que a fixação do modelo tem de recusar, e confere as três coisas
// que a recusa deixa: o run fecha `failed` com a razão própria na transição terminal, a causa fica
// no log do nó, e o provider não vê pedido nenhum.
func aos520cRecusado(t *testing.T, n *aos520No, runID, causa string) {
	t.Helper()
	antes := len(n.upstream.pedidos())
	rec := postJSON(n.h, "POST", "/runs", map[string]any{"run_id": runID, "objective": "Le o documento notes e resume", "principal_nhi": durAgent, "credential": n.tok, "max_turns": 3})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs: %d %s", rec.Code, rec.Body.String())
	}
	oc, ok, err := n.svc.Wait(context.Background(), runID)
	if err != nil || !ok {
		t.Fatalf("Wait: ok=%v err=%v", ok, err)
	}
	if !errors.Is(oc.Err, ErrModelPinRefused) || causaDaFixacaoDoModelo(oc.Err) != causa {
		t.Fatalf("o run tinha de ser recusado pela fixacao com a causa %s; veio %v", causa, oc.Err)
	}
	if st := aos520Estado(t, n, runID); st != state.Failed {
		t.Fatalf("o run recusado ficou em %q; quer failed", st)
	}
	evs, err := n.node.EventStore.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	razaoTerminal := false
	for _, ev := range evs {
		if ev.Type == agentruntime.EventTypeTurnRecorded {
			t.Fatal("o run recusado deu um turno")
		}
		if strings.Contains(string(ev.Payload), `"`+reasonModelPinRefused+`"`) {
			razaoTerminal = true
		}
	}
	if !razaoTerminal {
		t.Fatalf("a transicao terminal do run recusado nao traz a razao %q", reasonModelPinRefused)
	}
	if !strings.Contains(n.lido(), "RECUSADO pela fixacao do modelo") || !strings.Contains(n.lido(), "causa="+causa) {
		t.Fatalf("a recusa nao ficou no log do no:\n%s", n.lido())
	}
	if depois := len(n.upstream.pedidos()); depois != antes {
		t.Fatalf("o run recusado fez %d pedido(s) ao provider", depois-antes)
	}
}

// O PLANO DO CANDIDATO SEM CANDIDATO É RECUSADO — nunca continua no titular. Um plano é atribuído ao
// candidato (está no livro); depois o nó é recriado (a) com o interruptor a `off`, (b) com a fatia a 0, (c) sem o
// registo do candidato, e (d) com `on` e OUTRO perfil para o alias do candidato. Em todos, um run
// novo desse plano fecha `failed` com a causa própria, sem um pedido ao provider; e um run de OUTRO
// plano corre normalmente, com o titular.
func TestAOS520_No_PlanoDoCandidatoSemCandidato_ERecusado(t *testing.T) {
	outroPerfil := strings.Replace(aos520PerfilCand, "modelo-novo-4.5", "modelo-b-1.0", 1)
	for nome, c := range map[string]struct {
		interruptor string
		regs        []aos520Registo
	}{
		"interruptor a off":         {"off", []aos520Registo{aos520cCand(100)}},
		"fatia a zero":              {"on", []aos520Registo{aos520cCand(0)}},
		"registo retirado":          {"on", nil},
		"outro perfil para o alias": {"on", []aos520Registo{{"candidato", outroPerfil, 100}}},
	} {
		t.Run(nome, func(t *testing.T) {
			sub := aos520NovoSubstrato(t)
			a := aos520cNo(t, sub, "on", aos520cCand(100))
			// O plano é atribuído ao candidato, e o run dele ainda não começou (o nó caiu antes).
			if d, err := a.node.canary.livro.decidir(context.Background(), a.node.canary.candidato, chaveDeRun("plano-do-candidato"), 0); err != nil || d.papel != canary.RoleCandidate {
				t.Fatalf("atribuir o plano ao candidato: %+v %v", d, err)
			}
			a.fechar(t)

			b := aos520cNo(t, sub, c.interruptor, c.regs...)
			aos520cRecusado(t, b, "plano-do-candidato", causaPlanoDoCandidatoSemCandidato)
			// Um plano que NÃO era do candidato corre.
			desde := len(b.upstream.pedidos())
			b.correr(t, "outro-plano", nil)
			querModelo := aos486Modelo
			if nome == "outro perfil para o alias" {
				querModelo = aos520AliasCand // o candidato novo, a 100%, serve os planos novos
			}
			if modelos := aos520cModelosPedidos(t, b.aos486No, desde); !aos520cTodos(modelos, querModelo) {
				t.Fatalf("o run de outro plano pediu %q; quer %q", modelos, querModelo)
			}
		})
	}
}

// O LIVRO ILEGÍVEL, A CONTENÇÃO E A FIXAÇÃO QUE NÃO SE GRAVA RECUSAM O RUN. Com o candidato a servir:
// um evento que não se sabe ler no livro, o CAS sempre perdido, e o `run.model_pinned` que não se
// consegue escrever — o run fecha `failed` com a causa de cada um, e não é servido por ninguém.
func TestAOS520_No_FalhasDaEscolha_RecusamORun(t *testing.T) {
	t.Run("livro ilegivel", func(t *testing.T) {
		n := aos520cNo(t, aos520NovoSubstrato(t), "on", aos520cCand(100))
		if _, err := n.node.EventStore.Append(context.Background(), canaryStream, eventstore.EventInput{Type: EventTypeQuotaReserved, Payload: []byte(`{}`), RunID: canaryRunID, StepID: "lixo"}); err != nil {
			t.Fatal(err)
		}
		aos520cRecusado(t, n, "run-livro-ilegivel", causaLivroIlegivel)
	})
	for nome, c := range map[string]struct {
		tipo, causa string
		erro        error
	}{
		"contencao":            {EventTypeModelCanaryPlanAssigned, causaLivroEmContencao, eventstore.ErrSeqConflict},
		"fixacao nao gravada":  {EventTypeRunModelPinned, causaFixacaoNaoGravada, errors.New("disco cheio")},
		"atribuicao nao grava": {EventTypeModelCanaryPlanAssigned, causaLivroIlegivel, errors.New("disco cheio")},
	} {
		t.Run(nome, func(t *testing.T) {
			n := aos520cNo(t, aos520NovoSubstrato(t), "on", aos520cCand(100))
			avaria := &aos520cStoreComAvaria{EventStorePort: n.node.EventStore, tipo: c.tipo, erro: c.erro}
			n.node.canary.livro.es = avaria
			n.node.EventStore = avaria
			aos520cRecusado(t, n, "run-"+strings.ReplaceAll(nome, " ", "-"), c.causa)
		})
	}
}

// aos520cStoreComAvaria falha todos os Append de um tipo de evento.
type aos520cStoreComAvaria struct {
	EventStorePort
	tipo string
	erro error
}

func (s *aos520cStoreComAvaria) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if in.Type == s.tipo {
		return eventstore.AppendResult{}, s.erro
	}
	return s.EventStorePort.Append(ctx, streamID, in, opts...)
}

// aos520cTurnoUmECrash dá o turno 1 de um run nesta incarnação, pelo caminho de escolha do hostRun
// ([NodeService.escolherModeloDoRun] e o resto do que o hostRun fixa), e «crasha»: o run fica em
// `running`, com o registo de retoma escrito. É o molde de [aos520TurnoUmECrash], com a escolha.
func aos520cTurnoUmECrash(t *testing.T, n *aos520No, runID string) agentruntime.Goal {
	t.Helper()
	ctx := context.Background()
	goal := agentruntime.Goal{RunID: runID, Principal: referencemonitor.Principal{NHIID: durAgent}, Objective: aos490Objectivo, MaxTurns: 3}
	goal, err := n.svc.escolherModeloDoRun(ctx, &runState{runID: runID}, goal)
	if err != nil {
		t.Fatalf("escolherModeloDoRun: %v", err)
	}
	if goal.AssemblyVersion == "" {
		goal.AssemblyVersion = layoutDosRunsNovos(n.node.estadoDoProvider != nil)
	}
	goal = n.node.fixarConclusao(n.node.fixarPerfilDoRun(n.node.fixarProjeccaoDoRun(fixarLayout(goal))))
	prod := goal
	prod.Credential = n.tok
	prod.MaxTurns = 1
	rctx := withModelCredential(ctx, n.tok)
	if goal.ModelPinnedBy != "" {
		rctx = withModeloDoRun(rctx, goal.Model.ModelID)
	}
	if _, _, err := n.node.Runtime.Run(rctx, prod, nil); err != nil && !errors.Is(err, agentruntime.ErrMaxTurnsExceeded) {
		t.Fatalf("turno 1 antes do crash: %v", err)
	}
	mq, err := state.NewMachine(n.node.EventStore, runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mq.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if err := mq.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1), Reason: "crash_simulado"}); err != nil {
		t.Fatalf("claim do crash simulado: %v", err)
	}
	if err := n.svc.putResumeRecord(ctx, goal); err != nil {
		t.Fatalf("registo de retoma: %v", err)
	}
	return goal
}

// AS TRÊS SAÍDAS DA RETOMA, PARA O MODELO FIXADO. Um run começa com o candidato, dá um turno, e o
// nó é recriado sem o candidato a servir — de três maneiras:
//
//  1. o interruptor passa a `off`, com o perfil ainda carregado: o run retoma e ACABA COM O
//     CANDIDATO — o turno vivo pede o alias do candidato —, com replay a 100%;
//  2. o canary terminou (40 planos), com o perfil carregado: o mesmo;
//  3. o registo foi retirado: a retoma pela API é recusada (409, `modelo_fixado_nao_servido`) e a
//     varredura de crash-resume deixa o run órfão — continua `running`, sem um pedido ao provider.
//
// Em nenhuma o run passa para o titular. (A mutação dirigida: repor a sobreposição do modelo do nó
// num run com a marca faz as saídas 1 e 2 pedirem o titular.)
func TestAOS520_No_RunDoCandidatoRetomado_AsTresSaidas(t *testing.T) {
	ctx := context.Background()
	digestCand := aos520Digest(t, aos520PerfilCand)
	comecar := func(t *testing.T, runID string) *aos520Substrato {
		sub := aos520NovoSubstrato(t)
		a := aos520cNo(t, sub, "on", aos520cCand(100))
		goal := aos520cTurnoUmECrash(t, a, runID)
		if goal.Model.ModelID != aos520AliasCand || goal.ModelPinnedBy != "aos-canary/v1" || goal.RouteProfileDigest != digestCand {
			t.Fatalf("o run tinha de comecar fixado no candidato: %+v", goal.Model)
		}
		if modelos := aos520cModelosPedidos(t, a.aos486No, 0); len(modelos) != 1 || modelos[0] != aos520AliasCand {
			t.Fatalf("o turno 1 pediu %q", modelos)
		}
		a.fechar(t)
		return sub
	}
	acabaComOCandidato := func(t *testing.T, n *aos520No, runID string) {
		t.Helper()
		aos520RetomarPelaAPI(t, n, runID)
		if st := aos520Estado(t, n, runID); st != state.Complete {
			t.Fatalf("o run retomado ficou em %q", st)
		}
		if modelos := aos520cModelosPedidos(t, n.aos486No, 0); len(modelos) != 1 || modelos[0] != aos520AliasCand {
			t.Fatalf("o turno vivo da retoma pediu %q; o run esta fixado no candidato %q e NAO passa para o titular", modelos, aos520AliasCand)
		}
		m := n.medir(t, runID, 0)
		modelos, digests := aos520cManifestos(t, m.eventos)
		if len(modelos) != 2 || !aos520cTodos(modelos, aos520AliasCand) || !aos520cTodos(digests, digestCand) {
			t.Fatalf("manifestos do run retomado: modelos=%q digests=%q", modelos, digests)
		}
		if rec, ok, err := n.node.ResumeRecords.Get(ctx, runID); err != nil || !ok || rec.Model.ModelID != aos520AliasCand || rec.ModelPinnedBy != "aos-canary/v1" {
			t.Fatalf("registo de retoma depois da retoma: ok=%v err=%v %+v", ok, err, rec.Model)
		}
		// REPLAY A 100% de um run do candidato retomado a meio.
		aos505NoReplayCom(t, n.aos486No, runID, m, aos520AliasCand)
	}

	t.Run("interruptor a off, perfil carregado", func(t *testing.T) {
		const runID = "run-520-saida-off"
		sub := comecar(t, runID)
		acabaComOCandidato(t, aos520cNo(t, sub, "off", aos520cCand(100)), runID)
	})
	t.Run("fatia a zero, perfil carregado", func(t *testing.T) {
		const runID = "run-520-saida-fatia-zero"
		sub := comecar(t, runID)
		acabaComOCandidato(t, aos520cNo(t, sub, "on", aos520cCand(0)), runID)
	})
	t.Run("canary terminado, perfil carregado", func(t *testing.T) {
		const runID = "run-520-saida-terminado"
		sub := comecar(t, runID)
		n := aos520cNo(t, sub, "on", aos520cCand(100))
		for i := 0; n.node.canary.livro.fechadoVisto.Load() == 0; i++ {
			if i > 45 {
				t.Fatal("o canary nao fechou")
			}
			if _, err := n.node.canary.livro.decidir(ctx, n.node.canary.candidato, chaveDeRun(fmt.Sprintf("enche-%02d", i)), 0); err != nil {
				t.Fatal(err)
			}
		}
		acabaComOCandidato(t, n, runID)
	})
	t.Run("registo retirado", func(t *testing.T) {
		const runID = "run-520-saida-retirado"
		sub := comecar(t, runID)
		n := aos520cNo(t, sub, "on") // a pasta existe, sem o registo do candidato
		eventosAntes := aos520NumEventos(t, n, runID)
		n.svc.mu.Lock()
		n.svc.suspended[runID] = &runState{runID: runID, suspended: true, done: make(chan struct{})}
		n.svc.mu.Unlock()
		resp := postJSON(n.h, "POST", "/runs/"+runID+"/resume", map[string]any{"credential": n.tok})
		if resp.Code != http.StatusConflict || !strings.Contains(resp.Body.String(), modelgateway.PinnedCauseModelNotServed) {
			t.Fatalf("a retoma tinha de dar 409 com a causa %s; veio %d %s", modelgateway.PinnedCauseModelNotServed, resp.Code, resp.Body.String())
		}
		if err := n.svc.Resume(ctx, runID, n.tok); !errors.Is(err, ErrResumePerfilFixado) || causaDoPerfilFixado(err) != modelgateway.PinnedCauseModelNotServed {
			t.Fatalf("Resume: %v", err)
		}
		n.svc.mu.Lock()
		delete(n.svc.suspended, runID)
		n.svc.mu.Unlock()
		scanned, resumed, err := n.svc.ResumeInterruptedRuns(ctx)
		if err != nil || scanned != 1 || resumed != 0 {
			t.Fatalf("a varredura devia ver 1 orfao e nao retomar nenhum: scanned=%d resumed=%d err=%v", scanned, resumed, err)
		}
		if !strings.Contains(n.lido(), "deixado como orfao") || !strings.Contains(n.lido(), "causa="+modelgateway.PinnedCauseModelNotServed) {
			t.Fatalf("o orfao nao ficou declarado com a causa:\n%s", n.lido())
		}
		if st := aos520Estado(t, n, runID); st != state.Running {
			t.Fatalf("o run ficou em %q; tinha de continuar running (recuperavel)", st)
		}
		if got := aos520NumEventos(t, n, runID); got != eventosAntes {
			t.Fatalf("a recusa escreveu no stream do run: %d eventos, eram %d", got, eventosAntes)
		}
		if len(n.upstream.pedidos()) != 0 {
			t.Fatal("a recusa fez um pedido ao provider — o run nao pode ser servido pelo titular")
		}
		n.fechar(t)
		// Reposto o registo, o run retoma e acaba com o candidato.
		acabaComOCandidato(t, aos520cNo(t, sub, "off", aos520cCand(100)), runID)
	})
}

// A CONFERÊNCIA DA RETOMA USA O ALIAS FIXADO NO RUN. Um registo com a marca: o alias conferido é o
// do REGISTO, e não o que o nó pede. O nó cujo titular mudou e que já não serve o alias fixado
// recusa; o nó que o serve com outro perfil recusa; o que o serve com o mesmo perfil aceita. Um
// registo SEM a marca é conferido com o alias do nó, como no PR 0.
func TestAOS520_No_AConferenciaDaRetomaUsaOAliasFixado(t *testing.T) {
	aos519AmbienteDoGateway(t)
	aos520PastaCom(t, aos520cCand(10))
	assinados, err := loadSignedRouteProfilesFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	digestCand := aos520Digest(t, aos520PerfilCand)
	fixado := func(alias, digest string) integration.ResumeRecord {
		return integration.ResumeRecord{RunID: "r", Model: agentruntime.ModelConfig{ModelID: alias}, RouteProfileDigest: digest, ModelPinnedBy: versaoDaEscolha}
	}
	no := &Node{modelID: aos486Modelo, modeloAutoritativo: true, perfisDaRota: assinados}
	if err := no.conferirPerfilFixado(fixado(aos520AliasCand, digestCand)); err != nil {
		t.Fatalf("run fixado no candidato, perfil carregado: %v", err)
	}
	if got := no.fixarModelo(fixado(aos520AliasCand, digestCand).GoalWith("")).Model.ModelID; got != aos520AliasCand {
		t.Fatalf("o modelo do no sobrepos-se ao de um run com a marca: %q", got)
	}
	if err := no.conferirPerfilFixado(fixado(aos520AliasCand, "sha256:"+strings.Repeat("7", 64))); causaDoPerfilFixado(err) != modelgateway.PinnedCauseProfileDifferent {
		t.Fatalf("run fixado no candidato com outro perfil: %v", err)
	}
	// O run fixado como TITULAR, num nó cujo titular mudou: o alias antigo tem perfil na tabela em
	// código (o digest casa), mas o nó já não o SERVE — recusa, não passa para o titular novo.
	outroTitular := &Node{modelID: "k3", modeloAutoritativo: true, perfisDaRota: assinados}
	if err := outroTitular.conferirPerfilFixado(fixado(aos486Modelo, aos519DigestsDaTabela[aos486Modelo])); causaDoPerfilFixado(err) != modelgateway.PinnedCauseModelNotServed {
		t.Fatalf("run fixado no titular antigo, num no com outro titular: %v", err)
	}
	if err := no.conferirPerfilFixado(fixado(aos486Modelo, aos519DigestsDaTabela[aos486Modelo])); err != nil {
		t.Fatalf("run fixado no titular, no mesmo no: %v", err)
	}
	// Sem a pasta: o candidato não é servido.
	semPasta := &Node{modelID: aos486Modelo, modeloAutoritativo: true}
	if err := semPasta.conferirPerfilFixado(fixado(aos520AliasCand, digestCand)); causaDoPerfilFixado(err) != modelgateway.PinnedCauseModelNotServed {
		t.Fatalf("run fixado no candidato, num no sem a pasta: %v", err)
	}
	// SEM a marca: o alias conferido é o do nó (PR 0), e o modelo do nó sobrepõe-se.
	semMarca := integration.ResumeRecord{RunID: "r", Model: agentruntime.ModelConfig{ModelID: "outro-alias"}, RouteProfileDigest: aos519DigestsDaTabela[aos486Modelo]}
	if err := no.conferirPerfilFixado(semMarca); err != nil {
		t.Fatalf("run sem a marca: %v", err)
	}
	if got := no.fixarModelo(semMarca.GoalWith("")).Model.ModelID; got != aos486Modelo {
		t.Fatalf("num run sem a marca o modelo do no sobrepoe-se; veio %q", got)
	}
}

// UM RUN ANTIGO, SEM A MARCA, É RETOMADO COMO SEMPRE — mesmo num nó com o candidato a 100%. O run
// começa num nó sem candidato a servir (não é fixado por escolha) e é retomado num nó com `on` e a
// fatia a 100%: a retoma NÃO o entrega ao candidato, não lhe grava fixação, e o turno vivo pede o
// titular.
func TestAOS520_No_RunSemMarcaRetomadoNumNoComCandidato_ContinuaNoTitular(t *testing.T) {
	sub := aos520NovoSubstrato(t)
	const runID = "run-520-sem-marca"
	a := aos520cNo(t, sub, "off", aos520cCand(100))
	goal := aos520cTurnoUmECrash(t, a, runID)
	if goal.ModelPinnedBy != "" || goal.Model.ModelID != aos486Modelo {
		t.Fatalf("sem candidato a servir o run nao e fixado por escolha: %+v marca=%q", goal.Model, goal.ModelPinnedBy)
	}
	a.fechar(t)

	b := aos520cNo(t, sub, "on", aos520cCand(100))
	aos520RetomarPelaAPI(t, b, runID)
	if modelos := aos520cModelosPedidos(t, b.aos486No, 0); len(modelos) != 1 || modelos[0] != aos486Modelo {
		t.Fatalf("o turno vivo de um run sem a marca pediu %q; quer o titular", modelos)
	}
	if _, tem := aos520cFixacao(t, b.node.EventStore, runID); tem {
		t.Fatal("um run retomado sem a marca ganhou um run.model_pinned")
	}
	if a := aos520EventosDoLivro(t, b.node.EventStore, EventTypeModelCanaryPlanAssigned); len(a) != 0 {
		t.Fatalf("um run retomado foi atribuido ao candidato: %+v", a)
	}
	if rec, _, err := b.node.ResumeRecords.Get(context.Background(), runID); err != nil || rec.ModelPinnedBy != "" {
		t.Fatalf("o registo de retoma de um run antigo ganhou a marca: %q err=%v", rec.ModelPinnedBy, err)
	}
}

// O 40.º PLANO, COM DUAS RÉPLICAS E UM REINÍCIO. Duas réplicas do nó sobre o mesmo Event Store, as
// duas com o candidato a 100%, e 39 planos já atribuídos. A réplica 1 corre o 40.º plano: é do
// candidato, e o canary fecha. A réplica 2 corre o plano seguinte: é do titular (`canary_fechado`).
// Um nó recriado depois (o reinício) continua a dar o titular aos planos novos — a contagem é a do
// livro —, e o arranque declara o canary terminado. Um plano já atribuído continua no candidato.
func TestAOS520_No_OQuadragesimoPlano_DuasReplicasEReinicio(t *testing.T) {
	ctx := context.Background()
	sub := aos520NovoSubstrato(t)
	r1 := aos520cNo(t, sub, "on", aos520cCand(100))
	r2 := aos520cNo(t, sub, "on", aos520cCand(100))
	for i := 1; i <= 39; i++ {
		if _, err := r1.node.canary.livro.decidir(ctx, r1.node.canary.candidato, chaveDeRun(fmt.Sprintf("plano-%02d", i)), 0); err != nil {
			t.Fatal(err)
		}
	}
	// O livro fala pelo log do arranque do nó; aqui vai para o log desta réplica.
	r1.node.canary.livro.log = r1.svc.log
	r1.correr(t, "plano-40", nil)
	if modelos := aos520cModelosPedidos(t, r1.aos486No, 0); !aos520cTodos(modelos, aos520AliasCand) {
		t.Fatalf("o 40.o plano pediu %q; quer o candidato", modelos)
	}
	if !strings.Contains(r1.lido(), "CANARY DO MODELO CANDIDATO TERMINADO") {
		t.Fatalf("o fim do canary nao foi dito no log da replica que o fechou:\n%s", r1.lido())
	}
	if m := aos520cMetricas(t, r1.aos486No); !strings.Contains(m, "\naos_model_canary_plans_assigned 40\n") || !strings.Contains(m, "\naos_model_canary_closed 1\n") {
		t.Fatal("o /metrics da replica 1 nao diz 40 planos e o canary terminado")
	}
	// A réplica 2 não escreveu nada no livro, e é pelo livro que sabe que fechou.
	r2.correr(t, "plano-41", nil)
	if modelos := aos520cModelosPedidos(t, r2.aos486No, 0); !aos520cTodos(modelos, aos486Modelo) {
		t.Fatalf("o 41.o plano, na replica 2, pediu %q; quer o titular", modelos)
	}
	if fix, _ := aos520cFixacao(t, sub.store, "plano-41"); fix.Role != "titular" || fix.Reason != "canary_fechado" {
		t.Fatalf("fixacao do 41.o plano: %+v", fix)
	}
	if m := aos520cMetricas(t, r2.aos486No); !strings.Contains(m, "\naos_model_canary_closed 1\n") {
		t.Fatal("o /metrics da replica 2 nao diz o canary terminado depois de ler o livro")
	}
	if n := len(aos520EventosDoLivro(t, sub.store, EventTypeModelCanaryPlanAssigned)); n != 40 {
		t.Fatalf("planos atribuidos = %d; o limite e 40", n)
	}
	if n := len(aos520EventosDoLivro(t, sub.store, EventTypeModelCanaryClosed)); n != 1 {
		t.Fatalf("marcas de fecho = %d", n)
	}
	r1.fechar(t)
	r2.fechar(t)

	// O REINÍCIO: o canary continua terminado.
	r3 := aos520cNo(t, sub, "on", aos520cCand(100))
	if r3.node.canary.livro.fechadoVisto.Load() != 1 || r3.node.canary.livro.atribuidosVistos.Load() != 40 {
		t.Fatalf("depois do reinicio o livro diz atribuidos=%d fechado=%d", r3.node.canary.livro.atribuidosVistos.Load(), r3.node.canary.livro.fechadoVisto.Load())
	}
	r3.correr(t, "plano-42", nil)
	if modelos := aos520cModelosPedidos(t, r3.aos486No, 0); !aos520cTodos(modelos, aos486Modelo) {
		t.Fatalf("depois do reinicio, um plano novo pediu %q; quer o titular", modelos)
	}
	// E o selo do fim do canary no audit de governação do gateway está ligado a este nó.
	if r3.node.perfisDaRota.selarFimDoCanary == nil {
		t.Fatal("o no com um candidato nao tem onde selar o fim do canary")
	}
	if err := r3.node.perfisDaRota.selarFimDoCanary(ctx, fimDoCanary{alias: aos520AliasCand, profileDigest: "sha256:p", entryDigest: "sha256:e", atribuidos: 40, limite: 40}); err != nil {
		t.Fatalf("o selo do fim do canary no audit do gateway: %v", err)
	}
}

// DOIS CANDIDATOS COM `on` NÃO ARRANCA; COM `off` ARRANCA E NÃO SERVE NENHUM. E um valor do
// interruptor fora do vocabulário não arranca — com ou sem a pasta.
func TestAOS520_No_DoisCandidatosEValorInvalido(t *testing.T) {
	// A allowlist embebida autoriza dois nomes para o board destes testes. Para os dois serem
	// candidatos, o titular deste nó é um terceiro alias.
	const titular = "o-titular-deste-no"
	dois := []aos520Registo{aos520cCand(10), {"outro-candidato", aos520PerfilA, 10}}

	aos519AmbienteDoGateway(t)
	t.Setenv("AOS_MODEL_NAME", titular)
	aos520PastaCom(t, dois...)
	aos520Interruptor(t, "on")
	_, _, err := parseModelFromEnv(false)
	if !errors.Is(err, ErrModelCandidateMoreThanOne) {
		t.Fatalf("dois candidatos com on: quer ErrModelCandidateMoreThanOne; veio %v", err)
	}
	if !strings.Contains(err.Error(), "candidato") || !strings.Contains(err.Error(), "outro-candidato") || strings.Contains(err.Error(), "sha256:") {
		t.Fatalf("o erro nomeia os registos pelo rotulo e nao leva digests: %v", err)
	}

	aos520Interruptor(t, "off")
	cliente, _, err := parseModelFromEnv(false)
	if err != nil {
		t.Fatalf("dois registos nao-titulares com off: %v", err)
	}
	assinados := perfisDoCliente(cliente)
	if _, serve := assinados.candidatoAServir(titular); serve || len(assinados.naoTitulares(titular)) != 2 {
		t.Fatal("com off nenhum candidato e servido, e a pasta tem os dois")
	}
	if d, ok := aos520cDespachante(cliente); !ok || len(d.porAlias) != 3 || d.titular != titular {
		t.Fatal("com off e dois registos nao-titulares, o despachante tem o titular e os dois aliases")
	}
	if linhas := modelCandidateBanner(assinados, titular); len(linhas) != 2 || !strings.Contains(linhas[0], "NAO SERVIDO") || !strings.Contains(linhas[1], "NAO SERVIDO") {
		t.Fatalf("com off e dois registos, o arranque diz dois candidatos nao servidos: %q", linhas)
	}

	for _, valor := range []string{"ON", "true", "1", "sim", "candidato", "on "} {
		t.Setenv("AOS_MODEL_CANDIDATE", valor)
		if valor == "on " { // os espaços à volta são aparados: é `on`, e com dois candidatos não arranca
			if _, _, err := parseModelFromEnv(false); !errors.Is(err, ErrModelCandidateMoreThanOne) {
				t.Fatalf("%q: %v", valor, err)
			}
			continue
		}
		if _, _, err := parseModelFromEnv(false); !errors.Is(err, ErrBadModelCandidate) {
			t.Fatalf("AOS_MODEL_CANDIDATE=%q tinha de abortar o arranque; veio %v", valor, err)
		}
	}
	// Sem a pasta, um valor inválido aborta na mesma; um valor válido não liga nada.
	aos504SemVariavel(t, "AOS_MODEL_ROUTE_PROFILES_DIR")
	aos504SemVariavel(t, "AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR")
	t.Setenv("AOS_MODEL_CANDIDATE", "talvez")
	if _, _, err := parseModelFromEnv(false); !errors.Is(err, ErrBadModelCandidate) {
		t.Fatalf("sem a pasta, um valor invalido: %v", err)
	}
	t.Setenv("AOS_MODEL_CANDIDATE", "on")
	cliente, _, err = parseModelFromEnv(false)
	if err != nil || perfisDoCliente(cliente) != nil {
		t.Fatalf("sem a pasta, on nao liga nada: err=%v", err)
	}
	if _, despacha := aos520cDespachante(cliente); despacha {
		t.Fatal("sem a pasta o cliente de modelo nao e um despachante")
	}
}

// aos520cDespachante procura o despachante por baixo dos invólucros do cliente de modelo do nó.
func aos520cDespachante(cliente agentruntime.ModelClient) (*clientePorModelo, bool) {
	for cliente != nil {
		if d, ok := cliente.(*clientePorModelo); ok {
			return d, true
		}
		cliente = aos520cInner(cliente)
	}
	return nil, false
}

// O CANDIDATO SEM PREÇO NÃO ARRANCA, com a contabilidade de custo composta. As conferências de
// arranque do candidato, uma a uma: com `off` não confere nada; com `on`, mais de um candidato
// aborta, e um candidato sem preço aborta só quando a contabilidade está composta.
func TestAOS520_ConferirCandidato_UmEComPreco(t *testing.T) {
	aos519AmbienteDoGateway(t)
	aos520PastaCom(t, aos520cCand(10))
	assinados, err := loadSignedRouteProfilesFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	perguntado := ""
	semPreco := func(alias string) (bool, error) { perguntado = alias; return false, nil }
	comPreco := func(alias string) (bool, error) { perguntado = alias; return true, nil }
	avaria := errors.New("tabela ilegivel")

	if err := assinados.conferirCandidato(aos486Modelo, true, semPreco); err != nil || perguntado != "" {
		t.Fatalf("com off nao se confere nada: err=%v perguntado=%q", err, perguntado)
	}
	assinados.candidatoLigado = true
	if err := assinados.conferirCandidato(aos486Modelo, false, semPreco); err != nil || perguntado != "" {
		t.Fatalf("sem contabilidade de custo o preco nao e exigido: err=%v", err)
	}
	if err := assinados.conferirCandidato(aos486Modelo, true, semPreco); !errors.Is(err, ErrModelCandidateWithoutPrice) || perguntado != aos520AliasCand {
		t.Fatalf("candidato sem preco com a contabilidade composta: err=%v perguntado=%q", err, perguntado)
	}
	if err := assinados.conferirCandidato(aos486Modelo, true, comPreco); err != nil {
		t.Fatalf("candidato com preco: %v", err)
	}
	if err := assinados.conferirCandidato(aos486Modelo, true, func(string) (bool, error) { return false, avaria }); !errors.Is(err, avaria) {
		t.Fatalf("a tabela que nao se le: %v", err)
	}
	// O registo para o alias do TITULAR não é um candidato: nada a conferir.
	if err := assinados.conferirCandidato(aos520AliasCand, true, semPreco); err != nil {
		t.Fatalf("um registo para o alias do titular nao e um candidato: %v", err)
	}
	var semPasta *perfisAssinados
	if err := semPasta.conferirCandidato(aos486Modelo, true, semPreco); err != nil {
		t.Fatalf("sem a pasta: %v", err)
	}
}

// O ARRANQUE DIZ O TITULAR E O CANDIDATO. A servir: o titular, o candidato, a fatia, o digest do
// registo, e a linha do perfil diz que a fatia está em vigor. Com `off`: «candidato NAO SERVIDO».
// Com a fatia a 0: o mesmo, com a razão. Sem registo não-titular e com `off`: nenhuma linha.
func TestAOS520_Banner_TitularECandidato(t *testing.T) {
	carregar := func(t *testing.T, interruptor string, regs ...aos520Registo) *perfisAssinados {
		aos519AmbienteDoGateway(t)
		aos520PastaCom(t, regs...)
		assinados, err := loadSignedRouteProfilesFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		assinados.candidatoLigado = interruptor == "on"
		return assinados
	}
	digestCand := aos520Digest(t, aos520PerfilCand)

	aServir := carregar(t, "on", aos520cCand(10))
	linhas := modelCandidateBanner(aServir, aos486Modelo)
	c, _ := aServir.candidatoAServir(aos486Modelo)
	if len(linhas) != 1 {
		t.Fatalf("linhas = %q", linhas)
	}
	for _, quer := range []string{"A SERVIR", `titular "gpt-4o"`, `candidato "gpt-4o-mini"`, digestCand, c.entryDigest, "fatia assinada 10%", "ate 40 planos", "aos-canary/v1", "NAO passa a titular"} {
		if !strings.Contains(linhas[0], quer) {
			t.Fatalf("a linha do candidato a servir nao tem %q:\n%s", quer, linhas[0])
		}
	}
	if perfis := modelRouteProfilesBanner(aServir); len(perfis) != 1 || !strings.Contains(perfis[0], "10% — EM VIGOR") || strings.Contains(perfis[0], "SEM EFEITO") {
		t.Fatalf("a linha do perfil do candidato a servir: %q", perfis)
	}

	for nome, c := range map[string]struct {
		assinados *perfisAssinados
		razao     string
	}{
		"off":          {carregar(t, "off", aos520cCand(10)), "AOS_MODEL_CANDIDATE=off"},
		"fatia a zero": {carregar(t, "on", aos520cCand(0)), "a fatia assinada e 0%"},
	} {
		linhas := modelCandidateBanner(c.assinados, aos486Modelo)
		if len(linhas) != 1 || !strings.Contains(linhas[0], "candidato NAO SERVIDO") || !strings.Contains(linhas[0], "candidato nao servido") ||
			!strings.Contains(linhas[0], c.razao) || !strings.Contains(linhas[0], `alias "gpt-4o-mini"`) || !strings.Contains(linhas[0], "RECUSADO") {
			t.Fatalf("%s: %q", nome, linhas)
		}
		if perfis := modelRouteProfilesBanner(c.assinados); len(perfis) != 1 || !strings.Contains(perfis[0], "SEM EFEITO nesta versao (AOS-520)") {
			t.Fatalf("%s: a linha do perfil e a de antes: %q", nome, perfis)
		}
	}
	if linhas := modelCandidateBanner(carregar(t, "off"), aos486Modelo); linhas != nil {
		t.Fatalf("sem registo nao-titular e com off: %q", linhas)
	}
	if linhas := modelCandidateBanner(carregar(t, "on"), aos486Modelo); len(linhas) != 1 || !strings.Contains(linhas[0], "nao ha candidato") {
		t.Fatalf("on sem registo nao-titular: %q", linhas)
	}
	// Sem a pasta: nada com o interruptor ausente ou a off; um aviso com ele a on.
	for _, v := range []string{"", "off"} {
		aos520Interruptor(t, v)
		if linhas := modelCandidateBanner(nil, aos486Modelo); linhas != nil {
			t.Fatalf("sem a pasta, interruptor %q: %q", v, linhas)
		}
	}
	aos520Interruptor(t, "on")
	if linhas := modelCandidateBanner(nil, aos486Modelo); len(linhas) != 1 || !strings.HasPrefix(linhas[0], "AVISO") || !strings.Contains(linhas[0], "nao ha candidato") {
		t.Fatalf("sem a pasta, com on: %q", linhas)
	}
	aos520Interruptor(t, "")
	// Um registo para o alias do TITULAR não é um candidato.
	if linhas := modelCandidateBanner(carregar(t, "off", aos520Registo{"titular", aos520PerfilA, 10}), aos486Modelo); linhas != nil {
		t.Fatalf("um registo para o alias do titular nao e um candidato: %q", linhas)
	}
}

// O DESPACHANTE. Sem alias no contexto, o titular; com um alias conhecido, o adaptador desse alias;
// com um alias DESCONHECIDO, erro — nunca o titular.
func TestAOS520_Despachante_AliasDesconhecidoEErroNuncaOTitular(t *testing.T) {
	chamado := ""
	fake := func(nome string) agentruntime.ModelClient {
		return aos520cClienteFalso(func() { chamado = nome })
	}
	d := &clientePorModelo{titular: "tit", porAlias: map[string]agentruntime.ModelClient{"tit": fake("tit"), "cand": fake("cand")}}
	ctx := context.Background()
	if _, err := d.Call(ctx, agentruntime.PromptView{}); err != nil || chamado != "tit" {
		t.Fatalf("sem alias: chamado=%q err=%v", chamado, err)
	}
	if _, err := d.Call(withModeloDoRun(ctx, "cand"), agentruntime.PromptView{}); err != nil || chamado != "cand" {
		t.Fatalf("alias do candidato: chamado=%q err=%v", chamado, err)
	}
	if _, err := d.Call(withModeloDoRun(ctx, "tit"), agentruntime.PromptView{}); err != nil || chamado != "tit" {
		t.Fatalf("alias do titular: chamado=%q err=%v", chamado, err)
	}
	chamado = ""
	_, err := d.Call(withModeloDoRun(ctx, "retirado"), agentruntime.PromptView{})
	if !errors.Is(err, ErrModelNotServed) || chamado != "" || !strings.Contains(err.Error(), modelgateway.PinnedCauseModelNotServed) {
		t.Fatalf("alias desconhecido: chamado=%q err=%v — tinha de ser erro, sem chamar adaptador nenhum", chamado, err)
	}
	if withModeloDoRun(ctx, "") != ctx || modeloDoRunFrom(ctx) != "" {
		t.Fatal("um alias vazio nao se anexa")
	}
}

type aos520cClienteFalso func()

func (f aos520cClienteFalso) Call(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	f()
	return agentruntime.ModelResponse{Final: true}, nil
}

// O CLIENTE DE MODELO DO NÓ É UM DESPACHANTE SÓ COM UM PERFIL NÃO-TITULAR — mesmo com `off`. Sem
// perfil não-titular (sem pasta, pasta vazia, ou só um registo para o alias do titular) o cliente é
// o de antes.
func TestAOS520_No_ODespachanteSoExisteComPerfilNaoTitular(t *testing.T) {
	despacha := func(t *testing.T, interruptor string, regs ...aos520Registo) (*clientePorModelo, bool) {
		aos519AmbienteDoGateway(t)
		aos520PastaCom(t, regs...)
		aos520Interruptor(t, interruptor)
		cliente, _, err := parseModelFromEnv(false)
		if err != nil {
			t.Fatal(err)
		}
		return aos520cDespachante(cliente)
	}
	for _, interruptor := range []string{"off", "on", ""} {
		d, ok := despacha(t, interruptor, aos520cCand(10))
		if !ok || d.titular != aos486Modelo || len(d.porAlias) != 2 || d.porAlias[aos520AliasCand] == nil || d.porAlias[aos486Modelo] == nil {
			t.Fatalf("interruptor %q com um registo nao-titular: o cliente tinha de ser um despachante com os dois aliases", interruptor)
		}
	}
	if _, ok := despacha(t, "on"); ok {
		t.Fatal("pasta vazia: sem despachante")
	}
	if _, ok := despacha(t, "on", aos520Registo{"titular", aos520PerfilA, 10}); ok {
		t.Fatal("so um registo para o alias do titular: sem despachante")
	}
}

// aos520cInner desembrulha um invólucro do cliente de modelo do nó.
func aos520cInner(c agentruntime.ModelClient) agentruntime.ModelClient {
	switch v := c.(type) {
	case clienteComPerfis:
		return v.inner
	case clienteComEstado:
		return v.inner
	case clienteComForma:
		return v.inner
	case clienteComRejeicoes:
		return v.inner
	case clienteComRota:
		return v.inner
	case custoNaoDerivadoClient:
		return v.inner
	case *toolEnrichingClient:
		return v.inner
	}
	return nil
}

// A CHAVE DO CANARY: o pedido de plano verificado, ou o id do run INTEIRO — nunca partido pelo `~`.
func TestAOS520_ChaveDoCanary(t *testing.T) {
	if got := chaveDoCanary("plano~no~2", origemDoPlano{pedido: "plano", seq: 7}); got != chaveDePedido("plano") {
		t.Fatalf("com o vinculo verificado a chave e o pedido: %+v", got)
	}
	if got := chaveDoCanary("a~b~c", origemDoPlano{}); got != chaveDeRun("a~b~c") {
		t.Fatalf("sem vinculo a chave e o id do run inteiro, sem o partir pelo `~`: %+v", got)
	}
	// Um pedido cujo id contém `~`: a chave é o id inteiro do pedido.
	if got := chaveDoCanary("pedido~com~til~no", origemDoPlano{pedido: "pedido~com~til"}); got != chaveDePedido("pedido~com~til") {
		t.Fatalf("a chave de um pedido com `~`: %+v", got)
	}
	// O tipo separa os dois espaços no livro, e NÃO entra na função de escolha.
	if chaveDePedido("x").indice() == chaveDeRun("x").indice() || passoDoPlano(chaveDePedido("x")) == passoDoPlano(chaveDeRun("x")) {
		t.Fatal("um pedido e um run com o mesmo id tem de ser duas chaves no livro")
	}
	want := []string{"plano_do_candidato_sem_candidato", "livro_do_canary_ilegivel", "livro_do_canary_em_contencao", "fixacao_nao_gravada", "fixacao_diferente_da_gravada", "livro_de_versao_mais_recente", "marca_sem_fixacao_gravada", "historia_do_run_ilegivel", "candidato_sem_o_perfil_do_registo"}
	if got := causasDaFixacaoDoModelo(); !reflect.DeepEqual(got, want) {
		t.Fatalf("o vocabulario das causas mudou: %q", got)
	}
}

// O `run.model_pinned` QUE JÁ LÁ ESTAVA VALE. Uma segunda escrita igual passa; uma diferente —
// outro alias, outro perfil ou outro papel — é recusada, e o evento gravado não muda.
func TestAOS520_GravarModeloFixado_OGravadoVale(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	p := modeloFixadoPayload{V: versaoDoModeloFixado, Role: "candidato", Alias: "cand", ProfileDigest: "sha256:p", EntryDigest: "sha256:e", SharePercent: 10, Function: versaoDaEscolha, Reason: razaoFuncao, PlanKey: "r"}
	if err := gravarModeloFixado(ctx, es, "r", p); err != nil {
		t.Fatal(err)
	}
	if err := gravarModeloFixado(ctx, es, "r", p); err != nil {
		t.Fatalf("a mesma fixacao, outra vez: %v", err)
	}
	for _, outro := range []modeloFixadoPayload{
		{V: p.V, Role: "titular", Alias: "tit", ProfileDigest: "sha256:t"},
		{V: p.V, Role: "candidato", Alias: "cand", ProfileDigest: "sha256:outro"},
		{V: p.V, Role: "titular", Alias: "cand", ProfileDigest: "sha256:p"},
	} {
		if err := gravarModeloFixado(ctx, es, "r", outro); causaDaFixacaoDoModelo(err) != causaFixacaoDiferenteDaGravada {
			t.Fatalf("uma fixacao diferente da gravada (%+v): %v", outro, err)
		}
	}
	if fix, tem := aos520cFixacao(t, es, "r"); !tem || fix != p {
		t.Fatalf("o evento gravado mudou: %+v", fix)
	}
	if err := gravarModeloFixado(ctx, nil, "r", p); causaDaFixacaoDoModelo(err) != causaFixacaoNaoGravada {
		t.Fatalf("sem Event Store: %v", err)
	}
	if causaDaFixacaoDoModelo(errors.New("outro erro")) != "" {
		t.Fatal("causa de um erro alheio")
	}
	// A recusa embrulha a sentinela e o erro de baixo.
	baixo := errors.New("de baixo")
	r := recusaDaFixacao(causaLivroIlegivel, baixo)
	if !errors.Is(r, ErrModelPinRefused) || !errors.Is(r, baixo) || !strings.Contains(r.Error(), "causa="+causaLivroIlegivel) {
		t.Fatalf("recusa: %v", r)
	}
	if r := recusaDaFixacao(causaLivroIlegivel, nil); !errors.Is(r, ErrModelPinRefused) || !strings.Contains(r.Error(), "causa="+causaLivroIlegivel) {
		t.Fatalf("recusa sem erro de baixo: %v", r)
	}
}

// O `seq` DO PEDIDO SAI DO LOG, COM O SUBMISSOR. A verificação do vínculo devolve, com o submissor,
// o `seq` do `planrequest.submitted` do pedido — o mesmo evento, a mesma leitura. É dele que o
// canary sabe se o plano é anterior à abertura. Uma recusa do vínculo não devolve `seq` nenhum.
func TestAOS520_No_OSeqDoPedidoSaiDoLogComOSubmissor(t *testing.T) {
	n := aos520cPlanoNo(t, nil, "off")
	outro := n.pedir(t, "um-plano-antes")
	const plano = "plano-da-origem"
	n.modeloChama(aos502Leitura)
	// Fecha o primeiro pedido para o segundo ser o reclamavel.
	_ = outro
	evs, err := n.node.EventStore.Read(context.Background(), planRequestStream, 1)
	if err != nil {
		t.Fatal(err)
	}
	var seq uint64
	for _, ev := range evs {
		if ev.Type == EventTypePlanRequestSubmitted && ev.StepID == prefixoPedido+"um-plano-antes" {
			seq = ev.Seq
		}
	}
	if seq == 0 {
		t.Fatal("o pedido nao esta na fila")
	}
	h := aos502Interno(t, n, n.node.EventStore)
	h.node.PlanDrainers = n.node.PlanDrainers
	chamador := readerIdentity{principal: govReader, board: govBoard, region: govRegion}
	v := vinculoAoPedido{RunID: "um-plano-antes", Geracao: outro, PlanID: aos502PlanID, NodeID: "no"}
	agora := time.Now().UTC()
	principal, s, err := h.pedidoVerificado(context.Background(), chamador, idDoRunFilho("um-plano-antes", "no"), v, agora)
	if err != nil || principal != aos502Submissor || s != seq {
		t.Fatalf("pedidoVerificado: principal=%q seq=%d err=%v; quer %q e o seq %d do planrequest.submitted", principal, s, err, aos502Submissor, seq)
	}
	if rb, err := h.submissorDoPedido(context.Background(), chamador, idDoRunFilho("um-plano-antes", "no"), v, agora); err != nil || rb != principal {
		t.Fatalf("submissorDoPedido: %q %v", rb, err)
	}
	v.RunID = plano // um pedido que nunca existiu
	if _, s, err := h.pedidoVerificado(context.Background(), chamador, idDoRunFilho(plano, "no"), v, agora); !errors.Is(err, errVinculoRecusado) || s != 0 {
		t.Fatalf("um vinculo recusado nao devolve seq: seq=%d err=%v", s, err)
	}
}
