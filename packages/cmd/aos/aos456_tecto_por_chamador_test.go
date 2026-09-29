package main

// AOS-456 — TECTO DE CONCORRÊNCIA POR-CHAMADOR, PROVADO NO HANDLER REAL.
//
// PORQUE ESTE FICHEIRO ESTÁ ESCRITO ASSIM. A **tentativa 1** deste ticket passou 31 check-runs
// de CI e não funcionava: os seus testes exercitavam a ESTRUTURA DE DADOS do limitador e nunca o
// `handleSubmit`, onde o token GLOBAL era consumido ANTES de a etapa por-chamador decidir — pelo
// que cada recusa por-chamador já tinha gasto recurso partilhado e a starvation sobrevivia em
// TODAS as configurações. Foi revertida (`0834dd8`).
//
// Por isso os casos que medem o CRITÉRIO submetem por HTTP, contra um nó com o gate soberano
// composto — que é a postura em que o tecto ENTRA EM VIGOR, e **não** aquela em que o principal é
// verificado. A primeira versão desta frase dizia «a única postura em que o principal é VERIFICADO»,
// e era FALSA: sem credencial forte o principal vem do header `X-Aos-Reader`, que o chamador
// escreve. A frase sobreviveu byte-a-byte a dois commits que corrigiram exactamente isso — e é o
// padrão deste ticket em miniatura. Ver [TestAOS456AServeAPIComporEAnunciarNoARRANQUEREAL] para as
// quatro posturas e o que cada uma vale, e a nota de [principalDoRunEVerificavel] para a diferença. Nenhum deles chama `svc.submit` para provar o
// critério; os dois que o chamam directamente provam ramos que o HTTP não alcança (a isenção da
// retoma) e dizem-no.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
)

// aos456Bloqueado mantém CADA run REGISTADO em `s.runs` até `release` fechar. Sem isto os testes
// não seriam deterministas: com o `countingModel` os runs terminam entre submissões sequenciais,
// `s.runs` esvazia-se e o tecto nunca chega a morder — um teste que passaria com o tecto REMOVIDO.
type aos456Bloqueado struct {
	entrou  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *aos456Bloqueado) Call(ctx context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	select {
	case m.entrou <- struct{}{}:
	default:
	}
	select {
	case <-m.release:
		return agentruntime.ModelResponse{Text: "libertado", Final: true,
			Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1}}, nil
	case <-ctx.Done():
		return agentruntime.ModelResponse{}, ctx.Err()
	}
}

func (m *aos456Bloqueado) libertar() { m.once.Do(func() { close(m.release) }) }

// noSoberanoComTecto monta um nó com o gate soberano de leitura E o tecto por-chamador composto,
// com o modelo BLOQUEADO (ver [aos456Bloqueado]). Devolve o servidor e a credencial de teste.
func noSoberanoComTecto(t *testing.T, tecto int) (*httptest.Server, string) {
	t.Helper()
	model := &aos456Bloqueado{entrou: make(chan struct{}, 256), release: make(chan struct{})}
	t.Cleanup(model.libertar)

	node, _ := newAPINode(t, model, false)
	t.Cleanup(func() { _ = node.Close() })

	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute),
		WithInFlightPerCaller(tecto))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	// Sem `Shutdown` no cleanup, pelo molde de `newAPI` (api_test.go): com o modelo BLOQUEADO um
	// shutdown ficaria à espera de runs que só terminam quando o modelo é libertado, e a ordem LIFO
	// dos cleanups libertá-lo-ia depois. Os runs caem com o contexto do nó.

	regions := govsov.NewRegistry(map[string]string{"board:demo": "eu"})
	h, err := NewAPIHandler(svc, node, WithReadSovereignty(regions, audit.NewMemStore()),
		// Balde LARGO e relógio PARADO: qualquer 429 que se meça NÃO pode vir do token-bucket
		// global — se viesse, o teste ficaria verde com o tecto por-chamador desligado.
		WithRateLimit(1000, 4096), WithAPIClock(aos277Clock()))
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, credencialDeTeste(t, node)
}

// submeterComo faz um `POST /runs` como `principal`, pela via de headers do gate soberano, e
// devolve o status HTTP.
func submeterComo(t *testing.T, srv *httptest.Server, cred, principal, runID string) int {
	t.Helper()
	corpo, err := json.Marshal(map[string]any{
		"run_id": runID, "objective": "trabalho de referencia", "credential": cred,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/runs", bytes.NewReader(corpo))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderReaderPrincipal, principal)
	req.Header.Set(HeaderReaderBoard, "board:demo")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// TestAOS456ARajadaDeUmNaoTiraLugaresAoOutro — O CRITÉRIO DE ACEITAÇÃO, medido no handler.
//
// Alice enche o seu tecto e é recusada. Bob, imediatamente a seguir, TEM de ser admitido. Com o
// tecto por-chamador removido a primeira metade falha (alice nunca levaria 429 abaixo do tecto
// global de 512); com um tecto GLOBAL apertado em vez de por-chamador falha a segunda (bob levava
// 429 pela rajada de alice) — que é exactamente o estado em que o nó está hoje sem este ticket.
func TestAOS456ARajadaDeUmNaoTiraLugaresAoOutro(t *testing.T) {
	const tecto = 2
	srv, cred := noSoberanoComTecto(t, tecto)

	// Alice: `tecto` admissões, e a (tecto+1)-ésima recusada. Os runs ficam presos no modelo,
	// logo a contagem não desce entre pedidos.
	for i := 0; i < tecto; i++ {
		if st := submeterComo(t, srv, cred, "human:alice", fmt.Sprintf("a-%02d", i)); st != http.StatusCreated {
			t.Fatalf("alice: submissao %d ABAIXO do tecto %d devia ser admitida (201), veio %d", i+1, tecto, st)
		}
	}
	if st := submeterComo(t, srv, cred, "human:alice", "a-excede"); st != http.StatusTooManyRequests {
		t.Fatalf("alice: a submissao %d com o tecto em %d devia dar 429, veio %d — o tecto por-chamador "+
			"NAO esta a morder no handler (foi exactamente assim que a tentativa 1 passou verde)",
			tecto+1, tecto, st)
	}

	// Bob, com alice no tecto: NENHUM 429. É a propriedade que o tecto global não dá.
	for i := 0; i < tecto; i++ {
		if st := submeterComo(t, srv, cred, "human:bob", fmt.Sprintf("b-%02d", i)); st != http.StatusCreated {
			t.Fatalf("bob: submissao %d devia ser admitida (201) com alice no tecto, veio %d — a rajada de "+
				"alice tirou lugares a bob, que e a starvation que este ticket existe para fechar", i+1, st)
		}
	}
	// E bob tem o SEU tecto, não o de alice: o mecanismo é por-chamador, não uma isenção para o
	// segundo que chega.
	if st := submeterComo(t, srv, cred, "human:bob", "b-excede"); st != http.StatusTooManyRequests {
		t.Fatalf("bob: a submissao %d devia dar 429 — bob tem o SEU tecto, e nao passe livre, veio %d",
			tecto+1, st)
	}
}

// TestAOS456AUmaRECUSANaoOcupaLUGAR_MasGASTAUmTOKEN — a fronteira exacta do que este eixo dá, e o
// teste que a mede em vez de a esconder.
//
// A VERSÃO ANTERIOR DESTE TESTE ERA O PECADO DA TENTATIVA 1, COMETIDO POR MIM. Chamava-se
// «UmaRECUSANaoConsomeRecursoPartilhado», e só era verde porque punha o balde global a 4096 com o
// relógio parado: pôs o recurso partilhado FORA DO ALCANCE DO SENSOR e declarou a propriedade
// provada. Uma revisão adversarial mediu a tabela real, com um balde de tamanho realista:
//
//	alice #1       -> 201
//	alice #2..#10  -> 429 da etapa POR-CHAMADOR (e cada uma GASTOU um token global)
//	bob   #1       -> 429 "rate limit excedido"  <- a 1.ª etapa, balde vazio
//
// É a forma da tabela que reverteu a tentativa 1. O que MUDOU e o que NÃO mudou:
//
//   - NÃO mudou: o token global é consumido no topo do `handleSubmit` e a decisão por-chamador
//     acontece dentro do `submit`. Justiça em TAXA é o eixo AOS-456b, e não está feita.
//   - MUDOU: A não tira LUGARES a B. É o recurso que este eixo governa, e é o que o teste afirma.
//
// Este teste mede AS DUAS metades, com um balde apertado de propósito. Uma afirmação que só é
// verdadeira com o sensor cego não é uma afirmação.
func TestAOS456AUmaRECUSANaoOcupaLUGAR_MasGASTAUmTOKEN(t *testing.T) {
	const tecto = 1
	const burst = 10

	model := &aos456Bloqueado{entrou: make(chan struct{}, 64), release: make(chan struct{})}
	t.Cleanup(model.libertar)
	node, _ := newAPINode(t, model, false)
	t.Cleanup(func() { _ = node.Close() })
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute),
		WithInFlightPerCaller(tecto))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	regions := govsov.NewRegistry(map[string]string{"board:demo": "eu"})
	// BALDE APERTADO e relógio PARADO: `burst` tokens, e não reabastecem. É o que torna o custo
	// partilhado VISÍVEL.
	h, err := NewAPIHandler(svc, node, WithReadSovereignty(regions, audit.NewMemStore()),
		WithRateLimit(1000, burst), WithAPIClock(aos277Clock()))
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cred := credencialDeTeste(t, node)

	// (1) Alice esgota o balde: 1 admitida, burst-1 recusadas pelo tecto POR-CHAMADOR.
	admitidas, recusadas := 0, 0
	for i := 0; i < burst; i++ {
		switch st := submeterComo(t, srv, cred, "human:alice", fmt.Sprintf("a-%02d", i)); st {
		case http.StatusCreated:
			admitidas++
		case http.StatusTooManyRequests:
			recusadas++
		default:
			t.Fatalf("status inesperado %d", st)
		}
	}
	if admitidas != tecto || recusadas != burst-tecto {
		t.Fatalf("esperava %d admitida(s) e %d recusada(s), veio %d/%d", tecto, burst-tecto, admitidas, recusadas)
	}

	// (2) A METADE QUE ESTE EIXO **NÃO** DÁ, e que o banner tem de declarar: bob leva 429 do BALDE,
	// porque as recusas de alice gastaram os tokens. Se isto passar a 201 um dia, o eixo 456b foi
	// feito — e este teste tem de ser reescrito, não removido.
	if st := submeterComo(t, srv, cred, "human:bob", "b-00"); st != http.StatusTooManyRequests {
		t.Fatalf("bob veio %d. A propriedade MEDIDA e conhecida e que as recusas de alice GASTAM "+
			"tokens do balde global (consumido no topo do handleSubmit, antes da decisao por-chamador): "+
			"esperava 429. Se isto mudou, foi o AOS-456b a ser feito — actualize o banner, o README e "+
			"este teste, em vez de apagar a assercao", st)
	}

	// (3) A METADE QUE ESTE EIXO **DÁ**: os LUGARES de alice não são os de bob. Com o balde
	// reposto (handler novo, mesmo serviço — é o `s.runs` que conta), bob é admitido enquanto alice
	// continua no tecto.
	h2, err := NewAPIHandler(svc, node, WithReadSovereignty(regions, audit.NewMemStore()),
		WithRateLimit(1000, burst), WithAPIClock(aos277Clock()))
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	srv2 := httptest.NewServer(h2)
	t.Cleanup(srv2.Close)
	if st := submeterComo(t, srv2, cred, "human:bob", "b-01"); st != http.StatusCreated {
		t.Fatalf("com o balde reposto bob devia ser admitido (o LUGAR de alice nao e o dele), veio %d — "+
			"e esta a propriedade que o eixo da concorrencia existe para dar", st)
	}
	// CONTROLO: alice, com o balde igualmente reposto, continua recusada — o 429 dela vem do
	// TECTO e não do balde, senão (3) não provava nada.
	if st := submeterComo(t, srv2, cred, "human:alice", "a-99"); st != http.StatusTooManyRequests {
		t.Fatalf("CONTROLO: alice devia continuar recusada pelo TECTO com o balde reposto, veio %d", st)
	}
}

// TestAOS456ATectoEAtomico — a verificação e a reserva partilham a secção crítica. Sem isso, N
// pedidos concorrentes do mesmo submissor veem todos `count < tecto` e passam todos: um TOCTOU
// que torna o tecto uma sugestão.
//
// A ASSERÇÃO É EXACTA (`admitidos == tecto`, porque os runs ficam presos no modelo e nenhum lugar
// é libertado durante a rajada). O SENSOR NÃO É. Medido, injectando o defeito neste mesmo ficheiro
// — separar a contagem da reserva com uma janela de tamanho crescente, `-count=5`:
//
//	janela                     deteta?
//	-------------------------  -------
//	nenhuma (unlock/relock)    NAO
//	time.Sleep(1µs)            NAO
//	runtime.Gosched()          sim
//	time.Sleep(50µs)           sim
//
// Ou seja: este teste NÃO prova atomicidade — prova que não há uma janela da ORDEM DE GRANDEZA
// que o defeito REAL teria. E o defeito real é ter a verificação no handler em vez de no submit:
// lá, entre contar e reservar ficam o `readGov.authorize`, a verificação da credencial (uma
// `ed25519.Verify` mede 59.9 µs neste repo) e a selagem de residência no WORM — dezenas a
// centenas de microssegundos, folgadamente dentro do que este sensor apanha. Contra um reordering
// de nanossegundos o sensor é cego, e a garantia contra esse vem da ESTRUTURA (um só par
// Lock/Unlock em service.go, sem retorno pelo meio), não deste teste.
func TestAOS456ATectoEAtomico(t *testing.T) {
	const tecto = 3
	const concorrentes = 40
	srv, cred := noSoberanoComTecto(t, tecto)

	var mu sync.Mutex
	admitidos, recusados, outros := 0, 0, 0
	var wg sync.WaitGroup
	for i := 0; i < concorrentes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st := submeterComo(t, srv, cred, "human:alice", fmt.Sprintf("c-%03d", i))
			mu.Lock()
			defer mu.Unlock()
			switch st {
			case http.StatusCreated:
				admitidos++
			case http.StatusTooManyRequests:
				recusados++
			default:
				outros++
			}
		}(i)
	}
	wg.Wait()

	if outros != 0 {
		t.Fatalf("%d pedidos responderam com um status que nao e 201 nem 429 — o teste nao esta a medir "+
			"o tecto", outros)
	}
	if admitidos != tecto {
		t.Fatalf("com %d pedidos CONCORRENTES do mesmo submissor e tecto %d, foram admitidos %d — a "+
			"verificacao e a reserva NAO sao atomicas (TOCTOU: cada pedido viu a contagem antes de "+
			"qualquer outro reservar)", concorrentes, tecto, admitidos)
	}
	if recusados != concorrentes-tecto {
		t.Fatalf("esperava %d recusas, vieram %d", concorrentes-tecto, recusados)
	}
}

// TestAOS456AOutroChamadorPassaSobConcorrencia — o caso de cima mede a atomicidade; este mede que
// ela não foi obtida com um lock GLOBAL de admissão que também barrasse os outros. Sob a mesma
// rajada concorrente de alice, bob é admitido.
func TestAOS456AOutroChamadorPassaSobConcorrencia(t *testing.T) {
	const tecto = 2
	srv, cred := noSoberanoComTecto(t, tecto)

	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			submeterComo(t, srv, cred, "human:alice", fmt.Sprintf("d-%03d", i))
		}(i)
	}
	wg.Wait()

	for i := 0; i < tecto; i++ {
		if st := submeterComo(t, srv, cred, "human:bob", fmt.Sprintf("e-%02d", i)); st != http.StatusCreated {
			t.Fatalf("bob: submissao %d devia ser admitida depois de 30 pedidos concorrentes de alice, veio %d", i+1, st)
		}
	}
}

// TestAOS456ARetomaEIsenta — um run suspenso à espera de um humano tem de poder ser retomado. Um
// run que não se pode retomar por causa de uma quota é um run PRESO, não um run limitado. O tecto
// GLOBAL já tem esta isenção (a retoma não consulta `maxInFlight`); este caso fixa-a para o
// por-chamador.
//
// Chama `submit` DIRECTAMENTE — e é o único caso deste ficheiro que o faz para provar o critério —
// porque o parâmetro `resuming` é interno: a via HTTP que o liga (`POST /runs/{id}/resume`) exige
// um run realmente suspenso no log, e o que aqui se fixa é o RAMO, não o caminho da retoma.
func TestAOS456ARetomaEIsenta(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	t.Cleanup(func() { _ = node.Close() })
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute),
		WithInFlightPerCaller(1))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	// Sem `Shutdown` no cleanup, pelo molde de `newAPI` (api_test.go): os runs caem com o contexto
	// do nó. (Este caso usa o `countingModel`, não o bloqueado — não há runs presos por que esperar.)

	// Um run do MESMO principal já registado, com o tecto em 1: a contagem está NO tecto.
	svc.mu.Lock()
	svc.runs["ja-em-curso"] = &runState{runID: "ja-em-curso", done: make(chan struct{}), principal: "human:alice"}
	svc.mu.Unlock()

	// CONTROLO PRIMEIRO, para que a isenção abaixo não seja vacuosa: uma submissão NOVA do mesmo
	// principal TEM de ser recusada pelo tecto.
	nova := agentruntime.Goal{RunID: "nao-retomado", Objective: "x", MaxTurns: 1}
	nova.Principal.NHIID = "human:alice"
	if err := svc.submit(context.Background(), nova, false); err != ErrCallerInFlightCeiling {
		t.Fatalf("CONTROLO: submissao NOVA do mesmo principal com o tecto em 1 e um run em curso devia "+
			"dar ErrCallerInFlightCeiling, deu %v — sem esta recusa a isencao da retoma abaixo nao prova nada", err)
	}

	// A ISENÇÃO: a MESMA submissão com `resuming=true` NÃO pode ser recusada pelo tecto.
	retoma := agentruntime.Goal{RunID: "retomado", Objective: "x", MaxTurns: 1}
	retoma.Principal.NHIID = "human:alice"
	if err := svc.submit(context.Background(), retoma, true); err == ErrCallerInFlightCeiling {
		t.Fatal("a RETOMA foi recusada pelo tecto por-chamador — um run que nao se pode retomar por " +
			"causa de uma quota e um run PRESO, nao um run limitado")
	}
}

// TestAOS456ASemPrincipalVERIFICADONaoHaTectoAImpor — a fronteira do mecanismo, declarada em
// teste e não só em prosa. Num nó em modo LEGADO (sem gate soberano) o principal do run vem do
// CORPO do pedido: um tecto sobre um valor que o chamador escolhe contorna-se mudando-o. O
// mecanismo continua a contar o que lá está — mas o banner é que tem de dizer que NÃO está em
// vigor, e é isso que o caso do banner mais abaixo fixa.
//
// O que se prova aqui é o corolário operacional: o tecto é por VALOR de principal, e dois valores
// diferentes são dois chamadores. É por isso que a composição depende do gate — sem ele, a
// atribuição não é verificável.
func TestAOS456ATectoEPorVALORDePrincipal(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	t.Cleanup(func() { _ = node.Close() })
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute),
		WithInFlightPerCaller(1))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	// Sem `Shutdown` no cleanup, pelo molde de `newAPI` (api_test.go): os runs caem com o contexto
	// do nó. (Este caso usa o `countingModel`, não o bloqueado — não há runs presos por que esperar.)

	svc.mu.Lock()
	svc.runs["da-alice"] = &runState{runID: "da-alice", done: make(chan struct{}), principal: "human:alice"}
	svc.mu.Unlock()

	// O MESMO valor: recusado.
	mesma := agentruntime.Goal{RunID: "outra-da-alice", Objective: "x", MaxTurns: 1}
	mesma.Principal.NHIID = "human:alice"
	if err := svc.submit(context.Background(), mesma, false); err != ErrCallerInFlightCeiling {
		t.Fatalf("o MESMO principal devia bater no tecto, deu %v", err)
	}
	// Um valor DIFERENTE: não conta para o tecto de alice. Se contasse, o tecto seria global com
	// outro nome.
	outra := agentruntime.Goal{RunID: "da-bob", Objective: "x", MaxTurns: 1}
	outra.Principal.NHIID = "human:bob"
	if err := svc.submit(context.Background(), outra, false); err == ErrCallerInFlightCeiling {
		t.Fatal("um principal DIFERENTE bateu no tecto de alice — o tecto nao e por-chamador, e global " +
			"com outro nome")
	}
}

// TestAOS456ABannerDistingueAsTRESPosturas — a tentativa 1 derivava a postura só da CONFIG e
// anunciava «LIGADA» com a barreira a nil, porque a composição dependia de outra coisa. A postura
// do MEIO — configurada e NÃO composta — é a que tem de ser ALCANÇÁVEL e distinguível, e é a
// postura real de quem define a variável num nó sem gate soberano.
func TestAOS456ABannerDistingueAsTRESPosturas(t *testing.T) {
	base := ingressLimits{ratePerSec: 10, burst: 20, maxInFlight: 50}
	comTecto := func() ingressLimits { l := base; l.inFlightPerCaller = 4; return l }
	casos := []struct {
		nome      string
		lim       ingressLimits
		gate      bool
		verificav bool
		exige     []string
		proibe    []string
	}{
		// Sem config, a composição é irrelevante: não há tecto nenhum para anunciar.
		{"nao configurado (com gate)", base, true, true,
			[]string{"NAO CONFIGURADO"}, []string{"LIGADO sobre", "NAO COMPOSTO", "(4)"}},
		{"nao configurado (sem gate)", base, false, false,
			[]string{"NAO CONFIGURADO"}, []string{"LIGADO sobre", "NAO COMPOSTO"}},
		{"configurado mas NAO composto", comTecto(), false, false,
			[]string{"NAO COMPOSTO", "(4)", "auto-declarado"}, []string{"LIGADO sobre", "NAO CONFIGURADO"}},
		// A POSTURA QUE A REVISÃO ADVERSARIAL DESCOBRIU: gate composto, credencial forte AUSENTE. O
		// tecto está em vigor e é CONTORNÁVEL rodando o header. Antes desta correcção o banner
		// dizia-lhe «SUBMISSOR VERIFICADO».
		{"LIGADO mas DEMO-GRADE (contornavel por header)", comTecto(), true, false,
			[]string{"DEMO-GRADE", "X-Aos-Reader", "CONTORNA-SE", "NAO vale contra abuso", "AOS_SOVEREIGN_OIDC_ISSUER"},
			[]string{"VERIFICADO —", "NAO COMPOSTO", "NAO CONFIGURADO"}},
		{"LIGADO sobre principal VERIFICADO", comTecto(), true, true,
			[]string{"VERIFICADO", "credencial FORTE", "4 submissao"},
			[]string{"DEMO-GRADE", "NAO COMPOSTO", "NAO CONFIGURADO"}},
		// RAMO SEM GUARDA (achado da segunda revisão): `verificável` sem `gate` NÃO compõe o tecto no
		// `serveAPI`, logo o banner não pode anunciar VERIFICADO. Inalcançável hoje pelos predicados
		// reais, mas a função não pode depender dessa coincidência — era a forma do ALTO-1b.
		{"verificavel SEM gate: nada esta composto", comTecto(), false, true,
			[]string{"NAO COMPOSTO"}, []string{"LIGADO sobre"}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			// A DOBRA DESTE EIXO, e não o banner inteiro. O banner passou a ter outra dobra com o
			// mesmo vocabulário («NAO COMPOSTO», «DEMO-GRADE») para o tecto de streams SSE
			// (AOS-459/AOS-460), e procurar no texto todo confundia os dois eixos — o que também
			// tornava estas asserções mais fracas do que pareciam: qualquer ocorrência em qualquer
			// parte do banner as satisfazia.
			txt := dobraDoEixo(t, strings.Join(ingressPostureBanner(c.lim, c.gate, c.verificav), "\n"),
				marcadorDobra456)
			for _, ex := range c.exige {
				if !strings.Contains(txt, ex) {
					t.Errorf("banner NAO declara %q\n--- banner ---\n%s", ex, txt)
				}
			}
			for _, pr := range c.proibe {
				if strings.Contains(txt, pr) {
					t.Errorf("banner declara %q, que e FALSO nesta postura\n--- banner ---\n%s", pr, txt)
				}
			}
		})
	}
}

// TestAOS456AEnvFailClosedNOSDOISSENTIDOS — a tentativa 1 validou só `> 0` e aceitava `1e8` sem
// uma palavra: fail-closed contra o zero, aberto de par em par contra o absurdo. Aqui o tecto
// SUPERIOR tem significado próprio — um tecto por-chamador ACIMA do global nunca morde (o global
// morde primeiro), logo é configuração que anuncia uma barreira inerte.
//
// A IGUALDADE TAMBÉM É RECUSADA, e a primeira versão deste teste declarava-a «coerente, degenera no
// global» — achado da segunda revisão adversarial. Medido com global=3 e per-caller=3: a 4.ª
// submissão do mesmo chamador dá 429 **igual com e sem** o tecto por-chamador composto, porque o
// check global corre no `handleSubmit` ANTES do `submit`. O por-chamador só dispararia na janela de
// corrida desse check (que é um TOCTOU fora do mutex) — e uma barreira que só morde por acidente,
// anunciada como LIGADA, é a forma de falha que este ticket existe para não repetir.
func TestAOS456AEnvFailClosedNOSDOISSENTIDOS(t *testing.T) {
	casos := []struct {
		valor  string
		global string
		aceita bool
		porque string
	}{
		{"", "", true, "vazia => tecto NAO COMPOSTO, que e o default"},
		{"4", "", true, "dentro do global por omissao (512)"},
		{"511", "", true, "um abaixo do global por omissao: o maior valor que MORDE"},
		{"512", "", false, "IGUAL ao global por omissao: NAO morde — o check global corre no handler, antes do submit"},
		{"0", "", false, "zero NAO desliga: seria a armadilha inversa do AOS_INGRESS_MAX_INFLIGHT"},
		{"-1", "", false, "negativo"},
		{"abc", "", false, "ilegivel"},
		{"2.5", "", false, "nao-inteiro: um tecto de lugares e um inteiro"},
		{"513", "", false, "ACIMA do global por omissao => barreira INERTE"},
		{"999999", "", false, "muito acima do global => barreira INERTE (o caso que a tentativa 1 aceitava)"},
		{"8", "4", false, "acima do global EXPLICITO"},
		{"4", "4", false, "igual ao global explicito: barreira inerte, anunciada como LIGADA"},
		{"3", "4", true, "abaixo do global explicito"},
	}
	for _, c := range casos {
		nome := c.valor
		if nome == "" {
			nome = "(vazio)"
		}
		t.Run("PER_CALLER="+nome+"/global="+c.global, func(t *testing.T) {
			clearIngressEnv(t)
			if c.global != "" {
				t.Setenv("AOS_INGRESS_MAX_INFLIGHT", c.global)
			}
			t.Setenv("AOS_INGRESS_MAX_INFLIGHT_PER_CALLER", c.valor)
			lim, _, err := ingressLimitsFromEnv()
			if c.aceita && err != nil {
				t.Fatalf("valor %q devia ser ACEITE (%s), deu: %v", c.valor, c.porque, err)
			}
			if !c.aceita {
				if err == nil {
					t.Fatalf("valor %q foi ACEITO — devia ABORTAR o arranque (%s)", c.valor, c.porque)
				}
				// O erro tem de NOMEAR a variável: um erro de config que não diz qual variável está
				// errada obriga o operador a adivinhar.
				if !strings.Contains(err.Error(), "AOS_INGRESS_MAX_INFLIGHT_PER_CALLER") {
					t.Fatalf("o erro devia nomear a variavel: %v", err)
				}
				return
			}
			// ACEITE: o número LIDO tem de ser o número em vigor — sem isto, uma leitura que
			// devolvesse sempre 0 passaria todos os casos «aceita».
			if c.valor == "" {
				if lim.inFlightPerCaller != 0 {
					t.Fatalf("variavel vazia devia deixar o tecto a 0 (NAO COMPOSTO), veio %d", lim.inFlightPerCaller)
				}
				return
			}
			if got := fmt.Sprintf("%d", lim.inFlightPerCaller); got != c.valor {
				t.Fatalf("o tecto lido (%s) nao e o configurado (%s)", got, c.valor)
			}
			if !lim.tuned {
				t.Fatal("com a variavel definida o banner tem de anunciar limites AFINADOS")
			}
		})
	}
}

// BenchmarkAOS456SubmitRecusadoInSitu mede o custo do tecto NO CAMINHO DE PRODUÇÃO — o que o
// critério «a latência não degrada, com número» pede.
//
// A PRIMEIRA VERSÃO DESTE BENCHMARK MEDIA UMA CÓPIA DO LAÇO, e foi um achado de revisão
// adversarial: reimplementava a varredura sobre um `map` local, nunca chamava `submit` e nunca
// tomava `s.mu`. Provado inútil por mutação — pôr o laço de PRODUÇÃO a fazer 20x o trabalho não
// mexia um nanossegundo no número. Um sensor que não vê o código que mede não é um sensor.
//
// Aqui o caminho é o real: `submit` com o tecto cheio é exactamente `Lock → varredura → Unlock →
// return ErrCallerInFlightCeiling`, sem lease, sem goroutine, sem I/O.
//
// MEDIDO in situ (revisão independente, mesmo contentor): 1,10 µs com 64 runs, 6,17 µs com 512,
// 69 µs com 4096, 1,37 ms com 32768. A ordem de grandeza confirma-se ONDE a premissa vale.
//
// ⚠️ A PREMISSA `s.runs ≤ 512` É FALSA, e é o resíduo honesto deste eixo: nem `handleResume` nem o
// `ResumeInterruptedRuns` (arranque e varredura periódica) consultam o tecto GLOBAL, e o check
// global do handler é um TOCTOU fora do mutex. `s.runs` pode passar o tecto, e aí a varredura
// degrada linearmente segurando o mutex. Fica declarado no ticket como fronteira conhecida, não
// resolvido: a alternativa O(1) tem um risco fail-CLOSED próprio (uma entrada a mais tranca o
// chamador para sempre) e a via que faz `s.runs` crescer exige four-eyes composto e credencial
// fresca por retoma.
func BenchmarkAOS456SubmitRecusadoInSitu(b *testing.B) {
	for _, n := range []int{1, 64, 512, 4096} {
		b.Run(fmt.Sprintf("runs=%d", n), func(b *testing.B) {
			node, _ := newAPINode(&testing.T{}, &countingModel{}, false)
			defer func() { _ = node.Close() }()
			svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute),
				WithInFlightPerCaller(1))
			if err != nil {
				b.Fatalf("NewNodeService: %v", err)
			}
			// `s.runs` povoado à mão: o benchmark mede a VARREDURA, e hospedar n runs a sério mediria
			// o loop de serviço.
			svc.mu.Lock()
			for i := 0; i < n; i++ {
				id := fmt.Sprintf("run-%05d", i)
				svc.runs[id] = &runState{runID: id, done: make(chan struct{}),
					principal: fmt.Sprintf("human:p%03d", i%16)}
			}
			svc.mu.Unlock()

			g := agentruntime.Goal{RunID: "medido", Objective: "x", MaxTurns: 1}
			// `human:p000` porque `i%16` com i=0 o produz sempre: para QUALQUER n >= 1 este principal
			// tem ao menos um run, logo o tecto de 1 está cheio e o caminho medido é a RECUSA. Um
			// principal que não estivesse no tecto deixaria o submit seguir para o lease, e o
			// benchmark mediria I/O em vez da varredura (o `runs=1` apanhou isto).
			g.Principal.NHIID = "human:p000"
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := svc.submit(ctx, g, false); err != ErrCallerInFlightCeiling {
					b.Fatalf("esperava a recusa pelo tecto (o caminho que se mede), veio %v", err)
				}
			}
		})
	}
}

// TestAOS456APredicadoDeComposicaoCOINCIDEComOGateReal — o sensor que impede a divergência de
// voltar. O `principalVerificavel` do arranque decide se o tecto entra em vigor E o que o banner
// anuncia; se deixar de coincidir com a composição REAL do gate, o nó desliga a barreira (ou
// anuncia-a) contra o estado de facto. A primeira versão desse predicado era uma cópia à mão e já
// omitia o ramo `SovereignAuthority`.
//
// O teste compara [noTemGateSoberanoDeLeitura] com o ÚNICO facto observável que conta: o handler
// composto tem, ou não tem, `readGov`.
//
// ALCANCE, declarado: cobre a via do NÓ — que é a única que [serveAPI] usa. Um handler composto
// pela opção [WithReadSovereignty] tem `readGov` sem que o nó o declare, e esse caso NÃO é uma
// divergência (é o molde dos testes); por isso a matriz abaixo nunca passa essa opção.
func TestAOS456APredicadoDeComposicaoCOINCIDEComOGateReal(t *testing.T) {
	regions := govsov.NewRegistry(map[string]string{"board:demo": "eu"})

	casos := []struct {
		nome    string
		compor  func(n *Node)
		esperar bool
	}{
		{"sem soberania (no LEGADO)", func(n *Node) { n.SovereignReadRegions = nil; n.SovereignAuthority = nil }, false},
		{"registo board->regiao (via de compatibilidade)", func(n *Node) { n.SovereignReadRegions = regions }, true},
		{"AUTORIDADE sem registo (o ramo que a copia a mao omitia)", func(n *Node) {
			n.SovereignReadRegions = nil
			n.SovereignAuthority = &SovereignRegionAuthority{}
		}, true},
		{"autoridade E registo (a postura do Bootstrap)", func(n *Node) {
			n.SovereignReadRegions = regions
			n.SovereignAuthority = &SovereignRegionAuthority{}
		}, true},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			node, _ := newAPINode(t, &countingModel{}, false)
			t.Cleanup(func() { _ = node.Close() })
			if node.WORM == nil {
				t.Fatal("o Bootstrap devia compor sempre um WORM (nem que seja em memoria) — sem ele " +
					"esta matriz nao distingue os ramos que quer distinguir")
			}
			c.compor(node)

			predicado := noTemGateSoberanoDeLeitura(node)
			if predicado != c.esperar {
				t.Fatalf("noTemGateSoberanoDeLeitura = %v, esperava %v", predicado, c.esperar)
			}

			// E a COMPOSIÇÃO REAL, medida pelo COMPORTAMENTO e não pela struct (o [NewAPIHandler]
			// devolve o mux, e inspeccionar internals seria um sensor mais fraco de qualquer forma):
			// com o gate composto, um `POST /runs` SEM headers de leitor leva 403; sem gate, 201.
			svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
			if err != nil {
				t.Fatalf("NewNodeService: %v", err)
			}
			h, err := NewAPIHandler(svc, node)
			if err != nil {
				t.Fatalf("NewAPIHandler: %v", err)
			}
			rec := postJSON(h, "POST", "/runs", map[string]any{
				"run_id": "predicado-" + fmt.Sprintf("%p", node), "objective": "x",
				"principal_nhi": "nhi:x", "credential": credencialDeTeste(t, node),
			})
			real := rec.Code == http.StatusForbidden
			if rec.Code != http.StatusForbidden && rec.Code != http.StatusCreated {
				t.Fatalf("submissao sem headers devia dar 403 (com gate) ou 201 (sem gate), veio %d: %s",
					rec.Code, rec.Body.String())
			}
			if real != predicado {
				t.Fatalf("DIVERGENCIA: o predicado do arranque diz gate=%v mas o handler REAL respondeu %d "+
					"a um submit SEM headers de leitor (403 ⇒ gate composto, 201 ⇒ nao).\n"+
					"E este o desencontro que desliga o tecto por-chamador (ou o anuncia) contra o estado de "+
					"facto do no — ver noTemGateSoberanoDeLeitura em sovereignty.go", predicado, rec.Code)
			}

			// E o WORM em falta derruba os DOIS lados juntos: a conjunção não é decorativa.
			semWorm := *node
			semWorm.WORM = nil
			if noTemGateSoberanoDeLeitura(&semWorm) {
				t.Fatal("sem WORM o gate NAO se compoe (o selo D6 nao teria onde ser gravado) e o " +
					"predicado tem de o dizer")
			}
		})
	}
}

// TestAOS456ARunFilhoEImputadoAQuemPediuOPlano — o defeito deste ticket reaparecido NOUTRA PORTA, e
// fechado. O `aos-orq` drena a fila de pedidos de plano e submete TODOS os runs-filho sob o SEU
// principal: imputar ao chamador colapsaria os planos de todos os humanos num único tecto, e dois
// humanos com planos distintos veriam `429` um por causa do outro — exactamente a starvation que o
// ticket existe para fechar. Foi um dos achados medidos na revisão da tentativa 1.
//
// A imputação vai ao `RequestedBy`, que é DERIVADO PELO NÓ (AOS-439) e não um campo de corpo.
func TestAOS456ARunFilhoEImputadoAQuemPediuOPlano(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	t.Cleanup(func() { _ = node.Close() })
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute),
		WithInFlightPerCaller(1))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}

	// Um run-filho JÁ em curso, submetido pelo drenador em nome de ALICE.
	filhoDaAlice := agentruntime.Goal{RunID: "plano-a~no1", Objective: "x", MaxTurns: 1}
	filhoDaAlice.Principal.NHIID = "nhi:aos-orq" // o DRENADOR
	filhoDaAlice.Principal.RequestedBy = "human:alice"
	if err := svc.submit(context.Background(), filhoDaAlice, false); err != nil {
		t.Fatalf("o 1o run-filho devia ser admitido: %v", err)
	}

	// (1) OUTRO run-filho, do plano de BOB, pelo MESMO drenador: TEM de ser admitido. Se a
	// imputação fosse ao chamador, o tecto de 1 do `aos-orq` já estaria cheio e bob levava 429 por
	// causa de alice — com o agravante de o drenador ser o único chamador de todos os planos.
	filhoDoBob := agentruntime.Goal{RunID: "plano-b~no1", Objective: "x", MaxTurns: 1}
	filhoDoBob.Principal.NHIID = "nhi:aos-orq"
	filhoDoBob.Principal.RequestedBy = "human:bob"
	if err := svc.submit(context.Background(), filhoDoBob, false); err == ErrCallerInFlightCeiling {
		t.Fatal("o run-filho do plano de BOB foi recusado pelo tecto ocupado pelo plano de ALICE — a " +
			"imputacao esta a ir ao DRENADOR, e todos os humanos partilham um tecto: e o defeito deste " +
			"ticket reaparecido na porta dos planos")
	} else if err != nil {
		t.Fatalf("o run-filho de bob devia ser admitido: %v", err)
	}

	// (2) CONTROLO, sem o qual (1) passaria com o tecto REMOVIDO: um SEGUNDO run-filho do plano de
	// ALICE bate no tecto dela. A imputação separa chamadores; não é uma isenção para planos.
	outroDaAlice := agentruntime.Goal{RunID: "plano-a~no2", Objective: "x", MaxTurns: 1}
	outroDaAlice.Principal.NHIID = "nhi:aos-orq"
	outroDaAlice.Principal.RequestedBy = "human:alice"
	if err := svc.submit(context.Background(), outroDaAlice, false); err != ErrCallerInFlightCeiling {
		t.Fatalf("CONTROLO: o 2o run-filho do plano de ALICE devia bater no tecto dela (1), deu %v — sem "+
			"esta recusa o caso (1) acima passaria com o tecto desligado", err)
	}

	// (3) E um run que NÃO é de plano (RequestedBy vazio) é imputado a quem chama — o drenador tem
	// o seu próprio tecto, e não fica isento por submeter planos.
	proprioDoOrq := agentruntime.Goal{RunID: "orq-proprio", Objective: "x", MaxTurns: 1}
	proprioDoOrq.Principal.NHIID = "nhi:aos-orq"
	if got := imputadoA(proprioDoOrq); got != "nhi:aos-orq" {
		t.Fatalf("sem RequestedBy a imputacao e a quem CHAMA, veio %q", got)
	}
	if got := imputadoA(filhoDaAlice); got != "human:alice" {
		t.Fatalf("com RequestedBy a imputacao e a quem PEDIU o plano, veio %q", got)
	}
}

// TestAOS456AServeAPIComporEAnunciarNoARRANQUEREAL — o sensor que FALTAVA, e a lacuna foi apanhada
// por revisão adversarial: duas mutações no wiring de [serveAPI] sobreviviam à suite INTEIRA.
//
//	N1: remover `&& gateComposto` da condição de composição  -> tecto sobre principal do CORPO
//	N2: passar `true` fixo ao banner                          -> anuncia VERIFICADO sempre
//
// A N2 é LITERALMENTE o defeito ALTO da tentativa 1, e era reintroduzível sem uma linha vermelha: o
// teste do banner injecta os booleanos à mão e o teste do predicado compara-o com o handler —
// nenhum dos dois passa pelo `serveAPI`, que é o único sítio onde a decisão é tomada. Este teste
// arranca o servidor REAL, com as variáveis REAIS, e lê o banner que sai.
func TestAOS456AServeAPIComporEAnunciarNoARRANQUEREAL(t *testing.T) {
	// Arranca o servidor REAL numa porta conhecida, corre `medir` contra ele, encerra, e devolve o
	// banner. A porta vem de [portaLivreLoopback] porque `serveAPI` recebe um endereço e não um
	// listener — sem isso o teste leria o texto sem poder MEDIR o comportamento, que foi
	// exactamente a lacuna que deixou a mutação N1 sobreviver.
	arrancarMedirELerDobra456 := func(t *testing.T, node *Node, medir func(t *testing.T, addr string)) string {
		t.Helper()
		addr := portaLivreLoopback(t)
		var out syncBuf
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- serveAPI(ctx, &out, node, addr) }()
		// ESPERA PELO LISTENER em vez de um Sleep fixo (achado da segunda revisão: um sleep de 250 ms
		// é um flake latente — o POST do caso (A) faz `t.Fatalf` se a porta ainda não estiver aberta).
		esperarPorta(t, addr)
		if medir != nil {
			medir(t, "http://"+addr)
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("serveAPI devia encerrar graciosamente, veio %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("serveAPI nao encerrou apos cancelamento do ctx")
		}
		// A DOBRA DESTE EIXO, e não a saída inteira — sem isto as asserções dos cinco casos abaixo
		// podem ser satisfeitas por vocabulário de OUTRA dobra do banner, e foi o que aconteceu: a
		// dobra do SSE (AOS-459/460) usa «NAO COMPOSTO», «DEMO-GRADE» e «LIGADO sobre principal
		// VERIFICADO» exactamente como esta, e passou a satisfazer quatro dos cinco casos por si só.
		// Medido pela sétima revisão adversarial, com a dobra AOS-456 inteira neutralizada: detecção
		// 5/5 antes do AOS-459/460, **1/5 depois**. Isto é o AOS-461 a repor o sensor.
		//
		// [dobraDoEixo] também aborta se a dobra não existir de todo, o que os `strings.Contains` de
		// tipo «NÃO pode declarar X» não conseguiam ver: passavam num banner vazio.
		return dobraDoEixo(t, out.String(), marcadorDobra456)
	}

	// (A) NÓ LEGADO (sem gate soberano) com a variável DEFINIDA. O tecto NÃO se compõe, e o banner
	// tem de dizer «NAO COMPOSTO». Mata a N1: se a condição perder o `gateComposto`, o banner
	// continua a dizer «NAO COMPOSTO» (deriva do mesmo booleano) mas o tecto fica LIGADO sobre um
	// principal do corpo — por isso o caso (A) também MEDE o comportamento, não só o texto.
	t.Run("no LEGADO: NAO compoe e NAO anuncia", func(t *testing.T) {
		clearIngressEnv(t)
		t.Setenv("AOS_INGRESS_MAX_INFLIGHT_PER_CALLER", "1")
		// MODELO BLOQUEADO, e é a diferença entre um sensor e um teste que passa sempre: com o
		// `countingModel` o primeiro run TERMINA antes do segundo POST, `s.runs` esvazia-se e a
		// medição abaixo passaria mesmo com o tecto composto. Foi assim que a mutação N1 sobreviveu
		// à primeira versão deste caso.
		model := &aos456Bloqueado{entrou: make(chan struct{}, 16), release: make(chan struct{})}
		t.Cleanup(model.libertar)
		node, _ := newAPINode(t, model, true)
		t.Cleanup(func() { _ = node.Close() })
		node.SovereignReadRegions = nil
		node.SovereignAuthority = nil

		// MEDE, e não só lê: com o tecto a 1 e o nó em modo LEGADO, DUAS submissões do mesmo
		// `principal_nhi` do corpo TÊM de passar. Se a condição de composição perder o
		// `gateComposto` (mutação N1), a segunda leva 429 e este caso avermelha — que é o que o
		// texto do banner sozinho não conseguia ver.
		banner := arrancarMedirELerDobra456(t, node, func(t *testing.T, base string) {
			for i := 0; i < 2; i++ {
				corpo, _ := json.Marshal(map[string]any{
					"run_id": fmt.Sprintf("legado-%02d", i), "objective": "x",
					"principal_nhi": "nhi:mesmo", "credential": credencialDeTeste(t, node),
				})
				resp, err := http.Post(base+"/runs", "application/json", bytes.NewReader(corpo))
				if err != nil {
					t.Fatalf("POST /runs: %v", err)
				}
				st := resp.StatusCode
				_ = resp.Body.Close()
				if st == http.StatusTooManyRequests {
					t.Fatalf("submissao %d do mesmo principal_nhi levou 429 num no LEGADO com o tecto a 1 "+
						"— o tecto foi COMPOSTO sobre um principal que vem do CORPO do pedido, que o chamador "+
						"escolhe. E a mutacao N1, e um tecto contornavel mudando um campo do corpo", i+1)
				}
				if st != http.StatusCreated {
					t.Fatalf("submissao %d devia dar 201 num no legado, veio %d", i+1, st)
				}
			}
		})
		if !strings.Contains(banner, "NAO COMPOSTO") {
			t.Fatalf("num no LEGADO o banner devia declarar NAO COMPOSTO; saiu:\n%s", banner)
		}
		for _, proibido := range []string{"LIGADO sobre principal VERIFICADO", "LIGADO sobre principal DEMO-GRADE"} {
			if strings.Contains(banner, proibido) {
				t.Fatalf("num no LEGADO o banner NAO pode declarar %q; saiu:\n%s", proibido, banner)
			}
		}
	})

	// (B) NÓ COM GATE mas SEM credencial forte: compõe, e anuncia DEMO-GRADE. Mata a N2 — um `true`
	// fixo no banner produziria «VERIFICADO» aqui.
	t.Run("gate composto SEM credencial forte: compoe e anuncia DEMO-GRADE", func(t *testing.T) {
		clearIngressEnv(t)
		t.Setenv("AOS_INGRESS_MAX_INFLIGHT_PER_CALLER", "2")
		// MODELO BLOQUEADO: com o `countingModel` os runs terminam entre submissões e `s.runs`
		// esvazia-se, pelo que a medição abaixo passaria com o tecto DESLIGADO. A primeira versão
		// deste caso usava-o — e o sensor novo apanhou-se a si mesmo antes de eu o empurrar.
		model := &aos456Bloqueado{entrou: make(chan struct{}, 64), release: make(chan struct{})}
		t.Cleanup(model.libertar)
		node, _ := newAPINode(t, model, true)
		t.Cleanup(func() { _ = node.Close() })
		node.SovereignReadRegions = govsov.NewRegistry(map[string]string{"board:demo": "eu"})
		node.SovereignReadCredential = nil // a postura que o achado descobriu

		// MEDE, e é o achado da SEGUNDA revisão adversarial: a decisão deliberada «o tecto CONTINUA
		// composto na postura DEMO-GRADE, porque vale contra rajada honesta» estava escrita em quatro
		// sítios e fixada em ZERO testes. A mutação que compõe o tecto só com credencial forte
		// (`&& gateComposto` → `&& principalVerificavel` em serveAPI) sobrevivia à suite inteira, e
		// deixava o banner a dizer «LIGADO … vale contra rajada HONESTA» com NADA composto — o defeito
		// ALTO da tentativa 1, na única postura que um nó configurado por env fora de produção alcança.
		//
		// O sensor é o MESMO header repetido: com o tecto a 2 e os runs presos no modelo, a 3.ª
		// submissão do mesmo `X-Aos-Reader` TEM de levar 429. (Que o header ROTATIVO passe é a
		// fronteira desta postura, medida no caso (F) abaixo — aqui prova-se que o mecanismo está
		// LIGADO, não que é inforjável.)
		banner := arrancarMedirELerDobra456(t, node, func(t *testing.T, base string) {
			cred := credencialDeTeste(t, node)
			admitidas, recusadas := 0, 0
			for i := 0; i < 8; i++ {
				corpo, _ := json.Marshal(map[string]any{
					"run_id": fmt.Sprintf("demo-%02d", i), "objective": "x", "credential": cred,
				})
				req, _ := http.NewRequest(http.MethodPost, base+"/runs", bytes.NewReader(corpo))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set(HeaderReaderPrincipal, "human:mesmo") // O MESMO, sempre
				req.Header.Set(HeaderReaderBoard, "board:demo")
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatalf("POST /runs: %v", err)
				}
				switch resp.StatusCode {
				case http.StatusCreated:
					admitidas++
				case http.StatusTooManyRequests:
					recusadas++
				}
				_ = resp.Body.Close()
			}
			if recusadas == 0 {
				t.Fatalf("8 submissoes do MESMO header com o tecto a 2: %d admitidas, 0 recusadas — o "+
					"tecto NAO esta composto nesta postura, e o banner diz que esta. E a decisao "+
					"deliberada («vale contra rajada honesta») a nao ser imposta", admitidas)
			}
			if admitidas == 0 {
				t.Fatal("NENHUMA submissao admitida — o teste nao esta a medir o tecto")
			}
		})
		if !strings.Contains(banner, "DEMO-GRADE") {
			t.Fatalf("com o gate composto e SEM credencial forte o banner devia declarar DEMO-GRADE — "+
				"anunciar VERIFICADO aqui e o defeito ALTO da tentativa 1; saiu:\n%s", banner)
		}
		// E tem de dizer COMO se contorna, senão o aviso não é accionável.
		for _, exigido := range []string{"X-Aos-Reader", "CONTORNA-SE", "AOS_SOVEREIGN_OIDC_ISSUER"} {
			if !strings.Contains(banner, exigido) {
				t.Fatalf("o aviso DEMO-GRADE devia nomear %q; saiu:\n%s", exigido, banner)
			}
		}
		if strings.Contains(banner, "LIGADO sobre principal VERIFICADO") {
			t.Fatalf("banner declara VERIFICADO sem credencial forte composta; saiu:\n%s", banner)
		}
	})

	// (C) NÓ COM AUTORIDADE **E** CREDENCIAL FORTE: anuncia VERIFICADO. Sem este caso, um banner que
	// dissesse DEMO-GRADE sempre passaria (A) e (B) — o ramo VERIFICADO tem de ser alcançável.
	//
	// A AUTORIDADE É COMPOSTA A SÉRIO, e a primeira versão deste caso não a compunha — punha só o
	// registo board→região e a credencial, que é precisamente o estado que o caso (E) abaixo agora
	// fixa como NÃO-verificado. O sensor validava o estado errado e não cobria o caminho que o
	// `Bootstrap` produz (onde registo ⇒ autoridade). Achado da SEGUNDA revisão adversarial.
	t.Run("autoridade E credencial forte: anuncia VERIFICADO", func(t *testing.T) {
		clearIngressEnv(t)
		t.Setenv("AOS_INGRESS_MAX_INFLIGHT_PER_CALLER", "3")
		node, _ := newAPINode(t, &countingModel{}, true)
		t.Cleanup(func() { _ = node.Close() })
		auth, err := NewSovereignRegionAuthority(context.Background(),
			map[string]string{"board:demo": "eu"}, node.WORM, time.Now)
		if err != nil {
			t.Fatalf("NewSovereignRegionAuthority: %v", err)
		}
		node.SovereignAuthority = auth
		node.SovereignReadRegions = auth.Registry() // como o Bootstrap o faz (bootstrap.go:2457)
		node.SovereignReadCredential = credencialDeLeituraInerte{}

		banner := arrancarMedirELerDobra456(t, node, nil)
		if !strings.Contains(banner, "LIGADO sobre principal VERIFICADO") {
			t.Fatalf("com autoridade E credencial forte o banner devia declarar VERIFICADO; saiu:\n%s", banner)
		}
		if strings.Contains(banner, "DEMO-GRADE") {
			t.Fatalf("banner declara DEMO-GRADE com credencial forte composta; saiu:\n%s", banner)
		}
	})

	// (E) O ESTADO QUE A SEGUNDA REVISÃO ADVERSARIAL DESCOBRIU: registo board→região **e**
	// credencial forte, mas SEM autoridade. Aqui a credencial está composta no nó e **IGNORADA**
	// pelo handler — `NewAPIHandler` cai no `case node.SovereignReadRegions != nil:` e passa `nil`,
	// pelo que o `autorizarComCausa` lê o principal do header.
	//
	// O banner NÃO pode dizer VERIFICADO. Antes desta correcção dizia, e a medição foi 60
	// submissões com o header a rodar, todas admitidas com o tecto a 2 — o ALTO-1 da primeira
	// revisão, uma camada mais abaixo.
	//
	// Pelo `Bootstrap` o estado é inalcançável (registo ⇒ autoridade); o caso existe porque o
	// predicado não pode depender dessa coincidência para estar certo, e porque era este o estado
	// que o caso (C) compunha.
	t.Run("credencial COMPOSTA mas IGNORADA pelo handler: NAO e VERIFICADO", func(t *testing.T) {
		clearIngressEnv(t)
		t.Setenv("AOS_INGRESS_MAX_INFLIGHT_PER_CALLER", "2")
		model := &aos456Bloqueado{entrou: make(chan struct{}, 128), release: make(chan struct{})}
		t.Cleanup(model.libertar)
		node, _ := newAPINode(t, model, true)
		t.Cleanup(func() { _ = node.Close() })
		node.SovereignAuthority = nil // SEM autoridade: o handler ignora a credencial
		node.SovereignReadRegions = govsov.NewRegistry(map[string]string{"board:demo": "eu"})
		node.SovereignReadCredential = credencialDeLeituraInerte{}

		if principalDoRunEVerificavel(node) {
			t.Fatal("o predicado diz VERIFICADO com a credencial COMPOSTA mas IGNORADA pelo handler " +
				"(NewAPIHandler passa nil no ramo do registo) — o principal vem do header, e um tecto " +
				"sobre um header contorna-se rodando-o")
		}
		banner := arrancarMedirELerDobra456(t, node, nil)
		if strings.Contains(banner, "LIGADO sobre principal VERIFICADO") {
			t.Fatalf("banner declara VERIFICADO sobre um principal que vem do HEADER; saiu:\n%s", banner)
		}
		if !strings.Contains(banner, "DEMO-GRADE") {
			t.Fatalf("banner devia declarar DEMO-GRADE neste estado; saiu:\n%s", banner)
		}
	})

	// (D) A VARIÁVEL AUSENTE não pode anunciar tecto nenhum, em nó nenhum.
	t.Run("variavel ausente: NAO CONFIGURADO", func(t *testing.T) {
		clearIngressEnv(t)
		node, _ := newAPINode(t, &countingModel{}, true)
		t.Cleanup(func() { _ = node.Close() })
		node.SovereignReadRegions = govsov.NewRegistry(map[string]string{"board:demo": "eu"})
		node.SovereignReadCredential = credencialDeLeituraInerte{}

		banner := arrancarMedirELerDobra456(t, node, nil)
		if !strings.Contains(banner, "NAO CONFIGURADO") {
			t.Fatalf("sem a variavel o banner devia declarar NAO CONFIGURADO; saiu:\n%s", banner)
		}
	})
}

// credencialDeLeituraInerte compõe o campo `SovereignReadCredential` sem trazer um IdP: este
// ficheiro só precisa que ele esteja NÃO-NULO, porque é isso que [principalDoRunEVerificavel] lê. O
// `verify` nega sempre — o que é a postura fail-closed correcta para uma credencial sem IdP, e
// impede que alguém use este tipo como atalho para autorizar algo.
type credencialDeLeituraInerte struct{}

func (credencialDeLeituraInerte) verify(context.Context, *http.Request) (string, string, error) {
	return "", "", ErrNoReadCredential
}

// TestAOS456AAOrdemDasGuardasEidempotenciaDaReSUBMISSAO — a ordem das guardas em [NodeService.submit]
// é *load-bearing* e não tinha sensor nenhum (achado da segunda revisão adversarial: mover o bloco do
// tecto para ANTES das guardas duplicado/suspenso/completado passava a suite inteira).
//
// A CONSEQUÊNCIA, medida: com o chamador NO tecto, uma re-submissão do MESMO `run_id` responde hoje
// «duplicado» (idempotente); com a ordem trocada responderia 429. Um retry de rede de um cliente no
// tecto passaria a ser recusado por quota para sempre — e o `nodeClient.Submit` do `aos-orq` DEPENDE
// dessa idempotência: ele repete o `submeter` por passagem, e um 429 aí aborta a passagem inteira.
//
// A regra que este teste fixa: **o tecto é a ÚLTIMA guarda**. Um pedido que já tem resposta
// determinada pelo ESTADO do run (duplicado, suspenso, terminado) recebe essa resposta, não uma quota.
func TestAOS456AAOrdemDasGuardasEidempotenciaDaReSUBMISSAO(t *testing.T) {
	novo := func(t *testing.T) *NodeService {
		t.Helper()
		node, _ := newAPINode(t, &countingModel{}, false)
		t.Cleanup(func() { _ = node.Close() })
		svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute),
			WithInFlightPerCaller(1))
		if err != nil {
			t.Fatalf("NewNodeService: %v", err)
		}
		return svc
	}

	// (1) DUPLICADO ganha ao tecto: o run já está em curso, e o chamador está no tecto por causa dele.
	t.Run("re-submissao de run EM CURSO responde duplicado, nao 429", func(t *testing.T) {
		svc := novo(t)
		svc.mu.Lock()
		svc.runs["ja-existe"] = &runState{runID: "ja-existe", done: make(chan struct{}), principal: "human:alice"}
		svc.mu.Unlock()
		g := agentruntime.Goal{RunID: "ja-existe", Objective: "x", MaxTurns: 1}
		g.Principal.NHIID = "human:alice"
		err := svc.submit(context.Background(), g, false)
		if err == ErrCallerInFlightCeiling {
			t.Fatal("a re-submissao do MESMO run_id respondeu com o TECTO em vez de «duplicado» — a ordem " +
				"das guardas foi trocada. Um retry de rede de um cliente no tecto fica recusado por quota " +
				"para sempre, e o aos-orq depende desta idempotencia (repete o submeter por passagem)")
		}
		if err != ErrRunAlreadyInProgress {
			t.Fatalf("esperava ErrRunAlreadyInProgress, veio %v", err)
		}
	})

	// (2) SUSPENSO ganha ao tecto: senão um run à espera de um humano fica irretomável por quota — e o
	// chamador nem sabe que ele existe.
	t.Run("re-submissao de run SUSPENSO responde suspenso, nao 429", func(t *testing.T) {
		svc := novo(t)
		svc.mu.Lock()
		svc.runs["outro"] = &runState{runID: "outro", done: make(chan struct{}), principal: "human:alice"}
		svc.suspended["suspenso"] = &runState{runID: "suspenso", done: make(chan struct{}), principal: "human:alice"}
		svc.mu.Unlock()
		g := agentruntime.Goal{RunID: "suspenso", Objective: "x", MaxTurns: 1}
		g.Principal.NHIID = "human:alice"
		if err := svc.submit(context.Background(), g, false); err != ErrRunSuspended {
			t.Fatalf("esperava ErrRunSuspended (o estado do run ganha ao tecto), veio %v", err)
		}
	})

	// (3) TERMINADO ganha ao tecto, pela mesma razão: o desfecho é a resposta certa.
	t.Run("re-submissao de run TERMINADO responde terminado, nao 429", func(t *testing.T) {
		svc := novo(t)
		svc.mu.Lock()
		svc.runs["outro"] = &runState{runID: "outro", done: make(chan struct{}), principal: "human:alice"}
		svc.completed["feito"] = &runState{runID: "feito", done: make(chan struct{}), principal: "human:alice"}
		svc.mu.Unlock()
		g := agentruntime.Goal{RunID: "feito", Objective: "x", MaxTurns: 1}
		g.Principal.NHIID = "human:alice"
		if err := svc.submit(context.Background(), g, false); err != ErrRunAlreadyCompleted {
			t.Fatalf("esperava ErrRunAlreadyCompleted (o desfecho ganha ao tecto), veio %v", err)
		}
	})

	// (4) CONTROLO: um run_id NOVO do chamador no tecto leva 429. Sem isto, (1)–(3) passariam com o
	// tecto removido.
	t.Run("CONTROLO: run_id NOVO do chamador no tecto leva o tecto", func(t *testing.T) {
		svc := novo(t)
		svc.mu.Lock()
		svc.runs["ja-existe"] = &runState{runID: "ja-existe", done: make(chan struct{}), principal: "human:alice"}
		svc.mu.Unlock()
		g := agentruntime.Goal{RunID: "novo", Objective: "x", MaxTurns: 1}
		g.Principal.NHIID = "human:alice"
		if err := svc.submit(context.Background(), g, false); err != ErrCallerInFlightCeiling {
			t.Fatalf("CONTROLO: um run_id NOVO devia levar o tecto, veio %v — sem esta recusa os casos "+
				"(1)-(3) acima passam com o tecto desligado", err)
		}
	})
}

// TestAOS456AOBannerDeclaraOQueOEIXONAODA — o commit deste ticket afirmou das três frases do bloco
// `alcance` que «são achados de revisão adversarial e NENHUMA é opcional». Nada as prendia: zerar o
// bloco `alcance` passava a suite inteira (achado da segunda revisão).
//
// Cada uma destas frases existe porque a sua ausência já enganou alguém neste ticket. São o que o
// operador precisa de saber para NÃO supor mais do que o eixo dá.
func TestAOS456AOBannerDeclaraOQueOEIXONAODA(t *testing.T) {
	lim := ingressLimits{ratePerSec: 10, burst: 20, maxInFlight: 50, inFlightPerCaller: 4}
	exigencias := []struct{ marcador, porque string }{
		{"NAO e um tecto de OCUPACAO", "as duas isencoes compoem-se: mediram-se 20 runs com o tecto a 1"},
		{"SUSPENSO", "um run suspenso sai da contagem"},
		{"RETOMA (/resume) NAO a consulta", "a retoma e isenta, e isso aumenta os runs vivos por chamador"},
		{"GASTA um token do balde GLOBAL", "a recusa por-chamador consome taxa comum — o criterio que era FALSO"},
		{"AOS-456b", "justica em TAXA nao esta feita, e o operador tem de saber que nao esta"},
		{"PISO PRATICO", "um valor demasiado baixo parte planos com fan-out do aos-orq"},
	}
	// Nas DUAS posturas em que o tecto está em vigor: o alcance não pode existir só numa.
	for _, postura := range []struct {
		nome                    string
		gate, principalVerifica bool
	}{
		{"DEMO-GRADE", true, false},
		{"VERIFICADO", true, true},
	} {
		t.Run(postura.nome, func(t *testing.T) {
			txt := strings.Join(ingressPostureBanner(lim, postura.gate, postura.principalVerifica), "\n")
			for _, e := range exigencias {
				if !strings.Contains(txt, e.marcador) {
					t.Errorf("o banner NAO declara %q — %s\n--- banner ---\n%s", e.marcador, e.porque, txt)
				}
			}
		})
	}
}

// marcadoresDeDobra é o REGISTO das dobras por-eixo do banner de ingresso, e existe para que
// acrescentar uma dobra passe a delimitar automaticamente a anterior.
//
// O AOS-460 delimitava a dobra do SSE com um sentinela (`"\x00"`), o que a fazia ir até ao fim do
// texto: a dobra seguinte que alguém acrescentasse ficava DENTRO dela e repunha exactamente a
// confusão de vocabulário que o AOS-460 diagnosticou (achado BAIXO-3 da sétima revisão). Com o
// registo, [dobraDoEixo] fecha cada dobra no próximo marcador registado que apareça depois dela.
//
// LIMITE DECLARADO: quem acrescentar uma dobra e NÃO a registar aqui volta a alargar a anterior. É
// isso que [TestAOS461TodasAsDobrasDoBannerEstaoREGISTADAS] vigia — e vigia-o só para dobras que
// seguem a convenção de nome «TECTO … (AOS-NNN):», que é a das duas que existem.
var marcadoresDeDobra = []string{
	marcadorDobra456,
	marcadorDobraSSE,
}

const (
	marcadorDobra456 = "TECTO POR-CHAMADOR (AOS-456)"
	marcadorDobraSSE = "TECTO DE STREAMS SSE POR LEITOR (AOS-459)"
)

// dobraDoEixo devolve a parte do banner que começa em `inicio` (um dos [marcadoresDeDobra]) e
// termina no próximo marcador registado, ou no fim do texto se `inicio` for a última dobra.
//
// Existe porque o banner de ingresso acumula dobras de eixos diferentes com vocabulário partilhado
// («NAO COMPOSTO», «DEMO-GRADE», «LIGADO sobre principal VERIFICADO»), e uma asserção sobre o texto
// inteiro não distingue qual eixo a satisfez — nem a satisfaz o eixo certo: foi assim que a dobra do
// SSE, acrescentada pelo AOS-459/460, desarmou quatro asserções do sensor de arranque real do
// AOS-456a (5/5 → 1/5 de detecção, medido pela sétima revisão adversarial).
func dobraDoEixo(t *testing.T, banner, inicio string) string {
	t.Helper()
	if !slices.Contains(marcadoresDeDobra, inicio) {
		t.Fatalf("a dobra %q nao esta em marcadoresDeDobra — sem registo nao ha como fecha-la, e a "+
			"dobra ANTERIOR passaria a engoli-la", inicio)
	}
	i := strings.Index(banner, inicio)
	if i < 0 {
		t.Fatalf("o banner NAO tem a dobra %q — a assercao seguinte nao mediria o eixo certo:\n%s", inicio, banner)
	}
	resto := banner[i+len(inicio):]
	fim := len(resto)
	for _, m := range marcadoresDeDobra {
		if m == inicio {
			continue
		}
		if j := strings.Index(resto, m); j >= 0 && j < fim {
			fim = j
		}
	}
	return inicio + resto[:fim]
}

// TestAOS461TodasAsDobrasDoBannerEstaoREGISTADAS — sem isto, [marcadoresDeDobra] é uma lista que
// envelhece em silêncio: a dobra nova fica de fora, a anterior volta a ir até ao fim do texto, e as
// asserções sobre ela voltam a poder ser satisfeitas por vocabulário de outro eixo.
//
// VARRE TODAS AS COMBINAÇÕES DE POSTURA, e a primeira versão varria UMA (achado MÉDIO-4 da oitava
// revisão adversarial). Varrer uma postura basta para as duas dobras de hoje — o marcador de cada uma
// está nos quatro ramos do seu `switch` — e é **falso para uma dobra condicional**, que é a forma do
// ramo INERTE que já existe. Medido pela revisão: uma dobra `TECTO DE Z (AOS-465):` emitida só quando
// `!gateComposto` tem nome conforme e **escapava** à varredura, voltando a alargar a dobra anterior em
// silêncio.
//
// LIMITE QUE FICA, e é honesto: uma dobra cujo nome NÃO siga a convenção «TECTO … (AOS-NNN):» não é
// apanhada por nenhuma destas combinações. Fechá-lo exigiria que o banner declarasse as suas próprias
// dobras em vez de as escrever em texto livre — vale a pena quando houver uma terceira, não antes.
func TestAOS461TodasAsDobrasDoBannerEstaoREGISTADAS(t *testing.T) {
	// AS FORMAS DE `lim` que mudam de RAMO em alguma dobra: tectos ligados, tectos desligados, e o par
	// inerte (`por-leitor >= global`), que é o ramo condicional do banner.
	formas := map[string]ingressLimits{
		"tectos ligados": {ratePerSec: 1, burst: 1, maxInFlight: 8, inFlightPerCaller: 2,
			trajMaxConns: 8, trajMaxConnsPerReader: 2},
		"tectos desligados": {ratePerSec: 1, burst: 1, maxInFlight: 8, inFlightPerCaller: 0,
			trajMaxConns: 8, trajMaxConnsPerReader: 0},
		"par INERTE": {ratePerSec: 1, burst: 1, maxInFlight: 8, inFlightPerCaller: 2,
			trajMaxConns: 4, trajMaxConnsPerReader: 4},
	}
	re := regexp.MustCompile(`TECTO [^:]*\(AOS-\d+\):`)
	vistos := map[string]bool{}
	for nomeForma, lim := range formas {
		for _, gate := range []bool{false, true} {
			for _, verif := range []bool{false, true} {
				banner := strings.Join(ingressPostureBanner(lim, gate, verif), "\n")
				achados := re.FindAllString(banner, -1)
				if len(achados) == 0 {
					t.Fatalf("%s/gate=%v/verif=%v: nenhum marcador de dobra no banner — a varredura "+
						"deixou de medir o que quer que fosse", nomeForma, gate, verif)
				}
				for _, a := range achados {
					nome := strings.TrimSuffix(a, ":")
					vistos[nome] = true
					if !slices.Contains(marcadoresDeDobra, nome) {
						t.Errorf("%s/gate=%v/verif=%v: a dobra %q aparece no banner e NAO esta em "+
							"marcadoresDeDobra: dobraDoEixo nao a consegue fechar, e a dobra anterior "+
							"a esta engole-a — registe-a", nomeForma, gate, verif, nome)
					}
				}
			}
		}
	}
	// E o inverso: um marcador registado que já não apareça em NENHUMA postura deixa dobraDoEixo a
	// delimitar por um texto morto, e a dobra anterior a ir longe demais outra vez.
	for _, m := range marcadoresDeDobra {
		if !vistos[m] {
			t.Errorf("o marcador registado %q NAO aparece em nenhuma das %d posturas varridas — "+
				"registo obsoleto", m, len(formas)*4)
		}
	}
}
