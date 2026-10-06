package main

// medicao_do_contrato.go — O QUE O `serve` MEDE SOBRE O CONTRATO DE CONCLUSÃO (AOS-495).
//
// A medição de `observe` do lado do plano era um `grep` no log da drenagem. Este ficheiro leva-a
// ao ficheiro de métricas que o `consume` já escreve (metricas_do_consumo.go), em três contadores:
//
//   - execuções de plano em que o contrato NÃO foi aplicado, por motivo;
//   - veredictos NEGATIVOS observados, por razão — os nós que `enforce` fechava `failed`;
//   - nós submetidos, por classe face ao contrato — onde se vê quantos ficaram sem ele, e porquê.
//
// CARDINALIDADE FECHADA. Os três rótulos têm vocabulário fechado, e é a ESCRITA que o impõe
// ([metricasDoConsumo.registarContrato]): um valor fora da lista não se escreve. A razão de um
// veredicto vem de outro processo; chega aqui já reduzida por [causaDoRunFilho].
//
// O `serve` corre dentro do processo do `consume`, e é ele que entrega a medição a quem a
// escreve. Num `serve` manual não há ficheiro de métricas: a medição é nil e os métodos não
// fazem nada.

import agentruntime "github.com/aos-ref/kernel/agent-runtime"

// medicaoDoContrato acumula o que UM `serve` mediu. Não é concorrente: o executor de nós
// submete e recolhe na mesma goroutine.
type medicaoDoContrato struct {
	naoAplicado map[string]int // motivo → execuções de plano
	observados  map[string]int // razão → nós do plano
	classes     map[string]int // classe → nós submetidos
	// AOS-499 — a saída por referência, medida (saida_por_referencia.go). Só com o interruptor em
	// `observe`; vazios em `off`.
	estruturas  map[string]int // classe estrutural → nós submetidos
	designacoes map[string]int // estado da designação → nós candidatos
	tamanhos    map[string]int // classe de tamanho do resultado designado, tal como se transporta → nós
	transportes map[string]int // o que o nó fez dos bytes designados → nós
	formas      map[string]int // forma do resultado designado (envelope da sandbox, cru, sem bytes) → nós
	comparacoes map[string]int // relação entre o texto final e o CONTEÚDO do resultado designado → nós
	numeros     map[string]int // os números do conteúdo aparecem todos no texto final → nós
	razoes      map[string]int // classe da razão tamanho do texto final / tamanho do conteúdo → nós
	// AOS-501 — a entrega por referência (entrega_por_referencia.go). Só com o interruptor em
	// `on`; vazios nos outros modos.
	entregas            map[string]int // entregue, ou a causa → nós com saída de origem declarada
	extraccoes          map[string]int // forma da extracção → saídas entregues
	recusasDaOrigem     map[string]int // razão do validador → tentativas do planeador recusadas
	candidatosSemOrigem int            // candidatos por estrutura cujo plano não declarou a origem
}

// contratoNaoAplicado conta uma execução de plano em que o contrato não foi aplicado.
func (m *medicaoDoContrato) contratoNaoAplicado(motivo string) {
	if m == nil {
		return
	}
	if m.naoAplicado == nil {
		m.naoAplicado = map[string]int{}
	}
	m.naoAplicado[motivo]++
}

// veredictoObservado conta um nó do plano que concluiu com um veredicto negativo observado.
func (m *medicaoDoContrato) veredictoObservado(razao string) {
	if m == nil {
		return
	}
	if m.observados == nil {
		m.observados = map[string]int{}
	}
	m.observados[razao]++
}

// noSubmetido conta um nó do plano submetido, pela sua classe face ao contrato.
func (m *medicaoDoContrato) noSubmetido(classe string) {
	if m == nil {
		return
	}
	if m.classes == nil {
		m.classes = map[string]int{}
	}
	m.classes[classe]++
}

// razoesObservaveis é a lista fechada do rótulo `razao` de [metricaVeredictosObservados]: as
// razões negativas do kernel e os dois nomes em que [causaDoRunFilho] reduz o resto.
func razoesObservaveis() []string {
	razoes := []string{causaRazaoDesconhecida, causaRunNaoConcluido}
	for _, r := range agentruntime.OutcomeReasons() {
		if r != agentruntime.OutcomeFulfilled {
			razoes = append(razoes, string(r))
		}
	}
	return razoes
}

// registarContrato soma às séries o que um `serve` mediu. Só escreve rótulos do vocabulário
// fechado; o resto conta-se em nenhum lado, de propósito — uma série por valor desconhecido era
// cardinalidade decidida por quem está do outro lado do fio.
func (m *metricasDoConsumo) registarContrato(c *medicaoDoContrato) {
	if c == nil {
		return
	}
	somar := func(metrica, rotulo string, fechado []string, valores map[string]int) {
		for _, v := range fechado {
			if n := valores[v]; n > 0 {
				m.somar(serie(metrica, rotulo, v), float64(n))
			}
		}
	}
	somar(metricaContratoNaoAplicado, "motivo", motivosDoContratoNaoAplicado, c.naoAplicado)
	somar(metricaVeredictosObservados, "razao", razoesObservaveis(), c.observados)
	somar(metricaNosPorContrato, "classe", classesDoContrato, c.classes)
	m.registarOrigem(c)
	m.registarEntrega(c)
}
