package record_test

import (
	"testing"

	"github.com/aos-ref/platform/memory/record"
)

// recordWriter é o conjunto de operações de ESCRITA/APAGAMENTO do registo. A
// barreira do Princípio 4 (AOS-036) exige que a vista entregue à projecção
// (record.RecordView) NÃO satisfaça este conjunto — a segregação é a nível de tipo.
type recordWriter interface {
	AppendTurn(record.Turn) error
	AppendSpan(record.Span)
}

// TestBarrier_RecordViewIsNotWritable prova, a NÍVEL DE TIPO, que a camada de
// projecção não consegue apagar/mutar o registo:
//
//  1. record.View(rec) devolve um RecordView cujo conjunto de métodos NÃO inclui
//     qualquer mutador — um type-assertion para recordWriter FALHA;
//  2. o valor devolvido também NÃO é reconvertível para *record.TrajectoryRecord
//     (o wrapper é de tipo não exportado), pelo que não há fuga por asserção.
//
// A prova de compilação é documentada em TestBarrier_CompileTimeContract abaixo: a
// linha comentada não compilaria porque RecordView não tem AppendTurn/Delete.
func TestBarrier_RecordViewIsNotWritable(t *testing.T) {
	t.Parallel()
	rec := record.NewTrajectoryRecord("trace-1")
	if err := rec.AppendTurn(completeTurn(1, "s", "r")); err != nil {
		t.Fatal(err)
	}

	view := record.View(rec)

	if _, ok := view.(recordWriter); ok {
		t.Fatal("BARREIRA VIOLADA: RecordView expõe operações de escrita/apagamento")
	}

	// Nota: `view.(*record.TrajectoryRecord)` NEM SEQUER COMPILA (go vet:
	// "impossible type assertion") — o registo mutável não implementa RecordView
	// (falta-lhe TurnSummaries/isRecordView). Os dois tipos são estruturalmente
	// disjuntos: não há fuga por asserção da vista para o registo.

	// A vista continua a permitir LEITURA (a projecção precisa disto).
	if view.TurnCount() != 1 {
		t.Fatalf("a vista read-only devia ler 1 turno, leu %d", view.TurnCount())
	}
	if view.TraceID() != "trace-1" {
		t.Fatalf("trace_id inesperado: %q", view.TraceID())
	}
}

// TestBarrier_CompileTimeContract documenta o contrato de COMPILAÇÃO da barreira. As
// linhas comentadas abaixo NÃO compilam — é essa a prova de que a projecção não tem
// acesso de escrita ao registo. Descomentar qualquer uma quebra o build:
//
//	var v record.RecordView = record.View(rec)
//	v.AppendTurn(record.Turn{})   // erro: v.AppendTurn undefined (RecordView não o tem)
//	v.AppendSpan(record.Span{})   // erro: v.AppendSpan undefined
//	_ = v.(*record.TrajectoryRecord).turns // erro: turns é não exportado / asserção falha
//
// O teste em si apenas afirma que o único caminho de escrita passa pelo registo
// concreto — nunca pela vista.
func TestBarrier_CompileTimeContract(t *testing.T) {
	t.Parallel()
	rec := record.NewTrajectoryRecord("trace-2")

	// O ÚNICO caminho de escrita: o registo concreto (via persist/AppendTurn).
	var _ recordWriter = rec // *TrajectoryRecord satisfaz o writer

	// A vista NÃO satisfaz o writer (verificado estruturalmente).
	var view record.RecordView = record.View(rec)
	if _, isWriter := view.(recordWriter); isWriter {
		t.Fatal("BARREIRA VIOLADA: a vista satisfaz o writer")
	}
}
