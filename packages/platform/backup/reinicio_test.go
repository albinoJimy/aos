package backup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/platform/audit"
)

// AOS-101 — O QUE UM DESTINO DURÁVEL FAZ AO EXPORTADOR, MEDIDO.
//
// Este ficheiro MEDIA UM LIMITE e passou a medir o seu fecho. A versão anterior provava que um
// exportador reiniciado sobre um destino que sobrevive ao processo colidia com [ErrImmutable] — e
// colidia PARA SEMPRE, porque [NewExporter] começava sempre do génesis e o índice nunca avançava.
// Terminava com esta frase:
//
//	«Uma mutação que faça o exportador RETOMAR (ou que dê outra referência ao primeiro segmento de
//	cada arranque) faz este teste cair. É o sinal certo: nesse dia o limite deixou de existir e a
//	decisão de composição do nó tem de ser reaberta.»
//
// É esse dia. O teste caiu porque as DUAS mutações que ele nomeava foram feitas — a retoma e a
// referência endereçada por conteúdo (resume.go) — e o que aqui está agora prova a propriedade
// inversa: um destino durável é utilizável, e um exportador reiniciado CONTINUA a cadeia em vez de
// a recomeçar.
//
// O que MUDA para quem compõe: o nó continua a exigir o destino injectado ([Config.BackupDestination]
// e não inventa nenhum — mas a razão deixou de ser «nenhum backend durável é utilizável» e passou a
// ser a de sempre, que é não se escolher por um operador onde é que os backups dele vivem.
func TestAOS101_UmExportadorREINICIADORetomaACadeiaNoDestinoDURAVEL(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 2, "m")

	// O destino é o MESMO objecto nos dois arranques — é isso que modela a durabilidade.
	dst := NewInMemoryImmutableStore("eu-west")
	// E o SIGNER também é o mesmo. Uma chave nova por arranque tornaria os checkpoints anteriores
	// inverificáveis e a retoma seria recusada — o que está medido em
	// TestAOS101_RetomaComCHAVEDIFERENTEeRecusada, e é a mesma razão pela qual o nó recusa
	// auto-gerar uma (ErrBackupSigningKeyMissing).
	signer := newSigner(t)
	// E a CUSTÓDIA DA KEK também sobrevive ao processo — aqui um vault partilhado, que é uma FIXTURE:
	// no deployment, é a custódia Vault Transit num mount próprio que o faz (por envelope, AOS-453). Entropia REAL,
	// e não detRand: com detRand dois vaults separados geravam a MESMA KEK e o teste passava pela
	// razão errada. Uma KEK diferente por arranque é recusada — TestAOS101_RetomaComOUTRAKEKeRecusada.
	vault := audit.NewInMemoryKeyVault(nil)

	exp1, err := NewExporter(src, dst, signer, WithKeyVault(vault))
	if err != nil {
		t.Fatalf("NewExporter (1.º arranque): %v", err)
	}
	if exp1.ResumedFrom() != 0 {
		t.Fatalf("um destino VIRGEM nao tem nada que retomar; got ResumedFrom=%d", exp1.ResumedFrom())
	}
	r1, err := exp1.Export(ctx)
	if err != nil {
		t.Fatalf("1.º arranque, ciclo 1: %v", err)
	}
	if !r1.Created || r1.Cycle != 1 {
		t.Fatalf("1.º arranque devia ter selado o ciclo 1; got Created=%v Cycle=%d", r1.Created, r1.Cycle)
	}

	// «Reinício do processo»: exportador NOVO sobre o MESMO destino durável.
	exp2, err := NewExporter(src, dst, signer, WithKeyVault(vault))
	if err != nil {
		t.Fatalf("NewExporter (2.º arranque) devia RETOMAR e nao falhar: %v", err)
	}
	if got := exp2.ResumedFrom(); got != 1 {
		t.Fatalf("o 2.º arranque devia ter retomado do ciclo 1; got ResumedFrom=%d", got)
	}

	// O CURSOR foi retomado: sem eventos novos, não há nada que exportar. Antes da retoma, este
	// ciclo teria reexportado o log inteiro do génesis — e colidido.
	r2, err := exp2.Export(ctx)
	if err != nil {
		t.Fatalf("2.º arranque, ciclo sem novidade: %v", err)
	}
	if r2.Created {
		t.Fatalf("o exportador retomado NAO devia reexportar o que ja estava no backup; escreveu %q", r2.Ref)
	}

	// E o ciclo seguinte AVANÇA a cadeia, em vez de colidir na referência do ciclo 1.
	seed(t, src, "run-a", 3, "m")
	r3, err := exp2.Export(ctx)
	if err != nil {
		t.Fatalf("2.º arranque, ciclo com novidade: %v", err)
	}
	if !r3.Created || r3.Cycle != 2 {
		t.Fatalf("o exportador retomado devia selar o ciclo 2; got Created=%v Cycle=%d", r3.Created, r3.Cycle)
	}
	if r3.Events != 3 {
		t.Fatalf("so os 3 eventos NOVOS deviam ser exportados (incremental sobre o cursor retomado); got %d", r3.Events)
	}

	// A prova que fecha o ciclo: a cadeia atravessa a fronteira do processo e RESTAURA como uma
	// só — não basta verificar, porque uma cadeia que verifica com uma KEK perdida não restaura.
	ev := restaurarCadeia(t, dst, vault, signer, 2)
	if ev.EventsRestored != 5 {
		t.Fatalf("deviam restaurar-se os 5 eventos dos DOIS arranques; got %d", ev.EventsRestored)
	}
}

// A KEK: uma cadeia selada com uma custódia de chaves que não sobreviveu ao processo NÃO retoma.
//
// A assinatura prova quem selou; não prova que esta custódia ainda decifra o que foi selado. Sem a
// verificação, o 2.º arranque retomava, anunciava «RETOMADA … verificada», o manifesto verificava —
// e o restauro falhava no dia do DR com ErrSegmentTampered, lido como adulteração. É o que o vault
// de referência (em memória) faz a cada reinício do nó; a custódia que sobrevive é a de envelope
// (AOS-453, aos453_custodia_test.go).
func TestAOS101_RetomaComOUTRAKEKeRecusada(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 2, "m")
	dst := NewInMemoryImmutableStore("eu-west")
	signer := newSigner(t)

	exp1, err := NewExporter(src, dst, signer, WithKeyVault(audit.NewInMemoryKeyVault(nil)))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	if _, err := exp1.Export(ctx); err != nil {
		t.Fatalf("ciclo 1: %v", err)
	}

	// Mesma chave de assinatura, OUTRA custódia de KEK (entropia real — não detRand).
	_, err = NewExporter(src, dst, signer, WithKeyVault(audit.NewInMemoryKeyVault(nil)))
	if !errors.Is(err, ErrResumeUnverifiable) {
		t.Fatalf("uma KEK que nao e a que selou a cadeia devia recusar a retoma com ErrResumeUnverifiable; got %v", err)
	}
	if !strings.Contains(err.Error(), "KEK do backup NAO e a que selou") {
		t.Errorf("o erro tem de NOMEAR a KEK como causa (e nao adulteracao); got %v", err)
	}
}

// O CONTRA-CANTO da retoma: uma chave de assinatura diferente NÃO retoma.
//
// A retoma restaura o cursor incremental a partir do que o destino diz. Se isso fosse aceite sem
// prova, um registo forjado com StreamHeads acima do real faria o exportador SALTAR eventos — e o
// backup ficaria com um buraco que nada acusaria até ao dia do restauro. A assinatura é a raiz
// dessa prova, e por isso um exportador com outra chave RECUSA arrancar em vez de continuar uma
// cadeia que não prova ser sua.
//
// É a mesma propriedade que o nó já fazia valer no arranque (ErrBackupSigningKeyMissing: «uma
// chave nova por arranque tornaria os checkpoints anteriores inverificáveis»), agora medida onde
// ela vive.
func TestAOS101_RetomaComCHAVEDIFERENTEeRecusada(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 2, "m")
	dst := NewInMemoryImmutableStore("eu-west")

	exp1, err := NewExporter(src, dst, newSigner(t), WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	if _, err := exp1.Export(ctx); err != nil {
		t.Fatalf("ciclo 1: %v", err)
	}

	// Outro signer — o caso de quem regenera a chave a cada arranque.
	_, err = NewExporter(src, dst, newSigner(t), WithRandSource(detRand()))
	if !errors.Is(err, ErrResumeUnverifiable) {
		t.Fatalf("um exportador com OUTRA chave devia recusar arrancar com ErrResumeUnverifiable; got %v", err)
	}
}
