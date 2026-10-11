package bancoensaio

// AOS-517 — O PROXY QUE SERVE É, PELO DIGEST, O PROXY EM QUE SE MEDE.
//
// O banco de ensaio e os gates opcionais atrás do proxy correm contra uma imagem fixada pelo
// digest. O compose de produção referia a mesma imagem por uma tag móvel: media-se num proxy e
// servia-se por outro, e nada ficava vermelho quando os dois se afastavam. O proxy transforma
// os pedidos, e a transformação muda com a versão — uma rota qualificada noutra imagem não diz
// nada da que serve.
//
// A DECLARAÇÃO é uma só: [ImagemDoProxy], em proxy.go. Os outros sítios não a podem importar
// (o compose é YAML; o teste do gateway vive noutro módulo, que este módulo-folha importa e não
// o contrário; os guiões de CI são shell), e por isso este teste é o que os prende a ela:
//
//	1. deploy/server/docker-compose.prod.yml — a linha `image:` do serviço `litellm`;
//	2. packages/qa/banco-ensaio/proxy.go — a declaração, e a sua forma (`@sha256:` + 64 hex);
//	3. packages/platform/model-gateway/route_aos505_proxyreal_test.go — `aos505ImagemDoProxy`;
//	4. scripts/ci/banco-ensaio-proxy.sh — lê a constante 2, e não traz imagem escrita à mão;
//	5. scripts/ci/wire-live.sh — lê a constante 3, idem;
//	6. scripts/ci/rota-live.sh — lê a constante 3, idem;
//	7. a VARREDURA: toda a referência à imagem do proxy, por tag ou por digest, em
//	   deploy/server, scripts/ci, docs/runbooks e nos dois módulos que lançam o proxy. É o que
//	   apanha um sítio novo — um guião, um runbook — que ninguém acrescentou à lista acima.
//
// Fora da varredura, de propósito: `deploy/node/dev-hardened/` (o ambiente de desenvolvimento,
// que não é o proxy de produção nem o da medição) e `docs/reports/` (registos datados do que se
// mediu num dia; não se reescrevem quando o digest muda).

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Os ficheiros dos sítios nomeados, relativos à raiz do repositório.
const (
	aos517Compose    = "deploy/server/docker-compose.prod.yml"
	aos517FonteBanco = "packages/qa/banco-ensaio/proxy.go"
	aos517FonteGW    = "packages/platform/model-gateway/route_aos505_proxyreal_test.go"
	aos517Runbook    = "docs/runbooks/PROC-BANCO-DE-ENSAIO.md"
)

// aos517Guioes são os guiões de CI que lançam o proxy: cada um lê a imagem de UMA constante Go.
var aos517Guioes = []struct{ guiao, constante, fonte string }{
	{"scripts/ci/banco-ensaio-proxy.sh", "ImagemDoProxy", "$REPO_ROOT/$MOD/proxy.go"},
	{"scripts/ci/wire-live.sh", "aos505ImagemDoProxy", "$REPO_ROOT/$MOD/route_aos505_proxyreal_test.go"},
	{"scripts/ci/rota-live.sh", "aos505ImagemDoProxy", "$REPO_ROOT/$MOD/route_aos505_proxyreal_test.go"},
}

// aos517RaizesDaVarredura são as pastas onde uma referência à imagem do proxy tem de ser a
// declarada.
var aos517RaizesDaVarredura = []string{
	"deploy/server",
	"scripts/ci",
	"docs/runbooks",
	"packages/qa/banco-ensaio",
	"packages/platform/model-gateway",
}

// aos517MinimoDeFicheiros é o piso da varredura: os quatro ficheiros que hoje trazem a
// referência escrita (compose, as duas fontes Go, o runbook do banco). Abaixo disto a varredura
// não está a ler o que diz ler, e um verde seria vazio.
const aos517MinimoDeFicheiros = 4

var (
	aos517Repositorio = "berriai/" + "litellm"
	// Uma referência à imagem: com ou sem registo, seguida de `:tag` ou de `@digest`.
	aos517ReReferencia = regexp.MustCompile(`(?:ghcr\.io/)?` + regexp.QuoteMeta(aos517Repositorio) + `[:@][A-Za-z0-9_.:@-]*`)
	// A forma que a declaração tem de ter: fixada, e por um SHA-256 inteiro.
	aos517ReFixada  = regexp.MustCompile(`^ghcr\.io/` + regexp.QuoteMeta(aos517Repositorio) + `@sha256:[0-9a-f]{64}$`)
	aos517ReImagem  = regexp.MustCompile(`(?m)^\s+image:\s*(\S+)\s*$`)
	aos517ReServico = regexp.MustCompile(`(?m)^  [A-Za-z0-9_-]+:\s*$`)
)

// aos517Conferencia é o resultado de conferir uma árvore.
type aos517Conferencia struct {
	Declarada   string   // a referência declarada em proxy.go
	Nomeados    int      // sítios nomeados conferidos (compose, duas constantes, três guiões)
	Ocorrencias int      // referências escritas que a varredura leu
	Ficheiros   int      // ficheiros em que as leu
	Falhas      []string // uma linha por divergência; vazio = tudo igual
}

// aos517Conferir confere, na árvore com raiz `raiz`, que todos os sítios referem a imagem do
// proxy pela mesma referência fixada. Não usa a constante compilada: lê tudo dos ficheiros, para
// que os testes a possam correr sobre uma cópia mutada.
func aos517Conferir(raiz string) aos517Conferencia {
	var c aos517Conferencia
	falha := func(f string, a ...any) { c.Falhas = append(c.Falhas, fmt.Sprintf(f, a...)) }
	ler := func(rel string) (string, bool) {
		b, err := os.ReadFile(filepath.Join(raiz, filepath.FromSlash(rel)))
		if err != nil {
			// FALHA, não salto: sem o ficheiro o sítio não é medido.
			falha("%s: não se consegue ler (%v)", rel, err)
			return "", false
		}
		return strings.ReplaceAll(string(b), "\r\n", "\n"), true
	}
	constante := func(rel, nome string) (string, bool) {
		txt, ok := ler(rel)
		if !ok {
			return "", false
		}
		// A MESMA expressão que os guiões usam no `sed`: linha inteira, sem indentação.
		m := regexp.MustCompile(`(?m)^const `+regexp.QuoteMeta(nome)+` = "(.*)"$`).FindAllStringSubmatch(txt, -1)
		if len(m) != 1 {
			falha("%s: esperava UMA declaração `const %s = \"…\"` numa linha só, encontrei %d", rel, nome, len(m))
			return "", false
		}
		return m[0][1], true
	}

	// (2) A declaração.
	decl, ok := constante(aos517FonteBanco, "ImagemDoProxy")
	if !ok {
		return c
	}
	c.Declarada = decl
	c.Nomeados++
	if !aos517ReFixada.MatchString(decl) {
		falha("%s: ImagemDoProxy = %q não é uma referência fixada (ghcr.io/%s@sha256: seguido de 64 hex)",
			aos517FonteBanco, decl, aos517Repositorio)
	}

	// (1) O compose de produção.
	if txt, ok := ler(aos517Compose); ok {
		c.Nomeados++
		cab := "\n  litellm:\n"
		i := strings.Index(txt, cab)
		if i < 0 {
			falha("%s: não encontrei o serviço `litellm` — o proxy mudou de nome ou saiu do compose", aos517Compose)
		} else {
			bloco := txt[i+len(cab):]
			if j := aos517ReServico.FindStringIndex(bloco); j != nil {
				bloco = bloco[:j[0]]
			}
			imagens := aos517ReImagem.FindAllStringSubmatch(bloco, -1)
			switch {
			case len(imagens) != 1:
				falha("%s: o serviço `litellm` tem %d linhas `image:`; esperava uma", aos517Compose, len(imagens))
			case !strings.Contains(imagens[0][1], "@sha256:"):
				falha("%s: o serviço `litellm` refere a imagem por TAG MÓVEL (%s) e não por digest — "+
					"um pull ou uma recriação troca de proxy sem ninguém ver; fixa-a em %s",
					aos517Compose, imagens[0][1], decl)
			case imagens[0][1] != decl:
				falha("%s: o proxy de PRODUÇÃO (%s) não é o proxy em que se MEDE (%s, em %s)",
					aos517Compose, imagens[0][1], decl, aos517FonteBanco)
			}
		}
	}

	// (3) A constante do teste do gateway, que dois guiões lêem.
	if gw, ok := constante(aos517FonteGW, "aos505ImagemDoProxy"); ok {
		c.Nomeados++
		if gw != decl {
			falha("%s: aos505ImagemDoProxy (%s) não é a imagem declarada (%s, em %s)",
				aos517FonteGW, gw, decl, aos517FonteBanco)
		}
	}

	// (4-6) Os guiões: lêem a imagem da constante, e de mais lado nenhum.
	for _, g := range aos517Guioes {
		txt, ok := ler(g.guiao)
		if !ok {
			continue
		}
		c.Nomeados++
		leitura := `IMAGEM="$( sed -n 's/^const ` + g.constante + ` = "\(.*\)"$/\1/p' "$FONTE" )"`
		if !strings.Contains(txt, "\n"+leitura+"\n") {
			falha("%s: já não lê a imagem do proxy de `const %s` (esperava a linha %s)", g.guiao, g.constante, leitura)
		}
		if origem := `FONTE="` + g.fonte + `"`; !strings.Contains(txt, "\n"+origem+"\n") {
			falha("%s: a FONTE da imagem já não é %s", g.guiao, g.fonte)
		}
		if n := strings.Count(txt, "IMAGEM="); n != 1 {
			falha("%s: IMAGEM é atribuída %d vezes; esperava uma (a leitura da constante)", g.guiao, n)
		}
	}

	// (7) A varredura.
	for _, pasta := range aos517RaizesDaVarredura {
		base := filepath.Join(raiz, filepath.FromSlash(pasta))
		err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			nome := d.Name()
			if d.IsDir() {
				// Pastas de segredos do operador e pastas ocultas não são do repositório.
				if p != base && (strings.HasPrefix(nome, ".") || strings.HasPrefix(nome, "secrets")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(raiz, p)
			rel = filepath.ToSlash(rel)
			achou := false
			for n, linha := range strings.Split(string(b), "\n") {
				for _, ref := range aos517ReReferencia.FindAllString(linha, -1) {
					ref = strings.TrimRight(ref, ".:-")
					achou = true
					c.Ocorrencias++
					if ref != decl {
						falha("%s:%d: refere a imagem do proxy como %q; a declarada é %s "+
							"(uma referência abreviada também conta: escreve-a inteira ou não a escrevas)",
							rel, n+1, ref, decl)
					}
				}
			}
			if achou {
				c.Ficheiros++
			}
			return nil
		})
		if err != nil {
			falha("varredura de %s: %v", pasta, err)
		}
	}
	if c.Ficheiros < aos517MinimoDeFicheiros {
		falha("a varredura só encontrou a imagem do proxy em %d ficheiros; o piso é %d — não está a ler o que diz ler",
			c.Ficheiros, aos517MinimoDeFicheiros)
	}
	return c
}

// aos517Raiz é a raiz do repositório, vista do módulo do banco.
func aos517Raiz() string { return filepath.Join("..", "..", "..") }

// TestAOS517_ProxyDeProducaoEOProxyEmQueSeMede é o guarda: a árvore real, tal como está.
func TestAOS517_ProxyDeProducaoEOProxyEmQueSeMede(t *testing.T) {
	c := aos517Conferir(aos517Raiz())
	for _, f := range c.Falhas {
		t.Error(f)
	}
	// O que o ficheiro declara é o que o binário do banco usa: a constante compilada.
	if c.Declarada != ImagemDoProxy {
		t.Errorf("a declaração lida de %s (%s) não é a constante compilada (%s)", aos517FonteBanco, c.Declarada, ImagemDoProxy)
	}
	if want := 3 + len(aos517Guioes); c.Nomeados != want {
		t.Errorf("sítios nomeados conferidos: %d; esperava %d", c.Nomeados, want)
	}
	t.Logf("imagem do proxy: %s — %d sítios nomeados e %d referências escritas em %d ficheiros, todos iguais",
		c.Declarada, c.Nomeados, c.Ocorrencias, c.Ficheiros)
}

// aos517CopiaDosSitios copia para uma pasta temporária os ficheiros dos sítios, nos mesmos
// caminhos relativos. É sobre esta cópia que as mutações correm: a árvore real não é tocada.
func aos517CopiaDosSitios(t *testing.T) string {
	t.Helper()
	destino := t.TempDir()
	ficheiros := []string{aos517Compose, aos517FonteBanco, aos517FonteGW, aos517Runbook}
	for _, g := range aos517Guioes {
		ficheiros = append(ficheiros, g.guiao)
	}
	for _, rel := range ficheiros {
		b, err := os.ReadFile(filepath.Join(aos517Raiz(), filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("ler %s: %v", rel, err)
		}
		alvo := filepath.Join(destino, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(alvo), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(alvo, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return destino
}

// aos517Mutar aplica `f` ao conteúdo de um ficheiro da cópia e FALHA se nada mudou — uma
// mutação que não muta não prova nada.
func aos517Mutar(t *testing.T, raiz, rel string, f func(string) string) {
	t.Helper()
	p := filepath.Join(raiz, filepath.FromSlash(rel))
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	novo := f(string(b))
	if novo == string(b) {
		t.Fatalf("a mutação de %s não alterou o ficheiro", rel)
	}
	if err := os.WriteFile(p, []byte(novo), 0o644); err != nil {
		t.Fatal(err)
	}
}

// aos517TrocaUmCaracter troca o ÚLTIMO carácter hexadecimal do digest, na primeira ocorrência.
func aos517TrocaUmCaracter(t *testing.T) func(string) string {
	t.Helper()
	i := strings.LastIndex(ImagemDoProxy, ":")
	digest := ImagemDoProxy[i+1:]
	ultimo := "0"
	if strings.HasSuffix(digest, "0") {
		ultimo = "1"
	}
	mutado := digest[:len(digest)-1] + ultimo
	return func(s string) string { return strings.Replace(s, digest, mutado, 1) }
}

// TestAOS517_MutacaoDeUmCaracterAvermelhaEmCadaSitio é a mutação dirigida do ticket: um
// carácter do digest trocado em QUALQUER dos sítios que o trazem escrito avermelha o guarda, e
// a falha nomeia o ficheiro mutado. A cópia sem mutação passa — o controlo que prova que o
// vermelho vem da mutação e não da cópia.
func TestAOS517_MutacaoDeUmCaracterAvermelhaEmCadaSitio(t *testing.T) {
	if c := aos517Conferir(aos517CopiaDosSitios(t)); len(c.Falhas) != 0 {
		t.Fatalf("a cópia SEM mutação devia passar: %v", c.Falhas)
	}
	for _, rel := range []string{aos517Compose, aos517FonteBanco, aos517FonteGW, aos517Runbook} {
		t.Run(rel, func(t *testing.T) {
			raiz := aos517CopiaDosSitios(t)
			aos517Mutar(t, raiz, rel, aos517TrocaUmCaracter(t))
			c := aos517Conferir(raiz)
			if len(c.Falhas) == 0 {
				t.Fatalf("um carácter do digest trocado em %s e o guarda ficou verde", rel)
			}
			// Mutar a DECLARAÇÃO afasta-a de todos os outros: a falha nomeia-os a eles.
			if rel != aos517FonteBanco && !strings.Contains(strings.Join(c.Falhas, "\n"), rel) {
				t.Fatalf("a falha não nomeia o ficheiro mutado (%s): %v", rel, c.Falhas)
			}
		})
	}
}

// TestAOS517_TagEmVezDeDigestFalhaComMensagemPropria é o controlo negativo do ticket: com o
// compose a referir uma tag, o guarda falha a dizer «tag móvel» — não passa por «não encontrei
// digest para comparar», nem se confunde com dois digests diferentes.
func TestAOS517_TagEmVezDeDigestFalhaComMensagemPropria(t *testing.T) {
	raiz := aos517CopiaDosSitios(t)
	tag := "ghcr.io/" + aos517Repositorio + ":main-" + "stable"
	aos517Mutar(t, raiz, aos517Compose, func(s string) string {
		return strings.Replace(s, "image: "+ImagemDoProxy, "image: "+tag, 1)
	})
	c := aos517Conferir(raiz)
	junto := strings.Join(c.Falhas, "\n")
	if !strings.Contains(junto, "TAG MÓVEL") || !strings.Contains(junto, tag) {
		t.Fatalf("esperava a falha própria da tag móvel, a nomear %s; tive: %v", tag, c.Falhas)
	}
}

// TestAOS517_OutrasFugasAvermelham cobre o que a troca de um carácter não cobre: os sítios que
// não trazem o digest escrito (os guiões) e as maneiras de o guarda ficar verde sem medir.
func TestAOS517_OutrasFugasAvermelham(t *testing.T) {
	casos := []struct {
		nome, ficheiro string
		mutar          func(string) string
		diz            string
	}{
		{"guião com a imagem escrita à mão", "scripts/ci/wire-live.sh", func(s string) string {
			return strings.Replace(s, `IMAGEM="$( sed`, `IMAGEM="outra/imagem:1" # $( sed`, 1)
		}, "já não lê a imagem do proxy"},
		{"guião a ler de outra fonte", "scripts/ci/banco-ensaio-proxy.sh", func(s string) string {
			return strings.Replace(s, `FONTE="$REPO_ROOT/$MOD/proxy.go"`, `FONTE="$REPO_ROOT/$MOD/outro.go"`, 1)
		}, "a FONTE da imagem já não é"},
		{"guião que reatribui a imagem depois de a ler", "scripts/ci/rota-live.sh", func(s string) string {
			return s + "\nIMAGEM=outra/imagem:1\n"
		}, "IMAGEM é atribuída 2 vezes"},
		{"serviço do proxy fora do compose", aos517Compose, func(s string) string {
			return strings.Replace(s, "\n  litellm:\n", "\n  proxy:\n", 1)
		}, "não encontrei o serviço `litellm`"},
		{"declaração com digest truncado", aos517FonteBanco, func(s string) string {
			return strings.Replace(s, ImagemDoProxy, ImagemDoProxy[:len(ImagemDoProxy)-8], 1)
		}, "não é uma referência fixada"},
		{"referência nova por tag num runbook", aos517Runbook, func(s string) string {
			return s + "\ndocker pull " + aos517Repositorio + ":latest\n"
		}, aos517Runbook},
	}
	for _, caso := range casos {
		t.Run(caso.nome, func(t *testing.T) {
			raiz := aos517CopiaDosSitios(t)
			aos517Mutar(t, raiz, caso.ficheiro, caso.mutar)
			c := aos517Conferir(raiz)
			if !strings.Contains(strings.Join(c.Falhas, "\n"), caso.diz) {
				t.Fatalf("esperava uma falha com %q; tive: %v", caso.diz, c.Falhas)
			}
		})
	}
	t.Run("varredura abaixo do piso", func(t *testing.T) {
		raiz := aos517CopiaDosSitios(t)
		if err := os.Remove(filepath.Join(raiz, filepath.FromSlash(aos517Runbook))); err != nil {
			t.Fatal(err)
		}
		if c := aos517Conferir(raiz); !strings.Contains(strings.Join(c.Falhas, "\n"), "o piso é") {
			t.Fatalf("com um ficheiro a menos a varredura devia cair abaixo do piso; tive: %v", c.Falhas)
		}
	})
}
