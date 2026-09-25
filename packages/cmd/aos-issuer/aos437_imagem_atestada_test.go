package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// AOS-437 — o `aos-issuer` viaja na imagem assinada do nó, atestado como o `aos-orq` foi no
// AOS-403 (ADR-017 ponto 3, emenda AOS-437).
//
// A LACUNA que estes testes fecham: até aqui nenhum teste nem gate apanhava um binário
// acrescentado ao Dockerfile SEM subject na atestação. O `verify-attestation.sh` verifica o que o
// statement assinado DIZ, contra um mapa fixo de ficheiros; um binário novo que o Dockerfile copie
// e que ninguém acrescente a esse mapa, ao `sbom.sh` e ao `sign.sh` shipa na imagem assinada sem
// SBOM próprio, sem verificação de reprodutibilidade e sem recusa — e tudo sai verde. O digest da
// imagem cobre-o, mas é o subject por binário que diz a quem audita O QUE foi construído e com
// que módulos.
//
// A fonte da verdade é o Dockerfile: o conjunto de binários é DERIVADO dos seus
// `COPY --from=builder /out/<bin> /usr/local/bin/<bin>`, e cada um tem de aparecer nos três
// scripts da cadeia. Acrescentar um COPY sem o atestar avermelha este teste.

// aos437BinariosSemSubject são os binários da imagem que, DE PROPÓSITO, não têm subject próprio.
// Cada entrada é uma excepção NOMEADA, não um esquecimento: o `aos-healthprobe` é stdlib-only,
// não lê configuração nem toca estado, e corre só como HEALTHCHECK; fica coberto apenas pelo
// digest da imagem. É resíduo pré-existente (AOS-168), não introduzido por AOS-437 — atestá-lo é
// trabalho de outro ticket, e enquanto não o for a excepção fica aqui, à vista.
var aos437BinariosSemSubject = map[string]string{
	"aos-healthprobe": "HEALTHCHECK stdlib-only; coberto só pelo digest da imagem (resíduo AOS-168)",
}

func aos437LerDoRepo(t *testing.T, partes ...string) string {
	t.Helper()
	caminho := filepath.Join(append([]string{"..", "..", ".."}, partes...)...)
	b, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("ler %s: %v", caminho, err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

type aos437Cadeia struct {
	dockerfile, sbom, sign, verify string
}

func aos437CadeiaDoRepo(t *testing.T) aos437Cadeia {
	t.Helper()
	return aos437Cadeia{
		dockerfile: aos437LerDoRepo(t, "deploy", "node", "Dockerfile"),
		sbom:       aos437LerDoRepo(t, "scripts", "ci", "sbom.sh"),
		sign:       aos437LerDoRepo(t, "scripts", "ci", "sign.sh"),
		verify:     aos437LerDoRepo(t, "scripts", "ci", "verify-attestation.sh"),
	}
}

var (
	reAos437Copy     = regexp.MustCompile(`(?m)^COPY --from=builder /out/(\S+) /usr/local/bin/(\S+)\s*$`)
	reAos437Binarios = regexp.MustCompile(`(?s)\nBINARIOS = \{(.*?)\}\n`)
	reAos437Files    = regexp.MustCompile(`(?s)\nFILES = \{(.*?)\n\}\n`)
	reAos437ParPy    = regexp.MustCompile(`"([^"]+)":\s*"([^"]+)"`)
	reAos437AddSubj  = regexp.MustCompile(`(?m)^\s*printf '  "additionalSubjects": .*$`)
	reAos437NomePath = regexp.MustCompile(`\{ "name": "([^"]+)", "path": "usr/local/bin/([^"]+)"`)
	reAos437PyBloco  = regexp.MustCompile(`(?s)python3 - ((?:[^<]|<[^<])*?)<<'PY'[^\n]*\n(.*?)\nPY\n`)
	reAos437ArgBash  = regexp.MustCompile(`"[^"]*"`)
	reAos437TuploPar = regexp.MustCompile(`(?s)\(([^()]*)\)\s*=\s*sys\.argv\[1:(\d*)\]`)
	reAos437TuploNu  = regexp.MustCompile(`(?m)^((?:\w+\s*,\s*)+\w+)\s*=\s*sys\.argv\[1:(\d*)\]`)
	reAos437Indice   = regexp.MustCompile(`sys\.argv\[(\d+)\]`)
)

// binariosDaImagem deriva do Dockerfile o conjunto de binários que a imagem final carrega.
//
// TODA a linha COPY/ADD do ESTÁGIO FINAL tem de ter a forma canónica (achado M3 da revisão do
// AOS-437). Só casar a forma canónica deixava passar em silêncio as variantes que metem um binário
// na imagem sem o nomear — `--chmod`, destino directório, `/out/` inteiro, `/usr/bin/`, `ADD`, dois
// espaços —, e o varredor dava verde sobre elas porque os outros três COPY continuavam lá.
func (c aos437Cadeia) binariosDaImagem() (bins []string, erros []string) {
	final := c.dockerfile
	if i := strings.LastIndex(final, "\nFROM "); i >= 0 {
		final = final[i:]
	}
	for _, linha := range strings.Split(final, "\n") {
		campo := strings.ToUpper(strings.TrimSpace(linha))
		if !strings.HasPrefix(campo, "COPY ") && !strings.HasPrefix(campo, "ADD ") {
			continue
		}
		if !reAos437Copy.MatchString(strings.TrimRight(linha, "\r")) {
			erros = append(erros, "linha fora da forma canonica no estagio final (so `COPY --from=builder /out/<bin> /usr/local/bin/<bin>`): "+strings.TrimSpace(linha))
		}
	}
	for _, m := range reAos437Copy.FindAllStringSubmatch(c.dockerfile, -1) {
		if m[1] != m[2] {
			erros = append(erros, "COPY com nome de origem e destino diferentes: /out/"+m[1]+" -> /usr/local/bin/"+m[2])
		}
		bins = append(bins, m[2])
	}
	sort.Strings(bins)
	return bins, erros
}

// divergencias devolve, para cada binário da imagem, o que falta na cadeia de atestação.
// Vazio = a cadeia cobre a imagem inteira.
func (c aos437Cadeia) divergencias() []string {
	bins, erros := c.binariosDaImagem()
	if len(bins) < 3 {
		// Um varredor que não encontra COPY nenhum daria verde sobre coisa nenhuma.
		return append(erros, "só "+strconv.Itoa(len(bins))+" COPY --from=builder encontrados no Dockerfile — o varredor partiu-se")
	}

	binarios := map[string]string{}
	if m := reAos437Binarios.FindStringSubmatch(c.verify); m != nil {
		for _, p := range reAos437ParPy.FindAllStringSubmatch(m[1], -1) {
			binarios[p[1]] = p[2]
		}
	} else {
		erros = append(erros, "verify-attestation.sh sem o mapa BINARIOS")
	}
	files := map[string]string{}
	if m := reAos437Files.FindStringSubmatch(c.verify); m != nil {
		for _, p := range reAos437ParPy.FindAllStringSubmatch(m[1], -1) {
			files[p[1]] = p[2]
		}
	} else {
		erros = append(erros, "verify-attestation.sh sem o mapa FILES")
	}
	adicionais := map[string]bool{}
	if linha := reAos437AddSubj.FindString(c.sbom); linha != "" {
		for _, p := range reAos437NomePath.FindAllStringSubmatch(linha, -1) {
			if p[1] != p[2] {
				erros = append(erros, "additionalSubjects com name "+p[1]+" e path usr/local/bin/"+p[2])
			}
			adicionais[p[1]] = true
		}
	} else {
		erros = append(erros, "sbom.sh sem a linha additionalSubjects da proveniência")
	}

	for _, bin := range bins {
		if _, excepcao := aos437BinariosSemSubject[bin]; excepcao {
			continue
		}
		subject := "usr/local/bin/" + bin
		sbom := "sbom-" + bin + ".json"
		if bin == "aos" {
			sbom = "sbom.json"
		}
		// verify-attestation.sh: recusa pelo mapa fixo e pelo manifesto.
		if binarios[bin] != subject {
			erros = append(erros, bin+": sem entrada BINARIOS no verify-attestation.sh (quer "+strconv.Quote(bin)+": "+strconv.Quote(subject)+")")
		}
		if files[subject] != bin {
			erros = append(erros, bin+": sem entrada FILES "+strconv.Quote(subject)+" no verify-attestation.sh")
		}
		if files[sbom] != sbom {
			erros = append(erros, bin+": o SBOM "+sbom+" não está em FILES do verify-attestation.sh")
		}
		// sbom.sh: extraído da imagem, SBOM próprio e (excepto o nó, que é o subject principal)
		// a sua reprodutibilidade em additionalSubjects.
		if !regexp.MustCompile(`(?m)^atestar_binario ` + regexp.QuoteMeta(bin) + ` "`).MatchString(c.sbom) {
			erros = append(erros, bin+": o sbom.sh não o passa por atestar_binario")
		}
		if bin != "aos" {
			if !strings.Contains(c.sbom, `/`+sbom+`"`) {
				erros = append(erros, bin+": o sbom.sh não escreve "+sbom)
			}
			if !adicionais[bin] {
				erros = append(erros, bin+": sem entrada em additionalSubjects da proveniência (sbom.sh)")
			}
		}
		// sign.sh: subjects do statement in-toto e artefactos do manifesto de entrega.
		if !strings.Contains(c.sign, `{"name": "`+subject+`", "digest"`) {
			erros = append(erros, bin+": sem subject "+subject+" no statement do sign.sh")
		}
		if !strings.Contains(c.sign, `{"name": "`+sbom+`", "digest"`) {
			erros = append(erros, bin+": sem subject "+sbom+" no statement do sign.sh")
		}
		if !strings.Contains(c.sign, `{"name": "`+bin+`", "path": "`+bin+`"`) {
			erros = append(erros, bin+": sem artefacto "+bin+" no manifesto do sign.sh")
		}
		if !strings.Contains(c.sign, `{"name": "`+sbom+`", "path": "`+sbom+`"`) {
			erros = append(erros, bin+": sem artefacto "+sbom+" no manifesto do sign.sh")
		}
	}
	// Uma excepção cujo binário deixou de existir é uma excepção morta: sai da lista.
	presentes := map[string]bool{}
	for _, b := range bins {
		presentes[b] = true
	}
	for b := range aos437BinariosSemSubject {
		if !presentes[b] {
			erros = append(erros, "excepção morta em aos437BinariosSemSubject: "+b+" já não está no Dockerfile")
		}
	}
	return erros
}

// aridadePython prova, estaticamente, que cada bloco `python3 - <args> <<'PY'` desempacota
// exactamente os argumentos que o bash lhe passa. O sign.sh só assina no release (onde há chave):
// um desacerto de contagem rebentaria aí, e só aí. Devolve também quantos blocos verificou, para
// que um varredor partido não passe por verde.
func aos437AridadePython(nomeScript, src string) (blocos int, erros []string) {
	for _, m := range reAos437PyBloco.FindAllStringSubmatch(src, -1) {
		blocos++
		args := len(reAos437ArgBash.FindAllString(m[1], -1))
		corpo := m[2]
		var nomes []string
		fatia := ""
		if t := reAos437TuploPar.FindStringSubmatch(corpo); t != nil {
			nomes, fatia = strings.Split(t[1], ","), t[2]
		} else if t := reAos437TuploNu.FindStringSubmatch(corpo); t != nil {
			nomes, fatia = strings.Split(t[1], ","), t[2]
		}
		id := nomeScript + " bloco python #" + strconv.Itoa(blocos)
		if nomes != nil {
			n := 0
			for _, x := range nomes {
				if strings.TrimSpace(x) != "" {
					n++
				}
			}
			if n != args {
				erros = append(erros, id+": desempacota "+strconv.Itoa(n)+" nomes e o bash passa "+strconv.Itoa(args)+" argumentos")
			}
			if fatia != "" {
				fim, _ := strconv.Atoi(fatia)
				if fim-1 != args {
					erros = append(erros, id+": fatia sys.argv[1:"+fatia+"] para "+strconv.Itoa(args)+" argumentos")
				}
			}
			continue
		}
		// Sem desempacotamento: índices soltos sys.argv[i] têm de caber nos argumentos passados.
		maior := 0
		for _, ix := range reAos437Indice.FindAllStringSubmatch(corpo, -1) {
			if v, _ := strconv.Atoi(ix[1]); v > maior {
				maior = v
			}
		}
		if maior == 0 {
			erros = append(erros, id+": não lê sys.argv de forma reconhecível — o varredor não o consegue provar")
		} else if maior > args {
			erros = append(erros, id+": lê sys.argv["+strconv.Itoa(maior)+"] e o bash só passa "+strconv.Itoa(args)+" argumentos")
		}
	}
	return blocos, erros
}

func TestAOS437_ImagemCompilaECopiaOEmissor(t *testing.T) {
	df := aos437LerDoRepo(t, "deploy", "node", "Dockerfile")
	// Prime EXPLÍCITO do go.sum do próprio módulo, e só depois o build offline.
	bloco := "WORKDIR /src/packages/cmd/aos-issuer\n" +
		"RUN go mod download && go mod verify\n" +
		`RUN GOPROXY=off go build -trimpath -ldflags="-s -w -buildid=" -o /out/aos-issuer .` + "\n"
	if !strings.Contains(df, bloco) {
		t.Errorf("Dockerfile sem o bloco de build do aos-issuer com prime explícito:\n%s", bloco)
	}
	if !strings.Contains(df, "COPY --from=builder /out/aos-issuer /usr/local/bin/aos-issuer\n") {
		t.Error("Dockerfile não copia o aos-issuer para /usr/local/bin/aos-issuer")
	}
	// O emissor não muda o que a imagem É: o ENTRYPOINT e o HEALTHCHECK continuam os do nó.
	if !strings.Contains(df, `ENTRYPOINT ["/usr/local/bin/aos"]`) {
		t.Error("o ENTRYPOINT da imagem deixou de ser o nó")
	}
	if !strings.Contains(df, `CMD ["/usr/local/bin/aos-healthprobe"]`) {
		t.Error("o HEALTHCHECK deixou de ser o aos-healthprobe")
	}
	// Só o BINÁRIO: nenhuma chave nem variável de chave do emissor é posta na imagem.
	for _, proibido := range []string{"issuer.key", "AOS_ISSUER_KEY", "VAULT_TOKEN", "ENV AOS_MANDATE"} {
		if strings.Contains(df, proibido) {
			t.Errorf("Dockerfile menciona %q — a chave do emissor nunca entra na imagem (ADR-017 §5)", proibido)
		}
	}
	// O WORKDIR do builder do emissor não pode ficar como o WORKDIR final da imagem.
	if strings.LastIndex(df, "WORKDIR /var/lib/aos\n") < strings.LastIndex(df, "WORKDIR /src/packages/cmd/aos-issuer") {
		t.Error("o WORKDIR final deixou de ser o do nó")
	}
}

func TestAOS437_TodoOBinarioDaImagemEstaAtestado(t *testing.T) {
	c := aos437CadeiaDoRepo(t)
	bins, _ := c.binariosDaImagem()
	quer := []string{"aos", "aos-healthprobe", "aos-issuer", "aos-orq"}
	if strings.Join(bins, ",") != strings.Join(quer, ",") {
		t.Errorf("binários da imagem = %v, quer %v", bins, quer)
	}
	for _, e := range c.divergencias() {
		t.Error(e)
	}
}

func TestAOS437_AridadeDosBlocosPythonDaCadeia(t *testing.T) {
	for _, caso := range []struct {
		script     string
		minBlocos  int
		partesRepo []string
	}{
		{"sign.sh", 3, []string{"scripts", "ci", "sign.sh"}},
		{"verify-attestation.sh", 1, []string{"scripts", "ci", "verify-attestation.sh"}},
	} {
		src := aos437LerDoRepo(t, caso.partesRepo...)
		n, erros := aos437AridadePython(caso.script, src)
		if n < caso.minBlocos {
			t.Errorf("%s: só %d blocos python encontrados, quer >= %d — o varredor partiu-se", caso.script, n, caso.minBlocos)
		}
		for _, e := range erros {
			t.Error(e)
		}
	}
}

// As mutações provam que o teste morde: cada uma reproduz um esquecimento real e tem de
// avermelhar pelo motivo certo. Correm sobre cópias em memória — nada no disco muda.
func TestAOS437_MutacoesAvermelham(t *testing.T) {
	real := aos437CadeiaDoRepo(t)
	if e := real.divergencias(); len(e) != 0 {
		t.Fatalf("a cadeia real já diverge, as mutações não provariam nada: %v", e)
	}

	substituir := func(t *testing.T, s, velho, novo string) string {
		t.Helper()
		if strings.Count(s, velho) != 1 {
			t.Fatalf("âncora da mutação não é única (%d): %q", strings.Count(s, velho), velho)
		}
		return strings.Replace(s, velho, novo, 1)
	}
	contem := func(erros []string, sub string) bool {
		for _, e := range erros {
			if strings.Contains(e, sub) {
				return true
			}
		}
		return false
	}

	t.Run("aos-issuer fora do BINARIOS", func(t *testing.T) {
		c := real
		c.verify = substituir(t, c.verify, `"aos-issuer": "usr/local/bin/aos-issuer"`, `"aos-issuerX": "usr/local/bin/aos-issuer"`)
		if e := c.divergencias(); !contem(e, "aos-issuer: sem entrada BINARIOS") {
			t.Errorf("tirar o aos-issuer do BINARIOS não avermelhou: %v", e)
		}
	})
	t.Run("COPY ficticio sem atestacao", func(t *testing.T) {
		c := real
		c.dockerfile = substituir(t, c.dockerfile,
			"COPY --from=builder /out/aos-issuer /usr/local/bin/aos-issuer\n",
			"COPY --from=builder /out/aos-issuer /usr/local/bin/aos-issuer\nCOPY --from=builder /out/aos-fantasma /usr/local/bin/aos-fantasma\n")
		e := c.divergencias()
		for _, quer := range []string{
			"aos-fantasma: sem entrada BINARIOS",
			"aos-fantasma: sem entrada em additionalSubjects",
			"aos-fantasma: sem subject usr/local/bin/aos-fantasma no statement do sign.sh",
		} {
			if !contem(e, quer) {
				t.Errorf("COPY fictício: falta o vermelho %q em %v", quer, e)
			}
		}
	})
	// M3: cada variante que mete um binário na imagem sem a forma canónica avermelha.
	for _, v := range []struct{ nome, linha string }{
		{"--chmod", "COPY --from=builder --chmod=0555 /out/aos-x /usr/local/bin/aos-x"},
		{"destino directorio", "COPY --from=builder /out/aos-x /usr/local/bin/"},
		{"out inteiro", "COPY --from=builder /out/ /usr/local/bin/"},
		{"usr/bin", "COPY --from=builder /out/aos-x /usr/bin/aos-x"},
		{"ADD", "ADD /out/aos-x /usr/local/bin/aos-x"},
		{"dois espacos", "COPY --from=builder  /out/aos-x /usr/local/bin/aos-x"},
	} {
		t.Run("COPY fora da forma: "+v.nome, func(t *testing.T) {
			c := real
			c.dockerfile = substituir(t, c.dockerfile,
				"COPY --from=builder /out/aos-issuer /usr/local/bin/aos-issuer\n",
				"COPY --from=builder /out/aos-issuer /usr/local/bin/aos-issuer\n"+v.linha+"\n")
			if e := c.divergencias(); !contem(e, "fora da forma canonica") {
				t.Errorf("%s: a variante nao avermelhou: %v", v.nome, e)
			}
		})
	}
	t.Run("aos-issuer fora de additionalSubjects", func(t *testing.T) {
		c := real
		c.sbom = substituir(t, c.sbom, `"name": "aos-issuer", "path": "usr/local/bin/aos-issuer"`, `"name": "aos-orq2", "path": "usr/local/bin/aos-orq2"`)
		if e := c.divergencias(); !contem(e, "aos-issuer: sem entrada em additionalSubjects") {
			t.Errorf("tirar o aos-issuer de additionalSubjects não avermelhou: %v", e)
		}
	})
	t.Run("aos-issuer fora do statement do sign.sh", func(t *testing.T) {
		c := real
		c.sign = substituir(t, c.sign, `{"name": "usr/local/bin/aos-issuer", "digest"`, `{"name": "usr/local/bin/outro", "digest"`)
		if e := c.divergencias(); !contem(e, "aos-issuer: sem subject usr/local/bin/aos-issuer") {
			t.Errorf("tirar o aos-issuer do statement não avermelhou: %v", e)
		}
	})
	t.Run("excepcao morta", func(t *testing.T) {
		c := real
		c.dockerfile = substituir(t, c.dockerfile, "COPY --from=builder /out/aos-healthprobe /usr/local/bin/aos-healthprobe\n", "")
		if e := c.divergencias(); !contem(e, "excepção morta") {
			t.Errorf("uma excepção sem binário não avermelhou: %v", e)
		}
	})
	t.Run("argumento a mais no manifesto do sign.sh", func(t *testing.T) {
		src := substituir(t, real.sign, `"$sbom_orq_sha" "$iss_sha" "$sbom_iss_sha" <<'PY'`, `"$sbom_orq_sha" "$iss_sha" "$sbom_iss_sha" "$extra" <<'PY'`)
		if _, e := aos437AridadePython("sign.sh", src); !contem(e, "desempacota 16 nomes e o bash passa 17") {
			t.Errorf("argumento a mais não avermelhou: %v", e)
		}
	})
	t.Run("fatia fixa curta", func(t *testing.T) {
		src := substituir(t, real.sign, "\n orq_sha, sbom_orq_sha, iss_sha, sbom_iss_sha) = sys.argv[1:]", "\n orq_sha, sbom_orq_sha, iss_sha, sbom_iss_sha) = sys.argv[1:13]")
		if _, e := aos437AridadePython("sign.sh", src); !contem(e, "fatia sys.argv[1:13] para 14 argumentos") {
			t.Errorf("fatia fixa desalinhada não avermelhou: %v", e)
		}
	})
}
