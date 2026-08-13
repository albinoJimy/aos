package plandispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// adapters.go — A LIGAÇÃO REAL do registo de decisões de ramo (ADR-022 §2.1) ao
// subsistema que já existe no nó: o domínio de eventos `aos.planner.v1` (AOS-235)
// sobre o Event Store append-only. Não é um mecanismo novo — é o fio.
//
// Vive AQUI, e não no wiring, por uma razão de prova: sem este adaptador, um teste
// da cadeia teria de substituir o Event Store por um double — e um teste que
// substitui a peça vizinha não prova composição.
//
// O adaptador simétrico do ORÇAMENTO (a porta [BranchBudget] ligada à hierarquia CAS
// de AOS-008) NÃO pode viver aqui: importaria `control-plane/budget`, e o guard de
// imports deste pacote (fronteira ADR-018) admite apenas `plan` e `plannerevents`.
// Vive no pacote irmão `orchestrator/planbudget`, e liga-se por tipo estrutural — a
// porta é uma interface com tipos de stdlib, pelo que o adaptador não precisa sequer
// de importar este pacote.

// ErrJournalDeps — Recorder ou EventReader em falta (fail-closed na construção).
var ErrJournalDeps = errors.New("plandispatch: EventJournal exige Recorder + EventReader")

// EventJournal implementa [BranchJournal] sobre o domínio `aos.planner.v1`: escreve
// com [plannerevents.Recorder] (que apensa `plan.branch_decided` com step id
// determinístico por nó, logo IDEMPOTENTE por (plan_id, node_id)) e lê com
// [plannerevents.Reconstruct] (read-only, ordem total do stream, bytes preservados).
//
// A assimetria escrita/leitura é a mesma do resto do domínio e é o que torna o
// replay estrutural: a leitura passa por um [plannerevents.EventReader] — uma porta
// que só sabe LER — pelo que o caminho de reconstrução não tem sequer como apensar
// um facto novo nem consultar um modelo.
type EventJournal struct {
	rec    *plannerevents.Recorder
	reader plannerevents.EventReader
}

// NewEventJournal liga o registo de decisões de ramo ao Event Store. Ambas as
// dependências são obrigatórias (fail-closed, [ErrJournalDeps]).
func NewEventJournal(rec *plannerevents.Recorder, reader plannerevents.EventReader) (*EventJournal, error) {
	if rec == nil || reader == nil {
		return nil, ErrJournalDeps
	}
	return &EventJournal{rec: rec, reader: reader}, nil
}

// Decisions reconstrói o stream do plano e projecta as decisões de ramo por
// node_id. Percorre os factos pela ORDEM DE APENSO e deixa o ÚLTIMO vencer — uma
// escolha sem consequência prática (a idempotency_key `plan_id:planstep:branch_decided:<node_id>`
// impede um segundo facto para o mesmo nó) mas que mantém a projecção total e
// determinística mesmo perante um stream que, por qualquer razão histórica, tenha
// repetições.
//
// Fail-closed: um payload de `plan.branch_decided` que não desserialize, ou que
// chegue sem node_id/digest, aborta a leitura — nunca se ignora um facto do
// registo, porque ignorá-lo faria o despachante RE-AVALIAR uma decisão que já foi
// tomada (exactamente o que ADR-010 proíbe).
func (j *EventJournal) Decisions(ctx context.Context, planID string) (map[string]BranchDecision, error) {
	events, err := plannerevents.Reconstruct(ctx, j.reader, planID)
	if err != nil {
		if plannerevents.IsStreamAbsent(err) {
			// Stream ainda sem factos: é o estado INICIAL legítimo de um plano (nenhuma
			// decisão tomada), não uma avaria. Devolver «nenhuma decisão» é o que permite
			// que a PRIMEIRA passagem de despacho avalie; tratá-lo como erro tornaria a
			// primeira decisão impossível e o despacho condicional inutilizável.
			return map[string]BranchDecision{}, nil
		}
		return nil, err
	}
	out := make(map[string]BranchDecision)
	for _, ev := range events {
		if ev.Type != plannerevents.EventBranchDecided {
			continue
		}
		var p plannerevents.BranchDecidedPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return nil, fmt.Errorf("plandispatch: payload de decisão de ramo ilegível (seq %d): %w", ev.Seq, err)
		}
		if p.NodeID == "" || p.ConditionDigest == "" {
			return nil, fmt.Errorf("plandispatch: decisão de ramo incompleta no stream (seq %d)", ev.Seq)
		}
		out[p.NodeID] = BranchDecision{
			NodeID:          p.NodeID,
			Taken:           p.Taken,
			ConditionDigest: p.ConditionDigest,
			Sources:         p.Sources,
		}
	}
	return out, nil
}

// Record apensa `plan.branch_decided`. A idempotência por (plan_id, node_id) é do
// Event Store (idempotency_key composta do step id determinístico), não deste
// adaptador — que não guarda estado nenhum.
func (j *EventJournal) Record(ctx context.Context, planID string, d BranchDecision) error {
	_, err := j.rec.RecordBranchDecided(ctx, plannerevents.BranchDecidedPayload{
		PlanID:          planID,
		NodeID:          d.NodeID,
		Taken:           d.Taken,
		ConditionDigest: d.ConditionDigest,
		Sources:         d.Sources,
	})
	return err
}

// ErrLifecycleResultsDeps — LifecycleView em falta (fail-closed na construção).
var ErrLifecycleResultsDeps = errors.New("plandispatch: LifecycleResults exige LifecycleView")

// LifecycleResults implementa [ResultView] projectando o observável
// `terminal_state` de ADR-022 §2.1 a partir da autoridade que JÁ existe: a máquina
// de estados durável do run (AOS-017), lida pela porta [LifecycleView] que o
// despachante já tem ligada.
//
// PORQUE EXISTE. Sem ele, das TRÊS portas de ramos condicionais duas tinham
// adaptador real ([EventJournal], planbudget.TreeBudgetMeter) e a terceira — a
// FONTE DO OBSERVÁVEL — só tinha o double dos testes. Um operador que ligasse o
// despacho condicional a vivo não encontrava nada para lhe passar, e
// [WithConditionalBranches] ignora a opção quando qualquer porta é nil: o primeiro
// plano com `conditional_on` era recusado com [ErrConditionalUnsupported]. Uma
// extensão verde em testes e inutilizável a vivo é a lacuna de composição que só o
// run real apanha — e é essa que este adaptador fecha.
//
// # O QUE ESTE ADAPTADOR *NÃO* PROJECTA (declarado, não esquecido)
//
//   - `verdict` — o veredicto estruturado vem do facto `plan.verdict_recorded`
//     (AOS-271), projectado por [ResultFromVerdict]; este adaptador não o conhece e
//     devolve [VerdictAbsent]. A admissão JÁ NÃO recusa em bloco os ramos sobre
//     `verdict` (AOS-271 substituiu o interruptor datado pelas regras reais de §2.2),
//     pelo que o fail-closed LOUD passou para aqui: um observável AUSENTE deixa o
//     predicado INDECIDO ([evalPredicate]) e o nó em `waiting_condition` — nunca falso,
//     que seria podar o ramo e REGISTAR a poda. Enquanto um wiring não ligar uma
//     [ResultView] que projecte veredictos, um plano com ramo de qualidade fica parado
//     e VISÍVEL, em vez de mutilado em silêncio.
//   - `metric` — as métricas declaradas do resultado não têm produtor neste adaptador;
//     o mapa fica nil e um predicado sobre métrica fica igualmente INDECIDO. Mesma
//     disciplina, mesma razão.
//
// Simétrico de [EventJournal] na assimetria que interessa: LÊ, nunca escreve.
type LifecycleResults struct {
	view LifecycleView
}

// NewLifecycleResults liga a fonte do observável `terminal_state` à vista do ciclo
// de vida do run. Fail-closed ([ErrLifecycleResultsDeps]).
func NewLifecycleResults(view LifecycleView) (*LifecycleResults, error) {
	if view == nil {
		return nil, ErrLifecycleResultsDeps
	}
	return &LifecycleResults{view: view}, nil
}

// Result implementa [ResultView]. ok=false para qualquer estado NÃO-TERMINAL — é
// «ainda não registado», e a condição fica INDECIDA (o nó espera), nunca falsa por
// omissão de leitura. Um erro da porta é fail-closed e SURFACED pela passagem.
func (r *LifecycleResults) Result(ctx context.Context, planID, nodeID string) (NodeResultRecord, bool, error) {
	st, err := r.view.State(ctx, planID, nodeID)
	if err != nil {
		return NodeResultRecord{}, false, err
	}
	switch st {
	case NodeComplete:
		return NodeResultRecord{Terminal: TerminalComplete}, true, nil
	case NodeFailed:
		return NodeResultRecord{Terminal: TerminalFailed}, true, nil
	default:
		return NodeResultRecord{}, false, nil
	}
}
