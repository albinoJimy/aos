package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/pdp"
	audit "github.com/aos-ref/platform/audit"
)

// AOS-310 — o arranque sela `policy.changed` quando a política em vigor difere do último selo.
//
// `PDP.Reload` não tem chamador nem rota; trocar de política é reiniciar. Antes, esse caminho
// não deixava changelog nenhum na hash-chain — só o `policy_version` de cada mediação, que diz
// QUE mudou mas não QUANDO, QUEM, nem o hash antigo/novo.

const aos310BundleDir = "../../control-plane/pdp/policies"

func abrirBundleDeReferencia(t *testing.T) *pdp.PDP {
	t.Helper()
	p, err := pdp.Open(aos310BundleDir)
	if err != nil {
		t.Fatalf("pdp.Open(%q) — bundle assinado committado: %v", aos310BundleDir, err)
	}
	if p.Version() == "" || p.ContentHash() == "" {
		t.Fatalf("bundle sem versao/hash: %q / %q", p.Version(), p.ContentHash())
	}
	return p
}

// TestAOS310_PrimeiroArranqueSelaATransicaoEOSegundoNao — partição vazia ⇒ sela (old ""), mesma
// política outra vez ⇒ nada (idempotente), com o registo a transportar versão e hash.
func TestAOS310_PrimeiroArranqueSelaATransicaoEOSegundoNao(t *testing.T) {
	p := abrirBundleDeReferencia(t)
	worm := audit.NewMemStore()
	agora := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	st, err := provisionPolicyChangelog(context.Background(), worm, p, agora)
	if err != nil {
		t.Fatal(err)
	}
	if !st.sealed || st.previous != "" {
		t.Fatalf("primeiro arranque: sealed=%v previous=%q, quero sealed com previous vazio", st.sealed, st.previous)
	}
	recs, err := worm.Read(context.Background(), pdp.DefaultPolicyPartition, 1, 10)
	if err != nil || len(recs) != 1 {
		t.Fatalf("quero exactamente 1 policy.changed, tenho %d (%v)", len(recs), err)
	}
	obl := recs[0].Obligations[0]
	if obl.Type != pdp.PolicyChangedEventType || obl.Params["new_version"] != p.Version() ||
		obl.Params["content_hash"] != p.ContentHash() || obl.Params["old_version"] != "" ||
		obl.Params["author"] != policyProvisionActor {
		t.Errorf("registo selado nao transporta a transicao: %+v", obl.Params)
	}
	if recs[0].PolicyVersion != p.Version() || !recs[0].Timestamp.Equal(agora) {
		t.Errorf("PolicyVersion=%q Timestamp=%v, quero %q/%v", recs[0].PolicyVersion, recs[0].Timestamp, p.Version(), agora)
	}

	// Segundo arranque, mesma política: nada selado.
	st2, err := provisionPolicyChangelog(context.Background(), worm, p, agora.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if st2.sealed || st2.previous != p.Version() {
		t.Fatalf("arranque idempotente: sealed=%v previous=%q", st2.sealed, st2.previous)
	}
	if head, _ := worm.Head(context.Background(), pdp.DefaultPolicyPartition); head != 1 {
		t.Fatalf("head=%d, quero 1 — o arranque idempotente re-selou", head)
	}
}

// TestAOS310_TrocaDeVersaoSelaComOldVersion — simula um bundle anterior selado com outra versão:
// o arranque com a política de referência sela a transição old→new.
func TestAOS310_TrocaDeVersaoSelaComOldVersion(t *testing.T) {
	p := abrirBundleDeReferencia(t)
	worm := audit.NewMemStore()
	anterior := pdp.PolicyChangeEvent{OldVersion: "", NewVersion: "0.9.0", ContentHash: "sha256:antiga", Author: "config:node", Reason: "arranque antigo", At: time.Unix(1, 0).UTC()}
	if _, err := worm.Append(context.Background(), pdp.BuildPolicyChangedRecord(anterior, "")); err != nil {
		t.Fatal(err)
	}
	st, err := provisionPolicyChangelog(context.Background(), worm, p, time.Unix(2, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !st.sealed || st.previous != "0.9.0" {
		t.Fatalf("sealed=%v previous=%q, quero transicao a partir de 0.9.0", st.sealed, st.previous)
	}
	recs, _ := worm.Read(context.Background(), pdp.DefaultPolicyPartition, 2, 2)
	if len(recs) != 1 || recs[0].Obligations[0].Params["old_version"] != "0.9.0" || recs[0].Obligations[0].Params["new_version"] != p.Version() {
		t.Errorf("a transicao selada nao diz 0.9.0 -> %s: %+v", p.Version(), recs)
	}
}

type wormQueRecusaSelarPolitica struct{ audit.Store }

func (wormQueRecusaSelarPolitica) Append(context.Context, audit.AuditRecord) (audit.AuditRecord, error) {
	return audit.AuditRecord{}, errors.New("WORM em baixo (teste)")
}

// TestAOS310_FailClosed — selagem recusada ⇒ ErrPolicyProvisioning; PDP nil/não-carregado ⇒ no-op;
// WORM nil com PDP carregado ⇒ erro.
func TestAOS310_FailClosed(t *testing.T) {
	p := abrirBundleDeReferencia(t)
	if _, err := provisionPolicyChangelog(context.Background(), wormQueRecusaSelarPolitica{Store: audit.NewMemStore()}, p, time.Now()); !errors.Is(err, ErrPolicyProvisioning) {
		t.Fatalf("selagem recusada devia dar ErrPolicyProvisioning, veio %v", err)
	}
	if _, err := provisionPolicyChangelog(context.Background(), nil, p, time.Now()); !errors.Is(err, ErrPolicyProvisioning) {
		t.Fatalf("sem WORM devia dar ErrPolicyProvisioning, veio %v", err)
	}
	if st, err := provisionPolicyChangelog(context.Background(), audit.NewMemStore(), nil, time.Now()); err != nil || st.sealed {
		t.Fatalf("PDP nil devia ser no-op: %v %v", st, err)
	}
	if st, err := provisionPolicyChangelog(context.Background(), audit.NewMemStore(), pdp.NewUnloaded(), time.Now()); err != nil || st.sealed {
		t.Fatalf("PDP nao-carregado devia ser no-op: %v %v", st, err)
	}
}

// TestAOS310_BootstrapSelaEODeclaraNoBanner — pela composição REAL: um nó com bundle carregado
// arranca com um `policy.changed` na partição `policy` do seu WORM, e o banner diz que selou.
func TestAOS310_BootstrapSelaEODeclaraNoBanner(t *testing.T) {
	cfg := tnBaseConfig()
	cfg.Model = &countingModel{}
	cfg.PDP = abrirBundleDeReferencia(t)
	var banner strings.Builder
	node, err := Bootstrap(context.Background(), cfg, &banner)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()
	recs, err := node.WORM.Read(context.Background(), pdp.DefaultPolicyPartition, 1, 10)
	if err != nil || len(recs) != 1 {
		t.Fatalf("o arranque devia ter selado 1 policy.changed, tem %d (%v)", len(recs), err)
	}
	if !strings.Contains(banner.String(), "TRANSICAO SELADA") || !strings.Contains(banner.String(), cfg.PDP.Version()) {
		t.Errorf("o banner nao declara a transicao selada:\n%s", banner.String())
	}
	// CONTROLO — sem bundle, nem selo nem linha: o PDP não-carregado já tem a sua.
	cfg2 := tnBaseConfig()
	cfg2.Model = &countingModel{}
	var banner2 strings.Builder
	node2, err := Bootstrap(context.Background(), cfg2, &banner2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = node2.Close() }()
	if head, _ := node2.WORM.Head(context.Background(), pdp.DefaultPolicyPartition); head != 0 {
		t.Errorf("sem bundle nao ha o que selar (head=%d)", head)
	}
	if strings.Contains(banner2.String(), "AOS-310") {
		t.Errorf("sem bundle o banner nao devia ter linha AOS-310:\n%s", banner2.String())
	}
	_ = io.Discard
}
