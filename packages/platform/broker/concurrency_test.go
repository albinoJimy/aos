package broker

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/substrate/sandbox"
)

// TestConcorrencia_TrocaEInjeccao exercita trocas e injecções em paralelo para o
// detector de corridas (-race): o leaseStore, o relógio e o guest têm de ser
// concorrente-seguros, e cada troca produz um handle/lease-id distinto.
func TestConcorrencia_TrocaEInjeccao(t *testing.T) {
	st := newStack(t, time.Hour)
	inj, err := st.broker.NewInjector(st.guest)
	if err != nil {
		t.Fatalf("NewInjector: %v", err)
	}

	const n = 32
	var wg sync.WaitGroup
	handles := make([]Handle, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			runID := "run-" + strconv.Itoa(i)
			h, err := st.broker.Exchange(context.Background(), request(runID, provInScopeCap))
			handles[i], errs[i] = h, err
		}(i)
	}
	wg.Wait()

	seen := make(map[Handle]struct{}, n)
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("Exchange[%d]: %v", i, errs[i])
		}
		if _, dup := seen[handles[i]]; dup {
			t.Fatalf("handle duplicado: %q", handles[i])
		}
		seen[handles[i]] = struct{}{}
	}

	// injecta todos em paralelo.
	var wg2 sync.WaitGroup
	for i := 0; i < n; i++ {
		wg2.Add(1)
		go func(i int) {
			defer wg2.Done()
			_ = inj.Inject(context.Background(), string(handles[i]), sandbox.Instance{ID: fmt.Sprintf("vm-%d", i)})
		}(i)
	}
	wg2.Wait()

	if st.guest.Injections() != n {
		t.Fatalf("esperado %d injeccoes, obtido %d", n, st.guest.Injections())
	}
}
