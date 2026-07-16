package messaging

import (
	"context"
	"crypto/ed25519"
)

// Signer assina server-side com a chave privada ed25519 da NHI do emissor. É a
// PORTA do broker/Vault (AOS-070, ADR-006): a chave privada é gerida server-side e
// NUNCA entra neste módulo nem no runtime do agente. Sign recebe o digest canónico
// e devolve SÓ a assinatura — espelha o padrão do Vault ([vault.Secret.DeliverTo]
// devolve apenas erro), mantendo o segredo do lado do custodiante.
//
// A implementação de referência (testes) usa seeds efémeras; produção liga o
// broker/Vault, que detém o material e assina sem o expor.
type Signer interface {
	// Sign assina a serialização canónica message com a chave privada da NHI nhi e
	// devolve a assinatura ed25519. Fail-closed: sem material para a NHI (ou
	// custodiante indisponível) devolve erro e a mensagem não é assinada.
	Sign(ctx context.Context, nhi string, message []byte) ([]byte, error)
}

// NHIRegistry resolve a NHI CLAMADA como origem para a sua chave pública PINADA e
// a sua autoridade AUTORITATIVA. É a PORTA da identidade (AOS-005/006): a chave
// pública pinada é a ÚNICA âncora de origem (um emissor forjado não valida contra
// ela); a autoridade autoritativa é o escopo REGISTADO da NHI — a fonte de verdade
// da autoridade, NÃO o campo Authority auto-declarado na mensagem.
type NHIRegistry interface {
	// Lookup devolve a chave pública pinada e a autoridade autoritativa da NHI.
	// ok=false quando a NHI é desconhecida (origem não autenticável → fail-closed).
	// Um erro de backend é propagado (tratado fail-closed pelo Verifier).
	Lookup(ctx context.Context, nhi string) (pub ed25519.PublicKey, authority []string, ok bool, err error)
}

// ReferenceResolver prova que o item referenciado por uma mensagem EXISTE e é
// AUTÊNTICO. É a fronteira que eleva "o ID existe" (o hallucination gate antigo) a
// "a referência é autêntica": devolve o hash de conteúdo AUTÊNTICO do item, que o
// Verifier reconcilia contra o hash coberto pela assinatura.
type ReferenceResolver interface {
	// Resolve devolve o hash de conteúdo autêntico do item id. ok=false quando o
	// item não existe (referência fabricada → fail-closed). Um erro de backend é
	// propagado (tratado fail-closed pelo Verifier).
	Resolve(ctx context.Context, id string) (hash []byte, ok bool, err error)
}
