package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// Estes testes fecham o achado A1 da auditoria de 2026-08-17: o PDP decidia — e o WORM selava —
// sobre um `resource_value` CONSTANTE enquanto a execução tocava o argumento que o MODELO
// escolheu. Com dois documentos na raiz semeada, o modelo lia o segundo e o trilho nomeava o
// primeiro.
//
// A propriedade tem dois lados e ambos são exercitados: o recurso auditado passa a ser o
// EFECTIVO, e uma configuração que não o garanta NÃO ARRANCA.

func escreverRegistry(t *testing.T, conteudo string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tools.json")
	if err := os.WriteFile(p, []byte(conteudo), 0o600); err != nil {
		t.Fatalf("escrever registry: %v", err)
	}
	t.Setenv("AOS_MODEL_TOOLS", p)
}

// TestResourceValue_EfeitoParametrizadoComConstante_AbortaArranque: é o coração da correcção.
// Uma tool cujo efeito o modelo parametriza (sandbox.path_arg) mas cujo recurso é uma CONSTANTE
// é recusada no arranque — não aceite com um aviso, não corrigida em silêncio. Se isto fosse
// opt-in, uma configuração não migrada continuaria a mentir, que é exactamente como o defeito
// sobreviveu até ser encontrado por teste.
func TestResourceValue_EfeitoParametrizadoComConstante_AbortaArranque(t *testing.T) {
	escreverRegistry(t, `[{"name":"doc_read","capability":"cap:fs.read",
	  "resource_type":"file","resource_value":"doc://notes","resource_region":"eu",
	  "sandbox":{"command":"read","path_arg":"doc_id"}}]`)

	_, _, err := loadModelToolsFromEnv()
	if err == nil {
		t.Fatal("um efeito parametrizado com recurso CONSTANTE tem de abortar: o selo do WORM nomearia um recurso diferente do tocado")
	}
	if !errors.Is(err, ErrBadModelTools) {
		t.Fatalf("erro tem de ser atribuivel a ErrBadModelTools, veio: %v", err)
	}
	// A mensagem tem de dizer o que fazer — um erro que nomeia o problema sem a saída obriga
	// quem opera a ler o código.
	if !strings.Contains(err.Error(), "path_arg") && !strings.Contains(err.Error(), "doc_id") {
		t.Fatalf("a mensagem tem de nomear o slot que torna o efeito parametrizado: %v", err)
	}
}

// TestResourceValue_ConstanteSemSandbox_ContinuaAceite: retro-compatibilidade exacta. Uma tool
// SEM efeito parametrizado mantém a constante e não muda de comportamento — a correcção só
// aperta onde havia divergência possível.
func TestResourceValue_ConstanteSemSandbox_ContinuaAceite(t *testing.T) {
	escreverRegistry(t, `[{"name":"web_post","capability":"cap:http.post",
	  "resource_type":"http","resource_value":"https://api.example.com/results","resource_region":"eu"}]`)

	_, bindings, err := loadModelToolsFromEnv()
	if err != nil {
		t.Fatalf("uma constante sem efeito parametrizado continua valida: %v", err)
	}
	if got := bindings["web_post"].resourceValue; got != "https://api.example.com/results" {
		t.Fatalf("resource_value alterado sem razao: %q", got)
	}
}

// TestResourceValue_SintaxeAmbigua_AbortaArranque: chavetas por fechar deixariam o operador a
// julgar que declarou um template quando declarou uma constante com chavetas — e a constante
// voltaria a divergir do efeito, em silêncio.
func TestResourceValue_SintaxeAmbigua_AbortaArranque(t *testing.T) {
	for _, mau := range []string{"doc://{doc_id", "doc://doc_id}", "doc://{}"} {
		escreverRegistry(t, `[{"name":"t","capability":"c","resource_value":"`+mau+`"}]`)
		if _, _, err := loadModelToolsFromEnv(); err == nil {
			t.Fatalf("resource_value %q e ambiguo e tem de ser recusado", mau)
		}
	}
}

// TestRecursoEfectivo_SlotPreenchidoPeloArgumento: a propriedade central. O recurso que chega ao
// RM (e daí ao PDP e ao selo) é o que o modelo pediu, não a constante do registry.
func TestRecursoEfectivo_SlotPreenchidoPeloArgumento(t *testing.T) {
	c := &toolEnrichingClient{
		inner: modeloQueChama("doc_read", `{"doc_id":"confidencial"}`),
		bindings: map[string]toolBinding{"doc_read": {
			capability: "cap:fs.read", resourceType: "file",
			resourceValue: "doc://{doc_id}", resourceRegion: "eu",
		}},
	}
	resp, err := c.Call(context.Background(), agentruntime.PromptView{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := resp.ToolCalls[0].ResourceValue; got != "doc://confidencial" {
		t.Fatalf("o recurso auditado tem de ser o EFECTIVO; veio %q", got)
	}
	// O que NÃO muda: capability e taint continuam a vir do registry trusted.
	if resp.ToolCalls[0].Capability != "cap:fs.read" {
		t.Fatalf("capability tem de continuar a vir do registry: %q", resp.ToolCalls[0].Capability)
	}
	if resp.ToolCalls[0].AuthorizationTaint != "" {
		t.Fatal("o taint tem de continuar vazio (untrusted) — AOS-069")
	}
}

// TestRecursoEfectivo_SlotAusente_NegaEmVezDeCairNaConstante: o caso que reporia o defeito. Sem
// o argumento, a saída correcta NÃO é a constante nem a string vazia — é negar.
func TestRecursoEfectivo_SlotAusente_NegaEmVezDeCairNaConstante(t *testing.T) {
	casos := map[string]string{
		"argumento em falta": `{"outro":"x"}`,
		"args ilegiveis":     `nao é json`,
		"slot vazio":         `{"doc_id":""}`,
		"slot nao-escalar":   `{"doc_id":{"a":1}}`,
	}
	for nome, input := range casos {
		t.Run(nome, func(t *testing.T) {
			c := &toolEnrichingClient{
				inner: modeloQueChama("doc_read", input),
				bindings: map[string]toolBinding{"doc_read": {
					capability: "cap:fs.read", resourceType: "file",
					resourceValue: "doc://{doc_id}", resourceRegion: "eu",
				}},
			}
			resp, err := c.Call(context.Background(), agentruntime.PromptView{})
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if resp.ToolCalls[0].Capability != "" {
				t.Fatal("sem recurso resolvivel a capability TEM de ficar vazia (default-deny no RM)")
			}
			if resp.ToolCalls[0].ResourceValue == "doc://{doc_id}" {
				t.Fatal("o template por resolver NAO pode viajar como se fosse um recurso")
			}
		})
	}
}

// modeloQueChama devolve um ModelClient que emite uma tool call com os argumentos dados — o
// mínimo para exercitar o enriquecimento sem tocar num provider real.
func modeloQueChama(tool, args string) agentruntime.ModelClient {
	return modelClientFunc(func(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		return agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{
			{ToolID: tool, Input: json.RawMessage(args)},
		}}, nil
	})
}

type modelClientFunc func(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error)

func (f modelClientFunc) Call(ctx context.Context, v agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return f(ctx, v)
}

// TestResourceValue_VazioComEfeitoParametrizado_EIsento: a fronteira do que a validação recusa.
// O defeito A1 é o trilho AFIRMAR um recurso que não foi o tocado; um valor vazio não afirma
// nada. Isentá-lo mantém a correcção sobre misatribuição e não sobre ausência — e não abre
// bypass útil, porque quem o usasse ficaria sem recurso na decisão e na auditoria.
func TestResourceValue_VazioComEfeitoParametrizado_EIsento(t *testing.T) {
	escreverRegistry(t, `[{"name":"doc_read","capability":"cap:fs.read",
	  "sandbox":{"command":"read","path_arg":"doc_id"}}]`)
	if _, _, err := loadModelToolsFromEnv(); err != nil {
		t.Fatalf("resource_value vazio nao afirma recurso nenhum e nao deve abortar: %v", err)
	}
}
