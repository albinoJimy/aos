package mcp

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/aos-ref/platform/registry/digest"
)

// esquemaManifesto identifica a VERSÃO da forma canónica do manifesto. Entra no
// documento hasheado para que uma futura mudança da forma produza digests
// declaradamente distintos, em vez de colidir silenciosamente com a geração
// anterior (o mesmo papel que o prefixo "sha256:" faz para o algoritmo).
const esquemaManifesto = "aos.mcp.manifesto/v1"

// esquemaAncora identifica a versão da forma canónica da ÂNCORA (ver [DigestAncorado]).
const esquemaAncora = "aos.mcp.ancora/v1"

// formaCanonicaTool é a projecção CANÓNICA de uma tool anunciada. Todos os campos
// são SEMPRE emitidos (sem omitempty): a FORMA do documento não pode depender dos
// valores, ou um campo ausente e um campo vazio colidiriam.
//
// InputSchema é o schema JÁ SANITIZADO por [sanitizeSchema] — EXACTAMENTE a cópia
// que atravessa para o control-plane na entrada kind=tool. O digest cobre o que o
// AOS passa a poder invocar, não a cópia integral que fica em quarentena de taint.
type formaCanonicaTool struct {
	Descricao   string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Nome        string          `json:"name"`
}

// formaCanonicaResource é a projecção CANÓNICA de um resource anunciado.
type formaCanonicaResource struct {
	Descricao string `json:"description"`
	MimeType  string `json:"mime_type"`
	Nome      string `json:"name"`
	URI       string `json:"uri"`
}

// formaCanonicaManifesto é a forma canónica do manifesto de capacidades — o
// documento sobre o qual [digestManifesto] calcula o SHA-256. É CONSTRUÍDA
// EXPLICITAMENTE (nunca um json.Marshal de [CapabilityManifest] em bruto): a
// serialização do digest é um contrato de supply-chain e não pode mudar por
// acidente quando alguém acrescentar um campo ao tipo do protocolo.
type formaCanonicaManifesto struct {
	Esquema string `json:"schema"`
	// ProtocolVersion — INCLUÍDA. É auto-declarada pelo servidor, mas governa a
	// SEMÂNTICA DE WIRE de todas as chamadas seguintes (como os resultados são
	// enquadrados): um servidor que baixe silenciosamente a versão negociada muda
	// aquilo com que o host está a falar. O churn é limitado — o versionamento MCP
	// é uma enumeração pequena e lenta (datas), nunca um valor por-arranque.
	ProtocolVersion string `json:"protocol_version"`
	// Resources — INCLUÍDOS (URI/nome/descrição/mime-type). São, com as tools, a
	// superfície de capacidade: o que o host pode passar a LER.
	Resources []formaCanonicaResource `json:"resources"`
	// ResourcesIndisponiveis — INCLUÍDO. É um facto OBSERVADO PELO HOST (o
	// resources/list devolveu erro), não uma declaração do servidor. Incluí-lo
	// impede que uma descoberta INCOMPLETA seja pinada como equivalente a uma
	// descoberta completa que viu zero resources.
	ResourcesIndisponiveis bool `json:"resources_unavailable"`
	// Tools — INCLUÍDAS (nome + descrição + schema sanitizado).
	Tools []formaCanonicaTool `json:"tools"`
	// NOTA — ServerInfo é DELIBERADAMENTE EXCLUÍDO. O próprio protocol.go declara-o
	// informativo: «a identidade de confiança/versionamento canónica vive no REG, não
	// no que o servidor afirma de si». É um rótulo livre, autoria do adversário, que
	// ele copia trivialmente numa substituição — logo não autentica nada — mas sem
	// limite de churn (build ids, timestamps), o que tornaria o pin frágil a
	// re-rotulagens que não mudam capacidade nenhuma. A identidade entra no digest
	// pela via NÃO-FORJÁVEL: a âncora local de transporte/endpoint ([DigestAncorado])
	// somada ao par (id, version) pinado no REG.
}

// formaCanonicaAncora liga o digest do manifesto à ÂNCORA LOCAL de identidade: o
// transporte e o endpoint/comando, que vêm da [ConnectionInfo] configurada pelo
// operador e NÃO são forjáveis pelo servidor. É o que faz com que substituir o
// binário ou o endpoint por trás de um par (id, version) inalterado mude o digest
// da entrada — o defeito que AOS-320 fecha.
type formaCanonicaAncora struct {
	Esquema        string `json:"schema"`
	Endpoint       string `json:"endpoint"`
	ManifestDigest string `json:"manifest_digest"`
	Transporte     string `json:"transport"`
}

// digestManifesto calcula o digest do MANIFESTO DE CAPACIDADES (AOS-320) com
// [digest.DigestJSON] sobre a forma canónica construída explicitamente acima.
//
// DETERMINISMO: as tools são ordenadas por nome e os resources por URI (a ordem em
// que o servidor as enumera não é semântica); a canonicalização de DigestJSON
// ordena as chaves de todos os objectos e normaliza whitespace. O mesmo manifesto
// produz sempre o mesmo digest, sem relógio nem aleatoriedade.
//
// FAIL-CLOSED sobre NOMES REPETIDOS: [CapabilityManifest.Tools] é um ARRAY, e a
// deduplicação de chaves de [digest.CanonicalJSON] só actua DENTRO de objectos —
// dois elementos com o mesmo "name" não seriam recusados por ela. Duas tools com o
// mesmo nome tornam a ordenação ambígua (dois documentos canónicos possíveis para o
// mesmo manifesto) e são, no REG, a MESMA chave (id = serverID+"/"+nome): é uma
// colisão semântica, exactamente o vector que a recusa de chaves duplicadas existe
// para eliminar. Devolve [ErrCapacidadeDuplicada]. O mesmo para URIs de resource.
func digestManifesto(m CapabilityManifest) (string, error) {
	tools := make([]formaCanonicaTool, 0, len(m.Tools))
	vistas := make(map[string]struct{}, len(m.Tools))
	for _, t := range m.Tools {
		if _, dup := vistas[t.Name]; dup {
			return "", fmt.Errorf("%w: tool %q anunciada mais do que uma vez", ErrCapacidadeDuplicada, t.Name)
		}
		vistas[t.Name] = struct{}{}
		tools = append(tools, formaCanonicaTool{
			Descricao:   t.Description,
			InputSchema: sanitizeSchema(t.InputSchema),
			Nome:        t.Name,
		})
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Nome < tools[j].Nome })

	resources := make([]formaCanonicaResource, 0, len(m.Resources))
	uris := make(map[string]struct{}, len(m.Resources))
	for _, r := range m.Resources {
		if _, dup := uris[r.URI]; dup {
			return "", fmt.Errorf("%w: resource %q anunciado mais do que uma vez", ErrCapacidadeDuplicada, r.URI)
		}
		uris[r.URI] = struct{}{}
		resources = append(resources, formaCanonicaResource{
			Descricao: r.Description,
			MimeType:  r.MimeType,
			Nome:      r.Name,
			URI:       r.URI,
		})
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].URI < resources[j].URI })

	doc, err := json.Marshal(formaCanonicaManifesto{
		Esquema:                esquemaManifesto,
		ProtocolVersion:        m.ProtocolVersion,
		Resources:              resources,
		ResourcesIndisponiveis: m.ResourcesUnavailable,
		Tools:                  tools,
	})
	if err != nil {
		return "", fmt.Errorf("%w: forma canonica do manifesto: %v", ErrProtocol, err)
	}
	return digest.DigestJSON(doc)
}

// digestAncorado liga o digest do manifesto à âncora local (transporte + endpoint) e
// devolve o valor que a entrada kind=mcp_server transporta em
// Contract.ManifestDigest. É o digest que o [registry.Registry] recomputa em
// verifyDigest — logo a âncora fica DENTRO da assinatura sobre (id, version, digest).
//
// Um endpoint vazio é um valor legítimo (o operador pode não o declarar) e produz um
// digest estável e distinto de qualquer endpoint declarado — nunca um erro silencioso.
// EXPORTADA DE PROPÓSITO (revisão adversarial de AOS-320): a suite de supply-chain
// reconstruía uma forma ancorada PRÓPRIA — um `map[string]string{"endpoint","manifest"}`
// que não tinha relação nenhuma com esta. O vector ficava a provar «duas strings opacas
// diferentes dão digests diferentes», que o teste de golden já provava, e teria continuado
// VERDE se esta função deixasse cair o transporte e o endpoint. Um vector que não toca no
// código sob teste é pior do que nenhum: entra no gate e compra confiança que não sustenta.
func DigestAncorado(digestDoManifesto, endpoint string, kind TransportKind) (string, error) {
	doc, err := json.Marshal(formaCanonicaAncora{
		Esquema:        esquemaAncora,
		Endpoint:       endpoint,
		ManifestDigest: digestDoManifesto,
		Transporte:     string(kind),
	})
	if err != nil {
		return "", fmt.Errorf("%w: forma canonica da ancora: %v", ErrProtocol, err)
	}
	return digest.DigestJSON(doc)
}
