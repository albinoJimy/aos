package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/governance/autonomy"
	audit "github.com/aos-ref/platform/audit"
)

// AOS-306 (lado do nó) e AOS-307 — o que a rota responde quando o WORM falha, e o que o
// provisionamento faz com o que o WORM já tem.

// wormQueRecusaSelar é um audit.Store cujo Append falha sempre — o WORM «em baixo».
type wormQueRecusaSelar struct{ audit.Store }

var errWORMEmBaixo = errors.New("WORM em baixo (teste)")

func (w wormQueRecusaSelar) Append(context.Context, audit.AuditRecord) (audit.AuditRecord, error) {
	return audit.AuditRecord{}, errWORMEmBaixo
}

// TestAOS306_SelagemFalhadaNaoAplicaEDizAVerdade reproduz a medição da auditoria: WORM em baixo,
// POST /autonomy. Antes: 400 «nivel recusado» E o nível aplicado. Agora: 503 que nomeia a
// indisponibilidade, e o nível NÃO muda.
func TestAOS306_SelagemFalhadaNaoAplicaEDizAVerdade(t *testing.T) {
	h, priv, _ := noParaTeste(t)
	// Substitui o WORM do sink por um que recusa selar — depois de o registo existir, como num
	// nó a correr cujo disco de audit fica indisponível.
	h.node.Autonomy.sink.bind(wormQueRecusaSelar{Store: audit.NewMemStore()})

	w := httptest.NewRecorder()
	h.handleAutonomySet(w, pedidoDeAutonomia(t, priv, "human:op", "agt-1", "fs", "L2", "com o WORM em baixo"))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("selagem falhada devolveu %d, quero 503: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "recusado") || !strings.Contains(w.Body.String(), "NAO aplicado") {
		t.Errorf("a resposta tem de dizer que o nivel NAO foi aplicado por indisponibilidade, nao «recusado»: %s", w.Body.String())
	}
	if got, ok := h.node.Autonomy.registry.Get("agt-1", "fs"); ok {
		t.Fatalf("o nivel foi aplicado (%s) com a selagem falhada — a resposta e o estado divergem", got)
	}
	if n := len(h.node.Autonomy.registry.History()); n != 0 {
		t.Fatalf("o historico cresceu (%d) sem selo", n)
	}
}

// TestAOS306_ErrosDeValidacaoContinuamA400 — o 400 fica reservado ao que o registo recusa de
// facto. (A rota pré-valida agent/domain/reason/level, pelo que a única via de SetLevel devolver
// um erro de validação é um par vazio — que a rota já apanha. O que se prova aqui é o contraste:
// uma selagem OK dá 200, e o caminho de erro só é 503 quando é selagem.)
func TestAOS306_ErrosDeValidacaoContinuamA400(t *testing.T) {
	h, priv, _ := noParaTeste(t)
	w := httptest.NewRecorder()
	h.handleAutonomySet(w, pedidoDeAutonomia(t, priv, "human:op", "agt-1", "fs", "L2", "com o WORM a funcionar"))
	if w.Code != http.StatusOK {
		t.Fatalf("com selagem OK devia aplicar: %d %s", w.Code, w.Body.String())
	}
	w2 := httptest.NewRecorder()
	h.handleAutonomySet(w2, pedidoDeAutonomia(t, priv, "human:op", "", "fs", "L2", "par vazio"))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("par vazio devia ser 400, veio %d", w2.Code)
	}
}

// TestAOS307_NivelDeOperadorSobreviveAoReinicioEPrevaleceSobreOAmbiente — o cenário da
// auditoria: um operador promove por POST /autonomy; o nó reinicia com AOS_AUTONOMY_LEVELS a
// dizer outra coisa. Antes: o ambiente ganhava em silêncio. Agora: o WORM prevalece, e o banner
// declara o par preservado.
func TestAOS307_NivelDeOperadorSobreviveAoReinicioEPrevaleceSobreOAmbiente(t *testing.T) {
	worm := audit.NewMemStore()
	specs := []autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L1}}

	// Arranque 1: provisiona L1 (actor config:node).
	w1 := buildAutonomyOracle(specs, autonomy.L0)
	if err := w1.provision(context.Background(), worm); err != nil {
		t.Fatal(err)
	}
	// Um OPERADOR muda para L3 (sela com o seu actor).
	if _, err := w1.registry.SetLevel(context.Background(), "agt-1", "fs", autonomy.L3, "decisao humana", "op:jimy"); err != nil {
		t.Fatal(err)
	}

	// Arranque 2: registo NOVO, mesmo WORM, ambiente ainda a dizer L1.
	w2 := buildAutonomyOracle(specs, autonomy.L0)
	if err := w2.provision(context.Background(), worm); err != nil {
		t.Fatal(err)
	}
	if got := w2.registry.LevelFor("agt-1", "fs"); got != autonomy.L3 {
		t.Fatalf("apos reinicio o nivel e %s, quero L3 (a decisao do operador no WORM)", got)
	}
	if w2.rehydrated != 2 {
		t.Errorf("rehydrated = %d, quero 2 (provisionamento + operador)", w2.rehydrated)
	}
	if len(w2.preservedOverEnv) != 1 || !strings.Contains(w2.preservedOverEnv[0], "agt-1:fs=L3(env L1") {
		t.Errorf("preservedOverEnv = %v, quero o par preservado com os dois niveis", w2.preservedOverEnv)
	}
	// Nada de novo foi selado: o WORM tem exactamente os dois eventos.
	if head, _ := worm.Head(context.Background(), autonomy.DefaultAutonomyPartition); head != 2 {
		t.Errorf("head do WORM = %d, quero 2 — o reinicio nao pode voltar a selar o ambiente por cima da decisao", head)
	}
	// E o GET /autonomy (via History) vê o par.
	if n := len(w2.registry.History()); n != 2 {
		t.Errorf("historico reidratado tem %d entradas, quero 2", n)
	}
	// O banner declara-o, nos dois sentidos.
	linha := strings.Join(autonomyPostureBanner(w2), "\n")
	for _, exigido := range []string{"RELIDA(S) do WORM", "PRESERVADO", "agt-1:fs=L3(env L1"} {
		if !strings.Contains(linha, exigido) {
			t.Errorf("o banner nao contem %q:\n%s", exigido, linha)
		}
	}
}

// TestAOS307_AmbienteEditadoPrevaleceSobreProvisionamentoAnterior — a outra direcção da regra:
// se o último selo do par foi do PRÓPRIO provisionamento, editar AOS_AUTONOMY_LEVELS e reiniciar
// aplica o valor novo (e sela-o). Um valor IGUAL não re-sela.
func TestAOS307_AmbienteEditadoPrevaleceSobreProvisionamentoAnterior(t *testing.T) {
	worm := audit.NewMemStore()
	w1 := buildAutonomyOracle([]autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L1}}, autonomy.L0)
	if err := w1.provision(context.Background(), worm); err != nil {
		t.Fatal(err)
	}
	// Reinício com o MESMO ambiente: não re-sela.
	w2 := buildAutonomyOracle([]autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L1}}, autonomy.L0)
	if err := w2.provision(context.Background(), worm); err != nil {
		t.Fatal(err)
	}
	if head, _ := worm.Head(context.Background(), autonomy.DefaultAutonomyPartition); head != 1 {
		t.Fatalf("arranque idempotente re-selou (head=%d, quero 1)", head)
	}
	if _, ok := w2.sealedPairs["agt-1:fs"]; !ok {
		t.Fatal("o par confirmado do WORM tem de contar como provisionado para o banner")
	}
	// Reinício com o ambiente EDITADO para L2: aplica e sela.
	w3 := buildAutonomyOracle([]autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L2}}, autonomy.L0)
	if err := w3.provision(context.Background(), worm); err != nil {
		t.Fatal(err)
	}
	if got := w3.registry.LevelFor("agt-1", "fs"); got != autonomy.L2 {
		t.Fatalf("ambiente editado devia prevalecer sobre provisionamento anterior: %s", got)
	}
	if head, _ := worm.Head(context.Background(), autonomy.DefaultAutonomyPartition); head != 2 {
		t.Fatalf("a edicao do ambiente devia ter selado (head=%d, quero 2)", head)
	}
	if len(w3.preservedOverEnv) != 0 {
		t.Errorf("nao houve decisao de operador a preservar: %v", w3.preservedOverEnv)
	}
}

// wormQueNaoSeLe falha a leitura — o WORM ilegível no arranque.
type wormQueNaoSeLe struct{ audit.Store }

func (wormQueNaoSeLe) Read(context.Context, string, uint64, uint64) ([]audit.AuditRecord, error) {
	return nil, errors.New("disco de audit ilegivel (teste)")
}

// TestAOS307_RehidratacaoIlegivelAbortaOArranque — fail-closed: um nó que não consegue confirmar
// os níveis do WORM não arranca a servir os do ficheiro.
func TestAOS307_RehidratacaoIlegivelAbortaOArranque(t *testing.T) {
	w := buildAutonomyOracle([]autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L1}}, autonomy.L0)
	err := w.provision(context.Background(), wormQueNaoSeLe{Store: audit.NewMemStore()})
	if !errors.Is(err, ErrAutonomyProvisioning) {
		t.Fatalf("WORM ilegivel devia abortar com ErrAutonomyProvisioning, veio: %v", err)
	}
	if got := w.registry.LevelFor("agt-1", "fs"); got != autonomy.L0 {
		t.Fatalf("com a rehidratacao falhada nada pode ter sido aplicado: %s", got)
	}
}
