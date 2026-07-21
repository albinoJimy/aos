package authoringsurface

import "context"

// ProvenanceView é a projecção não-secreta da [domain.Provenance] do registry
// (EPIC-05): de onde veio a skill, quem a publicou e o seu estado de confiança TOFU.
// Sem segredos — só a origem auditável.
type ProvenanceView struct {
	// Origin é a origem do artefacto (ex.: "self", "git+https://…").
	Origin string
	// Publisher é o identificador do publicador (não a chave/segredo).
	Publisher string
	// Trust é o estado de confiança TOFU ("first_seen"|"pinned"|"changed").
	Trust string
}

// Attribution é a ATRIBUIÇÃO RICA da skill candidata (AC2): quem a autorou, em que
// versão SemVer, a partir de que run, com que pin de integridade, e a sua
// proveniência. É apresentada em TODO o loop (dry-run, submissão) para que a autoria
// seja sempre visível. Sem segredos: o [ContentHashHex] é um DIGEST (pin), nunca o
// conteúdo.
type Attribution struct {
	// Author é a NHI/agente (ou humano) que autorou a skill — accountability (EPIC-05).
	Author string
	// Version é a versão SemVer (X.Y.Z) do artefacto, do registry.
	Version string
	// OriginRunID é o run de origem que produziu a skill (correlação forense).
	OriginRunID string
	// ContentHashHex é o hash de conteúdo em hex (pin de integridade). Digest, não
	// conteúdo — nunca um segredo.
	ContentHashHex string
	// Provenance é a origem auditável do registry (origin/publisher/trust).
	Provenance ProvenanceView
}

// Attribution LÊ a atribuição da candidata via [AttributionReader] e apresenta-a
// (AC2). Fail-closed: sem a porta configurada devolve [ErrNoAttributionReader]; uma
// candidata inválida devolve [ErrInvalidCandidate]. Emite o span
// aos.authoring.surface.kind=attribution_view (só eixos não-secretos: autor, versão).
func (l *AuthoringLoop) Attribution(ctx context.Context, candidate CandidateRef) (Attribution, error) {
	if !candidate.Valid() {
		return Attribution{}, ErrInvalidCandidate
	}
	if l.attribution == nil {
		return Attribution{}, ErrNoAttributionReader
	}

	att, err := l.attribution.Attribution(ctx, candidate.Skill, candidate.Version)
	if err != nil {
		return Attribution{}, err
	}

	l.emit(ctx, SurfaceKindAttribution, candidate, func(s span) {
		s.set(AttrAuthor, att.Author)
		s.set(AttrVersion, att.Version)
		s.set(AttrTrust, att.Provenance.Trust)
	})
	return att, nil
}
