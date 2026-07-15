package tiering

import "testing"

// escada de referência usada nos testes: economy(0,basic,fast) <
// standard(1,standard,slow) < standard-fast(1,standard,fast) < frontier(2,frontier,slow).
func refLadder() *Ladder {
	return NewLadder(
		Tier{Name: "frontier", Model: "big-reasoner", CostRank: 3, Capability: CapabilityFrontier, Fast: false},
		Tier{Name: "standard", Model: "mid", CostRank: 2, Capability: CapabilityStandard, Fast: false},
		Tier{Name: "standard-fast", Model: "mid-fast", CostRank: 2, Capability: CapabilityStandard, Fast: true},
		Tier{Name: "economy", Model: "small", CostRank: 1, Capability: CapabilityBasic, Fast: true},
	)
}

func TestLadderOrderedByCost(t *testing.T) {
	l := refLadder()
	got := l.Tiers()
	wantOrder := []string{"economy", "standard", "standard-fast", "frontier"}
	if len(got) != len(wantOrder) {
		t.Fatalf("esperava %d tiers, obtive %d", len(wantOrder), len(got))
	}
	for i, w := range wantOrder {
		if got[i].Name != w {
			t.Errorf("posicao %d: esperava %q, obtive %q (ordenacao por CostRank asc)", i, w, got[i].Name)
		}
	}
}

func TestSelectCostCapacity(t *testing.T) {
	l := refLadder()
	tests := []struct {
		name      string
		cap       Capability
		class     Class
		wantTier  string
		wantModel string
		wantNotOK bool
	}{
		{
			name: "extraccao usa o tier mais barato (economy)",
			cap:  CapabilityBasic, class: ClassBatch, wantTier: "economy", wantModel: "small",
		},
		{
			name: "raciocinio exige frontier (unico que satisfaz)",
			cap:  CapabilityFrontier, class: ClassBatch, wantTier: "frontier", wantModel: "big-reasoner",
		},
		{
			name: "standard batch escolhe o mais barato que satisfaz (standard, nao economy)",
			cap:  CapabilityStandard, class: ClassBatch, wantTier: "standard", wantModel: "mid",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := l.Select(Request{Capability: tc.cap, Class: tc.class}, nil)
			if tc.wantNotOK {
				if ok {
					t.Fatalf("esperava sem tier elegivel, obtive %q", got.Name)
				}
				return
			}
			if !ok {
				t.Fatalf("esperava tier %q, obtive nenhum", tc.wantTier)
			}
			if got.Name != tc.wantTier || got.Model != tc.wantModel {
				t.Errorf("esperava %q/%q, obtive %q/%q", tc.wantTier, tc.wantModel, got.Name, got.Model)
			}
		})
	}
}

func TestSelectLatencyVsBatch(t *testing.T) {
	l := refLadder()
	// Capacidade STANDARD: existe standard(slow, custo 2) e standard-fast(fast, custo 2).
	// INTERACTIVO favorece latência ⇒ standard-fast. BATCH favorece custo ⇒ o mais
	// barato (empate de custo, desempate por nome ⇒ "standard").
	inter, ok := l.Select(Request{Capability: CapabilityStandard, Class: ClassInteractive}, nil)
	if !ok || inter.Name != "standard-fast" {
		t.Errorf("interactivo: esperava standard-fast (favorece latencia), obtive %q (ok=%v)", inter.Name, ok)
	}
	batch, ok := l.Select(Request{Capability: CapabilityStandard, Class: ClassBatch}, nil)
	if !ok || batch.Name != "standard" {
		t.Errorf("batch: esperava standard (mais barato, tolera lento), obtive %q (ok=%v)", batch.Name, ok)
	}
}

func TestSelectFilteredNoEligible(t *testing.T) {
	l := refLadder()
	// Filtro que rejeita TODOS os modelos (fora da allowlist) ⇒ sem tier elegível.
	_, ok := l.Select(Request{Capability: CapabilityBasic, Class: ClassBatch}, func(Tier) bool { return false })
	if ok {
		t.Fatal("com filtro que rejeita tudo, nenhum tier deve ser elegivel (fail-closed)")
	}
}

func TestSelectFilterExcludesModel(t *testing.T) {
	l := refLadder()
	// Exclui o economy (small): o mais barato elegível para basic passa a ser o
	// standard (mid), pois basic é satisfeito por qualquer capacidade >= basic.
	got, ok := l.Select(Request{Capability: CapabilityBasic, Class: ClassBatch}, func(t Tier) bool {
		return t.Model != "small"
	})
	if !ok {
		t.Fatal("esperava um tier elegivel apos excluir economy")
	}
	if got.Name != "standard" {
		t.Errorf("apos excluir economy, esperava standard (proximo mais barato), obtive %q", got.Name)
	}
}

func TestCheaperStepsDown(t *testing.T) {
	l := refLadder()
	// frontier -> standard-fast (proximo mais barato: CostRank 3 -> 2, desempate nome).
	got, ok := l.Cheaper("frontier", nil)
	if !ok {
		t.Fatal("frontier deve ter um degrau mais barato")
	}
	if got.Name != "standard" && got.Name != "standard-fast" {
		t.Errorf("frontier desce para um tier de custo 2, obtive %q", got.Name)
	}
	// economy é o mais barato: nao ha para onde descer.
	if _, ok := l.Cheaper("economy", nil); ok {
		t.Error("economy e o mais barato: Cheaper deve devolver ok=false (nunca upgrade)")
	}
	// tier desconhecido: ok=false.
	if _, ok := l.Cheaper("inexistente", nil); ok {
		t.Error("tier desconhecido: Cheaper deve devolver ok=false")
	}
}

func TestCheaperSkipsFilteredRungs(t *testing.T) {
	l := refLadder()
	// A partir de frontier, se os tiers de custo 2 (standard/standard-fast) forem
	// filtrados (fora da allowlist), Cheaper salta-os e desce até economy.
	filter := func(t Tier) bool { return t.CostRank != 2 }
	got, ok := l.Cheaper("frontier", filter)
	if !ok {
		t.Fatal("esperava descer ate economy saltando os tiers filtrados")
	}
	if got.Name != "economy" {
		t.Errorf("esperava economy (unico elegivel abaixo), obtive %q", got.Name)
	}
}

func TestCheaperNeverLeavesAllowlist(t *testing.T) {
	l := refLadder()
	// Se TODOS os degraus abaixo do corrente estao fora da allowlist, Cheaper
	// recusa (ok=false) — nunca degrada para fora da fronteira.
	filter := func(t Tier) bool { return t.Name == "frontier" } // só o corrente é permitido
	if _, ok := l.Cheaper("frontier", filter); ok {
		t.Error("sem degrau elegivel dentro da allowlist, Cheaper deve devolver ok=false (nunca fora da fronteira)")
	}
}

func TestCheaperByModel(t *testing.T) {
	l := refLadder()
	got, ok := l.CheaperByModel("big-reasoner", nil)
	if !ok || got.CostRank != 2 {
		t.Errorf("CheaperByModel(big-reasoner) deve descer para custo 2, obtive %q ok=%v", got.Name, ok)
	}
	if _, ok := l.CheaperByModel("small", nil); ok {
		t.Error("CheaperByModel(small) e o mais barato: ok=false")
	}
}

func TestSelectDeterministic(t *testing.T) {
	l := refLadder()
	req := Request{Capability: CapabilityStandard, Class: ClassInteractive}
	first, _ := l.Select(req, nil)
	for i := 0; i < 100; i++ {
		got, _ := l.Select(req, nil)
		if got.Name != first.Name {
			t.Fatalf("selecao nao-determinista: %q != %q", got.Name, first.Name)
		}
	}
}
