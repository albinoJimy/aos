package replay

// AOS-214 — REPLAY SOBERANO DE CONTEÚDO SELADO. Fecha o residual (b) de DEF-301/AOS-093: «o
// REPLAY de um run selado exige acesso do leitor ao vault do titular». A cifra por-titular de
// AOS-093 tornou o conteúdo não-determinístico dos runs CIPHERTEXT no Event Store
// ([capturePayload.SealedContent] + [capturePayload.SealedSubject]); um LEITOR (um TERCEIRO que
// reconstrói/inspecciona um run selado) obtinha ciphertext — a reconstrução do lado do leitor não
// tinha um [agentruntime.ContentOpener] GATED por soberania.
//
// Este ficheiro liga o opener por-titular ATRÁS do mesmo padrão de gate que o [ReplayEngine] já
// traz para o content-capture mode 3 (AOS-079): um [Accessor] AUTORIZADO cujo escopo é a âncora,
// e [ErrPayloadAccessDenied] como a negação fail-closed. O gate SOBERANO real (credencial forte
// AOS-205 + região do titular) vive no read-path do nó (cmd/aos): o opener/accessor só são
// compostos DEPOIS de o gate soberano autorizar — um leitor não autorizado nunca alcança o opener.
//
// NÃO DUPLICA o resume durável in-process (o step-ledger decifra no Rebuild sob o vault do nó, já
// fail-closed em AOS-093/[durable]) — trata SÓ do lado do LEITOR.
//
// FAIL-CLOSED e SEM FUGAS: conteúdo selado sem opener/accessor autorizado ⇒ [ErrPayloadAccessDenied]
// (nunca o texto em claro); depois do crypto-shredding (KEK destruída) ⇒ o [agentruntime.ContentOpener]
// devolve o seu erro de decifração (audit.ErrDecrypt), propagado tal-qual — o direito ao apagamento
// vale também contra o replay. O plaintext decifrado NUNCA é registado/spanned/erro — só é devolvido
// ao chamador autorizado.

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// DefaultSovereignContentScope é o escopo de LEITURA de conteúdo selado exigido no [Accessor]
// composto por [WithContentOpener] — a âncora de autorização do opener por-titular, análoga a
// [DefaultPayloadReadScope] do mode 3. O read-path soberano do nó só compõe um accessor com este
// escopo DEPOIS de a credencial forte (AOS-205) + a região autorizarem; um accessor sem ele é
// negado fail-closed ([ErrPayloadAccessDenied]).
const DefaultSovereignContentScope = "sovereign:content:read"

// WithContentOpener liga o [agentruntime.ContentOpener] por-titular (AOS-093) e o [Accessor]
// AUTORIZADO com que o motor DECIFRA o conteúdo selado de um run na reconstrução do lado do
// LEITOR. É o análogo de [WithPayloadResolver] para a cifra por-titular: o accessor tem de deter
// [DefaultSovereignContentScope] — um accessor sem essa autoridade é negado ([ErrPayloadAccessDenied]),
// provando que o conteúdo está atrás do gate soberano. Sem esta opção, um evento com conteúdo selado
// encontrado na reconstrução ⇒ [ErrPayloadAccessDenied] (fail-closed: o leitor obtém a negação, nunca
// ciphertext interpretado como claro).
//
// O opener REAL é o cifrador por-titular do nó (o mesmo [agentruntime.ContentCipher] cablado no
// composition root que o capturer/step-ledger usam para SELAR) — reutiliza [audit.OpenContent], sem
// cripto nova. Depois do crypto-shredding do titular, o open falha (audit.ErrDecrypt), propagado.
func WithContentOpener(opener agentruntime.ContentOpener, accessor Accessor) EngineOption {
	return func(e *ReplayEngine) {
		e.contentOpener = opener
		e.contentAccessor = accessor
	}
}

// resolveSealed DECIFRA o conteúdo selado por-titular de um evento de captura, IMPONDO o gate:
//   - sem [agentruntime.ContentOpener] ligado, ou accessor sem o escopo soberano ⇒
//     [ErrPayloadAccessDenied] (fail-closed — o leitor não autorizado nunca obtém o claro);
//   - o open falha (KEK destruída no crypto-shredding, ou blob adulterado) ⇒ o erro do opener
//     (audit.ErrDecrypt) propaga-se TAL-QUAL — o shred aguenta o replay;
//   - sucesso ⇒ o corpo interno ([sealedContent]: resposta do modelo + resultados de tools) é
//     re-hidratado nos campos em claro do payload e o envelope selado é limpo.
//
// O plaintext decifrado fica APENAS na estrutura devolvida ao chamador autorizado — nunca em log/span/erro.
func (e *ReplayEngine) resolveSealed(ctx context.Context, p capturePayload) (capturePayload, error) {
	if e.contentOpener == nil {
		return capturePayload{}, ErrPayloadAccessDenied
	}
	if !e.contentAccessor.hasScope(e.contentReadScope) {
		return capturePayload{}, ErrPayloadAccessDenied
	}
	plain, err := e.contentOpener.OpenContent(ctx, p.SealedSubject, p.SealedContent)
	if err != nil {
		// Fail-closed: depois do shred, audit.OpenContent devolve ErrDecrypt — mesmo o leitor
		// autorizado obtém a negação. Nunca se cai para ciphertext-como-claro.
		return capturePayload{}, err
	}
	var sc sealedContent
	if uerr := json.Unmarshal(plain, &sc); uerr != nil {
		return capturePayload{}, ErrCorruptCapture
	}
	p.Response = sc.Response
	p.ToolResults = sc.ToolResults
	p.LeadingCorrection = sc.LeadingCorrection // AOS-218: correcção de steer selada por-titular
	p.SealedContent = nil
	p.SealedSubject = ""
	return p, nil
}

// ReconstructedTurn é o conteúdo DECIFRADO de um turno devolvido ao leitor autorizado por
// [ReplayEngine.Reconstruct]: a resposta do modelo REGISTADA e os resultados de tools, mais os
// metadados de localização (turno/step) e o relógio de captura. É o material que um TERCEIRO
// autorizado por soberania inspecciona — obtido do log, nunca ao vivo.
type ReconstructedTurn struct {
	Turn   int
	StepID string
	// Response é a resposta do modelo re-hidratada (texto + tool calls + uso). Em claro APENAS
	// quando o leitor está autorizado (o conteúdo selado foi decifrado atrás do gate soberano).
	Response agentruntime.ModelResponse
	// ToolResults são os resultados de tools REGISTADOS (untrusted), decifrados quando selados.
	ToolResults []agentruntime.Tainted
	// ObservedAtUnixNano é o carimbo de captura lido do log (reportado, nunca re-injectado).
	ObservedAtUnixNano int64
}

// Reconstruct reconstrói — do lado do LEITOR — o conteúdo não-determinístico decifrado de um run
// selado, IMPONDO o gate soberano do opener por-titular. É INSPECÇÃO (não verificação de
// fidelidade): ao contrário de [ReplayEngine.Replay], NÃO re-materializa prompts nem exige a
// [TrajectorySpec] — devolve as capturas decifradas por turno. Resolve mode 3 (AOS-079) e o
// envelope selado (AOS-093), ambos atrás dos seus gates.
//
// Fail-closed: conteúdo selado sem opener/accessor autorizado ⇒ [ErrPayloadAccessDenied] (o leitor
// obtém a negação, NUNCA o claro); depois do crypto-shredding ⇒ o erro de decifração (audit.ErrDecrypt)
// propaga-se — o replay não ressuscita o que a erasure apagou. Stream inexistente / sem capturas ⇒
// [ErrNoTrajectory].
//
// NUNCA chama um modelo ao vivo, NUNCA despacha uma tool, NUNCA escreve no Event Store — o motor só
// detém um [EventReader].
func (e *ReplayEngine) Reconstruct(ctx context.Context, runID string) ([]ReconstructedTurn, error) {
	if runID == "" {
		return nil, ErrEmptyRunID
	}
	events, err := e.reader.Read(ctx, runID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil, ErrNoTrajectory
		}
		return nil, err
	}
	caps := make(map[int]capturePayload)
	stepByTurn := make(map[int]string)
	var order []int
	for _, ev := range events {
		switch ev.Type {
		case agentruntime.EventTypeTurnRecorded:
			// O step_id canónico do turno vem do turn.recorded (AOS-013), quando presente.
			var trp turnRecordedPayload
			if uerr := json.Unmarshal(ev.Payload, &trp); uerr == nil {
				stepByTurn[trp.Turn] = ev.StepID
			}
		case EventTypeCaptured:
			var p capturePayload
			if uerr := json.Unmarshal(ev.Payload, &p); uerr != nil {
				return nil, ErrCorruptCapture
			}
			// MODE 3 (AOS-079): referência-só ⇒ resolve o payload completo no PayloadStore (com o
			// accessor de leitura de payloads). Fail-closed em qualquer falha.
			if p.PayloadRef != "" {
				resolved, rerr := e.resolvePayload(ctx, p)
				if rerr != nil {
					return nil, rerr
				}
				p = resolved
			}
			// AOS-093: conteúdo cifrado por-titular ⇒ decifra atrás do gate soberano (fail-closed).
			if p.SealedContent != nil {
				resolved, serr := e.resolveSealed(ctx, p)
				if serr != nil {
					return nil, serr
				}
				p = resolved
			}
			if _, seen := caps[p.Turn]; !seen {
				order = append(order, p.Turn)
			}
			// Sem turn.recorded, deriva o step do envelope da captura ("cap-<step>" ⇒ "<step>").
			if _, ok := stepByTurn[p.Turn]; !ok {
				stepByTurn[p.Turn] = strings.TrimPrefix(ev.StepID, captureStepPrefix)
			}
			caps[p.Turn] = p
		}
	}
	if len(caps) == 0 {
		return nil, ErrNoTrajectory
	}
	sort.Ints(order)
	out := make([]ReconstructedTurn, 0, len(order))
	for _, turn := range order {
		capt := caps[turn]
		rt := ReconstructedTurn{
			Turn:               turn,
			StepID:             stepByTurn[turn],
			Response:           capt.Response.decode(),
			ObservedAtUnixNano: capt.ObservedAtUnixNano,
		}
		for _, tr := range capt.ToolResults {
			// Só o VALOR interessa à leitura soberana de conteúdo; o erro e a negação
			// sanitizada são metadados de reconstrução do tail (ver o motor de replay).
			value, _, _ := tr.decode()
			rt.ToolResults = append(rt.ToolResults, value)
		}
		out = append(out, rt)
	}
	return out, nil
}
