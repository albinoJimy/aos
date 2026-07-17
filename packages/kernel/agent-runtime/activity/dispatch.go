package activity

import (
	"context"
	"errors"
	"fmt"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/saga"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
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
const OpActivity = "aos.activity"

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

// dispatchNormal corre o fluxo completo: already-applied → mediação → memoização.
func (d *Dispatcher) dispatchNormal(ctx context.Context, act Activity, key, keyHash string, span agentruntime.Span) (Result, error) {
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

	// A verificação already-applied vive DENTRO de Apply (precede o efeito). O efeito
	// abaixo é a ÚNICA via de execução: constrói o Call e chama Mediate — sem permit,
	// a tool nunca corre (o RM só a despacha sob permit não-forjável).
	res, applied, err := d.ledger.Apply(ctx, key, func(ctx context.Context) (durable.Result, error) {
		dec, mErr := d.rm.Mediate(ctx, act.toCall())
		if mErr != nil {
			return durable.Result{}, mErr // cancelamento de contexto (fatal)
		}
		if dec.Effect != referencemonitor.EffectPermit {
			return durable.Result{}, fmt.Errorf("%w: effect=%s code=%s", ErrMediationDenied, dec.Effect, dec.Code)
		}
		// Tool permitida mas falhou a jusante: nada é memorizado; retriável.
		if dec.ToolErr != nil {
			return durable.Result{}, fmt.Errorf("%w: %w", ErrToolExecution, &ToolError{Err: dec.ToolErr})
		}
		status := act.Status
		if status == "" {
			status = StatusOK
		}
		return durable.Result{Status: status, Payload: dec.Output}, nil
	})
	if err != nil {
		if errors.Is(err, ErrMediationDenied) {
			span.SetAttribute(AttrDecision, "denied")
			d.obs.Denied(keyHash)
		} else {
			span.SetAttribute(AttrDecision, "error")
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
		// Observabilidade de custo por span SÓ no efeito real de agora (AOS021-Q5): em
		// dedup nenhum custo é incorrido, pelo que emiti-lo faria um agregador somar o
		// custo N vezes por N retries. CostMicroUSD==0 continua a não emitir (custo
		// gratuito e desconhecido indistintos — dívida menor de observabilidade).
		if act.CostMicroUSD != 0 {
			span.SetAttribute(agentruntime.AttrCostUSD, microUSDToUSD(act.CostMicroUSD))
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
	span.SetAttribute(agentruntime.AttrToolName, act.ToolID)
	span.SetAttribute(agentruntime.AttrRunID, act.RunID)
	span.SetAttribute(agentruntime.AttrStepID, act.StepID)
	return ctx, span
}

// microUSDToUSD converte micro-USD inteiro em USD (float, só para o atributo de span).
func microUSDToUSD(microUSD int64) float64 { return float64(microUSD) / 1_000_000.0 }
