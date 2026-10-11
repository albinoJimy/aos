package bancoensaio

// AOS-522 (correcção de um defeito do AOS-518) — UMA CORRIDA INTERROMPIDA NUNCA DÁ `recusado`.
//
// O defeito: o cancelamento da corrida (um Ctrl-C) podia apanhar um run DEPOIS de o pedido ao
// modelo ter tido 200. O runtime devolvia então um erro sem classe; o banco registava
// `erro_outro` com o último código HTTP a 200; a regra lia isso como uma falha do modelo
// (`nao_cumprido`) e saía `recusado` — de um modelo que cumpria tudo. Cerca de 1 em 40 corridas
// interrompidas, conforme o instante do cancelamento.
//
// Aqui o instante é FIXO: um contexto que só responde «cancelado» a quem pergunta por `Err()` — o
// canal `Done()` nunca fecha, e por isso o pedido HTTP em voo acaba sempre com o seu 200.

import (
	"context"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// ctxCanceladoSoNoErr é um contexto cujo cancelamento só se vê por Err().
type ctxCanceladoSoNoErr struct {
	context.Context
	cancelado *atomic.Bool
}

func (c ctxCanceladoSoNoErr) Err() error {
	if c.cancelado.Load() {
		return context.Canceled
	}
	return nil
}

// O cancelamento apanha o run logo a seguir a um pedido com 200 — num nó com tools (ao 1.º, 2.º,
// 3.º e 31.º pedido) e num nó sem tools (ao 29.º e ao 30.º). O veredicto é sempre `inconclusivo`,
// sem razão firme nenhuma, e o nó apanhado fica fora da amostra pela causa própria.
func TestAOS522_Interrompida_OCancelamentoDepoisDeUm200NaoEFalhaDoModelo(t *testing.T) {
	for _, em := range []int64{1, 2, 3, 29, 30, 31} {
		falso := aos518FalsoBom()
		cancelado, pedidos := &atomic.Bool{}, &atomic.Int64{}
		amb := aos518Ambiente(t, aos518PerfilSimples, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if pedidos.Add(1) == em {
				cancelado.Store(true)
			}
			falso.ServeHTTP(w, req)
		}), nil, TectoDePedidosDaQualificacao)
		r := amb.correr(t, ctxCanceladoSoNoErr{context.Background(), cancelado})
		q := aos518Veredicto(t, r)
		if r.Terminou != TerminouInterrompid || q.Veredicto != VeredictoInconclusivo || !reflect.DeepEqual(q.Razoes, []string{RazaoCorridaInterrompida, RazaoAmostraIncompleta}) {
			t.Errorf("cancelada ao pedido %d: terminou %s, veredicto %s %v; quer interrompida e inconclusivo, so com as duas razoes passageiras", em, r.Terminou, q.Veredicto, q.Razoes)
		}
		c := q.Contagens
		if n := c.interrompidos(InterrompidoPeloCancelamento); n != 1 || c.naoCumpridos() != 0 || c.semToolsNaoCumpridos() != 0 {
			t.Errorf("cancelada ao pedido %d: %d no(s) interrompido(s) pelo cancelamento (quer 1); por cumprir: %d com tools, %d sem tools", em, n, c.naoCumpridos(), c.semToolsNaoCumpridos())
		}
		// O run apanhado TEVE um 200 no último pedido: é o caso que dava `recusado`.
		apanhados := 0
		for _, o := range r.Observacoes {
			if o.cancelada() {
				apanhados++
				if len(o.HTTP) == 0 || o.HTTP[len(o.HTTP)-1] != http.StatusOK || o.Desfecho == DesfechoCumprido {
					t.Errorf("cancelada ao pedido %d: o run apanhado tem http %v e desfecho %s; o teste ja nao reproduz o defeito", em, o.HTTP, o.Desfecho)
				}
			}
		}
		if apanhados != 1 || pedidos.Load() != em {
			t.Errorf("cancelada ao pedido %d: %d run(s) apanhado(s), %d pedidos", em, apanhados, pedidos.Load())
		}
	}
}

// A DEFESA EM PROFUNDIDADE, na regra: um run apanhado pelo cancelamento não alimenta limiar
// nenhum, tenha ele o aspecto que tiver — resposta vazia, 4xx num segundo turno, modelo trocado,
// primeira tentativa sem tool call. E o CONTROLO: os mesmos runs sem a marca dão `recusado`.
func TestAOS522_Interrompida_UmRunCanceladoNaoAlimentaRazaoFirme(t *testing.T) {
	runs := func(cancelados bool) []Observacao {
		m := func(mm MedidasDoRun) *MedidasDoRun { mm.CorridaCancelada = cancelados; return &mm }
		return []Observacao{
			// Com tools: resposta vazia no fim, sem tool call à primeira, com o modelo trocado.
			{Caso: "T1", No: "a", Braco: "A", Amostra: 1, Tentativa: 1, ExigeTool: true, Turnos: 1, HTTP: []int{200},
				Desfecho: string(agentruntime.OutcomeEmptyOutput), Qualificacao: m(MedidasDoRun{TurnosComModeloDiferente: 1})},
			// Com tools: 4xx de parâmetro num pedido com mensagem tool.
			{Caso: "T1", No: "b", Braco: "A", Amostra: 1, Tentativa: 1, ExigeTool: true, Turnos: 1, ToolCalls: 1, HTTP: []int{200, 400}, TiposDeErro: []string{TipoOutro},
				Desfecho: DesfechoErroHTTP, Qualificacao: m(MedidasDoRun{PedidosComMensagemTool: 1, QuatroXXComMensagemTool: 1, TurnosComModeloIgual: 1})},
			// Sem tools: o caso medido — `erro_outro` depois de um 200, e um turno sem comparação.
			{Caso: "T1", No: "c", Braco: "A", Amostra: 1, Tentativa: 1, Turnos: 1, HTTP: []int{200},
				Desfecho: DesfechoErroOutro, Qualificacao: m(MedidasDoRun{})},
		}
	}
	c := contarParaQualificacao(TerminouInterrompid, 40, 16, runs(true))
	v := qualificarModelo(c)
	if v.Veredicto != VeredictoInconclusivo || !reflect.DeepEqual(v.Razoes, []string{RazaoCorridaInterrompida, RazaoAmostraIncompleta}) {
		t.Errorf("runs cancelados: %s %v", v.Veredicto, v.Razoes)
	}
	if c.interrompidos(InterrompidoPeloCancelamento) != 3 || c.NosComToolsNaAmostra != 0 || c.NosSemToolsNaAmostra != 0 || c.RespostasVaziasNoFim != 0 ||
		c.SemToolCallAPrimeira != 0 || c.QuatroXXComMensagemTool != 0 || c.modeloTrocado() != 0 || c.Turnos != 0 || c.PedidosComResposta != 0 {
		t.Errorf("um run cancelado alimentou uma contagem de limiar: %+v", c)
	}
	// O controlo: sem a marca, as mesmas observações são falhas do modelo — todas as razões firmes.
	if v := qualificarModelo(contarParaQualificacao(TerminouInterrompid, 40, 16, runs(false))); v.Veredicto != VeredictoRecusado ||
		!contem(v.Razoes, RazaoModeloNaoCumpridos) || !contem(v.Razoes, RazaoModelo4xxNoSegundo) || !contem(v.Razoes, RazaoModeloRespostasVazias) ||
		!contem(v.Razoes, RazaoModeloServidoDiferente) || !contem(v.Razoes, RazaoModeloServidoNaoReportado) || !contem(v.Razoes, RazaoSemToolsNaoCumpridos) {
		t.Errorf("o controlo sem a marca tinha de dar recusado com as razoes firmes: %s %v", v.Veredicto, v.Razoes)
	}
	// Uma falha FIRME de um run que fechou ANTES da interrupção continua a valer: recusado.
	antes := append([]Observacao{{Caso: "T2", No: "a", Braco: "A", Amostra: 1, Tentativa: 1, ExigeTool: true, Turnos: 1, HTTP: []int{200},
		Desfecho: string(agentruntime.OutcomeContractNoCall), Qualificacao: &MedidasDoRun{TurnosComModeloIgual: 1}}}, runs(true)...)
	if v := qualificarModelo(contarParaQualificacao(TerminouInterrompid, 40, 16, antes)); v.Veredicto != VeredictoRecusado || v.Razoes[0] != RazaoModeloNaoCumpridos {
		t.Errorf("uma falha firme anterior a interrupcao tinha de valer: %s %v", v.Veredicto, v.Razoes)
	}
	// Um run que FECHOU cumprido não deixa de o ser por o contexto estar cancelado quando acabou.
	cumprido := Observacao{Caso: "T3", No: "a", ExigeTool: true, Desfecho: DesfechoCumprido, Qualificacao: &MedidasDoRun{CorridaCancelada: true}}
	if classe, _ := estadoFinalDoNo(cumprido); classe != noCumprido {
		t.Errorf("um run cumprido ficou %s", classe)
	}
	// O cancelamento apanhou o ÚLTIMO passo: a corrida deu a volta toda («completa») e mesmo assim
	// não está completa — `inconclusivo`, com a razão da interrupção.
	if v := qualificarModelo(contarParaQualificacao(TerminouCompleta, 40, 16, runs(true))); v.Veredicto != VeredictoInconclusivo || !contem(v.Razoes, RazaoCorridaInterrompida) {
		t.Errorf("cancelada no ultimo passo: %s %v", v.Veredicto, v.Razoes)
	}
}
