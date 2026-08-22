package main

import (
	"context"
	"testing"

	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------------------------
// A RECONCILIAÇÃO SOBREVIVE AO RESTART.
//
// Segunda metade do achado 1.7, e a que o relatório mede explicitamente:
//
//	após restart COM re-hidratação: tentado outra vez? false
// ---------------------------------------------------------------------------------------------

// cadeiaDeRetencao sela uma sequência de (tipo, record_id) na partição de retenção.
func cadeiaDeRetencao(t *testing.T, factos ...[2]string) audit.Store {
	t.Helper()
	store := audit.NewMemStore()
	for i, f := range factos {
		if _, err := store.Append(context.Background(), audit.AuditRecord{
			Partition:  retentionPartition,
			Decision:   audit.DecisionAllow,
			Capability: "retention:teste",
			Resource:   audit.Resource{Type: f[0], Value: f[1]},
		}); err != nil {
			t.Fatalf("selar facto %d: %v", i, err)
		}
	}
	return store
}

func TestORestauroRepoeOsRegistosPorReconciliar(t *testing.T) {
	store := cadeiaDeRetencao(t,
		[2]string{audit.RetentionExpiredEventType, "rec-a"},
		[2]string{audit.RetentionExpireUnconfirmedEventType, "rec-a"},
	)
	set, n, err := restoreReconciliation(context.Background(), store, retentionPartition)
	if err != nil {
		t.Fatalf("restoreReconciliation: %v", err)
	}
	if n != 1 || !set.Pending("rec-a") {
		t.Fatalf("esperava rec-a POR RECONCILIAR (n=%d) — apos um deploy, a destruicao falhada "+
			"nunca mais e tentada e a cadeia fica sozinha a afirmar que aconteceu", n)
	}
}

// TestUmaReconciliacaoCONFIRMADALimpaAPendencia — o alarme sabe desligar-se.
func TestUmaReconciliacaoCONFIRMADALimpaAPendencia(t *testing.T) {
	store := cadeiaDeRetencao(t,
		[2]string{audit.RetentionExpireUnconfirmedEventType, "rec-b"},
		[2]string{audit.RetentionExpireConfirmedEventType, "rec-b"},
	)
	_, n, err := restoreReconciliation(context.Background(), store, retentionPartition)
	if err != nil {
		t.Fatalf("restoreReconciliation: %v", err)
	}
	if n != 0 {
		t.Errorf("uma reconciliacao CONFIRMADA devia limpar a pendencia; n=%d", n)
	}
	// CONTROLO DA ORDEM: o inverso NÃO limpa. Sem este ramo, um restauro que ignorasse a ordem
	// (ou que só olhasse para o primeiro facto) passaria acima.
	inversa := cadeiaDeRetencao(t,
		[2]string{audit.RetentionExpireConfirmedEventType, "rec-c"},
		[2]string{audit.RetentionExpireUnconfirmedEventType, "rec-c"},
	)
	if _, n2, _ := restoreReconciliation(context.Background(), inversa, retentionPartition); n2 != 1 {
		t.Errorf("com a falha DEPOIS da confirmacao a pendencia devia ficar; n=%d", n2)
	}
}

// TestUmRetentionExpiredSozinhoNaoGeraPendencia é o ramo que impede o restauro de acusar TODOS os
// registos já expirados.
//
// O `retention.expired` existe nos DOIS casos — no que correu bem e no que falhou — e é
// precisamente por ser indistinguível que o achado 1.7 existia. Se ele decidisse, cada nó
// arrancaria a re-tentar o sink para todo o histórico de expirações.
func TestUmRetentionExpiredSozinhoNaoGeraPendencia(t *testing.T) {
	store := cadeiaDeRetencao(t,
		[2]string{audit.RetentionExpiredEventType, "rec-d"},
		[2]string{audit.RetentionExpiredEventType, "rec-e"},
	)
	_, n, err := restoreReconciliation(context.Background(), store, retentionPartition)
	if err != nil {
		t.Fatalf("restoreReconciliation: %v", err)
	}
	if n != 0 {
		t.Errorf("expiracoes bem-sucedidas geraram %d pendencia(s) — o no arrancaria a re-tentar "+
			"o sink para todo o historico", n)
	}
}

// TestOArranqueLIGAAReconciliacaoAoJob é a CABLAGEM, e é a décima terceira vez que este padrão
// aparece no repositório.
//
// Os testes acima exercem `restoreReconciliation` DIRECTAMENTE. Uma mutação que removesse o
// `audit.WithExpirationReconciliation(...)` do bootstrap passava em todos: a função continua
// correcta e o job nunca recebe o conjunto.
func TestOArranqueLIGAAReconciliacaoAoJob(t *testing.T) {
	node := noRealComRetencao(t)
	if node.ExpirationJob == nil {
		t.Fatal("ExpirationJob nao composto — o cenario nao esta montado")
	}
	if !node.ExpirationJob.ReconciliationComposed() {
		t.Error("o job NAO recebeu o conjunto de reconciliacao — a funcao de restauro pode estar " +
			"certa e ninguem lha passar; uma destruicao falhada nunca seria re-tentada")
	}
}
