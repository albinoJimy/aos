package otelgenai

// Este ficheiro é a FONTE ÚNICA DA VERDADE do vocabulário OTel GenAI semconv do
// AOS. Os nomes são EXACTAMENTE os da convenção (gen_ai.*/error.type) e os
// atributos próprios do AOS usam o prefixo aos.*. O adaptador OTel real (o SDK
// go.opentelemetry.io — adapter de deployment DIFERIDO, ver doc.go) mapeia estas
// strings para attribute.Key/instrument.Name SEM renomear.
//
// NOTA gosec (G101): as chaves gen_ai.usage.input_tokens/output_tokens contêm a
// subcadeia "token" e disparam um FALSO-POSITIVO de "hardcoded credential" no
// gosec. Não são segredos — são nomes de atributo de telemetria. O baseline SCA/
// SAST do agent-runtime já as triava; ao passarem a viver neste módulo folha, o
// baseline deste módulo pode precisar de idêntica triagem (sinalizado no handoff;
// não editar baseline aqui).
const (
	// AttrOperationName — gen_ai.operation.name ("invoke_agent"|"chat"|"execute_tool").
	AttrOperationName = "gen_ai.operation.name"
	// AttrRequestModel — gen_ai.request.model (model_id do turno).
	AttrRequestModel = "gen_ai.request.model"
	// AttrInputTokens — gen_ai.usage.input_tokens.
	AttrInputTokens = "gen_ai.usage.input_tokens"
	// AttrOutputTokens — gen_ai.usage.output_tokens.
	AttrOutputTokens = "gen_ai.usage.output_tokens"
	// AttrCostUSD — custo do span em USD (ADR-010). A semconv GenAI não fixa uma
	// chave de custo estável; usamos "gen_ai.usage.cost_usd" como atributo do AOS
	// (valor float em USD, derivado do micro-USD inteiro). A contabilidade fina de
	// custo/tokens é AOS-078; aqui a chave existe só para o mapa de atributos.
	AttrCostUSD = "gen_ai.usage.cost_usd"
	// AttrCostMicroUSD — custo EXACTO do span em micro-USD int64 (1 USD = 1_000_000),
	// emitido em PARALELO com [AttrCostUSD] (float, conveniência OTel). É a FONTE DE
	// VERDADE do custo por span: a agregação por trajectória/sub-árvore (AOS-078) soma
	// SEMPRE este inteiro para não acumular drift de vírgula flutuante. Coincide com a
	// chave "aos.cost.micro_usd" que o Model Gateway (metering/cost) já emite no seu
	// span de custo — a mesma chave, aqui elevada à fonte única do vocabulário. Não é
	// segredo (custo não é credencial, ADR-010).
	AttrCostMicroUSD = "aos.cost.micro_usd"
	// AttrToolName — gen_ai.tool.name (span execute_tool).
	AttrToolName = "gen_ai.tool.name"
	// AttrPrincipalNHI — aos.principal.nhi_id: o identificador estável da NHI do
	// principal que executa o span. Obrigatório no span chat (CA1 de AOS-076: o
	// turno de modelo identifica QUEM o executa) e útil no invoke_agent. É um
	// identificador de identidade, nunca um segredo/credencial (ADR-006).
	AttrPrincipalNHI = "aos.principal.nhi_id"
	// AttrRunID — correlação da trajectória (run_id → trace).
	AttrRunID = "aos.run_id"
	// AttrStepID — correlação do passo (step_id → span).
	AttrStepID = "aos.step_id"
	// AttrPromptHash — hash do prompt materializado do turno (âncora de replay).
	AttrPromptHash = "aos.prompt_hash"
	// AttrPrefixHash — hash do PREFIXO cache-estável (system + tool set congelado).
	// Byte-idêntico entre turnos do mesmo run: comparar prefix_hash entre turnos
	// torna o cache-hit-rate do prefixo OBSERVÁVEL por telemetria.
	AttrPrefixHash = "aos.prefix_hash"
	// AttrToolCallHash — aos.tool_call.hash: hash ESTÁVEL sha256(tool+args) da tool
	// call, anotado no span execute_tool pelo Reference Monitor. É a âncora de
	// action-dedup do circuit breaker (AOS-081) e a referência de content-capture
	// desta fase (só o HASH, nunca os valores dos args — o payload é AOS-079).
	AttrToolCallHash = "aos.tool_call.hash"
	// AttrErrorType — error.type (semconv OTel): tipo/condição de erro do span. No
	// span execute_tool marca que a tool PERMITIDA falhou em runtime, distinguindo
	// um output vazio legítimo de um output de tool falhada.
	AttrErrorType = "error.type"
	// AttrTaint — aos.taint: o rótulo de taint da AUTORIZAÇÃO da tool call anotado no
	// span execute_tool. Torna a decisão de taint OBSERVÁVEL directamente do span —
	// não só do evento de mediação durável. É o rótulo de confiança, nunca o
	// conteúdo: o Input da tool jamais é gravado no span (ADR-005). Distingue-se de
	// [AttrResultTaint]: aqui é a proveniência do PLANO que autorizou (pode ser
	// "trusted" ou "untrusted"), não a marca do resultado.
	AttrTaint = "aos.taint"
	// AttrResultTaint — aos.tool.result_taint: a marca do RESULTADO da tool call. É
	// SEMPRE "untrusted" (ADR-005) qualquer que seja o veredicto do RM — o output de
	// qualquer tool volta ao loop como conteúdo não-confiável. Ao contrário de
	// [AttrTaint] (a taint da autorização, que pode ser "trusted"), esta é a garantia
	// invariante que CA2 de AOS-076 exige no span execute_tool.
	AttrResultTaint = "aos.tool.result_taint"
	// AttrDeniedBy — aos.decision.denied_by: o nome do hook que negou/escalou,
	// anotado no span execute_tool apenas quando a decisão não é permit.
	AttrDeniedBy = "aos.decision.denied_by"
	// AttrDecision — aos.decision: o efeito da mediação (permit|deny|escalate|error)
	// anotado no span execute_tool, para leitura directa do veredicto no span.
	AttrDecision = "aos.decision"
	// AttrTenantID — aos.tenant_id: o tenant (organização/projecto) sob o qual a
	// unidade de trabalho corre. NÃO vive no span de modelo hoje (o eixo de
	// isolamento é a NHI do principal, ADR-006); entra no wide event (AOS-082) por
	// ENRIQUECIMENTO no emit-time do diagnóstico, permitindo a agregação ad-hoc
	// "custo por tenant e por modelo" sem reinstrumentar. É um rótulo de identidade
	// de tenant, nunca um segredo/credencial.
	AttrTenantID = "aos.tenant_id"
)

// PinnedVersionPrefix é o prefixo das dimensões de VERSÃO PINADA do manifesto
// (skills/tools/memória) que enriquecem o wide event (AOS-082): p.ex.
// "aos.pinned.skill", "aos.pinned.tool", "aos.pinned.memory". Vivem no manifesto
// do run, não no span de modelo; entram no wide event por enriquecimento. O
// [WideEvent] recolhe TODAS as chaves com este prefixo no seu mapa
// PinnedVersions (derivado), sem fixar o conjunto exacto de dimensões — assim uma
// versão pinada nova (p.ex. aos.pinned.policy) aparece sem alterar este módulo.
const PinnedVersionPrefix = "aos.pinned."

// Nomes de operação da semconv GenAI (gen_ai.operation.name).
const (
	// OpInvokeAgent — span que envolve o run/nível de delegação inteiro.
	OpInvokeAgent = "invoke_agent"
	// OpChat — span de uma chamada ao modelo (turno).
	OpChat = "chat"
	// OpExecuteTool — span de uma tool call despachada via Reference Monitor.
	OpExecuteTool = "execute_tool"
)

// MicroUSDToUSD converte micro-USD inteiro para USD (float, só para o atributo de
// span; o burn-down interno mantém-se inteiro). Exposto para os instrumentadores
// (RT/RM) derivarem AttrCostUSD sem duplicar a conversão.
func MicroUSDToUSD(microUSD int64) float64 { return float64(microUSD) / 1_000_000.0 }
