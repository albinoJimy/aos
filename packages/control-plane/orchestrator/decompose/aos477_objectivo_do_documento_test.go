package decompose_test

// AOS-477 — A HIPÓTESE DO TICKET, MEDIDA: «o `plan_hash` cobre o campo `objective` do
// `PlanDocument`». Cobre — e é por isso que não chega.
//
// O `objective` do documento é o que o MODELO escreveu. O decompositor carimba o `planner_meta`
// (AOS-243) e deixa o resto como veio; o objectivo que o planeador RECEBEU nunca entra no
// documento. Logo o `plan_hash` compromete-se com a paráfrase do modelo, e um documento cujo
// `objective` diz outra coisa tem um hash igualmente válido. É a prova de que o `plan.proposed`
// precisa de um campo próprio para o objectivo recebido (`objective_commitment`).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/decompose"
	"github.com/aos-ref/control-plane/orchestrator/plan"
)

func hashCanonico(t *testing.T, d plan.PlanDocument) string {
	t.Helper()
	raw, err := plan.Encode(d)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	s := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(s[:])
}

func TestAOS477OObjectivoDoDocumentoEDoModeloENaoDoPedido(t *testing.T) {
	m := &fakeModel{reply: forjadoJSON(t)}
	in := stdInput() // Goal = "construir X"
	doc, err := novo(t, m, decompose.WithModelID(modeloTeste)).Decompose(context.Background(), in)
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	// (1) O que o documento leva é o texto do MODELO, não o objectivo recebido.
	if doc.Objective != "meta-objectivo" {
		t.Fatalf("o objective do documento devia ser o que o modelo escreveu, veio %q", doc.Objective)
	}
	if doc.Objective == in.Context.Goal {
		t.Fatal("pré-condição do teste: o modelo tem de ter escrito um objective diferente do recebido")
	}
	// (2) O `plan_hash` cobre esse campo — mudá-lo muda o hash. Mas o documento que o log ancora
	// é o de (1), cujo `objective` não é o do pedido: verificar o objectivo RECEBIDO a partir do
	// `plan_hash` exigiria que o documento o repetisse, e nada o obriga.
	honesto := doc
	honesto.Objective = in.Context.Goal
	if hashCanonico(t, doc) == hashCanonico(t, honesto) {
		t.Fatal("o plan_hash tinha de cobrir o campo objective do documento")
	}
}
