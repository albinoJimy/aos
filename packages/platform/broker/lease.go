package broker

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/aos-ref/platform/broker/internal/vault"
)

// Handle é o identificador OPACO e NÃO-SECRETO que o agente recebe da troca
// (compõe o credentials_handle de AOS-064). Resolve-se server-side numa [Lease];
// o agente nunca vê o valor da credencial.
type Handle string

// Lease é a credencial downstream emitida pela troca: metadados NÃO-SECRETOS
// (quem/para quê/quando, id, TTL) mais o material do segredo encapsulado num
// [vault.Secret] OPACO — cujo valor nem este pacote consegue ler, só reencaminhar
// para um sink server-side ([Lease.injectInto]). Redigida em String()/MarshalJSON.
type Lease struct {
	ID           string    // id de lease NÃO-SECRETO (revogação/auditoria)
	Handle       Handle    // handle opaco entregue ao agente
	RunID        string    // correlação com a trajectória (não-secreto)
	PrincipalNHI string    // quem (identidade não-humana)
	Resource     string    // para quê (recurso downstream, não-secreto)
	Region       string    // soberania de dados
	Capability   string    // capability trocada
	IssuedAt     time.Time // quando
	ExpiresAt    time.Time // TTL curto

	secret  vault.Secret // OPACO: valor não-legível aqui; só entregável a um Sink
	mu      sync.Mutex
	revoked bool
}

// String redige o material (ADR-006): o [vault.Secret] já se auto-redige.
func (l *Lease) String() string {
	return "Lease{id=" + l.ID + ",principal=" + l.PrincipalNHI +
		",resource=" + l.Resource + ",capability=" + l.Capability + ",secret=REDACTED}"
}

// GoString redige o material também sob %#v (ADR-006). Sem isto, o fmt reflectiria
// nos campos e dumparia o valor NÃO-EXPORTADO do [vault.Secret] aninhado (um
// método GoString/String num campo não-exportado não é invocável por reflexão);
// implementando-o no portador, o %#v nunca alcança o segredo.
func (l *Lease) GoString() string { return l.String() }

// leaseView é a projecção NÃO-SECRETA da lease para serialização/auditoria.
type leaseView struct {
	LeaseID      string `json:"lease_id"`
	Handle       string `json:"handle"`
	PrincipalNHI string `json:"principal_nhi"`
	Resource     string `json:"resource"`
	Region       string `json:"region,omitempty"`
	Capability   string `json:"capability"`
	IssuedAt     string `json:"issued_at"`
	ExpiresAt    string `json:"expires_at"`
	Secret       string `json:"secret"` // sempre "REDACTED"
}

func (l *Lease) view() leaseView {
	return leaseView{
		LeaseID:      l.ID,
		Handle:       string(l.Handle),
		PrincipalNHI: l.PrincipalNHI,
		Resource:     l.Resource,
		Region:       l.Region,
		Capability:   l.Capability,
		IssuedAt:     l.IssuedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt:    l.ExpiresAt.UTC().Format(time.RFC3339Nano),
		Secret:       "REDACTED",
	}
}

// MarshalJSON IMPÕE a redação em JSON: nunca serializa o valor do segredo.
func (l *Lease) MarshalJSON() ([]byte, error) { return json.Marshal(l.view()) }

// revoke marca a lease como revogada (a credencial deixa de ser injectável).
func (l *Lease) revoke() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.revoked = true
}

// usableLocked verifica se a lease pode ser injectada AGORA (não revogada e dentro
// do TTL). Devolve [ErrLeaseRevoked] ou [ErrLeaseExpired] fail-closed. Assume que
// l.mu JÁ está adquirido (ver [Lease.injectInto]).
func (l *Lease) usableLocked(now time.Time) error {
	if l.revoked {
		return ErrLeaseRevoked
	}
	if !now.Before(l.ExpiresAt) {
		return ErrLeaseExpired
	}
	return nil
}

// injectInto entrega o material a um sink SERVER-SIDE (a única saída do valor).
// A verificação de usabilidade (TTL/revogação) e a entrega correm SOB O MESMO
// l.mu, tornando a injecção ATÓMICA face a [Lease.revoke]: uma revogação concorrente
// ou é observada por usableLocked (e a entrega não acontece), ou só ganha o lock
// depois da entrega já ter terminado — nunca coexiste com uma entrega desta lease
// (fecha a janela TOCTOU). Uma lease expirada/revogada NÃO entrega o valor.
func (l *Lease) injectInto(sink vault.Sink, now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usableLocked(now); err != nil {
		return err
	}
	return l.secret.DeliverTo(sink)
}

// leaseStore é o registo SERVER-SIDE handle → lease. É a fronteira onde o segredo
// (encapsulado) reside entre a troca e a injecção; nada aqui é exposto ao agente.
// Concorrente-seguro (-race).
//
// Expiração LAZY (decisão explícita): o corte de acesso no TTL/revogação é imposto
// na injecção ([Lease.usableLocked]) — uma lease expirada/revogada nunca entrega o
// valor. O store NÃO evicta entradas expiradas/revogadas (não há reaper): o *Lease
// (com o [vault.Secret] encapsulado, cujo valor este pacote nem consegue ler)
// permanece em memória. Para um serviço de longa duração, um GC/reaper de leases
// expiradas fica registado como trabalho operacional futuro, fora do escopo de
// AOS-070; a correcção de segurança (acesso cortado no TTL) já está garantida.
type leaseStore struct {
	mu     sync.RWMutex
	leases map[Handle]*Lease
	byID   map[string]*Lease
}

func newLeaseStore() *leaseStore {
	return &leaseStore{leases: map[Handle]*Lease{}, byID: map[string]*Lease{}}
}

func (s *leaseStore) put(l *Lease) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leases[l.Handle] = l
	s.byID[l.ID] = l
}

func (s *leaseStore) get(h Handle) (*Lease, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	l, ok := s.leases[h]
	return l, ok
}

// revokeByID revoga a lease pelo id NÃO-SECRETO; devolve false se desconhecida.
func (s *leaseStore) revokeByID(leaseID string) bool {
	s.mu.RLock()
	l, ok := s.byID[leaseID]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	l.revoke()
	return true
}
