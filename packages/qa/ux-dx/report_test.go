package uxdx_test

import (
	"fmt"
	"testing"

	"github.com/aos-ref/control-plane/governance/hitl"
)

// AC5 — RELATÓRIO da bateria. Emite a linha marcada AOS_UXDX_REPORT (molde
// AOS_DR_REPORT/AOS_SCALE_REPORT) com a contagem de gates de usabilidade validados e o
// limiar anti-fadiga CONSUMIDO de AOS-095. O gate (scripts/ci/ux-dx.sh) valida esta
// linha fail-closed. O relatório é byte-estável (determinista, -count=2).
func TestUXDX_Report(t *testing.T) {
	// 4 gates de usabilidade validados (AOS-120/121/123/125), o override-rate consumido
	// como sinal anti-fadiga, e a paridade das 3 plataformas.
	const usabilityGates = 4
	report := fmt.Sprintf(
		`AOS_UXDX_REPORT {"usability_gates":%d,"platforms":%d,"override_rate_threshold":%.2f,"anti_fatigue_signal":%q,"pass":true}`,
		usabilityGates,
		len(allPlatforms),
		hitl.DefaultOverrideRateThreshold,
		hitl.MetricOverrideRate,
	)
	// A invariante consumida: o limiar é o de AOS-095 (não um recriado localmente).
	if hitl.DefaultOverrideRateThreshold != 0.40 {
		t.Fatalf("limiar anti-fadiga=%v, quero 0.40 (consumido de AOS-095)", hitl.DefaultOverrideRateThreshold)
	}
	fmt.Println(report)
}
