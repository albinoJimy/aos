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

	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/runlifecycle"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	audit "github.com/aos-ref/platform/audit"
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
	// exitPendenteDeAprovacao — o plano exige decisão HUMANA e nada foi materializado
	// (AOS-408). NÃO é avaria: é o estado normal de um plano de risco num gate assíncrono.
	// Sem um código próprio, um operador (e um teste de aceitação) não distinguiria «à
	// espera do humano» de «rebentou», e a diferença decide o que fazer a seguir.
	exitPendenteDeAprovacao = 6
	// exitDecisaoRecusada — houve decisão humana e foi NÃO (ou o prazo passou, ou a
	// assinatura não verifica). Distinto do 6: ali espera-se, aqui o caso está fechado.
	exitDecisaoRecusada = 7
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
	// AOS-408: a cerimónia de decisão de um plano pendente. `plans` LÊ (nunca pede posse);
	// `decide` ESCREVE a decisão sob a mesma posse e o mesmo appender fenced do `serve`.
	case "plans":
		err = cmdPlans(os.Args[2:])
	case "decide":
		err = cmdDecide(os.Args[2:])
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
                  [--goal OBJECTIVO --snapshot SNAP.json [--plan-out DOC.json]]
  aos-orq inspect (--wal FICHEIRO | --nats HOST:PORTA) --run ID
  aos-orq plans   (--wal FICHEIRO | --nats HOST:PORTA) --run ID [--ttl DURACAO]
  aos-orq decide  (--wal FICHEIRO | --nats HOST:PORTA) --run ID --plan-doc DOC.json
                  --snapshot SNAP.json --decision approve|reject --approval APROVACAO.json
                  [--approvers APROVADORES.json] [--ttl DURACAO]
                  (o plan_id DERIVA do run: <run>-plan. O lease e do RUN e a escrita e no stream do
                   PLANO; com os dois independentes, dois decide nao seriam arbitrados por lease.)

Substrato (EXCLUSIVO — um ou outro, nunca ambos):
  --wal   Event Store de referencia sobre ficheiro. NAO arbitra entre processos:
          posse SEQUENCIAL, e um segundo «serve» e recusado com 5.
  --nats  Event Store REPLICADO (JetStream). ARBITRA entre processos: N instancias
          em paralelo sao suportadas e o vencedor e decidido pelo LEASE (3).
          [--nats-stream NOME] [--nats-replicas N] [--nats-region REGIAO]

Gate de aprovação de plano (AOS-408): um plano com nós de risco (danger) ou lacuna de
capacidade NAO materializa — fica PENDENTE (saida 6) e a decisao vem por fora, assinada.

Códigos de saída: 0 ok · 1 erro · 3 posse do RUN negada (lease vivo de outro) · 4 posse superada/expirada · 5 WAL (ou AOS_MODEL_AUDIT_PATH) detido por outro ESCRITOR · 6 plano PENDENTE de decisao humana · 7 decisao RECUSADA
`)
}

// largarSePendente ANUNCIA que larga a posse quando o plano ficou PENDENTE de decisão humana
// (AOS-408), e devolve o erro original intacto.
//
// Um pendente não é uma avaria a meio do trabalho: é o fim do trabalho deste processo. Manter o
// lease até expirar bloquearia o `aos-orq decide` — que ESCREVE a decisão no stream do plano e
// precisa da mesma posse — e a barreira do gate ficaria a bloquear-se a si mesma. Uma falha
// verdadeira, em contraste, NÃO larga: aí o lease a expirar é a informação certa (alguém estava a
// trabalhar neste run e caiu).
//
// Um erro a largar é reportado JUNTO do pendente, nunca em vez dele: a causa que interessa ao
// operador é o plano estar à espera de uma decisão.
func largarSePendente(ctx context.Context, ten *runlifecycle.Tenure, parar func(), err error) error {
	// Uma RECUSA também é o fim do trabalho deste processo sobre o run (o plano não vai correr por
	// esta via), e reter a posse bloqueava o passo seguinte do operador com um «posse negada».
	if !errors.Is(err, errPlanoPendente) && !errors.Is(err, errDecisaoRecusada) {
		return err
	}
	if parar != nil {
		parar()
	}
	if rerr := ten.Release(ctx); rerr != nil {
		return fmt.Errorf("%w (e o anúncio de largar a posse falhou: %v)", err, rerr)
	}
	return err
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
	case errors.Is(err, errPlanoPendente):
		return exitPendenteDeAprovacao
	case errors.Is(err, errDecisaoRecusada):
		return exitDecisaoRecusada
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
	snapshot := fs.String("snapshot", "", "ficheiro JSON do snapshot PINADO de capabilities (obrigatório com --plan-doc/--goal: é dele que sai o oráculo de efeito e o validador AOS-231)")
	goal := fs.String("goal", "", "objectivo a decompor num DAG multi-nó pelo Planner governado (F2E-02, AOS-388; exige --snapshot; exclui --nodes/--plan-doc)")
	decomposeFixture := fs.String("decompose-fixture", "", "NÃO-PRODUÇÃO: ficheiro com o PlanDocument que o decompositor-fixture devolve, para exercitar o pipeline do --goal sem LLM até o Model Gateway ser composto (T2-B)")
	planOut := fs.String("plan-out", "", "ficheiro onde escrever o PlanDocument que ficou PENDENTE de aprovação humana (AOS-408): o documento cru não vive no log, e é este ficheiro que o `decide` reapresenta")
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

	// AOS-395: o audit de governação do gateway resolve-se ANTES de tomar posse do run. Um
	// AOS_MODEL_AUDIT_PATH inválido aborta aqui, sem reclamar o lease nem escrever no log —
	// abortar depois da posse deixaria um lease tomado por causa de um erro de config. Um
	// caminho detido por outro processo sai com 5, como o WAL detido. Só se abre quando a
	// decomposição vai DE FACTO pelo gateway: um `serve` sem `--goal`, com fixture ou sem
	// gateway não sela nada, e trancar-lhe o caminho recusaria réplicas `--nats` que partilham
	// o ambiente sem nunca chamarem o modelo (AOS-100). Um erro de config do gateway é
	// reportado pelo ramo do `--goal`, abaixo.
	var govAudit audit.Store
	var govAuditPath string
	if *goal != "" && *decomposeFixture == "" {
		if gw, gwErr := gatewayConfigFromEnv(); gwErr == nil && gw != nil {
			st, caminho, fecharAudit, err := parseModelAuditFromEnv()
			if err != nil {
				return err
			}
			defer func() { _ = fecharAudit() }()
			govAudit, govAuditPath = st, caminho
		}
	}

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
	// AOS-408: a postura do gate de plano declara-se quando ha plano para gatar (--goal ou
	// --plan-doc). Um `serve --nodes` nao passa por gate nenhum e o banner nao se aplica.
	if *goal != "" || *planDoc != "" {
		fmt.Println(bannerDoGateDePlano())
	}

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

	// (4-goal) PIPELINE goal→DAG (F2E-02, AOS-388): com --goal, é o Planner GOVERNADO que
	// produz os nós — mediação RM, reserva CAS, NHI agent:planner e validação AOS-231
	// reais — e o Delegator real materializa (fim do recusaSpawn). Mutuamente exclusivo
	// com o caminho manual --nodes/--plan-doc, que fica como override; a exclusividade
	// torna o laço e a materialização abaixo no-ops quando --goal é usado.
	if *goal != "" {
		if len(separar(*nodes)) > 0 || *planDoc != "" {
			return errors.New("--goal é a fonte dos nós (Planner governado) e não se combina com --nodes/--plan-doc")
		}
		if *snapshot == "" {
			return errors.New("--goal exige --snapshot: o validador (AOS-231) e o oráculo de efeito derivam do snapshot pinado")
		}
		snap, err := carregarSnapshot(*snapshot)
		if err != nil {
			return err
		}
		model, err := modeloDeDecomposicao(*decomposeFixture)
		if err != nil {
			return err
		}
		// AOS-391: sem fixture, a decomposição usa o Model Gateway (LLM vivo) lido do
		// ambiente. Fail-closed: sem fixture E sem gateway não há modelo — o `--goal` recusa
		// em vez de decompor com um modelo-fantasma.
		gwCfg, err := gatewayConfigFromEnv()
		if err != nil {
			return err
		}
		if model == nil && gwCfg == nil {
			return errors.New("--goal exige --decompose-fixture (pipeline offline) OU o Model Gateway (AOS_MODEL_ENDPOINT + AOS_MODEL_NAME); nenhum composto")
		}
		// AOS-395: a postura do audit de governação declara-se quando a decomposição vai de
		// facto pelo gateway (sem fixture) — amarrada ao estado composto, não à intenção.
		if linha := modelAuditPostureBanner(model == nil && gwCfg != nil, govAuditPath); linha != "" {
			fmt.Println(linha)
		}
		if err := decomporEMaterializar(ctx, ten, store, rec, snap, *goal, model, gwCfg, *worker, govAudit, *planOut); err != nil {
			return largarSePendente(ctx, ten, parar, err)
		}
	}

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
		if err := materializar(ctx, ten, store, rec, *planDoc, *snapshot, *worker); err != nil {
			return largarSePendente(ctx, ten, parar, err)
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
func materializar(ctx context.Context, ten *runlifecycle.Tenure, store runlifecycle.EventStore, rec *runlifecycle.PlanRecorder, docPath, snapPath, worker string) error {
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
	// AOS-412: o `--plan-doc` percorre o MESMO caminho que o `--goal`, menos a decomposição.
	//
	// Até aqui era uma via à parte e incompleta: materializava com um token de faz-de-conta
	// (`"nhi:"+worker`), NÃO validava o documento (a regra AOS-231 só corria no `--goal`) e NÃO
	// despachava — os nós ficavam admitidos e pendentes para sempre. Com o modelo vivo isso
	// deixava um plano de risco APROVADO sem forma nenhuma de correr: repetir o `--goal`
	// re-decompõe e produz outro organigrama, e o `--plan-doc`, a via determinística, parava na
	// admissão.
	//
	// (a) validação estrutural — o documento é untrusted, venha de onde vier;
	if err := validarEstrutura(doc, snap); err != nil {
		return err
	}
	// (b) o MESMO gate do `--goal`: um plano sem risco auto-aprova e fica com os factos no log;
	//     um de risco exige a decisão humana DESTE organigrama, sob o mesmo catálogo e no plano do
	//     run da posse (ten.RunID(), nunca derivado do `--plan`);
	hashDoPlano, err := gatearPlano(ctx, pedidoDeGate{
		rec:   rec,
		store: store,
		runID: ten.RunID(),
		doc:   doc,
		snap:  snap,
	})
	if err != nil {
		return err
	}
	// (c) a base de execução REAL (identidade, RM, orçamento) e (d) materializar + despachar.
	b, err := comporBaseDeExecucao(ctx, ten.RunID(), worker, snap)
	if err != nil {
		return err
	}
	return materializarEDespachar(ctx, ten, store, rec, b, snap, doc, hashDoPlano, worker)
}

// NOTA (AOS-390, ADR-024, revista no AOS-412): a MATERIALIZAÇÃO continua admissão-pura — não
// spawna papéis nem arranca folhas; admite os nós no DAG e apensa `plan.materialized`. O efeito
// nasce no DESPACHO governado, que desde o AOS-412 o `--plan-doc` também compõe (antes não
// compunha, e os nós ficavam pendentes para sempre). É a leitura do ADR-024 levada até ao fim:
// efeito no despacho, e não «sem efeito nenhum por esta via».

// Tectos do orçamento da árvore usados pela materialização deste comando. Um tecto
// real vem do plano de controlo; aqui são generosos e declarados, para que a admissão
// exercite o caminho de RESERVA sem ser o que decide o desfecho da demonstração.
const (
	materializeBudgetTokens = 1 << 30
	materializeBudgetCost   = 1 << 30
)
