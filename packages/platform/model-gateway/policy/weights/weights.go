// Package weights é a TABELA DE PESOS do scoring ponderado determinístico do Model
// Gateway (AOS-269, ADR-021 regra 3, tecnica/06 §6.1). É a policy-as-code que diz
// QUANTO vale cada factor (health, headroom, custo, latência, task-fit,
// estabilidade) em cada PERFIL nomeado (balanced/fast/cheap/quality) — e nada mais:
// não conhece router, tiering nem soberania. Quem ORDENA é routing/scoring; quem
// GUARDA (soberania, allowlist, piso de capacidade) é o router, ANTES de qualquer
// score (ADR-021 regra 1 — um peso nunca ressuscita um candidato cross-border).
//
// # Artefacto COMPORTAMENTAL versionado e ASSINADO (ADR-012 + ADR-011)
//
// A tabela é um artefacto comportamental: mudar um peso MUDA A DECISÃO do gateway
// sem mudar uma linha de código. Por isso segue EXACTAMENTE o molde da allowlist
// regional (policy/allowlist, AOS-058) — não se inventou aqui um segundo mecanismo
// de confiança:
//
//   - vive num ficheiro VERSIONADO (weights_table.json) embebido via go:embed;
//   - tem um DIGEST canónico (sha256) que a torna tamper-evident: [Table.Version]
//     devolve "versão#digest12", o valor registado em CADA decisão de roteamento
//     (span model_routing + DecisionSink) — a decisão fica ligada à versão EXACTA
//     dos pesos em vigor (ADR-010, replay byte-a-byte);
//   - traz um campo SemVer OBRIGATÓRIO e validado ([Table.SemVer]): alterar pesos
//     exige bump de versão + passagem no eval-gate ANTES de produção (ADR-012);
//   - é ASSINADA em ed25519 (crypto/ed25519 stdlib) sobre esse digest. O
//     carregamento VERIFICA a assinatura contra a chave PÚBLICA embebida e essa
//     pública tem de bater com o TRUST ANCHOR PINADO EM CÓDIGO
//     ([trustAnchorFingerprint]) — trocar weights_table.pub por uma chave própria
//     (para reassinar pesos adulterados) é recusado fail-closed
//     ([ErrTrustAnchorMismatch]). A privada é custodiada OFFLINE (ADR-006) e nunca
//     entra no runtime nem no repositório (ver gen_signature.go).
//
// # Fail-closed (ADR-021 regra 3)
//
// NÃO existe carregador que devolva uma [Table] utilizável sem verificar a
// assinatura: [LoadSignedTable] é o único caminho, e [LoadTable]/[LoadSignedTableFromDir]
// passam por ele. Uma tabela malformada, com perfil vazio, com pesos negativos, com
// um perfil de soma zero, com SemVer inválido ou com assinatura/anchor errados NÃO
// produz tabela — o chamador (o scorer, e por ele o router) RECUSA. Não há pesos
// implícitos por omissão: a ausência de tabela nunca é "usa uns pesos quaisquer".
//
// # Zero floats (ADR-021 regra 2)
//
// Todos os pesos são INTEIROS (`int`), em milésimos de unidade de decisão. Não há
// float32/float64 em lado nenhum deste pacote nem do scorer: o data-plane de
// decisão é aritmética inteira pura, condição do determinismo e do replay.
package weights

import (
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

//go:embed weights_table.json
var embeddedTable []byte

//go:embed weights_table.sig
var embeddedSig []byte

//go:embed weights_table.pub
var embeddedPub []byte

// MaxWeight é o tecto de um peso individual (milésimos). Um peso fora de [0,
// MaxWeight] é tabela MALFORMADA (fail-closed) — evita que um peso absurdo
// (overflow ou erro de digitação) domine a soma ponderada de forma não revista.
const MaxWeight = 1000

// trustAnchorFingerprint é o SHA-256 (hex minúsculo) da chave PÚBLICA de confiança
// da tabela de pesos — o TRUST ANCHOR, pinado e revisto EM CÓDIGO, exactamente como
// em policy/allowlist. A privada correspondente foi gerada offline com entropia real
// (crypto/rand) e é custodiada FORA do repositório (ADR-006); não deriva de nenhuma
// seed/etiqueta pública, logo não é reconstrutível a partir do repo. [LoadTable]
// exige que o fingerprint da pública embebida (weights_table.pub) bata EXACTAMENTE
// com esta constante: trocar o ficheiro .pub por outra chave é rejeitado fail-closed
// ([ErrTrustAnchorMismatch]). Rodar a chave exige alterar esta constante
// (code-review auditável) + reassinar offline (ver gen_signature.go).
const trustAnchorFingerprint = "a362ecaed2a70ac887deac34ed9fd4073a1854c6b0d0fb958e2d18596b2ffef8"

// Erros fail-closed do carregamento/validação. São o vocabulário que o scorer e o
// router propagam quando RECUSAM por falta de tabela válida.
var (
	// ErrTableMalformed — documento inválido: sem versão/SemVer, sem perfis, nome de
	// perfil vazio/duplicado, peso fora de [0,MaxWeight], perfil de soma ZERO (que
	// não ordenaria nada) ou default_profile inexistente. Fail-closed.
	ErrTableMalformed = errors.New("weights: tabela de pesos malformada (fail-closed)")
	// ErrSignatureInvalid — a assinatura ed25519 não verifica contra a pública de
	// confiança. Tabela adulterada/não-assinada é recusada no carregamento.
	ErrSignatureInvalid = errors.New("weights: assinatura da tabela de pesos invalida (fail-closed)")
	// ErrPubKeyInvalid — a chave pública de confiança é inválida (não-base64 ou
	// tamanho errado).
	ErrPubKeyInvalid = errors.New("weights: chave publica de confianca da tabela invalida")
	// ErrTrustAnchorMismatch — a pública EMBEBIDA não corresponde ao trust anchor
	// pinado em código. Fail-closed: a raiz de confiança é a constante revista, não o
	// ficheiro embebido.
	ErrTrustAnchorMismatch = errors.New("weights: chave publica embebida nao corresponde ao trust anchor pinado (fail-closed)")
)

// Weights é o vector de pesos de UM perfil — INTEIROS em milésimos (ADR-021 regra
// 2: zero floats no data-plane). Cada peso multiplica o valor normalizado (0..1000)
// do factor homónimo; a soma ponderada é dividida pela soma dos pesos, tudo em
// aritmética inteira.
type Weights struct {
	// Health — saúde observada do endpoint/modelo (0 = doente, 1000 = são).
	Health int `json:"health"`
	// Headroom — folga real de TPM/RPM do endpoint (deriva da porta LoadProvider já
	// existente do router, AOS-057/059 — não é um sinal novo).
	Headroom int `json:"headroom"`
	// Cost — quão BARATO é o tier (normalizado sobre a escada de custo).
	Cost int `json:"cost"`
	// Latency — quão RÁPIDO é o tier (p95 medido offline, ou o bit Fast da escada).
	Latency int `json:"latency"`
	// TaskFit — sinal de QUALIDADE por tipo de tarefa, calibrado OFFLINE pelo eval
	// harness (EPIC-08). Nunca aprendido em runtime (ADR-021 regra 4).
	TaskFit int `json:"task_fit"`
	// Stability — estabilidade histórica (taxa de sucesso), também offline.
	Stability int `json:"stability"`
}

// Sum devolve a soma dos pesos (o divisor da normalização). Inteiro.
func (w Weights) Sum() int {
	return w.Health + w.Headroom + w.Cost + w.Latency + w.TaskFit + w.Stability
}

// valid reporta se todos os pesos estão em [0,MaxWeight] e a soma é > 0 (um perfil
// de soma zero não ordena nada — seria um ranking silenciosamente arbitrário).
func (w Weights) valid() bool {
	for _, v := range []int{w.Health, w.Headroom, w.Cost, w.Latency, w.TaskFit, w.Stability} {
		if v < 0 || v > MaxWeight {
			return false
		}
	}
	return w.Sum() > 0
}

// Profile é um perfil NOMEADO de pesos (a "intenção declarada" do consumidor:
// balanced/fast/cheap/quality) — a gramática que substitui a ordem lexicográfica
// gravada em código.
type Profile struct {
	Name    string  `json:"name"`
	Weights Weights `json:"weights"`
}

// Table é a tabela de pesos carregada, versionada e VERIFICADA.
type Table struct {
	VersionTag     string    `json:"version"`
	SemVerTag      string    `json:"semver"`
	DefaultProfile string    `json:"default_profile"`
	Profiles       []Profile `json:"profiles"`

	digest string // sha256 hex do conteúdo canónico (material assinado + versão)
}

// LoadTable carrega a tabela EMBEBIDA (weights_table.json), verificando PRIMEIRO o
// trust anchor pinado e DEPOIS a assinatura ed25519. Fail-closed em qualquer falha
// — não devolve tabela parcial nem pesos por omissão.
func LoadTable() (*Table, error) {
	pubB64 := strings.TrimSpace(string(embeddedPub))
	if err := verifyTrustAnchor(pubB64); err != nil {
		return nil, err
	}
	return LoadSignedTable(embeddedTable, strings.TrimSpace(string(embeddedSig)), pubB64)
}

// verifyTrustAnchor confirma que a pública embebida é EXACTAMENTE a do anchor
// pinado em código (fingerprint sha256) — o gate que torna a assinatura
// não-forjável por troca do ficheiro .pub.
func verifyTrustAnchor(pubB64 string) error {
	raw, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPubKeyInvalid, err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != trustAnchorFingerprint {
		return ErrTrustAnchorMismatch
	}
	return nil
}

// LoadSignedTable é o ÚNICO carregador: valida a estrutura (fail-closed), calcula o
// digest canónico e VERIFICA a assinatura ed25519 sobre esse digest com a pública
// dada. Não há caminho que aceite uma tabela sem verificar assinatura.
func LoadSignedTable(tableJSON []byte, signatureB64, pubKeyB64 string) (*Table, error) {
	pub, err := decodePubKey(pubKeyB64)
	if err != nil {
		return nil, err
	}
	t, err := parse(tableJSON)
	if err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return nil, fmt.Errorf("%w: assinatura nao-base64: %v", ErrSignatureInvalid, err)
	}
	// A assinatura cobre o DIGEST canónico: mexer num único peso muda o digest e
	// invalida a assinatura (tamper-evident).
	if !ed25519.Verify(pub, []byte(t.digest), sig) {
		return nil, ErrSignatureInvalid
	}
	return t, nil
}

// Nomes dos ficheiros do BUNDLE EXTERNO de pesos (espelham os embebidos), para o
// operador que curadoria e assina a sua própria tabela — a mesma via externa da
// allowlist (AOS-220): governança idêntica, só a proveniência e o anchor mudam.
const (
	BundleTableFile = "weights_table.json"
	BundleSigFile   = "weights_table.sig"
)

// LoadSignedTableFromDir carrega e VERIFICA uma tabela de pesos a partir de um
// DIRECTÓRIO MONTADO, contra um trust anchor fornecido OUT-OF-BAND (base64 da
// pubkey ed25519 do assinante) — não o fingerprint pinado da tabela embebida.
// Fail-closed: ficheiro ausente/ilegível, assinatura inválida ou anchor malformado
// ⇒ erro (o chamador recusa arrancar / recusa rotear).
func LoadSignedTableFromDir(dir, trustAnchorB64 string) (*Table, error) {
	tableJSON, err := os.ReadFile(filepath.Join(dir, BundleTableFile))
	if err != nil {
		return nil, fmt.Errorf("weights: ler tabela do bundle: %w", err)
	}
	sig, err := os.ReadFile(filepath.Join(dir, BundleSigFile))
	if err != nil {
		return nil, fmt.Errorf("weights: ler assinatura do bundle: %w", err)
	}
	return LoadSignedTable(tableJSON, strings.TrimSpace(string(sig)), strings.TrimSpace(trustAnchorB64))
}

// Digest devolve o digest canónico (sha256 hex) do documento SEM verificar
// assinatura. É o MATERIAL ASSINADO — usado offline para produzir a assinatura
// (gen_signature.go) e nos testes para (re)assinar. NÃO é um carregador: não
// devolve tabela utilizável.
func Digest(tableJSON []byte) (string, error) {
	t, err := parse(tableJSON)
	if err != nil {
		return "", err
	}
	return t.digest, nil
}

// parse desserializa e VALIDA a estrutura (fail-closed) e calcula o digest — uso
// interno partilhado por [LoadSignedTable] e [Digest].
func parse(tableJSON []byte) (*Table, error) {
	var t Table
	dec := json.NewDecoder(strings.NewReader(string(tableJSON)))
	dec.DisallowUnknownFields() // schema FECHADO: um campo desconhecido é malformação, não é ignorado
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTableMalformed, err)
	}
	if t.VersionTag == "" {
		return nil, fmt.Errorf("%w: versao vazia", ErrTableMalformed)
	}
	if !validSemVer(t.SemVerTag) {
		return nil, fmt.Errorf("%w: semver invalido %q (ADR-012 exige MAJOR.MINOR.PATCH)", ErrTableMalformed, t.SemVerTag)
	}
	if len(t.Profiles) == 0 {
		return nil, fmt.Errorf("%w: sem perfis", ErrTableMalformed)
	}
	seen := make(map[string]struct{}, len(t.Profiles))
	for _, p := range t.Profiles {
		if p.Name == "" {
			return nil, fmt.Errorf("%w: perfil sem nome", ErrTableMalformed)
		}
		if _, dup := seen[p.Name]; dup {
			return nil, fmt.Errorf("%w: perfil duplicado %q", ErrTableMalformed, p.Name)
		}
		seen[p.Name] = struct{}{}
		if !p.Weights.valid() {
			return nil, fmt.Errorf("%w: pesos invalidos no perfil %q (fora de [0,%d] ou soma zero)", ErrTableMalformed, p.Name, MaxWeight)
		}
	}
	if _, ok := seen[t.DefaultProfile]; !ok {
		return nil, fmt.Errorf("%w: default_profile %q nao existe", ErrTableMalformed, t.DefaultProfile)
	}
	t.digest = canonicalDigest(&t)
	return &t, nil
}

// validSemVer valida MAJOR.MINOR.PATCH com componentes numéricos não-negativos
// (subconjunto fechado de SemVer, suficiente para o ciclo ADR-012 e sem
// dependências). Parser inteiro puro — sem floats, sem regexp.
func validSemVer(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return false // sem zeros à esquerda (canonicidade do digest)
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return false
		}
	}
	return true
}

// decodePubKey descodifica a pública ed25519 (base64) e valida o tamanho.
func decodePubKey(pubKeyB64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPubKeyInvalid, err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, ErrPubKeyInvalid
	}
	return ed25519.PublicKey(raw), nil
}

// Version devolve a identidade VERSIONADA e tamper-evident da tabela:
// "versão#digest12". É o valor registado em cada decisão de roteamento (span +
// DecisionSink), ligando a decisão aos pesos EXACTOS em vigor.
func (t *Table) Version() string {
	return t.VersionTag + "#" + t.digest[:12]
}

// SemVer devolve o SemVer declarado (ADR-012): alterar pesos = bump + eval-gate.
func (t *Table) SemVer() string { return t.SemVerTag }

// Default devolve o nome do perfil por omissão (garantidamente existente — validado
// no carregamento).
func (t *Table) Default() string { return t.DefaultProfile }

// Lookup devolve os pesos do perfil NOMEADO. ok=false para perfil desconhecido — o
// chamador RECUSA (nunca cai num perfil implícito: um perfil desconhecido não é
// silenciosamente tratado como "balanced").
func (t *Table) Lookup(profile string) (Weights, bool) {
	for _, p := range t.Profiles {
		if p.Name == profile {
			return p.Weights, true
		}
	}
	return Weights{}, false
}

// Names devolve os nomes dos perfis por ordem ALFABÉTICA (determinismo na
// introspecção/diagnóstico).
func (t *Table) Names() []string {
	out := make([]string, 0, len(t.Profiles))
	for _, p := range t.Profiles {
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}

// canonicalDigest calcula um sha256 determinista do CONTEÚDO da tabela (versão,
// semver, default e perfis ORDENADOS por nome), independente de espaços/ordem do
// JSON original. É a base da versão tamper-evident E o material assinado.
func canonicalDigest(t *Table) string {
	profiles := make([]Profile, len(t.Profiles))
	copy(profiles, t.Profiles)
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	canon := struct {
		Version string    `json:"version"`
		SemVer  string    `json:"semver"`
		Default string    `json:"default_profile"`
		Profs   []Profile `json:"profiles"`
	}{Version: t.VersionTag, SemVer: t.SemVerTag, Default: t.DefaultProfile, Profs: profiles}
	b, _ := json.Marshal(canon)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
