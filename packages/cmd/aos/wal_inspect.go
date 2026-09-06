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
// store a partir do WAL e conta; NÃO submete trabalho, NÃO grava eventos novos e NÃO toca no
// ficheiro — [eventstore.OpenReadOnly] não anexa o WAL para append nem repõe a cauda parcial
// (AOS-347). Destina-se a correr num contentor EFÉMERO da MESMA imagem; ao contrário do que
// esta linha dizia antes, JÁ NÃO EXIGE o contentor principal parado, porque um abridor que não
// escreve não tem por onde colidir no seq nem por onde encolher o log. O volume pode estar
// montado `:ro`.
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

	// AOS-347 — INSPECÇÃO ABRE SÓ PARA LEITURA. O `wal-count` corre com o nó a
	// funcionar (é o que um operador usa a meio de um incidente), e [eventstore.Open]
	// anexava o WAL para APPEND: ganhava uma segunda cabeça de escrita, que colide no
	// seq com a do nó (medido: seqs [1 2 3 4 4 5 5] e o arranque seguinte a recusar com
	// E_RESTORE_ORDER), e podia TRUNCAR o ficheiro ao repor a cauda parcial. O comentário
	// dizia «com o contentor principal PARADO» — uma convenção documentada, não uma
	// restrição imposta. [eventstore.OpenReadOnly] torna-a propriedade do tipo.
	es, err := eventstore.OpenReadOnly(*path)
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
