package authoringsurface

import "context"

// SubmitForRatification ENCAMINHA a candidata ao gate de ratificação de AOS-096 (AC3):
// DELEGA ao [RatificationSubmitter.Submit] — que constrói o [hitl.SelfModArtifact] no
// adaptador e devolve o RatificationID (o token anti-transplante que o HUMANO assina)
// — e devolve esse token ao autor/ratificador. A superfície APRESENTA+SUBMETE; NÃO
// chama Ratify nem promove (não há caminho de Ratify aqui — o encaminhamento é a
// única acção sobre o gate). Fail-closed: sem [RatificationSubmitter] devolve
// [ErrNoSubmitter]; uma candidata inválida devolve [ErrInvalidCandidate]. Emite o span
// aos.authoring.surface.kind=submit (sem segredos: só kind + o ratID já público).
func (l *AuthoringLoop) SubmitForRatification(ctx context.Context, candidate CandidateRef) (string, error) {
	if !candidate.Valid() {
		return "", ErrInvalidCandidate
	}
	if l.submitter == nil {
		return "", ErrNoSubmitter
	}

	ratID, err := l.submitter.Submit(ctx, candidate)
	if err != nil {
		l.emit(ctx, SurfaceKindSubmit, candidate, func(s span) {
			s.set(AttrSubmitError, true)
		})
		return "", err
	}

	l.emit(ctx, SurfaceKindSubmit, candidate, func(s span) {
		s.set(AttrRatificationID, ratID)
	})
	return ratID, nil
}
