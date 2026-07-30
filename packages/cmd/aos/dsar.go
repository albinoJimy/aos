// AOS-172 (E7) — DSAR / CRYPTO-SHREDDING no nó `aos` (Art. 17 do RGPD, ADR-011). Terceiro
// entregável do ticket: COMPÕE o fluxo DSAR já existente ([dsar.Flow], AOS-093) no nó e
// expõe uma forma AUTENTICADA de submeter um pedido de apagamento. NÃO reimplementa a
// cifra/vault/shredder — orquestra a erasure UNIFICADA por-titular sobre a governança
// existente (o crypto-shredding do audit AOS-083 e o selo WORM tamper-evident).
//
// ESCOLHA de superfície (justificada): um ENDPOINT HTTP autenticado no plano de controlo
// (POST /dsar/erase), e NÃO um subcomando CLI in-process. Razão: o nó é um servidor
// long-running que DETÉM os stores (vault de chaves + WORM); um subcomando CLI in-process
// não alcançaria o processo em execução. O endpoint REUTILIZA o modelo de autenticação de
// LEITURA de governação (o mesmo principal+board fail-closed de D7) — um pedido DSAR é uma
// operação de governação e exige um principal de governação AUTENTICADO; sem o gate soberano
// composto o endpoint está DESLIGADO (fail-closed). Passa também pelo token-bucket do plano
// de CONTROLO (admitControl). A credencial FORTE do operador DSAR (OIDC/mTLS no IdP de
// soberania) é DEMO-GRADE aqui e fica DEFERIDA para AOS-205 (análogo a D4).
//
// GARANTIAS reutilizadas do [dsar.Flow] (não re-litigadas):
//   - o legal HOLD é re-consultado ANTES de cada Shred — um titular sob hold NÃO é apagado
//     (fail-closed do apagamento); o evento dsar.blocked é selado;
//   - o crypto-shredding destrói a KEK por-titular ([ShreddableKeyStore.Shred]) — a PII fica
//     ILEGÍVEL sem mutar a hash-chain (que selou o HASH do ciphertext, não o plaintext);
//   - received/key_destroyed/blocked são selados no WORM (EventSealer); idempotente.
package main

import (
	"context"
	"errors"
	"net/http"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	audit "github.com/aos-ref/platform/audit"
)

// contentSealer é a implementação de [agentruntime.ContentCipher] do nó (AOS-093).
// Cifra/decifra o CONTEÚDO DOS RUNS por chave POR-TITULAR reutilizando o envelope
// DEK/KEK de platform/audit ([audit.SealContent]/[audit.OpenContent]) — zero crypto
// novo — e regista subject→stream no índice titular→partição para o crypto-shredding
// e o legal hold POR-PARTIÇÃO alcançarem o substrato. Detém as MESMAS instâncias de
// vault/índice que o fluxo DSAR, pelo que o POST /dsar/erase destrói exactamente a KEK
// que cifra o conteúdo — tornando-o irrecuperável sem mutar o log.
//
// O vault é a PORTA [audit.KeyVault] (AOS-215/DEF-302), não o tipo concreto: um deployment
// injecta um key-service/software-KMS de CUSTÓDIA EXTERNA por [Config.DSARVault] e o cifrador
// passa a selar/decifrar por ELE, sem tocar o binário. Sem injecção cai no
// [audit.InMemoryKeyVault] de referência (demo-grade, KEK em memória).
type contentSealer struct {
	vault audit.KeyVault
	index *audit.InMemorySubjectPartitionIndex
}

// newContentSealer liga o cifrador ao vault (porta [audit.KeyVault]) e índice DSAR partilhados.
func newContentSealer(vault audit.KeyVault, index *audit.InMemorySubjectPartitionIndex) *contentSealer {
	return &contentSealer{vault: vault, index: index}
}

// SealContent cifra plaintext sob a KEK do titular (provisionada na 1ª escrita) e liga
// subject→streamID no índice — o stream do run passa a ser uma partição do titular, que
// o shred/hold cobrem (AOS-093 CA4/CA6). FAIL-CLOSED: um erro de cifra propaga-se e
// aborta a escrita (o chamador nunca cai para texto-claro).
func (s *contentSealer) SealContent(_ context.Context, subject, streamID string, plaintext []byte) ([]byte, error) {
	sealed, err := audit.SealContent(s.vault, subject, plaintext, nil)
	if err != nil {
		return nil, err
	}
	if s.index != nil && streamID != "" {
		s.index.Link(subject, streamID)
	}
	return sealed, nil
}

// OpenContent decifra o que SealContent selou. FAIL-CLOSED após crypto-shredding: se a
// KEK do titular foi destruída devolve [audit.ErrDecrypt] — o conteúdo é irrecuperável.
func (s *contentSealer) OpenContent(_ context.Context, subject string, sealed []byte) ([]byte, error) {
	return audit.OpenContent(s.vault, subject, sealed)
}

// contentSealer satisfaz a porta de cifra por-titular do substrato (compile-time).
var _ agentruntime.ContentCipher = (*contentSealer)(nil)

// wormEventSealer adapta o audit.Store do nó à porta [dsar.EventSealer]. O fluxo DSAR sela
// APENAS metadados (received/key_destroyed/blocked, SEM PII), pelo que o Ingest se reduz a um
// Append na hash-chain WORM: o RawRecord nunca traz PII (PayloadRef nil), logo nada é cifrado
// e o registo é puramente responsabilização selada. A partição vem já definida no Record
// (dsar.WithPartition / Request.Partition).
type wormEventSealer struct{ store audit.Store }

// Ingest implementa [dsar.EventSealer]: sela o registo de conformidade na cadeia da sua
// partição. Sem PII no RawRecord ⇒ sem cifra, sem PayloadRef.
func (s wormEventSealer) Ingest(ctx context.Context, raw audit.RawRecord) (audit.AuditRecord, error) {
	return s.store.Append(ctx, raw.Record)
}

// dsarRequestWire é a representação de wire de um pedido DSAR de apagamento. NÃO carrega
// qualquer valor pessoal: o SubjectID é o identificador PSEUDÓNIMO do titular (o mesmo que
// ancora a chave por-titular no vault), nunca o dado pessoal em si.
type dsarRequestWire struct {
	RequestID string `json:"request_id"`
	SubjectID string `json:"subject_id"`
}

// maxSubjectIDLen limita o comprimento do subject_id pseudónimo aceite. Um pseudónimo opaco
// (ex.: um ULID/UUID/hash com prefixo de namespace) cabe folgadamente; valores muito longos
// são suspeitos de transportar dado pessoal em texto-livre.
const maxSubjectIDLen = 128

// validPseudonym impõe o contrato "subject_id é pseudónimo OPACO" na fronteira do endpoint,
// para reduzir o risco de PII acidental ficar IMUTAVELMENTE selada no WORM tamper-evident (que
// o crypto-shredding não consegue remover). Aceita apenas um charset opaco conservador —
// letras/dígitos ASCII e os separadores '-', '_', ':', '.' (cobre ULID/UUID/hash namespaced)
// — e um comprimento limitado. Rejeita '@' (emails), espaços/nomes, não-ASCII e pontuação
// livre, típicos de PII. É defesa em profundidade, NÃO uma prova de que o valor é pseudónimo:
// a garantia forte (pseudonimização na origem) vive no IdP de soberania (AOS-205).
func validPseudonym(s string) bool {
	if len(s) == 0 || len(s) > maxSubjectIDLen {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == ':', r == '.':
			// carácter opaco permitido
		default:
			return false
		}
	}
	return true
}

// dsarResponse é o desfecho SEM PII de um pedido DSAR: se foi apagado ou BLOQUEADO (legal
// hold), os rótulos dos stores destruídos e os audit_seq selados (prova de auditabilidade).
type dsarResponse struct {
	RequestID      string   `json:"request_id"`
	SubjectID      string   `json:"subject_id"`
	Status         string   `json:"status"` // "erased" | "blocked"
	Blocked        bool     `json:"blocked"`
	Partial        bool     `json:"partial,omitempty"`
	StoresShredded []string `json:"stores_shredded,omitempty"`
	ReceivedSeq    uint64   `json:"received_seq,omitempty"`
	OutcomeSeq     uint64   `json:"outcome_seq,omitempty"`
}

// handleDSAR satisfaz um pedido DSAR de apagamento (Art. 17). Fail-closed em cada porta:
//
//  1. admission do plano de CONTROLO (token-bucket dedicado);
//  2. o fluxo DSAR TEM de estar composto (senão o endpoint está desligado ⇒ 501);
//  3. AUTENTICAÇÃO de governação: reutiliza o gate soberano de LEITURA (principal+board
//     fail-closed). Sem gate composto ⇒ 501; credencial ausente/board desconhecido ⇒ 403;
//  4. decodifica o pedido (subject pseudónimo, sem PII) sob limite de corpo;
//  5. [dsar.Flow.Receive] re-consulta o legal hold ANTES do shred, executa o crypto-shredding
//     e sela received/key_destroyed/blocked no WORM. Um titular sob hold NÃO é apagado
//     (200 blocked); um erro genuíno ⇒ 500 (corpo uniforme, sem PII).
func (h *apiHandler) handleDSAR(w http.ResponseWriter, r *http.Request) {
	// (1) ADMISSION do plano de controlo (mesmo bucket dedicado de /steer,/pause,/approve).
	if !h.admitControl(w) {
		return
	}
	// (2) O fluxo DSAR tem de estar composto no nó.
	if h.node.DSAR == nil {
		writeError(w, http.StatusNotImplemented, "dsar desligado (fluxo nao composto)")
		return
	}
	// (3) AUTENTICAÇÃO de governação — reutiliza o gate soberano de leitura (D7). Um pedido
	// DSAR é uma operação de governação: exige um principal+board AUTENTICADO. Sem o gate
	// composto o endpoint está DESLIGADO (fail-closed); credencial ausente/board desconhecido
	// ⇒ 403 (corpo uniforme, sem revelar detalhe).
	if h.readGov == nil {
		writeError(w, http.StatusNotImplemented, "dsar desligado (governanca soberana nao composta)")
		return
	}
	if _, ok := h.readGov.authorize(r); !ok {
		writeError(w, http.StatusForbidden, "nao autorizado")
		return
	}

	// (4) LIMITE DE CORPO + descodificação (o subject é pseudónimo, sem PII).
	var req dsarRequestWire
	if status, ok := h.decodeJSON(w, r, &req); !ok {
		writeError(w, status, "corpo invalido")
		return
	}

	// (4b) CONTRATO do pseudónimo (defesa em profundidade). O SubjectID é selado VERBATIM na
	// hash-chain WORM imutável (Resource.Value) — que o próprio crypto-shredding NÃO consegue
	// remover (a cadeia é append-only/tamper-evident). Se um chamador enviar por engano PII
	// real (email/nome) como subject_id, esse valor ficaria PERMANENTEMENTE selado. Impõe-se
	// aqui o contrato "subject_id é pseudónimo opaco": rejeita comprimentos/charsets típicos de
	// PII ANTES de encaminhar para o fluxo. Um subject_id vazio segue para o fluxo (que devolve
	// ErrNoSubject ⇒ "em falta"), preservando a mensagem existente.
	if req.SubjectID != "" && !validPseudonym(req.SubjectID) {
		writeError(w, http.StatusBadRequest, "subject_id invalido (esperado pseudonimo opaco)")
		return
	}

	// (5) EXECUTA o fluxo DSAR (legal hold → crypto-shredding → selo WORM).
	res, err := h.node.DSAR.Receive(r.Context(), dsar.Request{RequestID: req.RequestID, SubjectID: req.SubjectID})
	if err != nil {
		if errors.Is(err, dsar.ErrLegalHold) {
			// BLOQUEADO por legal hold: nada foi apagado (fail-closed do apagamento); o evento
			// dsar.blocked foi selado. É um desfecho legítimo (não um erro de servidor): o
			// requerente autorizado tem direito a saber que a preservação suspendeu o apagamento.
			writeJSON(w, http.StatusOK, dsarResponse{
				RequestID: res.RequestID, SubjectID: res.SubjectID, Status: "blocked",
				Blocked: true, Partial: res.Partial, StoresShredded: res.StoresShredded,
				ReceivedSeq: res.ReceivedSeq, OutcomeSeq: res.OutcomeSeq,
			})
			return
		}
		if errors.Is(err, dsar.ErrNoSubject) {
			writeError(w, http.StatusBadRequest, "subject_id em falta")
			return
		}
		// Erro genuíno (selagem/store) ⇒ 500 sem detalhe no corpo.
		writeError(w, http.StatusInternalServerError, "dsar recusado")
		return
	}
	writeJSON(w, http.StatusOK, dsarResponse{
		RequestID: res.RequestID, SubjectID: res.SubjectID, Status: "erased",
		Blocked: false, StoresShredded: res.StoresShredded,
		ReceivedSeq: res.ReceivedSeq, OutcomeSeq: res.OutcomeSeq,
	})
}
