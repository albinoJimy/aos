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
// ser corroborada pelo stream de LEASE (um claim por vida, emparelhado por ordem com a transição de
// retoma da cadeia — ou, numa re-hospedagem por crash, com a transição que leva o token dessa
// vida); e só o PRIMEIRO turno gravado depois (por seq) do evento que corroborou a vida é
// desculpado. Uma divergência noutro turno é divergência normal — sem nota e com
// amostra no SLI, mesmo num run retomado.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	causaReplayLeaseIndisp        = "lease_indisponivel"
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
	v, err := h.vidasDoRun(r.Context(), runID, events)
	if err != nil {
		writeReplayError(w, http.StatusServiceUnavailable, causaReplayLeaseIndisp)
		return
	}

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
// INEXISTENTE é um run sem lease (sem vidas a corroborar). Um erro a LER o lease é devolvido: o
// chamador responde 503 sem amostra — tratá-lo como «sem corroboração» pôs a divergência
// desculpável de um run retomado a disparar o alerta CRITICAL por uma falha de leitura.
func (h *apiHandler) vidasDoRun(ctx context.Context, runID string, events []eventstore.Event) (vidas, error) {
	lease, err := h.node.EventStore.Read(ctx, durable.LeaseStreamID(runID), 1)
	if err != nil {
		if !errors.Is(err, eventstore.ErrStreamNotFound) {
			return vidas{}, err
		}
		lease = nil
	}
	return analisarVidas(events, lease), nil
}

// vidas é o que o stream de um run e o seu stream de lease provam, juntos, sobre as suas vidas.
type vidas struct {
	// corroboradas é o nº de vidas k ≥ 2 corroboradas — o run é retomado se for > 0.
	corroboradas int
	// desculpados são os turnos cuja divergência não prova adulteração: o PRIMEIRO turno gravado
	// depois (por seq) do evento que corroborou cada vida.
	desculpados map[int]bool
}

func (v vidas) retomado() bool { return v.corroboradas > 0 }

// analisarVidas decide se o run foi retomado e qual o turno desculpável de cada retoma. Não lê
// carimbos de tempo: só a ordem (seq) de cada stream.
//
//  1. A MÁQUINA DE ESTADOS TEM DE SER COERENTE. As transições, pela ordem do stream, têm de ser a
//     cadeia que o [state.Machine] escreve: produtor [state.DefaultProducerNHI], `step_id`
//     `state-1`, `state-2`, … sem buracos, a primeira a sair de `ready`, cada `from` igual ao `to`
//     anterior, cada aresta válida ([state.IsValidTransition]), nada depois de um terminal e
//     fencing tokens não decrescentes. Qualquer falha ⇒ nenhuma retoma. A PoC da 1.ª revisão — uma
//     `waiting_on_human → running` acrescentada depois do `complete` — cai aqui.
//  2. OS CLAIMS DO LEASE TÊM DE SER OS DO GESTOR DE LEASE: produtor
//     [durable.DefaultLeaseProducerNHI], tokens 1, 2, … estritamente crescentes. É a evidência
//     independente: noutro stream, escrita por concorrência optimista (expected_seq).
//  3. CADA CLAIM k ≥ 2 PRECISA DE UM PAR NA CADEIA, e o par é por ORDEM: o k-ésimo claim emparelha
//     com a (k−1)-ésima transição de retoma da cadeia (`waiting_on_human → running` ou
//     `paused → running` — as transições de retoma NÃO levam token, ver [runGate.ResumeFromHuman]
//     e [runGate.Resume]). Sem essa transição (a re-hospedagem por crash, AOS-253, que encontra o
//     run em `running` e não transita no arranque), o par é a primeira transição que leve o token k
//     (na prática, a terminal ou a escalada seguinte dessa vida). Um claim sem par não corrobora.
//  4. O TURNO DESCULPADO de cada vida corroborada é o primeiro `turn.recorded` DEPOIS, por seq, do
//     evento que a corroborou: numa retoma por aprovação ou por pausa, o primeiro turno que a vida
//     nova gravou (os anteriores reproduzem-se sem re-gravar).
//
// O QUE ISTO NÃO COBRE, declarado: numa re-hospedagem por crash o par é uma transição posterior
// aos turnos dessa vida, pelo que nenhum turno dela é desculpado — uma divergência nela (a do
// ADR-037 §5, retoma por crash depois de uma falha de tool) sai como divergência normal. E quem
// escreve cru nos DOIS streams, com a cadeia inteira certa, ainda forja uma retoma.
func analisarVidas(runEvents, leaseEvents []eventstore.Event) vidas {
	// (1) A cadeia, com as posições das transições de retoma e do primeiro uso de cada token.
	anterior := state.Ready
	var ultimoToken uint64
	var retomas []int
	usoDoToken := map[uint64]int{}
	n := 0
	for i, ev := range runEvents {
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
		if t.To == state.Running && (t.From == state.WaitingOnHuman || t.From == state.Paused) {
			retomas = append(retomas, i)
		}
		if t.TokenValue != 0 {
			ultimoToken = t.TokenValue
			if _, visto := usoDoToken[t.TokenValue]; !visto {
				usoDoToken[t.TokenValue] = i
			}
		}
	}
	if n == 0 {
		return vidas{}
	}

	// (2) Os claims.
	var claims []uint64
	for _, ev := range leaseEvents {
		if ev.Type != durable.EventTypeLeaseClaimed {
			continue
		}
		var c struct {
			Token uint64 `json:"token"`
		}
		if json.Unmarshal(ev.Payload, &c) != nil || ev.Producer.NHIID != durable.DefaultLeaseProducerNHI ||
			c.Token != uint64(len(claims)+1) {
			return vidas{}
		}
		claims = append(claims, c.Token)
	}

	// (3) e (4): o par de cada vida k ≥ 2, e o primeiro turno gravado depois dele.
	v := vidas{desculpados: map[int]bool{}}
	for k := 2; k <= len(claims); k++ {
		par := -1
		if k-2 < len(retomas) {
			par = retomas[k-2]
		} else if i, ok := usoDoToken[uint64(k)]; ok {
			par = i
		}
		if par < 0 {
			continue
		}
		v.corroboradas++
		if turno, ok := primeiroTurnoDepois(runEvents, par); ok {
			v.desculpados[turno] = true
		}
	}
	return v
}

// primeiroTurnoDepois devolve o número do primeiro `turn.recorded` do stream depois da posição dada.
func primeiroTurnoDepois(runEvents []eventstore.Event, posicao int) (int, bool) {
	for _, ev := range runEvents[posicao+1:] {
		if ev.Type != agentruntime.EventTypeTurnRecorded {
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
