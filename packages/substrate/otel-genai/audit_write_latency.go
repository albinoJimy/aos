package otelgenai

// audit_write_latency.go — A ESCRITA DO SELO DE MEDIAÇÃO, MEDIDA SEM SLO (AOS-402).
//
// Desde AOS-401 o Reference Monitor publica no span `execute_tool` a duração da escrita do selo
// `tool.call.mediated` no sink durável ([AttrMediationAuditWriteLatencyNanos]), e o SLO de
// overhead deixou de a contar. Mas a medida só existia no span, e o colector OTel de produção
// exporta os traces para `debug`, que os descarta: a escrita continuava a ser inferida, que é o
// que o AOS-401 quis deixar de fazer. Este cálculo leva-a ao único sítio legível em produção, o
// `/metrics` do nó, pela mesma janela e pelos mesmos wide events do avaliador de SLOs.
//
// NÃO é um SLI: não tem alvo, não viola, não alerta. Nenhum orçamento foi ratificado para o custo
// de um sink durável (ADR-026, Emenda §3). É por isso que tem tipo próprio, fora do catálogo.

import "sort"

// AuditWriteLatency é a observação da escrita do selo de mediação numa janela, para uma decisão.
// Durações em nanossegundos, como o `latency_ns` do selo e o SLI de overhead, para que as duas
// metades da decisão se comparem sem conversões.
type AuditWriteLatency struct {
	// Decision é o efeito da mediação: permit, deny ou escalate. Só a escrita de um permit está
	// no caminho crítico (o efeito espera por ela); a de uma recusa é a prova escrita depois de a
	// decisão estar fixada. EXCEPÇÃO que importa ao operador: quando o selo do permit FALHA, a
	// decisão degrada para deny (`denied_by=audit-sink`) e a medida soma a escrita falhada e a
	// nova tentativa — um sink em avaria aparece em `deny`, não em `permit`.
	Decision string
	// Samples é o número de mediações da janela com a medida. Zero ⇒ P50/P95/Max são zero e não
	// significam nada.
	Samples int
	P50     int64
	P95     int64
	Max     int64
}

// decisoesComEscrita são as decisões que o Reference Monitor sela, na ordem em que se publicam.
var decisoesComEscrita = []string{DecisionPermit, DecisionDeny, DecisionEscalate}

// MediationAuditWriteLatency deriva, dos wide events de uma janela, a escrita do selo de mediação
// por decisão. Devolve sempre as três decisões, pela ordem fixa, com Samples a zero quando não há
// medida — quem publica decide o que mostrar.
//
// A amostra é a do SLI de overhead ([overheadP95SLI]): spans `execute_tool` que decidiram e trazem
// a medida. Duas exclusões próprias:
//   - span sem o atributo (Reference Monitor anterior ao AOS-401): fica fora, sem fallback para
//     outra janela — um zero herdado seria uma escrita que nunca foi medida;
//   - recusa por contexto cancelado (`denied_by=context`): o Reference Monitor sai antes de
//     escrever o selo e publica zero, que puxaria os percentis para baixo sem ser uma escrita.
func MediationAuditWriteLatency(events []WideEvent) []AuditWriteLatency {
	porDecisao := make(map[string][]int64, len(decisoesComEscrita))
	for _, e := range events {
		if e.Operation != OpExecuteTool || e.Decision == "" {
			continue
		}
		if e.DeniedBy == DeniedByContext {
			continue
		}
		d, ok := mediationAuditWriteLatencyOf(e)
		if !ok {
			continue
		}
		porDecisao[e.Decision] = append(porDecisao[e.Decision], d)
	}

	out := make([]AuditWriteLatency, 0, len(decisoesComEscrita))
	for _, dec := range decisoesComEscrita {
		lat := porDecisao[dec]
		obs := AuditWriteLatency{Decision: dec, Samples: len(lat)}
		if len(lat) > 0 {
			obs.P50 = percentileNanos(lat, 50)
			obs.P95 = percentileNanos(lat, 95)
			obs.Max = maxNanos(lat)
		}
		out = append(out, obs)
	}
	return out
}

// mediationAuditWriteLatencyOf devolve a escrita do selo do evento e se ela EXISTE, com a mesma
// regra de [mediationPolicyLatencyOf]: o bag do span é a fonte; o campo tipado serve um
// [WideEvent] construído directamente.
func mediationAuditWriteLatencyOf(e WideEvent) (int64, bool) {
	if _, ok := e.Attributes[AttrMediationAuditWriteLatencyNanos]; ok {
		return attrInt64Bag(e.Attributes, AttrMediationAuditWriteLatencyNanos), true
	}
	if e.MediationAuditWriteLatencyNanos != 0 {
		return e.MediationAuditWriteLatencyNanos, true
	}
	return 0, false
}

func maxNanos(xs []int64) int64 {
	s := make([]int64, len(xs))
	copy(s, xs)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)-1]
}
