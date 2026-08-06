package sandbox

// ADAPTADOR args-do-modelo → ExecRequest (AOS-005, a peça de execução que faltava).
//
// O modelo produz argumentos OPACOS por tool call (ex.: {"doc_id":"notes"}); a sandbox executa um
// [ExecRequest] estruturado ([ToolCall] com Command/Path/Args/Write). Esta peça faz a ponte de
// forma DECLARATIVA e por-tool ([SandboxBinding]) — sem código por tool, sem o modelo poder
// escolher o Command (que vem SEMPRE da config trusted, nunca dos args do modelo: os args untrusted
// só preenchem VALORES nos slots que a binding autoriza). É o análogo, no eixo de execução, do
// [toolBinding] do registry que mapeia a capability/recurso para a mediação.

import (
	"encoding/json"
	"fmt"
)

// SandboxBinding declara COMO os args (untrusted) de uma tool call viram um [ToolCall] da sandbox.
// O Command é FIXO (trusted, da config) — o modelo nunca o escolhe. Os campos *Arg nomeiam qual
// chave dos args do modelo preenche cada slot; vazios ⇒ slot não usado.
type SandboxBinding struct {
	// Command é o comando FIXO executado no guest (trusted). Obrigatório.
	Command string
	// PathArg nomeia a chave dos args cujo valor (string) vira [ToolCall.Path]. Opcional.
	PathArg string
	// ArgsFrom nomeia, por ordem, as chaves dos args cujos valores (string) viram [ToolCall.Args].
	ArgsFrom []string
	// WriteArg nomeia a chave dos args cujo valor (string) vira [ToolCall.Write] (bytes). Opcional.
	WriteArg string
}

// ErrBadSandboxArgs — os args do modelo não são um objecto JSON, ou falta uma chave que a binding
// exige. Fail-closed: uma tool call malformada não produz um ExecRequest ambíguo.
var ErrBadSandboxArgs = fmt.Errorf("sandbox: args da tool call invalidos para a binding (fail-closed)")

// BuildExecRequest constrói o [ExecRequest] a partir dos args OPACOS do modelo segundo a binding
// TRUSTED. O Command vem SEMPRE da binding; os args do modelo só preenchem Path/Args/Write nos
// slots nomeados. Fail-closed: args não-objecto, Command vazio, ou uma chave nomeada ausente/não-string.
func BuildExecRequest(runID, stepID, toolID string, modelArgs []byte, b SandboxBinding) (ExecRequest, error) {
	if b.Command == "" {
		return ExecRequest{}, fmt.Errorf("%w: Command da binding vazio", ErrBadSandboxArgs)
	}
	var args map[string]any
	if err := json.Unmarshal(modelArgs, &args); err != nil {
		return ExecRequest{}, fmt.Errorf("%w: %v", ErrBadSandboxArgs, err)
	}
	getStr := func(key string) (string, error) {
		v, ok := args[key]
		if !ok {
			return "", fmt.Errorf("%w: falta a chave %q nos args", ErrBadSandboxArgs, key)
		}
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("%w: a chave %q nao e string", ErrBadSandboxArgs, key)
		}
		return s, nil
	}

	call := ToolCall{ToolID: toolID, Command: b.Command}
	if b.PathArg != "" {
		p, err := getStr(b.PathArg)
		if err != nil {
			return ExecRequest{}, err
		}
		call.Path = p
	}
	for _, key := range b.ArgsFrom {
		a, err := getStr(key)
		if err != nil {
			return ExecRequest{}, err
		}
		call.Args = append(call.Args, a)
	}
	if b.WriteArg != "" {
		w, err := getStr(b.WriteArg)
		if err != nil {
			return ExecRequest{}, err
		}
		call.Write = []byte(w)
	}
	return ExecRequest{RunID: runID, StepID: stepID, Call: call}, nil
}
