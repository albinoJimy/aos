// Package working implementa a MEMÓRIA DE TRABALHO do AOS (AOS-037): a gestão da
// janela de contexto — a janela que o modelo efectivamente vê num turno. É aqui
// que o contrato de cache do ADR-009 vive ou morre: PREFIXO IMUTÁVEL (system +
// tool set congelado no run) + TAIL APPEND-ONLY (memory_context, timestamps,
// resultados). A janela cresce SÓ pelo tail; NUNCA muta nem reordena o prefixo
// entre turnos do MESMO run.
//
// Responsabilidades (escopo estrito de AOS-037):
//   - montagem prefixo-imutável + tail append-only (reutiliza o layout
//     cache-estável do Agent Runtime, agentruntime.PromptAssembler — ADR-009);
//   - CONTABILIDADE EM TOKENS (não em nº de mensagens) da ocupação da janela,
//     exposta como métrica e comparada com o limite do modelo;
//   - SINAL DE EXAUSTÃO GRACIOSA a ~80% (configurável) — marca para compressão em
//     checkpoint (AOS-043) ou escala ao runtime, NUNCA um hard-stop cego;
//   - PINNING de prefixo: novas tools/entradas que quebrariam o prefixo só entram
//     em RUNS NOVOS (fail-closed para a estabilidade do prefixo);
//   - EVICTION do tail que PRESERVA o registo — o que sai da VISTA permanece no
//     backend (Princípio 4, AOS-036), via a MemoryPort;
//   - SLI de cache-hit-rate, derivado da estabilidade do prefixo, exposto e não
//     regressivo num cenário de referência.
//
// FORA DE ÂMBITO (não implementado aqui): a COMPRESSÃO assíncrona em si (AOS-043 —
// aqui só se PREPARA/marca para checkpoint) e as classes concretas
// episódica/semântica/procedural (AOS-038/039/040).
//
// Determinismo: o estimador de tokens é uma função PURA injectável; o relógio (só
// para o CreatedAt obrigatório do registo evictado) é injectável; sem time.Now nem
// rand no caminho de decisão; o hash do prefixo é estável entre turnos.
package working

// TokenEstimator estima a ocupação em tokens de um texto. É DETERMINÍSTICO e PURO
// (sem estado, sem aleatoriedade): a mesma string produz sempre a mesma contagem.
// É injectável para que a contagem exacta por modelo (EPIC-06 / Model Gateway)
// possa substituir a heurística default sem alterar a gestão da janela.
type TokenEstimator func(string) int

// DefaultTokenEstimator é a heurística default: aproxima por palavras (campos
// separados por espaço em branco) com um piso por caracteres (~4 chars/token) para
// conteúdo denso/sem espaços. É monótona no comprimento e reproduzível — espelha a
// estimativa usada na projecção (AOS-036) para que a contabilidade da janela e a do
// resumo injectado sejam coerentes.
func DefaultTokenEstimator(s string) int {
	if s == "" {
		return 0
	}
	words := 0
	inField := false
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			inField = false
		default:
			if !inField {
				words++
				inField = true
			}
		}
	}
	chars := (len([]rune(s)) + 3) / 4
	if words > chars {
		return words
	}
	return chars
}

// Occupancy é a fotografia da ocupação da janela EM TOKENS (nunca em nº de
// mensagens). É a métrica exposta ao runtime e à telemetria: separa a parte
// CACHEÁVEL (prefixo imutável) da parte NOVA (tail append-only) e posiciona o
// total face ao limite do modelo.
type Occupancy struct {
	// PrefixTokens é a ocupação do prefixo imutável (system + tool set congelado).
	// É constante ao longo do run — a base do prefix caching (ADR-009).
	PrefixTokens int
	// TailTokens é a ocupação do tail append-only (só cresce dentro do run).
	TailTokens int
	// Limit é o limite de tokens do modelo para a janela.
	Limit int
	// Threshold é o limiar de exaustão graciosa (floor(Limit * ExhaustionRatio)).
	Threshold int
}

// Total é a ocupação total da janela em tokens (prefixo + tail).
func (o Occupancy) Total() int { return o.PrefixTokens + o.TailTokens }

// Ratio é a fracção de ocupação face ao limite do modelo em [0, +inf). Um limite
// não-positivo devolve 0 (sem limite configurado não há burn-down).
func (o Occupancy) Ratio() float64 {
	if o.Limit <= 0 {
		return 0
	}
	return float64(o.Total()) / float64(o.Limit)
}

// Exhausted indica se a ocupação atingiu ou ultrapassou o limiar de exaustão
// graciosa (~80%). É o predicado que dispara o SINAL — não um hard-stop: a janela
// continua a aceitar tail (a decisão de comprimir/escalar é do runtime, AOS-043).
func (o Occupancy) Exhausted() bool {
	return o.Threshold > 0 && o.Total() >= o.Threshold
}

// cacheSLI acumula o SLI de cache-hit-rate da janela, medido EM TOKENS. O
// cache-hit deriva da ESTABILIDADE DO PREFIXO (ADR-009): no primeiro turno o
// prefixo é uma escrita de cache (miss); em cada turno seguinte do MESMO run, por
// o prefixo ser byte-idêntico, os seus tokens são servidos da cache (hit). O tail é
// sempre novo (miss) porque cresce a cada turno.
//
//	cache_hit_rate = cachedTokens / totalPromptTokens
//
// onde cachedTokens soma os tokens de prefixo dos turnos 2..N (o prefixo já em
// cache) e totalPromptTokens soma (prefixo + tail) de todos os turnos.
type cacheSLI struct {
	turns             int
	cachedTokens      int
	totalPromptTokens int
}

// observeTurn regista um turno materializado no acumulador do SLI. O primeiro
// turno estabelece a cache do prefixo (miss); os seguintes contam o prefixo como
// hit. É PURO no sentido de não tocar relógio/aleatoriedade.
func (s *cacheSLI) observeTurn(prefixTokens, tailTokens int) {
	if s.turns > 0 {
		// Turno 2..N: o prefixo já está em cache — hit pelos seus tokens.
		s.cachedTokens += prefixTokens
	}
	s.totalPromptTokens += prefixTokens + tailTokens
	s.turns++
}

// rate devolve o cache-hit-rate acumulado em [0,1]. Sem turnos (ou sem tokens) o
// SLI é 0 — não há cache a medir.
func (s *cacheSLI) rate() float64 {
	if s.totalPromptTokens == 0 {
		return 0
	}
	return float64(s.cachedTokens) / float64(s.totalPromptTokens)
}
