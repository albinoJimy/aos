package main

// Executor de nós do plano (AOS-413, ADR-027).
//
// O despacho (ADR-024) decide QUE nó arranca; este executor diz o que arrancar É: um run do nó
// `aos`, com as tools pinadas do nó como lista-branca. Depois acompanha-o e, quando o run acaba,
// escreve o que o despacho precisa de ler para avançar:
//
//   - a conclusão do nó (running→complete|failed), durável e sob a posse do run (ADR-023);
//   - o veredicto, quando o nó é um verificador (plan.verdict_recorded), lido da saída final do
//     run por uma gramática FECHADA — tudo o que não se ler é `fail`.
//
//   - os payloads dos contratos cumpridos (AOS-414): publica `plan.payload_published` e entrega
//     a cada nó o que o `consumes` DELE declara, marcado untrusted no prompt do run.
//
// O conteúdo dos payloads vive na memória deste processo em REGIME, e reconstrói-se do log no
// arranque (AOS-418, que emenda a decisão (A) do dono no AOS-414). Originalmente: no
// log fica a referência com o digest. O que NÃO faz: não executa o conteúdo untrusted num plano
// separado do que planeia — a separação de planos (DEF-806/AOS-069) continua aberta.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	orchestrator "github.com/aos-ref/control-plane/orchestrator"
	plan "github.com/aos-ref/control-plane/orchestrator/plan"
	plannerevents "github.com/aos-ref/control-plane/orchestrator/plannerevents"
	runlifecycle "github.com/aos-ref/control-plane/runlifecycle"
	arstate "github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// nodeRunner é o que o executor precisa do nó: submeter e ler o estado. O [nodeClient] é a
// implementação de produção; os testes usam um nó falso.
type nodeRunner interface {
	Submit(ctx context.Context, p pedidoDeRun) error
	Status(ctx context.Context, runID string) (estadoDoRun, bool, error)
}

// Razões de veredicto que o próprio executor atribui (gramática de identificador).
const (
	razaoVeredictoIlegivel = "verdict_unparseable"
)

// instrucaoDeVeredicto vai no fim do objectivo de um verificador: é o formato que o executor lê.
const instrucaoDeVeredicto = "\n\nNo fim, responde APENAS com um objecto JSON, sem mais texto: " +
	`{"outcome":"pass"|"fail","reasons":["<codigo_em_minusculas>"]}` +
	". Qualquer outra resposta conta como fail."

// Prazos do executor. O prazo por omissão fica abaixo da validade do NHI do run (45 min): passado
// ele, as submissões seguintes seriam recusadas pelo nó de qualquer modo.
const (
	prazoDoPlanoPorOmissao        = 40 * time.Minute
	intervaloDeSondagemPorOmissao = 2 * time.Second
)

// errNosEmVoo — o prazo do `serve` acabou com nós ainda a correr (código de saída 8).
var errNosEmVoo = errors.New("aos-orq: prazo do serve esgotado com nos do plano em execucao")

// errNosFalhados — o plano chegou ao fim com pelo menos um nó em `failed` (código de saída 13,
// AOS-484). Decide-se pelo ESTADO DURÁVEL do nó no grafo, e por mais nada: um nó cujo run parou a
// meio, se perdeu (404 persistente) ou cujo `consumes` ficou por cumprir QUANDO IA CORRER está
// `failed`; um verificador que disse `fail` está `complete`, e o ramo que ele reteve fica por
// despachar — o [podarSemPayload] não o fecha, mesmo que o `consumes` dele não se cumpra. Nenhum
// dos dois conta.
//
// CONTA, e é um limite declarado: um nó `failed` cuja falha o plano previa com um ramo
// `terminal_state eq failed`. O ramo de recuperação corre e conclui, e a saída é 13 na mesma — o
// critério olha para o estado dos nós e não pergunta se a falha foi tratada.
var errNosFalhados = errors.New("aos-orq: o plano terminou com nos falhados")

// configDoExecutor é o executor tal como o `serve` o compõe.
type configDoExecutor struct {
	cli      nodeRunner
	prazo    time.Duration
	sondagem time.Duration
	// perdida recebe a perda da posse do run: a espera pára em vez de sondar até ao prazo.
	perdida <-chan error
	// geracaoDoPedido é a geração da reclamação do pedido de plano que este `serve` trabalha
	// (AOS-439, `--plan-request-generation`). > 0 ⇒ cada run filho leva o vínculo ao pedido, e o nó
	// deriva dele o submissor; 0 ⇒ `serve` manual, sem pedido.
	geracaoDoPedido int
	// declararOrigem (AOS-477): o vínculo leva também o plano e o nó, que o nó grava no
	// `run.plan_origin` do run filho. Só com a reclamação de um nó que entregou a referência ao
	// pedido (`request_seq`) — um nó anterior recusaria os campos com 400 (`DisallowUnknownFields`).
	declararOrigem bool
}

// bannerDoExecutor declara no arranque se o trabalho dos nós é executado — e onde.
func bannerDoExecutor(cli *nodeClient) string {
	if cli == nil {
		return "executor de nos (AOS-413): NAO composto — o despacho marca os nos a correr e NADA os executa (defina AOS_ORQ_NODE_URL e o NHI do run em AOS_ORQ_NODE_CREDENTIAL_FILE)"
	}
	chamador := "sem autenticacao do chamador (no sem gate soberano)"
	if cli.bearer != nil {
		// AOS-416 — O QUE ESTA FRASE PROVA, E O QUE NÃO PROVA.
		//
		// O arranque LÊ as duas credenciais montadas, por isso «do ficheiro montado» deixou de
		// ser uma promessa: um ficheiro ilegível já não chega aqui. O que o arranque NÃO prova é
		// autenticação — um segredo legível mas obsoleto (rodado no IdP, ficheiro por
		// actualizar) dá 401 na primeira submissão, e o segredo relê-se a CADA chamada de
		// propósito, para que rodá-lo não exija reiniciar. Por isso a frase diz o mecanismo, não
		// o desfecho.
		chamador = "chamador autenticado pelo IdP (client_credentials), um token por chamada"
	}
	return fmt.Sprintf("executor de nos (AOS-413/AOS-414, ADR-027): COMPOSTO — cada no despachado e um run do no aos em %s (%s; NHI do run do ficheiro montado; tools do no como lista-branca). Os payloads do `consumes` viajam MARCADOS untrusted e RECONSTROEM-SE do log no arranque (AOS-418): a forma fechada vem inteira do evento, a aberta rele-se do run filho e confere-se contra o digest publicado. O que nao se consegue confirmar NAO entra, e o consumidor nao corre — a direccao segura. A separacao de planos (DEF-806) continua aberta: o conteudo e lido pelo mesmo plano que planeia", cli.base, chamador)
}

// resumoDaExecucao é a linha final do `serve` com o executor: o estado de cada nó do plano.
func resumoDaExecucao(g *orchestrator.GraphBuilder, payload plannerevents.MaterializedPayload) string {
	estados := make([]string, 0, len(payload.Nodes))
	for _, n := range payload.Nodes {
		st, _ := g.DAG().State(n.NodeID)
		estados = append(estados, n.NodeID+"="+string(st))
	}
	sort.Strings(estados)
	return "execucao: " + strings.Join(estados, " ")
}

// nosFalhados devolve, por ordem, os nós do plano que estão `failed` no grafo do run (AOS-484).
//
// Lê o MESMO grafo que o [resumoDaExecucao] imprime — re-hidratado do log no arranque e mantido
// pelo `MarkTerminal` desta posse —, pelo que a linha `execucao:` e o código de saída não podem
// dizer coisas diferentes, e uma retoma vê os mesmos nós falhados sem re-executar nenhum.
func nosFalhados(g *orchestrator.GraphBuilder, payload plannerevents.MaterializedPayload) []string {
	var falhados []string
	for _, n := range payload.Nodes {
		if st, ok := g.DAG().State(n.NodeID); ok && st == arstate.Failed {
			falhados = append(falhados, n.NodeID)
		}
	}
	sort.Strings(falhados)
	return falhados
}

// separadorDoRunFilho separa o run do nó do plano no id do run filho. Não é `/` (o `/runs/{id}`
// do nó casa um só segmento) nem `.` ou `:` — que a gramática de node_id admite, e com os quais
// o run `a` + nó `b.c` e o run `a.b` + nó `c` davam o mesmo id. O `~` não é um carácter de
// node_id, e o `serve` recusa um run_id que o contenha: a decomposição é única.
const separadorDoRunFilho = "~"

// marcaDeEscape é o carácter que introduz uma sequência escapada no id do run filho.
//
// # PORQUE É `+`, E PORQUE NÃO É `_`
//
// A primeira versão usou `_`, que PERTENCE à gramática do `node_id`. O próprio teste de
// injectividade a apanhou: com o atalho de «não há nada a escapar, devolve intacto», o
// `node_id` `a_2eb` atravessava tal e qual, e o `node_id` `a.b` escapava PARA `a_2eb` — dois nós
// do plano no MESMO run filho, e portanto no mesmo stream. A colisão exacta que este desenho
// existe para evitar.
//
// Escapar a marca sempre (`_` → `__`) resolveria a injectividade, mas mudaria todos os ids cujo
// `node_id` tem sublinhado — que a gramática admite e são comuns.
//
// `+` NÃO pertence à gramática do `node_id` (`[A-Za-z0-9_.:-]`), pelo que um `node_id` válido
// nunca o contém e nunca é tocado. Continua a escapar-se a si mesmo (`+` → `++`), porque o
// `Decode` do documento de plano NÃO impõe a gramática — ela é invariante semântica, verificada
// pelo AOS-231 — e um `node_id` fora dela pode chegar aqui.
//
// E é representável onde tem de ser: o `subjectDe` só recusa `. * >` e espaço em branco, e num
// segmento de caminho de URL o `+` é literal — ao contrário do `%`, que iniciaria uma sequência
// percent-encoded e partiria o `GET /runs/{id}` com que o executor consulta o estado do filho.
const marcaDeEscape = '+'

// escaparParaStream torna um `node_id` seguro para entrar num `stream_id`, de forma INJECTIVA.
//
// # PORQUE É QUE ISTO EXISTE (AOS-424, ADR-029 §2.4)
//
// O `run_id` de um run **é** o seu `stream_id` no Event Store, e um `stream_id` não pode conter
// `.`, `*`, `>` nem espaço em branco — o backend replicado recusa-os, porque um subject NATS
// não os representa. Mas a gramática do `node_id` (`plan.ValidNodeID`, AOS-231) admite `.` e
// `:`, e o prompt de decomposição em vigor diz ao modelo, por escrito, que os pode usar.
//
// Sem escape, um nó de plano chamado `analise.dados` produzia o run filho
// `run-x~analise.dados` — um `stream_id` com ponto, que funciona sobre o substrato de ficheiro
// e falha sobre JetStream. Era esse o conflito de invariantes que bloqueava o aperto do
// contrato do Event Store: dois invariantes registados, um pelo AOS-231 e outro pelo ADR-007.
//
// Das duas saídas que o ADR-029 §3 deixou em aberto, esta é a que **não mexe no que o planeador
// pode emitir**: o `node_id` continua a poder ter pontos, e quem paga é a legibilidade do id do
// run filho.
//
// # PORQUE É QUE É INJECTIVO, E NÃO SÓ «substituir os maus»
//
// Substituir `.` por `-` faria os nós `a.b` e `a-b` colidirem no MESMO run filho — dois nós do
// plano a escrever no mesmo stream. É a mesma razão pela qual o `subjectDe` recusa em vez de
// escapar: aproximar um nome cria colisões silenciosas.
//
// A marca escapa-se a si mesma (`_` → `__`), o que torna a codificação reversível e, portanto,
// injectiva. Ninguém a reverte hoje — procurou-se, e o separador só aparece numa guarda —, mas
// a reversibilidade é o que **prova** que não há colisões, e deixa um humano descodificar um id
// num log.
//
// A decisão do que escapar vem de [eventstore.CaractereNaoRepresentavel], o PREDICADO da regra:
// se a regra apertar, isto aperta com ela sem ninguém se lembrar.
//
// Dizia «vem de [eventstore.CaracteresNaoRepresentaveis]» — a CONSTANTE —, e isso deixou de ser
// verdade quando a regra passou a apanhar a classe dos caracteres de controlo: a constante virou
// um subconjunto próprio dela. A afirmação sobreviveu à mudança e teria enganado o próximo
// leitor. Ver [deveEscapar].
func escaparParaStream(nodeID string) string {
	precisa := false
	for i := 0; i < len(nodeID); i++ {
		// A MARCA CONTA. Sem isto, o atalho devolvia intacto um `node_id` que JÁ contivesse a
		// marca, e ele colidia com o escape de outro — foi assim que a primeira versão deixou
		// `a.b` e `a_2eb` no mesmo run filho.
		if deveEscapar(nodeID[i]) || nodeID[i] == marcaDeEscape {
			precisa = true
			break
		}
	}
	if !precisa {
		// O caso esmagadoramente comum: um `node_id` como `pesquisa` ou `n1` atravessa
		// intacto, e o id do run filho continua a ser exactamente o que era.
		return nodeID
	}
	var b strings.Builder
	b.Grow(len(nodeID) + 8)
	for i := 0; i < len(nodeID); i++ {
		c := nodeID[i]
		switch {
		case c == marcaDeEscape:
			b.WriteByte(marcaDeEscape)
			b.WriteByte(marcaDeEscape)
		case deveEscapar(c):
			b.WriteByte(marcaDeEscape)
			b.WriteString(hex.EncodeToString([]byte{c}))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// deveEscapar diz se um byte não pode aparecer cru num `stream_id`.
//
// Inclui o `~` — que a gramática do `node_id` NÃO admite, mas que o `Decode` do documento de
// plano não impõe (a gramática é invariante semântica, verificada pelo AOS-231). Se um
// `node_id` com `~` chegasse aqui, a decomposição `<run>~<nó>` deixaria de ser única; escapá-lo
// custa nada e fecha-o.
//
// DECIDE PELO PREDICADO DA REGRA, e não pela constante. Lia
// [eventstore.CaracteresNaoRepresentaveis], que é um SUBCONJUNTO PRÓPRIO do que a regra recusa
// desde que ela passou a apanhar a classe dos caracteres de controlo: um `node_id` com um
// `\x01` atravessava intacto e produzia um `stream_id` que o `Append` recusa — o run do nó
// nunca arrancaria, e a mensagem falaria do id do run filho e não do `node_id`.
//
// Estava tapado pela gramática do `node_id` ter charset fechado, isto é, POR ACASO. É assim que
// a classe inteira do AOS-424 sobreviveu dez gates; as duas pontas decidem agora pela mesma
// função.
func deveEscapar(c byte) bool {
	return eventstore.CaractereNaoRepresentavel(rune(c)) || c == separadorDoRunFilho[0]
}

// childRunID é o id do run do nó `aos` que faz o trabalho de um nó do plano.
//
// O `node_id` vai ESCAPADO (ver [escaparParaStream]): o id resultante é sempre um `stream_id`
// válido, em qualquer substrato. Um `node_id` sem caracteres problemáticos — o caso comum —
// atravessa intacto, pelo que os ids que já existiam não mudam.
//
// COMPATIBILIDADE, declarada: um plano EM VOO no momento do deploy, cujo `node_id` contenha um
// carácter escapável, passa a computar um id diferente — e o executor perde o rasto ao run
// filho que já tinha submetido (consulta o estado pelo id novo e não o encontra). A janela é
// pequena, porque um `serve` possui um run e termina, mas existe. Planos com `node_id` sem
// pontos (todas as fixtures da árvore) não são afectados de todo.
func childRunID(runID, nodeID string) string {
	return runID + separadorDoRunFilho + escaparParaStream(nodeID)
}

// executorDeNos acompanha os runs filhos de UM plano, sob a posse do run.
type executorDeNos struct {
	cli      nodeRunner
	rec      *runlifecycle.PlanRecorder
	g        *orchestrator.GraphBuilder
	runID    string
	nos      map[string]plan.Node // o documento aprovado, por node_id
	tools    map[string][]string  // as tools pinadas de cada nó, do plan.materialized
	headroom *boundedHeadroom
	// geracaoDoPedido — ver [configDoExecutor.geracaoDoPedido] (AOS-439).
	geracaoDoPedido int
	// declararOrigem — ver [configDoExecutor.declararOrigem] (AOS-477).
	declararOrigem bool
	// emVoo são os nós cujo run foi submetido e ainda não foi recolhido.
	emVoo map[string]struct{}
	// sumidos marca desde quando um nó em voo responde 404, e agora dá o relógio.
	sumidos map[string]time.Time
	agora   func() time.Time
	// payloads é o conteúdo publicado por cada contrato cumprido, guardado EM MEMÓRIA
	// enquanto este `serve` vive (AOS-414, opção (A) do dono). No log fica a referência com o
	// digest; o conteúdo não entra no WAL do orquestrador, que não tem a cifra por-titular do
	// nó. O custo, declarado: um `serve` que morra perde-o, e a retoma recusa-se a correr um
	// consumidor sem o material — alto, em vez de o correr às cegas.
	payloads map[chaveDePayload]string
	// rehidratados conta os payloads reconstruídos do log neste arranque (AOS-418), e é
	// impresso no fim da reidratação — não no banner do executor, que é escrito muito antes de
	// o executor existir.
	rehidratados int
}

// chaveDePayload identifica um contrato cumprido: (produtor, output).
type chaveDePayload struct{ no, output string }

// ErrPayloadPerdido — um consumidor precisa de um payload que este processo não tem. Ou o
// produtor concluiu noutro `serve` (o conteúdo vive na memória deste), ou o contrato não chegou a
// ser publicável (`metrics` sem fonte, saída acima do tecto, dois contratos abertos no mesmo nó).
// Em qualquer dos casos o nó NÃO corre — mas quem falha é o NÓ, não o `serve`: ver [podarSemPayload].
var ErrPayloadPerdido = errors.New("aos-orq: contrato de entrada por cumprir (AOS-414)")

// maxPayloadBytes é o tecto do conteúdo de UM payload no produtor. O nó impõe o mesmo por
// payload, e o corpo do `POST /runs` tem o seu tecto (1 MiB): uma saída maior do que isto não se
// transporta, e o contrato fica POR CUMPRIR — o consumidor não corre, em vez de receber metade.
const maxPayloadBytes = 128 << 10

// prazoDeRehidratacao limita o arranque quando os payloads de forma aberta têm de ser relidos do
// nó. Esgotá-lo não é erro: os que não voltaram ficam por cumprir e os consumidores respectivos
// falham, que é o mesmo desfecho de não os ter (AOS-418).
const prazoDeRehidratacao = 2 * time.Minute

// toleranciaA404 é quanto tempo um run em voo pode responder 404 antes de contar como perdido.
// Um 404 não é, por si, a morte do run: o nó responde 404 a um run durável `running` que não está
// na memória DESTE processo — outra réplica, ou a janela antes de a retoma de arranque o voltar a
// hospedar. Marcá-lo `failed` à primeira era dar por morto trabalho que continua.
const toleranciaA404 = 2 * time.Minute

// novoExecutorDeNos compõe o executor E reidrata os payloads dos contratos já cumpridos.
//
// # PORQUE É QUE A REIDRATAÇÃO ESTÁ AQUI E NÃO NO WIRING
//
// Esteve no wiring, e a revisão adversarial mostrou o preço: tirar as três linhas que a chamavam
// deixava a suite INTEIRA verde, porque nada no pacote exercita `composeEDespachar`. Um passo que
// se pode esquecer sem nenhum teste dar por isso não é um passo — é uma sugestão. Aqui, esquecê-lo
// exige apagá-lo de dentro do construtor, e aí os testes de unidade ficam vermelhos.
func novoExecutorDeNos(ctx context.Context, cli nodeRunner, rec *runlifecycle.PlanRecorder, g *orchestrator.GraphBuilder, runID string,
	doc plan.PlanDocument, pinadas map[string][]string, headroom *boundedHeadroom,
	store runlifecycle.EventStore, planID string) (*executorDeNos, error) {
	nos := make(map[string]plan.Node, len(doc.Nodes))
	for _, n := range doc.Nodes {
		nos[n.NodeID] = n
	}
	e := &executorDeNos{cli: cli, rec: rec, g: g, runID: runID, nos: nos, tools: pinadas, headroom: headroom,
		emVoo: map[string]struct{}{}, sumidos: map[string]time.Time{}, agora: time.Now,
		payloads: map[chaveDePayload]string{}}
	if store != nil && planID != "" {
		if err := e.rehidratarPayloads(ctx, store, planID); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// retomar põe em voo os nós que um `serve` anterior deixou `running`, e reserva-lhes o headroom
// (o semáforo é em memória e nasceu vazio com este processo).
func (e *executorDeNos) retomar(ctx context.Context, running []string) error {
	for _, id := range running {
		if _, ok := e.nos[id]; !ok {
			continue
		}
		if ok, err := e.headroom.Acquire(ctx); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("retoma: o tecto de concorrência (%d) não chega para os nós já em execução", e.headroom.max)
		}
		e.emVoo[id] = struct{}{}
	}
	return nil
}

// submeter pede ao nó o run do nó do plano. Idempotente (ver [nodeClient.Submit]).
func (e *executorDeNos) submeter(ctx context.Context, nodeID string) error {
	n, ok := e.nos[nodeID]
	if !ok {
		return fmt.Errorf("executor: nó %q fora do documento aprovado", nodeID)
	}
	objectivo := n.Objective
	if n.IsVerifier() {
		objectivo += instrucaoDeVeredicto
	}
	entradas, err := e.entradasDe(n)
	if err != nil {
		return err
	}
	p := pedidoDeRun{
		RunID:     childRunID(e.runID, nodeID),
		Objective: objectivo,
		Tools:     nomesDasTools(e.tools[nodeID]),
		Inputs:    entradas,
	}
	// AOS-439: o vínculo ao pedido — de que plano, e de que geração da reclamação, este run é
	// trabalho. Não diz quem é o submissor: o nó lê-o do seu log, e só se a reclamação viva for
	// deste chamador.
	if e.geracaoDoPedido > 0 {
		p.PlanRequest = &vinculoAoPedido{RunID: e.runID, Geracao: e.geracaoDoPedido}
		// AOS-477: e de que plano e de que nó — num CAMPO, para o run filho o declarar sem que
		// ninguém tenha de partir o id pelo `~`.
		if e.declararOrigem {
			p.PlanRequest.PlanID, p.PlanRequest.NodeID = e.rec.PlanID(), nodeID
		}
	}
	if err := e.cli.Submit(ctx, p); err != nil {
		return err
	}
	e.emVoo[nodeID] = struct{}{}
	return nil
}

// entradasDe reúne os payloads que o `consumes` DESTE nó declara — nunca o que o produtor
// quis dar. Um contrato por cumprir é fail-closed: o nó não corre sem o material.
func (e *executorDeNos) entradasDe(n plan.Node) ([]entradaDoNo, error) {
	if len(n.Consumes) == 0 {
		return nil, nil
	}
	entradas := make([]entradaDoNo, 0, len(n.Consumes))
	for _, c := range n.Consumes {
		conteudo, ok := e.payloads[chaveDePayload{no: c.From, output: c.Output}]
		if !ok {
			return nil, fmt.Errorf("%w: o no %q consome %q/%q", ErrPayloadPerdido, n.NodeID, c.From, c.Output)
		}
		entradas = append(entradas, entradaDoNo{
			From: c.From, Output: c.Output, Digest: digestDoConteudo(conteudo), Content: conteudo,
		})
	}
	return entradas, nil
}

// podarSemPayload fecha, ANTES do despacho, os nós cujo `consumes` já não pode ser cumprido: o
// produtor está terminal e o payload não está em memória. Sem isto, o nó era despachado, o sink
// recusava e a passagem ABORTAVA — deixando os irmãos em voo por recolher e o `serve` a repetir o
// mesmo erro em todas as retomas. Falha o NÓ (durável, com razão visível) e o plano segue: os
// dependentes são podados pelas regras normais do despacho.
//
// SÓ SE FECHA UM NÓ QUE IA MESMO CORRER (AOS-484). Um nó atrás de um ramo condicional por decidir
// ou NÃO tomado não corre, logo não lhe falta payload nenhum: fechá-lo como `failed` punha no log
// uma falha que não aconteceu — e, desde que um nó `failed` dá a saída 13, fazia um veredicto
// `fail` que retém um ramo sair como um plano falhado. Ver [ramosRetidos].
func (e *executorDeNos) podarSemPayload(ctx context.Context) error {
	retidos, err := e.ramosRetidos(ctx)
	if err != nil {
		return err
	}
	for id, n := range e.nos {
		if len(n.Consumes) == 0 {
			continue
		}
		if st, ok := e.g.DAG().State(id); !ok || st != arstate.Ready {
			continue
		}
		if retidos[id] {
			continue
		}
		for _, c := range n.Consumes {
			if _, temos := e.payloads[chaveDePayload{no: c.From, output: c.Output}]; temos {
				continue
			}
			pst, ok := e.g.DAG().State(c.From)
			if !ok || (pst != arstate.Complete && pst != arstate.Failed) {
				continue // o produtor ainda pode publicar
			}
			if err := e.fecharSemPayload(ctx, id, c); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

// contratoPorCumprir devolve o primeiro `consumes` do nó cujo payload este processo não tem — e
// por isso não pode entregar (AOS-484). É a mesma pergunta que o [entradasDe] faz ao montar o
// pedido, feita ANTES de qualquer efeito: o sink chama-a antes do `Spawn` de um papel e antes de
// submeter o run.
func (e *executorDeNos) contratoPorCumprir(nodeID string) (plan.PayloadEdge, bool) {
	n, ok := e.nos[nodeID]
	if !ok {
		return plan.PayloadEdge{}, false
	}
	for _, c := range n.Consumes {
		if _, temos := e.payloads[chaveDePayload{no: c.From, output: c.Output}]; !temos {
			return c, true
		}
	}
	return plan.PayloadEdge{}, false
}

// fecharSemPayload fecha como `failed`, durável e sob a posse, um nó que não corre porque o
// contrato `c` do seu `consumes` ficou por cumprir. É o ÚNICO caminho por onde isso acontece, e
// têm-no dois chamadores: a poda do início da passagem ([podarSemPayload]) e o sink, quando o
// despacho decide o ramo de um nó na mesma passagem em que ele fica elegível. As duas transições
// são as do grafo (`MarkRunning` + `MarkTerminal`), pelo que uma retoma re-hidrata o nó já
// `failed` e nenhum dos dois o volta a tocar — ambos só olham para nós `ready`.
func (e *executorDeNos) fecharSemPayload(ctx context.Context, id string, c plan.PayloadEdge) error {
	fmt.Printf("  execucao: no %s NAO corre — o contrato %s/%s ficou por cumprir\n", id, c.From, c.Output)
	if err := e.g.MarkRunning(ctx, id); err != nil {
		return fmt.Errorf("marcar %q a correr para o fechar: %w", id, err)
	}
	if err := e.g.MarkTerminal(ctx, id, arstate.Failed); err != nil {
		return fmt.Errorf("fechar %q sem payload: %w", id, err)
	}
	return nil
}

// ramosRetidos devolve os nós que NÃO vão correr nesta passagem por causa de um ramo condicional
// (AOS-484): os que têm `conditional_on` sem decisão registada ou com a decisão «não tomado», e
// toda a descendência deles por qualquer dos dois canais de aresta — a mesma propagação que o
// despacho faz (`plandispatch.propagateNotTaken`).
//
// A fonte é o FACTO `plan.branch_decided` do log, lido pela mesma porta que o despacho usa, e não
// uma segunda avaliação das condições: quem decide o ramo continua a ser o despachante. Um ramo que
// o despacho decida «tomado» nesta passagem só aparece aqui na seguinte; um nó assim, sem o
// payload, é fechado pelo sink (ver `dispatchSink.Dispatch`), antes de qualquer efeito.
//
// Um nó que já arrancou (a correr, concluído ou falhado) não está retido: o ramo dele foi tomado.
// Um plano sem arestas condicionais não lê o log.
func (e *executorDeNos) ramosRetidos(ctx context.Context) (map[string]bool, error) {
	haCondicionais := false
	for _, n := range e.nos {
		if len(n.ConditionalOn) > 0 {
			haCondicionais = true
			break
		}
	}
	if !haCondicionais {
		return nil, nil
	}
	decisoes, err := e.rec.BranchJournal().Decisions(ctx, e.rec.PlanID())
	if err != nil {
		return nil, fmt.Errorf("decisoes de ramo do plano %q: %w", e.rec.PlanID(), err)
	}
	memo := make(map[string]bool, len(e.nos))
	var retido func(id string) bool
	retido = func(id string) bool {
		if v, visto := memo[id]; visto {
			return v
		}
		memo[id] = false // o grafo é acíclico (AOS-231); isto só trava um documento que não o seja
		n, ok := e.nos[id]
		if !ok {
			return false
		}
		if st, ok := e.g.DAG().State(id); ok && st != arstate.Ready {
			return false
		}
		r := false
		if len(n.ConditionalOn) > 0 {
			d, decidido := decisoes[id]
			r = !decidido || !d.Taken
		}
		for _, origem := range n.IncomingEdges() {
			if r {
				break
			}
			r = retido(origem)
		}
		memo[id] = r
		return r
	}
	for id := range e.nos {
		retido(id)
	}
	return memo, nil
}

// digestDoConteudo é o `sha256:<hex>` do conteúdo. O nó reverifica-o: é um controlo de
// INTEGRIDADE do transporte entre este processo e o run — não uma prova de origem (quem calcula
// e quem envia são o mesmo processo) nem confiança no conteúdo, que é untrusted de qualquer modo.
func digestDoConteudo(conteudo string) string {
	soma := sha256.Sum256([]byte(conteudo))
	return "sha256:" + hex.EncodeToString(soma[:])
}

// publicarSaidas cumpre os contratos de saída do nó que acabou de concluir (AOS-414): guarda o
// conteúdo em memória para os consumidores e apensa `plan.payload_published` com a REFERÊNCIA
// (forma aberta: run filho + digest) ou com a forma FECHADA validada (o veredicto).
//
// O que não se consegue derivar não se publica: um contrato `metrics` exigiria números que
// ninguém mediu, e inventá-los seria pior do que o contrato ficar por cumprir.
func (e *executorDeNos) publicarSaidas(ctx context.Context, n plan.Node, st estadoDoRun, v *plannerevents.VerdictRecordedPayload) error {
	abertos := 0
	for _, c := range n.Outputs {
		if !c.Type.ClosedForm() {
			abertos++
		}
	}
	for _, contrato := range n.Outputs {
		p := plannerevents.PayloadPublishedPayload{NodeID: n.NodeID, Output: contrato.Name}
		var conteudo string
		switch {
		case contrato.Type == plan.PayloadVerdict && v != nil:
			// Forma fechada: viaja inline e validada pelo construtor. O conteúdo que o
			// consumidor recebe é a forma canónica do veredicto, não texto do modelo.
			p.Closed = &plannerevents.ClosedPayload{Outcome: v.Outcome, Reasons: v.Reasons}
			bruto, err := conteudoFechado(v.Outcome, v.Reasons)
			if err != nil {
				return err
			}
			conteudo = bruto
		case contrato.Type.ClosedForm():
			// `metrics` (ou um veredicto que não se conseguiu ler): sem fonte, não se publica.
			continue
		case abertos > 1:
			// DOIS contratos de forma aberta no mesmo nó: um run devolve UMA saída final, e
			// atribuí-la aos dois publicaria bytes iguais sob nomes diferentes — o tipo que o
			// validador impõe na admissão não significaria nada na entrega.
			continue
		case len(st.FinalText) > maxPayloadBytes:
			continue
		default:
			conteudo = st.FinalText
			p.Record = plannerevents.PayloadRecordRef{
				Store:  plannerevents.PayloadStoreEventStore,
				Stream: childRunID(e.runID, n.NodeID),
				Digest: digestDoConteudo(conteudo),
			}
		}
		if _, err := e.rec.RecordPayloadPublished(ctx, p, n); err != nil {
			return fmt.Errorf("publicacao de %q/%q: %w", n.NodeID, contrato.Name, err)
		}
		e.payloads[chaveDePayload{no: n.NodeID, output: contrato.Name}] = conteudo
		fmt.Printf("  execucao: payload %s/%s publicado (%s)\n", n.NodeID, contrato.Name, contrato.Type)
	}
	return nil
}

// conteudoFechado é a forma canónica de um veredicto como PAYLOAD.
//
// Vive numa função porque é calculada em DOIS momentos — na publicação e na reidratação de
// AOS-418 — e duas expressões equivalentes hoje divergem amanhã em silêncio: o consumidor
// receberia bytes diferentes conforme o processo tivesse ou não reiniciado, e nada o diria.
//
// # PORQUE É QUE ELA NORMALIZA, E NÃO SÓ FORMATA
//
// Uma função partilhada fecha o eixo da EXPRESSÃO e não o das ENTRADAS, e foi por aí que a
// primeira versão deste código divergiu: a publicação passava-lhe as razões CRUAS
// (`veredictoDaSaida`) e a reidratação passava-lhe as razões do evento, que o
// `plannerevents.normalizeClosed` já reduziu — em particular, uma lista VAZIA vira `nil` porque o
// campo é `omitempty`. Resultado medido: publicado `{"outcome":"pass","reasons":[]}`, reidratado
// `{"outcome":"pass","reasons":null}` — para um verificador que responda `reasons: []`, que a
// gramática fechada aceita. Reduzir aqui torna a função TOTAL sobre as duas entradas.
func conteudoFechado(outcome plannerevents.VerdictOutcome, reasons []string) (string, error) {
	if len(reasons) == 0 {
		reasons = nil
	}
	bruto, err := json.Marshal(map[string]any{"outcome": outcome, "reasons": reasons})
	if err != nil {
		return "", err
	}
	return string(bruto), nil
}

// rehidratarPayloads reconstrói o conteúdo dos contratos já cumpridos a partir do LOG, para que
// um `serve` que morra a meio de um plano não leve os payloads com ele (AOS-418).
//
// # PORQUE É QUE ISTO NÃO PRECISA DE EVENTO NOVO
//
// O `plan.payload_published` já carrega o suficiente, e de duas formas diferentes:
//
//   - FECHADA (veredicto): o conteúdo está INTEIRO no evento (`Closed`). Reconstrói-se pela mesma
//     função canónica que o publicou.
//   - ABERTA: o evento carrega a REFERÊNCIA durável (o run filho que produziu a saída) e o
//     DIGEST. O conteúdo relê-se do run filho pelo nó, e o digest do evento diz se o que voltou é
//     o mesmo que foi publicado.
//
// # FAIL-CLOSED, E PORQUÊ
//
// Um payload que não se consiga reconstruir NÃO entra no mapa: o consumidor falha depois com
// [ErrPayloadPerdido], que é o comportamento de hoje. A alternativa — entregar o que voltou sem
// conferir o digest — daria ao consumidor bytes que ninguém publicou, e a marca `untrusted` do
// AOS-414 protege a FRONTEIRA, não a identidade do conteúdo.
func (e *executorDeNos) rehidratarPayloads(ctx context.Context, store runlifecycle.EventStore, planID string) error {
	// A reidratação tem PRAZO. Cada payload de forma aberta é uma chamada ao nó, sequencial, e o
	// `ctx` que chega aqui não traz deadline: com o nó indisponível, um plano com muitos
	// produtores dava minutos de arranque mudo — e o `--plan-timeout` só começa a contar depois.
	ctx, cancelar := context.WithTimeout(ctx, prazoDeRehidratacao)
	defer cancelar()

	vistos := 0
	eventos, err := store.Read(ctx, planID, 0)
	if err != nil {
		return fmt.Errorf("rehidratar payloads: ler o stream do plano %q: %w", planID, err)
	}
	for _, ev := range eventos {
		if ev.Type != plannerevents.EventPayloadPublished {
			continue
		}
		var p plannerevents.PayloadPublishedPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			// NÃO aborta. O evento não desaparece de um log append-only: abortar aqui repetia-se
			// em TODAS as retomas e trancava o plano para sempre por linha de comando. É a mesma
			// regra que `fechar` aplica a um veredicto ilegível — o payload fica por cumprir, o
			// consumidor falha, e o resto do plano segue.
			fmt.Printf("  execucao: %s ILEGIVEL no plano %s (ignorado; o consumidor falha se precisar dele): %v\n", plannerevents.EventPayloadPublished, planID, err)
			continue
		}
		vistos++
		chave := chaveDePayload{no: p.NodeID, output: p.Output}
		if _, ja := e.payloads[chave]; ja {
			continue
		}
		switch {
		case p.Closed != nil:
			conteudo, err := conteudoFechado(p.Closed.Outcome, p.Closed.Reasons)
			if err != nil {
				return fmt.Errorf("rehidratar payloads: forma fechada de %q/%q: %w", p.NodeID, p.Output, err)
			}
			e.payloads[chave] = conteudo
			e.rehidratados++
		case p.Record.Stream != "":
			conteudo, ok := e.relerDoRunFilho(ctx, p)
			if !ok {
				continue
			}
			e.payloads[chave] = conteudo
			e.rehidratados++
		}
	}
	if vistos > 0 {
		// Imprime-se TAMBÉM com zero reconstruídos: um plano a meio cujos payloads não voltaram
		// é precisamente o que o operador tem de ver, e o silêncio dizia-lhe o contrário.
		fmt.Printf("  execucao: %d de %d payload(s) reconstruido(s) do log (AOS-418)\n", e.rehidratados, vistos)
	}
	return nil
}

// relerDoRunFilho relê a saída de um run filho e confirma-a contra o digest do evento. Devolve
// (conteudo, true) só quando o que voltou é byte a byte o que foi publicado.
func (e *executorDeNos) relerDoRunFilho(ctx context.Context, p plannerevents.PayloadPublishedPayload) (string, bool) {
	st, existe, err := e.cli.Status(ctx, p.Record.Stream)
	if err != nil {
		fmt.Printf("  execucao: payload %s/%s NAO rehidratado (run filho %s ilegivel: %v)\n", p.NodeID, p.Output, p.Record.Stream, err)
		return "", false
	}
	if !existe {
		// O nó já não conhece o run filho. A saída existiu, mas não há de onde a reler — e
		// inventá-la não é opção.
		fmt.Printf("  execucao: payload %s/%s NAO rehidratado (o no ja nao conhece o run filho %s)\n", p.NodeID, p.Output, p.Record.Stream)
		return "", false
	}
	if st.FinalText == "" {
		// O CASO PROVÁVEL, e não o da adulteração. O `final_text` do nó vem de um registo de
		// desfechos EM MEMÓRIA, com poda FIFO: um nó reiniciado responde `completed` sem texto.
		// Dizer «o digest não bate» aqui seria acusar substituição onde o facto é «o nó já não
		// retém a saída» — o diagnóstico errado no caso mais frequente.
		fmt.Printf("  execucao: payload %s/%s NAO rehidratado (o no ja nao retem a saida do run %s — registo de desfechos em memoria)\n", p.NodeID, p.Output, p.Record.Stream)
		return "", false
	}
	if digestDoConteudo(st.FinalText) != p.Record.Digest {
		// Não é um erro de transporte: é a saída do run filho a não ser a que foi publicada.
		// Entregá-la seria substituir o payload por outro sem ninguém dar por isso.
		fmt.Printf("  execucao: payload %s/%s NAO rehidratado (digest do run filho %s nao bate com o publicado)\n", p.NodeID, p.Output, p.Record.Stream)
		return "", false
	}
	return st.FinalText, true
}

// nomesDasTools converte as capabilities pinadas (`cap:tool:<nome>`) nos nomes de tool que a
// lista-branca do nó compara com o `ToolID` de cada call. Devolve SEMPRE uma lista não-nil: um nó
// sem tools pinadas leva `[]`, que no nó quer dizer «nenhuma tool». Um nil sairia como `null` e
// seria «sem restrição» — o nó herdava as tools de todo o NHI do run, incluindo as de risco de
// outros nós.
func nomesDasTools(caps []string) []string {
	nomes := []string{}
	for _, c := range caps {
		if nome, ok := strings.CutPrefix(c, "cap:tool:"); ok && nome != "" {
			nomes = append(nomes, nome)
		}
	}
	return nomes
}

// recolher lê o estado de cada nó em voo e fecha os que acabaram. Devolve quantos fechou.
func (e *executorDeNos) recolher(ctx context.Context) (int, error) {
	fechados := 0
	for nodeID := range e.emVoo {
		st, existe, err := e.cli.Status(ctx, childRunID(e.runID, nodeID))
		if err != nil {
			// Uma leitura falhada (rede, IdP, 5xx, 429) não diz nada sobre o run: volta-se a ler na
			// passagem seguinte, e o prazo do serve limita a espera.
			fmt.Printf("  execucao: estado de %s ilegivel nesta passagem: %v\n", nodeID, err)
			continue
		}
		if !existe {
			// 404 de um run que foi submetido. Só conta como perdido se persistir: ver
			// [toleranciaA404]. Perdido, fica `failed` — esperar por ele era esperar para sempre.
			desde, visto := e.sumidos[nodeID]
			if !visto {
				e.sumidos[nodeID] = e.agora()
				continue
			}
			if e.agora().Sub(desde) < toleranciaA404 {
				continue
			}
		} else {
			delete(e.sumidos, nodeID)
			if !st.terminal() {
				continue
			}
		}
		if err := e.fechar(ctx, nodeID, st, existe); err != nil {
			return fechados, err
		}
		delete(e.emVoo, nodeID)
		delete(e.sumidos, nodeID)
		fechados++
	}
	return fechados, nil
}

// fechar escreve o veredicto (se for verificador) e a conclusão do nó, e liberta o headroom.
// A ordem importa: o veredicto antes da conclusão, para que uma passagem que veja o nó
// `complete` veja também o veredicto que ele emitiu.
func (e *executorDeNos) fechar(ctx context.Context, nodeID string, st estadoDoRun, existe bool) error {
	n := e.nos[nodeID]
	destino := arstate.Failed
	if existe && st.concluiu() {
		destino = arstate.Complete
	}
	var veredicto *plannerevents.VerdictRecordedPayload
	if n.IsVerifier() && destino == arstate.Complete {
		v := veredictoDaSaida(st.FinalText)
		v.NodeID = nodeID
		v.Subjects = n.IncomingEdges()
		_, err := e.rec.RecordVerdict(ctx, v, n)
		if errors.Is(err, plannerevents.ErrInvalidVerdict) {
			// O construtor recusou o que se leu (p.ex. razões a mais): continua a ser um veredicto
			// que não se consegue admitir, logo `fail` — e não um serve abortado.
			v.Outcome, v.Reasons = plannerevents.VerdictFail, []string{razaoVeredictoIlegivel}
			_, err = e.rec.RecordVerdict(ctx, v, n)
		}
		if errors.Is(err, plannerevents.ErrInvalidVerdict) {
			// Nem o `fail` se admite: o que falha são os SUJEITOS, que vêm do plano (um verificador
			// sem arestas de entrada, ou com mais do que o schema aceita). A validação só recusa
			// isso quando o veredicto é a origem de uma condição, pelo que ninguém o lê: fica sem
			// veredicto, e o plano segue — abortar aqui repetia-se em todas as retomas.
			fmt.Printf("  execucao: no %s sem veredicto admissivel (%v)\n", nodeID, err)
			err = nil
		}
		if err != nil {
			return fmt.Errorf("veredicto de %q: %w", nodeID, err)
		}
		veredicto = &v
	}
	// AOS-414: os contratos de saída cumprem-se ANTES da conclusão — uma passagem que veja o nó
	// `complete` tem de ver também o que ele publicou.
	if destino == arstate.Complete {
		if err := e.publicarSaidas(ctx, n, st, veredicto); err != nil {
			return err
		}
	}
	if err := e.g.MarkTerminal(ctx, nodeID, destino); err != nil && !errors.Is(err, orchestrator.ErrLogAhead) {
		return fmt.Errorf("conclusão de %q: %w", nodeID, err)
	}
	if err := e.headroom.Release(ctx); err != nil {
		return err
	}
	fmt.Printf("  execucao: no %s %s (run %s)\n", nodeID, destino, childRunID(e.runID, nodeID))
	return nil
}

// veredictoDaSaida lê o veredicto da saída final de um run verificador pela gramática fechada.
// Qualquer desvio — texto à volta, campos a mais, outcome fora do enum, razão fora da gramática
// de identificador — é `fail` com a razão `verdict_unparseable`. O modelo nunca escolhe os
// sujeitos: vêm do plano.
func veredictoDaSaida(saida string) plannerevents.VerdictRecordedPayload {
	ilegivel := plannerevents.VerdictRecordedPayload{
		Outcome: plannerevents.VerdictFail,
		Reasons: []string{razaoVeredictoIlegivel},
	}
	var v struct {
		Outcome string   `json:"outcome"`
		Reasons []string `json:"reasons"`
	}
	texto := strings.TrimSpace(saida)
	if !chavesExactas(texto) {
		return ilegivel
	}
	dec := json.NewDecoder(strings.NewReader(texto))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil || dec.InputOffset() != int64(len(texto)) {
		return ilegivel
	}
	outcome := plannerevents.VerdictOutcome(v.Outcome)
	if outcome != plannerevents.VerdictPass && outcome != plannerevents.VerdictFail {
		return ilegivel
	}
	vistas := map[string]struct{}{}
	for _, r := range v.Reasons {
		if _, dup := vistas[r]; dup || !plan.ValidIdentifier(r) {
			return ilegivel
		}
		vistas[r] = struct{}{}
	}
	return plannerevents.VerdictRecordedPayload{Outcome: outcome, Reasons: v.Reasons}
}

// chavesExactas confirma que o texto é UM objecto JSON cujas chaves de topo são só `outcome` e
// `reasons`, escritas exactamente assim e cada uma no máximo uma vez. O `encoding/json` aceita
// chaves noutra caixa (`OUTCOME`) e, com chaves repetidas, fica com a última — `{"outcome":"fail",
// "outcome":"pass"}` dava `pass`. Numa gramática fechada nenhuma das duas formas é admissível.
func chavesExactas(texto string) bool {
	dec := json.NewDecoder(strings.NewReader(texto))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return false
	}
	vistas := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return false
		}
		chave, ok := t.(string)
		if !ok || (chave != "outcome" && chave != "reasons") || vistas[chave] {
			return false
		}
		vistas[chave] = true
		var valor json.RawMessage
		if err := dec.Decode(&valor); err != nil {
			return false
		}
	}
	t, err := dec.Token()
	return err == nil && t == json.Delim('}')
}
