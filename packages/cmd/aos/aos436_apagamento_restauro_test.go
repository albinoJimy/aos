package main

// AOS-436 — O APAGAMENTO SOBREVIVE AO RESTAURO.
//
// Os testes montam o cenário com o nó REAL (Bootstrap) e um Vault falso que sabe a IDADE de cada
// chave, lista as chaves que tem e — como o Vault 1.18 real — recusa com 400 um DELETE sobre uma
// chave sem `deletion_allowed`.
//
// A primeira versão destes testes passava pela razão errada: verificava `vault.ready()`, e a
// revisão adversarial mostrou que «não pronto» não protegia nada. Estes verificam o que o titular
// sente — a KEK viva ou morta, o conteúdo que abre ou não abre — e cada achado da revisão tem o seu.

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
// Vault falso com IDADES, LIST e deletion_allowed
// ---------------------------------------------------------------------------------------------

type chaveFalsa struct {
	nascida  int64 // segundos Unix
	apagavel bool  // deletion_allowed
}

type vaultComIdades struct {
	mu           sync.Mutex
	chaves       map[string]*chaveFalsa
	proxima      int64           // nascimento de uma chave criada por POST
	shredFalha   bool            // o DELETE responde OK mas a chave SOBREVIVE
	indisponivel bool            // LIST e GET de chaves respondem 503
	getFalha     map[string]bool // GET desta chave responde 500
	gets         int             // GETs a transit/keys/<nome>
}

func novoVaultComIdades() *vaultComIdades {
	return &vaultComIdades{chaves: map[string]*chaveFalsa{}, getFalha: map[string]bool{}, proxima: 1_900_000_000}
}

// restaurar é o restauro de backup: repõe uma chave com a idade que ela tinha (sem deletion_allowed,
// como uma chave acabada de criar).
func (f *vaultComIdades) restaurar(nome string, nascida int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chaves[nome] = &chaveFalsa{nascida: nascida}
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
		fmt.Fprint(w, `{"data":{"ttl":0,"renewable":false}}`) // token sem expiração
		return
	case "/v1/transit/keys":
		if r.Method != "LIST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if f.indisponivel {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if len(f.chaves) == 0 {
			w.WriteHeader(http.StatusNotFound) // o Vault real: motor vazio ⇒ 404
			return
		}
		nomes := make([]string, 0, len(f.chaves))
		for n := range f.chaves {
			nomes = append(nomes, n)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"keys": nomes}})
		return
	}
	resto := strings.TrimPrefix(r.URL.Path, "/v1/transit/")
	switch {
	case strings.HasPrefix(resto, "keys/") && strings.HasSuffix(resto, "/config"):
		nome := strings.TrimSuffix(strings.TrimPrefix(resto, "keys/"), "/config")
		if c, ok := f.chaves[nome]; ok {
			var in struct {
				DeletionAllowed bool `json:"deletion_allowed"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			c.apagavel = in.DeletionAllowed
		}
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(resto, "keys/"):
		nome := strings.TrimPrefix(resto, "keys/")
		switch r.Method {
		case http.MethodPost:
			if _, ok := f.chaves[nome]; !ok {
				f.chaves[nome] = &chaveFalsa{nascida: f.proxima}
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			c, ok := f.chaves[nome]
			if ok && !c.apagavel {
				// O Vault 1.18 real: «deletion is not allowed for this key».
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if !f.shredFalha {
				delete(f.chaves, nome)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			f.gets++
			if f.indisponivel {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if f.getFalha[nome] {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			c, ok := f.chaves[nome]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"name": nome, "type": "aes256-gcm96", "keys": map[string]int64{"1": c.nascida},
			}})
		}
	case strings.HasPrefix(resto, "encrypt/"):
		if _, ok := f.chaves[strings.TrimPrefix(resto, "encrypt/")]; !ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var in struct {
			Plaintext string `json:"plaintext"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		writeData(w, "ciphertext", "vault:v1:"+in.Plaintext)
	case strings.HasPrefix(resto, "decrypt/"):
		if _, ok := f.chaves[strings.TrimPrefix(resto, "decrypt/")]; !ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var in struct {
			Ciphertext string `json:"ciphertext"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		writeData(w, "plaintext", strings.TrimPrefix(in.Ciphertext, "vault:v1:"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// ---------------------------------------------------------------------------------------------
// Utilitários
// ---------------------------------------------------------------------------------------------

// Instantes fixos, todos no PASSADO do relógio de parede (o limite do futuro usa-o).
var (
	instanteDaDestruicao = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	nascidaAntes         = instanteDaDestruicao.Add(-30 * 24 * time.Hour).Unix()
	nascidaDepois        = instanteDaDestruicao.Add(2 * time.Hour).Unix()
	// nascidaHaMuito serve os testes que correm o fluxo DSAR REAL, cujo selo leva o relógio de
	// parede: uma idade de 2001 é anterior a qualquer instante em que o teste possa correr.
	nascidaHaMuito = time.Date(2001, 9, 9, 1, 46, 40, 0, time.UTC).Unix()
	noFuturo       = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
)

// selar acrescenta um registo à partição dada.
func selar(t *testing.T, store audit.Store, rec audit.AuditRecord) {
	t.Helper()
	if _, err := store.Append(context.Background(), rec); err != nil {
		t.Fatalf("selar %s: %v", rec.Capability, err)
	}
}

// cadeiaComApagamento sela `dsar.received` + `dsar.key_destroyed` de um titular, no instante dado.
func cadeiaComApagamento(t *testing.T, store audit.Store, titular string, quando time.Time) {
	t.Helper()
	for _, verbo := range []string{dsar.EventReceived, dsar.EventKeyDestroyed} {
		selar(t, store, audit.AuditRecord{
			Partition: "governance.dsar", Timestamp: quando, Decision: audit.DecisionAllow,
			Capability: verbo, RequestID: "req-aos436",
			Resource: audit.Resource{Type: subjectResourceType, Value: titular},
		})
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

// escreverComAChaveDe escreve entradas num registo (`destino`) AUTENTICADAS com a chave do registo
// `proprio` — a forma de um registo importado legítimo, que foi escrito pelo mesmo nó (a chave
// viaja no bundle). Cria a chave se ainda não existir.
func escreverComAChaveDe(t *testing.T, proprio, destino string, entradas ...entradaDeApagamento) {
	t.Helper()
	p := novoRegistoDeApagamentos(proprio, true)
	p.mu.Lock()
	err := p.prepararChave()
	chave := p.chave
	p.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	d := &registoDeApagamentos{caminho: destino, chave: chave}
	if err := d.acrescentar(entradas...); err != nil {
		t.Fatal(err)
	}
}

func novoServidor(t *testing.T, fv *vaultComIdades) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(fv)
	t.Cleanup(srv.Close)
	return srv
}

// ---------------------------------------------------------------------------------------------
// O caso nominal: (a) Vault antigo + WORM actual
// ---------------------------------------------------------------------------------------------

// TestAOS436_VaultAntigoComWORMActualReDestroiAKEK — a cadeia afirma o apagamento, o restauro do
// Vault trouxe a KEK de volta, e o nó destrói-a antes de servir.
func TestAOS436_VaultAntigoComWORMActualReDestroiAKEK(t *testing.T) {
	const titular = "nhi:apagado-a"
	nome := nomeDaKEK(titular)
	store := audit.NewMemStore()
	cadeiaComApagamento(t, store, titular, instanteDaDestruicao)
	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaAntes)

	_, vault := noComVault(t, store, novoServidor(t, fv), nil)

	if fv.existe(nome) {
		t.Fatal("a KEK ressuscitada pelo restauro CONTINUA viva depois do arranque")
	}
	selos := selosReshred(t, store)
	if len(selos) != 1 || selos[0].Resource.Value != titular || selos[0].Principal.NHIID != reconciliacaoNHI {
		t.Fatalf("esperava 1 dsar.key_reshredded em nome proprio a nomear o titular; veio %+v", selos)
	}
	if selos[0].Obligations[0].Params["destruida_em"] != instanteDaDestruicao.Format(time.RFC3339) {
		t.Errorf("o selo devia levar o instante da destruicao ORIGINAL; veio %v", selos[0].Obligations[0].Params)
	}
	if err := vault.ready(context.Background()); err != nil {
		t.Errorf("reconciliacao provada ⇒ custodia pronta; veio %v", err)
	}
}

// TestAOS436_TitularQueVoltouNaoEDestruido — o CONTROLO que proíbe destruir por existência.
func TestAOS436_TitularQueVoltouNaoEDestruido(t *testing.T) {
	const titular = "nhi:voltou"
	nome := nomeDaKEK(titular)
	store := audit.NewMemStore()
	cadeiaComApagamento(t, store, titular, instanteDaDestruicao)
	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaDepois)

	_, vault := noComVault(t, store, novoServidor(t, fv), nil)

	if !fv.existe(nome) {
		t.Fatal("a reconciliacao DESTRUIU a KEK nova de um titular que voltou depois de apagado")
	}
	if n := len(selosReshred(t, store)); n != 0 {
		t.Errorf("nenhum re-apagamento devia ter sido selado; vieram %d", n)
	}
	if err := vault.ready(context.Background()); err != nil {
		t.Errorf("uma geracao nova e o caso legitimo — custodia pronta; veio %v", err)
	}
}

// TestAOS436_OMesmoSegundoContaComoADestruida — a fronteira resolve-se pelo lado do apagamento.
func TestAOS436_OMesmoSegundoContaComoADestruida(t *testing.T) {
	const titular = "nhi:mesmo-segundo"
	nome := nomeDaKEK(titular)
	store := audit.NewMemStore()
	cadeiaComApagamento(t, store, titular, instanteDaDestruicao.Add(700*time.Millisecond))
	fv := novoVaultComIdades()
	fv.restaurar(nome, instanteDaDestruicao.Unix())

	noComVault(t, store, novoServidor(t, fv), nil)
	if fv.existe(nome) {
		t.Fatal("uma KEK nascida no mesmo segundo da destruicao devia ser tratada como a destruida")
	}
}

// ---------------------------------------------------------------------------------------------
// (b) Restaurar um bundle MAIS ANTIGO — o registo importado
// ---------------------------------------------------------------------------------------------

// TestAOS436_BundleAntigoComRegistoImportado — a cadeia restaurada é anterior ao apagamento e não
// sabe dele. Só o registo, autenticado pela chave que o bundle traz, sabe.
func TestAOS436_BundleAntigoComRegistoImportado(t *testing.T) {
	const titular = "nhi:apagado-b"
	nome := nomeDaKEK(titular)
	dir := t.TempDir()
	proprio := filepath.Join(dir, "apagamentos-dsar.txt")
	importado := filepath.Join(dir, "apagamentos-importado.txt")
	escreverComAChaveDe(t, proprio, importado, entradaDeApagamento{nome: nome, destruidaEm: instanteDaDestruicao})

	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaAntes)
	store := audit.NewMemStore()
	_, vault := noComVault(t, store, novoServidor(t, fv), func(c *Config) {
		c.DSARErasureRegister = proprio
		c.DSARErasureRegisterImport = importado
	})

	if fv.existe(nome) {
		t.Fatal("bundle antigo: a KEK voltou e o registo importado nao a fez destruir")
	}
	selos := selosReshred(t, store)
	if len(selos) != 1 || selos[0].Resource.Type != kekResourceType {
		t.Fatalf("esperava 1 selo a nomear a CHAVE pelo id do registo; veio %+v", selos)
	}
	if selos[0].Resource.Value == nome || !reHex64.MatchString(selos[0].Resource.Value) {
		t.Errorf("o selo nomeia o nome do Vault (invertivel) em vez do id do registo: %q", selos[0].Resource.Value)
	}
	// O registo próprio passou a superconjunto — é isto que o próximo backup leva.
	reg := novoRegistoDeApagamentos(proprio, true)
	lido, err := reg.ler(proprio, false, time.Now())
	if err != nil || len(lido.rejeitadas) != 0 || !lido.validas[reg.idDe(nome)].Equal(instanteDaDestruicao) {
		t.Fatalf("a entrada importada devia ter sido fundida no registo proprio; veio %+v %v", lido, err)
	}
	if err := vault.ready(context.Background()); err != nil {
		t.Errorf("reconciliacao provada ⇒ custodia pronta; veio %v", err)
	}
}

// ---------------------------------------------------------------------------------------------
// ACHADO 1 — registo envenenado, instante no futuro, selo relido como autoridade, legal hold
// ---------------------------------------------------------------------------------------------

// TestAOS436_RegistoImportadoForjadoNaoDestroi — a linha da revisão: o nome da KEK VIVA da alice,
// um instante em 2099, e nenhum MAC válido (quem forja não tem a chave). Não destrói; fica nomeada.
func TestAOS436_RegistoImportadoForjadoNaoDestroi(t *testing.T) {
	const alice = "human:alice"
	nome := nomeDaKEK(alice)
	dir := t.TempDir()
	proprio := filepath.Join(dir, "apagamentos-dsar.txt")
	importado := filepath.Join(dir, "importado.txt")
	// Três forjas: o formato antigo (nome em claro), um id/mac inventados, e um registo válido
	// mas escrito com OUTRA chave.
	outro := filepath.Join(dir, "outro-no.txt")
	escreverComAChaveDe(t, outro, filepath.Join(dir, "outro-importado.txt"), entradaDeApagamento{nome: nome, destruidaEm: instanteDaDestruicao})
	deOutroNo, _ := os.ReadFile(filepath.Join(dir, "outro-importado.txt"))
	forjado := nome + " 2099-01-01T00:00:00Z\n" +
		strings.Repeat("a", 64) + " 2099-01-01T00:00:00Z " + strings.Repeat("b", 64) + "\n" +
		string(deOutroNo)
	if err := os.WriteFile(importado, []byte(forjado), 0o600); err != nil {
		t.Fatal(err)
	}
	regCom(t, proprio) // o volume restaurado traz a chave do no, como num restauro a serio

	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaDepois)
	store := audit.NewMemStore()
	_, vault := noComVault(t, store, novoServidor(t, fv), func(c *Config) {
		c.DSARErasureRegister = proprio
		c.DSARErasureRegisterImport = importado
	})

	if !fv.existe(nome) {
		t.Fatal("um registo importado FORJADO destruiu a KEK viva da alice")
	}
	if n := len(selosReshred(t, store)); n != 0 {
		t.Errorf("nada devia ter sido selado; vieram %d", n)
	}
	err := vault.ready(context.Background())
	if !errors.Is(err, ErrApagamentoPorReconciliar) || !strings.Contains(err.Error(), "MAC invalido") {
		t.Fatalf("as linhas forjadas deviam ficar NOMEADAS e a reconciliacao por provar; veio %v", err)
	}
}

// TestAOS436_InstanteNoFuturoComMACValidoNaoDestroi — um relógio adiantado faz o mesmo que um
// atacante, sem malícia: a linha autentica e o instante é impossível.
func TestAOS436_InstanteNoFuturoComMACValidoNaoDestroi(t *testing.T) {
	const titular = "nhi:relogio-errado"
	nome := nomeDaKEK(titular)
	dir := t.TempDir()
	proprio := filepath.Join(dir, "apagamentos-dsar.txt")
	escreverComAChaveDe(t, proprio, proprio, entradaDeApagamento{nome: nome, destruidaEm: noFuturo})

	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaDepois)
	_, vault := noComVault(t, audit.NewMemStore(), novoServidor(t, fv), func(c *Config) { c.DSARErasureRegister = proprio })

	if !fv.existe(nome) {
		t.Fatal("uma entrada datada no FUTURO destruiu uma KEK viva")
	}
	if err := vault.ready(context.Background()); err == nil || !strings.Contains(err.Error(), "FUTURO") {
		t.Fatalf("a entrada no futuro devia ficar nomeada; veio %v", err)
	}
}

// TestAOS436_OSeloDoReApagamentoNaoEAutoridade — o WORM não eterniza uma data. Um
// `dsar.key_reshredded` com `destruida_em` em 2099 (a pegada exacta do achado) está selado; a
// KEK viva do titular NÃO é destruída por ele, arranque após arranque.
func TestAOS436_OSeloDoReApagamentoNaoEAutoridade(t *testing.T) {
	const alice = "human:alice"
	nome := nomeDaKEK(alice)
	store := audit.NewMemStore()
	selar(t, store, audit.AuditRecord{
		Partition: "governance.dsar", Timestamp: instanteDaDestruicao, Decision: audit.DecisionAllow,
		Capability: EventKeyReshredded, Resource: audit.Resource{Type: subjectResourceType, Value: alice},
		Obligations: []audit.Obligation{{Type: obReshred, Params: map[string]string{"destruida_em": "2099-01-01T00:00:00Z"}}},
	})
	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaDepois)

	// E um segundo selo com uma data PLAUSÍVEL (passada, posterior ao nascimento da KEK viva): se o
	// selo fosse autoridade, esta data bastava para a condenar — o limite do futuro não a apanha.
	const bob = "human:bob"
	selar(t, store, audit.AuditRecord{
		Partition: "governance.dsar", Timestamp: instanteDaDestruicao, Decision: audit.DecisionAllow,
		Capability: EventKeyReshredded, Resource: audit.Resource{Type: subjectResourceType, Value: bob},
		Obligations: []audit.Obligation{{Type: obReshred, Params: map[string]string{
			"destruida_em": instanteDaDestruicao.Add(3 * time.Hour).Format(time.RFC3339)}}},
	})
	fv.restaurar(nomeDaKEK(bob), nascidaDepois)

	_, vault := noComVault(t, store, novoServidor(t, fv), nil)
	if !fv.existe(nome) || !fv.existe(nomeDaKEK(bob)) {
		t.Fatal("um dsar.key_reshredded relido da cadeia destruiu uma KEK viva — o WORM eterniza a data")
	}
	if err := vault.ready(context.Background()); err != nil {
		t.Errorf("sem autoridade de destruicao na cadeia nao ha nada por provar; veio %v", err)
	}
}

// TestAOS436_FactoDaCadeiaNoFuturoNaoDestroi — o mesmo limite vale para a cadeia.
func TestAOS436_FactoDaCadeiaNoFuturoNaoDestroi(t *testing.T) {
	const titular = "nhi:cadeia-adiantada"
	nome := nomeDaKEK(titular)
	store := audit.NewMemStore()
	cadeiaComApagamento(t, store, titular, noFuturo)
	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaDepois)
	noComVault(t, store, novoServidor(t, fv), nil)
	if !fv.existe(nome) {
		t.Fatal("um key_destroyed datado no futuro destruiu a KEK viva")
	}
}

// TestAOS436_LegalHoldNaoEDestruidoEFicaFechado — a re-destruição consulta o hold, como o fluxo DSAR.
// A KEK retida fica viva (preservação) e FECHADA no portão (nada se decifra nem se escreve sob ela);
// a de outro titular continua a servir, e o nó não sai de rotação por um hold.
func TestAOS436_LegalHoldNaoEDestruidoEFicaFechado(t *testing.T) {
	const retido, livre = "nhi:sob-hold", "nhi:livre"
	nome := nomeDaKEK(retido)
	store := audit.NewMemStore()
	cadeiaComApagamento(t, store, retido, instanteDaDestruicao)
	selar(t, store, audit.AuditRecord{
		Partition: legalHoldPartition, Timestamp: instanteDaDestruicao.Add(time.Hour), Decision: audit.DecisionAllow,
		Capability: capLegalHoldPlace, Resource: audit.Resource{Type: subjectResourceType, Value: retido},
		Obligations: []audit.Obligation{{Type: legalHoldTargetObl, Params: map[string]string{"subject_id": retido}}},
	})
	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaAntes)

	_, vault := noComVault(t, store, novoServidor(t, fv), nil)

	if !fv.existe(nome) {
		t.Fatal("a reconciliacao destruiu uma KEK sob LEGAL HOLD — destruicao de prova")
	}
	if _, _, err := vault.WrapDEK(retido, []byte("dek")); !errors.Is(err, ErrApagamentoPorReconciliar) {
		t.Errorf("a KEK retida devia estar FECHADA no portao; WrapDEK veio %v", err)
	}
	if _, ok := vault.UnwrapDEK(audit.KeyRefFor(retido), []byte("vault:v1:ZGVr")); ok {
		t.Error("a KEK retida desembrulhou uma DEK — o portao nao a fechou")
	}
	if _, _, err := vault.WrapDEK(livre, []byte("dek")); err != nil {
		t.Errorf("CONTROLO: outro titular devia continuar a servir; veio %v", err)
	}
	if err := vault.ready(context.Background()); err != nil {
		t.Errorf("um hold nao tira o no de rotacao (o bloqueio ja preserva); veio %v", err)
	}
}

// ---------------------------------------------------------------------------------------------
// ACHADO 2 — «por provar» tem de FECHAR o conteúdo, não só pintar o /readyz
// ---------------------------------------------------------------------------------------------

// TestAOS436_ConteudoFechadoEnquantoPorProvar prova-o pelo MESMO objecto que o replay soberano usa
// (`node.contentOpener`, sovereign_replay.go): selado antes, fica ilegível enquanto a reconciliação
// estiver por provar; nada novo se sela; e volta a abrir quando a passagem se prova (controlo).
func TestAOS436_ConteudoFechadoEnquantoPorProvar(t *testing.T) {
	const titular = "nhi:com-conteudo"
	store := audit.NewMemStore()
	cadeiaComApagamento(t, store, "nhi:outro-apagado", instanteDaDestruicao) // há o que reconciliar
	fv := novoVaultComIdades()
	node, vault := noComVault(t, store, novoServidor(t, fv), nil)
	svc, h := newAPI(t, node)
	ctx := context.Background()

	sealer, ok := node.contentOpener.(*contentSealer)
	if !ok {
		t.Fatalf("o contentOpener do no nao e o contentSealer (%T)", node.contentOpener)
	}
	selado, err := sealer.SealContent(ctx, titular, "run-1", []byte("segredo do titular"))
	if err != nil {
		t.Fatalf("PRECONDICAO: com a reconciliacao provada o conteudo sela; veio %v", err)
	}

	// O Vault deixa de responder à lista: a passagem seguinte fica por provar.
	fv.define(func(f *vaultComIdades) { f.indisponivel = true })
	svc.RefreshVaultTokenNow(ctx)

	if claro, err := node.contentOpener.OpenContent(ctx, titular, selado); err == nil {
		t.Fatalf("POR PROVAR e o conteudo DECIFROU (%q) — o not-ready nao protegia nada, e continua sem proteger", claro)
	}
	if _, err := sealer.SealContent(ctx, "nhi:novo", "run-2", []byte("x")); !errors.Is(err, ErrApagamentoPorReconciliar) {
		t.Errorf("POR PROVAR e o conteudo novo foi SELADO sob KEK por reconciliar; veio %v", err)
	}
	if code, _ := getProbe(h, "/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz devia ser 503; veio %d", code)
	}
	if v, ok := leGauge(t, h, "aos_dsar_erasure_reconciled"); !ok || v != 0 {
		t.Errorf("aos_dsar_erasure_reconciled devia ler 0; veio %v (presente=%v)", v, ok)
	}

	// CONTROLO: o Vault volta, a passagem prova-se, o conteúdo volta a abrir.
	fv.define(func(f *vaultComIdades) { f.indisponivel = false })
	svc.RefreshVaultTokenNow(ctx)
	claro, err := node.contentOpener.OpenContent(ctx, titular, selado)
	if err != nil || string(claro) != "segredo do titular" {
		t.Fatalf("provada a reconciliacao, o conteudo devia voltar a abrir; veio %q %v", claro, err)
	}
	if err := vault.ready(ctx); err != nil {
		t.Errorf("provada ⇒ pronta; veio %v", err)
	}
}

// ---------------------------------------------------------------------------------------------
// ACHADO 3 — as fontes são independentes: a cadeia é SEMPRE reconciliada
// ---------------------------------------------------------------------------------------------

func TestAOS436_FonteQueFalhaNaoImpedeACadeia(t *testing.T) {
	const titular = "nhi:so-a-cadeia-sabe"
	nome := nomeDaKEK(titular)
	dir := t.TempDir()
	proprio := filepath.Join(dir, "apagamentos-dsar.txt")
	// O registo próprio tem uma linha COMPLETA malformada, e o importado não existe. A chave existe
	// (criada antes de o lixo aparecer, como num volume real).
	regCom(t, proprio)
	if err := os.WriteFile(proprio, []byte("isto nao e uma linha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := audit.NewMemStore()
	cadeiaComApagamento(t, store, titular, instanteDaDestruicao)
	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaAntes)

	_, vault := noComVault(t, store, novoServidor(t, fv), func(c *Config) {
		c.DSARErasureRegister = proprio
		c.DSARErasureRegisterImport = filepath.Join(dir, "nao-existe.txt")
	})

	if fv.existe(nome) {
		t.Fatal("duas fontes opcionais falharam e a KEK que a CADEIA conhece ficou viva")
	}
	err := vault.ready(context.Background())
	if err == nil || !strings.Contains(err.Error(), "importado") || !strings.Contains(err.Error(), "malformada") {
		t.Fatalf("as duas fontes falhadas deviam ficar NOMEADAS; veio %v", err)
	}
}

// ---------------------------------------------------------------------------------------------
// ACHADO 4 — a linha cortada não é permanente
// ---------------------------------------------------------------------------------------------

func TestAOS436_LinhaCortadaETruncadaNaProximaEscrita(t *testing.T) {
	dir := t.TempDir()
	proprio := filepath.Join(dir, "apagamentos-dsar.txt")
	escreverComAChaveDe(t, proprio, proprio, entradaDeApagamento{nome: nomeDaKEK("nhi:a"), destruidaEm: instanteDaDestruicao})
	// Uma escrita interrompida: meia linha, sem '\n'.
	f, err := os.OpenFile(proprio, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(strings.Repeat("c", 40))
	_ = f.Close()

	reg := novoRegistoDeApagamentos(proprio, true)
	lido, err := reg.ler(proprio, false, time.Now())
	if err != nil || len(lido.rejeitadas) != 0 || !lido.fragmento || len(lido.validas) != 1 {
		t.Fatalf("o fragmento final devia ser IGNORADO (e dito), sem rejeitar as linhas completas; veio %+v %v", lido, err)
	}
	// A próxima escrita trunca o fragmento e acrescenta uma linha inteira.
	if err := reg.acrescentar(entradaDeApagamento{nome: nomeDaKEK("nhi:b"), destruidaEm: instanteDaDestruicao}); err != nil {
		t.Fatal(err)
	}
	if avisos := reg.tirarAvisos(); len(avisos) != 1 || !strings.Contains(avisos[0], "TRUNCADO") {
		t.Errorf("a truncagem devia ser dita; veio %v", avisos)
	}
	lido, err = reg.ler(proprio, false, time.Now())
	if err != nil || len(lido.rejeitadas) != 0 || lido.fragmento || len(lido.validas) != 2 {
		t.Fatalf("depois da escrita o registo devia estar limpo e com as duas entradas; veio %+v %v", lido, err)
	}
	// CONTROLO: uma linha COMPLETA malformada continua a ser rejeitada.
	f, _ = os.OpenFile(proprio, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("lixo completo\n")
	_ = f.Close()
	if lido, _ := reg.ler(proprio, false, time.Now()); len(lido.rejeitadas) != 1 {
		t.Fatalf("uma linha completa malformada devia ser rejeitada; veio %+v", lido)
	}
}

// ---------------------------------------------------------------------------------------------
// ACHADO 5 — o registo não revela o titular
// ---------------------------------------------------------------------------------------------

func TestAOS436_RegistoNaoRevelaOTitular(t *testing.T) {
	const titular = "human:user-4242"
	nome := nomeDaKEK(titular)
	proprio := filepath.Join(t.TempDir(), "r.txt")
	escreverComAChaveDe(t, proprio, proprio, entradaDeApagamento{nome: nome, destruidaEm: instanteDaDestruicao})
	raw, err := os.ReadFile(proprio)
	if err != nil {
		t.Fatal(err)
	}
	// O nome do Vault é sha256 PÚBLICO do keyRef — invertível por dicionário de utilizadores.
	for _, proibido := range []string{"user-4242", nome, strings.TrimPrefix(nome, "aos-kek-")} {
		if strings.Contains(string(raw), proibido) {
			t.Fatalf("o registo em claro contem %q — um dicionario de utilizadores diz quem pediu o Art. 17", proibido)
		}
	}
}

// ---------------------------------------------------------------------------------------------
// ACHADO 8 — um LIST, não um GET por chave; e nenhuma chave pára as outras
// ---------------------------------------------------------------------------------------------

func TestAOS436_MilharesDeApagamentosUmLIST(t *testing.T) {
	store := audit.NewMemStore()
	for i := 0; i < 3000; i++ {
		cadeiaComApagamento(t, store, fmt.Sprintf("nhi:antigo-%04d", i), instanteDaDestruicao)
	}
	falha, ressuscitada := "nhi:antigo-0001", "nhi:antigo-2999"
	// A passagem percorre as KEKs por ordem de nome: a que falha tem de vir PRIMEIRO, senão uma
	// passagem que abortasse no primeiro erro passava neste teste.
	if nomeDaKEK(falha) > nomeDaKEK(ressuscitada) {
		falha, ressuscitada = ressuscitada, falha
	}
	fv := novoVaultComIdades()
	fv.restaurar(nomeDaKEK(falha), nascidaAntes)
	fv.restaurar(nomeDaKEK(ressuscitada), nascidaAntes)
	fv.define(func(f *vaultComIdades) { f.getFalha[nomeDaKEK(falha)] = true })

	_, vault := noComVault(t, store, novoServidor(t, fv), nil)

	fv.define(func(f *vaultComIdades) {
		if f.gets > 10 {
			t.Errorf("%d GETs para 3000 apagamentos com 2 KEKs vivas — devia ser um LIST e um GET por KEK viva", f.gets)
		}
	})
	if fv.existe(nomeDaKEK(ressuscitada)) {
		t.Fatal("uma chave que falhou a leitura IMPEDIU a re-destruicao da seguinte")
	}
	if _, _, err := vault.WrapDEK(falha, []byte("d")); !errors.Is(err, ErrApagamentoPorReconciliar) {
		t.Errorf("a KEK de idade por verificar devia ficar FECHADA no portao; veio %v", err)
	}
	if err := vault.ready(context.Background()); err == nil {
		t.Error("uma KEK viva por verificar devia manter o no fora de rotacao")
	}
}

// ---------------------------------------------------------------------------------------------
// Hipóteses da revisão, fechadas por teste
// ---------------------------------------------------------------------------------------------

// TestAOS436_VaultRestauradoComONoACorrer — a passagem corre em cada tick, não só no arranque.
func TestAOS436_VaultRestauradoComONoACorrer(t *testing.T) {
	const titular = "nhi:restaurado-a-quente"
	nome := nomeDaKEK(titular)
	store := audit.NewMemStore()
	cadeiaComApagamento(t, store, titular, instanteDaDestruicao)
	fv := novoVaultComIdades()
	node, _ := noComVault(t, store, novoServidor(t, fv), nil)
	svc, _ := newAPI(t, node)

	fv.restaurar(nome, nascidaAntes) // o Vault é restaurado por baixo do nó
	svc.RefreshVaultTokenNow(context.Background())
	if fv.existe(nome) {
		t.Fatal("um Vault restaurado com o no a correr ficou com a KEK ressuscitada viva")
	}
}

// ---------------------------------------------------------------------------------------------
// O ciclo completo pelo fluxo DSAR real
// ---------------------------------------------------------------------------------------------

// TestAOS436_CicloCompletoApagarERestaurar: apagar pelo fluxo composto, restaurar (a) e (b) — e
// (b) sem a chave do registo, que é o caso de um bundle anterior a ela.
func TestAOS436_CicloCompletoApagarERestaurar(t *testing.T) {
	const titular = "nhi:ciclo"
	nome := nomeDaKEK(titular)
	dir := t.TempDir()
	d1, d2, d3 := filepath.Join(dir, "no1"), filepath.Join(dir, "no2"), filepath.Join(dir, "no3")
	for _, d := range []string{d1, d2, d3} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	registo1 := filepath.Join(d1, "apagamentos-dsar.txt")
	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaHaMuito)
	srv := novoServidor(t, fv)

	// (1) O APAGAMENTO, pelo fluxo composto pelo nó.
	worm := audit.NewMemStore()
	node1, _ := noComVault(t, worm, srv, func(c *Config) { c.DSARErasureRegister = registo1 })
	if _, err := node1.DSAR.Receive(context.Background(), dsar.Request{
		RequestID: "req-ciclo", SubjectID: titular, Principal: "nhi:operador",
	}); err != nil {
		t.Fatalf("erase: %v", err)
	}
	if fv.existe(nome) {
		t.Fatal("PRECONDICAO: o erase nao destruiu a KEK (o fake exige deletion_allowed)")
	}
	reg1 := novoRegistoDeApagamentos(registo1, true)
	if lido, err := reg1.ler(registo1, false, time.Now()); err != nil || len(lido.validas) != 1 {
		t.Fatalf("o Delete CONFIRMADO devia ter escrito a entrada; veio %+v %v", lido, err)
	}

	// (2) RESTAURO (a): vault-data antigo, WORM actual.
	fv.restaurar(nome, nascidaHaMuito)
	noComVault(t, worm, srv, func(c *Config) { c.DSARErasureRegister = registo1 })
	if fv.existe(nome) {
		t.Fatal("restauro (a): a KEK ressuscitada sobreviveu ao arranque")
	}

	// (3) RESTAURO (b): bundle mais antigo — a cadeia não sabe; a chave do registo veio no bundle.
	fv.restaurar(nome, nascidaHaMuito)
	registo2 := filepath.Join(d2, "apagamentos-dsar.txt")
	chave, _ := os.ReadFile(registo1 + sufixoDaChaveDoRegisto)
	if err := os.WriteFile(registo2+sufixoDaChaveDoRegisto, chave, 0o600); err != nil {
		t.Fatal(err)
	}
	noComVault(t, audit.NewMemStore(), srv, func(c *Config) {
		c.DSARErasureRegister = registo2
		c.DSARErasureRegisterImport = registo1
	})
	if fv.existe(nome) {
		t.Fatal("restauro (b): a KEK ressuscitada sobreviveu com o registo importado")
	}

	// (4) RESTAURO (b) de um bundle ANTERIOR À CHAVE: o nó cria uma chave nova, o importado não
	// autentica — NÃO destrói, fica por provar e diz porquê.
	fv.restaurar(nome, nascidaHaMuito)
	_, v3 := noComVault(t, audit.NewMemStore(), srv, func(c *Config) {
		c.DSARErasureRegister = filepath.Join(d3, "apagamentos-dsar.txt")
		c.DSARErasureRegisterImport = registo1
	})
	if !fv.existe(nome) {
		t.Fatal("um registo que nao autentica sob a chave do no destruiu uma KEK")
	}
	if err := v3.ready(context.Background()); err == nil || !strings.Contains(err.Error(), "NAO existe e ha uma importacao pedida") {
		t.Fatalf("devia ficar por provar a dizer que falta a chave; veio %v", err)
	}
	if _, err := os.Stat(filepath.Join(d3, "apagamentos-dsar.txt"+sufixoDaChaveDoRegisto)); !os.IsNotExist(err) {
		t.Fatal("com uma importacao pedida o no CRIOU uma chave nova — o importado nunca mais autenticaria")
	}
}

// TestAOS436_DestruicaoPorConfirmarNaoEntraNoRegisto — o registo é do que MORREU.
func TestAOS436_DestruicaoPorConfirmarNaoEntraNoRegisto(t *testing.T) {
	const titular = "nhi:nao-morreu"
	registo := filepath.Join(t.TempDir(), "apagamentos-dsar.txt")
	fv := novoVaultComIdades()
	fv.restaurar(nomeDaKEK(titular), nascidaHaMuito)
	fv.define(func(f *vaultComIdades) { f.shredFalha = true })

	node, _ := noComVault(t, audit.NewMemStore(), novoServidor(t, fv), func(c *Config) { c.DSARErasureRegister = registo })
	_, _ = node.DSAR.Receive(context.Background(), dsar.Request{RequestID: "req-x", SubjectID: titular, Principal: "nhi:operador"})

	lido, err := novoRegistoDeApagamentos(registo, true).ler(registo, true, time.Now())
	if err != nil || len(lido.validas) != 0 {
		t.Fatalf("uma destruicao POR CONFIRMAR entrou no registo: %+v %v", lido, err)
	}
}

// TestAOS436_RegistoQueNaoSeEscreveDeixaONoUnready — a destruição confirmada que não chega ao disco
// fica pendente, e a custódia fica vermelha até ela chegar.
func TestAOS436_RegistoQueNaoSeEscreveDeixaONoUnready(t *testing.T) {
	registo := filepath.Join(t.TempDir(), "sub", "apagamentos-dsar.txt") // o directório ainda não existe
	fv := novoVaultComIdades()
	node, vault := noComVault(t, audit.NewMemStore(), novoServidor(t, fv), func(c *Config) { c.DSARErasureRegister = registo })
	fv.restaurar(nomeDaKEK("nhi:z"), nascidaHaMuito)
	vault.Delete("nhi:z")

	if err := vault.ready(context.Background()); err == nil || !strings.Contains(err.Error(), "registo") {
		t.Fatalf("registo por escrever devia deixar a custodia VERMELHA, a nomea-lo; veio %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(registo), 0o700); err != nil {
		t.Fatal(err)
	}
	node.apagamentos.reconciliarPeriodicamente(func(string, ...any) {})
	if err := vault.ready(context.Background()); err != nil {
		t.Fatalf("depois de a pendente chegar ao disco a custodia devia estar pronta; veio %v", err)
	}
	reg := novoRegistoDeApagamentos(registo, true)
	if lido, _ := reg.ler(registo, false, time.Now()); lido.validas[reg.idDe(nomeDaKEK("nhi:z"))].IsZero() {
		t.Fatal("a entrada pendente nao chegou ao registo")
	}
}

// ---------------------------------------------------------------------------------------------
// Peças
// ---------------------------------------------------------------------------------------------

// TestAOS436_OFakeModelaODeletionAllowed — o fake recusa com 400 um DELETE sem deletion_allowed,
// como o Vault real. Sem isto, um Delete que esquecesse a habilitação passaria nos testes.
func TestAOS436_OFakeModelaODeletionAllowed(t *testing.T) {
	fv := novoVaultComIdades()
	nome := nomeDaKEK("nhi:x")
	fv.restaurar(nome, nascidaAntes)
	srv := novoServidor(t, fv)
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/v1/transit/keys/"+nome, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !fv.existe(nome) {
		t.Fatalf("DELETE sem deletion_allowed devia dar 400 e deixar a chave; veio %d existe=%v", resp.StatusCode, fv.existe(nome))
	}
	if err := newVaultKeyVault(srv.URL, "transit", "tok").destruirKEKPorNome(context.Background(), nome); err != nil || fv.existe(nome) {
		t.Fatalf("a destruicao do no habilita primeiro e confirma; veio %v existe=%v", err, fv.existe(nome))
	}
}

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
			t.Errorf("%s: sem instante legivel devia ser ERRO", mau)
		}
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

// TestAOS436_ReshredConfirmaUmaPendenciaDoTitular — um re-apagamento selado a nomear o titular é uma
// destruição confirmada para o restauro das pendências (AOS-322).
func TestAOS436_ReshredConfirmaUmaPendenciaDoTitular(t *testing.T) {
	store := audit.NewMemStore()
	for _, f := range []audit.AuditRecord{
		{Capability: dsar.EventShredUnconfirmed, Resource: audit.Resource{Type: subjectResourceType, Value: "nhi:p"}},
		{Capability: EventKeyReshredded, Resource: audit.Resource{Type: subjectResourceType, Value: "nhi:p"}},
		{Capability: dsar.EventShredUnconfirmed, Resource: audit.Resource{Type: subjectResourceType, Value: "nhi:q"}},
		{Capability: EventKeyReshredded, Resource: audit.Resource{Type: kekResourceType, Value: strings.Repeat("d", 64)}},
	} {
		f.Partition, f.Decision = "governance.dsar", audit.DecisionAllow
		selar(t, store, f)
	}
	cust := novaCustodiaComPendencias()
	if _, err := restoreShredPending(context.Background(), store, "governance.dsar", cust); err != nil {
		t.Fatal(err)
	}
	if len(cust.repostas) != 1 || cust.repostas[0] != "nhi:q" {
		t.Fatalf("so nhi:q devia ficar pendente; veio %v", cust.repostas)
	}
}
