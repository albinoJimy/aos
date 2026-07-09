package referencemonitor

// MonitorError é o tipo de erro sentinela do Reference Monitor. Carrega um
// código estável e é comparável com errors.Is por identidade.
type MonitorError struct {
	Code string
	msg  string
}

func (e *MonitorError) Error() string { return e.Code + ": " + e.msg }

// Sentinelas do RM. Todos comparáveis com errors.Is.
var (
	// ErrInvalidPermit — tentativa de despachar uma tool com um Permit forjado,
	// zero, já usado (uso único) ou emitido para outro call. É a manifestação do
	// no-bypass estrutural: só Mediate consegue mintar um permit válido.
	ErrInvalidPermit = &MonitorError{Code: "E_INVALID_PERMIT", msg: "permit forjado, zero, reutilizado ou nao correspondente ao call"}

	// ErrToolNotRegistered — nenhuma tool registada para o ToolID (default-deny).
	ErrToolNotRegistered = &MonitorError{Code: "E_TOOL_NOT_REGISTERED", msg: "tool nao registada no RM (default-deny)"}

	// ErrToolAlreadyRegistered — ToolID já registado (registo é imutável).
	ErrToolAlreadyRegistered = &MonitorError{Code: "E_TOOL_ALREADY_REGISTERED", msg: "tool ja registada"}

	// ErrInvalidRegistration — ToolID vazio ou função nula no Register.
	ErrInvalidRegistration = &MonitorError{Code: "E_INVALID_REGISTRATION", msg: "tool_id vazio ou funcao nula"}

	// ErrHookPanic — um hook entrou em panic; tratado como fail-closed (deny).
	ErrHookPanic = &MonitorError{Code: "E_HOOK_PANIC", msg: "hook panicou; fail-closed"}

	// ErrAuditUnavailable — o registo de auditoria no Event Store falhou num
	// caminho de permit; a acção degrada para deny (uma acção não-auditável não
	// é permitida — ADR-002/010).
	ErrAuditUnavailable = &MonitorError{Code: "E_AUDIT_UNAVAILABLE", msg: "registo de auditoria indisponivel; fail-closed"}
)
