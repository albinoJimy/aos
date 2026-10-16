package routeentry

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"slices"
	"strings"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// EntryFormat é a versão de formato de um registo de entrada. Um registo com outra é recusado
// ([CauseUnknownFormat]): um formato novo traz um leitor novo, e não um leitor que adivinha.
const EntryFormat = "aos-registo-de-entrada-de-perfil/1"

// signingDomain abre a mensagem assinada. É o que impede que uma assinatura feita para outra
// coisa — a allowlist, um mandato, uma aprovação — sirva de assinatura de um registo de entrada,
// mesmo que alguém use a mesma chave para as duas.
const signingDomain = "aos/registo-de-entrada-de-perfil/v1\n"

// Os nomes dos membros de um registo de entrada. Conjunto FECHADO, e todos obrigatórios.
const (
	fieldFormat        = "formato"
	fieldBoard         = "board"
	fieldRegion        = "regiao"
	fieldProfile       = "perfil"
	fieldProfileDigest = "digest_do_perfil"
	fieldReportDigest  = "digest_do_relatorio"
	fieldProxyDigest   = "digest_da_imagem_do_proxy"
	fieldInitialShare  = "fatia_inicial_percentagem"
	// fieldUse é o ÚNICO membro opcional (AOS-530, ADR-042 §2.3): sem ele o registo é de uso
	// [UseRun], e os bytes, o digest e a assinatura de um registo de antes não mudam.
	fieldUse = "uso"
)

// entryFields são os membros OBRIGATÓRIOS de um registo de entrada.
var entryFields = []string{fieldFormat, fieldBoard, fieldRegion, fieldProfile, fieldProfileDigest, fieldReportDigest, fieldProxyDigest, fieldInitialShare}

// allEntryFields são todos os membros que um registo pode ter: os obrigatórios e [fieldUse].
var allEntryFields = append(append([]string(nil), entryFields...), fieldUse)

// Os usos de um registo de entrada (AOS-530, ADR-042 §2.3 — emenda ao ADR-041 §2.1). Vocabulário
// FECHADO: outro valor recusa ([CauseBadUse]).
const (
	// UseRun é o uso de sempre, e a omissão: o perfil serve turnos de runs. Um registo de uso run
	// cujo alias não é o do titular é um candidato (ADR-041 §2.1).
	UseRun = "run"
	// UseTool é o perfil de um alias que só uma tool chama (a `ocr_read`, ADR-042). NUNCA é
	// candidato: não conta como não-titular, não ganha adaptador de run, não entra no canary nem
	// no disjuntor, e um run não se fixa nele. A fatia tem de ser 0 ([CauseToolWithShare]), e o
	// relatório citado é da experiência `ocr` ([CheckReport]).
	UseTool = "tool"
)

// Entry é um registo de entrada lido. Sair de [ParseEntry] não quer dizer que o nó o aceite: a
// assinatura, o ambiente, o relatório e a allowlist conferem-se em [LoadDir].
type Entry struct {
	// Label é o rótulo do registo: o nome do ficheiro sem o sufixo. Vazio fora de [LoadDir].
	Label string
	// Board e Region são o board e a região do nó a que o registo se destina.
	Board  string
	Region string
	// Profile é o perfil da rota, já passado pela leitura fechada do gateway.
	Profile modelgateway.RouteProfile
	// ProfileDigest é o digest do perfil ([modelgateway.RouteProfile.Digest]), igual ao que o
	// registo declara.
	ProfileDigest string
	// EntryDigest é o sha256 dos bytes exactos do registo — o que a assinatura cobre.
	EntryDigest string
	// ReportDigest é o digest do relatório de qualificação que o registo cita.
	ReportDigest string
	// ProxyImageDigest é o digest da imagem do proxy que o registo cita.
	ProxyImageDigest string
	// InitialSharePercent é a fatia inicial do canary, de 0 a 100. Quem a usa é o AOS-520; aqui é
	// só lida, validada e mostrada. Num registo de uso [UseTool] é sempre 0.
	InitialSharePercent int
	// Use é o uso do registo: [UseRun] (também quando o registo não traz o membro) ou [UseTool].
	Use string
}

// ForTool diz se o registo é de uso [UseTool]: um perfil que só uma tool chama, e que nunca é
// candidato a servir runs.
func (e Entry) ForTool() bool { return e.Use == UseTool }

// EntryDigest devolve o digest de um registo de entrada: `sha256:` sobre os bytes EXACTOS do
// ficheiro. Não há forma canónica: o que se assina é o ficheiro, e mudar-lhe um espaço é mudar o
// registo.
func EntryDigest(raw []byte) string { return sha256Of(raw) }

// SigningMessage devolve a mensagem que se assina para um registo: o prefixo de domínio seguido
// do digest do registo. É a ÚNICA escrita dela — serve quem assina ([Sign]) e quem verifica.
func SigningMessage(raw []byte) []byte {
	return []byte(signingDomain + EntryDigest(raw))
}

// Sign assina um registo de entrada e devolve a assinatura em base64, como vai no ficheiro
// `.registo.sig`. Não confere o registo: quem assina confere antes ([ParseEntry], [CheckReport]).
func Sign(raw []byte, priv ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, SigningMessage(raw)))
}

// VerifySignature confere a assinatura (o conteúdo do ficheiro `.registo.sig`) de um registo
// contra a chave pública do assinante. Devolve false para uma assinatura que não é base64, que
// não tem o tamanho de uma assinatura ed25519, ou que não verifica.
func VerifySignature(raw, signature []byte, pub ed25519.PublicKey) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	// Um BOM UTF-8 e espaço à volta toleram-se: o ficheiro é texto, escrito num posto Windows.
	text := strings.TrimSpace(string(bytes.TrimPrefix(signature, []byte{0xEF, 0xBB, 0xBF})))
	sig, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, SigningMessage(raw), sig)
}

// ParseEntry lê um registo de entrada pela leitura fechada, e confere o que se confere só com ele:
// a versão de formato, a fatia, o perfil, o nome pedido, o digest do perfil e a classe de estado.
// Devolve um [*Refusal] com a causa; nunca repete conteúdo do registo.
//
// A ORDEM das conferências é fixa, e é a das causas em [Causes].
//
// O membro `uso` é opcional (AOS-530): ausente, o registo é de uso [UseRun] e lê-se exactamente
// como antes — o digest e a assinatura são sobre os bytes do ficheiro, e um ficheiro sem o membro
// é o mesmo ficheiro de sempre. Presente, entra nos bytes e por isso na assinatura.
func ParseEntry(raw []byte) (Entry, error) {
	members, ok := strictObject(raw)
	if !ok {
		return Entry{}, refuse(CauseEntryUnreadable, "")
	}
	// A versão de formato lê-se ANTES de se conferir o conjunto de nomes: um registo de um
	// formato futuro, com membros que este leitor não conhece, diz «formato desconhecido» e não
	// «ilegível».
	if format, isText := textOf(members[fieldFormat]); !isText {
		return Entry{}, refuse(CauseEntryUnreadable, "")
	} else if format != EntryFormat {
		return Entry{}, refuse(CauseUnknownFormat, "")
	}
	if !onlyKeys(members, allEntryFields) {
		return Entry{}, refuse(CauseEntryUnreadable, "")
	}
	for _, name := range entryFields {
		if _, has := members[name]; !has {
			return Entry{}, refuse(CauseEntryUnreadable, "")
		}
	}
	e := Entry{EntryDigest: EntryDigest(raw), Use: UseRun}
	texts := map[string]*string{
		fieldBoard: &e.Board, fieldRegion: &e.Region,
		fieldProfileDigest: &e.ProfileDigest, fieldReportDigest: &e.ReportDigest, fieldProxyDigest: &e.ProxyImageDigest,
	}
	for name, dest := range texts {
		v, isText := textOf(members[name])
		if !isText || v == "" {
			return Entry{}, refuse(CauseEntryUnreadable, "")
		}
		*dest = v
	}
	for _, d := range []string{e.ProfileDigest, e.ReportDigest, e.ProxyImageDigest} {
		if !validDigest(d) {
			return Entry{}, refuse(CauseEntryUnreadable, "")
		}
	}
	share, isInt := intOf(members[fieldInitialShare])
	if !isInt {
		return Entry{}, refuse(CauseEntryUnreadable, "")
	}
	if share < 0 || share > 100 {
		return Entry{}, refuse(CauseShareOutOfRange, "")
	}
	e.InitialSharePercent = share
	if raw, has := members[fieldUse]; has {
		use, isText := textOf(raw)
		if !isText {
			return Entry{}, refuse(CauseEntryUnreadable, "")
		}
		if use != UseRun && use != UseTool {
			return Entry{}, refuse(CauseBadUse, "")
		}
		e.Use = use
	}
	// UM REGISTO DE USO TOOL NÃO TEM CANARY: a fatia é de runs, e nenhum run é servido por ele. Uma
	// fatia maior que zero diria o contrário do uso — recusa-se em vez de a ignorar.
	if e.ForTool() && e.InitialSharePercent > 0 {
		return Entry{}, refuse(CauseToolWithShare, "")
	}

	// O perfil passa pela leitura fechada do gateway, a mesma dos perfis candidatos: os campos
	// que ela lê, e nenhum outro. Não há por onde um perfil diga outra coisa que não seja um
	// perfil — a allowlist, a soberania, o veredicto do kernel e a mediação não são campos dele.
	profile, err := modelgateway.ParseRouteProfile(members[fieldProfile])
	if err != nil {
		return Entry{}, refuse(CauseBadProfile, "")
	}
	if !validLabel(profile.Requested) {
		return Entry{}, refuse(CauseBadRouteName, "")
	}
	if profile.Digest() != e.ProfileDigest {
		return Entry{}, refuse(CauseProfileDigestMismatch, "")
	}
	if ReturnsState(profile) {
		return Entry{}, refuse(CauseProfileReturnsState, "")
	}
	// O PERFIL DE UM REGISTO DE USO TOOL LÊ IMAGENS E NÃO PEDE EFEITOS (ADR-042 §2.3 e §2.4): declara
	// a capacidade `imagem` e não declara `tools` — o modelo que lê a imagem não pode pedir tools.
	if e.ForTool() && (!slices.Contains(profile.Capabilities, modelgateway.CapabilityImage) || slices.Contains(profile.Capabilities, modelgateway.CapabilityTools)) {
		return Entry{}, refuse(CauseToolProfileCapabilities, "")
	}
	e.Profile = profile
	return e, nil
}

// ReturnsState diz se um perfil devolve estado opaco ao provider, por qualquer dos três campos
// que o dizem: a classe de estado, o id das tool calls do provider, ou o sítio da devolução.
// Decisão D8 do dono (2026-10-11): até ao smoke da captura sobre JetStream, um perfil destes não
// entra por registo assinado.
func ReturnsState(p modelgateway.RouteProfile) bool {
	return p.StateReturn != modelgateway.StateReturnNever ||
		p.ToolCallID != modelgateway.ToolCallIDRuntime ||
		p.StateReturnAt != modelgateway.StateReturnAtOrigin
}
