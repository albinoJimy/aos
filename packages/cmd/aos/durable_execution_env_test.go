package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pdp "github.com/aos-ref/control-plane/pdp"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/signing"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-191 — SUPERFÍCIE DE CONFIGURAÇÃO da execução durável (achado REG-01 ≡ STR-09 ≡
// PLA-03 da auditoria v4; DUR-01 da v3 ainda aberto).
//
// O DEFEITO fechado aqui: `Config.DurableExecution` existia, o composition-root compunha
// correctamente checkpointer/capturer/step-ledger a partir dele, e NENHUM caminho do
// BINÁRIO o escrevia (`grep AOS_DURABLE .` → 0; único escritor: um teste). Como
// `Config` vive em `package main`, nem um embedder externo o podia preencher: a
// capacidade era INALCANÇÁVEL no artefacto entregue.
//
// Por isso estes testes atacam a costura ENV → [nodeConfigFromEnv] → [Bootstrap] (o
// caminho do binário), e não apenas o Bootstrap com uma Config escrita à mão — era
// exactamente a ausência dessa costura o defeito.

// dexEnvBase prepara o ambiente MÍNIMO de um nó de referência com substrato durável em
// disco e devolve o directório de estado. Cada teste acrescenta (ou omite)
// AOS_DURABLE_EXECUTION. t.Setenv garante isolamento e restauro entre testes.
func dexEnvBase(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AOS_MODE", "")
	t.Setenv("AOS_ISSUER_ID", "iss:aos-191")
	t.Setenv("AOS_HUMANS", tnHuman)
	t.Setenv("AOS_ISSUER_PUBKEY", "")
	t.Setenv("AOS_API_ADDR", "")
	t.Setenv("AOS_OTLP_ENDPOINT", "")
	t.Setenv("AOS_EVENTSTORE_PATH", filepath.Join(dir, "events.wal"))
	t.Setenv("AOS_WORM_PATH", filepath.Join(dir, "worm.wal"))
	t.Setenv("AOS_ISSUER_KEY_PATH", filepath.Join(dir, "issuer.seed"))
	return dir
}

// dexRunnableConfig completa a config vinda do AMBIENTE com os colaboradores que o
// ambiente NÃO fornece (modelo, catálogo, revalidador, PDP, autoridade de escopo) — os
// mesmos defaults de teste de AOS-180. Tudo o que respeita à EXECUÇÃO DURÁVEL
// (DurableExecution + EventStorePath) vem intacto do ambiente: é isso que está sob prova.
func dexRunnableConfig(t *testing.T, model agentruntime.ModelClient) Config {
	t.Helper()
	ctx := context.Background()

	cfg, err := nodeConfigFromEnv()
	if err != nil {
		t.Fatalf("nodeConfigFromEnv: %v", err)
	}

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
	return cfg
}

// dexRunTool corre UM run com uma tool call sobre o nó e devolve quantas vezes a tool
// executou. É o efeito através do qual se observa se o step-ledger está (ou não) no
// caminho de execução.
func dexRunTool(t *testing.T, node *Node, runID string) int64 {
	t.Helper()
	ctx := context.Background()

	tok, err := node.Authority.MintForHuman(ctx, tnHuman, durAgent, durClass, []string{durCap})
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	var execs int64
	if err := node.Runtime.Register("counter", func(_ context.Context, _ []byte) ([]byte, error) {
		atomic.AddInt64(&execs, 1)
		return []byte("pong"), nil
	}); err != nil {
		t.Fatalf("register counter: %v", err)
	}
	res, _, err := node.Runtime.Run(ctx, agentruntime.Goal{
		RunID:      runID,
		Principal:  referencemonitor.Principal{NHIID: "nhi:" + runID},
		Objective:  "contar",
		MaxTurns:   4,
		Credential: tok.Compact,
	}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Terminated {
		t.Fatalf("o run devia terminar: %+v", res)
	}
	if got := atomic.LoadInt64(&execs); got != 1 {
		t.Fatalf("a tool devia ter corrido exactamente 1 vez, correu %d", got)
	}
	return atomic.LoadInt64(&execs)
}

// dexEventTypes lê o stream do run no Event Store e devolve o conjunto de tipos de evento
// nele persistidos. É a observação do EFEITO da execução durável no substrato.
func dexEventTypes(t *testing.T, es EventStorePort, runID string) map[string]int {
	t.Helper()
	events, err := es.Read(context.Background(), runID, 0)
	if err != nil && !errors.Is(err, eventstore.ErrStreamNotFound) {
		t.Fatalf("ler o stream %q do Event Store: %v", runID, err)
	}
	types := make(map[string]int, len(events))
	for _, e := range events {
		types[e.Type]++
	}
	return types
}

// TestAOS191_DurableExecutionReachableFromEnv prova o FECHO do defeito: com
// AOS_DURABLE_EXECUTION activa (e um Event Store durável), o caminho do BINÁRIO
// (env → nodeConfigFromEnv → Bootstrap) COMPÕE os três colaboradores duráveis E o
// runtime recebe-os de facto.
//
// NÃO-VACUOSO — três asserções de efeito, não "não deu erro":
//   - a config construída a partir do AMBIENTE traz DurableExecution=true (antes de
//     AOS-191 nenhum caminho do binário escrevia este campo);
//   - o nó expõe checkpointer, capturer e step-ledger REAIS (tipos concretos de
//     durable/replay, não interfaces nil);
//   - depois de um run com uma tool call, o Event Store contém os eventos
//     "step.ledger.applied" e "step.checkpoint" — só produzíveis se o RUNTIME estiver
//     realmente a despachar através do ledger e a fazer checkpoint (o teste negativo
//     abaixo mostra que sem a variável eles NÃO aparecem).
func TestAOS191_DurableExecutionReachableFromEnv(t *testing.T) {
	dexEnvBase(t)
	t.Setenv("AOS_DURABLE_EXECUTION", "1")

	cfg := dexRunnableConfig(t, &twoTurnToolModel{})
	if !cfg.DurableExecution {
		t.Fatal("nodeConfigFromEnv devia escrever Config.DurableExecution=true a partir de AOS_DURABLE_EXECUTION=1 (era este o campo inalcancavel pelo binario)")
	}
	if cfg.EventStorePath == "" {
		t.Fatal("a config de ambiente devia trazer o Event Store duravel (AOS_EVENTSTORE_PATH)")
	}

	var banner strings.Builder
	node, err := Bootstrap(context.Background(), cfg, &banner)
	if err != nil {
		t.Fatalf("bootstrap com execucao duravel por ambiente: %v", err)
	}
	defer func() { _ = node.Close() }()

	// (1) Os TRÊS colaboradores duráveis estão compostos — e são os REAIS.
	if node.Checkpointer == nil || node.Capturer == nil || node.Ledger == nil {
		t.Fatalf("os tres colaboradores duraveis deviam estar compostos, veio checkpointer=%v capturer=%v ledger=%v",
			node.Checkpointer, node.Capturer, node.Ledger)
	}
	if _, ok := node.Checkpointer.(*durable.EventStoreCheckpointer); !ok {
		t.Fatalf("checkpointer devia ser o durable.EventStoreCheckpointer, veio %T", node.Checkpointer)
	}
	if _, ok := node.Capturer.(*replay.EventStoreCapturer); !ok {
		t.Fatalf("capturer devia ser o replay.EventStoreCapturer, veio %T", node.Capturer)
	}

	// (2) O RUNTIME recebeu-os: um run com tool call deixa rasto durável no Event Store.
	const runID = "run-aos191-on"
	dexRunTool(t, node, runID)
	types := dexEventTypes(t, node.EventStore, runID)
	if types[durable.EventTypeLedgerApplied] == 0 {
		t.Fatalf("o Event Store devia conter %q (o step-ledger esta no caminho de execucao do runtime), tipos observados: %v",
			durable.EventTypeLedgerApplied, types)
	}
	if types[durable.EventTypeCheckpoint] == 0 {
		t.Fatalf("o Event Store devia conter %q (o checkpointer esta ligado ao runtime), tipos observados: %v",
			durable.EventTypeCheckpoint, types)
	}

	// (3) O banner declara o estado resultante e o substrato.
	if !strings.Contains(banner.String(), "execucao duravel (AOS-180): LIGADA") {
		t.Fatalf("o banner devia declarar a execucao duravel LIGADA, veio:\n%s", banner.String())
	}
	if !strings.Contains(banner.String(), "duravel em disco (AOS-170)") {
		t.Fatalf("o banner devia declarar o substrato duravel em disco, veio:\n%s", banner.String())
	}
}

// TestAOS191_WithoutEnvVarDurableCollaboratorsStayNil prova a RETRO-COMPATIBILIDADE: sem
// AOS_DURABLE_EXECUTION o comportamento é o ACTUAL — os três colaboradores permanecem nil,
// o runtime usa os defaults no-op (AOS-013) e o run corre na mesma. Diferencial face ao
// teste anterior: o MESMO run NÃO deixa eventos de ledger/checkpoint no Event Store.
func TestAOS191_WithoutEnvVarDurableCollaboratorsStayNil(t *testing.T) {
	dexEnvBase(t) // NOTA: AOS_DURABLE_EXECUTION deliberadamente NÃO definida.

	cfg := dexRunnableConfig(t, &twoTurnToolModel{})
	if cfg.DurableExecution {
		t.Fatal("sem AOS_DURABLE_EXECUTION a config NAO devia activar a execucao duravel")
	}

	var banner strings.Builder
	node, err := Bootstrap(context.Background(), cfg, &banner)
	if err != nil {
		t.Fatalf("bootstrap sem execucao duravel: %v", err)
	}
	defer func() { _ = node.Close() }()

	if node.Checkpointer != nil || node.Capturer != nil || node.Ledger != nil {
		t.Fatalf("sem a variavel os tres deviam permanecer nil, veio checkpointer=%v capturer=%v ledger=%v",
			node.Checkpointer, node.Capturer, node.Ledger)
	}

	const runID = "run-aos191-off"
	dexRunTool(t, node, runID) // o run continua a correr — comportamento inalterado.
	types := dexEventTypes(t, node.EventStore, runID)
	if types[durable.EventTypeLedgerApplied] != 0 || types[durable.EventTypeCheckpoint] != 0 {
		t.Fatalf("sem execucao duravel o Event Store NAO devia conter eventos de ledger/checkpoint, tipos observados: %v", types)
	}
	if !strings.Contains(banner.String(), "execucao duravel (AOS-180): DESLIGADA") {
		t.Fatalf("o banner devia declarar a execucao duravel DESLIGADA, veio:\n%s", banner.String())
	}
}

// TestAOS191_RunComposesDurableExecutionFromEnv sela o elo [run] → [nodeConfigFromEnv] →
// [Bootstrap] no caminho FELIZ — o caminho REAL do binário, ponta a ponta e sem mutar a
// config a meio.
//
// PORQUÊ, além dos testes acima: [dexRunnableConfig] chama `nodeConfigFromEnv` e depois
// COMPLETA a config (modelo, catálogo, PDP, autoridade) antes de invocar `Bootstrap`
// directamente — prova a costura env→Config e a composição, mas NÃO prova que é `run` quem
// entrega essa config a `Bootstrap`. E os testes negativos (`enabled`, sem
// AOS_EVENTSTORE_PATH) abortam DENTRO de `nodeConfigFromEnv`, pelo que também nunca
// observam a config a CHEGAR ao composition-root. Sem este teste, um refactor que fizesse
// `run` construir a config por outra via (ou passar uma config zerada) — exactamente a
// classe de rotura que AOS-191 fecha — deixaria toda a suite verde. Molde:
// [TestAOS169_DurableSubstrateWiredFromEnv], que faz o mesmo para AOS_EVENTSTORE_PATH.
func TestAOS191_RunComposesDurableExecutionFromEnv(t *testing.T) {
	dexEnvBase(t)
	t.Setenv("AOS_DURABLE_EXECUTION", "1")

	var banner strings.Builder
	if err := run(&banner); err != nil {
		t.Fatalf("run com AOS_DURABLE_EXECUTION=1 e substrato duravel devia arrancar, veio: %v", err)
	}

	// O banner é a ÚNICA superfície que o binário expõe deste estado (run fecha o nó ao
	// sair), e declara o estado REALMENTE composto — não a intenção da config.
	if !strings.Contains(banner.String(), "execucao duravel (AOS-180): LIGADA") {
		t.Fatalf("run devia entregar a config de ambiente ao Bootstrap e compor a execucao duravel, banner:\n%s", banner.String())
	}
	if !strings.Contains(banner.String(), "checkpointer + capturer + step-ledger COMPOSTOS sobre o event store") {
		t.Fatalf("o banner devia nomear os TRES colaboradores compostos, veio:\n%s", banner.String())
	}
	if !strings.Contains(banner.String(), "duravel em disco (AOS-170)") {
		t.Fatalf("a execucao duravel devia assentar no substrato duravel em disco, banner:\n%s", banner.String())
	}
}

// TestAOS191_RunWithoutEnvVarDeclaresDurableExecutionOff é o par NEGATIVO ao nível de [run]:
// sem a variável, o MESMO caminho do binário arranca (retro-compatibilidade) e o banner
// declara DESLIGADA com a instrução de como ligar — sem esta asserção, "LIGADA" acima
// poderia ser o estado por omissão e o teste positivo seria vacuoso.
func TestAOS191_RunWithoutEnvVarDeclaresDurableExecutionOff(t *testing.T) {
	dexEnvBase(t) // NOTA: AOS_DURABLE_EXECUTION deliberadamente NÃO definida.

	var banner strings.Builder
	if err := run(&banner); err != nil {
		t.Fatalf("run sem AOS_DURABLE_EXECUTION devia arrancar como antes, veio: %v", err)
	}
	if !strings.Contains(banner.String(), "execucao duravel (AOS-180): DESLIGADA") {
		t.Fatalf("sem a variavel o banner devia declarar DESLIGADA, veio:\n%s", banner.String())
	}
	if !strings.Contains(banner.String(), "defina AOS_DURABLE_EXECUTION=1 (exige AOS_EVENTSTORE_PATH)") {
		t.Fatalf("o banner desligado devia dizer ao operador COMO ligar, veio:\n%s", banner.String())
	}
}

// TestParseDurableExecution cobre a semântica do valor da variável: quais ligam, quais
// desligam, e que LIXO ABORTA (fail-closed) em vez de ser tratado como false.
func TestParseDurableExecution(t *testing.T) {
	t.Parallel()
	on := []string{"1", "true", "TRUE", "True", "t", "yes", "y", "on", " on "}
	for _, v := range on {
		got, err := parseDurableExecution(v)
		if err != nil || !got {
			t.Fatalf("parseDurableExecution(%q) devia ligar, veio (%v, %v)", v, got, err)
		}
	}
	off := []string{"", "   ", "0", "false", "FALSE", "f", "no", "n", "off"}
	for _, v := range off {
		got, err := parseDurableExecution(v)
		if err != nil || got {
			t.Fatalf("parseDurableExecution(%q) devia desligar, veio (%v, %v)", v, got, err)
		}
	}
	bad := []string{"tru", "enabled", "sim", "2", "durable", "on;rm -rf"}
	for _, v := range bad {
		got, err := parseDurableExecution(v)
		if !errors.Is(err, ErrBadDurableExecution) {
			t.Fatalf("parseDurableExecution(%q) devia abortar com ErrBadDurableExecution, veio (%v, %v)", v, got, err)
		}
		if got {
			t.Fatalf("parseDurableExecution(%q) nunca pode devolver true num erro (fail-closed)", v)
		}
	}
}

// TestRunRejectsMalformedDurableExecution prova o fail-closed no ENTRYPOINT (o caminho do
// binário): um valor não reconhecido ABORTA o arranque em vez de degradar em silêncio para
// execução NÃO-durável. Simetria com ErrBadIssuerPubKey/ErrBadBoardRegions.
func TestRunRejectsMalformedDurableExecution(t *testing.T) {
	dexEnvBase(t)
	t.Setenv("AOS_DURABLE_EXECUTION", "enabled") // não é um booleano reconhecido

	if err := run(io.Discard); !errors.Is(err, ErrBadDurableExecution) {
		t.Fatalf("AOS_DURABLE_EXECUTION invalida devia abortar com ErrBadDurableExecution, veio: %v", err)
	}
}

// TestRunRejectsDurableExecutionWithoutDurableEventStore prova a SEMÂNTICA escolhida para a
// interacção com AOS_EVENTSTORE_PATH: pedir execução durável sobre um Event Store
// IN-MEMORY RECUSA o arranque (fail-closed SEMPRE, não só em AOS_MODE=production). Sem esta
// guarda o nó anunciaria durabilidade e perderia checkpoints/capturas/ledger no reinício —
// a mesma classe de defeito (capacidade anunciada e não cumprida) que AOS-191 fecha.
func TestRunRejectsDurableExecutionWithoutDurableEventStore(t *testing.T) {
	dexEnvBase(t)
	t.Setenv("AOS_DURABLE_EXECUTION", "true")
	t.Setenv("AOS_EVENTSTORE_PATH", "") // substrato in-memory ⇒ durabilidade seria falsa

	if err := run(io.Discard); !errors.Is(err, ErrDurableExecutionNeedsDurableSubstrate) {
		t.Fatalf("execucao duravel sem Event Store duravel devia abortar com ErrDurableExecutionNeedsDurableSubstrate, veio: %v", err)
	}
}

// TestBootstrapRejectsDurableExecutionOverInMemoryStore prova que a guarda é do
// COMPOSITION-ROOT (não só da fronteira de ambiente): uma Config com DurableExecution mas
// sem substrato durável é recusada por [Bootstrap] ANTES de compor o que quer que seja.
func TestBootstrapRejectsDurableExecutionOverInMemoryStore(t *testing.T) {
	t.Parallel()
	cfg := tnBaseConfig()
	cfg.DurableExecution = true // sem EventStore nem EventStorePath ⇒ in-memory

	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if !errors.Is(err, ErrDurableExecutionNeedsDurableSubstrate) {
		t.Fatalf("Bootstrap devia recusar execucao duravel sobre store in-memory, veio: %v", err)
	}
	if node != nil {
		t.Fatal("um bootstrap recusado NAO devia devolver no (fail-closed)")
	}
}

// TestBootstrapDurableExecutionOverInjectedStoreIsDeclared cobre a fronteira restante: um
// Event Store INJECTADO por config é do chamador e o nó não pode atestar a sua
// durabilidade — não recusa, mas o banner DECLARA a fronteira em vez de a esconder (sem
// isto, um embedder poderia anunciar durabilidade sobre um store volátil em silêncio).
func TestBootstrapDurableExecutionOverInjectedStoreIsDeclared(t *testing.T) {
	t.Parallel()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = es.Close() }()

	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStore = es

	var banner strings.Builder
	node, err := Bootstrap(context.Background(), cfg, &banner)
	if err != nil {
		t.Fatalf("bootstrap com Event Store injectado: %v", err)
	}
	defer func() { _ = node.Close() }()

	if node.Ledger == nil {
		t.Fatal("com Event Store injectado a execucao duravel devia compor-se")
	}
	if !strings.Contains(banner.String(), "FORNECIDO POR CONFIG") {
		t.Fatalf("o banner devia DECLARAR que a durabilidade e a do store injectado, veio:\n%s", banner.String())
	}
}
