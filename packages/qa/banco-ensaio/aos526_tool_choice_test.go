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

// AOS-526 — A REGRA `params.tool_choice` NO BANCO DE ENSAIO. Um perfil candidato com ela corre na
// bateria e na qualificação; o falso que «anuncia e não chama» só chama a tool quando o pedido a
// pede; e um fornecedor que recusa o campo com 400 de parâmetro faz a qualificação recusar a rota.

// aos526PerfilComRegra é o perfil simples da qualificação (AOS-518) com a regra.
var aos526PerfilComRegra = strings.Replace(aos518PerfilSimples, `"capabilities":["tools"]`,
	`"capabilities":["tools"],"params":{"tool_choice":"enquanto_o_contrato_nao_estiver_satisfeito"}`, 1)

// aos526Corpos corre a bateria (braço A, roteiro só `cumpre`) com o perfil dado e devolve os
// corpos dos pedidos.
func aos526Corpos(t *testing.T, perfil string) [][]byte {
	t.Helper()
	p, err := LerPerfilCandidato([]byte(perfil))
	if err != nil {
		t.Fatal(err)
	}
	b := mustBateria(t)
	falso := NovoProviderFalso([]Comportamento{ComportamentoCumpre})
	falso.GuardarCorpos()
	srv := httptest.NewServer(falso)
	t.Cleanup(srv.Close)
	no, err := NovoNoDeEnsaio(context.Background(), CfgDoNo{Bateria: b, BaseURL: srv.URL + "/v1", Credencial: "credencial-de-teste", Perfil: p})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(no.Fechar)
	plano, err := planoDasOpcoes(b, opcoes{experiencia: string(ExperienciaBateria), braco: string(BracoA), semente: 526})
	if err != nil {
		t.Fatal(err)
	}
	(&ambienteFalso{bateria: b, falso: falso, no: no}).correr(t, plano, nil)
	return falso.Corpos()
}

// O PEDIDO NA BATERIA: com a regra, o 1.º pedido de cada nó com tools nomeia a tool do contrato
// (cada nó da bateria oferece uma só tool, e é a que o contrato exige);
// o pedido que já leva o resultado de uma tool (o contrato foi cumprido no turno anterior) e o de
// um nó sem tools nunca levam o campo. Sem a regra, nenhum pedido o leva.
func TestAOS526_Banco_OPedidoLevaToolChoiceSoEnquantoOContratoEstaPorCumprir(t *testing.T) {
	com, sem := aos526Corpos(t, aos526PerfilComRegra), aos526Corpos(t, aos518PerfilSimples)
	if len(com) == 0 || len(com) != len(sem) {
		t.Fatalf("as duas corridas fizeram %d e %d pedidos", len(com), len(sem))
	}
	primeiros, segundos, semTools := 0, 0, 0
	for i, corpo := range com {
		var p pedidoAoFalso
		if err := json.Unmarshal(corpo, &p); err != nil {
			t.Fatal(err)
		}
		temResultado := bytes.Contains(corpo, []byte(`"role":"tool"`))
		nomeada := ""
		if len(p.Tools) == 1 {
			nomeada = `"tool_choice":{"type":"function","function":{"name":"` + p.Tools[0].Function.Name + `"}}`
		}
		switch {
		case len(p.Tools) == 0:
			semTools++
			if p.comToolChoice() {
				t.Fatalf("pedido %d de um no sem tools levou tool_choice: %s", i, corpo)
			}
		case temResultado:
			segundos++
			if p.comToolChoice() {
				t.Fatalf("pedido %d, depois de o contrato ser cumprido, levou tool_choice: %s", i, corpo)
			}
		default:
			primeiros++
			if nomeada == "" || !bytes.HasSuffix(corpo, []byte(","+nomeada+"}")) {
				t.Fatalf("o primeiro pedido %d de um no com tools nao nomeou a tool do contrato: %s", i, corpo)
			}
		}
		// Tirado o campo, o pedido é o da corrida sem a regra, byte a byte.
		got := corpo
		if nomeada != "" {
			got = bytes.Replace(corpo, []byte(","+nomeada), nil, 1)
		}
		if !bytes.Equal(got, sem[i]) {
			t.Fatalf("pedido %d: sem o tool_choice, o corpo nao e o da corrida sem a regra:\n com: %s\n sem: %s", i, corpo, sem[i])
		}
	}
	if primeiros == 0 || segundos == 0 || semTools == 0 {
		t.Fatalf("a bateria nao exercitou os tres casos: primeiros %d, segundos %d, sem tools %d", primeiros, segundos, semTools)
	}
	for i, corpo := range sem {
		if bytes.Contains(corpo, []byte("tool_choice")) {
			t.Fatalf("pedido %d sem a regra levou tool_choice: %s", i, corpo)
		}
	}
}

// O FALSO QUE ANUNCIA E NÃO CHAMA. Sem a regra, a 1.ª tentativa de cada nó com tools fecha sem
// tool call (L2) e a qualificação recusa; com a regra, o mesmo falso chama a tool e a rota sai
// qualificada.
func TestAOS526_Banco_ARegraCorrigeOFalsoQueAnunciaENaoChama(t *testing.T) {
	_, r := aos518Qualificar(t, aos518PerfilSimples, "--roteiro", string(ComportamentoRespeitaToolChoice), "--silencioso")
	q := aos518Veredicto(t, r)
	if q.Veredicto != VeredictoRecusado || !contem(q.Razoes, RazaoModeloSemToolCall) || q.Contagens.SemToolCallAPrimeira != 40 {
		t.Fatalf("sem a regra: veredicto %s %v, sem tool call a primeira %d; quer recusado por L2 com 40", q.Veredicto, q.Razoes, q.Contagens.SemToolCallAPrimeira)
	}
	e, r := aos518Qualificar(t, aos526PerfilComRegra, "--roteiro", string(ComportamentoRespeitaToolChoice), "--silencioso")
	q = aos518Veredicto(t, r)
	if e.codigo != SaidaQualificadoEmEnsaio || q.Veredicto != VeredictoQualificado || len(q.Razoes) != 0 {
		t.Fatalf("com a regra: codigo %d, veredicto %s %v\n%s", e.codigo, q.Veredicto, q.Razoes, e.stderr)
	}
	if c := q.Contagens; c.SemToolCallAPrimeira != 0 || c.NosComToolsCumpridos != 40 || c.NosSemToolsCumpridos != 16 {
		t.Fatalf("com a regra: sem tool call a primeira %d, cumpridos %d e %d", c.SemToolCallAPrimeira, c.NosComToolsCumpridos, c.NosSemToolsCumpridos)
	}
	perfil, err := LerPerfilCandidato([]byte(aos526PerfilComRegra))
	if err != nil {
		t.Fatal(err)
	}
	if q.Rota.DigestDoPerfil != perfil.Digest() {
		t.Fatalf("o veredicto nao e o do perfil com a regra: %s", q.Rota.DigestDoPerfil)
	}
	verSemFugas(t, "qualificacao com a regra", tudoOQueFoiEscrito(t, e, e.saida), proibidosDeTexto(t))
}

// O FORNECEDOR QUE RECUSA O CAMPO. Com a regra, cada 1.º pedido de um nó com tools leva 400 de
// parâmetro: os nós com tools não se cumprem e a qualificação RECUSA a rota — descobre-se no
// banco, não em produção. Sem a regra, o mesmo fornecedor não vê o campo e a rota qualifica-se.
func TestAOS526_Banco_UmFornecedorQueRecusaOCampoFazAQualificacaoRecusar(t *testing.T) {
	e, r := aos518Qualificar(t, aos526PerfilComRegra, "--roteiro", string(ComportamentoRecusaToolChoice), "--silencioso")
	q := aos518Veredicto(t, r)
	// L1 é a razão firme; a amostra dos nós SEM tools fica incompleta porque o nó de resumo que
	// depende de um nó com tools não chega a correr (como nos outros falsos maus do AOS-518).
	if quer := []string{RazaoModeloNaoCumpridos, RazaoAmostraIncompleta}; e.codigo != SaidaModeloRecusado || q.Veredicto != VeredictoRecusado || !reflect.DeepEqual(q.Razoes, quer) {
		t.Fatalf("com a regra: codigo %d, veredicto %s %v; quer recusado com %v", e.codigo, q.Veredicto, q.Razoes, quer)
	}
	c := q.Contagens
	if c.Pedidos4xxDeParametro == 0 || c.NosComToolsCumpridos != 0 || c.NaoCumpridosPorDesfecho[DesfechoErroHTTP] != c.NosComToolsNaAmostra || c.NosComToolsNaAmostra == 0 {
		t.Fatalf("os 400 nao contaram como erro de parametro: 4xx de parametro %d, cumpridos %d, por desfecho %v, na amostra %d",
			c.Pedidos4xxDeParametro, c.NosComToolsCumpridos, c.NaoCumpridosPorDesfecho, c.NosComToolsNaAmostra)
	}
	if c.InterrompidosPorCausa[InterrompidoPorConta] != 0 {
		t.Fatalf("um 400 de parametro foi lido como problema da conta: %v", c.InterrompidosPorCausa)
	}
	verSemFugas(t, "qualificacao recusada pelo tool_choice", tudoOQueFoiEscrito(t, e, e.saida), proibidosDeTexto(t))

	e, r = aos518Qualificar(t, aos518PerfilSimples, "--roteiro", string(ComportamentoRecusaToolChoice), "--silencioso")
	if q = aos518Veredicto(t, r); e.codigo != SaidaQualificadoEmEnsaio || q.Veredicto != VeredictoQualificado {
		t.Fatalf("sem a regra o fornecedor nao ve o campo: codigo %d, veredicto %s %v", e.codigo, q.Veredicto, q.Razoes)
	}
}

// O perfil candidato com a regra é lido pela leitura fechada, e um valor fora do vocabulário recusa.
func TestAOS526_Banco_OPerfilCandidatoComARegra(t *testing.T) {
	p, err := LerPerfilCandidato([]byte(aos526PerfilComRegra))
	if err != nil || p.Params == nil || p.Params.ToolChoice == "" {
		t.Fatalf("o perfil candidato com a regra nao se leu: %+v %v", p.Params, err)
	}
	if paramsDoPerfil(*p) != "tool_choice=enquanto_o_contrato_nao_estiver_satisfeito" {
		t.Fatalf("o resumo nao mostra a regra: %q", paramsDoPerfil(*p))
	}
	mau := strings.Replace(aos526PerfilComRegra, "enquanto_o_contrato_nao_estiver_satisfeito", "required", 1)
	if _, err := LerPerfilCandidato([]byte(mau)); err == nil {
		t.Fatal("um valor fora do vocabulario tinha de recusar o perfil candidato")
	}
	if _, existe := modelgateway.RouteProfileFor(AliasDaRota); existe {
		t.Fatal("o alias da rota de ensaio passou a existir na tabela do no")
	}
}
