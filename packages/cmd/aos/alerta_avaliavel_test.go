package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------------------------
// UM ALERTA QUE NUNCA PODE DISPARAR TEM DE O DIZER.
//
// Achado da verificação de completude de 2026-08-23, medido em PRODUÇÃO antes de se corrigir:
//
//	aos_slo_samples{sli="headroom_tokens"}   0
//	aos_slo_samples{sli="replay_fidelity"}   0
//	aos_alert_firing{alert="headroom_budget_exhaustion",...}  0
//	aos_alert_firing{alert="replay_fidelity_low",...}         0
//	aos_alert_firing{alert="audit_worm_integrity_broken",...} 0   ← este TEM amostra
//
// O HELP dizia «0 = regra avaliada sem disparar». Para os dois primeiros era FALSO: os SLIs não
// têm produtor neste binário — o escalonador e o motor de replay vivem noutro processo — pelo que
// nunca terão amostra e a regra é ESTRUTURALMENTE incapaz de disparar.
//
// Um painel todo a verde não distinguia «avaliado e bem» de «nunca poderá ficar vermelho», e três
// dos cinco runbooks canónicos (RB-01, RB-03, RB-05) estavam desse lado.
//
// PORQUE UM RÓTULO E NÃO A OMISSÃO: para ALERTAS esta casa já tinha decidido o contrário — «um
// alerta cujo runbook não resolve leva `runbook_orphan="1"` EM VEZ DE SER OMITIDO». Omitir uma
// regra fá-la desaparecer do painel, que é perder a própria regra.
// ---------------------------------------------------------------------------------------------

var reAlerta = regexp.MustCompile(`aos_alert_firing\{([^}]*)\}\s+(\d+)`)

// alertasDoMetrics devolve, do corpo do /metrics, o mapa alerta → rótulos.
func alertasDoMetrics(t *testing.T, body string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range reAlerta.FindAllStringSubmatch(body, -1) {
		rotulos := m[1]
		i := strings.Index(rotulos, `alert="`)
		if i < 0 {
			continue
		}
		resto := rotulos[i+len(`alert="`):]
		j := strings.Index(resto, `"`)
		if j < 0 {
			continue
		}
		out[resto[:j]] = rotulos
	}
	return out
}

// metricsDoNo274 corre o avaliador e devolve o corpo do /metrics.
func metricsDoNo274(t *testing.T) string {
	t.Helper()
	node := aos274Node(t, 50*time.Millisecond)
	svc := aos274Service(t, node)
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	aos274EmitToolSpans(node, 8)
	svc.EvaluateSLOsNow(context.Background())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics devolveu %d", rec.Code)
	}
	return rec.Body.String()
}

func TestUmAlertaSEM_PRODUTOR_DeclaraSeNaoAvaliavel(t *testing.T) {
	alertas := alertasDoMetrics(t, metricsDoNo274(t))

	// CONTROLO DO PRÓPRIO TESTE: sem alertas extraídos, tudo o que vem a seguir passa por
	// vacuidade. Uma expressão regular que não casa com nada não prova ausência de nada.
	if len(alertas) < 5 {
		t.Fatalf("so extrai %d alerta(s) do /metrics — a leitura falhou e as asercoes seguintes "+
			"seriam vacuas: %v", len(alertas), alertas)
	}

	for _, semProdutor := range []string{"headroom_budget_exhaustion", "replay_fidelity_low"} {
		rotulos, ok := alertas[semProdutor]
		if !ok {
			t.Errorf("o alerta %q DESAPARECEU do /metrics — omitir uma regra e perder a propria "+
				"regra do painel, e a casa ja tinha decidido o contrario para o runbook orfao", semProdutor)
			continue
		}
		if !strings.Contains(rotulos, `avaliavel="0"`) {
			t.Errorf("o alerta %q nao tem produtor neste no e NAO se declara inavaliavel (%s) — um "+
				"painel a verde nao distingue «avaliado e bem» de «nunca podera ficar vermelho»",
				semProdutor, rotulos)
		}
	}
}

// TestUmAlertaCOM_PRODUTOR_DeclaraSeAvaliavel é a âncora anti-vacuidade.
//
// Sem ela, marcar TUDO como inavaliável passaria no teste acima — e a distinção que o rótulo
// existe para fazer desapareceria no sentido inverso.
func TestUmAlertaCOM_PRODUTOR_DeclaraSeAvaliavel(t *testing.T) {
	alertas := alertasDoMetrics(t, metricsDoNo274(t))

	rotulos, ok := alertas["audit_worm_integrity_broken"]
	if !ok {
		t.Fatal("o alerta de integridade do WORM devia estar no /metrics")
	}
	if !strings.Contains(rotulos, `avaliavel="1"`) {
		t.Errorf("o alerta de integridade do WORM TEM produtor (a sonda do WORM corre sempre) e "+
			"declarou-se inavaliavel: %s", rotulos)
	}
}

// TestOSinalDeRunbookORFAOContinuaLa — o rótulo novo não pode ter comido o que já existia.
func TestOSinalDeRunbookORFAOContinuaLa(t *testing.T) {
	alertas := alertasDoMetrics(t, metricsDoNo274(t))
	var comOrfao int
	for _, rotulos := range alertas {
		if strings.Contains(rotulos, "runbook_orphan=") {
			comOrfao++
		}
	}
	if comOrfao != len(alertas) {
		t.Errorf("%d de %d alertas perderam o rotulo runbook_orphan ao ganhar o avaliavel",
			len(alertas)-comOrfao, len(alertas))
	}
}
