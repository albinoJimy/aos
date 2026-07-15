package credentials

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/internal/adapters/oauth"
)

// ErrRegionNotConfigured — o par (provider, região) pedido não está configurado
// nesta origem. Fail-closed: a fronteira de soberania é respeitada — a origem
// NUNCA serve a chave de outra região por defeito (a allowlist regional concreta
// é AOS-058; aqui a config + a recusa atribuível).
var ErrRegionNotConfigured = errors.New("credentials: par provider/regiao nao configurado")

// ErrNoProvider — não há provedor OAuth registado para o provider pedido.
var ErrNoProvider = errors.New("credentials: sem provedor OAuth registado")

// ErrEmptyToken — a troca OAuth produziu um token vazio. Fail-closed (nunca se
// serve uma credencial sem segredo, que geraria um pedido não-autenticado).
var ErrEmptyToken = errors.New("credentials: token OAuth vazio (fail-closed)")

// CredentialError torna qualquer falha de aquisição ATRIBUÍVEL: identifica o
// provider e a região, preservando o errors.Is da causa. É o erro fail-closed que
// o adaptador propaga quando não há credencial válida — nunca há um fallback
// silencioso para outra conta/região.
type CredentialError struct {
	Provider string
	Region   string
	Err      error
}

func (e *CredentialError) Error() string {
	return "credentials: aquisicao falhou provider=" + e.Provider + " regiao=" + e.Region + ": " + e.Err.Error()
}

func (e *CredentialError) Unwrap() error { return e.Err }

// ProviderRegion identifica uma configuração (provider, região) elegível.
type ProviderRegion struct {
	Provider string
	Region   string
}

// Config configura a origem: a política de TTL/refresh do cache e o conjunto de
// pares (provider, região) elegíveis (a config por provider/região). Um pedido
// para um par fora de Allowed falha fail-closed atribuível.
type Config struct {
	// TTL é a vida curta da credencial no cache. Default: 10min.
	TTL time.Duration
	// RefreshLead é a antecedência com que o cache RENOVA antes de expirar. Uma
	// credencial é renovada quando now >= ExpiresAt - RefreshLead. Default: 1min.
	RefreshLead time.Duration
	// Allowed é o conjunto de pares (provider, região) configurados. Se vazio, a
	// origem é permissiva (qualquer par que o broker+registry suportem) — usar só
	// quando a soberania é imposta a montante. Preencher para impor a fronteira.
	Allowed []ProviderRegion
	// Clock é o relógio injectável (TTL/refresh deterministas). Default: time.Now.
	Clock func() time.Time
	// AcquireTimeout, se > 0, limita a aquisição (broker.Issue + troca OAuth) de um
	// par: uma emissão pendurada não retém o lock POR-CHAVE indefinidamente. Usa o
	// relógio real (context.WithTimeout), pelo que é OPT-IN — deixar a 0 preserva o
	// determinismo (a aquisição fica então limitada apenas pelo ctx do chamador).
	AcquireTimeout time.Duration
}

// Source é a fonte REAL de credenciais: implementa [adapters.CredentialSource]
// sobre um [CredentialBroker] + um [oauth.Registry], com cache JIT (TTL curto,
// refresh antes de expirar), rotação sem interromper in-flight e revogação.
//
// Concorrência: seguro para Fetch em paralelo e GENUINAMENTE paralelo entre pares
// (provider, região) distintos. O lock global [Source.mu] guarda apenas os mapas
// (secções críticas curtas) e NUNCA é retido através da I/O do broker/troca OAuth;
// a aquisição de um par é serializada por um lock POR-CHAVE (singleflight), de
// modo que uma emissão lenta/pendurada de UM par não bloqueia o fast-path de
// leitura em cache nem a aquisição de OUTROS pares (sem head-of-line blocking).
type Source struct {
	broker         CredentialBroker
	registry       *oauth.Registry
	clock          func() time.Time
	ttl            time.Duration
	lead           time.Duration
	acquireTimeout time.Duration
	allowed        map[string]bool // "provider|regiao" → configurado (vazio = permissivo)

	mu      sync.Mutex
	entries map[string]*entry    // "provider|regiao" → credencial em cache
	locks   map[string]*keyMutex // "provider|regiao" → lock de aquisição por-chave
}

// keyMutex serializa a aquisição (miss/refresh) de UMA chave sem reter o lock
// global através da I/O. Emissões de chaves diferentes correm em paralelo.
type keyMutex struct{ mu sync.Mutex }

// entry é uma credencial em cache com o lease que a originou e a sua expiração no
// cache. A [adapters.Credential] é um valor imutável: uma vez devolvida por Fetch,
// o chamador tem a SUA cópia — uma rotação que substitua esta entrada NÃO afecta
// uma chamada já em curso (in-flight completa com a chave antiga).
type entry struct {
	cred      adapters.Credential
	leaseID   string
	expiresAt time.Time
}

// Compile-time: a Source satisfaz a porta de AOS-055.
var _ adapters.CredentialSource = (*Source)(nil)

// NewSource constrói a origem sobre o broker e o registo OAuth dados.
func NewSource(broker CredentialBroker, registry *oauth.Registry, cfg Config) *Source {
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	lead := cfg.RefreshLead
	if lead <= 0 {
		lead = time.Minute
	}
	allowed := map[string]bool{}
	for _, pr := range cfg.Allowed {
		allowed[pr.Provider+"|"+pr.Region] = true
	}
	return &Source{
		broker:         broker,
		registry:       registry,
		clock:          clock,
		ttl:            ttl,
		lead:           lead,
		acquireTimeout: cfg.AcquireTimeout,
		allowed:        allowed,
		entries:        map[string]*entry{},
		locks:          map[string]*keyMutex{},
	}
}

// Fetch implementa [adapters.CredentialSource]. Resolve a credencial de infra
// para o par (provider, região) EXACTO pedido — server-side, JIT, com cache. Em
// qualquer falha devolve um [*CredentialError] atribuível; nunca cai para outra
// conta/região.
func (s *Source) Fetch(ctx context.Context, provider, region string) (adapters.Credential, error) {
	key := provider + "|" + region

	// Fronteira de soberania: se há allowlist configurada, o par tem de constar.
	if len(s.allowed) > 0 && !s.allowed[key] {
		return adapters.Credential{}, &CredentialError{Provider: provider, Region: region, Err: ErrRegionNotConfigured}
	}

	// Fast-path SEM reter o lock através de I/O: uma credencial fresca é servida
	// sob uma secção crítica curta. Nunca bloqueia noutro par nem numa emissão lenta.
	if cred, ok := s.cached(key, s.clock()); ok {
		return cred, nil
	}

	// Miss/refresh: serializa POR-CHAVE (singleflight). Pares diferentes correm em
	// paralelo; o lock global não é retido durante broker.Issue/Exchange.
	km := s.keyLock(key)
	km.mu.Lock()
	defer km.mu.Unlock()

	// Double-check: outra goroutine para a MESMA chave pode ter renovado enquanto
	// esperávamos o lock por-chave — evita uma reemissão duplicada.
	if cred, ok := s.cached(key, s.clock()); ok {
		return cred, nil
	}

	// Deadline OPT-IN: uma emissão pendurada não retém o lock por-chave para sempre.
	if s.acquireTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.acquireTimeout)
		defer cancel()
	}

	// JIT: obtém um lease novo do broker para o par EXACTO (renovação ou 1ª vez).
	lease, err := s.broker.Issue(ctx, provider, region)
	if err != nil {
		return adapters.Credential{}, &CredentialError{Provider: provider, Region: region, Err: err}
	}

	prov, ok := s.registry.Get(provider)
	if !ok {
		return adapters.Credential{}, &CredentialError{Provider: provider, Region: region, Err: ErrNoProvider}
	}

	material := oauth.NewMaterial(provider, region, lease.reveal(), lease.ExpiresAt)
	cred, tokenExpiresAt, err := prov.Exchange(ctx, material)
	if err != nil {
		return adapters.Credential{}, &CredentialError{Provider: provider, Region: region, Err: err}
	}
	if cred.KeyID() == "" {
		// Token vazio: fail-closed, nunca serve uma credencial sem segredo.
		return adapters.Credential{}, &CredentialError{Provider: provider, Region: region, Err: ErrEmptyToken}
	}

	// TTL do cache LIMITADO pela expiração REAL: o menor de (now+ttl, expiração do
	// lease, expiração do token). Assim o refresh-lead actua sobre a expiração
	// efectiva — nunca se serve uma credencial já expirada no provedor só porque o
	// TTL sintético do cache ainda não passou (invariante JIT/TTL).
	now := s.clock()
	expiresAt := now.Add(s.ttl)
	if !lease.ExpiresAt.IsZero() && lease.ExpiresAt.Before(expiresAt) {
		expiresAt = lease.ExpiresAt
	}
	if !tokenExpiresAt.IsZero() && tokenExpiresAt.Before(expiresAt) {
		expiresAt = tokenExpiresAt
	}

	// Troca ATÓMICA da referência em cache: novas chamadas passam a ver a nova
	// credencial; as em curso mantêm a cópia que já receberam (rotação sem corte).
	s.mu.Lock()
	s.entries[key] = &entry{cred: cred, leaseID: lease.LeaseID, expiresAt: expiresAt}
	s.mu.Unlock()
	return cred, nil
}

// cached devolve a credencial em cache para a chave SE ainda estiver fresca (fora
// da janela de refresh), sob uma secção crítica curta do lock global. Não faz I/O.
func (s *Source) cached(key string, now time.Time) (adapters.Credential, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok && now.Before(e.expiresAt.Add(-s.lead)) {
		return e.cred, true
	}
	return adapters.Credential{}, false
}

// keyLock devolve (criando se preciso) o lock de aquisição por-chave. A criação é
// guardada pelo lock global (secção curta); a I/O corre depois, só sob o por-chave.
func (s *Source) keyLock(key string) *keyMutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	km, ok := s.locks[key]
	if !ok {
		km = &keyMutex{}
		s.locks[key] = km
	}
	return km
}

// Revoke revoga a credencial em cache para o par (provider, região): invalida a
// entrada (deixa de ser servida) e revoga o lease no broker. A próxima Fetch
// obtém uma credencial NOVA (se o broker ainda tiver material) ou falha
// fail-closed atribuível (se não tiver). Uma chamada já em curso com a credencial
// antiga não é afectada.
func (s *Source) Revoke(ctx context.Context, provider, region string) error {
	key := provider + "|" + region
	s.mu.Lock()
	e, ok := s.entries[key]
	delete(s.entries, key)
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return s.broker.Revoke(ctx, e.leaseID)
}

// leaseIDFor devolve o leaseID da credencial em cache para o par (uso de teste
// white-box: correlaciona rotação/revogação sem tocar no segredo).
func (s *Source) leaseIDFor(provider, region string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[provider+"|"+region]
	if !ok {
		return "", false
	}
	return e.leaseID, true
}
