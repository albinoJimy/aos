package autonomy

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	audit "github.com/aos-ref/platform/audit"
)

// Achados S-06 e R-07 de duas revisões — o CUSTO que a reidratação de AOS-307 introduziu.
//
// Antes de AOS-307 o histórico era reposto a zero em cada arranque e tinha tantas entradas
// quantos os pares declarados. Desde que o arranque reidrata a partição `autonomy` inteira, ele
// passou a conter TODAS as alterações da vida do WORM — e dois caminhos varriam-no por completo:
// `LastChange` (chamado uma vez por par no provisionamento) e o `GET /autonomy` (que iterava a
// cópia defensiva de `History()` e deduplicava a seguir).
//
// O índice `last` fecha os dois. O que estes testes fixam é o INVARIANTE que o torna seguro: o
// índice e o histórico nunca divergem — porque há um só sítio que os escreve.

// varrerHistorico é a implementação ANTIGA de LastChange, mantida aqui como ORÁCULO: o índice
// tem de concordar com ela para toda a sequência. Se um caminho de escrita novo esquecer o
// índice, é esta comparação que o apanha.
func varrerHistorico(hist []LevelChange, agent, domain string) (LevelChange, bool) {
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Agent == agent && hist[i].Domain == domain {
			return hist[i], true
		}
	}
	return LevelChange{}, false
}

// TestAOS307Indice_NuncaDivergeDoHistorico — sequência MISTA pelos dois caminhos de escrita
// (SetLevel e rehidratação), com pares repetidos e um par que só aparece num deles.
func TestAOS307Indice_NuncaDivergeDoHistorico(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()

	// (1) Caminho SetLevel, com sink ligado (sela e aplica).
	r := NewLevelRegistry(WithSink(NewAuditSink(store, "")))
	passos := []struct {
		agent, domain string
		nivel         Level
		actor         string
	}{
		{"a", "http", L1, "config:node"},
		{"b", "fs", L2, "op:jimy"},
		{"a", "http", L3, "op:maria"}, // par repetido: o índice tem de ficar com o ÚLTIMO
		{"c", "net", L1, "config:node"},
		{"b", "fs", L4, "op:jimy"}, // repetido outra vez
	}
	for _, p := range passos {
		if _, err := r.SetLevel(ctx, p.agent, p.domain, p.nivel, "motivo", p.actor); err != nil {
			t.Fatalf("SetLevel %s:%s: %v", p.agent, p.domain, err)
		}
	}

	// (2) Caminho REHIDRATAÇÃO, num registo novo sobre o mesmo trilho.
	r2 := NewLevelRegistry()
	if _, err := r2.Rehydrate(ctx, store, ""); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}

	// O índice de AMBOS concorda com a varredura do histórico, par a par.
	for _, reg := range []*LevelRegistry{r, r2} {
		hist := reg.History()
		vistos := map[pairKey]struct{}{}
		for _, ch := range hist {
			vistos[pairKey{ch.Agent, ch.Domain}] = struct{}{}
		}
		if len(vistos) == 0 {
			t.Fatal("historico vazio — o teste nao mede nada")
		}
		for k := range vistos {
			quero, okQ := varrerHistorico(hist, k.agent, k.domain)
			tenho, okT := reg.LastChange(k.agent, k.domain)
			if okQ != okT || !reflect.DeepEqual(tenho, quero) {
				t.Errorf("indice diverge do historico em %s:%s — indice=%+v(%v) historico=%+v(%v)",
					k.agent, k.domain, tenho, okT, quero, okQ)
			}
		}
		// E um par que NUNCA existiu continua a não existir no índice.
		if _, ok := reg.LastChange("nao", "existe"); ok {
			t.Error("o indice inventou um par que o historico nao tem")
		}
	}

	// (3) `Pairs()` é o ESTADO: um por par, ordenado, com a última alteração de cada.
	pares := r.Pairs()
	if len(pares) != 3 {
		t.Fatalf("Pairs() = %d entradas, quero 3 (a/http, b/fs, c/net)", len(pares))
	}
	if pares[0].Agent != "a" || pares[1].Agent != "b" || pares[2].Agent != "c" {
		t.Errorf("Pairs() nao vem ordenado: %+v", pares)
	}
	if pares[0].New != L3 || pares[0].Actor != "op:maria" {
		t.Errorf("Pairs() nao traz a ULTIMA alteracao do par a/http: %+v", pares[0])
	}
	if pares[1].New != L4 {
		t.Errorf("Pairs() nao traz a ULTIMA alteracao do par b/fs: %+v", pares[1])
	}
}

// TestAOS307Indice_CustoNaoCresceComOHistorico — a asserção de custo.
//
// Com 5000 alterações sobre 3 pares, `LastChange` e `Pairs()` não podem alocar proporcionalmente
// ao histórico. Mede-se por [testing.AllocsPerRun]: `LastChange` devolve um valor e não deve
// alocar nada; `Pairs()` aloca UM slice de 3 — não de 5000 — pelo que o número de alocações é
// constante e independente do tamanho do trilho.
func TestAOS307Indice_CustoNaoCresceComOHistorico(t *testing.T) {
	r := NewLevelRegistry()
	const alteracoes = 5000
	pares := []struct{ a, d string }{{"a", "http"}, {"b", "fs"}, {"c", "net"}}
	at := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	for i := 0; i < alteracoes; i++ {
		p := pares[i%len(pares)]
		r.restore(LevelChange{
			Agent: p.a, Domain: p.d, Old: L0, New: Level(uint8(i%5) + 1),
			Reason: "carga", Actor: fmt.Sprintf("op:%d", i), At: at,
		})
	}
	if n := len(r.History()); n != alteracoes {
		t.Fatalf("historico = %d, quero %d — o teste nao esta a medir o que diz", n, alteracoes)
	}

	if got := testing.AllocsPerRun(100, func() { _, _ = r.LastChange("b", "fs") }); got != 0 {
		t.Errorf("LastChange alocou %v vezes; quero 0 — se voltar a varrer o historico, isto sobe", got)
	}
	// `Pairs()` aloca o slice de saída (1) e o array de suporte; o que importa é que NÃO cresce
	// com o histórico. Comparar com um registo pequeno é a asserção honesta.
	pequeno := NewLevelRegistry()
	for _, p := range pares {
		pequeno.restore(LevelChange{Agent: p.a, Domain: p.d, New: L1, Reason: "x", Actor: "y", At: at})
	}
	grandeAllocs := testing.AllocsPerRun(50, func() { _ = r.Pairs() })
	pequenoAllocs := testing.AllocsPerRun(50, func() { _ = pequeno.Pairs() })
	if grandeAllocs != pequenoAllocs {
		t.Errorf("Pairs() aloca %v com %d alteracoes e %v com %d — o custo esta a seguir o historico",
			grandeAllocs, alteracoes, pequenoAllocs, len(pares))
	}
	// E a correcção mantém-se com o histórico grande.
	if ch, ok := r.LastChange("b", "fs"); !ok || ch.Agent != "b" {
		t.Errorf("LastChange com historico grande devolveu %+v (%v)", ch, ok)
	}
	if len(r.Pairs()) != len(pares) {
		t.Errorf("Pairs() = %d, quero %d — um por par, nao um por alteracao", len(r.Pairs()), len(pares))
	}
}
