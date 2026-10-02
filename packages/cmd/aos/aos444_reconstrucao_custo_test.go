package main

// AOS-444 — O CUSTO DE `GET /runs/{id}/reconstruct` É DO RUN, NÃO DO NÓ.
//
// O ticket abriu com a suspeita de que a reconstrução levava minutos em produção. A medição em
// produção refutou-a (42 ms; o que não respondeu foi o SSE ao vivo do /trajectory, lido como um
// pedido que termina). Este teste fica como guarda BARATA das suspeitas que o ticket nomeou, por
// CONTAGEM de operações e não por tempo de parede (determinista em CI):
//
//   - o Event Store só é lido no stream DESTE run, e nunca enumerado;
//   - o WORM faz UM `At` (residência) e UM `Append` (selo D6) — nenhuma verificação da cadeia
//     (`Read`/`Head`) por pedido;
//   - o Vault recebe UM `decrypt` por captura selada e nada mais — não um pedido por evento;
//
// e nada disto cresce com o resto do nó (outros runs no Event Store, outras partições no WORM).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// transitContado envolve o Vault falso do AOS-436 e conta os pedidos por operação.
type transitContado struct {
	inner http.Handler
	mu    sync.Mutex
	porOp map[string]int
}

func (c *transitContado) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	partes := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/"), "/")
	op := r.Method + " " + strings.Join(partes[:min(2, len(partes))], "/") // sem o nome da chave
	c.mu.Lock()
	c.porOp[op]++
	c.mu.Unlock()
	c.inner.ServeHTTP(w, r)
}

// esContado conta as leituras do Event Store (por stream) e as enumerações de streams.
type esContado struct {
	EventStorePort
	mu        sync.Mutex
	porStream map[string]int // stream -> eventos devolvidos
	reads     int
	streams   int
}

func (e *esContado) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	evs, err := e.EventStorePort.Read(ctx, streamID, fromSeq)
	e.mu.Lock()
	e.reads++
	e.porStream[streamID] += len(evs)
	e.mu.Unlock()
	return evs, err
}

func (e *esContado) Streams() ([]string, error) {
	e.mu.Lock()
	e.streams++
	e.mu.Unlock()
	return e.EventStorePort.Streams()
}

// wormContado conta as operações do WORM — a MESMA instância que o nó e a leitura soberana usam.
type wormContado struct {
	audit.Store
	mu                         sync.Mutex
	appends, reads, heads, ats int
}

func (s *wormContado) Append(ctx context.Context, rec audit.AuditRecord) (audit.AuditRecord, error) {
	s.mu.Lock()
	s.appends++
	s.mu.Unlock()
	return s.Store.Append(ctx, rec)
}

func (s *wormContado) Read(ctx context.Context, p string, from, to uint64) ([]audit.AuditRecord, error) {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
	return s.Store.Read(ctx, p, from, to)
}

func (s *wormContado) Head(ctx context.Context, p string) (uint64, error) {
	s.mu.Lock()
	s.heads++
	s.mu.Unlock()
	return s.Store.Head(ctx, p)
}

func (s *wormContado) At(ctx context.Context, p string, seq uint64) (audit.AuditRecord, bool, error) {
	s.mu.Lock()
	s.ats++
	s.mu.Unlock()
	return s.Store.At(ctx, p, seq)
}

// noAOS444 é o nó REAL (Bootstrap) com execução durável, soberania de leitura e a custódia Vault
// de produção (vaultKeyVault) sobre o Transit falso — com contadores nas três fronteiras de I/O.
type noAOS444 struct {
	node    *Node
	h       http.Handler
	es      *esContado
	worm    *wormContado
	transit *transitContado
}

func novoNoAOS444(t *testing.T) *noAOS444 {
	t.Helper()
	dir := t.TempDir()
	fs, err := audit.OpenFileStore(filepath.Join(dir, "worm.wal"))
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	worm := &wormContado{Store: fs}
	transit := &transitContado{inner: novoVaultComIdades(), porOp: map[string]int{}}
	srv := httptest.NewServer(transit)
	t.Cleanup(srv.Close)

	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORM = worm
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.BoardRegions = map[string]string{govBoard: govRegion}
	cfg.DSARVault = newVaultKeyVault(srv.URL, "transit", "tok")
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (gov+durable+vault): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	es := &esContado{EventStorePort: node.EventStore}
	node.EventStore = es
	_, h := newAPI(t, node)
	return &noAOS444{node: node, h: h, es: es, worm: worm, transit: transit}
}

// gravarTurnos grava `turnos` turnos como o loop do runtime: um turn.recorded e uma captura selada
// por-titular (o capturer REAL do nó) por turno — dois eventos por turno no stream do run.
func gravarTurnos(t *testing.T, node *Node, subject, runID string, turnos int) {
	t.Helper()
	ctx := context.Background()
	rt := agentruntime.NewTurnRecorder(node.EventStore)
	for turn := 1; turn <= turnos; turn++ {
		step := fmt.Sprintf("step-%06d", turn)
		if _, err := rt.Record(ctx, agentruntime.TurnRecord{
			RunID: runID, StepID: step, Turn: turn, Final: turn == turnos,
			Manifest: agentruntime.Manifest{
				PromptHash: fmt.Sprintf("sha256:aos444-%s-%d", runID, turn),
				Model:      agentruntime.ModelManifest{ModelID: "modelo-aos444", Seed: 1},
			},
		}); err != nil {
			t.Fatalf("Record turn.recorded: %v", err)
		}
		if err := node.Capturer.Capture(ctx, agentruntime.TurnCapture{
			RunID: runID, StepID: step, Turn: turn, Subject: subject,
			Response: agentruntime.ModelResponse{Text: fmt.Sprintf("turno %d", turn), Final: turn == turnos},
			ToolResults: []agentruntime.CapturedToolResult{{
				Invocation: agentruntime.ToolInvocation{ToolID: "echo"},
				Result:     agentruntime.Untrusted([]byte("saida")),
			}},
		}); err != nil {
			t.Fatalf("Capture: %v", err)
		}
	}
}

// custoReconstrucao é o que UM pedido de reconstrução gastou nas três fronteiras de I/O.
type custoReconstrucao struct {
	esReads, esEventosDoRun, esEventosAlheios, esStreams int
	wormAppends, wormAts, wormHeads, wormReads           int
	vaultTotal, vaultDecrypts                            int
}

func (c custoReconstrucao) String() string {
	return fmt.Sprintf("ES(reads=%d eventos-do-run=%d alheios=%d streams=%d) WORM(append=%d at=%d head=%d read=%d) Vault(total=%d decrypt=%d)",
		c.esReads, c.esEventosDoRun, c.esEventosAlheios, c.esStreams,
		c.wormAppends, c.wormAts, c.wormHeads, c.wormReads, c.vaultTotal, c.vaultDecrypts)
}

// reconstruir faz UM pedido autorizado de reconstrução e devolve o seu custo.
func (n *noAOS444) reconstruir(t *testing.T, runID string, turnos int) custoReconstrucao {
	t.Helper()
	n.es.mu.Lock()
	n.es.porStream, n.es.reads, n.es.streams = map[string]int{}, 0, 0
	n.es.mu.Unlock()
	n.worm.mu.Lock()
	n.worm.appends, n.worm.reads, n.worm.heads, n.worm.ats = 0, 0, 0, 0
	n.worm.mu.Unlock()
	n.transit.mu.Lock()
	n.transit.porOp = map[string]int{}
	n.transit.mu.Unlock()

	rec := getReq(n.h, "/runs/"+runID+"/reconstruct", govHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("reconstrução autorizada devia dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	var resp reconstructResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Turns) != turnos {
		t.Fatalf("esperava %d turnos reconstruídos: err=%v turnos=%d", turnos, err, len(resp.Turns))
	}

	c := custoReconstrucao{esReads: n.es.reads, esStreams: n.es.streams,
		wormAppends: n.worm.appends, wormAts: n.worm.ats, wormHeads: n.worm.heads, wormReads: n.worm.reads}
	for s, ev := range n.es.porStream {
		if s == runID {
			c.esEventosDoRun += ev
		} else {
			c.esEventosAlheios += ev
		}
	}
	for op, k := range n.transit.porOp {
		c.vaultTotal += k
		if op == "POST transit/decrypt" {
			c.vaultDecrypts += k
		}
	}
	return c
}

// TestAOS444_ReconstrucaoCustaOperacoesDoRun: o custo de reconstruir um run é do RUN — igual num nó
// limpo e num nó com outros runs e selos alheios — e cabe no orçamento que as suspeitas do ticket
// violariam (varrimento, verificação da cadeia por pedido, Vault por evento).
func TestAOS444_ReconstrucaoCustaOperacoesDoRun(t *testing.T) {
	const subject, runID, turnos = "nhi:agent-444", "run-444", 3

	limpo := novoNoAOS444(t)
	gravarTurnos(t, limpo.node, subject, runID, turnos)
	c := limpo.reconstruir(t, runID, turnos)

	if c.esReads == 0 || c.esEventosDoRun != c.esReads*2*turnos || c.esEventosAlheios != 0 || c.esStreams != 0 {
		t.Fatalf("o Event Store tem de ser lido só no stream do run (%d eventos por leitura) e nunca enumerado: %v", 2*turnos, c)
	}
	if c.wormAppends != 1 || c.wormAts != 1 || c.wormHeads != 0 || c.wormReads != 0 {
		t.Fatalf("o WORM tem de fazer 1 At (residência) + 1 Append (selo D6) e nenhuma verificação da cadeia: %v", c)
	}
	if c.vaultDecrypts != turnos || c.vaultTotal != turnos {
		t.Fatalf("o Vault tem de receber 1 decrypt por captura (%d) e nada mais: %v", turnos, c)
	}

	cheio := novoNoAOS444(t)
	for i := 0; i < 30; i++ {
		gravarTurnos(t, cheio.node, fmt.Sprintf("nhi:ruido-%d", i), fmt.Sprintf("run-ruido-%d", i), 2)
	}
	for i := 0; i < 500; i++ {
		if _, err := cheio.worm.Append(context.Background(), audit.AuditRecord{
			Partition: readAuditPartition(fmt.Sprintf("run-ruido-%d", i%30)), Decision: audit.DecisionAllow,
			Principal: audit.Principal{NHIID: govReader}, Capability: capReadOutcome,
			RunID: fmt.Sprintf("run-ruido-%d", i%30), ToolID: readAuditToolID,
			Resource: audit.Resource{Type: readResourceType, Value: "x", Region: govRegion},
		}); err != nil {
			t.Fatalf("Append ruido: %v", err)
		}
	}
	gravarTurnos(t, cheio.node, subject, runID, turnos)
	if c2 := cheio.reconstruir(t, runID, turnos); c2 != c {
		t.Fatalf("o custo da reconstrução CRESCEU com o resto do nó:\n  nó limpo: %v\n  nó cheio: %v", c, c2)
	}
}
