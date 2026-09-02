package dr

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/replay"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/backup"
	"github.com/aos-ref/substrate/eventstore"
)

// Restorer é o subconjunto de [backup.Restorer] (AOS-101) de que o orquestrador
// depende: VerifyManifest está EMBUTIDO em RestoreTo (verifica ANTES de escrever;
// fail-closed em adulteração — ErrSegmentTampered/ErrChainBroken/ErrCheckpointStale).
// A interface existe para o orquestrador não acoplar o tipo concreto e para os testes
// injectarem um restaurador de observação. *backup.Restorer satisfá-la.
type Restorer interface {
	// RestoreTo verifica o backup e reinsere o log até target (PITR por stream) no
	// sink, devolvendo a evidência assinada do restauro.
	RestoreTo(ctx context.Context, m backup.Manifest, cp backup.Checkpoint, expectedHead uint64, target map[string]uint64, sink eventstore.RestoreSink) (backup.RestoreEvidence, error)
}

// StoreFactory constrói um Event Store de DR LIMPO ligado à fronteira-alvo. TEM de o
// construir com eventstore.WithSovereigntyBoard(board, region) — assim o Store RECUSA
// cross-border por construção (réplicas fora da fronteira ⇒ ErrSovereigntyViolation).
// É injectada para o chamador controlar a topologia de réplicas; o orquestrador
// ASSERTA que a região do Store devolvido == região-alvo (AC6). O Store devolvido
// serve as três portas de que a recuperação precisa: eventstore.RestoreSink (escrita
// de restauro), replay.EventReader (leitura de replay) e a base durável da retoma.
type StoreFactory func(board, region string) (*eventstore.Store, error)

// ResumeFn RE-CONDUZ a execução resume-from-step sobre o Event Store de DR JÁ
// restaurado, devolvendo a evidência da retoma. A idempotência por passo (StepLedger,
// chave f(run_id,step_id)) GARANTE 0 efeitos duplicados na retoma (AC4); o chamador
// mede-os e reporta-os em [ResumeEvidence.DuplicatedEffects] (o orquestrador aborta
// fail-closed se != 0). É injectada porque o chamador detém o wiring do worker
// (LeaseManager/FencedAppender/StepLedger/Resumer/Checkpointer) e do Reference Monitor;
// o orquestrador fornece o Store restaurado e NUNCA re-despacha por outra via.
//
// A função DEVE operar EXCLUSIVAMENTE sobre dr (o Store restaurado, descartável) —
// nunca sobre um Store de produção.
type ResumeFn func(ctx context.Context, dr *eventstore.Store) (ResumeEvidence, error)

// AuditCheck são os inputs da verificação hash-chain do audit WORM pós-restauro
// (AC5, AOS-072/083). O orquestrador chama [audit.VerifyFromCheckpointAtHead] ANTES de
// retomar: a assinatura do checkpoint é validada, o rollback/truncatura de tail é
// rejeitado ([audit.ErrCheckpointStale] quando AuditSeq < ExpectedHead) e o intervalo
// Checkpoint.AuditSeq+1..To é reprocessado contra a âncora assinada. Qualquer falha
// aborta — o serviço não é dado por restabelecido a partir de um WORM não-íntegro.
type AuditCheck struct {
	// Store é o WORM (survivente ao desastre, imutável) cuja cadeia se verifica.
	Store audit.Store
	// Public é a chave pública (raiz de confiança) que assina os checkpoints do WORM.
	Public ed25519.PublicKey
	// Checkpoint é a âncora assinada da cadeia a partir da qual se verifica.
	Checkpoint audit.Checkpoint
	// ExpectedHead é o piso de FRESCURA: um checkpoint com AuditSeq inferior é um
	// rollback e é rejeitado (fecha o vector de reapresentação de checkpoint antigo).
	ExpectedHead uint64
	// To é o audit_seq até onde a cadeia é percorrida a partir da âncora.
	To uint64
}

// Recovery descreve UMA recuperação de DR: o board (fronteira-alvo), o run a retomar,
// o backup a restaurar (manifesto + checkpoint assinado + piso de frescura + alvo de
// PITR), os inputs determinísticos do replay, a verificação do WORM e a função de
// retoma. É DADOS — o orquestrador executa-a como transacção fail-closed.
type Recovery struct {
	// Board é o board de soberania a recuperar; resolve-se para a região-alvo.
	Board string
	// RunID é o run/stream cuja trajectória se prova por replay e se retoma.
	RunID string
	// Manifest é o manifesto hash-chain do backup a restaurar (AOS-101).
	Manifest backup.Manifest
	// Backup é o checkpoint assinado do head do backup (âncora do manifesto).
	Backup backup.Checkpoint
	// ExpectedHead é o piso de frescura do checkpoint do BACKUP (rejeita um manifesto
	// anterior ao head conhecido — rollback de segmentos).
	ExpectedHead uint64
	// Target é o instante-alvo do PITR por stream (seq-alvo; nil ⇒ restaura por inteiro).
	Target map[string]uint64
	// Spec são os inputs DETERMINÍSTICOS re-fornecidos ao replay (system, tool set
	// congelado, objectivo, memory_context, model/params/seed e assembly esperados)
	// para re-materializar e VERIFICAR o prompt de cada turno (AC3).
	Spec replay.TrajectorySpec
	// Audit são os inputs da verificação hash-chain do WORM pós-restauro (AC5).
	Audit AuditCheck
	// Resume é a função que retoma resume-from-step sobre o Store restaurado (AC4).
	Resume ResumeFn
}

func (r Recovery) validate() error {
	if r.Board == "" {
		return ErrEmptyBoard
	}
	if r.RunID == "" {
		return ErrEmptyRunID
	}
	if r.Resume == nil {
		return ErrNilResume
	}
	if r.Audit.Store == nil {
		return ErrNilAuditStore
	}
	return nil
}

// ReplaySummary é a projecção da prova de fidelidade do replay para a evidência
// (AC3): 100% quando Fidelity==1.0 && !Diverged.
type ReplaySummary struct {
	// Fidelity é a fracção de turnos verificados cujo hash coincidiu (1.0 = 100%).
	Fidelity float64 `json:"fidelity"`
	// FinalStateHash é o fingerprint do estado final reconstruído (prova de
	// equivalência entre replay completo e resume do mesmo run).
	FinalStateHash string `json:"final_state_hash"`
	// Turns é o nº de turnos reproduzidos e verificados.
	Turns int `json:"turns"`
	// Diverged indica se houve divergência localizada (deve ser false para restabelecer).
	Diverged bool `json:"diverged"`
}

// ResumeEvidence é a evidência da retoma resume-from-step (AC1/AC4). DuplicatedEffects
// TEM de ser 0 — a idempotência por passo garante-o e o orquestrador aborta se != 0.
type ResumeEvidence struct {
	// ResumeTurn é o turno (1-based) em que a retoma recomeçou (NextTurn do Resumer).
	ResumeTurn int `json:"resume_turn"`
	// Executed é o nº de passos que a retoma processou (pode incluir a re-execução do
	// passo de fronteira, cujo efeito o ledger DEDUPLICA).
	Executed int `json:"executed"`
	// Skipped é o nº de passos já confirmados que a retoma saltou (resume-from-step).
	Skipped int `json:"skipped"`
	// DuplicatedEffects é o nº de efeitos externos duplicados MEDIDOS na retoma. Tem de
	// ser 0 (idempotency key = f(run_id,step_id)); != 0 aborta o DR fail-closed.
	DuplicatedEffects int `json:"duplicated_effects"`
}

// Evidence é a EVIDÊNCIA combinada de uma recuperação de DR bem-sucedida: prova, com
// timestamps, que o log foi restaurado e verificado (backup + WORM), que o replay
// reproduziu 100% e que a retoma não duplicou efeitos — tudo dentro da fronteira de
// soberania. É serializável (persistida pelo game day, AC7).
type Evidence struct {
	RunID  string `json:"run_id"`
	Board  string `json:"board"`
	Region string `json:"region"`
	// Restore é a evidência assinada do restauro do log (AOS-101).
	Restore backup.RestoreEvidence `json:"restore"`
	// AuditVerified prova que a hash-chain do WORM foi verificada ANTES de retomar (AC5).
	AuditVerified bool `json:"audit_verified"`
	// Replay é o resumo da prova de fidelidade determinística (AC3).
	Replay ReplaySummary `json:"replay"`
	// Resume é a evidência da retoma resume-from-step sem duplicação (AC1/AC4).
	Resume ResumeEvidence `json:"resume"`
	// StartedAt/CompletedAt delimitam o encadeamento restaurar→verificar→replay→retomar.
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	// RTO é o wall-clock do encadeamento (CompletedAt − StartedAt) — a base da métrica
	// de RTO medida pelo game day.
	RTO time.Duration `json:"rto"`
}

// Recoverer é o ORQUESTRADOR de DR: encadeia, como TRANSACÇÃO FAIL-CLOSED, o restauro
// verificado do log, a verificação do WORM, a prova de fidelidade do replay e a retoma
// idempotente resume-from-step, dentro da fronteira de soberania. NÃO reimplementa
// nenhuma garantia — COMPÕE as peças Done (backup/audit/replay/worker/eventstore).
// Qualquer falha aborta SEM tocar em
// produção (opera sempre sobre um Event Store de DR limpo e descartável).
//
// Sem estado mutável ⇒ seguro para uso concorrente.
type Recoverer struct {
	resolver BoundaryResolver
	factory  StoreFactory
	restorer Restorer
	now      func() time.Time
}

// RecovererOption configura o [Recoverer].
type RecovererOption func(*Recoverer)

// WithClock injecta o relógio de wall-clock do RTO (uso interno/testes determinísticos).
// Default: time.Now.
func WithClock(now func() time.Time) RecovererOption {
	return func(r *Recoverer) { r.now = now }
}

// NewRecoverer constrói o orquestrador. resolver (board→região), factory (Store de DR
// na fronteira) e restorer (AOS-101) são obrigatórios — fail-closed na construção.
func NewRecoverer(resolver BoundaryResolver, factory StoreFactory, restorer Restorer, opts ...RecovererOption) (*Recoverer, error) {
	if resolver == nil {
		return nil, ErrNilResolver
	}
	if factory == nil {
		return nil, ErrNilFactory
	}
	if restorer == nil {
		return nil, ErrNilRestorer
	}
	r := &Recoverer{resolver: resolver, factory: factory, restorer: restorer, now: time.Now}
	for _, o := range opts {
		o(r)
	}
	if r.now == nil {
		r.now = time.Now
	}
	return r, nil
}

// Recover executa a recuperação de DR como transacção FAIL-CLOSED. A ordem é
// deliberada (cada etapa é uma pré-condição da seguinte) e ABORTA à primeira falha,
// NUNCA dando o serviço por restabelecido a partir de um estado não-íntegro:
//
//	(a) resolve a fronteira-alvo (board→região, injectada);
//	(b) constrói um Event Store de DR LIMPO na fronteira (WithSovereigntyBoard — recusa
//	    cross-border por construção) e ASSERTA região(Store)==região-alvo (AC6);
//	(c) Restorer.RestoreTo até ao seq-alvo — VerifyManifest aborta em adulteração
//	    (ErrSegmentTampered/ErrChainBroken/ErrCheckpointStale) ANTES de escrever;
//	(d) audit.VerifyFromCheckpointAtHead pós-restauro, ANTES de retomar (AC5);
//	(e) replay.Replay para PROVAR fidelidade — Fidelity==1.0 && sem divergência, senão
//	    aborta (AC3);
//	(f) retoma resume-from-step sobre o Store restaurado; StepLedger garante 0 efeitos
//	    duplicados (AC4) — aborta se a retoma reportar duplicação;
//	(g) reafirma a fronteira (região do log restaurado == região-alvo, AC6).
//
// Devolve a [Evidence] combinada (restauro + WORM verificado + fidelidade + timings).
func (r *Recoverer) Recover(ctx context.Context, rec Recovery) (Evidence, error) {
	if err := rec.validate(); err != nil {
		return Evidence{}, err
	}
	started := r.now().UTC()

	// (a) Resolve a fronteira-alvo (injectada; sem import de control-plane).
	region, err := r.resolver.RegionForBoard(rec.Board)
	if err != nil {
		return Evidence{}, fmt.Errorf("dr: resolver fronteira do board %q: %w", rec.Board, err)
	}
	region = normalizeRegion(region)
	if region == "" {
		return Evidence{}, ErrUnknownBoard
	}

	// (b) Constrói o Event Store de DR LIMPO na fronteira. WithSovereigntyBoard recusa
	// cross-border por construção; a asserção seguinte fecha o caso de uma fábrica que
	// devolvesse um Store noutra região.
	drStore, err := r.factory(rec.Board, region)
	if err != nil {
		return Evidence{}, fmt.Errorf("dr: construir Event Store de DR na fronteira %q: %w", region, err)
	}
	if drStore == nil {
		return Evidence{}, ErrNilFactory
	}
	if normalizeRegion(drStore.Region()) != region {
		return Evidence{}, fmt.Errorf("%w: store=%q alvo=%q", ErrRegionMismatch, drStore.Region(), region)
	}

	// (c) Restaura o log até ao seq-alvo. VerifyManifest (embutido) aborta em
	// adulteração ANTES de qualquer escrita.
	restoreEv, err := r.restorer.RestoreTo(ctx, rec.Manifest, rec.Backup, rec.ExpectedHead, rec.Target, drStore)
	if err != nil {
		return Evidence{}, fmt.Errorf("dr: restaurar log: %w", err)
	}
	// (g-cedo) Reafirma a fronteira: o log restaurado tem de residir na região-alvo.
	if normalizeRegion(restoreEv.Region) != region {
		return Evidence{}, fmt.Errorf("%w: log=%q alvo=%q", ErrRegionMismatch, restoreEv.Region, region)
	}

	// (d) Verifica a hash-chain do WORM ANTES de retomar (AC5). Rejeita stale/rollback/
	// adulteração.
	if err := audit.VerifyFromCheckpointAtHead(ctx, rec.Audit.Store, rec.Audit.Public, rec.Audit.Checkpoint, rec.Audit.ExpectedHead, rec.Audit.To); err != nil {
		return Evidence{}, fmt.Errorf("dr: verificar audit WORM: %w", err)
	}

	// (e) Prova a fidelidade do replay (só-leitura, zero efeitos). 100% ou aborta.
	replayer, err := replay.NewEngine(drStore)
	if err != nil {
		return Evidence{}, fmt.Errorf("dr: construir motor de replay: %w", err)
	}
	rr, err := replayer.Replay(ctx, rec.RunID, replay.Options{Spec: rec.Spec})
	if err != nil {
		return Evidence{}, fmt.Errorf("dr: replay do run %q: %w", rec.RunID, err)
	}
	if rr.Fidelity != 1.0 || rr.Divergence != nil {
		return Evidence{}, fmt.Errorf("%w: fidelity=%.4f diverged=%t", ErrReplayInfidelity, rr.Fidelity, rr.Divergence != nil)
	}

	// (f) Retoma resume-from-step sobre o Store restaurado. StepLedger garante 0
	// efeitos duplicados; aborta se a retoma reportar duplicação (defesa em profundidade).
	resumeEv, err := rec.Resume(ctx, drStore)
	if err != nil {
		return Evidence{}, fmt.Errorf("dr: retoma resume-from-step: %w", err)
	}
	if resumeEv.DuplicatedEffects != 0 {
		return Evidence{}, fmt.Errorf("%w: %d efeitos", ErrDuplicatedEffects, resumeEv.DuplicatedEffects)
	}

	completed := r.now().UTC()
	return Evidence{
		RunID:         rec.RunID,
		Board:         rec.Board,
		Region:        region,
		Restore:       restoreEv,
		AuditVerified: true,
		Replay: ReplaySummary{
			Fidelity:       rr.Fidelity,
			FinalStateHash: rr.FinalStateHash,
			Turns:          len(rr.Steps),
			Diverged:       rr.Divergence != nil,
		},
		Resume:      resumeEv,
		StartedAt:   started,
		CompletedAt: completed,
		RTO:         completed.Sub(started),
	}, nil
}

// Compile-time: o restaurador de referência satisfaz a porta usada pelo orquestrador.
var _ Restorer = (*backup.Restorer)(nil)
