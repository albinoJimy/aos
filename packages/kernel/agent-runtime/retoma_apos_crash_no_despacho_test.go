package agentruntime_test

// A RETOMA, DEPOIS DE UM CRASH A MEIO DO DESPACHO.
//
// O teste irmão (`confirma_so_o_que_produziu_efeito_test.go`) observa a DECISÃO de confirmar, na
// porta [agentruntime.Checkpointer]. Este observa a CONSEQUÊNCIA: liga o laço a um
// [durable.EventStoreCheckpointer] real e pergunta ao [durable.Resumer] real onde a retoma
// recomeçaria.
//
// COMO SE SIMULA O CRASH, e porque não é artifício. A primeira tentativa deste teste corria um
// run até ao fim e olhava para o cursor — mas num run COMPLETO o último checkpoint é sempre o do
// turno, e a pergunta ficava sem sentido. O cenário que importa é parar ENTRE o despacho e a
// verificação do turno.
//
// O próprio laço dá o gancho: uma falha de checkpoint é FATAL (`return res, err` em todas as
// cinco chamadas a `rt.cp`). Um checkpointer que falhe na fase `verified` deixa o run parado
// exactamente na fronteira que interessa — despacho gravado, turno por verificar — e é um modo de
// falha real, não uma encenação: o disco enche, o Event Store fica indisponível, o processo morre.
//
// PORQUE EXISTE. Um subagente da auditoria de 2026-08-27 notou que o harness de replay nunca
// exercita o `Resumer` — verifica o motor de replay contra si próprio. Sem isto, a guarda do
// `cpActivity` estaria coberta só do lado da intenção.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// checkpointerQueMorreAoVerificar delega tudo no checkpointer durável real, MENOS a fase
// `verified` — onde falha, parando o laço na fronteira pós-despacho.
type checkpointerQueMorreAoVerificar struct{ real agentruntime.Checkpointer }

func (c checkpointerQueMorreAoVerificar) Checkpoint(ctx context.Context, cp agentruntime.Checkpoint) error {
	if cp.Phase == agentruntime.PhaseVerified {
		return errors.New("crash simulado: o checkpoint de verificacao nao gravou")
	}
	return c.real.Checkpoint(ctx, cp)
}

// retomaAposCrashNoDespacho corre um turno com UMA tool call, mata o run na fronteira
// pós-despacho, e devolve o ponto de retoma apurado pelo `Resumer` real.
func retomaAposCrashNoDespacho(t *testing.T, registar func(rm *referencemonitor.Monitor)) durable.ResumePoint {
	t.Helper()
	const runID = "run-crash"

	store := novoStore()
	real, err := durable.NewCheckpointer(store)
	if err != nil {
		t.Fatalf("compor o checkpointer duravel: %v", err)
	}

	rm := referencemonitor.New(referencemonitor.WithHooks(
		referencemonitor.IdentityStub{}, referencemonitor.PolicyStub{},
		referencemonitor.BudgetStub{}, referencemonitor.EgressStub{},
		referencemonitor.AuditStub{},
	))
	registar(rm)

	modelo := agentruntime.ModelClientFunc(func(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		return agentruntime.ModelResponse{
			Text: "uso a tool",
			ToolCalls: []agentruntime.ToolInvocation{{
				ToolID: "t", Capability: "cap:fs.read", ResourceType: "file",
				ResourceValue: "x", Input: []byte("x"),
			}},
		}, nil
	})

	rt := agentruntime.New(modelo, rm, agentruntime.NewTurnRecorder(coletorMudo{}),
		agentruntime.WithCheckpointer(checkpointerQueMorreAoVerificar{real: real}))
	if _, err := rt.Run(context.Background(), agentruntime.Goal{
		RunID: runID, Principal: referencemonitor.Principal{NHIID: "a"},
		Scope: []string{"cap:fs.read"}, System: "s", Objective: "o", MaxTurns: 4,
	}); err == nil {
		t.Fatal("o crash simulado devia ter parado o run — o gancho deixou de funcionar e o teste " +
			"passaria a observar um run completo, que e outra coisa")
	}

	res, err := durable.NewResumer(store)
	if err != nil {
		t.Fatalf("compor o resumer: %v", err)
	}
	ponto, err := res.Resume(context.Background(), runID)
	if err != nil {
		t.Fatalf("apurar o ponto de retoma: %v", err)
	}
	return ponto
}

// TestRetomaAposCrash_NaoSaltaToolFalhada é a propriedade que a guarda do `cpActivity` existe
// para garantir: o ledger não memorizou o passo e declara-o RETRIÁVEL, logo o cursor não pode
// dizer que ele ficou feito — senão a retoma salta-o e a acção nunca executa.
func TestRetomaAposCrash_NaoSaltaToolFalhada(t *testing.T) {
	ponto := retomaAposCrashNoDespacho(t, func(rm *referencemonitor.Monitor) {
		_ = rm.Register("t", func(context.Context, []byte) ([]byte, error) {
			return nil, errors.New("HTTP 500 a jusante")
		})
	})

	if strings.Contains(ponto.LastConfirmed.ConfirmedStepID, "-tool-") {
		t.Errorf("o cursor confirmou uma tool FALHADA: a retoma SALTA-A e a accao nunca executa.\n"+
			"LastConfirmed=%q NextStepID=%q", ponto.LastConfirmed.ConfirmedStepID, ponto.NextStepID)
	}
}

// TestRetomaAposCrash_NaoSaltaToolNegada cobre o caso que já existia antes da correcção: o RM
// negou por default-deny e o efeito nem chegou a correr.
func TestRetomaAposCrash_NaoSaltaToolNegada(t *testing.T) {
	ponto := retomaAposCrashNoDespacho(t, func(*referencemonitor.Monitor) {})

	if strings.Contains(ponto.LastConfirmed.ConfirmedStepID, "-tool-") {
		t.Errorf("o cursor confirmou uma tool NEGADA, cujo efeito nunca correu.\n"+
			"LastConfirmed=%q NextStepID=%q", ponto.LastConfirmed.ConfirmedStepID, ponto.NextStepID)
	}
}

// TestRetomaAposCrash_SaltaToolJaAplicada é a metade que impede a guarda de virar um silenciador.
// Uma tool PERMITIDA que correu TEM de ficar confirmada — senão a retoma REPETE um efeito externo
// já aplicado, que é o dano oposto e igualmente grave.
//
// Sem este caso, `if false` no lugar da guarda passaria os dois testes acima.
func TestRetomaAposCrash_SaltaToolJaAplicada(t *testing.T) {
	ponto := retomaAposCrashNoDespacho(t, func(rm *referencemonitor.Monitor) {
		_ = rm.Register("t", func(_ context.Context, in []byte) ([]byte, error) {
			return append([]byte("ok:"), in...), nil
		})
	})

	if !strings.Contains(ponto.LastConfirmed.ConfirmedStepID, "-tool-") {
		t.Errorf("uma tool PERMITIDA que correu NAO ficou confirmada: a retoma REPETE o efeito.\n"+
			"LastConfirmed=%q NextStepID=%q", ponto.LastConfirmed.ConfirmedStepID, ponto.NextStepID)
	}
}

// storeEmMemoria é o mínimo que [durable.EventStore] exige: append com seq crescente por stream
// e leitura por ordem. Sem dedup e sem durabilidade — o que se observa aqui é o CURSOR, e um
// store real só acrescentaria fsync ao tempo do teste.
type storeEmMemoria struct {
	mu      sync.Mutex
	streams map[string][]eventstore.Event
}

func novoStore() *storeEmMemoria {
	return &storeEmMemoria{streams: map[string][]eventstore.Event{}}
}

func (s *storeEmMemoria) Append(_ context.Context, streamID string, in eventstore.EventInput, _ ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq := uint64(len(s.streams[streamID])) + 1
	ev := eventstore.Event{
		StreamID: streamID, Seq: seq, Type: in.Type, Payload: in.Payload,
		SchemaVersion: in.SchemaVersion,
	}
	s.streams[streamID] = append(s.streams[streamID], ev)
	return eventstore.AppendResult{Seq: seq, Status: eventstore.StatusCommitted, Event: ev}, nil
}

func (s *storeEmMemoria) Read(_ context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []eventstore.Event
	for _, e := range s.streams[streamID] {
		if e.Seq >= fromSeq {
			out = append(out, e)
		}
	}
	return out, nil
}
