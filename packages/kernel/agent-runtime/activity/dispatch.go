package activity

import (
	"context"
	"errors"
	"fmt"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/saga"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// AttrDecision é o atributo de span que regista o desfecho da activity
// (permit|dedup|replay|denied|error). Complementa os atributos GenAI de AOS-013.
const AttrDecision = "aos.decision"

// OpActivity é o nome do span de ESCOPO DURÁVEL da activity (AOS-021): envolve o
// despacho idempotente (AOS-014) + a mediação (AOS-003) e regista o DESFECHO durável
// (permit|dedup|replay|denied|error) e o custo do efeito real. É DELIBERADAMENTE
// distinto do span execute_tool da semconv GenAI: esse é aberto pelo Reference
// Monitor DENTRO de [referencemonitor.Monitor.Mediate] — o ponto único de mediação
// (ADR-002) e a ÚNICA autoridade do span execute_tool (AOS-076). O span aos.activity
// nasce pai do execute_tool (o ctx derivado propaga-se ao Mediate), formando a árvore
// aos.activity → execute_tool. Manter as operações separadas evita (a) DUPLICAR o
// span execute_tool — o duplo-contar em agregadores por-operação quando o mesmo tracer
// é partilhado com o RM — e (b) apresentar um span execute_tool sem os atributos
// obrigatórios de CA2 (hash(tool+args) + result_taint), que só o RM anota. O
// aos.activity carrega o que o RM NÃO conhece: dedup, replay e o custo por efeito real.
//
// ANTI-DERIVA (AOS-211): o nome é o MESMO símbolo da folha substrate
// ([otelgenai.OpActivity]), não uma segunda cópia da string. Tem de o ser porque a
// operação é agora chave do contrato semconv ([otelgenai.requiredAttrs]) — que vive na
// folha, abaixo do kernel — e uma cópia local abriria a porta a duas strings
// "aos.activity" que divergissem sem ninguém dar por isso. Referenciar (em vez de um
// teste de igualdade) torna a deriva IMPOSSÍVEL por construção, não apenas detectável.
const OpActivity = otelgenai.OpActivity

// Dispatcher é o PONTO DE COMPOSIÇÃO das activities (AOS-021): unifica idempotência
// (ledger AOS-014) + mediação (RM AOS-003) + replay (AOS-016) + taint (ADR-005) +
// registo de compensação (AOS-020). Construir com [NewDispatcher].
//
// # Garantia estrutural (no-bypass)
//
// Em [ModeNormal] o Dispatcher NUNCA executa um efeito por outra via que não
// [Mediator.Mediate]: constrói o [referencemonitor.Call] e chama Mediate DENTRO do
// efeito do ledger; a tool só corre sob permit não-forjável do RM. Em [ModeReplay] o
// Dispatcher sequer detém um [Mediator] (é nil) — devolver o registo não pode disparar
// um efeito. Não há método que execute a tool fora deste caminho.
//
// Seguro para uso concorrente (o ledger e o RM são seguros para concorrência; o
// Dispatcher é imutável após construção).
type Dispatcher struct {
	mode      Mode
	rm        Mediator
	ledger    Ledger
	replaySrc ReplaySource
	registry  CompensationRegistrar
	tracer    agentruntime.Tracer
	obs       Observer
	sealer    ResultSealer
}

// Option configura o [Dispatcher].
type Option func(*Dispatcher)

// WithTracer injecta o tracer de observabilidade (default [agentruntime.NoopTracer]).
func WithTracer(t agentruntime.Tracer) Option { return func(d *Dispatcher) { d.tracer = t } }

// WithObserver injecta o gancho de contadores (default [NopObserver]).
func WithObserver(o Observer) Option { return func(d *Dispatcher) { d.obs = o } }

// WithCompensationRegistry injecta o registo de compensações de AOS-020. Necessário
// se alguma activity trouxer uma [Compensation]; sem ele, uma activity com compensação
// é recusada ([ErrNoRegistry]) em vez de perder a acção inversa.
func WithCompensationRegistry(r CompensationRegistrar) Option {
	return func(d *Dispatcher) { d.registry = r }
}

// ResultSealer sela em QUARENTENA o corpo do resultado de uma call permitida e devolve a
// REFERÊNCIA NUA que fica no lugar dele (`aos-handle/v1:` + token) — o PRODUTOR do `tool_result`
// por handle do layout 1.8.0 (AOS-551, fase R2 do ADR-043).
//
// Os parâmetros são os da quarentena do nó, pela mesma ordem: o titular dos dados, o run, o passo
// da call, a tool, e o conteúdo. O erro é do vocabulário de quem sela e NÃO chega ao modelo: o
// despacho materializa-o como [ErrResultSeal].
//
// O CORPO QUE DEVOLVE É A REFERÊNCIA NUA, e não um documento que a embrulhe. É a forma que
// [agentruntime.ParseHandleRef] aceita, a única que o `CallRewriter` resolve e a que o tail já
// espera. Dentro de um run um documento não compra nada sobre ela; quem precisa do digest é a
// travessia da aresta do plano, que é outro ticket.
type ResultSealer func(ctx context.Context, subject, runID, stepID, toolID string, content []byte) ([]byte, error)

// WithResultSealer injecta o PRODUTOR do `tool_result` por handle. Default: nenhum — nada é
// selado e o corpo do resultado é o que a tool devolveu, em qualquer layout.
//
// # Porque o selo vive DENTRO do efeito do ledger, e só aqui
//
// O assento é único, e foi medido. É o único ponto que é, ao mesmo tempo:
//
//   - DEPOIS da decisão do Reference Monitor, pelo que não há caminho novo para uma tool e o
//     invariante de fronteira do AOS-003 fica intacto;
//   - ANTES da escrita no step-ledger, pelo que o ledger, o loop, a captura, o replay e a rota
//     que serve os bytes designados vêem TODOS os mesmos bytes — e a âncora da origem da saída,
//     que os seus dois verificadores confirmam re-hasheando o payload do ledger, continua a
//     conferir;
//   - com contexto POR RUN: o titular já viaja no `ctx` ([durable.ContextWithTitular]) e a versão
//     do layout também ([agentruntime.ContextWithAssemblyVersion]).
//
// Um envelope que CHAMASSE [Dispatcher.Dispatch] corre DEPOIS da escrita no ledger: o ledger
// ficava com o conteúdo e o loop com a referência, a âncora deixava de conferir, e cada dedup
// re-selava com outro ordinal — logo outro token, logo outro `prompt_hash` numa retoma.
func WithResultSealer(s ResultSealer) Option { return func(d *Dispatcher) { d.sealer = s } }

// Erros do PRODUTOR do `tool_result` por handle. Os dois são FAIL-CLOSED: nada é memorizado no
// ledger, pelo que nem o conteúdo nem uma referência inválida chegam ao loop, ao tail ou à
// âncora. O preço é o do [ErrToolExecution] que já existe — o efeito correu e uma nova tentativa
// volta a correr —, e é o preço certo: a alternativa é gravar conteúdo untrusted no log de um run
// cujo layout promete que ele não existe.
var (
	// ErrResultSeal — a selagem recusou por uma causa OPERACIONAL do espaço de handles: o tecto
	// de handles do run, o tecto de bytes, o volume cheio, a cifra ou o disco indisponíveis.
	//
	// LEVA UM [ToolError] NA CADEIA, e isso é deliberado: o adaptador da via durável converte um
	// `*ToolError` em `Decision{Permit, ToolErr}` (`integration/runtime_ports.go`), pelo que o
	// loop materializa `tool_error=` no tail, o modelo lê que a chamada não deu resultado
	// utilizável, e o RUN CONTINUA. Sem isso o erro subia até ao loop e matava o run inteiro —
	// e com o tecto de handles por run (64 no nó de referência) isso é ALCANÇÁVEL: a 65.ª tool
	// call de um run da 1.8.0 mataria o run com 64 passos já aplicados. A designação da origem
	// vê `ToolError != nil`, logo a chamada não é efectiva e a âncora resolve `missing`.
	//
	// O QUE CONTINUAR CUSTA, declarado: um `tool_error` é um `tool_result` SEM o rótulo do handle,
	// pelo que o piso do run cai para untrusted e — porque o acumulador é monótono — NUNCA volta
	// a subir ([agentruntime.SegmentFloor]). Daí em diante toda a call do run chega ao Reference
	// Monitor untrusted. É a semântica que um `tool_error` já tem num layout por handle, e não uma
	// excepção do selo; o que o selo acrescenta é que a CAPACIDADE do espaço de handles passa a
	// poder decidi-la. E o tecto repete-se: nada é memorizado, o `step_id` é posicional, logo a
	// tool call seguinte re-executa o efeito. O backstop é o detector de acções repetidas e o
	// disjuntor de no-progress, que só agarram argumentos iguais. Dimensionar o tecto de handles
	// por run contra o `MaxTurns` é do AOS-556, que é quem torna a 1.8.0 accionável.
	ErrResultSeal = errors.New("activity: a selagem do resultado em handle recusou")
	// ErrSealedResultNotRef — o selo devolveu um corpo que não é uma referência de handle bem
	// formada. É defeito de quem o compôs, e não se deixa passar: o tail materializá-lo-ia como
	// erro de tool (negando a mais, que é seguro) mas a designação da origem digeria-o e o
	// `aos-orq` entregá-lo-ia a jusante como saída do run.
	ErrSealedResultNotRef = errors.New("activity: o selo do resultado nao devolveu uma referencia de handle bem formada")
	// ErrSealWithoutAssemblyVersion — há selo composto mas o contexto não traz a versão do
	// layout do run. Recusa, nunca «assume o layout antigo»: assumir é o fail-open exacto que
	// este ticket existe para não ter.
	ErrSealWithoutAssemblyVersion = errors.New("activity: selo do resultado composto sem a versao do layout no contexto (agentruntime.ContextWithAssemblyVersion)")
)

// seloDescartouEfeito diz se o erro é de um dos caminhos do SELO.
//
// OS TRÊS SÓ PODEM OCORRER DEPOIS DE O EFEITO TER CORRIDO COM ÊXITO: o [Dispatcher.selar] é
// chamado com `dec.Output`, que só existe depois de o Reference Monitor ter devolvido permit SEM
// `ToolErr` — isto é, com a tool já executada. Em todos eles o resultado é DESCARTADO e nada é
// memorizado no ledger, pelo que a acção inversa tem de ser registada ainda que o passo não tenha
// desfecho durável.
//
// É A ENUMERAÇÃO ÚNICA. Um erro novo no caminho do selo entra aqui, ou a sua compensação cai em
// silêncio — e um efeito irreversível aplicado sem inversa registada não dá erro em sítio nenhum.
func seloDescartouEfeito(err error) bool {
	return errors.Is(err, ErrResultSeal) ||
		errors.Is(err, ErrSealedResultNotRef) ||
		errors.Is(err, ErrSealWithoutAssemblyVersion)
}

// selar aplica o PRODUTOR ao output de uma call permitida e devolve o payload que o ledger vai
// gravar. Sem selo composto, ou num layout que não põe os resultados por handle, devolve o output
// TAL COMO ENTROU — byte-idêntico, sem o olhar.
//
// A DECISÃO É DO LAYOUT E DA TOOL, pela MESMA função que o tail pergunta
// ([agentruntime.SealsToolResult]): é ela que guarda a excepção da `media_view`, cujo resultado o
// tail resolve CRU para anexar a imagem.
//
// Um output VAZIO não se sela: não há nada para selar, o espaço de handles recusaria, e o tail de
// um permit vazio materializa os mesmos bytes que no layout anterior.
// Devolve o payload, se ele É uma referência (para o ledger o marcar), e o erro.
func (d *Dispatcher) selar(ctx context.Context, act Activity, output []byte) ([]byte, bool, error) {
	if d.sealer == nil || len(output) == 0 {
		return output, false, nil
	}
	versao := agentruntime.AssemblyVersionFromContext(ctx)
	if versao == "" {
		return nil, false, ErrSealWithoutAssemblyVersion
	}
	if !agentruntime.SealsToolResult(versao, act.ToolID) {
		return output, false, nil
	}
	ref, err := d.sealer(ctx, act.Principal.Titular(), act.RunID, act.StepID, act.ToolID, output)
	if err != nil {
		// O [ToolError] na cadeia é o que faz o run CONTINUAR em vez de morrer — ver
		// [ErrResultSeal].
		return nil, false, fmt.Errorf("%w: %w", ErrResultSeal, &ToolError{Err: err})
	}
	if _, ok := agentruntime.ParseHandleRef(string(ref)); !ok {
		return nil, false, ErrSealedResultNotRef
	}
	return ref, true, nil
}

// NewDispatcher constrói um dispatcher em [ModeNormal] sobre o Reference Monitor
// (obrigatório) e o step-ledger (obrigatório). É o fluxo completo do contrato.
func NewDispatcher(rm Mediator, ledger Ledger, opts ...Option) (*Dispatcher, error) {
	d := newBase(ModeNormal, opts...)
	d.rm = rm
	d.ledger = ledger
	if rm == nil {
		return nil, ErrNilMediator
	}
	if ledger == nil {
		return nil, ErrNilLedger
	}
	return d, nil
}

// NewReplayDispatcher constrói um dispatcher em [ModeReplay] sobre uma [ReplaySource]
// (obrigatória). NÃO recebe Reference Monitor nem ledger: em replay a activity só
// devolve o resultado registado — a ausência de efeito é ESTRUTURAL (rm == nil).
func NewReplayDispatcher(src ReplaySource, opts ...Option) (*Dispatcher, error) {
	d := newBase(ModeReplay, opts...)
	d.replaySrc = src
	if src == nil {
		return nil, ErrNilReplaySource
	}
	return d, nil
}

func newBase(mode Mode, opts ...Option) *Dispatcher {
	d := &Dispatcher{
		mode:   mode,
		tracer: agentruntime.NoopTracer{},
		obs:    NopObserver{},
	}
	for _, o := range opts {
		o(d)
	}
	if d.tracer == nil {
		d.tracer = agentruntime.NoopTracer{}
	}
	if d.obs == nil {
		d.obs = NopObserver{}
	}
	return d
}

// Mode devolve o modo de operação do dispatcher.
func (d *Dispatcher) Mode() Mode { return d.mode }

// Dispatch despacha UMA activity segundo o modo do dispatcher.
//
// # ModeNormal
//
//  1. deriva a idempotency key = run_id:step_id (AOS-014);
//  2. verifica already-applied no ledger — se já aplicado, devolve o resultado
//     REGISTADO SEM re-executar ([Result.Deduplicated] = true);
//  3. medeia pelo Reference Monitor ANTES de executar (identidade/política/orçamento/
//     egress/audit); só sob permit a tool corre — sem caminho directo (ADR-002);
//  4. grava o resultado (append-only) no Event Store via o ledger (formato de evento
//     que alimenta replay AOS-016 e observabilidade);
//  5. devolve o resultado marcado UNTRUSTED (ADR-005). Se a activity tiver compensação,
//     regista-a no [CompensationRegistrar] após Apply — TANTO no applied como no dedup
//     (a intenção de compensar é reconstruível na retoma; AOS-020, AOS021-Q1).
//
// Um deny/escalate do RM devolve [ErrMediationDenied] (nenhum efeito, nada memorizado).
// Uma tool permitida que falhe a jusante devolve [ErrToolExecution] (nada memorizado,
// retriável). O único erro fatal do RM é cancelamento de contexto.
//
// # ModeReplay
//
// Devolve o resultado REGISTADO da [ReplaySource] ([Result.Replayed] = true) com ZERO
// efeito, SEM mediação nem execução. Sem registo ⇒ [ErrReplayMiss] (nunca execução ao
// vivo como fallback).
func (d *Dispatcher) Dispatch(ctx context.Context, act Activity) (Result, error) {
	if err := act.validate(); err != nil {
		return Result{}, err
	}
	key, err := durable.IdempotencyKey(act.RunID, act.StepID)
	if err != nil {
		return Result{}, err
	}
	keyHash := durable.HashKey(key)

	spanCtx, span := d.startSpan(ctx, act)
	defer span.End()

	switch d.mode {
	case ModeReplay:
		return d.dispatchReplay(key, keyHash, span)
	case ModeNormal:
		return d.dispatchNormal(spanCtx, act, key, keyHash, span)
	default:
		return Result{}, ErrUnknownMode
	}
}

// dispatchReplay devolve o resultado registado — ZERO efeito, sem tocar no RM. Esta
// função NÃO referencia d.rm: em modo replay ele é nil, provando estruturalmente a
// ausência de efeito.
func (d *Dispatcher) dispatchReplay(key, keyHash string, span agentruntime.Span) (Result, error) {
	rec, ok := d.replaySrc.Applied(key)
	if !ok {
		span.SetAttribute(AttrDecision, "replay-miss")
		return Result{}, fmt.Errorf("%w: key_hash=%s", ErrReplayMiss, keyHash)
	}
	span.SetAttribute(AttrDecision, "replay")
	d.obs.Replayed(keyHash)
	return Result{
		Output:   agentruntime.Untrusted(rec.Payload),
		Status:   rec.Status,
		Replayed: true,
	}, nil
}

// denyOutsideAllowlist medeia, FORA do ledger, uma call cuja tool está fora da lista-branca do
// run (AOS-485). O RM nega-a por si — gate ou backstop — e deixa o evento, o selo e o contador.
// Um permit aqui é o RM a não impor a lista (um [Mediator] que não é o RM de referência): a
// tool já correu, mas o resultado NÃO é devolvido nem memorizado, e o erro é fatal ao loop —
// fail-closed, em vez de entregar ao modelo o output de uma tool que o run não podia chamar.
func (d *Dispatcher) denyOutsideAllowlist(ctx context.Context, act Activity, keyHash string, span agentruntime.Span) (Result, error) {
	dec, err := d.rm.Mediate(ctx, act.toCall())
	if err != nil {
		span.SetAttribute(AttrDecision, "error")
		return Result{}, err // cancelamento de contexto (fatal)
	}
	if dec.Effect == referencemonitor.EffectPermit {
		span.SetAttribute(AttrDecision, "error")
		return Result{}, ErrAllowlistNotEnforced
	}
	span.SetAttribute(AttrDecision, "denied")
	d.obs.Denied(keyHash)
	return Result{}, fmt.Errorf("%w: %w", ErrMediationDenied, &MediationDenial{
		Effect: string(dec.Effect), Code: dec.Code, DeniedBy: dec.DeniedBy,
	})
}

// dispatchNormal corre o fluxo completo: already-applied → mediação → memoização.
func (d *Dispatcher) dispatchNormal(ctx context.Context, act Activity, key, keyHash string, span agentruntime.Span) (Result, error) {
	// LISTA-BRANCA DO RUN ANTES DO LEDGER (AOS-485). O already-applied do ledger corre ANTES da
	// mediação — o Mediate vive dentro da closure do Apply — e a impressão da acção é
	// (ToolID, Input), sem a lista. Uma call fora da lista cuja chave já tivesse sido aplicada
	// (o mesmo passo, despachado antes sem lista ou com outra) recebia o output MEMORIZADO sem
	// passar pelo RM: um permit sem mediação, sem recusa contada, e o output a chegar ao modelo.
	// Antes do AOS-485 o ciclo negava antes do dispatcher e isto não acontecia. Por isso uma
	// call fora da lista NÃO entra no Apply: vai directamente ao RM, que a nega com pegada, e
	// nada é lido nem escrito no ledger.
	if !referencemonitor.RunAllowsTool(act.AllowedTools, act.ToolID) {
		return d.denyOutsideAllowlist(ctx, act, keyHash, span)
	}
	if act.Compensation != nil {
		if d.registry == nil {
			return Result{}, ErrNoRegistry
		}
		// Validação fail-closed ANTES de qualquer efeito: uma compensação sem acção
		// inversa nunca poderia reverter. Rejeitá-la aqui garante ainda que o registo
		// pós-Apply (que corre TAMBÉM em dedup, ver abaixo) não pode falhar a meio —
		// com StepID já validado e Action não-nil, Register é infalível.
		if act.Compensation.Action == nil {
			return Result{}, ErrNilCompensationAction
		}
	}

	// CANAL LATERAL do custo do efeito (AOS-212). Variável EXTERIOR fechada pela closure
	// do Apply: a closure alimenta-a a partir do DESFECHO real (dec.CostMicroUSD), o ramo
	// applied abaixo anota o span a partir DELA — nunca do Activity de entrada. Fica a 0
	// nos caminhos dedup/replay POR CONSTRUÇÃO: a closure SÓ corre no efeito real
	// (already-applied vive DENTRO de Apply e precede-a; replay nem chega aqui), pelo que a
	// disciplina "uma vez por efeito real, zero em dedup/replay" cai de graça. E — o eixo de
	// risco #1 — o custo NÃO entra no durable.Result gravado no ledger: se entrasse, o
	// replay (que lê rec.Payload) re-emitiria um custo que o efeito nunca voltou a incorrer.
	var effectCostMicroUSD int64

	// TITULAR DO RUN → LEDGER (AOS-093/AOS-245). O ledger cifra o Result.Payload — o
	// OUTPUT da tool — sob a KEK POR-TITULAR antes de o persistir, mas só sabe QUEM é o
	// titular se alguém lho disser: o ledger do nó é composto UMA vez no arranque e o
	// titular é POR-RUN. Este é o ponto que o conhece (act.Principal), e é o MESMO valor
	// que o capturer sela (goal.Titular() em loop.go, que o loop põe no Principal da call),
	// pelo que os mesmos bytes ficam sob a MESMA chave em replay.captured e em
	// step.ledger.applied — e o crypto-shredding (GDPR Art. 17) alcança ambos ao destruir uma
	// só KEK. AOS-440: é o TITULAR DOS DADOS ([referencemonitor.Principal.Titular]), que num run
	// filho de um plano é o submissor e não o chamador; vazio ⇒ o NHIID de sempre.
	//
	// Sem isto o ledger caía no fallback do produtor (vazio no nó) e persistia o output
	// da tool EM CLARO no WAL, com o cifrador composto e inerte. O ledger de produção
	// leva a guarda [durable.WithRequireTitular], que torna esse silêncio impossível.
	ctx = durable.ContextWithTitular(ctx, act.Principal.Titular())

	// A verificação already-applied vive DENTRO de Apply (precede o efeito). O efeito
	// abaixo é a ÚNICA via de execução: constrói o Call e chama Mediate — sem permit,
	// a tool nunca corre (o RM só a despacha sob permit não-forjável).
	// A IMPRESSÃO DIGITAL DA ACÇÃO acompanha o Apply, e o ledger recusa se a chave já
	// estiver aplicada por OUTRA acção.
	//
	// A chave é `f(run_id, step_id)` e o step_id é POSICIONAL — nada da acção entra nele. Numa
	// retoma em que o turno não tenha captura, o modelo é re-interrogado ao vivo e pode emitir
	// outra tool call, que recebe o step_id já aplicado; sem esta impressão o dedup devolvia-lhe
	// o resultado da acção ANTERIOR, sem executar e sem passar pelo Reference Monitor.
	//
	// Reutiliza o MESMO hash canónico que o RM já calcula para o span (`aos.tool.call_hash`):
	// um segundo digest com outra canonicalização divergiria do primeiro, e a divergência
	// apareceria como recusa espúria numa retoma legítima.
	fingerprint := otelgenai.CanonicalToolCallHash(act.ToolID, act.Input)
	// AUTOR DO EFEITO → ENVELOPE DO LEDGER (AOS-478). O `step.ledger.applied` é causado por
	// esta tool call e identifica o MESMO principal que o selo `tool.call.mediated` do passo:
	// o que o RM resolveu do token verificado ([referencemonitor.Decision.Principal]), e não o
	// que o loop apresentou. Só existe depois da mediação, daí a função lida pelo ledger no fim.
	var autor eventstore.Producer
	res, applied, err := d.ledger.Apply(ctx, key, func(ctx context.Context) (durable.Result, error) {
		dec, mErr := d.rm.Mediate(ctx, act.toCall())
		if mErr != nil {
			return durable.Result{}, mErr // cancelamento de contexto (fatal)
		}
		if dec.Effect != referencemonitor.EffectPermit {
			// O VEREDICTO viaja tipado (ver [MediationDenial]): sem isso o adaptador a
			// jusante não distingue deny de ESCALATE e apagaria o caminho de aprovação
			// humana — o loop nunca suspenderia.
			return durable.Result{}, fmt.Errorf("%w: %w", ErrMediationDenied, &MediationDenial{
				Effect: string(dec.Effect), Code: dec.Code, DeniedBy: dec.DeniedBy,
			})
		}
		// Tool permitida mas falhou a jusante: nada é memorizado; retriável.
		if dec.ToolErr != nil {
			return durable.Result{}, fmt.Errorf("%w: %w", ErrToolExecution, &ToolError{Err: dec.ToolErr})
		}
		autor = dec.Principal.EventProducer()
		// Canal lateral: capta o custo MEDIDO do efeito que ACABOU de correr. Só aqui —
		// nunca no durable.Result devolvido, que é o que o ledger grava e o replay relê.
		effectCostMicroUSD = dec.CostMicroUSD
		// O PRODUTOR do `tool_result` por handle (AOS-551), no único assento que existe: depois
		// do permit, antes da escrita no ledger. Ver [WithResultSealer].
		payload, referencia, sErr := d.selar(ctx, act, dec.Output)
		if sErr != nil {
			return durable.Result{}, sErr
		}
		status := act.Status
		if status == "" {
			status = StatusOK
		}
		// `Reference` é o vocabulário do PRÓPRIO ledger para «este payload é uma referência e não
		// bytes de conteúdo» ([durable.Result.Reference]). Não é persistido e não altera a cifra:
		// é a guarda de escrita do modo sensível, que RECUSA um payload não-vazio não marcado
		// quando não há cifrador ([durable.ErrClearResultInSensitiveMode]). Sem esta marca, um nó
		// em modo sensível sem o cifrador por-titular recusava TODA a tool call de um run da
		// 1.8.0 — por não saber que o payload já é uma referência.
		return durable.Result{Status: status, Payload: payload, Reference: referencia}, nil
	}, durable.WithActionFingerprint(fingerprint), durable.WithEffectProducer(func() eventstore.Producer { return autor }))
	if err != nil {
		if errors.Is(err, ErrMediationDenied) {
			span.SetAttribute(AttrDecision, "denied")
			d.obs.Denied(keyHash)
		} else {
			span.SetAttribute(AttrDecision, "error")
		}
		// A COMPENSAÇÃO DE UM EFEITO QUE CORREU E FOI DESCARTADO (AOS-551).
		//
		// Numa recusa do SELO o efeito CORREU — o RM permitiu e a tool executou —, nada foi
		// memorizado, e a acção inversa ficava sem ser registada porque o registo normal vive
		// depois deste return. Com a recusa a materializar-se como `tool_error` (ver
		// [ErrResultSeal]) o run CONTINUA e pode fechar cumprido: um efeito irreversível
		// aplicado, cujo resultado ninguém tem, e que nenhuma saga sabe reverter.
		//
		// A CONDIÇÃO MEDE A PROPRIEDADE — «o efeito correu e o resultado foi descartado» — e não
		// um proxy dela. Vale para os TRÊS erros do selo ([seloDescartouEfeito]), fatais
		// incluídos: nos fatais o run fecha `failed` e a saga corre, mas corria sobre um registry
		// sem este passo, pelo que o efeito ficava igualmente irreversível. Não vale num
		// deny/escalate (nenhum efeito ocorreu) nem num [ErrToolExecution] (o efeito FALHOU) —
		// registar a inversa de algo que não aconteceu seria pior.
		//
		// O registry é idempotente por `step_id`, pelo que uma nova tentativa não duplica nem
		// muda a ordem LIFO. As pré-condições (registry composto, Action não-nil) foram validadas
		// no topo desta função, logo este Register é infalível e o seu erro não pode esconder o
		// do selo.
		if act.Compensation != nil && seloDescartouEfeito(err) {
			_ = d.registry.Register(saga.Compensation{StepID: act.StepID, Action: act.Compensation.Action, Reason: act.Compensation.Reason})
		}
		return Result{}, err
	}

	// Registo de compensação DESACOPLADO da execução do efeito (AOS021-Q1). Corre tanto
	// no caminho applied (efeito agora) COMO no caminho dedup (already-applied): a
	// INTENÇÃO de compensar tem de ser reconstruível independentemente de o efeito ter
	// corrido NESTA invocação. No crash-resume, um worker novo re-despacha os passos
	// aplicados (obtém dedup) e ESTE registo restaura a acção inversa no registry — sem
	// ele, a saga percorreria um registry vazio e transitaria compensating→ready sem
	// reverter nada, enquanto o efeito permanece aplicado. O registry é idempotente por
	// step_id (re-registar em dedup não duplica nem altera a ordem LIFO).
	//
	// LIMITE HONESTO (durabilidade): o registry é in-memory e a Action é uma closure
	// não-serializável; a reconstrução depende de o loop RE-DESPACHAR os passos
	// aplicados na retoma. A durabilidade PLENA da intenção (marcador por step_id no
	// Event Store + factory de compensação por ToolID no rebuild) fica para o adaptador
	// de engine (AOS-022). Ver doc.go, "Compensação: alcance e limite".
	if act.Compensation != nil {
		comp := saga.Compensation{StepID: act.StepID, Action: act.Compensation.Action, Reason: act.Compensation.Reason}
		if rerr := d.registry.Register(comp); rerr != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrCompensationRegister, rerr)
		}
	}

	if applied {
		span.SetAttribute(AttrDecision, "permit")
		// Observabilidade de custo por span SÓ no efeito real de agora (AOS021-Q5,
		// AOS-212): a fonte é o DESFECHO do efeito (effectCostMicroUSD, alimentado por
		// dec.CostMicroUSD na closure), NÃO o Activity de entrada. Em dedup nenhum custo é
		// incorrido e a variável está a 0 por construção, pelo que este ramo nem sequer
		// corre em dedup/replay — um agregador nunca soma o custo N vezes por N retries.
		// custo == 0 continua a NÃO emitir o atributo (custo gratuito e desconhecido
		// indistintos, e retro-compat: uma tool sem custo não altera o span). Emite-se o
		// micro-USD inteiro (fonte de verdade) em PARALELO com o USD float, tal como o span
		// chat (loop.go), para a agregação reconciliar sem erro de arredondamento.
		if effectCostMicroUSD != 0 {
			span.SetAttribute(agentruntime.AttrCostUSD, microUSDToUSD(effectCostMicroUSD))
			span.SetAttribute(agentruntime.AttrCostMicroUSD, effectCostMicroUSD)
		}
		d.obs.Applied(keyHash)
	} else {
		span.SetAttribute(AttrDecision, "dedup")
		d.obs.Deduplicated(keyHash)
	}
	return Result{
		Output:       agentruntime.Untrusted(res.Payload),
		Status:       res.Status,
		Deduplicated: !applied,
	}, nil
}

// startSpan abre o span de escopo durável [OpActivity] e anota-o com a correlação
// (run/step/tool). NÃO é o span execute_tool — esse é aberto pelo Reference Monitor
// dentro de Mediate (AOS-076), a única autoridade. Devolve o ctx derivado (para o RM
// abrir o execute_tool como FILHO deste span, herdando trace_id + parent_span_id) e o
// span aos.activity, que o dispatcher anota com o desfecho durável (permit|dedup|
// replay|denied|error) e o custo do efeito real.
func (d *Dispatcher) startSpan(ctx context.Context, act Activity) (context.Context, agentruntime.Span) {
	ctx, span := d.tracer.StartSpan(ctx, OpActivity)
	// gen_ai.operation.name PRIMEIRO (AOS-211): sem ele, ValidateSpanData resolvia a
	// operação por fallback ao Name do span e — antes de OpActivity entrar em
	// requiredAttrs — aceitava-o SEM contrato; consumidores que leem estritamente o
	// atributo (ex. operationOf de platform/eval) nunca o viam como operação. startSpan
	// é a ÚNICA fonte deste span, pelo que TODOS os aos.activity o passam a trazer — a
	// pré-condição para o pôr sob contrato no mesmo commit (anotar antes de exigir).
	span.SetAttribute(agentruntime.AttrOperationName, OpActivity)
	span.SetAttribute(agentruntime.AttrToolName, act.ToolID)
	span.SetAttribute(agentruntime.AttrRunID, act.RunID)
	span.SetAttribute(agentruntime.AttrStepID, act.StepID)
	return ctx, span
}

// microUSDToUSD converte micro-USD inteiro em USD (float, só para o atributo de span).
func microUSDToUSD(microUSD int64) float64 { return float64(microUSD) / 1_000_000.0 }
