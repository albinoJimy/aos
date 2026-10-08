package modelgateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/port"
)

// ModelClientAdapter adapta o [Gateway] à porta [agentruntime.ModelClient] do
// Agent Runtime (AOS-013). É a ligação canónica RT → GW: o loop do runtime tem
// uma porta ModelClient mínima (PromptView → ModelResponse); o GW é a
// implementação/fachada real por detrás dela. Traduz a [agentruntime.PromptView]
// materializada para o contrato compatível OpenAI e a [port.ChatResponse] de
// volta para [agentruntime.ModelResponse].
//
// Assim o RT continua a depender só da SUA porta (ModelClient) e o GW da SUA
// (port.Gateway) — o adaptador reconcilia os dois contratos sem que nenhum
// dependa do outro.
type ModelClientAdapter struct {
	gw      port.Gateway
	model   string
	tools   []port.Tool
	region  string
	board   string
	princip string
	// principCtx SOURCE o token do principal do CONTEXTO por-chamada (AOS-278). Quando
	// != nil e devolve um valor não-vazio, esse valor TEM PRECEDÊNCIA sobre [princip]: é
	// como a identidade REAL do RUN (o token NHI de Goal.Credential, o mesmo que cada tool
	// call mediada verifica) chega ao estágio authn do GW, que é construção-time e nível-nó
	// (não sabe qual run serve). Vazio/ausente ⇒ cai para [princip] (a omissão sob o cutover
	// duro é ""), e o estágio authn nega ATRIBUÍVELMENTE — nunca se forja um principal.
	principCtx func(context.Context) string
	// ofertaCtx SOURCE do CONTEXTO por-chamada a lista-branca de tools do RUN (AOS-486). O
	// tool set de [WithTools] é do NÓ e fixa-se uma vez; o que cada run pode chamar só o ctx
	// sabe. Ver [WithToolOfferFromContext].
	ofertaCtx func(context.Context) (permite func(nome string) bool, restrito bool)
	runID     string
	// nativa: a projecção CONFIGURADA é a de mensagens nativas ([WithProjection], AOS-490).
	// false — o valor-zero — é o texto único, a forma de sempre.
	nativa bool
	// versaoNativa: a versão da projecção nativa a usar ([WithProjectionVersion], AOS-504). Vazia
	// — o valor-zero — é [NativeProjectionVersion], a de sempre.
	versaoNativa string
	// formaObs recebe os rótulos da ficha da forma de cada turno que a traz
	// ([WithResponseShapeObserver], AOS-507). nil ⇒ ninguém observa.
	formaObs ResponseShapeObserver
	// rejeicaoObs recebe a causa de cada resposta recusada ([WithResponseRejectedObserver],
	// AOS-509). nil ⇒ ninguém observa.
	rejeicaoObs ResponseRejectedObserver
	// estado: a captura do estado opaco do provider ([WithProviderStateCapture], AOS-514). nil —
	// o valor-zero — é o adaptador de sempre: o estado que a resposta traga não atravessa.
	estado *capturaDoEstado
}

// Compile-time: o adaptador satisfaz a porta do runtime.
var _ agentruntime.ModelClient = (*ModelClientAdapter)(nil)

// RuntimeAdapterOption configura o [ModelClientAdapter].
type RuntimeAdapterOption func(*ModelClientAdapter)

// WithTools congela o tool set (do registry, EPIC-05) exposto ao modelo.
func WithTools(tools []port.Tool) RuntimeAdapterOption {
	return func(a *ModelClientAdapter) { a.tools = tools }
}

// WithPrincipal define o token scoped do principal (validação forte é AOS-057).
func WithPrincipal(token string) RuntimeAdapterOption {
	return func(a *ModelClientAdapter) { a.princip = token }
}

// WithPrincipalFromContext liga a fonte POR-CHAMADA do token do principal (AOS-278):
// o adaptador é construído UMA vez ao nível do nó, mas a identidade a apresentar ao
// estágio authn do GW é a do RUN, e essa só se conhece por-chamada — viaja no ctx que
// flui de Run(ctx, goal) até Call(ctx, view), a MESMA mecânica por-run que o plano de
// replay usa (ver resume_model.go). fn lê esse valor do ctx; um valor não-vazio tem
// precedência sobre [WithPrincipal]. É o que estende ao turno de modelo a identidade
// real que as tool calls já verificam. fn nil ⇒ opção inerte.
func WithPrincipalFromContext(fn func(context.Context) string) RuntimeAdapterOption {
	return func(a *ModelClientAdapter) {
		if fn != nil {
			a.principCtx = fn
		}
	}
}

// WithToolOfferFromContext liga a fonte POR-CHAMADA da lista-branca de tools do run (AOS-486),
// no molde de [WithPrincipalFromContext]: o adaptador é construído uma vez por nó com o tool
// set do nó ([WithTools]), e o que um run pode chamar só se conhece por-chamada, no ctx que
// flui de Run(ctx, goal) até Call(ctx, view).
//
// fn devolve o predicado «o run pode chamar esta tool» e o indicador `restrito`:
//
//   - restrito == false ⇒ o run não tem lista-branca: o pedido leva o tool set do nó TAL COMO
//     foi fixado (o mesmo slice — o pedido fica byte-idêntico ao de um adaptador sem esta opção);
//   - restrito == true ⇒ o pedido leva só as tools cujo nome o predicado admite, pela ORDEM em
//     que o nó as fixou. Nenhuma admitida ⇒ nenhum schema (o campo `tools` é omitido).
//
// O indicador é EXPLÍCITO de propósito: a diferença entre «sem lista» e «lista vazia» é a que
// separa oferecer tudo de não oferecer nada, e uma lista vazia que perdesse a identidade ao
// atravessar o contexto (uma cópia que a tornasse nil) passava a oferecer tudo. Um predicado
// nil com restrito == true resolve pelo lado seguro: nenhuma tool.
//
// O que é oferecido NÃO substitui a recusa: a lista-branca continua imposta pelo Reference
// Monitor em cada chamada (AOS-413/AOS-485). Isto só evita mostrar ao modelo uma tool que lhe
// seria negada. fn nil ⇒ opção inerte.
func WithToolOfferFromContext(fn func(context.Context) (permite func(nome string) bool, restrito bool)) RuntimeAdapterOption {
	return func(a *ModelClientAdapter) {
		if fn != nil {
			a.ofertaCtx = fn
		}
	}
}

// toolsOferecidas devolve o tool set que o pedido deste run leva — ver
// [WithToolOfferFromContext]. Sem fonte, ou com um run sem lista-branca, é `a.tools` intocado.
func (a *ModelClientAdapter) toolsOferecidas(ctx context.Context) []port.Tool {
	if a.ofertaCtx == nil {
		return a.tools
	}
	permite, restrito := a.ofertaCtx(ctx)
	if !restrito {
		return a.tools
	}
	var out []port.Tool
	for _, t := range a.tools {
		if permite != nil && permite(t.Function.Name) {
			out = append(out, t)
		}
	}
	return out
}

// WithRegionBoard define a fronteira de soberania alvo (consumida por AOS-058).
func WithRegionBoard(region, board string) RuntimeAdapterOption {
	return func(a *ModelClientAdapter) { a.region, a.board = region, board }
}

// WithRun correlaciona as chamadas deste adaptador com a TRAJECTÓRIA (run) do
// agente: o runID entra em cada [port.ChatRequest] e torna-se o eixo de agregação
// do SLI de cache-hit-rate (AOS-061, por run/tenant) e a ligação da atribuição à
// trajectória (ADR-010). Só serve a um adaptador construído POR RUN: o run que o
// runtime anexa ao ctx de cada chamada ([agentruntime.ContextWithModelCall],
// AOS-394) tem precedência, e é esse o caminho de um adaptador construído por nó.
func WithRun(runID string) RuntimeAdapterOption {
	return func(a *ModelClientAdapter) { a.runID = runID }
}

// WithProjection escolhe a forma em que o adaptador envia o prompt ao provider (AOS-490, ADR-036
// §2.4): [ProjectionText] — o prompt materializado numa mensagem de utilizador, a forma de
// sempre e a do adaptador sem esta opção — ou [ProjectionNative] — mensagens nativas derivadas
// do tail ([ProjectNative]).
//
// A nativa só se aplica a um turno montado num layout que a projecção cobre (a 1.4.0 — ver
// [projecaoNativaSuporta]); um run fixado na 1.3.0 vai SEMPRE em texto único, byte a byte como
// antes, qualquer que seja a configuração. O modo EFECTIVAMENTE usado em cada turno volta na
// resposta ([agentruntime.ModelResponse.Projection]) e fica no manifesto do turno.
//
// O modo tem de ser do vocabulário fechado ([ParseProjection]); um valor desconhecido é
// ignorado aqui — fica o texto único —, e é a quem lê a configuração que cabe recusá-lo.
func WithProjection(mode string) RuntimeAdapterOption {
	return func(a *ModelClientAdapter) {
		if m, err := ParseProjection(mode); err == nil {
			a.nativa = m == ProjectionNative
		}
	}
}

// WithProjectionVersion escolhe a VERSÃO da projecção nativa (AOS-504, emenda ao ADR-036 §2.4):
// [NativeProjectionVersion] — a de sempre, e a do adaptador sem esta opção — ou
// [NativeProjectionVersion110] — linha de fim por segmento e o texto de protocolo novo —, ou
// [NativeProjectionVersion120] (AOS-506) — a 1.1.0 com o texto de protocolo que diz que uma tool
// só se pede pelo mecanismo nativo de function calling.
//
// Só tem efeito num turno que vá em projecção nativa ([WithProjection] e um layout coberto): em
// texto único não há projecção nem versão dela. A versão EFECTIVAMENTE usada em cada turno
// volta na resposta ([agentruntime.ModelResponse.ProjectionVersion]) e fica no manifesto desse
// turno — um run em curso quando a configuração muda pode ter turnos em versões diferentes, e
// cada um grava a sua.
//
// A versão tem de ser do vocabulário fechado ([ParseNativeProjectionVersion]); um valor
// desconhecido é ignorado aqui — fica a de sempre —, e é a quem lê a configuração que cabe
// recusá-lo.
func WithProjectionVersion(version string) RuntimeAdapterOption {
	return func(a *ModelClientAdapter) {
		if v, err := ParseNativeProjectionVersion(version); err == nil {
			a.versaoNativa = v
		}
	}
}

// NewModelClient constrói o adaptador RT→GW para um modelo dado.
func NewModelClient(gw port.Gateway, model string, opts ...RuntimeAdapterOption) *ModelClientAdapter {
	a := &ModelClientAdapter{gw: gw, model: model}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Call implementa [agentruntime.ModelClient]: materializa o pedido a partir da
// PromptView, invoca o GW (que atravessa a pipeline determinística) e traduz a
// resposta normalizada de volta para o runtime.
func (a *ModelClientAdapter) Call(ctx context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	// IDENTIDADE POR-RUN (AOS-278): a fonte de ctx (o token NHI do run) tem precedência
	// sobre o token de construção. Ausente/vazia ⇒ fica o de construção (sob o cutover duro,
	// ""), e o estágio authn do GW nega atribuívelmente — nenhum principal é forjado.
	principal := a.princip
	if a.principCtx != nil {
		if p := a.principCtx(ctx); p != "" {
			principal = p
		}
	}
	// CORRELAÇÃO POR-CHAMADA (AOS-394): o run e o passo do turno vêm do ctx que o runtime
	// escreve antes de chamar o modelo. O par lê-se JUNTO: havendo anexo, é ele que vale
	// INTEIRO; não havendo, fica o [WithRun] de construção (um adaptador por run) e o passo
	// segue vazio. A precedência é sobre o PAR e não sobre cada campo de propósito — completar
	// o run de uma fonte com o passo de outra selaria uma correlação que nunca existiu, que é
	// precisamente o que este ticket proíbe. Sem nenhuma das duas fontes os campos seguem
	// vazios e o selo mostra a ausência.
	runID, stepID, ok := agentruntime.ModelCallFromContext(ctx)
	if !ok {
		runID, stepID = a.runID, ""
	}
	// A FORMA DO PEDIDO (AOS-490). Texto único: o prompt materializado numa mensagem `user` —
	// os bytes com hash. Nativa: as mensagens derivadas do tail, só quando o nó a configurou E
	// o turno foi montado num layout que a projecção cobre. Uma projecção que falha não cai
	// para o texto: o erro sobe e o turno falha de forma atribuível, em vez de o run mudar de
	// forma a meio sem que nada o registe.
	msgs := []port.Message{{Role: port.RoleUser, Content: string(view.Materialized)}}
	nativa := a.nativa && projecaoNativaSuporta(view.AssemblyVersion)
	versaoNativa := a.versaoNativa
	if versaoNativa == "" {
		versaoNativa = NativeProjectionVersion
	}
	if nativa {
		var perr error
		if msgs, perr = ProjectNativeVersion(versaoNativa, view); perr != nil {
			return agentruntime.ModelResponse{}, perr
		}
	}
	req := port.ChatRequest{
		Model:     a.model,
		Messages:  msgs,
		Tools:     a.toolsOferecidas(ctx),
		Principal: principal,
		Region:    a.region,
		Board:     a.board,
		RunID:     runID,
		StepID:    stepID,
	}
	resp, err := a.gw.Chat(ctx, req)
	if err != nil {
		// AOS-509: uma resposta recusada conta com a sua causa; o erro sobe como subia.
		a.observarRejeicao(err)
		return agentruntime.ModelResponse{}, err
	}
	out, err := translateResponse(resp)
	if err != nil {
		a.observarRejeicao(err)
		return agentruntime.ModelResponse{}, err
	}
	if nativa {
		// O adaptador declara a forma que USOU; o runtime grava-a no manifesto do turno. O
		// texto único não declara nada — o manifesto fica com os bytes de antes.
		out.Projection, out.ProjectionVersion = ProjectionNative, versaoNativa
	}
	// AOS-491 — quantas tools ESTE pedido ofereceu ao modelo: os schemas que foram no campo
	// `tools`, depois do corte pela lista-branca do run. É o que separa, no registo, um turno
	// sem tool calls de um modelo que as tinha à disposição de um que não tinha nenhuma.
	out.ToolsOffered = len(req.Tools)
	// AOS-514 — o estado opaco do provider, fechado num envelope com a rota a que pertence. Só
	// com a captura ligada; não muda mais nada na resposta (o texto e as tool calls já estão
	// traduzidos, e não vêm daqui).
	a.estado.capturar(&out, resp, a.model)
	// AOS-507 — a ficha da forma (nil com a medição desligada) conta na métrica de quem observa.
	observarForma(a.formaObs, out.Shape, out.StopReason)
	return out, nil
}

// ErrRespostaSemChoices — o gateway devolveu 200 e o corpo não produziu nenhuma escolha.
//
// O DEFEITO QUE ISTO FECHA, encontrado na varredura adversarial de 2026-08-21. O tradutor tratava
// `len(Choices) == 0` como um turno FINAL sem erro, e a cadeia a jusante selava o run como
// CONCLUÍDO COM SUCESSO:
//
//	Choices vazio ⇒ Final=true, err=nil ⇒ res.Terminated ⇒ running → complete (run_complete)
//
// Reproduzido pela função de composição de produção com cinco corpos diferentes — incluindo um
// PAYLOAD DE ERRO do provider devolvido com 200, que é o que proxies compatíveis com OpenAI fazem.
// Sem compensação (a saga só dispara a partir de `failed`) e sem sinal para o operador: no log
// durável, a avaria era indistinguível de um run que respondeu e concluiu.
//
// PORQUE É ERRO E NÃO CONCLUSÃO. Uma resposta de chat bem-formada tem SEMPRE pelo menos uma
// escolha. `choices` vazio ou ausente não é «um turno legitimamente vazio» — é uma resposta
// malformada, e o nó não tem como distinguir «o modelo não disse nada» de «o provider avariou».
// Fail-closed: assume-se o segundo.
//
// A POSTURA JÁ EXISTIA NO RAMO DE STREAMING e estava invertida aqui. O `CollectStream` sintetiza
// uma escolha para um fluxo que ENTREGOU conteúdo e só lhe falta o marcador terminal — caso
// diferente —, e o [adapters.ErrTruncatedStream] existe precisamente para recusar fabricar uma
// conclusão limpa a partir de um fluxo que terminou mal. É essa a regra; o caminho síncrono
// deixa de ser a excepção.
//
// LIMITE DECLARADO: um `finish_reason` VAZIO com uma escolha presente continua a contar como
// final. É outro caso — há conteúdo — e mudá-lo partiria providers que o omitem legitimamente.
// Fica nomeado em vez de arrastado nesta correcção.
var ErrRespostaSemChoices = errors.New("model-gateway: o gateway respondeu sem nenhuma escolha — resposta malformada, nao um turno vazio (um 200 com corpo de erro do provider chega aqui assim)")

// translateResponse converte [port.ChatResponse] em [agentruntime.ModelResponse].
//
// AOS-259 — É AQUI QUE O CANAL DE CUSTO ATRAVESSA A FRONTEIRA RT↔GW. O custo derivado
// pela contabilidade do gateway viaja em [port.Usage.CostMicroUSD] e projecta-se em
// [agentruntime.ModelResponse.CostMicroUSD], que o runtime JÁ consome em três sítios
// que até aqui recebiam zero: o acumulado do run (Result.TotalCostMicroUSD), o atributo
// do span `chat` (aos.cost.micro_usd) e — o que decide — o campo `cost_micro_usd` do
// evento durável `turn.recorded`, que é a fonte do burn-down do nó. Continua a ser UM
// canal: não se abre uma segunda contabilidade no runtime, projecta-se a que existe.
//
// Micro-USD INTEIRO em toda a travessia: os dois lados da fronteira são int64 e a
// projecção é uma cópia — sem conversão, sem float, sem arredondamento onde se pudesse
// perder um micro-USD.
// maxModeloServido é o tecto em bytes do nome do modelo servido que o turno grava. Um nome
// de modelo real tem dezenas de bytes; o tecto impede um provider avariado ou hostil de
// encher cada `turn.recorded` com o que quiser meter no campo `model`.
const maxModeloServido = 256

// modeloServido saneia o `model` devolvido pelo provider antes de ele ir para o manifesto:
// retira os caracteres não imprimíveis e corta em [maxModeloServido] bytes, sem partir um
// carácter UTF-8 a meio.
func modeloServido(s string) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s))
	if len(s) <= maxModeloServido {
		return s
	}
	corte := maxModeloServido
	for corte > 0 && !utf8.RuneStart(s[corte]) {
		corte--
	}
	return s[:corte]
}

func translateResponse(resp port.ChatResponse) (agentruntime.ModelResponse, error) {
	out := agentruntime.ModelResponse{
		Usage: agentruntime.Usage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
			// AOS-336 — A MARCA ATRAVESSA. Até aqui copiavam-se só os três números, e
			// `agentruntime.Usage` não tinha onde guardar «não medido»: num nó SEM
			// contabilidade de custo composta o gateway serve a resposta na mesma (limite
			// declarado do AOS-321) e o `turn.recorded` recebia zeros para uma chamada não
			// medida. `!Definido()` e não `.Ausente` porque são DUAS formas de ausência: o
			// `usage` em falta, e o `usage` presente sem contadores legíveis — é o critério
			// do próprio [port.Usage.Definido], projectado sem o enfraquecer.
			Ausente: !resp.Usage.Definido(),
			// AOS-490 — os tokens de prompt servidos da cache de prefixo do provider (um
			// subconjunto de InputTokens), até ao `turn.recorded`: é a medição que diz se o
			// prefixo estável está a ser aproveitado.
			CacheReadTokens: resp.Usage.CacheReadTokens,
		},
		CostMicroUSD: resp.Usage.CostMicroUSD,
		// AOS-396 — o modelo que SERVIU: o `model` que o provider devolveu (o gateway só o
		// preenche com o modelo resolvido quando o provider não o manda). Vai para o
		// `served_model_id` do manifesto do turno; o modelo pedido vem do Goal. É texto do
		// provider que fica em claro em cada evento: ver [modeloServido].
		Model: modeloServido(resp.Model),
	}
	// AOS-505 — A ROTA SOB GOVERNAÇÃO. Quando o gateway comparou a rota deste turno
	// (resp.Route.Check preenchido), o modelo que SERVIU passa a ser o que o proxy declarou nos
	// cabeçalhos, e não o `model` do corpo — que o proxy carimba com o nome pedido. Se o proxy
	// não o declarou fica VAZIO: nunca se preenche com o nome pedido nem com o do corpo. O
	// resultado da comparação e o digest do perfil seguem com ele para o registo do turno.
	// Com a governação desligada este bloco não corre, e o turno sai como saía.
	if resp.Route.Check != "" {
		out.Model = modeloServido(resp.Route.Model)
		out.RouteCheck = agentruntime.RouteCheck(resp.Route.Check)
		out.RouteProfileDigest = resp.Route.ProfileDigest
	}
	// AOS-507 — a ficha da forma do corpo, quando o adaptador a mediu. nil com a medição
	// desligada, e o turno sai como saía. É transporte: nada abaixo a lê.
	out.Shape = fichaDoRuntime(resp.Shape)
	if len(resp.Choices) == 0 {
		// FAIL-CLOSED. Ver [ErrRespostaSemChoices]: isto NAO e um turno vazio.
		return agentruntime.ModelResponse{}, fmt.Errorf("%w (modelo %q)", ErrRespostaSemChoices, resp.Model)
	}
	choice := resp.Choices[0]
	out.Text = choice.Message.Content
	// AOS-490 — o raciocínio do turno, carga opaca, byte a byte. Segue só para a captura; não
	// volta a nenhum pedido ([port.ChatRequest.MarshalWire] retira-o) nem entra no tail.
	out.Reasoning = choice.Message.ReasoningContent
	for _, tc := range choice.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, agentruntime.ToolInvocation{
			ToolID: tc.Function.Name,
			Input:  []byte(tc.Function.Arguments),
		})
	}
	// Sem tool calls e finish_reason terminal ⇒ o turno é final.
	out.Final = len(out.ToolCalls) == 0 && (choice.FinishReason == "stop" || choice.FinishReason == "")
	// AOS-491 — o motivo de paragem ATRAVESSA, normalizado. Até aqui o `finish_reason` era lido
	// na linha acima e deitado fora: uma resposta cortada, uma recusa por filtro e uma
	// conclusão chegavam iguais ao runtime. É só transporte — o `Final` acima não mudou.
	out.StopReason = motivoDeParagem(choice.FinishReason)
	return out, nil
}

// motivoDeParagem normaliza o `finish_reason` do provider no vocabulário fechado do runtime
// ([agentruntime.StopReason], AOS-491). É o ÚNICO sítio onde esse texto é traduzido; o que
// sai daqui é sempre uma das constantes, pelo que o valor bruto não chega ao runtime, à
// captura, ao `turn.recorded` nem a um rótulo de métrica.
//
// A comparação é EXACTA, sem aparar nem mudar a caixa: o mapa é o do wire OpenAI, que é o
// contrato da porta, e um provider que escreva o motivo de outra maneira é precisamente o que
// `other` existe para tornar visível. O vazio fica vazio — «o provider não disse» não é o mesmo
// que «disse uma coisa que não conhecemos».
//
// Serve as duas projecções (texto único e mensagens nativas): as duas passam por
// [translateResponse], que só lê a primeira escolha.
func motivoDeParagem(finishReason string) agentruntime.StopReason {
	switch finishReason {
	case "":
		return agentruntime.StopUnreported
	case port.FinishStop:
		return agentruntime.StopStop
	case port.FinishToolCalls, port.FinishFunctionCall:
		return agentruntime.StopToolCalls
	case port.FinishLength:
		return agentruntime.StopLength
	case port.FinishContentFilter:
		return agentruntime.StopContentFilter
	default:
		return agentruntime.StopOther
	}
}
