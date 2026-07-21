package authoringsurface

import (
	"context"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// Vocabulário de span do loop de autoria (AC5). Os spans do loop ligam-se à
// trajectória (pelo ctx propagado e por AttrRunID quando conhecido) e carregam só
// eixos NÃO-SECRETOS: o tipo de interacção, se o egress foi bloqueado, que nada foi
// cometido (committed=false), o autor/versão da atribuição e o token de ratificação
// já público. NUNCA prompt, args, conteúdo da skill nem credenciais. O EgressFilter e
// o Launcher do EPIC-07 emitem os SEUS próprios spans (OpEgressDecision/OpExecuteTool)
// no adaptador — este vocabulário é o da CAMADA de apresentação.
const (
	// OpAuthoringSurface — a operação do span do loop de autoria (aos.authoring.surface).
	OpAuthoringSurface = "aos.authoring.surface"

	// AttrAuthoringKind — aos.authoring.surface.kind: o tipo de interacção
	// ("dry_run" | "attribution_view" | "submit").
	AttrAuthoringKind = "aos.authoring.surface.kind"

	// AttrCommitted — aos.authoring.surface.committed: sempre false no caminho de
	// sucesso do dry-run (a invariante "nada cometido").
	AttrCommitted = "aos.authoring.surface.committed"
	// AttrEgressBlocked — aos.authoring.surface.egress_blocked: o egress externo foi
	// negado por default-deny durante o dry-run.
	AttrEgressBlocked = "aos.authoring.surface.egress_blocked"
	// AttrEffectCount — aos.authoring.surface.effect_count: nº de efeitos capturados
	// (untrusted) no dry-run. Cardinalidade, não conteúdo.
	AttrEffectCount = "aos.authoring.surface.effect_count"
	// AttrDryRunError — aos.authoring.surface.dry_run_error: o dry-run falhou.
	AttrDryRunError = "aos.authoring.surface.dry_run_error"

	// AttrAuthor — aos.authoring.surface.author: a NHI/agente autora (não-secreto).
	AttrAuthor = "aos.authoring.surface.author"
	// AttrVersion — aos.authoring.surface.version: a versão SemVer da candidata.
	AttrVersion = "aos.authoring.surface.version"
	// AttrTrust — aos.authoring.surface.trust: o estado de confiança TOFU da
	// proveniência ("first_seen"|"pinned"|"changed").
	AttrTrust = "aos.authoring.surface.trust"

	// AttrRatificationID — aos.authoring.surface.ratification_id: o token que o gate de
	// AOS-096 devolveu (já público; é o que o humano assina — não um segredo).
	AttrRatificationID = "aos.authoring.surface.ratification_id"
	// AttrSubmitError — aos.authoring.surface.submit_error: a submissão falhou.
	AttrSubmitError = "aos.authoring.surface.submit_error"
)

// Tipos de interacção do loop (o eixo aos.authoring.surface.kind).
const (
	// SurfaceKindDryRun — a execução simulada da candidata (nada cometido).
	SurfaceKindDryRun = "dry_run"
	// SurfaceKindAttribution — a apresentação da atribuição (autor/versão/proveniência).
	SurfaceKindAttribution = "attribution_view"
	// SurfaceKindSubmit — o encaminhamento ao gate de ratificação de AOS-096.
	SurfaceKindSubmit = "submit"
)

// span é o pequeno adaptador de escrita de atributos usado pelos emissores do loop —
// evita repetir a verificação nil e mantém os call-sites legíveis (s.set(k, v)).
type span struct{ s agentruntime.Span }

func (w span) set(key string, value any) { w.s.SetAttribute(key, value) }

// emit abre e fecha um span de INTERACÇÃO do loop ligado ao trace: nomeia a operação
// [OpAuthoringSurface], anota o tipo de interacção (kind), a skill/versão da candidata
// e o run_id (quando conhecido), e deixa o emissor anotar os seus eixos não-secretos
// via decorate. Sem segredos: só identificadores, o tipo, a versão e os flags já
// públicos da decisão.
func (l *AuthoringLoop) emit(ctx context.Context, kind string, candidate CandidateRef, decorate func(span)) {
	_, s := l.tracer.StartSpan(ctx, OpAuthoringSurface)
	defer s.End()
	s.SetAttribute(AttrAuthoringKind, kind)
	s.SetAttribute(AttrVersion, candidate.Version)
	if l.runID != "" {
		s.SetAttribute(agentruntime.AttrRunID, l.runID)
	}
	if decorate != nil {
		decorate(span{s: s})
	}
}
