package backup

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// AOS-101 — os modos de falha da RETOMA, medidos.
//
// O reinicio_test.go mede o caminho feliz (um destino durável passou a ser utilizável). Este
// ficheiro mede os quatro sítios onde a retoma podia ser PIOR do que o limite que substituiu:
// um ciclo interrompido a meio, dois exportadores sobre a mesma cadeia, um cursor adulterado, e
// um sufixo de manifesto a passar por cadeia completa.

// lojaComFalhaNoCiclo é um [ImmutableStore] que deixa passar a escrita do SEGMENTO e faz falhar a
// do REGISTO DE CICLO — o crash exacto que a ordem das duas escritas tem de tolerar.
type lojaComFalhaNoCiclo struct {
	*InMemoryImmutableStore
	falhar bool
	err    error
}

func (s *lojaComFalhaNoCiclo) Put(ref string, blob []byte, retainUntil time.Time) error {
	if s.falhar && len(ref) > 0 && contemCiclo(ref) {
		return s.err
	}
	return s.InMemoryImmutableStore.Put(ref, blob, retainUntil)
}

// contemCiclo distingue a escrita do registo de ciclo da do segmento pela forma da referência —
// que é a única coisa que um [ImmutableStore] vê de um Put.
func contemCiclo(ref string) bool { return strings.Contains(ref, "/cycle-") }

// UM CICLO QUE MORRE ENTRE O SEGMENTO E O REGISTO NÃO BLOQUEIA O DESTINO PARA SEMPRE.
//
// É o defeito que a retoma, sozinha, teria apenas ADIADO um reinício: com uma referência de
// segmento puramente indexada, o segmento órfão do ciclo interrompido ocuparia a ref que a
// re-tentativa precisa, e o destino voltaria a ser permanentemente inutilizável — pelo mesmo
// mecanismo, uma camada mais abaixo.
//
// A referência endereçada por conteúdo é o que o fecha, por DOIS caminhos que dão no mesmo:
// se a re-tentativa produzir ciphertext diferente (DEK fresca), a ref é outra e o órfão fica
// retido e não referenciado; se produzir o mesmo, a ref colide e [Exporter.putSegment] aceita-a
// depois de confirmar o hash INTEIRO, porque o objecto que se queria escrever já lá está.
//
// Este teste NÃO fixa qual dos dois corre — isso dependeria de detalhes da fonte de entropia e do
// vault, que não são a propriedade em causa. Fixa o que importa: o ciclo AVANÇA, e a cadeia que
// dele resulta verifica.
func TestAOS101_CicloInterrompidoEntreSegmentoERegistoNaoBloqueiaODestino(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 2, "m")

	falhaDoDestino := errors.New("destino indisponivel a meio do ciclo")
	dst := &lojaComFalhaNoCiclo{
		InMemoryImmutableStore: NewInMemoryImmutableStore("eu-west"),
		falhar:                 true,
		err:                    falhaDoDestino,
	}
	signer := newSigner(t)

	exp1, err := NewExporter(src, dst, signer, WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	if _, err := exp1.Export(ctx); !errors.Is(err, falhaDoDestino) {
		t.Fatalf("o ciclo devia falhar na escrita do registo; got %v", err)
	}
	// O segmento FICOU (órfão) e a cadeia NÃO avançou — nenhum registo de ciclo foi selado.
	if dst.Len() != 1 {
		t.Fatalf("devia ter ficado exactamente o segmento orfao; got %d objectos", dst.Len())
	}

	// «Reinício»: o destino voltou a si e um exportador novo tenta outra vez.
	dst.falhar = false
	exp2, err := NewExporter(src, dst, signer, WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter (2.º arranque) sobre um ciclo interrompido: %v", err)
	}
	if got := exp2.ResumedFrom(); got != 0 {
		t.Fatalf("nenhum ciclo chegou a ser SELADO, logo nao ha nada que retomar; got ResumedFrom=%d", got)
	}
	r, err := exp2.Export(ctx)
	if err != nil {
		t.Fatalf("a re-tentativa devia avancar, e nao colidir para sempre no orfao: %v", err)
	}
	if !r.Created || r.Cycle != 1 {
		t.Fatalf("a re-tentativa devia selar o ciclo 1; got Created=%v Cycle=%d", r.Created, r.Cycle)
	}

	// E o resultado é uma cadeia que verifica — o órfão não a contamina.
	rst, err := NewRestorer(dst, exp2.Vault(), signer.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	m, cp, err := rst.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if err := rst.VerifyManifest(m, cp, 0); err != nil {
		t.Fatalf("a cadeia depois de um ciclo interrompido devia verificar: %v", err)
	}
}

// DOIS EXPORTADORES SOBRE O MESMO DESTINO COLIDEM NO REGISTO DE CICLO — E ISSO É O DESENHO.
//
// A referência do registo de ciclo é indexada de propósito: é a única escrita do ciclo que NÃO
// depende do conteúdo, e por isso é onde dois escritores se encontram. O segundo é recusado com
// [ErrChainOwned] em vez de bifurcar a cadeia em silêncio.
//
// A distinção face a uma adulteração é a mesma que o AOS-284 fez na hash-chain da auditoria, e
// pela mesma razão: uma bifurcação tem uma causa (dois donos) e uma correcção (um destino por
// exportador) que nada têm a ver com as de um atacante.
func TestAOS101_DoisExportadoresNoMesmoDestinoColidemNoRegistoDeCiclo(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 2, "m")
	dst := NewInMemoryImmutableStore("eu-west")
	signer := newSigner(t)

	// Os dois são construídos ANTES de qualquer ciclo: ambos vêem um destino virgem e ambos
	// julgam ser donos do ciclo 1. É a corrida real de duas réplicas com o mesmo destino.
	expA, err := NewExporter(src, dst, signer, WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter A: %v", err)
	}
	expB, err := NewExporter(src, dst, signer, WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter B: %v", err)
	}
	if _, err := expA.Export(ctx); err != nil {
		t.Fatalf("A, ciclo 1: %v", err)
	}
	_, err = expB.Export(ctx)
	if !errors.Is(err, ErrChainOwned) {
		t.Fatalf("B devia ser recusado com ErrChainOwned (bifurcacao, nao adulteracao); got %v", err)
	}
	// E NÃO como uma adulteração: quem lê o erro tem de ser mandado corrigir a configuração, não
	// procurar um atacante.
	if errors.Is(err, ErrSegmentTampered) || errors.Is(err, ErrChainBroken) {
		t.Fatalf("uma bifurcacao NAO se deve confundir com adulteracao; got %v", err)
	}
}

// UM CURSOR ADULTERADO NO DESTINO NÃO É ADOPTADO.
//
// É o modo de falha mais grave que a retoma introduz, e a razão de ela verificar antes de confiar:
// um registo com StreamHeads acima do real faria o exportador SALTAR os eventos intermédios, e o
// backup ficaria com um buraco que nada acusaria — nem o /metrics, nem a verificação da cadeia,
// que fecharia perfeitamente sobre o que lá estivesse. Só o dia do restauro.
//
// O que torna a defesa suficiente é o [canonicalSegment] COBRIR os StreamHeads: mexer no cursor
// muda o EntryHash, e o EntryHash recomputa-se no arranque.
func TestAOS101_UmCursorADULTERADONoDestinoNaoERetomado(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 2, "m")
	signer := newSigner(t)

	// Uma cadeia legítima, para ter um registo real de onde partir.
	origem := NewInMemoryImmutableStore("eu-west")
	exp, err := NewExporter(src, origem, signer, WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	if _, err := exp.Export(ctx); err != nil {
		t.Fatalf("ciclo 1: %v", err)
	}
	blob, err := origem.Get(cycleRef("eu-west", 1))
	if err != nil {
		t.Fatalf("Get do registo de ciclo: %v", err)
	}
	var rec cycleRecord
	if err := json.Unmarshal(blob, &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// A adulteração: o cursor salta para lá do que foi realmente exportado. O checkpoint é o
	// AUTÊNTICO — a assinatura continua a validar —, e é por isso que a verificação não pode
	// parar nela.
	rec.Entry.StreamHeads["run-a"] = 9999
	forjado, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	destinoForjado := NewInMemoryImmutableStore("eu-west")
	if err := destinoForjado.Put(cycleRef("eu-west", 1), forjado, t0.Add(time.Hour)); err != nil {
		t.Fatalf("Put do registo forjado: %v", err)
	}

	_, err = NewExporter(src, destinoForjado, signer, WithRandSource(detRand()))
	if !errors.Is(err, ErrResumeUnverifiable) {
		t.Fatalf("um cursor adulterado devia recusar o arranque com ErrResumeUnverifiable; got %v", err)
	}
}

// O SUFIXO DE UM EXPORTADOR RETOMADO NÃO PASSA POR CADEIA COMPLETA — E FALHA ALTO.
//
// [Exporter.Manifest] devolve só os elos selados por ESTE processo, e um sufixo não é restaurável.
// A questão não é se alguém se pode enganar — é se o engano é SILENCIOSO. Não é: a âncora não bate
// com o comprimento e [Restorer.VerifyManifest] recusa com [ErrChainBroken], que é exactamente o
// que o caminho certo ([Restorer.LoadManifest]) resolve.
func TestAOS101_ManifestoPARCIALDeUmExportadorRetomadoERecusado(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 2, "m")
	dst := NewInMemoryImmutableStore("eu-west")
	signer := newSigner(t)

	exp1, err := NewExporter(src, dst, signer, WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	if _, err := exp1.Export(ctx); err != nil {
		t.Fatalf("ciclo 1: %v", err)
	}

	exp2, err := NewExporter(src, dst, signer, WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter (retoma): %v", err)
	}
	seed(t, src, "run-a", 1, "m")
	if _, err := exp2.Export(ctx); err != nil {
		t.Fatalf("ciclo 2: %v", err)
	}

	rst, err := NewRestorer(dst, exp2.Vault(), signer.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}

	// O caminho ERRADO: o manifesto do processo (1 elo) com o checkpoint do ciclo 2.
	if err := rst.VerifyManifest(exp2.Manifest(), exp2.Checkpoint(), 0); !errors.Is(err, ErrChainBroken) {
		t.Fatalf("um sufixo de cadeia devia ser recusado ALTO com ErrChainBroken; got %v", err)
	}

	// O caminho CERTO devolve a cadeia inteira, e verifica.
	m, cp, err := rst.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(m.Segments) != 2 {
		t.Fatalf("LoadManifest devia reconstruir os 2 elos; got %d", len(m.Segments))
	}
	if err := rst.VerifyManifest(m, cp, 0); err != nil {
		t.Fatalf("a cadeia reconstruida devia verificar: %v", err)
	}
}

// A SONDAGEM ENCONTRA O ÚLTIMO CICLO, E NÃO O PRIMEIRO NEM UM DO MEIO.
//
// A descoberta é exponencial-e-bissecção sobre uma cadeia contígua; um erro de fronteira daria um
// exportador a retomar de um ciclo ANTIGO, que colidiria no registo seguinte ([ErrChainOwned]) e
// mandaria o operador procurar um segundo escritor que não existe.
func TestAOS101_ASondagemEncontraOUltimoCiclo(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	dst := NewInMemoryImmutableStore("eu-west")
	signer := newSigner(t)

	exp, err := NewExporter(src, dst, signer, WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	const ciclos = 5
	for i := 0; i < ciclos; i++ {
		seed(t, src, "run-a", 1, "m")
		r, err := exp.Export(ctx)
		if err != nil {
			t.Fatalf("ciclo %d: %v", i+1, err)
		}
		if r.Cycle != uint64(i+1) {
			t.Fatalf("ciclo %d selado como %d", i+1, r.Cycle)
		}
	}

	if got, err := lastSealedCycle(dst, "eu-west"); err != nil || got != ciclos {
		t.Fatalf("a sondagem devia encontrar o ciclo %d; got %d (err=%v)", ciclos, got, err)
	}
	exp2, err := NewExporter(src, dst, signer, WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter (retoma): %v", err)
	}
	if got := exp2.ResumedFrom(); got != ciclos {
		t.Fatalf("devia retomar do ciclo %d; got %d", ciclos, got)
	}
}

// UM EXPORTADOR RETOMADO SEM NOVIDADE REPORTA A COBERTURA REAL, E NÃO UM MAPA VAZIO.
//
// O ciclo «nada mudou» devolve os StreamHeads a partir do último elo. Num exportador retomado esse
// elo não está em memória, e sem a base adoptada o resultado seria um mapa VAZIO — o backup a
// declarar cobertura nenhuma no preciso momento em que está em dia. Quem lê isto para publicar
// cobertura ou RPO leria uma regressão que não aconteceu.
func TestAOS101_CicloSemNovidadeDepoisDeRetomarReportaACoberturaReal(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 2, "m")
	dst := NewInMemoryImmutableStore("eu-west")
	signer := newSigner(t)

	exp1, err := NewExporter(src, dst, signer, WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	if _, err := exp1.Export(ctx); err != nil {
		t.Fatalf("ciclo 1: %v", err)
	}

	exp2, err := NewExporter(src, dst, signer, WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter (retoma): %v", err)
	}
	r, err := exp2.Export(ctx)
	if err != nil {
		t.Fatalf("ciclo sem novidade: %v", err)
	}
	if r.Created {
		t.Fatalf("nao havia novidade; nao devia ter sido escrito segmento")
	}
	if got := r.StreamHeads["run-a"]; got != 2 {
		t.Fatalf("o ciclo em dia devia reportar a cobertura RETOMADA (run-a=2); got %d de %v", got, r.StreamHeads)
	}
	if r.Cycle != 1 {
		t.Fatalf("o ciclo corrente devia continuar a ser o 1 (nada foi selado); got %d", r.Cycle)
	}
}
