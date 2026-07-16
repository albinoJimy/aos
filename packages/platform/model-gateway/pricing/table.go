// Package pricing é a TABELA DE PREÇOS VERSIONADA por (modelo, região) do Model
// Gateway (AOS-062, tecnica/06 §4, ADR-008/ADR-010). É a fonte de verdade dos
// RATES que a contabilidade de custo (metering/cost) usa para derivar o custo em
// micro-USD INTEIRO de cada chamada — nunca um preço mágico disperso pelo código.
//
// # Quatro rates DISTINTOS por (modelo, região)
//
// Cada entrada declara QUATRO rates independentes, um por tipo de token, porque os
// provedores cobram preços DIFERENTES por tipo (cache read é o mais barato; cache
// write custa mais do que o input normal):
//
//   - InputPerMTokMicroUSD      — input NÃO-cacheado (por 1M tokens, micro-USD)
//   - OutputPerMTokMicroUSD     — output/completion
//   - CacheReadPerMTokMicroUSD  — leitura de cache de prefixo (desconto)
//   - CacheWritePerMTokMicroUSD — escrita/criação de cache (prémio)
//
// Os rates são em MICRO-USD por 1_000_000 de tokens (int64) — dinheiro é sempre
// inteiro, NUNCA float (ADR-008, à imagem de [budget.Amount]). A conversão
// tokens→custo é determinística e documentada em metering/cost.
//
// # Versionada e tamper-evident (ADR-011, padrão AOS-058)
//
// A tabela vive num documento VERSIONADO com um DIGEST canónico (sha256) que a
// torna tamper-evident: [Table.Version] devolve "versão#digest12". Ao contrário da
// allowlist (AOS-058), a tabela de preços NÃO é um segredo nem um gate de
// segurança — é um DADO de facturação público — pelo que NÃO exige assinatura
// ed25519/trust-anchor; a integridade tamper-evident (digest + versão selada no
// audit) basta para a reconciliação e o burn-down não serem falsificados por uma
// mudança silenciosa.
//
// # Alteração de preço = EVENTO EXPLÍCITO (nunca silenciosa)
//
// Uma alteração de preço produz uma VERSÃO NOVA e um evento explícito (ver
// change.go, [Diff] + [ChangeRecorder]): a diferença entre duas versões é selada
// no changelog WORM e emitida por um sink. Uma mudança silenciosa que falsificasse
// o burn-down/reconciliação é impossível por construção — o digest muda e a
// activação é um evento auditável.
//
// # Fail-closed (ADR-008)
//
// Um (modelo, região) SEM preço é FAIL-CLOSED: [Table.RateFor] devolve ok=false e
// a contabilidade trata-o como custo NÃO-CALCULÁVEL (erro atribuível), NUNCA 0
// silencioso — um 0 silencioso falsificaria o burn-down (uma chamada cara pareceria
// grátis). Rates negativos no documento são rejeitados no carregamento.
//
// # Zero dependências externas / determinismo
//
// Só stdlib (encoding/json, crypto/sha256). Sem relógio nem rand na decisão;
// serialização canónica estável (ordem determinista) para o digest.
package pricing

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

//go:embed pricing_table.json
var embeddedTable []byte

// Erros fail-closed do carregamento/consulta.
var (
	// ErrTableMalformed — o documento é inválido (JSON, versão vazia, entrada sem
	// modelo/região, ou rate negativo). Fail-closed: o gateway recusa arrancar sobre
	// uma tabela de preços malformada em vez de facturar com preços inválidos.
	ErrTableMalformed = errors.New("pricing: tabela de precos malformada (fail-closed)")
	// ErrNoPrice — não existe preço para o (modelo, região) pedido. Fail-closed
	// (ADR-008): o custo é NÃO-CALCULÁVEL (erro atribuível), nunca 0 silencioso — um
	// 0 falsificaria o burn-down. Comparável por errors.Is.
	ErrNoPrice = errors.New("pricing: sem preco para (modelo, regiao) (custo nao-calculavel, fail-closed)")
	// ErrDuplicateEntry — o documento tem duas entradas para o mesmo (modelo, região).
	// Fail-closed: um preço ambíguo é recusado (qual valeria?).
	ErrDuplicateEntry = errors.New("pricing: entrada de preco duplicada para (modelo, regiao) (fail-closed)")
)

// Rate são os QUATRO rates por tipo de token de um (modelo, região), em MICRO-USD
// por 1_000_000 de tokens (int64 — dinheiro nunca em float). São DISTINTOS: cache
// read é tipicamente o mais barato e cache write o mais caro (prémio de criação).
type Rate struct {
	// InputPerMTokMicroUSD é o preço do input NÃO-cacheado por 1M tokens (micro-USD).
	InputPerMTokMicroUSD int64 `json:"input_per_mtok_micro_usd"`
	// OutputPerMTokMicroUSD é o preço do output/completion por 1M tokens.
	OutputPerMTokMicroUSD int64 `json:"output_per_mtok_micro_usd"`
	// CacheReadPerMTokMicroUSD é o preço da LEITURA de cache de prefixo (desconto).
	CacheReadPerMTokMicroUSD int64 `json:"cache_read_per_mtok_micro_usd"`
	// CacheWritePerMTokMicroUSD é o preço da ESCRITA/criação de cache (prémio).
	CacheWritePerMTokMicroUSD int64 `json:"cache_write_per_mtok_micro_usd"`
}

// nonNegative indica que nenhum dos quatro rates é negativo (rate válido). Um rate
// negativo no documento é rejeitado no carregamento (fail-closed).
func (r Rate) nonNegative() bool {
	return r.InputPerMTokMicroUSD >= 0 &&
		r.OutputPerMTokMicroUSD >= 0 &&
		r.CacheReadPerMTokMicroUSD >= 0 &&
		r.CacheWritePerMTokMicroUSD >= 0
}

// Key é a chave de preço: o par (modelo, região). A região é significativa — o
// mesmo modelo pode ter preços diferentes por região/endpoint.
type Key struct {
	Model  string
	Region string
}

// Entry é uma linha do documento de preços: o (modelo, região) e os seus quatro
// rates. É a forma de serialização (JSON) da tabela.
type Entry struct {
	Model  string `json:"model"`
	Region string `json:"region"`
	Rate   Rate   `json:"rate"`
}

// Table é a tabela de preços carregada, versionada e verificada. Imutável após
// construção (os rates não mudam sob os pés de um cálculo em curso — uma alteração
// é uma Table NOVA com versão nova).
type Table struct {
	VersionTag string       `json:"version"`
	Entries    []Entry      `json:"entries"`
	byKey      map[Key]Rate // índice de consulta O(1)
	digest     string       // sha256 hex do conteúdo canónico (versão/tamper-evidence)
}

// LoadEmbedded carrega a tabela de preços EMBEBIDA (pricing_table.json) — a tabela
// de referência por omissão. Fail-closed se malformada.
func LoadEmbedded() (*Table, error) { return LoadTable(embeddedTable) }

// LoadTable desserializa e VALIDA uma tabela a partir do JSON: versão não-vazia,
// cada entrada com modelo E região não-vazios, rates não-negativos, sem (modelo,
// região) duplicado. Calcula o digest canónico (versão tamper-evident). Fail-closed
// em qualquer violação.
func LoadTable(doc []byte) (*Table, error) {
	var t Table
	if err := json.Unmarshal(doc, &t); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTableMalformed, err)
	}
	if t.VersionTag == "" {
		return nil, fmt.Errorf("%w: versao vazia", ErrTableMalformed)
	}
	t.byKey = make(map[Key]Rate, len(t.Entries))
	for _, e := range t.Entries {
		if e.Model == "" || e.Region == "" {
			return nil, fmt.Errorf("%w: entrada sem modelo/regiao", ErrTableMalformed)
		}
		if !e.Rate.nonNegative() {
			return nil, fmt.Errorf("%w: rate negativo em (%s,%s)", ErrTableMalformed, e.Model, e.Region)
		}
		k := Key{Model: e.Model, Region: e.Region}
		if _, dup := t.byKey[k]; dup {
			return nil, fmt.Errorf("%w: (%s,%s)", ErrDuplicateEntry, e.Model, e.Region)
		}
		t.byKey[k] = e.Rate
	}
	t.digest = canonicalDigest(&t)
	return &t, nil
}

// NewTable constrói uma tabela programaticamente a partir de uma versão e de um
// conjunto de entradas, aplicando a MESMA validação de [LoadTable] (rates
// não-negativos, sem duplicados). Útil para composition roots e testes sem tocar
// no ficheiro embebido.
func NewTable(version string, entries []Entry) (*Table, error) {
	doc, err := json.Marshal(Table{VersionTag: version, Entries: entries})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTableMalformed, err)
	}
	return LoadTable(doc)
}

// RateFor devolve os quatro rates do (modelo, região) e ok=true; ok=false se não
// há preço (fail-closed a montante — o chamador trata como custo não-calculável,
// nunca 0 silencioso).
func (t *Table) RateFor(model, region string) (Rate, bool) {
	r, ok := t.byKey[Key{Model: model, Region: region}]
	return r, ok
}

// Version devolve a identidade VERSIONADA e tamper-evident da tabela:
// "versão#digest12". É o valor selado no audit WORM (na activação e em cada
// registo de custo) para ligar o custo à versão EXACTA de preços em vigor — a base
// da reconciliação com a factura.
func (t *Table) Version() string { return t.VersionTag + "#" + t.digest[:12] }

// Digest devolve o digest canónico completo (sha256 hex) do conteúdo da tabela.
func (t *Table) Digest() string { return t.digest }

// Keys devolve o conjunto ORDENADO de chaves (modelo, região) com preço — usado
// pelo [Diff] para detectar entradas adicionadas/removidas entre versões.
func (t *Table) Keys() []Key {
	out := make([]Key, 0, len(t.byKey))
	for k := range t.byKey {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return out[i].Region < out[j].Region
	})
	return out
}

// canonicalDigest calcula um sha256 determinista do CONTEÚDO da tabela (versão +
// entradas normalizadas/ordenadas), independente de espaços/ordem do JSON original.
// É a base da versão tamper-evident: qualquer alteração de um rate muda o digest.
func canonicalDigest(t *Table) string {
	entries := make([]Entry, len(t.Entries))
	copy(entries, t.Entries)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Model != entries[j].Model {
			return entries[i].Model < entries[j].Model
		}
		return entries[i].Region < entries[j].Region
	})
	canon := struct {
		Version string  `json:"version"`
		Entries []Entry `json:"entries"`
	}{Version: t.VersionTag, Entries: entries}
	b, _ := json.Marshal(canon)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
