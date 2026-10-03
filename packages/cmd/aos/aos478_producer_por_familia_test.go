package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	autonomy "github.com/aos-ref/control-plane/governance/autonomy"
	pdp "github.com/aos-ref/control-plane/pdp"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/control"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	identity "github.com/aos-ref/platform/identity"
	domain "github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/sandbox"
	"github.com/aos-ref/testkit"
)

// ---------------------------------------------------------------------------
// AOS-478 — o `producer` do envelope, por família, contra EMISSORES REAIS
// ---------------------------------------------------------------------------
//
// `tecnica/13_Modelo_Dados_Eventos.md` §3.1 («O `producer` por família de evento») é o
// contrato: por tipo, a classe do `producer` (cadeia / só nhi_id / componente) ou
// `fora-do-envelope` para os rótulos de audit. Este teste:
//
//  1. lê ESSA tabela do documento (não uma cópia dela aqui — uma cópia concordaria sempre
//     consigo própria);
//  2. percorre o catálogo de tipos com o critério do gate `event-catalog`
//     (scripts/ci/event-catalog.py, RE_CONST_DECL) e exige que CADA tipo tenha linha no
//     contrato — um tipo novo sem classe avermelha aqui;
//  3. corre um NÓ REAL (Bootstrap + NodeService, a cadeia do nó sem hooks de teste): dois
//     ciclos de escalada → cerimónia four-eyes → retoma, o segundo a executar numa sandbox
//     ligada pelo `registerSandboxLaunchers` de produção; pausa e steer assinados; e um
//     segundo run cujo pendente expira pelo varrimento real;
//  4. lê TODOS os eventos do Event Store do nó e falha se um tipo que o contrato marca como
//     atribuível sair com `producer.nhi_id` vazio, ou um `cadeia` sem `delegation_chain`.
//
// O que NÃO cobre, por desenho: os emissores que o nó não compõe (escalonador, orquestrador,
// broker) — esses têm a classe no contrato e o default de componente no próprio construtor.

const (
	aos478RunA     = "run-aos478-a"
	aos478RunB     = "run-aos478-b"
	aos478Sandbox  = "passo_sbx"
	aos478Operador = "human:operador-478"
	aos478Titular  = "titular-dono-478"
)

// aos478Classes são as classes que o contrato pode declarar. Uma célula fora deste conjunto
// é um contrato mal escrito, e o teste recusa-o em vez de o interpretar.
var aos478Classes = map[string]bool{"cadeia": true, "nhi_id": true, "componente": true, "fora-do-envelope": true}

// aos478ContratoDoProducer lê a tabela do contrato de tecnica/13 §3.1: padrão → classe.
func aos478ContratoDoProducer(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "tecnica", "13_Modelo_Dados_Eventos.md"))
	if err != nil {
		t.Fatalf("ler o contrato: %v", err)
	}
	s := string(b)
	const titulo = "#### O `producer` por família de evento"
	i := strings.Index(s, titulo)
	if i < 0 {
		t.Fatalf("o contrato perdeu a secção %q", titulo)
	}
	sec := s[i:]
	if j := strings.Index(sec, "\n### "); j > 0 {
		sec = sec[:j]
	}
	code := regexp.MustCompile("`([^`]+)`")
	padrao := regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*(\.\*)?$`)
	out := map[string]string{}
	for _, ln := range strings.Split(sec, "\n") {
		if !strings.HasPrefix(ln, "| `") {
			continue
		}
		cel := strings.Split(ln, "|")
		if len(cel) < 4 {
			t.Fatalf("linha do contrato malformada: %q", ln)
		}
		cls := code.FindStringSubmatch(cel[2])
		if cls == nil || !aos478Classes[cls[1]] {
			t.Fatalf("classe desconhecida na linha %q", ln)
		}
		for _, m := range code.FindAllStringSubmatch(cel[1], -1) {
			if !padrao.MatchString(m[1]) {
				t.Fatalf("padrão de tipo inválido %q na linha %q", m[1], ln)
			}
			if prev, dup := out[m[1]]; dup {
				t.Fatalf("o padrão %q aparece duas vezes no contrato (%s e %s)", m[1], prev, cls[1])
			}
			out[m[1]] = cls[1]
		}
	}
	if len(out) < 20 {
		t.Fatalf("o contrato tem só %d padrões — o parser partiu-se ou a tabela encolheu", len(out))
	}
	return out
}

// aos478Classe resolve um tipo contra o contrato: correspondência exacta primeiro, senão o
// prefixo `x.*` MAIS LONGO (é o que deixa `memory.migration.*` sobrepor-se a `memory.*`).
func aos478Classe(contrato map[string]string, tipo string) (string, bool) {
	if c, ok := contrato[tipo]; ok {
		return c, true
	}
	melhor, cls := "", ""
	for p, c := range contrato {
		if !strings.HasSuffix(p, ".*") {
			continue
		}
		pref := strings.TrimSuffix(p, "*")
		if strings.HasPrefix(tipo, pref) && len(pref) > len(melhor) {
			melhor, cls = pref, c
		}
	}
	return cls, melhor != ""
}

// aos478Catalogo extrai os tipos do catálogo com o critério do gate event-catalog
// (RE_CONST_DECL de scripts/ci/event-catalog.py): constante cujo identificador contém
// `Event`, declarada numa linha, com valor `familia.segmento[.segmento]`.
func aos478Catalogo(t *testing.T) map[string]string {
	t.Helper()
	decl := regexp.MustCompile(`^[ \t]*(?:const )?[A-Za-z0-9_]*[Ee]vent[A-Za-z0-9_]*[ \t]*(?:=|[A-Za-z][A-Za-z0-9_.]* =)[ \t]*"([a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+)"[ \t]*$`)
	raiz := filepath.Join("..", "..")
	out := map[string]string{}
	err := filepath.WalkDir(raiz, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for sc.Scan() {
			if m := decl.FindStringSubmatch(sc.Text()); m != nil {
				if _, ja := out[m[1]]; !ja {
					out[m[1]] = p
				}
			}
		}
		return sc.Err()
	})
	if err != nil {
		t.Fatalf("varrer o catálogo: %v", err)
	}
	// Âncora de não-vacuidade: o gate event-catalog falha com zero constantes pela mesma razão.
	if len(out) < 100 {
		t.Fatalf("catálogo com %d tipos — o critério de extracção partiu-se", len(out))
	}
	return out
}

// aos478Model: o turno 1 pede passo_um; o turno 2 pede a tool de SANDBOX com um ExecRequest
// (o nó não compõe EffectRewriter sem AOS_MODEL_TOOLS, e o input chega ao launcher tal-qual);
// o turno 3 conclui. Não distingue runs: o run B só chega ao turno 1.
type aos478Model struct{ hits int64 }

func (m *aos478Model) Call(_ context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	atomic.AddInt64(&m.hits, 1)
	switch view.Turn {
	case 1:
		return acnCall("passo_um", "doc-a"), nil
	case 2:
		req, _ := json.Marshal(sandbox.ExecRequest{
			RunID: aos478RunA, StepID: "sbx-aos478",
			Call: sandbox.ToolCall{ToolID: aos478Sandbox, Command: "echo"},
		})
		return agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{{
			ToolID: aos478Sandbox, Capability: acnCap, Input: req,
			ResourceType: "doc", ResourceValue: "doc-sbx", ResourceRegion: "eu",
		}}}, nil
	default:
		return agentruntime.ModelResponse{Text: "concluido", Final: true}, nil
	}
}

// aos478Harness é o acnHarness (approval_cycle_node_test.go) com três diferenças: a segunda
// tool corre na sandbox de produção, há um operador pinado para os sinais de controlo, e o
// modelo é o de cima.
type aos478Harness struct {
	acnHarness
	opPriv ed25519.PrivateKey
}

func newAOS478Harness(t *testing.T) *aos478Harness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("AOS_SANDBOX_DRIVER", "") // fake: o driver de referência, fora de produção
	t.Setenv("AOS_SANDBOX_SEED_DIR", "")

	signer := acnSigner(t)
	levels := autonomy.NewLevelRegistry()
	if _, err := levels.SetLevel(ctx, acnAgent, "fs", autonomy.L4, "teste AOS-478", "operador"); err != nil {
		t.Fatalf("SetLevel: %v", err)
	}
	policyDP, err := pdp.Open("../../control-plane/pdp/policies", pdp.WithAutonomyOracle(levels))
	if err != nil {
		t.Fatalf("abrir bundle de politica de referencia: %v", err)
	}
	pubA, privA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, privB, _ := ed25519.GenerateKey(rand.Reader)
	opPub, opPriv, _ := ed25519.GenerateKey(rand.Reader)

	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.Model = &aos478Model{}
	cfg.Catalog = catalogStub{entries: []domain.Entry{
		acnEntry(t, signer, "passo_um"), acnEntry(t, signer, aos478Sandbox),
	}}
	cfg.SignedToolRegistry = nodeSignedRegistrySpec(signer, nil)
	cfg.IssuerClasses = map[string]identity.ClassPolicy{acnClass: {TTL: 15 * time.Minute, Scope: []string{acnCap}}}
	cfg.Policy = integration.StaticPolicy{MaxEgress: domain.EgressInternal}
	cfg.PDP = policyDP
	cfg.Authority = authz.NewStaticAuthoritySource().
		Set("human:"+tnHuman, acnCap).Set(acnAgent, acnCap).Set("agent:"+acnClass, acnCap)
	cfg.Approvers = []ApproverConfig{
		{Principal: "human:alice", PubKey: pubA, Authority: []string{"approve:danger", "approve:gray"}},
		{Principal: "human:bob", PubKey: pubB, Authority: []string{"approve:danger", "approve:gray"}},
	}
	cfg.Operators = map[string]ed25519.PublicKey{aos478Operador: opPub}
	cfg.SteerClock = tnClock()

	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })

	h := &aos478Harness{opPriv: opPriv}
	h.node, h.keys = node, [2]ed25519.PrivateKey{privA, privB}
	h.execs = map[string]*int64{"passo_um": new(int64)}
	if err := node.Runtime.Register("passo_um", func(context.Context, []byte) ([]byte, error) {
		atomic.AddInt64(h.execs["passo_um"], 1)
		return []byte(`{"content":"passo_um"}`), nil
	}); err != nil {
		t.Fatalf("Register passo_um: %v", err)
	}
	// A SANDBOX DE PRODUÇÃO: o mesmo registerSandboxLaunchers que o Bootstrap chama, com o
	// driver fake e o EventStoreSink sobre o Event Store do nó.
	if err := registerSandboxLaunchers(node.Runtime, node.EventStore,
		map[string]sandbox.SandboxBinding{aos478Sandbox: {Command: "echo"}}, nil, false, t.Logf); err != nil {
		t.Fatalf("registerSandboxLaunchers: %v", err)
	}
	svc, err := NewNodeService(node)
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	h.svc = svc
	return h
}

// aprova corre a cerimónia four-eyes REAL sobre o pendente do run (sem o Expire do helper do
// acnHarness: a expiração aqui vem do varrimento, que é o caminho de produção).
func (h *aos478Harness) aprova(t *testing.T, runID, requestID string) {
	t.Helper()
	ctx := context.Background()
	pend, err := h.node.PendingApprovals.ListForRun(ctx, runID)
	if err != nil || len(pend) != 1 {
		t.Fatalf("ListForRun(%s): err=%v n=%d", runID, err, len(pend))
	}
	req := integration.FourEyesRequest{RequestID: requestID, Preview: pend[0].Preview, RiskClass: risk.ClassDanger, DualControlRequired: true}
	legA := integration.SignFourEyesLeg(h.keys[0], req, "human:alice", "sess-A", "cred-A", acnChallenge(requestID, 'a'), nil)
	legB := integration.SignFourEyesLeg(h.keys[1], req, "human:bob", "sess-B", "cred-B", acnChallenge(requestID, 'b'), nil)
	if _, err := h.node.ApprovalBroker.Approve(ctx, requestID, req, legA, legB); err != nil {
		t.Fatalf("cerimonia four-eyes (%s): %v", requestID, err)
	}
}

// expiraPorVarrimento conduz o varrimento de PRODUÇÃO (TTL de produção) sobre o pendente do
// run com um relógio manual ancorado no `created_at` gravado (AOS-488). Antes encolhia-se o
// TTL para 1 ns e media-se no relógio de parede: no Windows o varrimento caía no mesmo tick
// do `created_at`, a idade dava 0 < 1 ns e o `approval.expired` não saía (2 em 6 runs).
func (h *aos478Harness) expiraPorVarrimento(t *testing.T, runID string) {
	t.Helper()
	ctx := context.Background()
	pend, err := h.node.PendingApprovals.ListForRun(ctx, runID)
	if err != nil || len(pend) != 1 {
		t.Fatalf("ListForRun(%s): err=%v n=%d", runID, err, len(pend))
	}
	criado, err := time.Parse(time.RFC3339Nano, pend[0].CreatedAt)
	if err != nil {
		t.Fatalf("created_at do pendente ilegivel %q: %v", pend[0].CreatedAt, err)
	}
	ttl := h.svc.approvalTTL
	relogio := testkit.NewManualClock(criado.Add(ttl - time.Nanosecond))
	h.svc.approvalClock = relogio.Now

	// Um nanossegundo antes do TTL o pendente ainda vale; no TTL expira. Se o varrimento
	// ignorasse este relógio, a idade no de parede (~0) nunca chegaria ao TTL de produção e
	// a segunda verificação avermelhava.
	h.svc.SweepApprovalsNow(ctx)
	if ainda, _ := h.node.PendingApprovals.ListForRun(ctx, runID); len(ainda) != 1 {
		t.Fatalf("antes do TTL o pendente de %s nao podia ter expirado; n=%d", runID, len(ainda))
	}
	relogio.Advance(time.Nanosecond)
	h.svc.SweepApprovalsNow(ctx)
	if resta, _ := h.node.PendingApprovals.ListForRun(ctx, runID); len(resta) != 0 {
		t.Fatalf("no TTL o varrimento tinha de expirar o pendente de %s; restam %d", runID, len(resta))
	}
}

func (h *aos478Harness) submeteRun(t *testing.T, runID string) {
	t.Helper()
	if err := h.svc.Submit(context.Background(), agentruntime.Goal{
		RunID: runID, Principal: referencemonitor.Principal{NHIID: acnAgent},
		// B6 da revisão: um titular DIFERENTE do agente — é o que torna observável, no nó, que
		// o autor no envelope do ledger não substituiu o titular da cifra.
		Subject:    aos478Titular,
		Credential: h.token(t), Objective: "pegadas do producer", MaxTurns: 5,
	}); err != nil {
		t.Fatalf("Submit(%s): %v", runID, err)
	}
	h.esperaFim(t, runID)
}

// aos478Cenario corre o cenário no nó real e devolve todos os eventos do Event Store.
func aos478Cenario(t *testing.T) []eventstore.Event {
	t.Helper()
	ctx := context.Background()
	h := newAOS478Harness(t)

	// Run A: turno 1 escala → aprova → retoma; turno 2 (sandbox) escala → aprova → retoma.
	h.submeteRun(t, aos478RunA)
	h.aprova(t, aos478RunA, "req-478-1")
	if err := h.svc.Resume(ctx, aos478RunA, h.token(t)); err != nil {
		t.Fatalf("Resume 1: %v", err)
	}
	h.esperaFim(t, aos478RunA)
	h.aprova(t, aos478RunA, "req-478-2")
	if err := h.svc.Resume(ctx, aos478RunA, h.token(t)); err != nil {
		t.Fatalf("Resume 2: %v", err)
	}
	h.esperaFim(t, aos478RunA)
	if fim, done := h.svc.Outcome(aos478RunA); !done || !fim.Result.Terminated {
		t.Fatalf("run A devia ter terminado: done=%t %+v", done, fim.Result)
	}

	// Sinais de controlo assinados pelo operador pinado.
	if err := h.node.Steer.Pause(ctx, aos478RunA, signedSignal(t, h.opPriv, aos478Operador, aos478RunA, control.SignalPause, nil)); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	corr := []byte("corrige o rumo")
	if err := h.node.Steer.Steer(ctx, aos478RunA, corr, signedSignal(t, h.opPriv, aos478Operador, aos478RunA, control.SignalSteer, corr)); err != nil {
		t.Fatalf("Steer: %v", err)
	}

	// Run B: escala e fica sem decisão; o varrimento de produção expira-o.
	h.submeteRun(t, aos478RunB)
	h.expiraPorVarrimento(t, aos478RunB)
	// approval.decided: o emissor é chamado pela rota de decisão de exaustão com o principal
	// VERIFICADO; aqui chama-se o mesmo emissor com um principal pinado.
	if err := h.node.PendingApprovals.Decide(ctx, integration.PendingKindExhaustion, aos478RunB, "exh-478", "stop", aos478Operador); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	streams, err := h.node.EventStore.Streams()
	if err != nil {
		t.Fatalf("Streams: %v", err)
	}
	var todos []eventstore.Event
	for _, s := range streams {
		evs, err := h.node.EventStore.Read(ctx, s, 1)
		if err != nil {
			t.Fatalf("Read(%s): %v", s, err)
		}
		todos = append(todos, evs...)
	}
	return todos
}

// TestAOS478_ProducerPorFamilia é o AC5 do AOS-478 (ver o comentário do ficheiro).
func TestAOS478_ProducerPorFamilia(t *testing.T) {
	contrato := aos478ContratoDoProducer(t)

	// (2) Catálogo → contrato: todo o tipo tem classe.
	catalogo := aos478Catalogo(t)
	var semClasse []string
	for tipo, onde := range catalogo {
		if _, ok := aos478Classe(contrato, tipo); !ok {
			semClasse = append(semClasse, tipo+" ("+onde+")")
		}
	}
	sort.Strings(semClasse)
	if len(semClasse) > 0 {
		t.Fatalf("tipos do catálogo sem linha no contrato do producer (tecnica/13 §3.1):\n  %s", strings.Join(semClasse, "\n  "))
	}

	// (3)+(4) Emissores reais.
	eventos := aos478Cenario(t)
	vistos := map[string]int{}
	for _, ev := range eventos {
		vistos[ev.Type]++
		if _, noCatalogo := catalogo[ev.Type]; !noCatalogo {
			t.Errorf("%s: tipo emitido que não está no catálogo do gate event-catalog", ev.Type)
		}
		cls, ok := aos478Classe(contrato, ev.Type)
		if !ok {
			t.Errorf("%s: emitido sem classe no contrato", ev.Type)
			continue
		}
		p := ev.Producer
		switch cls {
		case "fora-do-envelope":
			t.Errorf("%s: o contrato diz que é rótulo de audit, mas chegou ao Event Store", ev.Type)
		// A classe é EXACTA, não um mínimo: um contrato enfraquecido (cadeia → nhi_id, nhi_id →
		// componente) para um tipo emitido avermelha aqui, tal como um emissor que piore.
		case "cadeia":
			if p.NHIID == "" || len(p.DelegationChain) == 0 {
				t.Errorf("%s (stream %s seq %d): classe cadeia com producer=%+v", ev.Type, ev.StreamID, ev.Seq, p)
			}
		case "componente":
			if !strings.HasPrefix(p.NHIID, "nhi:") || len(p.DelegationChain) != 0 {
				t.Errorf("%s (stream %s seq %d): classe componente com producer=%+v", ev.Type, ev.StreamID, ev.Seq, p)
			}
		case "nhi_id":
			if p.NHIID == "" || len(p.DelegationChain) != 0 {
				t.Errorf("%s (stream %s seq %d): classe nhi_id com producer=%+v", ev.Type, ev.StreamID, ev.Seq, p)
			}
		}
	}

	// Não-vacuidade: as famílias que o AOS-478 corrigiu, e as que lhes servem de referência,
	// TÊM de ter sido exercitadas — senão o laço acima passava sobre nada.
	for _, tipo := range []string{
		// Só `escalated`: o `mediated` da retoma e o `outcome` partilham a chave (run_id, step_id)
		// do selo anterior e o Event Store deduplica-os (o WORM tem-nos). Defeito anterior a este
		// ticket, reportado à parte; o producer de ambos é coberto em reference-monitor.
		"tool.call.escalated",
		"step.ledger.applied",
		sandbox.EventInstanceCreated, sandbox.EventExecCompleted, sandbox.EventInstanceDestroyed,
		"approval.pending", "approval.granted", "approval.consumed", "approval.expired", "approval.decided",
		control.EventTypeControlPause, control.EventTypeControlSteer,
		"run.state.transition", "step.checkpoint", "lease.claimed", "lease.released",
		"turn.recorded", "replay.captured", "run.resume.record", "memory.record.written",
		"run.toolset.frozen",
	} {
		if vistos[tipo] == 0 {
			t.Errorf("o cenário não exercitou %s — a verificação deste tipo seria vácua", tipo)
		}
	}
	if t.Failed() {
		t.Logf("tipos vistos: %v", vistos)
	}

	aos478CoberturaDoContrato(t, contrato, catalogo, vistos)
	aos478Relacoes(t, eventos)
}

// aos478Unitario é um tipo que o cenário do nó NÃO emite, com a classe que o contrato lhe dá
// e o teste unitário que prova o producer no emissor real.
type aos478Unitario struct{ classe, pacote, teste string }

// aos478CobertosPorUnitario: os tipos que o cenário do nó não emite (não são compostos no nó,
// ou o Event Store deduplica-os — ver a nota da não-vacuidade) e o teste que os cobre. A
// classe aqui TEM de bater com a do contrato: é o que faz um contrato enfraquecido para um
// tipo NÃO emitido avermelhar (p.ex. `credential.*` → `componente`).
var aos478CobertosPorUnitario = map[string]aos478Unitario{
	"tool.call.mediated":               {"cadeia", "kernel/reference-monitor", "TestMediate_EventoNoEventStoreReal"},
	"tool.call.denied":                 {"cadeia", "kernel/reference-monitor", "TestAOS478_NegacaoLevaOPrincipal"},
	"tool.call.outcome":                {"cadeia", "kernel/reference-monitor", "TestAOS478_DesfechoLevaOMesmoProducerQueAMediacao"},
	"identity.nhi.issued":              {"cadeia", "platform/identity", "TestEvents_IssueAndRevoke_RealEventStore"},
	"identity.nhi.revoked":             {"nhi_id", "platform/identity", "TestAOS478_RevogacaoLevaOJTI"},
	"control.resume":                   {"nhi_id", "kernel/agent-runtime/control", "TestAOS478_ResumeLevaOEmissor"},
	"control.correction_consumed":      {"componente", "kernel/agent-runtime/control", "TestDefaultProducer_NuncaVazio"},
	"credential.exchange.issued":       {"nhi_id", "platform/broker", "TestAOS478_TrocaENegacaoLevamOPrincipal"},
	"credential.exchange.denied":       {"nhi_id", "platform/broker", "TestAOS478_TrocaENegacaoLevamOPrincipal"},
	"registry.artifact.published":      {"nhi_id", "platform/registry/adapters", "TestJournal_AppendAndReadAll"},
	"registry.artifact.status_changed": {"nhi_id", "platform/registry/adapters", "TestJournal_AppendAndReadAll"},
	"memory.record.deleted":            {"nhi_id", "platform/memory/adapters", "TestEventStore_TombstoneCarriesAttribution"},
	"memory.episode.recorded":          {"nhi_id", "platform/memory/episodic", "TestAOS478_EpisodioLevaOAgente"},
	"memory.semantic.fact.recorded":    {"nhi_id", "platform/memory/semantic", "TestAOS478_FactoEPromocaoLevamOAgente"},
	"memory.semantic.fact.promoted":    {"nhi_id", "platform/memory/semantic", "TestAOS478_FactoEPromocaoLevamOAgente"},
	"memory.context.compacted":         {"nhi_id", "platform/memory/compression", "TestAOS478_CompactacaoLevaOAgente"},
	"memory.migration.applied":         {"componente", "platform/memory/migrations", "TestAOS478_RegistoDeMigracaoLevaIdentidadeDeComponente"},
	"memory.migration.reverted":        {"componente", "platform/memory/migrations", "TestAOS478_RegistoDeMigracaoLevaIdentidadeDeComponente"},
	"worker.step.dispatched":           {"componente", "kernel/agent-runtime/worker", "TestAOS478_WorkerProducerPorEvento"},
}

// aos478CoberturaDoContrato: TODO o tipo do catálogo que o contrato marca `cadeia` ou
// `nhi_id` é emitido no cenário do nó OU consta de [aos478CobertosPorUnitario]. Cada entrada
// da lista tem de existir no catálogo, ter no contrato a MESMA classe, e nomear um teste que
// existe no pacote dito — uma lista que mentisse sobre a sua cobertura avermelha aqui.
func aos478CoberturaDoContrato(t *testing.T, contrato map[string]string, catalogo map[string]string, vistos map[string]int) {
	t.Helper()
	for tipo := range catalogo {
		cls, _ := aos478Classe(contrato, tipo)
		if cls != "cadeia" && cls != "nhi_id" {
			continue
		}
		if vistos[tipo] > 0 {
			continue
		}
		if _, ok := aos478CobertosPorUnitario[tipo]; !ok {
			t.Errorf("%s: o contrato marca-o %s, o cenário do nó não o emite e nenhum teste unitário está declarado a cobri-lo", tipo, cls)
		}
	}
	for tipo, u := range aos478CobertosPorUnitario {
		if _, ok := catalogo[tipo]; !ok {
			t.Errorf("cobertura declarada para %s, que não está no catálogo", tipo)
			continue
		}
		if cls, _ := aos478Classe(contrato, tipo); cls != u.classe {
			t.Errorf("%s: o contrato diz %q, o teste unitário %s prova %q", tipo, cls, u.teste, u.classe)
		}
		if !aos478TesteExiste(t, u.pacote, u.teste) {
			t.Errorf("%s: o teste %s não existe em packages/%s", tipo, u.teste, u.pacote)
		}
	}
}

// aos478TesteExiste procura `func <nome>(` nos ficheiros de teste do directório.
func aos478TesteExiste(t *testing.T, pacote, nome string) bool {
	t.Helper()
	fs, err := filepath.Glob(filepath.Join("..", "..", pacote, "*_test.go"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, f := range fs {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("ler %s: %v", f, err)
		}
		if strings.Contains(string(b), "func "+nome+"(") {
			return true
		}
	}
	return false
}

// aos478Relacoes verifica as relações que o contrato afirma entre envelopes do MESMO run
// (AC2 e AC4 do AOS-478).
func aos478Relacoes(t *testing.T, eventos []eventstore.Event) {
	t.Helper()
	// O producer do selo `tool.call.*` de cada tool, no run A (no Event Store é o `escalated`:
	// ver a nota da lista de não-vacuidade). A identidade já está resolvida quando a política
	// escala, pelo que é o principal VERIFICADO — o mesmo que o `mediated` levaria.
	mediado := map[string]eventstore.Producer{}
	for _, ev := range eventos {
		if ev.StreamID != aos478RunA || !strings.HasPrefix(ev.Type, "tool.call.") {
			continue
		}
		var pl struct {
			ToolID string `json:"tool_id"`
		}
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			t.Fatalf("payload de mediação: %v", err)
		}
		mediado[pl.ToolID] = ev.Producer
		// AC4: o principal da tool call é o agente VERIFICADO, com a cadeia human → agente.
		c := ev.Producer.DelegationChain
		if len(c) == 0 || c[0].Sub != "human:"+tnHuman || c[len(c)-1].ActAs != ev.Producer.NHIID {
			t.Errorf("%s: cadeia %+v não começa no humano %q nem acaba no nhi_id %q", ev.Type, c, tnHuman, ev.Producer.NHIID)
		}
	}
	if len(mediado) != 2 {
		t.Fatalf("esperava o selo das duas tools no run A; veio %v", mediado)
	}
	mesmo := func(a, b eventstore.Producer) bool {
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		return string(ja) == string(jb)
	}
	for _, ev := range eventos {
		switch {
		case strings.HasPrefix(ev.Type, "sandbox."):
			// AC2: o ciclo de vida da sandbox leva o principal da tool call que o causou.
			if !mesmo(ev.Producer, mediado[aos478Sandbox]) {
				t.Errorf("%s: producer %+v != o do tool.call.mediated de %s %+v", ev.Type, ev.Producer, aos478Sandbox, mediado[aos478Sandbox])
			}
		case ev.Type == "step.ledger.applied" && ev.StreamID == aos478RunA:
			// AC2: o step-ledger leva o principal da tool call do passo (as duas têm o mesmo).
			if !mesmo(ev.Producer, mediado["passo_um"]) {
				t.Errorf("step.ledger.applied %s: producer %+v != o do tool.call.mediated %+v", ev.StepID, ev.Producer, mediado["passo_um"])
			}
			// O titular da cifra continua a ser o do RUN, não o autor do envelope.
			var reg struct {
				Sealed  bool   `json:"sealed"`
				Subject string `json:"subject"`
			}
			if err := json.Unmarshal(ev.Payload, &reg); err != nil {
				t.Fatalf("payload do ledger: %v", err)
			}
			if !reg.Sealed || reg.Subject != aos478Titular {
				t.Errorf("step.ledger.applied %s: sealed=%t subject=%q, quer selado sob o titular %q", ev.StepID, reg.Sealed, reg.Subject, aos478Titular)
			}
		case ev.Type == "turn.recorded" || ev.Type == "replay.captured" || ev.Type == "run.resume.record":
			// AC4: o turno leva o principal do RUN (Goal.Principal), sem cadeia — a diferença
			// em relação ao tool.call.* está explicada no contrato.
			if ev.Producer.NHIID != acnAgent {
				t.Errorf("%s: producer.nhi_id=%q, o contrato diz o principal do run %q", ev.Type, ev.Producer.NHIID, acnAgent)
			}
		case ev.Type == "approval.pending" || ev.Type == "approval.consumed":
			if ev.Producer.NHIID != acnAgent {
				t.Errorf("%s: producer.nhi_id=%q, o contrato diz o principal apresentado pelo run %q", ev.Type, ev.Producer.NHIID, acnAgent)
			}
		case ev.Type == "approval.granted":
			if ev.Producer.NHIID != "human:alice" {
				t.Errorf("approval.granted: producer.nhi_id=%q, o contrato diz o primeiro aprovador", ev.Producer.NHIID)
			}
		case ev.Type == control.EventTypeControlPause || ev.Type == control.EventTypeControlSteer || ev.Type == "approval.decided":
			if ev.Producer.NHIID != aos478Operador {
				t.Errorf("%s: producer.nhi_id=%q, o contrato diz o emissor autenticado %q", ev.Type, ev.Producer.NHIID, aos478Operador)
			}
		}
	}
}
