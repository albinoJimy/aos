package bancoensaio

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// AOS-516 — A QUALIFICAÇÃO DA DEVOLUÇÃO DO ESTADO OPACO, NO MODO FALSO.
//
// O nó de ensaio com um perfil candidato que devolve estado, à frente do provider falso do
// estado: o que EXIGE de volta, byte a byte, o que emitiu (o falso exigente do AOS-515), e o que
// o PROÍBE. Os números de cada cenário são exactos e estão presos aqui.

// aos516Perfil devolve um perfil candidato da rota de ensaio com a classe de estado dada.
func aos516Perfil(t *testing.T, devolver, extra string) *modelgateway.RouteProfile {
	t.Helper()
	versao := "1.3.0"
	if devolver == "nunca" {
		versao = "1.2.0"
	}
	p, err := LerPerfilCandidato([]byte(`{"requested":"rota-de-ensaio","expected_model":"anthropic/modelo-de-ensaio","wire_class":"openai-chat-completions","capabilities":["tools"],` +
		`"params":{"thinking":{"type":"enabled","budget_tokens":2048},"max_tokens":16000},"projection_version":"` + versao + `","devolver":"` + devolver + `"` + extra + `}`))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// aos516Correr corre o caso T1 (um nó de leitura com uma tool) `amostras` vezes contra o falso
// do estado dado, com o perfil dado, e devolve o relatório e a forma que o falso registou.
func aos516Correr(t *testing.T, perfil *modelgateway.RouteProfile, falso *FalsoDeEstado, host string, amostras int) (*Relatorio, *FormaNoFornecedor) {
	t.Helper()
	b := mustBateria(t)
	srv := httptest.NewServer(falso)
	t.Cleanup(srv.Close)
	no, err := NovoNoDeEnsaio(context.Background(), CfgDoNo{Bateria: b, BaseURL: srv.URL + "/v1", Credencial: "credencial-de-teste", Perfil: perfil, HostEsperado: host})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(no.Fechar)
	plano := PlanoDaBateria(b, BracoA, amostras)
	plano.Casos, plano.Semente = []string{"T1"}, 516
	r, err := Correr(context.Background(), CfgDaCorrida{
		Modo: ModoFalso, Plano: plano, Bateria: b, No: no, Relogio: relogioFixo, PerfilDigest: perfil.Digest(),
		Rota:              RotaDoRelatorio{Fornecedor: string(FornecedorFalso), Modelo: "modelo-falso-do-banco", Digest: DigestDaRota("falso", "modelo-falso-do-banco", "")},
		FormaNoFornecedor: func() (*FormaNoFornecedor, error) { return falso.Forma(), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	// Em nenhum cenário o relatório — o JSON e o resumo em texto — leva um byte do estado.
	cru, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range append(SentinelasDoEstado(), SentinelaDeTexto, "resposta final") {
		if bytes.Contains(cru, []byte(s)) || strings.Contains(ResumoEmTexto(r), s) {
			t.Fatalf("o relatorio leva conteudo do estado ou de uma resposta: %q", s)
		}
	}
	return r, r.FormaNoFornecedor
}

func aos516Contagens(t *testing.T, onde string, tem, quer map[string]int) {
	t.Helper()
	if len(tem) == 0 && len(quer) == 0 {
		return
	}
	if !reflect.DeepEqual(tem, quer) {
		t.Errorf("%s = %v, quer %v", onde, tem, quer)
	}
}

// OBRIGATÓRIO + EXIGENTE: o run faz três turnos com tools e o texto final; os três pedidos que
// levam turnos anteriores saem com o estado, e o falso — que o confere byte a byte, com o pedido
// anterior como prefixo — aceita-os todos. Com o id do runtime e com o do provider.
func TestAOS516_Falso_ObrigatorioComOExigente_Passa(t *testing.T) {
	for nome, c := range map[string]struct {
		extra   string
		exigeID bool
		id      string
	}{
		"id do runtime":  {"", false, "tool_call.id_do_runtime"},
		"id do provider": {`,"tool_call_id":"provider"`, true, "tool_call.id_do_provider"},
	} {
		t.Run(nome, func(t *testing.T) {
			perfil := aos516Perfil(t, "obrigatorio", c.extra)
			falso := &FalsoDeEstado{Turnos: 3, ExigeID: c.exigeID, ServidoComo: perfil.ExpectedModel}
			const amostras = 5
			r, forma := aos516Correr(t, perfil, falso, "", amostras)
			aos516Contagens(t, "desfechos", r.Taxas.Desfechos, map[string]int{DesfechoCumprido: amostras})
			aos516Contagens(t, "http", r.Taxas.HTTP, map[string]int{"200": 4 * amostras})
			d := r.Taxas.Devolucao
			if d == nil {
				t.Fatal("o relatorio nao tem as contagens da devolucao")
			}
			if d.TurnosComEstadoCapturado != 3*amostras || d.PedidosComTurnosAnteriores != 3*amostras || d.AceitesPeloFornecedor != 3*amostras || d.Recusas != 0 || d.ComEstado.HTTP4xx != 0 {
				t.Errorf("devolucao = %+v; quer %d turnos capturados, %d pedidos e todos devolvidos", d, 3*amostras, 3*amostras)
			}
			aos516Contagens(t, "nao devolvido por causa", d.NaoDevolvidoPorCausa, nil)
			verTaxa(t, "taxa de devolucao", d.TaxaDeDevolucao, 3*amostras, 3*amostras)
			verTaxa(t, "segundo turno aceite", r.Taxas.SegundoTurnoAceite, amostras, amostras)
			// O falso recebeu de volta 1+2+3 turnos por run, todos com o estado, e nao recusou nada.
			if forma == nil || forma.Wire != WireDeChat || forma.Turnos != 6*amostras || len(forma.Recusas) != 0 {
				t.Fatalf("forma no fornecedor = %+v", forma)
			}
			aos516Contagens(t, "estado que voltou", forma.Estado, map[string]int{
				"thinking_blocks": 6 * amostras, "reasoning_content": 6 * amostras, "tool_call.thought_signature": 6 * amostras, c.id: 6 * amostras,
			})
			aos516Contagens(t, "forma do assistant", forma.Assistant, map[string]int{
				"content=vazio chaves=content,reasoning_content,role,thinking_blocks,tool_calls": 6 * amostras,
			})
			aos516Contagens(t, "parametros", forma.Parametros, map[string]int{"thinking": 4 * amostras, "thinking.type": 4 * amostras, "thinking.budget_tokens": 4 * amostras, "max_tokens": 4 * amostras})
			// O relatorio diz o que foi ligado.
			e := r.Protocolo.Estado
			if e == nil || e.Captura != "capture" || e.GovernacaoDaRota != "observe" || e.Layout != "1.5.0" || e.Projeccao != "1.3.0" || e.Devolver != "obrigatorio" || r.Protocolo.Layout != "1.5.0" {
				t.Errorf("protocolo do relatorio = %+v", r.Protocolo)
			}
			if r.Digests.Perfil != perfil.Digest() {
				t.Errorf("o relatorio nao leva o digest do perfil")
			}
		})
	}
}

// OS CONTROLOS NEGATIVOS. Cada um falha por uma razão diferente, e o relatório distingue-as.
func TestAOS516_Falso_ControlosNegativos(t *testing.T) {
	const amostras = 3
	t.Run("perfil nunca com o exigente: o provider da 400 ao segundo pedido", func(t *testing.T) {
		perfil := aos516Perfil(t, "nunca", "")
		r, forma := aos516Correr(t, perfil, &FalsoDeEstado{Turnos: 3, ServidoComo: perfil.ExpectedModel}, "", amostras)
		aos516Contagens(t, "desfechos", r.Taxas.Desfechos, map[string]int{DesfechoErroHTTP: amostras})
		aos516Contagens(t, "http", r.Taxas.HTTP, map[string]int{"200": amostras, "400": amostras})
		verTaxa(t, "segundo turno aceite", r.Taxas.SegundoTurnoAceite, 0, amostras)
		// Um perfil que nao devolve estado nao liga nada: o relatorio e o de uma corrida de sempre.
		if r.Taxas.Devolucao != nil || r.Protocolo.Estado != nil || r.Protocolo.Layout != "1.4.0" {
			t.Errorf("com devolver em nunca o banco ligou a devolucao: %+v / %+v", r.Taxas.Devolucao, r.Protocolo)
		}
		aos516Contagens(t, "recusas do falso", forma.Recusas, map[string]int{RecusaEstadoEmFalta: amostras})
		aos516Contagens(t, "estado que voltou", forma.Estado, map[string]int{"tool_call.id_do_runtime": amostras})
	})
	t.Run("perfil obrigatorio com o provider que proibe estado: 400 num pedido que levou estado", func(t *testing.T) {
		perfil := aos516Perfil(t, "obrigatorio", "")
		r, forma := aos516Correr(t, perfil, &FalsoDeEstado{Proibe: true, Turnos: 3, ServidoComo: perfil.ExpectedModel}, "", amostras)
		aos516Contagens(t, "desfechos", r.Taxas.Desfechos, map[string]int{DesfechoErroHTTP: amostras})
		aos516Contagens(t, "http", r.Taxas.HTTP, map[string]int{"200": amostras, "400": amostras})
		d := r.Taxas.Devolucao
		if d == nil || d.TurnosComEstadoCapturado != amostras || d.AceitesPeloFornecedor != 0 || d.DecididosADevolver != amostras || d.ComEstado.HTTP4xx != amostras || d.Recusas != 0 {
			t.Errorf("devolucao = %+v; quer %d pedidos devolvidos e %d respostas 4xx neles", d, amostras, amostras)
		}
		aos516Contagens(t, "recusas do falso", forma.Recusas, map[string]int{RecusaEstadoPresente: amostras})
	})
	for nome, c := range map[string]struct {
		servido bool
		host    string
	}{
		"o provider nao declara o modelo servido":          {false, ""},
		"ha um host esperado e o endpoint nao e declarado": {true, "api.exemplo.test"},
	} {
		t.Run("perfil obrigatorio, rota nao provada — "+nome+": o pedido nao sai", func(t *testing.T) {
			perfil := aos516Perfil(t, "obrigatorio", "")
			falso := &FalsoDeEstado{Turnos: 3}
			if c.servido {
				falso.ServidoComo = perfil.ExpectedModel
			}
			r, forma := aos516Correr(t, perfil, falso, c.host, amostras)
			aos516Contagens(t, "desfechos", r.Taxas.Desfechos, map[string]int{DesfechoEstadoNaoDevolvido: amostras})
			// So o primeiro pedido de cada run chegou ao provider: o segundo nao foi enviado.
			aos516Contagens(t, "http", r.Taxas.HTTP, map[string]int{"200": amostras, "sem_resposta_http": amostras})
			d := r.Taxas.Devolucao
			if d == nil || d.Recusas != amostras || d.AceitesPeloFornecedor != 0 || d.ComEstado.HTTP4xx != 0 || d.TurnosComEstadoCapturado != amostras {
				t.Fatalf("devolucao = %+v; quer %d recusas e nenhum pedido devolvido", d, amostras)
			}
			aos516Contagens(t, "nao devolvido por causa", d.NaoDevolvidoPorCausa, map[string]int{modelgateway.StateCauseRouteUnproven: amostras})
			if forma.Pedidos != amostras || forma.Turnos != 0 {
				t.Errorf("o provider recebeu %d pedidos e %d turnos de volta; quer %d e 0", forma.Pedidos, forma.Turnos, amostras)
			}
			if r.Protocolo.Estado.EndpointComparado != (c.host != "") {
				t.Errorf("endpoint_comparado = %v", r.Protocolo.Estado.EndpointComparado)
			}
			if bytes.Contains(mustJSON(t, r), []byte("api.exemplo.test")) {
				t.Errorf("o host esperado foi para o relatorio")
			}
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	cru, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return cru
}
