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
	"slices"
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
//
// TEM DE EXCEDER O PIOR CASO DE UMA GERAÇÃO (AOS-439). Desde o vínculo reclamação→run, cada run
// filho só é aceite com a reclamação VIVA; a 30 min, abaixo dos 40 do `--plan-timeout` do
// `aos-orq`, um nó que ficasse pronto depois do TTL era recusado e a geração inteira perdia-se (e a
// saída 1 não larga o lease). O pior caso não é só o prazo: antes dele o `serve` decompõe (até 3
// tentativas do planeador, cada uma com o egress do modelo — 120 s em produção) e re-hidrata os
// payloads (2 min): 40 + 3×2 + 2 = 48 min. 60 min dá 12 de folga.
// `TestAOS439TTLDaReclamacaoExcedeOPrazoDoPlano` soma essas parcelas das fontes e falha se este
// valor ficar abaixo. Custo: um consumidor que morra segura o pedido até uma hora.
const ttlDaReclamacao = 60 * time.Minute

// pedidoNaFila é o estado projectado de um pedido, reconstituído do log.
type pedidoNaFila struct {
	RunID     string
	Payload   planRequestPayload
	Seq       uint64 // do facto de submissão — é a ordem de chegada
	Geracao   int    // a PRÓXIMA geração livre de reclamação
	Terminado bool
	// GeracoesContadas é quantas das gerações 1..Geracao contam para o tecto do AOS-467: todas menos
	// as que seguem uma geração acabada à espera de humano (as re-verificações).
	GeracoesContadas int
	// Esgotado marca a geração que passa o tecto: entrega-se para o drenador FECHAR o pedido, não
	// para planear. Decide-o a reclamação, que conhece o tecto — a projecção não conhece.
	Esgotado bool
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
		// chamouModelo é o que cada desfecho DECLAROU sobre a sua geração (AOS-467); ausente ⇒ não
		// declarou.
		chamouModelo map[int]bool
		// planoValidado é o que cada desfecho declarou: depois dele o plano estava validado.
		planoValidado map[int]bool
	}
	porRun := map[string]*estado{}
	ordem := []string{}

	garantir := func(runID string) *estado {
		e, ok := porRun[runID]
		if !ok {
			e = &estado{reclamadoEm: map[int]time.Time{}, desfechoDe: map[int]string{}, desfechoEm: map[int]time.Time{}, chamouModelo: map[int]bool{}, planoValidado: map[int]bool{}}
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
			if d.ChamouModelo != nil {
				e.chamouModelo[ger] = *d.ChamouModelo
			}
			if d.PlanoValidado != nil {
				e.planoValidado[ger] = *d.PlanoValidado
			}
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
		// GERAÇÕES QUE CONTAM PARA O TECTO (AOS-467): as que CHAMARAM O MODELO (decisão do dono,
		// depois da revisão adversarial: a regra «todas menos as re-verificações» fechava um plano
		// aprovado e longo, cujas retomas pela saída 8 correm pelo documento sem modelo).
		//
		//   - declarada pelo drenador (`chamou_modelo`): conta se chamou;
		//   - NÃO declarada (reclamação expirada, drenador anterior, a geração a oferecer): regra
		//     conservadora — conta, excepto se a anterior acabou à espera de humano (re-verificação) ou
		//     declarou o plano VALIDADO (a seguinte retoma pelo documento). Sem esta segunda excepção, a
		//     primeira retoma de um plano cuja decomposição foi a de número «tecto» saía marcada —
		//     achado ALTO da segunda revisão adversarial.
		//
		// Desconta-se pelos MAPAS e não por um laço de 1 até à geração: um desfecho reportado para uma
		// geração arbitrária (10⁹) faria da projecção um laço de mil milhões.
		naoContam := 0
		for g, chamou := range e.chamouModelo {
			if !chamou && g >= 1 && g < e.p.Geracao {
				naoContam++
			}
		}
		// A GERAÇÃO A OFERECER só conta como provisória se a anterior TEM desfecho: uma anterior que
		// expirou sem desfecho (o drenador morreu a meio) pode ter validado o plano, e a seguinte ser
		// uma retoma pelo documento — contá-la fechava um plano saudável (cenário D da terceira
		// revisão). A expirada conta por si; o custo é que, depois de uma morte, um pedido pode
		// decompor uma vez para lá do tecto antes de ser marcado.
		if _, houve := e.desfechoDe[e.maiorGeracao]; !houve && e.maiorGeracao >= 1 {
			naoContam++
		}
		for g, classe := range e.desfechoDe {
			seguinte := g + 1
			semModelo := classe == DesfechoAguardaHumano || e.planoValidado[g]
			if _, declarada := e.chamouModelo[seguinte]; semModelo && g >= 1 && seguinte <= e.p.Geracao && !declarada {
				naoContam++
			}
		}
		e.p.GeracoesContadas = e.p.Geracao - naoContam
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
	// ChamouModelo é o que o drenador declara: esta geração chamou o modelo (AOS-467). nil quando não
	// declarou (um drenador anterior) — e aí a projecção usa a regra conservadora. É o que separa a
	// geração que DECOMPÔS (o custo que o tecto limita) da retoma de um plano já aprovado, que corre
	// pelo documento sem modelo e não pode contar: um plano longo seria fechado pelo tecto.
	ChamouModelo *bool `json:"chamou_modelo,omitempty"`
	// PlanoValidado — depois desta geração o log do run tem `plan.validated` (AOS-467): a geração
	// seguinte retoma pelo documento, sem modelo, e não conta para o tecto. nil quando não declarou.
	PlanoValidado *bool `json:"plano_validado,omitempty"`
}

// respostaDeReclamo é o que a rota devolve quando há trabalho.
type respostaDeReclamo struct {
	RunID     string `json:"run_id"`
	Objective string `json:"objective"`
	Board     string `json:"board,omitempty"`
	Region    string `json:"region,omitempty"`
	Geracao   int    `json:"generation"`
	// RequestedBy é o principal que SUBMETEU o pedido (o do `planrequest.submitted`, AOS-439). Vai
	// ao drenador para ele recusar ANTES de planear um pedido cujo submissor o seu mandato não
	// nomeia — sem isto o planeador corria o modelo com o NHI do mandato por um submissor que o
	// humano não autorizou. Não revela nada novo a quem o recebe: o drenador já recebe o objectivo
	// decifrado, e o `requested_by` é o que o nó derivará do mesmo pedido no `POST /runs`.
	RequestedBy string `json:"requested_by,omitempty"`
	// GeracoesEsgotadas marca a geração que passou o tecto de gerações (AOS-467): o drenador fecha o
	// pedido com a saída 12, SEM planear. Um drenador anterior ignora o campo e planeia.
	GeracoesEsgotadas bool `json:"generations_exhausted,omitempty"`
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
	// SÓ QUEM O DONO NOMEOU DRENA (AOS-439). Autenticado não chega: qualquer identidade da região
	// reclamava pedidos ALHEIOS e recebia o objectivo DECIFRADO. A recusa é a MESMA 403 de cima, e
	// vem ANTES de qualquer escrita — uma reclamação recusada não gasta uma geração do pedido.
	if !h.eDrenador(reclamante.principal) {
		h.logf("plan-claim: RECUSADO (AOS-439) — principal=%q nao consta de AOS_PLAN_DRAINERS", reclamante.principal)
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
		//
		// UM PEDIDO ESGOTADO ENTREGA-SE SEM OBJECTIVO (AOS-467): o drenador fecha-o sem planear e não
		// precisa dele. Não se decifra — o objectivo do titular não sai em claro sem necessidade —, e
		// isso tem dois efeitos medidos pela revisão: um `aos-orq` anterior, que ignora a marca, não
		// tem o que decompor e não chama o modelo; e o pedido de objectivo ILEGÍVEL do AOS-442,
		// re-reclamado a cada expiração para sempre, entrega-se para fechar ao passar o tecto.
		var objetivo string
		var errAbrir error
		if !pedido.Esgotado {
			objetivo, errAbrir = abrirObjetivo(h.node, pedido.Payload)
		}
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
			// AOS-439: o submissor, para o drenador o confrontar com o seu mandato antes de planear.
			RequestedBy: pedido.Payload.Principal,
			// AOS-467: a geração passou o tecto — o drenador fecha o pedido (saída 12) sem planear.
			GeracoesEsgotadas: pedido.Esgotado,
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

// codigoDeGeracoesEsgotadas é a saída do `aos-orq` que fecha uma geração marcada (AOS-467,
// `exitGeracoesEsgotadas` no aos-orq). É o único código do consumidor que o nó conhece.
const codigoDeGeracoesEsgotadas = 12

// verificarQuotaNaEntrega decide se a entrega de uma geração exige quota (AOS-466/467). Uma geração
// MARCADA não exige: é de fecho e não planeia. Uma marcada só se repete quando a reclamação anterior
// expirou sem desfecho — um desfecho que não seja o terminal 12 é RECUSADO a uma marcada
// ([handlePlanOutcome]) —, e exigir-lhe quota deixaria pendente até à reposição um pedido a que só
// falta fechar (achado MÉDIO da segunda revisão; a versão anterior só isentava a primeira).
func verificarQuotaNaEntrega(p pedidoNaFila) bool {
	return !p.Esgotado
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
		// QUOTA DE PLANEAMENTO (AOS-466): a entrega fica na quota do titular ANTES da reclamação.
		// Desde aqui a geração custa a reserva até o seu desfecho trazer a parcela — é o que cobre a
		// geração que está a correr, e a que se perder. Se falha, não se entrega: uma geração que
		// planeasse sem constar da quota seria planeamento de graça.
		//
		// Um stream de quota ILEGÍVEL é de UM titular: esse pedido fica por entregar e a reclamação
		// segue para o próximo — sem isto, o registo estragado de um titular fechava a fila a todos.
		// Uma quota ESGOTADA também é só dele: uma re-oferta não se entrega sem quota para mais uma
		// geração (decisão do dono), e o pedido fica pendente até haver. Qualquer outra falha é do
		// substrato, e é a reclamação inteira que não se faz (503).
		// O TECTO DE GERAÇÕES (AOS-467): a geração que o passa entrega-se MARCADA, para o drenador
		// fechar o pedido sem planear. Não verifica quota — não planeia —, e é por isso que tem de
		// ser decidido AQUI, antes da entrega: depois, a quota esgotada já a teria deixado pendente.
		p.Esgotado = h.cfg.planMaxGenerations > 0 && p.GeracoesContadas > h.cfg.planMaxGenerations
		if h.node.QuotaPorPrincipal != nil {
			if err := h.node.QuotaPorPrincipal.registarEntrega(ctx, p.Payload.Principal, p.RunID, p.Geracao, verificarQuotaNaEntrega(p)); errors.Is(err, ErrPrincipalQuotaUnreadable) {
				h.logf("plan-claim: pedido %q NAO entregue — a quota do titular e ilegivel: %v", p.RunID, err)
				continue
			} else if errors.Is(err, ErrPrincipalQuotaExhausted) {
				h.logf("plan-claim: pedido %q geracao %d NAO entregue — %v", p.RunID, p.Geracao, err)
				continue
			} else if err != nil {
				return nil, fmt.Errorf("quota: entrega de %q geracao %d: %w", p.RunID, p.Geracao, err)
			}
		}
		res, err := h.node.EventStore.Append(ctx, planRequestStream, eventstore.EventInput{
			Type:     EventTypePlanRequestClaimed,
			Payload:  payloadDaReclamacao(reclamante.principal, p.Esgotado),
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

// payloadDaReclamacao é o corpo do facto de reclamação. `esgotada` regista no LOG que a geração foi
// entregue marcada pelo tecto de gerações (AOS-467): é por ele que o desfecho dessa geração só se
// aceita como o terminal 12.
func payloadDaReclamacao(por string, esgotada bool) json.RawMessage {
	marca := ""
	if esgotada {
		marca = `,"esgotada":true`
	}
	return json.RawMessage(`{"v":"` + planRequestVersao + `","by":` + comoJSON(por) + marca + `}`)
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
	// Consumo é o que o planeamento desta geração gastou no modelo, medido pelo drenador (AOS-466).
	// Ausente — um `aos-orq` anterior — vale como NÃO MEDIDO, e a reserva de planeamento não se
	// liberta.
	Consumo *consumoReportado `json:"consumo,omitempty"`
	// ChamouModelo — esta geração chamou o modelo (AOS-467). Ausente num drenador anterior.
	ChamouModelo *bool `json:"chamou_modelo,omitempty"`
	// PlanoValidado — depois desta geração o plano do run está validado (AOS-467).
	PlanoValidado *bool `json:"plano_validado,omitempty"`
}

// consumoReportado é o consumo de planeamento de uma geração, como o drenador o declara. Os
// `..._medidos` a falso dizem «não se sabe», que não é o mesmo que zero.
type consumoReportado struct {
	Tokens        int64 `json:"tokens"`
	TokensMedidos bool  `json:"tokens_medidos"`
	CostMicroUSD  int64 `json:"cost_micro_usd"`
	CustoMedido   bool  `json:"custo_medido"`
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
	// A MESMA LISTA da reclamação (AOS-439): quem não drena também não fecha pedidos de ninguém.
	if !h.eDrenador(quem.principal) {
		h.logf("plan-outcome: RECUSADO (AOS-439) — principal=%q nao consta de AOS_PLAN_DRAINERS", quem.principal)
		writeError(w, http.StatusForbidden, "nao autorizado")
		return
	}
	var req pedidoDeDesfecho
	if err := json.NewDecoder(http.MaxBytesReader(semVigia(w), r.Body, 8<<10)).Decode(&req); err != nil {
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
	if c := req.Consumo; c != nil && (c.Tokens < 0 || c.CostMicroUSD < 0) {
		writeError(w, http.StatusBadRequest, "consumo invalido")
		return
	}

	// QUOTA DE PLANEAMENTO (AOS-466), em DOIS tempos à volta do desfecho.
	//
	// (a) A PARCELA desta geração ANTES do desfecho. Se não se grava, o desfecho também não — e o
	// drenador vê o 503. Na ordem inversa, um desfecho gravado sem parcela deixava uma geração que
	// gastou fora da quota; nesta, o pior é a reclamação expirar e a geração seguinte correr, e essa
	// geração sem parcela custa a reserva inteira.
	//
	// (b) O FECHO só DEPOIS de o desfecho terminal estar no log. A primeira versão marcava a parcela
	// como final ANTES do desfecho: se o desfecho falhava, a reserva estava libertada com o pedido
	// vivo, e as gerações seguintes planeavam sem ela (achado MÉDIO-2 da revisão adversarial).
	// O ESTADO DO PEDIDO lê-se uma vez: serve a marca do tecto (AOS-467) e a quota (AOS-466).
	estado, achado, err := estadoDoPedido(r.Context(), h.node.EventStore, req.RunID, time.Now().UTC())
	if err != nil {
		h.logf("plan-claim: estado do pedido ilegivel run=%q: %v", req.RunID, err)
		writeError(w, http.StatusServiceUnavailable, "desfecho nao registado")
		return
	}
	// UMA GERAÇÃO MARCADA SÓ FECHA COM O TERMINAL 12 (AOS-467). Foi entregue para fechar, e sem o
	// objectivo; um drenador anterior ignora a marca, corre um `serve` sem objectivo que não admite nó
	// nenhum, e reportava SUCESSO (código 0) — o pedido ficava «terminado com sucesso» com zero nós, a
	// mentir ao titular e ao operador (achado MÉDIO da segunda revisão). O 12 é o único código do
	// `aos-orq` que o nó conhece, e conhece-o porque é o protocolo desta marca. Recusado, o desfecho
	// não se grava, a reclamação expira e a geração volta marcada.
	if achado && estado.marcadas[req.Geracao] && (req.Classe != DesfechoTerminal || req.CodigoSaida != codigoDeGeracoesEsgotadas) {
		h.logf("plan-claim: RECUSADO — a geracao %d de %q foi entregue MARCADA pelo tecto de geracoes e so fecha com o desfecho terminal %d; veio %s/%d (drenador anterior ao AOS-467?)",
			req.Geracao, req.RunID, codigoDeGeracoesEsgotadas, req.Classe, req.CodigoSaida)
		writeError(w, http.StatusBadRequest, "geracao marcada pelo tecto so fecha com o desfecho terminal 12")
		return
	}
	var plano *planeamentoDoPedido
	if h.node.QuotaPorPrincipal != nil {
		if plano, err = h.parcelaDePlaneamento(r.Context(), req, estado, achado); errors.Is(err, errGeracaoNaoEntregue) {
			writeError(w, http.StatusBadRequest, "geracao nao entregue")
			return
		} else if err != nil {
			h.logf("plan-claim: parcela de planeamento nao gravada run=%q geracao=%d: %v", req.RunID, req.Geracao, err)
			writeError(w, http.StatusServiceUnavailable, "desfecho nao registado")
			return
		}
	}

	bruto, err := json.Marshal(desfechoPayload{
		Versao:   planRequestVersao,
		RunID:    req.RunID,
		Classe:   req.Classe,
		CodigoDe: req.CodigoSaida,
		Detalhe:  truncar(req.Detalhe, 512),
		// AOS-467: o que o tecto de gerações conta.
		ChamouModelo:  req.ChamouModelo,
		PlanoValidado: req.PlanoValidado,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "erro interno")
		return
	}
	// Idempotente por (run_id, geração): reportar o mesmo desfecho duas vezes é a mesma
	// escrita, e o `StatusDuplicate` cai no mesmo caminho de sucesso. Um consumidor que reporte
	// e morra antes de ler a resposta pode repetir sem consequência.
	res, err := h.node.EventStore.Append(r.Context(), planRequestStream, eventstore.EventInput{
		Type:     EventTypePlanRequestOutcome,
		Payload:  bruto,
		RunID:    planRequestRunID,
		StepID:   prefixoDesfecho + strconv.Itoa(req.Geracao) + "-" + req.RunID,
		Producer: eventstore.Producer{NHIID: planIngressNHI},
	})
	if err != nil {
		h.logf("plan-claim: desfecho nao gravado run=%q geracao=%d: %v", req.RunID, req.Geracao, err)
		writeError(w, http.StatusServiceUnavailable, "desfecho nao registado")
		return
	}
	// O FECHO SÓ COM UM DESFECHO TERMINAL QUE ESTA ESCRITA GRAVOU. Um duplicado quer dizer que a
	// geração já tinha desfecho — possivelmente outro, e não terminal: fechar pela classe do pedido
	// repetido libertava a reserva de um pedido que o log diz vivo (achado da re-revisão).
	if plano != nil && req.Classe == DesfechoTerminal && res.Status != eventstore.StatusDuplicate {
		// O desfecho JÁ está no log: responder 503 aqui diria ao drenador que não está, e ele não
		// avisaria o fim do plano. Um fecho que falha deixa a reserva inteira até ao fim do mês — a
		// mais, nunca a menos — e fica no log do operador.
		if err := h.node.QuotaPorPrincipal.fecharPlaneamento(r.Context(), plano.titular, req.RunID); err != nil {
			h.logf("plan-claim: fecho do planeamento nao gravado run=%q — a reserva de planeamento fica inteira ate ao fim do mes: %v", req.RunID, err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// planeamentoDoPedido é o que o fecho precisa de saber sobre o pedido.
type planeamentoDoPedido struct {
	titular string
}

// errGeracaoNaoEntregue — o desfecho nomeia uma geração que o nó nunca entregou. Com a quota
// composta recusa-se (400): cada geração entregue custa a reserva até ter parcela, e uma geração
// arbitrária — 10⁹ — negaria o mês do titular (achado da re-revisão).
var errGeracaoNaoEntregue = errors.New("geracao nao entregue")

// parcelaDePlaneamento grava a parcela de planeamento de uma geração contra a quota de quem
// SUBMETEU o pedido — o principal do `planrequest.submitted`, nunca o do drenador que reporta. Um
// pedido que o nó não conhece, ou sem titular, não reservou nada: nil, e não se liquida nada.
func (h *apiHandler) parcelaDePlaneamento(ctx context.Context, req pedidoDeDesfecho, estado estadoDePedido, achado bool) (*planeamentoDoPedido, error) {
	if !achado || estado.titular == "" {
		return nil, nil // sem titular não houve reserva (o `POST /plans` só reserva com principal)
	}
	if !slices.Contains(estado.reclamadas, req.Geracao) {
		return nil, errGeracaoNaoEntregue
	}
	var c consumoDoPlaneamento
	if req.Consumo != nil {
		c = consumoDoPlaneamento{
			Tokens: req.Consumo.Tokens, TokensMedidos: req.Consumo.TokensMedidos,
			CostMicroUSD: req.Consumo.CostMicroUSD, CustoMedido: req.Consumo.CustoMedido,
		}
	}
	if err := h.node.QuotaPorPrincipal.registarPlaneamento(ctx, estado.titular, req.RunID, req.Geracao, c); err != nil {
		return nil, err
	}
	return &planeamentoDoPedido{titular: estado.titular}, nil
}

// truncar limita o detalhe livre que o consumidor envia. O detalhe é diagnóstico, não contrato.
func truncar(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// pendentesNaFila conta os pedidos por drenar: o TOTAL, e quantos são do `submissor` dado.
//
// A `marca` é opcional (nil ⇒ lê tudo) porque há um chamador — a métrica em `api.go` — que não
// tem estado onde a guardar e para quem uma leitura completa ocasional não custa nada. Os dois
// caminhos QUENTES (`POST /plans` e a reclamação) passam-na.
//
// # PORQUE É QUE A CONTAGEM POR SUBMISSOR SAI DAQUI, E NÃO DE UMA FUNÇÃO PRÓPRIA
//
// A mesma razão que [projectarFilaComMarca] declara para a marca de água: a definição de «está na
// fila» estaria em dois sítios, e dois sítios derivam. Quem mudar a regra de filtragem muda as duas
// contagens com ela, sem ter de se lembrar.
//
// E NÃO CUSTA NADA: a projecção já devolve a fila inteira, pelo que contar por submissor é uma
// passagem sobre uma fatia que o tecto global limita. Não há mapa a manter nem caminho de libertação —
// ao contrário do tecto por-chamador do `POST /runs` (AOS-456a). Aqui a fonte de verdade é o log, e a
// contagem é derivada dele.
//
// # MAS HÁ TOCTOU, E A PRIMEIRA VERSÃO DESTE TICKET AFIRMAVA O CONTRÁRIO
//
// A leitura e o `Append` NÃO estão serializados: entre contar e gravar, outra goroutine pode gravar.
// O AOS-456a decide sob MUTEX no `submit`, pelo que a afirmação «não há TOCTOU, ao contrário do
// AOS-456a» era o INVERSO da verdade — este eixo tem a janela MAIS larga dos dois. Achado MÉDIO-1 de
// uma revisão adversarial independente.
//
// O excesso sob rajada depende da carga (duas séries de 5 corridas deram gamas diferentes), pelo que
// não se declara gama. O tecto GLOBAL tem a mesma janela — dívida herdada do AOS-423.
//
// O LIMITE REAL, e é este que se declara: **a quota é imposta a menos de `AOS_INGRESS_BURST`**. Uma
// rajada concorrente admite até ao burst antes de a projecção seguinte a ver. Com os defaults de
// produção (quota 125, burst 128, 400 concorrentes) o excesso medido foi **3 em 5 corridas de 5** —
// 1,02× a quota. Torna-se material para quem baixar a quota muito abaixo do burst, e é isso que o
// operador precisa de saber antes de a afinar.
//
// O `jaPendente` diz se o `runID` dado JÁ está na fila **submetido por este mesmo `submissor`**. Serve a
// guarda da re-submissão IDEMPOTENTE — ver [handlePlanRequest]. `runID` ou `submissor` vazios ⇒ `false`.
//
// Um `submissor` vazio devolve `doSubmissor == 0` e NUNCA a contagem dos pedidos sem principal:
// sem gate soberano composto todos os pedidos ficam com o principal vazio (ver
// [handlePlanRequest]), e contá-los como «de um submissor» faria o tecto por-submissor valer como
// tecto global para todos os chamadores somados — mais apertado do que o global e anunciado como
// equidade. O chamador é que decide não compor; esta função não adivinha.
func pendentesNaFila(ctx context.Context, store EventStorePort, marca *marcaDeAgua, submissor, runID string) (total, doSubmissor int, jaPendente bool, err error) {
	var desde uint64
	if marca != nil {
		desde = marca.desde()
	}
	eventos, err := store.Read(ctx, planRequestStream, desde)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return 0, 0, false, nil
		}
		return 0, 0, false, err
	}
	fila, nova := projectarFilaComMarca(eventos, time.Now().UTC())
	if marca != nil {
		marca.avancar(nova)
	}
	for i := range fila {
		if submissor != "" && fila[i].Payload.Principal == submissor {
			doSubmissor++
		}
		// DO PRÓPRIO SUBMISSOR, e só dele. Sem este filtro a isenção era um ORÁCULO DE EXISTÊNCIA: com a
		// quota cheia, um `run_id` pendente de OUTRA pessoa respondia 201 e um inexistente 429 — cross-
		// submissor e cross-região, em produção, contra o ADR-030 §2.1. Um retry de rede traz o mesmo
		// principal; quem re-submete o `run_id` de outro nunca devia estar isento.
		if runID != "" && fila[i].RunID == runID && fila[i].Payload.Principal == submissor {
			jaPendente = true
		}
	}
	return len(fila), doSubmissor, jaPendente, nil
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
