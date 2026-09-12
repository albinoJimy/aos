package digest

import (
	"encoding/json"
	"strings"
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
	// publicavel distingue «forma que o REG ainda ACEITA publicar» de «forma HISTÓRICA do
	// event-log, que tem de continuar a resolver mas já não pode nascer» (AOS-334).
	//
	// A distinção não existia, e a ausência dela é que era o defeito: um golden que congela um
	// valor sem dizer o que ele representa lê-se como «esta forma é válida». O digest do
	// `mcp_server` sem manifesto TEM de continuar estável — as entradas já no log resolvem por
	// ele — e ao mesmo tempo essa forma deixou de ser publicável. As duas coisas são
	// verdadeiras, e o campo é o que as separa.
	publicavel bool
}

func casosGolden() []casoGolden {
	return []casoGolden{
		{
			nome:        "tool_minimo",
			publicavel:  true,
			kind:        domain.KindTool,
			contrato:    domain.Contract{Egress: domain.EgressNone},
			sha256:      "sha256:598d8a70b117520fccd43f9abe0dbeef4f7c533b15718a19c631854599fcd7b4",
			placeholder: "placeholder-fnv1a:6c29a28cb3b881f9",
		},
		{
			nome:       "tool_completo",
			publicavel: true,
			kind:       domain.KindTool,
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
			nome:       "skill_completo",
			publicavel: true,
			kind:       domain.KindSkill,
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
			// A FORMA PUBLICÁVEL de um mcp_server: COM manifesto. Não existia caso golden
			// nenhum para ela — o AOS-320 introduziu a forma e não lhe congelou o digest —,
			// e sem isto o campo `publicavel` abaixo não carregava bit nenhum que o `kind`
			// já não carregasse: a asserção que o usava era uma tautologia sobre a fixture.
			nome:        "mcp_server_com_manifesto",
			publicavel:  true,
			kind:        domain.KindMCPServer,
			contrato:    domain.Contract{Egress: domain.EgressInternal, ManifestDigest: "sha256:" + strings.Repeat("ab", 32)},
			sha256:      "",
			placeholder: "",
		},
		{
			// FORMA HISTÓRICA, NÃO PUBLICÁVEL. É o que o REG publicava antes de AOS-320.
			// Tem de continuar a hashear para o MESMO valor — senão as entradas mcp_server
			// já no log deixavam de resolver (ErrDigestMismatch) —, e desde AOS-334 já não
			// pode ser publicada: `TestAOS334_MCPServerSemManifestoERecusado` recusa-a.
			//
			// O log é append-only; reescrever a leitura do histórico seria pior do que a
			// lacuna. O que se fecha é a publicação NOVA.
			nome:        "mcp_server_sem_manifesto",
			publicavel:  false,
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
			// A guarda continua, mas ESPECÍFICA do que ela protege: os valores congelados
			// abaixo são das entradas SEM manifesto. O caso COM manifesto entrou na tabela
			// para dar bit ao campo `publicavel`, e não tem valor congelado — é coberto pelo
			// TestGolden_FormaPublicavelDiscrimina.
			if c.sha256 == "" {
				return
			}
			if c.contrato.ManifestDigest != "" {
				t.Fatalf("fixture invalida: %s tem ManifestDigest e valor congelado — o golden e' das entradas SEM manifesto", c.nome)
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

// TestGolden_FormaHistoricaNaoEPublicavel torna o campo `publicavel` LOAD-BEARING.
//
// Sem esta asserção o campo seria decoração: uma anotação que ninguém lê descreve a intenção de
// quem a escreveu, não o estado do sistema — que é exactamente o defeito que o AOS-329 persegue
// noutro eixo.
//
// A regra fixada aqui é a do AOS-334: um `mcp_server` sem `ManifestDigest` não é publicável. Se
// alguém acrescentar um caso golden dessa forma marcado como publicável, isto avermelha.
//
// LIMITE DECLARADO: isto fixa a COERÊNCIA DA FIXTURE, não o comportamento. A prova de que o
// `Publish` recusa de facto é `TestAOS334_MCPServerSemManifestoERecusado`, no pacote `registry` —
// aqui não é alcançável sem inverter a dependência (o `registry` importa o `digest`).
func TestGolden_FormaHistoricaNaoEPublicavel(t *testing.T) {
	t.Parallel()
	var historicas int
	for _, c := range casosGolden() {
		semManifesto := c.kind == domain.KindMCPServer && c.contrato.ManifestDigest == ""
		if semManifesto {
			historicas++
		}
		if c.publicavel == semManifesto {
			t.Errorf("%s: publicavel=%v mas mcp_server-sem-manifesto=%v — a fixture contradiz a regra do AOS-334", c.nome, c.publicavel, semManifesto)
		}
	}
	// NÃO-VACUOSIDADE: se o caso histórico desaparecer da tabela, o teste acima passa a não
	// asserir nada. O golden existe precisamente para essa forma continuar a resolver.
	if historicas != 1 {
		t.Errorf("casos historicos = %d, quer 1 — o golden do mcp_server pre-AOS-320 tem de continuar na tabela", historicas)
	}
}

// TestGolden_FormaPublicavelDiscrimina fecha o que a asserção anterior NÃO provava.
//
// O `TestGolden_FormaHistoricaNaoEPublicavel` reduzia-se, sobre a tabela admissível, a
// `publicavel == (kind != mcp_server)` — uma tautologia sobre booleanos postos à mão, porque a
// guarda da fixture só deixava entrar casos sem manifesto. Medido em revisão adversarial:
// remover a validação de produção deixava-o verde.
//
// O que é preciso provar é o que o campo `ManifestDigest` existe para fazer: DISCRIMINAR. Dois
// `mcp_server` com manifestos diferentes têm de ter digests de contrato diferentes, e um
// `mcp_server` com manifesto tem de diferir da forma histórica sem ele — senão o campo não
// ancora nada e o AOS-320 não fechou o que diz ter fechado.
func TestGolden_FormaPublicavelDiscrimina(t *testing.T) {
	t.Parallel()
	base := domain.Contract{Egress: domain.EgressInternal}
	comA := domain.Contract{Egress: domain.EgressInternal, ManifestDigest: "sha256:" + strings.Repeat("ab", 32)}
	comB := domain.Contract{Egress: domain.EgressInternal, ManifestDigest: "sha256:" + strings.Repeat("cd", 32)}

	d := SHA256Digester{}
	dSem, dA, dB := d.Digest(domain.KindMCPServer, base), d.Digest(domain.KindMCPServer, comA), d.Digest(domain.KindMCPServer, comB)

	if dA == dB {
		t.Errorf("dois manifestos DIFERENTES produzem o mesmo digest de contrato (%s) — o campo nao discrimina", dA)
	}
	if dA == dSem {
		t.Errorf("com e sem manifesto produzem o mesmo digest (%s) — o campo nao entra no hash", dA)
	}
	// CONTROLO: o mesmo manifesto tem de produzir o mesmo digest, senão a discriminação acima
	// seria só não-determinismo.
	if got := d.Digest(domain.KindMCPServer, comA); got != dA {
		t.Errorf("o digest nao e determinista: %s != %s", got, dA)
	}
}
