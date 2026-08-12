package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AOS-247 (F5) — provas FALSIFICÁVEIS das duas metades da remediação do fallback de credencial
// do model gateway.
//
// Antes disto, [staticModelCredential.Fetch] devolvia um bearer de DEV embebido no binário
// sempre que AOS_MODEL_API_KEY_PATH estava ausente — INCLUSIVE sob AOS_MODE=production, e sem
// UMA linha que o declarasse. Do lado de fora, um nó de produção mal configurado era
// indistinguível de um nó que apresenta a credencial da organização.
//
// As duas metades, e os dois sentidos que estes testes fixam:
//
//   - PRODUÇÃO ⇒ RECUSA no ARRANQUE ([ErrProductionNeedsModelCredential]), não em runtime. O
//     gate vive em [parseModelFromEnv] (a fronteira que LÊ o ambiente) e não no `Fetch`, que
//     corre por-pedido e não tem por onde abortar o processo;
//   - REFERÊNCIA ⇒ o arranque passa mas DECLARA-SE ([devModelCredentialBanner]), e a linha
//     nunca traz o VALOR de credencial nenhuma — só a postura.
//
// A prova negativa (sem gateway, ou com credencial montada, o aviso NÃO sai) é tão importante
// como a positiva: um aviso incondicional treinaria o operador a ignorá-lo.

// aos247ProdEnvBase fixa uma superfície de PRODUÇÃO que passa TODAS as guardas anteriores
// (identidade endurecida + soberania forte) e não liga substrato durável — assim a única guarda
// que pode disparar a seguir é a de AOS-247, e um teste que falhe nomeia mesmo este defeito e
// não o do vizinho. Espelha prodKEKEnvBase (AOS-215) sem o gatilho da KEK durável. O gateway
// fica LIGADO contra o endpoint dado, SEM AOS_MODEL_API_KEY_PATH. Devolve o tempdir.
func aos247ProdEnvBase(t *testing.T, endpoint string) string {
	t.Helper()
	dir := t.TempDir()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gerar chave de teste: %v", err)
	}

	t.Setenv("AOS_MODE", "production")
	// identidade ENDURECIDA (passa ErrProductionNeedsHardenedIdentity).
	t.Setenv("AOS_ISSUER_ID", "iss:aos-247")
	t.Setenv("AOS_ISSUER_PUBKEY", hex.EncodeToString(pub))
	t.Setenv("AOS_ISSUER_KEY_PATH", "")
	t.Setenv("AOS_HUMANS", "operator")
	// soberania FORTE (passa ErrProductionNeedsSovereignRead + ErrProductionNeedsSovereignAuthority).
	t.Setenv("AOS_BOARD_REGIONS", "board:demo=eu")
	t.Setenv("AOS_SOVEREIGN_OIDC_ISSUER", "https://idp.example/realms/aos")
	t.Setenv("AOS_SOVEREIGN_OIDC_AUDIENCE", "aos-node")
	t.Setenv("AOS_SOVEREIGN_OIDC_JWKS_URI", "")
	t.Setenv("AOS_SOVEREIGN_OIDC_MAX_AGE", "")
	t.Setenv("AOS_SOVEREIGN_OIDC_REQUIRE_JTI", "")
	// SEM substrato durável ⇒ ErrProductionNeedsDurableKEK não entra em jogo.
	t.Setenv("AOS_WORM_PATH", "")
	t.Setenv("AOS_EVENTSTORE_PATH", "")
	t.Setenv("AOS_DURABLE_EXECUTION", "")
	t.Setenv("AOS_DSAR_VAULT_ADDR", "")
	t.Setenv("AOS_DSAR_VAULT_TOKEN_PATH", "")
	// MODEL GATEWAY LIGADO, credencial AUSENTE — o estado sob teste.
	t.Setenv("AOS_MODEL_ENDPOINT", endpoint)
	t.Setenv("AOS_MODEL_NAME", "gpt-4o") // está na allowlist ASSINADA embebida (regra board-eu).
	t.Setenv("AOS_MODEL_API_KEY_PATH", "")
	// higiene: variáveis que, herdadas da máquina, contaminariam a config.
	t.Setenv("AOS_API_ADDR", "")
	t.Setenv("AOS_OTLP_ENDPOINT", "")
	t.Setenv("AOS_MODEL_TOOLS", "")
	t.Setenv("AOS_MODEL_ALLOWLIST_BUNDLE_DIR", "")
	t.Setenv("AOS_MODEL_REGION", "")
	t.Setenv("AOS_MODEL_BOARD", "")
	t.Setenv("AOS_RETENTION_VERSION", "")
	t.Setenv("AOS_RETENTION_PERIODS", "")
	t.Setenv("AOS_OPERATORS", "")
	t.Setenv("AOS_APPROVERS_FILE", "")
	t.Setenv("AOS_HUMAN_OIDC_ISSUER", "")
	return dir
}

// aos247EscreverCredencial escreve um ficheiro de credencial com um valor ALEATÓRIO gerado em
// runtime — nunca um literal no código — e devolve (caminho, valor). O valor serve só para as
// asserções de NÃO-VAZAMENTO; nenhum teste o imprime.
func aos247EscreverCredencial(t *testing.T, dir string) (string, string) {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("gerar credencial de teste: %v", err)
	}
	valor := hex.EncodeToString(b[:])
	caminho := filepath.Join(dir, "model-key")
	if err := os.WriteFile(caminho, []byte(valor), 0o600); err != nil {
		t.Fatalf("escrever credencial de teste: %v", err)
	}
	return caminho, valor
}

// TestAOS247_ProducaoSemCredencialAborta é a asserção central do ticket, na fronteira REAL de
// arranque ([nodeConfigFromEnv]): com o gateway ligado e sem ficheiro de credencial, o nó de
// produção RECUSA compor-se. Se isto passasse, o nó arrancaria a apresentar o bearer de DEV.
func TestAOS247_ProducaoSemCredencialAborta(t *testing.T) {
	aos247ProdEnvBase(t, "http://gw.invalid/v1")

	cfg, err := nodeConfigFromEnv()
	if !errors.Is(err, ErrProductionNeedsModelCredential) {
		t.Fatalf("producao com o gateway ligado e sem AOS_MODEL_API_KEY_PATH devia dar ErrProductionNeedsModelCredential, veio: %v", err)
	}
	if cfg.Model != nil {
		t.Fatal("na recusa nao se devolve modelo composto (fail-closed, nao meio-ligado)")
	}
	if !strings.Contains(err.Error(), "AOS_MODEL_API_KEY_PATH") {
		t.Fatalf("o erro devia nomear a variavel que o operador tem de definir, veio: %v", err)
	}
}

// TestAOS247_ProducaoComCredencialArranca é a metade que impede a remediação de degenerar em
// "recusar sempre": com a credencial montada, a produção compõe o gateway normalmente.
func TestAOS247_ProducaoComCredencialArranca(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	dir := aos247ProdEnvBase(t, srv.URL)
	caminho, _ := aos247EscreverCredencial(t, dir)
	t.Setenv("AOS_MODEL_API_KEY_PATH", caminho)

	cfg, err := nodeConfigFromEnv()
	if err != nil {
		t.Fatalf("producao COM AOS_MODEL_API_KEY_PATH tem de compor-se, veio: %v", err)
	}
	if cfg.Model == nil {
		t.Fatal("com o gateway ligado e credencial montada, Config.Model devia estar composto")
	}
}

// TestAOS247_ProducaoSemGatewayNaoExigeCredencial delimita o gate: sem AOS_MODEL_ENDPOINT o nó
// usa o referenceModel e não apresenta bearer nenhum a ninguém — exigir a credencial aí seria
// recusar um arranque legítimo.
func TestAOS247_ProducaoSemGatewayNaoExigeCredencial(t *testing.T) {
	aos247ProdEnvBase(t, "")
	t.Setenv("AOS_MODEL_NAME", "")

	cfg, err := nodeConfigFromEnv()
	if err != nil {
		t.Fatalf("producao sem gateway tem de arrancar (referenceModel), veio: %v", err)
	}
	if cfg.Model != nil {
		t.Fatal("sem AOS_MODEL_ENDPOINT o modelo devia ficar nil (referenceModel)")
	}
}

// TestAOS247_GateVivePorPostura isola o gate no parser: a MESMA config só é recusada quando a
// postura é de produção. Se alguém voltar a mover a decisão para dentro do `Fetch` (onde não há
// por onde abortar o arranque), este teste cai.
func TestAOS247_GateVivePorPostura(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	t.Setenv("AOS_MODEL_ENDPOINT", srv.URL)
	t.Setenv("AOS_MODEL_NAME", "gpt-4o")
	t.Setenv("AOS_MODEL_API_KEY_PATH", "")
	t.Setenv("AOS_MODEL_TOOLS", "")
	t.Setenv("AOS_MODEL_ALLOWLIST_BUNDLE_DIR", "")

	if _, _, err := parseModelFromEnv(true); !errors.Is(err, ErrProductionNeedsModelCredential) {
		t.Fatalf("production=true sem credencial devia recusar, veio: %v", err)
	}
	mc, _, err := parseModelFromEnv(false)
	if err != nil {
		t.Fatalf("production=false com a mesma config tem de compor o gateway, veio: %v", err)
	}
	if mc == nil {
		t.Fatal("fora de producao o fallback e legitimo — o gateway devia ficar composto")
	}
}

// TestAOS247_ReferenciaDeclaraBearerDeDev prova a SEGUNDA metade no caminho de arranque REAL:
// [run] escreve a linha que declara o bearer de DEV. Sem esta linha o estado voltaria a ser
// indistinguível de um nó com a credencial da organização.
func TestAOS247_ReferenciaDeclaraBearerDeDev(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	banner := aos247RunBanner(t, srv.URL, "")

	if !strings.Contains(banner, "BEARER DE DEV EM USO") {
		t.Fatalf("o arranque de referencia com o gateway ligado e sem credencial tem de DECLARAR o bearer de DEV.\nBanner:\n%s", banner)
	}
	if !strings.Contains(banner, "AOS_MODEL_API_KEY_PATH") {
		t.Errorf("a linha devia nomear o remedio ALCANCAVEL pelo operador (a variavel de ambiente).\nBanner:\n%s", banner)
	}
	// NÃO-VAZAMENTO: a linha declara a POSTURA, nunca o segredo. O valor é obtido em runtime do
	// próprio provider (não há literal deste segredo no ficheiro de teste) e NUNCA é impresso —
	// nem na mensagem de falha.
	devBearer, err := staticModelCredential{}.Fetch(context.Background(), "openai", "eu")
	if err != nil {
		t.Fatalf("Fetch do fallback: %v", err)
	}
	if strings.Contains(banner, devBearer) {
		t.Fatal("o banner NAO pode conter o VALOR da credencial — declara-se a postura, nunca o segredo")
	}
}

// TestAOS247_ReferenciaComCredencialNaoDeclara é a prova NEGATIVA: com a credencial montada não
// há bearer de DEV em uso, logo não sai aviso nenhum — e o valor montado também nunca aparece.
func TestAOS247_ReferenciaComCredencialNaoDeclara(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	caminho, valor := aos247EscreverCredencial(t, t.TempDir())
	banner := aos247RunBanner(t, srv.URL, caminho)

	if strings.Contains(banner, "AVISO CREDENCIAL DE MODELO") {
		t.Errorf("com AOS_MODEL_API_KEY_PATH definido o aviso NAO devia sair — um aviso incondicional treina o operador a ignora-lo.\nBanner:\n%s", banner)
	}
	if strings.Contains(banner, valor) {
		t.Fatal("o banner NAO pode conter o VALOR da credencial montada")
	}
}

// TestAOS247_SemGatewayNaoDeclara: sem gateway composto o nó usa o referenceModel e não
// apresenta bearer nenhum — avisar sobre uma credencial que não existe seria ruído falso.
func TestAOS247_SemGatewayNaoDeclara(t *testing.T) {
	banner := aos247RunBanner(t, "", "")
	if strings.Contains(banner, "AVISO CREDENCIAL DE MODELO") {
		t.Errorf("sem AOS_MODEL_ENDPOINT nao ha bearer nenhum em uso — o aviso nao devia sair.\nBanner:\n%s", banner)
	}
}

// TestAOS247_BannerEstadosPuros cobre a FUNÇÃO nos seus quatro estados, incluindo o
// inalcançável pelo binário (gateway composto in-process sem endpoint de ambiente): o aviso
// segue o estado REALMENTE composto, não a intenção da config.
func TestAOS247_BannerEstadosPuros(t *testing.T) {
	t.Parallel()

	if lines := devModelCredentialBanner(false, ""); lines != nil {
		t.Errorf("sem gateway composto nao sai linha nenhuma, vieram %d: %v", len(lines), lines)
	}
	if lines := devModelCredentialBanner(false, "/etc/aos/model-key"); lines != nil {
		t.Errorf("sem gateway composto nao sai linha nenhuma (mesmo com caminho definido), vieram %d: %v", len(lines), lines)
	}
	if lines := devModelCredentialBanner(true, "/etc/aos/model-key"); lines != nil {
		t.Errorf("com credencial montada nao sai linha nenhuma, vieram %d: %v", len(lines), lines)
	}
	// Um caminho só com espaços é ausência de credencial (o parser faz TrimSpace): o aviso TEM
	// de sair, senão um typo no compose apagaria a declaração.
	lines := devModelCredentialBanner(true, "   ")
	if len(lines) != 1 {
		t.Fatalf("gateway composto sem credencial devia dar exactamente 1 linha, vieram %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "BEARER DE DEV EM USO") || !strings.Contains(lines[0], "AOS-247") {
		t.Fatalf("a linha devia declarar o bearer de DEV e o ticket que a impoe, veio: %q", lines[0])
	}
}

// aos247RunBanner corre [run] em modo de REFERÊNCIA (sem socket) com o gateway apontado a
// endpoint e a credencial em apiKeyPath (vazios ⇒ desligados), e devolve o banner. Fixa a
// superfície de ambiente inteira para que o resultado não dependa da máquina de quem testa —
// o molde de runWithoutTouchingBoardRegions (AOS-203).
func aos247RunBanner(t *testing.T, endpoint, apiKeyPath string) string {
	t.Helper()
	t.Setenv("AOS_MODE", "") // o aviso é para FORA de produção (em produção o arranque recusa).
	t.Setenv("AOS_API_ADDR", "")
	t.Setenv("AOS_ISSUER_ID", "iss:aos-247")
	t.Setenv("AOS_HUMANS", "operator")
	t.Setenv("AOS_ISSUER_PUBKEY", "")
	t.Setenv("AOS_ISSUER_KEY_PATH", "")
	t.Setenv("AOS_BOARD_REGIONS", "board:demo=eu")
	t.Setenv("AOS_EVENTSTORE_PATH", "")
	t.Setenv("AOS_WORM_PATH", "")
	t.Setenv("AOS_DURABLE_EXECUTION", "")
	t.Setenv("AOS_OTLP_ENDPOINT", "")
	t.Setenv("AOS_OPERATORS", "")
	t.Setenv("AOS_APPROVERS_FILE", "")
	t.Setenv("AOS_MODEL_ENDPOINT", endpoint)
	if endpoint != "" {
		t.Setenv("AOS_MODEL_NAME", "gpt-4o")
	} else {
		t.Setenv("AOS_MODEL_NAME", "")
	}
	t.Setenv("AOS_MODEL_API_KEY_PATH", apiKeyPath)
	t.Setenv("AOS_MODEL_TOOLS", "")
	t.Setenv("AOS_MODEL_ALLOWLIST_BUNDLE_DIR", "")
	t.Setenv("AOS_MODEL_REGION", "")
	t.Setenv("AOS_MODEL_BOARD", "")

	var sb strings.Builder
	if err := run(&sb); err != nil {
		t.Fatalf("run devia arrancar fora de producao, veio: %v", err)
	}
	return sb.String()
}
