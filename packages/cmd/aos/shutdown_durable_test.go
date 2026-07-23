package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	eventstore "github.com/aos-ref/substrate/eventstore"
)

// AOS-164b — SHUTDOWN GRACIOSO DURÁVEL (sobre AOS-170). Prova, com o Event Store durável
// REAL (EventStorePath, não um mock), que o encerramento não perde nem duplica trabalho
// DURÁVEL: (1) os eventos committed antes do shutdown sobrevivem a um REINÍCIO (replay do
// WAL) byte-a-byte, sem perda nem duplicação; (2) a idempotência/dedup do log é
// reconstruída no replay; (3) o lease do run cancelado no deadline fica reclamável após o
// reinício e um novo claim minta um token ESTRITAMENTE MAIOR — um token residual da
// execução anterior seria fenced (sem dupla-execução).

// completeOrBlockModel conclui um run no 1º turno (gravando um turno DURÁVEL no Event
// Store) EXCEPTO quando o prompt materializado contém o marcador de bloqueio: nesse caso
// bloqueia até o ctx ser cancelado (simula um run em-curso que o shutdown terá de cancelar
// cooperativamente no deadline).
type completeOrBlockModel struct {
	blockMarker string
	started     chan struct{}
	once        sync.Once
}

func (m *completeOrBlockModel) Call(ctx context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	if bytes.Contains(view.Materialized, []byte(m.blockMarker)) {
		m.once.Do(func() {
			if m.started != nil {
				close(m.started)
			}
		})
		<-ctx.Done()
		return agentruntime.ModelResponse{}, ctx.Err()
	}
	return agentruntime.ModelResponse{
		Text:  "run concluido",
		Final: true,
		Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

func TestServiceShutdownDurable_NoLossNoDupNoDoubleExecAfterRestart(t *testing.T) {
	dir := t.TempDir()
	esPath := filepath.Join(dir, "events.wal")
	ctx := context.Background()

	// Relógio fixo do lease durante a vida do nó (o lease nunca é visto expirado a meio de
	// um run vivo). O relógio pós-restart avança para lá do TTL (ver adiante).
	base := time.Unix(1_700_000_000, 0).UTC()
	liveClk := durable.ClockFunc(func() time.Time { return base })

	model := &completeOrBlockModel{blockMarker: "BLOCK-UNTIL-CANCEL", started: make(chan struct{})}
	cfg := tnBaseConfig()
	cfg.Model = model
	cfg.EventStorePath = esPath // <-- Event Store DURÁVEL REAL (AOS-170), não um mock

	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap durável: %v", err)
	}

	const ttl = time.Minute
	svc, err := NewNodeService(node, WithLeaseClock(liveClk), WithLeaseTTL(ttl))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}

	// (A) Runs que CONCLUEM — cada um grava um turno DURÁVEL (fsync) no Event Store.
	completed := []string{"run-a", "run-b", "run-c"}
	for _, id := range completed {
		if err := svc.Submit(ctx, svcGoal(id, "trabalho normal")); err != nil {
			t.Fatalf("Submit %q: %v", id, err)
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, id := range completed {
		oc, ok, werr := svc.Wait(waitCtx, id)
		if werr != nil || !ok {
			t.Fatalf("Wait %q: ok=%v err=%v", id, ok, werr)
		}
		if oc.Err != nil || oc.Panicked || !oc.Result.Terminated {
			t.Fatalf("run %q devia concluir limpo: %+v", id, oc)
		}
	}

	// Fotografa os eventos committed de cada stream ANTES do shutdown (o estado durável a
	// preservar). node.EventStore é o store durável real.
	before := make(map[string][]eventstore.Event, len(completed))
	for _, id := range completed {
		evs, err := node.EventStore.Read(ctx, id, 1)
		if err != nil {
			t.Fatalf("Read %q antes do shutdown: %v", id, err)
		}
		if len(evs) == 0 {
			t.Fatalf("run %q não gravou eventos committed (teste vacuoso)", id)
		}
		// A "sobrevivência" a provar é a de um TURNO DURÁVEL real, não de um evento qualquer:
		// cada run concluído tem de ter gravado pelo menos um "turn.recorded".
		hasTurn := false
		for _, ev := range evs {
			if ev.Type == agentruntime.EventTypeTurnRecorded {
				hasTurn = true
				break
			}
		}
		if !hasTurn {
			t.Fatalf("run %q não gravou nenhum turno durável (%q) — a prova de sobrevivência ficaria vacuosa", id, agentruntime.EventTypeTurnRecorded)
		}
		before[id] = evs
	}

	// (B) Um run EM-CURSO que só termina quando cancelado — força o cancelamento
	// cooperativo do shutdown no deadline.
	const blockID = "run-block"
	if err := svc.Submit(ctx, svcGoal(blockID, "BLOCK-UNTIL-CANCEL")); err != nil {
		t.Fatalf("Submit %q: %v", blockID, err)
	}
	<-model.started // o run em-curso está vivo (bloqueado no ctx)

	// O lease do run em-curso está VIVO e detido: outra "réplica" não o pode roubar
	// (prova que o run está realmente possuído no momento do shutdown).
	otherLM, err := durable.NewLeaseManager(node.EventStore, ttl, durable.WithLeaseClock(liveClk), durable.WithWorkerID("replica-outra"))
	if err != nil {
		t.Fatalf("NewLeaseManager (outra réplica): %v", err)
	}
	if _, err := otherLM.Claim(ctx, blockID); !errors.Is(err, durable.ErrLeaseHeld) {
		t.Fatalf("lease do run em-curso devia estar detido (ErrLeaseHeld), veio: %v", err)
	}

	// (C) SHUTDOWN com deadline curto → o dreno não conclui → cancela o run em-curso LIMPO
	// (cooperativo, fronteira de fim-de-turno) e devolve o erro do ctx. O run desenrola e
	// liberta a posse do lease.
	shCtx, shCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer shCancel()
	if err := svc.Shutdown(shCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown com run bloqueado devia devolver DeadlineExceeded (cancelou limpo), veio: %v", err)
	}
	if c := svc.InProgressCount(); c != 0 {
		t.Fatalf("após shutdown InProgressCount = %d, quero 0 (run cancelado desenrolou)", c)
	}

	// (D) Simula o REINÍCIO: fecha o nó (descarga final) e REABRE o Event Store durável do
	// MESMO caminho por reconstrução do WAL.
	if err := node.Close(); err != nil {
		t.Fatalf("node.Close: %v", err)
	}
	es2, err := eventstore.Open(esPath)
	if err != nil {
		t.Fatalf("reabrir Event Store durável (replay do WAL): %v", err)
	}
	defer es2.Close()

	// (1) Os eventos committed SOBREVIVEM ao reinício SEM perda NEM duplicação: cada stream
	// reabre byte-a-byte igual, com seq contíguo (gapless) e mesma cardinalidade.
	for _, id := range completed {
		got, err := es2.Read(ctx, id, 1)
		if err != nil {
			t.Fatalf("Read %q pós-restart: %v", id, err)
		}
		want := before[id]
		if len(got) != len(want) {
			t.Fatalf("stream %q: %d eventos pós-restart, quero %d (perda ou duplicação no replay)", id, len(got), len(want))
		}
		for i := range want {
			if got[i].Seq != want[i].Seq || got[i].EventID != want[i].EventID || got[i].Type != want[i].Type {
				t.Fatalf("stream %q evento %d divergiu no restart: got seq=%d id=%s type=%s", id, i, got[i].Seq, got[i].EventID, got[i].Type)
			}
			if got[i].Seq != uint64(i+1) {
				t.Fatalf("stream %q evento %d: seq = %d, quero %d (não-gapless)", id, i, got[i].Seq, i+1)
			}
		}
	}

	// (1b) O run CANCELADO no deadline NÃO deixou COMMIT PARCIAL: o seu stream de negócio não
	// contém NENHUM turno durável ("turn.recorded"). O cancelamento é cooperativo, na fronteira
	// de fim-de-turno — o run parou ANTES de gravar o turno, logo não há um turno meio-escrito a
	// sobreviver ao replay. Aqui a propriedade é ENCENADA (observada no store pós-restart), não
	// só inferida por composição. Stream inexistente (ErrStreamNotFound) é o caso forte: nada
	// committed de todo.
	blockEvs, berr := es2.Read(ctx, blockID, 1)
	if berr != nil && !errors.Is(berr, eventstore.ErrStreamNotFound) {
		t.Fatalf("Read %q pós-restart: %v", blockID, berr)
	}
	for _, ev := range blockEvs {
		if ev.Type == agentruntime.EventTypeTurnRecorded {
			t.Fatalf("run cancelado %q deixou um turno durável (%s seq=%d) — houve commit parcial na interrupção", blockID, ev.Type, ev.Seq)
		}
	}

	// (2) A IDEMPOTÊNCIA/dedup foi reconstruída no replay: re-appendar o MESMO
	// (RunID, StepID) de um evento committed devolve StatusDuplicate com o seq original —
	// não duplica. É a garantia que torna um retry pós-restart seguro.
	sample := before[completed[0]][0]
	res, err := es2.Append(ctx, completed[0], eventstore.EventInput{
		Type:     sample.Type,
		RunID:    sample.RunID,
		StepID:   sample.StepID,
		Producer: eventstore.Producer{NHIID: "nhi:replay-probe"},
	})
	if err != nil {
		t.Fatalf("re-append (probe de idempotência): %v", err)
	}
	if res.Status != eventstore.StatusDuplicate {
		t.Fatalf("idempotência não sobreviveu ao replay: status = %q, quero %q", res.Status, eventstore.StatusDuplicate)
	}
	if res.Seq != sample.Seq {
		t.Fatalf("dedup pós-restart devolveu seq %d, quero o original %d", res.Seq, sample.Seq)
	}

	// (3) O LEASE do run cancelado é RECLAMÁVEL após o reinício (foi liberto/expira) e um
	// novo claim minta um token ESTRITAMENTE MAIOR — sem dupla-execução: um token residual
	// da execução anterior é fenced. Relógio pós-restart avança para lá do TTL (o lease
	// durável expira).
	postClk := durable.ClockFunc(func() time.Time { return base.Add(2 * ttl) })
	lm2, err := durable.NewLeaseManager(es2, ttl, durable.WithLeaseClock(postClk), durable.WithWorkerID("replica-pos-restart"))
	if err != nil {
		t.Fatalf("NewLeaseManager pós-restart: %v", err)
	}
	newLease, err := lm2.Claim(ctx, blockID)
	if err != nil {
		t.Fatalf("o lease do run cancelado devia ser reclamável após o restart (liberto/expirado): %v", err)
	}
	if !newLease.Token.Valid() {
		t.Fatalf("novo lease com token inválido: %v", newLease.Token)
	}
	// Monotonicidade do fencing: QUALQUER token abaixo do corrente (ex.: o token residual
	// da execução anterior) é rejeitado — nenhuma escrita tardia da 1ª execução committa,
	// logo não há dupla-execução.
	stale := durable.Lease{
		RunID:     blockID,
		Token:     durable.FencingToken(newLease.Token.Value() - 1),
		Worker:    "replica-antiga",
		TTL:       ttl,
		ExpiresAt: base.Add(ttl),
	}
	if _, err := lm2.Heartbeat(ctx, stale); !errors.Is(err, durable.ErrLeaseSuperseded) {
		t.Fatalf("token residual da execução anterior devia ser fenced (ErrLeaseSuperseded) após o novo claim, veio: %v", err)
	}
}
