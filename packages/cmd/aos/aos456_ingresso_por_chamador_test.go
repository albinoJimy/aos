package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// relogioManual é um relógio injectável. Sem ele um teste de token-bucket mede o tempo real e
// mede-se a si próprio — a disciplina do `testkit` aplicada localmente (AGENTS.md §11).
type relogioManual struct{ t time.Time }

func (r *relogioManual) agora() time.Time       { return r.t }
func (r *relogioManual) avanca(d time.Duration) { r.t = r.t.Add(d) }

func novoRelogio() *relogioManual {
	return &relogioManual{t: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
}

// TestAOS456RajadaDeUmNaoAtingeOOutro é o CRITÉRIO DE ACEITAÇÃO do ticket, e o teste que não
// existia — a razão de o defeito ter sobrevivido ao AOS-277.
func TestAOS456RajadaDeUmNaoAtingeOOutro(t *testing.T) {
	rel := novoRelogio()
	// Dotação de 3, sem reabastecimento no intervalo do teste (o relógio não avança).
	tab := newBaldesPorChamador(16, 3, 0.001, rel.agora)

	// A esgota o SEU balde.
	for i := 0; i < 3; i++ {
		if !tab.allow("human:alice") {
			t.Fatalf("alice recusada no pedido %d, dentro da sua dotacao de 3", i+1)
		}
	}
	if tab.allow("human:alice") {
		t.Fatal("alice passou o 4.o pedido: o balde por-chamador nao esta a morder")
	}

	// B, intacto. É isto que o balde global NÃO dá, e é o ticket todo.
	for i := 0; i < 3; i++ {
		if !tab.allow("human:bob") {
			t.Fatalf("bob recusado no pedido %d POR CAUSA da rajada de alice — a starvation entre "+
				"pares que o AOS-456 existe para fechar", i+1)
		}
	}
}

// TestAOS456TabelaRespeitaOTectoEEvictaOMaisCheio prova que a tabela não é ela própria o vector,
// e que a política de evicção é a declarada.
func TestAOS456TabelaRespeitaOTectoEEvictaOMaisCheio(t *testing.T) {
	rel := novoRelogio()
	tab := newBaldesPorChamador(3, 5, 0.001, rel.agora)

	// `drenado` gasta 4 dos 5 tokens: fica o MAIS VAZIO, logo NUNCA deve ser a vítima.
	for i := 0; i < 4; i++ {
		tab.allow("human:drenado")
	}
	// Dois cheios-menos-um.
	tab.allow("human:cheio-a")
	tab.allow("human:cheio-b")

	if got := tab.tamanho(); got != 3 {
		t.Fatalf("tabela com %d entradas, esperava 3 (o tecto)", got)
	}

	// O quarto principal força evicção. Com tecto 3 o lote é max(1, 3/8) = 1.
	tab.allow("human:novo")
	if got := tab.tamanho(); got > 3 {
		t.Fatalf("tabela cresceu para %d ACIMA do tecto de 3 — o map sem tecto e o vector que "+
			"este ficheiro existe para fechar", got)
	}
	if tab.evictados() != 1 {
		t.Fatalf("evicções = %d, esperava 1 (lote = max(1, tecto/8) = 1 com tecto 3)", tab.evictados())
	}

	// O drenado SOBREVIVEU: evictá-lo dar-lhe-ia dotação fresca, que é o que um atacante
	// quereria. Se este ramo cair, a política de evicção passou a ser explorável.
	if !tab.temPrincipal("human:drenado") {
		t.Fatal("o balde MAIS VAZIO foi evictado — isso DA dotacao fresca a quem inundou, e torna " +
			"a eviccao explorável pelo atacante (ele rodaria principais para reiniciar o seu balde)")
	}
}

// TestAOS456ReceiverNilNaoPermiteNada — a barreira não-composta não se comporta como barreira
// aberta. O chamador é obrigado a decidir explicitamente.
func TestAOS456ReceiverNilNaoPermiteNada(t *testing.T) {
	var tab *baldesPorChamador // NÃO COMPOSTA
	if tab.allow("human:alice") {
		t.Fatal("tabela nil PERMITIU — um nil que permite e uma barreira ausente que parece aberta")
	}
	if newBaldesPorChamador(0, 1, 1, nil) != nil {
		t.Fatal("tecto 0 devia dar tabela nil (etapa nao composta), nao uma tabela sem tecto")
	}
}

// TestAOS456PrincipalVazioNaoPartilhaBalde — sem principal não há atribuição, e juntar os
// não-identificados num balde "" seria um balde partilhado a fingir-se por-chamador.
func TestAOS456PrincipalVazioNaoPartilhaBalde(t *testing.T) {
	rel := novoRelogio()
	tab := newBaldesPorChamador(8, 5, 0.001, rel.agora)
	if tab.allow("") {
		t.Fatal("principal vazio foi ADMITIDO no balde por-chamador")
	}
	if tab.tamanho() != 0 {
		t.Fatalf("um principal vazio criou %d entrada(s) na tabela", tab.tamanho())
	}
}

// TestAOS456BaldeReabasteceComORelogio — controlo de que o balde é um balde e não um contador:
// sem esta prova, um "limite" que nunca reabastece passaria os testes acima e fecharia o ingresso
// para sempre a quem o esgotasse uma vez.
func TestAOS456BaldeReabasteceComORelogio(t *testing.T) {
	rel := novoRelogio()
	tab := newBaldesPorChamador(8, 2, 1, rel.agora) // 2 de burst, 1 token/segundo
	tab.allow("human:alice")
	tab.allow("human:alice")
	if tab.allow("human:alice") {
		t.Fatal("passou o 3.o pedido sem tempo decorrido")
	}
	rel.avanca(1100 * time.Millisecond)
	if !tab.allow("human:alice") {
		t.Fatal("NAO reabasteceu depois de 1,1 s a 1 token/s — e um contador, nao um balde")
	}
}

// TestAOS456BannerDeclaraAPosturaVERDADEIRA — o banner é a única coisa que um operador lê no
// arranque. As três posturas têm de ser distinguíveis, e a do meio é a que interessa: com a
// dotação por-chamador IGUAL à global a etapa está ligada e NÃO dá justiça, e dizer o contrário
// seria anunciar protecção inexistente.
func TestAOS456BannerDeclaraAPosturaVERDADEIRA(t *testing.T) {
	casos := []struct {
		nome   string
		lim    ingressLimits
		exige  []string
		proibe []string
	}{
		{
			nome:   "nao composta",
			lim:    ingressLimits{ratePerSec: 10, burst: 20, maxInFlight: 5, perCallerMax: 0},
			exige:  []string{"NAO COMPOSTA"},
			proibe: []string{"PROTEGE um chamador"},
		},
		{
			nome: "ligada mas SEM justica (dotacao igual a global)",
			lim: ingressLimits{ratePerSec: 10, burst: 20, maxInFlight: 5,
				perCallerMax: 4096, perCallerRate: 10, perCallerBurst: 20},
			exige:  []string{"LIGADA", "NAO protege um chamador da rajada de outro"},
			proibe: []string{"PROTEGE um chamador da rajada de outro"},
		},
		{
			nome: "ligada COM justica (dotacao menor)",
			lim: ingressLimits{ratePerSec: 10, burst: 20, maxInFlight: 5,
				perCallerMax: 4096, perCallerRate: 2, perCallerBurst: 4},
			exige:  []string{"LIGADA", "PROTEGE um chamador da rajada de outro"},
			proibe: []string{"NAO protege"},
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			txt := strings.Join(ingressPostureBanner(c.lim), "\n")
			for _, e := range c.exige {
				if !strings.Contains(txt, e) {
					t.Errorf("banner NAO declara %q\n--- banner ---\n%s", e, txt)
				}
			}
			for _, pr := range c.proibe {
				if strings.Contains(txt, pr) {
					t.Errorf("banner declara %q, que e FALSO nesta postura\n--- banner ---\n%s", pr, txt)
				}
			}
		})
	}

	// CONTROLO DE NÃO-VACUIDADE: a frase antiga afirmava o defeito. Se ela sobreviver, o banner
	// mente na postura ligada.
	ligado := strings.Join(ingressPostureBanner(ingressLimits{
		ratePerSec: 10, burst: 20, maxInFlight: 5,
		perCallerMax: 4096, perCallerRate: 2, perCallerBurst: 4,
	}), "\n")
	if strings.Contains(ligado, "NAO e por-IP nem por-principal") {
		t.Error("o banner ainda afirma que o limite NAO e por-principal, com a 2.a etapa LIGADA")
	}
}

// TestAOS456EnvFailClosed — um valor ilegível, negativo ou ZERO aborta o arranque. `0` merece a
// nota: não desliga a etapa, tornaria a tabela sem tecto se fosse aceite pelo construtor.
func TestAOS456EnvFailClosed(t *testing.T) {
	for _, v := range []string{"0", "-1", "abc", "1.5", ""} {
		nome := v
		if nome == "" {
			nome = "(vazio)"
		}
		t.Run("AOS_INGRESS_PER_CALLER_MAX="+nome, func(t *testing.T) {
			t.Setenv("AOS_INGRESS_PER_CALLER_MAX", v)
			lim, _, err := ingressLimitsFromEnv()
			if v == "" {
				if err != nil {
					t.Fatalf("variavel VAZIA devia manter o default, deu erro: %v", err)
				}
				if lim.perCallerMax != DefaultPerCallerMax {
					t.Fatalf("default = %d, esperava %d", lim.perCallerMax, DefaultPerCallerMax)
				}
				return
			}
			if err == nil {
				t.Fatalf("valor %q foi ACEITO — devia abortar o arranque (fail-closed na config)", v)
			}
		})
	}
}

// TestAOS456CadaBaldeEIndependente — controlo de que a tabela não é um balde só com um nome por
// cima: N principais, cada um com a dotação inteira.
func TestAOS456CadaBaldeEIndependente(t *testing.T) {
	rel := novoRelogio()
	tab := newBaldesPorChamador(64, 2, 0.001, rel.agora)
	for i := 0; i < 20; i++ {
		p := fmt.Sprintf("human:u%02d", i)
		// As duas chamadas são SEQUENCIAIS e cada uma consome um token. Escritas em `||` liam-se
		// como expressão duplicada (staticcheck SA4000, e com razão: o short-circuit tornaria a
		// segunda condicional e o teste ambíguo).
		for n := 1; n <= 2; n++ {
			if !tab.allow(p) {
				t.Fatalf("%s recusado no pedido %d, dentro da dotacao de 2", p, n)
			}
		}
		if tab.allow(p) {
			t.Fatalf("%s passou o 3.o pedido", p)
		}
	}
}

// TestAOS456EviccaoAmortizaEmLote — a varredura de evicção é O(n) sobre a tabela e corre com o
// mutex GLOBAL tomado, no caminho quente de `POST /runs`. Evictar um por inserção faria um
// atacante que rode principais pagar essa varredura a CADA pedido e, com o lock global,
// serializar todas as submissões atrás dela: o mecanismo que impede um chamador de esfomear os
// outros tornar-se-ia a via para esfomear todos.
//
// Este teste é o sensor dessa propriedade. Sem ele, «é em lote» é uma afirmação sobre o código e
// não sobre o comportamento.
func TestAOS456EviccaoAmortizaEmLote(t *testing.T) {
	rel := novoRelogio()
	const tecto = 64
	lote := tecto / 8 // 8
	tab := newBaldesPorChamador(tecto, 5, 0.001, rel.agora)

	// Enche até ao tecto — sem evicção nenhuma.
	for i := 0; i < tecto; i++ {
		tab.allow(fmt.Sprintf("human:base%03d", i))
	}
	if tab.varreduraDeEviccao() != 0 {
		t.Fatalf("houve %d varredura(s) a ENCHER a tabela, esperava 0", tab.varreduraDeEviccao())
	}

	// `lote` principais novos têm de custar UMA varredura, não `lote`.
	for i := 0; i < lote; i++ {
		tab.allow(fmt.Sprintf("human:novo%03d", i))
	}
	if got := tab.varreduraDeEviccao(); got != 1 {
		t.Fatalf("%d principais novos custaram %d varredura(s), esperava 1 — a evicção NAO esta a "+
			"amortizar, e um atacante que rode principais paga (e impoe) uma varredura O(n) com o "+
			"mutex global tomado a cada pedido", lote, got)
	}
	if got := tab.tamanho(); got > tecto {
		t.Fatalf("tabela em %d, acima do tecto %d", got, tecto)
	}
}
