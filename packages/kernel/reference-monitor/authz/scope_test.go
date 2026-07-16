package authz

import (
	"reflect"
	"testing"
)

func TestNormalize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"vazio", nil, nil},
		{"ordena_e_dedup", []string{"c", "a", "b", "a"}, []string{"a", "b", "c"}},
		{"remove_vazias", []string{"", "x", ""}, []string{"x"}},
		{"so_vazias", []string{"", ""}, nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Normalize(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Normalize(%v) = %v, quer %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestIntersect(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		a, b []string
		want []string
	}{
		{"intersecao_basica", []string{"read", "write", "admin"}, []string{"read", "admin"}, []string{"admin", "read"}},
		{"disjuntos", []string{"read"}, []string{"write"}, nil},
		{"a_vazio", nil, []string{"read"}, nil},
		{"b_vazio", []string{"read"}, nil, nil},
		{"determinista_ordenado", []string{"z", "a", "m"}, []string{"m", "z", "a"}, []string{"a", "m", "z"}},
		{"dedup_no_resultado", []string{"read", "read"}, []string{"read"}, []string{"read"}},
		{"ignora_vazias", []string{"", "read"}, []string{"read", ""}, []string{"read"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Intersect(tc.a, tc.b)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Intersect(%v,%v) = %v, quer %v", tc.a, tc.b, got, tc.want)
			}
			// Comutatividade do conjunto resultante (ordenado, logo igualdade directa).
			if rev := Intersect(tc.b, tc.a); !reflect.DeepEqual(rev, tc.want) {
				t.Fatalf("Intersect nao comutativo: %v vs %v", rev, tc.want)
			}
			// O resultado é subconjunto de AMBOS (menor privilégio, nunca alarga).
			if !Subset(got, tc.a) || !Subset(got, tc.b) {
				t.Fatalf("resultado %v nao e subconjunto de ambos %v / %v", got, tc.a, tc.b)
			}
		})
	}
}

func TestSubsetAndContains(t *testing.T) {
	t.Parallel()
	if !Subset(nil, []string{"a"}) {
		t.Fatal("conjunto vazio deve ser subconjunto de qualquer conjunto")
	}
	if Subset([]string{"a", "b"}, []string{"a"}) {
		t.Fatal("{a,b} nao e subconjunto de {a}")
	}
	if !Subset([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("{a} e subconjunto de {a,b}")
	}
	if !Contains([]string{"a", "b"}, "b") {
		t.Fatal("Contains devia encontrar b")
	}
	if Contains([]string{"a"}, "z") {
		t.Fatal("Contains nao devia encontrar z")
	}
}

func TestAuthorizeDefaultDeny(t *testing.T) {
	t.Parallel()
	eff := []string{"read", "write"}
	cases := []struct {
		name       string
		capability string
		wantAllow  bool
	}{
		{"dentro_do_escopo", "read", true},
		{"fora_do_escopo", "admin", false},
		{"capability_vazia", "", false},
		{"escopo_vazio_nega", "read", false}, // sobreposto abaixo com eff vazio
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := eff
			if tc.name == "escopo_vazio_nega" {
				e = nil
			}
			dec := Authorize(e, tc.capability)
			if dec.Allow != tc.wantAllow {
				t.Fatalf("Authorize(%v,%q).Allow = %v, quer %v", e, tc.capability, dec.Allow, tc.wantAllow)
			}
			if !dec.Allow && dec.Reason == "" {
				t.Fatal("negacao deve ter Reason nao-vazio")
			}
		})
	}
}

func TestStaticAuthoritySource(t *testing.T) {
	t.Parallel()
	src := NewStaticAuthoritySource().
		Set("human:alice", "read", "write", "admin").
		Set("agent:researcher", "read", "write")

	if caps, ok := src.Authority("human:alice"); !ok || !reflect.DeepEqual(caps, []string{"admin", "read", "write"}) {
		t.Fatalf("alice = %v ok=%v", caps, ok)
	}
	if _, ok := src.Authority("desconhecido"); ok {
		t.Fatal("sujeito desconhecido deve devolver ok=false")
	}
	// Mutação do resultado não afecta a fonte (cópia defensiva).
	caps, _ := src.Authority("agent:researcher")
	caps[0] = "MUTADO"
	caps2, _ := src.Authority("agent:researcher")
	if caps2[0] == "MUTADO" {
		t.Fatal("Authority deve devolver copia defensiva")
	}
}
