package main

// plan_claim.go — A FILA GANHA QUEM A DRENE, SEM DEIXAR DE SER INENUMERÁVEL.
//
// O AOS-417 abriu o `POST /plans` e não pôs ninguém do outro lado: o `201 accepted` prometia uma
// corrida que nunca começava. Isto fecha essa metade, pela via do ADR-030.
//
// # A FORMA, E PORQUE NÃO É UMA ROTA DE LEITURA
//
// `POST /plans/claim` devolve NO MÁXIMO UM pedido, e só depois de o ter RECLAMADO. Nenhuma rota
// enumera a fila e nenhuma devolve um pedido sem o consumir. A fila não é enumerável por
// CONSTRUÇÃO — não por filtro que alguém possa relaxar.
//
// # A AUTORIZAÇÃO É EXPLÍCITA PORQUE NÃO HERDA NENHUMA
//
// A fila está protegida hoje por duas coisas, e esta rota não herda nem uma:
//
//   - a BARRA em `aos-internal/plan-requests` — o padrão `{id}` do `http.ServeMux` casa um só
//     segmento, logo `GET /runs/aos-internal/plan-requests/trajectory` dá 404 por ROTEAMENTO;
//   - o [runIDReservado], que recusa o prefixo nas duas rotas de submissão.
//
// A trava do AOS-426 ([streamDeRun]) NÃO protege a fila: o `RunID` sintético dos seus eventos é
// igual ao nome do stream, pelo que ela devolveria `true`. Está escrito assim no próprio teste do
// AOS-426, que põe a fila no grupo «os que a barra já protegia».
//
// Um caminho com mais de um segmento contorna as duas. Daí o gate soberano ser verificado aqui,
// à cabeça, e a rota RECUSAR quando ele não está composto — servir sem gate seria pior do que não
// ter rota.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// A família `planrequest.*` é do NÓ — ver o cabeçalho de `plan_ingress.go` para a razão de não
// reutilizar a família `plan.*` do orquestrador.
const (
	// EventTypePlanRequestClaimed marca que um consumidor tomou o pedido para si.
	EventTypePlanRequestClaimed = "planrequest.claimed"
	// EventTypePlanRequestOutcome marca o desfecho de UMA tentativa.
	EventTypePlanRequestOutcome = "planrequest.outcome"
)

// Prefixos de `StepID` dentro do stream da fila. São o que distingue os três tipos de facto no
// mesmo stream — o molde é o do `approval_store_durable`, que usa `grant-`/`used-`/`pending-`.
const (
	prefixoPedido   = "req-"
	prefixoReclamo  = "claim-"
	prefixoDesfecho = "outcome-"
)

// Classes de desfecho. Mapeiam os códigos de saída do `serve` do `aos-orq` (ADR-030 §2.6), e a
// distinção é load-bearing: confundir transitório com permanente dá um pedido perdido ou um laço
// a retentar para sempre uma recusa determinista.
const (
	// DesfechoTransitorio — o `serve` não chegou a decidir nada (lease detido, fenced, WAL
	// detido, nós em voo). O pedido volta à fila IMEDIATAMENTE, na geração seguinte.
	DesfechoTransitorio = "transitorio"
	// DesfechoTerminal — houve decisão e está fechada (recusa, plano rejeitado) ou o plano
	// correu. Não se retenta.
	DesfechoTerminal = "terminal"
	// DesfechoAguardaHumano — o plano exige aprovação humana e nada foi materializado. Nem
	// retentativa (ninguém decidiu ainda) nem desfecho (não está fechado): o pedido fica
	// ESTACIONADO, e volta a ser oferecido de [intervaloDeReverificacao] em
	// [intervaloDeReverificacao] (AOS-442, emenda ao ADR-030 §2.6).
	DesfechoAguardaHumano = "aguarda_humano"
)

// intervaloDeReverificacao é quanto tempo um pedido À ESPERA DE HUMANO fica fora da fila antes de
// voltar a ser oferecido ao consumidor (AOS-442).
//
// # PORQUE É QUE O NÓ RE-OFERECE, E NÃO ESPERA QUE LHE DIGAM
//
// Até ao AOS-442 o `aguarda_humano` contava como terminado: depois da decisão humana, NADA no
// caminho da fila voltava a correr o pedido — ficava aprovado e parado. Para o tirar dali o nó
// precisava de saber que a decisão existe, e a decisão vive no Event Store do `aos-orq`, noutro
// volume. Ensinar-lho seria pô-lo a conhecer a semântica do orquestrador — a fronteira do ADR-018.
//
// Por isso o nó sabe só isto: um pedido estacionado volta a ser oferecido, de tempos a tempos, a
// quem drena a fila. Quem o reclama é que verifica se já há decisão — e, se não houver, reporta
// `aguarda_humano` outra vez, numa geração nova, sem pagar nada ao modelo. É mecânica de fila, do
// mesmo género do [ttlDaReclamacao]; não é o nó a decidir nada sobre o plano.
//
// O valor é o compromisso entre a espera depois de o humano decidir (até isto, mais o intervalo do
// timer de drenagem) e o ruído no log (uma reclamação e um desfecho por re-oferta, enquanto o
// pedido espera — o prazo do pendente, 24 h no `aos-orq`, fecha-o como recusado).
const intervaloDeReverificacao = 10 * time.Minute

// ttlDaReclamacao é quanto tempo uma reclamação sem desfecho segura o pedido.
//
// Existe porque o consumidor pode MORRER entre reclamar e reportar. O molde das aprovações
// reclama antes de ler e aceita queimar o item se o processo morrer — lado seguro para um grant
// de autoridade humana. Aqui o lado seguro é o OPOSTO: um pedido de plano perdido em silêncio é
// exactamente o defeito que este eixo fecha.
//
// O valor é generoso de propósito. Curto de mais, um `serve` lento é reclamado duas vezes e
// corre duas — e a segunda bate no lease do primeiro (saída 3), o que é recuperável mas ruidoso.
const ttlDaReclamacao = 30 * time.Minute

// tectoDePendentes é o número máximo de pedidos por drenar.
//
// Atingido, o INGRESSO recusa pedidos NOVOS (ADR-030 §2.7). Não se descartam os antigos: um
// pedido descartado em silêncio é a mesma classe de defeito que este eixo inteiro existe para
// fechar. É alto de propósito — a intenção é travar um laço em fuga, não moldar carga.
const tectoDePendentes = 1000

// pedidoNaFila é o estado projectado de um pedido, reconstituído do log.
type pedidoNaFila struct {
	RunID     string
	Payload   planRequestPayload
	Seq       uint64 // do facto de submissão — é a ordem de chegada
	Geracao   int    // a PRÓXIMA geração livre de reclamação
	Terminado bool
}

// projectarFila reconstitui o estado da fila a partir do log.
//
// É uma FUNÇÃO PURA de (eventos, agora) — sem relógio próprio, sem I/O — porque é onde vive toda
// a lógica de elegibilidade e é isso que a torna testável sem levantar um nó.
//
// Um pedido está ELEGÍVEL quando: foi submetido, não tem desfecho terminal, não tem reclamação
// VIVA (uma reclamação sem desfecho e dentro do [ttlDaReclamacao]) e não está ESTACIONADO (a sua
// última geração acabou em `aguarda_humano` há menos de [intervaloDeReverificacao]).
//
// Delega em [projectarComTerminados] e deita fora a segunda metade. Existe porque é esta a
// pergunta que quase todos os chamadores fazem, e porque é a assinatura que os testes do AOS-423
// amarram.
func projectarFila(eventos []eventstore.Event, agora time.Time) []pedidoNaFila {
	fila, _ := projectarComTerminados(eventos, agora)
	return fila
}

// projectarComTerminados é a projecção, mais o estado terminal de CADA pedido por ordem de
// chegada — que é o que a marca de água do AOS-429 precisa de saber e a fila sozinha não diz.
//
// As duas saídas vêm da MESMA passagem de propósito: a definição de «terminado» tem de existir
// uma só vez. Duas cópias derivam, e uma marca de água derivada de uma regra desactualizada
// saltaria pedidos vivos — a falha mais cara que este ficheiro pode ter.
func projectarComTerminados(eventos []eventstore.Event, agora time.Time) ([]pedidoNaFila, []estadoTerminal) {
	type estado struct {
		p            pedidoNaFila
		submetido    bool
		maiorGeracao int
		reclamadoEm  map[int]time.Time
		desfechoDe   map[int]string
		desfechoEm   map[int]time.Time
	}
	porRun := map[string]*estado{}
	ordem := []string{}

	garantir := func(runID string) *estado {
		e, ok := porRun[runID]
		if !ok {
			e = &estado{reclamadoEm: map[int]time.Time{}, desfechoDe: map[int]string{}, desfechoEm: map[int]time.Time{}}
			e.p.RunID = runID
			porRun[runID] = e
			ordem = append(ordem, runID)
		}
		return e
	}

	for _, ev := range eventos {
		switch {
		case ev.Type == EventTypePlanRequestSubmitted && strings.HasPrefix(ev.StepID, prefixoPedido):
			runID := strings.TrimPrefix(ev.StepID, prefixoPedido)
			e := garantir(runID)
			var p planRequestPayload
			if json.Unmarshal(ev.Payload, &p) != nil {
				// Payload ilegível: o pedido NÃO entra na fila. Um consumidor não pode honrar o
				// que não sabe ler, e interpretá-lo por omissão é como se perde a região.
				continue
			}
			e.submetido = true
			e.p.Payload = p
			e.p.Seq = ev.Seq

		case ev.Type == EventTypePlanRequestClaimed:
			ger, runID, ok := partirChaveComGeracao(ev.StepID, prefixoReclamo)
			if !ok {
				continue
			}
			e := garantir(runID)
			if t, err := time.Parse(time.RFC3339Nano, ev.Ts); err == nil {
				e.reclamadoEm[ger] = t
			}
			if ger > e.maiorGeracao {
				e.maiorGeracao = ger
			}

		case ev.Type == EventTypePlanRequestOutcome:
			ger, runID, ok := partirChaveComGeracao(ev.StepID, prefixoDesfecho)
			if !ok {
				continue
			}
			e := garantir(runID)
			var d desfechoPayload
			if json.Unmarshal(ev.Payload, &d) != nil {
				continue
			}
			e.desfechoDe[ger] = d.Classe
			if t, err := time.Parse(time.RFC3339Nano, ev.Ts); err == nil {
				e.desfechoEm[ger] = t
			}
			if ger > e.maiorGeracao {
				e.maiorGeracao = ger
			}
		}
	}

	var fora []pedidoNaFila
	var terminais []estadoTerminal
	for _, runID := range ordem {
		e := porRun[runID]
		if !e.submetido {
			// RECLAMAÇÃO ÓRFÃ: não se serve o que nunca foi pedido. Não entra nos terminais
			// tão-pouco — não tem `seq` de submissão, logo não há nada que autorize cortar.
			continue
		}
		// SÓ O TERMINAL FECHA (AOS-442). O `aguarda_humano` fechava também, e era por isso que um
		// plano aprovado depois da decisão humana nunca mais corria. Ele ESTACIONA — ver abaixo — e
		// não conta como terminado para a marca de água: um pedido à espera de humano não acabou, e
		// cortar o log acima dele esconderia a sua re-oferta.
		for _, classe := range e.desfechoDe {
			if classe == DesfechoTerminal {
				e.p.Terminado = true
			}
		}
		terminais = append(terminais, estadoTerminal{seq: e.p.Seq, terminado: e.p.Terminado})
		if e.p.Terminado {
			continue
		}
		// Reclamação VIVA numa geração sem desfecho e dentro do TTL ⇒ o pedido está a ser
		// trabalhado por alguém.
		viva := false
		for ger, em := range e.reclamadoEm {
			if _, houve := e.desfechoDe[ger]; houve {
				continue
			}
			if agora.Sub(em) < ttlDaReclamacao {
				viva = true
				break
			}
		}
		if viva {
			continue
		}
		// ESTACIONADO: a última geração acabou à espera de humano, e ainda não passou o intervalo
		// de re-oferta. Só a ÚLTIMA conta — um `aguarda_humano` antigo seguido de uma tentativa
		// posterior (transitória, ou uma reclamação expirada) já não diz nada sobre o agora.
		//
		// Um carimbo ilegível RE-OFERECE em vez de estacionar para sempre: estacionar sem prazo é
		// exactamente o defeito que isto fecha, e re-oferecer custa uma verificação do consumidor.
		if e.desfechoDe[e.maiorGeracao] == DesfechoAguardaHumano {
			if em, ok := e.desfechoEm[e.maiorGeracao]; ok && agora.Sub(em) < intervaloDeReverificacao {
				continue
			}
		}
		e.p.Geracao = e.maiorGeracao + 1
		fora = append(fora, e.p)
	}
	// Ordem de CHEGADA. Sem isto, um pedido azarado podia ficar para trás indefinidamente.
	sort.Slice(fora, func(i, j int) bool { return fora[i].Seq < fora[j].Seq })
	// Os terminais TAMBÉM por `seq`, porque a marca de água é um prefixo contíguo e um prefixo
	// só faz sentido sobre uma ordem. O `ordem` é de primeira aparição no log, que coincide com
	// o `seq` para os submetidos — ordenar aqui torna-o independente disso.
	sort.Slice(terminais, func(i, j int) bool { return terminais[i].seq < terminais[j].seq })
	return fora, terminais
}

// partirChaveComGeracao lê `<prefixo><geração>-<run_id>`.
//
// O `run_id` PODE conter hífenes — daí o corte ser no PRIMEIRO, e não pelo último.
func partirChaveComGeracao(stepID, prefixo string) (int, string, bool) {
	if !strings.HasPrefix(stepID, prefixo) {
		return 0, "", false
	}
	resto := strings.TrimPrefix(stepID, prefixo)
	i := strings.Index(resto, "-")
	if i <= 0 || i == len(resto)-1 {
		return 0, "", false
	}
	ger, err := strconv.Atoi(resto[:i])
	if err != nil || ger < 1 {
		return 0, "", false
	}
	return ger, resto[i+1:], true
}

// desfechoPayload é o facto de desfecho de UMA tentativa.
type desfechoPayload struct {
	Versao   string `json:"v"`
	RunID    string `json:"run_id"`
	Classe   string `json:"classe"`
	CodigoDe int    `json:"codigo_saida,omitempty"`
	Detalhe  string `json:"detalhe,omitempty"`
}

// respostaDeReclamo é o que a rota devolve quando há trabalho.
type respostaDeReclamo struct {
	RunID     string `json:"run_id"`
	Objective string `json:"objective"`
	Board     string `json:"board,omitempty"`
	Region    string `json:"region,omitempty"`
	Geracao   int    `json:"generation"`
}

// handlePlanClaim reclama UM pedido pendente e devolve-o.
//
// 204 quando não há nada para este reclamante — e o 204 é o mesmo quer a fila esteja vazia, quer
// tudo o que lá está pertença a outra região. É a postura da §2.1 do ADR-030: não se revela a
// EXISTÊNCIA de um recurso a quem não pode agir sobre ele.
func (h *apiHandler) handlePlanClaim(w http.ResponseWriter, r *http.Request) {
	// O GATE SOBERANO É PRÉ-CONDIÇÃO, e a recusa é 501 e não 403: a diferença entre «não estás
	// autorizado» e «este nó não sabe autorizar ninguém» é diagnóstica, e esconder a segunda
	// mandaria o operador procurar credenciais quando o que falta é composição.
	if h.readGov == nil {
		writeError(w, http.StatusNotImplemented, "reclamacao de pedidos de plano sem gate soberano composto")
		return
	}
	reclamante, ok := h.readGov.authorize(r)
	if !ok || reclamante.principal == "" {
		writeError(w, http.StatusForbidden, "nao autorizado")
		return
	}

	// UM PEDIDO ILEGÍVEL NÃO TAPA OS SEGUINTES (AOS-442). Até aqui a reclamação devolvia 503 ao
	// primeiro objectivo que não abrisse, e o `consume` abortava a drenagem inteira. Com a
	// re-oferta dos pedidos à espera de humano isso passava a repetir-se: o pedido morto voltava à
	// cabeça da fila a cada expiração da reclamação e parava a drenagem de todos os outros. Agora
	// salta-se para o seguinte; o 503 fica só para quando NADA do que se reclamou era entregável.
	saltados := 0
	for tentativa := 0; tentativa < maxIlegiveisPorReclamacao; tentativa++ {
		pedido, err := h.reclamarUm(r.Context(), reclamante)
		if err != nil {
			h.logf("plan-claim: reclamacao falhou principal=%q: %v", reclamante.principal, err)
			writeError(w, http.StatusServiceUnavailable, "reclamacao indisponivel")
			return
		}
		if pedido == nil {
			break
		}
		// O OBJECTIVO ABRE-SE AQUI, e não na projecção (AOS-429).
		//
		// A projecção corre a cada submissão, sobre a fila toda, só para contar pendentes;
		// decifrar ali seria pagar cripto por cada pedido de cada varredura para deitar fora tudo
		// menos um. Aqui abre-se exactamente o pedido que vai ser entregue, uma vez.
		//
		// A forma do wire não muda: o consumidor recebe `objective` em claro, como sempre, pelo
		// canal que o gate soberano já autenticou. É por isto que ele nunca precisa da chave.
		objetivo, errAbrir := abrirObjetivo(h.node, pedido.Payload)
		if errAbrir != nil {
			// A CAUSA MAIS PROVÁVEL É LEGÍTIMA, e é o Art. 17 a funcionar: a KEK do titular foi
			// destruída por um `/dsar/erase` e o pedido deixou de ser executável.
			//
			// O pedido JÁ FOI RECLAMADO quando se chega aqui — a reclamação é o que arbitra entre
			// consumidores e tem de acontecer antes. Fica reclamado, e a reclamação expira pelo
			// TTL, que é o caminho normal de um consumidor que morre a meio. NÃO se escreve
			// desfecho: quem reporta desfechos é o consumidor, e inventar um aqui poria o nó a
			// afirmar sobre uma tentativa que nunca correu — fechar um pedido ilegível exige
			// decidir QUEM escreve esse desfecho e com que código (resíduo do AOS-442).
			h.logf("plan-claim: pedido reclamado mas ILEGIVEL run=%q principal=%q: %v — "+
				"tipicamente a chave do titular foi destruida por /dsar/erase; segue para o seguinte",
				pedido.RunID, reclamante.principal, errAbrir)
			saltados++
			continue
		}
		writeJSON(w, http.StatusOK, respostaDeReclamo{
			RunID:     pedido.RunID,
			Objective: objetivo,
			Board:     pedido.Payload.Board,
			Region:    pedido.Payload.Region,
			Geracao:   pedido.Geracao,
		})
		return
	}
	if saltados > 0 {
		// Nada entregável, e pelo menos um ilegível: o 503 de sempre, que é o que o operador vê.
		writeError(w, http.StatusServiceUnavailable, "pedido indisponivel")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// maxIlegiveisPorReclamacao limita quantos pedidos ilegíveis uma reclamação salta antes de
// desistir. Cada salto escreve um facto de reclamação; sem tecto, uma fila de pedidos apagados
// seria percorrida inteira num só pedido HTTP.
const maxIlegiveisPorReclamacao = 16

// reclamarUm projecta a fila, escolhe o pedido mais antigo elegível PARA ESTE reclamante, e
// tenta reclamá-lo.
//
// O `StatusDuplicate` é o árbitro: dois consumidores que projectem o mesmo estado tentam a mesma
// geração, e só um vence. O perdedor tenta o seguinte — não falha.
func (h *apiHandler) reclamarUm(ctx context.Context, reclamante readerIdentity) (*pedidoNaFila, error) {
	eventos, err := h.node.EventStore.Read(ctx, planRequestStream, h.marcaDaFila.desde())
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil, nil // fila nunca usada: não é erro
		}
		return nil, err
	}
	fila, nova := projectarFilaComMarca(eventos, time.Now().UTC())
	h.marcaDaFila.avancar(nova)
	for _, p := range fila {
		// SOBERANIA (ADR-016 §5): um pedido submetido noutra região não é entregue aqui. A
		// comparação é por valor exacto, como o `regionMatches` do gateway — «qualquer região»
		// não é uma fronteira de soberania.
		if p.Payload.Region != "" && reclamante.region != "" && p.Payload.Region != reclamante.region {
			continue
		}
		res, err := h.node.EventStore.Append(ctx, planRequestStream, eventstore.EventInput{
			Type:     EventTypePlanRequestClaimed,
			Payload:  json.RawMessage(`{"v":"` + planRequestVersao + `","by":` + comoJSON(reclamante.principal) + `}`),
			RunID:    planRequestRunID,
			StepID:   prefixoReclamo + strconv.Itoa(p.Geracao) + "-" + p.RunID,
			Producer: eventstore.Producer{NHIID: planIngressNHI},
		})
		if err != nil {
			return nil, fmt.Errorf("reclamar %q geracao %d: %w", p.RunID, p.Geracao, err)
		}
		if res.Status == eventstore.StatusDuplicate {
			continue // outro consumidor ganhou esta geração; tenta o pedido seguinte
		}
		pedido := p
		return &pedido, nil
	}
	return nil, nil
}

// comoJSON devolve uma string JSON válida. Marshal de uma string não falha.
func comoJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// pedidoDeDesfecho é o corpo que o consumidor envia ao reportar o resultado de uma tentativa.
type pedidoDeDesfecho struct {
	RunID       string `json:"run_id"`
	Geracao     int    `json:"generation"`
	Classe      string `json:"classe"`
	CodigoSaida int    `json:"codigo_saida,omitempty"`
	Detalhe     string `json:"detalhe,omitempty"`
}

// handlePlanOutcome regista o desfecho de UMA tentativa.
//
// # PORQUE É QUE ISTO É UMA ROTA E NÃO UM EFEITO DO TEMPO
//
// Sem ela, um pedido cujo `serve` falhou só voltava à fila quando a reclamação expirasse — meia
// hora de silêncio por uma falha que o consumidor conhecia no primeiro segundo. Com ela, o
// transitório volta JÁ e o permanente não volta nunca.
//
// A classe é do CHAMADOR, e isso é deliberado: só o `aos-orq` sabe o código de saída do `serve`,
// e a tradução código→classe vive lá (ver `consumir.go`). O nó valida o vocabulário, não a
// decisão — impor aqui a tabela de códigos punha o nó a conhecer a semântica de saída do
// orquestrador, que é precisamente a fronteira do ADR-018.
func (h *apiHandler) handlePlanOutcome(w http.ResponseWriter, r *http.Request) {
	if h.readGov == nil {
		writeError(w, http.StatusNotImplemented, "desfecho de pedidos de plano sem gate soberano composto")
		return
	}
	quem, ok := h.readGov.authorize(r)
	if !ok || quem.principal == "" {
		writeError(w, http.StatusForbidden, "nao autorizado")
		return
	}
	var req pedidoDeDesfecho
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "corpo invalido")
		return
	}
	switch req.Classe {
	case DesfechoTransitorio, DesfechoTerminal, DesfechoAguardaHumano:
	default:
		writeError(w, http.StatusBadRequest, "classe de desfecho invalida")
		return
	}
	if req.RunID == "" || req.Geracao < 1 {
		writeError(w, http.StatusBadRequest, "run_id e generation sao obrigatorios")
		return
	}
	// O `run_id` do desfecho vai para um `StepID`, e um `StepID` entra na idempotency-key. Um
	// valor que o Event Store recusasse daria 503 numa rota que devia dar 400.
	// O RESERVADO TAMBÉM, e a ausência disto era um buraco real (AOS-430).
	//
	// O `POST /plans` recusa um `run_id` com o prefixo reservado (`runIDReservado`); esta rota
	// só chamava o `runIDInvalido`, que valida REPRESENTABILIDADE — e a barra é representável,
	// por decisão do AOS-424. Um desfecho podia portanto ser reportado para
	// `aos-internal/qualquer-coisa` e gravar um `StepID` no espaço reservado.
	//
	// O dano hoje era pequeno (a projecção trata-o como órfão e ignora-o), mas a assimetria
	// entre as duas rotas é que é o defeito: a mesma regra tem de valer nas duas pontas, senão
	// a próxima pessoa a ler uma delas conclui o contrário da outra.
	if runIDReservado(req.RunID) {
		writeError(w, http.StatusBadRequest, "run_id invalido")
		return
	}
	if runIDInvalido(req.RunID) {
		writeError(w, http.StatusBadRequest, "run_id invalido")
		return
	}

	bruto, err := json.Marshal(desfechoPayload{
		Versao:   planRequestVersao,
		RunID:    req.RunID,
		Classe:   req.Classe,
		CodigoDe: req.CodigoSaida,
		Detalhe:  truncar(req.Detalhe, 512),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "erro interno")
		return
	}
	// Idempotente por (run_id, geração): reportar o mesmo desfecho duas vezes é a mesma
	// escrita, e o `StatusDuplicate` cai no mesmo caminho de sucesso. Um consumidor que reporte
	// e morra antes de ler a resposta pode repetir sem consequência.
	if _, err := h.node.EventStore.Append(r.Context(), planRequestStream, eventstore.EventInput{
		Type:     EventTypePlanRequestOutcome,
		Payload:  bruto,
		RunID:    planRequestRunID,
		StepID:   prefixoDesfecho + strconv.Itoa(req.Geracao) + "-" + req.RunID,
		Producer: eventstore.Producer{NHIID: planIngressNHI},
	}); err != nil {
		h.logf("plan-claim: desfecho nao gravado run=%q geracao=%d: %v", req.RunID, req.Geracao, err)
		writeError(w, http.StatusServiceUnavailable, "desfecho nao registado")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// truncar limita o detalhe livre que o consumidor envia. O detalhe é diagnóstico, não contrato.
func truncar(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// pendentesNaFila conta os pedidos por drenar. É o que o [tectoDePendentes] mede.
//
// A `marca` é opcional (nil ⇒ lê tudo) porque há um chamador — a métrica em `api.go` — que não
// tem estado onde a guardar e para quem uma leitura completa ocasional não custa nada. Os dois
// caminhos QUENTES (`POST /plans` e a reclamação) passam-na.
func pendentesNaFila(ctx context.Context, store EventStorePort, marca *marcaDeAgua) (int, error) {
	var desde uint64
	if marca != nil {
		desde = marca.desde()
	}
	eventos, err := store.Read(ctx, planRequestStream, desde)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return 0, nil
		}
		return 0, err
	}
	fila, nova := projectarFilaComMarca(eventos, time.Now().UTC())
	if marca != nil {
		marca.avancar(nova)
	}
	return len(fila), nil
}

// filaReclamavel diz se a rota de reclamacao vai SERVIR, e existe para o banner de arranque o
// poder declarar antes de a API estar construida.
//
// Espelha o predicado de auto-derivacao do `readGov` em `newAPI`: o WORM composto MAIS uma fonte
// de autoridade board->regiao. Nao ve a composicao EXPLICITA (`WithReadSovereignty`), que e um
// seam de teste — nesse caso o banner SUBDECLARA, e subdeclarar e o lado seguro: diz que a fila
// nao e drenavel quando ela e, em vez do contrario.
//
// Ha um teste que amarra este predicado ao de `newAPI`: se um deles mudar sozinho, o banner passa
// a mentir em silencio, que e a forma de defeito que este ficheiro inteiro existe para fechar.
func filaReclamavel(node *Node) bool {
	if node == nil || node.WORM == nil {
		return false
	}
	return node.SovereignAuthority != nil || node.SovereignReadRegions != nil
}
