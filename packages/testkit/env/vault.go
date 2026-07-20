package env

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
)

// ===========================================================================
// FakeVault — vault de TESTE determinista (custódia server-side de segredos)
// ===========================================================================
//
// LAYERING: o vault real do broker (platform/broker/internal/vault) é INTERNAL —
// nenhum outro módulo o pode importar. O keyvault do audit é de outro domínio
// (chaves de PII). Por isso o ambiente efémero define AQUI um vault de teste
// LEVE, modelado no vault.Memory do broker: os segredos vivem num mapa e SÓ saem
// encapsulados num [Secret] opaco cujo valor é NÃO-EXPORTADO — nenhum método o
// devolve; a única saída é [Secret.DeliverTo] a um [Sink] server-side. Redação
// em String()/MarshalJSON impede fuga acidental por log/JSON. É determinista
// (sem aleatoriedade) e efémero por Env (fresco e vazio a cada New).

// ErrNoMaterial — o vault não tem material para a [VaultKey] pedida. Fail-closed:
// o vault de teste não inventa nem substitui por outra credencial.
var ErrNoMaterial = errors.New("env.FakeVault: sem material para a chave (provider/regiao/capability)")

// VaultKey identifica material downstream. Todos os campos são NÃO-SECRETOS
// (nomes de provedor/região/capability), seguros para logs e para o Event Store.
type VaultKey struct {
	Provider   string
	Region     string
	Capability string
}

func (k VaultKey) id() string { return k.Provider + "|" + k.Region + "|" + k.Capability }

// Sink é o alvo de injecção SERVER-SIDE: o ponto onde a credencial é colocada
// para ser usada (o mount de credencial da microVM). [Secret.DeliverTo] é a
// única via que lhe entrega o valor.
type Sink interface {
	// Place recebe o valor em claro do lado server-side. ref é um rótulo
	// não-secreto (correlação); secret é o valor a montar no guest.
	Place(ref, secret string) error
}

// Secret é o portador do segredo downstream. O valor é NÃO-EXPORTADO e nenhum
// método o devolve; a única saída é [Secret.DeliverTo]. Redigido em
// String()/MarshalJSON. Um Secret zero é vazio ([Secret.IsZero]).
type Secret struct {
	ref string // rótulo não-secreto (ex.: VaultKey.id()); seguro para logs
	v   string // valor em claro — NÃO-EXPORTADO; nunca devolvido
}

// Ref devolve o rótulo NÃO-SECRETO do material (nunca o valor).
func (s Secret) Ref() string { return s.ref }

// IsZero indica material ausente/vazio.
func (s Secret) IsZero() bool { return s.v == "" }

// String redige o valor: nunca imprime o segredo.
func (s Secret) String() string { return "Secret{ref=" + s.ref + ",value=REDACTED}" }

// GoString redige o valor também sob %#v.
func (s Secret) GoString() string { return s.String() }

// MarshalJSON IMPÕE a redação em JSON: um encoding/json acidental serializa a
// forma redigida, nunca o valor (garantia do TIPO, não da omissão de um campo).
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// DeliverTo é a ÚNICA saída do valor: entrega-o a um [Sink] server-side e
// devolve apenas erro. O valor NUNCA regressa ao chamador. Um Secret zero
// devolve [ErrNoMaterial].
func (s Secret) DeliverTo(sink Sink) error {
	if s.v == "" {
		return ErrNoMaterial
	}
	return sink.Place(s.ref, s.v)
}

// FakeVault é o vault de teste in-memory determinista: os segredos vivem neste
// mapa e SÓ saem encapsulados num [Secret] opaco (via [FakeVault.Fetch]), nunca
// como valor nu. Concorrente-seguro. Efémero por Env — NUNCA usar em produção.
type FakeVault struct {
	mu      sync.RWMutex
	secrets map[string]string // VaultKey.id() → valor em claro
}

// NewFakeVault constrói um vault de teste vazio.
func NewFakeVault() *FakeVault {
	return &FakeVault{secrets: map[string]string{}}
}

// Put regista/roda o segredo da [VaultKey]. Uso server-side de aprovisionamento
// (o seed de um teste), nunca exposto ao agente.
func (v *FakeVault) Put(key VaultKey, secret string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.secrets[key.id()] = secret
}

// Remove torna a [VaultKey] indisponível (revogação central). Os próximos Fetch
// falham fail-closed com [ErrNoMaterial].
func (v *FakeVault) Remove(key VaultKey) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.secrets, key.id())
}

// Fetch devolve o [Secret] opaco da [VaultKey]. Fail-closed: sem material
// devolve [ErrNoMaterial] (nunca um Secret vazio silencioso).
func (v *FakeVault) Fetch(_ context.Context, key VaultKey) (Secret, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	s, ok := v.secrets[key.id()]
	if !ok || s == "" {
		return Secret{}, ErrNoMaterial
	}
	return Secret{ref: key.id(), v: s}, nil
}

// Len devolve o número de segredos custodiados (para asserção do estado limpo).
func (v *FakeVault) Len() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.secrets)
}
