// Package sovereignty é o mapa de GOVERNAÇÃO board→região autorizada (AOS-094,
// tecnica/09 §6, ADR-011). Codifica a FRONTEIRA REGIONAL de cada board (a região
// de soberania onde os dados desse board podem residir) e resolve-a FAIL-CLOSED —
// um board desconhecido, ou sem região, NÃO tem região autorizada (nunca "qualquer
// região por omissão").
//
// # Papel na cadeia de soberania (PDP→PEP)
//
// A soberania de dados é imposta por board. O escopo de identidade da NHI codifica
// o board (EPIC-01); este registo resolve board→região; o PDP anexa a região
// resolvida como OBRIGAÇÃO `region` a um permit (AOS-094/AC2); o PEP (AOS-087,
// [referencemonitor.enforceRegion]) RECUSA fail-closed qualquer efeito cujo
// recurso-alvo esteja fora dessa região — incluindo um roteamento/failover
// cross-border. Este pacote é a AUTORIDADE do primeiro elo (board→região); NÃO
// reimplementa o enforcement do PEP nem a guarda de failover do Model Gateway
// (EPIC-06) — apenas fornece, de forma determinista e fail-closed, a região que
// esses mecanismos impõem.
//
// # Fail-closed (invariante central)
//
// Um board vazio, um board não registado, ou uma região vazia NUNCA resolvem para
// uma região autorizada. É a postura "região desconhecida ⇒ deny" de ADR-011: a
// ausência de mapeamento explícito é recusa, nunca permissão cross-border por
// omissão. O registo é IMUTÁVEL após construção (a cópia defensiva impede mutação
// externa do mapa) — determinista e seguro para uso concorrente (só leitura).
package sovereignty

import "strings"

// Registry é o mapa imutável board→região autorizada. Construir com [NewRegistry].
// Um Registry nil é válido e fail-closed (resolve tudo para "não autorizado"): um
// PDP sem soberania configurada não inventa regiões.
type Registry struct {
	// byBoard mapeia o board (normalizado) → região autorizada (normalizada,
	// não-vazia). Boards ou regiões vazias são descartadas na construção.
	byBoard map[string]string
}

// normalize devolve a forma canónica de um board/região para comparação estável:
// espaços aparados e caixa reduzida (a soberania é case-insensitive, à imagem de
// [referencemonitor.enforceRegion], que compara com EqualFold).
func normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// NewRegistry constrói o registo a partir de um mapa board→região. Entradas com
// board OU região vazios (após aparar) são DESCARTADAS — nunca criam uma fronteira
// indefinida que colidisse com o fail-closed. As chaves e valores são normalizados
// (aparados + caixa reduzida) para uma resolução estável. O mapa de origem é
// copiado (imutabilidade): mutações externas posteriores não afectam o registo.
func NewRegistry(boardRegions map[string]string) *Registry {
	m := make(map[string]string, len(boardRegions))
	for board, region := range boardRegions {
		b, r := normalize(board), normalize(region)
		if b == "" || r == "" {
			continue
		}
		m[b] = r
	}
	return &Registry{byBoard: m}
}

// RegionFor resolve a região autorizada de um board. Devolve (região, true) só
// quando o board está registado com uma região não-vazia; caso contrário
// ("", false) — fail-closed. Um Registry nil, um board vazio ou um board
// desconhecido resolvem sempre para ("", false): a ausência de mapeamento é recusa,
// nunca uma região por omissão.
func (r *Registry) RegionFor(board string) (string, bool) {
	if r == nil {
		return "", false
	}
	region, ok := r.byBoard[normalize(board)]
	return region, ok
}

// Authorized reporta se uma dada região é a região autorizada do board — a
// verificação de fronteira que o Model Gateway (allowlist regional, EPIC-06) e o
// PEP exprimem. Fail-closed: um board sem região autorizada, ou uma região que não
// coincide (cross-border), devolvem false. A comparação é case-insensitive
// (normalizada), coerente com o enforcement do PEP.
func (r *Registry) Authorized(board, region string) bool {
	want, ok := r.RegionFor(board)
	if !ok {
		return false
	}
	return want == normalize(region)
}

// Boards devolve o número de boards registados (observabilidade/testes). Um
// Registry nil tem zero boards.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.byBoard)
}
