package modelgateway_test

import (
	"context"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// TestAOS336_SemRecorderAMarcaChegaAoRuntime é a AC4 ponta-a-ponta, e é a razão de este ticket
// existir separado do AOS-321.
//
// Os testes acima provam a PROJECÇÃO isolada; este prova a TRAVESSIA na composição que produz o
// defeito: um gateway SEM contabilidade de custo — o que um nó tem quando a tabela de preços não
// cobre o par (modelo, região) configurado. Nessa composição o gateway serve a resposta na mesma
// (é o limite declarado do AOS-321, fixado por `TestSemContabilidadeUsageAusenteContinuaAServir`),
// e era exactamente aí que o `turn.recorded` recebia zeros para uma chamada não medida.
//
// Sem este teste, o AC4 continuaria a valer «enquanto houver recorder composto» — que é a frase
// que este ticket foi aberto para apagar.
func TestAOS336_SemRecorderAMarcaChegaAoRuntime(t *testing.T) {
	t.Parallel()
	gw := newGateway(t, &adaptadorDeWire{corpoChat: corpoSemUsage})
	cli := modelgateway.NewModelClient(gw, "m")

	resp, err := cli.Call(context.Background(), agentruntime.PromptView{
		Turn:         1,
		Materialized: []byte("oi"),
	})
	if err != nil {
		t.Fatalf("sem contabilidade composta a chamada TEM de servir: %v", err)
	}
	if !resp.Usage.Ausente {
		t.Fatalf("a marca nao chegou ao runtime: %+v — o turn.recorded volta a gravar zeros para uma chamada NAO MEDIDA", resp.Usage)
	}
	if resp.Usage.Definido() {
		t.Errorf("Definido() = true sem recorder e sem usage: %+v", resp.Usage)
	}
	// CONTROLO: a resposta continua a ser servida e util. Se a marca custasse a resposta,
	// ter-se-ia trocado um zero silencioso por um caminho de modelo partido.
	if resp.Text == "" {
		t.Error("a resposta perdeu o conteudo")
	}
}
