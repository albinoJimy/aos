package main

// aos438_desfecho_test.go — a drenagem gravava como FALHADO um plano bem-sucedido (AOS-438).
//
// Medido em produção a 2026-09-25: plan-e2e-437-1790336067 — o nó `n1` terminou `complete` e o
// run filho respondeu certo, mas o `consume` reportou `codigo=1 classe=transitorio`. O pedido
// voltou à fila, a retoma re-decompôs com o modelo, o gate recusou o organigrama novo, e o desfecho
// final ficou `7`. A causa: `codigoDe(nil)` caía no `default`. Os testes do AOS-423 cobriam a
// tabela código→classe, mas nunca a tradução do retorno do `serve` — que é onde estava.

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	planner "github.com/aos-ref/control-plane/orchestrator/planner"
	durable "github.com/aos-ref/kernel/agent-runtime/durable"
)

func TestAOS438ServeSemErroETerminalComCodigoZero(t *testing.T) {
	codigo, classe, detalhe := desfechoDoServe(nil)
	if codigo != exitOK || classe != "terminal" || detalhe != "" {
		t.Fatalf("um serve sem erro tinha de dar (0, terminal, \"\"), veio (%d, %q, %q) — "+
			"é o defeito que gravava planos bem-sucedidos como falha transitória", codigo, classe, detalhe)
	}
	if codigoDe(nil) != exitOK {
		t.Fatalf("codigoDe(nil) tinha de ser exitOK, veio %d", codigoDe(nil))
	}
}

func TestAOS438ErrosContinuamClassificados(t *testing.T) {
	// O terceiro valor é o TIPO do erro, e não o texto (AOS-443): o texto pode citar conteúdo do
	// modelo e não vai para o nó.
	for _, c := range []struct {
		nome   string
		err    error
		codigo int
		classe string
		tipo   string
	}{
		{"generico", errors.New("rede em baixo"), exitErro, "transitorio", "generico"},
		{"plano recusado pelo planeador", fmt.Errorf("x: %w", planner.ErrPlanRejected), exitPlanoRecusado, "terminal", "plano_recusado_pelo_planeador"},
		{"posse negada", fmt.Errorf("posse: %w", durable.ErrLeaseHeld), exitPosseNegada, "transitorio", "posse_negada"},
	} {
		codigo, classe, tipo := desfechoDoServe(c.err)
		if codigo != c.codigo || classe != c.classe || tipo != c.tipo {
			t.Errorf("%s: quer (%d, %q, %q), veio (%d, %q, %q)", c.nome, c.codigo, c.classe, c.tipo, codigo, classe, tipo)
		}
	}
}

// Um serve bem-sucedido larga a posse: sem `--release` o lease ficava vivo até ao TTL, e duas das
// quatro gerações do plano medido foram gastas contra ele.
func TestAOS438OServeDoConsumeLargaAPosse(t *testing.T) {
	args := argsDoServe("/etc/aos-orq/snapshot.json",
		pedidoReclamado{RunID: "plan-x", Objective: "o objectivo"}, substrato{wal: "/var/lib/aos-orq/consume.wal"},
		40*time.Minute, 2*time.Second, "orq", origemDoPlano{})
	if !slices.Contains(args, "--release") {
		t.Fatalf("o consume tem de passar --release ao serve; args=%v", args)
	}
	for _, par := range [][2]string{{"--run", "plan-x"}, {"--goal", "o objectivo"}, {"--snapshot", "/etc/aos-orq/snapshot.json"}, {"--wal", "/var/lib/aos-orq/consume.wal"}} {
		i := slices.Index(args, par[0])
		if i < 0 || i+1 >= len(args) || args[i+1] != par[1] {
			t.Errorf("falta %s %s em %v", par[0], par[1], args)
		}
	}
}
