package integration

import (
	"context"
	"sync"

	"github.com/aos-ref/platform/registry/revalidation"
)

// RecordingAlerter é um [revalidation.Alerter] observável e seguro para
// concorrência: acumula os alertas de divergência emitidos pela revalidação por
// chamada. É a COSTURA para o pilar de alertas de EPIC-08, que ainda NÃO existe no
// repo — quando existir, o pipeline real (ex.: PagerDuty/webhook/bus) substitui
// este sink via a mesma porta [revalidation.Alerter].
//
// Best-effort por contrato: um alerta NUNCA bloqueia nem desbloqueia uma decisão;
// é observacional/reactivo. Aqui, "emitir" é gravar em memória para inspecção
// (testes de integração, forense local).
type RecordingAlerter struct {
	mu     sync.Mutex
	alerts []revalidation.Alert
}

// NewRecordingAlerter constrói um sink vazio.
func NewRecordingAlerter() *RecordingAlerter { return &RecordingAlerter{} }

// Alert implementa [revalidation.Alerter]: regista o incidente (best-effort).
func (a *RecordingAlerter) Alert(_ context.Context, alert revalidation.Alert) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.alerts = append(a.alerts, alert)
}

// Alerts devolve uma cópia dos alertas registados, por ordem de emissão.
func (a *RecordingAlerter) Alerts() []revalidation.Alert {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]revalidation.Alert, len(a.alerts))
	copy(out, a.alerts)
	return out
}

// Len é o número de alertas registados.
func (a *RecordingAlerter) Len() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.alerts)
}
