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

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
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
type artefactoCriado struct {
	Digest        string `json:"digest"`
	MediaType     string `json:"media_type"`
	Bytes         int    `json:"bytes"`
	Name          string `json:"name"`
	Tool          string `json:"tool"`
	StepID        string `json:"step_id"`
	SealedSubject string `json:"sealed_subject"`
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
func newArtifactRewriter() func(referencemonitor.Call) (referencemonitor.Call, error) {
	return func(call referencemonitor.Call) (referencemonitor.Call, error) {
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
		raw, err := json.Marshal(entradaDoArtefacto{Schema: artifactInputSchema, RunID: call.RunID, StepID: call.StepID, Subject: subject,
			Name: p.Name, MediaType: p.MediaType, Content: p.Content})
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
	digest, causa := a.guardar(ctx, in.Subject, in.RunID, conteudo)
	if causa != "" {
		return a.recusar(causa)
	}
	if err := a.registar(ctx, in.RunID, artefactoCriado{Digest: digest, MediaType: in.MediaType, Bytes: len(conteudo), Name: in.Name,
		Tool: artifactWriteToolName, StepID: in.StepID, SealedSubject: in.Subject}); err != nil {
		return a.recusar(artCausaRegisto)
	}
	a.gravados.Add(1)
	return json.Marshal(struct {
		Schema    string `json:"schema"`
		Digest    string `json:"digest"`
		MediaType string `json:"media_type"`
		Bytes     int    `json:"bytes"`
		Name      string `json:"name"`
	}{ArtifactRefSchema, digest, in.MediaType, len(conteudo), in.Name})
}

// recusar conta a recusa e devolve o erro da tool com a causa.
func (a *armazemDeArtefactos) recusar(causa string) ([]byte, error) {
	a.recusados.Add(1)
	return nil, fmt.Errorf("%s: %s", artifactWriteToolName, causa)
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
func artifactBanner(a *armazemDeArtefactos) []string {
	if a == nil {
		return nil
	}
	return []string{fmt.Sprintf("armazem de artefactos (AOS-548, ADR-043 §2.3): AOS_ARTIFACT_DIR=%q — a tool %s grava texto (json, markdown, csv, texto, svg sem script nem ligacoes externas; ate %d bytes, %d artefactos e %d bytes por run, %d bytes no volume) CIFRADO por titular, sela a proveniencia (%s) no stream do run e devolve uma referencia; GET /runs/{id}/artifacts serve-o como anexo (nosniff, CSP sandbox) pelas portas da leitura soberana. Ocupado no arranque: %d bytes. Resíduo: um titular apagado deixa os blobs cifrados (ilegiveis) na pasta",
		a.pasta, artifactWriteToolName, artifactMaxBytes, artifactMaxPorRun, artifactMaxBytesPorRun, a.maximo, EventTypeArtifactCreated, a.usado.Load())}
}
