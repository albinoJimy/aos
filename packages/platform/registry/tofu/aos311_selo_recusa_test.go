package tofu

import (
	"context"
	"errors"
	"testing"
	"time"

	audit "github.com/aos-ref/platform/audit"
)

// AOS-311, triagem de `Append(ctx)` — o selo de uma tentativa RECUSADA é prova de facto
// consumado e não pode ser cancelável por quem provoca a recusa.
//
// Desde que `audit.FileStore.Append` respeita o `ctx` — a correcção certa para o caminho que
// DECIDE se o efeito acontece — os selos pós-decisão passaram a poder ser suprimidos por um
// contexto morto. `Monitor.auditAttempt` é um deles: corre com a causa já apurada e descarta o
// erro do `Append`, pelo que a supressão seria silenciosa.

// TestAOS311_SeloDeTentativaRecusadaSobreviveAContextoMorto — com o contexto do chamador já
// cancelado, o registo da recusa continua a ser escrito.
func TestAOS311_SeloDeTentativaRecusadaSobreviveAContextoMorto(t *testing.T) {
	store := audit.NewMemStore()
	m := &Monitor{
		audit:     store,
		partition: DefaultPartition,
		now:       func() time.Time { return time.Unix(1, 0).UTC() },
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // quem provocou a recusa foi-se embora

	m.auditAttempt(ctx, "mcp:servidor-x", reference{Digest: "sha256:abc"}, errors.New("recusa de teste"))

	head, err := store.Head(context.Background(), DefaultPartition)
	if err != nil {
		t.Fatal(err)
	}
	if head != 1 {
		t.Fatalf("head=%d, quero 1 — a tentativa recusada TEM de deixar rasto mesmo com o contexto morto", head)
	}
	recs, err := store.Read(context.Background(), DefaultPartition, 1, 1)
	if err != nil || len(recs) != 1 {
		t.Fatalf("ler o selo: %v (%d)", err, len(recs))
	}
	if recs[0].Decision != audit.DecisionDeny || recs[0].ToolID != "mcp:servidor-x" {
		t.Errorf("o selo nao regista a recusa da identidade tentada: %+v", recs[0])
	}
}

// TestAOS311_OStoreContinuaARecusarSobContextoMorto — o CONTROLO.
//
// Sem ele, a correcção acima passaria se alguém tivesse desligado o AOS-311 no store — e o
// fail-closed por prazo do caminho que decide deixaria de valer. É a asserção que distingue
// «desacoplei o selo pós-efeito» de «desliguei a verificação de contexto».
func TestAOS311_OStoreContinuaARecusarSobContextoMorto(t *testing.T) {
	store := audit.NewMemStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Append(ctx, audit.AuditRecord{Partition: "x", Decision: audit.DecisionAllow}); !errors.Is(err, context.Canceled) {
		t.Fatalf("o store tem de continuar a recusar sob contexto morto (AOS-311), veio: %v", err)
	}
	if head, _ := store.Head(context.Background(), "x"); head != 0 {
		t.Fatalf("escreveu apesar do contexto cancelado (head=%d)", head)
	}
}
