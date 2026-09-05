package audit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// aos311_ctx_test.go — AOS-311: uma selagem sob contexto morto NÃO escreve.
//
// O defeito: Append recebia ctx e só o passava à porta de posse, que sem posse armada
// devolve nil sem olhar para ele. Um prazo esgotado a meio da cadeia PDP → sink selava na
// mesma, e o «timeout fail-closed» da STRIDE §4.3-D era verdadeiro só para o eventstore.
//
// A prova aqui é negativa e mede três coisas: Head inalterado, ficheiro sem bytes novos, e
// o erro devolvido é o do contexto — distinguível de ErrParticaoAlheia. Sem time.Sleep: o
// contexto chega morto (WithCancel já cancelado, WithDeadline no passado), ou morre entre
// os dois pontos de verificação por um contexto de teste que muda de resposta.

// tamanhoDoWAL devolve o tamanho actual do ficheiro (0 se ainda não existir).
func tamanhoDoWAL(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	return fi.Size()
}

// exigirRecusaPorContexto verifica o contrato do AOS-311 sobre um erro devolvido por Append.
func exigirRecusaPorContexto(t *testing.T, err, esperado error) {
	t.Helper()
	if err == nil {
		t.Fatal("Append sob contexto morto devolveu nil — selou quando não devia")
	}
	if !errors.Is(err, esperado) {
		t.Fatalf("erro = %v, quero errors.Is(_, %v)", err, esperado)
	}
	if errors.Is(err, ErrParticaoAlheia) {
		t.Fatalf("erro %v confunde-se com ErrParticaoAlheia — tem de ser distinguível", err)
	}
}

// contextosMortos são os dois modos pedidos pela AC2: cancelado antes da chamada e prazo
// já no passado.
func contextosMortos(t *testing.T) map[string]struct {
	ctx      context.Context
	esperado error
} {
	t.Helper()
	cancelado, cancel := context.WithCancel(context.Background())
	cancel()
	expirado, cancelExp := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	t.Cleanup(cancelExp)
	return map[string]struct {
		ctx      context.Context
		esperado error
	}{
		"cancelado": {cancelado, context.Canceled},
		"expirado":  {expirado, context.DeadlineExceeded},
	}
}

// TestAOS311_FileStore_ContextoMortoNaoSela — AC1/AC2: com o ctx já morto à entrada, nada
// é selado nem persistido, e o erro é o do contexto.
func TestAOS311_FileStore_ContextoMortoNaoSela(t *testing.T) {
	for nome, caso := range contextosMortos(t) {
		t.Run(nome, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "worm.wal")
			s := openWORM(t, path)
			defer s.Close()
			vivo := context.Background()

			// Um registo vivo primeiro, para que Head e o ficheiro tenham algo a preservar.
			if _, err := s.Append(vivo, sampleRecord("run-1", DecisionAllow)); err != nil {
				t.Fatalf("append vivo: %v", err)
			}
			headAntes, _ := s.Head(vivo, "run-1")
			bytesAntes := tamanhoDoWAL(t, path)

			_, err := s.Append(caso.ctx, sampleRecord("run-1", DecisionDeny))
			exigirRecusaPorContexto(t, err, caso.esperado)

			if h, _ := s.Head(vivo, "run-1"); h != headAntes {
				t.Fatalf("Head mudou de %d para %d sob contexto morto", headAntes, h)
			}
			if b := tamanhoDoWAL(t, path); b != bytesAntes {
				t.Fatalf("ficheiro cresceu de %d para %d bytes sob contexto morto", bytesAntes, b)
			}
			// O audit_seq não foi consumido: a próxima escrita viva reusa a posição.
			rec, err := s.Append(vivo, sampleRecord("run-1", DecisionDeny))
			if err != nil {
				t.Fatalf("append vivo após recusa: %v", err)
			}
			if rec.AuditSeq != headAntes+1 {
				t.Fatalf("audit_seq = %d, quero %d (posição reutilizada)", rec.AuditSeq, headAntes+1)
			}
			if err := Verify(vivo, s, "run-1", 1, rec.AuditSeq); err != nil {
				t.Fatalf("Verify após recusa por contexto: %v", err)
			}
		})
	}
}

// ctxQueMorreDepois é um contexto de teste cujo Err é nil nas primeiras `vivoAte` consultas
// e context.DeadlineExceeded a partir daí. Serve para provar o SEGUNDO ponto de verificação
// da AC1 (antes de persist) sem dormir: a primeira consulta, à entrada, passa; a segunda,
// já com o selo calculado e o s.mu detido, encontra o prazo esgotado.
type ctxQueMorreDepois struct {
	context.Context
	consultas *atomic.Int32
	vivoAte   int32
}

func (c ctxQueMorreDepois) Err() error {
	if c.consultas.Add(1) > c.vivoAte {
		return context.DeadlineExceeded
	}
	return nil
}

// TestAOS311_FileStore_PrazoMorreEntreEntradaEPersist — AC1 (segundo ponto) e o cenário do
// ticket: o contexto sobrevive à entrada e morre antes da selagem durável. Nada é escrito.
func TestAOS311_FileStore_PrazoMorreEntreEntradaEPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worm.wal")
	s := openWORM(t, path)
	defer s.Close()
	vivo := context.Background()

	if _, err := s.Append(vivo, sampleRecord("run-1", DecisionAllow)); err != nil {
		t.Fatalf("append vivo: %v", err)
	}
	bytesAntes := tamanhoDoWAL(t, path)

	consultas := &atomic.Int32{}
	ctx := ctxQueMorreDepois{Context: context.Background(), consultas: consultas, vivoAte: 1}
	_, err := s.Append(ctx, sampleRecord("run-1", DecisionDeny))
	exigirRecusaPorContexto(t, err, context.DeadlineExceeded)

	if n := consultas.Load(); n < 2 {
		t.Fatalf("Append consultou ctx.Err() %d vez(es); a AC1 exige dois pontos (entrada e antes de persist)", n)
	}
	if h, _ := s.Head(vivo, "run-1"); h != 1 {
		t.Fatalf("Head = %d, quero 1 — o registo entrou na cadeia apesar do prazo", h)
	}
	if b := tamanhoDoWAL(t, path); b != bytesAntes {
		t.Fatalf("ficheiro cresceu de %d para %d bytes — persist correu sob prazo esgotado", bytesAntes, b)
	}
}

// TestAOS311_FileStore_PosseArmadaSemCorrida — AC4: com posse composta, a ordem é
// posse → ctx. Uma posse AFIRMATIVA sob ctx morto recusa pelo contexto (e não conta como
// recusa de posse); uma posse NEGADA sob ctx morto recusa por posse — o primeiro «não»
// ganha, e nenhum dos dois escreve.
func TestAOS311_FileStore_PosseArmadaSemCorrida(t *testing.T) {
	cancelado, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("posse afirmativa, ctx morto → erro do contexto", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "worm.wal")
		s, err := OpenFileStore(path, ComPosseDeParticao(posseFixa{minhas: map[string]bool{"minha": true}}))
		if err != nil {
			t.Fatalf("OpenFileStore: %v", err)
		}
		defer s.Close()

		_, err = s.Append(cancelado, sampleRecord("minha", DecisionAllow))
		exigirRecusaPorContexto(t, err, context.Canceled)
		if total, _ := s.RecusasDePosse(); total != 0 {
			t.Fatalf("recusas de posse = %d, quero 0 — uma recusa por contexto não é uma recusa de posse", total)
		}
		if h, _ := s.Head(context.Background(), "minha"); h != 0 {
			t.Fatalf("Head = %d, quero 0", h)
		}
		if b := tamanhoDoWAL(t, path); b != 0 {
			t.Fatalf("ficheiro com %d bytes, quero 0", b)
		}
	})

	t.Run("posse negada, ctx morto → ErrParticaoAlheia (posse primeiro)", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "worm.wal")
		s, err := OpenFileStore(path, ComPosseDeParticao(posseFixa{minhas: map[string]bool{}}))
		if err != nil {
			t.Fatalf("OpenFileStore: %v", err)
		}
		defer s.Close()

		_, err = s.Append(cancelado, sampleRecord("alheia", DecisionAllow))
		if !errors.Is(err, ErrParticaoAlheia) {
			t.Fatalf("erro = %v, quero ErrParticaoAlheia (a posse é consultada antes do ctx)", err)
		}
		if total, _ := s.RecusasDePosse(); total != 1 {
			t.Fatalf("recusas de posse = %d, quero 1", total)
		}
		if b := tamanhoDoWAL(t, path); b != 0 {
			t.Fatalf("ficheiro com %d bytes, quero 0", b)
		}
	})
}

// TestAOS311_MemStore_ContextoMortoNaoSela — AC4 do enunciado (simetria): o MemStore
// recusa à entrada, para que testes sobre ele não passem pela razão errada.
func TestAOS311_MemStore_ContextoMortoNaoSela(t *testing.T) {
	for nome, caso := range contextosMortos(t) {
		t.Run(nome, func(t *testing.T) {
			s := NewMemStore()
			vivo := context.Background()
			if _, err := s.Append(vivo, sampleRecord("run-1", DecisionAllow)); err != nil {
				t.Fatalf("append vivo: %v", err)
			}
			_, err := s.Append(caso.ctx, sampleRecord("run-1", DecisionDeny))
			exigirRecusaPorContexto(t, err, caso.esperado)
			if h, _ := s.Head(vivo, "run-1"); h != 1 {
				t.Fatalf("Head = %d, quero 1", h)
			}
			rec, err := s.Append(vivo, sampleRecord("run-1", DecisionDeny))
			if err != nil || rec.AuditSeq != 2 {
				t.Fatalf("append vivo após recusa: rec.AuditSeq=%d err=%v, quero 2/nil", rec.AuditSeq, err)
			}
		})
	}
}
