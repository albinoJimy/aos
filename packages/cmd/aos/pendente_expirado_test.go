package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
)

// ---------------------------------------------------------------------------------------------
// O PRAZO DE UM PENDENTE NÃO É UMA CORRIDA COM O VARREDOR.
//
// Achado da verificação de completude de 2026-08-23, verificado por leitura antes de se corrigir:
// `ListForRun` filtra por grant emitido e por retirada, e a IDADE só é avaliada no
// `ListExpirable` — que é o varredor.
//
// Entre o instante em que um pendente cruza o TTL e o tick seguinte havia uma janela — até
// `AOS_APPROVAL_SWEEP_INTERVAL`, 1 min por omissão — em que ele continuava aprovável e produzia
// um grant VÁLIDO com TTL fresco. Com o varredor DESLIGADO (`interval=0`, suportado), a janela é
// INFINITA.
//
// O QUE ESTAVA A SER CONTORNADO: o varredor regista que «a accao ficara NEGADA» e o run volta a
// ser retomável. Aprovar depois do prazo converte esse «vai ser negado» num «autorizado» — que é
// exactamente a decisão que o TTL existe para tomar por omissão.
// ---------------------------------------------------------------------------------------------

// semearPendenteAntigo regista uma escalada com um carimbo NO PASSADO — o pendente que o varredor
// ainda não apanhou.
func semearPendenteAntigo(t *testing.T, node *Node, runID, capability string, preview []byte, idade time.Duration) {
	t.Helper()
	if err := node.PendingApprovals.Put(context.Background(), integration.PendingRecord{
		RunID:      runID,
		StepID:     "step-antigo",
		Turn:       1,
		ToolID:     "tool-antigo",
		Capability: capability,
		Preview:    preview,
		CreatedAt:  time.Now().Add(-idade).UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("semear pendente antigo: %v", err)
	}
}

func TestUmPendenteFORA_DO_PRAZO_NaoEAprovavel(t *testing.T) {
	node, h, quem, chaves := noComTresAprovadores(t)
	preview := []byte("efeito escalado ha muito tempo")
	// 16 minutos: o TTL por omissão é 15.
	semearPendenteAntigo(t, node, "run-prazo", capIrreversivelDeTeste, preview, 16*time.Minute)

	rec := postJSON(h, "POST", "/runs/run-prazo/approve",
		corpoDual(t, preview, true, []string{quem[0], quem[1]}, [][]byte{chaves[0], chaves[1]}))
	if rec.Code == http.StatusOK {
		t.Errorf("um pendente FORA DO PRAZO produziu um grant valido — o prazo era uma corrida com "+
			"o varredor, e com ele desligado a janela e infinita: %s", rec.Body.String())
	}
}

// TestUmPendenteDENTRO_DO_PRAZO_ContinuaAprovavel é a âncora anti-vacuidade.
//
// Sem ela, «recusar sempre» passaria no teste acima e nenhuma cerimónia funcionaria — o defeito
// simétrico, e pior, porque tornaria toda a escalada indecidível.
func TestUmPendenteDENTRO_DO_PRAZO_ContinuaAprovavel(t *testing.T) {
	node, h, quem, chaves := noComTresAprovadores(t)
	preview := []byte("efeito escalado ha pouco")
	semearPendenteAntigo(t, node, "run-prazo-ok", capIrreversivelDeTeste, preview, 2*time.Minute)

	rec := postJSON(h, "POST", "/runs/run-prazo-ok/approve",
		corpoDual(t, preview, true, []string{quem[0], quem[1]}, [][]byte{chaves[0], chaves[1]}))
	if rec.Code != http.StatusOK {
		t.Fatalf("um pendente DENTRO do prazo devia continuar aprovavel; veio %d (%s)",
			rec.Code, rec.Body.String())
	}
}

// TestUmPendenteSEM_CARIMBO_NaoEDadoComoExpirado — fail-open declarado, e é o correcto.
//
// Um registo sem `CreatedAt` (selado por uma versão anterior, ou por um caminho que não o
// preencha) não tem idade conhecida. Tratá-lo como expirado tornaria indecidíveis escaladas
// legítimas por causa de um campo em falta; o varredor tem exactamente a mesma postura.
func TestUmPendenteSEM_CARIMBO_NaoEDadoComoExpirado(t *testing.T) {
	node, h, quem, chaves := noComTresAprovadores(t)
	preview := []byte("efeito sem carimbo")
	if err := node.PendingApprovals.Put(context.Background(), integration.PendingRecord{
		RunID: "run-sem-carimbo", StepID: "s1", Turn: 1,
		ToolID: "t", Capability: capIrreversivelDeTeste, Preview: preview,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rec := postJSON(h, "POST", "/runs/run-sem-carimbo/approve",
		corpoDual(t, preview, true, []string{quem[0], quem[1]}, [][]byte{chaves[0], chaves[1]}))
	if rec.Code != http.StatusOK {
		t.Errorf("um pendente SEM carimbo foi dado como expirado; veio %d (%s) — um campo em falta "+
			"nao pode tornar uma escalada legitima indecidivel", rec.Code, rec.Body.String())
	}
}

// TestUmCarimboILEGIVEL_NaoEDadoComoExpirado é a lacuna que a mutação W4 revelou.
//
// O teste do carimbo em FALTA não cobria isto: um `CreatedAt` vazio é apanhado pelo guarda
// `!= ""` ANTES do `Parse`. Um carimbo NÃO-VAZIO mas ilegível segue para o `Parse`, e é aí que se
// decide o que fazer com um erro.
//
// FAIL-OPEN DECLARADO, e é o mesmo que o varredor pratica: uma data que não se consegue ler não
// é prova de idade nenhuma. Tratá-la como expirada tornaria indecidível uma escalada legítima por
// causa de um campo corrompido — e a decisão de negar por omissão pertence ao TTL, não a um erro
// de parsing.
func TestUmCarimboILEGIVEL_NaoEDadoComoExpirado(t *testing.T) {
	node, h, quem, chaves := noComTresAprovadores(t)
	preview := []byte("efeito com carimbo ilegivel")
	if err := node.PendingApprovals.Put(context.Background(), integration.PendingRecord{
		RunID: "run-carimbo-mau", StepID: "s1", Turn: 1,
		ToolID: "t", Capability: capIrreversivelDeTeste, Preview: preview,
		CreatedAt: "isto-nao-e-uma-data",
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rec := postJSON(h, "POST", "/runs/run-carimbo-mau/approve",
		corpoDual(t, preview, true, []string{quem[0], quem[1]}, [][]byte{chaves[0], chaves[1]}))
	if rec.Code != http.StatusOK {
		t.Errorf("um carimbo ILEGIVEL foi tratado como prova de expiracao; veio %d (%s) — um erro "+
			"de parsing nao pode tomar a decisao que pertence ao TTL", rec.Code, rec.Body.String())
	}
}
