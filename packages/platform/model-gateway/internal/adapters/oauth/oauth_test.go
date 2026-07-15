package oauth_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/internal/adapters/oauth"
)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// TestProviders_FluxoOAuthPorProvider prova o fluxo OAuth de CADA um dos três
// provedores: o nome e o mecanismo são os esperados, a troca de material produz
// uma credencial NÃO-vazia para o par (provider, região) pedido, e é
// DETERMINISTA (mesmo material → mesmo KeyID).
func TestProviders_FluxoOAuthPorProvider(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	opts := oauth.Options{TokenTTL: 5 * time.Minute, Clock: fixedClock(now)}

	cases := []struct {
		name string
		prov oauth.Provider
		mech oauth.Mechanism
	}{
		{"openai", oauth.NewOpenAI(opts), oauth.MechanismAPIKey},
		{"anthropic", oauth.NewAnthropic(opts), oauth.MechanismServiceOAuth},
		{"google", oauth.NewGoogle(opts), oauth.MechanismFederated},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.prov.Name() != tc.name {
				t.Errorf("Name = %q, quer %q", tc.prov.Name(), tc.name)
			}
			if tc.prov.Mechanism() != tc.mech {
				t.Errorf("Mechanism = %q, quer %q", tc.prov.Mechanism(), tc.mech)
			}
			mat := oauth.NewMaterial(tc.name, "eu", "material-bruto-do-vault", now.Add(time.Hour))
			cred, exp, err := tc.prov.Exchange(context.Background(), mat)
			if err != nil {
				t.Fatalf("Exchange: %v", err)
			}
			if cred.Provider != tc.name || cred.Region != "eu" {
				t.Errorf("credencial mal atribuida: provider=%q regiao=%q", cred.Provider, cred.Region)
			}
			if cred.KeyID() == "" {
				t.Fatal("credencial sem segredo (KeyID vazio) — troca falhou")
			}
			// A expiração REAL do token é devolvida e é positiva (a origem limita o
			// TTL do cache por ela). Pass-through usa a expiração do lease; os OAuth
			// derivados usam now+TokenTTL.
			if exp.IsZero() || !exp.After(now) {
				t.Errorf("expiracao do token invalida: %v (now=%v)", exp, now)
			}
			// Determinismo: repetir a troca do mesmo material dá o mesmo KeyID.
			cred2, _, err := tc.prov.Exchange(context.Background(), mat)
			if err != nil {
				t.Fatalf("Exchange (2): %v", err)
			}
			if cred.KeyID() != cred2.KeyID() {
				t.Errorf("troca nao-determinista: %s != %s", cred.KeyID(), cred2.KeyID())
			}
		})
	}
}

// TestProviders_TokensDistintosPorProvider prova que o MESMO material bruto
// produz tokens de infra DIFERENTES por provedor (mecanismos distintos) — a
// credencial é específica do provedor, não intercambiável.
func TestProviders_TokensDistintosPorProvider(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	opts := oauth.Options{Clock: fixedClock(now)}
	mat := func(p string) oauth.Material { return oauth.NewMaterial(p, "eu", "mesmo-material", now) }

	oa, _, _ := oauth.NewOpenAI(opts).Exchange(context.Background(), mat("openai"))
	an, _, _ := oauth.NewAnthropic(opts).Exchange(context.Background(), mat("anthropic"))
	go_, _, _ := oauth.NewGoogle(opts).Exchange(context.Background(), mat("google"))

	ids := map[string]bool{oa.KeyID(): true, an.KeyID(): true, go_.KeyID(): true}
	if len(ids) != 3 {
		t.Fatalf("esperava 3 tokens distintos por provedor, obtidos KeyIDs=%v", ids)
	}
}

// TestFederated_RegiaoNaDerivacao prova que o token federado (Google) depende da
// REGIÃO: o mesmo material em regiões diferentes produz tokens diferentes (a
// audiência é regional — a chave de uma região não vale noutra).
func TestFederated_RegiaoNaDerivacao(t *testing.T) {
	t.Parallel()
	opts := oauth.Options{Clock: fixedClock(time.Unix(1, 0))}
	g := oauth.NewGoogle(opts)
	eu, _, _ := g.Exchange(context.Background(), oauth.NewMaterial("google", "eu", "m", time.Unix(1, 0)))
	us, _, _ := g.Exchange(context.Background(), oauth.NewMaterial("google", "us", "m", time.Unix(1, 0)))
	if eu.KeyID() == us.KeyID() {
		t.Fatal("token federado nao depende da regiao (audiencia nao regional)")
	}
}

// TestExchange_MaterialVazio_FailClosed: material sem segredo é recusado
// (ErrNoMaterial), nunca produz um token vazio.
func TestExchange_MaterialVazio_FailClosed(t *testing.T) {
	t.Parallel()
	opts := oauth.Options{Clock: fixedClock(time.Unix(1, 0))}
	for _, p := range []oauth.Provider{oauth.NewOpenAI(opts), oauth.NewAnthropic(opts), oauth.NewGoogle(opts)} {
		_, _, err := p.Exchange(context.Background(), oauth.NewMaterial(p.Name(), "eu", "", time.Unix(1, 0)))
		if err == nil {
			t.Fatalf("%s: material vazio devia falhar fail-closed", p.Name())
		}
	}
}

// TestRedaction_MaterialEToken prova que nem [oauth.Material] nem [oauth.Token]
// revelam o segredo por %v/%s/%+v nem por JSON (ADR-006). Como Token não é
// exportável fora do pacote, cobre-se Material (o portador do segredo bruto) e
// a credencial de saída.
func TestRedaction_MaterialEToken(t *testing.T) {
	t.Parallel()
	const secret = "SEGREDO-BRUTO-XYZ"
	mat := oauth.NewMaterial("openai", "eu", secret, time.Unix(1, 0))
	for _, s := range []string{
		fmt.Sprintf("%v", mat), mat.String(), fmt.Sprintf("%+v", mat),
	} {
		if strings.Contains(s, secret) {
			t.Fatalf("Material revelou o segredo: %q", s)
		}
		if !strings.Contains(s, "REDACTED") {
			t.Errorf("Material devia redigir: %q", s)
		}
	}
	// MarshalJSON IMPÕE a redação: o JSON serializa a forma redigida (não a
	// estrutura), pelo que nunca contém o segredo e contém o marcador REDACTED.
	if b, _ := json.Marshal(mat); strings.Contains(string(b), secret) {
		t.Fatalf("JSON de Material revelou o segredo: %s", b)
	} else if !strings.Contains(string(b), "REDACTED") {
		t.Errorf("JSON de Material devia redigir (MarshalJSON): %s", b)
	}
	// A credencial de saída também redige (reutiliza a redação de AOS-055).
	cred, _, _ := oauth.NewOpenAI(oauth.Options{Clock: fixedClock(time.Unix(1, 0))}).
		Exchange(context.Background(), mat)
	var _ adapters.Credential = cred
	if strings.Contains(fmt.Sprintf("%v", cred), secret) {
		t.Fatal("credencial de saida revelou o segredo")
	}
}

// TestRegistry_GetEDefault confirma o registo por nome e o registo default dos
// três provedores.
func TestRegistry_GetEDefault(t *testing.T) {
	t.Parallel()
	reg := oauth.DefaultRegistry(oauth.Options{Clock: fixedClock(time.Unix(1, 0))})
	for _, name := range []string{"openai", "anthropic", "google"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("provedor %q em falta no registo default", name)
		}
	}
	if _, ok := reg.Get("desconhecido"); ok {
		t.Error("provedor desconhecido nao devia estar registado")
	}
}
