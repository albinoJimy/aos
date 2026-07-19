// Package runbooks é o REGISTO canónico dos runbooks operacionais (AOS-106) e o gate
// FAIL-CLOSED de NÃO-ÓRFÃOS bidireccional alerta↔runbook.
//
// # O que este módulo faz (e não faz)
//
// COMPÕE o alerting-as-code de AOS-105 (substrate/otel-genai) — não o reimplementa.
// Importa otel-genai APENAS para ler os IDs de runbook que os alertas referenciam
// (Route.Runbook de cada regra de [otelgenai.OperationalAlertConfig]) e cruza-os com
// o registo. O import é DOWN (platform → substrate), zero deps externas.
//
// # O invariante NÃO-ÓRFÃO (verificado no CI, AC do ticket)
//
// [ValidateAgainstAlerts] é fail-closed e falha se:
//
//   - (a) algum ID de runbook referenciado por um alerta de AOS-105 NÃO resolve para
//     uma entrada no registo (um alerta a apontar para um runbook inexistente é um
//     ÓRFÃO — o teste de CI falha);
//   - (b) algum runbook canónico RB-01..RB-05 falta no registo, ou não declara a sua
//     estrutura (doc + ADR);
//   - (c) a ligação bidireccional diverge: um runbook canónico que DEVE ter alerta não
//     tem o seu alerta na config, ou o alerta declarado não encaminha para ele.
//
// # As honestidades que o registo trata explicitamente
//
//   - RB-02 (zumbi cross-host) NÃO tem SLI-alerta: é diagnóstico por lease/heartbeat,
//     não uma métrica com SLO. Fica no registo como runbook SEM alerta, com
//     justificação ([Entry.NoAlertReason]) — não é um órfão silencioso.
//   - PROC-DR e PROC-ESCALA são FORWARD-REFS a runbooks de OUTROS tickets (AOS-102 /
//     AOS-107). Ambos JÁ existem: PROC-DR no README de platform/dr; PROC-ESCALA em
//     docs/runbooks/PROC-ESCALA.md (escrito pelo AOS-107, que fecha o forward-ref). O
//     registo aceita-os como referências marcadas, NÃO como órfãos — todo o ID que um
//     alerta referencia tem entrada.
package runbooks

import (
	"fmt"
	"sort"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// Kind classifica a proveniência de uma entrada do registo.
type Kind string

const (
	// KindCanonical — os cinco runbooks canónicos deste ticket (AOS-106), documentados
	// em docs/runbooks/RB-0N.md com estrutura sinal→diagnóstico→mitigação.
	KindCanonical Kind = "canonical"
	// KindForwardRef — runbook/procedimento cuja documentação canónica pertence a OUTRO
	// ticket (PROC-DR→AOS-102, PROC-ESCALA→AOS-107). Referenciado por alertas de AOS-105,
	// registado aqui para não ficar órfão.
	KindForwardRef Kind = "forward_ref"
)

// RunbookZombieCrossHost é o ID do RB-02 (zumbi cross-host). Não vive em otel-genai
// porque, ao contrário dos restantes, NÃO tem alerta de SLI (é diagnóstico por
// lease/heartbeat) — logo o alerting-as-code nunca o referencia.
const RunbookZombieCrossHost = "RB-02"

// Entry é uma entrada do registo de runbooks: identidade, proveniência, estrutura
// (doc + ADR) e a ligação ao alerta de AOS-105 (ou a justificação da sua ausência).
type Entry struct {
	// ID é o identificador estável (ex.: "RB-01", "PROC-DR"). Bate com o Route.Runbook
	// que os alertas de AOS-105 usam.
	ID string
	// Title é o título humano do runbook.
	Title string
	// Kind distingue os canónicos (AOS-106) dos forward-refs (outros tickets).
	Kind Kind
	// ADR é o ADR de conformidade citado pelo runbook (vazio nos forward-refs cuja
	// conformidade vive no ticket dono).
	ADR string
	// DocPath é o caminho do doc estruturado, relativo à raiz do repo (ex.:
	// "docs/runbooks/RB-01.md"). Vazio nos forward-refs documentados noutro artefacto.
	DocPath string
	// OwnerTicket é o ticket dono da documentação (AOS-106 nos canónicos; AOS-102/107
	// nos forward-refs).
	OwnerTicket string
	// Pending marca um forward-ref cujo runbook ainda NÃO foi escrito. Aceite pelo
	// registo como pendente-documentado, nunca como órfão silencioso. Ambos os
	// forward-refs (PROC-DR, PROC-ESCALA) já estão escritos, logo Pending=false.
	Pending bool
	// Alert é o nome do alerta de AOS-105 ligado a este runbook (contrato estável de
	// otel-genai). Vazio quando o runbook não tem SLI-alerta (RB-02) — nesse caso
	// NoAlertReason justifica-o.
	Alert string
	// NoAlertReason justifica, para um canónico SEM alerta, porque não tem (RB-02: é
	// diagnóstico operacional por lease/heartbeat, não um SLI). Obrigatório sse
	// Kind==Canonical e Alert=="".
	NoAlertReason string
}

// canonicalIDs é a lista ordenada dos cinco runbooks canónicos que o ticket AOS-106
// tem de entregar. A validação exige que todos existam no registo.
var canonicalIDs = []string{
	otelgenai.RunbookRateLimitCollapse, // RB-01
	RunbookZombieCrossHost,             // RB-02
	otelgenai.RunbookBudgetExhaustion,  // RB-03
	otelgenai.RunbookPDPFailure,        // RB-04
	otelgenai.RunbookAutoModRollback,   // RB-05
}

// registry é o catálogo canónico. Os IDs de alerta são consts de otel-genai (contrato):
// um rename lá parte a compilação aqui — a ligação alerta↔runbook é tight-coupled de
// propósito.
var registry = []Entry{
	{
		ID: otelgenai.RunbookRateLimitCollapse, Title: "Colapso de rate limit (agregado)",
		Kind: KindCanonical, ADR: "ADR-008", DocPath: "docs/runbooks/RB-01.md",
		OwnerTicket: "AOS-106", Alert: otelgenai.AlertHeadroomRateLimitCollapse,
	},
	{
		ID: RunbookZombieCrossHost, Title: "Zumbi cross-host (falso-positivo/positivo)",
		Kind: KindCanonical, ADR: "ADR-001", DocPath: "docs/runbooks/RB-02.md",
		OwnerTicket: "AOS-106", Alert: "",
		NoAlertReason: "diagnóstico operacional por lease/heartbeat + fencing token (não um SLI); " +
			"AOS-105 não emite alerta de zumbi — o runbook é accionado por inspecção de lease",
	},
	{
		ID: otelgenai.RunbookBudgetExhaustion, Title: "Esgotamento de orçamento (tokens/$)",
		Kind: KindCanonical, ADR: "ADR-008", DocPath: "docs/runbooks/RB-03.md",
		OwnerTicket: "AOS-106", Alert: otelgenai.AlertHeadroomBudgetExhaustion,
	},
	{
		ID: otelgenai.RunbookPDPFailure, Title: "Falha de PDP (Policy Decision Point)",
		Kind: KindCanonical, ADR: "ADR-011", DocPath: "docs/runbooks/RB-04.md",
		OwnerTicket: "AOS-106", Alert: otelgenai.AlertControlPlaneAvailabilityLow,
	},
	{
		ID: otelgenai.RunbookAutoModRollback, Title: "Rollback de auto-modificação",
		Kind: KindCanonical, ADR: "ADR-012", DocPath: "docs/runbooks/RB-05.md",
		OwnerTicket: "AOS-106", Alert: otelgenai.AlertReplayFidelityLow,
	},
	// Forward-refs: referenciados por alertas de AOS-105, documentados noutros tickets.
	{
		ID: otelgenai.ProcDisasterRecovery, Title: "Procedimento de DR (recuperação por replay)",
		Kind: KindForwardRef, DocPath: "packages/platform/dr/README.md",
		OwnerTicket: "AOS-102", Pending: false, Alert: otelgenai.AlertAuditWORMIntegrityBroken,
	},
	{
		ID: otelgenai.ProcScaleOut, Title: "Procedimento de escala (pool/cold-start)",
		Kind: KindForwardRef, DocPath: "docs/runbooks/PROC-ESCALA.md", OwnerTicket: "AOS-107", Pending: false,
		Alert: otelgenai.AlertSandboxColdStartP95High,
	},
}

// Registry devolve uma cópia das entradas do registo (ordem estável por ID).
func Registry() []Entry {
	out := make([]Entry, len(registry))
	copy(out, registry)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// index constrói o mapa ID→Entry do registo.
func index() map[string]Entry {
	m := make(map[string]Entry, len(registry))
	for _, e := range registry {
		m[e.ID] = e
	}
	return m
}

// Lookup resolve um ID de runbook para a sua entrada. ok=false se não houver entrada
// (um alerta que referencie um ID sem entrada é um ÓRFÃO).
func Lookup(id string) (Entry, bool) {
	e, ok := index()[id]
	return e, ok
}

// CanonicalIDs devolve os IDs dos cinco runbooks canónicos de AOS-106 (RB-01..RB-05).
func CanonicalIDs() []string {
	out := make([]string, len(canonicalIDs))
	copy(out, canonicalIDs)
	return out
}

// ErrRegistry sinaliza uma incoerência do registo (órfão, canónico em falta, ligação
// bidireccional divergente).
var ErrRegistry = fmt.Errorf("runbooks: registo de runbooks inconsistente")

// ValidateAgainstAlerts é o GATE FAIL-CLOSED de não-órfãos (verificado no CI). Cruza o
// registo com a config de alertas de AOS-105 e devolve nil sse:
//
//	(a) TODO Route.Runbook de TODA a regra resolve para uma entrada do registo;
//	(b) CADA runbook canónico RB-01..RB-05 está no registo com doc + ADR;
//	(c) a ligação bidireccional é coerente — um canónico com alerta tem o seu alerta na
//	    config a encaminhar para ele; um canónico SEM alerta (RB-02) justifica-o e NÃO
//	    é referenciado por nenhum alerta.
//
// Remover um runbook, apontar um alerta para um ID inexistente, ou desligar o alerta de
// um canónico faz esta validação (e o teste de CI) FALHAR.
func ValidateAgainstAlerts(cfg otelgenai.OperationalAlertConfig) error {
	idx := index()

	// (a) NÃO-ÓRFÃOS: todo o runbook referenciado por um alerta tem entrada. Constrói-se
	// também o mapa reverso runbook→alertas para a checagem bidireccional (c).
	referencedBy := make(map[string][]string)
	for _, r := range cfg.Rules {
		rb := r.Route.Runbook
		if rb == "" {
			// AOS-105 já proíbe alertas sem runbook; defensivo (não confiar cegamente).
			return fmt.Errorf("%w: alerta %q sem runbook (órfão)", ErrRegistry, r.Name)
		}
		if _, ok := idx[rb]; !ok {
			return fmt.Errorf("%w: alerta %q referencia runbook %q SEM entrada no registo (órfão)",
				ErrRegistry, r.Name, rb)
		}
		referencedBy[rb] = append(referencedBy[rb], r.Name)
	}

	// (b) COBERTURA dos canónicos: RB-01..RB-05 presentes, com estrutura (doc + ADR).
	for _, id := range canonicalIDs {
		e, ok := idx[id]
		if !ok {
			return fmt.Errorf("%w: runbook canónico %q em falta no registo", ErrRegistry, id)
		}
		if e.Kind != KindCanonical {
			return fmt.Errorf("%w: runbook %q devia ser canónico, é %q", ErrRegistry, id, e.Kind)
		}
		if e.DocPath == "" {
			return fmt.Errorf("%w: runbook canónico %q sem doc estruturado (DocPath vazio)", ErrRegistry, id)
		}
		if e.ADR == "" {
			return fmt.Errorf("%w: runbook canónico %q sem ADR de conformidade", ErrRegistry, id)
		}
	}

	// (c) LIGAÇÃO BIDIRECCIONAL coerente para cada entrada canónica.
	for _, e := range registry {
		if e.Kind != KindCanonical {
			continue
		}
		if e.Alert == "" {
			// Canónico SEM alerta (RB-02): tem de se justificar E não pode ser
			// referenciado por nenhum alerta (senão a justificação é falsa).
			if e.NoAlertReason == "" {
				return fmt.Errorf("%w: runbook %q sem alerta e sem justificação (NoAlertReason)", ErrRegistry, e.ID)
			}
			if names := referencedBy[e.ID]; len(names) > 0 {
				return fmt.Errorf("%w: runbook %q declara-se sem alerta mas é referenciado por %v", ErrRegistry, e.ID, names)
			}
			continue
		}
		// Canónico COM alerta: o alerta declarado tem de existir na config E encaminhar
		// para este runbook (bidireccional — nem alerta a apontar para outro runbook, nem
		// runbook a reclamar um alerta que não o encaminha).
		names := referencedBy[e.ID]
		found := false
		for _, n := range names {
			if n == e.Alert {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: runbook %q declara alerta %q mas nenhuma regra com esse nome encaminha para %q (regras que encaminham: %v)",
				ErrRegistry, e.ID, e.Alert, e.ID, names)
		}
	}

	return nil
}

// Validate corre o gate de não-órfãos contra a config de alertas de REFERÊNCIA de
// AOS-105 ([otelgenai.DefaultOperationalAlertConfig]). É o atalho que o CI usa.
func Validate() error {
	return ValidateAgainstAlerts(otelgenai.DefaultOperationalAlertConfig())
}
