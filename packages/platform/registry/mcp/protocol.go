package mcp

import (
	"encoding/json"
	"fmt"
)

// jsonRPCVersion é a versão do protocolo JSON-RPC 2.0 usada por TODOS os transportes
// MCP. O MCP é JSON-RPC 2.0 puro — este pacote usa apenas encoding/json (stdlib).
const jsonRPCVersion = "2.0"

// protocolVersion é a versão do MCP que o host anuncia no initialize. É uma data
// (esquema de versionamento do MCP); mantém-se estável e explícita.
const protocolVersion = "2025-06-18"

// Métodos do handshake MCP (EPIC-05 §4). O host invoca-os por esta ordem:
// initialize estabelece a sessão; tools/list e resources/list descobrem as
// capabilities. Tudo o que voltar é conteúdo untrusted.
const (
	methodInitialize    = "initialize"
	methodToolsList     = "tools/list"
	methodResourcesList = "resources/list"
)

// rpcRequest é um pedido JSON-RPC 2.0. O ID é numérico monotónico por transporte
// (injectável/determinista); Params é opaco (marshalled pelo chamador).
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse é uma resposta JSON-RPC 2.0. Result e Error são mutuamente exclusivos
// (JSON-RPC): uma resposta bem-sucedida traz Result; uma falha traz Error.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError é o objecto de erro JSON-RPC 2.0 devolvido pelo servidor.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// newRequest constrói um pedido JSON-RPC 2.0 já serializado para envio. params nil
// é omitido. O id é atribuído pelo transporte (contador monotónico).
func newRequest(id int64, method string, params any) ([]byte, error) {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params de %s: %w", method, err)
		}
		raw = b
	}
	return json.Marshal(rpcRequest{
		JSONRPC: jsonRPCVersion,
		ID:      id,
		Method:  method,
		Params:  raw,
	})
}

// ClientInfo identifica o HOST (o AOS) no handshake initialize. Não contém segredos.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// initializeParams são os parâmetros do initialize enviados pelo host.
type initializeParams struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	ClientInfo      ClientInfo      `json:"clientInfo"`
}

// ServerInfo identifica o servidor MCP (nome + versão anunciados). É informativo —
// a identidade de confiança/versionamento canónica vive no REG (AOS-045), não no que
// o servidor afirma de si.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// initializeResult é o resultado do initialize devolvido pelo servidor.
type initializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	ServerInfo      ServerInfo      `json:"serverInfo"`
}

// Tool é uma tool anunciada por um servidor MCP (tools/list). O Name, a Description
// e o InputSchema são conteúdo controlado por TERCEIROS — UNTRUSTED (ADR-005): são
// dados taint-marcados, nunca instruções.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// toolsListResult é o resultado de tools/list.
type toolsListResult struct {
	Tools []Tool `json:"tools"`
}

// Resource é um resource anunciado por um servidor MCP (resources/list). A
// Description/URI são igualmente UNTRUSTED.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType,omitempty"`
}

// resourcesListResult é o resultado de resources/list.
type resourcesListResult struct {
	Resources []Resource `json:"resources"`
}

// CapabilityManifest é o manifesto de capabilities devolvido pelo handshake MCP
// (initialize + tools/list + resources/list). Alimenta o contract das entradas do
// REG. TODO o seu conteúdo textual (descrições, schemas) é UNTRUSTED.
//
// O campo Digest é PREENCHIDO pelo [Host.Handshake] (AOS-320): é o SHA-256 do
// manifesto CANONICALIZADO, e é dele que deriva o Digest da entrada kind=mcp_server
// — ver [digestManifesto] e [digestAncorado].
type CapabilityManifest struct {
	// ServerInfo é o que o servidor afirma sobre si (informativo).
	ServerInfo ServerInfo
	// ProtocolVersion negociada no initialize.
	ProtocolVersion string
	// Tools e Resources descobertos. Conteúdo untrusted.
	Tools     []Tool
	Resources []Resource
	// ResourcesUnavailable é true quando o resources/list FALHOU (o servidor devolveu
	// erro nesse método), distinguindo-o de um servidor legitimamente SEM resources
	// (Resources vazio, este campo false). Torna explícita a supressão que, de outro
	// modo, seria um fail-open silencioso.
	ResourcesUnavailable bool
	// Digest é o SHA-256 (prefixado, digest.Prefix) da forma canónica do manifesto,
	// calculado por [digestManifesto] com digest.DigestJSON. É a impressão digital da
	// superfície de capacidade anunciada: dois servidores que anunciem superfícies
	// diferentes têm digests diferentes, e o mesmo manifesto reproduz sempre o mesmo
	// valor. NÃO é derivado de conteúdo secreto — é público, como qualquer digest de
	// supply-chain.
	Digest string
}
