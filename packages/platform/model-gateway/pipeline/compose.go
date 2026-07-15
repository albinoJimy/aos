package pipeline

import "context"

// StageFunc adapta uma função à interface [Stage] com um nome dado. Útil para
// os tickets de extensão (AOS-058..062) e para testes construírem estágios sem
// declarar um tipo. O nome vai para o rasto de decisões e para o [StageError].
type StageFunc struct {
	StageName string
	Fn        func(ctx context.Context, ex *Exchange) error
}

// Name implementa [Stage].
func (s StageFunc) Name() string { return s.StageName }

// Process implementa [Stage].
func (s StageFunc) Process(ctx context.Context, ex *Exchange) error {
	if s.Fn == nil {
		return nil
	}
	return s.Fn(ctx, ex)
}

// DenyStage é um estágio que recusa SEMPRE com a razão dada — a mecânica
// fail-closed que os estágios reais (ex.: allowlist de AOS-058) usam. Demonstra
// que uma recusa aborta a chamada antes de o provider ser invocado.
type DenyStage struct {
	StageName string
	Err       error
}

// Name implementa [Stage].
func (d DenyStage) Name() string {
	if d.StageName == "" {
		return "deny"
	}
	return d.StageName
}

// Process implementa [Stage] recusando fail-closed.
func (d DenyStage) Process(_ context.Context, ex *Exchange) error {
	ex.record(d.Name(), "deny", "recusa fail-closed")
	if d.Err != nil {
		return d.Err
	}
	return ErrDenied
}
