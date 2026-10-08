package modelgateway

import (
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-514 — o fecho do envelope do estado, com o nonce controlado pelo teste.

func aos514RespostaComEstado() port.ChatResponse {
	return port.ChatResponse{State: &port.ProviderState{
		Fields: []port.ProviderStateField{{Where: port.StateWhereMessage, Name: "reasoning_content", Raw: []byte(`"sim"`)}},
	}}
}

// SEM NONCE NÃO HÁ ESTADO GUARDADO. O digest do envelope vai para o tail; sem os 256 bits do
// nonce, o `sha256` de um raciocínio curto (aqui, «sim») confirmava-se por tentativas. Se a fonte
// de aleatoriedade falhar, o estado fica «não devolvível» e a causa é contada.
func TestAOS514_Fecho_SemNonceNaoGuarda(t *testing.T) {
	var resultados []string
	c := &capturaDoEstado{maxBytes: DefaultProviderStateMaxBytes, obs: func(r string) { resultados = append(resultados, r) },
		nonce: func([]byte) error { return errors.New("sem entropia") }}
	out := agentruntime.ModelResponse{Model: "servido"}
	c.capturar(&out, aos514RespostaComEstado(), "pedido")
	if out.State == nil || out.State.Status != agentruntime.ProviderStateNotReturnable || len(out.State.Bytes) != 0 {
		t.Fatalf("sem nonce o estado nao pode ser guardado: %+v", out.State)
	}
	if len(resultados) != 1 || resultados[0] != ProviderStateResultNoNonce {
		t.Fatalf("a causa tinha de ser contada: %v", resultados)
	}
}

// O NONCE ENTRA NO ENVELOPE, e é ele que separa os digests: o mesmo estado com dois nonces dá
// dois digests, e com o mesmo nonce dá o mesmo (o envelope é determinista).
func TestAOS514_Fecho_ONonceSeparaOsDigests(t *testing.T) {
	fechar := func(b byte) *agentruntime.ProviderState {
		c := &capturaDoEstado{maxBytes: DefaultProviderStateMaxBytes, nonce: func(n []byte) error {
			for i := range n {
				n[i] = b
			}
			return nil
		}}
		out := agentruntime.ModelResponse{Model: "servido", RouteProfileDigest: "sha256:nao-e-um-digest"}
		c.capturar(&out, aos514RespostaComEstado(), "pedido")
		return out.State.Normalizado()
	}
	a, a2, b := fechar(1), fechar(1), fechar(2)
	if a.Digest != a2.Digest || a.Digest == b.Digest {
		t.Fatalf("digests: a=%s a2=%s b=%s", a.Digest, a2.Digest, b.Digest)
	}
	env, err := port.UnmarshalProviderStateEnvelope(a.Bytes)
	if err != nil || env.Nonce[0] != 1 || env.RequestedModel != "pedido" || env.ServedModel != "servido" {
		t.Fatalf("envelope: %+v err=%v", env, err)
	}
	// Um digest de perfil que não tem a forma certa não entra no envelope.
	if env.RouteProfileDigest != "" {
		t.Fatalf("o digest do perfil so entra com a forma certa: %q", env.RouteProfileDigest)
	}
	// Sem captura configurada, ou sem estado na resposta, nada acontece.
	out := agentruntime.ModelResponse{}
	(*capturaDoEstado)(nil).capturar(&out, aos514RespostaComEstado(), "pedido")
	(&capturaDoEstado{maxBytes: 1}).capturar(&out, port.ChatResponse{}, "pedido")
	if out.State != nil {
		t.Fatalf("sem captura ou sem estado, o turno nao leva estado: %+v", out.State)
	}
}
