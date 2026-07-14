package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/memory/provenance"
	"github.com/aos-ref/platform/registry"
	"github.com/aos-ref/platform/registry/domain"
)

// Atributos de span da integração MCP (namespace próprio aos.registry.mcp.*). SEM
// SEGREDOS: nunca se grava o token de auth do host nem o Mcp-Session-Id.
const (
	attrTransport = "aos.registry.mcp.transport"
	attrServerID  = "aos.registry.mcp.server_id"
	attrToolCount = "aos.registry.mcp.tool_count"
	attrResCount  = "aos.registry.mcp.resource_count"
	attrDecision  = "aos.registry.mcp.decision"
	// attrResListErr distingue "servidor sem resources" de "resources suprimidos por
	// erro do servidor" — a supressão do resources/list não passa despercebida na
	// descoberta (deixa de ser um fail-open silencioso).
	attrResListErr = "aos.registry.mcp.resources_list_error"
)

// Nomes de operação dos spans MCP.
const (
	opConnect  = "registry.mcp.connect"
	opDiscover = "registry.mcp.discover"
)

// Host é o HOST MCP do AOS: liga a servidores por qualquer transporte, faz o
// handshake, marca os schemas devolvidos como UNTRUSTED (barreira de AOS-042) e
// regista as tools descobertas como entradas CANDIDATAS no REG (staging). É seguro
// para concorrência (o Registry subjacente serializa a escrita).
type Host struct {
	reg      *registry.Registry
	ingestor *provenance.Ingestor
	tracer   agentruntime.Tracer
	now      func() time.Time
}

// HostOption configura o Host.
type HostOption func(*Host)

// WithTracer injecta a porta de observabilidade (spans OTel GenAI). Default NoopTracer.
func WithTracer(t agentruntime.Tracer) HostOption {
	return func(h *Host) {
		if t != nil {
			h.tracer = t
		}
	}
}

// WithClock injecta o relógio (determinismo; nunca time.Now numa decisão).
func WithClock(f func() time.Time) HostOption {
	return func(h *Host) {
		if f != nil {
			h.now = f
		}
	}
}

// WithIngestor injecta o Ingestor de proveniência (AOS-042) usado para taintar os
// schemas. Default: um Ingestor sobre o [provenance.DefaultTaintController].
func WithIngestor(in *provenance.Ingestor) HostOption {
	return func(h *Host) {
		if in != nil {
			h.ingestor = in
		}
	}
}

// NewHost constrói o Host MCP sobre um Registry (onde a descoberta produz staging).
// reg nil devolve [ErrNoRegistry].
func NewHost(reg *registry.Registry, opts ...HostOption) (*Host, error) {
	if reg == nil {
		return nil, ErrNoRegistry
	}
	h := &Host{
		reg:      reg,
		ingestor: provenance.NewIngestor(nil),
		tracer:   agentruntime.NoopTracer{},
		now:      time.Now,
	}
	for _, o := range opts {
		o(h)
	}
	return h, nil
}

// ConnectionInfo descreve o servidor a descobrir e a identidade do run que o liga. O
// ServerID + Version são a chave pinada da entrada mcp_server no REG; RunID/AgentID
// alimentam a proveniência dos registos taintados; Egress é a classe declarada.
type ConnectionInfo struct {
	// ServerID é o id estável do servidor MCP no catálogo (ex.: "mcp.filesystem").
	ServerID string
	// Version é a versão SemVer pinada do servidor.
	Version domain.Version
	// Origin/Publisher preenchem a proveniência do REG.
	Origin    string
	Publisher string
	// RunID/AgentID identificam o run que faz a descoberta (proveniência do taint).
	RunID   string
	AgentID string
	// Egress é a classe de egress declarada do servidor (none/internal/external).
	Egress domain.EgressClass
	// ToolVersion é a versão pinada atribuída às entradas de tool descobertas. Se
	// zero, reutiliza Version.
	ToolVersion domain.Version
	// Endpoint é uma referência NÃO-SECRETA e OPCIONAL do artefacto de origem: o
	// comando do binário (STDIO) ou o URL do endpoint (remoto). Fica gravada na
	// proveniência da entrada mcp_server para que a auditoria saiba que binário/endpoint
	// originou o artefacto. NUNCA deve conter segredos (token/sessão). O digest do
	// binário/manifesto é RESERVADO para AOS-047.
	Endpoint string
}

// DiscoveryResult é o produto de uma descoberta: o manifesto de capabilities, a
// barreira de taint (Partition, com os schemas em quarentena como dados) e as
// entradas CANDIDATAS criadas no REG (todas em staging, nunca active).
type DiscoveryResult struct {
	// Manifest é o manifesto de capabilities do handshake.
	Manifest CapabilityManifest
	// Taint é a partição de AOS-042 onde os schemas/descrições foram admitidos como
	// dados untrusted (quarentena). O planeador só veria a TrustedView — que aqui
	// fica VAZIA (nada do que o servidor devolve é control-plane).
	Taint *provenance.Partition
	// Staged são as entradas criadas no REG pela descoberta — SEMPRE em staging.
	Staged []domain.Entry
}

// Handshake executa o handshake MCP (initialize → tools/list → resources/list) sobre
// o transporte e devolve o manifesto de capabilities. É o passo de descoberta puro,
// sem efeitos no REG nem taint (usado por Discover).
func (h *Host) Handshake(ctx context.Context, t Transport) (CapabilityManifest, error) {
	ctx, span := h.tracer.StartSpan(ctx, opConnect)
	defer span.End()
	span.SetAttribute(attrTransport, string(t.Kind()))

	// initialize
	initParams := initializeParams{
		ProtocolVersion: protocolVersion,
		ClientInfo:      ClientInfo{Name: "aos-mcp-host", Version: "1.0.0"},
	}
	initRaw, err := t.Call(ctx, methodInitialize, initParams)
	if err != nil {
		span.SetAttribute(attrDecision, "handshake_error")
		return CapabilityManifest{}, fmt.Errorf("%w: initialize: %v", ErrHandshakeFailed, err)
	}
	var initRes initializeResult
	if err := json.Unmarshal(initRaw, &initRes); err != nil {
		span.SetAttribute(attrDecision, "handshake_error")
		return CapabilityManifest{}, fmt.Errorf("%w: initialize result: %v", ErrProtocol, err)
	}

	// tools/list
	toolsRaw, err := t.Call(ctx, methodToolsList, nil)
	if err != nil {
		span.SetAttribute(attrDecision, "handshake_error")
		return CapabilityManifest{}, fmt.Errorf("%w: tools/list: %v", ErrHandshakeFailed, err)
	}
	var toolsRes toolsListResult
	if err := json.Unmarshal(toolsRaw, &toolsRes); err != nil {
		span.SetAttribute(attrDecision, "handshake_error")
		return CapabilityManifest{}, fmt.Errorf("%w: tools/list result: %v", ErrProtocol, err)
	}

	// resources/list (opcional: um servidor sem resources pode devolver erro ou vazio;
	// tratamos erro como "sem resources" para não abortar a descoberta informativa). A
	// falha é REGISTADA (span + ResourcesUnavailable) para distinguir "sem resources" de
	// "resources suprimidos por erro" — a supressão deixa de ser um fail-open silencioso.
	var resList resourcesListResult
	var resUnavailable bool
	if resRaw, rerr := t.Call(ctx, methodResourcesList, nil); rerr == nil {
		_ = json.Unmarshal(resRaw, &resList)
	} else {
		resUnavailable = true
		span.SetAttribute(attrResListErr, true)
	}

	span.SetAttribute(attrServerID, initRes.ServerInfo.Name)
	span.SetAttribute(attrToolCount, len(toolsRes.Tools))
	span.SetAttribute(attrResCount, len(resList.Resources))
	span.SetAttribute(attrDecision, "handshake_ok")

	return CapabilityManifest{
		ServerInfo:           initRes.ServerInfo,
		ProtocolVersion:      initRes.ProtocolVersion,
		Tools:                toolsRes.Tools,
		Resources:            resList.Resources,
		ResourcesUnavailable: resUnavailable,
		// Digest RESERVADO (AOS-047): vazio.
	}, nil
}

// Discover faz o handshake, marca TODOS os schemas/descrições devolvidos como
// untrusted (barreira de AOS-042) e regista as tools descobertas no REG em STAGING
// (nunca active). Devolve o manifesto, a partição de taint e as entradas candidatas.
//
// Ordem (fail-closed): valida a ligação → handshake → taint → publish(staging). Se o
// registo no REG falhar, o erro é propagado (a descoberta não "meio-regista").
func (h *Host) Discover(ctx context.Context, t Transport, conn ConnectionInfo) (*DiscoveryResult, error) {
	if conn.ServerID == "" || conn.Version.IsZero() {
		return nil, ErrInvalidConnection
	}
	if conn.RunID == "" || conn.AgentID == "" {
		return nil, ErrInvalidConnection
	}
	if h.reg == nil {
		return nil, ErrNoRegistry
	}

	ctx, span := h.tracer.StartSpan(ctx, opDiscover)
	defer span.End()
	span.SetAttribute(attrTransport, string(t.Kind()))
	span.SetAttribute(attrServerID, conn.ServerID)

	manifest, err := h.Handshake(ctx, t)
	if err != nil {
		span.SetAttribute(attrDecision, "handshake_error")
		return nil, err
	}

	// TAINT (AOS-042): tudo o que o servidor devolveu é untrusted. Admite na partição
	// como dados (quarentena) — nunca no control-plane.
	part := provenance.NewPartition(nil)
	for _, tool := range manifest.Tools {
		if terr := h.taintMark(ctx, part, conn, "mcp.tool:"+tool.Name, toolTaintPayload(tool)); terr != nil {
			span.SetAttribute(attrDecision, "taint_error")
			return nil, fmt.Errorf("taint tool %q: %w", tool.Name, terr)
		}
	}
	for _, res := range manifest.Resources {
		if terr := h.taintMark(ctx, part, conn, "mcp.resource:"+res.URI, resourceTaintPayload(res)); terr != nil {
			span.SetAttribute(attrDecision, "taint_error")
			return nil, fmt.Errorf("taint resource %q: %w", res.URI, terr)
		}
	}

	// DESCOBERTA → STAGING: o servidor MCP é uma entrada candidata; cada tool
	// descoberta é uma entrada candidata. TUDO entra em staging (Publish nunca
	// produz active). O tipo de transporte é gravado na proveniência da entrada.
	staged, err := h.stage(ctx, conn, manifest, t.Kind())
	if err != nil {
		span.SetAttribute(attrDecision, "stage_error")
		return nil, err
	}

	span.SetAttribute(attrToolCount, len(manifest.Tools))
	span.SetAttribute(attrResCount, len(manifest.Resources))
	if manifest.ResourcesUnavailable {
		span.SetAttribute(attrResListErr, true)
	}
	span.SetAttribute(attrDecision, "discovered")

	return &DiscoveryResult{Manifest: manifest, Taint: part, Staged: staged}, nil
}

// stage publica o servidor MCP e as suas tools no REG, SEMPRE em staging. A classe de
// egress do servidor propaga-se às tools (declaração de contrato; a imposição é dos
// tickets de segurança seguintes).
func (h *Host) stage(ctx context.Context, conn ConnectionInfo, manifest CapabilityManifest, kind TransportKind) ([]domain.Entry, error) {
	egress := conn.Egress
	if egress == "" || !egress.Valid() {
		// Fail-closed na declaração: sem classe válida, assume-se a de maior contenção
		// declarável (internal) — nunca external por omissão.
		egress = domain.EgressInternal
	}
	toolVer := conn.ToolVersion
	if toolVer.IsZero() {
		toolVer = conn.Version
	}

	var staged []domain.Entry

	// Entrada do servidor MCP (kind mcp_server). A proveniência (Origin) capta uma
	// referência NÃO-SECRETA do artefacto de origem — transporte + endpoint/comando —
	// para que "o binário registado no REG" seja rastreável ao nível de AOS-046 (o
	// digest do binário/manifesto fica para AOS-047).
	serverEntry, err := h.reg.Publish(ctx, registry.PublishRequest{
		ID:        conn.ServerID,
		Version:   conn.Version,
		Kind:      domain.KindMCPServer,
		Origin:    serverOrigin(conn, kind),
		Publisher: conn.Publisher,
		Contract:  domain.Contract{Egress: egress},
	})
	if err != nil {
		return nil, fmt.Errorf("stage servidor %q: %w", conn.ServerID, err)
	}
	staged = append(staged, serverEntry)

	// Uma entrada tool por tool descoberta. O input schema (untrusted) vai para o
	// contract como declaração opaca — mas SANITIZADO: os keywords de anotação em
	// linguagem natural ("description"/"title"), o vector clássico de tool-poisoning
	// escondido nos sub-campos do schema, são removidos ANTES de entrar no control-plane.
	// A cópia INTEGRAL do schema permanece na quarentena de taint (data-plane, AOS-042).
	for _, tool := range manifest.Tools {
		entry, terr := h.reg.Publish(ctx, registry.PublishRequest{
			ID:        conn.ServerID + "/" + tool.Name,
			Version:   toolVer,
			Kind:      domain.KindTool,
			Origin:    conn.Origin,
			Publisher: conn.Publisher,
			Contract: domain.Contract{
				InputSchema: sanitizeSchema(tool.InputSchema),
				Egress:      egress,
			},
		})
		if terr != nil {
			return nil, fmt.Errorf("stage tool %q: %w", tool.Name, terr)
		}
		staged = append(staged, entry)
	}
	return staged, nil
}

// serverOrigin compõe a referência de origem NÃO-SECRETA do artefacto mcp_server: o
// origin declarado + o tipo de transporte + o endpoint/comando (se dado). Permite que a
// auditoria da entrada saiba QUE transporte/endpoint originou o artefacto. NUNCA inclui
// segredos (o token/sessão do host nunca são passados em ConnectionInfo.Endpoint).
func serverOrigin(conn ConnectionInfo, kind TransportKind) string {
	ref := "transport=" + string(kind)
	if conn.Endpoint != "" {
		ref += ";endpoint=" + conn.Endpoint
	}
	if conn.Origin == "" {
		return ref
	}
	return conn.Origin + " (" + ref + ")"
}

// sanitizeSchema devolve uma cópia do JSON-Schema com os keywords de ANOTAÇÃO em
// linguagem natural ("description"/"title") removidos recursivamente. O schema é
// controlado pelo servidor (untrusted); o tool-poisoning clássico esconde a injecção
// nesses sub-campos. A ESTRUTURA de validação (type/properties/required/…) é preservada
// — o planeador precisa dela — mas o texto de terceiros não atravessa para a cópia
// operativa (Contract) do control-plane. Preserva propriedades cujo NOME seja
// literalmente "description"/"title" (são nomes de campo, não anotações). Fail-closed:
// um schema que não seja JSON válido é OMITIDO (nil), nunca propagado em bruto.
func sanitizeSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	b, err := json.Marshal(stripSchemaAnnotations(v))
	if err != nil {
		return nil
	}
	return json.RawMessage(b)
}

// stripSchemaAnnotations percorre um valor JSON-Schema e remove os keywords de anotação
// NL. Trata os mapas NOME→subschema (properties/patternProperties/$defs/definitions)
// preservando os NOMES e recorrendo só nos subschemas — para não apagar uma propriedade
// legitimamente chamada "description"/"title".
func stripSchemaAnnotations(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			switch k {
			case "description", "title":
				continue // keyword de anotação NL — removido
			case "properties", "patternProperties", "$defs", "definitions":
				if sub, ok := val.(map[string]any); ok {
					named := make(map[string]any, len(sub))
					for name, s := range sub {
						named[name] = stripSchemaAnnotations(s)
					}
					out[k] = named
					continue
				}
				out[k] = stripSchemaAnnotations(val)
			default:
				out[k] = stripSchemaAnnotations(val)
			}
		}
		return out
	case []any:
		for i := range t {
			t[i] = stripSchemaAnnotations(t[i])
		}
		return t
	default:
		return v
	}
}
