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
