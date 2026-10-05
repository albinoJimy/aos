package main

// AOS-500 — um plano que declara a origem de uma saída (`outputs[].from_tool`, linha 1.3.0) NÃO
// CORRE neste binário: a entrega por referência é do AOS-501.
//
// O que os testes prendem:
//   - com `--plan-doc`, a recusa dá-se ANTES da posse: o Event Store nem chega a ser aberto e o
//     nó `aos` não recebe nada, qualquer que seja o interruptor do AOS-499;
//   - com `--goal`, a recusa dá-se a seguir à decomposição: nenhum nó é admitido nem submetido,
//     o documento não é escrito e a posse é largada;
//   - o MESMO plano sem o campo corre como sempre (não-vacuidade);
//   - a causa tem nome próprio no vocabulário fechado, e a saída é a de um documento recusado.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	planapproval "github.com/aos-ref/control-plane/governance/plan-approval"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
)

// aos500PlanoComOrigem é o organigrama medido em produção — um nó lê o documento, outro resume —
// com a saída do leitor a declarar que é o resultado da sua tool. Sem risco: auto-aprovava e
// corria, se o binário o deixasse.
const aos500PlanoComOrigem = `{
  "plan_version": "1.3.0",
  "objective": "ler-e-resumir",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model":"fixture","prompt_version":"1.4.0","capabilities_hash":"sha256:snap-aos408"},
  "nodes": [
    {"node_id":"ler","role":"reader","objective":"ler o documento","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10},
     "outputs":[{"name":"notas","type":"record","from_tool":"fs.read"}]},
    {"node_id":"resumir","role":"writer","objective":"resumir as notas","depends_on":["ler"],
     "tools":[],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10},
     "consumes":[{"from":"ler","output":"notas","type":"record"}]}
  ]
}`

// aos500PlanoSemOrigem é o mesmo plano sem a declaração: a única diferença é o campo.
func aos500PlanoSemOrigem(t *testing.T) string {
	t.Helper()
	sem := strings.Replace(aos500PlanoComOrigem, `,"from_tool":"fs.read"`, "", 1)
	if sem == aos500PlanoComOrigem {
		t.Fatal("pre-condicao: a fixture declara from_tool")
	}
	return sem
}

// aos500Ambiente monta o nó falso, o binário e os ficheiros de um `serve` com executor.
func aos500Ambiente(t *testing.T) (f *aos413No, env []string, bin, dir, snapPath string) {
	t.Helper()
	f = &aos413No{}
	srv := f.servidor(t)
	bin = construir(t)
	dir = t.TempDir()
	cred := filepath.Join(dir, "nhi.jwt")
	escrever(t, cred, "nhi-do-operador")
	env = []string{"AOS_ORQ_NODE_URL=" + srv.URL, "AOS_ORQ_NODE_CREDENTIAL_FILE=" + cred, "AOS_MODE="}
	snapPath = filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo)
	return f, env, bin, dir, snapPath
}

func aos500ExigeRecusa(t *testing.T, r resultado) {
	t.Helper()
	if r.code != exitDocumentoRecusado {
		t.Fatalf("um plano com from_tool tinha de sair %d (documento recusado, determinista); saiu %d\n%s\n%s", exitDocumentoRecusado, r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "outputs[].from_tool") || !strings.Contains(r.stderr, "AOS-501") {
		t.Fatalf("a recusa tem de dizer a causa (o campo, e de quem e a entrega):\n%s", r.stderr)
	}
	// O erro não repete texto do documento: nem ids de nó, nem o nome da tool.
	for _, texto := range []string{"fs.read", "notas", `"ler"`} {
		if strings.Contains(r.stderr, texto) {
			t.Fatalf("a recusa repete %q, que e texto do documento:\n%s", texto, r.stderr)
		}
	}
}

// TestAOS500_PlanDocComOrigemERecusadoAntesDaPosse — FALHA-ANTES: sem a recusa, este plano
// auto-aprova, corre os dois nós e publica o texto final do leitor sob um contrato que promete
// o resultado da tool.
func TestAOS500_PlanDocComOrigemERecusadoAntesDaPosse(t *testing.T) {
	// `off` é a omissão; `observe` mede. Nenhum dos dois entrega, e a recusa não depende deles.
	for _, modo := range []string{"", saidaPorReferenciaOff, saidaPorReferenciaObserve} {
		t.Run("modo="+modo, func(t *testing.T) {
			f, env, bin, dir, snapPath := aos500Ambiente(t)
			docPath := filepath.Join(dir, "plano.json")
			escrever(t, docPath, aos500PlanoComOrigem)
			wal := filepath.Join(dir, "es.wal")
			env = append(env, "AOS_ORQ_SAIDA_POR_REFERENCIA="+modo)

			r := correrComEnv(t, env, bin, "serve", "--wal", wal, "--run", "run-aos500", "--plan-doc", docPath,
				"--snapshot", snapPath, "--worker", "p1", "--poll-interval", "20ms")
			aos500ExigeRecusa(t, r)

			// ANTES DA POSSE, E ANTES DE QUALQUER EFEITO.
			if _, err := os.Stat(wal); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("o Event Store foi aberto para escrita antes da recusa (stat do WAL: %v)", err)
			}
			if strings.Contains(r.stdout, "posse:") || strings.Contains(r.stdout, "snapshot:") {
				t.Fatalf("o serve avancou ate a posse ou ate conferir o snapshot com o no:\n%s", r.stdout)
			}
			if ids := f.submetidos(); len(ids) != 0 {
				t.Fatalf("um plano recusado submeteu runs ao no: %v", ids)
			}
		})
	}
}

// TestAOS500_OMesmoPlanoSemOrigemCorre — NÃO-VACUIDADE: a recusa é do campo, não do plano. E o
// carimbo 1.3.0 sozinho não recusa nada.
func TestAOS500_OMesmoPlanoSemOrigemCorre(t *testing.T) {
	for _, carimbo := range []string{"1.3.0", "1.2.0"} {
		t.Run(carimbo, func(t *testing.T) {
			f, env, bin, dir, snapPath := aos500Ambiente(t)
			docPath := filepath.Join(dir, "plano.json")
			escrever(t, docPath, strings.Replace(aos500PlanoSemOrigem(t), `"plan_version": "1.3.0"`, `"plan_version": "`+carimbo+`"`, 1))
			wal := filepath.Join(dir, "es.wal")

			r := correrComEnv(t, env, bin, "serve", "--wal", wal, "--run", "run-aos500-sem", "--plan-doc", docPath,
				"--snapshot", snapPath, "--worker", "p1", "--poll-interval", "20ms")
			if r.code != exitOK {
				t.Fatalf("o plano sem from_tool tinha de correr; saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
			}
			if ids := f.submetidos(); len(ids) != 2 {
				t.Fatalf("os dois nos tinham de ser submetidos ao no; foram %v", ids)
			}
			// O consumidor recebe o que sempre recebeu: o texto final do leitor.
			entradas := f.inputs("run-aos500-sem~resumir")
			if len(entradas) != 1 {
				t.Fatalf("o resumidor tinha de receber uma entrada; recebeu %v", entradas)
			}
			if e, _ := entradas[0].(map[string]any); e["content"] != "feito: run-aos500-sem~ler" {
				t.Fatalf("a entrada do resumidor nao e o texto final do leitor: %v", entradas[0])
			}
		})
	}
}

// TestAOS500_GoalComOrigemERecusadoSemSubmeterNada — o caminho do planeador: o modelo (aqui, a
// fixture) devolve um plano com `from_tool`. A posse já existe, porque o planeador corre sob ela;
// o que não pode acontecer é um nó ser admitido ou submetido.
func TestAOS500_GoalComOrigemERecusadoSemSubmeterNada(t *testing.T) {
	f, env, bin, dir, snapPath := aos500Ambiente(t)
	fix := filepath.Join(dir, "fixture.json")
	escrever(t, fix, aos500PlanoComOrigem)
	doc := filepath.Join(dir, "plano-out.json")
	wal := filepath.Join(dir, "es.wal")
	const run = "run-aos500-goal"

	r := correrComEnv(t, env, bin, "serve", "--wal", wal, "--run", run, "--goal", "ler-e-resumir",
		"--snapshot", snapPath, "--decompose-fixture", fix, "--plan-out", doc, "--worker", "p1", "--poll-interval", "20ms")
	aos500ExigeRecusa(t, r)
	if !strings.Contains(r.stdout, "decomposto:") {
		t.Fatalf("pre-condicao: a decomposicao tinha de ter corrido (a recusa e a seguir a ela):\n%s", r.stdout)
	}
	if ids := f.submetidos(); len(ids) != 0 {
		t.Fatalf("um plano recusado submeteu runs ao no: %v", ids)
	}
	for _, rasto := range []string{"gate de plano:", "nos_despachados", "no admitido", "materializado"} {
		if strings.Contains(r.stdout, rasto) {
			t.Fatalf("o serve avancou para alem da recusa (%q):\n%s", rasto, r.stdout)
		}
	}
	if _, err := os.Stat(doc); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("o documento recusado foi escrito no --plan-out (stat: %v)", err)
	}
	// Nenhuma decisão ficou no log: o gate não chegou a ver o plano.
	if pl := correr(t, bin, "plans", "--wal", wal, "--run", run); strings.Contains(pl.stdout, "decisao=approved") {
		t.Fatalf("o plano recusado ficou aprovado no log:\n%s", pl.stdout)
	}
	// A posse foi largada: outro `serve` sobre o mesmo run não bate num lease vivo.
	if s := correrComEnv(t, env, bin, "serve", "--wal", wal, "--run", run, "--worker", "p2", "--release"); s.code != exitOK {
		t.Fatalf("depois da recusa a posse tinha de estar livre; o serve seguinte saiu %d\n%s", s.code, s.stderr)
	}

	// NÃO-VACUIDADE: a mesma fixture sem o campo decompõe, auto-aprova e corre.
	f2, env2, bin2, dir2, snap2 := aos500Ambiente(t)
	fix2 := filepath.Join(dir2, "fixture.json")
	escrever(t, fix2, aos500PlanoSemOrigem(t))
	r2 := correrComEnv(t, env2, bin2, "serve", "--wal", filepath.Join(dir2, "es.wal"), "--run", run, "--goal", "ler-e-resumir",
		"--snapshot", snap2, "--decompose-fixture", fix2, "--worker", "p1", "--poll-interval", "20ms")
	if r2.code != exitOK || len(f2.submetidos()) != 2 {
		t.Fatalf("a fixture sem from_tool tinha de correr os dois nos; saiu %d e submeteu %v\n%s\n%s", r2.code, f2.submetidos(), r2.stdout, r2.stderr)
	}
}

// TestAOS500_MaterializarRecusaOQueLeu — a segunda guarda, sobre os bytes que de facto se vão
// materializar: o ficheiro é relido depois da posse, e um documento trocado entre as duas
// leituras não corre por o primeiro ter passado. Chama-se a função sem posse nem log: a recusa
// tem de vir ANTES de qualquer um deles ser tocado (com a guarda retirada, isto rebenta).
func TestAOS500_MaterializarRecusaOQueLeu(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo)
	docPath := filepath.Join(dir, "plano.json")
	escrever(t, docPath, aos500PlanoComOrigem)

	err := materializar(context.Background(), nil, nil, nil, docPath, snapPath, snapshotConferido{}, "p1", nil)
	if !errors.Is(err, errOrigemSemEntrega) {
		t.Fatalf("materializar um documento com from_tool devia dar errOrigemSemEntrega; deu %v", err)
	}
}

// TestAOS500_CausaEmVocabularioFechado — o código de saída, o nome da causa e a posse.
func TestAOS500_CausaEmVocabularioFechado(t *testing.T) {
	com, err := plan.Decode([]byte(aos500PlanoComOrigem))
	if err != nil {
		t.Fatalf("a fixture tem de descodificar: %v", err)
	}
	sem, err := plan.Decode([]byte(aos500PlanoSemOrigem(t)))
	if err != nil {
		t.Fatalf("a fixture sem o campo tem de descodificar: %v", err)
	}
	if err := recusarOrigemDeclarada(sem); err != nil {
		t.Fatalf("um plano sem from_tool foi recusado: %v", err)
	}
	recusa := recusarOrigemDeclarada(com)
	if !errors.Is(recusa, errOrigemSemEntrega) {
		t.Fatalf("um plano com from_tool devia dar errOrigemSemEntrega; deu %v", recusa)
	}
	if got := codigoDe(recusa); got != exitDocumentoRecusado {
		t.Fatalf("codigo de saida = %d; quer %d", got, exitDocumentoRecusado)
	}
	if got := tipoDoErro(recusa); got != "origem_sem_entrega" {
		t.Fatalf("causa = %q; quer origem_sem_entrega (nome proprio, distinto de documento_recusado)", got)
	}
	if !largaAPosse(recusa) {
		t.Fatal("a recusa e o fim do trabalho sobre o run: a posse tem de ser largada")
	}
	if classe := classeDoDesfecho(codigoDe(recusa)); classe != classeDoDesfecho(exitDocumentoRecusado) {
		t.Fatalf("classe do desfecho = %q; a de um documento recusado e %q", classe, classeDoDesfecho(exitDocumentoRecusado))
	}

	// Sobre o ficheiro: só recusa o que é seu. Um caminho que não existe e um documento que não
	// descodifica seguem para o caminho de sempre.
	dir := t.TempDir()
	if err := recusarDocumentoComOrigem(filepath.Join(dir, "nao-existe.json")); err != nil {
		t.Fatalf("um ficheiro inexistente nao e desta recusa: %v", err)
	}
	lixo := filepath.Join(dir, "lixo.json")
	escrever(t, lixo, `{"plan_version":`)
	if err := recusarDocumentoComOrigem(lixo); err != nil {
		t.Fatalf("um documento que nao descodifica nao e desta recusa: %v", err)
	}
	bom := filepath.Join(dir, "com.json")
	escrever(t, bom, aos500PlanoComOrigem)
	if err := recusarDocumentoComOrigem(bom); !errors.Is(err, errOrigemSemEntrega) {
		t.Fatalf("o documento com from_tool devia ser recusado; deu %v", err)
	}
}

// TestAOS500_MapeadorLevaAOrigemAoCartao — o mapeador do documento para o plano de aprovação
// copia o campo, e o cartão de um plano com `from_tool` é diferente do cartão do mesmo plano sem
// ele. Sem isto o humano aprovava uma entrega que o cartão não mostrava.
func TestAOS500_MapeadorLevaAOrigemAoCartao(t *testing.T) {
	com, err := plan.Decode([]byte(aos500PlanoComOrigem))
	if err != nil {
		t.Fatal(err)
	}
	sem, err := plan.Decode([]byte(aos500PlanoSemOrigem(t)))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := carregarSnapshot(escreverTmp(t, aos408SnapshotComPerigo))
	if err != nil {
		t.Fatalf("carregarSnapshot: %v", err)
	}
	cartao := func(doc plan.PlanDocument) string {
		t.Helper()
		return aos500CartaoDoPlano(t, doc, snap)
	}
	cCom, cSem := cartao(com), cartao(sem)
	if cCom == cSem {
		t.Fatal("o plano com from_tool e o mesmo plano sem ele deram o mesmo cartao")
	}
	if !strings.Contains(cCom, `"notas:record:untrusted:tool=fs.read"`) {
		t.Fatalf("o cartao nao mostra a origem declarada da saida:\n%s", cCom)
	}
	if !strings.Contains(cSem, `"notas:record:untrusted"`) || strings.Contains(cSem, "tool=") {
		t.Fatalf("o cartao do plano sem origem nao e o de sempre:\n%s", cSem)
	}
	if got := strings.Replace(cCom, ":tool=fs.read", "", 1); got != cSem {
		t.Fatalf("a unica diferenca entre os cartoes devia ser a origem da saida:\n com=%s\n sem=%s", cCom, cSem)
	}
}

// aos500CartaoDoPlano mapeia o documento para o plano de aprovação, pelo mapeador de produção, e
// devolve o wire do cartão.
func aos500CartaoDoPlano(t *testing.T, doc plan.PlanDocument, snap planvalidate.Snapshot) string {
	t.Helper()
	riscos := planvalidate.ResolveRisks(doc, snap, nil)
	card, err := planapproval.BuildPlanCard(planoParaGate(doc, riscos, "run-aos500", "agt-run-aos500", "orq"))
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("Marshal do cartao: %v", err)
	}
	return string(raw)
}
