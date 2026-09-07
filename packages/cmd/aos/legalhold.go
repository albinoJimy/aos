// AOS-213 (CON-02/DEF-903) — SUPERFÍCIE DE ADMINISTRAÇÃO de legal hold e expiração. Fecha a
// lacuna que a Opção C do dono sequenciou para DEPOIS de o apagamento ser real: o [audit.LegalHold]
// estava composto ([Node.DSARHolds]) mas SEM rota de administração (um operador não colocava/
// levantava um hold sem código) e o [audit.ExpirationJob] não era conduzível de fora. Este ficheiro
// expõe três rotas AUTENTICADAS, exactamente na disciplina do POST /dsar/erase (ver dsar.go):
//
//   - POST /dsar/hold    — coloca um legal hold (por titular e/ou partição) que SUSPENDE o
//     apagamento e a expiração desse titular/partição (o fluxo DSAR re-consulta-o antes de cada
//     shred; o ExpirationJob salta os held).
//   - POST /dsar/release — levanta o legal hold, reabrindo o titular/partição ao erase/expiração.
//   - POST /dsar/expire  — conduz UMA passagem do [audit.ExpirationJob] (varre os registos
//     classificados do Event Store, expira os que cruzaram o TTL e não estão sob hold, por
//     crypto-shred da KEK por-titular — apagamento REAL, não no-op).
//
// AUTENTICAÇÃO — a MESMA credencial forte do /dsar/erase (readGov.authorize, AOS-205): sem o gate
// soberano composto ⇒ 501; credencial ausente/forjada/board desconhecido ⇒ 403. Um header
// auto-declarado NÃO autoriza. Passa também pelo token-bucket do plano de CONTROLO (admitControl).
//
// CONTRATO subject_id/partition = PSEUDÓNIMO/IDENTIFICADOR OPACO: rejeita PII ANTES de encaminhar
// (defesa em profundidade — [validPseudonym]), porque o valor é selado VERBATIM na hash-chain WORM
// imutável, que o próprio crypto-shredding NÃO consegue remover.
//
// SELO WORM SEM PII — cada acção de hold/release é selada na partição [legalHoldPartition]
// (quem/quando/subject-pseudónimo/partição/board), tamper-evident, ANTES de a acção ser aplicada
// (fail-closed: se o WORM não selar, a acção não acontece ⇒ 503; para o hold isso significa não
// afirmar uma preservação não-auditada, para o release significa manter a preservação em vigor).
package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	integration "github.com/aos-ref/integration"
	control "github.com/aos-ref/kernel/agent-runtime/control"
	audit "github.com/aos-ref/platform/audit"
)

// Partições WORM dedicadas (isoladas ⇒ cada uma forma a sua própria cadeia gapless verificável).
const (
	// legalHoldPartition é a cadeia das acções administrativas de legal hold (place/release).
	legalHoldPartition = "governance.legalhold"
	// retentionPartition é a cadeia dos eventos retention.expired do [audit.ExpirationJob] no nó.
	retentionPartition = "governance.retention"
)

// Vocabulário estável dos selos de legal hold (sem PII).
const (
	capLegalHoldPlace   = "legalhold:place"
	capLegalHoldRelease = "legalhold:release"

	legalHoldToolID       = "gov.legalhold"       // produtor do selo (ToolID), sem PII
	legalHoldBoardObl     = "gov.legalhold.board" // board do operador (identificador de governação)
	legalHoldTargetObl    = "gov.legalhold.target"
	subjectResourceType   = "dsar.subject"
	partitionResourceType = "dsar.partition"
)

// holdRequestWire é a representação de wire de um pedido de legal hold. NÃO carrega valores
// pessoais: SubjectID é o PSEUDÓNIMO opaco do titular (o mesmo que ancora a KEK no vault) e
// Partition é o identificador OPACO do stream/partição — nunca o dado pessoal em si. Pelo menos
// um dos dois é obrigatório.
type holdRequestWire struct {
	RequestID string `json:"request_id"`
	SubjectID string `json:"subject_id,omitempty"`
	Partition string `json:"partition,omitempty"`
	// Emitter é a PROVA DE AUTORIDADE (AOS-367): a assinatura ed25519 do operador com `dsar:erase`
	// sobre o payload canónico (acção "hold"/"release"‖alvo‖request_id). Só EXIGIDA quando
	// AOS_DSAR_ERASERS está composto; ignorada (zero) na via legada por leitura.
	Emitter emitterWire `json:"emitter"`
}

// expireRequestWire transporta as DUAS assinaturas do dual-control da expiração em massa (AOS-367).
// O corpo é opcional na via legada (AOS_DSAR_ERASERS vazio ⇒ nada se decodifica); quando a prova
// está composta, ambos os campos são obrigatórios.
type expireRequestWire struct {
	RequestID string `json:"request_id,omitempty"`
	// Emitter é a PRIMEIRA assinatura de um eraser sobre o payload ("expire"‖""‖request_id).
	Emitter emitterWire `json:"emitter"`
	// CoEmitter é a SEGUNDA assinatura, de um eraser DISTINTO, sobre o MESMO payload — a expiração
	// em massa não tem alvo único, pelo que a barreira de região é substituída por dual-control.
	CoEmitter *emitterWire `json:"co_emitter,omitempty"`
}

// holdResponse é o desfecho SEM PII de uma acção de legal hold: o alvo (pseudónimo/opaco), o
// estado resultante e o audit_seq selado (prova de auditabilidade).
type holdResponse struct {
	RequestID string `json:"request_id"`
	SubjectID string `json:"subject_id,omitempty"`
	Partition string `json:"partition,omitempty"`
	Status    string `json:"status"` // "held" | "released"
	Seq       uint64 `json:"seq,omitempty"`
}

// expireResponse resume UMA passagem do [audit.ExpirationJob] SEM PII: só contagens (nunca os
// titulares/ids expirados, que seriam sensíveis).
type expireResponse struct {
	Scanned    int `json:"scanned"`
	Expired    int `json:"expired"`
	Held       int `json:"held"`
	Skipped    int `json:"skipped"`
	NotExpired int `json:"not_expired"`
}

// handleHold coloca um legal hold. Ver [handleLegalHold].
func (h *apiHandler) handleHold(w http.ResponseWriter, r *http.Request) {
	h.handleLegalHold(w, r, true)
}

// handleRelease levanta um legal hold. Ver [handleLegalHold].
func (h *apiHandler) handleRelease(w http.ResponseWriter, r *http.Request) {
	h.handleLegalHold(w, r, false)
}

// handleLegalHold satisfaz um pedido de colocar (place=true) ou levantar (place=false) um legal
// hold. Fail-closed em cada porta, na MESMA disciplina de handleDSAR:
//
//  1. admission do plano de CONTROLO (token-bucket dedicado);
//  2. o legal hold TEM de estar composto (senão 501);
//  3. AUTENTICAÇÃO de governação — reutiliza o gate soberano de leitura (credencial forte AOS-205);
//     gate não composto ⇒ 501; credencial ausente/forjada ⇒ 403;
//  4. decodifica o pedido (subject pseudónimo / partição opaca, sem PII) sob limite de corpo, e
//     impõe o contrato do pseudónimo/identificador opaco;
//  5. SELA a acção no WORM (sem PII) ANTES de a aplicar (fail-closed: WORM não sela ⇒ 503, acção
//     não acontece); depois aplica no [audit.LegalHold].
func (h *apiHandler) handleLegalHold(w http.ResponseWriter, r *http.Request, place bool) {
	// (1) ADMISSION do plano de controlo: vem da TABELA DE ROTAS (planoGovernacao, planos.go) — e
	// aplica-se em /dsar/hold e /dsar/release, as rotas REGISTADAS que entram por aqui.
	// (2) O legal hold tem de estar composto.
	if h.node.DSARHolds == nil {
		writeError(w, http.StatusNotImplemented, "legal hold desligado (nao composto)")
		return
	}
	// (3) AUTENTICAÇÃO de governação (credencial forte, como o /dsar/erase).
	if h.readGov == nil {
		writeError(w, http.StatusNotImplemented, "legal hold desligado (governanca soberana nao composta)")
		return
	}
	reader, ok := h.readGov.authorize(r)
	if !ok {
		writeError(w, http.StatusForbidden, "nao autorizado")
		return
	}
	// (4) LIMITE DE CORPO + descodificação e contrato do pseudónimo/identificador opaco.
	var req holdRequestWire
	if status, ok := h.decodeJSON(w, r, &req); !ok {
		writeError(w, status, "corpo invalido")
		return
	}
	if req.SubjectID == "" && req.Partition == "" {
		writeError(w, http.StatusBadRequest, "subject_id ou partition em falta")
		return
	}
	if req.SubjectID != "" && !validPseudonym(req.SubjectID) {
		writeError(w, http.StatusBadRequest, "subject_id invalido (esperado pseudonimo opaco)")
		return
	}
	if req.Partition != "" && !validPseudonym(req.Partition) {
		writeError(w, http.StatusBadRequest, "partition invalida (esperado identificador opaco)")
		return
	}
	// (4c) PROVA DE AUTORIDADE (AOS-367). Opt-in por composição de AOS_DSAR_ERASERS. A acção
	// ("hold"/"release") entra no payload assinado — uma assinatura de hold não se reapresenta como
	// release. O alvo é o titular (ou, sem titular, a partição). VEM DEPOIS de `authorize` e ANTES
	// do efeito (a barreira de destruição e a selagem).
	acao := "hold"
	if !place {
		acao = "release"
	}
	alvo := req.SubjectID
	if alvo == "" {
		alvo = req.Partition
	}
	if _, ok := h.exigeAutoridadeDSAR(w, r, req.Emitter, acao, alvo, req.RequestID); !ok {
		return
	}
	// (4d) BARREIRA DE REGIÃO no /dsar/release (AOS-367), no molde de [readGovernance.podeApagarTitular]
	// que o /dsar/erase já aplica (ver dsar.go). Um chamador de outra região NÃO pode levantar a
	// preservação de um alvo cuja residência a fronteira devia proteger — levantar o hold é remover o
	// que trava o varredor automático (agnóstico de região) de o destruir. Só o RELEASE (place=false):
	// um hold nunca é menos seguro por atravessar regiões. Cobre AMBOS os alvos — o titular (quantifica
	// sobre os seus runs) E a partição só-de-partição (residência da própria partição) —, porque a
	// partição é um alvo tão legítimo como o titular e deixá-la de fora era um desvio cross-region.
	// Cross-region ⇒ 403 e o hold MANTÉM-SE (não se chega a chamar ReleaseSubject/ReleasePartition).
	if !place {
		switch {
		case req.SubjectID != "":
			if !h.readGov.podeApagarTitular(r.Context(), reader, req.SubjectID, h.node.DSARIndex) {
				writeError(w, http.StatusForbidden, "nao autorizado")
				return
			}
		case req.Partition != "":
			if !h.readGov.podeLibertarParticao(r.Context(), reader, req.Partition) {
				writeError(w, http.StatusForbidden, "nao autorizado")
				return
			}
		}
	}
	// (5) SELA primeiro (facto auditável, sem PII); só depois aplica (fail-closed). Se o WORM não
	// selar, a acção NÃO acontece: para o hold, não se afirma uma preservação não-auditada; para o
	// release, a preservação em vigor MANTÉM-SE (nunca se reabre um titular ao apagamento sem o
	// registo de que o hold foi levantado).
	//
	// BARREIRA DE DESTRUIÇÃO (ver [audit.LegalHold.BeginDestruction]), tomada em modo EXCLUSIVO e
	// a ENVOLVER a selagem E a aplicação. Era aqui que estava a segunda metade do defeito: a
	// selagem é um `fsync` de 21–58 ms e acontece ANTES de `HoldSubject`, portanto durante a sua
	// própria selagem o hold NÃO vigorava — e o varredor, que avalia `held()` no topo do ciclo e
	// destrói ~30 ms depois, podia destruir material pelo qual este operador já esperava um 200.
	//
	// Com a barreira, o 200 significa o que parece significar: nenhuma destruição posterior deixa
	// de ver este hold. O custo é esperar, no máximo, por UM passo de destruição em voo.
	fimBarreira := h.node.DSARHolds.BeginPlacement()
	defer fimBarreira()

	seq, err := h.sealLegalHold(r.Context(), reader, req, place)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "indisponivel")
		return
	}
	status := "held"
	if place {
		if req.SubjectID != "" {
			h.node.DSARHolds.HoldSubject(req.SubjectID)
		}
		if req.Partition != "" {
			h.node.DSARHolds.HoldPartition(req.Partition)
		}
	} else {
		if req.SubjectID != "" {
			h.node.DSARHolds.ReleaseSubject(req.SubjectID)
		}
		if req.Partition != "" {
			h.node.DSARHolds.ReleasePartition(req.Partition)
		}
		status = "released"
	}
	writeJSON(w, http.StatusOK, holdResponse{
		RequestID: req.RequestID, SubjectID: req.SubjectID, Partition: req.Partition,
		Status: status, Seq: seq,
	})
}

// sealLegalHold sela a acção de legal hold na hash-chain WORM (partição [legalHoldPartition]),
// SEM PII: quem (principal de governação), quando, o alvo (subject pseudónimo / partição opaca), a
// região resolvida e o board do operador (identificador de governação). Devolve o audit_seq
// selado. Um erro de Append propaga-se (o chamador NEGA a acção fail-closed).
func (h *apiHandler) sealLegalHold(ctx context.Context, reader readerIdentity, req holdRequestWire, place bool) (uint64, error) {
	capability := capLegalHoldPlace
	if !place {
		capability = capLegalHoldRelease
	}
	// Resource nomeia o alvo PRIMÁRIO (titular se presente, senão partição) — ambos opacos.
	resType, resValue := subjectResourceType, req.SubjectID
	if req.SubjectID == "" {
		resType, resValue = partitionResourceType, req.Partition
	}
	// Params carregam ambos os alvos (pseudónimos/opacos), nunca PII.
	params := make(map[string]string, 2)
	if req.SubjectID != "" {
		params["subject_id"] = req.SubjectID
	}
	if req.Partition != "" {
		params["partition"] = req.Partition
	}
	rec := audit.AuditRecord{
		Partition:  legalHoldPartition,
		Timestamp:  h.cfg.now().UTC(),
		Decision:   audit.DecisionAllow,
		Principal:  audit.Principal{NHIID: reader.principal},
		Capability: capability,
		RequestID:  req.RequestID,
		ToolID:     legalHoldToolID,
		Resource:   audit.Resource{Type: resType, Value: resValue, Region: reader.region},
		Obligations: []audit.Obligation{
			{Type: legalHoldBoardObl, Fields: []string{reader.board}},
			{Type: legalHoldTargetObl, Params: params},
		},
	}
	sealed, err := h.node.WORM.Append(ctx, rec)
	if h.svc != nil {
		err = h.svc.seloWORM.registar(err)
	}
	if err != nil {
		return 0, err
	}
	return sealed.AuditSeq, nil
}

// handleExpire conduz UMA passagem do [audit.ExpirationJob] composto no nó (AOS-213). Fail-closed:
// admission do plano de controlo; job não composto ⇒ 501; gate soberano não composto ⇒ 501;
// credencial forte ausente/forjada ⇒ 403; hash-chain do WORM adulterada PÓS-SHRED ⇒ 500 (AOS-221,
// paridade com /dsar/erase). A expiração RESPEITA o legal hold (o job salta os held) e MATERIALIZA
// a expiração por crypto-shred da KEK por-titular (apagamento real). Devolve as contagens da
// passagem SEM PII.
//
// SERIALIZAÇÃO (AOS-213): o Run do job é pensado para uma execução de cada vez — o seu ciclo faz
// idem.Seen(key) e só mais tarde idem.Add(key) (após o Append ao WORM), sem atomicidade
// check-then-act ao nível do registo. Duas passagens concorrentes poderiam, para o MESMO registo,
// ver ambas Seen==false e selar DOIS eventos retention.expired para o mesmo facto (a hash-chain
// mantém-se válida e o crypto-shred é idempotente, mas a cadeia de auditoria ficaria poluída). O
// guard [NodeService.expireInFlight] admite UMA passagem activa; uma segunda invocação concorrente
// recebe 409 (no-op). O admitControl (token-bucket) limita a taxa mas NÃO serializa.
//
// O guard vive no SERVIÇO (AOS-267) e não no handler: desde que o scheduler interno conduz a mesma
// passagem, a exclusão tem de ser entre a ROTA e o TICK, não apenas entre invocações da rota.
func (h *apiHandler) handleExpire(w http.ResponseWriter, r *http.Request) {
	if h.node.ExpirationJob == nil {
		writeError(w, http.StatusNotImplemented, "expiracao desligada (job nao composto)")
		return
	}
	if h.readGov == nil {
		writeError(w, http.StatusNotImplemented, "expiracao desligada (governanca soberana nao composta)")
		return
	}
	// QUEM dispara a expiração em massa fica SELADO, ANTES de ela correr.
	//
	// O DEFEITO QUE FECHA, e é o pior dos dois achados de atribuição: esta rota destrói KEKs de
	// TODOS os titulares fora do TTL e não selava atribuição NENHUMA. O `sealRetentionSweep` só
	// tinha chamadores no varredor AUTOMÁTICO — que sela a sua NHI própria antes de correr e
	// RECUSA a passagem se o WORM não aceitar («sem o quem selado, a passagem NÃO corre»).
	//
	// Ou seja: o caminho sem humano registava quem; o caminho COM humano não registava ninguém.
	//
	// Mesma postura do varredor: FAIL-CLOSED. Se o WORM não aceitar o selo de atribuição, a
	// expiração não corre. Um apagamento em massa que a cadeia não consegue atribuir não deve
	// acontecer.
	leitor, ok := h.readGov.authorize(r)
	if !ok {
		writeError(w, http.StatusForbidden, "nao autorizado")
		return
	}
	// PROVA DE AUTORIDADE + DUAL-CONTROL (AOS-367). Opt-in por composição de AOS_DSAR_ERASERS, e
	// VIVE SÓ AQUI, no handler HTTP: o varredor AUTOMÁTICO (retention_sweeper.go, sweepRetentionOnce)
	// fica INTACTO — a exigência é sobre quem ORDENA uma expiração em massa por rota, não sobre o
	// tick agendado. Colocada ANTES do `expireInFlight` para que um pedido sem prova não tome sequer
	// o guard de serialização.
	//
	// A expiração é um varrimento GLOBAL por TTL, sem alvo único — a `podeApagarTitular` (que
	// quantifica sobre a residência de UM titular) não se aplica. A barreira de região que o AC
	// permite toma aqui a forma de DUAL-CONTROL: DUAS assinaturas de erasers DISTINTOS, no molde do
	// /autonomy para L4/L5. Uma expiração em massa é a acção com o maior alcance de destruição do nó,
	// e é a que menos deve poder ser ordenada por uma pessoa só.
	if len(h.node.DSARErasers) > 0 {
		if h.node.SteerAuth == nil {
			writeError(w, http.StatusNotImplemented, "canal de controlo sem autenticador")
			return
		}
		var req expireRequestWire
		if status, ok := h.decodeJSON(w, r, &req); !ok {
			writeError(w, status, "corpo invalido")
			return
		}
		// PRIMEIRA assinatura pelo helper partilhado (capability + autenticação sobre o payload).
		em, ok := h.exigeAutoridadeDSAR(w, r, req.Emitter, "expire", "", req.RequestID)
		if !ok {
			return
		}
		// SEGUNDA assinatura, de um eraser DISTINTO, sobre o MESMO payload. A distinção de pubkeys
		// entre emitterIDs é garantida no arranque ([parseOperators]/[Bootstrap] abortam com pubkey
		// partilhada), pelo que dois ids são duas chaves. Aqui a mensagem NÃO é uniforme de propósito:
		// a exigência é postura declarada, e o operador que assinou sozinho precisa de saber o que
		// falta (molde do /autonomy).
		if req.CoEmitter == nil {
			writeError(w, http.StatusForbidden, "expiracao em massa exige duas assinaturas de erasers distintos (co_emitter em falta)")
			return
		}
		co, err := req.CoEmitter.decode()
		if err != nil {
			writeError(w, http.StatusBadRequest, "co_emitter invalido")
			return
		}
		if co.ID == em.ID || !h.node.DSARErasers[co.ID] {
			h.logf("DSAR (AOS-367): expiracao em massa RECUSADA — co_emitter %q e o mesmo emissor ou NAO detem %s", co.ID, dsarEraseCapability)
			writeError(w, http.StatusForbidden, "nao autorizado")
			return
		}
		if err := h.node.SteerAuth.Authenticate(r.Context(), integration.DSARScope, control.SignalDSAR,
			integration.CanonicalDSARPayload("expire", "", req.RequestID), co); err != nil {
			writeError(w, http.StatusForbidden, "nao autorizado")
			return
		}
	}
	// Só UMA passagem de cada vez (ver nota de SERIALIZAÇÃO acima). CAS não-bloqueante: se já
	// houver uma passagem activa, recusa 409 em vez de correr uma segunda concorrente.
	if !h.svc.expireInFlight.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, "expiracao ja em curso")
		return
	}
	// SELO DE ATRIBUIÇÃO ANTES DE CORRER, e fail-closed — mesma postura do varredor automático.
	// Se o WORM não aceitar, a expiração NÃO acontece: um apagamento em massa que a cadeia não
	// consegue atribuir não deve acontecer.
	expiracaoID := "retexpire-" + time.Now().UTC().Format(time.RFC3339Nano)
	if err := h.svc.selarPassagemDeRetencao(r.Context(), retentionSweepStartedEvent, expiracaoID,
		time.Now().UTC(), nil, retentionTriggerRota, leitor.principal); err != nil {
		h.svc.expireInFlight.Store(false)
		writeError(w, http.StatusInternalServerError, "selo de atribuicao recusado pelo WORM — a expiracao NAO corre")
		return
	}
	defer h.svc.expireInFlight.Store(false)
	report, err := h.node.ExpirationJob.Run(r.Context())

	// O DESFECHO TAMBÉM SE SELA — achado da verificação de completude de 2026-08-23.
	//
	// A correcção de 2026-08-21 fechou a assimetria de ATRIBUIÇÃO entre a via automática e a
	// humana: ambas passaram a selar QUEM. Deixou intacta a de DESFECHO, que é a metade que diz
	// se o apagamento chegou a acontecer.
	//
	// O varredor selava DOIS registos — `sweep.started` e `sweep.completed` com as contagens e,
	// ao lado delas, as destruições POR CONFIRMAR. A rota selava UM. Um auditor que lesse a
	// partição anos depois obtinha, para a via automática, «quem, quantos, e quantas ficaram por
	// confirmar»; para um apagamento em massa ordenado por uma PESSOA obtinha «quem começou» — e
	// um `started` órfão é indistinguível de uma passagem que morreu a meio.
	//
	// AS CONTAGENS IAM SÓ NO CORPO HTTP, que não é durável e desaparece com a sessão.
	//
	// SELA-SE MESMO COM ERRO, e é a mesma postura do varredor («o desfecho é selado a seguir de
	// qualquer forma»): uma passagem que falhou a meio é precisamente aquela cujo resumo mais
	// falta faz.
	// AOS-311: o desfecho é prova de um facto CONSUMADO (as destruições já aconteceram), pelo que
	// o selo não pode ser cancelado por quem pediu a expiração e desligou a seguir. O selo de
	// ATRIBUIÇÃO acima é decisão e continua a herdar o contexto do pedido.
	selCtx, cancelSelo := context.WithTimeout(context.WithoutCancel(r.Context()), controlSealTimeout)
	defer cancelSelo()
	if serr := h.svc.selarPassagemDeRetencao(selCtx, retentionSweepCompletedEvent, expiracaoID,
		time.Now().UTC(), &report, retentionTriggerRota, leitor.principal); serr != nil {
		// NÃO é fail-closed, e a razão é a do varredor: cada destruição já foi selada pelo job
		// ANTES de acontecer. O que se perde é o RESUMO — registado aqui de forma ruidosa em vez
		// de transformar uma falha de selagem do sumário numa falha da expiração inteira.
		h.svc.log("expiracao por rota (completude 2026-08-23): selo de DESFECHO recusado pelo WORM (as destruicoes desta passagem ficaram seladas uma a uma pelo job; falta o resumo): %v", serr)
	}

	// DESTRUIÇÃO POR CONFIRMAR — o eixo tem de ter CAUSA no log, na rota como no varredor. Sem
	// isto, uma política Transit sem `deletion_allowed` deixava o nó UNREADY sem causa em lado
	// nenhum até ao varrimento seguinte. A contagem não nomeia titulares.
	if pend, ok := shredPendingOf(h.node.DSARVault); ok && pend > 0 {
		h.svc.log("expiracao por rota (completude 2026-08-23): ATENCAO — %d destruicao(oes) de KEK POR CONFIRMAR na custodia. A expiracao esta SELADA na cadeia mas a chave pode continuar viva e o conteudo recuperavel; o /readyz fica VERMELHO ate uma destruicao confirmada. Causas tipicas: politica Transit sem deletion_allowed, replicacao, ou token sem autoridade para destruir", pend)
	}

	if err != nil {
		// Um passo falhou (ex.: selagem do retention.expired): 500 sem detalhe no corpo. Os
		// registos restantes foram processados na mesma (errors.Join no job), e o desfecho JÁ
		// ficou selado acima.
		writeError(w, http.StatusInternalServerError, "expiracao recusada")
		return
	}
	// AOS-221 — VERIFICAÇÃO PÓS-SHRED da hash-chain do WORM, em PARIDADE com POST /dsar/erase
	// (ver dsar.go). A expiração por TTL MATERIALIZA-SE por crypto-shred da KEK por-titular
	// (retention.go cryptoShredSink.Expire) — é um apagamento REAL, o MESMO vector do /dsar/erase.
	// Prova-se aqui que destruir a CHAVE não mutou a cadeia tamper-evident: o shred apaga a KEK,
	// não os registos selados (incl. os selos retention.expired desta passagem), pelo que a
	// hash-chain TEM de continuar a validar. Re-encadeia TODAS as partições (via SEM chave privada).
	// Cadeia partida ⇒ incidente de integridade ⇒ fail-closed (500 uniforme, sem detalhe). Um WORM
	// injectado opaco (sem audit.PartitionLister) não é verificável pelo nó ⇒ NÃO é falha da
	// expiração (a integridade desse substrato é do chamador).
	if verr := h.node.VerifyWORM(r.Context()); verr != nil && !errors.Is(verr, audit.ErrPartitionsUnavailable) {
		writeError(w, http.StatusInternalServerError, "integridade do worm comprometida apos a expiracao")
		return
	}
	writeJSON(w, http.StatusOK, expireResponse{
		Scanned:    report.Scanned,
		Expired:    report.Expired,
		Held:       report.Held,
		Skipped:    report.Skipped,
		NotExpired: report.NotExpired,
	})
}
