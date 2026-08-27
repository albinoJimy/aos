package main

// STATUS DA RECUSA DE LEITURA — separar «a tua credencial não presta» de «não há aqui nada».
//
// `GET /runs/{id}` devolvia `404` para quatro situações: credencial recusada, board desconhecido,
// cross-region, e run inexistente. As três últimas PODEM revelar a existência de um run alheio e
// por isso têm de ficar indistinguíveis. A primeira NÃO PODE revelar nada — é decidida só pela
// credencial, antes de qualquer consulta de existência.
//
// O custo da agregação foi medido em produção a 2026-08-26: o token de leitura é de USO ÚNICO
// (anti-replay de `jti`), pelo que reutilizá-lo devolvia `404` — lido como «o run desapareceu».
//
// Estes testes trancam as DUAS metades. Sem a segunda, propagar o status podia alargar-se às
// recusas de governação sem nada ficar vermelho — e aí a correcção teria aberto enumeração.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aos-ref/integration/oidc"
)

// boardDoTeste é o único board que a governança destes testes conhece. Um teste que queira
// observar a recusa da CREDENCIAL nunca deve tropeçar na resolução do board.
const boardDoTeste = "board:admissao"

// govRecusadoraPara compõe a governança mínima com uma credencial que falha SEMPRE com `err`, e
// uma autoridade board→região que RESOLVE — para que a causa observada seja a credencial e não a
// governação. Reutiliza o [regioesFixas] que o pacote já tem.
func govRecusadoraPara(err error) *readGovernance {
	return &readGovernance{
		cred:    credQueFalha{err: err},
		regions: regioesFixas{boardDoTeste: "eu"},
	}
}

// TestAdmissaoLeitura_CredencialRecusadaDa401 é o caso que motivou a mudança: um token
// reutilizado, expirado ou de outra chave passa a ser distinguível de um run inexistente.
func TestAdmissaoLeitura_CredencialRecusadaDa401(t *testing.T) {
	for _, c := range []struct {
		nome string
		err  error
	}{
		{"replay de jti", oidc.ErrTokenReplayed},
		{"claim board em falta", ErrNoBoardClaim},
	} {
		t.Run(c.nome, func(t *testing.T) {
			g := govRecusadoraPara(c.err)

			_, _, causa := g.authorizeReadComCausa(t.Context(), httptest.NewRequest(http.MethodGet, "/runs/x", nil), "run-x")

			if causa != recusaCredencial {
				t.Fatalf("uma credencial APRESENTADA e recusada tem de dar recusaCredencial (401), veio %d", causa)
			}
		})
	}
}

// TestAdmissaoLeitura_SemCredencialFicaEm404 — a leitura anónima NÃO ganha resposta distinta.
//
// É o mesmo predicado que a exclui do log do operador: quem não apresentou credencial está a
// sondar, e distinguir «este endpoint quer autenticação» de «não há aqui nada» é precisamente o
// que a não-enumerabilidade recusa primeiro.
func TestAdmissaoLeitura_SemCredencialFicaEm404(t *testing.T) {
	g := govRecusadoraPara(ErrNoReadCredential)

	_, _, causa := g.authorizeReadComCausa(t.Context(), httptest.NewRequest(http.MethodGet, "/runs/x", nil), "run-x")

	if causa != recusaGovernacao {
		t.Fatalf("sem credencial tem de continuar indistinguivel de 'nao existe' (404), veio %d", causa)
	}
}

// TestAdmissaoLeitura_BoardDesconhecidoFicaEm404 é a metade que impede a correcção de abrir
// enumeração pelo outro lado: a credencial verifica, mas a GOVERNAÇÃO nega.
//
// Sem este caso, alargar `recusaCredencial` a toda a recusa passaria os outros testes — e um
// board desconhecido responderia diferente de um run inexistente.
func TestAdmissaoLeitura_BoardDesconhecidoFicaEm404(t *testing.T) {
	g := &readGovernance{
		cred: credQueAceita{board: "board:desconhecido"},
		// A autoridade só conhece o board do teste — logo o board da credencial NÃO resolve.
		regions: regioesFixas{boardDoTeste: "eu"},
	}

	_, _, causa := g.authorizeReadComCausa(t.Context(), httptest.NewRequest(http.MethodGet, "/runs/x", nil), "run-x")

	if causa != recusaGovernacao {
		t.Fatalf("board desconhecido tem de ficar indistinguivel de 'nao existe' (404), veio %d", causa)
	}
}

// credQueAceita verifica SEMPRE com sucesso — para isolar a recusa na governação.
type credQueAceita struct{ board string }

func (c credQueAceita) verify(_ context.Context, _ *http.Request) (string, string, error) {
	return "reader:teste", c.board, nil
}
