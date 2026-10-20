package main

// AOS-540 — A NOVA TENTATIVA (AOS-502/506/510) DE UM NÓ QUE PEDIU ANTES DE LER.
//
// Na 1.6.0 o turno 1 de um run com `plan_input` nunca termina o run; um run que «não pediu tool
// nenhuma» grava dois turnos. As duas provas aceitam exactamente essa forma — e mais nenhuma.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

const aos540DigestDasEntradas = "sha256:" + "ab0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"

// aos540DoisTurnos reescreve o log REAL de um run de um turno (layout de sempre) como o de um run
// que pediu antes de ler: o turno 1 adiou as entradas, o turno 2 é o turno único original. `t1` e
// `t2` mudam o payload de cada turno depois da forma base.
func aos540DoisTurnos(t *testing.T, evs []eventstore.Event, t1, t2 func(map[string]any)) []eventstore.Event {
	t.Helper()
	var out []eventstore.Event
	for _, ev := range evs {
		if ev.Type != agentruntime.EventTypeTurnRecorded {
			out = append(out, ev)
			continue
		}
		for i, mudar := range []func(map[string]any){t1, t2} {
			var campos map[string]any
			if err := json.Unmarshal(ev.Payload, &campos); err != nil {
				t.Fatal(err)
			}
			man := campos["manifest"].(map[string]any)
			man["assembly_version"] = agentruntime.AssemblyVersion160
			campos["turn"] = i + 1
			if i == 0 {
				man["prompt_hash"] = "sha256:" + strings.Repeat("1", 64)
				campos["plan_inputs_deferred"] = 1
				campos["plan_inputs_digest"] = aos540DigestDasEntradas
				campos["tool_calls_requested"] = 0
				campos["stop_reason"] = "stop"
			}
			if mudar != nil {
				mudar(campos)
			}
			novo := ev
			novo.Payload, _ = json.Marshal(campos)
			out = append(out, novo)
		}
	}
	return out
}

type aos540Juiz func([]eventstore.Event, state.State, state.Outcome, quesitoDaTentativa) (string, string)

func aos540ProvaDosDoisTurnos(t *testing.T, juiz aos540Juiz, evs []eventstore.Event, estado state.State, desfecho state.Outcome, q quesitoDaTentativa) {
	t.Helper()
	ok := aos540DoisTurnos(t, evs, nil, nil)
	causa, hash := juiz(ok, estado, desfecho, q)
	if causa != "" || hash != "sha256:"+strings.Repeat("1", 64) {
		t.Fatalf("dois turnos de um run que pediu antes de ler, sem tool calls: ADMITIDO com o hash do turno 1; veio causa=%q hash=%q", causa, hash)
	}
	if got := digestDasEntradasDoRun(ok); got != aos540DigestDasEntradas {
		t.Fatalf("digest das entradas do run = %q", got)
	}
	man := func(c map[string]any) map[string]any { return c["manifest"].(map[string]any) }
	tres := append(aos540DoisTurnos(t, evs, nil, nil), aos540DoisTurnos(t, evs, nil, nil)...)
	var soTurnos []eventstore.Event
	for _, ev := range tres {
		if ev.Type == agentruntime.EventTypeTurnRecorded {
			soTurnos = append(soTurnos, ev)
		}
	}
	tresTurnos := append(append([]eventstore.Event(nil), evs...), soTurnos[:2]...)
	for _, c := range []struct {
		nome  string
		evs   []eventstore.Event
		causa string
	}{
		{"Turno1PediuTools", aos540DoisTurnos(t, evs, func(c map[string]any) { c["tool_calls_requested"] = 1 }, nil), causaRetryPediuTools},
		{"Turno1NaoParouEmStop", aos540DoisTurnos(t, evs, func(c map[string]any) { c["stop_reason"] = "length" }, nil), causaRetryParagem},
		{"Turno1NaoAdiou", aos540DoisTurnos(t, evs, func(c map[string]any) { delete(c, "plan_inputs_deferred") }, nil), causaRetryTurnos},
		{"LayoutQueNaoAdia", aos540DoisTurnos(t, evs, func(c map[string]any) { man(c)["assembly_version"] = agentruntime.AssemblyVersion150 }, nil), causaRetryTurnos},
		{"Turno2NoutroLayout", aos540DoisTurnos(t, evs, nil, func(c map[string]any) { man(c)["assembly_version"] = agentruntime.AssemblyVersion150 }), causaRetryTurnos},
		{"Turno2TambemAdiou", aos540DoisTurnos(t, evs, nil, func(c map[string]any) { c["plan_inputs_deferred"] = 1 }), causaRetryTurnos},
		{"TresTurnos", tresTurnos, causaRetryTurnos},
	} {
		t.Run(c.nome, func(t *testing.T) {
			if causa, hash := juiz(c.evs, estado, desfecho, q); causa != c.causa || hash != "" {
				t.Fatalf("causa=%q hash=%q, quero %q", causa, hash, c.causa)
			}
		})
	}
	// O log de UM turno continua admitido, com o hash dele, e sem digest de entradas.
	if causa, hash := juiz(evs, estado, desfecho, q); causa != "" || hash == "" || digestDasEntradasDoRun(evs) != "" {
		t.Fatalf("o turno unico tem de continuar admitido: causa=%q", causa)
	}
}

func TestAOS540_ProvaDoAOS502_DoisTurnosDeQuemPediuAntesDeLer(t *testing.T) {
	evs, estado, desfecho, q := aos502LogLimpo(t)
	aos540ProvaDosDoisTurnos(t, julgarTentativaAnterior, evs, estado, desfecho, q)
}

func TestAOS540_ProvaDoAOS510_DoisTurnosDeQuemPediuAntesDeLer(t *testing.T) {
	_, evs, estado, desfecho, q, _ := aos510LogLimpo(t)
	aos540ProvaDosDoisTurnos(t, julgarTentativaVazia, evs, estado, desfecho, q)
}

// aos540Entrada é um `plan_input` de produção, com o digest certo.
func aos540Entrada(conteudo string) []map[string]string {
	soma := sha256.Sum256([]byte(conteudo))
	return []map[string]string{{"from": "n0", "output": "document_content", "digest": "sha256:" + hex.EncodeToString(soma[:]), "content": conteudo}}
}

// aos540SubmeterTentativa envia o `POST /runs` da tentativa `n` (1 ⇒ a primeira) de um nó com
// entradas, como o drenador.
func aos540SubmeterTentativa(t *testing.T, n *aos502No, plano string, ger, tentativa int, conteudo string) {
	t.Helper()
	v := aos502Vinculo(plano, aos502No_, ger, tentativa)
	if tentativa == 1 {
		v = aos502Vinculo(plano, aos502No_, ger, 0)
	}
	id := idDaTentativa(plano, aos502No_, tentativa)
	r := postReq(n.h, "/runs", map[string]any{
		"run_id": id, "objective": "Le o documento notes", "principal_nhi": durAgent, "credential": n.tok,
		"tools": []string{aos502Leitura}, "completion_requires": []string{aos502Leitura},
		"inputs": aos540Entrada(conteudo), "plan_request": v,
	}, govHeaders())
	if r.Code != http.StatusCreated {
		t.Fatalf("tentativa %d: %d %s", tentativa, r.Code, r.Body.String())
	}
	n.esperar(t, id)
}

// NO NÓ COMPOSTO, com o interruptor ligado: o nó consumidor que fechou failed por
// contract_unmet_no_call em DOIS turnos sem tool calls tem a nova tentativa ADMITIDA; com as
// mesmas entradas a medição não conta diferença, e com OUTRAS entradas conta — o digest das
// entradas faz o que o hash do turno 1 já não consegue.
func TestAOS540_No_NovaTentativaDeUmNoConsumidor(t *testing.T) {
	n := aos502ComporCom(t, agentruntime.CompletionEnforce, 2, func(cfg *Config) { cfg.PlanInputDepoisDoPrimeiroTurno = true })
	const plano = "plano-540-tentativa"
	ger := n.pedir(t, plano)
	n.modeloNaoChama(aos493RespostasDeProducao(t)["plan-e2e-v0145-1791115918"], "stop")
	aos540SubmeterTentativa(t, n, plano, ger, 1, "dados do passo anterior")
	_, r := n.ler(t, idDaTentativa(plano, aos502No_, 1))
	if r.Status != "failed" || r.OutcomeReason != string(agentruntime.OutcomeContractNoCall) {
		t.Fatalf("pre-condicao: failed por contract_unmet_no_call; veio %+v", r)
	}
	evs, _ := n.node.EventStore.Read(context.Background(), idDaTentativa(plano, aos502No_, 1), 1)
	if got := len(aos486LerTurnos(t, evs)); got != 2 {
		t.Fatalf("pre-condicao: dois turnos na 1.6.0; veio %d", got)
	}

	api := aos502Interno(t, n, n.node.EventStore)
	chamador := readerIdentity{principal: govReader, board: govBoard, region: govRegion}
	prova2, causa, _ := api.provarTentativa(context.Background(), chamador, *aos502Vinculo(plano, aos502No_, ger, 2))
	if causa != "" || prova2.entradas == "" || prova2.promptHash != aos486LerTurnos(t, evs)[0].promptHash {
		t.Fatalf("a prova tinha de admitir, com o hash do turno 1 e o digest das entradas; causa=%q prova=%+v", causa, prova2)
	}
	aos540SubmeterTentativa(t, n, plano, ger, 2, "dados do passo anterior")
	api.medirPromptDaTentativa(context.Background(), idDaTentativa(plano, aos502No_, 2), prova2, nil)
	if got := api.tentativas.promptDiferente.Load(); got != 0 {
		t.Fatalf("as mesmas entradas e o mesmo pedido: a medicao fica a zero; contou %d", got)
	}
	prova3, causa, _ := api.provarTentativa(context.Background(), chamador, *aos502Vinculo(plano, aos502No_, ger, 3))
	if causa != "" {
		t.Fatalf("a prova da tentativa 3: causa=%q", causa)
	}
	aos540SubmeterTentativa(t, n, plano, ger, 3, "OUTRAS entradas")
	api.medirPromptDaTentativa(context.Background(), idDaTentativa(plano, aos502No_, 3), prova3, nil)
	if got := api.tentativas.promptDiferente.Load(); got != 1 {
		t.Fatalf("outras entradas: a medicao conta; contou %d", got)
	}
}

// MAX_TURNS < 2 num run com entradas e o interruptor ligado ⇒ 400, com a razão; sem entradas, ou
// com o interruptor desligado, o pedido é o de sempre.
func TestAOS540_No_MaxTurnsMenorQueDoisERecusado(t *testing.T) {
	for _, c := range []struct {
		nome     string
		ligado   bool
		entradas bool
		quer     int
	}{{"ligado-com-entradas", true, true, http.StatusBadRequest}, {"ligado-sem-entradas", true, false, http.StatusCreated}, {"desligado-com-entradas", false, true, http.StatusCreated}} {
		t.Run(c.nome, func(t *testing.T) {
			n := aos540No(t, c.ligado)
			corpo := map[string]any{
				"run_id": "run-540-max-" + c.nome, "objective": aos540Objectivo, "principal_nhi": durAgent,
				"credential": n.tok, "tools": []string{"arquivo"}, "max_turns": 1,
			}
			if c.entradas {
				corpo["inputs"] = aos540Entrada(aos540Documento)
			}
			rec := postJSON(n.h, "POST", "/runs", corpo)
			if rec.Code != c.quer {
				t.Fatalf("%d (%s), quero %d", rec.Code, rec.Body.String(), c.quer)
			}
			if c.quer == http.StatusBadRequest && !strings.Contains(rec.Body.String(), "max_turns tem de ser pelo menos 2") {
				t.Fatalf("a recusa tem de dizer porque: %s", rec.Body.String())
			}
		})
	}
}
