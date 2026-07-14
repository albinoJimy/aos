package tofu

import (
	"testing"

	"github.com/aos-ref/platform/registry/domain"
)

func v(t *testing.T, s string) domain.Version { return mustVersion(t, s) }

// TestOnObserve cobre a decisão PURA de observação em cada estado — a máquina de
// detecção de drift, sem audit nem relógio.
func TestOnObserve(t *testing.T) {
	ref1 := reference{Version: v(t, "1.0.0"), Digest: "sha256:aaa"}
	ref1b := reference{Version: v(t, "1.0.0"), Digest: "sha256:bbb"} // mesma versão, digest diferente
	ref2 := reference{Version: v(t, "2.0.0"), Digest: "sha256:ccc"}

	tests := []struct {
		name      string
		prev      *record
		obs       reference
		wantState TrustState
		wantChng  bool
		wantCap   string
		wantErr   error
	}{
		{
			name:      "primeira ligacao -> first_seen",
			prev:      nil,
			obs:       ref1,
			wantState: StateFirstSeen,
			wantChng:  true,
			wantCap:   capFirstSeen,
		},
		{
			name:      "first_seen re-observado identico -> sem transicao",
			prev:      &record{State: StateFirstSeen, Ref: ref1},
			obs:       ref1,
			wantState: StateFirstSeen,
			wantChng:  false,
		},
		{
			name:      "first_seen re-observado diferente -> re-regista first_seen",
			prev:      &record{State: StateFirstSeen, Ref: ref1},
			obs:       ref1b,
			wantState: StateFirstSeen,
			wantChng:  true,
			wantCap:   capFirstSeen,
		},
		{
			name:      "pinned identico -> passa (sem transicao)",
			prev:      &record{State: StatePinned, Ref: ref1},
			obs:       ref1,
			wantState: StatePinned,
			wantChng:  false,
		},
		{
			name:      "pinned digest divergente (mesma versao) -> changed (DRIFT)",
			prev:      &record{State: StatePinned, Ref: ref1},
			obs:       ref1b,
			wantState: StateChanged,
			wantChng:  true,
			wantCap:   capChanged,
			wantErr:   ErrSchemaDrift,
		},
		{
			name:      "pinned versao divergente sem re-aprovacao -> changed (DRIFT)",
			prev:      &record{State: StatePinned, Ref: ref1},
			obs:       ref2,
			wantState: StateChanged,
			wantChng:  true,
			wantCap:   capChanged,
			wantErr:   ErrSchemaDrift,
		},
		{
			name:      "changed re-observado -> mantem-se bloqueado (idempotente)",
			prev:      &record{State: StateChanged, Ref: ref1},
			obs:       ref1b,
			wantState: StateChanged,
			wantChng:  false,
			wantErr:   ErrSchemaDrift,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := onObserve(tc.prev, tc.obs)
			if got.Next.State != tc.wantState {
				t.Errorf("state = %q, quer %q", got.Next.State, tc.wantState)
			}
			if got.Changed != tc.wantChng {
				t.Errorf("changed = %v, quer %v", got.Changed, tc.wantChng)
			}
			if tc.wantCap != "" && got.Cap != tc.wantCap {
				t.Errorf("cap = %q, quer %q", got.Cap, tc.wantCap)
			}
			if got.Err != tc.wantErr {
				t.Errorf("err = %v, quer %v", got.Err, tc.wantErr)
			}
		})
	}
}

// TestOnRatify cobre a ratificação pura (first_seen -> pinned).
func TestOnRatify(t *testing.T) {
	ref1 := reference{Version: v(t, "1.0.0"), Digest: "sha256:aaa"}
	other := reference{Version: v(t, "1.0.0"), Digest: "sha256:zzz"}

	tests := []struct {
		name      string
		prev      *record
		want      reference
		wantState TrustState
		wantChng  bool
		wantErr   error
	}{
		{
			name:      "first_seen ratificado com par exacto -> pinned",
			prev:      &record{State: StateFirstSeen, Ref: ref1},
			want:      ref1,
			wantState: StatePinned,
			wantChng:  true,
		},
		{
			name:    "ratificar identidade desconhecida -> recusa",
			prev:    nil,
			want:    ref1,
			wantErr: ErrNotFirstSeen,
		},
		{
			name:    "ratificar em pinned -> recusa (nao e first_seen)",
			prev:    &record{State: StatePinned, Ref: ref1},
			want:    ref1,
			wantErr: ErrNotFirstSeen,
		},
		{
			name:    "ratificar par divergente do observado -> recusa (TOCTOU)",
			prev:    &record{State: StateFirstSeen, Ref: ref1},
			want:    other,
			wantErr: ErrRatifyMismatch,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := onRatify(tc.prev, tc.want)
			if got.Err != tc.wantErr {
				t.Fatalf("err = %v, quer %v", got.Err, tc.wantErr)
			}
			if tc.wantErr == nil {
				if got.Next.State != tc.wantState {
					t.Errorf("state = %q, quer %q", got.Next.State, tc.wantState)
				}
				if got.Changed != tc.wantChng {
					t.Errorf("changed = %v, quer %v", got.Changed, tc.wantChng)
				}
				if got.Cap != capPinned {
					t.Errorf("cap = %q, quer %q", got.Cap, capPinned)
				}
			}
		})
	}
}

// TestOnReapprove cobre a re-aprovação pura (changed -> pinned) e a regra do SemVer.
func TestOnReapprove(t *testing.T) {
	pinnedRef := reference{Version: v(t, "1.0.0"), Digest: "sha256:aaa"}

	tests := []struct {
		name      string
		prev      *record
		next      reference
		wantState TrustState
		wantErr   error
	}{
		{
			name:      "changed + versao superior -> pinned",
			prev:      &record{State: StateChanged, Ref: pinnedRef},
			next:      reference{Version: v(t, "2.0.0"), Digest: "sha256:ccc"},
			wantState: StatePinned,
		},
		{
			name:    "changed + MESMA versao (digest diferente) -> recusa in-band",
			prev:    &record{State: StateChanged, Ref: pinnedRef},
			next:    reference{Version: v(t, "1.0.0"), Digest: "sha256:ccc"},
			wantErr: ErrInBandReapproval,
		},
		{
			name:    "changed + versao inferior -> recusa regressao",
			prev:    &record{State: StateChanged, Ref: reference{Version: v(t, "2.0.0"), Digest: "sha256:ddd"}},
			next:    reference{Version: v(t, "1.0.0"), Digest: "sha256:ccc"},
			wantErr: ErrVersionRegression,
		},
		{
			name:    "re-aprovar em pinned -> recusa (nao e changed)",
			prev:    &record{State: StatePinned, Ref: pinnedRef},
			next:    reference{Version: v(t, "2.0.0"), Digest: "sha256:ccc"},
			wantErr: ErrNotChanged,
		},
		{
			name:    "re-aprovar identidade desconhecida -> recusa",
			prev:    nil,
			next:    reference{Version: v(t, "2.0.0"), Digest: "sha256:ccc"},
			wantErr: ErrNotChanged,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := onReapprove(tc.prev, tc.next)
			if got.Err != tc.wantErr {
				t.Fatalf("err = %v, quer %v", got.Err, tc.wantErr)
			}
			if tc.wantErr == nil {
				if got.Next.State != tc.wantState {
					t.Errorf("state = %q, quer %q", got.Next.State, tc.wantState)
				}
				if !got.Next.Ref.equal(tc.next) {
					t.Errorf("ref = %+v, quer %+v", got.Next.Ref, tc.next)
				}
				if got.Cap != capReapproved {
					t.Errorf("cap = %q, quer %q", got.Cap, capReapproved)
				}
			}
		})
	}
}

// TestAdmits confirma que SÓ pinned admite (default-deny).
func TestAdmits(t *testing.T) {
	tests := []struct {
		state   TrustState
		wantAdm bool
	}{
		{StatePinned, true},
		{StateFirstSeen, false},
		{StateChanged, false},
		{TrustState("garbage"), false},
	}
	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			adm, reason := admits(tc.state)
			if adm != tc.wantAdm {
				t.Errorf("admits(%q) = %v, quer %v", tc.state, adm, tc.wantAdm)
			}
			if !adm && reason == "" {
				t.Errorf("uma nao-admissao deve ter razao legivel")
			}
		})
	}
}

// TestReferenceEqual confirma que a igualdade de referência exige versão E digest.
func TestReferenceEqual(t *testing.T) {
	base := reference{Version: v(t, "1.0.0"), Digest: "sha256:aaa"}
	tests := []struct {
		name string
		o    reference
		want bool
	}{
		{"identica", reference{Version: v(t, "1.0.0"), Digest: "sha256:aaa"}, true},
		{"digest diferente", reference{Version: v(t, "1.0.0"), Digest: "sha256:bbb"}, false},
		{"versao diferente", reference{Version: v(t, "1.0.1"), Digest: "sha256:aaa"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := base.equal(tc.o); got != tc.want {
				t.Errorf("equal = %v, quer %v", got, tc.want)
			}
		})
	}
}
