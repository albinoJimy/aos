package main

import (
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// AOS-456 — ADMISSION POR-CHAMADOR NO INGRESSO DO PLANO DE DADOS.
//
// O PROBLEMA, medido e não suposto. O `AOS_INGRESS_*` do AOS-277 deu ao ingresso UM token-bucket
// e um tecto de in-flight, ambos POR-NÓ. Com um utilizador — o dono — é anti-exaustão correcta, e
// era o que aquele ticket pedia. Com N utilizadores é negação de serviço ENTRE PARES, sem malícia
// necessária: quem submeter em rajada esgota o balde de todos, e o segundo utilizador vê 429 por
// causa do primeiro. O banner de arranque já o declarava em voz alta — «o balde é GLOBAL entre
// chamadores: NÃO é por-principal (um só cliente ruidoso pode esgotá-lo para todos)» —, que é a
// postura honesta de um defeito conhecido, não a sua ausência.
//
// ---------------------------------------------------------------------------
// A DECISÃO ESTRUTURANTE, E PORQUE O DESENHO ÓBVIO NÃO FUNCIONA.
//
// O desenho óbvio é «mantém o balde global e acrescenta um por-chamador a seguir». Não resolve
// nada, e vale escrever porquê, porque o ticket AOS-456 foi escrito com essa formulação e ela está
// incompleta: se um pedido AUTENTICADO continuar a consumir o balde global, o atacante A drena o
// global antes de esgotar o seu próprio, e B é recusado por falta de tokens GLOBAIS. A starvation
// sobrevive ao balde novo.
//
// O desenho que funciona reparte os pedidos por ATRIBUIBILIDADE:
//
//   - pedido que NÃO se consegue atribuir (modo legado, ou gate soberano não composto) ⇒ balde
//     GLOBAL. É a única barreira possível: não há a quem imputar o consumo, e uma rajada anónima
//     tem de ter tecto;
//   - pedido ATRIBUÍVEL a um principal verificado ⇒ balde DESSE principal, e só dele.
//
// Assim a rajada de A esgota A. B fica intacto, que é literalmente o critério de aceitação do
// ticket.
//
// O QUE ISTO CUSTA, declarado: a taxa AGREGADA de pedidos atribuíveis deixa de ter um tecto em
// taxa — N chamadores valem N vezes a dotação. O que continua a limitar o agregado é o tecto de
// runs EM CURSO (`AOS_INGRESS_MAX_INFLIGHT`, intocado), que é o recurso que interessa, e o tecto
// da própria tabela, que limita N. Quem quiser um tecto agregado em TAXA tem de o pedir noutro
// eixo; este ticket não o inventa.
//
// ---------------------------------------------------------------------------
// A TABELA É LIMITADA, E A EVICÇÃO FOI ESCOLHIDA CONTRA ABUSO.
//
// Um `map[principal]*tokenBucket` sem tecto é ele próprio o vector que este ficheiro existe para
// fechar: quem rode principais fá-la crescer sem limite. Daí o tecto
// [AOS_INGRESS_PER_CALLER_MAX].
//
// Cheia a tabela, evicta-se o balde MAIS CHEIO, e a escolha não é estética:
//
//   - um balde À CAPACIDADE não tem estado que se perca — é indistinguível de um recém-criado —,
//     pelo que evictá-lo é grátis;
//   - evictar o mais cheio é INEXPLORÁVEL pelo atacante. Evicção DÁ uma dotação fresca a quem for
//     evictado, logo nunca prejudica a vítima; e um atacante que queira reiniciar o SEU balde
//     drenado não consegue, porque o seu é o mais VAZIO e a política nunca o escolhe.
//
// A alternativa — recusar o chamador novo com a tabela cheia — seria fail-closed mas daria um
// vector melhor ao atacante: enchia a tabela com principais descartáveis e trancava a porta a
// todos os outros.
// ---------------------------------------------------------------------------

// DefaultPerCallerMax é o tecto por omissão da tabela de baldes por-chamador. Dimensionado para
// um nó de referência: alto o suficiente para que nenhuma operação real bata no tecto, baixo o
// suficiente para que a tabela seja memória irrelevante (cada entrada é um [tokenBucket], dezenas
// de bytes).
const DefaultPerCallerMax = 4096

// baldesPorChamador é uma tabela LIMITADA de token-buckets indexada pelo principal do chamador.
// Concorrente-segura. O zero-value não é utilizável — usa [newBaldesPorChamador].
type baldesPorChamador struct {
	mu     sync.Mutex
	baldes map[string]*tokenBucket

	tecto    int
	capacity float64
	rate     float64
	now      func() time.Time

	// eviccoes conta as evicções por tecto. Existe para que o tecto seja OBSERVÁVEL: uma tabela
	// que evicta constantemente está subdimensionada, e sem esta contagem isso é invisível —
	// nada no comportamento externo distingue «tabela folgada» de «tabela a reciclar baldes a
	// cada pedido».
	eviccoes int64
}

// newBaldesPorChamador constrói a tabela. `tecto <= 0` devolve nil: SEM tabela, e o chamador
// TEM de tratar nil como «não composto» em vez de silenciosamente permitir — ver [allow].
func newBaldesPorChamador(tecto int, capacity, rate float64, now func() time.Time) *baldesPorChamador {
	if tecto <= 0 {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	return &baldesPorChamador{
		baldes:   make(map[string]*tokenBucket, 8),
		tecto:    tecto,
		capacity: capacity,
		rate:     rate,
		now:      now,
	}
}

// allow consome um token do balde de `principal`, criando-o se necessário.
//
// Um receiver nil devolve false: a tabela NÃO COMPOSTA não admite nada por esta via, e quem chama
// tem de decidir explicitamente o que fazer (ver [apiHandler.admitirSubmissao], que cai no balde
// global). O contrário — nil a permitir — seria o modo de falha que este repositório persegue: uma
// barreira ausente que se comporta como uma barreira aberta.
//
// Um `principal` vazio devolve false pela mesma razão: sem principal não há atribuição, e atribuir
// a "" juntaria todos os chamadores não-identificados num balde partilhado que parece por-chamador.
func (b *baldesPorChamador) allow(principal string) bool {
	if b == nil || principal == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if balde, ok := b.baldes[principal]; ok {
		return balde.allow()
	}

	if len(b.baldes) >= b.tecto {
		b.evictarMaisCheioLocked()
	}
	balde := newTokenBucket(b.capacity, b.rate, b.now)
	b.baldes[principal] = balde
	return balde.allow()
}

// evictarMaisCheioLocked remove o balde com MAIS tokens. Exige b.mu.
//
// Lê `tokens` sem tomar o mutex do balde: é uma leitura aproximada de propósito. Tomar o mutex de
// cada balde para escolher a vítima seria ordem de aquisição de N mutexes dentro do mutex da
// tabela, e o valor exacto não muda a correcção — a política só precisa de preferir os cheios aos
// vazios. Uma leitura desalinhada escolheria uma vítima ligeiramente pior, nunca uma insegura: a
// vítima ganha uma dotação fresca.
func (b *baldesPorChamador) evictarMaisCheioLocked() {
	var (
		vitima    string
		melhor    float64 = -1
		encontrou bool
	)
	for p, balde := range b.baldes {
		t := balde.tokens
		if t > melhor {
			vitima, melhor, encontrou = p, t, true
		}
	}
	if encontrou {
		delete(b.baldes, vitima)
		b.eviccoes++
	}
}

// tamanho devolve quantos baldes a tabela tem. Só para teste e para métrica.
func (b *baldesPorChamador) tamanho() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.baldes)
}

// evictados devolve o número de evicções por tecto desde o arranque.
func (b *baldesPorChamador) evictados() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.eviccoes
}

// temPrincipal diz se a tabela tem um balde para `principal`. Só para teste: é o que torna a
// política de evicção VERIFICÁVEL em vez de declarada.
func (b *baldesPorChamador) temPrincipal(principal string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.baldes[principal]
	return ok
}
