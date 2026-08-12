package broker

import (
	"github.com/aos-ref/platform/broker/internal/vault"
)

// Este ficheiro REEXPORTA o cliente Vault REAL (KV v2) para o composition root do
// nó, tal como [NewMemoryVault] reexporta o Vault de referência. O cliente vive em
// [internal/vault] (única fronteira onde o [vault.Secret] opaco se constrói); daqui
// só sai a PORTA [VaultClient]. Ver [internal/vault.KVv2] para a decisão registada
// KV v2 vs dynamic secrets (só dynamic dá corte downstream real; a v1 in-process
// corta na injecção).

// VaultKVv2Config parametriza o cliente Vault REAL (KV v2). Reexporta
// [vault.KVv2Config]. Addr/Token são obrigatórios; o Token é lido de FICHEIRO
// montado por quem chama (material privado nunca por variável de ambiente).
type VaultKVv2Config = vault.KVv2Config

// NewVaultKVv2 constrói o cliente Vault REAL (KV v2, stdlib net/http) por detrás da
// porta [VaultClient]. Fail-closed se a config for inválida ([vault.ErrKVConfig]).
// Zero-dep (ADR-017): não importa o SDK Go do Vault. É a alternativa de PRODUÇÃO ao
// [NewMemoryVault] de referência — o segredo vive no Vault e sai só encapsulado.
func NewVaultKVv2(cfg VaultKVv2Config) (VaultClient, error) {
	return vault.NewKVv2(cfg)
}
