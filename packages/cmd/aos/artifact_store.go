package main

// O ARMAZÉM DE ARTEFACTOS DO RUN (AOS-548, ADR-043 §2.3) — `AOS_ARTIFACT_DIR`.
//
// O modelo do run grava ficheiros de texto — JSON, Markdown, CSV, texto, SVG — com a tool
// `artifact_write`, e quem tem leitura soberana do run descarrega-os por `GET /runs/{id}/artifacts`.
//
//   - Vazia (por omissão): desligado. A tool não existe e o nó é o de sempre.
//   - Definida: a pasta do volume do nó onde vivem os artefactos. Um ficheiro por artefacto, em
//     `<pasta>/<sha256 do run>/<sha256 do conteúdo>`, CIFRADO pelo cifrador por titular do nó — o
//     mesmo das capturas —, pelo que a DSAR e a retenção (crypto-shredding da KEK do titular) o
//     tornam ilegível. Nunca há bytes em claro no disco.
//
// PROVENIÊNCIA. Cada gravação sela no stream do run um `artifact.created` com o digest, o tipo, o
// tamanho, o nome, a tool, o passo e o titular. A rota de leitura só serve um digest que um evento
// do PRÓPRIO run declara: um ficheiro na pasta sem evento não é servido.
//
// TODO O ARTEFACTO É UNTRUSTED. Foi escrito pelo modelo. Os tipos são um vocabulário fechado, o SVG
// é recusado com qualquer forma de script ou ligação externa, e a entrega é sempre como anexo, com
// `nosniff` e CSP `sandbox`.
//
// RUN E PASSO NÃO VÊM DO MODELO. Uma reescrita do efeito (como a da `ocr_read`) põe na entrada da
// tool o run, o passo e o titular da tool call REAL.
//
// RESÍDUO DECLARADO: um titular apagado deixa os blobs cifrados no disco até a pasta do run ser
// apagada à mão; o tecto do volume (`AOS_ARTIFACT_MAX_BYTES`) recusa gravações quando se enche.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/taint"
	"github.com/aos-ref/substrate/eventstore"
)

// artifactWriteToolName é o nome da tool. É código do nó: o efeito não é configuração.
const artifactWriteToolName = "artifact_write"

// artifactWriteCapability é a capability da tool (AOS-549): é local no risco (só ela, exacta), e a
// política assinada tem a regra dela.
const artifactWriteCapability = referencemonitor.ArtifactWriteCapability

// EventTypeArtifactCreated é o facto durável de um artefacto gravado. Constante junto do emissor
// (disciplina do event-catalog, tecnica/13 §3.3).
const EventTypeArtifactCreated = "artifact.created"

// taintDoArtefacto é o rótulo que o evento de proveniência de um artefacto SELA (AOS-552).
//
// CONSTANTE DE COMPILAÇÃO, e não uma `var` com `taint.Untrusted.String()`: o valor é uma
// invariante, não um cálculo, e uma `var` deixava o guard-test a comparar a definição consigo
// mesma em vez de cruzar os dois vocabulários. [taint.StringUntrusted] é a forma textual que o
// kernel publica — a mesma que o `context.taint` da mediação e os rótulos do tail usam —, para que
// um leitor não tenha de traduzir entre dois vocabulários.
const taintDoArtefacto = taint.StringUntrusted

// artifactStepPrefix é o prefixo do `step_id` do envelope do evento: sem ele, o evento tinha a
// chave de idempotência da `tool.call.mediated` do mesmo passo e era engolido como duplicado.
const artifactStepPrefix = "artefacto-"

// artifactNHI é a identidade emissora do facto: um sub-papel nomeado do nó.
const artifactNHI = "nhi:aos-node/artifact"

// artifactInputSchema é o schema da entrada da tool DEPOIS da reescrita do efeito.
const artifactInputSchema = "aos-artifact-write/v1"

// ArtifactRefSchema é o schema da referência que a tool devolve ao modelo.
const ArtifactRefSchema = "aos-artifact-ref/v1"

// Tectos. Os de por run e do volume são a defesa contra um modelo que grava sem parar.
const (
	artifactMaxBytes        = 1 << 20  // por artefacto
	artifactMaxPorRun       = 16       // artefactos por run
	artifactMaxBytesPorRun  = 16 << 20 // bytes por run
	artifactMaxBytesOmissao = 1 << 30  // o volume, sem AOS_ARTIFACT_MAX_BYTES
	artifactMaxNome         = 64
)

// Os tipos de artefacto. Vocabulário FECHADO: o HTML fica de fora (ADR-043 §2.3).
var tiposDeArtefacto = map[string]bool{
	"application/json": true,
	"text/markdown":    true,
	"text/csv":         true,
	"text/plain":       true,
	"image/svg+xml":    true,
}

// Causas de recusa da tool. Vocabulário FECHADO.
const (
	artCausaEntrada  = "entrada_invalida"
	artCausaTipo     = "tipo_nao_aceite"
	artCausaConteudo = "conteudo_invalido"
	artCausaGrande   = "artefacto_acima_do_tecto"
	artCausaRun      = "tecto_do_run"
	artCausaVolume   = "volume_cheio"
	artCausaCifra    = "cifra_indisponivel"
	artCausaDisco    = "disco_indisponivel"
	artCausaRegisto  = "registo_indisponivel"
)

var (
	// ErrBadArtifactDir — AOS_ARTIFACT_DIR definida e a pasta não se abre.
	ErrBadArtifactDir = errors.New("aos: AOS_ARTIFACT_DIR invalida — tem de ser uma pasta existente e gravavel no volume do no")
	// ErrBadArtifactMaxBytes — AOS_ARTIFACT_MAX_BYTES fora de um inteiro positivo.
	ErrBadArtifactMaxBytes = errors.New("aos: AOS_ARTIFACT_MAX_BYTES invalida — um inteiro positivo de bytes")
	// ErrArtifactSemCifra — o armazém ligado sem o cifrador por titular: nunca bytes em claro.
	ErrArtifactSemCifra = errors.New("aos: AOS_ARTIFACT_DIR exige o cifrador por titular do no (os artefactos nunca ficam em claro no disco)")
)

// nomeDeArtefacto é o alfabeto do nome que vai no `Content-Disposition`.
var nomeDeArtefacto = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// cifradorDeArtefactos é o que o armazém usa do cifrador por titular do nó ([contentSealer]).
type cifradorDeArtefactos interface {
	SealContent(ctx context.Context, subject, streamID string, plaintext []byte) ([]byte, error)
	OpenContent(ctx context.Context, subject string, sealed []byte) ([]byte, error)
}

// armazemDeArtefactos é o armazém ligado.
type armazemDeArtefactos struct {
	root   *os.Root
	pasta  string
	maximo int64
	cifra  cifradorDeArtefactos
	// registar escreve o evento de proveniência (ligado no Bootstrap; nil ⇒ a tool recusa).
	registar func(ctx context.Context, runID string, p artefactoCriado) error
	// handles é o plano de entradas por handle (AOS-550; ligado no Bootstrap com AOS_HANDLE_DIR).
	// nil ⇒ desligado: nenhum handle é cunhado e o resultado da tool é o de sempre, byte a byte.
	// Ver handles.go e handle_store.go.
	handles *planoDeHandles

	mu    sync.Mutex // serializa as gravações (tectos por run e do volume)
	usado atomic.Int64

	gravados, recusados, servidos atomic.Int64
}

// artifactConfigFromEnv lê AOS_ARTIFACT_DIR e AOS_ARTIFACT_MAX_BYTES. Pasta vazia ⇒ "" (desligado).
func artifactConfigFromEnv() (string, int64, error) {
	pasta := strings.TrimSpace(os.Getenv("AOS_ARTIFACT_DIR"))
	maximo := int64(artifactMaxBytesOmissao)
	if raw := strings.TrimSpace(os.Getenv("AOS_ARTIFACT_MAX_BYTES")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			return "", 0, fmt.Errorf("%w (veio %q)", ErrBadArtifactMaxBytes, raw)
		}
		maximo = n
	}
	return pasta, maximo, nil
}

// abrirArmazemDeArtefactos abre a pasta, confinada, e soma o que lá está para o tecto do volume.
func abrirArmazemDeArtefactos(pasta string, maximo int64, cifra cifradorDeArtefactos) (*armazemDeArtefactos, error) {
	if pasta == "" {
		return nil, nil
	}
	if cifra == nil {
		return nil, ErrArtifactSemCifra
	}
	root, err := os.OpenRoot(pasta)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadArtifactDir, err)
	}
	if maximo <= 0 {
		maximo = artifactMaxBytesOmissao
	}
	a := &armazemDeArtefactos{root: root, pasta: pasta, maximo: maximo, cifra: cifra}
	var total int64
	_ = fs.WalkDir(root.FS(), ".", func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && !strings.HasSuffix(d.Name(), ".tmp") {
			if info, ierr := d.Info(); ierr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	a.usado.Store(total)
	return a, nil
}

// fechar liberta a pasta. Idempotente e nil-safe.
func (a *armazemDeArtefactos) fechar() {
	if a != nil && a.root != nil {
		_ = a.root.Close()
	}
}

// pastaDoRun é o nome da pasta de um run: o sha256 do run_id (o run_id tem caracteres que um nome
// de ficheiro não aceita em todos os sistemas).
func pastaDoRun(runID string) string {
	s := sha256.Sum256([]byte(runID))
	return hex.EncodeToString(s[:])
}

// digestHex devolve o sha256 do conteúdo em hexadecimal.
func digestHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// guardar cifra e grava um artefacto, com os tectos. Devolve o digest `sha256:<hex>`, ou a causa.
func (a *armazemDeArtefactos) guardar(ctx context.Context, subject, runID string, conteudo []byte) (string, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	hexd := digestHex(conteudo)
	dir := pastaDoRun(runID)
	nome := dir + "/" + hexd
	if _, err := a.root.Stat(nome); err == nil {
		return "sha256:" + hexd, "" // o mesmo conteúdo no mesmo run: já está
	}
	// Os tectos do run, pelo que está na pasta dele.
	n, bytesDoRun := 0, int64(0)
	if entradas, err := fs.ReadDir(a.root.FS(), dir); err == nil {
		for _, e := range entradas {
			if info, ierr := e.Info(); ierr == nil && e.Type().IsRegular() && !strings.HasSuffix(e.Name(), ".tmp") {
				n++
				bytesDoRun += info.Size()
			}
		}
	}
	if n >= artifactMaxPorRun || bytesDoRun+int64(len(conteudo)) > artifactMaxBytesPorRun {
		return "", artCausaRun
	}
	cifrado, err := a.cifra.SealContent(ctx, subject, runID, conteudo)
	if err != nil {
		return "", artCausaCifra
	}
	if a.usado.Load()+int64(len(cifrado)) > a.maximo {
		return "", artCausaVolume
	}
	if err := a.root.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", artCausaDisco
	}
	// Escrita atómica: um ficheiro temporário do mesmo run, depois o nome final.
	tmp := nome + ".tmp"
	f, err := a.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", artCausaDisco
	}
	if _, werr := f.Write(cifrado); werr != nil {
		_ = f.Close()
		_ = a.root.Remove(tmp)
		return "", artCausaDisco
	}
	if err := f.Close(); err != nil {
		_ = a.root.Remove(tmp)
		return "", artCausaDisco
	}
	if err := a.root.Rename(tmp, nome); err != nil {
		_ = a.root.Remove(tmp)
		return "", artCausaDisco
	}
	a.usado.Add(int64(len(cifrado)))
	return "sha256:" + hexd, ""
}

// abrir lê e decifra um artefacto, e confere que os bytes são os do digest.
func (a *armazemDeArtefactos) abrir(ctx context.Context, subject, runID, digest string) ([]byte, error) {
	hexd, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || len(hexd) != 64 {
		return nil, fs.ErrNotExist
	}
	f, err := a.root.Open(pastaDoRun(runID) + "/" + hexd)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cifrado, err := io.ReadAll(io.LimitReader(f, 2*artifactMaxBytes+4096))
	if err != nil {
		return nil, err
	}
	claro, err := a.cifra.OpenContent(ctx, subject, cifrado)
	if err != nil {
		return nil, err
	}
	if digestHex(claro) != hexd {
		return nil, errors.New("aos: o artefacto nao bate com o digest")
	}
	return claro, nil
}

// artefactoCriado é o corpo do evento [EventTypeArtifactCreated]. Sem o conteúdo.
//
// # A LINHAGEM E O TAINT (AOS-552, ADR-043 §2.3)
//
// O ADR-043 §2.3 exige que o evento de proveniência leve «os digests das entradas». Até ao AOS-552
// este corpo tinha sete campos e NENHUM de linhagem: um artefacto derivado de outro não era
// rastreável. Ganha dois, e nenhum deles leva conteúdo:
//
//   - [artefactoCriado.Inputs] — os digests dos handles de que esta gravação se alimentou, pela
//     ordem dos argumentos. Quem os sabe é a reescrita, que resolveu as referências antes da
//     mediação ([planoDeHandles.linhagemDaCall]); a partir dela a informação estava perdida.
//     Vazio quando o modelo escreveu o conteúdo inline, que é o caso de toda a R1b.
//   - [artefactoCriado.Taint] — o rótulo HERDADO, SELADO no evento e não recalculado por quem o
//     lê. Vale sempre `untrusted`, e isso não é redundância: é o que torna a herança uma
//     AFIRMAÇÃO verificável em vez de uma convenção.
//
// # PROVENIÊNCIA REGISTADA, NÃO DESCLASSIFICAÇÃO
//
// «Todo o artefacto é untrusted, seja qual for o produtor» (ADR-043 §2.3) FICA. A linhagem não
// promove nada e não existe caminho neste código que escreva outro valor em [artefactoCriado.Taint]:
// as entradas são handles, um handle é sempre untrusted ([agentruntime.Quarantine], que nunca
// promove), o join de untrusted com qualquer coisa é untrusted, e sem entradas o artefacto é
// untrusted porque foi o modelo que o escreveu. Quem decide o que pode fluir para onde é a política
// de fluxo de dados do AOS-551, que decide SOBRE untrusted e não precisa de promover nada.
//
// O HANDLE DESTE ARTEFACTO não leva campo de linhagem próprio, e é deliberado: ele é o MESMO
// conteúdo, selado no mesmo passo, pelo que a sua linhagem é a que este evento declara — procura-se
// pelo par `(run, step_id)`, que os dois eventos partilham.
//
// # UM EVENTO POR GRAVAÇÃO, NÃO POR ARTEFACTO
//
// O armazém deduplica o CONTEÚDO dentro de um run, mas sela um evento por CHAMADA. Logo o mesmo
// digest pode ter dois eventos — por exemplo, o conteúdo escrito inline e depois reescrito a partir
// do handle dele —, com linhagens diferentes, e um deles pode nomear-se a si mesmo como entrada (a
// derivação-identidade: os bytes são os mesmos). Nenhum dos dois é falso; o que seria falso é ler
// este evento como «a» proveniência de um digest. A rota de leitura resolve um digest pelo PRIMEIRO
// evento que casa, o que é indiferente para servir o conteúdo e não serve para construir um grafo
// de derivação sem considerar o `step_id`.
//
// A ORDEM de [artefactoCriado.Inputs] é a dos argumentos que a reescrita percorre — alfabética
// pelo nome, porque é assim que a recusa dela é determinista. Com o produtor de hoje a lista tem no
// máximo um elemento: só o `content` transporta conteúdo.
type artefactoCriado struct {
	Digest        string `json:"digest"`
	MediaType     string `json:"media_type"`
	Bytes         int    `json:"bytes"`
	Name          string `json:"name"`
	Tool          string `json:"tool"`
	StepID        string `json:"step_id"`
	SealedSubject string `json:"sealed_subject"`
	// Inputs são os digests das entradas em quarentena que produziram este artefacto (AOS-552).
	// Omitido quando não houve nenhuma.
	Inputs []string `json:"inputs,omitempty"`
	// Taint é o rótulo herdado, selado. Ver o comentário do tipo: vale sempre `untrusted`.
	//
	// SEM `omitempty`, de propósito: o único produtor escreve-o sempre, e com `omitempty` a
	// AUSÊNCIA ficava indistinguível de «sem rótulo» — quem lesse teria de tratar a ausência como
	// untrusted por convenção, que é precisamente o recálculo por quem lê que este campo existe
	// para eliminar. REGRA PARA OS EVENTOS ANTERIORES AO AOS-552 (em produção desde a v0.1.69): não
	// têm o campo, e leem-se como `untrusted` — era o que o ADR-043 §2.3 já afirmava para todo o
	// artefacto, e não havia nenhum outro valor possível.
	Taint string `json:"taint"`
}

// registoDeArtefacto constrói a escrita durável do evento sobre o Event Store do nó. nil com store nil.
func registoDeArtefacto(es eventstore.EventStore) func(context.Context, string, artefactoCriado) error {
	if es == nil {
		return nil
	}
	return func(ctx context.Context, runID string, p artefactoCriado) error {
		if runID == "" || p.StepID == "" {
			return fmt.Errorf("aos: artefacto sem run_id/step_id (run=%q step=%q)", runID, p.StepID)
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		_, err = es.Append(ctx, runID, eventstore.EventInput{
			Type:     EventTypeArtifactCreated,
			Payload:  raw,
			RunID:    runID,
			StepID:   artifactStepPrefix + p.StepID,
			Producer: eventstore.Producer{NHIID: artifactNHI},
		})
		return err
	}
}

// entradaDoArtefacto é a entrada da tool DEPOIS da reescrita: o que o modelo pediu, com o run, o
// passo e o titular da tool call real.
type entradaDoArtefacto struct {
	Schema    string `json:"schema"`
	RunID     string `json:"run_id"`
	StepID    string `json:"step_id"`
	Subject   string `json:"subject"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Content   string `json:"content"`
	// Inputs são os digests das entradas em quarentena que produziram este conteúdo (AOS-552): os
	// handles que a reescrita resolveu nos argumentos que TRANSPORTAM CONTEÚDO. Vem do
	// [bilheteDaCall] da mesma cadeia, e não do modelo — o [pedidoDoModelo] tem três campos e
	// `DisallowUnknownFields`, pelo que o modelo não consegue declarar uma linhagem que não teve.
	//
	// A LINHAGEM VIAJA NO EFEITO, e é isso que a torna parte do que o Reference Monitor medeia,
	// aprova e sela — o que o ADR-043 §2.3 pede.
	Inputs []string `json:"inputs,omitempty"`
}

// pedidoDoModelo é o que o modelo escreve: três campos, nenhum a mais.
type pedidoDoModelo struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Content   string `json:"content"`
}

// newArtifactRewriter devolve a reescrita do efeito da `artifact_write`: os argumentos do modelo
// passam a [entradaDoArtefacto] com o run, o passo e o titular da Call. Não toca na decisão; um
// erro vira Deny fail-closed. Uma Call de outra tool passa inalterada.
func newArtifactRewriter() reescritaDaCall {
	return func(call referencemonitor.Call, bilhete *bilheteDaCall) (referencemonitor.Call, error) {
		if call.ToolID != artifactWriteToolName {
			return call, nil
		}
		dec := json.NewDecoder(bytes.NewReader(call.Input))
		dec.DisallowUnknownFields()
		var p pedidoDoModelo
		if err := dec.Decode(&p); err != nil {
			return referencemonitor.Call{}, errors.New(artifactWriteToolName + ": argumentos ilegiveis (name, media_type e content, e nada mais)")
		}
		if _, err := dec.Token(); err != io.EOF {
			return referencemonitor.Call{}, errors.New(artifactWriteToolName + ": argumentos com conteudo a mais")
		}
		subject := call.Principal.Titular()
		if call.RunID == "" || call.StepID == "" || subject == "" {
			return referencemonitor.Call{}, errors.New(artifactWriteToolName + ": tool call sem run, passo ou titular")
		}
		// A LINHAGEM (AOS-552): só o digest do handle resolvido no argumento que TRANSPORTA O
		// CONTEÚDO. O `name` e o `media_type` podem levar uma referência resolvida — o `name` não,
		// que é slot do `resource_value` e a reescrita dos handles recusa-o — e uma referência no
		// `media_type` dava ao modelo uma forma de fazer o evento afirmar uma derivação que não
		// existe: o conteúdo viria inline e o `inputs` nomearia um handle que só forneceu o tipo.
		var entradas []string
		if d := bilhete.digestDoArgumento("content"); d != "" {
			entradas = []string{d}
		}
		raw, err := json.Marshal(entradaDoArtefacto{Schema: artifactInputSchema, RunID: call.RunID, StepID: call.StepID, Subject: subject,
			Name: p.Name, MediaType: p.MediaType, Content: p.Content, Inputs: entradas})
		if err != nil {
			return referencemonitor.Call{}, err
		}
		call.Input = raw
		return call, nil
	}
}

// executar é a [referencemonitor.ToolFunc] da `artifact_write`.
func (a *armazemDeArtefactos) executar(ctx context.Context, input []byte) ([]byte, error) {
	// ESTRITO: a entrada é a da reescrita, e só ela (defesa em profundidade do achado 1 da revisão).
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.DisallowUnknownFields()
	var in entradaDoArtefacto
	if err := dec.Decode(&in); err != nil || in.Schema != artifactInputSchema || in.RunID == "" || in.StepID == "" || in.Subject == "" {
		return a.recusar(artCausaEntrada)
	}
	if !nomeDeArtefacto.MatchString(in.Name) {
		return a.recusar(artCausaEntrada)
	}
	if !tiposDeArtefacto[in.MediaType] {
		return a.recusar(artCausaTipo)
	}
	// AOS-550: um `content` que seja uma referência a handle já chegou aqui RESOLVIDO. A troca
	// acontece no `CallRewriter`, ANTES da mediação, para que a preview da aprovação, o
	// `resource_value` auditado e o hash do span descrevam o efeito executado — ver handles.go.
	conteudo := []byte(in.Content)
	if len(conteudo) == 0 || len(conteudo) > artifactMaxBytes {
		return a.recusar(artCausaGrande)
	}
	if causa := validarConteudo(in.MediaType, conteudo); causa != "" {
		return a.recusar(causa)
	}
	if a.registar == nil {
		return a.recusar(artCausaRegisto)
	}
	// AOS-550 — O HANDLE DO ARTEFACTO, selado ANTES de o artefacto ser gravado e declarado.
	//
	// A ORDEM É DELIBERADA: uma falha da selagem com o artefacto já gravado deixaria o efeito
	// FEITO e a tool a reportar falha — a divergência entre o que se diz ao mundo e o que
	// aconteceu que este repositório fecha em todos os outros caminhos. Pela outra ordem, uma
	// falha da gravação deixa no máximo um handle selado que o modelo nunca vê (conteúdo que ele
	// próprio escreveu, na quarentena do seu run), e nada no armazém de artefactos.
	//
	// O conteúdo vai para o ESPAÇO PRÓPRIO DE HANDLES (tectos, vocabulário e superfície de leitura
	// próprios — handle_store.go), com o run nos dados associados e um `handle.created` de emissor
	// próprio: nada deste espaço fica descarregável com `read:artifact`. O token é cunhado pelo nó
	// e o nome do handle é `<tool>-<índice>`, nunca o nome que o modelo pediu.
	//
	// AOS-556 — NUM LAYOUT POR HANDLE ESTA TOOL NÃO SELA, porque o PRODUTOR do `tool_result` já
	// sela o resultado de cada call permitida. Selar nos dois punha o modelo a receber a
	// referência de um documento que CONTÉM a referência do artefacto: a de dentro NUNCA se
	// resolve (a recusa de referência aninhada olha para os argumentos que o modelo emitiu, não
	// para os bytes resolvidos, pelo que nem é recusada — é inerte) e cada escrita gastava DUAS
	// posições do tecto de handles do run.
	//
	// A PERGUNTA É A MESMA FUNÇÃO que o produtor e o tail fazem ([agentruntime.SealsToolResult]),
	// sobre a versão que o `Runtime.Run` pôs no contexto: não há aqui uma segunda definição de
	// quem sela.
	//
	// O QUE NÃO SE FEZ, E PORQUÊ: isentar esta tool do selo do PRODUTOR — o molde da `media_view` —
	// tirava ao segmento o rótulo `tool_result_handle`, e sem esse rótulo
	// [agentruntime.SegmentFloor] devolve untrusted. Todo o run que escrevesse um artefacto caía
	// para untrusted para sempre, que é o oposto do que a fase existe para comprar.
	//
	// CONSEQUÊNCIA DECLARADA: num layout por handle o conteúdo do artefacto não ganha handle
	// próprio, logo não é resolúvel como argumento de outra tool do mesmo run. Não se perde nada
	// que funcione hoje (a referência de dentro nunca era resolúvel), e o artefacto continua no
	// armazém, declarado por `artifact.created` e descarregável pelo titular. Num nó com handles e
	// SEM execução durável não há produtor e o resultado chega ao tail como erro de tool — o que
	// acontece a TODAS as tools nesse nó, e está declarado no banner de arranque.
	var handle string
	if a.handles != nil && !agentruntime.SealsToolResult(agentruntime.AssemblyVersionFromContext(ctx), artifactWriteToolName) {
		ref, hcausa := a.handles.selarEmQuarentena(ctx, in.Subject, in.RunID, in.StepID, artifactWriteToolName, in.MediaType, conteudo)
		if hcausa != "" {
			return a.recusar(hcausa)
		}
		handle = ref
	}
	digest, causa := a.guardar(ctx, in.Subject, in.RunID, conteudo)
	if causa != "" {
		return a.recusar(causa)
	}
	// AOS-552 — A LINHAGEM E O TAINT HERDADO, selados no evento. A linhagem chega NA ENTRADA, posta
	// pela reescrita que resolveu as referências — logo viajou dentro do efeito que o RM mediou. O
	// rótulo é `untrusted` sempre, e é uma AFIRMAÇÃO selada, não um recálculo de quem lê — ver
	// [artefactoCriado].
	if err := a.registar(ctx, in.RunID, artefactoCriado{Digest: digest, MediaType: in.MediaType, Bytes: len(conteudo), Name: in.Name,
		Tool: artifactWriteToolName, StepID: in.StepID, SealedSubject: in.Subject,
		Inputs: in.Inputs, Taint: taintDoArtefacto}); err != nil {
		return a.recusar(artCausaRegisto)
	}
	a.gravados.Add(1)
	return json.Marshal(struct {
		Schema    string `json:"schema"`
		Digest    string `json:"digest"`
		MediaType string `json:"media_type"`
		Bytes     int    `json:"bytes"`
		Name      string `json:"name"`
		Handle    string `json:"handle,omitempty"`
	}{ArtifactRefSchema, digest, in.MediaType, len(conteudo), in.Name, handle})
}

// recusar conta a recusa e devolve o erro da tool com a causa.
//
// AOS-550: o erro é construído por [erroDaTool], que recusa uma causa fora do vocabulário FECHADO
// da tool — a mensagem de erro de uma tool vai no CORPO do segmento `tool_result`, e um texto de
// terceiros aí repõe o canal inline que a fase R2 fecha.
func (a *armazemDeArtefactos) recusar(causa string) ([]byte, error) {
	a.recusados.Add(1)
	return nil, erroDaTool(artifactWriteToolName, causa)
}

// validarConteudo confere o conteúdo pelo tipo: UTF-8 sem NUL em todos; JSON bem formado; SVG pelas
// regras de [svgSeguro].
func validarConteudo(tipo string, b []byte) string {
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return artCausaConteudo
	}
	switch tipo {
	case "application/json":
		if !json.Valid(b) {
			return artCausaConteudo
		}
	case "image/svg+xml":
		if !svgSeguro(b) {
			return artCausaConteudo
		}
	}
	return ""
}

// svgNS é o namespace do SVG. Um elemento com outro namespace (um `h:img` XHTML) é recusado.
const svgNS = "http://www.w3.org/2000/svg"

// elementosSVG são os elementos aceites. ALLOWLIST: formas, texto, grupos, gradientes e definições —
// o que uma planta ou um diagrama precisa. Fora dela: `script`, `style`, `foreignObject`, `image`,
// `a`, as animações (`set`, `animate*`, que mudam atributos como o `href`) e tudo o resto.
var elementosSVG = map[string]bool{
	"svg": true, "g": true, "defs": true, "title": true, "desc": true, "symbol": true, "use": true,
	"rect": true, "circle": true, "ellipse": true, "line": true, "polyline": true, "polygon": true, "path": true,
	"text": true, "tspan": true, "lineargradient": true, "radialgradient": true, "stop": true,
	"clippath": true, "mask": true, "pattern": true, "marker": true,
}

// svgSeguro diz se um SVG é XML bem formado, com a raiz `svg`, só com os elementos de
// [elementosSVG] no namespace do SVG, sem DOCTYPE nem entidades, sem instruções de processamento
// além da declaração XML, sem atributos `on*` nem `xml:base`, e sem nenhuma ligação para fora: um
// `href`/`src` só pode ser `#…`, e nenhum valor ou texto pode trazer um esquema (`javascript:`,
// `data:`, `http:`…), um `url(` que não seja `url(#…)`, `image-set(`, `@import`, `expression(` ou
// uma barra invertida (os escapes do CSS). Os valores comparam-se NORMALIZADOS: sem espaços nem
// caracteres de controlo e em minúsculas — `java&#9;script:` é `javascript:`. RECUSA, não limpa.
func svgSeguro(b []byte) bool {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = true
	raiz := true
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return !raiz
		}
		if err != nil {
			return false
		}
		switch t := tok.(type) {
		case xml.Directive:
			return false // DOCTYPE, ENTITY
		case xml.ProcInst:
			if t.Target != "xml" {
				return false
			}
		case xml.StartElement:
			local := strings.ToLower(t.Name.Local)
			if (t.Name.Space != "" && t.Name.Space != svgNS) || !elementosSVG[local] {
				return false
			}
			if raiz {
				if local != "svg" {
					return false
				}
				raiz = false
			}
			for _, at := range t.Attr {
				// As declarações de namespace só podem ser as do SVG e do XLink.
				if at.Name.Space == "xmlns" || (at.Name.Space == "" && at.Name.Local == "xmlns") {
					if at.Value != svgNS && at.Value != "http://www.w3.org/1999/xlink" {
						return false
					}
					continue
				}
				nome := strings.ToLower(at.Name.Local)
				espaco := strings.ToLower(at.Name.Space)
				valor := normalizarValor(at.Value)
				if strings.HasPrefix(nome, "on") || (espaco == "xml" || espaco == "http://www.w3.org/xml/1998/namespace") && nome == "base" || nome == "base" {
					return false
				}
				if nome == "href" || nome == "src" {
					if !strings.HasPrefix(valor, "#") {
						return false
					}
					continue
				}
				if !valorSeguro(valor) {
					return false
				}
			}
		case xml.CharData:
			if !valorSeguro(normalizarValor(string(t))) {
				return false
			}
		}
	}
}

// normalizarValor tira espaços e caracteres de controlo e passa a minúsculas: é a forma em que um
// browser lê um esquema (`java\tscript:` é `javascript:`).
func normalizarValor(v string) string {
	var sb strings.Builder
	for _, r := range v {
		if r <= ' ' || r == 0x7f {
			continue
		}
		sb.WriteRune(r)
	}
	return strings.ToLower(sb.String())
}

// valorSeguro recusa um valor (já normalizado) com uma ligação para fora ou um escape CSS.
func valorSeguro(v string) bool {
	for _, mau := range []string{"javascript:", "vbscript:", "data:", "http:", "https:", "ftp:", "file:", "//", "@import", "@font-face", "expression(", "image-set(", "\\"} {
		if strings.Contains(v, mau) {
			return false
		}
	}
	for i := strings.Index(v, "url("); i >= 0; {
		resto := strings.TrimLeft(v[i+4:], "'\"")
		if !strings.HasPrefix(resto, "#") {
			return false
		}
		j := strings.Index(v[i+4:], "url(")
		if j < 0 {
			break
		}
		i += 4 + j
	}
	return true
}

// semArtefactos tira a `artifact_write` da lista das tools oferecidas sem executor: com o armazém
// ligado, o executor é código do nó, registado no RM pelo Bootstrap. Desligado, continua órfã — e é
// declarada.
func semArtefactos(semExecutor []string, ligado bool) []string {
	if !ligado {
		return semExecutor
	}
	var out []string
	for _, n := range semExecutor {
		if n != artifactWriteToolName {
			out = append(out, n)
		}
	}
	return out
}

// artifactBanner é a linha de arranque. Sem armazém não sai linha nenhuma.
//
// O TECTO POR ARTEFACTO QUE SE ANUNCIA É O EFECTIVO. Com o espaço de handles ligado (AOS-550) a
// tool sela cada conteúdo em quarentena ANTES de o gravar, e o tecto de lá é o do corpo de um
// `tool_result` — mais apertado. Anunciar 1 MiB nesse caso era uma regressão silenciosa: o
// operador lia um número e a tool recusava noutro (achado B-2 da revisão de segurança).
func artifactBanner(a *armazemDeArtefactos) []string {
	if a == nil {
		return nil
	}
	tecto, nota := artifactMaxBytes, ""
	if a.handles != nil && handleMaxBytes < tecto {
		tecto, nota = handleMaxBytes, fmt.Sprintf(" (o tecto por artefacto e o do corpo de um tool_result, %d, e nao os %d do armazem: com AOS_HANDLE_DIR ligada cada artefacto e selado tambem no espaco de handles; num run num layout por handle quem sela o resultado e o PRODUTOR e a tool nao sela, pelo que ali o tecto efectivo volta a ser os %d do armazem — AOS-556)", handleMaxBytes, artifactMaxBytes, artifactMaxBytes)
	}
	return []string{fmt.Sprintf("armazem de artefactos (AOS-548, ADR-043 §2.3): AOS_ARTIFACT_DIR=%q — a tool %s grava texto (json, markdown, csv, texto, svg sem script nem ligacoes externas; ate %d bytes%s, %d artefactos e %d bytes por run, %d bytes no volume) CIFRADO por titular, sela a proveniencia (%s) no stream do run e devolve uma referencia; GET /runs/{id}/artifacts serve-o como anexo (nosniff, CSP sandbox) pelas portas da leitura soberana. Ocupado no arranque: %d bytes. Resíduo: um titular apagado deixa os blobs cifrados (ilegiveis) na pasta",
		a.pasta, artifactWriteToolName, tecto, nota, artifactMaxPorRun, artifactMaxBytesPorRun, a.maximo, EventTypeArtifactCreated, a.usado.Load())}
}
