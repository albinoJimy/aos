package bancoensaio

import (
	"testing"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// AOS-516 — A QUALIFICAÇÃO DA DEVOLUÇÃO NA FORMA DA OPENROUTER, NO MODO FALSO.
//
// O nó de ensaio, com um perfil candidato de uma rota `openrouter/…`, à frente do provider falso
// do estado na forma da OpenRouter: `reasoning` (texto) e `reasoning_details` (blocos, com
// assinatura) na resposta, exigidos de volta no topo da mensagem `assistant` de cada turno.
// Sem proxy: os bytes que o gateway devolve são os que recebeu.

// aos516PerfilDaOpenRouter lê um perfil candidato da rota `openrouter/…` de ensaio.
func aos516PerfilDaOpenRouter(t *testing.T, params, devolver string) *modelgateway.RouteProfile {
	t.Helper()
	p, err := LerPerfilCandidato([]byte(aos516PerfilOpenRouter("openrouter/autor-de-ensaio/modelo-de-ensaio", params, devolver)))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// OBRIGATÓRIO + EXIGENTE ⇒ `cumprida`. Os `reasoning_details` de cada turno voltam no topo da
// mensagem, com os mesmos valores E os mesmos bytes; o texto de `reasoning` volta também.
func TestAOS516_Falso_OpenRouter_ObrigatorioCumpre(t *testing.T) {
	for nome, c := range map[string]struct {
		params string
		quer   map[string]int
	}{
		"thinking":         {aos516ParamsThinking, map[string]int{"thinking": 4, "thinking.type": 4, "thinking.budget_tokens": 4, "max_tokens": 4}},
		"reasoning_effort": {aos516ParamsEffort, map[string]int{"reasoning_effort": 4, "max_tokens": 4}},
	} {
		t.Run(nome, func(t *testing.T) {
			const amostras = 3
			perfil := aos516PerfilDaOpenRouter(t, c.params, "obrigatorio")
			falso := &FalsoDeEstado{Turnos: 3, FormaDoEstado: FormaOpenRouter, ServidoComo: perfil.ExpectedModel}
			r, forma := aos516Correr(t, perfil, falso, "", amostras)
			aos516Veredicto(t, r, QualificacaoCumprida)
			aos516Contagens(t, "desfechos", r.Taxas.Desfechos, map[string]int{DesfechoCumprido: amostras})
			aos516Contagens(t, "http", r.Taxas.HTTP, map[string]int{"200": 4 * amostras})
			d := r.Taxas.Devolucao
			if d == nil || d.TurnosComRaciocinioCapturado != 3*amostras || d.PedidosQueDeviamLevarRaciocinio != 3*amostras || d.AceitesPeloFornecedor != 3*amostras || d.Recusas != 0 {
				t.Fatalf("devolucao = %+v", d)
			}
			if forma == nil || forma.Wire != WireDeChat || forma.Turnos != 6*amostras || len(forma.Recusas) != 0 {
				t.Fatalf("forma no fornecedor = %+v", forma)
			}
			aos516Contagens(t, "estado que voltou", forma.Estado, map[string]int{
				"reasoning_details.intacto": 6 * amostras, "reasoning_details.bytes_iguais": 6 * amostras,
				"reasoning.presente": 6 * amostras, "tool_call.id_do_runtime": 6 * amostras,
			})
			aos516Contagens(t, "forma do assistant", forma.Assistant, map[string]int{
				"content=vazio chaves=content,reasoning,reasoning_details,role,tool_calls": 6 * amostras,
			})
			quer := map[string]int{}
			for k, v := range c.quer {
				quer[k] = v * amostras
			}
			aos516Contagens(t, "parametros", forma.Parametros, quer)
		})
	}
}

// OS CONTROLOS NEGATIVOS da forma da OpenRouter.
func TestAOS516_Falso_OpenRouter_ControlosNegativos(t *testing.T) {
	const amostras = 3
	t.Run("perfil nunca com o exigente: o falso recusa por estado em falta (o veredicto esta no teste da linha de comandos)", func(t *testing.T) {
		perfil := aos516PerfilDaOpenRouter(t, aos516ParamsThinking, "nunca")
		r, forma := aos516Correr(t, perfil, &FalsoDeEstado{Turnos: 3, FormaDoEstado: FormaOpenRouter, ServidoComo: perfil.ExpectedModel}, "", amostras)
		aos516Contagens(t, "http", r.Taxas.HTTP, map[string]int{"200": amostras, "400": amostras})
		aos516Contagens(t, "recusas do falso", forma.Recusas, map[string]int{RecusaEstadoEmFalta: amostras})
		aos516Contagens(t, "estado que voltou", forma.Estado, map[string]int{"reasoning_details.em_falta": amostras, "tool_call.id_do_runtime": amostras})
	})
	t.Run("perfil obrigatorio com o provider que proibe estado: nao_cumprida", func(t *testing.T) {
		perfil := aos516PerfilDaOpenRouter(t, aos516ParamsThinking, "obrigatorio")
		r, forma := aos516Correr(t, perfil, &FalsoDeEstado{Turnos: 3, Proibe: true, FormaDoEstado: FormaOpenRouter, ServidoComo: perfil.ExpectedModel}, "", amostras)
		if q := r.Qualificacao; q == nil || q.Veredicto != QualificacaoNaoCumprida {
			t.Errorf("qualificacao = %+v, quer %s", q, QualificacaoNaoCumprida)
		}
		aos516Contagens(t, "recusas do falso", forma.Recusas, map[string]int{RecusaEstadoPresente: amostras})
	})
	t.Run("o modelo servido nao e o do perfil: o estado nao sai e a devolucao nao se cumpre", func(t *testing.T) {
		perfil := aos516PerfilDaOpenRouter(t, aos516ParamsThinking, "obrigatorio")
		r, forma := aos516Correr(t, perfil, &FalsoDeEstado{Turnos: 3, FormaDoEstado: FormaOpenRouter, ServidoComo: "openrouter/outro-autor/outro-modelo"}, "", amostras)
		if q := r.Qualificacao; q == nil || q.Veredicto == QualificacaoCumprida {
			t.Errorf("qualificacao = %+v: com outra rota servida nao pode ser cumprida", q)
		}
		if forma.Estado["reasoning_details.intacto"] != 0 {
			t.Errorf("o estado saiu para uma rota que nao se provou igual: %v", forma.Estado)
		}
	})
}

// A forma da OpenRouter que o falso emite passa pelas listas FECHADAS do gateway: a sonda do
// estado guarda `reasoning` e `reasoning_details` de `message`, com os bytes recebidos.
func TestAOS516_Falso_OpenRouter_AFormaCabeNasListasDoGateway(t *testing.T) {
	esperado := detalhesDoTurno(0)
	if !mesmosValores(esperado, []byte(`[{"index":0,"format":"anthropic-claude-v1","id":"reasoning-text-0","signature":"`+assinaturaDoTurno(0)+`","text":"S-PENSA-0 <b> & e","type":"reasoning.text"},`+
		` {"type":"reasoning.encrypted","data":"`+redigidoDoTurno(0)+`","id":"reasoning-encrypted-0","format":"anthropic-claude-v1","index":1}]`)) {
		t.Errorf("os detalhes do turno 0 mudaram de forma: %s", esperado)
	}
	if mesmosValores(esperado, detalhesDoTurno(1)) || mesmosValores(esperado, []byte(`[]`)) {
		t.Error("a comparacao por valores aceita o que nao e igual")
	}
}
