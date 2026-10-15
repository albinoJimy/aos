package bancoensaio

import (
	"context"
	"errors"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// AOS-529 — AS CORRECÇÕES DA REVISÃO ADVERSARIAL.

// Um motor que ERRA factos (troca zeros por «O» no código e corta o nome): a contagem tem de dar o
// número exacto de ausentes por imagem e no resumo. Sem isto, uma contagem que desse sempre
// «presente» passava em todos os testes.
func TestAOS529_UmMotorQueErraFactosTemOsAusentesContados(t *testing.T) {
	r := aos529Correr(t, FalsoOCRErraFactos)
	quer := map[string][]string{
		"fatura-a":    {"FT 2026/0912", "Tecidos do Norte"},
		"fatura-b":    {"FT 2026/1407", "Panificação Ribeirão"},
		"recibo":      {"João Gonçalves Simões"},
		"guia-tabela": {"GR-5520"},
	}
	for _, l := range r.Imagens {
		if l.Estado != EstadoOCRLida || !slices.Equal(l.AusentesPelaRegra, quer[l.Documento]) ||
			l.PresentesPelaRegra != l.Factos-len(quer[l.Documento]) || l.PresentesComAcentos != l.PresentesPelaRegra {
			t.Errorf("%s: ausentes %v (quer %v), %d e %d de %d", l.ID, l.AusentesPelaRegra, quer[l.Documento], l.PresentesPelaRegra, l.PresentesComAcentos, l.Factos)
		}
	}
	total := aos529Total(t, r, "total")
	if total.Factos != 80 || total.PresentesPelaRegra != 58 || total.PresentesComAcentos != 58 || total.Lidas != 13 {
		t.Errorf("total: %+v (quer 58 de 80 factos)", total)
	}
	if g := aos529Total(t, r, "formato pdf"); g.Factos != 6 || g.PresentesPelaRegra != 4 {
		t.Errorf("formato pdf: %+v", g)
	}
	if !strings.Contains(ResumoOCREmTexto(r), " 72.5% ") {
		t.Error("o resumo tem de dizer 72,5% dos factos")
	}
}

func TestAOS529_OZeroNaoTemBarraEDistingueSeDoO(t *testing.T) {
	zero, _ := glifo('0')
	o, _ := glifo('O')
	if zero == o {
		t.Fatal("o zero e o O sao iguais")
	}
	// Sem barra: no meio do corpo só há tinta nas duas colunas das bordas do zero.
	for i := 4; i <= 8; i++ {
		tinta := 0
		for j := 0; j < larguraDaCelula; j++ {
			if zero[i][j] {
				tinta++
			}
		}
		if tinta != 2 {
			t.Errorf("a linha %d do zero tem %d pixeis: tem barra", i, tinta)
		}
	}
	// Mais estreito: a quinta coluna do zero está vazia, a do O não.
	vazia := true
	for i := range zero {
		vazia = vazia && !zero[i][4]
	}
	if !vazia || !o[5][4] {
		t.Error("o zero tem de ser mais estreito do que o O")
	}
	for _, l := range limitesDaMedicao(aos529Bateria(t), &MotorOCRFalsoDoBanco{Perfil: FalsoOCRCerto}) {
		if strings.Contains(l, "COMPARACAO") {
			return
		}
	}
	t.Error("os limites tem de dizer que a fonte pesa na comparacao entre motores")
}

func TestAOS529_TesseractAceitaOIDDeUmaImagemDoPosto(t *testing.T) {
	id := "sha256:a49fb8c4a19afe3d0fdc62cd8d826ddd968e045fa7bfca454486523a08a08e55"
	d := &aos529Docker{}
	m, err := NovoMotorOCRTesseract(context.Background(), id, "", d.correr)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(d.chamadas[0], " "); got != "image inspect --format {{.Id}} "+id {
		t.Errorf("a imagem confere-se pelo ID: %s", got)
	}
	for _, c := range d.chamadas[1:] {
		if c[0] == "run" && !slices.Contains(c, "never") {
			t.Errorf("todo o docker run leva --pull never: %v", c)
		}
	}
	if _, err := m.Ler(context.Background(), aos529Bateria(t).Media()[0]); err != nil {
		t.Fatal(err)
	}
	if ultima := d.chamadas[len(d.chamadas)-1]; !slices.Contains(ultima, id) || !slices.Contains(ultima, "never") {
		t.Errorf("a leitura corre a imagem pelo ID e sem pull: %v", ultima)
	}
	if !strings.Contains(usoDoOCR, "sha256:ID") || strings.Contains(usoDoOCR, "NOME:ETIQUETA") {
		t.Error("o uso diz as duas formas que funcionam")
	}
}

func TestAOS529_APastaMontadaELegivelPeloUidDoContentor(t *testing.T) {
	var modo os.FileMode
	d := &aos529Docker{ler: func(_ context.Context, args []string) ([]byte, []byte, int, error) {
		for i, a := range args {
			if a == "-v" {
				st, err := os.Stat(strings.TrimSuffix(args[i+1], ":/in:ro"))
				if err != nil {
					return nil, nil, 1, nil
				}
				modo = st.Mode().Perm()
			}
		}
		return []byte("texto\n"), nil, 0, nil
	}}
	m, _ := NovoMotorOCRTesseract(context.Background(), aos529Imagem, "", d.correr)
	if _, err := m.Ler(context.Background(), aos529Bateria(t).Media()[0]); err != nil {
		t.Fatal(err)
	}
	// No Windows as permissões de uma pasta não se lêem assim; o anfitrião que importa é o Linux.
	if runtime.GOOS != "windows" && modo&0o005 != 0o005 {
		t.Errorf("a pasta montada tem %v: o uid 65532 nao a le", modo)
	}
}

// A falha classifica-se pelo estágio que falhou e pelo pico do cgroup, não só pela saída 137.
func TestAOS529_AFalhaClassificaSePeloEstagioEPeloPico(t *testing.T) {
	alto := strings.TrimSpace(marcaDaMemoria) + " 1000000000"
	baixo := strings.TrimSpace(marcaDaMemoria) + " 50000000"
	casos := []struct {
		nome    string
		codigo  int
		stderr  string
		memoria bool
		diz     string
	}{
		{"oom do pdftoppm no pipe (sai 1)", 1, marcaDoEstagio + "pdftoppm 1\n" + alto, true, "pdftoppm"},
		{"oom do tesseract (137, pico alto)", 137, marcaDoEstagio + "tesseract 137\n" + alto, true, "tesseract"},
		{"137 sem pico medido", 137, marcaDoEstagio + "tesseract 137\n" + strings.TrimSpace(marcaDaMemoria) + " -", false, "tesseract saiu com 137"},
		{"137 com pico baixo", 137, baixo, false, "contentor saiu com 137"},
		{"pdftoppm falha sem memoria", 1, marcaDoEstagio + "pdftoppm 1\n" + baixo, false, "pdftoppm saiu com 1"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			err := classificarFalha(c.codigo, memoriaDoStderr(c.stderr), estagioDoStderr(c.stderr))
			if errors.Is(err, ErrOCRMemoria) != c.memoria || !strings.Contains(err.Error(), c.diz) {
				t.Errorf("%v (memoria %v)", err, c.memoria)
			}
		})
	}
	// O guião recolhe o código de cada estágio do pipe sem `pipefail` (o `dash` não o tem).
	for _, quer := range []string{`echo "p$?" >&4`, `echo "t$?" >&4`, "4>&1", marcaDoEstagio, "memory.peak"} {
		if !strings.Contains(guiaoDoTesseract, quer) {
			t.Errorf("o guiao nao tem %q", quer)
		}
	}
	if strings.Contains(guiaoDoTesseract, "pipefail") {
		t.Error("o dash da Debian nao tem pipefail")
	}
}

func TestAOS529_OsLimitesDizemRuncEChamadaIsolada(t *testing.T) {
	m := &MotorOCRTesseract{Imagem: aos529Imagem}
	notas := strings.Join(m.Condicoes().Notas, "\n")
	if !strings.Contains(notas, "runc") || !strings.Contains(notas, "NAO com o gVisor") || !strings.Contains(notas, "isolada") {
		t.Errorf("notas do tesseract: %s", notas)
	}
}
