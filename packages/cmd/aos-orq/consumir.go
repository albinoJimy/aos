package main

// consumir.go — QUEM DRENA A FILA DE PEDIDOS DE PLANO.
//
// O `POST /plans` do nó grava um pedido e devolve `201 accepted`. Até aqui, nada o consumia: o
// `201` prometia uma corrida que nunca começava. Este comando é a outra metade (AOS-423,
// ADR-030).
//
// # DRENA UMA VEZ E TERMINA, E ISSO É DELIBERADO
//
// Não é um serviço de longa duração. Reclama, corre, reporta, repete — e termina quando a fila
// não tem mais nada para este consumidor.
//
// A razão era que a FORMA DO TRABALHADOR não estava decidida (ADR-030 §4), e um comando drenável não
// obrigava a decidir: quem o invoca pode ser um timer do host ou um laço de um serviço, com o mesmo
// código. Decidiu-a o dono no AOS-447 (nota ao ADR-030 §4): UM trabalhador, o timer
// `aos-drenar-planos` 1 min depois da drenagem anterior, com `--max 1` — sem fazer deste binário o
// primeiro serviço de longa duração do AOS além do nó.
//
// # A TRADUÇÃO CÓDIGO→CLASSE VIVE AQUI, E NÃO NO NÓ
//
// Só este binário sabe o que os códigos de saída do `serve` significam. Pôr a tabela no nó
// obrigá-lo-ia a conhecer a semântica de saída do orquestrador — que é precisamente a fronteira
// do ADR-018. O nó valida o VOCABULÁRIO de classes; a decisão é daqui.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	planner "github.com/aos-ref/control-plane/orchestrator/planner"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/substrate/eventstore"
)

// maxPedidosPorDrenagem limita quantos pedidos uma invocação consome.
//
// Sem tecto, uma fila grande faria uma invocação correr indefinidamente — e um timer que dispara
// enquanto a invocação anterior ainda corre dá dois consumidores, que é seguro (o `StatusDuplicate`
// arbitra) mas desperdiça. Com tecto, cada invocação termina e a seguinte continua de onde esta
// parou.
const maxPedidosPorDrenagem = 16

// classeDoDesfecho traduz o erro de um `serve` na classe que o nó regista.
//
// A TABELA, e porque a distinção é load-bearing (ADR-030 §2.6): confundir transitório com
// permanente dá um de dois defeitos, e ambos são piores do que a fila parada — um pedido perdido,
// ou um laço a retentar para sempre uma recusa determinista.
//
//	3 exitPosseNegada          TRANSITÓRIO  outro processo detém o lease do RUN; retenta-se
//	4 exitFenced               TRANSITÓRIO  a posse foi superada a meio; retenta-se
//	5 exitWALDetido            TRANSITÓRIO  outro escritor detém o STORE; retenta-se
//	8 exitNosEmVoo             TRANSITÓRIO  o prazo acabou com nós a correr; nova invocação retoma
//	6 exitPendenteDeAprovacao  AGUARDA      o plano espera um humano; nem retentativa nem desfecho —
//	                                        o nó estaciona-o e re-oferece-o, e a re-verificação corre
//	                                        pelo documento, sem modelo (AOS-442)
//	7 exitDecisaoRecusada      TERMINAL     houve decisão e foi NÃO; caso fechado
//	9 exitPlanoRecusado        TERMINAL     o planeador esgotou tentativas; não se retenta
//	10 exitDocumentoRecusado   TERMINAL     documento/snapshot recusado; determinista (AOS-442)
//	0 (sem erro)               TERMINAL     o plano correu
//	1 exitErro                 TRANSITÓRIO  genérico — ver abaixo
//
// O GENÉRICO É TRANSITÓRIO, e é a escolha menos óbvia. Um erro que não soubemos classificar pode
// ser uma configuração má (que se repetirá) ou uma falha de rede (que não). Tratá-lo como
// terminal PERDE o pedido em silêncio, que é o defeito que este eixo existe para fechar; tratá-lo
// como transitório devolve-o à fila, onde fica visível e contável. O tecto de pendentes é o que
// impede isso de virar um laço infinito — e é a razão pela qual o tecto recusa em vez de
// descartar.
func classeDoDesfecho(codigo int) string {
	switch codigo {
	case exitOK, exitDecisaoRecusada, exitPlanoRecusado, exitDocumentoRecusado:
		return "terminal"
	case exitPendenteDeAprovacao:
		return "aguarda_humano"
	default:
		return "transitorio"
	}
}

// cmdConsume drena a fila de pedidos de plano do nó.
//
// O `err` é nomeado porque o ficheiro de métricas (AOS-443) se escreve no `defer`, também quando a
// drenagem aborta — e tem de saber se abortou.
func cmdConsume(args []string) (err error) {
	fs := flag.NewFlagSet("consume", flag.ContinueOnError)
	snapshot := fs.String("snapshot", "", "instantâneo de validação que o planeador consome (o mesmo do `serve --goal`)")
	maxPedidos := fs.Int("max", maxPedidosPorDrenagem, "número máximo de pedidos a consumir nesta invocação")
	planTimeout := fs.Duration("plan-timeout", prazoDoPlanoPorOmissao, "prazo de cada plano, passado ao `serve`")
	pollInterval := fs.Duration("poll-interval", intervaloDeSondagemPorOmissao, "intervalo de sondagem do executor, passado ao `serve`")
	worker := fs.String("worker", "", "identidade deste trabalhador, passada ao `serve`")
	planDir := fs.String("plan-dir", "", "pasta onde fica o documento de cada plano validado, para a retoma correr por --plan-doc em vez de decompor de novo (AOS-442); por omissão, `planos/` ao lado do --wal. Com --nats é obrigatória e tem de ser PARTILHADA entre as réplicas")
	decomposeFixture := fs.String("decompose-fixture", "", "NÃO-PRODUÇÃO: passado ao `serve --goal` (ver `serve -h`), para exercitar a drenagem sem LLM")
	metricsFile := fs.String("metrics-file", "", "ficheiro de métricas em formato de texto Prometheus, reescrito de forma atómica no fim de cada drenagem com os contadores acumulados (AOS-443); por omissão, "+nomeDoFicheiroDeMetricas+" ao lado do --wal. Sem --wal e sem ele, não se escreve")
	var sub substrato
	sub.registarFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *maxPedidos < 1 {
		return errors.New("--max tem de ser positivo")
	}
	// O SUBSTRATO valida-se AQUI e não só dentro de cada `serve`: uma invocação que não sabe
	// escrever em lado nenhum não deve reclamar um pedido do nó — reclamar e falhar a seguir
	// gasta uma geração por nada.
	if err := sub.validar(); err != nil {
		return err
	}
	// A PASTA DOS DOCUMENTOS também, e pela mesma razão (AOS-442): sem ela, a retoma de um plano
	// aprovado não tem por onde correr senão decompor de novo.
	pasta, err := pastaDosPlanos(*planDir, sub)
	if err != nil {
		return err
	}

	// AOS-443 — AS MÉTRICAS ESCREVEM-SE NO FIM, ACONTEÇA O QUE ACONTECER. Daqui para baixo, uma
	// falha é uma drenagem que correu e falhou, e conta como tal; antes disto era configuração, e
	// nada foi tentado. Não escrever o ficheiro FALHA a invocação: é ele que o sensor lê, e um
	// sensor a ler um ficheiro parado diria «tudo bem» sobre uma fila que ninguém vê.
	caminhoMetricas := caminhoDasMetricas(*metricsFile, sub)
	metricas, errLer := &metricasDoConsumo{series: map[string]float64{}}, error(nil)
	if caminhoMetricas != "" {
		metricas, errLer = lerMetricas(caminhoMetricas)
	}
	if errLer != nil {
		fmt.Fprintf(os.Stderr, "aos-orq: metricas anteriores ilegiveis em %s (%v); os contadores recomecam do zero\n",
			caminhoMetricas, errLer)
	}
	tratados := 0
	defer func() {
		if caminhoMetricas == "" {
			return
		}
		resultado := "ok"
		if err != nil {
			resultado = "erro"
		}
		metricas.registarDrenagem(resultado, tratados, time.Now().UTC())
		if errEsc := escreverMetricas(caminhoMetricas, metricas.texto()); errEsc != nil {
			err = errors.Join(err, fmt.Errorf("metricas da drenagem NAO escritas em %s: %w", caminhoMetricas, errEsc))
			return
		}
		fmt.Printf("metricas: %s (drenagem %s)\n", caminhoMetricas, resultado)
	}()

	cli, err := nodeClientDoAmbiente()
	if err != nil {
		return err
	}
	if cli == nil {
		return errors.New("consume exige AOS_ORQ_NODE_URL: a fila vive no nó, e sem o canal para o " +
			"nó não há nada para reclamar")
	}
	if err := os.MkdirAll(pasta, 0o700); err != nil {
		return fmt.Errorf("pasta dos documentos dos planos %q: %w", pasta, err)
	}

	ctx := context.Background()
	// AOS-441 — O SNAPSHOT CONFERE-SE COM O NÓ ANTES DE RECLAMAR. Cada pedido corre um `serve`
	// (por `--goal` ou, na retoma, por `--plan-doc` — AOS-442), e os dois exigem o snapshot; um
	// consume sem ele, ou com um que nomeia tools que o nó não tem, falharia TODOS os pedidos da
	// mesma maneira — e cada falha gastava uma geração. Pela mesma razão do substrato acima: o que
	// já se sabe antes de pedir não se descobre depois.
	if *snapshot == "" {
		return errors.New("consume exige --snapshot: cada pedido corre um `serve` (`--goal` ou `--plan-doc`), que valida o plano contra o snapshot pinado")
	}
	snapConferido, err := conferirSnapshotComONo(ctx, cli, *snapshot)
	if err != nil {
		return err
	}
	// A mesma linha do `serve`: é por ela que o registo da drenagem prova que a conferência correu.
	fmt.Printf("snapshot: %d tool(s) conferida(s) com o catálogo do nó (nome, digest, egress, reversibility) — AOS-441\n", len(snapConferido.Tools))
	consumidos, reverificados := 0, 0
	for consumidos < *maxPedidos && reverificados < maxReverificacoesPorDrenagem {
		pedido, houve, err := cli.ReclamarPedido(ctx)
		if err != nil {
			return fmt.Errorf("reclamar pedido: %w", err)
		}
		if !houve {
			break // fila vazia para este consumidor — o desfecho normal
		}
		// O OBJECTIVO NÃO SE IMPRIME (AOS-443). É dado do titular, selado no nó sob a KEK dele
		// (AOS-429); este stdout vai para o journal e para o log da drenagem, que o apagamento DSAR
		// não alcança. O tamanho chega para distinguir um objectivo vazio de um presente.
		fmt.Printf("reclamado: run=%s geracao=%d objectivo_bytes=%d\n", pedido.RunID, pedido.Geracao, len(pedido.Objective))
		metricas.registarReclamacao(pedido.Geracao)
		tratados++
		inicio := time.Now()

		// AOS-442: por onde o plano entra — o documento validado de uma tentativa anterior, ou a
		// decomposição do objectivo —, decidido pelo LOG do run. Alguns casos são um desfecho sem
		// `serve` (ver retoma_do_plano.go): um plano ainda à espera de humano, um prazo expirado,
		// um documento recusado.
		origem, erroDoServe := origemDoPedido(sub, pasta, pedido.RunID, *decomposeFixture, *snapshot, inicio.UTC())
		serveCorreu := erroDoServe == nil
		if serveCorreu {
			fmt.Printf("origem do plano: run=%s %s\n", pedido.RunID, origem.descrever())
			erroDoServe = correrPedido(*snapshot, pedido, sub, *planTimeout, *pollInterval, *worker, origem)
		}
		codigo, classe, tipo := desfechoDoServe(erroDoServe)
		// AOS-443: o resumo vai TAMBÉM em sucesso — antes, o `detail` só existia com erro, e
		// «terminado com sucesso» e «terminado» diziam o mesmo a quem pergunta pelo plano. Com erro,
		// leva o TIPO do erro e nunca o texto (ver [tipoDoErro]).
		resumo := resumoDoPedido{
			origem:  origemDoResumo(origem, serveCorreu, classe),
			geracao: pedido.Geracao,
			nos:     nosDoDocumento(origem.documento, origem.jaValidado, inicio),
			duracao: time.Since(inicio),
			erro:    tipo,
		}
		detalhe := detalheDoDesfecho(resumo)
		fmt.Printf("desfecho: run=%s codigo=%d classe=%s %s\n", pedido.RunID, codigo, classe, resumo.linha())
		if tipo == "generico" {
			// Um erro que nenhum sentinela classifica é o que mais precisa de diagnóstico, e o tipo
			// não diz nada. O texto vai SÓ para o stderr desta drenagem (journal e log do `aos`, com
			// retenção limitada), nunca para o nó. Resíduo declarado no AOS-443.
			fmt.Fprintf(os.Stderr, "aos-orq: erro nao classificado do serve de %s: %v\n", pedido.RunID, erroDoServe)
		}

		// UMA RE-VERIFICAÇÃO NÃO GASTA O `--max` (AOS-442). O nó re-oferece os pedidos à espera
		// de humano, pelos mais antigos primeiro; se cada verificação contasse, meia dúzia de
		// planos à espera de decisão ocupava todas as drenagens e os pedidos novos nunca corriam.
		// Não há laço: um pedido re-verificado fica estacionado no nó durante o intervalo de
		// re-oferta. O tecto próprio é só um travão.
		if origem.jaValidado && classe == "aguarda_humano" {
			reverificados++
		} else {
			consumidos++
		}

		// O DESFECHO REPORTA-SE SEMPRE, mesmo quando o `serve` falhou. Não reportar deixa o
		// pedido preso até ao TTL da reclamação — meia hora de silêncio por uma falha que já
		// conhecemos.
		if err := reportarEAvisar(ctx, cli, os.Stdout, pedido.RunID, pedido.Geracao, classe, codigo, detalhe); err != nil {
			// Falhar a reportar NÃO é fatal para os pedidos seguintes: o TTL recupera este.
			// Mas é ruidoso de propósito — um consumidor que não consegue reportar está a
			// trabalhar às cegas.
			fmt.Fprintf(os.Stderr, "aos-orq: desfecho de %s NAO reportado (%v); o pedido volta a "+
				"fila quando a reclamacao expirar\n", pedido.RunID, err)
			metricas.registarDesfecho(resumo, classe, codigo, false)
			continue
		}
		metricas.registarDesfecho(resumo, classe, codigo, true)
		// O DOCUMENTO SAI QUANDO O PEDIDO FECHA (AOS-442). É conteúdo em claro — o objectivo
		// derivado, os nós —, fora do alcance do apagamento DSAR; enquanto o pedido está vivo tem
		// de existir (é por ele que a retoma corre), mas depois de um desfecho TERMINAL reportado
		// já não serve a ninguém. Só depois de o nó o ter registado: apagar antes, e falhar o
		// relatório, deixava uma retoma sem documento.
		if classe == "terminal" {
			apagarDocumentoDoPlano(origem.documento)
		}
	}

	if consumidos == 0 && reverificados == 0 {
		fmt.Println("fila vazia: nada a consumir")
	} else {
		fmt.Printf("drenagem terminada: %d pedido(s) consumido(s), %d re-verificado(s) ainda a espera de humano\n",
			consumidos, reverificados)
	}
	return nil
}

// reportadorDeDesfecho é a metade do cliente do nó que [reportarEAvisar] usa — existe para o teste
// do AOS-445 poder provar a ORDEM (reporte, depois aviso) sem levantar um nó.
type reportadorDeDesfecho interface {
	ReportarDesfecho(ctx context.Context, runID string, geracao int, classe string, codigo int, detalhe string) error
}

// prefixoDoAviso abre a linha que o `drenar-planos.sh` recolhe para o outbox dos avisos (AOS-445).
// A forma inteira é [linhaDoAviso]; o TestAOS445ContratoDaLinhaDoAvisoComOsScripts fixa-a dos dois
// lados.
const prefixoDoAviso = "aviso: "

// linhaDoAviso é a linha estável que diz «este plano TERMINOU, e o nó já o sabe» (AOS-445).
//
// Só leva o que o aviso ao operador pode levar: o `run_id` (que o `avisar-planos.sh` pseudonimiza
// antes de sair do servidor), a geração, a classe e o código. Nunca o objectivo, o resultado nem o
// tipo do erro — esses ficam no log da drenagem e no `GET /plans/{id}`.
func linhaDoAviso(runID string, geracao int, classe string, codigo int) string {
	return fmt.Sprintf("%srun=%s geracao=%d classe=%s codigo=%d", prefixoDoAviso, runID, geracao, classe, codigo)
}

// reportarEAvisar reporta o desfecho ao nó e, SÓ DEPOIS de o nó o ter aceitado, imprime a linha
// `aviso:` de um desfecho TERMINAL (AOS-445).
//
// A ORDEM É O CONTRATO. A linha `desfecho:` sai antes do reporte e diz o que o `serve` deu; esta diz
// o que o nó registou. Um reporte falhado devolve o pedido à fila quando a reclamação expirar, e a
// geração seguinte terá o seu desfecho — avisar já seria anunciar um fim que o nó não conhece, e
// possivelmente dois fins para o mesmo plano. Os desfechos que não são terminais (transitório, à
// espera de humano) não avisam: o plano ainda não acabou.
func reportarEAvisar(ctx context.Context, rep reportadorDeDesfecho, out io.Writer, runID string, geracao int, classe string, codigo int, detalhe string) error {
	if err := rep.ReportarDesfecho(ctx, runID, geracao, classe, codigo, detalhe); err != nil {
		return err
	}
	if classe == "terminal" {
		fmt.Fprintln(out, linhaDoAviso(runID, geracao, classe, codigo))
	}
	return nil
}

// apagarDocumentoDoPlano remove o documento de um pedido fechado. Um documento que já não existe
// não é erro; uma falha a apagar é ruidosa mas não pára a drenagem — o pedido está fechado, e o que
// fica é uma cópia a mais que o operador tem de ver.
func apagarDocumentoDoPlano(caminho string) {
	if caminho == "" {
		return
	}
	if err := os.Remove(caminho); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "aos-orq: documento do plano fechado NAO apagado (%v): %s\n", err, caminho)
	}
}

// maxReverificacoesPorDrenagem trava o número de re-verificações de planos à espera de humano
// numa invocação. Não conta para o `--max` — ver o ciclo em [cmdConsume].
const maxReverificacoesPorDrenagem = 64

// descrever diz, numa linha de stdout, por onde o plano entra — é o que o operador procura quando
// uma retoma se comporta de forma inesperada.
func (o origemDoPlano) descrever() string {
	if o.porDocumento {
		return "documento=" + o.documento + " (retoma pelo plano validado; sem decomposicao)"
	}
	return "objectivo (decomposicao; documento a guardar em " + o.documento + ")"
}

// correrPedido corre UM pedido pelo mesmo caminho que um `serve` manual — `--goal` ou `--plan-doc`,
// consoante a [origemDoPlano].
//
// Reutiliza o `cmdServe` em vez de reimplementar o pipeline: o pedido tem de atravessar
// exactamente a mesma governação que uma invocação à mão — posse por lease, decomposição
// governada, gate de aprovação, executor de nós. Um caminho paralelo seria um segundo sítio onde
// a governação podia divergir, que é a forma de defeito que o AOS-424 e o AOS-425 passaram a
// série inteira a encontrar.
func correrPedido(snapshot string, p pedidoReclamado, sub substrato, planTimeout, pollInterval time.Duration, worker string, origem origemDoPlano) error {
	return cmdServe(argsDoServe(snapshot, p, sub, planTimeout, pollInterval, worker, origem))
}

// desfechoDoServe traduz o retorno do `serve` no que se reporta ao nó: código, classe e o tipo do
// erro ([tipoDoErro]; vazio sem erro). Existe como função — e não inline no ciclo — porque foi
// exactamente esta tradução que partiu (AOS-438): o ciclo chamava `codigoDe` com `nil`, e nenhum
// teste passava por aqui.
func desfechoDoServe(erroDoServe error) (codigo int, classe, erro string) {
	codigo = codigoDe(erroDoServe)
	return codigo, classeDoDesfecho(codigo), tipoDoErro(erroDoServe)
}

// tipoDoErro devolve um NOME ESTÁVEL para o erro do `serve` — o sentinela que o classifica —, e
// nunca o texto dele (AOS-443).
//
// O TEXTO NÃO VAI PARA O NÓ. Um erro do `serve` pode citar conteúdo escrito pelo modelo — o
// `plan.Decode` cita com `%q` os `node_id`, o `risk_class` e os campos desconhecidos do documento, e
// o planeador embrulha essas recusas —, e o nó grava o `detail` em claro no stream da fila, fora do
// alcance do `/dsar/erase`. O nome do sentinela diz o que aconteceu sem transportar nada disso. A
// tabela segue a de [codigoDe]; o que ela não conhece é `generico`.
func tipoDoErro(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, eventstore.ErrWALHeld):
		return "wal_detido"
	case errors.Is(err, durable.ErrLeaseHeld):
		return "posse_negada"
	case errors.Is(err, durable.ErrStaleFencingToken),
		errors.Is(err, durable.ErrLeaseSuperseded),
		errors.Is(err, durable.ErrLeaseExpired):
		return "posse_superada"
	case errors.Is(err, errPlanoPendente):
		return "plano_pendente"
	case errors.Is(err, errDecisaoRecusada):
		return "decisao_recusada"
	case errors.Is(err, errNosEmVoo):
		return "nos_em_voo"
	case errors.Is(err, planner.ErrPlanRejected):
		return "plano_recusado_pelo_planeador"
	case errors.Is(err, errDocumentoDoPlanoRecusado):
		return "documento_recusado"
	case errors.Is(err, ErrSnapshotNaoCorresponde):
		return "snapshot_nao_corresponde"
	case errors.Is(err, ErrSnapshotDiferenteDoSelado):
		return "snapshot_diferente_do_selado"
	default:
		return "generico"
	}
}

// argsDoServe monta a invocação do `serve` para um pedido reclamado.
//
// `--release` (AOS-438): um `serve` que acaba bem LARGA a posse por anúncio. Sem isso o lease do run
// ficava vivo até ao TTL, e qualquer nova reclamação do mesmo pedido — uma retoma depois de uma falha
// transitória — batia em «run já tem um lease válido detido» (saída 3) até ele expirar. Medido em
// produção: duas das quatro gerações de plan-e2e-437-1790336067 foram gastas contra o lease da primeira.
//
// A ORIGEM do plano (AOS-442): com documento validado, `--plan-doc` — a retoma não decompõe; sem
// ele, `--goal` e `--plan-out`, para que o documento validado fique guardado para a próxima.
func argsDoServe(snapshot string, p pedidoReclamado, sub substrato, planTimeout, pollInterval time.Duration, worker string, origem origemDoPlano) []string {
	args := []string{"--run", p.RunID}
	switch {
	case origem.porDocumento:
		args = append(args, "--plan-doc", origem.documento)
	default:
		args = append(args, "--goal", p.Objective)
		if origem.documento != "" {
			args = append(args, "--plan-out", origem.documento)
		}
		if origem.decomposeFixture != "" {
			args = append(args, "--decompose-fixture", origem.decomposeFixture)
		}
	}
	args = append(args, "--release")
	if snapshot != "" {
		args = append(args, "--snapshot", snapshot)
	}
	if worker != "" {
		args = append(args, "--worker", worker)
	}
	args = append(args, "--plan-timeout", planTimeout.String(), "--poll-interval", pollInterval.String())
	args = append(args, sub.comoFlags()...)
	return args
}
