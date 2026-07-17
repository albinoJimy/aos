package actiondedup

import "sync"

// Registry encaminha observações de acção para o [Detector] da trajectória (run) certa. É a
// conveniência do Agent Runtime que gere MUITOS runs em simultâneo: o RT chama
// Observe(runID, hash) quando um execute_tool fecha e liga o breaker de cada run com
// Source(runID). Cada run tem a SUA janela deslizante independente. Seguro para uso
// concorrente. Construir com [NewRegistry].
//
// A config é POR REGISTRY (tipicamente uma classe de agente); para classes com limiares
// distintos, use um Registry por classe (ou [Detector] por run directamente). Elevar a
// resolução de config a por-run é deliberadamente deixado de fora para não sobre-desenhar.
type Registry struct {
	cfg Config

	mu    sync.Mutex
	byRun map[string]*Detector
}

// NewRegistry constrói o registry com a config aplicada a cada run que vier a observar.
func NewRegistry(cfg Config) *Registry {
	return &Registry{cfg: cfg, byRun: make(map[string]*Detector)}
}

// detectorLocked devolve (criando se preciso) o detector do run. Assume o lock detido.
func (r *Registry) detectorLocked(runID string) *Detector {
	d, ok := r.byRun[runID]
	if !ok {
		d = NewDetector(r.cfg)
		r.byRun[runID] = d
	}
	return d
}

// Observe encaminha o hash estável da tool call para o detector do run (criando-o na
// primeira acção do run). É o ponto que o RT chama quando o execute_tool fecha.
func (r *Registry) Observe(runID, hash string) {
	r.mu.Lock()
	d := r.detectorLocked(runID)
	r.mu.Unlock()
	d.Observe(hash)
}

// Source devolve a [breaker.ProgressSource] do run (o próprio [Detector]), para cablar em
// breaker.WithProgressSource. Cria o detector do run se ainda não existir, de modo que a
// cablagem fail-closed do breaker (fonte não-nil quando MaxStaleIterations>0) fique
// satisfeita antes da primeira acção.
func (r *Registry) Source(runID string) *Detector {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.detectorLocked(runID)
}

// Forget descarta o detector de um run terminado (liberta a janela). Idempotente.
func (r *Registry) Forget(runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byRun, runID)
}
