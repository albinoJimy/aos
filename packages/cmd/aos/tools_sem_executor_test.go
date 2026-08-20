package main

import (
	"bytes"
	"strings"
	"testing"

	sandbox "github.com/aos-ref/substrate/sandbox"
)

// ---------------------------------------------------------------------------
// UMA TOOL OFERECIDA AO MODELO E SEM EXECUTOR TEM DE SER DECLARADA.
//
// Encontrado em produção a 2026-08-20: o banner dizia «1 tool(s) [doc_read] ligadas ao driver» e
// o manifesto oferecia DUAS — `doc_read` e `web_post`. A segunda não tem bloco `sandbox`, logo o
// nó não regista despacho para ela e cada chamada morre no caminho de recusa, depois de o modelo
// gastar um turno a tentá-la.
//
// A PRIMEIRA CORRECÇÃO QUE ESCREVI ESTAVA ERRADA: recusar o arranque. O teste
// `TestRegistriesDoRepoArrancam` apanhou-me, e ao ler a `demo-pdp-tool-deny.sh` percebi porquê —
// aquela tool é oferecida SEM EXECUTOR DE PROPÓSITO, para demonstrar que «o modelo NÃO executa uma
// tool só porque lha ofereceram». Recusar o arranque destruiria a propriedade que a demonstração
// existe para mostrar.
//
// O que faltava não era uma recusa: era a DECLARAÇÃO.
// ---------------------------------------------------------------------------

// TestToolsSemExecutorSaoNomeadas — as oferecidas sem despacho aparecem, e as ligadas não.
func TestToolsSemExecutorSaoNomeadas(t *testing.T) {
	specs := []modelToolSpec{
		{Name: "doc_read", Capability: "cap:fs.read", Sandbox: &sandboxMapping{Command: "read"}},
		{Name: "web_post", Capability: "cap:http.post"},
	}
	bindings := map[string]sandbox.SandboxBinding{"doc_read": {Command: "read"}}

	orfas := toolsSemExecutor(specs, bindings)
	if len(orfas) != 1 || orfas[0] != "web_post" {
		t.Fatalf("orfas = %v, quero exactamente [web_post]", orfas)
	}
}

// TestTodasComExecutorNaoProduzemRuido é o CONTROLO.
//
// Sem ele, uma implementação que devolvesse SEMPRE a lista toda passaria no teste acima — e o
// banner passaria a acusar de órfã cada tool que corre, que é a maneira mais rápida de ensinar
// alguém a ignorar a linha.
func TestTodasComExecutorNaoProduzemRuido(t *testing.T) {
	specs := []modelToolSpec{
		{Name: "doc_read", Capability: "cap:fs.read", Sandbox: &sandboxMapping{Command: "read"}},
		{Name: "doc_write", Capability: "cap:fs.write", Sandbox: &sandboxMapping{Command: "write"}},
	}
	bindings := map[string]sandbox.SandboxBinding{
		"doc_read":  {Command: "read"},
		"doc_write": {Command: "write"},
	}
	if orfas := toolsSemExecutor(specs, bindings); len(orfas) != 0 {
		t.Errorf("nenhuma tool devia ser dada como orfa: %v", orfas)
	}
}

// TestManifestoDeProducaoTemUmaToolSemExecutor documenta o FACTO observado, contra o ficheiro real
// que o repositório entrega — e falha se ele mudar sem que este texto mude.
//
// Não é uma acusação: pode ser deliberado (ver a demo de defesa-em-profundidade). É um registo de
// que o operador de produção está a oferecer ao modelo uma tool que não corre, para que a decisão
// seja CONSCIENTE e não herdada.
func TestManifestoDeProducaoTemUmaToolSemExecutor(t *testing.T) {
	t.Setenv("AOS_MODEL_TOOLS", "../../../deploy/server/model-tools/tools.json")
	specs, err := readModelToolSpecs()
	if err != nil {
		t.Fatalf("o manifesto de producao nao carrega: %v", err)
	}
	bindings, err := sandboxBindingsFromEnv()
	if err != nil {
		t.Fatalf("bindings: %v", err)
	}
	orfas := toolsSemExecutor(specs, bindings)
	if len(orfas) != 1 || orfas[0] != "web_post" {
		t.Errorf("o manifesto de producao mudou: orfas = %v (era exactamente [web_post]). "+
			"Se a mudanca foi deliberada, actualize este teste E o texto que explica porque uma "+
			"tool e oferecida sem executor", orfas)
	}
}

// TestBannerDeclaraAsToolsSemExecutor é o teste da LIGAÇÃO.
//
// Os testes acima provam o que `toolsSemExecutor` calcula; nenhum prova que o banner o DIZ. Uma
// mutação que apagasse a linha do log passava em todos — e o operador continuava sem saber que o
// modelo está a ser convidado a chamar algo que não corre.
//
// É a quinta vez neste dia que a unidade estava testada e a ligação não. Passou a haver sempre
// uma mutação de cablagem.
func TestBannerDeclaraAsToolsSemExecutor(t *testing.T) {
	t.Setenv("AOS_MODEL_TOOLS", "../../../deploy/server/model-tools/tools.json")
	t.Setenv("AOS_MODEL_TOOLS_REGISTER", "")

	var banner bytes.Buffer
	node, err := Bootstrap(t.Context(), tnBaseConfig(), &banner)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer node.Close()

	saida := banner.String()
	if !strings.Contains(saida, "NAO TEM EXECUTOR") {
		t.Errorf("o banner NAO declara as tools oferecidas sem executor — o operador nao sabe que o "+
			"modelo vai gastar turnos a tentar uma tool que nunca corre:\n%s", saida)
	}
	if !strings.Contains(saida, "web_post") {
		t.Errorf("o banner nao NOMEIA a tool sem executor: %s", saida)
	}
	// CONTROLO: a linha das tools LIGADAS continua lá. Sem este ramo, uma correcção que trocasse
	// uma linha pela outra passaria — e perder-se-ia a declaração de qual driver executa o quê.
	if !strings.Contains(saida, "ligadas ao driver") {
		t.Error("o banner perdeu a linha das tools LIGADAS")
	}
}
