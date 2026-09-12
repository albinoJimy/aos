package mcp

// Error é o erro sentinela da integração MCP. Código estável, comparável com
// errors.Is. Fail-closed em toda a superfície: qualquer condição de segurança
// ambígua resolve-se por rejeição.
type Error struct {
	Code string
	msg  string
}

func (e *Error) Error() string { return e.Code + ": " + e.msg }

var (
	// ErrTLSRequired — um transporte remoto (SSE/Streamable HTTP) recebeu um endpoint
	// sem TLS (http:// puro ou esquema não-https). TLS é OBRIGATÓRIO nos remotos; o
	// downgrade é recusado fail-closed (EPIC-05 §4).
	ErrTLSRequired = &Error{Code: "E_MCP_TLS_REQUIRED", msg: "transporte remoto exige TLS (https); http:// puro recusado"}

	// ErrEgressBlocked — o endpoint remoto NÃO está na egress allowlist (ADR-004,
	// EPIC-07 AOS-067). Fail-closed: allowlist ausente/vazia nega tudo. Aplicado NÃO
	// só ao endpoint declarado como a CADA hop de redirect HTTP (o alvo efectivo da
	// ligação é re-validado contra a allowlist).
	ErrEgressBlocked = &Error{Code: "E_MCP_EGRESS_BLOCKED", msg: "endpoint fora da egress allowlist (default-deny)"}

	// ErrAuthRequired — um transporte Streamable HTTP foi construído sem credencial de
	// autenticação do host e sem a opção explícita [WithoutAuth]. A auth é OBRIGATÓRIA
	// fail-closed (simétrica a [ErrTLSRequired]/[ErrEgressBlocked]); a sua ausência é
	// recusada em construção (EPIC-05 §4/§4.1).
	ErrAuthRequired = &Error{Code: "E_MCP_AUTH_REQUIRED", msg: "Streamable HTTP exige autenticacao do host (Bearer) ou WithoutAuth() explicito"}

	// ErrInvalidEndpoint — o URL do endpoint remoto é mal-formado.
	ErrInvalidEndpoint = &Error{Code: "E_MCP_INVALID_ENDPOINT", msg: "endpoint remoto mal-formado"}

	// ErrNoLauncher — pediu-se um transporte STDIO sem SandboxLauncher. O STDIO corre
	// SEMPRE em sandbox (ADR-004); sem a porta de lançamento não há execução.
	ErrNoLauncher = &Error{Code: "E_MCP_NO_LAUNCHER", msg: "STDIO exige um SandboxLauncher (sem execucao fora de sandbox)"}

	// ErrHandshakeFailed — o handshake MCP (initialize/tools/list/resources/list)
	// falhou (transporte, protocolo ou resposta inválida).
	ErrHandshakeFailed = &Error{Code: "E_MCP_HANDSHAKE_FAILED", msg: "handshake MCP falhou"}

	// ErrProtocol — uma mensagem JSON-RPC violou o protocolo (versão errada, id não
	// correspondente, corpo ilegível).
	ErrProtocol = &Error{Code: "E_MCP_PROTOCOL", msg: "violacao do protocolo JSON-RPC/MCP"}

	// ErrTransportClosed — o transporte já foi fechado.
	ErrTransportClosed = &Error{Code: "E_MCP_TRANSPORT_CLOSED", msg: "transporte MCP fechado"}

	// ErrNoRegistry — o host foi construído sem Registry; a descoberta não tem onde
	// registar as entradas candidatas (staging).
	ErrNoRegistry = &Error{Code: "E_MCP_NO_REGISTRY", msg: "host MCP sem Registry (descoberta nao pode produzir staging)"}

	// ErrInvalidConnection — a ConnectionInfo da descoberta é mal-formada (server id
	// ou versão em falta).
	ErrInvalidConnection = &Error{Code: "E_MCP_INVALID_CONNECTION", msg: "ConnectionInfo mal-formada"}

	// ErrCapacidadeDuplicada — o manifesto anuncia duas tools com o MESMO nome (ou
	// dois resources com o mesmo URI). Fail-closed (AOS-320): a repetição torna a
	// forma canónica do manifesto ambígua e colide na chave do REG
	// (id = serverID+"/"+nome), pelo que o handshake é recusado em vez de pinar um
	// manifesto com duas leituras possíveis.
	ErrCapacidadeDuplicada = &Error{Code: "E_MCP_CAPACIDADE_DUPLICADA", msg: "manifesto anuncia a mesma capacidade mais do que uma vez"}
)
