package backup

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// retoma_test.go — o remédio para o que reinicio_test.go mede.
//
// O teste vizinho afirma que um exportador reiniciado COLIDE para sempre num destino
// durável. Continua a afirmá-lo, e bem: a retoma é EXPLÍCITA, não um default. Estes testes
// provam que quem a chama deixa de colidir — e que ela recusa o que não deve adoptar.

// exportadorSobre devolve um exportador novo sobre o mesmo destino e o MESMO assinante.
//
// O assinante partilhado não é conveniência de teste: em produção a chave de assinatura vem
// da configuração e é a MESMA entre arranques (é isso que a torna raiz de confiança). Um
// teste com chaves diferentes por arranque mediria um cenário que não existe.
func exportadorSobre(t *testing.T, src *eventstore.Store, dst ImmutableStore, s *Ed25519Signer) *Exporter {
	t.Helper()
	e, err := NewExporter(src, dst, s, WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	return e
}

func TestAOS101_ComRetomaOExportadorReiniciadoCONTINUA(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 2, "m")
	dst := NewInMemoryImmutableStore("eu-west")
	assinante := newSigner(t)

	exp1 := exportadorSobre(t, src, dst, assinante)
	r1, err := exp1.Export(ctx)
	if err != nil {
		t.Fatalf("1.º arranque: %v", err)
	}
	if r1.Cycle != 1 {
		t.Fatalf("o 1.º ciclo devia ser 1; veio %d", r1.Cycle)
	}

	// Reinício do processo, agora COM retoma.
	seed(t, src, "run-a", 3, "m2") // eventos novos, que só o 2.º arranque vê
	exp2 := exportadorSobre(t, src, dst, assinante)
	if err := exp2.RetomarDoDestino(ctx, assinante.Public()); err != nil {
		t.Fatalf("RetomarDoDestino: %v", err)
	}
	r2, err := exp2.Export(ctx)
	if err != nil {
		t.Fatalf("depois de retomar, o Export devia passar: %v", err)
	}

	// O ÍNDICE AVANÇA — é o que distingue continuar de recomeçar.
	if r2.Cycle != 2 {
		t.Fatalf("depois da retoma o ciclo devia ser 2; veio %d", r2.Cycle)
	}
	if r2.Ref == r1.Ref {
		t.Fatalf("o 2.º ciclo reescreveu a ref do 1.º (%q)", r1.Ref)
	}

	// E NÃO RE-EXPORTOU o que já lá estava: o cursor veio dos StreamHeads do manifesto.
	// Sem isto a retoma «funcionaria» duplicando todos os eventos no segundo segmento —
	// que passaria nos testes de índice e falsificaria a cobertura.
	if r2.Events != 3 {
		t.Fatalf("o 2.º ciclo devia exportar só os 3 eventos NOVOS; exportou %d", r2.Events)
	}

	// A cadeia fecha de ponta a ponta contra o checkpoint do 2.º ciclo.
	rest, err := NewRestorer(dst, exp2.Vault(), assinante.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	if err := rest.VerifyManifest(exp2.Manifest(), exp2.Checkpoint(), 2); err != nil {
		t.Fatalf("a cadeia devia verificar depois da retoma: %v", err)
	}
	if n := len(exp2.Manifest().Segments); n != 2 {
		t.Fatalf("o manifesto retomado devia ter 2 segmentos; tem %d", n)
	}
}

func TestAOS101_RetomaEmDestinoVazioNaoEErro(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	dst := NewInMemoryImmutableStore("eu-west")
	assinante := newSigner(t)

	exp := exportadorSobre(t, src, dst, assinante)
	if err := exp.RetomarDoDestino(ctx, assinante.Public()); err != nil {
		t.Fatalf("retomar de um destino vazio não é erro: %v", err)
	}
	seed(t, src, "run-a", 1, "m")
	r, err := exp.Export(ctx)
	if err != nil || r.Cycle != 1 {
		t.Fatalf("depois de uma retoma vazia o 1.º ciclo é 1; veio ciclo=%d err=%v", r.Cycle, err)
	}
}

// TestAOS101_ARetomaRECUSAUmDestinoAdulterado — a razão de a retoma verificar.
//
// Adoptar um manifesto sem o confrontar escreveria segmentos legítimos por cima de uma
// história forjada, e o resultado verificaria como íntegro a partir daí. É a mesma classe
// de defeito que o AOS-284 fechou no WORM: duas histórias costuradas passam a parecer uma.
func TestAOS101_ARetomaRecusaUmDestinoAdulterado(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 2, "m")
	dst := NewInMemoryImmutableStore("eu-west")
	assinante := newSigner(t)

	exp1 := exportadorSobre(t, src, dst, assinante)
	if _, err := exp1.Export(ctx); err != nil {
		t.Fatalf("1.º arranque: %v", err)
	}

	// Adultera o estado persistido: mexe no manifesto sem re-assinar o checkpoint.
	ref := refDoManifesto("eu-west", 1)
	blob, err := dst.Get(ref)
	if err != nil {
		t.Fatalf("ler o estado: %v", err)
	}
	var est estadoPersistido
	if err := json.Unmarshal(blob, &est); err != nil {
		t.Fatalf("desserializar: %v", err)
	}
	est.Manifesto.Segments[0].Events = 999 // um campo COBERTO pelo EntryHash
	adulterado, err := json.Marshal(est)
	if err != nil {
		t.Fatalf("serializar: %v", err)
	}
	// O objecto de estado NAO leva object-lock (ver persistirEstado), pelo que se apaga e
	// reescreve sem API so-para-teste. Os SEGMENTOS, esses, continuam presos pela retencao.
	if err := dst.Delete(ref, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("apagar o estado: %v", err)
	}
	if err := dst.Put(ref, adulterado, time.Now()); err != nil {
		t.Fatalf("reescrever o estado adulterado: %v", err)
	}

	exp2 := exportadorSobre(t, src, dst, assinante)
	err = exp2.RetomarDoDestino(ctx, assinante.Public())
	if err == nil {
		t.Fatal("a retoma devia RECUSAR um manifesto adulterado")
	}
	if !strings.Contains(err.Error(), "não verifica") {
		t.Fatalf("o erro devia dizer que não verifica: %v", err)
	}
}

func TestAOS101_RetomarDepoisDoPrimeiroExportERecusada(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 1, "m")
	dst := NewInMemoryImmutableStore("eu-west")
	assinante := newSigner(t)

	exp := exportadorSobre(t, src, dst, assinante)
	if _, err := exp.Export(ctx); err != nil {
		t.Fatalf("Export: %v", err)
	}
	// Retomar por cima de um exportador que já escreveu misturaria duas histórias na
	// mesma cadeia. Recusa-se em vez de se adivinhar qual delas o chamador queria.
	if err := exp.RetomarDoDestino(ctx, assinante.Public()); err == nil {
		t.Fatal("retomar depois do primeiro Export devia ser recusado")
	}
}

// TestAOS101_ARetomaEncontraOUltimoCicloComPoucasIdas — a procura, medida.
//
// O ImmutableStore não tem List, pelo que o último ciclo se PROCURA. O que se afirma aqui
// não é a elegância: é que a procura ACERTA em vários tamanhos, incluindo os que apanham
// os limites da duplicação (potências de dois e os seus vizinhos).
func TestAOS101_ARetomaEncontraOUltimoCiclo(t *testing.T) {
	ctx := context.Background()
	for _, ciclos := range []int{1, 2, 3, 4, 5, 8, 9, 16, 17} {
		src := newSourceStore(t, "board-eu", "eu-west")
		dst := NewInMemoryImmutableStore("eu-west")
		assinante := newSigner(t)
		exp := exportadorSobre(t, src, dst, assinante)
		for i := 0; i < ciclos; i++ {
			seed(t, src, "run-a", 1, "m")
			if _, err := exp.Export(ctx); err != nil {
				t.Fatalf("ciclos=%d, Export %d: %v", ciclos, i+1, err)
			}
		}
		novo := exportadorSobre(t, src, dst, assinante)
		if err := novo.RetomarDoDestino(ctx, assinante.Public()); err != nil {
			t.Fatalf("ciclos=%d, RetomarDoDestino: %v", ciclos, err)
		}
		if n := len(novo.Manifest().Segments); n != ciclos {
			t.Fatalf("ciclos=%d: a retoma trouxe %d segmentos", ciclos, n)
		}
		r, err := func() (ExportResult, error) { seed(t, src, "run-a", 1, "z"); return novo.Export(ctx) }()
		if err != nil {
			t.Fatalf("ciclos=%d, Export depois da retoma: %v", ciclos, err)
		}
		if r.Cycle != uint64(ciclos+1) {
			t.Fatalf("ciclos=%d: o ciclo seguinte devia ser %d; veio %d", ciclos, ciclos+1, r.Cycle)
		}
	}
}
