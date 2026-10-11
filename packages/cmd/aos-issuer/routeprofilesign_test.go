package main

// AOS-519 — `aos-issuer route-profile-sign` e `route-profile-anchor`.
//
// O que se prova: a assinatura que o subcomando produz é a que o NÓ aceita (a pasta carrega com a
// âncora que o subcomando imprime); e o subcomando não assina o que o nó recusaria — com um
// veredicto `recusado`, `inconclusivo` ou de um ensaio não fica ficheiro nenhum para entregar.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
)

// aos519Posto é o posto do dono: a seed num ficheiro, e a pasta com o registo e o relatório por assinar.
type aos519Posto struct {
	dir, seed string
	pub       ed25519.PublicKey
}

func aos519NovoPosto(t *testing.T, relatorio, registo func(map[string]any)) *aos519Posto {
	t.Helper()
	dir := t.TempDir()
	key := rt.NewKey(t)
	seed := filepath.Join(t.TempDir(), "perfis.seed")
	if err := os.WriteFile(seed, []byte(hex.EncodeToString(key.Priv.Seed())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rdoc := rt.ReportDoc(rt.ProfileDigest(t, rt.ProfileJSON))
	if relatorio != nil {
		relatorio(rdoc)
	}
	report, digest := rt.SealReport(t, rdoc)
	edoc := rt.EntryDoc(t, rt.ProfileJSON, rt.Board, rt.Region, digest)
	if registo != nil {
		registo(edoc)
	}
	rt.Write(t, dir, "novo", rt.Marshal(t, edoc), "", report)
	return &aos519Posto{dir: dir, seed: seed, pub: key.Pub}
}

func (p *aos519Posto) caminho(sufixo string) string { return filepath.Join(p.dir, "novo"+sufixo) }

func (p *aos519Posto) assinar(extra ...string) (stdout, stderr string, err error) {
	var out, diag bytes.Buffer
	args := append([]string{"route-profile-sign", "--entry", p.caminho(routeentry.SuffixEntry), "--report", p.caminho(routeentry.SuffixReport), "--key-file", p.seed}, extra...)
	err = run(args, &out, &diag)
	return out.String(), diag.String(), err
}

// O QUE O SUBCOMANDO ASSINA, O NÓ ACEITA — e a âncora é a que o subcomando diz.
func TestAOS519_RouteProfileSign_OQueSeAssinaONoAceita(t *testing.T) {
	p := aos519NovoPosto(t, nil, nil)
	stdout, stderr, err := p.assinar("--out", p.caminho(routeentry.SuffixSignature))
	if err != nil {
		t.Fatalf("route-profile-sign: %v", err)
	}
	if stdout != "" {
		t.Fatalf("com --out o stdout fica vazio; veio %q", stdout)
	}
	ancora := base64.StdEncoding.EncodeToString(p.pub)
	for _, quer := range []string{"ASSINADO", `alias "gpt-4o"`, `"openai/modelo-novo-4.5"`, "fatia inicial 10%", ancora, "nao prova que o banco de ensaio correu"} {
		if !strings.Contains(stderr, quer) {
			t.Errorf("o resumo nao diz %q:\n%s", quer, stderr)
		}
	}
	// A seed nunca aparece em lado nenhum.
	seed, _ := os.ReadFile(p.seed)
	if s := strings.TrimSpace(string(seed)); strings.Contains(stderr, s) || strings.Contains(stdout, s) {
		t.Fatal("a seed da chave apareceu na saida")
	}

	var anchorOut bytes.Buffer
	if err := run([]string{"route-profile-anchor", "--key-file", p.seed}, &anchorOut, &bytes.Buffer{}); err != nil || strings.TrimSpace(anchorOut.String()) != ancora {
		t.Fatalf("route-profile-anchor = %q (%v); quer %q", anchorOut.String(), err, ancora)
	}

	// O nó: a pasta carrega, com a âncora impressa.
	pol, err := allowlist.LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	entradas, err := routeentry.LoadDir(routeentry.Config{Dir: p.dir, TrustAnchor: ancora, Board: rt.Board, Region: rt.Region, Allowlist: pol})
	if err != nil || len(entradas) != 1 || entradas[0].Profile.Requested != "gpt-4o" {
		t.Fatalf("o no nao aceita o que o subcomando assinou: %v", err)
	}

	// Sem --out, a assinatura sai no stdout — e é a mesma.
	noFicheiro, _ := os.ReadFile(p.caminho(routeentry.SuffixSignature))
	stdout, _, err = p.assinar()
	if err != nil || strings.TrimSpace(stdout) != strings.TrimSpace(string(noFicheiro)) {
		t.Fatalf("a assinatura no stdout nao e a do ficheiro: %v", err)
	}
}

// NÃO SE ASSINA O QUE O NÓ RECUSARIA: nenhuma assinatura fica escrita, e o erro diz a causa.
func TestAOS519_RouteProfileSign_NaoAssinaOQueONoRecusaria(t *testing.T) {
	casos := []struct {
		nome, causa        string
		relatorio, registo func(map[string]any)
	}{
		{"veredicto recusado", routeentry.CauseNotQualified, func(d map[string]any) { rt.Qualification(d)["veredicto"] = "recusado" }, nil},
		{"veredicto inconclusivo", routeentry.CauseNotQualified, func(d map[string]any) { rt.Qualification(d)["veredicto"] = "inconclusivo" }, nil},
		{"relatorio de um ensaio", routeentry.CauseVerdictNotForProduction, func(d map[string]any) {
			d["modo"] = "falso"
			rt.Qualification(d)["vale_para_producao"] = false
		}, nil},
		{"relatorio de outro perfil", routeentry.CauseVerdictOfOtherProfile, func(d map[string]any) {
			rt.QualifiedRoute(d)["digest_do_perfil"] = "sha256:" + strings.Repeat("4", 64)
		}, nil},
		{"relatorio que nao e o citado", routeentry.CauseReportNotCited, nil, func(d map[string]any) {
			d["digest_do_relatorio"] = "sha256:" + strings.Repeat("5", 64)
		}},
		{"registo com campo desconhecido", routeentry.CauseEntryUnreadable, nil, func(d map[string]any) { d["titular"] = true }},
		{"fatia fora de 0 a 100", routeentry.CauseShareOutOfRange, nil, func(d map[string]any) { d["fatia_inicial_percentagem"] = 101 }},
		{"digest do perfil errado", routeentry.CauseProfileDigestMismatch, nil, func(d map[string]any) {
			d["digest_do_perfil"] = "sha256:" + strings.Repeat("6", 64)
		}},
		{"imagem do proxy que nao e a declarada", routeentry.CauseProxyImageNotDeclared, nil, func(d map[string]any) {
			d["digest_da_imagem_do_proxy"] = "sha256:" + strings.Repeat("7", 64)
		}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			p := aos519NovoPosto(t, c.relatorio, c.registo)
			sig := p.caminho(routeentry.SuffixSignature)
			stdout, _, err := p.assinar("--out", sig)
			if err == nil || !strings.Contains(err.Error(), "causa="+c.causa) {
				t.Fatalf("tinha de recusar com causa=%s; veio %v", c.causa, err)
			}
			if _, serr := os.Stat(sig); !os.IsNotExist(serr) || stdout != "" {
				t.Fatalf("ficou uma assinatura de um registo que o no recusaria (stdout=%q, ficheiro=%v)", stdout, serr)
			}
		})
	}
}

// A chave vem de um FICHEIRO, e os argumentos são todos obrigatórios.
func TestAOS519_RouteProfileSign_Uso(t *testing.T) {
	p := aos519NovoPosto(t, nil, nil)
	for nome, args := range map[string][]string{
		"sem argumentos":         {"route-profile-sign"},
		"sem --key-file":         {"route-profile-sign", "--entry", p.caminho(routeentry.SuffixEntry), "--report", p.caminho(routeentry.SuffixReport)},
		"sem --report":           {"route-profile-sign", "--entry", p.caminho(routeentry.SuffixEntry), "--key-file", p.seed},
		"flag desconhecida":      {"route-profile-sign", "--key", "0000"},
		"argumento posicional":   {"route-profile-sign", "--entry", p.caminho(routeentry.SuffixEntry), "--report", p.caminho(routeentry.SuffixReport), "--key-file", p.seed, "extra"},
		"registo que nao existe": {"route-profile-sign", "--entry", filepath.Join(p.dir, "nao-existe"), "--report", p.caminho(routeentry.SuffixReport), "--key-file", p.seed},
		"chave que nao existe":   {"route-profile-sign", "--entry", p.caminho(routeentry.SuffixEntry), "--report", p.caminho(routeentry.SuffixReport), "--key-file", filepath.Join(p.dir, "nao-existe")},
		"ancora sem --key-file":  {"route-profile-anchor"},
	} {
		var out bytes.Buffer
		if err := run(args, &out, &bytes.Buffer{}); err == nil || out.Len() != 0 {
			t.Errorf("%s: tinha de falhar sem escrever nada; err=%v stdout=%q", nome, err, out.String())
		}
	}
}

// AS MENSAGENS DE ERRO DA CHAVE são deste subcomando, e não repetem bytes da seed (revisão
// adversarial): o erro do descodificador de hex dizia o byte inválido, que é um byte da seed.
func TestAOS519_RouteProfileSign_OErroDaChaveNaoEcoaASeed(t *testing.T) {
	p := aos519NovoPosto(t, nil, nil)
	boa, _ := os.ReadFile(p.seed)
	semente := strings.TrimSpace(string(boa))
	casos := map[string]string{
		"um caracter que nao e hex": semente[:20] + "Z" + semente[21:],
		"curta":                     semente[:40],
		"com lixo no fim":           semente + "!!",
	}
	for nome, conteudo := range casos {
		if err := os.WriteFile(p.seed, []byte(conteudo), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"route-profile-sign", "--entry", p.caminho(routeentry.SuffixEntry), "--report", p.caminho(routeentry.SuffixReport), "--key-file", p.seed},
			{"route-profile-anchor", "--key-file", p.seed},
		} {
			var out, diag bytes.Buffer
			err := run(args, &out, &diag)
			if err == nil || out.Len() != 0 {
				t.Fatalf("%s (%s): tinha de falhar sem assinar; err=%v", nome, args[0], err)
			}
			tudo := err.Error() + diag.String()
			if strings.Contains(tudo, "approve-sign") || strings.Contains(tudo, "aprovador") {
				t.Errorf("%s (%s): a mensagem e de outro subcomando: %v", nome, args[0], err)
			}
			// Nem a seed, nem um troço dela, nem o byte que o descodificador rejeitou.
			for _, proibido := range []string{semente, semente[:16], "U+005A", "'Z'", "invalid byte", "!!"} {
				if strings.Contains(tudo, proibido) {
					t.Errorf("%s (%s): a saida repete material da seed (%q): %s", nome, args[0], proibido, tudo)
				}
			}
		}
	}
}

// O --OUT NÃO ESCREVE POR CIMA do registo, do relatório nem da chave — pelo mesmo caminho, por
// outra caixa, ou por um caminho relativo que vai dar ao mesmo sítio.
func TestAOS519_RouteProfileSign_OOutNaoEscrevePorCimaDasEntradas(t *testing.T) {
	p := aos519NovoPosto(t, nil, nil)
	registo, relatorio := p.caminho(routeentry.SuffixEntry), p.caminho(routeentry.SuffixReport)
	alvos := map[string]string{
		"--entry":                 registo,
		"--report":                relatorio,
		"--key-file":              p.seed,
		"--entry por outro nome":  filepath.Join(p.dir, ".", "sub", "..", "novo"+routeentry.SuffixEntry),
		"--entry com outra caixa": filepath.Join(p.dir, "NOVO"+routeentry.SuffixEntry),
	}
	for nome, alvo := range alvos {
		antes := map[string][]byte{}
		for _, f := range []string{registo, relatorio, p.seed} {
			antes[f], _ = os.ReadFile(f)
		}
		_, _, err := p.assinar("--out", alvo)
		if err == nil || !strings.Contains(err.Error(), "--out e o mesmo ficheiro") {
			t.Fatalf("%s: tinha de recusar; veio %v", nome, err)
		}
		for f, conteudo := range antes {
			if depois, _ := os.ReadFile(f); !bytes.Equal(depois, conteudo) {
				t.Fatalf("%s: o ficheiro %s foi alterado", nome, filepath.Base(f))
			}
		}
	}
}

// O DONO ASSINA O QUE VÊ: o resumo mostra os parâmetros do pedido, a versão da projecção e a
// fatia, e sai ANTES de a chave ser lida — com a chave errada, o resumo já lá está.
func TestAOS519_RouteProfileSign_OResumoMostraOQueSeAssina(t *testing.T) {
	comTudo := strings.Replace(rt.ProfileJSON, `"capabilities":["tools"]`, `"capabilities":["tools"],"params":{"max_tokens":16000,"reasoning_effort":"high"},"projection_version":"1.2.0"`, 1)
	dir := t.TempDir()
	key := rt.NewKey(t)
	seed := filepath.Join(t.TempDir(), "perfis.seed")
	if err := os.WriteFile(seed, []byte(hex.EncodeToString(key.Priv.Seed())), 0o600); err != nil {
		t.Fatal(err)
	}
	report, digest := rt.SealReport(t, rt.ReportDoc(rt.ProfileDigest(t, comTudo)))
	edoc := rt.EntryDoc(t, comTudo, rt.Board, rt.Region, digest)
	edoc["fatia_inicial_percentagem"] = 25
	rt.Write(t, dir, "novo", rt.Marshal(t, edoc), "", report)
	args := []string{"route-profile-sign", "--entry", filepath.Join(dir, "novo"+routeentry.SuffixEntry), "--report", filepath.Join(dir, "novo"+routeentry.SuffixReport)}

	var out, diag bytes.Buffer
	if err := run(append(args, "--key-file", seed), &out, &diag); err != nil {
		t.Fatal(err)
	}
	for _, quer := range []string{"VAI ASSINAR", "max_tokens=16000", "reasoning_effort=high", "projection_version: 1.2.0", "fatia inicial 25%", "devolver: nunca", `modelo esperado "openai/modelo-novo-4.5"`} {
		if !strings.Contains(diag.String(), quer) {
			t.Errorf("o resumo nao mostra %q:\n%s", quer, diag.String())
		}
	}
	if strings.Index(diag.String(), "VAI ASSINAR") > strings.Index(diag.String(), "ASSINADO —") {
		t.Error("o resumo tinha de sair antes da confirmacao da assinatura")
	}
	// Com uma chave que não se lê: o resumo já saiu, e não há assinatura.
	out.Reset()
	diag.Reset()
	if err := run(append(args, "--key-file", filepath.Join(dir, "nao-existe")), &out, &diag); err == nil || out.Len() != 0 {
		t.Fatalf("sem chave tinha de falhar: %v", err)
	}
	if !strings.Contains(diag.String(), "VAI ASSINAR") || !strings.Contains(diag.String(), "max_tokens=16000") {
		t.Fatalf("o resumo tinha de sair antes de a chave ser lida:\n%s", diag.String())
	}
	// Um perfil sem parâmetros nem versão di-lo por extenso.
	p := aos519NovoPosto(t, nil, nil)
	_, stderr, err := p.assinar()
	if err != nil || !strings.Contains(stderr, "params do pedido: nenhum") || !strings.Contains(stderr, "nao declara (vale a do no)") {
		t.Fatalf("resumo de um perfil simples: %v\n%s", err, stderr)
	}
}
