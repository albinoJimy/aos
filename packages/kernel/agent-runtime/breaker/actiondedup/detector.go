// Package actiondedup entrega o COLECTOR concreto do sinal de action-dedup (AOS-081)
// que alimenta o circuit breaker multi-sinal do agente vivo (AOS-080).
//
// O detector mantém, POR TRAJECTÓRIA (run), uma JANELA DESLIZANTE dos hashes de acção
// recentes (o mesmo hash(tool+args) ESTÁVEL que o Reference Monitor já anota no span
// execute_tool via [otelgenai.CanonicalToolCallHash] — o detector NÃO re-hasheia, apenas
// consome). Quando a contagem do último hash observado dentro da janela cruza um limiar
// configurável, o agente está a repetir a MESMA acção sem efeito — um loop semântico — e o
// detector reporta ausência de progresso.
//
// A ligação ao breaker é a porta [breaker.ProgressSource]: [Detector] implementa
// MadeProgress() (devolve false quando há loop), pelo que se liga por
// breaker.WithProgressSource(det). A janela deslizante (em vez de um contador cru infinito)
// evita que repetições legítimas ESPARSAS — a mesma tool chamada de longe a longe —
// contem como loop.
package actiondedup

import "sync"

// Config são os parâmetros CONFIGURÁVEIS do detector, resolvidos POR CLASSE de agente
// (nunca constantes hard-coded), alinhados com o MaxStaleIterations por classe do breaker.
type Config struct {
	// WindowSize é o tamanho da janela deslizante (nº de acções recentes retidas). Só
	// contam repetições DENTRO desta janela — repetições esparsas fora dela não somam.
	// Se <= 0, ou menor que Threshold, é elevado a Threshold (uma janela mais curta que o
	// limiar nunca o poderia atingir).
	WindowSize int
	// Threshold é o nº de ocorrências do MESMO hash na janela a partir do qual se sinaliza
	// loop (comparação `>=`, coerente com o fail-closed do avaliador). Se <= 0, o detector
	// fica DESLIGADO (nunca sinaliza loop — MadeProgress devolve sempre true).
	Threshold int
}

// effective normaliza a config: uma janela menor que o limiar nunca o atingiria, logo
// eleva-se WindowSize a Threshold. Threshold <= 0 mantém-se (detector desligado).
func (c Config) effective() Config {
	if c.Threshold <= 0 {
		return Config{WindowSize: 0, Threshold: 0}
	}
	w := c.WindowSize
	if w < c.Threshold {
		w = c.Threshold
	}
	return Config{WindowSize: w, Threshold: c.Threshold}
}

// Detector é o detector de loop semântico de UM run: uma janela deslizante de hashes de
// acção com contagem por hash. Implementa [breaker.ProgressSource] (MadeProgress). Seguro
// para uso concorrente — o Agent Runtime observa acções de goroutines de execução enquanto
// o breaker consulta MadeProgress da sua própria goroutine. Construir com [NewDetector].
type Detector struct {
	cfg Config

	mu      sync.Mutex
	buf     []string       // ring buffer dos últimos WindowSize hashes
	head    int            // próxima posição de escrita (aponta o mais antigo quando cheio)
	filled  int            // nº de posições válidas (<= WindowSize)
	counts  map[string]int // contagem de cada hash presente na janela
	looping bool           // veredicto do último Observe: true ⇒ loop detectado
}

// NewDetector constrói o detector de um run com a config dada (normalizada por
// [Config.effective]). Um Threshold <= 0 devolve um detector inerte (MadeProgress sempre
// true) — o sinal de no-progress por action-dedup fica desligado para esse run.
func NewDetector(cfg Config) *Detector {
	eff := cfg.effective()
	d := &Detector{cfg: eff}
	if eff.Threshold > 0 {
		d.buf = make([]string, eff.WindowSize)
		d.counts = make(map[string]int, eff.WindowSize)
	}
	return d
}

// Observe regista o hash ESTÁVEL de UMA tool call na janela deslizante (tipicamente
// chamado quando o span execute_tool fecha). Actualiza o veredicto de loop: há loop quando
// a contagem do hash agora observado, DENTRO da janela, atinge o Threshold. Um hash novo
// (contagem 1) repõe o veredicto para "progresso". No-op se o detector está desligado.
func (d *Detector) Observe(hash string) {
	if d.cfg.Threshold <= 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.filled == d.cfg.WindowSize {
		// Janela cheia: a posição head detém o mais antigo — despeja-o antes de escrever.
		old := d.buf[d.head]
		d.counts[old]--
		if d.counts[old] <= 0 {
			delete(d.counts, old)
		}
	} else {
		d.filled++
	}
	d.buf[d.head] = hash
	d.head = (d.head + 1) % d.cfg.WindowSize
	d.counts[hash]++
	d.looping = d.counts[hash] >= d.cfg.Threshold
}

// Enabled reporta se o detector está ARMADO (Threshold > 0). Implementa a porta OPCIONAL
// breaker.EnabledSource: um detector INERTE (Threshold<=0, cujo MadeProgress é sempre true)
// reporta false. É isto que permite à cablagem fail-closed do breaker recusar ligar um
// detector cego quando MaxStaleIterations>0 — fechando o buraco de uma fonte não-nil mas
// permanentemente muda ao sinal (a nil-check do breaker sozinha não o apanha). Não toma lock:
// cfg é imutável após [NewDetector].
func (d *Detector) Enabled() bool {
	return d.cfg.Threshold > 0
}

// MadeProgress implementa [breaker.ProgressSource]: reporta se a ÚLTIMA acção observada
// representou progresso. Devolve false quando o último hash cruzou o limiar de repetição na
// janela (loop semântico) — é isso que faz o breaker somar uma iteração estéril. Um detector
// nunca-observado (ou desligado) devolve true (não há evidência de loop).
func (d *Detector) MadeProgress() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.looping
}

// Looping expõe o veredicto corrente (true ⇒ loop detectado) para observabilidade/testes,
// sem depender do sentido invertido de MadeProgress.
func (d *Detector) Looping() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.looping
}

// Reset esvazia a janela e o veredicto — usado ao retomar um run após um trip, para que a
// contagem parta de zero e não re-dispare imediatamente sobre acções antigas.
func (d *Detector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.head = 0
	d.filled = 0
	d.looping = false
	if d.counts != nil {
		d.counts = make(map[string]int, d.cfg.WindowSize)
	}
}
