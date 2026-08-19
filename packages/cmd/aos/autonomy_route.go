package main

import (
	"encoding/base64"
	"net/http"
	"sort"
	"strings"

	"github.com/aos-ref/control-plane/governance/autonomy"
	"github.com/aos-ref/integration"
	"github.com/aos-ref/kernel/agent-runtime/control"
)

// ------------------------------------------------------------------------------------------
// POST/GET /autonomy — mudar e ler os NÍVEIS DE AUTONOMIA em runtime.
//
// O DEFEITO QUE ISTO FECHA. O oráculo de autonomia era o ÚNICO mecanismo de governação deste
// sistema que se mudava editando um ficheiro no servidor (`AOS_AUTONOMY_LEVELS` no `.env`) e
// recriando o nó. O `/approve`, o `/pause`, o `/steer` e o `/promote` são todos assinados, com
// nonce de uso único e selados com QUEM os emitiu.
//
// Ou seja: o mecanismo que decide QUANTA SUPERVISÃO HUMANA SE APLICA era o que tinha menos
// supervisão a mudar. E como exigia reiniciar o nó, mudá-lo custava indisponibilidade — o que
// desencoraja ajustá-lo, que é o pior incentivo possível para um controlo de segurança.
//
// Faltar-lhe a rota era ao mesmo tempo o que o tornava difícil de ligar e o que o tornava
// difícil de auditar. É o mesmo defeito visto de dois lados.
//
// O QUE NÃO FOI PRECISO CONSTRUIR, e é a razão de isto ser pequeno: o
// [autonomy.LevelRegistry.SetLevel] já recebe `reason` e `actor` e já SELA a mudança na
// hash-chain como `autonomy.level_changed`. Foi desenhado para mudanças em runtime. Nada o
// chamava a não ser o provisionamento do arranque.
// ------------------------------------------------------------------------------------------

// autonomyRequest é a face de wire de uma mudança de nível.
type autonomyRequest struct {
	Emitter emitterWire `json:"emitter"`
	Agent   string      `json:"agent"`
	Domain  string      `json:"domain"`
	Level   string      `json:"level"`  // "L0".."L5"
	Reason  string      `json:"reason"` // OBRIGATÓRIO — ver handleAutonomySet
}

// autonomyPairWire é um par (agente, domínio) e o seu nível, para o GET.
type autonomyPairWire struct {
	Agent  string `json:"agent"`
	Domain string `json:"domain"`
	Level  string `json:"level"`
}

// handleAutonomySet aplica uma mudança de nível ASSINADA.
//
// A assinatura cobre o tuplo (âmbito "autonomy", kind, payload canónico, nonce, issued_at), e o
// payload canónico inclui o PAR, o NÍVEL e o MOTIVO. É o que impede reapresentar uma assinatura
// legítima de "L1" como se fosse de "L5" — e o que amarra a justificação ao acto, em vez de a
// deixar ser um campo que se acrescenta depois.
//
// O `actor` selado vem do EMISSOR VERIFICADO, nunca do corpo. Quem assina é quem fica no registo;
// aceitar um actor do pedido seria deixar o chamador escolher em nome de quem a mudança aparece.
func (h *apiHandler) handleAutonomySet(w http.ResponseWriter, r *http.Request) {
	// Oráculo não composto ⇒ 501, e NÃO um 200 que não fez nada. Um sucesso que não muda o
	// sistema é a resposta mais cara que uma API de governação pode dar: o operador acredita ter
	// mudado a postura e não mudou.
	if h.node == nil || h.node.Autonomy == nil || h.node.Autonomy.registry == nil {
		writeError(w, http.StatusNotImplemented, "oraculo de autonomia nao composto (AOS_AUTONOMY_LEVELS ausente no arranque)")
		return
	}
	if h.node.SteerAuth == nil {
		writeError(w, http.StatusNotImplemented, "canal de controlo sem autenticador")
		return
	}

	var req autonomyRequest
	if status, ok := h.decodeJSON(w, r, &req); !ok {
		writeError(w, status, "corpo invalido")
		return
	}
	agent := strings.TrimSpace(req.Agent)
	domain := strings.TrimSpace(req.Domain)
	reason := strings.TrimSpace(req.Reason)
	if agent == "" || domain == "" {
		writeError(w, http.StatusBadRequest, "agent e domain obrigatorios")
		return
	}
	// MOTIVO OBRIGATÓRIO. O SetLevel aceita-o e sela-o; uma mudança de nível sem justificação
	// escrita é uma decisão de governação que o registo não consegue explicar depois — que é
	// exactamente a pergunta a que a auditoria de um sistema destes tem de responder.
	if reason == "" {
		writeError(w, http.StatusBadRequest, "reason obrigatorio: uma mudanca de nivel sem motivo nao e auditavel")
		return
	}
	nivel, ok := parseAutonomyLevelWire(req.Level)
	if !ok {
		writeError(w, http.StatusBadRequest, "level invalido (esperado L0..L5)")
		return
	}

	emitter, err := req.Emitter.decode()
	if err != nil {
		writeError(w, http.StatusBadRequest, "emitter invalido")
		return
	}
	// Autenticação: assinatura ed25519 do emissor REGISTADO, sobre este payload exacto, com
	// nonce de uso único durável. Falha ⇒ 403 UNIFORME — não se distingue "emissor desconhecido"
	// de "assinatura errada" de "nonce reutilizado", pelo mesmo motivo que o read-path devolve
	// 404 para tudo: a mensagem de erro não pode ser um oráculo.
	payload := integration.CanonicalAutonomyPayload(agent, domain, nivel.String(), reason)
	if err := h.node.SteerAuth.Authenticate(r.Context(), integration.AutonomyScope,
		control.SignalAutonomy, payload, emitter); err != nil {
		writeError(w, http.StatusForbidden, "emissor nao autorizado")
		return
	}

	anterior, existia := h.node.Autonomy.registry.Get(agent, domain)
	// O SetLevel sela `autonomy.level_changed` na hash-chain com motivo e actor.
	if _, err := h.node.Autonomy.registry.SetLevel(r.Context(), agent, domain, nivel, reason, emitter.ID); err != nil {
		writeError(w, http.StatusBadRequest, "nivel recusado")
		return
	}
	// SELO DE CONTROLO (A3): a mesma partição onde o pause/steer/approve ficam. O selo de
	// autonomia já existe na partição própria; este liga a mudança ao trilho de ACÇÕES DE
	// CONTROLO, onde se pergunta "que operações de governação houve, e de quem".
	h.sealControlAction(r.Context(), "autonomy", agent+"/"+domain, emitter.ID)

	de := "(nao registado)"
	if existia {
		de = anterior.String()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "applied",
		"agent":  agent,
		"domain": domain,
		"from":   de,
		"to":     nivel.String(),
		"actor":  emitter.ID,
	})
}

// handleAutonomyGet devolve os pares em vigor AGORA.
//
// Não é conforto. O banner de arranque afirma a postura de autonomia, e a partir do momento em
// que ela muda em runtime essa linha passa a poder mentir. Sem leitura, a rota seria escrever sem
// poder confirmar — e a única fonte de verdade seria um log de há horas.
func (h *apiHandler) handleAutonomyGet(w http.ResponseWriter, r *http.Request) {
	if h.node == nil || h.node.Autonomy == nil || h.node.Autonomy.registry == nil {
		writeError(w, http.StatusNotImplemented, "oraculo de autonomia nao composto")
		return
	}
	pares := make([]autonomyPairWire, 0, 8)
	for _, m := range h.node.Autonomy.registry.History() {
		// O histórico é append-only; o estado é o ÚLTIMO valor de cada par. Reconstrói-se em vez
		// de se manter um espelho, para não haver duas fontes que possam divergir.
		if nivel, ok := h.node.Autonomy.registry.Get(m.Agent, m.Domain); ok {
			pares = append(pares, autonomyPairWire{Agent: m.Agent, Domain: m.Domain, Level: nivel.String()})
		}
	}
	sort.Slice(pares, func(i, j int) bool {
		if pares[i].Agent != pares[j].Agent {
			return pares[i].Agent < pares[j].Agent
		}
		return pares[i].Domain < pares[j].Domain
	})
	pares = dedupPares(pares)
	writeJSON(w, http.StatusOK, map[string]any{
		"pairs": pares,
		// Declarado de propósito: um par AUSENTE desta lista não é "sem política" — é L0, o mais
		// supervisionado. Omitir esta nota deixaria a lista parecer exaustiva quando o silêncio
		// é ele próprio uma decisão.
		"unregistered_resolves_to": autonomy.L0.String(),
	})
}

// dedupPares fica com a última ocorrência de cada par (a lista vem do histórico).
func dedupPares(in []autonomyPairWire) []autonomyPairWire {
	out := in[:0]
	var ant autonomyPairWire
	for i, p := range in {
		if i > 0 && p.Agent == ant.Agent && p.Domain == ant.Domain {
			out[len(out)-1] = p
			continue
		}
		out = append(out, p)
		ant = p
	}
	return out
}

// parseAutonomyLevelWire traduz "L0".."L5". Fail-closed: qualquer outra coisa é recusada em vez de
// resolver para o valor-zero — que é L0 e passaria por "aceite" enquanto silenciosamente ignorava
// o que o operador pediu.
func parseAutonomyLevelWire(s string) (autonomy.Level, bool) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "L0":
		return autonomy.L0, true
	case "L1":
		return autonomy.L1, true
	case "L2":
		return autonomy.L2, true
	case "L3":
		return autonomy.L3, true
	case "L4":
		return autonomy.L4, true
	case "L5":
		return autonomy.L5, true
	default:
		return autonomy.L0, false
	}
}

// (mantém o base64 importado coerente com emitterWire.decode)
var _ = base64.StdEncoding
