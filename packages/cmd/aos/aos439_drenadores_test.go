package main

// aos439_drenadores_test.go — QUEM DRENA A FILA É UMA LISTA FECHADA (AOS-439, pré-requisito).
//
// Antes: qualquer identidade autenticada da região do pedido reclamava pedidos ALHEIOS e recebia o
// objectivo DECIFRADO, e fechava-os com o desfecho que quisesse. Estes testes provam a recusa, a
// uniformidade dela, e a âncora anti-vacuidade (o drenador listado continua a drenar).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// intrusoHeaders é uma identidade AUTENTICADA (board válido, mesma região do pedido) que NÃO
// consta da lista de drenadores — um utilizador que submete planos, ou outro service account.
func intrusoHeaders() map[string]string {
	return map[string]string{HeaderReaderPrincipal: "nhi:intruso-439", HeaderReaderBoard: govBoard}
}

// O DEFEITO, medido: uma identidade autenticada que não é drenadora reclama o pedido de OUTRA
// pessoa e recebe o objectivo em claro. Vermelho antes da guarda, verde depois.
func TestAOS439ReclamacaoRecusaQuemNaoEDrenador(t *testing.T) {
	_, h := noComFila(t)
	const objectivo = "objectivo confidencial da alice"
	if sub := postReq(h, "/plans", map[string]any{"run_id": "run-439-alheio", "objective": objectivo}, euReaderHeaders()); sub.Code != http.StatusCreated {
		t.Fatalf("submissao: %d (%s)", sub.Code, sub.Body.String())
	}

	rec := postReq(h, "/plans/claim", nil, intrusoHeaders())
	if strings.Contains(rec.Body.String(), objectivo) {
		t.Fatalf("uma identidade que NAO e drenadora recebeu o objectivo DECIFRADO de um pedido alheio: %s", rec.Body.String())
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("quem nao e drenador tem de receber 403, veio %d (%s)", rec.Code, rec.Body.String())
	}

	// E NÃO CONSUMIU O PEDIDO: o drenador listado continua a recebê-lo. Sem isto, uma recusa que
	// escrevesse a reclamação antes de recusar deixaria o pedido preso até ao TTL.
	dono := postReq(h, "/plans/claim", nil, euReaderHeaders())
	if dono.Code != http.StatusOK {
		t.Fatalf("o drenador listado tinha de continuar a receber o pedido (200), veio %d (%s)", dono.Code, dono.Body.String())
	}
	var p respostaDeReclamo
	if err := json.Unmarshal(dono.Body.Bytes(), &p); err != nil || p.RunID != "run-439-alheio" || p.Geracao != 1 {
		t.Fatalf("o drenador recebeu %+v (%v), esperava run-439-alheio na geracao 1 — a recusa gastou uma geracao", p, err)
	}
	// Revisão do AOS-439: a reclamação diz QUEM submeteu, para o drenador recusar antes de planear
	// um pedido que o seu mandato não cobre.
	if p.RequestedBy != govReader {
		t.Fatalf("a reclamacao tem de trazer o submissor do pedido (%q), veio %q", govReader, p.RequestedBy)
	}
}

// O desfecho também: sem a guarda, quem não drena fechava o pedido de outra pessoa com `terminal`.
func TestAOS439DesfechoRecusaQuemNaoEDrenador(t *testing.T) {
	_, h := noComFila(t)
	if sub := postReq(h, "/plans", map[string]any{"run_id": "run-439-fecho", "objective": "o"}, euReaderHeaders()); sub.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", sub.Code)
	}
	rec := postReq(h, "/plans/outcome", map[string]any{
		"run_id": "run-439-fecho", "generation": 1, "classe": DesfechoTerminal,
	}, intrusoHeaders())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("quem nao e drenador tem de receber 403 no desfecho, veio %d (%s)", rec.Code, rec.Body.String())
	}
	// E o pedido continua vivo: o drenador reclama-o.
	if dono := postReq(h, "/plans/claim", nil, euReaderHeaders()); dono.Code != http.StatusOK {
		t.Fatalf("o desfecho recusado fechou o pedido: o drenador devia recebe-lo (200), veio %d", dono.Code)
	}
}

// A recusa é a MESMA 403 das outras recusas da rota: distinguir «não és drenador» de «não estás
// autenticado» não serve ninguém que não esteja a sondar.
func TestAOS439RecusaEUniforme(t *testing.T) {
	_, h := noComFila(t)
	semCredencial := postReq(h, "/plans/claim", nil, map[string]string{HeaderReaderPrincipal: "nhi:x"})
	intruso := postReq(h, "/plans/claim", nil, intrusoHeaders())
	if semCredencial.Code != intruso.Code || semCredencial.Body.String() != intruso.Body.String() {
		t.Fatalf("a recusa por nao-drenador distingue-se da recusa de governacao: %d %q vs %d %q",
			semCredencial.Code, semCredencial.Body.String(), intruso.Code, intruso.Body.String())
	}
}

// FAIL-CLOSED: um nó sem lista não deixa ninguém drenar — nem quem antes drenava.
func TestAOS439SemListaNinguemDrena(t *testing.T) {
	cfg := tnBaseConfig()
	cfg.PlanDrainers = nil
	cfg.BoardRegions = map[string]string{govBoard: govRegion, govBoardUS: govRegionUS}
	cfg.Model = &countingModel{}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	_, h := newAPI(t, node)
	if sub := postReq(h, "/plans", map[string]any{"run_id": "run-439-vazia", "objective": "o"}, euReaderHeaders()); sub.Code != http.StatusCreated {
		t.Fatalf("submissao: %d", sub.Code)
	}
	if rec := postReq(h, "/plans/claim", nil, euReaderHeaders()); rec.Code != http.StatusForbidden {
		t.Fatalf("sem AOS_PLAN_DRAINERS ninguem reclama (403), veio %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestAOS439ParseDosDrenadores(t *testing.T) {
	got, err := parsePlanDrainers(" 91a30a69-781d-448e-90c9-1de9f5e7bcbe , outro ")
	if err != nil || len(got) != 2 || got[0] != "91a30a69-781d-448e-90c9-1de9f5e7bcbe" || got[1] != "outro" {
		t.Fatalf("lista valida: %v %v", got, err)
	}
	if got, err := parsePlanDrainers(" , "); err != nil || got != nil {
		t.Fatalf("vazia tem de dar nil (ninguem), veio %v %v", got, err)
	}
	for _, mau := range []string{"*", "a,*", "a b", "human:jimy", "a,a"} {
		if _, err := parsePlanDrainers(mau); !errors.Is(err, ErrBadPlanDrainers) {
			t.Errorf("AOS_PLAN_DRAINERS=%q tinha de ser recusada, veio %v", mau, err)
		}
	}
}

func TestAOS439BannerDosDrenadores(t *testing.T) {
	vazio := strings.Join(planDrainersPostureBanner(nil), "\n")
	if !strings.Contains(vazio, "NENHUM") || !strings.Contains(vazio, "FAIL-CLOSED") {
		t.Fatalf("o banner sem lista tem de declarar que ninguem drena: %s", vazio)
	}
	cheio := strings.Join(planDrainersPostureBanner(map[string]bool{"sa-1": true}), "\n")
	if !strings.Contains(cheio, `"sa-1"`) || !strings.Contains(cheio, "1 principal") {
		t.Fatalf("o banner tem de nomear os drenadores: %s", cheio)
	}
}
