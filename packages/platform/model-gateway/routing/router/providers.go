package router

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// Este ficheiro reúne as IMPLEMENTAÇÕES DE REFERÊNCIA determinísticas das portas
// de carga e de admissão do router. São o análogo do StaticQuotaProvider (AOS-027)
// e do StaticModelTierRouter (AOS-031): fecham o contrato das portas para teste e
// arranque, SEM I/O nem relógio real. Produção liga o estado real de carga
// (keypool/observabilidade) e o admission control distribuído (EPIC-03) por
// adaptador (ver routing/tieradapter).

// StaticLoadProvider é a impl de referência do [LoadProvider]: um mapa fixo de
// headroom por (provider, região). Sem I/O; segura para uso concorrente. Um
// endpoint não configurado devolve o headroom por omissão.
type StaticLoadProvider struct {
	mu    sync.RWMutex
	byKey map[string]Headroom
	def   Headroom
}

// NewStaticLoadProvider constrói a impl de referência com um headroom por omissão
// (endpoint ausente). Um def com WorstLimit<=0 é normalizado a 1 (evita divisão
// por zero na comparação).
func NewStaticLoadProvider(def Headroom) *StaticLoadProvider {
	if def.WorstLimit <= 0 {
		def.WorstLimit = 1
	}
	return &StaticLoadProvider{byKey: make(map[string]Headroom), def: def}
}

// Set fixa o headroom de um endpoint (provider, região).
func (p *StaticLoadProvider) Set(provider, region string, h Headroom) *StaticLoadProvider {
	if h.WorstLimit <= 0 {
		h.WorstLimit = 1
	}
	p.mu.Lock()
	p.byKey[provider+"|"+region] = h
	p.mu.Unlock()
	return p
}

// Load implementa [LoadProvider].
func (p *StaticLoadProvider) Load(_ context.Context, provider, region string) (Headroom, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if h, ok := p.byKey[provider+"|"+region]; ok {
		return h, nil
	}
	return p.def, nil
}

// StaticAdmissionCoordinator é a impl de referência do [AdmissionCoordinator]: um
// token-bucket GLOBAL simples e PARTILHADO por chave (provider:model:region), que
// prova a coordenação SEM COLAPSO AGREGADO (ADR-008) — vários boards, cada um
// dentro do seu limite local, partilham o MESMO tecto global e são ADIADOS quando
// a soma satura, em vez de saturarem cegamente o rate limit partilhado.
//
// É determinística (sem relógio no caminho de decisão): a reserva DEDUZ do
// headroom partilhado e mantém-se até [StaticAdmissionCoordinator.Release]. Um
// custo que EXCEDE o tecto é rejeição PERMANENTE (nunca admissível). Produção usa
// o admission control distribuído real (EPIC-03) por adaptador — esta impl NÃO o
// reimplementa, apenas fecha o contrato da porta.
type StaticAdmissionCoordinator struct {
	mu sync.Mutex
	// capTokens/capRequests é o tecto GLOBAL partilhado por chave.
	capTokens    map[string]int64
	capRequests  map[string]int64
	usedTokens   map[string]int64
	usedRequests map[string]int64
	// defTokens/defRequests é o tecto por omissão (chave não configurada).
	defTokens   int64
	defRequests int64
	retryAfter  time.Duration
	nextID      int
}

// NewStaticAdmissionCoordinator constrói a impl de referência com um tecto GLOBAL
// por omissão (tokens/requests por janela) e o retry_after aconselhado no defer.
func NewStaticAdmissionCoordinator(defTokens, defRequests int64, retryAfter time.Duration) *StaticAdmissionCoordinator {
	return &StaticAdmissionCoordinator{
		capTokens:    make(map[string]int64),
		capRequests:  make(map[string]int64),
		usedTokens:   make(map[string]int64),
		usedRequests: make(map[string]int64),
		defTokens:    defTokens,
		defRequests:  defRequests,
		retryAfter:   retryAfter,
	}
}

// SetLimit fixa o tecto GLOBAL de uma chave (provider:model:region).
func (a *StaticAdmissionCoordinator) SetLimit(provider, model, region string, tokens, requests int64) *StaticAdmissionCoordinator {
	k := provider + ":" + model + ":" + region
	a.mu.Lock()
	a.capTokens[k] = tokens
	a.capRequests[k] = requests
	a.mu.Unlock()
	return a
}

// Reserve implementa [AdmissionCoordinator]. Reserva 1 request + EstimatedTokens
// do headroom GLOBAL partilhado. Custo > tecto ⇒ rejeição permanente. Sem headroom
// ⇒ defer (retry_after), NUNCA concede sem débito. É esta reserva a montante que
// impede o colapso agregado.
func (a *StaticAdmissionCoordinator) Reserve(_ context.Context, req AdmissionRequest) (AdmissionOutcome, error) {
	cost := req.EstimatedTokens
	if cost < 1 {
		cost = 1
	}
	const reqCost = int64(1)
	k := req.Provider + ":" + req.Model + ":" + req.Region

	a.mu.Lock()
	defer a.mu.Unlock()

	capT := a.defTokens
	if v, ok := a.capTokens[k]; ok {
		capT = v
	}
	capR := a.defRequests
	if v, ok := a.capRequests[k]; ok {
		capR = v
	}

	// Rejeição PERMANENTE: o custo excede o próprio tecto (nenhum refill o admite).
	if cost > capT || reqCost > capR {
		return AdmissionOutcome{Rejected: true, HeadroomTokens: 0, HeadroomRequests: 0}, nil
	}

	headT := capT - a.usedTokens[k]
	headR := capR - a.usedRequests[k]
	if cost > headT || reqCost > headR {
		// Sem headroom GLOBAL: adia (coordenação — evita o colapso agregado).
		return AdmissionOutcome{
			Granted:          false,
			RetryAfter:       a.retryAfter,
			HeadroomTokens:   headT,
			HeadroomRequests: headR,
		}, nil
	}

	a.usedTokens[k] += cost
	a.usedRequests[k] += reqCost
	a.nextID++
	return AdmissionOutcome{
		Granted:          true,
		ReservationID:    resID(a.nextID),
		HeadroomTokens:   headT - cost,
		HeadroomRequests: headR - reqCost,
	}, nil
}

// Release devolve o débito de uma reserva ao headroom global (reconciliação de
// teste). Chave = provider:model:region.
func (a *StaticAdmissionCoordinator) Release(provider, model, region string, tokens, requests int64) {
	k := provider + ":" + model + ":" + region
	a.mu.Lock()
	defer a.mu.Unlock()
	a.usedTokens[k] -= tokens
	if a.usedTokens[k] < 0 {
		a.usedTokens[k] = 0
	}
	a.usedRequests[k] -= requests
	if a.usedRequests[k] < 0 {
		a.usedRequests[k] = 0
	}
}

func resID(n int) string {
	return "gw-res-" + strconv.Itoa(n)
}
