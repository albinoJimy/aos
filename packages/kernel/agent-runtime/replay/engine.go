package replay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// Atributos do marcador de replay/eval (ADR-010, tecnica/08). Ligam-se ao trace
// original por [agentruntime.AttrRunID] (aos.run_id).
const (
	// OpReplay — nome de operação do span de replay.
	OpReplay = "replay"
	// AttrReplayFromStep — aos.replay.from_step (step_id de retoma; "" = do início).
	AttrReplayFromStep = "aos.replay.from_step"
	// AttrReplayFidelity — aos.replay.fidelity (fracção de turnos verificados cujo
	// prompt_hash re-materializado coincidiu; 1.0 = fidelidade de 100%).
	AttrReplayFidelity = "aos.replay.fidelity"
	// AttrReplayDiverged — aos.replay.diverged (bool: houve divergência localizada).
	AttrReplayDiverged = "aos.replay.diverged"
	// AttrReplayTurns — aos.replay.turns (nº de turnos replayados no segmento).
	AttrReplayTurns = "aos.replay.turns"
	// AttrEvalResult — gen_ai.evaluation.result ("pass" | "fail"): resultado do eval
	// de replay para o eval-driven development e o RCA (ADR-010).
	AttrEvalResult = "gen_ai.evaluation.result"
)

// EventReader é o ÚNICO acesso ao Event Store de que o [ReplayEngine] depende:
// apenas Read. É o que torna o zero-efeitos ESTRUTURAL — não há Append, não há
// modelo, não há Reference Monitor, não há registo de tools. *eventstore.Store
// satisfaz esta interface.
type EventReader interface {
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// TrajectorySpec são os inputs DETERMINÍSTICOS re-fornecidos ao replay para
// re-materializar o prompt: o system prompt, o tool set CONGELADO (mesma ordem), o
// objectivo e o memory_context que semeiam o tail. São código/configuração — NÃO
// vivem no log (só os seus hashes) e é PRECISAMENTE alterá-los que simula a
// "evolução de código" cujo efeito o replay detecta por divergência de hash.
type TrajectorySpec struct {
	System        string
	Tools         []agentruntime.ToolSpec
	Objective     string
	MemoryContext []byte
	// Model é a configuração de modelo ESPERADA (model_id/params/seed) — os inputs
	// não-determinísticos que o manifesto pina (ADR-010) mas que NÃO entram nos bytes
	// materializados do prompt. Se ModelID != "", o replay compara-a com a gravada no
	// manifesto e localiza divergência de modelo (Reason="model") — drift que é
	// INVISÍVEL ao prompt_hash (troca de modelo/params/seed sem mexer no prompt).
	// Vazio (ModelID == "") ⇒ sem verificação de modelo (retrocompatível).
	Model agentruntime.ModelConfig
	// AssemblyVersion é a versão do assembler ESPERADA. Se != "", o replay compara-a
	// com a gravada no manifesto (Reason="assembly_version") — uma subida da versão do
	// código de montagem sem alterar os bytes do prompt é, tal como o modelo, invisível
	// ao prompt_hash. Vazio ⇒ sem verificação (retrocompatível).
	AssemblyVersion string
}

// Options parametriza um replay.
type Options struct {
	// Spec são os inputs DETERMINÍSTICOS re-fornecidos para re-materializar o prompt
	// (system, tool set congelado, objectivo, memory_context). Ver [TrajectorySpec].
	Spec TrajectorySpec
	// FromStepID activa o resume-from-step: o replay reconstrói o estado (tail) dos
	// turnos anteriores a partir do log (zero efeitos) e começa a VERIFICAR/emitir a
	// partir do turno cujo step_id é este. Vazio ⇒ replay desde o início.
	//
	// Reconstrução por RE-FOLD INTEGRAL (não por cursor de AOS-015). O estado no ponto
	// de retoma é reconstruído dobrando o log DESDE O TURNO 1 (as respostas do modelo e
	// os resultados de tools REGISTADOS), não consumindo o Cursor/checkpoint de AOS-015.
	// É deliberado e robusto — o re-fold não depende de nenhum checkpoint ter sido
	// escrito — mas significa que FromStepID é apenas uma JANELA DE VERIFICAÇÃO: os
	// turnos ANTERIORES ao segmento são dobrados SEM verificação de prompt_hash. Logo,
	// uma divergência de prompt num turno pré-segmento (Spec evoluído) NÃO é detectada
	// em modo resume — o estado reconstruído reflecte as respostas REGISTADAS, que são
	// determinísticas por construção. Para verificar a trajectória inteira, corra o
	// replay sem FromStepID.
	FromStepID string
	// StepIdentity, se fornecido, VERIFICA que a derivação de step_id (p.ex.
	// [durable.StepSequencer] de AOS-014) reproduz o step_id gravado em cada turno —
	// uma divergência de sequência de passos é detectada e localizada. Default: sem
	// verificação de sequência (usa os step_ids do log tal-e-qual).
	StepIdentity agentruntime.StepIdentity
}

// ReplayDivergence localiza o passo EXACTO onde o replay diverge da execução
// original — o prompt re-materializado não bate com o gravado (ou a sequência de
// passos difere). É o sinal de "replay infiel após evolução de código".
type ReplayDivergence struct {
	// StepID e Turn localizam o passo divergente.
	StepID string
	Turn   int
	// ExpectedHash é o prompt_hash GRAVADO na execução original (manifesto).
	ExpectedHash string
	// ActualHash é o prompt_hash RE-MATERIALIZADO no replay.
	ActualHash string
	// Reason descreve a natureza da divergência: "prompt_hash" (os bytes materializados
	// do prompt divergem), "model" (model_id/params/seed pinados divergem — invisível
	// ao prompt_hash), "assembly_version" (a versão do assembler diverge) ou
	// "step_id sequence" (a derivação de step_id não reproduz o gravado).
	Reason string
}

// ReplayedTurn é o registo de um turno reproduzido no segmento de replay.
type ReplayedTurn struct {
	Turn   int
	StepID string
	// IncomingStateHash é o hash do tail ANTES da chamada ao modelo deste turno — o
	// fingerprint do ESTADO neste passo (usado para provar a equivalência do resume).
	IncomingStateHash string
	// PromptHash é o hash re-materializado; RecordedPromptHash é o gravado.
	PromptHash         string
	RecordedPromptHash string
	// Seed é lido do manifesto (model.seed); ObservedAtUnixNano é o relógio lido da
	// captura. Ambos vêm do log — nunca ao vivo.
	Seed               int64
	ObservedAtUnixNano int64
	// Response é a resposta do modelo REGISTADA e devolvida pelo cliente de replay.
	Response agentruntime.ModelResponse
	// Matched indica se o prompt_hash re-materializado coincidiu com o gravado.
	Matched bool
}

// ReplayResult é o desfecho de um replay.
type ReplayResult struct {
	RunID             string
	ResumedFromStepID string
	// Steps são os turnos do segmento replayado (do ponto de retoma até ao fim ou à
	// divergência).
	Steps []ReplayedTurn
	// FinalText/Terminated espelham o desfecho reconstruído.
	FinalText  string
	Terminated bool
	// FinalStateHash é o fingerprint do ESTADO final reconstruído (hash do tail). É
	// idêntico entre um replay completo e um resume-from-step do mesmo run — a prova
	// de que o resume produz o mesmo estado.
	FinalStateHash string
	// Fidelity é a fracção de turnos verificados cujo hash coincidiu (1.0 = 100%).
	Fidelity float64
	// AnchorsVerified nomeia as comparações NÃO-prompt que CORRERAM de facto neste
	// replay: "model", "assembly_version", "step_id". Ver [activeAnchors].
	//
	// # PORQUE ISTO EXISTE
	//
	// As três são OPT-IN — só correm quando o chamador re-fornece o campo esperado
	// ([TrajectorySpec].Model.ModelID, .AssemblyVersion, [Options].StepIdentity). Uma
	// spec que os omita desliga-as EM SILÊNCIO, e até aqui nada no resultado o dizia:
	// medido a 2026-08-28, o MESMO log com Model e AssemblyVersion omitidos devolvia
	// `Fidelity=1, Divergence=nil` — indistinguível de uma verificação completa.
	//
	// O opt-in é deliberado e mantém-se (retro-compatibilidade). O que muda é que
	// deixa de ser invisível: quem consome o resultado passa a poder distinguir «não
	// divergiu» de «não foi comparado».
	AnchorsVerified []string
	// Divergence é não-nil se uma divergência foi detectada e localizada.
	Divergence *ReplayDivergence
}

// ReplayEngine é o motor de replay determinístico (AOS-016). Detém APENAS um
// [EventReader] (só Read) e um [agentruntime.Tracer] (observabilidade). Não tem
// modelo, nem Reference Monitor, nem Append — o zero-efeitos é ESTRUTURAL.
//
// Sem estado mutável ⇒ seguro para uso concorrente e -race limpo.
type ReplayEngine struct {
	reader EventReader
	tracer agentruntime.Tracer
	// payloadStore/accessor resolvem as referências de content-capture mode 3
	// (AOS-079): quando um evento "replay.captured" é referência-só, o motor lê o
	// payload completo do store externo IMPONDO o IAM (accessor autorizado). É só um
	// LEITOR de payloads — não é caminho de efeito ao vivo (o store devolve bytes
	// gravados, tal como o EventReader devolve eventos gravados). nil ⇒ só resolve
	// capturas inline (AOS-016); um evento mode 3 sem store ⇒ fail-closed.
	payloadStore PayloadStore
	accessor     Accessor
	// contentOpener/contentAccessor resolvem o CONTEÚDO SELADO por-titular (AOS-093/AOS-214):
	// quando um evento de captura carrega [capturePayload.SealedContent], o motor DECIFRA-o via
	// o [agentruntime.ContentOpener] IMPONDO o gate (contentAccessor tem de deter
	// contentReadScope). É o lado do LEITOR da cifra por-titular — o opener/accessor só são
	// compostos ([WithContentOpener]) DEPOIS de o gate soberano do nó autorizar. nil ⇒ conteúdo
	// selado encontrado ⇒ [ErrPayloadAccessDenied] (fail-closed, nunca claro).
	contentOpener   agentruntime.ContentOpener
	contentAccessor Accessor
	// contentReadScope é o escopo exigido no contentAccessor (default [DefaultSovereignContentScope]).
	contentReadScope string
}

// EngineOption configura o [ReplayEngine].
type EngineOption func(*ReplayEngine)

// WithTracer injecta a porta de observabilidade (default [agentruntime.NoopTracer]).
func WithTracer(t agentruntime.Tracer) EngineOption {
	return func(e *ReplayEngine) { e.tracer = t }
}

// WithPayloadResolver liga o [PayloadStore] externo e o [Accessor] AUTORIZADO com que
// o motor resolve as referências de content-capture mode 3 (AOS-079). O accessor tem
// de deter o escopo de LEITURA do store — um accessor sem autoridade é negado pelo
// store ([ErrPayloadAccessDenied]), provando que o payload está atrás do seu IAM
// próprio, separado do escritor do Event Store. Sem esta opção o motor só resolve
// capturas inline; um evento mode 3 encontrado sem resolver ⇒ [ErrPayloadStoreRequired].
func WithPayloadResolver(store PayloadStore, accessor Accessor) EngineOption {
	return func(e *ReplayEngine) {
		e.payloadStore = store
		e.accessor = accessor
	}
}

// NewEngine constrói o motor sobre um leitor do Event Store. reader é obrigatório.
func NewEngine(reader EventReader, opts ...EngineOption) (*ReplayEngine, error) {
	if reader == nil {
		return nil, ErrNilStore
	}
	e := &ReplayEngine{reader: reader, tracer: agentruntime.NoopTracer{}, contentReadScope: DefaultSovereignContentScope}
	for _, o := range opts {
		o(e)
	}
	if e.tracer == nil {
		e.tracer = agentruntime.NoopTracer{}
	}
	return e, nil
}

// turnRecordedPayload é a projecção mínima do payload "turn.recorded" (AOS-013) de
// que o replay precisa: o nº do turno e o manifesto (prompt_hash, model.seed…). As
// tags JSON casam com o [agentruntime] turnPayload/Manifest.
type turnRecordedPayload struct {
	Turn     int                   `json:"turn"`
	Manifest agentruntime.Manifest `json:"manifest"`
}

// trajectory é o material reconstruído do log de um run, indexado por turno.
type trajectory struct {
	turns      []int
	manifest   map[int]agentruntime.Manifest
	capture    map[int]capturePayload
	stepByTurn map[int]string
}

// load relê o stream do run e indexa turn.recorded + replay.captured por turno.
func (e *ReplayEngine) load(ctx context.Context, runID string) (trajectory, error) {
	events, err := e.reader.Read(ctx, runID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return trajectory{}, ErrNoTrajectory
		}
		return trajectory{}, err
	}
	tr := trajectory{
		manifest:   make(map[int]agentruntime.Manifest),
		capture:    make(map[int]capturePayload),
		stepByTurn: make(map[int]string),
	}
	seen := make(map[int]bool)
	for _, ev := range events {
		switch ev.Type {
		case agentruntime.EventTypeTurnRecorded:
			var p turnRecordedPayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return trajectory{}, err
			}
			tr.manifest[p.Turn] = p.Manifest
			tr.stepByTurn[p.Turn] = ev.StepID
			if !seen[p.Turn] {
				seen[p.Turn] = true
				tr.turns = append(tr.turns, p.Turn)
			}
		case EventTypeCaptured:
			var p capturePayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return trajectory{}, ErrCorruptCapture
			}
			// MODE 3 (AOS-079): evento referência-só ⇒ resolver o payload completo no
			// PayloadStore externo (impondo o IAM). Fail-closed em qualquer falha.
			if p.PayloadRef != "" {
				resolved, err := e.resolvePayload(ctx, p)
				if err != nil {
					return trajectory{}, err
				}
				p = resolved
			}
			// AOS-214: conteúdo cifrado por-titular (AOS-093) ⇒ decifra do lado do LEITOR atrás do
			// gate soberano do opener. Fail-closed sem opener/accessor autorizado
			// ([ErrPayloadAccessDenied]); depois do crypto-shredding, o open falha (audit.ErrDecrypt),
			// propagado — o shred aguenta o replay.
			if p.SealedContent != nil {
				resolved, err := e.resolveSealed(ctx, p)
				if err != nil {
					return trajectory{}, err
				}
				p = resolved
			}
			tr.capture[p.Turn] = p
		}
	}
	if len(tr.turns) == 0 {
		return trajectory{}, ErrNoTrajectory
	}
	sort.Ints(tr.turns)
	return tr, nil
}

// resolvePayload resolve um evento de captura mode 3 (referência-só) para o seu
// payload completo, lendo-o do [PayloadStore] externo com o accessor AUTORIZADO. É
// fail-closed:
//   - sem PayloadStore ligado ⇒ [ErrPayloadStoreRequired] (a ref é irrecuperável);
//   - acesso negado pelo IAM do store ⇒ [ErrPayloadAccessDenied] (propagado);
//   - payload em falta (não retido) ⇒ [ErrIncompleteCapture] (captura incompleta);
//   - conteúdo adulterado (hash != ref) ⇒ [ErrPayloadIntegrity].
//
// O payload resolvido reidrata Response/ToolResults; schema/turn/relógio do evento
// do ES (metadados pequenos) são preservados.
func (e *ReplayEngine) resolvePayload(ctx context.Context, ref capturePayload) (capturePayload, error) {
	if e.payloadStore == nil {
		return capturePayload{}, ErrPayloadStoreRequired
	}
	blob, err := e.payloadStore.Get(ctx, PayloadRef{Digest: ref.PayloadRef}, e.accessor)
	if err != nil {
		return capturePayload{}, asIncompleteCapture(err)
	}
	var full capturePayload
	if err := json.Unmarshal(blob, &full); err != nil {
		return capturePayload{}, ErrCorruptCapture
	}
	// Fail-closed sobre o blob externo (defesa-em-profundidade, AOS-079): embora a
	// PayloadRef seja content-addressed (o Get já rejeita bytes adulterados), NÃO
	// confiamos no conteúdo interno para (a) indexar o turno nem (b) interpretar o
	// schema. Um schema não suportado ou um Turn interno divergente do envelope do ES
	// é captura corrupta, não um silêncio.
	//
	// CORRIGIDO a 2026-08-28: esta nota dizia que a ref «está ancorada no ES
	// tamper-evident (AOS-072)», e a premissa é FALSA. AOS-072 é «Audit tamper-evident
	// (hash-chain + WORM + assinatura)» e vive em packages/platform/audit — um store
	// SEPARADO, alimentado pelas mediações do RM e pelo egress, que é o que a selagem
	// diária ancora. O Event Store onde a captura vive é append-only na API e tem um
	// crc32 por registo no WAL, que é detecção de ERRO e não um MAC: recalcula-se.
	// A verificação abaixo estava certa; a razão declarada é que mandava quem a lesse
	// concluir que podia confiar noutro sítio qualquer.
	if full.SchemaVersion != captureSchemaVersion {
		return capturePayload{}, ErrCorruptCapture
	}
	if ref.Turn != 0 && full.Turn != 0 && full.Turn != ref.Turn {
		return capturePayload{}, ErrCorruptCapture
	}
	// Preserva os metadados do evento do ES (o payload externo tem os seus, mas a
	// fonte de verdade do envelope é o evento) e limpa a ref (já resolvida).
	full.PayloadRef = ""
	if full.Turn == 0 {
		full.Turn = ref.Turn
	}
	if full.ObservedAtUnixNano == 0 {
		full.ObservedAtUnixNano = ref.ObservedAtUnixNano
	}
	return full, nil
}

// admit é o GATE DE ADMISSÃO de replay (AOS-079, CA4): "fidelidade é condição, não
// opção". Verifica ANTES de reproduzir que TODOS os turnos da trajectória têm a
// captura de não-determinismo completa e um manifesto utilizável. Se algum turno não
// foi capturado (evento "replay.captured" em falta) ou o manifesto não tem
// prompt_hash, o replay é INADMISSÍVEL ([ErrIncompleteCapture]) — recusa fail-closed
// em vez de produzir silenciosamente uma reprodução de baixa fidelidade.
//
// As referências de mode 3 já foram resolvidas em [load] (uma ref não resolúvel/perda
// de payload falhou aí); aqui confirma-se que cada turno tem o conteúdo presente.
func admit(tr trajectory) error {
	for _, turn := range tr.turns {
		capt, ok := tr.capture[turn]
		if !ok {
			return ErrIncompleteCapture
		}
		if tr.manifest[turn].PromptHash == "" {
			return ErrIncompleteCapture
		}
		if err := capturaCompleta(capt); err != nil {
			return err
		}
	}
	return nil
}

// capturaCompleta recusa uma captura de turno em que a resposta registada tem MAIS tool calls
// do que resultados capturados (AOS-289).
//
// # DE ONDE VEM A ASSIMETRIA
//
// O loop acumula um resultado por iteração e, ao ESCALAR, sai do laço tendo capturado `j+1`
// resultados — mas grava `Response` INTEIRA, com as M tool calls. O ramo de escalada é o ÚNICO
// produtor desta divergência: nenhuma outra saída do laço a produz (a negação do RM e o erro de
// tool acumulam o resultado ANTES de qualquer ramo, e as fronteiras de orçamento, disjuntor e
// pausa correm depois da captura).
//
// # PORQUE ISTO TEM DE RECUSAR
//
// O motor itera sobre as M tool calls da resposta e o dispatcher devolve um resultado untrusted
// VAZIO para os índices sem registo — um segmento que o run original nunca produziu, dobrado no
// tail. A auditoria mediu o efeito: `Fidelity=1`, `Divergence=nil`, e um `FinalStateHash`
// diferente do original. Uma fidelidade de 1,0 sobre uma trajectória fabricada é pior do que uma
// recusa: é uma prova a afirmar o que não observou.
//
// # O SENTIDO DA COMPARAÇÃO É DELIBERADO
//
// Recusa-se `len(ToolCalls) > len(ToolResults)` — resultados a MENOS do que chamadas, que é o
// fabrico. O caso inverso (mais resultados do que chamadas) não fabrica nada no tail: o motor
// itera sobre as chamadas e nunca alcança o excedente. Recusá-lo seria alargar a guarda para
// além do defeito medido, e não há caminho no loop que o produza.
func capturaCompleta(capt capturePayload) error {
	if len(capt.Response.ToolCalls) > len(capt.ToolResults) {
		return fmt.Errorf("%w: turno %d tem %d tool calls na resposta e %d resultados capturados (AOS-289)",
			ErrIncompleteCapture, capt.Turn, len(capt.Response.ToolCalls), len(capt.ToolResults))
	}
	return nil
}

// Replay reconstrói a trajectória do run a partir do Event Store. Lê TODOS os
// inputs não-determinísticos do log (resposta do modelo e resultados de tools da
// captura; seed do manifesto; relógio do carimbo de captura), re-materializa o
// prompt de cada turno com o MESMO PromptAssembler e compara o prompt_hash com o
// gravado. Uma divergência é localizada no passo exacto (e o replay pára aí). O
// FromStepID de [Options] activa o resume-from-step.
//
// NUNCA chama um modelo ao vivo, NUNCA despacha uma tool ao vivo, NUNCA escreve no
// Event Store — o motor não detém sequer caminho para esses efeitos.
func (e *ReplayEngine) Replay(ctx context.Context, runID string, opts Options) (ReplayResult, error) {
	if runID == "" {
		return ReplayResult{}, ErrEmptyRunID
	}
	tr, err := e.load(ctx, runID)
	if err != nil {
		return ReplayResult{}, err
	}

	// GATE DE ADMISSÃO (AOS-079, CA4): recusa fail-closed uma trajectória com captura
	// incompleta ANTES de reproduzir — fidelidade é condição, não opção.
	if err := admit(tr); err != nil {
		return ReplayResult{}, err
	}

	// Resolve o turno de retoma (resume-from-step). 0 ⇒ desde o início.
	fromTurn := 0
	if opts.FromStepID != "" {
		fromTurn = -1
		for _, t := range tr.turns {
			if tr.stepByTurn[t] == opts.FromStepID {
				fromTurn = t
				break
			}
		}
		if fromTurn < 0 {
			return ReplayResult{}, ErrStepNotFound
		}
	}

	// Constrói o cliente de modelo e o dispatcher de replay — AMBOS alimentados
	// EXCLUSIVAMENTE pela captura do log. Não há aqui nenhum modelo/tool ao vivo.
	modelClient := newReplayModelClient(tr.capture)
	dispatcher := newReplayDispatcher(tr.capture)

	// O MESMO assembler que o loop usou (mesmo system + tool set congelado).
	asm := agentruntime.NewPromptAssembler(opts.Spec.System, opts.Spec.Tools)

	res := ReplayResult{
		RunID:             runID,
		ResumedFromStepID: opts.FromStepID,
		Fidelity:          1.0,
		AnchorsVerified:   activeAnchors(opts.Spec, opts.StepIdentity),
	}

	// Semeia o tail EXACTAMENTE como o loop (memory_context + objectivo).
	tail := seedTail(opts.Spec)

	matched, verified := 0, 0
	for _, turn := range tr.turns {
		stepID := tr.stepByTurn[turn]
		capt, ok := tr.capture[turn]
		if !ok {
			return ReplayResult{}, ErrMissingCapture
		}
		manifest := tr.manifest[turn]
		inSegment := fromTurn == 0 || turn >= fromTurn

		// (0) CORRECÇÃO DE STEER (AOS-218/AOS-023): o turno ANTERIOR injectou uma correcção
		// TRUSTED no tail (leading correction deste turno), capturada em
		// [capturePayload.LeadingCorrection]. Dobra-se ANTES de re-materializar o prompt —
		// EXACTAMENTE onde o loop base a acrescentou (fim do turno anterior, antes do
		// assemble deste) — usando a MESMA construção ([agentruntime.TailFromCorrection]).
		// Sem ela o tail reconstruído omitiria o segmento e o prompt_hash divergiria
		// espuriamente num run steerado. Aplica-se a TODO o turno (segmento verificado OU
		// pré-segmento dobrado no resume), pelo que uma correcção pré-segmento também entra
		// no estado reconstruído — o que faz o resume-from-step convergir com o replay
		// completo. Runs sem steer têm LeadingCorrection vazia ⇒ tail byte-idêntico.
		if len(capt.LeadingCorrection) > 0 {
			tail = append(tail, agentruntime.TailFromCorrection(capt.LeadingCorrection))
		}

		// (1) RE-MATERIALIZAR o prompt do turno com o tail corrente.
		incoming := tailHash(tail)
		view := asm.Assemble(turn, tail)

		// (2) "CHAMAR" o modelo de replay — devolve a resposta REGISTADA.
		resp, cerr := modelClient.Call(ctx, view)
		if cerr != nil {
			return ReplayResult{}, cerr
		}

		if inSegment {
			verified++
			rt := ReplayedTurn{
				Turn:               turn,
				StepID:             stepID,
				IncomingStateHash:  incoming,
				PromptHash:         view.PromptHash,
				RecordedPromptHash: manifest.PromptHash,
				Seed:               manifest.Model.Seed,
				ObservedAtUnixNano: capt.ObservedAtUnixNano,
				Response:           resp,
			}
			if div := e.detectDivergence(runID, turn, stepID, view.PromptHash, manifest, opts.Spec, opts.StepIdentity); div != nil {
				res.Steps = append(res.Steps, rt) // rt.Matched fica false
				res.Divergence = div
				res.Fidelity = fidelity(matched, verified)
				e.emitMarker(ctx, res)
				return res, nil // divergência LOCALIZADA — pára no passo exacto.
			}
			rt.Matched = true
			matched++
			res.Steps = append(res.Steps, rt)
		}

		// (3) DOBRAR o resultado do turno no tail — histórico do modelo + resultado de
		// cada tool (do dispatcher de replay, REGISTADO). Idêntico ao loop base, quer
		// o turno esteja no segmento verificado, quer seja um turno anterior dobrado
		// para reconstruir o estado do resume.
		if resp.Text != "" {
			tail = append(tail, agentruntime.TailFromModelText(resp.Text))
		}
		for idx := range resp.ToolCalls {
			// A negação REGISTADA entra na reconstrução: o loop materializa-a no tail,
			// logo omiti-la divergiria o prompt_hash de qualquer run com uma negação.
			value, toolErr, denial := dispatcher.Dispatch(turn, idx)
			tail = append(tail, agentruntime.TailFromToolResultDenied(value, toolErr, denial))
		}

		// (4) TERMINAÇÃO — igual ao loop: resposta final ou sem tool calls.
		if resp.Final || len(resp.ToolCalls) == 0 {
			res.Terminated = true
			res.FinalText = resp.Text
			break
		}
	}

	res.FinalStateHash = tailHash(tail)
	res.Fidelity = fidelity(matched, verified)
	e.emitMarker(ctx, res)
	return res, nil
}

// detectDivergence localiza a divergência do turno, por ordem de fundamentalidade:
//  1. prompt_hash — os bytes materializados do prompt divergem do gravado;
//  2. model — model_id/params/seed re-fornecidos divergem dos pinados no manifesto
//     (drift INVISÍVEL ao prompt_hash: o modelo não entra nos bytes do prompt);
//  3. assembly_version — a versão do assembler re-fornecida diverge da gravada;
//  4. step_id sequence — a derivação de step_id não reproduz o gravado.
//
// As comparações de modelo e de assembly são OPT-IN: só correm quando o chamador
// re-fornece o campo esperado na [TrajectorySpec] (Model.ModelID != "" /
// AssemblyVersion != ""). Devolve a primeira divergência encontrada ou nil.
func (e *ReplayEngine) detectDivergence(runID string, turn int, stepID, actual string, manifest agentruntime.Manifest, spec TrajectorySpec, ident agentruntime.StepIdentity) *ReplayDivergence {
	// (1) prompt_hash — âncora primária (bytes materializados do prompt).
	if actual != manifest.PromptHash {
		return &ReplayDivergence{StepID: stepID, Turn: turn, ExpectedHash: manifest.PromptHash, ActualHash: actual, Reason: "prompt_hash"}
	}
	// (2) model — model_id/params/seed pinados. INVISÍVEL ao prompt_hash: uma troca de
	// modelo/params/seed que não mexa nos bytes materializados passaria como fidelidade
	// 1.0 se não fosse comparada aqui explicitamente contra o manifesto (ADR-010).
	if spec.Model.ModelID != "" {
		if exp, act := canonicalModel(manifest.Model.ModelID, manifest.Model.Params, manifest.Model.Seed), canonicalModel(spec.Model.ModelID, spec.Model.Params, spec.Model.Seed); exp != act {
			return &ReplayDivergence{StepID: stepID, Turn: turn, ExpectedHash: exp, ActualHash: act, Reason: "model"}
		}
	}
	// (3) assembly_version — a versão do CÓDIGO de montagem (também invisível ao
	// prompt_hash quando o layout materializado não muda entre versões).
	if spec.AssemblyVersion != "" && spec.AssemblyVersion != manifest.AssemblyVersion {
		return &ReplayDivergence{StepID: stepID, Turn: turn, ExpectedHash: manifest.AssemblyVersion, ActualHash: spec.AssemblyVersion, Reason: "assembly_version"}
	}
	// (4) step_id sequence — a derivação de step_id não reproduz o gravado.
	if ident != nil {
		if derived := ident.StepID(runID, turn); derived != stepID {
			return &ReplayDivergence{StepID: stepID, Turn: turn, ExpectedHash: stepID, ActualHash: derived, Reason: "step_id sequence"}
		}
	}
	return nil
}

// activeAnchors nomeia as comparações não-prompt que [ReplayEngine.detectDivergence]
// vai de facto correr, dadas a spec e a identidade de passo.
//
// As condições AQUI têm de espelhar EXACTAMENTE as de [ReplayEngine.detectDivergence].
// Duas cópias de uma condição divergem, e a que mente seria esta — um relatório a dizer
// que comparou o que ninguém comparou é pior do que relatório nenhum. É por isso que
// [TestAnchorsVerifiedEspelhaOQueEComparado] amarra as duas: por cada âncora que esta
// função declara activa, o teste força a divergência correspondente e exige que ela
// saia — se uma condição mudar num sítio e não no outro, fica vermelho.
func activeAnchors(spec TrajectorySpec, ident agentruntime.StepIdentity) []string {
	var out []string
	if spec.Model.ModelID != "" {
		out = append(out, "model")
	}
	if spec.AssemblyVersion != "" {
		out = append(out, "assembly_version")
	}
	if ident != nil {
		out = append(out, "step_id")
	}
	return out
}

// canonicalModel devolve uma representação estável e comparável da configuração de
// modelo (model_id + seed + params por chave ordenada). Determinística — mapas não
// têm ordem, por isso as chaves são ordenadas antes de serializar.
func canonicalModel(id string, params map[string]string, seed int64) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("model_id=")
	b.WriteString(id)
	b.WriteString(";seed=")
	b.WriteString(strconv.FormatInt(seed, 10))
	for _, k := range keys {
		b.WriteString(";")
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(params[k])
	}
	return b.String()
}

// emitMarker emite o span de replay/eval ligado ao trace original (ADR-010).
func (e *ReplayEngine) emitMarker(ctx context.Context, res ReplayResult) {
	_, span := e.tracer.StartSpan(ctx, OpReplay)
	span.SetAttribute(agentruntime.AttrOperationName, OpReplay)
	span.SetAttribute(agentruntime.AttrRunID, res.RunID)
	span.SetAttribute(AttrReplayFromStep, res.ResumedFromStepID)
	span.SetAttribute(AttrReplayFidelity, res.Fidelity)
	span.SetAttribute(AttrReplayTurns, len(res.Steps))
	diverged := res.Divergence != nil
	span.SetAttribute(AttrReplayDiverged, diverged)
	if diverged {
		span.SetAttribute(AttrEvalResult, "fail")
		span.SetAttribute(agentruntime.AttrStepID, res.Divergence.StepID)
	} else {
		span.SetAttribute(AttrEvalResult, "pass")
	}
	span.End()
}

// seedTail semeia o tail append-only tal como o loop base (memory_context +
// objectivo, por esta ordem, ambos trusted).
func seedTail(spec TrajectorySpec) []agentruntime.TailSegment {
	tail := make([]agentruntime.TailSegment, 0, 8)
	if len(spec.MemoryContext) > 0 {
		tail = append(tail, agentruntime.TailSegment{Kind: agentruntime.TailMemory, Content: spec.MemoryContext})
	}
	if spec.Objective != "" {
		tail = append(tail, agentruntime.TailSegment{Kind: agentruntime.TailObjective, Content: []byte(spec.Objective)})
	}
	return tail
}

// tailHash é o fingerprint determinístico do tail (o ESTADO do run num ponto). Um
// hash sobre kind+content de cada segmento, com separadores, imune a colisões de
// concatenação.
func tailHash(tail []agentruntime.TailSegment) string {
	h := sha256.New()
	for _, seg := range tail {
		_, _ = h.Write([]byte(seg.Kind))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(seg.Content)
		_, _ = h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// fidelity devolve matched/verified (1.0 quando não houve turnos verificados).
func fidelity(matched, verified int) float64 {
	if verified == 0 {
		return 1.0
	}
	return float64(matched) / float64(verified)
}
