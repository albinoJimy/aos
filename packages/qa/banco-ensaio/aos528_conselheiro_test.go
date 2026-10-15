package bancoensaio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AOS-528 — A TOOL `aconselhar`, A EXPERIÊNCIA `conselheiro` E OS SEUS FALSOS.

// ambienteDoConselheiro é um nó de ensaio com a bateria difícil, ligado a um executor falso e,
// quando o modo não é vazio, a um conselheiro falso noutro servidor.
type ambienteDoConselheiro struct {
	bateria     *Bateria
	falso       *ProviderFalso
	doConselho  *ConselheiroFalso
	conselheiro *Conselheiro
	no          *NoDeEnsaio
}

func novoAmbienteDoConselheiro(t *testing.T, roteiro []Comportamento, modo string, maxConsultas int, guardar func(RespostaDoRun)) *ambienteDoConselheiro {
	t.Helper()
	a := &ambienteDoConselheiro{bateria: aos528Bateria3(t), falso: NovoProviderFalso(roteiro)}
	a.falso.GuardarCorpos()
	srv := httptest.NewServer(a.falso)
	t.Cleanup(srv.Close)
	cfg := CfgDoNo{Bateria: a.bateria, BaseURL: srv.URL + "/v1", Credencial: "credencial-de-teste", GuardarRespostas: guardar}
	if modo != "" {
		a.doConselho = &ConselheiroFalso{Modo: modo}
		doConselho := httptest.NewServer(a.doConselho)
		t.Cleanup(doConselho.Close)
		var err error
		if a.conselheiro, err = NovoConselheiro(CfgDoConselheiro{BaseURL: doConselho.URL + "/v1", Credencial: "credencial-do-conselheiro", MaxConsultas: maxConsultas}); err != nil {
			t.Fatal(err)
		}
		cfg.Conselheiro = a.conselheiro
	}
	no, err := NovoNoDeEnsaio(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(no.Fechar)
	a.no = no
	return a
}

func (a *ambienteDoConselheiro) correr(t *testing.T, amostras int, precos PrecosDeclarados) *Relatorio {
	t.Helper()
	maxConsultas := 0
	if a.conselheiro != nil {
		maxConsultas = a.conselheiro.maxConsultas
	}
	r, err := Correr(context.Background(), CfgDaCorrida{
		Modo: ModoFalso, Plano: PlanoDoConselheiro(a.bateria, amostras, 528, maxConsultas), Bateria: a.bateria, No: a.no,
		Rota:     RotaDoRelatorio{Fornecedor: string(FornecedorFalso), Modelo: "modelo-falso-do-banco", Digest: DigestDaRota("falso", "modelo-falso-do-banco", "")},
		Conselho: &CfgDoConselho{Conselheiro: a.conselheiro, Rota: RotaDoRelatorio{Fornecedor: string(FornecedorFalso), Modelo: "modelo-falso-do-conselheiro"}, PrecoDoExecutor: precos.Executor, PrecoDoConselheiro: precos.Conselheiro},
		Relogio:  relogioFixo,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Terminou != TerminouCompleta || r.Taxas.Conselheiro == nil {
		t.Fatalf("a corrida do conselheiro terminou %s, com o bloco %+v", r.Terminou, r.Taxas.Conselheiro)
	}
	return r
}

// braco devolve o bloco de um braço do relatório.
func (r *Relatorio) aos528Braco(t *testing.T, nome string) BracoDoConselheiro {
	t.Helper()
	for _, b := range r.Taxas.Conselheiro.Bracos {
		if b.Braco == nome {
			return b
		}
	}
	t.Fatalf("o relatorio nao tem o braco %s", nome)
	return BracoDoConselheiro{}
}

// OS BRAÇOS DISTINGUEM OS FALSOS: (a) não consulta e erra, (b) consulta e segue o conselho, (c)
// consulta e ignora-o; e, para (b), o conselho certo e o errado.
func TestAOS528_OsBracosDistinguemOsFalsos(t *testing.T) {
	for _, caso := range []struct {
		nome          string
		roteiro       Comportamento
		modo          string
		so, com       int // casos cumpridos em sete, por braço
		consultados   int // runs do braço com conselheiro que consultaram
		proibidoNoCom bool
	}{
		{"cumpre sozinho, nao consulta", ComportamentoConselhoCumpre, ConselhoCerto, 7, 7, 0, false},
		{"(a) nao consulta e erra", ComportamentoConselhoNaoConsulta, ConselhoCerto, 0, 0, 0, true},
		{"(b) consulta e segue o conselho certo", ComportamentoConselhoSegue, ConselhoCerto, 0, 7, 7, false},
		{"(b) consulta e segue o conselho errado", ComportamentoConselhoSegue, ConselhoErrado, 0, 0, 7, true},
		{"(c) consulta e ignora o conselho certo", ComportamentoConselhoIgnora, ConselhoCerto, 0, 0, 7, true},
	} {
		t.Run(caso.nome, func(t *testing.T) {
			a := novoAmbienteDoConselheiro(t, []Comportamento{caso.roteiro}, caso.modo, 2, nil)
			r := a.correr(t, 1, PrecosDeclarados{})
			so, com := r.aos528Braco(t, BracoSoExecutor), r.aos528Braco(t, BracoComConselheiro)
			if !(fracao{caso.so, 7}).confere(so.CasosCumpridos) || !(fracao{caso.com, 7}).confere(com.CasosCumpridos) {
				t.Errorf("cumpridos: so-executor %d/%d (quer %d/7), com conselheiro %d/%d (quer %d/7)",
					so.CasosCumpridos.Numerador, so.CasosCumpridos.Denominador, caso.so, com.CasosCumpridos.Numerador, com.CasosCumpridos.Denominador, caso.com)
			}
			if !(fracao{caso.consultados, 7}).confere(com.TaxaDeConsulta) || so.TaxaDeConsulta.Denominador != 0 || so.Consultas != 0 {
				t.Errorf("taxa de consulta: com %d/%d (quer %d/7); sem conselheiro %d/%d (quer 0/0)", com.TaxaDeConsulta.Numerador, com.TaxaDeConsulta.Denominador, caso.consultados, so.TaxaDeConsulta.Numerador, so.TaxaDeConsulta.Denominador)
			}
			if a.doConselho.Pedidos() != caso.consultados || com.Conselheiro.Pedidos != int64(caso.consultados) || com.ConsultasComResposta != caso.consultados {
				t.Errorf("o conselheiro recebeu %d pedidos, o relatorio diz %d e %d com resposta; quer %d", a.doConselho.Pedidos(), com.Conselheiro.Pedidos, com.ConsultasComResposta, caso.consultados)
			}
			erradas := 0
			for _, k := range com.PorCaso {
				erradas += k.RespostasErradas
			}
			if (erradas == 7) != caso.proibidoNoCom {
				t.Errorf("respostas erradas no braco com conselheiro: %d", erradas)
			}
			// Os runs do braço sem conselheiro não têm a tool: nenhum pedido do executor a oferece.
			for _, o := range r.Observacoes {
				if (o.BracoDoConselheiro == BracoComConselheiro) != (o.Conselho != nil) {
					t.Fatalf("observacao %s/%s do braco %s com conselho %+v", o.Caso, o.No, o.BracoDoConselheiro, o.Conselho)
				}
			}
		})
	}
	// O braço sem conselheiro não vê a tool; o com conselheiro vê-a. Lido nos pedidos ao executor.
	a := novoAmbienteDoConselheiro(t, []Comportamento{ComportamentoConselhoCumpre}, ConselhoCerto, 2, nil)
	a.correr(t, 1, PrecosDeclarados{})
	com, sem := 0, 0
	for _, corpo := range a.falso.Corpos() {
		if strings.Contains(string(corpo), `"name":"`+ToolAconselhar+`"`) {
			com++
		} else {
			sem++
		}
	}
	// Sete runs por braço, dois pedidos cada (ler, responder): todos os do braço com conselheiro
	// levam a tool, nenhum do outro.
	if com != 14 || sem != 14 {
		t.Errorf("pedidos ao executor com a tool aconselhar oferecida: %d (quer 14); sem: %d (quer 14)", com, sem)
	}
}

// O CUSTO POR CASO CUMPRIDO, calculado sobre observações escritas à mão.
func TestAOS528_CustoPorCasoCumpridoEContasPorBraco(t *testing.T) {
	b := aos528Bateria3(t)
	obs := func(caso, braco string, factos string, pedidos int, entrada, saida int64, conselho *ConsultasDoRun) Observacao {
		proibidos := ProibidosNenhum
		if factos == FactosAusentes {
			proibidos = ProibidosPresente
		}
		return Observacao{Caso: caso, No: b.Casos[0].Nos[0].ID, BracoDoConselheiro: braco, Tentativa: 1, Desfecho: DesfechoCumprido,
			Factos: factos, Proibidos: proibidos, Pedidos: pedidos, TokensDeEntrada: entrada, TokensDeSaida: saida, HTTP: []int{200}, Conselho: conselho}
	}
	// Os nós da 3.0.0 têm ids diferentes: cada observação leva o do seu caso.
	porCaso := map[string]string{}
	for _, c := range b.Casos {
		porCaso[c.ID] = c.Nos[0].ID
	}
	var lista []Observacao
	add := func(o Observacao) {
		o.No = porCaso[o.Caso]
		lista = append(lista, o)
	}
	// Braço sem conselheiro: X1 cumprido, X2 errado. 2 + 2 pedidos; 1 000 000 e 200 000 tokens.
	add(obs("X1", BracoSoExecutor, FactosPresentes, 2, 600_000, 100_000, nil))
	add(obs("X2", BracoSoExecutor, FactosAusentes, 2, 400_000, 100_000, nil))
	// Braço com conselheiro: X1 e X2 cumpridos; X2 consultou duas vezes, uma delas recusada pelo limite.
	add(obs("X1", BracoComConselheiro, FactosPresentes, 2, 500_000, 50_000, &ConsultasDoRun{}))
	add(obs("X2", BracoComConselheiro, FactosPresentes, 3, 500_000, 50_000, &ConsultasDoRun{Pedidas: 2, ComResposta: 1, RecusadasPeloLimite: 1, Pedidos: 1, HTTP: []int{200}, TokensDeEntrada: 100_000, TokensDeSaida: 20_000}))
	precos := PrecosDeclarados{
		Executor:    &Preco{EntradaMicroUSDPorMTok: 1_000_000, SaidaMicroUSDPorMTok: 5_000_000},
		Conselheiro: &Preco{EntradaMicroUSDPorMTok: 3_000_000, SaidaMicroUSDPorMTok: 15_000_000},
	}
	bracos := CalcularBracosDoConselheiro(b, []string{BracoSoExecutor, BracoComConselheiro}, precos, lista)
	so, com := bracos[0], bracos[1]
	// Sem conselheiro: 1 000 000 × 1 + 200 000 × 5 = 2 000 000 micro-USD; um caso cumprido.
	if !(fracao{1, 2}).confere(so.CasosCumpridos) || so.Executor.Pedidos != 4 || *so.Executor.MicroUSDEstimado != 2_000_000 ||
		*so.Conselheiro.MicroUSDEstimado != 0 || *so.MicroUSDTotal != 2_000_000 || *so.MicroUSDPorCasoCumprido != 2_000_000 {
		t.Errorf("braco sem conselheiro: %+v", so)
	}
	// Com conselheiro: executor 1 000 000 × 1 + 100 000 × 5 = 1 500 000; conselheiro 100 000 × 3 +
	// 20 000 × 15 = 600 000; total 2 100 000 por dois casos cumpridos = 1 050 000.
	if !(fracao{2, 2}).confere(com.CasosCumpridos) || com.Executor.Pedidos != 5 || *com.Executor.MicroUSDEstimado != 1_500_000 ||
		com.Conselheiro.Pedidos != 1 || *com.Conselheiro.MicroUSDEstimado != 600_000 || *com.MicroUSDTotal != 2_100_000 || *com.MicroUSDPorCasoCumprido != 1_050_000 {
		t.Errorf("braco com conselheiro: executor %+v, conselheiro %+v, total %v, por caso %v", com.Executor, com.Conselheiro, *com.MicroUSDTotal, *com.MicroUSDPorCasoCumprido)
	}
	if !(fracao{1, 2}).confere(com.TaxaDeConsulta) || com.Consultas != 2 || com.ConsultasPorRun == nil || *com.ConsultasPorRun != 1 ||
		com.ConsultasRecusadasPeloLimite != 1 || com.ConsultasComResposta != 1 {
		t.Errorf("consultas: taxa %d/%d, %d pedidas, %v por run", com.TaxaDeConsulta.Numerador, com.TaxaDeConsulta.Denominador, com.Consultas, com.ConsultasPorRun)
	}
	// Arredonda ao micro-USD mais próximo: 2 100 001 por dois casos é 1 050 001 (metade sobe).
	um, dois := int64(2_100_001), int64(2_100_000)
	if v := porCasoCumprido(&um, 2); *v != 1_050_001 {
		t.Errorf("2 100 001 / 2 = %d", *v)
	}
	if porCasoCumprido(&dois, 0) != nil || porCasoCumprido(nil, 2) != nil {
		t.Error("sem casos cumpridos ou sem preco, nao ha custo por caso")
	}
	// Sem preço do conselheiro, o braço com ele não tem total (o sem conselheiro tem).
	bracos = CalcularBracosDoConselheiro(b, []string{BracoSoExecutor, BracoComConselheiro}, PrecosDeclarados{Executor: precos.Executor}, lista)
	if bracos[0].MicroUSDTotal == nil || bracos[1].MicroUSDTotal != nil || bracos[1].MicroUSDPorCasoCumprido != nil {
		t.Error("sem o preco do conselheiro so o braco sem ele tem custo")
	}
	// Uma consulta sem resposta por causa passageira tira a passagem da amostra SEMPRE: o run que
	// acertou e o que errou, com o mesmo 503, saem os dois. Tirar só o que errou subia o braço.
	falhada := func() *ConsultasDoRun {
		return &ConsultasDoRun{Pedidas: 1, FalhadasPorCausaPassageira: 1, Pedidos: 1, HTTP: []int{503}}
	}
	lista = lista[:0]
	add(obs("X1", BracoComConselheiro, FactosPresentes, 2, 1, 1, falhada()))
	add(obs("X2", BracoComConselheiro, FactosAusentes, 2, 1, 1, falhada()))
	com = CalcularBracosDoConselheiro(b, []string{BracoComConselheiro}, precos, lista)[0]
	if com.Interrompidas != 2 || com.InterrompidasPorCausa[InterrompidoPeloConselheiro] != 2 || !(fracao{0, 0}).confere(com.CasosCumpridos) || com.TaxaDeConsulta.Denominador != 0 {
		t.Errorf("consultas sem resposta por 503, uma certa e uma errada: %d interrompidas %v, cumpridos %d/%d", com.Interrompidas, com.InterrompidasPorCausa, com.CasosCumpridos.Numerador, com.CasosCumpridos.Denominador)
	}
}

// O PIOR CASO declarado inclui as consultas e é imposto: no plano, no tecto do dia, e por run.
func TestAOS528_OPiorCasoIncluiAsConsultasEEImposto(t *testing.T) {
	b := aos528Bateria3(t)
	p := PlanoDoConselheiro(b, 2, 1, 3)
	exec, _ := p.PedidosMaximos(b)
	cons, _ := p.PedidosMaximosDoConselheiro(b)
	// Executor: 2 passagens × 2 braços × 7 nós × 3 tentativas × 8 turnos; conselheiro: 2 × 7 × 3 × 3.
	if exec != 672 || cons != 126 {
		t.Errorf("pior caso: executor %d (quer 672), conselheiro %d (quer 126)", exec, cons)
	}
	so := PlanoDoConselheiro(b, 1, 1, 0)
	if exec, _ := so.PedidosMaximos(b); exec != 168 {
		t.Errorf("so com o executor: %d, quer 168", exec)
	}
	if cons, _ := so.PedidosMaximosDoConselheiro(b); cons != 0 {
		t.Errorf("sem conselheiro: %d consultas previstas", cons)
	}
	// O tecto do dia tem de levar o executor E as consultas (no mesmo fornecedor, somados).
	dir := t.TempDir()
	contador, err := AbrirContador(filepath.Join(dir, "contador.json"), FornecedorFalso, Tectos{PedidosDia: 336 + 41}, nil, "", relogioFixo)
	if err != nil {
		t.Fatal(err)
	}
	defer contador.Fechar()
	a := novoAmbienteDoConselheiro(t, []Comportamento{ComportamentoConselhoSegue}, ConselhoCerto, 2, nil)
	irmao, err := contador.Irmao(FornecedorFalso, Tectos{PedidosDia: 336 + 41}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	a.conselheiro.contador = irmao
	_, err = Correr(context.Background(), CfgDaCorrida{
		Modo: ModoFalso, Plano: PlanoDoConselheiro(a.bateria, 1, 1, 2), Bateria: a.bateria, No: a.no, Contador: contador,
		Conselho: &CfgDoConselho{Conselheiro: a.conselheiro}, Relogio: relogioFixo,
	})
	if !errors.Is(err, ErrNaoCabe) || a.falso.Pedidos() != 0 || a.doConselho.Pedidos() != 0 {
		t.Fatalf("336 + 42 nao cabem em 377: err %v, pedidos %d e %d", err, a.falso.Pedidos(), a.doConselho.Pedidos())
	}
	// Com sonda, a do conselheiro conta no tecto DELE: noutro fornecedor, 42 consultas e a sonda não
	// cabem num tecto de 42.
	outro, err := contador.Irmao(FornecedorAnthropic, Tectos{PedidosDia: 42}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	a.conselheiro.contador = outro
	_, err = Correr(context.Background(), CfgDaCorrida{
		Modo: ModoFalso, Plano: PlanoDoConselheiro(a.bateria, 1, 1, 2), Bateria: a.bateria, No: a.no, Contador: contador, Sondar: true,
		Conselho: &CfgDoConselho{Conselheiro: a.conselheiro}, Relogio: relogioFixo,
	})
	if !errors.Is(err, ErrNaoCabe) || a.falso.Pedidos() != 0 || a.doConselho.Pedidos() != 0 {
		t.Fatalf("42 consultas e a sonda do conselheiro nao cabem em 42: err %v", err)
	}
	if err := (CfgDaCorrida{Plano: PlanoDoConselheiro(a.bateria, 1, 1, 2), Bateria: a.bateria, Contador: contador, Conselho: &CfgDoConselho{Conselheiro: a.conselheiro}}).caberDoConselheiro(1); err != nil {
		t.Errorf("sem sonda, 42 consultas cabem em 42: %v", err)
	}
	// Por run: a terceira consulta de um run com máximo de duas não sai.
	c, doConselho := aos528ConselheiroDireto(t, 2, nil)
	c.abrir(true)
	for i := 1; i <= 3; i++ {
		_, err := c.aconselhar(context.Background(), []byte(`{"pergunta":"qual?","contexto":"nada"}`))
		if (i <= 2) != (err == nil) {
			t.Errorf("consulta %d: err = %v", i, err)
		}
		if i == 3 && !errors.Is(err, errLimiteDeConsultas) {
			t.Errorf("a terceira consulta tinha de ser recusada pelo limite: %v", err)
		}
	}
	doRun, _ := c.fechar()
	if doConselho.Pedidos() != 2 || doRun.Pedidas != 3 || doRun.RecusadasPeloLimite != 1 || doRun.Pedidos != 2 || doRun.ComResposta != 2 {
		t.Errorf("limite por run: %d pedidos ao conselheiro, contagens %+v", doConselho.Pedidos(), doRun)
	}
	// Fora do braço com conselheiro, a tool não consulta (mesmo que lhe chegue uma chamada).
	c.abrir(false)
	if _, err := c.aconselhar(context.Background(), []byte(`{"pergunta":"qual?"}`)); !errors.Is(err, errConselheiroIndisponivel) || doConselho.Pedidos() != 2 {
		t.Errorf("fora do braco: err %v", err)
	}
	if doRun, _ := c.fechar(); doRun != nil {
		t.Error("fora do braco nao ha consultas a contar")
	}
	if _, err := NovoConselheiro(CfgDoConselheiro{BaseURL: "http://x/v1", MaxConsultas: MaxConsultasAceites + 1}); !errors.Is(err, ErrConselheiro) {
		t.Errorf("maximo de consultas acima do aceite: %v", err)
	}
}

// aos528ConselheiroDireto monta um conselheiro ligado a um conselheiro falso, sem nó.
func aos528ConselheiroDireto(t *testing.T, maxConsultas int, contador *Contador) (*Conselheiro, *ConselheiroFalso) {
	t.Helper()
	falso := &ConselheiroFalso{Modo: ConselhoCerto}
	srv := httptest.NewServer(falso)
	t.Cleanup(srv.Close)
	c, err := NovoConselheiro(CfgDoConselheiro{BaseURL: srv.URL + "/v1", Credencial: "c", MaxConsultas: maxConsultas, Contador: contador})
	if err != nil {
		t.Fatal(err)
	}
	return c, falso
}

// O tecto do fornecedor do conselheiro atingido a meio: a consulta não sai e a corrida pára.
func TestAOS528_OTectoDoConselheiroParaAConsulta(t *testing.T) {
	dir := t.TempDir()
	dono, err := AbrirContador(filepath.Join(dir, "contador.json"), FornecedorKimi, Tectos{PedidosDia: 100}, nil, "", relogioFixo)
	if err != nil {
		t.Fatal(err)
	}
	defer dono.Fechar()
	irmao, err := dono.Irmao(FornecedorAnthropic, Tectos{PedidosDia: 1}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	c, falso := aos528ConselheiroDireto(t, 2, irmao)
	c.abrir(true)
	if _, err := c.aconselhar(context.Background(), []byte(`{"pergunta":"um"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.aconselhar(context.Background(), []byte(`{"pergunta":"dois"}`)); !errors.Is(err, errTectoDoConselheiro) {
		t.Fatalf("a segunda consulta passou o tecto de 1: %v", err)
	}
	doRun, _ := c.fechar()
	if falso.Pedidos() != 1 || doRun.FalhadasPorCausaPassageira != 1 || doRun.Pedidos != 1 || c.paragemDaCorrida() != TerminouTecto {
		t.Errorf("tecto do conselheiro: %d pedidos, %+v, paragem %q", falso.Pedidos(), doRun, c.paragemDaCorrida())
	}
	// A conta foi para a linha do fornecedor do conselheiro, e não para a do contador dono.
	cru, _ := os.ReadFile(filepath.Join(dir, "contador.json"))
	var f ficheiroDoContador
	if err := json.Unmarshal(cru, &f); err != nil {
		t.Fatal(err)
	}
	dia := relogioFixo().Format("2006-01-02")
	if f.Dias[dia]["anthropic"].Pedidos != 1 || f.Dias[dia]["kimi"].Pedidos != 0 {
		t.Errorf("contador: %+v", f.Dias[dia])
	}
	// Fechar o irmão não remove a trava do dono.
	irmao.Fechar()
	if _, err := os.Stat(filepath.Join(dir, "contador.json"+sufixoDaTrava)); err != nil {
		t.Error("fechar o irmao removeu a trava do dono")
	}
	if _, err := dono.Irmao(FornecedorAnthropic, Tectos{PedidosDia: 1, TemUSD: true, MicroUSDDia: 1}, nil, "m"); !errors.Is(err, ErrSemPreco) {
		t.Errorf("irmao com tecto em dolares e sem preco: %v", err)
	}
}

// aos528LancadorDeDuasRotas faz de proxy efémero sem Docker: um servidor local que encaminha pelo
// `model` do pedido — o alias do conselheiro para o conselheiro falso, o resto para o executor
// falso. Guarda o pedido de proxy.
type aos528LancadorDeDuasRotas struct {
	t       *testing.T
	roteiro []Comportamento
	modo    string

	lancamentos int
	pedido      PedidoDeProxy
	falso       *ProviderFalso
	doConselho  *ConselheiroFalso
}

func (l *aos528LancadorDeDuasRotas) Lancar(_ context.Context, p PedidoDeProxy) (*ProxyVivo, error) {
	l.lancamentos++
	l.pedido = p
	l.falso, l.doConselho = NovoProviderFalso(l.roteiro), &ConselheiroFalso{Modo: l.modo}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+chaveMestraDeTeste {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var corpo struct {
			Model string `json:"model"`
		}
		cru, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(cru))
		_ = json.Unmarshal(cru, &corpo)
		if corpo.Model == AliasDoConselheiro {
			l.doConselho.ServeHTTP(w, r)
			return
		}
		l.falso.ServeHTTP(w, r)
	}))
	l.t.Cleanup(srv.Close)
	return NovoProxyVivo(srv.URL+"/v1", chaveMestraDeTeste, srv.Close), nil
}

// O CONSELHEIRO DE OUTRO FORNECEDOR, pela linha de comandos no modo real (com o lançador de
// teste): as contas e os tectos vão para o fornecedor certo, a segunda rota leva a chave dele, e
// as recusas dizem porquê.
func TestAOS528_Real_ConselheiroDeOutroFornecedor(t *testing.T) {
	chaves := escreverChaves(t, chavesDeTeste())
	pasta := filepath.Dir(chaves)
	precos := filepath.Join(pasta, "precos.json")
	if err := os.WriteFile(precos, []byte(`{"modelos":{"`+modeloAnthropicDeTeste+`":{"entrada_micro_usd_por_mtok":3000000,"saida_micro_usd_por_mtok":15000000},"`+modeloKimiDeTeste+`":{"entrada_micro_usd_por_mtok":1000000,"saida_micro_usd_por_mtok":2000000}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	lerContador := func() map[string]usoDoDia {
		t.Helper()
		cru, err := os.ReadFile(filepath.Join(pasta, "contador.json"))
		if err != nil {
			t.Fatal(err)
		}
		var f ficheiroDoContador
		if err := json.Unmarshal(cru, &f); err != nil {
			t.Fatal(err)
		}
		return f.Dias[relogioFixo().Format("2006-01-02")]
	}
	// Executor Kimi, conselheiro Anthropic (o único com tecto em dólares no ficheiro de teste).
	lanc := &aos528LancadorDeDuasRotas{t: t, roteiro: []Comportamento{ComportamentoConselhoSegue}, modo: ConselhoCerto}
	saida := filepath.Join(pasta, "r1")
	e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "kimi", "--experiencia", "conselheiro",
		"--conselheiro-fornecedor", "anthropic", "--precos", precos, "--saida", saida)
	if e.codigo != SaidaOK {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	c := lanc.pedido.Conselheiro
	if lanc.lancamentos != 1 || c == nil || c.Prefixo != "anthropic" || c.Modelo != modeloAnthropicDeTeste || c.Alias != AliasDoConselheiro ||
		c.apiKey != sentinelaChaveAnthropic || c.apiBase != "" || lanc.pedido.apiKey != sentinelaChaveKimi || lanc.pedido.Prefixo != "openai" {
		t.Fatalf("o lancador nao recebeu as duas rotas, cada uma com a sua chave: %+v", lanc.pedido)
	}
	// Kimi: 1 sonda + 35 pedidos do executor (7 runs de dois pedidos sem conselheiro, 7 de três com
	// ele: ler, consultar, responder). Anthropic: 1 sonda + 7 consultas.
	uso := lerContador()
	if uso["kimi"].Pedidos != int64(lanc.falso.Pedidos()) || uso["kimi"].Pedidos != 36 || uso["anthropic"].Pedidos != 8 || lanc.doConselho.Pedidos() != 8 {
		t.Errorf("contador: kimi %d (o falso do executor recebeu %d), anthropic %d (o conselheiro recebeu %d)", uso["kimi"].Pedidos, lanc.falso.Pedidos(), uso["anthropic"].Pedidos, lanc.doConselho.Pedidos())
	}
	if uso["anthropic"].MicroUSD <= 0 || uso["kimi"].MicroUSD != 0 {
		t.Errorf("o gasto estimado do conselheiro tinha de contar no fornecedor dele: %+v", uso)
	}
	var r Relatorio
	achados, _ := filepath.Glob(filepath.Join(saida, "*.json"))
	cru, _ := os.ReadFile(achados[0])
	if err := json.Unmarshal(cru, &r); err != nil {
		t.Fatal(err)
	}
	k := r.Taxas.Conselheiro
	if k == nil || k.Rota == nil || k.Rota.Fornecedor != "anthropic" || k.Pedidos.Enviados != 8 || *k.Pedidos.GastosHoje != 8 || k.Sonda == nil || k.Sonda.Resultado != SondaOK ||
		k.PiorCasoPorFornecedor["kimi"] != 337 || k.PiorCasoPorFornecedor["anthropic"] != 43 || r.Pedidos.Enviados != 36 || k.Custo.MicroUSDHoje == nil {
		t.Errorf("bloco do conselheiro: %+v; pedidos do executor %d", k, r.Pedidos.Enviados)
	}
	if !strings.Contains(e.stderr, "CONSELHEIRO — fornecedor anthropic") || !strings.Contains(e.stderr, "PIOR CASO POR FORNECEDOR (com as sondas): kimi=337 (executor), anthropic=43 (conselheiro)") {
		t.Errorf("o pior caso por fornecedor tinha de ser dito antes de enviar:\n%s", e.stderr)
	}
	verSemFugas(t, "conselheiro de outro fornecedor", tudoOQueFoiEscrito(t, e, saida), []string{sentinelaChaveKimi, sentinelaBaseKimi, sentinelaChaveAnthropic, chaveMestraDeTeste, SentinelaDoConselho})
	// Um contador que desapareça reconstrói-se dos relatórios de hoje: os pedidos do conselheiro
	// contam no fornecedor DELE.
	dia := relogioFixo().Format("2006-01-02")
	if p, _, n := UsoDosRelatoriosDeHoje([]string{saida}, FornecedorAnthropic, dia); p != 8 || n != 1 {
		t.Errorf("reconstrucao, anthropic: %d pedidos em %d relatorio(s), quer 8 em 1", p, n)
	}
	if p, _, n := UsoDosRelatoriosDeHoje([]string{saida}, FornecedorKimi, dia); p != 36 || n != 1 {
		t.Errorf("reconstrucao, kimi: %d pedidos em %d relatorio(s), quer 36 em 1", p, n)
	}

	// O MESMO fornecedor, outro modelo: as duas contas na mesma linha, e o tecto é um só.
	lanc = &aos528LancadorDeDuasRotas{t: t, roteiro: []Comportamento{ComportamentoConselhoSegue}, modo: ConselhoCerto}
	e = executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "kimi", "--experiencia", "conselheiro",
		"--conselheiro-fornecedor", "kimi", "--conselheiro-modelo", segundoModeloKimi, "--saida", filepath.Join(pasta, "r2"))
	if e.codigo != SaidaOK || lanc.pedido.Conselheiro.Modelo != segundoModeloKimi || lanc.pedido.Conselheiro.apiBase != sentinelaBaseKimi {
		t.Fatalf("mesmo fornecedor: codigo %d\n%s", e.codigo, e.stderr)
	}
	if uso := lerContador(); uso["kimi"].Pedidos != 36+44 {
		t.Errorf("mesmo fornecedor: kimi %d pedidos, quer %d (os 36 de antes, 36 do executor e 8 do conselheiro)", uso["kimi"].Pedidos, 36+44)
	}

	// Recusas, antes de lançar o proxy e de enviar seja o que for.
	for nome, caso := range map[string]struct {
		args  []string
		saida int
		diz   string
	}{
		"modelo do conselheiro fora da lista": {[]string{"--conselheiro-fornecedor", "kimi", "--conselheiro-modelo", "modelo-que-nao-esta"}, SaidaRecusada, "conselheiro: banco-ensaio: ficheiro de chaves: o campo KIMI_MODELOS esta nao esta na lista do ficheiro"},
		"fornecedor do conselheiro sem chave": {[]string{"--conselheiro-fornecedor", "openrouter"}, SaidaRecusada, "conselheiro: banco-ensaio: ficheiro de chaves: o campo OPENROUTER_API_KEY esta em falta"},
		"fornecedor desconhecido":             {[]string{"--conselheiro-fornecedor", "outro"}, SaidaUso, "--conselheiro-fornecedor"},
		"tecto em dolares sem preco":          {[]string{"--conselheiro-fornecedor", "anthropic"}, SaidaRecusada, "conselheiro (anthropic)"},
		"conselheiro falso no modo real":      {[]string{"--conselheiro-falso", "certo"}, SaidaUso, "--conselheiro-falso e so do modo falso"},
		"modelo sem fornecedor":               {[]string{"--conselheiro-modelo", segundoModeloKimi}, SaidaUso, "--conselheiro-modelo exige --conselheiro-fornecedor"},
	} {
		lanc := &aos528LancadorDeDuasRotas{t: t}
		args := append([]string{"real", "--chaves", chaves, "--fornecedor", "kimi", "--experiencia", "conselheiro", "--saida", filepath.Join(pasta, "r3")}, caso.args...)
		e := executar(t, Ambiente{Lancador: lanc}, args...)
		if e.codigo != caso.saida || lanc.lancamentos != 0 || !strings.Contains(e.stderr, caso.diz) {
			t.Errorf("%s: codigo %d (quer %d), lancamentos %d\n%s", nome, e.codigo, caso.saida, lanc.lancamentos, e.stderr)
		}
	}

	// O TECTO DO DIA DO CONSELHEIRO esgotado: a corrida não arranca, e a mensagem diz de quem é.
	campos := chavesDeTeste()
	campos[CampoTectoPedidosAnthro] = "20"
	chaves = escreverChaves(t, campos)
	lanc = &aos528LancadorDeDuasRotas{t: t}
	e = executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "kimi", "--experiencia", "conselheiro",
		"--conselheiro-fornecedor", "anthropic", "--precos", precos, "--saida", filepath.Join(filepath.Dir(chaves), "r4"))
	if e.codigo != SaidaRecusada || lanc.lancamentos != 0 || !strings.Contains(e.stderr, "o pior caso do conselheiro (42 consultas e 1 sonda, no fornecedor anthropic) nao cabe") {
		t.Errorf("tecto do conselheiro: codigo %d, lancamentos %d\n%s", e.codigo, lanc.lancamentos, e.stderr)
	}
	// No MESMO fornecedor o CLI soma os dois piores casos antes de lançar o proxy: 337 do executor e
	// 22 do conselheiro (uma consulta por run) não cabem em 358.
	campos = chavesDeTeste()
	campos[CampoTectoPedidosKimi] = "358"
	lanc = &aos528LancadorDeDuasRotas{t: t}
	e = executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", escreverChaves(t, campos), "--fornecedor", "kimi", "--experiencia", "conselheiro",
		"--conselheiro-fornecedor", "kimi", "--max-consultas", "1", "--saida", t.TempDir())
	if e.codigo != SaidaRecusada || lanc.lancamentos != 0 || !strings.Contains(e.stderr, "o pior caso do conselheiro (21 consultas e 1 sonda, no fornecedor kimi) nao cabe") {
		t.Errorf("mesmo fornecedor, tecto 358: codigo %d, lancamentos %d\n%s", e.codigo, lanc.lancamentos, e.stderr)
	}
	// --so-plano diz o pior caso por fornecedor e não lança nada.
	e = executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", escreverChaves(t, chavesDeTeste()), "--fornecedor", "kimi", "--experiencia", "conselheiro",
		"--conselheiro-fornecedor", "kimi", "--max-consultas", "1", "--so-plano")
	if e.codigo != SaidaOK || lanc.lancamentos != 0 || !strings.Contains(e.stderr, "kimi=359 (executor 337 e conselheiro 22)") {
		t.Errorf("--so-plano: codigo %d\n%s", e.codigo, e.stderr)
	}
}

// A FUGA: a pergunta e o conselho não aparecem no relatório, no resumo nem nas saídas; com
// `--guardar-respostas` ficam no ficheiro de respostas do run, e só lá.
func TestAOS528_Fuga_PerguntaEConselhoSoNasRespostasGuardadas(t *testing.T) {
	b := aos528Bateria3(t)
	pergunta := b.Casos[0].Nos[0].Objectivo
	for _, guardar := range []bool{false, true} {
		saida := t.TempDir()
		args := []string{"falso", "--experiencia", "conselheiro", "--conselheiro-falso", "certo", "--saida", saida}
		if guardar {
			args = append(args, "--guardar-respostas")
		}
		e := executar(t, Ambiente{}, args...)
		if e.codigo != SaidaOK {
			t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
		}
		relatorios, _ := filepath.Glob(filepath.Join(saida, "ensaio-*"))
		semRespostas := e.stdout + "\n" + e.stderr
		for _, f := range relatorios {
			if info, _ := os.Stat(f); info.IsDir() {
				continue
			}
			cru, _ := os.ReadFile(f)
			semRespostas += string(cru)
		}
		for _, marca := range []string{SentinelaDoConselho, pergunta, "Find the boiler that failed", SentinelaDeTexto, "Context:"} {
			if strings.Contains(semRespostas, marca) {
				t.Errorf("guardar=%v: %q apareceu no relatorio, no resumo ou nas saidas", guardar, marca)
			}
		}
		guardadas, _ := filepath.Glob(filepath.Join(saida, PrefixoDaPastaDasRespostas+"*", "*.json"))
		if !guardar {
			if len(guardadas) != 0 {
				t.Errorf("sem a opcao ficaram %d ficheiros de respostas", len(guardadas))
			}
			continue
		}
		comConsulta := 0
		for _, f := range guardadas {
			cru, _ := os.ReadFile(f)
			var r RespostaDoRun
			if json.Unmarshal(cru, &r) != nil || filepath.Base(f) == NomeDoIndiceDasRespostas {
				continue
			}
			if r.BracoDoConselheiro == "" {
				t.Errorf("%s: sem o braco do conselheiro", f)
			}
			for _, c := range r.Consultas {
				comConsulta++
				if !strings.Contains(c.Conselho, SentinelaDoConselho) || c.Pergunta == "" || c.Contexto == "" || r.BracoDoConselheiro != BracoComConselheiro {
					t.Errorf("%s: consulta guardada incompleta", f)
				}
			}
		}
		if len(guardadas) != 15 || comConsulta != 7 {
			t.Errorf("respostas guardadas: %d ficheiros (quer 14 runs e o indice), %d consultas (quer 7)", len(guardadas), comConsulta)
		}
	}
}

// NA LINHA DE COMANDOS: as omissões, o resumo, e o que a experiência nova não deixa mudar nas outras.
func TestAOS528_NaLinhaDeComandos(t *testing.T) {
	relatorioDe := func(e execucao) Relatorio {
		t.Helper()
		achados, _ := filepath.Glob(filepath.Join(e.saida, "ensaio-falso-*.json"))
		if len(achados) != 1 {
			t.Fatalf("quer um relatorio em %s, ha %d (stderr: %s)", e.saida, len(achados), e.stderr)
		}
		cru, _ := os.ReadFile(achados[0])
		var r Relatorio
		if err := json.Unmarshal(cru, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	precos := filepath.Join(t.TempDir(), "precos.json")
	if err := os.WriteFile(precos, []byte(`{"modelos":{"modelo-falso-do-banco":{"entrada_micro_usd_por_mtok":1000000,"saida_micro_usd_por_mtok":5000000},"modelo-falso-do-conselheiro":{"entrada_micro_usd_por_mtok":3000000,"saida_micro_usd_por_mtok":15000000}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Com o conselheiro falso certo e o roteiro por omissão (b): o braço com ele cumpre, o outro não.
	e := executar(t, Ambiente{}, "falso", "--experiencia", "conselheiro", "--conselheiro-falso", "certo", "--precos", precos, "--saida", t.TempDir())
	r := relatorioDe(e)
	if e.codigo != SaidaOK || r.Digests.Bateria != aos528DigestDaBateria3 || r.Pedidos.PrevistosMax != 336 || r.Taxas.Conselheiro.Pedidos.PrevistosMax != 42 ||
		r.Taxas.Conselheiro.PiorCasoPorFornecedor["falso"] != 378 || r.Taxas.Conselheiro.MaxConsultasPorRun != 2 {
		t.Fatalf("conselheiro em falso: saida %d, previstos %d\n%s", e.codigo, r.Pedidos.PrevistosMax, e.stderr)
	}
	if com := r.aos528Braco(t, BracoComConselheiro); !(fracao{7, 7}).confere(com.CasosCumpridos) || com.MicroUSDPorCasoCumprido == nil {
		t.Errorf("braco com conselheiro: %+v", com)
	}
	if !strings.Contains(e.stdout, "CONSELHEIRO (bateria 3.0.0; sem veredicto;") || !strings.Contains(e.stdout, "executor+conselheiro  7/7") ||
		!strings.Contains(e.stdout, "so-executor           0/7") || strings.Contains(e.stdout, "QUALIFICACAO DO MODELO") {
		t.Errorf("o resumo tinha de trazer a tabela por braco:\n%s", e.stdout)
	}
	// Sem conselheiro: só o braço do executor (a corrida do tecto), sem consultas previstas.
	e = executar(t, Ambiente{}, "falso", "--experiencia", "conselheiro", "--roteiro", "conselho_cumpre", "--silencioso", "--saida", t.TempDir())
	r = relatorioDe(e)
	if e.codigo != SaidaOK || r.Pedidos.PrevistosMax != 168 || len(r.Taxas.Conselheiro.Bracos) != 1 || r.Taxas.Conselheiro.Rota != nil || r.Taxas.Conselheiro.Pedidos != nil ||
		!(fracao{7, 7}).confere(r.Taxas.Conselheiro.Bracos[0].CasosCumpridos) {
		t.Errorf("so com o executor: saida %d, %+v", e.codigo, r.Taxas.Conselheiro)
	}
	// Determinista: duas corridas iguais dão o mesmo relatório.
	a := executar(t, Ambiente{}, "falso", "--experiencia", "conselheiro", "--conselheiro-falso", "errado", "--amostras", "2", "--silencioso", "--saida", t.TempDir())
	b := executar(t, Ambiente{}, "falso", "--experiencia", "conselheiro", "--conselheiro-falso", "errado", "--amostras", "2", "--silencioso", "--saida", t.TempDir())
	ra, rb := relatorioDe(a), relatorioDe(b)
	ja, _ := json.Marshal(ra)
	jb, _ := json.Marshal(rb)
	if string(ja) != string(jb) || ra.Taxas.Conselheiro.aos528BracoSemT(BracoComConselheiro).CasosCumpridos.Denominador != 14 {
		t.Error("a experiencia conselheiro nao e determinista, ou nao correu duas passagens")
	}
	// O subcomando `bateria` mostra a bateria da experiência; as outras mostram as suas.
	if e := executar(t, Ambiente{}, "bateria", "--experiencia", "conselheiro"); e.codigo != SaidaOK || !strings.HasPrefix(e.stdout, "bateria 3.0.0  digest "+aos528DigestDaBateria3+"\n") || strings.Count(e.stdout, "\n") != 8 {
		t.Errorf("bateria --experiencia conselheiro: %d\n%s", e.codigo, e.stdout)
	}
	// O que não se aceita: recusado antes de correr, sem relatório.
	for nome, args := range map[string][]string{
		"conselheiro noutra experiencia":   {"falso", "--conselheiro-falso", "certo"},
		"max-consultas noutra experiencia": {"falso", "--experiencia", "degraus", "--max-consultas", "2"},
		"conselheiro no modo proxy":        {"proxy", "--experiencia", "conselheiro", "--conselheiro-falso", "certo"},
		"fornecedor no modo falso":         {"falso", "--experiencia", "conselheiro", "--conselheiro-fornecedor", "kimi"},
		"modo do falso desconhecido":       {"falso", "--experiencia", "conselheiro", "--conselheiro-falso", "talvez"},
		"max-consultas sem conselheiro":    {"falso", "--experiencia", "conselheiro", "--max-consultas", "2"},
		"max-consultas a mais":             {"falso", "--experiencia", "conselheiro", "--conselheiro-falso", "certo", "--max-consultas", "6"},
		"outro braco":                      {"falso", "--experiencia", "conselheiro", "--braco", "B"},
		"o provider falso do estado":       {"falso", "--experiencia", "conselheiro", "--estado", "exige"},
		"comportamento dos degraus":        {"falso", "--experiencia", "conselheiro", "--roteiro", "degrau_cumpre"},
		"conselho fora da experiencia":     {"falso", "--roteiro", "conselho_segue"},
		"conselho nos degraus":             {"falso", "--experiencia", "degraus", "--roteiro", "conselho_cumpre"},
		"precos noutra experiencia":        {"falso", "--precos", precos},
	} {
		pasta := t.TempDir()
		e := executar(t, Ambiente{}, append(args, "--saida", pasta)...)
		ficheiros, _ := os.ReadDir(pasta)
		if e.codigo != SaidaUso || len(ficheiros) != 0 {
			t.Errorf("%s: saida %d com %d ficheiro(s), quer %d e nenhum\n%s", nome, e.codigo, len(ficheiros), SaidaUso, e.stderr)
		}
	}
}

// aos528BracoSemT é [Relatorio.aos528Braco] sobre o bloco, sem testing.T.
func (c *RelatorioDoConselheiro) aos528BracoSemT(nome string) BracoDoConselheiro {
	for _, b := range c.Bracos {
		if b.Braco == nome {
			return b
		}
	}
	return BracoDoConselheiro{}
}

// O QUE NÃO MUDA: as tools do banco, o roteiro por omissão, o nó sem conselheiro (não oferece nem
// regista a tool) e o digest da configuração das experiências de antes.
func TestAOS528_AsExperienciasDeAntesNaoMudam(t *testing.T) {
	if len(ToolsDoBanco()) != 2 || EscreverRoteiro(RoteiroPorOmissao()) != "cumpre,cumpre,texto,cumpre,vazia,cumpre,cortada,sem_factos,erro_500,rejeita_segundo" {
		t.Error("as tools do banco ou o roteiro por omissao mudaram")
	}
	for _, t1 := range ToolsDoBanco() {
		if t1.Nome == ToolAconselhar {
			t.Error("a tool do conselheiro nao pode estar nas tools do banco (mudava o digest da configuracao)")
		}
	}
	// Um nó sem conselheiro: a tool não é oferecida em pedido nenhum, nem está registada no RM.
	a := novoAmbienteFalso(t, RoteiroPorOmissao(), nil)
	a.correr(t, PlanoDaBateria(a.bateria, BracoA, 1), nil)
	for _, corpo := range a.falso.Corpos() {
		if strings.Contains(string(corpo), ToolAconselhar) {
			t.Fatal("um no sem conselheiro ofereceu a tool aconselhar")
		}
	}
	if a.no.conselheiro != nil {
		t.Error("um no sem conselheiro tem conselheiro")
	}
	// O digest da configuração de um plano de antes não depende do código do conselheiro.
	um, _ := CarregarBateria()
	p := PlanoDaBateria(um, BracoA, 1)
	antes := digestDaConfiguracao(p, nil, nil)
	p.BracosDoConselheiro, p.MaxConsultas = nil, 0
	if digestDaConfiguracao(p, nil, nil) != antes {
		t.Error("o digest da configuracao mudou com os campos do conselheiro vazios")
	}
	// Uma corrida da experiência `conselheiro` não tem a configuração do conselho noutra.
	cfg := CfgDaCorrida{Plano: p, Bateria: um, No: a.no, Conselho: &CfgDoConselho{}}
	if err := cfg.validarConselho(); !errors.Is(err, ErrPlano) {
		t.Errorf("conselho numa experiencia de antes: %v", err)
	}
}
