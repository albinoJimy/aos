package main

// AOS-530 (peça 3a, revisão) — A LEITURA DA IMAGEM, robusta a trocas e com a causa certa.
//
// Uma raiz FALSA ([aos530RaizFalsa]) simula o que o disco não deixa simular em todos os sistemas:
// um erro de permissão, um ficheiro trocado entre o Lstat e a abertura, uma ligação simbólica no
// Windows sem privilégio. Os casos reais de unix (ligação física, ligação simbólica para dentro da
// raiz) estão em aos530_ocr_leitura_unix_test.go.

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aos-ref/platform/model-gateway/port"
)

// aos530Info é um fs.FileInfo com o modo e o tamanho dados.
type aos530Info struct {
	modo fs.FileMode
	tam  int64
}

func (i aos530Info) Name() string       { return "x" }
func (i aos530Info) Size() int64        { return i.tam }
func (i aos530Info) Mode() fs.FileMode  { return i.modo }
func (i aos530Info) ModTime() time.Time { return time.Time{} }
func (i aos530Info) IsDir() bool        { return i.modo.IsDir() }
func (i aos530Info) Sys() any           { return nil }

// aos530Ficheiro é um ficheiro aberto falso: o Stat do descritor e a leitura são os dados.
type aos530Ficheiro struct {
	r       io.Reader
	info    fs.FileInfo
	statErr error
}

func (f *aos530Ficheiro) Read(p []byte) (int, error) { return f.r.Read(p) }
func (f *aos530Ficheiro) Stat() (fs.FileInfo, error) { return f.info, f.statErr }
func (f *aos530Ficheiro) Close() error               { return nil }

// aos530LeituraFalhada é um leitor que falha a meio.
type aos530LeituraFalhada struct{}

func (aos530LeituraFalhada) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Err: errors.New("erro de E/S")}
}

// aos530RaizFalsa conta as aberturas e devolve o que lhe mandarem.
type aos530RaizFalsa struct {
	lstatInfo fs.FileInfo
	lstatErr  error
	ficheiro  ficheiroDoOCR
	abrirErr  error
	aberturas int
}

func (r *aos530RaizFalsa) Lstat(string) (fs.FileInfo, error) { return r.lstatInfo, r.lstatErr }
func (r *aos530RaizFalsa) abrir(string) (ficheiroDoOCR, error) {
	r.aberturas++
	if r.abrirErr != nil {
		return nil, r.abrirErr
	}
	return r.ficheiro, nil
}

// O ficheiro regular e pequeno que o Lstat vê, e o PNG válido que a leitura daria: se uma defesa
// cedesse, a tool transcrevia.
func aos530Regular(t *testing.T) (fs.FileInfo, []byte) {
	png := aos530PNG(t)
	return aos530Info{tam: int64(len(png))}, png
}

func TestAOS530_OCR_LeituraComARaizFalsa(t *testing.T) {
	semPermissao := &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission}
	casos := []struct {
		nome      string
		raiz      func(info fs.FileInfo, png []byte) *aos530RaizFalsa
		causa     string
		aberturas int
	}{
		{"sem permissao no lstat", func(fs.FileInfo, []byte) *aos530RaizFalsa {
			return &aos530RaizFalsa{lstatErr: &fs.PathError{Op: "lstat", Path: "x", Err: fs.ErrPermission}}
		}, ocrCausaIlegivel, 0},
		{"sem permissao na abertura", func(info fs.FileInfo, _ []byte) *aos530RaizFalsa {
			return &aos530RaizFalsa{lstatInfo: info, abrirErr: semPermissao}
		}, ocrCausaIlegivel, 1},
		{"stat do descritor falha", func(info fs.FileInfo, png []byte) *aos530RaizFalsa {
			return &aos530RaizFalsa{lstatInfo: info, ficheiro: &aos530Ficheiro{r: bytes.NewReader(png), statErr: errors.New("x")}}
		}, ocrCausaIlegivel, 1},
		{"leitura falha", func(info fs.FileInfo, _ []byte) *aos530RaizFalsa {
			return &aos530RaizFalsa{lstatInfo: info, ficheiro: &aos530Ficheiro{r: aos530LeituraFalhada{}, info: info}}
		}, ocrCausaIlegivel, 1},
		{"escape recusado pela raiz", func(fs.FileInfo, []byte) *aos530RaizFalsa {
			return &aos530RaizFalsa{lstatErr: &fs.PathError{Op: "lstat", Path: "x", Err: errors.New("path escapes from parent")}}
		}, ocrCausaForaDaRaiz, 0},
		{"em falta", func(fs.FileInfo, []byte) *aos530RaizFalsa {
			return &aos530RaizFalsa{lstatErr: &fs.PathError{Op: "lstat", Path: "x", Err: fs.ErrNotExist}}
		}, ocrCausaEmFalta, 0},
		// UMA LIGAÇÃO SIMBÓLICA NO TOPO, mesmo para um ficheiro regular dentro da raiz: recusada no
		// Lstat, sem abrir.
		{"ligacao simbolica", func(_ fs.FileInfo, png []byte) *aos530RaizFalsa {
			return &aos530RaizFalsa{lstatInfo: aos530Info{modo: fs.ModeSymlink, tam: 10},
				ficheiro: &aos530Ficheiro{r: bytes.NewReader(png), info: aos530Info{tam: int64(len(png))}}}
		}, ocrCausaNaoRegular, 0},
		// TROCADO ENTRE O LSTAT E A ABERTURA: o nome era um ficheiro regular pequeno; o descritor
		// aberto é um FIFO, ou um ficheiro grande. A decisão é sobre o descritor.
		{"descritor nao regular", func(info fs.FileInfo, png []byte) *aos530RaizFalsa {
			return &aos530RaizFalsa{lstatInfo: info, ficheiro: &aos530Ficheiro{r: bytes.NewReader(png), info: aos530Info{modo: fs.ModeNamedPipe}}}
		}, ocrCausaNaoRegular, 1},
		{"descritor grande", func(info fs.FileInfo, png []byte) *aos530RaizFalsa {
			return &aos530RaizFalsa{lstatInfo: info, ficheiro: &aos530Ficheiro{r: bytes.NewReader(png), info: aos530Info{tam: port.MaxImageBytes + 1}}}
		}, ocrCausaGrande, 1},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			info, png := aos530Regular(t)
			raiz := c.raiz(info, png)
			gw := &aos530GwFalso{resp: aos530Resposta("texto", port.FinishStop, 1)}
			l := aos530LigacaoFalsa(t, t.TempDir(), gw)
			l.raiz = raiz
			_, custo, err := l.executar(aos530ComCredencial(), aos530Entrada("fatura.png"))
			if causaDaFalhaOCR(err) != c.causa || custo != 0 || gw.contagem() != 0 {
				t.Fatalf("quer %s sem pedido; veio %v custo=%d pedidos=%d", c.causa, err, custo, gw.contagem())
			}
			if raiz.aberturas != c.aberturas {
				t.Fatalf("aberturas: quer %d, vieram %d", c.aberturas, raiz.aberturas)
			}
		})
	}
	// Controlo: a mesma raiz falsa, sem nada trocado, transcreve — os casos acima não falham por
	// outra razão.
	info, png := aos530Regular(t)
	gw := &aos530GwFalso{resp: aos530Resposta("texto", port.FinishStop, 1)}
	l := aos530LigacaoFalsa(t, t.TempDir(), gw)
	l.raiz = &aos530RaizFalsa{lstatInfo: info, ficheiro: &aos530Ficheiro{r: bytes.NewReader(png), info: info}}
	if _, _, err := l.executar(aos530ComCredencial(), aos530Entrada("fatura.png")); err != nil || gw.contagem() != 1 {
		t.Fatalf("o controlo tinha de transcrever: %v", err)
	}
}

// UM NOME QUE O SISTEMA NORMALIZA. No Windows, `fatura.png.` e `fatura.png ` abrem `fatura.png`:
// o ficheiro lido não seria o nomeado no recurso selado. Recusados em todos os sistemas, sem
// pedido; o nome exacto lê-se.
func TestAOS530_OCR_NomeQueOSistemaNormaliza(t *testing.T) {
	pasta := t.TempDir()
	if err := os.WriteFile(filepath.Join(pasta, "fatura.png"), aos530PNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, doc := range []string{"fatura.png.", "fatura.png ", "fatura.png..", "fatura.png. "} {
		gw := &aos530GwFalso{resp: aos530Resposta("texto", port.FinishStop, 1)}
		l := aos530LigacaoFalsa(t, pasta, gw)
		if _, _, err := l.executar(aos530ComCredencial(), aos530Entrada(doc)); causaDaFalhaOCR(err) != ocrCausaNomeNaoCanonico || gw.contagem() != 0 {
			t.Fatalf("%q: quer %s sem pedido; veio %v pedidos=%d", doc, ocrCausaNomeNaoCanonico, err, gw.contagem())
		}
	}
	gw := &aos530GwFalso{resp: aos530Resposta("texto", port.FinishStop, 1)}
	if _, _, err := aos530LigacaoFalsa(t, pasta, gw).executar(aos530ComCredencial(), aos530Entrada("fatura.png")); err != nil {
		t.Fatalf("o nome exacto tinha de ler: %v", err)
	}
}
