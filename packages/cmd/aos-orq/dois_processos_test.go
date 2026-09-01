package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/substrate/eventstore"
)

// dois_processos_test.go — A DEMONSTRAÇÃO COM PROCESSOS REAIS (AOS-281, DoD).
//
// # Porque isto prova algo que um teste in-process não pode provar
//
// Os testes de `control-plane/runlifecycle` correm duas RÉPLICAS LÓGICAS (dois
// LeaseManager, duas Tenure) sobre um Event Store partilhado — e são a prova da
// mecânica. O que eles NÃO podem excluir é a hipótese de a coordenação estar, sem
// querer, a passar por memória partilhada: o store é o mesmo objecto Go.
//
// Aqui não há objecto partilhado. Há dois PROCESSOS DO SISTEMA OPERATIVO, cada um com
// o seu espaço de endereçamento, e um ÚNICO canal entre eles: o ficheiro do WAL do
// Event Store. É a fundação que o ticket nomeia — «coordenação através do Event Store
// replicado, nunca por memória partilhada» — testada pela única via que a falsifica.
//
// # O que fica FORA, e é honesto dizê-lo
//
// O Event Store de REFERÊNCIA é in-process (as réplicas de AOS-100 são cópias
// in-process do log) com durabilidade por WAL. Ele NÃO suporta dois processos a
// escrever CONCORRENTEMENTE no mesmo ficheiro — cada `Open` faz replay e passa a ter a
// sua própria cabeça. Um backend genuinamente multi-processo (NATS JetStream, o da
// tabela de stack) é infraestrutura, não deste módulo.
//
// Por isso o que estes testes exercem através do processo é a POSSE SEQUENCIAL: o
// handoff e a re-hidratação atravessam a fronteira do processo, que é AC2 e AC4. A
// CONTENÇÃO CONCORRENTE (AC1) fica coberta pelos testes in-process do `runlifecycle`,
// onde o store é partilhado de facto. Nenhum dos dois sozinho cobre tudo; declarados,
// cobrem.

// construir compila o binário uma vez para todos os testes deste ficheiro.
func construir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("toolchain `go` indisponível: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "aos-orq-test.exe")
	// -ldflags="-s -w": binário sem tabela de símbolos. Reduz o risco de o antivírus do
	// ambiente de desenvolvimento pôr o executável de teste em quarentena a meio da
	// execução, que é um falso-vermelho caro de diagnosticar.
	cmd := exec.Command("go", "build", "-ldflags=-s -w", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build do aos-orq: %v\n%s", err, out)
	}
	return bin
}

// resultado é o desfecho de UM processo.
type resultado struct {
	code   int
	stdout string
	stderr string
}

// correr executa o binário como processo separado e devolve o desfecho.
func correr(t *testing.T, bin string, args ...string) resultado {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("execução de %v: %v", args, err)
		}
	}
	return resultado{code: code, stdout: out.String(), stderr: errb.String()}
}

func asExitError(err error, dst **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*dst = ee
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// TESTE 1 — HANDOFF ENTRE DOIS PROCESSOS REAIS, COM RE-HIDRATAÇÃO (AC2 + AC4).
//
// P1 toma posse, admite `a` e `b`, e ANUNCIA que larga. P1 morre.
// P2 arranca — espaço de endereçamento novo, zero memória em comum — toma posse,
// RE-HIDRATA o grafo do WAL e admite `c`.
//
// As asserções que valem:
//   - P2 vê os DOIS nós de P1 no grafo re-hidratado (a coordenação passou pelo log);
//   - o token de P2 é ESTRITAMENTE maior (a posse mudou de dono, monotonicamente);
//   - P2 não precisou de esperar o TTL (o handoff foi por anúncio, não por expiração).
// ---------------------------------------------------------------------------

func TestDoisProcessos_HandoffERehidratacaoPeloLog(t *testing.T) {
	bin := construir(t)
	wal := filepath.Join(t.TempDir(), "es.wal")
	const run = "run-p2p"

	p1 := correr(t, bin, "serve", "--wal", wal, "--run", run, "--nodes", "a,b", "--release", "--worker", "p1")
	if p1.code != exitOK {
		t.Fatalf("P1 saiu %d\nstdout:\n%s\nstderr:\n%s", p1.code, p1.stdout, p1.stderr)
	}
	if !strings.Contains(p1.stdout, "posse largada") {
		t.Fatalf("P1 não anunciou que largou a posse:\n%s", p1.stdout)
	}
	tokenP1 := tokenDe(t, p1.stdout)

	// P1 já não existe. O único vestígio dele é o ficheiro.
	if fi, err := os.Stat(wal); err != nil || fi.Size() == 0 {
		t.Fatalf("o WAL não ficou escrito (err=%v) — não há canal nenhum entre os processos", err)
	}

	p2 := correr(t, bin, "serve", "--wal", wal, "--run", run, "--nodes", "c", "--worker", "p2")
	if p2.code != exitOK {
		t.Fatalf("P2 saiu %d\nstdout:\n%s\nstderr:\n%s", p2.code, p2.stdout, p2.stderr)
	}

	// (a) A RE-HIDRATAÇÃO atravessou a fronteira do processo.
	if !strings.Contains(p2.stdout, "grafo re-hidratado: nos=2") {
		t.Fatalf("P2 não re-hidratou os 2 nós de P1 — começou VAZIO sobre um run com topologia durável:\n%s", p2.stdout)
	}
	// E não reescreveu o que já era durável.
	if strings.Count(p2.stdout, "no ja duravel") != 0 && !strings.Contains(p2.stdout, "no admitido: c") {
		t.Fatalf("P2 não admitiu o nó novo:\n%s", p2.stdout)
	}

	// (b) A POSSE mudou de dono, monotonicamente.
	tokenP2 := tokenDe(t, p2.stdout)
	if tokenP2 <= tokenP1 {
		t.Fatalf("token de P2 = %d, tem de ser estritamente maior que o de P1 = %d", tokenP2, tokenP1)
	}

	// (c) O log final sustenta o grafo completo — e um terceiro processo, que só LÊ,
	// vê os três nós. Se a coordenação dependesse de memória, este não veria nada.
	insp := correr(t, bin, "inspect", "--wal", wal, "--run", run)
	if insp.code != exitOK {
		t.Fatalf("inspect saiu %d\nstderr:\n%s", insp.code, insp.stderr)
	}
	if !strings.Contains(insp.stdout, "nos=3") {
		t.Fatalf("um terceiro processo, só de leitura, não vê os 3 nós:\n%s", insp.stdout)
	}
	if !strings.Contains(insp.stdout, "ordem=a,b,c") {
		t.Fatalf("a ordem topológica reconstruída não é a esperada:\n%s", insp.stdout)
	}
}

// ---------------------------------------------------------------------------
// TESTE 2 — SEM ANÚNCIO, A POSSE NÃO É RECLAMÁVEL (AC1 através do processo).
//
// P1 toma posse e NÃO larga (sem `--release`). P2 arranca e tem de ser RECUSADO com o
// código de posse negada — não é uma avaria, é a invariante: um lease vivo não se
// rouba.
//
// É o CONTRAPONTO do teste 1, e é ele que impede que o teste 1 passe por acidente: se
// a posse fosse concedida a toda a gente, o handoff «funcionaria» sempre.
// ---------------------------------------------------------------------------

func TestDoisProcessos_SemAnuncioAPosseNaoEReclamavel(t *testing.T) {
	bin := construir(t)
	wal := filepath.Join(t.TempDir(), "es.wal")
	const run = "run-sem-anuncio"

	p1 := correr(t, bin, "serve", "--wal", wal, "--run", run, "--nodes", "a", "--worker", "p1")
	if p1.code != exitOK {
		t.Fatalf("P1 saiu %d\nstderr:\n%s", p1.code, p1.stderr)
	}
	if strings.Contains(p1.stdout, "posse largada") {
		t.Fatalf("P1 largou a posse sem --release:\n%s", p1.stdout)
	}

	p2 := correr(t, bin, "serve", "--wal", wal, "--run", run, "--nodes", "b", "--worker", "p2")
	if p2.code != exitPosseNegada {
		t.Fatalf("P2 saiu %d, quer %d (posse negada) — um lease VIVO de outro processo foi roubado\nstdout:\n%s\nstderr:\n%s",
			p2.code, exitPosseNegada, p2.stdout, p2.stderr)
	}

	// E o nó de P2 NÃO entrou no log: recusar a posse tem de significar não escrever.
	insp := correr(t, bin, "inspect", "--wal", wal, "--run", run)
	if !strings.Contains(insp.stdout, "nos=1") {
		t.Fatalf("o processo a quem a posse foi NEGADA escreveu na mesma:\n%s", insp.stdout)
	}
}

// ---------------------------------------------------------------------------
// TESTE 3 — O LIMITE DO SUBSTRATO, MEDIDO E DECLARADO (AOS-281, AC1).
//
// # O que este teste PROVA, e porque é o teste mais importante deste ficheiro
//
// A arbitragem da posse depende INTEIRAMENTE de uma propriedade do Event Store: que a
// concorrência optimista (`expected_seq`) do stream `lease:<run_id>` seja atómica
// ENTRE ESCRITORES. O `LeaseManager` está correcto — relê, compara, escreve com
// `expected_seq` e retenta — mas correcção condicional a uma propriedade que o
// substrato tem de fornecer.
//
// O Event Store de REFERÊNCIA não a fornece entre PROCESSOS. As réplicas de AOS-100
// são cópias IN-PROCESS do log e o índice de dedup vive em memória; um `Open` faz
// replay do WAL no arranque e, a partir daí, cada processo tem a SUA cabeça. Dois
// processos que abram o mesmo ficheiro e reclamem o mesmo run recebem AMBOS o token 1
// — medido, deterministicamente, é o que este teste mostra.
//
// # Porque isto é um teste e não um comentário
//
// Um comentário envelhece em silêncio. Este teste falha no dia em que o substrato
// GANHAR a propriedade — e falhar nesse dia é o comportamento certo, porque nesse dia
// esta declaração passa a estar errada e tem de ser retirada do ADR-023 §4 e do
// registo de deferimentos. É um sensor da declaração, não uma bênção ao defeito.
//
// CONSEQUÊNCIA OPERACIONAL, dita em voz alta: correr dois `aos-orq` em paralelo sobre
// um WAL partilhado NÃO é seguro e não é a topologia deste componente. O deployment
// distribuído exige um Event Store genuinamente partilhado/replicado (NATS JetStream,
// da tabela de stack) em que o `expected_seq` seja atómico entre processos. O que os
// testes 1 e 2 provam — handoff por anúncio e re-hidratação a atravessarem a fronteira
// do processo — é INDEPENDENTE deste limite, porque aí os processos são SEQUENCIAIS e
// cada `Open` replaya o log completo.
// ---------------------------------------------------------------------------

func TestLimite_EventStoreDeReferenciaNaoArbitraEntreProcessos(t *testing.T) {
	bin := construir(t)
	wal := filepath.Join(t.TempDir(), "es.wal")
	const run = "run-limite"

	// Semeia o ficheiro para que a corrida seja pelo LEASE e não pela criação do WAL.
	if seed := correr(t, bin, "serve", "--wal", wal, "--run", run+"-seed", "--nodes", "s", "--release"); seed.code != exitOK {
		t.Fatalf("semeadura saiu %d\nstderr:\n%s", seed.code, seed.stderr)
	}

	// N processos EM SIMULTÂNEO, todos a reclamar o MESMO run.
	const n = 4
	var wg sync.WaitGroup
	res := make([]resultado, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res[i] = correr(t, bin, "serve", "--wal", wal, "--run", run,
				"--nodes", "n"+strconv.Itoa(i), "--worker", "p"+strconv.Itoa(i))
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
			t.Logf("processo com desfecho inesperado (%d):\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
		}
	}
	t.Logf("desfecho da corrida entre %d processos: vencedores=%d guardados(WAL)=%d negados(lease)=%d outros=%d",
		n, vencedores, guardados, negadosPeloLease, outros)

	// DESDE O AOS-286 esta corrida mede o GUARD, não o substrato: `serve` toma a posse
	// exclusiva do WAL antes de abrir o store, pelo que exactamente UM processo passa.
	//
	// # A asserção que foi RELAXADA, e porquê — não por conveniência
	//
	// Esta corrida exigia que TODOS os n-1 perdedores saíssem com [exitWALDetido]. Isso
	// era FALSO por uma razão de TEMPO que nada tem que ver com a propriedade medida: os
	// perdedores correm contra o TEMPO DE VIDA do vencedor, e um que chegue ao ficheiro
	// DEPOIS de ele sair apanha o lease vivo em vez do guard — saindo com
	// [exitPosseNegada]. As duas são recusas CORRECTAS; qual delas se apanha depende do
	// escalonador.
	//
	// MEDIDO a 2026-09-01: verde 8/8 localmente (incluindo sob carga paralela) e vermelho
	// na CI com `guardados=2 negados(lease)=1`. Era intermitência latente, não regressão —
	// o ficheiro não foi tocado desde AOS-286.
	//
	// O que a corrida passa a afirmar é o que uma corrida PODE afirmar: **um só escreve, e
	// todos os outros são recusados por uma causa NOMEADA**. A propriedade que a asserção
	// antiga queria — «o guard recusa um segundo escritor» — passou para
	// [TestGuardDoWAL_SegundoEscritorERecusadoDeterministicamente], onde é medida SEM
	// corrida nenhuma. Relaxar aqui sem a mover para lá teria sido perder cobertura.
	if vencedores != 1 {
		t.Fatalf("vencedores = %d, quer exactamente 1 — o guard de posse do WAL (AOS-285/286) não está a arbitrar entre estes processos", vencedores)
	}
	if recusados := guardados + negadosPeloLease; recusados != n-1 {
		t.Fatalf("recusados = %d (guard=%d lease=%d), quer %d — todo o perdedor tem de sair por uma causa nomeada",
			recusados, guardados, negadosPeloLease, n-1)
	}
	if outros != 0 {
		t.Fatalf("%d processo(s) com desfecho inesperado — recusar tem de ser distinguível de avariar", outros)
	}

	// A DECLARAÇÃO: com o Event Store de referência, a arbitragem entre processos NÃO
	// é garantida. Se um dia passar a ser — vencedores==1 de forma fiável —, este teste
	// falha e obriga a retirar a declaração do ADR-023 §4 e do registo de deferimentos.
	// A verificação directa (dois `Open`, dois claims, o mesmo token) é a prova
	// determinística; a corrida acima é a sua manifestação observável.
	if !doisOpensReclamamOMesmoToken(t) {
		t.Fatal("dois Open sobre o mesmo WAL JÁ NÃO concedem o mesmo token — o substrato ganhou arbitragem entre processos. " +
			"É uma BOA notícia e este teste é o sensor dela: retirar a declaração de ADR-023 §4 e o deferimento associado, " +
			"e converter este teste na asserção forte (exactamente 1 vencedor)")
	}
}

// doisOpensReclamamOMesmoToken mede a propriedade em falta SEM DEPENDER DE TIMING: dois
// `eventstore.Open` sobre o mesmo WAL, SEQUENCIALMENTE, e depois um claim de cada um
// sobre o mesmo run. Se o substrato arbitrasse entre escritores, o segundo claim veria
// o lease do primeiro e seria recusado ([durable.ErrLeaseHeld]); como cada `Open` tem a
// sua própria cabeça em memória, ambos mintam o token 1.
//
// É DELIBERADAMENTE sequencial e in-process: uma corrida entre processos mediria a
// mesma coisa mas com resultado dependente do escalonador, e um sensor intermitente é
// pior do que nenhum — daria «o substrato melhorou» num dia em que só o SO calhou
// serializar. Dois `Open` são exactamente o que dois processos têm; a concorrência não
// acrescenta nada à demonstração e tira-lhe o determinismo.
func doisOpensReclamamOMesmoToken(t *testing.T) bool {
	t.Helper()
	wal := filepath.Join(t.TempDir(), "sonda.wal")
	ctx := context.Background()

	s1, err := eventstore.Open(wal)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	defer s1.Close()
	s2, err := eventstore.Open(wal)
	if err != nil {
		t.Fatalf("Open #2: %v", err)
	}
	defer s2.Close()

	lm1, err := durable.NewLeaseManager(s1, leaseTTL)
	if err != nil {
		t.Fatalf("LeaseManager #1: %v", err)
	}
	lm2, err := durable.NewLeaseManager(s2, leaseTTL)
	if err != nil {
		t.Fatalf("LeaseManager #2: %v", err)
	}

	l1, err1 := lm1.Claim(ctx, "sonda")
	if err1 != nil {
		t.Fatalf("o primeiro claim falhou (%v) — a sonda não chegou a medir nada", err1)
	}
	l2, err2 := lm2.Claim(ctx, "sonda")

	t.Logf("sonda determinística: claim#1 token=%d err=%v · claim#2 token=%d err=%v",
		l1.Token.Value(), err1, l2.Token.Value(), err2)

	// A propriedade CONTINUA EM FALTA sse o segundo claim passou com o MESMO token.
	return err2 == nil && l2.Token.Value() == l1.Token.Value()
}

// tokenDe extrai o valor de `token=N` da saída de `serve`.
func tokenDe(t *testing.T, out string) uint64 {
	t.Helper()
	for _, campo := range strings.Fields(out) {
		if v, ok := strings.CutPrefix(campo, "token="); ok {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				t.Fatalf("token ilegível em %q: %v", campo, err)
			}
			return n
		}
	}
	t.Fatalf("nenhum `token=` na saída:\n%s", out)
	return 0
}

// ---------------------------------------------------------------------------
// TESTE 4 — O ORÁCULO DE EFEITO REAL, ATRAVÉS DE UM PROCESSO REAL (DEF-273).
//
// Um binário separado carrega o snapshot PINADO, materializa um documento aprovado
// com um nó `verifier`, e o resultado tem de mostrar o clamp: a tool READ-ONLY
// sobrevive, a de EFEITO não.
//
// # Porque isto vale mais do que o teste de unidade equivalente
//
// O DEF-273 não dizia «o clamp está errado» — dizia «não há chamador de produção». Um
// teste de unidade no `runlifecycle` prova o mecanismo mas não desfaz essa crítica: a
// via continuaria a ser chamada apenas por testes. Aqui quem chama é um BINÁRIO, com
// o snapshot vindo de um ficheiro e o documento de outro, exactamente como um
// operador o faria.
// ---------------------------------------------------------------------------

func TestDEF273_OraculoRealAtravesDeProcessoReal(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	wal := filepath.Join(dir, "es.wal")

	// Snapshot PINADO: `fs.read` é leitura local reversível (SEM efeito); `http.post`
	// fala para fora (COM efeito).
	snapPath := filepath.Join(dir, "snapshot.json")
	escrever(t, snapPath, `{
  "hash": "sha256:snap-teste",
  "tools": [
    {"name":"fs.read","version":"1.0.0","digest":"sha256:aaa","admissible":true,
     "sensitivity":"public","egress":"none","reversibility":"reversible"},
    {"name":"http.post","version":"2.0.0","digest":"sha256:bbb","admissible":true,
     "sensitivity":"public","egress":"external","reversibility":"reversible"}
  ]
}`)

	// Documento APROVADO com um verificador que declara AS DUAS tools, a de efeito
	// PRIMEIRO — o pior caso para `primaryTool`.
	docPath := filepath.Join(dir, "plano.json")
	escrever(t, docPath, planoComVerificador)

	r := correr(t, bin, "serve", "--wal", wal, "--run", "run-mat", "--plan", "plan-mat",
		"--plan-doc", docPath, "--snapshot", snapPath, "--worker", "p1")
	if r.code != exitOK {
		t.Fatalf("serve com materialização saiu %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "oraculo=snapshot(sha256:snap-teste)") {
		t.Fatalf("a materialização não declarou o oráculo derivado do snapshot:\n%s", r.stdout)
	}

	// A LINHA QUE VALE: o verificador ficou com a tool read-only e SEM a de efeito.
	linha := linhaDoNo(t, r.stdout, "verif")
	if !strings.Contains(linha, "cap:tool:fs.read") {
		t.Fatalf("o verificador perdeu a autoridade READ-ONLY: %q\n(com o oráculo por omissão viria VAZIA — é o DEF-273 por fechar)", linha)
	}
	if strings.Contains(linha, "cap:tool:http.post") {
		t.Fatalf("o verificador manteve a autoridade DE EFEITO: %q — o clamp de ADR-022 §2.2 não correu", linha)
	}

	// O nó comum NÃO é clampado — o clamp é do papel, não de toda a gente.
	if l := linhaDoNo(t, r.stdout, "build"); !strings.Contains(l, "cap:tool:fs.read") {
		t.Fatalf("o nó comum perdeu a sua tool: %q — o clamp do verificador escapou", l)
	}
}

// ---------------------------------------------------------------------------
// TESTE 5 — SEM SNAPSHOT, A MATERIALIZAÇÃO É RECUSADA (fail-closed).
//
// Não-vacuidade do teste 4 e a propriedade que interessa ao operador: aceitar
// `--plan-doc` sem `--snapshot` seria oferecer o `DefaultEffectOracle` com o ar de
// estar a fazer a coisa certa — um verificador sem autoridade e ninguém a perceber
// porquê.
// ---------------------------------------------------------------------------

func TestDEF273_SemSnapshotOComandoRecusa(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	docPath := filepath.Join(dir, "plano.json")
	escrever(t, docPath, planoComVerificador)

	r := correr(t, bin, "serve", "--wal", filepath.Join(dir, "es.wal"),
		"--run", "run-sem-snap", "--plan-doc", docPath)
	if r.code == exitOK {
		t.Fatalf("--plan-doc SEM --snapshot foi aceite — o materializador correria com o DefaultEffectOracle:\n%s", r.stdout)
	}
	if !strings.Contains(r.stderr, "DEF-273") {
		t.Fatalf("a recusa não explica a razão (esperava referência ao DEF-273):\n%s", r.stderr)
	}
}

// escrever grava um ficheiro de teste.
func escrever(t *testing.T, path, conteudo string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(conteudo), 0o600); err != nil {
		t.Fatalf("escrita de %s: %v", path, err)
	}
}

// linhaDoNo devolve a linha `  no=<id> ...` da saída da materialização.
func linhaDoNo(t *testing.T, out, nodeID string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "no="+nodeID+" ") {
			return strings.TrimSpace(l)
		}
	}
	t.Fatalf("nó %q ausente da saída da materialização:\n%s", nodeID, out)
	return ""
}

// planoComVerificador é o documento APROVADO da demonstração do oráculo de efeito.
//
// DUAS FOLHAS INDEPENDENTES, e a independência é deliberada: o `DefaultClassifier`
// do materializador trata como PAPEL-QUE-EXPANDE qualquer nó de que outro dependa, e
// este comando RECUSA spawns (não compõe o Delegator — ver [recusaSpawn]). Uma aresta
// entre os dois nós faria o teste falhar por uma razão que nada tem a ver com o
// clamp que ele mede.
//
// O verificador declara AS DUAS tools com a DE EFEITO PRIMEIRO — o pior caso, porque
// é a ordem em que `primaryTool` escolheria a tool errada se o clamp não corresse.
const planoComVerificador = `{
  "plan_version": "1.0.0",
  "objective": "compilar e verificar",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model":"m","prompt_version":"1","capabilities_hash":"sha256:snap-teste"},
  "nodes": [
    {"node_id":"build","role":"worker","objective":"compilar","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}},
    {"node_id":"verif","role":"verifier","objective":"verificar o build","depends_on":[],
     "tools":[{"name":"http.post","version":"2.0.0","digest":"sha256:bbb"},
              {"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}}
  ]
}`

// ---------------------------------------------------------------------------
// TESTE — `inspect` LÊ com o WAL detido; `serve` é RECUSADO (AOS-286).
//
// É a assimetria que faz o guard ser utilizável: quem só lê nunca pede posse e nunca é
// bloqueado. O `inspect` é, para o `aos-orq`, o que o `wal-inspect` é para o nó — a via
// que um operador usa a meio de um incidente, com o escritor a correr.
// ---------------------------------------------------------------------------

func TestAOS286_InspectLeSobPosse_ServeERecusado(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	wal := filepath.Join(dir, "es.wal")
	const run = "run-286"

	// Semeia topologia com um escritor que LARGA no fim (o WAL fica livre).
	if r := correr(t, bin, "serve", "--wal", wal, "--run", run, "--nodes", "a,b", "--release"); r.code != exitOK {
		t.Fatalf("semeadura saiu %d\nstderr:\n%s", r.code, r.stderr)
	}

	// Agora um ESCRITOR segura a posse do WAL — modelado pelo próprio mecanismo, que é o
	// que o nó `aos` usaria.
	largar, err := eventstore.LockWAL(wal)
	if err != nil {
		t.Fatalf("posse do escritor: %v", err)
	}
	defer func() { _ = largar() }()

	// (a) `serve` é RECUSADO, com o código que manda parar o outro ESCRITOR.
	s := correr(t, bin, "serve", "--wal", wal, "--run", run, "--nodes", "c")
	if s.code != exitWALDetido {
		t.Fatalf("`serve` com o WAL detido saiu %d, quer %d\nstdout:\n%s\nstderr:\n%s", s.code, exitWALDetido, s.stdout, s.stderr)
	}
	if !strings.Contains(s.stderr, "ESCRITOR") {
		t.Errorf("a recusa não explica que o conflito é com outro ESCRITOR do store (e não com o dono de um run):\n%s", s.stderr)
	}

	// (b) `inspect` LÊ na mesma, e vê a topologia toda.
	i := correr(t, bin, "inspect", "--wal", wal, "--run", run)
	if i.code != exitOK {
		t.Fatalf("`inspect` com o WAL detido saiu %d — a via de LEITURA foi bloqueada pelo guard, e é a que o operador usa a meio de um incidente\nstderr:\n%s", i.code, i.stderr)
	}
	if !strings.Contains(i.stdout, "nos=2") {
		t.Fatalf("`inspect` não leu a topologia com a posse tomada:\n%s", i.stdout)
	}
}

// TestGuardDoWAL_SegundoEscritorERecusadoDeterministicamente mede, SEM corrida nenhuma, a
// propriedade que a corrida de [TestLimite_EventStoreDeReferenciaNaoArbitraEntreProcessos]
// afirmava por acidente de tempo: um segundo escritor sobre o mesmo WAL é RECUSADO, e com
// o código que manda o operador parar o outro ESCRITOR (5) e não o outro dono do run (3).
//
// A diferença face à corrida é que aqui a posse é tomada PELO TESTE e mantida enquanto o
// processo corre. Não há janela: o `serve` encontra sempre o ficheiro detido. Uma
// propriedade que só se observa quando o escalonador colabora não é uma propriedade
// medida — é uma que se calhou de ver.
func TestGuardDoWAL_SegundoEscritorERecusadoDeterministicamente(t *testing.T) {
	bin := construir(t)
	wal := filepath.Join(t.TempDir(), "detido.wal")

	// O TESTE é o primeiro escritor, e não larga.
	largar, err := eventstore.LockWAL(wal)
	if err != nil {
		t.Fatalf("tomar a posse do WAL: %v", err)
	}
	defer func() { _ = largar() }()

	r := correr(t, bin, "serve", "--wal", wal, "--run", "run-detido", "--nodes", "a")
	if r.code != exitWALDetido {
		t.Fatalf("`serve` sobre um WAL DETIDO saiu %d, quer %d.\nstdout:\n%s\nstderr:\n%s",
			r.code, exitWALDetido, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "detido") && !strings.Contains(r.stderr, "DETIDO") {
		t.Fatalf("a recusa não diz ao operador que o Event Store está detido por outro escritor:\n%s", r.stderr)
	}

	// E depois de largar, o mesmo comando passa — o guard recusa quem chega a más horas,
	// não quem chega.
	if err := largar(); err != nil {
		t.Fatalf("largar a posse: %v", err)
	}
	if r := correr(t, bin, "serve", "--wal", wal, "--run", "run-detido", "--nodes", "a"); r.code != exitOK {
		t.Fatalf("`serve` depois de a posse ser largada saiu %d, quer 0 — o guard tem de ser uma porta, não um muro.\nstderr:\n%s",
			r.code, r.stderr)
	}
}
