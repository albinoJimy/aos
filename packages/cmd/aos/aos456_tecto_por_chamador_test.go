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
// composto (a única postura em que o principal é VERIFICADO e o tecto entra em vigor), e leem os
// códigos que dois chamadores distintos recebem. Nenhum deles chama `svc.submit` para provar o
// critério; os dois que o chamam directamente provam ramos que o HTTP não alcança (a isenção da
// retoma) e dizem-no.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// TestAOS456AUmaRECUSANaoConsomeRecursoPartilhado — a assimetria em que o desenho assenta (ver
// `docs/reports/AOS-456-desenho-do-trade-off-de-ordem.md`): um pedido RECUSADO não ocupa lugar
// nenhum, ao contrário de um token de balde, que se GASTA na recusa. Se a recusa consumisse
// recurso partilhado, 40 recusas de alice bloqueariam bob — que foi o defeito da tentativa 1.
func TestAOS456AUmaRECUSANaoConsomeRecursoPartilhado(t *testing.T) {
	const tecto = 1
	srv, cred := noSoberanoComTecto(t, tecto)

	if st := submeterComo(t, srv, cred, "human:alice", "a-000"); st != http.StatusCreated {
		t.Fatalf("a 1a submissao de alice devia ser admitida, veio %d", st)
	}
	// 40 recusas seguidas.
	for i := 1; i <= 40; i++ {
		if st := submeterComo(t, srv, cred, "human:alice", fmt.Sprintf("a-%03d", i)); st != http.StatusTooManyRequests {
			t.Fatalf("alice: submissao %d devia ser recusada com o tecto em 1, veio %d", i+1, st)
		}
	}
	// Bob continua a ser admitido. E o seu tecto está INTACTO — as 40 recusas de alice não lhe
	// gastaram nada.
	if st := submeterComo(t, srv, cred, "human:bob", "b-000"); st != http.StatusCreated {
		t.Fatalf("bob levou %d depois de 40 pedidos RECUSADOS de alice — uma recusa esta a consumir "+
			"recurso partilhado, que e precisamente o defeito da tentativa 1", st)
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
	// Sem `Shutdown` no cleanup, pelo molde de `newAPI` (api_test.go): com o modelo BLOQUEADO um
	// shutdown ficaria à espera de runs que só terminam quando o modelo é libertado, e a ordem LIFO
	// dos cleanups libertá-lo-ia depois. Os runs caem com o contexto do nó.

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
	// Sem `Shutdown` no cleanup, pelo molde de `newAPI` (api_test.go): com o modelo BLOQUEADO um
	// shutdown ficaria à espera de runs que só terminam quando o modelo é libertado, e a ordem LIFO
	// dos cleanups libertá-lo-ia depois. Os runs caem com o contexto do nó.

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
		verificav bool
		exige     []string
		proibe    []string
	}{
		// Sem config, a verificabilidade é irrelevante: não há tecto nenhum para anunciar.
		{"nao configurado (com gate)", base, true,
			[]string{"NAO CONFIGURADO"}, []string{"LIGADO —", "NAO COMPOSTO", "(4)"}},
		{"nao configurado (sem gate)", base, false,
			[]string{"NAO CONFIGURADO"}, []string{"LIGADO —", "NAO COMPOSTO"}},
		{"configurado mas NAO composto", comTecto(), false,
			[]string{"NAO COMPOSTO", "(4)", "auto-declarado"}, []string{"LIGADO —", "NAO CONFIGURADO"}},
		{"ligado", comTecto(), true,
			[]string{"LIGADO —", "4 run(s)", "429"}, []string{"NAO COMPOSTO", "NAO CONFIGURADO"}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			txt := strings.Join(ingressPostureBanner(c.lim, c.verificav), "\n")
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
func TestAOS456AEnvFailClosedNOSDOISSENTIDOS(t *testing.T) {
	casos := []struct {
		valor  string
		global string
		aceita bool
		porque string
	}{
		{"", "", true, "vazia => tecto NAO COMPOSTO, que e o default"},
		{"4", "", true, "dentro do global por omissao (512)"},
		{"512", "", true, "IGUAL ao global por omissao: degenera no global, e e coerente"},
		{"0", "", false, "zero NAO desliga: seria a armadilha inversa do AOS_INGRESS_MAX_INFLIGHT"},
		{"-1", "", false, "negativo"},
		{"abc", "", false, "ilegivel"},
		{"2.5", "", false, "nao-inteiro: um tecto de lugares e um inteiro"},
		{"513", "", false, "ACIMA do global por omissao => barreira INERTE"},
		{"999999", "", false, "muito acima do global => barreira INERTE (o caso que a tentativa 1 aceitava)"},
		{"8", "4", false, "acima do global EXPLICITO"},
		{"4", "4", true, "igual ao global explicito"},
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

// BenchmarkAOS456ContagemPorChamador mede o que o desenho deixou explicitamente NÃO VERIFICADO: o
// custo da varredura de `s.runs` na secção crítica do submit.
//
// PORQUE UMA VARREDURA E NÃO UM CONTADOR. Um `map[principal]int` incrementado na reserva seria
// O(1), mas teria de ser decrementado nos QUATRO sítios onde um run sai de `s.runs`
// (service.go:771, :821, :1184, :1195) — e uma entrada que fique a mais tranca o chamador para
// sempre, uma falha fail-CLOSED e silenciosa, pior do que a varredura. A contagem derivada de
// `s.runs` não pode dessincronizar-se porque não tem estado próprio. Este benchmark existe para
// que a troca seja feita com um número, e não com uma intuição: `s.runs` está limitado pelo tecto
// global (default 512), pelo que o pior caso é conhecido.
//
// MEDIDO (go test -bench, -benchtime 200000x, este contentor):
//
//	runs=1     42.5 ns/op
//	runs=64     718 ns/op
//	runs=512   6.65 µs/op   <- PIOR CASO (o tecto global por omissão)
//
// COMO LER 6.65 µs. Está DENTRO de `s.mu`, que serializa todas as submissões — é o número que
// importa, e não o custo por pedido. Dois pontos de comparação do MESMO pedido: a
// `ed25519.Verify` da credencial mede 59.9 µs (nove vezes mais, e FORA do mutex) e o rate-limit
// de ingresso por omissão admite 64 pedidos/segundo, quando esta secção crítica sozinha
// sustentaria ~150 mil. A varredura não é o gargalo em nenhuma configuração de referência.
//
// QUANDO DEIXARIA DE SER VERDADE: um operador que suba muito `AOS_INGRESS_RATE` **e** mantenha
// `AOS_INGRESS_MAX_INFLIGHT` no máximo. Fica declarado, não resolvido — trocar por um contador
// O(1) é uma optimização com um risco fail-CLOSED próprio (uma entrada que fique a mais tranca o
// chamador para sempre) e não se paga contra estes números.
func BenchmarkAOS456ContagemPorChamador(b *testing.B) {
	for _, n := range []int{1, 64, 512} {
		b.Run(fmt.Sprintf("runs=%d", n), func(b *testing.B) {
			runs := make(map[string]*runState, n)
			for i := 0; i < n; i++ {
				id := fmt.Sprintf("run-%04d", i)
				runs[id] = &runState{runID: id, principal: fmt.Sprintf("human:p%03d", i%16)}
			}
			alvo := "human:p007"
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c := 0
				for _, r := range runs {
					if r.principal == alvo {
						c++
					}
				}
				if c == -1 {
					b.Fatal("impossivel")
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
