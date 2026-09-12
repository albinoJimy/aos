package registry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
	eventstore "github.com/aos-ref/substrate/eventstore"
)

// AOS-334 — PUBLICAR UM `mcp_server` SEM `ManifestDigest` É RECUSADO.
//
// O AOS-320 fechou por CONVENÇÃO: só o `mcp.Host.stage` publica `mcp_server`, e ele grava sempre
// o digest. Convenção não é gate — qualquer outro caminho de publicação (`promotion.Promote`, um
// seed de catálogo, um import) reabria em silêncio o defeito que o AOS-320 existe para eliminar.
//
// O QUE O DEFEITO ERA: o contrato de um `mcp_server` é `{Egress, ManifestDigest}`. Sem o digest
// sobra a CLASSE DE EGRESS — um valor de cardinalidade quase nula — pelo que dois servidores MCP
// sem nada em comum produzem o MESMO digest de contrato. Um digest que não distingue não ancora
// nada.

func novoRegistryDeTeste(t *testing.T, opts ...Option) *Registry {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	base := []Option{WithClock(fixedClock()), WithAdmissionVerifier(allowVerifier{})}
	reg, err := New(store, append(base, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return reg
}

func aos334Req(kind domain.ArtifactKind, c domain.Contract) PublishRequest {
	return PublishRequest{
		ID: "art.aos334", Version: domain.Version{Major: 1}, Kind: kind,
		Origin: "mcp://host.example", Publisher: "pub:aos334",
		Contract: c,
	}
}

func TestAOS334_MCPServerSemManifestoERecusado(t *testing.T) {
	t.Parallel()
	reg := novoRegistryDeTeste(t)

	casos := []struct {
		nome     string
		contrato domain.Contract
	}{
		{"campo ausente", domain.Contract{Egress: domain.EgressInternal}},
		{"campo vazio", domain.Contract{Egress: domain.EgressInternal, ManifestDigest: ""}},
		// Só espaços é a mesma ausência com outra roupa: um digest que não identifica nada.
		{"campo so com espacos", domain.Contract{Egress: domain.EgressInternal, ManifestDigest: "   "}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			_, err := reg.Publish(context.Background(), aos334Req(domain.KindMCPServer, c.contrato))
			if err == nil {
				t.Fatal("publicar um mcp_server sem manifesto tinha de ser recusado")
			}
			// ATRIBUÍVEL NAS DUAS IDENTIDADES: quem só distingue «pedido inválido» continua a
			// funcionar, e quem quiser a causa exacta tem-na. É o molde de
			// `validateContractSchemas`, que embrulha ErrInvalidRequest + a causa raiz.
			if !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("err = %v, quer ErrInvalidRequest", err)
			}
			if !errors.Is(err, ErrManifestDigestRequired) {
				t.Errorf("err = %v, quer ErrManifestDigestRequired", err)
			}
		})
	}
}

// TestAOS334_ToolESkillSemManifestoContinuamAPublicar é o CONTROLO que o AC nomeia. O campo é
// ESPECÍFICO DO KIND: sem isto, uma regra escrita como «todo o contrato exige manifesto» passaria
// o teste acima e partiria a publicação de todas as tools e skills do catálogo.
func TestAOS334_ToolESkillSemManifestoContinuamAPublicar(t *testing.T) {
	t.Parallel()
	for _, kind := range []domain.ArtifactKind{domain.KindTool, domain.KindSkill} {
		t.Run(string(kind), func(t *testing.T) {
			reg := novoRegistryDeTeste(t)
			req := aos334Req(kind, domain.Contract{Egress: domain.EgressNone})
			if _, err := reg.Publish(context.Background(), req); err != nil {
				t.Fatalf("%s sem manifesto tem de continuar a publicar: %v", kind, err)
			}
		})
	}
}

// TestAOS334_ARegraEExigeNaoExclusivo é o SEGUNDO controlo, e fecha a formulação errada.
//
// As entradas `kind=tool` derivadas de um servidor MCP TRANSPORTAM a âncora de propósito — é a
// correcção A1 do AOS-320, e é o que liga cada tool ao manifesto de onde veio. Uma regra escrita
// como «só mcp_server pode ter manifesto» passaria os dois testes acima e destruiria essa
// ligação em silêncio.
func TestAOS334_ARegraEExigeNaoExclusivo(t *testing.T) {
	t.Parallel()
	reg := novoRegistryDeTeste(t)
	req := aos334Req(domain.KindTool, domain.Contract{
		Egress:         domain.EgressInternal,
		ManifestDigest: "sha256:ancora-do-servidor-de-origem",
	})
	e, err := reg.Publish(context.Background(), req)
	if err != nil {
		t.Fatalf("uma tool COM ancora tem de publicar — e a ancora e o que a liga ao manifesto: %v", err)
	}
	if e.Contract.ManifestDigest == "" {
		t.Error("a ancora perdeu-se na publicacao")
	}
}

// TestAOS334_ARecusaAconteceAntesDeHashear fixa a ORDEM, que não é cosmética: o digest é o que
// pina o artefacto, e hashear uma entrada que vai ser recusada é produzir a identidade de uma
// coisa que não existe. É a disciplina que `validateContractSchemas` estabeleceu, e o teste
// existe para que uma mudança futura não a inverta ao mover a validação para `Entry.Validate`.
func TestAOS334_ARecusaAconteceAntesDeHashear(t *testing.T) {
	t.Parallel()
	var hasheou bool
	reg := novoRegistryDeTeste(t, WithDigester(digesterEspiao{&hasheou}))
	_, err := reg.Publish(context.Background(), aos334Req(domain.KindMCPServer, domain.Contract{Egress: domain.EgressInternal}))
	if err == nil {
		t.Fatal("devia recusar")
	}
	if hasheou {
		t.Error("hasheou-se uma entrada que ia ser recusada — a validacao ficou DEPOIS do digest")
	}
}

func digestDeTeste(semente string) string {
	return digest.DigestBytes([]byte(semente))
}

// injectaEntradaLegada põe no log a forma HISTÓRICA (mcp_server sem manifesto), que o `Publish`
// já recusa. Escreve o evento directamente, que é o que o replay de um log antigo faz.
func injectaEntradaLegada(t *testing.T, reg *Registry, id string, v domain.Version) {
	t.Helper()
	e := domain.Entry{
		ID: id, Version: v, Kind: domain.KindMCPServer,
		Contract:   domain.Contract{Egress: domain.EgressInternal},
		Provenance: domain.Provenance{Origin: "mcp://legado", Publisher: "pub:legado", Timestamp: "2026-01-01T00:00:00Z", Trust: domain.TrustFirstSeen},
		Status:     domain.StatusStaging,
	}
	e.Digest = (digest.SHA256Digester{}).Digest(e.Kind, e.Contract)
	payload, err := encodePublished(e)
	if err != nil {
		t.Fatalf("encodePublished: %v", err)
	}
	// Escreve-se pelo MESMO journal do Registry — é o replay de um log antigo, não uma via
	// paralela. O teste está no pacote de propósito: esta forma já não se consegue produzir
	// pela API pública.
	if _, err := reg.journal.Append(context.Background(), EventTypePublished, payload, "published:legado", "pub:legado", 0); err != nil {
		t.Fatalf("injectar legado: %v", err)
	}
}

type digesterEspiao struct{ chamado *bool }

func (d digesterEspiao) Digest(kind domain.ArtifactKind, c domain.Contract) string {
	*d.chamado = true
	return "sha256:" + strings.Repeat("0", 64)
}

// ═══════════════════════════════════════════════════════════════════════════════════════════
// O QUE A REVISÃO ADVERSARIAL ENCONTROU, e que a primeira versão deixava passar.
// ═══════════════════════════════════════════════════════════════════════════════════════════

// TestAOS334_LegadoNaoChegaAActive fecha o achado (1a). A primeira versão fechou o `Publish` e
// declarou que as entradas legadas «continuam a resolver, de propósito» — confundindo duas coisas
// diferentes. LER uma entrada histórica é legítimo; PROMOVÊ-LA a `active` é uma decisão NOVA,
// tomada hoje, e `active` é o estado que resolve e é usado.
//
// A entrada é injectada por replay do log, que é a única via — o `Publish` já a recusa.
func TestAOS334_LegadoNaoChegaAActive(t *testing.T) {
	t.Parallel()
	reg := novoRegistryDeTeste(t)
	ctx := context.Background()

	// Publica-se um mcp_server LEGÍTIMO e depois degrada-se o contrato no log, que é a forma
	// determinista de obter no catálogo a forma histórica sem passar pela validação nova.
	valido := aos334Req(domain.KindMCPServer, domain.Contract{
		Egress: domain.EgressInternal, ManifestDigest: digestDeTeste("legado"),
	})
	if _, err := reg.Publish(ctx, valido); err != nil {
		t.Fatalf("publicar o valido: %v", err)
	}
	// Controlo da premissa: a forma válida PROMOVE.
	if _, err := reg.SetStatus(ctx, valido.ID, valido.Version, domain.StatusActive); err != nil {
		t.Fatalf("a forma valida tem de promover: %v", err)
	}

	// E agora a histórica: mesma via de promoção, contrato sem manifesto.
	legado := novoRegistryDeTeste(t)
	injectaEntradaLegada(t, legado, "art.legado", domain.Version{Major: 1})
	_, err := legado.SetStatus(ctx, "art.legado", domain.Version{Major: 1}, domain.StatusActive)
	if err == nil {
		t.Fatal("uma entrada mcp_server SEM manifesto nao pode chegar a active")
	}
	if !errors.Is(err, ErrManifestDigestRequired) {
		t.Errorf("err = %v, quer ErrManifestDigestRequired", err)
	}
}

// TestAOS334_AFormaDoDigestEVerificada fecha o achado (1b). A primeira versão verificava só a
// PRESENÇA: `ManifestDigest: "x"` passava, e dois servidores sem nada em comum voltavam a
// partilhar o digest de contrato — o digest-constante-da-classe que o AOS-320 existe para
// eliminar, reaberto por uma constante à escolha de quem publica.
func TestAOS334_AFormaDoDigestEVerificada(t *testing.T) {
	t.Parallel()
	maus := []string{"x", "nao-e-hash", "sha256:curto", "sha256:" + strings.Repeat("z", 64), strings.Repeat("a", 64)}
	for _, md := range maus {
		reg := novoRegistryDeTeste(t)
		_, err := reg.Publish(context.Background(), aos334Req(domain.KindMCPServer,
			domain.Contract{Egress: domain.EgressInternal, ManifestDigest: md}))
		if !errors.Is(err, ErrManifestDigestRequired) {
			t.Errorf("digest %q devia ser recusado por FORMA: %v", md, err)
		}
	}
	// CONTROLO: um digest bem-formado continua a publicar. Sem isto, uma verificação que
	// recusasse tudo passaria os casos acima e partiria toda a publicação de mcp_server.
	reg := novoRegistryDeTeste(t)
	if _, err := reg.Publish(context.Background(), aos334Req(domain.KindMCPServer,
		domain.Contract{Egress: domain.EgressInternal, ManifestDigest: digestDeTeste("bom")})); err != nil {
		t.Errorf("um digest bem-formado tem de publicar: %v", err)
	}
}
