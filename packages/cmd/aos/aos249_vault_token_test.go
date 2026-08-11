package main

// AOS-249 (achado F6) — HIGIENE DA CUSTÓDIA DA KEK NO VAULT: validação de esquema no arranque e
// validade do TOKEN a valer na prontidão.
//
// O defeito tinha duas metades, e ambas se manifestavam como SILÊNCIO:
//
//   - `AOS_DSAR_VAULT_ADDR` não era validada, enquanto o gémeo de attestation NO MESMO BINÁRIO
//     recusa http fora de loopback. O token que DESTRÓI chaves de titulares viajava em claro;
//   - o token era lido UMA vez e nunca renovado. Com o token de curta duração que o próprio
//     README recomenda, a custódia morria e o `/readyz` ficava VERDE — porque a única sonda que
//     existia (`/v1/sys/seal-status`) é NÃO-AUTENTICADA e responde 200 a quem já não tem
//     credencial nenhuma.
//
// Estes testes provam as duas metades e, sobretudo, provam a propriedade que interessa: a
// expiração do token NÃO PASSA EM SILÊNCIO — vira readiness vermelha ANTES do instante da
// expiração, e um erase/expire nesse estado falha fechado em vez de se dar por feito.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------
// Vault falso: seal-status + auth/token/* + motor Transit
// ---------------------------------------------------------------------------

// fakeVault é um Vault mínimo com as TRÊS superfícies que a custódia toca: `sys/seal-status`
// (não-autenticado), `auth/token/{lookup,renew}-self` (autenticado — é aqui que se prova a
// credencial) e o motor Transit. Tudo o que é autenticado exige o token corrente: é assim que
// "token expirado" se simula com fidelidade — o Vault continua saudável e responde 403 a NÓS.
type fakeVault struct {
	mu sync.Mutex

	aceite       string // token aceite nos caminhos autenticados ("" ⇒ aceita qualquer)
	selado       bool
	ttl          int64 // TTL devolvido por lookup-self (segundos); 0 = token sem expiração
	renovavel    bool
	ttlAposRenew int64 // TTL após um renew-self bem-sucedido
	renovacoes   int   // quantos renew-self foram aceites

	chaves     map[string]bool // chaves Transit vivas
	shredFalha bool            // o DELETE responde OK mas a chave SOBREVIVE (destruição falsa)
}

func novoFakeVault() *fakeVault {
	return &fakeVault{ttl: 3600, renovavel: true, ttlAposRenew: 3600, chaves: map[string]bool{}}
}

func (f *fakeVault) contaRenovacoes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.renovacoes
}

func (f *fakeVault) defineTTL(ttl int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ttl = ttl
}

func (f *fakeVault) defineAceite(tok string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aceite = tok
}

func (f *fakeVault) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// (a) NÃO-AUTENTICADO — a sonda de saúde do Vault que já existia.
	if r.URL.Path == "/v1/sys/seal-status" {
		fmt.Fprintf(w, `{"type":"shamir","initialized":true,"sealed":%t}`, f.selado)
		return
	}
	// (b) AUTENTICADO — tudo o resto. Um token que o Vault já não reconhece leva 403, tal como
	// no Vault real depois da expiração.
	if f.aceite != "" && r.Header.Get("X-Vault-Token") != f.aceite {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/v1/auth/token/lookup-self":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"ttl": f.ttl, "renewable": f.renovavel},
		})
		return
	case "/v1/auth/token/renew-self":
		if !f.renovavel {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.renovacoes++
		f.ttl = f.ttlAposRenew
		w.WriteHeader(http.StatusOK)
		return
	}
	// (c) Motor Transit.
	resto := strings.TrimPrefix(r.URL.Path, "/v1/transit/")
	switch {
	case strings.HasPrefix(resto, "keys/") && strings.HasSuffix(resto, "/config"):
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(resto, "keys/"):
		nome := strings.TrimPrefix(resto, "keys/")
		switch r.Method {
		case http.MethodPost:
			f.chaves[nome] = true
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			if !f.shredFalha {
				delete(f.chaves, nome)
			}
			w.WriteHeader(http.StatusNoContent)
		default: // GET — a VERIFICAÇÃO da destruição: 404 prova que a chave morreu.
			if f.chaves[nome] {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"name": nome}})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}
	case strings.HasPrefix(resto, "encrypt/"):
		nome := strings.TrimPrefix(resto, "encrypt/")
		if !f.chaves[nome] {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var in struct {
			Plaintext string `json:"plaintext"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		writeData(w, "ciphertext", "vault:v1:"+in.Plaintext)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// ---------------------------------------------------------------------------
// AC1 — o esquema de AOS_DSAR_VAULT_ADDR é validado, com o critério do GÉMEO
// ---------------------------------------------------------------------------

// TestAOS249_VaultAddrSchemeAborts prova que uma URL de custódia com transporte inseguro ABORTA o
// arranque, e que loopback/https continuam a passar. Sem isto, o token que destrói chaves de
// titulares atravessava a rede em claro.
func TestAOS249_VaultAddrSchemeAborts(t *testing.T) {
	dir := t.TempDir()
	tok := filepath.Join(dir, "vault-token")
	if err := os.WriteFile(tok, []byte("s.token-de-teste"), 0o600); err != nil {
		t.Fatalf("escrever token de teste: %v", err)
	}

	casos := []struct {
		addr   string
		aborta bool
	}{
		{"https://vault.internal:8200", false}, // https ⇒ aceite
		{"http://127.0.0.1:8200", false},       // loopback em claro ⇒ aceite (dev)
		{"http://localhost:8200", false},       // loopback nominal ⇒ aceite
		{"http://[::1]:8200", false},           // loopback IPv6 ⇒ aceite
		{"http://vault:8200", true},            // http em claro para a rede ⇒ ABORTA
		{"http://10.0.0.5:8200", true},         // idem, por IP
		{"vault:8200", true},                   // sem esquema ⇒ ABORTA
		{"ftp://vault:8200", true},             // esquema não-HTTP ⇒ ABORTA
	}
	for _, c := range casos {
		t.Setenv("AOS_DSAR_VAULT_ADDR", c.addr)
		t.Setenv("AOS_DSAR_VAULT_TOKEN_PATH", tok)
		t.Setenv("AOS_DSAR_VAULT_TOKEN_MIN_TTL", "")
		v, err := parseVaultDSARFromEnv()
		if c.aborta {
			if !errors.Is(err, ErrInsecureVaultDSARAddr) {
				t.Fatalf("addr=%q devia ABORTAR com ErrInsecureVaultDSARAddr, veio (%v, %v)", c.addr, v, err)
			}
			if v != nil {
				t.Fatalf("addr=%q abortou mas devolveu uma custodia utilizavel — nao pode degradar", c.addr)
			}
			continue
		}
		if err != nil || v == nil {
			t.Fatalf("addr=%q devia ser aceite, veio (%v, %v)", c.addr, v, err)
		}
	}
}

// TestAOS249_VaultAddrCriterionMatchesAttestationTwin prova que o critério é O MESMO do gémeo de
// attestation no mesmo binário — o ponto do achado F6 não era "falta uma validação" mas "o mesmo
// binário recusa ali o que aceita aqui". A guarda falha se alguém enfraquecer um dos lados: para
// cada URL, o veredicto da custódia e o do verificador de attestation têm de coincidir.
func TestAOS249_VaultAddrCriterionMatchesAttestationTwin(t *testing.T) {
	dir := t.TempDir()
	tok := filepath.Join(dir, "vault-token")
	if err := os.WriteFile(tok, []byte("s.token-de-teste"), 0o600); err != nil {
		t.Fatalf("escrever token de teste: %v", err)
	}
	urls := []string{
		"https://x.example:8200", "http://127.0.0.1:8200", "http://localhost:8200",
		"http://x.example:8200", "http://10.0.0.5:8200", "ftp://x.example", "x.example:8200",
	}
	for _, u := range urls {
		_, gemeoErr := integration.NewRemoteDeviceAttestationVerifier(integration.RemoteAttestationConfig{URL: u})
		gemeoRecusa := gemeoErr != nil

		t.Setenv("AOS_DSAR_VAULT_ADDR", u)
		t.Setenv("AOS_DSAR_VAULT_TOKEN_PATH", tok)
		_, custodiaErr := parseVaultDSARFromEnv()
		custodiaRecusa := errors.Is(custodiaErr, ErrInsecureVaultDSARAddr)

		if gemeoRecusa != custodiaRecusa {
			t.Fatalf("DIVERGENCIA de criterio em %q: attestation recusa=%v, custodia da KEK recusa=%v (%v) — e exactamente o defeito F6",
				u, gemeoRecusa, custodiaRecusa, custodiaErr)
		}
	}
}

// TestAOS249_BadMinTTLAborts prova que uma margem malformada aborta em vez de degradar em
// silêncio para o default (o operador julgaria ter uma janela de aviso que não tem).
func TestAOS249_BadMinTTLAborts(t *testing.T) {
	dir := t.TempDir()
	tok := filepath.Join(dir, "vault-token")
	if err := os.WriteFile(tok, []byte("s.token"), 0o600); err != nil {
		t.Fatalf("escrever token: %v", err)
	}
	for _, mau := range []string{"cinco minutos", "0", "-1m"} {
		t.Setenv("AOS_DSAR_VAULT_ADDR", "https://vault.internal:8200")
		t.Setenv("AOS_DSAR_VAULT_TOKEN_PATH", tok)
		t.Setenv("AOS_DSAR_VAULT_TOKEN_MIN_TTL", mau)
		if _, err := parseVaultDSARFromEnv(); !errors.Is(err, ErrBadVaultTokenMinTTL) {
			t.Fatalf("AOS_DSAR_VAULT_TOKEN_MIN_TTL=%q devia abortar com ErrBadVaultTokenMinTTL, veio %v", mau, err)
		}
	}
}

// ---------------------------------------------------------------------------
// AC2/AC3 — a expiração do token NÃO passa em silêncio
// ---------------------------------------------------------------------------

// TestAOS249_ExpiredTokenTurnsReadinessRed é o teste central do ticket: com o Vault SAUDÁVEL e
// DESTRAVADO (a sonda antiga responderia 200 e o nó ficaria verde), um token que o Vault já não
// aceita torna a prontidão VERMELHA. É a diferença entre sondar o vizinho e sondar a nossa
// capacidade de o usar.
func TestAOS249_ExpiredTokenTurnsReadinessRed(t *testing.T) {
	fv := novoFakeVault()
	fv.aceite = "token-fresco"
	srv := httptest.NewServer(fv)
	defer srv.Close()

	// CONTROLO: com o token que o Vault aceita, a sonda está VERDE — o teste não é vacuoso.
	bom := newVaultKeyVault(srv.URL, "transit", "token-fresco")
	if err := bom.ready(context.Background()); err != nil {
		t.Fatalf("token valido devia dar prontidao VERDE, veio: %v", err)
	}

	// O caso: o MESMO Vault, o MESMO seal-status verde, um token expirado.
	mau := newVaultKeyVault(srv.URL, "transit", "token-expirado")
	err := mau.ready(context.Background())
	if !errors.Is(err, ErrVaultToken) {
		t.Fatalf("token expirado devia dar prontidao VERMELHA (ErrVaultToken), veio: %v", err)
	}
	// E o valor do token NUNCA aparece no que o operador lê.
	if strings.Contains(err.Error(), "token-expirado") {
		t.Fatalf("a mensagem de erro EXPOE o valor do token: %q", err)
	}
}

// TestAOS249_ReadinessGoesRedBEFOREExpiry prova a parte que o critério de aceitação sublinha: o
// vermelho chega ANTES da expiração, não depois. Um token ainda PERFEITAMENTE VÁLIDO (lookup-self
// devolve 200) mas com TTL abaixo da margem já põe o nó unready — é essa antecedência que dá ao
// orquestrador tempo para drenar em vez de descobrir o problema pelo primeiro 403.
func TestAOS249_ReadinessGoesRedBEFOREExpiry(t *testing.T) {
	fv := novoFakeVault()
	fv.renovavel = false // sem renovação possível: o TTL só desce
	fv.ttl = 60          // um minuto — o token AINDA SERVE
	srv := httptest.NewServer(fv)
	defer srv.Close()

	v := newVaultKeyVault(srv.URL, "transit", "tok", withVaultTokenMinTTL(5*time.Minute))
	err := v.ready(context.Background())
	if !errors.Is(err, ErrVaultToken) {
		t.Fatalf("TTL=60s abaixo da margem de 5m devia dar VERMELHO ANTES da expiracao, veio: %v", err)
	}
	// Contraprova: com margem mais curta que o TTL restante, o MESMO estado é verde — a decisão
	// vem da margem, não de um vermelho constante.
	folgado := newVaultKeyVault(srv.URL, "transit", "tok", withVaultTokenMinTTL(10*time.Second))
	if err := folgado.ready(context.Background()); err != nil {
		t.Fatalf("TTL=60s com margem de 10s devia estar VERDE, veio: %v", err)
	}
}

// TestAOS249_EraseAndExpireFailClosedOnExpiredToken prova a outra metade do critério: no estado
// de token expirado, a via GDPR falha FECHADA em vez de se dar por cumprida.
//
//   - `WrapDEK`/`EnsureKey` (ingestão de PII) propagam ErrVaultKEK — nada é escrito com uma KEK
//     que não existe;
//   - `Delete` — o funil ÚNICO por onde passam tanto o `/dsar/erase` como a expiração de
//     retenção (audit.pipeline chama `vault.Delete`) — deixa de se dar por feito: a destruição é
//     VERIFICADA e, não podendo ser confirmada, a prontidão fica vermelha com
//     ErrVaultShredUnconfirmed. Antes, os três pedidos levavam 403 e o nó afirmava ao titular uma
//     irrecuperabilidade que não tinha acontecido.
func TestAOS249_EraseAndExpireFailClosedOnExpiredToken(t *testing.T) {
	fv := novoFakeVault()
	fv.aceite = "token-fresco"
	srv := httptest.NewServer(fv)
	defer srv.Close()

	v := newVaultKeyVault(srv.URL, "transit", "token-expirado")

	if _, _, err := v.EnsureKey("human:alice"); !errors.Is(err, ErrVaultKEK) {
		t.Fatalf("EnsureKey com token expirado devia falhar FECHADO (ErrVaultKEK), veio: %v", err)
	}
	if _, _, err := v.WrapDEK("human:alice", []byte("0123456789abcdef0123456789abcdef")); !errors.Is(err, ErrVaultKEK) {
		t.Fatalf("WrapDEK com token expirado devia falhar FECHADO (ErrVaultKEK), veio: %v", err)
	}

	// Erase/expire: a porta audit.KeyVault não devolve erro, mas a custódia deixa de fingir.
	v.Delete("human:alice")
	if err := v.shredFault(); !errors.Is(err, ErrVaultShredUnconfirmed) {
		t.Fatalf("crypto-shred impossivel (403) devia ficar POR CONFIRMAR, veio: %v", err)
	}

	// E fica vermelho mesmo depois de a credencial ser reposta: uma destruição por confirmar é
	// uma afirmação falsa perante o titular, não um soluço transitório.
	fv.defineAceite("")
	if err := v.ready(context.Background()); !errors.Is(err, ErrVaultShredUnconfirmed) {
		t.Fatalf("com uma destruicao por confirmar a prontidao devia continuar VERMELHA, veio: %v", err)
	}
	// Remediação: uma destruição CONFIRMADA da mesma chave limpa a pendência (o operador
	// recupera sem reiniciar o nó) — prova que a guarda não é um beco sem saída.
	v.Delete("human:alice")
	if err := v.ready(context.Background()); err != nil {
		t.Fatalf("apos a destruicao CONFIRMADA o no devia voltar a VERDE, veio: %v", err)
	}
}

// TestAOS249_UnconfirmedShredIsCaughtEvenWithValidToken prova que a verificação não é só um
// efeito colateral do 403: com credencial válida e o Vault a responder 204 ao DELETE mas a chave
// a SOBREVIVER (política, replicação, bug do motor), a pendência dispara na mesma. É a diferença
// entre "o pedido foi aceite" e "a chave morreu".
func TestAOS249_UnconfirmedShredIsCaughtEvenWithValidToken(t *testing.T) {
	fv := novoFakeVault()
	fv.shredFalha = true
	srv := httptest.NewServer(fv)
	defer srv.Close()

	v := newVaultKeyVault(srv.URL, "transit", "tok")
	if _, _, err := v.EnsureKey("human:bob"); err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	v.Delete("human:bob")
	if err := v.ready(context.Background()); !errors.Is(err, ErrVaultShredUnconfirmed) {
		t.Fatalf("DELETE aceite mas chave VIVA devia dar ErrVaultShredUnconfirmed, veio: %v", err)
	}
}

// TestAOS249_ReadyzTurns503OnExpiredToken leva a propriedade até à FRONTEIRA que o orquestrador
// observa: não basta a sonda interna dizer não, o `/readyz` tem de responder 503. É o mesmo
// caminho de [readinessProber] que a revisão de prontidão #2 abriu — AOS-249 só lhe acrescenta a
// pergunta autenticada.
func TestAOS249_ReadyzTurns503OnExpiredToken(t *testing.T) {
	fv := novoFakeVault()
	fv.aceite = "token-fresco"
	srv := httptest.NewServer(fv)
	defer srv.Close()

	// UM NÓ POR CASO, e a custódia escolhida ANTES de o serviço ser composto. `Node.DSARVault` é
	// fixado pelo Bootstrap e a partir daí só LIDO — e lido por goroutines de FUNDO: a manutenção
	// do token deste ticket ([NodeService.renewVaultToken], que resolve a porta no arranque da
	// goroutine) e o avaliador de SLOs ([NodeService.controlPlaneAvailable], a cada passagem).
	// Reatribuir o campo sobre um nó JÁ COMPOSTO seria uma escrita a correr contra essas leituras
	// — data race a valer, apanhada pelo -race — e nem sequer é o que produção faz: um nó arranca
	// com UMA custódia e morre com ela.
	readyz := func(t *testing.T, token string) (int, map[string]string) {
		t.Helper()
		node, _ := newAPINode(t, &countingModel{}, false)
		defer func() { _ = node.Close() }()
		node.DSARVault = newVaultKeyVault(srv.URL, "transit", token)
		svc, h := newAPI(t, node)
		defer func() { _ = svc.Shutdown(context.Background()) }()
		return getProbe(h, "/readyz")
	}

	// Custódia com token VÁLIDO ⇒ 200 (controlo: o teste não é vacuoso).
	if code, body := readyz(t, "token-fresco"); code != http.StatusOK {
		t.Fatalf("/readyz com token valido devia dar 200, veio %d (%v)", code, body)
	}

	// O MESMO Vault saudável, token expirado ⇒ 503.
	code, body := readyz(t, "token-expirado")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz com token do Vault EXPIRADO devia dar 503, veio %d (%v)", code, body)
	}
	// O corpo continua mínimo e sem detalhe interno (filosofia não-enumerável do handler).
	if body["status"] != "unready" {
		t.Fatalf("/readyz 503 devia trazer status=unready, veio %q", body["status"])
	}
}

// ---------------------------------------------------------------------------
// A metade que EVITA a expiração: renovação e adopção do token rodado
// ---------------------------------------------------------------------------

// TestAOS249_TokenIsRenewedBeforeMargin prova que a manutenção renova (`renew-self`) quando o TTL
// entra no dobro da margem — e que só então: renovar a cada tick um token com dias de validade
// seria ruído contra o Vault.
func TestAOS249_TokenIsRenewedBeforeMargin(t *testing.T) {
	fv := novoFakeVault()
	fv.ttl = 3600 // uma hora: muito acima do limiar
	fv.ttlAposRenew = 3600
	srv := httptest.NewServer(fv)
	defer srv.Close()

	v := newVaultKeyVault(srv.URL, "transit", "tok", withVaultTokenMinTTL(time.Minute))
	if err := v.refreshToken(context.Background()); err != nil {
		t.Fatalf("manutencao com TTL folgado nao devia falhar: %v", err)
	}
	if n := fv.contaRenovacoes(); n != 0 {
		t.Fatalf("TTL folgado NAO devia ser renovado, houve %d renovacoes", n)
	}

	// O TTL desce para dentro do limiar (2x a margem = 2 min) ⇒ a manutenção renova.
	fv.defineTTL(30)
	if err := v.refreshToken(context.Background()); err != nil {
		t.Fatalf("manutencao devia recuperar o token renovando-o, veio: %v", err)
	}
	if n := fv.contaRenovacoes(); n != 1 {
		t.Fatalf("TTL dentro do limiar devia disparar UMA renovacao, houve %d", n)
	}
	// E o efeito da renovação é o que interessa: a prontidão volta a VERDE.
	if err := v.ready(context.Background()); err != nil {
		t.Fatalf("apos a renovacao o no devia estar PRONTO, veio: %v", err)
	}
}

// TestAOS249_RotatedTokenFileIsAdopted prova a via de produção recomendada: um agente externo
// (AppRole/Kubernetes-auth) reescreve o ficheiro montado e o nó ADOPTA o token novo sem
// reiniciar — sem uma dependência nova, só re-lendo o caminho que já conhecia.
func TestAOS249_RotatedTokenFileIsAdopted(t *testing.T) {
	fv := novoFakeVault()
	fv.aceite = "token-v2" // o Vault já só aceita o token NOVO
	srv := httptest.NewServer(fv)
	defer srv.Close()

	caminho := filepath.Join(t.TempDir(), "vault-token")
	if err := os.WriteFile(caminho, []byte("token-v1\n"), 0o600); err != nil {
		t.Fatalf("escrever token: %v", err)
	}
	v := newVaultKeyVault(srv.URL, "transit", "token-v1", withVaultTokenFile(caminho))

	if err := v.ready(context.Background()); !errors.Is(err, ErrVaultToken) {
		t.Fatalf("com o token antigo o no devia estar VERMELHO, veio: %v", err)
	}
	// O agente externo roda o token no MESMO caminho.
	if err := os.WriteFile(caminho, []byte("token-v2\n"), 0o600); err != nil {
		t.Fatalf("rodar token: %v", err)
	}
	if err := v.refreshToken(context.Background()); err != nil {
		t.Fatalf("a manutencao devia ADOPTAR o token rodado, veio: %v", err)
	}
	if err := v.ready(context.Background()); err != nil {
		t.Fatalf("apos adoptar o token rodado o no devia estar PRONTO, veio: %v", err)
	}
	// A adopção também repõe a via de escrita: com a credencial certa, a KEK volta a embrulhar.
	if _, _, err := v.EnsureKey("human:carol"); err != nil {
		t.Fatalf("apos a adopcao a custodia devia voltar a funcionar, veio: %v", err)
	}
}

// TestAOS249_MissingTokenFileIsLoudNotSilent prova que o ficheiro do token desaparecer/esvaziar-se
// é um estado RUIDOSO (prontidão vermelha), não um nó que continua verde com uma credencial que já
// não existe no disco — e que a mensagem nomeia o CAMINHO, nunca o valor.
func TestAOS249_MissingTokenFileIsLoudNotSilent(t *testing.T) {
	fv := novoFakeVault()
	srv := httptest.NewServer(fv)
	defer srv.Close()

	caminho := filepath.Join(t.TempDir(), "vault-token")
	if err := os.WriteFile(caminho, []byte("s.segredo-que-nao-pode-vazar"), 0o600); err != nil {
		t.Fatalf("escrever token: %v", err)
	}
	v := newVaultKeyVault(srv.URL, "transit", "s.segredo-que-nao-pode-vazar", withVaultTokenFile(caminho))
	if err := v.ready(context.Background()); err != nil {
		t.Fatalf("estado nominal devia estar VERDE, veio: %v", err)
	}
	if err := os.Remove(caminho); err != nil {
		t.Fatalf("remover o ficheiro do token: %v", err)
	}
	err := v.refreshToken(context.Background())
	if !errors.Is(err, ErrVaultToken) {
		t.Fatalf("ficheiro do token desaparecido devia dar ErrVaultToken, veio: %v", err)
	}
	if !strings.Contains(err.Error(), caminho) {
		t.Fatalf("a mensagem devia nomear o CAMINHO do ficheiro para o operador agir: %q", err)
	}
	if strings.Contains(err.Error(), "s.segredo-que-nao-pode-vazar") {
		t.Fatalf("a mensagem EXPOE o valor do token: %q", err)
	}
	if rerr := v.ready(context.Background()); !errors.Is(rerr, ErrVaultToken) {
		t.Fatalf("com o ficheiro do token desaparecido a prontidao devia ser VERMELHA, veio: %v", rerr)
	}
}

// TestAOS249_UncheckedTokenIsNotAGoodToken prova o fail-closed do estado inicial: uma custódia
// cuja validade NUNCA foi verificada não conta como servível. É o que impede que um Vault que
// nunca respondeu ao lookup-self passe por "sem problemas detectados".
func TestAOS249_UncheckedTokenIsNotAGoodToken(t *testing.T) {
	v := newVaultKeyVault("https://vault.invalido.example:8200", "transit", "tok")
	if err := v.tokenVerdict(time.Now()); !errors.Is(err, ErrVaultToken) {
		t.Fatalf("token por verificar devia ser NAO-SERVIVEL (fail-closed), veio: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Guarda de LIGAÇÃO: o renovador tem chamador de produção
// ---------------------------------------------------------------------------

// TestAOS249_TokenRenewerHasProductionCaller falha se o laço de manutenção voltar a ficar sem
// chamador no loop de serviço — o defeito seria indistinguível do original (código que existe e
// nunca corre). Molde de [TestAOS251_ObserveActionHasProductionCaller]: varredura SINTÁCTICA dos
// ficheiros não-teste, com não-vacuosidade garantida exigindo também a DECLARAÇÃO.
func TestAOS249_TokenRenewerHasProductionCaller(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir do pacote do nó: %v", err)
	}
	fset := token.NewFileSet()
	referencias, declarado := 0, false
	for _, e := range entries {
		nome := e.Name()
		if e.IsDir() || !strings.HasSuffix(nome, ".go") || strings.HasSuffix(nome, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, nome, nil, 0)
		if perr != nil {
			t.Fatalf("parser.ParseFile(%q): %v", nome, perr)
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "renewVaultToken" {
				declarado = true
				continue // a declaração não é uma referência
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel != nil && sel.Sel.Name == "renewVaultToken" {
				referencias++
			}
			return true
		})
	}
	if !declarado {
		t.Fatal("renewVaultToken NAO esta declarado no pacote — a guarda ficaria cega (renomeacao?)")
	}
	if referencias == 0 {
		t.Fatal("renewVaultToken tem ZERO referencias de producao — o laco de manutencao do token nunca arrancaria e a custodia voltaria a morrer em silencio")
	}
}

// TestAOS249_RenewerIsWiredIntoServiceLoop prova a ligação a valer (não só sintacticamente): um
// serviço construído sobre um nó cuja custódia tem token executa a manutenção quando a conduzimos
// à mão, e o Shutdown pára o laço com o mesmo `sweepStop` dos outros varrimentos.
func TestAOS249_RenewerIsWiredIntoServiceLoop(t *testing.T) {
	fv := novoFakeVault()
	fv.ttl = 30 // dentro do limiar ⇒ a manutenção tem de renovar
	srv := httptest.NewServer(fv)
	defer srv.Close()

	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	node.DSARVault = newVaultKeyVault(srv.URL, "transit", "tok", withVaultTokenMinTTL(time.Minute))

	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute),
		WithVaultTokenRenewInterval(0)) // ticker desligado: conduzimos a manutenção à mão
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	defer func() { _ = svc.Shutdown(context.Background()) }()

	svc.RefreshVaultTokenNow(context.Background())
	if n := fv.contaRenovacoes(); n != 1 {
		t.Fatalf("a manutencao no loop de servico devia ter renovado o token UMA vez, houve %d", n)
	}

	// E a porta é a certa: o vault in-memory de referência NÃO tem token para manter, logo a
	// manutenção é no-op (nenhum laço a correr sobre uma custódia que não o entende). Num nó
	// PRÓPRIO, pela razão de [TestAOS249_ReadyzTurns503OnExpiredToken]: trocar a custódia debaixo
	// de um serviço vivo é uma escrita contra as leituras das goroutines de fundo.
	nodeMem, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = nodeMem.Close() }()
	nodeMem.DSARVault = audit.NewInMemoryKeyVault(nil)

	svcMem, err := NewNodeService(nodeMem, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute),
		WithVaultTokenRenewInterval(0))
	if err != nil {
		t.Fatalf("NewNodeService (custodia in-memory): %v", err)
	}
	defer func() { _ = svcMem.Shutdown(context.Background()) }()

	svcMem.RefreshVaultTokenNow(context.Background())
	if n := fv.contaRenovacoes(); n != 1 {
		t.Fatalf("custodia in-memory nao devia gerar renovacoes, passou de 1 para %d", n)
	}
}
