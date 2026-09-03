package main

// AOS-299 — A COMPOSIÇÃO DO FENCING NO NÓ.
//
// O `FencedStore` fenceia as escritas do step-ledger e do checkpointer contra a autoridade de
// token. No nó, essa autoridade é o `*durable.LeaseManager` — que nasce no [NewNodeService],
// DEPOIS do ledger, que nasce no [Bootstrap]. A ligação é tardia por necessidade, e é essa
// ligação que estes testes fixam.
//
// # PORQUE ISTO PRECISA DE TESTE PRÓPRIO
//
// A [fencingAuthority] sem lease manager ligado reporta «não há detentor» — e não um erro. É a
// escolha certa para um embedder que compõe com [Bootstrap] e conduz o `Runtime` directamente:
// esse nó não tem posse nenhuma, logo não há escritor a superar.
//
// Mas essa mesma escolha, aplicada ao NÓ, seria um fencing inerte que ninguém notava. O que a
// fecha é a recusa de COMPOSIÇÃO abaixo: se há ledger durável e não há autoridade, o serviço não
// se constrói. Sem este teste, alguém que reordene o bootstrap desarma o fencing sem avermelhar
// nada.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"path/filepath"
	"testing"

	durable "github.com/aos-ref/kernel/agent-runtime/durable"
)

// aos299NoDuravel compõe um nó com execução durável — o único modo em que há ledger a fencear.
func aos299NoDuravel(t *testing.T) *Node {
	t.Helper()
	dir := t.TempDir()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.Operators = map[string]ed25519.PublicKey{"human:op-299": pub}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node
}

// TestAOS299_OBootstrapCompoeAAutoridadeJuntoDoLedger: os dois nascem no mesmo ramo, e é isso que
// torna a recusa de composição uma rede e não a regra.
func TestAOS299_OBootstrapCompoeAAutoridadeJuntoDoLedger(t *testing.T) {
	node := aos299NoDuravel(t)
	if node.Ledger == nil {
		t.Fatal("premissa: a execucao duravel devia ter composto o ledger")
	}
	if node.fencingAuth == nil {
		t.Fatal("ha ledger e NAO ha autoridade de fencing — as escritas do ledger ficariam sem quem lhes valide o token (AOS-299)")
	}
}

// TestAOS299_ONewNodeServiceLIGAAAutoridade prova que a ligação tardia acontece mesmo. Sem ela, a
// autoridade reportaria «não há detentor» para todos os runs do nó, e o fencing ficaria inerte —
// a escrever, mas sem nunca barrar ninguém.
func TestAOS299_ONewNodeServiceLIGAAAutoridade(t *testing.T) {
	node := aos299NoDuravel(t)
	if a := node.fencingAuth.actual(); a != nil {
		t.Fatal("premissa: antes do NewNodeService a autoridade nao devia ter lease manager")
	}
	svc, err := NewNodeService(node)
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })

	if node.fencingAuth.actual() == nil {
		t.Fatal("o NewNodeService NAO ligou o LeaseManager a autoridade: o fencing do ledger fica inerte (AOS-299)")
	}
}

// TestAOS299_UmNoComLedgerESemAutoridadeNaoARRANCA é a rede, e a razão de ela existir: a
// alternativa a recusar é um nó que hospeda runs a escrever sem fencing sem que nada o denuncie.
func TestAOS299_UmNoComLedgerESemAutoridadeNaoARRANCA(t *testing.T) {
	node := aos299NoDuravel(t)
	node.fencingAuth = nil // simula um bootstrap reordenado que deixou de a compor

	if _, err := NewNodeService(node); !errors.Is(err, ErrFencingAuthorityMissing) {
		t.Fatalf("NewNodeService com ledger e sem autoridade = %v, quero ErrFencingAuthorityMissing", err)
	}
}

// TestAOS299_SemAutoridadeLigadaNaoHaDetentor fixa a semântica que preserva o embedding, e fá-lo
// no tipo do nó — não no do pacote `durable` — porque é aqui que a decisão foi tomada.
func TestAOS299_SemAutoridadeLigadaNaoHaDetentor(t *testing.T) {
	a := &fencingAuthority{}
	tok, err := a.CurrentToken(context.Background(), "run-qualquer")
	if err != nil {
		t.Fatalf("sem lease manager NAO e um erro — e a ausencia de posse: %v", err)
	}
	if tok.Valid() {
		t.Fatalf("token = %d, quero 0 (nenhum detentor)", tok.Value())
	}
	expirado, existe, err := a.CurrentLeaseExpired(context.Background(), "run-qualquer")
	if err != nil || expirado || existe {
		t.Fatalf("CurrentLeaseExpired sem lease manager = (%v, %v, %v), quero (false, false, nil)", expirado, existe, err)
	}
	var _ durable.TokenSource = a
}

// TestAOS299_OLEDGERDoNoESCREVEPeloStoreFENCEADO é a prova de CABLAGEM, e faltava.
//
// Os testes acima provam que a autoridade é composta e ligada — e passavam na mesma com o ledger
// a escrever pelo store CRU, que é o defeito. Isto foi medido: revertida a composição para `es`,
// nenhum deles ficava vermelho. É a mesma lacuna que AOS-293 expôs — mecanismo provado, cablagem
// por provar —, e fecha-se da mesma maneira: exercendo o objecto que o nó compôs.
//
// O run é reclamado pelo LeaseManager do serviço (há detentor), e o Apply corre SEM token no
// contexto. Se o ledger escrever pelo store fenceado, é recusado; se escrever pelo cru, passa.
func TestAOS299_OLEDGERDoNoESCREVEPeloStoreFENCEADO(t *testing.T) {
	node := aos299NoDuravel(t)
	svc, err := NewNodeService(node)
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })

	const run = "run-aos299-cablagem"
	lease, err := svc.leases.Claim(context.Background(), run)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	efeito := func(context.Context) (durable.Result, error) {
		return durable.Result{Payload: []byte("ok")}, nil
	}
	// SEM token: um run COM detentor não aceita escrita de quem não o apresenta.
	_, _, err = node.Ledger.Apply(durable.ContextWithTitular(context.Background(), "nhi:t"), run+":s1", efeito)
	if !errors.Is(err, durable.ErrStaleFencingToken) {
		t.Fatalf("Apply sem token num run reclamado = %v, quero ErrStaleFencingToken — o ledger do no NAO esta a escrever pelo store fenceado (AOS-299)", err)
	}

	// COM o token do detentor: escreve.
	ctx := durable.ContextWithFencingToken(durable.ContextWithTitular(context.Background(), "nhi:t"), lease.Token)
	if _, aplicado, err := node.Ledger.Apply(ctx, run+":s1", efeito); err != nil || !aplicado {
		t.Fatalf("Apply do detentor = (aplicado=%v, %v), quero (true, nil)", aplicado, err)
	}
}
