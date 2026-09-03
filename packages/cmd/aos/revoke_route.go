package main

// REVOGAÇÃO DE NHI PELA REDE — a via alcançável que faltava (AOS-288).
//
// # PORQUE UMA ROTA, E NÃO SÓ A COMPOSIÇÃO
//
// Ligar `WithRevocations` no verifier fecha metade do defeito: o nó passa a CONSULTAR o
// registo. A outra metade é ninguém conseguir ESCREVER nele. Um registo de revogação que só
// se alimenta de dentro do processo é um mecanismo composto e inacionável — e o banner que
// anuncia «revogacao» continuaria a anunciar uma propriedade que nenhum operador consegue
// exercer. É a mesma classe de defeito, um passo à frente.
//
// # A DISCIPLINA É A DO /autonomy, e não por simetria decorativa
//
// Revogar um token é uma acção de governação irreversível dentro da janela de vida dele. Segue
// portanto o molde do plano de controlo: admissão e mTLS vêm da TABELA DE ROTAS (planoControlo,
// ver planos.go) e nunca do corpo; a assinatura ed25519 do operador vem NO CORPO e é produzida
// FORA do nó (`aos-issuer revoke-sign`), pelo que a chave privada nunca entra neste processo
// (ADR-016 §1, non-signing); o nonce é durável e de uso único; e a decisão terminal fica selada
// na hash-chain com o principal do actor.
//
// O `jti` e o MOTIVO entram no payload assinado ([integration.CanonicalRevokePayload]). O jti
// porque uma assinatura capturada não pode servir para revogar outro token; o motivo porque uma
// revogação sem justificação escrita é uma decisão que o registo não consegue explicar depois —
// e é essa a pergunta a que uma auditoria de identidade tem de responder.

import (
	"net/http"
	"strings"

	integration "github.com/aos-ref/integration"
	control "github.com/aos-ref/kernel/agent-runtime/control"
	audit "github.com/aos-ref/platform/audit"
)

// revokeRequest é o corpo de POST /nhi/revoke. Espelha o de POST /autonomy: o alvo em claro
// mais o emissor assinado. O `jti` não é secreto — é um identificador de token, e quem o
// apresenta já o conhece.
type revokeRequest struct {
	JTI     string      `json:"jti"`
	Reason  string      `json:"reason"`
	Emitter emitterWire `json:"emitter"`
}

// handleRevoke revoga um token NHI pelo seu `jti`.
//
// Fail-closed em três degraus, cada um com o seu 501/400/403 e nenhum a devolver 200 sem
// efeito — um sucesso que não muda o sistema é a resposta mais cara que uma API de governação
// pode dar, porque o operador acredita ter revogado e não revogou.
func (h *apiHandler) handleRevoke(w http.ResponseWriter, r *http.Request) {
	if h.node == nil || h.node.Revocations == nil {
		writeError(w, http.StatusNotImplemented, "registo de revogacao nao composto")
		return
	}
	if h.node.SteerAuth == nil {
		writeError(w, http.StatusNotImplemented, "canal de controlo sem autenticador")
		return
	}

	var req revokeRequest
	if status, ok := h.decodeJSON(w, r, &req); !ok {
		writeError(w, status, "corpo invalido")
		return
	}
	jti := strings.TrimSpace(req.JTI)
	reason := strings.TrimSpace(req.Reason)
	if jti == "" {
		writeError(w, http.StatusBadRequest, "jti obrigatorio")
		return
	}
	if reason == "" {
		writeError(w, http.StatusBadRequest, "reason obrigatorio: uma revogacao sem motivo nao e auditavel")
		return
	}

	emitter, err := req.Emitter.decode()
	if err != nil {
		writeError(w, http.StatusBadRequest, "emitter invalido")
		return
	}
	// 403 UNIFORME, pela mesma razão do /autonomy e do read-path soberano: não se distingue
	// «emissor desconhecido» de «assinatura errada» de «nonce reutilizado». A mensagem de erro
	// não pode ser um oráculo — aqui menos ainda, porque enumerar jti válidos seria dar a quem
	// sonda exactamente a lista que ele quer.
	payload := integration.CanonicalRevokePayload(jti, reason)
	if err := h.node.SteerAuth.Authenticate(r.Context(), integration.RevokeScope,
		control.SignalRevoke, payload, emitter); err != nil {
		writeError(w, http.StatusForbidden, "emissor nao autorizado")
		return
	}

	// A ORDEM É AUDIT-AFTER-EFFECT, deliberadamente: `Revoke` escreve o `identity.nhi.revoked`
	// durável ANTES de selarmos o controlo. Se o selo falhar, a revogação já vale e o log
	// regista a falta do selo — o inverso (selar primeiro) deixaria um selo a afirmar uma
	// revogação que podia não ter acontecido.
	if err := h.node.Revocations.Revoke(r.Context(), jti); err != nil {
		writeError(w, http.StatusInternalServerError, "revogacao nao gravada")
		return
	}
	// O MOTIVO VAI NO SELO, e não só na assinatura (achado da revisão de segurança). O
	// `reason` era exigido, verificado como parte do payload assinado — e depois descartado:
	// nada o gravava. O comentário do topo deste ficheiro dizia que ficava selado, e não
	// ficava. O /autonomy parece fazer o mesmo e não faz: o motivo dele é selado pelo
	// `LevelRegistry.SetLevel` na partição própria.
	//
	// Sem isto, uma auditoria que pergunte «porque foi este token revogado?» não tem resposta
	// em lado nenhum — o que torna a exigência do motivo uma formalidade em vez de uma
	// obrigação. É uma [audit.Obligation] e não um campo do registo porque é isso que a
	// hash-chain aceita sem mudar o esquema.
	h.sealControlAction(r.Context(), "nhi_revoke", jti, emitter.ID,
		audit.Obligation{Type: "gov.nhi.revoke.reason", Fields: []string{reason}})

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "revoked",
		"jti":    jti,
		"actor":  emitter.ID,
	})
}
