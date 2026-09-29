package main

// AOS-460 — A REPARTIÇÃO POR LEITOR CORRE ANTES DO TECTO GLOBAL, E ISSO IMPORTA SOB CARGA.
//
// # O DEFEITO QUE ISTO FECHA
//
// O AOS-459 pôs a reserva por-leitor DEPOIS do incremento global, com o argumento de que «o global é
// a barreira do nó e é a mais barata». Consequência, na rota real, com as DUAS categorias de 429
// separadas (cinco corridas por ordem, este teste, `global=2 / por-leitor=1`, alice presa a UM stream
// vivo e 32 recusas dela em voo, bob a pedir sequencialmente):
//
//	ordem                429 pelo tecto GLOBAL   429 pelo tecto DE BOB   total negado a bob
//	AOS-459 (antiga)     34–52                   0                       34–52  (17–26%)
//	AOS-460 (esta)       0                       29–42                   29–42  (15–21%)
//
// O QUE ESTA CORRECÇÃO ENTREGA, E O QUE NÃO ENTREGA — e a primeira versão deste ficheiro dizia só
// «Trocada a ordem: 0 em 200», numa tabela cujas colunas eram «bob 200 | bob 429». A leitura que isso
// convidava — bob deixa de ser negado — é falsa, e o sensor abaixo filtrava em silêncio precisamente
// a categoria para onde o dano migra (achado ALTO-1 da sétima revisão adversarial).
//
// ENTREGA: uma recusa da repartição por-leitor **não consome lugar global**. A categoria que a ordem
// move vai a ZERO, e isso é negação por razão ERRADA que desaparece — o nó estava cheio de pedidos
// destinados a serem recusados.
//
// NÃO ENTREGA: que bob deixe de levar 429. O que sobra é bob a colidir com o SEU PRÓPRIO tecto de UM:
// sob contenção, o pedido N+1 dele chega antes de o lugar do pedido N ser libertado. É recusa por
// razão CERTA, confinada ao próprio principal — e é artefacto do tecto a 1. Medido no AOS-461, mesma
// rajada, proporção sã (`por-leitor ≤ global/2`):
//
//	global=16 / por-leitor=8    bob 200/200, ZERO de qualquer categoria (3 corridas)
//	global=64 / por-leitor=32   bob 200/200, ZERO de qualquer categoria (3 corridas)   <- o default é 32
//
// E nessa proporção as DUAS ordens dão 0: a ordem só é observável quando a folga global é de UM
// lugar. Isso não a torna dispensável — torna-a a diferença entre um nó apertado que degrada com
// razão e um que degrada sem razão.
//
// É a assimetria que o AOS-456a declara e cumpre — «exceder responde 429 SEM ocupar lugar nenhum, pelo
// que a rajada de um chamador não tira lugares aos outros» —, que o AOS-459 dizia replicar e não
// replicava.
//
// # PORQUE NENHUM DOS SEIS TESTES DO AOS-459 DAVA POR ISSO
//
// Todos mediam o resultado de UM pedido de cada vez. A diferença entre as duas ordens só existe
// quando há recusas EM VOO ao mesmo tempo que outro leitor pede — e nada exercitava isso. O teste
// abaixo é o que faltava, e é a única coisa deste ficheiro: um sensor para uma ordem.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
)

// TestAOS460RecusasEmVOONaoTiramLugaresGlobaisAOutroLeitor — o sensor da ordem.
func TestAOS460RecusasEmVOONaoTiramLugaresGlobaisAOutroLeitor(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	t.Cleanup(func() { _ = node.Close() })
	regions := govsov.NewRegistry(map[string]string{govBoard: govRegion, govBoardUS: govRegionUS})
	svc, h := newAPI(t, node, WithReadSovereignty(regions, node.WORM),
		// global APERTADO (2) e por-leitor a 1: é a razão de ser do teste. Com a ordem errada, as
		// recusas de alice ocupam o único lugar global que sobra.
		WithMaxTrajectoryConns(2), WithMaxTrajectoryConnsPerReader(1),
		// O balde de taxa do AOS-458 fica FORA do caminho: o 429 que se mede tem de vir do tecto de
		// OCUPAÇÃO, não do de taxa (que é global entre chamadores e tem residual próprio, declarado
		// no AOS-458 e no AOS-456b).
		WithReadRateLimit(1e9, 1e9), WithAPIClock(aos277Clock()))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	const runID = "run-460"
	submitHTTPAndWait(t, svc, h, runID, euReaderHeaders())
	appendTraj(t, node.EventStore, runID, "s1")

	pedirComo := func(ctx context.Context, principal string) (int, string) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/runs/"+runID+"/trajectory", nil)
		if err != nil {
			return 0, err.Error()
		}
		req.Header.Set(HeaderReaderPrincipal, principal)
		req.Header.Set(HeaderReaderBoard, govBoard)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		corpo := make([]byte, 256)
		n, _ := resp.Body.Read(corpo)
		_ = resp.Body.Close()
		return resp.StatusCode, string(corpo[:n])
	}

	// ALICE tem UM stream vivo ⇒ está no seu tecto, e toda a submissão dela é recusada pela
	// repartição. Mantém-se vivo durante a medição.
	ctxAlice, pararAlice := context.WithCancel(context.Background())
	defer pararAlice()
	reqAlice, err := http.NewRequestWithContext(ctxAlice, http.MethodGet, ts.URL+"/runs/"+runID+"/trajectory", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	reqAlice.Header.Set(HeaderReaderPrincipal, "human:alice")
	reqAlice.Header.Set(HeaderReaderBoard, govBoard)
	respAlice, err := http.DefaultClient.Do(reqAlice)
	if err != nil {
		t.Fatalf("o stream de alice: %v", err)
	}
	defer func() { _ = respAlice.Body.Close() }()
	if respAlice.StatusCode != http.StatusOK {
		t.Fatalf("o stream de alice devia dar 200, veio %d", respAlice.StatusCode)
	}
	buf := make([]byte, 64)
	if _, err := respAlice.Body.Read(buf); err != nil {
		t.Fatalf("o stream de alice nao entregou backfill, logo pode nao estar vivo: %v", err)
	}

	// RAJADA de alice: 32 pedidos concorrentes, TODOS destinados a recusa pela repartição. E bob a
	// pedir em SEQUÊNCIA — nunca compete consigo próprio, logo o único lugar global que sobra é dele.
	pararRajada := make(chan struct{})
	var wgRajada sync.WaitGroup
	for i := 0; i < 32; i++ {
		wgRajada.Add(1)
		go func() {
			defer wgRajada.Done()
			for {
				select {
				case <-pararRajada:
					return
				default:
				}
				ctx, cancelar := context.WithTimeout(context.Background(), 2*time.Second)
				pedirComo(ctx, "human:alice")
				cancelar()
			}
		}()
	}
	t.Cleanup(func() { close(pararRajada); wgRajada.Wait() })

	// AS DUAS CATEGORIAS, CONTADAS — e a primeira versão contava uma e descartava a outra em
	// silêncio, o que é o achado ALTO-1 da sétima revisão adversarial.
	//
	// O critério é, e continua a ser, o 429 do tecto GLOBAL: é a ÚNICA categoria que a ordem move, e
	// uma recusa global aqui significa «o nó está cheio de pedidos que vão ser RECUSADOS» — negação
	// por razão errada. Mas a versão anterior filtrava a outra categoria com o comentário «bob nunca
	// devia vê-lo», que é FALSO e mensurável: com o tecto por-leitor a 1, o pedido N+1 de bob chega
	// antes de o lugar do pedido N ser libertado (a contenção atrasa o retorno do handler anterior),
	// e bob colide com o SEU PRÓPRIO tecto.
	//
	// A diferença entre as duas é qualitativa e não deve ser aplanada: a global é uma recusa por
	// razão ERRADA (o nó cheio de recusas alheias), a por-leitor é a repartição de bob a funcionar
	// sobre um tecto de UM. Ambas se registam; só a primeira avermelha.
	//
	// ALCANCE, medido no AOS-461 (ver a tabela no ticket): o resíduo por-leitor é artefacto do tecto
	// a 1 e desaparece por completo a 8 e a 32 — o default do binário é 32.
	const tentativas = 200
	negadoAoBob, negadoPeloTectoDele := 0, 0
	for i := 0; i < tentativas; i++ {
		ctx, cancelar := context.WithTimeout(context.Background(), 2*time.Second)
		code, corpo := pedirComo(ctx, "human:bob")
		cancelar()
		switch {
		case code != http.StatusTooManyRequests:
		case strings.Contains(corpo, "deste leitor"):
			negadoPeloTectoDele++
		default:
			negadoAoBob++
		}
	}
	// REGISTADO SEMPRE, e não só no caminho de falha: é o número que a afirmação «0 de 200» omitia.
	t.Logf("bob, sob rajada de recusas de alice: %d de %d negados pelo tecto GLOBAL (o criterio) e "+
		"%d pelo tecto DELE (residuo do tecto por-leitor a 1, ZERO a 8 e a 32 — AOS-461)",
		negadoAoBob, tentativas, negadoPeloTectoDele)

	// O CRITÉRIO. Com a ordem errada mediram-se 44–78 negados pelo GLOBAL; com a certa, 0. Uma folga
	// pequena admite ruído de agendamento sem admitir o defeito, que é de outra ordem de grandeza.
	if negadoAoBob > tentativas/20 {
		t.Fatalf("bob levou %d de %d recusas pelo tecto GLOBAL enquanto alice — PRESA ao seu tecto de "+
			"UM stream — mandava recusas em voo.\n\n"+
			"A REPARTICAO POR LEITOR ESTA A CORRER DEPOIS DO INCREMENTO GLOBAL: cada pedido destinado "+
			"a recusa TOMA um lugar global antes de o devolver, e enquanto esta em voo ocupa-o. E a "+
			"assimetria que o AOS-456a declara e cumpre («429 SEM ocupar lugar nenhum») e que o "+
			"AOS-459 dizia replicar. Medido com a ordem errada: 54-78 de 200; com a certa: 0.\n\n"+
			"A correccao e reservar por-leitor ANTES de `trajConns.Add(1)` em handleTrajectory.",
			negadoAoBob, tentativas)
	}
}

// TestAOS460OBannerDeclaraAsTRESPosturasDoTectoPorLeitor — o AOS-459 não declarava NENHUMA.
//
// O AOS-456a tem três posturas distinguíveis no arranque, e o comentário do seu código diz que a do
// meio «é a que a revisão adversarial da tentativa 1 apanhou». O AOS-459 reproduziu **exactamente** a
// postura do meio — repartição ligada sobre um principal que vem do header — e não declarou nada, ao
// mesmo tempo que a descrevia na prosa como se fosse a postura sem atribuição nenhuma.
func TestAOS460OBannerDeclaraAsTRESPosturasDoTectoPorLeitor(t *testing.T) {
	base := ingressLimits{ratePerSec: 10, burst: 20, maxInFlight: 50, trajMaxConns: 100}
	comTecto := func() ingressLimits { l := base; l.trajMaxConnsPerReader = 4; return l }
	casos := []struct {
		nome                    string
		lim                     ingressLimits
		gate, principalVerifica bool
		exige, proibe           []string
	}{
		{"nao composto (sem reparticao)", base, true, true,
			[]string{"NAO COMPOSTO"}, []string{"LIGADO sobre"}},
		{"CONFIGURADO mas sem gate: nao ha a quem imputar", comTecto(), false, false,
			[]string{"CONFIGURADO (4)", "principal VAZIO", "degenera"}, []string{"LIGADO sobre"}},
		{"LIGADO sobre principal DEMO-GRADE (contornavel)", comTecto(), true, false,
			[]string{"DEMO-GRADE", "X-Aos-Reader", "CONTORNA-SE", "NAO vale contra abuso"},
			[]string{"VERIFICADO —"}},
		{"LIGADO sobre principal VERIFICADO", comTecto(), true, true,
			[]string{"VERIFICADO", "credencial FORTE", "NAO toma lugar global"},
			[]string{"DEMO-GRADE", "CONTORNA-SE"}},
		// O CASO QUE FALTAVA (AOS-461, MÉDIO-1 da sétima revisão): verificável SEM gate composto.
		// Sem o `&& gateComposto` no ramo VERIFICADO, esta combinação anuncia «LIGADO sobre principal
		// VERIFICADO … credencial FORTE verificada» num nó onde o `admitSovereignRead` devolve
		// principal VAZIO e NÃO há repartição nenhuma. Inalcançável hoje pelo `Bootstrap`
		// (`principalDoRunEVerificavel ⇒ noTemGateSoberanoDeLeitura`) — está aqui porque a função não
		// pode depender dessa implicação para estar certa, que é a razão pela qual a tabela do
		// AOS-456 tem o caso gémeo e esta não tinha.
		{"verificavel SEM gate: nada esta composto", comTecto(), false, true,
			[]string{"CONFIGURADO (4)", "principal VAZIO", "degenera"},
			[]string{"VERIFICADO", "credencial FORTE", "DEMO-GRADE"}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			txt := dobraDoEixo(t, strings.Join(ingressPostureBanner(c.lim, c.gate, c.principalVerifica), "\n"),
				marcadorDobraSSE)
			for _, ex := range c.exige {
				if !strings.Contains(txt, ex) {
					t.Errorf("a dobra do SSE NAO declara %q\n--- dobra ---\n%s", ex, txt)
				}
			}
			for _, pr := range c.proibe {
				if strings.Contains(txt, pr) {
					t.Errorf("a dobra do SSE declara %q, FALSO nesta postura\n--- dobra ---\n%s", pr, txt)
				}
			}
		})
	}
}

// TestAOS460OBannerDECLARAUmParINERTE — a env recusa `por-leitor >= global`, mas a composição
// in-process não. Um par inerte anunciado como repartição é a forma de falha que este ciclo pagou
// cinco vezes; o banner passa a dizê-lo.
//
// ALCANCE, corrigido pelo AOS-461: este aviso NÃO é alcançável por um operador. Todos os estados que
// o disparam abortam o arranque em `ingressLimitsFromEnv`, e o único chamador de produção do banner
// é alimentado por essa leitura — ver a nota no ramo correspondente de [ingressPostureBanner] e os
// catorze casos de [TestAOS459EnvFailClosedEORRACIOENTREOSDOIS], que são a barreira que morde. Este
// teste fixa o ramo para quem compõe in-process, e não uma protecção do operador.
func TestAOS460OBannerDECLARAUmParINERTE(t *testing.T) {
	inerte := ingressLimits{ratePerSec: 10, burst: 20, maxInFlight: 50,
		trajMaxConns: 4, trajMaxConnsPerReader: 4} // IGUAIS ⇒ o global corta primeiro
	txt := strings.Join(ingressPostureBanner(inerte, true, true), "\n")
	if !strings.Contains(txt, "INERTE") {
		t.Fatalf("com por-leitor(4) >= global(4) o banner tem de declarar que a reparticao esta INERTE "+
			"— o global corta primeiro e ela nunca dispara:\n%s", txt)
	}
	// CONTROLO: um par válido NÃO pode ser marcado inerte.
	valido := inerte
	valido.trajMaxConnsPerReader = 3
	if txt := strings.Join(ingressPostureBanner(valido, true, true), "\n"); strings.Contains(txt, "INERTE") {
		t.Fatalf("um par VALIDO (3 < 4) foi marcado INERTE:\n%s", txt)
	}
}

// TestAOS460UmaLibertacaoAMAISDaLugaresAMais — o invariante que faltava, ao nível da FUNÇÃO.
//
// Os seis testes do AOS-459 cobriam o decremento **a menos** (que tranca o leitor) e não o **a
// mais**, que dá lugares a mais. Uma revisão adversarial mediu: com `defer libertar()` duplicado, os
// seis davam `ok`.
//
// ⚠️ ESTE CASO NÃO CHEGA, e a primeira versão dele fingiu que sim: exercita
// `reservarStreamDoLeitor`/`libertar` directamente, e a duplicação que a revisão injectou vive no
// HANDLER. Ver [TestAOS460ALibertacaoDUPLANoHandlerDaStreamsAMais], que é o que a apanha.
func TestAOS460UmaLibertacaoAMAISDaLugaresAMais(t *testing.T) {
	h, _, worm := noParaTeste(t)
	h.readGov = newReadGovernance(regioesFixas{"board:eu": "eu"}, nil, worm, aos277Clock())
	h.cfg.trajMaxConns = 100
	h.cfg.trajMaxConnsPerReader = 3
	h.trajPorLeitor = make(map[string]int)

	libertar := make([]func(), 0, 3)
	for i := 0; i < 3; i++ {
		f, ok := h.reservarStreamDoLeitor("human:alice")
		if !ok {
			t.Fatalf("a reserva %d, dentro do tecto 3, foi recusada", i+1)
		}
		libertar = append(libertar, f)
	}
	// Liberta UM. Exactamente UM lugar tem de voltar.
	libertar[0]()

	admitidos := 0
	var extra []func()
	for i := 0; i < 5; i++ {
		if f, ok := h.reservarStreamDoLeitor("human:alice"); ok {
			admitidos++
			extra = append(extra, f)
		}
	}
	for _, f := range extra {
		f()
	}
	for _, f := range libertar[1:] {
		f()
	}
	if admitidos != 1 {
		t.Fatalf("libertado UM lugar de 3, voltaram %d — uma libertacao a MAIS da ao leitor mais "+
			"streams do que o seu tecto (com 2 chamadas por stream, um tecto de 3 vale 4)", admitidos)
	}
}

// TestAOS460AAtomicidadeComBARREIRADeArranque — o teste de atomicidade do AOS-459 era um detector de
// ~4%.
//
// Uma revisão adversarial quantificou: a mutação que separa a verificação do incremento em duas
// secções críticas (TOCTOU lógico) sobrevivia em 24 de 25 corridas com `-count=1` e `GOMAXPROCS=4`,
// que é a máquina de CI. E o `-race` não ajuda: os dois acessos ficam sob mutex, logo é corrida
// LÓGICA e não de dados.
//
// A diferença é a BARREIRA: lançar as goroutines e libertá-las todas no mesmo instante, em vez de as
// deixar arrancar em sequência. Ajuda, e NÃO chega — medido neste ficheiro, com a mutação TOCTOU e a
// janela entre as duas secções críticas a crescer, `-count=10` e `GOMAXPROCS=16`:
//
//	janela entre verificar e incrementar   corridas que detectam
//	nenhuma (Unlock/Lock adjacentes)       1 de 10
//	runtime.Gosched()                      10 de 10
//	time.Sleep(1µs)                        9 de 10
//
// **Este teste NÃO prova atomicidade.** Prova que não há uma janela da ordem de grandeza que um
// defeito real teria — mover a verificação para outro sítio do handler, por exemplo. Contra duas
// operações adjacentes é um detector de ~10%, e a garantia vem da ESTRUTURA (um só `Lock` com
// `defer Unlock`, sem retorno pelo meio, em `reservarStreamDoLeitor`), não daqui.
//
// É o mesmo limite, medido da mesma forma, que o AOS-456a registou para a sua própria asserção de
// atomicidade. Regista-se em vez de se esconder.
func TestAOS460AAtomicidadeComBARREIRADeArranque(t *testing.T) {
	const porLeitor = 3
	const concorrentes = 256
	h, _, worm := noParaTeste(t)
	h.readGov = newReadGovernance(regioesFixas{"board:eu": "eu"}, nil, worm, aos277Clock())
	h.cfg.trajMaxConns = 10000
	h.cfg.trajMaxConnsPerReader = porLeitor
	h.trajPorLeitor = make(map[string]int)

	largada := make(chan struct{})
	var prontas, feitas sync.WaitGroup
	var mu sync.Mutex
	var libertar []func()
	for i := 0; i < concorrentes; i++ {
		prontas.Add(1)
		feitas.Add(1)
		go func() {
			defer feitas.Done()
			prontas.Done()
			<-largada // TODAS partem no mesmo instante
			if f, ok := h.reservarStreamDoLeitor("human:alice"); ok {
				mu.Lock()
				libertar = append(libertar, f)
				mu.Unlock()
			}
		}()
	}
	prontas.Wait()
	close(largada)
	feitas.Wait()

	if len(libertar) != porLeitor {
		t.Fatalf("%d reservas concorrentes libertadas no MESMO instante, com o tecto em %d, produziram "+
			"%d — verificar e incrementar NAO sao atomicos (TOCTOU logico; o -race nao o ve porque os "+
			"dois acessos ficam sob mutex)", concorrentes, porLeitor, len(libertar))
	}
	for _, f := range libertar {
		f()
	}
	if n, presente := h.streamsDoLeitor("human:alice"); n != 0 || presente {
		t.Fatalf("depois de libertar: contagem=%d presente=%v", n, presente)
	}
}

// TestAOS460ALibertacaoDUPLANoHandlerDaStreamsAMais — pelo HANDLER, que é onde a duplicação vive.
//
// Uma revisão adversarial mediu que, com `defer libertar()` duplicado em `handleTrajectory`, os seis
// testes do AOS-459 davam `ok` — e a primeira versão do caso acima também, porque exercita a função
// e não o handler. O efeito só aparece DEPOIS de um stream fechar: a contagem desce DOIS por saída,
// e o leitor passa a caber mais do que o seu tecto.
func TestAOS460ALibertacaoDUPLANoHandlerDaStreamsAMais(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	t.Cleanup(func() { _ = node.Close() })
	regions := govsov.NewRegistry(map[string]string{govBoard: govRegion, govBoardUS: govRegionUS})
	svc, h := newAPI(t, node, WithReadSovereignty(regions, node.WORM),
		WithMaxTrajectoryConns(100), WithMaxTrajectoryConnsPerReader(2),
		WithReadRateLimit(1e9, 1e9), WithAPIClock(aos277Clock()))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	const runID = "run-460-dupla"
	submitHTTPAndWait(t, svc, h, runID, euReaderHeaders())
	appendTraj(t, node.EventStore, runID, "s1")

	abrirVivo := func() (*http.Response, context.CancelFunc) {
		ctx, cancelar := context.WithCancel(context.Background())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/runs/"+runID+"/trajectory", nil)
		if err != nil {
			cancelar()
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set(HeaderReaderPrincipal, "human:alice")
		req.Header.Set(HeaderReaderBoard, govBoard)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancelar()
			t.Fatalf("GET trajectory: %v", err)
		}
		if resp.StatusCode == http.StatusOK {
			buf := make([]byte, 64)
			_, _ = resp.Body.Read(buf) // garante que o handler ja reservou
		}
		return resp, cancelar
	}

	// DOIS streams vivos ⇒ alice está no tecto.
	r1, c1 := abrirVivo()
	defer func() { _ = r1.Body.Close(); c1() }()
	r2, c2 := abrirVivo()
	defer func() { _ = r2.Body.Close(); c2() }()
	if r1.StatusCode != http.StatusOK || r2.StatusCode != http.StatusOK {
		t.Fatalf("os dois primeiros streams deviam dar 200, vieram %d e %d", r1.StatusCode, r2.StatusCode)
	}

	// FECHA UM. Exactamente UM lugar tem de voltar.
	c1()
	_ = r1.Body.Close()

	// Tenta abrir DOIS. O primeiro tem de passar; o segundo tem de levar 429.
	var abertos []*http.Response
	var cancelar []context.CancelFunc
	admitidos := 0
	for i := 0; i < 2; i++ {
		// Espera activa curta: o decremento acontece quando o handler sai, e isso é assíncrono.
		var resp *http.Response
		var canc context.CancelFunc
		for tent := 0; tent < 100; tent++ {
			resp, canc = abrirVivo()
			if resp.StatusCode == http.StatusOK || i > 0 {
				break
			}
			_ = resp.Body.Close()
			canc()
			time.Sleep(20 * time.Millisecond)
		}
		abertos = append(abertos, resp)
		cancelar = append(cancelar, canc)
		if resp.StatusCode == http.StatusOK {
			admitidos++
		}
	}
	for i := range abertos {
		_ = abertos[i].Body.Close()
		cancelar[i]()
	}

	if admitidos != 1 {
		t.Fatalf("fechado UM de DOIS streams com o tecto em 2, foram admitidos %d — esperava 1.\n\n"+
			"Uma libertacao a MAIS no handler faz a contagem descer DOIS por saida, e o leitor passa a "+
			"caber mais do que o seu tecto. Os seis testes do AOS-459 nao davam por isso, e o caso ao "+
			"nivel da funcao tambem nao: a duplicacao vive no `defer` do handleTrajectory.", admitidos)
	}
}
