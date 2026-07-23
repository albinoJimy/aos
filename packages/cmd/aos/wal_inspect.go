package main

// AOS-169 — DIAGNÓSTICO DE DURABILIDADE do WAL (§13.3), read-only. Suporta a prova
// NÃO-VACUOSA de "kill+reinício SEM DUPLICAÇÃO" ao nível do CONTENTOR empacotado: o harness
// de durabilidade (deploy/node/aos169-durability-harness.sh) precisa de um EFEITO OBSERVÁVEL,
// de fora do processo, que distinga um reconhecimento idempotente de uma re-execução fresca —
// e o status 201 de POST /runs é deliberadamente NÃO-ENUMERÁVEL (uniforme para ambos), pelo que
// NÃO serve de discriminante. Este subcomando lê o número de TURNOS DURÁVEIS já committed de um
// run directamente do WAL: se uma re-submissão após o reinício NÃO acrescentar um turno (mesma
// cardinalidade), a dupla-execução foi barrada (dedup por (RunID,StepID) do WAL + fencing do
// lease) — a propriedade §13.3 fica ENCENADA no artefacto, não só inferida.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	eventstore "github.com/aos-ref/substrate/eventstore"
)

// Erros da inspecção do WAL (fail-closed; comparáveis com errors.Is).
var (
	// ErrWALPathRequired — falta o --path (ficheiro WAL do Event Store durável).
	ErrWALPathRequired = errors.New("aos: --path obrigatorio (ficheiro WAL do Event Store duravel)")
	// ErrWALRunRequired — falta o --run (RunID / stream a contar).
	ErrWALRunRequired = errors.New("aos: --run obrigatorio (RunID / stream a contar)")
)

// cmdWALCount abre o Event Store durável (WAL) em --path por REPLAY e imprime, numa única
// linha, o NÚMERO de eventos committed do stream --run — opcionalmente filtrado aos TURNOS
// DURÁVEIS (--turns ⇒ só eventos "turn.recorded"). É um diagnóstico READ-ONLY: só reconstrói o
// store a partir do WAL e conta; NÃO submete trabalho nem grava eventos novos (o único write
// possível é a truncatura crash-safe idempotente de um tail parcial, o MESMO que o nó faz no
// arranque). Destina-se a correr num contentor EFÉMERO da MESMA imagem, com o contentor
// principal PARADO (sem escritor concorrente do WAL).
//
// Semântica de saída (fácil de capturar num `$(...)` de shell): imprime só o inteiro. Um stream
// inexistente conta 0 (um run que nunca gravou trabalho committed é 0 turnos — não é erro).
func cmdWALCount(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("wal-count", flag.ContinueOnError)
	fs.SetOutput(w)
	path := fs.String("path", "", "ficheiro WAL do Event Store duravel")
	runID := fs.String("run", "", "RunID / stream a contar")
	turnsOnly := fs.Bool("turns", false, "contar SO os turnos duraveis (turn.recorded)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*path) == "" {
		return ErrWALPathRequired
	}
	if strings.TrimSpace(*runID) == "" {
		return ErrWALRunRequired
	}

	es, err := eventstore.Open(*path)
	if err != nil {
		return fmt.Errorf("aos: abrir Event Store duravel %q (replay do WAL): %w", *path, err)
	}
	defer func() { _ = es.Close() }()

	evs, err := es.Read(context.Background(), *runID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			fmt.Fprintln(w, 0) // stream inexistente ⇒ 0 turnos committed (não é erro)
			return nil
		}
		return fmt.Errorf("aos: ler stream %q do WAL: %w", *runID, err)
	}
	n := 0
	for _, ev := range evs {
		if *turnsOnly && ev.Type != agentruntime.EventTypeTurnRecorded {
			continue
		}
		n++
	}
	fmt.Fprintln(w, n)
	return nil
}
