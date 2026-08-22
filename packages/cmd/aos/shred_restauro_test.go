package main

import (
	"context"
	"errors"
	"io"
	"testing"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------------------------
// O DESMENTIDO SOBREVIVE AO RESTART.
//
// Segunda metade do achado 1.6, demonstrada com output:
//
//	DEPOIS DE UM RESTART:  chave viva? true    prontidão? VERDE    por confirmar: 0
//
// Mesmo eixo do `holdsRestored`: a barreira EXISTIA, o CONTEÚDO dela é que não sobrevivia ao
// arranque. Uma guarda cujo estado se perde no restart é uma guarda até ao próximo deploy.
// ---------------------------------------------------------------------------------------------

// custodiaComPendencias é o duplo mínimo que implementa [shredPendingMarker] — a porta que o
// arranque usa para repor. Não simula o Vault; conta o que lhe mandaram repor, que é o que este
// teste precisa de observar.
type custodiaComPendencias struct {
	*audit.InMemoryKeyVault
	repostas []string
}

func (c *custodiaComPendencias) marcarShredPorConfirmar(subjectID string) {
	c.repostas = append(c.repostas, subjectID)
}

func novaCustodiaComPendencias() *custodiaComPendencias {
	return &custodiaComPendencias{InMemoryKeyVault: audit.NewInMemoryKeyVault(nil)}
}

// cadeiaDSAR sela uma sequência de (capability, titular) na partição DSAR de um MemStore.
func cadeiaDSAR(t *testing.T, factos ...[2]string) audit.Store {
	t.Helper()
	store := audit.NewMemStore()
	for i, f := range factos {
		_, err := store.Append(context.Background(), audit.AuditRecord{
			Partition:  "governance.dsar",
			Decision:   audit.DecisionAllow,
			Capability: f[0],
			RequestID:  "req-restauro",
			Resource:   audit.Resource{Type: "dsar.subject", Value: f[1]},
		})
		if err != nil {
			t.Fatalf("selar facto %d: %v", i, err)
		}
	}
	return store
}

func TestRestauroRepoeADestruicaoPorConfirmar(t *testing.T) {
	store := cadeiaDSAR(t,
		[2]string{dsar.EventReceived, "nhi:a"},
		[2]string{dsar.EventShredUnconfirmed, "nhi:a"},
	)
	cust := novaCustodiaComPendencias()

	n, err := restoreShredPending(context.Background(), store, "governance.dsar", cust)
	if err != nil {
		t.Fatalf("restoreShredPending: %v", err)
	}
	if n != 1 || len(cust.repostas) != 1 || cust.repostas[0] != "nhi:a" {
		t.Fatalf("esperava 1 pendencia reposta para nhi:a, veio n=%d repostas=%v — o desmentido "+
			"morreu no restart e o /readyz fica VERDE sobre um apagamento por provar", n, cust.repostas)
	}
}

// TestUmaDestruicaoCONFIRMADADepoisLimpaAPendencia — a regra de limpeza, e não é simetria
// decorativa.
//
// Sem ela, uma destruição que falhou e foi REPETIDA com sucesso continuaria a pôr o nó UNREADY
// após cada restart, para sempre. Um alarme que não sabe desligar-se deixa de ser lido.
func TestUmaDestruicaoCONFIRMADADepoisLimpaAPendencia(t *testing.T) {
	store := cadeiaDSAR(t,
		[2]string{dsar.EventShredUnconfirmed, "nhi:b"},
		[2]string{dsar.EventKeyDestroyed, "nhi:b"}, // a segunda tentativa correu bem
	)
	cust := novaCustodiaComPendencias()

	n, err := restoreShredPending(context.Background(), store, "governance.dsar", cust)
	if err != nil {
		t.Fatalf("restoreShredPending: %v", err)
	}
	if n != 0 {
		t.Errorf("uma destruicao confirmada DEPOIS da falhada devia limpar a pendencia; veio n=%d "+
			"repostas=%v — o alarme nao sabe desligar-se", n, cust.repostas)
	}
	// CONTROLO DA ORDEM: o inverso NÃO limpa. Sem este ramo, um restauro que ignorasse a ordem
	// (ou que só olhasse para o primeiro facto) passaria no teste acima.
	inversa := cadeiaDSAR(t,
		[2]string{dsar.EventKeyDestroyed, "nhi:c"},
		[2]string{dsar.EventShredUnconfirmed, "nhi:c"}, // uma SEGUNDA erasure falhou
	)
	c2 := novaCustodiaComPendencias()
	if n2, _ := restoreShredPending(context.Background(), inversa, "governance.dsar", c2); n2 != 1 {
		t.Errorf("com a falha DEPOIS da confirmacao a pendencia devia ficar; veio n=%d", n2)
	}
}

// TestCustodiaQueNaoSabeConfirmarNaoTemNadaARepor — a porta é opcional dos dois lados.
func TestCustodiaQueNaoSabeConfirmarNaoTemNadaARepor(t *testing.T) {
	store := cadeiaDSAR(t, [2]string{dsar.EventShredUnconfirmed, "nhi:d"})
	n, err := restoreShredPending(context.Background(), store, "governance.dsar", audit.NewInMemoryKeyVault(nil))
	if err != nil || n != 0 {
		t.Errorf("um vault que nao sabe confirmar nao tem pendencia a repor; veio n=%d err=%v", n, err)
	}
}

// TestOFluxoDoNoRECEBEUOConfirmador é o teste de CABLAGEM, e é a DÉCIMA vez que este padrão
// aparece no repositório.
//
// Todos os testes acima exercem `restoreShredPending` e o fluxo DIRECTAMENTE. Uma mutação que
// removesse o `dsar.WithShredConfirmer(...)` do bootstrap passaria em todos eles: as unidades
// continuam correctas e o nó real continua a selar `key_destroyed` sobre chaves vivas.
func TestOFluxoDoNoRECEBEUOConfirmador(t *testing.T) {
	// A PRIMEIRA versão deste teste exercia só o adaptador — e era FRACA pela razão exacta que
	// diz combater: uma mutação que removesse o `dsar.WithShredConfirmer(...)` do bootstrap
	// passava nela, porque o adaptador continuava correcto e ninguém o ligava ao fluxo.
	//
	// `Config.DSARVault` é injectável, portanto a cablagem prova-se PELO NÓ REAL: uma custódia que
	// recusa confirmar, o fluxo COMPOSTO pelo bootstrap, e a cadeia a ler.
	cfg := tnBaseConfig()
	cfg.DSARVault = custodiaQueRecusa{InMemoryKeyVault: audit.NewInMemoryKeyVault(nil)}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	if node.DSAR == nil {
		t.Fatal("o fluxo DSAR nao foi composto — o cenario nao esta montado")
	}

	_, rerr := node.DSAR.Receive(context.Background(), dsar.Request{
		RequestID: "req-cablagem", SubjectID: "nhi:cablagem", Principal: "nhi:operador",
	})
	if !errors.Is(rerr, dsar.ErrShredUnconfirmed) {
		t.Fatalf("o fluxo COMPOSTO pelo bootstrap nao perguntou a custodia (err=%v) — o adaptador "+
			"pode estar certo e o `dsar.WithShredConfirmer` nao estar la", rerr)
	}

	// E A CADEIA REGISTOU-O. Sem este ramo, um fluxo que devolvesse o erro sem selar passaria.
	head, _ := node.WORM.Head(context.Background(), "governance.dsar")
	if head == 0 {
		t.Fatal("a particao DSAR ficou vazia")
	}
	recs, _ := node.WORM.Read(context.Background(), "governance.dsar", 1, head)
	var desmentiu, afirmou bool
	for _, r := range recs {
		switch r.Capability {
		case dsar.EventShredUnconfirmed:
			desmentiu = true
		case dsar.EventKeyDestroyed:
			afirmou = true
		}
	}
	if afirmou {
		t.Error("o no REAL selou key_destroyed com a custodia a recusar confirmar")
	}
	if !desmentiu {
		t.Error("o no REAL nao selou shred_unconfirmed")
	}
}

// custodiaQueRecusa é uma custódia que faz o trabalho e NÃO consegue confirmá-lo — o vault que
// aceita o DELETE e mantém a chave. Implementa a porta interna [shredConfirmer].
type custodiaQueRecusa struct{ *audit.InMemoryKeyVault }

func (custodiaQueRecusa) shredConfirmed(string) error { return errCustodiaRecusa }

var errCustodiaRecusa = errorString("a custodia nao confirma")

type errorString string

func (e errorString) Error() string { return string(e) }

// custodiaQueRegista é a custódia que SABE confirmar e regista o que lhe repuseram no arranque.
type custodiaQueRegista struct {
	*audit.InMemoryKeyVault
	repostas []string
}

func (c *custodiaQueRegista) shredConfirmed(string) error { return nil }
func (c *custodiaQueRegista) marcarShredPorConfirmar(s string) {
	c.repostas = append(c.repostas, s)
}

// TestOArranqueDoNoREPOEAPendencia é o segundo teste de CABLAGEM, e existe pela mesma razão que o
// primeiro: `restoreShredPending` pode estar correcta e o `Bootstrap` não a chamar.
//
// Prova-se com o nó REAL — `Config.WORM` semeado com o desmentido que um arranque anterior selou,
// e uma custódia que regista o que lhe repõem.
func TestOArranqueDoNoREPOEAPendencia(t *testing.T) {
	store := cadeiaDSAR(t,
		[2]string{dsar.EventReceived, "nhi:sobrevivente"},
		[2]string{dsar.EventShredUnconfirmed, "nhi:sobrevivente"},
	)
	cust := &custodiaQueRegista{InMemoryKeyVault: audit.NewInMemoryKeyVault(nil)}

	cfg := tnBaseConfig()
	cfg.WORM = store
	cfg.DSARVault = cust
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })

	if len(cust.repostas) != 1 || cust.repostas[0] != "nhi:sobrevivente" {
		t.Fatalf("o arranque NAO repos a pendencia que a cadeia ainda afirma (repostas=%v) — "+
			"depois de um restart o /readyz fica VERDE sobre um apagamento por provar", cust.repostas)
	}
}

// TestOArranqueNaoInventaPendencias é o CONTROLO do teste acima.
//
// Sem ele, um arranque que repusesse TODO o titular que aparece na cadeia — ou que repusesse uma
// pendência fixa — passaria. Um nó que nasce UNREADY sobre apagamentos que correram bem é tão
// inútil como um que nasce verde sobre os que falharam.
func TestOArranqueNaoInventaPendencias(t *testing.T) {
	store := cadeiaDSAR(t,
		[2]string{dsar.EventReceived, "nhi:tranquilo"},
		[2]string{dsar.EventKeyDestroyed, "nhi:tranquilo"},
	)
	cust := &custodiaQueRegista{InMemoryKeyVault: audit.NewInMemoryKeyVault(nil)}

	cfg := tnBaseConfig()
	cfg.WORM = store
	cfg.DSARVault = cust
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })

	if len(cust.repostas) != 0 {
		t.Errorf("o arranque inventou %d pendencia(s) sobre uma cadeia que so tem apagamentos "+
			"CONFIRMADOS: %v", len(cust.repostas), cust.repostas)
	}
}

// TestUmApagamentoBLOQUEADONaoGeraPendencia é a lacuna que a mutação S6 revelou.
//
// A mutação «qualquer facto DSAR passa a marcar pendência» NÃO caía: os testes acima tinham
// sempre um desfecho a seguir ao `dsar.received`, e o último facto ganhava de qualquer forma.
//
// O caso que faltava é o REAL e o mais comum dos falhados: um apagamento bloqueado por legal
// hold sela `received` e `blocked`, e não tenta destruir NADA. Se o restauro tratasse esses
// factos como pendência, cada titular sob preservação punha o nó UNREADY para sempre — e o
// remédio seria desligar a sonda, que é a pior forma de resolver um alarme.
func TestUmApagamentoBLOQUEADONaoGeraPendencia(t *testing.T) {
	store := cadeiaDSAR(t,
		[2]string{dsar.EventReceived, "nhi:sob-hold"},
		[2]string{dsar.EventBlocked, "nhi:sob-hold"},
	)
	cust := novaCustodiaComPendencias()

	n, err := restoreShredPending(context.Background(), store, "governance.dsar", cust)
	if err != nil {
		t.Fatalf("restoreShredPending: %v", err)
	}
	if n != 0 {
		t.Errorf("um apagamento BLOQUEADO gerou %d pendencia(s) %v — nada foi destruido, logo nada "+
			"ha por confirmar; cada titular sob legal hold poria o no UNREADY para sempre", n, cust.repostas)
	}
}
