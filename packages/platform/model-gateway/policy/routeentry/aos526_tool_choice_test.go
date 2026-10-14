package routeentry_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-526 — A REGRA `params.tool_choice` PASSA PELA VIA ASSINADA, sem mudar o formato do registo:
// o perfil viaja dentro dele e é lido pela leitura fechada do gateway, que conhece o campo novo.

// Um registo ANTIGO (sem o campo) lê-se igual: o mesmo perfil e o mesmo digest. O digest pinado
// é o sha256 do JSON canónico do perfil escrito à mão — a regra do digest de antes do AOS-526.
func TestAOS526_RegistoAntigoLeSeIgual(t *testing.T) {
	a := novoAmbiente(t)
	rt.Good(t, a.dir, "antigo", rt.ProfileJSON, rt.Board, rt.Region, a.key)
	entradas, err := routeentry.LoadDir(a.cfg)
	if err != nil || len(entradas) != 1 {
		t.Fatalf("o registo antigo nao carregou: %v", err)
	}
	quero := modelgateway.RouteProfile{Requested: "gpt-4o", ExpectedModel: "openai/modelo-novo-4.5", WireClass: modelgateway.WireOpenAIChat, Capabilities: []string{modelgateway.CapabilityTools}}
	if !reflect.DeepEqual(entradas[0].Profile, quero) {
		t.Fatalf("o perfil de um registo antigo leu-se diferente: %+v", entradas[0].Profile)
	}
	soma := sha256.Sum256([]byte(`{"requested":"gpt-4o","expected_model":"openai/modelo-novo-4.5","wire_class":"openai-chat-completions","capabilities":["tools"]}`))
	if pinado := "sha256:" + hex.EncodeToString(soma[:]); entradas[0].ProfileDigest != pinado || entradas[0].Profile.Digest() != pinado {
		t.Fatalf("o digest do perfil de um registo antigo mudou: %s, quero %s", entradas[0].ProfileDigest, pinado)
	}
}

// Um registo com a regra carrega, com o valor no perfil; com `nunca` por extenso carrega com o
// digest do perfil sem ela.
func TestAOS526_RegistoComARegraCarrega(t *testing.T) {
	a := novoAmbiente(t)
	comRegra := strings.Replace(rt.ProfileJSON, `"capabilities":["tools"]`, `"capabilities":["tools"],"params":{"tool_choice":"enquanto_o_contrato_nao_estiver_satisfeito"}`, 1)
	rt.Good(t, a.dir, "com-regra", comRegra, rt.Board, rt.Region, a.key)
	entradas, err := routeentry.LoadDir(a.cfg)
	if err != nil || len(entradas) != 1 {
		t.Fatalf("o registo com a regra nao carregou: %v", err)
	}
	if p := entradas[0].Profile.Params; p == nil || p.ToolChoice != port.ToolChoiceWhileContractPending || routeentry.ReturnsState(entradas[0].Profile) {
		t.Fatalf("a regra nao chegou ao perfil carregado: %+v", p)
	}
	if entradas[0].ProfileDigest == rt.ProfileDigest(t, rt.ProfileJSON) {
		t.Fatal("a regra nao mudou o digest do perfil")
	}

	b := novoAmbiente(t)
	nunca := strings.Replace(rt.ProfileJSON, `"capabilities":["tools"]`, `"capabilities":["tools"],"params":{"tool_choice":"nunca"}`, 1)
	rt.Good(t, b.dir, "nunca", nunca, rt.Board, rt.Region, b.key)
	entradas, err = routeentry.LoadDir(b.cfg)
	if err != nil || len(entradas) != 1 || entradas[0].ProfileDigest != rt.ProfileDigest(t, rt.ProfileJSON) {
		t.Fatalf("`nunca` por extenso tinha de carregar com o digest do perfil sem a regra: %v", err)
	}
}

// Um valor fora do vocabulário recusa a pasta no arranque, com a causa do perfil.
func TestAOS526_RegistoComValorForaDoVocabularioRecusa(t *testing.T) {
	for _, valor := range []string{`"required"`, `"sempre"`, `{"type":"function","function":{"name":"doc_read"}}`, `"Enquanto_o_contrato_nao_estiver_satisfeito"`} {
		mau := strings.Replace(rt.ProfileJSON, `"capabilities":["tools"]`, `"capabilities":["tools"],"params":{"tool_choice":`+valor+`}`, 1)
		caso{nome: valor, causa: routeentry.CauseBadProfile, registo: func(d map[string]any) {
			d["perfil"] = json.RawMessage(mau)
		}}.correr(t)
	}
}
