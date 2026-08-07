package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// ---------------------------------------------------------------------------
// AOS-021 — ApprovalBroker: cerimónia four-eyes → grant → verificação na mediação
// ---------------------------------------------------------------------------
//
// PORQUE EXISTEM DUAS FASES (e não uma): o [FourEyesGate] CONSOME os challenges das
// pernas (uso-único durável, anti-replay). Logo a cerimónia de aprovação só pode correr
// UMA vez — chamá-la a cada mediação queimaria o challenge à primeira e recusaria todas
// as seguintes. O broker separa:
//
//	 (1) CERIMÓNIA  [ApprovalBroker.Approve]  — corre o four-eyes UMA vez (POST /approve),
//	     e persiste um GRANT ligado à preview da acção aprovada.
//	 (2) VERIFICAÇÃO [ApprovalBroker.VerifyApproval] — na mediação, consulta o grant e
//	     confirma que ele é DESTA call, está fresco e não foi usado. Não re-corre o
//	     four-eyes nem toca em challenges.

// Erros do broker de aprovação (comparáveis com errors.Is).
var (
	// ErrNilApprovalGate — broker sem [FourEyesGate].
	ErrNilApprovalGate = errors.New("integration: FourEyesGate nil")
	// ErrNilApprovalStore — broker sem store de grants.
	ErrNilApprovalStore = errors.New("integration: ApprovalStore nil")
	// ErrGrantNotFound — a evidência não corresponde a nenhum grant (ou já foi usada).
	ErrGrantNotFound = errors.New("integration: aprovação inexistente ou já utilizada")
	// ErrGrantExpired — o grant existe mas está fora do TTL.
	ErrGrantExpired = errors.New("integration: aprovação expirada")
	// ErrGrantPreviewMismatch — o grant é de OUTRA acção (a preview não bate).
	ErrGrantPreviewMismatch = errors.New("integration: aprovação pertence a outra accao")
)

// DefaultApprovalTTL é a janela de validade de uma aprovação concluída. Decisão do dono:
// 15 minutos — tempo para o resume acontecer, curto o suficiente para o contexto em que o
// humano decidiu não ter mudado materialmente.
const DefaultApprovalTTL = 15 * time.Minute

// ApprovalGrant é o registo de uma aprovação CONCLUÍDA e verificada. Não contém segredos
// (nem assinaturas nem challenges): só a amarra à acção, a atribuição e a frescura.
type ApprovalGrant struct {
	// ID é o identificador opaco entregue a quem aprovou; é o que volta como EVIDÊNCIA
	// na mediação ("tenho a aprovação X").
	ID string
	// Preview é o digest canónico da acção aprovada ([referencemonitor.ApprovalPreview]).
	// É a AMARRA: um grant só serve a call cuja preview seja igual a esta.
	Preview []byte
	// Approvers são os principals distintos que aprovaram.
	Approvers []string
	// DualControl indica que a acção era irreversível e obteve duas aprovações distintas.
	DualControl bool
	// ExpiresAt é o fim da janela de validade.
	ExpiresAt time.Time
}

// ApprovalStore persiste grants de aprovação. Consume é de USO-ÚNICO e ATÓMICO: devolve o
// grant e remove-o, de modo que uma aprovação não possa destravar a mesma acção
// repetidamente. A implementação de produção persiste no Event Store/WORM (sobrevive a
// failover); [NewMemApprovalStore] é a de referência in-memory.
type ApprovalStore interface {
	Put(ctx context.Context, g ApprovalGrant) error
	// Consume devolve o grant e marca-o usado. ok=false se não existir.
	Consume(ctx context.Context, id string) (ApprovalGrant, bool, error)
}

// memApprovalStore é a [ApprovalStore] in-memory de referência. DEMO-GRADE: um restart
// perde os grants pendentes (o efeito é fail-closed — a acção volta a exigir aprovação).
type memApprovalStore struct {
	mu     sync.Mutex
	grants map[string]ApprovalGrant
}

// NewMemApprovalStore constrói a store in-memory de referência.
func NewMemApprovalStore() ApprovalStore {
	return &memApprovalStore{grants: make(map[string]ApprovalGrant)}
}

func (s *memApprovalStore) Put(_ context.Context, g ApprovalGrant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grants[g.ID] = g
	return nil
}

func (s *memApprovalStore) Consume(_ context.Context, id string) (ApprovalGrant, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.grants[id]
	if !ok {
		return ApprovalGrant{}, false, nil
	}
	delete(s.grants, id) // USO-ÚNICO: uma aprovação destrava UMA execução
	return g, true, nil
}

// ApprovalBrokerOption configura o [ApprovalBroker].
type ApprovalBrokerOption func(*ApprovalBroker)

// WithApprovalTTL sobrepõe a janela de validade (default [DefaultApprovalTTL]).
func WithApprovalTTL(d time.Duration) ApprovalBrokerOption {
	return func(b *ApprovalBroker) {
		if d > 0 {
			b.ttl = d
		}
	}
}

// WithApprovalClock injecta o relógio (testes deterministas).
func WithApprovalClock(c func() time.Time) ApprovalBrokerOption {
	return func(b *ApprovalBroker) {
		if c != nil {
			b.clock = c
		}
	}
}

// ApprovalBroker liga a cerimónia four-eyes existente à porta
// [referencemonitor.ApprovalVerifier] do [referencemonitor.ApprovalGate]. É o adaptador do
// bridge negação→aprovação→reexecução: o kernel fica com a INVARIANTE (a prova não-forjável
// que destrava o taint-gate) e a POLÍTICA (quem aprova, quantas pernas, frescura) vive aqui.
type ApprovalBroker struct {
	gate  *FourEyesGate
	store ApprovalStore
	ttl   time.Duration
	clock func() time.Time
}

// NewApprovalBroker constrói o broker sobre o gate four-eyes e a store de grants.
func NewApprovalBroker(gate *FourEyesGate, store ApprovalStore, opts ...ApprovalBrokerOption) (*ApprovalBroker, error) {
	if gate == nil {
		return nil, ErrNilApprovalGate
	}
	if store == nil {
		return nil, ErrNilApprovalStore
	}
	b := &ApprovalBroker{gate: gate, store: store, ttl: DefaultApprovalTTL, clock: time.Now}
	for _, o := range opts {
		o(b)
	}
	return b, nil
}

// Approve corre a CERIMÓNIA four-eyes (uma única vez — consome os challenges) e, em
// sucesso, persiste um [ApprovalGrant] ligado a req.Preview. É o que a superfície
// POST /runs/{id}/approve invoca.
//
// O grantID é escolhido pelo chamador (tipicamente o request_id do pedido de aprovação),
// mantendo o broker sem fontes de aleatoriedade — determinista e testável.
//
// FAIL-CLOSED: qualquer recusa do four-eyes (assinatura, contagem/distinção de pernas,
// challenge replayed, attestation) propaga-se e NADA é persistido.
func (b *ApprovalBroker) Approve(ctx context.Context, grantID string, req FourEyesRequest, legs ...ApprovalLeg) (ApprovalGrant, error) {
	if grantID == "" {
		return ApprovalGrant{}, fmt.Errorf("integration: grant id vazio")
	}
	dec, err := b.gate.Authorize(ctx, req, legs...)
	if err != nil {
		return ApprovalGrant{}, err
	}
	if !dec.Authorized {
		return ApprovalGrant{}, fmt.Errorf("integration: aprovacao recusada: %s", dec.Reason)
	}
	g := ApprovalGrant{
		ID:          grantID,
		Preview:     append([]byte(nil), req.Preview...),
		Approvers:   append([]string(nil), dec.Approvers...),
		DualControl: req.DualControlRequired,
		ExpiresAt:   b.clock().Add(b.ttl),
	}
	if err := b.store.Put(ctx, g); err != nil {
		return ApprovalGrant{}, err
	}
	return g, nil
}

// VerifyApproval implementa [referencemonitor.ApprovalVerifier]: na mediação, resolve a
// evidência (o grant id) e confirma que a aprovação é DESTA acção, está dentro do TTL e
// não foi usada. NÃO re-corre o four-eyes (os challenges já foram consumidos na cerimónia).
//
// ORDEM FAIL-CLOSED: o grant é consumido (uso-único) ANTES das verificações de amarra e
// frescura — assim uma tentativa de reutilização, ou de aplicar um grant a OUTRA acção,
// queima-o em vez de o deixar disponível para nova tentativa. Uma aprovação destrava, no
// máximo, UMA execução.
//
// CONTRAPARTIDA ASSUMIDA: se a call for negada mais à frente na cadeia (escopo, egress,
// PDP), o grant já foi consumido e é preciso nova aprovação. É deliberado — a alternativa
// (não consumir até ao permit) deixaria uma janela em que a mesma aprovação poderia ser
// reapresentada, e "aprovar uma vez, executar várias" é o pior dos dois riscos.
func (b *ApprovalBroker) VerifyApproval(ctx context.Context, evidence, preview []byte) (referencemonitor.ApprovalProof, error) {
	id := string(evidence)
	if id == "" {
		return referencemonitor.ApprovalProof{}, ErrGrantNotFound
	}
	g, ok, err := b.store.Consume(ctx, id)
	if err != nil {
		return referencemonitor.ApprovalProof{}, err
	}
	if !ok {
		return referencemonitor.ApprovalProof{}, ErrGrantNotFound
	}
	// AMARRA: o grant tem de ser da acção que está a ser mediada. Um grant de outra call
	// (outro run/step/tool/recurso, ou o mesmo com input adulterado) não serve.
	if !constantTimeEqualBytes(g.Preview, preview) {
		return referencemonitor.ApprovalProof{}, ErrGrantPreviewMismatch
	}
	if !b.clock().Before(g.ExpiresAt) {
		return referencemonitor.ApprovalProof{}, ErrGrantExpired
	}
	return referencemonitor.ApprovalProof{
		Approvers:   append([]string(nil), g.Approvers...),
		DualControl: g.DualControl,
	}, nil
}

// constantTimeEqualBytes compara dois digests sem ramificar no conteúdo. Os digests não
// são segredos, mas a comparação de tempo constante é o hábito correcto num caminho de
// autorização e custa nada.
func constantTimeEqualBytes(a, b []byte) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

var _ referencemonitor.ApprovalVerifier = (*ApprovalBroker)(nil)
