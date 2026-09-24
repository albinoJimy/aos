package main

// AOS-436 — O APAGAMENTO SOBREVIVE AO RESTAURO.
//
// Os testes montam o cenário que o defeito descrevia, com o nó REAL (Bootstrap) e um Vault falso
// que sabe a IDADE de cada chave — é essa a pergunta de que a reconciliação depende:
//
//	(a) Vault ANTIGO + WORM ACTUAL — a cadeia sabe do apagamento, a KEK voltou;
//	(b) TUDO ANTIGO                — a cadeia não sabe; só o registo importado sabe;
//
// e os CONTROLOS que impedem a solução barata («destrói tudo o que existir»): o titular que VOLTOU
// depois de apagado tem uma KEK nova e legítima que NÃO pode ser destruída.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------------------------
// Vault falso com IDADES
// ---------------------------------------------------------------------------------------------

// vaultComIdades é um Vault mínimo cujo motor Transit guarda, por chave, o instante de nascimento
// (segundos Unix) — o `data.keys` que o Vault real devolve. `restaurar` é o restauro de backup:
// repõe uma chave com a idade que ela tinha.
type vaultComIdades struct {
	mu           sync.Mutex
	chaves       map[string]int64 // nome → nascimento (segundos Unix)
	proxima      int64            // nascimento de uma chave criada por POST
	shredFalha   bool             // o DELETE responde OK mas a chave SOBREVIVE
	indisponivel bool             // GET de chaves responde 503 (Vault a arrancar)
}

func novoVaultComIdades() *vaultComIdades {
	return &vaultComIdades{chaves: map[string]int64{}, proxima: 1_900_000_000}
}

func (f *vaultComIdades) restaurar(nome string, nascida int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chaves[nome] = nascida
}

func (f *vaultComIdades) existe(nome string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.chaves[nome]
	return ok
}

func (f *vaultComIdades) define(fn func(*vaultComIdades)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *vaultComIdades) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/v1/sys/seal-status":
		fmt.Fprint(w, `{"sealed":false}`)
		return
	case "/v1/auth/token/lookup-self":
		// TTL 0 = token sem expiração: a prontidão do token não interfere nestes testes.
		fmt.Fprint(w, `{"data":{"ttl":0,"renewable":false}}`)
		return
	}
	resto := strings.TrimPrefix(r.URL.Path, "/v1/transit/")
	switch {
	case strings.HasPrefix(resto, "keys/") && strings.HasSuffix(resto, "/config"):
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(resto, "keys/"):
		nome := strings.TrimPrefix(resto, "keys/")
		switch r.Method {
		case http.MethodPost:
			if _, ok := f.chaves[nome]; !ok {
				f.chaves[nome] = f.proxima
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			if !f.shredFalha {
				delete(f.chaves, nome)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			if f.indisponivel {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			nasc, ok := f.chaves[nome]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"name": nome, "type": "aes256-gcm96", "keys": map[string]int64{"1": nasc},
			}})
		}
	case strings.HasPrefix(resto, "encrypt/"):
		var in struct {
			Plaintext string `json:"plaintext"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		writeData(w, "ciphertext", "vault:v1:"+in.Plaintext)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// ---------------------------------------------------------------------------------------------
// Utilitários
// ---------------------------------------------------------------------------------------------

// Instantes fixos. A destruição é em T; uma chave ressuscitada nasceu antes; uma geração nova
// nasceu depois.
var (
	instanteDaDestruicao = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	nascidaAntes         = instanteDaDestruicao.Add(-30 * 24 * time.Hour).Unix()
	nascidaDepois        = instanteDaDestruicao.Add(2 * time.Hour).Unix()
	// nascidaHaMuito serve os testes que correm o fluxo DSAR REAL, cujo selo leva o relógio de
	// parede: uma idade de 2001 é anterior a qualquer instante em que o teste possa correr.
	nascidaHaMuito = time.Date(2001, 9, 9, 1, 46, 40, 0, time.UTC).Unix()
)

// cadeiaComApagamento sela `dsar.received` + `dsar.key_destroyed` de um titular, no instante dado.
func cadeiaComApagamento(t *testing.T, store audit.Store, titular string, quando time.Time) {
	t.Helper()
	for _, verbo := range []string{dsar.EventReceived, dsar.EventKeyDestroyed} {
		if _, err := store.Append(context.Background(), audit.AuditRecord{
			Partition: "governance.dsar", Timestamp: quando, Decision: audit.DecisionAllow,
			Capability: verbo, RequestID: "req-aos436",
			Resource: audit.Resource{Type: subjectResourceType, Value: titular},
		}); err != nil {
			t.Fatalf("selar %s: %v", verbo, err)
		}
	}
}

// selosReshred devolve os `dsar.key_reshredded` da partição DSAR.
func selosReshred(t *testing.T, store audit.Store) []audit.AuditRecord {
	t.Helper()
	ctx := context.Background()
	head, err := store.Head(ctx, "governance.dsar")
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head == 0 {
		return nil
	}
	recs, err := store.Read(ctx, "governance.dsar", 1, head)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var out []audit.AuditRecord
	for _, r := range recs {
		if r.Capability == EventKeyReshredded {
			out = append(out, r)
		}
	}
	return out
}

// noComVault arranca o nó REAL sobre a cadeia e o Vault falso dados.
func noComVault(t *testing.T, store audit.Store, srv *httptest.Server, ajusta func(*Config)) (*Node, *vaultKeyVault) {
	t.Helper()
	vault := newVaultKeyVault(srv.URL, "transit", "tok")
	cfg := tnBaseConfig()
	cfg.WORM = store
	cfg.DSARVault = vault
	if ajusta != nil {
		ajusta(&cfg)
	}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node, vault
}

func escreveRegisto(t *testing.T, caminho string, linhas ...string) {
	t.Helper()
	if err := os.WriteFile(caminho, []byte(strings.Join(linhas, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------------------------
// (a) Vault ANTIGO + WORM ACTUAL
// ---------------------------------------------------------------------------------------------

// TestAOS436_VaultAntigoComWORMActualReDestroiAKEK é o sabor (a) do defeito: a cadeia afirma o
// apagamento, o restauro do Vault trouxe a KEK de volta — e ANTES de AOS-436 nada o via.
func TestAOS436_VaultAntigoComWORMActualReDestroiAKEK(t *testing.T) {
	const titular = "nhi:apagado-a"
	nome := nomeDeApagamento(titular)
	store := audit.NewMemStore()
	cadeiaComApagamento(t, store, titular, instanteDaDestruicao)

	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaAntes) // o restauro do vault-data antigo
	srv := httptest.NewServer(fv)
	defer srv.Close()

	_, vault := noComVault(t, store, srv, nil)

	if fv.existe(nome) {
		t.Fatal("a KEK ressuscitada pelo restauro CONTINUA viva depois do arranque — o conteudo do titular apagado volta a decifrar")
	}
	selos := selosReshred(t, store)
	if len(selos) != 1 {
		t.Fatalf("esperava 1 dsar.key_reshredded selado, vieram %d", len(selos))
	}
	s := selos[0]
	if s.Resource.Type != subjectResourceType || s.Resource.Value != titular {
		t.Errorf("o selo devia nomear o titular que a cadeia ja nomeava; veio %+v", s.Resource)
	}
	if s.Principal.NHIID != reconciliacaoNHI {
		t.Errorf("o re-apagamento e em nome PROPRIO do no; veio principal %q", s.Principal.NHIID)
	}
	if len(s.Obligations) != 1 || s.Obligations[0].Params["kek"] != nome ||
		s.Obligations[0].Params["destruida_em"] != instanteDaDestruicao.Format(time.RFC3339) {
		t.Errorf("o selo devia levar a chave e o instante da destruicao ORIGINAL; veio %+v", s.Obligations)
	}
	if err := vault.ready(context.Background()); err != nil {
		t.Errorf("com a reconciliacao provada a custodia devia estar pronta; veio %v", err)
	}
}

// TestAOS436_TitularQueVoltouNaoEDestruido é o CONTROLO que proíbe a solução barata.
//
// O titular foi apagado e voltou: a KEK que existe hoje nasceu DEPOIS da destruição e cifra
// dados novos e legítimos. «A cadeia diz destruída e a chave existe» é verdade aqui também — e
// destruí-la seria apagar sem pedido. Sem este teste, um reconciliador que destruísse por
// existência passaria no teste acima.
func TestAOS436_TitularQueVoltouNaoEDestruido(t *testing.T) {
	const titular = "nhi:voltou"
	nome := nomeDeApagamento(titular)
	store := audit.NewMemStore()
	cadeiaComApagamento(t, store, titular, instanteDaDestruicao)

	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaDepois) // geração NOVA, de depois do apagamento
	srv := httptest.NewServer(fv)
	defer srv.Close()

	_, vault := noComVault(t, store, srv, nil)

	if !fv.existe(nome) {
		t.Fatal("a reconciliacao DESTRUIU a KEK nova de um titular que voltou depois de apagado — dados legitimos perdidos sem pedido")
	}
	if n := len(selosReshred(t, store)); n != 0 {
		t.Errorf("nenhum re-apagamento devia ter sido selado; vieram %d", n)
	}
	if err := vault.ready(context.Background()); err != nil {
		t.Errorf("uma geracao nova e o caso legitimo — a custodia devia estar pronta; veio %v", err)
	}
}

// TestAOS436_OMesmoSegundoContaComoADestruida fixa a regra da fronteira: o Vault data ao
// segundo, e uma chave nascida no MESMO segundo da destruição é tratada como a destruída — a
// ambiguidade resolve-se pelo lado do apagamento (resíduo declarado).
func TestAOS436_OMesmoSegundoContaComoADestruida(t *testing.T) {
	const titular = "nhi:mesmo-segundo"
	nome := nomeDeApagamento(titular)
	store := audit.NewMemStore()
	cadeiaComApagamento(t, store, titular, instanteDaDestruicao.Add(700*time.Millisecond))

	fv := novoVaultComIdades()
	fv.restaurar(nome, instanteDaDestruicao.Unix())
	srv := httptest.NewServer(fv)
	defer srv.Close()

	noComVault(t, store, srv, nil)
	if fv.existe(nome) {
		t.Fatal("uma KEK nascida no mesmo segundo da destruicao devia ser tratada como a destruida")
	}
}

// ---------------------------------------------------------------------------------------------
// (b) TUDO ANTIGO — o registo importado
// ---------------------------------------------------------------------------------------------

// TestAOS436_TudoAntigoComRegistoImportado é o sabor (b): a cadeia restaurada é anterior ao
// apagamento e não sabe dele. Só o registo, trazido de fora do bundle, sabe — e uma entrada do
// registo SEM facto na cadeia também é re-destruída e selada.
func TestAOS436_TudoAntigoComRegistoImportado(t *testing.T) {
	const titular = "nhi:apagado-b"
	nome := nomeDeApagamento(titular)
	store := audit.NewMemStore() // a cadeia do backup antigo: nada sobre o apagamento

	dir := t.TempDir()
	importado := filepath.Join(dir, "apagamentos-importado.txt")
	proprio := filepath.Join(dir, "apagamentos-dsar.txt")
	escreveRegisto(t, importado, "# recolhido do servidor", nome+" "+instanteDaDestruicao.Format(time.RFC3339))

	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaAntes)
	srv := httptest.NewServer(fv)
	defer srv.Close()

	_, vault := noComVault(t, store, srv, func(c *Config) {
		c.DSARErasureRegister = proprio
		c.DSARErasureRegisterImport = importado
	})

	if fv.existe(nome) {
		t.Fatal("restauro de TUDO antigo: a KEK voltou e o registo importado nao a fez destruir")
	}
	selos := selosReshred(t, store)
	if len(selos) != 1 || selos[0].Resource.Type != kekResourceType || selos[0].Resource.Value != nome {
		t.Fatalf("esperava 1 selo que nomeia a CHAVE (o registo nunca teve o titular); veio %+v", selos)
	}
	if selos[0].Obligations[0].Params["origem"] != "importado" {
		t.Errorf("a origem do alvo devia ser o registo importado; veio %q", selos[0].Obligations[0].Params["origem"])
	}
	// O REGISTO PRÓPRIO PASSOU A SER SUPERCONJUNTO — é isto que o próximo backup leva.
	lido, err := lerRegistoDeApagamentos(proprio, false)
	if err != nil {
		t.Fatalf("o registo proprio devia existir depois da fusao: %v", err)
	}
	if !lido[nome].Equal(instanteDaDestruicao) {
		t.Errorf("a entrada importada devia ter sido fundida no registo proprio; veio %v", lido)
	}
	if err := vault.ready(context.Background()); err != nil {
		t.Errorf("reconciliacao provada ⇒ custodia pronta; veio %v", err)
	}
}

// TestAOS436_ImportacaoPedidaEAusenteFicaPorProvar — um restauro que PEDIU uma importação e não a
// tem não se dá por reconciliado.
func TestAOS436_ImportacaoPedidaEAusenteFicaPorProvar(t *testing.T) {
	fv := novoVaultComIdades()
	srv := httptest.NewServer(fv)
	defer srv.Close()
	_, vault := noComVault(t, audit.NewMemStore(), srv, func(c *Config) {
		c.DSARErasureRegisterImport = filepath.Join(t.TempDir(), "nao-existe.txt")
	})
	if err := vault.ready(context.Background()); !errors.Is(err, ErrRegistoDeApagamentos) {
		t.Fatalf("importacao pedida e ausente devia deixar a custodia VERMELHA; veio %v", err)
	}
}

// TestAOS436_RegistoMalformadoERecusado — a leitura é estrita. Uma linha saltada seria um
// apagamento esquecido em silêncio; e um nome fora da forma exacta chegaria ao caminho HTTP do
// Vault.
func TestAOS436_RegistoMalformadoERecusado(t *testing.T) {
	dir := t.TempDir()
	valido := nomeDeApagamento("nhi:x") + " " + instanteDaDestruicao.Format(time.RFC3339)
	casos := map[string]string{
		"traversal":         "aos-kek-../../sys/seal " + instanteDaDestruicao.Format(time.RFC3339),
		"sem instante":      nomeDeApagamento("nhi:x"),
		"instante ilegivel": nomeDeApagamento("nhi:x") + " ontem",
		"hex curto":         "aos-kek-abc " + instanteDaDestruicao.Format(time.RFC3339),
	}
	for nomeCaso, linha := range casos {
		p := filepath.Join(dir, strings.ReplaceAll(nomeCaso, " ", "-"))
		escreveRegisto(t, p, valido, linha)
		if _, err := lerRegistoDeApagamentos(p, false); !errors.Is(err, ErrRegistoDeApagamentos) || !strings.Contains(err.Error(), ":2:") {
			t.Errorf("%s: esperava recusa com o numero da linha (2); veio %v", nomeCaso, err)
		}
	}
	// CONTROLO: o registo válido lê-se, e a repetição fica com o instante MAIS RECENTE.
	p := filepath.Join(dir, "ok")
	depois := instanteDaDestruicao.Add(time.Hour).Format(time.RFC3339)
	escreveRegisto(t, p, "# comentario", "", valido, nomeDeApagamento("nhi:x")+" "+depois)
	lido, err := lerRegistoDeApagamentos(p, false)
	if err != nil || lido[nomeDeApagamento("nhi:x")].Format(time.RFC3339) != depois {
		t.Fatalf("registo valido devia ler-se com o instante mais recente; veio %v %v", lido, err)
	}
}

// ---------------------------------------------------------------------------------------------
// FAIL-CLOSED: a custódia que não responde e a chave que não morre
// ---------------------------------------------------------------------------------------------

// TestAOS436_CustodiaQueNaoRespondeDeixaONoUnready — o Vault ainda a arrancar quando o nó
// arranca. O nó não se diz pronto; o laço de manutenção retoma e prova.
func TestAOS436_CustodiaQueNaoRespondeDeixaONoUnready(t *testing.T) {
	const titular = "nhi:vault-lento"
	nome := nomeDeApagamento(titular)
	store := audit.NewMemStore()
	cadeiaComApagamento(t, store, titular, instanteDaDestruicao)

	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaAntes)
	fv.define(func(f *vaultComIdades) { f.indisponivel = true })
	srv := httptest.NewServer(fv)
	defer srv.Close()

	node, vault := noComVault(t, store, srv, nil)
	svc, h := newAPI(t, node)

	if err := vault.ready(context.Background()); !errors.Is(err, ErrApagamentoPorReconciliar) {
		t.Fatalf("custodia sem resposta devia deixar a reconciliacao POR PROVAR; veio %v", err)
	}
	// O /readyz e a série de causa seguem-na — é o que o orquestrador e o painel lêem.
	if code, _ := getProbe(h, "/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz devia ser 503 com os apagamentos por reconciliar; veio %d", code)
	}
	if v, ok := leGauge(t, h, "aos_dsar_erasure_reconciled"); !ok || v != 0 {
		t.Errorf("aos_dsar_erasure_reconciled devia ler 0; veio %v (presente=%v)", v, ok)
	}

	// O Vault acorda; a manutenção da custódia retoma a reconciliação.
	fv.define(func(f *vaultComIdades) { f.indisponivel = false })
	svc.RefreshVaultTokenNow(context.Background())

	if fv.existe(nome) {
		t.Fatal("a re-tentativa pelo laco de manutencao nao destruiu a KEK ressuscitada")
	}
	if code, _ := getProbe(h, "/readyz"); code != http.StatusOK {
		t.Errorf("depois de provada, o /readyz devia voltar a 200; veio %d", code)
	}
	if v, _ := leGauge(t, h, "aos_dsar_erasure_reconciled"); v != 1 {
		t.Errorf("aos_dsar_erasure_reconciled devia voltar a 1; veio %v", v)
	}
}

// TestAOS436_KEKQueNaoSeDeixaDestruirNaoESelada — a re-destruição que o Vault não confirma não
// fica afirmada na cadeia, e o nó fica UNREADY.
func TestAOS436_KEKQueNaoSeDeixaDestruirNaoESelada(t *testing.T) {
	const titular = "nhi:teimosa"
	nome := nomeDeApagamento(titular)
	store := audit.NewMemStore()
	cadeiaComApagamento(t, store, titular, instanteDaDestruicao)

	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaAntes)
	fv.define(func(f *vaultComIdades) { f.shredFalha = true })
	srv := httptest.NewServer(fv)
	defer srv.Close()

	_, vault := noComVault(t, store, srv, nil)

	if n := len(selosReshred(t, store)); n != 0 {
		t.Errorf("uma re-destruicao NAO confirmada foi selada como feita (%d selo(s)) — a cadeia afirma o que nao aconteceu", n)
	}
	if err := vault.ready(context.Background()); !errors.Is(err, ErrApagamentoPorReconciliar) {
		t.Fatalf("a KEK sobreviveu a re-destruicao: a custodia devia estar VERMELHA; veio %v", err)
	}
}

// ---------------------------------------------------------------------------------------------
// O CICLO COMPLETO: apagar, fazer backup, restaurar
// ---------------------------------------------------------------------------------------------

// TestAOS436_CicloCompletoApagarERestaurar corre o fluxo DSAR REAL do nó e depois os dois
// restauros, sem fabricar a cadeia nem o registo à mão:
//
//  1. nó 1 apaga o titular por `node.DSAR.Receive` — o `Delete` confirmado escreve o registo;
//  2. restauro (a): um nó novo sobre o MESMO WORM e o Vault restaurado — re-destrói;
//  3. restauro (b): um nó novo sobre uma cadeia VAZIA (backup antigo) com o registo do nó 1
//     importado — re-destrói.
func TestAOS436_CicloCompletoApagarERestaurar(t *testing.T) {
	const titular = "nhi:ciclo"
	nome := nomeDeApagamento(titular)
	dir := t.TempDir()
	registo1 := filepath.Join(dir, "no1", "apagamentos-dsar.txt")
	if err := os.MkdirAll(filepath.Dir(registo1), 0o700); err != nil {
		t.Fatal(err)
	}

	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaHaMuito) // a KEK do titular, viva antes do pedido
	srv := httptest.NewServer(fv)
	defer srv.Close()

	// (1) O APAGAMENTO, pelo fluxo composto pelo nó.
	worm := audit.NewMemStore()
	node1, _ := noComVault(t, worm, srv, func(c *Config) { c.DSARErasureRegister = registo1 })
	if _, err := node1.DSAR.Receive(context.Background(), dsar.Request{
		RequestID: "req-ciclo", SubjectID: titular, Principal: "nhi:operador",
	}); err != nil {
		t.Fatalf("erase: %v", err)
	}
	if fv.existe(nome) {
		t.Fatal("PRECONDICAO: o erase nao destruiu a KEK")
	}
	lido, err := lerRegistoDeApagamentos(registo1, false)
	if err != nil || lido[nome].IsZero() {
		t.Fatalf("o Delete CONFIRMADO devia ter escrito a chave no registo; veio %v %v", lido, err)
	}

	// (2) RESTAURO (a): vault-data antigo, WORM actual.
	fv.restaurar(nome, nascidaHaMuito)
	noComVault(t, worm, srv, func(c *Config) { c.DSARErasureRegister = registo1 })
	if fv.existe(nome) {
		t.Fatal("restauro (a): a KEK ressuscitada sobreviveu ao arranque")
	}

	// (3) RESTAURO (b): tudo antigo — cadeia vazia, registo próprio vazio, registo importado.
	fv.restaurar(nome, nascidaHaMuito)
	registo2 := filepath.Join(dir, "no2", "apagamentos-dsar.txt")
	if err := os.MkdirAll(filepath.Dir(registo2), 0o700); err != nil {
		t.Fatal(err)
	}
	noComVault(t, audit.NewMemStore(), srv, func(c *Config) {
		c.DSARErasureRegister = registo2
		c.DSARErasureRegisterImport = registo1
	})
	if fv.existe(nome) {
		t.Fatal("restauro (b): a KEK ressuscitada sobreviveu ao arranque com o registo importado")
	}
}

// TestAOS436_DestruicaoPorConfirmarNaoEntraNoRegisto — o registo é do que MORREU. Uma linha sobre
// uma chave viva faria a reconciliação destruí-la num restauro futuro.
func TestAOS436_DestruicaoPorConfirmarNaoEntraNoRegisto(t *testing.T) {
	const titular = "nhi:nao-morreu"
	registo := filepath.Join(t.TempDir(), "apagamentos-dsar.txt")
	fv := novoVaultComIdades()
	fv.restaurar(nomeDeApagamento(titular), nascidaHaMuito)
	fv.define(func(f *vaultComIdades) { f.shredFalha = true })
	srv := httptest.NewServer(fv)
	defer srv.Close()

	node, _ := noComVault(t, audit.NewMemStore(), srv, func(c *Config) { c.DSARErasureRegister = registo })
	_, _ = node.DSAR.Receive(context.Background(), dsar.Request{RequestID: "req-x", SubjectID: titular, Principal: "nhi:operador"})

	lido, err := lerRegistoDeApagamentos(registo, true)
	if err != nil {
		t.Fatalf("ler registo: %v", err)
	}
	if len(lido) != 0 {
		t.Fatalf("uma destruicao POR CONFIRMAR entrou no registo: %v", lido)
	}
}

// TestAOS436_RegistoQueNaoSeEscreveDeixaONoUnready — a destruição confirmada que não chega ao
// disco fica pendente, e a custódia fica vermelha até ela chegar.
func TestAOS436_RegistoQueNaoSeEscreveDeixaONoUnready(t *testing.T) {
	dir := t.TempDir()
	registo := filepath.Join(dir, "sub", "apagamentos-dsar.txt") // o directório ainda não existe
	fv := novoVaultComIdades()
	srv := httptest.NewServer(fv)
	defer srv.Close()

	node, vault := noComVault(t, audit.NewMemStore(), srv, func(c *Config) { c.DSARErasureRegister = registo })
	fv.restaurar(nomeDeApagamento("nhi:z"), nascidaHaMuito)
	vault.Delete("nhi:z")

	if err := vault.ready(context.Background()); !errors.Is(err, ErrRegistoDeApagamentos) {
		t.Fatalf("registo por escrever devia deixar a custodia VERMELHA; veio %v", err)
	}
	// O directório aparece (o volume foi montado); a re-tentativa escreve a pendente.
	if err := os.MkdirAll(filepath.Dir(registo), 0o700); err != nil {
		t.Fatal(err)
	}
	node.apagamentos.retentarSeFalhou(context.Background(), func(string, ...any) {})
	if err := vault.ready(context.Background()); err != nil {
		t.Fatalf("depois de a pendente chegar ao disco a custodia devia estar pronta; veio %v", err)
	}
	if lido, _ := lerRegistoDeApagamentos(registo, false); lido[nomeDeApagamento("nhi:z")].IsZero() {
		t.Fatal("a entrada pendente nao chegou ao registo")
	}
}

// ---------------------------------------------------------------------------------------------
// Peças
// ---------------------------------------------------------------------------------------------

// TestAOS436_NascimentoDaChaveTransit — as duas formas do `data.keys` e a regra do mais antigo.
func TestAOS436_NascimentoDaChaveTransit(t *testing.T) {
	sim := `{"data":{"keys":{"1":1700000000,"2":1800000000}}}`
	if n, err := nascimentoDeChaveTransit([]byte(sim)); err != nil || n.Unix() != 1700000000 {
		t.Errorf("simetrica: esperava a versao 1 (1700000000); veio %v %v", n, err)
	}
	asim := `{"data":{"keys":{"1":{"creation_time":"2026-01-02T03:04:05Z"}}}}`
	if n, err := nascimentoDeChaveTransit([]byte(asim)); err != nil || !n.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("assimetrica: veio %v %v", n, err)
	}
	for _, mau := range []string{`{"data":{"keys":{}}}`, `{"data":{"name":"x"}}`, `{"data":{"keys":{"1":"x"}}}`, `lixo`} {
		if _, err := nascimentoDeChaveTransit([]byte(mau)); err == nil {
			t.Errorf("%s: sem instante legivel devia ser ERRO (adivinhar a idade e decidir as cegas)", mau)
		}
	}
}

// TestAOS436_RegistoNaoLevaOTitular — o registo sai em claro do bundle cifrado; só pode levar o
// nome não-reversível.
func TestAOS436_RegistoNaoLevaOTitular(t *testing.T) {
	const titular = "nhi:titular-secreto-436"
	p := filepath.Join(t.TempDir(), "r.txt")
	reg := novoRegistoDeApagamentos(p)
	if err := reg.acrescentar(entradaDeApagamento{nome: nomeDeApagamento(titular), destruidaEm: instanteDaDestruicao}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "titular-secreto") {
		t.Fatal("o registo de apagamentos leva o titular em claro")
	}
	if !strings.Contains(string(raw), nomeDeApagamento(titular)) {
		t.Fatal("o registo devia levar o nome nao-reversivel da chave")
	}
}

// TestAOS436_EnvSuperficie — as duas variáveis chegam à Config.
func TestAOS436_EnvSuperficie(t *testing.T) {
	t.Setenv("AOS_DSAR_ERASURE_REGISTER", " /var/lib/aos/apagamentos-dsar.txt ")
	t.Setenv("AOS_DSAR_ERASURE_REGISTER_IMPORT", "/var/lib/aos/apagamentos-importado.txt")
	cfg, err := nodeConfigFromEnv()
	if err != nil {
		t.Fatalf("nodeConfigFromEnv: %v", err)
	}
	if cfg.DSARErasureRegister != "/var/lib/aos/apagamentos-dsar.txt" ||
		cfg.DSARErasureRegisterImport != "/var/lib/aos/apagamentos-importado.txt" {
		t.Fatalf("as variaveis nao chegaram a Config: %q %q", cfg.DSARErasureRegister, cfg.DSARErasureRegisterImport)
	}
}

// TestAOS436_ReshredConfirmaUmaPendenciaDoTitular — um re-apagamento selado é uma destruição
// confirmada para o restauro das pendências (AOS-322): sem isto, um titular cuja segunda destruição
// ficou por confirmar e que a reconciliação depois destruiu ficava UNREADY para sempre.
func TestAOS436_ReshredConfirmaUmaPendenciaDoTitular(t *testing.T) {
	store := audit.NewMemStore()
	for _, f := range []audit.AuditRecord{
		{Capability: dsar.EventShredUnconfirmed, Resource: audit.Resource{Type: subjectResourceType, Value: "nhi:p"}},
		{Capability: EventKeyReshredded, Resource: audit.Resource{Type: subjectResourceType, Value: "nhi:p"}},
		{Capability: dsar.EventShredUnconfirmed, Resource: audit.Resource{Type: subjectResourceType, Value: "nhi:q"}},
		{Capability: EventKeyReshredded, Resource: audit.Resource{Type: kekResourceType, Value: nomeDeApagamento("nhi:q")}},
	} {
		f.Partition, f.Decision = "governance.dsar", audit.DecisionAllow
		if _, err := store.Append(context.Background(), f); err != nil {
			t.Fatal(err)
		}
	}
	cust := novaCustodiaComPendencias()
	if _, err := restoreShredPending(context.Background(), store, "governance.dsar", cust); err != nil {
		t.Fatal(err)
	}
	if len(cust.repostas) != 1 || cust.repostas[0] != "nhi:q" {
		t.Fatalf("so nhi:q (selo que nomeia a CHAVE, nao o titular) devia ficar pendente; veio %v", cust.repostas)
	}
}
