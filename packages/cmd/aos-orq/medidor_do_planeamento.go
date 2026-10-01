package main

// medidor_do_planeamento.go — o CONSUMO REAL do modelo no planeamento de um pedido (AOS-466).
//
// O nó reserva uma quantia de planeamento contra a quota do principal que submeteu o pedido
// (`POST /plans`) e liquida-a pelo que este processo mede e declara no `POST /plans/outcome`. Antes
// deste ticket o `aos-orq` não media nada: o [gatewayDecomposeModel] deitava fora o `usage` da
// resposta, e o tecto `AOS_ORQ_PLAN_BUDGET_*` (AOS-434) soma estimativas declaradas.
//
// O QUE SE MEDE: `prompt_tokens + completion_tokens` de cada chamada ao modelo (ou o `total_tokens`,
// se for maior). Uma chamada que falha, ou uma resposta sem `usage` (`prompt_tokens <= 0` — nenhum
// pedido real ao modelo tem um prompt vazio), torna os tokens NÃO MEDIDOS: o nó cobra por essa
// geração o que se mediu mais a reserva. Os dólares NUNCA se medem aqui — a tabela de preços vive
// no nó —, excepto no caso exacto de nenhuma chamada: zero chamadas custam zero.

import (
	"context"
	"sync"

	"github.com/aos-ref/platform/model-gateway/port"
)

// consumoDoPlaneamento é o que se declara ao nó no desfecho de uma geração. Os `...Medido` a falso
// dizem «não se sabe», que não é o mesmo que zero.
type consumoDoPlaneamento struct {
	Tokens        int64 `json:"tokens"`
	TokensMedidos bool  `json:"tokens_medidos"`
	CostMicroUSD  int64 `json:"cost_micro_usd"`
	CustoMedido   bool  `json:"custo_medido"`
}

// medidorDoPlaneamento acumula o consumo das chamadas ao modelo de UMA geração de um pedido.
// Concorrente por segurança: o planeador chama o modelo em série hoje, mas nada no contrato de
// [decompose.Model] o promete.
type medidorDoPlaneamento struct {
	mu         sync.Mutex
	tokens     int64
	chamadas   int
	naoMedidas int
}

// registar conta uma chamada ao modelo.
func (m *medidorDoPlaneamento) registar(u port.Usage, err error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chamadas++
	if err != nil || u.PromptTokens <= 0 || u.CompletionTokens < 0 {
		m.naoMedidas++
		return
	}
	// O `total_tokens` do provider, quando é maior do que a soma, é o que se cobra: uma resposta
	// com `prompt_tokens` e sem `completion_tokens` não passa a custar só o prompt.
	m.tokens += max(u.TotalTokens, u.PromptTokens+u.CompletionTokens)
}

// consumo é o que se declara ao nó. Um medidor nil não viu as chamadas, e por isso não sabe:
// NÃO MEDIDO. O `consume` cria sempre um por pedido, mesmo para os desfechos sem `serve` — zero
// chamadas, esse sim, é zero medido.
func (m *medidorDoPlaneamento) consumo() consumoDoPlaneamento {
	if m == nil {
		return consumoDoPlaneamento{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return consumoDoPlaneamento{
		Tokens:        m.tokens,
		TokensMedidos: m.naoMedidas == 0,
		CustoMedido:   m.chamadas == 0,
	}
}

// chamouModelo diz se ESTA geração chamou o modelo (AOS-467): é o que o tecto de gerações do nó
// conta. Um medidor nil não sabe — e diz que chamou, o lado que fecha mais cedo e nunca deixa uma
// geração cara passar sem contar.
func (m *medidorDoPlaneamento) chamouModelo() bool {
	if m == nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.chamadas > 0
}

// chaveDoMedidor transporta o medidor do `consume` até à composição do modelo, atravessando o
// `serve` in-process. Só se LÊ na composição ([construirModeloGateway]), que o guarda num campo: a
// contagem nunca depende de o planeador propagar o ctx até ao modelo.
type chaveDoMedidor struct{}

func comMedidor(ctx context.Context, m *medidorDoPlaneamento) context.Context {
	if m == nil {
		return ctx
	}
	return context.WithValue(ctx, chaveDoMedidor{}, m)
}

func medidorDe(ctx context.Context) *medidorDoPlaneamento {
	m, _ := ctx.Value(chaveDoMedidor{}).(*medidorDoPlaneamento)
	return m
}
