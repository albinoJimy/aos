package main

// AOS_MODEL_TOOLS — REGISTRY DE TOOLS opt-in (análogo mínimo do EPIC-05) que liga a porta do modelo
// ao Reference Monitor fim-a-fim. Um ficheiro JSON MONTADO e TRUSTED (config do operador, nunca
// dados do modelo) que declara: (a) as tools OFERECIDAS ao modelo (nome/descrição/parâmetros — o
// `tools` do request OpenAI, sem o qual o modelo não emite tool_calls) e (b) o BINDING DE
// GOVERNANÇA de cada uma (capability + recurso) que o RM/PDP avalia. Vazio ⇒ nenhuma tool é
// oferecida (comportamento actual: o modelo não pede tools; nada muda).
//
// Porquê node-local e opt-in: o nó de referência não embute um registry de tools (é um epic à
// parte, como o Model Gateway era o EPIC-06 até ser ligado). Este seam expõe uma tool ao modelo SEM
// tocar no kernel (agent-runtime) nem no gateway, só para EXERCITAR o caminho de MEDIAÇÃO PDP: o
// modelo pede uma tool → o RM decide (allow/deny) no ponto de mediação único (ADR-002). Produção
// ligaria aqui o registry real (catálogo assinado + executor da tool).
//
// INVARIANTE DE SEGURANÇA (AOS-069, ver ToolInvocation.AuthorizationTaint): o binding
// capability/recurso vem do REGISTRY (config trusted), não da saída do modelo — o modelo só escolhe
// QUAL tool pelo nome. O AuthorizationTaint NUNCA é preenchido aqui: fica vazio ⇒ untrusted
// (fail-closed). Uma tool call ORIGINADA pelo modelo é autorização untrusted, e o TaintGate do RM
// impede que uma autorização untrusted origine uma capability privilegiada. É exactamente esta a
// propriedade que o seam demonstra (P4: "untrusted não comanda").

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/port"
)

// ErrBadModelTools — AOS_MODEL_TOOLS está definido mas o registry é inválido. Fail-closed: quem
// declara tools obtém-nas bem-formadas (name + capability por tool) ou o nó recusa arrancar.
var ErrBadModelTools = errors.New("aos: AOS_MODEL_TOOLS mal configurado — deve apontar um ficheiro JSON legivel com uma lista de tools; cada tool exige `name` (o que o modelo chama) e `capability` (o direito escopado que o Reference Monitor avalia) nao-vazios")

// modelToolSpec é uma entrada do registry JSON: a face OFERECIDA ao modelo (Name/Description/
// Parameters — o schema de function-calling) MAIS o binding de GOVERNANÇA (Capability + Resource*)
// que o RM avalia. O binding é do registry, nunca do modelo.
type modelToolSpec struct {
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Parameters     json.RawMessage `json:"parameters"`
	Capability     string          `json:"capability"`
	ResourceType   string          `json:"resource_type"`
	ResourceValue  string          `json:"resource_value"`
	ResourceRegion string          `json:"resource_region"`
	// Egress / CredentialScopes descrevem o CONTRATO de supply-chain da tool (só usados quando
	// AOS_MODEL_TOOLS_REGISTER regista o catálogo assinado; ver modelcatalog.go). Egress ∈
	// {none,internal,external} (default none); CredentialScopes são scopes DECLARADOS (nunca
	// segredos). Não afectam a decisão Cedar (essa avalia a Capability), só a REVALIDAÇÃO.
	Egress           string   `json:"egress"`
	CredentialScopes []string `json:"credential_scopes"`
	// Sandbox, quando presente, LIGA a tool à execução mediada em sandbox (AOS-005/AOS-064):
	// declara COMO os args OPACOS do modelo viram um ExecRequest (Command FIXO + slots
	// nomeados). É config TRUSTED — o modelo nunca escolhe o Command. Ausente ⇒ a tool é
	// mediada mas não tem executor de sandbox ligado (só exercita a decisão). Ver sandboxwiring.go.
	Sandbox *sandboxMapping `json:"sandbox,omitempty"`
}

// sandboxMapping é a face JSON do [sandbox.SandboxBinding] no registry: como os args do
// modelo preenchem o ExecRequest. command é FIXO (trusted); path_arg/args_from/write_arg
// nomeiam as chaves dos args (untrusted) que preenchem Path/Args/Write.
type sandboxMapping struct {
	Command  string   `json:"command"`
	PathArg  string   `json:"path_arg"`
	ArgsFrom []string `json:"args_from"`
	WriteArg string   `json:"write_arg"`
}

// readModelToolSpecs lê + valida o ficheiro AOS_MODEL_TOOLS e devolve os specs crus. Vazio ⇒
// (nil, nil): não configurado. Fonte única partilhada por loadModelToolsFromEnv (face do modelo) e
// buildSignedToolRegistryFromEnv (catálogo assinado).
func readModelToolSpecs() ([]modelToolSpec, error) {
	path := strings.TrimSpace(os.Getenv("AOS_MODEL_TOOLS"))
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: ler ficheiro: %v", ErrBadModelTools, err)
	}
	var specs []modelToolSpec
	if err := json.Unmarshal(raw, &specs); err != nil {
		return nil, fmt.Errorf("%w: JSON invalido: %v", ErrBadModelTools, err)
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("%w: lista vazia", ErrBadModelTools)
	}
	for i, s := range specs {
		if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.Capability) == "" {
			return nil, fmt.Errorf("%w: tool #%d sem name/capability", ErrBadModelTools, i)
		}
	}
	return specs, nil
}

// toolBinding é o mapeamento trusted nome-da-tool → (capability, recurso) aplicado à ToolInvocation
// que o modelo emitiu, antes da mediação.
type toolBinding struct {
	capability     string
	resourceType   string
	resourceValue  string
	resourceRegion string
}

// loadModelToolsFromEnv lê AOS_MODEL_TOOLS. Devolve (tools p/ WithTools, bindings p/ enriquecimento,
// err). Vazio ⇒ (nil, nil, nil): nenhuma tool oferecida, comportamento inalterado.
func loadModelToolsFromEnv() ([]port.Tool, map[string]toolBinding, error) {
	specs, err := readModelToolSpecs()
	if err != nil {
		return nil, nil, err
	}
	if len(specs) == 0 {
		return nil, nil, nil
	}
	tools := make([]port.Tool, 0, len(specs))
	bindings := make(map[string]toolBinding, len(specs))
	for _, s := range specs {
		name := strings.TrimSpace(s.Name)
		capab := strings.TrimSpace(s.Capability)
		tools = append(tools, port.Tool{
			Type: "function",
			Function: port.FunctionDef{
				Name:        name,
				Description: s.Description,
				Parameters:  s.Parameters,
			},
		})
		bindings[name] = toolBinding{
			capability:     capab,
			resourceType:   strings.TrimSpace(s.ResourceType),
			resourceValue:  strings.TrimSpace(s.ResourceValue),
			resourceRegion: strings.TrimSpace(s.ResourceRegion),
		}
	}
	return tools, bindings, nil
}

// toolEnrichingClient decora um [agentruntime.ModelClient]: quando o modelo escolhe uma tool pelo
// NOME, preenche o binding de GOVERNANÇA (capability + recurso) a partir do registry trusted, para
// o Reference Monitor ter o que avaliar. Uma tool fora do registry fica com Capability vazia ⇒
// default-deny no RM (o modelo não pode inventar uma capability). NÃO toca em AuthorizationTaint
// (fica untrusted, fail-closed — AOS-069).
type toolEnrichingClient struct {
	inner    agentruntime.ModelClient
	bindings map[string]toolBinding
}

var _ agentruntime.ModelClient = (*toolEnrichingClient)(nil)

func (c *toolEnrichingClient) Call(ctx context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	resp, err := c.inner.Call(ctx, view)
	if err != nil {
		return resp, err
	}
	for i := range resp.ToolCalls {
		b, ok := c.bindings[resp.ToolCalls[i].ToolID]
		if !ok {
			continue // desconhecida do registry ⇒ Capability vazia ⇒ RM nega fail-closed.
		}
		resp.ToolCalls[i].Capability = b.capability
		resp.ToolCalls[i].ResourceType = b.resourceType
		resp.ToolCalls[i].ResourceValue = b.resourceValue
		resp.ToolCalls[i].ResourceRegion = b.resourceRegion
		// AuthorizationTaint: DELIBERADAMENTE não preenchido (vazio ⇒ untrusted). Ver AOS-069.
	}
	return resp, nil
}
