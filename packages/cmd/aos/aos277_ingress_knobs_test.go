package main

// AOS-277 — OS KNOBS DE INGRESSO, PROVADOS PELA VIA DO OPERADOR.
//
// A admission já existia (AOS-166): estes testes NÃO provam que há backpressure — provam que
// as TRÊS variáveis de ambiente chegam ao handler REAL e que o 429 responde aos números que o
// operador escreveu, não aos defaults do binário. Por isso NENHUM deles chama [WithRateLimit]
// ou [WithMaxInFlight] à mão: as opções vêm SEMPRE de [ingressLimitsFromEnv], que é o troço
// novo. Um teste que passasse as opções directamente ficaria verde mesmo que a leitura do
// ambiente estivesse partida — que é precisamente o defeito que este ticket fecha.

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// aos277Clock é o relógio FIXO da admission: sem passagem de tempo o token-bucket NUNCA
// reabastece, pelo que o burst configurado é exactamente o número de pedidos admitidos.
func aos277Clock() func() time.Time {
	return func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
}

// aos277BlockingModel bloqueia CADA turno até `release` fechar (ou o ctx cair), mantendo o
// run REGISTADO no loop de serviço — que é o que faz a contagem de in-flight subir. Ao
// contrário do `blockingModel` de service_test.go, sinaliza TODAS as entradas (canal com
// buffer), não só a primeira: o teste precisa de saber que o run chegou mesmo a correr.
type aos277BlockingModel struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *aos277BlockingModel) Call(ctx context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	select {
	case m.entered <- struct{}{}:
	default:
	}
	select {
	case <-m.release:
		return agentruntime.ModelResponse{
			Text:  "libertado",
			Final: true,
			Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
		}, nil
	case <-ctx.Done():
		return agentruntime.ModelResponse{}, ctx.Err()
	}
}

func (m *aos277BlockingModel) releaseAll() { m.once.Do(func() { close(m.release) }) }

// clearIngressEnv isola o teste do ambiente do processo: as três variáveis ficam VAZIAS
// (⇒ defaults) salvo quando o próprio caso as define a seguir.
func clearIngressEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AOS_INGRESS_RATE", "")
	t.Setenv("AOS_INGRESS_BURST", "")
	t.Setenv("AOS_INGRESS_MAX_INFLIGHT", "")
}

// ---------------------------------------------------------------------------
// (a) leitura fail-closed — valor inválido ABORTA, ausência mantém os defaults
// ---------------------------------------------------------------------------

// TestAOS277IngressEnvIsFailClosed prova o critério (a): as três variáveis são lidas com
// validação fail-closed e um valor inválido devolve [ErrBadIngressLimits] — nunca um
// fallback silencioso para o default (que deixaria o operador convencido de que o nó admite
// o que ele escreveu) nem um valor degenerado que fecha o ingresso.
func TestAOS277IngressEnvIsFailClosed(t *testing.T) {
	t.Run("ausentes mantem os defaults do binario", func(t *testing.T) {
		clearIngressEnv(t)
		lim, opts, err := ingressLimitsFromEnv()
		if err != nil {
			t.Fatalf("sem variaveis definidas nao devia haver erro: %v", err)
		}
		if lim.ratePerSec != DefaultRatePerSec || lim.burst != DefaultRateBurst || lim.maxInFlight != DefaultMaxInFlight {
			t.Fatalf("defaults nao preservados: %+v", lim)
		}
		if lim.tuned {
			t.Fatal("sem variaveis definidas o banner nao pode anunciar limites AFINADOS")
		}
		if len(opts) != 2 {
			t.Fatalf("esperava 2 opcoes de API (rate-limit + max-in-flight), vieram %d", len(opts))
		}
	})

	t.Run("valores validos sao aplicados", func(t *testing.T) {
		clearIngressEnv(t)
		t.Setenv("AOS_INGRESS_RATE", "2.5")
		t.Setenv("AOS_INGRESS_BURST", "7")
		t.Setenv("AOS_INGRESS_MAX_INFLIGHT", "3")
		lim, _, err := ingressLimitsFromEnv()
		if err != nil {
			t.Fatalf("valores validos nao deviam falhar: %v", err)
		}
		if lim.ratePerSec != 2.5 || lim.burst != 7 || lim.maxInFlight != 3 {
			t.Fatalf("valores lidos errados: %+v", lim)
		}
		if !lim.tuned {
			t.Fatal("com variaveis definidas o banner tem de anunciar limites AFINADOS")
		}
	})

	// Cada caso é um modo de falha REAL do ingresso, não um zoo de parsing:
	//   - rate 0 / negativo ⇒ balde que nunca reabastece ⇒ 429 para sempre;
	//   - burst < 1 ⇒ o balde nunca acumula o token que a admissão consome ⇒ NADA admitido;
	//   - max-in-flight 0 ⇒ na implementação DESLIGA o tecto (a guarda é `> 0`) — o valor
	//     que parece "nada passa" faz o oposto;
	//   - NaN/Inf ⇒ `strconv.ParseFloat` aceita-os e um `+Inf` passaria um `> 0` ingénuo.
	invalidos := []struct{ env, valor string }{
		{"AOS_INGRESS_RATE", "0"},
		{"AOS_INGRESS_RATE", "-1"},
		{"AOS_INGRESS_RATE", "abc"},
		{"AOS_INGRESS_RATE", "NaN"},
		{"AOS_INGRESS_RATE", "+Inf"},
		{"AOS_INGRESS_BURST", "0"},
		{"AOS_INGRESS_BURST", "0.5"},
		{"AOS_INGRESS_BURST", "-4"},
		{"AOS_INGRESS_BURST", "Inf"},
		{"AOS_INGRESS_MAX_INFLIGHT", "0"},
		{"AOS_INGRESS_MAX_INFLIGHT", "-2"},
		{"AOS_INGRESS_MAX_INFLIGHT", "2.5"},
		{"AOS_INGRESS_MAX_INFLIGHT", "muitos"},
	}
	for _, tc := range invalidos {
		t.Run(tc.env+"="+tc.valor, func(t *testing.T) {
			clearIngressEnv(t)
			t.Setenv(tc.env, tc.valor)
			_, _, err := ingressLimitsFromEnv()
			if !errors.Is(err, ErrBadIngressLimits) {
				t.Fatalf("%s=%q devia ABORTAR com ErrBadIngressLimits, veio %v", tc.env, tc.valor, err)
			}
			// A mensagem tem de NOMEAR a variável — um erro de config que não diz qual
			// variável está errada obriga o operador a adivinhar.
			if !strings.Contains(err.Error(), tc.env) {
				t.Fatalf("o erro devia nomear %s: %v", tc.env, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// (b1) burst excedido ⇒ 429; dentro dos limites ⇒ 201
// ---------------------------------------------------------------------------

// TestAOS277BurstExceededYields429 prova a primeira metade do critério (b) pela API REAL:
// com AOS_INGRESS_BURST=2 (e o relógio parado, para o reabastecimento não interferir), os
// dois primeiros `POST /runs` são ADMITIDOS (201) e o terceiro leva 429. Não-vacuoso nas duas
// pontas: se a variável fosse ignorada, o default de 128 admitiria os três.
func TestAOS277BurstExceededYields429(t *testing.T) {
	clearIngressEnv(t)
	t.Setenv("AOS_INGRESS_RATE", "1000") // irrelevante com o relógio parado; prova que rate != burst
	t.Setenv("AOS_INGRESS_BURST", "2")

	_, ingressOpts, err := ingressLimitsFromEnv()
	if err != nil {
		t.Fatalf("ingressLimitsFromEnv: %v", err)
	}

	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node, append(ingressOpts, WithAPIClock(aos277Clock()))...)

	codes := make([]int, 0, 3)
	for _, id := range []string{"run-277-a", "run-277-b", "run-277-c"} {
		rec := postJSON(h, "POST", "/runs", map[string]any{
			"run_id":        id,
			"objective":     "trabalho de referencia",
			"principal_nhi": "nhi:" + id,
		})
		codes = append(codes, rec.Code)
	}
	if codes[0] != http.StatusCreated || codes[1] != http.StatusCreated {
		t.Fatalf("os dois primeiros submits (dentro do burst) deviam ser ADMITIDOS com 201, vieram %v", codes)
	}
	if codes[2] != http.StatusTooManyRequests {
		t.Fatalf("o terceiro submit (burst=2 excedido) devia dar 429, veio %d", codes[2])
	}
}

// ---------------------------------------------------------------------------
// (b2) in-flight no tecto ⇒ 429; abaixo do tecto ⇒ 201
// ---------------------------------------------------------------------------

// TestAOS277MaxInFlightYields429 prova a segunda metade do critério (b): com
// AOS_INGRESS_MAX_INFLIGHT=1 e um run REALMENTE a correr (modelo bloqueado), a segunda
// submissão leva 429 — e leva-o pelo TECTO, não pelo balde (o burst fica largo e o relógio
// parado, logo o balde tem tokens de sobra). Prova de não-vacuosidade no fim: libertado o
// primeiro run, a contagem desce e uma submissão nova volta a ser admitida (201).
func TestAOS277MaxInFlightYields429(t *testing.T) {
	clearIngressEnv(t)
	t.Setenv("AOS_INGRESS_BURST", "64") // largo: o 429 que se mede NÃO pode vir do balde
	t.Setenv("AOS_INGRESS_MAX_INFLIGHT", "1")

	_, ingressOpts, err := ingressLimitsFromEnv()
	if err != nil {
		t.Fatalf("ingressLimitsFromEnv: %v", err)
	}

	model := &aos277BlockingModel{entered: make(chan struct{}, 4), release: make(chan struct{})}
	t.Cleanup(model.releaseAll)
	node, _ := newAPINode(t, model, false)
	defer func() { _ = node.Close() }()
	svc, h := newAPI(t, node, append(ingressOpts, WithAPIClock(aos277Clock()))...)

	// 1º submit: dentro do tecto ⇒ ADMITIDO.
	if rec := postJSON(h, "POST", "/runs", map[string]any{
		"run_id":        "run-277-inflight-1",
		"objective":     "trabalho bloqueado",
		"principal_nhi": "nhi:run-277-inflight-1",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("1o submit devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
	// O run está mesmo A CORRER (não só hospedado): o modelo entrou no turno e ficou preso.
	select {
	case <-model.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("o run admitido nunca chegou ao modelo — a contagem de in-flight nao seria significativa")
	}
	if n := svc.InProgressCount(); n != 1 {
		t.Fatalf("com um run bloqueado a contagem de in-flight devia ser 1, veio %d", n)
	}

	// 2º submit: a contagem está NO tecto ⇒ 429.
	if rec := postJSON(h, "POST", "/runs", map[string]any{
		"run_id":        "run-277-inflight-2",
		"objective":     "trabalho recusado",
		"principal_nhi": "nhi:run-277-inflight-2",
	}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("2o submit (in-flight no tecto de 1) devia dar 429, veio %d (%s)", rec.Code, rec.Body.String())
	}

	// NÃO-VACUOSIDADE: libertado o run, a contagem desce e o ingresso volta a admitir.
	model.releaseAll()
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, ok, werr := svc.Wait(waitCtx, "run-277-inflight-1"); werr != nil || !ok {
		t.Fatalf("o 1o run devia ter terminado: ok=%v err=%v", ok, werr)
	}
	if rec := postJSON(h, "POST", "/runs", map[string]any{
		"run_id":        "run-277-inflight-3",
		"objective":     "trabalho readmitido",
		"principal_nhi": "nhi:run-277-inflight-3",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("com o tecto livre o submit devia voltar a dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// (c) o banner declara os limites EM VIGOR
// ---------------------------------------------------------------------------

// TestAOS277BannerDeclaresLimits prova o critério (c) e a disciplina de AOS-248: a linha
// traz os TRÊS números realmente aplicados, diz de onde vieram, e NÃO promete mais do que a
// admission faz — declara explicitamente que o plano de controlo e as leituras ficam de fora
// e que o limite é por-réplica.
func TestAOS277BannerDeclaresLimits(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		lines := ingressPostureBanner(ingressLimits{ratePerSec: DefaultRatePerSec, burst: DefaultRateBurst, maxInFlight: DefaultMaxInFlight})
		if len(lines) != 1 {
			t.Fatalf("esperava 1 linha de banner, vieram %d", len(lines))
		}
		for _, marker := range []string{"DEFAULTS do binario", "64", "128", "512", "429"} {
			if !strings.Contains(lines[0], marker) {
				t.Fatalf("o banner de defaults devia conter %q: %s", marker, lines[0])
			}
		}
	})

	t.Run("afinados", func(t *testing.T) {
		lines := ingressPostureBanner(ingressLimits{ratePerSec: 12, burst: 34, maxInFlight: 56, tuned: true})
		if len(lines) != 1 {
			t.Fatalf("esperava 1 linha de banner, vieram %d", len(lines))
		}
		// Os NÚMEROS EM VIGOR (critério c).
		for _, marker := range []string{"AFINADO", "12", "34", "56"} {
			if !strings.Contains(lines[0], marker) {
				t.Fatalf("o banner afinado devia conter %q: %s", marker, lines[0])
			}
		}
		// O ALCANCE — o que a linha NÃO pode deixar o operador supor (AOS-248).
		for _, marker := range []string{"POST /runs e SO", "/steer", "POR-PROCESSO", "SUSPENSO", "Retry-After"} {
			if !strings.Contains(lines[0], marker) {
				t.Fatalf("o banner devia declarar o alcance (%q): %s", marker, lines[0])
			}
		}
		if strings.Contains(lines[0], "DEFAULTS do binario") {
			t.Fatalf("banner afinado nao pode anunciar defaults: %s", lines[0])
		}
	})
}

// ---------------------------------------------------------------------------
// (a)+(c) no CAMINHO DE ARRANQUE REAL — serveAPI
// ---------------------------------------------------------------------------

// TestAOS277ServeAPIDeclaresAndEnforcesLimits fecha o laço no wiring: é [serveAPI] — a
// função que o entrypoint invoca quando AOS_API_ADDR está definido — que lê as variáveis e
// emite a linha. Duas provas num só arranque:
//
//   - FAIL-CLOSED: com uma variável inválida, serveAPI devolve [ErrBadIngressLimits] e NÃO
//     chega a servir (o nó não arranca com um limite que o operador escreveu mal);
//   - BANNER: com valores válidos, a linha declara-os no arranque loopback.
func TestAOS277ServeAPIDeclaresAndEnforcesLimits(t *testing.T) {
	t.Run("valor invalido ABORTA o arranque", func(t *testing.T) {
		clearIngressEnv(t)
		t.Setenv("AOS_INGRESS_MAX_INFLIGHT", "0")
		node, _ := newAPINode(t, &countingModel{}, true)
		defer func() { _ = node.Close() }()

		err := serveAPI(context.Background(), io.Discard, node, "127.0.0.1:0")
		if !errors.Is(err, ErrBadIngressLimits) {
			t.Fatalf("serveAPI com AOS_INGRESS_MAX_INFLIGHT invalida devia devolver ErrBadIngressLimits, veio %v", err)
		}
	})

	t.Run("banner declara os limites em vigor", func(t *testing.T) {
		clearIngressEnv(t)
		t.Setenv("AOS_INGRESS_RATE", "9")
		t.Setenv("AOS_INGRESS_BURST", "11")
		t.Setenv("AOS_INGRESS_MAX_INFLIGHT", "13")
		node, _ := newAPINode(t, &countingModel{}, true)
		defer func() { _ = node.Close() }()

		var out syncBuf
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- serveAPI(ctx, &out, node, "127.0.0.1:0") }()
		time.Sleep(100 * time.Millisecond)
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("serveAPI loopback devia encerrar graciosamente, veio %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("serveAPI nao encerrou apos cancelamento do ctx")
		}

		banner := out.String()
		for _, marker := range []string{"ingresso / admission (AOS-166/AOS-277)", "AFINADO", "9 pedido(s)/segundo", "burst de 11", "13 run(s) EM CURSO"} {
			if !strings.Contains(banner, marker) {
				t.Fatalf("o banner de arranque devia conter %q; saiu:\n%s", marker, banner)
			}
		}
	})
}

// portaLivreLoopback reserva e devolve um endereço de loopback livre. `serveAPI` recebe o
// endereço (não um listener), pelo que não há forma de lhe perguntar a porta depois — e
// "127.0.0.1:0" deixaria o teste sem saber onde bater.
func portaLivreLoopback(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reservar porta livre: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("libertar porta reservada: %v", err)
	}
	return addr
}

// esperarPorta espera que serveAPI comece mesmo a aceitar ligações.
func esperarPorta(t *testing.T, addr string) {
	t.Helper()
	limite := time.Now().Add(10 * time.Second)
	for time.Now().Before(limite) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("serveAPI nao comecou a servir em %s", addr)
}

// submeterRun faz um `POST /runs` REAL (pela rede, não por httptest) e devolve o status.
func submeterRun(t *testing.T, base, runID string) int {
	t.Helper()
	corpo := `{"run_id":"` + runID + `","objective":"trabalho de referencia","principal_nhi":"nhi:` + runID + `"}`
	req, err := http.NewRequest(http.MethodPost, base+"/runs", strings.NewReader(corpo))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST /runs (%s): %v", runID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// TestAOS277ServeAPIEnforcaOsLimitesLidos fecha o VÁCUO que a auditoria de completude provou
// por mutação: apagar `apiOpts = append(apiOpts, ingressOpts...)` de [serveAPI] deixava a
// suite INTEIRA verde, porque os 429 eram todos medidos num servidor montado À MÃO no teste.
// Nada provava que o servidor que o entrypoint constrói aplica os números que leu.
//
// Este teste bate no servidor REAL de [serveAPI], pela rede, e exige o 429. Escolhe o TECTO
// DE IN-FLIGHT (e não o burst) porque é determinístico sem relógio injectado: [serveAPI] não
// aceita [WithAPIClock], pelo que um teste de balde dependeria do reabastecimento em tempo
// real.
func TestAOS277ServeAPIEnforcaOsLimitesLidos(t *testing.T) {
	clearIngressEnv(t)
	t.Setenv("AOS_INGRESS_RATE", "1000") // largo: o 429 que se mede NÃO pode vir do balde
	t.Setenv("AOS_INGRESS_BURST", "64")
	t.Setenv("AOS_INGRESS_MAX_INFLIGHT", "1")

	model := &aos277BlockingModel{entered: make(chan struct{}, 4), release: make(chan struct{})}
	node, _ := newAPINode(t, model, false)
	defer func() { _ = node.Close() }()

	addr := portaLivreLoopback(t)
	ctx, cancel := context.WithCancel(context.Background())
	fim := make(chan error, 1)
	go func() { fim <- serveAPI(ctx, io.Discard, node, addr) }()
	defer func() {
		model.releaseAll()
		cancel()
		select {
		case err := <-fim:
			if err != nil {
				t.Errorf("serveAPI devia encerrar graciosamente, veio %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("serveAPI nao encerrou apos cancelamento do ctx")
		}
	}()
	esperarPorta(t, addr)

	base := "http://" + addr
	if code := submeterRun(t, base, "run-277-serve-1"); code != http.StatusCreated {
		t.Fatalf("o 1o submit (dentro do tecto) devia ser ADMITIDO com 201, veio %d", code)
	}
	// Espera o run ENTRAR mesmo no modelo: só então está registado no loop de serviço e a
	// contagem de in-flight vale 1. Sem esta barreira o teste seria uma corrida.
	select {
	case <-model.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("o 1o run nao chegou a correr — sem ele a contagem de in-flight nao sobe e a prova fica vacuosa")
	}

	if code := submeterRun(t, base, "run-277-serve-2"); code != http.StatusTooManyRequests {
		t.Fatalf("o 2o submit devia levar 429 do servidor que serveAPI construiu (AOS_INGRESS_MAX_INFLIGHT=1), veio %d.\n"+
			"Um 201 aqui significa que os limites lidos por ingressLimitsFromEnv NAO chegam ao NewAPIServer — o defeito que este teste existe para apanhar.", code)
	}
}
