package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// captureSink é um sink server-side que CAPTURA o valor colocado. Modela um ponto
// de injecção que retém a credencial (o interior do guest). Serve para provar que
// a ÚNICA saída do valor ([vault.Secret.DeliverTo]) entrega mesmo o valor correcto
// server-side — nunca ao agente.
type captureSink struct {
	ref    string
	secret string
	calls  int
}

func (c *captureSink) Place(ref, secret string) error {
	c.ref = ref
	c.secret = secret
	c.calls++
	return nil
}

func TestVault_Secret_RedigeMasEntregaServerSide(t *testing.T) {
	vlt := NewMemoryVault()
	key := VaultKey{Provider: provider, Region: region, Capability: provInScopeCap}
	vlt.Put(key, sentinel)

	sec, err := vlt.Fetch(context.Background(), key)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// redação em todos os caminhos.
	for name, s := range map[string]string{
		"String": sec.String(),
		"%v":     fmt.Sprintf("%v", sec),
		"%#v":    fmt.Sprintf("%#v", sec),
	} {
		if strings.Contains(s, sentinel) {
			t.Errorf("%s expoe o segredo: %s", name, s)
		}
	}
	raw, err := json.Marshal(sec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), sentinel) {
		t.Errorf("JSON expoe o segredo: %s", raw)
	}
	// o ref é NÃO-SECRETO e disponível.
	if sec.Ref() != key.Provider+"|"+key.Region+"|"+key.Capability {
		t.Errorf("ref inesperado: %q", sec.Ref())
	}

	// a ÚNICA saída entrega o valor CORRECTO server-side.
	var sink captureSink
	if err := sec.DeliverTo(&sink); err != nil {
		t.Fatalf("DeliverTo: %v", err)
	}
	if sink.secret != sentinel {
		t.Errorf("valor entregue server-side incorrecto: %q", sink.secret)
	}
	if sink.calls != 1 {
		t.Errorf("esperado 1 Place, obtido %d", sink.calls)
	}
}

func TestVault_Fetch_SemMaterial_FailClosed(t *testing.T) {
	vlt := NewMemoryVault()
	_, err := vlt.Fetch(context.Background(), VaultKey{Provider: "x", Region: "y", Capability: "z"})
	if !errors.Is(err, ErrNoMaterial) {
		t.Fatalf("esperado ErrNoMaterial, obtido %v", err)
	}
}

func TestVault_SecretZero_IsZero_EntregaFalha(t *testing.T) {
	vlt := NewMemoryVault()
	key := VaultKey{Provider: provider, Region: region, Capability: provInScopeCap}
	vlt.Put(key, sentinel)
	vlt.Remove(key)
	_, err := vlt.Fetch(context.Background(), key)
	if !errors.Is(err, ErrNoMaterial) {
		t.Fatalf("apos Remove esperado ErrNoMaterial, obtido %v", err)
	}
}
