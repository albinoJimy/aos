package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/aos-ref/platform/model-gateway/pipeline"
)

// TestStages_Names confirma os nomes estáveis dos cinco estágios de referência
// (o rasto de decisões e os StageError dependem deles).
func TestStages_Names(t *testing.T) {
	t.Parallel()
	s := pipeline.DefaultStages()
	want := map[pipeline.Stage]string{
		s.Auth:        "auth-principal",
		s.Allowlist:   "allowlist-regional",
		s.Routing:     "roteamento",
		s.CacheLayout: "cache-layout",
		s.Metering:    "metering",
	}
	for st, name := range want {
		if st.Name() != name {
			t.Errorf("Name() = %q, quer %q", st.Name(), name)
		}
	}
}

// TestExchange_Now cobre o relógio injectável (nunca usado em decisão, mas
// disponível para timestamps deterministas).
func TestExchange_Now(t *testing.T) {
	t.Parallel()
	ex := &pipeline.Exchange{}
	if ex.Now().IsZero() {
		t.Error("Now() sem relogio devia devolver time.Now (nao-zero)")
	}
	fixed := time.Unix(42, 0)
	pipeline.WithClock(ex, func() time.Time { return fixed })
	if !ex.Now().Equal(fixed) {
		t.Errorf("Now() com relogio injectado = %v, quer %v", ex.Now(), fixed)
	}
}

// TestStageFunc_And_DenyStage_Defaults cobre os defaults dos helpers de composição.
func TestStageFunc_And_DenyStage_Defaults(t *testing.T) {
	t.Parallel()
	// StageFunc com Fn nil é no-op.
	sf := pipeline.StageFunc{StageName: "x"}
	if err := sf.Process(context.Background(), &pipeline.Exchange{}); err != nil {
		t.Errorf("StageFunc nil Fn devia ser no-op, erro=%v", err)
	}
	// DenyStage sem nome usa "deny" e sem Err usa ErrDenied.
	d := pipeline.DenyStage{}
	if d.Name() != "deny" {
		t.Errorf("DenyStage.Name default = %q", d.Name())
	}
	if err := d.Process(context.Background(), &pipeline.Exchange{}); err != pipeline.ErrDenied {
		t.Errorf("DenyStage default err = %v, quer ErrDenied", err)
	}
}

// TestStageError_Message cobre a formatação do erro atribuível.
func TestStageError_Message(t *testing.T) {
	t.Parallel()
	e := &pipeline.StageError{Stage: "allowlist-regional", Err: pipeline.ErrDenied}
	if got := e.Error(); got == "" || got == pipeline.ErrDenied.Error() {
		t.Errorf("StageError.Error() nao identifica o estagio: %q", got)
	}
}
