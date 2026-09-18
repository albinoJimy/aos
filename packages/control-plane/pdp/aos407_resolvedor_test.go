package pdp

import (
	"context"
	"sync"
	"testing"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
)

// resolvedorVivo é uma autoridade board→região que roda o mapa sem reabrir o PDP, como a
// SovereignRegionAuthority do nó.
type resolvedorVivo struct {
	mu     sync.RWMutex
	regiao map[string]string
}

func (r *resolvedorVivo) RegionFor(board string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.regiao[board]
	return reg, ok && board != ""
}

func (r *resolvedorVivo) rodar(board, regiao string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.regiao = map[string]string{board: regiao}
}

func regiaoDaObrigacao(d Decision) string {
	for _, o := range d.Obligations {
		if o.Type == ObligationRegion {
			return o.Params["region"]
		}
	}
	return ""
}

// TestAOS407_SetBoardRegionsLigaDepoisDoOpenEVeARotacao — FALHA-ANTES: o PDP só aceitava uma
// fotografia do registo, e só no Open; o nó abre o PDP antes de a autoridade existir.
func TestAOS407_SetBoardRegionsLigaDepoisDoOpenEVeARotacao(t *testing.T) {
	p := mustOpen(t)
	in := httpPost()
	in.Principal.Board = "board:prod"

	antes, err := p.Decide(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if regiaoDaObrigacao(antes) != "" || p.SovereigntyEnabled() {
		t.Fatalf("sem resolvedor a soberania é inerte: %+v", antes)
	}

	vivo := &resolvedorVivo{regiao: map[string]string{"board:prod": "eu"}}
	p.SetBoardRegions(vivo)
	d, err := p.Decide(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if d.Effect != Permit || regiaoDaObrigacao(d) != "eu" {
		t.Fatalf("com o resolvedor ligado: permit com region=eu; veio %+v", d)
	}

	vivo.rodar("board:prod", "eu-central")
	d, _ = p.Decide(context.Background(), in)
	if regiaoDaObrigacao(d) != "eu-central" {
		t.Fatalf("a rotação da autoridade tem de valer sem reabrir o PDP; veio %+v", d)
	}

	in.Principal.Board = ""
	d, _ = p.Decide(context.Background(), in)
	if d.Effect != Deny {
		t.Fatalf("board vazio com soberania ligada é deny; veio %+v", d)
	}
}

// TestAOS407_WithBoardRegionsNilFicaInerte — um *Registry nil dentro da interface negaria tudo.
func TestAOS407_WithBoardRegionsNilFicaInerte(t *testing.T) {
	var nada *govsov.Registry
	p := mustOpen(t)
	WithBoardRegions(nada)(p)
	if p.SovereigntyEnabled() {
		t.Fatal("WithBoardRegions(nil) tem de deixar a soberania inerte")
	}
	d, err := p.Decide(context.Background(), httpPost())
	if err != nil || d.Effect != Permit {
		t.Fatalf("inerte: permit como antes; veio %+v err=%v", d, err)
	}
}

// TestAOS407_SetBoardRegionsSemCorrida — decisões concorrentes com a ligação (-race).
func TestAOS407_SetBoardRegionsSemCorrida(t *testing.T) {
	p := mustOpen(t)
	in := httpPost()
	in.Principal.Board = "board:prod"
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = p.Decide(context.Background(), in)
			}
		}()
	}
	p.SetBoardRegions(&resolvedorVivo{regiao: map[string]string{"board:prod": "eu"}})
	wg.Wait()
}
