package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	eventstore "github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------------------------
// O SUMÁRIO DIZ O QUE HÁ — E NÃO DIZ DE QUEM.
//
// A restrição que governa este comando não é de formatação: os stream ids deste store são RUN
// IDS. Listá-los seria ENUMERAR OS RUNS, que é exactamente o que o 404 uniforme do read-path
// existe para impedir. Um diagnóstico de operador que despejasse a lista faria pela porta das
// traseiras o que a porta da frente recusa.
//
// Por isso o teste que interessa aqui não é «conta bem» — é «NÃO NOMEIA».
// ---------------------------------------------------------------------------------------------

// walDeTeste escreve um WAL com streams e tipos conhecidos, e devolve o caminho.
func walDeTeste(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "events.wal")
	es, err := eventstore.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	escreve := func(stream, tipo string, n int) {
		for i := 0; i < n; i++ {
			if _, err := es.Append(ctx, stream, eventstore.EventInput{
				Type:     tipo,
				Producer: eventstore.Producer{NHIID: "nhi:teste"},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	escreve("run-alfa", "turn.recorded", 3)
	escreve("run-alfa", "step.checkpoint", 5)
	escreve("run-beta", "turn.recorded", 2)
	escreve("run-gama", "run.toolset.frozen", 1)
	if err := es.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func sumario(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := cmdWALSummary(args, &out); err != nil {
		t.Fatalf("cmdWALSummary: %v", err)
	}
	return out.String()
}

func TestSumarioContaTiposEStreams(t *testing.T) {
	s := sumario(t, "--path", walDeTeste(t))

	for _, esperado := range []string{
		"streams 3",
		"turn.recorded 5", // 3 do alfa + 2 do beta
		"step.checkpoint 5",
		"run.toolset.frozen 1",
	} {
		if !strings.Contains(s, esperado) {
			t.Errorf("falta %q na saida:\n%s", esperado, s)
		}
	}
}

// TestSumarioNAONomeiaOsStreams é o teste que interessa, e a razão de o comando existir na forma
// em que existe.
//
// Os stream ids são run ids. Se saíssem, este diagnóstico seria uma via de enumeração de runs — a
// mesma coisa que o read-path recusa com um 404 uniforme, uma regra de residência e uma
// credencial forte.
func TestSumarioNAONomeiaOsStreams(t *testing.T) {
	s := sumario(t, "--path", walDeTeste(t))
	for _, run := range []string{"run-alfa", "run-beta", "run-gama"} {
		if strings.Contains(s, run) {
			t.Errorf("o sumario NOMEIA o stream %q — e uma via de enumeracao de runs pela porta "+
				"das traseiras:\n%s", run, s)
		}
	}
	// CONTROLO: e mesmo assim CONTA-OS. Sem este ramo, um comando que nao imprimisse nada
	// passaria no teste acima — nao nomear por nao dizer nada seria trivial e inútil.
	if !strings.Contains(s, "streams 3") {
		t.Errorf("nao contou os streams: %s", s)
	}
}

// TestSumarioOrdenaPorNOME — a saída tem de ser comparável entre dois momentos.
//
// Ordenar por CONTAGEM faria a ordem mudar com os dados, e um `diff` entre duas recolhas acusaria
// movimento onde não houve.
func TestSumarioOrdenaPorNome(t *testing.T) {
	s := sumario(t, "--path", walDeTeste(t))
	var tipos []string
	for _, l := range strings.Split(s, "\n") {
		if l == "" || strings.HasPrefix(l, "streams ") {
			continue
		}
		tipos = append(tipos, strings.Fields(l)[0])
	}
	if len(tipos) < 2 {
		t.Fatalf("poucos tipos para verificar a ordem: %v", tipos)
	}
	for i := 1; i < len(tipos); i++ {
		if tipos[i-1] > tipos[i] {
			t.Errorf("saida fora de ordem alfabetica: %q antes de %q", tipos[i-1], tipos[i])
		}
	}
}

// TestSumarioSemPathRecusa — fail-closed de argumentos, no molde do `wal-count`.
func TestSumarioSemPathRecusa(t *testing.T) {
	var out bytes.Buffer
	if err := cmdWALSummary(nil, &out); !errors.Is(err, ErrWALSummaryPathRequired) {
		t.Errorf("sem --path esperava ErrWALSummaryPathRequired, veio %v", err)
	}
}

// TestOCLIAlcancaOWalSummary é o teste de CABLAGEM, e apareceu por mutação.
//
// Os testes acima chamam `cmdWALSummary` DIRECTAMENTE. Uma mutação que removesse o `case` do
// dispatcher passava em todos: a função continuava correcta e nenhum operador lhe chegava —
// `aos wal-summary` responderia «subcomando desconhecido».
//
// É a nona vez que este padrão aparece no repositório.
func TestOCLIAlcancaOWalSummary(t *testing.T) {
	var out bytes.Buffer
	if err := dispatch([]string{"wal-summary", "--path", walDeTeste(t)}, &out); err != nil {
		t.Fatalf("dispatch(wal-summary): %v", err)
	}
	if !strings.Contains(out.String(), "streams 3") {
		t.Errorf("o CLI nao encaminhou para o sumario:\n%s", out.String())
	}
	// CONTROLO: um subcomando que NAO existe continua a ser recusado. Sem este ramo, um
	// dispatcher que aceitasse tudo passaria no teste acima.
	var out2 bytes.Buffer
	if err := dispatch([]string{"wal-sumario-que-nao-existe"}, &out2); err == nil {
		t.Error("o dispatcher aceitou um subcomando inexistente")
	}
}
