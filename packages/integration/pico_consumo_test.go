package integration

import (
	"testing"

	budget "github.com/aos-ref/control-plane/budget"
)

// ---------------------------------------------------------------------------------------------
// A FOLGA DO ORÇAMENTO TEM DE SER MENSURÁVEL.
//
// O README declara há semanas que o tecto está «configurado onde nunca morde» — 200 000 tokens
// contra ~1 750 medidos por run. O mecanismo funciona; nesta configuração é protecção que não
// engata. E isso estava escrito só num parágrafo: o banner declara o tecto em detalhe e lê-se
// como protecção activa.
//
// O pico é a outra metade do rácio. Sem ele, «este tecto chega a apertar?» é uma pergunta que
// ninguém pode responder olhando para o nó.
// ---------------------------------------------------------------------------------------------

func TestPicoRegistaOMaiorConsumoPorRun(t *testing.T) {
	rb, err := NewRunBudget(1000)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	if _, visto := rb.PicoDeConsumo(); visto {
		t.Fatal("sem runs fechados NAO pode haver pico — 0 leria-se como «nada gasta nada»")
	}

	// Run pequeno.
	rel, err := rb.acquire(ctx, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	res, err := rb.tree.Reserve(ctx, "run-a", budget.Amount{Tokens: 120})
	if err != nil {
		t.Fatal(err)
	}
	if err := rb.tree.Commit(ctx, res); err != nil {
		t.Fatal(err)
	}
	rel()

	pico, visto := rb.PicoDeConsumo()
	if !visto || pico != 120 {
		t.Fatalf("pico = %d (visto=%v), queria 120", pico, visto)
	}

	// Run maior: o pico sobe.
	rel2, _ := rb.acquire(ctx, "run-b")
	res2, _ := rb.tree.Reserve(ctx, "run-b", budget.Amount{Tokens: 400})
	_ = rb.tree.Commit(ctx, res2)
	rel2()
	if pico, _ := rb.PicoDeConsumo(); pico != 400 {
		t.Errorf("pico = %d, queria 400 — o maior de todos", pico)
	}

	// Run menor: o pico NAO desce. É um máximo, não o último.
	rel3, _ := rb.acquire(ctx, "run-c")
	res3, _ := rb.tree.Reserve(ctx, "run-c", budget.Amount{Tokens: 50})
	_ = rb.tree.Commit(ctx, res3)
	rel3()
	if pico, _ := rb.PicoDeConsumo(); pico != 400 {
		t.Errorf("pico = %d apos um run menor — o pico e um MAXIMO, nao o ultimo valor", pico)
	}
}

// TestPicoNaoContaUmRunQueNaoGastou é o controlo do valor-zero.
//
// Um run que abre e fecha sem gastar nada TEM de marcar «houve observação» (senão um nó com
// tráfego pareceria um nó que nunca mediu) mas com pico 0 — e a folga daí resultante é infinita,
// o que é a leitura CERTA: nada gastou nada.
func TestPicoNaoContaUmRunQueNaoGastou(t *testing.T) {
	rb, err := NewRunBudget(1000)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := rb.acquire(t.Context(), "run-vazio")
	if err != nil {
		t.Fatal(err)
	}
	rel()
	pico, visto := rb.PicoDeConsumo()
	if !visto {
		t.Error("um run que FECHOU e uma observacao, mesmo com consumo zero")
	}
	if pico != 0 {
		t.Errorf("pico = %d num run que nao gastou nada", pico)
	}
}

// TestPicoIgnoraORUidoDeUmaLeituraNEGATIVA fixa o fail-safe.
//
// `limite - disponivel` só é o consumo se o limite for o desta incarnação. Se alguma vez essa
// invariante se partir (um limite lido de outra fonte, uma reserva libertada fora de ordem), o
// resultado seria negativo — e um pico negativo faria a folga aparecer INVERTIDA, que é pior do
// que não ter métrica.
func TestPicoIgnoraORuidoDeUmaLeituraNegativa(t *testing.T) {
	rb, err := NewRunBudget(1000)
	if err != nil {
		t.Fatal(err)
	}
	rb.registarConsumo(-5)
	if _, visto := rb.PicoDeConsumo(); visto {
		t.Error("uma leitura NEGATIVA foi contada como observacao — a folga apareceria invertida")
	}
}
