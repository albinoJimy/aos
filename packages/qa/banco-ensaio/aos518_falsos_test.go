package bancoensaio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-518 — OS CONTROLOS NEGATIVOS EM CI: os providers falsos «maus», um por modo de falha, e o
// falso «bom». Correm em cada PR, sem rede e sem Docker, pela linha de comandos inteira.

// Os perfis de teste da qualificação. O modelo esperado é o que o falso declara ter servido.
const (
	aos518PerfilSimples = `{"requested":"rota-de-ensaio","expected_model":"openai/modelo-falso-do-banco","wire_class":"openai-chat-completions","capabilities":["tools"]}`
	// O perfil com devolução OBRIGATÓRIA: é o que pede a corrida de controlo sem devolução.
	aos518PerfilObrigatorio = `{"requested":"rota-de-ensaio","expected_model":"anthropic/modelo-de-ensaio","wire_class":"openai-chat-completions","capabilities":["tools"],"projection_version":"1.3.0","devolver":"obrigatorio"}`
)

// aos518Qualificar corre `aos-ensaio falso --experiencia qualificacao` e devolve a execução e o relatório.
func aos518Qualificar(t *testing.T, perfil string, args ...string) (execucao, *Relatorio) {
	t.Helper()
	saida := t.TempDir()
	e := executar(t, Ambiente{}, append([]string{"falso", "--experiencia", "qualificacao", "--perfil", aos516Ficheiro(t, perfil), "--saida", saida}, args...)...)
	e.saida = saida
	return e, aos516Relatorio(t, saida)
}

func aos518Veredicto(t *testing.T, r *Relatorio) *QualificacaoDoModelo {
	t.Helper()
	if r.QualificacaoDoModelo == nil {
		t.Fatalf("o relatorio de uma qualificacao nao traz o veredicto")
	}
	return r.QualificacaoDoModelo
}

// O FALSO «BOM»: qualificado, com 40 de 40 nós com tools.
func TestAOS518_Falso_OBomSaiQualificado(t *testing.T) {
	e, r := aos518Qualificar(t, aos518PerfilSimples, "--roteiro", "cumpre")
	if e.codigo != SaidaOK {
		t.Fatalf("codigo %d, quer %d\n%s", e.codigo, SaidaOK, e.stderr)
	}
	q := aos518Veredicto(t, r)
	if q.Veredicto != VeredictoQualificado || len(q.Razoes) != 0 || len(q.Avisos) != 0 || q.Regra != RegraDoVeredictoDoModelo {
		t.Fatalf("veredicto = %s %v avisos %v", q.Veredicto, q.Razoes, q.Avisos)
	}
	c := q.Contagens
	if c.NosComToolsNaAmostra != 40 || c.NosComToolsCumpridos != 40 || c.NosComToolsExigidos != 40 || c.NosObservados != 56 {
		t.Errorf("amostra = %d de %d, cumpridos %d, nos observados %d; quer 40 de 40, 40, 56", c.NosComToolsNaAmostra, c.NosComToolsExigidos, c.NosComToolsCumpridos, c.NosObservados)
	}
	// 96 pedidos: 5 nos com tools de 2 turnos e 2 sem tools de 1, oito vezes. Todos 200, todos
	// com o modelo esperado, e 40 deles levavam uma mensagem tool.
	if c.Turnos != 96 || c.TurnosComModeloIgual != 96 || c.PedidosCom2xx != 96 || c.PedidosComMensagemTool != 40 || r.Pedidos.Enviados != 96 {
		t.Errorf("turnos %d, modelo igual %d, 2xx %d, com mensagem tool %d, enviados %d", c.Turnos, c.TurnosComModeloIgual, c.PedidosCom2xx, c.PedidosComMensagemTool, r.Pedidos.Enviados)
	}
	for _, l := range q.Limiares {
		if l.Violado || l.Medida.Numerador != 0 || l.Medida.IC95Superior == nil {
			t.Errorf("limiar %s = %+v", l.ID, l)
		}
	}
	// O que identifica a rota qualificada, e o orcamento.
	b := mustBateria(t)
	if q.Rota.DigestDoPerfil != r.Digests.Perfil || q.Rota.DigestDaRota != r.Digests.Rota || q.Rota.DigestDaBateria != b.Digest() ||
		q.Rota.VersaoDaBateria != b.Versao || q.Rota.ModeloEsperado != "openai/modelo-falso-do-banco" || q.Rota.ImagemDoProxy != "" {
		t.Errorf("rota qualificada = %+v", q.Rota)
	}
	if !strings.HasPrefix(q.Rota.DigestDoPerfil, "sha256:") || !strings.HasPrefix(q.Rota.DigestDaRota, "sha256:") {
		t.Errorf("faltam digests na rota qualificada: %+v", q.Rota)
	}
	o := q.Orcamento
	if o.PiorCasoDeclarado != 672 || o.TectoDePedidos != 300 || o.PedidosEnviados != 96 || o.TectoMicroUSD != 3_000_000 || o.MicroUSDEstimado != nil || o.QualificacoesDoPerfilHoje != nil || o.MaximoPorPerfilPorDia != 2 {
		t.Errorf("orcamento = %+v", o)
	}
	if q.Controlo != nil {
		t.Errorf("um perfil sem devolucao obrigatoria nao tem corrida de controlo")
	}
	// O que a corrida NAO prova, em vocabulario fechado e nas frases dos limites.
	for _, quer := range []string{NaoProvaBateriaSintetica, NaoProvaLeitura, NaoProvaAmostra, NaoProvaQueEBom, NaoProvaACorrida, NaoProvaOutraRota, NaoProvaModeloReal} {
		if !contem(q.NaoProva, quer) {
			t.Errorf("nao_prova sem %q: %v", quer, q.NaoProva)
		}
	}
	limites := strings.Join(r.Limites, "\n")
	for _, quer := range []string{"seis casos sinteticos", "Nenhum dos dois prova que o fornecedor LEU", "abaixo de cerca de 9%", "NAO entra na regra", "Nao prova que a corrida aconteceu"} {
		if !strings.Contains(limites, quer) {
			t.Errorf("os limites nao dizem %q", quer)
		}
	}
	if strings.Contains(limites, "nao aceita nem recusa um modelo") {
		t.Errorf("uma qualificacao da um veredicto: o limite «nao aceita nem recusa» ja nao e verdade nela")
	}
	if !strings.HasPrefix(e.stdout, "QUALIFICACAO DO MODELO (AOS-518): QUALIFICADO") || !strings.Contains(e.stderr, "VEREDICTO DA QUALIFICACAO: QUALIFICADO") {
		t.Errorf("o veredicto nao vem a cabeca do resumo, ou falta no fim:\n%.300s", e.stdout)
	}
	// SO CONTAGENS E VOCABULARIO FECHADO: nem texto de respostas, nem conteudo de documentos.
	verSemFugas(t, "qualificacao do falso bom", tudoOQueFoiEscrito(t, e, e.saida), proibidosDeTexto(t))
}

func contem(lista []string, v string) bool {
	for _, x := range lista {
		if x == v {
			return true
		}
	}
	return false
}

// OS CINCO FALSOS «MAUS»: cada um da `recusado` com a SUA razao. As listas de razoes sao exactas e
// distintas duas a duas — um falso que caisse na razao de outro falhava aqui.
func TestAOS518_Falso_OsCincoMausSaemRecusadosCadaUmComASuaRazao(t *testing.T) {
	maus := []struct {
		nome, roteiro string
		razoes        []string
		// propria e a razao (ou, no ultimo, o desfecho) que so este falso tem.
		propria string
		confere func(t *testing.T, c ContagensDaQualificacao)
	}{
		{"nao chama a tool", "texto", []string{RazaoModeloNaoCumpridos, RazaoModeloSemToolCall}, RazaoModeloSemToolCall, func(t *testing.T, c ContagensDaQualificacao) {
			if c.SemToolCallAPrimeira != 40 || c.NaoCumpridosPorDesfecho[string(agentruntime.OutcomeContractNoCall)] != 40 {
				t.Errorf("sem tool call a primeira %d, por desfecho %v", c.SemToolCallAPrimeira, c.NaoCumpridosPorDesfecho)
			}
		}},
		{"responde vazio", "vazia", []string{RazaoModeloNaoCumpridos, RazaoModeloRespostasVazias}, RazaoModeloRespostasVazias, func(t *testing.T, c ContagensDaQualificacao) {
			if c.RespostasVaziasNoFim == 0 || c.NaoCumpridosPorDesfecho[string(agentruntime.OutcomeEmptyOutput)] != 40 {
				t.Errorf("vazias no fim %d, por desfecho %v", c.RespostasVaziasNoFim, c.NaoCumpridosPorDesfecho)
			}
		}},
		{"recusa o segundo turno com 4xx", "rejeita_segundo", []string{RazaoModeloNaoCumpridos, RazaoModelo4xxNoSegundo}, RazaoModelo4xxNoSegundo, func(t *testing.T, c ContagensDaQualificacao) {
			if c.QuatroXXComMensagemTool != 40 || c.PedidosComMensagemTool != 40 || c.NaoCumpridosPorDesfecho[DesfechoErroHTTP] != 40 {
				t.Errorf("4xx em segundos turnos %d de %d, por desfecho %v", c.QuatroXXComMensagemTool, c.PedidosComMensagemTool, c.NaoCumpridosPorDesfecho)
			}
		}},
		{"troca o modelo servido", "troca_modelo", []string{RazaoModeloServidoDiferente}, RazaoModeloServidoDiferente, func(t *testing.T, c ContagensDaQualificacao) {
			// Cumpre tudo — e por isso so L5 o apanha.
			if c.NosComToolsCumpridos != 40 || c.TurnosComModeloDiferente != 96 || c.TurnosComModeloIgual != 0 {
				t.Errorf("cumpridos %d, turnos com outro modelo %d, com o esperado %d", c.NosComToolsCumpridos, c.TurnosComModeloDiferente, c.TurnosComModeloIgual)
			}
		}},
		{"corta a resposta", "cortada", []string{RazaoModeloNaoCumpridos}, string(agentruntime.OutcomeTruncated), func(t *testing.T, c ContagensDaQualificacao) {
			// A D2 nao tem limiar proprio para o corte: apanha-o L1, sozinho, e o desfecho diz porque.
			if !reflect.DeepEqual(c.NaoCumpridosPorDesfecho, map[string]int{string(agentruntime.OutcomeTruncated): 40}) {
				t.Errorf("nao cumpridos por desfecho = %v, quer so truncated: 40", c.NaoCumpridosPorDesfecho)
			}
		}},
	}
	vistas := map[string]string{}
	for _, m := range maus {
		e, r := aos518Qualificar(t, aos518PerfilSimples, "--roteiro", m.roteiro, "--silencioso")
		q := aos518Veredicto(t, r)
		if e.codigo != SaidaModeloRecusado || q.Veredicto != VeredictoRecusado {
			t.Errorf("%s: codigo %d, veredicto %s; quer %d e recusado", m.nome, e.codigo, q.Veredicto, SaidaModeloRecusado)
		}
		if !reflect.DeepEqual(q.Razoes, m.razoes) {
			t.Errorf("%s: razoes %v, quer %v", m.nome, q.Razoes, m.razoes)
		}
		if r.Terminou != TerminouCompleta || q.Contagens.NosComToolsNaAmostra != 40 {
			t.Errorf("%s: terminou %s com %d nos na amostra; um falso mau falha com a amostra inteira", m.nome, r.Terminou, q.Contagens.NosComToolsNaAmostra)
		}
		m.confere(t, q.Contagens)
		chave := strings.Join(q.Razoes, ",")
		if outro, visto := vistas[chave]; visto {
			t.Errorf("%s caiu nas razoes de %s: %v", m.nome, outro, q.Razoes)
		}
		vistas[chave] = m.nome
		verSemFugas(t, "qualificacao: "+m.nome, tudoOQueFoiEscrito(t, e, e.saida), proibidosDeTexto(t))
	}
	// A razao propria de cada um dos quatro primeiros nao aparece em nenhum dos outros.
	for _, m := range maus[:4] {
		for chave, nome := range vistas {
			if nome != m.nome && contem(strings.Split(chave, ","), m.propria) {
				t.Errorf("a razao %q, que e a de «%s», apareceu em «%s»", m.propria, m.nome, nome)
			}
		}
	}
}

// OS ALVOS DA REVISAO ADVERSARIAL (o verde falso): providers que nao merecem `qualificado` e que
// um arnes descuidado deixava passar. Nenhum sai qualificado.
func TestAOS518_Falso_NenhumVerdeFalso(t *testing.T) {
	// 500 a tudo: nenhum no entra na amostra. Nao e `recusado` (nao diz nada do modelo) nem,
	// muito menos, `qualificado`.
	e, r := aos518Qualificar(t, aos518PerfilSimples, "--roteiro", "erro_500", "--silencioso")
	q := aos518Veredicto(t, r)
	if e.codigo != SaidaInconclusivo || q.Veredicto != VeredictoInconclusivo || !reflect.DeepEqual(q.Razoes, []string{RazaoAmostraIncompleta, RazaoInterrompidos5xx}) {
		t.Errorf("500 a tudo: codigo %d, %s %v", e.codigo, q.Veredicto, q.Razoes)
	}
	if q.Contagens.NosComToolsNaAmostra != 0 || q.Contagens.InterrompidosPorCausa[InterrompidoPor5xx] != 40 {
		t.Errorf("500 a tudo: amostra %d, interrompidos %v", q.Contagens.NosComToolsNaAmostra, q.Contagens.InterrompidosPorCausa)
	}

	// 401 a tudo: e a chave, nao o modelo.
	_, r = aos518Qualificar(t, aos518PerfilSimples, "--roteiro", "erro_401", "--silencioso")
	if q = aos518Veredicto(t, r); q.Veredicto != VeredictoInconclusivo || !reflect.DeepEqual(q.Razoes, []string{RazaoAmostraIncompleta, RazaoInterrompidosConta}) {
		t.Errorf("401 a tudo: %s %v", q.Veredicto, q.Razoes)
	}

	// O roteiro misto por omissao (uma conversa de cada comportamento anormal): recusado.
	e, r = aos518Qualificar(t, aos518PerfilSimples, "--silencioso")
	if q = aos518Veredicto(t, r); e.codigo != SaidaModeloRecusado || q.Veredicto != VeredictoRecusado {
		t.Errorf("roteiro misto: codigo %d, %s %v", e.codigo, q.Veredicto, q.Razoes)
	}

	// Um provider que chama a tool e responde com um texto SEM os factos do documento: o kernel
	// fecha `cumprido` (o contrato de conclusao nao le o conteudo) e a D2 nao tem limiar para
	// isto. Nao muda o veredicto — mas nao passa em silencio: o aviso vem a cabeca.
	e, r = aos518Qualificar(t, aos518PerfilSimples, "--roteiro", "sem_factos")
	if q = aos518Veredicto(t, r); !reflect.DeepEqual(q.Avisos, []string{AvisoFactosAusentes}) || !strings.HasPrefix(e.stdout, "AVISO — FACTOS_AUSENTES_EM_RESPOSTAS_CUMPRIDAS") || r.Taxas.FactosAusentes.Numerador == 0 {
		t.Errorf("sem factos: %s, avisos %v, factos ausentes %d\n%.200s", q.Veredicto, q.Avisos, r.Taxas.FactosAusentes.Numerador, e.stdout)
	}

	// Um provider que NAO declara o modelo servido: nenhum turno se prova igual ao esperado.
	amb := aos518Ambiente(t, aos518PerfilSimples, NovoProviderFalso([]Comportamento{ComportamentoCumpre}), nil, TectoDePedidosDaQualificacao)
	if q = aos518Veredicto(t, amb.correr(t, context.Background())); q.Veredicto != VeredictoRecusado || !reflect.DeepEqual(q.Razoes, []string{RazaoModeloServidoNaoReportado}) {
		t.Errorf("modelo nao reportado: %s %v", q.Veredicto, q.Razoes)
	}

	// Um provider que responde 400 a TUDO: o perfil e recusado, e a corrida para ao terceiro
	// pedido em vez de gastar o tecto.
	pedidos := &atomic.Int64{}
	recusaTudo := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pedidos.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"SENTINELA-MENSAGEM-DO-ERRO parametro que o provider nao aceita","type":"invalid_request_error"}}`))
	})
	amb = aos518Ambiente(t, aos518PerfilSimples, recusaTudo, nil, TectoDePedidosDaQualificacao)
	r = amb.correr(t, context.Background())
	q = aos518Veredicto(t, r)
	if q.Veredicto != VeredictoRecusado || !reflect.DeepEqual(q.Razoes, []string{RazaoModeloNaoCumpridos, RazaoPerfilRecusado, RazaoAmostraIncompleta}) {
		t.Errorf("400 a tudo: %s %v", q.Veredicto, q.Razoes)
	}
	if r.Terminou != TerminouPerfilRecusado || pedidos.Load() != RecusasDoPerfilQueAbortam {
		t.Errorf("400 a tudo: terminou %s com %d pedidos; quer %s ao fim de %d", r.Terminou, pedidos.Load(), TerminouPerfilRecusado, RecusasDoPerfilQueAbortam)
	}
}

// ambienteDaQualificacao é um nó de ensaio de qualificação ligado a um provider dado, para os
// cenários que a linha de comandos não deixa montar (interromper a meio, um provider à medida).
type ambienteDaQualificacao struct {
	cfg CfgDaCorrida
}

func aos518Ambiente(t *testing.T, perfilJSON string, provider http.Handler, contador *Contador, tecto int64) *ambienteDaQualificacao {
	t.Helper()
	b := mustBateria(t)
	perfil, err := LerPerfilCandidato([]byte(perfilJSON))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(provider)
	t.Cleanup(srv.Close)
	limite := NovoLimiteDaQualificacao(tecto)
	no, err := NovoNoDeEnsaio(context.Background(), CfgDoNo{
		Bateria: b, BaseURL: srv.URL + "/v1", Credencial: "credencial-de-teste", Contador: contador,
		Perfil: perfil, Qualificacao: true, Limite: limite,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(no.Fechar)
	return &ambienteDaQualificacao{cfg: CfgDaCorrida{
		Modo: ModoFalso, Plano: PlanoDaQualificacao(b, 518), Bateria: b, No: no, Contador: contador,
		Rota:         RotaDoRelatorio{Fornecedor: string(FornecedorFalso), Modelo: "modelo-falso-do-banco", Digest: DigestDaRota("falso", "modelo-falso-do-banco", "")},
		PerfilDigest: perfil.Digest(), Relogio: relogioFixo,
		Qualificacao: &CfgDaQualificacao{ModeloEsperado: perfil.ExpectedModel, Limite: limite, TectoMicroUSD: TectoMicroUSDDaQualificacao},
	}}
}

func (a *ambienteDaQualificacao) correr(t *testing.T, ctx context.Context) *Relatorio {
	t.Helper()
	r, err := Correr(ctx, a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// aos518FalsoBom devolve um provider falso que cumpre e declara o modelo que o perfil simples espera.
func aos518FalsoBom() *ProviderFalso {
	f := NovoProviderFalso([]Comportamento{ComportamentoCumpre})
	f.DeclararModeloServido("openai/modelo-falso-do-banco")
	return f
}

// UMA CORRIDA INTERROMPIDA A MEIO da `inconclusivo` — mesmo que tudo o que correu tenha corrido bem.
func TestAOS518_Falso_InterrompidaAMeioSaiInconclusivo(t *testing.T) {
	falso := aos518FalsoBom()
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	pedidos := &atomic.Int64{}
	amb := aos518Ambiente(t, aos518PerfilSimples, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		falso.ServeHTTP(w, req)
		if pedidos.Add(1) == 30 {
			cancelar()
		}
	}), nil, TectoDePedidosDaQualificacao)
	r := amb.correr(t, ctx)
	q := aos518Veredicto(t, r)
	if r.Terminou != TerminouInterrompid || q.Veredicto != VeredictoInconclusivo {
		t.Fatalf("terminou %s, veredicto %s %v; quer interrompida e inconclusivo", r.Terminou, q.Veredicto, q.Razoes)
	}
	if !contem(q.Razoes, RazaoCorridaInterrompida) || !contem(q.Razoes, RazaoAmostraIncompleta) {
		t.Errorf("razoes = %v", q.Razoes)
	}
	for _, firme := range RazoesFirmesDoModelo() {
		if contem(q.Razoes, firme) {
			t.Errorf("uma corrida interrompida sem falha nenhuma nao pode trazer a razao firme %q", firme)
		}
	}
	if c := q.Contagens; c.NosComToolsNaAmostra == 0 || c.NosComToolsNaAmostra >= 40 || c.NosComToolsCumpridos != c.NosComToolsNaAmostra {
		t.Errorf("amostra parcial = %d, cumpridos %d", c.NosComToolsNaAmostra, c.NosComToolsCumpridos)
	}
}

// O PIOR CASO NAO CABE NO TECTO: `inconclusivo`, com relatorio escrito, e NENHUM pedido enviado —
// o contador do falso fica a zero.
func TestAOS518_Falso_PiorCasoQueNaoCabeNaoEnviaNada(t *testing.T) {
	// Pela biblioteca, com o provider a vista: 672 pedidos no pior caso contra um tecto de 671.
	falso := aos518FalsoBom()
	caminho := filepath.Join(t.TempDir(), "contador.json")
	contador, err := AbrirContador(caminho, FornecedorFalso, Tectos{PedidosDia: 671}, nil, "", relogioFixo)
	if err != nil {
		t.Fatal(err)
	}
	defer contador.Fechar()
	amb := aos518Ambiente(t, aos518PerfilSimples, falso, contador, TectoDePedidosDaQualificacao)
	r := amb.correr(t, context.Background())
	q := aos518Veredicto(t, r)
	if r.Terminou != TerminouNaoCoube || q.Veredicto != VeredictoInconclusivo || !reflect.DeepEqual(q.Razoes, []string{RazaoPiorCasoNaoCabe, RazaoAmostraIncompleta}) {
		t.Errorf("terminou %s, %s %v", r.Terminou, q.Veredicto, q.Razoes)
	}
	if falso.Pedidos() != 0 || r.Pedidos.Enviados != 0 || len(r.Observacoes) != 0 || q.Orcamento.PiorCasoDeclarado != 672 {
		t.Errorf("o falso recebeu %d pedidos, o relatorio conta %d enviados e %d observacoes, pior caso %d; quer 0, 0, 0 e 672", falso.Pedidos(), r.Pedidos.Enviados, len(r.Observacoes), q.Orcamento.PiorCasoDeclarado)
	}
	// Com um pedido mais de tecto ja cabe: a fronteira e a do pior caso, exacta.
	contador.Fechar()
	se, err := AbrirContador(filepath.Join(t.TempDir(), "contador.json"), FornecedorFalso, Tectos{PedidosDia: 672}, nil, "", relogioFixo)
	if err != nil {
		t.Fatal(err)
	}
	defer se.Fechar()
	if q = aos518Veredicto(t, aos518Ambiente(t, aos518PerfilSimples, aos518FalsoBom(), se, TectoDePedidosDaQualificacao).correr(t, context.Background())); q.Veredicto != VeredictoQualificado {
		t.Errorf("com o tecto igual ao pior caso: %s %v", q.Veredicto, q.Razoes)
	}

	// Pela linha de comandos: sai com o codigo de recusa, escreve o relatorio, e o contador fica a zero.
	pasta := t.TempDir()
	doCLI := filepath.Join(pasta, "contador.json")
	e, r := aos518Qualificar(t, aos518PerfilSimples, "--roteiro", "cumpre", "--tecto-pedidos", "200", "--contador", doCLI)
	if q = aos518Veredicto(t, r); e.codigo != SaidaRecusada || q.Veredicto != VeredictoInconclusivo || r.Pedidos.Enviados != 0 || *r.Pedidos.GastosHoje != 0 {
		t.Errorf("linha de comandos: codigo %d, %s, enviados %d, gastos hoje %d", e.codigo, q.Veredicto, r.Pedidos.Enviados, *r.Pedidos.GastosHoje)
	}
	if !strings.Contains(e.stderr, "nenhum pedido foi enviado") || !strings.HasPrefix(e.stdout, "QUALIFICACAO DO MODELO (AOS-518): INCONCLUSIVO") {
		t.Errorf("a recusa por tecto nao se explica:\n%s\n%.200s", e.stderr, e.stdout)
	}
}

// O TECTO PROPRIO DA QUALIFICACAO: ao atingir o maximo de pedidos a corrida para, o pedido
// seguinte NAO sai, e o veredicto e `inconclusivo`.
func TestAOS518_Falso_TectoDaQualificacaoParaACorrida(t *testing.T) {
	falso := aos518FalsoBom()
	amb := aos518Ambiente(t, aos518PerfilSimples, falso, nil, 25)
	r := amb.correr(t, context.Background())
	q := aos518Veredicto(t, r)
	if r.Terminou != TerminouTectoDaQualificacao || q.Veredicto != VeredictoInconclusivo || q.Razoes[0] != RazaoTectoDaQualificacao {
		t.Fatalf("terminou %s, %s %v", r.Terminou, q.Veredicto, q.Razoes)
	}
	if falso.Pedidos() != 25 || q.Orcamento.TectoDePedidos != 25 || !amb.cfg.Qualificacao.Limite.Atingido() {
		t.Errorf("o falso recebeu %d pedidos com um tecto de 25 (o tecto conta ANTES de enviar)", falso.Pedidos())
	}
	// O limite, sozinho: deixa sair exactamente o maximo.
	l := NovoLimiteDaQualificacao(TectoDePedidosDaQualificacao)
	for i := 0; i < TectoDePedidosDaQualificacao; i++ {
		if err := l.Reservar(); err != nil {
			t.Fatalf("o pedido %d foi recusado antes do tecto", i+1)
		}
	}
	if l.Atingido() {
		t.Errorf("com exactamente o maximo enviado, nenhum pedido foi ainda recusado")
	}
	if err := l.Reservar(); err == nil || l.Enviados() != TectoDePedidosDaQualificacao || !l.Atingido() {
		t.Errorf("o pedido 301 tinha de ser recusado: err=%v, enviados=%d", err, l.Enviados())
	}
	// A linha de comandos monta o limite com o tecto da D7 (preso no orcamento do relatorio do falso bom).
	// Uma qualificacao sem o limite no transporte do no recusa correr.
	amb = aos518Ambiente(t, aos518PerfilSimples, aos518FalsoBom(), nil, 25)
	amb.cfg.Qualificacao.Limite = NovoLimiteDaQualificacao(25) // outro objecto: o no nao o consulta
	if _, err := Correr(context.Background(), amb.cfg); err == nil {
		t.Errorf("uma qualificacao cujo tecto nao e o do transporte do no tinha de ser recusada")
	}
}

// «ACEITE» SEPARA-SE DE «EXIGIDO». Um perfil com devolucao obrigatoria qualifica-se com a corrida
// de controlo sem devolucao. Se o controlo falha como devia (4xx nos segundos turnos), a exigencia
// esta provada; se passa, o relatorio traz `exigencia_nao_provada` A CABECA do resumo — sem
// bloquear o `qualificado` (D2), e sem poder ser omitido.
func TestAOS518_Falso_ControloSemDevolucao(t *testing.T) {
	// O falso EXIGENTE: sem o estado de volta responde 400. O controlo falha como devia.
	e, r := aos518Qualificar(t, aos518PerfilObrigatorio, "--estado", "exige")
	q := aos518Veredicto(t, r)
	if e.codigo != SaidaOK || q.Veredicto != VeredictoQualificado || contem(q.Avisos, AvisoExigenciaNaoProvada) || contem(q.Avisos, AvisoDevolucaoNaoCumprida) {
		t.Fatalf("exigente: codigo %d, %s %v avisos %v\n%s", e.codigo, q.Veredicto, q.Razoes, q.Avisos, e.stderr)
	}
	ct := q.Controlo
	if ct == nil || ct.Resultado != ControloFalhouComoDevido || ct.Passagens != 1 || ct.Contagens == nil || ct.Contagens.QuatroXXComMensagemTool != 5 {
		t.Fatalf("controlo do exigente = %+v", ct)
	}
	perfil, _ := LerPerfilCandidato([]byte(aos518PerfilObrigatorio))
	if doControlo := PerfilDeControlo(*perfil); ct.DigestDoPerfil != doControlo.Digest() || ct.DigestDoPerfil == r.Digests.Perfil ||
		doControlo.StateReturn != modelgateway.StateReturnNever || doControlo.ProjectionVersion != modelgateway.NativeProjectionVersion120 || doControlo.ExpectedModel != perfil.ExpectedModel {
		t.Errorf("perfil de controlo = %+v (digest no relatorio %s)", doControlo, ct.DigestDoPerfil)
	}
	// O pior caso declarado inclui o controlo (672 + 84), e os pedidos dele contam nos enviados.
	if q.Orcamento.PiorCasoDeclarado != 756 || r.Pedidos.PrevistosMax != 756 || q.Orcamento.PedidosEnviados != r.Pedidos.Enviados || r.Pedidos.Enviados <= 96 {
		t.Errorf("orcamento com controlo = %+v, enviados %d", q.Orcamento, r.Pedidos.Enviados)
	}
	if strings.Contains(e.stdout, "EXIGENCIA_NAO_PROVADA") {
		t.Errorf("com a exigencia provada nao ha aviso")
	}

	// O falso que ACEITA TUDO (nao exige nada e nunca traz raciocinio): o controlo passa.
	e, r = aos518Qualificar(t, aos518PerfilObrigatorio, "--roteiro", "cumpre")
	q = aos518Veredicto(t, r)
	if e.codigo != SaidaOK || q.Veredicto != VeredictoQualificado {
		t.Fatalf("aceita tudo: codigo %d, %s %v", e.codigo, q.Veredicto, q.Razoes)
	}
	if q.Controlo == nil || q.Controlo.Resultado != ControloPassou || !reflect.DeepEqual(q.Avisos, []string{AvisoExigenciaNaoProvada, AvisoDevolucaoNaoCumprida}) {
		t.Fatalf("aceita tudo: controlo %+v, avisos %v", q.Controlo, q.Avisos)
	}
	// A CABECA: a primeira linha do resumo e o aviso, antes do veredicto.
	if !strings.HasPrefix(e.stdout, "AVISO — EXIGENCIA_NAO_PROVADA") || strings.Index(e.stdout, "AVISO — EXIGENCIA_NAO_PROVADA") > strings.Index(e.stdout, "QUALIFICACAO DO MODELO") {
		t.Errorf("o aviso nao vem a cabeca do resumo:\n%.400s", e.stdout)
	}
	if !strings.Contains(e.stderr, "AVISO DA QUALIFICACAO — exigencia_nao_provada") {
		t.Errorf("o aviso nao sai no fim da corrida:\n%s", e.stderr)
	}
	resumo, _ := filepath.Glob(filepath.Join(e.saida, "*.txt"))
	if cru, _ := os.ReadFile(resumo[0]); !strings.HasPrefix(string(cru), "AVISO — EXIGENCIA_NAO_PROVADA") {
		t.Errorf("o aviso nao vem a cabeca do ficheiro de resumo")
	}

	// Com a corrida principal recusada o controlo nao corre — e o aviso continua la.
	_, r = aos518Qualificar(t, aos518PerfilObrigatorio, "--roteiro", "texto", "--silencioso")
	if q = aos518Veredicto(t, r); q.Veredicto != VeredictoRecusado || q.Controlo == nil || q.Controlo.Resultado != ControloNaoCorreu || q.Controlo.Contagens != nil || !contem(q.Avisos, AvisoExigenciaNaoProvada) {
		t.Errorf("principal recusada: %s, controlo %+v, avisos %v", q.Veredicto, q.Controlo, q.Avisos)
	}
}

// A LINHA DE COMANDOS da qualificacao: o que recusa antes de correr.
func TestAOS518_LinhaDeComandos_Recusas(t *testing.T) {
	perfil := aos516Ficheiro(t, aos518PerfilSimples)
	for nome, args := range map[string][]string{
		"sem perfil":       {"falso", "--experiencia", "qualificacao", "--saida", t.TempDir()},
		"com --amostras":   {"falso", "--experiencia", "qualificacao", "--perfil", perfil, "--amostras", "2", "--saida", t.TempDir()},
		"com outro braco":  {"falso", "--experiencia", "qualificacao", "--perfil", perfil, "--braco", "B", "--saida", t.TempDir()},
		"experiencia nova": {"falso", "--experiencia", "qualificacoes", "--perfil", perfil, "--saida", t.TempDir()},
	} {
		e := executar(t, Ambiente{}, args...)
		if e.codigo != SaidaUso {
			t.Errorf("%s: codigo %d, quer %d\n%s", nome, e.codigo, SaidaUso, e.stderr)
		}
		if achados, _ := filepath.Glob(filepath.Join(e.saida, "*")); len(achados) != 0 {
			t.Errorf("%s: escreveu %v", nome, achados)
		}
	}
	// A biblioteca recusa o mesmo: um plano de qualificacao com outra amostra, e uma qualificacao sem perfil.
	b := mustBateria(t)
	plano := PlanoDaQualificacao(b, 1)
	plano.Amostras = 1
	if _, err := plano.PedidosMaximos(b); err == nil {
		t.Errorf("um plano de qualificacao com 1 passagem tinha de ser recusado")
	}
	amb := aos518Ambiente(t, aos518PerfilSimples, aos518FalsoBom(), nil, 10)
	amb.cfg.PerfilDigest = ""
	if _, err := Correr(context.Background(), amb.cfg); err == nil {
		t.Errorf("uma qualificacao sem digest de perfil tinha de ser recusada")
	}
	if _, err := NovoNoDeEnsaio(context.Background(), CfgDoNo{Bateria: b, BaseURL: "http://127.0.0.1:1/v1", Credencial: "x", Qualificacao: true}); err == nil {
		t.Errorf("um no de qualificacao sem perfil tinha de ser recusado")
	}
	// E uma experiencia que nao e a qualificacao nao aceita a configuracao dela.
	amb = aos518Ambiente(t, aos518PerfilSimples, aos518FalsoBom(), nil, 10)
	amb.cfg.Plano = PlanoDaBateria(b, BracoA, 1)
	if _, err := Correr(context.Background(), amb.cfg); err == nil {
		t.Errorf("a configuracao da qualificacao numa corrida da bateria tinha de ser recusada")
	}
}

// INERTE: as experiencias que ja existiam nao ganham nenhum campo da qualificacao. (Os bytes do
// relatorio da bateria estao presos pelo digest de linha de base do AOS-516.)
func TestAOS518_Inerte_AsOutrasExperienciasNaoMudam(t *testing.T) {
	for _, args := range [][]string{
		{"falso"},
		{"falso", "--experiencia", "separadores", "--amostras", "2"},
		{"falso", "--perfil", aos516Ficheiro(t, aos518PerfilSimples)},
		{"falso", "--estado", "exige", "--perfil", aos516Ficheiro(t, aos518PerfilObrigatorio)},
	} {
		saida := t.TempDir()
		e := executar(t, Ambiente{}, append(args, "--saida", saida, "--silencioso")...)
		if e.codigo != SaidaOK {
			t.Fatalf("%v: codigo %d\n%s", args, e.codigo, e.stderr)
		}
		tudo := tudoOQueFoiEscrito(t, e, saida)
		for _, novo := range []string{"qualificacao_do_modelo", CampoDoDigestDoRelatorio, "medidas_da_qualificacao", "QUALIFICACAO DO MODELO", "VEREDICTO DA QUALIFICACAO", port.HeaderServedModel} {
			if strings.Contains(tudo, novo) {
				t.Errorf("%v: a saida de uma experiencia que ja existia traz %q", args, novo)
			}
		}
		if !strings.Contains(tudo, "O banco devolve taxas; nao aceita nem recusa um modelo.") {
			t.Errorf("%v: o primeiro limite de sempre desapareceu", args)
		}
	}
	// O provider falso so declara o modelo servido quando lho pedem: sem isso, as respostas sao as de antes.
	srv := httptest.NewServer(NovoProviderFalso([]Comportamento{ComportamentoCumpre}))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if v := resp.Header.Values(port.HeaderServedModel); len(v) != 0 {
		t.Errorf("o provider falso declarou um modelo servido sem lho pedirem: %v", v)
	}
	// O vocabulario das razoes nao tem repetidos, e as firmes e as passageiras nao se cruzam.
	todas := append(RazoesFirmesDoModelo(), RazoesPassageirasDoModelo()...)
	ordenadas := append([]string(nil), todas...)
	sort.Strings(ordenadas)
	for i := 1; i < len(ordenadas); i++ {
		if ordenadas[i] == ordenadas[i-1] {
			t.Errorf("razao repetida no vocabulario: %q", ordenadas[i])
		}
	}
}
