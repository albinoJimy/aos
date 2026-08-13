package main

// AOS-263 (PARTE 3) — A DECISÃO SOBRE O PROMPT DE EXAUSTÃO: `continue` e `abort`.
//
// A parte 2 fez o aviso de burn-down SUSPENDER o run e SELAR uma pergunta durável; ficou por
// entregar a metade que lhe dá sentido — a via por onde um humano RESPONDE. Uma pergunta sem
// resposta possível é o mesmo defeito que AOS-262 tinha (mecanismo sem superfície), só que
// pior: aqui o run está PARADO à espera dela.
//
//	prompt de exaustão selado (parte 2)          ← a pergunta, em GET /runs/{id}
//	  └→ POST /runs/{id}/exhaustion  (AQUI)      ← a resposta, AUTENTICADA
//	       ├→ selo WORM da decisão (principal VERIFICADO, run, montante, razão)
//	       ├→ `abort`:    paragem DURÁVEL do run (waiting_on_human → killed)
//	       ├→ `continue`: o run fica RETOMÁVEL — e só então POST /runs/{id}/resume o aceita
//	       └→ o pendente sai da lista por DECISÃO (não por expiração)
//
// # AS DUAS METADES DA PERGUNTA TÊM A MESMA AUTORIDADE
//
// A pergunta é «parar ou continuar?», e as duas respostas entram pela MESMA rota, com a MESMA
// assinatura de operador pinado, o MESMO nonce durável e o MESMO selo WORM. É deliberado e
// é o ponto: a resposta ARRISCADA é a de continuar a queimar orçamento acima do limiar, e
// desenhá-la mais barata do que a segura (por exemplo, deixando-a entrar por uma retoma com
// credencial NHI, que é a MESMA classe de credencial com que o run já corria) daria uma
// decisão humana mais fraca do que o four-eyes já entregue — exactamente a regressão de
// postura que a decisão do dono (i) recusou, só que na outra perna.
//
// Por isso o `continue` NÃO é o `POST /runs/{id}/resume`: a retoma é a EXECUÇÃO (re-hospedar
// o run com credencial fresca), não a DECISÃO. Enquanto a pergunta estiver por responder, a
// retoma recusa ([NodeService.Resume]); depois de um `continue` selado, aceita. Sem esse
// travão o prompt seria contornável por quem já tinha credencial do run, e o pendente ficaria
// na lista até o varrimento anunciar no log «expirado sem decisao» sobre uma pergunta que foi
// respondida.
//
// # AUTORIDADE — paridade com o `pause`, sem esquema novo (decisão do dono (ii))
//
// A admissão é a MESMA de [apiHandler.handleApprove] e [apiHandler.handlePause], pela mesma
// ordem: (1) [apiHandler.admitControl] — token-bucket dedicado do plano de controlo, ANTES de
// qualquer descodificação ou verificação de assinatura; (2) [apiHandler.admitControlMTLS] —
// mTLS de controlo quando composto; (3) NON-SIGNING — a autenticação REAL é a assinatura
// ed25519 que vem no corpo, verificada pelo [integration.Ed25519Authenticator] que o nó já
// compõe a partir de AOS_OPERATORS, com o nonce-store DURÁVEL (uso-único que sobrevive a
// restart) e a janela de frescura. Nada disto é reimplementado aqui: é o MESMO autenticador,
// o MESMO nonce-store e a MESMA janela que governam o /steer e o /pause.
//
// # PORQUE A DECISÃO NÃO VIAJA COMO UM `pause` (a confusão de assinatura que isso abriria)
//
// A tentação óbvia era chamar [control.SteerChannel.Pause] e deixá-lo autenticar. Não se faz,
// e a razão é concreta: um sinal de pause é assinado sobre (run ‖ "pause" ‖ ∅ ‖ nonce ‖
// issued_at) — não diz nada sobre QUE decisão se tomou. Um pause legítimo capturado ANTES de
// ser gasto poderia ser submetido nesta rota como um `abort`, e o efeito passaria de «parar de
// forma retomável» para «terminar o run». Quem assina tem de assinar O QUE decide.
//
// Por isso a decisão é assinada sob um KIND PRÓPRIO ([exhaustionDecisionSignalKind]) e sobre
// um payload que AMARRA a decisão ao pendente concreto ([exhaustionDecisionPayload]). O kind
// é deliberadamente um que o [control.SteerChannel] NÃO conhece: a separação vale nos dois
// sentidos — uma assinatura de decisão não passa por pause/steer/resume no canal, e nenhuma
// destas passa por decisão aqui. O nonce, esse, é de uso-único no MESMO âmbito (run, emissor)
// de todo o canal de controlo, pelo que também não há reutilização cruzada.
//
// # O `abort` — sobre o que já existe, nunca um kill novo
//
// O `abort` NÃO constrói caminho de paragem nenhum. Reusa as duas paragens duráveis que o nó
// já tem, e escolhe entre elas pelo ESTADO em que o run está:
//
//   - run SUSPENSO em `waiting_on_human` (o estado em que o próprio prompt o pôs, e o único em
//     que a pergunta faz sentido): materializa waiting_on_human → `killed`, a ÚNICA aresta de
//     paragem que a tabela declarativa de AOS-017 dá a esse estado e a MESMA que ADR-013 já usa
//     no timeout fail-closed do gate humano. TERMINAL, no vocabulário de AOS-252. A razão
//     gravada ([reasonExhaustionAbort]) é distinta da do timeout para o log atribuir a causa
//     certa — não é um mecanismo novo, é o mesmo com o motivo certo;
//   - run que VOLTOU A CORRER com a pergunta em aberto (é possível: quem retoma pode fazê-lo
//     sem responder — ver [exhaustionPrompt.raise]): a rota RECUSA (409) e NOMEIA a PAUSA
//     GRACIOSA que já existe, `POST /runs/{id}/pause`. É deliberado. Matar um run a meio de um
//     turno seria exactamente o kill novo que esta entrega não quer: a pausa graciosa pára-o na
//     FRONTEIRA DE FIM DE TURNO (sem efeitos parciais) e deixa-o em `paused` — RETOMÁVEL. As
//     duas metades de «o run pára de forma retomável-ou-terminal» são estas, e cada uma é
//     conduzida pelo mecanismo que já a sabia fazer.
//
// # O QUE A ROTA NÃO OFERECE, E PORQUÊ (declarado, não omitido em silêncio)
//
//   - `extend` — DEFERIDO por decisão do dono ((iii), 2026-08-12), com eixo registado em
//     docs/governance/REGISTO-Deferimentos.md (DEF-220, AOS-263). O [budget.Budget] não tem
//     mutador de tecto e os LIMITES são, por desenho declarado, configuração FORA do log de
//     eventos (packages/control-plane/budget/events.go) — não reconstruíveis por `Rebuild`.
//     Levantar o tecto exigiria quebrar essa decisão de desenho, com evento próprio e ADR;
//   - `summarize_stop` — FORA das opções apresentadas. Não tem executor: o loop não tem
//     caminho de resumo, e um «pára e resume o que fizeste» sem quem o resuma seria uma
//     paragem comum com um nome que promete mais. Fica DECLARADO aqui, no banner
//     ([exhaustionPromptPostureBanner]) e em deploy/node/README.md — não no wire: o que o
//     prompt apresenta são só opções EXECUTÁVEIS, e uma lista de «não-oferecidas» dentro da
//     resposta convidaria um cliente a tentá-las.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	integration "github.com/aos-ref/integration"
	control "github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/kernel/agent-runtime/state"
	audit "github.com/aos-ref/platform/audit"
)

// Vocabulário ESTÁVEL desta entrega (rótulos de auditoria e de wire — nunca segredos).
const (
	// exhaustionOptionAbort é o identificador da decisão que PÁRA o run. Tem executor: esta
	// rota.
	exhaustionOptionAbort = "abort"
	// exhaustionOptionContinue é o identificador da decisão que deixa o run PROSSEGUIR além do
	// limiar. Tem executor: esta rota — que a sela e a regista, destrancando a re-hospedagem
	// por [exhaustionResumeRoute]. NÃO transita o run: um run suspenso continua suspenso até
	// alguém o re-hospedar com credencial fresca, e é bom que seja assim (a decisão de
	// governação e a execução com credencial são actos distintos, de autoridades distintas).
	exhaustionOptionContinue = "continue"
	// exhaustionDecisionRoute é a rota que executa AMBAS as decisões. Viaja no wire com cada
	// opção — uma opção sem rota é indistinguível de uma promessa.
	exhaustionDecisionRoute = "POST /runs/{id}/exhaustion"
	// exhaustionGracefulPauseRoute é a pausa graciosa JÁ EXISTENTE. É nomeada na recusa de um
	// abort sobre um run que voltou a correr: o operador não fica sem via, fica com a via
	// certa para o estado em que o run está.
	exhaustionGracefulPauseRoute = "POST /runs/{id}/pause"
	// reasonExhaustionAbort é o motivo gravado na transição waiting_on_human→killed do abort e
	// no selo WORM da decisão. DISTINTO do motivo do timeout humano (ADR-013) e do da suspensão
	// ([reasonExhaustionPrompt]) — a mesma aresta, causas diferentes, atribuição legível.
	reasonExhaustionAbort = "budget_exhaustion_abort"
	// reasonExhaustionContinue é a razão selada quando o operador decide DEIXAR CORRER. Rótulo
	// estável e próprio: um auditor que procure «quem autorizou este run a passar do limiar»
	// tem de o encontrar por um nome, não pela ausência de um abort.
	reasonExhaustionContinue = "budget_exhaustion_continue"
	// reasonExhaustionAbortFailed é a razão do registo COMPENSATÓRIO: o abort foi selado mas o
	// efeito não se materializou. Sem ele a cadeia afirmaria um facto que não ocorreu (ver
	// [apiHandler.sealExhaustionAbortFailure]).
	reasonExhaustionAbortFailed = "budget_exhaustion_abort_failed"

	// exhaustionDecisionSignalKind é o domínio de assinatura das decisões desta rota. NÃO é
	// nenhum dos kinds do [control.SteerChannel] (pause/steer/resume) — e é essa a defesa: o
	// kind entra no tuplo assinado, pelo que uma assinatura produzida para um pause nunca
	// valida como decisão e vice-versa.
	exhaustionDecisionSignalKind control.SignalKind = "exhaustion_decision"
	// exhaustionDecisionDomain é a etiqueta de domínio no payload canónico assinado. Separa
	// estes bytes de qualquer outro payload que o mesmo autenticador venha a cobrir.
	exhaustionDecisionDomain = "aos263:exhaustion-decision"

	// exhaustionDecisionPartition é a cadeia WORM DEDICADA das decisões sobre prompts de
	// exaustão (isolada ⇒ forma a sua própria cadeia gapless verificável, no molde de
	// [legalHoldPartition]).
	exhaustionDecisionPartition = "governance.exhaustion"
	// capExhaustionAbort é a capability exercida pelo principal ao abortar.
	capExhaustionAbort = "exhaustion:abort"
	// capExhaustionContinue é a capability exercida ao autorizar o run a prosseguir. É PRÓPRIA
	// e não a do abort: são autoridades diferentes sobre a mesma pergunta, e uma cadeia que as
	// confundisse não permitiria auditar «quem deixou correr» separadamente de «quem parou».
	capExhaustionContinue = "exhaustion:continue"
	// exhaustionDecisionToolID nomeia o produtor do selo (sem PII).
	exhaustionDecisionToolID = "gov.exhaustion"
	// exhaustionDecisionObl é a obrigação que transporta a AMARRA da decisão: os números do
	// burn-down que a justificaram.
	exhaustionDecisionObl = "gov.exhaustion.burndown"
	// exhaustionRunResourceType nomeia o alvo da decisão: o próprio run (identificador opaco).
	exhaustionRunResourceType = "aos.run"
)

var (
	// ErrExhaustionRunNotWaiting — pediu-se o abort de um run que NÃO está durávelmente
	// suspenso à espera de humano. Fail-closed: não se mata um run vivo por esta via (ver o
	// cabeçalho — a via de um run vivo é a pausa graciosa).
	ErrExhaustionRunNotWaiting = errors.New("aos: run nao esta suspenso em waiting_on_human — o abort de AOS-263 nao mata um run vivo")
	// ErrExhaustionPromptNotFound — não há prompt de exaustão POR RESPONDER com este
	// (run, step). Sem ele não há o que decidir — e, sobretudo, não há de onde ler o montante
	// consumido que o selo exige (que NÃO se recalcula aqui: a fonte é o aviso de AOS-262).
	ErrExhaustionPromptNotFound = errors.New("aos: sem prompt de exaustao por responder para este run/step")
)

// exhaustionDecisionRequest é o corpo de `POST /runs/{id}/exhaustion`.
//
// O `step_id` NÃO é decorativo: é ele que amarra a assinatura a UMA pergunta concreta. Sem ele,
// uma decisão assinada para o prompt de um turno valeria para qualquer prompt futuro do mesmo
// run — e o operador estaria a assinar um cheque em branco sobre perguntas que ainda não viu.
type exhaustionDecisionRequest struct {
	Decision string      `json:"decision"`
	StepID   string      `json:"step_id"`
	Emitter  emitterWire `json:"emitter"`
}

// exhaustionDecisionPayload é o payload CANÓNICO que a assinatura do operador cobre, além do
// (run ‖ kind ‖ nonce ‖ issued_at) que o [integration.Ed25519Authenticator] já cobre.
//
// Codificação INJECTIVA por length-prefix (8 bytes big-endian por campo), pela MESMA razão que
// [integration.SignSignal] a usa no tuplo exterior: com um separador de byte único, dois pares
// (decisão, passo) distintos poderiam produzir a MESMA sequência de bytes se o separador
// ocorresse dentro de um campo — e uma assinatura válida para um deles valeria para o outro. O
// `step_id` vem do cliente, pelo que a sua forma não é de confiança; o comprimento fixa a
// fronteira e a ambiguidade desaparece.
func exhaustionDecisionPayload(decision, stepID string) []byte {
	campos := []string{exhaustionDecisionDomain, decision, stepID}
	total := 0
	for _, c := range campos {
		total += 8 + len(c)
	}
	out := make([]byte, 0, total)
	var lb [8]byte
	for _, c := range campos {
		binary.BigEndian.PutUint64(lb[:], uint64(len(c)))
		out = append(out, lb[:]...)
		out = append(out, c...)
	}
	return out
}

// handleExhaustionDecision RESPONDE a um prompt de exaustão de orçamento (AOS-263).
//
// Fluxo, cada passo fail-closed e por esta ordem exacta:
//
//  1. admissão do plano de controlo (429) e mTLS de controlo quando composto (403);
//  2. wire (400) — vocabulário FECHADO de decisões: só o que esta rota EXECUTA;
//  3. maquinaria composta (501) — sem autenticador, sem registo de pendentes, sem gates de
//     estado ou sem WORM, a rota está DESLIGADA. Nenhuma delas se pode contornar: uma decisão
//     que não se consegue autenticar, ou que não se consegue SELAR, não é uma decisão;
//  4. AUTENTICAÇÃO (403) — assinatura ed25519 + frescura + nonce durável de uso-único. É aqui,
//     e só aqui, que `emitter.id` deixa de ser uma alegação e passa a ser o PRINCIPAL;
//  5. o prompt TEM de existir por responder (404 uniforme) — é dele que sai o montante
//     consumido, que NÃO se recalcula;
//  6. o run TEM de estar durávelmente em `waiting_on_human` (409 nomeando a pausa graciosa);
//  7. SELO WORM da decisão. É PRÉ-CONDIÇÃO do efeito: se a hash-chain não aceitar o registo, o
//     run NÃO é abortado (500). Um efeito irreversível sem registo inviolável é precisamente o
//     que o WORM existe para impedir; o inverso — um registo de uma tentativa que falhou a
//     seguir — é auditável e recuperável;
//  8. EFEITO, conforme a decisão: `abort` ⇒ paragem DURÁVEL (waiting_on_human → killed);
//     `continue` ⇒ nenhuma transição, o run fica retomável. Em ambas, a pergunta sai da lista
//     POR DECISÃO — nunca por expiração.
//
// RESPOSTA UNIFORME nas recusas de autenticação: assinatura inválida, emissor desconhecido,
// replay e stale partilham o MESMO 403 sem dizer qual foi — o corpo não é um oráculo. Quem
// precisa do detalhe tem o log do nó e o WORM.
func (h *apiHandler) handleExhaustionDecision(w http.ResponseWriter, r *http.Request) {
	if !h.admitControl(w) {
		return
	}
	if !h.admitControlMTLS(w, r) {
		return
	}
	runID := r.PathValue("id")
	if runID == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var req exhaustionDecisionRequest
	if status, ok := h.decodeJSON(w, r, &req); !ok {
		writeError(w, status, "corpo invalido")
		return
	}
	decision := strings.TrimSpace(req.Decision)
	stepID := strings.TrimSpace(req.StepID)
	if stepID == "" {
		writeError(w, http.StatusBadRequest, "step_id obrigatorio (identifica a pergunta a que se responde)")
		return
	}
	// VOCABULÁRIO FECHADO — só o que esta rota EXECUTA. `resume` NÃO é uma decisão e é
	// recusado com a distinção nomeada: é a EXECUÇÃO que se segue a um `continue` decidido, e
	// exige uma credencial NHI fresca que esta rota deliberadamente não transporta (misturar
	// um bearer token com uma decisão assinada juntaria duas autoridades diferentes no mesmo
	// corpo).
	if decision != exhaustionOptionAbort && decision != exhaustionOptionContinue {
		if decision == "resume" {
			writeError(w, http.StatusBadRequest, "\"resume\" nao e uma decisao: e a re-hospedagem em "+exhaustionResumeRoute+" (exige credencial fresca; esta rota nao a transporta), e so passa a ser aceite depois de um \""+exhaustionOptionContinue+"\" decidido aqui")
			return
		}
		writeError(w, http.StatusBadRequest, "decisao desconhecida (esta rota executa: "+exhaustionOptionContinue+", "+exhaustionOptionAbort+")")
		return
	}
	emitter, err := req.Emitter.decode()
	if err != nil {
		writeError(w, http.StatusBadRequest, "emitter invalido")
		return
	}

	if h.node == nil || h.node.SteerAuth == nil || h.node.PendingApprovals == nil ||
		h.node.stateGates == nil || h.node.WORM == nil {
		writeError(w, http.StatusNotImplemented, "decisao de exaustao desligada (canal de controlo autenticado, registo de pendentes, gates de estado e WORM sao todos obrigatorios)")
		return
	}

	// (4) AUTENTICAÇÃO — o MESMO autenticador, nonce-store durável e janela de frescura do
	// /steer e do /pause. A assinatura cobre (run ‖ kind ‖ decisão+passo ‖ nonce ‖ issued_at):
	// nem a decisão nem o alvo são adulteráveis sem invalidar a assinatura, e o nonce é
	// consumido DURAVELMENTE (um replay não repete o efeito nem depois de um restart).
	if err := h.node.SteerAuth.Authenticate(r.Context(), runID, exhaustionDecisionSignalKind,
		exhaustionDecisionPayload(decision, stepID), emitter); err != nil {
		// Nunca o valor da assinatura nem do nonce: só o caminho e a classe de recusa.
		h.svc.log("decisao de exaustao (AOS-263) RECUSADA na autenticacao: run=%q emissor=%q: %v", runID, emitter.ID, err)
		writeError(w, http.StatusForbidden, "decisao recusada")
		return
	}
	// A PARTIR DAQUI `emitter.ID` é o PRINCIPAL VERIFICADO: a assinatura validou contra a
	// pubkey PINADA desse ID (AOS_OPERATORS). Antes disto era só um campo do corpo.
	principal := emitter.ID

	// (5) A pergunta tem de existir POR RESPONDER. É a fonte do montante consumido — os
	// números do aviso de AOS-262, tal-qual, nunca recalculados aqui (duas contabilidades a
	// poder divergir seria o operador a decidir sobre a errada).
	prompt, err := h.exhaustionPendingFor(r.Context(), runID, stepID)
	if err != nil {
		if errors.Is(err, ErrExhaustionPromptNotFound) {
			// 404 UNIFORME: não distingue "nunca houve" de "já foi respondido/expirou" — o
			// mesmo não-enumerável do resto do read-path.
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		h.svc.log("decisao de exaustao (AOS-263): ler pendentes do run %q falhou: %v", runID, err)
		writeError(w, http.StatusInternalServerError, "decisao indisponivel")
		return
	}

	// (6) O ESTADO DURÁVEL manda. Um run que voltou a correr não se mata por aqui.
	st, err := h.node.stateGates.currentState(r.Context(), runID)
	if err != nil {
		h.svc.log("decisao de exaustao (AOS-263): ler o estado duravel do run %q falhou: %v", runID, err)
		writeError(w, http.StatusInternalServerError, "decisao indisponivel")
		return
	}
	if st != state.WaitingOnHuman {
		// As DUAS decisões exigem o run suspenso, por razões diferentes: o abort porque não
		// mata um run vivo (a via desse é a pausa graciosa), o continue porque autorizar a
		// continuação de um run que já está a correr seria selar uma decisão sem objecto.
		if decision == exhaustionOptionAbort {
			writeError(w, http.StatusConflict, "run em "+string(st)+" — o abort so decide um run SUSPENSO; para parar um run vivo use "+exhaustionGracefulPauseRoute+" (pausa graciosa: pára na fronteira de fim-de-turno e deixa o run RETOMAVEL)")
			return
		}
		writeError(w, http.StatusConflict, "run em "+string(st)+" — o "+exhaustionOptionContinue+" so decide um run SUSPENSO a espera desta resposta")
		return
	}

	// (7) SELO WORM — PRÉ-CONDIÇÃO do efeito.
	seq, err := h.sealExhaustionDecision(r.Context(), runID, principal, decision, prompt)
	if err != nil {
		h.svc.log("decisao de exaustao (AOS-263) NAO SELADA no WORM (run %q, principal %q, decisao %q) — NADA foi executado: %v", runID, principal, decision, err)
		writeError(w, http.StatusInternalServerError, "decisao nao selada — nada foi executado")
		return
	}

	if decision == exhaustionOptionContinue {
		h.finishExhaustionContinue(w, r, runID, stepID, principal, seq, prompt)
		return
	}

	// (8) EFEITO DO ABORT: paragem durável + retirada do pendente POR DECISÃO.
	if err := h.node.stateGates.killFromWaitingOnHuman(r.Context(), runID, reasonExhaustionAbort); err != nil {
		h.svc.log("decisao de exaustao (AOS-263): abortar o run %q falhou APOS o selo (audit_seq=%d) — o run continua suspenso: %v", runID, seq, err)
		// O selo diz «este principal abortou este run». O efeito não aconteceu. Sem um registo
		// COMPENSATÓRIO, a cadeia de não-repúdio afirmaria um facto que não ocorreu e a única
		// desmentida viveria no log do nó — que não é superfície de não-repúdio.
		h.sealExhaustionAbortFailure(r.Context(), runID, principal, seq, prompt, err)
		if errors.Is(err, ErrExhaustionRunNotWaiting) {
			// CORRIDA, e as duas formas dela: o run foi RETOMADO entre a leitura do estado e a
			// transição (e aí a tabela declarativa recusa `running → killed` — nada é destruído a
			// meio de um turno), ou outra decisão concorrente já o parou. Em ambas, o estado
			// durável mudou debaixo deste pedido e nada aqui o força.
			writeError(w, http.StatusConflict, "o run deixou de estar suspenso entretanto (retomado, ou ja parado por outra decisao) — se voltou a correr, a via e "+exhaustionGracefulPauseRoute)
			return
		}
		writeError(w, http.StatusInternalServerError, "abort falhou")
		return
	}
	// O balde de suspensos é um CACHE do que o log diz. Depois do abort o log diz `killed`:
	// deixá-lo lá faria o GET continuar a reportar `waiting_on_human` e o POST /resume
	// re-hospedar um run terminado. Corre DEPOIS da transição durável — a verdade primeiro,
	// o cache a seguir.
	h.svc.esqueceSuspensao(runID)
	// A pergunta sai da lista por DECISÃO, não por expiração. Uma falha aqui NÃO desfaz o
	// abort (que está selado e materializado) e por isso NÃO vira erro para o cliente: o
	// resíduo é uma entrada a mais na lista de trabalho, que o varrimento de pendentes limpa.
	// Dizer "falhou" a um operador cujo run FOI abortado seria a mentira pior das duas.
	if err := h.node.PendingApprovals.Decide(r.Context(), integration.PendingKindExhaustion, runID, stepID, decision, principal); err != nil {
		h.svc.log("decisao de exaustao (AOS-263): o run %q FOI abortado e selado (audit_seq=%d) mas o pendente nao saiu da lista por decisao (sai depois pelo TTL): %v", runID, seq, err)
	}

	h.svc.log("ABORT POR EXAUSTAO (AOS-263) — run %q TERMINADO em killed por decisao de %q: %s do tecto consumidos (limiar %.2f, turno %d). Decisao selada no WORM (particao %q, audit_seq=%d). NAO e retomavel: killed e terminal absorvente",
		runID, principal, exhaustionAmounts(prompt), prompt.Threshold, prompt.Turn, exhaustionDecisionPartition, seq)

	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":   runID,
		"decision": decision,
		"status":   "aborted",
		// state é o estado DURÁVEL resultante — o vocabulário de AOS-252, não um rótulo novo.
		"state": string(state.Killed),
		// principal e audit_seq são a via de acesso à evidência: com eles o operador encontra o
		// selo na cadeia. Material público.
		"principal": principal,
		"audit_seq": seq,
	})
}

// finishExhaustionContinue materializa a decisão de DEIXAR CORRER, já selada no WORM.
//
// O efeito é UM SÓ e é o registo de decisão: retirar a pergunta da lista. Não há transição de
// estado a fazer — o run continua `waiting_on_human`, que é onde tem de estar até alguém o
// re-hospedar com credencial fresca — e não há cache a limpar, porque nada mudou no desfecho.
//
// AO CONTRÁRIO DO ABORT, uma falha aqui É ERRO PARA O CLIENTE (500). No abort, o efeito
// irreversível já tinha acontecido e dizer "falhou" seria a pior das mentiras; aqui o
// [integration.PendingApprovals.Decide] é o efeito: é ele que destranca a retoma
// ([NodeService.Resume] recusa enquanto a pergunta estiver por responder). Um 200 sobre um
// Decide falhado devolveria ao operador um "pode continuar" que o nó continuaria a recusar,
// e a pergunta acabaria "expirada sem decisao" no log apesar de respondida.
func (h *apiHandler) finishExhaustionContinue(w http.ResponseWriter, r *http.Request, runID, stepID, principal string, seq uint64, prompt integration.PendingRecord) {
	if err := h.node.PendingApprovals.Decide(r.Context(), integration.PendingKindExhaustion, runID, stepID, exhaustionOptionContinue, principal); err != nil {
		h.svc.log("decisao de exaustao (AOS-263): o %q do run %q foi SELADO (audit_seq=%d) mas o pendente nao saiu da lista por decisao — a retoma continua barrada ate o TTL expirar a pergunta: %v",
			exhaustionOptionContinue, runID, seq, err)
		writeError(w, http.StatusInternalServerError, "decisao selada mas nao registada na lista de trabalho — a retoma continua barrada")
		return
	}
	h.svc.log("CONTINUAR APOS EXAUSTAO (AOS-263) — run %q AUTORIZADO a prosseguir por decisao de %q: %s do tecto consumidos (limiar %.2f, turno %d). Decisao selada no WORM (particao %q, audit_seq=%d). O run continua SUSPENSO e agora RETOMAVEL: a re-hospedagem e %s com credencial fresca",
		runID, principal, exhaustionAmounts(prompt), prompt.Threshold, prompt.Turn,
		exhaustionDecisionPartition, seq, exhaustionResumeRoute)

	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":   runID,
		"decision": exhaustionOptionContinue,
		"status":   "resumable",
		// O estado DURÁVEL NÃO mudou — dizê-lo é o que impede o operador de julgar que o run já
		// voltou a correr.
		"state":     string(state.WaitingOnHuman),
		"principal": principal,
		"audit_seq": seq,
		// A execução que se segue à decisão. Sem esta linha o operador ficaria com uma decisão
		// tomada e sem saber por onde o run recomeça.
		"next": exhaustionResumeRoute,
	})
}

// exhaustionPendingFor devolve o prompt de exaustão POR RESPONDER de (run, step). Lê a MESMA
// listagem que a superfície de administração expõe — [integration.PendingApprovals.ListForRun]
// já exclui expirados e decididos —, pelo que aquilo sobre que se decide é exactamente aquilo
// que o operador viu por decidir.
//
// Só devolve o tipo [integration.PendingKindExhaustion]: uma APROVAÇÃO com o mesmo step_id não
// serve de alvo a um abort (é outra decisão, com outra amarra e outra rota) — e aceitá-la seria
// a confusão de tipo que a parte 1 fechou na store.
func (h *apiHandler) exhaustionPendingFor(ctx context.Context, runID, stepID string) (integration.PendingRecord, error) {
	recs, err := h.node.PendingApprovals.ListForRun(ctx, runID)
	if err != nil {
		return integration.PendingRecord{}, err
	}
	for _, rec := range recs {
		if rec.Kind.Resolved() == integration.PendingKindExhaustion && rec.StepID == stepID {
			return rec, nil
		}
	}
	return integration.PendingRecord{}, fmt.Errorf("%w: run=%q step=%q", ErrExhaustionPromptNotFound, runID, stepID)
}

// sealExhaustionDecision sela a decisão na hash-chain WORM e devolve o audit_seq. Um erro
// propaga-se: o chamador NEGA a acção fail-closed (molde de [apiHandler.sealLegalHold]).
//
// O que o selo tem de conter, e porquê cada campo:
//
//   - PRINCIPAL — o emissor cuja assinatura ed25519 VALIDOU. É o não-repúdio: sem ele o registo
//     prova que um run foi morto mas não por quem;
//   - RUN e STEP — o alvo, e a PERGUNTA concreta a que se respondeu;
//   - o MONTANTE CONSUMIDO (com o tecto, o limiar e o turno) — os números do aviso de AOS-262
//     tal como o pendente os selou. É o CONTEXTO da decisão: sem ele o selo diz que alguém
//     abortou, não que alguém abortou perante 800 de 1000 tokens queimados;
//   - a RAZÃO — [reasonExhaustionAbort], um rótulo ESTÁVEL de enumeração fechada.
//
// NÃO há texto livre do operador, e é deliberado: a assinatura cobre (decisão, passo), não uma
// nota. Selar uma frase não assinada ao lado de um principal verificado poria na cadeia de
// não-repúdio palavras que esse principal não assinou — e que um intermediário poderia ter
// trocado sem invalidar nada.
func (h *apiHandler) sealExhaustionDecision(ctx context.Context, runID, principal, decision string, prompt integration.PendingRecord) (uint64, error) {
	// A RAZÃO e a CAPABILITY são as da decisão TOMADA, não de um genérico "decidiu": um
	// auditor tem de poder pedir à cadeia «quem autorizou runs a passar o limiar» sem ter de
	// abrir as obrigações de cada registo.
	razao, capacidade := reasonExhaustionAbort, capExhaustionAbort
	if decision == exhaustionOptionContinue {
		razao, capacidade = reasonExhaustionContinue, capExhaustionContinue
	}
	rec := audit.AuditRecord{
		Partition: exhaustionDecisionPartition,
		Timestamp: h.cfg.now().UTC(),
		// A decisão humana foi AUTORIZADA e executada — o veredicto do registo é sobre a acção
		// de governação (parar ou deixar correr), não sobre o run.
		Decision:   audit.DecisionAllow,
		Reason:     razao,
		Principal:  audit.Principal{NHIID: principal},
		Capability: capacidade,
		RunID:      runID,
		StepID:     prompt.StepID,
		ToolID:     exhaustionDecisionToolID,
		Resource:   audit.Resource{Type: exhaustionRunResourceType, Value: runID},
		Obligations: []audit.Obligation{{
			Type: exhaustionDecisionObl,
			// O selo grava a AMARRA COMPLETA da pergunta: as duas dimensões e a RAZÃO. Sem a
			// dimensão $ e sem a razão, um selo de `continue` sobre um run bloqueado pelo tecto
			// em dólares registaria para sempre números de tokens que não explicam a decisão —
			// e o WORM é precisamente onde essa explicação tem de ficar. Os pares de $ só
			// aparecem quando há tecto em $ (`omitempty` do registo ⇒ zero aqui), pela mesma
			// razão de não escrever um denominador que ninguém configurou.
			Params: exhaustionSealParams(decision, prompt),
		}},
	}
	sealed, err := h.node.WORM.Append(ctx, rec)
	if err != nil {
		return 0, err
	}
	return sealed.AuditSeq, nil
}

// exhaustionSealParams monta os parâmetros da obrigação do selo de decisão — a AMARRA da
// pergunta, tal como ela foi registada.
//
// Os pares de $ e a razão só entram quando EXISTEM: um `limit_cost_micro_usd` a zero num nó sem
// tecto em dólares seria um denominador que ninguém configurou a fingir-se de facto, e é essa
// classe de zero-que-parece-medição que o canal de custo de AOS-259 declara não escrever.
func exhaustionSealParams(decision string, prompt integration.PendingRecord) map[string]string {
	params := map[string]string{
		"decision":        decision,
		"threshold":       strconv.FormatFloat(prompt.Threshold, 'f', -1, 64),
		"consumed_tokens": strconv.FormatInt(prompt.ConsumedTokens, 10),
		"limit_tokens":    strconv.FormatInt(prompt.LimitTokens, 10),
		"turn":            strconv.Itoa(prompt.Turn),
	}
	if prompt.LimitCostMicroUSD > 0 {
		params["consumed_cost_micro_usd"] = strconv.FormatInt(prompt.ConsumedCostMicroUSD, 10)
		params["limit_cost_micro_usd"] = strconv.FormatInt(prompt.LimitCostMicroUSD, 10)
	}
	if prompt.Reason != "" {
		params["reason_detail"] = prompt.Reason
	}
	return params
}

// exhaustionAmounts descreve, numa linha de log, o consumido/tecto nas dimensões que TÊM tecto.
// Molde (e razão) de [burndownDimensoes]: quando é a dimensão $ que decide, uma linha só com
// tokens contradiz a decisão que está a relatar.
func exhaustionAmounts(prompt integration.PendingRecord) string {
	linha := fmt.Sprintf("%d de %d tokens", prompt.ConsumedTokens, prompt.LimitTokens)
	if prompt.LimitCostMicroUSD > 0 {
		linha += fmt.Sprintf(" e %d de %d micro-USD", prompt.ConsumedCostMicroUSD, prompt.LimitCostMicroUSD)
	}
	return linha
}

// sealExhaustionAbortFailure escreve o registo COMPENSATÓRIO de um abort que foi selado e NÃO
// se materializou (corrida com uma retoma, transição recusada pela tabela declarativa, falha
// do log de estado).
//
// PORQUE EXISTE: o selo do abort é pré-condição do efeito e é escrito ANTES dele — a ordem
// certa, porque um efeito irreversível sem registo inviolável é o que o WORM existe para
// impedir. Mas ela deixa uma janela: se o efeito falhar, a cadeia fica com «este principal
// abortou este run» sobre um run que continua vivo ou suspenso. Um auditor que leia só a
// cadeia — que é o que a cadeia promete — lê um facto que não aconteceu. Esta segunda linha
// é a desmentida, DENTRO da mesma cadeia gapless, a apontar o `audit_seq` que corrige.
//
// `DecisionDeny` é o veredicto certo: o que o registo diz é que a acção de governação NÃO se
// materializou. Best-effort declarado — se nem esta linha entrar, resta o log do nó; mas o
// caminho em que o WORM aceita a primeira e recusa a segunda é estreito, e o efeito NÃO
// aconteceu (não há nada irreversível por cobrir).
func (h *apiHandler) sealExhaustionAbortFailure(ctx context.Context, runID, principal string, seq uint64, prompt integration.PendingRecord, causa error) {
	rec := audit.AuditRecord{
		Partition:  exhaustionDecisionPartition,
		Timestamp:  h.cfg.now().UTC(),
		Decision:   audit.DecisionDeny,
		Reason:     reasonExhaustionAbortFailed,
		Principal:  audit.Principal{NHIID: principal},
		Capability: capExhaustionAbort,
		RunID:      runID,
		StepID:     prompt.StepID,
		ToolID:     exhaustionDecisionToolID,
		Resource:   audit.Resource{Type: exhaustionRunResourceType, Value: runID},
		Obligations: []audit.Obligation{{
			Type: exhaustionDecisionObl,
			Params: map[string]string{
				"decision": exhaustionOptionAbort,
				// O selo que este registo CORRIGE. Sem a referência, as duas linhas seriam dois
				// factos soltos e caberia ao auditor adivinhar que uma anula a outra.
				"corrects_audit_seq": strconv.FormatUint(seq, 10),
				// Classe da falha, nunca o erro cru: um erro interno no WORM seria superfície de
				// diagnóstico para quem só devia ver decisões.
				"failure": exhaustionAbortFailureClass(causa),
			},
		}},
	}
	if _, err := h.node.WORM.Append(ctx, rec); err != nil {
		h.svc.log("decisao de exaustao (AOS-263): o registo COMPENSATORIO do abort falhado do run %q (corrige audit_seq=%d) NAO entrou na cadeia — a desmentida fica so neste log: %v", runID, seq, err)
	}
}

// exhaustionAbortFailureClass reduz a causa a um rótulo de ENUMERAÇÃO FECHADA. O texto de um
// erro interno não é vocabulário de auditoria: muda com refactorings e pode transportar
// detalhe que não pertence a uma cadeia lida por terceiros.
func exhaustionAbortFailureClass(err error) string {
	if errors.Is(err, ErrExhaustionRunNotWaiting) {
		return "run_deixou_de_estar_suspenso"
	}
	return "transicao_duravel_falhou"
}

// killFromWaitingOnHuman materializa a paragem TERMINAL de um run suspenso: waiting_on_human →
// killed, pela [state.Machine] reconstruída DO LOG.
//
// Reconstrói em vez de usar um gate aberto porque, num run suspenso, gate aberto não há: o
// [NodeService.hostRun] fecha-o ao sair ([runStateGates.Close]) e a verdade da suspensão fica
// no log — é a mesma leitura que [runStateGates.currentState] faz, agora com uma transição a
// seguir.
//
// FAIL-CLOSED em dobro, e a segunda camada não é redundante: a pré-condição explícita dá um
// erro NOMEADO (e um 409 legível) quando o run não está suspenso, e a TABELA DECLARATIVA de
// AOS-017 é a rede de segurança contra a corrida — se o run for retomado entre o Rebuild e a
// transição, `running → killed` não existe na tabela e [state.ErrInvalidTransition] recusa-a
// sem tocar no log. Um abort NUNCA mata um run a meio de um turno.
func (g *runStateGates) killFromWaitingOnHuman(ctx context.Context, runID, reason string) error {
	if g == nil {
		return ErrNoStateGateForRun
	}
	var opts []state.Option
	if g.tracer != nil {
		opts = append(opts, state.WithTracer(g.tracer))
	}
	m, err := state.NewMachine(g.store, runID, opts...)
	if err != nil {
		return err
	}
	st, err := m.Rebuild(ctx)
	if err != nil {
		return err
	}
	if st != state.WaitingOnHuman {
		return fmt.Errorf("%w: run %q em %q", ErrExhaustionRunNotWaiting, runID, st)
	}
	return m.Kill(ctx, state.TransitionEvent{Reason: reason})
}

// esqueceSuspensao retira um run do balde de SUSPENSOS em memória. É o gémeo em cache da
// paragem durável do abort: o balde é o que faz [NodeService.Suspended] responder
// `waiting_on_human` e o que [NodeService.Resume] consulta primeiro, pelo que um run já
// terminado que lá ficasse continuaria a anunciar-se retomável — e seria re-hospedado.
//
// NÃO o arquiva em `completed`: um run abortado não "completou", e reportá-lo assim seria a
// mentira operacional que AOS-252 veio corrigir. Sem entrada em memória, o GET cai no DESFECHO
// DURÁVEL e lê do log o que ele diz — `killed`. O log é a verdade; o mapa era só o atalho.
//
// Devolve se o run estava mesmo no balde (falso numa réplica que não o hospedou — a suspensão
// é durável e decidível a partir de qualquer réplica).
func (s *NodeService) esqueceSuspensao(runID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.suspended[runID]
	delete(s.suspended, runID)
	return ok
}
