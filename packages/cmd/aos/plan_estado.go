package main

// plan_estado.go — QUEM SUBMETE UM PLANO PASSA A PODER VER O DESFECHO (AOS-430).
//
// # O PROBLEMA
//
// O `POST /plans` responde `201` e mais nada. O run de TOPO do plano nunca é hospedado pelo nó:
// o `aos-orq` só lhe submete os runs FILHOS, `<topo>~<nó>` (`childRunID`), e o de topo vive no
// Event Store dele, num volume separado por desenho. Logo `GET /runs/<topo>` dá **404**, sempre
// — e quem submeteu ficava sem nada por onde seguir o que pediu.
//
// # PORQUE É QUE ISTO NÃO VIOLA A NÃO-ORACULARIDADE, E A CITAÇÃO IMPORTA
//
// A objecção óbvia é o ADR-030 §2.1, que este eixo passou um ticket inteiro a dar-lhe casa: a
// fila **não é enumerável por construção**, e uma rota que devolva o estado de um pedido parece
// ser exactamente o oráculo que a reclamação existe para não ser.
//
// A regra, lida literalmente, é outra:
//
//	«Uma superfície HTTP do nó não revela a EXISTÊNCIA de um recurso A QUEM NÃO PODE AGIR
//	SOBRE ELE.»
//
// Quem submeteu **pode** agir sobre o seu pedido — foi ele que o criou e foi ele que escolheu o
// `run_id`. Servir-lhe o estado não lhe revela nada que ele já não soubesse. O que a regra
// proíbe é revelá-lo a OUTREM, e é isso que esta rota recusa: a comparação de titularidade é
// obrigatória, e a recusa é o **mesmo 404** que um pedido inexistente.
//
// Logo isto não emenda o ADR-030. É a sua aplicação.
//
// # O `Principal` TEM AQUI O SEU PRIMEIRO LEITOR
//
// `planRequestPayload.Principal` era gravado desde o AOS-417 e **nunca lido por código nenhum**.
// Passa a ser a fronteira de titularidade. Um campo gravado que ninguém lê é uma afirmação por
// verificar; a partir daqui tem consequência, e teste.
//
// # A ARMADILHA DA MARCA DE ÁGUA, QUE EU PRÓPRIO CRIEI NO AOS-429
//
// A projecção da fila passou a ler A PARTIR da marca de água — o `seq` acima do qual todos os
// pedidos estão terminados. Para contar pendentes isso é correcto e é o ponto.
//
// **Para esta rota seria exactamente o contrário do que se quer.** Um pedido TERMINADO está,
// por definição, abaixo da marca — e é precisamente o desfecho dele que quem submeteu vem
// procurar. Reutilizar o caminho quente devolveria «não existe» para todos os planos que
// acabaram, que é o pior resultado possível: uma resposta errada e indistinguível da certa.
//
// Por isso esta rota lê desde o princípio, e paga-o. O custo está declarado no AOS-430: é linear
// no histórico da fila, por chamada. Aceita-se porque a rota é autenticada (não há sondagem
// anónima), serve uma pergunta que um humano ou serviço faz de tempos a tempos, e não está em
// nenhum caminho quente. Se um dia estiver, a saída é persistir a marca — resíduo do AOS-429.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// Vocabulário do estado servido. É FECHADO, e não o `classe` cru do desfecho, porque um pedido
// que ainda não tem desfecho nenhum também tem estado — e «sem classe» não é resposta.
const (
	// EstadoPlanoPendente — submetido, à espera de quem o reclame (ou com a reclamação
	// expirada pelo TTL). É o estado em que um pedido fica se ninguém estiver a drenar a fila.
	EstadoPlanoPendente = "pending"
	// EstadoPlanoEmCurso — há uma reclamação VIVA: alguém está a trabalhá-lo agora.
	EstadoPlanoEmCurso = "in_progress"
	// EstadoPlanoTerminado — desfecho terminal. Acabou, bem ou mal; o `codigo_saida` diz qual.
	EstadoPlanoTerminado = "terminal"
	// EstadoPlanoAguardaHumano — parou à espera de uma decisão humana.
	EstadoPlanoAguardaHumano = "aguarda_humano"
)

// respostaDeEstadoDoPlano é o que a rota devolve.
//
// # O QUE NÃO ESTÁ AQUI, E PORQUÊ
//
// **O objectivo.** Está selado sob a KEK do titular desde o AOS-429, e devolvê-lo obrigaria a
// repetir o caminho de decifragem e o tratamento do Art. 17. Quem submeteu o objectivo já o tem;
// devolvê-lo seria criar uma segunda superfície de leitura de dados pessoais para não acrescentar
// informação nenhuma.
//
// **Os runs filhos e a trajectória.** Vivem no Event Store do `aos-orq`, noutro volume. O nó não
// os tem e não os pode inventar. Está declarado como resíduo no AOS-430.
type respostaDeEstadoDoPlano struct {
	RunID string `json:"run_id"`
	// Estado é um dos quatro valores acima.
	Estado string `json:"status"`
	// Geracao é a tentativa em que o pedido vai. Sobe a cada reclamação; um número alto diz a
	// quem submeteu que o pedido está a ser retentado, sem lhe dizer porquê.
	Geracao int `json:"generation"`
	// CodigoSaida e Detalhe vêm do desfecho reportado pelo consumidor, quando há.
	//
	// SEM `omitempty` no código: o `desfechoPayload` tem-no, e por isso o código 0 — SUCESSO —
	// não é gravado e volta a sair como 0 na desserialização. Por acaso está certo, mas por
	// acaso; omitir aqui faria «sucesso» e «sem desfecho» parecerem a mesma coisa no wire, e
	// essa distinção é a razão de existir do campo `status`.
	CodigoSaida int    `json:"exit_code"`
	Detalhe     string `json:"detail,omitempty"`
}

// handlePlanStatus serve o estado do pedido de plano DE QUEM PERGUNTA.
func (h *apiHandler) handlePlanStatus(w http.ResponseWriter, r *http.Request) {
	// O GATE SOBERANO É PRÉ-CONDIÇÃO, e a recusa é 501 e não 403 — a mesma postura das rotas
	// irmãs (`/plans/claim`, `/plans/outcome`): a diferença entre «não estás autorizado» e «este
	// nó não sabe autorizar ninguém» é diagnóstica, e escondê-la manda o operador procurar
	// credenciais quando o que falta é composição.
	if h.readGov == nil {
		writeError(w, http.StatusNotImplemented, "estado de pedidos de plano sem gate soberano composto")
		return
	}
	leitor, ok := h.readGov.authorize(r)
	if !ok || leitor.principal == "" {
		writeError(w, http.StatusForbidden, "nao autorizado")
		return
	}

	runID := strings.TrimSpace(r.PathValue("id"))
	if runID == "" || runIDInvalido(runID) || runIDReservado(runID) {
		// O MESMO 404 de «não existe». Um 400 aqui diria a quem sonda que a forma do id importa,
		// e a forma é conhecível — não é informação que valha a pena dar de graça.
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if h.node == nil || h.node.EventStore == nil {
		writeError(w, http.StatusServiceUnavailable, "indisponivel")
		return
	}

	estado, encontrado, err := estadoDoPedido(r.Context(), h.node.EventStore, runID, time.Now().UTC())
	if err != nil {
		h.logf("plan-status: leitura falhou run=%q principal=%q: %v", runID, leitor.principal, err)
		writeError(w, http.StatusServiceUnavailable, "estado indisponivel")
		return
	}

	// A FRONTEIRA DE TITULARIDADE, E É ELA QUE MANTÉM O ADR-030 §2.1 DE PÉ.
	//
	// As três recusas — não existe, não é teu, é de outra região — dão a MESMA resposta. Um
	// chamador não distingue «este plano não existe» de «existe e não é teu», que é exactamente
	// o que a regra exige. Se as separássemos, esta rota passaria a ser o oráculo que a
	// reclamação evita.
	if !encontrado || estado.titular != leitor.principal {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	// SOBERANIA (ADR-016 §5): um pedido submetido noutra região não é servido aqui, pela mesma
	// comparação por valor exacto que a reclamação usa. «Qualquer região» não é uma fronteira.
	if estado.regiao != "" && leitor.region != "" && estado.regiao != leitor.region {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	writeJSON(w, http.StatusOK, estado.resposta)
}

// estadoDePedido é o que a projecção sabe sobre UM pedido — a resposta mais o que decide se ela
// pode ser dada.
type estadoDePedido struct {
	resposta respostaDeEstadoDoPlano
	titular  string
	regiao   string
}

// estadoDoPedido projecta o estado de UM pedido a partir do log.
//
// Lê desde o princípio do stream, de propósito: ver a secção sobre a marca de água no cabeçalho
// deste ficheiro. É a diferença entre encontrar um plano terminado e dizer que ele não existe.
func estadoDoPedido(ctx context.Context, store EventStorePort, runID string, agora time.Time) (estadoDePedido, bool, error) {
	eventos, err := store.Read(ctx, planRequestStream, 0)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return estadoDePedido{}, false, nil // fila nunca usada
		}
		return estadoDePedido{}, false, err
	}

	var (
		achado      bool
		e           estadoDePedido
		maiorGer    int
		reclamadoEm = map[int]time.Time{}
		desfechoDe  = map[int]desfechoPayload{}
	)
	e.resposta.RunID = runID

	for _, ev := range eventos {
		switch {
		case ev.Type == EventTypePlanRequestSubmitted && ev.StepID == prefixoPedido+runID:
			var p planRequestPayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				// Payload ilegível: o pedido não é servível, e é a mesma resposta de não
				// existir. Não se inventa estado a partir de um facto que não se sabe ler.
				continue
			}
			achado = true
			e.titular = p.Principal
			e.regiao = p.Region
		case ev.Type == EventTypePlanRequestClaimed:
			ger, run, ok := partirChaveComGeracao(ev.StepID, prefixoReclamo)
			if !ok || run != runID {
				continue
			}
			if t, err := time.Parse(time.RFC3339Nano, ev.Ts); err == nil {
				reclamadoEm[ger] = t
			}
			if ger > maiorGer {
				maiorGer = ger
			}
		case ev.Type == EventTypePlanRequestOutcome:
			ger, run, ok := partirChaveComGeracao(ev.StepID, prefixoDesfecho)
			if !ok || run != runID {
				continue
			}
			var d desfechoPayload
			if err := json.Unmarshal(ev.Payload, &d); err != nil {
				continue
			}
			desfechoDe[ger] = d
			if ger > maiorGer {
				maiorGer = ger
			}
		}
	}

	if !achado {
		// Sem `submitted` não há pedido. Reclamações ou desfechos órfãos não fazem um — é a
		// mesma regra da projecção da fila: «não se serve o que nunca foi pedido».
		return estadoDePedido{}, false, nil
	}

	e.resposta.Geracao = maiorGer
	// O ESTADO DERIVA-SE PELA MESMA ORDEM DA PROJECÇÃO DA FILA, e tem de derivar: se as duas
	// discordassem, a rota diria «pendente» sobre um pedido que a fila já não serve, ou o
	// inverso. Terminal primeiro, depois reclamação viva, depois à-espera-de-humano, depois
	// pendente.
	//
	// O TERMINAL É O DE MAIOR GERAÇÃO, escolhido e não encontrado. Antes do AOS-442 a primeira
	// entrada do mapa que fosse terminal OU à-espera-de-humano ganhava — e a ordem de um mapa em
	// Go é aleatória. Com a re-oferta, um pedido pode ter `aguarda_humano` na geração 1 e
	// `terminal` na 3, e a resposta tem de ser sempre a segunda.
	terminal, gerTerminal := desfechoPayload{}, 0
	for ger, d := range desfechoDe {
		if d.Classe == DesfechoTerminal && ger > gerTerminal {
			terminal, gerTerminal = d, ger
		}
	}
	if gerTerminal > 0 {
		e.resposta.Estado = EstadoPlanoTerminado
		e.resposta.CodigoSaida = terminal.CodigoDe
		e.resposta.Detalhe = terminal.Detalhe
		return e, true, nil
	}
	for ger, em := range reclamadoEm {
		if _, houve := desfechoDe[ger]; houve {
			continue
		}
		if agora.Sub(em) < ttlDaReclamacao {
			e.resposta.Estado = EstadoPlanoEmCurso
			return e, true, nil
		}
	}
	// À ESPERA DE HUMANO quando é a ÚLTIMA geração que o diz — mesmo que a fila já o esteja a
	// re-oferecer (AOS-442). A re-oferta é mecânica de fila, para o consumidor verificar se a
	// decisão já existe; para quem submeteu, a verdade continua a ser «espera por um humano», até
	// uma tentativa posterior dizer outra coisa.
	if d, houve := desfechoDe[maiorGer]; houve && d.Classe == DesfechoAguardaHumano {
		e.resposta.Estado = EstadoPlanoAguardaHumano
		e.resposta.CodigoSaida = d.CodigoDe
		e.resposta.Detalhe = d.Detalhe
		return e, true, nil
	}
	// PENDENTE, e o último desfecho transitório (se houver) viaja com ele: é o que diz a quem
	// submeteu PORQUE é que o pedido ainda está na fila depois de tentativas.
	e.resposta.Estado = EstadoPlanoPendente
	if d, houve := desfechoDe[maiorGer]; houve {
		e.resposta.CodigoSaida = d.CodigoDe
		e.resposta.Detalhe = d.Detalhe
	}
	return e, true, nil
}
