// AOS-214 (EPIC-18) — REPLAY SOBERANO DE CONTEÚDO SELADO: a superfície de LEITOR do nó `aos`.
// Fecha o residual (b) de DEF-301/AOS-093 — «o REPLAY de um run selado exige acesso do leitor ao
// vault do titular». A cifra por-titular de AOS-093 tornou o conteúdo não-determinístico dos runs
// CIPHERTEXT no Event Store; um LEITOR (um TERCEIRO que reconstrói/inspecciona um run selado)
// precisava de decifração AUTORIZADA POR SOBERANIA. O resume durável in-process (o step-ledger
// decifra no Rebuild sob o vault do nó, já fail-closed em AOS-093) NÃO é duplicado aqui — este
// ficheiro trata SÓ do lado do leitor.
//
// SUPERFÍCIE (decidida e justificada): compõe o [replay.ReplayEngine] ATRÁS de um endpoint SOBERANO
// (GET /runs/{id}/reconstruct), gated pela MESMA governança de leitura de AOS-172/205 que o GET de
// desfecho e o SSE de trajectória já usam — NÃO uma segunda via nem um segundo mecanismo de authz.
// A alternativa (ligar o opener ao SSE de trajectória que já sela D6) foi REJEITADA: o SSE
// transporta os EVENTOS crus por seq (ciphertext, um tail ao vivo) e injectar decifração nesse
// caminho misturaria dois contratos (transporte de log vs. reconstrução de conteúdo) e arriscaria
// devolver claro pela via de streaming. Um endpoint dedicado mantém a reconstrução (decifração)
// ISOLADA atrás do gate soberano, e o opener por-titular só é composto DEPOIS de o gate autorizar.
//
// FAIL-CLOSED em cada porta (a MESMA disciplina de dsar.go / trajectory.go):
//  1. AUTHZ SOBERANA POR-CHAMADOR (D7): board→região fail-closed; leitor não autorizado (região
//     errada / sem credencial) ⇒ o MESMO 404 uniforme e não-enumerável — nunca alcança o opener,
//     nunca vê o claro.
//  2. POSSE (não-enumerável): um run que esta réplica não hospeda nem reteve ⇒ 404 uniforme.
//  3. SELO WORM DE LEITURA SENSÍVEL (D6) como PRÉ-CONDIÇÃO: se o WORM não selar, NEGA (503) — a
//     reconstrução é uma leitura sensível auditada, sem PII no selo.
//  4. RECONSTRÓI via [replay.ReplayEngine.Reconstruct], que decifra o conteúdo selado ATRÁS do
//     gate do opener por-titular. Depois do /dsar/erase (KEK destruída) ⇒ audit.ErrDecrypt ⇒ 410
//     Gone (o direito ao apagamento vale também contra o replay). Um titular sob legal hold NÃO é
//     shredded, pelo que a reconstrução autorizada devolve o conteúdo normalmente.
//
// SEM PII EM LOG/SPAN/ERRO: o conteúdo decifrado só entra no corpo da resposta 200 ao leitor
// AUTORIZADO (o seu deliverable, análogo ao FinalText do GET de desfecho); as mensagens de erro são
// uniformes e nunca carregam o claro.
package main

import (
	"errors"
	"net/http"

	"github.com/aos-ref/kernel/agent-runtime/replay"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// capReadReconstruct é a capability de leitura sensível SELADA no WORM (D6) para a reconstrução
// soberana de conteúdo selado — vocabulário estável, sem PII (a par de [capReadOutcome]/[capReadTrajectory]).
const capReadReconstruct = "read:reconstruct"

// reconstructedTurnWire é a projecção de wire de um turno reconstruído e DECIFRADO devolvido ao
// leitor autorizado. Carrega o conteúdo em claro (o deliverable do leitor soberano) — nunca vai a
// log/span/erro.
type reconstructedTurnWire struct {
	Turn        int      `json:"turn"`
	StepID      string   `json:"step_id,omitempty"`
	Text        string   `json:"text,omitempty"`
	Final       bool     `json:"final,omitempty"`
	ToolOutputs []string `json:"tool_outputs,omitempty"`
}

// reconstructResponse é o desfecho de uma reconstrução soberana autorizada: os turnos decifrados
// do run. NÃO é enumerável (só é servido a um leitor que passou o gate D7+D6).
type reconstructResponse struct {
	RunID string                  `json:"run_id"`
	Turns []reconstructedTurnWire `json:"turns"`
}

// newReaderReplayEngine compõe o [replay.ReplayEngine] do lado do LEITOR com o opener por-titular
// LIGADO ATRÁS do gate soberano: é chamado SÓ depois de [readGovernance.authorize] ter autorizado o
// leitor (ver [handleReconstruct]), pelo que o accessor com o escopo soberano — a âncora de
// autorização do opener — só existe para um leitor autorizado. O motor detém APENAS um leitor do
// Event Store (zero-efeitos estrutural) + o opener; nunca escreve nem chama modelos/tools ao vivo.
//
// DEFESA-EM-PROFUNDIDADE (não é a âncora de negação do nó): aqui o accessor soberano é SEMPRE
// concedido — chegámos a este ponto só porque [handleReconstruct] já passou o gate D7 (authz do
// endpoint), que é a âncora REAL de negação do nó (leitor não autorizado ⇒ 404, nunca alcança o
// opener). O escopo do accessor não varia por-chamador no nó; o gate da camada replay
// ([resolveSealed]: escopo + opener!=nil) é belt-and-suspenders, e a sua falsificabilidade dos «dois
// sentidos» é exercitada IN-PROCESS (motor composto sem opener/escopo), não pelo endpoint. Futuros
// leitores: não presumam que o escopo é per-chamador aqui — a decisão soberana é a D7 acima.
func (h *apiHandler) newReaderReplayEngine(reader readerIdentity) (*replay.ReplayEngine, error) {
	accessor := replay.Accessor{
		Principal: reader.principal,
		Scopes:    []string{replay.DefaultSovereignContentScope},
	}
	return replay.NewEngine(h.node.EventStore, replay.WithContentOpener(h.node.contentOpener, accessor))
}

// handleReconstruct serve GET /runs/{id}/reconstruct: a reconstrução soberana do conteúdo selado de
// um run para um leitor autorizado (AOS-214). Ordem deliberada de portas fail-closed — ver o
// cabeçalho do ficheiro.
func (h *apiHandler) handleReconstruct(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if runID == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	// (1) AUTHZ SOBERANA POR-CHAMADOR (D7). Leitor não autorizado ⇒ 404 uniforme (não-enumerável,
	// sem PII). Gate não composto ⇒ legado (proceed=true): mas a reconstrução EXIGE o gate soberano
	// (é uma leitura de conteúdo sensível decifrado), pelo que sem ele NEGAMOS abaixo (501).
	reader, authorized := h.admitSovereignRead(w, r)
	if !authorized {
		return
	}
	// A reconstrução soberana EXIGE o gate composto: sem ele não há credencial soberana a impor, e
	// devolver conteúdo decifrado seria abrir uma via sem gate. Fail-closed: 501 (desligado).
	if h.readGov == nil {
		writeError(w, http.StatusNotImplemented, "reconstrucao desligada (governanca soberana nao composta)")
		return
	}
	// O opener por-titular + o Event Store têm de estar compostos (num nó real, sempre — defensivo).
	if h.node.EventStore == nil || h.node.contentOpener == nil {
		writeError(w, http.StatusNotImplemented, "reconstrucao desligada (cifra por-titular nao composta)")
		return
	}

	// (2) POSSE (não-enumerável). Confirma que esta réplica hospeda/reteve o run ANTES de selar ou
	// decifrar — um run desconhecido ⇒ o MESMO 404 uniforme de handleGet, sem virar oráculo. A
	// leitura aqui é do stream cru (ciphertext), sem decifração.
	events, rerr := h.node.EventStore.Read(r.Context(), runID, 1)
	if rerr != nil {
		if errors.Is(rerr, eventstore.ErrStreamNotFound) {
			if !h.runKnown(runID) {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
		} else {
			writeError(w, streamSetupErrorStatus(rerr), "reconstrucao indisponivel")
			return
		}
	}
	if len(events) == 0 && !h.runKnown(runID) {
		// Stream vazio e run desconhecido ⇒ 404 uniforme (não abre uma reconstrução vazia).
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	// (3) SELO WORM DE LEITURA SENSÍVEL (D6) como PRÉ-CONDIÇÃO — a reconstrução decifra conteúdo
	// sensível e não pode ser silenciosa. Se o WORM não selar, NEGA fail-closed (503).
	if !h.sealSensitiveRead(w, r, reader, runID, capReadReconstruct) {
		return
	}

	// (4) RECONSTRÓI atrás do gate do opener por-titular (só composto porque o leitor está
	// autorizado). Decifra o conteúdo selado; depois do crypto-shredding ⇒ audit.ErrDecrypt ⇒ 410.
	engine, err := h.newReaderReplayEngine(reader)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reconstrucao indisponivel")
		return
	}
	turns, err := engine.Reconstruct(r.Context(), runID)
	if err != nil {
		writeError(w, reconstructErrorStatus(err), "reconstrucao indisponivel")
		return
	}

	resp := reconstructResponse{RunID: runID, Turns: make([]reconstructedTurnWire, 0, len(turns))}
	for _, t := range turns {
		tw := reconstructedTurnWire{Turn: t.Turn, StepID: t.StepID, Text: t.Response.Text, Final: t.Response.Final}
		for _, out := range t.ToolResults {
			tw.ToolOutputs = append(tw.ToolOutputs, string(out.Value))
		}
		resp.Turns = append(resp.Turns, tw)
	}
	writeJSON(w, http.StatusOK, resp)
}

// reconstructErrorStatus mapeia os erros da reconstrução soberana a status HTTP, com corpo uniforme
// (nunca conteúdo decifrado):
//   - audit.ErrDecrypt (KEK destruída pelo crypto-shredding) ⇒ 410 Gone — o conteúdo foi apagado
//     por DSAR e o replay não o ressuscita (o direito ao apagamento vale contra o replay);
//   - replay.ErrPayloadAccessDenied (gate do opener negou) ⇒ 403 — nunca o claro;
//   - replay.ErrNoTrajectory (sem capturas) ⇒ 404 uniforme;
//   - o resto ⇒ 500 sem detalhe.
func reconstructErrorStatus(err error) int {
	switch {
	case errors.Is(err, audit.ErrDecrypt):
		return http.StatusGone
	case errors.Is(err, replay.ErrPayloadAccessDenied):
		return http.StatusForbidden
	case errors.Is(err, replay.ErrNoTrajectory):
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}
