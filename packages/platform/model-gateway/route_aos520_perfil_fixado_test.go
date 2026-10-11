package modelgateway_test

// AOS-520 (PR 0) — O PERFIL DA ROTA FICA FIXADO POR RUN.
//
// Este ficheiro era o do «limite conhecido» do AOS-519
// (TestAOS519_LimiteConhecido_RunRetomadoComOutroPerfilContinuaEmSilencio): prendia que um run
// retomado num nó que arrancou com OUTRO perfil para o mesmo alias CONTINUAVA, com o perfil novo,
// sem que nada o detectasse. Mudou DE PROPÓSITO, como aquele teste pedia: o digest do perfil passa
// a ficar fixado no run, e o adaptador confere-o em cada turno, antes de qualquer pedido.
//
// O que fica preso aqui:
//
//   - um run FIXADO retomado com outro perfil falha fechado, com a causa, e nenhum pedido sai;
//   - o gémeo: um run SEM perfil fixado — sem valor, ou com a marca de um run re-hospedado que
//     começou sem fixação — continua exactamente como continuava;
//   - num run fixado o digest vai para o manifesto do turno mesmo com a governação da rota
//     desligada; num run não fixado o turno sai byte a byte como saía;
//   - as causas são um conjunto fechado.

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// aos520Proxy responde 200 e declara ter servido o modelo dado; grava os corpos.
type aos520Proxy struct {
	mu      sync.Mutex
	servido string
	corpos  [][]byte
}

func (p *aos520Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var corpo bytes.Buffer
	_, _ = corpo.ReadFrom(r.Body)
	p.mu.Lock()
	p.corpos = append(p.corpos, corpo.Bytes())
	servido := p.servido
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-litellm-model-name", servido)
	_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
}

func (p *aos520Proxy) pedidos() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]byte(nil), p.corpos...)
}

const (
	aos520Alias   = "gpt-4o"
	aos520PerfilA = `{"requested":"gpt-4o","expected_model":"openai/modelo-a","wire_class":"openai-chat-completions","capabilities":["tools"],"params":{"max_tokens":1000}}`
	aos520PerfilB = `{"requested":"gpt-4o","expected_model":"openai/modelo-b","wire_class":"openai-chat-completions","capabilities":["tools"],"params":{"max_tokens":2000}}`
)

// aos520Arrancar é um «arranque do nó»: um gateway composto com os candidatos dados (nenhum ⇒ a
// tabela em código), na governação dada, e o adaptador do alias dado sobre o MESMO conjunto.
func aos520Arrancar(t *testing.T, modo, alias, servido string, candidatos ...modelgateway.RouteProfile) (agentruntime.ModelClient, *aos520Proxy) {
	t.Helper()
	proxy := &aos520Proxy{servido: servido}
	srv := httptest.NewServer(proxy)
	t.Cleanup(srv.Close)
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(), []modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu"}})
	cfg.RouteProfiles = candidatos
	cfg.Route = modelgateway.RouteGovernance{Mode: modo}
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	opts := []modelgateway.RuntimeAdapterOption{modelgateway.WithPrincipal("tok"), modelgateway.WithRegionBoard("eu", "board-eu"),
		modelgateway.WithProjection(modelgateway.ProjectionNative)}
	if len(candidatos) > 0 {
		set, err := modelgateway.NewRouteProfileSet(candidatos...)
		if err != nil {
			t.Fatal(err)
		}
		opts = append(opts, modelgateway.WithRouteProfileSet(set))
	}
	return modelgateway.NewModelClient(gw, alias, opts...), proxy
}

// aos520Turno dá um turno do MESMO run (o contexto leva o run e o passo, como o runtime os
// escreve), com o perfil fixado dado na vista — o que o loop lá põe a partir do Goal.
func aos520Turno(cliente agentruntime.ModelClient, passo, fixado string) (agentruntime.ModelResponse, error) {
	ctx := agentruntime.ContextWithModelCall(context.Background(), "run-520-retomado", passo)
	return cliente.Call(ctx, agentruntime.PromptView{Materialized: []byte("ola"), ProjectionVersion: modelgateway.NativeProjectionVersion, RouteProfileDigest: fixado})
}

// O RUN FIXADO, RETOMADO COM OUTRO PERFIL, FALHA FECHADO. Com a governação da rota desligada e em
// observação: o turno 1, no nó do perfil A, segue e declara o digest de A; o nó é recriado com o
// perfil B para o MESMO alias; o turno 2 do mesmo run é recusado com a causa, e NENHUM pedido
// chega ao provider.
func TestAOS520_RunFixadoRetomadoComOutroPerfilFalhaFechado(t *testing.T) {
	perfilA, perfilB := aos513Perfil(t, aos520PerfilA), aos513Perfil(t, aos520PerfilB)
	if perfilA.Digest() == perfilB.Digest() {
		t.Fatal("os dois perfis tem o mesmo digest")
	}
	for _, modo := range []string{modelgateway.RouteGovernanceOff, modelgateway.RouteGovernanceObserve} {
		t.Run("governacao "+modo, func(t *testing.T) {
			noA, proxyA := aos520Arrancar(t, modo, aos520Alias, perfilA.ExpectedModel, perfilA)
			primeiro, err := aos520Turno(noA, "step-000001", perfilA.Digest())
			if err != nil {
				t.Fatalf("turno 1, no no do perfil em que o run esta fixado: %v", err)
			}
			// O digest vai declarado para o manifesto com a governação ligada OU desligada.
			if primeiro.RouteProfileDigest != perfilA.Digest() {
				t.Fatalf("turno 1: digest declarado %q; num run fixado tem de ser o do perfil A (%s)", primeiro.RouteProfileDigest, perfilA.Digest())
			}
			querCheck := ""
			if modo == modelgateway.RouteGovernanceObserve {
				querCheck = port.RouteCheckEqual
			}
			if string(primeiro.RouteCheck) != querCheck {
				t.Fatalf("turno 1: check %q; quer %q — a fixacao nao inventa uma comparacao que o gateway nao fez", primeiro.RouteCheck, querCheck)
			}

			// O nó é recriado com o perfil B para o MESMO alias, e o run é retomado.
			noB, proxyB := aos520Arrancar(t, modo, aos520Alias, perfilB.ExpectedModel, perfilB)
			segundo, err := aos520Turno(noB, "step-000002", perfilA.Digest())
			if !errors.Is(err, modelgateway.ErrPinnedRouteProfile) || modelgateway.PinnedRouteProfileCauseOf(err) != modelgateway.PinnedCauseProfileDifferent {
				t.Fatalf("turno 2 com outro perfil: err=%v; quer ErrPinnedRouteProfile com a causa %s", err, modelgateway.PinnedCauseProfileDifferent)
			}
			if !reflect.DeepEqual(segundo, agentruntime.ModelResponse{}) {
				t.Fatalf("o turno recusado devolveu uma resposta: %+v", segundo)
			}
			if n := len(proxyB.pedidos()); n != 0 {
				t.Fatalf("o turno recusado fez %d pedido(s) ao provider; a recusa e ANTES de qualquer pedido", n)
			}
			// A mensagem leva a causa, e nada dos perfis.
			if msg := err.Error(); !strings.Contains(msg, "causa="+modelgateway.PinnedCauseProfileDifferent) || strings.Contains(msg, "modelo-a") || strings.Contains(msg, "modelo-b") || strings.Contains(msg, "sha256:") {
				t.Fatalf("mensagem da recusa: %s", msg)
			}
			// Controlo: reposto o perfil A, o mesmo run segue.
			noA2, _ := aos520Arrancar(t, modo, aos520Alias, perfilA.ExpectedModel, perfilA)
			if out, err := aos520Turno(noA2, "step-000002", perfilA.Digest()); err != nil || out.RouteProfileDigest != perfilA.Digest() {
				t.Fatalf("reposto o perfil A o run tinha de seguir: digest=%q err=%v", out.RouteProfileDigest, err)
			}
			if n := len(proxyA.pedidos()); n != 1 {
				t.Fatalf("pedidos ao no A = %d", n)
			}
		})
	}
}

// O GÉMEO: O RUN SEM PERFIL FIXADO CONTINUA COMO HOJE. É o comportamento que o teste do AOS-519
// prendia, e que se mantém para quem não tem fixação: sem valor na vista (um run de um nó sem
// perfis assinados) ou com a marca de um run re-hospedado que começou sem fixação, o run retomado
// no nó do perfil B segue com o perfil B, e nada falha.
func TestAOS520_RunSemPerfilFixadoContinuaComoHoje(t *testing.T) {
	perfilA, perfilB := aos513Perfil(t, aos520PerfilA), aos513Perfil(t, aos520PerfilB)
	for nome, marca := range map[string]string{"sem valor": "", "marca de nao fixado": agentruntime.RouteProfileDigestUnpinned} {
		t.Run(nome, func(t *testing.T) {
			noA, proxyA := aos520Arrancar(t, modelgateway.RouteGovernanceObserve, aos520Alias, perfilA.ExpectedModel, perfilA)
			primeiro, err := aos520Turno(noA, "step-000001", marca)
			if err != nil {
				t.Fatal(err)
			}
			noB, proxyB := aos520Arrancar(t, modelgateway.RouteGovernanceObserve, aos520Alias, perfilB.ExpectedModel, perfilB)
			segundo, err := aos520Turno(noB, "step-000002", marca)
			if err != nil {
				t.Fatalf("um run sem perfil fixado nao e recusado: %v", err)
			}
			// O turno segue; o manifesto leva o digest de B; a comparação dá `igual` contra o modelo
			// de B; e o pedido levou os parâmetros de B.
			if primeiro.RouteProfileDigest != perfilA.Digest() || segundo.RouteProfileDigest != perfilB.Digest() ||
				string(segundo.RouteCheck) != port.RouteCheckEqual || segundo.Model != perfilB.ExpectedModel {
				t.Fatalf("turno 1 digest %s; turno 2 digest %s check %s modelo %s — o run nao fixado segue com o perfil B (%s)",
					primeiro.RouteProfileDigest, segundo.RouteProfileDigest, segundo.RouteCheck, segundo.Model, perfilB.Digest())
			}
			a, b := proxyA.pedidos(), proxyB.pedidos()
			if len(a) != 1 || len(b) != 1 || !bytes.Contains(a[0], []byte(`"max_tokens":1000`)) || !bytes.Contains(b[0], []byte(`"max_tokens":2000`)) {
				t.Fatalf("os pedidos do run levaram os parametros de cada perfil?\n A: %s\n B: %s", a, b)
			}
		})
	}
}

// INERTE NUM RUN NÃO FIXADO, E SÓ O MANIFESTO MUDA NUM FIXADO. Com a governação da rota desligada:
// um run sem perfil fixado não declara digest nenhum (o turno é o de antes), e o pedido ao
// provider de um run fixado é, byte a byte, o de um run não fixado — a fixação não toca no wire.
func TestAOS520_SemFixacaoOTurnoEODeAntes(t *testing.T) {
	perfilA := aos513Perfil(t, aos520PerfilA)
	respostas := map[string]agentruntime.ModelResponse{}
	corpos := map[string][]byte{}
	for nome, fixado := range map[string]string{"sem valor": "", "marca": agentruntime.RouteProfileDigestUnpinned, "fixado": perfilA.Digest()} {
		no, proxy := aos520Arrancar(t, modelgateway.RouteGovernanceOff, aos520Alias, perfilA.ExpectedModel, perfilA)
		out, err := aos520Turno(no, "step-000001", fixado)
		if err != nil {
			t.Fatalf("%s: %v", nome, err)
		}
		respostas[nome], corpos[nome] = out, proxy.pedidos()[0]
	}
	if !reflect.DeepEqual(respostas["sem valor"], respostas["marca"]) || respostas["sem valor"].RouteProfileDigest != "" {
		t.Fatalf("um run nao fixado, com a governacao desligada, nao declara digest: %q e %q", respostas["sem valor"].RouteProfileDigest, respostas["marca"].RouteProfileDigest)
	}
	// O run fixado difere do não fixado SÓ no digest declarado.
	semDigest := respostas["fixado"]
	semDigest.RouteProfileDigest = ""
	if respostas["fixado"].RouteProfileDigest != perfilA.Digest() || !reflect.DeepEqual(semDigest, respostas["sem valor"]) {
		t.Fatalf("o turno de um run fixado so difere no digest declarado:\n fixado:     %+v\n nao fixado: %+v", respostas["fixado"], respostas["sem valor"])
	}
	if !bytes.Equal(corpos["fixado"], corpos["sem valor"]) || !bytes.Equal(corpos["marca"], corpos["sem valor"]) {
		t.Fatalf("o pedido ao provider mudou com a fixacao:\n fixado:     %s\n nao fixado: %s", corpos["fixado"], corpos["sem valor"])
	}
}

// AS OUTRAS SAÍDAS DE UM RUN FIXADO, todas antes de qualquer pedido: o alias do run deixou de ter
// perfil; a pasta dos perfis foi retirada (o nó voltou à tabela em código, cujo perfil para o
// alias é outro); e o valor fixado não tem a forma de um digest.
func TestAOS520_RunFixado_SemPerfilSemPastaEValorSemForma(t *testing.T) {
	perfilA := aos513Perfil(t, aos520PerfilA)
	daTabela, tem := modelgateway.RouteProfileFor(aos520Alias)
	if !tem || daTabela.Digest() == perfilA.Digest() {
		t.Fatal("o teste precisa de um alias da tabela em codigo com outro digest")
	}
	casos := []struct {
		nome, alias, fixado, causa string
		candidatos                 []modelgateway.RouteProfile
	}{
		{"o alias do run ja nao tem perfil", "alias-que-ninguem-serve", perfilA.Digest(), modelgateway.PinnedCauseModelNotServed, []modelgateway.RouteProfile{perfilA}},
		{"a pasta dos perfis foi retirada", aos520Alias, perfilA.Digest(), modelgateway.PinnedCauseProfileDifferent, nil},
		{"valor fixado sem forma de digest", aos520Alias, "isto nao e um digest", modelgateway.PinnedCauseProfileDifferent, []modelgateway.RouteProfile{perfilA}},
		{"digest truncado", aos520Alias, perfilA.Digest()[:20], modelgateway.PinnedCauseProfileDifferent, []modelgateway.RouteProfile{perfilA}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			no, proxy := aos520Arrancar(t, modelgateway.RouteGovernanceOff, c.alias, perfilA.ExpectedModel, c.candidatos...)
			_, err := aos520Turno(no, "step-000002", c.fixado)
			if !errors.Is(err, modelgateway.ErrPinnedRouteProfile) || modelgateway.PinnedRouteProfileCauseOf(err) != c.causa {
				t.Fatalf("err=%v; quer a causa %s", err, c.causa)
			}
			if n := len(proxy.pedidos()); n != 0 {
				t.Fatalf("a recusa fez %d pedido(s) ao provider", n)
			}
		})
	}
	// Controlo: na tabela em código, um run fixado no digest da tabela segue.
	no, _ := aos520Arrancar(t, modelgateway.RouteGovernanceOff, aos520Alias, daTabela.ExpectedModel)
	if out, err := aos520Turno(no, "step-000001", daTabela.Digest()); err != nil || out.RouteProfileDigest != daTabela.Digest() {
		t.Fatalf("run fixado no perfil da tabela, no no da tabela: digest=%q err=%v", out.RouteProfileDigest, err)
	}
}

// AS CAUSAS SÃO UM CONJUNTO FECHADO: três, com estes nomes; e a conferência só devolve causas dele.
func TestAOS520_CausasEmVocabularioFechado(t *testing.T) {
	quer := []string{"perfil_diferente_do_fixado", "modelo_fixado_nao_servido", "perfil_assinado_sobre_estado_sem_fixacao"}
	if got := modelgateway.PinnedRouteProfileCauses(); !reflect.DeepEqual(got, quer) {
		t.Fatalf("causas = %v; quer %v", got, quer)
	}
	fechado := map[string]bool{}
	for _, c := range quer {
		fechado[c] = true
	}
	perfilA := aos513Perfil(t, aos520PerfilA)
	set, err := modelgateway.NewRouteProfileSet(perfilA)
	if err != nil {
		t.Fatal(err)
	}
	var nulo *modelgateway.RouteProfileSet
	for _, conjunto := range []*modelgateway.RouteProfileSet{nulo, set} {
		for _, alias := range []string{aos520Alias, "k3", "alias-sem-perfil", ""} {
			for _, fixado := range []string{"", agentruntime.RouteProfileDigestUnpinned, perfilA.Digest(), "sha256:" + strings.Repeat("0", 64), "lixo", "NONE", " none"} {
				err := conjunto.CheckPinnedProfile(alias, fixado)
				if err == nil {
					continue
				}
				if !errors.Is(err, modelgateway.ErrPinnedRouteProfile) || !fechado[modelgateway.PinnedRouteProfileCauseOf(err)] {
					t.Fatalf("alias %q fixado %q: recusa fora do vocabulario: %v", alias, fixado, err)
				}
			}
		}
	}
	// Um erro que não é desta conferência não tem causa.
	if c := modelgateway.PinnedRouteProfileCauseOf(errors.New("outro")); c != "" {
		t.Fatalf("causa de um erro alheio = %q", c)
	}
	// Só o vazio e a marca EXACTA dispensam a conferência: uma marca quase igual é um digest que não casa.
	for _, quase := range []string{"NONE", " none", "none "} {
		if err := set.CheckPinnedProfile(aos520Alias, quase); modelgateway.PinnedRouteProfileCauseOf(err) != modelgateway.PinnedCauseProfileDifferent {
			t.Fatalf("o valor %q passou como marca de nao fixado: %v", quase, err)
		}
	}
}
