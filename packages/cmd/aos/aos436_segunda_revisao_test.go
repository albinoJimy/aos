package main

// AOS-436 — os achados da SEGUNDA revisão adversarial independente, um teste por achado. Os testes
// do revisor (rev436_*_test.go, fora do repositório) serviram de ponto de partida: aqui passam a
// afirmar o comportamento corrigido, e cada um tem uma mutação que o avermelha (ver o ticket).

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/durable"
	audit "github.com/aos-ref/platform/audit"
)

func regCom(t *testing.T, proprio string) *registoDeApagamentos {
	t.Helper()
	r := novoRegistoDeApagamentos(proprio, true)
	r.mu.Lock()
	err := r.prepararChave()
	r.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func sem(t *testing.T, caminho, prefixo string) {
	t.Helper()
	raw, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatal(err)
	}
	var fica []string
	for _, l := range strings.SplitAfter(string(raw), "\n") {
		if !strings.HasPrefix(l, prefixo) {
			fica = append(fica, l)
		}
	}
	if err := os.WriteFile(caminho, []byte(strings.Join(fica, "")), 0o600); err != nil {
		t.Fatal(err)
	}
}

// R1 (a meio) — remover uma linha do MEIO do importado parte a cadeia: a linha seguinte deixa de
// autenticar, e a reconciliação NÃO se declara provada. (O corte do FIM é resíduo declarado.)
func TestAOS436R2_LinhaRemovidaAMeioPartACadeia(t *testing.T) {
	nA, nB, nC := nomeDaKEK("nhi:A"), nomeDaKEK("nhi:B"), nomeDaKEK("nhi:C")
	dir := t.TempDir()
	proprio := filepath.Join(dir, "apagamentos-dsar.txt")
	importado := filepath.Join(dir, "importado.txt")
	escreverComAChaveDe(t, proprio, importado,
		entradaDeApagamento{nome: nA, destruidaEm: instanteDaDestruicao},
		entradaDeApagamento{nome: nB, destruidaEm: instanteDaDestruicao},
		entradaDeApagamento{nome: nC, destruidaEm: instanteDaDestruicao})
	sem(t, importado, regCom(t, proprio).idDe(nB))

	fv := novoVaultComIdades()
	for _, n := range []string{nA, nB, nC} {
		fv.restaurar(n, nascidaAntes)
	}
	_, vault := noComVault(t, audit.NewMemStore(), novoServidor(t, fv), func(c *Config) {
		c.DSARErasureRegister = proprio
		c.DSARErasureRegisterImport = importado
	})
	err := vault.ready(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cadeia partiu-se") {
		t.Fatalf("uma linha removida a meio devia partir a cadeia e deixar a reconciliacao POR PROVAR; veio %v", err)
	}
	if _, _, werr := vault.WrapDEK("nhi:qualquer", []byte("d")); !errors.Is(werr, ErrApagamentoPorReconciliar) {
		t.Errorf("por provar, o portao devia estar fechado; veio %v", werr)
	}
	if fv.existe(nA) {
		t.Error("a linha A autentica e devia ter sido aplicada na mesma")
	}
}

// R1 (mais antigo) — um importado a quem falta um apagamento que o bundle restaurado já conhece é
// mais antigo do que o bundle: recusado, e NADA é escrito no registo próprio.
func TestAOS436R2_ImportadoMaisAntigoQueOBundleERecusado(t *testing.T) {
	nX, nY := nomeDaKEK("nhi:X-no-bundle"), nomeDaKEK("nhi:Y")
	dir := t.TempDir()
	proprio := filepath.Join(dir, "apagamentos-dsar.txt")
	importado := filepath.Join(dir, "importado.txt")
	escreverComAChaveDe(t, proprio, proprio, entradaDeApagamento{nome: nX, destruidaEm: instanteDaDestruicao})
	escreverComAChaveDe(t, proprio, importado, entradaDeApagamento{nome: nY, destruidaEm: instanteDaDestruicao})
	antes, _ := os.ReadFile(proprio)

	fv := novoVaultComIdades()
	fv.restaurar(nY, nascidaAntes)
	_, vault := noComVault(t, audit.NewMemStore(), novoServidor(t, fv), func(c *Config) {
		c.DSARErasureRegister = proprio
		c.DSARErasureRegisterImport = importado
	})
	err := vault.ready(context.Background())
	if err == nil || !strings.Contains(err.Error(), "MAIS ANTIGO") {
		t.Fatalf("um importado mais antigo do que o bundle devia ser recusado e nomeado; veio %v", err)
	}
	depois, _ := os.ReadFile(proprio)
	if !bytes.Equal(antes, depois) {
		t.Error("com o importado recusado, o registo proprio foi ESCRITO — fundir parte de um registo suspeito torna-o suspeito")
	}
}

// R4 — registo com entradas e chave perdida: FALHA, nunca cria outra, nunca escreve; repor a chave
// recupera.
func TestAOS436R2_ChavePerdidaNaoSeRecria(t *testing.T) {
	const titular = "nhi:apagado-antes"
	dir := t.TempDir()
	proprio := filepath.Join(dir, "apagamentos-dsar.txt")
	store := audit.NewMemStore()
	cadeiaComApagamento(t, store, titular, instanteDaDestruicao)
	fv := novoVaultComIdades()
	srv := novoServidor(t, fv)
	n1, _ := noComVault(t, store, srv, func(c *Config) { c.DSARErasureRegister = proprio })
	_ = n1.Close()
	chave, err := os.ReadFile(proprio + sufixoDaChaveDoRegisto)
	if err != nil {
		t.Fatalf("PRECONDICAO: o 1.o arranque devia ter criado a chave e escrito a entrada da cadeia: %v", err)
	}
	if err := os.Remove(proprio + sufixoDaChaveDoRegisto); err != nil {
		t.Fatal(err)
	}
	antes, _ := os.ReadFile(proprio)

	n2, v2 := noComVault(t, store, srv, func(c *Config) { c.DSARErasureRegister = proprio })
	if err := v2.ready(context.Background()); err == nil || !strings.Contains(err.Error(), "tem entradas e a chave") {
		t.Fatalf("registo com entradas e sem chave devia FALHAR a dize-lo; veio %v", err)
	}
	if _, err := os.Stat(proprio + sufixoDaChaveDoRegisto); !os.IsNotExist(err) {
		t.Fatal("o no CRIOU uma chave nova por cima de um registo com entradas")
	}
	v2.Delete("nhi:outro") // uma destruição com a chave perdida fica pendente, não escrita
	if depois, _ := os.ReadFile(proprio); !bytes.Equal(antes, depois) {
		t.Fatal("com a chave perdida o no ESCREVEU no registo")
	}
	_ = n2.Close()

	// RECUPERAÇÃO documentada: repor a chave do bundle mais recente.
	if err := os.WriteFile(proprio+sufixoDaChaveDoRegisto, chave, 0o600); err != nil {
		t.Fatal(err)
	}
	_, v3 := noComVault(t, store, srv, func(c *Config) { c.DSARErasureRegister = proprio })
	if err := v3.ready(context.Background()); err != nil {
		t.Fatalf("reposta a chave, a reconciliacao devia provar-se; veio %v", err)
	}
}

// R5 — restauro de um bundle sem a chave, com importação pedida: nada é criado nem escrito; seguir a
// indicação (copiar a chave do bundle mais recente) recupera.
func TestAOS436R2_ImportadoSemChaveNaoEscreveNadaEARecuperacaoFunciona(t *testing.T) {
	const titular = "nhi:apagado-no-bundle-antigo"
	nome := nomeDaKEK(titular)
	dir := t.TempDir()
	d1, d2 := filepath.Join(dir, "recente"), filepath.Join(dir, "restaurado")
	for _, d := range []string{d1, d2} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	recente := filepath.Join(d1, "apagamentos-dsar.txt")
	escreverComAChaveDe(t, recente, recente, entradaDeApagamento{nome: nome, destruidaEm: instanteDaDestruicao})
	store := audit.NewMemStore()
	cadeiaComApagamento(t, store, titular, instanteDaDestruicao)
	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaAntes)
	srv := novoServidor(t, fv)
	restaurado := filepath.Join(d2, "apagamentos-dsar.txt")
	cfg := func(c *Config) { c.DSARErasureRegister = restaurado; c.DSARErasureRegisterImport = recente }

	n1, v1 := noComVault(t, store, srv, cfg)
	if err := v1.ready(context.Background()); err == nil || !strings.Contains(err.Error(), "NAO existe e ha uma importacao pedida") {
		t.Fatalf("sem a chave, o importado nao autentica e isso tem de ser dito; veio %v", err)
	}
	for _, f := range []string{restaurado, restaurado + sufixoDaChaveDoRegisto} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("sem a chave o no criou %s — seguir a indicacao depois ja nao recuperaria", f)
		}
	}
	if fv.existe(nome) {
		t.Error("a CADEIA conhece o apagamento: a KEK devia ter sido destruida na mesma")
	}
	_ = n1.Close()

	fv.restaurar(nome, nascidaAntes)
	k, _ := os.ReadFile(recente + sufixoDaChaveDoRegisto)
	if err := os.WriteFile(restaurado+sufixoDaChaveDoRegisto, k, 0o600); err != nil {
		t.Fatal(err)
	}
	_, v2 := noComVault(t, store, srv, cfg)
	if err := v2.ready(context.Background()); err != nil {
		t.Fatalf("copiada a chave certa, a reconciliacao devia provar-se; veio %v", err)
	}
}

// R3 — depois de fundido, o importado deixa de ser exigido: apagá-lo não fecha nada.
func TestAOS436R2_ImportadoFundidoDeixaDeSerExigido(t *testing.T) {
	dir := t.TempDir()
	proprio := filepath.Join(dir, "apagamentos-dsar.txt")
	importado := filepath.Join(dir, "importado.txt")
	escreverComAChaveDe(t, proprio, importado, entradaDeApagamento{nome: nomeDaKEK("nhi:x"), destruidaEm: instanteDaDestruicao})
	fv := novoVaultComIdades()
	fv.restaurar(nomeDaKEK("nhi:y"), nascidaAntes) // o Vault tem chaves: há LIST a fazer
	node, vault := noComVault(t, audit.NewMemStore(), novoServidor(t, fv), func(c *Config) {
		c.DSARErasureRegister = proprio
		c.DSARErasureRegisterImport = importado
	})
	svc, _ := newAPI(t, node)
	if err := os.Remove(importado); err != nil { // a limpeza de /tmp depois do restauro
		t.Fatal(err)
	}
	svc.RefreshVaultTokenNow(context.Background())
	if err := vault.ready(context.Background()); err != nil {
		t.Fatalf("apagar o importado DEPOIS de fundido fechou o no; veio %v", err)
	}
	if _, _, err := vault.WrapDEK("nhi:c", []byte("d")); err != nil {
		t.Errorf("o conteudo devia continuar aberto; veio %v", err)
	}
}

// R7 — a chave nasce atómica: 32 bytes, 0600, sem temporários à vista; uma chave curta existente
// não é substituída.
func TestAOS436R2_ChaveNasceAtomica(t *testing.T) {
	dir := t.TempDir()
	proprio := filepath.Join(dir, "apagamentos-dsar.txt")
	regCom(t, proprio)
	raw, err := os.ReadFile(proprio + sufixoDaChaveDoRegisto)
	if err != nil || len(raw) != 32 {
		t.Fatalf("chave criada com %d bytes (%v)", len(raw), err)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temporario da chave ficou para tras: %s", e.Name())
		}
	}
	curta := filepath.Join(t.TempDir(), "r.txt")
	if err := os.WriteFile(curta+sufixoDaChaveDoRegisto, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := novoRegistoDeApagamentos(curta, true)
	r.mu.Lock()
	err = r.prepararChave()
	r.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "tem 0 bytes") {
		t.Fatalf("uma chave curta devia ser erro nomeado, nunca substituida; veio %v", err)
	}
}

// G — o MAC impede RE-DATAR uma linha legítima. Sem ele, mover o instante de uma linha verdadeira
// para depois do regresso do titular destruía a KEK nova — dados legítimos.
func TestAOS436R2_LinhaLegitimaReDatadaNaoDestroi(t *testing.T) {
	nome := nomeDaKEK("nhi:voltou")
	dir := t.TempDir()
	proprio := filepath.Join(dir, "apagamentos-dsar.txt")
	importado := filepath.Join(dir, "importado.txt")
	escreverComAChaveDe(t, proprio, importado, entradaDeApagamento{nome: nome, destruidaEm: instanteDaDestruicao})
	raw, _ := os.ReadFile(importado)
	nova := instanteDaDestruicao.Add(3 * time.Hour).Format(time.RFC3339)
	_ = os.WriteFile(importado, []byte(strings.Replace(string(raw), instanteDaDestruicao.Format(time.RFC3339), nova, 1)), 0o600)
	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaDepois) // o titular voltou 2h depois do apagamento
	noComVault(t, audit.NewMemStore(), novoServidor(t, fv), func(c *Config) {
		c.DSARErasureRegister = proprio
		c.DSARErasureRegisterImport = importado
	})
	if !fv.existe(nome) {
		t.Fatal("uma linha legitima RE-DATADA destruiu a KEK nova de um titular que voltou")
	}
}

// H-a — «não abre» tem duas causas. Portão fechado ou Vault sem resposta sai como
// durable.ErrConteudoIndisponivel (o step-ledger falha fechado, não esquece o passo); só a KEK
// destruída sai como audit.ErrDecrypt (apagado, 410).
func TestAOS436R2_IndisponivelNaoEApagado(t *testing.T) {
	const titular = "nhi:com-passos"
	store := audit.NewMemStore()
	cadeiaComApagamento(t, store, "nhi:outro-apagado", instanteDaDestruicao)
	fv := novoVaultComIdades()
	node, _ := noComVault(t, store, novoServidor(t, fv), nil)
	svc, _ := newAPI(t, node)
	ctx := context.Background()
	sealer := node.contentOpener.(*contentSealer)
	selado, err := sealer.SealContent(ctx, titular, "run-1", []byte("resultado de um efeito externo"))
	if err != nil {
		t.Fatal(err)
	}

	// (1) Portão fechado.
	fv.define(func(f *vaultComIdades) { f.indisponivel = true })
	svc.RefreshVaultTokenNow(ctx)
	_, err = sealer.OpenContent(ctx, titular, selado)
	if !errors.Is(err, durable.ErrConteudoIndisponivel) || errors.Is(err, audit.ErrDecrypt) {
		t.Fatalf("portao fechado devia sair como INDISPONIVEL (e nunca como apagado); veio %v", err)
	}
	if code := reconstructErrorStatus(err); code != http.StatusServiceUnavailable {
		t.Errorf("o replay soberano devia responder 503, nao %d", code)
	}

	// (2) Portão aberto, mas o decrypt falha com a KEK viva (Vault a falhar só nessa operação).
	fv.define(func(f *vaultComIdades) { f.indisponivel = false })
	svc.RefreshVaultTokenNow(ctx)
	_, err = sealer.OpenContent(ctx, titular, []byte("lixo-nao-e-um-envelope"))
	if !errors.Is(err, durable.ErrConteudoIndisponivel) {
		t.Fatalf("com a KEK VIVA, um desembrulho falhado nao e um apagamento; veio %v", err)
	}

	// (3) KEK destruída: isso sim é apagado.
	node.DSARVault.Delete(titular)
	_, err = sealer.OpenContent(ctx, titular, selado)
	if !errors.Is(err, audit.ErrDecrypt) || errors.Is(err, durable.ErrConteudoIndisponivel) {
		t.Fatalf("KEK destruida devia sair como APAGADO (ErrDecrypt); veio %v", err)
	}
}

// H-d — os erros e o /readyz não nomeiam a KEK pelo nome do Vault (invertível por dicionário).
func TestAOS436R2_ErrosNaoNomeiamAKEK(t *testing.T) {
	const titular = "nhi:teimosa"
	nome := nomeDaKEK(titular)
	store := audit.NewMemStore()
	cadeiaComApagamento(t, store, titular, instanteDaDestruicao)
	fv := novoVaultComIdades()
	fv.restaurar(nome, nascidaAntes)
	fv.define(func(f *vaultComIdades) { f.shredFalha = true })
	_, vault := noComVault(t, store, novoServidor(t, fv), nil)
	vault.Delete(titular) // o Vault aceita o DELETE e a chave sobrevive: fica por confirmar

	err := vault.ready(context.Background())
	if err == nil {
		t.Fatal("PRECONDICAO: a re-destruicao falhada devia deixar a custodia vermelha")
	}
	_, _, werr := vault.WrapDEK(titular, []byte("d"))
	if werr == nil {
		t.Fatal("PRECONDICAO: a KEK ressuscitada que nao morreu devia estar fechada no portao")
	}
	for _, msg := range []string{err.Error(), werr.Error(), vault.shredConfirmed(titular).Error()} {
		if strings.Contains(msg, "aos-kek-") || strings.Contains(msg, strings.TrimPrefix(nome, "aos-kek-")) {
			t.Fatalf("um erro nomeia a KEK pelo nome do Vault (sha256 de um keyRef publico): %q", msg)
		}
	}
}

// N4 (terceira revisão) — a cadeia e o registo do MESMO tar podem não coincidir: o tar lê os
// ficheiros pela ordem do directório, e um apagamento entre as duas leituras deixa a cadeia a saber
// de um id que o registo ainda não tem. Importar o registo desse mesmo bundle NÃO pode ser recusado
// por isso — e o que só a cadeia sabe é reconciliado pela cadeia na mesma.
func TestAOS436R3_RegistoDoMesmoBundleAtrasadoFaceACadeiaEAceite(t *testing.T) {
	const titularX = "nhi:X-so-na-cadeia"
	nX, nY := nomeDaKEK(titularX), nomeDaKEK("nhi:Y")
	dir := t.TempDir()
	proprio := filepath.Join(dir, "apagamentos-dsar.txt")
	importado := filepath.Join(dir, "importado.txt")
	escreverComAChaveDe(t, proprio, proprio, entradaDeApagamento{nome: nY, destruidaEm: instanteDaDestruicao})
	raw, err := os.ReadFile(proprio)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(importado, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store := audit.NewMemStore()
	cadeiaComApagamento(t, store, titularX, instanteDaDestruicao)

	fv := novoVaultComIdades()
	fv.restaurar(nX, nascidaAntes)
	fv.restaurar(nY, nascidaAntes)
	_, vault := noComVault(t, store, novoServidor(t, fv), func(c *Config) {
		c.DSARErasureRegister = proprio
		c.DSARErasureRegisterImport = importado
	})
	if err := vault.ready(context.Background()); err != nil {
		t.Fatalf("o registo do MESMO bundle, atrasado face a cadeia, devia ser aceite; veio %v", err)
	}
	if fv.existe(nX) || fv.existe(nY) {
		t.Fatalf("as duas KEKs ressuscitadas deviam ter sido re-destruidas: X=%v Y=%v", fv.existe(nX), fv.existe(nY))
	}
}
