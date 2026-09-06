package eventstore

// ERRO DEVOLVIDO ⇒ NADA FICOU DURÁVEL — a invariante que o WAL não cumpria.
//
// # O DEFEITO, medido
//
// `wal.append` escrevia, fazia `Flush` e só depois `Sync`. Um `Flush` bem-sucedido
// significa que os bytes JÁ passaram por write(2); se o `fsync` seguinte falhar com o
// processo VIVO — EIO, ENOSPC sob delayed allocation, EDQUOT, volume thin-provisioned
// ou em rede — o registo ficava COMPLETO e com CRC VÁLIDO no ficheiro, e o `Append`
// devolvia erro. Medido a 2026-08-30: `head` vivo=1 e WAL em disco com 2 registos; e
// como os bytes estão na page cache independentemente do fsync, bastava um reinício de
// processo para o evento dado como FALHADO ressuscitar.
//
// A consequência aguda era outra: o índice de deduplicação do Store só é povoado DEPOIS
// da escrita no WAL, pelo que um retry não recebia `StatusDuplicate` — recebia o MESMO
// seq do órfão. O ficheiro ficava com seqs [1 2 2] e o `Open` seguinte recusava com
// `E_RESTORE_ORDER`. O nó deixava de arrancar.
//
// # PORQUE ESTE RAMO NÃO ESTAVA COBERTO
//
// O teste de regressão que devia cobri-lo — [TestDurable_PersistErrorNoPhantom] — injecta
// a falha FECHANDO o descritor, o que faz falhar o `Write` e nunca o `Sync`; e não chega a
// inspeccionar o ficheiro do WAL, só o estado in-memory. Não era descuido: com um
// *os.File não havia como falhar só o `Sync`. É por isso que a correcção traz a costura
// [ficheiroWAL] — sem ela, este teste não é escrevível.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// ficheiroFalhado embrulha o ficheiro real e falha o `Sync` a partir da N-ésima chamada.
// O `Write` passa SEMPRE — é isso que reproduz a janela real: bytes no ficheiro, erro no
// chamador.
type ficheiroFalhado struct {
	real       ficheiroWAL
	syncs      int
	writes     int
	falharApós int
}

// writes conta as escritas que REALMENTE passaram pela sonda. Existe para o teste poder
// PROVAR que a costura cobre o caminho de escrita — antes de AOS-348 este contador
// ficava a zero e ninguém reparava.
func (f *ficheiroFalhado) Write(p []byte) (int, error) {
	f.writes++
	return f.real.Write(p)
}
func (f *ficheiroFalhado) Close() error { return f.real.Close() }

func (f *ficheiroFalhado) Sync() error {
	f.syncs++
	if f.syncs > f.falharApós {
		return errors.New("sonda: fsync falhou (EIO)")
	}
	return f.real.Sync()
}

// abreComFsyncFalhado abre um store durável e substitui o ficheiro do WAL por um que
// falha o `Sync` depois de `apos` chamadas bem-sucedidas.
func abreComFsyncFalhado(t *testing.T, path string, apos int, falharTruncate bool) (*Store, *ficheiroFalhado) {
	t.Helper()
	s, err := Open(path, WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	ff := &ficheiroFalhado{real: s.wal.f, falharApós: apos}
	// AOS-348: [wal.trocarFicheiro] troca o descritor E reconstrói o `bufio.Writer` sobre
	// ele. Antes trocava-se só `s.wal.f`, e o writer continuava agarrado ao *os.File
	// original — o `Write` da sonda nunca era chamado (medido: writes=0). O ramo do
	// `Flush` ficou por cobrir precisamente por isso.
	s.wal.trocarFicheiro(ff)
	if falharTruncate {
		// A truncatura vai por [os.Truncate] (ver o comentário de wal.truncar); para a
		// fazer falhar substitui-se a função, nao um metodo do descritor.
		s.wal.truncar = func(int64) error { return errors.New("sonda: truncate falhou (EIO)") }
	}
	return s, ff
}

func registosNoFicheiro(t *testing.T, path string) []Event {
	t.Helper()
	evs, _, _, err := replayWAL(path)
	if err != nil {
		t.Fatalf("replayWAL(%q): %v", path, err)
	}
	return evs
}

// TestWAL_FsyncFalhado_NaoDeixaRegistoNoFicheiro é o teste que NASCEU VERMELHO: antes da
// correcção o ficheiro ficava com o registo que o chamador soube ter falhado.
func TestWAL_FsyncFalhado_NaoDeixaRegistoNoFicheiro(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.wal")

	s, _ := abreComFsyncFalhado(t, path, 1, false)
	if _, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":1}`), RunID: "run-A", StepID: "s1"}); err != nil {
		t.Fatalf("1º append devia passar: %v", err)
	}
	// 2º append: o Write passa, o fsync falha.
	_, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":2}`), RunID: "run-A", StepID: "s2"})
	if err == nil {
		t.Fatal("o append com fsync falhado devia devolver erro")
	}
	if err := s.Close(); err != nil && !bytes.Contains([]byte(err.Error()), []byte("sonda")) {
		t.Logf("close (esperado falhar pela sonda): %v", err)
	}

	// A INVARIANTE: o chamador viu erro, logo o ficheiro NÃO pode conter o registo.
	evs := registosNoFicheiro(t, path)
	if len(evs) != 1 {
		t.Fatalf("o ficheiro tem %d registo(s); o append falhado NÃO podia ficar durável", len(evs))
	}
	if evs[0].StepID != "s1" {
		t.Fatalf("o registo que sobrou não é o confirmado: step_id=%q", evs[0].StepID)
	}
}

// TestWAL_FsyncFalhado_ReabrirNaoRessuscita fecha a segunda metade: sem a reposição, os
// bytes ficavam na page cache e um simples REINÍCIO DE PROCESSO trazia o evento de volta.
func TestWAL_FsyncFalhado_ReabrirNaoRessuscita(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.wal")

	s, _ := abreComFsyncFalhado(t, path, 1, false)
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)
	if _, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":2}`), RunID: "run-A", StepID: "s2"}); err == nil {
		t.Fatal("esperava erro no append com fsync falhado")
	}
	_ = s.Close()

	s2, err := Open(path, WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("reabrir: %v", err)
	}
	defer s2.Close()
	got, err := s2.Read(ctx, "run-A", 1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("após reabrir há %d evento(s); o append falhado RESSUSCITOU", len(got))
	}
	// E o head tem de continuar em 1, para que o retry seguinte NÃO reuse um seq que já
	// está no ficheiro — era isso que produzia o WAL com seqs duplicados que o Open recusa.
	if h := got[len(got)-1].Seq; h != 1 {
		t.Fatalf("último seq = %d, quero 1", h)
	}
}

// TestWAL_FsyncFalhado_RetryNaoDuplicaSeq prova a consequência aguda: sem a reposição, o
// retry reusava o seq do órfão e o Open seguinte recusava o WAL inteiro.
func TestWAL_FsyncFalhado_RetryNaoDuplicaSeq(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.wal")

	s, ff := abreComFsyncFalhado(t, path, 1, false)
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)
	if _, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":2}`), RunID: "run-A", StepID: "s2"}); err == nil {
		t.Fatal("esperava erro")
	}
	// O operador/chamador re-tenta; o disco recuperou.
	ff.falharApós = 1 << 30
	appendEv(t, s, "run-A", "s2b", "t", `{"n":2}`)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// O WAL tem de reabrir. Antes da correcção ficava com seqs [1 2 2] e o Open recusava
	// com E_RESTORE_ORDER (lote não-gapless) — o nó não arrancava.
	s2, err := Open(path, WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("o WAL ficou irreabrível após um fsync falhado + retry: %v", err)
	}
	defer s2.Close()
	got, _ := s2.Read(ctx, "run-A", 1)
	if len(got) != 2 {
		t.Fatalf("após retry há %d evento(s), quero 2", len(got))
	}
	vistos := map[uint64]bool{}
	for _, e := range got {
		if vistos[e.Seq] {
			t.Fatalf("seq %d duplicado no WAL", e.Seq)
		}
		vistos[e.Seq] = true
	}
}

// TestWAL_ReposicaoImpossivel_EnvenenaEmVezDeContinuar cobre o caso em que nem truncar se
// consegue. Continuar a aceitar escritas construiria o WAL com seq duplicado que o Open
// recusa; recusar em voz alta é o mal menor, e é o que tem de acontecer.
func TestWAL_ReposicaoImpossivel_EnvenenaEmVezDeContinuar(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.wal")

	s, ff := abreComFsyncFalhado(t, path, 1, true /* truncate também falha */)
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)
	_, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":2}`), RunID: "run-A", StepID: "s2"})
	if err == nil {
		t.Fatal("esperava erro")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("nao aceita mais escritas")) {
		t.Fatalf("o erro devia declarar o envenenamento: %v", err)
	}
	// Mesmo com o disco a recuperar, o WAL não volta a aceitar: a invariante não é
	// reponível e continuar produziria o seq duplicado.
	ff.falharApós = 1 << 30
	if _, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":3}`), RunID: "run-A", StepID: "s3"}); err == nil {
		t.Fatal("o WAL envenenado NÃO podia aceitar escritas novas")
	}
	_ = s.Close()
	_ = os.Remove(path)
}

// escritorQueEscreveTudoEFalha devolve (len(p), erro): escreveu TUDO no ficheiro real e
// ainda assim reporta falha. É o comportamento que o `bufio.Writer` propaga sem
// acrescentar io.ErrShortWrite — e por isso um `Flush` falhado também pode deixar o
// registo INTEIRO em disco, tal como o `Sync`.
type escritorQueEscreveTudoEFalha struct {
	real    ficheiroWAL
	writes  int
	falharA int
}

func (f *escritorQueEscreveTudoEFalha) Sync() error  { return f.real.Sync() }
func (f *escritorQueEscreveTudoEFalha) Close() error { return f.real.Close() }

func (f *escritorQueEscreveTudoEFalha) Write(p []byte) (int, error) {
	n, err := f.real.Write(p)
	if err != nil {
		return n, err
	}
	f.writes++
	if f.writes >= f.falharA {
		return n, errors.New("sonda: write escreveu tudo e falhou (EIO)")
	}
	return n, nil
}

// TestWAL_FlushFalhadoDepoisDeEscreverTudo_NaoDeixaRegistoNoFicheiro fecha a metade da
// afirmação que o comentário de `wal.append` faz sobre o `Flush`. Sem este teste, essa
// afirmação seria só prosa.
func TestWAL_FlushFalhadoDepoisDeEscreverTudo_NaoDeixaRegistoNoFicheiro(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.wal")

	s, err := Open(path, WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)

	// O bufio faz UM Write por Flush (o buffer inteiro de uma vez): falhar no 1º write
	// depois deste ponto é falhar o Flush do 2º registo, com os bytes já escritos.
	// O bufio.Writer capturou o ficheiro na CONSTRUCAO — trocar so o s.wal.f deixaria as
	// escritas a passar ao lado da sonda (e o teste a passar por engano). O buffer esta
	// vazio aqui: o append anterior fez Flush.
	sonda := &escritorQueEscreveTudoEFalha{real: s.wal.f, falharA: 1}
	s.wal.trocarFicheiro(sonda)
	if _, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":2}`), RunID: "run-A", StepID: "s2"}); err == nil {
		t.Fatal("esperava erro do Flush")
	}
	_ = s.Close()

	evs := registosNoFicheiro(t, path)
	if len(evs) != 1 {
		t.Fatalf("o ficheiro tem %d registo(s); um Flush que escreveu tudo e falhou NAO podia ficar duravel", len(evs))
	}
	if evs[0].StepID != "s1" {
		t.Fatalf("sobrou o registo errado: %q", evs[0].StepID)
	}
}
