package broker

import (
	"context"
	"errors"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// TestExchange_Escopo_ForaDeEscopoNegado prova que o broker só troca por
// credenciais consistentes com o escopo do token (autoridade efectiva =
// utilizador ∩ classe, AOS-057) e NEGA fail-closed fora dele.
func TestExchange_Escopo_ForaDeEscopoNegado(t *testing.T) {
	tests := []struct {
		name       string
		capability string
		wantDenied bool
	}{
		{"in-scope (user∩classe tem charge)", provInScopeCap, false},
		{"classe tem mas utilizador nao (refund)", classScopedCap, true},
		{"nem utilizador nem classe (admin.delete)", unknownCap, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newStack(t, time.Minute)
			// aprovisiona material para a capability pedida (isola o escopo do vault).
			st.vault.Put(VaultKey{Provider: provider, Region: region, Capability: tc.capability}, sentinel)

			h, err := st.broker.Exchange(context.Background(), request("run-1", tc.capability))
			if tc.wantDenied {
				var de *DeniedError
				if !errors.As(err, &de) {
					t.Fatalf("esperado *DeniedError, obtido %v", err)
				}
				if h != "" {
					t.Fatalf("handle emitido fora de escopo: %q", h)
				}
				// registou a NEGAÇÃO (mediada) e NÃO emitiu a troca.
				evs := readStream(t, st.es, "run-1")
				var denied, issued bool
				for _, e := range evs {
					switch e.Type {
					case referencemonitor.EventTypeDenied:
						denied = true
					case exchangeEventType:
						issued = true
					}
				}
				if !denied {
					t.Error("negacao fora de escopo nao registada")
				}
				if issued {
					t.Error("troca emitida apesar de fora de escopo")
				}
				return
			}
			if err != nil {
				t.Fatalf("in-scope devia permitir: %v", err)
			}
			if h == "" {
				t.Fatal("handle vazio in-scope")
			}
		})
	}
}

func TestEffectiveAuthority_Interseccao(t *testing.T) {
	tests := []struct {
		name  string
		user  []string
		class []string
		want  []string
	}{
		{"interseccao normal", []string{"a", "b", "c"}, []string{"b", "c", "d"}, []string{"b", "c"}},
		{"vazia (sem sobreposicao)", []string{"a"}, []string{"b"}, nil},
		{"classe vazia nega tudo", []string{"a", "b"}, nil, nil},
		{"utilizador vazio nega tudo", nil, []string{"a"}, nil},
		{"dedup e ordenacao", []string{"c", "a", "a", "b"}, []string{"a", "b", "c"}, []string{"a", "b", "c"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EffectiveAuthority(tc.user, tc.class)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestPermitsCapability(t *testing.T) {
	user := []string{"cap:a", "cap:b"}
	class := []string{"cap:b", "cap:c"}
	if !permitsCapability(user, class, "cap:b") {
		t.Error("cap:b devia estar na interseccao")
	}
	if permitsCapability(user, class, "cap:a") {
		t.Error("cap:a (so utilizador) nao devia passar")
	}
	if permitsCapability(user, class, "cap:c") {
		t.Error("cap:c (so classe) nao devia passar")
	}
	if permitsCapability(user, class, "") {
		t.Error("capability vazia nunca passa")
	}
}

// TestScopeGate_ForaDoToolID_Neutro garante que o gate não interfere com outras
// tools na mesma cadeia de mediação.
func TestScopeGate_ForaDoToolID_Neutro(t *testing.T) {
	g := NewScopeGate(DefaultExchangeToolID, defaultClassScopes())
	res, err := g.Evaluate(context.Background(), &referencemonitor.Call{
		ToolID:     "outra.tool",
		Capability: unknownCap,
		Principal:  principal(),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Decision != referencemonitor.HookAllow {
		t.Fatalf("esperado Allow fora do toolID, obtido %v", res.Decision)
	}
}
