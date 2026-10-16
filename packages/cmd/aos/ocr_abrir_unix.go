//go:build unix

package main

// A abertura e as ligações físicas da imagem da `ocr_read` em unix (AOS-530). Ver ocr_tool.go.

import (
	"errors"
	"io/fs"
	"syscall"
)

// ocrFlagsDeAbertura acrescenta O_NONBLOCK à abertura só de leitura: um FIFO trocado pelo ficheiro
// entre o Lstat e a abertura não pendura a tool (a verificação no descritor recusa-o depois).
const ocrFlagsDeAbertura = syscall.O_NONBLOCK

// ligacoesMultiplas diz se o ficheiro tem mais de uma ligação física: o mesmo inode pode estar
// também fora da raiz, onde o os.Root não chega.
func ligacoesMultiplas(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}

// nomeDemasiadoLongo diz se o sistema recusou o nome por ser longo demais (ENAMETOOLONG).
func nomeDemasiadoLongo(err error) bool { return errors.Is(err, syscall.ENAMETOOLONG) }
