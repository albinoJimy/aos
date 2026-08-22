package dsar_test

import (
	"errors"
	"testing"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------------------------
// A CADEIA NÃO AFIRMA O QUE NINGUÉM VERIFICOU.
//
// Achado 1.6 da varredura adversarial de 2026-08-21:
//
//	/dsar/erase com vault que aceita o DELETE e mantém a chave:
//	  HTTP 500 ao requerente           ← o chamador NÃO recebe afirmação falsa
//	  WORM: dsar.key_destroyed / allow ← BYTE-IDÊNTICO ao caso honesto
//
// O 500 já existia. O que faltava era o registo tamper-evident — o que sobrevive a tudo o resto,
// e o único que um regulador vai ler anos depois.
// ---------------------------------------------------------------------------------------------

var errCustodiaMuda = errors.New("a custodia nao confirma")

// custodiaQueNaoConfirma implementa [dsar.ShredConfirmer] recusando SEMPRE a confirmação — o vault
// que aceita o DELETE e mantém a chave.
type custodiaQueNaoConfirma struct{}

func (custodiaQueNaoConfirma) ShredConfirmed(string) error { return errCustodiaMuda }

// custodiaHonesta confirma sempre. É o CONTROLO: sem ela, «selar sempre shred_unconfirmed»
// passaria no teste principal.
type custodiaHonesta struct{}

func (custodiaHonesta) ShredConfirmed(string) error { return nil }

// selosDSAR devolve os registos da partição DSAR.
func selosDSAR(t *testing.T, h *harness) []audit.AuditRecord {
	t.Helper()
	head, err := h.store.Head(h.ctx, "governance.dsar")
	if err != nil || head == 0 {
		return nil
	}
	recs, err := h.store.Read(h.ctx, "governance.dsar", 1, head)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return recs
}

func TestCustodiaQueNaoConfirmaNaoDeixaACadeiaAfirmar(t *testing.T) {
	h := newHarness(t, dsar.WithShredConfirmer(custodiaQueNaoConfirma{}))
	const titular = "nhi:titular-1.6"
	h.seedAudit(t, "governance.dsar", titular, []byte("pii"))

	_, err := h.flow.Receive(h.ctx, dsar.Request{RequestID: "req-1.6", SubjectID: titular})
	if !errors.Is(err, dsar.ErrShredUnconfirmed) {
		t.Fatalf("Receive devia devolver ErrShredUnconfirmed, veio %v", err)
	}

	selos := selosDSAR(t, h)
	if len(selos) == 0 {
		t.Fatal("nenhum selo — o cenario nao esta montado")
	}
	var afirmou, desmentiu bool
	for _, s := range selos {
		switch s.Capability {
		case dsar.EventKeyDestroyed:
			afirmou = true
		case dsar.EventShredUnconfirmed:
			desmentiu = true
			if s.Decision != audit.DecisionDeny {
				t.Errorf("o selo de nao-confirmacao tem Decision=%v e devia ser Deny — uma leitura "+
					"por decisao confundi-lo-ia com um apagamento satisfeito", s.Decision)
			}
		}
	}
	if afirmou {
		t.Error("a cadeia selou dsar.key_destroyed com a custodia a NAO confirmar — afirma uma " +
			"irrecuperabilidade que ninguem verificou, e e sobre ela que o titular decide nao " +
			"voltar a pedir")
	}
	if !desmentiu {
		t.Error("a cadeia nao selou dsar.shred_unconfirmed — o desfecho falhado nao deixou marca " +
			"nenhuma no registo tamper-evident")
	}
}

// TestCustodiaHonestaContinuaASelarKeyDestroyed é o CONTROLO.
//
// Sem ele, «selar sempre shred_unconfirmed» passaria no teste acima — e um nó que nunca afirma um
// apagamento satisfeito é tão inútil como um que o afirma sempre.
func TestCustodiaHonestaContinuaASelarKeyDestroyed(t *testing.T) {
	h := newHarness(t, dsar.WithShredConfirmer(custodiaHonesta{}))
	const titular = "nhi:titular-ok"
	h.seedAudit(t, "governance.dsar", titular, []byte("pii"))

	if _, err := h.flow.Receive(h.ctx, dsar.Request{RequestID: "req-ok", SubjectID: titular}); err != nil {
		t.Fatalf("Receive com custodia honesta: %v", err)
	}
	var afirmou, desmentiu bool
	for _, s := range selosDSAR(t, h) {
		switch s.Capability {
		case dsar.EventKeyDestroyed:
			afirmou = true
		case dsar.EventShredUnconfirmed:
			desmentiu = true
		}
	}
	if !afirmou {
		t.Error("a custodia confirmou e a cadeia NAO selou key_destroyed")
	}
	if desmentiu {
		t.Error("a custodia confirmou e a cadeia selou shred_unconfirmed")
	}
}

// TestSemConfirmadorOFluxoMantemOComportamentoAnterior — a porta é OPCIONAL, e isso é uma decisão
// e não uma omissão: custódias que não sabem responder não ganham uma resposta inventada.
func TestSemConfirmadorOFluxoMantemOComportamentoAnterior(t *testing.T) {
	h := newHarness(t) // sem WithShredConfirmer
	const titular = "nhi:titular-sem-porta"
	h.seedAudit(t, "governance.dsar", titular, []byte("pii"))

	if _, err := h.flow.Receive(h.ctx, dsar.Request{RequestID: "req-sem", SubjectID: titular}); err != nil {
		t.Fatalf("Receive sem confirmador: %v", err)
	}
	var afirmou bool
	for _, s := range selosDSAR(t, h) {
		if s.Capability == dsar.EventKeyDestroyed {
			afirmou = true
		}
	}
	if !afirmou {
		t.Error("sem confirmador o fluxo devia manter o comportamento anterior e selar key_destroyed")
	}
}
