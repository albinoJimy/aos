package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/pdp"
)

// TestRun cobre o caminho de flags/entrada da ferramenta (run) num directório
// temporário com a política de referência.
func TestRun(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile(filepath.Join("..", "..", "policies", "aos_authz.cedar"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aos_authz.cedar"), src, 0o644); err != nil {
		t.Fatal(err)
	}

	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("policy-sign", flag.ContinueOnError)
	// -key explícito no tempdir: mantém o teste hermético (o default é agora
	// ~/.aos/keys/signing.key, fora do repo, que não devemos poluir nos testes).
	os.Args = []string{"policy-sign", "-dir", dir, "-version", "3.0.0", "-key", filepath.Join(dir, "signing.key")}

	if err := run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	p, err := pdp.Open(dir)
	if err != nil {
		t.Fatalf("Open apos run: %v", err)
	}
	if p.Version() != "3.0.0" {
		t.Errorf("Version()=%q, esperava 3.0.0", p.Version())
	}
}

// TestRun_DefaultKeyPathForaDoRepo assevera que, sem -key, a chave privada é
// materializada FORA do repo em ~/.aos/keys/signing.key (finding secrets): o home
// é redireccionado para um tempdir para o teste ser hermético e não poluir o home
// real. Cobre defaultKeyPath e a criação do directório-pai da chave.
func TestRun_DefaultKeyPathForaDoRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home) // Windows
	t.Setenv("HOME", home)        // POSIX

	dir := t.TempDir()
	src, err := os.ReadFile(filepath.Join("..", "..", "policies", "aos_authz.cedar"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aos_authz.cedar"), src, 0o644); err != nil {
		t.Fatal(err)
	}

	oldArgs, oldFS := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
	flag.CommandLine = flag.NewFlagSet("policy-sign", flag.ContinueOnError)
	os.Args = []string{"policy-sign", "-dir", dir, "-version", "1.0.0"} // sem -key ⇒ default

	if err := run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	keyPath := filepath.Join(home, ".aos", "keys", "signing.key")
	if _, err := os.Stat(keyPath); err != nil {
		t.Errorf("chave privada devia estar fora do repo em %s: %v", keyPath, err)
	}
	// A chave NÃO deve ser materializada dentro do dir do bundle (repo).
	if _, err := os.Stat(filepath.Join(dir, "signing.key")); !os.IsNotExist(err) {
		t.Errorf("chave privada NAO devia estar na arvore do bundle: err=%v", err)
	}
}

// TestSign_GeraAssinaEVerifica exercita o fluxo da ferramenta: gera par de
// chaves, assina o bundle e verifica-o (Open). Confirma que a chave privada e o
// trust anchor são escritos e que o bundle assinado carrega.
func TestSign_GeraAssinaEVerifica(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile(filepath.Join("..", "..", "policies", "aos_authz.cedar"))
	if err != nil {
		t.Fatalf("ler politica: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aos_authz.cedar"), src, 0o644); err != nil {
		t.Fatal(err)
	}

	keyPath := filepath.Join(dir, "signing.key")
	m, ver, err := sign(dir, "1.2.3", keyPath)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if m.PolicyVersion != "1.2.3" || ver != "1.2.3" {
		t.Errorf("versao inesperada: manifest=%q open=%q", m.PolicyVersion, ver)
	}
	if m.ContentHash == "" {
		t.Error("content_hash vazio")
	}

	for _, f := range []string{"manifest.json", "aos_authz.sig", "trust_anchor.pub", "signing.key"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("esperava %s escrito: %v", f, err)
		}
	}

	// Segunda assinatura reutiliza a chave existente (não regenera) e sobe versão.
	if _, _, err := sign(dir, "1.2.4", keyPath); err != nil {
		t.Fatalf("re-sign com chave existente: %v", err)
	}

	// Bundle final verifica.
	p, err := pdp.Open(dir)
	if err != nil {
		t.Fatalf("Open final: %v", err)
	}
	if p.Version() != "1.2.4" {
		t.Errorf("Version()=%q, esperava 1.2.4", p.Version())
	}
}

// TestSign_AvaliacaoDeFumoRejeitaAtributoForaDoMapa é o TESTE-VENENO de AOS-378 (alínea c):
// um bundle cuja regra refira um atributo FORA do mapa fixo do motor (context.reversibility,
// resource.type) COMPILA e ASSINA sem se queixar, mas em runtime daria diag.Errors ⇒ deny de
// tudo. A avaliação de fumo acrescentada a [sign] tem de o apanhar: sign devolve erro e o ramo
// que imprime "verificacao OK" NÃO é alcançado (a ferramenta sai != 0). Falha-antes: sem a
// sonda, sign devolvia nil e a ferramenta declarava-se verde sobre um bundle que nega tudo.
//
// O fixture vive num tempdir com chave de teste; NÃO toca o bundle real nem a chave privada.
func TestSign_AvaliacaoDeFumoRejeitaAtributoForaDoMapa(t *testing.T) {
	casos := []struct {
		nome  string
		regra string
	}{
		{
			"context.reversibility fora do mapa",
			`@id("poison_reversibility")
permit ( principal, action == Action::"cap:http.post", resource )
when { context.reversibility == "reversible" };`,
		},
		{
			"resource.type fora do mapa",
			`@id("poison_resource_type")
permit ( principal, action == Action::"cap:http.post", resource )
when { resource.type == "url" };`,
		},
		// Os quatro ESCAPES que a sonda DINÂMICA deixava passar (revisão adversarial AOS-378):
		// o atributo mau vive atrás de um scope `action in [...]` que a extracção de acção não
		// casava, ou de um `&&` sobre um atributo mapeado cujo valor a semente não satisfazia
		// (curto-circuito). A verificação ESTÁTICA apanha-os na mesma — não depende de alcançar
		// o `when`.
		{
			"action in [...] (scope de accao multipla)",
			`@id("poison_action_in")
permit ( principal, action in [Action::"cap:http.post", Action::"cap:http.put"], resource )
when { context.reversibility == "reversible" };`,
		},
		{
			"curto-circuito por sensitivity nao-semeada",
			`@id("poison_shortcircuit_sensitivity")
permit ( principal, action == Action::"cap:http.post", resource )
when { context.sensitivity == "confidential" && context.reversibility == "reversible" };`,
		},
		{
			"curto-circuito por authority nao-semeada",
			`@id("poison_shortcircuit_authority")
permit ( principal, action == Action::"cap:http.post", resource )
when { principal.authority.contains("cap:some.other") && context.reversibility == "reversible" };`,
		},
		{
			"curto-circuito por region nao-semeada",
			`@id("poison_shortcircuit_region")
permit ( principal, action == Action::"cap:http.post", resource )
when { resource.region == "us" && context.reversibility == "reversible" };`,
		},
		// Os DOIS buracos da 2ª revisão adversarial: formas que a regex de ponto ingénua deixava
		// escapar e que erram em runtime. Escondidos atrás de `action in`/curto-circuito (que a
		// sonda dinâmica também falha) ⇒ só a análise estática os apanha.
		{
			"indice de chave nao-mapeada (brackets preservados)",
			`@id("poison_bracket")
permit ( principal, action == Action::"cap:http.post", resource )
when { context["foo-bar"] == "x" };`,
		},
		{
			"indice + action in (escape composto)",
			`@id("poison_bracket_action_in")
permit ( principal, action in [Action::"cap:http.post", Action::"cap:http.put"], resource )
when { context["foo-bar"] == "x" };`,
		},
		{
			"acesso encadeado sobre atributo mapeado",
			`@id("poison_chained")
permit ( principal, action == Action::"cap:http.post", resource )
when { context.taint.foo == "x" };`,
		},
		{
			"encadeado atras de curto-circuito",
			`@id("poison_chained_shortcircuit")
permit ( principal, action == Action::"cap:http.post", resource )
when { context.sensitivity == "confidential" && context.taint.foo == "x" };`,
		},
		// 3ª revisão adversarial: método de SET sobre um atributo STRING (só principal.authority
		// é Set; region/taint/sensitivity são String). `.contains(...)` sobre uma String compila
		// mas erra em runtime («expected set, got string») ⇒ deny-all. Isolado e escondido atrás
		// dos escapes que enganam a sonda dinâmica.
		{
			"set-method sobre context.taint (String)",
			`@id("poison_setmethod_taint")
permit ( principal, action == Action::"cap:http.post", resource )
when { context.taint.contains("tag") };`,
		},
		{
			"set-method sobre region atras de curto-circuito",
			`@id("poison_setmethod_region_sc")
permit ( principal, action == Action::"cap:http.post", resource )
when { resource.region == "us" && context.taint.contains("tag") };`,
		},
		{
			"set-method sobre sensitivity via action in",
			`@id("poison_setmethod_sensitivity_actionin")
permit ( principal, action in [Action::"cap:http.post", Action::"cap:http.put"], resource )
when { context.sensitivity.containsAny(["x"]) };`,
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "poison.cedar"), []byte(c.regra), 0o644); err != nil {
				t.Fatal(err)
			}
			keyPath := filepath.Join(dir, "signing.key")

			// (a) sign devolve erro DE AVALIAÇÃO (não de compilação nem de assinatura): o
			// bundle compilou e a assinatura verificou; foi a sonda de fumo que o barrou.
			_, _, err := sign(dir, "1.0.0", keyPath)
			if err == nil {
				t.Fatal("sign devia falhar: a regra refere um atributo fora do mapa fixo do motor (nega em runtime)")
			}
			if !strings.Contains(err.Error(), "avaliacao de fumo") {
				t.Errorf("erro=%v, esperava falha da avaliacao de fumo (não de compilação/assinatura)", err)
			}

			// (b) ao nível da ferramenta (run): captura stdout e prova que "verificacao OK"
			// NÃO é impresso e que run devolve erro (⇒ os.Exit(1) em main).
			oldArgs, oldFS := os.Args, flag.CommandLine
			defer func() { os.Args, flag.CommandLine = oldArgs, oldFS }()
			flag.CommandLine = flag.NewFlagSet("policy-sign", flag.ContinueOnError)
			os.Args = []string{"policy-sign", "-dir", dir, "-version", "1.0.1", "-key", keyPath}

			out := captureStdout(t, func() {
				if rerr := run(); rerr == nil {
					t.Error("run devia devolver erro (avaliacao de fumo falhou) ⇒ exit != 0")
				}
			})
			if strings.Contains(out, "verificacao OK") {
				t.Errorf("stdout continha \"verificacao OK\" apesar de a avaliacao de fumo falhar: %q", out)
			}
		})
	}
}

// TestSign_AvaliacaoDeFumoAceitaBundleReal é o CONTROLO NEGATIVO da alínea (c): a política de
// referência committada (os 2 permits que só usam atributos do mapa fixo) continua a assinar,
// verificar E a passar a avaliação de fumo — sem qualquer alteração. Prova que a sonda não
// produz falsos positivos sobre o bundle real.
func TestSign_AvaliacaoDeFumoAceitaBundleReal(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile(filepath.Join("..", "..", "policies", "aos_authz.cedar"))
	if err != nil {
		t.Fatalf("ler politica: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aos_authz.cedar"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sign(dir, "1.0.0", filepath.Join(dir, "signing.key")); err != nil {
		t.Fatalf("bundle real recusado pela avaliacao de fumo (falso positivo): %v", err)
	}
}

// captureStdout redirecciona os.Stdout enquanto fn corre e devolve o que fn imprimiu.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	_ = w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

// TestLoadOrGenerateKey_Branches cobre carregar chave existente, gerar nova, e
// os erros de chave malformada.
func TestLoadOrGenerateKey_Branches(t *testing.T) {
	dir := t.TempDir()

	// 1) Gera nova (ficheiro inexistente): escreve chave privada + trust anchor.
	kp := filepath.Join(dir, "signing.key")
	priv, err := loadOrGenerateKey(kp, dir)
	if err != nil {
		t.Fatalf("gerar: %v", err)
	}
	if len(priv) != ed25519.PrivateKeySize {
		t.Fatalf("chave gerada com tamanho invalido: %d", len(priv))
	}
	if _, err := os.Stat(filepath.Join(dir, "trust_anchor.pub")); err != nil {
		t.Errorf("trust anchor devia existir: %v", err)
	}

	// 2) Carrega a chave existente: devolve a mesma.
	got, err := loadOrGenerateKey(kp, dir)
	if err != nil {
		t.Fatalf("carregar: %v", err)
	}
	if base64.StdEncoding.EncodeToString(got) != base64.StdEncoding.EncodeToString(priv) {
		t.Error("carregar devia devolver a mesma chave")
	}

	// 3) base64 invalido.
	bad := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(bad, []byte("nao-e-base64!!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrGenerateKey(bad, dir); err == nil || !strings.Contains(err.Error(), "base64") {
		t.Errorf("esperava erro de base64, obtive %v", err)
	}

	// 4) tamanho invalido (base64 valido mas poucos bytes).
	short := filepath.Join(dir, "short.key")
	if err := os.WriteFile(short, []byte(base64.StdEncoding.EncodeToString([]byte("curto"))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrGenerateKey(short, dir); err == nil || !strings.Contains(err.Error(), "tamanho") {
		t.Errorf("esperava erro de tamanho, obtive %v", err)
	}
}
