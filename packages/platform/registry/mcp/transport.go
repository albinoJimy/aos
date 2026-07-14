package mcp

import (
	"context"
	"encoding/json"
)

// TransportKind identifica um dos três transportes MCP (EPIC-05 §4). É gravado nos
// spans e no diagnóstico.
type TransportKind string

const (
	// TransportSTDIO — servidor local como subprocesso em sandbox (ADR-004).
	TransportSTDIO TransportKind = "stdio"
	// TransportSSE — servidor remoto legado (streaming HTTP); TLS + egress allowlist.
	TransportSSE TransportKind = "sse"
	// TransportStreamableHTTP — transporte remoto recomendado (request/response +
	// streaming num único endpoint); TLS + auth do host + sessões.
	TransportStreamableHTTP TransportKind = "streamable_http"
)

// Transport é a PORTA uniforme de um canal MCP. Abstrai o round-trip JSON-RPC 2.0
// para que o handshake (initialize/tools/list/resources/list) seja idêntico nos três
// transportes. As implementações são [stdioTransport], [sseTransport] e
// [streamableHTTPTransport].
//
// Call é sequencial e seguro para uso pelo handshake (uma chamada de cada vez por
// transporte); o id JSON-RPC é atribuído internamente por um contador monotónico.
type Transport interface {
	// Kind devolve o tipo de transporte.
	Kind() TransportKind
	// Call executa um round-trip JSON-RPC: envia method+params e devolve o Result
	// bruto (ou um erro — de transporte, de protocolo, ou o rpcError do servidor).
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
	// Close liberta os recursos do transporte (subprocesso, ligação HTTP).
	Close() error
}

// SandboxProcess é um servidor MCP local JÁ LANÇADO em isolamento pela porta
// [SandboxLauncher]. Expõe apenas o stdio enquadrado (JSON-RPC newline-delimited) —
// NENHUM socket do host, nenhum descritor extra. O ciclo de vida (terminar/reap) é
// de Close.
type SandboxProcess interface {
	// Stdin é o canal de escrita para o servidor (pedidos JSON-RPC).
	Stdin() interface{ Write([]byte) (int, error) }
	// Stdout é o canal de leitura do servidor (respostas JSON-RPC).
	Stdout() interface{ Read([]byte) (int, error) }
	// Close termina o processo e liberta os recursos.
	Close() error
}

// LaunchSpec descreve o servidor MCP local a lançar. Deliberadamente SEM ambiente do
// host por omissão: Env vazio significa que o processo NÃO herda o ambiente do host
// (sem segredos, sem sockets via variáveis). É a porta que decide o isolamento real.
type LaunchSpec struct {
	// Command é o binário do servidor (um artefacto do supply-chain — o pin+hash do
	// binário é AOS-047; aqui só se lança).
	Command string
	// Args são os argumentos do servidor.
	Args []string
	// Env é o ambiente EXPLÍCITO do processo. Vazio = nenhum (fail-closed: o host não
	// vaza o seu ambiente para dentro da sandbox).
	Env []string
}

// SandboxLauncher é a PORTA de isolamento (ADR-004) que lança um servidor MCP local
// DENTRO de uma sandbox — o substrato microVM de EPIC-07 (AOS-064) implementá-la-á.
// O contrato é: o processo NUNCA tem acesso ao socket do host; recebe apenas stdio
// enquadrado. Este pacote fornece [OSSandboxLauncher] como impl de referência que
// documenta a fronteira; NÃO se reimplementa a microVM.
type SandboxLauncher interface {
	// Launch inicia o servidor isolado e devolve o processo com stdio enquadrado.
	Launch(ctx context.Context, spec LaunchSpec) (SandboxProcess, error)
}

// EgressAllowlist é a PORTA de egress (ADR-004, EPIC-07 AOS-067): decide se o host
// pode ligar a um endpoint remoto. Fail-closed por contrato: um host ausente da
// allowlist (ou uma allowlist vazia/nil) é NEGADO. Este pacote fornece
// [StaticEgressAllowlist] e [DenyAllEgress] como impls de referência.
type EgressAllowlist interface {
	// Allowed indica se o egress para o host (sem porta) é permitido.
	Allowed(host string) bool
}

// StaticEgressAllowlist é uma allowlist declarativa por conjunto de hosts. É
// fail-closed: um host ausente é negado, e o zero-value (conjunto nil) nega tudo.
type StaticEgressAllowlist struct {
	hosts map[string]struct{}
}

// NewStaticEgressAllowlist constrói uma allowlist com os hosts dados. Sem hosts =
// nega tudo (default-deny).
func NewStaticEgressAllowlist(hosts ...string) *StaticEgressAllowlist {
	set := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		if h != "" {
			set[h] = struct{}{}
		}
	}
	return &StaticEgressAllowlist{hosts: set}
}

// Allowed implementa [EgressAllowlist].
func (a *StaticEgressAllowlist) Allowed(host string) bool {
	if a == nil || len(a.hosts) == 0 || host == "" {
		return false
	}
	_, ok := a.hosts[host]
	return ok
}

// DenyAllEgress é a allowlist que nega TODO o egress. É o default seguro quando
// nenhuma allowlist é configurada (fail-closed explícito).
type DenyAllEgress struct{}

// Allowed implementa [EgressAllowlist] — sempre false.
func (DenyAllEgress) Allowed(string) bool { return false }
