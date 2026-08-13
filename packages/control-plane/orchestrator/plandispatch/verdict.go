package plandispatch

import (
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// verdict.go — A LIGAÇÃO entre o veredicto EMITIDO (ADR-022 §2.2, AOS-271) e o
// veredicto CONSUMIDO por um ramo de qualidade (§2.1, AOS-270).
//
// AOS-270 deixou a ponta solta: a gramática admitia `subject: verdict`, o avaliador
// sabia compará-lo — e nada no repositório PRODUZIA um. Um ramo de qualidade avaliava
// «ausente ⇒ falso» e podava-se a si próprio em silêncio (defeito corrigido em
// condition.go: a ausência é agora INDECISÃO, nunca falsidade). Este ficheiro é a outra
// metade da ponta a ser atada, e é deliberadamente MINÚSCULO: uma projecção pura do facto
// apenso para o retrato imutável que [ResultView] devolve. Não há segundo formato de
// veredicto, não há conversão de símbolos (o alfabeto é o mesmo nas três pontas — ver
// [VerdictValue]), não há política escondida.
//
// PORQUE UMA PROJECÇÃO E NÃO UMA PORTA NOVA. A [ResultView] já é a fronteira de
// leitura do resultado registado; o que faltava não era uma superfície, era a
// FUNÇÃO que diz como um `plan.verdict_recorded` se lê como resultado. O wiring que
// implementa a [ResultView] sobre o Event Store chama isto e devolve o registo — o
// despachante continua sem saber que existe um Event Store, que é a fronteira
// ADR-018 intacta.

// ResultFromVerdict projecta o facto `plan.verdict_recorded` de UM verificador no
// [NodeResultRecord] que as condições de qualidade observam.
//
// O que atravessa: a disposição (pass/fail), as MÉTRICAS declaradas indexadas por nome
// — os dois observáveis que um predicado pode ler de um veredicto — e os SUJEITOS.
//
// Os sujeitos NÃO são um observável (nenhum predicado os lê): são a ATRIBUIÇÃO, e
// atravessam porque o despachante tem de poder recusar um ramo cujo veredicto examinou
// outro trabalho que não o que a aresta guarda. Descartá-los — como esta projecção
// fazia — deixava a atribuição construída na admissão e validada na emissão sem
// qualquer efeito no caminho quente.
//
// O que NÃO atravessa: as RAZÕES. São códigos de diagnóstico para o audit e para o
// approval-card do gate, não material de ramificação — se um ramo pudesse condicionar
// sobre a presença de um código, a gramática fechada de §2.1 ganhava um quarto
// observável por acidente, sem passar pelo schema. O `terminal_state` também não vem
// daqui: é do ciclo de vida do nó (AOS-017), não do veredicto, e misturá-los deixaria
// um verificador declarar o seu próprio estado terminal.
//
// Um veredicto com métricas de nome repetido é impossível a jusante de
// [plannerevents.NewVerdictRecorded]; se ainda assim chegasse, a ÚLTIMA ocorrência
// vence — determinístico (a ordem do slice é a ordem do facto), nunca uma escolha
// dependente de iteração de mapa. Pura: sem I/O, sem relógio.
func ResultFromVerdict(p plannerevents.VerdictRecordedPayload) NodeResultRecord {
	rec := NodeResultRecord{Verdict: VerdictValue(p.Outcome)}
	if len(p.Subjects) > 0 {
		rec.Subjects = append([]string(nil), p.Subjects...)
	}
	if len(p.Metrics) > 0 {
		rec.Metrics = make(map[string]int64, len(p.Metrics))
		for _, m := range p.Metrics {
			rec.Metrics[m.Name] = m.Value
		}
	}
	return rec
}
