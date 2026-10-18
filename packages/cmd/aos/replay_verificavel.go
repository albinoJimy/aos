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
// # UM RUN RETOMADO DIVERGE POR DESENHO — MAS SÓ NUM TURNO, E SÓ SE A RETOMA FOR VERDADEIRA
//
// ADR-037 §5: a captura do turno re-executado é a da primeira vida, e o primeiro turno gravado na
// segunda vida remonta-se de um tail que ela não viu. Essa divergência não prova adulteração.
//
// A desculpa é estreita de propósito (revisão do AOS-533): uma linha `run.state.transition`
// escrita crua no stream bastava para pôr `retomado: true`, calar a nota e tirar a amostra do SLI
// — e quem tem escrita crua não recalcula um `prompt_hash` sem o claro, pelo que forjar a retoma
// anulava a detecção. Ver [analisarVidas]: a retoma tem de vir da máquina de estados COERENTE e
// ser corroborada pelo stream de LEASE; e só o PRIMEIRO turno gravado depois de cada retoma
// corroborada é desculpado. Uma divergência noutro turno é divergência normal — sem nota e com
// amostra no SLI, mesmo num run retomado.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

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

// anchorsSemFonteIndependente são o que a rota NUNCA verifica contra uma fonte independente, e por
// isso vai sempre em `anchors_not_verified` (revisão do AOS-533):
//   - "tools_from_log": o tool set da spec é o `manifest.tools` do próprio log — o replay prova a
//     coerência do prompt com o log, não que o tool set gravado é o que o nó tinha;
//   - "projection_version": a versão da projecção nativa (AOS-513) não entra no `prompt_hash` nem
//     tem âncora no motor.
var anchorsSemFonteIndependente = []string{"tools_from_log", "projection_version"}

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

	// (5) AS VIDAS DO RUN: retoma só da máquina de estados coerente, corroborada pelo lease.
	v := h.vidasDoRun(r.Context(), runID, events)

	// (6) O REPLAY, atrás do opener por-titular. Sem turno desculpável, o span vai ao tracer do nó
	// (amostra do SLI). Com ele, corre-se primeiro SEM tracer — só depois do resultado se sabe se a
	// divergência cai no turno desculpado — e, se não cair, corre-se outra vez COM tracer. O motor
	// é determinista e sem efeitos: a segunda corrida dá o mesmo resultado, e só a pagam os runs
	// retomados.
	res, ok := h.correrReplay(r.Context(), w, reader, runID, spec, !v.retomado())
	if !ok {
		return
	}
	desculpada := res.Divergence != nil && v.desculpados[res.Divergence.Turn]
	amostra := !v.retomado() && h.replayAmostra()
	if v.retomado() && !desculpada {
		if res, ok = h.correrReplay(r.Context(), w, reader, runID, spec, true); !ok {
			return
		}
		amostra = h.replayAmostra()
	}
	writeJSON(w, http.StatusOK, replayResponseDe(res, v.retomado(), desculpada, amostra))
}

// correrReplay compõe o motor — com o tracer do nó se `comSpan`, para o span `replay`
// ([replay.OpReplay], com `aos.replay.fidelity`) alimentar o SLI `replay_fidelity` — e corre o
// replay. ok=false ⇒ a recusa já foi escrita.
func (h *apiHandler) correrReplay(ctx context.Context, w http.ResponseWriter, reader readerIdentity, runID string, spec replay.TrajectorySpec, comSpan bool) (replay.ReplayResult, bool) {
	var extra []replay.EngineOption
	if comSpan && h.node.Tracer != nil {
		extra = append(extra, replay.WithTracer(h.node.Tracer))
	}
	engine, err := h.newReaderReplayEngine(reader, extra...)
	if err != nil {
		writeReplayError(w, http.StatusInternalServerError, causaReplayFalhou)
		return replay.ReplayResult{}, false
	}
	res, err := engine.Replay(ctx, runID, replay.Options{Spec: spec})
	if err != nil {
		if errors.Is(err, replay.ErrNoTrajectory) {
			writeError(w, http.StatusNotFound, "not found")
			return replay.ReplayResult{}, false
		}
		status, causa := replayErrorStatus(err)
		writeReplayError(w, status, causa)
		return replay.ReplayResult{}, false
	}
	return res, true
}

// replayAmostra diz se um span emitido pelo tracer do nó chega ao avaliador de SLOs: só com a
// torneira composta (observabilidade ligada). É o produtor A PEDIDO do SLI `replay_fidelity`.
func (h *apiHandler) replayAmostra() bool {
	return h.node.Tracer != nil && h.node.sloTap != nil
}

// replayResponseDe projecta o resultado do motor no wire — só hashes, contagens e vocabulário fechado.
// A nota só sai com uma divergência DESCULPADA (o primeiro turno de uma retoma corroborada).
func replayResponseDe(res replay.ReplayResult, retomado, desculpada, amostra bool) replayResponse {
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
	out.AnchorsNotVerified = append(out.AnchorsNotVerified, anchorsSemFonteIndependente...)
	if d := res.Divergence; d != nil {
		out.Divergence = &replayDivergenceWire{
			Turn: d.Turn, StepID: d.StepID, Reason: d.Reason, ExpectedHash: d.ExpectedHash, ActualHash: d.ActualHash,
		}
		if desculpada {
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

// vidasDoRun lê o stream de lease do run e analisa as vidas ([analisarVidas]). Um stream de lease
// ilegível ou ausente é «sem corroboração»: nenhuma retoma é aceite, e uma divergência fica normal
// — o lado seguro da detecção.
func (h *apiHandler) vidasDoRun(ctx context.Context, runID string, events []eventstore.Event) vidas {
	lease, err := h.node.EventStore.Read(ctx, durable.LeaseStreamID(runID), 1)
	if err != nil {
		lease = nil
	}
	return analisarVidas(events, lease)
}

// vidas é o que o stream de um run e o seu stream de lease provam, juntos, sobre as suas vidas.
type vidas struct {
	// desculpados são os turnos cuja divergência não prova adulteração: o PRIMEIRO turno gravado
	// depois de cada retoma corroborada. Vazio ⇒ o run não é tratado como retomado.
	desculpados map[int]bool
}

func (v vidas) retomado() bool { return len(v.desculpados) > 0 }

// analisarVidas decide se o run foi retomado e qual o turno desculpável de cada retoma.
//
//  1. A MÁQUINA DE ESTADOS TEM DE SER COERENTE. As transições, pela ordem do stream, têm de ser a
//     cadeia que o [state.Machine] escreve: produtor [state.DefaultProducerNHI], `step_id`
//     `state-1`, `state-2`, … sem buracos, a primeira a sair de `ready`, cada `from` igual ao `to`
//     anterior, cada aresta válida ([state.IsValidTransition]), nada depois de um terminal e
//     fencing tokens não decrescentes. Qualquer falha ⇒ nenhuma retoma. A PoC da revisão — uma
//     `waiting_on_human → running` acrescentada depois do `complete` — cai aqui.
//  2. A RETOMA TEM DE ESTAR NO STREAM DE LEASE. Cada vida reclama o lease
//     ([durable.EventTypeLeaseClaimed], produtor [durable.DefaultLeaseProducerNHI], tokens 1, 2, …
//     estritamente crescentes). É a evidência independente escolhida: está noutro stream, é escrita
//     por concorrência optimista (expected_seq) e cobre as DUAS retomas — a por aprovação (AOS-021)
//     e a por crash (AOS-253). O grant four-eyes consumido só cobriria a primeira.
//  3. O CLAIM TEM DE SER USADO PELA MÁQUINA. Um claim de token k ≥ 2 só conta se alguma transição
//     da cadeia levar esse token: a máquina escreveu sob a vida nova.
//  4. O TURNO DESCULPADO é o primeiro `turn.recorded` do stream do run com carimbo ≥ ao do claim —
//     o primeiro turno que a vida nova gravou (os anteriores reproduzem-se sem re-gravar).
//
// O que isto NÃO fecha: quem escreve cru nos DOIS streams, com a cadeia inteira certa, forja também
// a retoma. Mas deixa de chegar uma linha, e a desculpa fica presa a um só turno por retoma.
func analisarVidas(runEvents, leaseEvents []eventstore.Event) vidas {
	// (1) A cadeia.
	anterior := state.Ready
	var ultimoToken uint64
	tokensUsados := map[uint64]bool{}
	n := 0
	for _, ev := range runEvents {
		if ev.Type != state.EventTypeTransition {
			continue
		}
		n++
		var t struct {
			From       state.State `json:"from"`
			To         state.State `json:"to"`
			TokenValue uint64      `json:"token_value"`
		}
		if json.Unmarshal(ev.Payload, &t) != nil || ev.Producer.NHIID != state.DefaultProducerNHI ||
			ev.StepID != fmt.Sprintf("state-%d", n) || state.IsTerminal(anterior) || t.From != anterior ||
			!state.IsValidTransition(t.From, t.To) || (t.TokenValue != 0 && t.TokenValue < ultimoToken) {
			return vidas{}
		}
		anterior = t.To
		if t.TokenValue != 0 {
			ultimoToken = t.TokenValue
			tokensUsados[t.TokenValue] = true
		}
	}
	if n == 0 {
		return vidas{}
	}

	// (2) e (3): os claims, e os que a máquina usou.
	v := vidas{desculpados: map[int]bool{}}
	var proximo uint64 = 1
	for _, ev := range leaseEvents {
		if ev.Type != durable.EventTypeLeaseClaimed {
			continue
		}
		var c struct {
			Token uint64 `json:"token"`
		}
		if json.Unmarshal(ev.Payload, &c) != nil || ev.Producer.NHIID != durable.DefaultLeaseProducerNHI || c.Token != proximo {
			return vidas{}
		}
		proximo++
		if c.Token < 2 || !tokensUsados[c.Token] {
			continue
		}
		// (4) O primeiro turno gravado pela vida nova.
		desde, err := time.Parse(time.RFC3339Nano, ev.Ts)
		if err != nil {
			return vidas{}
		}
		if turno, ok := primeiroTurnoDesde(runEvents, desde); ok {
			v.desculpados[turno] = true
		}
	}
	return v
}

// primeiroTurnoDesde devolve o número do primeiro `turn.recorded` do stream com carimbo ≥ desde.
func primeiroTurnoDesde(runEvents []eventstore.Event, desde time.Time) (int, bool) {
	for _, ev := range runEvents {
		if ev.Type != agentruntime.EventTypeTurnRecorded {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, ev.Ts)
		if err != nil || ts.Before(desde) {
			continue
		}
		var p struct {
			Turn int `json:"turn"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil {
			return 0, false
		}
		return p.Turn, true
	}
	return 0, false
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
