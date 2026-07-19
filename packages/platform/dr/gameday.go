package dr

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// RPOSource é o subconjunto de [backup.Exporter] (AOS-101) de que o game day depende
// para MEDIR o RPO. O RPO já está medido pelo exportador — o game day apenas o LÊ e
// compara com o alvo. *backup.Exporter satisfá-la.
type RPOSource interface {
	// WithinRPO indica se a periodicidade de exportação satisfaz o RPO-alvo.
	WithinRPO(target time.Duration) bool
	// RPOWindow devolve a janela efectiva de RPO no instante now (now − último Export).
	RPOWindow(now time.Time) time.Duration
	// Periodicity devolve a periodicidade-alvo do ciclo de exportação (base do RPO).
	Periodicity() time.Duration
}

// EvidencePersister persiste a evidência combinada de um game day (AC7: evidência do
// último exercício). É injectada — em produção liga a um store durável/WORM; nos
// testes recolhe em memória. Fail-closed: se a persistência falha, o game day
// devolve erro (a evidência não ficou registada).
type EvidencePersister func(GameDayEvidence) error

// GameDayEvidence é a evidência COMBINADA de um exercício de game day (AC2/AC7):
// a evidência da recuperação (restauro + WORM + fidelidade + retoma) mais as métricas
// de RPO e RTO contra os alvos e o agendamento do próximo exercício. Serializável.
type GameDayEvidence struct {
	Evidence `json:"recovery"`

	// At é o instante do exercício.
	At time.Time `json:"at"`
	// RPOWindow é a janela de RPO medida (do exportador) no instante do exercício.
	RPOWindow time.Duration `json:"rpo_window"`
	// RPOTarget é o alvo de RPO (<= 1 min proposto).
	RPOTarget time.Duration `json:"rpo_target"`
	// RPOWithin indica se o RPO medido cumpre o alvo.
	RPOWithin bool `json:"rpo_within"`
	// RTOTarget é o alvo de RTO (<= 30 min proposto).
	RTOTarget time.Duration `json:"rto_target"`
	// RTOWithin indica se o RTO medido (Evidence.RTO) cumpre o alvo.
	RTOWithin bool `json:"rto_within"`
	// NextExercise é o instante agendado do próximo game day (AC7: cadência periódica).
	NextExercise time.Time `json:"next_exercise"`
	// Passed é o veredicto global: WORM verificado, fidelidade 100%, 0 duplicados e
	// RPO/RTO dentro dos alvos.
	Passed bool `json:"passed"`
}

// GameDay corre o encadeamento de DR contra um Store DESCARTÁVEL, mede RPO (reutiliza
// [RPOSource]) e RTO (wall-clock do [Recoverer], relógio injectável) contra os alvos, e
// PERSISTE a evidência combinada (AC7). É o teste end-to-end operacionalizado como
// exercício periódico.
//
// Seguro para uso concorrente.
type GameDay struct {
	recoverer   *Recoverer
	rpo         RPOSource
	rpoTarget   time.Duration
	rtoTarget   time.Duration
	periodicity time.Duration
	persist     EvidencePersister
	now         func() time.Time

	mu      sync.Mutex
	last    GameDayEvidence
	hasLast bool
}

// GameDayOption configura o [GameDay].
type GameDayOption func(*GameDay)

// WithEvidencePersister liga o sink de persistência da evidência (AC7). Default: nenhum
// (a evidência fica só acessível via [GameDay.Last]).
func WithEvidencePersister(p EvidencePersister) GameDayOption {
	return func(g *GameDay) { g.persist = p }
}

// WithGameDayClock injecta o relógio do game day (carimbo do exercício e agendamento
// do próximo). Default: time.Now. NOTA: o wall-clock do RTO é o do [Recoverer]
// (injectado separadamente em [WithClock]).
func WithGameDayClock(now func() time.Time) GameDayOption {
	return func(g *GameDay) { g.now = now }
}

// NewGameDay constrói o runner. recoverer e rpo são obrigatórios. rpoTarget/rtoTarget
// são os alvos propostos (AC2: <= 1 min / <= 30 min); periodicity é a cadência do
// exercício (AC7). Alvos/periodicidade <= 0 são rejeitados (fail-closed: sem alvo não
// há veredicto).
func NewGameDay(recoverer *Recoverer, rpo RPOSource, rpoTarget, rtoTarget, periodicity time.Duration, opts ...GameDayOption) (*GameDay, error) {
	if recoverer == nil {
		return nil, ErrNilRestorer
	}
	if rpo == nil {
		return nil, fmt.Errorf("dr: fonte de RPO (exportador) em falta")
	}
	if rpoTarget <= 0 || rtoTarget <= 0 || periodicity <= 0 {
		return nil, fmt.Errorf("dr: alvos de RPO/RTO e periodicidade têm de ser > 0")
	}
	g := &GameDay{
		recoverer:   recoverer,
		rpo:         rpo,
		rpoTarget:   rpoTarget,
		rtoTarget:   rtoTarget,
		periodicity: periodicity,
		now:         time.Now,
	}
	for _, o := range opts {
		o(g)
	}
	if g.now == nil {
		g.now = time.Now
	}
	return g, nil
}

// Run executa UM exercício de game day: corre a recuperação de DR (encadeamento
// fail-closed), mede RPO/RTO contra os alvos, agenda o próximo exercício e persiste a
// evidência combinada. Um erro da recuperação propaga-se (o exercício falhou — o DR
// não é dado por válido). A persistência é fail-closed: se falha, devolve erro.
//
// SLO fail-closed (AC2): se a recuperação foi ÍNTEGRA mas o RPO e/ou o RTO medidos
// excederam o alvo, a evidência completa (Passed==false) é PERSISTIDA e DEVOLVIDA, mas
// Run devolve [ErrTargetsExceeded] — assim um chamador que só verifique err==nil não
// confunde um exercício fora do SLO com um game day bem-sucedido. A evidência vem
// preenchida no primeiro valor de retorno mesmo com este erro (para inspecção).
func (g *GameDay) Run(ctx context.Context, rec Recovery) (GameDayEvidence, error) {
	at := g.now().UTC()

	ev, err := g.recoverer.Recover(ctx, rec)
	if err != nil {
		return GameDayEvidence{}, err
	}

	rpoWindow := g.rpo.RPOWindow(at)
	rpoWithin := g.rpo.WithinRPO(g.rpoTarget) && rpoWindow <= g.rpoTarget
	rtoWithin := ev.RTO <= g.rtoTarget

	gde := GameDayEvidence{
		Evidence:     ev,
		At:           at,
		RPOWindow:    rpoWindow,
		RPOTarget:    g.rpoTarget,
		RPOWithin:    rpoWithin,
		RTOTarget:    g.rtoTarget,
		RTOWithin:    rtoWithin,
		NextExercise: at.Add(g.periodicity),
	}
	gde.Passed = ev.AuditVerified &&
		ev.Replay.Fidelity == 1.0 && !ev.Replay.Diverged &&
		ev.Resume.DuplicatedEffects == 0 &&
		rpoWithin && rtoWithin

	if g.persist != nil {
		if err := g.persist(gde); err != nil {
			return GameDayEvidence{}, fmt.Errorf("dr: persistir evidência do game day: %w", err)
		}
	}

	g.mu.Lock()
	g.last = gde
	g.hasLast = true
	g.mu.Unlock()

	// SLO fail-closed (AC2): chegámos aqui só se a recuperação foi ÍNTEGRA (Recover
	// abortou antes em qualquer falha de WORM/fidelidade/duplicação/soberania), pelo
	// que Passed==false implica exclusivamente um RPO/RTO fora do alvo. Devolve a
	// evidência completa JUNTO com o sentinela — evidência inspeccionável, falha visível.
	if !gde.Passed {
		return gde, ErrTargetsExceeded
	}
	return gde, nil
}

// Last devolve a evidência do ÚLTIMO game day corrido (AC7). ok==false se nenhum
// exercício correu ainda.
func (g *GameDay) Last() (GameDayEvidence, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.last, g.hasLast
}

// Periodicity devolve a cadência agendada dos exercícios (AC7).
func (g *GameDay) Periodicity() time.Duration { return g.periodicity }

// Due indica se um novo exercício está em atraso face ao último (now − último >=
// periodicidade). true se nenhum exercício correu ainda (o primeiro está sempre devido).
func (g *GameDay) Due(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.hasLast {
		return true
	}
	return now.UTC().Sub(g.last.At) >= g.periodicity
}
