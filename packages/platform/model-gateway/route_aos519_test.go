package modelgateway_test

// AOS-519 — O QUE MUDA NO GATEWAY COM OS PERFIS DE REGISTOS ASSINADOS.
//
// Duas coisas, e ambas inertes sem perfis a mais: o rótulo do modelo servido fecha-se no conjunto
// de perfis do gateway (e não só na tabela em código), e cada perfil que veio de um registo de
// entrada fica selado no audit de governação.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// aos519Particao é a cadeia do audit onde ficam os carregamentos de perfis.
const aos519Particao = "modelgw-gov:route-profiles"

const aos519PerfilNovo = `{"requested":"gpt-4o","expected_model":"openai/modelo-novo-4.5","wire_class":"openai-chat-completions","capabilities":["tools"]}`

func aos519Config(t *testing.T, store audit.Store) modelgateway.ProductionConfig {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	return prodConfig(store, srv.URL, srv.Client(), []modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu"}})
}

func aos519Registo(p modelgateway.RouteProfile) modelgateway.RouteProfileEntry {
	return modelgateway.RouteProfileEntry{
		Requested: p.Requested, ProfileDigest: p.Digest(),
		EntryDigest: "sha256:" + strings.Repeat("e", 64), ReportDigest: "sha256:" + strings.Repeat("f", 64),
	}
}

// O RÓTULO DO MODELO SERVIDO: com o conjunto nil é o da tabela em código, byte a byte; com um
// perfil a mais, o modelo esperado dele é um valor do conjunto, e o que ele substituiu deixa de ser.
func TestAOS519_RotulosDoModeloServidoPorConjunto(t *testing.T) {
	deSempre := []string{"openai/kimi-for-coding", "openai/k3", "outro", "nao_reportado"}
	if got := (*modelgateway.RouteProfileSet)(nil).ServedModelLabels(); !reflect.DeepEqual(got, deSempre) || !reflect.DeepEqual(modelgateway.ServedModelLabels(), deSempre) {
		t.Fatalf("rotulos sem perfis a mais = %v; quer %v", got, deSempre)
	}
	semCandidatos, err := modelgateway.NewRouteProfileSet()
	if err != nil {
		t.Fatal(err)
	}
	if got := semCandidatos.ServedModelLabels(); !reflect.DeepEqual(got, deSempre) {
		t.Fatalf("rotulos de um conjunto sem candidatos = %v; quer %v", got, deSempre)
	}
	set, err := modelgateway.NewRouteProfileSet(aos513Perfil(t, aos519PerfilNovo))
	if err != nil {
		t.Fatal(err)
	}
	// O perfil novo substitui a entrada `gpt-4o` da tabela, no lugar dela: `openai/k3` continua no
	// conjunto porque a entrada `k3` ainda o espera.
	quer := []string{"openai/kimi-for-coding", "openai/modelo-novo-4.5", "openai/k3", "outro", "nao_reportado"}
	if got := set.ServedModelLabels(); !reflect.DeepEqual(got, quer) {
		t.Fatalf("rotulos com o perfil novo = %v; quer %v", got, quer)
	}
	for entra, sai := range map[string]string{
		"openai/modelo-novo-4.5": "openai/modelo-novo-4.5", "openai/k3": "openai/k3",
		"openai/desconhecido": "outro", "": "nao_reportado",
	} {
		if got := set.ServedModelLabel(entra); got != sai {
			t.Errorf("ServedModelLabel(%q) = %q; quer %q", entra, got, sai)
		}
	}
	// Sem o perfil novo, o mesmo nome é `outro`: o conjunto continua fechado.
	if got := modelgateway.ServedModelLabel("openai/modelo-novo-4.5"); got != "outro" {
		t.Errorf("fora do conjunto o modelo novo tinha de ser `outro`; veio %q", got)
	}
}

// O SELO «PERFIL CARREGADO»: um por registo, com os três digests, na cadeia própria.
func TestAOS519_OSeloDoPerfilCarregado(t *testing.T) {
	ctx := context.Background()
	perfil := aos513Perfil(t, aos519PerfilNovo)

	// Sem registos, o gateway não escreve nada nessa cadeia: o audit é o de sempre.
	store := audit.NewMemStore()
	cfg := aos519Config(t, store)
	cfg.RouteProfiles = []modelgateway.RouteProfile{perfil}
	if _, err := modelgateway.NewProduction(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if head, _ := store.Head(ctx, aos519Particao); head != 0 {
		t.Fatalf("sem registos de entrada ficaram %d selos de perfil carregado", head)
	}
	particoesDeSempre := store.Partitions()

	// Com o registo, fica UM selo, com os três digests.
	store = audit.NewMemStore()
	cfg = aos519Config(t, store)
	cfg.RouteProfiles = []modelgateway.RouteProfile{perfil}
	registo := aos519Registo(perfil)
	cfg.RouteProfileEntries = []modelgateway.RouteProfileEntry{registo}
	if _, err := modelgateway.NewProduction(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	head, _ := store.Head(ctx, aos519Particao)
	if head != 1 {
		t.Fatalf("selos de perfil carregado = %d; quer 1", head)
	}
	selo, tem, err := store.At(ctx, aos519Particao, 1)
	if err != nil || !tem {
		t.Fatalf("selo: %v %v", tem, err)
	}
	if len(selo.Obligations) != 1 {
		t.Fatalf("obligations = %d", len(selo.Obligations))
	}
	quer := map[string]string{
		"route_profile": "loaded", "requested": "gpt-4o", "expected_model": "openai/modelo-novo-4.5",
		"profile_digest": perfil.Digest(), "entry_digest": registo.EntryDigest, "report_digest": registo.ReportDigest,
	}
	if !reflect.DeepEqual(selo.Obligations[0].Params, quer) {
		t.Fatalf("selo = %v; quer %v", selo.Obligations[0].Params, quer)
	}
	if selo.Capability != "route-profile:load" || selo.PolicyVersion != "route-profile/"+perfil.Digest() || selo.Resource.Value != "gpt-4o" {
		t.Fatalf("selo: capability=%s versao=%s recurso=%s", selo.Capability, selo.PolicyVersion, selo.Resource.Value)
	}
	// As outras cadeias são as de um gateway sem registos: o selo é a única coisa a mais.
	if got := store.Partitions(); len(got) != len(particoesDeSempre)+1 {
		t.Fatalf("cadeias com o registo = %v; sem ele = %v", got, particoesDeSempre)
	}
}

// FAIL-CLOSED: um registo que não corresponde a um perfil candidato — outro nome, outro digest,
// um digest em falta, ou dois para o mesmo nome — não compõe gateway, e não deixa selo nenhum.
func TestAOS519_RegistoQueNaoCorrespondeNaoCompoe(t *testing.T) {
	ctx := context.Background()
	perfil := aos513Perfil(t, aos519PerfilNovo)
	bom := aos519Registo(perfil)
	casos := map[string][]modelgateway.RouteProfileEntry{
		"outro digest de perfil":  {{Requested: bom.Requested, ProfileDigest: "sha256:" + strings.Repeat("0", 64), EntryDigest: bom.EntryDigest, ReportDigest: bom.ReportDigest}},
		"nome sem perfil":         {{Requested: "nome-sem-perfil", ProfileDigest: bom.ProfileDigest, EntryDigest: bom.EntryDigest, ReportDigest: bom.ReportDigest}},
		"sem digest do registo":   {{Requested: bom.Requested, ProfileDigest: bom.ProfileDigest, ReportDigest: bom.ReportDigest}},
		"sem digest do relatorio": {{Requested: bom.Requested, ProfileDigest: bom.ProfileDigest, EntryDigest: bom.EntryDigest}},
		"dois para o mesmo nome":  {bom, bom},
	}
	for nome, registos := range casos {
		store := audit.NewMemStore()
		cfg := aos519Config(t, store)
		cfg.RouteProfiles = []modelgateway.RouteProfile{perfil}
		cfg.RouteProfileEntries = registos
		if _, err := modelgateway.NewProduction(ctx, cfg); !errors.Is(err, modelgateway.ErrBadRouteProfile) {
			t.Errorf("%s: tinha de recusar com ErrBadRouteProfile; veio %v", nome, err)
		}
		if head, _ := store.Head(ctx, aos519Particao); head != 0 {
			t.Errorf("%s: ficaram %d selos de um carregamento recusado", nome, head)
		}
	}
	// Um registo para um perfil da TABELA EM CÓDIGO que não veio como candidato também corresponde
	// — o conjunto é um só —, mas o nó nunca o entrega: só entrega registos dos perfis que leu.
	store := audit.NewMemStore()
	cfg := aos519Config(t, store)
	daTabela, _ := modelgateway.RouteProfileFor("k3")
	cfg.RouteProfileEntries = []modelgateway.RouteProfileEntry{aos519Registo(daTabela)}
	if _, err := modelgateway.NewProduction(ctx, cfg); err != nil {
		t.Fatalf("um registo cujo digest e o do perfil em vigor compoe: %v", err)
	}
}
