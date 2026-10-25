package modelgateway_test

// AOS-551 — A PROJECÇÃO NATIVA DA 1.8.0 (fase R2 do ADR-043).
//
// A 1.8.0 é a 1.7.0 com o CORPO dos `plan_input` e dos `tool_result` substituído por texto fixo e
// a referência do handle no cabeçalho do segmento. O que esta suite mede não é a lista de versões
// suportadas (isso seria só reler a constante): é que a projecção POE A REFERÊNCIA onde o modelo a
// vê — sem ela a 1.8.0 não funciona, porque é a referência que o modelo tem de escrever de volta
// num argumento — e que NÃO põe o conteúdo em sítio nenhum do pedido.
//
// Até esta fatia a 1.8.0 estava FORA da lista e um run nela ia em TEXTO ÚNICO, perdendo as
// mensagens nativas, as tool calls no campo próprio e o cache de prefixo. O contraste com a 1.3.0
// (que vai e tem de continuar a ir em texto único) é o que prova que o teste mede a lista.

import (
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

const (
	aos551Conteudo = "O SEGREDO QUE NAO PODE ENTRAR NO PEDIDO"
	aos551Entrada  = "O PAYLOAD DA ENTRADA DO PLANO"
)

// aos551Vista180 é um tail da 1.8.0 montado pelo CAMINHO DE PRODUÇÃO: a semente do run (com a
// entrada do plano já selada em handle, como o nó a sela) e um turno com uma call permitida cujo
// corpo é a referência que o produtor pôs lá.
func aos551Vista180(t *testing.T) (agentruntime.PromptView, string, string) {
	t.Helper()
	refEntrada := agentruntime.HandleRefPrefix + agentruntime.MintHandleToken("run-551-proj", "sha256:aa", 1)
	refResultado := agentruntime.HandleRefPrefix + agentruntime.MintHandleToken("run-551-proj", "sha256:bb", 2)
	semente, seq, err := agentruntime.OpenRunTail(agentruntime.AssemblyVersion180, agentruntime.Goal{
		RunID:     "run-551-proj",
		Objective: "Le o documento notes e resume",
		Inputs: []agentruntime.PlanInput{{
			From: "n1_fetch", Output: "doc", Digest: "sha256:aa",
			Content: []byte(aos551Entrada), Handle: refEntrada,
		}},
	})
	if err != nil {
		t.Fatalf("OpenRunTail(1.8.0): %v", err)
	}
	segs, _ := seq.Turn(aos490Passo1, "vou ler o documento",
		[]agentruntime.CapturedToolResult{aos490Permitida("doc_read", `{"doc_id":"notes"}`, refResultado)})
	return aos490Vista(t, agentruntime.AssemblyVersion180, "", append(semente, segs...)), refEntrada, refResultado
}

// A 1.8.0 VAI EM MENSAGENS NATIVAS, e as duas referências chegam ao modelo.
func TestAOS551_Projeccao_A180VaiEmMensagensNativasComAsReferencias(t *testing.T) {
	view, refEntrada, refResultado := aos551Vista180(t)
	msgs := aos490Nativo(t, view)
	// A FORMA é a da 1.7.0, e o ÚLTIMO `user` é a entrada do plano: a 1.8.0 deriva da 1.6.0
	// («pedir antes de ler»), em que os `plan_input` só entram no tail DEPOIS do primeiro turno.
	// É o adiamento do AOS-540 que a 1.8.0 herda — a 1.22 do epic diz que não há ganho em turnos
	// por causa dele —, e vê-lo aqui é a confirmação de que a herança está intacta.
	formas := aos490Formas(msgs)
	quero := []string{"system", "user", "assistant[" + aos490Passo1 + "-tool-1]", "tool:" + aos490Passo1 + "-tool-1", "user"}
	if strings.Join(formas, ",") != strings.Join(quero, ",") {
		t.Fatalf("formas do pedido = %v; quero %v", formas, quero)
	}
	todo := aos551Juntar(msgs)
	// AS REFERÊNCIAS ESTÃO LÁ, cada uma no rótulo do SEU segmento e com o token exacto que o nó
	// cunhou. É a razão de existir da projecção nesta versão: sem a referência no pedido, o
	// modelo não tem o que escrever num argumento e a 1.8.0 não anda.
	//
	// Confere-se o par `rótulo=referência`, e não o prefixo solto: o CORPO fixo de cada segmento
	// também nomeia `aos-handle/v1:` — é o texto que diz ao modelo como usar a referência —, pelo
	// que contar o prefixo contava a explicação e não o rótulo.
	for rotulo, ref := range map[string]string{
		agentruntime.PlanInputHandleLabel:  refEntrada,
		agentruntime.ToolResultHandleLabel: refResultado,
	} {
		if n := strings.Count(todo, rotulo+"="+ref); n != 1 {
			t.Fatalf("o rótulo %s=%s aparece %d vezes no pedido, quero 1:\n%s", rotulo, ref, n, todo)
		}
	}
	// E O CONTEÚDO NÃO ESTÁ. Nem o da entrada do plano nem o do resultado da tool.
	for _, segredo := range []string{aos551Conteudo, aos551Entrada} {
		if strings.Contains(todo, segredo) {
			t.Fatalf("o conteúdo %q entrou no pedido — a separação está desfeita:\n%s", segredo, todo)
		}
	}
}

// CONTRASTE: a 1.3.0 continua a ir em TEXTO ÚNICO. Sem isto, o teste acima passaria com a lista de
// versões a dizer «sim» a tudo.
func TestAOS551_Projeccao_A130ContinuaEmTextoUnico(t *testing.T) {
	view := aos490Vista(t, agentruntime.AssemblyVersion130, "", []agentruntime.TailSegment{
		aos490Objectivo("Le o documento notes"),
	})
	req, _, err := aos490Pedir(t, view, modelgateway.WithProjection(modelgateway.ProjectionNative))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
		t.Fatalf("a 1.3.0 saiu em mensagens nativas: %v", aos490Formas(req.Messages))
	}
}

// aos551Juntar concatena o conteúdo de todas as mensagens do pedido.
func aos551Juntar(msgs []port.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Content)
		b.WriteString("\n")
		for _, tc := range m.ToolCalls {
			b.WriteString(tc.Function.Name)
			b.WriteString(tc.Function.Arguments)
			b.WriteString("\n")
		}
	}
	return b.String()
}
