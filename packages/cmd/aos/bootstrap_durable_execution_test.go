package main

import (
	"context"
	"crypto/ed25519"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pdp "github.com/aos-ref/control-plane/pdp"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/signing"
	"github.com/aos-ref/substrate/eventstore"
)

const (
	durClass = "agent-worker" // classe presente na allowlist da política de referência
	durCap   = "cap:fs.read"  // capability permitida por allowlist + regra Cedar
	durAgent = "agt-1"        // agentID do token NHI; tem de constar da AuthoritySource
)

// catalogStub é um [toolset.Catalog] determinista para testes de AOS-180.
type catalogStub struct{ entries []domain.Entry }

func (c catalogStub) ActiveEntries(context.Context) ([]domain.Entry, error) {
	out := make([]domain.Entry, len(c.entries))
	copy(out, c.entries)
	return out, nil
}

// durPublisherKeyID identifica o publicador de confiança do teste.
const durPublisherKeyID = "pub:aos-180-test"

// durSigner devolve um assinante Ed25519 determinístico para os artefactos do teste.
func durSigner(t *testing.T) *signing.Signer {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	s, err := signing.NewSigner(durPublisherKeyID, ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}

// counterEntry constrói uma entry coerente: digest SHA-256 do contrato e assinatura
// sobre (id, version, digest), como exige a revalidação de AOS-051.
func counterEntry(t *testing.T, signer *signing.Signer) domain.Entry {
	t.Helper()
	contract := domain.Contract{Egress: domain.EgressNone}
	dig := digest.SHA256Digester{}.Digest(domain.KindTool, contract)
	return domain.Entry{
		ID:        "counter",
		Version:   domain.Version{Major: 1, Minor: 0, Patch: 0},
		Kind:      domain.KindTool,
		Digest:    dig,
		Signature: signer.Sign("counter", domain.Version{Major: 1, Minor: 0, Patch: 0}, dig),
		Contract:  contract,
		Provenance: domain.Provenance{
			Origin:    "mcp://aos-180-test",
			Publisher: signer.KeyID(),
			Timestamp: "2026-07-25T00:00:00Z",
			Trust:     domain.TrustFirstSeen,
		},
		Status: domain.StatusActive,
	}
}

// twoTurnToolModel emite uma tool call no primeiro turno e uma resposta final no
// segundo — o mínimo para exercitar a execução e deduplicação duráveis.
//
// AVISO DE VACUIDADE (AOS-192, achado VAC-01 da auditoria v4). O contador é
// POR-INSTÂNCIA e MONOTÓNICO: partilhar UMA instância por DUAS vidas do nó faz a 2.ª
// vida entrar em turn>=3, devolver Final e NUNCA emitir tool call. Qualquer asserção
// de "não houve dupla execução" sobre essa 2.ª vida passaria por a tool nunca ter sido
// RE-TENTADA — não por o ledger ter deduplicado — e passaria na mesma com a
// deduplicação partida. Cada vida do nó TEM de receber uma instância NOVA (ver
// [restartToolModel] e [TestNode_DurableExecution_NoDoubleExecAfterRestart]).
type twoTurnToolModel struct{ calls int32 }

func (m *twoTurnToolModel) Call(_ context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	turn := atomic.AddInt32(&m.calls, 1)
	if turn == 1 {
		return agentruntime.ModelResponse{
			ToolCalls: []agentruntime.ToolInvocation{{
				ToolID:     "counter",
				Capability: durCap,
				Input:      []byte("tick"),
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

// restartToolModel é o modelo do teste de restart. Emite uma tool call no PRIMEIRO
// turno DESTA INSTÂNCIA e conclui nos seguintes — logo, dando a cada vida do nó uma
// instância NOVA, a 2.ª vida VOLTA a emitir a tool call e o ledger é REALMENTE posto à
// prova (era exactamente isto que faltava: ver o aviso em [twoTurnToolModel]).
//
// Instrumentação (AOS-192): conta as tool calls EMITIDAS (para o teste poder asserir que
// a 2.ª vida TENTOU) e guarda o prompt materializado de cada turno (para o teste poder
// asserir que a tool call foi mesmo DESPACHADA e que o resultado que voltou ao loop foi o
// canónico MEMORIZADO — não para detectar re-execução, que é papel do contador do efeito).
type restartToolModel struct {
	mu        sync.Mutex
	turns     int
	toolCalls int
	prompts   []string
}

func (m *restartToolModel) Call(_ context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	m.mu.Lock()
	m.turns++
	turn := m.turns
	m.prompts = append(m.prompts, string(view.Materialized))
	if turn == 1 {
		m.toolCalls++
	}
	m.mu.Unlock()

	if turn == 1 {
		return agentruntime.ModelResponse{
			ToolCalls: []agentruntime.ToolInvocation{{
				ToolID:     "counter",
				Capability: durCap,
				Input:      []byte("tick"),
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

// emitted devolve quantas tool calls esta instância EMITIU.
func (m *restartToolModel) emitted() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.toolCalls
}

// sawInPrompt indica se algum prompt materializado que ESTA instância recebeu contém
// needle. É por aqui que o teste observa QUE bytes de resultado de tool voltaram ao
// loop (o tail do prompt materializa o resultado — ver agent-runtime/prompt.go,
// tailFromResult).
func (m *restartToolModel) sawInPrompt(needle string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.prompts {
		if strings.Contains(p, needle) {
			return true
		}
	}
	return false
}

// durLedgerKeyFromWAL lê o stream do run no Event Store e devolve a idempotency key
// CANÓNICA do único registo de ledger persistido (evento "step.ledger.applied"). A
// chave é derivada do que foi REALMENTE commitado — o step_id do envelope, namespaced
// com o prefixo reservado do ledger — em vez de hardcoded, para o teste não depender do
// formato de step_id do loop. Falha se não houver exactamente um registo.
func durLedgerKeyFromWAL(t *testing.T, es *eventstore.Store, runID string) string {
	t.Helper()
	events, err := es.Read(context.Background(), runID, 0)
	if err != nil {
		t.Fatalf("ler o stream %q do Event Store: %v", runID, err)
	}
	var keys []string
	for _, e := range events {
		if e.Type != durable.EventTypeLedgerApplied {
			continue
		}
		// O ledger grava o envelope com step_id = "ledger-" + step_id do passo; a chave
		// do ledger é run_id + ":" + step_id do PASSO (ver durable/step_ledger.go).
		step := strings.TrimPrefix(e.StepID, "ledger-")
		key, kerr := durable.IdempotencyKey(runID, step)
		if kerr != nil {
			t.Fatalf("IdempotencyKey(%q, %q): %v", runID, step, kerr)
		}
		keys = append(keys, key)
	}
	if len(keys) != 1 {
		t.Fatalf("esperava EXACTAMENTE 1 registo %q no WAL do run %q, vieram %d (%v)",
			durable.EventTypeLedgerApplied, runID, len(keys), keys)
	}
	return keys[0]
}

// TestNode_DurableExecution_NoDoubleExecAfterRestart prova AOS-180 AO NÍVEL DO NÓ:
// quando DurableExecution está activo, o nó persiste o step-ledger no Event Store e,
// após um restart (Close + Bootstrap sobre o mesmo WAL), uma tool call RE-EMITIDA pelo
// modelo é DEDUPLICADA pelo ledger reconstruído em vez de re-executada.
//
// # Porque este teste é NÃO-VACUOSO (AOS-192, achado VAC-01)
//
// A versão anterior partilhava UMA instância de modelo pelas duas vidas do nó. Como o
// contador do modelo é monotónico, a 2.ª vida entrava em turn>=3 e devolvia Final SEM
// emitir tool call: a asserção `execs == 1` passava porque a tool NUNCA FOI RE-TENTADA,
// não porque o ledger deduplicou — e teria passado na mesma com a deduplicação partida.
//
// A correcção reconstitui a condição real e ASSERE-A explicitamente, distinguindo
// "não re-executou porque DEDUPLICOU" de "não re-executou porque NUNCA TENTOU":
//
//	(1) a 2.ª vida recebe uma instância NOVA do modelo e o teste assere que ela EMITIU
//	    >= 1 tool call (sem isto, tudo o resto seria vacuoso);
//	(2) o ledger do nó NOVO está VAZIO para a chave antes de RebuildLedger e passa a
//	    already-applied DEPOIS — [durable.StepLedger.Applied] é a API exacta que expõe o
//	    "already-applied", e o único caminho que a povoa é a releitura do WAL;
//	(3) o efeito registado NESTA 2.ª vida NUNCA corre (execs2 == 0) e o total mantém-se 1
//	    — é ESTA a asserção que detecta re-execução;
//	(4) a tool call foi mesmo DESPACHADA e o que voltou ao loop foi o resultado MEMORIZADO
//	    da 1.ª vida ("pong-vida-1"), não um erro — fecha a via de falso-verde "nunca chegou
//	    a ser despachada". NÃO detecta re-execução (ver o limite documentado na asserção);
//	(5) o WAL contém EXACTAMENTE UM registo "step.ledger.applied" para a chave.
//
// Prova negativa registada no relatório do ticket: partir a verificação already-applied
// de [durable.StepLedger.Apply] torna este teste VERMELHO em (3).
func TestNode_DurableExecution_NoDoubleExecAfterRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	signer := durSigner(t)
	entry := counterEntry(t, signer)

	// Revalidador com trust store que confia no publicador do teste.
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

	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed") // chave estável entre reinícios
	// Uma instância de modelo POR VIDA do nó — o estado NÃO atravessa o restart.
	model1 := &restartToolModel{}
	cfg.Model = model1
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

	// Contadores SEPARADOS por vida: execs1 é o efeito da 1.ª vida, execs2 o da 2.ª. É a
	// separação que permite dizer que a 2.ª vida NÃO correu o efeito (execs2 == 0) em vez
	// de apenas "o total não subiu".
	var execs1, execs2 int64
	const (
		payload1 = "pong-vida-1" // resultado memorizado pelo ledger na 1.ª vida
		payload2 = "pong-vida-2" // resultado que SÓ apareceria se a tool re-executasse
	)
	runID := "run-durable-restart"
	goal := agentruntime.Goal{
		RunID:     runID,
		Principal: referencemonitor.Principal{NHIID: "nhi:" + runID},
		Objective: "contar",
		MaxTurns:  4,
	}

	// PRIMEIRA VIDA: arranca o nó, emite o token, regista a tool e corre o run.
	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	tok, err := node.Authority.MintForHuman(ctx, tnHuman, durAgent, durClass, []string{durCap})
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	goal.Credential = tok.Compact

	if err := node.Runtime.Register("counter", func(_ context.Context, _ []byte) ([]byte, error) {
		atomic.AddInt64(&execs1, 1)
		return []byte(payload1), nil
	}); err != nil {
		t.Fatalf("register counter: %v", err)
	}

	res, _, err := node.Runtime.Run(ctx, goal, nil)
	if err != nil {
		t.Fatalf("primeira execução: %v", err)
	}
	if !res.Terminated {
		t.Fatalf("primeira execução não terminou: %+v", res)
	}
	if got := model1.emitted(); got != 1 {
		t.Fatalf("a 1.ª vida devia ter EMITIDO 1 tool call, emitiu %d", got)
	}
	if got := atomic.LoadInt64(&execs1); got != 1 {
		t.Fatalf("na primeira execução a tool devia ter corrido 1 vez, correu %d", got)
	}

	// A chave do ledger é a que foi REALMENTE commitada no WAL (não uma hardcoded), e
	// tem de coincidir com a derivação canónica do sub-passo (turno 1, 1.ª tool call).
	ledgerKey := durLedgerKeyFromWAL(t, node.EventStore, runID)
	canonical, err := durable.NewStepSequencer().SubKey(runID, 1, 1)
	if err != nil {
		t.Fatalf("SubKey canónica: %v", err)
	}
	if ledgerKey != canonical {
		t.Fatalf("a chave commitada pelo ledger (%q) diverge da derivação canónica do sub-passo (%q): o step_id do loop e o do ledger deixaram de casar",
			ledgerKey, canonical)
	}

	if err := node.Close(); err != nil {
		t.Fatalf("close (simula crash): %v", err)
	}

	// SEGUNDA VIDA: novo processo sobre o mesmo WAL e chave de issuer. O MODELO É NOVO —
	// é isto que recria a condição real (a tool VOLTA a ser pedida após o restart).
	model2 := &restartToolModel{}
	cfg.Model = model2
	node2, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("bootstrap pós-restart: %v", err)
	}
	defer node2.Close()

	// A tool da 2.ª vida devolve bytes DIFERENTES: se ela correr, o resultado que volta ao
	// loop denuncia-o (ver asserção (4)).
	if err := node2.Runtime.Register("counter", func(_ context.Context, _ []byte) ([]byte, error) {
		atomic.AddInt64(&execs2, 1)
		return []byte(payload2), nil
	}); err != nil {
		t.Fatalf("register counter pós-restart: %v", err)
	}

	// (2) ANTES do rebuild o ledger do processo NOVO não conhece a chave (estado in-memory
	// vazio); DEPOIS do rebuild devolve already-applied com o resultado CANÓNICO da 1.ª
	// vida. O único caminho que povoa este mapa é a releitura do WAL.
	if _, ok := node2.Ledger.Applied(ledgerKey); ok {
		t.Fatalf("um nó recém-arrancado NÃO devia conhecer %q antes de RebuildLedger (o estado in-memory do ledger nasce vazio)", ledgerKey)
	}
	if err := node2.Runtime.RebuildLedger(ctx, runID); err != nil {
		t.Fatalf("rebuild ledger: %v", err)
	}
	memo, ok := node2.Ledger.Applied(ledgerKey)
	if !ok {
		t.Fatalf("depois de RebuildLedger o ledger devia dar ALREADY-APPLIED para %q (reconstruído do WAL)", ledgerKey)
	}
	if got := string(memo.Payload); got != payload1 {
		t.Fatalf("o resultado memorizado devia ser o canónico da 1.ª vida %q, veio %q", payload1, got)
	}

	res2, _, err := node2.Runtime.Run(ctx, goal, nil)
	if err != nil {
		t.Fatalf("segunda execução (retoma): %v", err)
	}
	if !res2.Terminated {
		t.Fatalf("segunda execução não terminou: %+v", res2)
	}

	// (1) A 2.ª vida TENTOU: sem isto, tudo o que se segue seria vacuoso (era este o
	// defeito VAC-01 — a asserção passava por a tool nunca ter sido re-pedida).
	if got := model2.emitted(); got < 1 {
		t.Fatalf("a 2.ª vida do nó tinha de EMITIR >= 1 tool call para o ledger ser posto à prova, emitiu %d — sem re-tentativa a prova de deduplicação é VACUOSA", got)
	}
	// (3) O efeito da 2.ª vida NUNCA correu, e o total mantém-se em 1.
	if got := atomic.LoadInt64(&execs2); got != 0 {
		t.Fatalf("a tool foi RE-EXECUTADA após o restart (%d execuções na 2.ª vida): o ledger não deduplicou a tool call re-emitida", got)
	}
	if got := atomic.LoadInt64(&execs1) + atomic.LoadInt64(&execs2); got != 1 {
		t.Fatalf("esperava 1 execução de tool no total das duas vidas, obtive %d", got)
	}
	// (4) A tool call FOI MESMO DESPACHADA e o que voltou ao loop foi o resultado
	// MEMORIZADO (payload1), não um erro nem um resultado novo: fecha a via de falso-verde
	// "a tool nunca chegou a ser despachada" (catálogo vazio, deny do PDP, run terminado
	// antes do dispatch), que faria (3) passar por vacuidade.
	//
	// LIMITE DESTA ASSERÇÃO — não a promovas a detector de re-execução. Se a guarda
	// already-applied in-memory de [durable.StepLedger.Apply] for partida, o efeito CORRE e
	// mesmo assim payload2 NÃO aparece aqui: o ramo `appendRes.Status == StatusDuplicate` de
	// runEffect (durable/step_ledger.go:321-333) faz o Event Store deduplicar o Append e
	// devolve o registo CANÓNICO da 1.ª vida, pelo que o payload que sobe ao dispatcher e ao
	// tail do prompt é sempre o de payload1. Verificado empiricamente (§3.0 do relatório).
	// Quem detecta re-execução é a asserção (3) (execs2 == 0) — NÃO PODE SER REMOVIDA.
	if !model2.sawInPrompt(payload1) {
		t.Fatalf("o resultado de tool visto pela 2.ª vida devia ser o canónico memorizado %q (devolvido pelo ledger sem novo efeito)", payload1)
	}
	if model2.sawInPrompt(payload2) {
		t.Fatalf("a 2.ª vida observou %q: a tool re-registada EXECUTOU em vez de ser deduplicada pelo ledger", payload2)
	}
	// (5) O WAL continua com EXACTAMENTE UM registo de ledger para a chave.
	if got := durLedgerKeyFromWAL(t, node2.EventStore, runID); got != ledgerKey {
		t.Fatalf("a chave do único registo de ledger mudou entre vidas: %q → %q", ledgerKey, got)
	}
}
