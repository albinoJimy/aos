package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	pdp "github.com/aos-ref/control-plane/pdp"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/signing"
)

// AOS-245 — O OUTPUT DA TOOL EM CLARO NO WAL (protecção de dados).
//
// O defeito: o step-ledger memoriza o Result.Payload — o OUTPUT de cada tool call — no evento
// durável "step.ledger.applied". A cifra por-titular de AOS-093 estava COMPOSTA no nó
// (bootstrap.go: durable.WithContentSealer(contentCipher)) mas o TITULAR nunca lá chegava: o
// ledger lia-o de l.producer.NHIID e o bootstrap não passava produtor nenhum. Com subject == ""
// a selagem é saltada e o payload é persistido TAL-QUAL — os MESMOS bytes ficavam cifrados em
// "replay.captured" (o capturer passa Subject: goal.Principal.NHIID) e EM CLARO em
// "step.ledger.applied". Activo com AOS_DURABLE_EXECUTION=1 — a configuração exigida em produção
// com four-eyes (ver ErrProductionNeedsDurableApproval).
//
// O fix leva o titular POR-RUN ao ledger pelo contexto do despacho
// ([durable.ContextWithTitular], anexado pelo activity.Dispatcher a partir do Principal da
// activity — o MESMO valor que o loop dá ao capturer) e compõe o ledger do nó com a guarda
// fail-closed [durable.WithRequireTitular]: com cifrador composto, um passo sem titular é
// RECUSADO antes de qualquer efeito em vez de degradar em silêncio para texto-claro.
//
// Testes NÃO-VACUOSOS (correm com -race):
//
//	(T1) FUGA ANTES DO FIX (falsificabilidade): um ledger composto EXACTAMENTE como o
//	     bootstrap o compunha — cifrador SIM, titular NÃO — deixa o output da tool em CLARO no
//	     WAL. É a âncora: prova que o marcador é observável no WAL quando não é selado, logo a
//	     ausência dele em (T2) mede selagem e não outra coisa.
//	(T2) O FIX ponta-a-ponta: um run REAL do nó com uma tool cujo output traz um marcador
//	     sintético ⇒ nada em claro no WAL, registo do ledger SELADO sob o titular do run,
//	     recuperável antes do erase e IRRECUPERÁVEL depois (crypto-shredding, GDPR Art. 17).
//	(T3) GUARDA FAIL-CLOSED: com cifrador + WithRequireTitular e SEM titular resolvível, Apply
//	     devolve ErrNoTitular, o efeito NUNCA corre e nada é persistido.
//	(T4) ALCANCE DA EXPIRAÇÃO: a projecção de retenção do nó passa a enumerar
//	     "step.ledger.applied" (antes só via "replay.captured"), pelo que o conteúdo do ledger
//	     conta para o TTL do titular.

// aos245Marker é o marcador sintético (nunca dado real) que a tool devolve como OUTPUT e que,
// sob o fix, NUNCA pode aparecer em claro no WAL do Event Store.
const aos245Marker = "OUTPUT-SINTETICO-TOOL: dossier de SUJEITO-KRAM-4512 no caso SYNTH-245"

// aos245WALLeak procura o marcador no WAL nas DUAS formas em que texto-claro lá pode ficar e
// devolve a que encontrou (vazio ⇒ não vazou). A forma base64 é ESSENCIAL e não é um detalhe:
// [durable.ledgerRecord].Result é um []byte, que o encoding/json serializa em BASE64 — um grep
// só pelo texto literal daria VERDE sobre um WAL que carrega o output da tool integralmente
// recuperável (base64 não é cifra). É o falso-verde que este helper fecha.
func aos245WALLeak(wal []byte, marker string) string {
	if bytes.Contains(wal, []byte(marker)) {
		return "texto literal"
	}
	if bytes.Contains(wal, []byte(base64.StdEncoding.EncodeToString([]byte(marker)))) {
		return "base64 (JSON de []byte)"
	}
	return ""
}

// aos245ToolModel emite UMA tool call no primeiro turno e conclui no segundo — o mínimo para
// pôr um output de tool no step-ledger. Uma instância por run.
type aos245ToolModel struct {
	mu    sync.Mutex
	turns int
}

func (m *aos245ToolModel) Call(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	m.mu.Lock()
	m.turns++
	turn := m.turns
	m.mu.Unlock()

	if turn == 1 {
		return agentruntime.ModelResponse{
			ToolCalls: []agentruntime.ToolInvocation{{
				ToolID:     "counter",
				Capability: durCap,
				Input:      []byte("pedido"),
			}},
			Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
		}, nil
	}
	return agentruntime.ModelResponse{
		Text:  "run concluido",
		Final: true,
		Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

// newAOS245Node compõe um nó DURÁVEL (WAL do ES + WORM em disco) com a cadeia real de mediação
// que uma tool call precisa de atravessar (catálogo assinado + revalidação + PDP de referência +
// autoridade de scope), o modelo dado, e devolve também o caminho do WAL e a credencial do run.
// É o molde de [TestNode_DurableExecution_NoDoubleExecAfterRestart], reduzido a uma só vida.
func newAOS245Node(t *testing.T, model agentruntime.ModelClient) (node *Node, esPath, credential string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	signer := durSigner(t)
	entry := counterEntry(t, signer)

	auditStore := audit.NewMemStore()
	trust, err := signing.NewTrustStore(auditStore)
	if err != nil {
		t.Fatalf("trust store: %v", err)
	}
	if err := trust.Add(ctx, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatalf("trust add: %v", err)
	}
	revalidator, err := revalidation.New(trust, auditStore)
	if err != nil {
		t.Fatalf("revalidator: %v", err)
	}

	esPath = filepath.Join(dir, "events.wal")
	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = esPath
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.Model = model
	cfg.Catalog = catalogStub{entries: []domain.Entry{entry}}
	cfg.Revalidator = revalidator
	cfg.IssuerClasses = map[string]identity.ClassPolicy{
		durClass: {TTL: 15 * time.Minute, Scope: []string{durCap}},
	}
	cfg.Policy = integration.StaticPolicy{MaxEgress: domain.EgressInternal}
	cfg.PDP, err = pdp.Open("../../control-plane/pdp/policies")
	if err != nil {
		t.Fatalf("abrir bundle de política de referência: %v", err)
	}
	cfg.Authority = authz.NewStaticAuthoritySource().
		Set("human:"+tnHuman, durCap).
		Set(durAgent, durCap).
		Set("agent:"+durClass, durCap)

	node, err = Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (durável AOS-245): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })

	tok, err := node.Authority.MintForHuman(ctx, tnHuman, durAgent, durClass, []string{durCap})
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return node, esPath, tok.Compact
}

// aos245LedgerRecord devolve o registo "step.ledger.applied" ÚNICO do stream do run, na forma de
// wire (o que ficou REALMENTE no WAL): resultado (claro ou ciphertext), marca de selagem e
// titular. Falha se não houver exactamente um.
func aos245LedgerRecord(t *testing.T, es EventStorePort, runID string) (result []byte, sealed bool, subject string) {
	t.Helper()
	events, err := es.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("ler o stream %q: %v", runID, err)
	}
	var found int
	for _, e := range events {
		if e.Type != durable.EventTypeLedgerApplied {
			continue
		}
		var p struct {
			Result  []byte `json:"result"`
			Sealed  bool   `json:"sealed"`
			Subject string `json:"subject"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("unmarshal ledgerRecord: %v", err)
		}
		result, sealed, subject = p.Result, p.Sealed, p.Subject
		found++
	}
	if found != 1 {
		t.Fatalf("esperava EXACTAMENTE 1 evento %q no stream %q, vieram %d",
			durable.EventTypeLedgerApplied, runID, found)
	}
	return result, sealed, subject
}

// ---------------------------------------------------------------------------
// (T1) FUGA ANTES DO FIX (não-vacuidade): cifrador composto SEM titular ⇒ output EM CLARO.
// ---------------------------------------------------------------------------

// TestNode_AOS245_LeakWithoutTitular_Falsifiable prova que a fuga é REAL e observável: um ledger
// composto sobre o Event Store do nó EXACTAMENTE como o bootstrap o compunha antes do fix —
// WithContentSealer(contentCipher) e NENHUM titular — persiste o Result.Payload EM CLARO no WAL.
// Sem esta âncora, a ausência do marcador em (T2) poderia significar apenas "o marcador nunca
// chegou ao WAL", e não "chegou cifrado".
func TestNode_AOS245_LeakWithoutTitular_Falsifiable(t *testing.T) {
	ctx := context.Background()
	node, esPath := newDurableNode(t)
	const runID = "run-245-leak"

	// O MESMO cifrador por-titular que o nó compõe (mesmo vault, mesmo índice) — só falta o
	// titular, tal como faltava no bootstrap. Sem WithRequireTitular: é a composição ANTIGA.
	cipher := newContentSealer(node.DSARVault, audit.NewInMemorySubjectPartitionIndex())
	ledger, err := durable.NewStepLedger(node.EventStore, durable.WithContentSealer(cipher))
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}
	key, err := durable.IdempotencyKey(runID, "step-000001")
	if err != nil {
		t.Fatalf("IdempotencyKey: %v", err)
	}
	if _, _, err := ledger.Apply(ctx, key, func(context.Context) (durable.Result, error) {
		return durable.Result{Status: "ok", Payload: []byte(aos245Marker)}, nil
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	wal, err := os.ReadFile(esPath)
	if err != nil {
		t.Fatalf("ler WAL: %v", err)
	}
	if aos245WALLeak(wal, aos245Marker) == "" {
		t.Fatal("um ledger com cifrador mas SEM titular devia deixar o output em CLARO no WAL (prova não-vácua da fuga que o fix fecha)")
	}
	// E o que lá está é RECUPERÁVEL por qualquer leitor do WAL: o registo não foi selado e o
	// campo result traz os bytes do output tal-qual.
	result, sealed, subject := aos245LedgerRecord(t, node.EventStore, runID)
	if sealed || subject != "" {
		t.Fatalf("sem titular o registo NÃO devia estar selado, veio sealed=%t subject=%q", sealed, subject)
	}
	if !bytes.Equal(result, []byte(aos245Marker)) {
		t.Fatalf("sem titular o campo result devia trazer o output TAL-QUAL, veio %q", result)
	}
}

// ---------------------------------------------------------------------------
// (T2) O FIX: um run REAL do nó ⇒ output SELADO no WAL sob o titular do run e SHREDDABLE.
// ---------------------------------------------------------------------------

// TestNode_AOS245_ToolOutputSealedInLedger é o teste que FALHA contra o código anterior ao fix:
// um run com execução durável cuja tool devolve um marcador sintético não pode deixar esse
// marcador em claro no WAL. Prova, ponta-a-ponta:
//
//	(a) o marcador NÃO aparece em claro no WAL (grep-no-WAL);
//	(b) o registo "step.ledger.applied" está SELADO sob o titular do RUN (o mesmo NHI-id que o
//	    capturer usa) — não sob a identidade do nó nem sob titular vazio;
//	(c) NÃO-VACUIDADE: o ciphertext do ledger decifra sob esse titular e contém o marcador — ou
//	    seja, o output ESTÁ lá, cifrado, e não simplesmente ausente;
//	(d) SHREDDABLE: o /dsar/erase do titular torna-o irrecuperável (audit.ErrDecrypt) sem mutar
//	    o log — é o alcance do crypto-shredding (GDPR Art. 17) sobre o ledger, que era o que
//	    faltava.
func TestNode_AOS245_ToolOutputSealedInLedger(t *testing.T) {
	ctx := context.Background()
	node, esPath, credential := newAOS245Node(t, &aos245ToolModel{})

	const runID = "run-245-sealed"
	titular := "nhi:" + runID

	var execs int64
	if err := node.Runtime.Register("counter", func(context.Context, []byte) ([]byte, error) {
		atomic.AddInt64(&execs, 1)
		return []byte(aos245Marker), nil
	}); err != nil {
		t.Fatalf("Register(counter): %v", err)
	}

	res, _, err := node.Runtime.Run(ctx, agentruntime.Goal{
		RunID:      runID,
		Principal:  referencemonitor.Principal{NHIID: titular},
		Credential: credential,
		Objective:  "produzir um output com marcador",
		MaxTurns:   4,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Terminated {
		t.Fatalf("o run não terminou: %+v", res)
	}
	// NÃO-VACUIDADE do próprio run: a tool CORREU (senão não haveria output a proteger) e o
	// resultado que voltou ao loop é o marcador.
	if got := atomic.LoadInt64(&execs); got != 1 {
		t.Fatalf("a tool devia ter corrido exactamente 1 vez, correu %d — sem efeito não há output a selar", got)
	}
	if len(res.ToolResults) != 1 || !bytes.Contains(res.ToolResults[0].Value, []byte(aos245Marker)) {
		t.Fatalf("o loop devia ter recebido o marcador como resultado da tool, veio %+v", res.ToolResults)
	}

	// (a) CONFIDENCIALIDADE: nada em claro no WAL.
	wal, err := os.ReadFile(esPath)
	if err != nil {
		t.Fatalf("ler WAL: %v", err)
	}
	for _, needle := range []string{aos245Marker, "SUJEITO-KRAM-4512"} {
		if form := aos245WALLeak(wal, needle); form != "" {
			t.Fatalf("o WAL contém o output da tool EM CLARO (%q, forma: %s) — o step-ledger não selou por-titular (AOS-245)", needle, form)
		}
	}

	// (b) O registo do ledger está SELADO sob o titular DO RUN.
	sealedResult, sealed, subject := aos245LedgerRecord(t, node.EventStore, runID)
	if !sealed {
		t.Fatal("o registo step.ledger.applied devia estar marcado como selado (sealed=true)")
	}
	if subject != titular {
		t.Fatalf("o titular do registo do ledger devia ser o principal do run %q, veio %q", titular, subject)
	}

	// (c) NÃO-VACUIDADE da selagem: o ciphertext decifra sob o titular e traz o marcador.
	plain, err := audit.OpenContent(node.DSARVault, titular, sealedResult)
	if err != nil {
		t.Fatalf("OpenContent do resultado do ledger sob o titular do run: %v", err)
	}
	if !bytes.Contains(plain, []byte(aos245Marker)) {
		t.Fatal("o resultado decifrado do ledger devia conter o marcador (prova de que o output ESTÁ lá, cifrado)")
	}

	// (d) SHREDDABLE: o erase do titular torna o output do ledger irrecuperável.
	if _, err := node.DSAR.Receive(ctx, dsar.Request{RequestID: "req-245", SubjectID: titular}); err != nil {
		t.Fatalf("DSAR erase do titular: %v", err)
	}
	if _, err := audit.OpenContent(node.DSARVault, titular, sealedResult); !errors.Is(err, audit.ErrDecrypt) {
		t.Fatalf("após o erase, o resultado do ledger devia ser irrecuperável (ErrDecrypt), deu: %v", err)
	}
	walAfter, err := os.ReadFile(esPath)
	if err != nil {
		t.Fatalf("ler WAL após erase: %v", err)
	}
	if bytes.Contains(walAfter, []byte(aos245Marker)) {
		t.Fatal("o marcador apareceu no WAL após o erase — impossível se tivesse sido cifrado")
	}
}

// ---------------------------------------------------------------------------
// (T3) GUARDA FAIL-CLOSED: cifrador composto + titular ausente ⇒ recusa ANTES do efeito.
// ---------------------------------------------------------------------------

// TestNode_AOS245_RequireTitularFailsClosed prova que a degradação silenciosa deixou de ser
// possível na composição do nó: com o cifrador composto e [durable.WithRequireTitular] — a
// composição REAL do bootstrap — um Apply sem titular resolvível é RECUSADO com ErrNoTitular, o
// efeito NUNCA corre e NADA é persistido. E, no mesmo ledger, com o titular no contexto o mesmo
// Apply passa e sela (a guarda recusa a ausência de titular, não o trabalho legítimo).
func TestNode_AOS245_RequireTitularFailsClosed(t *testing.T) {
	ctx := context.Background()
	node, esPath := newDurableNode(t)

	cipher := newContentSealer(node.DSARVault, audit.NewInMemorySubjectPartitionIndex())
	ledger, err := durable.NewStepLedger(node.EventStore,
		durable.WithContentSealer(cipher), durable.WithRequireTitular())
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}

	// (i) SEM titular: recusa antes do efeito.
	var ran int64
	keyDenied, err := durable.IdempotencyKey("run-245-failclosed", "step-000001")
	if err != nil {
		t.Fatalf("IdempotencyKey: %v", err)
	}
	_, _, err = ledger.Apply(ctx, keyDenied, func(context.Context) (durable.Result, error) {
		atomic.AddInt64(&ran, 1)
		return durable.Result{Status: "ok", Payload: []byte(aos245Marker)}, nil
	})
	if !errors.Is(err, durable.ErrNoTitular) {
		t.Fatalf("um Apply sem titular com cifra composta devia dar ErrNoTitular, deu: %v", err)
	}
	if got := atomic.LoadInt64(&ran); got != 0 {
		t.Fatalf("o efeito NÃO devia ter corrido sob a guarda fail-closed, correu %d vezes", got)
	}
	wal, err := os.ReadFile(esPath)
	if err != nil {
		t.Fatalf("ler WAL: %v", err)
	}
	if form := aos245WALLeak(wal, aos245Marker); form != "" {
		t.Fatalf("um Apply recusado NÃO devia ter persistido nada no WAL (forma: %s)", form)
	}

	// (ii) COM titular no contexto: o mesmo ledger aplica e SELA (a guarda não é um bloqueio geral).
	const titular = "nhi:agente-245-ok"
	runID := "run-245-failclosed-ok"
	keyOK, err := durable.IdempotencyKey(runID, "step-000001")
	if err != nil {
		t.Fatalf("IdempotencyKey: %v", err)
	}
	if _, applied, err := ledger.Apply(durable.ContextWithTitular(ctx, titular), keyOK,
		func(context.Context) (durable.Result, error) {
			return durable.Result{Status: "ok", Payload: []byte(aos245Marker)}, nil
		}); err != nil || !applied {
		t.Fatalf("Apply com titular no contexto: applied=%t err=%v", applied, err)
	}
	_, sealed, subject := aos245LedgerRecord(t, node.EventStore, runID)
	if !sealed || subject != titular {
		t.Fatalf("com titular no contexto o registo devia ficar selado sob %q, veio sealed=%t subject=%q", titular, sealed, subject)
	}
}

// ---------------------------------------------------------------------------
// (T4) ALCANCE DA EXPIRAÇÃO: a projecção de retenção enumera "step.ledger.applied".
// ---------------------------------------------------------------------------

// TestNode_AOS245_RetentionSourceCoversLedger prova que a fonte de registos expiráveis do nó
// (AOS-213) deixou de ver APENAS "replay.captured": um registo do step-ledger selado por-titular
// é agora enumerado como [audit.ExpirableRecord] da classe pii_operational, com o titular e a
// partição certos. Sem isto, um titular cujo conteúdo sensível vivesse no ledger nunca faria o
// relógio do TTL correr — o crypto-shred alcança-o (a KEK é a mesma), mas o job nunca o veria.
func TestNode_AOS245_RetentionSourceCoversLedger(t *testing.T) {
	ctx := context.Background()
	node, _ := newDurableNode(t)

	const titular = "nhi:agente-245-retencao"
	const runID = "run-245-retencao"

	cipher := newContentSealer(node.DSARVault, audit.NewInMemorySubjectPartitionIndex())
	ledger, err := durable.NewStepLedger(node.EventStore,
		durable.WithContentSealer(cipher), durable.WithRequireTitular())
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}
	key, err := durable.IdempotencyKey(runID, "step-000001")
	if err != nil {
		t.Fatalf("IdempotencyKey: %v", err)
	}
	if _, _, err := ledger.Apply(durable.ContextWithTitular(ctx, titular), key,
		func(context.Context) (durable.Result, error) {
			return durable.Result{Status: "ok", Payload: []byte(aos245Marker)}, nil
		}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	records, err := (eventStoreRecordSource{es: node.EventStore}).List(ctx)
	if err != nil {
		t.Fatalf("List da fonte de retenção: %v", err)
	}
	var found bool
	for _, r := range records {
		if r.SubjectID != titular {
			continue
		}
		found = true
		if r.Class != audit.ClassPIIOperational {
			t.Fatalf("classe do registo do ledger devia ser %q, veio %q", audit.ClassPIIOperational, r.Class)
		}
		if r.Partition != runID {
			t.Fatalf("partição devia ser o stream do run %q, veio %q", runID, r.Partition)
		}
		if r.CreatedAt.IsZero() {
			t.Fatal("o carimbo de criação devia vir do ts observacional do evento")
		}
	}
	if !found {
		t.Fatalf("a fonte de retenção devia enumerar o registo %q do titular %q (alcance do TTL sobre o step-ledger)",
			durable.EventTypeLedgerApplied, titular)
	}
}
