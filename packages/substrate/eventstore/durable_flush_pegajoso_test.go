package eventstore

// AOS-348 — UM `Flush` FALHADO MATAVA O WAL PARA SEMPRE E ANUNCIAVA-SE TRANSITÓRIO.
//
// # O DEFEITO, medido
//
// `wal.append` tratava o `Flush` como o `Sync`: em erro, chamava [wal.desfazer]. Mas
// `desfazer` truncava o ficheiro e NUNCA fazia `w.w.Reset(w.f)`. O erro pegajoso do
// `bufio.Writer` ficava, e o append seguinte morria já no `w.w.Write(hdr[:])` com um
// `return err` cru — sem passar por `desfazer` e sem marcar `envenenado`. O
// `Store.Append` embrulhava-o como «eventstore: persistir evento committed», que se lê
// como um ENOSPC transitório qualquer.
//
// Medido lado a lado, mesma falha, mesmo retry:
//
//	[Flush]  2.º append falha = "persistir evento committed: sonda: write falhou (ENOSPC)"
//	         RETRY com a falha REMOVIDA = mesma mensagem ENOSPC   ****  MORREU
//	         retries extra #1 #2 #3 = ENOSPC, ENOSPC, ENOSPC
//	[fsync]  falha = "sonda: fsync falhou (EIO)"
//	         RETRY = <nil>                                        ****  RECUPERA
//
// Um caminho recupera, o outro morre em silêncio. O chamador lê «ENOSPC», conclui «disco
// cheio, volto a tentar», e nunca mais escreve nada.
//
// # PORQUE NUNCA FOI APANHADO
//
// A costura [ficheiroWAL] NÃO cobria o caminho de escrita: o `bufio.Writer` é construído
// em `openWALAppend` sobre o `*os.File` original, pelo que trocar só `s.wal.f` — como os
// testes faziam — interceptava `Sync` e `Close` e mais nada. Medido: `writes=0`. O
// `Write` de [ficheiroFalhado] era código morto. [TestAOS348_CosturaCobreOCaminhoDeEscrita]
// é o sensor que impede essa regressão de voltar.
//
// E o gémeo do `Flush` ([TestWAL_FlushFalhadoDepoisDeEscreverTudo_NaoDeixaRegistoNoFicheiro])
// verificava o FICHEIRO e nunca RETENTAVA — pelo que não distinguia «reposto e utilizável»
// de «reposto e morto».

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// escritorComFalhaRemovivel escreve sempre tudo no ficheiro real, mas reporta erro
// enquanto `falhar` estiver ligado. Desligar `falhar` é o análogo de a avaria de I/O
// passar — que é a condição em que um retry TEM de poder recuperar.
type escritorComFalhaRemovivel struct {
	real   ficheiroWAL
	falhar bool
	writes int
}

func (f *escritorComFalhaRemovivel) Sync() error  { return f.real.Sync() }
func (f *escritorComFalhaRemovivel) Close() error { return f.real.Close() }

func (f *escritorComFalhaRemovivel) Write(p []byte) (int, error) {
	n, err := f.real.Write(p)
	if err != nil {
		return n, err
	}
	f.writes++
	if f.falhar {
		return n, errors.New("sonda: write escreveu tudo e falhou (ENOSPC)")
	}
	return n, nil
}

// TestAOS348_RetryDepoisDeFlushFalhadoRecupera é o teste que nasceu VERMELHO: com a falha
// REMOVIDA, o retry devolvia a mesma mensagem ENOSPC, indefinidamente.
func TestAOS348_RetryDepoisDeFlushFalhadoRecupera(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.wal")

	s, err := Open(path, WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)

	sonda := &escritorComFalhaRemovivel{real: s.wal.f, falhar: true}
	s.wal.trocarFicheiro(sonda)

	_, errFalha := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":2}`), RunID: "run-A", StepID: "s2"})
	if errFalha == nil {
		t.Fatal("esperava erro do Flush")
	}

	// A AVARIA PASSOU. Um erro que se anuncia como transitório tem de o ser.
	sonda.falhar = false
	res, errRetry := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":2}`), RunID: "run-A", StepID: "s2b"})
	if errRetry != nil {
		t.Fatalf("RETRY com a falha REMOVIDA = %v — o Flush falhado matou o WAL para sempre "+
			"enquanto devolvia um erro que se lê como transitório (%v)", errRetry, errFalha)
	}
	if res.Seq != 2 {
		t.Fatalf("seq do retry = %d, quero 2 — o append falhado não podia ter consumido um seq", res.Seq)
	}

	// E o que ficou durável é exactamente o que foi confirmado: s1 e s2b, gapless.
	evs := registosNoFicheiro(t, path)
	if len(evs) != 2 {
		t.Fatalf("ficheiro com %d registo(s), quero 2", len(evs))
	}
	if evs[1].StepID != "s2b" {
		t.Fatalf("segundo registo = %q, quero s2b", evs[1].StepID)
	}
}

// TestAOS348_FlushEFsyncRecuperamIGUAL fixa o contraste que era o defeito. Sem este teste
// a assimetria pode regressar sem que nada avermelhe: os dois caminhos são escritos em
// sítios diferentes e nada os obriga a concordar.
func TestAOS348_FlushEFsyncRecuperamIGUAL(t *testing.T) {
	casos := []struct {
		nome     string
		instalar func(t *testing.T, s *Store) (ligar func(bool))
	}{
		{"flush", func(t *testing.T, s *Store) func(bool) {
			sonda := &escritorComFalhaRemovivel{real: s.wal.f, falhar: true}
			s.wal.trocarFicheiro(sonda)
			return func(v bool) { sonda.falhar = v }
		}},
		{"fsync", func(t *testing.T, s *Store) func(bool) {
			ff := &ficheiroFalhado{real: s.wal.f, falharApós: 0}
			s.wal.trocarFicheiro(ff)
			return func(v bool) {
				if v {
					ff.falharApós = 0
					ff.syncs = 0
				} else {
					ff.falharApós = 1 << 30
				}
			}
		}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "events.wal")
			s, err := Open(path, WithReplicas(1), WithQuorum(1))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer s.Close()
			appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)

			ligar := c.instalar(t, s)
			if _, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":2}`), RunID: "run-A", StepID: "s2"}); err == nil {
				t.Fatal("esperava erro")
			}
			ligar(false) // a avaria passou

			if _, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":3}`), RunID: "run-A", StepID: "s3"}); err != nil {
				t.Fatalf("caminho %q NÃO recupera do retry (%v) — o gémeo recupera; "+
					"a assimetria é o defeito de AOS-348", c.nome, err)
			}
		})
	}
}

// TestAOS348_CosturaCobreOCaminhoDeEscrita é o sensor que impede a regressão da COSTURA.
// Um teste que injecta uma sonda que nunca é chamada passa por engano, e foi assim que o
// erro pegajoso do `Flush` sobreviveu. Aqui a sonda tem de ter visto escritas.
func TestAOS348_CosturaCobreOCaminhoDeEscrita(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")
	s, ff := abreComFsyncFalhado(t, path, 1<<30, false)
	defer s.Close()
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)

	if ff.writes == 0 {
		t.Fatal(">>> A COSTURA NÃO COBRE O CAMINHO DE ESCRITA <<< writes=0 — trocar s.wal.f " +
			"sem reconstruir o bufio.Writer deixa a sonda a ser código morto (AOS-348)")
	}
	if ff.syncs == 0 {
		t.Fatal("a costura deixou de cobrir o Sync")
	}
}
