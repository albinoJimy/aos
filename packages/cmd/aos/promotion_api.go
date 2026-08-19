package main

// AOS-275 (achado F7) — ROTA EXTERNA do promotion controller: `POST /promote`.
//
// O AOS-206 fechou o defeito "gate implementado, nó não o compõe" (o controller passou a ser
// composto INCONDICIONALMENTE, ver promotion.go). Ficou, porém, a SEGUNDA metade do mesmo
// defeito: a via sancionada só era alcançável IN-PROCESS ([PromotionController.Promote]) — o
// binário entregue não tinha superfície nenhuma por onde um operador submetesse uma
// ratificação. Com ratificadores pinados em AOS_RATIFIERS, o nó anunciava no banner uma
// capacidade que NENHUM humano conseguia exercer, e o banner declarava-a "DEFERIDA". Este
// ficheiro fecha-o: a MESMA via sancionada passa a ter uma rota.
//
// # NADA DE NOVO NA SEGURANÇA — a rota é transporte
//
// O handler NÃO decide nada. A decisão é EXCLUSIVAMENTE de [PromotionController.Promote] (o
// [hitl.RatificationGate] de PRODUÇÃO, com freshness + nonce-store DURÁVEL forçados por
// construção). A rota descodifica o wire, recusa fail-closed o que é estruturalmente inválido
// ANTES de tocar no gate, e transporta o resto. Nenhum invariante é reimplementado aqui: a
// pré-condição eval-gate+canary, a autoridade "ratify:production", o anti-transplante, a
// frescura, o uso-único durável e a selagem WORM valem TODOS por esta rota porque é o MESMO
// gate — é essa a razão de o anti-replay ser demonstrável PELA ROTA (aos275_promote_route_test.go).
//
// # AUTENTICAÇÃO — a MESMA admissão do /approve, nem mais nem menos
//
// Três camadas, na ordem do [apiHandler.handleApprove]:
//
//  1. [apiHandler.admitControl] — token-bucket DEDICADO do plano de controlo, ANTES de qualquer
//     descodificação de corpo ou verificação de assinatura (o corpo é limitado em decodeJSON).
//  2. [apiHandler.admitControlMTLS] — mTLS de controlo quando composto (DEF-012): sem
//     certificado de cliente VERIFICADO ⇒ 403 antes de tudo o resto.
//  3. NON-SIGNING (ADR-016 §1): a autenticação REAL da ratificação é a ASSINATURA ed25519 que
//     vem no corpo, verificada pelo gate contra a PUBKEY PINADA do ratificador (AOS_RATIFIERS)
//     — o nó nunca detém a chave privada do humano. Não há credencial ambiente: um POST sem
//     assinatura válida de um ratificador autorizado NÃO promove nada (ratifier_unknown /
//     ratification_forged), exactamente como in-process.
//
// # RESPOSTA UNIFORME (não-enumerável)
//
// Qualquer decisão que não seja a promoção — pré-condição falhada, ratificador desconhecido ou
// sem autoridade, transplante, assinatura forjada, ratificação stale, REPLAY, ou até uma RECUSA
// assinada — devolve o MESMO 403 "promocao recusada". O motivo estável (ex.:
// [hitl.ReasonRatificationReplayed]) fica SELADO no WORM, que é onde a evidência pertence; a
// resposta HTTP não é um oráculo de qual invariante falhou.
//
// # RESIDUAL NOMEADO
//
// Uma submissão não-autenticada (assinatura inválida) faz o gate selar a decisão na partição de
// QUARENTENA "ratification-unratified" — ou seja, ingresso não-autenticado com efeito de escrita
// (limitado) no WORM. É a MESMA postura do /approve e é o preço de auditar as tentativas; o que
// a limita é o token-bucket do plano de controlo (1) e, quando composto, o mTLS (2). O eixo para
// a fechar por completo é o mesmo do resto do plano de controlo (DEF-012: PKI de cliente).

import (
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	hitl "github.com/aos-ref/control-plane/governance/hitl"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// promoteRequest é o corpo de `POST /promote`: o artefacto candidato (identidade + prova das
// etapas anteriores) e a ratificação ASSINADA que o promove. Espelha exactamente os dois
// argumentos de [PromotionController.Promote] — a rota não acrescenta campos nem semântica.
type promoteRequest struct {
	Artifact     selfModArtifactWire `json:"artifact"`
	Ratification ratificationWire    `json:"ratification"`
}

// selfModArtifactWire é a face de wire de [hitl.SelfModArtifact]. NUNCA transporta o CONTEÚDO do
// artefacto — só o seu hash (o mesmo princípio do tipo de domínio): o que se ratifica é uma
// identidade, e o conteúdo não tem de atravessar a fronteira de rede para isso.
type selfModArtifactWire struct {
	ID           string         `json:"id"`
	Kind         string         `json:"kind"`          // skill | procedural_memory
	Version      string         `json:"version"`       // SemVer do artefacto (o humano ratifica ESTA versão)
	ContentHash  string         `json:"content_hash"`  // base64(hash do conteúdo)
	CanaryPassed bool           `json:"canary_passed"` // pré-condição (EPIC-08)
	Eval         evalResultWire `json:"eval"`
}

// evalResultWire é a face de wire de [otelgenai.EvaluationResult] — a prova do eval-gate
// (AOS-084) que é PRÉ-CONDIÇÃO da ratificação e que o canónico assinado amarra.
type evalResultWire struct {
	Suite         string  `json:"suite"`
	EvalID        string  `json:"eval_id"`
	Dataset       string  `json:"dataset"` // golden | failure_derived
	Verdict       string  `json:"verdict"` // pass | fail
	Score         float64 `json:"score"`
	TargetTraceID string  `json:"target_trace_id,omitempty"` // hex de 16 bytes (32 chars), opcional
}

// ratificationWire é a face de wire de [hitl.SignedApproval] usada como RATIFICAÇÃO. O
// `request_id` TEM de ser o [hitl.SelfModArtifact.RatificationID] deste artefacto — é o
// anti-transplante, e é o gate que o impõe (a rota nem o recalcula para "ajudar": corrigi-lo
// aqui destruiria a amarra que a assinatura cobre).
type ratificationWire struct {
	RequestID string `json:"request_id"`
	Ratifier  string `json:"ratifier"`
	Approved  bool   `json:"approved"`
	Nonce     string `json:"nonce"` // base64 (>= 16 bytes — uso-único durável)
	// IssuedAt é RFC3339 e é a base da FRESCURA. É [time.Time] (não string) pelo MESMO molde
	// do [emitterWire] do canal de controlo: um timestamp malformado é apanhado pelo decoder
	// (⇒ 400 sem tocar no gate) e um ausente fica zero — que o gate rejeita como
	// [hitl.ReasonRatificationMalformed]. Nenhuma das duas vias promove.
	IssuedAt  time.Time `json:"issued_at"`
	Signature string    `json:"signature"` // base64(assinatura ed25519 sobre o canónico)
}

// promoteKind traduz a classe do artefacto com VOCABULÁRIO FECHADO. Uma classe desconhecida é
// 400 e NÃO um valor livre: [hitl.SelfModArtifact.canonical] sela o Kind, pelo que um typo
// ("skills") produziria SILENCIOSAMENTE outra identidade de ratificação — o pedido seria negado
// por "transplante" e o operador procuraria o erro no sítio errado. Fail-closed com a causa
// atribuída à fronteira, no molde do vocabulário fechado de `authority` (AOS-193).
func promoteKind(s string) (hitl.ArtifactKind, bool) {
	switch strings.TrimSpace(s) {
	case string(hitl.ArtifactSkill):
		return hitl.ArtifactSkill, true
	case string(hitl.ArtifactProceduralMemory):
		return hitl.ArtifactProceduralMemory, true
	default:
		return "", false
	}
}

// decodeArtifact converte o wire em [hitl.SelfModArtifact], recusando fail-closed tudo o que
// tornaria a ratificação estruturalmente inútil ANTES de tocar no gate. Devolve a mensagem de
// erro (já sem detalhe enumerável) para um 400.
func (w selfModArtifactWire) decode() (hitl.SelfModArtifact, string) {
	id := strings.TrimSpace(w.ID)
	if id == "" {
		return hitl.SelfModArtifact{}, "artifact.id obrigatorio"
	}
	kind, ok := promoteKind(w.Kind)
	if !ok {
		return hitl.SelfModArtifact{}, "artifact.kind invalido (skill|procedural_memory)"
	}
	version := strings.TrimSpace(w.Version)
	if version == "" {
		// A versão é selada no audit e no canónico: sem ela, o não-repúdio não diz O QUÊ se
		// promoveu.
		return hitl.SelfModArtifact{}, "artifact.version obrigatoria"
	}
	// O content_hash é EXIGIDO (e não meramente opcional): é o que amarra a ratificação aos
	// BYTES exactos promovidos. Aceitá-lo vazio daria uma ratificação válida para qualquer
	// conteúdo que partilhasse o resto da identidade — o oposto do objectivo do gate.
	contentHash, err := base64.StdEncoding.DecodeString(strings.TrimSpace(w.ContentHash))
	if err != nil || len(contentHash) == 0 {
		return hitl.SelfModArtifact{}, "artifact.content_hash invalido (base64 nao-vazio)"
	}
	eval := otelgenai.EvaluationResult{
		Suite:   strings.TrimSpace(w.Eval.Suite),
		EvalID:  strings.TrimSpace(w.Eval.EvalID),
		Dataset: otelgenai.EvalDataset(strings.TrimSpace(w.Eval.Dataset)),
		// O veredicto NÃO é validado contra um vocabulário aqui: o eval-gate é fail-closed por
		// construção (só [otelgenai.EvalPass] passa), pelo que um valor desconhecido já é
		// tratado como "não passou" — negar aqui só mudaria o código de estado, não o desfecho.
		Verdict: otelgenai.EvalVerdict(strings.TrimSpace(w.Eval.Verdict)),
		Score:   w.Eval.Score,
	}
	if raw := strings.TrimSpace(w.Eval.TargetTraceID); raw != "" {
		b, derr := hex.DecodeString(raw)
		if derr != nil || len(b) != len(eval.TargetTraceID) {
			return hitl.SelfModArtifact{}, "eval.target_trace_id invalido (hex de 16 bytes)"
		}
		copy(eval.TargetTraceID[:], b)
	}
	return hitl.SelfModArtifact{
		ID:           id,
		Kind:         kind,
		Version:      version,
		EvalResult:   eval,
		CanaryPassed: w.CanaryPassed,
		ContentHash:  contentHash,
	}, ""
}

// decode converte o wire em [hitl.SignedApproval]. Só descodificação/forma: a VERIFICAÇÃO
// (assinatura, autoridade, frescura, uso-único) é do gate, e nada aqui a antecipa.
func (w ratificationWire) decode() (hitl.SignedApproval, string) {
	requestID := strings.TrimSpace(w.RequestID)
	if requestID == "" {
		return hitl.SignedApproval{}, "ratification.request_id obrigatorio (= ratification_id do artefacto)"
	}
	ratifier := strings.TrimSpace(w.Ratifier)
	if ratifier == "" {
		return hitl.SignedApproval{}, "ratification.ratifier obrigatorio"
	}
	nonce, err := base64.StdEncoding.DecodeString(strings.TrimSpace(w.Nonce))
	if err != nil {
		return hitl.SignedApproval{}, "ratification.nonce invalido (base64)"
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(w.Signature))
	if err != nil {
		return hitl.SignedApproval{}, "ratification.signature invalida (base64)"
	}
	return hitl.SignedApproval{
		RequestID: requestID,
		Approver:  ratifier,
		Approved:  w.Approved,
		Nonce:     nonce,
		IssuedAt:  w.IssuedAt,
		Signature: sig,
	}, ""
}

// handlePromote submete uma RATIFICAÇÃO assinada ao promotion controller (AOS-159/AOS-206) — a
// rota externa que AOS-275 acrescenta à via sancionada.
//
// Fluxo: admissão do plano de controlo (+ mTLS quando composto) → descodificação fail-closed do
// wire → [PromotionController.Promote] (o gate de PRODUÇÃO) → resposta. A decisão terminal é
// SEMPRE selada no WORM pelo gate, com o PRINCIPAL (o ratificador) quando a assinatura verifica
// — é de lá que se lê o motivo, incluindo o [hitl.ReasonRatificationReplayed] de uma ratificação
// re-submetida após consumo.
//
// 200 ⇒ promovido; 400 ⇒ wire estruturalmente inválido (nada chegou ao gate); 403 ⇒ recusado
// (motivo no WORM, resposta uniforme); 429 ⇒ admissão; 501 ⇒ controller não composto (defensivo:
// o [Bootstrap] compõe-o SEMPRE, mas um [Node] montado à mão não fica a devolver 500).
func (h *apiHandler) handlePromote(w http.ResponseWriter, r *http.Request) {
	if h.node == nil || h.node.Promotion == nil {
		writeError(w, http.StatusNotImplemented, "promotion controller nao composto")
		return
	}
	var req promoteRequest
	if status, ok := h.decodeJSON(w, r, &req); !ok {
		writeError(w, status, "corpo invalido")
		return
	}
	artifact, msg := req.Artifact.decode()
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	ratification, msg := req.Ratification.decode()
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	admit, err := h.node.Promotion.Promote(r.Context(), artifact, ratification)
	if err != nil {
		// O gate não devolve erro nas decisões (sela e responde admit=false); um erro aqui é
		// falha de infraestrutura. Sem detalhe no corpo.
		writeError(w, http.StatusInternalServerError, "promocao indisponivel")
		return
	}
	if !admit {
		// UNIFORME: replay, stale, transplante, ratificador desconhecido/sem autoridade,
		// assinatura forjada, pré-condição falhada e recusa assinada partilham o MESMO 403. O
		// motivo estável está selado no WORM (partição "ratification:<artifact-id>" quando a
		// assinatura verifica; "ratification-unratified" quando não).
		writeError(w, http.StatusForbidden, "promocao recusada")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "promoted",
		// ratification_id é o RequestID do selo no WORM: é por ele que o operador encontra a
		// decisão na cadeia. Material público (hash de metadados públicos).
		"ratification_id": artifact.RatificationID(),
		"artifact_id":     artifact.ID,
		"version":         artifact.Version,
		"ratifier":        ratification.Approver,
	})
}
