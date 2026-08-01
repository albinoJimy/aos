package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	audit "github.com/aos-ref/platform/audit"
)

// AOS-221 (completude CA-2) — a EXPIRAÇÃO por TTL (POST /dsar/expire) é um crypto-shred REAL da
// KEK por-titular, no MESMO molde do POST /dsar/erase; por isso o nó re-encadeia e VERIFICA a
// hash-chain do WORM PÓS-SHRED também neste caminho (paridade com dsar.go). Antes desta imposição,
// a doc-comment de VerifyWORM alegava a ramificação AOS-213 mas o handleExpire NÃO chamava a
// verificação — over-claim. Este teste FALSIFICÁVEL fecha essa lacuna nos dois sentidos.

// armableWORM é um [audit.Store]+[audit.PartitionLister] de teste que delega tudo a um MemStore
// real, mas que — quando ARMADO — devolve, em cada Read, um primeiro registo com o Capability
// mutado SEM recalcular o EntryHash. O re-encadeamento de [audit.VerifyStore] recomputa o hash e
// deteta a divergência (TamperMutation), exactamente o vector "cadeia partida" que a verificação
// pós-shred impõe. O Append NÃO é tocado, pelo que o ExpirationJob.Run (que só sela, nunca lê o
// WORM) continua a suceder — isolando a asserção no facto de a VERIFICAÇÃO ser MESMO chamada.
type armableWORM struct {
	*audit.MemStore
	broken atomic.Bool
}

func (w *armableWORM) Read(ctx context.Context, partition string, from, to uint64) ([]audit.AuditRecord, error) {
	recs, err := w.MemStore.Read(ctx, partition, from, to)
	if err != nil || !w.broken.Load() || len(recs) == 0 {
		return recs, err
	}
	// Adultera o CONTEÚDO do primeiro registo sem tocar no EntryHash armazenado: o verificador
	// recomputa ComputeEntryHash(prev, rec) e diverge ⇒ TamperMutation (o CRC de framing NÃO
	// veria isto — é o vector de AOS-221).
	tampered := recs[0]
	tampered.Capability = tampered.Capability + "-TAMPERED"
	recs[0] = tampered
	return recs, nil
}

// newRetentionNodeWithWORM é o newRetentionNode com um WORM INJECTADO (armableWORM) em vez do
// FileStore durável — para poder ARMAR a adulteração pós-shred sob teste. Mantém tudo o resto
// igual: execução durável (ES em disco para o capturer cifrar por-titular), soberania de leitura
// (para as rotas autenticarem) e uma política que expira pii_operational com relógio muito à frente.
func newRetentionNodeWithWORM(t *testing.T, worm audit.Store) *Node {
	t.Helper()
	dir := t.TempDir()
	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORM = worm // INJECTADO: precede o WORMPath; o ciclo de vida é do teste
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.BoardRegions = map[string]string{govBoard: govRegion}
	rc, err := audit.NewRetentionConfig("1.0.0", map[audit.DataClass]time.Duration{
		audit.ClassPIIOperational: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewRetentionConfig: %v", err)
	}
	cfg.Retention = rc
	cfg.RetentionClock = retentionFarFuture
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (retention, WORM injectado): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node
}

// TestExpireRoute_PostShredVerifiesWORM_FailClosed prova, nos DOIS sentidos, que POST /dsar/expire
// re-encadeia e verifica a hash-chain do WORM DEPOIS do crypto-shred da expiração:
//
//	(POSITIVO, não-vácuo) com o WORM íntegro, uma expiração REAL devolve 200 — a verificação
//	pós-shred passa e não há falso-positivo.
//	(NEGATIVO) armada a adulteração (cadeia partida no momento da verificação), a MESMA rota
//	devolve 500 fail-closed. Sem a imposição (se handleExpire não chamasse VerifyWORM), esta
//	segunda passagem — idempotente, Run sem erro — devolveria 200: é a FALHA-ANTES.
//	(RESTAURO) desarmada, a rota volta a 200 — o 500 foi da adulteração, não de um estado preso.
//
// Correr com -race.
func TestExpireRoute_PostShredVerifiesWORM_FailClosed(t *testing.T) {
	fake := &armableWORM{MemStore: audit.NewMemStore()}
	node := newRetentionNodeWithWORM(t, fake)
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}

	const subject = "nhi:agent-expire-tamper"
	captureSynthetic(t, node, subject, "run-expire-tamper", "conteudo a expirar: TAMPER-221", "outTamper")

	// (POSITIVO) WORM íntegro ⇒ a expiração REAL sucede e a verificação pós-shred passa (200).
	ok := postReq(h, "/dsar/expire", nil, govHeaders())
	if ok.Code != http.StatusOK {
		t.Fatalf("expire com WORM integro devia dar 200, veio %d (%s)", ok.Code, ok.Body.String())
	}
	var rep expireResponse
	if err := json.Unmarshal(ok.Body.Bytes(), &rep); err != nil {
		t.Fatalf("resposta expire nao descodifica: %v", err)
	}
	if rep.Expired < 1 {
		t.Fatalf("expire integro devia expirar >=1 (nao-vacuo), veio %+v", rep)
	}

	// (NEGATIVO) ARMA a adulteração: a cadeia parte-se no momento em que VerifyWORM re-encadeia.
	// A 2a passagem é idempotente (Run devolve 0 expirados, sem erro) — logo o ÚNICO caminho para
	// um 500 é a VERIFICAÇÃO pós-shred. É a prova de que ela é MESMO chamada.
	fake.broken.Store(true)
	broken := postReq(h, "/dsar/expire", nil, govHeaders())
	if broken.Code != http.StatusInternalServerError {
		t.Fatalf("expire com hash-chain adulterada pos-shred devia dar 500 fail-closed, veio %d (%s)",
			broken.Code, broken.Body.String())
	}

	// (RESTAURO) desarmada, a rota volta a 200 — confirma que o 500 foi da adulteração detectada.
	fake.broken.Store(false)
	restored := postReq(h, "/dsar/expire", nil, govHeaders())
	if restored.Code != http.StatusOK {
		t.Fatalf("apos desarmar, expire devia voltar a 200, veio %d (%s)", restored.Code, restored.Body.String())
	}
}
