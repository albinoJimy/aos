// Package evasion documenta o LIMITE do lint de separação (AOS021-Q3): reúne as
// formas de efeito externo que a heurística sintáctica `pkgIdent.Fn` NÃO apanha —
// import aliasado, método sobre valor de cliente, e valor de função. Serve de entrada
// ao analisador para um teste que assevera que estas evasões dão 0 violações,
// tornando EXPLÍCITO o que o lint (segunda camada) não cobre — a garantia forte é
// ESTRUTURAL (ver cabeçalho do pacote separation). NÃO é compilado pelo módulo (vive
// em testdata).
package evasion

import (
	h "net/http" // import ALIASADO: o ident passa a ser "h", não "http".
	"os"
)

// aliasedNet usa um import aliasado — h.Get não casa "http.Get" no mapa do lint.
func aliasedNet(url string) error {
	resp, err := h.Get(url) // EVASÃO 1: aliased import (não apanhado).
	if err != nil {
		return err
	}
	_ = resp
	return nil
}

// clientMethod usa o método idiomático (*http.Client).Do — selector sobre um VALOR,
// não sobre o ident do pacote; o lint não o alcança.
func clientMethod(req *h.Request) error {
	client := &h.Client{}
	resp, err := client.Do(req) // EVASÃO 2: método sobre valor de cliente (não apanhado).
	if err != nil {
		return err
	}
	_ = resp
	return nil
}

// funcValue guarda a primitiva de I/O num VALOR de função e invoca-o — a chamada é
// `f(p)`, não `os.Open(p)`; o lint não a apanha.
func funcValue(p string) error {
	f := os.Open // EVASÃO 3: valor de função (a atribuição não é uma chamada).
	file, err := f(p)
	if err != nil {
		return err
	}
	_ = file
	return nil
}
