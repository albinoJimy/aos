package main

// SELO DAS ACÇÕES DE CONTROLO (achado A3 da auditoria de 2026-08-17).
//
// O QUE FECHA. Uma LEITURA de run ficava na hash-chain tamper-evidente (`gov.read`). Uma
// INTERVENÇÃO NA EXECUÇÃO — `pause`, `steer` — ficava só no Event Store. A acção mais
// consequente tinha o registo mais fraco, e a diferença não seguia a consequência: a decisão de
// exaustão, que não interrompe nada, já era selada em `governance.exhaustion`.
//
// O evento do Event Store carrega o `emitter_id` E a assinatura ed25519, pelo que ADULTERÁ-LO é
// detectável por quem tenha a pubkey do operador. O que faltava era a REMOÇÃO: o Event Store não
// é encadeado, e apagar o evento não parte cadeia nenhuma. O selo fecha isso.
//
// A APROVAÇÃO four-eyes tinha o mesmo defeito noutra forma: a cadeia selava que UM gate humano
// fora satisfeito (`human_gate: "satisfied"`) sem selar QUEM o satisfez — as identidades dos
// aprovadores e o grant não apareciam no WORM. Para uma autorização cujo propósito É o
// não-repúdio de quem autorizou, era a peça que faltava.

import (
	"context"
	"strings"
	"time"

	audit "github.com/aos-ref/platform/audit"
)

const (
	// controlSealPartition é a partição das acções do plano de CONTROLO. Separada de
	// `governance.exhaustion` de propósito: aquela regista uma RESPOSTA a uma pergunta do nó;
	// esta regista uma INTERVENÇÃO não solicitada sobre um run em curso. São autoridades com a
	// mesma chave e consequências diferentes, e a auditoria ganha em não as misturar.
	controlSealPartition = "governance.control"
	// controlSealToolID identifica a origem do registo (o canal, não uma tool).
	controlSealToolID = "gov.control"
	// controlRunResourceType é o tipo do recurso selado: o próprio run sobre o qual se agiu.
	controlRunResourceType = "run"
	// controlApproversObl transporta os aprovadores da cerimónia four-eyes. É o campo que
	// responde "quem autorizou" — sem ele o selo diz que houve aval e não de quem.
	controlApproversObl = "four_eyes.approvers"
	// controlGrantObl transporta o id do grant emitido, para amarrar o selo à evidência que
	// destrava a acção na retoma.
	controlGrantObl = "four_eyes.grant"
	// controlSealTimeout é o prazo PRÓPRIO do selo pós-efeito. Existe porque o selo deixou de
	// herdar o cancelamento do pedido (ver [apiHandler.sealControlAction]): sem prazo nenhum, um
	// store pendurado prenderia o handler para sempre. Generoso face a um fsync local e curto
	// face a uma sessão HTTP.
	controlSealTimeout = 5 * time.Second
)

// sealControlAction sela uma acção de controlo JÁ AUTENTICADA E JÁ APLICADA na hash-chain
// tamper-evidente.
//
// ORDEM, e porque é esta. O selo vem DEPOIS do efeito porque a autenticação do sinal (nonce
// de uso-único, assinatura sobre o tuplo) acontece DENTRO do canal — selar antes obrigaria a
// verificar duas vezes, e a segunda verificação consumiria o nonce e recusaria o próprio sinal
// que se quer registar.
//
// RESIDUAL DECLARADO: entre o efeito e o selo há uma janela. Se o WORM falhar, a acção
// aconteceu e o registo não existe. Não se devolve erro ao chamador nesse caso — a acção
// ACONTECEU, e responder erro levá-lo-ia a repetir o sinal, o que consumiria outro nonce e daria
// um replay: trocaria um registo em falta por um registo em falta MAIS um operador confuso. A
// falha é gritada no log, que é o canal que existe sempre.
//
// Só se selam acções que SURTIRAM EFEITO. Um sinal recusado (assinatura inválida, replay, alvo
// errado) não muda estado nenhum e não entra na cadeia — mesmo critério da decisão de exaustão.
// Selá-los daria a quem inunda o canal um vector para inchar o trilho.
func (h *apiHandler) sealControlAction(ctx context.Context, kind, runID, emitterID string, obrigacoes ...audit.Obligation) {
	if h.node == nil || h.node.WORM == nil {
		return // sem substrato tamper-evidente composto: nada a selar (modo de referência).
	}
	// O CONTEXTO DO PEDIDO NÃO PODE CANCELAR ESTE SELO (achado de revisão de segurança sobre
	// AOS-311). Desde que o `audit.FileStore.Append` respeita o `ctx` — o que é a correcção certa
	// para o caminho audit-BEFORE-effect do Reference Monitor, onde um prazo esgotado tem de dar
	// deny — este selo, que corre DEPOIS do efeito, passou a ser cancelável por quem o provoca:
	// um operador que emita `pause` assinado e corte a ligação TCP a seguir aplica a pausa e faz
	// o `Append` falhar, ficando o trilho `governance.control` sem quem exerceu a acção. É um
	// primitivo de supressão de auditoria, e a janela é pequena mas repetível.
	//
	// `WithoutCancel` preserva os valores do contexto (trace, deadline de shutdown do processo
	// não; o prazo próprio abaixo cobre o caso de o store estar pendurado) e larga o
	// cancelamento. É o idioma já usado em `packages/integration/budget.go` e em
	// `packages/substrate/sandbox/lifecycle.go` para exactamente esta situação: um efeito que já
	// aconteceu tem de ser registado mesmo que quem o pediu já se tenha ido embora.
	selCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlSealTimeout)
	defer cancel()
	rec := controlSealRecord(kind, runID, emitterID, h.cfg.now(), obrigacoes...)
	if _, err := h.node.WORM.Append(selCtx, rec); err != nil {
		// Nunca a assinatura nem o nonce: só o caminho e a classe de falha.
		h.svc.log("SELO DE CONTROLO EM FALTA: a accao %q sobre run=%q por %q FOI APLICADA mas NAO ficou na hash-chain: %v", kind, runID, emitterID, err)
	}
}

// approversObligation constrói a obrigação que nomeia quem aprovou. Vazia ⇒ nil, para não selar
// uma lista vazia que se leria como "aprovado por ninguém".
func approversObligation(approvers []string) []audit.Obligation {
	limpos := make([]string, 0, len(approvers))
	for _, a := range approvers {
		if a = strings.TrimSpace(a); a != "" {
			limpos = append(limpos, a)
		}
	}
	if len(limpos) == 0 {
		return nil
	}
	return []audit.Obligation{{Type: controlApproversObl, Fields: limpos}}
}

// controlSealRecord constrói o registo de uma acção de controlo. É partilhado por
// [apiHandler.sealControlAction] e por [NodeService.selarRetomaPeloCanal] porque o selo de uma
// retoma TEM de ser indistinguível do selo de uma pausa: um auditor compara-os, e uma forma
// diferente para a mesma classe de acção leria como duas coisas diferentes.
func controlSealRecord(kind, runID, emitterID string, ts time.Time, obrigacoes ...audit.Obligation) audit.AuditRecord {
	return audit.AuditRecord{
		Partition: controlSealPartition,
		Timestamp: ts.UTC(),
		// O veredicto é sobre a ACÇÃO DE GOVERNAÇÃO (foi exercida), não sobre o run.
		Decision:    audit.DecisionAllow,
		Reason:      "control_" + kind,
		Principal:   audit.Principal{NHIID: emitterID},
		Capability:  "control:" + kind,
		RunID:       runID,
		ToolID:      controlSealToolID,
		Resource:    audit.Resource{Type: controlRunResourceType, Value: runID},
		Obligations: obrigacoes,
	}
}

// selarRetomaPeloCanal sela na hash-chain a retoma que JÁ SURTIU EFEITO (AOS-304).
//
// PORQUE NÃO NO HANDLER, ao contrário das outras cinco acções de controlo. `handleResume` devolve
// 202 assim que `NodeService.Resume` re-submete o run; a retoma pelo canal acontece DEPOIS e
// noutra goroutine, dentro do `hostRun`, e pode ainda falhar (transição recusada, escrita de
// auditoria em erro). Selar no handler gravaria na cadeia tamper-evidente uma retoma que podia não
// ter acontecido — pior do que não selar, porque uma entrada FALSA numa cadeia cuja única
// propriedade é a fidedignidade estraga o registo inteiro, e não só esta linha.
//
// Aqui o efeito é certo: o `SteerChannel.Resume` devolveu sem erro, o `control.resume` é durável e
// a máquina transitou.
//
// A ORDEM É A MESMA DAS OUTRAS CINCO — selo DEPOIS do efeito —, e a falha do WORM também: grita-se
// no log e NÃO se devolve erro. Devolver erro aqui abortaria a hospedagem de um run que já foi
// retomado, trocando um registo em falta por um run preso.
func (s *NodeService) selarRetomaPeloCanal(ctx context.Context, runID, emitterID string) {
	if s.node == nil || s.node.WORM == nil {
		return // sem substrato tamper-evidente composto: nada a selar (modo de referência).
	}
	// Mesmo desacoplamento do cancelamento que [apiHandler.sealControlAction] (achado de revisão
	// de segurança sobre AOS-311): o efeito já aconteceu e o contexto do run pode morrer — por
	// término, aborto ou shutdown — entre a retoma e o selo. Um selo pós-efeito nunca pode ser
	// cancelado pelo que ele existe para registar.
	selCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlSealTimeout)
	defer cancel()
	rec := controlSealRecord("resume", runID, emitterID, time.Now())
	if _, err := s.node.WORM.Append(selCtx, rec); err != nil {
		// Nunca a assinatura nem o nonce: só a classe de falha.
		s.log("SELO DE CONTROLO EM FALTA: a retoma do run %q por %q FOI APLICADA mas NAO ficou na hash-chain: %v", runID, emitterID, err)
	}
}
