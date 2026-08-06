// Package wire é o CONTRATO partilhado entre o orchestrator (host) e o guest-agent (dentro da
// microVM), trocado por vsock. É JSON deliberadamente minimalista: uma tool call entra, um
// resultado sai. O mesmo shape é o que o NÓ envia por HTTP ao orchestrator (o nó define os seus
// próprios structs equivalentes para ficar zero-dep — não importa este módulo).
package wire

// VsockPort é a porta AF_VSOCK onde o guest-agent escuta dentro da microVM. O host liga-se ao
// uds do firecracker e faz "CONNECT <VsockPort>" para chegar aqui.
const VsockPort = 5252

// ToolCall é o subconjunto de sandbox.ToolCall que o guest precisa para executar. O Command é
// FIXO (vem do registry trusted do nó, nunca do modelo); Path/Args/Write são os valores.
type ToolCall struct {
	ToolID  string   `json:"tool_id"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Path    string   `json:"path,omitempty"`
	Write   []byte   `json:"write,omitempty"`
}

// ExecInput é o envelope enviado ao guest (e por HTTP ao orchestrator).
type ExecInput struct {
	RunID  string   `json:"run_id"`
	StepID string   `json:"step_id"`
	Call   ToolCall `json:"call"`
}

// Result é o que o guest devolve (e o orchestrator devolve por HTTP). Stdout é SEMPRE tratado
// como untrusted a jusante (o nó marca-o por tipo em ExecResult).
type Result struct {
	Stdout   []byte `json:"stdout,omitempty"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}
