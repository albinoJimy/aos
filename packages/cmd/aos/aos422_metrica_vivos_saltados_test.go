package main

// AOS-422 — A GUARDA DO AOS-411 PASSA A TER PROVA.
//
// O AOS-411 fez um run VIVO deixar de contar como órfão, e com isso calou a passagem periódica
// que só encontra runs a correr — que era o ruído a corrigir. Mas calou também a única prova de
// que a guarda funciona: a passagem só escreve no log quando encontra órfãos verdadeiros
// (`if anuncia || scanned > 0`), e os contadores eram variáveis locais da função.
//
// O efeito prático foi medido ao tentar verificar o AOS-411 em produção: com um run vivo e
// nenhum órfão, o varredor não escreve nada, e **a correcção tornou a sua própria evidência
// inobservável**. O `aos_orphan_sweeps_total` já diz que o varredor correu; faltava dizer o que
// ele SALTOU.

import (
	"strings"
	"testing"
)

// amostrasDe devolve as linhas da família, para a mensagem de falha dizer o que apareceu.
func amostrasDe(corpo, familia string) string {
	var out []string
	for _, l := range strings.Split(corpo, "\n") {
		if strings.Contains(l, familia) {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return "(a familia nao aparece no /metrics)"
	}
	return strings.Join(out, "\n")
}

// TestAOS422_SerieAusenteAntesDaPrimeiraPassagem é a regra que o bloco dos varredores já
// impõe e que esta métrica tem de respeitar: um `0` num nó que nunca varreu leria-se como
// «varreu e não havia nada», que é a mentira simétrica da que isto vem fechar.
func TestAOS422_SerieAusenteAntesDaPrimeiraPassagem(t *testing.T) {
	h := noComTodasAsFamilias(t)
	h.svc.passagensOrfaos.Store(0)
	h.svc.ultimoOrfaoUnix.Store(0)
	corpo := metricasDe(t, h)

	if strings.Contains(corpo, "aos_orphan_live_skipped_total") {
		t.Fatal("a série apareceu antes da PRIMEIRA passagem do varredor: um zero sem varredura lê-se como «varreu e não havia nada» (AOS-422)")
	}
}

// TestAOS422_ZeroDepoisDaPrimeiraPassagemEUmZeroVerDADEIRO: passada a primeira varredura, o
// zero deixa de ser ausência de dados e passa a ser um facto — «varreu, e não havia run vivo
// nenhum para saltar».
func TestAOS422_ZeroDepoisDaPrimeiraPassagemEUmZeroVerdadeiro(t *testing.T) {
	h := noComTodasAsFamilias(t)
	h.svc.passagensOrfaos.Store(1)
	corpo := metricasDe(t, h)

	if !strings.Contains(corpo, `aos_orphan_live_skipped_total{dono="esta_replica"} 0`) {
		t.Fatalf("faltou o zero verdadeiro de `esta_replica` depois da 1.ª passagem:\n%s", amostrasDe(corpo, "aos_orphan_live_skipped_total"))
	}
	if !strings.Contains(corpo, `aos_orphan_live_skipped_total{dono="outra_replica"} 0`) {
		t.Fatalf("faltou o zero verdadeiro de `outra_replica`:\n%s", amostrasDe(corpo, "aos_orphan_live_skipped_total"))
	}
}

// TestAOS422_OsVivosSaltadosChegamAoMetrics é o teste que dá ao AOS-411 a evidência que lhe
// faltava: um run vivo saltado tem de ser VISÍVEL sem depender de uma linha de log que, no caso
// que interessa, não é escrita.
func TestAOS422_OsVivosSaltadosChegamAoMetrics(t *testing.T) {
	h := noComTodasAsFamilias(t)
	h.svc.passagensOrfaos.Store(3)
	h.svc.vivosSaltadosAqui.Store(2)
	h.svc.vivosSaltadosNoutra.Store(5)
	corpo := metricasDe(t, h)

	if !strings.Contains(corpo, `aos_orphan_live_skipped_total{dono="esta_replica"} 2`) {
		t.Fatalf("os runs vivos saltados NESTA réplica não chegaram ao /metrics:\n%s", amostrasDe(corpo, "aos_orphan_live_skipped_total"))
	}
	if !strings.Contains(corpo, `aos_orphan_live_skipped_total{dono="outra_replica"} 5`) {
		t.Fatalf("os runs vivos com lease noutra réplica não chegaram ao /metrics:\n%s", amostrasDe(corpo, "aos_orphan_live_skipped_total"))
	}
}

// TestAOS422_UmaFamiliaDuasAmostras fixa a forma: `# HELP`/`# TYPE` UMA vez, duas amostras. Dois
// blocos de declaração para o mesmo nome fazem o Prometheus rejeitar o payload INTEIRO — não só
// a família —, e era esse o erro fácil aqui.
func TestAOS422_UmaFamiliaDuasAmostras(t *testing.T) {
	h := noComTodasAsFamilias(t)
	h.svc.passagensOrfaos.Store(1)
	corpo := metricasDe(t, h)

	if n := strings.Count(corpo, "# HELP aos_orphan_live_skipped_total"); n != 1 {
		t.Fatalf("# HELP aparece %d vezes, quero 1 — o Prometheus rejeita o payload inteiro com duplicados (AOS-422)", n)
	}
	if n := strings.Count(corpo, "# TYPE aos_orphan_live_skipped_total"); n != 1 {
		t.Fatalf("# TYPE aparece %d vezes, quero 1", n)
	}
	if n := strings.Count(corpo, "aos_orphan_live_skipped_total{"); n != 2 {
		t.Fatalf("a família tem %d amostras, quero 2 (esta_replica e outra_replica)", n)
	}
}
