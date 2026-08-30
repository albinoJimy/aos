package runlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/orchestrator/plandispatch"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// readers.go — AS PORTAS QUE O ESCALONADOR DERIVA (ADR-023 §2.2).
//
// Todas as portas implementadas aqui são de LEITURA. É essa assimetria — e não uma
// convenção de quem as usa — que mantém a fronteira do ADR-018: o despachante
// continua sem conhecer o Event Store, e a sua única autoridade é o que o log já
// regista.
//
// # Porque SNAPSHOTS e não leituras vivas
//
// O despachante faz uma PASSAGEM: avalia todos os nós de um plano de uma vez. Se cada
// `State`/`Result` relesse o log no seu instante, uma passagem veria um MOSAICO de
// instantes diferentes — o nó A avaliado antes de um facto que o nó B já viu. Uma
// decisão de ramo tomada sobre um mosaico não é reproduzível, e a reprodutibilidade é
// precisamente o que ADR-022 §2.4(3) exige da decisão de ramo.
//
// O `BranchJournal` do despachante já declara esta disciplina para o registo de ramos
// («dá à passagem uma vista COERENTE, em vez de um mosaico de leituras intercaladas»).
// Estas portas aplicam-lhe a mesma regra: relê-se UMA vez, e a passagem inteira corre
// sobre esse retrato imutável.
//
// Custo secundário, e é o que torna a escolha barata: reconstruir por nó seria O(n)
// leituras do stream por passagem de n nós.

// ErrForeignPlan — pediu-se a um leitor amarrado a um run o estado de um plano
// diferente. Fail-closed: um leitor responde pelo plano do run a que foi amarrado e
// por mais nenhum. Devolver [plandispatch.NodeUnknown] em silêncio faria um plano
// errado parecer um plano por começar.
var ErrForeignPlan = errors.New("runlifecycle: leitura pedida para um plan_id que não é o do run amarrado")

// LifecycleReader é a fonte da porta [plandispatch.LifecycleView]: DERIVA o estado do
// ciclo de vida por-nó do log do run, por [orchestrator.RebuildDAG] — a mesma função
// pura que o replay usa (ADR-010), não uma segunda leitura do log com regras próprias.
//
// Amarrado a UM (plan_id, run_id) na construção. Não escreve nada, por caminho nenhum.
type LifecycleReader struct {
	store  EventStore
	planID string
	runID  string
}

// NewLifecycleReader amarra um leitor ao par (plan_id, run_id). Ambos obrigatórios.
func NewLifecycleReader(store EventStore, planID, runID string) (*LifecycleReader, error) {
	if store == nil {
		return nil, ErrDeps
	}
	if planID == "" || runID == "" {
		return nil, fmt.Errorf("%w: plan_id/run_id", ErrEmptyRunID)
	}
	return &LifecycleReader{store: store, planID: planID, runID: runID}, nil
}

// Snapshot relê o log UMA vez e devolve o retrato imutável sobre o qual uma passagem
// de despacho inteira corre. Fail-closed: um log que já não sustente um DAG válido
// aborta aqui em vez de produzir uma vista parcial.
func (r *LifecycleReader) Snapshot(ctx context.Context) (*LifecycleSnapshot, error) {
	dag, err := orchestrator.RebuildDAG(ctx, storeAdapter{r.store}, r.runID)
	if err != nil {
		return nil, fmt.Errorf("runlifecycle: retrato do ciclo de vida do run %q: %w", r.runID, err)
	}
	return &LifecycleSnapshot{planID: r.planID, dag: dag}, nil
}

// LifecycleSnapshot é o retrato IMUTÁVEL do ciclo de vida por-nó num instante.
// Satisfaz [plandispatch.LifecycleView].
type LifecycleSnapshot struct {
	planID string
	dag    *orchestrator.DAG
}

var _ plandispatch.LifecycleView = (*LifecycleSnapshot)(nil)

// State devolve o estado do nó, PROJECTADO da máquina durável de AOS-017 para o enum
// do despachante. Um nó ausente do grafo é [plandispatch.NodeUnknown] — o sentinela
// fail-closed do despachante: não despacha e não satisfaz dependentes.
func (s *LifecycleSnapshot) State(_ context.Context, planID, nodeID string) (plandispatch.NodeState, error) {
	if planID != s.planID {
		return plandispatch.NodeUnknown, fmt.Errorf("%w: pedido %q, amarrado %q", ErrForeignPlan, planID, s.planID)
	}
	st, ok := s.dag.State(nodeID)
	if !ok {
		return plandispatch.NodeUnknown, nil
	}
	return projectNodeState(st), nil
}

// projectNodeState traduz o estado da máquina de AOS-017 para o enum do despachante.
//
// A tradução é DELIBERADAMENTE conservadora nos estados intermédios. `waiting_on_tool`,
// `waiting_on_human`, `paused` e `compensating` mapeiam para
// [plandispatch.NodeRunning] — «em voo»: não re-despacha e NÃO satisfaz dependentes.
// Nenhum deles é terminal, e tratá-los como falha terminal seria libertar dependentes
// sobre trabalho que ainda pode concluir com sucesso; tratá-los como pendentes seria
// re-despachar um nó que já está em voo. «Em voo» é a única leitura que não mente.
//
// `compensating` merece a nota: sai para `ready` (a saga refaz o trabalho), pelo que
// é o menos terminal de todos — classificá-lo como falha seria o erro mais caro.
//
// Um estado desconhecido (log legado/corrompido) é [plandispatch.NodeUnknown], o
// sentinela fail-closed, nunca um palpite.
func projectNodeState(s state.State) plandispatch.NodeState {
	switch s {
	case state.Ready:
		return plandispatch.NodePending
	case state.Running, state.WaitingOnTool, state.WaitingOnHuman, state.Paused, state.Compensating:
		return plandispatch.NodeRunning
	case state.Complete:
		return plandispatch.NodeComplete
	case state.Failed, state.Killed, state.TimedOut:
		return plandispatch.NodeFailed
	default:
		return plandispatch.NodeUnknown
	}
}

// ResultReader é a fonte da porta [plandispatch.ResultView]: DERIVA o resultado
// registado de um nó dos factos `plan.verdict_recorded` do stream do plano, pela
// projecção pura [plandispatch.ResultFromVerdict] — a função que o próprio despachante
// declara como sendo «a outra metade da ponta a ser atada» (AOS-271).
//
// # O que fecha o DEF-272 do lado do CONSUMO
//
// O contrato tipado do veredicto e a projecção já existiam; o que não existia era
// alguém que os LESSE de um log real. Sem isto, um ramo de qualidade admitido ficava
// com o observável [plandispatch.VerdictAbsent] para sempre — INDECIDO (o nó espera),
// que é a direcção segura, mas também a que faz metade de um organigrama aprovado
// nunca correr.
type ResultReader struct {
	store  EventStore
	planID string
}

// NewResultReader amarra um leitor de resultados ao stream de um plano.
func NewResultReader(store EventStore, planID string) (*ResultReader, error) {
	if store == nil {
		return nil, ErrDeps
	}
	if planID == "" {
		return nil, fmt.Errorf("%w: plan_id", ErrEmptyRunID)
	}
	return &ResultReader{store: store, planID: planID}, nil
}

// Snapshot relê o stream do plano UMA vez e indexa os veredictos por node_id.
func (r *ResultReader) Snapshot(ctx context.Context) (*ResultSnapshot, error) {
	events, err := readStream(ctx, r.store, r.planID)
	if err != nil {
		return nil, fmt.Errorf("runlifecycle: retrato dos resultados do plano %q: %w", r.planID, err)
	}
	byNode := make(map[string]plandispatch.NodeResultRecord)
	for i := range events {
		if events[i].Type != plannerevents.EventVerdictRecorded {
			continue
		}
		var p plannerevents.VerdictRecordedPayload
		if uerr := json.Unmarshal(events[i].Payload, &p); uerr != nil {
			return nil, fmt.Errorf("runlifecycle: veredicto ilegível no plano %q (seq=%d): %w", r.planID, events[i].Seq, uerr)
		}
		// O step id do veredicto é único por nó (`planstep:verdict_recorded:<node>`),
		// pelo que o Event Store já garante um só facto por verificador. Se ainda assim
		// houvesse dois, vence o PRIMEIRO: um veredicto é imutável, e deixar um segundo
		// substituí-lo seria a porta por onde um `pass` tardio apaga um `fail`.
		if _, seen := byNode[p.NodeID]; seen {
			continue
		}
		byNode[p.NodeID] = plandispatch.ResultFromVerdict(p)
	}
	return &ResultSnapshot{planID: r.planID, byNode: byNode}, nil
}

// ResultSnapshot é o retrato IMUTÁVEL dos resultados registados de um plano.
// Satisfaz [plandispatch.ResultView].
type ResultSnapshot struct {
	planID string
	byNode map[string]plandispatch.NodeResultRecord
}

var _ plandispatch.ResultView = (*ResultSnapshot)(nil)

// Result devolve o resultado registado de um nó. `ok=false` significa «ainda não
// registado» — a condição fica INDECIDA (o nó espera), nunca falsa por omissão.
func (s *ResultSnapshot) Result(_ context.Context, planID, nodeID string) (plandispatch.NodeResultRecord, bool, error) {
	if planID != s.planID {
		return plandispatch.NodeResultRecord{}, false, fmt.Errorf("%w: pedido %q, amarrado %q", ErrForeignPlan, planID, s.planID)
	}
	rec, ok := s.byNode[nodeID]
	return rec, ok, nil
}

// PayloadReader é a fonte da porta [plandispatch.PayloadView]: DERIVA as referências
// publicadas dos factos `plan.payload_published`, pela projecção pura
// [plandispatch.RefFromPublished].
//
// # O que fecha o DEF-273 do lado do CONSUMO
//
// Não existia implementação nenhuma desta porta, pelo que `PayloadResolver.Inbox`
// devolvia [plandispatch.ErrPayloadNotPublished] em qualquer passagem: nenhum
// consumidor recebia referência nenhuma num run real. O contrato tipado estava
// fechado dos dois lados e vazio no meio.
type PayloadReader struct {
	store  EventStore
	planID string
}

// payloadKey identifica um contrato publicado: (produtor, output).
type payloadKey struct{ node, output string }

// NewPayloadReader amarra um leitor de payloads ao stream de um plano.
func NewPayloadReader(store EventStore, planID string) (*PayloadReader, error) {
	if store == nil {
		return nil, ErrDeps
	}
	if planID == "" {
		return nil, fmt.Errorf("%w: plan_id", ErrEmptyRunID)
	}
	return &PayloadReader{store: store, planID: planID}, nil
}

// Snapshot relê o stream do plano UMA vez e indexa as referências por (produtor, output).
func (r *PayloadReader) Snapshot(ctx context.Context) (*PayloadSnapshot, error) {
	events, err := readStream(ctx, r.store, r.planID)
	if err != nil {
		return nil, fmt.Errorf("runlifecycle: retrato dos payloads do plano %q: %w", r.planID, err)
	}
	byContract := make(map[payloadKey]plandispatch.PayloadRef)
	for i := range events {
		if events[i].Type != plannerevents.EventPayloadPublished {
			continue
		}
		var p plannerevents.PayloadPublishedPayload
		if uerr := json.Unmarshal(events[i].Payload, &p); uerr != nil {
			return nil, fmt.Errorf("runlifecycle: payload ilegível no plano %q (seq=%d): %w", r.planID, events[i].Seq, uerr)
		}
		k := payloadKey{node: p.NodeID, output: p.Output}
		// Primeiro vence, pela mesma razão do veredicto: «um nó não publica duas
		// versões do mesmo output, e não há caminho por onde uma segunda referência
		// substitua em silêncio a que o consumidor já leu». É isso que separa isto de
		// um blackboard, onde o valor muda debaixo de quem o lê.
		if _, seen := byContract[k]; seen {
			continue
		}
		byContract[k] = plandispatch.RefFromPublished(p)
	}
	return &PayloadSnapshot{planID: r.planID, byContract: byContract}, nil
}

// PayloadSnapshot é o retrato IMUTÁVEL das referências publicadas de um plano.
// Satisfaz [plandispatch.PayloadView].
type PayloadSnapshot struct {
	planID     string
	byContract map[payloadKey]plandispatch.PayloadRef
}

var _ plandispatch.PayloadView = (*PayloadSnapshot)(nil)

// Payload devolve a referência publicada para (produtor, output). `ok=false` é a
// espera legítima do consumidor (o produtor não terminou), nunca um valor por omissão.
func (s *PayloadSnapshot) Payload(_ context.Context, planID, producerNodeID, output string) (plandispatch.PayloadRef, bool, error) {
	if planID != s.planID {
		return plandispatch.PayloadRef{}, false, fmt.Errorf("%w: pedido %q, amarrado %q", ErrForeignPlan, planID, s.planID)
	}
	ref, ok := s.byContract[payloadKey{node: producerNodeID, output: output}]
	return ref, ok, nil
}

// readStream relê um stream inteiro, tratando a ausência como stream vazio — um plano
// sem factos ainda não é um erro, é um plano por começar.
func readStream(ctx context.Context, store EventStore, streamID string) ([]eventstore.Event, error) {
	events, err := store.Read(ctx, streamID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return events, nil
}

// storeAdapter adapta [EventStore] à forma que o orquestrador declara. As duas
// interfaces são estruturalmente idênticas; o adaptador existe só porque Go não
// converte interfaces nominais entre pacotes.
type storeAdapter struct{ EventStore }

var _ orchestrator.EventStore = storeAdapter{}

// unmarshalPayload desserializa um payload de evento. Existe como função nomeada para
// que os leitores deste pacote partilhem a MESMA forma de ler o log — e para que uma
// mudança de codificação tenha um só sítio onde acontecer.
func unmarshalPayload(raw []byte, into any) error { return json.Unmarshal(raw, into) }
