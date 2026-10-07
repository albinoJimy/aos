// Package port é o CONTRATO de porta único do Model Gateway (GW) do AOS
// (AOS-055, tecnica/06 §3). Expõe uma superfície NORMALIZADA compatível com a
// API OpenAI — chat/completions (incl. streaming e tool calling) e embeddings —
// que abstrai as diferenças entre provedores heterogéneos (Anthropic, OpenAI,
// Google, self-hosted, endpoints regionais).
//
// É o ÚNICO ponto de dependência dos consumidores: o Agent Runtime e demais
// planos falam com esta porta, nunca com um adaptador ou provider concreto. Os
// adaptadores (ver o pacote adapters) são DETALHE de implementação atrás dela.
//
// # Versionamento (SemVer)
//
// [Version] é a versão SemVer do CONTRATO. Ancorada ao contrato público
// (MAJOR = quebra; MINOR = adição retro-compatível; PATCH = correcção sem
// alteração de contrato), à imagem do gate de SemVer do registry (AOS-052). Um
// swap de modelo/provider NUNCA é silencioso: é um evento de variância explícito
// registado pelo GW (ver [github.com/aos-ref/platform/model-gateway] Gateway).
//
// # Zero dependências externas
//
// Todos os tipos são PRÓPRIOS (sem SDK de provider). A serialização wire (JSON
// snake_case, forma OpenAI) é feita só com encoding/json da stdlib. Nenhum tipo
// aqui importa um SDK de provedor — é essa a razão de ser da porta.
package port

import (
	"context"
	"encoding/json"
)

// Version é a versão SemVer do contrato de porta do GW. Incrementar segundo a
// semântica ancorada a contrato: MAJOR quebra a forma pública dos tipos/métodos,
// MINOR acrescenta de forma retro-compatível, PATCH corrige sem alterar contrato.
const Version = "1.5.0"

// Role é o papel de uma mensagem na conversa (forma OpenAI).
type Role string

const (
	// RoleSystem — instrução de sistema (vai no prefixo cache-estável).
	RoleSystem Role = "system"
	// RoleUser — entrada do utilizador/tarefa.
	RoleUser Role = "user"
	// RoleAssistant — saída do modelo (pode conter tool calls).
	RoleAssistant Role = "assistant"
	// RoleTool — resultado de uma tool devolvido ao modelo (correlacionado por
	// ToolCallID).
	RoleTool Role = "tool"
)

// FunctionCall é a invocação de função pretendida pelo modelo. Arguments é uma
// STRING JSON (forma wire OpenAI), não um objecto — o modelo emite-a como texto e
// o chamador desserializa-a contra o schema da tool.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall é uma tool call pretendida pelo modelo (forma OpenAI). Type é sempre
// "function" nesta versão do contrato.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// Message é uma mensagem da conversa (forma OpenAI). Content é o texto; para
// mensagens RoleTool, ToolCallID correlaciona com a ToolCall respondida. Para
// mensagens RoleAssistant que pedem tools, ToolCalls é preenchido.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	// ReasoningContent é o raciocínio do modelo que o provider devolve com a mensagem
	// (`reasoning_content` do wire), como CARGA OPACA: texto, byte a byte, que o gateway não
	// interpreta (AOS-490, ADR-036 §2.7; campo aditivo, MINOR 1.2.0).
	//
	// SÓ SE LÊ, NÃO SE ENVIA. Chega nas RESPOSTAS ([UnmarshalChatResponse]) e
	// [ChatRequest.MarshalWire] retira-o de todas as mensagens de um PEDIDO, qualquer que
	// seja o chamador: devolvê-lo ao provider custa os tokens do raciocínio em cada turno
	// seguinte e não é exigido pelo provider medido (ticket AOS-490, «Medição de
	// 2026-10-03»). Os blocos de raciocínio assinados e os itens cifrados de outros
	// fornecedores não têm esta forma e ficam fora do contrato.
	//
	// QUALQUER FORMA JSON É ACEITE ([Message.UnmarshalJSON]). O campo não é do protocolo
	// OpenAI e cada fornecedor dá-lhe a sua forma: uma string, um objecto, uma lista de blocos.
	// Uma string JSON guarda-se descodificada; qualquer outra forma guarda-se como os BYTES
	// JSON crus que vieram; `null` ou ausente é vazio. Nunca é erro: uma carga que o gateway
	// não interpreta não pode derrubar a resposta que a transporta.
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// UnmarshalJSON lê uma mensagem do wire. É a leitura de sempre, campo a campo, com UMA diferença:
// `reasoning_content` aceita qualquer valor JSON (AOS-490).
//
// O DEFEITO QUE FECHA. Com o campo declarado como string, uma resposta cujo `reasoning_content`
// fosse um objecto, uma lista ou um número fazia o `encoding/json` recusar a resposta INTEIRA —
// a mensagem, as tool calls e o usage iam com ela. Antes de o campo existir essas respostas eram
// aceites (o campo era ignorado). Afectava todos os consumidores da porta, em qualquer forma de
// pedido.
func (m *Message) UnmarshalJSON(data []byte) error {
	type semMetodos Message // o mesmo layout, sem este UnmarshalJSON (evita a recursão)
	aux := struct {
		*semMetodos
		ReasoningContent json.RawMessage `json:"reasoning_content"`
	}{semMetodos: (*semMetodos)(m)}
	m.ReasoningContent = ""
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	m.ReasoningContent = raciocinioOpaco(aux.ReasoningContent)
	return nil
}

// raciocinioOpaco converte o valor JSON de `reasoning_content` na carga opaca que o contrato
// transporta: uma string JSON ⇒ a string; `null` ou ausente ⇒ vazio; qualquer outra forma ⇒ os
// bytes JSON tal como vieram.
func raciocinioOpaco(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	}
	return string(raw)
}

// FunctionDef descreve uma função disponível ao modelo. Parameters é o JSON
// Schema (opaco) dos argumentos. Espelha o tool set CONGELADO do registry
// (EPIC-05, AOS-050) — a ordem em que as tools são fornecidas é significativa e
// nunca é reordenada pelo GW (estabilidade do prefixo cache, ADR-009).
type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Tool é uma ferramenta disponível ao modelo (forma OpenAI). Type é "function".
type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// Usage é o consumo de uma chamada: os tokens medidos pelo provider e o custo
// DERIVADO deles pelo gateway. Os nomes de token espelham a semconv OTel GenAI
// (gen_ai.usage.*). Os campos de cache alimentam o SLI de cache-hit-rate (AOS-061)
// e a contabilidade de custo (AOS-062).
//
// # As duas naturezas dos campos (importa para quem escreve um adaptador)
//
// Os cinco contadores de token são MEDIDOS: vêm do provider e são preenchidos pelo
// adaptador que traduz a resposta. [Usage.CostMicroUSD] é DERIVADO: nenhum adaptador
// de provider o preenche — é o gateway que o calcula no estágio de metering, a partir
// destes mesmos tokens e da tabela de preços versionada, e o escreve na resposta
// normalizada antes de a devolver.
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	// CacheReadTokens são os tokens de prompt servidos da cache de prefixo do provider — um
	// SUBCONJUNTO de PromptTokens. No wire OpenAI o provider reporta-os em
	// `usage.prompt_tokens_details.cached_tokens`, e é de lá que [UnmarshalChatResponse] os
	// lê (AOS-490); o campo de topo `cache_read_tokens` é a forma própria deste contrato e,
	// quando vem preenchido, prevalece.
	CacheReadTokens  int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
	// CostMicroUSD é o custo DERIVADO desta chamada em MICRO-USD INTEIRO (1 USD =
	// 1_000_000 micro-USD), calculado pela contabilidade do gateway (metering/cost) a
	// partir dos quatro tipos de token e da tabela de preços versionada por (modelo,
	// região). NUNCA float: dinheiro é inteiro em todo o caminho (ADR-008), igual a
	// [budget.Amount] e a [cost.Amount].
	//
	// É o CANAL DE CUSTO ponta a ponta (AOS-259): sem este campo o Agent Runtime somava
	// zeros — a resposta normalizada não tinha por onde trazer o custo que a
	// contabilidade do gateway já derivava, e o span `chat`, o TurnRecord e o burn-down
	// do run mostravam 0 com o metering a funcionar. O adaptador RT→GW projecta-o em
	// agentruntime.ModelResponse.CostMicroUSD, de onde flui para o span e para o evento
	// durável `turn.recorded` que o burn-down lê.
	//
	// TRÊS ESTADOS, NÃO DOIS (AOS-321). Um zero neste campo pode significar três coisas
	// distintas, e quem o lê tem de as distinguir:
	//
	//  1. NÃO-DERIVADO — um gateway sem contabilidade de custo composta (sem recorder)
	//     deixa o campo a zero. Ausência de contabilidade, não custo nulo.
	//  2. DERIVADO e efectivamente zero — houve usage medido e a tabela de preços
	//     produziu zero (tokens a zero com preço conhecido). É um facto contabilizado.
	//  3. INDEFINIDO — o provedor devolveu 200 e NÃO reportou objecto `usage` nenhum.
	//     Não há de onde derivar custo. Este estado é agora visível em [Usage.Ausente]:
	//     não desce disfarçado de zero para o agregado por run/árvore nem para o evento
	//     durável `turn.recorded`.
	//
	// Um custo NÃO-CALCULÁVEL (sem preço para o par (modelo, região), tokens negativos,
	// overflow) continua a NÃO chegar aqui como zero — é fail-closed no metering (erro
	// atribuível), precisamente para que um zero silencioso nunca falsifique o burn-down.
	CostMicroUSD int64 `json:"cost_micro_usd,omitempty"`

	// Ausente marca que o provedor NÃO reportou objecto `usage` nenhum — o estado (3)
	// acima. É a distinção que faltava entre «o provedor disse zero» e «o provedor não
	// disse nada» (AOS-321), e vive no TIPO em vez de numa convenção sobre o valor dos
	// contadores.
	//
	// POLARIDADE DELIBERADA: o valor-zero do campo é «definido». Só o desserializador do
	// wire ([UnmarshalChatResponse], [UnmarshalEmbeddingsResponse]) e os agregadores de
	// streaming ([CollectStream]) marcam a ausência, porque é aí — e só aí — que a
	// ausência EXISTE de facto no material recebido. Um [Usage] construído em código
	// (adaptador in-memory, composição, teste) afirma o que escreve e continua definido:
	// a marca é ADITIVA e não reinterpreta nenhum consumidor existente.
	//
	// NÃO vai no wire (`json:"-"`): é um facto sobre a MENSAGEM RECEBIDA, não um campo
	// do protocolo do provedor. Serializá-lo re-introduziria a ambiguidade num round-trip.
	Ausente bool `json:"-"`
}

// Definido reporta se este usage tem contadores em que se possa confiar — isto é,
// se o provedor reportou de facto o objecto `usage`.
//
// É o MOLDE de [github.com/aos-ref/platform/model-gateway/metering/cache_sli.CallRate],
// que trata um denominador ausente como SLI indefinido e nunca como 0%: uma amostra
// indefinida é OMITIDA da agregação, não contada como zero (que envenenaria o sinal).
// A contabilidade de custo passa a ter a mesma disciplina.
func (u Usage) Definido() bool {
	if u.Ausente {
		return false
	}
	// DUAS FORMAS DE AUSÊNCIA, e a segunda escapou à primeira versão de AOS-321.
	//
	// A revisão adversarial mediu-o na composição de produção: `{"usage":{}}` e
	// `{"usage":{"total_tokens":1500}}` traziam o OBJECTO — logo `Ausente` era falso —
	// mas nenhum contador que o [github.com/aos-ref/platform/model-gateway/metering/cost]
	// saiba ler, porque o cálculo é sobre prompt/completion/cache e não sobre
	// `total_tokens`. O custo saía 0 e era AGREGADO por run e por árvore. São exactamente
	// as formas que proxies OpenAI-compatible e variantes com content-filter produzem, pelo
	// que o dano que o ticket declarava fechado continuava aberto para elas.
	//
	// UM 200 COM ZERO PROMPT TOKENS NÃO É UMA MEDIÇÃO. Não existe chamada de chat ou de
	// embeddings sem entrada: há sempre system+user, há sempre input. Zero prompt tokens é
	// «ausência de dados disfarçada de leitura» — a expressão é do próprio
	// [ErrBurndownNoUsage] do nó, sobre o mesmo defeito uma camada acima.
	//
	// É o MESMO critério de `cache_sli.CallRate`, que trata `PromptTokens <= 0` como SLI
	// indefinido e nunca como 0%: sem denominador não há leitura. A contabilidade de custo
	// passa a partilhá-lo em vez de o imitar por metade.
	return u.PromptTokens > 0
}

// ChatRequest é o pedido de chat/completions NORMALIZADO. Os campos de controlo
// de plataforma (Principal, Region, Board) NÃO fazem parte do wire enviado ao
// provider — são consumidos pela pipeline determinística do GW (auth, allowlist,
// roteamento) e nunca chegam ao provedor nem a logs/spans como segredo.
type ChatRequest struct {
	// Model é o modelo PEDIDO. O modelo efectivamente usado pode diferir (swap),
	// caso em que o GW regista um evento de variância explícito.
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
	// ToolChoice controla a escolha de tool ("auto"|"none"|"required"|nome).
	ToolChoice string `json:"tool_choice,omitempty"`
	Stream     bool   `json:"stream,omitempty"`
	// Temperature/Seed/MaxTokens são parâmetros de amostragem. Ponteiros para
	// distinguir "não definido" de zero (determinismo do wire).
	Temperature *float64 `json:"temperature,omitempty"`
	Seed        *int64   `json:"seed,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`

	// --- Metadados de plataforma (NÃO vão no wire do provider) ---
	// Principal é o token scoped/time-bound do par (utilizador, agente). Opaco
	// aqui: a validação forte é AOS-057. Nunca é serializado para o provider.
	Principal string `json:"-"`
	// Region/Board são a fronteira de soberania alvo (consumidos por AOS-058).
	Region string `json:"-"`
	Board  string `json:"-"`
	// RunID correlaciona a chamada com a TRAJECTÓRIA do agente (run). Metadado de
	// plataforma (nunca no wire): é o eixo de agregação do SLI de cache-hit-rate
	// (AOS-061, por run/tenant) e liga a métrica/atribuição à trajectória (ADR-010).
	RunID string `json:"-"`
	// StepID correlaciona a chamada com o PASSO do run que a fez (o step_id do turno, o mesmo
	// dos checkpoints e do turn.recorded). Metadado de plataforma (nunca no wire): entra nos
	// selos de governação do GW (AOS-394). Vazio se o chamador não o fornece. Campo aditivo
	// (MINOR 1.1.0).
	StepID string `json:"-"`
	// TreeID correlaciona a chamada com a ÁRVORE de runs (o run-tree/agent-tree a que
	// o run pertence). Metadado de plataforma (nunca no wire): é o eixo de agregação
	// do custo por ÁRVORE (AOS-062) que alimenta o burn-down e o admission GLOBAL
	// (ADR-008, EPIC-03). Vazio se o chamador não o fornece (a agregação por run
	// mantém-se).
	TreeID string `json:"-"`
}

// Choice é uma escolha da resposta de chat (forma OpenAI).
type Choice struct {
	Index   int     `json:"index"`
	Message Message `json:"message"`
	// FinishReason é o `finish_reason` do provider, TAL COMO VEIO: a porta não o interpreta
	// nem o restringe. Os valores que o contrato conhece são as constantes Finish* abaixo;
	// um provider pode mandar outro texto, ou nenhum. Quem o leva para fora do gateway — o
	// adaptador do runtime — normaliza-o num vocabulário fechado e não propaga o valor bruto
	// (AOS-491).
	FinishReason string `json:"finish_reason"`
}

// Valores de [Choice.FinishReason] que o contrato conhece (AOS-491; aditivo, MINOR 1.3.0). São
// os do wire OpenAI. Não é um vocabulário fechado da porta — o campo continua a ser o texto do
// provider —, é o mapa a partir do qual o adaptador do runtime normaliza.
const (
	// FinishStop — o modelo parou por si.
	FinishStop = "stop"
	// FinishToolCalls — o modelo parou para pedir tool calls.
	FinishToolCalls = "tool_calls"
	// FinishFunctionCall — a forma antiga de [FinishToolCalls] (a API de `functions`).
	FinishFunctionCall = "function_call"
	// FinishLength — a resposta foi cortada pelo limite de tokens.
	FinishLength = "length"
	// FinishContentFilter — a resposta foi retida ou cortada por um filtro de conteúdo.
	FinishContentFilter = "content_filter"
)

// ChatResponse é a resposta de chat/completions NORMALIZADA. Model é o modelo
// EFECTIVAMENTE usado (pode diferir de ChatRequest.Model num swap).
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
	// Route é o que o proxy à frente do provider DECLAROU sobre a rota que serviu esta resposta
	// (AOS-505; campo aditivo, MINOR 1.4.0). Não é do corpo: vem dos cabeçalhos da resposta HTTP,
	// e por isso não vai no wire (`json:"-"`). Ver [ServedRoute]. O caminho de streaming não o
	// preenche.
	Route ServedRoute `json:"-"`
	// Shape é a ficha da forma do corpo desta resposta (AOS-507; campo aditivo, MINOR 1.5.0): ver
	// [ResponseShape]. Não é do corpo — é uma medição SOBRE o corpo —, e por isso não vai no wire
	// (`json:"-"`). nil quando o adaptador não a mede, que é a omissão. O caminho de streaming
	// não a preenche.
	Shape *ResponseShape `json:"-"`
}

// ChatStreamDelta é um incremento (delta) do streaming de chat. Content é o
// fragmento de texto deste chunk; ToolCalls são fragmentos de tool call
// (agregados por Index); FinishReason não-vazio marca o fim lógico.
type ChatStreamDelta struct {
	Role         Role            `json:"role,omitempty"`
	Content      string          `json:"content,omitempty"`
	ToolCalls    []ToolCallDelta `json:"tool_calls,omitempty"`
	FinishReason string          `json:"finish_reason,omitempty"`
	// Usage é preenchido apenas no chunk final (forma OpenAI stream_options).
	Usage *Usage `json:"usage,omitempty"`
}

// ToolCallDelta é um fragmento de tool call no streaming. ArgumentsFragment é
// concatenado (por Index) para formar a string JSON de argumentos completa.
type ToolCallDelta struct {
	Index             int    `json:"index"`
	ID                string `json:"id,omitempty"`
	Name              string `json:"name,omitempty"`
	ArgumentsFragment string `json:"arguments,omitempty"`
}

// ChatStream é o iterador de deltas do streaming. Recv devolve o próximo delta
// ou [io.EOF] quando o stream termina; Close liberta recursos (idempotente).
type ChatStream interface {
	Recv() (ChatStreamDelta, error)
	Close() error
}

// EmbeddingsRequest é o pedido de embeddings NORMALIZADO. Input é a lista de
// textos a vectorizar. Os metadados de plataforma seguem a mesma regra de
// ChatRequest (não vão no wire).
type EmbeddingsRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`

	Principal string `json:"-"`
	Region    string `json:"-"`
	Board     string `json:"-"`
	// RunID correlaciona a chamada com a trajectória do agente (ver [ChatRequest]).
	RunID string `json:"-"`
	// StepID correlaciona a chamada com o passo do run (ver [ChatRequest]).
	StepID string `json:"-"`
	// TreeID correlaciona a chamada com a árvore de runs (ver [ChatRequest]) — o eixo
	// de agregação do custo por árvore que alimenta o burn-down global (AOS-062).
	TreeID string `json:"-"`
}

// Embedding é um vector de embedding de um input (forma OpenAI).
type Embedding struct {
	Index     int       `json:"index"`
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"`
}

// EmbeddingsResponse é a resposta de embeddings NORMALIZADA.
type EmbeddingsResponse struct {
	Object string      `json:"object"`
	Model  string      `json:"model"`
	Data   []Embedding `json:"data"`
	Usage  Usage       `json:"usage"`
}

// Gateway é a PORTA ÚNICA compatível OpenAI — o contrato que os consumidores
// (Agent Runtime, etc.) usam para TODA a invocação de modelo. É o gate
// obrigatório: nenhum caminho de código fora do GW fala com um provider (imposto
// pelo arch-lint do pacote archlint).
type Gateway interface {
	// PortVersion devolve a versão SemVer do contrato ([Version]).
	PortVersion() string
	// Chat executa uma chamada de chat/completions síncrona.
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	// ChatStream executa uma chamada de chat/completions em streaming.
	ChatStream(ctx context.Context, req ChatRequest) (ChatStream, error)
	// Embeddings executa uma chamada de embeddings.
	Embeddings(ctx context.Context, req EmbeddingsRequest) (EmbeddingsResponse, error)
}
