package main

// AOS-500 — FORA DE `on`, A LINHA 1.3.0 DO PLANO NÃO CORRE NESTE BINÁRIO: nem o documento que
// declara a origem de uma saída (`outputs[].from_tool`), nem o que só carimba essa linha. O
// binário trata-os como o anterior os tratava.
//
// O AOS-501 ligou a entrega por referência atrás de `AOS_ORQ_SAIDA_POR_REFERENCIA=on`. Estes
// testes correm SEM a variável (a omissão é `off`) e continuam a valer tal como foram escritos:
// são eles que prendem que, fora de `on`, a guarda não saiu. O que acontece em `on` está em
// aos501_entrega_por_referencia_test.go.
//
// O que os testes prendem:
//   - com `--plan-doc`, a recusa dá-se ANTES da posse: o Event Store nem chega a ser aberto e o
//     nó `aos` não recebe nada, qualquer que seja o interruptor do AOS-499 — para o campo e para
//     o carimbo sozinho;
//   - com `--goal`, um documento da linha 1.3.0 é uma tentativa RECUSADA do planeador, que tenta
//     de novo: a sequência [1.3.0, 1.2.0] corre pela segunda, e o `--plan-out` sai carimbado
//     1.2.0. Só o planeador que insiste acaba o `serve`, e acaba-o como acaba qualquer plano
//     recusado em todas as tentativas (saída 9);
//   - todo o documento relido do disco é recusado com a saída 10 e a causa `origem_sem_entrega`:
//     em `materializar`, na re-verificação de um pendente pelo `consume`, e no `decide`;
//   - a guarda vê a origem em qualquer nó e em qualquer saída;
//   - o MESMO plano sem o campo, carimbado 1.2.0, corre como sempre (não-vacuidade).

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
	"github.com/aos-ref/control-plane/orchestrator/planner"
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

// aos500PlanoSemOrigem é o mesmo plano sem a declaração, AINDA carimbado 1.3.0: a única
// diferença para [aos500PlanoComOrigem] é o campo.
func aos500PlanoSemOrigem(t *testing.T) string {
	t.Helper()
	sem := strings.Replace(aos500PlanoComOrigem, `,"from_tool":"fs.read"`, "", 1)
	if sem == aos500PlanoComOrigem {
		t.Fatal("pre-condicao: a fixture declara from_tool")
	}
	return sem
}

// aos500Carimbado troca o carimbo de um dos planos acima.
func aos500Carimbado(t *testing.T, plano, carimbo string) string {
	t.Helper()
	out := strings.Replace(plano, `"plan_version": "1.3.0"`, `"plan_version": "`+carimbo+`"`, 1)
	if out == plano && carimbo != "1.3.0" {
		t.Fatal("pre-condicao: a fixture carimba 1.3.0")
	}
	return out
}

// aos500PlanoDeHoje é o plano que um planeador produz hoje: sem o campo, carimbado 1.2.0.
func aos500PlanoDeHoje(t *testing.T) string {
	t.Helper()
	return aos500Carimbado(t, aos500PlanoSemOrigem(t), "1.2.0")
}

// aos500Ambiente monta o nó falso e os ficheiros de um `serve` com executor.
func aos500Ambiente(t *testing.T) (f *aos413No, env []string, dir, snapPath string) {
	t.Helper()
	f = &aos413No{}
	srv := f.servidor(t)
	dir = t.TempDir()
	cred := filepath.Join(dir, "nhi.jwt")
	escrever(t, cred, "nhi-do-operador")
	env = []string{"AOS_ORQ_NODE_URL=" + srv.URL, "AOS_ORQ_NODE_CREDENTIAL_FILE=" + cred, "AOS_MODE="}
	snapPath = filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo)
	return f, env, dir, snapPath
}

// aos500ExigeRecusa confere a recusa TERMINAL do documento: a saída 10 e a causa dita.
func aos500ExigeRecusa(t *testing.T, r resultado) {
	t.Helper()
	if r.code != exitDocumentoRecusado {
		t.Fatalf("um documento da linha 1.3.0 tinha de sair %d (documento recusado, determinista); saiu %d\n%s\n%s", exitDocumentoRecusado, r.code, r.stdout, r.stderr)
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

// aos500ExigeAntesDaPosse confere que o `serve` não tocou em nada.
func aos500ExigeAntesDaPosse(t *testing.T, r resultado, wal string, f *aos413No) {
	t.Helper()
	if _, err := os.Stat(wal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("o Event Store foi aberto para escrita antes da recusa (stat do WAL: %v)", err)
	}
	if strings.Contains(r.stdout, "posse:") || strings.Contains(r.stdout, "snapshot:") {
		t.Fatalf("o serve avancou ate a posse ou ate conferir o snapshot com o no:\n%s", r.stdout)
	}
	if ids := f.submetidos(); len(ids) != 0 {
		t.Fatalf("um plano recusado submeteu runs ao no: %v", ids)
	}
}

// TestAOS500ComOBinarioReal corre os cenários que precisam do `aos-orq` como processo sobre UM
// só binário (compilá-lo por cenário custava minutos à suite).
func TestAOS500ComOBinarioReal(t *testing.T) {
	bin := construir(t)
	t.Run("PlanDocComOrigemERecusadoAntesDaPosse", func(t *testing.T) { aos500PlanDocComOrigem(t, bin) })
	t.Run("PlanDocCarimbado130SemOCampoERecusado", func(t *testing.T) { aos500PlanDocSoCarimbo(t, bin) })
	t.Run("GoalRecusaNoLacoEOPlaneadorRecupera", func(t *testing.T) { aos500GoalRecupera(t, bin) })
	t.Run("GoalComPlaneadorQueInsiste", func(t *testing.T) { aos500GoalInsiste(t, bin) })
	t.Run("ConsumeComPlaneadorQueInsiste", func(t *testing.T) { aos500ConsumeInsiste(t, bin) })
	t.Run("ConsumeEDecideSobreDocumentoGuardadoDaLinha130", func(t *testing.T) { aos500DocumentoGuardado(t, bin) })
}

// aos500PlanDocComOrigem — FALHA-ANTES: sem a recusa, este plano auto-aprova, corre os dois nós
// e publica o texto final do leitor sob um contrato que promete o resultado da tool.
func aos500PlanDocComOrigem(t *testing.T, bin string) {
	// `off` é a omissão; `observe` mede. Nenhum dos dois entrega, e a recusa não depende deles.
	for _, modo := range []string{"", saidaPorReferenciaOff, saidaPorReferenciaObserve} {
		t.Run("modo="+modo, func(t *testing.T) {
			f, env, dir, snapPath := aos500Ambiente(t)
			docPath := filepath.Join(dir, "plano.json")
			escrever(t, docPath, aos500PlanoComOrigem)
			wal := filepath.Join(dir, "es.wal")
			env = append(env, "AOS_ORQ_SAIDA_POR_REFERENCIA="+modo)

			r := correrComEnv(t, env, bin, "serve", "--wal", wal, "--run", "run-aos500", "--plan-doc", docPath,
				"--snapshot", snapPath, "--worker", "p1", "--poll-interval", "20ms")
			aos500ExigeRecusa(t, r)
			aos500ExigeAntesDaPosse(t, r, wal, f)
		})
	}
}

// aos500PlanDocSoCarimbo — o carimbo 1.3.0 SEM o campo também não corre (o binário anterior
// recusava-o com a mesma saída 10), e o mesmo documento carimbado 1.2.0 corre como sempre.
// FALHA-ANTES: sem a guarda sobre o carimbo, o 1.3.0 corria os dois nós.
func aos500PlanDocSoCarimbo(t *testing.T, bin string) {
	t.Run("1.3.0", func(t *testing.T) {
		f, env, dir, snapPath := aos500Ambiente(t)
		docPath := filepath.Join(dir, "plano.json")
		escrever(t, docPath, aos500PlanoSemOrigem(t))
		wal := filepath.Join(dir, "es.wal")
		r := correrComEnv(t, env, bin, "serve", "--wal", wal, "--run", "run-aos500-carimbo", "--plan-doc", docPath,
			"--snapshot", snapPath, "--worker", "p1", "--poll-interval", "20ms")
		aos500ExigeRecusa(t, r)
		if !strings.Contains(r.stderr, "carimba 1.3.0") {
			t.Fatalf("a recusa do carimbo sozinho tem de o dizer:\n%s", r.stderr)
		}
		aos500ExigeAntesDaPosse(t, r, wal, f)
	})
	// NÃO-VACUIDADE: a recusa é da linha 1.3.0, não do plano.
	t.Run("1.2.0", func(t *testing.T) {
		f, env, dir, snapPath := aos500Ambiente(t)
		docPath := filepath.Join(dir, "plano.json")
		escrever(t, docPath, aos500PlanoDeHoje(t))
		r := correrComEnv(t, env, bin, "serve", "--wal", filepath.Join(dir, "es.wal"), "--run", "run-aos500-sem", "--plan-doc", docPath,
			"--snapshot", snapPath, "--worker", "p1", "--poll-interval", "20ms")
		if r.code != exitOK {
			t.Fatalf("o plano sem from_tool carimbado 1.2.0 tinha de correr; saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
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

// aos500Goal corre um `serve --goal` com a sequência de respostas do «modelo» dada.
func aos500Goal(t *testing.T, bin, run string, respostas ...string) (r resultado, f *aos413No, env []string, wal, planOut string) {
	t.Helper()
	f, env, dir, snapPath := aos500Ambiente(t)
	var fixs []string
	for i, c := range respostas {
		p := filepath.Join(dir, "fixture-"+string(rune('a'+i))+".json")
		escrever(t, p, c)
		fixs = append(fixs, p)
	}
	planOut = filepath.Join(dir, "plano-out.json")
	wal = filepath.Join(dir, "es.wal")
	r = correrComEnv(t, env, bin, "serve", "--wal", wal, "--run", run, "--goal", "ler-e-resumir",
		"--snapshot", snapPath, "--decompose-fixture", strings.Join(fixs, ","), "--plan-out", planOut, "--worker", "p1", "--poll-interval", "20ms")
	return r, f, env, wal, planOut
}

// aos500GoalRecupera — as duas sequências da revisão adversarial. No binário anterior a primeira
// resposta era recusada (o carimbo pelo validador, o campo pelo `Decode`) e o planeador tentava
// de novo; a segunda corria. Aqui tem de ser o MESMO: saída 0, duas tentativas, os dois nós
// submetidos, e o `--plan-out` com o documento da segunda — carimbado 1.2.0.
//
// FALHA-ANTES (as duas formas que a revisão encontrou): com o carimbo aceite, a primeira
// sequência corria à primeira e guardava um documento 1.3.0; com a recusa do campo terminal, a
// segunda sequência saía 10 sem segunda tentativa.
func aos500GoalRecupera(t *testing.T, bin string) {
	for nome, primeira := range map[string]string{
		"sem campo @1.3.0, depois sem campo @1.2.0": aos500PlanoSemOrigem(t),
		"com from_tool @1.3.0, depois sem @1.2.0":   aos500PlanoComOrigem,
	} {
		t.Run(nome, func(t *testing.T) {
			const run = "run-aos500-seq"
			r, f, _, _, planOut := aos500Goal(t, bin, run, primeira, aos500PlanoDeHoje(t))
			if r.code != exitOK {
				t.Fatalf("a segunda tentativa tinha de correr (saida 0, como no binario anterior); saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
			}
			if !strings.Contains(r.stdout, "(tentativas=2,") {
				t.Fatalf("o planeador tinha de ter sido recusado uma vez e aceite a segunda (tentativas=2):\n%s", r.stdout)
			}
			if ids := f.submetidos(); len(ids) != 2 {
				t.Fatalf("os dois nos da segunda tentativa tinham de ser submetidos; foram %v", ids)
			}
			raw, err := os.ReadFile(planOut)
			if err != nil {
				t.Fatalf("o --plan-out do plano que correu tinha de existir: %v", err)
			}
			guardado, err := plan.Decode(raw)
			if err != nil {
				t.Fatalf("o --plan-out nao descodifica: %v", err)
			}
			if !guardado.PlanVersion.Equal(plan.PlanVersion{Major: 1, Minor: 2, Patch: 0}) || guardado.DeclaresOutputSource() {
				t.Fatalf("o documento guardado e carimbado %s (origem declarada: %v); tinha de ser o da segunda tentativa, 1.2.0 e sem origem — e o que um rollback consegue ler",
					guardado.PlanVersion, guardado.DeclaresOutputSource())
			}
			if strings.Contains(r.stderr, "origem_sem_entrega") || strings.Contains(r.stderr, "AOS-501") {
				t.Fatalf("no laco, a recusa nao e a terminal:\n%s", r.stderr)
			}
		})
	}
}

// aos500GoalInsiste — o planeador que devolve SEMPRE um documento da linha 1.3.0 esgota as
// tentativas, e o `serve` acaba como acaba com qualquer plano recusado em todas elas: saída 9,
// nada submetido, nada guardado, nenhuma decisão, e a posse largada.
func aos500GoalInsiste(t *testing.T, bin string) {
	for nome, resposta := range map[string]string{
		"com from_tool": aos500PlanoComOrigem,
		"so o carimbo":  aos500PlanoSemOrigem(t),
	} {
		t.Run(nome, func(t *testing.T) {
			const run = "run-aos500-goal"
			r, f, env, wal, planOut := aos500Goal(t, bin, run, resposta)
			if r.code != exitPlanoRecusado {
				t.Fatalf("um planeador que insiste na linha 1.3.0 esgota as tentativas: saida %d (plano recusado); saiu %d\n%s\n%s", exitPlanoRecusado, r.code, r.stdout, r.stderr)
			}
			if !strings.Contains(r.stderr, "plan_version_ahead_of_reader") {
				t.Fatalf("a recusa do laco tem de levar o codigo que o binario anterior dava ao carimbo:\n%s", r.stderr)
			}
			if ids := f.submetidos(); len(ids) != 0 {
				t.Fatalf("um plano recusado submeteu runs ao no: %v", ids)
			}
			for _, rasto := range []string{"decomposto:", "gate de plano:", "nos_despachados", "no admitido", "materializado"} {
				if strings.Contains(r.stdout, rasto) {
					t.Fatalf("o serve avancou para alem da recusa (%q):\n%s", rasto, r.stdout)
				}
			}
			if _, err := os.Stat(planOut); !errors.Is(err, os.ErrNotExist) {
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
		})
	}
}

// aos500ConsumeInsiste — o mesmo pelo `consume`, de ponta a ponta: o pedido fecha TERMINAL com o
// código do plano recusado, o nó não recebe nenhum run, e o documento não fica guardado.
func aos500ConsumeInsiste(t *testing.T, bin string) {
	no := &aos442No{}
	a := novoAmbiente442(t, bin, no)
	const run = "plan-aos500-insiste"
	no.oferecer(pedidoReclamado{RunID: run, Objective: "ler e resumir", Geracao: 1})
	r := a.consumir(t, aos500PlanoComOrigem)
	if !strings.Contains(r.stdout, "codigo=9 classe=terminal") {
		t.Fatalf("o pedido tinha de fechar terminal com o codigo do plano recusado (9):\n%s\n%s", r.stdout, r.stderr)
	}
	exigirDesfechos(t, no.vistos(), pedidoDeDesfechoVisto{RunID: run, Geracao: 1, Classe: "terminal", Codigo: exitPlanoRecusado})
	if ids := no.submetidos(); len(ids) != 0 {
		t.Fatalf("um plano recusado submeteu runs ao no: %v", ids)
	}
	exigirDocumentoApagado(t, a.documentoDe(t, run))
}

// aos500DocumentoGuardado — os dois leitores do disco que a revisão encontrou sem guarda (M3),
// pelo binário: a re-verificação de um pendente pelo `consume` e o `decide`.
//
// O estado monta-se sem binário mutante: um plano de risco (1.2.0) fica PENDENTE de humano e o
// seu documento guardado é trocado, no disco, pelo mesmo plano carimbado 1.3.0 — o que um
// binário posterior teria guardado antes de um rollback para este. Os dois leitores têm de
// responder com a saída 10 e a causa `origem_sem_entrega`, sem escrever nada.
//
// FALHA-ANTES: sem as guardas, o `decide` respondia 7 (o hash não é o selado) e o `consume`
// fechava o pedido como `documento_recusado` — e, com um pendente cujo hash fosse o selado, o
// `consume` respondia `aguarda_humano` em cada re-oferta e a recusa só chegava depois de o
// humano aprovar.
func aos500DocumentoGuardado(t *testing.T, bin string) {
	no := &aos442No{}
	a := novoAmbiente442(t, bin, no)
	const run = "plan-aos500-guardado"

	no.oferecer(pedidoReclamado{RunID: run, Objective: "ler e publicar", Geracao: 1})
	r1 := a.consumir(t, aos413PlanoDoModeloVivo)
	if !strings.Contains(r1.stdout, "pendente de aprovacao humana") {
		t.Fatalf("pre-condicao: o plano com n3 danger tinha de ficar pendente:\n%s", r1.stdout)
	}
	doc := a.documentoDe(t, run)
	raw, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("pre-condicao: o documento pendente tinha de estar guardado: %v", err)
	}
	guardado, err := plan.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	guardado.PlanVersion = plan.PlanVersion{Major: 1, Minor: 3, Patch: 0}
	trocado, err := plan.Encode(guardado)
	if err != nil {
		t.Fatal(err)
	}
	escrever(t, doc, string(trocado))

	// O `decide`: recusa pela causa própria, antes de olhar para a assinatura — e não decide.
	pl := correr(t, bin, "plans", "--wal", a.wal, "--run", run)
	m := reRequestID.FindStringSubmatch(pl.stdout)
	if m == nil {
		t.Fatalf("sem request_id:\n%s", pl.stdout)
	}
	priv, aprovadores := aos408Aprovador(t, a.dir, "human:alice")
	aprovacao := aos408Assinar(t, a.dir, "aprovacao.json", m[1], "human:alice", priv, true)
	d := correr(t, bin, "decide", "--wal", a.wal, "--run", run, "--plan-doc", doc, "--snapshot", a.snap,
		"--decision", "approve", "--approval", aprovacao, "--approvers", aprovadores)
	if d.code != exitDocumentoRecusado || !strings.Contains(d.stderr, "AOS-501") || !strings.Contains(d.stderr, "carimba 1.3.0") {
		t.Fatalf("o decide sobre um documento da linha 1.3.0 tinha de sair %d pela causa propria; saiu %d\n%s\n%s", exitDocumentoRecusado, d.code, d.stdout, d.stderr)
	}
	if depois := correr(t, bin, "plans", "--wal", a.wal, "--run", run); !strings.Contains(depois.stdout, "estado=PENDENTE") {
		t.Fatalf("o decide recusado nao podia ter deixado decisao no log:\n%s", depois.stdout)
	}

	// O `consume`, na re-oferta: fecha o pedido, terminal, sem correr o `serve`.
	no.oferecer(pedidoReclamado{RunID: run, Objective: "ler e publicar", Geracao: 2})
	r2 := a.consumir(t, aos413PlanoDoModeloVivo)
	if !strings.Contains(r2.stdout, "codigo=10 classe=terminal") || !strings.Contains(r2.stdout, "erro=origem_sem_entrega") {
		t.Fatalf("a re-verificacao de um pendente da linha 1.3.0 tinha de fechar com codigo=10 classe=terminal erro=origem_sem_entrega:\n%s\n%s", r2.stdout, r2.stderr)
	}
	if strings.Contains(r2.stdout, "decomposto:") || strings.Contains(r2.stdout, "posse:") {
		t.Fatalf("a recusa da-se sem correr o serve (nem modelo, nem posse):\n%s", r2.stdout)
	}
	exigirDesfechos(t, no.vistos(),
		pedidoDeDesfechoVisto{RunID: run, Geracao: 1, Classe: "aguarda_humano", Codigo: exitPendenteDeAprovacao},
		pedidoDeDesfechoVisto{RunID: run, Geracao: 2, Classe: "terminal", Codigo: exitDocumentoRecusado},
	)
	if ids := no.submetidos(); len(ids) != 0 {
		t.Fatalf("um plano recusado submeteu runs ao no: %v", ids)
	}
}

// aos500Decode descodifica uma das fixtures deste ficheiro.
func aos500Decode(t *testing.T, raw string) plan.PlanDocument {
	t.Helper()
	doc, err := plan.Decode([]byte(raw))
	if err != nil {
		t.Fatalf("a fixture tem de descodificar: %v", err)
	}
	return doc
}

// TestAOS500_MaterializarRecusaOQueLeu — a segunda guarda, sobre os bytes que de facto se vão
// materializar: o ficheiro é relido depois da posse, e um documento trocado entre as duas
// leituras não corre por o primeiro ter passado. Chama-se a função sem posse nem log: a recusa
// tem de vir ANTES de qualquer um deles ser tocado (com a guarda retirada, isto rebenta).
func TestAOS500_MaterializarRecusaOQueLeu(t *testing.T) {
	for nome, plano := range map[string]string{"com from_tool": aos500PlanoComOrigem, "so o carimbo": aos500PlanoSemOrigem(t)} {
		dir := t.TempDir()
		snapPath := filepath.Join(dir, "snap.json")
		escrever(t, snapPath, aos408SnapshotComPerigo)
		docPath := filepath.Join(dir, "plano.json")
		escrever(t, docPath, plano)

		err := materializar(context.Background(), nil, nil, nil, docPath, snapPath, snapshotConferido{}, "p1", nil)
		if !errors.Is(err, errOrigemSemEntrega) {
			t.Fatalf("%s: materializar um documento da linha 1.3.0 devia dar errOrigemSemEntrega; deu %v", nome, err)
		}
	}
}

// TestAOS500_CausaEmVocabularioFechado — o código de saída, o nome da causa e a posse.
func TestAOS500_CausaEmVocabularioFechado(t *testing.T) {
	com := aos500Decode(t, aos500PlanoComOrigem)
	soCarimbo := aos500Decode(t, aos500PlanoSemOrigem(t))
	deHoje := aos500Decode(t, aos500PlanoDeHoje(t))

	if err := recusarSemEntrega(deHoje); err != nil {
		t.Fatalf("um plano sem from_tool carimbado 1.2.0 foi recusado: %v", err)
	}
	for nome, doc := range map[string]plan.PlanDocument{"com from_tool": com, "so o carimbo": soCarimbo} {
		recusa := recusarSemEntrega(doc)
		if !errors.Is(recusa, errOrigemSemEntrega) {
			t.Fatalf("%s: devia dar errOrigemSemEntrega; deu %v", nome, recusa)
		}
		if got := codigoDe(recusa); got != exitDocumentoRecusado {
			t.Fatalf("%s: codigo de saida = %d; quer %d", nome, got, exitDocumentoRecusado)
		}
		if got := tipoDoErro(recusa); got != "origem_sem_entrega" {
			t.Fatalf("%s: causa = %q; quer origem_sem_entrega (nome proprio, distinto de documento_recusado)", nome, got)
		}
		if !largaAPosse(recusa) {
			t.Fatalf("%s: a recusa e o fim do trabalho sobre o run: a posse tem de ser largada", nome)
		}
		if classe := classeDoDesfecho(codigoDe(recusa)); classe != classeDoDesfecho(exitDocumentoRecusado) {
			t.Fatalf("%s: classe do desfecho = %q; a de um documento recusado e %q", nome, classe, classeDoDesfecho(exitDocumentoRecusado))
		}
	}
	// O patch do carimbo não conta: 1.3.5 é a mesma linha. E a 1.4.0 não é desta guarda — é do
	// validador, que a recusa por estar à frente do leitor, como sempre.
	patch := soCarimbo
	patch.PlanVersion = plan.PlanVersion{Major: 1, Minor: 3, Patch: 5}
	if !errors.Is(recusarSemEntrega(patch), errOrigemSemEntrega) {
		t.Fatal("um documento carimbado 1.3.5 e da linha 1.3 e tinha de ser recusado")
	}
	acima := soCarimbo
	acima.PlanVersion = plan.PlanVersion{Major: 1, Minor: 4, Patch: 0}
	if err := recusarSemEntrega(acima); err != nil {
		t.Fatalf("a 1.4.0 sem origem nao e desta guarda (e do validador): %v", err)
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

// TestAOS500_AGuardaVeAOrigemEmQualquerNoEQualquerSaida — a recusa do `aos-orq` depende de um
// predicado que os testes anteriores só exercitavam com a origem na primeira saída do primeiro
// nó. Uma guarda que só olhasse para o primeiro nó (mutação R19), ou um predicado que só olhasse
// para a primeira saída (R22), deixava correr um plano com a origem noutro sítio. O carimbo é
// 1.2.0 de propósito: aqui só o CAMPO pode fazer disparar a guarda.
func TestAOS500_AGuardaVeAOrigemEmQualquerNoEQualquerSaida(t *testing.T) {
	for no := 0; no < 3; no++ {
		for saida := 0; saida < 2; saida++ {
			doc := aos500Decode(t, aos500PlanoDeHoje(t))
			doc.Nodes = []plan.Node{
				{NodeID: "n0", Role: "r", Objective: "o", Outputs: []plan.Output{{Name: "a", Type: plan.PayloadRecord}, {Name: "b", Type: plan.PayloadArtifact}}},
				{NodeID: "n1", Role: "r", Objective: "o", Outputs: []plan.Output{{Name: "a", Type: plan.PayloadRecord}, {Name: "b", Type: plan.PayloadArtifact}}},
				{NodeID: "n2", Role: "r", Objective: "o", Outputs: []plan.Output{{Name: "a", Type: plan.PayloadRecord}, {Name: "b", Type: plan.PayloadArtifact}}},
			}
			if err := recusarSemEntrega(doc); err != nil {
				t.Fatalf("pre-condicao: sem origem e carimbado 1.2.0, o documento nao e desta guarda: %v", err)
			}
			doc.Nodes[no].Outputs[saida].FromTool = "fs_read"
			err := recusarSemEntrega(doc)
			if !errors.Is(err, errOrigemSemEntrega) {
				t.Fatalf("a origem na saida %d do no %d nao foi vista pela guarda (err=%v)", saida, no, err)
			}
			if !strings.Contains(err.Error(), "1 no(s) do plano declaram a origem") {
				t.Fatalf("a mensagem conta os nos que declaram: %v", err)
			}
		}
	}
	// Pelo caminho do ficheiro, com a origem só no SEGUNDO nó e na SEGUNDA saída.
	segundo := strings.Replace(aos500PlanoDeHoje(t), `"tools":[],`,
		`"tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],"outputs":[{"name":"livre","type":"record"},{"name":"copia","type":"artifact","from_tool":"fs.read"}],`, 1)
	if segundo == aos500PlanoDeHoje(t) {
		t.Fatal("pre-condicao: o segundo no da fixture tem `tools` vazio")
	}
	if doc := aos500Decode(t, segundo); doc.Nodes[0].DeclaresOutputSource() || !doc.Nodes[1].DeclaresOutputSource() {
		t.Fatal("pre-condicao: so o segundo no declara origem")
	}
	dir := t.TempDir()
	docPath := filepath.Join(dir, "plano.json")
	escrever(t, docPath, segundo)
	if err := recusarDocumentoComOrigem(docPath); !errors.Is(err, errOrigemSemEntrega) {
		t.Fatalf("o documento com a origem no segundo no devia ser recusado antes da posse; deu %v", err)
	}
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo)
	if err := materializar(context.Background(), nil, nil, nil, docPath, snapPath, snapshotConferido{}, "p1", nil); !errors.Is(err, errOrigemSemEntrega) {
		t.Fatalf("materializar um documento com a origem no segundo no devia dar errOrigemSemEntrega; deu %v", err)
	}
}

// TestAOS500_NoLacoDoPlaneadorARecusaNaoETerminal — a guarda no laço do `serve --goal`: um
// documento carimbado na linha 1.3.0 volta ao planeador como recusa do validador, com o código
// que o binário anterior dava a esse carimbo. O plano de hoje não é tocado.
func TestAOS500_NoLacoDoPlaneadorARecusaNaoETerminal(t *testing.T) {
	snap, err := carregarSnapshot(escreverTmp(t, aos408SnapshotComPerigo))
	if err != nil {
		t.Fatalf("carregarSnapshot: %v", err)
	}
	v := validadorDoSnapshot{snap: snap}
	quer := planner.Rejection{Rule: "schema", Reason: "plan_version_ahead_of_reader"}
	for nome, plano := range map[string]string{"com from_tool": aos500PlanoComOrigem, "so o carimbo": aos500PlanoSemOrigem(t)} {
		r := v.Validate(aos500Decode(t, plano))
		if r == nil {
			t.Fatalf("%s: um documento carimbado 1.3.0 foi ACEITE no laco do planeador — corria, e ficava guardado carimbado 1.3.0", nome)
		}
		if *r != quer {
			t.Fatalf("%s: recusa do laco = %+v; quer %+v (o codigo do binario anterior, sem node_id)", nome, *r, quer)
		}
	}
	if r := v.Validate(aos500Decode(t, aos500PlanoDeHoje(t))); r != nil {
		t.Fatalf("o plano de hoje (sem o campo, 1.2.0) foi recusado no laco: %+v", *r)
	}
	// O campo com um carimbo abaixo da linha continua a ser do validador, como antes.
	if r := v.Validate(aos500Decode(t, aos500Carimbado(t, aos500PlanoComOrigem, "1.2.0"))); r == nil || r.Reason != "plan_version_below_features" {
		t.Fatalf("from_tool carimbado 1.2.0: quer plan_version_below_features do validador; veio %+v", r)
	}
}

// TestAOS500_OsLeitoresDoDiscoRecusamALinha130 — os dois leitores que a revisão encontrou sem
// guarda (M3), chamados directamente. O hash dado ao `decide` é o do PRÓPRIO documento: a recusa
// não pode depender de o documento ser outro. O estado dado à re-verificação é nil: a recusa tem
// de vir antes de ele ser lido (com a guarda retirada, isto rebenta).
func TestAOS500_OsLeitoresDoDiscoRecusamALinha130(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo)
	for nome, plano := range map[string]string{"com from_tool": aos500PlanoComOrigem, "so o carimbo": aos500PlanoSemOrigem(t)} {
		docPath := filepath.Join(dir, "plano-"+strings.ReplaceAll(nome, " ", "-")+".json")
		escrever(t, docPath, plano)
		selado := hashDoPlano(aos500Decode(t, plano))

		if _, _, err := lerDocumentoPorHash(docPath, selado); !errors.Is(err, errOrigemSemEntrega) {
			t.Fatalf("%s: o decide sobre um documento da linha 1.3.0 (com o hash selado certo) devia dar errOrigemSemEntrega; deu %v", nome, err)
		}
		if _, err := documentoExigeHumano(docPath, snapPath, "run-aos500", nil); !errors.Is(err, errOrigemSemEntrega) {
			t.Fatalf("%s: a re-verificacao de um pendente da linha 1.3.0 devia dar errOrigemSemEntrega; deu %v", nome, err)
		}
	}
	// NÃO-VACUIDADE: o documento de hoje lê-se pelo `decide` como sempre.
	hoje := filepath.Join(dir, "hoje.json")
	escrever(t, hoje, aos500PlanoDeHoje(t))
	if _, _, err := lerDocumentoPorHash(hoje, hashDoPlano(aos500Decode(t, aos500PlanoDeHoje(t)))); err != nil {
		t.Fatalf("o documento de hoje foi recusado pelo decide: %v", err)
	}
}

// TestAOS500_MapeadorLevaAOrigemAoCartao — o mapeador do documento para o plano de aprovação
// copia o campo, e o cartão de um plano com `from_tool` é diferente do cartão do mesmo plano sem
// ele. Sem isto o humano aprovava uma entrega que o plano de aprovação não mostrava.
func TestAOS500_MapeadorLevaAOrigemAoCartao(t *testing.T) {
	com := aos500Decode(t, aos500PlanoComOrigem)
	sem := aos500Decode(t, aos500PlanoSemOrigem(t))
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
	// Duas diferenças, e só elas: a origem na saída, e o carimbo do contrato do cartão, que sobe a
	// 1.2.0 quando o cartão mostra uma origem (AOS-501).
	got := strings.Replace(cCom, ":tool=fs.read", "", 1)
	got = strings.Replace(got, `{"schema_version":"1.2.0","run_id"`, `{"schema_version":"1.1.0","run_id"`, 1)
	if got != cSem {
		t.Fatalf("a diferenca entre os cartoes devia ser so a origem da saida e o carimbo:\n com=%s\n sem=%s", cCom, cSem)
	}
}

// TestAOS500_MapeadorLevaAOrigemSoASaidaQueADeclara — um nó com DUAS saídas em que só uma tem
// origem: o cartão mostra a origem nessa, e só nessa. Um mapeador que copiasse a origem da
// primeira saída para todas (mutação R10) passava no teste anterior, que só tem uma saída.
func TestAOS500_MapeadorLevaAOrigemSoASaidaQueADeclara(t *testing.T) {
	snap, err := carregarSnapshot(escreverTmp(t, aos408SnapshotComPerigo))
	if err != nil {
		t.Fatalf("carregarSnapshot: %v", err)
	}
	for _, c := range []struct {
		nome    string
		saidas  string
		querCom string
		querSem string
	}{
		{"origem na primeira", `[{"name":"notas","type":"record","from_tool":"fs.read"},{"name":"anexo","type":"artifact"}]`,
			`"notas:record:untrusted:tool=fs.read"`, `"anexo:artifact:untrusted"`},
		{"origem na segunda", `[{"name":"anexo","type":"artifact"},{"name":"notas","type":"record","from_tool":"fs.read"}]`,
			`"notas:record:untrusted:tool=fs.read"`, `"anexo:artifact:untrusted"`},
	} {
		raw := strings.Replace(aos500PlanoComOrigem, `[{"name":"notas","type":"record","from_tool":"fs.read"}]`, c.saidas, 1)
		if raw == aos500PlanoComOrigem && c.nome != "" {
			t.Fatalf("%s: pre-condicao: a fixture tem a saida `notas` com origem", c.nome)
		}
		cartao := aos500CartaoDoPlano(t, aos500Decode(t, raw), snap)
		if !strings.Contains(cartao, c.querCom) {
			t.Fatalf("%s: o cartao nao mostra a origem na saida que a declara (%s):\n%s", c.nome, c.querCom, cartao)
		}
		if !strings.Contains(cartao, c.querSem) {
			t.Fatalf("%s: a saida sem origem tinha de aparecer como sempre (%s):\n%s", c.nome, c.querSem, cartao)
		}
		if n := strings.Count(cartao, "tool="); n != 1 {
			t.Fatalf("%s: a origem tinha de aparecer numa so saida do cartao; aparece em %d:\n%s", c.nome, n, cartao)
		}
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
