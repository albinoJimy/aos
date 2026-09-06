package eventstore

// Testes que a REVISÃO ADVERSARIAL de EPIC-24 obrigou a escrever. Cada um cobre um caminho
// que o perfil de cobertura mostrava a ZERO — e um caminho de recuperação sem cobertura é
// indistinguível de um caminho partido.

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRevisao_StatFalhadoNaoEnvenenaOWAL — achado 4.
//
// AOS-349 pôs um `os.Stat` antes de CADA append. Na primeira versão, qualquer erro dele
// chamava `w.recusar(...)` e envenenava o WAL para sempre: um `ESTALE` num volume em rede,
// ou um script de rotação a renomear o ficheiro por um instante, produziam o mesmo desfecho
// que um disco morto — o WAL recusava tudo, `Healthy()` ia a false e o nó saía de serviço
// até reinício.
//
// É exactamente a assimetria que AOS-348 fecha para o `Flush`/`Sync`, reintroduzida por
// esta guarda. O que o AC pede como terminal é o `w.tamanho` À FRENTE do ficheiro, não
// «não consegui medir».
func TestRevisao_StatFalhadoNaoEnvenenaOWAL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.wal")
	s, err := Open(path, WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)

	falhar := true
	real := s.wal.tamanhoReal
	s.wal.tamanhoReal = func() (int64, error) {
		if falhar {
			return 0, errors.New("sonda: stat falhou (ESTALE)")
		}
		return real()
	}

	_, errStat := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":2}`), RunID: "run-A", StepID: "s2"})
	if errStat == nil {
		t.Fatal("um Stat falhado tem de recusar ESTE append")
	}
	if errors.Is(errStat, ErrWALDesincronizado) {
		t.Fatalf("«não consegui medir» foi reportado como dessincronização: %v", errStat)
	}

	// A AVARIA PASSOU. O WAL tem de continuar utilizável — foi isto que a primeira versão
	// tornava impossível.
	falhar = false
	res, errRetry := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":2}`), RunID: "run-A", StepID: "s2b"})
	if errRetry != nil {
		t.Fatalf("o WAL não recuperou de um Stat transitório (%v) — um syscall falhado matou o "+
			"substrato tal como um disco morto o mataria", errRetry)
	}
	if res.Seq != 2 {
		t.Fatalf("seq = %d, quero 2", res.Seq)
	}
	if !s.Healthy() {
		t.Fatal("Healthy() ficou false depois de uma falha transitória de Stat")
	}
}

// TestRevisao_DesfazerRecusaFicheiroEncolhidoAMeioDoAppend — achado 9.
//
// O teste que dá nome a AOS-349 encolhe o ficheiro ANTES do append, pelo que a recusa vem
// da guarda de `appendBloqueado` e `desfazer` nunca corre. A guarda que o AC NOMEIA — «a
// reposição verifica o tamanho real antes de truncar» — tinha zero cobertura.
//
// Aqui a dessincronização é introduzida na JANELA REAL: entre a medição do append e a
// falha do `Sync`, que é o único caminho que a alcança em produção (uma corrida com quem
// truncou o ficheiro por baixo).
func TestRevisao_DesfazerRecusaFicheiroEncolhidoAMeioDoAppend(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.wal")

	s, ff := abreComFsyncFalhado(t, path, 1<<30, false)
	defer s.Close()
	for i := 1; i <= 4; i++ {
		appendEv(t, s, "run-A", "s"+string(rune('0'+i)), "t", `{"n":1}`)
	}

	// A medição do append vê o tamanho COERENTE (passa a guarda de entrada); o ficheiro
	// encolhe a seguir; o fsync falha e `desfazer` corre sobre um ficheiro mais curto do
	// que a memória. É a janela que só uma corrida produz.
	offs, _ := offsetsDeRegistos(t, path)
	coerente := s.wal.tamanhoReal
	encolher := func() {
		if err := os.Truncate(path, offs[2]); err != nil {
			t.Fatalf("truncate: %v", err)
		}
	}
	primeira := true
	s.wal.tamanhoReal = func() (int64, error) {
		if primeira {
			primeira = false
			n, err := coerente() // a guarda de entrada vê o ficheiro ainda intacto
			encolher()           // …e só depois ele encolhe
			return n, err
		}
		return coerente()
	}
	ff.falharApós = 0
	ff.syncs = 0

	_, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":9}`), RunID: "run-A", StepID: "s9"})
	if err == nil {
		t.Fatal("esperava erro do fsync")
	}
	if !errors.Is(err, ErrWALDesincronizado) {
		t.Fatalf("erro = %v, esperava ErrWALDesincronizado da guarda de `desfazer` — "+
			"sem ela, os.Truncate para um tamanho MAIOR estenderia o ficheiro com zeros", err)
	}

	// O QUE A GUARDA GARANTE: o ficheiro NÃO foi estendido com ZEROS até `w.tamanho`. Era
	// isso que enterrava os registos 3..5 num buraco de 606 bytes nulos e tornava o log
	// ilegível — o dano medido de AOS-349.
	fi, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("stat: %v", statErr)
	}
	if fi.Size() >= s.wal.tamanho {
		t.Fatalf("a «reposição» ESTENDEU o ficheiro até ao tamanho em memória: %d bytes "+
			"(memória=%d) — é a extensão com zeros que a guarda existe para impedir",
			fi.Size(), s.wal.tamanho)
	}

	// O WAL RECUSA a partir daqui: a invariante não é reponível e continuar construiria um
	// log que o Open seguinte não sabe ler.
	if s.Healthy() {
		t.Fatal("Healthy() == true com o WAL dessincronizado — o nó não sairia de serviço")
	}

	// RESIDUAL DECLARADO, e este teste mede-o em vez de o esconder. Nesta JANELA DE CORRIDA
	// — o ficheiro encolhe DEPOIS da medição de entrada e ANTES do fsync — os bytes do
	// append já passaram por write(2) em O_APPEND e estão no ficheiro. A guarda impede a
	// extensão; não desescreve o que já aterrou. Fechá-lo exigiria saber quanto do registo
	// chegou ao disco, que é precisamente o que um flush parcial torna indeterminado.
	//
	// O que contém o dano é o envenenamento acima (o nó dreana e o operador é mandado
	// reconciliar com a cópia de segurança), não a reposição. Fica AFIRMADO aqui para que
	// ninguém leia o veredicto deste teste como «o append falhado nunca fica no ficheiro».
	if fi.Size() <= offs[2] {
		t.Logf("nota: nesta corrida o append falhado não chegou ao ficheiro (%d bytes) — "+
			"o residual não se manifestou, mas continua declarado", fi.Size())
	}
}

// TestRevisao_RessincronizacaoAtravessaAFronteiraDaJanela — achado 8.
//
// `ressincroniza` varre em janelas de 64 KiB com uma sobreposição de 3 bytes. Todos os
// testes de AOS-346 usam WALs de ~1,5 KiB, pelo que a segunda iteração do laço — e com ela
// a aritmética da sobreposição — nunca era exercitada.
//
// Se essa aritmética estiver errada (um `base += n` em vez de `n-3`, um off-by-one),
// `contaOrfaos` devolve 0, o [Open] conclui «cauda rasgada» e TRUNCA os registos íntegros:
// a perda de dados que AOS-346 fecha, reintroduzida para eventos grandes. Este teste põe um
// evento de ~200 KiB antes da quebra, obrigando o varrimento a atravessar várias janelas.
func TestRevisao_RessincronizacaoAtravessaAFronteiraDaJanela(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")
	s := openDurable(t, path)

	// Um evento GRANDE (bem acima da janela de varrimento) seguido de três pequenos.
	grande := `{"marca":"GRANDE","enchimento":"` + strings.Repeat("x", 200<<10) + `"}`
	appendEv(t, s, "run-A", "s1", "t", grande)
	for i := 2; i <= 4; i++ {
		appendEv(t, s, "run-A", "s"+string(rune('0'+i)), "t", `{"marca":"EVENTO-`+string(rune('0'+i))+`"}`)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	antes := tamanhoDe(t, path)

	// Corrompe o CABEÇALHO do registo GRANDE: o varrimento tem de percorrer os ~200 KiB do
	// payload dele — várias janelas — antes de encontrar o registo 2.
	offs, b := offsetsDeRegistos(t, path)
	if len(offs) != 4 {
		t.Fatalf("esperava 4 registos, li %d", len(offs))
	}
	if offs[1] < janelaDeRessincronizacao {
		t.Fatalf("o registo grande tem %d bytes — não atravessa a janela de %d e o teste não mede nada",
			offs[1], janelaDeRessincronizacao)
	}
	atual := binary.BigEndian.Uint32(b[offs[0] : offs[0]+4])
	corrompeCabecalho(t, path, 1, atual+223)

	s2, err := Open(path, WithReplicas(1), WithQuorum(1))
	if err == nil {
		_ = s2.Close()
		t.Fatalf("Open ACEITOU um cabeçalho corrompido antes de %d bytes de payload — a "+
			"ressincronização não atravessou a fronteira da janela e os registos 2..4 seriam apagados",
			offs[1])
	}
	if !errors.Is(err, ErrWALCorruptedMidLog) {
		t.Fatalf("erro = %v, esperava ErrWALCorruptedMidLog", err)
	}
	if got := tamanhoDe(t, path); got != antes {
		t.Fatalf("o ficheiro foi alterado pela recusa: %d -> %d bytes", antes, got)
	}
}

func tamanhoDe(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return fi.Size()
}
