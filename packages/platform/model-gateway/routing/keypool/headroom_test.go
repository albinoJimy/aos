package keypool_test

import (
	"testing"

	"github.com/aos-ref/platform/model-gateway/routing/keypool"
)

// TestPoolHeadroom_ReadsWithoutConsuming — o headroom é LEITURA PURA: descreve a
// folga da conta que [Pool.Select] escolheria e NÃO incrementa a carga. É a
// propriedade que permite ao router ler o sinal de carga no caminho de decisão sem
// contabilizar throughput a dobrar (o gateway é que consome, uma vez, ao adquirir a
// credencial).
func TestPoolHeadroom_ReadsWithoutConsuming(t *testing.T) {
	p := keypool.NewPool(
		keypool.Account{KeyID: "a", LimitRPM: 100, LimitTPM: 1000},
		keypool.Account{KeyID: "b", LimitRPM: 100, LimitTPM: 1000},
	)
	p.SetLoad("a", 80, 0)
	p.SetLoad("b", 20, 0)

	used, limit, saturated := p.Headroom()
	if saturated || used != 20 || limit != 100 {
		t.Fatalf("headroom = (%d/%d, saturado=%v); queria a folga da conta menos carregada (20/100)", used, limit, saturated)
	}
	// Ler de novo dá o MESMO valor: nada foi consumido (um Select teria mexido).
	used2, limit2, _ := p.Headroom()
	if used2 != used || limit2 != limit {
		t.Fatalf("a leitura consumiu throughput: (%d/%d) -> (%d/%d)", used, limit, used2, limit2)
	}
	// ... e um Select, esse, mexe — o contraste que prova que a leitura é pura.
	if _, err := p.Select(); err != nil {
		t.Fatalf("Select: %v", err)
	}
	if used3, _, _ := p.Headroom(); used3 != used+1 {
		t.Fatalf("depois de um Select a carga observada devia subir: %d -> %d", used, used3)
	}
}

// TestPoolHeadroom_SaturatedIsSafeSide — todas as contas no limite (ou pool vazio) é
// SATURADO, não folga desconhecida: sem capacidade não se presume capacidade.
func TestPoolHeadroom_SaturatedIsSafeSide(t *testing.T) {
	p := keypool.NewPool(keypool.Account{KeyID: "a", LimitRPM: 10, LimitTPM: 100})
	p.SetLoad("a", 10, 0)
	if _, _, saturated := p.Headroom(); !saturated {
		t.Fatal("conta no limite de RPM tem de reportar saturado")
	}
	if _, _, saturated := keypool.NewPool().Headroom(); !saturated {
		t.Fatal("pool vazio tem de reportar saturado (lado seguro)")
	}
}

// TestRegistryHeadroom_NoPoolIsSaturated — um par (provider, região) SEM pool é
// saturado, coerente com o fail-closed de Select (ErrNoPool): «sem pool não há
// capacidade», nunca «folga desconhecida ⇒ presume-se folga».
func TestRegistryHeadroom_NoPoolIsSaturated(t *testing.T) {
	reg := keypool.NewRegistry()
	reg.Register("openai", "eu", keypool.NewPool(keypool.Account{KeyID: "a", LimitRPM: 100, LimitTPM: 1000}))

	if _, _, saturated := reg.Headroom("openai", "eu"); saturated {
		t.Fatal("um pool com capacidade não pode reportar saturado")
	}
	if _, _, saturated := reg.Headroom("openai", "us-east"); !saturated {
		t.Fatal("um par sem pool tem de reportar saturado (fail-closed)")
	}
}
