package main

// AOS-461 — OS RESIDUAIS QUE A SÉTIMA REVISÃO ADVERSARIAL MEDIU, e um sensor que faltava desde o
// AOS-459.
//
// O AOS-460 corrigiu a ordem da admissão do stream SSE e declarou-o com uma afirmação que a medição
// não sustentava («0 de 200»), com um sensor que filtrava em silêncio a categoria de 429 para onde o
// dano migra, e com uma dobra nova no banner que desarmou quatro asserções do sensor de arranque real
// do AOS-456a (detecção 5/5 → 1/5, reproduzida nas duas árvores).
//
// Este ficheiro traz o que faltava em testes; as correcções de texto e de guarda vivem nos ficheiros
// que as afirmavam.

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
)

// TestAOS461RecusaGLOBALDEVOLVEOLugarGlobal — o invariante que ficou sem sensor, e a lacuna é anterior
// ao AOS-460.
//
// Depois da reordenação do AOS-460, uma recusa por-leitor não toca no contador global (nada a
// devolver). O sentido que SOBRA é o que ninguém vigiava: uma recusa do tecto GLOBAL **incrementa** o
// contador antes de decidir e tem de o devolver no ramo de recusa — o `defer` só é registado DEPOIS
// desse `return`, pelo que o rollback é explícito e pode ser removido sem quebrar compilação.
//
// MEDIDO (achado BAIXO-1 da sétima revisão, reproduzido aqui): remover `h.trajConns.Add(-1)` do ramo
// de recusa global deixa a suite INTEIRA do pacote verde, e a fuga é permanente — cada recusa retém um
// lugar e o tecto do nó esgota-se para sempre, sem uma única ligação viva.
//
// E O SIMÉTRICO, no mesmo cenário (MÉDIO-3 da sétima revisão, aqui fixado): um pedido destinado a
// recusa GLOBAL toma primeiro um lugar POR-LEITOR, porque a reserva corre antes. O `defer libertar()`
// é registado ANTES do bloco global e cobre esse `return` — mas isso não tinha sensor. Se vazasse, N
// recusas globais esgotariam a quota do leitor, e o leitor ficaria fechado fora da rota mesmo depois
// de o nó esvaziar. Mede-se pelo COMPORTAMENTO (o leitor volta a ser admitido quando um lugar global
// liberta), não por uma leitura da struct.
func TestAOS461RecusaGLOBALDEVOLVEOLugarGlobal(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	t.Cleanup(func() { _ = node.Close() })
	regions := govsov.NewRegistry(map[string]string{govBoard: govRegion, govBoardUS: govRegionUS})
	svc, h := newAPI(t, node, WithReadSovereignty(regions, node.WORM),
		// global=3 e por-leitor=2: o global enche-se com leitores DENTRO da sua quota (bob 2 + carol 1),
		// e sobra medir o que uma recusa GLOBAL faz aos dois contadores.
		WithMaxTrajectoryConns(3), WithMaxTrajectoryConnsPerReader(2),
		// balde de taxa fora do caminho: o 429 medido tem de vir do tecto de OCUPAÇÃO
		WithReadRateLimit(1e9, 1e9), WithAPIClock(aos277Clock()))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	const runID = "run-461-rollback-global"
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
				"nao mede nada:\n%s", rec.Body.String())
		}
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			t.Fatalf("valor ilegivel %q: %v", m[1], err)
		}
		return v
	}

	abrirComoCtx := func(ctx context.Context, principal string) *http.Response {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/runs/"+runID+"/trajectory", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set(HeaderReaderPrincipal, principal)
		req.Header.Set(HeaderReaderBoard, govBoard)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET trajectory: %v", err)
		}
		return resp
	}

	if n := activos(); n != 0 {
		t.Fatalf("antes de tudo devia haver 0 streams activos, ha %v", n)
	}

	// O GLOBAL CHEIO com leitores DENTRO da sua quota: bob 2 (o seu tecto) + carol 1 = 3 = global.
	abrirVivo := func(principal string) (*http.Response, context.CancelFunc) {
		ctx, cancelar := context.WithCancel(context.Background())
		resp := abrirComoCtx(ctx, principal)
		if resp.StatusCode != http.StatusOK {
			cancelar()
			t.Fatalf("o stream vivo de %s devia dar 200, veio %d", principal, resp.StatusCode)
		}
		if _, err := readSSE(bufio.NewReader(resp.Body)); err != nil {
			cancelar()
			t.Fatalf("o stream de %s nao entregou backfill, logo pode nao estar vivo: %v", principal, err)
		}
		return resp, cancelar
	}
	rb1, cb1 := abrirVivo("human:bob")
	defer func() { _ = rb1.Body.Close(); cb1() }()
	rb2, cb2 := abrirVivo("human:bob")
	defer func() { _ = rb2.Body.Close(); cb2() }()
	rc1, cc1 := abrirVivo("human:carol")
	defer func() { _ = rc1.Body.Close(); cc1() }()
	if n := activos(); n != 3 {
		t.Fatalf("com tres streams vivos devia haver 3 activos, ha %v", n)
	}

	// DEZ recusas do tecto GLOBAL, de um leitor cuja quota está LIVRE.
	const recusas = 10
	for i := 0; i < recusas; i++ {
		ctx, cancelar := context.WithCancel(context.Background())
		resp := abrirComoCtx(ctx, "human:alice")
		corpo := make([]byte, 256)
		n, _ := resp.Body.Read(corpo)
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("recusa %d: esperava 429, veio %d", i+1, resp.StatusCode)
		}
		if strings.Contains(string(corpo[:n]), "deste leitor") {
			t.Fatalf("recusa %d veio do tecto DO LEITOR e nao do GLOBAL — o cenario deixou de medir o "+
				"que diz medir (a quota de alice devia estar livre): %q", i+1, corpo[:n])
		}
		_ = resp.Body.Close()
		cancelar()
	}

	// (1) O LUGAR GLOBAL VOLTA. Sem o rollback, dez recusas levariam a métrica a 13 e o tecto do nó
	// esgotar-se-ia por recusas, sem uma única ligação nova viva.
	if n := activos(); n != 3 {
		t.Fatalf("depois de %d RECUSAS do tecto GLOBAL ha %v streams activos, esperava 3 — cada recusa "+
			"RETEVE o lugar que o `trajConns.Add(1)` tomou antes de decidir. A fuga e PERMANENTE: o "+
			"tecto do no esgota-se por recusas, e o operador ve a metrica a subir sem ninguem a ligar. "+
			"Falta o `h.trajConns.Add(-1)` no ramo de recusa de handleTrajectory", recusas, n)
	}

	// (2) O LUGAR POR-LEITOR TAMBÉM VOLTA. Liberta-se UM lugar global, e alice — que acabou de levar
	// dez recusas — tem de ser ADMITIDA. Se o `defer libertar()` não cobrisse o ramo de recusa global,
	// a quota dela (2) estaria gasta e ela levaria 429 do tecto DELA com o nó a ter lugar.
	cc1()
	_ = rc1.Body.Close()
	esperarActivos(t, activos, 2)

	ctxA, pararA := context.WithCancel(context.Background())
	defer pararA()
	respA := abrirComoCtx(ctxA, "human:alice")
	defer func() { _ = respA.Body.Close() }()
	if respA.StatusCode != http.StatusOK {
		corpo := make([]byte, 256)
		n, _ := respA.Body.Read(corpo)
		t.Fatalf("alice devia ser ADMITIDA depois de um lugar global libertar, veio %d (%q) — as %d "+
			"recusas GLOBAIS gastaram a quota POR-LEITOR dela e nunca a devolveram. E o defeito "+
			"simetrico do que o AOS-460 fechou: o `defer libertar()` tem de cobrir o ramo de recusa "+
			"global", respA.StatusCode, corpo[:n], recusas)
	}
}

// esperarActivos espera que a métrica desça ao valor esperado: fechar o corpo do lado do cliente não
// garante que o handler do servidor já retornou, e sem isto a asserção seguinte corre uma corrida.
func esperarActivos(t *testing.T, activos func() float64, querido float64) {
	t.Helper()
	prazo := time.Now().Add(5 * time.Second)
	for time.Now().Before(prazo) {
		if activos() == querido {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("a metrica nao desceu a %v em 5s, esta em %v", querido, activos())
}

// TestAOS461ODEMOGRADEContornaSeRodandoOHeader — a afirmação que viaja no banner de PRODUÇÃO e não
// tinha sensor nenhum.
//
// O banner diz, na postura DEMO-GRADE do eixo SSE: «este tecto CONTORNA-SE rodando o header (medido:
// 12 streams vivos com o tecto a 1)». O número estava no commit, no ticket, no comentário do código
// **e no banner que um operador lê** — e em teste nenhum (achado BAIXO-5 da sétima revisão). O eixo
// AOS-456 tem a afirmação gémea («60 submissões rotativas») fixada no arranque real; este não tinha
// nada.
//
// O QUE ISTO FIXA, e não é o mecanismo: é a FRONTEIRA declarada. Nesta postura o principal vem do
// header `X-Aos-Reader`, que o chamador escreve, pelo que N principais distintos dão N lugares. Vale
// contra rajada honesta; não vale contra abuso. Se alguém tornar o tecto inforjável nesta postura,
// este teste avermelha — e é aí que se corrige o banner, não antes.
func TestAOS461ODEMOGRADEContornaSeRodandoOHeader(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	t.Cleanup(func() { _ = node.Close() })
	regions := govsov.NewRegistry(map[string]string{govBoard: govRegion, govBoardUS: govRegionUS})
	// POSTURA DEMO-GRADE: gate soberano composto (registo board→região) e NENHUMA credencial forte —
	// é o que `WithReadSovereignty` compõe, e é a postura que um nó com AOS_BOARD_REGIONS e sem OIDC
	// tem. O banner declara-a como DEMO-GRADE precisamente por isto.
	svc, h := newAPI(t, node, WithReadSovereignty(regions, node.WORM),
		WithMaxTrajectoryConns(100), WithMaxTrajectoryConnsPerReader(1),
		WithReadRateLimit(1e9, 1e9), WithAPIClock(aos277Clock()))
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	const runID = "run-461-demo-grade"
	submitHTTPAndWait(t, svc, h, runID, euReaderHeaders())
	appendTraj(t, node.EventStore, runID, "s1")

	// O NÓ NÃO ESTÁ EM POSTURA VERIFICADA — sem isto o teste poderia estar a medir a postura errada e
	// a afirmação que fixa não seria a que o banner faz.
	if principalDoRunEVerificavel(node) {
		t.Fatal("o no esta em postura VERIFICADA: este teste mede a fronteira da postura DEMO-GRADE")
	}

	const distintos = 12
	var vivos []*http.Response
	var cancelar []context.CancelFunc
	t.Cleanup(func() {
		for i := range vivos {
			_ = vivos[i].Body.Close()
			cancelar[i]()
		}
	})
	for i := 0; i < distintos; i++ {
		ctx, c := context.WithCancel(context.Background())
		resp := abrirTrajComo(t, ts.URL, runID, fmt.Sprintf("human:sybil-%02d", i), ctx)
		if resp.StatusCode != http.StatusOK {
			c()
			t.Fatalf("o %do principal rotativo levou %d — se o tecto passou a ser inforjavel nesta "+
				"postura, o banner DEMO-GRADE ja nao pode dizer «CONTORNA-SE rodando o header» nem citar "+
				"12 streams: corrija-o", i+1, resp.StatusCode)
		}
		if _, err := readSSE(bufio.NewReader(resp.Body)); err != nil {
			c()
			t.Fatalf("o stream %d nao entregou backfill, logo pode nao estar vivo: %v", i+1, err)
		}
		vivos = append(vivos, resp)
		cancelar = append(cancelar, c)
	}
	t.Logf("%d streams VIVOS com o tecto por-leitor a 1, so a rodar X-Aos-Reader — e a fronteira que o "+
		"banner DEMO-GRADE declara", distintos)

	// E o CONTROLO, que é o que torna isto um sensor e não uma tautologia: o MESMO principal é
	// recusado ao segundo. Sem ele, um tecto simplesmente DESLIGADO passaria este teste.
	ctx, c := context.WithCancel(context.Background())
	defer c()
	resp := abrirTrajComo(t, ts.URL, runID, "human:sybil-00", ctx)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("o MESMO principal devia levar 429 ao segundo stream, veio %d — o tecto por-leitor esta "+
			"DESLIGADO, e entao os 12 acima nao medem contorno nenhum", resp.StatusCode)
	}
}

// abrirTrajComo abre a rota de trajectória com um principal de leitor arbitrário.
func abrirTrajComo(t *testing.T, base, runID, principal string, ctx context.Context) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/runs/"+runID+"/trajectory", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set(HeaderReaderPrincipal, principal)
	req.Header.Set(HeaderReaderBoard, govBoard)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET trajectory: %v", err)
	}
	return resp
}
