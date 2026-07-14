package toolset

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/registry"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/substrate/eventstore"
)

// --- helpers deterministas -------------------------------------------------

func ver(mj, mn, p int) domain.Version { return domain.Version{Major: mj, Minor: mn, Patch: p} }

func fixedClock() func() time.Time {
	base := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	return func() time.Time { return base }
}

// allowAdmit é o AdmissionVerifier fail-open dos testes (satisfaz a interface
// pública do REG): admite qualquer promoção a active para exercitar o ciclo de vida
// sem assinatura real.
type allowAdmit struct{}

func (allowAdmit) Verify(context.Context, domain.Entry) error { return nil }

// newRegistry constrói um REG real sobre um Event Store em memória, com relógio
// determinista e admissão fail-open (testes não exercitam assinatura).
func newRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reg, err := registry.New(store,
		registry.WithClock(fixedClock()),
		registry.WithAdmissionVerifier(allowAdmit{}),
	)
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	return reg
}

func publishActive(t *testing.T, reg *registry.Registry, id string, v domain.Version, kind domain.ArtifactKind) {
	t.Helper()
	ctx := context.Background()
	req := registry.PublishRequest{
		ID: id, Version: v, Kind: kind,
		Origin: "mcp://" + id, Publisher: "pub:test",
		Contract: domain.Contract{Egress: domain.EgressInternal},
	}
	if _, err := reg.Publish(ctx, req); err != nil {
		t.Fatalf("Publish(%s@%s): %v", id, v, err)
	}
	if _, err := reg.SetStatus(ctx, id, v, domain.StatusActive); err != nil {
		t.Fatalf("SetStatus(%s@%s active): %v", id, v, err)
	}
}

func deprecate(t *testing.T, reg *registry.Registry, id string, v domain.Version) {
	t.Helper()
	if _, err := reg.SetStatus(context.Background(), id, v, domain.StatusDeprecated); err != nil {
		t.Fatalf("SetStatus(%s@%s deprecated): %v", id, v, err)
	}
}

// fakeCatalog é uma porta Catalog controlada para os casos de fronteira (ambiguidade,
// propagação de erro, ordem de entrega arbitrária) sem depender do REG real.
type fakeCatalog struct {
	entries []domain.Entry
	err     error
}

func (f fakeCatalog) ActiveEntries(context.Context) ([]domain.Entry, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]domain.Entry, len(f.entries))
	copy(out, f.entries)
	return out, nil
}

func fakeEntry(id string, v domain.Version, kind domain.ArtifactKind, digest string) domain.Entry {
	return domain.Entry{
		ID: id, Version: v, Kind: kind, Digest: digest,
		Contract:   domain.Contract{Egress: domain.EgressInternal},
		Provenance: domain.Provenance{Origin: "mcp://" + id, Trust: domain.TrustFirstSeen},
		Status:     domain.StatusActive,
	}
}

// --- Domínio: IMUNIDADE a mudanças no REG a meio de um run -----------------

// TestFreeze_ImmuneToMidRunChanges é o teste central de AOS-050 (critério de
// aceitação + DoD): adicionar/actualizar uma tool no REG A MEIO de um run NÃO altera
// o tool set desse run; o run SEGUINTE vê-a. Prova a imunidade do snapshot congelado
// a mudanças do catálogo posteriores ao arranque.
func TestFreeze_ImmuneToMidRunChanges(t *testing.T) {
	t.Parallel()
	reg := newRegistry(t)
	ctx := context.Background()
	v1 := ver(1, 0, 0)

	// Arranque do run-1: A@1.0.0 e B@1.0.0 active.
	publishActive(t, reg, "tool.a", v1, domain.KindTool)
	publishActive(t, reg, "tool.b", v1, domain.KindTool)

	run1, err := FreezeToolSet(ctx, reg, "run-1", nil, WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("FreezeToolSet(run-1): %v", err)
	}
	// Testemunhas capturadas ANTES de qualquer mudança no REG.
	hashBefore := run1.Hash()
	idsBefore := run1.IDs()
	digestABefore, _ := run1.ExpectedDigest("tool.a")
	prefixBefore := run1.Assembler("SYS").PrefixHash()

	if len(idsBefore) != 2 || idsBefore[0] != "tool.a" || idsBefore[1] != "tool.b" {
		t.Fatalf("run-1 IDs = %v, quer [tool.a tool.b]", idsBefore)
	}
	expA, _ := run1.Expectation("tool.a")
	if !expA.Version.Equal(v1) {
		t.Fatalf("run-1 tool.a versão congelada = %v, quer 1.0.0", expA.Version)
	}

	// --- MUDANÇAS NO REG A MEIO DO RUN ---
	// (1) tool NOVA: C@1.0.0 active.
	publishActive(t, reg, "tool.c", v1, domain.KindTool)
	// (2) ACTUALIZAÇÃO de A: depreca 1.0.0 e activa 2.0.0.
	v2 := ver(2, 0, 0)
	deprecate(t, reg, "tool.a", v1)
	publishActive(t, reg, "tool.a", v2, domain.KindTool)

	// run-1 continua IMUTÁVEL: mesmas testemunhas, lidas DEPOIS das mudanças.
	if got := run1.Hash(); got != hashBefore {
		t.Fatalf("run-1 Hash mudou a meio do run: %s != %s", got, hashBefore)
	}
	if got := run1.IDs(); len(got) != 2 || got[0] != "tool.a" || got[1] != "tool.b" {
		t.Fatalf("run-1 IDs mudaram a meio do run: %v", got)
	}
	if got, _ := run1.ExpectedDigest("tool.a"); got != digestABefore {
		t.Fatalf("run-1 digest de tool.a mudou: %s != %s", got, digestABefore)
	}
	if exp, _ := run1.Expectation("tool.a"); !exp.Version.Equal(v1) {
		t.Fatalf("run-1 tool.a ainda devia ser 1.0.0, got %v", exp.Version)
	}
	if got := run1.Assembler("SYS").PrefixHash(); got != prefixBefore {
		t.Fatalf("run-1 prefixo imutável mudou a meio do run: %s != %s", got, prefixBefore)
	}
	// tool.c NÃO existe no run-1.
	if _, ok := run1.ExpectedDigest("tool.c"); ok {
		t.Fatalf("run-1 não devia ver a tool.c adicionada a meio do run")
	}

	// O run SEGUINTE (run-2) VÊ as mudanças: C nova + A@2.0.0 (A@1.0.0 deprecada sai).
	run2, err := FreezeToolSet(ctx, reg, "run-2", nil, WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("FreezeToolSet(run-2): %v", err)
	}
	ids2 := run2.IDs()
	if len(ids2) != 3 || ids2[0] != "tool.a" || ids2[1] != "tool.b" || ids2[2] != "tool.c" {
		t.Fatalf("run-2 IDs = %v, quer [tool.a tool.b tool.c]", ids2)
	}
	if exp, _ := run2.Expectation("tool.a"); !exp.Version.Equal(v2) {
		t.Fatalf("run-2 tool.a versão = %v, quer 2.0.0 (viu a actualização)", exp.Version)
	}
	if run2.Hash() == hashBefore {
		t.Fatalf("run-2 devia ter um tool set diferente do run-1")
	}
}

// --- Todas PINADAS; só ACTIVE entram --------------------------------------

// TestFreeze_PinnedActiveOnly prova que o congelamento resolve TODAS as tools por
// versão pinada (nunca latest) e que só entram tools active — staging fica de fora.
func TestFreeze_PinnedActiveOnly(t *testing.T) {
	t.Parallel()
	reg := newRegistry(t)
	ctx := context.Background()
	v := ver(1, 2, 0)

	publishActive(t, reg, "tool.http", v, domain.KindTool)
	publishActive(t, reg, "skill.sum", ver(2, 0, 0), domain.KindSkill)
	publishActive(t, reg, "mcp.fs", ver(1, 0, 0), domain.KindMCPServer)
	// staging (publicado, nunca promovido) — não entra.
	if _, err := reg.Publish(ctx, registry.PublishRequest{
		ID: "tool.staged", Version: v, Kind: domain.KindTool,
		Origin: "self", Publisher: "p", Contract: domain.Contract{Egress: domain.EgressNone},
	}); err != nil {
		t.Fatalf("Publish(staged): %v", err)
	}

	f, err := FreezeToolSet(ctx, reg, "run-x", nil)
	if err != nil {
		t.Fatalf("FreezeToolSet: %v", err)
	}
	if f.Len() != 3 {
		t.Fatalf("Len=%d (%v), quer 3 (staging excluída)", f.Len(), f.IDs())
	}
	if _, ok := f.ExpectedDigest("tool.staged"); ok {
		t.Fatalf("tool.staged (staging) não devia estar congelada")
	}
	for _, s := range f.Specs() {
		if s.Version == "" || s.Version == "latest" {
			t.Fatalf("spec %s versão não pinada: %q", s.Name, s.Version)
		}
		if s.Digest == "" {
			t.Fatalf("spec %s sem digest", s.Name)
		}
	}
	// tool.http pinada exactamente em 1.2.0.
	if exp, _ := f.Expectation("tool.http"); exp.Version.String() != "1.2.0" {
		t.Fatalf("tool.http pinada em %s, quer 1.2.0", exp.Version.String())
	}
	// o servidor MCP carrega a sua origem.
	if exp, _ := f.Expectation("mcp.fs"); exp.Kind != domain.KindMCPServer {
		t.Fatalf("mcp.fs kind=%s", exp.Kind)
	}
}

// --- Integração: gravado no MANIFESTO de dependências ---------------------

// TestFreeze_RecordedInManifest prova que o tool set congelado (versões+digests) é
// gravado no manifesto de dependências da trajectória, separando tools/servidores de
// skills, com os pares (name, version, digest) exactos do snapshot.
func TestFreeze_RecordedInManifest(t *testing.T) {
	t.Parallel()
	reg := newRegistry(t)
	ctx := context.Background()

	publishActive(t, reg, "tool.http", ver(1, 2, 0), domain.KindTool)
	publishActive(t, reg, "skill.sum", ver(2, 1, 0), domain.KindSkill)
	publishActive(t, reg, "mcp.fs", ver(1, 0, 0), domain.KindMCPServer)

	f, err := FreezeToolSet(ctx, reg, "run-m", nil)
	if err != nil {
		t.Fatalf("FreezeToolSet: %v", err)
	}

	base := agentruntime.Manifest{PromptHash: "sha256:abc", Model: agentruntime.ModelManifest{ModelID: "m"}}
	m := f.ApplyToManifest(base)

	// ApplyToManifest não muta o argumento.
	if base.Tools != nil || base.Skills != nil {
		t.Fatalf("ApplyToManifest mutou o argumento base")
	}
	// Skills separados dos tools.
	if len(m.Skills) != 1 || m.Skills[0].Name != "skill.sum" {
		t.Fatalf("m.Skills = %+v, quer [skill.sum]", m.Skills)
	}
	if m.Skills[0].Version != "2.1.0" || m.Skills[0].Digest == "" {
		t.Fatalf("skill sem version/digest pinado: %+v", m.Skills[0])
	}
	// tools + servidores MCP em Tools.
	if len(m.Tools) != 2 {
		t.Fatalf("m.Tools = %+v, quer 2 (tool + mcp)", m.Tools)
	}
	byName := map[string]agentruntime.PinnedDep{}
	for _, d := range m.Tools {
		byName[d.Name] = d
	}
	if d := byName["tool.http"]; d.Version != "1.2.0" || d.Digest == "" {
		t.Fatalf("tool.http no manifesto = %+v, quer version 1.2.0 + digest", d)
	}
	if d := byName["mcp.fs"]; d.MCPServer == "" || d.Version != "1.0.0" {
		t.Fatalf("mcp.fs no manifesto = %+v, quer MCPServer preenchido + version 1.0.0", d)
	}

	// A projecção plana (ManifestDeps) inclui todo o conjunto congelado.
	if flat := f.ManifestDeps(); len(flat) != 3 {
		t.Fatalf("ManifestDeps plano = %d, quer 3", len(flat))
	}
	// Entries devolve clones independentes (mutar não vaza para o snapshot).
	ents := f.Entries()
	if len(ents) != 3 {
		t.Fatalf("Entries = %d, quer 3", len(ents))
	}
	ents[0].ID = "MUTATED"
	if f.Entries()[0].ID == "MUTATED" {
		t.Fatalf("mutação de Entries() vazou para o snapshot")
	}
}

// --- Cache: prefixo imutável BYTE-IDÊNTICO ao longo do run -----------------

// TestFreeze_PrefixByteIdenticalAcrossRun prova o SLI de estabilidade de prefix
// cache (ADR-009/AOS-037): o prefixo imutável materializado a partir do tool set
// congelado é byte-idêntico entre turnos do run (hash constante), e mantém-se
// idêntico mesmo depois de o REG mudar a meio do run.
func TestFreeze_PrefixByteIdenticalAcrossRun(t *testing.T) {
	t.Parallel()
	reg := newRegistry(t)
	ctx := context.Background()

	publishActive(t, reg, "tool.a", ver(1, 0, 0), domain.KindTool)
	publishActive(t, reg, "tool.b", ver(1, 0, 0), domain.KindTool)

	f, err := FreezeToolSet(ctx, reg, "run-p", nil)
	if err != nil {
		t.Fatalf("FreezeToolSet: %v", err)
	}
	asm := f.Assembler("SYSTEM PROMPT")

	// Vários turnos com tail crescente: o PREFIXO nunca muda.
	view1 := asm.Assemble(1, []agentruntime.TailSegment{{Kind: agentruntime.TailObjective, Content: []byte("go")}})
	view2 := asm.Assemble(2, []agentruntime.TailSegment{
		{Kind: agentruntime.TailObjective, Content: []byte("go")},
		{Kind: agentruntime.TailToolResult, Content: []byte("result")},
	})
	if !bytes.Equal(view1.Prefix, view2.Prefix) {
		t.Fatalf("prefixo divergiu entre turnos (regressão de cache)")
	}
	if view1.PrefixHash != view2.PrefixHash {
		t.Fatalf("PrefixHash divergiu entre turnos: %s != %s", view1.PrefixHash, view2.PrefixHash)
	}
	// O hash do prefixo materializado é o mesmo que o do tail crescente NÃO alterou.
	if view1.PromptHash == view2.PromptHash {
		t.Fatalf("PromptHash devia mudar (tail cresce), mas o prefixo não")
	}

	// Hash do conjunto congelado constante entre chamadas (imutável).
	h1 := f.Hash()
	h2 := f.Hash()
	if h1 == "" || h1 != h2 {
		t.Fatalf("Hash do tool set não é estável: %q vs %q", h1, h2)
	}
	// Identidade e timestamp do snapshot expostos e estáveis.
	if f.RunID() != "run-p" {
		t.Fatalf("RunID = %q, quer run-p", f.RunID())
	}
	if f.FrozenAt() == "" {
		t.Fatalf("FrozenAt vazio")
	}
	prefixHashBefore := view1.PrefixHash

	// Mudança no REG a meio do run: NÃO afecta o prefixo derivado do snapshot.
	publishActive(t, reg, "tool.z", ver(9, 9, 9), domain.KindTool)
	after := f.Assembler("SYSTEM PROMPT").Assemble(3, nil)
	if after.PrefixHash != prefixHashBefore {
		t.Fatalf("prefixo mudou após alteração do REG: %s != %s", after.PrefixHash, prefixHashBefore)
	}
}

// --- Determinismo: ordem de entrega não altera o snapshot ------------------

// TestFreeze_DeterministicOrder prova que a ordem congelada (e o hash) são
// independentes da ordem em que o catálogo entrega as entradas — reordena sempre por
// (id, version). Congela duas vezes com ordens de entrega opostas e compara.
func TestFreeze_DeterministicOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := fakeEntry("tool.a", ver(1, 0, 0), domain.KindTool, "sha256:aaa")
	b := fakeEntry("tool.b", ver(1, 0, 0), domain.KindTool, "sha256:bbb")
	c := fakeEntry("tool.c", ver(1, 0, 0), domain.KindTool, "sha256:ccc")

	f1, err := FreezeToolSet(ctx, fakeCatalog{entries: []domain.Entry{a, b, c}}, "r", nil)
	if err != nil {
		t.Fatalf("freeze1: %v", err)
	}
	f2, err := FreezeToolSet(ctx, fakeCatalog{entries: []domain.Entry{c, b, a}}, "r", nil)
	if err != nil {
		t.Fatalf("freeze2: %v", err)
	}
	if f1.Hash() != f2.Hash() {
		t.Fatalf("hash depende da ordem de entrega: %s != %s", f1.Hash(), f2.Hash())
	}
	wantIDs := []string{"tool.a", "tool.b", "tool.c"}
	for i, id := range f2.IDs() {
		if id != wantIDs[i] {
			t.Fatalf("ordem congelada não estável: %v", f2.IDs())
		}
	}
}

// --- Selector: restringe, nunca adiciona ----------------------------------

func TestFreeze_Selector(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	entries := []domain.Entry{
		fakeEntry("tool.a", ver(1, 0, 0), domain.KindTool, "sha256:a"),
		fakeEntry("tool.b", ver(1, 0, 0), domain.KindTool, "sha256:b"),
		fakeEntry("tool.c", ver(1, 0, 0), domain.KindTool, "sha256:c"),
	}
	cat := fakeCatalog{entries: entries}

	// Restringe a {a, c} — e um id inexistente (ignorado, nunca adiciona).
	f, err := FreezeToolSet(ctx, cat, "r", &Selector{IDs: []string{"tool.a", "tool.c", "tool.absent"}})
	if err != nil {
		t.Fatalf("FreezeToolSet: %v", err)
	}
	if got := f.IDs(); len(got) != 2 || got[0] != "tool.a" || got[1] != "tool.c" {
		t.Fatalf("selector IDs = %v, quer [tool.a tool.c]", got)
	}
	// Selector vazio = tudo.
	all, err := FreezeToolSet(ctx, cat, "r", &Selector{})
	if err != nil {
		t.Fatalf("FreezeToolSet(empty selector): %v", err)
	}
	if all.Len() != 3 {
		t.Fatalf("selector vazio devia congelar tudo, got %d", all.Len())
	}
}

// --- Fronteiras fail-closed ------------------------------------------------

func TestFreeze_FailClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sentinel := errors.New("boom do catálogo")

	tests := []struct {
		name    string
		cat     Catalog
		runID   string
		wantErr error
	}{
		{"catalogo nil", nil, "r", ErrNoCatalog},
		{"runID vazio", fakeCatalog{}, "", ErrNoRunID},
		{"erro do catalogo propaga", fakeCatalog{err: sentinel}, "r", sentinel},
		{
			"id ambiguo (2 versoes active)",
			fakeCatalog{entries: []domain.Entry{
				fakeEntry("tool.a", ver(1, 0, 0), domain.KindTool, "sha256:1"),
				fakeEntry("tool.a", ver(2, 0, 0), domain.KindTool, "sha256:2"),
			}},
			"r",
			ErrAmbiguousToolID,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := FreezeToolSet(ctx, tc.cat, tc.runID, nil)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("erro = %v, quer %v", err, tc.wantErr)
			}
		})
	}
}

// --- Fronteira de serialização: bytes de controlo recusados ----------------

// TestFreeze_RejectsControlBytes prova a invariante de fronteira de AOS-050-Q1: um
// campo projectado (id ou origem de servidor MCP) que contenha um byte de controlo
// — o conjunto que inclui os delimitadores \0/\x1e do toolset_hash e \t/\n do
// prefixo imutável — é RECUSADO no congelamento (ErrControlByte). Sem isto, duas
// identidades pinadas distintas poderiam colidir no mesmo hash ou fundir linhas no
// prefixo byte-idêntico; com isto, a injectividade e a estabilidade byte-a-byte
// deixam de depender de uma invariante de charset não imposta.
func TestFreeze_RejectsControlBytes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mcpBadOrigin := fakeEntry("mcp.fs", ver(1, 0, 0), domain.KindMCPServer, "sha256:1")
	mcpBadOrigin.Provenance.Origin = "mcp://a\x1eb" // \x1e é o delimitador de registo do hash

	bad := map[string]domain.Entry{
		"id com NUL":              fakeEntry("tool.a\x00b", ver(1, 0, 0), domain.KindTool, "sha256:1"),
		"id com TAB":              fakeEntry("tool.a\tb", ver(1, 0, 0), domain.KindTool, "sha256:1"),
		"id com newline":          fakeEntry("tool.a\nb", ver(1, 0, 0), domain.KindTool, "sha256:1"),
		"id com RS 0x1e":          fakeEntry("tool.a\x1eb", ver(1, 0, 0), domain.KindTool, "sha256:1"),
		"id com DEL 0x7f":         fakeEntry("tool.a\x7fb", ver(1, 0, 0), domain.KindTool, "sha256:1"),
		"digest com controlo":     fakeEntry("tool.a", ver(1, 0, 0), domain.KindTool, "sha256:a\x00b"),
		"origem MCP com controlo": mcpBadOrigin,
	}
	for name, e := range bad {
		e := e
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := FreezeToolSet(ctx, fakeCatalog{entries: []domain.Entry{e}}, "r", nil)
			if !errors.Is(err, ErrControlByte) {
				t.Fatalf("erro = %v, quer ErrControlByte", err)
			}
		})
	}

	// Concretamente: as DUAS identidades que colidiriam sob delimitadores ingénuos
	// ({id:"a\x00b", mcp:""} vs {id:"a", ...campo seguinte "b"}) são ambas recusadas
	// no congelamento — a colisão nunca chega a computeHash.
	if _, err := FreezeToolSet(ctx, fakeCatalog{entries: []domain.Entry{
		fakeEntry("a\x00b", ver(1, 0, 0), domain.KindTool, "sha256:x"),
	}}, "r", nil); !errors.Is(err, ErrControlByte) {
		t.Fatalf("colisão de fronteira não recusada: %v", err)
	}

	// Charset benigno (pontos, dois-pontos, barras, hífen, porta) continua aceite,
	// incluindo na origem de um servidor MCP (projectada para MCPServer).
	ok := fakeEntry("tool.http.get-v2", ver(1, 0, 0), domain.KindMCPServer, "sha256:1")
	ok.Provenance.Origin = "mcp://host:8080/path"
	if _, err := FreezeToolSet(ctx, fakeCatalog{entries: []domain.Entry{ok}}, "r", nil); err != nil {
		t.Fatalf("charset benigno recusado: %v", err)
	}
}

// --- Conjunto vazio --------------------------------------------------------

func TestFreeze_Empty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, err := FreezeToolSet(ctx, fakeCatalog{}, "r", nil)
	if err != nil {
		t.Fatalf("FreezeToolSet: %v", err)
	}
	if f.Len() != 0 {
		t.Fatalf("Len=%d, quer 0", f.Len())
	}
	m := f.ApplyToManifest(agentruntime.Manifest{})
	if m.Tools != nil || m.Skills != nil {
		t.Fatalf("manifesto vazio devia ter Tools/Skills nil")
	}
	// O prefixo (só system) ainda materializa.
	view := f.Assembler("SYS").Assemble(1, nil)
	if len(view.Prefix) == 0 {
		t.Fatalf("prefixo vazio")
	}
}

// --- OTel: span de congelamento com atributos públicos, sem segredos -------

// TestFreeze_SpanEmitted verifica que a resolução do tool set emite um span com
// run_id, cardinalidade, hash do tool set e veredicto — atributos PÚBLICOS, sem
// qualquer segredo.
func TestFreeze_SpanEmitted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rec := &agentruntime.RecordingTracer{}
	cat := fakeCatalog{entries: []domain.Entry{
		fakeEntry("tool.a", ver(1, 0, 0), domain.KindTool, "sha256:a"),
		fakeEntry("skill.b", ver(1, 0, 0), domain.KindSkill, "sha256:b"),
	}}
	f, err := FreezeToolSet(ctx, cat, "run-span", nil, WithTracer(rec))
	if err != nil {
		t.Fatalf("FreezeToolSet: %v", err)
	}
	spans := rec.SpansByOperation(opFreeze)
	if len(spans) != 1 {
		t.Fatalf("quer 1 span %q, got %d", opFreeze, len(spans))
	}
	s := spans[0]
	if !s.Ended {
		t.Fatalf("span não foi fechado")
	}
	if s.Attributes[agentruntime.AttrRunID] != "run-span" {
		t.Fatalf("span run_id = %v", s.Attributes[agentruntime.AttrRunID])
	}
	if s.Attributes[registry.AttrToolSetSize] != 2 {
		t.Fatalf("span toolset_size = %v, quer 2", s.Attributes[registry.AttrToolSetSize])
	}
	if s.Attributes[AttrToolSetHash] != f.Hash() {
		t.Fatalf("span toolset_hash = %v, quer %s", s.Attributes[AttrToolSetHash], f.Hash())
	}
	if s.Attributes[registry.AttrDecision] != "frozen" {
		t.Fatalf("span decision = %v, quer frozen", s.Attributes[registry.AttrDecision])
	}
}
