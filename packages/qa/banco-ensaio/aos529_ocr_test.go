package bancoensaio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// AOS-529 — A EXPERIÊNCIA `ocr`: o relatório distingue os motores; o Tesseract corre nas
// condições do gVisor.

// aos529Relogio avança um segundo por leitura, para o tempo por imagem ser determinista.
func aos529Relogio() func() time.Time {
	agora := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		agora = agora.Add(500 * time.Millisecond)
		return agora
	}
}

func aos529Correr(t *testing.T, perfil string) *RelatorioOCR {
	t.Helper()
	m, err := NovoMotorOCRFalso(perfil)
	if err != nil {
		t.Fatal(err)
	}
	return CorrerExperienciaOCR(context.Background(), aos529Bateria(t), m, aos529Relogio(), nil)
}

func aos529Total(t *testing.T, r *RelatorioOCR, grupo string) LinhaDoResumoOCR {
	t.Helper()
	for _, g := range r.Resumo {
		if g.Grupo == grupo {
			return g
		}
	}
	t.Fatalf("sem o grupo %q no resumo", grupo)
	return LinhaDoResumoOCR{}
}

func TestAOS529_ORelatorioDistingueOsTresMotoresFalsos(t *testing.T) {
	certo := aos529Correr(t, FalsoOCRCerto)
	semAcentos := aos529Correr(t, FalsoOCRSemAcentos)
	falha := aos529Correr(t, FalsoOCRFalhaPDF)

	tc := aos529Total(t, certo, "total")
	if tc.Lidas != tc.Imagens || tc.PresentesPelaRegra != tc.Factos || tc.PresentesComAcentos != tc.Factos {
		t.Errorf("o motor certo tem de ler tudo, com acentos: %+v", tc)
	}
	for _, l := range certo.Imagens {
		if l.Estado != "lida" || l.TempoMs != 500 || l.MemoriaPicoBytes != memoriaDoFalso || l.TamanhoDoTexto == 0 || !strings.HasPrefix(l.DigestDoMedia, "sha256:") {
			t.Errorf("%s: %+v", l.ID, l)
		}
	}

	// O que perde os acentos: a regra do banco não o vê; a comparação com acentos vê, e só nas
	// imagens com acentos.
	ts := aos529Total(t, semAcentos, "total")
	if ts.PresentesPelaRegra != ts.Factos || ts.PresentesComAcentos >= ts.Factos {
		t.Errorf("sem acentos: a regra tem de dar tudo e a comparacao com acentos menos: %+v", ts)
	}
	if g := aos529Total(t, semAcentos, "sem acentos"); g.PresentesComAcentos != g.Factos {
		t.Errorf("nas imagens sem acentos nao se perde nada: %+v", g)
	}
	for _, l := range semAcentos.Imagens {
		if l.ComAcentos != (len(l.SemOAcentoExacto) > 0) || len(l.AusentesPelaRegra) != 0 {
			t.Errorf("%s: com acentos %v, sem o acento exacto %v, ausentes %v", l.ID, l.ComAcentos, l.SemOAcentoExacto, l.AusentesPelaRegra)
		}
		if l.ID == "fatura-b-limpo" && !slices.Contains(l.SemOAcentoExacto, "Évora") {
			t.Errorf("fatura-b-limpo: quer Évora entre os sem acento exacto: %v", l.SemOAcentoExacto)
		}
	}

	// O que falha uma imagem: só o PDF, com causa, e os factos dele ausentes.
	tf := aos529Total(t, falha, "total")
	if tf.Lidas != tf.Imagens-1 {
		t.Errorf("o motor que falha o PDF le todas menos uma: %+v", tf)
	}
	for _, l := range falha.Imagens {
		if l.Formato == FormatoPDF {
			if l.Estado != "falhou" || l.Causa != CausaOCRMotor || len(l.AusentesPelaRegra) != l.Factos || l.MemoriaPicoBytes != -1 {
				t.Errorf("PDF: %+v", l)
			}
		} else if l.Estado != "lida" {
			t.Errorf("%s nao devia falhar", l.ID)
		}
	}
	if g := aos529Total(t, falha, "formato pdf"); g.Lidas != 0 || g.PresentesPelaRegra != 0 {
		t.Errorf("formato pdf: %+v", g)
	}

	// O resumo em texto diz os três de maneira diferente.
	textos := []string{ResumoOCREmTexto(certo), ResumoOCREmTexto(semAcentos), ResumoOCREmTexto(falha)}
	if !strings.Contains(textos[0], "100.0%  100.0%") || strings.Contains(textos[0], "falhou") ||
		!strings.Contains(textos[1], "100.0%   54.8%") ||
		!strings.Contains(textos[2], "falhou:motor") {
		t.Errorf("o resumo nao distingue os motores:\n%s\n%s\n%s", textos[0], textos[1], textos[2])
	}
	for _, s := range textos {
		// Estes relatórios saem de CorrerExperienciaOCR sem o veredicto do AOS-530, e o resumo di-lo.
		if !strings.Contains(s, "sem veredicto") || !strings.Contains(s, "13 imagens") || !strings.Contains(s, aos529DigestDaBateriaOCR) {
			t.Error("o resumo tem de dizer a bateria, os limites e que nao ha veredicto")
		}
	}
}

func TestAOS529_UmaCorridaCanceladaSaiInterrompida(t *testing.T) {
	ctx, cancelar := context.WithCancel(context.Background())
	cancelar()
	m, _ := NovoMotorOCRFalso(FalsoOCRCerto)
	r := CorrerExperienciaOCR(ctx, aos529Bateria(t), m, aos529Relogio(), nil)
	if !r.Interrompida || len(r.Imagens) != 0 || !strings.Contains(ResumoOCREmTexto(r), "INTERROMPIDA") {
		t.Errorf("interrompida %v, %d imagens", r.Interrompida, len(r.Imagens))
	}
	if _, err := NovoMotorOCRFalso("adivinha"); err == nil {
		t.Error("um perfil desconhecido tem de ser recusado")
	}
}

// ─── O Tesseract em contentor, com um Docker de teste ───────────────────────────────────────

const aos529Imagem = "registo.local/ocr/tesseract@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type aos529Docker struct {
	chamadas [][]string
	// ler responde a um `docker run` de leitura.
	ler func(ctx context.Context, args []string) ([]byte, []byte, int, error)
	// semImagem faz o `image inspect` falhar.
	semImagem bool
}

func (d *aos529Docker) correr(ctx context.Context, args ...string) ([]byte, []byte, int, error) {
	d.chamadas = append(d.chamadas, args)
	switch {
	case args[0] == "image":
		if d.semImagem {
			return nil, []byte("No such image"), 1, nil
		}
		return []byte("[]"), nil, 0, nil
	case args[0] == "rm":
		return nil, nil, 0, nil
	case slices.Contains(args, "--version"):
		return []byte("tesseract 5.3.0\n leptonica-1.82.0\n"), nil, 0, nil
	}
	if d.ler != nil {
		return d.ler(ctx, args)
	}
	return []byte("texto lido\n"), []byte("Estimating resolution\n" + marcaDaMemoria + "123456789\n"), 0, nil
}

func TestAOS529_TesseractCorreNasCondicoesDoGVisor(t *testing.T) {
	d := &aos529Docker{}
	m, err := NovoMotorOCRTesseract(context.Background(), aos529Imagem, "", d.correr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Descricao(), "tesseract 5.3.0") || !strings.Contains(m.Descricao(), "por+eng") || !strings.Contains(m.Descricao(), aos529Imagem) {
		t.Errorf("descricao: %s", m.Descricao())
	}
	media := aos529Bateria(t).Media()
	var pdf MediaOCR
	for _, x := range media {
		if x.Formato == FormatoPDF {
			pdf = x
		}
	}
	lida, err := m.Ler(context.Background(), pdf)
	if err != nil || lida.Texto != "texto lido\n" || lida.MemoriaPico != 123456789 {
		t.Fatalf("leitura: %+v %v", lida, err)
	}
	args := d.chamadas[len(d.chamadas)-1]
	junto := strings.Join(args, " ")
	for _, quer := range []string{
		"--pull never", "--network none", "--memory 1073741824", "--memory-swap 1073741824", "--pids-limit 512",
		"--read-only", "--cap-drop ALL", "--security-opt no-new-privileges", "--user 65532:65532",
		":/in:ro", "--entrypoint sh " + aos529Imagem, "/in/fatura-b-pdf.pdf por+eng",
	} {
		if !strings.Contains(junto, quer) {
			t.Errorf("o docker run nao tem %q: %s", quer, junto)
		}
	}
	if slices.Contains(args, "pull") || strings.Contains(junto, "--network bridge") || strings.Contains(junto, "--privileged") {
		t.Error("o motor nao descarrega nem tem rede nem privilegios")
	}
	if i := slices.Index(args, "--name"); i < 0 || !strings.HasPrefix(args[i+1], prefixoDoDocker+"ocr-") {
		t.Errorf("o contentor tem de ter o prefixo do banco, para a limpeza o varrer: %v", args)
	}
	// A pasta montada desaparece depois da leitura.
	for i, a := range args {
		if a == "-v" {
			pasta := strings.TrimSuffix(args[i+1], ":/in:ro")
			if _, err := os.Stat(pasta); !os.IsNotExist(err) {
				t.Errorf("a pasta temporaria ficou: %s", pasta)
			}
		}
	}
}

func TestAOS529_TesseractRecusaImagemNaoFixadaOuAusente(t *testing.T) {
	for _, imagem := range []string{"tesseract:latest", "ubuntu", aos529Imagem[:len(aos529Imagem)-1], "--privileged@sha256:" + strings.Repeat("a", 64),
		"aos-ensaio-ocr:tesseract@sha256:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("A", 64), "sha256:" + strings.Repeat("a", 63)} {
		d := &aos529Docker{}
		if _, err := NovoMotorOCRTesseract(context.Background(), imagem, "", d.correr); !errors.Is(err, ErrImagemDoMotor) || len(d.chamadas) != 0 {
			t.Errorf("%q: quer recusa sem chamar o docker, deu %v e %d chamadas", imagem, err, len(d.chamadas))
		}
	}
	d := &aos529Docker{semImagem: true}
	if _, err := NovoMotorOCRTesseract(context.Background(), aos529Imagem, "", d.correr); !errors.Is(err, ErrImagemDoMotor) || len(d.chamadas) != 1 {
		t.Errorf("imagem ausente: %v, %d chamadas", err, len(d.chamadas))
	}
	if _, err := NovoMotorOCRTesseract(context.Background(), aos529Imagem, "por; rm -rf /", (&aos529Docker{}).correr); !errors.Is(err, ErrImagemDoMotor) {
		t.Error("linguas invalidas tem de ser recusadas")
	}
}

func TestAOS529_TesseractClassificaTempoEMemoria(t *testing.T) {
	media := aos529Bateria(t).Media()[0]

	oom := &aos529Docker{ler: func(context.Context, []string) ([]byte, []byte, int, error) {
		return nil, []byte(marcaDaMemoria + "1073741824\n"), 137, nil
	}}
	m, _ := NovoMotorOCRTesseract(context.Background(), aos529Imagem, "", oom.correr)
	if _, err := m.Ler(context.Background(), media); !errors.Is(err, ErrOCRMemoria) {
		t.Errorf("saida 137: quer ErrOCRMemoria, deu %v", err)
	}

	lento := &aos529Docker{ler: func(ctx context.Context, _ []string) ([]byte, []byte, int, error) {
		<-ctx.Done()
		return nil, nil, -1, ctx.Err()
	}}
	m, _ = NovoMotorOCRTesseract(context.Background(), aos529Imagem, "", lento.correr)
	m.limite = 20 * time.Millisecond
	if _, err := m.Ler(context.Background(), media); !errors.Is(err, ErrOCRTempo) {
		t.Errorf("passou o limite: quer ErrOCRTempo, deu %v", err)
	}
	ultima := lento.chamadas[len(lento.chamadas)-1]
	if ultima[0] != "rm" || ultima[1] != "-f" || !strings.HasPrefix(ultima[2], prefixoDoDocker+"ocr-") {
		t.Errorf("passado o limite o contentor tem de ser removido: %v", ultima)
	}

	falhou := &aos529Docker{ler: func(context.Context, []string) ([]byte, []byte, int, error) {
		return nil, []byte("Error in pixReadStream"), 1, nil
	}}
	m, _ = NovoMotorOCRTesseract(context.Background(), aos529Imagem, "", falhou.correr)
	if _, err := m.Ler(context.Background(), media); err == nil || errors.Is(err, ErrOCRTempo) || errors.Is(err, ErrOCRMemoria) {
		t.Errorf("saida 1: quer um erro do motor, deu %v", err)
	}
	if causaDoErroOCR(ErrOCRTempo) != CausaOCRTempo || causaDoErroOCR(ErrOCRMemoria) != CausaOCRMemoria || causaDoErroOCR(errors.New("x")) != CausaOCRMotor {
		t.Error("causas")
	}
	if memoriaDoStderr("sem marca") != -1 || memoriaDoStderr(marcaDaMemoria+"-") != -1 {
		t.Error("sem a memoria, -1")
	}
}

// ─── A linha de comandos ────────────────────────────────────────────────────────────────────

func aos529Executar(t *testing.T, ctx context.Context, d *aos529Docker, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	amb := Ambiente{Relogio: aos529Relogio()}
	if d != nil {
		amb.dockerDeTeste = d.correr
	}
	codigo := Executar(ctx, append([]string{"ocr"}, args...), &out, &errb, amb)
	return codigo, out.String(), errb.String()
}

func TestAOS529_LinhaDeComandos(t *testing.T) {
	pasta := t.TempDir()
	codigo, out, errb := aos529Executar(t, context.Background(), nil, "--motor", "falso", "--falso", "falha-pdf", "--saida", pasta)
	if codigo != SaidaOK || !strings.Contains(out, "falhou:motor") {
		t.Fatalf("falso: %d\n%s\n%s", codigo, out, errb)
	}
	feitos, _ := filepath.Glob(filepath.Join(pasta, "ensaio-ocr-falso-falha-pdf-*.json"))
	if len(feitos) != 1 {
		t.Fatalf("quer um relatorio, ha %v", feitos)
	}
	cru, _ := os.ReadFile(feitos[0])
	var r RelatorioOCR
	if err := json.Unmarshal(cru, &r); err != nil || r.Experiencia != "ocr" || len(r.Imagens) != 13 || r.DigestBateria != aos529DigestDaBateriaOCR || r.Limites.Rede != "none" || r.Limites.TempoPorChamadaS != 45 {
		t.Errorf("relatorio: %v %+v", err, r.Limites)
	}

	codigo, out, _ = aos529Executar(t, context.Background(), &aos529Docker{}, "--motor", "tesseract", "--imagem", aos529Imagem, "--saida", pasta, "--silencioso")
	if codigo != SaidaOK || strings.Contains(out, "por imagem") || !strings.Contains(out, "ensaio-ocr-tesseract-") {
		t.Errorf("tesseract com docker de teste: %d %s", codigo, out)
	}

	ctx, cancelar := context.WithCancel(context.Background())
	cancelar()
	if codigo, _, _ := aos529Executar(t, ctx, nil, "--motor", "falso", "--saida", pasta); codigo != SaidaParouAMeio {
		t.Errorf("cancelada: quer %d, deu %d", SaidaParouAMeio, codigo)
	}

	for _, c := range []struct {
		args   []string
		codigo int
	}{
		{[]string{"--saida", pasta}, SaidaUso},
		{[]string{"--motor", "easyocr", "--saida", pasta}, SaidaUso},
		{[]string{"--motor", "falso", "--falso", "x", "--saida", pasta}, SaidaUso},
		{[]string{"--motor", "falso", "--imagem", aos529Imagem, "--saida", pasta}, SaidaUso},
		{[]string{"--motor", "tesseract", "--saida", pasta}, SaidaUso},
		{[]string{"--motor", "tesseract", "--imagem", "tesseract:latest", "--saida", pasta}, SaidaUso},
		{[]string{"--motor", "tesseract", "--imagem", aos529Imagem, "--linguas", "por;x", "--saida", pasta}, SaidaUso},
		{[]string{"--motor", "falso"}, SaidaUso},
		{[]string{"--motor", "falso", "--saida", pasta, "a-mais"}, SaidaUso},
	} {
		if codigo, _, _ := aos529Executar(t, context.Background(), &aos529Docker{}, c.args...); codigo != c.codigo {
			t.Errorf("%v: quer %d, deu %d", c.args, c.codigo, codigo)
		}
	}
	if codigo, _, errb := aos529Executar(t, context.Background(), &aos529Docker{semImagem: true}, "--motor", "tesseract", "--imagem", aos529Imagem, "--saida", pasta); codigo != SaidaSemInfra || !strings.Contains(errb, "nao a descarrega") {
		t.Errorf("imagem ausente: %d %s", codigo, errb)
	}

	imagens := t.TempDir()
	codigo, out, _ = aos529Executar(t, context.Background(), nil, "bateria", "--imagens", imagens)
	escritas, _ := os.ReadDir(imagens)
	if codigo != SaidaOK || !strings.Contains(out, aos529DigestDaBateriaOCR) || len(escritas) != 13 {
		t.Errorf("bateria: %d, %d imagens\n%s", codigo, len(escritas), out)
	}
	if codigo, _, _ := aos529Executar(t, context.Background(), nil, "bateria", "a-mais"); codigo != SaidaUso {
		t.Error("bateria com argumento a mais")
	}
}
