package sandbox

import "context"

// CredentialInjector resolve um handle OPACO para uma credencial downstream e
// INJECTA-A server-side na microVM (ADR-006). O contrato é: o segredo NUNCA
// regressa ao chamador, NUNCA entra no [ExecResult], nos eventos ou nos spans — a
// sandbox recebe o handle (um id), o injector resolve-o do lado seguro e coloca a
// credencial no interior do guest. AOS-070 entrega o broker/vault real; esta porta
// fixa a fronteira e prova a invariante "handle, nunca segredo".
//
// Nota de escopo (AOS-064): o injector é OPCIONAL. Sem injector configurado, o
// handle é apenas propagado (opaco) e correlacionado no audit — nenhuma resolução
// de segredo acontece neste ticket.
type CredentialInjector interface {
	// Inject resolve o handle e injecta a credencial server-side na instância. Um
	// handle vazio é no-op. Devolve erro se o handle for desconhecido/negado
	// (fail-closed), sem NUNCA expor o segredo no erro.
	Inject(ctx context.Context, handle string, inst Instance) error
}
