package authz

import (
	"errors"
	"reflect"
	"testing"
)

func TestFoldScopeMonotonicRestriction(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		sets [][]string
		want []string
	}{
		{
			name: "user_inter_class",
			sets: [][]string{{"read", "write", "admin"}, {"read", "write"}},
			want: []string{"read", "write"},
		},
		{
			name: "cadeia_profunda_so_restringe",
			sets: [][]string{{"read", "write", "admin"}, {"read", "write"}, {"read"}},
			want: []string{"read"},
		},
		{
			name: "elo_disjunto_colapsa_para_vazio",
			sets: [][]string{{"read", "write"}, {"admin"}},
			want: nil,
		},
		{
			name: "conjunto_unico",
			sets: [][]string{{"b", "a"}},
			want: []string{"a", "b"},
		},
		{
			name: "vazio",
			sets: nil,
			want: nil,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := FoldSets(tc.sets)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("FoldSets(%v) = %v, quer %v", tc.sets, got, tc.want)
			}
			// Prova estrutural: o resultado é subconjunto de CADA conjunto da dobra.
			for _, s := range tc.sets {
				if !Subset(got, s) {
					t.Fatalf("resultado %v nao e subconjunto de %v (alargou!)", got, s)
				}
			}
		})
	}
	// FoldScope variádico é equivalente a FoldSets.
	if got := FoldScope([]string{"a", "b"}, []string{"b", "c"}); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("FoldScope variadico = %v, quer [b]", got)
	}
}

func TestCheckNoEscalation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		claimed []string
		allowed []string
		wantErr bool
	}{
		{"reclama_subconjunto_ok", []string{"read"}, []string{"read", "write"}, false},
		{"reclama_igual_ok", []string{"read", "write"}, []string{"read", "write"}, false},
		{"reclama_vazio_nunca_escala", nil, []string{"read"}, false},
		{"reclama_acima_escala", []string{"read", "admin"}, []string{"read", "write"}, true},
		{"reclama_sobre_escopo_vazio_escala", []string{"read"}, nil, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := CheckNoEscalation(tc.claimed, tc.allowed)
			if tc.wantErr {
				if !errors.Is(err, ErrScopeEscalation) {
					t.Fatalf("quer ErrScopeEscalation, obteve %v", err)
				}
			} else if err != nil {
				t.Fatalf("nao quer erro, obteve %v", err)
			}
		})
	}
}

func TestRestrictAlong(t *testing.T) {
	t.Parallel()
	t.Run("cadeia_que_so_restringe", func(t *testing.T) {
		t.Parallel()
		got, err := RestrictAlong(
			[]string{"read", "write", "admin"},
			[]string{"read", "write"}, // agente
			[]string{"read"},          // sub-agente
		)
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if !reflect.DeepEqual(got, []string{"read"}) {
			t.Fatalf("escopo final = %v, quer [read]", got)
		}
	})
	t.Run("sub_agente_que_alarga_e_negado", func(t *testing.T) {
		t.Parallel()
		// Raiz concede {read}; um elo tenta reclamar {read, write} — alarga.
		_, err := RestrictAlong(
			[]string{"read"},
			[]string{"read", "write"},
		)
		if !errors.Is(err, ErrScopeEscalation) {
			t.Fatalf("quer ErrScopeEscalation, obteve %v", err)
		}
	})
}
