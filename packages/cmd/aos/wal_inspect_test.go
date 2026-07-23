package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	eventstore "github.com/aos-ref/substrate/eventstore"
)

// AOS-169 — o diagnóstico READ-ONLY de durabilidade (cmdWALCount) que torna NÃO-VACUOSA a prova
// de "kill+reinício sem duplicação" no harness de contentor: conta os turnos duráveis de um run
// directamente do WAL, do lado de fora do processo. Estes testes provam que a contagem é FIEL
// (só turnos vs todos os eventos), robusta a stream inexistente (0), e fail-closed nos flags.

// walSeed cria um Event Store durável em `path`, grava os eventos dados no stream=run e fecha —
// deixando um WAL replayável (o mesmo que um contentor deixaria num volume).
func walSeed(t *testing.T, path, run string, types []string) {
	t.Helper()
	es, err := eventstore.Open(path)
	if err != nil {
		t.Fatalf("Open(seed): %v", err)
	}
	ctx := context.Background()
	for i, typ := range types {
		if _, err := es.Append(ctx, run, eventstore.EventInput{
			Type:     typ,
			RunID:    run,
			StepID:   "step-" + string(rune('a'+i)),
			Producer: eventstore.Producer{NHIID: "nhi:wal-seed"},
		}); err != nil {
			t.Fatalf("Append(%s): %v", typ, err)
		}
	}
	if err := es.Close(); err != nil {
		t.Fatalf("Close(seed): %v", err)
	}
}

// walCount corre cmdWALCount e devolve o inteiro impresso (trim).
func walCount(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := cmdWALCount(args, &out); err != nil {
		t.Fatalf("cmdWALCount(%v): %v", args, err)
	}
	return strings.TrimSpace(out.String())
}

// TestWALCountTurnsOnly prova a fidelidade: --turns conta SÓ os "turn.recorded", ignorando os
// demais eventos do stream; sem --turns conta todos. É a métrica que o harness compara antes/
// depois de uma re-submissão para detectar (ou excluir) uma dupla-execução.
func TestWALCountTurnsOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.wal")
	const run = "run-wal-1"
	// 2 turnos duráveis + 1 evento de outro tipo no mesmo stream.
	walSeed(t, path, run, []string{
		agentruntime.EventTypeTurnRecorded,
		"custom.marker",
		agentruntime.EventTypeTurnRecorded,
	})

	if got := walCount(t, "--path", path, "--run", run, "--turns"); got != "2" {
		t.Fatalf("--turns devia contar 2 turnos duraveis, veio %q", got)
	}
	if got := walCount(t, "--path", path, "--run", run); got != "3" {
		t.Fatalf("sem --turns devia contar 3 eventos, veio %q", got)
	}
}

// TestWALCountUnknownStreamIsZero prova que um run sem trabalho committed (stream inexistente)
// conta 0 SEM erro — o harness lê "0 turnos" em vez de falhar num run que nunca gravou.
func TestWALCountUnknownStreamIsZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.wal")
	walSeed(t, path, "run-existe", []string{agentruntime.EventTypeTurnRecorded})

	if got := walCount(t, "--path", path, "--run", "run-nao-existe", "--turns"); got != "0" {
		t.Fatalf("stream inexistente devia contar 0, veio %q", got)
	}
}

// TestWALCountReadOnlyDoesNotDuplicate prova que a inspecção é READ-ONLY e IDEMPOTENTE: contar
// repetidamente não altera a cardinalidade (o subcomando não acrescenta eventos ao WAL). É a
// invariante que legitima usá-lo como sonda no harness sem perturbar o estado durável.
func TestWALCountReadOnlyDoesNotDuplicate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.wal")
	const run = "run-wal-ro"
	walSeed(t, path, run, []string{agentruntime.EventTypeTurnRecorded})

	first := walCount(t, "--path", path, "--run", run, "--turns")
	second := walCount(t, "--path", path, "--run", run, "--turns")
	if first != "1" || second != "1" {
		t.Fatalf("a contagem read-only devia manter-se 1 (nao acrescenta eventos), veio %q depois %q", first, second)
	}
}

// TestWALCountFailClosedFlags prova o fail-closed dos flags obrigatórios.
func TestWALCountFailClosedFlags(t *testing.T) {
	var out bytes.Buffer
	if err := cmdWALCount([]string{"--run", "r"}, &out); !errors.Is(err, ErrWALPathRequired) {
		t.Fatalf("sem --path devia dar ErrWALPathRequired, veio %v", err)
	}
	if err := cmdWALCount([]string{"--path", "x.wal"}, &out); !errors.Is(err, ErrWALRunRequired) {
		t.Fatalf("sem --run devia dar ErrWALRunRequired, veio %v", err)
	}
}
