package main

// AOS-464 — A FILA DE PEDIDOS DE PLANO REPARTE-SE POR SUBMISSOR.
//
// # O DEFEITO
//
// O `tectoDePendentes = 1000` de `plan_claim.go` protegia o NÓ e não dizia nada sobre QUEM ocupa a fila: um submissor
// autenticado enfileirava os 1000 e todos os outros levavam **503** em `POST /plans` até alguém
// drenar. Um pedido só sai da fila com desfecho terminal ou reclamação viva, e nenhum dos dois
// depende de quem submeteu — logo não é uma rajada que passa, é **ocupação que fica**.
//
// É o MESMO defeito que o AOS-456a fechou no `POST /runs`, um plano ao lado, e nenhuma das outras
// barreiras o cobria: o balde de admissão é de TAXA e global entre chamadores, o tecto de runs em
// curso conta runs HOSPEDADOS (esta rota não hospeda nenhum) e o `edge` tem `limit_req` e não
// `limit_conn`.
//
// # O QUE É DIFERENTE DO EIXO AOS-456/459, E IMPORTA
//
// A contagem NÃO vive em memória: sai da projecção da fila, que é derivada do log. Não há mapa a
// manter, não há caminho de libertação e não há TOCTOU — as duas contagens saem da MESMA leitura.
// O que há em troca é uma atribuição DEGENERADA quando o gate soberano não está composto: o corpo
// do pedido nunca declara o principal, pelo que sem `readGov` ele fica **vazio para todos**. Um
// tecto chaveado no vazio não seria contornável — seria um tecto GLOBAL mais apertado anunciado
// como equidade, e é por isso que a repartição não compõe nessa postura.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
)

// postPlanoComHeaders submete um pedido de plano com os headers de leitor dados. Existe porque o
// `postJSON` do pacote não leva headers, e neste eixo o header É a atribuição.
func postPlanoComHeaders(t *testing.T, h http.Handler, headers map[string]string, runID string) *httptest.ResponseRecorder {
	t.Helper()
	corpo, err := json.Marshal(map[string]any{"run_id": runID, "objective": "trabalho " + runID})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/plans", bytes.NewReader(corpo))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// aos464No compõe um nó com a fila activa e o gate soberano na postura DEMO-GRADE (registo
// board→região e NENHUMA credencial forte) — a que um nó com `AOS_BOARD_REGIONS` e sem OIDC tem, e
// a que a rota de planos precisa para imputar o pedido a alguém.
func aos464No(t *testing.T, opts ...APIOption) (*Node, http.Handler) {
	t.Helper()
	node, _ := newAPINode(t, &countingModel{}, false)
	t.Cleanup(func() { _ = node.Close() })
	auth, err := NewSovereignRegionAuthority(context.Background(),
		map[string]string{govBoard: govRegion, govBoardUS: govRegionUS}, node.WORM, time.Now)
	if err != nil {
		t.Fatalf("NewSovereignRegionAuthority: %v", err)
	}
	base := []APIOption{WithSovereignAuthority(auth, credDeHeaders{}, node.WORM), WithAPIClock(aos277Clock())}
	_, h := newAPI(t, node, append(base, opts...)...)
	return node, h
}

// TestAOS464DEMOGRADENaoCompoePorqueSeriaNegacaoDirigida — o ALTO que uma revisão adversarial
// independente mediu, e a razão pela qual este eixo recusa a postura que os gémeos aceitam.
//
// Com o gate composto e SEM credencial forte, o submissor vem do header `X-Aos-Reader`, que o chamador
// escreve. Nos eixos AOS-456a e AOS-459 isso deixa um atacante EVADIR o tecto dele — limitado: obtém o
// que obteria sem tecto nenhum. Aqui ele não precisa de evadir: escreve o header da VÍTIMA e gasta a
// quota dela, e a ocupação é DURÁVEL e gratuita (um pedido só sai com desfecho terminal ou reclamação
// viva, e nenhum dos dois depende de quem submeteu).
//
// MEDIDO na primeira versão deste ticket: 5 pedidos forjados fechavam uma vítima nomeada fora do
// `POST /plans` com **15 de 20 lugares globais LIVRES**, e a fronteira 201/429 contava-lhe os pendentes
// exactos dela. Com os defaults, 125 pedidos e 875 lugares livres, com `aos_plan_queue_pending` a ler
// 12,5% — um painel saudável. Este teste fixa que já não acontece; o CONTROLO fixa que o global morde.
func TestAOS464DEMOGRADENaoCompoePorqueSeriaNegacaoDirigida(t *testing.T) {
	const global, quota = 20, 5
	node, _ := newAPINode(t, &countingModel{}, false)
	t.Cleanup(func() { _ = node.Close() })
	regions := govsov.NewRegistry(map[string]string{govBoard: govRegion, govBoardUS: govRegionUS})
	// POSTURA DEMO-GRADE: gate composto, NENHUMA credencial forte (`readGov.cred == nil`).
	_, h := newAPI(t, node, WithReadSovereignty(regions, node.WORM),
		WithPlanMaxPending(global), WithPlanMaxPendingPerSubmitter(quota), WithAPIClock(aos277Clock()))

	// O ATACANTE gasta a quota da vítima, rodando só o header.
	for i := 0; i < quota; i++ {
		if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "forjado-"+strconv.Itoa(i)); rec.Code != http.StatusCreated {
			t.Fatalf("o pedido forjado %d devia ser aceite nesta postura (o tecto nao compoe), veio %d", i+1, rec.Code)
		}
	}
	// A VÍTIMA tem de continuar a poder submeter.
	rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "legitimo-da-alice")
	if rec.Code == http.StatusTooManyRequests {
		t.Fatalf("NEGACAO DIRIGIDA: %d pedidos forjados fecharam a vitima fora do POST /plans com %d de "+
			"%d lugares globais LIVRES. A reparticao COMPOS-SE sobre um principal FORJAVEL, e nesta "+
			"postura isso nao e um tecto contornavel — e um trinco de negacao dirigida, com ocupacao "+
			"DURAVEL e gratuita. A correccao e exigir `readGov.cred != nil` na atribuicao",
			quota, global-quota, global)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("a vitima devia ser admitida, veio %d: %s", rec.Code, rec.Body.String())
	}

	// O CONTROLO: o tecto GLOBAL continua a ser a barreira nesta postura. Sem ele, o caso acima
	// passaria por não haver tecto nenhum.
	for i := quota + 1; i < global; i++ {
		if r := postPlanoComHeaders(t, h, aos464Headers("human:bob"), "enche-"+strconv.Itoa(i)); r.Code != http.StatusCreated {
			t.Fatalf("a submissao %d devia encher o global, veio %d", i+1, r.Code)
		}
	}
	if r := postPlanoComHeaders(t, h, aos464Headers("human:bob"), "excede-o-global"); r.Code != http.StatusServiceUnavailable {
		t.Fatalf("com o global cheio esperava 503, veio %d — sem reparticao o global TEM de morder", r.Code)
	}
}

// TestAOS464UmSubmissorNaoOcupaAFilaDosOutros — O CRITÉRIO.
//
// Com o tecto global a 6 e a repartição a 2: alice enche a quota DELA e leva 429; bob, que não
// submeteu nada, continua a ser ADMITIDO. É exactamente a propriedade que o tecto global sozinho
// não dá, e a contraprova está em [TestAOS464SemReparticaoUmSubmissorNEGAAROTAAoOutro].
func TestAOS464UmSubmissorNaoOcupaAFilaDosOutros(t *testing.T) {
	_, h := aos464No(t, WithPlanMaxPending(6), WithPlanMaxPendingPerSubmitter(2))

	for i := 0; i < 2; i++ {
		rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-alice-"+strconv.Itoa(i))
		if rec.Code != http.StatusCreated {
			t.Fatalf("a %da submissao de alice, DENTRO da quota, devia dar 201, veio %d: %s",
				i+1, rec.Code, rec.Body.String())
		}
	}

	rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-alice-excede")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a 3a submissao de alice devia dar 429 (quota DELA, tecto 2), veio %d: %s",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "deste submissor") {
		t.Fatalf("o 429 devia nomear o tecto DO SUBMISSOR — sem isso o chamador nao distingue a sua "+
			"quota da fila cheia do no; veio %q", rec.Body.String())
	}

	// O QUE O TECTO GLOBAL SOZINHO NAO DA: bob passa, porque a fila tem lugar e ele esta dentro da
	// quota dele. Com a reparticao desligada, alice teria enchido os 6 e bob levaria 503.
	rec = postPlanoComHeaders(t, h, aos464Headers("human:bob"), "plano-bob-1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("bob devia ser ADMITIDO com a fila a 2 de 6 e a quota dele livre, veio %d: %s",
			rec.Code, rec.Body.String())
	}
}

// TestAOS464SemReparticaoUmSubmissorNEGAAROTAAoOutro — a CONTRAPROVA, e é o defeito medido.
//
// Sem a repartição (`planMaxPendingPerSubmitter <= 0`), alice enche o tecto global sozinha e bob
// leva **503** — «o nó não tem quem drene» — quando o problema é que uma pessoa ocupou a fila.
func TestAOS464SemReparticaoUmSubmissorNEGAAROTAAoOutro(t *testing.T) {
	const global = 4
	_, h := aos464No(t, WithPlanMaxPending(global), WithPlanMaxPendingPerSubmitter(0))

	for i := 0; i < global; i++ {
		rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-solo-"+strconv.Itoa(i))
		if rec.Code != http.StatusCreated {
			t.Fatalf("alice, SEM reparticao, devia encher os %d lugares; a %da deu %d",
				global, i+1, rec.Code)
		}
	}
	rec := postPlanoComHeaders(t, h, aos464Headers("human:bob"), "plano-bob-negado")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("bob devia levar 503 com a fila cheia por alice, veio %d — se isto mudou, o tecto "+
			"global deixou de ser o que era e este teste deixou de medir o defeito", rec.Code)
	}
}

// TestAOS464AOrdemDasDuasGuardasDiagnosticaACausa — a ordem é *load-bearing* para o DIAGNÓSTICO.
//
// Quando as DUAS condições são verdadeiras ao mesmo tempo — a fila está cheia E este submissor está
// acima da quota dele — o mais provável é que ele seja a CAUSA. Responder-lhe 503 («o nó não tem
// quem drene») manda-o procurar o consumidor quando o problema são os pedidos dele.
//
// A composição põe as duas a valer: global=2, por-submissor=1, alice com 1 e bob com 1 ⇒ a fila
// está cheia (2 de 2) e alice está na quota (1 de 1). Com a repartição ANTES: 429. Com a ordem
// trocada: 503, e o operador procura o consumidor em vez de olhar para alice.
func TestAOS464AOrdemDasDuasGuardasDiagnosticaACausa(t *testing.T) {
	_, h := aos464No(t, WithPlanMaxPending(2), WithPlanMaxPendingPerSubmitter(1))

	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-ordem-a"); rec.Code != http.StatusCreated {
		t.Fatalf("a 1a de alice devia dar 201, veio %d", rec.Code)
	}
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:bob"), "plano-ordem-b"); rec.Code != http.StatusCreated {
		t.Fatalf("a 1a de bob devia dar 201, veio %d", rec.Code)
	}
	// AS DUAS CONDIÇÕES VALEM AGORA: fila 2 de 2, alice 1 de 1.
	rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-ordem-a2")
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatalf("alice levou 503 com a fila cheia E a quota dela esgotada — o tecto GLOBAL esta a ser " +
			"verificado ANTES da reparticao, e o 503 diz-lhe que o no nao tem quem drene quando o que " +
			"ha e a quota dela. A correccao e verificar a reparticao primeiro: as duas contagens saem " +
			"da MESMA projeccao, logo a ordem nao poupa trabalho nenhum e so decide o diagnostico")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("esperava 429 do tecto do submissor, veio %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAOS464SemGateSoberanoAReparticaoNAOCompoe — a atribuição DEGENERADA, e porque não se compõe.
//
// Sem `readGov` o principal do pedido fica VAZIO para todos os chamadores: o corpo nunca o declara
// ([planRequest] tem `run_id` e `objective` e mais nada). Uma repartição chaveada no vazio não seria
// contornável — seria um tecto GLOBAL de 2 em vez de 6, a recusar com 429 a chamadores que não
// excederam nada, e anunciado como equidade.
//
// O que isto fixa: com a repartição CONFIGURADA e o gate ausente, as submissões passam até ao tecto
// GLOBAL, e a recusa que vem é a dele (503) e não a da quota (429).
func TestAOS464SemGateSoberanoAReparticaoNAOCompoe(t *testing.T) {
	const global = 5
	node, _ := newAPINode(t, &countingModel{}, false)
	t.Cleanup(func() { _ = node.Close() })
	// SEM WithReadSovereignty: h.readGov fica nil, e é essa a postura que se mede.
	_, h := newAPI(t, node, WithPlanMaxPending(global), WithPlanMaxPendingPerSubmitter(2),
		WithAPIClock(aos277Clock()))

	for i := 0; i < global; i++ {
		rec := postJSON(h, "POST", "/plans", map[string]any{
			"run_id": "plano-sem-gate-" + strconv.Itoa(i), "objective": "trabalho",
		})
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("a %da submissao levou 429 num no SEM gate soberano, com a reparticao a 2 — a "+
				"reparticao COMPOS-SE sobre um principal VAZIO, logo degenerou num tecto global de 2 "+
				"em vez de %d e recusa chamadores que nao excederam nada", i+1, global)
		}
		if rec.Code != http.StatusCreated {
			t.Fatalf("a %da submissao devia dar 201, veio %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}
	// E o tecto GLOBAL continua a valer: é ele a única barreira nesta postura.
	rec := postJSON(h, "POST", "/plans", map[string]any{
		"run_id": "plano-sem-gate-excede", "objective": "trabalho",
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a submissao %d devia levar 503 do tecto global, veio %d — sem gate a reparticao nao "+
			"compoe, mas o global TEM de continuar a morder", global+1, rec.Code)
	}
}

func aos464Headers(principal string) map[string]string {
	return map[string]string{HeaderReaderPrincipal: principal, HeaderReaderBoard: govBoard}
}

// TestAOS464ContagemDeSubmISSORVazioNaoContaOsSEMPrincipal — a metade INTERNA da protecção contra a
// atribuição degenerada, e existe porque os quatro testes acima não a apanham sozinhos.
//
// MEDIDO, e é um achado sobre os próprios testes deste ficheiro: a protecção vive em DOIS sítios — o
// `h.readGov != nil` no handler e o `if submissor != ""` em [pendentesNaFila] — e os testes de rota
// só detectam a CONJUNÇÃO quebrada. Remover só a metade do handler sobrevive aos quatro (5/5
// corridas), porque sem `readGov` o `p.Principal` já é vazio e o resultado não muda. Essa metade é
// **cinto-e-suspensórios**, e fica declarada como tal em vez de contada como coberta: vale se alguém
// vier a preencher o principal por outro caminho, e é aí que ela é a única barreira.
//
// Esta é a metade que se pode medir isoladamente: com a fila cheia de pedidos SEM principal, uma
// contagem por submissor vazio tem de devolver ZERO, e não «todos». Se devolvesse todos, um tecto
// por-submissor chaveado no vazio valeria como tecto global mais apertado.
func TestAOS464ContagemDeSubmISSORVazioNaoContaOsSEMPrincipal(t *testing.T) {
	const quantos = 4
	node, _ := newAPINode(t, &countingModel{}, false)
	t.Cleanup(func() { _ = node.Close() })
	// SEM gate soberano: cada pedido fica com o principal VAZIO, que é a postura que se mede.
	_, h := newAPI(t, node, WithPlanMaxPending(50), WithPlanMaxPendingPerSubmitter(0),
		WithAPIClock(aos277Clock()))
	for i := 0; i < quantos; i++ {
		if rec := postJSON(h, "POST", "/plans", map[string]any{
			"run_id": "plano-vazio-" + strconv.Itoa(i), "objective": "trabalho",
		}); rec.Code != http.StatusCreated {
			t.Fatalf("a %da submissao devia dar 201, veio %d", i+1, rec.Code)
		}
	}

	total, doSubmissor, _, err := pendentesNaFila(context.Background(), node.EventStore, nil, "", "")
	if err != nil {
		t.Fatalf("pendentesNaFila: %v", err)
	}
	if total != quantos {
		t.Fatalf("a fila devia ter %d pendentes, tem %d — o cenario deixou de medir o que diz medir",
			quantos, total)
	}
	if doSubmissor != 0 {
		t.Fatalf("a contagem para submissor VAZIO deu %d, esperava 0 — os pedidos sem principal estao "+
			"a ser contados como «de um submissor», logo um tecto por-submissor chaveado no vazio "+
			"valeria como tecto GLOBAL mais apertado (%d em vez do global), a recusar com 429 "+
			"chamadores que nao excederam nada e anunciado como equidade", doSubmissor, doSubmissor)
	}

	// E O CONTROLO, que é o que torna isto um sensor e não uma tautologia: com um principal REAL a
	// contagem tem de ser diferente de zero, senão a função podia estar simplesmente a devolver 0.
	regions := govsov.NewRegistry(map[string]string{govBoard: govRegion, govBoardUS: govRegionUS})
	node2, _ := newAPINode(t, &countingModel{}, false)
	t.Cleanup(func() { _ = node2.Close() })
	_, h2 := newAPI(t, node2, WithReadSovereignty(regions, node2.WORM),
		WithPlanMaxPending(50), WithPlanMaxPendingPerSubmitter(0), WithAPIClock(aos277Clock()))
	for i := 0; i < 3; i++ {
		if rec := postPlanoComHeaders(t, h2, aos464Headers("human:alice"), "plano-real-"+strconv.Itoa(i)); rec.Code != http.StatusCreated {
			t.Fatalf("a %da de alice devia dar 201, veio %d", i+1, rec.Code)
		}
	}
	if _, daAlice, _, err := pendentesNaFila(context.Background(), node2.EventStore, nil, "human:alice", ""); err != nil || daAlice != 3 {
		t.Fatalf("a contagem de alice deu %d (err=%v), esperava 3 — a funcao nao esta a contar por "+
			"submissor, e o caso do vazio acima passaria por ela devolver sempre 0", daAlice, err)
	}
}

// TestAOS464EnvFailClosedEOPARFINAL — o par valida-se FINAL, e é a correcção do AOS-463 aplicada ao
// NASCER deste eixo em vez de paga em revisão adversarial.
//
// A armadilha que isto fecha: se a comparação `por-submissor < global` vivesse DENTRO do ramo do
// por-submissor, baixar só `AOS_PLAN_MAX_PENDING` para `<= 125` (o default da repartição) deixava o
// par INERTE em silêncio — o global cortaria primeiro, a repartição nunca dispararia, e um submissor
// voltava a poder ocupar a fila toda. Os casos com `global` definido e `porSubmissor` AUSENTE são os
// que a tabela do AOS-459 não tinha, e foi essa lacuna exacta que o AOS-463 pagou.
func TestAOS464EnvFailClosedEOPARFINAL(t *testing.T) {
	casos := []struct {
		global, porSubmissor string
		aceita               bool
		porque               string
	}{
		{"", "", true, "ambos ausentes ⇒ defaults (1000 / 125)"},
		{"", "125", true, "o default da reparticao, abaixo do global por omissao"},
		{"", "999", true, "um abaixo do global por omissao: o maior valor que MORDE"},
		{"", "1000", false, "IGUAL ao global por omissao ⇒ o global corta primeiro, reparticao INERTE"},
		{"", "2000", false, "ACIMA do global por omissao ⇒ inerte"},
		{"400", "100", true, "par coerente, ambos explicitos"},
		{"400", "400", false, "iguais ⇒ inerte"},
		{"400", "401", false, "por-submissor acima do global ⇒ inerte"},
		// OS CASOS QUE A TABELA DO AOS-459 NAO TINHA, e que custaram o AOS-463:
		{"100", "", false, "SO o global baixado: a reparticao fica no default 125 >= 100 ⇒ INERTE"},
		{"125", "", false, "SO o global, IGUAL ao default da reparticao ⇒ INERTE"},
		{"126", "", true, "SO o global, um acima do default da reparticao: o menor que MORDE"},
		{"0", "", false, "global zero NAO desliga: abriria a fila a um laco em fuga"},
		{"-1", "", false, "global negativo"},
		{"abc", "", false, "global ilegivel"},
		// A JUSTIFICACAO ANTERIOR ERA FALSA, copiada do AOS-456a (onde o default e 0): «quem quer
		// desligar deixa-a POR DEFINIR». Deixa-la por definir da o DEFAULT 125, LIGADA. Consequencia
		// declarada: NAO HA COMO DESLIGAR a reparticao por ambiente, e o ramo «NAO CONFIGURADA» do
		// banner e alcancavel so por `WithPlanMaxPendingPerSubmitter(0)`, que e seam de teste. E a
		// mesma fronteira do eixo SSE (prior art), aqui escrita ao contrario. Achado BAIXO-3 de uma
		// revisao adversarial.
		{"", "0", false, "por-submissor zero: recusa-se em vez de desligar, e desligar por ambiente NAO e possivel"},
		{"", "-1", false, "por-submissor negativo"},
		{"", "2.5", false, "nao-inteiro: um tecto de pedidos e um inteiro"},
	}
	for _, c := range casos {
		nome := "global=" + c.global + "/porSubmissor=" + c.porSubmissor
		t.Run(nome, func(t *testing.T) {
			clearIngressEnv(t)
			t.Setenv("AOS_PLAN_MAX_PENDING", c.global)
			t.Setenv("AOS_PLAN_MAX_PENDING_PER_SUBMITTER", c.porSubmissor)
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
			if lim.planMaxPendingPerSubmitter >= lim.planMaxPending {
				t.Fatalf("par em vigor INERTE: porSubmissor=%d >= global=%d",
					lim.planMaxPendingPerSubmitter, lim.planMaxPending)
			}
		})
	}
}

// TestAOS464OBannerDeclaraAsQUATROPosturasDaFila — a dobra do eixo, e são QUATRO e não três.
//
// Os eixos AOS-456 e AOS-459 têm três posturas. Este tem quatro, porque a primeira divide-se: «não
// configurada» (a variável desligada) e «configurada mas não composta» (o gate ausente) exigem acções
// DIFERENTES do operador — definir a variável, ou compor o gate soberano. Colapsá-las numa mandaria
// metade dos operadores editar o ficheiro errado.
func TestAOS464OBannerDeclaraAsQUATROPosturasDaFila(t *testing.T) {
	base := ingressLimits{ratePerSec: 10, burst: 20, maxInFlight: 50,
		trajMaxConns: 100, trajMaxConnsPerReader: 4, planMaxPending: 800}
	comReparticao := func() ingressLimits { l := base; l.planMaxPendingPerSubmitter = 100; return l }
	casos := []struct {
		nome                    string
		lim                     ingressLimits
		gate, principalVerifica bool
		exige, proibe           []string
	}{
		{"nao configurada", base, true, true,
			[]string{"NAO CONFIGURADA", "AOS_PLAN_MAX_PENDING_PER_SUBMITTER", "800"},
			[]string{"LIGADA sobre", "negacao DIRIGIDA"}},
		{"configurada mas SEM gate: principal vazio", comReparticao(), false, false,
			[]string{"CONFIGURADA (100)", "NAO COMPOSTA", "VAZIO", "AOS_BOARD_REGIONS"},
			[]string{"LIGADA sobre", "degenerar num tecto global mais apertado.Defina"}},
		// A POSTURA DEMO-GRADE **NÃO COMPÕE**, e é a diferença face aos eixos AOS-456a e AOS-459, que
		// compõem sobre um principal forjável. A razão está medida em
		// [TestAOS464DEMOGRADENaoCompoePorqueSeriaNegacaoDirigida]: aqui um atacante escreveria o header
		// da vítima e gastaria a quota dela, com ocupação durável. A dobra tem de dizer que NÃO se
		// compõe e porquê, e NÃO pode dizer «contorna-se» — que leria como «não é pior do que nada».
		{"gate composto SEM credencial forte: NAO compoe", comReparticao(), true, false,
			[]string{"CONFIGURADA (100)", "NAO COMPOSTA", "X-Aos-Reader", "negacao DIRIGIDA",
				"DURAVEL", "AOS_SOVEREIGN_OIDC_ISSUER"},
			[]string{"LIGADA sobre", "VERIFICADO", "CONTORNA-SE"}},
		{"LIGADA sobre principal VERIFICADO", comReparticao(), true, true,
			[]string{"VERIFICADO", "credencial FORTE", "429", "ANTES do tecto global"},
			[]string{"DEMO-GRADE", "CONTORNA-SE"}},
		// O CASO GEMEO dos outros eixos: verificavel SEM gate composto. Inalcancavel pelo Bootstrap,
		// e esta aqui porque a funcao nao pode depender dessa coincidencia para estar certa — foi o
		// MEDIO-1 da setima revisao, pago no eixo SSE.
		{"verificavel SEM gate: nada esta composto", comReparticao(), false, true,
			[]string{"CONFIGURADA (100)", "NAO COMPOSTA", "VAZIO"},
			[]string{"VERIFICADO", "credencial FORTE", "DEMO-GRADE"}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			txt := dobraDoEixo(t, strings.Join(ingressPostureBanner(c.lim, c.gate, c.principalVerifica), "\n"),
				marcadorDobraFila)
			for _, ex := range c.exige {
				if !strings.Contains(txt, ex) {
					t.Errorf("a dobra da FILA NAO declara %q\n--- dobra ---\n%s", ex, txt)
				}
			}
			for _, pr := range c.proibe {
				if strings.Contains(txt, pr) {
					t.Errorf("a dobra da FILA declara %q, FALSO nesta postura\n--- dobra ---\n%s", pr, txt)
				}
			}
		})
	}
}

// TestAOS464ReSubmissaoDoQueJaEstaNaFilaNaoGastaQuota — o falso negativo que a primeira versão tinha.
//
// Um pedido repetido para um `run_id` já pendente não acrescenta nada à fila, e o banner promete «201
// accepted IDEMPOTENTE». Recusá-lo por quota era um falso negativo puro — e acontecia exactamente
// quando um cliente faz retry de rede, a 125 por chamador em vez de a 1000 globais.
func TestAOS464ReSubmissaoDoQueJaEstaNaFilaNaoGastaQuota(t *testing.T) {
	const quota = 2
	node, h := aos464No(t, WithPlanMaxPending(20), WithPlanMaxPendingPerSubmitter(quota))

	for i := 0; i < quota; i++ {
		if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-retry-"+strconv.Itoa(i)); rec.Code != http.StatusCreated {
			t.Fatalf("a %da devia dar 201, veio %d", i+1, rec.Code)
		}
	}
	// A QUOTA ESTÁ CHEIA: um run NOVO tem de levar 429.
	if rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-retry-novo"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("um run NOVO com a quota cheia devia dar 429, veio %d — o cenario deixou de medir", rec.Code)
	}
	// O RETRY de um run JÁ PENDENTE tem de dar 201 idempotente.
	rec := postPlanoComHeaders(t, h, aos464Headers("human:alice"), "plano-retry-0")
	if rec.Code != http.StatusCreated {
		t.Fatalf("a re-submissao de um run JA PENDENTE devia dar 201 idempotente, veio %d (%s) — e um "+
			"falso negativo: nao acrescenta nada a fila, e acontece quando um cliente faz retry de rede",
			rec.Code, rec.Body.String())
	}
	// E a fila NÃO cresceu com o retry.
	total, daAlice, _, err := pendentesNaFila(context.Background(), node.EventStore, nil, "human:alice", "")
	if err != nil {
		t.Fatalf("pendentesNaFila: %v", err)
	}
	if total != quota || daAlice != quota {
		t.Fatalf("a fila tem %d (alice %d), esperava %d — o retry acrescentou um pedido, logo a isencao "+
			"da quota estaria a admitir trabalho novo", total, daAlice, quota)
	}
}
