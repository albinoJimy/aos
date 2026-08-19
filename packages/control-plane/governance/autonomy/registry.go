package autonomy

import (
	"context"
	"sync"
	"time"
)

// Oracle é a PORTA que o caminho de decisão (o PDP, AOS-087) consulta para obter o
// NÍVEL CORRENTE de autonomia de um par (agente, domínio). É a fronteira estável
// entre o registo concreto (GOV) e o consumidor (control-plane/pdp): o PDP aceita
// um Oracle por inversão de dependência e não conhece o armazenamento por trás.
//
// Contrato: LevelFor é O(1) e FAIL-CLOSED — um par sem nível registado devolve
// [L0] (o mais restritivo), nunca um nível elevado por omissão.
type Oracle interface {
	LevelFor(agent, domain string) Level
}

// pairKey é a chave de registo (agente, domínio). O nível é sempre por PAR: o
// mesmo agente pode operar a níveis distintos em domínios distintos (AC2).
type pairKey struct{ agent, domain string }

// LevelChange é um elo do HISTÓRICO de níveis: a transição old→new de um par
// (agente, domínio) com o MOTIVO e o ACTOR (AC5). É o facto que
// [LevelRegistry.SetLevel] sela no audit como evento autonomy.level_changed.
type LevelChange struct {
	Agent  string
	Domain string
	// Old é o nível ANTERIOR (L0 se o par não tinha nível registado).
	Old Level
	// New é o nível que passou a vigorar.
	New Level
	// Reason é o motivo declarado da alteração (obrigatório para responsabilização).
	Reason string
	// Actor é o principal que efectuou a alteração (atribuição no audit).
	Actor string
	// At é o instante (UTC) da alteração.
	At time.Time
}

// Sink sela uma [LevelChange] num registo de audit tamper-evident. É uma PORTA:
// o registo depende dela, não do subsistema de audit concreto. Ver [AuditSink]
// (a impl sobre platform/audit). Uma selagem falhada NÃO é engolida — é devolvida
// por [LevelRegistry.SetLevel] para ser observável.
type Sink interface {
	SealLevelChange(ctx context.Context, ch LevelChange) error
}

// LevelRegistry é o registo (agente, domínio) → [Level] com consulta O(1)
// FAIL-CLOSED, histórico append-only e [LevelRegistry.SetLevel] AUDITÁVEL.
// Satisfaz [Oracle]. É seguro para concorrência. Construir com [NewLevelRegistry].
type LevelRegistry struct {
	mu      sync.RWMutex
	levels  map[pairKey]Level
	history []LevelChange
	now     func() time.Time
	sink    Sink
	// defaultLevel e o PISO dos pares SEM registo. Valor-zero = L0 (fail-closed), pelo que um
	// registo construido sem [WithDefaultLevel] se comporta exactamente como antes.
	defaultLevel Level
}

// RegistryOption configura um [LevelRegistry].
type RegistryOption func(*LevelRegistry)

// WithSink liga o [Sink] que SELA cada alteração de nível no audit (AC5). Sem ele,
// [LevelRegistry.SetLevel] aplica e regista no histórico em memória mas não sela na
// hash-chain WORM — usar [NewAuditSink] em produção.
func WithSink(s Sink) RegistryOption { return func(r *LevelRegistry) { r.sink = s } }

// WithDefaultLevel define o PISO para pares SEM nível registado. Sem esta opção o piso é [L0] —
// o mais supervisionado — e nada muda para quem não a use.
//
// Existe para que o piso seja uma DECLARAÇÃO e não uma herança. Hoje um par desconhecido cai em
// L0 em silêncio: é fail-closed e correcto, mas é uma decisão de governação que ninguém tomou
// explicitamente — e é a razão pela qual "ligar a autonomia" significa, sem mais nada, "todo o
// agente novo bloqueia". A diferença não é de comportamento; é de quem responde por ele.
func WithDefaultLevel(l Level) RegistryOption {
	return func(r *LevelRegistry) {
		if l.Valid() {
			r.defaultLevel = l
		}
	}
}

// WithClock injecta o relógio usado para datar as alterações (testes
// deterministas). Por omissão usa [time.Now] em UTC.
func WithClock(f func() time.Time) RegistryOption {
	return func(r *LevelRegistry) {
		if f != nil {
			r.now = f
		}
	}
}

// NewLevelRegistry constrói um registo VAZIO: todo o par (agente, domínio) começa
// FAIL-CLOSED em [L0] até um [LevelRegistry.SetLevel] explícito.
func NewLevelRegistry(opts ...RegistryOption) *LevelRegistry {
	r := &LevelRegistry{
		levels: make(map[pairKey]Level),
		now:    func() time.Time { return time.Now().UTC() },
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// LevelFor implementa [Oracle]: devolve o nível corrente do par, FAIL-CLOSED em
// [L0] se o par não tiver nível registado. O(1).
func (r *LevelRegistry) LevelFor(agent, domain string) Level {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if lvl, ok := r.levels[pairKey{agent, domain}]; ok {
		return lvl
	}
	// PISO. Valor-zero = L0, portanto um registo construído sem [WithDefaultLevel] comporta-se
	// exactamente como antes — fail-closed no mais supervisionado.
	return r.defaultLevel
}

// Get devolve o nível registado do par e um bool a indicar se HAVIA registo
// (distingue "explicitamente L0" de "sem nível" — ambos operam como L0 mas só o
// primeiro tem histórico). O(1).
func (r *LevelRegistry) Get(agent, domain string) (Level, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	lvl, ok := r.levels[pairKey{agent, domain}]
	return lvl, ok
}

// SetLevel fixa o nível do par (agente, domínio), regista a transição no histórico
// e SELA um evento autonomy.level_changed no audit (AC5). Devolve a [LevelChange]
// aplicada.
//
// FAIL-CLOSED na validação: um nível fora de L0–L5 é REJEITADO ([ErrInvalidLevel])
// sem mutar o registo; um par incompleto é rejeitado ([ErrEmptyPair]); um motivo
// vazio ([ErrMissingReason]) ou actor vazio ([ErrMissingActor]) é rejeitado (AC5:
// sem alterações anónimas ou sem justificação na hash-chain).
//
// SEMÂNTICA DA SELAGEM (molde policy.changed de AOS-088): a alteração é aplicada ao
// registo ANTES da selagem e a mutação em memória NÃO é revertida se o [Sink]
// falhar — a selagem falhada é DEVOLVIDA (não engolida) junto com a change já
// aplicada, para que uma alteração de nível sem changelog selado seja detectável
// por quem chama. Sem [Sink] configurado a selagem é no-op (devolve nil).
func (r *LevelRegistry) SetLevel(ctx context.Context, agent, domain string, level Level, reason, actor string) (LevelChange, error) {
	if !level.Valid() {
		return LevelChange{}, ErrInvalidLevel
	}
	if agent == "" || domain == "" {
		return LevelChange{}, ErrEmptyPair
	}
	// AC5: a alteração é um evento auditável COM motivo e atribuição de
	// responsabilidade. Rejeitar motivo/actor vazios ANTES de mutar/selar, para
	// que a hash-chain nunca contenha uma promoção anónima ou sem justificação.
	if reason == "" {
		return LevelChange{}, ErrMissingReason
	}
	if actor == "" {
		return LevelChange{}, ErrMissingActor
	}

	r.mu.Lock()
	k := pairKey{agent, domain}
	old := r.levels[k] // ausente ⇒ L0 (o valor-zero é o fail-closed correcto)
	ch := LevelChange{
		Agent:  agent,
		Domain: domain,
		Old:    old,
		New:    level,
		Reason: reason,
		Actor:  actor,
		At:     r.now(),
	}
	r.levels[k] = level
	r.history = append(r.history, ch)
	sink := r.sink
	r.mu.Unlock()

	// Selagem fora do lock (I/O do audit não bloqueia consultas O(1) concorrentes).
	if sink != nil {
		if err := sink.SealLevelChange(ctx, ch); err != nil {
			return ch, err
		}
	}
	return ch, nil
}

// History devolve uma CÓPIA do histórico completo de alterações, por ordem de
// aplicação. Cópia defensiva: o chamador não partilha o slice interno.
func (r *LevelRegistry) History() []LevelChange {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]LevelChange, len(r.history))
	copy(out, r.history)
	return out
}

// HistoryFor devolve as alterações de um par (agente, domínio) específico, por
// ordem de aplicação (cópia defensiva).
func (r *LevelRegistry) HistoryFor(agent, domain string) []LevelChange {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]LevelChange, 0)
	for _, ch := range r.history {
		if ch.Agent == agent && ch.Domain == domain {
			out = append(out, ch)
		}
	}
	return out
}

// ClassPrefix marca uma entrada de nível cujo alvo é uma CLASSE de agente e não uma instância.
//
// Porquê um prefixo e não um mapa separado: assim uma regra de classe passa pelo MESMO SetLevel —
// logo pelo mesmo selo na hash-chain, o mesmo histórico e o mesmo Get. Não há segunda máquina de
// estado a manter em sincronia, e uma regra de classe é tão auditável como uma de instância.
//
// A fronteira de configuração RECUSA um agente cujo id comece por este prefixo, para o namespace
// não ser ambíguo.
const ClassPrefix = "class:"

// ClassOracle é a resolução EM CASCATA: instância → classe → piso.
//
// É uma interface SEPARADA de [Oracle] de propósito. Quem só implementa LevelFor continua a
// funcionar sem alterações, e quem precisa da cascata faz um type-assert. Alargar a Oracle
// obrigaria todos os implementadores (incluindo os duplos de teste) a mudar de assinatura, o que
// transforma uma adição numa migração.
type ClassOracle interface {
	LevelForAgentOrClass(agent, class, domain string) Level
}

// LevelForAgentOrClass resolve do MAIS ESPECÍFICO para o mais geral:
//
//	(agente, domínio)  →  (class:<classe>, domínio)  →  piso  →  L0
//
// A instância ganha à classe, e a classe ganha ao piso. É o que torna a cascata utilizável sem
// abrir nada: continua a poder tratar-se um agente à parte quando há razão para isso, sem obrigar
// a enumerar identidades que ainda não existem — e os agent_id deste sistema são cunhados por run.
func (r *LevelRegistry) LevelForAgentOrClass(agent, class, domain string) Level {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if lvl, ok := r.levels[pairKey{agent, domain}]; ok {
		return lvl
	}
	if class != "" {
		if lvl, ok := r.levels[pairKey{ClassPrefix + class, domain}]; ok {
			return lvl
		}
	}
	return r.defaultLevel
}
