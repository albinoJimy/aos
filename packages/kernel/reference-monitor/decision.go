package referencemonitor

import (
	"sync/atomic"
	"time"
)

// Effect é o veredicto de uma mediação (contrato C1, tecnica/12 §4).
type Effect string

const (
	// EffectPermit — a tool call é autorizada e foi despachada.
	EffectPermit Effect = "permit"
	// EffectDeny — a tool call é negada (fail-closed); nenhum efeito ocorre.
	EffectDeny Effect = "deny"
	// EffectEscalate — requer gate humano (ADR-013); nenhum efeito ocorre até aprovação.
	EffectEscalate Effect = "escalate"
)

// Códigos estáveis de decisão expostos em [Decision.Code]. São contrato: um
// chamador pode ramificar por igualdade sem fazer parse de strings livres. Os
// que espelham um sentinela ([MonitorError.Code]) reutilizam o mesmo valor.
const (
	// CodePermit — decisão permit (código vazio por convenção).
	CodePermit = ""
	// CodeContextCanceled — negado porque o contexto já estava cancelado.
	CodeContextCanceled = "E_CONTEXT_CANCELED"
	// CodeEmptyHookChain — negado porque a cadeia de hooks está vazia
	// (misconfiguração fail-closed; ver [WithHooks]).
	CodeEmptyHookChain = "E_EMPTY_HOOK_CHAIN"
	// CodeDeniedByHook — um hook devolveu deny.
	CodeDeniedByHook = "E_DENIED_BY_HOOK"
	// CodeEscalated — um hook requereu gate humano (escalate).
	CodeEscalated = "E_ESCALATED"
	// CodeHookError — um hook devolveu erro ou entrou em panic (fail-closed).
	CodeHookError = "E_HOOK_ERROR"
	// CodeToolNotRegistered — tool não registada (default-deny). Espelha
	// [ErrToolNotRegistered].Code.
	CodeToolNotRegistered = "E_TOOL_NOT_REGISTERED"
	// CodeAuditUnavailable — falha de auditoria no caminho de permit; degradou
	// para deny. Espelha [ErrAuditUnavailable].Code.
	CodeAuditUnavailable = "E_AUDIT_UNAVAILABLE"
	// CodeObligationUnsatisfied — uma obrigação da decisão não pôde ser cumprida
	// pelo PEP antes do efeito (região cross-border, redação não-garantida, ou uma
	// obrigação de tipo desconhecido). Fail-closed: uma obrigação que o PEP não
	// sabe/consegue impor NÃO liberta o efeito (AOS-087, ADR-002).
	CodeObligationUnsatisfied = "E_OBLIGATION_UNSATISFIED"
)

// Obligation é uma obrigação que o PEP deve impor sobre uma decisão permit
// (ex.: redigir PII, TTL, nível de audit). No AOS-003 os stubs não produzem
// obrigações; o contrato existe para AOS-004 as devolver.
type Obligation struct {
	Type   string            // ex.: "redact_pii", "audit", "ttl"
	Fields []string          // ex.: ["email", "phone"]
	Params map[string]string // parâmetros genéricos (ex.: {"seconds": "3600"})
}

// HookLatency é a duração de um hook da cadeia de política numa mediação (AOS-405).
type HookLatency struct {
	// Hook é o [Hook.Name] do hook.
	Hook string
	// Latency é o tempo de [Hook.Evaluate] desse hook, incluindo o que ele próprio escreve (o
	// hook de revalidação sela no WORM dentro desta janela).
	Latency time.Duration
}

// Decision é o resultado de [Monitor.Mediate]. É sempre devolvida (mesmo em
// negação): fail-closed produz uma Decision Deny, nunca a ausência de resposta.
type Decision struct {
	// Effect é o veredicto final.
	Effect Effect
	// Code é um código estável e legível-por-máquina da decisão (ver as
	// constantes Code*). Permite ao chamador ramificar programaticamente sem
	// fazer parse de Reason — ex.: distinguir CodeAuditUnavailable de
	// CodeDeniedByHook. Vazio (CodePermit) num permit.
	Code string
	// Reason descreve a decisão (motivo de negação ou de permissão).
	Reason string
	// DeniedBy é o nome do hook que negou/escalou (vazio em permit).
	DeniedBy string
	// Obligations são as obrigações a impor (só relevantes em permit).
	Obligations []Obligation
	// Latency é o tempo total de mediação (avaliação + registo + despacho).
	Latency time.Duration
	// DecisionLatency é o tempo da CADEIA DE DECISÃO apenas — avaliação de política,
	// obrigações e registo pré-efeito — EXCLUINDO a janela do despacho da tool.
	//
	// Num permit é ESTRITAMENTE MENOR que [Latency]: fecha depois de o selo pré-efeito
	// `tool.call.mediated` estar durável e ANTES de a tool ser despachada.
	// Numa recusa ou escalada é IGUAL a [Latency] — esses caminhos não despacham nada.
	//
	// É esta, e não [Latency], que exprime o custo que a mediação ACRESCENTA, e é a que
	// o SLO de overhead de mediação (p95 < 15 ms) mede. Confundi-las fazia o SLI reportar
	// a duração da execução no sandbox — 0,6–1,8 s em gVisor — como overhead de decisão,
	// e disparar um `critical` em qualquer nó com tráfego real (DEF-281, fechado por AOS-398).
	DecisionLatency time.Duration
	// PolicyLatency é a janela da CADEIA DE POLÍTICA: identidade, PDP, orçamento, egress e
	// obrigações — até imediatamente ANTES da escrita do selo de auditoria. É o mesmo instante
	// que o `latency_ns` do selo `tool.call.mediated` regista, e é ESTA a janela que o SLO de
	// overhead de mediação (p95 < 15 ms) governa (AOS-401, emenda ao ADR-026 §1).
	//
	// Existe porque [DecisionLatency] inclui a escrita durável do selo: a 2026-09-16, com a
	// v0.1.15, a janela da decisão mediu 30,8–32,7 ms em produção. O SLO voltava a disparar. A
	// diferença atribuiu-se à escrita por inferência; [AuditWriteLatency] é a medida directa que
	// faltava. O AOS-404 decompôs esses valores: duas calls lentas em todos os troços ao mesmo tempo,
	// num p95 de poucas amostras. A janela da política inclui o selo durável da revalidação (AOS-381).
	PolicyLatency time.Duration
	// AuditWriteLatency é a duração da escrita do selo de mediação no sink (Event Store/WORM).
	// Num permit está no caminho crítico — o efeito espera por ela — e soma com [PolicyLatency]
	// para dar [DecisionLatency]. Observável, sem SLO: nenhum alvo foi ratificado para o custo
	// de um sink durável, e é por não o haver que ela deixou de contar para os 15 ms.
	AuditWriteLatency time.Duration
	// HookLatencies é a duração de CADA hook que correu, pela ordem da cadeia (AOS-405). Somam-se
	// dentro de [PolicyLatency]; o resto da política é o próprio RM (registo da tool e imposição de
	// obrigações). Numa recusa ou escalada só estão os hooks até ao que decidiu, esse incluído; na
	// recusa por contexto cancelado, antes de correr qualquer hook, é nil. Observável, sem SLO.
	HookLatencies []HookLatency
	// MediationSeq é o seq do evento de mediação no Event Store (0 se o registo
	// não produziu seq).
	MediationSeq uint64
	// Output é o resultado devolvido pela tool despachada (só em permit).
	Output []byte
	// ToolErr é o erro devolvido pela tool despachada, se houver (só em permit).
	// Nota: um erro DA TOOL não é uma negação de política — a decisão foi Permit
	// e o efeito ocorreu; ToolErr reporta a falha de execução downstream.
	ToolErr error
	// CostMicroUSD é o custo MEDIDO do efeito real em micro-USD inteiro, tal como
	// REPORTADO pela tool despachada no momento da execução (só em permit; 0 quando a
	// tool não mede custo, o caso das tools de referência de produção — ver AOS-212 /
	// DEF-810). É um SINAL DE OBSERVABILIDADE do desfecho, não estado de decisão: o
	// Agent Runtime lê-o em tempo de Apply para anotar o span aos.activity a partir do
	// DESFECHO do efeito (não do Activity de entrada) e NUNCA o grava no durable.Result
	// do ledger — assim replay/dedup, que não re-incorrem o efeito, emitem ZERO custo.
	// Uma tool reporta-o via [Monitor.RegisterCosting]; via [Monitor.Register] é sempre 0.
	CostMicroUSD int64

	// permit é o token não-forjável emitido só em Permit (nil caso contrário).
	// É não-exportado: código externo não o consegue construir nem inspeccionar,
	// o que sustenta o no-bypass estrutural.
	permit *Permit
}

// Permitted indica se a decisão autorizou (e despachou) a tool call, com um
// permit válido associado.
func (d Decision) Permitted() bool {
	return d.Effect == EffectPermit && d.permit != nil && d.permit.tok != nil
}

// permitToken é o segredo não-forjável de um Permit. Todos os campos são
// não-exportados e o tipo é não-exportado: nenhum pacote externo consegue
// construir um permitToken válido, logo nenhum consegue forjar um Permit
// aceite por dispatch.
type permitToken struct {
	fingerprint uint64      // liga o permit ao call que o originou
	nonce       uint64      // valor único por emissão (evidência anti-replay)
	used        atomic.Bool // uso único: dispatch consome via CompareAndSwap
}

// Permit é a prova não-forjável de que uma tool call foi autorizada por
// Mediate. Só o pacote reference-monitor consegue mintar um Permit com token
// válido (ver Monitor.mint). Um Permit{} zero (ou qualquer um construído fora
// deste pacote) tem tok == nil e é rejeitado por dispatch com [ErrInvalidPermit].
//
// A ligação ao call que o originou vive no fingerprint do token (permitToken.
// fingerprint), validado por dispatch — não é preciso embeber o Call.
type Permit struct {
	tok *permitToken
}
