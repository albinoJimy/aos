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

// configDoExecutor é o executor tal como o `serve` o compõe.
type configDoExecutor struct {
	cli      nodeRunner
	prazo    time.Duration
	sondagem time.Duration
	// perdida recebe a perda da posse do run: a espera pára em vez de sondar até ao prazo.
	perdida <-chan error
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

// separadorDoRunFilho separa o run do nó do plano no id do run filho. Não é `/` (o `/runs/{id}`
// do nó casa um só segmento) nem `.` ou `:` — que a gramática de node_id admite, e com os quais
// o run `a` + nó `b.c` e o run `a.b` + nó `c` davam o mesmo id. O `~` não é um carácter de
// node_id, e o `serve` recusa um run_id que o contenha: a decomposição é única.
const separadorDoRunFilho = "~"

// childRunID é o id do run do nó `aos` que faz o trabalho de um nó do plano.
func childRunID(runID, nodeID string) string { return runID + separadorDoRunFilho + nodeID }

// executorDeNos acompanha os runs filhos de UM plano, sob a posse do run.
type executorDeNos struct {
	cli      nodeRunner
	rec      *runlifecycle.PlanRecorder
	g        *orchestrator.GraphBuilder
	runID    string
	nos      map[string]plan.Node // o documento aprovado, por node_id
	tools    map[string][]string  // as tools pinadas de cada nó, do plan.materialized
	headroom *boundedHeadroom
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
	if err := e.cli.Submit(ctx, pedidoDeRun{
		RunID:     childRunID(e.runID, nodeID),
		Objective: objectivo,
		Tools:     nomesDasTools(e.tools[nodeID]),
		Inputs:    entradas,
	}); err != nil {
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
func (e *executorDeNos) podarSemPayload(ctx context.Context) error {
	for id, n := range e.nos {
		if len(n.Consumes) == 0 {
			continue
		}
		if st, ok := e.g.DAG().State(id); !ok || st != arstate.Ready {
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
			fmt.Printf("  execucao: no %s NAO corre — o contrato %s/%s ficou por cumprir\n", id, c.From, c.Output)
			if err := e.g.MarkRunning(ctx, id); err != nil {
				return fmt.Errorf("marcar %q a correr para o fechar: %w", id, err)
			}
			if err := e.g.MarkTerminal(ctx, id, arstate.Failed); err != nil {
				return fmt.Errorf("fechar %q sem payload: %w", id, err)
			}
			break
		}
	}
	return nil
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
