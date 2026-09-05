package broker

import (
	"errors"
	"testing"

	"github.com/aos-ref/platform/broker/internal/vault"
)

// AOS-330 — A POLÍTICA DECIDE NO MESMO NAMESPACE EM QUE A CHAVE VIVE.
//
// A normalização que forma o path do Vault não é injectiva — `sanitizeSegment` reduz tudo o que
// não seja `[A-Za-z0-9._-]` a `_`. Com a política a decidir sobre o valor CRU e o Vault a
// resolver sobre o NORMALIZADO, autorizar um provedor não era autorizar a chave que ele alcança:
// aprovado `acme:eu`, servia-se o material aprovisionado em `acme_eu`, que é outro provedor.
//
// A ESCOLHA (a segunda via do ticket, e a razão está no `authorizeProvider`): recusa-se o que a
// normalização altere, em vez de decidir sobre o normalizado. Decidir sobre o normalizado
// tornaria a colisão INVISÍVEL em vez de impossível — a política passaria a tratar `acme:eu` e
// `acme_eu` como o mesmo provedor, em silêncio.

// familiasQueDobram são conjuntos de valores DISTINTOS que a normalização funde num só segmento.
// O primeiro de cada família é a forma normalizada — a única que pode ser aceite.
var familiasQueDobram = [][]string{
	{"acme_eu", "acme:eu", "acme/eu", "acme eu"},
	{"stripe_x", "stripe/x", "stripe:x", "stripe x"},
	{"_", " ", "\t", "*", "/", ":"},
}

// TestAOS330_ValoresQueDobramNaoSaoAmbosAceites é o AC central. Não basta recusar os malformados:
// o que tem de ser impossível é DOIS valores distintos alcançarem a MESMA chave.
func TestAOS330_ValoresQueDobramNaoSaoAmbosAceites(t *testing.T) {
	t.Parallel()
	// Política que autoriza TUDO na classe, para que a única razão de recusa possível seja o
	// namespace. Sem isto o teste passaria por o eixo de autoridade negar primeiro.
	classProviders := map[string][]string{"agente": {ProviderAny}}

	for _, familia := range familiasQueDobram {
		aceites := make([]string, 0, len(familia))
		for _, v := range familia {
			if err := authorizeProvider(classProviders, "agente", nil, v); err == nil {
				aceites = append(aceites, v)
			}
		}
		if len(aceites) > 1 {
			t.Errorf("familia %q: %d valores aceites (%q) — dois provedores distintos alcancam a MESMA chave", familia, len(aceites), aceites)
		}
		// E o que sobrevive tem de ser a forma normalizada: se fosse outra, a decisão e o
		// path continuariam em namespaces diferentes.
		if len(aceites) == 1 && aceites[0] != familia[0] {
			t.Errorf("familia %q: o aceite foi %q, esperava a forma normalizada %q", familia, aceites[0], familia[0])
		}
	}
}

// TestAOS330_ValorAlteradoPelaNormalizacaoERecusadoAtribuivel — a recusa tem de ser
// `ErrProviderUndetermined` e não `ErrProviderOutOfScope`: o eixo não é um provedor fora da
// autoridade, é um valor que não identifica provedor nenhum. Antes disto, `" "` sob `enforced`
// era atribuído a «fora de escopo» — atribuição errada do mesmo defeito.
func TestAOS330_ValorAlteradoPelaNormalizacaoERecusadoAtribuivel(t *testing.T) {
	t.Parallel()
	posturas := []struct {
		nome           string
		classProviders map[string][]string
	}{
		{"unset", nil},
		{"enforced", map[string][]string{"agente": {ProviderAny}}},
	}
	maus := []string{" ", "\t", "*", "acme:eu", "acme/eu", "acme eu", "acmé"}

	for _, p := range posturas {
		for _, v := range maus {
			err := authorizeProvider(p.classProviders, "agente", nil, v)
			if !errors.Is(err, ErrProviderUndetermined) {
				t.Errorf("[%s] provedor %q: err = %v, quer ErrProviderUndetermined", p.nome, v, err)
			}
		}
	}
}

// TestAOS330_OGuardaApanhaOsTresQueEscapavam é o AC3, escrito à letra. Os três passavam o guarda
// `provider == ""` e chegavam ao Vault:
//
//   - `" "` e `"\t"` — sob `unset` devolviam nil, falsificando a afirmação de que «o Vault nunca
//     é consultado com um eixo em branco», que estava escrita como invariante e não era uma;
//   - `"*"` — NÃO era só não-apanhado: sob `enforced`, com o tecto da classe a declarar
//     `ProviderAny`, era AUTORIZADO, e produzia o path `_/…`.
func TestAOS330_OGuardaApanhaOsTresQueEscapavam(t *testing.T) {
	t.Parallel()
	for _, v := range []string{" ", "\t", ProviderAny} {
		if err := authorizeProvider(nil, "agente", nil, v); !errors.Is(err, ErrProviderUndetermined) {
			t.Errorf("[unset] %q: err = %v, quer ErrProviderUndetermined", v, err)
		}
		curinga := map[string][]string{"agente": {ProviderAny}}
		if err := authorizeProvider(curinga, "agente", nil, v); !errors.Is(err, ErrProviderUndetermined) {
			t.Errorf("[enforced, tecto curinga] %q: err = %v, quer ErrProviderUndetermined", v, err)
		}
	}
}

// TestAOS330_OsProvedoresLegitimosContinuamAPassar é o CONTROLO, e sem ele tudo o que está acima
// passaria com um `return ErrProviderUndetermined` incondicional — que negaria todas as trocas do
// sistema, um defeito muito pior do que o que se fecha.
func TestAOS330_OsProvedoresLegitimosContinuamAPassar(t *testing.T) {
	t.Parallel()
	bons := []string{"stripe", "openai", "acme-eu", "acme_eu", "acme.eu", "s3", "Provider1"}

	for _, v := range bons {
		if err := authorizeProvider(nil, "agente", nil, v); err != nil {
			t.Errorf("[unset] provedor legitimo %q recusado: %v", v, err)
		}
	}
	// Sob `enforced`, um provedor legítimo DENTRO do tecto passa, e um FORA continua a ser
	// recusado pelo eixo de autoridade — que é uma recusa diferente e tem de continuar
	// distinguível.
	pol := map[string][]string{"agente": {"stripe"}}
	if err := authorizeProvider(pol, "agente", nil, "stripe"); err != nil {
		t.Errorf("[enforced] provedor no tecto recusado: %v", err)
	}
	if err := authorizeProvider(pol, "agente", nil, "openai"); !errors.Is(err, ErrProviderOutOfScope) {
		t.Errorf("[enforced] fora do tecto: err = %v, quer ErrProviderOutOfScope — a recusa de AUTORIDADE tem de continuar distinta da de NAMESPACE", err)
	}
}

// TestAOS330_OQueAPoliticaAceitaEOQueOPathUsa fecha o eixo: para todo o valor que a política
// aceita, o segmento do path é o PRÓPRIO valor. É a propriedade que faltava, e é ela que torna
// «autorizar um provedor» equivalente a «autorizar a chave que ele alcança».
func TestAOS330_OQueAPoliticaAceitaEOQueOPathUsa(t *testing.T) {
	t.Parallel()
	candidatos := []string{"stripe", "acme_eu", "acme:eu", " ", "*", "acme.eu", "s3", "a/b", ""}
	var aceites int
	for _, v := range candidatos {
		if err := authorizeProvider(nil, "agente", nil, v); err != nil {
			continue
		}
		aceites++
		if !vault.SegmentoEstavel(v) {
			t.Errorf("a politica aceitou %q, que a normalizacao do path ALTERA — os dois namespaces voltaram a divergir", v)
		}
	}
	// NÃO-VACUOSIDADE: se nada fosse aceite, o `for` acima não asseria coisa nenhuma.
	if aceites == 0 {
		t.Fatal("nenhum candidato foi aceite — o teste nao esta a provar nada")
	}
}
