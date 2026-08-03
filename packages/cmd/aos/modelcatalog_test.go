package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/toolset"
)

// TestSignedToolRegistry_Disabled — sem AOS_MODEL_TOOLS_REGISTER, o registo é no-op (o nó mantém o
// catálogo/revalidador de referência).
func TestSignedToolRegistry_Disabled(t *testing.T) {
	t.Setenv("AOS_MODEL_TOOLS", writeTools(t, `[{"name":"web_post","capability":"cap:http.post"}]`))
	t.Setenv("AOS_MODEL_TOOLS_REGISTER", "")
	cat, reval, pol, err := buildSignedToolRegistryFromEnv()
	if err != nil || cat != nil || reval != nil || pol != nil {
		t.Fatalf("register desligado ⇒ (nil,nil,nil,nil); got cat=%v reval=%v pol=%v err=%v", cat, reval, pol, err)
	}
}

// TestSignedToolRegistry_RevalidationPasses — com o registo ligado, a entry ASSINADA de web_post
// PASSA todos os estágios da revalidação (lookup no frozen, identidade, digest, assinatura contra o
// trust store, scope/egress). É esta admissão que faz a decisão fluir para o PDP/Cedar.
func TestSignedToolRegistry_RevalidationPasses(t *testing.T) {
	t.Setenv("AOS_MODEL_TOOLS", writeTools(t, `[{
		"name":"web_post","capability":"cap:http.post","resource_region":"eu",
		"egress":"external","credential_scopes":["net:http.post"]
	}]`))
	t.Setenv("AOS_MODEL_TOOLS_REGISTER", "1")
	cat, reval, pol, err := buildSignedToolRegistryFromEnv()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if cat == nil || reval == nil || pol == nil {
		t.Fatal("register ligado ⇒ catalogo/revalidador/policy compostos")
	}
	ctx := context.Background()
	frozen, err := toolset.FreezeToolSet(ctx, cat, "run-1", nil)
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	entries, err := cat.ActiveEntries(ctx)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ActiveEntries: %d entries, err=%v", len(entries), err)
	}
	dec, err := reval.Revalidate(ctx, revalidation.Request{
		RunID:   "run-1",
		StepID:  "s1",
		ToolID:  "web_post",
		Current: entries[0],
		Frozen:  frozen,
		Policy:  pol.Policy("run-1"),
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
	if _, _, _, err := buildSignedToolRegistryFromEnv(); err == nil {
		t.Fatal("egress invalido devia ABORTAR fail-closed")
	}
	// Register ligado mas AOS_MODEL_TOOLS ausente ⇒ aborta (config incoerente).
	t.Setenv("AOS_MODEL_TOOLS", filepath.Join(t.TempDir(), "nao-existe.json"))
	if _, _, _, err := buildSignedToolRegistryFromEnv(); err == nil {
		t.Fatal("register sem AOS_MODEL_TOOLS bem formado devia ABORTAR")
	}
}
