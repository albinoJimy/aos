package audit

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// aos284_atribuicao_test.go — AC5 do AOS-284: a atribuição réplica→partição é DETERMINÍSTICA
// e RECONSTRUÍVEL. Prova a metade que faltava: não só quem verifica sabe que partições
// existiam (Partitions()), mas também QUEM as detinha, por uma função pura.

// TestAOS284_AC5_AtribuicaoEDeterministicaEIndependenteDaOrdem — a mesma partição, o mesmo
// conjunto de réplicas, dá sempre o mesmo dono, seja qual for a ORDEM da lista. É o que
// permite a um verificador reconstruir a posse sem consultar estado nenhum.
func TestAOS284_AC5_AtribuicaoEDeterministicaEIndependenteDaOrdem(t *testing.T) {
	replicas := []string{"orq-a", "orq-b", "orq-c"}
	baralhada := []string{"orq-c", "orq-a", "orq-b"}

	for _, p := range []string{"gov.read/run-1", "gov.write/run-2", "sys.audit/run-3", "x"} {
		dono1, ok1 := AtribuirParticao(p, replicas)
		dono2, ok2 := AtribuirParticao(p, baralhada)
		if !ok1 || !ok2 {
			t.Fatalf("%q: devia ter dono; ok1=%v ok2=%v", p, ok1, ok2)
		}
		if dono1 != dono2 {
			t.Fatalf("%q: atribuição depende da ordem da lista (%q vs %q)", p, dono1, dono2)
		}
		// E o dono é sempre um dos candidatos, nunca inventado.
		if dono1 != "orq-a" && dono1 != "orq-b" && dono1 != "orq-c" {
			t.Fatalf("%q: dono %q não está no conjunto", p, dono1)
		}
	}
}

// TestAOS284_AC5_TodaParticaoTemExactamenteUmDono — cada partição vai para uma e uma só
// réplica, e o conjunto de donos cobre (com partições suficientes) todas as réplicas: é uma
// partição do espaço, não um mapeamento degenerado para uma só réplica.
func TestAOS284_AC5_ReparticaoCobreTodasAsReplicas(t *testing.T) {
	replicas := []string{"orq-a", "orq-b", "orq-c"}
	contagem := map[string]int{}
	const n = 300
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("gov.read/run-%d", i)
		dono, ok := AtribuirParticao(p, replicas)
		if !ok {
			t.Fatalf("%q sem dono", p)
		}
		contagem[dono]++
	}
	for _, r := range replicas {
		if contagem[r] == 0 {
			t.Fatalf("réplica %q não recebeu nenhuma das %d partições — repartição degenerada: %v", r, n, contagem)
		}
	}
}

// TestAOS284_AC5_ConjuntoVazioNaoTemDono — sem candidatos não há dono (fail-closed a
// montante da porta). É o que garante que uma má configuração recusa em vez de atribuir a
// ninguém em silêncio.
func TestAOS284_AC5_ConjuntoVazioNaoTemDono(t *testing.T) {
	if dono, ok := AtribuirParticao("qualquer", nil); ok {
		t.Fatalf("conjunto vazio não devia ter dono; veio %q", dono)
	}
	if dono, ok := AtribuirParticao("qualquer", []string{"", ""}); ok {
		t.Fatalf("só nomes vazios não é conjunto; veio %q", dono)
	}
}

// TestAOS284_AC5_RemapeamentoEMinimoAoRemoverReplica — a propriedade que justifica HRW sobre
// módulo: quando uma réplica sai, SÓ as suas partições mudam de dono; as outras ficam onde
// estavam. Um esquema por módulo remapearia quase tudo.
func TestAOS284_AC5_RemapeamentoEMinimoAoRemoverReplica(t *testing.T) {
	antes := []string{"orq-a", "orq-b", "orq-c"}
	depois := []string{"orq-a", "orq-b"} // orq-c saiu

	const n = 300
	var mexeramSemSerDoOrqC, doOrqC, estaveis int
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("run-%d", i)
		d1, _ := AtribuirParticao(p, antes)
		d2, _ := AtribuirParticao(p, depois)
		switch {
		case d1 == "orq-c":
			doOrqC++
			// tem de ser reatribuída — não pode continuar em orq-c, que saiu
			if d2 == "orq-c" {
				t.Fatalf("%q continua atribuída a uma réplica que saiu", p)
			}
		case d1 != d2:
			mexeramSemSerDoOrqC++
		default:
			estaveis++
		}
	}
	// A garantia dura: NENHUMA partição que não fosse do orq-c mudou de dono.
	if mexeramSemSerDoOrqC != 0 {
		t.Fatalf("%d partições que não eram do orq-c mudaram de dono — remapeamento não é mínimo", mexeramSemSerDoOrqC)
	}
	if doOrqC == 0 || estaveis == 0 {
		t.Fatalf("cenário vazio: doOrqC=%d estaveis=%d", doOrqC, estaveis)
	}
}

// TestAOS284_AC5_MapaDeAtribuicaoReconstroiAPosse — o verificador enumera as partições e, com
// o conjunto de réplicas, reconstrói quem detinha cada uma. É o AC5 tornado observável de
// fora, sem lease store.
func TestAOS284_AC5_MapaDeAtribuicaoReconstroiAPosse(t *testing.T) {
	particoes := []string{"gov.read/run-1", "gov.write/run-2", "sys.audit/run-3"}
	replicas := []string{"orq-a", "orq-b"}

	mapa := MapaDeAtribuicao(particoes, replicas)
	if len(mapa) != len(particoes) {
		t.Fatalf("o mapa devia cobrir todas as partições; tem %d de %d", len(mapa), len(particoes))
	}
	for _, p := range particoes {
		dono, existe := mapa[p]
		if !existe {
			t.Fatalf("partição %q sem entrada no mapa", p)
		}
		// coincide com a decisão da porta de posse da réplica dona.
		porta := AtribuicaoDeterministica{EstaReplica: dono, Replicas: replicas}
		detem, err := porta.Detem(context.Background(), p)
		if err != nil || !detem {
			t.Fatalf("o dono %q reconstruído não se reconhece dono de %q (detem=%v err=%v)", dono, p, detem, err)
		}
	}
	// Conjunto vazio ⇒ nenhuma partição tem dono (ausência, não entrada para "").
	if vazio := MapaDeAtribuicao(particoes, nil); len(vazio) != 0 {
		t.Fatalf("sem réplicas o mapa devia ser vazio; tem %v", vazio)
	}
}

// TestAOS284_AC5_PortaAtribuicaoImpedeOForkEntreReplicas — o teste que fecha o ciclo do AC5
// com o FileStore: DUAS réplicas com a MESMA atribuição determinística sobre o mesmo WAL só
// deixam escrever a que a função atribui como dona. Sem convenção externa: a posse deriva da
// função, e as duas réplicas chegam à mesma resposta.
func TestAOS284_AC5_PortaAtribuicaoImpedeOForkEntreReplicas(t *testing.T) {
	ctx := context.Background()
	caminho := filepath.Join(t.TempDir(), "worm.wal")
	const particao = "gov.read/run-atribuida"
	replicas := []string{"orq-a", "orq-b"}

	dono, ok := AtribuirParticao(particao, replicas)
	if !ok {
		t.Fatalf("a partição de teste devia ter dono")
	}
	outra := "orq-a"
	if dono == outra {
		outra = "orq-b"
	}

	// A réplica dona escreve; a outra abre o MESMO ficheiro mas é recusada, sem convenção
	// externa a dizer-lhe que não é dona — a função de atribuição di-lo.
	replicaDona, err := OpenFileStore(caminho, ComPosseDeParticao(AtribuicaoDeterministica{EstaReplica: dono, Replicas: replicas}))
	if err != nil {
		t.Fatalf("abrir dona: %v", err)
	}
	replicaOutra, err := OpenFileStore(caminho, ComPosseDeParticao(AtribuicaoDeterministica{EstaReplica: outra, Replicas: replicas}))
	if err != nil {
		t.Fatalf("abrir outra: %v", err)
	}

	if _, err := replicaDona.Append(ctx, registoDeTeste(particao, "pela-dona")); err != nil {
		t.Fatalf("a réplica atribuída como dona devia escrever: %v", err)
	}
	if _, err := replicaOutra.Append(ctx, registoDeTeste(particao, "pela-outra")); !errors.Is(err, ErrParticaoAlheia) {
		t.Fatalf("a réplica não-dona devia ser RECUSADA; veio %v", err)
	}
	_ = replicaDona.Close()
	_ = replicaOutra.Close()

	// E o WAL reabre sem bifurcação — exactamente um escritor lá pôs registos.
	depois, err := OpenFileStore(caminho)
	if err != nil {
		t.Fatalf("o WAL tinha de reabrir sem fork: %v", err)
	}
	defer func() { _ = depois.Close() }()
	if head, err := depois.Head(ctx, particao); err != nil || head != 1 {
		t.Fatalf("a partição devia ter só o registo da dona; head=%d err=%v", head, err)
	}
}

// TestAOS284_AC5_DetemFailClosedSemReplicas — a porta de atribuição sem conjunto de réplicas
// recusa com causa (fail-closed), coerente com a porta de posse base.
func TestAOS284_AC5_DetemFailClosedSemReplicas(t *testing.T) {
	porta := AtribuicaoDeterministica{EstaReplica: "orq-a", Replicas: nil}
	if _, err := porta.Detem(context.Background(), "qualquer"); !errors.Is(err, ErrAtribuicaoSemReplicas) {
		t.Fatalf("sem réplicas Detem devia falhar fail-closed; veio %v", err)
	}
	// Uma réplica que não conste do conjunto não é dona de nada — desfecho determinístico,
	// não incerteza: (false, nil).
	porta2 := AtribuicaoDeterministica{EstaReplica: "orq-forasteira", Replicas: []string{"orq-a", "orq-b"}}
	detem, err := porta2.Detem(context.Background(), "qualquer")
	if err != nil || detem {
		t.Fatalf("réplica fora do conjunto não detém nada sem erro; detem=%v err=%v", detem, err)
	}
}

// TestAOS284_AC5_ReplicasCanonicas — a canonização remove vazios e duplicados e ordena, para
// que dois observadores registem e comparem o mesmo conjunto.
func TestAOS284_AC5_ReplicasCanonicas(t *testing.T) {
	got := ReplicasCanonicas([]string{"orq-b", "", "orq-a", "orq-b", "orq-a", ""})
	if len(got) != 2 || got[0] != "orq-a" || got[1] != "orq-b" {
		t.Fatalf("canonização errada: %v", got)
	}
	// A atribuição sobre o conjunto canónico coincide com a do original (a ordem/duplicados
	// não contam).
	orig := []string{"orq-b", "orq-a", "orq-b"}
	for i := 0; i < 50; i++ {
		p := fmt.Sprintf("run-%d", i)
		d1, _ := AtribuirParticao(p, orig)
		d2, _ := AtribuirParticao(p, got)
		if d1 != d2 {
			t.Fatalf("%q: canonização mudou a atribuição (%q vs %q)", p, d1, d2)
		}
	}
}
