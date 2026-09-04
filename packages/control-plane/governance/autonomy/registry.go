package autonomy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ErrSealFailed — a selagem no [Sink] falhou e, por isso, a alteração de nível NÃO
// entrou em vigor (AOS-306, audit-before-effect). [LevelRegistry.SetLevel] devolve-o
// EMBRULHADO com o erro do sink (`errors.Is(err, ErrSealFailed)` e
// `errors.Is(err, <erro do sink>)` funcionam ambos — dois %w), para que o consumidor
// (o handler HTTP) distinga «WORM indisponível» (5xx) de «pedido inválido» (4xx).
var ErrSealFailed = errors.New("autonomy: selagem da alteracao de nivel falhou; nivel NAO aplicado")

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

// LevelChangeProof é a PROVA de que uma alteração de nível foi PEDIDA por quem o
// registo diz — o emissor e a assinatura ed25519 EXACTA que produziu, com o nonce e o
// carimbo que essa assinatura cobre.
//
// PORQUE EXISTE (achado de revisão sobre AOS-307). Desde que o arranque REIDRATA os
// níveis a partir do WORM, um registo `autonomy.level_changed` deixou de ser só um
// rasto e passou a ser uma ENTRADA DE PRIVILÉGIO. O `EntryHash` da hash-chain é um
// SHA-256 SEM CHAVE: quem consiga escrever no ficheiro do store consegue encadear um
// registo forjado que a re-verificação aceita, porque re-encadear é aritmética pública.
// Sem estes campos, o `Actor` era uma string que o próprio atacante escrevia.
//
// A prova é transportada nos `Params` da obrigação, que entram no conteúdo canónico do
// `EntryHash` — logo é ela própria tamper-evident DENTRO da cadeia — mas o que a torna
// útil é ser verificável FORA dela: contra as pubkeys dos operadores que o nó recebe do
// ambiente, que o escritor do WORM não controla.
//
// Só material PÚBLICO: identificador, assinatura, nonce e carimbo. Nenhum segredo.
type LevelChangeProof struct {
	// EmitterID é o operador que assinou (o `id` do control.Emitter).
	EmitterID string `json:"emitter_id"`
	// SignatureB64 é base64(assinatura ed25519) sobre o tuplo canónico do pedido.
	SignatureB64 string `json:"signature_b64"`
	// NonceB64 é base64(nonce de uso-único) — parte do tuplo assinado, por isso tem de
	// ser guardado para a assinatura voltar a verificar.
	NonceB64 string `json:"nonce_b64"`
	// IssuedAtRFC3339 é o carimbo do pedido (RFC3339 com nanos) — também coberto pela
	// assinatura. A FRESCURA não é reavaliada na rehidratação (ver o validador do nó):
	// reler o passado é, por definição, reler carimbos velhos.
	IssuedAtRFC3339 string `json:"issued_at"`
}

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
	// Proofs são as assinaturas do PEDIDO que originou a alteração (ver
	// [LevelChangeProof]). Vazio para alterações que não nascem de um pedido assinado —
	// o provisionamento por configuração é o caso legítimo. Quem REIDRATA decide o que
	// aceitar sem prova; este pacote só as transporta e sela.
	Proofs []LevelChangeProof
}

// Sink sela uma [LevelChange] num registo de audit tamper-evident. É uma PORTA:
// o registo depende dela, não do subsistema de audit concreto. Ver [AuditSink]
// (a impl sobre platform/audit). Uma selagem falhada NÃO é engolida — é devolvida
// por [LevelRegistry.SetLevel] (embrulhada em [ErrSealFailed]) e a alteração NÃO
// é aplicada (AOS-306).
type Sink interface {
	SealLevelChange(ctx context.Context, ch LevelChange) error
}

// LevelRegistry é o registo (agente, domínio) → [Level] com consulta O(1)
// FAIL-CLOSED, histórico append-only e [LevelRegistry.SetLevel] AUDITÁVEL.
// Satisfaz [Oracle]. É seguro para concorrência. Construir com [NewLevelRegistry].
type LevelRegistry struct {
	// mu protege levels/history. As LEITURAS ([LevelRegistry.LevelFor], Get, History...)
	// tomam só mu.RLock e são O(1)/O(n) em memória — NUNCA esperam por I/O.
	mu sync.RWMutex
	// writeMu serializa os ESCRITORES ([LevelRegistry.SetLevel], [LevelRegistry.Rehydrate])
	// durante toda a operação, INCLUINDO a selagem no [Sink] (AOS-306: selar antes de
	// aplicar). Só se toma mu para ler o nível antigo e para aplicar. Assim dois SetLevel
	// concorrentes serializam-se (raro — são acções humanas) e as leituras nunca ficam
	// presas atrás do WORM. A ordem de aquisição é sempre writeMu → mu, nunca o inverso.
	writeMu sync.Mutex
	levels  map[pairKey]Level
	history []LevelChange
	// last é a ÚLTIMA alteração aplicada POR PAR — o índice que torna [LevelRegistry.LastChange]
	// e [LevelRegistry.Pairs] O(1) e O(pares) em vez de O(histórico).
	//
	// PORQUE PASSOU A FAZER FALTA: antes de AOS-307 o histórico era reposto a zero em cada
	// arranque e tinha tantas entradas quantos os pares declarados. Desde que o arranque reidrata
	// a partição `autonomy` inteira, ele passou a conter TODAS as alterações da vida do WORM — e
	// `LastChange` (chamado uma vez por par declarado no provisionamento) e o `GET /autonomy`
	// varriam-no por inteiro. Um nó de vida longa com promoções frequentes pagava isso em cada
	// arranque e em cada leitura do plano de controlo.
	//
	// O invariante — o índice nunca diverge do histórico — é barato de manter porque há UM só
	// sítio que os escreve, [LevelRegistry.restore], e é fixado por teste.
	last map[pairKey]LevelChange
	now  func() time.Time
	sink Sink
	// defaultLevel e o PISO dos pares SEM registo. Valor-zero = L0 (fail-closed), pelo que um
	// registo construido sem [WithDefaultLevel] se comporta exactamente como antes.
	defaultLevel Level
}

// RegistryOption configura um [LevelRegistry].
type RegistryOption func(*LevelRegistry)

// WithSink liga o [Sink] que SELA cada alteração de nível no audit (AC5) ANTES de a
// aplicar (AOS-306). Sem ele, [LevelRegistry.SetLevel] aplica e regista no histórico
// em memória sem selar na hash-chain WORM (testes/dev) — usar [NewAuditSink] em produção.
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
		last:   make(map[pairKey]LevelChange),
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
// SEMÂNTICA DA SELAGEM (AOS-306, audit-before-effect — o idioma do Reference Monitor):
// selar ANTES de aplicar. Uma alteração de nível sem changelog selado NUNCA entra em
// vigor: se o [Sink] falhar, devolve-se `(LevelChange{}, err)` com err embrulhado em
// [ErrSealFailed] (a causa do sink continua acessível por errors.Is) e nem o registo
// nem o histórico mutam. A excepção é o sink nil, que aplica sem selar (testes/dev).
//
// Concorrência: writeMu é detido durante toda a operação (incluindo a selagem); mu é
// tomado só para ler o nível antigo e para aplicar. Leituras nunca esperam pelo WORM.
func (r *LevelRegistry) SetLevel(ctx context.Context, agent, domain string, level Level, reason, actor string) (LevelChange, error) {
	return r.SetLevelWithProof(ctx, agent, domain, level, reason, actor, nil)
}

// SetLevelWithProof é [LevelRegistry.SetLevel] com as PROVAS do pedido que originou a
// alteração (ver [LevelChangeProof]) — as assinaturas ficam seladas nos Params do evento
// `autonomy.level_changed`, e é isso que torna o registo verificável FORA da hash-chain
// quando o arranque o reidratar.
//
// PORQUE UM MÉTODO NOVO E NÃO UM PARÂMETRO A MAIS EM SetLevel: [LevelRegistry.SetLevel]
// tem chamadores em três sítios (provisionamento do nó, controlador, rota) e é a
// assinatura que os testes de módulo usam. Acrescentar-lhe um parâmetro obrigaria a
// tocar em todos por causa de um caso — o da rota assinada — em que a prova existe. O
// caminho sem prova continua a ser exactamente o de antes, byte a byte: sem provas, o
// evento selado não ganha nenhum param novo.
func (r *LevelRegistry) SetLevelWithProof(ctx context.Context, agent, domain string, level Level, reason, actor string, proofs []LevelChangeProof) (LevelChange, error) {
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

	// Serializa os escritores: o `old` lido a seguir é estável até aplicarmos, porque
	// nenhum outro SetLevel/Rehydrate pode intercalar-se.
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	k := pairKey{agent, domain}
	r.mu.RLock()
	old := r.levels[k] // ausente ⇒ L0 (o valor-zero é o fail-closed correcto)
	r.mu.RUnlock()
	ch := LevelChange{
		Agent:  agent,
		Domain: domain,
		Old:    old,
		New:    level,
		Reason: reason,
		Actor:  actor,
		At:     r.now(),
		Proofs: proofs,
	}

	// 1) SELAR — fora de mu (as leituras O(1) continuam a responder), dentro de writeMu.
	if r.sink != nil {
		if err := r.sink.SealLevelChange(ctx, ch); err != nil {
			return LevelChange{}, fmt.Errorf("%w: %w", ErrSealFailed, err)
		}
	}
	// 2) APLICAR — só depois de o selo existir na hash-chain.
	r.restore(ch)
	return ch, nil
}

// restore aplica uma [LevelChange] ao registo e ao histórico SEM selar. É o passo
// «efeito» de [LevelRegistry.SetLevel] (depois do selo) e o passo de reposição de
// [LevelRegistry.Rehydrate] (o selo já existe no WORM). O chamador detém writeMu.
func (r *LevelRegistry) restore(ch LevelChange) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := pairKey{ch.Agent, ch.Domain}
	r.levels[k] = ch.New
	r.history = append(r.history, ch)
	// O ÚNICO sítio que escreve o histórico é também o único que escreve o índice — é o que
	// impede os dois de divergirem. Um caminho de escrita novo que não passe por aqui parte o
	// invariante, e há um teste que o apanha.
	r.last[k] = ch
}

// LastChange devolve a ÚLTIMA alteração aplicada ao par (agente, domínio) e um bool a
// indicar se houve alguma. Serve ao arranque do nó (AOS-307) para decidir precedência
// entre o nível reidratado do WORM e o declarado no ambiente — p.ex. «se o último actor
// do par foi a configuração, o ambiente pode sobrepor-se; senão, o WORM prevalece».
// O(1) sobre o índice `last`, em memória, sem I/O.
func (r *LevelRegistry) LastChange(agent, domain string) (LevelChange, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ch, ok := r.last[pairKey{agent, domain}]
	return ch, ok
}

// Pairs devolve a ÚLTIMA alteração de CADA par com nível registado, ordenada por (agente,
// domínio) — o ESTADO corrente, distinto do TRILHO que [LevelRegistry.History] devolve.
//
// Existe para que quem só quer saber «o que vigora agora» não pague o histórico inteiro: o
// `GET /autonomy` reconstruía os pares iterando `History()` — uma cópia defensiva de todas as
// alterações já seladas na vida do WORM — e deduplicando a seguir. Com a reidratação de AOS-307
// isso passou a ser linear no trilho por cada leitura de uma rota do plano de controlo.
//
// Devolve `LevelChange` e não só o nível porque o chamador precisa do `Actor` e do `Reason`
// tanto quanto do valor — é o que distingue «este nível veio de uma decisão assinada» de «veio
// da configuração», e é a mesma informação que a precedência do arranque usa.
//
// Ordenada de propósito: a resposta do `GET` é ordenada, e ordenar aqui garante que dois
// chamadores nunca vêem ordens diferentes do mesmo estado.
func (r *LevelRegistry) Pairs() []LevelChange {
	r.mu.RLock()
	out := make([]LevelChange, 0, len(r.last))
	for _, ch := range r.last {
		out = append(out, ch)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		return out[i].Domain < out[j].Domain
	})
	return out
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
