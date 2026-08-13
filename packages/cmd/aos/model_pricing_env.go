package main

// AOS-259 — A FONTE DO NÚMERO DO CUSTO: A TABELA DE PREÇOS DO MODEL GATEWAY.
//
// O canal de custo ponta a ponta (port.Usage.CostMicroUSD → ModelResponse.CostMicroUSD →
// span `chat` → evento `turn.recorded` → burn-down) transporta o que a contabilidade do
// gateway derivar. Este ficheiro é o que decide se há alguma coisa a derivar: resolve a
// TABELA DE PREÇOS em vigor e compõe (ou não) o [cost.Recorder] que o gateway recebe.
//
// # De onde vem o número (e de onde NÃO vem)
//
// O número vem da tabela versionada e tamper-evident do pacote `pricing` — quatro rates
// distintos por (modelo, região), em micro-USD inteiro por 1M tokens, com digest sha256 —
// multiplicada pelos quatro contadores de token que o provider ecoou. NÃO vem de uma
// constante no código, nem de uma estimativa, nem do provider (a API compatível OpenAI
// não devolve custo; devolve tokens). Não há aqui nenhum preço novo: esta camada escolhe
// a tabela e verifica que ela cobre o par que o nó vai usar.
//
// # Porque é que a tabela EMBEBIDA raramente serve um nó real
//
// A tabela embebida (pricing_table.json) tem os pares de REFERÊNCIA do repositório
// (claude-sonnet/claude-haiku/gpt-4o em eu-west/us-east). Um nó configurado com
// AOS_MODEL_ID/AOS_MODEL_REGION fora desse conjunto NÃO tem preço — e um preço inventado
// para o modelo do operador seria pior do que não haver custo nenhum, porque o burn-down
// em dólares passaria a mentir com autoridade. Por isso: AOS_MODEL_PRICING_PATH deixa o
// operador montar a SUA tabela (mesmo formato, mesmo digest tamper-evident, carregada por
// [pricing.LoadTable]), e sem par coberto o canal transporta ZERO — um zero DECLARADO no
// banner de postura, nunca um número fabricado.
//
// # Porque é opcional (e não fail-closed) compor o recorder
//
// A contabilidade de custo é fail-closed POR CHAMADA: com um recorder ligado, uma chamada
// a um (modelo, região) sem preço é RECUSADA (pricing.ErrNoPrice), para que nada seja
// facturado a zero em silêncio. Essa postura é correcta dentro do caminho metrado, mas
// ligá-la a um nó cujo modelo não está na tabela transformaria o fail-closed num nó que
// erra TODAS as chamadas ao modelo — um brownout total por falta de um dado de
// facturação. Por isso a cobertura é verificada UMA VEZ, no arranque, com a tabela em
// mão: par coberto ⇒ recorder ligado (e o fail-closed por chamada vale de facto, porque
// já sabemos que o preço existe); par não coberto ⇒ sem recorder, custo zero declarado.
//
// # Um canal, não dois
//
// O recorder é composto SEM sinks (nem métrica nem burn-down). O burn-down do nó é o
// LEDGER DE TURNOS de AOS-261 (eventos `turn.recorded` no Event Store, deduplicados por
// `run_id:step_id` e duráveis); ligar aqui um [cost.BurndownSink] abriria um SEGUNDO
// canal de contabilidade, em memória, com outra chave e outra retenção — exactamente o
// que AOS-261 rejeitou. A agregação por run/árvore do recorder continua a correr e é
// introspectável, mas quem decide o burn-down é o ledger, alimentado por este mesmo
// número através do canal de custo.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aos-ref/platform/model-gateway/metering/cost"
	"github.com/aos-ref/platform/model-gateway/pricing"
)

// ErrBadModelPricing — AOS_MODEL_PRICING_PATH está definida mas a tabela não carrega
// (ficheiro inacessível, JSON inválido, versão vazia, rate negativo, par duplicado).
// Fail-closed de CONFIG, no molde de [ErrBadModelAudit]: distingue "não configurado"
// (vazio ⇒ tabela embebida) de "configurado mas inválido" (aborta o arranque em vez de
// facturar em silêncio com preços que o operador julga ter substituído).
var ErrBadModelPricing = errors.New("aos: tabela de precos do model gateway mal configurada — AOS_MODEL_PRICING_PATH aponta um documento que nao carrega (ilegivel, JSON invalido, versao vazia, rate negativo ou par (modelo, regiao) duplicado); fail-closed em vez de arrancar sobre precos que nao sao os que o operador montou")

// modelPricingPosture é o ESTADO COMPOSTO da contabilidade de custo do nó — o que o
// banner declara. Amarrado ao que ficou REALMENTE ligado, não à intenção da config
// (disciplina de banner de AOS-203).
type modelPricingPosture struct {
	// Armed indica que o [cost.Recorder] foi composto: o par (modelo, região) do nó tem
	// preço na tabela em vigor e o canal de custo transporta um número derivado.
	Armed bool
	// TableVersion é a identidade tamper-evident da tabela em vigor ("versão#digest12").
	TableVersion string
	// External indica que a tabela veio de AOS_MODEL_PRICING_PATH (curada pelo operador)
	// e não da embebida de referência.
	External bool
	// Model/Region são o par consultado (o que o nó vai usar de facto).
	Model, Region string
}

// parseModelPricingFromEnv resolve a tabela de preços em vigor e compõe o
// [cost.Recorder] do gateway quando — e só quando — ela cobre o par (model, region) que
// o nó vai usar. Devolve (nil, postura, nil) quando não há cobertura: não é um erro de
// configuração, é a ausência de um dado de facturação, e o canal de custo transporta zero
// declarado. Fail-closed ([ErrBadModelPricing]) só quando o operador APONTOU uma tabela
// que não carrega.
//
// Env:
//   - AOS_MODEL_PRICING_PATH — caminho de um documento de preços no formato de
//     `pricing_table.json` (versão + entradas (modelo, região, 4 rates em micro-USD por
//     1M tokens)). Vazio ⇒ tabela EMBEBIDA de referência.
func parseModelPricingFromEnv(model, region string) (*cost.Recorder, modelPricingPosture, error) {
	table, posture, err := resolveModelPricing(model, region)
	if err != nil || !posture.Armed {
		return nil, posture, err
	}
	// Sem sinks de propósito — ver o cabeçalho ("um canal, não dois").
	return cost.NewRecorder(cost.NewCalculator(table)), posture, nil
}

// resolveModelPricing carrega a tabela em vigor e apura a POSTURA (origem, versão,
// cobertura do par). É o corpo partilhado entre a composição ([parseModelPricingFromEnv])
// e a declaração ([modelPricingPostureFromEnv]) — uma leitura só, para que o banner não
// possa divergir do que ficou realmente ligado.
//
// DEFERIDO (DEF-279) — ALCANCE DA VERIFICAÇÃO DE COBERTURA. A cobertura é apurada aqui para
// o par de CONFIGURAÇÃO (`AOS_MODEL_NAME`/`AOS_MODEL_REGION`), mas o cálculo de custo do
// gateway corre sobre o par RESOLVIDO pelo roteamento. Hoje os dois coincidem sempre — o nó
// compõe UMA conta de infra (uma região, um modelo), pelo que o roteamento não tem por onde
// resolver outro par. A verificação é, por isso, exaustiva sobre o inventário REAL, e é essa
// a garantia que o banner declara: a do inventário, não a de um universo de pares que este nó
// não consegue produzir. Verificar todos os pares seria hoje código sobre um inventário de uma
// entrada — mais uma coisa a manter, sem efeito. REABRIR quando o nó passar a compor mais do
// que uma conta/região: aí o par resolvido pode divergir do configurado e a verificação de
// arranque deixa de cobrir o que o cálculo faz.
func resolveModelPricing(model, region string) (*pricing.Table, modelPricingPosture, error) {
	path := strings.TrimSpace(os.Getenv("AOS_MODEL_PRICING_PATH"))
	var (
		table *pricing.Table
		err   error
	)
	if path == "" {
		table, err = pricing.LoadEmbedded()
		if err != nil {
			// A embebida é verificada em teste e vive no binário; falhar aqui é um defeito de
			// build, não uma má configuração do operador — mas continua a ser fail-closed.
			return nil, modelPricingPosture{}, fmt.Errorf("%w: tabela EMBEBIDA: %v", ErrBadModelPricing, err)
		}
	} else {
		doc, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, modelPricingPosture{}, fmt.Errorf("%w: ler %q: %v", ErrBadModelPricing, path, readErr)
		}
		table, err = pricing.LoadTable(doc)
		if err != nil {
			return nil, modelPricingPosture{}, fmt.Errorf("%w: %q: %v", ErrBadModelPricing, path, err)
		}
	}

	posture := modelPricingPosture{
		TableVersion: table.Version(),
		External:     path != "",
		Model:        model,
		Region:       region,
	}
	// COBERTURA: a mesma consulta que o cálculo por chamada fará. Verificá-la aqui é o que
	// impede que o fail-closed por chamada se transforme num nó que erra todas as chamadas.
	//
	// ALCANCE EXACTO DESTA VERIFICAÇÃO (e o eixo que ela deixa aberto): o par verificado é o
	// PEDIDO (AOS_MODEL_NAME/AOS_MODEL_REGION), enquanto `Gateway.recordCost` calcula sobre o par
	// RESOLVIDO pelo roteamento — `failover.Stage.Process` escreve `ex.ResolvedRegion` com a
	// região do endpoint ESCOLHIDO, que pode não ser a pedida. Hoje as duas coincidem sempre,
	// porque [newGatewayModelClient] compõe um ÚNICO [modelgateway.InfraAccount] na região pedida
	// e não há outra por onde falhar — a garantia é real, mas é uma garantia do INVENTÁRIO e não
	// desta função. Acrescentar uma segunda conta/região ao keypool (a razão de o router de
	// failover existir) torna alcançável um par sem preço: `pricing.ErrNoPrice` ⇒ `costError` ⇒
	// chamada RECUSADA, e o mecanismo de resiliência vira a causa da interrupção. Quem lá chegar
	// tem de verificar a cobertura de TODOS os pares do inventário, não só o pedido — o
	// inventário é montado no mesmo sítio de onde esta função é chamada. O banner declara este
	// limite em vez de o calar.
	_, posture.Armed = table.RateFor(model, region)
	return table, posture, nil
}

// modelPricingPostureFromEnv re-apura a postura a partir do ambiente, para o banner. É
// puro (não compõe nada) e lê o MESMO par (modelo, região) que a composição usa —
// AOS_MODEL_NAME e AOS_MODEL_REGION (com o default de soberania do nó). Um erro aqui é
// impossível de alcançar em prática: a composição já falhou-fechada antes do banner; se
// ainda assim ocorrer, devolve a postura vazia (não-armada), que o banner declara.
func modelPricingPostureFromEnv() modelPricingPosture {
	region := defaultModelGatewayRegion
	if v := strings.TrimSpace(os.Getenv("AOS_MODEL_REGION")); v != "" {
		region = v
	}
	_, p, err := resolveModelPricing(strings.TrimSpace(os.Getenv("AOS_MODEL_NAME")), region)
	if err != nil {
		return modelPricingPosture{}
	}
	return p
}

// modelPricingPostureBanner declara o MODO da contabilidade de custo do nó (AOS-259).
// Só sai quando há gateway composto: sem gateway não há custo de modelo a declarar.
func modelPricingPostureBanner(gatewayComposed bool, p modelPricingPosture) []string {
	if !gatewayComposed {
		return nil
	}
	origem := "EMBEBIDA (pares de referencia do repositorio)"
	if p.External {
		origem = "EXTERNA (AOS_MODEL_PRICING_PATH, curada pelo operador)"
	}
	if !p.Armed {
		return []string{
			fmt.Sprintf("custo do modelo / canal de custo (AOS-259): CANAL LIGADO, FONTE AUSENTE — o par (modelo=%q, regiao=%q) NAO tem preco na tabela %s em vigor (%s), pelo que o custo derivado e ZERO em toda a travessia: port.Usage.CostMicroUSD, o span chat (aos.cost.micro_usd), o campo cost_micro_usd do evento turn.recorded e o acumulado do run. ESSE ZERO E AUSENCIA DE DADOS, NAO CUSTO NULO — nao foi inventado nenhum preco para o modelo deste no. O burn-down NAO fica cego por isso: a dimensao que DECIDE e TOKENS (AOS_BUDGET_MAX_TOKENS), que continua a ser lida do ledger de turnos e continua fail-closed (ErrBurndownNoUsage se somar zero). Para ter custo em dolares monte a sua tabela de precos em AOS_MODEL_PRICING_PATH (formato de pricing_table.json: versao + entradas (modelo, regiao, 4 rates em micro-USD por 1M tokens)). Eixo: AOS-259",
				p.Model, p.Region, origem, p.TableVersion),
		}
	}
	return []string{
		fmt.Sprintf("custo do modelo / canal de custo (AOS-259): ARMADO — o custo de cada turno e DERIVADO dos 4 contadores de token ecoados pelo provider pela tabela de precos %s versao %s (tamper-evident por digest), em micro-USD INTEIRO (zero float no caminho de dinheiro), para o par (modelo=%q, regiao=%q). Flui num UNICO canal: port.Usage.CostMicroUSD -> ModelResponse.CostMicroUSD -> span chat (aos.cost.micro_usd) + campo cost_micro_usd do evento DURAVEL turn.recorded, que e a fonte do burn-down do no. FAIL-CLOSED POR CHAMADA: um custo nao-calculavel (tokens negativos, overflow) RECUSA a chamada — nunca e facturado a zero em silencio. A cobertura de preco foi verificada NO ARRANQUE para o par CONFIGURADO, e e isso que impede o fail-closed de virar brownout ENQUANTO o inventario do keypool tiver uma unica conta/regiao — que e o caso deste wiring (newGatewayModelClient compoe um so InfraAccount na regiao pedida). O calculo por chamada usa o par RESOLVIDO pelo roteamento (ex.ResolvedModel/ex.ResolvedRegion), pelo que acrescentar uma SEGUNDA conta ou regiao ao inventario torna alcancavel um par sem preco verificado: nesse dia a verificacao de arranque tem de passar a cobrir TODOS os pares alcancaveis, senao um failover — o mecanismo de resiliencia — passa a ser a causa da interrupcao. Eixo: AOS-259. NAO ha segundo canal: o recorder do gateway nao tem sink de burn-down, quem decide e o ledger de turnos (AOS-261)",
			origem, p.TableVersion, p.Model, p.Region),
	}
}
