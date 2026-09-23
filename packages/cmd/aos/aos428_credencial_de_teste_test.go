package main

// aos428_credencial_de_teste_test.go — A CUNHAGEM QUE OS TESTES DE SUBMISSÃO PASSARAM A PRECISAR.
//
// # PORQUÊ
//
// Desde o AOS-428 o `POST /runs` VERIFICA a credencial do run antes de criar o run. Trinta e oito
// testes submetiam sem credencial nenhuma e esperavam `201` — e passavam porque a verificação
// acontecia só no primeiro turno de modelo, que esses testes não alcançavam.
//
// Podiam ter sido salvos com um seam que desligasse a guarda. Não foram, de propósito: um seam
// desses deixaria trinta e oito testes a exercitar um caminho que a produção não tem, e criaria
// uma porta que alguém podia ligar em produção — a classe de defeito que o AOS-424 passou uma
// série inteira a fechar.
//
// Cunham uma credencial REAL, pela autoridade do próprio nó de teste. Passam a exercitar o
// caminho que o `aos-orq` usa.

import (
	"context"
	"testing"
)

// credencialDeTeste cunha um NHI válido para o nó dado.
//
// Devolve "" quando o nó não tem autoridade composta — o ramo ENDURECIDO não a tem, por desenho
// (trust-anchor-only, AOS-156). Um teste nesse modo tem de trazer a sua credencial de fora, tal
// como a produção traz.
func credencialDeTeste(t *testing.T, node *Node) string {
	t.Helper()
	if node == nil || node.Authority == nil {
		return ""
	}
	tok, err := node.Authority.MintForHuman(context.Background(), tnHuman, tnAgent, tnClass, []string{tnCap})
	if err != nil {
		t.Fatalf("cunhar credencial de teste: %v", err)
	}
	return tok.Compact
}
