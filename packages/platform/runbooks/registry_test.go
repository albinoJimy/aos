package runbooks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// TestNoOrphan_DefaultConfigValidates: o registo e a config de REFERÊNCIA de AOS-105
// estão coerentes — nenhum alerta órfão, todos os canónicos presentes, ligação
// bidireccional fechada. É o caminho verde do gate de CI.
func TestNoOrphan_DefaultConfigValidates(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatalf("Validate() = %v, quero nil (registo↔alertas coerente)", err)
	}
}

// TestNoOrphan_AlertReferencingMissingRunbook_Fails é o TESTE FAIL-CLOSED exigido pelo
// ticket: se um alerta de AOS-105 referencia um runbook SEM entrada no registo, o gate
// FALHA. Parte da config de referência e desvia UMA regra para um ID inexistente.
func TestNoOrphan_AlertReferencingMissingRunbook_Fails(t *testing.T) {
	cfg := otelgenai.DefaultOperationalAlertConfig()
	if len(cfg.Rules) == 0 {
		t.Fatal("config de referência sem regras")
	}
	// Desvia a primeira regra para um runbook que não existe no registo.
	cfg.Rules[0].Route.Runbook = "RB-INEXISTENTE"

	err := ValidateAgainstAlerts(cfg)
	if err == nil {
		t.Fatal("ValidateAgainstAlerts devia FALHAR com um alerta a apontar para runbook inexistente (órfão)")
	}
	if !errors.Is(err, ErrRegistry) {
		t.Fatalf("erro = %v, quero que embrulhe ErrRegistry", err)
	}
}

// TestNoOrphan_AlertWithEmptyRunbook_Fails: uma regra sem runbook nenhum é órfã por
// definição (defensivo — AOS-105 já o proíbe, mas o gate não confia cegamente).
func TestNoOrphan_AlertWithEmptyRunbook_Fails(t *testing.T) {
	cfg := otelgenai.DefaultOperationalAlertConfig()
	cfg.Rules[0].Route.Runbook = ""
	if err := ValidateAgainstAlerts(cfg); !errors.Is(err, ErrRegistry) {
		t.Fatalf("regra sem runbook = %v, quero ErrRegistry", err)
	}
}

// TestCanonical_AllFiveRegisteredWithStructure: os cinco canónicos RB-01..RB-05 estão
// no registo, classificados como canónicos, com doc estruturado e ADR de conformidade.
func TestCanonical_AllFiveRegisteredWithStructure(t *testing.T) {
	want := map[string]string{
		"RB-01": "ADR-008", "RB-02": "ADR-001", "RB-03": "ADR-008",
		"RB-04": "ADR-011", "RB-05": "ADR-012",
	}
	got := CanonicalIDs()
	if len(got) != 5 {
		t.Fatalf("CanonicalIDs = %v, quero 5", got)
	}
	for id, adr := range want {
		e, ok := Lookup(id)
		if !ok {
			t.Fatalf("runbook canónico %q ausente do registo", id)
		}
		if e.Kind != KindCanonical {
			t.Errorf("%s: Kind = %q, quero canonical", id, e.Kind)
		}
		if e.DocPath == "" {
			t.Errorf("%s: sem DocPath (estrutura sinal/diagnóstico/mitigação)", id)
		}
		if e.ADR != adr {
			t.Errorf("%s: ADR = %q, quero %q", id, e.ADR, adr)
		}
		if e.OwnerTicket != "AOS-106" {
			t.Errorf("%s: OwnerTicket = %q, quero AOS-106", id, e.OwnerTicket)
		}
	}
}

// TestCanonicalDocsExist: cada doc estruturado referenciado por um canónico existe no
// repo (a entrada resolve para um ficheiro real, não só para um ID). É a metade do
// invariante "o ID resolve para uma entrada — e o doc existe".
func TestCanonicalDocsExist(t *testing.T) {
	root := repoRoot(t)
	for _, e := range Registry() {
		if e.Kind != KindCanonical {
			continue
		}
		p := filepath.Join(root, filepath.FromSlash(e.DocPath))
		if _, err := os.Stat(p); err != nil {
			t.Errorf("doc do runbook %s não existe em %s: %v", e.ID, e.DocPath, err)
		}
	}
}

// TestRB02_NoAlertButRegistered: RB-02 (zumbi cross-host) está no registo como runbook
// canónico SEM alerta, com justificação, e NÃO é referenciado por nenhum alerta de
// AOS-105 — documentado, não órfão silencioso.
func TestRB02_NoAlertButRegistered(t *testing.T) {
	e, ok := Lookup(RunbookZombieCrossHost)
	if !ok {
		t.Fatal("RB-02 ausente do registo")
	}
	if e.Kind != KindCanonical {
		t.Errorf("RB-02 Kind = %q, quero canonical", e.Kind)
	}
	if e.Alert != "" {
		t.Errorf("RB-02 não devia ter alerta, tem %q", e.Alert)
	}
	if e.NoAlertReason == "" {
		t.Error("RB-02 sem justificação para não ter alerta")
	}
	// Nenhuma regra de AOS-105 encaminha para RB-02.
	for _, r := range otelgenai.DefaultOperationalAlertConfig().Rules {
		if r.Route.Runbook == RunbookZombieCrossHost {
			t.Fatalf("regra %q encaminha para RB-02, que se declara sem alerta", r.Name)
		}
	}
}

// TestForwardRefs_Marked: PROC-DR e PROC-ESCALA estão no registo como forward-refs
// marcados — PROC-DR já documentado (AOS-102), PROC-ESCALA pendente (AOS-107). Ambos
// referenciados por alertas de AOS-105, logo NÃO podem ser órfãos.
func TestForwardRefs_Marked(t *testing.T) {
	dr, ok := Lookup(otelgenai.ProcDisasterRecovery)
	if !ok {
		t.Fatal("PROC-DR ausente do registo")
	}
	if dr.Kind != KindForwardRef {
		t.Errorf("PROC-DR Kind = %q, quero forward_ref", dr.Kind)
	}
	if dr.OwnerTicket != "AOS-102" || dr.Pending {
		t.Errorf("PROC-DR = %+v, quero owner AOS-102 e não-pendente (já existe no README de platform/dr)", dr)
	}

	esc, ok := Lookup(otelgenai.ProcScaleOut)
	if !ok {
		t.Fatal("PROC-ESCALA ausente do registo")
	}
	if esc.Kind != KindForwardRef {
		t.Errorf("PROC-ESCALA Kind = %q, quero forward_ref", esc.Kind)
	}
	if esc.OwnerTicket != "AOS-107" || !esc.Pending {
		t.Errorf("PROC-ESCALA = %+v, quero owner AOS-107 e PENDENTE (forward-ref para AOS-107)", esc)
	}
}

// TestBidirectional_EveryReferencedRunbookResolves: o invariante inverso — TODO ID de
// runbook que ALGUMA regra de AOS-105 referencia resolve para uma entrada. Percorre a
// config real (não uma mutada), garantindo que a fonte de verdade dos alertas nunca
// aponta para o vazio.
func TestBidirectional_EveryReferencedRunbookResolves(t *testing.T) {
	for _, r := range otelgenai.DefaultOperationalAlertConfig().Rules {
		if _, ok := Lookup(r.Route.Runbook); !ok {
			t.Errorf("alerta %q referencia runbook %q sem entrada no registo", r.Name, r.Route.Runbook)
		}
	}
}

// repoRoot sobe da directoria do módulo até encontrar a que contém docs/runbooks.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if fi, err := os.Stat(filepath.Join(dir, "docs", "runbooks")); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("não encontrei a raiz do repo (docs/runbooks) a partir de %s", dir)
		}
		dir = parent
	}
}
