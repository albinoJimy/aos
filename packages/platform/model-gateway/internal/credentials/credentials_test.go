package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/internal/adapters/oauth"
	"github.com/aos-ref/platform/model-gateway/port"
)

// mutClock é um relógio mutável concorrente-seguro (TTL/rotação deterministas,
// sem time.Now real).
type mutClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(t time.Time) *mutClock { return &mutClock{t: t} }
func (c *mutClock) now() time.Time   { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *mutClock) set(t time.Time)  { c.mu.Lock(); c.t = t; c.mu.Unlock() }

// newTestSource constrói uma Source sobre um broker fake e o registo default dos
// três provedores, com TTL=100s e refresh lead=20s (janela de refresh: >= 80s).
func newTestSource(clock func() time.Time, broker CredentialBroker, allowed ...ProviderRegion) *Source {
	reg := oauth.DefaultRegistry(oauth.Options{Clock: clock})
	return NewSource(broker, reg, Config{
		TTL:         100 * time.Second,
		RefreshLead: 20 * time.Second,
		Clock:       clock,
		Allowed:     allowed,
	})
}

func chatReq(model string) port.ChatRequest {
	return port.ChatRequest{Model: model, Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}}}
}

// TestSource_RenovaAntesDoTTL prova que o cache RENOVA a credencial ANTES de o TTL
// expirar: dentro da janela de refresh (mas antes da expiração) uma Fetch reemite
// um lease novo, em vez de servir a credencial prestes a expirar.
func TestSource_RenovaAntesDoTTL(t *testing.T) {
	t.Parallel()
	clock := newClock(time.Unix(0, 0))
	broker := NewFakeBroker(clock.now, time.Hour)
	broker.SetSecret("openai", "eu", "k0")
	src := newTestSource(clock.now, broker, ProviderRegion{Provider: "openai", Region: "eu"})

	if _, err := src.Fetch(context.Background(), "openai", "eu"); err != nil {
		t.Fatalf("Fetch t=0: %v", err)
	}
	if got := broker.IssueCount(); got != 1 {
		t.Fatalf("IssueCount t=0 = %d, quer 1", got)
	}
	// t=50s: fora da janela de refresh (80s) — serve do cache, sem reemitir.
	clock.set(time.Unix(50, 0))
	if _, err := src.Fetch(context.Background(), "openai", "eu"); err != nil {
		t.Fatalf("Fetch t=50: %v", err)
	}
	if got := broker.IssueCount(); got != 1 {
		t.Fatalf("IssueCount t=50 = %d, quer 1 (nao devia reemitir dentro do TTL)", got)
	}
	// t=85s: dentro da janela de refresh, ANTES da expiração (100s) — reemite.
	clock.set(time.Unix(85, 0))
	if _, err := src.Fetch(context.Background(), "openai", "eu"); err != nil {
		t.Fatalf("Fetch t=85: %v", err)
	}
	if got := broker.IssueCount(); got != 2 {
		t.Fatalf("IssueCount t=85 = %d, quer 2 (devia RENOVAR antes do TTL expirar)", got)
	}
}

// TestSource_Revogacao prova que uma credencial revogada deixa de ser servida: a
// entrada de cache é invalidada, o lease é revogado no broker, e a próxima Fetch
// obtém um lease NOVO (o antigo nunca é reutilizado).
func TestSource_Revogacao(t *testing.T) {
	t.Parallel()
	clock := newClock(time.Unix(0, 0))
	broker := NewFakeBroker(clock.now, time.Hour)
	broker.SetSecret("openai", "eu", "k0")
	src := newTestSource(clock.now, broker, ProviderRegion{Provider: "openai", Region: "eu"})

	if _, err := src.Fetch(context.Background(), "openai", "eu"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	lid1, ok := src.leaseIDFor("openai", "eu")
	if !ok {
		t.Fatal("sem lease em cache apos Fetch")
	}
	if err := src.Revoke(context.Background(), "openai", "eu"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !broker.Revoked(lid1) {
		t.Fatalf("lease %q devia ter sido revogado no broker", lid1)
	}
	if _, ok := src.leaseIDFor("openai", "eu"); ok {
		t.Fatal("cache devia estar invalidado apos revogacao")
	}
	// A próxima Fetch (mesmo dentro do TTL original) reemite um lease NOVO.
	if _, err := src.Fetch(context.Background(), "openai", "eu"); err != nil {
		t.Fatalf("Fetch pos-revogacao: %v", err)
	}
	lid2, _ := src.leaseIDFor("openai", "eu")
	if lid2 == lid1 {
		t.Fatalf("lease revogado %q foi reutilizado", lid1)
	}
	if got := broker.IssueCount(); got != 2 {
		t.Fatalf("IssueCount = %d, quer 2 (revogacao forca reemissao)", got)
	}
}

// blockingAdapter é um adaptador que BLOQUEIA dentro de Chat (simulando uma
// chamada em curso) e captura o KeyID da credencial que recebeu. Serve o teste de
// rotação: prova que uma chamada em curso completa com a chave ANTIGA.
type blockingAdapter struct {
	provider string
	entered  chan string
	release  chan struct{}
	done     chan struct{}
}

func newBlockingAdapter(provider string) *blockingAdapter {
	return &blockingAdapter{
		provider: provider,
		entered:  make(chan string, 4),
		release:  make(chan struct{}),
		done:     make(chan struct{}, 4),
	}
}

func (a *blockingAdapter) Provider() string { return a.provider }

func (a *blockingAdapter) Chat(_ context.Context, req port.ChatRequest, cred adapters.Credential) (port.ChatResponse, error) {
	a.entered <- cred.KeyID()
	<-a.release
	a.done <- struct{}{}
	return port.ChatResponse{
		Model:   req.Model,
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "ok"}, FinishReason: "stop"}},
	}, nil
}

func (a *blockingAdapter) ChatStream(_ context.Context, req port.ChatRequest, _ adapters.Credential) (port.ChatStream, error) {
	return port.NewSliceStream([]port.ChatStreamDelta{{Role: port.RoleAssistant, Content: "ok", FinishReason: "stop"}}), nil
}

func (a *blockingAdapter) Embeddings(_ context.Context, req port.EmbeddingsRequest, _ adapters.Credential) (port.EmbeddingsResponse, error) {
	return port.EmbeddingsResponse{Model: req.Model, Object: "list"}, nil
}

// TestSource_RotacaoNaoInterrompeInFlight prova o invariante central: uma ROTAÇÃO
// de chave concorrente com uma chamada EM CURSO não a interrompe — a chamada que
// já obteve a chave antiga completa com ela; só as chamadas NOVAS vêem a chave
// nova. A troca da referência de cache é atómica; -race guarda a corrida.
func TestSource_RotacaoNaoInterrompeInFlight(t *testing.T) {
	t.Parallel()
	clock := newClock(time.Unix(0, 0))
	broker := NewFakeBroker(clock.now, time.Hour)
	broker.SetSecret("openai", "eu", "gen0")
	src := newTestSource(clock.now, broker, ProviderRegion{Provider: "openai", Region: "eu"})

	adapter := newBlockingAdapter("openai")
	gw := modelgateway.New(adapter,
		modelgateway.WithCredentialSource(src),
		modelgateway.WithDefaultRegion("eu"),
		modelgateway.WithClock(clock.now),
	)

	// Chamada 1: obtém a chave gen0 e fica EM CURSO (parada no adaptador).
	go func() { _, _ = gw.Chat(context.Background(), chatReq("m")) }()
	keyID1 := <-adapter.entered

	// ROTAÇÃO: o vault roda a chave; avança o relógio para a janela de refresh.
	broker.SetSecret("openai", "eu", "gen1")
	clock.set(time.Unix(85, 0))

	// Chamada 2 (nova): deve ver a chave gen1.
	go func() { _, _ = gw.Chat(context.Background(), chatReq("m")) }()
	keyID2 := <-adapter.entered

	close(adapter.release) // liberta ambas
	<-adapter.done
	<-adapter.done

	want0 := adapters.NewCredential("openai", "eu", "gen0").KeyID()
	want1 := adapters.NewCredential("openai", "eu", "gen1").KeyID()
	if keyID1 != want0 {
		t.Errorf("chamada em curso NAO usou a chave antiga: keyID1=%s want=%s", keyID1, want0)
	}
	if keyID2 != want1 {
		t.Errorf("chamada nova NAO usou a chave rodada: keyID2=%s want=%s", keyID2, want1)
	}
	if keyID1 == keyID2 {
		t.Fatal("rotacao nao teve efeito: as duas chamadas usaram a mesma chave")
	}
}

// TestSource_ConcorrenciaFetchRotacao é um stress -race: muitos Fetch concorrentes
// enquanto o segredo roda em paralelo. Cada Fetch tem de devolver uma credencial
// consistente (KeyID não vazio, sem erro) — o mutex guarda a troca da referência.
func TestSource_ConcorrenciaFetchRotacao(t *testing.T) {
	t.Parallel()
	clock := newClock(time.Unix(0, 0))
	broker := NewFakeBroker(clock.now, time.Hour)
	broker.SetSecret("openai", "eu", "gen-inicial")
	// lead >= TTL força CADA Fetch a reemitir — máxima contenção broker+cache.
	reg := oauth.DefaultRegistry(oauth.Options{Clock: clock.now})
	src := NewSource(broker, reg, Config{
		TTL: 100 * time.Second, RefreshLead: 100 * time.Second, Clock: clock.now,
		Allowed: []ProviderRegion{{Provider: "openai", Region: "eu"}},
	})

	var wg sync.WaitGroup
	// Rotadores.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				broker.SetSecret("openai", "eu", fmt.Sprintf("gen-%d-%d", r, i))
			}
		}(r)
	}
	// Leitores.
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				cred, err := src.Fetch(context.Background(), "openai", "eu")
				if err != nil {
					t.Errorf("Fetch concorrente: %v", err)
					return
				}
				if cred.KeyID() == "" {
					t.Error("Fetch concorrente devolveu credencial sem segredo")
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestSource_FailClosed_RegiaoNaoConfigurada prova a fronteira de soberania: um
// par (provider, região) não configurado falha fail-closed com erro ATRIBUÍVEL, e
// NUNCA cai para outra região — mesmo que outra região tenha material.
func TestSource_FailClosed_RegiaoNaoConfigurada(t *testing.T) {
	t.Parallel()
	clock := newClock(time.Unix(0, 0))
	broker := NewFakeBroker(clock.now, time.Hour)
	broker.SetSecret("openai", "us", "chave-us") // us TEM material...
	// ...mas a origem só configura openai/eu.
	src := newTestSource(clock.now, broker, ProviderRegion{Provider: "openai", Region: "eu"})

	cred, err := src.Fetch(context.Background(), "openai", "us")
	if err == nil {
		t.Fatal("regiao nao configurada devia falhar fail-closed")
	}
	var ce *CredentialError
	if !errors.As(err, &ce) {
		t.Fatalf("erro devia ser *CredentialError atribuivel, obtido %T: %v", err, err)
	}
	if ce.Provider != "openai" || ce.Region != "us" {
		t.Errorf("erro nao atribuivel ao par pedido: provider=%q regiao=%q", ce.Provider, ce.Region)
	}
	if !errors.Is(err, ErrRegionNotConfigured) {
		t.Errorf("errors.Is(ErrRegionNotConfigured) falhou: %v", err)
	}
	if cred.KeyID() != "" {
		t.Error("nao devia devolver credencial")
	}
	// Prova que NUNCA caiu para a chave de us: o broker nem foi consultado.
	if got := broker.IssueCount(); got != 0 {
		t.Errorf("broker foi consultado (%d) para uma regiao nao configurada", got)
	}
}

// TestSource_FailClosed_SemMaterial prova que, para um par CONFIGURADO mas sem
// material no broker, a Fetch falha fail-closed atribuível (ErrNoMaterial) e não
// serve a chave de outra região que exista.
func TestSource_FailClosed_SemMaterial(t *testing.T) {
	t.Parallel()
	clock := newClock(time.Unix(0, 0))
	broker := NewFakeBroker(clock.now, time.Hour)
	broker.SetSecret("openai", "us", "chave-us") // só us tem material
	src := newTestSource(clock.now, broker,
		ProviderRegion{Provider: "openai", Region: "eu"},
		ProviderRegion{Provider: "openai", Region: "us"},
	)

	_, err := src.Fetch(context.Background(), "openai", "eu") // eu configurado, sem material
	if err == nil {
		t.Fatal("par sem material devia falhar fail-closed")
	}
	var ce *CredentialError
	if !errors.As(err, &ce) || ce.Region != "eu" {
		t.Fatalf("erro devia ser atribuivel a eu, obtido %v", err)
	}
	if !errors.Is(err, ErrNoMaterial) {
		t.Errorf("errors.Is(ErrNoMaterial) falhou: %v", err)
	}
}

// captureAdapter regista o KeyID da credencial injectada (asserção de injecção
// server-side sem revelar o segredo).
type captureAdapter struct {
	provider string
	keyID    string
	resp     port.ChatResponse
}

func (a *captureAdapter) Provider() string { return a.provider }
func (a *captureAdapter) Chat(_ context.Context, req port.ChatRequest, cred adapters.Credential) (port.ChatResponse, error) {
	a.keyID = cred.KeyID()
	r := a.resp
	r.Model = req.Model
	return r, nil
}
func (a *captureAdapter) ChatStream(context.Context, port.ChatRequest, adapters.Credential) (port.ChatStream, error) {
	return port.NewSliceStream([]port.ChatStreamDelta{{FinishReason: "stop"}}), nil
}
func (a *captureAdapter) Embeddings(_ context.Context, req port.EmbeddingsRequest, _ adapters.Credential) (port.EmbeddingsResponse, error) {
	return port.EmbeddingsResponse{Model: req.Model, Object: "list"}, nil
}

// TestIntegracao_GW_BrokerFake_InjeccaoServerSide é o teste de integração: o GW
// pede a chave ao broker fake (via Source), injecta-a server-side no adaptador e
// executa. Prova que a credencial correcta chegou ao adaptador (por KeyID, sem
// expor o segredo) e que a aquisição foi JIT (um lease emitido).
func TestIntegracao_GW_BrokerFake_InjeccaoServerSide(t *testing.T) {
	t.Parallel()
	clock := newClock(time.Unix(1_700_000_000, 0))
	broker := NewFakeBroker(clock.now, time.Hour)
	broker.SetSecret("openai", "eu", "sk-infra-real")
	src := newTestSource(clock.now, broker, ProviderRegion{Provider: "openai", Region: "eu"})

	adapter := &captureAdapter{provider: "openai", resp: port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "resposta"}, FinishReason: "stop"}},
		Usage:   port.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8},
	}}
	gw := modelgateway.New(adapter,
		modelgateway.WithCredentialSource(src),
		modelgateway.WithDefaultRegion("eu"),
		modelgateway.WithClock(clock.now),
	)
	resp, err := gw.Chat(context.Background(), chatReq("m"))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Choices[0].Message.Content != "resposta" {
		t.Errorf("resposta nao normalizada: %+v", resp)
	}
	// Injecção server-side: o adaptador recebeu a credencial de infra correcta
	// (openai/eu, pass-through) — provado por KeyID, sem revelar o segredo.
	want := adapters.NewCredential("openai", "eu", "sk-infra-real").KeyID()
	if adapter.keyID != want {
		t.Errorf("credencial injectada errada: keyID=%s want=%s", adapter.keyID, want)
	}
	if got := broker.IssueCount(); got != 1 {
		t.Errorf("aquisicao devia ser JIT (1 lease), obtido %d", got)
	}
}

// TestSeguranca_SegredoNaoVazaParaSpanNemResposta prova que a chave de infra NUNCA
// aparece na resposta ao agente nem nos atributos de span (ADR-006). Usa o
// provedor OpenAI (pass-through: o token É a chave), logo o segredo sentinela
// atravessa até ao adaptador server-side mas não pode subir de volta.
func TestSeguranca_SegredoNaoVazaParaSpanNemResposta(t *testing.T) {
	t.Parallel()
	const sentinel = "SENTINEL-INFRA-KEY-42"
	clock := newClock(time.Unix(1_700_000_000, 0))
	broker := NewFakeBroker(clock.now, time.Hour)
	broker.SetSecret("openai", "eu", sentinel)
	src := newTestSource(clock.now, broker, ProviderRegion{Provider: "openai", Region: "eu"})

	f := adapters.NewFakeAdapter("openai")
	f.SetChatResponse("m", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "resposta segura"}, FinishReason: "stop"}},
		Usage:   port.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8},
	})
	tr := &agentruntime.RecordingTracer{}
	gw := modelgateway.New(f,
		modelgateway.WithCredentialSource(src),
		modelgateway.WithTracer(tr),
		modelgateway.WithDefaultRegion("eu"),
		modelgateway.WithClock(clock.now),
	)
	resp, err := gw.Chat(context.Background(), chatReq("m"))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	// Resposta ao agente: nem em JSON nem em %+v pode conter o segredo.
	b, _ := json.Marshal(resp)
	if strings.Contains(string(b), sentinel) {
		t.Fatalf("segredo VAZOU na resposta JSON ao agente: %s", b)
	}
	if strings.Contains(fmt.Sprintf("%+v", resp), sentinel) {
		t.Fatal("segredo VAZOU na resposta formatada ao agente")
	}
	// Spans OTel GenAI: nenhum atributo (chave ou valor) pode conter o segredo.
	spans := tr.Spans()
	if len(spans) == 0 {
		t.Fatal("nenhum span capturado (esperava span 'chat')")
	}
	for _, sp := range spans {
		for k, v := range sp.Attributes {
			if strings.Contains(k, sentinel) || strings.Contains(fmt.Sprint(v), sentinel) {
				t.Fatalf("segredo VAZOU no span: %s=%v", k, v)
			}
		}
	}
}

// TestSeguranca_ScanDeSegredos varre TODA a árvore de código de produção
// (não-teste) do módulo model-gateway e prova que não há nenhuma chave de provedor
// hard-coded (ADR-006: a chave nunca em código). Cobre não só credentials/oauth mas
// também gateway.go, pipeline/, port/ e adapters/ (o scan estreito deixava passar
// uma chave hard-coded fora daqueles dois dirs). Ignora _test.go (segredos de
// fixture deliberados) e dirs testdata (fixtures do arch-lint).
func TestSeguranca_ScanDeSegredos(t *testing.T) {
	t.Parallel()
	// Padrões de chave/segredo REAL de provedor: o prefixo distintivo SEGUIDO de
	// material de alta entropia. Exigir o material a seguir (não só o prefixo)
	// evita falsos-positivos em prosa/expressões que mencionem o prefixo (ex.: a
	// própria heurística de redacção de openai_http.go refere "ya29."/"sk-") e
	// ainda apanha a chave clássica da OpenAI (`sk-` + entropia), que o scan
	// anterior deixava passar.
	badPatterns := []*regexp.Regexp{
		regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`),  // Anthropic
		regexp.MustCompile(`sk-live-[A-Za-z0-9]{16,}`),   // Stripe/live
		regexp.MustCompile(`sk-proj-[A-Za-z0-9_-]{20,}`), // OpenAI project
		regexp.MustCompile(`sk-or-v1-[A-Za-z0-9]{16,}`),  // OpenRouter
		regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),        // OpenAI clássica (sk- + entropia)
		regexp.MustCompile(`AIzaSy[A-Za-z0-9_-]{20,}`),   // Google API key
		regexp.MustCompile(`ya29\.[A-Za-z0-9._-]{20,}`),  // Google OAuth access token
		regexp.MustCompile(`AKIA[0-9A-Z]{16}`),           // AWS access key id
		regexp.MustCompile(`xoxb-[A-Za-z0-9-]{20,}`),     // Slack bot token
		regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`),        // GitHub PAT
	}
	// Marcadores literais de chave privada PEM (sem entropia a exigir).
	pemMarkers := []string{"-----BEGIN PRIVATE KEY", "-----BEGIN RSA PRIVATE KEY"}

	// Raiz do módulo model-gateway (o teste corre em internal/credentials).
	root := filepath.Join("..", "..")
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		src := string(data)
		for _, re := range badPatterns {
			if m := re.FindString(src); m != "" {
				t.Errorf("possivel segredo hard-coded em %s: %q", path, m)
			}
		}
		for _, marker := range pemMarkers {
			if strings.Contains(src, marker) {
				t.Errorf("possivel chave privada hard-coded em %s: %q", path, marker)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s): %v", root, err)
	}
	if scanned == 0 {
		t.Fatal("nenhum ficheiro de producao varrido (scan invalido)")
	}
}

// TestSource_TTLLimitadoPelaExpiracaoReal prova a invariante JIT/TTL: o TTL do
// cache é LIMITADO pela expiração REAL do token, não pelo TTL sintético do cache.
// Com um TTL de cache LONGO (600s) mas um TokenTTL CURTO (60s) num mecanismo OAuth
// derivado (anthropic), a credencial é RENOVADA antes da expiração real do token —
// nunca se serve um token já expirado no provedor só porque o TTL do cache não passou.
func TestSource_TTLLimitadoPelaExpiracaoReal(t *testing.T) {
	t.Parallel()
	clock := newClock(time.Unix(0, 0))
	broker := NewFakeBroker(clock.now, time.Hour) // lease longo (1h) — não é o limite
	broker.SetSecret("anthropic", "eu", "material-anthropic")
	// TokenTTL curto (60s) << TTL do cache (600s): a expiração do TOKEN deve mandar.
	reg := oauth.DefaultRegistry(oauth.Options{TokenTTL: 60 * time.Second, Clock: clock.now})
	src := NewSource(broker, reg, Config{
		TTL: 600 * time.Second, RefreshLead: 10 * time.Second, Clock: clock.now,
		Allowed: []ProviderRegion{{Provider: "anthropic", Region: "eu"}},
	})

	if _, err := src.Fetch(context.Background(), "anthropic", "eu"); err != nil {
		t.Fatalf("Fetch t=0: %v", err)
	}
	if got := broker.IssueCount(); got != 1 {
		t.Fatalf("IssueCount t=0 = %d, quer 1", got)
	}
	// t=45s: token expira a 60s; janela de refresh = 60-10 = 50s. Ainda fresca.
	clock.set(time.Unix(45, 0))
	if _, err := src.Fetch(context.Background(), "anthropic", "eu"); err != nil {
		t.Fatalf("Fetch t=45: %v", err)
	}
	if got := broker.IssueCount(); got != 1 {
		t.Fatalf("IssueCount t=45 = %d, quer 1 (ainda dentro da vida do token)", got)
	}
	// t=55s: >= 50s (refresh-lead sobre a expiração REAL do token, 60s) — RENOVA
	// ANTES de o token expirar. Sem o fix, o cache serviria até ~590s (600-10),
	// muito depois de o token expirar a 60s.
	clock.set(time.Unix(55, 0))
	if _, err := src.Fetch(context.Background(), "anthropic", "eu"); err != nil {
		t.Fatalf("Fetch t=55: %v", err)
	}
	if got := broker.IssueCount(); got != 2 {
		t.Fatalf("IssueCount t=55 = %d, quer 2 (devia RENOVAR antes da expiracao REAL do token)", got)
	}
}

// TestIntegracao_GW_ErroAtribuivel_ViaChat fecha AC5 ao nível da FACHADA: um
// gw.Chat construído sobre a Source REAL propaga o [*CredentialError] ATRIBUÍVEL
// (provider+região, errors.Is da causa) — não um erro genérico — e o adaptador
// NUNCA é invocado (fail-closed sem fallback para outra conta/região).
func TestIntegracao_GW_ErroAtribuivel_ViaChat(t *testing.T) {
	t.Parallel()
	clock := newClock(time.Unix(1_700_000_000, 0))
	broker := NewFakeBroker(clock.now, time.Hour)
	broker.SetSecret("openai", "us", "chave-us") // us TEM material, mas...
	// ...a Source só configura openai/eu (que NÃO tem material no broker).
	src := newTestSource(clock.now, broker, ProviderRegion{Provider: "openai", Region: "eu"})

	adapter := &captureAdapter{provider: "openai"}
	newGW := func(region string) *modelgateway.Gateway {
		return modelgateway.New(adapter,
			modelgateway.WithCredentialSource(src),
			modelgateway.WithDefaultRegion(region),
			modelgateway.WithClock(clock.now),
		)
	}

	// (a) Região NÃO configurada (us): fail-closed ErrRegionNotConfigured, atribuível
	// a us — NUNCA cai para a chave de us que existe no broker.
	_, err := newGW("us").Chat(context.Background(), chatReq("m"))
	if err == nil {
		t.Fatal("gw.Chat devia falhar fail-closed para regiao nao configurada")
	}
	var ce *CredentialError
	if !errors.As(err, &ce) {
		t.Fatalf("erro via gw.Chat devia ser *CredentialError atribuivel, obtido %T: %v", err, err)
	}
	if ce.Provider != "openai" || ce.Region != "us" {
		t.Errorf("erro nao atribuivel ao par pedido: provider=%q regiao=%q", ce.Provider, ce.Region)
	}
	if !errors.Is(err, ErrRegionNotConfigured) {
		t.Errorf("errors.Is(ErrRegionNotConfigured) via gw.Chat falhou: %v", err)
	}
	if adapter.keyID != "" {
		t.Error("adaptador foi invocado apesar do fail-closed (nao devia)")
	}

	// (b) Região configurada (eu) mas SEM material: fail-closed ErrNoMaterial,
	// atribuível a eu.
	_, err = newGW("eu").Chat(context.Background(), chatReq("m"))
	if err == nil {
		t.Fatal("gw.Chat devia falhar fail-closed para par sem material")
	}
	ce = nil
	if !errors.As(err, &ce) || ce.Region != "eu" || ce.Provider != "openai" {
		t.Fatalf("erro via gw.Chat devia ser atribuivel a openai/eu, obtido %v", err)
	}
	if !errors.Is(err, ErrNoMaterial) {
		t.Errorf("errors.Is(ErrNoMaterial) via gw.Chat falhou: %v", err)
	}
	if adapter.keyID != "" {
		t.Error("adaptador foi invocado apesar do fail-closed (nao devia)")
	}
}

// TestIntegracao_GW_InjeccaoPorProvedor cobre AC1 ao nível da FACHADA para os TRÊS
// provedores: o caminho completo Source→oauth→gw.Chat→adaptador injecta, para cada
// mecanismo (api_key/service_oauth/federated), a credencial ESPECÍFICA do provedor
// (por KeyID, sem revelar o segredo) — não apenas openai (pass-through).
func TestIntegracao_GW_InjeccaoPorProvedor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		provider string
		newProv  func(oauth.Options) oauth.Provider
	}{
		{"openai", oauth.NewOpenAI},
		{"anthropic", oauth.NewAnthropic},
		{"google", oauth.NewGoogle},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.provider, func(t *testing.T) {
			t.Parallel()
			clock := newClock(time.Unix(1_700_000_000, 0))
			broker := NewFakeBroker(clock.now, time.Hour)
			secret := "material-vault-" + tc.provider
			broker.SetSecret(tc.provider, "eu", secret)
			src := newTestSource(clock.now, broker, ProviderRegion{Provider: tc.provider, Region: "eu"})

			adapter := &captureAdapter{provider: tc.provider, resp: port.ChatResponse{
				Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "ok"}, FinishReason: "stop"}},
			}}
			gw := modelgateway.New(adapter,
				modelgateway.WithCredentialSource(src),
				modelgateway.WithDefaultRegion("eu"),
				modelgateway.WithClock(clock.now),
			)
			if _, err := gw.Chat(context.Background(), chatReq("m")); err != nil {
				t.Fatalf("%s: Chat: %v", tc.provider, err)
			}

			// KeyID esperado: derivado pela MESMA troca OAuth do provedor (o KeyID
			// depende só do segredo do token, específico do mecanismo — não do
			// relógio nem do TTL). Para anthropic/google, difere do material bruto.
			wantCred, _, err := tc.newProv(oauth.Options{Clock: clock.now}).
				Exchange(context.Background(), oauth.NewMaterial(tc.provider, "eu", secret, clock.now().Add(time.Hour)))
			if err != nil {
				t.Fatalf("%s: Exchange esperado: %v", tc.provider, err)
			}
			if wantCred.KeyID() == "" {
				t.Fatal("KeyID esperado vazio — setup invalido")
			}
			if adapter.keyID != wantCred.KeyID() {
				t.Errorf("%s: credencial injectada errada: keyID=%s want=%s", tc.provider, adapter.keyID, wantCred.KeyID())
			}
			// Mecanismos derivados (não pass-through) NÃO injectam o material bruto.
			if tc.provider != "openai" {
				raw := adapters.NewCredential(tc.provider, "eu", secret).KeyID()
				if adapter.keyID == raw {
					t.Errorf("%s: injectou o material BRUTO em vez do token derivado", tc.provider)
				}
			}
			if got := broker.IssueCount(); got != 1 {
				t.Errorf("%s: aquisicao devia ser JIT (1 lease), obtido %d", tc.provider, got)
			}
		})
	}
}

// TestLease_MarshalJSON_Redige prova que encoding/json de um Lease serializa a
// forma REDIGIDA (ADR-006): a garantia é imposta pelo tipo (MarshalJSON), não
// apenas pela omissão do campo não-exportado. Um lease traz o segredo de infra.
func TestLease_MarshalJSON_Redige(t *testing.T) {
	t.Parallel()
	const secret = "sk-lease-infra-abcdef1234567890"
	broker := NewFakeBroker(func() time.Time { return time.Unix(1, 0) }, time.Hour)
	broker.SetSecret("openai", "eu", secret)
	lease, err := broker.Issue(context.Background(), "openai", "eu")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	b, err := json.Marshal(lease)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatalf("segredo VAZOU no JSON do Lease: %s", b)
	}
	if !strings.Contains(string(b), "REDACTED") {
		t.Errorf("JSON do Lease devia redigir: %s", b)
	}
}

// TestReferenceBroker_FailClosed confirma que o broker de referência (não ligado
// ao vault) recusa emitir — impede uso acidental em produção sem o vault.
func TestReferenceBroker_FailClosed(t *testing.T) {
	t.Parallel()
	var b CredentialBroker = ReferenceBroker{}
	_, err := b.Issue(context.Background(), "openai", "eu")
	if !errors.Is(err, ErrNotWired) {
		t.Fatalf("broker de referencia devia falhar ErrNotWired, obtido %v", err)
	}
}
