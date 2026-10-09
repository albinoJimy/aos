package bancoensaio

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// AOS-516 — A REVISÃO ADVERSARIAL DO BANCO: O VERDE FALSO.
//
// O banco é o instrumento que qualifica um modelo. A primeira versão contava como «devolvido» a
// DECISÃO do gateway, tomada antes de o pedido sair, e como «estado capturado» um envelope só
// com ids de tool call. Estes testes prendem o contrário: só conta o que o fornecedor ACEITOU, só
// se mede sobre turnos que trouxeram raciocínio, e o veredicto é calculado pelo banco.

// aos516CorrerContra corre o caso T1 `amostras` vezes contra o handler dado.
func aos516CorrerContra(t *testing.T, perfil *modelgateway.RouteProfile, h http.Handler, amostras int) *Relatorio {
	t.Helper()
	b := mustBateria(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	no, err := NovoNoDeEnsaio(context.Background(), CfgDoNo{Bateria: b, BaseURL: srv.URL + "/v1", Credencial: "credencial-de-teste", Perfil: perfil})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(no.Fechar)
	plano := PlanoDaBateria(b, BracoA, amostras)
	plano.Casos, plano.Semente = []string{"T1"}, 516
	r, err := Correr(context.Background(), CfgDaCorrida{
		Modo: ModoFalso, Plano: plano, Bateria: b, No: no, Relogio: relogioFixo, PerfilDigest: perfil.Digest(),
		Rota: RotaDoRelatorio{Fornecedor: string(FornecedorFalso), Modelo: "modelo-falso-do-banco", Digest: DigestDaRota("falso", "modelo-falso-do-banco", "")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func aos516Veredicto(t *testing.T, r *Relatorio, veredicto string, razoes ...string) {
	t.Helper()
	q := r.Qualificacao
	if q == nil {
		t.Fatal("o relatorio nao traz a qualificacao da devolucao")
	}
	if razoes == nil {
		razoes = []string{}
	}
	if q.Veredicto != veredicto || !reflect.DeepEqual(q.Razoes, razoes) {
		t.Errorf("qualificacao = %s %v; quer %s %v\ndevolucao = %+v", q.Veredicto, q.Razoes, veredicto, razoes, r.Taxas.Devolucao)
	}
}

// O CENÁRIO FELIZ dá `cumprida`, sem razões.
func TestAOS516_Revisao_OExigenteDaCumprida(t *testing.T) {
	perfil := aos516Perfil(t, "obrigatorio", "")
	r, _ := aos516Correr(t, perfil, &FalsoDeEstado{Turnos: 3, ServidoComo: perfil.ExpectedModel}, "", 3)
	aos516Veredicto(t, r, QualificacaoCumprida)
	d := r.Taxas.Devolucao
	if d.TurnosComRaciocinioCapturado != 9 || d.TurnosSoComIDs != 0 || d.PedidosQueDeviamLevarRaciocinio != 9 || d.DecididosADevolver != 9 {
		t.Errorf("devolucao = %+v", d)
	}
	want := PedidosComEstado{Pedidos: 9, HTTP2xx: 9}
	if d.ComEstado != want {
		t.Errorf("pedidos com estado = %+v, quer %+v", d.ComEstado, want)
	}
}

// A1 — O PEDIDO QUE LEVAVA O ESTADO FALHA DEPOIS DA DECISÃO. O gateway decidiu devolver; o
// fornecedor nunca aceitou. Nada conta como devolvido, a taxa é 0, e cada falha tem o seu
// contador — o 429 e o 5xx não se confundem com «o fornecedor recusou o estado».
func TestAOS516_Revisao_A1_SoContaOQueOFornecedorAceitou(t *testing.T) {
	const amostras = 3
	for nome, c := range map[string]struct {
		falhar    func(w http.ResponseWriter)
		quer      PedidosComEstado
		veredicto string
		razoes    []string
	}{
		"500": {func(w http.ResponseWriter) { w.WriteHeader(500); _, _ = w.Write([]byte(`{"error":{"message":"x"}}`)) },
			PedidosComEstado{Pedidos: amostras, HTTP5xx: amostras}, QualificacaoInconclusiva,
			[]string{RazaoPedidosNaoAceites, RazaoHTTP5xxComEstado, RazaoRunsInterrompidos}},
		"429": {func(w http.ResponseWriter) { w.WriteHeader(429); _, _ = w.Write([]byte(`{"error":{"message":"x"}}`)) },
			PedidosComEstado{Pedidos: amostras, HTTP429: amostras}, QualificacaoInconclusiva,
			[]string{RazaoPedidosNaoAceites, RazaoHTTP429ComEstado, RazaoRunsInterrompidos}},
		"404": {func(w http.ResponseWriter) { w.WriteHeader(404); _, _ = w.Write([]byte(`{"error":{"message":"x"}}`)) },
			PedidosComEstado{Pedidos: amostras, HTTP4xx: amostras}, QualificacaoNaoCumprida,
			[]string{RazaoHTTP4xxComEstado, RazaoRunsParadosPor4xx, RazaoPedidosNaoAceites}},
		"ligacao fechada": {func(w http.ResponseWriter) {
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
				}
			}
		}, PedidosComEstado{Pedidos: amostras, ErroDeTransporte: amostras}, QualificacaoInconclusiva,
			[]string{RazaoPedidosNaoAceites, RazaoTransporteComEstado, RazaoRunsInterrompidos}},
	} {
		t.Run(nome, func(t *testing.T) {
			perfil := aos516Perfil(t, "obrigatorio", "")
			falso := &FalsoDeEstado{Turnos: 3, ServidoComo: perfil.ExpectedModel}
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				corpo, _ := io.ReadAll(r.Body)
				if bytes.Contains(corpo, []byte(`"role":"assistant"`)) {
					c.falhar(w)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(corpo))
				falso.ServeHTTP(w, r)
			})
			r := aos516CorrerContra(t, perfil, h, amostras)
			d := r.Taxas.Devolucao
			if d == nil {
				t.Fatal("sem contagens da devolucao")
			}
			// O falso nunca recebeu um turno de volta.
			if falso.Forma().Turnos != 0 {
				t.Fatalf("o falso recebeu %d turnos de volta", falso.Forma().Turnos)
			}
			if d.DecididosADevolver != amostras || d.AceitesPeloFornecedor != 0 || d.PedidosQueDeviamLevarRaciocinio != amostras {
				t.Errorf("decididos %d, aceites %d, deviam %d; quer %d, 0, %d", d.DecididosADevolver, d.AceitesPeloFornecedor, d.PedidosQueDeviamLevarRaciocinio, amostras, amostras)
			}
			verTaxa(t, "taxa de devolucao", d.TaxaDeDevolucao, 0, amostras)
			if d.ComEstado != c.quer {
				t.Errorf("pedidos com estado = %+v, quer %+v", d.ComEstado, c.quer)
			}
			aos516Veredicto(t, r, c.veredicto, c.razoes...)
		})
	}
}

// semRaciocinio é um provider de chat que responde só com tool calls — sem `reasoning_content`,
// sem `thinking_blocks`, sem assinaturas — duas vezes, e depois em texto. Declara-se servido pelo
// modelo dado.
type semRaciocinio struct{ servido string }

func (s semRaciocinio) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	corpo, _ := io.ReadAll(r.Body)
	var pedido pedidoAoFalso
	_ = json.Unmarshal(corpo, &pedido)
	w.Header().Set("Content-Type", "application/json")
	if s.servido != "" {
		w.Header().Set("x-litellm-model-name", s.servido)
	}
	feitos := strings.Count(string(corpo), `"role":"assistant"`)
	if len(pedido.Tools) == 0 || feitos >= 2 {
		_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"lido."},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		return
	}
	tool, args := chamadaDoFalso(corpo, pedido.Tools[0].Function.Name, pedido.Tools[0].Function.Parameters.Properties)
	argsJSON, _ := json.Marshal(args)
	_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_sem_raciocinio_` +
		string(rune('a'+feitos)) + `","type":"function","function":{"name":"` + tool + `","arguments":` + string(argsJSON) + `}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
}

// A2 — O FORNECEDOR NÃO MANDA RACIOCÍNIO NENHUM. Os runs cumprem e o gateway «devolve» os
// envelopes só com ids; mas não havia nada a provar, e o veredicto é `sem_raciocinio` — nunca
// uma taxa a 100%.
func TestAOS516_Revisao_A2_SemRaciocinioNaoQualifica(t *testing.T) {
	const amostras = 3
	for _, classe := range []string{"obrigatorio", "opcional"} {
		t.Run(classe, func(t *testing.T) {
			perfil := aos516Perfil(t, classe, "")
			r := aos516CorrerContra(t, perfil, semRaciocinio{servido: perfil.ExpectedModel}, amostras)
			aos516Contagens(t, "desfechos", r.Taxas.Desfechos, map[string]int{DesfechoCumprido: amostras})
			d := r.Taxas.Devolucao
			if d == nil {
				t.Fatal("sem contagens da devolucao")
			}
			if d.TurnosComRaciocinioCapturado != 0 || d.TurnosSoComIDs != 2*amostras {
				t.Errorf("turnos com raciocinio %d, so com ids %d; quer 0 e %d", d.TurnosComRaciocinioCapturado, d.TurnosSoComIDs, 2*amostras)
			}
			if d.PedidosQueDeviamLevarRaciocinio != 0 || d.AceitesPeloFornecedor != 0 {
				t.Errorf("deviam %d, aceites %d; quer 0 e 0", d.PedidosQueDeviamLevarRaciocinio, d.AceitesPeloFornecedor)
			}
			verTaxa(t, "taxa de devolucao", d.TaxaDeDevolucao, 0, 0)
			aos516Veredicto(t, r, QualificacaoSemRaciocinio, RazaoSemTurnosComRaciocinio)
		})
	}
}

// OS CONTROLOS NEGATIVOS têm veredicto `nao_cumprida`, cada um com a sua razão.
func TestAOS516_Revisao_ControlosNegativosNaoCumprem(t *testing.T) {
	t.Run("o provider proibe o estado", func(t *testing.T) {
		perfil := aos516Perfil(t, "obrigatorio", "")
		r, _ := aos516Correr(t, perfil, &FalsoDeEstado{Proibe: true, Turnos: 3, ServidoComo: perfil.ExpectedModel}, "", 3)
		aos516Veredicto(t, r, QualificacaoNaoCumprida, RazaoHTTP4xxComEstado, RazaoRunsParadosPor4xx, RazaoPedidosNaoAceites)
		verTaxa(t, "taxa de devolucao", r.Taxas.Devolucao.TaxaDeDevolucao, 0, 3)
	})
	t.Run("rota nao provada: o gateway recusa", func(t *testing.T) {
		perfil := aos516Perfil(t, "obrigatorio", "")
		r, _ := aos516Correr(t, perfil, &FalsoDeEstado{Turnos: 3}, "", 3)
		aos516Veredicto(t, r, QualificacaoNaoCumprida, RazaoRecusas, RazaoRunsNaoCumpridos, RazaoPedidosNaoAceites)
		// O denominador conta os pedidos recusados: a taxa não fica a 100% com recusas.
		verTaxa(t, "taxa de devolucao", r.Taxas.Devolucao.TaxaDeDevolucao, 0, 3)
	})
}

// A CLASSE `opcional` com a rota não provada: o pedido SEGUE sem o estado (decisão `sem_estado`),
// não leva estado — por isso o 400 do exigente NÃO conta como «4xx num pedido com estado» — e a
// devolução não se cumpre.
func TestAOS516_Revisao_Opcional_SegueSemEstadoENaoCumpre(t *testing.T) {
	const amostras = 3
	perfil := aos516Perfil(t, "opcional", "")
	r, forma := aos516Correr(t, perfil, &FalsoDeEstado{Turnos: 3}, "", amostras)
	d := r.Taxas.Devolucao
	if d == nil {
		t.Fatal("sem contagens da devolucao")
	}
	aos516Contagens(t, "decisoes", d.PedidosPorDecisao, map[string]int{modelgateway.StateReturnNone: amostras})
	aos516Contagens(t, "causas", d.NaoDevolvidoPorCausa, map[string]int{modelgateway.StateCauseRouteUnproven: amostras})
	if (d.ComEstado != PedidosComEstado{}) {
		t.Errorf("pedidos com estado = %+v; nenhum pedido levou estado", d.ComEstado)
	}
	if d.Recusas != 0 || d.AceitesPeloFornecedor != 0 || d.PedidosQueDeviamLevarRaciocinio != amostras {
		t.Errorf("devolucao = %+v", d)
	}
	aos516Contagens(t, "recusas do falso", forma.Recusas, map[string]int{RecusaEstadoEmFalta: amostras})
	if q := r.Qualificacao; q == nil || q.Veredicto != QualificacaoNaoCumprida {
		t.Errorf("qualificacao = %+v, quer nao_cumprida", q)
	}
}

// AS CONTAGENS POR PEDIDO, sem servidor: cada ramo de [EstadoDoRun.contarPedido].
func TestAOS516_Revisao_ContarPedido(t *testing.T) {
	for nome, c := range map[string]struct {
		chamada chamadaObservada
		havia   bool
		quer    EstadoDoRun
	}{
		"sem turnos anteriores nao conta nada": {chamadaObservada{tentativas: []int{400}}, true, EstadoDoRun{}},
		"parcial levou estado, mas nao e aceite": {
			chamadaObservada{devolucao: modelgateway.StateReturnPartial, causaDaDevolucao: modelgateway.StateCauseOtherRoute, tentativas: []int{200}}, true,
			EstadoDoRun{Decisoes: map[string]int{modelgateway.StateReturnPartial: 1}, Causas: map[string]int{modelgateway.StateCauseOtherRoute: 1},
				DeviamLevarRaciocinio: 1, ComEstado: PedidosComEstado{Pedidos: 1, HTTP2xx: 1}}},
		"4xx sem estado no pedido nao conta como 4xx com estado": {
			chamadaObservada{devolucao: modelgateway.StateReturnNone, tentativas: []int{400}}, true,
			EstadoDoRun{Decisoes: map[string]int{modelgateway.StateReturnNone: 1}, DeviamLevarRaciocinio: 1}},
		"5xx nao e 4xx; a repeticao que passa e aceite": {
			chamadaObservada{devolucao: modelgateway.StateReturnAll, tentativas: []int{503, 200}}, true,
			EstadoDoRun{Decisoes: map[string]int{modelgateway.StateReturnAll: 1}, DeviamLevarRaciocinio: 1, Aceites: 1,
				ComEstado: PedidosComEstado{Pedidos: 1, HTTP5xx: 1, HTTP2xx: 1}}},
		"o tecto nao deixou sair": {
			chamadaObservada{devolucao: modelgateway.StateReturnAll, naoEnviados: 1}, true,
			EstadoDoRun{Decisoes: map[string]int{modelgateway.StateReturnAll: 1}, DeviamLevarRaciocinio: 1, ComEstado: PedidosComEstado{Pedidos: 1, NaoEnviado: 1}}},
		"armado e nem chegou ao transporte": {
			chamadaObservada{devolucao: modelgateway.StateReturnAll}, true,
			EstadoDoRun{Decisoes: map[string]int{modelgateway.StateReturnAll: 1}, DeviamLevarRaciocinio: 1, ComEstado: PedidosComEstado{Pedidos: 1, NaoEnviado: 1}}},
		"aceite sem raciocinio anterior nao e numerador": {
			chamadaObservada{devolucao: modelgateway.StateReturnAll, tentativas: []int{200}}, false,
			EstadoDoRun{Decisoes: map[string]int{modelgateway.StateReturnAll: 1}, ComEstado: PedidosComEstado{Pedidos: 1, HTTP2xx: 1}}},
		"recusado conta no denominador": {
			chamadaObservada{devolucao: modelgateway.StateReturnRefused, causaDaDevolucao: modelgateway.StateCauseRouteUnproven}, true,
			EstadoDoRun{Decisoes: map[string]int{modelgateway.StateReturnRefused: 1}, Causas: map[string]int{modelgateway.StateCauseRouteUnproven: 1}, DeviamLevarRaciocinio: 1}},
	} {
		t.Run(nome, func(t *testing.T) {
			var e EstadoDoRun
			e.contarPedido(c.chamada, c.havia)
			if !reflect.DeepEqual(e, c.quer) {
				t.Errorf("estado = %+v\nquer   %+v", e, c.quer)
			}
		})
	}
}
