package routeentry

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
)

// ProductionProxyImage é a imagem do proxy declarada para produção, pelo digest. É a mesma
// referência do compose de produção e do banco de ensaio: o teste do AOS-517
// (`packages/qa/banco-ensaio/aos517_digest_do_proxy_test.go`) varre este módulo e avermelha se
// esta linha se afastar da declaração.
//
// O nó compara com ela o digest que o registo e o relatório citam. NÃO vê que imagem o proxy está
// de facto a correr: isso é garantido pelo compose, não pelo nó.
const ProductionProxyImage = "ghcr.io/berriai/litellm@sha256:154e23bb5f31b1f10e16392a8ef299bd2cde08de3a64a6849002cfcc25ce3c63"

// ProductionProxyImageDigest devolve o digest de [ProductionProxyImage] (`sha256:…`).
func ProductionProxyImageDigest() string {
	_, digest, _ := strings.Cut(ProductionProxyImage, "@")
	return digest
}

// Os sufixos dos três ficheiros de um registo.
const (
	// SuffixEntry é o sufixo do ficheiro do registo de entrada.
	SuffixEntry = ".registo.json"
	// SuffixSignature é o sufixo do ficheiro da assinatura.
	SuffixSignature = ".registo.sig"
	// SuffixReport é o sufixo do ficheiro do relatório citado.
	SuffixReport = ".relatorio.json"
)

// Tectos da pasta.
const (
	// MaxEntries é o máximo de registos numa pasta. É também o que fecha o número de séries das
	// métricas que têm o modelo servido no rótulo.
	MaxEntries = 8
	// maxEntryBytes é o tecto de um registo de entrada.
	maxEntryBytes = 64 << 10
	// maxSignatureBytes é o tecto do ficheiro da assinatura.
	maxSignatureBytes = 1 << 10
	// maxReportBytes é o tecto de um relatório de qualificação.
	maxReportBytes = 8 << 20
)

// Config é o que [LoadDir] precisa de quem compõe o nó.
type Config struct {
	// Dir é a pasta dos registos.
	Dir string
	// TrustAnchor é a chave pública ed25519 do assinante dos perfis, em base64, dada por fora
	// (configuração do nó). É uma chave SÓ para perfis de rota.
	TrustAnchor string
	// ForeignAnchors são âncoras de OUTRAS políticas (base64 de chaves públicas ed25519) com que a
	// dos perfis não pode coincidir — hoje, as da allowlist. Um valor que não se descodifique é
	// ignorado: quem o valida é quem o usa.
	ForeignAnchors []string
	// Board e Region são o board e a região do nó.
	Board  string
	Region string
	// Allowlist é a allowlist de modelos em vigor no nó, já verificada. nil ⇒ nenhum nome é
	// autorizado (fail-closed).
	Allowlist *allowlist.Policy
}

// LoadDir lê a pasta dos registos de entrada e confere TUDO: a âncora, o conjunto de ficheiros,
// e, para cada registo, a assinatura, o registo, o ambiente, a imagem do proxy, o relatório
// citado e a allowlist. Devolve os registos pela ordem do rótulo.
//
// Ou devolve todos, ou devolve um [*Refusal]: à primeira conferência que falha, nada é devolvido.
// Uma pasta vazia devolve zero registos e nenhum erro.
//
// Lê os ficheiros UMA vez. Não vigia a pasta: o que mudar nela depois não tem efeito em quem já
// tem os registos na mão.
func LoadDir(cfg Config) ([]Entry, error) {
	pub, err := trustAnchor(cfg)
	if err != nil {
		return nil, err
	}
	labels, err := scan(cfg.Dir)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(labels))
	byName := map[string]bool{}
	for _, label := range labels {
		e, err := loadOne(cfg, pub, label)
		if err != nil {
			var r *Refusal
			if errors.As(err, &r) {
				return nil, refuse(r.Cause, label)
			}
			return nil, err
		}
		if byName[e.Profile.Requested] {
			return nil, refuse(CauseDuplicateName, label)
		}
		byName[e.Profile.Requested] = true
		entries = append(entries, e)
	}
	return entries, nil
}

// trustAnchor lê e valida a âncora de confiança dos perfis.
func trustAnchor(cfg Config) (ed25519.PublicKey, error) {
	anchor := strings.TrimSpace(cfg.TrustAnchor)
	if anchor == "" {
		return nil, refuse(CauseNoTrustAnchor, "")
	}
	raw, err := base64.StdEncoding.DecodeString(anchor)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, refuse(CauseBadTrustAnchor, "")
	}
	for _, foreign := range cfg.ForeignAnchors {
		other, err := base64.StdEncoding.DecodeString(strings.TrimSpace(foreign))
		if err == nil && bytes.Equal(other, raw) {
			return nil, refuse(CauseSharedTrustAnchor, "")
		}
	}
	return ed25519.PublicKey(raw), nil
}

// scan lê a pasta e devolve os rótulos dos registos, ordenados. Recusa a pasta que não se lê, a
// entrada que não é um dos três ficheiros de um registo, a assinatura ou o relatório sem registo,
// e o excesso de registos. A falta de uma assinatura ou de um relatório NÃO é daqui: tem causa
// própria, em [loadOne].
func scan(dir string) ([]string, error) {
	listing, err := os.ReadDir(dir)
	if err != nil {
		return nil, refuse(CauseDirUnreadable, "")
	}
	entries := map[string]bool{}
	var others []string
	for _, f := range listing {
		label, suffix, known := splitName(f.Name())
		// Só ficheiros regulares: uma subpasta e uma ligação simbólica não são um registo.
		if !known || !f.Type().IsRegular() {
			return nil, refuse(CauseUnknownFile, safeLabel(label))
		}
		if suffix == SuffixEntry {
			entries[label] = true
		} else {
			others = append(others, label)
		}
	}
	for _, label := range others {
		if !entries[label] {
			return nil, refuse(CauseUnknownFile, label)
		}
	}
	if len(entries) > MaxEntries {
		return nil, refuse(CauseTooManyEntries, "")
	}
	labels := make([]string, 0, len(entries))
	for label := range entries {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return labels, nil
}

// splitName parte o nome de um ficheiro no rótulo e no sufixo. `known` é false quando o sufixo não
// é um dos três ou o rótulo não passa em [validLabel].
func splitName(name string) (label, suffix string, known bool) {
	for _, s := range []string{SuffixEntry, SuffixSignature, SuffixReport} {
		if l, has := strings.CutSuffix(name, s); has {
			return l, s, validLabel(l)
		}
	}
	return "", "", false
}

// safeLabel devolve o rótulo se ele se puder escrever numa mensagem; senão, vazio. O nome de um
// ficheiro que não se reconhece não é repetido.
func safeLabel(label string) string {
	if validLabel(label) {
		return label
	}
	return ""
}

// loadOne lê e confere um registo. As recusas saem sem rótulo; [LoadDir] põe-lho.
func loadOne(cfg Config, pub ed25519.PublicKey, label string) (Entry, error) {
	raw, state := readCapped(filepath.Join(cfg.Dir, label+SuffixEntry), maxEntryBytes)
	if state != fileRead {
		return Entry{}, refuse(CauseEntryUnreadable, "")
	}
	// A ASSINATURA PRIMEIRO. Nada do registo é lido antes de se saber que foi o dono a assiná-lo.
	signature, state := readCapped(filepath.Join(cfg.Dir, label+SuffixSignature), maxSignatureBytes)
	switch state {
	case fileMissing:
		return Entry{}, refuse(CauseSignatureMissing, "")
	case fileUnreadable:
		return Entry{}, refuse(CauseSignatureInvalid, "")
	}
	if !VerifySignature(raw, signature, pub) {
		return Entry{}, refuse(CauseSignatureInvalid, "")
	}
	e, err := ParseEntry(raw)
	if err != nil {
		return Entry{}, err
	}
	e.Label = label
	if e.Board != cfg.Board || e.Region != cfg.Region || cfg.Board == "" || cfg.Region == "" {
		return Entry{}, refuse(CauseOtherEnvironment, "")
	}
	if e.ProxyImageDigest != ProductionProxyImageDigest() {
		return Entry{}, refuse(CauseProxyImageNotDeclared, "")
	}
	report, state := readCapped(filepath.Join(cfg.Dir, label+SuffixReport), maxReportBytes)
	switch state {
	case fileMissing:
		return Entry{}, refuse(CauseReportMissing, "")
	case fileUnreadable:
		return Entry{}, refuse(CauseReportUnreadable, "")
	}
	if err := CheckReport(e, report); err != nil {
		return Entry{}, err
	}
	// A ALLOWLIST CONTINUA A MANDAR. O perfil diz o que um nome significa; quem autoriza o nome,
	// para este board e esta região, é a allowlist assinada — e sabe-se aqui, no arranque, e não
	// ao primeiro pedido.
	if cfg.Allowlist == nil || cfg.Allowlist.Evaluate(allowlist.Input{Board: cfg.Board, Model: e.Profile.Requested, Region: cfg.Region}) != allowlist.EffectAllow {
		return Entry{}, refuse(CauseNameNotAllowlisted, "")
	}
	return e, nil
}

// O estado da leitura de um ficheiro.
const (
	fileRead = iota
	fileMissing
	fileUnreadable
)

// readCapped lê um ficheiro até `max` bytes. Um ficheiro maior, vazio ou que não se lê é
// `fileUnreadable`; um que não existe é `fileMissing`.
func readCapped(path string, max int64) ([]byte, int) {
	f, err := os.Open(path) // #nosec G304 -- o caminho é a pasta configurada pelo operador mais um rótulo conferido contra um alfabeto fechado
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fileMissing
		}
		return nil, fileUnreadable
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(raw)) > max || len(raw) == 0 {
		return nil, fileUnreadable
	}
	return raw, fileRead
}
