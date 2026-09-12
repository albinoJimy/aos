package broker

import (
	"errors"
	"fmt"
	"testing"
)

// TestPortErrorCode fixa a tradução opt-in dos erros da porta C3 para os códigos de porta
// ESTÁVEIS (AOS-382), incluindo os embrulhos (errors.Is/As), a precedência escopo-antes-de-genérico,
// e o "" para o não-reconhecido. Sem este teste, PortErrorCode seria superfície exportada sem
// chamadores — apodreceria e a precedência ficaria por verificar.
func TestPortErrorCode(t *testing.T) {
	casos := []struct {
		nome string
		err  error
		quer string
	}{
		{"nil", nil, ""},
		{"fora de escopo", ErrOutOfScope, CodeScopeDenied},
		{"provider fora de escopo", ErrProviderOutOfScope, CodeScopeDenied},
		{"recurso fora de escopo", ErrResourceOutOfScope, CodeScopeDenied},
		{"escopo embrulhado", fmt.Errorf("contexto: %w", ErrOutOfScope), CodeScopeDenied},
		{"sem material", ErrNoMaterial, CodeVaultUnavailable},
		{"vault nil", ErrNilVault, CodeVaultUnavailable},
		{"denied by hook", &DeniedError{Effect: "deny", Code: "E_DENIED_BY_HOOK", Reason: "x"}, CodeNoDecision},
		{"denied embrulhado", fmt.Errorf("w: %w", &DeniedError{Effect: "deny", Reason: "y"}), CodeNoDecision},
		{"nao reconhecido", errors.New("qualquer outro"), ""},
	}
	for _, c := range casos {
		if got := PortErrorCode(c.err); got != c.quer {
			t.Errorf("%s: PortErrorCode = %q, quero %q", c.nome, got, c.quer)
		}
	}
}
