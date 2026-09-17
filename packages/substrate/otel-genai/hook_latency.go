package otelgenai

// hook_latency.go — A LATÊNCIA DE CADA HOOK DA CADEIA DE POLÍTICA, MEDIDA SEM SLO (AOS-405).
//
// O SLI de overhead de mediação governa a janela da política inteira (AOS-401). O AOS-404 viu em
// produção políticas de 17 a 104 ms e, pelo carimbo do selo de revalidação, pôs todo o excesso no
// troço que vem depois dele: o fsync desse selo, risk-classify, PDP, taint, scope, budget e egress.
// Os selos guardados não separam esse troço, e decidir entre corrigir um hook e recalibrar o SLO
// sem saber qual deles pesa seria voltar a inferir. Esta medida separa-o.
//
// NÃO é um SLI: não tem alvo, não viola, não alerta — como a escrita do selo (AOS-402). Chega ao
// `/metrics` do nó pela mesma janela e pelos mesmos wide events do avaliador de SLOs.

import (
	"sort"
	"strings"
)

// HookLatencyObservation é a latência de um hook da cadeia de política numa janela. Durações em
// nanossegundos, como a política e a escrita do selo, para se lerem lado a lado.
type HookLatencyObservation struct {
	// Hook é o nome do hook tal como o Reference Monitor o anotou (ver [MediationHookLatencyAttr]).
	Hook string
	// Samples é o número de mediações da janela em que o hook correu. Numa recusa os hooks depois
	// do que recusou não correm e não contam — daí os hooks do fim da cadeia poderem ter menos.
	Samples int
	P50     int64
	P95     int64
	Max     int64
}

// MediationHookLatencyAttr devolve o nome do atributo de span que carrega a latência do hook
// `hook`. O nome vem do código (Hook.Name()), mas um hook pode ter o nome configurado; tudo o que
// não for letra, dígito, `-` ou `_` passa a `_`, para o atributo e o rótulo do `/metrics` serem
// sempre legíveis. Um nome vazio passa a `_`.
func MediationHookLatencyAttr(hook string) string {
	return AttrMediationHookLatencyPrefix + nomeDeHookSeguro(hook)
}

func nomeDeHookSeguro(hook string) string {
	if hook == "" {
		return "_"
	}
	var b strings.Builder
	b.Grow(len(hook))
	for _, r := range hook {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// MediationHookLatency deriva, dos wide events de uma janela, a latência de cada hook da cadeia de
// política. Devolve uma observação por hook visto na janela, por ordem de nome; sem hooks medidos
// devolve nil.
//
// A amostra é a do SLI de overhead: spans `execute_tool` que decidiram. Exclui, como a escrita do
// selo, a recusa por contexto cancelado (`denied_by=context`), que sai antes de correr qualquer
// hook. Ao contrário da escrita do selo não se parte por decisão: o custo de um hook é o mesmo
// quer a cadeia acabe em permit quer em recusa, e partir tornaria as amostras poucas demais.
func MediationHookLatency(events []WideEvent) []HookLatencyObservation {
	porHook := map[string][]int64{}
	for _, e := range events {
		if e.Operation != OpExecuteTool || e.Decision == "" || e.DeniedBy == DeniedByContext {
			continue
		}
		for k, v := range e.Attributes {
			hook, ok := strings.CutPrefix(k, AttrMediationHookLatencyPrefix)
			if !ok || hook == "" {
				continue
			}
			// Volta a sanitizar: o Reference Monitor já o faz, mas a chave pode vir de outro produtor
			// na mesma torneira, e o nome vai para um rótulo do `/metrics` — um tab ou um byte que não
			// seja UTF-8 partiria o formato de exposição e, com ele, todas as séries do nó.
			hook = nomeDeHookSeguro(hook)
			n, ok := attrInt64(v)
			if !ok {
				continue
			}
			porHook[hook] = append(porHook[hook], n)
		}
	}
	if len(porHook) == 0 {
		return nil
	}
	hooks := make([]string, 0, len(porHook))
	for h := range porHook {
		hooks = append(hooks, h)
	}
	sort.Strings(hooks)
	out := make([]HookLatencyObservation, 0, len(hooks))
	for _, h := range hooks {
		lat := porHook[h]
		out = append(out, HookLatencyObservation{
			Hook:    h,
			Samples: len(lat),
			P50:     percentileNanos(lat, 50),
			P95:     percentileNanos(lat, 95),
			Max:     maxNanos(lat),
		})
	}
	return out
}
