package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// Achado A2 da auditoria de 2026-08-17. Uma colisão de `run_id` respondia SEMPRE `201 accepted`:
// o segundo submissor perdia o trabalho EM SILÊNCIO e, ao consultar o resultado, lia o run de
// OUTRA pessoa. Observado em produção.
//
// O `201` uniforme existia para não ser um ORÁCULO DE EXISTÊNCIA a um chamador ANÓNIMO — a
// premissa de ADR-016, quando o plano de dados não autenticava. Sob credencial FORTE composta
// essa premissa deixa de valer, e sobra só o custo.
//
// A correcção NÃO é "409 quando autenticado": é "409 a quem PODERIA LER este run". Um 409 a quem
// o GET esconde abriria por POST a porta que a soberania fecha. Estes testes exercitam as três
// faces — o 409 que passa a existir, o 201 que TEM de continuar a esconder, e a retro-compat.

const (
	a2BoardEU = "board:a2-eu"
	a2BoardUS = "board:a2-alt"
	a2RegEU   = "eu-west"
	a2RegUS   = "us-east"
)

// credDeHeaders é um [readCredentialVerifier] de teste: deriva o par (principal, board) dos
// headers de leitura. NÃO é a via legada — o que importa para o código sob teste é que
// `readGov.cred != nil`, ou seja, que a credencial forte está COMPOSTA. Isto permite variar o
// board por pedido sem montar um IdP.
type credDeHeaders struct{}

func (credDeHeaders) verify(_ context.Context, r *http.Request) (string, string, error) {
	p := r.Header.Get(HeaderReaderPrincipal)
	b := r.Header.Get(HeaderReaderBoard)
	if p == "" || b == "" {
		return "", "", fmt.Errorf("credencial de teste ausente")
	}
	return p, b, nil
}

// newA2Handler compõe um nó com DOIS boards em regiões DIFERENTES e, quando `cred != nil`, a
// CREDENCIAL FORTE composta — a topologia mínima para distinguir "pode ler" de "está
// autenticado", que é a distinção que a correcção faz.
func newA2Handler(t *testing.T, cred readCredentialVerifier) (*NodeService, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	cfg := tnBaseConfig()
	cfg.Model = &a2FinalModel{}
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.BoardRegions = map[string]string{a2BoardEU: a2RegEU, a2BoardUS: a2RegUS}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })

	auth, err := NewSovereignRegionAuthority(context.Background(),
		map[string]string{a2BoardEU: a2RegEU, a2BoardUS: a2RegUS}, node.WORM, nil)
	if err != nil {
		t.Fatalf("NewSovereignRegionAuthority: %v", err)
	}
	svc, err := NewNodeService(node)
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	h, err := NewAPIHandler(svc, node, WithSovereignAuthority(auth, cred, node.WORM))
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	return svc, h
}

type a2FinalModel struct{}

func (*a2FinalModel) Call(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return agentruntime.ModelResponse{Text: "ok", Final: true,
		Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

func a2Headers(board string) map[string]string {
	return map[string]string{HeaderReaderPrincipal: "nhi:a2-" + board, HeaderReaderBoard: board}
}

// TestA2_ColisaoNaMesmaRegiao_DevolveConflito: o achado. Um segundo submissor da MESMA região
// deixa de receber "accepted" para uma submissão que foi descartada.
func TestA2_ColisaoNaMesmaRegiao_DevolveConflito(t *testing.T) {
	_, h := newA2Handler(t, credDeHeaders{})
	const runID = "run-a2-colisao"

	if rec := postReq(h, "/runs", submitRequest{RunID: runID, Objective: "primeiro"}, a2Headers(a2BoardEU)); rec.Code != http.StatusCreated {
		t.Fatalf("a 1a submissao devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
	rec := postReq(h, "/runs", submitRequest{RunID: runID, Objective: "SEGUNDO, diferente"}, a2Headers(a2BoardEU))
	if rec.Code != http.StatusConflict {
		t.Fatalf("colisao de run_id com chamador que PODE LER o run devia dar 409, veio %d (%s)", rec.Code, rec.Body.String())
	}
	// A mensagem diz que existe, e mais nada — quem chega aqui já podia sabê-lo pelo GET.
	if body := rec.Body.String(); body == "" {
		t.Fatal("o 409 tem de trazer corpo: o ponto da correccao e o chamador SABER que perdeu a submissao")
	}
}

// TestA2_ColisaoDeOutraRegiao_ContinuaUniforme: a face que impede a correcção de abrir um
// buraco. Um submissor de OUTRA região não pode ler este run — o GET devolve-lhe `404` uniforme
// — logo o POST também não lhe pode revelar que ele existe.
func TestA2_ColisaoDeOutraRegiao_ContinuaUniforme(t *testing.T) {
	_, h := newA2Handler(t, credDeHeaders{})
	const runID = "run-a2-cross"

	if rec := postReq(h, "/runs", submitRequest{RunID: runID, Objective: "residente em eu-west"}, a2Headers(a2BoardEU)); rec.Code != http.StatusCreated {
		t.Fatalf("a 1a submissao devia dar 201, veio %d", rec.Code)
	}
	rec := postReq(h, "/runs", submitRequest{RunID: runID, Objective: "de outra regiao"}, a2Headers(a2BoardUS))
	if rec.Code != http.StatusCreated {
		t.Fatalf("um 409 a quem NAO pode ler o run abriria por POST o que o GET esconde; devia dar 201, veio %d (%s)",
			rec.Code, rec.Body.String())
	}

	// Âncora de não-vacuidade: confirma que este leitor REALMENTE não pode ler o run — sem isto,
	// o 201 acima poderia estar certo por acaso (por o run não existir, por exemplo).
	if got := getReq(h, "/runs/"+runID, a2Headers(a2BoardUS)); got.Code != http.StatusNotFound {
		t.Fatalf("o leitor de outra regiao tem de receber 404 no GET (senao o teste acima nao prova nada), veio %d", got.Code)
	}
	if got := getReq(h, "/runs/"+runID, a2Headers(a2BoardEU)); got.Code != http.StatusOK {
		t.Fatalf("o leitor da regiao de residencia tem de conseguir ler, veio %d", got.Code)
	}
}

// TestA2_SemCredencialForte_MantemUniforme: retro-compat. Sem credencial forte composta o plano
// de dados volta a poder ter chamadores não autenticados, e a premissa do oráculo de ADR-016
// continua verdadeira — o `201` uniforme mantém-se.
func TestA2_SemCredencialForte_MantemUniforme(t *testing.T) {
	_, h := newA2Handler(t, nil) // cred nil ⇒ via legada por headers
	const runID = "run-a2-legado"

	if rec := postReq(h, "/runs", submitRequest{RunID: runID, Objective: "primeiro"}, a2Headers(a2BoardEU)); rec.Code != http.StatusCreated {
		t.Fatalf("a 1a submissao devia dar 201, veio %d", rec.Code)
	}
	if rec := postReq(h, "/runs", submitRequest{RunID: runID, Objective: "segundo"}, a2Headers(a2BoardEU)); rec.Code != http.StatusCreated {
		t.Fatalf("sem credencial forte o 201 uniforme TEM de se manter (o oraculo ainda tem a quem revelar), veio %d", rec.Code)
	}
}
