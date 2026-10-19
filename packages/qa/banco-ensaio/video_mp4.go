package bancoensaio

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// A FORMA DE UM MP4 (AOS-537). O banco não confia no que o ffmpeg devolve: antes de um vídeo ir
// ao modelo, lê as caixas do MP4 e confere que é o vídeo da bateria — a caixa `ftyp` à cabeça, a
// `moov` antes da `mdat` (o `+faststart`), a duração da `mvhd` e as dimensões da `tkhd` da faixa
// de vídeo. Só lê caixas; não descodifica imagem nenhuma.

// FormaDoMP4 é o que se lê das caixas de um MP4.
type FormaDoMP4 struct {
	// Marca é a `major_brand` da caixa `ftyp` (`isom`, …).
	Marca string
	// MoovAntesDoMdat diz que o índice vem antes dos dados: o vídeo lê-se sem chegar ao fim.
	MoovAntesDoMdat bool
	// DuracaoMs é a duração da `mvhd`, em milissegundos.
	DuracaoMs int64
	// Largura e Altura são as da primeira `tkhd` com dimensões (a faixa de vídeo), em píxeis.
	Largura, Altura int
}

// ErrMP4 — os bytes não são um MP4 que o banco aceite.
var ErrMP4 = errors.New("banco-ensaio: MP4 invalido")

// caixaMP4 é uma caixa: o tipo e o corpo.
type caixaMP4 struct {
	tipo  string
	corpo []byte
}

// caixasMP4 parte uns bytes nas caixas de um nível. Fail-closed: um tamanho que sai dos bytes é
// um erro.
func caixasMP4(b []byte) ([]caixaMP4, error) {
	var out []caixaMP4
	for len(b) > 0 {
		if len(b) < 8 {
			return nil, fmt.Errorf("%w: caixa truncada", ErrMP4)
		}
		tamanho := uint64(binary.BigEndian.Uint32(b))
		tipo := string(b[4:8])
		cabeca := uint64(8)
		switch tamanho {
		case 0:
			tamanho = uint64(len(b))
		case 1:
			if len(b) < 16 {
				return nil, fmt.Errorf("%w: caixa %q truncada", ErrMP4, tipo)
			}
			tamanho, cabeca = binary.BigEndian.Uint64(b[8:]), 16
		}
		if tamanho < cabeca || tamanho > uint64(len(b)) {
			return nil, fmt.Errorf("%w: a caixa %q diz %d bytes e restam %d", ErrMP4, tipo, tamanho, len(b))
		}
		out = append(out, caixaMP4{tipo: tipo, corpo: b[cabeca:tamanho]})
		b = b[tamanho:]
	}
	return out, nil
}

// InspeccionarMP4 lê a forma de um MP4.
func InspeccionarMP4(b []byte) (FormaDoMP4, error) {
	var f FormaDoMP4
	topo, err := caixasMP4(b)
	if err != nil {
		return f, err
	}
	if len(topo) == 0 || topo[0].tipo != "ftyp" || len(topo[0].corpo) < 4 {
		return f, fmt.Errorf("%w: nao abre pela caixa ftyp", ErrMP4)
	}
	f.Marca = string(topo[0].corpo[:4])
	moov, mdat := -1, -1
	for i, c := range topo {
		switch {
		case c.tipo == "moov" && moov < 0:
			moov = i
		case c.tipo == "mdat" && mdat < 0:
			mdat = i
		}
	}
	if moov < 0 || mdat < 0 {
		return f, fmt.Errorf("%w: sem as caixas moov e mdat", ErrMP4)
	}
	f.MoovAntesDoMdat = moov < mdat
	dentro, err := caixasMP4(topo[moov].corpo)
	if err != nil {
		return f, err
	}
	temMvhd := false
	for _, c := range dentro {
		switch c.tipo {
		case "mvhd":
			if f.DuracaoMs, err = duracaoDaMvhd(c.corpo); err != nil {
				return f, err
			}
			temMvhd = true
		case "trak":
			if f.Largura > 0 {
				continue
			}
			faixa, err := caixasMP4(c.corpo)
			if err != nil {
				return f, err
			}
			for _, t := range faixa {
				if t.tipo == "tkhd" {
					if f.Largura, f.Altura, err = dimensoesDaTkhd(t.corpo); err != nil {
						return f, err
					}
				}
			}
		}
	}
	if !temMvhd {
		return f, fmt.Errorf("%w: sem a caixa mvhd", ErrMP4)
	}
	return f, nil
}

// duracaoDaMvhd lê a duração de uma `mvhd` (versão 0 ou 1), em milissegundos.
func duracaoDaMvhd(c []byte) (int64, error) {
	var escala, duracao uint64
	switch {
	case len(c) >= 20 && c[0] == 0:
		escala, duracao = uint64(binary.BigEndian.Uint32(c[12:])), uint64(binary.BigEndian.Uint32(c[16:]))
	case len(c) >= 32 && c[0] == 1:
		escala, duracao = uint64(binary.BigEndian.Uint32(c[20:])), binary.BigEndian.Uint64(c[24:])
	default:
		return 0, fmt.Errorf("%w: mvhd ilegivel", ErrMP4)
	}
	if escala == 0 || duracao > 1<<40 {
		return 0, fmt.Errorf("%w: mvhd sem escala de tempo ou com uma duracao absurda", ErrMP4)
	}
	return int64(duracao * 1000 / escala), nil // #nosec G115 -- duracao limitada acima a 2^40 e escala >= 1
}

// dimensoesDaTkhd lê a largura e a altura de uma `tkhd` (versão 0 ou 1), em píxeis inteiros.
func dimensoesDaTkhd(c []byte) (int, int, error) {
	pos := 76
	if len(c) > 0 && c[0] == 1 {
		pos = 88
	}
	if len(c) < pos+8 {
		return 0, 0, fmt.Errorf("%w: tkhd ilegivel", ErrMP4)
	}
	return int(binary.BigEndian.Uint32(c[pos:]) >> 16), int(binary.BigEndian.Uint32(c[pos+4:]) >> 16), nil
}

// conferirMP4 confere que a forma do MP4 é a do vídeo da bateria: o índice à cabeça, as
// dimensões exactas e a duração com a folga de um fotograma (e 100 ms do arredondamento).
func conferirMP4(f FormaDoMP4, v VideoDaBateria) error {
	esperada := int64(v.Segundos()) * 1000
	folga := int64(1000/v.FPS) + 100
	switch {
	case !f.MoovAntesDoMdat:
		return fmt.Errorf("%w: a caixa moov vem depois da mdat (sem +faststart)", ErrMP4)
	case f.Largura != v.Largura || f.Altura != v.Altura:
		return fmt.Errorf("%w: %dx%d em vez de %dx%d", ErrMP4, f.Largura, f.Altura, v.Largura, v.Altura)
	case f.DuracaoMs < esperada-folga || f.DuracaoMs > esperada+folga:
		return fmt.Errorf("%w: dura %d ms em vez de %d ms", ErrMP4, f.DuracaoMs, esperada)
	}
	return nil
}
