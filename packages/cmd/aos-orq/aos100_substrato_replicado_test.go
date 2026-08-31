package main

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// aos100_substrato_replicado_test.go — a topologia que este comando NÃO suportava.
//
// Sem cluster os testes são SALTADOS. Um substrato falso mediria o falso.
//
//	AOS_NATS_URL=127.0.0.1:14227 go test ./ -run AOS100 -v

const envClusterORQ = "AOS_NATS_URL"

func clusterORQ(t *testing.T) string {
	t.Helper()
	addr := os.Getenv(envClusterORQ)
	if addr == "" {
		t.Skipf("sem cluster: define %s (túnel SSH para o nó 0)", envClusterORQ)
	}
	return addr
}

func streamProprio(t *testing.T, prefixo string) string {
	t.Helper()
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("sufixo: %v", err)
	}
	return prefixo + hex.EncodeToString(b[:])
}

// TestAOS100_NServeEmParaleloSobreOSubstratoReplicado é o teste que fecha a metade do
// DEF-282 que pertence a este comando.
//
// # O que muda face a TestLimite_EventStoreDeReferenciaNaoArbitraEntreProcessos
//
// Aquele teste corre a MESMA corrida sobre `--wal` e mede o GUARD: exactamente um
// processo passa, e os restantes saem com [exitWALDetido] — recusados por um FICHEIRO,
// porque o substrato de referência não arbitra e a única defesa possível é impedir a
// configuração.
//
// Aqui a corrida é sobre `--nats`, e a diferença é a que importa: os N processos SÃO
// TODOS ADMITIDOS ao Event Store — nenhum é recusado por posse de ficheiro — e o
// vencedor é decidido pelo LEASE. Os perdedores saem com [exitPosseNegada], que diz ao
// operador para parar o outro dono do RUN, e não para parar o outro escritor do STORE.
//
// Se algum processo saísse com [exitWALDetido], o guard estaria a aplicar-se onde não
// deve. Se saísse mais do que um com [exitOK], o substrato não estaria a arbitrar — e
// então correr N réplicas seria PIOR do que uma, porque a maquinaria de posse daria a
// impressão de estar a arbitrar.
func TestAOS100_NServeEmParaleloSobreOSubstratoReplicado(t *testing.T) {
	addr := clusterORQ(t)
	bin := construir(t)
	stream := streamProprio(t, "ORQPAR_")
	const run = "run-paralelo"
	const n = 4

	var wg sync.WaitGroup
	res := make([]resultado, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res[i] = correr(t, bin, "serve",
				"--nats", addr, "--nats-stream", stream, "--nats-replicas", "3",
				"--run", run, "--nodes", "n"+strconv.Itoa(i), "--worker", "p"+strconv.Itoa(i))
		}(i)
	}
	wg.Wait()

	vencedores, negadosPeloLease, guardados, outros := 0, 0, 0, 0
	for _, r := range res {
		switch r.code {
		case exitOK:
			vencedores++
		case exitPosseNegada:
			negadosPeloLease++
		case exitWALDetido:
			guardados++
		default:
			outros++
			t.Logf("desfecho inesperado (%d):\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
		}
	}
	t.Logf("corrida entre %d processos sobre o substrato REPLICADO: vencedores=%d negados-pelo-lease=%d guardados(WAL)=%d outros=%d",
		n, vencedores, negadosPeloLease, guardados, outros)

	if guardados != 0 {
		t.Fatalf("%d processo(s) recusados pelo guard do WAL — com um substrato que arbitra, "+
			"recusar réplicas é recusar exactamente o que o AOS-100 entrega", guardados)
	}
	if outros != 0 {
		t.Fatalf("%d processo(s) com desfecho inesperado — recusar tem de ser distinguível de avariar", outros)
	}
	if vencedores != 1 {
		t.Fatalf("vencedores = %d, quer exactamente 1 — o substrato NÃO está a arbitrar a posse do run, "+
			"e correr N instâncias sobre ele não é seguro", vencedores)
	}
	if negadosPeloLease != n-1 {
		t.Fatalf("negados pelo LEASE = %d, quer %d — os perdedores têm de sair com o código da posse do RUN (%d), "+
			"que diz ao operador para parar o outro dono do run e não o outro escritor do store",
			negadosPeloLease, n-1, exitPosseNegada)
	}
}

// TestAOS100_PosseSequencialContinuaAFuncionarNoReplicado — o handoff (ADR-023 §2.5) é
// sobre posse SEQUENCIAL: um processo serve, ANUNCIA que larga, o seguinte reclama. Essa
// via não pode partir-se ao trocar de substrato, e a re-hidratação do grafo tem de
// atravessar a fronteira do processo pelo log — que agora é remoto.
func TestAOS100_PosseSequencialContinuaAFuncionarNoReplicado(t *testing.T) {
	addr := clusterORQ(t)
	bin := construir(t)
	stream := streamProprio(t, "ORQSEQ_")
	const run = "run-sequencial"

	primeiro := correr(t, bin, "serve", "--nats", addr, "--nats-stream", stream,
		"--run", run, "--nodes", "a,b", "--worker", "p1", "--release")
	if primeiro.code != exitOK {
		t.Fatalf("1.º processo saiu %d\nstdout:\n%s\nstderr:\n%s", primeiro.code, primeiro.stdout, primeiro.stderr)
	}

	segundo := correr(t, bin, "serve", "--nats", addr, "--nats-stream", stream,
		"--run", run, "--nodes", "c", "--worker", "p2", "--release")
	if segundo.code != exitOK {
		t.Fatalf("2.º processo saiu %d depois de o 1.º ANUNCIAR que largou — o handoff partiu-se\nstderr:\n%s",
			segundo.code, segundo.stderr)
	}

	// O 2.º tem de VER o que o 1.º escreveu: é o mesmo log, e é isso que a
	// re-hidratação do grafo atravessa.
	visto := correr(t, bin, "inspect", "--nats", addr, "--nats-stream", stream, "--run", run)
	if visto.code != exitOK {
		t.Fatalf("inspect saiu %d\nstderr:\n%s", visto.code, visto.stderr)
	}
	if !strings.Contains(visto.stdout, "nos=3") || !strings.Contains(visto.stdout, "ordem=a,b,c") {
		t.Fatalf("o inspect não vê os três nós escritos pelos DOIS processos — o log não atravessou "+
			"a fronteira do processo.\nstdout:\n%s", visto.stdout)
	}
}

// TestAOS100_SubstratoAmbiguoERecusado — aceitar --wal e --nats ao mesmo tempo daria um
// processo a anunciar coordenação distribuída enquanto trancava um ficheiro local. É o
// modo de falha mais caro possível, porque parece que funciona.
func TestAOS100_SubstratoAmbiguoERecusado(t *testing.T) {
	bin := construir(t)
	r := correr(t, bin, "serve", "--wal", t.TempDir()+"/x.wal", "--nats", "127.0.0.1:1", "--run", "r")
	if r.code == exitOK {
		t.Fatal("--wal e --nats em simultâneo foram ACEITES")
	}
	if !strings.Contains(r.stderr, "EXCLUSIVOS") {
		t.Fatalf("a recusa não explica que os substratos são exclusivos:\n%s", r.stderr)
	}
}

// TestAOS100_SemSubstratoERecusado — sem nenhum dos dois não há canal de coordenação, e
// o comando não tem nada que arrancar.
func TestAOS100_SemSubstratoERecusado(t *testing.T) {
	bin := construir(t)
	if r := correr(t, bin, "serve", "--run", "r"); r.code == exitOK {
		t.Fatal("`serve` sem --wal nem --nats foi aceite")
	}
}
