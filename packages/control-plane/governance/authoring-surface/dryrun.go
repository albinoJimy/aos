package authoringsurface

import "context"

// EffectTaintUntrusted é o único taint que um [CapturedEffect] carrega: um efeito
// observado num dry-run é SEMPRE não-confiável (ecoa [sandbox.TaintUntrusted]). Nunca
// há um efeito de dry-run "confiável" — o resultado da execução isolada é, por
// construção, untrusted.
const EffectTaintUntrusted = "untrusted"

// CapturedEffect é um efeito OBSERVADO durante o dry-run — capturado, nunca cometido.
// Descreve o que a skill TENTARIA fazer (uma escrita, uma chamada de rede bloqueada,
// um artefacto produzido no overlay descartado) para o autor/ratificador o poder
// inspeccionar. É rótulo de apresentação: não transporta segredos nem o payload real
// do efeito, só um descritor e o taint untrusted.
type CapturedEffect struct {
	// Kind classifica o efeito capturado (ex.: "egress", "fs_write", "artifact").
	Kind string
	// Descriptor é a descrição não-secreta do alvo do efeito (ex.: o destino de rede
	// negado, o caminho no overlay). Nunca conteúdo/credencial.
	Descriptor string
	// Taint marca a proveniência do efeito: sempre [EffectTaintUntrusted].
	Taint string
}

// DryRunResult é o desfecho de uma execução SIMULADA da skill candidata (AC1). É a
// prova, verificável, de que NADA foi cometido no mundo: os [Effects] foram
// CAPTURADOS (untrusted), o egress externo foi bloqueado (default-deny) e Committed é
// false. A superfície VALIDA estruturalmente Committed==false — um resultado com
// Committed=true é rejeitado ([ErrEffectCommitted]).
type DryRunResult struct {
	// Committed reporta se algum efeito foi cometido no mundo externo. TEM de ser
	// false: o dry-run é "nada cometido" por invariante. Um true é a violação capital
	// que a superfície rejeita.
	Committed bool
	// EgressBlocked reporta que o egress externo foi negado por default-deny (allowlist
	// vazia para o principal da candidata) — a ausência de saída para o mundo.
	EgressBlocked bool
	// Effects são os efeitos CAPTURADOS durante a execução isolada (untrusted). São
	// para inspecção; nunca foram aplicados.
	Effects []CapturedEffect
	// Output é o stdout/resumo untrusted da execução isolada (sem segredos).
	Output string
}

// DryRun executa a skill candidata em modo SIMULADO delegando ao [DryRunner] e
// VALIDA, fail-closed, que nada foi cometido (AC1/AC5). O adaptador do wiring monta a
// composição de isolamento do EPIC-07 (overlay descartado + egress default-deny + sem
// credenciais); esta camada garante a invariante de apresentação: se o resultado
// reportar Committed=true, é REJEITADO com [ErrEffectCommitted] — a superfície nunca
// apresenta um dry-run que cometeu efeitos. Emite o span aos.authoring.surface.kind=
// dry_run (sem segredos: só kind/egress_blocked/committed=false).
func (l *AuthoringLoop) DryRun(ctx context.Context, candidate CandidateRef) (DryRunResult, error) {
	if !candidate.Valid() {
		return DryRunResult{}, ErrInvalidCandidate
	}
	if l.dryRunner == nil {
		return DryRunResult{}, ErrNilDryRunner
	}

	res, err := l.dryRunner.DryRun(ctx, candidate)
	if err != nil {
		l.emit(ctx, SurfaceKindDryRun, candidate, func(s span) {
			s.set(AttrDryRunError, true)
		})
		return DryRunResult{}, err
	}

	// FAIL-CLOSED: a invariante capital do dry-run. Um efeito cometido invalida tudo.
	if res.Committed {
		l.emit(ctx, SurfaceKindDryRun, candidate, func(s span) {
			s.set(AttrCommitted, true)
		})
		return DryRunResult{}, ErrEffectCommitted
	}

	// Normaliza o taint dos efeitos capturados: nunca há um efeito de dry-run confiável.
	for i := range res.Effects {
		res.Effects[i].Taint = EffectTaintUntrusted
	}

	l.emit(ctx, SurfaceKindDryRun, candidate, func(s span) {
		s.set(AttrCommitted, false)
		s.set(AttrEgressBlocked, res.EgressBlocked)
		s.set(AttrEffectCount, len(res.Effects))
	})
	return res, nil
}
