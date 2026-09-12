package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/signing"
	"github.com/aos-ref/platform/registry/toolset"
)

// TestSignedToolRegistry_Disabled — sem AOS_MODEL_TOOLS_REGISTER, o parse é no-op (o nó mantém o
// catálogo/revalidador de referência).
func TestSignedToolRegistry_Disabled(t *testing.T) {
	t.Setenv("AOS_MODEL_TOOLS", writeTools(t, `[{"name":"web_post","capability":"cap:http.post"}]`))
	t.Setenv("AOS_MODEL_TOOLS_REGISTER", "")
	spec, err := parseSignedToolRegistryFromEnv()
	if err != nil || spec != nil {
		t.Fatalf("register desligado ⇒ (nil,nil); got spec=%v err=%v", spec, err)
	}
}

// TestSignedToolRegistry_RevalidationPasses — com o registo ligado, a entry ASSINADA de web_post
// PASSA todos os estágios da revalidação (lookup no frozen, identidade, digest, assinatura contra o
// trust store, scope/egress). É esta admissão que faz a decisão fluir para o PDP/Cedar. AOS-381: o
// revalidador é agora construído pelo Bootstrap SELADO no WORM do nó; aqui reproduz-se essa
// construção (trust store + revalidação sobre o MESMO store) sobre a spec parseada.
func TestSignedToolRegistry_RevalidationPasses(t *testing.T) {
	t.Setenv("AOS_MODEL_TOOLS", writeTools(t, `[{
		"name":"web_post","capability":"cap:http.post","resource_region":"eu",
		"egress":"external","credential_scopes":["net:http.post"]
	}]`))
	t.Setenv("AOS_MODEL_TOOLS_REGISTER", "1")
	spec, err := parseSignedToolRegistryFromEnv()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if spec == nil || spec.Catalog == nil || spec.Policy == nil || spec.PublisherKeyID == "" || spec.PublisherKey == nil {
		t.Fatalf("register ligado ⇒ spec completa; got %+v", spec)
	}
	ctx := context.Background()

	// Reproduz a composição do Bootstrap (signedToolRegistryRevalidator): trust store + pubkey do
	// publicador + revalidação, TUDO selado no MESMO store.
	worm := audit.NewMemStore()
	trust, err := signing.NewTrustStore(worm)
	if err != nil {
		t.Fatalf("trust store: %v", err)
	}
	if err := trust.Add(ctx, spec.PublisherKeyID, spec.PublisherKey); err != nil {
		t.Fatalf("trust add: %v", err)
	}
	reval, err := revalidation.New(trust, worm)
	if err != nil {
		t.Fatalf("revalidator: %v", err)
	}

	frozen, err := toolset.FreezeToolSet(ctx, spec.Catalog, "run-1", nil)
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	entries, err := spec.Catalog.ActiveEntries(ctx)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ActiveEntries: %d entries, err=%v", len(entries), err)
	}
	dec, err := reval.Revalidate(ctx, revalidation.Request{
		RunID:   "run-1",
		StepID:  "s1",
		ToolID:  "web_post",
		Current: entries[0],
		Frozen:  frozen,
		Policy:  spec.Policy.Policy("run-1"),
	})
	if err != nil {
		t.Fatalf("Revalidate erro: %v", err)
	}
	if !dec.Allowed {
		t.Fatalf("revalidacao NEGOU (stage=%v reason=%v) — o catalogo assinado devia ADMITIR web_post", dec.Stage, dec.Reason)
	}
}

// TestSignedToolRegistry_FailClosed — egress inválido num spec ABORTA fail-closed.
func TestSignedToolRegistry_FailClosed(t *testing.T) {
	t.Setenv("AOS_MODEL_TOOLS", writeTools(t, `[{"name":"web_post","capability":"cap:http.post","egress":"lunar"}]`))
	t.Setenv("AOS_MODEL_TOOLS_REGISTER", "1")
	if _, err := parseSignedToolRegistryFromEnv(); err == nil {
		t.Fatal("egress invalido devia ABORTAR fail-closed")
	}
	// Register ligado mas AOS_MODEL_TOOLS ausente ⇒ aborta (config incoerente).
	t.Setenv("AOS_MODEL_TOOLS", filepath.Join(t.TempDir(), "nao-existe.json"))
	if _, err := parseSignedToolRegistryFromEnv(); err == nil {
		t.Fatal("register sem AOS_MODEL_TOOLS bem formado devia ABORTAR")
	}
}
