package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// OS DOIS ZEROS SAO DIFERENTES E TEM DE SE LER DIFERENTES.
//
// Achado D. O `avaliavel` era calculado das amostras da JANELA corrente (5 min) e o HELP
// prometia uma propriedade ESTRUTURAL — "o SLI nao tem produtor neste no e a regra NUNCA pode
// disparar". Um SLI VIVO num no sem trafego lia-se como permanentemente morto, e isso incluia
// tres alertas `critical`.
//
// A funcao e PURA, pelo mesmo motivo que os banners de postura: cobre-se cada estado sem
// levantar um no.
func TestProdutor_ClassificaPorSLIENaoPorTrafego(t *testing.T) {
	casos := []struct {
		sli         string
		comTorneira bool
		semTorneira bool
	}{
		// SONDAS LOCAIS por passagem: existem sempre, torneira ou nao. Sao as duas que
		// ficam estaveis num no ocioso — e por isso as unicas que o teste-ancora antigo
		// conseguia observar.
		{otelgenai.SLIControlPlaneAvailability, true, true},
		{otelgenai.SLIAuditWORMIntegrity, true, true},
		// DERIVADOS DOS SPANS DO PROPRIO NO: o produtor e este binario. Sao EXACTAMENTE os
		// tres que ficavam mal rotulados num no sem trafego.
		{otelgenai.SLIMediationOverheadP95, true, false},
		{otelgenai.SLICostPerTrajectory, true, false},
		{otelgenai.SLIOverrideRate, true, false},
		// SEM PRODUTOR NESTE BINARIO, com ou sem torneira. Para estes o rotulo antigo estava
		// certo — e continua a estar.
		{otelgenai.SLIHeadroomTokens, false, false},
		{otelgenai.SLIReplayFidelity, false, false},
		{otelgenai.SLICacheHitRate, false, false},
		{otelgenai.SLISandboxColdStartP95, false, false},
	}
	for _, tc := range casos {
		if got := produtorNoNo(tc.sli, true); got != tc.comTorneira {
			t.Errorf("produtorNoNo(%q, torneira LIGADA)=%v, queria %v", tc.sli, got, tc.comTorneira)
		}
		if got := produtorNoNo(tc.sli, false); got != tc.semTorneira {
			t.Errorf("produtorNoNo(%q, torneira DESLIGADA)=%v, queria %v", tc.sli, got, tc.semTorneira)
		}
	}

	// CONTROLO ANTI-VACUIDADE: a torneira TEM de mudar alguma coisa. Sem esta asercao, uma
	// implementacao que devolvesse sempre `false` — ou sempre `true` — passaria todos os
	// casos cujo par calhasse coincidir, e o parametro seria decorativo.
	if produtorNoNo(otelgenai.SLIMediationOverheadP95, true) == produtorNoNo(otelgenai.SLIMediationOverheadP95, false) {
		t.Fatal("a torneira nao muda a classificacao de um SLI derivado de spans — o parametro e decorativo")
	}
	// E TEM de haver SLIs dos dois lados: se todos fossem `false`, o rotulo nao distinguiria
	// nada e o defeito ficaria de pe com outra roupa.
	if !produtorNoNo(otelgenai.SLIAuditWORMIntegrity, false) {
		t.Fatal("nenhum SLI e sempre-produtor — o rotulo nao teria como valer 1 num no ocioso")
	}

	// UM SLI DESCONHECIDO nasce SEM produtor. E a direccao segura: anunciar uma regra viva
	// sobre um sinal que ninguem alimenta e a promessa a mais que este eixo fecha.
	if produtorNoNo("sli_que_alguem_acrescentou_ao_catalogo", true) {
		t.Fatal("um SLI novo nasceu declarado COM produtor sem ninguem o ter cablado")
	}
}

// O ROTULO CHEGA AO /metrics, E O HELP DEIXA DE PROMETER O QUE NAO MEDE.
func TestProdutor_RotuloSaiNoMetricsComOHelpCorrigido(t *testing.T) {
	// UM NO COM TORNEIRA E SEM TRAFEGO — o cenario EXACTO do defeito: os produtores
	// existem (o exportador esta composto) e a janela esta vazia porque nao correu nada.
	// Nao se emitem spans DE PROPOSITO; o teste-ancora antigo emitia oito, e era por isso
	// que nunca podia observar esta classe.
	node := aos274Node(t, 50*time.Millisecond)
	if node.sloTap == nil {
		t.Fatal("PRECONDICAO: a torneira de spans tem de estar composta")
	}
	svc := aos274Service(t, node)
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	svc.EvaluateSLOsNow(context.Background())

	corpo := metricsBody(t, h)

	if !strings.Contains(corpo, `produtor="`) {
		t.Fatalf("o rotulo `produtor` nao chega ao /metrics — a classificacao existe e ninguem a le")
	}
	// O HELP NAO PODE CONTINUAR A PROMETER ESTRUTURA A PARTIR DO `avaliavel`.
	help := ""
	for _, l := range strings.Split(corpo, "\n") {
		if strings.HasPrefix(l, "# HELP aos_alert_firing ") {
			help = l
			break
		}
	}
	if help == "" {
		t.Fatal("aos_alert_firing sem linha de HELP")
	}
	if strings.Contains(help, `avaliavel="0" = o SLI nao tem produtor`) {
		t.Fatalf("o HELP ainda deriva a propriedade ESTRUTURAL do rotulo da JANELA: %s", help)
	}
	for _, termo := range []string{`produtor="0"`, `produtor="1"`, "janela esta VAZIA", "NUNCA dispara"} {
		if !strings.Contains(help, termo) {
			t.Fatalf("o HELP nao explica %q — os dois zeros continuariam a ler-se igual\n%s", termo, help)
		}
	}

	// AS DUAS SONDAS LOCAIS TEM `produtor="1"` num no que nunca viu um span. E esta a
	// asercao que o teste-ancora antigo NAO conseguia fazer, e a razao de o defeito ter
	// passado: ele escolheu justamente o SLI cujo `avaliavel` e invariante ao trafego.
	for _, alerta := range []string{"audit_worm_integrity_broken", "control_plane_availability_low"} {
		if !linhaDoAlerta(t, corpo, alerta, `produtor="1"`) {
			t.Fatalf("%s devia ter produtor=\"1\" — a sonda e local e nao depende de trafego", alerta)
		}
	}
	// E OS TRES CRITICAL DERIVADOS DE SPANS, NUM NO SEM TRAFEGO: `avaliavel="0"` (a janela
	// esta mesmo vazia) mas `produtor="1"` — vivos, nao mortos. Era exactamente isto que o
	// HELP antigo negava.
	for _, alerta := range []string{"mediation_overhead_high", "cost_per_trajectory_high", "mediation_overhead_p95_high"} {
		if !linhaDoAlerta(t, corpo, alerta, `avaliavel="0"`) {
			t.Fatalf("PRECONDICAO: %s devia estar sem amostras num no sem trafego", alerta)
		}
		if !linhaDoAlerta(t, corpo, alerta, `produtor="1"`) {
			t.Fatalf("%s e um CRITICAL vivo e sai rotulado como sem produtor — o operador leria que nunca dispara", alerta)
		}
	}
	// CONTROLO: os que NAO tem produtor continuam a diz-lo.
	for _, alerta := range []string{"headroom_budget_exhaustion", "replay_fidelity_low"} {
		if !linhaDoAlerta(t, corpo, alerta, `produtor="0"`) {
			t.Fatalf("CONTROLO: %s nao tem produtor neste binario e devia continuar a declara-lo", alerta)
		}
	}
}

func linhaDoAlerta(t *testing.T, corpo, alerta, termo string) bool {
	t.Helper()
	for _, l := range strings.Split(corpo, "\n") {
		if strings.HasPrefix(l, "aos_alert_firing{") && strings.Contains(l, `alert="`+alerta+`"`) {
			return strings.Contains(l, termo)
		}
	}
	t.Fatalf("alerta %q ausente do /metrics", alerta)
	return false
}

func metricsBody(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics devolveu %d", rec.Code)
	}
	return rec.Body.String()
}
