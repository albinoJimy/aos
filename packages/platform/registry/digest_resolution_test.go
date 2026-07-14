package registry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/substrate/eventstore"
)

// constDigester devolve um digest CONSTANTE, independente do conteúdo. Serve para
// SIMULAR uma divergência entre o digest ESPERADO gravado no catálogo (calculado
// pelo SHA-256 real na publicação) e o digest RECALCULADO na resolução — o
// equivalente a "conteúdo adulterado" cujo hash já não coincide com o pin.
type constDigester struct{ val string }

func (c constDigester) Digest(domain.ArtifactKind, domain.Contract) string { return c.val }

// --- AOS-047: resolução por versão exacta com digest correcto PASSA ---------

// TestResolve_ExactVersionCorrectDigestPasses: uma resolução por (id, versão)
// pinada exacta cujo digest recalculado COINCIDE com o esperado devolve a entrada
// (o digest verificado é o SHA-256 canonicalizado real).
func TestResolve_ExactVersionCorrectDigestPasses(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t) // digester por omissão = SHA256 canonicalizado
	ctx := context.Background()
	v := ver(1, 4, 2)
	pub := mustPublish(t, reg, toolReq("tool.http.get", v))

	// O digest publicado é um SHA-256 real (prefixo sha256:), não o placeholder.
	if got := pub.Digest; len(got) != len(digest.Prefix)+64 || got[:len(digest.Prefix)] != digest.Prefix {
		t.Fatalf("digest publicado %q nao e SHA-256 canonicalizado", got)
	}

	e, err := reg.Resolve(ctx, "tool.http.get", v)
	if err != nil {
		t.Fatalf("Resolve com digest correcto devia passar: %v", err)
	}
	if e.Digest != pub.Digest {
		t.Fatalf("digest resolvido %q != publicado %q", e.Digest, pub.Digest)
	}
	// GetDigest entrega o mesmo digest verificado ao RM.
	gd, err := reg.GetDigest(ctx, "tool.http.get", v)
	if err != nil {
		t.Fatalf("GetDigest: %v", err)
	}
	if gd != pub.Digest {
		t.Fatalf("GetDigest %q != publicado %q", gd, pub.Digest)
	}
}

// --- AOS-047: digest DIVERGENTE é BLOQUEADO (ErrDigestMismatch) -------------

// TestResolve_DigestMismatchBlocked prova o fail-closed central de AOS-047: se o
// digest recalculado sobre o conteúdo NÃO coincidir com o digest esperado no
// catálogo, a resolução (e a consulta de digest) BLOQUEIAM com ErrDigestMismatch,
// negando a admissão do artefacto adulterado no run.
//
// Modela-se a divergência publicando com o SHA-256 real e resolvendo através de
// uma segunda instância do REG (sobre o MESMO Event Store — a fonte de verdade)
// cujo Digester recalcula um valor diferente. É o equivalente estrutural a um
// conteúdo cujo hash deixou de coincidir com o pin.
func TestResolve_DigestMismatchBlocked(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	v := ver(1, 0, 0)

	// Publica com o SHA-256 real (digest esperado = SHA-256 do conteúdo).
	pubReg, err := New(store, WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("New(pub): %v", err)
	}
	if _, err := pubReg.Publish(ctx, toolReq("tool.x", v)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if _, err := pubReg.SetStatus(ctx, "tool.x", v, domain.StatusActive); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	// Resolve com um Digester que recalcula um digest DIFERENTE -> divergência.
	tamperReg, err := New(store,
		WithClock(fixedClock()),
		WithDigester(constDigester{val: "sha256:0000000000000000000000000000000000000000000000000000000000000000"}),
	)
	if err != nil {
		t.Fatalf("New(tamper): %v", err)
	}

	if _, err := tamperReg.Resolve(ctx, "tool.x", v); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Resolve com digest divergente = %v, quer ErrDigestMismatch", err)
	}
	// A identidade re-exportada e a do pacote digest coincidem.
	_, err = tamperReg.Resolve(ctx, "tool.x", v)
	if !errors.Is(err, digest.ErrDigestMismatch) {
		t.Fatalf("erro nao e digest.ErrDigestMismatch: %v", err)
	}
	// GetDigest (superfície do RM) também bloqueia fail-closed.
	if _, err := tamperReg.GetDigest(ctx, "tool.x", v); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("GetDigest com digest divergente = %v, quer ErrDigestMismatch", err)
	}
}

// TestResolve_MismatchSpanDecision verifica que a divergência marca o span com a
// decisão "digest_mismatch" (auditável; sem segredos — só id/version/digest).
func TestResolve_MismatchSpanDecision(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	v := ver(1, 0, 0)

	pubReg, _ := New(store, WithClock(fixedClock()))
	if _, err := pubReg.Publish(ctx, toolReq("tool.x", v)); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	tracer := &agentruntime.RecordingTracer{}
	tamperReg, _ := New(store,
		WithClock(fixedClock()),
		WithTracer(tracer),
		WithDigester(constDigester{val: "sha256:deadbeef"}),
	)
	if _, err := tamperReg.Resolve(ctx, "tool.x", v); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("esperado ErrDigestMismatch, got %v", err)
	}
	spans := tracer.SpansByOperation(opResolve)
	if len(spans) == 0 {
		t.Fatal("sem span de resolucao")
	}
	if got := spans[0].Attributes[AttrDecision]; got != "digest_mismatch" {
		t.Fatalf("decisao do span = %v, quer digest_mismatch", got)
	}
}

// --- AOS-047: resolução por latest/flutuante REJEITADA (reforço) ------------

// TestResolve_LatestRejected reforça (AOS-047) que a resolução por referência
// flutuante é rejeitada ANTES de qualquer verificação de digest — o pinning é a
// primeira barreira. Complementa TestResolveString_RejectsFloating.
func TestResolve_LatestRejected(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	mustPublish(t, reg, toolReq("tool.x", ver(1, 2, 3)))

	for _, ref := range []string{"latest", "main", "", "^1.0.0", "1.x"} {
		if _, err := reg.ResolveString(ctx, "tool.x", ref); !errors.Is(err, ErrFloatingResolution) {
			t.Fatalf("ResolveString(%q) = %v, quer ErrFloatingResolution", ref, err)
		}
	}
	// Versão não-especificada (0.0.0) via Resolve directo -> não pinada.
	if _, err := reg.Resolve(ctx, "tool.x", domain.Version{}); !errors.Is(err, ErrUnpinnedResolution) {
		t.Fatalf("Resolve nao-pinada = %v, quer ErrUnpinnedResolution", err)
	}
}

// --- AOS-047: manifesto de dependências grava (version, digest) por run -----

// TestManifest_RecordsVersionDigestPerRun prova que o par (version, digest) de
// cada artefacto resolvido flui para o manifesto de dependências da trajectória,
// REUTILIZANDO o tipo agentruntime.PinnedDep do RT (AOS-013/016) sem reimplementar
// o manifesto. Simula o arranque de um run: resolve o tool set (todas pinadas) e
// projecta-o em PinnedDeps que o RT gravaria no seu manifesto imutável.
func TestManifest_RecordsVersionDigestPerRun(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	v := ver(2, 3, 1)

	set := []PublishRequest{
		toolReq("tool.http.get", v),
		mcpReq("mcp.filesystem", v),
		skillReq("skill.summarize", v),
	}
	for _, req := range set {
		mustPublish(t, reg, req)
		mustSetStatus(t, reg, req.ID, v, domain.StatusActive)
	}

	// O RT resolve o tool set do run (sempre pinado) e recolhe as entradas.
	resolved := make([]domain.Entry, 0, len(set))
	for _, req := range set {
		e, err := reg.Resolve(ctx, req.ID, v)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", req.ID, err)
		}
		resolved = append(resolved, e)
	}

	deps := ManifestDeps(resolved)
	if len(deps) != len(set) {
		t.Fatalf("esperadas %d deps, got %d", len(set), len(deps))
	}

	// Cada dep grava (name, version, digest) coincidentes com a entrada resolvida.
	for i, dep := range deps {
		e := resolved[i]
		if dep.Name != e.ID {
			t.Fatalf("dep[%d].Name = %q, quer %q", i, dep.Name, e.ID)
		}
		if dep.Version != v.String() {
			t.Fatalf("dep[%d].Version = %q, quer %q", i, dep.Version, v.String())
		}
		if dep.Digest == "" || dep.Digest != e.Digest {
			t.Fatalf("dep[%d].Digest = %q, quer %q (o digest verificado)", i, dep.Digest, e.Digest)
		}
	}

	// O par flui para o MANIFESTO por trajectória do RT (reutilização, não
	// reimplementação): gravado em Manifest.Tools, torna o replay fiel.
	manifest := agentruntime.Manifest{
		SchemaVersion: agentruntime.ManifestSchemaVersion,
		Tools:         deps,
	}
	if len(manifest.Tools) != len(set) {
		t.Fatalf("manifesto gravou %d tools, quer %d", len(manifest.Tools), len(set))
	}
	// O servidor MCP transporta a origem para desambiguar a proveniência.
	for _, dep := range deps {
		if dep.Name == "mcp.filesystem" && dep.MCPServer == "" {
			t.Fatal("dep de mcp_server devia transportar a origem em MCPServer")
		}
	}
}

// --- AOS-047 (Q3): Publish REJEITA schemas de contrato malformados ----------

// TestPublish_MalformedSchemaRejected prova o fail-closed de AOS-047 na
// publicação: um schema de I/O que NÃO é JSON válido (ou que tem chaves
// duplicadas) é RECUSADO antes de ser pinado — o digest de um artefacto admitido
// nunca cobre bytes crus opacos. A recusa satisfaz TANTO ErrInvalidRequest quanto
// digest.ErrInvalidJSON (a causa raiz é visível ao chamador).
func TestPublish_MalformedSchemaRejected(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	v := ver(1, 0, 0)

	cases := []struct {
		name   string
		schema string
		out    bool // se true, o schema malformado é o OutputSchema
	}{
		{"input lixo", "{not json", false},
		{"input tokens a mais", "{} {}", false},
		{"input chave duplicada", `{"a":1,"a":2}`, false},
		{"output lixo", `{"a":`, true},
		{"output chave duplicada", `{"k":1,"k":2}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := toolReq("tool.schema", v)
			if tc.out {
				req.Contract.OutputSchema = []byte(tc.schema)
			} else {
				req.Contract.InputSchema = []byte(tc.schema)
			}
			_, err := reg.Publish(ctx, req)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Publish(%s) = %v, quer ErrInvalidRequest", tc.name, err)
			}
			if !errors.Is(err, digest.ErrInvalidJSON) {
				t.Fatalf("Publish(%s) = %v, quer tambem digest.ErrInvalidJSON", tc.name, err)
			}
		})
	}
}

// TestPublish_ValidSchemaAccepted confirma que a validação NÃO regride o caminho
// feliz: um schema JSON bem-formado (e um contrato sem schemas) publica sem erro,
// e o digest é o SHA-256 canonicalizado real.
func TestPublish_ValidSchemaAccepted(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	v := ver(1, 0, 0)
	req := toolReq("tool.schema.ok", v)
	req.Contract.InputSchema = []byte(`{"type":"object","required":["id"]}`)
	req.Contract.OutputSchema = []byte(`{"type":"string"}`)
	e, err := reg.Publish(ctx, req)
	if err != nil {
		t.Fatalf("Publish com schema valido devia passar: %v", err)
	}
	if len(e.Digest) != len(digest.Prefix)+64 || e.Digest[:len(digest.Prefix)] != digest.Prefix {
		t.Fatalf("digest %q nao e SHA-256 canonicalizado", e.Digest)
	}
}

// --- AOS-047 (Q4): IsAdmissible fecha o gate por integridade do digest -------

// TestIsAdmissible_DigestMismatchFailsClosed prova a defense-in-depth de AOS-047:
// uma entrada ACTIVE cujo conteúdo já não coincide com o digest pinado NÃO é
// admissível — IsAdmissible devolve ErrDigestMismatch (fail-closed), fechando o
// gate mesmo para um integrador que despache só pelo veredicto de admissibilidade.
func TestIsAdmissible_DigestMismatchFailsClosed(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	v := ver(1, 0, 0)

	// Publica com o SHA-256 real e promove a active.
	pubReg, _ := New(store, WithClock(fixedClock()))
	if _, err := pubReg.Publish(ctx, toolReq("tool.x", v)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if _, err := pubReg.SetStatus(ctx, "tool.x", v, domain.StatusActive); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	// Consulta a admissibilidade através de um REG cujo Digester recalcula um
	// digest DIFERENTE (equivalente a conteúdo adulterado).
	tamperReg, _ := New(store,
		WithClock(fixedClock()),
		WithDigester(constDigester{val: "sha256:0000000000000000000000000000000000000000000000000000000000000000"}),
	)
	ok, _, err := tamperReg.IsAdmissible(ctx, "tool.x", v)
	if ok {
		t.Fatal("entrada active adulterada NAO devia ser admissivel")
	}
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("IsAdmissible com digest divergente = %v, quer ErrDigestMismatch", err)
	}
}

// --- AOS-047: manifesto (version, digest) sobrevive round-trip pelo Event Store

// TestManifest_VersionDigestRoundTripThroughEventStore complementa
// TestManifest_RecordsVersionDigestPerRun com a PERSISTÊNCIA end-to-end: o
// manifesto construído a partir de ManifestDeps é gravado por
// agentruntime.TurnRecorder.Record no Event Store e RELIDO, confirmando que o par
// (version, digest) de cada dependência pinada sobrevive à serialização do evento
// turn.recorded. Reutiliza o TurnRecorder do RT (AOS-013) — não reimplementa o
// manifesto; apenas assevera a fronteira de projecção → persistência.
func TestManifest_VersionDigestRoundTripThroughEventStore(t *testing.T) {
	t.Parallel()
	reg, store := newTestRegistry(t)
	ctx := context.Background()
	v := ver(2, 3, 1)

	set := []PublishRequest{
		toolReq("tool.http.get", v),
		mcpReq("mcp.filesystem", v),
		skillReq("skill.summarize", v),
	}
	for _, req := range set {
		mustPublish(t, reg, req)
		mustSetStatus(t, reg, req.ID, v, domain.StatusActive)
	}
	resolved := make([]domain.Entry, 0, len(set))
	for _, req := range set {
		e, err := reg.Resolve(ctx, req.ID, v)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", req.ID, err)
		}
		resolved = append(resolved, e)
	}
	deps := ManifestDeps(resolved)

	// Persiste o manifesto por run via o TurnRecorder do RT (AOS-013).
	recorder := agentruntime.NewTurnRecorder(store)
	const runID = "run-aos047"
	seq, err := recorder.Record(ctx, agentruntime.TurnRecord{
		RunID:  runID,
		StepID: "turn-1",
		Turn:   1,
		Manifest: agentruntime.Manifest{
			SchemaVersion: agentruntime.ManifestSchemaVersion,
			Tools:         deps,
		},
		Final: true,
	})
	if err != nil {
		t.Fatalf("TurnRecorder.Record: %v", err)
	}
	if seq == 0 {
		t.Fatal("seq atribuido devia ser nao-zero")
	}

	// Relê o evento turn.recorded e desserializa o manifesto persistido.
	evs, err := store.Read(ctx, runID, 1)
	if err != nil {
		t.Fatalf("store.Read: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("esperado 1 evento no run, got %d", len(evs))
	}
	if evs[0].Type != agentruntime.EventTypeTurnRecorded {
		t.Fatalf("tipo do evento = %q, quer %q", evs[0].Type, agentruntime.EventTypeTurnRecorded)
	}
	var payload struct {
		Manifest agentruntime.Manifest `json:"manifest"`
	}
	if err := json.Unmarshal(evs[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal do payload turn.recorded: %v", err)
	}
	got := payload.Manifest.Tools
	if len(got) != len(resolved) {
		t.Fatalf("manifesto persistido gravou %d tools, quer %d", len(got), len(resolved))
	}
	// O par (version, digest) de cada dependência sobrevive ao round-trip.
	for i, dep := range got {
		e := resolved[i]
		if dep.Name != e.ID {
			t.Fatalf("dep[%d].Name = %q, quer %q", i, dep.Name, e.ID)
		}
		if dep.Version != v.String() {
			t.Fatalf("dep[%d].Version = %q, quer %q (perdido na serializacao)", i, dep.Version, v.String())
		}
		if dep.Digest == "" || dep.Digest != e.Digest {
			t.Fatalf("dep[%d].Digest = %q, quer %q (perdido na serializacao)", i, dep.Digest, e.Digest)
		}
	}
}

// TestPinnedDep_SingleEntry cobre a projecção unitária (tool/skill sem MCPServer).
func TestPinnedDep_SingleEntry(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	v := ver(1, 0, 0)
	mustPublish(t, reg, toolReq("tool.x", v))
	mustSetStatus(t, reg, "tool.x", v, domain.StatusActive)
	e, err := reg.Resolve(ctx, "tool.x", v)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	dep := PinnedDep(e)
	if dep.Name != "tool.x" || dep.Version != "1.0.0" || dep.Digest != e.Digest {
		t.Fatalf("PinnedDep = %+v", dep)
	}
	if dep.MCPServer != "" {
		t.Fatalf("tool nao devia ter MCPServer, got %q", dep.MCPServer)
	}
	if ManifestDeps(nil) != nil {
		t.Fatal("ManifestDeps(nil) devia ser nil")
	}
}
