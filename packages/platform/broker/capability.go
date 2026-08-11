package broker

// PASSO ZERO DE POLÍTICA (AOS-264) — A CAPABILITY DA TROCA.
//
// A troca de credencial é MEDIADA pelo Reference Monitor ([Broker.Exchange]) e cada
// pedido carrega uma [Downstream.Capability]. Essa capability atravessa DOIS gates:
//
//  1. o [ScopeGate] deste pacote (utilizador ∩ classe, AOS-057), na cadeia do RM; e
//  2. a jusante, no nó, a allowlist de capabilities por agent_class do PDP (AOS-007,
//     o bundle ASSINADO `control-plane/pdp/policies/capabilities/allowlist.json`).
//
// DECISÃO REGISTADA — REUTILIZAR `cap:http.post`, NÃO RE-ASSINAR O BUNDLE.
//
// A [Downstream.Capability] é escolhida pelo composition root; não é fixa no código.
// O bundle assinado JÁ concede `cap:http.post` (e `cap:fs.read`) à classe
// `agent-worker`. Declarar a troca de credencial de egress sob `cap:http.post`
// satisfaz LITERALMENTE o permit já assinado — a exfiltração de uma credencial
// downstream para um endpoint externo É um POST HTTP autenticado, pelo que a
// capability é semanticamente correcta, não um encaixe oportunista. Isto evita uma
// cerimónia de re-assinatura do bundle para a v1. Uma capability DEDICADA da troca
// (ex.: `cap:credential.exchange`) é o desenho-alvo quando o bundle for reeditado;
// até lá, o reuso é a decisão, e está TESTADO (aos264_capability_reuse_test.go).
//
// A CONSEQUÊNCIA DE SEGURANÇA que isto torna explícita: a troca só passa se o
// principal tiver `cap:http.post` na sua autoridade EFECTIVA (utilizador ∩ classe).
// Um principal de classe `agent-reader` (só `cap:fs.read`) NÃO pode trocar por
// credenciais — é negado fail-closed pelo [ScopeGate] ANTES do Vault. É o passo zero:
// sem identidade com a capability certa, nenhuma troca acontece (e, sem troca, os
// turnos que dela dependessem falham RUIDOSAMENTE — nunca com bearer vazio).

// ExchangeCapabilityHTTPPost é a capability sob a qual a troca de credencial de
// egress se declara na v1: `cap:http.post`, JÁ concedida à classe `agent-worker` no
// bundle assinado (AOS-007). É o valor a pôr em [Downstream.Capability] quando o
// composition root liga a troca (AOS-265), enquanto não houver capability dedicada.
const ExchangeCapabilityHTTPPost = "cap:http.post"

// ClassAgentWorker é a classe de agente cuja autoridade assinada inclui
// [ExchangeCapabilityHTTPPost] — a única das classes committadas que pode trocar por
// credenciais de egress na v1 (`agent-reader` só tem `cap:fs.read`; `agent-break-glass`
// tem `*`). Espelha `capabilities/allowlist.json`; não o redefine.
const ClassAgentWorker = "agent-worker"
