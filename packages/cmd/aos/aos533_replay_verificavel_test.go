package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	autonomy "github.com/aos-ref/control-plane/governance/autonomy"
	dsar "github.com/aos-ref/control-plane/governance/dsar"
	pdp "github.com/aos-ref/control-plane/pdp"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	audit "github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	domain "github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// AOS-533 — O REPLAY VERIFICÁVEL DE PRODUÇÃO, no nó composto: `GET /runs/{id}/replay` corre o
// [replay.ReplayEngine.Replay] sobre um run real (execução durável, cifra por-titular, gate
// soberano, four-eyes ⇒ registo de retoma) e devolve o veredicto de fidelidade, sem conteúdo.
//
// Os marcadores abaixo vão no objectivo, na resposta do modelo, no resultado da tool e nos inputs
// do plano. O `/reconstruct` devolve-os (controlo: são decifráveis por um leitor autorizado); o
// `/replay` não pode devolver nenhum.
const (
	aos533Objectivo = "OBJECTIVO-533-SECRETO le o documento"
	aos533Texto     = "TEXTO-DO-MODELO-533"
	aos533Saida     = "SAIDA-DA-TOOL-533"
	aos533Entrada   = "ENTRADA-DO-PLANO-533"
	aos533System    = "SYSTEM-533-SECRETO"
	aos533Memoria   = "MEMORIA-533-SECRETA"
)

// aos533Aprovadores são dois aprovadores pinados: compor o four-eyes é o que compõe o registo de
// retoma (bootstrap), de onde o `/replay` tira a spec.
func aos533Aprovadores(t *testing.T) []ApproverConfig {
	t.Helper()
	pubA, _, _ := ed25519.GenerateKey(rand.Reader)
	pubB, _, _ := ed25519.GenerateKey(rand.Reader)
	return []ApproverConfig{
		{Principal: "human:alice", PubKey: pubA, Authority: []string{"approve:danger", "approve:gray"}},
		{Principal: "human:bob", PubKey: pubB, Authority: []string{"approve:danger", "approve:gray"}},
	}
}

type aos533Opcoes struct {
	// l4 liga o oráculo de autonomia em L4: toda a tool call escala (para o run retomado).
	l4 bool
	// spans compõe a observabilidade (a torneira de spans do avaliador de SLOs).
	spans bool
	// dir e vault, quando dados, são os de uma incarnação ANTERIOR do nó: o mesmo WAL, o mesmo
	// WORM, a mesma chave do emissor e a mesma custódia de KEK — um reinício do nó.
	dir   string
	vault audit.KeyVault
	// svc são opções a mais do serviço (o relógio do lease da incarnação nova).
	svc []NodeServiceOption
}

type aos533No struct {
	node  *Node
	svc   *NodeService
	h     http.Handler
	model *acnModel
	keys  [2]ed25519.PrivateKey
}

// aos533Compor arranca o nó do ciclo de aprovação (approval_cycle_node_test.go) com o gate
// soberano de leitura composto. As tools devolvem o marcador de saída.
func aos533Compor(t *testing.T, o aos533Opcoes, guiao func(agentruntime.PromptView) agentruntime.ModelResponse) *aos533No {
	t.Helper()
	ctx := context.Background()
	dir := o.dir
	if dir == "" {
		dir = t.TempDir()
	}
	signer := acnSigner(t)
	var opts []pdp.Option
	if o.l4 {
		levels := autonomy.NewLevelRegistry()
		if _, err := levels.SetLevel(ctx, acnAgent, "fs", autonomy.L4, "teste do replay de um run retomado", "operador"); err != nil {
			t.Fatal(err)
		}
		opts = append(opts, pdp.WithAutonomyOracle(levels))
	}
	policyDP, err := pdp.Open("../../control-plane/pdp/policies", opts...)
	if err != nil {
		t.Fatal(err)
	}
	pubA, privA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, privB, _ := ed25519.GenerateKey(rand.Reader)
	model := &acnModel{guiao: guiao}
	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.BoardRegions = map[string]string{govBoard: govRegion}
	if o.vault != nil {
		cfg.DSARVault = o.vault
	}
	cfg.Model = model
	cfg.Catalog = catalogStub{entries: []domain.Entry{acnEntry(t, signer, "passo_um"), acnEntry(t, signer, "passo_dois")}}
	cfg.SignedToolRegistry = nodeSignedRegistrySpec(signer, nil)
	cfg.IssuerClasses = map[string]identity.ClassPolicy{acnClass: {TTL: 15 * time.Minute, Scope: []string{acnCap}}}
	cfg.Policy = integration.StaticPolicy{MaxEgress: domain.EgressInternal}
	cfg.PDP = policyDP
	cfg.Authority = authz.NewStaticAuthoritySource().Set("human:"+tnHuman, acnCap).Set(acnAgent, acnCap).Set("agent:"+acnClass, acnCap)
	cfg.Approvers = []ApproverConfig{
		{Principal: "human:alice", PubKey: pubA, Authority: []string{"approve:danger", "approve:gray"}},
		{Principal: "human:bob", PubKey: pubB, Authority: []string{"approve:danger", "approve:gray"}},
	}
	if o.spans {
		cfg.OTLPExporter = &otelgenai.RecordingExporter{}
	}
	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	for _, tool := range []string{"passo_um", "passo_dois"} {
		if err := node.Runtime.Register(tool, func(context.Context, []byte) ([]byte, error) {
			return []byte(`{"content":"` + aos533Saida + `"}`), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := NewNodeService(node, append([]NodeServiceOption{WithSLOEvalInterval(0)}, o.svc...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	if o.spans {
		svc.slo = newSLOEvaluator(node, DefaultSLOEvalInterval, DefaultSLOWindow)
	}
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatal(err)
	}
	return &aos533No{node: node, svc: svc, h: h, model: model, keys: [2]ed25519.PrivateKey{privA, privB}}
}

func (n *aos533No) token(t *testing.T) string {
	t.Helper()
	tok, err := n.node.Authority.MintForHuman(context.Background(), tnHuman, acnAgent, acnClass, []string{acnCap})
	if err != nil {
		t.Fatal(err)
	}
	return tok.Compact
}

// correr submete o Goal (com o principal e a credencial do harness) e espera que saia de em-curso.
func (n *aos533No) correr(t *testing.T, g agentruntime.Goal) {
	t.Helper()
	g.Principal = referencemonitor.Principal{NHIID: acnAgent}
	g.Credential = n.token(t)
	if g.Objective == "" {
		g.Objective = aos533Objectivo
	}
	if g.MaxTurns == 0 {
		g.MaxTurns = 5
	}
	if err := n.svc.Submit(context.Background(), g); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	n.esperar(t, g.RunID)
}

func (n *aos533No) esperar(t *testing.T, runID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, _, err := n.svc.Wait(ctx, runID); err != nil {
		t.Fatalf("Wait(%s): %v", runID, err)
	}
}

// aos533Guiao: turno 1 pede passo_um, turno 2 conclui com o marcador de texto.
func aos533Guiao(v agentruntime.PromptView) agentruntime.ModelResponse {
	if v.Turn == 1 {
		return acnCall("passo_um", "doc-a")
	}
	return agentruntime.ModelResponse{Text: aos533Texto, Final: true}
}

// replay faz o GET e descodifica a resposta (o corpo cru vem também, para as sondas).
func (n *aos533No) replay(t *testing.T, runID string, headers map[string]string) (int, replayResponse, string) {
	t.Helper()
	rec := getReq(n.h, "/runs/"+runID+"/replay", headers)
	var resp replayResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("resposta do /replay nao descodifica: %v (%s)", err, rec.Body.String())
		}
	}
	return rec.Code, resp, rec.Body.String()
}

// aos533SemConteudo falha se o corpo levar algum marcador de conteúdo.
func aos533SemConteudo(t *testing.T, corpo string) {
	t.Helper()
	for _, m := range []string{aos533Objectivo, "OBJECTIVO-533", aos533Texto, aos533Saida, aos533Entrada, aos533System, aos533Memoria} {
		if strings.Contains(corpo, m) {
			t.Fatalf("a resposta do /replay leva conteudo do run (%q): %s", m, corpo)
		}
	}
}

// aos533Fiel exige um replay de fidelidade total sobre `turnos` turnos.
func aos533Fiel(t *testing.T, resp replayResponse, turnos int) {
	t.Helper()
	if resp.Diverged || resp.Divergence != nil {
		t.Fatalf("o replay nao podia divergir: %+v", resp.Divergence)
	}
	if resp.Fidelity.Value != 1 || resp.Fidelity.Matched != turnos || resp.Fidelity.Verified != turnos || len(resp.Turns) != turnos {
		t.Fatalf("fidelidade total sobre %d turnos esperada: %+v (turnos=%d)", turnos, resp.Fidelity, len(resp.Turns))
	}
	for _, tw := range resp.Turns {
		if !tw.Matched || tw.PromptHash == "" || tw.PromptHash != tw.RecordedPromptHash {
			t.Fatalf("turno verificado sem coincidencia de prompt_hash: %+v", tw)
		}
	}
	if resp.SpecOrigin != specOriginResumeRecord || resp.FinalStateHash == "" {
		t.Fatalf("spec_origin/final_state_hash: %+v", resp)
	}
	if len(resp.AnchorsVerified) != 0 || len(resp.AnchorsNotVerified) != len(anchorsDoReplay)+len(anchorsSemFonteIndependente) {
		t.Fatalf("as ancoras nao-prompt nao correm e tem de ser declaradas: verified=%v not=%v", resp.AnchorsVerified, resp.AnchorsNotVerified)
	}
}

// TestAOS533_RunNormal_FidelidadeTotal_SeloESemConteudo: o caminho feliz, com o selo D6 próprio e
// a sonda de conteúdo — e o controlo de que os marcadores SÃO decifráveis (pelo /reconstruct).
func TestAOS533_RunNormal_FidelidadeTotal_SeloESemConteudo(t *testing.T) {
	ctx := context.Background()
	n := aos533Compor(t, aos533Opcoes{}, aos533Guiao)
	const runID = "run-533-normal"
	n.correr(t, agentruntime.Goal{RunID: runID})

	part := readAuditPartition(runID)
	if head, _ := n.node.WORM.Head(ctx, part); head != 0 {
		t.Fatalf("a particao de leitura devia estar vazia antes do replay, head=%d", head)
	}
	code, resp, corpo := n.replay(t, runID, govHeaders())
	if code != http.StatusOK {
		t.Fatalf("o replay autorizado devia dar 200, veio %d (%s)", code, corpo)
	}
	aos533Fiel(t, resp, 2)
	if resp.RunID != runID || resp.Retomado || resp.Nota != "" || resp.SLIAmostrado {
		t.Fatalf("run de uma vida, sem torneira: %+v", resp)
	}
	aos533SemConteudo(t, corpo)

	// O SELO D6 é o do replay, e só um: a capability distingue-o do /reconstruct.
	head, err := n.node.WORM.Head(ctx, part)
	if err != nil || head != 1 {
		t.Fatalf("o replay sela UMA leitura no WORM: head=%d err=%v", head, err)
	}
	seal, ok, err := n.node.WORM.At(ctx, part, 1)
	if err != nil || !ok || seal.Capability != capReadReplay || seal.Principal.NHIID != govReader || seal.Resource.Value != runID {
		t.Fatalf("selo do replay: ok=%v err=%v %+v", ok, err, seal)
	}
	assertNoPIIInPartition(t, n.node.WORM, part, aos533Objectivo, aos533Texto, aos533Saida)

	// CONTROLO: os marcadores existem e decifram-se — a sonda acima não é vácua.
	rec := getReq(n.h, "/runs/"+runID+"/reconstruct", govHeaders())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), aos533Texto) || !strings.Contains(rec.Body.String(), aos533Saida) {
		t.Fatalf("controlo: o /reconstruct devia devolver o texto e a saida: %d %s", rec.Code, rec.Body.String())
	}
}

// TestAOS533_NoDePlano_ComInputs: um run com payloads do plano (AOS-414) reproduz-se com os inputs
// do registo de retoma; sem eles divergia no turno 1 (controlo in-process).
func TestAOS533_NoDePlano_ComInputs(t *testing.T) {
	n := aos533Compor(t, aos533Opcoes{}, aos533Guiao)
	const runID = "plano-533~ler"
	conteudo := []byte(aos533Entrada)
	d := sha256.Sum256(conteudo)
	inputs := []agentruntime.PlanInput{{From: "produtor", Output: "notas", Digest: "sha256:" + hex.EncodeToString(d[:]), Content: conteudo}}
	n.correr(t, agentruntime.Goal{RunID: runID, Inputs: inputs})

	code, resp, corpo := n.replay(t, runID, govHeaders())
	if code != http.StatusOK {
		t.Fatalf("200 esperado, veio %d (%s)", code, corpo)
	}
	aos533Fiel(t, resp, 2)
	aos533SemConteudo(t, corpo)

	// CONTROLO: os inputs entram de facto — sem eles o turno 1 diverge.
	semInputs := aos533ReplayInProcess(t, n, runID, func(s *replay.TrajectorySpec) { s.Inputs = nil })
	if semInputs.Divergence == nil || semInputs.Divergence.Turn != 1 {
		t.Fatalf("controlo: sem os inputs o replay tinha de divergir no turno 1: %+v", semInputs.Divergence)
	}
}

// TestAOS533_NovaTentativaComAviso: o aviso da nova tentativa (AOS-506) vem do registo de retoma.
func TestAOS533_NovaTentativaComAviso(t *testing.T) {
	n := aos533Compor(t, aos533Opcoes{}, aos533Guiao)
	const runID = "plano-533~ler~2"
	n.correr(t, agentruntime.Goal{RunID: runID, RetryNotice: agentruntime.RetryNoticeNoFunctionCall})

	code, resp, corpo := n.replay(t, runID, govHeaders())
	if code != http.StatusOK {
		t.Fatalf("200 esperado, veio %d (%s)", code, corpo)
	}
	aos533Fiel(t, resp, 2)
	semAviso := aos533ReplayInProcess(t, n, runID, func(s *replay.TrajectorySpec) { s.RetryNotice = agentruntime.RetryNoticeNone })
	if semAviso.Divergence == nil || semAviso.Divergence.Turn != 1 {
		t.Fatalf("controlo: sem o aviso o replay tinha de divergir no turno 1: %+v", semAviso.Divergence)
	}
}

// TestAOS533_OObjectivoDaSpecDecide: trocar o objectivo da spec diverge no turno 1, com o motivo
// `prompt_hash` — a spec do registo é o que torna o veredicto verdadeiro, e não um enfeite.
func TestAOS533_OObjectivoDaSpecDecide(t *testing.T) {
	n := aos533Compor(t, aos533Opcoes{}, aos533Guiao)
	const runID = "run-533-objectivo"
	n.correr(t, agentruntime.Goal{RunID: runID})
	res := aos533ReplayInProcess(t, n, runID, func(s *replay.TrajectorySpec) { s.Objective += " (outro)" })
	if res.Divergence == nil || res.Divergence.Turn != 1 || res.Divergence.Reason != "prompt_hash" {
		t.Fatalf("outro objectivo tinha de divergir no turno 1 por prompt_hash: %+v", res.Divergence)
	}
}

// aos533ReplayInProcess corre o motor com a MESMA spec que a rota monta (registo de retoma + tools
// do primeiro turno), depois de `muda` — para os controlos de que cada campo conta.
func aos533ReplayInProcess(t *testing.T, n *aos533No, runID string, muda func(*replay.TrajectorySpec)) replay.ReplayResult {
	t.Helper()
	ctx := context.Background()
	rec, ok, err := n.node.ResumeRecords.Get(ctx, runID)
	if err != nil || !ok {
		t.Fatalf("registo de retoma: ok=%v err=%v", ok, err)
	}
	evs, err := n.node.EventStore.Read(ctx, runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	spec := replay.TrajectorySpec{System: rec.System, Tools: toolsDoPrimeiroTurno(evs), Objective: rec.Objective,
		MemoryContext: rec.MemoryContext, Inputs: rec.Inputs, RetryNotice: rec.RetryNotice}
	muda(&spec)
	eng, err := replay.NewEngine(n.node.EventStore, replay.WithContentOpener(n.node.contentOpener,
		replay.Accessor{Principal: govReader, Scopes: []string{replay.DefaultSovereignContentScope}}))
	if err != nil {
		t.Fatal(err)
	}
	res, err := eng.Replay(ctx, runID, replay.Options{Spec: spec})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	return res
}

// aos533ForjarCaptura escreve CRUA no stream uma captura inline do turno 1 com outro resultado de
// tool: o tail muda e o turno 2 deixa de bater.
func aos533ForjarCaptura(t *testing.T, n *aos533No, runID string) {
	t.Helper()
	ctx := context.Background()
	forjada, err := json.Marshal(map[string]any{
		"schema_version": "1.0", "turn": 1, "observed_at_unix_nano": 1,
		"response": map[string]any{"final": false, "input_tokens": 1, "output_tokens": 1, "cost_micro_usd": 0,
			"tool_calls": []map[string]any{{"tool_id": "passo_um", "capability": acnCap, "resource_type": "doc",
				"resource_value": "doc-a", "resource_region": "eu", "input": []byte(`{"doc":"doc-a"}`)}}},
		"tool_results": []map[string]any{{"taint": "untrusted", "output": []byte(`{"content":"ADULTERADO"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.node.EventStore.Append(ctx, runID, eventstore.EventInput{
		Type: replay.EventTypeCaptured, Payload: forjada, RunID: runID, StepID: "step-forjado-533",
		Producer: eventstore.Producer{NHIID: acnAgent},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestAOS533_CapturaAdulterada: uma captura do turno 1 escrita CRUA no stream (inline, com outro
// resultado de tool) muda o tail; o turno 2 diverge por `prompt_hash`, e o run não é retomado.
func TestAOS533_CapturaAdulterada(t *testing.T) {
	n := aos533Compor(t, aos533Opcoes{}, aos533Guiao)
	const runID = "run-533-adulterado"
	n.correr(t, agentruntime.Goal{RunID: runID})
	if code, resp, corpo := n.replay(t, runID, govHeaders()); code != http.StatusOK || resp.Diverged {
		t.Fatalf("PRECONDICAO: antes da adulteracao o replay e fiel: %d %s", code, corpo)
	}

	aos533ForjarCaptura(t, n, runID)
	code, resp, corpo := n.replay(t, runID, govHeaders())
	if code != http.StatusOK {
		t.Fatalf("a divergencia nao e erro: 200 esperado, veio %d (%s)", code, corpo)
	}
	d := resp.Divergence
	if !resp.Diverged || d == nil || d.Turn != 2 || d.Reason != "prompt_hash" || d.ExpectedHash == d.ActualHash || d.ExpectedHash == "" {
		t.Fatalf("a captura adulterada tinha de divergir no turno 2 por prompt_hash: %+v", d)
	}
	if resp.Retomado || resp.Nota != "" || resp.Fidelity.Value >= 1 || resp.Fidelity.Matched != 1 || resp.Fidelity.Verified != 2 {
		t.Fatalf("divergencia num run de uma vida, fidelidade 1/2: %+v", resp)
	}
	aos533SemConteudo(t, corpo)
	if strings.Contains(corpo, "ADULTERADO") {
		t.Fatalf("a resposta leva o conteudo da captura forjada: %s", corpo)
	}
}

// TestAOS533_RunRetomado: o run escala a tool call (L4), a cerimónia four-eyes aprova, a retoma
// corre-a. O stream tem a transição waiting_on_human → running: `retomado: true`, a divergência sai
// com a nota (ADR-037 §5) e o replay não amostra o SLI.
func TestAOS533_RunRetomado(t *testing.T) {
	ctx := context.Background()
	n := aos533Compor(t, aos533Opcoes{l4: true, spans: true}, aos533Guiao)
	const runID = "run-533-retomado"
	n.correr(t, agentruntime.Goal{RunID: runID})
	if _, susp := n.svc.Suspended(ctx, runID); !susp {
		t.Fatal("PRECONDICAO: a tool call tinha de escalar e o run ficar suspenso")
	}
	aos533Aprova(t, n, runID, "req-533-retoma")
	if err := n.svc.Resume(ctx, runID, n.token(t)); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	n.esperar(t, runID)
	if fim, done := n.svc.Outcome(runID); !done || !fim.Result.Terminated {
		t.Fatalf("o run retomado tinha de terminar: %+v", fim)
	}

	code, resp, corpo := n.replay(t, runID, govHeaders())
	if code != http.StatusOK {
		t.Fatalf("200 esperado, veio %d (%s)", code, corpo)
	}
	if !resp.Retomado || resp.SLIAmostrado {
		t.Fatalf("o run retomado tinha de sair retomado e sem amostra do SLI: %+v", resp)
	}
	// MEDIDO (AOS-533): a inferência do ADR-037 §5 para a retoma por aprovação confirma-se. A captura
	// do turno 1 é a da primeira vida (a escalada, sem o resultado da tool), e o turno 2 foi gravado
	// na segunda vida, com o resultado no tail: diverge no turno 2 por prompt_hash.
	d := resp.Divergence
	if !resp.Diverged || d == nil || d.Turn != 2 || d.Reason != "prompt_hash" || resp.Nota != notaRetomado {
		t.Fatalf("o run retomado por aprovacao diverge no turno 2 por prompt_hash, com a nota: %+v", resp)
	}
	if resp.Fidelity.Matched != 1 || resp.Fidelity.Verified != 2 {
		t.Fatalf("fidelidade 1/2 esperada: %+v", resp.Fidelity)
	}
	aos533SemConteudo(t, corpo)

	ev := n.svc.EvaluateSLOsNow(ctx)
	if v := aos274SLI(t, ev, otelgenai.SLIReplayFidelity); v.Samples != 0 {
		t.Fatalf("um run retomado nao amostra o SLI replay_fidelity: %d amostras (valor %g)", v.Samples, v.Value)
	}
}

// aos533Aprova corre a cerimónia four-eyes real sobre o único pendente do run.
func aos533Aprova(t *testing.T, n *aos533No, runID, requestID string) {
	t.Helper()
	ctx := context.Background()
	pendentes, err := n.node.PendingApprovals.ListForRun(ctx, runID)
	if err != nil || len(pendentes) != 1 {
		t.Fatalf("um pendente esperado: %d err=%v", len(pendentes), err)
	}
	p := pendentes[0]
	req := integration.FourEyesRequest{RequestID: requestID, Preview: p.Preview, RiskClass: risk.ClassDanger, DualControlRequired: true}
	legA := integration.SignFourEyesLeg(n.keys[0], req, "human:alice", "sess-A", "cred-A", acnChallenge(requestID, 'a'), nil)
	legB := integration.SignFourEyesLeg(n.keys[1], req, "human:bob", "sess-B", "cred-B", acnChallenge(requestID, 'b'), nil)
	if _, err := n.node.ApprovalBroker.Approve(ctx, requestID, req, legA, legB); err != nil {
		t.Fatalf("four-eyes: %v", err)
	}
	if err := n.node.PendingApprovals.Expire(ctx, p.RunID, p.StepID); err != nil {
		t.Fatal(err)
	}
}

// TestAOS533_SLI_ReplayFidelity_APedido: com a torneira composta, UMA chamada à rota é UMA amostra
// do SLI, com o valor da fidelidade; sem chamada não há amostra.
func TestAOS533_SLI_ReplayFidelity_APedido(t *testing.T) {
	ctx := context.Background()
	n := aos533Compor(t, aos533Opcoes{spans: true}, aos533Guiao)
	const runID = "run-533-sli"
	n.correr(t, agentruntime.Goal{RunID: runID})
	if v := aos274SLI(t, n.svc.EvaluateSLOsNow(ctx), otelgenai.SLIReplayFidelity); v.Samples != 0 {
		t.Fatalf("PRECONDICAO: sem chamada a rota nao ha amostra; vieram %d", v.Samples)
	}
	code, resp, corpo := n.replay(t, runID, govHeaders())
	if code != http.StatusOK || !resp.SLIAmostrado {
		t.Fatalf("200 com amostra esperado: %d %s", code, corpo)
	}
	v := aos274SLI(t, n.svc.EvaluateSLOsNow(ctx), otelgenai.SLIReplayFidelity)
	if v.Samples != 1 || v.Value != 1 || v.Breached() {
		t.Fatalf("uma chamada = uma amostra de fidelidade 1: samples=%d valor=%g", v.Samples, v.Value)
	}
	if !produtorNoNo(otelgenai.SLIReplayFidelity, n.node.sloTap != nil) {
		t.Fatal("com a torneira composta o replay_fidelity tem produtor (a pedido)")
	}
	if b := sloEvaluatorBanner(n.node, time.Second, time.Minute); !strings.Contains(b, "A PEDIDO") || !strings.Contains(b, "/replay") {
		t.Fatalf("o banner tem de dizer que a fidelidade de replay e a pedido: %s", b)
	}
}

// TestAOS533_OCRRead: o run da `ocr_read` (AOS-530) pelo nó composto com o gate soberano —
// fidelidade total, e nada do texto lido na resposta.
func TestAOS533_OCRRead(t *testing.T) {
	n := aos530Compor(t, aos530Opcoes{soberano: true})
	const runID = "run-533-ocr"
	// Com o gate soberano composto o POST /runs exige a credencial de leitura do submissor (D7).
	n.upstream.mu.Lock()
	n.upstream.guiao = [][2]string{{ocrToolName, `{"doc_id":"` + aos530Doc + `"}`}}
	n.upstream.mu.Unlock()
	sub := postReq(n.h, "/runs", map[string]any{
		"run_id": runID, "objective": "Le a fatura e diz o total", "principal_nhi": durAgent, "credential": n.tok, "max_turns": 4,
	}, govHeaders())
	if sub.Code != http.StatusCreated {
		t.Fatalf("POST /runs: %d (%s)", sub.Code, sub.Body.String())
	}
	wctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if oc, ok, err := n.svc.Wait(wctx, runID); err != nil || !ok || oc.Err != nil {
		t.Fatalf("o run tinha de concluir: ok=%v err=%v", ok, err)
	}
	evs, err := n.node.EventStore.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(aos486LerTurnos(t, evs)) != 2 {
		t.Fatal("PRECONDICAO: 2 turnos gravados")
	}
	pedidos := len(n.upstream.pedidos())
	rec := getReq(n.h, "/runs/"+runID+"/replay", govHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("200 esperado, veio %d (%s)", rec.Code, rec.Body.String())
	}
	var resp replayResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	aos533Fiel(t, resp, 2)
	if got := len(n.upstream.pedidos()); got != pedidos {
		t.Fatalf("o replay chamou o provider: %d pedidos novos", got-pedidos)
	}
	// CONTROLO: o /reconstruct tem o resultado da ocr_read; o /replay não pode ter nenhum byte dele.
	recon := getReq(n.h, "/runs/"+runID+"/reconstruct", govHeaders())
	var r reconstructResponse
	if recon.Code != http.StatusOK || json.Unmarshal(recon.Body.Bytes(), &r) != nil || len(r.Turns) == 0 || len(r.Turns[0].ToolOutputs) == 0 {
		t.Fatalf("controlo: o /reconstruct devia ter a saida da ocr_read: %d %s", recon.Code, recon.Body.String())
	}
	if saida := r.Turns[0].ToolOutputs[0]; len(saida) < 16 || bytes.Contains(rec.Body.Bytes(), []byte(saida[:16])) {
		t.Fatalf("a resposta do /replay leva o resultado da ocr_read: %s", rec.Body.String())
	}
}

// TestAOS533_Recusas: sem registo de retoma (422 com causa), leitor de outro board (404 uniforme,
// igual ao inexistente), depois do /dsar/erase (410) e no nó sem registo de retoma composto (501,
// sem selo).
func TestAOS533_Recusas(t *testing.T) {
	ctx := context.Background()
	n := aos533Compor(t, aos533Opcoes{}, aos533Guiao)

	t.Run("sem_registo_de_retoma", func(t *testing.T) {
		const runID = "run-533-sintetico"
		captureSynthetic(t, n.node, acnAgent, runID, aos533Texto, aos533Saida)
		rec := getReq(n.h, "/runs/"+runID+"/replay", govHeaders())
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"`+causaReplaySemRegisto+`"`) {
			t.Fatalf("422 sem_registo_de_retoma esperado: %d %s", rec.Code, rec.Body.String())
		}
		aos533SemConteudo(t, rec.Body.String())
	})

	const runID = "run-533-recusas"
	n.correr(t, agentruntime.Goal{RunID: runID})

	t.Run("leitor_nao_autorizado", func(t *testing.T) {
		negado := getReq(n.h, "/runs/"+runID+"/replay", map[string]string{HeaderReaderPrincipal: govReader, HeaderReaderBoard: govBadBoard})
		inexistente := getReq(n.h, "/runs/run-533-inexistente/replay", govHeaders())
		if negado.Code != http.StatusNotFound || inexistente.Code != http.StatusNotFound || negado.Body.String() != inexistente.Body.String() {
			t.Fatalf("404 uniforme: negado %d %q, inexistente %d %q", negado.Code, negado.Body.String(), inexistente.Code, inexistente.Body.String())
		}
		if head, _ := n.node.WORM.Head(ctx, readAuditPartition(runID)); head != 0 {
			t.Fatalf("uma leitura negada nao sela: head=%d", head)
		}
	})

	t.Run("depois_do_dsar_erase", func(t *testing.T) {
		if code, _, corpo := n.replay(t, runID, govHeaders()); code != http.StatusOK {
			t.Fatalf("PRECONDICAO: antes do apagamento o replay da 200: %d %s", code, corpo)
		}
		res, err := n.node.DSAR.Receive(ctx, dsar.Request{RequestID: "req-533", SubjectID: acnAgent})
		if err != nil || res.Blocked {
			t.Fatalf("erase: blocked=%v err=%v", res.Blocked, err)
		}
		rec := getReq(n.h, "/runs/"+runID+"/replay", govHeaders())
		if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), `"code":"`+causaReplayApagado+`"`) {
			t.Fatalf("410 conteudo_apagado esperado: %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("registo_de_retoma_nao_composto", func(t *testing.T) {
		node := newGovDurableNode(t) // sem four-eyes ⇒ sem registo de retoma
		_, h := newAPI(t, node)
		const sid = "run-533-sem-four-eyes"
		captureSynthetic(t, node, acnAgent, sid, aos533Texto, aos533Saida)
		rec := getReq(h, "/runs/"+sid+"/replay", govHeaders())
		if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), causaReplayRegistoNaoComposto) {
			t.Fatalf("501 registo_de_retoma_nao_composto esperado: %d %s", rec.Code, rec.Body.String())
		}
		if head, _ := node.WORM.Head(ctx, readAuditPartition(sid)); head != 0 {
			t.Fatalf("uma rota desligada nao sela: head=%d", head)
		}
	})
}

// TestAOS533_MapeamentoDeErros: cada classe de erro do motor tem status e causa próprios, sem 500.
func TestAOS533_MapeamentoDeErros(t *testing.T) {
	casos := []struct {
		err    error
		status int
		causa  string
	}{
		{replay.ErrIncompleteCapture, http.StatusUnprocessableEntity, causaReplayCapturaIncompleta},
		{agentruntime.ErrUnknownAssemblyVersion, http.StatusUnprocessableEntity, causaReplayLayoutDesconhecido},
		{agentruntime.ErrUnknownRetryNotice, http.StatusUnprocessableEntity, causaReplayAvisoDesconhecido},
		{replay.ErrPayloadStoreRequired, http.StatusNotImplemented, causaReplayPayloadStore},
		{replay.ErrCorruptCapture, http.StatusUnprocessableEntity, causaReplayCapturaCorrompida},
		{replay.ErrPayloadAccessDenied, http.StatusForbidden, causaReplayAcessoNegado},
		{audit.ErrDecrypt, http.StatusGone, causaReplayApagado},
	}
	for _, c := range casos {
		embrulhado := errorsJoin533(c.err)
		if s, k := replayErrorStatus(embrulhado); s != c.status || k != c.causa {
			t.Errorf("%v: %d/%s, queria %d/%s", c.err, s, k, c.status, c.causa)
		}
	}
	if s, k := registoErrorStatus(integration.ErrResumeRecordDivergente); s != http.StatusUnprocessableEntity || k != causaReplayRegistoRecusado {
		t.Errorf("registo recusado: %d/%s", s, k)
	}
}

// errorsJoin533 embrulha como o motor embrulha (fmt.Errorf %w com contexto do turno).
func errorsJoin533(err error) error {
	return &aos533Embrulho{err: err}
}

type aos533Embrulho struct{ err error }

func (e *aos533Embrulho) Error() string { return "replay: turno 1 (step-000001): " + e.err.Error() }
func (e *aos533Embrulho) Unwrap() error { return e.err }

// aos533Transicao é uma transição como a [state.Machine] a escreve (produtor, step `state-N`).
func aos533Transicao(n int, from, to string, tok uint64) eventstore.Event {
	p, _ := json.Marshal(map[string]any{"from": from, "to": to, "token_value": tok})
	return eventstore.Event{Type: state.EventTypeTransition, Payload: p, StepID: fmt.Sprintf("state-%d", n),
		Producer: eventstore.Producer{NHIID: state.DefaultProducerNHI}}
}

func aos533Turno(turn int) eventstore.Event {
	p, _ := json.Marshal(map[string]any{"turn": turn})
	return eventstore.Event{Type: agentruntime.EventTypeTurnRecorded, Payload: p}
}

func aos533Claims(n int) []eventstore.Event {
	out := make([]eventstore.Event, 0, n)
	for k := 1; k <= n; k++ {
		p, _ := json.Marshal(map[string]any{"token": k})
		out = append(out, eventstore.Event{Type: durable.EventTypeLeaseClaimed, Payload: p,
			Producer: eventstore.Producer{NHIID: durable.DefaultLeaseProducerNHI}})
	}
	return out
}

// TestAOS533_AnalisarVidas: as formas MEDIDAS no nó composto (as transições de retoma não levam
// token; só a primeira e a terminal levam) — e cada forjamento de uma linha deixa de desculpar.
func TestAOS533_AnalisarVidas(t *testing.T) {
	// Duas retomas por aprovação (medido: claims 1,2,3; token só em state-1 e state-6).
	duas := func() []eventstore.Event {
		return []eventstore.Event{
			aos533Transicao(1, "ready", "running", 1), aos533Turno(1),
			aos533Transicao(2, "running", "waiting_on_human", 0),
			aos533Transicao(3, "waiting_on_human", "running", 0), aos533Turno(2),
			aos533Transicao(4, "running", "waiting_on_human", 0),
			aos533Transicao(5, "waiting_on_human", "running", 0), aos533Turno(3),
			aos533Transicao(6, "running", "complete", 3),
		}
	}
	v := analisarVidas(duas(), aos533Claims(3))
	if v.corroboradas != 2 || len(v.desculpados) != 2 || !v.desculpados[2] || !v.desculpados[3] {
		t.Fatalf("duas retomas: as duas vidas corroboradas, turnos 2 e 3 desculpados: %+v", v)
	}
	// A MEIO (em waiting_on_human depois da 2.ª escalada, antes da 2.ª retoma): uma vida corroborada.
	meio := duas()[:6]
	if v := analisarVidas(meio, aos533Claims(2)); v.corroboradas != 1 || !v.desculpados[2] || len(v.desculpados) != 1 {
		t.Fatalf("a meio: a vida 2 corroborada sem terminal, turno 2 desculpado: %+v", v)
	}
	// Retoma + kill de humano (a forma do abort de exaustão e do human-TTL: waiting_on_human →
	// killed, sem token).
	kill := append(duas()[:6], aos533Transicao(5, "waiting_on_human", "killed", 0))
	if v := analisarVidas(kill, aos533Claims(2)); v.corroboradas != 1 || !v.desculpados[2] {
		t.Fatalf("retomado + kill: a vida 2 corroborada: %+v", v)
	}
	// Retoma por pausa (paused → running, sem token) e wall-clock (running → timed_out).
	pausa := []eventstore.Event{aos533Transicao(1, "ready", "running", 1), aos533Turno(1),
		aos533Transicao(2, "running", "paused", 0), aos533Transicao(3, "paused", "running", 0), aos533Turno(2),
		aos533Transicao(4, "running", "timed_out", 0)}
	if v := analisarVidas(pausa, aos533Claims(2)); v.corroboradas != 1 || !v.desculpados[2] {
		t.Fatalf("retoma por pausa: a vida 2 corroborada: %+v", v)
	}
	// Crash: a vida 2 encontra o run em running e não transita; o par é a terminal com o token 2,
	// depois dos turnos — nenhum desculpado (declarado).
	crash := []eventstore.Event{aos533Transicao(1, "ready", "running", 1), aos533Turno(1), aos533Turno(2),
		aos533Transicao(2, "running", "complete", 2)}
	if v := analisarVidas(crash, aos533Claims(2)); v.corroboradas != 1 || len(v.desculpados) != 0 {
		t.Fatalf("crash: corroborado pela terminal com o token 2, sem turno desculpado: %+v", v)
	}
	// Crash duplo: o claim 2 não tem par (sem retoma, sem token 2); o 3 tem a terminal.
	if v := analisarVidas([]eventstore.Event{aos533Transicao(1, "ready", "running", 1), aos533Turno(1),
		aos533Transicao(2, "running", "complete", 3)}, aos533Claims(3)); v.corroboradas != 1 {
		t.Fatalf("crash duplo: so a vida 3 corrobora: %+v", v)
	}

	umaVida := []eventstore.Event{aos533Transicao(1, "ready", "running", 1), aos533Turno(1), aos533Turno(2),
		aos533Transicao(2, "running", "complete", 1)}
	casos := map[string]struct{ run, lease []eventstore.Event }{
		// A PoC da 1.ª revisão: uma linha depois do terminal, com o produtor e o step certos.
		"transicao_depois_do_terminal": {append(append([]eventstore.Event{}, umaVida...),
			aos533Transicao(3, "waiting_on_human", "running", 0), aos533Turno(3)), aos533Claims(2)},
		// Um claim forjado sem transição de retoma nem transição com o seu token.
		"claim_sem_par":             {umaVida, aos533Claims(2)},
		"sem_corroboracao_no_lease": {duas(), aos533Claims(1)},
		"claim_com_outro_produtor": {duas(), append(aos533Claims(1),
			eventstore.Event{Type: durable.EventTypeLeaseClaimed, Payload: []byte(`{"token":2}`)})},
		"claims_fora_de_ordem": {duas(), append(aos533Claims(2)[1:], aos533Claims(1)...)},
		"transicao_com_outro_produtor": func() struct{ run, lease []eventstore.Event } {
			r := duas()
			r[3].Producer.NHIID = "agt-forjado"
			return struct{ run, lease []eventstore.Event }{r, aos533Claims(3)}
		}(),
		"cadeia_com_buraco": func() struct{ run, lease []eventstore.Event } {
			r := duas()
			r[3] = aos533Transicao(9, "waiting_on_human", "running", 0)
			return struct{ run, lease []eventstore.Event }{r, aos533Claims(3)}
		}(),
	}
	for nome, c := range casos {
		if v := analisarVidas(c.run, c.lease); v.retomado() {
			t.Errorf("%s: nao pode ser tratado como retomado (%+v)", nome, v)
		}
	}
}

// TestAOS533_PoC_RetomaForjada: a PoC da revisão no nó composto. Captura do turno 1 forjada e uma
// `waiting_on_human → running` acrescentada depois do `complete`, com o produtor e o step que a
// máquina usaria: o replay tem de dar divergência NORMAL — sem `retomado`, sem nota, com amostra
// no SLI e o alerta a disparar.
func TestAOS533_PoC_RetomaForjada(t *testing.T) {
	ctx := context.Background()
	n := aos533Compor(t, aos533Opcoes{spans: true}, aos533Guiao)
	const runID = "run-533-poc"
	n.correr(t, agentruntime.Goal{RunID: runID})
	aos533ForjarCaptura(t, n, runID)
	evs, err := n.node.EventStore.Read(ctx, runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	transicoes := 0
	for _, e := range evs {
		if e.Type == state.EventTypeTransition {
			transicoes++
		}
	}
	p, _ := json.Marshal(map[string]any{"from": "waiting_on_human", "to": "running"})
	if _, err := n.node.EventStore.Append(ctx, runID, eventstore.EventInput{
		Type: state.EventTypeTransition, Payload: p, RunID: runID, StepID: fmt.Sprintf("state-%d", transicoes+1),
		Producer: eventstore.Producer{NHIID: state.DefaultProducerNHI},
	}); err != nil {
		t.Fatal(err)
	}

	code, resp, corpo := n.replay(t, runID, govHeaders())
	if code != http.StatusOK {
		t.Fatalf("200 esperado, veio %d (%s)", code, corpo)
	}
	if !resp.Diverged || resp.Retomado || resp.Nota != "" || !resp.SLIAmostrado {
		t.Fatalf("a retoma forjada nao desculpa nada: divergencia normal, sem nota, com amostra: %+v", resp)
	}
	var ev *SLOEvaluation
	for i := 0; i < 4; i++ {
		ev = n.svc.EvaluateSLOsNow(ctx)
	}
	if v := aos274SLI(t, ev, otelgenai.SLIReplayFidelity); v.Samples != 1 || !v.Breached() {
		t.Fatalf("a divergencia tem de amostrar o SLI abaixo do alvo: samples=%d valor=%g", v.Samples, v.Value)
	}
	if !aos274Alert(t, ev, otelgenai.AlertReplayFidelityLow).Alert.Fired {
		t.Fatal("o alerta replay_fidelity_low tinha de disparar sobre a captura adulterada")
	}
}

// TestAOS533_RunRetomado_DivergenciaNoutroTurno: num run retomado de verdade, uma divergência num
// turno que NÃO é o primeiro da vida nova (aqui, um `turn.recorded` do turno 1 forjado) é
// divergência normal: `retomado: true`, mas sem nota e com amostra no SLI.
func TestAOS533_RunRetomado_DivergenciaNoutroTurno(t *testing.T) {
	ctx := context.Background()
	n := aos533Compor(t, aos533Opcoes{l4: true, spans: true}, aos533Guiao)
	const runID = "run-533-retomado-outro"
	aos533Retomar(t, n, runID)
	evs, err := n.node.EventStore.Read(ctx, runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	var forjado []byte
	for _, e := range evs {
		var p struct {
			Turn int `json:"turn"`
		}
		if e.Type == agentruntime.EventTypeTurnRecorded && json.Unmarshal(e.Payload, &p) == nil && p.Turn == 1 {
			forjado = []byte(strings.Replace(string(e.Payload), `"prompt_hash":"sha256:`, `"prompt_hash":"sha256:00`, 1))
		}
	}
	if forjado == nil {
		t.Fatal("PRECONDICAO: turn.recorded do turno 1")
	}
	if _, err := n.node.EventStore.Append(ctx, runID, eventstore.EventInput{
		Type: agentruntime.EventTypeTurnRecorded, Payload: forjado, RunID: runID, StepID: "step-forjado-533",
		Producer: eventstore.Producer{NHIID: acnAgent},
	}); err != nil {
		t.Fatal(err)
	}
	code, resp, corpo := n.replay(t, runID, govHeaders())
	if code != http.StatusOK {
		t.Fatalf("200 esperado, veio %d (%s)", code, corpo)
	}
	if !resp.Retomado || resp.Divergence == nil || resp.Divergence.Turn != 1 || resp.Nota != "" || !resp.SLIAmostrado {
		t.Fatalf("divergencia no turno 1 de um run retomado: retomado, sem nota, com amostra: %+v", resp)
	}
	if v := aos274SLI(t, n.svc.EvaluateSLOsNow(ctx), otelgenai.SLIReplayFidelity); v.Samples != 1 || !v.Breached() {
		t.Fatalf("a divergencia nao desculpada amostra o SLI: samples=%d valor=%g", v.Samples, v.Value)
	}
}

// aos533Retomar corre o ciclo escalada → four-eyes → retoma até ao fim.
func aos533Retomar(t *testing.T, n *aos533No, runID string) {
	t.Helper()
	ctx := context.Background()
	n.correr(t, agentruntime.Goal{RunID: runID})
	if _, susp := n.svc.Suspended(ctx, runID); !susp {
		t.Fatal("PRECONDICAO: a tool call tinha de escalar e o run ficar suspenso")
	}
	aos533Aprova(t, n, runID, "req-"+runID)
	if err := n.svc.Resume(ctx, runID, n.token(t)); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	n.esperar(t, runID)
	if fim, done := n.svc.Outcome(runID); !done || !fim.Result.Terminated {
		t.Fatalf("o run retomado tinha de terminar: %+v", fim)
	}
}

// TestAOS533_SystemEMemoria: um run com system e com memory_context reproduz-se com os do registo
// de retoma; sem cada um deles, diverge no turno 1. O `system` chega ao Goal pelo campo `system`
// do POST /runs (api.go), 1:1; o memory_context não tem caminho de produção no nó (nenhum handler o
// escreve no Goal), e entra aqui pelo Goal, como o loop o recebe.
func TestAOS533_SystemEMemoria(t *testing.T) {
	n := aos533Compor(t, aos533Opcoes{}, aos533Guiao)
	const runID = "run-533-system-memoria"
	n.correr(t, agentruntime.Goal{RunID: runID, System: aos533System, MemoryContext: []byte(aos533Memoria)})
	code, resp, corpo := n.replay(t, runID, govHeaders())
	if code != http.StatusOK {
		t.Fatalf("200 esperado, veio %d (%s)", code, corpo)
	}
	aos533Fiel(t, resp, 2)
	aos533SemConteudo(t, corpo)
	for nome, muda := range map[string]func(*replay.TrajectorySpec){
		"sem_system":  func(s *replay.TrajectorySpec) { s.System = "" },
		"sem_memoria": func(s *replay.TrajectorySpec) { s.MemoryContext = nil },
	} {
		if r := aos533ReplayInProcess(t, n, runID, muda); r.Divergence == nil || r.Divergence.Turn != 1 {
			t.Errorf("%s: o replay tinha de divergir no turno 1: %+v", nome, r.Divergence)
		}
	}
}

// aos533SemAlarme exige que a chamada não tenha amostrado o SLI nem posto o alerta a disparar.
func aos533SemAlarme(t *testing.T, n *aos533No) {
	t.Helper()
	ctx := context.Background()
	var ev *SLOEvaluation
	for i := 0; i < 4; i++ {
		ev = n.svc.EvaluateSLOsNow(ctx)
	}
	if v := aos274SLI(t, ev, otelgenai.SLIReplayFidelity); v.Samples != 0 {
		t.Fatalf("a divergencia desculpada nao amostra o SLI: %d amostras (valor %g)", v.Samples, v.Value)
	}
	if aos274Alert(t, ev, otelgenai.AlertReplayFidelityLow).Alert.Fired {
		t.Fatal("o alerta replay_fidelity_low disparou sobre um run retomado legitimo")
	}
}

// aos533Desculpada exige a divergência desculpada do turno dado: retomado, nota, sem amostra.
func aos533Desculpada(t *testing.T, n *aos533No, runID string, turno int) {
	t.Helper()
	code, resp, corpo := n.replay(t, runID, govHeaders())
	if code != http.StatusOK {
		t.Fatalf("200 esperado, veio %d (%s)", code, corpo)
	}
	if !resp.Retomado || resp.Divergence == nil || resp.Divergence.Turn != turno || resp.Divergence.Reason != "prompt_hash" ||
		resp.Nota != notaRetomado || resp.SLIAmostrado {
		t.Fatalf("divergencia desculpada no turno %d esperada (retomado, nota, sem amostra): %+v", turno, resp)
	}
}

// aos533Aprovar aprova o pendente e retoma, até o run sair de em-curso.
func aos533Aprovar(t *testing.T, n *aos533No, runID, requestID string) {
	t.Helper()
	aos533Aprova(t, n, runID, requestID)
	if err := n.svc.Resume(context.Background(), runID, n.token(t)); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	n.esperar(t, runID)
}

// TestAOS533_DuasRetomas: o caso da 2.ª revisão — um run L4 com DUAS tool calls escaladas (três
// vidas, claims 1, 2 e 3; as transições de retoma sem token). Consultado a meio (de novo em
// waiting_on_human) e depois do complete, a divergência do 1.º turno da vida 2 é desculpada, sem
// amostra e sem alerta; e o 1.º turno de cada vida retomada está nos desculpados.
func TestAOS533_DuasRetomas(t *testing.T) {
	ctx := context.Background()
	n := aos533Compor(t, aos533Opcoes{l4: true, spans: true}, nil) // passo_um, passo_dois, conclui
	const runID = "run-533-duas-retomas"
	n.correr(t, agentruntime.Goal{RunID: runID})
	aos533Aprovar(t, n, runID, "req-533-duas-1")
	if _, susp := n.svc.Suspended(ctx, runID); !susp {
		t.Fatal("PRECONDICAO: a segunda tool call tinha de escalar")
	}
	aos533Desculpada(t, n, runID, 2) // a meio
	aos533Aprovar(t, n, runID, "req-533-duas-2")
	if fim, done := n.svc.Outcome(runID); !done || !fim.Result.Terminated {
		t.Fatalf("o run tinha de terminar: %+v", fim)
	}
	aos533Desculpada(t, n, runID, 2) // depois do complete
	evs, _ := n.node.EventStore.Read(ctx, runID, 1)
	lease, _ := n.node.EventStore.Read(ctx, durable.LeaseStreamID(runID), 1)
	if v := analisarVidas(evs, lease); v.corroboradas != 2 || !v.desculpados[2] || !v.desculpados[3] {
		t.Fatalf("duas vidas corroboradas, o 1.o turno de cada uma desculpado: %+v", v)
	}
	aos533SemAlarme(t, n)
}

// TestAOS533_RetomadoEKill: retomado, de novo à espera de humano, e morto nessa espera
// (`waiting_on_human → killed`, sem token — a transição do abort de exaustão e do human-TTL,
// escrita pela mesma máquina de estados do nó).
func TestAOS533_RetomadoEKill(t *testing.T) {
	ctx := context.Background()
	n := aos533Compor(t, aos533Opcoes{l4: true, spans: true}, nil)
	const runID = "run-533-kill"
	n.correr(t, agentruntime.Goal{RunID: runID})
	aos533Aprovar(t, n, runID, "req-533-kill")
	m, err := state.NewMachine(n.node.EventStore, runID)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := m.Rebuild(ctx); err != nil || st != state.WaitingOnHuman {
		t.Fatalf("PRECONDICAO: waiting_on_human, veio %s (%v)", st, err)
	}
	if err := m.Transition(ctx, state.Killed, state.TransitionEvent{Reason: "abort do operador (teste AOS-533)"}); err != nil {
		t.Fatal(err)
	}
	aos533Desculpada(t, n, runID, 2)
	aos533SemAlarme(t, n)
}

// TestAOS533_RetomaPorCrash: a re-hospedagem pela varredura de arranque (AOS-253), sobre o MESMO
// WAL, WORM e custódia de KEK. O crash é o do AOS-253 (o turno 1 aplicado e capturado, o run em
// `running` com o lease 1, sem terminal); a vida 2 reclama o lease 2, não transita no arranque e
// sela a terminal com o token 2. Medido: fidelidade total — sem divergência a desculpar —, com
// `retomado: true` e amostra no SLI de valor 1.
func TestAOS533_RetomaPorCrash(t *testing.T) {
	ctx := context.Background()
	dir, vault := t.TempDir(), audit.NewInMemoryKeyVault(nil)
	const runID = "run-533-crash"
	n1 := aos533Compor(t, aos533Opcoes{dir: dir, vault: vault}, aos533Guiao)
	goal := agentruntime.Goal{RunID: runID, Principal: referencemonitor.Principal{NHIID: acnAgent},
		Credential: n1.token(t), Objective: aos533Objectivo, MaxTurns: 5}
	// O turno 1 corre ANTES do lease, como na passagem do AOS-253 (o Runtime.Run directo nao
	// apresenta token; com um detentor ja registado a escrita seria recusada, AOS-299).
	prod := goal
	prod.MaxTurns = 1
	if _, _, err := n1.node.Runtime.Run(ctx, prod, nil); err != nil && !errors.Is(err, agentruntime.ErrMaxTurnsExceeded) {
		t.Fatalf("turno 1: %v", err)
	}
	leases, err := durable.NewLeaseManager(n1.node.EventStore, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leases.Claim(ctx, runID); err != nil {
		t.Fatal(err)
	}
	m, err := state.NewMachine(n1.node.EventStore, runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1), Reason: "crash_simulado"}); err != nil {
		t.Fatal(err)
	}
	if err := n1.node.ResumeRecords.Put(ctx, registoDeRetomaDeTeste(t, goal)); err != nil {
		t.Fatal(err)
	}
	_ = n1.svc.Shutdown(ctx)
	_ = n1.node.Close()

	futuro := durable.ClockFunc(func() time.Time { return time.Now().Add(time.Hour) })
	n2 := aos533Compor(t, aos533Opcoes{dir: dir, vault: vault, spans: true, svc: []NodeServiceOption{WithLeaseClock(futuro)}}, aos533Guiao)
	if _, retomados, err := n2.svc.ResumeInterruptedRuns(ctx); err != nil || retomados != 1 {
		t.Fatalf("a varredura tinha de retomar 1 run: %d %v", retomados, err)
	}
	n2.esperar(t, runID)
	code, resp, corpo := n2.replay(t, runID, govHeaders())
	if code != http.StatusOK {
		t.Fatalf("200 esperado, veio %d (%s)", code, corpo)
	}
	if !resp.Retomado || resp.Diverged || resp.Fidelity.Value != 1 || resp.Nota != "" || !resp.SLIAmostrado {
		t.Fatalf("retoma por crash: retomado, fiel, com amostra: %+v", resp)
	}
	v := aos274SLI(t, n2.svc.EvaluateSLOsNow(ctx), otelgenai.SLIReplayFidelity)
	if v.Samples != 1 || v.Value != 1 || v.Breached() {
		t.Fatalf("uma amostra de fidelidade 1: samples=%d valor=%g", v.Samples, v.Value)
	}
}

// aos533StoreSemLease falha a leitura do stream de lease (o resto passa).
type aos533StoreSemLease struct{ EventStorePort }

func (s aos533StoreSemLease) Read(ctx context.Context, stream string, from uint64) ([]eventstore.Event, error) {
	if strings.HasPrefix(stream, "lease:") {
		return nil, eventstore.ErrNoQuorum
	}
	return s.EventStorePort.Read(ctx, stream, from)
}

// TestAOS533_LeaseIndisponivel: um erro a ler o lease é 503 `lease_indisponivel`, sem amostra —
// e não «sem corroboração», que pusesse a divergência desculpável de um run retomado a alarmar.
func TestAOS533_LeaseIndisponivel(t *testing.T) {
	n := aos533Compor(t, aos533Opcoes{l4: true, spans: true}, aos533Guiao)
	const runID = "run-533-lease"
	aos533Retomar(t, n, runID)
	n.node.EventStore = aos533StoreSemLease{n.node.EventStore}
	rec := getReq(n.h, "/runs/"+runID+"/replay", govHeaders())
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"code":"`+causaReplayLeaseIndisp+`"`) {
		t.Fatalf("503 lease_indisponivel esperado: %d %s", rec.Code, rec.Body.String())
	}
	aos533SemAlarme(t, n)
}
