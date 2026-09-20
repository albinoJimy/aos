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
// O conteúdo dos payloads vive na MEMÓRIA deste processo (ADR-027 §2.4, decisão (A) do dono): no
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
	return fmt.Sprintf("executor de nos (AOS-413/AOS-414, ADR-027): COMPOSTO — cada no despachado e um run do no aos em %s (%s; NHI do run do ficheiro montado; tools do no como lista-branca). Os payloads do `consumes` viajam MARCADOS untrusted e vivem na MEMORIA deste processo: um serve que morra perde-os e o consumidor NAO corre. A separacao de planos (DEF-806) continua aberta: o conteudo e lido pelo mesmo plano que planeia", cli.base, chamador)
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

// toleranciaA404 é quanto tempo um run em voo pode responder 404 antes de contar como perdido.
// Um 404 não é, por si, a morte do run: o nó responde 404 a um run durável `running` que não está
// na memória DESTE processo — outra réplica, ou a janela antes de a retoma de arranque o voltar a
// hospedar. Marcá-lo `failed` à primeira era dar por morto trabalho que continua.
const toleranciaA404 = 2 * time.Minute

func novoExecutorDeNos(cli nodeRunner, rec *runlifecycle.PlanRecorder, g *orchestrator.GraphBuilder, runID string,
	doc plan.PlanDocument, pinadas map[string][]string, headroom *boundedHeadroom) *executorDeNos {
	nos := make(map[string]plan.Node, len(doc.Nodes))
	for _, n := range doc.Nodes {
		nos[n.NodeID] = n
	}
	return &executorDeNos{cli: cli, rec: rec, g: g, runID: runID, nos: nos, tools: pinadas, headroom: headroom,
		emVoo: map[string]struct{}{}, sumidos: map[string]time.Time{}, agora: time.Now,
		payloads: map[chaveDePayload]string{}}
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
			bruto, err := json.Marshal(map[string]any{"outcome": v.Outcome, "reasons": v.Reasons})
			if err != nil {
				return err
			}
			conteudo = string(bruto)
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
