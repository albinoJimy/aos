// Package credentials é a fonte REAL de credenciais de infra do Model Gateway
// (AOS-056, tecnica/06 §4, tecnica/07 §7.1, ADR-006). Implementa a porta
// [adapters.CredentialSource] de AOS-055 apoiada num Credential Broker/Vault
// server-side com tokens JIT: obtém a chave de infra do broker quando é precisa
// (just-in-time), troca-a pelo token do provedor via camada OAuth
// (internal/adapters/oauth), guarda-a num cache com TTL curto que RENOVA antes de
// expirar, suporta ROTAÇÃO sem interromper chamadas em curso e REVOGAÇÃO.
//
// # Invariantes (ADR-006)
//
//   - A chave de infra NUNCA é hard-coded, logada, colocada num span nem devolvida
//     ao agente: sai sempre encapsulada numa [adapters.Credential] redigida.
//   - Sem credencial válida (ausente/expirada/revogada/região não configurada) a
//     obtenção FALHA fail-closed com um [*CredentialError] ATRIBUÍVEL (identifica
//     provider+região) — NUNCA cai para outra conta/região silenciosamente.
//   - Determinismo: relógio injectável (TTL/rotação sem time.Now real); IDs de
//     lease deterministas; sem aleatoriedade na decisão.
//
// O broker é a FRONTEIRA (porta [CredentialBroker]); o vault real é infra
// (EPIC-07). Este pacote fornece um broker FAKE determinista (testes) e um broker
// de REFERÊNCIA que documenta o vault real e falha fail-closed até ser ligado.
package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"
)

// ErrNoMaterial — o broker não tem material de infra para o par (provider,
// região). Fail-closed: a origem não inventa nem substitui por outra conta/região.
var ErrNoMaterial = errors.New("credentials: broker sem material para provider/regiao")

// ErrNotWired — o broker de REFERÊNCIA não está ligado a um vault real. É
// fail-closed por desenho: impede o uso acidental em produção sem o vault.
var ErrNotWired = errors.New("credentials: broker de referencia nao ligado a um vault (infra/EPIC-07)")

// Lease é um lease de credencial JIT emitido pelo broker: um segredo de infra com
// TTL curto e um identificador de lease revogável. O segredo é NÃO-EXPORTADO e
// redigido em [Lease.String] — nunca é logado (ADR-006). Só este pacote o lê,
// para o entregar à camada OAuth server-side.
type Lease struct {
	LeaseID   string
	Provider  string
	Region    string
	ExpiresAt time.Time
	secret    string
}

// String redige o segredo (ADR-006).
func (l Lease) String() string {
	return "Lease{id=" + l.LeaseID + ",provider=" + l.Provider + ",region=" + l.Region + ",secret=REDACTED}"
}

// MarshalJSON IMPÕE a redação também em JSON (ADR-006): um encoding/json acidental
// serializa a forma redigida, nunca o segredo. A garantia é imposta pelo tipo,
// não apenas pela omissão de um campo não-exportado.
func (l Lease) MarshalJSON() ([]byte, error) { return json.Marshal(l.String()) }

// reveal devolve o segredo do lease (uso interno do pacote, ao alimentar a troca
// OAuth server-side).
func (l Lease) reveal() string { return l.secret }

// CredentialBroker é a PORTA do Credential Broker/Vault (BRK, ADR-006). Dado
// (provider, região), emite um [Lease] JIT com TTL e permite revogá-lo por
// identificador. A implementação real é o vault de infra; aqui há um fake
// determinista e um stub de referência.
type CredentialBroker interface {
	// Issue emite um lease JIT para o par (provider, região). Fail-closed: devolve
	// erro (envolvendo [ErrNoMaterial]) se não houver material válido — nunca
	// devolve material de outra conta/região.
	Issue(ctx context.Context, provider, region string) (Lease, error)
	// Revoke revoga um lease previamente emitido, pelo seu identificador. Após a
	// revogação o segredo desse lease deixa de poder ser reemitido.
	Revoke(ctx context.Context, leaseID string) error
}

// ---------------------------------------------------------------------------
// FakeBroker — broker determinista para testes.
// ---------------------------------------------------------------------------

// FakeBroker é um [CredentialBroker] in-memory DETERMINISTA: um mapa
// (provider|região) → segredo corrente, um relógio injectável para o TTL, e um
// contador estável de IDs de lease. Suporta ROTAÇÃO (trocar o segredo corrente de
// um par sem tocar nos leases já emitidos) e REVOGAÇÃO. Concorrente-seguro (o
// -race guarda a troca de segredo em paralelo com Issue). NUNCA usar em produção.
type FakeBroker struct {
	clock func() time.Time
	ttl   time.Duration

	mu        sync.Mutex
	secrets   map[string]string // "provider|regiao" → segredo corrente
	seq       int               // contador determinista de IDs de lease
	issued    int               // total de leases emitidos (asserção JIT)
	revoked   map[string]bool   // leaseID → revogado
	revokeLog []string          // ordem de revogação (asserção)
}

// NewFakeBroker constrói um broker fake com o relógio e o TTL de lease dados. Se
// clock for nil usa time.Now; se ttl <= 0 usa 1h (a origem impõe o seu TTL curto).
func NewFakeBroker(clock func() time.Time, ttl time.Duration) *FakeBroker {
	if clock == nil {
		clock = time.Now
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &FakeBroker{
		clock:   clock,
		ttl:     ttl,
		secrets: map[string]string{},
		revoked: map[string]bool{},
	}
}

// SetSecret regista/roda o segredo corrente do par (provider, região). Chamar de
// novo com outro valor é uma ROTAÇÃO: os próximos Issue devolvem o novo segredo;
// os leases já emitidos mantêm o segredo antigo (a rotação não os altera).
func (b *FakeBroker) SetSecret(provider, region, secret string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.secrets[provider+"|"+region] = secret
}

// Remove torna o par (provider, região) indisponível (simula ausência de material
// ou revogação central). Os próximos Issue falham fail-closed.
func (b *FakeBroker) Remove(provider, region string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.secrets, provider+"|"+region)
}

// Issue implementa [CredentialBroker].
func (b *FakeBroker) Issue(_ context.Context, provider, region string) (Lease, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	secret, ok := b.secrets[provider+"|"+region]
	if !ok || secret == "" {
		return Lease{}, &brokerError{provider: provider, region: region, err: ErrNoMaterial}
	}
	b.seq++
	b.issued++
	return Lease{
		LeaseID:   "lease-" + provider + "-" + region + "-" + strconv.Itoa(b.seq),
		Provider:  provider,
		Region:    region,
		ExpiresAt: b.clock().Add(b.ttl),
		secret:    secret,
	}, nil
}

// Revoke implementa [CredentialBroker]: marca o lease como revogado.
func (b *FakeBroker) Revoke(_ context.Context, leaseID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.revoked[leaseID] = true
	b.revokeLog = append(b.revokeLog, leaseID)
	return nil
}

// IssueCount devolve o total de leases emitidos (asserção de que a aquisição é
// JIT e o cache evita reemissões desnecessárias).
func (b *FakeBroker) IssueCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.issued
}

// Revoked reporta se um leaseID foi revogado.
func (b *FakeBroker) Revoked(leaseID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.revoked[leaseID]
}

// brokerError torna um erro do broker ATRIBUÍVEL (provider+região) preservando o
// errors.Is da causa (ex.: [ErrNoMaterial]).
type brokerError struct {
	provider string
	region   string
	err      error
}

func (e *brokerError) Error() string {
	return "credentials: broker provider=" + e.provider + " regiao=" + e.region + ": " + e.err.Error()
}

func (e *brokerError) Unwrap() error { return e.err }

// ---------------------------------------------------------------------------
// ReferenceBroker — stub de referência do vault real (infra/EPIC-07).
// ---------------------------------------------------------------------------

// ReferenceBroker documenta a integração REAL com o Credential Broker/Vault e é
// fail-closed até ser ligada. O vault real (HashiCorp Vault / cloud KMS + broker
// server-side, tecnica/07 §7.1) emite leases JIT trocando o token scoped do
// principal por credenciais downstream com TTL curto e revogação central. Este
// stub NÃO contém segredos e recusa emitir (ErrNotWired) — o wiring concreto é
// responsabilidade de infra, fora do âmbito de AOS-056. Existe para fixar a
// forma da porta e evitar que um broker por engano devolva material vazio.
type ReferenceBroker struct{}

// Issue implementa [CredentialBroker]: fail-closed enquanto não ligado ao vault.
func (ReferenceBroker) Issue(_ context.Context, provider, region string) (Lease, error) {
	return Lease{}, &brokerError{provider: provider, region: region, err: ErrNotWired}
}

// Revoke implementa [CredentialBroker]: nada a revogar num stub não ligado.
func (ReferenceBroker) Revoke(_ context.Context, _ string) error { return ErrNotWired }
