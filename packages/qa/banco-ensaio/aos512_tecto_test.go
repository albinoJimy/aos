package bancoensaio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// AOS-512 — OS TECTOS DO DIA E O CONTADOR PERSISTENTE.

// lerContador lê o ficheiro do contador directamente do disco, à parte do código sob teste.
func lerContador(t *testing.T, caminho string) map[string]map[string]usoDoDia {
	t.Helper()
	cru, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Versao int                            `json:"versao"`
		Dias   map[string]map[string]usoDoDia `json:"dias"`
	}
	if err := json.Unmarshal(cru, &f); err != nil {
		t.Fatalf("contador ilegivel: %v", err)
	}
	return f.Dias
}

const diaDosTestes = "2026-10-08"

func abrirContadorDeTeste(t *testing.T, tecto int64) (*Contador, string) {
	t.Helper()
	caminho := filepath.Join(t.TempDir(), "contador.json")
	c, err := AbrirContador(caminho, FornecedorFalso, Tectos{PedidosDia: tecto}, nil, "", relogioFixo)
	if err != nil {
		t.Fatal(err)
	}
	return c, caminho
}

// planoDeUmPedido devolve um plano em que cada run faz exactamente um pedido (T3, uma tentativa).
func planoDeUmPedido(b *Bateria, amostras int) Plano {
	p := PlanoDaBateria(b, BracoA, amostras)
	p.Casos, p.Tentativas, p.MaxTurnos = []string{"T3"}, 1, 1
	return p
}

// Tecto de 3 e cinco runs de um pedido ⇒ EXACTAMENTE 3 pedidos chegam ao provider falso.
func TestAOS512_Tecto_TresDeCinco_SoSaemTres(t *testing.T) {
	contador, caminho := abrirContadorDeTeste(t, 3)
	a := novoAmbienteFalso(t, []Comportamento{ComportamentoCumpre}, contador)
	caso, _ := a.bateria.Caso("T3")
	doc, _ := a.bateria.Documento(caso.Nos[0].EntradaDoc)
	var desfechos []string
	for i := 0; i < 5; i++ {
		o, _ := a.no.Correr(context.Background(), PedidoDeRun{Caso: caso, No: caso.Nos[0], Braco: BracoA, Amostra: i, Tentativa: 1, Entrada: doc, OrigemEntrada: "bateria", MaxTurnos: 1})
		desfechos = append(desfechos, o.Desfecho)
	}
	if a.falso.Pedidos() != 3 {
		t.Fatalf("chegaram %d pedidos ao provider falso, quer exactamente 3", a.falso.Pedidos())
	}
	quer := []string{DesfechoCumprido, DesfechoCumprido, DesfechoCumprido, DesfechoTectoAtingido, DesfechoTectoAtingido}
	for i := range quer {
		if desfechos[i] != quer[i] {
			t.Fatalf("desfechos = %v, quer %v", desfechos, quer)
		}
	}
	if got := lerContador(t, caminho)[diaDosTestes]["falso"].Pedidos; got != 3 {
		t.Errorf("o contador em disco diz %d, quer 3", got)
	}
	if !contador.Esgotado() {
		t.Error("o contador tinha de se declarar esgotado")
	}
}

// O pedido é contado ANTES de ser enviado: quando o pedido n chega ao provider, o ficheiro em
// disco já diz n.
func TestAOS512_Tecto_OPedidoContaAntesDeSair(t *testing.T) {
	contador, caminho := abrirContadorDeTeste(t, 100)
	b, err := CarregarBateria()
	if err != nil {
		t.Fatal(err)
	}
	falso := NovoProviderFalso([]Comportamento{ComportamentoCumpre})
	var recebidos atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := recebidos.Add(1)
		if got := lerContador(t, caminho)[diaDosTestes]["falso"].Pedidos; got != n {
			t.Errorf("ao chegar o pedido %d, o contador em disco dizia %d: o pedido saiu antes de ser contado", n, got)
		}
		falso.ServeHTTP(w, r)
	}))
	defer srv.Close()
	no, err := NovoNoDeEnsaio(context.Background(), CfgDoNo{Bateria: b, BaseURL: srv.URL + "/v1", Credencial: "c", Contador: contador})
	if err != nil {
		t.Fatal(err)
	}
	defer no.Fechar()
	r, err := Correr(context.Background(), CfgDaCorrida{Modo: ModoFalso, Plano: PlanoDaBateria(b, BracoA, 1), Bateria: b, No: no, Contador: contador, Relogio: relogioFixo})
	if err != nil {
		t.Fatal(err)
	}
	if recebidos.Load() != 12 || r.Pedidos.Enviados != 12 || *r.Pedidos.GastosHoje != 12 || *r.Pedidos.RestantesHoje != 88 || *r.Pedidos.TectoDoDia != 100 {
		t.Errorf("recebidos %d; relatorio: enviados %d, gastos %d, restantes %d", recebidos.Load(), r.Pedidos.Enviados, *r.Pedidos.GastosHoje, *r.Pedidos.RestantesHoje)
	}
}

// FAIL-CLOSED: um contador que deixa de se ler a meio da corrida não deixa sair mais nenhum
// pedido. Não recomeça do zero, nem quando o ficheiro desaparece.
func TestAOS512_Tecto_ContadorIlegivelNaoEnvia(t *testing.T) {
	for nome, estragar := range map[string]func(string) error{
		"corrompido":   func(c string) error { return os.WriteFile(c, []byte("{isto nao e json"), 0o600) },
		"apagado":      func(c string) error { return os.Remove(c) },
		"outra versao": func(c string) error { return os.WriteFile(c, []byte(`{"versao":99,"dias":{}}`), 0o600) },
		"sem dias":     func(c string) error { return os.WriteFile(c, []byte(`{"versao":1}`), 0o600) },
		"contagem negativa": func(c string) error {
			return os.WriteFile(c, []byte(`{"versao":1,"dias":{"2026-10-08":{"falso":{"pedidos":-5}}}}`), 0o600)
		},
	} {
		t.Run(nome, func(t *testing.T) {
			contador, caminho := abrirContadorDeTeste(t, 100)
			a := novoAmbienteFalso(t, []Comportamento{ComportamentoCumpre}, contador)
			caso, _ := a.bateria.Caso("T3")
			doc, _ := a.bateria.Documento(caso.Nos[0].EntradaDoc)
			pedido := PedidoDeRun{Caso: caso, No: caso.Nos[0], Braco: BracoA, Tentativa: 1, Entrada: doc, OrigemEntrada: "bateria", MaxTurnos: 1}
			if o, _ := a.no.Correr(context.Background(), pedido); o.Desfecho != DesfechoCumprido || a.falso.Pedidos() != 1 {
				t.Fatalf("com o contador bom o pedido tinha de sair: %s", o.Desfecho)
			}
			if err := estragar(caminho); err != nil {
				t.Fatal(err)
			}
			pedido.Amostra = 1
			o, _ := a.no.Correr(context.Background(), pedido)
			if a.falso.Pedidos() != 1 {
				t.Fatalf("com o contador inutilizavel sairam %d pedidos a mais", a.falso.Pedidos()-1)
			}
			if o.Desfecho != DesfechoContador {
				t.Errorf("desfecho = %s, quer %s", o.Desfecho, DesfechoContador)
			}
			if err := contador.Reservar(); !errors.Is(err, ErrContador) {
				t.Errorf("Reservar sobre um contador inutilizavel: %v, quer ErrContador", err)
			}
			// A corrida inteira pára, com a causa no relatório.
			r, err := Correr(context.Background(), CfgDaCorrida{Modo: ModoFalso, Plano: planoDeUmPedido(a.bateria, 2), Bateria: a.bateria, No: a.no, Contador: contador, Relogio: relogioFixo})
			if err == nil && r.Terminou != TerminouContador {
				t.Errorf("a corrida tinha de recusar ou parar por causa do contador; terminou=%s", r.Terminou)
			}
			if a.falso.Pedidos() != 1 {
				t.Errorf("a corrida enviou pedidos com o contador inutilizavel")
			}
		})
	}
}

// Um contador que EXISTE e não se lê recusa o arranque; só um que ainda não existe é criado.
func TestAOS512_Tecto_AbrirRecusaOQueNaoSeLe(t *testing.T) {
	dir := t.TempDir()
	novo := filepath.Join(dir, "sub", "contador.json")
	if _, err := AbrirContador(novo, FornecedorKimi, Tectos{PedidosDia: 10}, nil, "m", relogioFixo); err != nil {
		t.Fatalf("o primeiro uso tinha de criar o contador: %v", err)
	}
	if dias := lerContador(t, novo); len(dias) != 0 {
		t.Errorf("um contador novo comeca vazio: %v", dias)
	}
	estragado := filepath.Join(dir, "estragado.json")
	if err := os.WriteFile(estragado, []byte("lixo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AbrirContador(estragado, FornecedorKimi, Tectos{PedidosDia: 10}, nil, "m", relogioFixo); !errors.Is(err, ErrContador) {
		t.Errorf("contador corrompido: err = %v, quer ErrContador", err)
	}
	if cru, _ := os.ReadFile(estragado); string(cru) != "lixo" {
		t.Error("um contador corrompido foi reescrito: tinha de ficar como estava")
	}
	if _, err := AbrirContador("", FornecedorKimi, Tectos{PedidosDia: 10}, nil, "m", relogioFixo); !errors.Is(err, ErrContador) {
		t.Errorf("sem caminho: err = %v, quer ErrContador", err)
	}
}

// Não há tecto por omissão: ausente, zero ou negativo recusa. E um tecto em dólares sem preço
// declarado para o modelo recusa também.
func TestAOS512_Tecto_SemTectoOuSemPrecoRecusa(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "contador.json")
	for _, tecto := range []int64{0, -1} {
		if _, err := AbrirContador(caminho, FornecedorKimi, Tectos{PedidosDia: tecto}, nil, "m", relogioFixo); !errors.Is(err, ErrSemTecto) {
			t.Errorf("tecto %d: err = %v, quer ErrSemTecto", tecto, err)
		}
	}
	if _, err := os.Stat(caminho); !errors.Is(err, os.ErrNotExist) {
		t.Error("um arranque recusado nao pode criar o contador")
	}
	comUSD := Tectos{PedidosDia: 10, MicroUSDDia: 5_000_000, TemUSD: true}
	precos := &TabelaDePrecos{Modelos: map[string]Preco{
		"com-preco":  {EntradaMicroUSDPorMTok: 3_000_000, SaidaMicroUSDPorMTok: 15_000_000},
		"preco-zero": {EntradaMicroUSDPorMTok: 0, SaidaMicroUSDPorMTok: 15_000_000},
	}}
	for nome, tabela := range map[string]*TabelaDePrecos{"sem tabela": nil, "tabela sem o modelo": precos} {
		if _, err := AbrirContador(caminho, FornecedorAnthropic, comUSD, tabela, "sem-preco", relogioFixo); !errors.Is(err, ErrSemPreco) {
			t.Errorf("%s: err = %v, quer ErrSemPreco", nome, err)
		}
	}
	if _, err := AbrirContador(caminho, FornecedorAnthropic, comUSD, precos, "preco-zero", relogioFixo); !errors.Is(err, ErrSemPreco) {
		t.Errorf("preco zero: err = %v, quer ErrSemPreco", err)
	}
	if _, err := AbrirContador(caminho, FornecedorAnthropic, Tectos{PedidosDia: 10, TemUSD: true}, precos, "com-preco", relogioFixo); !errors.Is(err, ErrSemTecto) {
		t.Errorf("tecto em dolares a zero: err = %v, quer ErrSemTecto", err)
	}
	if _, err := AbrirContador(caminho, FornecedorAnthropic, comUSD, precos, "com-preco", relogioFixo); err != nil {
		t.Errorf("com tecto e preco tinha de abrir: %v", err)
	}
}

// O contador sobrevive a reinícios, é por fornecedor e por dia (UTC).
func TestAOS512_Tecto_PersistePorFornecedorEPorDia(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "contador.json")
	agora := relogioFixo()
	relogio := func() time.Time { return agora }
	abrir := func(f Fornecedor) *Contador {
		c, err := AbrirContador(caminho, f, Tectos{PedidosDia: 3}, nil, "m", relogio)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	kimi := abrir(FornecedorKimi)
	for i := 0; i < 2; i++ {
		if err := kimi.Reservar(); err != nil {
			t.Fatal(err)
		}
	}
	// «Reinício»: outro Contador sobre o mesmo ficheiro vê os 2 e só deixa sair mais 1.
	outro := abrir(FornecedorKimi)
	if err := outro.Reservar(); err != nil {
		t.Fatalf("o terceiro pedido cabia: %v", err)
	}
	if err := outro.Reservar(); !errors.Is(err, ErrTectoAtingido) {
		t.Fatalf("o quarto pedido: err = %v, quer ErrTectoAtingido", err)
	}
	if err := kimi.Reservar(); !errors.Is(err, ErrTectoAtingido) {
		t.Fatalf("o primeiro contador tambem tinha de ver o tecto: %v", err)
	}
	// Outro fornecedor tem a sua contagem.
	if err := abrir(FornecedorAnthropic).Reservar(); err != nil {
		t.Fatalf("o tecto e por fornecedor: %v", err)
	}
	// 23:59:59 UTC ainda é o mesmo dia; um segundo depois é outro.
	agora = time.Date(2026, 10, 8, 23, 59, 59, 0, time.UTC)
	if err := kimi.Reservar(); !errors.Is(err, ErrTectoAtingido) {
		t.Fatalf("ainda no mesmo dia UTC: %v", err)
	}
	agora = agora.Add(time.Second)
	if err := kimi.Reservar(); err != nil {
		t.Fatalf("no dia seguinte o tecto recomeca: %v", err)
	}
	dias := lerContador(t, caminho)
	if dias["2026-10-08"]["kimi"].Pedidos != 3 || dias["2026-10-08"]["anthropic"].Pedidos != 1 || dias["2026-10-09"]["kimi"].Pedidos != 1 {
		t.Errorf("contador em disco: %v", dias)
	}
}

// Antes de começar, a corrida calcula quantos pedidos pode fazer e recusa se não couber.
func TestAOS512_Tecto_CorridaQueNaoCabeNaoArranca(t *testing.T) {
	contador, _ := abrirContadorDeTeste(t, 4)
	a := novoAmbienteFalso(t, []Comportamento{ComportamentoCumpre}, contador)
	// Cinco runs de um pedido precisam de 5; o tecto é 4.
	_, err := Correr(context.Background(), CfgDaCorrida{Modo: ModoFalso, Plano: planoDeUmPedido(a.bateria, 5), Bateria: a.bateria, No: a.no, Contador: contador, Relogio: relogioFixo})
	if !errors.Is(err, ErrNaoCabe) {
		t.Fatalf("err = %v, quer ErrNaoCabe", err)
	}
	if a.falso.Pedidos() != 0 {
		t.Fatalf("uma corrida recusada enviou %d pedidos", a.falso.Pedidos())
	}
	// Quatro cabem — à justa.
	r, err := Correr(context.Background(), CfgDaCorrida{Modo: ModoFalso, Plano: planoDeUmPedido(a.bateria, 4), Bateria: a.bateria, No: a.no, Contador: contador, Relogio: relogioFixo})
	if err != nil || r.Terminou != TerminouCompleta || a.falso.Pedidos() != 4 || *r.Pedidos.RestantesHoje != 0 {
		t.Fatalf("quatro pedidos cabiam num tecto de 4: err=%v", err)
	}
}

// Atingido o tecto A MEIO, a corrida pára: o pedido seguinte não sai e o relatório parcial diz
// a causa. Aqui o que o esgota é o tecto em dólares, que só se conhece depois de cada resposta.
func TestAOS512_Tecto_AtingidoAMeioParaACorrida(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "contador.json")
	// Preço enorme e tecto pequeno: a primeira resposta gasta mais do que o tecto do dia.
	precos := &TabelaDePrecos{Modelos: map[string]Preco{"m": {EntradaMicroUSDPorMTok: 1_000_000_000, SaidaMicroUSDPorMTok: 1_000_000_000}}}
	contador, err := AbrirContador(caminho, FornecedorAnthropic, Tectos{PedidosDia: 100, MicroUSDDia: 1000, TemUSD: true}, precos, "m", relogioFixo)
	if err != nil {
		t.Fatal(err)
	}
	a := novoAmbienteFalso(t, []Comportamento{ComportamentoCumpre}, contador)
	r, err := Correr(context.Background(), CfgDaCorrida{Modo: ModoFalso, Plano: planoDeUmPedido(a.bateria, 5), Bateria: a.bateria, No: a.no, Contador: contador, Relogio: relogioFixo})
	if err != nil {
		t.Fatal(err)
	}
	if a.falso.Pedidos() != 1 {
		t.Fatalf("sairam %d pedidos; depois do primeiro o tecto em dolares estava atingido", a.falso.Pedidos())
	}
	if r.Terminou != TerminouTecto || len(r.Observacoes) != 2 || r.Observacoes[1].Desfecho != DesfechoTectoAtingido {
		t.Fatalf("terminou=%s com %d observacoes; quer tecto_atingido e o relatorio parcial", r.Terminou, len(r.Observacoes))
	}
	if !r.Custo.Estimativa || r.Custo.MicroUSDHoje == nil || *r.Custo.MicroUSDHoje < 1000 || *r.Custo.MicroUSDRestante != 0 {
		t.Errorf("custo no relatorio: %+v", r.Custo)
	}
	if r.Pedidos.Enviados != 1 {
		t.Errorf("enviados = %d, quer 1", r.Pedidos.Enviados)
	}
}

func TestAOS512_Tecto_CustoEstimado(t *testing.T) {
	p := Preco{EntradaMicroUSDPorMTok: 3_000_000, SaidaMicroUSDPorMTok: 15_000_000}
	// 1200 tokens de entrada a 3 USD/Mtok e 340 de saída a 15 USD/Mtok: 3600 + 5100 micro-USD.
	if got := custoMicroUSD(p, 1200, 340); got != 8700 {
		t.Errorf("custo = %d micro-USD, quer 8700", got)
	}
	// Arredonda para cima: nunca subestima.
	if got := custoMicroUSD(p, 1, 0); got != 3 {
		t.Errorf("custo de 1 token = %d, quer 3", got)
	}
	if got := custoMicroUSD(Preco{EntradaMicroUSDPorMTok: 1, SaidaMicroUSDPorMTok: 1}, 1, 0); got != 1 {
		t.Errorf("uma fraccao de micro-USD arredonda para 1, veio %d", got)
	}
}

// O modo com modelo real não corre sem o contador — e tem de ser o MESMO que o transporte do nó
// de ensaio consulta.
func TestAOS512_ModoReal_NaoCorreSemTecto(t *testing.T) {
	contador, _ := abrirContadorDeTeste(t, 100)
	semContador := novoAmbienteFalso(t, []Comportamento{ComportamentoCumpre}, nil)
	plano := planoDeUmPedido(semContador.bateria, 1)
	for nome, cfg := range map[string]CfgDaCorrida{
		"sem contador nenhum":            {Modo: ModoReal, Plano: plano, Bateria: semContador.bateria, No: semContador.no},
		"contador que o no nao consulta": {Modo: ModoReal, Plano: plano, Bateria: semContador.bateria, No: semContador.no, Contador: contador},
	} {
		if _, err := Correr(context.Background(), cfg); !errors.Is(err, ErrModoRealSemTecto) {
			t.Errorf("%s: err = %v, quer ErrModoRealSemTecto", nome, err)
		}
	}
	if semContador.falso.Pedidos() != 0 {
		t.Fatalf("o modo real sem tecto enviou %d pedidos", semContador.falso.Pedidos())
	}
}
