package main

// replay_verificavel.go — O REPLAY VERIFICÁVEL DE PRODUÇÃO (AOS-533): `GET /runs/{id}/replay`.
//
// # O RESÍDUO QUE ISTO FECHA (no código; fecha-se de vez com uma série em produção)
//
// O `/reconstruct` (AOS-214) é o [replay.ReplayEngine] em modo de RECONSTRUÇÃO: decifra as
// capturas e devolve o conteúdo, sem re-materializar prompt nenhum. A verificação dos
// `prompt_hash` ([replay.ReplayEngine.Replay]) só corria nos testes e no gate `replay` — na série
// A5.1 (v0.1.58) não havia por onde a pedir a um run de produção. É o resíduo da §7 do
// acompanhamento da fronteira runtime↔modelo.
//
// # AS PORTAS SÃO AS DO `/reconstruct`, PELA MESMA ORDEM
//
// [apiHandler.portasDaLeituraDeConteudo]: D7 (404/401 uniforme), composição (501), posse com a
// trava de stream interno (AOS-426), selo WORM D6 como pré-condição (503) — com selo PRÓPRIO
// ([capReadReplay]): a rota decifra as capturas e o registo de retoma, e o auditor tem de distinguir
// quem verificou de quem leu o conteúdo.
//
// # DE ONDE VEM A SPEC, E O QUE FICA FORA
//
// Os inputs determinísticos do prompt NÃO vivem no log (só os seus hashes). Vêm do REGISTO DE
// RETOMA do run (AOS-021/253 — escrito pelo nó no arranque de cada hospedagem, cifrado por
// titular, o MESMO Goal já redigido que o run hospedou): system, objectivo, memory_context, inputs
// do plano (AOS-414) e aviso da nova tentativa (AOS-506). O tool set vem do `manifest.tools` do
// primeiro turno — o que o run OFERECEU (AOS-486), e não o tool set inteiro do registo; é o
// precedente de [hashDaSementeCom], que já recalcula o turno 1 em produção.
//
// FICAM FORA, e a resposta di-lo em `anchors_not_verified`: o modelo (o `manifest.model.params`
// funde os params do perfil da rota, e comparar com os do registo daria falsos «model»), a âncora
// de versão do assembler (cada turno monta-se no layout que gravou, AOS-489), a sequência de
// step_id e a autoridade (cega na via durável, ver [replay.Options.VerifyAuthority]).
//
// # A RESPOSTA NÃO TEM CONTEÚDO
//
// Hashes, contagens, o veredicto do kernel (vocabulário fechado) e a localização da divergência.
// Nunca o texto do modelo, os resultados das tools, o objectivo ou os inputs — quem quer o
// conteúdo tem o `/reconstruct`, com o seu selo.
//
// # UM RUN RETOMADO DIVERGE POR DESENHO
//
// ADR-037 §5: a captura do turno re-executado é a da primeira vida, e o turno seguinte foi gravado
// na segunda. A divergência num run retomado não prova adulteração; a resposta marca-o
// (`retomado: true` e uma nota) e o span não alimenta o SLI ([replaySpanOptions]).

import (
	"encoding/json"
	"errors"
	"net/http"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	"github.com/aos-ref/kernel/agent-runtime/state"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// specOriginResumeRecord — a única origem da spec nesta versão: o registo de retoma do run.
const specOriginResumeRecord = "resume_record"

// notaRetomado é o texto constante que acompanha a divergência de um run retomado.
const notaRetomado = "divergencia num run retomado nao prova adulteracao: a captura do turno re-executado e a da primeira vida e o turno seguinte foi gravado na segunda (ADR-037 §5)"

// Causas de recusa do `/replay`, em vocabulário fechado (o campo `code` do corpo de erro).
const (
	causaReplayRegistoNaoComposto = "registo_de_retoma_nao_composto"
	causaReplaySemRegisto         = "sem_registo_de_retoma"
	causaReplayRegistoRecusado    = "registo_de_retoma_recusado"
	causaReplayRegistoIndisp      = "registo_de_retoma_indisponivel"
	causaReplayCapturaIncompleta  = "captura_incompleta"
	causaReplayCapturaCorrompida  = "captura_corrompida"
	causaReplayLayoutDesconhecido = "layout_desconhecido"
	causaReplayAvisoDesconhecido  = "aviso_desconhecido"
	causaReplayPayloadStore       = "payload_store_nao_composto"
	causaReplayApagado            = "conteudo_apagado"
	causaReplayConteudoIndisp     = "conteudo_indisponivel"
	causaReplayAcessoNegado       = "acesso_negado"
	causaReplayFalhou             = "replay_falhou"
)

// anchorsDoReplay são as comparações não-prompt que o motor sabe fazer ([replay.ReplayResult.AnchorsVerified]).
var anchorsDoReplay = []string{"model", "assembly_version", "step_id", "authority"}

// replayFidelityWire é a fidelidade com os dois contadores de que é a fracção.
type replayFidelityWire struct {
	Value    float64 `json:"value"`
	Matched  int     `json:"matched"`
	Verified int     `json:"verified"`
}

// replayTurnWire é um turno verificado: só hashes.
type replayTurnWire struct {
	Turn               int    `json:"turn"`
	StepID             string `json:"step_id,omitempty"`
	PromptHash         string `json:"prompt_hash"`
	RecordedPromptHash string `json:"recorded_prompt_hash"`
	Matched            bool   `json:"matched"`
}

// replayDivergenceWire localiza a divergência.
type replayDivergenceWire struct {
	Turn         int    `json:"turn"`
	StepID       string `json:"step_id,omitempty"`
	Reason       string `json:"reason"`
	ExpectedHash string `json:"expected_hash"`
	ActualHash   string `json:"actual_hash"`
}

// replayResponse é o veredicto de fidelidade de um run. SEM conteúdo — ver o cabeçalho.
type replayResponse struct {
	RunID              string                `json:"run_id"`
	Fidelity           replayFidelityWire    `json:"fidelity"`
	Turns              []replayTurnWire      `json:"turns"`
	Diverged           bool                  `json:"diverged"`
	Divergence         *replayDivergenceWire `json:"divergence,omitempty"`
	AnchorsVerified    []string              `json:"anchors_verified"`
	AnchorsNotVerified []string              `json:"anchors_not_verified"`
	FinalStateHash     string                `json:"final_state_hash,omitempty"`
	Verdict            *agentruntime.Verdict `json:"verdict,omitempty"`
	SpecOrigin         string                `json:"spec_origin"`
	Retomado           bool                  `json:"retomado"`
	Nota               string                `json:"nota,omitempty"`
	// SLIAmostrado diz se ESTA chamada deu uma amostra ao SLI `replay_fidelity`: só com a torneira
	// de spans composta e nunca para um run retomado.
	SLIAmostrado bool `json:"sli_amostrado"`
}

// writeReplayError escreve uma recusa do `/replay` com a causa em vocabulário fechado. O corpo
// nunca leva o conteúdo do run nem o erro cru (que pode citar o turno e o step).
func writeReplayError(w http.ResponseWriter, status int, causa string) {
	writeJSON(w, status, map[string]string{"error": "replay indisponivel", "code": causa})
}

// handleReplay serve GET /runs/{id}/replay (AOS-533) — ver o cabeçalho do ficheiro.
func (h *apiHandler) handleReplay(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	reader, events, ok := h.portasDaLeituraDeConteudo(w, r, runID, leituraDeConteudo{
		desligada: "replay desligado", indisponivel: "replay indisponivel", capability: capReadReplay,
		// Sem o registo de retoma composto (four-eyes) não há de onde tirar a spec: a rota não
		// existe neste nó. Corre antes da posse e do selo — uma rota desligada não sela.
		composta: func(w http.ResponseWriter) bool {
			if h.node.ResumeRecords == nil {
				writeJSON(w, http.StatusNotImplemented, map[string]string{
					"error": "replay desligado (registo de retoma nao composto)", "code": causaReplayRegistoNaoComposto,
				})
				return false
			}
			return true
		},
	})
	if !ok {
		return
	}

	// (4) A SPEC, do registo de retoma (decifrado pelo cifrador do nó — depois do selo, porque é
	// conteúdo do titular).
	rec, tem, err := h.node.ResumeRecords.Get(r.Context(), runID)
	if err != nil {
		status, causa := registoErrorStatus(err)
		writeReplayError(w, status, causa)
		return
	}
	if !tem {
		writeReplayError(w, http.StatusUnprocessableEntity, causaReplaySemRegisto)
		return
	}
	spec := replay.TrajectorySpec{
		System:        rec.System,
		Tools:         toolsDoPrimeiroTurno(events),
		Objective:     rec.Objective,
		MemoryContext: rec.MemoryContext,
		Inputs:        rec.Inputs,
		RetryNotice:   rec.RetryNotice,
	}

	// (5) O REPLAY, atrás do opener por-titular. Um run retomado não amostra o SLI.
	retomado := runRetomado(events)
	extra, amostra := h.replaySpanOptions(retomado)
	engine, err := h.newReaderReplayEngine(reader, extra...)
	if err != nil {
		writeReplayError(w, http.StatusInternalServerError, causaReplayFalhou)
		return
	}
	res, err := engine.Replay(r.Context(), runID, replay.Options{Spec: spec})
	if err != nil {
		if errors.Is(err, replay.ErrNoTrajectory) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		status, causa := replayErrorStatus(err)
		writeReplayError(w, status, causa)
		return
	}
	writeJSON(w, http.StatusOK, replayResponseDe(res, retomado, amostra))
}

// replaySpanOptions compõe o tracer do nó no motor — o span `replay` ([replay.OpReplay], com
// `aos.replay.fidelity`) passa pela torneira e alimenta o SLI `replay_fidelity` (AOS-533) — EXCEPTO
// para um run retomado: a divergência dele é por desenho (ADR-037 §5) e uma amostra abaixo de 1
// faria disparar o alerta CRITICAL `replay_fidelity_low` sobre um run sem defeito.
//
// Devolve também se a chamada AMOSTRA de facto: sem torneira composta (observabilidade desligada)
// o span vai para o NoopTracer e não chega ao avaliador.
func (h *apiHandler) replaySpanOptions(retomado bool) ([]replay.EngineOption, bool) {
	if retomado || h.node.Tracer == nil {
		return nil, false
	}
	return []replay.EngineOption{replay.WithTracer(h.node.Tracer)}, h.node.sloTap != nil
}

// replayResponseDe projecta o resultado do motor no wire — só hashes, contagens e vocabulário fechado.
func replayResponseDe(res replay.ReplayResult, retomado, amostra bool) replayResponse {
	out := replayResponse{
		RunID:              res.RunID,
		Turns:              make([]replayTurnWire, 0, len(res.Steps)),
		Diverged:           res.Divergence != nil,
		AnchorsVerified:    append([]string{}, res.AnchorsVerified...),
		AnchorsNotVerified: []string{},
		FinalStateHash:     res.FinalStateHash,
		Verdict:            res.Verdict,
		SpecOrigin:         specOriginResumeRecord,
		Retomado:           retomado,
		SLIAmostrado:       amostra,
	}
	matched := 0
	for _, s := range res.Steps {
		if s.Matched {
			matched++
		}
		out.Turns = append(out.Turns, replayTurnWire{
			Turn: s.Turn, StepID: s.StepID, PromptHash: s.PromptHash, RecordedPromptHash: s.RecordedPromptHash, Matched: s.Matched,
		})
	}
	out.Fidelity = replayFidelityWire{Value: res.Fidelity, Matched: matched, Verified: len(res.Steps)}
	for _, a := range anchorsDoReplay {
		corrida := false
		for _, v := range res.AnchorsVerified {
			corrida = corrida || v == a
		}
		if !corrida {
			out.AnchorsNotVerified = append(out.AnchorsNotVerified, a)
		}
	}
	if d := res.Divergence; d != nil {
		out.Divergence = &replayDivergenceWire{
			Turn: d.Turn, StepID: d.StepID, Reason: d.Reason, ExpectedHash: d.ExpectedHash, ActualHash: d.ActualHash,
		}
		if retomado {
			out.Nota = notaRetomado
		}
	}
	return out
}

// toolsDoPrimeiroTurno devolve o tool set que o run OFERECEU, do `manifest.tools` do primeiro
// `turn.recorded` (escrito pelo nó). nil sem turnos — o motor recusa a seguir com
// [replay.ErrNoTrajectory].
func toolsDoPrimeiroTurno(events []eventstore.Event) []agentruntime.ToolSpec {
	primeiro, tem := 0, false
	var tools []agentruntime.ToolSpec
	for _, ev := range events {
		if ev.Type != agentruntime.EventTypeTurnRecorded {
			continue
		}
		var p struct {
			Turn     int                   `json:"turn"`
			Manifest agentruntime.Manifest `json:"manifest"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil {
			continue // o motor relê o stream e recusa um turno ilegível com a sua própria causa
		}
		if tem && p.Turn >= primeiro {
			continue
		}
		primeiro, tem = p.Turn, true
		tools = make([]agentruntime.ToolSpec, len(p.Manifest.Tools))
		for i, dep := range p.Manifest.Tools {
			tools[i] = agentruntime.ToolSpec(dep)
		}
	}
	return tools
}

// runRetomado diz se o run teve mais de uma vida, pelo que o seu stream regista: uma transição
// `waiting_on_human → running` (a retoma por aprovação, AOS-021) ou transições com mais de um
// fencing token (uma re-hospedagem reclama um lease novo — a retoma por crash, AOS-253).
func runRetomado(events []eventstore.Event) bool {
	tokens := map[uint64]bool{}
	for _, ev := range events {
		if ev.Type != state.EventTypeTransition {
			continue
		}
		var t struct {
			From       state.State `json:"from"`
			To         state.State `json:"to"`
			TokenValue uint64      `json:"token_value"`
		}
		if json.Unmarshal(ev.Payload, &t) != nil {
			continue
		}
		if t.From == state.WaitingOnHuman && t.To == state.Running {
			return true
		}
		if t.TokenValue != 0 {
			tokens[t.TokenValue] = true
		}
	}
	return len(tokens) > 1
}

// registoErrorStatus mapeia a falha de leitura do registo de retoma.
func registoErrorStatus(err error) (int, string) {
	switch {
	case integration.RegistoDeRetomaRecusado(err):
		return http.StatusUnprocessableEntity, causaReplayRegistoRecusado
	case errors.Is(err, durable.ErrConteudoIndisponivel):
		return http.StatusServiceUnavailable, causaReplayConteudoIndisp
	case errors.Is(err, audit.ErrDecrypt):
		// O titular foi apagado (/dsar/erase): o registo é indecifrável, e o replay não o ressuscita.
		return http.StatusGone, causaReplayApagado
	default:
		return http.StatusServiceUnavailable, causaReplayRegistoIndisp
	}
}

// replayErrorStatus mapeia os erros do [replay.ReplayEngine.Replay]. As classes do `/reconstruct`
// ([reconstructErrorStatus]) ficam iguais; acrescentam-se as do replay, cada uma com causa.
//
// [replay.ErrPayloadStoreRequired] é 501, e não 422: a trajectória pode estar inteira — uma
// captura de referência (mode 3, AOS-079) resolve-se num nó que componha o PayloadStore, e este
// não compõe nenhum. É uma limitação do nó, não um defeito do run.
func replayErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, durable.ErrConteudoIndisponivel):
		return http.StatusServiceUnavailable, causaReplayConteudoIndisp
	case errors.Is(err, audit.ErrDecrypt):
		return http.StatusGone, causaReplayApagado
	case errors.Is(err, replay.ErrPayloadAccessDenied):
		return http.StatusForbidden, causaReplayAcessoNegado
	case errors.Is(err, replay.ErrPayloadStoreRequired):
		return http.StatusNotImplemented, causaReplayPayloadStore
	case errors.Is(err, agentruntime.ErrUnknownAssemblyVersion):
		return http.StatusUnprocessableEntity, causaReplayLayoutDesconhecido
	case errors.Is(err, agentruntime.ErrUnknownRetryNotice):
		return http.StatusUnprocessableEntity, causaReplayAvisoDesconhecido
	case errors.Is(err, replay.ErrIncompleteCapture), errors.Is(err, replay.ErrMissingCapture):
		return http.StatusUnprocessableEntity, causaReplayCapturaIncompleta
	case errors.Is(err, replay.ErrCorruptCapture), errors.Is(err, replay.ErrPayloadIntegrity):
		return http.StatusUnprocessableEntity, causaReplayCapturaCorrompida
	default:
		return http.StatusInternalServerError, causaReplayFalhou
	}
}
