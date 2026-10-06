package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-505 — A ROTA SOB GOVERNAÇÃO, medida pelo nó COMPOSTO (`AOS_MODEL_ROUTE_GOVERNANCE`).
//
// O molde é o do AOS-490/AOS-504: o cliente de modelo sai de [parseModelFromEnv], como no
// arranque, e fala com um provider em httptest que grava o corpo de cada pedido — e que aqui
// emite os cabeçalhos que o proxy de produção emite sobre a rota (medidos em 2026-10-06 com a
// imagem de produção do LiteLLM, 1.96.2).

// Marcas que não podem aparecer em nenhum evento, métrica ou linha do nó: o identificador do
// deployment (um hash que inclui a chave do provider) e o caminho, a query e as credenciais do
// api_base.
const (
	aos505NoMarcaDoID      = "49a2733634d98e6913cebc7bba9c1d21e5377b2383bdd941f545526c168b5dbe"
	aos505NoMarcaDoCaminho = "caminho-marcado-505"
	aos505NoMarcaDaQuery   = "chave-marcada-505"
	aos505NoMarcaDoUser    = "utilizador-marcado-505"
	aos505NoAPIBaseA       = "https://" + aos505NoMarcaDoUser + ":segredo@api.a.exemplo.test:8443/" + aos505NoMarcaDoCaminho + "/v1?k=" + aos505NoMarcaDaQuery
	aos505NoHostA          = "api.a.exemplo.test:8443"
	aos505NoAPIBaseB       = "https://api.b.exemplo.test/" + aos505NoMarcaDoCaminho + "/v1"
	// aos505NoEsperado é o modelo esperado do perfil de `gpt-4o` (o modelo do nó destes testes).
	aos505NoEsperado = "openai/k3"
)

// aos505NoCabecalhos são os cabeçalhos do proxy para um modelo e um api_base.
func aos505NoCabecalhos(modelo, apiBase string) map[string]string {
	h := map[string]string{"x-litellm-model-id": aos505NoMarcaDoID, "x-litellm-model-group": aos486Modelo}
	if modelo != "" {
		h["x-litellm-model-name"] = modelo
	}
	if apiBase != "" {
		h["x-litellm-model-api-base"] = apiBase
	}
	return h
}

// aos505NoCompor compõe o nó com a governação da rota dada no ambiente. `definir` false ⇒ a
// variável do modo NÃO existe no ambiente do processo.
func aos505NoCompor(t *testing.T, modo string, definir bool, host string, cabecalhos func(pedido int) map[string]string) *aos486No {
	t.Helper()
	if definir {
		t.Setenv("AOS_MODEL_ROUTE_GOVERNANCE", modo)
	} else {
		aos504SemVariavel(t, "AOS_MODEL_ROUTE_GOVERNANCE")
	}
	t.Setenv("AOS_MODEL_ROUTE_API_HOST", host)
	aos504SemVariavel(t, "AOS_MODEL_PROJECTION_VERSION")
	n := aos486ComporCom(t, "native", nil)
	n.upstream.pede = "arquivo"
	n.upstream.responde = aos490RespostaDoProvider
	n.upstream.cabecalhos = cabecalhos
	return n
}

// aos505NoMetrics devolve o texto do `/metrics` do nó.
func aos505NoMetrics(t *testing.T, n *aos486No) string {
	t.Helper()
	rec := httptest.NewRecorder()
	n.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics: %d", rec.Code)
	}
	return rec.Body.String()
}

// aos505NoFamilias devolve as famílias do `/metrics` (`# TYPE`), por ordem alfabética.
func aos505NoFamilias(texto string) []string {
	var out []string
	for _, l := range strings.Split(texto, "\n") {
		if strings.HasPrefix(l, "# TYPE ") {
			out = append(out, strings.TrimPrefix(l, "# TYPE "))
		}
	}
	sort.Strings(out)
	return out
}

// aos505NoAmostra devolve o valor de uma amostra de `aos_model_route_checks_total`, e se existe.
func aos505NoAmostra(texto, resultado, servido string) (string, bool) {
	prefixo := `aos_model_route_checks_total{result="` + resultado + `",served="` + servido + `"} `
	for _, l := range strings.Split(texto, "\n") {
		if strings.HasPrefix(l, prefixo) {
			return strings.TrimPrefix(l, prefixo), true
		}
	}
	return "", false
}

// aos505NoRota é o que o `turn.recorded` de um turno gravou sobre a rota.
type aos505NoRota struct {
	servido, digest, check string
	temServido             bool
}

func aos505NoRotas(t *testing.T, eventos []eventstore.Event) []aos505NoRota {
	t.Helper()
	var out []aos505NoRota
	for _, ev := range eventos {
		if ev.Type != agentruntime.EventTypeTurnRecorded {
			continue
		}
		var p struct {
			Manifest struct {
				Model map[string]json.RawMessage `json:"model"`
			} `json:"manifest"`
			RouteCheck string `json:"route_check"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("turn.recorded ilegivel: %v", err)
		}
		r := aos505NoRota{check: p.RouteCheck}
		if raw, ok := p.Manifest.Model["served_model_id"]; ok {
			r.temServido = true
			_ = json.Unmarshal(raw, &r.servido)
		}
		if raw, ok := p.Manifest.Model["route_profile_digest"]; ok {
			_ = json.Unmarshal(raw, &r.digest)
		}
		out = append(out, r)
	}
	return out
}

// aos505NoSemMarcas falha se algum evento do run ou o `/metrics` tiver uma das marcas.
func aos505NoSemMarcas(t *testing.T, eventos []eventstore.Event, metrics string) {
	t.Helper()
	marcas := []string{aos505NoMarcaDoID, aos505NoMarcaDoID[:16], aos505NoMarcaDoCaminho, aos505NoMarcaDaQuery, aos505NoMarcaDoUser, "segredo@", "exemplo.test"}
	for _, ev := range eventos {
		for _, m := range marcas {
			if strings.Contains(string(ev.Payload), m) {
				t.Errorf("o evento %s leva %q — o identificador do deployment e o endpoint nao podem chegar a um evento: %s", ev.Type, m, ev.Payload)
			}
		}
	}
	for _, m := range marcas {
		if strings.Contains(metrics, m) {
			t.Errorf("o /metrics leva %q", m)
		}
	}
}

// COM A GOVERNAÇÃO DESLIGADA, O NÓ GRAVA OS BYTES DA BASE. Com a variável AUSENTE, vazia e em
// `off` — e o proxy a emitir todos os cabeçalhos, e o host esperado definido —: os pedidos ao
// provider são os goldens do AOS-490; os `turn.recorded` são, byte a byte, os medidos na base
// deste ticket; os eventos são os mesmos, pela mesma ordem; a parte em claro das capturas é a da
// base e o payload não ganha chaves; o `/metrics` tem as mesmas famílias e nenhuma da rota; e o
// arranque não declara linha nenhuma.
func TestAOS505_No_Off_SaoOsBytesDaBase(t *testing.T) {
	const runID = "run-490-nativo" // o do run de referência: os mesmos prompt_hash
	cabecalhos := func(int) map[string]string { return aos505NoCabecalhos("openai/outro-modelo", aos505NoAPIBaseB) }
	for _, c := range []struct {
		nome, modo string
		definir    bool
	}{{"variavel ausente", "", false}, {"variavel vazia", "", true}, {"off", "off", true}, {"off com espacos", " off  ", true}} {
		t.Run(c.nome, func(t *testing.T) {
			n := aos505NoCompor(t, c.modo, c.definir, aos505NoHostA, cabecalhos)
			m := n.correr(t, runID, nil)
			if len(m.pedidos) != 2 {
				t.Fatalf("queria 2 pedidos, vieram %d", len(m.pedidos))
			}
			for i, quer := range []string{aos490Pedido1, aos490Pedido2} {
				if got := string(m.pedidos[i].cru); got != quer {
					t.Errorf("pedido %d nao e o golden do AOS-490:\n veio:  %s\n quero: %s", i+1, got, quer)
				}
			}
			if got := aos504TiposDeEvento(m); !reflect.DeepEqual(got, aos505BaseTiposDeEvento) {
				t.Errorf("os eventos do run mudaram:\n veio:  %v\n quero: %v", got, aos505BaseTiposDeEvento)
			}
			var turnos, capturas []string
			for _, ev := range m.eventos {
				switch ev.Type {
				case agentruntime.EventTypeTurnRecorded:
					turnos = append(turnos, string(ev.Payload))
				case replay.EventTypeCaptured:
					var campos map[string]json.RawMessage
					if err := json.Unmarshal(ev.Payload, &campos); err != nil {
						t.Fatalf("captura ilegivel: %v", err)
					}
					var chaves []string
					for k := range campos {
						chaves = append(chaves, k)
					}
					sort.Strings(chaves)
					if !reflect.DeepEqual(chaves, aos505BaseChavesDaCaptura) {
						t.Errorf("as chaves da captura mudaram: %v, quero %v", chaves, aos505BaseChavesDaCaptura)
					}
					capturas = append(capturas, string(campos["response"]))
				}
			}
			if !reflect.DeepEqual(turnos, aos505BaseTurnos) {
				t.Errorf("os turn.recorded nao sao os da base:\n veio:  %v\n quero: %v", turnos, aos505BaseTurnos)
			}
			if !reflect.DeepEqual(capturas, aos505BaseCapturas) {
				t.Errorf("a parte em claro das capturas nao e a da base:\n veio:  %v\n quero: %v", capturas, aos505BaseCapturas)
			}
			metrics := aos505NoMetrics(t, n)
			if got := aos505NoFamilias(metrics); !reflect.DeepEqual(got, aos505BaseFamiliasDoMetrics) {
				t.Errorf("as familias do /metrics mudaram:\n veio:  %v\n quero: %v", got, aos505BaseFamiliasDoMetrics)
			}
			if strings.Contains(metrics, "aos_model_route") {
				t.Errorf("com a governacao desligada o /metrics nao pode ter a familia da rota")
			}
			if n.node.rotaDoModelo != nil {
				t.Errorf("com a governacao desligada o no nao tem contadores da rota")
			}
			aos505NoSemMarcas(t, m.eventos, metrics)
		})
	}
	// O arranque não declara nada, e o cliente de modelo não ganha invólucro.
	if got := modelRouteBanner(true, "off", aos486Modelo, true); got != nil {
		t.Errorf("com off o banner nao tem linha nenhuma; veio %v", got)
	}
}

// EM OBSERVAÇÃO, COM A ROTA IGUAL: os pedidos continuam a ser os goldens (a governação não muda
// o que o provider recebe); cada `turn.recorded` passa a ter o modelo que o proxy DECLAROU, o
// digest do perfil e `route_check: igual`; o contador conta dois turnos iguais; e o replay
// devolve o mesmo modelo servido.
func TestAOS505_No_Observe_RotaIgual(t *testing.T) {
	const runID = "run-490-nativo"
	n := aos505NoCompor(t, "observe", true, aos505NoHostA, func(int) map[string]string { return aos505NoCabecalhos(aos505NoEsperado, aos505NoAPIBaseA) })
	m := n.correr(t, runID, nil)
	for i, quer := range []string{aos490Pedido1, aos490Pedido2} {
		if got := string(m.pedidos[i].cru); got != quer {
			t.Fatalf("pedido %d: a governacao da rota mudou o pedido ao provider:\n veio:  %s\n quero: %s", i+1, got, quer)
		}
	}
	perfil, _ := modelgateway.RouteProfileFor(aos486Modelo)
	rotas := aos505NoRotas(t, m.eventos)
	if len(rotas) != 2 {
		t.Fatalf("queria 2 turnos, vieram %d", len(rotas))
	}
	for i, r := range rotas {
		if r != (aos505NoRota{servido: aos505NoEsperado, digest: perfil.Digest(), check: "igual", temServido: true}) {
			t.Errorf("turno %d: rota = %+v; quero %s, o digest do perfil e igual", i+1, r, aos505NoEsperado)
		}
	}
	// A única diferença para a base é a rota: tirando os três campos, os turnos são os da base.
	for i, ev := range aos505NoTurnos(m.eventos) {
		got := strings.Replace(string(ev), `,"route_check":"igual"`, "", 1)
		got = strings.Replace(got, `"served_model_id":"`+aos505NoEsperado+`"`, `"served_model_id":"gpt-4o"`, 1)
		got = strings.Replace(got, `,"route_profile_digest":"`+perfil.Digest()+`"`, "", 1)
		if got != aos505BaseTurnos[i] {
			t.Errorf("turno %d: fora dos tres campos da rota o turn.recorded mudou:\n veio:  %s\n quero: %s", i+1, got, aos505BaseTurnos[i])
		}
	}
	if got := aos504TiposDeEvento(m); !reflect.DeepEqual(got, aos505BaseTiposDeEvento) {
		t.Errorf("a governacao da rota nao acrescenta eventos ao stream do run: %v", got)
	}
	metrics := aos505NoMetrics(t, n)
	if v, ok := aos505NoAmostra(metrics, "igual", aos505NoEsperado); !ok || v != "2" {
		t.Errorf("aos_model_route_checks_total{igual,%s} = %q (existe=%v), quero 2", aos505NoEsperado, v, ok)
	}
	for _, par := range [][2]string{{"diferente", "outro"}, {"nao_reportado", "nao_reportado"}, {"diferente", aos505NoEsperado}} {
		if v, ok := aos505NoAmostra(metrics, par[0], par[1]); !ok || v != "0" {
			t.Errorf("aos_model_route_checks_total{%s,%s} = %q (existe=%v), quero 0 — uma amostra por par, sempre presente", par[0], par[1], v, ok)
		}
	}
	aos505NoSemMarcas(t, m.eventos, metrics)

	rep := aos505NoReplay(t, n, runID, m)
	for i, s := range rep.Steps {
		if s.Response.Model != aos505NoEsperado || s.Response.RouteCheck != agentruntime.RouteEqual || s.Response.RouteProfileDigest != perfil.Digest() {
			t.Errorf("replay, turno %d: modelo %q check %q digest %q; quero os do turno original", i+1, s.Response.Model, s.Response.RouteCheck, s.Response.RouteProfileDigest)
		}
	}
}

// aos505NoTurnos devolve os payloads dos `turn.recorded`, pela ordem.
func aos505NoTurnos(eventos []eventstore.Event) [][]byte {
	var out [][]byte
	for _, ev := range eventos {
		if ev.Type == agentruntime.EventTypeTurnRecorded {
			out = append(out, ev.Payload)
		}
	}
	return out
}

// aos505NoReplay reproduz o run pelo motor de replay e exige que não divirja.
func aos505NoReplay(t *testing.T, n *aos486No, runID string, m aos486Medido) *replay.ReplayResult {
	t.Helper()
	return aos505NoReplayCom(t, n, runID, m, aos486Modelo)
}

// aos505NoReplayCom é [aos505NoReplay] para um run gravado com o modelo dado.
func aos505NoReplayCom(t *testing.T, n *aos486No, runID string, m aos486Medido, modelo string) *replay.ReplayResult {
	t.Helper()
	eng, err := replay.NewEngine(n.node.EventStore, replay.WithContentOpener(n.node.contentOpener, replay.Accessor{
		Principal: "nhi:leitor-aos505", Scopes: []string{replay.DefaultSovereignContentScope},
	}))
	if err != nil {
		t.Fatalf("replay.NewEngine: %v", err)
	}
	rep, err := eng.Replay(context.Background(), runID, replay.Options{Spec: replay.TrajectorySpec{
		Objective: aos490Objectivo, Tools: m.turnos[0].specs, Model: agentruntime.ModelConfig{ModelID: modelo},
	}})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if rep.Divergence != nil || len(rep.Steps) != len(m.turnos) {
		t.Fatalf("o replay tinha de reproduzir os %d turnos sem divergir: %d passos, %+v", len(m.turnos), len(rep.Steps), rep.Divergence)
	}
	return &rep
}

// UMA TROCA POR BAIXO A MEIO DO RUN, EM OBSERVAÇÃO. O primeiro turno é servido pela rota do
// perfil; antes do segundo, a configuração do proxy muda. O turno seguinte regista a variância —
// `route_check: diferente` com o modelo que o proxy passou a declarar — e o contador sobe; o run
// segue e conclui. O replay devolve o mesmo modelo servido e a mesma variância, turno a turno.
func TestAOS505_No_Observe_TrocaPorBaixoAMeioDoRun(t *testing.T) {
	perfil, _ := modelgateway.RouteProfileFor(aos486Modelo)
	for _, c := range []struct {
		nome, runID      string
		depois           map[string]string
		servido, rotulo  string
		check            string
		temServidoDepois bool
	}{
		{"outro modelo, de fora dos perfis", "run-505-troca-modelo", aos505NoCabecalhos("openai/modelo-trocado-por-baixo", aos505NoAPIBaseA), "openai/modelo-trocado-por-baixo", "outro", "diferente", true},
		{"mesmo modelo, outro endpoint", "run-505-troca-endpoint", aos505NoCabecalhos(aos505NoEsperado, aos505NoAPIBaseB), aos505NoEsperado, aos505NoEsperado, "diferente", true},
		{"o proxy deixa de reportar", "run-505-troca-silencio", map[string]string{"x-litellm-model-id": aos505NoMarcaDoID}, "", "nao_reportado", "nao_reportado", false},
	} {
		t.Run(c.nome, func(t *testing.T) {
			n := aos505NoCompor(t, "observe", true, aos505NoHostA, func(pedido int) map[string]string {
				if pedido == 0 {
					return aos505NoCabecalhos(aos505NoEsperado, aos505NoAPIBaseA)
				}
				return c.depois
			})
			m := n.correr(t, c.runID, nil) // o run conclui: em observação o turno segue
			rotas := aos505NoRotas(t, m.eventos)
			if len(rotas) != 2 {
				t.Fatalf("queria 2 turnos, vieram %d", len(rotas))
			}
			if rotas[0] != (aos505NoRota{servido: aos505NoEsperado, digest: perfil.Digest(), check: "igual", temServido: true}) {
				t.Fatalf("turno 1 (antes da troca): %+v", rotas[0])
			}
			if rotas[1] != (aos505NoRota{servido: c.servido, digest: perfil.Digest(), check: c.check, temServido: c.temServidoDepois}) {
				t.Fatalf("turno 2 (depois da troca): %+v; quero servido %q e %s", rotas[1], c.servido, c.check)
			}
			if !c.temServidoDepois && strings.Contains(string(aos505NoTurnos(m.eventos)[1]), "served_model_id") {
				t.Fatalf("um modelo nao reportado nao pode ser preenchido — nem com o nome pedido, nem com o do corpo: %s", aos505NoTurnos(m.eventos)[1])
			}
			metrics := aos505NoMetrics(t, n)
			if v, _ := aos505NoAmostra(metrics, "igual", aos505NoEsperado); v != "1" {
				t.Errorf("contador igual = %q, quero 1", v)
			}
			if v, ok := aos505NoAmostra(metrics, c.check, c.rotulo); !ok || v != "1" {
				t.Errorf("contador {%s,%s} = %q (existe=%v), quero 1", c.check, c.rotulo, v, ok)
			}
			if strings.Contains(metrics, "modelo-trocado-por-baixo") {
				t.Errorf("um nome fora do perfil entrou num rotulo do /metrics")
			}
			aos505NoSemMarcas(t, m.eventos, metrics)

			rep := aos505NoReplay(t, n, c.runID, m)
			if s := rep.Steps[0].Response; s.Model != aos505NoEsperado || s.RouteCheck != agentruntime.RouteEqual {
				t.Errorf("replay, turno 1: %q %q", s.Model, s.RouteCheck)
			}
			if s := rep.Steps[1].Response; s.Model != c.servido || string(s.RouteCheck) != c.check || s.RouteProfileDigest != perfil.Digest() {
				t.Errorf("replay, turno 2: modelo %q check %q; quero o mesmo modelo servido (%q) e a mesma variancia (%s)", s.Model, s.RouteCheck, c.servido, c.check)
			}
		})
	}
}

// A MESMA TROCA, EM IMPOSIÇÃO: o turno seguinte FALHA com causa em vocabulário fechado, o run
// não conclui, o turno recusado não fica gravado como turno, e o contador conta-o.
func TestAOS505_No_Enforce_TrocaPorBaixoFalhaOTurno(t *testing.T) {
	const runID = "run-505-enforce"
	n := aos505NoCompor(t, "enforce", true, aos505NoHostA, func(pedido int) map[string]string {
		if pedido == 0 {
			return aos505NoCabecalhos(aos505NoEsperado, aos505NoAPIBaseA)
		}
		return aos505NoCabecalhos("openai/modelo-trocado-por-baixo", aos505NoAPIBaseA)
	})
	rec := postJSON(n.h, "POST", "/runs", map[string]any{
		"run_id": runID, "objective": aos490Objectivo, "principal_nhi": durAgent, "credential": n.tok, "max_turns": 3,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs: %d (%s)", rec.Code, rec.Body.String())
	}
	wctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	oc, ok, werr := n.svc.Wait(wctx, runID)
	if werr != nil || !ok {
		t.Fatalf("Wait: ok=%v err=%v", ok, werr)
	}
	if !errors.Is(oc.Err, modelgateway.ErrRouteVariance) || !errors.Is(oc.Err, agentruntime.ErrModelCall) {
		t.Fatalf("o run tinha de falhar com ErrRouteVariance numa chamada ao modelo; veio %v", oc.Err)
	}
	var rerr *modelgateway.RouteVarianceError
	if !errors.As(oc.Err, &rerr) || rerr.Cause != modelgateway.RouteCauseModelDifferent || rerr.Check != "diferente" {
		t.Fatalf("a causa tinha de ser modelo_diferente: %+v", rerr)
	}
	if strings.Contains(oc.Err.Error(), "modelo-trocado-por-baixo") || strings.Contains(oc.Err.Error(), "exemplo.test") {
		t.Errorf("o erro leva texto do proxy: %v", oc.Err)
	}
	m := n.medir(t, runID, 0)
	rotas := aos505NoRotas(t, m.eventos)
	if len(rotas) != 1 || rotas[0].check != "igual" {
		t.Fatalf("so o turno 1 fica gravado, e igual; vieram %+v", rotas)
	}
	if len(m.pedidos) != 2 {
		t.Fatalf("o provider tinha de receber os 2 pedidos (o segundo foi servido e recusado): %d", len(m.pedidos))
	}
	metrics := aos505NoMetrics(t, n)
	if v, _ := aos505NoAmostra(metrics, "igual", aos505NoEsperado); v != "1" {
		t.Errorf("contador igual = %q, quero 1", v)
	}
	if v, _ := aos505NoAmostra(metrics, "diferente", "outro"); v != "1" {
		t.Errorf("contador {diferente,outro} = %q, quero 1 — o turno recusado foi comparado", v)
	}
	aos505NoSemMarcas(t, m.eventos, metrics)
}

// AS VARIÁVEIS SÃO DE VOCABULÁRIO FECHADO, E O ARRANQUE RECUSA O QUE NÃO PERCEBE.
func TestAOS505_Env_VocabularioFechadoEBanner(t *testing.T) {
	if defaultModelRouteGovernance != "off" {
		t.Fatalf("a omissao tem de ser off: %q", defaultModelRouteGovernance)
	}
	t.Setenv("AOS_MODEL_ROUTE_API_HOST", "")
	for _, mau := range []string{"Observe", "on", "true", "enforcing", "observe,enforce"} {
		t.Setenv("AOS_MODEL_ROUTE_GOVERNANCE", mau)
		if got, err := parseModelRouteGovernanceFromEnv(); !errors.Is(err, ErrBadModelRouteGovernance) || got != "" {
			t.Errorf("%q: queria ErrBadModelRouteGovernance, veio %q %v", mau, got, err)
		}
		t.Setenv("AOS_MODEL_ENDPOINT", "http://127.0.0.1:1")
		t.Setenv("AOS_MODEL_NAME", aos486Modelo)
		if client, binder, err := parseModelFromEnv(false); !errors.Is(err, ErrBadModelRouteGovernance) || client != nil || binder != nil {
			t.Errorf("%q: o arranque tinha de recusar; veio %v", mau, err)
		}
	}
	// O host esperado: só um host. O valor recusado NÃO vai na mensagem.
	t.Setenv("AOS_MODEL_ROUTE_GOVERNANCE", "observe")
	for _, mau := range []string{"https://api.exemplo.test", "api.exemplo.test/v1", "u:segredo-505@api.exemplo.test", "host com espaco", "api.exemplo.test?k=1"} {
		t.Setenv("AOS_MODEL_ROUTE_API_HOST", mau)
		_, err := parseModelRouteAPIHostFromEnv()
		if !errors.Is(err, ErrBadModelRouteAPIHost) {
			t.Errorf("%q: queria ErrBadModelRouteAPIHost, veio %v", mau, err)
			continue
		}
		if strings.Contains(err.Error(), "segredo-505") || strings.Contains(err.Error(), "exemplo.test") {
			t.Errorf("a mensagem repete o valor recusado: %v", err)
		}
		if _, _, err := parseModelFromEnv(false); !errors.Is(err, ErrBadModelRouteAPIHost) {
			t.Errorf("%q: o arranque tinha de recusar; veio %v", mau, err)
		}
	}
	for entra, sai := range map[string]string{"api.kimi.com": "api.kimi.com", " API.Kimi.com:443 ": "api.kimi.com:443", "": ""} {
		t.Setenv("AOS_MODEL_ROUTE_API_HOST", entra)
		if got, err := parseModelRouteAPIHostFromEnv(); err != nil || got != sai {
			t.Errorf("host %q: veio %q %v, quero %q", entra, got, err, sai)
		}
	}
	// Ligada, com um modelo sem perfil: o arranque recusa. Desligada, o mesmo modelo arranca.
	t.Setenv("AOS_MODEL_ROUTE_API_HOST", "")
	if _, _, err := modelRouteFromEnv("modelo-sem-perfil"); !errors.Is(err, ErrModelRouteWithoutProfile) {
		t.Errorf("governacao ligada e modelo sem perfil: queria ErrModelRouteWithoutProfile, veio %v", err)
	}
	t.Setenv("AOS_MODEL_ROUTE_GOVERNANCE", "off")
	if cfg, contadores, err := modelRouteFromEnv("modelo-sem-perfil"); err != nil || contadores != nil || !reflect.DeepEqual(cfg, modelgateway.RouteGovernance{}) {
		t.Errorf("desligada: queria a configuracao a zero e sem contadores; veio %+v %v %v", cfg, contadores, err)
	}
	// O banner: nada com off ou sem gateway; uma linha com o modo, o perfil e os limites.
	if modelRouteBanner(false, "enforce", aos486Modelo, true) != nil {
		t.Error("sem gateway composto nao ha linha")
	}
	for _, modo := range []string{"observe", "enforce"} {
		linhas := modelRouteBanner(true, modo, aos486Modelo, false)
		if len(linhas) != 1 {
			t.Fatalf("%s: queria 1 linha, vieram %d", modo, len(linhas))
		}
		for _, quer := range []string{"AOS_MODEL_ROUTE_GOVERNANCE=" + modo, aos505NoEsperado, "NAO detecta", "nao sao atestacao", "NAO e comparado"} {
			if !strings.Contains(linhas[0], quer) {
				t.Errorf("%s: o banner nao diz %q: %s", modo, quer, linhas[0])
			}
		}
	}
}

// OS CONTADORES FECHAM OS DOIS VOCABULÁRIOS: um par que não seja deles não é contado com o texto
// recebido.
func TestAOS505_Contadores_VocabularioFechado(t *testing.T) {
	c := novosContadoresDaRota()
	c.observar(modelgateway.RouteObservation{Check: "igual", Served: aos505NoEsperado})
	c.observar(modelgateway.RouteObservation{Check: "diferente", Served: "texto-livre-do-proxy"})
	c.observar(modelgateway.RouteObservation{Check: "resultado-inventado", Served: aos505NoEsperado})
	if c.lido("igual", aos505NoEsperado) != 1 || c.lido("diferente", "outro") != 1 || c.lido("nao_reportado", aos505NoEsperado) != 1 {
		t.Fatalf("contagem errada: %d %d %d", c.lido("igual", aos505NoEsperado), c.lido("diferente", "outro"), c.lido("nao_reportado", aos505NoEsperado))
	}
	if c.lido("diferente", "texto-livre-do-proxy") != 0 || c.total["resultado-inventado"] != nil {
		t.Fatal("um valor fora do vocabulario ganhou uma serie")
	}
	var nulo *contadoresDaRota
	nulo.observar(modelgateway.RouteObservation{Check: "igual"}) // nil-safe
	if nulo.lido("igual", "outro") != 0 {
		t.Fatal("contadores nil")
	}
	// Os decoradores do cliente de modelo passam os contadores adiante.
	base := clienteComRota{contadores: c}
	if rotaDoCliente(custoNaoDerivadoClient{inner: base}) != c || rotaDoCliente(&toolEnrichingClient{inner: custoNaoDerivadoClient{inner: base}}) != c {
		t.Fatal("os decoradores perderam os contadores da rota")
	}
	if rotaDoCliente(nil) != nil || rotaDoCliente(&countingModel{}) != nil {
		t.Fatal("um cliente sem contadores devolve nil")
	}
}
