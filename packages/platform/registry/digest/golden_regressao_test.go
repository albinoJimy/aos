package digest

import (
	"encoding/json"
	"testing"

	"github.com/aos-ref/platform/registry/domain"
)

// ---------------------------------------------------------------------------
// PROVA DE NÃO-REGRESSÃO DOS DIGESTS (AOS-320)
//
// AOS-320 acrescentou Contract.ManifestDigest e passou a escrevê-lo na
// canonicalização. O campo é escrito por ÚLTIMO e SÓ quando não-vazio,
// precisamente para que o digest de TODAS as entradas sem manifesto — tool,
// skill, e um mcp_server anterior a AOS-320 — fique BYTE-A-BYTE inalterado.
//
// Os valores abaixo foram CAPTURADOS DO CÓDIGO ANTERIOR À ALTERAÇÃO (a mesma
// função corrida antes de o campo existir) e ficam aqui congelados como GOLDEN.
// Se alguém tocar na ordem dos campos, no enquadramento por comprimento, na
// canonicalização de schemas ou na ordenação de scopes, este teste fica vermelho
// e o pin de todo o catálogo já publicado deixa de estar em silêncio.
//
// Determinismo: as fixtures são literais; a canonicalização é pura (sem relógio,
// sem aleatoriedade, sem UUID).
// ---------------------------------------------------------------------------

// casoGolden é um par (kind, contract) com os dois digests esperados: o SHA-256 de
// AOS-047 e o placeholder FNV-1a de AOS-045 (as DUAS gerações de hashing têm de
// permanecer estáveis, porque ambas serializam a mesma ordem de campos e têm de se
// manter sincronizadas).
type casoGolden struct {
	nome        string
	kind        domain.ArtifactKind
	contrato    domain.Contract
	sha256      string
	placeholder string
}

func casosGolden() []casoGolden {
	return []casoGolden{
		{
			nome:        "tool_minimo",
			kind:        domain.KindTool,
			contrato:    domain.Contract{Egress: domain.EgressNone},
			sha256:      "sha256:598d8a70b117520fccd43f9abe0dbeef4f7c533b15718a19c631854599fcd7b4",
			placeholder: "placeholder-fnv1a:6c29a28cb3b881f9",
		},
		{
			nome: "tool_completo",
			kind: domain.KindTool,
			contrato: domain.Contract{
				InputSchema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
				OutputSchema:     json.RawMessage(`{"type":"string"}`),
				CredentialScopes: []string{"vault:db.read", "vault:http"},
				Egress:           domain.EgressExternal,
			},
			sha256:      "sha256:6e0a5ab8e21172645cba7fdbf5709beba3f8f9e83beb1bb9f8f6a680f93cc6d9",
			placeholder: "placeholder-fnv1a:cfc8c5613627bb20",
		},
		{
			nome: "skill_completo",
			kind: domain.KindSkill,
			contrato: domain.Contract{
				InputSchema:      json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
				OutputSchema:     json.RawMessage(`{"type":"object"}`),
				CredentialScopes: []string{"vault:kb"},
				Egress:           domain.EgressInternal,
			},
			sha256:      "sha256:fbfecd6d3a96102b1a254bf1c1ab5128c1261fe2394030c638b4149d40dd3afa",
			placeholder: "placeholder-fnv1a:bcabb6c2c0331bba",
		},
		{
			// Um mcp_server SEM ManifestDigest — a forma que o REG publicava antes de
			// AOS-320. Tem de continuar a hashear para o MESMO valor, ou as entradas
			// mcp_server já no log deixariam de resolver (ErrDigestMismatch).
			nome:        "mcp_server_sem_manifesto",
			kind:        domain.KindMCPServer,
			contrato:    domain.Contract{Egress: domain.EgressInternal},
			sha256:      "sha256:3924dad94409833c99f7d2fbb3f47f44a8d995f88e57cc3551b0f08d9f1e4074",
			placeholder: "placeholder-fnv1a:f6e3d2282a0ce0a4",
		},
	}
}

// TestGoldenDigests_ToolSkill_NaoRegridem fixa os digests das entradas SEM
// manifesto nos valores exactos de antes de AOS-320.
func TestGoldenDigests_ToolSkill_NaoRegridem(t *testing.T) {
	t.Parallel()
	for _, c := range casosGolden() {
		c := c
		t.Run(c.nome, func(t *testing.T) {
			t.Parallel()
			if c.contrato.ManifestDigest != "" {
				t.Fatalf("fixture invalida: %s tem ManifestDigest — o golden e' das entradas SEM manifesto", c.nome)
			}
			if got := (SHA256Digester{}).Digest(c.kind, c.contrato); got != c.sha256 {
				t.Fatalf("REGRESSAO do digest SHA-256 de %s:\n got  %s\n want %s\n(o digest das entradas ja' publicadas mudou — o pin de todo o catalogo quebra)", c.nome, got, c.sha256)
			}
			if got := (domain.PlaceholderDigester{}).Digest(c.kind, c.contrato); got != c.placeholder {
				t.Fatalf("REGRESSAO do digest placeholder de %s:\n got  %s\n want %s", c.nome, got, c.placeholder)
			}
		})
	}
}

// TestManifestDigest_VazioNaoEntraNaCanonicalizacao é a prova ESTRUTURAL (e não só
// por valor) de que o campo novo é invisível quando vazio: os bytes canónicos de um
// contrato sem manifesto são idênticos aos de um contrato onde o campo foi
// explicitamente posto a "". É o que garante que o golden acima não é uma
// coincidência de valores.
func TestManifestDigest_VazioNaoEntraNaCanonicalizacao(t *testing.T) {
	t.Parallel()
	base := domain.Contract{
		InputSchema:      json.RawMessage(`{"a":1}`),
		CredentialScopes: []string{"vault:x"},
		Egress:           domain.EgressInternal,
	}
	comVazio := base
	comVazio.ManifestDigest = ""

	if string(canonicalContract(domain.KindTool, base)) != string(canonicalContract(domain.KindTool, comVazio)) {
		t.Fatal("um ManifestDigest vazio nao pode alterar os bytes canonicos")
	}
	if string(domain.Canonicalize(domain.KindTool, base)) != string(domain.Canonicalize(domain.KindTool, comVazio)) {
		t.Fatal("domain.Canonicalize divergiu de digest.canonicalContract no caso vazio")
	}

	// E, simetricamente: NÃO-vazio TEM de mudar os bytes (senão o campo era inerte).
	comValor := base
	comValor.ManifestDigest = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if string(canonicalContract(domain.KindTool, base)) == string(canonicalContract(domain.KindTool, comValor)) {
		t.Fatal("um ManifestDigest nao-vazio TEM de alterar os bytes canonicos")
	}
	if string(domain.Canonicalize(domain.KindTool, base)) == string(domain.Canonicalize(domain.KindTool, comValor)) {
		t.Fatal("domain.Canonicalize ignora o ManifestDigest — as duas geracoes dessincronizaram")
	}
}

// TestManifestDigest_DiscriminaEntradasMCPServer prova, ao nível do digester, o
// coração de AOS-320: dois mcp_server com a MESMA classe de egress (o único campo
// que o contrato de um servidor MCP tinha antes) e manifestos diferentes deixam de
// colidir. O primeiro par documenta o defeito antigo; o segundo, a correcção.
func TestManifestDigest_DiscriminaEntradasMCPServer(t *testing.T) {
	t.Parallel()
	d := SHA256Digester{}

	// CONTROLO — a forma PRÉ-AOS-320 (só egress) colidia: qualquer par de servidores
	// da mesma classe tinha exactamente o mesmo digest.
	antigoA := domain.Contract{Egress: domain.EgressInternal}
	antigoB := domain.Contract{Egress: domain.EgressInternal}
	if d.Digest(domain.KindMCPServer, antigoA) != d.Digest(domain.KindMCPServer, antigoB) {
		t.Fatal("controlo invalido: a forma pre-AOS-320 devia colidir (era o defeito)")
	}

	// CORRECÇÃO — com manifestos distintos, os digests divergem.
	novoA := domain.Contract{Egress: domain.EgressInternal, ManifestDigest: "sha256:aaaa"}
	novoB := domain.Contract{Egress: domain.EgressInternal, ManifestDigest: "sha256:bbbb"}
	if d.Digest(domain.KindMCPServer, novoA) == d.Digest(domain.KindMCPServer, novoB) {
		t.Fatal("dois manifestos distintos, mesma egress: os digests TEM de divergir")
	}
	// Determinismo: o mesmo manifesto reproduz o mesmo digest.
	if d.Digest(domain.KindMCPServer, novoA) != d.Digest(domain.KindMCPServer, domain.Contract{Egress: domain.EgressInternal, ManifestDigest: "sha256:aaaa"}) {
		t.Fatal("o mesmo (kind, contract) tem de reproduzir o mesmo digest")
	}
}

// TestContractClone_PreservaManifestDigest fecha o buraco silencioso: se o clone
// profundo perdesse o campo, verifyDigest recomputaria sobre um contrato AMPUTADO e
// TODAS as entradas mcp_server passariam a falhar com ErrDigestMismatch.
func TestContractClone_PreservaManifestDigest(t *testing.T) {
	t.Parallel()
	e := domain.Entry{
		ID: "mcp.fs", Version: domain.Version{Major: 1}, Kind: domain.KindMCPServer,
		Digest:     "sha256:x",
		Contract:   domain.Contract{Egress: domain.EgressInternal, ManifestDigest: "sha256:manifesto"},
		Provenance: domain.Provenance{Trust: domain.TrustFirstSeen},
		Status:     domain.StatusStaging,
	}
	if got := e.Clone().Contract.ManifestDigest; got != "sha256:manifesto" {
		t.Fatalf("Clone perdeu o ManifestDigest: %q", got)
	}
	// E o digest recomputado sobre o clone é o mesmo do original.
	d := SHA256Digester{}
	if d.Digest(e.Kind, e.Contract) != d.Digest(e.Clone().Kind, e.Clone().Contract) {
		t.Fatal("o digest do clone divergiu do original")
	}
}
