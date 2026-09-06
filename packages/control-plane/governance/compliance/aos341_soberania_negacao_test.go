package compliance

import (
	"testing"

	"github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------
// A SEGUNDA METADE do AOS-341: a projecção de soberania tem de VER a negação que o
// Reference Monitor sela.
//
// A primeira metade vive no `kernel/reference-monitor`, onde o registo é construído
// (`aos341_obrigacao_causadora_test.go`). Cada uma tem teste no seu pacote: um teste só do
// lado do RM ficaria verde a provar que o selo sai, sem provar que alguém o lê; um teste só
// daqui ficaria verde sobre registos que o RM nunca produziria.
//
// Os registos abaixo são as formas MEDIDAS que o RM sela num deny
// `E_OBLIGATION_UNSATISFIED` por obrigação de região — não formas inventadas para o teste.
// ---------------------------------------------------------------------------

func negacaoPorRegiao(seq uint64, regiaoDoRecurso string, obs []audit.Obligation) audit.AuditRecord {
	return audit.AuditRecord{
		AuditSeq: seq, Partition: "run-1", Decision: audit.DecisionDeny,
		Capability: "cap:fs.read", ToolID: "doc_read",
		Code: "E_OBLIGATION_UNSATISFIED", DeniedBy: "obligation",
		Reason:      `obrigacao de regiao "eu" mas recurso sem regiao resolvida: cross-border negado (fail-closed)`,
		Resource:    audit.Resource{Type: "file", Value: "doc://notas", Region: regiaoDoRecurso},
		Obligations: obs,
	}
}

func obrigacaoRegiaoSelada() []audit.Obligation {
	return []audit.Obligation{{Type: "region", Params: map[string]string{"region": "eu"}}}
}

// TestAOS341_NegacaoSemRegiaoNoRecursoEProjectada é o caso que se perdia por completo. Sem a
// obrigação selada, as DUAS fontes de `sovereigntyRegion` estavam vazias e o registo saía
// com governed=false — invisível na secção que existe para mostrar o que a soberania
// governou, apesar de ter sido a soberania a negá-lo.
func TestAOS341_NegacaoSemRegiaoNoRecursoEProjectada(t *testing.T) {
	rec := negacaoPorRegiao(1, "", obrigacaoRegiaoSelada())

	got := projectSovereignty([]audit.AuditRecord{rec})

	if len(got) != 1 {
		t.Fatalf("a negacao POR soberania nao foi projectada (%d eventos) — o recurso nao tem "+
			"regiao resolvida, pelo que so a obrigacao selada a pode governar", len(got))
	}
	if got[0].Region != "eu" {
		t.Errorf("Region = %q; quero a regiao EXIGIDA pela obrigacao (%q)", got[0].Region, "eu")
	}
	if got[0].Decision != audit.DecisionDeny {
		t.Errorf("Decision = %v; quero deny", got[0].Decision)
	}
}

// TestAOS341_CrossBorderFicaSobARegiaoExigida fixa a metade do AC3 que era ambiguidade de
// contrato: numa negação cross-border a região do recurso e a exigida DIVERGEM, e o evento
// tem de ficar sob a exigida. Sem a obrigação selada ficava sob "us" — a região recusada —
// ao lado de eventos onde a mesma coluna significa a autorizada.
func TestAOS341_CrossBorderFicaSobARegiaoExigida(t *testing.T) {
	rec := negacaoPorRegiao(2, "us", obrigacaoRegiaoSelada())

	got := projectSovereignty([]audit.AuditRecord{rec})

	if len(got) != 1 {
		t.Fatalf("esperava 1 evento, obtive %d", len(got))
	}
	if got[0].Region != "eu" {
		t.Errorf("Region = %q; a coluna e a regiao EXIGIDA, nao a do recurso recusado (%q)",
			got[0].Region, "us")
	}
}

// TestAOS341_SemObrigacaoOFallbackContinua é o controlo do fallback: a correcção não pode
// estreitar a projecção. Um registo sem obrigação `region` mas com região no recurso — a
// forma que já existia antes do AOS-341 — continua a ser projectado, e sob a do recurso.
func TestAOS341_SemObrigacaoOFallbackContinua(t *testing.T) {
	rec := negacaoPorRegiao(3, "pt", nil)

	got := projectSovereignty([]audit.AuditRecord{rec})

	if len(got) != 1 || got[0].Region != "pt" {
		t.Fatalf("o fallback pelo Resource.Region regrediu: %+v", got)
	}
}

// TestAOS341_SemNenhumaFonteContinuaForaDaProjeccao é o controlo negativo, e é o que impede
// a correcção preguiçosa de projectar TUDO. Um registo que a soberania não governa — sem
// obrigação e sem região no recurso — continua fora. Sem este teste, uma projecção que
// deixasse de filtrar passaria os três de cima.
func TestAOS341_SemNenhumaFonteContinuaForaDaProjeccao(t *testing.T) {
	rec := negacaoPorRegiao(4, "", nil)

	if got := projectSovereignty([]audit.AuditRecord{rec}); len(got) != 0 {
		t.Fatalf("registo sem fonte regional foi projectado como soberania: %+v", got)
	}
}
