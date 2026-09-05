package modelgateway_test

// ---------------------------------------------------------------------------------------------
// AOS-321 — UM 200 SEM `usage` NÃO É UMA CHAMADA DE CUSTO NULO.
//
// `port.UnmarshalChatResponse` era um Unmarshal nu: uma resposta 200 de um provedor que OMITISSE
// o objecto `usage` produzia um `port.Usage{}` zerado, sem erro. Esse zero descia por `recordCost`
// — que não distinguia «zero tokens» de «contagem ausente» — e `costForTokens` devolve `0, nil`
// para `tokens == 0`. O custo zero acabava escrito no span, no agregado por run E por árvore, e no
// evento durável `turn.recorded` que o burn-down lê: um provedor que não reportasse tokens saía
// GRÁTIS, e isso é fail-open do burn-down que o ADR-008 exige.
//
// A distinção passa a viver no TIPO ([port.Usage.Ausente]/[port.Usage.Definido]) e a ser capturada
// NO WIRE, que é o único sítio onde a ausência existe de facto. Quatro testes, no molde de
// resposta_vazia_test.go: o caso AUSENTE, o caso de ZEROS explícitos (que continua a contar), o
// CONTROLO que prova que o caminho normal não se partiu, e o LIMITE DECLARADO do que
// deliberadamente NÃO muda.
// ---------------------------------------------------------------------------------------------

import (
	"context"
	"errors"
	"io"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/metering/cost"
	"github.com/aos-ref/platform/model-gateway/port"
)

// Corpos de resposta 200 do provedor, no wire OpenAI. São a MATÉRIA do teste: a distinção que
// este ticket fecha só existe no JSON recebido, pelo que os casos têm de ser expressos aí e não
// em structs Go já construídas (uma `port.Usage{}` escrita em Go afirma zeros; um corpo sem
// `usage` não afirma nada).
const (
	// corpoSemUsage é o defeito: 200, conteúdo válido, e nenhum objecto `usage`.
	corpoSemUsage = `{"id":"cmpl-1","object":"chat.completion","model":"m",
		"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
	// corpoUsageZeros é o caso VIZINHO que tem de continuar a contar: o provedor reportou
	// `usage` e disse zero. É um facto medido, não uma lacuna.
	corpoUsageZeros = `{"id":"cmpl-2","object":"chat.completion","model":"m",
		"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`
	// corpoUsageReal é o caminho normal.
	corpoUsageReal = `{"id":"cmpl-3","object":"chat.completion","model":"m",
		"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500}}`
	// corpoEmbeddingsSemUsage é o mesmo defeito na outra superfície da porta.
	corpoEmbeddingsSemUsage = `{"object":"list","model":"m","data":[{"index":0,"object":"embedding","embedding":[0.5]}]}`

	// Custo esperado de corpoUsageReal com a tabela de costTable(t) (input 3 micro-USD/tok,
	// output 15 micro-USD/tok): 1000*3 + 500*15 = 10_500. Derivado à mão, não pedido ao
	// calculador que está sob teste.
	usageRealMicroUSD = int64(1000*3 + 500*15)
)

// adaptadorDeWire é um [adapters.Adapter] determinista que devolve o resultado de desserializar
// um corpo JSON REAL pelo caminho de `port.UnmarshalChatResponse`. Não faz I/O, não tem relógio
// nem aleatoriedade — mas, ao contrário do FakeAdapter (que devolve structs Go já construídas),
// atravessa o desserializador, que é onde a presença/ausência de `usage` é decidida.
type adaptadorDeWire struct {
	corpoChat  string
	corpoEmbed string
	// deltas, quando não-vazio, é o que o streaming entrega (um stream sem chunk de usage
	// exprime-se com deltas cujo campo Usage é nil).
	deltas []port.ChatStreamDelta
}

func (a *adaptadorDeWire) Provider() string { return "wire" }

func (a *adaptadorDeWire) Chat(_ context.Context, _ port.ChatRequest, _ adapters.Credential) (port.ChatResponse, error) {
	return port.UnmarshalChatResponse([]byte(a.corpoChat))
}

func (a *adaptadorDeWire) ChatStream(_ context.Context, _ port.ChatRequest, _ adapters.Credential) (port.ChatStream, error) {
	return port.NewSliceStream(a.deltas), nil
}

func (a *adaptadorDeWire) Embeddings(_ context.Context, _ port.EmbeddingsRequest, _ adapters.Credential) (port.EmbeddingsResponse, error) {
	return port.UnmarshalEmbeddingsResponse([]byte(a.corpoEmbed))
}

// TestUsageAusenteNaoProduzCustoZeroSilencioso é o teste da AC: uma resposta 200 SEM objecto
// `usage` falha-fecha no caminho síncrono e NADA entra no agregado por run/árvore.
func TestUsageAusenteNaoProduzCustoZeroSilencioso(t *testing.T) {
	t.Parallel()
	rec := cost.NewRecorder(cost.NewCalculator(costTable(t)))
	tr := &agentruntime.RecordingTracer{}
	gw := newGateway(t, &adaptadorDeWire{corpoChat: corpoSemUsage},
		modelgateway.WithCost(rec), modelgateway.WithTracer(tr))

	_, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "m", Board: "board-eu", RunID: "run-ausente", TreeID: "tree-ausente",
		Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if !errors.Is(err, modelgateway.ErrUsageAusente) {
		t.Fatalf("erro = %v, queria ErrUsageAusente — sem isto o provedor que nao reporta tokens sai GRATIS", err)
	}
	// O agregado por RUN e por ÁRVORE — as duas leituras que o burn-down consome — não podem
	// ter recebido a amostra. Um zero agregado seria indistinguível de uma chamada barata.
	if amt, ok := rec.CostForRun(cost.RunKey{RunID: "run-ausente", Tenant: "board-eu"}); ok {
		t.Errorf("o agregado por RUN contabilizou uma amostra indefinida: %+v", amt)
	}
	if amt, ok := rec.CostForTree(cost.TreeKey{TreeID: "tree-ausente", Tenant: "board-eu"}); ok {
		t.Errorf("o agregado por ARVORE contabilizou uma amostra indefinida: %+v", amt)
	}
	// E o rasto diz porquê: o span fica marcado como não-contabilizável, não como custo 0.
	spans := tr.Spans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, queria 1", len(spans))
	}
	if spans[0].Attributes["aos.usage.undefined"] != true {
		t.Errorf("span sem a marca de usage indefinido: %+v", spans[0].Attributes)
	}
	if spans[0].Attributes[agentruntime.AttrErrorType] != "cost_error" {
		t.Errorf("error_type = %v, queria cost_error (a recusa tem de ser atribuivel)", spans[0].Attributes[agentruntime.AttrErrorType])
	}

	// A MESMA regra na outra superfície da porta: Embeddings não pode ser a porta das
	// traseiras por onde o mesmo zero entra.
	gwEmb := newGateway(t, &adaptadorDeWire{corpoEmbed: corpoEmbeddingsSemUsage}, modelgateway.WithCost(rec))
	if _, err := gwEmb.Embeddings(context.Background(), port.EmbeddingsRequest{
		Model: "m", Board: "board-eu", RunID: "run-ausente-emb", Input: []string{"oi"},
	}); !errors.Is(err, modelgateway.ErrUsageAusente) {
		t.Errorf("Embeddings: erro = %v, queria ErrUsageAusente", err)
	}
}

// TestUsageComZerosNaoCredivelNaoEContabilizado corrige um teste que FIXAVA O DEFEITO.
//
// A primeira versão chamava-se `TestUsageComZerosExplicitosContinuaAContabilizar` e afirmava
// que um `usage` reportado a zeros «afirmou um facto medido: conta, agrega, e vale 0
// micro-USD». A revisão adversarial mostrou que era falso, e mediu-o na composição de
// produção: `{"usage":{}}` e `{"usage":{"total_tokens":1500}}` passavam com custo 0 AGREGADO
// por run e por árvore — exactamente as formas que proxies OpenAI-compatible produzem, e
// exactamente o dano que AOS-321 dizia fechar.
//
// O erro do teste era tomar a presença do OBJECTO por medição. Um 200 com zero prompt tokens
// não é uma medição: não existe chamada de chat sem entrada. É a mesma disciplina do
// `cache_sli`, que trata um denominador ausente como indefinido e nunca como 0%.
//
// Fica aqui, com o nome trocado, em vez de ser apagado: um teste que fixou o comportamento
// errado é informação sobre como o defeito sobreviveu à primeira ronda.
func TestUsageComZerosNaoCredivelNaoEContabilizado(t *testing.T) {
	t.Parallel()
	rec := cost.NewRecorder(cost.NewCalculator(costTable(t)))
	gw := newGateway(t, &adaptadorDeWire{corpoChat: corpoUsageZeros}, modelgateway.WithCost(rec))

	_, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "m", Board: "board-eu", RunID: "run-zeros", TreeID: "tree-zeros",
		Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if !errors.Is(err, modelgateway.ErrUsageAusente) {
		t.Fatalf("um `usage` presente mas sem contadores credíveis nao e uma medicao: erro = %v, queria ErrUsageAusente", err)
	}
	if _, ok := rec.CostForRun(cost.RunKey{RunID: "run-zeros", Tenant: "board-eu"}); ok {
		t.Error("a amostra nao-credivel foi AGREGADA — e o zero silencioso que AOS-321 existe para fechar")
	}
	if _, ok := rec.CostForTree(cost.TreeKey{TreeID: "tree-zeros", Tenant: "board-eu"}); ok {
		t.Error("a amostra nao-credivel foi agregada na ARVORE")
	}
}

// TestUsagePresenteContinuaAContabilizar é o CONTROLO: o caminho normal, com tokens reais no
// wire, continua a derivar o custo e a devolvê-lo na resposta normalizada (o canal de AOS-259).
func TestUsagePresenteContinuaAContabilizar(t *testing.T) {
	t.Parallel()
	rec := cost.NewRecorder(cost.NewCalculator(costTable(t)))
	gw := newGateway(t, &adaptadorDeWire{corpoChat: corpoUsageReal}, modelgateway.WithCost(rec))

	resp, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "m", Board: "board-eu", RunID: "run-normal", TreeID: "tree-normal",
		Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if err != nil {
		t.Fatalf("o caminho normal partiu-se: %v", err)
	}
	if resp.Usage.CostMicroUSD != usageRealMicroUSD {
		t.Errorf("CostMicroUSD = %d, quer %d", resp.Usage.CostMicroUSD, usageRealMicroUSD)
	}
	if amt, ok := rec.CostForTree(cost.TreeKey{TreeID: "tree-normal", Tenant: "board-eu"}); !ok || amt.CostMicroUSD != usageRealMicroUSD {
		t.Errorf("agregado por arvore = %+v (ok=%v), quer %d micro-USD", amt, ok, usageRealMicroUSD)
	}
}

// TestSemContabilidadeUsageAusenteContinuaAServir fixa o LIMITE DECLARADO desta correcção.
//
// Um gateway SEM contabilidade de custo composta (sem recorder) serve na mesma um 200 sem
// `usage` — não há nada para contabilizar mal, e transformar isto em recusa faria da
// contabilidade de custo um ponto único de falha do caminho de modelo, invertendo a postura
// fail-OPEN do CANAL que AOS-259 fixou (TestAOS259_SemContabilidade_CustoZeroNaoMataORun).
// Fica nomeado em vez de arrastado: quem o quiser mudar, muda-o aqui e vê o que cai.
func TestSemContabilidadeUsageAusenteContinuaAServir(t *testing.T) {
	t.Parallel()
	tr := &agentruntime.RecordingTracer{}
	gw := newGateway(t, &adaptadorDeWire{corpoChat: corpoSemUsage}, modelgateway.WithTracer(tr))

	resp, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "m", Board: "board-eu", RunID: "run-sem-rec",
		Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if err != nil {
		t.Fatalf("sem contabilidade composta a chamada TEM de servir: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("a resposta perdeu-se: %+v", resp)
	}
	// Mas o rasto continua a dizer a verdade: indefinido, não medido a zero.
	spans := tr.Spans()
	if len(spans) != 1 || spans[0].Attributes["aos.usage.undefined"] != true {
		t.Errorf("um no sem recorder deixou de poder distinguir os dois casos no rasto: %+v", spans)
	}
}

// TestStreamSemChunkDeUsageNaoAgregaNemFalha fecha o SEGUNDO caminho de zero silencioso: o
// streaming, onde `CollectStream` e o meteredStream reconstroem o usage a partir dos deltas.
//
// Aqui a saída fail-closed NÃO está disponível — o stream já foi entregue ao chamador quando o
// metering corre — pelo que a resposta certa é a do molde do cache_sli: indefinido, NÃO agregado,
// span anotado, e sem marcar a chamada como falhada.
func TestStreamSemChunkDeUsageNaoAgregaNemFalha(t *testing.T) {
	t.Parallel()
	rec := cost.NewRecorder(cost.NewCalculator(costTable(t)))
	tr := &agentruntime.RecordingTracer{}
	// Deltas SEM nenhum chunk de usage — é o que um provedor que não suporte
	// stream_options.include_usage entrega.
	ad := &adaptadorDeWire{deltas: []port.ChatStreamDelta{
		{Role: port.RoleAssistant, Content: "ok"},
		{FinishReason: "stop"},
	}}
	gw := newGateway(t, ad, modelgateway.WithCost(rec), modelgateway.WithTracer(tr))

	s, err := gw.ChatStream(context.Background(), port.ChatRequest{
		Model: "m", Board: "board-eu", RunID: "run-stream", TreeID: "tree-stream",
		Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	agregada, err := port.CollectStream(s)
	if err != nil {
		t.Fatalf("CollectStream: %v", err)
	}
	// A reconstrução preserva a distinção em vez de a apagar num Usage{} zerado.
	if agregada.Usage.Definido() {
		t.Error("CollectStream fabricou um usage DEFINIDO a partir de um stream que nunca reportou tokens")
	}
	if amt, ok := rec.CostForRun(cost.RunKey{RunID: "run-stream", Tenant: "board-eu"}); ok {
		t.Errorf("o agregado por run contabilizou um stream nao medido: %+v", amt)
	}
	spans := tr.Spans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, queria 1", len(spans))
	}
	if spans[0].Attributes["aos.usage.undefined"] != true {
		t.Errorf("span do stream sem a marca de usage indefinido: %+v", spans[0].Attributes)
	}
	if et, marcado := spans[0].Attributes[agentruntime.AttrErrorType]; marcado {
		t.Errorf("error_type = %v: um stream que serviu conteudo e ficou por medir NAO e uma chamada falhada", et)
	}
}

// TestCollectStreamComChunkDeUsageContinuaDefinido é o controlo do caminho de streaming: um
// stream que TRAZ o chunk final com usage continua a produzir uma amostra definida e agregável.
func TestCollectStreamComChunkDeUsageContinuaDefinido(t *testing.T) {
	t.Parallel()
	u := port.Usage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500}
	s := port.NewSliceStream([]port.ChatStreamDelta{
		{Role: port.RoleAssistant, Content: "ok"},
		{FinishReason: "stop", Usage: &u},
	})
	resp, err := port.CollectStream(s)
	if err != nil && err != io.EOF {
		t.Fatalf("CollectStream: %v", err)
	}
	if !resp.Usage.Definido() {
		t.Fatal("um stream COM chunk de usage passou a ser lido como indefinido — o caminho normal partiu-se")
	}
	if resp.Usage.PromptTokens != 1000 || resp.Usage.CompletionTokens != 500 {
		t.Errorf("tokens perdidos na reconstrucao: %+v", resp.Usage)
	}
}

// corpoUsageParcial é a terceira forma que a revisão adversarial mediu: o objecto `usage`
// existe e traz um contador, mas NÃO os que o cálculo de custo lê. O `Calculator` deriva de
// prompt/completion/cache; `total_tokens` é informativo. Um provedor que reporte só o total
// produzia, antes desta correcção, custo 0 agregado COM 1500 tokens declarados no wire.
const corpoUsageParcial = `{"id":"cmpl-4","object":"chat.completion","model":"m",
		"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
		"usage":{"total_tokens":1500}}`

// TestUsageParcialNaoEContabilizado cobre a forma que escapou à primeira ronda de AOS-321.
//
// A distinção «objecto presente vs ausente» não bastava: um `usage` que traga apenas
// `total_tokens` é indistinguível, para o cálculo, de um `usage` vazio — e ambos passavam.
// É a forma que proxies OpenAI-compatible produzem com mais frequência, porque `total_tokens`
// é o único campo que quase todos preenchem.
func TestUsageParcialNaoEContabilizado(t *testing.T) {
	t.Parallel()
	rec := cost.NewRecorder(cost.NewCalculator(costTable(t)))
	gw := newGateway(t, &adaptadorDeWire{corpoChat: corpoUsageParcial}, modelgateway.WithCost(rec))

	_, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "m", Board: "board-eu", RunID: "run-parcial", TreeID: "tree-parcial",
		Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if !errors.Is(err, modelgateway.ErrUsageAusente) {
		t.Fatalf("`usage` com so total_tokens nao e uma medicao para o calculo: erro = %v, queria ErrUsageAusente", err)
	}
	if _, ok := rec.CostForRun(cost.RunKey{RunID: "run-parcial", Tenant: "board-eu"}); ok {
		t.Error("a amostra parcial foi AGREGADA com custo 0 — 1500 tokens declarados a sair gratis")
	}
}

// TestSpanOmiteContadoresQuandoIndefinido fixa a segunda correcção da revisão adversarial de
// AOS-321, que estava no código e na prosa mas NÃO tinha teste: uma mutação que repusesse a
// emissão de `gen_ai.usage.*` a zero, a par do marcador, deixava o módulo inteiro verde.
//
// Porque importa: um consumidor de OTel GenAI que some `gen_ai.usage.input_tokens` sem
// conhecer o atributo proprietário `aos.usage.undefined` lê uma medição de zero tokens onde
// não houve medição — o mesmo zero silencioso, mudado do plano de contabilidade para o de
// telemetria. O semconv trata um atributo ausente como desconhecido, que é o facto.
func TestSpanOmiteContadoresQuandoIndefinido(t *testing.T) {
	t.Parallel()
	tr := &agentruntime.RecordingTracer{}
	gw := newGateway(t, &adaptadorDeWire{corpoChat: corpoSemUsage}, modelgateway.WithTracer(tr))

	_, _ = gw.Chat(context.Background(), port.ChatRequest{
		Model: "m", Board: "board-eu", RunID: "run-span", TreeID: "tree-span",
		Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})

	spans := tr.Spans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, queria 1", len(spans))
	}
	if spans[0].Attributes["aos.usage.undefined"] != true {
		t.Error("o span devia marcar aos.usage.undefined")
	}
	for _, k := range []string{agentruntime.AttrInputTokens, agentruntime.AttrOutputTokens} {
		if _, presente := spans[0].Attributes[k]; presente {
			t.Errorf("o span NAO devia emitir %q quando o usage e indefinido — um zero ali le-se como medicao", k)
		}
	}
}
