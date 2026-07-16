// Package vault é a custódia server-side dos segredos downstream do broker
// (AOS-070, ADR-006). É INTERNAL: só código enraizado em .../platform/broker o
// pode importar — o runtime do agente (outro módulo) NUNCA lhe toca.
//
// A invariante central vive AQUI, imposta pelo TIPO: [Secret] transporta o valor
// num campo NÃO-EXPORTADO e NENHUM método o devolve. A única saída do valor é
// [Secret.DeliverTo], que o entrega a um [Sink] server-side (o ponto de injecção
// na microVM) e devolve apenas erro. Nem sequer o pacote broker (pai) consegue ler
// o valor — só reencaminhá-lo para um sink. Redação em String()/MarshalJSON
// garante que um log/JSON acidental nunca serializa o segredo.
package vault

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
)

// ErrNoMaterial — o Vault não tem material para a [Key] pedida. Fail-closed: o
// broker não inventa nem substitui por outra credencial.
var ErrNoMaterial = errors.New("vault: sem material para a chave (provider/regiao/capability)")

// Key identifica material downstream. Todos os campos são NÃO-SECRETOS (nomes de
// provedor/região/capability), seguros para logs e para o Event Store.
type Key struct {
	Provider   string
	Region     string
	Capability string
}

func (k Key) id() string { return k.Provider + "|" + k.Region + "|" + k.Capability }

// Sink é o alvo de injecção SERVER-SIDE: o ponto onde a credencial é colocada para
// ser usada (o mount de credencial da microVM). Implementações são infra
// server-side, NÃO alcançáveis pelo contexto do agente. [Secret.DeliverTo] é a
// única via que lhes entrega o valor.
type Sink interface {
	// Place recebe o valor em claro no lado server-side. ref é um rótulo
	// não-secreto (para correlação); secret é o valor a montar no guest.
	Place(ref, secret string) error
}

// Secret é o portador do segredo downstream. O valor é NÃO-EXPORTADO e nenhum
// método o devolve; a única saída é [Secret.DeliverTo]. Redigido em
// String()/MarshalJSON (ADR-006). Um Secret zero é vazio ([Secret.IsZero]).
type Secret struct {
	ref string // rótulo não-secreto (ex.: a Key.id()); seguro para logs
	v   string // valor em claro — NÃO-EXPORTADO; nunca devolvido
}

// Ref devolve o rótulo NÃO-SECRETO do material (nunca o valor).
func (s Secret) Ref() string { return s.ref }

// IsZero indica material ausente/vazio.
func (s Secret) IsZero() bool { return s.v == "" }

// String redige o valor (ADR-006): nunca imprime o segredo.
func (s Secret) String() string { return "Secret{ref=" + s.ref + ",value=REDACTED}" }

// GoString redige o valor também sob %#v (ADR-006).
func (s Secret) GoString() string { return s.String() }

// MarshalJSON IMPÕE a redação em JSON: um encoding/json acidental serializa a
// forma redigida, nunca o valor (garantia do TIPO, não da omissão de um campo).
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// DeliverTo é a ÚNICA saída do valor: entrega-o a um [Sink] server-side e devolve
// apenas erro. O valor NUNCA regressa ao chamador. Um Secret zero é no-op.
func (s Secret) DeliverTo(sink Sink) error {
	if s.v == "" {
		return ErrNoMaterial
	}
	return sink.Place(s.ref, s.v)
}

// Client é a PORTA do Vault: dado uma [Key], devolve o [Secret] opaco. A
// implementação real é o vault de infra (HashiCorp Vault / cloud KMS); aqui há uma
// impl de referência in-memory ([Memory]).
type Client interface {
	Fetch(ctx context.Context, key Key) (Secret, error)
}

// Memory é o Vault de REFERÊNCIA in-memory: os segredos vivem neste mapa e SÓ saem
// encapsulados num [Secret] opaco (via [Memory.Fetch]), nunca como valor nu.
// Concorrente-seguro. NUNCA usar em produção.
type Memory struct {
	mu      sync.RWMutex
	secrets map[string]string // Key.id() → valor em claro
}

// NewMemory constrói um Vault de referência vazio.
func NewMemory() *Memory {
	return &Memory{secrets: map[string]string{}}
}

// Put regista/roda o segredo da [Key]. Uso server-side de aprovisionamento (nunca
// exposto ao agente).
func (m *Memory) Put(key Key, secret string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.secrets[key.id()] = secret
}

// Remove torna a [Key] indisponível (revogação central no vault). Os próximos
// Fetch falham fail-closed com [ErrNoMaterial].
func (m *Memory) Remove(key Key) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.secrets, key.id())
}

// Fetch implementa [Client]. Fail-closed: sem material devolve [ErrNoMaterial].
func (m *Memory) Fetch(_ context.Context, key Key) (Secret, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.secrets[key.id()]
	if !ok || v == "" {
		return Secret{}, ErrNoMaterial
	}
	return Secret{ref: key.id(), v: v}, nil
}
