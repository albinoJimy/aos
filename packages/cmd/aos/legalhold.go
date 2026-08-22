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
	if err != nil {
		// Um passo falhou (ex.: selagem do retention.expired): 500 sem detalhe no corpo. Os
		// registos restantes foram processados na mesma (errors.Join no job).
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
