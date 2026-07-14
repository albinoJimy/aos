package registry

import (
	"context"
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/registry/domain"
)

// mcpReq e skillReq cobrem os OUTROS dois tipos de artefacto (alem de tool), para
// provar que os tres tipos sao representaveis e distinguiveis no catalogo.
func mcpReq(id string, v domain.Version) PublishRequest {
	return PublishRequest{
		ID: id, Version: v, Kind: domain.KindMCPServer,
		Origin: "mcp://host.example", Publisher: "pub:mcp",
		Contract: domain.Contract{Egress: domain.EgressInternal},
	}
}

func skillReq(id string, v domain.Version) PublishRequest {
	return PublishRequest{
		ID: id, Version: v, Kind: domain.KindSkill,
		Origin: "self", Publisher: "pub:self",
		Contract: domain.Contract{Egress: domain.EgressNone},
	}
}

// TestIntegration_RT_ResolvesToolSet simula o Agent Runtime a resolver o conjunto de
// tools de um run — SEMPRE por versao pinada — a partir do REG, com os tres tipos de
// artefacto no catalogo, todos promovidos a active.
func TestIntegration_RT_ResolvesToolSet(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	v := ver(1, 0, 0)
	set := []PublishRequest{
		toolReq("tool.http.get", v),
		mcpReq("mcp.filesystem", v),
		skillReq("skill.summarize", v),
	}
	for _, req := range set {
		mustPublish(t, reg, req)
		mustSetStatus(t, reg, req.ID, v, domain.StatusActive)
	}

	// O RT resolve cada tool por (id, version) pinada e verifica admissibilidade.
	kinds := map[domain.ArtifactKind]bool{}
	for _, req := range set {
		e, err := reg.Resolve(ctx, req.ID, v)
		if err != nil {
			t.Fatalf("RT Resolve(%s): %v", req.ID, err)
		}
		if !e.Version.Equal(v) {
			t.Fatalf("%s resolveu versao %v", req.ID, e.Version)
		}
		ok, _, err := reg.IsAdmissible(ctx, req.ID, v)
		if err != nil || !ok {
			t.Fatalf("%s devia ser admissivel (active)", req.ID)
		}
		kinds[e.Kind] = true
	}
	// Os tres tipos distintos foram resolvidos.
	if len(kinds) != 3 {
		t.Fatalf("esperados 3 tipos distinguiveis, got %d (%v)", len(kinds), kinds)
	}
}

// TestIntegration_RM_GetsExpectedDigest simula o Reference Monitor a obter o digest
// esperado de uma tool pinada para revalidacao (a comparacao concreta e AOS-051).
func TestIntegration_RM_GetsExpectedDigest(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	v := ver(2, 1, 0)
	published := mustPublish(t, reg, toolReq("tool.http.post", v))
	mustSetStatus(t, reg, "tool.http.post", v, domain.StatusActive)

	digest, err := reg.GetDigest(ctx, "tool.http.post", v)
	if err != nil {
		t.Fatalf("RM GetDigest: %v", err)
	}
	if digest == "" || digest != published.Digest {
		t.Fatalf("digest esperado %q != publicado %q", digest, published.Digest)
	}
}

// TestIntegration_RM_DefaultDenyOutsideCatalog: uma capacidade FORA do catalogo e
// negada por default no ponto de consulta do REG (getDigest devolve not_found;
// isAdmissible devolve deny). Nenhuma tool ausente do REG e despachavel (ADR-002).
//
// RASTREABILIDADE (AOS-045): este teste SIMULA o Reference Monitor chamando a
// superficie de consulta do REG (GetDigest/IsAdmissible) — prova o default-deny do
// lado REG, mas NAO exercita o RM real a mediar via REG (o reference-monitor nao
// importa este pacote nesta fundacao; e dependencia // indirect). A mediacao real
// RM->REG por tool call e AOS-051 (revalidacao por chamada), explicitamente fora do
// escopo deste ticket. AOS-051 deve fechar o texto literal do DoD ("default-deny no
// RM") com um teste que integre o Reference Monitor real.
func TestIntegration_RM_DefaultDenyOutsideCatalog(t *testing.T) {
	t.Parallel()
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	v := ver(1, 0, 0)

	// getDigest de uma capacidade inexistente -> ErrNotFound (o RM nega).
	if _, err := reg.GetDigest(ctx, "tool.exfiltrate", v); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDigest fora do catalogo = %v, quer ErrNotFound", err)
	}
	// isAdmissible -> deny.
	ok, reason, err := reg.IsAdmissible(ctx, "tool.exfiltrate", v)
	if err != nil {
		t.Fatalf("IsAdmissible err: %v", err)
	}
	if ok {
		t.Fatal("tool ausente do REG NAO pode ser despachavel (default-deny)")
	}
	if reason == "" {
		t.Fatal("negacao devia ter razao legivel")
	}
}

// TestIntegration_QuerySpansEmitted verifica que as operacoes de consulta emitem
// spans OTel GenAI via a porta Tracer (sem segredos — so id/version/digest/decisao).
func TestIntegration_QuerySpansEmitted(t *testing.T) {
	t.Parallel()
	tracer := &agentruntime.RecordingTracer{}
	reg, _ := newTestRegistry(t, WithTracer(tracer))
	ctx := context.Background()
	v := ver(1, 0, 0)
	mustPublish(t, reg, toolReq("tool.x", v))
	mustSetStatus(t, reg, "tool.x", v, domain.StatusActive)

	if _, err := reg.Resolve(ctx, "tool.x", v); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := reg.GetDigest(ctx, "tool.x", v); err != nil {
		t.Fatalf("GetDigest: %v", err)
	}
	if _, _, err := reg.IsAdmissible(ctx, "tool.x", v); err != nil {
		t.Fatalf("IsAdmissible: %v", err)
	}

	for _, op := range []string{opResolve, opGetDigest, opIsAdmissible} {
		spans := tracer.SpansByOperation(op)
		if len(spans) == 0 {
			t.Fatalf("nenhum span para a operacao %q", op)
		}
		s := spans[0]
		if !s.Ended {
			t.Fatalf("span %q nao foi terminado", op)
		}
		if s.Attributes[AttrArtifactID] != "tool.x" {
			t.Fatalf("span %q sem id do artefacto", op)
		}
		if s.Attributes[AttrDecision] == nil {
			t.Fatalf("span %q sem decisao", op)
		}
		// Garantia anti-segredo: nenhum atributo transporta valores de credencial.
		for k, val := range s.Attributes {
			if str, ok := val.(string); ok && str == "vault:http" {
				t.Fatalf("span %q expos scope de credencial no atributo %q", op, k)
			}
		}
	}
}
