package actiondedup

import (
	"fmt"
	"sync"
	"testing"
)

// TestDetectorLoopSignalsAtThreshold: repetir o MESMO hash até ao limiar sinaliza loop
// (MadeProgress passa a false); abaixo do limiar reporta progresso.
func TestDetectorLoopSignalsAtThreshold(t *testing.T) {
	d := NewDetector(Config{WindowSize: 5, Threshold: 3})
	const h = "sha256:aaaa"

	d.Observe(h) // 1
	if !d.MadeProgress() {
		t.Fatal("1 ocorrência (< limiar) não devia ser loop")
	}
	d.Observe(h) // 2
	if !d.MadeProgress() {
		t.Fatal("2 ocorrências (< limiar 3) não devia ser loop")
	}
	d.Observe(h) // 3 == limiar
	if d.MadeProgress() {
		t.Fatal("3 ocorrências (== limiar) devia sinalizar loop (MadeProgress false)")
	}
	if !d.Looping() {
		t.Fatal("Looping() devia ser true no limiar")
	}
}

// TestDetectorDistinctHashesNoLoop: hashes todos distintos nunca sinalizam loop, por muitas
// que sejam as acções.
func TestDetectorDistinctHashesNoLoop(t *testing.T) {
	d := NewDetector(Config{WindowSize: 4, Threshold: 2})
	for i := 0; i < 50; i++ {
		d.Observe(fmt.Sprintf("sha256:%04d", i))
		if !d.MadeProgress() {
			t.Fatalf("iteração %d: acções distintas não deviam sinalizar loop", i)
		}
	}
}

// TestDetectorSlidingWindowEvicts: repetições ESPARSAS (a mesma acção sempre a mais de uma
// janela de distância) NÃO acumulam — a janela deslizante despeja as ocorrências antigas.
func TestDetectorSlidingWindowEvicts(t *testing.T) {
	d := NewDetector(Config{WindowSize: 3, Threshold: 3})
	const h = "sha256:repeat"
	// Padrão h,a,b,h,c,e,h,... : cada h está a 3 posições do anterior (== janela), logo o
	// anterior é sempre despejado antes do novo entrar; a contagem de h nunca passa de 1.
	fillers := []string{"a", "b", "c", "e", "f", "g", "i", "j"}
	for i := 0; i < 8; i++ {
		d.Observe(h)
		d.Observe("sha256:" + fillers[i])
		d.Observe("sha256:" + fillers[(i+4)%len(fillers)])
		if !d.MadeProgress() {
			t.Fatalf("ciclo %d: repetições esparsas fora da janela não deviam sinalizar loop", i)
		}
	}
}

// TestDetectorNewHashResetsVerdict: depois de um loop, uma acção NOVA repõe o veredicto para
// progresso.
func TestDetectorNewHashResetsVerdict(t *testing.T) {
	d := NewDetector(Config{WindowSize: 5, Threshold: 2})
	d.Observe("sha256:x")
	d.Observe("sha256:x") // loop
	if d.MadeProgress() {
		t.Fatal("devia estar em loop")
	}
	d.Observe("sha256:novo") // acção nova
	if !d.MadeProgress() {
		t.Fatal("uma acção nova devia repor o progresso")
	}
}

// TestDetectorDisabledWhenThresholdZero: limiar <= 0 desliga o detector — nunca sinaliza
// loop, mesmo repetindo indefinidamente.
func TestDetectorDisabledWhenThresholdZero(t *testing.T) {
	d := NewDetector(Config{WindowSize: 5, Threshold: 0})
	for i := 0; i < 20; i++ {
		d.Observe("sha256:same")
	}
	if !d.MadeProgress() {
		t.Fatal("detector desligado (limiar 0) nunca devia sinalizar loop")
	}
	if d.Looping() {
		t.Fatal("detector desligado nunca está em loop")
	}
}

// TestDetectorEnabledReflectsThreshold: Enabled() distingue um detector ARMADO (Threshold>0)
// de um INERTE (Threshold<=0). É a porta que a cablagem fail-closed do breaker consulta para
// recusar ligar um detector cego quando MaxStaleIterations>0.
func TestDetectorEnabledReflectsThreshold(t *testing.T) {
	if got := NewDetector(Config{WindowSize: 5, Threshold: 3}).Enabled(); !got {
		t.Error("detector com Threshold>0 devia reportar Enabled()==true")
	}
	if got := NewDetector(Config{Threshold: 0}).Enabled(); got {
		t.Error("detector com Threshold==0 (zero-value) devia reportar Enabled()==false")
	}
	if got := NewDetector(Config{WindowSize: 5, Threshold: -1}).Enabled(); got {
		t.Error("detector com Threshold<0 devia reportar Enabled()==false")
	}
}

// TestDetectorReset esvazia a janela e o veredicto.
func TestDetectorReset(t *testing.T) {
	d := NewDetector(Config{WindowSize: 3, Threshold: 2})
	d.Observe("sha256:x")
	d.Observe("sha256:x")
	if !d.Looping() {
		t.Fatal("pré-condição: devia estar em loop")
	}
	d.Reset()
	if d.Looping() || !d.MadeProgress() {
		t.Fatal("após Reset o veredicto devia ser progresso")
	}
	d.Observe("sha256:x") // conta a partir de zero
	if d.Looping() {
		t.Fatal("após Reset a contagem devia partir de zero (1 < limiar)")
	}
}

// TestConfigEffectiveClampsWindow: uma janela menor que o limiar é elevada ao limiar (senão
// nunca o poderia atingir).
func TestConfigEffectiveClampsWindow(t *testing.T) {
	eff := Config{WindowSize: 1, Threshold: 4}.effective()
	if eff.WindowSize != 4 {
		t.Fatalf("janela devia ser elevada a 4, é %d", eff.WindowSize)
	}
	// Com a janela elevada, 4 repetições consecutivas atingem o limiar.
	d := NewDetector(Config{WindowSize: 1, Threshold: 4})
	for i := 0; i < 4; i++ {
		d.Observe("sha256:x")
	}
	if !d.Looping() {
		t.Fatal("com a janela elevada ao limiar, 4 repetições deviam sinalizar loop")
	}
}

// TestRegistryRoutesPerRun: cada run tem a sua janela independente — um loop num run não
// contamina outro.
func TestRegistryRoutesPerRun(t *testing.T) {
	reg := NewRegistry(Config{WindowSize: 5, Threshold: 2})
	reg.Observe("run-A", "sha256:x")
	reg.Observe("run-A", "sha256:x") // loop em A
	reg.Observe("run-B", "sha256:x") // 1 só em B

	if reg.Source("run-A").MadeProgress() {
		t.Fatal("run-A devia estar em loop")
	}
	if !reg.Source("run-B").MadeProgress() {
		t.Fatal("run-B (1 acção) não devia estar em loop")
	}
}

// TestRegistrySourceCreatesBeforeObserve: Source cria o detector do run antes da primeira
// acção, para satisfazer a cablagem fail-closed do breaker (fonte não-nil).
func TestRegistrySourceCreatesBeforeObserve(t *testing.T) {
	reg := NewRegistry(Config{WindowSize: 3, Threshold: 2})
	src := reg.Source("run-novo")
	if src == nil {
		t.Fatal("Source nunca devia devolver nil")
	}
	if !src.MadeProgress() {
		t.Fatal("detector recém-criado (sem acções) reporta progresso")
	}
}

// TestRegistryForget descarta o detector do run (liberta a janela); a próxima observação
// recria-o do zero.
func TestRegistryForget(t *testing.T) {
	reg := NewRegistry(Config{WindowSize: 5, Threshold: 2})
	reg.Observe("run-A", "sha256:x")
	reg.Observe("run-A", "sha256:x")
	if reg.Source("run-A").MadeProgress() {
		t.Fatal("pré-condição: run-A devia estar em loop antes do Forget")
	}
	reg.Forget("run-A")
	// Recriado do zero: 1 observação não é loop.
	reg.Observe("run-A", "sha256:x")
	if !reg.Source("run-A").MadeProgress() {
		t.Fatal("após Forget o detector devia ser recriado do zero")
	}
}

// TestDetectorConcurrent exercita o detector sob -race: Observe e MadeProgress de goroutines
// concorrentes não corrompem a janela.
func TestDetectorConcurrent(t *testing.T) {
	d := NewDetector(Config{WindowSize: 8, Threshold: 3})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				d.Observe(fmt.Sprintf("sha256:%d", i%5))
				_ = d.MadeProgress()
			}
		}(g)
	}
	wg.Wait()
}
