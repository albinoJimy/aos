package main

// AOS-507 — A FORMA DA RESPOSTA DO PROVIDER, NO NÓ COMPOSTO.
//
// O nó é o de [aos486ComporCom], com o cliente de modelo de [parseModelFromEnv]; o provider é o
// de ensaio, com a resposta do LiteLLM de produção ([aos490RespostaDoProvider]). O run de
// referência é o do AOS-490 (`run-490-nativo`), e os goldens são os MEDIDOS NA BASE pelo AOS-505
// (aos505_goldens_da_base_test.go) — gerados antes de qualquer código deste ticket.

import (
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// aos507Compor compõe o nó com a medição da forma dada no ambiente (e a governação da rota
// desligada). `definir` false ⇒ a variável NÃO existe no ambiente do processo.
func aos507Compor(t *testing.T, modo string, definir bool) *aos486No {
	t.Helper()
	if definir {
		t.Setenv("AOS_MODEL_RESPONSE_SHAPE", modo)
	} else {
		aos504SemVariavel(t, "AOS_MODEL_RESPONSE_SHAPE")
	}
	return aos505NoCompor(t, "", false, "", nil)
}

// aos507Partes separa os `turn.recorded` e a parte em claro das capturas de um run.
func aos507Partes(t *testing.T, m aos486Medido) (turnos, capturas []string) {
	t.Helper()
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
	return turnos, capturas
}

// COM A MEDIÇÃO DESLIGADA, O NÓ GRAVA OS BYTES DA BASE. Com a variável ausente, vazia e em `off`:
// os pedidos ao provider são os goldens do AOS-490; os `turn.recorded` e a parte em claro das
// capturas são, byte a byte, os medidos na base; os eventos são os mesmos, pela mesma ordem; o
// `/metrics` tem as mesmas famílias e nenhuma da forma; e o arranque não declara linha nenhuma.
func TestAOS507_No_Off_SaoOsBytesDaBase(t *testing.T) {
	const runID = "run-490-nativo"
	for _, c := range []struct {
		nome, modo string
		definir    bool
	}{{"variavel ausente", "", false}, {"variavel vazia", "", true}, {"off", "off", true}, {"off com espacos", " off  ", true}} {
		t.Run(c.nome, func(t *testing.T) {
			n := aos507Compor(t, c.modo, c.definir)
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
			turnos, capturas := aos507Partes(t, m)
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
			if strings.Contains(metrics, "response_shape") || n.node.formaDaResposta != nil {
				t.Errorf("com a medicao desligada o no nao tem contadores nem familia da forma")
			}
			for _, ev := range m.eventos {
				if strings.Contains(string(ev.Payload), "response_shape") {
					t.Errorf("com a medicao desligada o evento %s leva a ficha", ev.Type)
				}
			}
		})
	}
	if got := modelResponseShapeBanner(true, "off"); got != nil {
		t.Errorf("com off o banner nao tem linha nenhuma; veio %v", got)
	}
}

var aos507CampoDaFicha = regexp.MustCompile(`,"response_shape":\{[^{}]*\}`)

// EM OBSERVAÇÃO: os pedidos continuam a ser os goldens; cada `turn.recorded` é o da base MAIS a
// ficha; as capturas são as da base; não há eventos novos; o contador conta os dois turnos nas
// séries certas; e o replay do run gravado com a ficha é fiel.
func TestAOS507_No_Observe_FichaEmCadaTurno(t *testing.T) {
	const runID = "run-490-nativo"
	n := aos507Compor(t, "observe", true)
	m := n.correr(t, runID, nil)
	for i, quer := range []string{aos490Pedido1, aos490Pedido2} {
		if got := string(m.pedidos[i].cru); got != quer {
			t.Fatalf("pedido %d: a medicao mudou o pedido ao provider:\n veio:  %s\n quero: %s", i+1, got, quer)
		}
	}
	if got := aos504TiposDeEvento(m); !reflect.DeepEqual(got, aos505BaseTiposDeEvento) {
		t.Errorf("a medicao nao acrescenta eventos ao stream do run: %v", got)
	}
	turnos, capturas := aos507Partes(t, m)
	if !reflect.DeepEqual(capturas, aos505BaseCapturas) {
		t.Errorf("a ficha nao entra na captura; a parte em claro mudou:\n veio:  %v\n quero: %v", capturas, aos505BaseCapturas)
	}
	querFichas := []string{
		`,"response_shape":{"content":"vazio","content_bytes":0,"reasoning":"reasoning_content","reasoning_form":"string","reasoning_bytes":60,"reasoning_signed":"nao","refusal":"ausente","tool_calls_n":1,"tool_call_id":"outro","tool_call_id_max_bytes":13,"arguments_form":"string","legacy_function_call":"nao","choices_n":1,"finish_reason_mapped":"sim","reasoning_tokens":3,"system_fingerprint":"nao","unknown_keys_n":0,"shape_digest":"sha256:`,
		`,"response_shape":{"content":"texto","content_bytes":5,"reasoning":"reasoning_content","reasoning_form":"string","reasoning_bytes":54,"reasoning_signed":"nao","refusal":"ausente","tool_calls_n":0,"tool_call_id":"nenhum","tool_call_id_max_bytes":0,"legacy_function_call":"nao","choices_n":1,"finish_reason_mapped":"sim","reasoning_tokens":3,"system_fingerprint":"nao","unknown_keys_n":0,"shape_digest":"sha256:`,
	}
	if len(turnos) != 2 {
		t.Fatalf("queria 2 turnos, vieram %d", len(turnos))
	}
	for i, got := range turnos {
		ficha := aos507CampoDaFicha.FindString(got)
		if !strings.HasPrefix(ficha, querFichas[i]) {
			t.Errorf("turno %d: ficha inesperada:\n veio:  %s\n quero: %s…", i+1, ficha, querFichas[i])
		}
		if sem := strings.Replace(got, ficha, "", 1); sem != aos505BaseTurnos[i] {
			t.Errorf("turno %d: fora da ficha o turn.recorded mudou:\n veio:  %s\n quero: %s", i+1, sem, aos505BaseTurnos[i])
		}
		if strings.Contains(got, aos490RaciocinioMarca) || strings.Contains(got, "tool_PROVIDER") {
			t.Errorf("turno %d: o turn.recorded leva conteudo da resposta: %s", i+1, got)
		}
	}
	metrics := aos505NoMetrics(t, n)
	soma := 0
	series := 0
	for _, l := range strings.Split(metrics, "\n") {
		if !strings.HasPrefix(l, "aos_model_response_shape_total{") {
			continue
		}
		series++
		if strings.HasSuffix(l, " 1") {
			soma++
		} else if !strings.HasSuffix(l, " 0") {
			t.Errorf("amostra inesperada: %s", l)
		}
	}
	if series != 300 || soma != 2 {
		t.Errorf("aos_model_response_shape_total: %d series e %d com 1; quero 300 (a cardinalidade maxima) e 2", series, soma)
	}
	for _, quer := range []string{
		`aos_model_response_shape_total{content="vazio",reasoning="reasoning_content",stop_reason="tool_calls"} 1`,
		`aos_model_response_shape_total{content="texto",reasoning="reasoning_content",stop_reason="stop"} 1`,
	} {
		if !strings.Contains(metrics, quer+"\n") {
			t.Errorf("falta a amostra %s", quer)
		}
	}
	if strings.Contains(metrics, aos490RaciocinioMarca) {
		t.Errorf("o /metrics leva conteudo da resposta")
	}
	semForma := aos505NoFamilias(metrics)
	var outras []string
	for _, f := range semForma {
		if f != "aos_model_response_shape_total counter" {
			outras = append(outras, f)
		}
	}
	if !reflect.DeepEqual(outras, aos505BaseFamiliasDoMetrics) {
		t.Errorf("fora da familia nova o /metrics mudou: %v", outras)
	}
	rep := aos505NoReplay(t, n, runID, m)
	for i, s := range rep.Steps {
		if s.Response.Shape != nil {
			t.Errorf("replay, turno %d: um turno reproduzido volta sem ficha", i+1)
		}
	}
}

// SEM CONTEÚDO, NO NÓ: uma resposta com sentinelas nos campos que o nó não lê — a recusa, o
// raciocínio noutro nome, o id da tool call, a impressão digital e uma chave desconhecida, no
// nome e no valor — não deixa nenhuma em nenhum evento do run nem no `/metrics`.
func TestAOS507_No_Observe_SemSentinelas(t *testing.T) {
	const marca, chave = "SENTINELA-507-VALOR", "sentinela_507_chave"
	n := aos507Compor(t, "observe", true)
	n.upstream.responde = func(pedeTool bool, tool string) []byte {
		msg := `{"role":"assistant","content":"feito","refusal":"` + marca + `","thinking":"` + marca + `","` + chave + `":{"` + chave + `":"` + marca + `"}}`
		finish := "stop"
		if pedeTool {
			msg = `{"role":"assistant","content":null,"reasoning":{"` + chave + `":"` + marca + `"},"thinking_blocks":[{"thinking":"` + marca + `","signature":"` + marca + `"}],` +
				`"tool_calls":[{"id":"` + marca + `","type":"function","function":{"name":"` + tool + `","arguments":"{}"}}]}`
			finish = "tool_calls"
		}
		return []byte(`{"id":"` + marca + `","object":"chat.completion","model":"gpt-4o","system_fingerprint":"` + marca + `","` + chave + `":"` + marca + `",` +
			`"choices":[{"index":0,"message":` + msg + `,"finish_reason":"` + finish + `"}],` +
			`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
	}
	m := n.correr(t, "run-507-marcas", nil)
	fichas := 0
	for _, ev := range m.eventos {
		p := string(ev.Payload)
		if strings.Contains(p, marca) || strings.Contains(p, chave) {
			t.Errorf("o evento %s leva uma sentinela: %s", ev.Type, p)
		}
		if ev.Type == agentruntime.EventTypeTurnRecorded && strings.Contains(p, `"response_shape":{"content":`) {
			fichas++
			if !strings.Contains(p, `"unknown_keys_n":`) || !strings.Contains(p, `"system_fingerprint":"sim"`) {
				t.Errorf("a ficha nao mediu a resposta: %s", p)
			}
		}
	}
	if fichas != 2 {
		t.Errorf("queria a ficha nos 2 turnos, veio em %d", fichas)
	}
	if metrics := aos505NoMetrics(t, n); strings.Contains(metrics, marca) || strings.Contains(metrics, chave) {
		t.Errorf("o /metrics leva uma sentinela")
	}
}

// O INTERRUPTOR: vocabulário fechado, omissão `off`, e um valor inválido recusa o arranque.
func TestAOS507_Env_VocabularioFechadoEBanner(t *testing.T) {
	if defaultModelResponseShape != "off" {
		t.Fatalf("a omissao tem de ser off: %q", defaultModelResponseShape)
	}
	for entra, sai := range map[string]string{"": "off", "off": "off", " off ": "off", "observe": "observe", " observe\n": "observe"} {
		t.Setenv("AOS_MODEL_RESPONSE_SHAPE", entra)
		if got, err := parseModelResponseShapeFromEnv(); err != nil || got != sai {
			t.Errorf("%q: veio %q %v, quero %q", entra, got, err, sai)
		}
	}
	for _, mau := range []string{"Observe", "on", "true", "enforce", "observe,off", "1"} {
		t.Setenv("AOS_MODEL_RESPONSE_SHAPE", mau)
		if got, err := parseModelResponseShapeFromEnv(); !errors.Is(err, ErrBadModelResponseShape) || got != "" {
			t.Errorf("%q: queria ErrBadModelResponseShape, veio %q %v", mau, got, err)
		}
		t.Setenv("AOS_MODEL_ENDPOINT", "http://127.0.0.1:1")
		t.Setenv("AOS_MODEL_NAME", aos486Modelo)
		if client, binder, err := parseModelFromEnv(false); !errors.Is(err, ErrBadModelResponseShape) || client != nil || binder != nil {
			t.Errorf("%q: o arranque tinha de recusar; veio %v", mau, err)
		}
		if got := modelResponseShapeBannerFromEnv(true); got != nil {
			t.Errorf("%q: um valor invalido nao declara linha: %v", mau, got)
		}
	}
	if got := modelResponseShapeBanner(true, modelgateway.ResponseShapeObserve); len(got) != 1 || !strings.Contains(got[0], "AOS_MODEL_RESPONSE_SHAPE=observe") {
		t.Errorf("banner de observe: %v", got)
	}
	if got := modelResponseShapeBanner(false, modelgateway.ResponseShapeObserve); got != nil {
		t.Errorf("sem gateway composto nao ha linha: %v", got)
	}
}

// OS CONTADORES: 300 séries, todas de vocabulário fechado; um trio fora dele conta como ficha
// ilegível e nunca com o texto recebido.
func TestAOS507_Contadores_VocabularioFechado(t *testing.T) {
	c := novosContadoresDaForma()
	series := c.series()
	if len(series) != 300 || len(c.total) != 300 {
		t.Fatalf("cardinalidade = %d series (%d contadores), quero 300", len(series), len(c.total))
	}
	c.observar("texto", "nenhum", agentruntime.StopStop)
	c.observar("TEXTO-DO-PROVIDER", "nenhum", agentruntime.StopStop)
	c.observar("texto", "TEXTO-DO-PROVIDER", "MOTIVO-BRUTO")
	c.observar(agentruntime.ShapeUnreadable, agentruntime.ShapeReasoningNone, agentruntime.StopUnreported)
	if len(c.total) != 300 {
		t.Fatalf("um valor fora do vocabulario criou uma serie: %d", len(c.total))
	}
	for chave, quer := range map[[3]string]int64{
		{"texto", "nenhum", "stop"}:          1,
		{"ilegivel", "nenhum", "stop"}:       1,
		{"ilegivel", "nenhum", "other"}:      1,
		{"ilegivel", "nenhum", "unreported"}: 1,
	} {
		if got := c.lido(chave); got != quer {
			t.Errorf("%v = %d, quero %d", chave, got, quer)
		}
	}
	var nulo *contadoresDaForma
	nulo.observar("texto", "nenhum", agentruntime.StopStop)
	if nulo.lido([3]string{"texto", "nenhum", "stop"}) != 0 {
		t.Errorf("contadores nil nao contam")
	}
}
