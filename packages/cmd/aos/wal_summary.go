package main

// ---------------------------------------------------------------------------------------------
// `aos wal-summary` — O QUE HÁ NESTE WAL, SEM DIZER DE QUEM.
//
// PORQUE EXISTE. O `wal-count` responde «quantos eventos tem ESTE run», e para isso é preciso já
// saber qual run se procura. A pergunta que aparece primeiro num diagnóstico é outra: «o que há
// aqui dentro?» — que tipos de evento, quantos de cada, quantos streams.
//
// Dei por esta falta a inspeccionar o Event Store de produção duas vezes no mesmo dia, e nas duas
// contornei-a escrevendo um ficheiro de teste descartável dentro do pacote `eventstore` para
// chamar o `replayWAL` não-exportado. Um operador não pode fazer isso. Uma ferramenta de
// diagnóstico que só responde a perguntas que já exigem a resposta serve para confirmar, não para
// descobrir.
//
// E DEI POR ELA DEPOIS DE JÁ TER FEITO O TRABALHO À MÃO — o `wal_inspect.go` já existia e eu não
// o tinha procurado. Fica dito, porque a lição não é sobre este comando: é sobre olhar primeiro.
//
// ---------------------------------------------------------------------------------------------
// A RESTRIÇÃO QUE GOVERNA A SAÍDA, e não é um detalhe de formatação.
//
// Os stream ids DESTE store são run ids. Listá-los seria ENUMERAR OS RUNS — exactamente o que o
// 404 uniforme do read-path existe para impedir, e que o nó defende com uma regra de residência e
// uma credencial forte. Um diagnóstico de operador que despejasse a lista faria pela porta das
// traseiras o que a porta da frente recusa.
//
// Por isso a saída é DELIBERADAMENTE agregada: tipos, contagens, e o NÚMERO de streams. Nunca os
// nomes, nunca os payloads. Quem precisa de olhar para um run concreto já tem o `wal-count`, e
// para o usar tem de saber qual run quer — que é a fronteira certa.
// ---------------------------------------------------------------------------------------------

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	eventstore "github.com/aos-ref/substrate/eventstore"
)

// ErrWALSummaryPathRequired — falta o --path.
var ErrWALSummaryPathRequired = errors.New("aos: --path obrigatorio (ficheiro WAL do Event Store duravel)")

// cmdWALSummary imprime o HISTOGRAMA DE TIPOS de um WAL do Event Store, mais o número de streams.
//
// READ-ONLY, no mesmo molde do [cmdWALCount]: abre por replay e conta. Corre num contentor
// EFÉMERO da mesma imagem, com o contentor principal parado — não há escritor concorrente do WAL.
//
// A saída é estável e fácil de comparar entre dois momentos:
//
//	streams 33
//	turn.recorded 55
//	step.checkpoint 251
//	...
func cmdWALSummary(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("wal-summary", flag.ContinueOnError)
	fs.SetOutput(w)
	path := fs.String("path", "", "ficheiro WAL do Event Store duravel")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*path) == "" {
		return ErrWALSummaryPathRequired
	}

	es, err := eventstore.Open(*path)
	if err != nil {
		return fmt.Errorf("aos: abrir Event Store duravel %q (replay do WAL): %w", *path, err)
	}
	defer func() { _ = es.Close() }()

	streams := es.Streams()
	porTipo := map[string]int{}
	ctx := context.Background()
	for _, s := range streams {
		evs, rerr := es.Read(ctx, s, 1)
		if rerr != nil {
			if errors.Is(rerr, eventstore.ErrStreamNotFound) {
				continue
			}
			return fmt.Errorf("aos: ler stream do WAL: %w", rerr)
		}
		for _, e := range evs {
			porTipo[e.Type]++
		}
	}

	// O número de streams sai PRIMEIRO e sozinho: é o denominador de tudo o que vem a seguir.
	fmt.Fprintf(w, "streams %d\n", len(streams))

	// Ordenado por NOME e não por contagem: a saída tem de ser comparável entre dois momentos, e
	// uma ordem que muda com os dados faz um `diff` acusar movimento onde não houve.
	tipos := make([]string, 0, len(porTipo))
	for k := range porTipo {
		tipos = append(tipos, k)
	}
	sort.Strings(tipos)
	for _, t := range tipos {
		fmt.Fprintf(w, "%s %d\n", t, porTipo[t])
	}
	return nil
}
