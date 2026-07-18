package pdp

import (
	"fmt"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
)

// ObligationRegion é o tipo da obrigação de SOBERANIA DE DADOS emitida pelo PDP
// (AOS-094): o efeito só pode ocorrer dentro da região autorizada do board
// (Params["region"]). Espelha [referencemonitor.ObligationRegion] — o PEP de
// AOS-087 já a sabe impor ([referencemonitor.enforceRegion]): uma call cujo
// recurso-alvo esteja fora dessa região (cross-border) é NEGADA antes do dispatch.
// Manter este valor idêntico ao do RM é a costura da cadeia PDP-emite → PEP-impõe.
const ObligationRegion = "region"

// WithBoardRegions liga o registo GOV board→região autorizada (AOS-094) que o PDP
// consulta em CADA decisão de base permit para EMITIR a obrigação `region` a partir
// do board do escopo de identidade — a cadeia PDP-emite → PEP-impõe de AOS-087. Sem
// esta opção (ou com um registo nil) a soberania por board é inerte e o PDP decide
// como antes (comportamento idêntico — todos os testes de base mantêm-se). A
// soberania só TIGHTENS (deny fail-closed de board desconhecido, ou obrigação de
// região adicional), nunca afrouxa um permit.
func WithBoardRegions(r *govsov.Registry) Option {
	return func(p *PDP) { p.boardRegions = r }
}

// applySovereignty compõe a SOBERANIA POR BOARD (AOS-094) sobre uma decisão de BASE
// permit: quando um registo board→região está ligado ([WithBoardRegions]), resolve o
// board do principal (codificado no escopo de identidade) para a sua região
// autorizada e ANEXA a obrigação `region` — que o PEP (AOS-087) impõe, recusando
// qualquer efeito/roteamento cross-border (incluindo failover).
//
// FAIL-CLOSED (ADR-011). A soberania só TIGHTENS uma decisão, nunca a afrouxa:
//   - registo não ligado ⇒ inerte (o PDP decide como antes; soberania é opt-in);
//   - board VAZIO no escopo com registo ligado ⇒ DENY (uma decisão sujeita a
//     soberania sem board não tem fronteira resolvível — nunca permitir por omissão);
//   - board DESCONHECIDO ao registo ⇒ DENY (região desconhecida ⇒ deny; nunca
//     cross-border por omissão);
//   - board conhecido ⇒ permit + obrigação `region`=<região do board>.
//
// Aplica-se APENAS ao caminho de base permit (o chamador nunca a invoca sobre um
// deny/escalate): não transforma um deny em permit.
func (p *PDP) applySovereignty(in Input, base Decision) Decision {
	if p.boardRegions == nil {
		return base // soberania não configurada: comportamento idêntico ao anterior
	}
	region, ok := p.boardRegions.RegionFor(in.Principal.Board)
	if !ok {
		// Board vazio ou desconhecido ⇒ fronteira de soberania não resolvível: deny
		// fail-closed. A razão nomeia o board (identificador de governação, não PII).
		return Decision{
			Effect: Deny,
			Reason: fmt.Sprintf("sovereignty: board %q sem regiao autorizada no registo GOV: negado fail-closed (ADR-011)",
				in.Principal.Board),
			PolicyVersion: base.PolicyVersion,
		}
	}
	// Permit dentro da fronteira: anexa a obrigação de região que o PEP impõe. A
	// obrigação é aditiva às restantes (redact_pii/audit/ttl) — a ordem determinista
	// mantém `region` no fim para goldens estáveis.
	base.Obligations = append(base.Obligations, Obligation{
		Type:   ObligationRegion,
		Params: map[string]string{"region": region},
	})
	base.Reason = fmt.Sprintf("%s; soberania: board %q → regiao %q (obrigacao region)",
		base.Reason, in.Principal.Board, region)
	return base
}

// SovereigntyRegistry devolve o registo board→região ligado (nil se a soberania não
// está configurada). Exposto para composição/observabilidade (ex. o Model Gateway
// derivar a região autorizada de um board da MESMA fonte de verdade GOV).
func (p *PDP) SovereigntyRegistry() *govsov.Registry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.boardRegions
}
