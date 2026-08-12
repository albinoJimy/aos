package main

// AOS-278 — Estágio de identidade real do GW (CUTOVER DURO). Prova que o caminho do MODELO
// passou a EXIGIR um token NHI EdDSA real cuja cadeia on-behalf-of enraíza num humano (ADR-003),
// verificado pelo MESMO verifier das tool calls, com o principal RESOLVIDO do token e NUNCA
// forjado. O antigo stub (nodeModelAuthn), que forjava aos-node/aos-agent e devolvia allow
// incondicional, foi removido — não há fallback.
//
// Estas provas exercitam as quatro dobras que o cutover fecha:
//   1. credencial VÁLIDA (token verificável, escopo sela model:invoke) ⇒ o pipeline SEGUE;
//   2. credencial INVÁLIDA (token não-verificável) ⇒ DENY atribuível;
//   3. AUSÊNCIA de credencial no ctx ⇒ DENY atribuível (sem principal forjado);
//   4. verifier NÃO-LIGADO (o estado intermédio antes do bind do Bootstrap) ⇒ DENY fail-closed,
//      e o MESMO gateway passa a SEGUIR depois de ligado — a prova de que o bind é o que fecha
//      o cutover, e de que sem ele nenhum turno de modelo corre.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/identity"
)

// aos278OpenAIServer é um httptest que responde o wire OpenAI a qualquer chat/completions —
// o "provider" a jusante do gateway. Um turno que chega aqui PASSOU o estágio authn.
func aos278OpenAIServer(t *testing.T, reached *bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if reached != nil {
			*reached = true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// aos278AuthorityReader constrói uma autoridade de identidade REAL cuja classe "reader"
// concede model:invoke E cap:doc.read — para poder mintar tokens COM e SEM model:invoke no
// escopo selado (o escopo do token = pedido ∩ classe).
func aos278AuthorityReader(t *testing.T) (*integration.IssuerAuthority, *identity.Verifier) {
	t.Helper()
	auth, err := integration.NewIssuerAuthority(integration.AuthorityConfig{
		IssuerID:  "iss-aos278",
		Classes:   map[string]identity.ClassPolicy{"reader": {TTL: time.Hour, Scope: []string{"model:invoke", "cap:doc.read"}}},
		Directory: integration.NewAllowlistDirectory("operator-alice"),
	})
	if err != nil {
		t.Fatalf("NewIssuerAuthority: %v", err)
	}
	return auth, integration.NewVerifierFromAuthority(auth)
}

func aos278Mint(t *testing.T, auth *integration.IssuerAuthority, scope ...string) string {
	t.Helper()
	tok, err := auth.MintForHuman(context.Background(), "operator-alice", "agent-1", "reader", scope)
	if err != nil {
		t.Fatalf("MintForHuman(%v): %v", scope, err)
	}
	return tok.Compact
}

// TestAOS278_ValidCredentialProceeds — AC1/AC3: um token verificável cujo escopo sela
// model:invoke atravessa o estágio authn REAL e o pipeline segue até ao provider.
func TestAOS278_ValidCredentialProceeds(t *testing.T) {
	var reached bool
	srv := aos278OpenAIServer(t, &reached)
	auth, verifier := aos278AuthorityReader(t)

	mc, err := newGatewayModelClient(verifier, srv.URL, "gpt-4o", "", defaultModelGatewayRegion, defaultModelGatewayBoard, nil, nil, nil)
	if err != nil {
		t.Fatalf("newGatewayModelClient: %v", err)
	}
	ctx := withModelCredential(context.Background(), aos278Mint(t, auth, "model:invoke"))
	if _, err := mc.Call(ctx, agentruntime.PromptView{Turn: 1, Materialized: []byte("olá")}); err != nil {
		t.Fatalf("credencial VALIDA (escopo sela model:invoke) devia atravessar o authn e seguir, veio: %v", err)
	}
	if !reached {
		t.Fatal("o turno com credencial valida devia CHEGAR ao provider (pipeline seguiu)")
	}
}

// TestAOS278_InvalidCredentialDeniedAttributable — AC3: um token NÃO-verificável é NEGADO
// (o verifier real rejeita-o) e o turno NUNCA chega ao provider.
func TestAOS278_InvalidCredentialDeniedAttributable(t *testing.T) {
	var reached bool
	srv := aos278OpenAIServer(t, &reached)
	_, verifier := aos278AuthorityReader(t)

	mc, err := newGatewayModelClient(verifier, srv.URL, "gpt-4o", "", defaultModelGatewayRegion, defaultModelGatewayBoard, nil, nil, nil)
	if err != nil {
		t.Fatalf("newGatewayModelClient: %v", err)
	}
	ctx := withModelCredential(context.Background(), "nao-e-um-token-nhi-valido")
	if _, err := mc.Call(ctx, agentruntime.PromptView{Turn: 1, Materialized: []byte("olá")}); err == nil {
		t.Fatal("credencial INVALIDA devia ser NEGADA pelo estagio authn (fail-closed)")
	}
	if reached {
		t.Fatal("um turno com credencial invalida NAO pode chegar ao provider")
	}
}

// TestAOS278_MissingCredentialDeniedNotForged — AC1/AC3 (o coração do cutover): SEM credencial
// no ctx o principal chega VAZIO e o estágio authn nega — o antigo stub, em contraste,
// forjava aos-node/aos-agent e deixava passar. Prova que a ausência é DENY, não allow forjado.
func TestAOS278_MissingCredentialDeniedNotForged(t *testing.T) {
	var reached bool
	srv := aos278OpenAIServer(t, &reached)
	_, verifier := aos278AuthorityReader(t)

	mc, err := newGatewayModelClient(verifier, srv.URL, "gpt-4o", "", defaultModelGatewayRegion, defaultModelGatewayBoard, nil, nil, nil)
	if err != nil {
		t.Fatalf("newGatewayModelClient: %v", err)
	}
	// context.Background() SEM withModelCredential ⇒ modelCredentialFromContext devolve "".
	if _, err := mc.Call(context.Background(), agentruntime.PromptView{Turn: 1, Materialized: []byte("olá")}); err == nil {
		t.Fatal("SEM credencial no ctx o turno devia ser NEGADO (o stub forjava um principal; o estagio real nao)")
	}
	if reached {
		t.Fatal("um turno sem credencial NAO pode chegar ao provider")
	}
}

// TestAOS278_TokenWithoutModelInvokeDenied — o nó não ALARGA a autoridade do token: um token
// válido cujo escopo NÃO sela model:invoke é negado (a concessão do nó reconcilia-se com o
// escopo selado, menor privilégio) — o turno não chega ao provider.
func TestAOS278_TokenWithoutModelInvokeDenied(t *testing.T) {
	var reached bool
	srv := aos278OpenAIServer(t, &reached)
	auth, verifier := aos278AuthorityReader(t)

	mc, err := newGatewayModelClient(verifier, srv.URL, "gpt-4o", "", defaultModelGatewayRegion, defaultModelGatewayBoard, nil, nil, nil)
	if err != nil {
		t.Fatalf("newGatewayModelClient: %v", err)
	}
	// Escopo pedido = {cap:doc.read} (∩ classe ⇒ token sela cap:doc.read, SEM model:invoke).
	ctx := withModelCredential(context.Background(), aos278Mint(t, auth, "cap:doc.read"))
	if _, err := mc.Call(ctx, agentruntime.PromptView{Turn: 1, Materialized: []byte("olá")}); err == nil {
		t.Fatal("token sem model:invoke no escopo selado devia ser NEGADO (o no nao alarga a autoridade do token)")
	}
	if reached {
		t.Fatal("um turno cuja autoridade nao inclui model:invoke NAO pode chegar ao provider")
	}
}

// TestAOS278_UnboundVerifierFailsClosedThenBinds prova o CUTOVER DE COMPOSIÇÃO: o gateway é
// construído com o verifier LIGADO TARDIAMENTE ([lateBoundModelVerifier]). Enquanto NÃO-ligado,
// mesmo uma credencial que seria válida é NEGADA (ErrModelIdentityNotComposed) — o nó não corre
// turnos de modelo sem identidade real. Depois do bind (o que o Bootstrap faz), o MESMO gateway
// e a MESMA credencial SEGUEM. É a prova de que o bind é o que fecha o cutover.
func TestAOS278_UnboundVerifierFailsClosedThenBinds(t *testing.T) {
	var reached bool
	srv := aos278OpenAIServer(t, &reached)
	auth, realVerifier := aos278AuthorityReader(t)

	holder := &lateBoundModelVerifier{} // NÃO-ligado
	mc, err := newGatewayModelClient(holder, srv.URL, "gpt-4o", "", defaultModelGatewayRegion, defaultModelGatewayBoard, nil, nil, nil)
	if err != nil {
		t.Fatalf("newGatewayModelClient: %v", err)
	}
	ctx := withModelCredential(context.Background(), aos278Mint(t, auth, "model:invoke"))

	// NÃO-ligado ⇒ nega, mesmo com credencial que seria válida.
	if _, err := mc.Call(ctx, agentruntime.PromptView{Turn: 1, Materialized: []byte("olá")}); err == nil {
		t.Fatal("verifier NAO-ligado devia NEGAR (ErrModelIdentityNotComposed) — o no nao verifica tokens sem raiz de confianca real")
	}
	if reached {
		t.Fatal("com o verifier nao-ligado nenhum turno pode chegar ao provider")
	}

	// bind (o que o Bootstrap faz) ⇒ o MESMO gateway passa a seguir.
	holder.bind(realVerifier)
	if _, err := mc.Call(ctx, agentruntime.PromptView{Turn: 2, Materialized: []byte("olá")}); err != nil {
		t.Fatalf("depois do bind a MESMA credencial devia seguir, veio: %v", err)
	}
	if !reached {
		t.Fatal("depois do bind o turno devia CHEGAR ao provider")
	}
}

// TestAOS278_CredentialSurvivesResumeThreading prova que o token do run viaja no ctx por-run
// consumido pelo cliente de modelo, TAMBÉM sob a decoração de RETOMA — o decorador
// resumeAwareModelClient (outermost do nó) delega o ctx intacto no gateway, pelo que a
// credencial anexada ao runCtx (service.go) alcança o estágio authn na retoma tal como num run
// normal. Sem isto, a retoma correria o turno de modelo sem identidade.
func TestAOS278_CredentialSurvivesResumeThreading(t *testing.T) {
	var reached bool
	srv := aos278OpenAIServer(t, &reached)
	auth, verifier := aos278AuthorityReader(t)

	inner, err := newGatewayModelClient(verifier, srv.URL, "gpt-4o", "", defaultModelGatewayRegion, defaultModelGatewayBoard, nil, nil, nil)
	if err != nil {
		t.Fatalf("newGatewayModelClient: %v", err)
	}
	// Decoração do nó (Bootstrap): o resumeAwareModelClient é o outermost.
	mc := newResumeAwareModelClient(inner)

	// Um turno NÃO coberto pelo plano de replay delega no gateway real: a credencial no ctx
	// (o que service.go anexa ao runCtx) tem de o atravessar até ao estágio authn.
	ctx := withReplayPlan(withModelCredential(context.Background(), aos278Mint(t, auth, "model:invoke")), replayPlan{1: {Text: "turno ja dado"}})
	if _, err := mc.Call(ctx, agentruntime.PromptView{Turn: 2, Materialized: []byte("turno vivo")}); err != nil {
		t.Fatalf("na retoma, um turno vivo com credencial no ctx devia atravessar o gateway, veio: %v", err)
	}
	if !reached {
		t.Fatal("o turno vivo da retoma devia chegar ao provider (credencial sobreviveu ao threading)")
	}
}
