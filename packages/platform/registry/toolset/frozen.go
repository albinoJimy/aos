// Package toolset implementa o CONGELAMENTO do tool set por run (AOS-050,
// tecnica/05 §6) — a peça que concilia integridade de supply-chain com
// estabilidade de prefix cache (ADR-009).
//
// No arranque de cada run, [FreezeToolSet] resolve o conjunto COMPLETO de tools
// active a partir do REG (AOS-045/047), TODAS por versão pinada — nunca latest —,
// e captura as suas definições exactas e digests num [FrozenToolSet]: um SNAPSHOT
// IMUTÁVEL. Uma vez congelado, o conjunto NÃO muda durante a vida do run — é uma
// fotografia, não uma vista viva do REG. Mudanças posteriores no catálogo (uma
// tool nova, uma actualização de versão, uma re-aprovação) NÃO afectam runs em
// curso; só são visíveis num novo congelamento (o próximo run).
//
// O snapshot materializa-se de três formas complementares, todas deterministas e
// de ordem estável (id, version), nunca ordem de mapa:
//   - no PREFIXO IMUTÁVEL do prompt, via o PromptAssembler cache-estável de
//     AOS-013/037 ([FrozenToolSet.Assembler]) — byte-idêntico durante todo o run;
//   - no MANIFESTO de dependências da trajectória ([FrozenToolSet.ApplyToManifest]),
//     reutilizando registry.ManifestDeps (versões+digests, base do replay fiel,
//     ADR-012);
//   - numa API de consulta de digest esperado por tool id
//     ([FrozenToolSet.ExpectedDigest]) — a ENTRADA da revalidação criptográfica
//     por chamada (AOS-051), que este pacote NÃO implementa.
//
// É um pacote de composição fino: NÃO reimplementa o REG, o manifesto nem o
// PromptAssembler — projecta o snapshot congelado sobre eles.
package toolset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/registry"
	"github.com/aos-ref/platform/registry/domain"
)

// opFreeze é o nome do span OTel da resolução+congelamento do tool set.
const opFreeze = "registry.freeze_toolset"

// AttrToolSetHash — hash estável do conjunto congelado (sha256 das identidades
// pinadas na ordem congelada). Público: são ids/versions/digests, nunca segredos.
// É a testemunha da imutabilidade do snapshot (constante durante o run).
const AttrToolSetHash = "aos.registry.toolset_hash"

// toolSetHashPrefix é o prefixo dos hashes emitidos (coerente com o resto do repo).
const toolSetHashPrefix = "sha256:"

// Catalog é a porta MÍNIMA que o congelamento consome do REG (AOS-045/047): a
// enumeração ATÓMICA das entradas active e íntegras no instante do arranque do
// run. *registry.Registry satisfá-la via ActiveEntries. Mantê-la mínima desacopla
// o congelamento da superfície completa do REG e permite fakes deterministas em
// teste.
type Catalog interface {
	// ActiveEntries devolve o snapshot (uma fotografia atómica) das entradas
	// active e íntegras, cada uma por versão pinada. Ver
	// registry.Registry.ActiveEntries.
	ActiveEntries(ctx context.Context) ([]domain.Entry, error)
}

// Selector restringe (opcionalmente) o congelamento a um subconjunto de ids. Nil
// ou vazio = TODO o conjunto active (o caso por omissão do arranque de um run). Um
// selector só RESTRINGE, nunca ADICIONA: um id que não esteja active é ignorado
// (default-deny — só tools active/admissíveis entram). Serve casos em que uma
// política de run limita a superfície a tools específicas, sem nunca contornar o
// gate de admissão do REG.
type Selector struct {
	// IDs é o conjunto de ids admitidos. Vazio significa "todos os active".
	IDs []string
}

// Expectation é a EXPECTATIVA imutável de uma tool congelada: a sua identidade
// pinada (id, version, kind) e o digest ESPERADO. É a ENTRADA da revalidação
// criptográfica por chamada (AOS-051) — o digest contra o qual a definição prestes
// a executar será comparada. AOS-050 apenas PRODUZ e CONSULTA a expectativa; a
// comparação por chamada é AOS-051 e NÃO é feita aqui.
type Expectation struct {
	ID      string
	Version domain.Version
	Kind    domain.ArtifactKind
	Digest  string
}

// FrozenToolSet é o SNAPSHOT IMUTÁVEL do tool set de um run. Captura, no instante
// do arranque, as definições exactas e os digests de cada tool active. Todos os
// campos são privados e os acessores devolvem SEMPRE cópias/clones: nenhum
// chamador pode mutar o snapshot depois de congelado. É estruturalmente imune a
// mudanças posteriores no REG — não guarda qualquer referência viva ao catálogo.
type FrozenToolSet struct {
	runID    string
	frozenAt string                  // instante do congelamento (RFC3339Nano; injectável)
	entries  []domain.Entry          // definições exactas (clones), ordem estável (id, version)
	specs    []agentruntime.ToolSpec // projecção para o prefixo imutável (mesma ordem)
	byID     map[string]Expectation  // consulta O(1) de expectativa por id (AOS-051)
	hash     string                  // sha256 estável do conjunto congelado
}

// freezer é o estado de configuração (injectável) do congelamento.
type freezer struct {
	tracer agentruntime.Tracer
	now    func() time.Time
}

// Option configura [FreezeToolSet].
type Option func(*freezer)

// WithTracer injecta a porta de observabilidade (spans OTel). Por omissão
// NoopTracer. Um valor nil é ignorado.
func WithTracer(t agentruntime.Tracer) Option {
	return func(f *freezer) {
		if t != nil {
			f.tracer = t
		}
	}
}

// WithClock injecta o relógio do timestamp de congelamento (determinismo em
// testes; nunca time.Now numa decisão). Um valor nil é ignorado.
func WithClock(now func() time.Time) Option {
	return func(f *freezer) {
		if now != nil {
			f.now = now
		}
	}
}

// FreezeToolSet resolve o conjunto COMPLETO de tools active a partir do catálogo
// (todas por versão pinada) e devolve o [FrozenToolSet] imutável do run. É a
// operação do ARRANQUE do run: chamada UMA vez, o seu resultado acompanha o run
// durante toda a sua vida e nunca é recalculado a meio (recalcular a meio veria o
// REG evoluído e quebraria tanto o pinning de supply-chain como a estabilidade do
// prefixo).
//
// Fail-closed: cat/runID em falta são recusados; um id com múltiplas versões
// active torna a superfície ambígua e é recusado ([ErrAmbiguousToolID]); uma falha
// de integridade na enumeração (digest divergente) propaga-se do catálogo e aborta
// o congelamento. A ordem congelada é estável (id, version), nunca ordem de mapa.
func FreezeToolSet(ctx context.Context, cat Catalog, runID string, sel *Selector, opts ...Option) (*FrozenToolSet, error) {
	if cat == nil {
		return nil, ErrNoCatalog
	}
	if runID == "" {
		return nil, ErrNoRunID
	}
	fz := &freezer{
		tracer: agentruntime.NoopTracer{},
		// Relógio por omissão determinista (zero-value): o timestamp de
		// congelamento é observacional, nunca entra numa decisão. Produção injecta
		// um relógio real via WithClock.
		now: func() time.Time { return time.Time{} },
	}
	for _, o := range opts {
		o(fz)
	}

	ctx, span := fz.tracer.StartSpan(ctx, opFreeze)
	defer span.End()
	span.SetAttribute(agentruntime.AttrRunID, runID)

	active, err := cat.ActiveEntries(ctx)
	if err != nil {
		span.SetAttribute(registry.AttrDecision, "error")
		return nil, err
	}

	// Filtro do selector (opcional; só restringe) e reordenação estável. O
	// congelamento NÃO confia na ordem do fornecedor — reordena por (id, version),
	// tornando a superfície byte-idêntica independentemente da ordem de entrega.
	entries := filterSelector(active, sel)
	sortEntries(entries)

	specs := make([]agentruntime.ToolSpec, len(entries))
	byID := make(map[string]Expectation, len(entries))
	for i, e := range entries {
		if _, dup := byID[e.ID]; dup {
			span.SetAttribute(agentruntime.AttrToolName, e.ID)
			span.SetAttribute(registry.AttrDecision, "ambiguous")
			return nil, fmt.Errorf("%w: %s", ErrAmbiguousToolID, e.ID)
		}
		spec := agentruntime.ToolSpec{
			Name:      e.ID,
			Version:   e.Version.String(),
			Digest:    e.Digest,
			MCPServer: mcpServer(e),
		}
		// Fronteira de serialização (AOS-050-Q1): os campos projectados alimentam
		// serializers baseados em DELIMITADORES por byte a jusante (computeHash:
		// \0/\x1e; buildPrefix: \t/\n). Um byte de controlo num campo tornaria essas
		// fronteiras ambíguas e quebraria tanto a injectividade do toolset_hash como
		// a estabilidade byte-a-byte do prefixo imutável. Impomos AQUI, fail-closed,
		// a invariante de charset de que essas garantias dependem — em vez de a
		// assumir de um gate de admissão a montante.
		if field := firstControlByteField(spec); field != "" {
			span.SetAttribute(agentruntime.AttrToolName, e.ID)
			span.SetAttribute(registry.AttrDecision, "control_byte")
			return nil, fmt.Errorf("%w: campo %s de %q", ErrControlByte, field, e.ID)
		}
		specs[i] = spec
		byID[e.ID] = Expectation{
			ID:      e.ID,
			Version: e.Version,
			Kind:    e.Kind,
			Digest:  e.Digest,
		}
	}

	f := &FrozenToolSet{
		runID:    runID,
		frozenAt: fz.now().UTC().Format(time.RFC3339Nano),
		entries:  entries,
		specs:    specs,
		byID:     byID,
	}
	f.hash = computeHash(specs)

	span.SetAttribute(registry.AttrToolSetSize, len(entries))
	span.SetAttribute(AttrToolSetHash, f.hash)
	span.SetAttribute(registry.AttrDecision, "frozen")
	return f, nil
}

// RunID devolve o id do run a que este snapshot pertence.
func (f *FrozenToolSet) RunID() string { return f.runID }

// FrozenAt devolve o instante de congelamento (RFC3339Nano). Observacional.
func (f *FrozenToolSet) FrozenAt() string { return f.frozenAt }

// Len devolve o número de tools congeladas.
func (f *FrozenToolSet) Len() int { return len(f.entries) }

// Hash devolve o hash estável do conjunto congelado ("sha256:<hex>"). É constante
// durante todo o run (o snapshot é imutável) — a testemunha de que nenhuma mudança
// no REG alterou a superfície deste run.
func (f *FrozenToolSet) Hash() string { return f.hash }

// IDs devolve os ids das tools congeladas, na ordem estável congelada (cópia).
func (f *FrozenToolSet) IDs() []string {
	out := make([]string, len(f.entries))
	for i, e := range f.entries {
		out[i] = e.ID
	}
	return out
}

// ExpectedDigest devolve o digest ESPERADO da tool id congelada e um booleano de
// presença. É a API de consulta que a revalidação por chamada (AOS-051) usa: a
// expectativa contra a qual cada digest observado será comparado. Um id fora do
// conjunto congelado devolve ("", false) — default-deny (uma tool que não está na
// superfície congelada do run não tem expectativa e não deve executar).
func (f *FrozenToolSet) ExpectedDigest(id string) (string, bool) {
	e, ok := f.byID[id]
	if !ok {
		return "", false
	}
	return e.Digest, true
}

// Expectation devolve a expectativa completa (id, version, kind, digest) da tool
// id congelada e um booleano de presença. Ver [ExpectedDigest].
func (f *FrozenToolSet) Expectation(id string) (Expectation, bool) {
	e, ok := f.byID[id]
	return e, ok
}

// Specs devolve uma CÓPIA das identidades pinadas na ordem congelada — a projecção
// exacta que alimenta o prefixo imutável do prompt. A cópia impede a mutação do
// snapshot pelo chamador.
func (f *FrozenToolSet) Specs() []agentruntime.ToolSpec {
	out := make([]agentruntime.ToolSpec, len(f.specs))
	copy(out, f.specs)
	return out
}

// Entries devolve CLONES das entradas de catálogo congeladas (definições exactas),
// na ordem congelada. Clones profundos: o chamador não pode mutar o snapshot.
func (f *FrozenToolSet) Entries() []domain.Entry {
	out := make([]domain.Entry, len(f.entries))
	for i, e := range f.entries {
		out[i] = e.Clone()
	}
	return out
}

// Assembler congela o PREFIXO IMUTÁVEL do prompt (system + tool set congelado) a
// partir deste snapshot, reutilizando o PromptAssembler cache-estável de
// AOS-013/037 (ADR-009). O prefixo resultante é byte-idêntico durante toda a vida
// do run — construído UMA vez, nunca reordenado nem mutado —, preservando o
// cache-hit-rate (SLI de AOS-037). A ordem das tools é a ordem ESTÁVEL congelada
// (id, version), nunca a ordem de mapa. system é o system prompt do run.
func (f *FrozenToolSet) Assembler(system string) *agentruntime.PromptAssembler {
	return agentruntime.NewPromptAssembler(system, f.Specs())
}

// ApplyToManifest grava o tool set congelado (versões+digests) no manifesto de
// dependências da trajectória (ADR-012), devolvendo uma CÓPIA do manifesto dado
// com os campos Tools/Skills preenchidos a partir do snapshot. NÃO muta o
// argumento (imutabilidade). Reutiliza registry.ManifestDeps — não reimplementa o
// manifesto. Os skills vão a Skills; tools e servidores MCP vão a Tools (um
// servidor MCP transporta tools e pina-se do mesmo modo, com a sua origem).
func (f *FrozenToolSet) ApplyToManifest(m agentruntime.Manifest) agentruntime.Manifest {
	var tools, skills []domain.Entry
	for _, e := range f.entries {
		if e.Kind == domain.KindSkill {
			skills = append(skills, e)
		} else {
			tools = append(tools, e)
		}
	}
	m.Tools = registry.ManifestDeps(tools)
	m.Skills = registry.ManifestDeps(skills)
	return m
}

// ManifestDeps devolve o tool set congelado inteiro como dependências pinadas do
// manifesto, na ordem congelada (tools+skills+servidores juntos). Conveniência
// para quem grava um manifesto plano; ApplyToManifest separa por natureza.
func (f *FrozenToolSet) ManifestDeps() []agentruntime.PinnedDep {
	return registry.ManifestDeps(f.entries)
}

// --- helpers deterministas -------------------------------------------------

// filterSelector aplica o selector (opcional) sobre o snapshot active. Devolve
// SEMPRE uma nova slice (nunca partilha o array do fornecedor). Nil/vazio = tudo.
func filterSelector(entries []domain.Entry, sel *Selector) []domain.Entry {
	if sel == nil || len(sel.IDs) == 0 {
		out := make([]domain.Entry, len(entries))
		copy(out, entries)
		return out
	}
	want := make(map[string]bool, len(sel.IDs))
	for _, id := range sel.IDs {
		want[id] = true
	}
	out := make([]domain.Entry, 0, len(entries))
	for _, e := range entries {
		if want[e.ID] {
			out = append(out, e)
		}
	}
	return out
}

// sortEntries ordena por (id, version) crescente — ordenação total determinística.
// É a ordem estável que o prefixo imutável e o manifesto congelam.
func sortEntries(es []domain.Entry) {
	sort.Slice(es, func(i, j int) bool {
		if es[i].ID != es[j].ID {
			return es[i].ID < es[j].ID
		}
		return es[i].Version.Less(es[j].Version)
	})
}

// mcpServer devolve a origem do servidor MCP para desambiguar a proveniência do
// transporte (só para KindMCPServer; vazio para tool/skill). Espelha a regra de
// registry.PinnedDep.
func mcpServer(e domain.Entry) string {
	if e.Kind == domain.KindMCPServer {
		return e.Provenance.Origin
	}
	return ""
}

// firstControlByteField devolve o nome do primeiro campo de charset LIVRE do spec
// que contém um byte de controlo (ver [hasControlByte]), ou "" se todos forem
// limpos. A ordem de verificação é determinista (id, digest, mcp_server) para uma
// mensagem de erro estável. Version NÃO é verificado: Version.String() é derivado
// de campos inteiros (SemVer numérico) e é estruturalmente incapaz de conter um
// byte de controlo — verificá-lo seria código morto. id, digest e a origem do
// servidor MCP são strings de charset não imposto a montante, pelo que é aqui, no
// congelamento, que a invariante de fronteira dos serializers é garantida.
func firstControlByteField(s agentruntime.ToolSpec) string {
	switch {
	case hasControlByte(s.Name):
		return "id"
	case hasControlByte(s.Digest):
		return "digest"
	case hasControlByte(s.MCPServer):
		return "mcp_server"
	default:
		return ""
	}
}

// hasControlByte reporta se s contém um byte de controlo C0 (< 0x20) ou DEL
// (0x7f). Esse conjunto cobre TODOS os delimitadores usados a jusante — \0 e \x1e
// (computeHash) e \t (0x09) e \n (0x0a) (buildPrefix) —, pelo que rejeitá-lo
// garante que nenhum campo projectado pode injectar uma fronteira ambígua. Opera
// ao nível do byte (não da rune) porque é ao nível do byte que os serializers
// delimitam.
func hasControlByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// computeHash calcula um hash estável do conjunto congelado a partir das
// identidades pinadas na ordem dada. Campos separados por bytes de controlo (\0 /
// \x1e) para que fronteiras ambíguas não colidam; determinista e independente de
// mapas. É a testemunha byte-a-byte da imutabilidade do snapshot.
func computeHash(specs []agentruntime.ToolSpec) string {
	h := sha256.New()
	for _, s := range specs {
		h.Write([]byte(s.Name))
		h.Write([]byte{0})
		h.Write([]byte(s.Version))
		h.Write([]byte{0})
		h.Write([]byte(s.Digest))
		h.Write([]byte{0})
		h.Write([]byte(s.MCPServer))
		h.Write([]byte{0x1e})
	}
	return toolSetHashPrefix + hex.EncodeToString(h.Sum(nil))
}
