package main

import (
	"net/http"
	"testing"

	audit "github.com/aos-ref/platform/audit"
)

// TestAOS322_CustodiaDeReferenciaNaoTemPendenciaPossivel FIXA por teste o raciocínio que
// a discovery da EPIC-23 apurou — e que o enunciado original do AOS-322 tinha ao contrário.
//
// A hipótese de partida era que o `/readyz` ficava mudo por defeito: o `InMemoryKeyVault`
// não implementa `readinessProber`, logo uma destruição de KEK por confirmar nunca poria o
// nó unready. Os factos estavam certos; a conclusão não.
//
// O QUE ESTE TESTE FIXA: com a custódia de referência as TRÊS ausências — confirmador do
// fluxo DSAR, `readinessProber` e `shredPendingReporter` — são coerentes entre si e
// CORRECTAS, porque `Delete` é um apagamento em memória que não tem como falhar. Um
// `/readyz` verde neste modo significa «nada a confirmar», não «confirmado».
//
// PORQUE EXISTE. Sem ele, uma alteração futura que fizesse o `InMemoryKeyVault` implementar
// a porta de confirmação — ou que tornasse o seu `Delete` falível — passaria despercebida,
// e a coerência que hoje justifica as ausências deixaria de valer sem que nada ficasse
// vermelho. O teste é do RACIOCÍNIO, não do sintoma.
func TestAOS322_CustodiaDeReferenciaNaoTemPendenciaPossivel(t *testing.T) {
	t.Parallel()
	vault := audit.NewInMemoryKeyVault(nil)

	// (1) As três ausências, asseridas explicitamente.
	if _, ok := any(vault).(readinessProber); ok {
		t.Error("o vault de referencia NAO devia implementar readinessProber: nao ha estado de saude que reportar")
	}
	if _, ok := shredPendingOf(vault); ok {
		t.Error("o vault de referencia NAO devia reportar pendencia de shred: nao ha pendencia possivel")
	}
	if c := confirmadorDeShredDe(vault); c != nil {
		t.Error("o vault de referencia NAO devia dar confirmador ao fluxo DSAR: nao sabe responder, e nao deve inventar resposta")
	}

	// (2) A PREMISSA das três ausências, testada e não assumida: o apagamento é efectivo
	// e não tem canal de falha. Se isto deixar de ser verdade, as ausências acima passam
	// a ser um buraco — e é este assert que o denuncia.
	const titular = "titular-aos322"
	if _, _, err := vault.EnsureKey(titular); err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	ref := audit.KeyRefFor(titular)
	if _, ok := vault.Key(ref); !ok {
		t.Fatal("a KEK devia existir depois de EnsureKey")
	}
	vault.Delete(titular) // sem canal de erro, por desenho da porta
	if _, ok := vault.Key(ref); ok {
		t.Fatal("a KEK devia ter desaparecido: e este apagamento infalivel que torna a confirmacao desnecessaria")
	}
	vault.Delete(titular) // idempotente: apagar o que não existe é no-op
}

// TestAOS322_ReadyzVerdeComCustodiaDeReferencia prova o corolário observável do teste
// acima, no sítio onde o operador o lê: com a custódia de referência o `/readyz` responde
// 200 e a custódia não é sondada.
func TestAOS322_ReadyzVerdeComCustodiaDeReferencia(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	node.DSARVault = audit.NewInMemoryKeyVault(nil)

	_, h := newAPI(t, node)
	code, body := getProbe(h, "/readyz")
	if code != http.StatusOK {
		t.Fatalf("/readyz devia ser 200 com a custodia de referencia (nao ha pendencia possivel), veio %d (%v)", code, body)
	}
}
