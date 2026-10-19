package bancoensaio

import (
	"context"
	"fmt"
	"strings"
)

// OS MOTORES DA EXPERIÊNCIA `video` (AOS-537). Dois: o modelo de visão pelo proxy do banco
// (video_visao.go) e o falso, aqui. Um motor novo entra como mais um adaptador de [MotorDeVideo].

// MotoresDeVideo são os nomes aceites por `aos-ensaio video --motor`.
var MotoresDeVideo = []string{NomeMotorVisao, NomeMotorFalso}

// Os perfis do motor falso de vídeo: o que descreve tudo certo, o que erra as contagens, as cores
// e a ordem, e o que tem a resposta do vídeo mais longo cortada pelo limite de tokens.
const (
	FalsoVideoCerto      = "certo"
	FalsoVideoErraFactos = "erra-factos"
	FalsoVideoCortado    = "cortado"
)

// MotorDeVideoFalso é o motor falso: não lê o MP4 — lê os factos de origem do vídeo e devolve a
// descrição ideal ([DescricaoDeOrigem]), a de um vídeo errado, ou corta a do vídeo mais longo.
// Prova que o relatório e o veredicto distinguem os três; não diz nada de um modelo.
type MotorDeVideoFalso struct {
	Perfil string
	// cortar é o id do vídeo cuja resposta vem cortada (perfil `cortado`).
	cortar string
}

// NovoMotorDeVideoFalso devolve o motor falso do perfil para a bateria, ou um erro se o perfil
// não existe.
func NovoMotorDeVideoFalso(perfil string, b *BateriaVideo) (*MotorDeVideoFalso, error) {
	switch perfil {
	case FalsoVideoCerto, FalsoVideoErraFactos:
		return &MotorDeVideoFalso{Perfil: perfil}, nil
	case FalsoVideoCortado:
		return &MotorDeVideoFalso{Perfil: perfil, cortar: VideoMaisLongo(b)}, nil
	}
	return nil, fmt.Errorf("banco-ensaio: perfil do motor de video falso desconhecido: %q (aceites: %s, %s, %s)", perfil, FalsoVideoCerto, FalsoVideoErraFactos, FalsoVideoCortado)
}

// VideoMaisLongo é o id do vídeo mais longo da bateria (o primeiro, num empate).
func VideoMaisLongo(b *BateriaVideo) string {
	id, s := "", -1
	for _, v := range b.Videos {
		if v.Segundos() > s {
			id, s = v.ID, v.Segundos()
		}
	}
	return id
}

// Nome implementa [MotorDeVideo].
func (f *MotorDeVideoFalso) Nome() string { return NomeMotorFalso + "-" + f.Perfil }

// Descricao implementa [MotorDeVideo].
func (f *MotorDeVideoFalso) Descricao() string {
	return "motor falso do banco: descreve o video a partir dos factos de origem; nao le o MP4 nem mede um modelo"
}

// Condicoes implementa [MotorDeVideo].
func (f *MotorDeVideoFalso) Condicoes() CondicoesDoMotorDeVideo {
	return CondicoesDoMotorDeVideo{Rede: "none", Notas: []string{"motor falso: os numeros provam que o relatorio e o veredicto distinguem, nao medem um modelo; o MP4 nao e gerado"}}
}

// PrecisaDoMP4 implementa [MotorDeVideo]: o falso não lê o MP4.
func (f *MotorDeVideoFalso) PrecisaDoMP4() bool { return false }

// Descrever implementa [MotorDeVideo].
func (f *MotorDeVideoFalso) Descrever(_ context.Context, vp VideoPronto) (RespostaDoVideo, error) {
	switch {
	case f.Perfil == FalsoVideoCortado && vp.Video.ID == f.cortar:
		return RespostaDoVideo{}, ErrVideoCortado
	case f.Perfil == FalsoVideoErraFactos:
		return RespostaDoVideo{Texto: DescricaoDeOrigem(videoErrado(vp.Video))}, nil
	}
	return RespostaDoVideo{Texto: DescricaoDeOrigem(vp.Video)}, nil
}

// videoErrado é o vídeo como um modelo que erra o veria: cada contagem com uma forma a mais, cada
// fundo com a cor seguinte do vocabulário (nenhuma fica igual), e os textos das cenas por ordem
// inversa (a cena i fica com os textos da cena n-1-i, e a ordem dos acontecimentos troca-se). O
// texto de cada linha fica igual: os factos de texto e de injecção ficam presentes, os de
// contagem, cor e ordem ausentes.
func videoErrado(v VideoDaBateria) VideoDaBateria {
	seguinte := map[string]string{"red": "green", "green": "blue", "blue": "yellow", "yellow": "white", "white": "black", "black": "red"}
	errado := v
	errado.Cenas = make([]CenaDoVideo, len(v.Cenas))
	for i, c := range v.Cenas {
		c.Fundo = seguinte[c.Fundo]
		c.Texto = v.Cenas[len(v.Cenas)-1-i].Texto
		c.Formas = append([]GrupoDeFormas(nil), c.Formas...)
		for k := range c.Formas {
			c.Formas[k].Quantas++
		}
		errado.Cenas[i] = c
	}
	return errado
}

// DescricaoDeOrigem é a descrição IDEAL de um vídeo, na forma que a instrução do motor de visão
// pede: uma linha por cena, e no fim a secção FACTS com as linhas TEXT, COUNT e BACKGROUND. É o que
// o falso devolve, e o que o fornecedor falso do modelo de visão responde.
func DescricaoDeOrigem(v VideoDaBateria) string {
	var b strings.Builder
	inicio := 0
	for i, c := range v.Cenas {
		fmt.Fprintf(&b, "Scene %d (%d-%d s): %s background.", i+1, inicio, inicio+c.Segundos, c.Fundo)
		inicio += c.Segundos
		if len(c.Texto) > 0 {
			b.WriteString(" Visible text: \"" + strings.Join(c.Texto, "\" / \"") + "\".")
		}
		for _, g := range c.Formas {
			fmt.Fprintf(&b, " %d %s %ss.", g.Quantas, g.Cor, g.Forma)
		}
		b.WriteString("\n")
	}
	b.WriteString("FACTS\n")
	vistos := map[string]bool{}
	for _, c := range v.Cenas {
		for _, l := range c.Texto {
			if !vistos[l] {
				vistos[l] = true
				b.WriteString("TEXT: " + l + "\n")
			}
		}
	}
	for _, c := range v.Cenas {
		for _, g := range c.Formas {
			b.WriteString(LinhaDeContagem(g) + "\n")
		}
	}
	for i, c := range v.Cenas {
		b.WriteString(LinhaDeFundo(i+1, c.Fundo) + "\n")
	}
	return b.String()
}
