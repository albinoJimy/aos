package referencemonitor

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// ToolFunc é a assinatura de uma tool despachável. Recebe o input opaco do call
// e devolve o resultado (marcado untrusted a jusante) ou um erro de execução.
//
// IMPORTANTE (no-bypass): um valor ToolFunc NUNCA deve ser invocado
// directamente fora do RM. A única via legítima de execução é [Monitor.Mediate],
// que só despacha após permit. O lint de arquitectura (subpacote archlint)
// sinaliza invocações directas de ToolFunc fora deste pacote.
type ToolFunc func(ctx context.Context, input []byte) ([]byte, error)

// CostingToolFunc é uma tool que, além do resultado, REPORTA o custo MEDIDO do seu
// efeito real em micro-USD inteiro (>= 0), apurado no momento da execução (AOS-212).
// É a fonte declarada de [Decision.CostMicroUSD]: só uma tool registada por
// [Monitor.RegisterCosting] pode reportar custo; as tools de [Monitor.Register]
// reportam sempre 0 (custo desconhecido/gratuito — o caso HONESTO das tools de
// referência de produção do nó, que não incorrem custo mensurável).
//
// O custo é um SINAL DE OBSERVABILIDADE do desfecho, medido de baixo (a tool paga /
// o Model Gateway real de EPIC-06 devolveria o custo efectivamente faturado); não é
// uma estimativa declarada à cabeça. As mesmas garantias de no-bypass da [ToolFunc]
// aplicam-se: nunca invocar directamente fora do RM.
type CostingToolFunc func(ctx context.Context, input []byte) (out []byte, costMicroUSD int64, err error)

// registeredTool é o valor interno do registo de tools. Guarda a tool na forma em que
// foi registada: fn (simples, custo 0) OU cost (reporta o custo medido do efeito). O
// dispatcher interno ([Monitor.dispatch]) escolhe o caminho e devolve sempre o custo.
type registeredTool struct {
	fn   ToolFunc
	cost CostingToolFunc
}

// Metrics são contadores de observabilidade leve (sem SDK OTel — isso é
// EPIC-08). Todos os acessos são atómicos.
type Metrics struct {
	Permits     atomic.Uint64
	Denials     atomic.Uint64
	Escalations atomic.Uint64
}

// Snapshot devolve uma leitura consistente-o-suficiente dos contadores.
func (m *Metrics) Snapshot() (permits, denials, escalations uint64) {
	return m.Permits.Load(), m.Denials.Load(), m.Escalations.Load()
}

// Monitor é o Reference Monitor: o PEP mandatório do AOS. Construir com [New].
type Monitor struct {
	hooks []Hook
	sink  EventSink

	mu    sync.RWMutex
	tools map[string]registeredTool

	metrics Metrics

	now  func() time.Time
	rand func() uint64

	// tracer é a porta de observabilidade OTel GenAI (AOS-076). Default
	// [otelgenai.NoopTracer] — com ele o comportamento é idêntico ao de antes da
	// instrumentação. O Agent Runtime injecta o SEU tracer (via [WithTracer] ou
	// [Monitor.SetTracer]) para que o span execute_tool aberto AQUI caia na mesma
	// árvore/sink dos spans invoke_agent/chat do loop.
	tracer otelgenai.Tracer
}

// resultTaintUntrusted é a marca fixa do span execute_tool: o resultado de uma
// tool call volta SEMPRE untrusted (ADR-005), qualquer que seja o veredicto.
const resultTaintUntrusted = "untrusted"

// Option configura o Monitor na construção.
type Option func(*Monitor)

// WithHooks substitui a cadeia de hooks. A ORDEM dada é a ordem de invocação —
// o RM não a reordena nem valida a presença de hooks específicos; a ordem
// canónica de mediação (identity → policy → budget → egress → audit) é a que
// [DefaultHooks] fornece, e produção deve fornecer a cadeia completa. Uma cadeia
// VAZIA não abre exceção: [Monitor.Mediate] nega-a fail-closed (não permite
// tudo silenciosamente). Use [DefaultHooks] como base.
func WithHooks(hooks ...Hook) Option {
	return func(m *Monitor) { m.hooks = hooks }
}

// WithEventSink injecta o sink de auditoria durável (ver [NewEventStoreSink]).
func WithEventSink(s EventSink) Option {
	return func(m *Monitor) { m.sink = s }
}

// WithTracer injecta a porta de observabilidade OTel GenAI (AOS-076). Default
// [otelgenai.NoopTracer]. O Agent Runtime partilha aqui o SEU tracer para que o
// span execute_tool aberto em [Monitor.Mediate] caia na mesma árvore que os spans
// invoke_agent/chat do loop.
func WithTracer(t otelgenai.Tracer) Option {
	return func(m *Monitor) { m.tracer = t }
}

// SetTracer injecta o tracer após a construção — é o ponto de sutura que o Agent
// Runtime usa para partilhar a sua árvore de spans com um Monitor já construído
// (o RT detém o RM; ver agentruntime.New). Passar nil repõe o [otelgenai.NoopTracer].
func (m *Monitor) SetTracer(t otelgenai.Tracer) {
	if t == nil {
		t = otelgenai.NoopTracer{}
	}
	m.tracer = t
}

// withClock injecta um relógio (uso interno/testes).
func withClock(f func() time.Time) Option {
	return func(m *Monitor) { m.now = f }
}

// withNonce injecta a fonte de nonce (uso interno/testes determinísticos).
func withNonce(f func() uint64) Option {
	return func(m *Monitor) { m.rand = f }
}

// New constrói um Monitor. Por omissão: cadeia de stubs neutros ([DefaultHooks])
// e [discardSink] (não-durável — produção DEVE injectar [WithEventSink] com um
// sink real, senão o fail-closed de auditoria não tem efeito).
func New(opts ...Option) *Monitor {
	m := &Monitor{
		hooks:  DefaultHooks(),
		sink:   discardSink{},
		tools:  make(map[string]registeredTool),
		now:    time.Now,
		rand:   rand.Uint64,
		tracer: otelgenai.NoopTracer{},
	}
	for _, o := range opts {
		o(m)
	}
	if m.sink == nil {
		m.sink = discardSink{}
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.rand == nil {
		m.rand = rand.Uint64
	}
	if m.tracer == nil {
		m.tracer = otelgenai.NoopTracer{}
	}
	return m
}

// Register associa um ToolID a uma ToolFunc. O registo é imutável: re-registar o
// mesmo ToolID devolve [ErrToolAlreadyRegistered]. Uma tool não registada é
// negada por omissão (default-deny). Register é seguro para concorrência.
func (m *Monitor) Register(toolID string, fn ToolFunc) error {
	if fn == nil {
		return ErrInvalidRegistration
	}
	return m.register(toolID, registeredTool{fn: fn})
}

// RegisterCosting associa um ToolID a uma [CostingToolFunc] — uma tool que REPORTA o
// custo medido do seu efeito real (AOS-212). Idêntica a [Register] em todas as
// garantias (imutável, default-deny, segura para concorrência), acrescentando que o
// custo reportado alimenta [Decision.CostMicroUSD] e, a jusante, o span aos.activity.
// Usada pelo produtor de custo (o Model Gateway real / tools pagas de EPIC-06 e, no
// nó de referência, a tool de referência rotulada que prova o fio ponta-a-ponta).
func (m *Monitor) RegisterCosting(toolID string, fn CostingToolFunc) error {
	if fn == nil {
		return ErrInvalidRegistration
	}
	return m.register(toolID, registeredTool{cost: fn})
}

func (m *Monitor) register(toolID string, t registeredTool) error {
	if toolID == "" {
		return ErrInvalidRegistration
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.tools[toolID]; exists {
		return ErrToolAlreadyRegistered
	}
	m.tools[toolID] = t
	return nil
}

// Metrics devolve o ponteiro para os contadores de observabilidade.
func (m *Monitor) Metrics() *Metrics { return &m.metrics }

// Mediate é a SUPERFÍCIE ÚNICA de autorização e despacho de tool calls. Executa
// a cadeia de hooks configurada pela ordem em que foi fornecida (a ordem
// canónica identity → policy → budget → egress → audit é a de [DefaultHooks]) e,
// só se todos permitirem, grava o evento de mediação e despacha a tool via o
// dispatcher interno. Uma cadeia de hooks vazia é negada fail-closed.
//
// Garantias (fail-closed, ADR-002 / contrato C1):
//   - qualquer hook que devolva deny/escalate, erro ou panic → Decision Deny/
//     Escalate, evento de negação gravado (best-effort), tool NÃO despachada;
//   - tool não registada → Deny (default-deny);
//   - no caminho de permit, o evento de mediação é gravado ANTES do despacho;
//     se o registo falhar, a decisão degrada para Deny (auditoria fail-closed);
//   - Mediate nunca devolve Permit sem ter despachado sob um Permit válido.
//
// O erro devolvido é reservado a cancelamento de contexto; as negações de
// política são comunicadas via Decision.Effect, não via error.
//
// INSTRUMENTAÇÃO (AOS-076): Mediate é o ÚNICO ponto de mediação, logo abrir AQUI o
// span execute_tool garante cobertura de 100% das tool calls (ADR-002) — qualquer
// caller do RM fica instrumentado, não só o loop. O span nasce filho do ctx (o
// invoke_agent propagado pelo RT), é anotado com nome/hash(tool+args)/taint da
// autorização/marca untrusted do resultado, e é fechado (via defer) em TODOS os
// caminhos — permit, deny, escalate, erro de contexto — com o veredicto observável.
func (m *Monitor) Mediate(ctx context.Context, call Call) (dec Decision, err error) {
	spanCtx, span := m.tracer.StartSpan(ctx, otelgenai.OpExecuteTool)
	span.SetAttribute(otelgenai.AttrOperationName, otelgenai.OpExecuteTool)
	span.SetAttribute(otelgenai.AttrToolName, call.ToolID)
	// hash(tool+args) — REFERÊNCIA por hash (âncora de action-dedup, AOS-081); o
	// Input jamais é gravado no span (content-capture por referência; payload é AOS-079).
	span.SetAttribute(otelgenai.AttrToolCallHash, toolCallHash(call.ToolID, call.Input))
	// PRINCIPAL da decisão (AOS-087 DoD): o span da decisão do PDP é observável com o
	// principal (a NHI). É um IDENTIFICADOR, nunca um segredo (o Credential efémero e o
	// Input jamais são anotados). Fecha o critério "spans emitidos com principal e resultado".
	if call.Principal.NHIID != "" {
		span.SetAttribute(otelgenai.AttrPrincipalNHI, call.Principal.NHIID)
	}
	// Taint da AUTORIZAÇÃO (AOS-069): o rótulo, nunca o conteúdo.
	span.SetAttribute(otelgenai.AttrTaint, call.Context.Taint)
	// O RESULTADO volta SEMPRE untrusted (ADR-005), qualquer que seja o veredicto.
	span.SetAttribute(otelgenai.AttrResultTaint, resultTaintUntrusted)
	if call.RunID != "" {
		span.SetAttribute(otelgenai.AttrRunID, call.RunID)
	}
	if call.StepID != "" {
		span.SetAttribute(otelgenai.AttrStepID, call.StepID)
	}
	defer func() {
		span.SetAttribute(otelgenai.AttrDecision, string(dec.Effect))
		// Numa negação/escalada, anotar o hook atribuível (ex.: "taint") para que a
		// causa da decisão seja auto-descritível no span, sem segredos.
		if dec.Effect != EffectPermit && dec.DeniedBy != "" {
			span.SetAttribute(otelgenai.AttrDeniedBy, dec.DeniedBy)
		}
		// Uma tool PERMITIDA pode falhar em runtime: error.type distingue um output
		// vazio legítimo de um output de tool falhada.
		if dec.ToolErr != nil {
			span.SetAttribute(otelgenai.AttrErrorType, dec.ToolErr.Error())
		}
		span.End()
	}()

	// O ctx DERIVADO do span (spanCtx) segue para a avaliação: futuros spans internos
	// da cadeia de hooks nascem filhos do execute_tool, mantendo a propagação de trace.
	dec, err = m.evaluate(spanCtx, call)
	return dec, err
}

// evaluate corre a cadeia de mediação (hooks → default-deny → audit-before-effect →
// despacho) e devolve a decisão. É o núcleo de [Monitor.Mediate], separado apenas
// para que o span execute_tool envolva TODOS os caminhos de retorno via defer.
func (m *Monitor) evaluate(ctx context.Context, call Call) (Decision, error) {
	start := m.now()
	if err := ctx.Err(); err != nil {
		// Contexto já cancelado: fail-closed, sem sequer avaliar. Esta negação é
		// DELIBERADAMENTE não-auditada: gravar no Event Store exigiria o mesmo
		// contexto (já cancelado) e falharia de qualquer forma. É o único caminho
		// de deny sem registo; todos os outros passam por fail() (best-effort).
		d := Decision{Effect: EffectDeny, Code: CodeContextCanceled, DeniedBy: "context", Reason: err.Error(), Latency: m.now().Sub(start)}
		m.metrics.Denials.Add(1)
		return d, err
	}

	// 0) fail-closed de configuração: uma cadeia de hooks vazia NÃO permite tudo.
	//    A ausência de pontos de decisão é tratada como negação (o default seguro
	//    é [DefaultHooks]; [WithHooks] com cadeia vazia é misconfiguração).
	if len(m.hooks) == 0 {
		return m.fail(ctx, call, EffectDeny, CodeEmptyHookChain, "config",
			"cadeia de hooks vazia (fail-closed)", nil, start, ""), nil
	}

	// 1) Cadeia de hooks pela ordem fornecida (ver [WithHooks]; a ordem canónica
	//    identity → policy → budget → egress → audit é a de [DefaultHooks]). O
	//    call é partilhado por ponteiro para permitir resolução de identidade e
	//    propagação de contexto entre hooks.
	var obligations []Obligation
	// policyVersion é a versão de política observada na cadeia (preenchida pelo
	// PDP, AOS-004). Actualiza-se ANTES de tratar deny/escalate para que também a
	// negação de política registe a versão em vigor no evento de mediação.
	var policyVersion string
	for _, h := range m.hooks {
		res, err := safeEvaluate(ctx, h, &call)
		if res.PolicyVersion != "" {
			policyVersion = res.PolicyVersion
		}
		switch {
		case err != nil:
			return m.fail(ctx, call, EffectDeny, CodeHookError, h.Name(), fmt.Sprintf("hook %q: %v", h.Name(), err), nil, start, policyVersion), nil
		case res.Decision == HookDeny:
			reason := res.Reason
			if reason == "" {
				reason = fmt.Sprintf("negado por %q", h.Name())
			}
			// O `nil` AQUI É A DECISÃO, NÃO A METADE QUE FICOU POR FAZER. A escalada logo
			// abaixo propaga `res.Obligations` e a negação não: a assimetria nasceu com o
			// próprio parâmetro (#87), está fixada por `TestNegacaoNaoSelaObrigacoes`
			// (escalada_selada_test.go) e tem gémea no ramo de deny de `paraRM`
			// (control-plane/pdp/rmadapter.go), coberta por `TestNegacaoNaoLevaObrigacoes`.
			//
			// A RAZÃO ESCRITA EM #87 — selar `redact` ou `ttl` numa negação sugeriria que algo
			// foi APLICADO a um efeito que nunca existiu — vale para este caminho, mas NÃO é
			// invariante do modelo de audit, e convém sabê-lo antes de a ir confirmar:
			// `messaging.Verifier.seal` e `hitl.Channel.seal` selam registos `DecisionDeny` COM
			// obrigações, precisamente para carregarem o porquê estruturado, e
			// `compliance.projectHITL` depende disso para contar as negações. Não há
			// contradição — esses escrevem no `audit.Store` directamente, sem passar por esta
			// cadeia — mas a regra é DESTE caminho, não do campo.
			//
			// O QUE FECHA A QUESTÃO AQUI É O TIPO. [Obligation] no RM não é um saco de
			// metadados: é vocabulário FECHADO de imposição, e `enforceObligations` nega
			// fail-closed qualquer `Type` que não saiba cumprir. Um hook que anexasse uma
			// obrigação só para documentar a sua negação estaria a construir um valor que, num
			// permit, NEGA a call. O canal não existe porque o tipo não é esse.
			//
			// CUSTO CONHECIDO, para quem chegar aqui com esse problema: um hook não tem por
			// esta via canal estruturado para anexar informação à SUA negação — só o `Reason`,
			// em texto livre (o `PolicyVersion` viaja mesmo na negação, mas é do hook de
			// política). O AOS-332 (EPIC-23) bateu nisto ao selar a postura do eixo provider e
			// resolveu-o com um sufixo greppável no `Reason`. Mudar isto é mudar os três sítios
			// acima E a semântica do tipo — por decisão escrita, não por simetria aparente com
			// o ramo de baixo.
			return m.fail(ctx, call, EffectDeny, CodeDeniedByHook, h.Name(), reason, nil, start, policyVersion), nil
		case res.Decision == HookEscalate:
			reason := res.Reason
			if reason == "" {
				reason = fmt.Sprintf("escalado por %q", h.Name())
			}
			return m.fail(ctx, call, EffectEscalate, CodeEscalated, h.Name(), reason, res.Obligations, start, policyVersion), nil
		}
		obligations = append(obligations, res.Obligations...)
	}

	// 2) default-deny: a tool tem de estar registada para poder ser despachada.
	m.mu.RLock()
	_, registered := m.tools[call.ToolID]
	m.mu.RUnlock()
	if !registered {
		return m.fail(ctx, call, EffectDeny, CodeToolNotRegistered, "dispatch", "tool nao registada (default-deny)", nil, start, policyVersion), nil
	}

	// 2.5) ENFORCEMENT DE OBRIGAÇÕES ANTES DO EFEITO (AOS-087, AC4). O PEP não só
	//    COLETA as obrigações da cadeia — CUMPRE-AS antes de libertar o efeito:
	//    região cross-border ⇒ deny; redação aplicada aos args (call.Input);
	//    ttl/audit propagados; uma obrigação desconhecida/não-satisfazível ⇒ deny
	//    fail-closed. É genérico sobre o tipo [Obligation] (o RM não importa o PDP).
	//    Corre ANTES do audit-before-effect para que uma violação seja registada como
	//    deny (via fail), e ANTES do dispatch para que nenhum efeito viole a obrigação.
	if reason, ok := enforceObligations(&call, obligations); !ok {
		// O `nil` segue a mesma decisão do ramo `HookDeny` (ver lá o porquê), e é AQUI que ela
		// mais custa: nesta negação as obrigações EXISTEM — foram coletadas e uma delas é a
		// causa da recusa — pelo que o argumento «numa negação não há obrigação» não se aplica.
		// Fica só no `reason`, que as nomeia. Consequência conhecida a jusante:
		// `compliance.sovereigntyRegion` prefere a obrigação `region` e, sem ela, cai no
		// `Resource.Region`. Se isto mudar, muda com o ramo `HookDeny`, não sozinho.
		return m.fail(ctx, call, EffectDeny, CodeObligationUnsatisfied, "obligation", reason, nil, start, policyVersion), nil
	}

	// 3) Auditoria ANTES do efeito (audit-before-effect). Se falhar, fail-closed.
	rec := MediationRecord{
		RequestID: call.RequestID,
		RunID:     call.RunID, StepID: call.StepID, ParentStepID: call.ParentStepID,
		Effect: EffectPermit, ToolID: call.ToolID, Capability: call.Capability,
		Resource: call.Resource, Context: call.Context,
		Principal: call.Principal, Latency: m.now().Sub(start), Obligations: obligations,
		PolicyVersion: policyVersion,
	}
	seq, err := m.sink.RecordMediation(ctx, rec)
	if err != nil {
		// Uma acção não-auditável não é permitida (ADR-002/010).
		d := m.fail(ctx, call, EffectDeny, CodeAuditUnavailable, "audit-sink",
			fmt.Sprintf("%s: %v", ErrAuditUnavailable.msg, err), nil, start, policyVersion)
		return d, nil
	}

	// 4) Permit: mintar o Permit não-forjável e despachar via dispatcher interno. O
	//    despacho devolve TAMBÉM o custo medido do efeito (AOS-212): 0 para uma tool
	//    registada por Register, o valor reportado para uma CostingToolFunc.
	p := m.mint(call)
	out, costMicroUSD, toolErr := m.dispatch(ctx, p, call)

	m.metrics.Permits.Add(1)
	return Decision{
		Effect:       EffectPermit,
		Reason:       "permitido pela cadeia de mediacao",
		Obligations:  obligations,
		Latency:      m.now().Sub(start),
		MediationSeq: seq,
		Output:       out,
		ToolErr:      toolErr,
		CostMicroUSD: costMicroUSD,
		permit:       p,
	}, nil
}

// fail constrói uma Decision de negação/escalonamento, grava o evento
// correspondente (best-effort — a negação nunca deve ser bloqueada por uma
// falha de registo) e actualiza métricas. Nunca despacha a tool.
// O parâmetro `obligations` é EXPLÍCITO e não tem valor por omissão: cada sítio que recusa tem
// de DECIDIR se o registo leva obrigações. É a mesma disciplina do valor-zero das rotas — obrigar
// a escolher em vez de deixar o esquecimento produzir silêncio.
//
// PORQUE ISTO EXISTE. Uma ESCALADA de autonomia carrega uma obrigação `autonomy` com o nível, o
// domínio, o modo de oversight e a classe de risco — a resposta estruturada à pergunta «porquê».
// Ela chegava até aqui e morria: o registo saía com `Obligations: null` e a razão só em TEXTO
// LIVRE. Observado em produção a 2026-08-19: um auditor que percorra obrigações via as
// autorizações e NÃO via as escaladas.
//
// Registar não é impor: no caminho de recusa esta função devolve ANTES de `enforceObligations`,
// que só corre no permit. Acrescentar obrigações ao REGISTO não pode, por construção, mudar o que
// o nó deixa acontecer.
//
// E OBRIGAR A ESCOLHER NÃO É OBRIGAR A PREENCHER. Dos sete sítios que chamam esta função só a
// ESCALADA passa obrigações; os outros seis passam `nil`, e passam-no por decisão. A justificação
// está escrita no ramo `HookDeny` de [Monitor.evaluate], que é onde a escolha é visível e onde
// muda se algum dia mudar — não a deduzas deste parágrafo.
func (m *Monitor) fail(ctx context.Context, call Call, eff Effect, code, deniedBy, reason string, obligations []Obligation, start time.Time, policyVersion string) Decision {
	latency := m.now().Sub(start)
	// Registo best-effort: em deny/escalate o efeito já está bloqueado, pelo que
	// uma falha de auditoria não altera a decisão (contrasta com o permit path).
	//
	// MAS O CTX DO CHAMADOR NÃO PODE CANCELAR ESTE REGISTO (achado de revisão adversarial
	// sobre AOS-311). Este sítio é a metade PÓS-DECISÃO do RM: a negação/escalada JÁ está
	// tomada e o efeito JÁ está bloqueado — o que se escreve aqui é a PROVA de um facto
	// consumado, não a decisão. O audit-before-effect do permit (:348-354, que degrada
	// para deny se o sink falhar) fica INTACTO e continua a herdar o ctx: aí o Append
	// decide se o efeito acontece, e um prazo esgotado TEM de resolver em deny.
	//
	// Sem a separação, um `Mediate` chamado com o ctx do run já cancelado — aborto,
	// shutdown, cliente desligado — bloqueava a acção e não deixava rasto do bloqueio,
	// desde que o AOS-311 pôs o `audit.Store.Append` a respeitar o ctx (o sink de
	// referência do nó é o `audit.RMAdapter`). O `_` do erro tornava a perda silenciosa.
	// Um deny sem registo é indistinguível de uma chamada que nunca aconteceu.
	//
	// `WithoutCancel` preserva os valores do ctx (correlação/tracing) e larga só o
	// cancelamento; o prazo próprio evita que um sink pendurado prenda o RM. Idioma já
	// usado em `packages/integration/budget.go` e `packages/substrate/sandbox/lifecycle.go`.
	regCtx, cancelReg := context.WithTimeout(context.WithoutCancel(ctx), failRecordTimeout)
	defer cancelReg()
	seq, _ := m.sink.RecordMediation(regCtx, MediationRecord{
		RequestID: call.RequestID,
		RunID:     call.RunID, StepID: call.StepID, ParentStepID: call.ParentStepID,
		Effect: eff, Code: code, DeniedBy: deniedBy, Reason: reason,
		ToolID: call.ToolID, Capability: call.Capability,
		Resource: call.Resource, Context: call.Context,
		Principal: call.Principal, Latency: latency,
		PolicyVersion: policyVersion,
		Obligations:   obligations,
	})
	if eff == EffectEscalate {
		m.metrics.Escalations.Add(1)
	} else {
		m.metrics.Denials.Add(1)
	}
	return Decision{
		Effect:       eff,
		Code:         code,
		Reason:       reason,
		DeniedBy:     deniedBy,
		Latency:      latency,
		MediationSeq: seq,
	}
}

// failRecordTimeout é o prazo PRÓPRIO do registo pós-decisão de [Monitor.fail]. Existe
// porque esse registo deixou de herdar o cancelamento do chamador: sem prazo nenhum, um
// sink pendurado prenderia a mediação para sempre. Curto — o caminho de negação é o mais
// quente do RM e não pode ficar refém do trilho.
const failRecordTimeout = 2 * time.Second

// mint emite um Permit não-forjável ligado ao fingerprint do call. Só este
// método (invocado dentro de Mediate) consegue construir um permitToken válido.
func (m *Monitor) mint(call Call) *Permit {
	return &Permit{
		tok: &permitToken{
			fingerprint: fingerprint(call),
			nonce:       m.rand(),
		},
	}
}

// dispatch é o dispatcher INTERNO (não-exportado): o único caminho que executa
// uma tool. Exige um Permit válido — não-nil, com token minta­do por este RM,
// correspondente ao call e ainda não usado (uso único). Código externo não
// consegue construir um Permit aceite aqui nem alcançar este método.
func (m *Monitor) dispatch(ctx context.Context, p *Permit, call Call) ([]byte, int64, error) {
	if p == nil || p.tok == nil {
		return nil, 0, ErrInvalidPermit
	}
	if p.tok.fingerprint != fingerprint(call) {
		return nil, 0, ErrInvalidPermit
	}
	// Uso único: consome o permit atomicamente. Uma segunda tentativa falha.
	if !p.tok.used.CompareAndSwap(false, true) {
		return nil, 0, ErrInvalidPermit
	}
	m.mu.RLock()
	t, ok := m.tools[call.ToolID]
	m.mu.RUnlock()
	if !ok {
		return nil, 0, ErrToolNotRegistered
	}
	// Selector de campo (t.cost/t.fn), não uma [ToolFunc] em ident de âmbito: é o
	// caminho SANCIONADO de execução (archlint reconhece dispatch), e o único.
	if t.cost != nil {
		return t.cost(ctx, call.Input)
	}
	out, err := t.fn(ctx, call.Input)
	return out, 0, err
}

// toolCallHash calcula a âncora ESTÁVEL do span execute_tool (AOS-076), delegando na
// função canónica partilhada [otelgenai.CanonicalToolCallHash] (o módulo folha
// partilhado pelo RM e pelo Agent Runtime). É uma REFERÊNCIA por hash: o Input NUNCA é
// gravado no span (content-capture por referência; os payloads em claro são AOS-079).
//
// AOS-081: a normalização canónica dos args deixa de ser diferida — se o Input é JSON
// válido, é re-serializado em forma canónica (chaves ordenadas, espaço insignificante
// removido; a ordem dos arrays PRESERVA-SE, é semântica) antes do hash, de modo que o
// span passe a carregar a âncora ESTÁVEL de action-dedup (o mesmo hash para args
// semanticamente equivalentes). COMPAT: para args NÃO-JSON o valor é byte-idêntico ao
// pré-AOS-081 (sha256 dos bytes crus com separador nulo).
func toolCallHash(toolID string, args []byte) string {
	return otelgenai.CanonicalToolCallHash(toolID, args)
}

// safeEvaluate invoca um hook com recuperação de panic. Um panic converte-se em
// erro (fail-closed): a mediação nega em vez de propagar a falha.
func safeEvaluate(ctx context.Context, h Hook, call *Call) (res HookResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			res = HookResult{Decision: HookDeny}
			err = fmt.Errorf("%w: %q: %v", ErrHookPanic, h.Name(), r)
		}
	}()
	return h.Evaluate(ctx, call)
}
