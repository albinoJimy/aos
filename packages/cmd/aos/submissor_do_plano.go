package main

// submissor_do_plano.go — O SUBMISSOR DE UM PLANO VIAJA ATÉ AO RUN FILHO, E É O NÓ QUE O DERIVA
// (AOS-439, ADR-035).
//
// # O DEFEITO
//
// O `POST /plans` grava quem pediu (`planrequest.submitted.principal`). A partir daí o submissor
// PERDIA-SE: a reclamação não o devolvia, o `aos-orq` submetia cada run filho com o NHI do mandato,
// e a cadeia selada de cada tool call terminava no humano que assinou o mandato — seja quem for que
// pediu o plano. Medido em produção (2026-09-25, `plan-e2e-docread-1790340990`).
//
// # A FORMA: O VÍNCULO É DERIVADO, NUNCA ACEITE DO CORPO
//
// O `POST /runs` aceita um `plan_request` OPCIONAL: `{run_id: <plano>, generation: <g>}`. O corpo
// NÃO diz quem é o submissor — diz de que pedido o run é trabalho, e o nó VERIFICA-O contra o seu
// próprio log da fila:
//
//  1. existe `planrequest.submitted` para esse plano (e é dele que sai o submissor);
//  2. quem chama é drenador ([apiHandler.eDrenador]) e tem a reclamação VIVA da ÚLTIMA geração —
//     sem desfecho e dentro do [ttlDaReclamacao] — e é essa geração que o corpo nomeia;
//  3. a região do pedido é a do chamador;
//  4. o `run_id` tem a forma `<plano>~<nó>` ([separadorDoRunFilho] passa a CONTRATO do nó).
//
// Se tudo bate, o `requested_by` do run é o principal gravado no pedido. Se alguma coisa falha, a
// resposta é a MESMA 403 de todas as recusas da rota — as causas ficam no log. Sem o campo, nada
// muda: o run corre como sempre correu, sem submissor.
//
// # PORQUE É QUE ISTO NÃO É UM CAMPO `requested_by` NO CORPO
//
// Porque quem chama o `POST /runs` é o DRENADOR — o `aos-orq`, com o NHI do mandato —, e um campo
// no corpo seria o drenador a afirmar por quem age. Um drenador comprometido afirmaria o que
// quisesse. Com o vínculo, o drenador só consegue invocar um submissor que (a) pediu mesmo um
// plano, (b) cujo pedido ele tem reclamado AGORA, e (c) cujo nome o nó leu do SEU log.

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// separadorDoRunFilho é o separador do id de um run filho: `<plano>~<nó>`.
//
// É CONTRATO DO NÓ desde o AOS-439: o nó usa-o para verificar que um run é trabalho do plano que o
// vínculo nomeia. O `aos-orq` compõe-no em `childRunID` (`node_executor.go`, a mesma constante com
// o mesmo nome), e escapa o `~` dentro do `node_id` para a decomposição ser única.
// [TestAOS439SeparadorDoRunFilhoCasaComOOrquestrador] lê a constante do orquestrador e falha se as
// duas divergirem — os dois binários não se importam um ao outro.
const separadorDoRunFilho = "~"

// vinculoAoPedido é o campo `plan_request` do `POST /runs`: de que pedido de plano, e de que
// geração da reclamação, o run é trabalho. Não traz o submissor — esse lê-o o nó.
type vinculoAoPedido struct {
	RunID   string `json:"run_id"`
	Geracao int    `json:"generation"`
	// PlanID e NodeID são o plano e o nó de que o run é trabalho, DECLARADOS pelo drenador
	// (AOS-477). Opcionais e juntos; o nó confere-lhes a forma e grava-os no `run.plan_origin` do
	// run, sem os poder confrontar com o documento (ADR-018). Ver `plan_origem.go`.
	PlanID string `json:"plan_id,omitempty"`
	NodeID string `json:"node_id,omitempty"`
}

// codigoRequerenteForaDoMandato é o código do corpo da recusa em que o vínculo PASSOU mas o
// submissor não está nos `requesters` do mandato da credencial (AOS-439).
//
// É a ÚNICA recusa do `POST /runs` com código próprio, e não quebra a uniformidade das outras: só
// sai depois de o chamador ter provado que é o drenador com a reclamação viva do pedido — já
// autenticado, já autorizado a ver o pedido, e portador do mandato que o recusa. Não lhe revela nada
// que ele não tenha. Existe porque o `aos-orq` tem de distinguir esta recusa (determinista: o
// submissor não mudará) de uma credencial expirada (transitória), e fechar o pedido como terminal.
const codigoRequerenteForaDoMandato = "E_MANDATE_REQUESTER"

// errVinculoRecusado é a família das recusas do vínculo; a causa concreta vai no texto, para o log.
var errVinculoRecusado = errors.New("vinculo ao pedido de plano recusado")

// submissorDoPedido verifica o vínculo e devolve o submissor do pedido — o `requested_by` do run.
//
// A causa de uma recusa é para o LOG DO OPERADOR; a resposta ao chamador é uniforme.
func (h *apiHandler) submissorDoPedido(ctx context.Context, chamador readerIdentity, runID string, v vinculoAoPedido, agora time.Time) (string, error) {
	recusa := func(causa string) (string, error) {
		return "", errors.Join(errVinculoRecusado, errors.New(causa))
	}
	if h == nil || h.node == nil || h.node.EventStore == nil {
		return recusa("sem Event Store onde ler a fila")
	}
	if !h.eDrenador(chamador.principal) {
		return recusa("o chamador nao consta de AOS_PLAN_DRAINERS")
	}
	if v.RunID == "" || v.Geracao < 1 {
		return recusa("plan_request sem run_id ou generation")
	}
	if runIDReservado(v.RunID) || runIDInvalido(v.RunID) {
		return recusa("plan_request.run_id invalido")
	}
	// (4) A FORMA `<plano>~<nó>`: o run é trabalho DESTE plano, e o resto é um nó (sem outro `~`,
	// que o `aos-orq` escapa). Sem isto, a reclamação de um plano autorizava qualquer run_id.
	// O QUE A FORMA NÃO PROVA: que `<nó>` é um nó do documento aprovado — o nó não conhece o
	// documento (ADR-018). Uma reclamação viva serve para `<plano>~<qualquer-coisa>`, todos com o
	// mesmo submissor; declarado no ADR-035 §5.
	prefixo := v.RunID + separadorDoRunFilho
	if !strings.HasPrefix(runID, prefixo) || len(runID) == len(prefixo) || strings.Contains(runID[len(prefixo):], separadorDoRunFilho) {
		return recusa("run_id nao tem a forma <plano>" + separadorDoRunFilho + "<no> do plano nomeado")
	}

	// A marca de água só corta o prefixo de pedidos TERMINADOS — e um pedido com reclamação viva não
	// terminou —, pelo que ler a partir dela não perde nenhum facto deste pedido.
	eventos, err := h.node.EventStore.Read(ctx, planRequestStream, h.marcaDaFila.desde())
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return recusa("fila vazia: o plano nunca foi pedido")
		}
		return recusa("fila ilegivel: " + err.Error())
	}

	var (
		pedido       planRequestPayload
		submetido    bool
		maiorGeracao int
		reclamadoEm  = map[int]time.Time{}
		reclamadoPor = map[int]string{}
		desfechoDe   = map[int]string{}
	)
	for _, ev := range eventos {
		switch ev.Type {
		case EventTypePlanRequestSubmitted:
			if ev.StepID != prefixoPedido+v.RunID {
				continue
			}
			if json.Unmarshal(ev.Payload, &pedido) != nil {
				return recusa("pedido ilegivel")
			}
			submetido = true
		case EventTypePlanRequestClaimed:
			ger, alvo, ok := partirChaveComGeracao(ev.StepID, prefixoReclamo)
			if !ok || alvo != v.RunID {
				continue
			}
			var c struct {
				By string `json:"by"`
			}
			_ = json.Unmarshal(ev.Payload, &c)
			reclamadoPor[ger] = c.By
			if t, perr := time.Parse(time.RFC3339Nano, ev.Ts); perr == nil {
				reclamadoEm[ger] = t
			}
			if ger > maiorGeracao {
				maiorGeracao = ger
			}
		case EventTypePlanRequestOutcome:
			ger, alvo, ok := partirChaveComGeracao(ev.StepID, prefixoDesfecho)
			if !ok || alvo != v.RunID {
				continue
			}
			var d desfechoPayload
			_ = json.Unmarshal(ev.Payload, &d)
			desfechoDe[ger] = d.Classe
			if ger > maiorGeracao {
				maiorGeracao = ger
			}
		}
	}

	// (1) O plano foi pedido, e sabe-se por quem.
	if !submetido || pedido.Principal == "" {
		return recusa("sem planrequest.submitted com principal para o plano")
	}
	for _, classe := range desfechoDe {
		if classe == DesfechoTerminal {
			return recusa("o pedido ja terminou")
		}
	}
	// (2) A reclamação VIVA da ÚLTIMA geração, e é do chamador.
	if v.Geracao != maiorGeracao {
		return recusa("a geracao " + strconv.Itoa(v.Geracao) + " nao e a ultima (" + strconv.Itoa(maiorGeracao) + ")")
	}
	em, reclamado := reclamadoEm[v.Geracao]
	if !reclamado {
		return recusa("sem reclamacao legivel da geracao nomeada")
	}
	if _, houve := desfechoDe[v.Geracao]; houve {
		return recusa("a geracao nomeada ja tem desfecho")
	}
	if agora.Sub(em) >= ttlDaReclamacao {
		return recusa("a reclamacao da geracao nomeada expirou")
	}
	if reclamadoPor[v.Geracao] != chamador.principal {
		return recusa("a reclamacao viva nao e do chamador")
	}
	// (3) Soberania: o pedido entrou na região de quem o drena agora. Um pedido sem região (nó sem
	// gate soberano no ingresso) não se vincula — «qualquer região» não é uma fronteira.
	if pedido.Region == "" || pedido.Region != chamador.region {
		return recusa("regiao do pedido diferente da do chamador")
	}
	// (5) A origem DECLARADA (AOS-477): a forma, e o `node_id` tem de ser o do próprio `run_id`.
	// Recusa-se com a mesma 403: um `plan_id`/`node_id` que não bate é um drenador a mandar o que
	// não devia, e não há razão para lho distinguir.
	if err := validarOrigemDeclarada(v, runID); err != nil {
		return recusa(err.Error())
	}
	return pedido.Principal, nil
}
