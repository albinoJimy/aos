package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	audit "github.com/aos-ref/platform/audit"
)

// AOS-267 (Grupo B) — PROVAS FALSIFICÁVEIS do scheduler interno de retenção.
//
// O defeito que estes testes trancam: com `AOS_RETENTION_*` definidas, a expiração por TTL só
// corria sob `POST /dsar/expire`. Sem cron externo autenticado, NADA expirava — *storage
// limitation* silenciosamente não aplicada. Cada teste prova os DOIS sentidos e é não-vacuoso:
//
//   - o conteúdo é decifrável ANTES e irrecuperável DEPOIS, sem UMA chamada externa;
//   - o selo por varrimento nomeia QUEM (identidade em nome próprio, nunca um operador) e O QUÊ
//     (contagens), e não leva PII;
//   - um titular sob LEGAL HOLD sobrevive ao varrimento agendado e só expira depois do release;
//   - a cadência é fail-closed no ambiente e nenhum valor desliga o scheduler;
//   - rota e scheduler partilham UM guard (nunca correm em simultâneo).
//
// Correr SEMPRE com -race.

// retentionSweepRecords devolve os registos da cadeia [retentionPartition] cujo Resource.Type é o
// evento dado (started/completed), por ordem de selagem.
func retentionSweepRecords(t *testing.T, node *Node, event string) []audit.AuditRecord {
	t.Helper()
	ctx := context.Background()
	head, err := node.WORM.Head(ctx, retentionPartition)
	if err != nil {
		t.Fatalf("Head(%q): %v", retentionPartition, err)
	}
	if head == 0 {
		return nil
	}
	recs, err := node.WORM.Read(ctx, retentionPartition, 1, head)
	if err != nil {
		t.Fatalf("Read(%q): %v", retentionPartition, err)
	}
	var out []audit.AuditRecord
	for _, r := range recs {
		if r.Resource.Type == event {
			out = append(out, r)
		}
	}
	return out
}

// paramOf extrai um parâmetro de uma obrigação do registo (o primeiro que o traga).
func paramOf(rec audit.AuditRecord, key string) (string, bool) {
	for _, ob := range rec.Obligations {
		if v, ok := ob.Params[key]; ok {
			return v, true
		}
	}
	return "", false
}

// newScheduledRetentionService compõe o loop de serviço sobre um nó com política de retenção
// armada, com os OUTROS varrimentos desligados (isolam-se do que está sob teste) e a cadência de
// retenção dada.
func newScheduledRetentionService(t *testing.T, node *Node, cadence time.Duration) *NodeService {
	t.Helper()
	svc, err := NewNodeService(node,
		WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute),
		WithApprovalSweepInterval(0), WithDeadlineSweepInterval(0),
		WithRetentionSweepInterval(cadence),
	)
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Shutdown(ctx)
	})
	return svc
}

// -------------------------------------------------------------------------------------
// (CA1) A política definida ⇒ a expiração corre PERIODICAMENTE, sem acção externa.
// -------------------------------------------------------------------------------------

// TestAOS267_ScheduledSweepExpiresWithoutAnyExternalAction é o teste central do ticket: com a
// política armada, compor o loop de serviço BASTA — ninguém chama `POST /dsar/expire`, ninguém
// corre o job à mão, e o conteúdo do titular torna-se irrecuperável na mesma. Falsificável: antes
// de AOS-267 este teste esperaria para sempre (o job tinha um único condutor, a rota).
func TestAOS267_ScheduledSweepExpiresWithoutAnyExternalAction(t *testing.T) {
	node := newRetentionNode(t)
	const subject = "nhi:agent-sched"
	const runID = "run-sched"
	captureSynthetic(t, node, subject, runID, "conteudo agendado: SCHED-101", "outSched")

	sealed, gotSubj := sealedContentOf(t, node, runID)
	if gotSubj != subject {
		t.Fatalf("conteudo nao selado sob o titular: %q", gotSubj)
	}
	// NÃO-VÁCUO: antes de o scheduler existir, o conteúdo é recuperável.
	if _, err := audit.OpenContent(node.DSARVault, subject, sealed); err != nil {
		t.Fatalf("antes do scheduler o conteudo devia ser decifravel: %v", err)
	}

	// Compõe o serviço — e mais NADA. A cadência curta só encurta a espera do teste; o mecanismo
	// é o mesmo de 1h em produção.
	newScheduledRetentionService(t, node, 20*time.Millisecond)

	expirou := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := audit.OpenContent(node.DSARVault, subject, sealed); errors.Is(err, audit.ErrDecrypt) {
			expirou = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !expirou {
		t.Fatal("o scheduler interno devia ter expirado o titular SEM qualquer accao externa (nem POST /dsar/expire, nem ExpirationJob.Run a mao) — e a lacuna de storage-limitation que AOS-267 fecha")
	}

	// A destruição ficou ATRIBUÍDA: o varrimento selou-se na cadeia.
	if len(retentionSweepRecords(t, node, retentionSweepStartedEvent)) == 0 {
		t.Fatal("nenhum selo retention.sweep.started na cadeia — a destruicao automatica ficou sem quem a atribuir")
	}
}

// -------------------------------------------------------------------------------------
// (CA2) Credencial EM NOME PRÓPRIO + selo WORM por varrimento (quem / o quê), sem PII.
// -------------------------------------------------------------------------------------

func TestAOS267_SweepSealsOwnCredentialAndCountsWithoutPII(t *testing.T) {
	ctx := context.Background()
	node := newRetentionNode(t)
	const subject = "nhi:agent-selo"
	captureSynthetic(t, node, subject, "run-selo", "conteudo selo: SELO-202", "outSelo")

	// Cadência longa: o ticker NÃO interfere — a passagem é conduzida de forma determinista.
	svc := newScheduledRetentionService(t, node, time.Hour)
	if !svc.SweepRetentionNow(ctx) {
		t.Fatal("a passagem devia concluir com a cadeia integra")
	}

	started := retentionSweepRecords(t, node, retentionSweepStartedEvent)
	completed := retentionSweepRecords(t, node, retentionSweepCompletedEvent)
	if len(started) != 1 || len(completed) != 1 {
		t.Fatalf("esperava exactamente 1 selo de atribuicao e 1 de desfecho, vieram %d/%d", len(started), len(completed))
	}

	// QUEM: identidade EM NOME PRÓPRIO. Nunca a de um operador — não há operador nenhum aqui.
	for _, rec := range []audit.AuditRecord{started[0], completed[0]} {
		if rec.Principal.NHIID != retentionSchedulerNHI {
			t.Errorf("o selo devia ser atribuido a %q (identidade em nome proprio do no), veio %q", retentionSchedulerNHI, rec.Principal.NHIID)
		}
		if rec.Capability != capRetentionSweep {
			t.Errorf("capability do selo devia ser %q, veio %q", capRetentionSweep, rec.Capability)
		}
		if rec.ToolID != retentionSweepToolID {
			t.Errorf("tool_id do selo devia ser %q, veio %q", retentionSweepToolID, rec.ToolID)
		}
		if rec.PolicyVersion != node.Retention.Version() {
			t.Errorf("o selo devia rotular a versao da politica %q, veio %q", node.Retention.Version(), rec.PolicyVersion)
		}
		if v, ok := paramOf(rec, "trigger"); !ok || v != retentionTriggerScheduler {
			t.Errorf("o selo devia declarar a origem %q (scheduler, nao operador), veio %q (presente=%v)", retentionTriggerScheduler, v, ok)
		}
		if v, ok := paramOf(rec, "legal_hold"); !ok || v != "enforced" {
			t.Errorf("o selo devia registar que a barreira de legal hold estava composta, veio %q (presente=%v)", v, ok)
		}
		if v, ok := paramOf(rec, "issuer"); !ok || v != node.IssuerID {
			t.Errorf("o selo devia nomear o trust anchor deste no (%q), veio %q (presente=%v)", node.IssuerID, v, ok)
		}
		if v, ok := paramOf(rec, "cadence"); !ok || v != time.Hour.String() {
			t.Errorf("o selo devia registar a cadencia em vigor (%s), veio %q (presente=%v)", time.Hour, v, ok)
		}
	}
	// Os dois selos da MESMA passagem correlacionam-se.
	if started[0].RequestID == "" || started[0].RequestID != completed[0].RequestID {
		t.Errorf("os dois selos da mesma passagem deviam partilhar o id de correlacao, vieram %q e %q", started[0].RequestID, completed[0].RequestID)
	}

	// O QUÊ: contagens da passagem.
	expired, ok := paramOf(completed[0], "expired")
	if !ok || expired == "0" {
		t.Errorf("o selo de desfecho devia contar >=1 expirado, veio %q (presente=%v)", expired, ok)
	}
	for _, key := range []string{"scanned", "held", "skipped", "not_expired"} {
		if _, ok := paramOf(completed[0], key); !ok {
			t.Errorf("o selo de desfecho devia trazer a contagem %q", key)
		}
	}

	// ORDEM: a ATRIBUIÇÃO precede as destruições que autorizou, e o desfecho fecha a passagem.
	// É a ordem, na cadeia gapless, que serve de prova — por isso os três eventos vivem na MESMA
	// partição.
	head, err := node.WORM.Head(ctx, retentionPartition)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	recs, err := node.WORM.Read(ctx, retentionPartition, 1, head)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	viuExpired := false
	for _, r := range recs {
		if r.Resource.Type != audit.RetentionExpiredEventType {
			continue
		}
		viuExpired = true
		if r.AuditSeq < started[0].AuditSeq {
			t.Errorf("um retention.expired (seq %d) foi selado ANTES da atribuicao (seq %d) — houve destruicao sem quem selado", r.AuditSeq, started[0].AuditSeq)
		}
		if r.AuditSeq > completed[0].AuditSeq {
			t.Errorf("um retention.expired (seq %d) foi selado DEPOIS do desfecho (seq %d)", r.AuditSeq, completed[0].AuditSeq)
		}
	}
	if !viuExpired {
		t.Fatal("nao houve nenhum retention.expired — o teste da ordem seria vacuo")
	}

	// SEM PII: o titular NÃO aparece nos selos do varrimento (a disciplina de expireResponse: só
	// contagens). Os retention.expired do job continuam a nomear o titular — é outro registo, com
	// outra função, e não é o que este ticket acrescenta.
	for _, rec := range []audit.AuditRecord{started[0], completed[0]} {
		for _, ob := range rec.Obligations {
			for k, v := range ob.Params {
				if strings.Contains(v, subject) {
					t.Errorf("o selo do varrimento nao devia conter o titular (param %q = %q)", k, v)
				}
			}
		}
		if strings.Contains(rec.Resource.Value, subject) {
			t.Errorf("o Resource.Value do selo nao devia conter o titular: %q", rec.Resource.Value)
		}
	}

	// A cadeia continua a validar após o crypto-shred (paridade AOS-221).
	if err := audit.Verify(ctx, node.WORM, retentionPartition, 1, head); err != nil {
		t.Fatalf("hash-chain de retencao NAO valida apos o varrimento agendado: %v", err)
	}
}

// -------------------------------------------------------------------------------------
// (CA2) LEGAL HOLD — um varrimento AGENDADO respeita-o; o release reabre a expiração.
// -------------------------------------------------------------------------------------

func TestAOS267_ScheduledSweepRespectsLegalHold(t *testing.T) {
	ctx := context.Background()
	node := newRetentionNode(t)
	const subject = "nhi:agent-hold-sched"
	captureSynthetic(t, node, subject, "run-hold-sched", "conteudo retido agendado: HOLD-303", "outHoldSched")
	sealed, _ := sealedContentOf(t, node, "run-hold-sched")

	svc := newScheduledRetentionService(t, node, time.Hour)

	// SOB HOLD: o varrimento agendado corre, mas NADA é destruído.
	node.DSARHolds.HoldSubject(subject)
	if !svc.SweepRetentionNow(ctx) {
		t.Fatal("a passagem devia concluir com a cadeia integra")
	}
	if _, err := audit.OpenContent(node.DSARVault, subject, sealed); err != nil {
		t.Fatalf("sob legal hold o conteudo TEM de continuar decifravel — um varrimento que apague material sob hold e um incidente: %v", err)
	}
	completed := retentionSweepRecords(t, node, retentionSweepCompletedEvent)
	if len(completed) != 1 {
		t.Fatalf("esperava 1 selo de desfecho, vieram %d", len(completed))
	}
	if v, ok := paramOf(completed[0], "held"); !ok || v == "0" {
		t.Errorf("o selo devia contar >=1 RETIDO por legal hold, veio %q (presente=%v)", v, ok)
	}
	if v, ok := paramOf(completed[0], "expired"); !ok || v != "0" {
		t.Errorf("sob hold NADA devia expirar, o selo conta %q expirados", v)
	}

	// LIBERTADO: o MESMO varrimento agendado passa a expirar.
	node.DSARHolds.ReleaseSubject(subject)
	if !svc.SweepRetentionNow(ctx) {
		t.Fatal("a passagem devia concluir com a cadeia integra")
	}
	if _, err := audit.OpenContent(node.DSARVault, subject, sealed); !errors.Is(err, audit.ErrDecrypt) {
		t.Fatalf("apos o release o varrimento agendado devia expirar o titular (ErrDecrypt), deu: %v", err)
	}
}

// TestAOS267_SchedulerRefusesToArmWithoutLegalHold prova a perna fail-closed que o comentário do
// ticket exige: o legal hold NÃO é opcional. Sem a barreira de preservação composta, o job
// (holds nil ⇒ `held` sempre false) apagaria tudo o que cruzasse o TTL; um varredor AUTOMÁTICO
// nessas condições não arranca de todo, e o banner diz porquê.
func TestAOS267_SchedulerRefusesToArmWithoutLegalHold(t *testing.T) {
	node := newRetentionNode(t)

	base := func() *Node {
		return &Node{
			ExpirationJob: node.ExpirationJob,
			DSARHolds:     node.DSARHolds,
			WORM:          node.WORM,
			Retention:     node.Retention,
			// holdsRestored espelha o antecedente que o bootstrap REAL estabelece antes de
			// armar o varredor: a re-hidratação do legal hold a partir do WORM correu com
			// sucesso (bootstrap.go:1554). Sem ele o scheduler não arma — é a perna
			// fail-closed que a remediação da W6 adicionou (o CRÍTICO do restart). O caso
			// negativo `holdsRestored=false` é uma das linhas da tabela abaixo.
			holdsRestored: true,
		}
	}
	// NÃO-VÁCUO: a conjunção completa ARMA.
	if !retentionSchedulerArmed(base(), time.Hour) {
		t.Fatal("com politica + legal hold + WORM + job compostos o scheduler devia ARMAR (senao os casos negativos sao vacuos)")
	}

	cases := []struct {
		name       string
		mutate     func(*Node)
		wantReason string
	}{
		{"sem legal hold", func(n *Node) { n.DSARHolds = nil }, "legal hold NAO esta composto"},
		{"sem WORM", func(n *Node) { n.WORM = nil }, "nao ha onde selar QUEM"},
		{"sem politica", func(n *Node) { n.Retention = audit.RetentionConfig{} }, "SEM politica de retencao"},
		{"sem job", func(n *Node) { n.ExpirationJob = nil }, "ExpirationJob nao esta composto"},
		// A perna fail-closed CENTRAL da remediação da W6 (o CRÍTICO do restart): com tudo
		// composto mas o legal hold NÃO re-hidratado do WORM, o varredor automático apagaria
		// material sob preservação que o processo reiniciado já não conhece. NÃO arma.
		{"holds nao re-hidratados", func(n *Node) { n.holdsRestored = false }, "legal hold NAO foi RE-HIDRATADO"},
	}
	for _, c := range cases {
		n := base()
		c.mutate(n)
		if retentionSchedulerArmed(n, time.Hour) {
			t.Errorf("%s: o scheduler NAO devia armar", c.name)
		}
		banner := retentionSchedulerBanner(n, time.Hour)
		if !strings.Contains(banner, "DORMENTE") {
			t.Errorf("%s: o banner devia declarar o scheduler DORMENTE, veio: %s", c.name, banner)
		}
		if !strings.Contains(banner, c.wantReason) {
			t.Errorf("%s: o banner devia nomear a RAZAO (%q) — uma postura dormente sem causa nao tem remedio. Veio: %s", c.name, c.wantReason, banner)
		}
	}
}

// -------------------------------------------------------------------------------------
// (CA3) Cadência: default declarado; fail-closed em valor inválido; nada a desliga.
// -------------------------------------------------------------------------------------

func TestAOS267_SweepIntervalEnvIsFailClosed(t *testing.T) {
	t.Run("vazia usa o default declarado", func(t *testing.T) {
		t.Setenv("AOS_RETENTION_SWEEP_INTERVAL", "")
		d, err := retentionSweepIntervalFromEnv()
		if err != nil {
			t.Fatalf("vazia nao devia abortar: %v", err)
		}
		if d != DefaultRetentionSweepInterval {
			t.Fatalf("vazia devia dar o default %s, veio %s", DefaultRetentionSweepInterval, d)
		}
	})

	t.Run("valor valido em vigor", func(t *testing.T) {
		t.Setenv("AOS_RETENTION_SWEEP_INTERVAL", "45m")
		d, err := retentionSweepIntervalFromEnv()
		if err != nil {
			t.Fatalf("45m devia ser aceite: %v", err)
		}
		if d != 45*time.Minute {
			t.Fatalf("esperava 45m, veio %s", d)
		}
	})

	// "0" é a armadilha: no varrimento de aprovações DESLIGA, aqui ABORTA — com a política de
	// retenção definida, um scheduler desligado é exactamente a violação de storage-limitation
	// que este ticket fecha.
	for _, mau := range []string{"0", "0s", "-5m", "abc", "1", "5 m"} {
		t.Run("aborta em "+mau, func(t *testing.T) {
			t.Setenv("AOS_RETENTION_SWEEP_INTERVAL", mau)
			if _, err := retentionSweepIntervalFromEnv(); !errors.Is(err, ErrBadRetentionSweepInterval) {
				t.Fatalf("AOS_RETENTION_SWEEP_INTERVAL=%q devia abortar com ErrBadRetentionSweepInterval, veio %v", mau, err)
			}
		})
	}
}

// -------------------------------------------------------------------------------------
// SERIALIZAÇÃO — rota e scheduler partilham UM guard (nunca correm em simultâneo).
// -------------------------------------------------------------------------------------

func TestAOS267_RouteAndSchedulerShareOneGuard(t *testing.T) {
	ctx := context.Background()
	node := newRetentionNode(t)
	const subject = "nhi:agent-guard"
	captureSynthetic(t, node, subject, "run-guard", "conteudo guard: GUARD-404", "outGuard")
	sealed, _ := sealedContentOf(t, node, "run-guard")

	svc := newScheduledRetentionService(t, node, time.Hour)
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}

	// (a) Com uma passagem "em curso" (o guard do SERVIÇO detido), a ROTA recusa 409 — prova que
	// o handler passou a ler o guard do serviço, e não um seu.
	svc.expireInFlight.Store(true)
	if rec := postReq(h, "/dsar/expire", nil, govHeaders()); rec.Code != http.StatusConflict {
		t.Fatalf("com uma passagem em curso a rota devia dar 409, veio %d (%s)", rec.Code, rec.Body.String())
	}

	// (b) E o SCHEDULER salta a sua passagem em vez de correr em paralelo: nada é destruido e
	// nenhum selo novo entra na cadeia.
	head0, err := node.WORM.Head(ctx, retentionPartition)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if !svc.SweepRetentionNow(ctx) {
		t.Fatal("saltar a passagem nao e um incidente de integridade")
	}
	head1, err := node.WORM.Head(ctx, retentionPartition)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head1 != head0 {
		t.Fatalf("a passagem saltada nao devia selar nada, a cadeia passou de %d para %d", head0, head1)
	}
	if _, err := audit.OpenContent(node.DSARVault, subject, sealed); err != nil {
		t.Fatalf("a passagem saltada nao devia destruir nada: %v", err)
	}

	// (c) NÃO-VÁCUO: libertado o guard, a rota volta a 200 e a expiração acontece.
	svc.expireInFlight.Store(false)
	if rec := postReq(h, "/dsar/expire", nil, govHeaders()); rec.Code != http.StatusOK {
		t.Fatalf("libertado o guard a rota devia dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if _, err := audit.OpenContent(node.DSARVault, subject, sealed); !errors.Is(err, audit.ErrDecrypt) {
		t.Fatalf("apos a passagem pela rota o titular devia ficar irrecuperavel, deu: %v", err)
	}
}
