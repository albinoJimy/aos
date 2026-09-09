// Package decompose implementa o Decomposer de PRODUÇÃO do planeador (AOS-388): o
// adaptador que transforma um objectivo num [plan.PlanDocument] multi-nó chamando um
// modelo real, satisfazendo a porta [planner.Decomposer]. É a peça que faltava para o
// fluxo goal→DAG deixar de ser um stub de nó único (DEF-803, resíduo de AOS-025).
//
// FRONTEIRA DE CONFIANÇA (ADR-005). O documento devolvido é DADOS *untrusted*: sai do
// modelo, é validado só na FORMA por [plan.Decode] e devolvido tal-qual — excepto o
// carimbo de proveniência, que é a verdade do Decomposer e não uma alegação do modelo.
// NUNCA é executado aqui; a validação semântica (aciclicidade, resolução de tools,
// tectos, risco derivado) é de `planvalidate` (AOS-231/232) e o gate humano é de
// `plan-approval` (AOS-236), ambos a jusante.
//
// CAMADAS (ADR-019 §2.5). O pacote depende apenas do próprio módulo (`plan`,
// `planner`, `plannerprompt`) e de uma PORTA de modelo LOCAL ([Model]) — NÃO importa
// `platform/model-gateway`. É o mesmo padrão da porta `Pricer` em
// `planvalidate/budget.go`, que declarou evitar puxar o Model Gateway para não abrir
// uma excepção nova ao `layer-lint`. O concreto (o cliente do Gateway) é composto no
// binário `aos-orq`, exempto por ADR-018.
//
// RETRY. O Decomposer é sem estado por chamada e NÃO implementa retry próprio: o laço
// de N tentativas com reserva escalada é do [planner.Planner.Decompose], que o invoca.
// Um erro daqui conta como UMA tentativa falhada.
package decompose

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planner"
	"github.com/aos-ref/control-plane/orchestrator/plannerprompt"
)

// Model é a PORTA LOCAL do modelo de chat: recebe as mensagens `system`+`user` e
// devolve o texto da resposta. É deliberadamente mínima — o concreto (Model Gateway)
// adapta-se a ela no `aos-orq`, e os testes injectam um fake determinístico. Ver a
// nota de camadas no doc do pacote sobre porque a porta é local.
type Model interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// Erros do Decomposer — comparáveis por errors.Is.
var (
	// ErrNoModel — construção sem [Model]. Fail-closed: um Decomposer sem modelo seria
	// a capacidade-fantasma que admite tudo, o oposto de admission control.
	ErrNoModel = errors.New("decompose: Model obrigatorio (nil)")
	// ErrEmptyGoal — pedido sem objectivo; não há o que decompor. Fail-closed ANTES de
	// chamar o modelo (não se queima uma chamada por um pedido malformado).
	ErrEmptyGoal = errors.New("decompose: objectivo vazio")
	// ErrNoCapabilitiesHash — pedido sem `capabilities_hash`. A proveniência (§3.6,
	// AOS-243) exige o snapshot pinado contra o qual AOS-231 valida; sem ele, o carimbo
	// não teria significado.
	ErrNoCapabilitiesHash = errors.New("decompose: capabilities_hash vazio (proveniencia)")
	// ErrEmptyResponse — o modelo devolveu texto vazio (nenhum documento a parsear).
	ErrEmptyResponse = errors.New("decompose: resposta do modelo vazia")
)

// LLMDecomposer satisfaz [planner.Decomposer] chamando um [Model]. Imutável após
// [New]; seguro para uso concorrente na medida em que o [Model] injectado o for.
type LLMDecomposer struct {
	model        Model
	prompt       plannerprompt.Prompt
	modelID      string
	capabilities string
}

// Option configura o [LLMDecomposer].
type Option func(*LLMDecomposer)

// WithPrompt substitui o prompt de decomposição (default: [plannerprompt.Current]). Um
// prompt SEM FORMA é ignorado (mantém-se o default, que é sempre válido): exige-se
// template não-vazio E versão carimbada (não o sentinela {0,0,0}). Um prompt de versão
// zero carimbaria `planner_meta.prompt_version = "0.0.0"` — o sentinela «não carimbado»
// — furando a proveniência a jusante. Espelha o `Prompt.valid()` (unexported).
func WithPrompt(p plannerprompt.Prompt) Option {
	return func(d *LLMDecomposer) {
		if p.Template != "" && !p.Version.IsZero() {
			d.prompt = p
		}
	}
}

// WithModelID fixa o identificador do modelo carimbado em `planner_meta.model`
// (proveniência). Default: "unknown".
func WithModelID(id string) Option {
	return func(d *LLMDecomposer) {
		if id != "" {
			d.modelID = id
		}
	}
}

// WithCapabilities injecta o CATÁLOGO de capabilities pinado (as tools que o modelo
// pode referenciar por {name,version,digest}), renderizado como texto, que entra na
// mensagem `user`. A sua FONTE — o snapshot do REG correspondente ao
// `capabilities_hash` — é wiring do `aos-orq`; aqui é só o texto a apresentar ao
// modelo. Vazio ⇒ a mensagem não o inclui.
func WithCapabilities(catalog string) Option {
	return func(d *LLMDecomposer) { d.capabilities = catalog }
}

// New constrói um [LLMDecomposer]. `model` é OBRIGATÓRIO (fail-closed com [ErrNoModel]).
func New(model Model, opts ...Option) (*LLMDecomposer, error) {
	if model == nil {
		return nil, ErrNoModel
	}
	d := &LLMDecomposer{
		model:   model,
		prompt:  plannerprompt.Current,
		modelID: "unknown",
	}
	for _, o := range opts {
		if o != nil {
			o(d)
		}
	}
	return d, nil
}

// Decompose implementa [planner.Decomposer]: monta o prompt, chama o modelo, parseia a
// resposta com [plan.Decode] (fail-closed) e devolve o [plan.PlanDocument] *untrusted*
// com a proveniência carimbada pelo Decomposer.
func (d *LLMDecomposer) Decompose(ctx context.Context, in planner.DecomposeInput) (plan.PlanDocument, error) {
	if err := ctx.Err(); err != nil {
		return plan.PlanDocument{}, err
	}
	goal := strings.TrimSpace(in.Context.Goal)
	if goal == "" {
		return plan.PlanDocument{}, ErrEmptyGoal
	}
	capHash := strings.TrimSpace(in.Context.CapabilitiesHash)
	if capHash == "" {
		return plan.PlanDocument{}, ErrNoCapabilitiesHash
	}

	text, err := d.model.Complete(ctx, d.prompt.Template, d.renderUser(goal, capHash))
	if err != nil {
		return plan.PlanDocument{}, fmt.Errorf("decompose: chamada ao modelo: %w", err)
	}
	raw := extractJSON(text)
	if raw == "" {
		return plan.PlanDocument{}, ErrEmptyResponse
	}
	doc, err := plan.Decode([]byte(raw))
	if err != nil {
		return plan.PlanDocument{}, fmt.Errorf("decompose: documento invalido: %w", err)
	}

	// PROVENIÊNCIA (§3.6, AOS-243): os três campos de `planner_meta` são a VERDADE DO
	// DECOMPOSER, não uma alegação do modelo untrusted. Carimbá-los aqui impede um
	// modelo comprometido de forjar a versão do prompt ou o hash do snapshot contra o
	// qual AOS-231 vai validar — se o hash carimbado divergisse do pinado, ou a
	// validação rejeitaria (retry inútil), ou, pior, validaria contra o snapshot
	// errado. [plan.Decode] já garantiu que os três estavam PRESENTES (forma); aqui
	// substituem-se os valores pelos autoritativos.
	doc.PlannerMeta = plan.PlannerMeta{
		Model:            d.modelID,
		PromptVersion:    d.prompt.MetaPromptVersion(),
		CapabilitiesHash: capHash,
	}
	return doc, nil
}

// renderUser monta a mensagem `user` (untrusted): o objectivo, o catálogo de
// capabilities (se fornecido) e os valores de proveniência a carimbar. O template
// (a mensagem `system`) descreve o contrato de schema; o conteúdo VARIÁVEL vive aqui,
// nunca por edição do template — é isso que preserva a cache-estabilidade do prompt
// (ADR-009: o [plannerprompt.Prompt.Fingerprint] tem de ser invariante).
func (d *LLMDecomposer) renderUser(goal, capHash string) string {
	var b strings.Builder
	b.WriteString("OBJECTIVO (untrusted):\n")
	b.WriteString(goal)
	b.WriteString("\n\n")
	if d.capabilities != "" {
		b.WriteString("CAPABILITIES PINADAS (referencia tools SO por {name,version,digest} desta lista):\n")
		b.WriteString(d.capabilities)
		b.WriteString("\n\n")
	}
	b.WriteString("Carimba planner_meta com EXACTAMENTE:\n")
	fmt.Fprintf(&b, "- model: %s\n", d.modelID)
	fmt.Fprintf(&b, "- prompt_version: %s\n", d.prompt.MetaPromptVersion())
	fmt.Fprintf(&b, "- capabilities_hash: %s\n", capHash)
	return b.String()
}

// extractJSON isola o objecto JSON de uma resposta do modelo: o intervalo do PRIMEIRO
// '{' ao ÚLTIMO '}'. É robusto ao caso comum de um modelo que embrulha o objecto em
// cercas markdown (```json … ```), num rótulo de linguagem, ou em prosa antes/depois —
// tudo o que esteja fora das chavetas de topo é ignorado, incluindo uma cerca numa só
// linha (que a extracção por cercas partia). Como um [plan.PlanDocument] é sempre um
// objecto JSON de topo, este intervalo É o documento; lixo lá dentro, ou a ausência de
// chavetas, é recusado fail-closed por [plan.Decode] (ou devolve "" aqui). NÃO tenta
// reparar JSON. Puro.
func extractJSON(text string) string {
	start := strings.IndexByte(text, '{')
	end := strings.LastIndexByte(text, '}')
	if start < 0 || end < start {
		return ""
	}
	return text[start : end+1]
}

// Asserção em compile-time: o Decomposer satisfaz a porta do planeador.
var _ planner.Decomposer = (*LLMDecomposer)(nil)
