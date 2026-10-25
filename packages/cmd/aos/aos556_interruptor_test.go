package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// O INTERRUPTOR DA 1.8.0, NAS DUAS VIAS (AOS-556, ADR-044 §2.9; decisão do dono de 2026-10-25).
//
// O precedente do repositório era duplo e contraditório: a `AOS_MEDIA_NO_TURNO` move TODOS os runs
// novos, e o `requires_first_turn_trusted` é um pedido do wire que o nó recusa com 400. A 1.8.0
// leva as duas, com papéis distintos — a env HABILITA e é o recuo do operador, o campo ESCOLHE por
// run. O que estes testes fixam é precisamente a distinção: a env não move nada.

func TestAOS556_EnvDoInterruptor(t *testing.T) {
	for _, c := range []struct {
		valor  string
		ligado bool
		recusa bool
	}{
		{"", false, false},
		{"off", false, false},
		{"on", true, false},
		// Vocabulário FECHADO, como as irmãs: um valor mal escrito não pode cair em silêncio para
		// nenhum dos lados — nem para `off` (o operador ficava convencido de que o nó habilita)
		// nem para `on`.
		{"ON", false, true},
		{"true", false, true},
		{"1", false, true},
		{"sim", false, true},
	} {
		t.Setenv("AOS_CONTEUDO_POR_HANDLE", c.valor)
		ligado, err := parseConteudoPorHandleFromEnv()
		if c.recusa {
			if err == nil {
				t.Errorf("%q: devia recusar o arranque", c.valor)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.valor, err)
		}
		if ligado != c.ligado {
			t.Errorf("%q: ligado=%v, quero %v", c.valor, ligado, c.ligado)
		}
	}
}

// A LINHA DE POSTURA, e o que ela TEM de dizer. Com `off` não sai linha nenhuma — o arranque é o de
// antes, byte a byte. Com `on` tem de dizer as três coisas que um operador precisa de saber e que
// não são óbvias: que habilitar não move nada, que a política de fluxo ainda não está em vigor
// (logo não há ganho de produto), e qual é o recuo.
func TestAOS556_BannerDoInterruptor(t *testing.T) {
	if b := conteudoPorHandleBanner(false); b != nil {
		t.Fatalf("com off nao sai linha: %v", b)
	}
	b := conteudoPorHandleBanner(true)
	if len(b) != 1 {
		t.Fatalf("linhas = %d", len(b))
	}
	for _, q := range []string{
		"AOS_CONTEUDO_POR_HANDLE=on",
		agentruntime.AssemblyVersion180,
		"NAO MOVE RUN NENHUM",
		"400",
		"NAO esta em vigor",
		"Recuo: off",
	} {
		if !strings.Contains(b[0], q) {
			t.Errorf("a linha de postura nao diz %q: %s", q, b[0])
		}
	}
}

// O LAYOUT: a 1.8.0 só a PEDIDO, e tem PRECEDÊNCIA — ela deriva da 1.7.0 e herda-a, pelo que um
// run que a peça não precisa de pedir também a media.
func TestAOS556_LayoutSoAPedidoEComPrecedencia(t *testing.T) {
	comInput := agentruntime.Goal{Inputs: []agentruntime.PlanInput{{From: "n0"}}}
	pedido := agentruntime.Goal{ConteudoPorHandle: true}
	pedidoComInput := agentruntime.Goal{ConteudoPorHandle: true, Inputs: comInput.Inputs}

	for nome, c := range map[string]struct {
		captura, pedirAntes, media bool
		goal                       agentruntime.Goal
		quero                      string
	}{
		"pedida, nada mais ligado":        {false, false, false, pedido, agentruntime.AssemblyVersion180},
		"pedida, com media ligada":        {false, false, true, pedido, agentruntime.AssemblyVersion180},
		"pedida, com pedir-antes e input": {false, true, false, pedidoComInput, agentruntime.AssemblyVersion180},
		"pedida, com captura de estado":   {true, false, false, pedido, agentruntime.AssemblyVersion180},
		// E O CONTROLO QUE IMPORTA: sem o pedido, nada muda. É isto que distingue este interruptor
		// da `AOS_MEDIA_NO_TURNO`, que move todos os runs novos.
		"nao pedida, media ligada": {false, false, true, comInput, agentruntime.AssemblyVersion170},
		"nao pedida, pedir-antes":  {false, true, false, comInput, agentruntime.AssemblyVersion160},
		"nao pedida, captura":      {true, false, false, comInput, agentruntime.AssemblyVersion150},
		"nao pedida, nada":         {false, false, false, comInput, agentruntime.AssemblyVersion},
	} {
		if got := layoutDoRunNovo(c.captura, c.pedirAntes, c.media, c.goal); got != c.quero {
			t.Errorf("%s: layout=%s, quero %s", nome, got, c.quero)
		}
	}
}

// A RECUSA 400 NUM NÓ QUE NÃO HABILITA, e é o fail-closed que importa: aceitar o campo e ignorá-lo
// daria a quem submete a ideia de que o conteúdo viaja por handle quando viaja INLINE — a afirmação
// exactamente ao contrário. É o molde do `requires_first_turn_trusted` (AOS-542).
//
// E PROVA A HABILITAÇÃO EFECTIVA: este nó é composto com `cfg.ConteudoPorHandle = true` e SEM o
// produtor do `tool_result` (sem espaço de handles nem execução durável), pelo que a habilitação
// resolve FALSE e a submissão é recusada. Com a env `on` e sem produtor o layout seria alcançável
// mas inútil — o tail materializaria um erro de tool em vez do conteúdo e o piso cairia à primeira
// call.
func TestAOS556_SubmissaoRecusadaSemHabilitacao(t *testing.T) {
	n := aos486ComporCom(t, "native", func(cfg *Config) {
		cfg.ConteudoPorHandle = true // pedida pelo operador…
		cfg.Privileged = referencemonitor.NewStaticPrivilegedSet("cap:http.post", durCap)
	})
	if n.node.conteudoPorHandle {
		t.Fatal("sem o produtor do tool_result composto o no NAO pode habilitar o layout por handle")
	}
	soma := sha256.Sum256([]byte(aos540Documento))
	rec := postJSON(n.h, "POST", "/runs", map[string]any{
		"run_id": "run-556-sem-hab", "objective": aos540Objectivo,
		"principal_nhi": durAgent, "credential": n.tok, "tools": []string{"arquivo"},
		"inputs": []map[string]string{{"from": "n0", "output": "document_content",
			"digest": "sha256:" + hex.EncodeToString(soma[:]), "content": aos540Documento}},
		"max_turns": 4, "conteudo_por_handle": true,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /runs: %d %s, quero 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "conteudo_por_handle") {
		t.Fatalf("a recusa tem de ser a do campo, nao outra: %s", rec.Body.String())
	}
	// NÃO-VACUIDADE: o MESMO pedido sem o campo é aceite. Sem isto, a recusa acima passaria num nó
	// que recusasse tudo por outra razão (credencial, tools, inputs).
	rec2 := postJSON(n.h, "POST", "/runs", map[string]any{
		"run_id": "run-556-sem-campo", "objective": aos540Objectivo,
		"principal_nhi": durAgent, "credential": n.tok, "tools": []string{"arquivo"},
		"inputs": []map[string]string{{"from": "n0", "output": "document_content",
			"digest": "sha256:" + hex.EncodeToString(soma[:]), "content": aos540Documento}},
		"max_turns": 4,
	})
	if rec2.Code != http.StatusCreated {
		t.Fatalf("o mesmo pedido SEM o campo: %d %s, quero 201", rec2.Code, rec2.Body.String())
	}
}
