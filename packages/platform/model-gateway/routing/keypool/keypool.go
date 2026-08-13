// Package keypool selecciona a CHAVE DE INFRA pooled do Model Gateway por
// THROUGHPUT (TPM/RPM), por conta/região, DESACOPLADA da identidade do principal
// (AOS-057, tecnica/06 §4, ADR-011).
//
// # A invariante central (o coração do ticket)
//
// A selecção da chave de infra é o eixo do *throughput*; a identidade do
// principal é o eixo da ATRIBUIÇÃO. AOS-057 separa os dois eixos que o padrão
// ingénuo (credential pool round-robin) funde. A prova é ESTRUTURAL: a função de
// selecção — [Registry.Select] / [Pool.Select] — recebe APENAS (provider,
// região); a identidade do principal NÃO existe na sua assinatura e NÃO é
// consultada. É impossível, por construção, a escolha da chave depender de quem
// actua. O registo de atribuição (principal/modelo/região) é produzido noutro
// eixo (ver metering/attribution) e nunca "o pool".
//
// # Segredos (ADR-006)
//
// Este pacote NÃO lida com segredos: opera sobre [Account.KeyID], um
// identificador NÃO-SECRETO de conta (ex.: "acct-eu-1"). O segredo concreto vem
// do Credential Broker/Vault (AOS-056), server-side, por outra porta. O KeyID
// escolhido serve só para (a) contabilizar throughput e (b) alimentar a
// atribuição com a chave NÃO-secreta usada.
//
// # Determinismo
//
// A selecção é DETERMINISTA: escolhe a conta com mais folga de THROUGHPUT — a
// menor utilização relativa do PIOR eixo (RPM ou TPM), de modo que ambos os eixos
// entram no ranking (não só o RPM), com desempate estável por KeyID ascendente.
// Sem aleatoriedade real; o estado de carga é injectável nos testes. Seguro para
// uso concorrente.
package keypool

import (
	"errors"
	"sort"
	"sync"
)

// ErrNoPool — não há pool configurado para o par (provider, região). Fail-closed:
// o gateway recusa a chamada em vez de inventar uma conta.
var ErrNoPool = errors.New("keypool: sem pool configurado para provider/regiao")

// ErrNoCapacity — todas as contas do pool estão saturadas (RPM/TPM no limite).
// Fail-closed: nenhuma chave é servida acima do seu limite de throughput.
var ErrNoCapacity = errors.New("keypool: pool saturado (sem capacidade de throughput)")

// unlimited representa a ausência de limite (limite <= 0) para efeitos de
// comparação de utilização, sem overflow.
const unlimited = 1 << 30

// Account é uma conta de infra pooled: um identificador NÃO-SECRETO (KeyID) e os
// seus limites de throughput (RPM/TPM). Uma conta com limite <= 0 é tratada como
// ilimitada nesse eixo.
type Account struct {
	// KeyID é o identificador NÃO-SECRETO e estável da conta/chave (ex.: "acct-eu-1").
	// NUNCA é o segredo (ADR-006).
	KeyID string
	// LimitRPM é o limite de pedidos por minuto da conta (<=0 = ilimitado).
	LimitRPM int
	// LimitTPM é o limite de tokens por minuto da conta (<=0 = ilimitado).
	LimitTPM int
}

// accountState é o estado mutável de carga observada de uma conta.
type accountState struct {
	acct Account
	rpm  int // pedidos observados na janela corrente
	tpm  int // tokens observados na janela corrente
}

// saturated indica se a conta atingiu qualquer um dos seus limites.
func (a *accountState) saturated() bool {
	if a.acct.LimitRPM > 0 && a.rpm >= a.acct.LimitRPM {
		return true
	}
	if a.acct.LimitTPM > 0 && a.tpm >= a.acct.LimitTPM {
		return true
	}
	return false
}

// utilRatio é a utilização relativa (carga/limite) de uma conta num eixo, como um
// racional inteiro num/den (sem floats). den é sempre > 0 (limite <=0 vira
// unlimited a montante).
type utilRatio struct{ num, den int64 }

// less reporta se a utilização a é ESTRITAMENTE menor que b, por multiplicação
// cruzada inteira (determinista, sem floats). den > 0 em ambos.
func (a utilRatio) less(b utilRatio) bool { return a.num*b.den < b.num*a.den }

// worstAxisLoad devolve a utilização relativa do PIOR eixo (o maior entre RPM e
// TPM) da conta. A selecção por throughput ordena por este valor: prefere a conta
// cujo eixo mais carregado tem mais folga — assim o TPM entra no RANKING (não só
// como porta de saturação), como o critério de aceitação pede ('por TPM/RPM').
func worstAxisLoad(a *accountState) utilRatio {
	limR := int64(a.acct.LimitRPM)
	if limR <= 0 {
		limR = unlimited
	}
	limT := int64(a.acct.LimitTPM)
	if limT <= 0 {
		limT = unlimited
	}
	rpm := utilRatio{num: int64(a.rpm), den: limR}
	tpm := utilRatio{num: int64(a.tpm), den: limT}
	if rpm.less(tpm) {
		return tpm
	}
	return rpm
}

// lessLoaded reporta se a tem ESTRITAMENTE mais folga de throughput do que b —
// isto é, se o PIOR eixo (RPM ou TPM) de a está menos utilizado que o pior eixo de
// b. Determinista, sem floats.
func lessLoaded(a, b *accountState) bool {
	return worstAxisLoad(a).less(worstAxisLoad(b))
}

// Pool é o conjunto de contas de infra elegíveis para um par (provider, região).
// Concorrente-seguro.
type Pool struct {
	mu       sync.Mutex
	accounts []*accountState
}

// NewPool constrói um pool sobre as contas dadas (ordem irrelevante: a selecção é
// determinista por carga + KeyID). Contas com KeyID vazio são ignoradas.
func NewPool(accounts ...Account) *Pool {
	p := &Pool{}
	for _, a := range accounts {
		if a.KeyID == "" {
			continue
		}
		p.accounts = append(p.accounts, &accountState{acct: a})
	}
	// Ordem canónica por KeyID: torna o desempate estável e independente da ordem
	// de inserção.
	sort.Slice(p.accounts, func(i, j int) bool {
		return p.accounts[i].acct.KeyID < p.accounts[j].acct.KeyID
	})
	return p
}

// Select escolhe a conta com mais folga de throughput e devolve o seu KeyID
// NÃO-SECRETO. NÃO recebe a identidade do principal — a selecção é, por
// construção, independente de quem actua (a invariante central de AOS-057).
// Incrementa o RPM observado da conta escolhida (uma chamada consumida), de modo
// que chamadas sucessivas distribuem a carga pelas contas do pool. Fail-closed
// ([ErrNoCapacity]) se todas estiverem saturadas.
func (p *Pool) Select() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.accounts) == 0 {
		return "", ErrNoPool
	}
	var best *accountState
	for _, a := range p.accounts {
		if a.saturated() {
			continue
		}
		if best == nil || lessLoaded(a, best) {
			best = a
		}
		// Desempate: as contas já estão ordenadas por KeyID ascendente e só
		// substituímos best em caso de folga ESTRITAMENTE maior, pelo que em empate
		// permanece a de KeyID menor (determinismo).
	}
	if best == nil {
		return "", ErrNoCapacity
	}
	best.rpm++
	return best.acct.KeyID, nil
}

// SetLoad injecta a carga observada (rpm/tpm) de uma conta — uso determinista em
// testes (simula um estado de throughput sem relógio nem rede). Contas
// desconhecidas são ignoradas.
func (p *Pool) SetLoad(keyID string, rpm, tpm int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.acct.KeyID == keyID {
			a.rpm, a.tpm = rpm, tpm
			return
		}
	}
}

// Headroom devolve a FOLGA DE THROUGHPUT do pool como racional inteiro do PIOR
// eixo (RPM ou TPM) da conta que [Pool.Select] escolheria — a menos carregada
// não-saturada. É a MESMA aritmética que já governa a selecção (worstAxisLoad),
// exposta para leitura: quem precisa do sinal de carga do pool (o router
// cost/load-aware de AOS-059, pela porta LoadProvider) lê-o daqui em vez de manter
// uma contabilidade paralela de TPM/RPM que divergiria desta.
//
// NÃO CONSOME NADA (ao contrário de [Pool.Select], que incrementa o RPM da conta
// escolhida): é uma leitura pura, chamável no caminho de decisão sem alterar a
// distribuição de carga. saturated=true quando TODAS as contas estão no limite (ou
// o pool está vazio) — o lado seguro: sem capacidade não se presume folga.
//
// Sem floats (determinismo): devolve o par (usado, limite) do pior eixo; um limite
// <=0 (ilimitado) é reportado como [unlimited] para que a razão usado/limite seja
// comparável entre contas sem divisão por zero.
func (p *Pool) Headroom() (used, limit int64, saturated bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var best *accountState
	for _, a := range p.accounts {
		if a.saturated() {
			continue
		}
		if best == nil || lessLoaded(a, best) {
			best = a
		}
	}
	if best == nil {
		return 0, 0, true
	}
	r := worstAxisLoad(best)
	return r.num, r.den, false
}

// Observe acrescenta tokens consumidos ao TPM da conta (feedback de throughput
// após a resposta). Contas desconhecidas são ignoradas.
func (p *Pool) Observe(keyID string, tokens int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if a.acct.KeyID == keyID {
			a.tpm += tokens
			return
		}
	}
}

// Registry mapeia (provider, região) → [Pool]. É o ponto que o Gateway consulta:
// [Registry.Select] recebe APENAS (provider, região) — nunca o principal.
// Concorrente-seguro.
type Registry struct {
	mu    sync.RWMutex
	pools map[string]*Pool
}

// NewRegistry constrói um registo vazio.
func NewRegistry() *Registry {
	return &Registry{pools: map[string]*Pool{}}
}

func key(provider, region string) string { return provider + "|" + region }

// Register associa um pool ao par (provider, região).
func (r *Registry) Register(provider, region string, pool *Pool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pools[key(provider, region)] = pool
}

// Select escolhe a chave de infra do pool de (provider, região) por throughput.
// A ASSINATURA não contém a identidade do principal: é a prova estrutural do
// desacoplamento (AOS-057). Fail-closed ([ErrNoPool]) se não há pool para o par.
func (r *Registry) Select(provider, region string) (string, error) {
	r.mu.RLock()
	pool, ok := r.pools[key(provider, region)]
	r.mu.RUnlock()
	if !ok {
		return "", ErrNoPool
	}
	return pool.Select()
}

// Headroom devolve a folga de throughput do pool de (provider, região) — o sinal
// de CARGA que o router cost/load-aware consome pela porta LoadProvider. Um par SEM
// pool é reportado como SATURADO (saturated=true), não como folga desconhecida: o
// lado seguro é «sem pool não há capacidade», coerente com o fail-closed de
// [Registry.Select] ([ErrNoPool]). Leitura pura (não consome throughput).
func (r *Registry) Headroom(provider, region string) (used, limit int64, saturated bool) {
	r.mu.RLock()
	pool, ok := r.pools[key(provider, region)]
	r.mu.RUnlock()
	if !ok {
		return 0, 0, true
	}
	return pool.Headroom()
}

// Observe encaminha o feedback de tokens para o pool do par (provider, região).
func (r *Registry) Observe(provider, region, keyID string, tokens int) {
	r.mu.RLock()
	pool, ok := r.pools[key(provider, region)]
	r.mu.RUnlock()
	if ok {
		pool.Observe(keyID, tokens)
	}
}
