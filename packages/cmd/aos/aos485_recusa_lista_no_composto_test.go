package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pdp "github.com/aos-ref/control-plane/pdp"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-485 — a recusa pela lista-branca do run, no nó COMPOSTO como em produção: execução durável
// sobre Event Store e WORM em disco, cifra por-titular, bundle Cedar assinado, registo de tools
// assinado e TaintGate armado (a mesma preparação do `fase1Node`, AOS-069).
//
// O que este teste tem que o `TestAOS413_ToolsDoPostRunsCortaAToolForaDaLista` não tem: a tool
// pedida ESTÁ no catálogo assinado e a call é legítima. Naquele nó a `echo` não está no catálogo
// e, sem a lista, seria negada na mesma pela revalidação — «não executou» não provava que era a
// lista a impedir. Aqui a MESMA call executa quando a tool está na lista.

// aos485Medido é o que se lê do nó depois de um run.
type aos485Medido struct {
	execs          int64
	eventos        []eventstore.Event
	denialsAntes   float64
	denialsDepois  float64
	permitsDepois  float64
	permitsAntes   float64
	delegacaoRaiz  string
	turnoDaRecusa  string
	passoDaRecusa  string
	codigo, negou  string
	negados, media int
}

// aos485Metrica lê uma série sem rótulos do `/metrics` do nó.
func aos485Metrica(t *testing.T, h http.Handler, nome string) float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics devia dar 200, veio %d", rec.Code)
	}
	for _, linha := range strings.Split(rec.Body.String(), "\n") {
		if v, ok := strings.CutPrefix(linha, nome+" "); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				t.Fatalf("valor ilegivel da serie %s: %q", nome, linha)
			}
			return f
		}
	}
	t.Fatalf("a serie %s nao esta no /metrics", nome)
	return 0
}

// aos485NoComposto levanta o nó, submete um run de um nó de plano com a lista `tools` e devolve
// o que ficou medido.
func aos485NoComposto(t *testing.T, tools []string) aos485Medido {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	signer := durSigner(t)
	entry := counterEntry(t, signer)

	cfg := tnBaseConfig()
	cfg.DurableExecution = true // a via de produção: DurableDispatcher + step-ledger
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.DSARVault = audit.NewInMemoryKeyVault(nil)
	cfg.Model = &fase1Model{} // turno 1 pede `counter`; turno 2 conclui
	cfg.Catalog = catalogStub{entries: []domain.Entry{entry}}
	cfg.SignedToolRegistry = nodeSignedRegistrySpec(signer, nil, entry)
	cfg.IssuerClasses = map[string]identity.ClassPolicy{
		durClass: {TTL: 15 * time.Minute, Scope: []string{durCap}},
	}
	cfg.Policy = integration.StaticPolicy{MaxEgress: domain.EgressInternal}
	var err error
	cfg.PDP, err = pdp.Open(pdpPoliciesDir)
	if err != nil {
		t.Fatalf("abrir bundle de politica de referencia: %v", err)
	}
	cfg.Authority = authz.NewStaticAuthoritySource().
		Set("human:"+tnHuman, durCap).
		Set(durAgent, durCap).
		Set("agent:"+durClass, durCap)
	cfg.Privileged = referencemonitor.NewStaticPrivilegedSet("cap:http.post", durCap)

	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })

	var execs int64
	if err := node.Runtime.Register("counter", func(_ context.Context, _ []byte) ([]byte, error) {
		atomic.AddInt64(&execs, 1)
		return []byte("conteudo do documento"), nil
	}); err != nil {
		t.Fatalf("Register(counter): %v", err)
	}
	tok, err := node.Authority.MintForHuman(ctx, tnHuman, durAgent, durClass, []string{durCap})
	if err != nil {
		t.Fatalf("MintForHuman: %v", err)
	}

	svc, h := newAPI(t, node)
	var m aos485Medido
	m.denialsAntes = aos485Metrica(t, h, "aos_mediation_denials_total")
	m.permitsAntes = aos485Metrica(t, h, "aos_mediation_permits_total")

	const runID = "plan-485~n1"
	rec := postJSON(h, "POST", "/runs", map[string]any{
		"run_id":        runID,
		"objective":     "Le o documento notes e resume",
		"principal_nhi": durAgent,
		"credential":    tok.Compact,
		"tools":         tools,
		"inputs":        []map[string]string{},
		"max_turns":     3,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, ok, werr := svc.Wait(wctx, runID); werr != nil || !ok {
		t.Fatalf("o run devia ter sido hospedado e concluido: ok=%v err=%v", ok, werr)
	}

	m.execs = atomic.LoadInt64(&execs)
	m.denialsDepois = aos485Metrica(t, h, "aos_mediation_denials_total")
	m.permitsDepois = aos485Metrica(t, h, "aos_mediation_permits_total")
	if m.eventos, err = node.EventStore.Read(ctx, runID, 1); err != nil {
		t.Fatalf("ler o stream do run: %v", err)
	}
	turno1 := ""
	for _, e := range m.eventos {
		switch e.Type {
		case agentruntime.EventTypeTurnRecorded:
			if turno1 == "" {
				turno1 = e.StepID
			}
		case referencemonitor.EventTypeMediated:
			m.media++
		case referencemonitor.EventTypeDenied:
			m.negados++
			var p struct {
				Code      string `json:"code"`
				DeniedBy  string `json:"denied_by"`
				Principal struct {
					DelegationChain []struct {
						Sub string `json:"sub"`
					} `json:"delegation_chain"`
				} `json:"principal"`
			}
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatalf("payload do tool.call.denied ilegivel: %v", err)
			}
			m.codigo, m.negou, m.passoDaRecusa, m.turnoDaRecusa = p.Code, p.DeniedBy, e.StepID, e.ParentStepID
			if len(p.Principal.DelegationChain) > 0 {
				m.delegacaoRaiz = p.Principal.DelegationChain[0].Sub
			}
		}
	}
	if turno1 == "" {
		t.Fatalf("o stream do run nao tem o turn.recorded do turno 1 (%d eventos)", len(m.eventos))
	}
	if m.negados > 0 && (m.turnoDaRecusa != turno1 || m.passoDaRecusa != turno1+"-tool-1") {
		t.Fatalf("step_id=%q parent_step_id=%q, quero %q e %q", m.passoDaRecusa, m.turnoDaRecusa, turno1+"-tool-1", turno1)
	}
	return m
}

// Lista VAZIA — o run medido em produção: a tool não executa e a recusa fica no stream do run,
// atribuída à lista, com a cadeia de delegação do token verificado, e no `/metrics`.
func TestAOS485_NoComposto_ListaVaziaNegaPelaListaEConta(t *testing.T) {
	m := aos485NoComposto(t, []string{})
	if m.execs != 0 {
		t.Fatalf("a tool EXECUTOU %d vez(es) com a lista-branca vazia", m.execs)
	}
	if m.negados != 1 || m.media != 0 {
		t.Fatalf("queria 1 tool.call.denied e 0 tool.call.mediated, vieram %d e %d", m.negados, m.media)
	}
	if m.codigo != agentruntime.CodeToolOutsideRunAllowlist || m.negou != referencemonitor.RunAllowlistHookName {
		t.Fatalf("a recusa saiu por outra razao: code=%q denied_by=%q", m.codigo, m.negou)
	}
	if m.delegacaoRaiz != "human:"+tnHuman {
		t.Fatalf("a recusa nao leva a cadeia de delegacao do token verificado: raiz=%q", m.delegacaoRaiz)
	}
	// A série que o operador lê: lida do /metrics, não do contador em memória.
	if m.denialsDepois != m.denialsAntes+1 || m.permitsDepois != m.permitsAntes {
		t.Fatalf("/metrics: aos_mediation_denials_total %v→%v (quero +1), aos_mediation_permits_total %v→%v (quero igual)",
			m.denialsAntes, m.denialsDepois, m.permitsAntes, m.permitsDepois)
	}
	for _, e := range m.eventos {
		if strings.HasPrefix(e.Type, "sandbox.") || e.Type == referencemonitor.EventTypeOutcome || e.Type == "step.ledger.applied" {
			t.Fatalf("o stream tem um %s: nada podia ter sido despachado nem memorizado", e.Type)
		}
	}
}

// CONTROLO POSITIVO: com a tool NA lista, a mesma call, no mesmo nó, EXECUTA. É o que prova que
// no teste acima foi a lista a impedir, e não outro gate da cadeia.
func TestAOS485_NoComposto_ToolNaListaExecuta(t *testing.T) {
	m := aos485NoComposto(t, []string{"counter"})
	if m.execs != 1 || m.media != 1 || m.negados != 0 {
		t.Fatalf("com a tool na lista a call tinha de executar: execs=%d mediated=%d denied=%d (denied_by=%q)", m.execs, m.media, m.negados, m.negou)
	}
	if m.denialsDepois != m.denialsAntes || m.permitsDepois != m.permitsAntes+1 {
		t.Fatalf("/metrics: denials %v→%v (quero igual), permits %v→%v (quero +1)", m.denialsAntes, m.denialsDepois, m.permitsAntes, m.permitsDepois)
	}
}

// TestAOS485_RegistoDeRetomaLevaAListaDoGoal guarda a ÚNICA projecção Goal → registo de retoma
// do nó ([resumeRecordFromGoal]). Sem a linha da lista, o run retomado depois de um reinício
// voltava com a lista ausente — sem restrição — e nenhum teste do nó o via (o AOS-413 testou o
// registo e a sua persistência, não esta projecção). Vai até ao goal retomado, passando pelo
// registo durável: nil fica nil, vazia fica vazia, e a lista fica a lista.
func TestAOS485_RegistoDeRetomaLevaAListaDoGoal(t *testing.T) {
	casos := map[string][]string{"ausente": nil, "vazia": {}, "uma tool": {"counter"}}
	for nome, lista := range casos {
		t.Run(nome, func(t *testing.T) {
			es, err := eventstore.New()
			if err != nil {
				t.Fatalf("eventstore.New: %v", err)
			}
			defer es.Close()
			registos, err := integration.NewResumeRecords(es, nil)
			if err != nil {
				t.Fatalf("NewResumeRecords: %v", err)
			}
			goal := agentruntime.Goal{
				RunID:        "run-485-retoma",
				Principal:    referencemonitor.Principal{NHIID: durAgent, AgentID: durAgent},
				Objective:    "o trabalho de um no do plano",
				AllowedTools: lista,
			}
			ctx := context.Background()
			if err := registos.Put(ctx, resumeRecordFromGoal(goal)); err != nil {
				t.Fatalf("Put: %v", err)
			}
			rec, ok, err := registos.Get(ctx, goal.RunID)
			if err != nil || !ok {
				t.Fatalf("Get: ok=%v err=%v", ok, err)
			}
			// DeepEqual distingue nil de vazia.
			if retomado := rec.GoalWith("credencial-fresca"); !reflect.DeepEqual(retomado.AllowedTools, lista) {
				t.Fatalf("a lista %#v voltou da retoma como %#v", lista, retomado.AllowedTools)
			}
		})
	}
}
