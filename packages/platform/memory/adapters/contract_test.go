package adapters_test

import (
	"context"
	"errors"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/memory/adapters"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/ports"
	"github.com/aos-ref/substrate/eventstore"
)

// fixedTime é um instante determinístico para os metadados created_at.
var fixedTime = time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)

// portFactory constrói uma MemoryPort nova e, opcionalmente, um "reopen" que
// devolve uma porta FRESCA sobre o MESMO backing store. Para o Event Store,
// reopen prova a reconstrução a partir do log append-only (o novo adaptador não
// partilha qualquer estado em RAM com o anterior). Para o in-memory, reopen
// devolve a mesma instância (o estado vive na própria porta).
type portFactory struct {
	name    string
	newPort func(t *testing.T) (ports.MemoryPort, func(p ports.MemoryPort) ports.MemoryPort, func())
}

// factories devolve os dois backends que TÊM de passar a mesma suite de contrato.
func factories() []portFactory {
	return []portFactory{
		{
			name: "inmemory",
			newPort: func(t *testing.T) (ports.MemoryPort, func(ports.MemoryPort) ports.MemoryPort, func()) {
				p := adapters.NewInMemoryAdapter()
				reopen := func(prev ports.MemoryPort) ports.MemoryPort { return prev }
				return p, reopen, func() {}
			},
		},
		{
			name: "eventstore",
			newPort: func(t *testing.T) (ports.MemoryPort, func(ports.MemoryPort) ports.MemoryPort, func()) {
				store, err := eventstore.New()
				if err != nil {
					t.Fatalf("eventstore.New: %v", err)
				}
				p := adapters.NewEventStoreAdapter(store)
				// reopen materializa um adaptador NOVO sobre o mesmo store: a
				// leitura tem de reconstruir tudo a partir do log.
				reopen := func(ports.MemoryPort) ports.MemoryPort {
					return adapters.NewEventStoreAdapter(store)
				}
				return p, reopen, func() { _ = store.Close() }
			},
		},
	}
}

// delCtx devolve uma atribuição de Delete válida (a remoção é auditável/atribuída).
func delCtx() ports.DeleteContext {
	return ports.DeleteContext{AgentID: "agent-a", RunID: "run-del", Provenance: domain.ProvenanceTrusted}
}

// recFor constrói um registo válido de uma classe, com o body tipado correcto.
func recFor(class domain.MemoryClass, id, agentID, runID string, prov domain.Provenance) domain.Record {
	var body domain.Body
	switch class {
	case domain.ClassEpisodic:
		body = domain.EpisodicBody{TraceID: "trace-" + id, Goal: "g", Outcome: "success", StepCount: 3, Summary: "s"}
	case domain.ClassSemantic:
		body = domain.SemanticBody{Subject: "sky", Predicate: "is", Object: "blue", Confidence: 0.9}
	case domain.ClassProcedural:
		body = domain.ProceduralBody{SkillName: "sk-" + id, Version: "1.0.0", DefinitionHash: "h", Stage: "staging"}
	case domain.ClassWorking:
		body = domain.WorkingBody{TurnIndex: 1, Content: "ctx", TokenCount: 42}
	}
	return domain.Record{
		ID:    id,
		Class: class,
		Metadata: domain.Metadata{
			AgentID:       agentID,
			RunID:         runID,
			Provenance:    prov,
			CreatedAt:     fixedTime,
			TTLClass:      domain.TTLStandard,
			SchemaVersion: "1.0.0",
		},
		Body: body,
	}
}

// TestContract corre a MESMA suite table-driven contra AMBOS os adaptadores.
// É o teste central de AOS-035: prova que a MemoryPort é substituível por
// configuração sem alterar chamadores (backend-swap).
func TestContract(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		run  func(t *testing.T, p ports.MemoryPort, reopen func(ports.MemoryPort) ports.MemoryPort)
	}{
		{
			name: "version_matches_port_contract",
			run: func(t *testing.T, p ports.MemoryPort, _ func(ports.MemoryPort) ports.MemoryPort) {
				if got := p.Version(); got != ports.PortVersion {
					t.Fatalf("Version() = %q, quer %q", got, ports.PortVersion)
				}
			},
		},
		{
			name: "put_then_get_roundtrip",
			run: func(t *testing.T, p ports.MemoryPort, reopen func(ports.MemoryPort) ports.MemoryPort) {
				in := recFor(domain.ClassEpisodic, "e1", "agent-a", "run-1", domain.ProvenanceTrusted)
				if _, err := p.Put(ctx, in); err != nil {
					t.Fatalf("Put: %v", err)
				}
				p = reopen(p) // Event Store: força reconstrução do log.
				got, err := p.Get(ctx, domain.ClassEpisodic, "e1")
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				if got.ID != "e1" || got.Metadata.AgentID != "agent-a" {
					t.Fatalf("registo reconstruido errado: %+v", got)
				}
				eb, ok := got.Body.(domain.EpisodicBody)
				if !ok || eb.TraceID != "trace-e1" || eb.Outcome != "success" {
					t.Fatalf("body episodico reconstruido errado: %+v", got.Body)
				}
			},
		},
		{
			name: "get_missing_is_not_found",
			run: func(t *testing.T, p ports.MemoryPort, _ func(ports.MemoryPort) ports.MemoryPort) {
				_, err := p.Get(ctx, domain.ClassSemantic, "nope")
				if !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("quer ErrNotFound, obteve %v", err)
				}
			},
		},
		{
			name: "idempotent_put_same_run_mem_id",
			run: func(t *testing.T, p ports.MemoryPort, reopen func(ports.MemoryPort) ports.MemoryPort) {
				in := recFor(domain.ClassSemantic, "s1", "agent-a", "run-1", domain.ProvenanceTrusted)
				r1, err := p.Put(ctx, in)
				if err != nil {
					t.Fatalf("Put#1: %v", err)
				}
				// Segunda escrita com a MESMA f(run_id, class, id): no-op observável.
				dup := in
				dup.Body = domain.SemanticBody{Subject: "outro", Predicate: "p", Object: "o", Confidence: 0.1}
				r2, err := p.Put(ctx, dup)
				if err != nil {
					t.Fatalf("Put#2: %v", err)
				}
				if r1.ID != r2.ID {
					t.Fatalf("ids divergem: %q vs %q", r1.ID, r2.ID)
				}
				p = reopen(p)
				got, err := p.Get(ctx, domain.ClassSemantic, "s1")
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				// O duplicado ganha: o corpo é o da PRIMEIRA escrita.
				sb := got.Body.(domain.SemanticBody)
				if sb.Object != "blue" {
					t.Fatalf("duplicado nao preservou a 1a escrita: %+v", sb)
				}
				all, _ := p.Query(ctx, ports.Query{Class: domain.ClassSemantic})
				if len(all) != 1 {
					t.Fatalf("idempotencia falhou: %d registos, quer 1", len(all))
				}
			},
		},
		{
			name: "classes_are_distinct_no_crosstalk",
			run: func(t *testing.T, p ports.MemoryPort, reopen func(ports.MemoryPort) ports.MemoryPort) {
				// MESMO id "x" em duas classes diferentes: não se cruzam.
				epi := recFor(domain.ClassEpisodic, "x", "agent-a", "run-1", domain.ProvenanceTrusted)
				sem := recFor(domain.ClassSemantic, "x", "agent-a", "run-1", domain.ProvenanceTrusted)
				if _, err := p.Put(ctx, epi); err != nil {
					t.Fatalf("Put episodic: %v", err)
				}
				if _, err := p.Put(ctx, sem); err != nil {
					t.Fatalf("Put semantic: %v", err)
				}
				p = reopen(p)

				gotEpi, err := p.Get(ctx, domain.ClassEpisodic, "x")
				if err != nil || gotEpi.Class != domain.ClassEpisodic {
					t.Fatalf("Get episodic devolveu %+v err=%v", gotEpi, err)
				}
				if _, ok := gotEpi.Body.(domain.EpisodicBody); !ok {
					t.Fatalf("Get episodic devolveu body errado: %T", gotEpi.Body)
				}
				gotSem, err := p.Get(ctx, domain.ClassSemantic, "x")
				if err != nil || gotSem.Class != domain.ClassSemantic {
					t.Fatalf("Get semantic devolveu %+v err=%v", gotSem, err)
				}
				if _, ok := gotSem.Body.(domain.SemanticBody); !ok {
					t.Fatalf("Get semantic devolveu body errado: %T", gotSem.Body)
				}
				// Query de uma classe nunca traz a outra.
				qEpi, _ := p.Query(ctx, ports.Query{Class: domain.ClassEpisodic})
				if len(qEpi) != 1 || qEpi[0].Class != domain.ClassEpisodic {
					t.Fatalf("Query episodic cruzou classes: %+v", qEpi)
				}
				qWork, _ := p.Query(ctx, ports.Query{Class: domain.ClassWorking})
				if len(qWork) != 0 {
					t.Fatalf("Query working devia ser vazia, veio %d", len(qWork))
				}
			},
		},
		{
			name: "all_four_classes_independent",
			run: func(t *testing.T, p ports.MemoryPort, reopen func(ports.MemoryPort) ports.MemoryPort) {
				for i, c := range domain.AllClasses() {
					r := recFor(c, "id", "agent-a", "run-1", domain.ProvenanceTrusted)
					if _, err := p.Put(ctx, r); err != nil {
						t.Fatalf("Put classe %s: %v", c, err)
					}
					_ = i
				}
				p = reopen(p)
				for _, c := range domain.AllClasses() {
					q, err := p.Query(ctx, ports.Query{Class: c})
					if err != nil {
						t.Fatalf("Query %s: %v", c, err)
					}
					if len(q) != 1 {
						t.Fatalf("classe %s: %d registos, quer 1", c, len(q))
					}
					if q[0].Class != c {
						t.Fatalf("classe %s devolveu classe %s", c, q[0].Class)
					}
				}
			},
		},
		{
			name: "query_filters_by_agent_run_provenance",
			run: func(t *testing.T, p ports.MemoryPort, reopen func(ports.MemoryPort) ports.MemoryPort) {
				must := func(_ domain.Record, err error) {
					if err != nil {
						t.Fatalf("Put: %v", err)
					}
				}
				must(p.Put(ctx, recFor(domain.ClassSemantic, "a", "agent-a", "run-1", domain.ProvenanceTrusted)))
				must(p.Put(ctx, recFor(domain.ClassSemantic, "b", "agent-b", "run-1", domain.ProvenanceUntrusted)))
				must(p.Put(ctx, recFor(domain.ClassSemantic, "c", "agent-a", "run-2", domain.ProvenanceTrusted)))
				p = reopen(p)

				byAgent, _ := p.Query(ctx, ports.Query{Class: domain.ClassSemantic, AgentID: "agent-a"})
				if len(byAgent) != 2 {
					t.Fatalf("filtro agent-a: %d, quer 2", len(byAgent))
				}
				byRun, _ := p.Query(ctx, ports.Query{Class: domain.ClassSemantic, RunID: "run-2"})
				if len(byRun) != 1 || byRun[0].ID != "c" {
					t.Fatalf("filtro run-2 errado: %+v", byRun)
				}
				unt := domain.ProvenanceUntrusted
				byProv, _ := p.Query(ctx, ports.Query{Class: domain.ClassSemantic, Provenance: &unt})
				if len(byProv) != 1 || byProv[0].ID != "b" {
					t.Fatalf("filtro untrusted errado: %+v", byProv)
				}
			},
		},
		{
			name: "delete_tombstone_then_get_not_found",
			run: func(t *testing.T, p ports.MemoryPort, reopen func(ports.MemoryPort) ports.MemoryPort) {
				must := func(_ domain.Record, err error) {
					if err != nil {
						t.Fatalf("Put: %v", err)
					}
				}
				must(p.Put(ctx, recFor(domain.ClassProcedural, "k1", "agent-a", "run-1", domain.ProvenanceTrusted)))
				must(p.Put(ctx, recFor(domain.ClassProcedural, "k2", "agent-a", "run-1", domain.ProvenanceTrusted)))
				if err := p.Delete(ctx, domain.ClassProcedural, "k1", delCtx()); err != nil {
					t.Fatalf("Delete: %v", err)
				}
				// Delete idempotente: repetir não é erro.
				if err := p.Delete(ctx, domain.ClassProcedural, "k1", delCtx()); err != nil {
					t.Fatalf("Delete#2: %v", err)
				}
				p = reopen(p)
				if _, err := p.Get(ctx, domain.ClassProcedural, "k1"); !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("k1 devia estar apagado, err=%v", err)
				}
				got, err := p.Get(ctx, domain.ClassProcedural, "k2")
				if err != nil || got.ID != "k2" {
					t.Fatalf("k2 devia sobreviver ao delete de k1: %+v err=%v", got, err)
				}
			},
		},
		{
			// F1 regression: put->delete->recriar(run distinto)->delete tem de deixar
			// o registo APAGADO em AMBOS os backends. Antes da correcção, a dedup
			// global/permanente do Event Store engolia o 2.º tombstone (mesma
			// idempotency_key do 1.º) e o registo ressuscitava (buraco de erasure).
			name: "put_delete_recreate_delete_stays_deleted",
			run: func(t *testing.T, p ports.MemoryPort, reopen func(ports.MemoryPort) ports.MemoryPort) {
				put := func(runID string) {
					if _, err := p.Put(ctx, recFor(domain.ClassSemantic, "r1", "agent-a", runID, domain.ProvenanceTrusted)); err != nil {
						t.Fatalf("Put(%s): %v", runID, err)
					}
				}
				put("run-1")
				if err := p.Delete(ctx, domain.ClassSemantic, "r1", delCtx()); err != nil {
					t.Fatalf("Delete#1: %v", err)
				}
				// Recriar exige um run_id NOVO (com o mesmo run seria no-op de
				// idempotência de Put, não uma recriação).
				put("run-2")
				p = reopen(p)
				if _, err := p.Get(ctx, domain.ClassSemantic, "r1"); err != nil {
					t.Fatalf("apos recriacao o registo devia estar vivo: %v", err)
				}
				if err := p.Delete(ctx, domain.ClassSemantic, "r1", delCtx()); err != nil {
					t.Fatalf("Delete#2: %v", err)
				}
				p = reopen(p)
				if _, err := p.Get(ctx, domain.ClassSemantic, "r1"); !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("apos o 2.º delete o registo devia estar apagado, err=%v", err)
				}
			},
		},
		{
			// F3(b): put->delete->re-put com a MESMA chave f(run,class,id) tem de dar
			// o mesmo resultado nos dois backends — o registo permanece apagado (o
			// re-put é no-op de idempotência, não ressuscita o tombstone).
			name: "put_delete_reput_same_key_stays_deleted",
			run: func(t *testing.T, p ports.MemoryPort, reopen func(ports.MemoryPort) ports.MemoryPort) {
				in := recFor(domain.ClassWorking, "u1", "agent-a", "run-1", domain.ProvenanceTrusted)
				if _, err := p.Put(ctx, in); err != nil {
					t.Fatalf("Put: %v", err)
				}
				if err := p.Delete(ctx, domain.ClassWorking, "u1", delCtx()); err != nil {
					t.Fatalf("Delete: %v", err)
				}
				if _, err := p.Put(ctx, in); err != nil { // mesma chave: no-op
					t.Fatalf("re-Put: %v", err)
				}
				p = reopen(p)
				if _, err := p.Get(ctx, domain.ClassWorking, "u1"); !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("re-Put da mesma chave nao devia ressuscitar, err=%v", err)
				}
			},
		},
		{
			// F3(c): a ordem estável de Query após um update (nova chave sobre o mesmo
			// class+id, last-write-wins) tem de ser IDÊNTICA nos dois backends, mesmo
			// após reopen (reconstrução do log no Event Store).
			name: "query_stable_order_after_update_reopen",
			run: func(t *testing.T, p ports.MemoryPort, reopen func(ports.MemoryPort) ports.MemoryPort) {
				must := func(_ domain.Record, err error) {
					if err != nil {
						t.Fatalf("Put: %v", err)
					}
				}
				must(p.Put(ctx, recFor(domain.ClassEpisodic, "A", "agent-a", "run-1", domain.ProvenanceTrusted)))
				must(p.Put(ctx, recFor(domain.ClassEpisodic, "B", "agent-a", "run-1", domain.ProvenanceTrusted)))
				// Update A com um run novo: A passa a ter a escrita mais recente.
				upd := recFor(domain.ClassEpisodic, "A", "agent-a", "run-2", domain.ProvenanceTrusted)
				upd.Body = domain.EpisodicBody{TraceID: "trace-A", Goal: "g2", Outcome: "failed", StepCount: 7, Summary: "s2"}
				must(p.Put(ctx, upd))
				p = reopen(p)
				got, err := p.Query(ctx, ports.Query{Class: domain.ClassEpisodic})
				if err != nil {
					t.Fatalf("Query: %v", err)
				}
				ids := make([]string, len(got))
				for i, r := range got {
					ids[i] = r.ID
				}
				// B foi fixado antes da última escrita de A → ordem estável [B, A].
				if len(ids) != 2 || ids[0] != "B" || ids[1] != "A" {
					t.Fatalf("ordem estavel apos update divergiu: %v", ids)
				}
			},
		},
		{
			// F2: um Delete sem atribuição (agent_id/run_id/provenance) falha-fecha
			// IDENTICAMENTE nos dois backends e NÃO apaga nada.
			name: "delete_without_attribution_fails_closed",
			run: func(t *testing.T, p ports.MemoryPort, reopen func(ports.MemoryPort) ports.MemoryPort) {
				if _, err := p.Put(ctx, recFor(domain.ClassSemantic, "d1", "agent-a", "run-1", domain.ProvenanceTrusted)); err != nil {
					t.Fatalf("Put: %v", err)
				}
				if err := p.Delete(ctx, domain.ClassSemantic, "d1", ports.DeleteContext{}); !errors.Is(err, domain.ErrMissingAgentID) {
					t.Fatalf("quer ErrMissingAgentID, obteve %v", err)
				}
				noProv := ports.DeleteContext{AgentID: "agent-a", RunID: "run-1"}
				if err := p.Delete(ctx, domain.ClassSemantic, "d1", noProv); !errors.Is(err, domain.ErrMissingProvenance) {
					t.Fatalf("quer ErrMissingProvenance, obteve %v", err)
				}
				// Fail-closed: o registo continua vivo (nada foi apagado).
				p = reopen(p)
				if _, err := p.Get(ctx, domain.ClassSemantic, "d1"); err != nil {
					t.Fatalf("delete rejeitado nao devia apagar: %v", err)
				}
			},
		},
		{
			name: "write_without_provenance_fails_closed",
			run: func(t *testing.T, p ports.MemoryPort, _ func(ports.MemoryPort) ports.MemoryPort) {
				bad := recFor(domain.ClassSemantic, "np", "agent-a", "run-1", domain.ProvenanceTrusted)
				bad.Metadata.Provenance = "" // remove proveniência
				if _, err := p.Put(ctx, bad); !errors.Is(err, domain.ErrMissingProvenance) {
					t.Fatalf("quer ErrMissingProvenance, obteve %v", err)
				}
				// E não persistiu nada (fail-closed).
				if _, err := p.Get(ctx, domain.ClassSemantic, "np"); !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("registo rejeitado nao devia existir, err=%v", err)
				}
			},
		},
		{
			name: "write_without_schema_version_fails_closed",
			run: func(t *testing.T, p ports.MemoryPort, _ func(ports.MemoryPort) ports.MemoryPort) {
				bad := recFor(domain.ClassSemantic, "ns", "agent-a", "run-1", domain.ProvenanceTrusted)
				bad.Metadata.SchemaVersion = "" // remove schema_version
				if _, err := p.Put(ctx, bad); !errors.Is(err, domain.ErrMissingSchemaVersion) {
					t.Fatalf("quer ErrMissingSchemaVersion, obteve %v", err)
				}
				if _, err := p.Get(ctx, domain.ClassSemantic, "ns"); !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("registo rejeitado nao devia existir, err=%v", err)
				}
			},
		},
		{
			name: "written_record_carries_all_mandatory_metadata",
			run: func(t *testing.T, p ports.MemoryPort, reopen func(ports.MemoryPort) ports.MemoryPort) {
				in := recFor(domain.ClassEpisodic, "m1", "agent-z", "run-9", domain.ProvenanceUntrusted)
				if _, err := p.Put(ctx, in); err != nil {
					t.Fatalf("Put: %v", err)
				}
				p = reopen(p)
				got, err := p.Get(ctx, domain.ClassEpisodic, "m1")
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				// Todos os metadados obrigatórios sobrevivem ao round-trip.
				m := got.Metadata
				if m.AgentID != "agent-z" || m.RunID != "run-9" ||
					m.Provenance != domain.ProvenanceUntrusted ||
					m.TTLClass != domain.TTLStandard || m.SchemaVersion != "1.0.0" ||
					!m.CreatedAt.Equal(fixedTime) {
					t.Fatalf("metadados obrigatorios corrompidos: %+v", m)
				}
				if err := got.Validate(); err != nil {
					t.Fatalf("registo persistido nao valida: %v", err)
				}
			},
		},
	}

	for _, f := range factories() {
		for _, tc := range cases {
			t.Run(f.name+"/"+tc.name, func(t *testing.T) {
				p, reopen, cleanup := f.newPort(t)
				t.Cleanup(cleanup)
				tc.run(t, p, reopen)
			})
		}
	}
}

// TestContract_TracerEmitsSpanPerOperation prova que cada operação de porta emite
// um span (via a porta Tracer zero-dep do Agent Runtime), em ambos os backends.
func TestContract_TracerEmitsSpanPerOperation(t *testing.T) {
	ctx := context.Background()

	for _, backend := range []string{"inmemory", "eventstore"} {
		t.Run(backend, func(t *testing.T) {
			tr := &agentruntime.RecordingTracer{}
			var p ports.MemoryPort
			switch backend {
			case "inmemory":
				p = adapters.NewInMemoryAdapter(adapters.WithInMemoryTracer(tr))
			case "eventstore":
				store, err := eventstore.New()
				if err != nil {
					t.Fatalf("eventstore.New: %v", err)
				}
				t.Cleanup(func() { _ = store.Close() })
				p = adapters.NewEventStoreAdapter(store, adapters.WithEventStoreTracer(tr))
			}

			in := recFor(domain.ClassWorking, "w1", "agent-a", "run-1", domain.ProvenanceTrusted)
			if _, err := p.Put(ctx, in); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if _, err := p.Get(ctx, domain.ClassWorking, "w1"); err != nil {
				t.Fatalf("Get: %v", err)
			}
			if _, err := p.Query(ctx, ports.Query{Class: domain.ClassWorking}); err != nil {
				t.Fatalf("Query: %v", err)
			}
			if err := p.Delete(ctx, domain.ClassWorking, "w1", delCtx()); err != nil {
				t.Fatalf("Delete: %v", err)
			}

			for _, op := range []string{"memory.put", "memory.get", "memory.query", "memory.delete"} {
				if len(tr.SpansByOperation(op)) == 0 {
					t.Fatalf("backend %s: sem span para %q", backend, op)
				}
			}
			for _, sp := range tr.Spans() {
				if !sp.Ended {
					t.Fatalf("span %q nao foi fechado", sp.Operation)
				}
			}
		})
	}
}
