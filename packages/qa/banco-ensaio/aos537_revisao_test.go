package bancoensaio

// AOS-537 — REGRESSÕES DA REVISÃO INDEPENDENTE. As respostas fabricadas pela revisão: um
// `qualificado` tem de ser de confiança e um `recusado` não pode vir da formatação. Correm sem
// Docker e sem rede.

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// aos537MotorDe é um motor de teste que responde, a cada vídeo, o que a função dada escreve.
type aos537MotorDe struct{ escrever func(VideoDaBateria) string }

func (m aos537MotorDe) Nome() string                       { return "teste" }
func (m aos537MotorDe) Descricao() string                  { return "motor de teste da revisao" }
func (m aos537MotorDe) Condicoes() CondicoesDoMotorDeVideo { return CondicoesDoMotorDeVideo{} }
func (m aos537MotorDe) PrecisaDoMP4() bool                 { return false }
func (m aos537MotorDe) Descrever(_ context.Context, vp VideoPronto) (RespostaDoVideo, error) {
	return RespostaDoVideo{Texto: m.escrever(vp.Video)}, nil
}

func aos537Medir(t *testing.T, escrever func(VideoDaBateria) string) *RelatorioVideo {
	t.Helper()
	b := aos537Bateria(t)
	r := CorrerExperienciaVideo(context.Background(), b, b.SemMP4(), aos537MotorDe{escrever}, aos529Relogio(), nil)
	r.VeredictoDoVideo = VeredictoVideo(r, LimiarDeFactosDoVideo, nil)
	return r
}

// aos537Ausentes devolve os factos ausentes de um vídeo do relatório.
func aos537Ausentes(r *RelatorioVideo, id string) []string {
	for _, l := range r.Videos {
		if l.ID == id {
			return l.AusentesPelaRegra
		}
	}
	return nil
}

var reFundoDeOrigem = regexp.MustCompile(`(?m)^(BACKGROUND \d+): (\w+)$`)

func TestAOS537_Revisao_AFormatacaoNaoRecusaUmaRespostaPerfeita(t *testing.T) {
	formas := map[string]func(VideoDaBateria) string{
		"negrito": func(v VideoDaBateria) string {
			s := DescricaoDeOrigem(v)
			s = strings.NewReplacer("FACTS\n", "## FACTS\n", "TEXT: ", "**TEXT:** ", "COUNT: ", "**COUNT:** ").Replace(s)
			return reFundoDeOrigem.ReplaceAllString(s, "**$1:** $2")
		},
		"espaco antes dos dois pontos e tabela": func(v VideoDaBateria) string {
			s := strings.ReplaceAll(DescricaoDeOrigem(v), "COUNT: ", "COUNT : ")
			return reFundoDeOrigem.ReplaceAllString(s, "| $1 | $2 |")
		},
		"italico e crases": func(v VideoDaBateria) string {
			s := strings.ReplaceAll(DescricaoDeOrigem(v), "COUNT: ", "_COUNT_: `")
			return reFundoDeOrigem.ReplaceAllString(s, "`$1`: *$2*")
		},
	}
	for nome, escrever := range formas {
		r := aos537Medir(t, escrever)
		if v := r.VeredictoDoVideo; v.Veredicto != VeredictoQualificado || v.PresentesPelaRegra != v.Factos || v.Factos != 34 {
			t.Errorf("%s: %+v", nome, v)
		}
	}
}

func TestAOS537_Revisao_DuasContagensNumaLinhaContamAmbas(t *testing.T) {
	for _, junta := range []string{", ", " and ", " e ", "; "} {
		r := aos537Medir(t, func(v VideoDaBateria) string {
			var fora, contagens []string
			for _, l := range strings.Split(DescricaoDeOrigem(v), "\n") {
				if c, ok := strings.CutPrefix(l, "COUNT: "); ok {
					contagens = append(contagens, c)
				} else {
					fora = append(fora, l)
				}
			}
			if len(contagens) > 0 {
				fora = append(fora, "COUNT: "+strings.Join(contagens, junta))
			}
			return strings.Join(fora, "\n")
		})
		if a := aos537Ausentes(r, "contagem-de-formas"); len(a) != 0 || r.VeredictoDoVideo.Veredicto != VeredictoQualificado {
			t.Errorf("separador %q: ausentes %v", junta, a)
		}
	}
}

func TestAOS537_Revisao_TodasAsCoresETodosOsNumerosNaoAcertam(t *testing.T) {
	cores := "red, green, blue, yellow, white, black"
	for nome, escrever := range map[string]func(VideoDaBateria) string{
		"numa linha": func(v VideoDaBateria) string {
			var b strings.Builder
			for _, c := range v.Cenas {
				for _, l := range c.Texto {
					b.WriteString("TEXT: " + l + "\n")
				}
			}
			for i := range v.Cenas {
				b.WriteString(LinhaDeFundo(i+1, cores) + "\n")
			}
			for _, forma := range []string{"circle", "square", "triangle"} {
				for _, cor := range []string{"red", "green", "blue", "yellow", "white", "black"} {
					b.WriteString("COUNT: 2 or 3 or 4 or 5 or 6 or 7 or 8 or 9 " + cor + " " + forma + "s\n")
				}
			}
			return b.String()
		},
		"uma linha por hipotese": func(v VideoDaBateria) string {
			var b strings.Builder
			b.WriteString(DescricaoDeOrigem(v))
			for i := range v.Cenas {
				for _, cor := range []string{"red", "green", "blue", "yellow", "white", "black"} {
					b.WriteString(LinhaDeFundo(i+1, cor) + "\n")
				}
			}
			for _, c := range v.Cenas {
				for _, g := range c.Formas {
					for n := FormasMinimasDeUmGrupo; n <= FormasMaximasDeUmGrupo; n++ {
						g.Quantas = n
						b.WriteString(LinhaDeContagem(g) + "\n")
					}
				}
			}
			return b.String()
		},
	} {
		r := aos537Medir(t, escrever)
		if r.VeredictoDoVideo.Veredicto != VeredictoRecusado || aos537Tipo(r, FactoCor).PresentesPelaRegra != 0 || aos537Tipo(r, FactoContagem).PresentesPelaRegra != 0 {
			t.Errorf("%s: %+v, cor %+v, contagem %+v", nome, r.VeredictoDoVideo, aos537Tipo(r, FactoCor), aos537Tipo(r, FactoContagem))
		}
	}
}

func TestAOS537_Revisao_UmaContradicaoFalhaSoEsseFacto(t *testing.T) {
	r := aos537Medir(t, func(v VideoDaBateria) string {
		s := DescricaoDeOrigem(v)
		switch v.ID {
		case "placas-de-texto":
			s = strings.Replace(s, "BACKGROUND 3: blue", "BACKGROUND 3: red/blue", 1)
		case "cores-por-cena":
			s = strings.Replace(s, "BACKGROUND 2: green", "BACKGROUND 2: green or yellow", 1)
			s += "BACKGROUND 4: black\n"
		case "contagem-de-formas":
			s += "COUNT: 5 red circles\nCOUNT: 2 or 3 blue squares\n"
		}
		return s
	})
	quer := map[string][]string{
		"placas-de-texto":    {"BACKGROUND 3: blue"},
		"cores-por-cena":     {"BACKGROUND 2: green", "BACKGROUND 4: yellow"},
		"contagem-de-formas": {"COUNT: 4 red circles", "COUNT: 2 blue squares"},
	}
	for _, l := range r.Videos {
		a := l.AusentesPelaRegra
		sort.Strings(a)
		q := quer[l.ID]
		sort.Strings(q)
		if strings.Join(a, "|") != strings.Join(q, "|") {
			t.Errorf("%s: ausentes %v (quer %v)", l.ID, a, q)
		}
	}
}

func TestAOS537_Revisao_AOrdemAlfabeticaFalhaOParDeOrdem(t *testing.T) {
	r := aos537Medir(t, func(v VideoDaBateria) string {
		// Só a secção FACTS, com os textos por ordem alfabética: quem ordena e não vê o tempo.
		var textos, resto []string
		for _, l := range strings.Split(DescricaoDeOrigem(v), "\n") {
			switch {
			case strings.HasPrefix(l, "TEXT: "):
				textos = append(textos, l)
			case strings.HasPrefix(l, "COUNT: "), strings.HasPrefix(l, "BACKGROUND "):
				resto = append(resto, l)
			}
		}
		sort.Strings(textos)
		return strings.Join(append(textos, resto...), "\n")
	})
	if a := aos537Ausentes(r, "passos-longo"); len(a) != 1 || a[0] != "ORDEM: PASSO 5 -> PASSO 2" {
		t.Errorf("passos-longo: ausentes %v", a)
	}
	if a := aos537Ausentes(r, "placas-de-texto"); len(a) != 0 {
		t.Errorf("uma ordem que coincide com a alfabetica passa: %v", a)
	}
}

// O MP4 da pasta montada só se lê se for um ficheiro regular.
func TestAOS537_Revisao_OMP4DaPastaMontadaEUmFicheiroRegular(t *testing.T) {
	pasta := t.TempDir()
	certo := filepath.Join(pasta, "certo.mp4")
	if err := os.WriteFile(certo, []byte("mp4"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := lerMP4DaPasta(certo); err != nil || string(b) != "mp4" {
		t.Errorf("ficheiro regular: %q %v", b, err)
	}
	dir := filepath.Join(pasta, "pasta.mp4")
	_ = os.Mkdir(dir, 0o700)
	if _, err := lerMP4DaPasta(dir); err == nil {
		t.Error("uma pasta no lugar do MP4 foi lida")
	}
	if _, err := lerMP4DaPasta(filepath.Join(pasta, "nao-existe.mp4")); err == nil {
		t.Error("um MP4 que nao existe")
	}
	ligacao := filepath.Join(pasta, "ligacao.mp4")
	if err := os.Symlink(certo, ligacao); err != nil {
		t.Logf("sem ligacoes simbolicas neste posto (%v): a recusa da ligacao fica por provar aqui", err)
		return
	}
	if _, err := lerMP4DaPasta(ligacao); err == nil {
		t.Error("uma ligacao simbolica foi seguida")
	}
}
