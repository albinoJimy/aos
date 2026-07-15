package authn_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/aos-ref/platform/model-gateway/pipeline/authn"
)

// TestEffectiveAuthority_Intersection prova que a autoridade efectiva é
// utilizador ∩ classe: subconjunto de AMBOS, ordenada e sem duplicados (menor
// privilégio).
func TestEffectiveAuthority_Intersection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		user  []string
		class []string
		want  []string
	}{
		{
			name:  "intersecao propria",
			user:  []string{"model:invoke", "admin", "fs:read"},
			class: []string{"fs:read", "model:invoke", "net"},
			want:  []string{"fs:read", "model:invoke"},
		},
		{
			name:  "classe restringe utilizador amplo",
			user:  []string{"model:invoke", "admin"},
			class: []string{"model:invoke"},
			want:  []string{"model:invoke"},
		},
		{
			name:  "utilizador restringe classe ampla",
			user:  []string{"model:invoke"},
			class: []string{"model:invoke", "admin", "billing"},
			want:  []string{"model:invoke"},
		},
		{
			name:  "disjuntos -> vazio",
			user:  []string{"admin"},
			class: []string{"model:invoke"},
			want:  nil,
		},
		{
			name:  "utilizador vazio -> vazio",
			user:  nil,
			class: []string{"model:invoke"},
			want:  nil,
		},
		{
			name:  "duplicados removidos",
			user:  []string{"model:invoke", "model:invoke", "x"},
			class: []string{"model:invoke", "x", "x"},
			want:  []string{"model:invoke", "x"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := authn.EffectiveAuthority(tc.user, tc.class)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("EffectiveAuthority(%v,%v) = %v, quer %v", tc.user, tc.class, got, tc.want)
			}
			// Menor privilégio: o resultado é subconjunto de ambos.
			assertSubset(t, got, tc.user)
			assertSubset(t, got, tc.class)
		})
	}
}

func assertSubset(t *testing.T, sub, super []string) {
	t.Helper()
	set := map[string]bool{}
	for _, s := range super {
		set[s] = true
	}
	for _, s := range sub {
		if !set[s] {
			t.Fatalf("%q nao esta no superconjunto %v", s, super)
		}
	}
}

// TestStaticAuthority resolve capabilities de utilizador e classe (o resolver de
// referência).
func TestStaticAuthority(t *testing.T) {
	t.Parallel()
	r := authn.NewStaticAuthority().
		SetUser("alice", "model:invoke", "admin").
		SetClass("reader", "model:invoke")
	uc, err := r.UserAuthority(context.Background(), "alice")
	if err != nil || !reflect.DeepEqual(uc, []string{"model:invoke", "admin"}) {
		t.Fatalf("UserAuthority = %v,%v", uc, err)
	}
	cc, err := r.ClassAuthority(context.Background(), "reader")
	if err != nil || !reflect.DeepEqual(cc, []string{"model:invoke"}) {
		t.Fatalf("ClassAuthority = %v,%v", cc, err)
	}
	eff := authn.EffectiveAuthority(uc, cc)
	if !reflect.DeepEqual(eff, []string{"model:invoke"}) {
		t.Fatalf("efectiva = %v, quer [model:invoke]", eff)
	}
	// Utilizador/classe desconhecidos -> vazio (deny fail-closed a jusante).
	if uc, _ := r.UserAuthority(context.Background(), "ninguem"); len(uc) != 0 {
		t.Fatalf("utilizador desconhecido devia ter autoridade vazia, tem %v", uc)
	}
}
