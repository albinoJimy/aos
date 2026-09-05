package hitl

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/platform/audit"
)

// aos311_selo_terminal_test.go — ACHADO DE REVISÃO ADVERSARIAL SOBRE AOS-311: a
// DURABILIDADE DA PROVA não pode depender de quem a provoca.
//
// O AOS-311 pôs `audit.FileStore.Append` e `audit.MemStore.Append` a respeitar o `ctx`.
// É a correcção certa para o caminho audit-BEFORE-effect do Reference Monitor, onde um
// prazo esgotado tem de resolver em deny. Mas partiu os selos PÓS-DECISÃO: o
// [Channel] sela a decisão terminal e, no caminho de TIMEOUT, o ctx está morto POR
// CONSTRUÇÃO — é o prazo esgotado que produz a decisão. A negação fail-closed continuava
// a acontecer e deixava de ser ESCRITA, com o erro do Append engolido pelo `err == nil`.
// Um auditor que perguntasse «porque é que esta acção foi negada» não encontrava registo,
// e uma decisão assinada apanhada pelo prazo perdia a obrigação `hitl_signature` — a base
// do não-repúdio.
//
// Estes testes provam as DUAS metades da separação:
//   - durabilidade da prova: a decisão terminal fica selada mesmo com o ctx do chamador
//     morto (T1, T2, T5);
//   - fail-closed do efeito: continua a valer, e agora por guarda EXPLÍCITA no caminho da
//     decisão em vez de por efeito colateral do sink (T3), com o store a manter o
//     contrato do AOS-311 (T4).
//
// Sem T3/T4 a "correcção" podia ter sido feita desligando o AOS-311, e o fail-closed por
// prazo — que é o que o ticket entregou — deixaria de valer.

// sealerEspia embrulha o [audit.MemStore] e regista o `ctx.Err()` TAL COMO O SINK O VÊ.
// É a asserção directa da propriedade: o selo tem de chegar ao store com um contexto
// vivo, mesmo quando o do chamador já morreu.
type sealerEspia struct {
	*audit.MemStore
	errosVistos []error
}

func (s *sealerEspia) Append(ctx context.Context, rec audit.AuditRecord) (audit.AuditRecord, error) {
	s.errosVistos = append(s.errosVistos, ctx.Err())
	return s.MemStore.Append(ctx, rec)
}

// exigirSinkComContextoVivo verifica que houve exactamente uma tentativa de selagem e que
// o sink a recebeu sem cancelamento herdado.
func exigirSinkComContextoVivo(t *testing.T, espia *sealerEspia) {
	t.Helper()
	if len(espia.errosVistos) != 1 {
		t.Fatalf("tentativas de selagem = %d, quero 1", len(espia.errosVistos))
	}
	if espia.errosVistos[0] != nil {
		t.Fatalf("o sink recebeu um contexto morto (%v) — o selo de uma decisao terminal nao pode herdar o cancelamento de quem a provocou", espia.errosVistos[0])
	}
}

// T1 — O CASO DO REVISOR. Silêncio numa acção irreversível: o prazo esgota, a decisão é
// deny fail-closed, e essa negação FICA REGISTADA. Antes desta correcção a partição de
// quarentena vinha vazia.
func TestSeloDaDecisaoTerminalSobreviveAoPrazoEsgotado(t *testing.T) {
	t.Parallel()
	espia := &sealerEspia{MemStore: audit.NewMemStore()}
	ch, err := NewChannel(NewMemApproverRegistry(), blockingSource(), espia, WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	resp, err := ch.Confirm(ctx, dangerReq("run-7", "cap:fs.delete -> /data/prod"))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if resp.Approved {
		t.Fatal("FAIL-CLOSED violado: o silencio numa accao irreversivel APROVOU")
	}
	// A premissa do teste: o contexto do chamador está mesmo morto quando se sela.
	if ctx.Err() == nil {
		t.Fatal("o contexto do chamador devia ter expirado — o teste nao mede o que promete")
	}
	exigirSinkComContextoVivo(t, espia)

	head, err := espia.Head(context.Background(), partitionUnauth)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head != 1 {
		t.Fatalf("particao %q com head=%d, quero 1 — a negacao fail-closed TEM de deixar rasto", partitionUnauth, head)
	}
	rec := lastRecord(t, espia.MemStore, partitionUnauth)
	if rec.Decision != audit.DecisionDeny {
		t.Fatalf("decisao selada = %q, quero deny", rec.Decision)
	}
	if r := obligationParam(t, rec, "hitl_decision", "reason"); r != ReasonTimeout {
		t.Fatalf("motivo selado = %q, quero %q", r, ReasonTimeout)
	}
	if v := obligationParam(t, rec, "hitl_decision", "timed_out"); v != "true" {
		t.Fatalf("timed_out selado = %q, quero true", v)
	}
}

// T2 — NÃO-REPÚDIO PRESERVADO. Uma decisão ASSINADA E VERIFICADA cujo prazo morre no
// instante da selagem continua a selar a obrigação `hitl_signature`. O relógio do canal é
// o gancho: `c.now()` só é consultado DENTRO de [Channel.seal], pelo que cancelar ali
// modela exactamente a corrida real — o prazo expira entre a decisão e a escrita.
//
// O caminho escolhido é o self-approval 4-eyes: é o único deny com verified=true que se
// alcança de forma determinista, e é justamente onde a assinatura mais importa (forense
// de quem tentou auto-aprovar-se).
func TestSeloDaDecisaoTerminalPreservaAAssinaturaComOPrazoAMorrerNaSelagem(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	reg := NewMemApproverRegistry()
	reg.Register("approver-1", vault.provision("approver-1", 0x11), RequiredAuthority(risk.ClassDanger))
	espia := &sealerEspia{MemStore: audit.NewMemStore()}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// O prazo morre na primeira consulta ao relógio — que ocorre dentro do seal.
	relogioQueMata := func() time.Time {
		cancel()
		return fixedClock()()
	}

	src := scriptedSource{fn: func(_ context.Context, p Presentation) (SignedApproval, error) {
		return signApprovalFor(t, vault, "approver-1", true, p), nil
	}}
	ch, err := NewChannel(reg, src, espia, WithClock(relogioQueMata))
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}

	// Solicitante == aprovador ⇒ dual-control nega, mas a assinatura É válida e atribuível.
	resp, err := ch.Confirm(ctx, dangerReq("approver-1", "cap:fs.delete -> /data/prod"))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if resp.Approved {
		t.Fatal("4-eyes violado: o auto-aprovador foi aprovado")
	}
	if ctx.Err() == nil {
		t.Fatal("o relogio nao chegou a matar o contexto — o teste nao mede o que promete")
	}
	exigirSinkComContextoVivo(t, espia)

	rec := lastRecord(t, espia.MemStore, "hitl:approver-1")
	if rec.Decision != audit.DecisionDeny {
		t.Fatalf("decisao selada = %q, quero deny", rec.Decision)
	}
	if r := obligationParam(t, rec, "hitl_decision", "reason"); r != ReasonDualControl {
		t.Fatalf("motivo selado = %q, quero %q", r, ReasonDualControl)
	}
	// A PEÇA QUE SE PERDIA: sem ela o selo diz que houve uma tentativa e não de quem.
	if a := obligationParam(t, rec, "hitl_signature", "approver"); a != "approver-1" {
		t.Fatalf("aprovador selado = %q, quero approver-1", a)
	}
	if s := obligationParam(t, rec, "hitl_signature", "signature"); s == "" {
		t.Fatal("assinatura ausente do selo — o nao-repudio depende dela")
	}
	if rec.Principal.NHIID != "approver-1" {
		t.Fatalf("principal selado = %q, quero approver-1", rec.Principal.NHIID)
	}
}

// T3 — CONTROLO: o FAIL-CLOSED DO EFEITO não foi desligado. Uma aprovação assinada,
// verificável e por aprovador autorizado que chegue com o prazo JÁ MORTO continua a NEGAR.
// A propriedade passou a ser imposta por guarda explícita no caminho da decisão, e não
// pelo sink — o que se prova aqui é que ela continua a valer com um sink que aceita tudo.
func TestPrazoMortoContinuaANegarMesmoComAprovacaoValida(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	reg := NewMemApproverRegistry()
	reg.Register("approver-1", vault.provision("approver-1", 0x11), RequiredAuthority(risk.ClassDanger))
	espia := &sealerEspia{MemStore: audit.NewMemStore()}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := scriptedSource{fn: func(_ context.Context, p Presentation) (SignedApproval, error) {
		cancel() // o pedido morre enquanto o aprovador responde
		return signApprovalFor(t, vault, "approver-1", true, p), nil
	}}
	ch, err := NewChannel(reg, src, espia, WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}

	resp, err := ch.Confirm(ctx, dangerReq("run-7", "cap:fs.delete -> /data/prod"))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if resp.Approved {
		t.Fatal("FAIL-CLOSED violado: uma aprovacao chegada fora do prazo AUTORIZOU o efeito")
	}
	// E a negação fica registada — as duas metades valem ao mesmo tempo.
	if head, _ := espia.Head(context.Background(), partitionUnauth); head != 1 {
		t.Fatalf("particao %q com head=%d, quero 1", partitionUnauth, head)
	}
	rec := lastRecord(t, espia.MemStore, partitionUnauth)
	if r := obligationParam(t, rec, "hitl_decision", "reason"); r != ReasonTimeout {
		t.Fatalf("motivo selado = %q, quero %q", r, ReasonTimeout)
	}
}

// T4 — CONTROLO: o contrato do AOS-311 no store continua intacto. Se este teste falhasse,
// a correcção teria sido desligar o ticket, e o audit-before-effect do Reference Monitor
// deixaria de ser fail-closed por prazo.
func TestStoreDeAuditoriaContinuaARecusarSobContextoMorto(t *testing.T) {
	t.Parallel()
	st := audit.NewMemStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.Append(ctx, audit.AuditRecord{Partition: "x", Decision: audit.DecisionAllow}); !errors.Is(err, context.Canceled) {
		t.Fatalf("o store tem de continuar a recusar sob contexto morto (AOS-311), veio: %v", err)
	}
	if head, _ := st.Head(context.Background(), "x"); head != 0 {
		t.Fatalf("escreveu apesar do contexto cancelado (head=%d)", head)
	}
}

// T5 — a mesma propriedade no [RatificationGate], que partilha o molde: um bloqueio de
// promoção decidido sob contexto morto NÃO promove (fail-closed) e FICA SELADO. Sem a
// correcção o artefacto ficava por promover sem rasto nenhum de porquê.
func TestRatificacaoSelaOBloqueioDecididoSobContextoMorto(t *testing.T) {
	t.Parallel()
	h := newRatHarness(t)
	art := passingArtifact()
	signed := signRatificationFor(t, h.vault, "ratifier", true, art)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	admit, err := h.gate.Ratify(ctx, art, signed)
	if err != nil {
		t.Fatalf("Ratify: %v", err)
	}
	if admit {
		t.Fatal("FAIL-CLOSED violado: promoveu sobre um pedido que ja nao existe")
	}

	dec, params, principal, ok := decisionObligation(t, h.store, "ratification:"+art.ID)
	if !ok {
		t.Fatal("bloqueio nao selado — a decisao terminal tem de deixar rasto mesmo sob ctx morto")
	}
	if dec != audit.DecisionDeny {
		t.Fatalf("decisao selada = %q, quero deny", dec)
	}
	if params["reason"] != ReasonRatificationContextDead {
		t.Fatalf("motivo selado = %q, quero %q", params["reason"], ReasonRatificationContextDead)
	}
	// A ratificação era autêntica: o selo continua atribuível ao humano que a assinou.
	if principal.NHIID != "ratifier" {
		t.Fatalf("principal selado = %q, quero ratifier", principal.NHIID)
	}
}
