package wirefake

import (
	_ "embed"
	"encoding/json"
)

// Comportamento é o que o gateway faz a um caso: um turno aceite, ou a resposta recusada.
type Comportamento struct {
	// Desfecho é `turno` (a resposta deu um turno) ou `recusada` (a resposta inteira foi
	// recusada e o turno falhou).
	Desfecho string `json:"desfecho"`
	// Erro é a CAUSA de uma resposta recusada, no vocabulário fechado do gateway
	// (`json_invalido`, `sem_choices`, `content_parte_nao_texto`, …) — e não o texto do erro,
	// que no caso do `encoding/json` muda com a versão do toolchain.
	Erro string `json:"erro,omitempty"`
	// O turno, tal como o runtime o recebe.
	Text       string          `json:"text"`
	ToolCalls  []ChamadaDeTool `json:"tool_calls,omitempty"`
	StopReason string          `json:"stop_reason"`
	Final      bool            `json:"final"`
	Reasoning  string          `json:"reasoning,omitempty"`
	Model      string          `json:"model,omitempty"`
	// Os tokens do turno.
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	CacheReadTokens int64 `json:"cache_read_tokens,omitempty"`
	UsageAusente    bool  `json:"usage_ausente,omitempty"`
}

// ChamadaDeTool é uma tool call de um turno: o nome e os bytes dos argumentos.
type ChamadaDeTool struct {
	Tool  string `json:"tool"`
	Input string `json:"input"`
}

// Desfechos de [Comportamento.Desfecho].
const (
	DesfechoTurno    = "turno"
	DesfechoRecusada = "recusada"
)

//go:embed linha_de_base_aos508.json
var linhaDeBase []byte

//go:embed comportamento.json
var comportamento []byte

// LinhaDeBase é o que o gateway fazia a cada caso ANTES do AOS-509: gerada na base desse ticket
// e CONGELADA. É contra ela que se prova que as respostas que já passavam ficaram byte a byte.
func LinhaDeBase() map[string]Comportamento { return ler(linhaDeBase) }

// ComportamentoDeHoje é o que o gateway faz HOJE a cada caso, preso por teste
// (TestAOS508_OQueOGatewayFazACadaCaso): muda no mesmo commit que mudar o gateway.
func ComportamentoDeHoje() map[string]Comportamento { return ler(comportamento) }

func ler(b []byte) map[string]Comportamento {
	out := map[string]Comportamento{}
	if err := json.Unmarshal(b, &out); err != nil {
		panic("wirefake: registo ilegivel: " + err.Error())
	}
	return out
}
