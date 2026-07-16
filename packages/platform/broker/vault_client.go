package broker

import (
	"github.com/aos-ref/platform/broker/internal/vault"
)

// VaultClient é a PORTA do Vault que o broker compõe (reexporta [vault.Client]).
// A implementação real é o vault de infra (EPIC-07); a de referência é
// [NewMemoryVault]. O segredo NUNCA sai do vault senão encapsulado num
// [vault.Secret] opaco, entregue server-side.
type VaultClient = vault.Client

// VaultKey identifica material downstream (não-secreto). Reexporta [vault.Key].
type VaultKey = vault.Key

// GuestSink é o alvo de injecção SERVER-SIDE (o mount de credencial da microVM).
// Reexporta [vault.Sink]. Implementações são infra server-side, NÃO alcançáveis
// pelo contexto do agente.
type GuestSink = vault.Sink

// MemoryVault é o Vault de referência in-memory (secrets vivem aqui, só saem
// encapsulados). Reexporta [vault.Memory]. NUNCA usar em produção.
type MemoryVault = vault.Memory

// NewMemoryVault constrói o Vault de referência in-memory. Aprovisiona segredos
// server-side com [MemoryVault.Put]; nunca são expostos ao agente.
func NewMemoryVault() *MemoryVault { return vault.NewMemory() }
