package pdp

import (
	"fmt"
	"reflect"

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
	return func(p *PDP) { p.boardRegions = resolvedorOuNil(r) }
}

// resolvedorOuNil normaliza qualquer resolvedor para a semântica «nil ⇒ inerte». Um PONTEIRO NIL
// dentro de uma interface não é uma interface nil: passaria a guarda `== nil`, `SovereigntyEnabled`
// diria LIGADA e a primeira decisão de base permit chamaria `RegionFor` num receptor nil. A
// diferença entre «soberania desligada» e «nega tudo» não pode depender de o chamador ter escrito
// `(*Registry)(nil)` em vez de `nil` — daí a normalização ser aqui, no único sítio por onde os dois
// caminhos de ligação (opção e setter) passam, e não em cada um deles.
func resolvedorOuNil(r BoardRegionResolver) BoardRegionResolver {
	if r == nil {
		return nil
	}
	if v := reflect.ValueOf(r); v.Kind() == reflect.Ptr && v.IsNil() {
		return nil
	}
	return r
}

// BoardRegionResolver resolve o board de um principal para a sua região autorizada (AOS-407).
// Satisfazem-no o [govsov.Registry] (uma fotografia) e a autoridade viva do nó, que roda o mapa
// sem reabrir o PDP. ok=false para um board vazio ou desconhecido — o PDP nega fail-closed.
type BoardRegionResolver interface {
	RegionFor(board string) (string, bool)
}

// SetBoardRegions liga o resolvedor board→região DEPOIS de [Open] (AOS-407). É o caminho do nó: o
// PDP abre-se ao ler o ambiente, antes de o WORM e a autoridade de soberania existirem. Toma o lock
// de escrita, como [PDP.SetTracer]; a partir daqui cada decisão lê o resolvedor sob o lock de
// leitura. nil — ou um ponteiro nil dentro da interface, ver [resolvedorOuNil] — desliga a
// soberania por board em vez de negar tudo.
func (p *PDP) SetBoardRegions(r BoardRegionResolver) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.boardRegions = resolvedorOuNil(r)
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
	p.mu.RLock()
	resolvedor := p.boardRegions
	p.mu.RUnlock()
	if resolvedor == nil {
		return base // soberania não configurada: comportamento idêntico ao anterior
	}
	region, ok := resolvedor.RegionFor(in.Principal.Board)
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

// SovereigntyRegistry devolve o resolvedor ligado SE ele for um [govsov.Registry] — a fotografia
// do caminho legado. Exposto para composição/observabilidade (ex. o Model Gateway derivar a região
// autorizada de um board da MESMA fonte de verdade GOV).
//
// NÃO É um predicado de «a soberania está ligada». Desde AOS-407 o nó liga um resolvedor VIVO (a
// [SovereignRegionAuthority], que roda o mapa sem reabrir o PDP): nesse caminho — o de produção —
// esta função devolve nil com a soberania LIGADA. Quem quer saber se está ligada usa
// [PDP.SovereigntyEnabled]; quem usar `SovereigntyRegistry() != nil` lê o contrário da verdade.
func (p *PDP) SovereigntyRegistry() *govsov.Registry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	reg, _ := p.boardRegions.(*govsov.Registry)
	return reg
}

// SovereigntyEnabled diz se há um resolvedor board→região ligado (AOS-407): com ele, cada decisão
// de base permit exige um board resolvível e leva a obrigação `region`.
func (p *PDP) SovereigntyEnabled() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.boardRegions != nil
}
