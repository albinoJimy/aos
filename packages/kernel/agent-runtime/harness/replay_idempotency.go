package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/replay"
)

// Erros operacionais do harness (falha de setup, não de verificação). Um problema
// de VERIFICAÇÃO (divergência, efeito duplicado, retoma divergente) NÃO é um
// destes erros — é reportado no [FidelityReport] (Pass=false) e transformado em
// erro fail-closed por [FidelityReport.Err].
var (
	// ErrNoRunID — a [Case] não tem run_id (é o stream_id da trajectória).
	ErrNoRunID = errors.New("harness: run_id vazio")
	// ErrNoReader — a [Case] não tem [replay.EventReader] (trajectória gravada).
	ErrNoReader = errors.New("harness: EventReader (trajectória gravada) em falta")
	// ErrNoLedgerStore — a [Case] declara efeitos mas não fornece o Event Store
	// sobre o qual reconstruir o step-ledger para a verificação de idempotência.
	ErrNoLedgerStore = errors.New("harness: LedgerStore em falta para verificar idempotência")
)

// Effect modela um PASSO COM EFEITO EXTERNO a submeter à verificação de
// idempotência. O harness reexecuta o efeito sob um calendário at-least-once com
// um crash intercalado (o ledger é reconstruído do log entre tentativas) e conta
// quantas vezes o efeito OBSERVÁVEL correu de facto. A garantia de ADR-001 é ZERO
// efeitos observáveis duplicados: [Observed] tem de devolver 1 no fim.
//
// # Modelação da idempotency key
//
// [KeyAt] devolve a idempotency key da tentativa n (0-based). Um efeito CORRECTO
// devolve SEMPRE a MESMA chave canónica f(run_id, step_id) — logo o ledger
// deduplica as re-tentativas e o efeito corre uma só vez. Um efeito com um BUG de
// não-determinismo na chave (p.ex. carimbo de tempo / UUID por tentativa) devolve
// chaves diferentes; o ledger não consegue deduplicar e o harness DETECTA os
// efeitos duplicados — é exactamente esta a falha que o meta-teste injecta.
type Effect struct {
	// StepID é o step_id lógico do passo (documental; a chave vem de KeyAt).
	StepID string
	// KeyAt deriva a idempotency key da tentativa n (0-based). Estável ⇒ idempotente.
	KeyAt func(attempt int) (string, error)
	// Run é o corpo do efeito. DEVE registar o seu efeito observável (incrementar o
	// contador que [Observed] devolve) SEMPRE que corra de facto.
	Run func(ctx context.Context) (durable.Result, error)
	// Observed devolve o nº de vezes que o efeito CORREU de facto (side-effect).
	Observed func() int
}

// FaultPoint é um PONTO DE CRASH configurável: o step_id do turno a partir do qual
// a execução retoma (resume-from-step). O harness confirma que a retoma reconstrói
// o MESMO estado que o replay completo.
type FaultPoint struct {
	// AtStepID é o step_id do turno onde o crash ocorre / a retoma começa.
	AtStepID string
}

// Case é um RUN GRAVADO a verificar: a trajectória (só-leitura, para o replay), os
// inputs determinísticos re-fornecidos ao replay, os passos com efeito a exercitar
// quanto a idempotência e os pontos de crash a exercitar quanto a retoma.
type Case struct {
	// Name identifica o caso no relatório (opcional).
	Name string
	// RunID é o stream_id da trajectória gravada. Obrigatório.
	RunID string
	// Reader é o Event Store gravado (só Read — o replay não escreve). Obrigatório.
	Reader replay.EventReader
	// Spec são os inputs DETERMINÍSTICOS re-fornecidos ao replay (system, tool set
	// congelado, objectivo, memory_context, modelo/seed, versão do assembler).
	Spec replay.TrajectorySpec
	// LedgerStore é o Event Store sobre o qual reconstruir o step-ledger para a
	// verificação de idempotência. Obrigatório sse Effects != nil.
	LedgerStore durable.EventStore
	// Effects são os passos com efeito externo a exercitar (idempotência).
	Effects []Effect
	// Faults são os pontos de crash a exercitar (resume-from-step).
	Faults []FaultPoint
}

// FidelityReport é o RELATÓRIO de fidelidade de UM run. A serialização é canónica e
// estável (struct de ordem fixa, sem mapas) — consumível pelas métricas do backlog
// (replay-fidelity, efeitos duplicados). Ver [FidelityReport.JSON].
type FidelityReport struct {
	Name              string  `json:"name,omitempty"`
	RunID             string  `json:"run_id"`
	Turns             int     `json:"turns"`
	ReplayFidelity    float64 `json:"replay_fidelity"`
	Diverged          bool    `json:"diverged"`
	DivergenceStepID  string  `json:"divergence_step_id,omitempty"`
	DivergenceReason  string  `json:"divergence_reason,omitempty"`
	EffectsVerified   int     `json:"effects_verified"`
	DuplicatedEffects int     `json:"duplicated_effects"`
	ResumePoints      int     `json:"resume_points_verified"`
	ResumeMismatches  int     `json:"resume_mismatches"`
	Pass              bool    `json:"pass"`
}

// verifyConfig e VerifyOption parametrizam [Verify] / [VerifyAll].
type verifyConfig struct {
	tracer agentruntime.Tracer
}

// VerifyOption configura uma verificação.
type VerifyOption func(*verifyConfig)

// WithTracer injecta a porta de observabilidade no motor de replay (o marcador
// eval de ADR-010 é emitido ligado ao trace original). Default: NoopTracer.
func WithTracer(t agentruntime.Tracer) VerifyOption {
	return func(c *verifyConfig) { c.tracer = t }
}

// Verify corre as TRÊS verificações sobre a [Case] — replay determinístico,
// idempotência por passo e fault-injection (resume-from-step) — e devolve o
// [FidelityReport]. O error devolvido é reservado a falhas OPERACIONAIS (setup
// inválido, log ilegível); uma falha de PROPRIEDADE reflecte-se em Pass=false (use
// [FidelityReport.Err] para a converter num erro fail-closed).
func Verify(ctx context.Context, c Case, opts ...VerifyOption) (FidelityReport, error) {
	if c.RunID == "" {
		return FidelityReport{}, ErrNoRunID
	}
	if c.Reader == nil {
		return FidelityReport{}, ErrNoReader
	}
	cfg := verifyConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	rep := FidelityReport{Name: c.Name, RunID: c.RunID}

	var engOpts []replay.EngineOption
	if cfg.tracer != nil {
		engOpts = append(engOpts, replay.WithTracer(cfg.tracer))
	}
	eng, err := replay.NewEngine(c.Reader, engOpts...)
	if err != nil {
		return FidelityReport{}, err
	}

	// (a) REPLAY DETERMINÍSTICO completo.
	full, err := eng.Replay(ctx, c.RunID, replay.Options{Spec: c.Spec})
	if err != nil {
		return FidelityReport{}, fmt.Errorf("harness: replay completo: %w", err)
	}
	rep.Turns = len(full.Steps)
	rep.ReplayFidelity = full.Fidelity
	if full.Divergence != nil {
		rep.Diverged = true
		rep.DivergenceStepID = full.Divergence.StepID
		rep.DivergenceReason = full.Divergence.Reason
	}

	// (b) IDEMPOTÊNCIA POR PASSO.
	dup, verified, err := c.verifyIdempotency(ctx)
	if err != nil {
		return FidelityReport{}, err
	}
	rep.EffectsVerified = verified
	rep.DuplicatedEffects = dup

	// (c) FAULT-INJECTION (resume-from-step).
	mismatches, points, err := c.verifyFaults(ctx, eng, full)
	if err != nil {
		return FidelityReport{}, err
	}
	rep.ResumePoints = points
	rep.ResumeMismatches = mismatches

	rep.Pass = !rep.Diverged &&
		rep.ReplayFidelity == 1.0 &&
		rep.DuplicatedEffects == 0 &&
		rep.ResumeMismatches == 0
	return rep, nil
}

// verifyIdempotency exercita cada efeito e conta os efeitos OBSERVÁVEIS
// duplicados (a soma de max(0, Observed-1) por efeito).
func (c Case) verifyIdempotency(ctx context.Context) (duplicated, verified int, err error) {
	if len(c.Effects) == 0 {
		return 0, 0, nil
	}
	if c.LedgerStore == nil {
		return 0, 0, ErrNoLedgerStore
	}
	for _, eff := range c.Effects {
		if err := driveEffectSchedule(ctx, c.RunID, c.LedgerStore, eff); err != nil {
			return 0, 0, err
		}
		verified++
		if n := eff.Observed(); n > 1 {
			duplicated += n - 1
		}
	}
	return duplicated, verified, nil
}

// driveEffectSchedule submete o efeito a um calendário AT-LEAST-ONCE com um CRASH
// intercalado, o pior caso realista de ADR-001:
//
//	tentativa 0 — ledger fresco (worker A) corre o efeito e regista-o (durável);
//	CRASH        — worker B novo, estado in-memory vazio, RECONSTRÓI do log;
//	tentativa 1 — worker B reexecuta: deduplica pela camada DURÁVEL (rebuild);
//	tentativa 2 — retry no mesmo worker B: deduplica pela camada IN-MEMORY.
//
// Um efeito idempotente (chave estável) corre UMA vez (Observed==1). Um efeito com
// chave não-determinística falha a deduplicar e Observed cresce — o harness apanha.
func driveEffectSchedule(ctx context.Context, runID string, store durable.EventStore, eff Effect) error {
	workerA, err := durable.NewStepLedger(store)
	if err != nil {
		return err
	}
	if err := workerA.Rebuild(ctx, runID); err != nil {
		return err
	}
	if err := applyOnce(ctx, workerA, eff, 0); err != nil {
		return err
	}

	// CRASH: um worker NOVO com estado in-memory vazio reconstrói o ledger do log.
	workerB, err := durable.NewStepLedger(store)
	if err != nil {
		return err
	}
	if err := workerB.Rebuild(ctx, runID); err != nil {
		return err
	}
	if err := applyOnce(ctx, workerB, eff, 1); err != nil {
		return err
	}
	// Retry adicional no MESMO worker (dedup in-memory / single-flight).
	return applyOnce(ctx, workerB, eff, 2)
}

// applyOnce corre uma tentativa do efeito através do ledger com a chave da tentativa.
func applyOnce(ctx context.Context, l *durable.StepLedger, eff Effect, attempt int) error {
	key, err := eff.KeyAt(attempt)
	if err != nil {
		return err
	}
	_, _, err = l.Apply(ctx, key, eff.Run)
	return err
}

// verifyFaults exercita cada ponto de crash: retoma o replay a partir do step_id e
// confirma que o estado reconstruído (por re-fold integral) coincide EXACTAMENTE
// com o do replay completo — mesmo estado no ponto de retoma, mesmo estado final e
// mesmo desfecho. Qualquer divergência incrementa ResumeMismatches.
func (c Case) verifyFaults(ctx context.Context, eng *replay.ReplayEngine, full replay.ReplayResult) (mismatches, points int, err error) {
	if len(c.Faults) == 0 {
		return 0, 0, nil
	}
	incomingByStep := make(map[string]string, len(full.Steps))
	for _, s := range full.Steps {
		incomingByStep[s.StepID] = s.IncomingStateHash
	}
	for _, f := range c.Faults {
		points++
		resumed, rerr := eng.Replay(ctx, c.RunID, replay.Options{Spec: c.Spec, FromStepID: f.AtStepID})
		if rerr != nil {
			// Um step_id de retoma inexistente é uma falha de RETOMA (fail-closed),
			// não um erro operacional — conta como mismatch.
			if errors.Is(rerr, replay.ErrStepNotFound) {
				mismatches++
				continue
			}
			return 0, 0, fmt.Errorf("harness: resume em %s: %w", f.AtStepID, rerr)
		}
		switch {
		case resumed.Divergence != nil || resumed.Fidelity != 1.0:
			mismatches++
		case resumed.FinalStateHash != full.FinalStateHash:
			mismatches++
		case resumed.Terminated != full.Terminated || resumed.FinalText != full.FinalText:
			mismatches++
		case len(resumed.Steps) == 0:
			mismatches++
		default:
			// Estado NO PONTO de retoma tem de bater com o estado que o replay completo
			// tinha nesse mesmo ponto (prova de equivalência do resume-from-step). Se o
			// fault aponta para um step_id que o replay completo NÃO verificou (ausente
			// de full.Steps), NÃO há prova de equivalência a fazer — fail-closed (conta
			// como mismatch) em vez de saltar a asserção mais forte silenciosamente.
			want, ok := incomingByStep[f.AtStepID]
			if !ok || resumed.Steps[0].IncomingStateHash != want {
				mismatches++
			}
		}
	}
	return mismatches, points, nil
}

// Err converte um relatório FALHADO num erro fail-closed descritivo (nil se Pass).
// É o que os testes e o gate de CI (scripts/ci/replay.sh) usam para transformar
// uma quebra de fidelidade/idempotência/retoma num exit != 0.
func (r FidelityReport) Err() error {
	if r.Pass {
		return nil
	}
	switch {
	case r.Diverged:
		return fmt.Errorf("harness: replay divergiu no passo %s (razão %q), fidelidade %.4f", r.DivergenceStepID, r.DivergenceReason, r.ReplayFidelity)
	case r.ReplayFidelity != 1.0:
		return fmt.Errorf("harness: replay-fidelity %.4f < 1.0", r.ReplayFidelity)
	case r.DuplicatedEffects > 0:
		return fmt.Errorf("harness: %d efeito(s) observável(eis) duplicado(s)", r.DuplicatedEffects)
	case r.ResumeMismatches > 0:
		return fmt.Errorf("harness: %d ponto(s) de retoma divergente(s)", r.ResumeMismatches)
	default:
		return errors.New("harness: verificação falhou")
	}
}

// JSON devolve o relatório serializado de forma estável e indentada.
func (r FidelityReport) JSON() []byte {
	b, _ := json.MarshalIndent(r, "", "  ")
	return b
}

// CompactJSON devolve o relatório numa única linha (para emissão no gate de CI).
func (r FidelityReport) CompactJSON() []byte {
	b, _ := json.Marshal(r)
	return b
}

// AggregateReport agrega os relatórios de um GOLDEN SET (várias trajectórias).
// Serialização estável (struct de ordem fixa, sem mapas).
type AggregateReport struct {
	Cases                  []FidelityReport `json:"cases"`
	Runs                   int              `json:"runs"`
	MeanReplayFidelity     float64          `json:"mean_replay_fidelity"`
	TotalEffectsVerified   int              `json:"total_effects_verified"`
	TotalDuplicatedEffects int              `json:"total_duplicated_effects"`
	TotalResumePoints      int              `json:"total_resume_points"`
	TotalResumeMismatches  int              `json:"total_resume_mismatches"`
	Pass                   bool             `json:"pass"`
}

// VerifyAll verifica um conjunto de casos e agrega os relatórios. O erro devolvido
// é operacional; a falha de propriedade reflecte-se em Pass=false (ver
// [AggregateReport.Err]).
func VerifyAll(ctx context.Context, cases []Case, opts ...VerifyOption) (AggregateReport, error) {
	agg := AggregateReport{Pass: true}
	var sum float64
	for _, c := range cases {
		rep, err := Verify(ctx, c, opts...)
		if err != nil {
			return AggregateReport{}, err
		}
		agg.Cases = append(agg.Cases, rep)
		agg.Runs++
		sum += rep.ReplayFidelity
		agg.TotalEffectsVerified += rep.EffectsVerified
		agg.TotalDuplicatedEffects += rep.DuplicatedEffects
		agg.TotalResumePoints += rep.ResumePoints
		agg.TotalResumeMismatches += rep.ResumeMismatches
		if !rep.Pass {
			agg.Pass = false
		}
	}
	if agg.Runs > 0 {
		agg.MeanReplayFidelity = sum / float64(agg.Runs)
	}
	return agg, nil
}

// Err converte um agregado FALHADO no primeiro erro de caso descritivo (nil se Pass).
func (a AggregateReport) Err() error {
	if a.Pass {
		return nil
	}
	for _, c := range a.Cases {
		if err := c.Err(); err != nil {
			return fmt.Errorf("harness: caso %q: %w", c.Name, err)
		}
	}
	return errors.New("harness: agregado falhou")
}

// JSON devolve o agregado serializado de forma estável e indentada.
func (a AggregateReport) JSON() []byte {
	b, _ := json.MarshalIndent(a, "", "  ")
	return b
}

// CompactJSON devolve o agregado numa única linha (para emissão no gate de CI).
func (a AggregateReport) CompactJSON() []byte {
	b, _ := json.Marshal(a)
	return b
}
