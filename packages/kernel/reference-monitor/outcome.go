package referencemonitor

import (
	"context"
	"time"
)

// outcome.go — o DESFECHO de uma tool call, registado DEPOIS do efeito (ADR-025).
//
// # Porque existe, e porque é distinto do MediationRecord
//
// O [MediationRecord] sela a DECISÃO do PDP ANTES do efeito (audit-before-effect): diz que a
// call foi PERMITIDA, não que CORREU bem. É cego ao erro de EXECUÇÃO — uma tool que rebenta
// tem exactamente o mesmo selo de mediação de uma que corre. A promoção de autonomia (AOS-090)
// precisa de fiabilidade MEDIDA (taxa de erro sustentada), e essa só se lê do DESFECHO.
//
// Este registo é a metade pós-efeito, e a sua semântica de falha é OPOSTA à do selo de decisão:
// falhar o selo de decisão degrada o permit para DENY (fail-closed de enforcement); falhar o
// desfecho degrada só a MEDIÇÃO (fail-open) — o efeito já aconteceu, e recusá-lo depois não faz
// sentido. Ver [Monitor.recordOutcome].

// Desfechos possíveis de uma tool call executada.
const (
	// OutcomeOK — a tool correu sem erro.
	OutcomeOK = "ok"
	// OutcomeError — a tool devolveu erro na execução (não a decisão do PDP — essa é o selo).
	OutcomeError = "error"
)

// OutcomeRecord é o desfecho pós-efeito de uma tool call permitida. Rótulos e números, NUNCA
// segredos nem o input/output da tool — só o suficiente para agregar fiabilidade por (classe,
// domínio).
type OutcomeRecord struct {
	RunID      string
	StepID     string
	ToolID     string
	Capability string
	Resource   Resource
	AgentClass string // a classe do agente — a unidade estável sobre a qual a fiabilidade agrega
	Outcome    string // OutcomeOK | OutcomeError
	ErrorKind  string // classe do erro quando Outcome==OutcomeError (ex.: "tool_error"); vazio em ok
	Latency    time.Duration
}

// OutcomeSink regista o desfecho pós-efeito de uma tool call. Semântica FAIL-OPEN na medição:
// um erro devolvido NÃO muda decisão nenhuma (o efeito já aconteceu) — o [Monitor] engole-o.
// Contrasta com [EventSink.RecordMediation], cujo erro no permit degrada a decisão para deny.
// nil ⇒ o RM não regista desfechos (comportamento anterior a ADR-025; zero overhead).
type OutcomeSink interface {
	RecordOutcome(ctx context.Context, rec OutcomeRecord) error
}
