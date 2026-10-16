//go:build unix

package main

// AOS-530 (peça 3a, revisão) — os casos de leitura que só unix deixa montar com o disco real: uma
// ligação física (o mesmo inode também fora da raiz), uma ligação simbólica para DENTRO da raiz, um
// FIFO, e um ficheiro sem permissão de leitura.

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/aos-ref/platform/model-gateway/port"
)

func TestAOS530_OCR_LeituraUnix(t *testing.T) {
	fora, pasta := t.TempDir(), t.TempDir()
	png := aos530PNG(t)
	if err := os.WriteFile(filepath.Join(fora, "segredo.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pasta, "fatura.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}
	casos := map[string]string{}
	if os.Link(filepath.Join(fora, "segredo.png"), filepath.Join(pasta, "ligada.png")) == nil {
		casos["ligada.png"] = ocrCausaVariasLigacoes
	}
	if os.Symlink("fatura.png", filepath.Join(pasta, "atalho.png")) == nil {
		casos["atalho.png"] = ocrCausaNaoRegular
	}
	if syscall.Mkfifo(filepath.Join(pasta, "fifo.png"), 0o600) == nil {
		casos["fifo.png"] = ocrCausaNaoRegular
	}
	if os.Geteuid() != 0 {
		if err := os.WriteFile(filepath.Join(pasta, "fechada.png"), png, 0o000); err == nil {
			casos["fechada.png"] = ocrCausaIlegivel
		}
	}
	for doc, causa := range casos {
		t.Run(doc, func(t *testing.T) {
			gw := &aos530GwFalso{resp: aos530Resposta("texto", port.FinishStop, 1)}
			l := aos530LigacaoFalsa(t, pasta, gw)
			if _, _, err := l.executar(aos530ComCredencial(), aos530Entrada(doc)); causaDaFalhaOCR(err) != causa || gw.contagem() != 0 {
				t.Fatalf("%s: quer %s sem pedido; veio %v pedidos=%d", doc, causa, err, gw.contagem())
			}
		})
	}
}

// aos530RaizQueTroca é a raiz REAL da pasta, que troca o nome por uma ligação simbólica para OUTRO
// ficheiro da raiz no instante entre o Lstat e a abertura — a janela que um atacante com escrita na
// pasta aproveitaria. A identidade é a do os.SameFile.
type aos530RaizQueTroca struct {
	raizOS
	pasta string
}

func (r aos530RaizQueTroca) abrir(nome string) (ficheiroDoOCR, error) {
	if err := os.Remove(filepath.Join(r.pasta, nome)); err != nil {
		return nil, err
	}
	if err := os.Symlink("outra.png", filepath.Join(r.pasta, nome)); err != nil {
		return nil, err
	}
	return r.raizOS.abrir(nome)
}

func TestAOS530_OCR_TrocadoPorLigacaoSimbolicaDentroDaRaiz(t *testing.T) {
	pasta := t.TempDir()
	png := aos530PNG(t)
	for _, n := range []string{"fatura.png", "outra.png"} {
		if err := os.WriteFile(filepath.Join(pasta, n), png, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gw := &aos530GwFalso{resp: aos530Resposta("texto", port.FinishStop, 1)}
	l := aos530LigacaoFalsa(t, pasta, gw)
	l.raiz = aos530RaizQueTroca{raizOS: l.raiz.(raizOS), pasta: pasta}
	if _, _, err := l.executar(aos530ComCredencial(), aos530Entrada("fatura.png")); causaDaFalhaOCR(err) != ocrCausaTrocado || gw.contagem() != 0 {
		t.Fatalf("quer %s sem pedido; veio %v pedidos=%d", ocrCausaTrocado, err, gw.contagem())
	}
}
