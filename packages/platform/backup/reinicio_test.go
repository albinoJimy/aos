package backup

import (
	"context"
	"errors"
	"testing"
)

// AOS-101 — O QUE UM DESTINO DURÁVEL FAZ AO EXPORTADOR, MEDIDO.
//
// Este ficheiro não prova uma funcionalidade: DECLARA UM LIMITE, em código executável,
// porque a alternativa era ele continuar a ser inferido por quem lesse o construtor.
//
// O agendador de exportação (packages/cmd/aos/backup_scheduler.go) obrigou a responder a uma
// pergunta que ninguém tinha tido de responder enquanto o `Export` era chamado só por testes:
// PARA ONDE é que o nó exporta? A resposta óbvia — um backend durável (disco, object storage) —
// não é hoje possível, e a razão não está no agendador, está AQUI:
//
//   - [NewExporter] começa SEMPRE do génesis: `manifest` vazio, `lastExported` vazio. Não há
//     opção de RETOMA de um manifesto anterior;
//   - por isso o primeiro ciclo de qualquer exportador escreve sempre a MESMA referência,
//     `<regiao>/seg-00000001`;
//   - e o [ImmutableStore] é write-once por desenho (é o que o torna imutável).
//
// A composição destas três verdades é o que este teste mede: sobre um destino que SOBREVIVE ao
// processo, o primeiro ciclo depois de um reinício colide e devolve [ErrImmutable] — e devolve-o
// para sempre, porque o exportador reiniciado nunca avança do índice 1. Um nó configurado assim
// exportaria bem até ao primeiro restart e nunca mais; o `/metrics` mostraria falhas, mas o
// operador leria «o backup avariou», não «o backup nunca foi possível neste destino».
//
// A SEGUNDA METADE DO MESMO LIMITE, que este teste não mede porque não há o que medir:
// [Restorer.RestoreTo] recebe o `Manifest` e o `Checkpoint` COMO ARGUMENTOS — nada neste módulo
// os persiste. Segmentos duráveis sem manifesto persistido não são restauráveis por aqui.
//
// CONSEQUÊNCIA DE DESENHO, e é por isso que este teste existe: o nó compõe o destino por PORTA
// INJECTADA ([Config.BackupDestination]) e NÃO inventa um backend de ficheiro. Um backend durável
// exige primeiro a retoma do manifesto e a persistência do checkpoint — outro ticket, não este.
//
// Uma mutação que faça o exportador RETOMAR (ou que dê outra referência ao primeiro segmento de
// cada arranque) faz este teste cair. É o sinal certo: nesse dia o limite deixou de existir e a
// decisão de composição do nó tem de ser reaberta.
func TestAOS101_UmExportadorREINICIADOColideNoDestinoDURAVEL(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 2, "m")

	// O destino é o MESMO objecto nos dois arranques — é isso que modela a durabilidade.
	dst := NewInMemoryImmutableStore("eu-west")

	exp1, err := NewExporter(src, dst, newSigner(t), WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter (1.º arranque): %v", err)
	}
	r1, err := exp1.Export(ctx)
	if err != nil {
		t.Fatalf("1.º arranque, ciclo 1: %v", err)
	}
	if !r1.Created || r1.Ref == "" {
		t.Fatalf("1.º arranque devia ter escrito um segmento; got Created=%v Ref=%q", r1.Created, r1.Ref)
	}

	// «Reinício do processo»: exportador NOVO sobre o MESMO destino durável.
	exp2, err := NewExporter(src, dst, newSigner(t), WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter (2.º arranque): %v", err)
	}
	_, err = exp2.Export(ctx)
	if !errors.Is(err, ErrImmutable) {
		t.Fatalf("o exportador REINICIADO devia colidir em %q com ErrImmutable (nao ha retoma de manifesto); got err=%v", r1.Ref, err)
	}

	// E não é uma falha transitória: o índice não avança, pelo que o ciclo seguinte colide na
	// MESMA referência. É isto que torna o destino durável inutilizável, e não apenas ruidoso.
	if _, err := exp2.Export(ctx); !errors.Is(err, ErrImmutable) {
		t.Fatalf("a colisao devia ser PERMANENTE (o indice nao avanca); got err=%v", err)
	}
}
