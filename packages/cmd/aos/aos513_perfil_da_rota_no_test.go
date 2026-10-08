package main

// AOS-513 — O PERFIL DA ROTA NO NÓ: a versão da projecção fica fixada no run, os 4xx dos turnos
// com parâmetros têm nome, e nenhuma origem que não seja o perfil declara parâmetros.
//
// A prova de que o nó é INERTE com os perfis de hoje é a dos goldens medidos na base
// (aos505_goldens_da_base_test.go), que os testes do AOS-505, do AOS-507 e do AOS-514 continuam
// a exigir sobre o nó composto: os pedidos ao provider, os `turn.recorded`, as capturas e o
// /metrics. Aqui prova-se o que este ticket acrescenta.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// A VERSÃO DA PROJECÇÃO FICA PRESA AO RUN. Um run novo numa rota cujo perfil não declara versão —
// todos os de hoje — fica sem versão fixada, e o seu registo de retoma tem os bytes de sempre. Um
// run fixado numa versão leva-a para o registo e traz-a de volta na retoma. Um run que começou
// sem versão volta com a marca de que não fixou nenhuma, e a hospedagem não lha fixa a meio.
func TestAOS513_No_AVersaoDaProjeccaoFicaPresaAoRun(t *testing.T) {
	for _, p := range modelgateway.RouteProfiles() {
		goal := fixarProjeccao(agentruntime.Goal{Model: agentruntime.ModelConfig{ModelID: p.Requested}})
		if goal.ProjectionVersion != "" {
			t.Fatalf("o perfil %q de hoje fixou uma versao da projeccao (%q): a rota de producao nao muda sem o dono assinar outro perfil", p.Requested, goal.ProjectionVersion)
		}
	}
	base := agentruntime.Goal{RunID: "run-513", Objective: "o", CompletionMode: agentruntime.CompletionOff, Model: agentruntime.ModelConfig{ModelID: "gpt-4o"}}
	semVersao, err := resumeRecordFromGoal(fixarProjeccao(base))
	if err != nil {
		t.Fatal(err)
	}
	cru, err := json.Marshal(semVersao)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cru), "ProjectionVersion") {
		t.Fatalf("o registo de retoma de um run sem versao fixada ganhou o campo: %s", cru)
	}
	retomado := semVersao.GoalWith("cred")
	if retomado.ProjectionVersion != agentruntime.ProjectionVersionUnpinned {
		t.Fatalf("um run que comecou sem versao tem de voltar com a marca; veio %q", retomado.ProjectionVersion)
	}
	if again := fixarProjeccao(retomado); again.ProjectionVersion != agentruntime.ProjectionVersionUnpinned {
		t.Fatalf("a hospedagem de uma retoma fixou uma versao a meio do run: %q", again.ProjectionVersion)
	}
	// A VERSÃO EFECTIVAMENTE PROJECTADA (revisão, F2). A imagem nova declara 1.3.0 no perfil da
	// rota; o nó está em 1.2.0. O run retomado, que deu os seus turnos na 1.2.0, continua nela; um
	// run NOVO vai na do perfil.
	set, err := modelgateway.NewRouteProfileSet(modelgateway.RouteProfile{Requested: "gpt-4o", ExpectedModel: "openai/k3",
		WireClass: modelgateway.WireOpenAIChat, Capabilities: []string{modelgateway.CapabilityTools}, ProjectionVersion: modelgateway.NativeProjectionVersion130})
	if err != nil {
		t.Fatal(err)
	}
	projectada := func(goal agentruntime.Goal) string {
		asm, err := agentruntime.NewPromptAssemblerFor(agentruntime.AssemblyVersion140, "s", nil)
		if err != nil {
			t.Fatal(err)
		}
		view := asm.Assemble(1, []agentruntime.TailSegment{{Kind: agentruntime.TailObjective, Content: []byte("o")}})
		view.ProjectionVersion = agentruntime.ProjectionVersionForView(goal.ProjectionVersion)
		out, err := modelgateway.NewModelClient(aos513Gateway{}, "gpt-4o", modelgateway.WithProjection(modelgateway.ProjectionNative),
			modelgateway.WithProjectionVersion(modelgateway.NativeProjectionVersion120), modelgateway.WithRouteProfileSet(set)).Call(context.Background(), view)
		if err != nil {
			t.Fatal(err)
		}
		return out.ProjectionVersion
	}
	if v := projectada(retomado); v != "1.2.0" {
		t.Fatalf("o run retomado sem versao fixada foi projectado na %q: mudou de projeccao a meio (queria a do no, 1.2.0)", v)
	}
	if v := projectada(base); v != "1.3.0" {
		t.Fatalf("um run novo vai na versao do perfil; foi na %q", v)
	}
	// A re-escrita do registo na retoma tem os bytes do original.
	reescrito, err := resumeRecordFromGoal(fixarProjeccao(retomado))
	if err != nil {
		t.Fatal(err)
	}
	if outra, _ := json.Marshal(reescrito); string(outra) != string(cru) {
		t.Fatalf("a re-escrita do registo na retoma mudou os bytes:\n antes:  %s\n depois: %s", cru, outra)
	}
	// Um run FIXADO: a versão vai para o registo, volta na retoma e não é trocada.
	fixado := base
	fixado.ProjectionVersion = modelgateway.NativeProjectionVersion120
	rec, err := resumeRecordFromGoal(fixarProjeccao(fixado))
	if err != nil {
		t.Fatal(err)
	}
	if rec.ProjectionVersion != "1.2.0" || rec.GoalWith("cred").ProjectionVersion != "1.2.0" || fixarProjeccao(rec.GoalWith("cred")).ProjectionVersion != "1.2.0" {
		t.Fatalf("a versao fixada nao sobreviveu a retoma: registo %q, retomado %q", rec.ProjectionVersion, rec.GoalWith("cred").ProjectionVersion)
	}
}

// aos513Gateway é um gateway que responde sempre um turno final.
type aos513Gateway struct{ port.Gateway }

func (aos513Gateway) Chat(context.Context, port.ChatRequest) (port.ChatResponse, error) {
	return port.ChatResponse{Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "ok"}, FinishReason: "stop"}}}, nil
}

// OS 4XX DOS TURNOS COM PARÂMETROS: uma série por rota com parâmetros e por código; uma rota ou um
// código fora dos conjuntos não conta; e com a tabela de perfis de hoje não há série nenhuma.
func TestAOS513_No_ContadoresDosParametrosRecusados(t *testing.T) {
	if len(parametrosRecusados.rotas) != 0 {
		t.Fatalf("a tabela de perfis de hoje nao tem rotas com parametros; os contadores do processo tem %v", parametrosRecusados.rotas)
	}
	c := novosContadoresDeParametros([]string{"rota-a"})
	c.observar(modelgateway.RouteParamsRejection{Route: "rota-a", Status: "400"})
	c.observar(modelgateway.RouteParamsRejection{Route: "rota-a", Status: "400"})
	c.observar(modelgateway.RouteParamsRejection{Route: "rota-a", Status: modelgateway.RouteParamsRejectionOther})
	c.observar(modelgateway.RouteParamsRejection{Route: "rota-desconhecida", Status: "400"})
	c.observar(modelgateway.RouteParamsRejection{Route: "rota-a", Status: "texto do provider"})
	if c.lido("rota-a", "400") != 2 || c.lido("rota-a", "4xx") != 1 || c.lido("rota-desconhecida", "400") != 0 || c.lido("rota-a", "texto do provider") != 0 {
		t.Fatalf("contagem errada: 400=%d 4xx=%d", c.lido("rota-a", "400"), c.lido("rota-a", "4xx"))
	}
	if !reflect.DeepEqual(c.codigos, modelgateway.RouteParamsRejectionStatuses()) {
		t.Fatalf("os codigos nao sao o vocabulario do gateway: %v", c.codigos)
	}
	var nulo *contadoresDeParametros
	nulo.observar(modelgateway.RouteParamsRejection{Route: "x", Status: "400"})
	if nulo.lido("x", "400") != 0 {
		t.Fatal("contadores nulos contaram")
	}
}

// O CORPO DE UM PEDIDO HTTP NÃO DECLARA PARÂMETROS: `POST /runs` recusa um corpo com chaves que
// não são do pedido de run, e o pedido de run não tem campo de parâmetros do modelo.
func TestAOS513_No_OPedidoHTTPNaoDeclaraParametros(t *testing.T) {
	h := &apiHandler{cfg: apiConfig{maxBodyBytes: 1 << 16}}
	for _, corpo := range []string{
		`{"run_id":"r","objective":"o","principal_nhi":"p","thinking":{"type":"disabled"}}`,
		`{"run_id":"r","objective":"o","principal_nhi":"p","reasoning_effort":"high"}`,
		`{"run_id":"r","objective":"o","principal_nhi":"p","params":{"max_tokens":9}}`,
		`{"run_id":"r","objective":"o","principal_nhi":"p","model":{"params":{"thinking":"disabled"}}}`,
		`{"run_id":"r","objective":"o","principal_nhi":"p","projection_version":"1.2.0"}`,
		`{"run_id":"r","objective":"o","principal_nhi":"p","devolver":"obrigatorio"}`,
	} {
		var req submitRequest
		r := httptest.NewRequest(http.MethodPost, "/runs", strings.NewReader(corpo))
		if status, ok := h.decodeJSON(httptest.NewRecorder(), r, &req); ok || status != http.StatusBadRequest {
			t.Fatalf("o corpo %s tinha de ser recusado com 400; veio ok=%v status=%d", corpo, ok, status)
		}
	}
	campos := reflect.TypeOf(submitRequest{})
	for i := 0; i < campos.NumField(); i++ {
		nome := strings.ToLower(campos.Field(i).Name + " " + string(campos.Field(i).Tag))
		for _, proibido := range []string{"thinking", "reasoning", "max_tokens", "params", "projection", "devolver"} {
			if strings.Contains(nome, proibido) {
				t.Fatalf("o pedido de run ganhou um campo por onde um parametro do modelo entra: %s", campos.Field(i).Name)
			}
		}
	}
}
