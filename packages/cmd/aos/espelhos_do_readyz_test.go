package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// leGauge extrai o valor de uma serie sem rotulos do corpo do /metrics.
func leGauge(t *testing.T, h http.Handler, nome string) (float64, bool) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(nome) + ` ([0-9.e+-]+)$`)
	m := re.FindStringSubmatch(rec.Body.String())
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatalf("valor ilegivel para %s: %q", nome, m[1])
	}
	return v, true
}

// O /readyz E O aos_ready TEM DE CONCORDAR, EM TODAS AS CONDICOES.
//
// E esta a propriedade, e nao "o aos_ready cobre a WORM": o defeito nasceu de os dois se
// terem separado sem ninguem dar por isso, e volta a nascer da mesma maneira se o teste
// enumerar as condicoes em vez de as EMPARELHAR. Cada caso move UMA condicao e exige que os
// dois sinais se movam juntos.
//
// O caso "saudavel" e o CONTROLO ANTI-VACUIDADE: sem ele, uma implementacao que devolvesse
// SEMPRE 503/0 passaria todos os outros casos sem distinguir coisa nenhuma.
func TestEspelhos_ReadyzEAosReadyConcordam(t *testing.T) {
	casos := []struct {
		nome        string
		avaria      func(t *testing.T, svc *NodeService, node *Node)
		queroPronto bool
	}{
		{
			nome:        "saudavel (CONTROLO: sem este caso o teste nao distingue nada)",
			avaria:      func(*testing.T, *NodeService, *Node) {},
			queroPronto: true,
		},
		{
			nome: "drain",
			avaria: func(t *testing.T, svc *NodeService, _ *Node) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := svc.Shutdown(ctx); err != nil {
					t.Fatalf("Shutdown: %v", err)
				}
			},
		},
		{
			nome: "Event Store fechado",
			avaria: func(t *testing.T, _ *NodeService, node *Node) {
				if err := node.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
			},
		},
		{
			// O ACHADO F. Antes desta correccao o /readyz dava 503 e o aos_ready lia 1.
			nome: "WORM a recusar escritas",
			avaria: func(_ *testing.T, svc *NodeService, _ *Node) {
				_ = svc.seloWORM.registar(errors.New("cadeia WORM a recusar Append (disco :ro)"))
			},
		},
	}

	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			node, _ := newAPINode(t, &countingModel{}, false)
			defer func() { _ = node.Close() }()
			svc, h := newAPI(t, node)

			// PRECONDICAO: os dois comecam de acordo e VERDES. Sem isto, um caso que ja
			// nascesse vermelho passaria por "a avaria funcionou".
			if code, _ := getProbe(h, "/readyz"); code != http.StatusOK {
				t.Fatalf("PRECONDICAO: /readyz devia comecar 200, veio %d", code)
			}
			if v, ok := leGauge(t, h, "aos_ready"); !ok || v != 1 {
				t.Fatalf("PRECONDICAO: aos_ready devia comecar 1, veio %v (presente=%v)", v, ok)
			}

			tc.avaria(t, svc, node)

			code, _ := getProbe(h, "/readyz")
			pronto := code == http.StatusOK
			v, ok := leGauge(t, h, "aos_ready")
			if !ok {
				t.Fatal("aos_ready AUSENTE do /metrics — a serie tem de existir sempre")
			}

			if pronto != tc.queroPronto {
				t.Fatalf("/readyz: pronto=%v, queria %v (HTTP %d)", pronto, tc.queroPronto, code)
			}
			// A ASERCAO QUE E O ACHADO: os dois dizem o MESMO.
			if (v == 1) != pronto {
				t.Fatalf("DIVERGEM: /readyz diz pronto=%v (HTTP %d) e aos_ready diz %v — "+
					"o aos_ready e o que o colector RASPA, logo e ele que o painel mostra", pronto, code, v)
			}
		})
	}
}

// O SLI DE DISPONIBILIDADE DO PLANO DE CONTROLO SEGUE O MESMO EIXO.
//
// E o alimento do `control_plane_availability_low`, um dos DOIS unicos alertas deste no com
// produtor real. Se ficar a 1 com o WORM a recusar, o alerta cala-se precisamente quando faz
// falta. O caso saudavel e, outra vez, o controlo.
func TestEspelhos_SLIDeDisponibilidadeCobreOWORM(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	svc, _ := newAPI(t, node)

	ctx := context.Background()
	if !svc.controlPlaneAvailable(ctx) {
		t.Fatal("CONTROLO: um no saudavel devia estar disponivel — sem isto a asercao seguinte nao mede nada")
	}

	_ = svc.seloWORM.registar(errors.New("cadeia WORM a recusar Append"))

	if svc.controlPlaneAvailable(ctx) {
		t.Fatal("com o WORM a recusar escritas o plano de controlo NAO esta disponivel — o alerta control_plane_availability_low ficaria calado")
	}

	// RECUPERA: uma selagem bem-sucedida limpa o estado, sem intervencao. Sem esta asercao,
	// um `recusando` que nunca voltasse a false passaria os casos de cima.
	_ = svc.seloWORM.registar(nil)
	if !svc.controlPlaneAvailable(ctx) {
		t.Fatal("depois de uma selagem BEM-SUCEDIDA o plano de controlo devia voltar a estar disponivel")
	}
}

// A HONESTIDADE DO HELP. A serie afirma cobrir as quatro condicoes; se alguem lhe tirar uma,
// o texto continua a prometer. Amarra-se o texto ao que a linha calcula.
func TestEspelhos_HelpDoAosReadyNomeiaAsQuatro(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	corpo := rec.Body.String()

	linha := ""
	for _, l := range strings.Split(corpo, "\n") {
		if strings.HasPrefix(l, "# HELP aos_ready ") {
			linha = l
			break
		}
	}
	if linha == "" {
		t.Fatal("aos_ready sem linha de HELP")
	}
	for _, termo := range []string{"drain", "Event Store", "custodia da KEK", "WORM"} {
		if !strings.Contains(linha, termo) {
			t.Fatalf("o HELP de aos_ready nao nomeia %q — %q", termo, linha)
		}
	}
}
