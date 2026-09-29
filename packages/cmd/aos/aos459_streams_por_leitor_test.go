package main

// AOS-459 — O TECTO DE STREAMS SSE É REPARTIDO POR LEITOR.
//
// # O DEFEITO, e porque nenhuma das barreiras anteriores o fecha
//
// `trajConns` limitava 256 streams SSE concorrentes de forma GLOBAL, sem repartição. Um leitor
// autenticado abre os 256 e nega `GET /runs/{id}/trajectory` a **todos** os outros — e não é uma
// rajada que passa: é **ocupação que fica** enquanto ele mantiver as ligações abertas.
//
//	AOS-456a  conta RUNS em curso por submissor, não LIGAÇÕES.
//	AOS-458   limita TAXA. Abrir um stream custa UM token; a ligação vive minutos depois disso.
//	`edge`    o nginx.conf tem `limit_req` (taxa) e NÃO tem `limit_conn` (ligações vivas).
//
// Era o resíduo declarado no AOS-458, apanhado por revisão adversarial. Este ficheiro fecha-o.
//
// # O QUE ESTES TESTES PRENDEM, e o risco que vigiam
//
// A contagem por-leitor tem ESTADO PRÓPRIO — ao contrário do tecto por-chamador de runs (AOS-456a),
// que deriva de `s.runs` e por isso não pode dessincronizar-se. Aqui um decremento em falta **tranca
// o leitor para sempre**, o que é um fail-CLOSED silencioso e pior do que não ter tecto. Por isso o
// invariante «a contagem volta a zero e a entrada desaparece» tem teste próprio, e não é um detalhe.

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
)

// TestAOS459UmLeitorNaoOcupaOsLugaresDosOutros — O CRITÉRIO.
func TestAOS459UmLeitorNaoOcupaOsLugaresDosOutros(t *testing.T) {
	const porLeitor = 2
	h, _, worm := noParaTeste(t)
	h.readGov = newReadGovernance(regioesFixas{"board:eu": "eu"}, nil, worm, aos277Clock())
	h.cfg.trajMaxConns = 100 // largo: o 429 que se mede NÃO pode vir do global
	h.cfg.trajMaxConnsPerReader = porLeitor
	h.trajPorLeitor = make(map[string]int)

	// ALICE ocupa o seu tecto e é recusada ao exceder.
	libertar := make([]func(), 0, porLeitor)
	for i := 0; i < porLeitor; i++ {
		f, ok := h.reservarStreamDoLeitor("human:alice")
		if !ok {
			t.Fatalf("reserva %d de alice, DENTRO do tecto %d, foi recusada", i+1, porLeitor)
		}
		libertar = append(libertar, f)
	}
	if _, ok := h.reservarStreamDoLeitor("human:alice"); ok {
		t.Fatalf("a reserva %d de alice devia ser RECUSADA com o tecto em %d — a reparticao nao morde",
			porLeitor+1, porLeitor)
	}

	// BOB, com alice no tecto: TEM de conseguir. É a propriedade que o tecto global não dá.
	for i := 0; i < porLeitor; i++ {
		f, ok := h.reservarStreamDoLeitor("human:bob")
		if !ok {
			t.Fatalf("bob foi recusado na reserva %d por causa da ocupacao de alice — e a negacao de "+
				"servico entre leitores que este ticket fecha", i+1)
		}
		libertar = append(libertar, f)
	}
	// E bob tem o SEU tecto, não passe livre.
	if _, ok := h.reservarStreamDoLeitor("human:bob"); ok {
		t.Fatal("bob excedeu o seu proprio tecto — a reparticao e por leitor, nao uma isencao para o segundo")
	}
	for _, f := range libertar {
		f()
	}
}

// TestAOS459AContagemVOLTAAZeroEAEntradaDesaparece — o invariante que vigia o fail-CLOSED.
//
// Um decremento em falta tranca o leitor PARA SEMPRE; uma entrada que fique no mapa é uma fuga sem
// tecto num nó de vida longa. As duas metades medem-se aqui.
func TestAOS459AContagemVOLTAAZeroEAEntradaDesaparece(t *testing.T) {
	h, _, worm := noParaTeste(t)
	h.readGov = newReadGovernance(regioesFixas{"board:eu": "eu"}, nil, worm, aos277Clock())
	h.cfg.trajMaxConns = 100
	h.cfg.trajMaxConnsPerReader = 4
	h.trajPorLeitor = make(map[string]int)

	// Abre e fecha o tecto inteiro, TRÊS vezes. Se o decremento falhasse, a 2.ª volta já recusava.
	for volta := 1; volta <= 3; volta++ {
		var libertar []func()
		for i := 0; i < 4; i++ {
			f, ok := h.reservarStreamDoLeitor("human:alice")
			if !ok {
				t.Fatalf("volta %d: a reserva %d foi recusada — a contagem NAO voltou a zero, e o leitor "+
					"fica trancado para sempre (fail-CLOSED silencioso)", volta, i+1)
			}
			libertar = append(libertar, f)
		}
		if n, _ := h.streamsDoLeitor("human:alice"); n != 4 {
			t.Fatalf("volta %d: contagem viva %d, esperava 4", volta, n)
		}
		for _, f := range libertar {
			f()
		}
		n, presente := h.streamsDoLeitor("human:alice")
		if n != 0 {
			t.Fatalf("volta %d: a contagem ficou em %d depois de libertar tudo", volta, n)
		}
		if presente {
			t.Fatalf("volta %d: a ENTRADA do leitor ficou no mapa com a contagem a zero — o mapa cresce "+
				"uma entrada por leitor que ja se foi, e a fuga nao tem tecto", volta)
		}
	}
}

// TestAOS459AReservaEAtomica — verificar e reservar partilham a secção crítica. Corre com -race.
func TestAOS459AReservaEAtomica(t *testing.T) {
	const porLeitor = 3
	const concorrentes = 60
	h, _, worm := noParaTeste(t)
	h.readGov = newReadGovernance(regioesFixas{"board:eu": "eu"}, nil, worm, aos277Clock())
	h.cfg.trajMaxConns = 1000
	h.cfg.trajMaxConnsPerReader = porLeitor
	h.trajPorLeitor = make(map[string]int)

	var mu sync.Mutex
	var libertar []func()
	var wg sync.WaitGroup
	for i := 0; i < concorrentes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if f, ok := h.reservarStreamDoLeitor("human:alice"); ok {
				mu.Lock()
				libertar = append(libertar, f)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	// EXACTO: nada é libertado durante a rajada, logo o número de reservas TEM de ser o tecto.
	if len(libertar) != porLeitor {
		t.Fatalf("%d pedidos concorrentes com o tecto em %d produziram %d reservas — verificar e "+
			"reservar NAO sao atomicos", concorrentes, porLeitor, len(libertar))
	}
	for _, f := range libertar {
		f()
	}
	if n, presente := h.streamsDoLeitor("human:alice"); n != 0 || presente {
		t.Fatalf("depois de libertar: contagem=%d presente=%v", n, presente)
	}
}

// TestAOS459ARotaREALComUmStreamVIVODevolve429AoSegundo — o critério pela rota, com um stream
// REALMENTE vivo.
//
// Os casos acima exercitam `reservarStreamDoLeitor`. Este atravessa o `handleTrajectory` REAL sobre um
// servidor HTTP, porque foi exactamente a lição do AOS-456a: um teste que só exercita a estrutura de
// dados deixa passar um mecanismo que o handler nunca alcança. E o stream tem de estar VIVO — um
// pedido que já terminou não ocupa lugar nenhum, e o teste passaria com a repartição desligada.
func TestAOS459ARotaREALComUmStreamVIVODevolve429AoSegundo(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	t.Cleanup(func() { _ = node.Close() })
	regions := govsov.NewRegistry(map[string]string{govBoard: govRegion, govBoardUS: govRegionUS})
	svc, h := newAPI(t, node, WithReadSovereignty(regions, node.WORM),
		// UM stream por leitor, global largo: o 429 que se mede TEM de vir da repartição.
		WithMaxTrajectoryConnsPerReader(1), WithMaxTrajectoryConns(100))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	// O run precisa de residência SELADA, senão o `admitSovereignRead` responde 404 antes da
	// admission e o teste não mede nada (foi o que a primeira versão deste caso fez).
	const runID = "run-459-vivo"
	submitHTTPAndWait(t, svc, h, runID, euReaderHeaders())
	appendTraj(t, node.EventStore, runID, "s1")

	abrir := func(ctx context.Context) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/runs/"+runID+"/trajectory", nil)
		if err != nil {
			return nil, err
		}
		for k, v := range euReaderHeaders() {
			req.Header.Set(k, v)
		}
		return http.DefaultClient.Do(req)
	}

	// (1) PRIMEIRO stream: fica VIVO. Lê-se o primeiro evento do backfill para garantir que o
	// handler já reservou o lugar — sem isso o segundo pedido poderia chegar antes da reserva.
	ctx1, cancelar1 := context.WithCancel(context.Background())
	defer cancelar1()
	resp1, err := abrir(ctx1)
	if err != nil {
		t.Fatalf("1o stream: %v", err)
	}
	defer func() { _ = resp1.Body.Close() }()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("o 1o stream devia dar 200, veio %d", resp1.StatusCode)
	}
	if _, err := readSSE(bufio.NewReader(resp1.Body)); err != nil {
		t.Fatalf("o 1o stream nao entregou o backfill, logo pode nao estar vivo: %v", err)
	}

	// (2) SEGUNDO stream do MESMO leitor, com o primeiro vivo: 429.
	ctx2, cancelar2 := context.WithCancel(context.Background())
	defer cancelar2()
	resp2, err := abrir(ctx2)
	if err != nil {
		t.Fatalf("2o stream: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("o 2o stream do MESMO leitor, com o tecto em 1 e o 1o VIVO, devia dar 429 e deu %d — "+
			"a reparticao existe mas o handler nao a alcanca, que foi o defeito da tentativa 1 do "+
			"AOS-456", resp2.StatusCode)
	}

	// (3) NÃO-VACUOSIDADE: fechado o primeiro, o lugar volta. Sem isto, uma repartição que recusasse
	// SEMPRE passaria (2) por acidente.
	cancelar1()
	_ = resp1.Body.Close()
	var recuperou bool
	for i := 0; i < 100; i++ {
		ctx3, cancelar3 := context.WithCancel(context.Background())
		resp3, err := abrir(ctx3)
		if err == nil && resp3.StatusCode == http.StatusOK {
			_ = resp3.Body.Close()
			cancelar3()
			recuperou = true
			break
		}
		if resp3 != nil {
			_ = resp3.Body.Close()
		}
		cancelar3()
		time.Sleep(20 * time.Millisecond)
	}
	if !recuperou {
		t.Fatal("fechado o 1o stream, o lugar do leitor NAO voltou — o decremento nao acontece no " +
			"caminho real, e o leitor fica trancado para sempre (fail-CLOSED silencioso)")
	}
}

// TestAOS459ModoLEGADOEOTectoDesligadoNaoRecusam — as duas fronteiras declaradas, fixadas em teste.
func TestAOS459ModoLEGADOEOTectoDesligadoNaoRecusam(t *testing.T) {
	h, _, _ := noParaTeste(t)
	h.cfg.trajMaxConnsPerReader = 1
	h.trajPorLeitor = make(map[string]int)

	// (a) PRINCIPAL VAZIO (modo legado: o gate devolve readerIdentity{}). Não há a quem imputar, logo
	// a repartição degenera no tecto global — declarado, e é a mesma fronteira do AOS-456a.
	for i := 0; i < 5; i++ {
		if f, ok := h.reservarStreamDoLeitor(""); !ok {
			t.Fatalf("principal vazio: a reserva %d foi recusada — em modo legado nao ha atribuicao, e "+
				"recusar aqui partiria um no sem gate soberano composto", i+1)
		} else if f != nil {
			t.Fatal("principal vazio nao deve devolver funcao de libertacao: nada foi reservado")
		}
	}

	// (b) REPARTIÇÃO DESLIGADA (<= 0).
	h.cfg.trajMaxConnsPerReader = 0
	for i := 0; i < 5; i++ {
		if _, ok := h.reservarStreamDoLeitor("human:alice"); !ok {
			t.Fatalf("com a reparticao desligada a reserva %d nao devia ser recusada", i+1)
		}
	}

	// (c) MAPA NÃO COMPOSTO (apiHandler construído à mão). Um panic no caminho de pedido é pior do
	// que a ausência de tecto — o mesmo compromisso da guarda nil de `tokenBucket.allow`.
	h.cfg.trajMaxConnsPerReader = 1
	h.trajPorLeitor = nil
	if _, ok := h.reservarStreamDoLeitor("human:alice"); !ok {
		t.Fatal("mapa nil devia degenerar em «sem reparticao», nao recusar")
	}
}

// TestAOS459EnvFailClosedEORRACIOENTREOSDOIS — `por-leitor >= global` é INERTE, e recusa-se.
//
// A razão é PRÓPRIA deste eixo e foi verificada aqui, não copiada do AOS-456a: os dois tectos são
// verificados no MESMO ponto, um após o outro. Com ambos a N, um leitor sozinho chega a N sem exceder
// nenhum, e na (N+1)-ésima é o GLOBAL que corta — o por-leitor nunca dispara.
func TestAOS459EnvFailClosedEORRACIOENTREOSDOIS(t *testing.T) {
	casos := []struct {
		global, porLeitor string
		aceita            bool
		porque            string
	}{
		{"", "", true, "ambos ausentes ⇒ defaults (256 / 32)"},
		{"", "32", true, "o default do por-leitor, abaixo do global por omissao"},
		{"", "255", true, "um abaixo do global por omissao: o maior valor que MORDE"},
		{"", "256", false, "IGUAL ao global por omissao ⇒ o global corta primeiro, reparticao INERTE"},
		{"", "300", false, "ACIMA do global por omissao ⇒ inerte"},
		{"64", "32", true, "par coerente, ambos explicitos"},
		{"64", "64", false, "iguais ⇒ inerte"},
		{"64", "65", false, "por-leitor acima do global ⇒ inerte"},
		{"0", "", false, "global zero NAO desliga: seria a armadilha do AOS_INGRESS_MAX_INFLIGHT"},
		{"-1", "", false, "global negativo"},
		{"abc", "", false, "global ilegivel"},
		// O FAIL-OPEN QUE ESTA TABELA NAO COBRIA (AOS-463). Todos os casos com `global` explicito punham
		// tambem o `porLeitor` explicito, pelo que a validacao — que vivia DENTRO do ramo do por-leitor
		// — nunca era alcancada por um par onde um dos dois vem do DEFAULT. Medido antes da correccao:
		// ambos ARRANCAVAM com por-leitor=32 >= global, reparticao INERTE, e um leitor ocupava todos os
		// lugares. E o DoS que o AOS-459 existe para fechar, alcancavel com UMA variavel.
		{"4", "", false, "SO o global baixado: o por-leitor fica no default 32 >= 4 ⇒ INERTE"},
		{"32", "", false, "SO o global, IGUAL ao default do por-leitor ⇒ INERTE"},
		{"33", "", true, "SO o global, um acima do default do por-leitor: o menor que MORDE"},
		{"", "0", false, "por-leitor zero"},
		{"", "-1", false, "por-leitor negativo"},
		{"", "2.5", false, "nao-inteiro: um tecto de ligacoes e um inteiro"},
	}
	for _, c := range casos {
		nome := fmt.Sprintf("global=%q/porLeitor=%q", c.global, c.porLeitor)
		t.Run(nome, func(t *testing.T) {
			clearIngressEnv(t)
			t.Setenv("AOS_TRAJECTORY_MAX_CONNS", c.global)
			t.Setenv("AOS_TRAJECTORY_MAX_CONNS_PER_READER", c.porLeitor)
			lim, _, err := ingressLimitsFromEnv()
			if c.aceita && err != nil {
				t.Fatalf("devia ser ACEITE (%s), deu: %v", c.porque, err)
			}
			if !c.aceita {
				if err == nil {
					t.Fatalf("foi ACEITO — devia ABORTAR o arranque (%s)", c.porque)
				}
				return
			}
			// ACEITE: o par em vigor tem de manter o invariante, senão a validação não serve de nada.
			if lim.trajMaxConnsPerReader >= lim.trajMaxConns {
				t.Fatalf("par em vigor INERTE: porLeitor=%d >= global=%d",
					lim.trajMaxConnsPerReader, lim.trajMaxConns)
			}
		})
	}
}

// TestAOS459ARecusaPorLeitorDEVOLVEOLugarGlobal — o invariante do ROLLBACK, e sobreviveu à primeira
// volta de mutações.
//
// PASSA HOJE POR CONSTRUÇÃO, e a sua justificação estava obsoleta (corrigida pelo AOS-461). Dizia: «a
// reserva por-leitor corre DEPOIS do incremento global, pelo que uma recusa dela tem de devolver o
// lugar que o global já tomou». Desde o AOS-460 a reserva corre ANTES, pelo que uma recusa por-leitor
// **nunca toma** o lugar global — não há rollback a fazer e não há nada a devolver. Mantém-se porque
// fixa a ordem pelo lado do efeito observável: se alguém voltar a pôr a reserva depois do incremento
// E se esquecer do rollback, este teste avermelha.
//
// O invariante com sujeito é o SIMÉTRICO, e não tinha sensor nenhum até ao AOS-461 — ver
// [TestAOS461RecusaGLOBALDEVOLVEOLugarGlobal].
//
// O sensor é a métrica REAL (`aos_trajectory_streams_active`), e não uma leitura da struct: o
// `NewAPIHandler` devolve o mux, e a métrica é também o que o operador tem para afinar os dois tectos.
func TestAOS459ARecusaPorLeitorDEVOLVEOLugarGlobal(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	t.Cleanup(func() { _ = node.Close() })
	regions := govsov.NewRegistry(map[string]string{govBoard: govRegion, govBoardUS: govRegionUS})
	svc, h := newAPI(t, node, WithReadSovereignty(regions, node.WORM),
		WithMaxTrajectoryConnsPerReader(1), WithMaxTrajectoryConns(100))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	const runID = "run-459-rollback"
	submitHTTPAndWait(t, svc, h, runID, euReaderHeaders())
	appendTraj(t, node.EventStore, runID, "s1")

	activos := func() float64 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("/metrics devolveu %d", rec.Code)
		}
		m := regexp.MustCompile(`(?m)^aos_trajectory_streams_active\s+([0-9.e+]+)\s*$`).
			FindStringSubmatch(rec.Body.String())
		if m == nil {
			t.Fatalf("a serie aos_trajectory_streams_active NAO existe em /metrics — sem ela este teste "+
				"nao mede nada. Se foi renomeada, actualize ESTE teste em vez de o apagar:\n%s", rec.Body.String())
		}
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			t.Fatalf("valor ilegivel %q: %v", m[1], err)
		}
		return v
	}

	abrir := func(ctx context.Context) *http.Response {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/runs/"+runID+"/trajectory", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		for k, v := range euReaderHeaders() {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET trajectory: %v", err)
		}
		return resp
	}

	if n := activos(); n != 0 {
		t.Fatalf("antes de tudo devia haver 0 streams activos, ha %v", n)
	}

	// UM stream vivo ⇒ o leitor está no tecto.
	ctx1, cancelar1 := context.WithCancel(context.Background())
	defer cancelar1()
	resp1 := abrir(ctx1)
	defer func() { _ = resp1.Body.Close() }()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("o 1o stream devia dar 200, veio %d", resp1.StatusCode)
	}
	if _, err := readSSE(bufio.NewReader(resp1.Body)); err != nil {
		t.Fatalf("o 1o stream nao entregou o backfill: %v", err)
	}
	if n := activos(); n != 1 {
		t.Fatalf("com um stream vivo devia haver 1 activo, ha %v", n)
	}

	// DEZ recusas por-leitor. Desde o AOS-460 a reserva por-leitor corre ANTES do incremento global,
	// pelo que nenhuma delas chega a TOMAR um lugar global — o contador fica em 1 por não haver nada
	// a devolver, e não por o rollback funcionar. É a tautologia que o godoc acima declara. O sentido
	// com sujeito — uma recusa GLOBAL devolver o lugar que já tomou — está em
	// [TestAOS461RecusaGLOBALDEVOLVEOLugarGlobal].
	for i := 0; i < 10; i++ {
		ctx, cancelar := context.WithCancel(context.Background())
		resp := abrir(ctx)
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("recusa %d: esperava 429, veio %d", i+1, resp.StatusCode)
		}
		_ = resp.Body.Close()
		cancelar()
	}
	if n := activos(); n != 1 {
		t.Fatalf("depois de 10 RECUSAS por-leitor ha %v streams activos, esperava 1 — uma recusa por-leitor "+
			"TOCOU no contador global. Ou a reserva voltou a correr DEPOIS do incremento sem devolver o "+
			"lugar, ou o incremento passou a acontecer antes dela: em qualquer dos casos N recusas "+
			"esgotam o tecto do no (%d) sem uma unica ligacao viva. Ver a ordem em handleTrajectory", n, 100)
	}
}
