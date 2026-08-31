package main

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// aos285_guard_arranque_test.go — O GUARD DE ARRANQUE DO NÓ (AOS-285).
//
// A prova de que a arbitragem é ENTRE PROCESSOS vive em
// `substrate/eventstore/wallock_test.go`, que corre um subprocesso real — é lá que o
// mecanismo é exercido. Aqui prova-se o que é do NÓ: que ele pede a posse, que recusa
// arrancar sem ela, que a larga, e que o guard sabe a quem se aplica.

// ---------------------------------------------------------------------------
// TESTE — o SEGUNDO nó sobre o mesmo Event Store RECUSA arrancar.
//
// Falha-antes: sem o guard, o segundo `New` abre o mesmo WAL e o processo passa a ter
// duas réplicas a escrever o mesmo log — com a maquinaria de posse a DAR A IMPRESSÃO
// de arbitrar, porque o Event Store não arbitra entre escritores (DEF-282).
// ---------------------------------------------------------------------------

func TestAOS285_SegundoNoSobreOMesmoEventStoreRecusa(t *testing.T) {
	wal := filepath.Join(t.TempDir(), "events.wal")

	// A posse é tomada por «outro processo» — modelada aqui pelo próprio mecanismo, que
	// é o que o outro processo usaria. O que está sob prova é a REACÇÃO do nó.
	largar, err := eventstore.LockWAL(wal)
	if err != nil {
		t.Fatalf("posse inicial: %v", err)
	}
	defer func() { _ = largar() }()

	_, err = tomarPosseDoWAL(Config{EventStorePath: wal})
	if err == nil {
		t.Fatal("o nó tomou posse de um Event Store JÁ DETIDO — o guard de AOS-285 não está a arbitrar")
	}
	if !errors.Is(err, ErrEventStoreJaDetido) {
		t.Fatalf("erro = %v, quer ErrEventStoreJaDetido — a recusa tem de ser distinguível de uma avaria de I/O, porque a acção do operador é outra", err)
	}
	// A mensagem tem de dizer ao operador o que fazer e porquê — um erro que só diz
	// «detido» manda-o procurar a razão no código.
	msg := err.Error()
	// O caminho e comparado pelo BASENAME: o `%q` da mensagem escapa as barras em
	// Windows, pelo que comparar o caminho inteiro seria uma asserção sobre a forma de
	// citação e não sobre o conteúdo — frágil, e por uma razão que nada tem a ver com o
	// que está sob prova.
	for _, exigido := range []string{filepath.Base(wal), "DEF-282", "Pare a outra réplica"} {
		if !contemTexto(msg, exigido) {
			t.Errorf("a recusa não menciona %q — mensagem: %s", exigido, msg)
		}
	}
}

// ---------------------------------------------------------------------------
// TESTE — largar devolve a posse: um nó que encerra não bloqueia o seguinte.
//
// É o caminho do reinício normal (deploy, restart do supervisor). Se falhasse, o guard
// transformaria cada reinício num incidente.
// ---------------------------------------------------------------------------

func TestAOS285_LargarPermiteOArranqueSeguinte(t *testing.T) {
	wal := filepath.Join(t.TempDir(), "events.wal")

	primeira, err := tomarPosseDoWAL(Config{EventStorePath: wal})
	if err != nil {
		t.Fatalf("primeira posse: %v", err)
	}
	if primeira == nil {
		t.Fatal("o guard não se aplicou a um EventStorePath definido — devia aplicar-se")
	}
	if err := primeira.Largar(); err != nil {
		t.Fatalf("largar: %v", err)
	}

	segunda, err := tomarPosseDoWAL(Config{EventStorePath: wal})
	if err != nil {
		t.Fatalf("posse após largar = %v — um reinício normal do nó ficaria bloqueado", err)
	}
	if err := segunda.Largar(); err != nil {
		t.Fatalf("largar a segunda: %v", err)
	}
	// Idempotente: o `Close` do nó pode correr depois da guarda de limpeza do bootstrap.
	if err := segunda.Largar(); err != nil {
		t.Fatalf("largar duas vezes devia ser no-op, deu %v", err)
	}
}

// ---------------------------------------------------------------------------
// TESTE — A CONDIÇÃO DE APLICABILIDADE (o AC mais fácil de esquecer).
//
// O guard tem de saber a quem se aplica. Um guard que trancasse um store in-memory
// seria ruído; um que trancasse um store FORNECIDO por config tomaria uma decisão sobre
// um recurso cujo ciclo de vida é do chamador.
//
// E — o que interessa para o futuro — quando o AOS-100 trouxer um Event Store
// genuinamente partilhado, correr N réplicas passa a ser o OBJECTIVO. Esta condição é o
// sítio nomeado onde essa revisão tem de acontecer, e este teste é o que a torna
// visível a quem lá chegar.
// ---------------------------------------------------------------------------

func TestAOS285_CondicaoDeAplicabilidade(t *testing.T) {
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()

	casos := []struct {
		nome   string
		cfg    Config
		aplica bool
		porque string
	}{
		{
			nome:   "in-memory (sem path)",
			cfg:    Config{},
			aplica: false,
			porque: "não há ficheiro partilhável, logo não há configuração insegura possível",
		},
		{
			nome:   "store FORNECIDO por config",
			cfg:    Config{EventStore: store, EventStorePath: "/qualquer/caminho.wal"},
			aplica: false,
			porque: "o ciclo de vida é do chamador; trancar o que não abrimos seria decidir por ele",
		},
		{
			nome:   "durável em ficheiro (a topologia de escritor único)",
			cfg:    Config{EventStorePath: filepath.Join(t.TempDir(), "e.wal")},
			aplica: true,
			porque: "é o caso em que duas réplicas partilhariam um WAL que não arbitra",
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			_, aplica := guardDePosseAplicavel(c.cfg)
			if aplica != c.aplica {
				t.Fatalf("aplica = %v, quer %v — %s", aplica, c.aplica, c.porque)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TESTE — um nó sem guard larga sem estoirar (nil-safe).
//
// O `Node.Close` chama `posseWAL.Largar()` sempre; num nó in-memory o campo é nil.
// ---------------------------------------------------------------------------

func TestAOS285_LargarENilSafe(t *testing.T) {
	var nenhuma *posseDoWAL
	if err := nenhuma.Largar(); err != nil {
		t.Fatalf("largar uma posse inexistente devia ser no-op, deu %v", err)
	}
	semGuard, err := tomarPosseDoWAL(Config{})
	if err != nil {
		t.Fatalf("tomarPosseDoWAL sem path: %v", err)
	}
	if semGuard != nil {
		t.Fatal("o guard aplicou-se a um store in-memory")
	}
	if err := semGuard.Largar(); err != nil {
		t.Fatalf("largar: %v", err)
	}
}

// contemTexto é um `strings.Contains` local, para o ficheiro não arrastar o import só
// por causa de uma asserção.
func contemTexto(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// TESTE — O WORM TAMBÉM PEDE POSSE (AOS-284 AC1 → correcção ao guard).
//
// # Porque este teste existe, e o que foi MEDIDO
//
// O AC1 do AOS-284 pedia para descobrir se duas réplicas podem escrever a mesma
// partição da hash-chain. Medido a 2026-08-31, com dois `audit.OpenFileStore` sobre o
// mesmo ficheiro e um `Append` de cada na mesma partição:
//
//	A: seq=1   B: seq=1        → FORK
//	reabrir:   RECUSADO — «adulteracao insertion na particao "gov", audit_seq=1»
//
// A consequência não é degradação: o nó **deixa de arrancar**, e o diagnóstico diz
// ADULTERAÇÃO — indistinguível de um ataque para quem lê o erro.
//
// O caso COMUM já estava coberto por consequência: com Event Store e WORM ambos
// partilhados, o guard do Event Store recusa antes de o WORM ser aberto. O que ficava
// a descoberto — e é o que este teste fixa — é a configuração ASSIMÉTRICA: **mesmo
// WORM, Event Stores diferentes**. Nenhum dos dois guardas a via.
// ---------------------------------------------------------------------------

func TestAOS285_WORMTambemPedePosse(t *testing.T) {
	dir := t.TempDir()
	worm := filepath.Join(dir, "worm.wal")

	// Réplica A: Event Store próprio + WORM partilhado.
	a, err := tomarPosseDoWAL(Config{
		EventStorePath: filepath.Join(dir, "es-a.wal"),
		WORMPath:       worm,
	})
	if err != nil {
		t.Fatalf("posse de A: %v", err)
	}
	defer func() { _ = a.Largar() }()

	// Réplica B: Event Store DIFERENTE (logo o guard do ES não a apanha) e o MESMO WORM.
	_, err = tomarPosseDoWAL(Config{
		EventStorePath: filepath.Join(dir, "es-b.wal"),
		WORMPath:       worm,
	})
	if err == nil {
		t.Fatal("B arrancou com o MESMO WORM de A — é a configuração assimétrica que forka a hash-chain e faz o arranque seguinte recusar a cadeia como ADULTERADA")
	}
	if !errors.Is(err, ErrEventStoreJaDetido) {
		t.Fatalf("erro = %v, quer ErrEventStoreJaDetido", err)
	}
	if !contemTexto(err.Error(), "WORM") || !contemTexto(err.Error(), "AOS-284") {
		t.Errorf("a recusa não diz QUAL o ficheiro em conflito nem porquê — o operador tem dois caminhos para investigar: %s", err)
	}
}

// ---------------------------------------------------------------------------
// TESTE — um arranque RECUSADO não deixa ficheiros detidos.
//
// A posse é tomada em série (Event Store, depois WORM). Se a segunda falhar, a
// primeira TEM de ser largada — senão a tentativa seguinte, a do supervisor a
// reiniciar, seria recusada por um processo que nunca chegou a existir.
// ---------------------------------------------------------------------------

func TestAOS285_ArranqueRecusadoNaoDeixaPosseOrfa(t *testing.T) {
	dir := t.TempDir()
	worm := filepath.Join(dir, "worm.wal")
	esB := filepath.Join(dir, "es-b.wal")

	// Alguém detém o WORM.
	largarWorm, err := eventstore.LockWAL(worm)
	if err != nil {
		t.Fatalf("posse do WORM: %v", err)
	}

	// B tenta: obtém o Event Store, falha no WORM. O ES tem de ficar livre.
	if _, err := tomarPosseDoWAL(Config{EventStorePath: esB, WORMPath: worm}); err == nil {
		t.Fatal("B arrancou com o WORM detido")
	}
	if err := largarWorm(); err != nil {
		t.Fatalf("largar o WORM: %v", err)
	}

	// Agora tudo livre: B arranca. Se o ES tivesse ficado órfão, isto falhava.
	p, err := tomarPosseDoWAL(Config{EventStorePath: esB, WORMPath: worm})
	if err != nil {
		t.Fatalf("arranque após o conflito resolvido = %v — o Event Store ficou DETIDO por um arranque que abortou", err)
	}
	if err := p.Largar(); err != nil {
		t.Fatalf("largar: %v", err)
	}
}
