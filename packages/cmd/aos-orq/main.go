// Comando aos-orq — o componente AUTÓNOMO de ciclo de vida de run que o ADR-018
// nomeou para o distribuído e que o ADR-023 governa (AOS-281).
//
// # Porque é um binário à parte, e não uma flag do nó
//
// Não é preferência de arrumação — é o que as duas fronteiras guardadas por teste
// impõem (ADR-023 §2.6). O nó `aos` não pode importar o ORQ/SCH (ADR-018 §5, guarda
// directo E transitivo); o despachante não pode importar ciclo de vida (allowlist de
// imports); e `packages/integration` está DENTRO do grafo de build do nó, pelo que
// também não serve. Este é o terceiro sítio.
//
// DESLIGADO POR OMISSÃO na v1: nenhum deployment single-host o arranca, e o nó não
// sabe que ele existe (Carta §7 — a forma do produto v1 não é reaberta).
//
// # O que este comando demonstra
//
// A posse de um run por LEASE DURÁVEL, exercida por PROCESSOS REAIS cujo ÚNICO canal
// de coordenação é o Event Store — nunca memória partilhada (AOS-100). Dois arranques
// do binário partilham exactamente aquilo que dois processos partilham em produção: o
// log, e mais nada.
//
//	aos-orq serve   --wal F  --run R [--plan P] [--nodes a,b,c] [--release]
//	aos-orq serve   --nats HOST:PORTA --run R [...]
//	aos-orq inspect --wal F  --run R
//
// `serve` reclama a posse, re-hidrata o grafo do log, escreve sob fencing e — com
// `--release` — ANUNCIA que largou. `inspect` lê e não escreve nada.
//
// # DUAS TOPOLOGIAS, e o substrato é que decide qual
//
// Este comando corria numa só topologia porque só havia um substrato. Com o AOS-100
// há dois, e a diferença entre eles é a única coisa que importa aqui:
//
// **--wal (Event Store de REFERÊNCIA).** A arbitragem da posse depende de o
// `expected_seq` do stream `lease:<run_id>` ser atómico ENTRE ESCRITORES, e este
// substrato não o é entre PROCESSOS: as réplicas são cópias in-process do log e o
// índice de dedup vive em memória, pelo que cada `Open` fica com a sua própria cabeça.
// MEDIDO a 2026-08-30 — dois `Open` sobre o mesmo ficheiro e dois `Claim` do mesmo run
// passam AMBOS e mintam AMBOS o token 1. A topologia suportada é a posse SEQUENCIAL
// (um serve, ANUNCIA que larga, o seguinte reclama) e correr dois `serve` em simultâneo
// é IMPEDIDO pela posse exclusiva do ficheiro (AOS-285/286, código de saída 5). Ver
// DEF-282 e ADR-023 §4.
//
// **--nats (Event Store REPLICADO).** O `expected_seq` é imposto pelo SERVIDOR e é
// atómico entre escritores — MEDIDO a 2026-08-31 contra um cluster real. Correr N
// `serve` em paralelo sobre o mesmo run passa a ser SUPORTADO: o vencedor é decidido
// pelo LEASE (código 3, «posse do RUN negada»), não por um guard de ficheiro (código
// 5). É a diferença de código que diz ao operador onde procurar.
//
// DESLIGADO POR OMISSÃO continua a valer: nenhum deployment single-host arranca este
// binário, e a v1 não é reaberta (Carta §7).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planmaterialize"
	"github.com/aos-ref/control-plane/runlifecycle"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/substrate/eventstore"
)

// leaseTTL é o TTL de concessão da posse. Curto de propósito num comando de
// demonstração: torna o caminho de expiração observável sem esperas longas.
const leaseTTL = 30 * time.Second

// Códigos de saída DISTINTOS por causa, para que um teste de aceitação (ou um
// operador) possa distinguir «não consegui a posse» de «avariou». Um único código de
// erro tornaria o processo perdedor indistinguível de um processo partido — que é
// exactamente a distinção que uma disputa de posse precisa de fazer.
const (
	exitOK          = 0
	exitErro        = 1
	exitPosseNegada = 3 // outro processo detém um lease VIVO (durable.ErrLeaseHeld)
	exitFenced      = 4 // a posse foi superada/expirou a meio (ErrStaleFencingToken)
	// exitWALDetido — outro ESCRITOR detém o Event Store inteiro (AOS-285/286). É
	// DISTINTO do 3: ali a remediação é parar quem detém aquele RUN; aqui é parar o
	// outro escritor do STORE. Um código só faria o operador procurar no sítio errado.
	exitWALDetido = 5
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitErro)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(os.Args[2:])
	case "inspect":
		err = cmdInspect(os.Args[2:])
	default:
		usage()
		os.Exit(exitErro)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos-orq: %v\n", err)
		os.Exit(codigoDe(err))
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `aos-orq — composição ORQ/SCH↔nó sob disciplina de lease (AOS-281, ADR-023)

  aos-orq serve   (--wal FICHEIRO | --nats HOST:PORTA) --run ID [--plan ID] [--nodes a,b,c]
                  [--release] [--worker NOME] [--plan-doc DOC.json --snapshot SNAP.json]
  aos-orq inspect (--wal FICHEIRO | --nats HOST:PORTA) --run ID

Substrato (EXCLUSIVO — um ou outro, nunca ambos):
  --wal   Event Store de referencia sobre ficheiro. NAO arbitra entre processos:
          posse SEQUENCIAL, e um segundo «serve» e recusado com 5.
  --nats  Event Store REPLICADO (JetStream). ARBITRA entre processos: N instancias
          em paralelo sao suportadas e o vencedor e decidido pelo LEASE (3).
          [--nats-stream NOME] [--nats-replicas N] [--nats-region REGIAO]

Códigos de saída: 0 ok · 1 erro · 3 posse do RUN negada (lease vivo de outro) · 4 posse superada/expirada · 5 WAL detido por outro ESCRITOR
`)
}

// codigoDe traduz o erro no código de saída que o distingue.
func codigoDe(err error) int {
	switch {
	case errors.Is(err, eventstore.ErrWALHeld):
		return exitWALDetido
	case errors.Is(err, durable.ErrLeaseHeld):
		return exitPosseNegada
	case errors.Is(err, durable.ErrStaleFencingToken),
		errors.Is(err, durable.ErrLeaseSuperseded),
		errors.Is(err, durable.ErrLeaseExpired):
		return exitFenced
	default:
		return exitErro
	}
}

// A abertura do Event Store — e a escolha entre o substrato de ficheiro e o REPLICADO —
// vive em substrato.go. O `inspect` abre para LEITURA (nunca pede posse); o `serve` abre
// para ESCRITA, e é aí que a posse do ficheiro é (ou não) tomada.

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var sub substrato
	sub.registarFlags(fs)
	runID := fs.String("run", "", "run_id a possuir")
	planID := fs.String("plan", "", "plan_id do run (default: <run>-plan)")
	nodes := fs.String("nodes", "", "nós a admitir no grafo, separados por vírgula")
	release := fs.Bool("release", false, "ANUNCIAR que larga a posse no fim (handoff sem esperar TTL)")
	worker := fs.String("worker", "orq", "rótulo do worker (observabilidade — nunca decide liveness)")
	planDoc := fs.String("plan-doc", "", "ficheiro JSON do PlanDocument APROVADO a materializar")
	snapshot := fs.String("snapshot", "", "ficheiro JSON do snapshot PINADO de capabilities (obrigatório com --plan-doc: é dele que sai o oráculo de efeito)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *runID == "" {
		return errors.New("--run é obrigatório")
	}
	planoID := *planID
	if planoID == "" {
		planoID = *runID + "-plan"
	}

	ctx := context.Background()
	// ESCRITA ⇒ sobre ficheiro, posse exclusiva do WAL (AOS-286); sobre o substrato
	// REPLICADO, nenhuma posse de ficheiro — N escritores são o objectivo (AOS-100).
	// Ver substrato.go, onde essa diferença está nomeada.
	store, fechar, err := sub.abrirParaEscrita()
	if err != nil {
		return err
	}
	defer func() { _ = fechar() }()
	fmt.Println(sub.descrever())

	leases, err := durable.NewLeaseManager(store, leaseTTL, durable.WithWorkerID(*worker))
	if err != nil {
		return err
	}

	// (1) POSSE. Um lease VIVO de outro processo devolve ErrLeaseHeld e este processo
	// sai com o código 3 — não espera, não rouba, não escreve.
	ten, err := runlifecycle.Claim(ctx, store, leases, *runID)
	if err != nil {
		return fmt.Errorf("posse do run %q: %w", *runID, err)
	}
	fmt.Printf("posse: run=%s plano=%s token=%d worker=%s\n", ten.RunID(), planoID, ten.Token(), *worker)

	// O emissor do domínio do plano (veredicto, payload, decisões de ramo) — os
	// chamadores de produção que DEF-272/DEF-273 nomeavam como ausentes. É construído
	// SEMPRE (a via existe e é fenced pela MESMA posse) e EXERCIDO quando há documento
	// aprovado para materializar — ver (3-bis).
	rec, err := runlifecycle.NewPlanRecorder(ten, planoID, eventstore.Producer{NHIID: "nhi:" + *worker})
	if err != nil {
		return fmt.Errorf("emissor do domínio do plano %q: %w", planoID, err)
	}

	// (2) RENOVAÇÃO em segundo plano. Perder a posse PÁRA o trabalho (cancelamento
	// cooperativo) em vez de o deixar a bater no fencing a cada escrita.
	perdida := make(chan error, 1)
	parar := ten.Keep(ctx, leaseTTL/3, func(e error) {
		select {
		case perdida <- e:
		default:
		}
	})
	defer parar()

	// (3) RE-HIDRATAÇÃO. O grafo vem do log; num run novo vem vazio. Quem toma posse
	// não precisa de saber, à partida, se o run é novo — e era essa pergunta, mal
	// respondida, a origem do builder cego (ADR-023 §2.3).
	g, err := ten.Graph(ctx, eventstore.Producer{NHIID: "nhi:" + *worker})
	if err != nil {
		return fmt.Errorf("re-hidratação do grafo: %w", err)
	}
	fmt.Printf("grafo re-hidratado: nos=%d\n", g.DAG().Len())

	// (4) ESCRITA SOB FENCING. Cada AddNode passa pelo FencedAppender.
	for _, id := range separar(*nodes) {
		if g.DAG().Has(id) {
			fmt.Printf("no ja duravel: %s (re-hidratado, nao reescrito)\n", id)
			continue
		}
		if err := g.AddNode(ctx, orchestrator.NodeSpec{TaskID: id}); err != nil {
			return fmt.Errorf("admissão do nó %q: %w", id, err)
		}
		fmt.Printf("no admitido: %s\n", id)
	}

	// (3-bis) MATERIALIZAÇÃO DE UM PLANO APROVADO, COM O ORÁCULO DE EFEITO REAL.
	//
	// É aqui que a segunda metade do DEF-273 deixa de ser uma via por chamar. O
	// oráculo NÃO é passado por este comando: `Tenure.Materializer` deriva-o do
	// snapshot pinado e não aceita substituição — ver o comentário lá. O que este
	// comando fornece é a FONTE do snapshot e o documento aprovado.
	if *planDoc != "" {
		if err := materializar(ctx, ten, rec, *planDoc, *snapshot, *worker); err != nil {
			return err
		}
	}

	select {
	case e := <-perdida:
		return fmt.Errorf("posse perdida a meio: %w", e)
	default:
	}

	// (5) HANDOFF POR ANÚNCIO. É o último acto da posse: a partir daqui as escritas
	// deste processo são recusadas pelo log, antes sequer de haver novo detentor.
	if *release {
		parar()
		if err := ten.Release(ctx); err != nil {
			return fmt.Errorf("anúncio de largar a posse: %w", err)
		}
		fmt.Printf("posse largada: run=%s token=%d (reclamavel JA, sem esperar TTL)\n", ten.RunID(), ten.Token())
	}
	return nil
}

func cmdInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	var sub substrato
	sub.registarFlags(fs)

	runID := fs.String("run", "", "run_id a inspeccionar")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *runID == "" {
		return errors.New("--run é obrigatório")
	}
	ctx := context.Background()
	store, fechar, err := sub.abrirParaLeitura()
	if err != nil {
		return err
	}
	defer func() { _ = fechar() }()

	// Inspeccionar NÃO reclama posse e NÃO escreve: ler não move estado, e o replay é
	// função pura do log (ADR-010).
	dag, err := orchestrator.RebuildDAG(ctx, store, *runID)
	if err != nil {
		return fmt.Errorf("reconstrução do grafo: %w", err)
	}
	ordem, err := dag.TopoOrder()
	if err != nil {
		return fmt.Errorf("ordem topológica: %w", err)
	}
	leases, err := durable.NewLeaseManager(store, leaseTTL)
	if err != nil {
		return err
	}
	tok, err := leases.CurrentToken(ctx, *runID)
	if err != nil {
		return err
	}
	fmt.Printf("run=%s token_corrente=%d nos=%d ordem=%s\n", *runID, tok.Value(), dag.Len(), strings.Join(ordem, ","))
	return nil
}

// separar parte uma lista separada por vírgulas, ignorando entradas vazias.
func separar(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// materializar corre a materialização de um plano APROVADO sob a posse deste run,
// com o ORÁCULO DE EFEITO REAL derivado do snapshot pinado (DEF-273).
//
// # Porque o snapshot é obrigatório aqui
//
// Sem ele não há oráculo real, e sem oráculo real o materializador cai no
// `DefaultEffectOracle` — tudo conta como efeito — e um nó com o papel `verifier`
// materializa com autoridade VAZIA. Aceitar `--plan-doc` sem `--snapshot` seria
// oferecer exactamente o comportamento que esta via existe para eliminar, com o ar de
// estar a fazer a coisa certa. Recusa-se.
//
// O orçamento da árvore é criado aqui com um tecto vindo da linha de comando: um
// tecto real vem do plano de controlo, e este comando não o compõe. É limitação de
// escopo DESTE binário — a admissão em si ([runlifecycle.BudgetAdmission]) é a real,
// com reserva atómica em toda a ancestralidade e saldo por Commit/Release.
func materializar(ctx context.Context, ten *runlifecycle.Tenure, rec *runlifecycle.PlanRecorder, docPath, snapPath, worker string) error {
	if snapPath == "" {
		return errors.New("--plan-doc exige --snapshot: sem o snapshot pinado não há oráculo de efeito real, e o verificador materializaria com autoridade vazia (DEF-273)")
	}
	snap, err := carregarSnapshot(snapPath)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(docPath)
	if err != nil {
		return fmt.Errorf("documento aprovado %q: %w", docPath, err)
	}
	doc, err := plan.Decode(raw)
	if err != nil {
		return fmt.Errorf("documento aprovado %q: %w", docPath, err)
	}

	b, err := budget.New(ten.RunID(), budget.Amount{Tokens: materializeBudgetTokens, CostMicroUSD: materializeBudgetCost})
	if err != nil {
		return fmt.Errorf("orçamento da árvore: %w", err)
	}
	adm, err := runlifecycle.NewBudgetAdmission(b, ten.RunID())
	if err != nil {
		return err
	}

	m, err := ten.Materializer(ctx, snap, rec, adm, recusaSpawn{})
	if err != nil {
		return fmt.Errorf("materializador: %w", err)
	}
	payload, err := m.Materialize(ctx, planmaterialize.Request{
		RunID:          ten.RunID(),
		PlanID:         rec.PlanID(),
		ParentToken:    "nhi:" + worker,
		RootBudgetNode: ten.RunID(),
		Doc:            doc,
	})
	if err != nil {
		// FAIL-CLOSED SEM VAZAR: a materialização é em duas fases e aborta antes de
		// qualquer efeito, mas os nós JÁ admitidos deixaram reservas pendentes. Sem
		// esta devolução, cada tentativa falhada encolhia a árvore até negar tudo.
		if rerr := adm.Release(ctx); rerr != nil {
			return fmt.Errorf("materialização falhou (%w) e a devolução das reservas também: %v", err, rerr)
		}
		return fmt.Errorf("materialização: %w", err)
	}
	if err := adm.Commit(ctx); err != nil {
		return fmt.Errorf("confirmação das reservas de admissão: %w", err)
	}

	fmt.Printf("materializado: plano=%s nos=%d oraculo=snapshot(%s)\n", payload.PlanID, len(payload.Nodes), snap.Hash)
	for _, n := range payload.Nodes {
		fmt.Printf("  no=%s kind=%s tools=%s\n", n.NodeID, n.Kind, strings.Join(n.Tools, "|"))
	}
	return nil
}

// recusaSpawn satisfaz a porta de spawn RECUSANDO. Este comando não compõe o
// Delegator (AOS-026) nem o issuer de NHI filha, e um spawn silenciosamente ignorado
// seria pior do que um recusado: o plano pareceria materializado com sub-agentes que
// não existem. Um documento com papéis-que-expandem falha aqui, em voz alta.
type recusaSpawn struct{}

func (recusaSpawn) Spawn(_ context.Context, req planmaterialize.RoleSpawn) error {
	return fmt.Errorf("spawn de papel %q (nó %q) recusado: este comando não compõe o Delegator (AOS-026) — materializa planos só de folhas", req.Role, req.NodeID)
}

// Tectos do orçamento da árvore usados pela materialização deste comando. Um tecto
// real vem do plano de controlo; aqui são generosos e declarados, para que a admissão
// exercite o caminho de RESERVA sem ser o que decide o desfecho da demonstração.
const (
	materializeBudgetTokens = 1 << 30
	materializeBudgetCost   = 1 << 30
)
