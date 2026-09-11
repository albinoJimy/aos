package main

import (
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// aos392_despacho_multiproc_test.go — a prova operacional do DESPACHO GOVERNADO sob N
// processos (AOS-392), estendendo a corrida de AOS-100 ao efeito.
//
// # O que AOS-100 prova, e o que falta
//
// TestAOS100_NServeEmParaleloSobreOSubstratoReplicado corre N processos sobre o mesmo run
// e mede a ARBITRAGEM da posse (vencedores=1, os outros negados-pelo-lease) — mas com
// `--nodes` (nós dados à mão), sem despacho. Com o despacho governado composto (AOS-390,
// ADR-024), falta provar a propriedade que interessa ao distribuído: o efeito por-nó
// (goal→DAG→spawn/arranque) atravessa a fronteira do processo SEM duplo-despacho, porque
// só o DONO do lease o produz. É o modelo per-run do ADR-023 a segurar o despacho.
//
// # CLUSTER-GATED (como AOS-100)
//
// A corrida `--nats` exige o substrato REPLICADO do cluster (JetStream com réplicas). Sem
// AOS_NATS_URL o teste é SALTADO — não se substitui o cluster por um substrato falso, que
// mediria o falso. Corre no CI/servidor onde AOS_NATS_URL aponta para o nó 0.

// TestAOS392_DespachoMultiProcessoSobreSubstratoReplicado: N processos servem o MESMO run
// com `--goal` (decompõe → ADMITE → DESPACHA). Exactamente um — o dono do lease — despacha
// o run ponta-a-ponta; os outros saem negados-pelo-lease. Prova que o despacho composto de
// AOS-390 é seguro sob N réplicas: o efeito nasce UMA vez, no processo dono.
func TestAOS392_DespachoMultiProcessoSobreSubstratoReplicado(t *testing.T) {
	addr := clusterORQ(t) // SALTA sem AOS_NATS_URL — o substrato replicado é do cluster
	bin := construir(t)
	stream := streamProprio(t, "ORQ392_")
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, snapshotDuasTools)
	fixPath := filepath.Join(dir, "plano.json")
	escrever(t, fixPath, planoFixtureDuasFolhas)
	const run = "run-despacho-392"
	const n = 3

	var wg sync.WaitGroup
	res := make([]resultado, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res[i] = correr(t, bin, "serve",
				"--nats", addr, "--nats-stream", stream, "--nats-replicas", "3",
				"--run", run, "--goal", "recolher e analisar dados", "--snapshot", snapPath,
				"--decompose-fixture", fixPath, "--worker", "p"+strconv.Itoa(i))
		}(i)
	}
	wg.Wait()

	vencedores, negadosPeloLease, outros := 0, 0, 0
	var venceu resultado
	for _, r := range res {
		switch r.code {
		case exitOK:
			vencedores++
			venceu = r
		case exitPosseNegada:
			negadosPeloLease++
		default:
			outros++
			t.Logf("desfecho inesperado (%d):\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
		}
	}
	t.Logf("despacho entre %d processos sobre o substrato REPLICADO: vencedores=%d negados-pelo-lease=%d outros=%d",
		n, vencedores, negadosPeloLease, outros)

	if outros != 0 {
		t.Fatalf("%d processo(s) com desfecho inesperado — recusar tem de ser distinguível de avariar", outros)
	}
	if vencedores != 1 {
		t.Fatalf("vencedores = %d, quer exactamente 1 — o lease não está a arbitrar o DESPACHO do run, "+
			"e N réplicas a despachar o mesmo run produziriam efeito duplicado", vencedores)
	}
	if negadosPeloLease != n-1 {
		t.Fatalf("negados pelo LEASE = %d, quer %d — os perdedores saem com o código da posse do run (%d)",
			negadosPeloLease, n-1, exitPosseNegada)
	}
	// O DONO despachou PONTA-A-PONTA (goal→DAG→efeito), não só materializou: o efeito nasce
	// no despacho (ADR-024), e o laço imprime "despachado: ... nos_despachados=N".
	if !strings.Contains(venceu.stdout, "despachado:") {
		t.Fatalf("o processo dono materializou mas NÃO despachou o run ponta-a-ponta:\n%s", venceu.stdout)
	}
}
