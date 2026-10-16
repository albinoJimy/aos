//go:build !unix

package main

// A abertura e as ligações físicas da imagem da `ocr_read` fora de unix (AOS-530; no Windows só
// em desenvolvimento). Ver ocr_tool.go e ocr_abrir_unix.go.

import "io/fs"

// ocrFlagsDeAbertura não acrescenta nada: não há O_NONBLOCK nem FIFOs na pasta semeada.
const ocrFlagsDeAbertura = 0

// ligacoesMultiplas não sabe contar as ligações físicas fora de unix: nunca recusa por elas.
func ligacoesMultiplas(fs.FileInfo) bool { return false }

// nomeDemasiadoLongo não reconhece o erro fora de unix; o tecto [ocrMaxNome] recusa antes.
func nomeDemasiadoLongo(error) bool { return false }
